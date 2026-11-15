# RYFT messaging — Pub/Sub (2026-11-15)

RabbitMQ (D97) is replaced by Google Cloud Pub/Sub with the move to GCP serverless (D216, D218; design
`docs/work/2026-11-14-gcp/design.md` §2). Every timer becomes one Cloud Scheduler tick, and the outbox relays
inline (D219). What did not change: messages are **triggers, not records**. Postgres is the source of truth,
`POST /v1/recompute` replays, and every handler is idempotent (D100), so a duplicate changes nothing.

The code:
- `internal/events`: transport-neutral. `Message`, `Sender`, `Bus`, the encoders and decoders, `Handler`, the
  error kinds `Permanent`, `Bad`, `Defer`, `WithRedelivery`, and `Rejected` (a send no retry can deliver). `Bus` has no `Ready` and no `Close`: a REST publish
  holds no connection.
- `internal/events/pubsub`: the transport. REST publish, the push and tick handlers, and the topology. Stdlib only,
  no Google client library. Only `internal/eventbus` and `cmd/pubsub-topology` import it.
- `internal/events/outbox`: the outbox and its relay (D101 as amended by D219).
- `internal/auth` (`google.go`): the push and tick token check.

## The flows

| Event key | Published by | Consumed by | What it says |
| --- | --- | --- | --- |
| `ingested` | ingest (a sync), api (recompute, merge, session edits, the training import) | scoring | these days of this user need scoring |
| `session.completed` | api | scoring | a strength session was finished; score its day |
| `scored` | scoring | notify | today has its first recovery: the morning push (user and day only, see below) |
| `session.scored` | scoring | notify | a completed session has its load: the session push |
| `refresh` | scoring | notify | a sync's days are scored: a silent push |
| (a deferral) | notify | notify | an event held past quiet hours, back under its own key |
| `tick` | Cloud Scheduler | all four services | run the service's periodic steps |

Every event is written to the publisher's **outbox** first (D101), in the same transaction as what it announces
where that matters (a sync's queued days, a session's completion). The relay then publishes it.

**No health values in an event (D218).** The bodies hold ids, days, a reason and a request id. `scored` is
`{user_id, day, request_id}`: notify reads the score from `daily_scores` when it sends, so the number in the push is
the one stored then. A message can sit in Google's Pub/Sub for 7 days, in a dead-letter hold for 14 and in notify's
waiting rows until quiet hours end. Images before A1 also wrote `recovery`, `readiness`, `state`,
`sleep_performance` and `sleep_min`; a message of theirs still decodes (unknown fields are ignored).

## Topology (defined once: `backend/internal/events/pubsub/topology.go`, D218)

One topic per event key. Pub/Sub grants publishing per topic, not per attribute, so this keeps D99's matrix:
ingest cannot publish a `session.completed`.

| Topic | Publishers (`roles/pubsub.publisher` on this topic only) | Subscription → service, path | Ordering key | Ack deadline | Dead letters to |
| --- | --- | --- | --- | --- | --- |
| `ryft-ingested` | api, ingest | `scoring-ingested` → scoring `/push/ingested` | `user_id` | 300 s | `ryft-dlq-scoring` |
| `ryft-session-completed` | api | `scoring-session-completed` → scoring `/push/session.completed` | `user_id` | 300 s | `ryft-dlq-scoring` |
| `ryft-scored` | scoring | `notify-scored` → notify `/push/scored` | — | 60 s | `ryft-dlq-notify` |
| `ryft-session-scored` | scoring | `notify-session-scored` → notify `/push/session.scored` | — | 60 s | `ryft-dlq-notify` |
| `ryft-refresh` | scoring | `notify-refresh` → notify `/push/refresh` | — | 60 s | `ryft-dlq-notify` |
| `ryft-notify-wait` | notify | `notify-wait` → notify `/push/wait` | — | 60 s | `ryft-dlq-notify` |
| `ryft-tick` | the Cloud Scheduler service agent | `tick-api`, `tick-ingest`, `tick-scoring`, `tick-notify` → `/internal/tick` | — | 600 s | none |
| `ryft-dlq-scoring` | scoring (its own bad events), the Pub/Sub service agent (forwarding) | `dlq-scoring-hold` (pull) | — | 60 s | — |
| `ryft-dlq-notify` | notify (its own bad events), the Pub/Sub service agent (forwarding) | `dlq-notify-hold` (pull) | — | 60 s | — |

