# Review: the sync status contract (D297)

code-reviewer, one pass on commit 993877d (2026-11-27). Verdict: needs changes. The resolution column is filled in
by platform-lead after verifying each item against the code.

| # | Level | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | blocker | "N2R and the briefing are computed on read from the stored scores" is false. `loadCoaching` reads `nutrition.daily_biometric_baselines` (written by `RefreshN2RNights` on the 15-min api tick) and the pilot's `nutrition.wearable_nights` / `canonical_workouts` (the coaching pilot's Garmin/Suunto webhooks, `nutrition.wearable_inbox`, drained by `processWearables` per tick). Neither touches `score_queue` or `provider_inbox`, so `current` can be up to 15+ min ahead of the briefing. | **Fixed.** `freshness` now includes the pilot: unfinished `wearable_inbox` jobs, and an N2R baseline older than its day's score (`n2r_due` without its 6-h term). Both count only while the tick processes the account. API.md says the pilot can stay pending for up to 15 min. |
| 2 | blocker | App rule 4 (up to date) has no `freshness` condition: first-match ordering shows green while scores are pending > 3 min with a sync < 30 min old. | **Fixed.** Rule 4 requires `freshness: current` and no sync of the app still waiting for its scores. |
| 3 | should-fix | `MarkScored`'s `queued_at <= started` compares `now()` (the transaction start time, not its commit) with the scoring instance's clock. A run that starts during `ApplyDirect`'s transaction can mark the day scored without seeing its records. So "exactly" and "atomically" overclaim. | **Accepted; documented, not fixed here.** API.md and D297 state the race and its recovery (the delivery's own run, then the push); the overclaims are gone. Follow-up F1 in the outline makes `MarkScored` exact. |
| 4 | should-fix | `scores_pending_since` resets on every re-queue or re-receipt (`queued_at = now()`, `received_at` = the latest), so it is not "the oldest arrival". An old stuck day nobody touches (dead letter, `ErrNoConsent` ack in `cmd/scoring/main.go`) keeps the button off green forever; nothing sweeps it. | **Fixed.** `freshness` counts today and yesterday only, so an old stuck day no longer holds the button. `scores_pending_since` is documented as restarting on a re-queue. The consent ack is an open edge in the outline. |
| 5 | should-fix | API.md says a provider is listed when `GET /v1/me/connections` lists it, but `connectAPI.list` does not filter connections by served/flag; the outline does. The two disagree. | **Fixed.** The rule is written out the same way in API.md, D297 and the outline: the connection is `connected` or `needs_reauth`, the provider is served and its flag is on. One shared helper, not the connections list. |
| 6 | should-fix | "capped at 500 rows" is for pending rows only; `provider_inbox_user` also spans processed rows kept 30 days. Suggest a partial index `(user_id, provider, received_at) WHERE state = 'pending'`. | **Fixed.** Ticket 1 adds the partial index; the cost section names it. |
| 7 | should-fix | `gateFor` returns the first gate that fails whatever its mode (beta `warn` + consent `on` → `invite_required`, so "enforced" is decided on the wrong gate). Evaluate each gate against its own mode. | **Fixed.** The outline evaluates each gate against its own mode, and D297 gives the beta `warn` + consent `on` example. |
| n1 | nit | Grant claim correct. Add a test that the since time equals `ApplyDirect`'s `queued_at`. | Fixed (test 4). |
| n2 | nit | Example 2: Garmin `last_sync_at` equals `latest.hrv` to the second, but `last_data_at` is storage time, not the sample time. | Fixed (05:03:02). |
| n3 | nit | Say what the silent push means (reload, then one poll ≥ 10 s later) and what happens after the 3-minute window. | Fixed. |

## Second pass (af27f9b)

| # | Level | Finding | Resolution |
| --- | --- | --- | --- |
| 1b | blocker (1 still open) | The N2R term fires almost always. Scoring's `refreshActive` rewrites today and yesterday every tick for users active in 48 h, and `computed_at = now()` moves even when nothing changes (`store.go` persist). The same scheduler job ticks scoring and the api together, so `processed_at < computed_at` recurs every 15 min and the button never turns green. | **Fixed by dropping the pilot terms.** `freshness` tracks scoring and connection deliveries only. API.md and D297 state that, for N2R accounts, the morning baseline and the briefing built on it, and the coaching pilot's partner deliveries, follow within one 15-minute pass and are not tracked. An exact signal needs an event-driven N2R refresh (open question for the user). |
| 8 | should-fix | The wearable term does not match `claim_wearable` (`attempts < 5`, provider configured); retry-only jobs give pending with a null since; `ryft_api` has no grant on `wearable_inbox`. | Moot: the term is dropped. |
| 9 | should-fix | The today/yesterday scope opens a hole. Late data for T−k queues only T−k, and scoring then rescores up to 30 days after it (`compute.go` `baselineDays`), today included, while `freshness` already says `current`. | **Fixed.** A pending day counts if it is today or yesterday, or within the last 30 days and queued in the last 10 minutes. The absent-field fallback reads "a day of the last 30 days". |
| 10 | contradiction | `n2r_pending ⊆ n2r_due` and "cannot wait forever" do not hold under the 25-row cap per tick. API.md calls freshness "data for today or yesterday", but wearable jobs have no day. | Moot: the pilot terms and `n2r_pending` are gone. |

