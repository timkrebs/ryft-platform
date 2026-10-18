#!/usr/bin/env python3
"""Pretty-print the JSON array from GET /v1/days (read from stdin)."""
import json, sys

rows = json.load(sys.stdin)
print(f"{'day':<12}{'recovery':>9}{'strain':>8}{'sleep%':>8}{'need':>6}{'actual':>8}{'hrv':>7}{'rhr':>6}{'resp':>6}  calibrating")
for d in rows:
    v = lambda k, f="": ("-" if d.get(k) is None else format(d[k], f))
    print(f"{d['day']:<12}{v('recovery'):>9}{v('strain'):>8}{v('sleep_performance'):>8}{v('sleep_need_min'):>6}"
          f"{v('sleep_actual_min'):>8}{v('hrv_ms'):>7}{v('rhr'):>6}{v('resp_rate'):>6}  {d['calibrating']}")
