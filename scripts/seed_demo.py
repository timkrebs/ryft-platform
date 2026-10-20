#!/usr/bin/env python3
"""The App Review demo account (docs/OPERATIONS.md → App Review demo account).

Creates, or resets, the Supabase email user DEMO_EMAIL (review@ryftapp.de) with the
password from Vault, gives it beta access and consent, and fills it like a few weeks of
real use, so Today, Sleep, Strain, Train and Insights all have something to show: 60 days
of synthetic HealthKit data through POST /v1/sync (scripts/synth.py --demo: an Apple Watch
and an iPhone, three workouts a week), two logged strength sessions, a week of journal.
Re-runnable before every review submission: an existing demo account is deleted first
(DELETE /v1/me, which removes its Supabase sign-in too) and created again.

The password goes into App Store Connect (App Review Information), never into the repo.

  scripts/seed_demo.sh                                  # reads the password from Vault
  DEMO_TOKEN=dev-api-key-change-me:$(uuidgen) API_URL=http://localhost:8082 \\
    INGEST_URL=http://localhost:8081 scripts/seed_demo.py   # the compose stack, no Supabase
"""
import json, os, subprocess, sys, time, urllib.error, urllib.request, uuid
from datetime import date, datetime, timedelta, timezone
from zoneinfo import ZoneInfo

API = os.environ.get("API_URL", "https://api.ryftapp.de").rstrip("/")
INGEST = os.environ.get("INGEST_URL", API).rstrip("/")
EMAIL = os.environ.get("DEMO_EMAIL", "review@ryftapp.de")
TZ = "Europe/Berlin"
DAYS = 60
HERE = os.path.dirname(os.path.abspath(__file__))


def call(method, url, body=None, headers=None, raw_body=None):
    data = raw_body if raw_body is not None else (json.dumps(body).encode() if body is not None else None)
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    try:
        with urllib.request.urlopen(req, timeout=120) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw.strip() else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode(errors="replace")[:200]


def fail(msg):
    print(f"✗ seed_demo: {msg}")
    raise SystemExit(1)


def ok(msg):
    print(f"✓ {msg}")


# ---- the account ---------------------------------------------------------------

def supabase_token():
    sb = os.environ["SUPABASE_URL"].rstrip("/")
    anon, secret, password = os.environ["SUPABASE_ANON_KEY"], os.environ["SUPABASE_SECRET_KEY"], os.environ["DEMO_PASSWORD"]
    admin = {"apikey": secret}
    if secret.count(".") == 2:  # legacy service_role JWT
        admin["Authorization"] = f"Bearer {secret}"

    def sign_in():
        status, body = call("POST", f"{sb}/auth/v1/token?grant_type=password", {"email": EMAIL, "password": password},
                            {"apikey": anon})
        return body["access_token"] if status == 200 else None

    token = sign_in()
    if token is None:  # perhaps it exists with an older password: set the current one
        status, body = call("GET", f"{sb}/auth/v1/admin/users?per_page=1000", headers=admin)
        if status != 200:
            fail(f"list Supabase users: {status} {body}")
        existing = next((u for u in body.get("users", []) if (u.get("email") or "").lower() == EMAIL.lower()), None)
        if existing:
            status, body = call("PUT", f"{sb}/auth/v1/admin/users/{existing['id']}", {"password": password}, admin)
            if status != 200:
                fail(f"reset the password of {EMAIL}: {status} {body}")
            token = sign_in()
    if token:  # reset: erase everything RYFT holds and the sign-in, then start over
        status, body = call("DELETE", f"{API}/v1/me", headers={"Authorization": f"Bearer {token}"})
        if status != 200:
            fail(f"DELETE /v1/me of the old demo account: {status} {body}")
        if not body.get("auth_user_deleted"):
            fail("the old demo account's Supabase sign-in was not deleted (SUPABASE_SERVICE_ROLE_KEY on the api?)")
        ok(f"old demo account deleted ({EMAIL})")
    status, body = call("POST", f"{sb}/auth/v1/admin/users", {"email": EMAIL, "password": password, "email_confirm": True,
                                                              "user_metadata": {"full_name": "App Review"}}, admin)
    if status not in (200, 201):
        fail(f"create {EMAIL}: {status} {body}")
    token = sign_in()
    if token is None:
        fail(f"sign-in of the new {EMAIL}")
    ok(f"demo account created ({EMAIL})")
    return token


TOKEN = os.environ.get("DEMO_TOKEN") or supabase_token()
AUTH = {"Authorization": f"Bearer {TOKEN}"}


def api(method, path, body=None, want=(200,), base=API):
    status, out = call(method, base + path, body, AUTH)
    if status not in want:
        fail(f"{method} {path}: {status} {out}")
    return out


me = api("GET", "/v1/me")
admin_key = os.environ.get("ADMIN_API_KEY")
if not me["beta_access"] and admin_key:  # so the account keeps working once BETA_GATE is on
    status, inv = call("POST", f"{API}/v1/admin/invites", {"note": "App Review demo", "max_uses": 1},
                       {"Authorization": f"Bearer {admin_key}"})
    if status != 201:
        fail(f"create an invite: {status} {inv}")
    api("POST", "/v1/me/invite", {"code": inv["code"]}, want=(204,))
    ok("beta access (a one-use invite code)")
elif not me["beta_access"]:
    status, err = call("GET", f"{API}/v1/days", headers=AUTH)
    if status == 403:
        fail("the beta gate is on and the demo account has no access: set ADMIN_API_KEY to create a code")
    print("! no beta access (ADMIN_API_KEY unset): fine while BETA_GATE is off, run again with it before turning it on")
