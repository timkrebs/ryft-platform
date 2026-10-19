# RYFT — scoring inputs

How scoring treats the raw HealthKit data before any formula runs, how logged
strength sessions become load (**Strength**, below), and how a night becomes need,
quality, consistency and naps (**Sleep**, below). The other formulas
(recovery, readiness, strain, sleep need) are described in
[`ARCHITECTURE.md`](ARCHITECTURE.md#scoring-internalscoringalgorithmsgo-unit-tested); the
code is in `internal/scoring`.

## Several sources, one reading

The app uploads every source HealthKit has for a sample type: an iPhone and an Apple
Watch both count steps, and a Garmin or a ring can write heart rate and sleep next to
the watch. HealthKit only promises that one source's own samples do not overlap. So
scoring never sums or mixes sources that overlap; it picks one.

| Data | Unit of decision | Winner | Code |
| --- | --- | --- | --- |
| `heart_rate`, `steps`, `active_energy` | each sample type, each clock hour | the source with the most samples in that hour; tie: the name that sorts first | `DedupSources` |
| sleep segments | one night (the night window of a day) | the source with the most asleep minutes (deep + core + REM + asleep), then the most minutes overall, then the name | `DedupSleepSources` |

Why hours and not days: the watch charges for an hour, and the phone's samples are all
there is for that hour. A per-day rule would throw them away. Within an hour where both
recorded, the denser source is the one that was worn. For sleep, a phone that only logs
time in bed always loses to a tracker that measured sleep.

Where it applies:

- **Day strain** uses the de-duplicated heart rate of the local day.
- **Workout load and the workout curve** (`GET /v1/workouts/{id}`) load the whole hours
  around the workout (`SampleWindow`), de-duplicate them, then cut them to the workout
  window (`WorkoutSamples`). So a workout always uses the same source per hour as the
  day it belongs to, and its load matches its curve.
- **Sleep totals, stages and the hypnogram** use the night's winning source.
- **Steps and active energy** are summed per local day after de-duplication (below).

## Steps and active energy

`daily_scores.steps` and `active_kcal` are the local day's de-duplicated samples, summed
and rounded to whole numbers (`DayTotal`). A sample counts on the day it starts, so a
step sample from 23:55 to 00:05 belongs to the earlier day. `NULL` when no samples of
that type arrived. They feed `DailyScore`, `DayDetail.strain` and the trend metrics
`steps` and `active_kcal`, and nothing else; they do not change strain.

## Strength (Train, M3b)

Logged strength sessions (`PUT /v1/sessions/{id}`, completed with `…/complete`) become a
**muscular load** on the TRIMP scale. It joins the day's heart-rate TRIMP *before* the
strain compression, so a day with only a logged session has a strain, and a day with
both gets more than either alone. Code: `internal/scoring/strength.go` (pure, tested) and
`strength_day.go` (the day pass). Decisions: D58–D63.

**Per set** (completed sets only, `completed_at` set):

| Load type | Set load before the scale |
| --- | --- |
| `weight`, `bodyweight` | `reps × (weight / e1RM)² × muscleFactor`, intensity capped at 1 |
| `time` | `duration_sec / 60 × 0.5 × muscleFactor` |
| `distance` | `distance_m / 100 × 0.4 × muscleFactor` |

- **Weight** is the bar for `weight` exercises; for `bodyweight` ones body weight ×
  `bodyweight_factor` plus any added load (a belt). Body weight is the session's
  `bodyweight_kg`, else the last one logged on an earlier session, else **75 kg**.
- **e1RM** is Epley, `weight × (1 + reps/30)`, with reps capped at **12**: the athlete's
  best over all completed working sets of that exercise up to the session's end, the
  session's own sets included (so intensity never exceeds 1, and a first session is
  scored against itself).
- **Unilateral** exercises log reps per side: × 2. **Warm-up** sets count × 0.25 and
  never set records.
- **muscleFactor** = (Σ primary muscle mass + ½ Σ secondary) × the exercise's
  **load factor**. Muscle mass, relative to the quads:

  | quads, glutes | hamstrings | chest, lats | lower_back, upper_back | adductors, traps | front_delts, triceps | side_delts, biceps, calves, abs | rear_delts, obliques | forearms |
  | --- | --- | --- | --- | --- | --- | --- | --- | --- |
  | 1.0 | 0.8 | 0.7 | 0.6 | 0.4 | 0.35 | 0.3 | 0.25 | 0.2 |

  Load factor by kind (custom exercises get these): barbell 1.0, dumbbell and
  kettlebell 0.9, machine 0.8, cable 0.7, bodyweight 0.32, cardio 0.3. Built-ins may set
  their own: the Olympic lifts and their variants **1.15** (speed and the whole body),
  weighted pull-ups and dips 0.6.

