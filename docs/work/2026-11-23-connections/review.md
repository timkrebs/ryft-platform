# Reviews: direct connections, phase 1

## Design review (security-privacy-reviewer, 2026-11-23)

| # | Sev | Section | Finding | Fix | State |
|---|---|---|---|---|---|
| B1 | blocker | §6.2, §4.2, §5.1 | A reconnect sets the live row `pending` and overwrites its tokens; an unconfirmed reconnect then deregisters a working link; the unique index on `status <> 'revoked'` covers `pending`, so a relink fails at callback | pending grant in its own table/columns; index only `connected`/`needs_reauth`; swap at confirm; a different provider user id = disconnect of the old one | resolved in design (amended 2026-11-23) |
| B2 | blocker | §6.2, §6.3 | attacker's own `authorize_url` sent to a connected victim expires → `Deregister` can end the victim's live grant → §8 deletes the victim's data | before any `Deregister` on expiry/cap/permission, if a live connection holds (provider, provider user id), delete tokens only; test | resolved in design (amended 2026-11-23) |
| S1 | should | §5.1, §7.2 | inbox unique on (provider, kind, record_id) across users with `DO UPDATE`: an id collision writes B's data into A's row | `user_id` in the key, or `DO UPDATE … WHERE user_id = EXCLUDED.user_id` | resolved in design (amended 2026-11-23) |
| S2 | should | §6.4 | `deleteDirectData` omits the direct `user_sources` row §8 promises to delete | add it | resolved in design (amended 2026-11-23) |
| S3 | should | §4.4, §5.1 | repeated confirm "answers 200" but `confirm_hash` is NULL after success | keep the hash until `confirm_expires_at` | resolved in design (amended 2026-11-23) |
| S4 | should | §6.2, logs | LB logs keep preview matches with the full URL 14 days (`infra/gcp/logging.tf:23`); Cloud Run 5xx entries keep the URL; `code` may trip sqli | scope-exclude the callback now, or a logging exclusion for `/v1/connect/` | resolved: design §6.2, §7.5; infra logging.tf + edge.tf |
| S5 | should | §7.2 | weak verification → forged pushes inject data; a forged deregistration deletes it | per-user cap on pending inbox rows; phase 2: a cryptographic push signature is a precondition | resolved in design (amended 2026-11-23) |
| S6 | should | §5.1 | `revoked` rows keep `provider_user_id`/`permissions` 90 days | null them | resolved in design (amended 2026-11-23) |
| S7 | should | §5.5 | Art. 15: export lacks `provider_user_id`, `permissions` | include them | resolved in design (amended 2026-11-23) |

§8 (delete on disconnect) judged sound (Art. 17(1)(b)); disclose backup retention and the 30-day deregistration queue.
Consent Q1: the reviewer reads consent as purpose-bound, not version-bound, and suggests `CONNECT_MIN_POLICY_VERSION`;
left to the user (design §17 Q1). The brief's "drop without consent in force" stays the default.

## PR #234 review (code-reviewer, 2026-11-23): no blocker

| # | Sev | Finding | Resolution |
|---|---|---|---|
| 1 | should | disconnect blocked by App Attest / invite gate | exempt from both (API.md, design §4.5) |
| 2 | should | the cap counts the caller's own reservation | own open states and pending grant never count |
| 3 | should | flag scope: confirm gated or not | gated; confirm while off → 404, pending grant handled like `confirm_expired` (B2 applies) |
| 4 | should | callback logging exclusion only with the edge built | unconditional (`infra/gcp/logging.tf`) |
| 5 | should | this file showed blockers open | rows marked resolved |
| nits | — | "3 open" scope, `deregistered` in two cases, a two-body example, `ConnectCallback` path parsing, `rmssd` ≠ direct, reserve `unknown` | fixed in API.md / design |

## PR 2 `connect/foundation` reviews (2026-11-23)

The first code review's findings were lost when the disk filled mid-run (the reviewer could not recall them); a
fresh code review runs on the tree. Security and SRE: no blocker.

