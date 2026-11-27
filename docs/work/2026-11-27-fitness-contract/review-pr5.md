# Review: PR 5 (`feat/fitness-tiles`, D311)

One code-reviewer pass, 2026-11-27, on the uncommitted diff over `feat/sync-fitness-types`. No blockers.

## Should-fix (all applied)

1. **No plausibility bounds on VO₂ max and heart-rate recovery.** D307 says the readers (PRs 4 and 5) filter
   implausible values. A source posting `vo2max` 0 or a 900 bpm recovery would show and feed the 7-day change for
   30 days. **Applied:** VO₂ max is accepted from 10 to 100 ml/kg/min and recovery from 1 to 120 bpm. Both are
   pure constants, pinned in tests and SCORING.md.
2. **The model changelog announced recovery and VO₂ max.** These are on-read readings, not model outputs, and
   `hrr_bpm` is `null` for everyone until the privacy update. **Applied:** the 2026.11.2 entry covers active
   time only.
3. **VO₂ max mixed sources in the 7-day change.** **Applied:** the earlier value comes from the latest
   reading's source. The tile shows the newest reading from any source (D311).

## Nits

- 0071's inline `CHECK` scans under the lock. Fine at today's size (the comment says so). Not changed.
- The change is computed from the raw readings, so the two rounded values can differ from the shown change by
  0.1. Documented in D311 and SCORING.md.
- STATUS.md did not give the rescore command. **Applied.**
- `days.go` reads VO₂ max from −37 days where −36 is enough. Harmless, not changed.

## Checked and sound (reviewer)

The windows and the DST handling are correct. Every query is scoped to the user, and the test plants another
user's readings. The PR 3 storage guard is not bypassed. `active_min` is not a history input. The Strain
refactor keeps the same behaviour. No values are logged. The placeholder is `null`. The gaps at migrations 0068
and 0070 are safe. The goldens add only `active_min` and the model version, and a spot-check against the zones
and workouts holds. CI's `TestGoldenScores` against Postgres is still the real check.
