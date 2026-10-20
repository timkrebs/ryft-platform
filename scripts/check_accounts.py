#!/usr/bin/env python3
"""Account flows against a real Supabase project and the deployed platform.

Two throwaway users are created and confirmed through the Supabase admin API (the
email-confirmation step itself is a manual test from the app), signed in with the
password grant, checked for isolation, and deleted through DELETE /v1/me, which must
remove their Supabase sign-in too. They are cleaned up even if a check fails.

  SUPABASE_URL=https://<ref>.supabase.co SUPABASE_ANON_KEY=<publishable> SUPABASE_SECRET_KEY=sb_secret_… \\
  API_URL=https://api.ryftapp.de INGEST_URL=https://api.ryftapp.de scripts/check_accounts.py
"""
import json, os, secrets, string, sys, time, urllib.error, urllib.request, uuid
from datetime import datetime, timedelta, timezone

SB = os.environ["SUPABASE_URL"].rstrip("/")
ANON = os.environ["SUPABASE_ANON_KEY"]
SECRET = os.environ["SUPABASE_SECRET_KEY"]
API = os.environ.get("API_URL", "http://localhost:8082").rstrip("/")
INGEST = os.environ.get("INGEST_URL", "http://localhost:8081").rstrip("/")
DOMAIN = os.environ.get("SMOKE_EMAIL_DOMAIN", "ryftapp.de")