Blocker 2 is closed.

Sound: polling vs the 10 s cache across instances; open/closed enums; the Swift mirror; the anchor; `sources` never `null`.

# The backend PR (`feat/sync-status`, D298)

## Hand-backs (2026-11-27)
- **database-engineer:** `0065_sync_status.sql` (+ `lock_timeout 5s`), `roles_test.go`, `migrate_syncstatus_test.go`
  (CHECK, NULL default, api reads `received_at` not `payload`/`record_id`, scoring writes `closed_reason`
  through RLS, EXPLAIN uses `provider_inbox_user_pending` with `enable_seqscan = off`, rerun). vet green;
  **every Postgres test skipped locally** (no `RYFT_TEST_POSTGRES_URL`): CI is the first real run. The index is not
  `CONCURRENTLY` (the migrator wraps each file in a transaction; the inbox is tiny).
- **backend-engineer:** model, store, handler, `cmd/scoring` consent closing (`queueCloser` seam,
  `closeNoConsent`), API.md typed examples + changelog row, D298, STATUS, ARCHITECTURE. vet/build/unit green,
  `check-api-doc` green (52→56 strict blocks); `check-swift` compiled the mirror, its live-decode step needs a
  running stack (not verified). Postgres tests skipped. Cost: `served` reads the flag once per listed provider,
  so 5 queries + 1 per listed provider (≤ 7 today); D298 says so. The section anchor became `…-d297-d298`; no
  link in `../app/docs` used the old one.
- **test-engineer:** pure tests (`syncstatus_freshness_test.go`, `closequeue_test.go` fake) pass; DB tests for
  cases 1–10 + consent in `cmd/api/syncstatus_db_test.go`, `internal/store/syncstatus_test.go`,
  `cmd/scoring/closequeue_test.go` skip locally. Flagged: code answered `error` for needs_reauth with a waiting
  delivery; API.md's table says `pending`. **platform-lead:** switched the precedence to pending (contract), test
  and D298 updated.

## code-reviewer, one pass on 39be67e

| # | Level | Finding | Resolution |
| --- | --- | --- | --- |
| 1 | should-fix | `CloseQueue` closes every unscored row with no cutoff: a re-consent + sync between scoring's `GetUser` and the `UPDATE` closes the fresh row (`current` too early, `MarkScored` then finds nothing). | **Fixed.** `CloseQueue(…, before)` with `queued_at <= before`, taken before the account is read in `handleEvent` and `refreshActive`; store + scoring tests; D298. Verified in the diff by platform-lead. Same clock caveat as `MarkScored` (F1). |
| 2 | nit | A day after today (tz change) holds the button 10 min; "the last 30" is 31 dates (today−30..today). | **Fixed.** `d.Day <= today`; D298 defines today−30..today (a rescore of today−30 reaches today); table row added. |
| 3 | nit | DB fixtures take today from `syncDay(0)` (UTC), the handler from its own clock: a run across 00:00 UTC fails. | **Fixed.** The rig skips within a minute of 00:00 UTC (accounts pinned to UTC). |
| 4 | nit | "without current consent" wording vs `Admits` (`Consent != nil`): an older policy's consent keeps scoring while `healthkit` shows `consent_required`. | **Fixed** (wording in API.md changelog and D298). |
| 5 | nit | API.md:4122 Swift mirror comment changed (comment only). | **Fixed.** Restored as on f579b33; the mirror is byte-identical to the app's contract. |
| 6 | nit | A failed flags read in `served` turns the route into a 500. | **Not changed:** consistent with every other read of the route. |

Sound: freshness boundaries, since null ⇔ current, gates per mode, listing, needs_reauth+delivery = pending, stale
`closed_reason` never read, no close under `warn`/`off`, idempotent close, migration (grants, index, RLS,
lock_timeout), fixtures vs 0042/0046 constraints, API.md shape unchanged from #258.
