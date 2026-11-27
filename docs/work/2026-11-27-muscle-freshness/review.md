# Review: muscle freshness (C1 PR 6, D309)

One code-reviewer pass on the working tree before the first commit (2026-11-27). No blockers.

## Should fix

1. **Request cost** (`internal/store/freshness.go`). `session_sets` has no index on `completed_at`, so the 14-day query
   reads the account's whole set history, like the all-time one: two history scans plus `HasCompletedSession`, three
   round trips on every `GET /v1/days/{day}`. D309 → Known limits understated it. Fix: one statement.
   **Resolution:** `MuscleHistory` reads the working sets once (a CTE used twice), returns the 14-day rows, the latest
   time per muscle and the completed-session check in one round trip; D309 corrected.
2. **Cardio counts as full hard sets** (`internal/scoring/freshness.go`). Cardio exercises carry muscle tags (rowing,
   jump rope, sled), so ten jump-rope rounds left calves at 40, like ten heavy calf raises; Strength weights cardio at
   0.3. **Resolution:** a set of kind `cardio` counts `× 0.3` (`DefaultLoadFactor(cardio)`) in the fatigue; it is still
   a working set for `sets_7d` and `last_trained_at` (the contract's definition). Pinned (ten rounds: 76), D309 and
   SCORING.md say so.
3. **The "today" path is untested** (`cmd/api/days.go`). It read `time.Now()` directly. **Resolution:** the handler
   reads a clock (`h.now`), and `TestDayMuscleFreshness` covers today with a set 1 s before and 1 s after now.

## Nits

4. `docs/STATUS.md` named the drafting tool. **Resolution:** removed.
5. `freshnessScale = 10 / (math.Ln10 - 2*math.Ln2)` is correct but obscure. **Resolution:** `10 / math.Log(2.5)`.

## Checked and sound (reviewer)

Contract fidelity (null vs 18 entries, order, evaluation time, thresholds, `sets_7d`, `last_trained_at`, keys);
tenant isolation on every joined table and exercise visibility; the microsecond bound helper; `unnest`/`max` with
empty arrays; every pinned number recomputed; the Postgres fixtures exist with the shapes used; no new logging, no
new table.
