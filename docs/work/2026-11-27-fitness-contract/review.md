# C1 review (code-reviewer, 2026-11-27, on the uncommitted doc diff)

No blockers. The change is additive, `make check-api-doc` passes, the removed lines are all rewordings, `kinds` gains no
value, and the example numbers add up. The reviewer could not run Swift, so the Swift points come from reading the code.

## Should fix, and what was decided

| # | Finding | Decision |
| --- | --- | --- |
| 1 | Today's server answers `400` (`DisallowUnknownFields`) to a `PATCH /v1/me` carrying `goal`/`sex`/`weight_kg`, which fails the whole request; `goal: null` and an absent `goal` decode the same. | The app sends the new fields only when the last `GET /v1/me` contained the key `goal` (a key-presence check in a custom decode). From PR 1 on, the server always sends the key. |
| 2 | "`sex` feeds the TRIMP constants" is wrong for `strain.trimp`, which always uses the male constants with a 0.25 HRR floor. The RPE TRIMP (r = RPE/10, no floor) is on a different scale. | Rated workouts count in the cardio load on `strain.trimp`'s scale (its constants and floor), and SCORING.md pins this in PR 2. `sex` feeds RYFT Age and the resting estimate, not the load. |
| 3 | "No usable heart rate" pointed at `energy.RatedWorkout` (resting HR ≥ 25 and `avg_hr`), while the day TRIMP sums samples with resting HR 60 as fallback, so a load could be counted twice. | Defined as scoring's rule: fewer than 2 heart-rate samples in the workout window and no `avg_hr`. |
| 4 | `chronic_7d` names a 28-day sum ÷ 4; the coaching load calls that `chronic_28d_weekly`. | Renamed to `chronic_28d_weekly`. |
| 5 | "A source that sent nothing else has `kinds: []`" is false: `SourcesOf` skips kind-less samples, and `user_sources.kind` is `NOT NULL` (0020). | A source that sends only `body_mass` is not listed in `sources`. |
| 6 | A `cursor` whose `stream` is a gated type would be stored and move forward. | Skipped like the pilot's `body_mass` cursor today. |
| 7 | Data loss depends on the operator turning the setting on before the app build ships. | New flag `flags["sync.fitness_types"]`: the app sends the new types only while it is `true`, and the server stores them only while it is on for the account **and** the consent is to a policy version that names them. |
| 8 | PR 1 would store sex and weight before the privacy update. | `PATCH` with `sex` or `weight_kg` needs consent to the policy version that names them, else `403 consent_required`. `goal` is not gated. |

## Nits, all taken

- `confidence` also drops when `energy_kcal.complete_for_logged_items` is false; `intake_kcal` is rounded to an integer.
- `vo2max_delta_7d` is `null` unless a reading lies in the last 7 days.
- `training_load` includes today so far.
- When HealthKit deletes the `body_mass` sample behind `weight_kg`, the next newest weigh-in takes over (manual or HealthKit), else `null`.
- The `samples_7d` row no longer says "the eight `Sample.type` values".

## Re-verification

2026-11-27, platform-lead, by hand (the brief allows one reviewer pass): `chronic_7d` and `chronic7D` are gone from API.md and
DECISIONS.md. `sync.fitness_types` is named in the Screens table, the sync section, the `GET /v1/config` example and note, and
the changelog. The changelog carries every should-fix row and nit. `make check-api-doc` passes. The Swift key `chronic28DWeekly`
follows Foundation's `capitalized` ("28d" → "28D"), like the existing `samples7D`. `make check-swift` compiled the mirror but
decoded nothing (no local stack), so the first served response is the real decode test.
