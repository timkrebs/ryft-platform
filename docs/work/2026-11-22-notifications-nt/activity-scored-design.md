# `ACTIVITY_SCORED`: the sender (NT1), and `ryft.account` (2026-11-22)

Branch `push/activity-scored` from `origin/main`. The spec is `docs/API.md` → Push notifications (the
`ACTIVITY_SCORED` row, **the rules**, payload, `ryft` keys, collapse/expiry, the name table, the keys),
`NotificationPrefs`, changelog 2026-11-22. PR 1 (`push/device-token-follows-account`) holds 0039, 0040, D251,
D252; this PR starts at **0041** and **D253**.

## Problem and constraints
Alert a fresh, scored, visible Apple Health workout once, at most 3 a day, never for RYFT's own or backfilled
data. One operator, beta scale, Cloud Run + Pub/Sub, GDPR Art. 9: no health values in events or logs.
Today the data can't answer two of the rules: `source_bundle_id` is **input only** (`model.Workout`, not
stored), and workouts don't record the device that uploaded them. Both get a column.

## Options for the trigger
| | Option | For | Against |
|---|---|---|---|
| A | **The existing `refresh` event** (scoring → notify after a sync's or an import's days are scored). Notify scans the account's undecided workouts that ended in the last 12 h | No new key, topic, push route, DLQ or Terraform. The event already means "a sync's days are scored". The scan sees the state after duplicates and attach, whatever order the events arrive in | Decides after a sync's scoring only, not after the timer pass or a recompute |
| B | New key `workouts.scored` with the sync's workout ids | Names the workouts | New topic and subscription (the user's `terraform apply`), a new route. Ingest has to carry the ids through `ingested` (500 ids on a backfill). Group and primary changes still need a scan |
| C | Extend `RefreshEvent` with ids | No topology change | Same id plumbing as B, and it adds nothing the scan doesn't already know |

**Choice: A.** `RefreshEvent` stays `{user_id, day, request_id}`, so the events still carry ids only and no
values. `announce` in `cmd/scoring/main.go` and the topology stay as they are: **no `pubsub-topology` run,
no Terraform.** The scan's start bound (`start_at >= now − 7 d`) uses `workouts_user_start`.

```mermaid
sequenceDiagram
  participant App
  participant Ingest
  participant Scoring
  participant Notify
  participant APNs
  App->>Ingest: POST /v1/sync (device_id, workouts + source_bundle_id)
  Ingest->>Ingest: TouchDevice; UpsertWorkouts sets device_id, source_bundle_id on INSERT
  Ingest-->>Scoring: outbox → ingested{days}
  Scoring->>Scoring: per day: duplicates → sessions attach → strain (compute_steps.go order)
  Scoring-->>Notify: outbox → refresh{user, day}
  Notify->>APNs: silent refresh (throttled 5 min, + ryft.account)
  Notify->>Notify: ActivityCandidates(now) → ActivityRule (pure)
  loop each candidate
    Notify->>Notify: DecideWorkoutAlert tx (lock 7419008, ledger, cap)
    Notify->>APNs: ACTIVITY_SCORED (when outcome = sending)
    Notify->>Notify: FinishWorkoutAlert sent|failed, or Release on APNs down
  end
```

## Where each rule is checked
Everything is decided in **notify**. Scoring supplies `strain`, `duplicate_of` and `session_id`. Ingest records
two facts. The rules are the pure `notify.ActivityRule(c ActivityCandidate, now time.Time) Verdict` (new
`internal/notify/activity.go`). The verdict is `Skip` (nothing recorded, so a later scan looks again),
`Backfill` (decided silently) or `Eligible`.

