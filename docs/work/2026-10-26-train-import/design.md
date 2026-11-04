# TI-1: `POST /v1/train/import` — implementation spec

The contract is `docs/API.md` → Train → "`POST /v1/train/import`" and "Training import: answers to
T1–T12" (changelog 2026-10-26); the decisions are D124–D130. This file is what the implementers
build from; where it and the contract differ, the contract wins and this file is wrong. Nothing here
changes a v1 field: every addition is optional or a new endpoint.

Today's code, checked for this spec (`backend/…`): `cmd/api/train.go` (`putSession`, `completeSession`,
`settle`, `rescoreDays`, `recordsFor`), `internal/train` (`CheckSession`, `CheckPlan`, `Merge`),
`internal/store/train.go` (`SaveSession`, `writeSession`, `checkReferences`, `SavePlan`, `Enroll`,
`SaveCustomExercise`), `cmd/scoring/main.go` (`handle`, `scoreEvent`), `internal/notify/service.go`
(`session`, `refresh`), `internal/store/syncstate.go` (`QueueDaysWith`, `PendingDays`),
`cmd/ingest/main.go` (`admitted`, the sync's event).

## 1. Work split

| Who | Does |
| --- | --- |
| **database-engineer** | Migration (§2), the lease, the per-item unit of work (§5), grants, the dependent-days query, lock behaviour of the `ALTER`s. |
| **backend (api)** | `model` types, `train` pure rules (§3), the handler (§4), enroll body (§10), `PRsSince` filter, validation changes (§9). |
| **events / notify / scoring** | `reason: "import"` through scoring's refresh branch, notify's origin guard (§6). |
| **scoring-scientist** | The open points in §12. None blocks the contract. |

## 2. Migration (next free number, one file, additive; rollback = re-pin the old images)

```sql
-- sessions: where a session came from (D125).
ALTER TABLE sessions
    ADD COLUMN IF NOT EXISTS origin text NOT NULL DEFAULT 'app',
    ADD COLUMN IF NOT EXISTS import_source text;
ALTER TABLE sessions ADD CONSTRAINT sessions_origin_check
    CHECK (origin IN ('app', 'import')) NOT VALID;          -- VALIDATE in the same file: all rows are 'app'
ALTER TABLE sessions ADD CONSTRAINT sessions_import_source_check
    CHECK ((origin = 'import') = (import_source IS NOT NULL)
           AND (import_source IS NULL OR import_source ~ '^[a-z][a-z0-9_]{0,39}$')) NOT VALID;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_origin_check;
ALTER TABLE sessions VALIDATE CONSTRAINT sessions_import_source_check;

-- sets: the support of an assisted bodyweight set (D127).
ALTER TABLE session_sets ADD COLUMN IF NOT EXISTS assist_kg double precision
    CHECK (assist_kg IS NULL OR assist_kg BETWEEN 0 AND 200);

-- one import per user at a time (D129): a lease with an expiry, so a pod that dies releases it.
ALTER TABLE users ADD COLUMN IF NOT EXISTS import_lease_until timestamptz;

-- the api queues the days of an import (D128): sync's ingest did, the api never had to.
GRANT INSERT, UPDATE ON score_queue TO ryft_api;
-- notify's push guard reads the origin (its grant on sessions is per column, 0015).
GRANT SELECT (origin) ON sessions TO ryft_notify;

INSERT INTO feature_flags (key, enabled, rollout_pct) VALUES ('train.import', true, 0)
ON CONFLICT (key) DO NOTHING;
```

- No new table, no new index: lookups are by `(user_id, id)` (sessions PK), `sessions_user_started`,
  `session_exercises_exercise (user_id, exercise_id)`. No `origin` index: nothing lists by it.
- RLS, the Supabase policies, the `ryft_scoring` grants (table-level SELECT; its column UPDATE grants
  stay) need no change. `account deletion` needs none: the columns live on rows it already deletes.
- Respect D116's 5 s `lock_timeout`; the `NOT VALID` + `VALIDATE` split keeps the table lock short.
- Update `cmd/api/config_test.go`'s expected flags (`train.import: false`) and API.md's flag table is
  already written.

## 3. Pure rules (`internal/train`, with tests; no I/O)

| Function | Rule |
| --- | --- |
| `CheckImportSession(*model.Session, now) error` | `CheckSession` plus: `status == completed`; `ended_at` set and ≥ `started_at`; `started_at` in `[2000-01-01, now+24h]`; every set has `completed_at`; a set without `updated_at` gets its `completed_at` (the existing default is `now`, which would make every resend differ). Returns `train.Problem` (→ `invalid`). |
| `CheckAssist(ex model.Exercise, set)` | `assist_kg` is only for `load_type == bodyweight` and not with `weight_kg > 0` (`Problem`). Called from `store.checkReferences` (live `PUT`) and the import. `ClampSet` clamps `assist_kg` to 0–200 beside the others. |
| `SameSession(stored, body model.Session) bool` | The T10 comparison. Both sides go through the same normalisation first: `ClampSet`, `weight_kg`/`assist_kg` nil → 0, times `.UTC().Truncate(time.Microsecond)`, exercises and sets keyed by id (array order irrelevant), `import_source` included, server fields (`muscular_trimp`, `strain`, `volume_kg`, `workout_id`, `prs`, `updated_at`, `created_at`, `origin`) excluded. |
| `SamePlan(stored, body model.Plan) bool` | Every `Plan` field except `slug`, `is_builtin`, `PlanExercise.exercise`; days/exercises by id; `progression == nil` ≡ `{type: none}`; times none. |
| `ImportConflict(stored, body model.Session) bool` | `train.Merge`'s rule for the import: true when a set is on both sides and the stored `updated_at` is after the body's, or the stored status is not `completed`. |
| `CheckPlan` | Add: `bodyweight` target → `target_value` nil or 0–500 (T11). `kg` etc. unchanged. Applies to `PUT /v1/plans/{id}` as well; note it in the PR. |
| `CheckEnroll(plan, EnrollPost, now)` | §10. |
| `ImportDays(sessions, loc, today) []string` | Local days of `started_at`, deduplicated, sorted, only those in the 400 days ending `today` (`scoring.MaxRangeDays`; `addDays(today, -399)` … `today`, as `RescoreBehind`). |
| `ExpectedSeconds(base, days int) int` | `min(300, base + 2*days)`; never below `base`. |

Typed outcomes: give `train.Problem` an optional `Code` (`invalid` by default) so the store can return
`unknown_exercise`, `unknown_plan_reference`, `id_taken` without string matching; `failTrain` keeps
mapping a plain `Problem` to `422` for the other routes.

## 4. The handler (`cmd/api`, new `train_import.go`)

Route: `POST /v1/train/import`, registered on its own chain like `DELETE /v1/me` so it gets the
50 s budget and not `readTimeout`: `auth.Middleware(authOpts, betaGate(beta.Gate, httpx.Timeout(importTimeout, httpx.WithRoutes(imp))))`.
`httpx.MaxBodyImport = 1 << 20`. `ratelimit` class: the default (nothing to change; one token).

Request types in `model`: `TrainImportRequest{Source string; Plans []Plan; Sessions []ImportSession}`,
where `ImportSession` is `Session` **without `origin` and `import_source`** (a `ToSession()` copies it).
`httpx.ReadJSON` already disallows unknown fields, so those two make a `400`. A test compares the JSON
field names of `ImportSession` and `Session` by reflection so a later field cannot drift.

Steps, in this order (the early ones touch nothing):

1. Decode (`400`/`413`/`422` NUL as `ReadJSON` does).
2. Consent: `403 consent_required` under `CONSENT_ENFORCE`, `X-RYFT-Gate` under `warn`. Factor ingest's
   `admitted` (`user.Consent == nil || user.Consent.PolicyVersion != beta.PrivacyPolicyVersion`) into a
   shared function (`internal/beta` or `httpx`); both services call it. The invite gate already ran.
3. Request checks, whole-request `422`: `source` in the known set, `len(plans) ≤ 5`, `len(sessions) ≤ 50`,
   no repeated plan id, no repeated session id (messages without values).
4. Empty request → `200 {"results":[],"days_touched":[],"scoring":{…}}` before the lease.
5. Lease (`store.AcquireImportLease`, §8). Not got → `429`, `Retry-After: 10`, `code: import_in_progress`.
   `defer release`.
6. Plans in order: `train.CheckPlan`, then `store.ImportPlan`.
7. Sessions sorted by `(started_at, id)`: `train.CheckImportSession`, then `store.ImportSession`.
   A database error that is not a `Problem`/typed outcome (`pgconn` connection, timeout, deadlock after the
   store's one retry) aborts the loop: finish (step 8) for what is stored, then `503` with `Retry-After`
   and the error shape (no `results`). Items already stored stay (D124).
8. Finish, once, on `context.WithoutCancel(ctx)` with its own 10 s: see §6/§7.
9. Build `results` in request order (plans, then sessions), `days_touched`, `scoring`.
   Log one line (`slog`): `source`, counts per status, `days`, `duration_ms`, request id; no title, note,
   value or session id list (D114/D117). Metrics: `ryft_train_imports_total{result}` (`ok`, `busy`,
   `timeout`, `error`), `ryft_train_import_items_total{kind,status}`,
   `ryft_train_import_seconds`; add them to `docs/OBSERVABILITY.md` in the same PR.

`message` strings for `rejected` contain ids and field names only.

## 5. Store functions and the unit of work (`internal/store/train_import.go`)

```go
type ImportOutcome struct {
    Status    string      // created | unchanged | updated | rejected
    Code      string      // rejected only
    Message   string
    Day       string      // local day of started_at (sessions), "" for plans
    Exercises []uuid.UUID // exercise ids of the stored version and of the replaced one, for the records
}
func (s *Store) ImportPlan(ctx, userID uuid.UUID, p model.Plan) (ImportOutcome, error)
func (s *Store) ImportSession(ctx, userID uuid.UUID, source string, in model.Session, loc *time.Location, window DayWindow) (ImportOutcome, error)
func (s *Store) AcquireImportLease(ctx, userID uuid.UUID, ttl time.Duration) (release func(context.Context), ok bool, err error)
func (s *Store) PendingAmong(ctx, userID uuid.UUID, days []string) ([]string, error)          // score_queue.scored_at IS NULL
func (s *Store) DependentDays(ctx, userID uuid.UUID, since time.Time, ex []uuid.UUID, w DayWindow, loc *time.Location) ([]string, error)
func (s *Store) FinishImport(ctx, userID uuid.UUID, days []string, ev events.Message) error // QueueDaysWith + outbox.Insert
```

**`ImportSession`, one transaction per item** (a savepoint per item inside one request transaction is the
same semantics and the engineer's call; the rules are: an item is stored completely or not at all, a
failed item leaves no trace, and the items' commits are independent):

1. `lockSession` (the advisory lock `PUT` uses, so a live save of the same id waits).
2. `loadSession(... lock=true)`. Stored with `origin != 'import'` → `rejected` `id_taken`.
3. `checkReferences` as `PUT`'s, **plus** plan references that point into the request: because plans were
   committed first they are simply visible; no special case. Map the three failures to
   `unknown_exercise`, `unknown_plan_reference`, `id_taken` (an exercise or set id of another of the user's
   sessions: the existing `writeSession` row-count check).
4. Stored and `ImportConflict` → `rejected` `newer_on_server`.
5. Stored and `SameSession` → `unchanged`: **no write, no queue row**.
6. `writeSession(tx, user, body, replace = stored != nil)` extended with `origin`/`import_source` for the
   insert (not in `DO UPDATE SET`: the `PUT` path must never change them; the import's update path sets
   them in a separate `UPDATE … WHERE origin = 'import'`) and `assist_kg` in the set insert/update.
   Status `completed`, `ended_at` as sent. No `CompleteSessionWith`, no load: scoring owns
   `muscular_trimp`, `strain`, `volume_kg`, `workout_id` (leave them as they are on update, `null` on create).
7. If the session's day is in the window: `INSERT INTO score_queue … ON CONFLICT DO UPDATE SET queued_at = now(), scored_at = NULL`
   (the statement `QueueDaysWith` runs) in the same transaction. This is what makes an interrupted request
   resumable (§7).
8. Return the outcome with the exercise ids (new and, on update, the replaced version's).

`ImportPlan`: `SavePlan` semantics (replace whole; `ErrForbidden`/`ErrTaken` → `id_taken`; missing exercise →
`unknown_exercise`), preceded by `SamePlan` against `Plan(ctx, user, id)` under the same row lock
(`owner(... FOR UPDATE)`) → `unchanged` without a write.

Performance: a 1 MiB body holds a few thousand sets. Use the existing `pgx.Batch` in `writeSession`; the
reads in 2–5 are per item. If the 50 s is ever tight, the first thing to cut is the per-item re-read of
`checkReferences` (one `visibleExercises` for the whole request).

## 6. Events, scoring, notify (D125)

- **No `session.completed`, ever, from the import.** The event is
  `events.Ingested(model.IngestedEvent{UserID, Days, Reason: "import", RequestID}, "")`. `IngestedEvent`
  has no session id, so `cmd/scoring/main.go` publishes no `session.scored`: no `SESSION_SCORED`, no `PR`.
  `topology.go` already lets the api publish `ingested`; **no topology change** (`go run ./cmd/rabbitmq-topology`
  output stays identical; check with git diff).
- **Outbox (D101):** the event is written by `FinishImport` in the same transaction as the `score_queue`
  rows of the final day list, `outbox.Insert(ctx, tx, "api", m)`, then `h.outbox.Kick()`. One event per
  request, none when `days` is empty. `scoreEvent` treats `"import"` like `"sync"`: `ComputeDays(days)`
  (explicit list; not the two-element range form, which is for `recompute`/`merge` only — the list may have
  two days, so the range branch must stay keyed on the reason, as it is).
- **Refresh push:** in `cmd/scoring/main.go` extend `ev.Reason == "sync"` to `"sync" || "import"`
  (`day` = `slices.Max(days)`). notify's throttle (one per 5 min per user) stays.
- **notify guard (defence in depth):** `store.SessionNotice` also reads `origin`; `Service.session` counts
  and drops `origin == "import"` (`count(model.NotifySessionScored, "import")`). A re-`complete` of an
  imported session is the only way an id could reach it.
- **Idempotent consumers (D100):** the scoring handler is unchanged and already idempotent (a duplicate
  finds nothing changed; a redelivery rescores the asked days). Test it with the new reason.
- **Records:** `PRsSince` (the insight cards, `GET /v1/insights`) joins `sessions` and skips records whose
  session has `origin = 'import'`. `SessionNotice.PRs`/`Session.prs`/`pr_count` are unchanged: a record is
  a record in every list; only the cards and pushes are silent. (Open point 2 changes this filter.)

## 7. Rescoring (D128)

After the last item (`Finish`):

1. `exercises` = union of every outcome's `Exercises` for items that are not `rejected` (also
   `unchanged`: cheap repair after an interrupted attempt). `h.recordsFor(ctx, user, exercises)` once.
   If the request was large and this is slow, replace the per-exercise `LoggedSets` with one query.
2. `days` = `ImportDays(created ∪ updated sessions)`
   ∪ `PendingAmong(ImportDays(unchanged sessions))` (an earlier attempt queued them and scoring has not
   finished; a finished day is not queued again)
   ∪ `DependentDays(since = oldest created/updated started_at, exercises = those sessions' exercise ids)`:
   the user's other completed sessions with `started_at > since` that have one of those exercises, as local
   days in the window (their load is measured against the e1RM as of their end). Sorted, deduplicated.
3. `h.st.FinishImport(ctx, user, days, event)`; `h.cache.forget(user)`.
4. `scoring.queued_days = days_touched = days`; `expected_within_sec = train.ExpectedSeconds(h.expectedSec(ctx), len(days))`
   (the p95 helper is ingest's today: move it to `store` or call `ExpectedScoreSec` with the same 1 min cache).

Sessions outside the window (older than 400 days) are stored and count for history and records, but are
never queued: their `muscular_trimp`/`strain`/`volume_kg`/`workout_id` stay `null`; the response does not
list their days.

## 8. Rate limiting and concurrency (D129)

- One token from the default class for the whole request; nothing to add to `ratelimit.UserLimits`.
- **One import per user across pods:** `AcquireImportLease`:
  `UPDATE users SET import_lease_until = now() + $2 WHERE id = $1 AND (import_lease_until IS NULL OR import_lease_until < now()) RETURNING 1`;
  no row → busy. TTL 120 s (50 s of work + finishing + slack). `release` sets it `NULL`
  (`context.WithoutCancel`). A pod that dies leaves a lease that expires. It works through a transaction
  pooler, which a session-level advisory lock would not. Alternative the engineer may prefer: a
  `train_import_leases` table; the behaviour (`429`, `Retry-After: 10`, nothing processed) is the contract.
- Response: `429`, `Retry-After: 10`, `{"error":"an import is already running","code":"import_in_progress","request_id":"…"}`;
  add `import_in_progress` to `model.Code…` and to the closed-enum comments.

## 9. Validation changes to existing routes (all in API.md)

- `Set.assist_kg` on `PUT /v1/sessions/{id}`: clamp 0–200; `422` for a non-bodyweight exercise or with
  `weight_kg > 0`. `model.Set` gains `AssistKG *float64 `json:"assist_kg"``; `setColumns`/`setFields`/
  `writeSession` carry it; every reader of sets (`LoggedSets`, `ExerciseSessions`, `LastWorkingSets`,
  `fillSessions`, `PlanExerciseAttempts`) gets it for free through `setColumns`. **No scoring function reads
  it** (D127): `EffectiveWeight`, `SetLoad`, `PersonalRecords`, `E1RM` stay as they are, and a test pins that
  a set with `assist_kg` scores exactly like the same set without it.
- `PUT /v1/sessions/{id}` ignores `origin`/`import_source` of the body (they are on `model.Session` now, so
  `ReadJSON` accepts them): `writeSession` never writes them on this path; a test sends `origin: "import"`
  and reads `app` back, and sends `origin: "app"` for an imported id and reads `import` back.
- `GET /v1/sessions`, `GET /v1/sessions/{id}`: `origin`, `import_source` (`sessionColumns`, `scanSession`,
  `SessionSummaries`). Fixtures and the Swift mirror already carry them.
- `train.CheckPlan`: `bodyweight` target `target_value` 0–500 when present (T11).
- `POST /v1/exercises` with an archived custom exercise's id un-archives it (T12): no code change, a test.

## 10. Enroll (T8)

`POST /v1/plans/{id}/enroll` reads an optional body: `EnrollPost{StartedAt *time.Time; CurrentWeek,
CurrentDayIndex *int}` through `ReadJSON` tolerating `io.EOF`, as `completeSession` does (no body, an
empty body and `{}` are today's behaviour exactly). With any field set (`train.CheckEnroll`):

- `started_at` in `[2000-01-01, now+24h]`; `current_week` in `1..plan.weeks` (`1..1000` when `weeks` is
  null); `current_day_index` in `0..days_per_week-1`; `CurrentPlanDay(plan, candidate)` must resolve
  (`422` otherwise); unknown field `400`.
- `store.Enroll(ctx, user, planID, *EnrollPos)`: same lock (`lockEnrollments`), same pause of another active
  enrolment. An active enrolment of the plan is updated in place; else a paused one of the plan is resumed
  *and* updated (most recent first, as today); else a new row. Omitted fields: new → `now()`/1/0, existing →
  unchanged. `201` only when a row was created.
- Importing does **not** call `AdvanceEnrollment` (`completeSession` is its only caller).

## 11. Deletion and export

- `DELETE /v1/sessions/{id}` works on imported sessions as today (`DeleteSession`, `recordsFor`,
  `rescoreDays`); sending it again creates it again.
- Account deletion: no change (the columns and the `score_queue` rows are on tables `DeleteUser` empties;
  `import_lease_until` is on `users`). Add the new columns to `delete_test.go`'s fixture so the test sees
  an imported session, an `assist_kg` set and a `score_queue` row go.
- Export is the app's, built from `GET /v1/sessions*` (180-day chunks): `origin`, `import_source` and
  `assist_kg` are in those shapes. Nothing server-side to build.
- Privacy text: no new processor or recipient; mention of imports from other apps is the user's call
  (a change to the text moves `PRIVACY_POLICY_VERSION` in the same PR).

## 12. Tests

Pure (`internal/train`): `SameSession` — identical, reordered arrays, `Z` vs `+00:00` vs fractional zeros,
`weight_kg` nil vs 0 vs absent, `assist_kg` nil vs 0, out-of-range values that clamp to the same, one set
changed (`reps`, `completed_at`, `updated_at`), `import_source` changed, exercise `position` changed;
`SamePlan` (progression nil vs none, day order); `CheckImportSession` (every refusal, the `updated_at`
default); `CheckAssist`; `ImportConflict`; `ImportDays` around the 400-day edge and DST; `CheckEnroll`;
`CheckPlan` bodyweight target; the `ImportSession`/`Session` field-name reflection test.

Store (with a database): create → unchanged → updated → unchanged; the id of an app-origin session
(`id_taken`); a set edited later (`newer_on_server`) and a discarded imported session; a repeated set id in
another session; a plan day shared with another plan; `unchanged` writes nothing and queues nothing
(compare `xmin`/`score_queue.queued_at`); a failed item leaves no rows; the lease (second acquire fails,
release, expiry); `PendingAmong`; `DependentDays`; `FinishImport` writes the queue rows and exactly one
outbox row (`ingested`, `reason import`, no `session_id`), atomically (fail the insert → no queue rows);
`PRsSince` skips imported sessions; `SessionNotice.Origin`; `assist_kg` round trip and CHECK;
`origin`/`import_source` CHECKs.

Handler (`cmd/api`, in the style of `main_test.go`): the contract example verbatim (statuses, `results`
order = request order, `days_touched`, `scoring`); partial success (one unknown exercise among three);
plans before sessions (a session names a plan of the same request) and a rejected plan →
`unknown_plan_reference`; sessions processed oldest first whatever the array order (records and `prs`
as if in order); idempotent resend → all `unchanged`, no outbox row, `days_touched: []`; changed content →
`updated` and the old sets are gone; an interrupted request (stop the loop after one item) then the full
resend: the pending day is in the answer and the event; `origin`/`import_source` in the request → `400`;
`PUT` ignores them (§9); `403 invite_required`, `403 consent_required`, and `X-RYFT-Gate` in `warn`;
`413` at 1 MiB + 1; `422` for 6 plans, 51 sessions, an unknown `source`, a repeated id, a NUL; `200
results: []` for an empty request; the concurrent second request → `429` + `Retry-After: 10` +
`import_in_progress`, and the first still completes; a set with `completed_at: null` → `invalid`;
`assist_kg` on a weight exercise → `invalid`; sessions older than 400 days stored, not in `days_touched`,
no queue row; the enrolment of the active plan unchanged after importing a session with its `plan_day_id`;
`last_time`/`target_kg` and the exercise history see imported sessions; log lines hold no title/note/value
(extend the D117 log test); metrics counted; request budget 50 s (route not under `readTimeout`).

Events / scoring / notify: scoring handles `ingested` with `reason import` (`ComputeDays` the list, marks
`score_queue`, publishes `refresh` once with the newest day, **no** `session.scored`), idempotent on a
duplicate and a redelivery (D100); notify drops a `SessionNotice` with `origin import` (`result="import"`)
and still sends for an app session; the topology JSON is unchanged.

Other: enroll with no body / `{}` / each field / out of range / no day at that position / paused plan /
other active plan paused first; T12 (archive a custom exercise, `POST` its id again, `archived: false`);
T3 (a bodyweight set with `weight_kg` null and 0: same e1RM chart, same `PersonalRecords`, same
`SameSession`); a set with `assist_kg` scores exactly like one without; `config_test` flags;
`delete_test` (§11); `scripts/check_api.py` and `make smoke`: one import of two sessions and a plan, then
the same again (`unchanged`), `GET /v1/me/sync` pending then empty, `make check-swift`.

## 13. Open points

**scoring-scientist** (`docs/SCORING.md`; none changes the contract's shapes):

1. *Assisted sets.* The effective load for `assist_kg > 0`: proposal `max(0, bodyweight_kg ×
   bodyweight_factor − assist_kg)`; then volume, muscular load, whether such a set may give an e1RM
   point or a `max_weight`/`max_volume` record (today a bodyweight set without an added load gives no e1RM
   point; `max_reps` stays). It moves stored numbers: model version, `changelog.json`, a rescore, and an
   API.md changelog line. Until then `assist_kg` changes nothing (D127) and imports of assisted sets count
   as unassisted.
2. *The newest imported record as an info card.* Today imported records are silent everywhere (cards,
   pushes). Decide whether the newest one may surface once as a `pr` card (and its `PRsSince` filter in §6).
3. *Days that hold only an imported session.* Their daily row has strain from the session and no sleep or
   recovery; confirm that is right for the readiness/state of those days, and whether years of imported
   strain should feed the strain baselines and the 7-day load of days that do have data (the walk rescoring
   "days after a change" runs from the oldest imported day, up to the cap `maxAgain`).
4. *E1RM history as of a session's end.* An import adds sets before existing sessions, which changes their
   load. The spec queues those days (`DependentDays`, §7). Confirm that is the right fix and not "stored
   load is final once scored".
5. *Assumed durations.* The app assumes 60 minutes when the file has no duration. `strength_min` and the
   load use the session's duration; say whether an assumed one matters, in which case the contract needs a
   flag (`ended_at` quality) — not added now.

**database-engineer:** per-item transaction vs savepoints (§5); the lease (users column vs table, §8);
the `ALTER`s under D116's `lock_timeout`; the dependent-days query plan on a user with years of sessions;
the write cost of 50 sessions of up to 50×50 sets (most files are a few hundred sets); `score_queue`
grants for `ryft_api` and the column grant for `ryft_notify` on Supabase.

**app (for the lead):** leave `origin`/`import_source` out of the import request (nil optionals); decode
them with `app` as the default; send custom exercises with `POST /v1/exercises` before the import; strip
U+0000 from titles and notes (a NUL anywhere is a `422` for the whole request, Errors → NUL); on `429`
`import_in_progress` wait `Retry-After` and resend unchanged; on `413` halve; treat `rejected` +
`newer_on_server` as "already there, edited since"; the contract has `exercise` as a reserved result kind.
