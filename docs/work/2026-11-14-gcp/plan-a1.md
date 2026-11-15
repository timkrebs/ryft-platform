# Ticket A1: `backend: Pub/Sub messaging and the tick` (2026-11-14)

The first half of §10 row A of `design.md`: everything driven by events and ticks. Branch `backend/pubsub` from `origin/main`
(`c62b265`). It builds to B's contract (`ticket-b.md` → Interfaces) and deploys nothing (`GCP_DEPLOY` is off). A2 (the runtime,
HTTP, database, observability) comes next, in a fresh session.

## Interfaces (pinned; every ticket builds to these)

**Names** (exactly `infra/gcp/pubsub.auto.tfvars.json`):
- key → topic: `ingested` → `ryft-ingested`, `session.completed` → `ryft-session-completed`, `scored` → `ryft-scored`,
  `session.scored` → `ryft-session-scored`, `refresh` → `ryft-refresh`. notify's deferrals → `ryft-notify-wait`. DLQs
  `ryft-dlq-scoring`, `ryft-dlq-notify`. Ticks `ryft-tick`.
- Ordering key = the event's `user_id` (canonical UUID string) on `ryft-ingested` and `ryft-session-completed` only.
- Attributes: `ryft-key`, `ryft-message-id` (`Message.ID`), `ryft-reason` (`Message.Reason`), `ryft-replayed=true` on a replay,
  `ryft-error` (≤ 512 bytes) only on a letter a consumer dead-letters itself.

