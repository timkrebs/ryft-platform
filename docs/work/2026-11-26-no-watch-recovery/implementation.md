# No-watch recovery: implementation outline (after the contract PR)

The contract is `docs/API.md` → Recovery without a watch; the decisions are D291–D295. The head-architect's
`plan.md` for this work lives in the root repo (`../docs/work/2026-11-26-no-watch-recovery/plan.md`); this
session couldn't read it (outside the platform's allowed directory), so this outline follows the brief alone.
**D-numbers:** the brief reserved D290–D294, but the legal PR #252 already takes D290, so this work uses D291–D295.

## Tickets, in order

| # | Owner | What | Done when |
| --- | --- | --- | --- |
| 1 | database-engineer | The next free migration numbers (0061–0063 on main today; portions v2 runs in parallel, so take them at rebase). Migration **0061** `recovery_checkins`: `(user_id, wake_date)` primary key, `bedtime`, `wake_time` (timestamptz), `tz_offset_sec` int, `perceived_sleep_quality`/`perceived_freshness` smallint `CHECK 1..10`, `soreness` bool, the computed columns (`sleep_duration_min`, `recovery_score`, `components` jsonb, `acute_drop`, `confounders` text[], `confidence`, `model_version`), `created_at`, `updated_at`; RLS and grants like `journal_entries` (api read and write, scoring read). **0062** `workout_ratings`: `(user_id, workout_id)` primary key, `session_rpe numeric(3,1) CHECK 1..10 AND mod(session_rpe*2,1)=0`, `updated_at`; FK to `workouts` `ON DELETE CASCADE` (so the same-source fold and a session's re-attach, `store/train.go:721`, must move the rating **before** they delete or detach the old workout). **0063** seeds the flag `recovery.checkin` (enabled, 0 %), as 0036 did. | `go test ./internal/db/...` migrates up on Postgres; the account deletion and the merge tests cover both tables (the deletion allow-list test fails until they're listed). |
| 2 | scoring-scientist | `internal/scoring/checkin.go`: a pure `CheckinScore(in CheckinInput) CheckinResult` (D292) and `CheckinReadiness` (D294); the constants in one block; `ModelVersion` bump, a `changelog.json` entry (`affects: []`), `docs/SCORING.md` → "Check-in recovery" with every constant marked as a RYFT heuristic or cited. `internal/energy`: the sRPE path in `energy.Workout` (Banister's TRIMP with `r = session_rpe / 10` and the sex constants, when there's no valid HR and no TSS; D293). | Calibration tests below pass; the golden files don't move (no stored daily number changes). |
| 3 | backend-engineer | `internal/model/recovery.go` (`RecoveryCheckinPut`, `RecoveryCheckin`, `RecoveryComponents`, `RecoveryStatus`, `WorkoutRPEPut`); `Workout.SessionRPE`, `SessionRPELoad` (read-only, `omitempty`); `CoachingBriefing.Recovery *RecoveryStatus` (always serialised); `N2RScore.BedtimeSource`. `cmd/api/recovery.go`: the five routes, the flag check (404, no `code`), the consent check (`warn` as `on`). Store: upsert and rescore later check-ins in the next 14 days in the same transaction; one `wearableNight(day)` helper used by the tiers, `needs_checkin`, `superseded` and readiness (asleep stages or sleep-window HRV, and `daily_scores.recovery` not null); `nutritionBedtimes` falls back to the check-in bedtime and reports the source (the live N2R reads pick it up; no stored record to recompute, D293); the check-in's confounders from the live D − 1 N2R and the journal; `readWorkoutFacts` carries the rating; the fold and the session re-attach move ratings first; `periodization.Readiness` takes the check-in path where the HRV path yields `UNKNOWN`; everything reads check-ins and ratings only while the flag is on (kill switch). Turn the `http` examples into `<!-- type: model.X -->` `json` blocks. | Handler tests below pass; `make check-api-doc` is green with the typed blocks; `make smoke` exercises a check-in. |
| 4 | test-engineer (in parallel with 2–3, once the model types exist) | The tests below; `scripts/check_api.py` gains the check-in round trip; `scripts/smoke.sh` puts and deletes one check-in. | Every case below has a test. |
| 5 | api-contract-owner | After 3: the doc examples typed, the Swift mirror compiled (`make check-swift`). | Green. |
| Review | code-reviewer; security-privacy-reviewer (health data, consent, deletion, export, logging: no rating or time in a log line); sre (a new write path on the api; no new metric needed beyond the route labels) | | |

## Packages touched

