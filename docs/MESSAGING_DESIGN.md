# RYFT messaging — RabbitMQ (2026-10-21)

The review of 2026-10-19 recommended keeping NATS JetStream; the user decided for
RabbitMQ (familiarity, the management UI, dead-lettering and retries in the broker).
That decision and its costs: `DECISIONS.md` D97. Messages are **triggers, not records**:
Postgres is the source of truth, `POST /v1/recompute` replays, and every handler is
idempotent (D100), so a duplicate changes nothing.

## The flows

| Event (routing key) | Published by | Consumed by | What it says |
| --- | --- | --- | --- |
| `ingested` | ingest (a sync), api (recompute, merge, session edits) | scoring | these days of this user need scoring |
| `session.completed` | api | scoring | a strength session was finished; score its day |
| `scored` | scoring | notify | today has its first recovery: the morning push |
| `session.scored` | scoring | notify | a completed session has its load: the session push |
| `refresh` | scoring | notify | a sync's days are scored: a silent push |

Every event is written to the publisher's **outbox** first (D101), in the same
transaction as what it announces where that matters (a sync's queued days, a session's
completion), and the publisher's relay sends it with the broker's confirm.

## Topology (defined once: `backend/internal/events/amqpbus/topology.go`, D99)

```text
 ingest ─┐                                    ┌─► scoring ──► (handled, ack)
 api ────┼─► ryft.events (topic) ──ingested───┤      │ fails
 scoring ┘        │        session.completed  │      ▼
                  │                           │  ryft.retry ─► scoring.retry.5s ─┐ TTL
                  │  scored, session.scored,  │      (topic)   scoring.retry.30s │
                  └──refresh─────────────────►└►notify        scoring.retry.5m  │
                                                     │ fails/defers             ▼
                                                     ▼                     ryft.requeue
                                       notify.retry.{5s,30s,5m}            (direct, broker only)
                                       notify.wait.{1m,15m,1h} ──TTL──────► back to its queue
                after 6 attempts, or undecodable ──► ryft.dlx (topic) ──► dlq.scoring, dlq.notify (14 days)
```

- **Vhost** `ryft`; every queue a **quorum** queue on all three nodes; the tunables
  (delivery limit 10, dead-lettering at-least-once, TTLs, length limits with
  `reject-publish`) are policies, one per queue.
- **Retry tiers:** 5 s, 30 s, then 5 minutes, six attempts in all (about 16 minutes), then
  the DLQ with `Ryft-Subject`, `Ryft-Consumer`, `Ryft-Deliveries`, `Ryft-Error`.
  A retry is an acknowledged copy with `x-ryft-attempt`, never a requeue; the broker's
  own delivery limit only catches what the client cannot settle (a consumer that died
  holding the message).
- **The key:** a delivery from `ryft.events` is handled by its routing key, which the
  publisher's topic permission limits; its `type` must be empty or the same, else it is
  a bad event, dead-lettered under the routing key (so ingest cannot get a session
  handled by calling its `ingested` message `session.completed`). A retry tier's copy
  comes back through `ryft.requeue` and is handled by the type its first delivery was
  checked with.
- **Replay** (`scripts/dlq.sh replay`, after the cause is fixed): the letter goes back to
  `ryft.events` with its key, `x-ryft-replayed: true` and none of the DLQ's or the tiers'
  headers. Its handler takes it as a redelivery (`events.Redelivered`), with six attempts
  again; the marker is a header of its own, not a faked attempt count. Only the keys the
  queue's consumer is bound for are replayed, and only when the letter's type is its key:
  a dead letter's `Ryft-Subject` is its type only when the consumer is bound for that type,
  else its routing key (`amqpbus.subjectOf`), so a forged type is never sent on by the
  administrator who replays.
- **Deferral** (notify in quiet hours): the longest wait tier that fits, again until the
  handler stops deferring; not an attempt.
- **`events.Permanent`** (the event's user no longer exists): dropped and counted, not
  dead-lettered.
- **Generated:** `go run ./cmd/rabbitmq-topology` (in `backend/`) writes the Topology
  Operator's objects (`deploy/rabbitmq/topology/topology.yaml`) and compose's
  definitions (`deploy/rabbitmq/dev/definitions.json`); `make k8s-validate` fails when
  they differ.

## Guarantees

| Need | How |
| --- | --- |
| Nothing stored goes unannounced | the outbox: written with the data, sent until confirmed |
| Nothing published is lost | publisher confirms, `mandatory` (unroutable is an error), persistent messages, quorum queues on three nodes |
| A failure is retried, spaced | the TTL tiers, then the DLQ; nothing is retried in a tight loop |
| Duplicates are harmless | idempotent handlers (scoring rescores to the same rows; notify claims per user, day and category; refresh is throttled) |
| Ordering | per user, oldest first, from the outbox; not across retries, and not needed: a day scored before its history is scored again, with the days after it, when the history arrives, and a retry finishes what its attempt began (`events.Redelivered`; `ARCHITECTURE.md` → Baselines) |
| A broker outage | ingest and the api keep taking requests (their readiness leaves the broker out), the outbox keeps the events, the relay sends them afterwards; scoring and notify wait, unready (`/readyz` says why); scoring's 15-minute pass catches up yesterday and today anyway |

## Rights (D99)

Per service a user in vhost `ryft`, no `configure` for anyone. Write rights per exchange
and, with topic permissions, per routing key; read rights per queue:

| User | May publish | May consume |
| --- | --- | --- |
| ingest | `ryft.events`: `ingested` | — |
| api | `ryft.events`: `ingested`, `session.completed` | — |
| scoring | `ryft.events`: `scored`, `session.scored`, `refresh`; `ryft.retry`: `scoring.*`; `ryft.dlx`: `scoring` | `scoring` |
| notify | `ryft.retry`: `notify.*`; `ryft.dlx`: `notify` | `notify` |

`backend/internal/events/amqpbus/permissions_test.go` proves the matrix against a broker.
On the wire: mutual TLS on 5671 (the internal CA, each service its own certificate) and
the user's password (Vault): D98.

## Operating it

`OPERATIONS.md` → RabbitMQ: the UI by port-forward, health, dead letters
(`scripts/dlq.sh`), the outbox, topology changes, node drains, alarms, upgrades,
credentials. The cutover from NATS: `DECISIONS.md` D102, `OPERATIONS.md` → RabbitMQ →
The cutover.

## Revisit if

Sustained hundreds of messages a second (classic queues or streams instead of quorum
queues for the retry tiers), a consumer that needs the event history as data (streams),
or a need for exactly-once (an idempotency table, D100).