**Routes** (POST, wrapped envelope: `message.data`, `message.attributes`, `message.messageId`, `message.publishTime`,
`message.orderingKey`, `deliveryAttempt`, `subscription`):
- scoring: `/push/ingested`, `/push/session.completed`. notify: `/push/scored`, `/push/session.scored`, `/push/refresh`,
  `/push/wait` (here `ryft-key` must be present and ∈ `events.NotifyKeys`; elsewhere absent or equal to the path's key).
- all four: `/internal/tick` (envelope `data = {}`, `ryft-key=tick`).

**Status:** 204 = ack (ok; `events.Permanent` → dropped; a bad event → published to the consumer's own DLQ topic with
`ryft-error`, then ack; `events.Defer(d)` → an outbox row of publisher `notify` with `available_at = now + d`, then ack). 500 =
nack (handler error; DLQ publish failed; the deferral's outbox write failed). A missing or bad token → 401 (nack). Redelivery:
`deliveryAttempt > 1` or `ryft-replayed=true` → `events.WithRedelivery(ctx)`; a missing `deliveryAttempt` counts as 1.

**Push and tick auth** (`internal/auth`, Google OIDC, on every `/push/*` and `/internal/*` of all four services): RS256 only;
`exp` required; `iss` `https://accounts.google.com` (also `accounts.google.com`, which Google's own verifiers accept); `aud` ==
`PUSH_AUDIENCE` exactly (never the `Host` header); `email` == `PUSH_SA_EMAIL` with `email_verified` true; `sub` == `PUSH_SA_ID`.
Google's JWKS through `internal/auth`'s existing keyfunc, not the Supabase parser. `Authorization` is never logged.

**Env** (B's names; config fails at load, before any connection):
- `GCP_PROJECT` (required unless `PUBSUB_EMULATOR_HOST` is set), `PUBSUB_ENDPOINT` (default
  `https://europe-west1-pubsub.googleapis.com`), `PUBSUB_EMULATOR_HOST` (host:port, no token).
- `PUSH_AUDIENCE`, `PUSH_SA_EMAIL`, `PUSH_SA_ID`: required unless `ALLOW_INSECURE_PUSH=true`.
- `ALLOW_INSECURE_PUSH=true` and `PUBSUB_EMULATOR_HOST` are both refused when `K_SERVICE` is set (Cloud Run sets it).
- `AMQP_*` go.
- Publish: REST `POST {endpoint}/v1/projects/{p}/topics/{t}:publish`, token from the metadata server
  (`Metadata-Flavor: Google`), cached until shortly before expiry. Stdlib only, no Google client library.

**Tick** (`POST /internal/tick`): verify, then run the service's ticks in order within a budget; answer 204 after auth whatever a
step did (a step's error is logged; a failed tick is dropped, the next comes in 15 min). A tick that arrives while one runs in
the same process is answered 204 and counted as `overlap`. `ryft_ticks_total{service,result}`, result ∈ `ok|error|overlap`.
Budgets: api 45 s, ingest 45 s, scoring 240 s, notify 45 s.
- api: db ping → `ryft_db_up`, outbox `Sweep`, account reconcile, invite settings refresh (and a lazy first refresh on the first
  invite request, fail closed until then; no Supabase call at start), invite email purge.
- ingest: db ping, outbox `Sweep`.
- scoring: db ping, outbox `Sweep`, the scoring pass + housekeeping (the pass stops between users when the budget ends).
- notify: db ping, outbox `Sweep` (due wait rows), stale check.
- No more tickers, no `db.Watch`, no outbox poll in the services.

**Outbox** (D101 amended):
- `available_at timestamptz NOT NULL DEFAULT now()`; only due rows are drained and count in `ryft_outbox_pending` and
  `ryft_outbox_oldest_seconds` (B's `OutboxBacklog` comment says so).
- `Send(ctx, m)` = insert + `Flush`. `SendAt(ctx, m, at)` = insert with `available_at`, no flush (notify's deferral).
- `Flush(ctx)`: drains due rows inline, 2 s budget, detached from the request's cancel, logs and returns nothing. Called after
  the commit that inserted rows (replaces `Kick()`).
- `Sweep(ctx) error`: every due row in batches until none or ctx ends, then observe and clean (the tick).
- `Run(ctx)` stays for tests and local tools only.
- One user's rows are published in id order even with concurrent `Flush`es in one or several instances.

**Migration `0034_outbox_available_at.sql`** (additive): the column, the publisher CHECK widened to `notify`, a due index, and
`ryft_notify` granted the outbox like the other publishers, with an RLS policy limited to `publisher = 'notify'`.

**Topology:** Go in `internal/events/pubsub/topology.go`. `go run ./cmd/pubsub-topology` writes
`infra/gcp/pubsub.auto.tfvars.json` byte for byte (`MarshalIndent(v, "", "  ")` + `\n`, maps at the top and in the three
collections, struct field order per ticket-b) and the emulator's init script `deploy/pubsub-emulator/init.sh` (push endpoints
`http://<svc>:8080<path>`, the same retry policy and attempts as Terraform's constants: 10–600 s, 20). `-check` fails on drift.

**Local:** compose runs the Pub/Sub emulator, the init script, and a ticker publishing `{}` with `ryft-key=tick` to `ryft-tick`
every 60 s. The services run with `PUBSUB_EMULATOR_HOST` and `ALLOW_INSECURE_PUSH=true`, inline flush and ticks, as on Cloud Run
(one runtime, not a second mode). Integration tests read `RYFT_TEST_PUBSUB_EMULATOR_HOST`; CI runs the emulator with host
networking so a push reaches a test's `127.0.0.1` server.

## Tickets

| # | Owner | Files (one writer each) | Acceptance |
|---|---|---|---|
| A1-0 | api-contract-owner | `docs/API.md` | Base URLs row: `https://api.ryftlab.de` (api) and `https://ingest.ryftlab.de` (ingest, `POST /v1/sync`), the ingest URL required; a changelog row dated 2026-11-14. No shape change. `make check-api-doc check-swift` |
| A1-1 | database-engineer | `backend/internal/db/migrations/0034_*.sql`, the db tests that pin grants or policies | the migration applies on a fresh Postgres and on top of 0033, idempotent; `ryft_notify` reaches only its own rows under RLS; tests pin it |
| A1-2 | backend-engineer (transport) | `internal/events/{events.go,doc.go,*_test.go}`, `internal/events/pubsub/**` (new), `internal/events/amqpbus/**` (deleted), `internal/auth/google*` (new) + `auth/doc.go`, `internal/eventbus/**`, `internal/config/**`, `internal/metrics/metrics.go`, `internal/arch_test.go`, `cmd/pubsub-topology/**` (new), `cmd/rabbitmq-topology/**` (deleted), `go.mod`/`go.sum`, `deploy/pubsub-emulator/init.sh` (generated) | `go vet` and `go test` of these packages; topology output byte-identical to the committed tfvars; `amqp091-go` gone from go.mod; the handover note `a1-interfaces.md` |
| A1-3 | backend-engineer (outbox) | `internal/events/outbox/**` | Flush, SendAt, Sweep, due-only drain and gauges, order under concurrency; tests (integration with `RYFT_TEST_POSTGRES_URL`) |
| A1-4 | backend-engineer (wiring), after A1-2/A1-3 | `internal/app/**`, `cmd/{api,ingest,scoring,notify}/**` | push routes and ticks wired; no tickers, no `db.Watch`, no Consumers; the shutdown budget test re-pinned (Grace stays 30 s; 10 s is A2); `go vet ./... && go test ./...` green |
| A1-5 | gitops-engineer, after A1-2 | `docker-compose.yml`, `Makefile`, `.github/workflows/ci.yml`, `scripts/dev-amqp-certs.sh` (deleted), `scripts/dlq.sh` + its test, `scripts/pkgmap.sh`, `scripts/coverage.sh`, `docs/OPERATIONS.md` (the dead-letter runbook only) | `make up` stack defined; RabbitMQ out of compose/Makefile/CI; the emulator in CI's test job; the topology drift check in CI; `actionlint`; `bash scripts/tests/dlq_test.sh` |
| A1-6 | test-engineer, after A1-2/A1-3 | new test files only: `internal/events/pubsub/emulator_test.go`, fixtures under `internal/events/pubsub/testdata/` | publish → push → nack → DLQ against the emulator; recorded envelopes for every status path |
| A1-7 | docs (general-purpose), after A1-2/A1-3 | `docs/MESSAGING_DESIGN.md` (rewrite), `docs/DECISIONS.md`, `docs/OBSERVABILITY.md` (metric rows), `CLAUDE.md` (the topology rule) | D216–D219 written; the messaging entries of §10 marked |
| A1-8 | code-reviewer, security-privacy-reviewer | read-only | `review-a1.md`; blockers fixed by the owner, re-verified |

Order: A1-0 ∥ A1-1 ∥ A1-2 ∥ A1-3 → A1-4 ∥ A1-5 ∥ A1-6 ∥ A1-7 → A1-8 → fixes → local checks → PR → CI green.

## D-numbers
A1 writes **D216–D219** (D216 the platform and D217 the two hosts, which the API.md entry needs; D218 Pub/Sub, push and OIDC;
D219 the tick, the inline outbox and the not-before). A2 writes D220–D227.

## Risks
- The emulator's push and dead-letter behaviour differ from production (no IAM, no OIDC). The Terraform tests pin IAM; the
  OIDC check is pinned by Go tests with signed tokens.
- Concurrent `Flush`es can reorder one user's events unless the drain serialises; pinned by a test.
- Integration tests run only in CI (no Postgres or emulator locally). A red first round is likely; desk-check every test.

## Rollback
Nothing deploys. Revert the PR. The migration is additive and harmless if it stays.
