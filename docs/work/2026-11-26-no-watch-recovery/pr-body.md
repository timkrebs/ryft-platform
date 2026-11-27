Implements the no-watch recovery contract merged in #255 (`docs/API.md` → Recovery without a watch; D291–D295, plus D298 and D299 from the implementation).

## What it does
- **Routes:** `PUT`/`GET`/`DELETE /v1/recovery/checkins/{date}`, `GET /v1/recovery/checkins?from&to` (≤ 400 days, the export), `GET /v1/recovery/status`, `PUT`/`DELETE /v1/workouts/{id}/rpe`.
- **Score** (`internal/scoring/checkin.go`, pure, model **2026.11.1**): wellness with the 14-day within-subject blend, duration, midpoint regularity, the N2R part, renormalised; soreness has zero weight (`soreness_reported` only). 5 h at 3/3 → 29; 8 h at 9/9 → 93.
- **Precedence:** one wearable-night rule (asleep stages or sleep-window HRV, and a recovery) for the tiers, `needs_checkin`, `superseded` and readiness; a later wearable sync supersedes the check-in.
- **N2R:** the check-in bedtime as the live fallback after the measured sleep start, `bedtime_source`; never persisted into the night record or the meal-timing score.
- **Readiness:** the check-in path where the HRV path is `UNKNOWN`: `OPTIMAL`, `ACUTE_LIFESTYLE_SUPPRESSION` (N2R tags and the journal of the day before, incl. sick/travel/work stress; `journal_sick` keeps the plan protective), `LOW_RECOVERY_UNEXPLAINED`. `readiness_display` on every briefing; `readiness` unchanged.
- **Session RPE:** `session_rpe_load` = minutes × RPE; a rated workout without HR or power enters acute/chronic load, ACWR and TSB as Banister TRIMP with `r = RPE/10` (60 min at 8 → 151). Day strain unchanged.
- **Flag `recovery.checkin`** (0066: enabled, 0 %): the kill switch; off changes nothing for anyone.
- **Data:** deletion, merge, the same-source fold and the session re-attach cover both tables; no rating, time or score in a log line.

## On merge (`deploy.yml`)
1. The migrate job runs **0064** `recovery_checkins`, **0065** `workout_ratings`, **0066** the flag seed (0061–0063 are main's BLS migrations). 0065 also gives scoring `UPDATE (start_at, end_at)` on `workouts`, guarded by a trigger to a session's own workout.
2. The services roll (api: the routes; ingest: the fold's rating move; scoring: model 2026.11.1, a session's own workout updated in place on a time edit).
3. The site job (with `SITE_DEPLOY` on) ships the science page's model version 2026.11.1; nothing else on the site changes.
4. Nothing changes for any account: the flag is at 0 % with an empty allow-list. On every account the briefing gains `readiness_display` and N2R gains `bedtime_source` (additive).

## Reviews
`docs/work/2026-11-26-no-watch-recovery/review-impl.md`: one code-reviewer pass and a re-verification; every finding fixed.

## What you run, in order (later, not on merge)
1. Publish a privacy version that names the check-in (`docs/work/2026-11-26-no-watch-recovery/implementation.md` → Privacy).
2. Then one tester, by the allow-list: `docs/OPERATIONS.md` → Feature flags → `recovery.checkin`.
Rollback: `{"enabled": false, "rollout_pct": 100}`; nothing is deleted.

## Golden files
`internal/scoring/testdata/golden`: `model_version` only (CI's `test` job regenerates them against Postgres and compares).
