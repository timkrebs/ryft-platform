#!/usr/bin/env python3
"""Contract checks against a running stack, after smoke.sh has synced and scored.

Asserts the shapes and invariants docs/API.md promises the iOS app. Exits non-zero
on the first broken promise.

  API_URL=http://localhost:8082 API_KEY=... check_api.py YYYY-MM-DD(today)
"""
import json, os, sys, time, urllib.error, urllib.request, uuid
from datetime import date, datetime, timedelta, timezone
from zoneinfo import ZoneInfo

API = os.environ.get("API_URL", "http://localhost:8082")
INGEST = os.environ.get("INGEST_URL", "http://localhost:8081")
KEY = os.environ.get("API_KEY", "dev-api-key-change-me")
TODAY = sys.argv[1]


def send(req):
    """Status, headers and body of one request. A 429 is waited out (Retry-After, up to
    three times) as the app does: the checks run faster than any screen and would
    otherwise trip the per-user limit (10/s, burst 60) on a busy run."""
    for attempt in range(4):
        try:
            with urllib.request.urlopen(req) as r:
                return r.status, r.headers, r.read()
        except urllib.error.HTTPError as e:
            status, hdrs, data = e.code, e.headers, e.read()
        if status != 429 or attempt == 3:
            return status, hdrs, data
        time.sleep(min(float(hdrs.get("Retry-After") or 1), 5))


def get(path, expect=200, auth=True, key=None):
    req = urllib.request.Request(API + path, headers={"Authorization": f"Bearer {key or KEY}"} if auth else {})
    status, _, body = send(req)
    if status != expect:
        fail(f"GET {path}: status {status}, want {expect}: {body[:200]!r}")
    return json.loads(body) if body.strip().startswith((b"{", b"[")) else body


def post(path, body, expect, base=API, key=None):
    req = urllib.request.Request(base + path, data=json.dumps(body).encode(), method="POST",
                                 headers={"Authorization": f"Bearer {key or KEY}", "Content-Type": "application/json"})
    status, _, raw = send(req)
    if status != expect:
        fail(f"POST {path}: status {status}, want {expect}: {raw[:200]!r}")
    return json.loads(raw)


def fail(msg):
    print(f"✗ {msg}")
    sys.exit(1)


def check(cond, msg):
    if not cond:
        fail(msg)


def ok(msg):
    print(f"✓ {msg}")


STATES = {"balanced", "strained", "depleted", "calibrating"}

# ---- 2.1 readiness ------------------------------------------------------------
days = get(f"/v1/days?to={TODAY}")
check(len(days) > 0, "/v1/days returned no rows")
for d in days:
    check("readiness" in d and d["state"] in STATES, f"{d['day']}: readiness/state missing or bad: {d.get('state')}")
    r = d["readiness"]
    check(r is None or 1 <= r <= 100, f"{d['day']}: readiness {r} out of range")
    check(d["calibrating"] == (d["baseline_days"] < 7), f"{d['day']}: calibrating != baseline_days < 7")
    if r is not None and not d["calibrating"]:
        want = "balanced" if r >= 67 else "strained" if r >= 34 else "depleted"
        check(d["state"] == want, f"{d['day']}: state {d['state']} for readiness {r}")
ok(f"/v1/days: readiness + state on {len(days)} days")

# ---- 2.2 sleep ----------------------------------------------------------------
STAGES = ["deep", "light", "rem", "awake"]


def check_night(s, where):
    check([x["stage"] for x in s["stages"]] == STAGES, f"{where}: stage order {s['stages']}")
    staged = s["stages"][:3]
    if s["has_stages"]:
        check(sum(x["pct"] for x in staged) == 100, f"{where}: deep+light+rem pct != 100")
    check(s["in_bed_min"] >= s["total_min"] > 0, f"{where}: in_bed {s['in_bed_min']} < total {s['total_min']}")
    check(s["efficiency"] == round(100 * s["total_min"] / s["in_bed_min"]), f"{where}: efficiency")
    check(s["quality_label"] in {"good", "fair", "poor"}, f"{where}: quality_label {s['quality_label']}")
    deep, rem = s["stages"][0]["min"], s["stages"][2]["min"]
    want = min(100, rnd((deep + rem) / s["total_min"] / 0.40 * 100)) if s["has_stages"] else None
    check(want is None or s["restorative"] == want, f"{where}: restorative {s['restorative']}, want {want}")
    check(0 <= s["restorative"] <= 100 and 0 <= s["quality"] <= 100, f"{where}: quality/restorative range")


def rnd(x):
    """Go's math.Round for the non-negative values here (Python's round is to even)."""
    return int(x + 0.5)


detail = get(f"/v1/days/{TODAY}")
s = detail["sleep"]
check(s is not None, "/v1/days/{today}: sleep is null after a synced night")
check_night(s, "day sleep")
check(detail["score"]["sleep_actual_min"] == s["total_min"], "sleep.total_min != score.sleep_actual_min")
sc = detail["score"]
want = rnd(0.6 * sc["sleep_performance"] + 0.2 * s["efficiency"] + 0.2 * s["restorative"])
check(s["quality"] == sc["sleep_quality"] == want,
      f"quality {s['quality']} / score {sc['sleep_quality']}, want 0.6·perf + 0.2·eff + 0.2·restorative = {want}")
need = s["need"]
check(set(need) == {"base", "strain", "debt", "nap_credit", "total"}, f"sleep.need keys {sorted(need)}")
check(need["total"] == sc["sleep_need_min"] == need["base"] + need["strain"] + need["debt"] - need["nap_credit"],
      f"sleep.need {need} vs score.sleep_need_min {sc['sleep_need_min']}")
check([need[k] for k in ("base", "strain", "debt", "nap_credit")] ==
      [sc[k] for k in ("sleep_need_base_min", "sleep_need_strain_min", "sleep_need_debt_min", "sleep_need_nap_min")],
      f"sleep.need {need} != the score's sleep_need_* fields")
segs = s["segments"]
check(len(segs) > 0 and all(x["stage"] in STAGES + ["asleep"] for x in segs), "hypnogram segments")
check(all(a["start"] <= b["start"] for a, b in zip(segs, segs[1:])), "hypnogram not sorted")
ok(f"/v1/days/{TODAY}: sleep {s['total_min']} min, {len(segs)} hypnogram bars")

for rng, n in (("week", 7), ("month", 30)):
    sr = get(f"/v1/sleep?range={rng}&to={TODAY}")
    check(sr["range"] == rng and sr["to"] == TODAY, f"/v1/sleep {rng}: range/to echo")
    check(0 < sr["nights"] == len(sr["days"]) <= n, f"/v1/sleep {rng}: {sr['nights']} nights")
    for d in sr["days"]:
        check(sr["from"] <= d["day"] <= sr["to"], f"/v1/sleep {rng}: {d['day']} outside range")
        check_night(d, f"/v1/sleep {rng} {d['day']}")
    mean = round(sum(d["total_min"] for d in sr["days"]) / sr["nights"])
    check(abs(sr["avg_total_min"] - mean) <= 1, f"/v1/sleep {rng}: avg_total_min {sr['avg_total_min']} vs {mean}")
    ok(f"/v1/sleep?range={rng}: {sr['nights']} nights, avg {sr['avg_total_min']} min")
err = get("/v1/sleep?range=year", expect=422)
check(isinstance(err, dict) and "error" in err, "422 must carry {error}")
ok("/v1/sleep?range=year → 422 {error}")

# M4: naps, consistency, quality as its own score.
by_day = {d["day"]: d for d in days}
for d in days:
    c, nap = d["sleep_consistency"], d["nap_min"]
    check(c is None or 0 <= c <= 100, f"{d['day']}: sleep_consistency {c}")
    check(nap is None or 0 <= nap <= 90, f"{d['day']}: nap_min {nap}")
    if d["sleep_need_base_min"] is not None:
        check(d["sleep_need_min"] == d["sleep_need_base_min"] + d["sleep_need_strain_min"] + d["sleep_need_debt_min"]
              - d["sleep_need_nap_min"], f"{d['day']}: the need terms do not add up")
check(by_day[TODAY]["sleep_consistency"] is not None, "21 synthetic nights: today must have a sleep_consistency")
check(any(d["sleep_quality"] is not None and d["sleep_quality"] != d["sleep_performance"] for d in days),
      "sleep_quality must be its own score, not a copy of sleep_performance")
NAP = int(os.environ.get("NAP", "0"))
tz_me = ZoneInfo(get("/v1/me")["tz"])
nap_at = (datetime.now(timezone.utc) - timedelta(days=1)).replace(hour=13, minute=0, second=0, microsecond=0)
nap_local = nap_at.astimezone(tz_me)
if NAP and 10 <= nap_local.hour and (nap_local + timedelta(minutes=NAP)).hour < 20:
    nap_day = nap_local.date().isoformat()
    credit_day = (nap_local.date() + timedelta(days=1)).isoformat()
    check(by_day[nap_day]["nap_min"] == NAP, f"{nap_day}: nap_min {by_day[nap_day]['nap_min']}, want {NAP}")
    if credit_day in by_day:
        check(by_day[credit_day]["sleep_need_nap_min"] == NAP,
              f"{credit_day}: nap credit {by_day[credit_day]['sleep_need_nap_min']}, want {NAP}")
    ok(f"nap: {NAP} min on {nap_day}, credited to {credit_day}'s need")
else:
    print(f"- nap credit not checked (NAP={NAP}, the nap is at {nap_local:%H:%M} local)")
ok(f"/v1/days/{TODAY}: quality {s['quality']} (performance {sc['sleep_performance']}), need {need}, "
   f"consistency {by_day[TODAY]['sleep_consistency']}")

sr = get(f"/v1/sleep?range=week&to={TODAY}")
check(len(sr["bedtimes"]) == sr["nights"], f"/v1/sleep: {len(sr['bedtimes'])} bedtimes for {sr['nights']} nights")
for d, b in zip(sr["days"], sr["bedtimes"]):
    check(b == {"day": d["day"], "bed": d["start"], "wake": d["end"]}, f"/v1/sleep bedtimes: {b} vs {d['day']}")
    check(d["consistency"] == by_day[d["day"]]["sleep_consistency"] and d["nap_min"] == by_day[d["day"]]["nap_min"],
          f"/v1/sleep {d['day']}: consistency/nap_min differ from /v1/days")
    check("need" not in d and "segments" not in d, f"/v1/sleep {d['day']}: need/segments only on the day endpoint")