**Per session**: `muscular_trimp = Σ set load × 0.6`, one decimal; `strain` is that alone
on the strain scale (`21 · (1 − e^(−TRIMP/80))`); `volume_kg` is Σ reps × weight of the
completed working sets (both sides when unilateral).

**Calibration**, pinned by `strength_test.go` on the real seed library:

| Reference session | Muscular TRIMP | Strain |
| --- | --- | --- |
| 60 min, 5 × 5 back squat, bench press, barbell row at 80 % of e1RM, 80 kg athlete | 59.3 | **11.0** |
| 12 singles at 90 %: 6 snatches, 6 clean & jerks | 38.1 | **8.0** |
| 20-min bodyweight circuit: 4 × (10 push-ups, 15 bodyweight squats, 45 s plank), no history | 22.3 | **5.1** |

The spec gave the first one as "TRIMP ≈ 85 → strain ≈ 11", but 85 is strain 13.7 on the
existing compression; the strain values are what the user sees, so they are the anchors
(D59). Without a per-exercise load factor the formula puts the circuit near 15 and the
singles near 6.

**The day.** Scoring's pass for a local day takes the completed sessions *started* that
day and, for each one:

1. computes its load as above and stores `muscular_trimp`, `strain`, `volume_kg` on the
   session;
2. attaches it to **one workout**: a HealthKit workout of activity `strength`,
   `functional_strength` or `crossfit` that overlaps at least **half** of the session
   (largest overlap wins), else a workout scoring creates for it (`source = 'ryft-session'`,
   activity `strength`, the session's times, a fixed id), so the Strain screen lists it.
   The workout's `muscular_load` and `session_id` are set; its strain becomes
   `strain(heart-rate TRIMP + muscular load)`. Every pass re-decides: when the watch's
   workout syncs later, the load moves to it and the created workout is deleted;
   a deleted or discarded session's load comes off again;
3. adds the load to the day: `strain = strain(heart-rate TRIMP + Σ muscular_trimp)`,
   `strength_min` (minutes of those sessions), `volume_kg`, and
   `details.strength_week` (minutes, sessions and volume per muscle — primary in full,
   secondary half — over the 7 days ending that day).

Sessions are recomputed whenever their day is: after `…/complete`
(`ryft.session.completed`), after an edit or delete of a completed session
(`ryft.ingested`, reason `session`), and by `scoring rescore`. So a change to these
constants takes effect for history with `scripts/rescore_all.sh`.

**Records** (`PersonalRecords`), recomputed from all completed working sets of an exercise
whenever a session with it is completed, edited or deleted: `e1rm` (Epley as above),
`max_weight`, `max_reps` and `max_volume` (reps × kg in one set) for `weight` exercises;
`max_reps` (and `max_weight` of added load) for `bodyweight` ones; none for `time` and
`distance`. The first set to reach a value keeps the record. The insight `pr` reports one
set today or yesterday.

**Progression** (`NextTargetKG`, the `target_kg` of `GET /v1/plans/today`), rounded to
2.5 kg:

- `pct_1rm`: `target_value` % of the current e1RM; none without logged sets.
- `kg`: `target_value`, moved by a linear progression replayed over the history of that
  plan exercise (oldest first): after an attempt with at least the prescribed number of
  completed working sets at the top of the rep range (`reps_max`) and at least the
  target, + `increment_kg`; after a miss the same target; after `deload_after` misses in
  a row (default 3) 10 % less. Other progression types keep `target_value`.
- `rpe`, `bodyweight`: no kg target.

An attempt is a completed session whose exercise names the plan exercise
(`plan_exercise_id`), or — when it names none — the same exercise on a session started
from that plan day.

## Sleep (M4)

Code: `internal/scoring/sleep.go` (pure, tested in `sleep_upgrades_test.go`),
`compute.go` (the day pass), `learn.go` (the weekly estimate). Decisions: D65–D68.

**The night and the naps.** Day D's night window is D−1 20:00 to D 13:00 local (as
before); its segments come from one source (`DedupSleepSources`). They are grouped into
blocks: a pause of **60 min** or more between one segment's end and the next one's
start ends a block. The block with the most minutes asleep is **the night**. Another
block that ends at or after **10:00** is a morning nap. Every other block stays with the
night, so dozing on the sofa at 21:00 still counts as before. A late sleeper's single
block ending at 11:30 is the night, not a nap.

