# RYFT — scoring inputs

How scoring treats the raw HealthKit data before any formula runs, how logged
strength sessions become load (**Strength**, below), how a night becomes need,
quality, consistency and naps (**Sleep**, below), and why a day is calibrating
(**Calibrating and its reason**, below). The other formulas
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

**Golden fixtures** (`internal/scoring/testdata/sources/*.json`): what HealthKit hands
over for four set-ups, Apple Watch + iPhone, Oura + iPhone, iPhone only (eight days, no
sleep tracking), Garmin one day and Apple Watch the next. Each file says in its `notes`
what must happen and works the numbers out: heart-rate hours per source, minutes, zone
minutes and the strain (constant heart rate per stretch makes it arithmetic), resting
heart rate, steps, energy, the night's source and minutes, each workout's curve source,
`User.sources`, the no-heart-rate card. Nothing is snapshotted from the code.
`TestSourceFixtures` runs them through the functions `computeDay` uses (no database, so
always); `TestEngineSourceFixtures` stores them as ingest does and lets the engine
score them (with `RYFT_TEST_POSTGRES_URL`), adding readiness and state: iPhone only stays
`calibrating` with no readiness on day 8 as on day 1. A change to a source rule shows up
here first.

**No heart rate** (`no-heart-rate-source`, info): data arrived in the last 7 days but no
heart rate (an iPhone alone, or a tracker that does not share it with Apple Health).
Strain, recovery and readiness all start from heart rate, so the card says what is
missing instead of leaving the user with empty scores.

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

## Journal (M5)

Code: `internal/behavior` (pure, `behavior_test.go`), the api's
`cmd/api/journal.go`. Decisions: D74–D77.

**Which morning.** A journal entry is about a day: what the user did on day D (the
morning sheet asks about yesterday). Its effect shows the **morning after**: the
recovery and HRV of day D+1 (the night that ends on D+1) and that night's sleep
performance. Pairing an entry with its own day's morning would measure the night
*before* the behavior.

**Yes, no, unknown.** A saved day (`PUT /v1/journal/{day}`) holds exactly the
behaviors the sheet showed: `value_bool` true is a yes, false a no, for every kind (a
`count` behavior is yes when it happened at all, a `time` behavior when it happened
at that time). A behavior not sent on a saved day, and every day never saved, is
unknown and does not count.

**The effect.** For each behavior, over the entry days of the last **90** days whose
morning after is scored:

| Metric | Statistic |
| --- | --- |
| recovery | mean(yes) − mean(no), points |
| HRV | (mean(yes) − mean(no)) ÷ mean(no) × 100, % (nights with HRV only) |
| sleep performance | mean(yes) − mean(no), points |

A behavior is **unlocked** with at least **5** yes and **5** no days with a scored
recovery; HRV and sleep performance need 5 + 5 of their own and are `null` otherwise.
The interval is a **90 % percentile bootstrap**: both groups resampled with
replacement **1 000** times, the 5th and 95th percentile of the statistic. The
generator is seeded with the user, the behavior and the metric, so the same data gives
the same interval on every request. `significant` = the interval excludes 0. One
decimal.

**Order.** Unlocked behaviors first, by the size of the recovery effect; then locked
ones, fewest days still needed first. The list covers every enabled behavior and every
behavior with entries in the window (deleted custom behaviors excepted).

**The card** `behavior-impact` (notice): among the behaviors logged as done for
yesterday, the one with the most negative recovery effect that is unlocked and
significant.

**Computed on request**, not stored (D76): ~44 ms of CPU for 75 behaviors at once, under
15 ms for 25 (`BenchmarkImpacts75x90`). `ryft_behavior_impact_seconds` watches it.

## Max heart rate

Strain and the zones use heart-rate reserve, so they need `max_hr`. It comes from one of
three places (`users.max_hr_source`, migration 0019, D81):

| Source | `max_hr` | When |
| --- | --- | --- |
| `default` | 190 | nothing is known |
| `estimated` | `round(208 − 0.7 · age)` (Tanaka), age = this year − `birth_year` | `PATCH /v1/me` with a `birth_year`, unless manual |
| `manual` | what the user set | `PATCH` with `max_hr` (or `max_hr_manual: true`) |

`scoring.EstimatedMaxHR` computes it in integer tenths, so 35 years (183.5) rounds up to
184 for certain (`TestEstimatedMaxHR` pins seven ages). The estimate is taken when the
birth year or `max_hr_manual: false` is sent, not every January: a stored number never
moves by itself. Accounts that had changed `max_hr` from 190 before the migration are
`manual`. Changing it does not rescore history (`POST /v1/recompute`).

