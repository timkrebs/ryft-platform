Implements the no-watch recovery contract merged in #255 (`docs/API.md` → Recovery without a watch; D291–D295, plus D298 and D299 from the implementation).

## What it does
- **Routes:** `PUT`/`GET`/`DELETE /v1/recovery/checkins/{date}`, `GET /v1/recovery/checkins?from&to` (≤ 400 days, the export), `GET /v1/recovery/status`, `PUT`/`DELETE /v1/workouts/{id}/rpe`.
- **Score** (`internal/scoring/checkin.go`, pure, model **2026.11.1**): wellness with the 14-day within-subject blend, duration, midpoint regularity, the N2R part, renormalised; soreness has zero weight (`soreness_reported` only). 5 h at 3/3 → 29; 8 h at 9/9 → 93.
- **Precedence:** one wearable-night rule (asleep stages or sleep-window HRV, and a recovery) for the tiers, `needs_checkin`, `superseded` and readiness; a later wearable sync supersedes the check-in.
- **N2R:** the check-in bedtime as the live fallback after the measured sleep start, `bedtime_source`; never persisted into the night record or the meal-timing score.
- **Readiness:** the check-in path where the HRV path is `UNKNOWN`: `OPTIMAL`, `ACUTE_LIFESTYLE_SUPPRESSION` (N2R tags and the journal of the day before, incl. sick/travel/work stress; `journal_sick` keeps the plan protective), `LOW_RECOVERY_UNEXPLAINED`. `readiness_display` on every briefing; `readiness` unchanged.
- **Session RPE:** `session_rpe_load` = minutes × RPE; a rated workout without HR or power enters acute/chronic load, ACWR and TSB as Banister TRIMP with `r = RPE/10` (60 min at 8 → 151). Day strain unchanged.
- **Flag `recovery.checkin`** (0064: enabled, 0 %): the kill switch; off changes nothing for anyone.
- **Data:** deletion, merge, the same-source fold and the session re-attach cover both tables; no rating, time or score in a log line.

## On merge (`deploy.yml`)
1. The migrate job runs **0062** `recovery_checkins`, **0063** `workout_ratings`, **0064** the flag seed. (`0061` stays reserved for the BLS import.)
2. The services roll (api: the routes; ingest: the fold's rating move; scoring: model 2026.11.1). No site change beyond the science page's model version.
3. Nothing changes for any account: the flag is at 0 % with an empty allow-list.

## What you run, in order (later, not on merge)
1. Publish a privacy version that names the check-in (`docs/work/2026-11-26-no-watch-recovery/implementation.md` → Privacy).
2. Then one tester, by the allow-list: `docs/OPERATIONS.md` → Feature flags → `recovery.checkin`.
Rollback: `{"enabled": false, "rollout_pct": 100}`; nothing is deleted.

## Golden files
`internal/scoring/testdata/golden`: `model_version` only (CI's `test` job regenerates them against Postgres and compares).