cons = [d["consistency"] for d in sr["days"] if d["consistency"] is not None]
check(sr["avg_consistency"] == (rnd(sum(cons) / len(cons)) if cons else None), f"avg_consistency {sr['avg_consistency']}")
ok(f"/v1/sleep?range=week: avg consistency {sr['avg_consistency']}, {len(sr['bedtimes'])} bedtimes")

me = get("/v1/me")
plan = get("/v1/sleep/plan?wake=07:00")
pb = plan["basis"]
wake = datetime.fromisoformat(plan["wake"].replace("Z", "+00:00"))
check(wake.astimezone(tz_me).strftime("%H:%M") == "07:00" and timedelta(0) < wake - datetime.now(timezone.utc) <= timedelta(hours=24),
      f"plan wake {plan['wake']} is not the next 07:00 local")
beds = [datetime.fromisoformat(plan[k].replace("Z", "+00:00")) for k in ("bedtime_100", "bedtime_85", "bedtime_70")]
check(beds[0] < beds[1] < beds[2] < wake, f"plan bedtimes not ascending: {[plan[k] for k in ('bedtime_100', 'bedtime_85', 'bedtime_70')]}")
check(plan["need_min"] == pb["base"] + pb["strain_topup"] + pb["debt_topup"] - pb["nap_credit"] and pb["base"] == me["sleep_base_min"],
      f"plan need {plan['need_min']} vs basis {pb}")
check(plan["efficiency"] == pb["efficiency"] >= 85 and abs(plan["in_bed_min"] - plan["need_min"] * 100 / plan["efficiency"]) <= 1,
      f"plan efficiency {plan['efficiency']}, in bed {plan['in_bed_min']}")
check(abs((wake - beds[0]).total_seconds() / 60 - plan["in_bed_min"]) <= 1, "bedtime_100 must be wake − in_bed_min")
for bad in ("", "7:00", "25:00", "07:00:00"):
    check(isinstance(get(f"/v1/sleep/plan?wake={bad}", expect=422), dict), f"plan wake={bad!r} must be 422")
ok(f"/v1/sleep/plan?wake=07:00: need {plan['need_min']}, bed {plan['bedtime_100']} / {plan['bedtime_85']} / {plan['bedtime_70']}; bad wake → 422")

# ---- 2.3 workouts -------------------------------------------------------------
wks = get(f"/v1/workouts?to={TODAY}")
check(len(wks) > 0, "/v1/workouts: synthetic data has workouts")
wk = get(f"/v1/workouts/{wks[-1]['id']}")
check(wk["id"] == wks[-1]["id"] and wk["strain"] is not None, "workout detail: id/strain")
check(0 < len(wk["hr"]) <= 600, f"workout detail: {len(wk['hr'])} hr points")
check(all(wk["start"] <= p["t"] <= wk["end"] for p in wk["hr"]), "hr points outside the workout window")
check(all(a["t"] < b["t"] for a, b in zip(wk["hr"], wk["hr"][1:])), "hr points not sorted")
check(isinstance(wk["trimp"], int) and len(wk["zone_min"]) == 5, f"trimp/zone_min: {wk['trimp']} {wk['zone_min']}")
check(wk["avg_hr"] is not None and wk["max_hr"] >= wk["avg_hr"], "avg/max hr")
mins = (len(wk["hr"]) - 1)  # synth samples every minute
check(sum(wk["zone_min"]) <= mins + 1, f"zone minutes {sum(wk['zone_min'])} exceed duration {mins}")
ok(f"/v1/workouts/{{id}}: {len(wk['hr'])} hr points, zones {wk['zone_min']}, trimp {wk['trimp']}, strain {wk['strain']}")
get("/v1/workouts/00000000-0000-0000-0000-00000000dead", expect=404)
get("/v1/workouts/not-a-uuid", expect=400)
ok("/v1/workouts/{id}: 404 unknown, 400 malformed")
today = get(f"/v1/days/{TODAY}")
st = today["strain"]
check(st is not None and st["hr_min"] > 0 and len(st["zone_min"]) == 5, f"day strain: {st}")
check(st["steps"] == today["score"]["steps"] and st["active_kcal"] == today["score"]["active_kcal"],
      f"strain steps/kcal {st['steps']}/{st['active_kcal']} != score {today['score']['steps']}/{today['score']['active_kcal']}")
# synth.py starts moving at ~06:30 UTC: before that, today has heart rate but no steps yet.
check(st["steps"] is None or (isinstance(st["steps"], int) and st["steps"] > 0 and isinstance(st["active_kcal"], int)),
      f"strain steps/kcal {st['steps']}/{st['active_kcal']}")
check(any(isinstance(d["steps"], int) and d["steps"] > 0 for d in days), "no day has steps")
check(all(d["steps"] is None or (isinstance(d["steps"], int) and d["steps"] >= 0) for d in days), "steps must be whole and ≥ 0")
ok(f"/v1/days/{TODAY}.strain: trimp {st['trimp']}, {st['hr_min']} HR minutes, {st['steps']} steps, {st['active_kcal']} kcal")

# ---- 2.4 trends ---------------------------------------------------------------
FIELD = {"readiness": "readiness", "recovery": "recovery", "strain": "strain", "sleep": "sleep_performance",
         "sleep_min": "sleep_actual_min", "hrv": "hrv_ms", "rhr": "rhr", "steps": "steps", "active_kcal": "active_kcal",
         "sleep_quality": "sleep_quality", "sleep_consistency": "sleep_consistency", "nap_min": "nap_min"}
by_day = {d["day"]: d for d in days}
for metric, field in FIELD.items():
    tr = get(f"/v1/trends?metric={metric}&range=7d&to={TODAY}")
    check(tr["metric"] == metric and tr["range"] == "7d" and tr["to"] == TODAY, f"trend {metric}: echo")
    pts = tr["points"]
    check(0 < len(pts) <= 7, f"trend {metric}: {len(pts)} points")
    check(all(tr["from"] <= p["day"] <= tr["to"] for p in pts), f"trend {metric}: point outside window")
    for p in pts:
        check(by_day[p["day"]][field] == p["value"], f"trend {metric} {p['day']}: {p['value']} != days {by_day[p['day']][field]}")
    vals = [p["value"] for p in pts]
    check(tr["min"] == min(vals) and tr["max"] == max(vals), f"trend {metric}: min/max")
    check(abs(tr["avg"] - sum(vals) / len(vals)) <= 0.05, f"trend {metric}: avg {tr['avg']}")
    if metric in ("hrv", "rhr"):
        base_field = {"hrv": "hrv_baseline_ms", "rhr": "rhr_baseline"}[metric]
        check(tr["baseline"] is not None and tr["baseline"] == by_day[TODAY][base_field],
              f"trend {metric}: baseline {tr['baseline']} != today's {base_field} {by_day[TODAY][base_field]}")
    else:
        check(tr["baseline"] is None, f"trend {metric}: baseline must be null")
ok(f"/v1/trends: {len(FIELD)} metrics match /v1/days; hrv/rhr carry a baseline")
check(len(get(f"/v1/trends?metric=readiness&range=90d&to={TODAY}")["points"]) == len([d for d in days if d["readiness"] is not None]), "90d readiness")
get("/v1/trends?metric=calories", expect=422)
get("/v1/trends?metric=hrv&range=1y", expect=422)
ok("/v1/trends: 90d ok, unknown metric/range → 422")

# ---- 2.5 insights -------------------------------------------------------------
RANK = {"warn": 0, "notice": 1, "info": 2}
cards = get("/v1/insights")
check(isinstance(cards, list), "/v1/insights must be a list")
for c in cards:
    check(set(c) == {"id", "severity", "title", "body", "metric", "day"}, f"insight keys {sorted(c)}")
    check(c["severity"] in RANK and c["metric"] in FIELD and c["day"] <= TODAY and c["title"] and c["body"],
          f"insight {c}")
check([RANK[c["severity"]] for c in cards] == sorted(RANK[c["severity"]] for c in cards), "insights not sorted by severity")
check(get("/v1/insights") == cards, "cached insights differ between calls")
ok(f"/v1/insights: {len(cards)} cards {[c['id'] for c in cards]}")

# ---- 2.9 errors ---------------------------------------------------------------
def is_error(body):
    return (isinstance(body, dict) and set(body) <= {"error", "request_id"} and body["error"]
            and body.get("request_id"))


def raw(method, path, body=None, base=API, headers=None, key=None):
    """Status, headers and decoded body of one request (for header checks)."""
    hdrs = {"Authorization": f"Bearer {key or KEY}", "Content-Type": "application/json", **(headers or {})}
    req = urllib.request.Request(base + path, data=body, method=method, headers=hdrs)
    status, hdrs, data = send(req)
    return status, hdrs, json.loads(data) if data.strip() else None  # 204: no body


check(is_error(get("/v1/days", expect=401, auth=False)), "401 must carry {error}")
check(is_error(get("/v1/nope", expect=404)), "unknown /v1 route must be a JSON 404")
PUBLIC = API.startswith("https://")  # behind the Ingress, which routes only /v1
if not PUBLIC:
    check(is_error(get("/nope", expect=404, auth=False)), "unknown route must be a JSON 404")
check(is_error(get("/v1/days?from=2026-10-18&to=2026-10-01", expect=422)), "from > to must be 422")
check(is_error(get("/v1/days?from=yesterday", expect=400)), "malformed date must be 400")
check(is_error(post("/v1/recompute", {"from": "2024-01-01", "to": TODAY}, expect=422)), "recompute > 400 days must be 422")
check(is_error(post("/v1/recompute", {"from": TODAY, "to": TODAY, "x": 1}, expect=400)), "unknown body field must be 400")
ok("errors: 400/401/404/422 all carry {error, request_id}")