**The card** `max-hr-observed` (info): the highest heart-rate sample inside a workout,
per local day, over the last 90 days (`store.WorkoutPeakHR`: the samples, not the app's
`max_hr`, so every source counts the same). When two different days reach `max_hr + 3`
or more, the card suggests the lower of the two highest days, rounded. One day above can
be a strap glitch; two are a pattern. Nothing changes until the user sets it; the cards
are recomputed after every `PATCH /v1/me`.

## Calibrating and its reason

Recovery compares each overnight signal with the day's **window**: the scored days
stored before it, at most 30 (`store.ScoreHistory`; stored rows, not calendar days, so
a week without data does not shorten it). Three counts come out of that window:

| Count | What it is | Stored as |
| --- | --- | --- |
| `W` | days in the window with any data (an overnight signal, heart rate or a logged session, sleep, steps or active energy: `store.History.HasData`), 0–30 | not stored |
| `H` | of them, days with an overnight HRV (`hrv_ms > 0`): the N of the ln-HRV baseline | `baseline_hrv_days` (migration 0027) |
| `R` | of them, days with a resting HR (`rhr > 0`): the N of the resting-HR baseline | `baseline_rhr_days` (0027) |

`baseline_days = min(H, R)` (0006) and `calibrating = baseline_days < 7`
(`MinBaselineDays`). While a baseline has fewer than 7 values its SD is not the user's
but a population spread (0.18 in ln HRV, 3.5 bpm, 1.0 breath/min), and the state is
`calibrating`, though recovery and readiness may already be there (mostly sleep).

**Why it calibrates** (`calibrating_reason`, `scoring.CalibratingReason`, a pure
function of `W`, `H`, `R`), in this order:

1. `min(H, R) >= 7`: `null`, not calibrating;
2. `W >= 7` and `H == 0`: `no_hrv`;
3. `W >= 7` and `R == 0`: `no_rhr`;
4. otherwise `short_history`.

The question it answers for the app is "does waiting end this?". `short_history`: yes,
after `7 − baseline_days` more nights with both signals. `no_hrv` / `no_rhr`: no, not
until a source writes that signal to Apple Health (an iPhone alone, a watch that does not
share HRV). The decisions behind the rule (docs/API.md → `calibrating_reason`):

- **`W` decides when a missing signal counts as missing**, with the baselines' own bar
  of 7. A new account has `H = R = 0` because nothing is there yet, so it is
  `short_history` until its 8th day with data, the same day a complete account stops
  calibrating. Counting calendar days instead would call a user who synced once a
  month ago `no_hrv` on a single night.
- **`W` counts days with data, not every stored row.** A range or a rescore stores
  every day it covers, also the days before an account's first data
  (`scripts/rescore_all.sh` goes 400 days back), with nothing in them. Counted, they
  would make a watch user's first night `no_hrv` (30 rows, none with an HRV), and in
  the synthetic run below the two empty leading days would start `no_hrv` on the 6th
  day with data instead of the 8th. An iPhone alone still counts: its days have steps.
- **Only `H == 0` is `no_hrv`.** One to six HRV nights in the window are sporadic data
  (a watch worn some nights), and more nights do end them; calling that "no HRV
  source" would be wrong and would flip with every night.
- **`no_hrv` before `no_rhr`** when both are 0: HRV carries 0.45 of recovery against
  resting HR's 0.25, and `baseline_rhr_days == 0` shows the second gap.
- It is a classification of a state the engine already had, from counts it already
  had, not a new estimate: no published method applies, nothing is fitted, and the
  threshold is `MinBaselineDays`, unchanged.

**Stored, not derived on read**: `W` exists only while the day is scored, so the reason
and both counts are written with the row. They are results, not history inputs: a
change to them does not re-score the days after (`store.ScoreWrite`). Whether a day
has data is an input, though: steps or active energy appearing on a day that had none
re-score the days after it, as a changed HRV does (only their presence counts, so the
steps a day gains with every sync do not). Rows scored before 0027 have `NULL` in all
three, served as `null` ("unknown", not a fourth reason) until a rescore fills them. A
row an older scoring image wrote again after this one (a rollback re-pins images; that
image leaves the columns as they were) no longer agrees with its `baseline_days` and
`calibrating`, so the store serves it as unknown too (`store.knownCalibration`). A day
that was never scored has all three `null`.