| # | From | Sev | Finding | Fix | Owner | State |
|---|---|---|---|---|---|---|
| P2-1 | security | should | `infra/gcp/logging.tf:25` exclusion is a substring match: `?x=/v1/connect/` on any probe drops its Cloud Armor denial and 5xx entries | anchor: `httpRequest.requestUrl=~"^https://[^/]+/v1/connect/[a-z0-9]+/callback(\?|$)"` + test | infra-engineer | fixed (80/80 terraform test) |
| P2-2 | security + SRE | should | `cmd/ingest/connect.go:124` reads up to 4 MiB of an unverified body under the 60 s request limit; ingest runs concurrency 4 × 2 instances, so 8 slow unsigned pushes starve `/v1/sync` (no exposure while `CONNECT_PROVIDERS` is empty: 404 before the read) | per-route in-flight cap on the hook (excess → 503 + `Retry-After`) and a short read deadline | backend-engineer | fixed (round 1) |
| P2-3 | SRE | should | a big push can delay every later sync's scoring by up to 15 min | find the queueing point; keep a push's refresh from blocking sync refreshes | backend-engineer | fixed (round 1) |
| P2-4 | SRE | should | api tick: the connection steps share one time budget; a slow provider skips the daily notices and can lose a refreshed token | per-step deadlines, notices not behind provider calls, persist a refreshed grant on a context detached from the step deadline | backend-engineer | fixed (round 1) |
| P2-5 | security | nit | `edge.tf:57` `!request.path.startsWith('/connect/')` skips the WAF on every host and method | scope to `^/connect/[a-z0-9]+/push$` on the ingest host | infra-engineer | fixed (80/80 terraform test) |
| P2-6 | security | nit | `cmd/api/connections.go:711`, `store/connections.go:634`: the B2 live-grant check runs outside the cap lock; ignores another account's pending grant for the same provider user | re-check under the lock; count the other pending grant | backend-engineer | fixed (round 1) |
| P2-7 | security | nit | `connect/registry.go:63-72` `PushSide()` returns the whole Provider (a type assertion reaches OAuth) | a wrapper exposing only the push methods | backend-engineer | fixed (round 1) |
| P2-8 | security | nit | `0042:251` ingest may set `provider_user_id` to any value | trigger: ingest may only null it | database-engineer | fixed (trigger, tested on PG14) |
| P2-9 | security | nit | `scripts/check_cloudrun.py:249` the provider list is not stripped before the fake check (`"garmin, fake"` passes) | strip items + test | gitops-engineer | fixed (script test green) |
| P2-10 | security | nit | PKCE `code_challenge_method=S256` not pinned by the interface | pin it in `internal/connect` + test (phase 2 adapter inherits) | backend-engineer | fixed (round 1) |
| P2-11 | lead | should | compose api had no `DATA_ENCRYPTION_KEY`: config refuses a served provider without it, so the smoke could not start | dev key (32 zero bytes) in `docker-compose.yml` | lead | fixed |
| P2-12 | code | should | `cmd/ingest/connect.go:274` reads the payload unlocked; `lockInbox` (`store/direct.go:277`) checks only `state='pending'`: a re-push (revision 2) during processing is marked processed with revision 1 applied, and the provider's re-send is then a duplicate (a provisional night stays provisional) | `Revision` on `InboxItem`; `lockInbox … AND revision=$n` → `DirectNotPending`; covers the batched `Also` rows; race test | backend-engineer | fixed (round 2) |
| P2-13 | code | should | `deregistered` when another RYFT account holds the provider account live: API.md:1238 says `false`, `settled(depSkipped)` (`cmd/api/connections.go:684`) and design §4.5/§4.6 say `true` | **lead: API.md wins** (the contract, more honest: the grant is not ended); code + test → `false`, design amended by the lead | backend-engineer, lead | fixed (round 2) |
| P2-14 | code | should | relinking confirm locks the other account's `connections` row, then its apply lock (`store/connections.go:376-380`); `ApplyDirect`/`ApplyDeregistration`/`ApplyPermissions`/`Disconnect` take the apply lock first → 40P01 | find the other account unlocked, take its apply lock, re-select FOR UPDATE | backend-engineer | fixed (round 2) |
| P2-15 | code | should | callback: `PutPendingCapped` fails with `ErrConnectNoAccount`/a DB error after the exchange (`cmd/api/connections.go:473-479`): the grant lives on at the provider | `QueueDeregistration` as the cap branch does | backend-engineer | fixed (round 2) |
| P2-16 | code | nit | `StoreInbox` resolves owners outside its tx (`inbox.go:69`); `lockInbox` doesn't check the provider user id: a push in flight during a replacing confirm is applied to the replaced account | resolve owners in the tx with `FOR SHARE` on `connections`; check the link's provider user id at apply | backend-engineer | fixed (round 2) |
| P2-17 | code | nit | `boundedItem` (`cmd/ingest/connect.go:248`) passes JSON jsonb rejects (lone `\ud800`): insert fails → 503 → the provider retries forever | savepoint per item → `bad_item` (or reject the escapes) | backend-engineer | fixed (round 2) |
| P2-18 | code | nit | `Disconnect` (`store/connections.go:514`) runs `deleteDirectData` with nothing to end (a full sample scan under the apply lock, on a gate-exempt route) | skip without connection or pending grant | backend-engineer | fixed (round 2) |
| P2-19 | code | nit | `SaveRefresh` updates 0 rows silently when a disconnect ran mid-refresh: the rotated tokens are lost, the old ones deregister as `gone` | check rows affected; queue the new tokens | backend-engineer | fixed (round 2) |
| P2-20 | code | nit | repeat confirm after a relink away answers 200 with a `revoked` connection (`store/connections.go:334`) | `409 connect_ticket_invalid` (API.md: a 409 on a repeat → read the list) | backend-engineer | fixed (round 2) |
| P2-21 | code | nit | a processed deregistration notice keeps its row and digest 30 days; relink + revoke within that window is a duplicate and ignored | clear the provider user's inbox rows on a confirm after `revoked` | backend-engineer | fixed (round 2) |
| P2-22 | code | nit | dead `_ = skipped` in `cmd/api/connections_test.go:631-634` | remove | backend-engineer | fixed (round 2) |
| P2-23 | backend | follow-up | `internal/notify/service.go:364`: one 5-min silent-refresh throttle per user, so a connect refresh can swallow a sync refresh that follows it | not in PR 2 (the app refreshes on the first; a later sync shows on the next open) | — | deferred |
| P2-24 | re-review | should | refresh's provider call (`cmd/api/connections.go:843`) runs on the step context: cut mid-flight after the provider rotated → `needs_reauth`; `deregisterOne`'s refresh the same (`invalid_grant` read as `gone`) | the call on `WithTimeout(WithoutCancel(ctx), providerTimeout)`; no new refresh with less than call + persist left | backend-engineer | fixed (round 3) |
| P2-25 | re-review | should | `SaveRefresh` (`store/connections.go:746`) matches user + provider only: a relink/replace mid-refresh writes P1's tokens onto P2's connection | compare-and-swap on the claimed token; 0 rows → the queue branch | backend-engineer | fixed (round 3) |
| P2-26 | re-review | nit | confirm locks its row then the other account's (`connections.go:358,393`); `StoreInbox` FOR SHARE in `user_id` order (`inbox.go:79`) → 40P01 on a replace+relink with a push for both | lock both rows in one `ORDER BY user_id FOR UPDATE` after both apply locks (apply locks in a fixed order too) | backend-engineer | fixed (round 3) |
| P2-27 | re-review | nit | `ExpireInbox` (`inbox.go:333`) / `PurgeInbox`: the outer statement doesn't re-check `state` after a lock wait | `AND state='pending'` in the outer WHERE | backend-engineer | fixed (round 3) |
| P2-28 | re-review | nit | `API.md:1167,1188` promise 200 on any repeat within 10 min; P2-20 answers 409 once the connection was revoked/removed | name the case in the repeat text and the 409 row | api-contract-owner | fixed (API.md, check-api-doc green) |
| P2-29 | re-review | nit | `QueueDeregistration` on the request context in the callback's cap and permission paths (`connections.go:467,480`) | `handBack`'s detached context | backend-engineer | fixed (round 3) |

Privacy (phase 2): Garmin as a processor needs a DPA and a privacy-text update that discloses the 30-day
deregistration rows after account deletion and failed payloads up to 14 days.
