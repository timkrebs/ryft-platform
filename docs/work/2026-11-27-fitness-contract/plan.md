# C1 — the contract for goal, training load, net energy and the Fitness tiles (D304)

> Revised after the code review (`review.md`): `chronic_28d_weekly`; rated workouts on `strain.trimp`'s scale;
> the flag `sync.fitness_types`; `PATCH` of `sex`/`weight_kg` gated by consent; the nits. API.md and D304 are
> authoritative where this plan's first draft differs.

Brief: head-architect, 2026-11-27 (the app's new structure: Fitness tab, Nutrition as tab 4, a goal step in
onboarding, Profile as a sheet). This PR changes docs only: `docs/API.md` (sections, JSON examples, Swift
mirror, changelog), `docs/DECISIONS.md` (D304), `docs/STATUS.md`. The server implements nothing yet; the
implementation PRs are listed at the end. D303 is left free for the revision of #252, and D302 is taken by
`fix/merge-race` (#262).

## What exists today (origin/main 93a7e8d)

| Need | Today | Verdict |
| --- | --- | --- |
| Goal | `User` has none. The Nutrition pilot has `GoalProfile.goal` (`PUT /v1/users/goal-profile`): `HYPERTROPHY`, `MARATHON`, `CYCLING_RACE`, `FAT_LOSS_RECOMP` (`internal/periodization`), with height, race date and more. Swift: `NutritionTrainingGoal`. | **new field** `User.goal` |
| Sex | `User` has none. `N2RProfile.sex` (`female`, `male`, `unspecified`) feeds the Banister constants and Mifflin–St Jeor in the pilot. | **new field** `User.sex`, same vocabulary |
| Weight | `User` has none. `N2RProfile.body_mass_kg` (25–350, pilot); `Session.bodyweight_kg` (Train); `Sample.type` `body_mass` (kg) only for the pilot (`nutrition.enabled`), otherwise `unknown_type`. | **new fields** `User.weight_kg`, `weight_source`, `weight_measured_at` |
| RYFT Age contract | None (no section, no D entry). | sex and weight go on `User` |
| Day TRIMP, zones | `DayDetail.strain.trimp`, `zone_min` (5 HRR zones), `hr_min`; `WorkoutDetail.zone_min`, `trimp` | **exists** (zones) |
| Muscular load | `DayDetail.strain.muscular_trimp`, `Session.muscular_trimp` (D58–D63, D134–D138) | input exists |
| Session RPE | `Workout.session_rpe`, `session_rpe_load`; `energy.RatedWorkout` (TRIMP from RPE, D293) | input exists |
| Coaching load | `CoachingBriefing.training_load` (`acute_7d`, `chronic_28d_weekly`, `acwr`, `tsb`; pilot, workout-based incl. power/TSS) | stays; not the Fitness number |
| Training load per day | — | **new field** `DailyScore.training_load` |
| Active energy | `DailyScore.active_kcal`, trend `active_kcal` | **exists** |
| Resting energy | not synced (`Sample.type` has no basal energy) | **new sync type** `basal_energy` |
| Intake | `GET /v1/nutrition/daily` → `nutrients.energy_kcal.known_subtotal` (diary flag) | input exists |
| Net energy per day | — | **new field** `DailyScore.energy` |
| VO₂ max | synced (`vo2max`), counted in `samples_7d`, read nowhere | **new fields** `DailyScore.vo2max`, `vo2max_delta_7d` |
| Heart-rate recovery | not synced | **new sync type** `heart_rate_recovery`, **new field** `DailyScore.hrr_bpm` |
| Active time | nothing (`hr_min` is wear time, not activity) | **new field** `DailyScore.active_min` |
| Muscle freshness | exercises carry `primary_muscles` / `secondary_muscles` (closed, 18 values, `model.Muscles`); sets carry reps, weight, RPE, `completed_at` | **new field** `DayDetail.muscles` |

## The contract (what API.md gets)

### 1. Profile: `User.goal`, `sex`, `weight_*` (`GET`/`PATCH /v1/me`)

- `goal` (string?): `health`, `endurance`, `strength`, `body_composition`, `performance`; `null` = not chosen or
  "decide later". **Open** in responses (a later version may add a value: show an unknown one like `null`);
  `PATCH` accepts only these five or `null` (`422` otherwise). Default `null`.
- `sex` (string?): `female`, `male`, `unspecified` (the user chose not to say); `null` = never asked. **Closed.**
  `PATCH` accepts the three or `null`.
- `weight_kg` (number?, one decimal), `weight_source` (string?: `manual` or `healthkit`, closed),
  `weight_measured_at` (timestamp?). `PATCH` `weight_kg` (25–350, `422` outside; `null` clears the manual value)
  records a manual weigh-in at the time of the request. **The newest weigh-in wins**: a HealthKit `body_mass`
  sample measured after the manual entry replaces it, and a manual entry replaces older HealthKit readings. No
  "manual pin" (unlike `max_hr_manual`): weight changes, a pin would go stale.
- Goal ↔ Nutrition (`GoalProfile.goal`): `User.goal` is the app-wide goal (the daily recommendation, M3);
  `GoalProfile.goal` stays the pilot's plan goal and wins for nutrition targets. Changing one never writes the
  other; the app pre-fills the Nutrition picker from the table:

  | `User.goal` | Nutrition training goal | Why |
  | --- | --- | --- |
  | `health` | none (maintenance; Nutrition asks) | no deficit or surplus implied |
  | `endurance` | `MARATHON` or `CYCLING_RACE`: pre-select by the dominant workout activity of the last 28 days (cycling → `CYCLING_RACE`, else `MARATHON`); Nutrition confirms | both are endurance fueling |
  | `strength` | `HYPERTROPHY` | surplus, 2 g/kg protein |
  | `body_composition` | `FAT_LOSS_RECOMP` | deficit with the guardrails |
  | `performance` | none (Nutrition asks) | sport-specific; no default is safe |

- `sex` and `weight_kg` feed RYFT Age and the resting-energy estimate (3), not the training load (review #2);
  `N2RProfile` keeps its own `sex` and `body_mass_kg` for the pilot (unchanged, D304).

### 2. Training load: `DailyScore.training_load` (`GET /v1/days`, `GET /v1/days/{day}`)

Where: on `DailyScore`, because the Fitness tab already reads `GET /v1/days/{day}` and its chart reads
`GET /v1/days?from&to`; a separate read would cost a request and its own pending state. **Computed on read**
by the api from stored day values (a pure function in `internal/scoring`), so a late sync of an old day moves
the following 28 days at once without a cascade of rescoring, and no stored number moves.

```json
"training_load": {
  "cardio":   { "acute_7d": 612, "chronic_28d_weekly": 540, "ratio": 1.13, "optimal_low": 432, "optimal_high": 702,
                "state": "optimal", "days_with_data": 27, "window_days": 28 },
  "muscular": { "acute_7d": 118, "chronic_28d_weekly": 150, "ratio": 0.79, "optimal_low": 120, "optimal_high": 195,
                "state": "below", "days_with_data": 28, "window_days": 28 }
}
```

- Daily load: **cardio** = the day's heart-rate TRIMP (`strain.trimp`) + the RPE TRIMP of rated workouts without
  usable heart rate (`energy.RatedWorkout`, D293; so a user without a watch has a cardio load). **Muscular** =
  Σ `muscular_trimp` of the day's completed sessions (the existing session load; per-set RPE is too sparse).
- `acute_7d` = Σ of the 7 days ending on the day (today: so far); `chronic_28d_weekly` = Σ of the 28 days ÷ 4 (weekly
  units, like `energy.Load`); `ratio` = acute ÷ chronic (`null` when chronic is 0); `optimal_low`/`optimal_high` =
  chronic × 0.8 and × 1.3 (Gabbett 2016's band, a RYFT display heuristic, pinned in SCORING.md);
  `state` ∈ `below`, `optimal`, `above` (**closed**), `null` while `days_with_data` < 21 (then the band and
  ratio are `null` too).
- `days_with_data`: cardio — days of the 28 with heart-rate data (`hr_min` > 0) or a rated workout; muscular —
  days of the 28 on or after the account's first completed session. `window_days` is 28.
- The object and both parts are always present from the implementing server; absent before (decode optional).
- Not the coaching load: `CoachingBriefing.training_load` stays the pilot's (workout-based, with power), the
  doc says so.

### 3. Net energy: `DailyScore.energy`

```json
"energy": { "intake_kcal": 2150, "active_kcal": 640, "resting_kcal": 1712, "resting_source": "healthkit",
            "net_kcal": -202, "missing": [], "confidence": "high", "partial_day": false }
```

- `intake_kcal`: Σ kcal of the day's logged meals (as `GET /v1/nutrition/daily` → `energy_kcal.known_subtotal`);
  `null` without a meal or without the diary (`nutrition.diary_enabled` / `nutrition.enabled`).
- `active_kcal` = `DailyScore.active_kcal`.
- `resting_kcal`: Σ of the day's `basal_energy` samples (`resting_source: "healthkit"`), else an estimate from
  `weight_kg`, `birth_year` and `sex` (Henry 2005, weight only; `"estimated"`), else `null` (`resting_source: null`).
  Closed: `healthkit`, `estimated`.
- `net_kcal` = intake − (active + resting), `null` when any part is `null`. Negative = a deficit.
- `missing` (closed values, sorted): `intake`, `active`, `resting`; `[]` when all parts are there.
- `confidence` (closed): `high` (nothing missing, resting measured, intake from ≥ 2 meals, the day is over),
  `medium` (one of those not met), `low` (two or more); `null` when `net_kcal` is `null`. The thresholds are
  pinned in SCORING.md by the implementation PR.
- `partial_day`: `true` for today (the totals so far).
- Always present from the implementing server; absent before.

### 4. Fitness tiles

| Tile | Verdict | Field |
| --- | --- | --- |
| VO₂ max with a 7-day delta | synced, not read: **new fields** | `DailyScore.vo2max` (number?, the newest reading in the 30 days up to the day's end), `vo2max_delta_7d` (number?, `vo2max` minus the same value as of 7 days earlier; `null` when either is) |
| Heart-rate recovery | **new sync type** + **new field** | `Sample.type` `heart_rate_recovery` (bpm, the 1-minute drop; HealthKit `heartRateRecoveryOneMinute`); `DailyScore.hrr_bpm` (number?, mean of the day's readings) |
| Active time | **new field** (derived, no new type) | `DailyScore.active_min` (int?): the union of workout time and the minutes with heart rate in zone 1 or above (≥ 50 % HRR); `null` without heart rate and workouts |
| Active energy | **exists** | `DailyScore.active_kcal` |
| Heart-rate zones | **exist** | `DayDetail.strain.zone_min`, `WorkoutDetail.zone_min` |
| Muscle freshness | **new field** | `DayDetail.muscles` (`[MuscleFreshness]?`): all 18 muscles of `model.Muscles` in that order, `{muscle, freshness (0–100), state, last_trained_at?, sets_7d}`; `state` closed `fresh` (≥ 80), `recovering` (50–79), `fatigued` (< 50); at the request time for today, the day's end otherwise; `null` when the account never completed a session |

Freshness model (for the scoring PR): each completed working set's load is attributed to its primary muscles
(× 1) and secondary (× ½), decays exponentially (time constant per muscle size, provisionally 24–48 h), and is
scaled so a hard session on that muscle gives about 40. A RYFT heuristic, pinned by tests.

### 5. `POST /v1/sync`: new types

`basal_energy` (kcal; HealthKit `basalEnergyBurned`) and `heart_rate_recovery` (bpm); `body_mass` (kg) for every
account, not only the pilot. New HealthKit types change the privacy text, so: **the server stores them only for
an account whose consent is to a policy version that names them** (a server setting set when that policy
ships); before that, and on a server before the implementation PR, they count as `ignored.samples.unknown_type`
(never an error). The app asks for the HealthKit permissions and sends the types only in the build that ships
with the bundled privacy update. `samples_7d` gains the keys `basal_energy`, `heart_rate_recovery`, `body_mass`
(open set). `UserSource.kinds`: `basal_energy` → `energy`, `heart_rate_recovery` → `hr`, `body_mass` →
nothing new (the closed `kinds` enum does not change).

## Privacy (one bundled update, later)

The current policy (2026-10-26) lists the Apple Health types and does not list basal (resting) energy,
heart-rate recovery or body mass from Apple Health; the profile's sex, weight and goal are new too. All of it
goes into one privacy update (with #228 C2 or the #252 revision): the text, `PRIVACY_POLICY_VERSION`, the
consent screen's type list and App Store App Privacy. Not in this PR.

## Implementation PRs, in order

| # | PR | Owner | Estimate | Depends on |
| --- | --- | --- | --- | --- |
| 1 | `me: goal, sex and weight on the profile` — users columns (migration), `GET`/`PATCH /v1/me` (`goal` always sent; `sex`/`weight_kg` need consent to the naming policy version, else `403 consent_required`), export/deletion coverage, Swift-mirror check | database-engineer, backend-engineer | 1 d | — (`sex`/`weight` usable once the privacy update is live) |
| 2 | `scoring: the training load per day` — `scoring.TrainingLoad` (pure, pinned: windows, band, state, coverage, rated workouts), the api read of 27 extra days, SCORING.md | scoring-scientist, backend-engineer | 1.5–2 d | — |
| 3 | `sync: basal energy, heart-rate recovery, body mass for everyone (gated by the policy version)` — ingest types, the flag `sync.fitness_types` + the policy gate, gated cursors skipped, `samples_7d`, HealthKit weight → `User.weight_*` | backend-engineer, security-privacy-reviewer | 1 d | 1 |
| 4 | `scoring: net energy per day` — the day's `resting_kcal` total (column, model version, rescore), the Henry estimate (pure, pinned), `DailyScore.energy` on read with the meal intake | scoring-scientist, database-engineer, backend-engineer | 2 d | 1, 3 |
| 5 | `scoring: VO₂ max, heart-rate recovery, active time` — day fields in the scoring pass (model version, rescore) | scoring-scientist | 1.5 d | 3 (HRR) |
| 6 | `scoring: muscle freshness` — the pure freshness model, `DayDetail.muscles` | scoring-scientist, backend-engineer | 1.5–2 d | — |
| — | privacy bundle (web, separate): text, version, consent list; then the gate's policy version and the flag `sync.fitness_types` on | web-engineer, the user | — | 3 merged |

About 9–10 days of platform work. 2 and 6 can run in parallel with 1.

What the app can build: **now**, every screen against this contract with fixtures (decode every new field as
optional); **after 1**, the onboarding goal step and the Profile sheet for real; **after 2**, the Fitness load
tiles; **after 3**, a build with the new HealthKit permissions, held until the privacy update is live; **after
4**, net energy in Nutrition; **after 5 and 6**, the remaining Fitness tiles.

## Decisions for the user

1. The five goal values and their names (`health`, `endurance`, `strength`, `body_composition`, `performance`),
   and that `health` and `performance` have no Nutrition default.
2. Weight: the newest weigh-in wins (manual or HealthKit), with no manual pin.
3. The optimal band 0.8–1.3 × chronic as a display heuristic, and 21 of 28 days before a state shows.
4. Which privacy update carries the new types (#228 C2 or the #252 revision).

## Review

One code-reviewer pass on the doc diff → `review.md`.
