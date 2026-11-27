# The sync status: implementation outline (D297)

The contract is `docs/API.md` → `GET /v1/me/sync` → **The sync button**. This PR is docs only. This file is
the plan for the PR that serves it. One PR, api only, plus one grant migration. No new table, route, event or
Cloud Run change.

## Tickets

| # | Ticket | Owner | Acceptance |
| --- | --- | --- | --- |
| 1 | Migration `00NN_sync_status_grant.sql` (the next free number on main): `GRANT SELECT (received_at) ON provider_inbox TO ryft_api` | database-engineer | `roles_test.go` pins the column grant; the api still cannot read `payload` |
| 2 | `model.SyncSource`, `SyncStatus.Sources/Freshness/ScoresPendingSince`; the schema in `testdata/schemas`; the four API.md examples become typed `json` blocks | api-contract-owner, backend-engineer | `make check-api-doc`, `make check-swift` green; the Swift mirror unchanged |
| 3 | The store query and the handler (below) | backend-engineer | the tests below |
| 4 | Tests | test-engineer | below |
| 5 | Review | code-reviewer; security-privacy-reviewer (the grant); sre (the route's cost, the cache) | `review.md` |

## Where "pending" comes from

- **Days:** `score_queue` rows with `scored_at IS NULL`. `PendingDays` already reads them. Extend it (same
  query, same index use) to also return `min(queued_at)`.
- **Provider deliveries:** `provider_inbox` rows of the user with `state = 'pending'`, per provider, and their
  `min(received_at)`.
- `freshness = scores_pending` ⇔ `len(pending_days) > 0` or a listed source is `pending`.
  `scores_pending_since` = the earlier of the two minima, `null` when `current`.
- Why it is complete: a sync queues its days in its apply transaction before the `200` (`ApplySync`); a
  delivery is stored, queued and marked processed in one transaction (`ApplyDirect`); `MarkScored` only
  clears a row queued before the run started. Readiness is in `daily_scores`; N2R and the briefing are
  computed on read. A future writer that changes stored scores (the D291 check-in, if it rescores) must queue
  through `queueDays` as well. Then this rule covers it with no change.

## The query (one more per uncached call)

```sql
SELECT c.provider, c.status, c.last_data_at,
       (SELECT min(i.received_at) FROM provider_inbox i
         WHERE i.user_id = c.user_id AND i.provider = c.provider AND i.state = 'pending') AS pending_since
FROM connections c
WHERE c.user_id = $1 AND c.status IN ('connected', 'needs_reauth')
ORDER BY c.provider
```

`connections` by its primary key (`user_id, provider`); the subquery by `provider_inbox_user` (`user_id`),
capped at 500 rows per user by the hook. Then filter in Go to the providers `GET /v1/me/connections` would
list: the same registry and flag evaluation (`connect.<provider>`), shared through one helper so the two
routes cannot disagree. A connection whose provider is not served, or whose flag is off for the account, is
dropped. That is what keeps Garmin and Suunto absent before phase 2.

`healthkit` comes from what `SyncStateOf` already reads (`devices.last_sync_at`) and from `gateFor`.
`status = error`, `reason = gate` only when the gate's mode is `on` (`h.beta.Gate` / `h.beta.Consent`); in
`warn` the status is `ok`.

**Cost:** today an uncached call runs 4 queries (the user row with its last device and today's score, the
kinds, the pending days, the global p95). After this it runs 5, the new one on two indexes, behind the
existing 10 s per-user cache. The p95 query (`ExpectedScoreSec`) is global and runs once per uncached user
call. That is unchanged, but it is the most expensive of the five: if sre sees it in the query stats, cache
it process-wide for 10 s, as a separate change.

**The cache stays at 10 s.** An answer cached before a sync could say `current` right after it. The contract
handles this: the app treats its own sync as pending and polls no sooner than `max(expected_within_sec, 10)`
s. Ingest is another service and cannot invalidate the api's cache. Do not shorten the cache for this.

## Tests (`cmd/api`, Postgres integration, as `syncstatus` tests today)

1. No data: `sources` = `[healthkit]` with `last_sync_at: null`, `status: ok`; `freshness: current`;
   `scores_pending_since: null`.
2. A sync queued a day: `scores_pending`, `scores_pending_since` = its `queued_at`. After `MarkScored`:
   `current`. A re-queue after the run started stays pending (`queued_at > started`).
3. A connection `connected` with a pending inbox row: that source `pending`, `freshness: scores_pending`, the
   since time = `received_at`. After `ApplyDirect`: the source `ok` with the new `last_data_at`, the day
   pending, then `current` after scoring.
4. `needs_reauth`: `error`, `reason: needs_reauth`. `revoked`: absent. A `failed` inbox row: no source change.
5. The flag `connect.garmin` off for the account, or the provider not served: absent although a row exists.
6. Gate `on` without consent: `healthkit` `error`/`consent_required`; gate `warn`: `ok` (and `gate` set, as
   today).
7. Invariant over the fixtures: `freshness == scores_pending` ⇔ `len(pending_days) > 0 || any source pending`.
8. Contract: the new fields are always present (`sources` never `null`), and the doc examples decode.
9. The api role reads `provider_inbox.received_at` and still cannot read `payload` (`roles_test.go`).

## Open edges to check in the implementation

- **A sync whose apply step fails** (`503`): the records are stored, the days are not queued, and the app
  sends the same request again. Check that the resend queues those days although every record is now a
  `duplicate` (that `days_touched` covers duplicates). If it does not, the status says `current` for data no
  score has. That is an existing gap, not new with this route, but the button makes it visible.
- **Days that scoring never clears** (a day dead-lettered, or an account whose consent was withdrawn, if
  scoring skips it): the button shows stale after 3 minutes, by design. Check that `score_queue` rows of a
  user scoring skips by policy are cleared or not created, so such a user does not sit on "scores delayed".
- **Rollout:** no flag. The fields are additive, and old apps ignore them. The app decodes them as optional.

## Rollback

Revert the api commit: the fields go absent again, and the app falls back to `pending_days`. The grant can
stay (a column read, no data path).
