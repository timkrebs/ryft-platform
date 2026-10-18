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
