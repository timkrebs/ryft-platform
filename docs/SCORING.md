# RYFT — scoring inputs

How scoring treats the raw HealthKit data before any formula runs, how logged
strength sessions become load (**Strength**, below), what an imported training history
changes (**Imported history**, below), how a night becomes need,
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
`strength_day.go` (the day pass). Decisions: D58–D63, D134–D138 (model 2026.10.2).

**Per set** (completed sets only, `completed_at` set):

| Load type | Set load before the scale |
| --- | --- |
| `weight`, `bodyweight` | `reps × (weight / e1RM)² × muscleFactor`, intensity capped at 1 |
| `time` | `duration_sec / 60 × 0.5 × muscleFactor` |
| `distance` | `distance_m / 100 × 0.4 × muscleFactor` |

- **Weight** is the bar for `weight` exercises; for `bodyweight` ones body weight ×
  `bodyweight_factor` plus any added load (a belt), minus any assistance (`assist_kg`,
  below), never below 0. Body weight is the session's `bodyweight_kg`, else the last one
  logged on an earlier session, else **75 kg**.
- **e1RM** is Epley, `weight × (1 + reps/30)`, with reps capped at **12**: the athlete's
  best over the completed working sets of that exercise in the **90 days** before the
  session's history ends, the session's own sets always included (so intensity never
  exceeds 1, and a first session, or the first after 90 days without the lift, is scored
  against itself). The history ends at the session's `ended_at`, or at its last
  completed set when that is later (`HistoryUntil`): a set stamped after `ended_at` is
  still the session's own. Why 90 days and not the best ever: below.
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
(`ryft.ingested`, reason `session`), after an import (`ryft.ingested`, reason `import`,
scored like a sync's days), and by `scoring rescore`. So a change to these constants takes
effect for history with `scripts/rescore_all.sh`.

**Records** (`PersonalRecords`), recomputed from all completed working sets of an exercise
whenever a session with it is completed, edited, deleted or imported: `e1rm` (Epley as
above), `max_weight`, `max_reps` and `max_volume` (reps × kg in one set) for `weight`
exercises; `max_reps` (and `max_weight` of added load) for `bodyweight` ones; none for
`time` and `distance`. Records are all-time: the 90-day window is only the reference for
intensity. An assisted set holds no record. The first set to reach a value keeps the
record. The insight `pr` reports one set today or yesterday, of a session logged in RYFT
(imported records are silent, **Imported history** below).

**Progression** (`NextTargetKG`, the `target_kg` of `GET /v1/plans/today`), rounded to
2.5 kg:

- `pct_1rm`: `target_value` % of the current e1RM, the best of the last 90 days (as
  above); none without a logged set of the exercise in that window, so a plan never
  prescribes a share of a max from long ago.
- `kg`: `target_value`, moved by a linear progression replayed over the history of that
  plan exercise (oldest first): after an attempt with at least the prescribed number of
  completed working sets at the top of the rep range (`reps_max`) and at least the
  target, + `increment_kg`; after a miss the same target; after `deload_after` misses in
  a row (default 3) 10 % less. Other progression types keep `target_value`.
- `rpe`, `bodyweight`: no kg target.

An attempt is a completed session whose exercise names the plan exercise
(`plan_exercise_id`), or — when it names none — the same exercise on a session started
from that plan day.

**Assisted sets** (model 2026.10.2, D134). `Set.assist_kg` is the support a `bodyweight`
set had: an assistance machine's counterweight or a band, in kg (validation keeps it to
bodyweight exercises, 0–200, and never together with an added load; API.md → `Set`).

| | Rule | Why |
| --- | --- | --- |
| Weight | `max(0, body weight × bodyweight_factor + added − assist)` | The support acts on the moving body (the knee pad, the band under the foot), so it comes off the moved load kilogram for kilogram; the factor is the share of the body that moves, which the support does not change. At or above that share nothing is lifted. |
| Volume | reps × that weight | As for every set. |
| Muscular load | `reps × (weight / e1RM)² × muscleFactor`, unchanged | Intensity is relative to what the athlete can do, so the formula needs no special case. |
| e1RM reference | counts, with its weight | A user who only does assisted pull-ups needs a reference; an unassisted set (more weight) outweighs it anyway. |
| e1RM chart | no point | As for every unloaded bodyweight set (the chart shows `weight` and loaded `bodyweight` sets only). |
| Records | none | Twelve reps with 30 kg of support are not more than eight without, and there is no record kind for "least support". |

