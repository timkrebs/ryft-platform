# RYFT on GCP serverless: the design (2026-11-14)

Status: **proposed**. Docs only, nothing built, nothing applied. The decisions are "proposed D216 and up" (§12). They go
into `DECISIONS.md` with the first implementation PR, after the user accepts them. #161 (D207–D215) isn't merged yet.

**Why.** Since 2026-11-14 an SCP of the HashiCorp org (`p-emcptm8t`) denies `eks:CreateCluster` in account
173003892479. EKS cost about $400–450 a month, and that cost caused the shutdown on 11-10 (D206). The user decided on
2026-11-14 to move to GCP serverless: "für eine pre-alpha absolut ausreichend". The target is a few euros a month for
about ten testers.

**Unchanged:** Supabase (Postgres and Auth, Frankfurt), Grafana Cloud, the Apple keys, HCP Terraform (org
`tim-krebs-org`, now with GCP dynamic credentials), the domain `ryftlab.de` (D213–D215), the contract's shapes
(`docs/API.md`), the outbox (D101) and idempotent handlers (D100).

**Prices and limits are unchecked.** WebSearch, curl and WebFetch were all refused in this session (see the PR body), so
I couldn't read the GCP price list or the docs. Every price, free tier and product limit below is from memory of the
2025 pages and marked *(unchecked)*. Ticket B0 (§10) checks them before anything is applied.

---

## 0. The target in one table

| Today (EKS) | On GCP | Section |
|---|---|---|
| `api`, `ingest` Deployments behind Traefik on one host | Cloud Run services, public, scaling to zero, `api.ryftlab.de` and **`ingest.ryftlab.de`** | §1, §3 |
| `scoring`, `notify` consuming RabbitMQ | Cloud Run services, private, invoked by Pub/Sub push with OIDC | §2 |
| `migrate` PreSync Job, PostSync smoke | Cloud Run Job `migrate`, run by CI before the deploy; CI runs the smoke after it | §9 |
| Tickers in api, scoring, notify; the outbox relay's poll; `db.Watch` | one Cloud Scheduler job → topic `ryft-tick` → `POST /internal/tick` on each service | §3 |
| Vault CE + VSO | Secret Manager, one service account per service, secret-level IAM | §4, §5 |
| RabbitMQ (D97–D102) | Pub/Sub: one topic per event key, push subscriptions, a dead-letter topic per consumer | §2 |
| Argo CD + the promote PR (D94–D96) | GitHub Actions `deploy.yml` with Workload Identity Federation, no JSON keys | §9 |
| GHCR | Artifact Registry `europe-west1-docker.pkg.dev/<project>/ryft` | §9 |
| cert-manager, ExternalDNS, admission, NetworkPolicies, Alloy | gone. Google-managed certificates on the domain mappings; Terraform owns DNS | §5, §6 |
| Route 53 zone `ryftlab.de` (M365 records) | Cloud DNS zone, the same records | §11 |
| `web` nginx pod (not deployed, D215) | Firebase Hosting serving the static export from `web/` | §8 |

---

## 1. Region: **europe-west1** (recommended)

| | europe-west3 (Frankfurt) | europe-west1 (St. Ghislain, BE) |
|---|---|---|
| RTT to Supabase eu-central-1 *(estimate, unmeasured)* | ~1–2 ms | ~6–9 ms |
| A sync: about 20 round trips (auth lookup, upserts by batch, queued days, outbox row, commit, inline relay) *(counted from the handler, roughly)* | +~30 ms | +~150 ms |
| Scoring one ingested event: dozens to a few hundred queries | baseline | +0.3–1.5 s |
| Cloud Run price tier *(unchecked)* | Tier 2: about 40 % more above the free tier | Tier 1 |
| Free tier *(unchecked)* | per billing account, the same in every region | the same |
| Custom domain *(unchecked)* | **no Cloud Run domain mapping** in this region. A global external Application Load Balancer costs about $18 a month for its forwarding rule, or the app uses `*.run.app` URLs | domain mapping (Preview), free, Google-managed certificate |

**Why west1:** it is the only free way to `api.ryftlab.de`. A sync gets about 0.15 s slower, and scoring a few days
gets about a second slower. Both stay inside the sync-to-score target (p95 2 min, `SyncToScoreSlow`). A load balancer
alone would cost several times the rest of the bill. Firebase Hosting rewrites to Cloud Run don't work for the API: the
origin leg is HTTP/1, which brings back the 32 MiB cap, and there is a 60 s timeout *(unchecked)*.

**Two hosts, because a mapping can't route by path:** a domain mapping points one host at one service, so
`/v1/sync → ingest` on `api.ryftlab.de` (Traefik today) has no equivalent. `docs/API.md` → Base URLs already gives the
app a separate ingest URL ("leave the ingest URL empty"). The new build sets `https://api.ryftlab.de` and
`https://ingest.ryftlab.de`. That is a documentation change to the contract, not a shape change. api-contract-owner
writes it in PR A. The app needs a new build for D213 anyway.

**Revisit if:** domain mappings fail or leave Preview without west1 (then europe-west3 or west1 behind a load balancer,
~$18 a month), Supabase moves, or scoring latency matters (a rescore of many days).

**Everything in the EU:**
- Pub/Sub topics get `message_storage_policy` = europe-west1.
- Secrets use user-managed replication in europe-west1.
- Artifact Registry, Scheduler and the Cloud Run jobs are in europe-west1.
- Logs go to a regional bucket (§6).
- Firebase Hosting is a global CDN; the privacy text says so (§8).

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