`internal/scoring` (new `checkin.go`), `internal/energy` (sRPE load), `internal/periodization` (the new state and its
protective plan), `internal/model`, `internal/store` (`recovery.go`, `n2r.go`, `nutrition_score.go`, `coaching.go`,
the fold, deletion, merge), `cmd/api` (routes), `internal/db/migrations` (0061–0063). No new service, event, topic or
Cloud Run change: check-ins are written and scored on the api's request path, like the journal.

## Tests, with the spec's cases

| Case | Expectation |
| --- | --- |
| 5 h, quality 3, freshness 3, no history | `duration` 40, `wellness_absolute` 22, `recovery_score` **29**, `acute_drop` true (both ≤ 4) |
| 8 h, quality 9, freshness 9, no history | `duration` 100, `wellness_absolute` 89, `recovery_score` **93** |
| 14 check-ins at 6/6 in the 14 days before, then 5/5 | SD 0 → floored 6, `z` −1.85, `wellness_personal` 4 (3.7), `wellness` 32, `acute_drop` by `z` only |
| 4 check-ins in the 14 days before | `wellness_personal` null, `wellness = wellness_absolute` |
| Duration curve | 300 → 40, 375 → 70, 450 → 100, 570 → 100, 630 → 90, 720 → 75, 900 → 75 (floor); `sleep_base_min` 480 manual → `L` 480 |
| Regularity | 2 prior nights → null; midpoints 03:00 ×3 and today 03:25 → 100; 05:00 → 50; across midnight (23:50 vs 00:10) → 100 (circular) |
| Renormalisation | no regularity, no N2R → weights 0.5/0.3 scaled by 1/0.8 |
| Retroactive N2R: a 21:30 meal, `expected_bedtime` 23:00, check-in bedtime 22:00 | D − 1's `late_meal_penalty` with 0.5 h to bed is larger than with 1.5 h; `bedtime_source` `checkin`; after DELETE back to `expected`; a wearable `sleep_start` beats the check-in (`wearable`) |
| `needs_checkin` and the wearable night | 03:59 local → false; 04:00, no wearable night, no check-in → true; a check-in saved → false; asleep stages from a watch and a recovery → false; an `in_bed`-only iPhone night → true; **a day-only watch with resting HR (recovery present, no night) → true, and its check-in is `medium` and not `superseded`**; flag off → status 404 |
| Session RPE | 60 min × RPE 8 → `session_rpe_load` **480**, coaching load **151** (sex unspecified), 143 (male), 157 (female); RPE 3 for 60 min → 23 (unspecified); 45.4 minutes at 7 → 315 (45 × 7); the same workout with `avg_hr` → TRIMP, the rating ignored for load; 7.25 → 422; 0.5 → 422; a hidden duplicate → 422; re-sync of the same HealthKit id keeps the rating; fold moves it; a session re-attach moves it; HealthKit deletion removes it; `DELETE …/rpe` on another user's workout → 404 |
| Kill switch | flag off with stored check-ins and ratings → status 404, briefing `recovery` null, readiness back to the HRV path or `UNKNOWN`, load without the ratings, N2R back to `expected`; flag on again → all back |
| Readiness | HRV present but no prior CV (HRV path `UNKNOWN`) + a check-in → the check-in path; drop + `alcohol_above_035gkg` (live D − 1 N2R with the check-in bedtime) → `ACUTE_LIFESTYLE_SUPPRESSION`; drop + journal `late_meal` → the same with `journal_late_meal`; drop alone → `LOW_RECOVERY_UNEXPLAINED` with `deficit_suspended` on a fat-loss goal and carbs ≥ 4.5 g/kg; HRV biometrics present → the HRV path, the check-in ignored; never `AUTONOMIC_FATIGUE` from a check-in |
| Precedence | a check-in, then a sync with overnight HRV for the day → `superseded` true, status `wearable`/`high`; the HRV night deleted → `checkin` again |
| Validation and idempotency | the same PUT twice → equal bodies, `created_at` unchanged; wake day mismatch, 15 days back, future wake, 59 and 961 minutes, `tz_offset_sec` 30 → 422; `7.5` for quality → 400; unknown field → 400 |
| Gates | no consent → 403 `consent_required` on both PUTs, also under `warn`; GET and DELETE without consent → 200/204; flag off → PUT 404, GET/DELETE work; invite gate as everywhere |
| Rescoring | editing a check-in rescores the later ones within 14 days; a model bump rescores on the next recompute |
| Deletion, export, merge | `DELETE /v1/me` removes both tables; merge moves both; the range route caps at 400 days |