`assist_kg` null and 0 mean no support: every number is what model 2026.10.1 gave
(`TestAssistNilOrZeroChangesNothing`: 500 random sets and the three anchors). Calibration
(`strength_import_test.go`): pull-up (factor 1.0, muscleFactor 0.44), 80 kg athlete, 3 × 8
with 30 kg of support:

| Case | Weight | e1RM | Muscular TRIMP | Strain | Volume | Records | 2026.10.1 (support ignored) |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Nothing else in the window | 50 kg | 63.3 | 3.9 | 1.0 | 1200 kg | none | 3.9, 1.0, 1920 kg, max_reps 8 |
| A week after 5 unassisted reps | 50 kg | 93.3 | 1.8 | 0.5 | 1200 kg | max_reps 5 (the unassisted set) | 3.9, 1.0, 1920 kg, max_reps 8 |
| 80 kg of support or more | 0 | none | 0 | 0 | 0 | none | as unassisted |

On its own, an assisted session loads like the same reps unassisted, because both are
measured against the athlete's own sets; next to harder sets it weighs what it is, less. The
built-in `assisted-dip` is a `weight` machine exercise whose number the machine shows as
support, so there a higher number means *less* work. The app maps an imported assisted dip
or pull-up to `dip` / `pull-up` with `assist_kg`, not to `assisted-dip` (D134).

**Why the e1RM reference is the last 90 days** (model 2026.10.2, D135). Model 2026.10.1 took
the best over every set ever logged. With RYFT's own history (the beta began on 2026-10-20)
that was the recent best. An import brings years: a lifter whose squat peaked two years ago
at 140 × 5 (e1RM 163.3) and now squats 5 × 5 at 100 kg (e1RM 116.7) would see that session
fall from strain 6.9 to 3.9, and every later squat session with it, until the old peak is
beaten. For the reference session (5 × 5 squat, bench and row at 80 %) next to peaks 25 % higher
two years earlier: 11.0 with the window, 7.9 against the best ever. A best from before a long
break does not describe what the athlete can lift now: maximal strength declines measurably
after a few weeks to months without training (Mujika & Padilla 2000; Bosquet et al. 2013).
Ninety days holds a training block and a rotation of lifts (a back-squat block, a
front-squat block, back squats again), and it bounds the days an import can move (below). It
is a window, not a decay: one constant, the same answer on every rescore (it is anchored at
the session's end, not at today), and a first session after a break is scored against
itself, as a first session always was. Sources: I. Mujika, S. Padilla, "Detraining: loss of
training-induced physiological and performance adaptations. Part II: long term insufficient
training stimulus", *Sports Med* 30(3), 2000, 145–154; L. Bosquet et al., "Effect of
training cessation on muscular performance: a meta-analysis", *Scand J Med Sci Sports*
23(3), 2013, e140–e149. They support the direction (strength fades over weeks to months),
not the exact 90: that is a judgement, pinned by the tests and open to the beta's data.

| Calibration (`TestE1RMWindow`, `TestHistoryUntilAndAssumedDurations`, `TestEngineScoresAgainstTheE1RMWindow`) | 2026.10.2 | 2026.10.1 |
| --- | --- | --- |
| 5 × 5 squat at 100 kg, a 140 × 5 two years (engine: 200 days) before | TRIMP 32.0, strain 6.9 | 16.3, 3.9 |
| Reference session; peaks 25 % higher two years before, its anchors a week before | strain 11.0 | 7.9 |
| A set exactly 90 days before the session's end / a minute inside | out / in | in / in |
| 4 × 5 at 100 kg and a 110 × 3 at +70 min, `ended_at` +45, +60 or +120 min | TRIMP 28.1 each | 30.2, 30.2, 28.1 |
| The three anchors (11.0, 8.0, 5.1) | unchanged | |

## Imported history (TI-1)

`POST /v1/train/import` (API.md, D124–D130) stores another app's history as completed
sessions with `origin = import`. Scoring treats them as logged sessions. This section says
what that means, with the decisions behind it (D134–D138).

**History changes once.** An import is a change to the past: records, e1RM references, the
next mornings' sleep need. It happens at the import and is then as stable as any other
history. The request queues the days whose stored numbers can move and sends one `ingested`
event (reason `import`), which scoring handles like a sync's days (`ComputeDays` over the
explicit list, then the stored days after a change; D100):

