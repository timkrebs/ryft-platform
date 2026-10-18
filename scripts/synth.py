#!/usr/bin/env python3
"""Generate a synthetic /v1/sync payload for N days ending today: a night of sleep
with stages, overnight HRV/respiration/SpO2, a daily resting HR, all-day heart
rate at ~1-minute resolution, steps and active energy in 10-minute buckets (plus a
phone that also counts steps, so source de-duplication has work) and a workout every
other day.

Usage: synth.py [days] > payload.json
"""
import json, random, sys, uuid
from datetime import datetime, timedelta, timezone

days = int(sys.argv[1]) if len(sys.argv) > 1 else 10
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
    if d % 2 == 0:
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

print(json.dumps({"device_id": device, "device_name": "synth", "samples": samples, "sleep": sleep, "workouts": workouts}))