## Rollout

1. Merge the implementation PR: `deploy.yml` runs the migrate job (0061–0063), the api rolls (scoring too, for the
   model version). Nothing changes for anyone: the flag is at 0 %.
2. **Not before** a published privacy version names the check-in (below): put testers on the allow-list
   (`OPERATIONS.md` → Feature flags).
3. Rollback: the flag off; the tables stay (no data loss); a revert of the api is safe because nothing else
   reads them.

## Privacy

**What covers it today.** The policy in force (2026-10-26) and #252's draft 2026-11-26 cover "what you enter"
with examples (birth year, training, the journal) and base health data on explicit consent (Art. 9(2)(a)),
including "the health-related parts of what you enter, such as the journal and the food diary". A check-in fits
that basis, so the consent mechanism (`POST /v1/me/consent`, `consent_required`) needs no change. **But no text
names** reported bed and wake times, perceived sleep quality, freshness, soreness or session RPE, or a recovery
estimate computed from them; and #252 says further nutrition features (N2R, coaching) "are not part of this
version". So the check-in can't go live on the current text, and its N2R interplay needs the later N2R version.

**What #252 needs** (if the check-in should go live under 2026-11-26; otherwise the same text in the next version).
This PR doesn't edit #252.

- §2, the list of parts not yet available: add "the morning check-in (Section 3)" next to "the food diary (Section 3)".
- §3, a new bullet after "What you enter":
  > **Morning check-in, where it is available to you** (not yet in the app): when you went to bed and woke up, as
  > you report them, with your time zone; how well you think you slept and how fresh you feel, each from 1 to 10;
  > whether you feel sore; and, for a workout, how hard it felt, from 1 to 10. RYFT computes from them how long you
  > slept, an estimate of your recovery with how much data it rests on, suggested times to pre-fill the next
  > check-in, and the training load of workouts without heart rate. When your wearable records the night, RYFT
  > uses the wearable's data for that day instead and keeps the check-in.
- §3, "What RYFT computes from it": add "and, from a morning check-in, a recovery estimate and whether a day's
  recovery comes from your wearable or from your check-in".
- §5, the health-data bullet: "the health-related parts of what you enter, such as the journal, the morning
  check-in and the food diary".
- §8, portability: add "morning check-ins" to the export's contents once the app's export includes them (until
  then they are covered by "on request").
- §11: add "A recovery estimate from a check-in rests on what you report and is not a measurement."
- **Waits for the version that describes N2R and coaching** (not #252): that a reported bedtime replaces the planned
  bedtime in the nutrition score of the evening before, and that the coaching briefing uses the check-in for
  readiness.

No new processor, no new retention rule (account-bound, deleted with the account, backups 7 days), no new transfer.

## Open product questions (for the user)

1. **The Today ring.** Should a check-in's `recovery_score` show on Today for a no-watch user? Today the ring is
   `DailyScore.recovery`/`readiness`, which stay wearable-only (D291). Showing the check-in there needs a separate
   field and a visual tier, not a reuse.
2. **Day strain from RPE.** Reported effort feeds only the coaching load (D293). Should it also feed day strain
   (0–21) and its sleep-need top-up? That moves stored numbers and needs its own calibration.
3. **Soreness.** Stored, shown, a readiness reason; no score weight. Should it get one, or change the session advice?
4. **More confounders.** Should `sick`, `travel` or `work_stress` from the journal explain a drop (a "lifestyle"
   state) or stay unexplained?
5. **`AUTONOMIC_FATIGUE`.** The HRV path keeps it (v1 renames nothing). Add a neutral alias the app shows, or
   leave the copy neutral on the app's side only?
6. **Sleep-only trackers** (sleep but no overnight HRV): `needs_checkin` is false because the night is measured,
   so they get no recovery at all. Should they be asked for the ratings only, with the measured duration?
7. **Sleep typed into Apple Health.** The sync carries no `was_user_entered` on sleep segments, so a hand-typed
   night with asleep stages on a day with a recovery counts as a wearable night. Add the field to the sync
   (additive; the app may send it only once the server knows it, as strict decoding refuses unknown fields)?
8. **Apple's workout effort score** (HealthKit, iOS 18): should the app forward it as `session_rpe` when the user
   rated the workout on the Watch, or is the RYFT prompt the only source?
9. **Rollout cohort.** Tie `recovery.checkin` to the beta at large, or to the Nutrition pilot first (where the
   briefing and N2R make the most of it)?