policy = api("GET", "/v1/config")["privacy_policy_version"]
api("POST", "/v1/me/consent", {"policy_version": policy, "app_version": "seed_demo"}, want=(204,))
me = api("PATCH", "/v1/me", {"tz": TZ, "birth_year": 1990})
ok(f"consent to {policy}; Europe/Berlin, born 1990 → max HR {me['max_hr']} ({me['max_hr_source']})")

# ---- 60 days of HealthKit data ---------------------------------------------------

payload = json.loads(subprocess.run([sys.executable, os.path.join(HERE, "synth.py"), str(DAYS), "--demo"],
                                    check=True, capture_output=True).stdout)
day_of = lambda rec: rec["start"][:10]
cut = sorted({day_of(s) for s in payload["samples"]})
chunks = [cut[k:k + 10] for k in range(0, len(cut), 10)]  # ten days a request, well inside the sync timeout
for chunk in chunks:
    keep = set(chunk)
    part = {**payload, "samples": [s for s in payload["samples"] if day_of(s) in keep],
            "sleep": [s for s in payload["sleep"] if day_of(s) in keep],
            "workouts": [w for w in payload["workouts"] if day_of(w) in keep]}
    status, out = call("POST", INGEST + "/v1/sync", headers=AUTH, raw_body=json.dumps(part).encode())
    if status != 200:
        fail(f"POST /v1/sync {chunk[0]}..{chunk[-1]}: {status} {out}")
ok(f"synced {len(payload['samples'])} samples, {len(payload['sleep'])} sleep segments, "
   f"{len(payload['workouts'])} workouts over {DAYS} days")

tz = ZoneInfo(TZ)
today = datetime.now(tz).date()
api("POST", "/v1/recompute", {"from": (today - timedelta(days=DAYS)).isoformat(), "to": today.isoformat()}, want=(202,))
for _ in range(180):
    if api("GET", f"/v1/days/{today}")["score"]["recovery"] is not None:
        break
    time.sleep(1)
else:
    fail(f"{today} was not scored within 3 minutes")
ok(f"{DAYS} days scored")

# ---- Train: a plan and two logged sessions ----------------------------------------

plans = api("GET", "/v1/plans")
plan = next(p for p in plans["builtin"] if p["slug"] == "full-body-3x")
api("POST", f"/v1/plans/{plan['id']}/enroll", want=(200, 201))
for back in (3, 1):  # the second yesterday, so its records show as a card
    targets = api("GET", "/v1/plans/today")
    d = today - timedelta(days=back)
    start = datetime(d.year, d.month, d.day, 18, 0, tzinfo=tz).astimezone(timezone.utc)
    iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%SZ")
    exercises, minute = [], 0
    for pos, t in enumerate(targets["targets"]):
        sets = []
        for k in range(t["sets"]):
            minute += 3
            at = iso(start + timedelta(minutes=minute))
            sets.append({"id": str(uuid.uuid4()), "set_index": k, "reps": t["reps_max"], "weight_kg": t["target_kg"] or 40,
                         "rpe": 8, "duration_sec": None, "distance_m": None, "is_warmup": False,
                         "completed_at": at, "updated_at": at})
        exercises.append({"id": str(uuid.uuid4()), "exercise_id": t["exercise_id"], "plan_exercise_id": t["plan_exercise_id"],
                          "position": pos, "notes": "", "sets": sets})
    sid = str(uuid.uuid4())
    api("PUT", f"/v1/sessions/{sid}", {"id": sid, "title": targets["day"]["name"], "started_at": iso(start), "ended_at": None,
                                       "status": "in_progress", "plan_day_id": targets["day"]["id"], "notes": "",
                                       "bodyweight_kg": None, "device_id": None, "exercises": exercises}, want=(201,))
    done = api("POST", f"/v1/sessions/{sid}/complete", {"bodyweight_kg": 78, "ended_at": iso(start + timedelta(minutes=minute + 5))})
    ok(f"session {targets['day']['name']} on {d}: {sum(len(e['sets']) for e in exercises)} sets, {len(done.get('prs') or [])} records")

# ---- a week of journal ------------------------------------------------------------

by_slug = {b["slug"]: b["id"] for b in api("GET", "/v1/behaviors")}
for k in range(1, 8):
    d = today - timedelta(days=k)
    answers = {"alcohol": k in (2, 6), "caffeine_after_2pm": k in (1, 3, 4), "late_meal": k == 5,
               "screen_before_bed": k in (1, 2, 5, 6), "stretching": k in (3, 4, 7), "meditation": k in (1, 4, 7),
               "sick": False, "work_stress": k in (2, 3)}
    api("PUT", f"/v1/journal/{d}", {"entries": [{"behavior_id": by_slug[s], "value_bool": v} for s, v in answers.items()]})
ok("journal: the last 7 days answered")

# ---- every screen has data ----------------------------------------------------------

time.sleep(5)  # the sessions' days are rescored after …/complete
day = api("GET", f"/v1/days/{today}")
week = api("GET", "/v1/sleep?range=week")
sessions = [s for s in api("GET", "/v1/sessions") if s["status"] == "completed"]
cards = api("GET", "/v1/insights")
missing = [name for name, has in (("Today", day["score"]["readiness"] is not None), ("Sleep", day["sleep"] is not None and week["nights"] > 0),
                                  ("Strain", day["strain"] is not None), ("Train", len(sessions) >= 2), ("Insights", len(cards) > 0))
           if not has]
if missing:
    fail(f"no data on {', '.join(missing)}")
ok(f"Today readiness {day['score']['readiness']}, Sleep {week['nights']} nights, Strain {day['score']['strain']}, "
   f"Train {len(sessions)} sessions, Insights {[c['id'] for c in cards]}")