| Need | Today (RabbitMQ) | Pub/Sub *(limits unchecked)* |
|---|---|---|
| Push auth | mTLS + per-user password (D98) | push with an OIDC token of SA `ryft-push`. Each subscription sets `oidc_token.audience` **explicitly** to its service's `run.app` URL. Cloud Run IAM admits only `ryft-push` (`roles/run.invoker`) to scoring and notify. **On all four services** the app verifies the token on every `/push/*` and `/internal/*`: RS256 only; `exp` required; iss `https://accounts.google.com`; `aud` equal to the configured URL (never the `Host` header); `email` = `ryft-push@…` with `email_verified`; and `sub` = that SA's unique ID. On api and ingest (public) this check is the only gate. On scoring and notify it is the backstop if IAM drifts. It reuses only `internal/auth`'s JWKS keyfunc, with Google's JWKS: the Supabase parser hard-codes `aud=authenticated` and its role checks. `Authorization` is never logged. A token isn't bound to the body and lives an hour, so a captured token could replay or inject events for that hour. D100 makes a replay harmless, and an injected event only triggers a rescore or a push the user would have got anyway |
| Ack | `basic.ack` | a 2xx response. Wrapped envelope (`message.data`, `attributes`, `messageId`, `publishTime`, `orderingKey`, `deliveryAttempt`) |
| Ack deadline | prefetch + consumer drain | scoring 300 s (= its Cloud Run request timeout), notify 60 s, ticks 600 s (the maximum) |
| Retries | TTL tiers 5 s / 30 s / 5 min, 6 attempts (~16 min) | retry policy: min backoff 5 s, max 300 s, exponential. `max_delivery_attempts = 10` (~20 min: 5+10+20+40+80+160+300+300+300 s). Allowed range 5–100 |
| Dead letters | `ryft.dlx` → `dlq.*`, 14 days, headers `Ryft-*` | the dead-letter policy forwards to `ryft-dlq-<consumer>`. The topic keeps messages 14 days. The hold subscription keeps them 7 days (the subscription maximum). Pub/Sub adds `CloudPubSubDeadLetterSource*` attributes, but it can't add our `Ryft-Error`: the last error is in the log, keyed by `messageId`. The Pub/Sub service agent needs `publisher` on the DLQ topic and `subscriber` on the source subscription |
| Bad or undecodable event | straight to the DLQ | the handler publishes it to its own DLQ topic with `ryft-error`, then acks. If that publish fails: 500, so the retry policy dead-letters it anyway (`DeadLetterPublishFailing`) |
| `events.Permanent` | dropped and counted | 204, counted as dropped |
| Redelivery (`events.Redelivered`) | tier copy, dead consumer, replay | `deliveryAttempt > 1` or `ryft-replayed=true` → `events.WithRedelivery(ctx)`. `deliveryAttempt` is set only when a dead-letter policy exists *(unchecked)*: every event subscription has one |
| Deferral (notify quiet hours) | wait tiers 1 m / 15 m / 1 h, not an attempt | Pub/Sub has no per-message delay, and backing off counts as attempts. notify acks and writes an **outbox row with a not-before time** (`publisher = notify`, topic `ryft-notify-wait`, new column `outbox.available_at`). The additive migration also grants `ryft_notify` the outbox, which 0026 grants only to ingest, api and scoring, with an RLS policy limited to `publisher = 'notify'`. The tick relays due rows. Precision is the tick: a push at the end of quiet hours can be up to 15 min late. The knob is a second Scheduler job every 5 min for `tick-notify` (still free, §7) |
| Idempotency | D100 | unchanged and still required. Pub/Sub delivers at least once, a message can arrive again even after an ack, and exactly-once delivery isn't available for push |
| Ordering | oldest first from the outbox, one consumer (D17) | the relay already publishes oldest first and stops at the first failure. Scoring's two topics carry `ordering_key = user_id` with `enable_message_ordering`, so Pub/Sub has **one outstanding message per user** and holds that user's later ones until it is acked or dead-lettered. Ordered publishing needs the regional endpoint `europe-west1-pubsub.googleapis.com` *(unchecked)*. Not ordered: across the two topics, and notify (as today, "not needed": `MESSAGING_DESIGN.md` → Guarantees). D17 becomes "scoring max-instances 1, one message in flight per user" with concurrency 4. Concurrent users are safe, as the timer and the consumer already run concurrently today |
| Saturation | a queue absorbs it | a busy scoring instance answers 429, and a 429 counts as an attempt. 10 attempts, concurrency 4 and per-user ordering make a DLQ from load unlikely at this scale. `PushBacklog` alerts first (§6) |
| Replay | `scripts/dlq.sh replay` | `scripts/dlq.sh` rewritten on `gcloud pubsub subscriptions pull --auto-ack=false`: republish to the original topic with `ryft-replayed=true`, then ack. Same rule as D150: only keys the consumer handles, and only when the letter's key is its topic's |

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
  - `Grace` 10 s (Cloud Run's SIGTERM-to-SIGKILL window *(unchecked)*), with the D169 budget test re-pinned.
- **`internal/auth`.** `GoogleOIDC` verifier (issuer, audience, the expected SA).
- **`internal/httpx`.** h2c (§3), `ClientIP` for Google's front end (§5), `slog` keys for Cloud Logging (§6).
- **`internal/metrics`.** An OTLP/HTTP JSON exporter, stdlib only, built on the existing Prometheus registry's
  `Gather()` (§6).
- **`cmd/*`.** Each `main` picks the runtime. Scoring and notify serve HTTP, but only `/push/*` and `/internal/tick`,
  and they stay private: D17's "no user HTTP" holds.

**Locally, `make up` keeps working.** compose replaces RabbitMQ with the **Pub/Sub emulator** (the gcloud emulator
image). An init container applies the generated topology, with push endpoints `http://scoring:8080/push/…`. Push auth
is off only under the same explicit insecure flag as apikey mode (`ALLOW_INSECURE_PUSH=true`, refused when
`K_SERVICE` is set, which it is on Cloud Run). The emulator supports push and ordering, but dead-letter policies and
retry policies are *(unchecked)*. So the handlers' status mapping and DLQ publish are pinned by Go tests with recorded
envelopes, and a CI job runs publish → push → ack against the emulator. Handler tests don't need a broker any more.

---

## 3. Limits on Cloud Run

**Request size.** Cloud Run caps an HTTP/1 request at 32 MiB *(unchecked)*, and `POST /v1/sync` allows a 32 MiB
**body** (`httpx.MaxBodySync = 32 << 20`), so a full-size sync plus its headers would be refused by Google's front end
with an HTML 413. **Decision: HTTP/2 end to end** (`--use-http2`) on all four services. Go 1.24's
`http.Server.Protocols` with `SetUnencryptedHTTP2(true)` serves h2c with prior knowledge from the stdlib, so there's
no new dependency, and HTTP/1 stays on for local use. The contract stays as it is. B0 verifies that a 33 MiB body
reaches ingest through the domain mapping and gets *our* JSON 413. If it doesn't, the fallback is a contract change
(a 31 MiB cap, api-contract-owner), not a load balancer.

**Timeouts.**

| Service | Request timeout | Concurrency | Memory | Max instances | Why |
|---|---|---|---|---|---|
| api | 60 s | 40 | 512 MiB | 3 | the import's 50 s budget (D140) |
| ingest | 120 s | 4 | 1 GiB | 2 | 4 × 32 MiB bodies, decoded |
| scoring | 300 s | 4 | 512 MiB | **1** | D17 as amended in §2.2; tick budget 240 s |
| notify | 60 s | 10 | 256 MiB | 1 | APNs calls |
| `migrate` job | 600 s task timeout | — | 512 MiB | 1 task | migrations take a lock timeout of 5 s each |

All services run 1 vCPU, request-based billing (CPU only during requests), `min-instances 0`, and startup CPU boost.
The services' own `WriteTimeout`s (60 s) stay at or below the Cloud Run timeout.

**Cold starts** *(unmeasured)*: the distroless Go binary takes ~0.3 s, the pool's first TLS + SCRAM login to the
pooler ~0.1 s, Supabase's JWKS ~0.1 s, and `WaitMigrated` is one query. So expect roughly 0.5–1.5 s on the first
request after idle. The app already retries 503s. The synthetics (§6) hit api and ingest every 10 min, which keeps
them warm part of the time at no extra cost. `min-instances 1` on api would cost several euros a month *(unchecked)*
and is the user's choice (§13).

**Every background goroutine and ticker, with request-only CPU:**

| Where | Today | On Cloud Run |
|---|---|---|
| api `accounts.reconcileEvery` (15 min) | goroutine | tick |
| api `confirmGuard.Run` (hourly) + its `Refresh` at start | goroutine, plus a Supabase call during startup | tick, plus a lazy first `Refresh` on the first invite request, so it's off the cold start path. Until then a bound invite is refused, fail closed as today |
| api `purgeInviteEmailsEvery` (hourly) | goroutine | tick (idempotent, so every 15 min is fine) |
| outbox relay of api, ingest, scoring (kick + 2 s poll) | goroutine | inline drain in the request (§2.3), plus `Sweep` in the tick |
| scoring `every(15 min, pass)` + housekeeping | job | tick with a 240 s budget. `learner.tried`, `catchUp.failed` and `catchUp.next` lived in memory and are lost between instances: a failing user is retried every tick instead of once a day, and a pass starts from the first user. Harmless at ten users. A small `job_state` table would fix it if the budget ever runs out |
| notify `every(15 min, staleCheck)` | job | tick |
| `db.Watch` (30 s ping) in every service | goroutine | gone. The tick pings and sets `ryft_db_up`, and the synthetics probe `/readyz` from outside (§6) |
| `httpx.ServeMetrics` `:9090` | listener | gone. OTLP push at the end of each tick and on SIGTERM (§6) |
| RabbitMQ consumers | goroutines | push handlers |
| `app` start watcher, watchdog, readiness goroutines | goroutines | stay. They run during startup and shutdown, when CPU is allocated |
| api insight cache, ingest's `expectedEvery` cache, the rate limiter | per process | per instance. A cold instance starts empty (§5: the limiter) |

**Health paths.** Startup and liveness probes hit the container directly (`/readyz`, `/healthz`). Cloud Run's front
end reserves some paths that end in `z` *(unchecked)*. B0 checks whether `https://api.ryftlab.de/healthz` reaches the
container. If it doesn't, the synthetics use `GET /v1/model` instead. `/readyz` on a public service must not show
dependency errors to the internet. Today it returns `err.Error()` (`httpx.go`). On Cloud Run it answers a generic
`503`, logs the cause, and caches the result for 5 s, because every hit costs a pooler round trip.

---

## 4. Database

**Pooler.**
- The services use Supavisor **transaction mode (port 6543)**. Cloud Run's churn (cold starts, scale-out, SIGTERM)
  makes many short-lived clients, and transaction mode takes a server connection only for each transaction.
- pgx's default mode caches named prepared statements, which a transaction pooler can't hold. `DATABASE_URL` gets
  `default_query_exec_mode=exec` (pgx's unnamed statement), so the code doesn't change. Whether Supavisor now supports
  named prepared statements is *(unchecked)*; `exec` works either way.
- The advisory locks are transaction-level (`pg_advisory_xact_lock`, `store/locks.go`), so they're safe.
- B0 greps for session state (`SET` without `LOCAL`, `LISTEN`, temp tables). PR A runs the store's integration tests
  against PgBouncer in transaction mode in CI.
- **`migrate` stays on session mode (5432).**
- **TLS is verified.** This link now crosses the internet from Google to AWS, carrying health data and the SCRAM login,
  so `sslmode=require` (which doesn't verify, `SUPABASE.md` → Connections) becomes
  `sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt`. Supabase's CA certificate is baked into the image; it
  is public.
- The pooler is IPv4. The direct host is IPv6-only *(unchecked)*, and Cloud Run's default egress is IPv4, so the
  services use the pooler.

**Instance limits.** Clients at the maximum: api 3 × 4 + ingest 2 × 4 + scoring 1 × 2 + notify 1 × 2 + migrate 2 =
**26**, against a pooler client limit of around 200 on the small compute sizes *(unchecked)*. The server pool size
(Database → Connection pooling, ~15–20 by default *(unchecked)*) holds only for open transactions. `max-instances` is
what keeps the client count bounded. The runbook checks the project's numbers (§11 step 5).

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

**Rotation (`scripts/rotate_db_password.sh`, in OPERATIONS):**
1. Add version N+1. Nothing reads it yet.
2. Execute `migrate`, pinned to N+1 for that role: it runs `ALTER ROLE` with the SCRAM verifier.
3. Deploy the service's revision pinned to N+1 (a commit to `deploy/run/<svc>.yaml`). Old instances keep their open
   connections and drain.
4. If `ECIRCUITBREAKER` appears, wait it out; never retry in a loop.
5. After a day, disable version N.

Between steps 2 and 3, old instances can't open new connections. Expect seconds to minutes, which is acceptable at
pre-alpha.

---

## 5. Security

### 5.1 What each layer gave us and what replaces it

| Layer | It gave | Replacement on GCP | Left over |
|---|---|---|---|
| **Vault** (D186–D211) | a KV per service, Kubernetes auth, generated values, audit devices | Secret Manager. Each service has its own SA (`ryft-api@`, …), with `secretmanager.secretAccessor` granted **on each secret**, never at project level. Cloud Run injects secrets as env at instance start, **pinned by version number** (§4). Audit: Cloud Audit Logs. Admin Activity is always on; **Data Access logs for `secretmanager.googleapis.com` are turned on** (tiny volume). Values are added by the user, not generated in state | Anyone with project Owner reads every secret (as Vault's root did). So does `hcp-terraform`: it can grant itself `secretAccessor`, so the HCP Terraform workspace counts as secret access. No dynamic secrets (we had none) |
| **Admission** (D120, D180, D195) | Pod Security, image and namespace rules, Argo CD's scope | There are no pods to admit. Cloud Run sandboxes every container and can't run it privileged. **Deployer SA** `ryft-deployer` (WIF, §9): `run.developer` on the four services and two jobs, `iam.serviceAccountUser` on the five runtime SAs only, `artifactregistry.writer` on the one repository. It has no IAM rights, so it can't make scoring public. That rests on `run.developer` being unable to set `invoker-iam-disabled` *(unchecked)*, which is one more reason the in-app OIDC check runs on all four services. The **WIF condition uses IDs, not names**: `repository_id`, `repository_owner_id`, `ref == refs/heads/main`, `event_name == push`, `environment == deploy`, and `job_workflow_ref == timkrebs/ryft-platform/.github/workflows/deploy.yml@refs/heads/main`. So no other workflow on main can deploy, whatever `check_workflows.sh` says. Images are deployed by digest. **P5 (`p05-auth-env`) moves into the code:** `config.LoadAuth` refuses `AUTH_MODE=apikey`, `ALLOW_INSECURE_APIKEY_AUTH` and `USER_ID` whenever `K_SERVICE` or `CLOUD_RUN_JOB` is set (with a test), and a CI check rejects those names in `deploy/run/*.yaml` | **Binary Authorization isn't used** (its price on Cloud Run is *(unchecked)*). A compromised main or `deploy` environment can deploy any image, and the deployer could swap one runtime SA for another of the five. The service YAML in git is reviewed, and branch protection is the control |
| **NetworkPolicies** (D16, D185) | default deny; ingress only from Traefik; egress per service | **Ingress:** api and ingest public (`allUsers` invoker). Scoring and notify require IAM: only `ryft-push` can invoke. Whether `ingress=internal` admits Pub/Sub push is *(unchecked)*, so ingress stays `all` and IAM plus the in-app OIDC check is the boundary. **Service to service:** none. Only Pub/Sub calls scoring and notify | **Egress is unrestricted.** Limiting it needs Direct VPC egress, firewall rules and Cloud NAT (paid), and FQDN rules need Cloud NGFW (paid). Supabase's network restrictions can't be used either (egress addresses are dynamic) |
| **mTLS to RabbitMQ** (D98) and D99's rights | broker identity, per-key publish rights | Google API TLS + per-SA OAuth tokens. One topic per key keeps the publish matrix (§2.1). Nobody holds `subscriber` except the Pub/Sub agent and the operator (DLQ hold subscriptions) | — |
| **Traefik's limiter** (D155) | per-address limit keyed by the address Traefik appends | `httpx.ClientIP` takes the rightmost `X-Forwarded-For` entry. Google's front end appends the client address *(unchecked)*. B0 sends a request with a forged `X-Forwarded-For` to both hosts from a known address: if Google appends a hop of its own, every client shares one bucket, and `ClientIP` has to skip it. Domain mappings probably publish AAAA *(unchecked)*. The admin limiter's map has no cap, which `adminauth.go` says it needs before IPv6 clients arrive, and one /48 is about 65k /64 keys. **PR A caps it** | **Limits are per instance** and reset at a cold start. The aggregate is bounded by limit × max-instances |

### 5.2 Honest gaps (free tier only)

- **No Cloud Armor, no WAF.** Google's front end gives basic DDoS protection only. Cost-DoS is bounded by
  `max-instances` and the budget alert (§7).
- **No organisation** (a personal billing account), so no org policies: no resource-location constraint, and no
  `iam.disableServiceAccountKeyCreation`. The rule is "no SA keys" except one (next point), and the runbook checks it
  (`gcloud iam service-accounts keys list` per SA).
- **Grafana's Cloud Monitoring data source needs an SA key.** Grafana Cloud doesn't run on GCE, and WIF for it is
  *(unchecked)*. The SA `grafana-reader` holds `monitoring.viewer` only (metrics, no data, no logs), and its key is
  stored only in Grafana Cloud and rotated yearly. This is the one JSON key.
- **Logs at Google** include Cloud Run's request logs: client IP, user agent, path. They are kept in the EU bucket for
  14 days. The text in force doesn't name IP or user agent in its technical logs, so the privacy draft must (§8).
- **Unrestricted egress** makes the text's "each service … can reach only what it needs" false. The draft changes it
  to "can access only the data it needs" (§8).
- **Rate limits per instance** (above).

### 5.3 The data key (D211 → Secret Manager)

`scripts/recover_data_key.sh` lives on #161's branch, not on main (§10 decides how it lands). It still reads the newest
state version of `ryft-prod-euc1-vault` and writes the base64 key to a 0600 file. The GCP path skips Terraform state
entirely:

1. Run `scripts/recover_data_key.sh`. Note the version, fingerprint and length (must be 32).
2. Add it: `gcloud secrets versions add data-key --data-file="$HOME/.config/ryft/data-key-api"`. The secret is in
   europe-west1. `prevent_destroy` is set on the container, and a delayed version destroy is set if the provider
   offers it *(unchecked)*.
3. Compare without printing:
   `gcloud secrets versions access latest --secret=data-key | shasum -a 256 | cut -c1-16`
   must equal the script's fingerprint. If it differs, stop.
4. Keep the value in 1Password as the second copy (Secret Manager's destroy is final), then delete the local file.
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
- Outside that bucket: the `_Required` bucket (audit logs, 400 days, global *(unchecked)*), Cloud Trace and Error
  Reporting. Error Reporting stays off. The privacy draft names the audit logs.
- Logs stay at Google. Grafana Cloud doesn't query or receive them, so the privacy text's "Grafana: metrics without
  personal data" stays true.
- Log-based alerting is the Cloud Logging console's job, and it is limited at pre-alpha (§13).
- D52 (CloudWatch) is superseded.

**Metrics: two sources, one Grafana.**
1. **Platform metrics, read where they are.**
   - Sources: Cloud Run request count by code class, latencies, instance count and billable time; Pub/Sub undelivered
     messages, oldest unacked age and dead-letter counts per subscription; Scheduler attempts.
   - Grafana Cloud reads them through the **Google Cloud Monitoring data source** (`grafana-reader`, §5.2).
   - System metrics are free *(unchecked)*, and Monitoring API reads may be charged past a free allotment
     *(unchecked)*. So Grafana-managed rules on this source evaluate every 5 min, not every 1 min.
2. **App metrics (`ryft_*`) pushed by the service.**
   - The format is OTLP/HTTP with JSON encoding to Grafana Cloud's OTLP gateway, built on the existing Prometheus
     registry (`Gather()`), stdlib only, no new dependency.
   - Pushed at the end of each tick (every 15 min, while CPU is allocated) and on SIGTERM.
   - Series carry `service.instance.id`. Rules use `increase()` over ≥ 30 min.
   - The token is one Secret Manager secret, `otlp-token`: a write-only token that the four service SAs can access.
   - The 10k-series free tier *(unchecked)* is far away.

**Traces.** None from the app at pre-alpha. Cloud Run's request logs and its automatic request spans in Cloud Trace
*(unchecked)*, joined by the trace field, are enough to follow a sync. Beyla and Tempo are gone.

**Synthetics.** Grafana Synthetic Monitoring stays as code (#20), on the new hosts:
- `https://ryftlab.de/`, `/privacy`, `/de/privacy`, `/.well-known/apple-app-site-association` (content type and body
  marker, as today), and the `www` redirect;
- `https://api.ryftlab.de/healthz` and `https://ingest.ryftlab.de/healthz` (or `/v1/model`, §3).

That's 7 checks, every 10 min, from 2 probes: about 60k executions a month, inside a 100k free tier *(unchecked)*.

**Alerts.** The rules stay code. App metrics stay Prometheus-format rules in `deploy/grafana/alerts` (mimir ruler, the
same tooling). Cloud Monitoring signals become Grafana-managed rules provisioned by Terraform. The group limit (≤ 18)
still applies.

| Kept (source) | Rewritten on a new source | New | Gone |
|---|---|---|---|
| Panics, TextRefusedByDatabase, AuthUnavailable, RateLimited, SyncToScoreSlow, DeadLetterPublishFailing, PublishFailing, OutboxBacklog, OutboxStuck, ScoringErrors, NoSync, APNsFailing, PushFailing, APNsRejectsOurKey, AccountDeletionIncomplete, NewFeedback, AdminUnauthorized, AdminWebUsed, InviteEmailPurgeFailing (app OTLP) | HTTP5xxRatio and SyncSlow (Cloud Run metrics); DatabaseDown (the tick's `ryft_db_up`); DeadLetters (`dlq-*-hold` undelivered > 0, replacing RabbitMQDeadLetters too); SiteDown, SiteProbeFlapping, SiteWwwRedirectBroken, SiteCertificateExpiring, SyntheticsMissing (synthetics, new hosts) | **TickMissing** (no tick handled by a service for 45 min); **PushBacklog** (oldest unacked > 15 min on a scoring or notify subscription, replacing RabbitMQBacklog); **Saturated** (Cloud Run 429s); the **budget** alerts come by email from Billing, not Grafana | PodRestarting, ServiceExited, DatabaseFlapping, CertificateExpiring, RabbitMQAlarm, RabbitMQNodeDown, AMQPDisconnected, EventsUnroutable (a publish to a missing topic fails → PublishFailing), all Argo\*, all Admission\*, all Vault\* |

---

## 7. Costs a month at pre-alpha load

**Assumed.**
- 10 testers, 4 background syncs a day each (1,200 syncs a month, ~1 MB each, 2 s of CPU).
- 50 api requests a day each (15,000 a month, 0.2 s).
- ~3 Pub/Sub deliveries per sync (3,600, 1 s), ~3,000 cold starts (1 s).
- Ticks: 4 services every 15 min = 11,520 requests (scoring ~2 s, the others ~0.2 s).
- Synthetics: 2 of the 7 checks hit Cloud Run, ~17k requests at 0.1 s.
- Total about **25k vCPU-seconds, 12k GiB-seconds and 50k requests**.

| Item | Use | Free tier *(unchecked)* | List price above it *(unchecked)* | €/month |
|---|---|---|---|---|
| Cloud Run CPU | ~25k vCPU-s | 180k vCPU-s (request-based) | Tier 1 $0.000024/vCPU-s | 0 |
| Cloud Run memory | ~12k GiB-s | 360k GiB-s | $0.0000025/GiB-s | 0 |
| Cloud Run requests | ~50k | 2M | $0.40/M | 0 |
| Pub/Sub | ~10k messages, ~20 MB (1 KB minimum each) | 10 GiB | $40/TiB | 0 |
| Cloud Scheduler | 1 job | 3 jobs per billing account | $0.10/job | 0 |
| Secret Manager | 11 secrets (5 DB passwords, Supabase secret key, `ADMIN_API_KEY`, Apple `.p8`, APNs `.p8`, `data-key`, `otlp-token`), 1 active version each; ~10k accesses (secrets resolve at instance start) | 6 versions, 10k accesses | $0.06/version, $0.03/10k | ~0.30 |
| Artifact Registry | ~0.4 GB (5 images × 5 digests, cleanup policy) | 0.5 GB | $0.10/GB | 0–0.05 |
| Cloud DNS | 1 zone, < 1M queries | none | $0.20/zone, $0.40/M | ~0.20 |
| Cloud Logging | < 1 GiB | 50 GiB per project | $0.50/GiB | 0 |
| Cloud Monitoring | system metrics; reads every 5 min | system metrics free; reads unchecked | unchecked | 0 |
| Firebase Hosting | ~100 MB stored, < 1 GB out | 10 GB stored, 360 MB/day | Blaze per GB | 0 |
| Egress to Supabase and APNs | ~1.5 GB | unchecked | ~$0.12/GB | ~0.20 |
| Grafana Cloud, HCP Terraform, Supabase | unchanged | — | — | unchanged |
| **Total GCP** | | | | **≈ €0.50–1** |

**Where it stops being free.** The fixed part (ticks, synthetics, cold starts) is ~12k vCPU-s, and each tester adds
~1.3k. The Cloud Run CPU free tier lasts to about **100–130 testers** at these habits. Requests and Pub/Sub come much
later. Secret Manager and DNS cost cents from day one. Other bills arrive outside that curve:
- a **load balancer** (~$18/month, if domain mappings fail);
- **min-instances 1** on api (several €);
- europe-west3's Tier 2 (+~40 % above the free tier);
- a rescore of every account (unmeasured, an operator job).

**Worst case** (all 7 instances busy around the clock): about $450 a month. Max-instances bound it, and the budget
catches it.

**Budget alert (Terraform, `infra/gcp/budget.tf`).**
- One budget on the billing account, filtered to the project, **€5 a month**.
- Thresholds 50 %, 90 %, 100 % actual and 100 % forecast.
- Email to the billing admins, plus a Pub/Sub topic `ryft-budget` for later automation.
- A second budget at **€20** is the "stop now" line. The runbook's actions:
  - `gcloud run services remove-iam-policy-binding api --member=allUsers --role=roles/run.invoker`, and the same for
    ingest;
  - `firebase hosting:disable`, because max-instances doesn't bound Hosting's egress, and a project with billing is on
    the Blaze plan, so Spark's caps don't apply;
  - set `public_invokers = false` in `infra/gcp`'s tfvars before the next apply, which would otherwise add `allUsers`
    back.
- Budgets don't cap spending, and billing data lags by hours *(unchecked)*. Max-instances is the hard cap for Cloud
  Run. For Hosting, the alternative is a **second project without billing** (Spark, hard caps), at the price of a
  second project to run. That is the user's choice (§13).
- D55's "budget in USD" becomes EUR, the billing account's currency.

---

## 8. The legal pages without a web frontend

**Feasible, and almost free.**
- `web/` is already a full static export (`output: 'export'`, `out/`).
- nginx adds only headers and routing rules, and `firebase.json` can express all of them:
  - `cleanUrls: true` and `trailingSlash: false` (`/privacy` → `privacy.html`, `/x/` → 301 `/x`);
  - headers per glob: the CSP from `scripts/csp.mjs`, `Cache-Control` per location, and `Content-Type:
    application/json` for `/.well-known/apple-app-site-association`;
  - rewrites for `/(day|sleep|insights|train|auth|invite)/**` → `/open.html`;
  - `/admin` → `admin.html` with the admin CSP;
  - a 404 for `*.gz`.
- The free allowance (10 GB storage, 360 MB a day *(unchecked)*) is plenty. In a project with billing those are a
  free tier on Blaze, not caps (§7).

**Recommendation: host the whole export, not only the four pages.** It's the same build and the same tests, and it
brings back the AASA (universal links, password AutoFill, invite links), which D215 lost. The user decides (§13).
Either way, `/privacy`, `/de/privacy`, `/terms`, `/support`, `/impressum` load again, which REBUILD step 12 required
before any tester signs in.

**What it needs (PR C1).**
- A generator `web/scripts/firebase-json.mjs`, so the CSP hashes stay one source.
- Tests:
  - `nginx.test.ts` becomes `hosting.test.ts`, asserting the same headers and redirects against `firebase.json`;
  - `make web-e2e` runs against **superstatic** (the open-source server Firebase Hosting uses, which reads
    `firebase.json`).
- CI deploys `out/` with `firebase-tools` (pinned) through WIF, after `web-build` and the brand check.
- The custom domains are the apex and `www` (a redirect to the apex), with DNS records in Cloud DNS (§11).
- **CAA must allow Google's CA:** today it is Let's Encrypt only (D214). Google-managed certificates (Hosting and the
  Cloud Run mappings) need `issue "pki.goog"` *(unchecked whether Hosting still uses Let's Encrypt too)*. The zone
  carries both.
- `check_policy_in_force.sh` reads the deployed Hosting release instead of the overlay's web pin.
- The nginx image, `services/web` and its Synthetic labels go in PR D.

**The privacy text (PR C2, a draft PR, `status` left alone).**
- A new version, dated when the user decides, `status: draft`, with `PRIVACY_POLICY_VERSION` moved in the same PR
  (where it lives after `deploy/k8s` is gone: §9).
- It stays a draft PR, because a draft version on main blocks every deploy (STATUS, F1).
- Changes:
  - **Google Cloud replaces Amazon Web Services** as processor: Cloud Run, Pub/Sub, Secret Manager, Cloud Logging,
    Cloud DNS and Firebase Hosting. The location is Belgium (europe-west1), with the website served worldwide from the
    CDN. The terms are Google's Cloud Data Processing Addendum, plus Firebase's Data Processing and Security Terms for
    Hosting *(which terms apply to Hosting is unchecked)*, with the EU-U.S. Data Privacy Framework plus SCCs for any
    access from the US.
  - **Technical logs** (text §3): add "the IP address and user agent of every API request, in Google's request logs,
    14 days" and the audit logs of administrative access.
  - **Visiting ryftlab.de** (text §3): today it says the IP "is not stored" and is used "to limit requests per
    address" by our nginx. On Hosting, Google's CDN handles the IP and keeps its own access logs, and there's no
    limiter of ours. The bullet is rewritten to say that.
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

- **Terraform `infra/gcp` (the user applies; HCP Terraform workspace `ryft-gcp`, GCP dynamic credentials) owns:**
  project services, the service accounts, every IAM binding (services' invokers included), secret containers and
  their accessor bindings, the Pub/Sub topology (from the generated tfvars), the Scheduler job, Artifact Registry and
  its cleanup policy, the WIF pool for GitHub, the domain mappings, the Cloud DNS zone, the log bucket and sink, the
  budgets and the Grafana-managed rules.
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
  6. Firebase Hosting when `web/` changed.

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
- D55 (budget in EUR; the site on Hosting), D57, D64, D94–D96, D97, D98;
- D99 (amended: Pub/Sub IAM), D102–D105;
- D107 (amended: Hosting instead of the nginx image);
- D120, D121, D151, D155 (amended: §5.1), D169 (amended: 10 s), D175 (amended: §9), D180, D182, D183, D185;
- D186–D205 (Vault and admission), D207–D210;
- D211 (amended: §5.3), D215 (superseded by §8).

**Kept:** D100 and D101 (amended: inline relay, not-before), D213 (amended: no `argocd.` and `rabbitmq.` hosts, plus
`ingest.`), D214 (the records move to Cloud DNS).

**First, independent of everything:** the user destroys the half-built `infra/aws` **except `infra/aws/dns`**, which
still serves the M365 mail. A NAT gateway alone costs money every hour.

| # | PR | Size | Contents | After it, the user |
|---|---|---|---|---|
| — | **#161** | — | the user's call (§13). Recommended: merge it as it is (reviewed; it applies nothing without a cluster). It carries D207–D215, the domain cut and `recover_data_key.sh`, and the D numbers stay contiguous | — |
| B0 | a commit on this branch once web access is allowed (docs only) | S | check every *(unchecked)* figure against the live pages, with dates, and update §1, §3, §7 | — |
| A | `backend: Pub/Sub and Cloud Run` | **XL** (~3–4k lines with tests) | §2.3, §3, §4's DSN and `PGPASSWORD`, h2c, OIDC, OTLP, slog, the outbox migration (`available_at`), compose with the emulator, `cmd/pubsub-topology`, amqpbus and RabbitMQ deleted. `API.md` Base URLs (api-contract-owner), `MESSAGING_DESIGN.md` rewritten, DECISIONS D216–D227. Deploys nothing | — |
| B | `infra, deploy: GCP and the deploy workflow` | **L** | `infra/gcp` (prepared, `terraform test`, never applied), `deploy/run/*`, `deploy.yml` (off until the variable `GCP_DEPLOY=on`), `PRIVACY_POLICY_VERSION`'s new home and its checks, alert rules, `OPERATIONS.md` → GCP. Disjoint from A, so it is built in parallel | runbook steps 1–8 (§11) |
| C1 | `web: Firebase Hosting` | M | §8, merged before the testers return | step 9 |
| C2 | `legal: privacy draft (Google Cloud, Microsoft via GoDaddy)` | S, **draft** | §8, `status: draft` | decides and publishes; consent again |
| D | `remove: Kubernetes, Argo CD, Vault, admission, AWS` | L (mostly deletions) | `deploy/k8s`, `deploy/argocd`, `deploy/rabbitmq`, `deploy/monitoring`, `infra/aws/kubernetes` (incl. admission charts, `admission-e2e`, `argocd-luatest`), `infra/vault`, `infra/aws` except `dns` (after the destroy), the kubectl scripts (`admission_e2e`, `check_argocd`, `check_operators_scope`, `check_registry`, `install_kubectl`, `vault_diff`; `dlq`, `rescore_all`, `trace` rewritten in A/B), `admission-e2e.yml`, `make k8s-*`, `promote.sh`, `hold.sh`. `ARCHITECTURE.md` and `OPERATIONS.md` brought up to date. Only after production on GCP passes the smoke | — |
| E | `dns: Route 53 retired` | S | `infra/aws/dns` removed, after the NS switch + 48 h and the user's destroy. STATUS closed out | — |

A and B can be built in parallel (one writer each: backend-engineer + database-engineer + test-engineer on A;
infra-engineer + gitops-engineer on B), then reviewed by code-reviewer, security-privacy-reviewer and sre.

---

## 11. The user's runbook, in outline (each step has a stop rule)

| # | Step | Stop if |
|---|---|---|
| 0 | `cd infra/aws && terraform plan -destroy`, then destroy (not `infra/aws/dns`) | the plan lists anything of `dns`, or a resource not RYFT's (shared account: by IDs, never by tag) |
| 1 | **Billing and project:** a billing account in EUR, project `ryft-prod-<suffix>`, 2-step verification on the Google account, no other principals | the account can't be EUR, or the project ends up in someone's organisation |
| 2 | **HCP Terraform credentials:** a one-time bootstrap (a short gcloud script in OPERATIONS) creates the WIF pool for `app.terraform.io`. Its condition pins `terraform_organization_id` and `terraform_workspace_id` (IDs, not names). There are **two SAs**: `hcp-terraform-plan` (viewer, for plan runs, `terraform_run_phase == plan`) and `hcp-terraform-apply` (apply runs only). Set `TFC_GCP_PROVIDER_AUTH`, `TFC_GCP_PLAN_SERVICE_ACCOUNT_EMAIL`, `TFC_GCP_APPLY_SERVICE_ACCOUNT_EMAIL` and `TFC_GCP_WORKLOAD_PROVIDER_NAME` on workspace `ryft-gcp` | a plan fails authentication, or the apply SA needs `owner` |
| 3 | **`infra/gcp` apply 1:** everything except the services' IAM and the mappings. **The budget alert first:** check that the test email arrives | the plan has any change or destroy, or the add count differs from B's PR body |
| 4 | **The DNS move.** `scripts/dns_compare.sh` (PR B) digs every record set of the Route 53 zone against the Cloud DNS name servers: MX, the apex TXT, DMARC, the DKIM CNAME, autodiscover, SRVs, CAA now with `pki.goog`. Check `dig DS ryftlab.de @a.nic.de`: there must be no DS (DNSSEC off). Set GoDaddy's name servers to the four `ns-cloud-*`. Wait until `dig NS ryftlab.de @a.nic.de` shows Google, then send and receive a test mail | any record differs, except CAA, whose one intended difference is the added `pki.goog` (the script checks it exactly); or a DS exists; after the switch, mail fails → set the old NS back at once (Route 53 is still live) |
| 5 | **Secret values:** `gcloud secrets versions add … --data-file` for the DB passwords, the Supabase secret key, `ADMIN_API_KEY`, the Apple and APNs `.p8` keys from 1Password, the OTLP tokens. The data key via §5.3 | the data key's fingerprint differs or its length isn't 32 (then D211's fallback) |
| 6 | **Supabase:** the transaction pooler host and port 6543 for the services, session 5432 for migrate, the pool size (Database → Connection pooling), Data API still off, Site URL and redirect URLs on `ryftlab.de`. Test one connection as `ryft_api.<ref>` on 6543 | the login fails or `ECIRCUITBREAKER` appears: stop and wait it out, don't retry in a loop |
| 7 | **First deploy:** repository variable `GCP_DEPLOY=on`, then run `deploy.yml` by hand. Nothing is public yet | `migrate` doesn't log `migration applied` / `up to date`, or a revision isn't ready |
| 8 | **`infra/gcp` apply 2:** invokers (`allUsers` on api and ingest, `ryft-push` on scoring and notify), the domain mappings for `api.` and `ingest.`, after domain verification (TXT, Search Console). Then `make smoke-prod`, and a 33 MiB sync gets our JSON 413 | the plan touches more than IAM and mappings, the certificate stays pending for over an hour, or the smoke fails |
| 9 | **Site and Apple URLs:** C1 merged and Hosting live on the apex and `www`. In App Store Connect the privacy, support and marketing URLs are on `https://ryftlab.de/…`. The app build uses `api.` + `ingest.ryftlab.de`. The Sign in with Apple return URL stays Supabase's. Check the AASA's team ID | `/privacy`, `/de/privacy` or `/impressum` don't load, or the AASA's content type isn't JSON: don't submit or invite |
| 10 | **Observability:** OTLP tokens, `grafana-reader`'s key in the data source, `make alerts-apply`, the synthetics applied, one test alert | no `ryft_db_up` from any service after 30 min (the ticks aren't arriving) |
| 11 | **Testers:** only after C2 is decided and published | — |
| 12 | **Clean-up:** 48 h after the NS switch, `infra/aws/dns` destroy, then PR E. Delete the AWS HCP Terraform workspaces and the AWS and GHCR secrets in GitHub | mail fails during the 48 h |

---

## 12. Proposed decisions (D216 and up)

- **D216** RYFT runs on GCP serverless (Cloud Run, Pub/Sub, Secret Manager, Scheduler, Artifact Registry, Cloud DNS,
  Firebase Hosting). Nothing stays in the HashiCorp AWS account. *(the user's decision, 2026-11-14)*
- **D217** Region europe-west1 and Cloud Run domain mappings. Two API hosts, `api.` and `ingest.ryftlab.de` (§1).
- **D218** One Pub/Sub topic per event key with push and OIDC. Retries 5–300 s with 10 attempts, a DLQ per consumer,
  per-user ordering on scoring's topics. Topology is generated from Go (§2).
- **D219** Request-based CPU. All timers become one Scheduler job fanned out through `ryft-tick`. The outbox relays
  inline, with a tick sweep. Deferral uses an outbox not-before (§2.2, §3).
- **D220** HTTP/2 end to end (h2c, stdlib) keeps the 32 MiB sync unchanged (§3).
- **D221** Services on the transaction pooler with `default_query_exec_mode=exec` and `verify-full`, migrate on
  session mode. Role passwords in Secret Manager via `PGPASSWORD`, with a fixed rotation order (§4).
- **D222** Secret Manager with one SA per service, secret-level bindings, and versions pinned by number. Values are
  never in Terraform state. The data key's path (§5.3). P5 lives in `config.LoadAuth`. Push and tick tokens are
  checked in-app on all four services (§2.2, §5.1).
- **D223** CI owns the Cloud Run revisions and jobs; Terraform owns everything else. Deploys use WIF, images go to
  Artifact Registry, migrate runs before the deploy (§9).
- **D224** Logs stay in Google, in an EU bucket. Grafana reads Cloud Monitoring through a viewer-only SA key, and the
  app pushes OTLP at the tick and on SIGTERM (§6).
- **D225** The site on Firebase Hosting, generated from the same rules. CAA admits `pki.goog` (§8).
- **D226** A €5 budget, a €20 stop line, max-instances as the hard cap (§7).
- **D227** The removal order and the superseded decisions (§10).

## 13. The user decides

1. **Region:** europe-west1 with domain mappings (recommended), or europe-west3 with a load balancer (~$18 a month) or
   `run.app` URLs.
2. **Two API hosts** (`ingest.ryftlab.de` added; the app build sets both). Recommended. The alternative is a load
   balancer.
3. **#161:** merge it as it is (recommended), or close it and carry D213–D215 and `recover_data_key.sh` into PR A.
4. **The site:** the whole static export on Firebase Hosting (recommended), or only the legal pages.
5. **The privacy draft's wording and its date** (PR C2), including the open D214 questions (the tenant's location,
   GoDaddy's DPA).
6. **`min-instances`:** 0 everywhere (recommended), or 1 on api against cold starts (several € a month, unchecked).
7. **The one SA key** for Grafana's Cloud Monitoring data source (`monitoring.viewer`), or no platform metrics in
   Grafana (Cloud Monitoring's console only).
8. **Accepted gaps:** unrestricted egress, no Cloud Armor, no Binary Authorization, per-instance rate limits, no
   organisation policies.
9. **Hosting's cost bound:** the same project with `firebase hosting:disable` as the stop line (recommended for one
   operator), or a second project without billing.

## 14. Top risks

| Risk | Mitigation |
|---|---|
| Domain mappings are Preview and limited to some regions *(unchecked)* | B0 checks first. The fallback is `run.app` URLs in the app's config, or a load balancer when paying |
| The 32 MiB sync hits the HTTP/1 cap | h2c end to end. Step 8 tests a 33 MiB body. The fallback is a contract change, not silence |
| The pooler in transaction mode breaks a query that holds session state | `exec` mode, a grep in B0, the store's integration tests against PgBouncer in transaction mode in CI (PR A) |
| Work after the response never runs (request-only CPU) | inline relay before answering, the tick sweep, `OutboxBacklog` and `TickMissing` alerts |
| 429s from a saturated scoring count as attempts and dead-letter | concurrency 4, 10 attempts, per-user ordering, `PushBacklog`; replay is a script |
| The DNS move breaks mail | the record compare before the switch, Route 53 kept 48 h, rollback is the NS at GoDaddy |
| The data key is lost or mistyped | fingerprint compare, a 1Password copy, `prevent_destroy`, D211's fallback |
| Cost surprise | max-instances, a €5 budget, the €20 stop line (invokers off, Hosting off) |
| Testers consent to a text that misstates logging | C2 names IP and user agent in request logs, the CDN's handling, and corrects "reach only" (§8); the bucket keeps 14 days |
| A debug commit turns on apikey mode on a public service | P5 moves into `config.LoadAuth` (refused on Cloud Run) and a CI check of `deploy/run/*.yaml` |
| One `versions add` silently changes a running service (data key, DB password) | secrets pinned by version; rotation is a reviewed deploy (§4) |
| Unchecked prices and limits | B0 before any apply; every figure here carries the mark |