def call(method, url, body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw.strip() else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode(errors="replace")[:200]


def admin_headers():
    h = {"apikey": SECRET}
    if SECRET.count(".") == 2:  # legacy service_role JWT
        h["Authorization"] = f"Bearer {SECRET}"
    return h


def fail(msg):
    print(f"✗ {msg}")
    raise SystemExit(1)


def check(cond, msg):
    if not cond:
        fail(msg)


def ok(msg):
    print(f"✓ {msg}")


class User:
    def __init__(self, label):
        self.label = label
        self.email = f"ryft-smoke-{uuid.uuid4().hex[:12]}@{DOMAIN}"
        alphabet = string.ascii_letters + string.digits
        self.password = "".join(secrets.choice(alphabet) for _ in range(20)) + "7a"
        status, body = call("POST", f"{SB}/auth/v1/admin/users", {"email": self.email, "password": self.password,
                                                                   "email_confirm": True}, admin_headers())
        check(status in (200, 201) and body and body.get("id"), f"admin create {label}: {status} {body}")
        self.id = body["id"]
        self.deleted = False

    def sign_in(self):
        status, body = call("POST", f"{SB}/auth/v1/token?grant_type=password",
                            {"email": self.email, "password": self.password}, {"apikey": ANON})
        return status, (body or {}).get("access_token") if isinstance(body, dict) else None

    def api(self, method, path, body=None, base=API, token=None):
        return call(method, base + path, body, {"Authorization": f"Bearer {token or self.token}"})


iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%SZ")


def sync_one_day(u):
    """A night of sleep and a morning workout with heart rate, two days back."""
    end = datetime.now(timezone.utc).replace(hour=6, minute=0, second=0, microsecond=0) - timedelta(days=2)
    start = end - timedelta(hours=7)
    run = end + timedelta(hours=2)
    hr = [{"type": "heart_rate", "start": iso(run + timedelta(minutes=m)), "end": iso(run + timedelta(minutes=m)),
           "value": 150, "unit": "bpm", "source": "smoke"} for m in range(31)]
    hrv = [{"type": "hrv_sdnn", "start": iso(end - timedelta(hours=1)), "end": iso(end - timedelta(hours=1)),
            "value": 55, "unit": "ms", "source": "smoke"}]
    wid = str(uuid.uuid4())
    body = {"device_id": str(uuid.uuid4()), "samples": hr + hrv,
            "sleep": [{"start": iso(start), "end": iso(end), "stage": "core", "source": "smoke"}],
            "workouts": [{"id": wid, "activity": "running", "start": iso(run), "end": iso(run + timedelta(minutes=30)),
                          "source": "smoke"}]}
    status, resp = u.api("POST", "/v1/sync", body, base=INGEST)
    check(status == 200, f"sync {u.label}: {status} {resp}")
    return wid


def main():
    users = []
    try:
        a, b = User("A"), User("B")
        users += [a, b]
        for u in (a, b):
            status, u.token = u.sign_in()
            check(status == 200 and u.token, f"password sign-in {u.label}: {status}")
        ok(f"two confirmed users created and signed in ({a.email}, {b.email})")

        status, me = a.api("GET", "/v1/me")
        check(status == 200 and me["id"] == a.id and me["email"] == a.email and me["provider"] == "email",
              f"/v1/me must create A's row from the token: {status} {me}")
        ok("/v1/me created the user row (email, provider email)")

        # Beta: a new account needs a code while the gate is on (ADMIN_API_KEY makes
        # one per user), and a consent to the current policy before it may sync.
        status, cfg = a.api("GET", "/v1/config")
        check(status == 200 and cfg.get("privacy_policy_version"), f"/v1/config: {status} {cfg}")
        for u in (a, b):
            status, me_u = u.api("GET", "/v1/me")
            if not me_u.get("beta_access"):
                admin = os.environ.get("ADMIN_API_KEY", "")
                check(admin, "the beta gate is on: set ADMIN_API_KEY so the check can make invite codes")
                status, inv = call("POST", API + "/v1/admin/invites", {"note": f"check_accounts {u.label}", "max_uses": 1},
                                   {"Authorization": f"Bearer {admin}"})
                check(status == 201, f"create an invite: {status} {inv}")
                status, body = u.api("POST", "/v1/me/invite", {"code": inv["code"]})
                check(status == 204, f"redeem {u.label}: {status} {body}")
            status, body = u.api("POST", "/v1/me/consent", {"policy_version": cfg["privacy_policy_version"], "app_version": "check_accounts"})
            check(status == 204, f"consent {u.label}: {status} {body}")
        ok("beta: access (redeemed codes where the gate asked) and consent for both users")

        wid = sync_one_day(a)
        days = []
        for _ in range(60):
            status, days = a.api("GET", "/v1/days")
            if status == 200 and days:
                break
            time.sleep(1)
        check(days, "A's sync was not scored within 60 s")
        status, b_days = b.api("GET", "/v1/days")
        check(status == 200 and b_days == [], f"B must see none of A's days: {status} {b_days}")
        status, _ = b.api("GET", f"/v1/workouts/{wid}")
        check(status == 404, f"B must not see A's workout: {status}")
        status, _ = a.api("GET", f"/v1/workouts/{wid}")
        check(status == 200, f"A sees its workout: {status}")
        ok(f"isolation: A has {len(days)} scored day(s) and its workout; B sees neither")

        status, body = a.api("POST", "/v1/me/apple-authorization", {"code": "not-a-real-code"})
        check(status in (422, 501), f"apple-authorization with a bogus code: {status} {body}")
        ok(f"apple-authorization: {status} ({'configured, Apple refused the code' if status == 422 else 'not configured'})")

        for u in (a, b):
            status, body = u.api("DELETE", "/v1/me")
            check(status == 200 and body["status"] == "deleted", f"DELETE /v1/me {u.label}: {status} {body}")
            check(body["auth_user_deleted"] is True, f"{u.label}: the Supabase user must be gone too: {body}")
            check(body.get("apple_token_revoked") is None, f"{u.label}: an email account has no Apple token: {body}")
            u.deleted = True
            status, _ = u.api("GET", "/v1/me")
            check(status == 401, f"{u.label}'s token after deletion: {status}")
            status, _ = u.sign_in()
            check(status != 200, f"{u.label} must no longer sign in: {status}")
        ok("DELETE /v1/me erased both accounts: 401 afterwards and no Supabase sign-in left")
    finally:
        for u in users:  # best effort, also after a failed check
            if not u.deleted and getattr(u, "token", None):
                u.api("DELETE", "/v1/me")
            call("DELETE", f"{SB}/auth/v1/admin/users/{u.id}", None, admin_headers())
    print("account flows passed")


if __name__ == "__main__":
    sys.exit(main())
