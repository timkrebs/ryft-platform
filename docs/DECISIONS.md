# Decisions and discrepancies

Where the engagement prompt (`CLAUDE_PROMPT.md`) and the code disagreed, the prompt
won and the difference is recorded here. Judgement calls the prompt left open are
recorded too, so the iOS side and future changes can see why a number is what it is.

## D1 — Container runtime: Docker or Podman

The Makefile called `docker compose` / `docker build` unconditionally. The dev machine
this was built on has Podman only. `make` now uses `docker` when it is on `PATH`,
otherwise `podman` (`podman compose` runs the same `docker-compose.yml`). Override with
`make up CONTAINER=docker` or `COMPOSE="docker compose"`.

## D2 — Stale paths to the iOS app

`model.go` and the README pointed at `ios/Ryft/...` and `../app/RYFT/...`; the app
lives in `../app/Ryft/`. Comments and README fixed, no behaviour change.

## D3 — Readiness penalty shape and missing inputs

The prompt fixes the formula (`round(0.6·recovery + 0.4·sleep)`, "subtract up to 10
points when strain > 14 with recovery < 50") but not how the penalty scales. It ramps
linearly with yesterday's strain: 0 at 14, 10 at 21 (`ReadinessPenalty`), and is only
applied while today's recovery is below 50. "Recovery" is today's recovery — the only
one the prescribed signature `Readiness(recovery, sleepPerf, yesterdayStrain)` receives.

With no sleep recorded last night, readiness uses recovery for both terms (the same
renormalisation `Recovery` does for missing signals). With no recovery it is `null`.

## D4 — `state` is stored, and "calibrating" covers "no score"

`state` is computed at scoring time by `scoring.ReadinessState` and stored next to
`readiness` (migration 0002), so every reader gets the same value without importing
the thresholds. It is `calibrating` whenever the existing `calibrating` flag is set
(fewer than 7 nights of baseline) **or** readiness is `null` — the enum has no
separate "no data" value, and the app should show the calibrating copy in both cases.

## D5 — Sleep quality labels: good ≥ 80, fair ≥ 60

The prompt asks for `good | fair | poor` without thresholds, and its example (and the
brand board) shows quality 82 as "good" — so Whoop's 85/70 split cannot be used. The
bands are good ≥ 80, fair 60–79, poor < 60 (`scoring.SleepQualityLabel`).

## D6 — Stage percentages: staged sleep for deep/light/rem, time in bed for awake

The example (`deep 112 / light 261 / rem 95 / awake 10`, total 468, in bed 492 →
24 / 56 / 20 / 2 %) only works if deep/light/rem are shares of sleep and awake is a
share of time in bed. Deep/light/rem use the largest-remainder method so they always
sum to exactly 100. `stages` always has the four entries in fixed order; when the
source has no stages they are 0 min / 0 % and `has_stages` is false.

## D7 — Sleep additions beyond the prompt's shapes (additive)

- `DayDetail.sleep.segments`: the stage timeline. The brand board's Sleep screen
  draws a hypnogram bar, which the app cannot render from totals.
- `SleepRange` carries `from`, `to`, `nights`, `avg_in_bed_min`, `avg_efficiency` and
  `avg_stages` (what the Week/Month tabs show next to the total), and each day carries
  the full night summary, not only totals and stages.
- `GET /v1/sleep` accepts `to=YYYY-MM-DD` to page back, default today; `range`
  defaults to `week`.
- `days` lists only nights with data; the app fills gaps from `from`/`to`. This avoids
  inventing a "no data" encoding for every numeric field.

## D8 — Typed sleep columns instead of reading `details`

Night totals moved into columns on `daily_scores` (migration 0003) rather than being
parsed out of the free-form `details` JSON, which stays as it was for compatibility.
There is no SQL backfill — old rows get the columns on their next recompute, and the
upgrade procedure in `docs/STATUS.md` recomputes 90 days anyway. All timestamps now
scan from Postgres as UTC (`db.Connect`), so the API emits UTC as the contract says.

## D9 — Workout detail: integers on the wire, app values win

- `hr[].bpm`, `zone_min[]` and `trimp` are rounded integers, as in the prompt's example
  (`"bpm":152`, `"zone_min":[0,5,20,30,2]`, `"trimp":77`); storage keeps one decimal.
- `avg_hr` / `max_hr` stay what the app sent (HealthKit's workout statistics). Only
  when they are missing does the detail derive them from the samples in the window.
  The list endpoint is unchanged.
- Down-sampling uses Largest-Triangle-Three-Buckets rather than bucket means: a
  sprint peak is what the user looks for on the curve, and means flatten it.
- A workout scored from `avg_hr` alone (no samples) credits its whole duration to the
  average's zone, so `zone_min` is never misleadingly empty for it.

## D10 — `DayDetail.strain` (additive, not in the prompt)

The Strain screen needs the day's load, not only per-workout load. The day's TRIMP,
minutes of heart-rate data and zone minutes were already computed but only reachable
through the untyped `details`. They are now stored typed (migration 0004, together
with the workout columns) and served as `DayDetail.strain`.

## D11 — Trend baselines are stored, not recomputed on read

"The 30-day baseline for hrv/rhr" is taken to mean the baseline recovery scores
against: up to 30 scored days *before* the day, HRV as a geometric mean because the
model compares HRV in log space. Scoring already computes it, so it is stored per day
(migration 0005) — new columns, no new tables, as the prompt requires. The insight
rules need the same per-night baseline, and storing it keeps all three consumers
(recovery, trends, insights) on one definition. The two baselines are also exposed on
`DailyScore` (`hrv_baseline_ms`, `rhr_baseline`) so the Today tiles can show "vs. your
baseline" (additive).

## D12 — Trend metric `sleep` is sleep performance; `sleep_min` added

The prompt lists `sleep` without a unit. Since the other trend metrics are scores, and
the Today ring shows sleep as a percentage, `sleep` is `sleep_performance` (%).
`sleep_min` (minutes asleep) is added for duration charts. Points are sparse (days
without a value are left out, not sent as `null`), and `range` defaults to `7d` with an
optional `to` like `/v1/sleep`.

## D13 — Insight rule interpretations

- **"Bedtime variance > 90 min"** is read as *spread* (latest − earliest bedtime over the
  last 7 nights), not statistical variance (minutes², meaningless as a threshold) or
  standard deviation (a 90-min SD essentially never happens to non-shift-workers, so
  the card would never fire). Bedtimes are measured on a noon-to-noon axis so 23:30 and
  00:30 are an hour apart. At least 5 of the 7 nights must have data. Switching to SD
  is a change inside `bedtimeConsistency`.
- **Freshness.** "Today's" value is today's, or yesterday's when today has not synced
  yet; older values never produce a card, so a streak that ended days ago stays silent.
- **Consecutive** means consecutive calendar nights; a night without data ends a streak.
- **Calibrating.** Baseline-relative rules (HRV low, RHR up, best HRV) are silent while
  the day is `calibrating` — a 3-night baseline makes "below baseline" noise.
- **Three hard days without a green recovery** counts strain ≥ 14 days in the last 14,
  resetting on any recovery ≥ 67. A day's recovery is its morning, so a green morning
  followed by a hard day starts a new run that includes that day.
- **Sleep debt** uses `DailyScore.sleep_debt_min` (debt carried into last night) so the
  card and the Today screen show the same number.
- **Readiness trend up** = least-squares slope ≥ 1 point/day over the last 7 days on ≥ 5
  days, so one good morning after a bad one does not count as a trend.
- **History window** is 30 days, not the prompt's 14: "best HRV in 30 days" needs 30. The
  other rules only look at the last 14 or fewer days.
- Cards carry the extra `day` field the prompt's `Insight` example shows; `metric` is a
  `/v1/trends` metric so the app can deep-link.

## D14 — Metrics: route patterns, one package, pinned client_golang

- HTTP metrics label by the ServeMux pattern (`r.Pattern`, set by the nested mux behind
  `BearerAuth`), never the path, so day and UUID paths cannot explode cardinality. The
  prompt's "sync requests and latency" are these metrics with `route="POST /v1/sync"`
  rather than a second, sync-only pair.
- All metrics live in `internal/metrics`; service-specific ones are vectors, so a
  service only exports what it records.
- Added beyond the prompt: `ryft_last_sync_timestamp_seconds` (the "is my phone still
  syncing?" alert) and `ryft_events_published_total` (ingest swallows publish errors by
  design — this makes them visible).
- `prometheus/client_golang` is pinned to v1.23.2: v1.25 requires `go 1.26`, which would
  break the `golang:1.25` build image and the module's Go 1.24 floor.
