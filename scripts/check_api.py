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

print("all API contract checks passed")