status, hdrs, body = raw("GET", "/v1/nope", headers={"X-Request-ID": "client-chosen-id"})
rid = hdrs.get("X-Request-ID")
check(rid and rid != "client-chosen-id" and body.get("request_id") == rid,
      f"X-Request-ID must be server-generated and match the error body: {rid} {body}")
# api: a 200; ingest: its own JSON 404 for GET /v1/sync (only POST exists), which
# proves the answer comes from the service and not from the Ingress.
for base, path, want in ((API, "/v1/me", 200), (INGEST, "/v1/sync", 404)):
    status, hdrs, _ = raw("GET", path, base=base)
    check(status == want and hdrs.get("X-Request-ID"), f"{base}{path}: {status}, X-Request-ID {hdrs.get('X-Request-ID')}")
    for k, v in {"Cache-Control": "no-store", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY",
                 "Referrer-Policy": "no-referrer"}.items():
        check(hdrs.get(k) == v, f"{base}{path}: {k} = {hdrs.get(k)!r}, want {v!r}")
    check("max-age=" in (hdrs.get("Strict-Transport-Security") or ""), f"{base}{path}: HSTS missing")
big = json.dumps({"from": TODAY, "to": TODAY, "pad": "x" * (70 << 10)}).encode()
status, _, body = raw("POST", "/v1/recompute", big)
check(status == 413 and is_error(body), f"a 70 KiB recompute body must be 413, got {status} {body}")
status, _, body = raw("PATCH", "/v1/me", json.dumps({"tz": "Europe/Berlin", "name": "x" * (70 << 10)}).encode())
check(status == 413 and is_error(body), f"a 70 KiB settings body must be 413, got {status} {body}")
ok("X-Request-ID server-generated; security headers on api and ingest; oversized bodies 413")

# ---- sleep baseline: manual and back (M4) -------------------------------------
def patch_me(body, expect=200):
    status, _, out = raw("PATCH", "/v1/me", json.dumps(body).encode())
    check(status == expect, f"PATCH /v1/me {body}: {status} {out}")
    return out


me0 = get("/v1/me")
check(me0["sleep_base_source"] in {"default", "learned", "manual"} and "sleep_base_learned_min" in me0
      and "sleep_base_learned_at" in me0, f"/v1/me baseline fields: {me0}")
m = patch_me({"sleep_base_min": 481})
check(m["sleep_base_min"] == 481 and m["sleep_base_source"] == "manual", f"sleep_base_min makes it manual: {m}")
check(is_error(patch_me({"sleep_base_min": 482, "sleep_base_manual": False}, expect=422)), "base with manual=false must be 422")
m = patch_me({"sleep_base_manual": False})
learned = me0["sleep_base_learned_min"]
check(m["sleep_base_source"] == ("learned" if learned else "default") and m["sleep_base_min"] == (learned or 460),
      f"manual off goes back to the learned value or 460: {m}")
if me0["sleep_base_source"] == "manual":  # leave the account as it was
    m = patch_me({"sleep_base_min": me0["sleep_base_min"]})
check(m == me0, f"baseline not restored: {m} vs {me0}")
ok(f"PATCH /v1/me: manual 481 → {m['sleep_base_source']} {m['sleep_base_min']}; base with manual=false → 422")

# ---- remote config (GET /v1/config, /v1/admin/flags) ---------------------------
APP_FLAGS = {"train.enabled", "sleep.planner", "today.redesign", "coach.enabled", "push.enabled", "physiology.card"}
cfg = get("/v1/config")
check(set(cfg) == {"flags", "evaluated_at", "ttl_sec", "privacy_policy_version", "privacy_policy_url"} and cfg["ttl_sec"] == 21600,
      f"/v1/config shape: {cfg}")
check(APP_FLAGS <= set(cfg["flags"]) and all(isinstance(v, bool) for v in cfg["flags"].values()),
      f"/v1/config must evaluate the app's flags: {cfg['flags']}")
check(get("/v1/config")["flags"] == cfg["flags"], "the same user gets the same flags")
ADMIN = os.environ.get("ADMIN_API_KEY", "")
if ADMIN and os.environ.get("AUTH_MODE", "apikey") == "apikey":
    def admin(method, path, body=None, key=ADMIN):
        return raw(method, path, None if body is None else json.dumps(body).encode(), key=key)
    status, _, body = raw("GET", "/v1/admin/flags")  # a user's bearer is not the admin key
    check(status == 401 and is_error(body), f"/v1/admin with a user token: {status}")
    status, _, body = admin("GET", "/v1/admin/flags", key=ADMIN + "x")
    check(status == 401, f"/v1/admin with a wrong key: {status}")
    status, _, listed = admin("GET", "/v1/admin/flags")
    check(status == 200 and APP_FLAGS <= {f["key"] for f in listed}, f"GET /v1/admin/flags: {status}")
    tester, other = str(uuid.uuid4()), str(uuid.uuid4())
    status, _, f = admin("PUT", "/v1/admin/flags/check.api", {"enabled": True, "rollout_pct": 0, "allow_user_ids": [tester]})
    check(status in (200, 201) and f["allow_user_ids"] == [tester] and f["rollout_pct"] == 0, f"PUT flag: {status} {f}")
    check(get("/v1/config", key=f"{KEY}:{tester}")["flags"]["check.api"] is True, "the allow-listed user gets the flag")
    check(get("/v1/config", key=f"{KEY}:{other}")["flags"]["check.api"] is False, "others do not at 0 %")
    admin("PUT", "/v1/admin/flags/check.api", {"enabled": True, "rollout_pct": 100})
    check(get("/v1/config", key=f"{KEY}:{other}")["flags"]["check.api"] is True, "100 % reaches everyone")
    admin("PUT", "/v1/admin/flags/check.api", {"enabled": False, "rollout_pct": 100, "allow_user_ids": [tester]})
    check(get("/v1/config", key=f"{KEY}:{tester}")["flags"]["check.api"] is False, "disabled is off, allow-list included")
    for path, body in (("check.api", {"enabled": True}), ("check.api", {"enabled": True, "rollout_pct": 101}),
                       ("Not.A.Key", {"enabled": True, "rollout_pct": 0})):
        status, _, err = admin("PUT", f"/v1/admin/flags/{path}", body)
        check(status == 422 and is_error(err), f"PUT /v1/admin/flags/{path} {body}: {status}")
    status, _, _ = admin("DELETE", "/v1/admin/flags/check.api")
    check(status == 204 and "check.api" not in get("/v1/config")["flags"], f"DELETE flag: {status}")
    for k in (tester, other):  # the two users were created by their requests
        raw("DELETE", "/v1/me", key=f"{KEY}:{k}")
    ok(f"/v1/config: {len(cfg['flags'])} flags; admin: 401 without the key, allow-list, rollout, kill switch, 422s, delete")
else:
    ok(f"/v1/config: {sorted(k for k, v in cfg['flags'].items() if v)} on (admin checks need ADMIN_API_KEY, apikey mode)")

# ---- beta: consent and invite codes ---------------------------------------------
def iso_now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def jbody(v):
    return json.dumps(v).encode()


POLICY = get("/v1/config")["privacy_policy_version"]


def consent(key=None):
    status, _, body = raw("POST", "/v1/me/consent", jbody({"policy_version": POLICY, "app_version": "check_api"}), key=key)
    check(status == 204, f"POST /v1/me/consent: {status} {body}")


cfg_b = get("/v1/config")
check(cfg_b["privacy_policy_url"].startswith("https://") and POLICY, f"/v1/config privacy fields: {cfg_b}")

# ---- sync state (GET /v1/me/sync, scoring_state; D91) ---------------------------
SCORING_STATES = {"scored", "pending", "waiting_for_sleep", "stale"}
KINDS_ALL = ["energy", "hr", "hrv", "resp", "sleep", "spo2", "steps", "workout"]
for _ in range(30):  # smoke.sh's sync and recompute: scoring finishes the queued days
    ss = get("/v1/me/sync")
    if not ss["pending_days"]:
        break
    time.sleep(1)
check(set(ss) == {"gate", "last_sync_at", "last_sync_device", "latest", "today", "pending_days", "expected_within_sec",
                  "resync_requested_at"} and ss["pending_days"] == [] and ss["today"]["day"] == TODAY
      and ss["today"]["state"] in SCORING_STATES and ss["today"]["model_version"] and ss["last_sync_at"]
      and set(ss["latest"]) <= set(KINDS_ALL) and ss["latest"].get("hr") and ss["expected_within_sec"] >= 1,
      f"GET /v1/me/sync: {ss}")
check(ss["gate"] is None, f"the smoke user consented: {ss['gate']}")
rows = get(f"/v1/days?to={TODAY}")
check(all(d["scoring_state"] in SCORING_STATES for d in rows) and all(d["scoring_state"] == "scored" for d in rows if d["day"] < TODAY),
      f"scoring_state: {[(d['day'], d['scoring_state']) for d in rows[-3:]]}")
check(get(f"/v1/days/{TODAY}")["score"]["scoring_state"] == ss["today"]["state"], "the day's state is today's")
ok(f"/v1/me/sync: nothing pending after scoring, today {ss['today']['state']}, expected within {ss['expected_within_sec']} s")

# ---- scoring model (GET /v1/model, model_version) -----------------------------
MODEL = get("/v1/model")
check(MODEL["current"] and MODEL["changelog"] and MODEL["changelog"][0]["version"] == MODEL["current"]
      and all(set(c) == {"version", "date", "summary", "summary_en", "summary_de", "affects"} for c in MODEL["changelog"]),
      f"/v1/model shape: {MODEL}")
check(all(c["summary"] == c["summary_en"] for c in MODEL["changelog"]), "summary is English by default")
_, _, model_de = raw("GET", "/v1/model", headers={"Accept-Language": "de-DE,de;q=0.9"})
check(all(c["summary"] == c["summary_de"] for c in model_de["changelog"]), "summary follows Accept-Language")
latest = get(f"/v1/days?to={TODAY}")
check(latest and latest[-1]["model_version"] == MODEL["current"]
      and all(d["model_version"] in (None, MODEL["current"]) for d in latest),
      f"days carry the current model_version: {[(d['day'], d['model_version']) for d in latest[-3:]]}")