| # | Rule | Data | Verdict on fail |
|---|---|---|---|
| 1a | `source <> 'ryft-session'`, `session_id IS NULL` | workouts | Skip (the attach may change) |
| 1b | bundle: `lower(b) == "dev.timkrebs.ryft"` or has the prefix `"dev.timkrebs.ryft."`. NULL passes, and `dev.timkrebs.ryftx` passes | `workouts.source_bundle_id` (new) | Skip |
| 1c | only sync writes non-session workouts (the import writes none; `PUT /v1/activities` doesn't exist) | — | — |
| 2 | `strain IS NOT NULL` (0 counts); visible (SQL: the `ListWorkoutsBetween` hidden expression) | workouts | Skip |
| 3 | `end ≤ now`, `now − end ≤ 12 h` (SQL `end_at >= now − 12h`; the pure function re-checks) | workouts | Skip. Past the window the scan never sees it again: **decided implicitly, no row** |
| 4 | `end − start ≥ 600 s` | workouts | Skip |
| 5 | backfill: `w.created_at − d.created_at < 1 h`, joined on `devices (user_id, w.device_id)`. NULL `device_id` (rows from before 0041) or no device row passes | `workouts.device_id` (new), `workouts.created_at` (the insert, which the upsert never updates), `devices.created_at` | **Backfill**: row `backfill` |
| 6 | not decided: the scan has `NOT EXISTS` on the ledger by id. Inheritance runs inside the decide transaction (below) | `workout_alerts` | row `inherited` (or `duplicate` on a race) |
| 7 | `sending` + `sent` rows for `(user_id, day)` < 3, `day` = `start` in `r.Loc` | `workout_alerts` | row `capped` |
| 8 | `Allowed(prefs, ACTIVITY_SCORED)`, then `QuietUntil(prefs, now.In(loc))` | users | rows `muted`, `quiet`. Also `no_token` when there are no tokens |

The order inside the transaction is: duplicate, then inherited, then `pre` (backfill, then muted, quiet,
no_token, which the caller computes), then capped, then `sending`. Muted and quiet rows don't count toward the
cap, so the order only changes the label on the row.

## The ledger and inheritance
New table `workout_alerts`. There is one row per decided workout. The row keeps the copy's identity so that
later copies can inherit its mark without a foreign key to `workouts`.

| Column | Type | Notes |
|---|---|---|
| `user_id` | uuid NOT NULL REFERENCES users(id) | PK part |
| `workout_id` | uuid NOT NULL | PK part. **No FK**: the mark outlives a folded or deleted copy |
| `source`, `activity` | text NOT NULL | for the same-source match |
| `start_at`, `end_at` | timestamptz NOT NULL | same |
| `day` | date NOT NULL | the local day of `start` at decision (`ryft.day`), the cap's day |
| `outcome` | text NOT NULL CHECK IN (`sending`,`sent`,`failed`,`capped`,`muted`,`quiet`,`no_token`,`backfill`,`inherited`) | |
| `devices` | int NOT NULL DEFAULT 0 | tokens APNs accepted |
| `decided_at` | timestamptz NOT NULL DEFAULT now() | |

The primary key is `(user_id, workout_id)`. No other index: pruning keeps each user's ledger to a few rows.

**Inheritance is a pure function**, `notify.Inherited(c ActivityCandidate, ledger []store.AlertMark) bool`.
It is true when a ledger row has the candidate's id or a member's id, or when
`workoutdup.SameWorkout(row, x)` holds for the candidate or any member. `SameWorkout` is the fold's own rule:
same source and activity, both ends within 120 s, ≥ 50 % overlap. It returns false on equal ids, so the id
match is a separate check. Members are `workouts WHERE duplicate_of = c.id` (`id, source, activity, start_at,
end_at`).
- **The fold or a Garmin rewrite:** the new id matches the old row by the fold rule. That holds whether the
  old copy was folded, deleted through `deleted` before the fold (D146, where a fold-time transfer would miss
  it), or is still stored. **Ingest never touches the ledger.**
- **Duplicate group:** a primary is decided when its id or a member's id or fold-match is in the ledger. A
  primary that changes (W sent, then G becomes primary with W hidden) inherits.
- **Merge** moves the rows (below), so the `to` account doesn't alert `from`'s moved workouts again.

**Decided once, atomic, capped.** This is `store.DecideWorkoutAlert(ctx, userID, mark AlertMark, decide
func(ledger []AlertMark, sentOnDay int) string) (string, error)`, one transaction:
1. `SELECT pg_advisory_xact_lock(7419008, hashtext($1::text))`. The new class `lockAlerts` is added to
   `locks.go`'s table. Only notify takes it, so there's no ordering with `lockApply`.
2. Prune: `DELETE FROM workout_alerts WHERE user_id=$1 AND end_at < now() - interval '2 days'`. A row whose
   end is more than 2 days old can't match a candidate whose end is within 12 h + 120 s (data minimisation).
3. Read the user's rows. `sentOnDay := count(*) WHERE day=$mark.day AND outcome IN ('sending','sent')`.
4. `outcome := decide(ledger, sentOnDay)`. This is notify's pure `ActivityOutcome(c, ledger, sentOnDay, pre,
   3)`. On `duplicate` it returns without writing. Otherwise `INSERT … ON CONFLICT DO NOTHING`. If 0 rows are
   inserted (impossible under the lock, kept defensive), it returns `duplicate`.

Then `sending` → `sendAll`. If `accepted == 0 && lastErr != nil`, `ReleaseWorkoutAlert` deletes the row
(`WHERE outcome='sending'`) and the error is returned, so Pub/Sub redelivers, as `deliver` does. Otherwise
`FinishWorkoutAlert(outcome sent|failed, devices)`. Redelivering the refresh is a no-op for decided workouts,
because the scan excludes them and the claim returns `duplicate` (D100). Because of the lock the cap holds
across instances. The APNs call runs outside the transaction.

**Grants (0041):**
- `ryft_notify`: `SELECT, INSERT, DELETE` and `UPDATE (outcome, devices)` on `workout_alerts`.
  `SELECT (user_id, id, activity, start_at, end_at, source, source_bundle_id, device_id, strain, session_id,
  duplicate_of, created_at) ON workouts` (no HR, kcal or distance). `SELECT (id, created_at) ON devices` (it
  has `user_id, last_sync_at`). A new policy `workouts_notify_read FOR SELECT TO ryft_notify USING (true)`
  (0015's loop left out workouts). `devices_notify_read` exists.
- `ryft_api`: `SELECT, DELETE, UPDATE (user_id)` on `workout_alerts` (deletion, merge).
- `ryft_ingest`, `ryft_scoring`: nothing new. Their table-level `INSERT, UPDATE` on workouts (0009) covers the
  new columns, and scoring's column-level INSERT leaves them NULL.
- Policy `workout_alerts_services TO ryft_api, ryft_notify USING (true) WITH CHECK (true)`. On Supabase: RLS
  on, `REVOKE ALL … FROM anon, authenticated, service_role` (as 0015).

**Deletion:** `DeleteUser` gains `DELETE FROM workout_alerts WHERE user_id=$1` next to `notifications_sent`.
`owned()` in `delete_test.go` gains the table. **Merge:** `m.move("workout_alerts", "NOT EXISTS (SELECT 1
FROM workout_alerts t WHERE t.user_id = $2 AND t.workout_id = s.workout_id)", "same workout id: to's kept")`.
**Export:** the table is operational, like `notifications_sent`, and is not in the app's export.

