# RYFT — observability

Each service (ingest, scoring, api) serves Prometheus metrics on `GET /metrics` on its
own port, **9090** (`METRICS_PORT`, no auth), apart from the API and probe port 8080.
The Ingress only routes `/v1/*`, so `/metrics` is never public. Logs are JSON (`slog`)
on stdout and shipped to CloudWatch; see **Logs** below.

## Collection: Grafana Cloud

Metrics go to Grafana Cloud (free tier; D51). The collector is Grafana's
`k8s-monitoring` chart (`deploy/monitoring/k8s-monitoring.yaml`, `make
monitoring-apply`) in namespace `monitoring`: two Alloy collectors named as the
onboarding names them, `alloy-daemonset` (one per node, host network) and
`alloy-deployment` (one replica, cluster-wide work and the OTLP receiver), plus
node-exporter, kube-state-metrics, Beyla, Kepler (energy), OpenCost (cost) and
k8s-manifest-tail. Their pipelines are configured remotely in
Grafana Cloud's Fleet Management, which assigns them by collector name; the daemonset
discovers the RYFT pods through their annotations:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "9090"
prometheus.io/path: /metrics
```

The collector runs on the nodes' network, so its scrapes come from node addresses. Each
service admits port 9090 from namespace `monitoring` (`base/<service>/networkpolicy.yaml`)
and, on EKS, from the VPC range (`overlays/eks/metrics-from-nodes.yaml`). Pods share
that range, so any pod may read `/metrics`: counters and route labels, no user data.
Port 8080 stays reachable from Traefik only.

**RYFT's pod logs stay out of Grafana Cloud.** They carry users' daily scores and go to
CloudWatch instead (D52). The collector can read every container's log file
(`filesystem-log-reader`), so this is a Fleet Management setting, not a boundary:
exclude namespaces matching `ryft-.*` from pod-log collection. Check in Explore that
`{namespace=~"ryft-.*"}` returns nothing.

Alert rules: `deploy/grafana/alerts/ryft.yaml`, checked and loaded with `make
alerts-check` / `make alerts-apply`; see **Alerts** below.

A self-hosted Prometheus works the same way: a job that keeps pods with
`prometheus.io/scrape=true` and uses the annotated port, running in namespace
`monitoring`.

## Metrics

| Metric | Type | Labels | Service | Meaning |
| --- | --- | --- | --- | --- |
| `ryft_http_requests_total` | counter | `route`, `code` | all | Requests by ServeMux pattern (`GET /v1/days/{day}`, never the raw path) and status. Unauthenticated requests carry the outer pattern `/v1/`; unknown routes `unmatched`. |
| `ryft_http_request_duration_seconds` | histogram | `route` | all | Request latency. |
| `ryft_samples_ingested_total` | counter | `kind`, `type` | ingest | Records accepted by `POST /v1/sync`: `kind="sample"` with the sample type, `kind="sleep"` with the stage, `kind="workout"`. |
| `ryft_last_sync_timestamp_seconds` | gauge | — | ingest | Unix time of the last successful sync of any user. Resets on restart. (Had a `device` label before multi-user; per-user or per-device series would be unbounded.) |
| `ryft_rate_limited_total` | counter | `class` | ingest, api | Requests answered `429` by the per-user rate limit: `class="sync"` (`POST /v1/sync`), `session` (`PUT /v1/sessions/{id}`) or `default`. The recompute gate (one per minute) is not counted here. |
| `ryft_http_panics_total` | counter | `route` | all | Handler panics turned into a `500` by the recover middleware. Each also logs `msg="panic"` with the stack. |
| `ryft_auth_failures_total` | counter | `reason` | ingest, api | Rejected `/v1` requests. 401 reasons: `missing_token`, `malformed`, `expired`, `signature`, `issuer`, `audience`, `unknown_key`, `key_mismatch`, `role`, `anonymous`, `subject`, `api_key`, `deleted` (the account was deleted), `invalid`. 503 reasons: `jwks_unavailable`, `jwks_refresh_limited`, `internal` (user lookup failed). |
| `ryft_account_deletions_total` | counter | `step`, `result` | api | `DELETE /v1/me` and the reconciler. `step="data"` (our rows), `apple` (revoking the Sign in with Apple token) or `auth` (the Supabase auth user); `result` `ok`, `error` or `skipped` (not configured: no `APPLE_*` / `SUPABASE_SERVICE_ROLE_KEY`, or apikey mode). |
| `ryft_events_published_total` | counter | `reason`, `result` | ingest, api, scoring | Sends to the broker (by the outbox relay, D101), `ok`/`error`, on RabbitMQ also `unroutable` (no queue bound for the key: a topology change missed): `ingested` with `sync` (ingest), `recompute` and `session` (an edited or deleted completed session; api), `session.completed` as `session_completed` (api), and scoring's `scored`, `session_scored`, `refresh`. An `error` here is retried by the relay; an event is lost only if its outbox row could not be written (also `error`, and the sync or completion answers `503`). |
| `ryft_events_handled_total` | counter | `reason`, `result` | scoring, notify | Consumed events: `ok`, `retry` (to the next retry tier: 5 s, 30 s, 5 min), `failed` (the sixth attempt failed — dead-lettered), `bad` (unparseable — dead-lettered), `dropped` (cannot succeed, e.g. the user was deleted), `deferred` (notify: held until quiet hours end). notify's reasons are `scored`, `session_scored` and `refresh`. |
| `ryft_nats_redeliveries_total` | counter | `consumer` | scoring, notify | Messages received again, `consumer` the queue: redelivered by the broker (a consumer died holding it) or a retry tier's copy (`x-ryft-attempt` > 1). The name is from NATS, kept for the dashboards and the history (D102). |
| `ryft_days_scored_total` | counter | `result` | scoring | Days scored (`ok`/`error`; `no_consent`: skipped, consent withdrawn, D78), from events and the 15-minute timer. |
| `ryft_scoring_duration_seconds` | histogram | `result` | scoring | Time to score one day: load, compute, write (including its strength sessions). |
| `ryft_sleep_base_learned_total` | counter | `outcome` | scoring | Weekly sleep-baseline estimates (Sundays from 04:00 local, and `scoring learn-baselines`): `applied` (the baseline in use moved, the last 7 days were rescored), `kept` (same value, or set by hand), `insufficient` (fewer than 14 nights or 7 qualifying), `error`. A run of `error`s means the users' days or the baseline write failed; `applied` should be rare after the first weeks. |
| `ryft_journal_saves_total` | counter | — | api | Journal days saved (`PUT /v1/journal/{day}`). |
| `ryft_behavior_impact_seconds` | histogram | `caller` | api | CPU time of the behavior-impact computation: `caller="impact"` (`GET /v1/behaviors/impact`) or `insight` (yesterday's behaviors for the card). Its p95 passing ~0.1 s is the signal to move it into a worker (D76). |
| `ryft_flag_evaluations_total` | counter | `key`, `result` | api | Feature flags evaluated for `GET /v1/config`, `on` or `off` per flag key. |
| `ryft_admin_requests_total` | counter | `result` | api | `/v1/admin` requests: `ok`, `unauthorized` (wrong or no `ADMIN_API_KEY`), `invalid`, `error`. |
| `ryft_gate_refusals_total` | counter | `code`, `mode`, `service` | api, ingest, scoring | Requests a gate would refuse: `invite_required` (the beta gate, D79) or `consent_required` (a sync, or in scoring a day, without consent, D78). `mode="on"`: refused with `403`; `mode="warn"`: served with `X-RYFT-Gate` (D87). After a policy bump, `consent_required` rises until the testers open the app. (Was `ryft_beta_denied_total` before `61db1c4`, 2026-10-20.) |
| `ryft_consents_total` | counter | `action` | api | `granted`, `revoked`, `stale` (a POST for an outdated policy version: the app shows an old text). |
| `ryft_invite_redemptions_total` | counter | `result` | api | `POST /v1/me/invite`: `ok` (a use spent), `already` (the account had access), `invalid`. Many `invalid` from one place is someone guessing (5 an hour per user). |
| `ryft_events_dlq_total` | counter | `subject`, `result` | scoring, notify | Events dead-lettered (unparseable, or failed on the last attempt), by routing key, into `dlq.scoring`/`dlq.notify` (D99): `ok`, or `error` (the publish failed; the message was rejected, and the broker's delivery limit dead-letters it). Before the cutover, `subject` was the NATS subject (`ryft.ingested`). |
| `ryft_sync_to_score_seconds` | histogram | — | scoring | From ingest accepting a sync to scoring finishing one of the days it queued (`score_queue`, D91). The p95 is what testers wait for their scores; `SyncResponse.scoring.expected_within_sec` tells the app the same from the table. |
| `ryft_event_handler_panics_total` | counter | `subject` | scoring, notify | Event handlers that panicked: the message is retried and, at its last delivery, dead-lettered, instead of the service crashing (D92). Any is a bug. |
| `ryft_amqp_connected` | gauge | — | ingest, api, scoring, notify | 1 while the connection to RabbitMQ is up, 0 while it reconnects (backoff 1 s → 30 s with jitter). Registered when the service connects (migrate has none). |
| `ryft_amqp_reconnects_total` | counter | — | ingest, api, scoring, notify | Connections that closed and were dialled again (a broker restart, a node drain, a certificate renewal). |
| `ryft_outbox_pending` | gauge | `publisher` | ingest, api, scoring | Outbox rows not yet confirmed by the broker (every 15 s). Normally 0: the relay sends right after the commit. |
| `ryft_outbox_oldest_seconds` | gauge | `publisher` | ingest, api, scoring | Age of the oldest waiting row (0 when none waits). Above a few seconds: the relay cannot reach the broker, or it refuses (an alarm, a full queue). |
| `ryft_outbox_stuck` | gauge | `publisher` | ingest, api, scoring | Rows set aside after 50 failed sends (OPERATIONS → RabbitMQ → The outbox). |
| `ryft_outbox_relayed_total` | counter | `publisher`, `result` | ingest, api, scoring | Rows the relay sent: `ok` (confirmed and marked) or `error` (retried with backoff). |
| `rabbitmq_queue_messages_ready`, `rabbitmq_queue_messages_unacked`, `rabbitmq_queue_messages` | gauge | `vhost`, `queue` | RabbitMQ (:15692) | Per queue (`return_per_object_metrics`): waiting, delivered and not settled, all. `scoring`, `notify`, the retry and wait tiers, `dlq.*`. |
| `rabbitmq_alarms_memory_used_watermark`, `rabbitmq_alarms_free_disk_space_watermark` | gauge | `instance` | RabbitMQ | 1 while that node's alarm blocks publishers. |
| `rabbitmq_identity_info` | gauge | `rabbitmq_node`, `rabbitmq_cluster` | RabbitMQ | One series per node that reports; three when healthy. Also `rabbitmq_process_resident_memory_bytes`, `rabbitmq_disk_space_available_bytes`, `rabbitmq_connections`, `rabbitmq_channel_messages_published_total`. |
| `ryft_db_up` | gauge | `service` | all | 1 when the last database ping (every 30 s, 5 s timeout) succeeded. |
| `ryft_db_ping_failures_total` | counter | `service` | all | Failed pings: Supabase or its pooler unreachable, or the role refused. |
| `ryft_users_by_last_sync` | gauge | `age` | scoring | Accounts by the age of their newest device sync (every 15 min): `under_6h`, `6h_to_24h`, `1d_to_3d`, `over_3d`, `never`. Counts only, no user ids. |
| `ryft_feedback_received_total` | counter | `category` | api | Reports stored by `POST /v1/feedback` (`bug`, `idea`, `data`, `other`). The alert on any increase is how the operator hears about them (D83); read them with `scripts/beta_report.sh feedback`. |
| `ryft_push_notifications_total` | counter | `category`, `result` | notify | One per notification decided: `category` `MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE` or `REFRESH` (the silent push); `result` `sent`, `failed` (no device reached), `muted` (category off), `quiet` (dropped in quiet hours), `deferred` (morning push held until they end), `duplicate` (already sent that day), `throttled` (a silent refresh within 5 minutes of the last), `stale` (old day or session, deleted user), `no_token` (no registered device), `unconfigured` (no APNs key: counted, not sent). |
| `ryft_apns_responses_total` | counter | `push_type`, `status` | notify | APNs answers per device request: `alert` or `background`, HTTP status (`200`, `400`, `403`, `410`, `429`, `5xx`) or `error` (no answer). |
| `ryft_push_tokens_pruned_total` | counter | — | notify | Device tokens dropped because APNs said they are gone. |
| `ryft_train_sessions_completed_total` | counter | — | api | Strength sessions completed (the first `POST /v1/sessions/{id}/complete` of each). |
| `ryft_train_sets_logged_total` | counter | — | api | Completed sets `PUT /v1/sessions/{id}` stored for the first time. |
| `ryft_train_prs_total` | counter | `kind` | api | Personal records a session took over (`e1rm`, `max_weight`, `max_reps`, `max_volume`). |
| `argocd_app_info` | gauge | `name`, `sync_status`, `health_status`, `autosync_enabled`, … | Argo CD (controller, :8082) | One series per application, value 1; the alerts below read `sync_status` (`Synced`, `OutOfSync`, `Unknown` when manifests do not render) and `health_status` (`Healthy`, `Progressing`, `Degraded`, `Missing`). |
| `argocd_app_sync_total` | counter | `name`, `phase` | Argo CD | Sync operations by outcome: `Succeeded`, `Failed`, `Error` (a PreSync migration, a refused apply, the PostSync smoke), `Running`, `Terminating`. |
| `go_*`, `process_*` | | | all | Go runtime and process collectors. |

## Grafana dashboard — panel list

Variables: `$namespace`, a regex (default `ryft-.*`, the per-service namespaces). All rates over `[5m]` unless noted.

**Row: Health at a glance**

| Panel | Type | Query |
| --- | --- | --- |
| Time since last sync | stat (thresholds 6 h / 24 h) | `time() - max(ryft_last_sync_timestamp_seconds)` |
| Syncs (24 h) | stat | `sum(increase(ryft_http_requests_total{app="ingest",route="POST /v1/sync",code="200"}[24h]))` |
| Days scored (24 h) | stat | `sum(increase(ryft_days_scored_total{result="ok"}[24h]))` |
| API error ratio | stat (%) | `sum(rate(ryft_http_requests_total{app="api",code=~"5.."}[5m])) / sum(rate(ryft_http_requests_total{app="api"}[5m]))` |
| Pods up | stat | `sum by (app) (up{namespace=~"$namespace"})` |

**Row: Ingest**

| Panel | Type | Query |
| --- | --- | --- |
| Sync requests by status | time series | `sum by (code) (rate(ryft_http_requests_total{route="POST /v1/sync"}[5m]))` |
| Sync latency p50 / p95 / p99 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_http_request_duration_seconds_bucket{route="POST /v1/sync"}[5m])))` (repeat for 0.5, 0.99) |
| Samples ingested by type | stacked bars, 1 h buckets | `sum by (type) (increase(ryft_samples_ingested_total{kind="sample"}[1h]))` |
| Sleep segments / workouts | time series | `sum by (kind) (increase(ryft_samples_ingested_total{kind!="sample"}[1h]))` |
| Auth failures by reason | time series | `sum by (reason) (rate(ryft_auth_failures_total[5m]))` |
| Rate limited / panics | time series | `sum by (app, class) (rate(ryft_rate_limited_total[5m]))`, `sum by (app, route) (increase(ryft_http_panics_total[5m]))` |

