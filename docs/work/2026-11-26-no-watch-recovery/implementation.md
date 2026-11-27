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
| 3 | backend-engineer | `internal/model/recovery.go` (`RecoveryCheckinPut`, `RecoveryCheckin`, `RecoveryComponents`, `RecoveryStatus`, `WorkoutRPEPut`); `Workout.SessionRPE`, `SessionRPELoad` (read-only, `omitempty`); `CoachingBriefing.Recovery *RecoveryStatus` (always serialised); `CoachingBriefing.ReadinessDisplay string` (always serialised, independent of the flag: `readiness` with `AUTONOMIC_FATIGUE` → `LOW_RECOVERY_UNEXPLAINED`; the typed briefing example in `docs/API.md` gains it); `N2RScore.BedtimeSource`. `cmd/api/recovery.go`: the five routes, the flag check (404, no `code`), the consent check (`warn` as `on`). Store: upsert and rescore later check-ins in the next 14 days in the same transaction; one `wearableNight(day)` helper used by the tiers, `needs_checkin`, `superseded` and readiness (asleep stages or sleep-window HRV, and `daily_scores.recovery` not null); `nutritionBedtimes` falls back to the check-in bedtime and reports the source (the live N2R reads pick it up; no stored record to recompute, D293); the check-in's confounders from the live D − 1 N2R and the journal (including `sick`, `travel`, `work_stress`); `periodization.Plan` keeps the protective plan for `ACUTE_LIFESTYLE_SUPPRESSION` with `journal_sick`; `readWorkoutFacts` carries the rating; the fold and the session re-attach move ratings first; `periodization.Readiness` takes the check-in path where the HRV path yields `UNKNOWN`; everything reads check-ins and ratings only while the flag is on (kill switch). Turn the `http` examples into `<!-- type: model.X -->` `json` blocks. | Handler tests below pass; `make check-api-doc` is green with the typed blocks; `make smoke` exercises a check-in. |
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
| Readiness | HRV present but no prior CV (HRV path `UNKNOWN`) + a check-in → the check-in path; drop + `alcohol_above_035gkg` (live D − 1 N2R with the check-in bedtime) → `ACUTE_LIFESTYLE_SUPPRESSION`; drop + journal `late_meal` → the same with `journal_late_meal`; drop alone → `LOW_RECOVERY_UNEXPLAINED` with `deficit_suspended` on a fat-loss goal and carbs ≥ 4.5 g/kg; HRV biometrics present → the HRV path, the check-in ignored; never `AUTONOMIC_FATIGUE` from a check-in; drop + journal `sick`/`travel`/`work_stress` of the day before → `ACUTE_LIFESTYLE_SUPPRESSION` with `journal_sick`/`journal_travel`/`journal_work_stress`, and with `journal_sick` on a fat-loss goal `deficit_suspended` and carbs ≥ 4.5 g/kg; `sick` on the wake day only → not a confounder; soreness true vs false → same score, `acute_drop` and state, only `soreness_reported` differs |
| `readiness_display` | HRV path `AUTONOMIC_FATIGUE` → `readiness` `AUTONOMIC_FATIGUE`, `readiness_display` `LOW_RECOVERY_UNEXPLAINED`; every other state → equal to `readiness`; present with the flag off; the plan for `AUTONOMIC_FATIGUE` unchanged |
| Strain untouched | a rated workout without heart rate → `DailyScore.strain`, `sleep_need_strain_min` and `WorkoutDetail.trimp` as without the rating; the golden files don't move |
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

## Product questions — decided 2026-11-27

The user decided all nine on 2026-11-27, following the head-architect's recommendations (1–5 explicitly; 6–9
as the conservative default). The contract (`docs/API.md`) and D291–D295 carry them.

1. **The Today ring — decided: yes, marked as estimated.** The app fills the ring from
   `RecoveryStatus.recovery_score` while `source` is `checkin` and labels it as estimated from `source` and
   `confidence`. `DailyScore.recovery`/`readiness` stay wearable-only (no reuse); trends, baselines and insights
   never contain a check-in (D291).
2. **Day strain from RPE — decided: no.** The rating feeds acute and chronic load, ACWR and TSB only; day strain,
   its sleep-need top-up and `WorkoutDetail.trimp` do not change (D293).
3. **Soreness — decided: zero weight.** Captured for a later calibration: stored, shown, exported, the reason
   `soreness_reported`; it moves neither the score, `acute_drop` nor the readiness state (D292).
4. **More confounders — decided: yes.** `sick`, `travel` and `work_stress` of the day before explain a drop
   (`journal_sick`, `journal_travel`, `journal_work_stress`) → `ACUTE_LIFESTYLE_SUPPRESSION`, not
   `LOW_RECOVERY_UNEXPLAINED`. With `journal_sick` the plan stays protective (no deficit, the carbohydrate floor;
   our judgement call). Check-in path only; the HRV path does not change (D294).
5. **`AUTONOMIC_FATIGUE` — decided: a neutral name, additively.** The form: a new field
   `CoachingBriefing.readiness_display`, equal to `readiness` except `AUTONOMIC_FATIGUE` → `LOW_RECOVERY_UNEXPLAINED`;
   always present from the implementation PR on, not behind the flag. `readiness` keeps its values, so existing
   clients keep decoding, and the plan logic keeps reading it. A replacement state behind the flag or a
   negotiation header was rejected: an old build would read it as `UNKNOWN` (D294).
6. **Sleep-only trackers — decided (conservative): no special case.** The one rule applies: asleep stages
   without a recovery (no heart rate, no HRV) is not a wearable night, so the user is asked and the check-in is
   scored on the reported times; measured sleep is not blended in. (The question predates review #1, which put
   the recovery into the rule.)
7. **Sleep typed into Apple Health — decided (conservative): no sync field now.** A typed night with asleep
   stages on a day with a recovery still counts as a wearable night; a `was_user_entered` on sleep segments would
   be its own contract change with the app.
8. **Apple's workout effort score — decided (conservative): not read.** `session_rpe` is only the rating given in
   RYFT.
9. **Rollout cohort — decided (conservative): allow-listed testers only**, after a published privacy version
   names the check-in; no percentage rollout, neither to the beta at large nor to the whole Nutrition pilot,
   without a new decision.
