#!/usr/bin/env python3
"""Generate a synthetic /v1/sync payload for N days ending today: a night of sleep
with stages, overnight HRV/respiration/SpO2, a daily resting HR, all-day heart
rate at ~1-minute resolution, steps and active energy in 10-minute buckets (plus a
phone that also counts steps, so source de-duplication has work) and a workout every
other day. --nap MIN adds a nap of MIN minutes yesterday from 13:00 UTC (15:00 in
Berlin), which credits today's sleep need. --demo (scripts/seed_demo.py) names the
sources like a real Apple Watch and iPhone, with bundle ids, and puts the workouts on
Mondays, Wednesdays and Saturdays; without it the output is unchanged.

Usage: synth.py [days] [--nap MIN] [--demo] > payload.json
"""
import json, random, sys, uuid
from datetime import datetime, timedelta, timezone

args = sys.argv[1:]
nap = 0
if "--nap" in args:
    k = args.index("--nap")
    nap = int(args[k + 1])
    del args[k:k + 2]
demo = "--demo" in args
if demo:
    args.remove("--demo")
days = int(args[0]) if args else 10
random.seed(42)
now = datetime.now(timezone.utc).replace(second=0, microsecond=0)
device = str(uuid.uuid5(uuid.NAMESPACE_DNS, "synth.ryft.local"))

samples, sleep, workouts = [], [], []
iso = lambda t: t.isoformat().replace("+00:00", "Z")

def add(t, start, end, value, unit, source="synth"):
    samples.append({"type": t, "start": iso(start), "end": iso(end), "value": round(value, 2), "unit": unit, "source": source})

act = random.Random(7)  # its own stream, so the sleep and heart-rate data above stay as they were

for d in range(days, 0, -1):
    day0 = (now - timedelta(days=d - 1)).replace(hour=0, minute=0)
    bed = day0 - timedelta(hours=1, minutes=random.randint(0, 40))        # ~23:00 the night before
    wake = day0 + timedelta(hours=6, minutes=random.randint(30, 75))      # ~06:30-07:15
    sleep.append({"start": iso(bed), "end": iso(wake), "stage": "inbed", "source": "synth"})
    t = bed + timedelta(minutes=12)
    while t < wake:
        stage = random.choices(["core", "deep", "rem", "awake"], [50, 20, 25, 5])[0]
        length = timedelta(minutes=random.randint(10, 60))
        end = min(t + length, wake)
        sleep.append({"start": iso(t), "end": iso(end), "stage": stage, "source": "synth"})
        t = end

    hrv = random.gauss(52, 6)
    add("hrv_sdnn", wake - timedelta(hours=2), wake - timedelta(hours=2), max(20, hrv), "ms")
    add("respiratory_rate", wake - timedelta(hours=3), wake - timedelta(hours=3), random.gauss(14.5, 0.6), "brpm")
    add("spo2", wake - timedelta(hours=3), wake - timedelta(hours=3), random.gauss(96.5, 0.8), "%")
    add("resting_hr", day0 + timedelta(hours=8), day0 + timedelta(hours=8), random.gauss(50, 2), "bpm")

    # all-day HR, every minute, sedentary baseline
    end_of_day = min(day0 + timedelta(days=1), now)
    t = wake
    workout = None
    if (day0.weekday() in (0, 2, 5)) if demo else d % 2 == 0:
        ws = day0 + timedelta(hours=17, minutes=30)
        we = ws + timedelta(minutes=random.choice([40, 55, 70]))
        workout = (ws, we)
        workouts.append({"id": str(uuid.uuid5(uuid.NAMESPACE_DNS, f"wk-{day0.date()}")), "activity": "running",
                         "start": iso(ws), "end": iso(we), "source": "synth"})
    while t < end_of_day:
        if workout and workout[0] <= t <= workout[1]:
            bpm = random.gauss(152, 8)
        else:
            bpm = random.gauss(68, 6)
        add("heart_rate", t, t, max(40, bpm), "bpm")
        t += timedelta(minutes=1)

    # Steps and active energy in 10-minute buckets while awake; the phone counts
    # the same steps in 30-minute buckets (fewer samples, so the watch wins).
    t = wake
    while t < min(day0 + timedelta(hours=22), now):
        moving = workout and workout[0] <= t <= workout[1]
        step = act.randint(1200, 1700) if moving else act.randint(0, 180)
        add("steps", t, t + timedelta(minutes=10), step, "count")
        add("active_energy", t, t + timedelta(minutes=10), act.uniform(90, 130) if moving else act.uniform(2, 9), "kcal")
        if (t - wake) % timedelta(minutes=30) == timedelta(0):
            add("steps", t, t + timedelta(minutes=30), act.randint(0, 500), "count", source="synth-phone")
        t += timedelta(minutes=10)

# Appended last and without random draws, so every other value stays as it was.
if nap:
    t = (now - timedelta(days=1)).replace(hour=13, minute=0)
    sleep.append({"start": iso(t), "end": iso(t + timedelta(minutes=nap)), "stage": "core", "source": "synth"})

out = {"device_id": device, "device_name": "synth", "samples": samples, "sleep": sleep, "workouts": workouts}
if demo:
    names = {"synth": ("Apple Watch", "com.apple.health.5C1D2E3F-0A1B-4C2D-9E8F-7A6B5C4D3E2F"),
             "synth-phone": ("iPhone", "com.apple.health.9F8E7D6C-5B4A-4392-8170-6F5E4D3C2B1A")}
    for rec in samples + sleep + workouts:
        rec["source"], rec["source_bundle_id"] = names[rec["source"]]
    out.update(device_name="iPhone", app_version="seed_demo", os_version="iOS 26")
print(json.dumps(out))