**Row: Scoring**

| Panel | Type | Query |
| --- | --- | --- |
| Days scored by result | time series | `sum by (result) (rate(ryft_days_scored_total[5m]))` |
| Scoring duration p50 / p95 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_scoring_duration_seconds_bucket[5m])))` |
| Sleep baselines learned | stacked bars, 1 d buckets | `sum by (outcome) (increase(ryft_sleep_base_learned_total[1d]))` |
| Events handled by result | time series | `sum by (reason, result) (rate(ryft_events_handled_total[5m]))` |
| Redeliveries | time series | `sum by (consumer) (rate(ryft_nats_redeliveries_total[5m]))` |
| Publish errors | time series | `sum by (app, reason) (rate(ryft_events_published_total{result="error"}[5m]))` |

**Row: Journal**

| Panel | Type | Query |
| --- | --- | --- |
| Journal days saved (24 h) | stat | `sum(increase(ryft_journal_saves_total[24h]))` |
| Impact computation p95 | time series | `histogram_quantile(0.95, sum by (caller, le) (rate(ryft_behavior_impact_seconds_bucket[1h])))` |

**Row: Push**

| Panel | Type | Query |
| --- | --- | --- |
| Notifications by result | stacked bars, 1 h buckets | `sum by (category, result) (increase(ryft_push_notifications_total[1h]))` |
| APNs answers | time series | `sum by (push_type, status) (rate(ryft_apns_responses_total[5m]))` |
| Tokens pruned (7 d) | stat | `sum(increase(ryft_push_tokens_pruned_total[7d]))` |
| Flags served on / off | time series | `sum by (key, result) (rate(ryft_flag_evaluations_total[1h]))` |

**Row: API**

| Panel | Type | Query |
| --- | --- | --- |
| Requests by route | time series | `sum by (route) (rate(ryft_http_requests_total{app="api",route=~".*/v1/.*"}[5m]))` |
| Latency p95 by route | time series | `histogram_quantile(0.95, sum by (route, le) (rate(ryft_http_request_duration_seconds_bucket{app="api"}[5m])))` |
| 4xx / 5xx by route | time series | `sum by (route, code) (rate(ryft_http_requests_total{app="api",code=~"[45].."}[5m]))` |

**Row: Train**

| Panel | Type | Query |
| --- | --- | --- |
| Sessions completed (24 h) | stat | `sum(increase(ryft_train_sessions_completed_total[24h]))` |
| Sets logged | time series, 1 h buckets | `sum(increase(ryft_train_sets_logged_total[1h]))` |
| Records by kind | stacked bars, 1 d buckets | `sum by (kind) (increase(ryft_train_prs_total[1d]))` |
| Session saves: 409 / 422 / 429 | time series | `sum by (code) (rate(ryft_http_requests_total{route="PUT /v1/sessions/{id}",code=~"409\|422\|429"}[5m]))` |

**Row: Runtime** (repeat by `app`)

| Panel | Type | Query |
| --- | --- | --- |
| Memory (RSS) | time series | `process_resident_memory_bytes{namespace=~"$namespace"}` |
| Goroutines | time series | `go_goroutines{namespace=~"$namespace"}` |
| CPU | time series | `rate(process_cpu_seconds_total{namespace=~"$namespace"}[5m])` |

**Row: Events and the database**

| Panel | Type | Query |
| --- | --- | --- |
| Sync to score p50 / p95 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_sync_to_score_seconds_bucket[15m])))` (repeat for 0.5) |
| Dead letters | stacked bars, 1 h buckets | `sum by (subject, result) (increase(ryft_events_dlq_total[1h]))` |
| Queue depths (RabbitMQ) | time series | `max by (queue) (rabbitmq_queue_messages_ready{vhost="ryft"})`, `max by (queue) (rabbitmq_queue_messages_unacked{vhost="ryft"})` |
| Dead-letter queues | stat | `max by (queue) (rabbitmq_queue_messages{vhost="ryft", queue=~"dlq\\..*"})` (above 0: `scripts/dlq.sh list`) |
| Broker nodes and alarms | state timeline | `count(rabbitmq_identity_info{rabbitmq_cluster="rabbitmq"})`, `max by (instance) (rabbitmq_alarms_memory_used_watermark + rabbitmq_alarms_free_disk_space_watermark)` |
| Broker memory per node | time series | `max by (instance) (rabbitmq_process_resident_memory_bytes)` (the alarm at about 520 MB) |
| Connected to RabbitMQ | state timeline | `min by (namespace) (ryft_amqp_connected)`, and `sum by (namespace) (increase(ryft_amqp_reconnects_total[1h]))` |
| Outbox | time series | `max by (publisher) (ryft_outbox_pending)`, `max by (publisher) (ryft_outbox_oldest_seconds)`, `max by (publisher) (ryft_outbox_stuck)` |
| Publishes by result | time series | `sum by (reason, result) (rate(ryft_events_published_total[5m]))` |
| Database up | state timeline | `min by (service) (ryft_db_up)` |