ok(f"/v1/model: {MODEL['current']}, {len(MODEL['changelog'])} entries; the last scored day carries it")
me_c = get("/v1/me")
KINDS = ["energy", "hr", "hrv", "resp", "sleep", "spo2", "steps", "workout"]
ts = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00"))
check(isinstance(me_c["sources"], list) and me_c["sources"]
      and all(set(s) == {"name", "bundle_id", "kinds", "last_seen"} and s["kinds"] and set(s["kinds"]) <= set(KINDS)
              and s["kinds"] == sorted(s["kinds"]) for s in me_c["sources"])
      and [ts(s["last_seen"]) for s in me_c["sources"]] == sorted((ts(s["last_seen"]) for s in me_c["sources"]), reverse=True),
      f"User.sources of the smoke user: {me_c['sources']}")
check(me_c["consent"] and me_c["consent"]["policy_version"] == POLICY and me_c["beta_access"] in (True, False),
      f"User.consent after smoke.sh consented: {me_c.get('consent')}")
status, _, err = raw("POST", "/v1/me/consent", jbody({"policy_version": "1999-01-01"}))
check(status == 422 and is_error(err), f"an outdated policy version: {status}")
if os.environ.get("AUTH_MODE", "apikey") == "apikey":
    D = str(uuid.uuid4())
    KEY_D = f"{KEY}:{D}"
    tiny = {"device_id": str(uuid.uuid4()), "app_version": "0.9.0 (41)", "os_version": "iOS 26.1",
            "samples": [{"type": "steps", "start": iso_now(), "end": iso_now(), "value": 10, "unit": "count",
                         "source": "check-d", "source_bundle_id": "com.example.check-d"}], "sleep": [], "workouts": []}
    status, _, err = raw("POST", "/v1/sync", jbody(tiny), base=INGEST, key=KEY_D)
    check(status == 403 and err.get("code") == "consent_required" and is_error({k: v for k, v in err.items() if k != "code"}),
          f"sync without consent: {status} {err}")
    consent(KEY_D)
    status, _, _ = raw("POST", "/v1/sync", jbody(tiny), base=INGEST, key=KEY_D)
    check(status == 200, f"sync after consent: {status}")
    # The same page again, plus a type the server does not know: counted, not refused (D91).
    again = {**tiny, "samples": tiny["samples"] + [{"type": "blood_glucose", "start": iso_now(), "end": iso_now(), "value": 5,
                                                    "unit": "mmol/L", "source": "check-d"}]}
    status, _, sr = raw("POST", "/v1/sync", jbody(again), base=INGEST, key=KEY_D)
    check(status == 200 and sr["accepted"] == {"samples": 0, "sleep": 0, "workouts": 0} and sr["samples"] == 0
          and sr["ignored"]["samples"] == {"duplicate": 1, "outside_window": 0, "unknown_type": 1}
          and sr["scoring"]["queued_days"] == sr["days_touched"] and sr["scoring"]["expected_within_sec"] >= 1,
          f"SyncResponse: accepted, ignored, scoring: {status} {sr}")
    # A new account on D's phone: the account hint (only with the device header).
    KEY_J = f"{KEY}:{uuid.uuid4()}"
    status, _, mj = raw("GET", "/v1/me", headers={"X-RYFT-Device-ID": tiny["device_id"]}, key=KEY_J)
    hint = mj.get("account_hint") if status == 200 else None
    check(hint and hint["kind"] == "other_account_on_device" and hint["other_provider"] == "apikey" and hint["other_last_sync"]
          and set(hint) == {"kind", "other_provider", "other_last_sync"}, f"account_hint: {status} {mj}")
    check(get("/v1/me", key=KEY_J)["account_hint"] is None, "no hint without the device header")
    check(get("/v1/me", key=KEY_D, )["account_hint"] is None, "no hint for the account itself")
    raw("DELETE", "/v1/me", key=KEY_J)
    src = get("/v1/me", key=KEY_D)["sources"]
    check(len(src) == 1 and src[0]["name"] == "check-d" and src[0]["bundle_id"] == "com.example.check-d"
          and src[0]["kinds"] == ["steps"] and src[0]["last_seen"], f"User.sources after a steps-only sync: {src}")
    check("no-heart-rate-source" in {c["id"] for c in get("/v1/insights", key=KEY_D)},
          "steps but no heart rate this week: the no-heart-rate-source card")
    check(raw("DELETE", "/v1/me/consent", key=KEY_D)[0] == 204, "withdraw consent")
    status, _, err = raw("POST", "/v1/sync", jbody(tiny), base=INGEST, key=KEY_D)
    check(status == 403 and err.get("code") == "consent_required", f"sync after withdrawing: {status} {err}")
    check(get("/v1/me", key=KEY_D)["consent"] is None, "no consent after withdrawing")
    if ADMIN:
        status, _, inv = raw("POST", "/v1/admin/invites", jbody({"note": "check_api", "max_uses": 1}), key=ADMIN)
        check(status == 201 and inv["code"].startswith("RYFT-") and len(inv["code"]) == 14, f"create invite: {status} {inv}")
        check(get("/v1/me", key=KEY_D)["beta_access"] is False, "a new account has no beta access")
        status, _, err = raw("POST", "/v1/me/invite", jbody({"code": "RYFT-ZZZZ-ZZZZ"}), key=KEY_D)
        check(status == 422 and err["error"] == "invalid or used code", f"unknown code: {status} {err}")
        check(raw("POST", "/v1/me/invite", jbody({"code": inv["code"].lower().replace("-", " ")}), key=KEY_D)[0] == 204, "redeem")
        check(get("/v1/me", key=KEY_D)["beta_access"] is True, "beta access after the code")
        E = f"{KEY}:{uuid.uuid4()}"
        status, _, err = raw("POST", "/v1/me/invite", jbody({"code": inv["code"]}), key=E)
        check(status == 422 and err["error"] == "invalid or used code", f"a used-up code: {status}")
        check(raw("DELETE", f"/v1/admin/invites/{inv['code']}", key=ADMIN)[0] == 204, "delete the code")
        raw("DELETE", "/v1/me", key=E)
    raw("DELETE", "/v1/me", key=KEY_D)
    ok("beta: sync 403 consent_required without (and after withdrawing) consent, 200 with; invite codes redeem once, "
       "422 otherwise; a steps-only source in User.sources and the no-heart-rate-source card")
else:
    ok(f"beta: consent to {POLICY} on file")

