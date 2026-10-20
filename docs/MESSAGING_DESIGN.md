# RYFT messaging design review — NATS JetStream vs. RabbitMQ vs. Kafka (2026-10-19)

## Decision
Keep NATS JetStream. Record as D48. Add a dead-letter stream, consumer-lag metrics and
alerts; cluster JetStream (3 replicas) when the third node arrives. New consumers
(webhooks, weekly report, coach jobs) are durable consumers on stream `RYFT`, never new
infrastructure.

## Requirements
- Flows: `ryft.ingested`, `ryft.session.completed` → scoring; `ryft.scored`,
  `ryft.session.scored` → notify; planned: webhooks (M6b), weekly report (M7), coach (M9).
- Semantics: at-least-once; duplicates harmless (rescoring idempotent; one push per
  user/day via dedup + `notifications_sent`); ordering only per user, oldest-first.
- Volume: 3–6 events/user/day → < 0.1 msg/s at 1k users, < 10 msg/s at 100k; payload < 1 KB.
- Latency: seconds–minutes. Durability: survive restarts; loss tolerable (Postgres is
  the source of truth; `POST /v1/recompute` replays). Availability: single-AZ acceptable;
  services back off when the broker is down (D47).
- Security: per-service identity and least privilege (D43, D45) — implemented and tested.
- Constraints: one developer + agents, 2× t3.medium, €120–160/mo, Go, kustomize.

## Current implementation
Stream `RYFT` (`ryft.>`, work-queue, file store, 7 d, 1 GB); durable pull consumers
`scoring`, `notify`; publish dedup IDs (2-min window); NAK-with-delay, `MaxDeliver`;
NATS users per service with subject ACLs and private inboxes; bcrypt creds from Vault;
1 replica, 2 Gi PVC; publish/handle metrics; permission tests in CI.

## Guarantee comparison
| Need | NATS JetStream | RabbitMQ | Kafka |
|---|---|---|---|
| Retry/backoff | built in | built in (DLX+TTL for delay) | consumer-managed |
| Publish dedup | built in, in use | none → re-implement in Postgres | producer-retry dedup only |
| Fan-out | durable consumer + FilterSubject | queue binding | consumer group |
| Least privilege | done (D43) | redo (vhost/user/regex) | redo (ACLs, SASL/mTLS) |
| Dead-letter | missing today (fix) | native | your own topic |
| Replay | not needed (Postgres) | weak | strong, irrelevant here |
| Ops | 1 pod, 64 MB | Erlang ×3 quorum, alarms | KRaft ×3 or MSK ≈ €100+/mo, JVM |

## Trade-offs
| Option | Complexity | Cost | Time | Verdict |
|---|---|---|---|---|
| Keep NATS + DLQ + metrics, cluster at node 3 | low | €0 | days | **recommended** |
| RabbitMQ | medium | +€10–20/mo | 2–3 wk (ACLs, dedup, Vault, NetPol, tests) | not justified |
| Kafka | high | +€100–150/mo | 4–6 wk | wrong tool: triggers, not a log |
| Pluggable `events.Bus` with Rabbit in `local-postgres` overlay | contained | €0 prod | 1 wk | only as a learning exercise |

## Follow-ups for the platform session
1. `DECISIONS.md` D48 as above, with revisit conditions.
2. `internal/events`: on `MaxDeliver` exhaustion republish to `ryft.dlq.<subject>`
   (stream `RYFT_DLQ`, limits retention 30 d); metric `ryft_events_dlq_total`;
   `nats.conf` permissions; `OPERATIONS.md` runbook to inspect/re-drive.
3. Metrics/alerts: consumer `num_pending`, `num_ack_pending`, oldest unacked age;
   alert pending > 100 for 15 min.
4. With the third node: JetStream 3 replicas, stream `Replicas: 3`, NATS PDB.
5. JetStream KV before Redis for any small shared state.

## Revisit if
Sustained > 1 000 msg/s (server-side raw streams), non-Go consumers needing the event
history as data, compliance need for an immutable event log, or multi-region fan-out.