**Row: Beta**

| Panel | Type | Query |
| --- | --- | --- |
| Testers by last sync | bar gauge | `max by (age) (ryft_users_by_last_sync)` (`under_6h`, `6h_to_24h`, `1d_to_3d`, `over_3d`, `never`; who: `scripts/beta_report.sh`) |
| Refused or flagged: invite / consent | time series | `sum by (code, mode) (increase(ryft_gate_refusals_total[1h]))` |
| Consents | stacked bars, 1 d buckets | `sum by (action) (increase(ryft_consents_total[1d]))` |
| Feedback (7 d) | stat | `sum by (category) (increase(ryft_feedback_received_total[7d]))` |
| Deploys | table | `max by (name, sync_status, health_status) (argocd_app_info)`, and `sum by (name, phase) (increase(argocd_app_sync_total[24h]))` |

RabbitMQ's nodes carry the same scrape annotations (port 15692, plain HTTP; the
broker's network policy admits it from the VPC range like the services' 9090). Its
management UI shows the same per queue (OPERATIONS → RabbitMQ). Until the cutover the
NATS consumers reported their backlog themselves (`ryft_nats_consumer_pending`,
`ryft_nats_stream_messages`); those series end with R4. For the
in-cluster Postgres of `local-postgres`, `postgres_exporter` with a read-only role.