# ---- profile: birth year, max HR estimate, max-hr-observed (D81) ---------------
check(me_c["max_hr_source"] in {"default", "estimated", "manual"} and "birth_year" in me_c, f"/v1/me max-HR fields: {me_c}")
if os.environ.get("AUTH_MODE", "apikey") == "apikey":  # a fresh account, so the smoke user's max_hr stays
    KEY_E = f"{KEY}:{uuid.uuid4()}"

    def patch_e(body, expect=200):
        status, _, out = raw("PATCH", "/v1/me", jbody(body), key=KEY_E)
        check(status == expect, f"PATCH /v1/me {body}: {status} {out}")
        return out

    year = datetime.now(timezone.utc).year
    tanaka = lambda born: (2080 - 7 * (year - born) + 5) // 10  # round(208 − 0.7 · age), half up
    e = get("/v1/me", key=KEY_E)
    check((e["max_hr"], e["max_hr_source"], e["birth_year"]) == (190, "default", None), f"a new account: {e}")
    e = patch_e({"birth_year": 1990})
    check((e["max_hr"], e["max_hr_source"], e["birth_year"]) == (tanaka(1990), "estimated", 1990), f"estimate: {e}")
    e = patch_e({"max_hr": 197})
    check((e["max_hr"], e["max_hr_source"]) == (197, "manual"), f"by hand: {e}")
    e = patch_e({"birth_year": 1985})
    check((e["max_hr"], e["max_hr_source"], e["birth_year"]) == (197, "manual", 1985), f"manual wins: {e}")
    e = patch_e({"max_hr_manual": False})
    est = tanaka(1985)
    check((e["max_hr"], e["max_hr_source"]) == (est, "estimated"), f"back to the estimate: {e}")
    for body in ({"max_hr": 180, "max_hr_manual": False}, {"birth_year": year - 12}, {"birth_year": 1919}):
        check(is_error(patch_e(body, expect=422)), f"PATCH /v1/me {body} must be 422")
    # Two workouts on two days, 9 and 6 bpm above the estimate: the card names est + 6.
    consent(KEY_E)
    fmt = lambda t: t.strftime("%Y-%m-%dT%H:%M:%SZ")
    samples, workouts = [], []
    for back, peak in ((20, est + 9), (10, est + 6)):
        t0 = datetime.now(timezone.utc).replace(hour=12, minute=0, second=0, microsecond=0) - timedelta(days=back)
        samples += [{"type": "heart_rate", "start": fmt(t0 + timedelta(minutes=m)), "end": fmt(t0 + timedelta(minutes=m)),
                     "value": peak if m == 15 else 150, "unit": "bpm", "source": "check-e"} for m in range(31)]
        workouts.append({"id": str(uuid.uuid4()), "activity": "running", "start": fmt(t0),
                         "end": fmt(t0 + timedelta(minutes=30)), "source": "check-e"})
    status, _, _ = raw("POST", "/v1/sync", jbody({"device_id": str(uuid.uuid4()), "samples": samples, "sleep": [],
                                                 "workouts": workouts}), base=INGEST, key=KEY_E)
    check(status == 200, f"sync E: {status}")
    cards = {c["id"]: c for c in get("/v1/insights", key=KEY_E)}
    card = cards.get("max-hr-observed")
    check(card and card["title"] == f"Your max heart rate may be {est + 6}" and card["metric"] == "strain",
          f"max-hr-observed: {card}")
    patch_e({"max_hr": est + 6})
    check("max-hr-observed" not in {c["id"] for c in get("/v1/insights", key=KEY_E)},
          "after max_hr = the suggestion, one day is above it: no card (and PATCH dropped the cached cards)")
    raw("DELETE", "/v1/me", key=KEY_E)

    # ---- feedback and the admin view (D83), a fresh account for the 10-a-day limit
    KEY_F = f"{KEY}:{uuid.uuid4()}"
    note = f"check_api {uuid.uuid4()}"
    body = {"message": note, "category": "data", "app_version": "0.9.0", "build": "41", "device_model": "iPhone17,1",
            "os_version": "iOS 26.1", "request_ids": ["01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"],
            "diagnostics": {"last_sync": iso_now(), "healthkit_types": 9}}
    since = iso_now()
    check(raw("POST", "/v1/feedback", jbody(body), key=KEY_F)[0] == 204, "POST /v1/feedback")
    for bad in ({"message": " "}, {"message": "x" * 4001}, {"message": "x", "category": "rant"},
                {"message": "x", "request_ids": ["r"] * 21}, {"message": "x", "diagnostics": [1]}):
        status, _, err = raw("POST", "/v1/feedback", jbody(bad), key=KEY_F)
        check(status == 422 and is_error(err), f"POST /v1/feedback {str(bad)[:60]}: {status}")
    for _ in range(9):
        raw("POST", "/v1/feedback", jbody({"message": "again"}), key=KEY_F)
    status, hdrs, err = raw("POST", "/v1/feedback", jbody({"message": "one too many"}), key=KEY_F)
    check(status == 429 and int(hdrs.get("Retry-After", "0")) > 3600 and is_error(err), f"the 11th report today: {status}")
    if ADMIN:
        status, _, fb = raw("GET", f"/v1/admin/feedback?since={since}", key=ADMIN)
        mine = [f for f in fb if f["message"] == note] if status == 200 else []
        check(len(mine) == 1 and mine[0]["category"] == "data" and mine[0]["diagnostics"]["healthkit_types"] == 9
              and mine[0]["request_ids"] == body["request_ids"] and mine[0]["resolved_at"] is None,
              f"GET /v1/admin/feedback: {status} {mine}")
        check(raw("POST", f"/v1/admin/feedback/{mine[0]['id']}/resolve", key=ADMIN)[0] == 204, "resolve")
        status, _, users = raw("GET", "/v1/admin/users", key=ADMIN)
        f_id = get("/v1/me", key=KEY_F)["id"]
        row = next((u for u in users if u["id"] == f_id), None) if status == 200 else None
        check(row and row["short_id"] == f_id[:8] and row["open_feedback"] == 9 and row["sources"] == []
              and row["days_scored_14d"] == 0 and all("email" not in u for u in users),
              f"GET /v1/admin/users (no emails without ?emails=1): {status} {row}")
        check(raw("GET", "/v1/admin/users", key=KEY_F)[0] == 401, "the admin view needs the admin key")
        check(row and row["dup"] == 0, f"the dup column: {row}")
        status, _, det = raw("GET", f"/v1/admin/users/{f_id[:8]}", key=ADMIN)
        check(status == 200 and det["account"]["id"] == f_id and det["data"]["open_feedback"] == 9
              and det["possible_duplicates"] == [] and det["account"]["identities"] is None
              and "identities_note" in det["account"], f"GET /v1/admin/users/{{short id}}: {status} {det}")
        check(raw("GET", "/v1/admin/users/zzzzzzzz", key=ADMIN)[0] == 404, "an unknown short id is 404")

        # ---- two accounts on one phone (D89, D90): the duplicate shows, the merge fixes it
        KEY_G, KEY_H, phone = f"{KEY}:{uuid.uuid4()}", f"{KEY}:{uuid.uuid4()}", str(uuid.uuid4())
        for k, src in ((KEY_G, "check-g"), (KEY_H, "check-h")):
            consent(k)
            t0 = iso_now()
            status, _, _ = raw("POST", "/v1/sync", jbody({"device_id": phone, "samples": [{"type": "steps", "start": t0, "end": t0,
                               "value": 7, "unit": "count", "source": src}], "sleep": [], "workouts": []}), base=INGEST, key=k)
            check(status == 200, f"sync of {src}: {status}")
        g_id, h_id = get("/v1/me", key=KEY_G)["id"], get("/v1/me", key=KEY_H)["id"]
        status, _, det = raw("GET", f"/v1/admin/users/{h_id[:8]}", key=ADMIN)
        check(status == 200 and [d["id"] for d in det["possible_duplicates"]] == [g_id], f"the duplicate: {status} {det}")
        status, _, plan = raw("POST", f"/v1/admin/users/{g_id[:8]}/merge-into/{h_id[:8]}?dry_run=1&force=1", key=ADMIN)
        moved = {t["table"]: (t["moved"], t["dropped"]) for t in plan["tables"]} if status == 200 else {}
        check(status == 200 and plan["dry_run"] and plan["deleted"] is None and moved.get("samples") == (1, 0)
              and moved.get("devices") == (0, 1) and plan["consent"] == "kept", f"the merge plan: {status} {plan}")
        status, _, done = raw("POST", f"/v1/admin/users/{g_id}/merge-into/{h_id}?force=1", key=ADMIN)
        check(status == 200 and not done["dry_run"] and done["deleted"]["status"] == "deleted" and done["rescore"],
              f"the merge: {status} {done}")
        check(raw("POST", f"/v1/admin/users/{g_id}/merge-into/{h_id}?force=1", key=ADMIN)[0] == 404, "a second merge: 404")
        check(raw("GET", "/v1/me", key=KEY_G)[0] == 401, "the merged account is gone")
        h_sources = {x["name"] for x in get("/v1/me", key=KEY_H)["sources"]}
        check(h_sources == {"check-g", "check-h"}, f"to has both accounts' data: {h_sources}")
        raw("DELETE", "/v1/me", key=KEY_H)
    raw("DELETE", "/v1/me", key=KEY_F)
    ok(f"profile: born 1990 → {tanaka(1990)} estimated, manual wins, back to {est}; 422s; "
       f"max-hr-observed suggests {est + 6}, gone once set")
    ok("feedback: 204, 422s, 429 after 10 a day" + ("; admin feedback, resolve, users without emails, the user detail, "
                                                     "two accounts on one phone merged (plan first, 404 the second time)" if ADMIN else ""))

# ---- push: token registry, preferences, the morning event (M7) -----------------
dev, tok = str(uuid.uuid4()), uuid.uuid4().hex + uuid.uuid4().hex
for _ in range(2):  # registering again is an update
    status, _, _ = raw("POST", f"/v1/devices/{dev}/push", jbody({"token": tok.upper(), "env": "sandbox", "locale": "de-DE"}))
    check(status == 204, f"POST /v1/devices/{{id}}/push: {status}")
for path, body, want in ((f"/v1/devices/{dev}/push", {"token": "xyz", "env": "sandbox", "locale": "en"}, 422),
                         (f"/v1/devices/{dev}/push", {"token": tok, "env": "development", "locale": "en"}, 422),
                         (f"/v1/devices/{dev}/push", {"token": tok, "env": "sandbox", "extra": 1}, 400),
                         ("/v1/devices/not-a-uuid/push", {"token": tok, "env": "sandbox"}, 400)):
    status, _, err = raw("POST", path, jbody(body))
    check(status == want and is_error(err), f"POST {path} {body}: {status}, want {want}")
for _ in range(2):
    status, _, _ = raw("DELETE", f"/v1/devices/{dev}/push")
    check(status == 204, f"DELETE /v1/devices/{{id}}/push: {status}")
ok("/v1/devices/{id}/push: register (twice), 422/400 on bad input, delete (twice)")

CATS = {"MORNING_SCORE", "SESSION_SCORED", "PR", "SYNC_STALE"}
prefs0 = get("/v1/me")["notifications"]
check(set(prefs0) == {"categories", "quiet_hours"} and set(prefs0["categories"]) == CATS
      and all(isinstance(v, bool) for v in prefs0["categories"].values()), f"User.notifications: {prefs0}")
status, _, p = raw("PATCH", "/v1/me/notifications", jbody({"categories": {"PR": False}, "quiet_hours": {"from": "22:30", "to": "07:00"}}))
check(status == 200 and p["categories"]["PR"] is False and p["categories"]["MORNING_SCORE"] is True
      and p["quiet_hours"] == {"from": "22:30", "to": "07:00"}, f"PATCH notifications: {status} {p}")
status, _, p = raw("PATCH", "/v1/me/notifications", jbody({"quiet_hours": None}))
check(status == 200 and p["quiet_hours"] is None and p["categories"]["PR"] is False, f"quiet_hours null clears only them: {p}")
check(get("/v1/me")["notifications"] == p, "GET /v1/me shows the preferences")
for body in ({"categories": {"NEWSLETTER": True}}, {"quiet_hours": {"from": "25:00", "to": "07:00"}},
             {"quiet_hours": {"from": "22:00"}}):
    status, _, err = raw("PATCH", "/v1/me/notifications", jbody(body))
    check(status == 422 and is_error(err), f"PATCH notifications {body}: {status}")
status, _, p = raw("PATCH", "/v1/me/notifications", jbody({"categories": prefs0["categories"], "quiet_hours": prefs0["quiet_hours"]}))
check(status == 200 and p == prefs0, f"preferences not restored: {p}")
ok("PATCH /v1/me/notifications: categories merge, quiet hours set and cleared, 422s; restored")

