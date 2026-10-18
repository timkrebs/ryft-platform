# RYFT — scoring inputs

How scoring treats the raw HealthKit data before any formula runs. The formulas
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

## What is left alone

Not de-duplicated: HRV, resting heart rate, respiratory rate and SpO₂. They are a few
readings per night that are averaged (or the latest one is taken), so a second source
moves the value slightly but never doubles it.

Changing these rules changes stored scores. After a deploy, rescore history with
`scripts/rescore_all.sh` (`OPERATIONS.md` → EKS), which runs `scoring rescore` for
every user; a single user can still use `POST /v1/recompute`.