## Alerts

The rules are code: `deploy/grafana/alerts/ryft.yaml` (Prometheus format, D85), with
unit tests in `ryft.test.yaml`. `make alerts-check` runs `promtool check rules`, the tests
and `mimirtool rules check` (in containers); `make alerts-apply` loads them into the
stack's ruler with `GRAFANA_PROM_URL`, `GRAFANA_PROM_USER` and `GRAFANA_RULES_TOKEN` (an
access policy token with `rules:read` and `rules:write`). Routing is the operator's, in
Grafana Cloud → Alerting: a contact point with email and the Grafana mobile app, and a
notification policy for `severity` `critical`, `warning` and `info` (feedback).

| Alert | Severity | Fires when | First look |
| --- | --- | --- | --- |
| HTTP5xxRatio | critical | api or ingest answers > 2 % of requests with 5xx, 10 min | logs of that namespace, `err` on `level=ERROR` |
| PodRestarting | critical | a `ryft-*` pod restarted > 3× in 30 min | `kubectl -n <ns> describe pod`, the previous container's log |
| Panics | critical | a handler panicked | the `msg="panic"` line with the stack |
| AuthUnavailable | critical | JWKS unavailable or the user lookup fails, 5 min | Supabase status, `ryft_db_up` |
| DatabaseDown | critical | a service's database ping failed for 2 min | Supabase status and pooler, `ECIRCUITBREAKER` after failed logins (OPERATIONS → EKS) |
| DatabaseFlapping | warning | > 3 failed pings in 15 min | the same, intermittently |
| CertificateExpiring | warning | a cert-manager certificate expires in < 14 days, 1 h | `kubectl get certificate -A`, the issuer's events |
| SyncSlow | warning | sync p95 > 10 s, 15 min | database latency, sync sizes |
| RateLimited | warning | > 1 request/s answered 429, 10 min | which class (`ryft_rate_limited_total{class}`), the logs |
| SyncToScoreSlow | warning | p95 from sync to score over 2 min, 15 min | scoring's CPU and database time, the backlog, a large backfill |
| DeadLetters | warning | an event was dead-lettered | `scripts/dlq.sh list` (OPERATIONS → RabbitMQ) |
| DeadLetterPublishFailing | critical | not even the dead-letter publish worked | the broker, the consumer's rights |
| PublishFailing | warning | the relay's sends fail, 10 min | the broker; the outbox keeps the events (OutboxBacklog follows) |
| RabbitMQBacklog | warning | > 100 events ready in `scoring` or `notify`, 15 min | is the consumer connected (`ryft_amqp_connected`), its logs |
| RabbitMQDeadLetters | warning | a dead-letter queue holds a letter, 5 min | `scripts/dlq.sh list` (OPERATIONS → RabbitMQ) |
| RabbitMQAlarm | critical | a node's memory or disk alarm, 2 min (publishers are blocked) | queue depths (a consumer that stopped), `rabbitmq-diagnostics -q alarms` |
| RabbitMQNodeDown | warning | fewer than three nodes report, 5 min | `kubectl -n ryft-rabbitmq get pods`, a node drain, the PDB |
| AMQPDisconnected | warning | a service's connection to RabbitMQ is down, 5 min | its log (`amqp` lines: TLS, password, the broker); scoring's and notify's `/readyz` |
| EventsUnroutable | critical | an event was published with no queue bound for its key | `amqpbus/topology.go` and the generated topology applied (Argo CD `broker-rabbitmq`) |
| OutboxBacklog | warning | an outbox event waits > 5 min for the broker, 5 min | the broker, the relay's `outbox relay` log lines |
| OutboxStuck | warning | an outbox row failed 50 sends | its `last_error` (OPERATIONS → RabbitMQ → The outbox) |
| ScoringErrors | warning | days fail to score, 15 min | scoring's logs |
| NoSync | warning | no sync from anyone for 6 h (or ingest restarted since) | the app's background refresh, ingest; per tester: the panel below |
| APNsFailing | warning | > 10 % of APNs requests fail over 30 min (≥ 5; 410 does not count) | `status`: 400 = a token sent to the wrong environment, 403 = the key |
| PushFailing | warning | a notification reached no device | the same |
| APNsRejectsOurKey | critical | APNs answered 403 | `APNS_KEY_ID`, `APNS_TEAM_ID`, `APNS_KEY_P8` |
| AccountDeletionIncomplete | warning | a deleted account kept its Supabase sign-in or Apple grant | the reconciler's log, the keys |
| NewFeedback | info | a tester sent feedback | `scripts/beta_report.sh feedback` |
| ArgoSyncFailed | critical | a sync ended Failed or Error | `argocd app get <name>` (OPERATIONS → Deploy → A failed sync): which hook or object |
| ArgoAppDegraded | warning | an application is Degraded for 10 min | the Degraded object; for ryft-platform also the smoke Job's log in `ryft-smoke` |
| ArgoAppOutOfSync | warning | an auto-synced application differs from git for 30 min | a wave waiting on an unhealthy one, a render error (`Unknown`), a sync retrying |
| ArgoMetricsMissing | warning | no `argocd_app_info` for 15 min | `kubectl -n argocd get pods`, the metrics policy |