D's naps are those morning blocks plus the sleep ending **13:00–20:00** local (which no
night window covers), also from one source. Pauses under **30 min** join one nap; a nap
counts from **20 min** asleep; the day's total is capped at **90 min**. That is
`daily_scores.nap_min` (`0` without naps).

**Need, term by term.**

| Term | Value | Column |
| --- | --- | --- |
| base | `users.sleep_base_min` (below) | `sleep_need_base_min` |
| strain | `min(60, 3 · yesterday's strain)`, rounded | `sleep_need_strain_min` |
| debt | `min(60, 0.3 · debt)`, rounded; debt as before (7 nights, 0.7/day decay) | `sleep_need_debt_min` |
| nap credit | **yesterday's** naps (day D−1), at most 90 | `sleep_need_nap_min` |

`sleep_need_min = base + strain + debt − nap credit`. Each top-up is now rounded on its
own (it was the sum), so the terms the API reports add up; a need can differ by a minute
from before. The credit is computed from D−1's raw segments when D is scored, so a nap
that synced late counts as soon as D is scored again.

**Quality.** `quality = round(0.6 · performance + 0.2 · efficiency + 0.2 · restorative)`,
stored as `sleep_quality`. Restorative is `min(100, (deep + REM) / asleep / 0.40 · 100)`;
without stages it equals performance. Pins: the brand board's 82 % "good" is
performance 72, efficiency 95, restorative 100; the API example 91 is 87 / 95 / 100. It
used to be equal to performance. The Sleep screen reads the stored value and computes it
from the stored totals for nights not rescored yet.

**Consistency.** `100 − 0.5 · (σ bedtime + σ wake time)`, minutes, population standard
deviation on a noon-to-noon axis (23:30 and 00:30 are an hour apart), clamped to 0–100,
over the **7 nights** ending on D (D's own included) with at least **5** having data;
else `null`. Bedtime and wake time are the night's first and last segment. Pins: ± 15 min
around the same times → 85, ± 45 min → 55.

**Learned baseline.** Every Sunday from **04:00** local (scoring's 15-minute pass, once
per user and Sunday; skipped when the last estimate is younger than 6 days):

1. take the last **30** scored days up to today;
2. a night *qualifies* when that morning's recovery is **≥ 67** and the day before had a
   strain **< 10** (no score that day counts as easy);
3. with at least **14** nights of sleep and **7** qualifying ones, the estimate is the
   median minutes asleep of the qualifying nights, clamped to **360–570**;
4. it is stored as `sleep_base_learned_min` / `_at`. Unless the user set the baseline by
   hand (`sleep_base_source = manual`, which wins), it becomes `sleep_base_min` with
   source `learned`, and when that value moved the **last 7 days** are rescored in the
   same process, oldest first.

`scoring learn-baselines` runs the estimate for every user at once (`OPERATIONS.md`).
`PATCH /v1/me` with `sleep_base_min` makes the source `manual`; `sleep_base_manual:
false` goes back to the estimate, or to 460 without one. Migration 0013 marks users who
had already changed 460 as manual.

**Planner** (`GET /v1/sleep/plan`). Tonight's need from the baseline, **today's** strain
so far, the debt including last night and today's naps; time in bed = need ÷
efficiency, with efficiency the mean of the last 7 nights, **90** without any and never
below **85**; bedtimes `wake − share · in bed` for 100, 85 and 70 %. Pin: the API example
(need 512, 569 in bed, 19:31 / 20:56 / 22:22 for a 05:00 wake).

**After a deploy that changes these rules**, rescore history (`scripts/rescore_all.sh`)
so stored quality, consistency, naps and the need terms are filled for past days; days
not rescored report the whole need as base.

## What is left alone

Not de-duplicated: HRV, resting heart rate, respiratory rate and SpO₂. They are a few
readings per night that are averaged (or the latest one is taken), so a second source
moves the value slightly but never doubles it.

Changing these rules changes stored scores. After a deploy, rescore history with
`scripts/rescore_all.sh` (`OPERATIONS.md` → EKS), which runs `scoring rescore` for
every user; a single user can still use `POST /v1/recompute`.
