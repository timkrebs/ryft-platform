#!/usr/bin/env python3
"""Contract checks against a running stack, after smoke.sh has synced and scored.

Asserts the shapes and invariants docs/API.md promises the iOS app. Exits non-zero
on the first broken promise.

  API_URL=http://localhost:8082 API_KEY=... check_api.py YYYY-MM-DD(today)
"""
import json, os, sys, urllib.error, urllib.request

API = os.environ.get("API_URL", "http://localhost:8082")
KEY = os.environ.get("API_KEY", "dev-api-key-change-me")
TODAY = sys.argv[1]


def get(path, expect=200, auth=True):
    req = urllib.request.Request(API + path, headers={"Authorization": f"Bearer {KEY}"} if auth else {})
    try:
        with urllib.request.urlopen(req) as r:
            status, body = r.status, r.read()
    except urllib.error.HTTPError as e:
        status, body = e.code, e.read()
    if status != expect:
        fail(f"GET {path}: status {status}, want {expect}: {body[:200]!r}")
    return json.loads(body) if body.strip().startswith((b"{", b"[")) else body


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


detail = get(f"/v1/days/{TODAY}")
s = detail["sleep"]
check(s is not None, "/v1/days/{today}: sleep is null after a synced night")
check_night(s, "day sleep")
check(detail["score"]["sleep_actual_min"] == s["total_min"], "sleep.total_min != score.sleep_actual_min")
check(detail["score"]["sleep_performance"] == s["quality"], "sleep.quality != score.sleep_performance")
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
st = get(f"/v1/days/{TODAY}")["strain"]
check(st is not None and st["hr_min"] > 0 and len(st["zone_min"]) == 5, f"day strain: {st}")
ok(f"/v1/days/{TODAY}.strain: trimp {st['trimp']}, {st['hr_min']} HR minutes")

# ---- 2.4 trends ---------------------------------------------------------------
FIELD = {"readiness": "readiness", "recovery": "recovery", "strain": "strain", "sleep": "sleep_performance",
         "sleep_min": "sleep_actual_min", "hrv": "hrv_ms", "rhr": "rhr"}
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
get("/v1/trends?metric=steps", expect=422)
get("/v1/trends?metric=hrv&range=1y", expect=422)
ok("/v1/trends: 90d ok, unknown metric/range → 422")

print("all API contract checks passed")