`NoSync` needs a scrape since the last ingest restart (the gauge is in memory), hence the
`absent()`. Per-tester staleness is a dashboard panel, not a page: one tester on holiday
is no incident.

## Logs

One JSON object per line on stdout, written by `internal/logx`. `LOG_LEVEL`
(`debug`, `info` — default —, `warn`, `error`) sets the level; it is set in `deploy/k8s/base/<service>/config.env`.

| Field | On | Meaning |
| --- | --- | --- |
| `time`, `level`, `msg`, `service` | every line | `service` is `ingest`, `scoring` or `api`. |
| `request_id` | every line logged while serving a request; scoring lines for an event | The ID the server gave the request (UUIDv7, also the `X-Request-ID` response header and `request_id` in error bodies). Scoring logs the ID of the sync or recompute request that caused the event, so one ID follows a sync from ingest through scoring. |
| `client_request_id` | same | A client-sent `X-Request-ID` (if it looks like an ID). Logged only, never used as ours. |
| `user_id` | once the token is verified; scoring per event and per user in the 15-minute pass | The Supabase user ID (`sub`). |
| `route` | request lines once routed | ServeMux pattern, e.g. `GET /v1/days/{day}`. |
| `method`, `path`, `status`, `bytes`, `dur_ms` | the access line (`msg="http"`, one per request) | `status` `499`: the client gave up before the answer (logged at info, not an error). |
| `code` | the access line of a refused request | The error code (`invite_required`, `consent_required`) or, for a `401`/auth `503`, the reason (`expired`, `signature`, `deleted`, …). |
| `gate` | the access line of a request a gate in `warn` mode let through | The code(s) it would have been refused with, as in `X-RYFT-Gate`. |
| `err` | errors and warnings | The internal error. Clients only ever see `"internal error"` and the request ID. |