**Calibration**, pinned without a database by `calibrating_test.go`: every branch
(day one `W = 0` → `short_history`; `W = 30, H = 0` → `no_hrv`; `W = 30, R = 0` →
`no_rhr`; both 0 → `no_hrv`; `H = 3` of 30 → `short_history`; `H, R >= 7` → `null`; the
7th window day as the edge) and, over every reachable state `0 <= H, R <= W <= 30`, that
the reason is `null` exactly when `Recovery` says not calibrating. With
`RYFT_TEST_POSTGRES_URL`, `TestEngineCalibratingReason` scores 40 nights for four
set-ups (both signals; no HRV, the report that started this; no resting HR; neither):
`short_history` through the 7th day (`W = 6`), the reason from the 8th (`W = 7`) on, and
`baseline_days = min(H, R)` on every day. The iPhone-only source fixture says the same:
days 1–7 `short_history`, day 8 `no_hrv`. `TestEngineCalibratingReasonIgnoresEmptyDays`:
31 empty stored days before the first night change nothing (the first night is
`short_history` with `W = 0`, not `no_hrv`). `TestEngineLateStepsRescoreTheReason`: a
week of steps synced late for days stored empty turns the 8th day from `short_history`
to `no_hrv` in the same pass. `TestEngineCalibratingReasonFollowsTheRescore`: a day
scored before its history goes from `short_history` (0/0) to `null` (30/30) when the
history arrives.

**Model version: unchanged** (2026.10.1). Nothing that was stored moves: recovery,
readiness, state, `calibrating` and `baseline_days` are computed as before, and the
tests that pin them pass unchanged. Before/after over the synthetic generator
(`scripts/synth.py 60`, scored as a 62-day range, in four set-ups: as generated, HRV on
every 4th night, no HRV, neither HRV nor resting HR): none of the 31 `DailyScore`
fields the api served before (`details` included) differs on any of the 248 days. The
new fields there: as generated, `short_history` on the 9 calibrating days (2 empty, 7
with data), `null` from the 8th day with data; no HRV and neither, `short_history` for
those 9, then `no_hrv` on the other 53; HRV every 4th night, `short_history` until the
7th HRV night, then `null`. The three fields are new and say what was already true. A
bump would announce moved numbers in `GET /v1/model` where none moved, and rows without
the fields are found by `baseline_hrv_days IS NULL`, not by their version. **Rescore**: the 15-minute pass
re-scores today and yesterday of every active user, so the Today screen has the reason
within a pass of the deploy. Older days keep `null` until `scripts/rescore_all.sh`
(optional; it moves no stored number, it fills the three fields).

## Model version

`internal/scoring/version.go` names the model, `ModelVersion = "2026.10.1"`
(year.month.n). Scoring writes it on every `daily_scores` row and every workout it puts
a load on (`model_version`, migration 0018); rows scored before 2026-10-20 have `null`
until they are rescored. `GET /v1/model` serves it with the changelog
(`internal/scoring/changelog.json`, embedded; newest first, English and German, the
trend metrics it moves), so the app can say why a number changed (D80).

**The bump rule:** any change that moves a stored number (a formula, a constant, a
source or de-duplication rule, a new input) bumps the version in the same commit,
with a changelog entry whose `affects` lists the metrics it moves, and the commit
message says why. A refactor that keeps every number does not. After the deploy:

```bash
scripts/rescore_all.sh        # every user with consent, 400 days; the last line names the version
```

`TestChangelog` fails when the newest entry is not `ModelVersion`; `Changelog()`
refuses entries out of order, an unknown metric or an unsorted `affects`.

## Consent

Scoring computes nothing for a user without a current consent to health-data
processing (D78): a day or a range returns `ErrNoConsent` (counted as
`ryft_days_scored_total{result="no_consent"}`, the event acknowledged, not retried);
the 15-minute pass and `scoring rescore` / `learn-baselines` skip the user. Their
stored scores stay readable until they delete the account.

## What is left alone

Not de-duplicated: HRV, resting heart rate, respiratory rate and SpO₂. They are a few
readings per night that are averaged (or the latest one is taken), so a second source
moves the value slightly but never doubles it.

Changing these rules changes stored scores, so it bumps the model version (above).
After a deploy, rescore history with `scripts/rescore_all.sh` (`OPERATIONS.md` → EKS),
which runs `scoring rescore` for every user with consent; a single user can still use
`POST /v1/recompute`.