# The morning path end to end (apikey mode, compose): a new user with a token syncs
# a night ending today → scoring publishes ryft.scored → notify decides. Without an
# APNs key it counts "unconfigured", which its /metrics shows.
METRICS = os.environ.get("NOTIFY_METRICS_URL", "")
if METRICS and os.environ.get("AUTH_MODE", "apikey") == "apikey":
    def pushed(category, result):
        with urllib.request.urlopen(METRICS) as r:
            text = r.read().decode()
        line = f'ryft_push_notifications_total{{category="{category}",result="{result}"}} '
        return next((float(l[len(line):]) for l in text.splitlines() if l.startswith(line)), 0.0)

    def handled(reason, result):
        with urllib.request.urlopen(METRICS) as r:
            text = r.read().decode()
        line = f'ryft_events_handled_total{{reason="{reason}",result="{result}"}} '
        return next((float(l[len(line):]) for l in text.splitlines() if l.startswith(line)), 0.0)

    C = str(uuid.uuid4())
    KEY_C = f"{KEY}:{C}"
    before = pushed("MORNING_SCORE", "unconfigured")
    refreshes = handled("refresh", "ok")
    status, _, _ = raw("POST", f"/v1/devices/{uuid.uuid4()}/push", jbody({"token": uuid.uuid4().hex * 2, "env": "sandbox", "locale": "en"}), key=KEY_C)
    check(status == 204, f"C registers a token: {status}")
    consent(KEY_C)
    tz_c = ZoneInfo(get("/v1/me", key=KEY_C)["tz"])
    now_c = datetime.now(tz_c)
    end = min(now_c - timedelta(minutes=10), now_c.replace(hour=7, minute=0, second=0, microsecond=0))
    utc = lambda t: t.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    night = [{"start": utc(end - timedelta(hours=7)), "end": utc(end), "stage": "core", "source": "check"}]
    post("/v1/sync", {"device_id": str(uuid.uuid4()), "samples": [], "sleep": night, "workouts": []}, expect=200, base=INGEST, key=KEY_C)
    for _ in range(30):
        if pushed("MORNING_SCORE", "unconfigured") > before:
            break
        time.sleep(1)
    check(pushed("MORNING_SCORE", "unconfigured") > before, "notify never handled C's ryft.scored (scoring → the broker → notify)")
    for _ in range(30):  # and the silent refresh for the sync's scored days (D92)
        if handled("refresh", "ok") > refreshes:
            break
        time.sleep(1)
    check(handled("refresh", "ok") > refreshes, "notify never handled C's ryft.refresh (scoring → the broker → notify)")
    raw("DELETE", "/v1/me", key=KEY_C)
    ok("morning push path: sync → scoring → ryft.scored and ryft.refresh → notify (no APNs key: counted, not sent)")
else:
    print("- morning push path not checked (needs NOTIFY_METRICS_URL, apikey mode)")

# ---- journal and behavior impact (M5) -------------------------------------------
ALCOHOL = "bad67883-7fb1-5f98-9761-feba0a813536"  # the fixed id API.md documents
beh = get("/v1/behaviors")
builtin = [b for b in beh if not b["is_custom"]]
check(len(builtin) == 25 and {b["kind"] for b in builtin} == {"yes_no", "count", "time"}, f"{len(builtin)} built-in behaviors")
by_slug = {b["slug"]: b for b in beh}
check(by_slug["alcohol"]["id"] == ALCOHOL and by_slug["alcohol"]["name_de"] == "Alkohol", "alcohol's fixed id and names")
status, _, de = raw("GET", "/v1/behaviors", headers={"Accept-Language": "de-DE"})
check(next(b for b in de if b["id"] == ALCOHOL)["name"] == "Alkohol", "Accept-Language: de gives German names")
sauna = by_slug["sauna"]
status, _, l2 = raw("PATCH", "/v1/behaviors", jbody({"enabled": {sauna["id"]: not sauna["enabled"]}}))
check(status == 200 and next(b for b in l2 if b["id"] == sauna["id"])["enabled"] != sauna["enabled"], f"PATCH /v1/behaviors: {status}")
raw("PATCH", "/v1/behaviors", jbody({"enabled": {sauna["id"]: sauna["enabled"]}}))
status, _, err = raw("PATCH", "/v1/behaviors", jbody({"enabled": {str(uuid.uuid4()): True}}))
check(status == 422 and is_error(err), f"PATCH an unknown id: {status}")
cid = str(uuid.uuid4())
for want in (201, 200):  # the same id again updates
    status, _, cb = raw("POST", "/v1/behaviors", jbody({"id": cid, "name": "Check behavior", "kind": "count"}))
    check(status == want and cb["is_custom"] and cb["enabled"] and cb["slug"].startswith("check_behavior"), f"POST /v1/behaviors: {status} {cb}")
for body, want in (({"id": ALCOHOL, "name": "x", "kind": "yes_no"}, 409), ({"id": str(uuid.uuid4()), "name": "x", "kind": "mood"}, 422)):
    status, _, err = raw("POST", "/v1/behaviors", jbody(body))
    check(status == want and is_error(err), f"POST /v1/behaviors {body}: {status}, want {want}")
check(raw("DELETE", f"/v1/behaviors/{ALCOHOL}")[0] == 403, "a built-in cannot be deleted")
check(raw("DELETE", f"/v1/behaviors/{cid}")[0] == 204 and all(b["id"] != cid for b in get("/v1/behaviors")), "DELETE a custom behavior")
ok("/v1/behaviors: 25 built-ins with fixed ids, German names, choose, custom create/update/delete, 403/409/422")

# The Done-when of M5: alcohol on the days before the five weakest-HRV mornings
# of the last 14 (the journal is editable that far back), no alcohol on the others.
tz_j = ZoneInfo(get("/v1/me")["tz"])
today_j = datetime.now(tz_j).date()
days_j = {d["day"]: d for d in get(f"/v1/days?from={(today_j - timedelta(days=15)).isoformat()}&to={today_j.isoformat()}")}
cands = []
for k in range(1, 15):
    d = today_j - timedelta(days=k)
    m = days_j.get((d + timedelta(days=1)).isoformat())
    if m and m["hrv_ms"] is not None and m["recovery"] is not None:
        cands.append((m["hrv_ms"], d.isoformat()))
check(len(cands) >= 10, f"need 10+ scored mornings in the last 14 days for the journal check, have {len(cands)}")
yes_days = {d for _, d in sorted(cands)[:5]}
for _, d in cands:
    status, _, jd = raw("PUT", f"/v1/journal/{d}", jbody({"entries": [{"behavior_id": ALCOHOL, "value_bool": d in yes_days}]}))
    check(status == 200 and jd["answered"] and jd["editable"] and len(jd["entries"]) == 1, f"PUT /v1/journal/{d}: {status} {jd}")
jd = get(f"/v1/journal/{cands[0][1]}")
check(jd["entries"][0]["behavior_id"] == ALCOHOL and set(jd) == {"day", "answered", "answered_at", "editable", "entries"}, f"GET journal: {jd}")
old = (today_j - timedelta(days=15)).isoformat()
status, _, err = raw("PUT", f"/v1/journal/{old}", jbody({"entries": []}))
check(status == 422 and is_error(err), f"15 days back is not editable: {status}")
status, _, err = raw("PUT", f"/v1/journal/{cands[0][1]}", jbody({"entries": [{"behavior_id": by_slug["last_caffeine"]["id"], "value_bool": True}]}))
check(status == 422, f"a time behavior without value_time: {status}")
imp = get("/v1/behaviors/impact")
check(imp["window_days"] == 90 and imp["impacts"], f"impact: {imp}")
a = next(i for i in imp["impacts"] if i["behavior_id"] == ALCOHOL)
r = a["recovery"]
# n_yes ≥ 5, not = 5: entries of earlier runs that have left the editable 14 days
# stay in the 90-day window (they were chosen the same way, so they agree).
check(a["status"] == "unlocked" and a["n_yes"] >= 5 and a["n_no"] >= 5 and r and r["delta"] < 0 and r["ci_hi"] < 0
      and r["significant"] and a["hrv_pct"]["delta"] < 0, f"alcohol's effect: {a}")
check(imp["impacts"][0]["status"] == "unlocked", "unlocked impacts come first")
for i in imp["impacts"]:
    check((i["status"] == "locked") == (i["recovery"] is None) and set(i["needed"]) == {"yes", "no"}, f"impact shape: {i}")
ok(f"journal: alcohol before the 5 weakest mornings → recovery {r['delta']} (90 % {r['ci_lo']} to {r['ci_hi']}), HRV {a['hrv_pct']['delta']} %")

# ---- workouts across midnight -------------------------------------------------
# An 80-minute run from 23:20 to 00:40 local, 40 days back (outside every window
# checked above). Its load must cover all 80 minutes, not just those before midnight.
# A second, sparser heart-rate source (a band reading 60 bpm) overlaps it: source
# de-duplication must keep the watch's curve and load untouched.
tz = ZoneInfo(get("/v1/me")["tz"])
d = date.fromisoformat(TODAY) - timedelta(days=40)
start = datetime(d.year, d.month, d.day, 23, 20, tzinfo=tz).astimezone(timezone.utc)
iso = lambda t: t.strftime("%Y-%m-%dT%H:%M:%SZ")
wid = str(uuid.uuid5(uuid.NAMESPACE_DNS, "check-api-midnight-run"))
hr = [{"type": "heart_rate", "start": iso(start + timedelta(minutes=m)), "end": iso(start + timedelta(minutes=m)),
       "value": 150, "unit": "bpm", "source": "check"} for m in range(81)]
band = [{"type": "heart_rate", "start": iso(start + timedelta(minutes=m, seconds=30)),
         "end": iso(start + timedelta(minutes=m, seconds=30)), "value": 60, "unit": "bpm", "source": "check-band"}
        for m in range(0, 80, 10)]
synced_at = (datetime.now(timezone.utc) - timedelta(seconds=2)).strftime("%Y-%m-%dT%H:%M:%S")
post("/v1/sync", {"device_id": str(uuid.uuid5(uuid.NAMESPACE_DNS, "check-api")), "samples": hr + band, "sleep": [],
                  "workouts": [{"id": wid, "activity": "running", "start": iso(start),
                                "end": iso(start + timedelta(minutes=80)), "source": "check"}]},
     expect=200, base=INGEST)
for _ in range(30):  # wait until scoring has rescored the workout's day after this sync
    if get(f"/v1/days/{d.isoformat()}")["score"]["computed_at"][:19] >= synced_at:
        break
    time.sleep(1)
wk = get(f"/v1/workouts/{wid}")
check(wk["trimp"] is not None, "midnight workout was not scored within 30 s")
check(len(wk["hr"]) == 81 and sum(wk["zone_min"]) >= 78, f"midnight workout zones {wk['zone_min']} (want ~80 min)")
check({p["bpm"] for p in wk["hr"]} == {150}, f"the band's samples leaked into the curve: {sorted({p['bpm'] for p in wk['hr']})}")
ok(f"workout across midnight: {sum(wk['zone_min'])} zone minutes of 80, strain {wk['strain']}; overlapping source ignored")