Never logged: tokens, the `Authorization` header, email addresses, request bodies.
Scoring's `scored day` lines do carry a user's daily scores (readiness, HRV, resting HR)
next to `user_id`, so treat the logs as health data: limit who can read them and keep
them no longer than you need.

**Finding a request** a user reports (the app shows or logs `request_id`):

```sh
for s in ingest api scoring notify; do
  kubectl -n ryft-$s logs -l app=$s --tail=-1 --prefix --since=48h --max-log-requests=10; done \
  | grep 01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c
```

`--tail=-1` matters: with `-l`, `kubectl logs` shows only the last 10 lines of each pod
by default, so a grep finds nothing. `logs deploy/<name>` reads one pod of several.

Pod logs only reach back to the last restart. Everything older is in CloudWatch.

### In CloudWatch

Fluent Bit (`infra/aws/kubernetes`, `logs.tf`) ships the container logs of the `ryft-*`
namespaces and of Traefik to the log group `/ryft/ryft-prod-euc1/containers`. The group
is KMS-encrypted and keeps 14 days; there is one stream per container. Each JSON line is
parsed into fields under `data` (`data.request_id`, `data.user_id`, `data.msg`, …).
CloudWatch Logs Insights:

```text
fields @timestamp, data.service, data.msg, data.status, data.err
| filter data.request_id = "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"
| sort @timestamp asc
```

**Who can read them:** anyone whose IAM identity may call `logs:GetLogEvents` or
`logs:StartQuery` on the group *and* `kms:Decrypt` on the key `alias/ryft-prod-euc1-logs`.
Today that is the account's administrators only. Treat any grant as access to health
data: no broad `CloudWatchLogsReadOnlyAccess` for others, and no export to buckets or
third parties. Cost at alpha volume is around €1 a month, plus €1 for the key.
