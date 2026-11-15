# Review of A1 (2026-11-15)

One review of the staged diff by code-reviewer and security-privacy-reviewer, then a narrow security re-check of the fixes.
Neither found a blocker in the push handler, the OIDC check, the outbox or migration 0034.

## Findings and what happened

| # | From | Severity | Finding | Outcome |
|---|---|---|---|---|
| 1 | code | blocker (CI) | `docs/ARCHITECTURE.md`'s package map was stale (`pkgmap.sh --check` in `lint`) | fixed: map regenerated, the events prose updated; `make lint` green |
| 2 | code | major | With no Google keys loaded (a failed first fetch), every push and tick got 503 until the hourly refresh, so Pub/Sub would dead-letter | fixed: a fetch on the request, at most every 10 s, detached from the caller's cancel and held to its deadline (≤ 5 s) |
| 3 | security | major (privacy) | `ScoredEvent` carried recovery, readiness, state and sleep values that nobody reads; on Pub/Sub they'd sit in Google's backlog and DLQ | fixed: `user_id`, `day`, `request_id` only; no stored number moves |
| 4 | code | minor | Outbox attempts burned per request with no backoff | fixed: refused publishes (no topic, 400/403/404/413) are set aside at once; transient failures count only in `Sweep` |
| 5 | code | minor | An invite-settings refresh cut by the tick budget blocked bound invites for 15 min | fixed: a cut read records nothing |
| 6 | code | minor | A push in flight at SIGTERM can hold a pool connection past the pool step's 1 s (exit 1) | **A2** (it re-pins the budget for Grace 10 s) |
| 7 | security | minor | An unknown `kid` from the internet got 503 `jwks_refresh_limited` | fixed: 401 `unknown_key` unless the last fetch failed |
| 8 | security | minor | No limit on ticks across instances (a valid `ryft-push` token could run ticks back to back for its hour) | accepted: the token only reaches our own endpoints and is never logged; scoring runs one instance; revisit with A2 |
| 9 | security | minor | Unbounded `messageId`/`subscription`/`ryft-message-id` in logs and the outbox | fixed: `messageId` `^[0-9]{1,32}$`, logged values cut, the deferral ID ≤ 256 bytes |
| 10 | security | minor | The `reason` metric label came from the event body | fixed: known reasons, else `unknown` |
| 11 | security | minor | `PUBSUB_ENDPOINT` accepted any https host, and the metadata token goes there | fixed: `https://<name>.googleapis.com` only |
| 12 | security | minor | The emulator and curl images are by tag (compose, CI) | open: digests couldn't be looked up from the session (**needs you**, before merge) |
| 13 | security | minor | Retention (invite email purge, deletion retries) now depends on the tick, which the €20 stop line pauses | fixed: OPERATIONS → GCP says so and what to do |
| 14 | re-check | low | A Pub/Sub refusal after a deploy sets every due row aside; the runbook released one id at a time | fixed: a bulk release on `last_error LIKE 'rejected, set aside:%'` |
| 15 | re-check | low | `OutboxStuck`'s summary still says "after 50 failed sends" | **A2** (its alert test pins the text; promtool needs Podman) |
| 16 | re-check | low | `dlq.sh` prints 300 bytes of gcloud's stderr on a failed publish | accepted: no letter data in it (data goes through a mode-600 flags file) |
| 17 | code | minor | Lint: gosec G101/G704 on the client, an unused `nolint`, a revive comment, an `%v` | fixed by the lead (`//nolint:gosec` with the reason; `make lint` 0 issues) |

## For A2
- `internal/auth/supabase.go` has finding 2's shape (no keys → 503 until the hourly refresh); a cold Cloud Run instance makes it likelier.
- Findings 6 and 15.
- The OTLP hook is the `nil` `Finally` of every tick (`internal/app`, `cmd/api`, `cmd/ingest`).
- The `:9090` listener, `/healthz` and `/readyz` are unchanged; `OBSERVABILITY.md` still names the consume/job tier messages.
- A Cloud Run Job (`migrate`, `scoring-rescore`) that calls `config.Load` needs the `PUSH_*` and Pub/Sub env.
- The decision numbers left are D220–D227.

## Privacy
A1 deploys nothing, so the policy in force needs no change for it. Before the first production event goes through Pub/Sub,
C2 must be in force (Google Cloud as processor, event retention 7 days, dead letters 14 days), and Google's Cloud Data
Processing Addendum must be accepted for the billing account / project.