# ---- Train: exercises, plans, sessions (M3b) -----------------------------------
def call(method, path, body=None, expect=200, key=None, headers=None):
    """One request; expect is a status or a tuple of them. None for an empty body."""
    hdrs = {"Authorization": f"Bearer {key or KEY}", "Content-Type": "application/json", **(headers or {})}
    req = urllib.request.Request(API + path, data=json.dumps(body).encode() if body is not None else None,
                                 method=method, headers=hdrs)
    status, _, raw_body = send(req)
    if status not in (expect if isinstance(expect, tuple) else (expect,)):
        fail(f"{method} {path}: status {status}, want {expect}: {raw_body[:300]!r}")
    return json.loads(raw_body) if raw_body.strip() else None


lib = get("/v1/exercises")
squat = next(e for e in lib if e["slug"] == "back-squat")
check(len(lib) >= 150 and squat["id"] == "053b5627-084e-5417-a033-a6602d812f86" and squat["name"] == "Back squat"
      and squat["load_type"] == "weight" and "quads" in squat["primary_muscles"], f"library: {len(lib)}, {squat}")
de = call("GET", "/v1/exercises?q=kniebeuge&muscle=quads", headers={"Accept-Language": "de-DE,de;q=0.9,en;q=0.8"})
check(any(e["slug"] == "back-squat" and e["name"] == "Kniebeuge" and e["name_en"] == "Back squat" for e in de),
      "Accept-Language de names exercises in German")
call("GET", "/v1/exercises?muscle=pecs", expect=422)
call("PATCH", f"/v1/exercises/{squat['id']}", {"name": "Mine now"}, expect=403)
cx_id = str(uuid.uuid4())
cx_body = {"id": cx_id, "name": f"Check press {cx_id[:4]}", "kind": "dumbbell", "primary_muscles": ["chest"],
           "secondary_muscles": ["triceps"], "load_type": "weight"}
cx = call("POST", "/v1/exercises", cx_body, expect=201)
check(cx["is_custom"] and cx["slug"].startswith("check-press") and cx["name_de"] == cx["name"], f"custom exercise: {cx}")
call("POST", "/v1/exercises", cx_body, expect=200)  # a retry updates
call("POST", "/v1/exercises", {**cx_body, "id": squat["id"]}, expect=409)
ok(f"/v1/exercises: {len(lib)} built-ins, German names, a custom exercise, built-ins read-only")

plans = get("/v1/plans")
fb = next(p for p in plans["builtin"] if p["slug"] == "full-body-3x")
check({p["slug"] for p in plans["builtin"]} >= {"full-body-3x", "upper-lower-4x", "weightlifting-4x", "five-three-one-4x"},
      f"built-in plans: {[p['slug'] for p in plans['builtin']]}")
plan = get(f"/v1/plans/{fb['id']}")
check(len(plan["days"]) == 3 and plan["days"][0]["exercises"][0]["exercise"]["slug"] == "back-squat"
      and plan["progression"]["type"] == "linear", f"full-body-3x: {plan['days'][0]}")
call("PUT", f"/v1/plans/{fb['id']}", plan, expect=403)
call("POST", f"/v1/plans/{fb['id']}/enroll", expect=(200, 201))
today_plan = get("/v1/plans/today")
enr0 = today_plan["enrollment"]
targets = today_plan["targets"]
check(enr0["plan_id"] == fb["id"] and enr0["status"] == "active" and targets
      and all(t["target_kind"] == "kg" and t["target_kg"] is not None for t in targets), f"/v1/plans/today: {today_plan}")
ok(f"/v1/plans: enrolled in {fb['name']}, today is {today_plan['day']['name']} with {len(targets)} targets")

# A session two days ago at 18:00 local, logged offline-style: a first save with
# two sets per exercise, a second with all of them, then a stale replay of the first.
tz = ZoneInfo(get("/v1/me")["tz"])
D = date.fromisoformat(TODAY) - timedelta(days=2)
t_start = datetime(D.year, D.month, D.day, 18, 0, tzinfo=tz).astimezone(timezone.utc)
before_day = get(f"/v1/days/{D}")


def mkset(i, reps, kg, minute):
    t = iso(t_start + timedelta(minutes=minute))
    return {"id": str(uuid.uuid4()), "set_index": i, "reps": reps, "weight_kg": kg, "rpe": 8, "duration_sec": None,
            "distance_m": None, "is_warmup": False, "completed_at": t, "updated_at": t}


sid = str(uuid.uuid4())
exercises, minute = [], 0
for pos, t in enumerate(targets):
    sets = []
    for k in range(t["sets"]):
        minute += 2
        sets.append(mkset(k, t["reps_max"], max(t["target_kg"], 60), minute))
    exercises.append({"id": str(uuid.uuid4()), "exercise_id": t["exercise_id"], "plan_exercise_id": t["plan_exercise_id"],
                      "position": pos, "notes": "", "sets": sets})
exercises.append({"id": str(uuid.uuid4()), "exercise_id": cx_id, "plan_exercise_id": None, "position": len(exercises),
                  "notes": "", "sets": [mkset(k, 8, 22.5, minute + 2 * (k + 1)) for k in range(3)]})
full = {"id": sid, "title": "Check A", "started_at": iso(t_start), "ended_at": None, "status": "in_progress",
        "plan_day_id": today_plan["day"]["id"], "notes": "", "bodyweight_kg": None, "device_id": None, "exercises": exercises}
first = {**full, "exercises": [{**e, "sets": e["sets"][:2]} for e in exercises]}
n_sets = sum(len(e["sets"]) for e in exercises)
check(call("PUT", f"/v1/sessions/{sid}", first, expect=201)["status"] == "in_progress", "first save creates the session")
saved = call("PUT", f"/v1/sessions/{sid}", full, expect=200)
check(sum(len(e["sets"]) for e in saved["exercises"]) == n_sets, "the second save adds the sets")
saved = call("PUT", f"/v1/sessions/{sid}", first, expect=200)
check(sum(len(e["sets"]) for e in saved["exercises"]) == n_sets, "a stale replay must not delete newer sets")
stale = json.loads(json.dumps(first))
stale["exercises"][0]["sets"][0]["reps"] = 1
stale["exercises"][0]["sets"][0]["updated_at"] = iso(t_start)  # older than the stored copy
newer = json.loads(json.dumps(full))
newer["exercises"][0]["sets"][0]["updated_at"] = iso(t_start + timedelta(minutes=30))
call("PUT", f"/v1/sessions/{sid}", newer, expect=200)
conflict = call("PUT", f"/v1/sessions/{sid}", stale, expect=409)
check(conflict["id"] == sid and conflict["exercises"][0]["sets"][0]["reps"] == targets[0]["reps_max"],
      f"409 carries the server's session: {conflict['exercises'][0]['sets'][0]}")
call("PUT", f"/v1/sessions/{uuid.uuid4()}", {**full, "id": None, "status": "completed"}, expect=422)
call("PUT", f"/v1/sessions/{sid}", {**full, "exercises": [{**exercises[0], "exercise_id": str(uuid.uuid4())}]}, expect=422)
call("GET", "/v1/sessions?status=bogus", expect=422)
ok("PUT /v1/sessions/{id}: merge by id, stale replays keep sets, 409 with the server copy, 422 on bad input")

done = call("POST", f"/v1/sessions/{sid}/complete", {"bodyweight_kg": 80, "ended_at": iso(t_start + timedelta(minutes=60))})
check(done["status"] == "completed" and done["muscular_trimp"] > 0 and 0 < done["strain"] <= 21 and done["volume_kg"] > 0
      and done["workout_id"] and done["prs"] and all(p["session_id"] == sid for p in done["prs"]),
      f"completion: {({k: done[k] for k in ('status', 'muscular_trimp', 'strain', 'volume_kg', 'workout_id', 'prs')})}")
check(call("POST", f"/v1/sessions/{sid}/complete", None)["muscular_trimp"] == done["muscular_trimp"], "completing twice is the same")
summ = get(f"/v1/sessions?from={D}&to={D}")
mine = next((x for x in summ if x["id"] == sid), None)
check(mine and mine["duration_min"] == 60 and mine["pr_count"] >= 1 and mine["set_count"] == n_sets, f"summary: {mine}")
# strain.sessions comes from the sessions table at once; the score only after
# scoring has handled the session.completed event, so wait for both.
for _ in range(30):
    day = get(f"/v1/days/{D}")
    st = day["strain"] or {}
    if (any(x["id"] == sid for x in st.get("sessions", []))
            and (day["score"]["strength_min"] or 0) >= (before_day["score"]["strength_min"] or 0) + 60):
        break
    time.sleep(1)
check(any(x["id"] == sid for x in st.get("sessions", [])), f"the day was not rescored with the session: {st}")
sc, sc0 = day["score"], before_day["score"]
check(sc["strength_min"] >= (sc0["strength_min"] or 0) + 60 and sc["volume_kg"] > 0 and st["muscular_trimp"] > 0
      and sc["strain"] is not None and sc["strain"] >= (sc0["strain"] or 0), f"day strength: {sc} vs {sc0}")
wk = [w for w in get(f"/v1/workouts?from={D}&to={D}") if w.get("session_id") == sid]
check(len(wk) == 1 and wk[0]["muscular_load"] > 0 and wk[0]["id"] == done["workout_id"], f"the session's workout: {wk}")
enr1 = get("/v1/plans/today")["enrollment"]
check((enr1["current_week"], enr1["current_day_index"]) != (enr0["current_week"], enr0["current_day_index"]),
      f"completing today's plan day advances the enrolment: {enr0} → {enr1}")
hist_cx = get(f"/v1/exercises/{cx_id}/history")
check(hist_cx["prs"] and hist_cx["sessions"][0]["session_id"] == sid and hist_cx["sessions"][0]["date"] == D.isoformat()
      and hist_cx["e1rm"], f"exercise history: {hist_cx}")
check(get("/v1/trends?metric=strength_min&range=7d")["points"], "strength_min is a trend metric")
ok(f"complete: load {done['muscular_trimp']}, strain {done['strain']}, {len(done['prs'])} PRs; day {D} +{sc['strength_min'] - (sc0['strength_min'] or 0)} min, strain {sc0['strain']} → {sc['strain']}")

