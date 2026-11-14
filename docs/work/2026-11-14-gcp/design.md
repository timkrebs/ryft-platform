# RYFT on GCP serverless: the design (2026-11-14)

Status: **accepted by the user on 2026-11-14** (§13 records the answers). Docs only, nothing built, nothing applied. The
decisions are "proposed D216 and up" (§12) and go into `DECISIONS.md` with PR A. #161 is merged (`8389feb`), so D215 is
the last number on main.

**Why.** Since 2026-11-14 an SCP of the HashiCorp org (`p-emcptm8t`) denies `eks:CreateCluster` in account
173003892479. EKS cost about $400–450 a month, and that cost caused the shutdown on 11-10 (D206). The user decided on
2026-11-14 to move to GCP serverless: "für eine pre-alpha absolut ausreichend". The target is a few euros a month for
about ten testers. The site is back in the pre-alpha (the user's follow-up 2): the legal pages load before any tester
signs in (§8).

**Unchanged:** Supabase (Postgres and Auth, Frankfurt), Grafana Cloud, the Apple keys, the domain `ryftlab.de`
(D213, D214), the contract's shapes (`docs/API.md`), the outbox (D101) and idempotent handlers (D100).

**HCP Terraform:**
- Everything new runs in the org **`ryft-lab`** (`org-aus6gvWumndxZixQ`), the user's own account. Workspaces are
  CLI-driven with remote execution and GCP dynamic credentials (§11 step 2).
- The old org `tim-krebs-org` is reached only by putting `TF_TOKEN_app_terraform_io='<old token>'` in front of a single
  command. Every such step is marked **old org**.

**GCP project:** `ryftlab-prod`, no organization, billing on.

**Checked on 2026-11-14 (B0).** Sources are listed in §15. Marks in the text:
- no mark: confirmed against Google's, Supabase's, GitHub's or Grafana's docs or price pages, through search results or
  fetched pages;
- *(search)*: confirmed only by a search snippet of the official page, whose body the fetch couldn't read;
- *(unchecked)*: still unconfirmed.

---

## 0. The target in one table

| Today (EKS) | On GCP | Section |
|---|---|---|
| `api`, `ingest` Deployments behind Traefik on one host | Cloud Run services, public, scaling to zero, `api.ryftlab.de` and **`ingest.ryftlab.de`** | §1, §3 |
| `scoring`, `notify` consuming RabbitMQ | Cloud Run services, **ingress internal**, IAM-only, invoked by Pub/Sub push with OIDC on their `run.app` URL | §2, §5 |
| `migrate` PreSync Job, PostSync smoke | Cloud Run Job `migrate`, run by CI before the deploy; CI runs the smoke after it | §9 |
| Tickers in api, scoring, notify; the outbox relay's poll; `db.Watch` | one Cloud Scheduler job → topic `ryft-tick` → `POST /internal/tick` on each service | §3 |
| Vault CE + VSO | Secret Manager, one service account per service, secret-level IAM | §4, §5 |
| RabbitMQ (D97–D102) | Pub/Sub: one topic per event key, push subscriptions, a dead-letter topic per consumer | §2 |
| Argo CD + the promote PR (D94–D96) | GitHub Actions `deploy.yml` with Workload Identity Federation, no JSON keys | §9 |
| GHCR | Artifact Registry `europe-west1-docker.pkg.dev/<project>/ryft` | §9 |
| cert-manager, ExternalDNS, admission, NetworkPolicies, Alloy | gone. Google-managed certificates on the domain mappings; Terraform owns DNS | §5, §6 |
| Route 53 zone `ryftlab.de` (M365 records) | Cloud DNS zone, the same records | §11 |
| `web` nginx pod (not deployed, D215) | the whole static export on **Firebase Hosting** in a separate project **without billing** (`ryftlab-site`, Spark), on `https://ryftlab.de`, `www` redirecting | §8 |

---

## 1. Region: **europe-west1** (the user's decision, 2026-11-14)

The user first said Frankfurt, then chose west1 once he saw the cost.

| | europe-west3 (Frankfurt) | europe-west1 (St. Ghislain, BE) |
|---|---|---|
| RTT to Supabase eu-central-1 *(estimate, unmeasured)* | ~1–2 ms | ~6–9 ms |
| A sync: about 20 round trips (auth lookup, upserts by batch, queued days, outbox row, commit, inline relay) *(counted from the handler, roughly)* | +~30 ms | +~150 ms |
| Scoring one ingested event: dozens to a few hundred queries | baseline | +0.3–1.5 s |
| Cloud Run price tier | **Tier 2**: request-based $0.0000336/vCPU-s, $0.0000035/GiB-s, 1.4 × Tier 1 *(search)* | **Tier 1**: $0.000024/vCPU-s, $0.0000025/GiB-s, $0.40 per million requests *(search)* |
| Free tier | per billing account, applied as a spending discount at Tier 1 prices: 180k vCPU-s, 360k GiB-s, 2M requests a month (us-central1 pricing, checked by head-architect; *(search)* for the discount rule) | the same |
| Custom domain | **no Cloud Run domain mapping** here. Mappings exist only in asia-east1, asia-northeast1, asia-southeast1, europe-north1, **europe-west1**, europe-west4, us-central1, us-east1, us-east4, us-west1. The alternatives are a global external Application Load Balancer (~$18 a month for its forwarding rule *(unchecked)*) or `*.run.app` URLs in the app | domain mapping: **Preview**, "not production-ready", "not supported at General Availability"; free; Google-managed certificate |

**Why west1:** it is the only free way to `api.ryftlab.de`. A sync gets about 0.15 s slower, and scoring a few days
gets about a second slower. Both stay inside the sync-to-score target (p95 2 min, `SyncToScoreSlow`). A load balancer
alone would cost several times the rest of the bill.

**What the mapping docs say:**
- It maps a domain only to `/`, not to a path.
- The certificate takes about 15 minutes, up to 24 hours.
- TLS 1.0 and 1.1 can't be disabled (a gap, §5.2).
- Domain ownership must be verified.
- Cloud Run generates the A, AAAA or CNAME records.
- Whether HTTP/2 end to end works through a mapping isn't stated *(unchecked)*. Runbook step 9 tests it with a 33 MiB
  body.

**Two hosts, because a mapping can't route by path:** a domain mapping points one host at one service ("only to
`/`"), so `/v1/sync → ingest` on `api.ryftlab.de` (Traefik today) has no equivalent. `docs/API.md` → Base URLs already gives the
app a separate ingest URL ("leave the ingest URL empty"). The new build sets `https://api.ryftlab.de` and
`https://ingest.ryftlab.de`. That is a documentation change to the contract, not a shape change. api-contract-owner
writes it in PR A. The app needs a new build for D213 anyway.

**Revisit if** any of these happens:
- Domain mappings fail (their Preview "latency issues"), or are withdrawn. Then move to europe-west3 or stay in west1,
  either way behind a load balancer (~$18 a month *(unchecked)*).
- Supabase moves.
- Scoring latency starts to matter (a rescore of many days).

**Everything in the EU:**
- Pub/Sub topics get `message_storage_policy` = europe-west1.
- Secrets use user-managed replication in europe-west1.
- Artifact Registry, Scheduler and the Cloud Run jobs are in europe-west1.
- Logs go to a regional bucket (§6).
- The exception is the site: Firebase Hosting serves the static pages from a global CDN. The pages hold no personal
  data, and visitors' IPs pass through Google's edge (§8).

---

## 2. Events on Pub/Sub

### 2.1 Topology: one topic per key, so IAM is D99's matrix

Pub/Sub permissions are granted per topic, not per attribute. One topic per routing key keeps D99's rule ("ingest
cannot publish `session.completed`") without a broker.

| Topic | Publisher SA (`roles/pubsub.publisher` on this topic only) | Subscription → push endpoint | Ordering key |
|---|---|---|---|
| `ryft-ingested` | ingest, api | `scoring-ingested` → `https://<scoring>/push/ingested` | `user_id` |
| `ryft-session-completed` | api | `scoring-session-completed` → `/push/session.completed` | `user_id` |
| `ryft-scored` | scoring | `notify-scored` → `https://<notify>/push/scored` | — |
| `ryft-session-scored` | scoring | `notify-session-scored` → `/push/session.scored` | — |
| `ryft-refresh` | scoring | `notify-refresh` → `/push/refresh` | — |
| `ryft-notify-wait` | notify | `notify-wait` → `/push/wait` (attribute `ryft-key` ∈ NotifyKeys) | — |
| `ryft-tick` | the Cloud Scheduler service agent | `tick-api`, `tick-ingest`, `tick-scoring`, `tick-notify` → `/internal/tick` | — |
| `ryft-dlq-scoring`, `ryft-dlq-notify` | the Pub/Sub service agent (dead-lettering); scoring and notify for their own bad events | pull `dlq-scoring-hold`, `dlq-notify-hold` (the operator only) | — |

**On the wire:**
- `data` is the JSON body exactly as today (`events.Message.Body`).
- Attributes:
  - `ryft-key`
  - `ryft-message-id` (today's `Message.ID`, still not a dedup key, D100)
  - `ryft-reason`
  - `ryft-replayed=true` on a replay
  - `ryft-error` only on a letter a consumer dead-letters itself
- The subscription's endpoint path sets the key the handler checks. Publishers can't change it. `ryft-key` must be
  absent or equal to that key; otherwise the message is a bad event and is dead-lettered under the path's key. This
  keeps today's "its `type` must be empty or the same".
- Topology is defined once: Go (`internal/events/pubsub/topology.go`). `go run ./cmd/pubsub-topology` writes
  `infra/gcp/pubsub.auto.tfvars.json` and the emulator's init file. A check fails when the outputs differ (this replaces
  `cmd/rabbitmq-topology`).

### 2.2 Delivery

| Need | Today (RabbitMQ) | Pub/Sub |
|---|---|---|
| Push auth | mTLS + per-user password (D98) | push with an OIDC token of SA `ryft-push`. Each subscription pushes to the service's **`run.app` URL** and sets `oidc_token.audience` **explicitly** to it. Scoring and notify use **ingress `internal`**: Pub/Sub and Cloud Scheduler in the same project are admitted when they use the default `run.app` URL, not a custom domain. Cloud Run IAM admits only `ryft-push` (`roles/run.invoker`) to them. The Pub/Sub service agent needs `iam.serviceAccountTokenCreator` on `ryft-push` *(search)*. **On all four services** the app verifies the token on every `/push/*` and `/internal/*`: RS256 only; `exp` required; iss `https://accounts.google.com`; `aud` equal to the configured URL (never the `Host` header); `email` = `ryft-push@…` with `email_verified`; and `sub` = that SA's unique ID. On api and ingest (public) this check is the only gate. On scoring and notify it is the backstop if IAM drifts. It reuses only `internal/auth`'s JWKS keyfunc, with Google's JWKS: the Supabase parser hard-codes `aud=authenticated` and its role checks. `Authorization` is never logged. A token isn't bound to the body and lives an hour, so a captured token could replay or inject events for that hour. D100 makes a replay harmless, and an injected event only triggers a rescore or a push the user would have got anyway |
| Ack | `basic.ack` | status **102, 200, 201, 202 or 204**; any other code is a nack. The handlers answer 204. Wrapped envelope (the default; "unwrapped" isn't used): `message.data`, `attributes`, `messageId`, `publishTime`, `orderingKey`, `deliveryAttempt` |
| Ack deadline | prefetch + consumer drain | allowed 10–600 s. Scoring 300 s (= its Cloud Run request timeout), notify 60 s, ticks 600 s |
| Retries | TTL tiers 5 s / 30 s / 5 min, 6 attempts (~16 min) | retry policy: min backoff 10 s, max 600 s (the allowed range is 0–600 s), exponential. **For push, Pub/Sub may use its push backoff (100 ms to 60 s, exponential) instead of the retry policy.** So the spacing isn't ours to fix, and `max_delivery_attempts = 20` (allowed 5–100) keeps the span to the DLQ at roughly 10 min under push backoff and well over 20 min under the retry policy. The count is approximate (best effort), so a message can arrive more often than configured |
| Dead letters | `ryft.dlx` → `dlq.*`, 14 days, headers `Ryft-*` | the dead-letter policy forwards to `ryft-dlq-<consumer>`, which must be a different topic from the source. The hold subscription keeps letters **14 days** (a subscription allows 10 min to 31 days; no topic retention needed). Delivery attempts are counted only when the dead-letter topic has the right IAM. Pub/Sub adds `CloudPubSubDeadLetterSource*` attributes, but it can't add our `Ryft-Error`: the last error is in the log, keyed by `messageId`. The Pub/Sub service agent needs `publisher` on the DLQ topic and `subscriber` on the source subscription |
| Bad or undecodable event | straight to the DLQ | the handler publishes it to its own DLQ topic with `ryft-error`, then acks. If that publish fails: 500, so the retry policy dead-letters it anyway (`DeadLetterPublishFailing`) |
| `events.Permanent` | dropped and counted | 204, counted as dropped |
| Redelivery (`events.Redelivered`) | tier copy, dead consumer, replay | `deliveryAttempt > 1` or `ryft-replayed=true` → `events.WithRedelivery(ctx)`. `deliveryAttempt` is set when dead-lettering is enabled and nil otherwise (Google's pull sample; for push *(unchecked)*). Every event subscription has a dead-letter policy, and a missing field counts as attempt 1 |
| Deferral (notify quiet hours) | wait tiers 1 m / 15 m / 1 h, not an attempt | Pub/Sub has no per-message delay, and backing off counts as attempts. notify acks and writes an **outbox row with a not-before time** (`publisher = notify`, topic `ryft-notify-wait`, new column `outbox.available_at`). The additive migration also grants `ryft_notify` the outbox, which 0026 grants only to ingest, api and scoring, with an RLS policy limited to `publisher = 'notify'`. The tick relays due rows. Precision is the tick: a push at the end of quiet hours can be up to 15 min late. The knob is a second Scheduler job every 5 min for `tick-notify` (still free, §7) |
| Idempotency | D100 | unchanged and still required. Pub/Sub delivers at least once, a message can arrive again even after an ack, and exactly-once delivery isn't available for push |
| Ordering | oldest first from the outbox, one consumer (D17) | the relay already publishes oldest first and stops at the first failure. Scoring's two topics carry `ordering_key = user_id` with `enable_message_ordering`, For push, Pub/Sub "supports only one outstanding message for each ordering key at a time", so it holds a user's later messages until the current one is acked or dead-lettered. Ordering needs every message with the same key published "in the same region". The relay therefore publishes to the regional endpoint `europe-west1-pubsub.googleapis.com`. Two caveats from the docs: with a dead-letter topic as well, order "might not be" kept, because forwarding is best effort; and push suits keys that publish rarely, which a user is (a few syncs a day). Not ordered: across the two topics, and notify (as today, "not needed": `MESSAGING_DESIGN.md` → Guarantees). D17 becomes "scoring max-instances 1, one message in flight per user" with concurrency 4. Concurrent users are safe, as the timer and the consumer already run concurrently today |
| Saturation | a queue absorbs it | a busy scoring instance answers 429, and a 429 counts as an attempt. 20 attempts, concurrency 4 and per-user ordering make a DLQ from load unlikely at this scale. `PushBacklog` alerts first (§6) |
| Replay | `scripts/dlq.sh replay` | `scripts/dlq.sh` rewritten on `gcloud pubsub subscriptions pull` (without `--auto-ack`): republish to the original topic with `ryft-replayed=true`, then ack. Same rule as D150: only keys the consumer handles, and only when the letter's key is its topic's |

### 2.3 Code: what changes, behind which interface

- **`internal/events`.** `Sender`, `Message`, `Publisher` and the encode/decode functions stay. In `Bus`, the blocking
  `ConsumeIngested` and `ConsumeNotify` are replaced by push handlers: `events.Handler` becomes a `func(ctx, key, data)
  error` that the transport calls. Error semantics (`Permanent`, `Defer`, `Guarded`, `WithRedelivery`) stay exactly as
  they are.
- **`internal/events/pubsub` (new, stdlib only, no Google client library).** `Sender` publishes over REST
  (`topics/<t>:publish`), with an access token from the metadata server, or to `PUBSUB_EMULATOR_HOST` without a token.
  `PushHandler(consumer, keys, fn)` is an `http.Handler`: OIDC check, envelope, key check, dispatch, status mapping
  (§2.2). Topology lives in `topology.go`.
- **`internal/events/outbox`.**
  - Inline mode: on Cloud Run, `Kick()` drains synchronously with a 2 s budget before the handler answers, because
    nothing runs after the response.
  - `Sweep(ctx)` for the tick: rows from a died instance, failed publishes, due deferrals, `observe` and `clean`.
  - `available_at`.
  - The relay's `Run` loop stays for local and tests.
- **`internal/eventbus`.** `Open` returns the Pub/Sub sender. `internal/events/amqpbus`, `cmd/rabbitmq-topology`,
  `deploy/rabbitmq` and `amqp091-go` are deleted.
- **`internal/app`.**
  - A Cloud Run runtime: Consumers become push routes, and Jobs become `Ticks []Tick` that `/internal/tick` runs in
    order with a budget.
  - No `db.Watch`, and no `:9090` listener (Cloud Run routes one port).
  - `Grace` 10 s (Cloud Run gives "a 10 second period" between SIGTERM and SIGKILL), with the D169 budget test
    re-pinned.
- **`internal/auth`.** `GoogleOIDC` verifier (issuer, audience, the expected SA).
- **`internal/httpx`.** h2c (§3), `ClientIP` for Google's front end (§5), `slog` keys for Cloud Logging (§6).
- **`internal/metrics`.** An OTLP/HTTP JSON exporter, stdlib only, built on the existing Prometheus registry's
  `Gather()` (§6).
- **`cmd/*`.** Each `main` picks the runtime. Scoring and notify serve HTTP, but only `/push/*` and `/internal/tick`,
  and they stay private: D17's "no user HTTP" holds.

**Locally, `make up` keeps working.** compose replaces RabbitMQ with the **Pub/Sub emulator** (the gcloud emulator
image). An init container applies the generated topology, with push endpoints `http://scoring:8080/push/…`. Push auth
is off only under the same explicit insecure flag as apikey mode (`ALLOW_INSECURE_PUSH=true`, refused when
`K_SERVICE` is set, which it is on Cloud Run).

The emulator supports push and pull, ordering, forwarding to dead-letter topics, and retry policies. It doesn't do IAM
or configurable retention (it keeps everything), and messages are lost on restart. So:
- the full path runs locally, DLQ included;
- the topic IAM matrix is pinned by a Terraform test, not the emulator;
- the handlers' status mapping is pinned by Go tests with recorded envelopes;
- a CI job runs publish → push → nack → dead letter against the emulator.

Handler tests don't need a broker any more.

---

## 3. Limits on Cloud Run

**Request size.** Cloud Run's quotas page: "Maximum HTTP/1 request size 32 MiB … No limit if using HTTP/2 server", and
the same 32 MiB for HTTP/1 responses that aren't chunked or streamed. `POST /v1/sync` allows a 32 MiB **body**
(`httpx.MaxBodySync = 32 << 20`), so a full-size sync plus its headers would be refused by Google's front end with an
HTML 413. **Decision: HTTP/2 end to end** (`--use-http2`) on all four services. Go 1.24's
`http.Server.Protocols` with `SetUnencryptedHTTP2(true)` serves h2c with prior knowledge from the stdlib, so there's
no new dependency, and HTTP/1 stays on for local use. The contract stays as it is. The mapping docs don't say whether
HTTP/2 end to end holds through a mapping *(unchecked)*, so runbook step 9 sends a 33 MiB body to ingest through the
domain mapping, which must get *our* JSON 413. If it doesn't, the fallback is a contract change (a 31 MiB cap,
api-contract-owner), not a load balancer. Cloud Run's own limits are far away: requests can last 60 min, jobs 168 h,
and a service takes up to 1,000 concurrent requests per instance.

**Timeouts.**

| Service | Request timeout | Concurrency | Memory | Max instances | Why |
|---|---|---|---|---|---|
| api | 60 s | 40 | 512 MiB | 3 | the import's 50 s budget (D140) |
| ingest | 120 s | 4 | 1 GiB | 2 | 4 × 32 MiB bodies, decoded |
| scoring | 300 s | 4 | 512 MiB | **1** | D17 as amended in §2.2; tick budget 240 s |
| notify | 60 s | 10 | 256 MiB | 1 | APNs calls |
| `migrate` job | 600 s task timeout | — | 512 MiB | 1 task | migrations take a lock timeout of 5 s each |

All services run 1 vCPU, request-based billing, **`min-instances 0`** (the user's decision), and startup CPU boost.
With request-based billing, "CPU is allocated when instances are processing requests", and CPU outside requests needs
instance-based billing, which isn't used.
The services' own `WriteTimeout`s (60 s) stay at or below the Cloud Run timeout.

**Cold starts** *(unmeasured)*: the distroless Go binary takes ~0.3 s, the pool's first TLS + SCRAM login to the
pooler ~0.1 s, Supabase's JWKS ~0.1 s, and `WaitMigrated` is one query. So expect roughly 0.5–1.5 s on the first
request after idle. The app already retries 503s. The synthetics (§6) hit api and ingest every 10 min, which keeps
them warm part of the time at no extra cost. `min-instances 1` isn't used: it needs instance-based billing to do any
work outside requests, and it would cost several euros a month *(unchecked)*.

**Every background goroutine and ticker, with request-only CPU:**

| Where | Today | On Cloud Run |
|---|---|---|
| api `accounts.reconcileEvery` (15 min) | goroutine | tick |
| api `confirmGuard.Run` (hourly) + its `Refresh` at start | goroutine, plus a Supabase call during startup | tick, plus a lazy first `Refresh` on the first invite request, so it's off the cold start path. Until then a bound invite is refused, fail closed as today |
| api `purgeInviteEmailsEvery` (hourly) | goroutine | tick (idempotent, so every 15 min is fine) |
| outbox relay of api, ingest, scoring (kick + 2 s poll) | goroutine | inline drain in the request (§2.3), plus `Sweep` in the tick |
| scoring `every(15 min, pass)` + housekeeping | job | tick with a 240 s budget. `learner.tried`, `catchUp.failed` and `catchUp.next` lived in memory and are lost between instances: a failing user is retried every tick instead of once a day, and a pass starts from the first user. Harmless at ten users. A small `job_state` table would fix it if the budget ever runs out |
| notify `every(15 min, staleCheck)` | job | tick |
| `db.Watch` (30 s ping) in every service | goroutine | gone. The tick pings and sets `ryft_db_up`, and the synthetics probe `/health/ready` from outside (§6) |
| `httpx.ServeMetrics` `:9090` | listener | gone. OTLP push at the end of each tick and on SIGTERM (§6) |
| RabbitMQ consumers | goroutines | push handlers |
| `app` start watcher, watchdog, readiness goroutines | goroutines | stay. They run during startup and shutdown, when CPU is allocated |
| api insight cache, ingest's `expectedEvery` cache, the rate limiter | per process | per instance. A cold instance starts empty (§5: the limiter) |

**Health paths.**
- Cloud Run's known issues: you can't use paths starting with `/_ah/` or "some paths ending with z", and should avoid
  every path ending in `z`. Users report Google's own 404 on `/healthz`.
- So the services also answer **`GET /health/live`** and **`GET /health/ready`** (the same handlers). That's an
  additive line in `API.md`'s health row, in PR A.
- The probes, the synthetics and the runbook use the new paths. `/healthz` and `/readyz` stay for local use and
  compose.
- `/health/ready` on a public service must not show dependency errors to the internet. Today it returns `err.Error()` (`httpx.go`). On Cloud Run it answers a generic
`503`, logs the cause, and caches the result for 5 s, because every hit costs a pooler round trip.

---

## 4. Database

**Pooler.**
- The services use Supavisor **transaction mode (port 6543)**. Cloud Run's churn (cold starts, scale-out, SIGTERM)
  makes many short-lived clients, and transaction mode takes a server connection only for each transaction.
- Supabase: the shared pooler "doesn't support prepared statements in transaction mode"; turn them off in the driver.
  Session mode supports them. pgx's default mode caches named prepared statements.
  - `DATABASE_URL` gets `default_query_exec_mode=exec`, pgx's unnamed statement, sent with its bind in one round
    trip. The code doesn't change.
  - Whether Supavisor accepts pgx's unnamed statements is *(unchecked)*. PR A proves it with `make smoke-supabase`
    against port 6543.
  - If `exec` fails there, the fallback is `default_query_exec_mode=simple_protocol`: client-side parameters, which
    the store's tests must cover first.
- The advisory locks are transaction-level (`pg_advisory_xact_lock`, `store/locks.go`), so they're safe.
- PR A greps for session state (`SET` without `LOCAL`, `LISTEN`, temp tables) and runs the store's integration tests
  against PgBouncer in transaction mode in CI.
- **`migrate` stays on session mode (5432).**
- **TLS is verified.** This link now crosses the internet from Google to AWS, carrying health data and the SCRAM login,
  so `sslmode=require` (which doesn't verify, `SUPABASE.md` → Connections) becomes
  `sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt`. Supabase's CA certificate is baked into the image; it
  is public.
- Supabase: "The shared pooler is IPv4-only on every plan, in both session and transaction mode", on ports 5432
  (session) and 6543 (transaction). Direct connections are IPv6 unless the project buys the IPv4 add-on. Cloud Run's
  default egress is IPv4, so the services use the pooler.

**Instance limits.** Clients at the maximum: api 3 × 4 + ingest 2 × 4 + scoring 1 × 2 + notify 1 × 2 + migrate 2 =
**26**. Supabase's Nano and Micro sizes allow 60 direct connections, 200 pooler clients and a default pool size of
15. The pool size counts only open transactions in transaction mode, so 26 clients fit easily. `max-instances` is
what keeps the client count bounded. The runbook checks the project's numbers (§11 step 7).

**Per-service roles without Vault.**
- The roles stay: `ryft_api`, `ryft_ingest`, `ryft_scoring`, `ryft_notify` (migration 0009 and later), each with only
  its grants and the RLS policies.
- Passwords: one Secret Manager secret per role (`db-password-<svc>`), plus `db-password-owner`.
- `DATABASE_URL` is plain configuration *without* a password. The password comes as `PGPASSWORD` from the secret, and
  pgx reads `PGPASSWORD` when the URL has none, so the code doesn't change.
- The `migrate` job gets the owner password and the four role passwords as the variables it reads today. It sets
  SCRAM verifiers, as now.
- Only the job's SA and each role's own service SA can access a role's secret.
- Values are made by the user (`openssl rand -base64 32 | gcloud secrets versions add … --data-file=-`). They never
  pass through Terraform state, a terminal or argv.

**Every secret is pinned by version number, never `latest`.** In `deploy/run/*.yaml`, `latest` would be read at every
cold start. One `versions add` would then change a running revision with no deploy: the next cold start of api would
get a new data key, and the next cold start of a service would log in with a password Postgres doesn't know yet.
Failed logins trip Supavisor's circuit breaker for the **whole role** (`ECIRCUITBREAKER`, `SUPABASE.md`), not just one
instance.

**Rotation is one commit.** `migrate` sets every role's password from `DB_PASSWORD_<SVC>` on **every** run
(`cmd/migrate/main.go`). So if `deploy/run/migrate.yaml` and `deploy/run/<svc>.yaml` pinned different versions, the
next deploy would set the role back while the service runs on the other version, and the breaker would trip for the
whole role.
1. Add version N+1 (`gcloud secrets versions add`; note the number it prints). Nothing reads it yet.
2. **One commit** moves the pin from N to N+1 in both `migrate.yaml` and `<svc>.yaml`. `scripts/rotate_db_password.sh`
   writes it, and a CI check fails when the two disagree.
3. The merge's `deploy.yml` does the rest in its usual order: `migrate` runs `ALTER ROLE` with the SCRAM verifier,
   then the service rolls to a revision on N+1. Old instances keep their open connections and drain.
4. If `ECIRCUITBREAKER` appears, wait it out; never retry in a loop.
5. After a day, disable version N.

Between the migrate step and the roll, old instances can't open new connections. Expect seconds to a minute, which is
acceptable at pre-alpha.

---

## 5. Security

### 5.1 What each layer gave us and what replaces it

| Layer | It gave | Replacement on GCP | Left over |
|---|---|---|---|
| **Vault** (D186–D211) | a KV per service, Kubernetes auth, generated values, audit devices | Secret Manager. Each service has its own SA (`ryft-api@`, …), with `secretmanager.secretAccessor` granted **on each secret**, never at project level. Cloud Run injects secrets as env at instance start, **pinned by version number** (§4). Audit: Cloud Audit Logs. Admin Activity is always on; **Data Access logs for `secretmanager.googleapis.com` are turned on** (tiny volume). Values are added by the user, not generated in state | Anyone with project Owner reads every secret (as Vault's root did). So does `hcp-terraform-apply`: it can grant itself `secretAccessor`, so the HCP Terraform workspace counts as secret access. No dynamic secrets (we had none) |
| **Admission** (D120, D180, D195) | Pod Security, image and namespace rules, Argo CD's scope | There are no pods to admit. Cloud Run sandboxes every container and can't run it privileged. **Deployer SA** `ryft-deployer` (WIF, §9): `run.developer` on the four services and two jobs, `iam.serviceAccountUser` on the five runtime SAs only, `artifactregistry.writer` on the one repository. It has no IAM rights, so it can't make scoring public. That rests on `run.developer` being unable to set `invoker-iam-disabled` *(unchecked)*, which is one more reason the in-app OIDC check runs on all four services. The **WIF condition uses IDs, not names**: `repository_id`, `repository_owner_id`, `ref == refs/heads/main`, `event_name == push`, `environment == deploy`, and `job_workflow_ref == timkrebs/ryft-platform/.github/workflows/deploy.yml@refs/heads/main`. So no other workflow on main can deploy, whatever `check_workflows.sh` says. Images are deployed by digest. **P5 (`p05-auth-env`) moves into the code:** `config.LoadAuth` refuses `AUTH_MODE=apikey`, `ALLOW_INSECURE_APIKEY_AUTH`, `API_KEY` and `USER_ID` whenever `K_SERVICE` or `CLOUD_RUN_JOB` is set (with a test), and a CI check rejects those names in `deploy/run/*.yaml` | **Binary Authorization isn't used** (its price on Cloud Run is *(unchecked)*). A compromised main or `deploy` environment can deploy any image, and the deployer could swap one runtime SA for another of the five. The service YAML in git is reviewed, and branch protection is the control |
| **NetworkPolicies** (D16, D185) | default deny; ingress only from Traefik; egress per service | **Ingress:** api and ingest public (`allUsers` invoker, ingress `all`). **Scoring and notify: ingress `internal`.** The docs admit Cloud Scheduler and Pub/Sub "in the same project or VPC Service Controls perimeter" when they use "the default `run.app` URL and not a custom URL or custom domain", so nothing from the internet reaches them at the network level. On top of that, only `ryft-push` may invoke, and the in-app OIDC check applies. The default `run.app` URL must stay enabled: disabling it stops Scheduler and Pub/Sub. **Service to service:** none. Only Pub/Sub calls scoring and notify | **Egress is unrestricted.** Limiting it needs Direct VPC egress, firewall rules and Cloud NAT (paid), and FQDN rules need Cloud NGFW (paid). Supabase's network restrictions can't be used either (egress addresses are dynamic) |
| **mTLS to RabbitMQ** (D98) and D99's rights | broker identity, per-key publish rights | Google API TLS + per-SA OAuth tokens. One topic per key keeps the publish matrix (§2.1). Nobody holds `subscriber` except the Pub/Sub agent and the operator (DLQ hold subscriptions) | — |
| **Traefik's limiter** (D155) | per-address limit keyed by the address Traefik appends | `httpx.ClientIP` takes the rightmost `X-Forwarded-For` entry. A Google developer-forum test shows Cloud Run appending the observed address last after a forged header *(forum report, not docs)*. Runbook step 9 sends a request with a forged `X-Forwarded-For` to both hosts from a known address: if Google appends a hop of its own, every client shares one bucket, and `ClientIP` has to skip it. Mappings can be A, AAAA or CNAME records, generated by Cloud Run, so IPv6 clients are likely. The admin limiter's map has no cap, which `adminauth.go` says it needs before IPv6 clients arrive, and one /48 is about 65k /64 keys. **PR A caps it** | **Limits are per instance** and reset at a cold start. The aggregate is bounded by limit × max-instances |

### 5.2 Honest gaps (free tier only), **accepted for the pre-alpha**

Accepted by head-architect's default on 2026-11-14; the user can still object.

- **No Cloud Armor, no WAF.** Google's front end gives basic DDoS protection only. Cost-DoS is bounded by
  `max-instances` and the budget alert (§7).
- **TLS 1.0 and 1.1 stay on** for the mapped hosts: a domain mapping can't disable them. The app speaks TLS 1.2+.
- **No organisation** (project `ryftlab-prod`), so no org policies: no resource-location constraint, and no
  `iam.disableServiceAccountKeyCreation`. The rule is "no SA keys" except one (next point), and the runbook checks it
  (`gcloud iam service-accounts keys list` per SA).
- **Grafana's Cloud Monitoring data source needs an SA key (accepted by the user).** Grafana Cloud doesn't run on GCE.
  The SA `grafana-reader` holds `monitoring.viewer` only (metrics, no data, no logs), and its key is stored only in
  Grafana Cloud and rotated yearly. This is the one JSON key. The user creates it by hand at runbook step 11, never
  in Terraform, because a key resource would put the private key in the state.
- **Logs at Google** include Cloud Run's request logs: client IP, user agent, path. They are kept in the EU bucket for
  14 days. The text in force doesn't name IP or user agent in its technical logs, so the privacy draft must (§8).
- **Unrestricted egress** makes the text's "each service … can reach only what it needs" false. The draft changes it
  to "can access only the data it needs" (§8).
- **Rate limits per instance** (above).

### 5.3 The data key (D211 → Secret Manager)

`scripts/recover_data_key.sh` is on main since #161. It reads the newest state version of `ryft-prod-euc1-vault` in
the **old org** and writes the base64 key to a 0600 file. The GCP path skips Terraform state entirely. The order is the
user's: 1Password first, Secret Manager after `infra/gcp`'s first apply.

1. **Old org:** run `TF_TOKEN_app_terraform_io='<old token>' scripts/recover_data_key.sh`. The script reads that
   variable before the credentials file, and its default org is `tim-krebs-org`. Note the version, fingerprint and
   length (must be 32).
2. Put the value into 1Password as the item "RYFT data key" (paste it from the file; don't print it in a terminal).
3. After `infra/gcp` apply 1, add it:
   `gcloud secrets versions add data-key --project=ryftlab-prod --data-file="$HOME/.config/ryft/data-key-api"`.
   The secret is in europe-west1, with `prevent_destroy` on its container and **delayed destruction**
   (`version_destroy_ttl = 30d`; Secret Manager allows 1 to 1,000 days). A destroyed version is then disabled and
   restorable for 30 days. That doesn't cover deleting the secret itself, which `prevent_destroy` and IAM guard.
4. Compare without printing, using the version number `add` printed, which `deploy/run/api.yaml` then pins:
   `gcloud secrets versions access <N> --secret=data-key --project=ryftlab-prod | shasum -a 256 | cut -c1-16`
   must equal the script's fingerprint. If it differs, stop.
5. Delete the local file.
   `rm -P` overwrites nothing on APFS, so a deleted file's blocks remain until reused. FileVault is what protects them.
   The Time Machine exclusion and the local-snapshot caveat from D211 still apply.

Only `ryft-api@` can access `data-key`. The revision pins its version (§4), so a mistaken `versions add` changes
nothing until a reviewed deploy. **The demo password** (`recover_data_key.sh --demo`) isn't a runtime secret:
`seed_demo.sh` uses it once, and its home becomes 1Password (App Store Connect's review login). D211's fallback, a new key with the one-off statement before the api starts,
stays as it is.

---

## 6. Observability

**Logs.**
- stdout JSON goes to Cloud Logging. `httpx.Logger` maps `level` → `severity` and `msg` → `message`, and adds
  `logging.googleapis.com/trace` from `traceparent` / `X-Cloud-Trace-Context`, so request logs and app logs join.
- A log sink sends everything to a **regional bucket `ryft-logs` (europe-west1, 14 days)**. 14 days is what the
  privacy text in force promises ("Logs: 14 days"). The global `_Default` sink is disabled.
- Ingesting logs costs $0.50/GiB after 50 GiB per project a month, and includes 30 days of storage *(search)*.
- Outside that bucket:
  - the `_Required` bucket: audit logs, a fixed 400 days, not charged *(search)*; its location *(unchecked)*;
  - Cloud Trace;
  - Error Reporting, which stays off.

  The privacy draft names the audit logs.
- Logs stay at Google. Grafana Cloud doesn't query or receive them, so the privacy text's "Grafana: metrics without
  personal data" stays true.
- Log-based alerting is the Cloud Logging console's job, and it is limited at pre-alpha (§13).
- D52 (CloudWatch) is superseded.

**Metrics: two sources, one Grafana.**
1. **Platform metrics, read where they are.**
   - Sources: Cloud Run request count by code class, latencies, instance count and billable time; Pub/Sub undelivered
     messages, oldest unacked age and dead-letter counts per subscription; Scheduler attempts.
   - Grafana Cloud reads them through the **Google Cloud Monitoring data source** (`grafana-reader`, §5.2).
   - System metrics are free *(unchecked)*. Monitoring API reads are free up to a first million a month, but the
     sources disagree on whether that counts calls or returned time series *(unchecked)*. Either way, Grafana-managed
     rules on this source evaluate every 5 min, not every 1 min: about 20 rules × 8,640 evaluations ≈ 170k reads a
     month.
2. **App metrics (`ryft_*`) pushed by the service.**
   - The format is OTLP/HTTP with JSON encoding to Grafana Cloud's OTLP gateway, built on the existing Prometheus
     registry (`Gather()`), stdlib only, no new dependency.
   - Pushed at the end of each tick (every 15 min, while CPU is allocated) and on SIGTERM.
   - Series carry `service.instance.id`. Rules use `increase()` over ≥ 30 min.
   - The token is one Secret Manager secret, `otlp-token`: a write-only token that the four service SAs can access.
   - Grafana's free metrics allowance (10k series *(unchecked)*) is far away.

**Traces.** None from the app at pre-alpha. Cloud Run's request logs and its automatic request spans in Cloud Trace
(their free allowance *(unchecked)*), joined by the trace field, are enough to follow a sync. Beyla and Tempo are
gone.

**Synthetics.** Grafana Synthetic Monitoring stays as code (#20), on the new hosts only: `https://api.ryftlab.de/health/ready`
and `https://ingest.ryftlab.de/health/ready` (§3), plus two site checks: `https://ryftlab.de/privacy` and
`/.well-known/apple-app-site-association` (status 200 and `application/json`). That's 4 checks every 10 min from 2
probes: about 35k executions a month. The free tier has 100k API test executions a month. The site checks fetch
~17k pages a month, roughly 0.5 GB of Spark's transfer quota (§8).

**Alerts.** The rules stay code. App metrics stay Prometheus-format rules in `deploy/grafana/alerts` (mimir ruler, the
same tooling). Cloud Monitoring signals become Grafana-managed rules provisioned by Terraform. The group limit (≤ 18)
still applies.

| Kept (source) | Rewritten on a new source | New | Gone |
|---|---|---|---|
| Panics, TextRefusedByDatabase, AuthUnavailable, RateLimited, SyncToScoreSlow, DeadLetterPublishFailing, PublishFailing, OutboxBacklog, OutboxStuck, ScoringErrors, NoSync, APNsFailing, PushFailing, APNsRejectsOurKey, AccountDeletionIncomplete, NewFeedback, AdminUnauthorized, AdminWebUsed, InviteEmailPurgeFailing (app OTLP) | HTTP5xxRatio and SyncSlow (Cloud Run metrics); DatabaseDown (the tick's `ryft_db_up`); DeadLetters (`dlq-*-hold` undelivered > 0, replacing RabbitMQDeadLetters too); **ApiDown** (the two API synthetics fail from every probe for 10 min; it reuses SiteDown's shape), the Site\* alerts on the two site checks (SiteDown also fires when Spark disables the site, §8), and SyntheticsMissing (for the four checks) | **TickMissing** (no tick handled by a service for 45 min); **PushBacklog** (oldest unacked > 15 min on a scoring or notify subscription, replacing RabbitMQBacklog); **Saturated** (Cloud Run 429s); the **budget** alerts come by email from Billing, not Grafana | PodRestarting, ServiceExited, DatabaseFlapping, CertificateExpiring, RabbitMQAlarm, RabbitMQNodeDown, AMQPDisconnected, EventsUnroutable (a publish to a missing topic fails → PublishFailing), all Argo\*, all Admission\*, all Vault\* |

---

## 7. Costs a month at pre-alpha load

**Assumed.**
- 10 testers, 4 background syncs a day each (1,200 syncs a month, ~1 MB each, 2 s of CPU).
- 50 api requests a day each (15,000 a month, 0.2 s).
- ~3 Pub/Sub deliveries per sync (3,600, 1 s), ~3,000 cold starts (1 s).
- Ticks: 4 services every 15 min = 11,520 requests (scoring ~2 s, the others ~0.2 s).
- Synthetics: the 2 API checks (`api.` and `ingest.` `/health/ready`, §6), ~17k requests at 0.1 s. The 2 site checks
  hit Hosting, not Cloud Run.
- Total about **25k vCPU-seconds, 12k GiB-seconds and 50k requests**.

Prices are list prices in USD, Tier 1 (europe-west1). Unmarked figures are confirmed on the pricing pages (§15,
2026-11-14).

| Item | Use | Free tier | List price above it | €/month |
|---|---|---|---|---|
| Cloud Run CPU | ~25k vCPU-s | 180k vCPU-s (request-based), per billing account | $0.000024/vCPU-s *(search)* | 0 |
| Cloud Run memory | ~12k GiB-s | 360k GiB-s | $0.0000025/GiB-s *(search)* | 0 |
| Cloud Run requests | ~50k | 2M | $0.40/M | 0 |
| Pub/Sub | ~10k messages, ~20 MB (1 KB minimum per request) | 10 GiB | $40/TiB | 0 |
| Cloud Scheduler | 1 job | 3 jobs per billing account | $0.10/job | 0 |
| Secret Manager | 11 secrets (5 DB passwords, Supabase secret key, `ADMIN_API_KEY`, Apple `.p8`, APNs `.p8`, `data-key`, `otlp-token`), 1 active version each; ~10k accesses (secrets resolve at instance start) | 6 versions, 10k accesses | $0.06/version per location, $0.03/10k accesses | ~0.30 |
| Artifact Registry | ~0.4 GB (5 images × 5 digests, cleanup policy) | 0.5 GB, shared by the billing account | $0.10/GB | 0–0.05 |
| Cloud DNS | 1 zone, < 1M queries | none | $0.20/zone (first 25), $0.40/M queries | ~0.20 |
| Cloud Logging | < 1 GiB | 50 GiB per project, 30 days' retention included | $0.50/GiB | 0 |
| Cloud Monitoring | system metrics; Grafana's reads every 5 min (~170k a month, §6) | system metrics free *(unchecked)*; reads *(unchecked: the sources conflict)* | *(unchecked)* | 0 |
| Egress to Supabase and APNs | ~1.5 GB | *(unchecked)* | ~$0.12/GB *(unchecked)* | ~0.20 |
| Budgets | 1 | *(unchecked whether free)* | — | 0 |
| Firebase Hosting (`ryftlab-site`, Spark, no billing account) | ~100 MB stored (estimate), ~1–2 GB out (testers' pages, the synthetics) | 10 GB stored; transfer 10 GB/month (Hosting's quota page) or 360 MB/day (the pricing page): the two official pages disagree | none: a Spark project has no bill, it stops (§8) | 0 |
| Grafana Cloud, HCP Terraform, Supabase | unchanged | — | — | unchanged |
| **Total GCP** | | | | **≈ €0.50–1** |

**Where it stops being free.** The fixed part (ticks, synthetics, cold starts) is ~12k vCPU-s, and each tester adds
~1.3k. The Cloud Run CPU free tier lasts to about **100–130 testers** at these habits. Requests and Pub/Sub come much
later. Secret Manager and DNS cost cents from day one. Other bills arrive outside that curve:
- a **load balancer** (~$18/month *(unchecked)*), if domain mappings fail (§1);
- **min-instances 1** anywhere (the user chose 0; its idle price is *(unchecked)*);
- europe-west3's Tier 2 (1.4 × Tier 1 *(search)*);
- a rescore of every account (unmeasured, an operator job);
- the site project, only if the user links a billing account to it (that turns it into Blaze, §8).

**Worst case:** all 7 instances (§3: api 3, ingest 2, scoring 1, notify 1) busy around the clock is ~18.1M vCPU-s and
~11M GiB-s, about **$460 a month** at Tier 1. Max-instances bounds it, and the budget emails long before.

**The budget (by hand, it exists).** The user made **"RYFT 50 EUR"** in the console: EUR 50 a month, alerts at 50 %,
90 % and 100 %. It stays the only budget, and Terraform doesn't own it (D226: no `budget.tf`, nothing with two
owners). Runbook step 3 adds a **10 % rule (€5), the early warning**:
```bash
BUDGET=$(gcloud billing budgets list --billing-account="$BILLING_ACCOUNT" \
  --filter='displayName="RYFT 50 EUR"' --format='value(name)')
echo "$BUDGET"   # one line: billingAccounts/<id>/budgets/<budget-id>
gcloud billing budgets update "$BUDGET" --add-threshold-rule=percent=0.10
gcloud billing budgets describe "$BUDGET" --format='yaml(amount,thresholdRules,budgetFilter)'
```
- `$BILLING_ACCOUNT` is the ID from `gcloud billing projects describe ryftlab-prod --format='value(billingAccountName)'`
  without the `billingAccounts/` prefix. `--add-threshold-rule` can repeat; a full budget name needs no
  `--billing-account`.
- `describe` must show `0.1, 0.5, 0.9, 1.0`. If `budgetFilter` names no project, the budget counts the whole billing
  account, which is fine while `ryftlab-prod` is its only project with a bill.
- The `gcloud billing budgets` commands need the Budget API (`billingbudgets.googleapis.com`) on the quota project
  *(unchecked)*, so it is on step 2's keep list.
- **The €20 stop line** is a runbook action, not a second budget. With
  `--add-threshold-rule=percent=0.40` it also sends its own email (optional, one more rule on the same budget). At
  €20 actual spend:
  ```bash
  for svc in api ingest; do
    gcloud run services remove-iam-policy-binding "$svc" --region=europe-west1 --project=ryftlab-prod \
      --member=allUsers --role=roles/run.invoker
  done
  gcloud scheduler jobs pause ryft-tick --location=europe-west1 --project=ryftlab-prod
  ```
  Then set `public_invokers = false` in `infra/gcp`'s variables before the next apply, which would otherwise add
  `allUsers` back, and find the cause in the billing report (SKU view) before undoing either.
- Budgets don't cap spending, and "the first notification may take several hours" (Billing docs). Max-instances is the
  hard cap for Cloud Run. The site scales with traffic too, but it sits in a project without billing, so its quota is
  the cap and needs no stop line (no `firebase hosting:disable`, §8).
- D55's "budget in USD" becomes EUR, the billing account's currency.

---

## 8. The site: the whole export on Firebase Hosting (PR C1)

**The user's decision (follow-up 2, 2026-11-14): the site is back in the pre-alpha.** The whole static export of
`web/` goes on Firebase Hosting at `https://ryftlab.de`. This replaces the deferred ticket F and its GitHub Pages
option. It brings back:
- the legal pages (`/privacy`, `/de/privacy`, `/terms`, `/support`, `/impressum`), so the consent screen's
  `privacy_policy_url`, the app's Terms, Privacy and Support links and App Store Connect's URLs load again;
- the AASA: universal links, password AutoFill, push links (`ryft.url`) and invite links;
- `/admin`, the operator's page.

REBUILD step 12's "open no gate and invite nobody" clears once `/privacy`, `/de/privacy` and `/impressum` return 200
(runbook step 10). Testers also wait for C2 to be in force, because the text in force still names AWS (§14). API.md
needs no change: the URLs are the ones it already documents.

**Everything in `web/` goes static.**
- `next.config.ts` is `output: 'export'` with no server runtime.
- The build reads `../app` (the tokens, the Swift-licence snapshot, the AASA test), which CI already checks out.
  Nothing calls the RYFT API at build time.
- `/admin` is static too: a client page that calls `api.ryftlab.de` and Supabase from the browser
  (`web/config/admin.json`), with its own CSP.

What nginx does that Hosting doesn't, and whether leaving it out breaks anything:

| nginx | On Hosting | Breaks anything? |
|---|---|---|
| `/healthz` | gone | no: nothing probes it after D3; the synthetics check `/privacy` and the AASA (§6) |
| `*.gz` → 404 (the precompressed copies for `gzip_static`) | not uploaded (`ignore`); Hosting compresses on its own | no |
| the access log without IP, user agent or referrer (`web/docs/LEGAL_CHECKLIST.md` S6) | we get no logs, because Cloud Logging isn't linked. Google's CDN processes the IP to serve the page | no, but the privacy text changes (C2) |
| `absolute_redirect off`, `charset` on the text types | Hosting's own behaviour | no |

**`firebase.json`** is generated by `web/scripts/firebase-json.mjs` from the same sources as the nginx snippets
(`scripts/csp.mjs`'s hashes), so the CSP stays one source. It holds:
- `"public": "out"` and `"cleanUrls": true`. `/privacy` serves `privacy.html`, and a request with `.html` gets a 301
  without it, which also covers `/admin.html` → `/admin`.
- **`trailingSlash` left unset, with #156's rule as an explicit redirect:**
  `{"regex": "^/(?P<p>[A-Za-z0-9][A-Za-z0-9/._-]*)/$", "destination": "/:p", "type": 301}`.
  - Unset, Hosting "only uses trailing slashes for directory index files", and the export has none below the root.
    So every other path ending in `/` stays a 404 and is never redirected.
  - `//host/`, `/\host/` and `/%5Chost/` can't match the pattern (#156).
  - `trailingSlash: false` would redirect *every* such path, and whether Hosting collapses `//` first is
    *(unchecked)*, so it isn't used.
- The app-link rewrite `^/(day|sleep|insights|train|auth|invite)(/.*)?$` → `/open.html` (`regex`, RE2).
- Headers per path class, as in nginx:
  - the shared set on everything: nosniff, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, HSTS
    `max-age=31536000; includeSubDomains`, COOP, CORP, `Permissions-Policy`;
  - the marketing CSP on everything except `/admin`;
  - on `/admin` only: the admin CSP, `no-store` and `X-Robots-Tag`;
  - `Cache-Control` as today: a year for `/_next/static/**`, a day for icons and images, `no-cache` for `/privacy`,
    `/de/privacy` and the 404, `no-store` for the app-link paths, 300 s for the other pages;
  - for the AASA: `Content-Type: application/json` and an hour's cache.
- `"ignore": ["**/*.gz"]` and nothing else. `firebase init`'s default list has `**/.*`, which would drop
  `.well-known/` and the AASA with it.

The docs give the order: reserved `/__/*`, then redirects, then exact files, then rewrites, then the custom
`404.html`. So the trailing-slash redirect runs before any file is looked up.

**C1's acceptance.** The tests are `web/tests/hosting.test.ts`, replacing `nginx.test.ts`, and `make web-e2e` against
the Hosting emulator (`firebase emulators:exec --only hosting`, firebase-tools pinned):
- Every response has exactly one CSP: the marketing one everywhere, the admin one on `/admin` only. Whether Hosting
  merges two matching `headers` rules or sends both is *(unchecked)*. If it sends both, the generator writes the
  marketing rule's globs so that they leave `/admin` out.
- `/privacy/` → 301 `/privacy`. `/x//`, `//evil.example/` and `/%5Cevil.example/` → 404 with no `Location`.
- The AASA is served as `application/json` without a redirect. Whether a `headers` entry may set `Content-Type` is
  *(unchecked)*, so the test proves it.
- The app-link paths serve `open.html` with `no-store`.
- After the deploy, the same assertions run against `https://ryftlab.de` (runbook step 10), and Apple's CDN
  (`https://app-site-association.cdn-apple.com/a/v1/ryftlab.de`) returns the served AASA.

**The site project `ryftlab-site`.** This is head-architect's default; the user can still choose `ryftlab-prod`, which
puts Hosting on Blaze under "RYFT 50 EUR".
- **No billing account, so the Spark plan.** "Linking a Cloud Billing account to your project will auto-upgrade your
  project to the Blaze pricing plan." So it never gets one.
- **The quota is the cap.**
  - Hosting's quota page: storage "at no cost up to 10 GB", transfer "up to 10 GB/month". The pricing page says
    360 MB/day; the two official pages disagree.
  - Over the transfer quota on Spark: "a short grace period but then your sites will be disabled. Your sites will
    remain disabled until the start of the next month."
  - Over the storage quota: no new deploys.
  - No bill comes either way, so there is no stop line (no `firebase hosting:disable`).
- **The price of the cap.** A flood of ~10 GB takes the legal pages offline until the 1st.
  - The API keeps working, and SiteDown fires (§6).
  - New consents and App Review wait, because REBUILD step 12's rule applies again.
  - The user then waits, or upgrades the site project to Blaze with its own budget. Nothing here does that
    automatically.
- **Custom domains on Spark.**
  - Firebase's pricing page lists "Custom domain & SSL" under Hosting, and search snippets of that page show it in
    the no-cost column *(search: the fetched page text shows the row without a value)*.
  - The custom-domain page names no plan restriction.
  - Runbook step 10 is the check: if the console asks for Blaze to connect `ryftlab.de`, stop.
- **Certificates.** "Firebase Hosting provisions an SSL certificate for each of your domains", which "may take up to
  24 hours after you point your DNS to Firebase Hosting".
- **`www`** is connected as a second custom domain, with the wizard's option to redirect to `ryftlab.de`. My call:
  typed `www.` links shouldn't fail, and it costs one more certificate.
- **Logs.** Hosting's Cloud Logging export is opt-in per site. It records `remoteIp` and the user agent and keeps them
  30 days. It stays **off**, so we keep no visitor logs, and S6 stays true for us.

**What the user creates** is in runbook step 10's commands (§11): the project, Firebase on Spark, the deploy SA and
its two bindings, and the custom domains in the console. All of it is owned by hand, not by Terraform. The DNS records
the wizard shows go into `infra/gcp`'s variables, because Terraform owns the zone.

**How CI deploys.** `deploy.yml` gets a `site` job in environment `deploy`. The GitHub pool's condition
(`job_workflow_ref` = `deploy.yml` on main, §5.1) already admits it, so no new pool is needed. It runs when `web/`
changed, after `web-build` and the brand check:
1. `google-github-actions/auth` (pinned by SHA) with the GitHub provider in `ryftlab-prod` and
   `service_account: site-deployer@ryftlab-site.iam.gserviceaccount.com`. It writes a short-lived credentials file;
   there is no key.
2. `npx --yes firebase-tools@<pinned> deploy --only hosting --project ryftlab-site --non-interactive`, from `web/`.
   The CLI "will detect and use Application Default Credentials if they're set". `FIREBASE_TOKEN` is "no longer
   recommended" and `login:ci` is deprecated, so there are no tokens.
3. The step-10 assertions against `https://ryftlab.de` (curl), and `check_policy_in_force.sh` reads the live
   `/privacy`.

Rollback is the console's release history (Hosting → Release history → Roll back), or a revert commit.

**CAA.** Today the zone allows only Let's Encrypt (`issue` and `issuewild "letsencrypt.org"`, D214). Both
certificate users ask for two CAs:
- Cloud Run: "If you use Certification Authority Authorization (CAA) DNS records for your custom domain, authorize
  both `pki.goog` and `letsencrypt.org` so that Cloud Run can issue and renew your managed TLS certificate."
- Firebase Hosting, for a domain stuck in "Pending": "Ensure that the certificate authorities `letsencrypt.org` and
  `pki.goog` are allowed to create SSL certs for your domain."

So the Cloud DNS zone carries this set at the apex, and `api.`, `ingest.` and `www.` inherit it:
```
ryftlab.de.  CAA  0 issue "letsencrypt.org"
ryftlab.de.  CAA  0 issue "pki.goog"
ryftlab.de.  CAA  0 issuewild "letsencrypt.org"
```
- `issuewild` stays as it is: neither service asks for a wildcard, and an `issuewild` record governs only wildcard
  requests.
- The set goes live with the NS switch (step 6), before the mappings (step 9) and the site's domains (step 10)
  request certificates.
- A certificate still pending after 24 hours is the stop signal.
- The site's records are the apex A records and the `hosting-site=` TXT, the same for `www`. Firebase says to remove
  any A, CNAME and AAAA records that point elsewhere; the apex has none today (D215).

**The privacy text (PR C2, a draft PR only, `status` left alone).**
- A new version, dated when the user decides, `status: draft`, with `PRIVACY_POLICY_VERSION` moved in the same PR
  (where it lives after `deploy/k8s` is gone: §9).
- It stays a draft PR, because a draft version on main blocks every deploy (STATUS, F1). It goes into force after
  C1's pages load and before any tester signs in (runbook step 15).
- Changes:
  - **Google Cloud replaces Amazon Web Services** as processor: Cloud Run, Pub/Sub, Secret Manager, Cloud Logging,
    Cloud Trace (request paths and timings of sampled requests; its location is *(unchecked)*) and Cloud DNS. The
    location is Belgium (europe-west1). The terms are Google's Cloud Data Processing Addendum, with the EU-U.S. Data
    Privacy Framework plus SCCs for any access from the US.
  - **Firebase Hosting (Google, a global CDN)** is named as the processor that serves `ryftlab.de` worldwide and
    processes visitors' IP addresses to deliver the pages. The terms are Firebase's Data Processing and Security Terms
    *(unchecked: which terms cover Hosting in a project without billing)*.
  - **Technical logs** (text §3): add "the IP address and user agent of every API request, in Google's request logs,
    14 days" and the audit logs of administrative access.
  - **Visiting ryftlab.de** (text §3): today it says the IP "is not stored" and is used "to limit requests per
    address" by our nginx. It becomes: the site is served by Firebase Hosting; Google's CDN processes the IP address
    to deliver the page; we receive and keep no access logs (the Cloud Logging export is off); there is no rate limit
    of ours.
  - **Security** (text §8): "can reach only what it needs" becomes "can access only the data it needs" (§5.2).
  - **Retention** (text §6): logs 14 days (unchanged, now true by the bucket). Events that couldn't be processed:
    14 days on the dead-letter topic (unchanged).
  - **Microsoft 365 through GoDaddy replaces Google Workspace** for `support@ryftlab.de`. GoDaddy is the contracting
    processor (its DPA, Microsoft as sub-processor), and GoDaddy receives DMARC reports. Where the tenant stores mail
    is *unverified* (D214), and support mail can carry health data (Art. 9).
  - "data is stored encrypted at Supabase and AWS" → "… and Google". "kept in a secrets vault" stays true (Secret
    Manager).
  - Grafana Labs: unchanged ("metrics without personal data"), and §6 keeps it true.
- Everyone consents again when it goes into force (`consent_required` rises).

---

## 9. Deploy, and where config lives

- **Terraform `infra/gcp`** is applied by the user, in workspace **`ryft-gcp`** of the org **`ryft-lab`**. The workspace
  is CLI-driven with remote execution (`cloud { organization = "ryft-lab" workspaces { name = "ryft-gcp" } }`), and
  it signs in to GCP with dynamic credentials (§11 step 2). It owns:
  - project services (the keep list of §11 step 2) and the service accounts;
  - every IAM binding, the services' invokers included;
  - secret containers and their accessor bindings;
  - the Pub/Sub topology (from the generated tfvars) and the Scheduler job;
  - Artifact Registry and its cleanup policy;
  - the WIF pool for GitHub;
  - the domain mappings and the Cloud DNS zone, including the site's records (variables from the Hosting wizard);
  - the log bucket and sink;
  - the Grafana-managed rules.

  Not in Terraform: the budget (by hand, §7), the WIF pool for HCP Terraform and its two SAs (the bootstrap, because
  Terraform can't create its own credentials), the Grafana SA's key (created by hand and stored only in Grafana
  Cloud, §6), and the site project `ryftlab-site` with its deploy SA and custom domains (by hand, §11 step 10).
- **CI owns the revisions:** `deploy/run/<svc>.yaml` (Knative service YAML, env, secrets by name, probes, limits) and
  `deploy/run/migrate.yaml`. Nothing has two owners, as D94 required. `PRIVACY_POLICY_VERSION` moves from
  `deploy/k8s/base/{api,ingest}/config.env` to `deploy/run/config.env`, and the checks that read it follow.
- **`deploy.yml`** (on push to main, after `ci-ok`, environment `deploy`):
  1. WIF auth (`google-github-actions/auth`, pinned by SHA).
  2. Build and push the five images to Artifact Registry.
  3. `gcloud run jobs deploy migrate --image …@sha256:…`, then
     `gcloud run jobs execute migrate --wait`. If it fails, stop: nothing rolls, as with today's PreSync.
  4. `gcloud run services replace deploy/run/<svc>.yaml` with the digest, then scoring, notify, ingest, api.
  5. `make smoke-prod`.
  6. The `site` job when `web/` changed: Firebase Hosting in `ryftlab-site` (§8).

  Rollback: `gcloud run services update-traffic <svc> --to-revisions=<previous>=100`. Migrations are additive and
  stay. D175's hold becomes a repository variable per image group, read by the workflow. The deploy watch watches
  this workflow.
- **Operator jobs:** `scoring-rescore` is a Cloud Run Job (`gcloud run jobs execute scoring-rescore
  --args=rescore,-days,400 --wait`) and replaces `rescore_all.sh`'s `kubectl exec`. `trace.sh` and the beta scripts
  use the public api's admin routes and Cloud Logging instead of `kubectl`.

---

## 10. Removal, tickets and order

**Superseded** (written as "superseded by D2xx" in DECISIONS with PR A):
- D16, D17 (amended: §2.2), D24, D27, D43, D45, D46, D51, D52;
- D55 (budget in EUR, made by hand; the site on Hosting without billing), D57, D64, D94–D96, D97, D98;
- D99 (amended: Pub/Sub IAM), D102–D105;
- D107 (amended: Firebase Hosting instead of the nginx image, §8);
- D120, D121, D151, D155 (amended: §5.1), D169 (amended: 10 s), D175 (amended: §9), D180, D182, D183, D185;
- D186–D205 (Vault and admission), D207–D210;
- D211 (amended: §5.3), D215 (superseded by C1, §8).

**Kept:** D100 and D101 (amended: inline relay, not-before), D213 (amended: no `argocd.` and `rabbitmq.` hosts, plus
`ingest.`), D214 (the records move to Cloud DNS; CAA adds `pki.goog`).

B0 is done: this pass checked the figures (marks and §15). Its live tests move to where they can run: the
`verify-full` login to runbook step 7, the session-state grep to PR A, the forged-XFF, 33 MiB and HTTP/2 tests to step 9.

| # | PR | Size | Contents | After it, the user |
|---|---|---|---|---|
| A | `backend: Pub/Sub and Cloud Run` | **XL** (~3–4k lines with tests) | §2.3, §3, §4's DSN and `PGPASSWORD`, h2c, OIDC, OTLP, slog, `/health/live` and `/health/ready`, the outbox migration (`available_at`), compose with the emulator, `cmd/pubsub-topology`, amqpbus and RabbitMQ deleted, the session-state grep (§4). `API.md` (api-contract-owner): Base URLs and the health paths, additive. `MESSAGING_DESIGN.md` rewritten, DECISIONS D216–D227. Deploys nothing | — |
| B | `infra, deploy: GCP and the deploy workflow` | **L** | `infra/gcp` (workspace `ryft-gcp` in `ryft-lab`; prepared, `terraform test`, never applied), `deploy/run/*`, `deploy.yml` (off until the variable `GCP_DEPLOY=on`), `scripts/dns_compare.sh`, `PRIVACY_POLICY_VERSION`'s new home and its checks, alert rules, `OPERATIONS.md` → GCP with §11's bootstrap and stop line. Disjoint from A, so it is built in parallel | runbook steps 1–9 and 11 (§11) |
| C1 | `web: Firebase Hosting` | **M** | §8: `web/scripts/firebase-json.mjs` and the generated `firebase.json` (headers, #156's trailing-slash redirect, the app-link rewrite, `ignore`); `hosting.test.ts` replacing `nginx.test.ts`; `make web-e2e` against the Hosting emulator; the `site` job in `deploy.yml` (WIF, no tokens); the site's DNS variables in `infra/gcp`; `check_policy_in_force.sh` reading the live `/privacy`; the two site synthetics and Site\* alerts; `web/CLAUDE.md` and the CI docs. It touches B's `deploy.yml` and `infra/gcp`, so it stacks on B (web-engineer, with gitops-engineer for the job) | runbook step 10 |
| C2 | `legal: privacy draft (Google Cloud, Firebase Hosting, Microsoft via GoDaddy)` | S, **draft** | §8, `status: draft`. Goes into force after C1's pages load, before any tester | decides the wording and date; step 15 |
| D1 | `infra/aws: allow the removal` | S | the README's first PR: `NoDeletionOrDisable` dropped from `vault_keys`, `force_destroy = true` on the snapshot bucket. Plus E's prerequisite: `prevent_destroy` dropped from the zone in `infra/aws/dns`. Merged after the NS switch (step 6) | steps 12 (E) and 13's first apply |
| D2 | `infra/aws: empty the root` | S | the README's second PR: `vault.tf`, `budget.tf` and every other resource file of `infra/aws` deleted, so the root declares nothing and the plan destroys the keys (scheduled, 30 days), the aliases, the bucket and the AWS budget | step 13's second apply |
| D3 | `remove: Kubernetes, Argo CD, Vault, admission, AWS` | L (mostly deletions) | `deploy/k8s`, `deploy/argocd`, `deploy/rabbitmq`, `deploy/monitoring`, `infra/aws` with `dns` and `kubernetes` (incl. admission charts, `admission-e2e`, `argocd-luatest`), `infra/vault`, the kubectl scripts (`admission_e2e`, `check_argocd`, `check_operators_scope`, `check_registry`, `install_kubectl`, `vault_diff`; `dlq`, `rescore_all`, `trace` rewritten in A/B), `admission-e2e.yml`, `make k8s-*`, `promote.sh`, `hold.sh`, the nginx `web` image (`web/Dockerfile`, `web/nginx/`, the `web-image` job, `services/web`). `ARCHITECTURE.md` and `OPERATIONS.md` brought up to date, STATUS closed out. Only after E and D2 are applied and production on GCP passes the smoke | step 14 |

**Order.** A ∥ B, then C1 on top of B (C2 drafted alongside) → runbook steps 1–9 (GCP live) → **step 10, the site: the
pages load** → step 11 (observability) → C2 in force → step 15, testers. The retirement runs after step 10 and
doesn't block testers: D1 → E (step 12, ≥ 48 h after the NS switch) → D2 (step 13) → D3 and the old workspaces
(step 14). The data key's first half (step 0) can run today. The site project itself (step 10's first half) can be
created any time.

A and B can be built in parallel (one writer each: backend-engineer + database-engineer + test-engineer on A;
infra-engineer + gitops-engineer on B), then reviewed by code-reviewer, security-privacy-reviewer and sre.

---

## 11. The user's runbook, in outline (each step has a stop rule)

Steps marked **old org** run against `tim-krebs-org` and only with `TF_TOKEN_app_terraform_io='<old token>'` in
front of that one command (a leading space keeps it out of zsh's history with `setopt HIST_IGNORE_SPACE`). Every
other step runs in a shell where `TF_TOKEN_app_terraform_io` is unset, so the CLI uses the `ryft-lab` login.

| # | Step | Stop if |
|---|---|---|
| 0 | **Old org: the data key into 1Password** (§5.3 steps 1–2). Can run today, before anything else | no state version found, or the length isn't 32 |
| 1 | **The project, as it is:** `gcloud projects describe ryftlab-prod --format='value(projectNumber,parent.id)'` prints a number and no parent; `gcloud billing projects describe ryftlab-prod --format='value(billingEnabled)'` prints `True`; `gcloud projects get-iam-policy ryftlab-prod` names only you. 2-step verification is on for the Google account. In Search Console, add the domain property `ryftlab.de` and copy its TXT value (not secret) for step 2e | a parent (an organisation) appears, or another principal |
| 2 | **Bootstrap** (the commands below): the API keep list, the workspace `ryft-gcp`, the HCP Terraform pool, its two SAs, the workspace variables. Then `terraform -chdir=infra/gcp init` and `plan` | a plan fails authentication; `attributeCondition` lacks either ID; the apply SA would need `roles/owner` |
| 3 | **The budget's 10 % rule** (§7's commands) | `describe` doesn't list `0.1` |
| 4 | **`infra/gcp` apply 1:** everything except the services' IAM and the mappings. The Cloud DNS zone carries every record of `infra/aws/dns`, CAA with `pki.goog` as well as `letsencrypt.org`, and the site-verification TXT | the plan has any change or destroy, or the add count differs from B's PR body |
| 5 | **Secret values:** `gcloud secrets versions add … --project=ryftlab-prod --data-file` for the DB passwords, the Supabase secret key, `ADMIN_API_KEY`, the Apple and APNs `.p8` keys from 1Password, the OTLP tokens. The data key from 1Password via §5.3 steps 3–5 | the data key's fingerprint differs or its length isn't 32 (then D211's fallback) |
| 6 | **The DNS move.** `scripts/dns_compare.sh` (PR B) digs every record set of the Route 53 zone against the Cloud DNS name servers: MX, the apex TXT, DMARC, the DKIM CNAME, autodiscover, SRVs, CAA. Check `dig DS ryftlab.de @a.nic.de`: there must be no DS (DNSSEC off). Set GoDaddy's name servers to the four `ns-cloud-*`. Wait until `dig NS ryftlab.de @a.nic.de` shows Google, then send and receive a test mail. **Route 53 stays for 48 h.** Then verify the domain in Search Console and add `hcp-terraform-apply@ryftlab-prod.iam.gserviceaccount.com` as a **verified owner** (verification belongs to the user who did it, and the apply SA creates the mappings) | a record differs, except the two intended ones (CAA's added `pki.goog` and the verification TXT; the script checks them exactly); a DS exists; after the switch, mail fails → set the old NS back at once (Route 53 is still live) |
| 7 | **Supabase:** the transaction pooler host and port 6543 for the services, session 5432 for migrate, the pool size (Database → Connection pooling), Data API still off, Site URL and redirect URLs on `ryftlab.de`. One `verify-full` login as `ryft_api.<ref>` on 6543 and as the migrate role on 5432 (whether the pooler's certificate matches Supabase's CA and host name is *(unchecked)*; a mismatch fails closed), then `make smoke-supabase` on 6543 (§4) | the login fails or `ECIRCUITBREAKER` appears: stop and wait it out, don't retry in a loop |
| 8 | **First deploy:** repository variable `GCP_DEPLOY=on`, then run `deploy.yml` by hand. Nothing is public yet | `migrate` doesn't log `migration applied` / `up to date`, or a revision isn't ready |
| 9 | **`infra/gcp` apply 2:** invokers (`allUsers` on api and ingest, `ryft-push` on scoring and notify) and the domain mappings for `api.` and `ingest.`. Then `make smoke-prod`; a 33 MiB sync through `ingest.ryftlab.de` gets our JSON 413; a request with a forged `X-Forwarded-For` shows the real address in the limiter's log (§5.1) | the plan touches more than IAM and mappings; a certificate is still pending **after 24 hours** (the docs give 15 minutes to 24 hours; then the fallback of §1); the smoke fails; the 413 isn't ours (§3) |
| 10 | **The site and the app (C1 merged).** Create `ryftlab-site` (the commands below; this half can run any time). In the Firebase console, Hosting → Add custom domain: `ryftlab.de`, then `www.ryftlab.de` with "redirect to `ryftlab.de`". Put the A and TXT values the wizard shows into `infra/gcp`'s site variables and apply (**apply 3**). Run `deploy.yml` by hand once (the `site` job). Then: `curl -s -o /dev/null -w '%{http_code}\n'` prints `200` for `/privacy`, `/de/privacy`, `/impressum`, `/terms`, `/support`; `curl -sI https://ryftlab.de/.well-known/apple-app-site-association` shows `content-type: application/json` and no redirect; `curl -sI 'https://ryftlab.de//evil.example/'` has no `location`; `https://www.ryftlab.de/privacy` redirects to the apex. The app build uses `api.` + `ingest.ryftlab.de`; App Store Connect's privacy, support and marketing URLs move to `https://ryftlab.de/…` (REBUILD step 12); check the AASA's team ID. The Sign in with Apple return URL stays Supabase's. Until here, only you sign in | the console asks for Blaze to connect a domain (Spark doesn't allow it: stop, the user decides); apply 3 touches anything but the site's record sets; a certificate is still pending after 24 hours; any check above fails |
| 11 | **Observability:** OTLP tokens; `grafana-reader`'s key by hand (`gcloud iam service-accounts keys create grafana-reader.json --iam-account=grafana-reader@ryftlab-prod.iam.gserviceaccount.com`), pasted into the data source, then `rm grafana-reader.json`; `make alerts-apply`; the two synthetics; one test alert. `gcloud iam service-accounts keys list --managed-by=user` per SA shows that one key only | no `ryft_db_up` from any service after 30 min (the ticks aren't arriving) |
| 12 | **E, old org: Route 53.** ≥ 48 h after the switch, with D1 merged: ` TF_TOKEN_app_terraform_io='<old token>' terraform -chdir=infra/aws/dns init`, then ` TF_TOKEN_app_terraform_io='<old token>' terraform -chdir=infra/aws/dns apply -destroy`; read every line before `yes` | mail failed during the 48 h; the plan lists anything but the zone and its records |
| 13 | **D, old org: the Vault keys, the bucket, the AWS budget** (the README's two-PR route). With D1 merged: ` TF_TOKEN_app_terraform_io='<old token>' terraform -chdir=infra/aws init`, then ` TF_TOKEN_app_terraform_io='<old token>' terraform -chdir=infra/aws apply`: only the two key policies and the bucket's `force_destroy` change. With D2 merged, the same `apply` again: destroys only | the first plan shows anything but those three changes; the second shows a `+` or `~`, or a resource that isn't RYFT's (a shared account: by IDs, never by tag) |
| 14 | **Old org: the old workspaces.** After step 0's key is confirmed in Secret Manager (§5.3 step 4), and E and D are applied, safe-delete `ryft-prod-euc1-dns`, `ryft-prod-euc1-platform`, `ryft-prod-euc1-kubernetes` and `ryft-prod-euc1-vault` (the last one holds the old data key's state). Then D3 merged; the AWS and GHCR secrets in GitHub deleted | a safe delete is refused (resources are still in that state: find out why first) |
| 15 | **Testers:** after step 10's pages load and C2 is in force (its consent round runs first) | `/privacy` doesn't show C2's version |

**Step 2, the bootstrap (bash or zsh, as the project owner, `TF_TOKEN_app_terraform_io` unset).**
```bash
P=ryftlab-prod
PN=$(gcloud projects describe "$P" --format='value(projectNumber)')
TFC_ORG_ID=org-aus6gvWumndxZixQ                     # ryft-lab
POOL=hcp-terraform; PROVIDER=app-terraform-io
PLAN_SA=hcp-terraform-plan@$P.iam.gserviceaccount.com
APPLY_SA=hcp-terraform-apply@$P.iam.gserviceaccount.com

# 2a. APIs: the keep list on, everything else off.
KEEP=(artifactregistry.googleapis.com billingbudgets.googleapis.com cloudbilling.googleapis.com
  cloudresourcemanager.googleapis.com cloudscheduler.googleapis.com cloudtrace.googleapis.com dns.googleapis.com
  iam.googleapis.com iamcredentials.googleapis.com logging.googleapis.com monitoring.googleapis.com
  pubsub.googleapis.com run.googleapis.com secretmanager.googleapis.com serviceusage.googleapis.com sts.googleapis.com)
gcloud services enable "${KEEP[@]}" --project="$P"
for s in $(gcloud services list --enabled --project="$P" --format='value(config.name)'); do
  case " ${KEEP[*]} " in *" $s "*) continue ;; esac
  gcloud services disable "$s" --project="$P" || echo "still on: $s"
done
gcloud services list --enabled --project="$P" --format='value(config.name)'

# 2b. The workspace in ryft-lab (CLI-driven, remote execution). The token goes through stdin, never argv.
tfc_auth() { jq -r '"Authorization: Bearer " + .credentials["app.terraform.io"].token' ~/.terraform.d/credentials.tfrc.json; }
API=https://app.terraform.io/api/v2; CT='Content-Type: application/vnd.api+json'
tfc_auth | curl -sS -H @- -H "$CT" -X POST "$API/organizations/ryft-lab/workspaces" \
  --data '{"data":{"type":"workspaces","attributes":{"name":"ryft-gcp","execution-mode":"remote","auto-apply":false}}}' \
  | jq -r '.data.id // .errors'
WS_ID=$(tfc_auth | curl -sS -H @- "$API/organizations/ryft-lab/workspaces/ryft-gcp" | jq -r .data.id)
echo "$WS_ID"                                       # ws-…

# 2c. The pool for app.terraform.io, pinned to the org and workspace IDs.
gcloud iam workload-identity-pools create "$POOL" --project="$P" --location=global \
  --display-name="HCP Terraform ryft-lab"
gcloud iam workload-identity-pools providers create-oidc "$PROVIDER" --project="$P" --location=global \
  --workload-identity-pool="$POOL" --issuer-uri=https://app.terraform.io \
  --attribute-mapping='google.subject=assertion.sub,attribute.terraform_organization_id=assertion.terraform_organization_id,attribute.terraform_workspace_id=assertion.terraform_workspace_id,attribute.terraform_run_phase=assertion.terraform_run_phase' \
  --attribute-condition="assertion.terraform_organization_id == '$TFC_ORG_ID' && assertion.terraform_workspace_id == '$WS_ID'"
gcloud iam workload-identity-pools providers describe "$PROVIDER" --project="$P" --location=global \
  --workload-identity-pool="$POOL" --format='value(attributeCondition)'

# 2d. Two SAs: plan reads, apply writes. Each is usable only by its run phase.
gcloud iam service-accounts create hcp-terraform-plan --project="$P" --display-name="HCP Terraform plan (ryft-gcp)"
gcloud iam service-accounts create hcp-terraform-apply --project="$P" --display-name="HCP Terraform apply (ryft-gcp)"
for r in roles/viewer roles/iam.securityReviewer; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$PLAN_SA" --role="$r" --condition=None
done
for r in roles/run.admin roles/pubsub.admin roles/cloudscheduler.admin roles/secretmanager.admin \
         roles/artifactregistry.admin roles/dns.admin roles/logging.configWriter roles/iam.serviceAccountAdmin \
         roles/iam.serviceAccountUser roles/iam.workloadIdentityPoolAdmin roles/resourcemanager.projectIamAdmin \
         roles/serviceusage.serviceUsageAdmin; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$APPLY_SA" --role="$r" --condition=None
done
PS=principalSet://iam.googleapis.com/projects/$PN/locations/global/workloadIdentityPools/$POOL
gcloud iam service-accounts add-iam-policy-binding "$PLAN_SA" --project="$P" \
  --role=roles/iam.workloadIdentityUser --member="$PS/attribute.terraform_run_phase/plan"
gcloud iam service-accounts add-iam-policy-binding "$APPLY_SA" --project="$P" \
  --role=roles/iam.workloadIdentityUser --member="$PS/attribute.terraform_run_phase/apply"

# 2e. The workspace's variables (none sensitive; each prints 201).
tfc_var() {  # tfc_var KEY VALUE [env|terraform]
  tfc_auth | curl -sS -o /dev/null -w "%{http_code} $1\n" -H @- -H "$CT" -X POST "$API/workspaces/$WS_ID/vars" \
    --data "$(jq -nc --arg k "$1" --arg v "$2" --arg c "${3:-env}" \
      '{data:{type:"vars",attributes:{key:$k,value:$v,category:$c,sensitive:false}}}')"
}
tfc_var TFC_GCP_PROVIDER_AUTH true
tfc_var TFC_GCP_WORKLOAD_PROVIDER_NAME "projects/$PN/locations/global/workloadIdentityPools/$POOL/providers/$PROVIDER"
tfc_var TFC_GCP_PLAN_SERVICE_ACCOUNT_EMAIL "$PLAN_SA"
tfc_var TFC_GCP_APPLY_SERVICE_ACCOUNT_EMAIL "$APPLY_SA"
tfc_var google_site_verification 'google-site-verification=<value from step 1>' terraform
```
- **APIs.** Without `--force`, `gcloud services disable` refuses a service that was used in the last 30 days, was
  enabled in the last 3 days, or that an enabled service depends on. `--force` would disable the dependants as well,
  so it is never used here. The project is new, so most defaults will be "still on" at first: run 2a's loop again
  from day 4. What stays on after that is a dependency or in use, and goes into B's PR body. Terraform owns the same
  keep list (`google_project_service`, `disable_on_destroy = false`).
- **Audience.** The provider has no `--allowed-audiences`. That is GCP's default audience, the provider's full
  name, and HCP Terraform sends it when only `TFC_GCP_WORKLOAD_PROVIDER_NAME` is set *(search)*.
- **The condition** checks both IDs, not names, as the review asked: workspace names can change. Module test runs
  carry no workspace claims, so they can't match.
- **The apply SA is owner-equivalent in effect**, because `projectIamAdmin` can grant any role. That is inherent in
  Terraform owning IAM. The boundary is the condition (one org, one workspace, apply phase only) plus `auto-apply`
  off, and the stop rule still refuses `roles/owner` itself.
- **Grafana.** The rules' provider needs a Grafana token: a **sensitive** variable set in the workspace UI, never by
  this script or on argv.

**Step 10, the site project (as you, logged in to gcloud and `firebase login`; no billing account anywhere).**
```bash
S=ryftlab-site                                       # if the ID is taken, pick another and use it in C1's config
PN=$(gcloud projects describe ryftlab-prod --format='value(projectNumber)')
gcloud projects create "$S" --name="RYFT site"       # no --organization; never link a billing account
gcloud billing projects describe "$S" --format='value(billingEnabled)'   # False
gcloud services enable firebase.googleapis.com firebasehosting.googleapis.com iam.googleapis.com \
  iamcredentials.googleapis.com --project="$S"
npx --yes firebase-tools@<pinned> projects:addfirebase "$S"      # Spark, because there is no billing account
npx --yes firebase-tools@<pinned> hosting:sites:list --project "$S"  # the default site, ryftlab-site

# The deploy SA: Hosting only, usable only through the GitHub pool in ryftlab-prod.
gcloud iam service-accounts create site-deployer --project="$S" --display-name="CI: Firebase Hosting deploys"
SITE_SA=site-deployer@$S.iam.gserviceaccount.com
gcloud projects add-iam-policy-binding "$S" --member="serviceAccount:$SITE_SA" \
  --role=roles/firebasehosting.admin --condition=None
gcloud iam service-accounts add-iam-policy-binding "$SITE_SA" --project="$S" \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/$PN/locations/global/workloadIdentityPools/<github pool>/attribute.repository_id/<repository id>"
gcloud projects get-iam-policy "$S" --format='table(bindings.role,bindings.members)'  # you as owner, the SA, nothing else
gcloud iam service-accounts keys list --iam-account="$SITE_SA" --managed-by=user       # empty
```
- `<github pool>` and the `repository_id` attribute are B's (§5.1). The pool's condition already pins `deploy.yml` on
  main and the `deploy` environment, so this binding needs only the repository.
- `roles/firebasehosting.admin` is the one role. Whether it is the minimal set is *(unchecked)*. If the first deploy
  names a missing permission, add only that one (for example `roles/serviceusage.serviceUsageConsumer` on
  `ryftlab-site`).
- Which project's API enablement the cross-project impersonation consumes is *(unchecked)*. `iamcredentials` is on in
  both projects, and it costs nothing.
- `ryftlab-site.web.app` and `.firebaseapp.com` serve the same site. That is harmless: the admin page's API calls fail
  CORS there, and the app accepts only `ryftlab.de` links.

**The €20 stop line** (§7) is an OPERATIONS runbook entry in PR B, with the exact commands from §7.

---

## 12. Proposed decisions (D216 and up)

- **D216** RYFT runs on GCP serverless (Cloud Run, Pub/Sub, Secret Manager, Scheduler, Artifact Registry, Cloud DNS)
  in project `ryftlab-prod`, without an organisation, and the site on Firebase Hosting in `ryftlab-site`. Nothing stays in the HashiCorp AWS account. Terraform runs in
  HCP Terraform org `ryft-lab` (CLI-driven workspaces, remote execution); the old org `tim-krebs-org` is used only for
  the retirement. *(the user's decision, 2026-11-14)*
- **D217** Region europe-west1 and Cloud Run domain mappings (Preview), two API hosts, `api.` and `ingest.ryftlab.de`.
  Revisit if a mapping fails or its latency hurts (§1). `min-instances 0` everywhere, and the gaps of §5.2 are
  accepted for the pre-alpha. *(the user's decision, 2026-11-14)*
- **D218** One Pub/Sub topic per event key with push and OIDC. Retry policy 10–600 s (push backoff may apply instead),
  20 delivery attempts, a DLQ per consumer with a 14-day hold subscription, per-user ordering on scoring's topics.
  Topology is generated from Go (§2).
- **D219** Request-based CPU. All timers become one Scheduler job fanned out through `ryft-tick`. The outbox relays
  inline, with a tick sweep. Deferral uses an outbox not-before (§2.2, §3).
- **D220** HTTP/2 end to end (h2c, stdlib) keeps the 32 MiB sync unchanged (§3).
- **D221** Services on the transaction pooler with `default_query_exec_mode=exec` and `verify-full`, migrate on
  session mode. Role passwords in Secret Manager via `PGPASSWORD`, with a fixed rotation order (§4).
- **D222** Secret Manager with one SA per service, secret-level bindings, and versions pinned by number. Values are
  never in Terraform state. The data key's path (§5.3). P5 lives in `config.LoadAuth`. Push and tick tokens are
  checked in-app on all four services (§2.2, §5.1).
- **D223** CI owns the Cloud Run revisions and jobs; Terraform owns everything else except the budget and its own
  credentials. Deploys use WIF, images go to Artifact Registry, migrate runs before the deploy (§9). Terraform signs
  in with dynamic credentials: a pool pinned to `ryft-lab`'s org ID and `ryft-gcp`'s workspace ID, a plan SA and an
  apply SA, each usable only in its run phase (§11 step 2).
- **D224** Logs stay in Google, in an EU bucket. Grafana reads Cloud Monitoring through a viewer-only SA key (the one
  key, accepted by the user), and the app pushes OTLP at the tick and on SIGTERM (§6).
- **D225** The whole static export of `web/` on Firebase Hosting at `ryftlab.de`, with `www` redirecting. It lives in
  a separate project without billing (`ryftlab-site`, Spark), so the free quota is a hard cap: over it, the site goes
  offline until the next month, and no bill comes. `firebase.json` is generated from the nginx rules' sources, with
  #156's trailing-slash rule as an explicit redirect. CI deploys through WIF with no tokens. The CAA set allows
  `letsencrypt.org` and `pki.goog` (§8). *(the user's decision, follow-up 2, 2026-11-14; the separate project is
  head-architect's default)*
- **D226** One budget, "RYFT 50 EUR", made by hand and not in Terraform, with rules at 10/50/90/100 %. The €20 stop
  line is a runbook action (invokers off, the tick paused). Max-instances is the hard cap (§7). *(the user's decision,
  2026-11-14)*
- **D227** The removal order (the data key first, then Cloud DNS, Route 53, the AWS keys and bucket, the old
  workspaces) and the superseded decisions (§10, §11).

## 13. The user's answers (2026-11-14)

| # | Question | Answer |
|---|---|---|
| 1 | Region | europe-west1 with domain mappings, keeping §1's "revisit if" list |
| 2 | Two API hosts | yes, `api.` and `ingest.ryftlab.de` |
| 3 | #161 | merged (`8389feb`), D207–D215 on main |
| 4 | The site | first none (follow-up 1), then **reversed in follow-up 2**: the whole export on Firebase Hosting, C1, before testers |
| 5 | The privacy draft's wording and date | open (below); C2 names Firebase Hosting |
| 6 | `min-instances` | 0 everywhere (head-architect's default; the user can object) |
| 7 | The Grafana SA key | accepted |
| 8 | §5.2's gaps | accepted for the pre-alpha (the same default) |
| 9 | Hosting's cost bound | a separate project without billing (`ryftlab-site`, Spark), head-architect's default; the user can still choose `ryftlab-prod` |
| — | Budget | "RYFT 50 EUR" by hand, plus a 10 % rule; €5 and €20 as runbook steps; no second budget |
| — | HCP Terraform | new org `ryft-lab`; the old org only for the retirement, one prefixed command at a time |
| — | GCP project | `ryftlab-prod`, no organisation, billing on; the bootstrap keeps what RYFT needs |
| — | B0 | folded into this pass (marks, §15) |
| — | Retirement order | data key → Cloud DNS and the NS switch → Route 53 → the AWS keys, bucket and budget → the old workspaces |

**Still open for the user:**
- C2's wording and date, and the D214 questions (the M365 tenant's location, GoDaddy's DPA). Both are due before the
  first tester (step 15).
- The site's project: `ryftlab-site` without billing (default) or `ryftlab-prod`.
- Whether the €20 line should send its own email (one more `--add-threshold-rule=percent=0.40`, §7).

## 14. Top risks

| Risk | Mitigation |
|---|---|
| Domain mappings are Preview, "not production-ready due to latency issues" | the user accepted it for the pre-alpha. The fallback is `run.app` URLs in the app's config, or a load balancer when paying (§1) |
| The 32 MiB sync hits the HTTP/1 cap | h2c end to end. Step 9 tests a 33 MiB body through the mapping. The fallback is a contract change, not silence |
| The pooler in transaction mode breaks a query that holds session state | `exec` mode (fallback `simple_protocol`), the grep in PR A, the store's integration tests against PgBouncer in transaction mode in CI, `make smoke-supabase` on 6543 (step 7) |
| Work after the response never runs (request-only CPU) | inline relay before answering, the tick sweep, `OutboxBacklog` and `TickMissing` alerts |
| 429s from a saturated scoring count as attempts and dead-letter | concurrency 4, 20 attempts, per-user ordering, `PushBacklog`; replay is a script |
| The DNS move breaks mail | the record compare before the switch, Route 53 kept 48 h, rollback is the NS at GoDaddy |
| The data key is lost or mistyped | 1Password first (step 0), the fingerprint compare, `prevent_destroy`, delayed destruction, the old workspace deleted last, D211's fallback |
| A step meant for the new org runs against the old one, or the reverse | the old token only as a one-command prefix, every such step marked; the pool's condition accepts only `ryft-lab`'s IDs |
| Cost surprise | max-instances, the 10 % (€5) email, the €20 stop line (invokers off, the tick paused) |
| Spark's quota disables the site until the 1st (a flood, or the synthetics plus a launch) | ~1–2 GB of 10 GB in use; SiteDown fires; the API keeps working; the user can upgrade the site project to Blaze with a budget (§8) |
| A Hosting rule differs from nginx (two CSPs on `/admin`, the AASA's type, an off-site trailing-slash redirect) | C1's tests against the Hosting emulator, the same assertions live at step 10 (§8) |
| A certificate never issues | the CAA set allows `pki.goog` and `letsencrypt.org` before any domain is connected; 24 hours is the stop signal (§8) |
| Testers consent to a text that misstates logging | C2 names IP and user agent in request logs and Firebase Hosting, corrects "reach only" (§8), and is in force before any tester; the bucket keeps 14 days |
| A debug commit turns on apikey mode on a public service | P5 moves into `config.LoadAuth` (refused on Cloud Run) and a CI check of `deploy/run/*.yaml` |
| One `versions add` silently changes a running service (data key, DB password) | secrets pinned by version; rotation is a reviewed deploy (§4) |
| What is still unchecked (§15) | each item carries the mark, and the ones that can break a step have that step's test (HTTP/2 and XFF at step 9, `verify-full` at step 7, Spark's custom domains and the AASA's type at step 10) |

## 15. Sources (checked 2026-11-14)

Unmarked figures in this design come from these pages. *(search)* means only the search snippet of the official
page was read, because the page itself returned its title only or was refused to the fetch tool.

- Cloud Run: [pricing](https://cloud.google.com/run/pricing) *(search: the tier rates and the free-tier discount
  rule)*, [quotas](https://docs.cloud.google.com/run/quotas),
  [mapping custom domains](https://docs.cloud.google.com/run/docs/mapping-custom-domains) (regions, Preview,
  limitations, per-user verification, verified owners),
  [container contract](https://docs.cloud.google.com/run/docs/container-contract) (SIGTERM, reserved paths),
  [ingress](https://docs.cloud.google.com/run/docs/securing/ingress),
  [known issues](https://docs.cloud.google.com/run/docs/known-issues).
- Pub/Sub: [push](https://docs.cloud.google.com/pubsub/docs/push),
  [ordering](https://docs.cloud.google.com/pubsub/docs/ordering),
  [handling failures](https://docs.cloud.google.com/pubsub/docs/handling-failures),
  [emulator](https://docs.cloud.google.com/pubsub/docs/emulator),
  [subscription properties](https://docs.cloud.google.com/pubsub/docs/subscription-properties),
  [quotas](https://docs.cloud.google.com/pubsub/quotas), [pricing](https://cloud.google.com/pubsub/pricing).
- Secret Manager: [pricing](https://cloud.google.com/secret-manager/pricing),
  [delayed destruction](https://docs.cloud.google.com/secret-manager/docs/delay-destruction-of-secret-versions).
- [Cloud Scheduler pricing](https://cloud.google.com/scheduler/pricing),
  [Artifact Registry pricing](https://cloud.google.com/artifact-registry/pricing),
  [Cloud DNS pricing](https://cloud.google.com/dns/pricing),
  [Observability pricing](https://cloud.google.com/products/observability/pricing) *(search for `_Required`)*.
- Billing: [budgets](https://docs.cloud.google.com/billing/docs/how-to/budgets),
  [`gcloud billing budgets update`](https://docs.cloud.google.com/sdk/gcloud/reference/billing/budgets/update);
  [`gcloud services disable`](https://docs.cloud.google.com/sdk/gcloud/reference/services/disable) (the 30-day and
  3-day checks, `--force`).
- Supabase: [connecting to Postgres](https://supabase.com/docs/guides/database/connecting-to-postgres),
  [compute and disk](https://supabase.com/docs/guides/platform/compute-and-disk),
  [disabling prepared statements](https://supabase.com/docs/guides/troubleshooting/disabling-prepared-statements-qL8lEL).
- HCP Terraform: [GCP dynamic credentials](https://developer.hashicorp.com/terraform/cloud-docs/dynamic-provider-credentials/gcp-configuration)
  *(search)*, [workload identity tokens](https://developer.hashicorp.com/terraform/cloud-docs/dynamic-provider-credentials/workload-identity-tokens)
  *(search: the claims)*, [workspaces API](https://developer.hashicorp.com/terraform/cloud-docs/api-docs/workspaces)
  *(search: safe delete)*.
- Firebase: [custom domains](https://firebase.google.com/docs/hosting/custom-domain) (CAA, certificates, the
  redirect to a second domain), [Hosting quotas](https://firebase.google.com/docs/hosting/usage-quotas-pricing)
  (10 GB, disabled until the next month), [pricing plans](https://firebase.google.com/docs/projects/billing/firebase-pricing-plans)
  (Spark, billing auto-upgrades), [pricing](https://firebase.google.com/pricing) (360 MB/day; "Custom domain & SSL"
  *(search)*), [full config](https://firebase.google.com/docs/hosting/full-config) (priority order, `trailingSlash`,
  `cleanUrls`, RE2, `ignore`), [request logs](https://firebase.google.com/docs/hosting/web-request-logs-and-metrics)
  (`remoteIp`, 30 days), [the CLI in CI](https://firebase.google.com/docs/cli) (ADC, `login:ci` deprecated). Cloud
  Run's CAA sentence is on its mapping page above.
- [Grafana Synthetic Monitoring](https://grafana.com/docs/grafana-cloud/testing/synthetic-monitoring/)
  *(search: the free executions)*.
- [Google developer forum, X-Forwarded-For on Cloud Run](https://discuss.google.dev/) *(a forum report, not docs)*.

**Still unchecked:** the load balancer's forwarding-rule price; egress prices; Monitoring API read pricing (the
sources conflict); Cloud Trace's free allowance and location; whether system metrics and budgets are free;
`min-instances`' idle price; Grafana Cloud's free metrics and logs; `deliveryAttempt` on push; HTTP/2 through a mapping;
Supavisor with pgx's `exec` mode; the location of `_Required`; `run.developer` vs `invoker-iam-disabled`; Hosting
setting `Content-Type` for the AASA; two matching `headers` rules on Hosting; whether Hosting collapses `//`; Spark's
custom domains *(search)*; Hosting's transfer quota (10 GB/month or 360 MB/day); the minimal deploy role; which
project's APIs the cross-project impersonation consumes; Firebase's terms for Hosting; the token creator role *(search)*; the Budget API on the quota
project; the safe-delete answer for a non-empty state; the pooler's certificate under `verify-full`.