- **Retention:** the event subscriptions keep an undelivered message 7 days, the tick subscriptions 10 minutes (the
  next tick comes 15 minutes later), and the hold subscriptions keep a letter 14 days.
- **Endpoints:** each push subscription pushes to the service's `run.app` URL. Scoring and notify have ingress
  `internal`, which admits Pub/Sub only on that URL, not on a custom domain (design §2.2, §5.1).
- **Rights:** nobody holds `subscriber` except the Pub/Sub service agent (on the source subscriptions, for
  dead-lettering) and the operator (the hold subscriptions). `infra/gcp/pubsub.tf` adds the service agents' bindings;
  `infra/gcp/tests` pins the matrix.
- **Region:** topics store messages in europe-west1 only (`message_storage_policy`). The services publish to the
  regional endpoint `https://europe-west1-pubsub.googleapis.com` (`PUBSUB_ENDPOINT`'s default).
- **Generated:** `go run ./cmd/pubsub-topology` (in `backend/`) writes `infra/gcp/pubsub.auto.tfvars.json`
  (Terraform's input) and `deploy/pubsub-emulator/init.sh` (compose's emulator). `-check` fails when either
  differs. `Topology.Validate` checks the topology is closed: unique names, existing topics, a dead-letter topic that
  is not its own source, every consumer key pushed to its consumer. The delivery constants (10–600 s, 20 attempts)
  are also in `pubsub.tf`, and `TestTerraformConstants` reads that file so the two cannot drift.

## On the wire

Publish is one REST call per message, `POST {endpoint}/v1/projects/{project}/topics/{topic}:publish`, with an access
token from the metadata server, cached until a minute before it expires and fetched again after a 401. To
`PUBSUB_EMULATOR_HOST` it is plain HTTP without a token.

- `data` is the JSON body exactly as before (`events.Message.Body`).
- Attributes:
  - `ryft-key`: the event key.
  - `ryft-message-id`: `Message.ID`, stable for the same trigger. Not a dedup key (D100).
  - `ryft-reason`: `Message.Reason`, the metric label. On the consumer the label is the decoded event's reason
    when it is one of `events.Reasons` (the closed list the publishers write), else `unknown`.
  - `ryft-replayed=true`: only on a dead letter `scripts/dlq.sh` put back.
  - `ryft-error`: only on a letter a consumer dead-lettered itself. Why, at most 512 bytes.
- `orderingKey`: the body's `user_id` as a canonical UUID, on scoring's two topics only. An event for those topics
  without one is rejected: the outbox sets it aside at once.
- A push arrives as the wrapped envelope: `message.data`, `message.attributes`, `message.messageId`,
  `message.publishTime`, `message.orderingKey`, `deliveryAttempt`, `subscription`. A body over 1 MiB is refused, and
  so is a `messageId` that is not Pub/Sub's (1 to 32 decimal digits). The envelope is whoever pushed it: the
  subscription is cut to 256 bytes in a log line, a `ryft-key` that is not a key to 64, and a deferral keeps at most
  256 bytes of `ryft-message-id`.

**The key rule.** The subscription's path fixes the key, and publishers can't change it. On `/push/<key>` the
`ryft-key` attribute must be absent or `<key>`. Otherwise the message is a bad event, dead-lettered under `<key>`. On
`/push/wait` it must be present and one of notify's keys (`scored`, `session.scored`, `refresh`), or the letter is
filed under none. This keeps RabbitMQ's "its `type` must be empty or the same".

## Push auth (D218)

Every `/push/*` and `/internal/*` request on all four services is checked first (`auth.GoogleOIDC`):

- RS256 only, `exp` required.
- `iss` is `https://accounts.google.com` or `accounts.google.com` (Google's own verifiers accept both).
- `aud` equals `PUSH_AUDIENCE` exactly, never the `Host` header. Terraform sets `oidc_token.audience` explicitly to
  the service's `run.app` URL.
- `email` equals `PUSH_SA_EMAIL` (`ryft-push@…`), with `email_verified` true.
- `sub` equals `PUSH_SA_ID`, that account's unique ID.
- The keys are Google's JWKS, through `internal/auth`'s keyfunc (refreshed hourly), not the Supabase parser.

A bad or missing token is a 401. A token that can't be checked now is a 503: no keys are loaded, or its key id is
unknown while the last fetch failed. While no keys are loaded (a cold instance whose first fetch failed), a token
fetches them first, at most once every 10 s per instance and within the request's deadline (at most 5 s), so the
pushes verify again as soon as Google's JWKS answers, not at the hourly refresh. An unknown key id refreshes the set
at most once a minute; after that, while the last fetch worked, it is a 401 `unknown_key`, since anyone can send
one on the public api and ingest. Pub/Sub retries a 401 as it does a 503. Both count in
`ryft_auth_failures_total` with the Supabase verifier's reasons (`OBSERVABILITY.md`). Neither the `Authorization`
header nor a body is ever logged. On api and ingest (public) this check is the only gate. On scoring and notify it is
the backstop behind ingress `internal` and Cloud Run IAM (only `ryft-push` has `roles/run.invoker`).

A token isn't bound to the body and lives an hour, so a captured one could replay or inject events for that hour.
D100 makes a replay harmless, and an injected event only triggers a rescore or a push the user would have had anyway
(design §2.2).

## Status mapping

Pub/Sub takes 102, 200, 201, 202 and 204 as an ack. Any other status is a nack. The handlers answer 204.

| The handler returns | Answer | `ryft_events_handled_total` result |
| --- | --- | --- |
| `nil` | 204 | `ok` |
| `events.Permanent` (the user is gone) | 204 | `dropped` |
| `events.Bad` (data that doesn't decode, a wrong `ryft-key`) | published to the consumer's own DLQ topic with `ryft-error`, then 204. If that publish fails: 500 | `bad` (and `ryft_events_dlq_total` `ok` or `error`) |
| `events.Defer(d)` (notify) | an outbox row due at now + d, then 204. No sink (scoring), or the write fails: 500 | `deferred`, or `retry` |
| any other error, or a panic | 500 | `retry`; `failed` from attempt 20 |

Before the handler runs, the request can also get 401 or 503 (the token), 405 (not a POST), 400 (not an envelope, or
no decimal `messageId`) or 413 (over 1 MiB). Each is a nack, so on an event subscription Pub/Sub dead-letters it after its
attempts.

## Retries and dead letters

- **Retry policy:** exponential backoff from 10 s to 600 s (the allowed range is 0–600 s). **For push, Pub/Sub may
  use its push backoff (100 ms to 60 s) instead.** So the spacing isn't ours to fix (design §2.2).
- **Attempts:** `max_delivery_attempts = 20` (allowed 5–100). That is roughly 10 minutes to the DLQ under push
  backoff, and well over 20 minutes under the retry policy. The count is best effort, so a message can arrive more
  often than configured.
- **Pub/Sub's dead letters:** after the last attempt, the dead-letter policy forwards the message to
  `ryft-dlq-<consumer>`. Attempts are counted only when the Pub/Sub service agent has `publisher` on the DLQ topic and
  `subscriber` on the source subscription (`pubsub.tf`). Pub/Sub adds `CloudPubSubDeadLetterSource*` attributes but
  can't add `ryft-error`. The last error is in the consumer's log, keyed by `messageId`.
- **A consumer's own dead letters:** a bad event goes straight to the DLQ with `ryft-error`, not through 20 retries.
- **Redelivery:** `deliveryAttempt > 1`, or `ryft-replayed` exactly `true`, reaches the handler as
  `events.WithRedelivery(ctx)` and counts in `ryft_nats_redeliveries_total`. Any other `ryft-replayed` value is
  ignored and logged. A missing `deliveryAttempt` counts as 1. Every event subscription has a dead-letter policy, which
  is when Pub/Sub sets the field. That it does so for push is *(unchecked)* in the design.
- **Saturation:** a busy scoring instance answers 429, and a 429 counts as an attempt. `PushBacklog` alerts before
  that reaches the DLQ.

## Deferral (notify's quiet hours)

Pub/Sub has no per-message delay, and backing off would count as attempts. So:

1. notify's handler returns `events.Defer(d)`.
2. The push handler writes an outbox row (`SendAt`): publisher `notify`, `available_at` = now + d, the original key,
   body, reason and `ryft-message-id` (at most 256 bytes of it). Then it acks. A message deferred twice keeps its ID.
3. The row isn't flushed. notify's tick `Sweep` publishes it once due, to `ryft-notify-wait` with `ryft-key` = the
   original key.
4. `/push/wait` checks the key and hands the event to the same handler again under that key.

A deferral is not an attempt. Its precision is the tick: a push at the end of quiet hours can be up to 15 minutes late.
The knob is a second Scheduler job every 5 minutes for `tick-notify` (design §2.2).

Migration `0034_outbox_available_at.sql` adds `available_at` (default `now()`), widens the publisher CHECK to `notify`,
adds the index `outbox_due`, and grants `ryft_notify` the outbox with an RLS policy `publisher = 'notify'`.

## Ordering

- **From the outbox:** a publisher's rows are published in id order. Each drain batch is a transaction that first
  takes `pg_advisory_xact_lock(7419006, hashtext(publisher))`, with `lock_timeout` set from the drain's budget, so
  concurrent drains in one or several instances go one after another. A `Flush` that loses the race waits for the
  lock within its 2 s budget, then takes whatever is due, its own row included. A drain stops at the first failed
  send. Both the lock and the timeout are transaction-scoped, which Supabase's transaction pooler allows.
- **In Pub/Sub:** scoring's two topics carry `ordering_key = user_id`, and their subscriptions have
  `enable_message_ordering`. For push, Pub/Sub keeps one outstanding message per key, so it holds a user's later
  messages until the current one is acked or dead-lettered. Ordering needs every publish with the same key in the
  same region, hence the regional endpoint.
- **D17 as amended by D218:** scoring runs at most one instance with concurrency 4 (Cloud Run settings, A2/B), and
  each user has one message in flight.

**Caveats:**
- With a dead-letter topic, order "might not be" kept, because forwarding is best effort (Google's docs, design
  §2.2).
- There is no order across the two topics, and none for notify. Notify doesn't need it.
- Id order is commit order only roughly: a row whose transaction commits after a later id was relayed goes after it.
  One user's syncs can't do that, because the store's apply lock serialises them.
- Order is a nicety, not what correctness rests on. A day scored before its history is scored again, with the days
  after it, when the history arrives, and a retry finishes what its attempt began (`events.Redelivered`;
  `ARCHITECTURE.md` → Baselines).

## Idempotency (D100, unchanged and still required)

Pub/Sub delivers at least once. A message can arrive again even after an ack, and exactly-once delivery isn't
available for push. The outbox also sends a row again when a crash or a spent budget falls between the send and its
mark. So every handler stays idempotent, with a test. Scoring rescores to the same rows. Notify claims per user, day
and category. Refresh is throttled. Nothing dedups on `ryft-message-id`.

## The tick (D219)

Cloud Scheduler job `ryft-tick` (`infra/gcp/scheduler.tf`) runs every 15 minutes (UTC). It publishes `{}` with
`ryft-key=tick` to topic `ryft-tick`, and each service's subscription pushes it to `POST /internal/tick`.

- **Auth:** the token is checked as on a push. The envelope's `ryft-key` must be absent or `tick` (else 400).
- **Steps:** the service's steps run in order within a budget. A step must return when its context ends.

  | Service | Budget | Steps |
  | --- | --- | --- |
  | api | 45 s | db ping, outbox `Sweep`, account reconcile, invite settings refresh, invite email purge |
  | ingest | 45 s | db ping, outbox `Sweep` |
  | scoring | 240 s | db ping, outbox `Sweep`, the scoring pass and housekeeping (the pass stops between users when the budget ends) |
  | notify | 45 s | db ping, outbox `Sweep` (its due waits), the stale check |

- **The answer:** 204 after auth, whatever a step did. A step's error is logged, and a panic counts in
  `ryft_http_panics_total` while the next step still runs. A failed tick isn't retried; the next one comes in 15
  minutes. A tick that arrives while one runs in the same process gets 204 at once and counts as `overlap`.
- **Metrics:** `ryft_ticks_total{service,result}`, with result `ok`, `error` or `overlap`. It is counted before the
  final hook (A2's metrics push), and the response comes last: with request-based CPU nothing runs after it.
  `TickMissing` reads it.
- **No other timers remain:** no tickers, no `db.Watch`, no outbox poll in the services. The db ping sets `ryft_db_up`.
  The api makes no Supabase call at start: the invite settings refresh is a tick step, plus one lazy refresh on the
  first bound invite. Until a refresh succeeds, bound invites are refused (fail closed).
- **State in memory is per instance** and is lost between instances. For example, a failing user is retried every
  tick instead of once a day (design §3).

## The outbox (D101 as amended by D219)

Cloud Run gives CPU only while a request runs, so the relay has no goroutine of its own.

- **Inline:** `Insert` writes through the caller's transaction. After the commit and before the handler answers,
  `Flush` drains the due rows, with a 2 s budget (plus 1 s to mark what it sent). Its context is detached from the
  request's cancel, so a client that hangs up doesn't skip the relay. `Send` is `Insert` plus `Flush`. Flush logs a
  failure and returns nothing, and the rows stay.
- **The tick:** `Sweep` relays every due row in batches until none is left, a send fails, or the budget ends. That
  covers rows of a died instance, failed publishes and deferrals that came due. It keeps up to 2 s back for the rest:
  it sets the gauges and deletes published rows older than 7 days.
- **Not before:** `available_at` is when a row becomes due. Only due rows are drained, and only they count in
  `ryft_outbox_pending` and `ryft_outbox_oldest_seconds`. The age counts from the later of `created_at` and
  `available_at`.
- **Failed sends:** a drain stops at the first failed send and records its error in `last_error`. Only a `Sweep`
  counts it as an attempt: a `Flush` runs on every request, and during a Pub/Sub outage of a minute fifty requests
  would otherwise set the head row aside. A send the transport rejects for good (`events.Rejected`: a key no topic
  takes, an ordered event without a user, Pub/Sub's 400, 403, 404 or 413) sets the row aside at once, from either,
  with `last_error` beginning `rejected, set aside:`, and the rows after it go in the same drain.
- **Stuck rows:** a row is set aside after 50 failed sweeps (about 12 hours of ticks) or one rejection
  (`ryft_outbox_stuck`, `OutboxStuck`).
- **`Run`:** it loops `Sweep` every 2 s, for tests and local tools only.

## Replay

`scripts/dlq.sh`, rewritten on `gcloud` (ticket A1-5; usage in `OPERATIONS.md`), pulls from the hold subscription
without `--auto-ack`. It republishes each letter to its original topic with `ryft-replayed=true`, then acks it. D150's
rule still holds:
- only the keys the DLQ's consumer handles are replayed;
- only a letter whose key is its topic's is replayed, so a forged key is never sent on by the operator.

A replayed letter is a redelivery, with all 20 attempts again.

## Local development

`make up` runs the same runtime as Cloud Run, not a second mode:
- compose runs the Pub/Sub emulator (the gcloud emulator image);
- `deploy/pubsub-emulator/init.sh` creates the topology, with push endpoints `http://<service>:8080<path>` and the same
  retry policy and attempts;
- a ticker publishes `{}` with `ryft-key=tick` to `ryft-tick` every 60 s;
- the services run with `PUBSUB_EMULATOR_HOST` and `ALLOW_INSECURE_PUSH=true`, with inline flushes and ticks as on
  Cloud Run. `GCP_PROJECT` defaults to `ryft-local`.

`config.Load` refuses `ALLOW_INSECURE_PUSH=true` and `PUBSUB_EMULATOR_HOST` when `K_SERVICE` or `CLOUD_RUN_JOB` is set.
Integration tests read `RYFT_TEST_PUBSUB_EMULATOR_HOST`. CI runs the emulator with host networking, so a push reaches
a test's `127.0.0.1` server.

**What the emulator proves:** it does push and pull, ordering, forwarding to dead-letter topics and retry policies.
So the full path runs locally, DLQ included, and a CI test runs publish → push → nack → dead letter against it.

**What it doesn't prove:**
- **IAM.** The topic matrix is pinned by `infra/gcp`'s Terraform tests.
- **OIDC.** Push auth is off locally. The check is pinned by Go tests with signed tokens (`internal/auth`).
- **Retention.** The emulator keeps everything and loses it all on a restart.
- **Production's delivery behaviour:** push backoff versus the retry policy, `deliveryAttempt` on push, a saturated
  instance's 429s.
- **The network path:** the regional endpoint, ingress `internal`, the `run.app` audience.
- The status mapping is pinned by Go tests with recorded envelopes, not by the emulator. Whether the emulator accepts
  `init.sh`'s `messageRetentionDuration` and `retryPolicy` is for A1-6's emulator test to show.

## Operating it

`OPERATIONS.md`: dead letters (`scripts/dlq.sh`), the outbox, and the GCP runbook. `OBSERVABILITY.md`: the metrics
and the alerts (`TickMissing`, `OutboxBacklog`, `OutboxStuck`, `DeadLetters`, `DeadLetterPublishFailing`,
`PushBacklog`).

## Revisit if

- Sustained traffic makes per-user ordering on push a bottleneck. Push suits keys that publish rarely, and a user is
  one (a few syncs a day).
- A consumer needs the event history as data.
- Exactly-once becomes a need (an idempotency table, D100).
- The 15-minute tick's precision starts to matter for notify (the second Scheduler job above).