tmp = str(uuid.uuid4())
call("PUT", f"/v1/sessions/{tmp}", {**first, "id": tmp, "exercises": [{**first["exercises"][0], "id": str(uuid.uuid4()),
     "sets": [{**first["exercises"][0]["sets"][0], "id": str(uuid.uuid4())}]}]}, expect=201)
call("DELETE", f"/v1/sessions/{tmp}", expect=204)
call("GET", f"/v1/sessions/{tmp}", expect=404)
ok("DELETE /v1/sessions/{id}")

# ---- multi-user isolation (apikey mode: "<API_KEY>:<uuid>" acts as that user) ---
if os.environ.get("AUTH_MODE", "apikey") == "apikey":
    B = str(uuid.uuid4())
    KEY_B = f"{KEY}:{B}"
    me_b = get("/v1/me", key=KEY_B)
    check(me_b["id"] == B and me_b["provider"] == "apikey", f"/v1/me for a new user: {me_b}")
    consent(KEY_B)
    check(get(f"/v1/days?to={TODAY}", key=KEY_B) == [], "a new user must see no days")
    check(get("/v1/insights", key=KEY_B) == [], "a new user must not get another user's insight cards")
    a_wk = wks[-1]
    get(f"/v1/workouts/{a_wk['id']}", expect=404, key=KEY_B)

    # Yesterday and today are left out: scoring's 15-minute pass rescores them, and
    # after the session logged two days back (above) their need and debt can still
    # move when it does. B's data is 50 days back.
    settled = (date.fromisoformat(TODAY) - timedelta(days=1)).isoformat()
    strip = lambda rows: [{k: v for k, v in r.items() if k != "computed_at"} for r in rows if r["day"] < settled]
    a_days = strip(get(f"/v1/days?to={TODAY}"))
    # User B on the same iPhone as A: same device ID, and a workout that reuses
    # A's HealthKit UUID with B's own times. Both must stay separate.
    d_b = date.fromisoformat(TODAY) - timedelta(days=50)
    start_b = datetime(d_b.year, d_b.month, d_b.day, 7, 0, tzinfo=timezone.utc)
    hr_b = [{"type": "heart_rate", "start": iso(start_b + timedelta(minutes=m)), "end": iso(start_b + timedelta(minutes=m)),
             "value": 140, "unit": "bpm", "source": "check-b"} for m in range(31)]
    hrv_b = [{"type": "hrv_sdnn", "start": iso(start_b - timedelta(hours=2)), "end": iso(start_b - timedelta(hours=2)),
              "value": 99, "unit": "ms", "source": "check-b"}]
    synth_device = str(uuid.uuid5(uuid.NAMESPACE_DNS, "synth.ryft.local"))
    post("/v1/sync", {"device_id": synth_device, "samples": hr_b + hrv_b, "sleep": [],
                      "workouts": [{"id": a_wk["id"], "activity": "cycling", "start": iso(start_b),
                                    "end": iso(start_b + timedelta(minutes=30)), "source": "check-b"}]},
         expect=200, base=INGEST, key=KEY_B)
    for _ in range(30):
        b_days = get(f"/v1/days?from={d_b.isoformat()}&to={TODAY}", key=KEY_B)
        if any(d["hrv_ms"] == 99 for d in b_days):
            break
        time.sleep(1)
    check(any(d["hrv_ms"] == 99 for d in b_days), f"B's day was not scored: {b_days}")
    check(all(d["day"] in {d_b.isoformat(), (d_b + timedelta(days=1)).isoformat()} for d in b_days),
          f"B sees days it never synced: {[d['day'] for d in b_days]}")
    check(strip(get(f"/v1/days?to={TODAY}")) == a_days, "B's sync changed A's days")
    wk_a, wk_b = get(f"/v1/workouts/{a_wk['id']}"), get(f"/v1/workouts/{a_wk['id']}", key=KEY_B)
    check(wk_a["activity"] == a_wk["activity"] and wk_a["start"] == a_wk["start"], f"A's workout was overwritten: {wk_a}")
    check(wk_b["activity"] == "cycling" and wk_b["start"] == iso(start_b), f"B's workout with A's UUID: {wk_b}")
    get("/v1/days", expect=401, key="wrong-key-0123456789")
    get("/v1/days", expect=401, key=f"{KEY}:not-a-uuid")
    ok(f"isolation: user {B[:8]}… sees only its own data; shared device and workout UUID stay separate")

    # Train: B sees none of A's sessions, custom exercises or enrolment; the same
    # session id is B's own.
    check(get("/v1/sessions", key=KEY_B) == [], "B must see no sessions")
    get(f"/v1/sessions/{sid}", expect=404, key=KEY_B)
    check(all(not e["is_custom"] for e in get("/v1/exercises", key=KEY_B)), "B must not see A's custom exercises")
    call("DELETE", f"/v1/exercises/{cx_id}", expect=404, key=KEY_B)
    call("GET", "/v1/plans/today", expect=204, key=KEY_B)
    call("PUT", f"/v1/sessions/{sid}", {**first, "exercises": [{**first["exercises"][0], "id": str(uuid.uuid4()),
         "sets": [{**first["exercises"][0]["sets"][0], "id": str(uuid.uuid4())}]}]}, expect=201, key=KEY_B)
    check(get(f"/v1/sessions/{sid}")["status"] == "completed", "B's session with A's id left A's alone")
    ok("Train isolation: sessions, custom exercises and enrolments are per user")

    # ---- one night from two trackers counts once (source de-duplication) ------
    e = date.fromisoformat(TODAY) - timedelta(days=60)
    bed = datetime(e.year, e.month, e.day, 0, 30, tzinfo=ZoneInfo(me_b["tz"])).astimezone(timezone.utc)
    seg = lambda src, stage, off, mins: {"start": iso(bed + timedelta(minutes=off)),
                                         "end": iso(bed + timedelta(minutes=off + mins)), "stage": stage, "source": src}
    night = [seg("check-watch", "core", 0, 200), seg("check-watch", "deep", 200, 100), seg("check-watch", "rem", 300, 120),
             seg("check-ring", "core", 5, 400)]
    post("/v1/sync", {"device_id": synth_device, "samples": [], "sleep": night, "workouts": []},
         expect=200, base=INGEST, key=KEY_B)
    for _ in range(30):
        if get(f"/v1/days?from={e}&to={e}", key=KEY_B):
            break
        time.sleep(1)
    s_b = get(f"/v1/days/{e}", key=KEY_B)["sleep"]
    check(s_b and s_b["total_min"] == 420 and s_b["has_stages"], f"two trackers' night must count once (420 min): {s_b}")
    check(len(s_b["segments"]) == 3, f"hypnogram must show one source: {s_b['segments']}")
    ok("one night from two trackers counts once: 420 min, the watch's stages")

    # ---- steps from a watch and a phone count once per hour ------------------------
    f = date.fromisoformat(TODAY) - timedelta(days=70)
    nine = datetime(f.year, f.month, f.day, 9, 0, tzinfo=ZoneInfo(me_b["tz"])).astimezone(timezone.utc)
    q = lambda typ, src, off, val, unit: {"type": typ, "start": iso(nine + timedelta(minutes=off)),
                                         "end": iso(nine + timedelta(minutes=off + 5)), "value": val, "unit": unit, "source": src}
    act = ([q("steps", "check-watch", 5 * i, 100, "count") for i in range(12)] +      # 09:00 watch: 1200
           [q("steps", "check-phone", 15 * i, 290, "count") for i in range(4)] +      # 09:00 phone, sparser: dropped
           [q("steps", "check-phone", 60 + 20 * i, 200, "count") for i in range(3)] +  # 10:00 phone only: 600
           [q("active_energy", "check-watch", 5 * i, 10, "kcal") for i in range(12)])
    post("/v1/sync", {"device_id": synth_device, "samples": act, "sleep": [], "workouts": []},
         expect=200, base=INGEST, key=KEY_B)
    for _ in range(30):
        f_day = get(f"/v1/days?from={f}&to={f}", key=KEY_B)
        if f_day and f_day[0]["steps"] is not None:
            break
        time.sleep(1)
    check(f_day and f_day[0]["steps"] == 1800 and f_day[0]["active_kcal"] == 120,
          f"steps/kcal with two devices: {f_day and (f_day[0]['steps'], f_day[0]['active_kcal'])}, want 1800/120")
    ok("steps from watch and phone: 1800 (one source per hour), 120 kcal")

    # ---- Apple revocation is not configured locally: a clear 501, not a 500
    status, _, body = raw("POST", "/v1/me/apple-authorization", json.dumps({"code": "c.test"}).encode(), key=KEY_B)
    check(status == 501 and is_error(body), f"apple-authorization without Apple config: {status} {body}")

    # ---- account deletion: B leaves; A (same device ID, same workout UUID) keeps everything
    wk_a = get(f"/v1/workouts/{a_wk['id']}")
    # no Supabase auth user and no Apple token in apikey mode
    gone = {"status": "deleted", "auth_user_deleted": False, "apple_token_revoked": None}
    status, _, body = raw("DELETE", "/v1/me", key=KEY_B)
    check(status == 200 and body == gone, f"DELETE /v1/me: {status} {body}")
    check(is_error(get("/v1/me", expect=401, key=KEY_B)), "a deleted account's token must get 401")
    post("/v1/sync", {"device_id": synth_device, "samples": hr_b, "sleep": [], "workouts": []},
         expect=401, base=INGEST, key=KEY_B)
    status, _, body = raw("DELETE", "/v1/me", key=KEY_B)
    check(status == 200 and body == gone, f"repeating DELETE /v1/me must succeed: {status} {body}")
    check(strip(get(f"/v1/days?to={TODAY}")) == a_days, "deleting B changed A's days")
    check(get(f"/v1/workouts/{a_wk['id']}") == wk_a, "deleting B touched A's workout with the same UUID")
    ok(f"account deletion: {B[:8]}… erased, locked out (api and ingest), repeatable; A untouched")

print("all API contract checks passed")
