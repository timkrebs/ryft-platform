# The sync status: implementation outline (D297)

The contract is `docs/API.md` → `GET /v1/me/sync` → **The sync button**. This PR is docs only. This file is
the plan for the PR that serves it. One PR, api only, plus one migration (a grant and an index). No new table,
route, event or Cloud Run change. The review of the contract is in `review.md` next to this file.

## Tickets

| # | Ticket | Owner | Acceptance |
| --- | --- | --- | --- |
| 1 | Migration `00NN_sync_status.sql` (the next free number on main; 0061 is taken, the no-watch work claims 0062+): `GRANT SELECT (received_at) ON provider_inbox TO ryft_api`; `CREATE INDEX provider_inbox_user_pending ON provider_inbox (user_id, provider, received_at) WHERE state = 'pending'` | database-engineer | `roles_test.go` pins the column grant, and the api still cannot read `payload`; `EXPLAIN` of the query below uses the partial index |
| 2 | `model.SyncSource`, `SyncStatus.Sources/Freshness/ScoresPendingSince`; the schema in `testdata/schemas`; the four API.md examples become typed `json` blocks | api-contract-owner, backend-engineer | `make check-api-doc`, `make check-swift` green; the Swift mirror unchanged |
| 3 | The store reads and the handler (below); one helper for "provider served and flag on", shared by `GET /v1/me/connections`'s `available` and this route | backend-engineer | the tests below |
| 4 | Tests | test-engineer | below |
| 5 | Review | code-reviewer; security-privacy-reviewer (the grant); sre (the route's cost, the cache) | `review.md` |
| F1 | Follow-up, not this PR: make `MarkScored` exact (clear only when `queued_at` equals the value the run read at its start, not `queued_at <= started`) | scoring-scientist with backend-engineer | a test with a delivery whose transaction spans a run's start: the day stays pending |

## Where "pending" comes from (today and yesterday, local)

Let `D` = {today, yesterday} in the account's timezone.

1. **Days:** `score_queue` rows with `scored_at IS NULL` and `day ∈ D`. Extend `PendingDays` to return
   `queued_at` too, in the same query. `pending_days` itself stays all days, as today.
2. **Deliveries:** `provider_inbox` rows of the user with `state = 'pending'`, per provider, and their
   `min(received_at)`. Only for providers that are listed (below). A pending row of an unlisted provider
   cannot exist for long: disconnect deletes them.
3. **The coaching pilot**, only when the account has an N2R profile, the nutrition flag is on for it (the
   tick's `enabled` func), and it consented to the policy in force (the tick's `coachingConsent`). These are
   the conditions under which the tick will process the work, so pending cannot wait forever on a pass that
   skips the account:
   - `nutrition.wearable_inbox` jobs of the user's `wearable_links` with `completed_at IS NULL`. Since time:
     `min(due_at)` over jobs with `attempts = 0` (`due_at` defaults to the receipt time, and a retry moves it);
     a job in retry counts as pending but adds no since time.
   - N2R: a day `d ∈ D` with a `daily_scores` row that has N2R inputs (as in `n2r_due`), `sleep_end` passed,
     and no `daily_biometric_baselines` row, or one with `processed_at < greatest(d.computed_at,
     p.updated_at)`. Since time: `d.computed_at`. This is `n2r_due`'s predicate for one user, without its
     6-hour refresh term. Write it as a SQL function next to `n2r_due` (`nutrition.n2r_pending(user, at)`,
     `SECURITY DEFINER`, granted to `ryft_api`) so the two cannot drift; a test asserts that every row
     `n2r_pending` returns is one `n2r_due` returns for the same `at`.

`freshness = scores_pending` ⇔ any of 1–3. `scores_pending_since` = the earliest since time among them.
`null` when `current`.

## Sources

- `healthkit`: `last_sync_at` from `SyncStateOf` (`devices.last_sync_at`). `error` with `reason`:
  `invite_required` when `h.beta.Gate == on` and the account has no beta access; else `consent_required`
  when `h.beta.Consent == on` and it has not consented. Evaluate each gate against its own mode. Do not reuse
  `gateFor`, which returns the first failing gate whatever its mode. Otherwise `ok`.
- A provider: rows of `connections` with `status IN ('connected', 'needs_reauth')`, kept only when the provider
  is served (the connect registry) and `connect.<provider>` is on for the account (the same flag evaluation as
  `available`). `GET /v1/me/connections`'s own `connections` list is not filtered that way, so do not reuse it.

```sql
SELECT c.provider, c.status, c.last_data_at,
       (SELECT min(i.received_at) FROM provider_inbox i
         WHERE i.user_id = c.user_id AND i.provider = c.provider AND i.state = 'pending') AS pending_since
FROM connections c
WHERE c.user_id = $1 AND c.status IN ('connected', 'needs_reauth')
ORDER BY c.provider
```

`connections` by its primary key; the subquery by the new partial index. `provider_inbox_user` would also scan
the processed rows kept 30 days as dedupe keys.

## Cost per uncached call

Today: 4 queries (the user row with its last device and today's score, the kinds, the pending days, the global
p95). After: 5 for every account (the connections query; `PendingDays` only gains a column), 6 in the pilot
(`n2r_pending` plus the wearable jobs, one statement). All on primary keys or user-leading indexes, behind the
existing 10 s per-user cache. `ExpectedScoreSec` (global) remains the most expensive and is unchanged. If sre
sees it in the query stats, cache it process-wide for 10 s, as a separate change.

**The cache stays at 10 s.** An answer cached before a sync could say `current` right after it. The contract
handles this: the app treats its own sync as pending and trusts only answers from ≥ 10 s after the sync. The
api's cache timestamp is taken before the reads, so every instance's pre-sync entry has expired by then.
Ingest cannot invalidate the api's cache. Do not shorten it for this.

## Tests (`cmd/api` with Postgres, as the `syncstatus` tests today)

1. No data: `sources` = `[healthkit]`, `last_sync_at: null`, `ok`; `current`; since `null`.
2. A sync queued today: `scores_pending`, since = its `queued_at`. After `MarkScored`: `current`. A re-queue
   after the run started stays pending.
3. A queued day 10 days back only: `pending_days` holds it, `freshness` is `current`.
4. A `connected` connection with a pending inbox row: that source `pending`, `scores_pending`, since =
   `received_at`. After `ApplyDirect`: the source `ok` with the new `last_data_at`, the day pending (since =
   `ApplyDirect`'s `queued_at`), then `current` after scoring.
5. `needs_reauth`: `error`/`needs_reauth`. `revoked`: absent. A `failed` inbox row: no change.
6. The flag `connect.garmin` off for the account, or the provider not served: absent although a row exists.
7. Gates: beta `on` without access: `invite_required`; consent `on` without consent: `consent_required`;
   beta `warn` + consent `on` without consent: `consent_required`; both `warn`: `ok` (and `gate` set, as
   today).
8. Pilot: an N2R-profile account whose today was scored after its baseline: `scores_pending`, since =
   `computed_at`; after `RefreshN2RNights`: `current`. The same with the nutrition flag off: `current`. An
   uncompleted wearable job: pending; completed: `current`. `n2r_pending` ⊆ `n2r_due`.
9. Invariant over the fixtures: `freshness == scores_pending` ⇔ (1 ∨ 2 ∨ 3).
10. Contract: `sources` never `null`; the doc examples decode.

## Open edges to check in the implementation

- **A sync whose apply step fails** (`503`): the records are stored, the days are not queued, and the app
  sends the same request again. Check that the resend queues those days although every record is now a
  `duplicate` (that `days_touched` covers duplicates). If not, the status says `current` for data no score
  has. That is an existing gap, but the button makes it visible.
- **Consent withdrawn:** scoring acks `ErrNoConsent` without clearing the queue row (`cmd/scoring`). With
  today and yesterday only, such a row stops holding the button after two days, and the `healthkit` source
  shows `consent_required` while consent is enforced. Decide whether scoring should clear the row instead.
- **Rollout:** no flag. The fields are additive, and old apps ignore them.

## Rollback

Revert the api commit: the fields go absent again, and the app falls back to `pending_days`. The migration
can stay (a column read and an index, no data path).