## Ingest
`UpsertWorkouts(ctx, userID, deviceID uuid.UUID, in)`. `uuid.Nil` stores NULL, and the 19 test call sites pass
`uuid.Nil`. The INSERT adds `device_id, source_bundle_id`. The `DO UPDATE SET` adds only
`source_bundle_id = COALESCE(workouts.source_bundle_id, EXCLUDED.source_bundle_id)`, and leaves it out of the
`IS DISTINCT FROM` guard (so duplicate counting doesn't change). `device_id` is insert-only, so the device
that first stored the row is the one that counts. `cmd/ingest/main.go:229` passes `req.DeviceID`.
`TouchDevice` already runs first, so the device row exists. `ingested` is unchanged.

## Notify
- `Service.refresh` is restructured. `APNs == nil` stays as `unconfigured`, and nothing is decided. It loads
  `St.Recipient`: `ErrNotFound` → `Permanent`. If there are tokens and the user isn't throttled, it sends the
  silent push. Then **always**, also when throttled or when there are no tokens, it runs
  `s.activities(ctx, &r, now)` and returns its first error, after trying every candidate.
- `activities`: `St.ActivityCandidates(ctx, r.ID, now)`, then `ActivityRule` for each. `pre` is `backfill`,
  or else muted / quiet / `no_token`, or empty. Then `DecideWorkoutAlert`, then send. It counts
  `count(ACTIVITY_SCORED, outcome)`, with `duplicate` for a race.
- `Activity(day, id string, activity string, strain float64, start, end time.Time, lang string) Message`
  (messages.go):
  - title: `ACTIVITY_SCORED_TITLE` with `activityNames[activity][lang]`, the 20 rows of the API.md table.
    For `other` or an activity not in the table: `ACTIVITY_SCORED_TITLE_UNTITLED` with no args.
  - body: `ACTIVITY_SCORED_BODY` [`decimal(strain)`, `duration(end−start, lang)`].
  - `duration`: whole minutes rounded down. `56 min` / `56 Min.`, `1 h 12 min` / `1 Std. 12 Min.`,
    `2 h` / `2 Std.`, with U+00A0 before each unit.
  - URL: `site+"/activity/"+id`. `Expires: end + 12h`.
  - collapse id: `"ACTIVITY_SCORED-"+id` (lower case, 52 bytes).
  - `apns.PushAlert`, so priority 10.
  - `Catalog` gains the 3 keys.
- **`ryft.account`:** `Message.Payload(account uuid.UUID)` and `SilentPayload(day string, account uuid.UUID)`
  write `"account": account.String()` (lower case). The signature change makes every call site pass `r.ID`:
  the morning refresh and alert, session, PR, stale, refresh, activity. `Message` gets no new field.
- **Metrics:** `ryft_push_notifications_total{category="ACTIVITY_SCORED", result}` has the results `sent`,
  `failed`, `capped` (new), `backfill` (new), `inherited` (new), `muted`, `quiet`, `no_token`, `duplicate`.
  `stale` stays under REFRESH for a deleted user. Skips aren't counted: they recur on every scan. Update the
  metric's Help string and OBSERVABILITY.
- **Logs:** the existing `push` line (`category`, `day`, `devices`, `of`). No workout id, activity, strain,
  duration or source. The ledger row is the support trail. `apns refused` stays as it is (`device_id`).

## Model, API, mirror
- `model.NotifyActivityScored = "ACTIVITY_SCORED"`, appended to `NotifyCategories` (display order = the API
  order), so `prefsOf` lists it and PATCH accepts it.
- `cmd/api/push.go:73`: `"unknown category "+c+" ("+strings.Join(model.NotifyCategories, ", ")+")"`.
- `docs/API.md` Swift mirror (for the contract owner or the lead to apply in this PR):
  `case …, activityScored = "ACTIVITY_SCORED"` in `NotificationCategory`, replacing the comment's "joins with
  the sender's PR" lines. Then `make check-swift`.

## Migration 0041 `0041_activity_alerts.sql` (additive)
`SET LOCAL lock_timeout = '5s'`.
`ALTER TABLE workouts ADD COLUMN IF NOT EXISTS device_id uuid, ADD COLUMN IF NOT EXISTS source_bundle_id text`.
There's no CHECK because ingest's `validate` bounds the length at 255, and a CHECK would scan under ACCESS
EXCLUSIVE. The table, grants and policies are as above, with `IF NOT EXISTS` and `DROP POLICY IF EXISTS`, so
a rerun is a no-op.
**Rollback** = re-pin the images. The columns and the table stay, the old ingest writes NULL `device_id`
(which passes rule 5), and the old notify ignores the ledger. No down-migration.

## Failure modes
| Failure | Seen in | Recovery |
|---|---|---|
| DB error during scan or decide | `ryft_events_handled_total{reason="refresh",result="retry"}`, ERROR lines/h; DLQ policy 4 after 20 | Pub/Sub redelivers. Decided rows are skipped, and the silent push is throttled |
| APNs unreachable | `result="failed"`, `ryft_apns_responses_total{status="error"}` | release + nack, then retry |
| Crash between claim and send | a `sending` row older than 1 min (runbook query) | lost alert (at most once). It still counts toward the cap |
| A sync whose scoring fails | no refresh | the next sync's refresh rescans the 12 h window (the alert comes late) |
| Events out of order, or two devices | — | ledger inheritance (group + fold rule) |

## Files touched
- `backend/internal/db/migrations/0041_activity_alerts.sql`, `internal/db/migrate_activity_test.go`,
  `internal/db/roles_test.go`.
- `internal/store/store.go`: `UpsertWorkouts`, `DeleteUser`.
- `internal/store/merge.go`, `internal/store/locks.go`.
- New `internal/store/alerts.go`: `ActivityCandidates`, `DecideWorkoutAlert`, `FinishWorkoutAlert`,
  `ReleaseWorkoutAlert`, and the types `AlertMark` and `ActivityCandidate`.
- `cmd/ingest/main.go`.
- `internal/notify/{activity.go,messages.go,service.go}` and the fakes in its tests.
- `internal/model/push.go`, `internal/metrics/metrics.go`, `cmd/api/push.go`.
- Docs: `docs/{ARCHITECTURE,OBSERVABILITY,OPERATIONS,DECISIONS,STATUS}.md`, and the API.md mirror line.

## Tests
- **Pure (`internal/notify/activity_test.go`):**
  - `TestActivityRule`, a table with one row per rule:
    - `ryft-session` skips, and so does a `session_id`.
    - Bundles `dev.timkrebs.ryft` and `DEV.TIMKREBS.RYFT.watchkitapp` skip. `dev.timkrebs.ryftx` passes,
      and so does a nil bundle.
    - Strain nil skips. Strain `0` is eligible.
    - `end = now + 1s` skips, `end = now` is eligible. `now − end = 12h` is eligible, `12h + 1s` skips.
    - 599 s skips, 600 s is eligible.
    - Stored 59m59s after the device was first seen is backfill. 1h passes, and so does a nil device.
  - `TestActivityInherited`: the id, a member's id, the fold rule at ±120 s (121 s and another activity
    don't match), a member matched by the fold rule.
  - `TestActivityOutcome`: the order. The 4th is capped, and muted/quiet don't count toward the cap.
  - `TestActivityMessage`:
    - API.md's example payload byte for byte, en and de, raw U+00A0.
    - `other` and unknown activities give the untitled title. All 20 activities have en and de names.
    - Durations 56, 60, 72, 120 min in both languages.
    - The collapse id is 52 bytes. Expiry is `end + 12h`.
  - `TestCatalog` gains the 3 keys.
  - `TestEveryPayloadCarriesAccount` covers all five builders and the silent push.
- **Service (fake store, `notify_test.go`):**
  - The same refresh handled twice gives one send per device (redelivery).
  - 5 eligible on one day give 3 sent and 2 capped. The next day sends.
  - Quiet hours give `quiet`, and nothing is sent after they end. With the category off: `muted`.
  - No token gives `no_token`, and registering later doesn't alert.
  - Backfill is silent, and a same-source rewrite under a new id from a known device inherits.
  - W sent, then G becomes primary with W as a member: `inherited`.
  - APNs down: released, an error returned, and the retry sends.
  - APNs nil: nothing decided.
  - A throttled refresh still decides.
  - `logs_test`: the activity path logs no strain, duration, activity or workout id.
- **Store (Postgres):**
  - `ActivityCandidates`:
    - hidden rows are left out, but a duplicate whose primary was deleted is visible;
    - members are listed;
    - the 12 h window;
    - decided rows are excluded;
    - the device join and the bundle.
  - `DecideWorkoutAlert`: 6 goroutines on one day give exactly 3 `sending`. Idempotent: the second call gives
    `duplicate`. Prunes rows ended more than 2 days ago.
  - `UpsertWorkouts`: the first device is kept when a second sends the row, and the bundle is filled by
    COALESCE.
  - `TestDeleteUser` (`owned`), the merge test (rows moved, to's kept on the same id), roles (notify can't
    read `avg_hr` and can't update workouts; ingest can't touch the ledger).
- **api:** PATCH accepts `ACTIVITY_SCORED`, `GET /v1/me` lists it, the 422 text lists all five.
- **Gates:** `go vet`, `go test -race`, `make smoke`, `make check-swift`, `make cloudrun-check`.

## D-entries
- **D253:** `ACTIVITY_SCORED` is decided in notify on the existing `refresh` event, by a window scan. No new
  key or topic, and no Terraform.
- **D254:** The `workout_alerts` ledger. "Decided once" is a row, and inheritance uses the fold rule and the
  duplicate group, not a fold-time transfer (deletions run before the fold). The cap is held under the
  per-user lock 7419008. Rows are pruned 2 days after their end.
- **D255:** Workouts record `device_id` (the first uploader) and `source_bundle_id`. Backfill means stored
  < 1 h after the device row's `created_at`. Rows from before 0041 pass.
- **D256:** `ryft.account` on every payload, through `Payload(account)`. The Swift mirror's
  `NotificationCategory.activityScored` joins, and the 422 text is built from `NotifyCategories`.

## Size
One PR, one backend-engineer, about 3–4 h: one migration, about 4 store methods, a pure rule file, the
message, and a mechanical payload signature change. **If the PR runs short, the fallback cut** is
`ryft.account` alone first (D256 minus the category: additive, and the app treats a missing one as before),
then the sender with the category. The category must never ship without the sender, because `GET /v1/me`
lists it.

**Privacy:** there's no new processor. The ledger keeps a copy of activity and times for ≤ 2 days past the
end, and workouts gain a device id. Tell the user the privacy text should mention activity notifications if it
lists the push categories.