1. the local day of every created or updated session;
2. the days of `unchanged` sessions that an interrupted attempt queued and scoring has not
   finished;
3. the **dependent days** (`scoring.DependentScope`, D135): the user's other completed
   sessions started after the oldest changed one that
   - share an exercise with the changed sessions and start before the newest changed
     session's end + 90 days (later than that, no imported set is in their e1RM window), or
   - have no `bodyweight_kg` of their own, hold a bodyweight exercise and start after the
     oldest changed session with a body weight (their body weight is the last one logged
     before them, which may now be an imported one).

   The replaced version of an updated session counts as changed too. The store's query may
   return more days, never fewer: scoring an unaffected day again changes nothing. Days
   before the oldest changed session never depend on it, because every input looks back.

Only days in the 400 days ending today are scored (`MaxRangeDays`, as in a sync's window).
Older sessions are stored and count for records and the e1RM chart (and, within 90 days,
for the reference of later sessions); their `muscular_trimp`, `strain`, `volume_kg` and
`workout_id` stay `null`. Records are recomputed once per affected exercise.

**Why the dependent days are rescored, not left as scored.** A session's load is a pure
function of the stored history before it. A stored number that disagreed with that function
would move at the next edit of its day, a recompute or `scripts/rescore_all.sh`, at a time
nobody could explain. Rescoring at the import moves it once, when the user did something,
and `days_touched` names the days. The 90-day window keeps the reach to 90 days after the
imported sessions; with the best-ever reference it was everything after them, for good.

**Days that hold only imported sessions** (D137) are what a day with only a logged session
already is: a strain from the muscular load alone (a floor: without heart rate there is no
cardio or everyday movement in it), `strength_min` and `volume_kg`, a created `ryft-session`
workout, no recovery, no readiness, state `calibrating`. They feed what a logged session
feeds, and nothing more, because there is nothing more: the model has no strain baseline.

| Input of later days | From imported days | |
| --- | --- | --- |
| Yesterday's strain → sleep need (3 min per point, at most 60) and the readiness penalty | yes | the user trained that day |
| `details.strength_week` (7 days: minutes, sessions, volume per muscle) | yes | |
| The learned sleep baseline's qualifier (a strain under 10 the day before) | yes | |
| The HRV, resting-HR and breathing baselines | no | the days have none of them |
| `calibrating_reason`'s `W` (days with data) | yes: a logged session's strain is data | after a long import it can name `no_hrv` / `no_rhr` from the first synced day, which is then true of that account; a first night with HRV is still `short_history` |
| Sleep debt | no | it counts nights with sleep only |

A day gets a row when it is scored, at most one per day, so imported days cannot crowd the
30-row baseline window of the days that have nights: they are either days without a row
(before the account's first data, or a gap) or days that already have one. Scoring an
import walks on from each changed day through the next 30 stored rows (`walk`, capped at
`MaxRangeDays`); a new row's strain moves the next day's need, so the walk crosses every
stored row from the oldest imported day on. For 50 sessions that is at most the 400-day
window and the 90 dependent days, seconds to a minute of scoring per request.

**Imported records are silent** (D136): no `PR` push and no `pr` card, however recent
(`store.PRsSince` leaves out records held by an imported session). They are history the
user already saw in the other app, and an import arrives in batches: a record on a card
after the first batch may belong to an older session in the next one. They are records
everywhere else (`GET /v1/exercises/{id}/history`, `Session.prs`, `pr_count`). An import can
take a record away from a session logged in RYFT (an older set with a higher value, or the
same value first); that record's card then goes too, which is right.

**Assumed durations** (D138). When the file has no duration, the app sends `ended_at` =
`started_at` + 60 min. Strain, muscular load, volume, e1RM and records do not read the
duration: a set's load comes from its reps and weight, and the history ends at the last set
when that is later than `ended_at`. The assumption shows in minutes only: `strength_min` (a
trend metric), `strength_week.minutes`, `SessionSummary.duration_min`, and the session's
workout window (which HealthKit workout it joins, by at least half an overlap, and the
span of the created one). No contract flag for now. If the app finds that most imported
sessions lack a duration, an optional flag on the import's session lets scoring leave their
minutes out (`null`) instead of counting 60.

**Not de-duplicated:** a workout logged in RYFT and also in the imported file becomes two
sessions, and both add their load to the day (a strain-11 session twice is 16). The app's
import skips a file workout that overlaps a session logged in RYFT (it has them from
`GET /v1/sessions`) and says so; scoring adds a rule only if the beta shows such pairs (D137).

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
| `W` | days in the window with data: an overnight HRV, a resting HR or a respiratory rate, a strain (heart-rate minutes or a logged session), a night's sleep, steps or active energy (`store.History.HasData`, from `store.dataColumns`), 0–30. A day with only an SpO2, a nap, time in bed, a single heart-rate sample or a workout without heart rate does not count: rare, and it errs toward `short_history` | not stored |
| `H` | of them, days with an overnight HRV (`hrv_ms > 0`): the N of the ln-HRV baseline | `baseline_hrv_days` (migration 0027) |
| `R` | of them, days with a resting HR (`rhr > 0`): the N of the resting-HR baseline | `baseline_rhr_days` (0027) |

`baseline_days = min(H, R)` (0006) and `calibrating = baseline_days < 7`
(`MinBaselineDays`). While a baseline has fewer than 7 values its SD is not the user's
but a population spread (0.18 in ln HRV, 3.5 bpm, 1.0 breath/min), and the state is
`calibrating`, though recovery and readiness may already be there (mostly sleep).

**Why it calibrates** (`calibrating_reason`, `scoring.CalibratingReason`, a pure
function of `W`, `H`, `R` and whether the day itself has an overnight HRV and a resting
HR), in this order:

1. `min(H, R) >= 7`: `null`, not calibrating;
2. the day brings a signal its window never had (`H == 0` and the day has an HRV, or
   `R == 0` and it has a resting HR): `short_history`, for both signals;
3. `W >= 7`, `H == 0` and the day has no HRV either: `no_hrv`;
4. `W >= 7`, `R == 0` and the day has no resting HR either: `no_rhr`;
5. otherwise `short_history`.

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
- **The day's own signal counts, and a new one names nothing missing.** The window
  holds only the days before, so on the first morning of a new source (a watch after
  ten phone-only days, or back after 30 days off) it has no HRV yet, though the night
  has one. That morning is `short_history` for both signals: a source has appeared and
  its other signal may simply not be synced yet. A watch writes the HRV in the night
  and its resting HR later in the day, so a score at 07:00 has the one and not the
  other; calling that `no_rhr` would say "no resting-HR source" until the next sync.
  The next morning the new signal is in the window (`H = 1`), and a signal still
  missing then, in the window and on the day, is named (`no_rhr`).
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
`calibrating`, so the store serves it as unknown too (`store.knownCalibration`). That
check cannot see everything: when the older image's write happens to keep the same
`baseline_days` and `calibrating` (a re-score that changed neither), the counts and the
reason it left are served though they may be stale (a count above `baseline_days`, or a
reason that `W` or the day's own signal would now change). The next score by the
current image corrects them. A day that was never scored has all three `null`.

**The safety net** (`store.BehindHistoryDays`, the 15-minute pass) also compares the two
counts with the nights their window holds now: `baseline_hrv_days < n_hrv` or
`baseline_rhr_days < n_rhr` picks the day. `baseline_days` is the smaller count, so the
larger one (and its baseline) could move without it, as after a lost re-score; that row
is picked once and, re-scored, has the window's counts and is not picked again
(`TestEngineRescoreBehindSeesAStaleCount`). Rows scored before 0027 (`NULL` counts) are
not picked for that: filling them through the pass would spend its 50 days a pass on
the whole history of every active user for hours after the deploy, ahead of the days
that are really behind, for a field the app reads as "unknown" anyway. They are filled
by the next score of their day or by `scripts/rescore_all.sh`. `W` is not stored, so a
reason stale only in `W` is left to the re-score after a change (which treats a day's
data appearing as a change); the pass re-scores today and yesterday anyway.

**Calibration**, pinned without a database by `calibrating_test.go`: every branch
(day one `W = 0` → `short_history`; `W = 30, H = 0` → `no_hrv`; `W = 30, R = 0` →
`no_rhr`; both 0 → `no_hrv`; `H = 3` of 30 → `short_history`; `H, R >= 7` → `null`; the
7th window day as the edge; a new source's first morning, `H = R = 0` with the night's
HRV and with or without its resting HR yet → `short_history`; the next morning, `H = 1`
and still no resting HR → `no_rhr`) and, over every reachable state
`0 <= H, R <= W <= 30` with and without the day's own signals, that the reason is `null`
exactly when `Recovery` says not calibrating and that a signal is called missing only
when neither the window nor the day has it and the day brings no signal its window
lacked. With
`RYFT_TEST_POSTGRES_URL`, `TestEngineCalibratingReason` scores 40 nights for four
set-ups (both signals; no HRV, the report that started this; no resting HR; neither):
`short_history` through the 7th day (`W = 6`), the reason from the 8th (`W = 7`) on, and
`baseline_days = min(H, R)` on every day. The iPhone-only source fixture says the same:
days 1–7 `short_history`, day 8 `no_hrv`. `TestEngineCalibratingReasonIgnoresEmptyDays`:
31 empty stored days before the first night change nothing (the first night is
`short_history` with `W = 0`, not `no_hrv`). `TestEngineLateStepsRescoreTheReason`: a
week of steps synced late for days stored empty turns the 8th day from `short_history`
to `no_hrv` in the same pass. `TestEngineCalibratingReasonNewSourceMorning`: 10 phone-only days, a watch, 35
phone-only days, the watch again: `no_hrv` on the phone days from the 8th on,
`short_history` on each first watch morning. `TestEngineCalibratingReasonBeforeTheRestingHR`:
the first watch night scored at 07:00 with its HRV and without its resting HR is
`short_history`, and stays so once the resting HR syncs; a watch that never writes one
is `no_rhr` the next morning. `TestEngineCalibratingReasonFollowsTheRescore`:
a day scored before its history goes from `short_history` (0/0) to `null` (30/30) when
the history arrives. `TestEngineSafetyNetConvergesOnFreshScores`: on freshly scored
accounts (in order, no HRV, days missing, empty leading days, a backfill newest first)
every row's counts equal its 30 rows before, and the safety net picks nothing.

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
the fields are found by `baseline_hrv_days IS NULL`, not by their version.
**Rescore**: the 15-minute pass re-scores today and yesterday of every active user, so
the Today screen has the reason within a pass of the deploy. Older days keep `null` until `scripts/rescore_all.sh`
(optional; it moves no stored number, it fills the three fields).

## Model version

`internal/scoring/version.go` names the model, `ModelVersion = "2026.10.2"`
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

| Version | Date | What moved | Decisions |
| --- | --- | --- | --- |
| 2026.10.2 | 2026-10-26 | Strength: the e1RM reference is the best of the 90 days before a session's end, its own sets always included; the history ends at the session's last set when that is after `ended_at`; assisted bodyweight sets count with the support taken off and hold no record; `pct_1rm` targets use the same 90-day e1RM. `affects`: `strain` and `volume_kg` directly; `sleep`, `sleep_quality`, `recovery` and `readiness` through the next night's need (3 min per strain point) and the readiness penalty. | D134, D135, D138 |
| 2026.10.1 | 2026-10-20 | The model at the start of the beta. | D80 |

**2026.10.2, before and after.** The new rules differ from 2026.10.1 only for: a set with
`assist_kg` above 0 (the column is new with the import, migration 0028: no stored set had
one); a session with a set of the same exercise more than 90 days before its end and a
higher e1RM there than in the window; and a session with a completed set stamped after its
`ended_at`. `scripts/synth.py` generates no strength sessions, and the changed functions
read nothing but sessions and their sets, so a synthetic run cannot differ (not re-run for
this change). The calibration tables
above are the before/after of each rule; the three anchors (11.0, 8.0, 5.1) and every
2026.10.1 test pass unchanged. Production was not read for this change. **Rescore** after the
deploy: `scripts/rescore_all.sh` (400 days, every user with consent), so every stored row
carries 2026.10.2 and the app does not show two models side by side; until then only rows
scored again (today and yesterday by the 15-minute pass, imported and dependent days) carry
it.

**Numbers of past days can change without a new version.** A day's baselines (the
last 30 scored days), sleep debt and need, consistency and yesterday's strain come from
the stored days before it. When late data changes one of those on a stored day,
scoring re-scores the next 30 scored days with the same model, oldest first, never past
today (a retried event does so even when its own day is already up to date); the
15-minute pass re-scores days whose `baseline_days` (or one of its two counts) is
behind their history or whose baseline is missing although the history has the nights. `model_version` stays; a
re-score never sends a push (D70). A late sample moves later recoveries by 0–2 points;
a day first scored without its history moves by a median of 9 (p90 21) and leaves
`calibrating`.

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
