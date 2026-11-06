# RYFT — observability

Each service (ingest, scoring, api) serves Prometheus metrics on `GET /metrics` on its
own port, **9090** (`METRICS_PORT`, no auth), apart from the API and probe port 8080.
The Ingress only routes `/v1/*`, so `/metrics` is never public. Logs are JSON (`slog`)
on stdout and shipped to CloudWatch; see **Logs** below.

## Collection: Grafana Cloud

Metrics go to Grafana Cloud (free tier; D51). The collector is Grafana's
`k8s-monitoring` chart (`deploy/monitoring/k8s-monitoring.yaml`, `make
monitoring-apply KUBE_CONTEXT=<context>`) in namespace `monitoring`: two Alloy collectors named as the
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

**RYFT's pod logs stay out of Grafana Cloud.** They tie user ids to the days and records
they asked for (no health values: OPERATIONS → Data protection (logs)) and go to
CloudWatch instead (D52). The collector can read every container's log file
(`filesystem-log-reader`), so this is a Fleet Management setting, not a boundary:
exclude namespaces matching `ryft-.*` from pod-log collection. Check in Explore that
`{namespace=~"ryft-.*"}` returns nothing.

Two scrapes are the chart's own, not Fleet's (T12-e, D120; **Admission** below): the API
servers' admission metrics (job `ryft/kube-apiserver-admission`) and kube-state-metrics'
series for the admission bindings (job `ryft/admission-bindings`). They write through a
destination of their own with the `PROM_USERNAME` and `GC_TOKEN` of `ryft-secrets`, the
token the Fleet pipelines write with.

Alert rules: `deploy/grafana/alerts/ryft.yaml`, checked and loaded with `make
alerts-check` / `make alerts-apply`; see **Alerts** below.

**The SDK injector stays out of `ryft-*`.** `telemetryServices.sdkInjector` deploys a
mutating webhook (`mpod-v1.beyla.grafana.com`, pods on CREATE, `failurePolicy: Ignore`)
that can add an SDK init container or volume to pods of every namespace it does not
exclude. The admission policies pin the images and commands of RYFT's pods, so an injected
container would be refused, and the services emit their own metrics. The chart excludes
by exact namespace name, not by prefix, so `deploy/monitoring/k8s-monitoring.yaml` lists
every `ryft-*` namespace next to the chart's own defaults (a list value replaces the
default list). A new `ryft-*` namespace is added there too. Scraping and the Fleet
Management log setting above are unaffected. Apply with `make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1`, then
check read-only:

```bash
kubectl get mutatingwebhookconfigurations \
  -o custom-columns='NAME:.metadata.name,NAMESPACES:.webhooks[*].namespaceSelector,OBJECTS:.webhooks[*].objectSelector'
```

The selector of the injector's webhook must list every `ryft-*` namespace under `NotIn`.

A self-hosted Prometheus works the same way: a job that keeps pods with
`prometheus.io/scrape=true` and uses the annotated port, running in namespace
`monitoring`.

### Synthetic Monitoring: the site from outside

Nothing inside the cluster sees what a visitor, the App Review team or Apple's AASA
fetch sees, so Grafana Cloud's Synthetic Monitoring (free tier) checks `ryftapp.de` from
two public EU probes every 5 minutes. The definitions are code,
`deploy/grafana/synthetics/ryftapp.json`; `make synthetics-check` validates them
offline, `make synthetics-apply` upserts them by `job` through the SM API
(`SM_API_URL`, `SM_ACCESS_TOKEN`; OPERATIONS → Monitoring). The results are ordinary
metrics in the stack's Prometheus, so the same ruler evaluates the alerts.

| `job` | `instance` (the URL) | Passes when |
| --- | --- | --- |
| `ryft-site-home` | `https://ryftapp.de/` | 200, `text/html` |
| `ryft-site-privacy` | `https://ryftapp.de/privacy` | 200, `text/html`, the body holds `Art. 9(2)(a) GDPR` (only the real policy has it) |
| `ryft-site-aasa` | `https://ryftapp.de/.well-known/apple-app-site-association` | 200 with no redirect, `application/json`, the body holds `M77SKHKG63.dev.timkrebs.ryft` |
| `ryft-site-www-redirect` | `https://www.ryftapp.de/` | 301 (or 308) whose `Location` is `https://ryftapp.de/`, not followed |

Every check also fails when TLS fails. Metrics (labels `job`, `instance`, `probe`):
`probe_success` (1 or 0 per run), `probe_ssl_earliest_cert_expiry` (Unix seconds; the
served certificate, so `www` is covered as well), `probe_duration_seconds`,
`probe_http_status_code`. The alerts are `SiteDown`, `SiteProbeFlapping`,
`SiteWwwRedirectBroken`, `SiteCertificateExpiring` and `SyntheticsMissing` (Alerts below).
The SM agent republishes a run's last result about every 2 minutes, so a 5-minute check
yields roughly three samples per run; the windows (10 minutes for "every probe", 30
minutes and 0.8 for "flapping") count whole runs either way.

**Budget.** The free tier includes 100k API test executions a month (grafana.com/pricing →
Synthetics API Testing); an execution is one test run in one probe location per minute
of run time. 4 checks × 2 probes × 8,640 runs (every 5 min, 30 days) = 69,120, about 69 %,
assuming a run under a minute (they take under a second); `make synthetics-check`
prints the figure and refuses a definition above the limit. A third probe would be
103,680: over. The privacy marker is a sentence of the policy text; if a policy
revision drops it, `SiteDown` fires after the deploy, and the fix is the regexp in the
JSON (`make synthetics-apply`), not the page.

## Metrics

| Metric | Type | Labels | Service | Meaning |
| --- | --- | --- | --- | --- |
| `ryft_http_requests_total` | counter | `route`, `code` | ingest, api | Requests by ServeMux pattern (`GET /v1/days/{day}`, never the raw path) and status. Unauthenticated requests carry the outer pattern `/v1/`; unknown routes `unmatched`. |
| `ryft_http_request_duration_seconds` | histogram | `route` | ingest, api | Request latency. |
| `ryft_samples_ingested_total` | counter | `kind`, `type` | ingest | Records accepted by `POST /v1/sync`: `kind="sample"` with the sample type, `kind="sleep"` with the stage, `kind="workout"`. |
| `ryft_last_sync_timestamp_seconds` | gauge | — | ingest | Unix time of the last successful sync of any user. Resets on restart. (Had a `device` label before multi-user; per-user or per-device series would be unbounded.) |
| `ryft_rate_limited_total` | counter | `class` | ingest, api | Requests answered `429` by the per-user rate limit: `class="sync"` (`POST /v1/sync`), `session` (`PUT /v1/sessions/{id}`), `invite` (`POST /v1/me/invite`), `import` (`POST /v1/train/import`: bursts of 5, then one every 5 s; the app's batches one after another stay under it) or `default`; or `admin_ip` (`/v1/admin/*`: 10 a second per client address, bursts of 20, per pod; the address is never logged or labelled). The recompute gate (one per minute) is not counted here. |
| `ryft_http_panics_total` | counter | `route` | all | Handler panics turned into a `500` by the recover middleware. Each also logs `msg="panic"` with the stack. |
| `ryft_goroutine_panics_total` | counter | none | all (once a service starts through `internal/app`) | Panics recovered in a service's own goroutines (consumers, timers, the outbox relay, the servers). The service shuts down in order and exits 2, as an unrecovered panic does; the `msg="panic"` line names the `task` and holds the stack. A panic in a `/readyz` check is counted too (the line names the `check`, logged at most once a minute) and the service keeps running. Any is a bug. In the `Panics` alert; the process exits 2 right after it, so a scrape may never see the increase and `ServiceExited` is the other trace. |
| `ryft_http_text_refused_total` | counter | `route`, `at` | ingest, api | Requests refused for text the database cannot store (a NUL, or bytes that are not UTF-8; API.md → Errors → NUL): `at="body"` (`\u0000` in a JSON string, `422`), `url` (path or query, `400`), `database` (Postgres refused it, SQLSTATE 22021/22P05, `422`). No log line beyond the access line, and never the text. `database` above 0 means a path the checks and the raw-JSON normalisation missed, or a server-side bug producing bad text; the route says where (alert TextRefusedByDatabase). |
| `ryft_http_nul_stripped_total` | counter | `route` | ingest | Syncs accepted after the NULs were removed from their strings (`POST /v1/sync` does not refuse them: the app would never resend the page). One per request, however many NULs; never the text. A steady rate points at a source or device name the app should clean. |
| `ryft_auth_failures_total` | counter | `reason` | ingest, api | Rejected `/v1` requests. 401 reasons: `missing_token`, `malformed`, `expired`, `signature`, `issuer`, `audience`, `unknown_key`, `key_mismatch`, `role`, `anonymous`, `subject`, `api_key`, `deleted` (the account was deleted), `invalid`. 503 reasons: `jwks_unavailable`, `jwks_refresh_limited`, `internal` (user lookup failed, or on `/v1/admin/*` the allow-list check). |
| `ryft_account_deletions_total` | counter | `step`, `result` | api | `DELETE /v1/me` and the reconciler. `step="data"` (our rows), `apple` (revoking the Sign in with Apple token) or `auth` (the Supabase auth user); `result` `ok`, `error` or `skipped` (not configured: no `APPLE_*` / `SUPABASE_SERVICE_ROLE_KEY`, or apikey mode). |
| `ryft_sync_deleted_total` | counter | `kind` | ingest | Rows removed by the deletions of `POST /v1/sync`, by `kind` (`sample`, `sleep`, `workout`). The same-source workout fold is not counted here. |
| `ryft_sync_ignored_total` | counter | `kind`, `reason` | ingest | Records and deletions of `POST /v1/sync` not applied: `kind` `sample`, `sleep`, `workout`; `reason` `duplicate`, `outside_window`, `unknown_type`, `unknown_id`. |
| `ryft_workouts_folded_total` | counter | `reason` | ingest, scoring | Duplicate workouts handled: `same_source` (a copy removed by a sync's fold, ingest) or `cross_source` (a workout newly marked `duplicate_of` another source's, scoring). `fold-workouts` runs as a one-off process without `/metrics`, so its removals are not counted here. |
| `ryft_workouts_fold_skipped_total` | counter | `reason` | ingest | Syncs whose same-source fold did nothing: `too_many` (more than 5 000 workouts of the request's sources in its range). Log line `workout fold skipped`. |
| `ryft_events_published_total` | counter | `reason`, `result` | ingest, api, scoring | Sends to the broker (by the outbox relay, D101), `ok`/`error`, on RabbitMQ also `unroutable` (no queue bound for the key: a topology change missed): `ingested` with `sync` (ingest), `recompute` and `session` (an edited or deleted completed session; api), `session.completed` as `session_completed` (api), and scoring's `scored`, `session_scored`, `refresh`. An `error` here is retried by the relay; an event is lost only if its outbox row could not be written (also `error`, and the sync or completion answers `503`). |
| `ryft_events_handled_total` | counter | `reason`, `result` | scoring, notify | Consumed events: `ok`, `retry` (to the next retry tier: 5 s, 30 s, 5 min), `failed` (the sixth attempt failed — dead-lettered), `bad` (unparseable — dead-lettered), `dropped` (cannot succeed, e.g. the user was deleted), `deferred` (notify: held until quiet hours end). notify's reasons are `scored`, `session_scored` and `refresh`. |
| `ryft_nats_redeliveries_total` | counter | `consumer` | scoring, notify | Messages received again, `consumer` the queue: redelivered by the broker (a consumer died holding it), a retry tier's copy (`x-ryft-attempt` 1 or more), or a dead letter replayed with `scripts/dlq.sh` (`x-ryft-replayed`: a replay of 50 letters adds one per letter as the consumer handles them, one after another, not 50 at once). Each delivery counts once. The name is from NATS, kept for the dashboards and the history (D102). |
| `ryft_days_scored_total` | counter | `result` | scoring | Days scored (`ok`/`error`; `no_consent`: skipped, consent withdrawn, D78), from events and the 15-minute timer. |
| `ryft_scoring_duration_seconds` | histogram | `result` | scoring | Time to score one day: load, compute, write (including its strength sessions). |
| `ryft_days_rescored_total` | counter | `reason` | scoring | Stored days scored again beyond the days an event or the timer asked for, because their history changed (they are in `ryft_days_scored_total` too). `history`: scoring a day wrote a new row or changed one of the inputs the next 30 scored days are computed from (HRV, resting HR, respiratory rate, strain, sleep need and actual, sleep times, naps, whether steps and active energy are there), or a retried event re-scored its days, so those days were scored again in the same pass, in order, never past today: a backfill that arrived newest first, a late sample, a recompute, a retry. At most 400 per event; hitting that logs `rescore after history change capped`. `behind`: the 15-minute pass found days (within the last 400) whose `baseline_days` (or its HRV or resting-HR count) is below what their stored history now holds, or whose HRV or resting-HR baseline is missing although the history has such nights, and scored them again (about 50 days a pass, the days their re-score reaches included, which count as `history`): what the in-pass re-score could not see (two consumers at once, the cap, rows from before it existed). It compares counts and missing baselines, not values. After a deploy `behind` works off old rows, then stays near 0; a steady rate means the re-score misses something. |
| `ryft_sleep_base_learned_total` | counter | `outcome` | scoring | Weekly sleep-baseline estimates (Sundays from 04:00 local, and `scoring learn-baselines`): `applied` (the baseline in use moved, the last 7 days were rescored), `kept` (same value, or set by hand), `insufficient` (fewer than 14 nights or 7 qualifying), `error`. A run of `error`s means the users' days or the baseline write failed; `applied` should be rare after the first weeks. |
| `ryft_journal_saves_total` | counter | — | api | Journal days saved (`PUT /v1/journal/{day}`). |
| `ryft_behavior_impact_seconds` | histogram | `caller` | api | CPU time of the behavior-impact computation: `caller="impact"` (`GET /v1/behaviors/impact`) or `insight` (yesterday's behaviors for the card). Its p95 passing ~0.1 s is the signal to move it into a worker (D76). |
| `ryft_flag_evaluations_total` | counter | `key`, `result` | api | Feature flags evaluated for `GET /v1/config`, `on` or `off` per flag key. |
| `ryft_admin_requests_total` | counter | `result` | api | `/v1/admin` requests: `ok`, `invalid`, `error` (a failed handler, and the `503` of a token that cannot be checked now: the api could not complete a key fetch or the allow-list check, which is not by itself an outage, since anyone can send an unknown key id inside the refresh limit; the log and the access line's `code` say `jwks_unavailable`, `jwks_refresh_limited` or `account_check`, and the last counts as `internal` in `ryft_auth_failures_total`), `unauthorized` (no bearer, or one that is neither the key nor an allow-listed operator's token: one `401` for every reason, the reason is only in the log), `mfa_required` (a real operator without a second factor of the last 12 hours: `403`), `forbidden` (an operator's token on a key-only route: `403`). The series exist at 0 from start-up. |
| `ryft_admin_authorized_total` | counter | `actor` | api | `/v1/admin` requests that passed the bearer check, by `actor`: `key` (scripts, smoke) or `user` (the operator's page with a Supabase token). No label holds an id: who acted is in `GET /v1/admin/audit` (`actor`), and `admin_actor` (`key:3fa1c2d9`, `user:1b2c3d4e`, the first 8 hex digits) is on the admin log lines. |
| `ryft_gate_refusals_total` | counter | `code`, `mode`, `service` | api, ingest, scoring | Requests a gate would refuse: `invite_required` (the beta gate, D79) or `consent_required` (a sync or, `service="api"`, a training import, or in scoring a day, without consent, D78, D129). `mode="on"`: refused with `403`; `mode="warn"`: served with `X-RYFT-Gate` (D87). After a policy bump, `consent_required` rises until the testers open the app. The training import's consent refusals count as `mode="on"` also under `CONSENT_ENFORCE=warn`: on that route warn acts as on (D141). (Was `ryft_beta_denied_total` before `61db1c4`, 2026-10-20.) |
| `ryft_consents_total` | counter | `action` | api | `granted`, `revoked`, `stale` (a POST for an outdated policy version: the app shows an old text). |
| `ryft_invite_redemptions_total` | counter | `result` | api | `POST /v1/me/invite`: `ok` (a use spent), `already` (the account had access), `invalid` (every refusal, a revoked code and a bound code for another email included: one label on purpose, D159). Many `invalid` from one place is someone guessing (5 an hour per user). The `503` for a bound code when Supabase cannot be asked is not counted here: it is the warn line `invite email lookup failed`. |
| `ryft_invite_binding_available` | gauge | none | api | 1 while email-bound invites work: the Supabase project confirms emails (`mailer_autoconfirm` false in its public auth settings) and the api holds the Supabase admin key. 0 while *Confirm email* is off (an address then counts as confirmed without a mailbox), the setting cannot be read, or there is no admin key: bound codes are neither created nor redeemed (D159). The log line `invite binding` says which state on every change |
| `ryft_invite_settings_checks_total` | counter | `result` | api | Reads of that setting, at start and hourly: `ok`, `autoconfirm` (Confirm email is off: turn it on, OPERATIONS → Beta), `error` (unreadable, treated like off) |
| `ryft_invite_email_purge_total` | counter | `result` | api | Passes of the job that clears a bound invite email and its note 30 days after the code stopped being redeemable (D161): `ok` (also a pass that cleared nothing), `error`. Created at 0 at start. `InviteEmailPurgeFailing` watches `error` |
| `ryft_events_dlq_total` | counter | `subject`, `result` | scoring, notify | Events dead-lettered (unparseable, or failed on the last attempt), into `dlq.scoring`/`dlq.notify` (D99), by `subject`: the routing key for a delivery from `ryft.events` (what the publisher's topic permission limits; the type a publisher sets never becomes a label), the type only for a retry tier's copy (it comes back with the queue's name as routing key, so the type its first delivery was checked with is the key) and only when the queue's consumer is bound for it, else the routing key; so the label values are the topology's keys and queue names; then `result`: `ok`, or `error` (the publish failed; the message was rejected, and the broker's delivery limit dead-letters it). Before the cutover, `subject` was the NATS subject (`ryft.ingested`). |
| `ryft_sync_to_score_seconds` | histogram | — | scoring | From ingest accepting a sync to scoring finishing one of the days it queued (`score_queue`, D91; an import's days too: they share the queue, so a large import shows as a short rise, and `expected_within_sec`, the queue's p95 of the last 6 hours, rises with it, D141). The p95 is what testers wait for their scores; `SyncResponse.scoring.expected_within_sec` tells the app the same from the table. |
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
| `ryft_push_notifications_total` | counter | `category`, `result` | notify | One per notification decided: `category` `MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE` or `REFRESH` (the silent push); `result` `sent`, `failed` (no device reached), `muted` (category off), `quiet` (dropped in quiet hours), `deferred` (morning push held until they end), `duplicate` (already sent that day), `throttled` (a silent refresh within 5 minutes of the last), `stale` (old day or session, deleted user), `import` (`SESSION_SCORED` only: an imported session, never announced, D125; scoring sends none for an import, so this counts only a re-complete of an imported session), `no_token` (no registered device), `unconfigured` (no APNs key: counted, not sent). An import's days end in one `REFRESH` once scored, like a sync's. |
| `ryft_apns_responses_total` | counter | `push_type`, `status` | notify | APNs answers per device request: `alert` or `background`, HTTP status (`200`, `400`, `403`, `410`, `429`, `5xx`) or `error` (no answer). |
| `ryft_push_tokens_pruned_total` | counter | — | notify | Device tokens dropped because APNs said they are gone. |
| `ryft_train_sessions_completed_total` | counter | — | api | Strength sessions completed (the first `POST /v1/sessions/{id}/complete` of each). |
| `ryft_train_sets_logged_total` | counter | — | api | Completed sets `PUT /v1/sessions/{id}` stored for the first time. |
| `ryft_train_prs_total` | counter | `kind` | api | Personal records a session took over (`e1rm`, `max_weight`, `max_reps`, `max_volume`). |
| `ryft_train_imports_total` | counter | `result` | api | `POST /v1/train/import` requests past authentication (D124): `ok` (`200`, also when every item was rejected), `invalid` (`400`/`413`/`422` or `403 consent_required`: nothing processed), `disabled` (`403 import_not_enabled`: the flag `train.import` is off for the account; nothing read), `busy` (`429 import_in_progress`: another import of the account holds the lease, D132), `timeout` (`503 "request timed out"`: the items ran into the 50 s budget, or the client went away), `error` (`503`: the database failed). After `timeout` and `error` the stored items stay stored and are finished (queue and event, then records); the app resends and gets them back `unchanged`. A steady `busy` for one account means a lease left by a pod that died (it expires after 70 s). |
| `ryft_train_import_items_total` | counter | `kind`, `status` | api | Items of the import: `kind` `plan` or `session`, `status` `created`, `unchanged` (identical, nothing written), `updated` (replaced whole), `rejected` (the `code` is in the response, not here). Mostly `unchanged` means the app resends files it already sent. |
| `ryft_train_import_seconds` | histogram | — | api | Duration of an import that took the lease: the items, the records and the queueing. The budget is 50 s (the items stop 10 s before it so the finish fits); p95 above ~20 s means the per-item reads (D131's note) need batching. |
| `argocd_app_info` | gauge | `name`, `sync_status`, `health_status`, `autosync_enabled`, … | Argo CD (controller, :8082) | One series per application, value 1; the alerts below read `sync_status` (`Synced`, `OutOfSync`, `Unknown` when manifests do not render) and `health_status` (`Healthy`, `Progressing`, `Degraded`, `Missing`). |
| `argocd_app_sync_total` | counter | `name`, `phase` | Argo CD | Sync operations by outcome: `Succeeded`, `Failed`, `Error` (a PreSync migration, a refused apply, the PostSync smoke), `Running`, `Terminating`. |
| `apiserver_validating_admission_policy_check_total` | counter | `policy`, `policy_binding`, `enforcement_action`, `error_type` | EKS API servers (one scrape per endpoint, job `ryft/kube-apiserver-admission`) | Policy checks that found something, by binding: a violation counts once per action of the binding (`warn` and `audit` in Warn+Audit, `deny` and `audit` in Deny+Audit); `error_type` is `no_error` unless the expression could not be evaluated (`compile_error`, `invalid_error`, `out_of_budget`). **Clean passes are not counted** (design claim e was wrong): a policy that is never violated has no series, so the series is absent, not zero, and never a liveness signal. A child is created at 1 by its first event, which `increase()` cannot see: the rules add series that are new (see Admission). Only `policy=~"ryft-.*"` is used by the alerts. |
| `apiserver_validating_admission_policy_check_duration_seconds_sum`, `_count` | counter | as above | EKS API servers | Check latency as a mean only: the buckets are dropped to save series. Nothing alerts on it (kept for the dashboard; it can go if the budget tightens). |
| `pod_security_evaluations_total` | counter | `decision`, `mode`, `resource`, `policy_level`, `request_operation`, … | EKS API servers | Pod Security Admission decisions. Only `decision="deny"` is kept (the relabel drops `allow` and `exempt`). `mode` is `enforce` (refused), `warn` or `audit`. Enforce is live today, so `AdmissionPodDenied` watches it without any binding. |
| `kube_validatingadmissionpolicybinding_info` | gauge | `validatingadmissionpolicybinding`, `policy_name` (+ `param_*`, empty) | kube-state-metrics, native collector `validatingadmissionpolicybindings` (job `ryft/admission-bindings`) | One series per binding, value 1. 12 `ryft-*` bindings once the chart is enabled, none before. kube-state-metrics v2.20 does not export a binding's labels, so the committed action is not here (see `ryft:admission_binding_intended`). Custom-resource-state cannot be used: it resolves kinds from CRDs only and skips built-in ones (design claim m was wrong). |
| `kube_validatingadmissionpolicybinding_validation_action` | gauge | `validatingadmissionpolicybinding`, `action` | kube-state-metrics | One series, value 1, for each of the binding's live `validationActions` (`Deny`, `Warn`, `Audit`); an action it lacks has no series. |
| `ryft:admission_binding_intended` | recording rule | `validatingadmissionpolicybinding`, `intended_action` | Grafana ruler (`deploy/grafana/alerts/admission-intended.yaml`) | 1 per binding, `audit` or `deny`, generated from `actions:` in `infra/aws/kubernetes/admission-values.yaml` by `scripts/admission_intended.sh`. `make alerts-check` fails when the file and the values differ. It exists whether or not the chart is enabled. |
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
| Days re-scored after a history change | stacked bars, 1 h buckets | `sum by (reason) (increase(ryft_days_rescored_total[1h]))` |
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
| Imports by result | stacked bars, 1 h buckets | `sum by (result) (increase(ryft_train_imports_total[1h]))` |
| Imported items by status | stacked bars, 1 d buckets | `sum by (kind, status) (increase(ryft_train_import_items_total[1d]))` |
| Import duration p95 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_train_import_seconds_bucket[1h])))` |

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
| Dead-letter queues | stat | `max by (queue) (rabbitmq_queue_messages{vhost="ryft", queue=~"dlq\\..*"})` (above 0: `KUBE_CONTEXT=ryft-prod-euc1 scripts/dlq.sh list`) |
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
access policy token with `rules:read` and `rules:write`). The owner's stack takes at most
**20 rules per group** (alerts and recording rules alike); a larger group makes the ruler
refuse the whole load (`per-user rules per rule group limit (limit: 20 actual: 39) exceeded`
for group `ryft`, 2026-11-05) and nothing is written. So `ryft.yaml` splits its rules by
theme into `ryft` (the services, HTTP, auth, database, certificates), `ryft-pipeline`
(dead letters, RabbitMQ, outbox, scoring, sync) and `ryft-ops` (push, accounts and the beta,
Argo CD, the site's synthetics), next to `ryft-admission`; `admission-intended.yaml` is one
group, `ryft-admission-intended`. Keep a group at 18 or fewer and open a new one by theme
when it grows: `make alerts-check` runs `scripts/check_rule_groups.sh` first, which fails
above 20 (promtool and mimirtool do not know the limit). A load replaces each group it names
whole and leaves groups it does not name alone, so moving an alert between groups in the
file is safe, but a renamed or dropped group stays live until it is deleted in Grafana Cloud
(Alerting → Alert rules) or with `mimirtool rules delete`. Routing is the operator's, in
Grafana Cloud → Alerting: a contact point with email and the Grafana mobile app, and a
notification policy for `severity` `critical`, `warning` and `info` (feedback).

| Alert | Severity | Fires when | First look |
| --- | --- | --- | --- |
| HTTP5xxRatio | critical | api or ingest answers > 2 % of requests with 5xx, 10 min | logs of that namespace, `err` on `level=ERROR` |
| PodRestarting | critical | a `ryft-*` pod restarted > 3× in 30 min | `kubectl -n <ns> describe pod`, the previous container's log |
| ServiceExited | critical | a container of api, ingest, scoring or notify restarted in the last 15 min (`kube_pod_container_status_restarts_total`; any restart, whatever the exit code: a probe kill can exit 0) | OPERATIONS → A service exited: `describe pod` for the exit code and the events, the previous container's log (`kubectl --context ryft-prod-euc1 -n <ns> logs <pod> --previous`, or CloudWatch) |
| Panics | critical | a handler (`ryft_http_panics_total`), an event handler (`ryft_event_handler_panics_total`) or a service's own goroutine (`ryft_goroutine_panics_total`) panicked in the last 15 min, per namespace. Terms joined with `or`; the two labelled counters' first panic (a series born at 1) is caught by the TextRefusedByDatabase pattern | the `msg="panic"` line with the stack (`task` or `route` names where); OPERATIONS → A panic (Panics); after a goroutine panic the pod has restarted, so read the previous container's log |
| TextRefusedByDatabase | warning | Postgres refused a request's text (SQLSTATE 22021/22P05) in the last hour, per namespace and route (`ryft_http_text_refused_total{at="database"}`; a route's first refusal included) | The client got a `422` and nothing was logged (it used to be a logged `500`). Either a path the checks miss — a handler reading text without `httpx.ReadJSON`, a raw field `normalizeRaw` does not reach, a URL value outside `WithRoutes` — or a server-side bug writing bad text (a string cut inside a UTF-8 sequence). Reproduce on that route against a throwaway database (the `err` is not logged), then close the gap in `internal/httpx` with a test. |
| AuthUnavailable | critical | `jwks_unavailable` (no signing keys, or an unknown key id while the last fetch failed: no answer, another status than 200, a body that is not a key set, or no key set within the fetch's time; a client that hangs up mid-fetch does not count, D184) or `internal` (the user lookup, or the admin allow-list check, failed), 5 min. `jwks_refresh_limited` is left out: a token with an unknown key id inside the refresh limit, which anyone can send | Supabase status, `ryft_db_up` |
| DatabaseDown | critical | a service's database ping failed for 2 min | Supabase status and pooler, `ECIRCUITBREAKER` after failed logins (OPERATIONS → EKS) |
| DatabaseFlapping | warning | > 3 failed pings in 15 min | the same, intermittently |
| CertificateExpiring | warning | a cert-manager certificate expires in < 14 days, 1 h | `kubectl get certificate -A`, the issuer's events |
| SyncSlow | warning | sync p95 > 10 s, 15 min | database latency, sync sizes |
| RateLimited | warning | > 1 request/s answered 429, 10 min | which class (`ryft_rate_limited_total{class}`), the logs |
| SyncToScoreSlow | warning | p95 from sync to score over 2 min, 15 min | scoring's CPU and database time, the backlog, a large backfill |
| DeadLetters | warning | an event was dead-lettered | `KUBE_CONTEXT=ryft-prod-euc1 scripts/dlq.sh list` (OPERATIONS → RabbitMQ) |
| DeadLetterPublishFailing | critical | not even the dead-letter publish worked | the broker, the consumer's rights |
| PublishFailing | warning | the relay's sends fail, 10 min | the broker; the outbox keeps the events (OutboxBacklog follows) |
| RabbitMQBacklog | warning | > 100 events ready in `scoring` or `notify`, 15 min | is the consumer connected (`ryft_amqp_connected`), its logs |
| RabbitMQDeadLetters | warning | a dead-letter queue holds a letter, 5 min | `KUBE_CONTEXT=ryft-prod-euc1 scripts/dlq.sh list` (OPERATIONS → RabbitMQ) |
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
| AdminUnauthorized | warning | more than 20 `/v1/admin` requests with a bad bearer (`result="unauthorized"`) in 10 min. The api never sees a guessed password (that is Supabase Auth: Supabase → Logs → Auth). `mfa_required` is deliberately in neither admin alert | the api's `admin auth rejected` lines (`reason`: `missing`, `not_a_token`, `expired`, `signature`, `not_admin`, `not_allowed`, …) and the access lines' `code`; `ryft_rate_limited_total{class="admin_ip"}`; the CloudWatch queries in OPERATIONS → Beta → Admin sign-in |
| AdminWebUsed | info | an operator's token (the admin page) passed the bearer check in the last 10 min (`ryft_admin_authorized_total{actor="user"}`) | `GET /v1/admin/audit` (`actor.kind` `user`) shows who and what; not you: delete the `admin_accounts` row at once (OPERATIONS → Beta → Admin sign-in → Lost phone) |
| InviteEmailPurgeFailing | warning | the job that clears bound invite emails failed 3 times in 3 hours (`ryft_invite_email_purge_total{result="error"}`; each api pod runs it hourly) | the api's `invite email purge failed` line (the cause); the database; until it runs, addresses of people who only asked for an invite stay longer than the privacy text says |
| ArgoSyncFailed | critical | a sync ended Failed or Error | `argocd app get <name>` (OPERATIONS → Deploy → A failed sync): which hook or object |
| ArgoAppDegraded | warning | an application is Degraded for 10 min | the Degraded object; for ryft-platform also the smoke Job's log in `ryft-smoke` |
| ArgoAppOutOfSync | warning | an auto-synced application differs from git for 30 min | a wave waiting on an unhealthy one, a render error (`Unknown`), a sync retrying |
| ArgoMetricsMissing | warning | no `argocd_app_info` for 15 min | `kubectl -n argocd get pods`, the metrics policy |
| SiteDown | critical | the home page, `/privacy` or the AASA fails from every probe for 10 min (status, content type or body marker, not only "down") | OPERATIONS → Apex site → When a synthetic alert fires |
| SiteProbeFlapping | warning | one probe fails twice or more in 30 min while another gets through (left out for 30 min after SiteDown's condition held; failures that only overlap between probes do not suppress it) | the same: which probe, which URL, the edge's rate limit |
| SiteWwwRedirectBroken | warning | `www.ryftapp.de` does not answer a redirect to the apex from any probe, 10 min | the `www-to-apex` Middleware, the Ingress hosts |
| SiteCertificateExpiring | warning | the served certificate (apex or www) expires in < 14 days, 1 h | `kubectl -n ryft-web get certificate ryftapp-de-tls`, cert-manager's events (CertificateExpiring is the inside view of the same certificate) |
| AdmissionPolicyViolation | info | a Warn+Audit binding (or Pod Security's warn/audit mode) flagged an admitted request in the last 10 min; not for bindings that deny. Info, not warning: findings that repeat on every Argo CD refresh would page all day | OPERATIONS → Admission policy signals: the day's Logs Insights query by object |
| AdmissionPolicyDenied | warning | a Deny+Audit binding refused a request in the last 10 min (not `ryft-workloads-pods`, `ryft-workloads-pods-vault`, `ryft-operator-reach`, `ryft-auth-env`) | `argocd app get`, the audit annotation; fix the object in Git |
| AdmissionPodDenied | critical | a Pod-capable binding (`ryft-workloads-pods`, `ryft-workloads-pods-vault`, `ryft-operator-reach`, `ryft-auth-env`: step C, vault's pods and P5, the design's critical class) or Pod Security's enforce mode refused a request in the last 10 min. The counter has no resource label, so a P5 or P8 refusal of another object pages too | `FailedCreate` events; break-glass the binding first if workloads cannot be re-created |
| AdmissionPolicyError | critical / warning | a policy's CEL could not be evaluated in the last 10 min (`error_type` not `no_error`): critical when the binding refuses (the request fails under `Fail`), warning under Warn+Audit | the expression meets an unexpected object shape; break-glass, then fix the chart |
| AdmissionBindingMissing | critical | fewer than 17 `ryft-*` bindings for 10 min, once some existed in the last day | `kubectl get validatingadmissionpolicybindings`, kube-state-metrics, the `[0]` replace apply (OPERATIONS) |
| AdmissionBindingNotDenying | warning | a binding committed as deny has no `Deny` while other bindings do, 10 min | a fast-path patch left in place; the `[0]` replace apply |
| AdmissionBindingDrift | warning | the other mismatches with the committed action (deny without Audit; audit with Deny or without Warn or Audit), 2 h | the same |
| AdmissionBreakGlass | warning | no `ryft-*` binding has `Deny` while at least one is committed as deny, 4 h | end it with `admission_break_glass=false` |
| AdmissionSignalsMissing | warning | no `up` for the API-server scrape or the bindings scrape for 15 min (also before the monitoring values are applied) | `kubectl -n monitoring get pods`, the collector log |
| AdmissionScrapeDown | warning | `up == 0` for an admission target, 15 min | the same; remote_write errors in the collector log |
| SyntheticsMissing | warning | no `probe_success` for one of the four `ryft-site-*` jobs for about 25 min (10 min window + 15 min; label `job`) | the SM app: that check present and enabled, the execution limit; re-apply the checks (OPERATIONS → Monitoring) |

### Admission

The `Admission*` alerts (group `ryft-admission` in `ryft.yaml`) read the metrics above. Every
selector is pinned by `job`. Properties that are deliberate:

- **Counter alerts need no gate on the bindings.** They match `policy=~"ryft-.*"`, which only
  exists once the chart is enabled, and Pod Security's enforce mode is live today, so its
  refusals page now. The binding alerts (Missing, NotDenying, Drift, BreakGlass) are quiet in
  Phase 0 because they join on bindings that do not exist. `AdmissionBindingMissing` looks back
  a day, so a chart switched off on purpose raises it for up to a day (silence it), and one that
  never ran does not.
- **The first-event gap.** A counter child is created at 1 by its first event, so
  `increase()` is empty for the first violation, denial or error of a policy and binding.
  Two recording rules (`ryft:apiserver_admission_policy_check:fresh10m`,
  `ryft:pod_security_evaluations:fresh10m`) add the series that have no sample in the hour
  before the last 10 minutes (`unless last_over_time(... [1h] offset 10m)`), but only on an
  endpoint whose scrape was up 12 minutes ago (`up offset 12m == 1`), so turning the scrape
  on, or recovering it after an outage, does not make every old counter look new. A single
  event fires for 10 minutes and resolves. A new API-server endpoint (EKS replaces
  control-plane instances) is blind for its first 12 minutes.
- **The committed action is a recording rule.** kube-state-metrics cannot export a binding's
  labels, so `deploy/grafana/alerts/admission-intended.yaml` (namespace `ryft-admission` in the
  ruler) holds `ryft:admission_binding_intended` per binding, generated from
  `infra/aws/kubernetes/admission-values.yaml`. When a step changes `actions:`, regenerate it
  (`scripts/admission_intended.sh`) in the same PR; `make alerts-check` fails otherwise. Load
  the rules after the Terraform apply (the other order raises `NotDenying`; this one a
  Drift that waits two hours and clears when the rules load).
- **Break-glass is seen from the live actions.** A binding committed as deny without `Deny`
  while others deny is `AdmissionBindingNotDenying` (10 min); when no binding denies it is
  `AdmissionBreakGlass` (4 h) instead. `AdmissionBindingDrift` covers the rest, after 2 h.
- **Liveness is not gated.** `AdmissionSignalsMissing` and `AdmissionScrapeDown` fire when a
  scrape is gone or failing; the clean-pass counter is not a liveness signal (it does not
  exist).

Series budget (free tier, 10k active series): the three API-server families are about 250
series at most (17 bindings x 3 or 4 action and error children x 2 API servers, the latency
as `_sum` and `_count`, Pod Security refusals only), kube-state-metrics 17 (`info`) + at most
34 (`validation_action`: Deny and Audit or Warn and Audit per binding) + 2, the 17 recorded
intended series, and `up` plus `scrape_samples_scraped` per target; about 350 in total. Deny
and error children appear only after their first event.

To confirm in the kind e2e before Phase 1: the `enforcement_action` and `error_type` label
values on EKS's Kubernetes minor (which of them an evaluation error carries), that
`policy_binding` is the binding's name, and that Pod refusals carry `resource="pod"`.

`NoSync` needs a scrape since the last ingest restart (the gauge is in memory), hence the
`absent()`. Per-tester staleness is a dashboard panel, not a page: one tester on holiday
is no incident.

## Baselines

Recorded numbers a later change is compared with. **A laptop is not production**: these come from the
compose stack on one machine, Postgres in a container on a local disk, no network between the services, no
Supabase pooler, no TLS at the edge, one replica of everything. Use them for "did this change get slower
than it was", never as a capacity statement or an SLO.

### Local load (`make load`, `scripts/load.sh`)

What it does: k6 (`grafana/k6:2.3.0`, pinned by digest in the script, run by `$CONTAINER` on the compose network)
drives 50 synthetic users. Each consents, syncs **one full UTC day** (about 1 250 records: heart rate every minute from
waking to midnight, steps and active energy in 10-minute buckets with the phone's 30-minute steps beside them, a night
of sleep with stages, overnight HRV, respiration, SpO2, resting HR, one run; the shape of `scripts/synth.py`, generated
from a seeded PRNG so the bytes are the same every run) and then reads `GET /v1/days?from=…&to=…` over 30 days 20 times
with 250 ms think time. The users start 100 ms apart. One warm-up run, then 3 measured runs, each with fresh accounts;
the script waits until scoring has finished (the `ryft_sync_to_score_seconds` count stops rising) before it scrapes
`/metrics` of ingest, api and scoring, which are not published to the host, over the compose network.
`make up && make load`; it prints the table, writes `scripts/load/out/<UTC timestamp>/` (ignored by version control;
`baseline.json` has every run and the median run; `out/latest` names the newest; nothing is deleted, and `LOAD_OUT` is
refused if it is `/`, `$HOME` or a non-empty directory without the `.ryft-load-out` marker) and exits 1 when any
route's error rate is above 1 % (`LOAD_MAX_ERROR_RATE`) or an optional k6 threshold (`LOAD_P95_SYNC_MS`,
`LOAD_P95_DAYS_MS`) failed. In local mode `INGEST_URL` and `API_URL` are only the readiness probes from the host; k6
talks to the services over the compose network. A plain URL on localhost is required: `LOAD_ALLOW_REMOTE=1` allows
other plain hosts (with `LOAD_REMOTE_API_KEY`, never the ambient `API_KEY`), but never `*.ryftapp.de` or
`*.supabase.co`; never aim it at production. Each invocation leaves 200 accounts and about 250 000 samples in the dev
database (`make down` clears it).

How 50 users exist locally: `AUTH_MODE=apikey` accepts the bearer `<API_KEY>:<uuid>` as that user
(`internal/auth/apikey.go`), so one key is enough and no auth code changes.

Rate limits (`internal/ratelimit`, per user): `POST /v1/sync` 2/s with bursts of 60, everything else 10/s with bursts
of 60. The test does one sync per user and 4 reads/s per user, so it stays under both: client-side 429s 0 and
`ryft_rate_limited_total` 0 in every run. No override was needed or set.

Metric: `ryft_sync_to_score_seconds` (histogram, scoring; buckets 1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800 s) is
taken as the difference of the buckets before and after each run and read like `histogram_quantile`. The buckets
are coarse: a value under 1 s is interpolated inside the first bucket (read it as "under a second"), and the 2.4, 4.0,
4.4 and 4.6 s values below are linear interpolation inside the (2, 5] s bucket: read them as "in the 2 to 5 s bucket".
Each run observes 102 days (about two queued days per user).

| | |
| --- | --- |
| Date | 2026-11-04 |
| Commit | `fb7b971` (main; the load tooling is scripts only) |
| Machine | MacBook, Apple M4 Pro, 12 cores, 48 GB, macOS 27.2; podman 6.1.4, libkrun VM of 4 vCPU and 7.9 GiB; Postgres 17-alpine and RabbitMQ 4.3.6 in that VM, volumes on the VM disk |
| Other load | another project's throwaway Postgres ran in the same VM (about 13 % of a vCPU); the machine was otherwise in ordinary use |
| Stack | `make up` with the default compose file; single replica of ingest, api, scoring, notify |

Three measured runs (invocation 1; ms unless noted; sync→score in s from the buckets). Warm-up run before them:
sync p50 222 / p95 465 ms.

| Run | `POST /v1/sync` p50 / p95 / p99 | `GET /v1/days` p50 / p95 / p99 | errors | sync→score p50 / p95 |
| --- | --- | --- | --- | --- |
| 1 | 98.9 / 1 252 / 1 413 | 18.4 / 167 / 254 | 0 % | 0.5 / 0.9 s |
| 2 | 1 573 / 4 564 / 4 607 | 16.8 / 422 / 986 | 0 % | 0.8 / 4.0 s |
| 3 (median) | 520 / 4 275 / 4 486 | 16.5 / 221 / 652 | 0 % | 0.8 / 2.4 s |

**Recorded baseline = the median run (run 3, by sync p95)**: sync p50 520 ms, p95 4.3 s, p99 4.5 s; days p50 16.5 ms,
p95 221 ms, p99 652 ms; error rate 0 %; `ryft_sync_to_score_seconds` p95 2.4 s.

The spread is wide and honest: the sync p95 over the three runs is 1.3 to 4.6 s, and a second invocation straight
after (a fresh set of accounts, same stack) gave sync p95 of 4.4 / 0.23 / 2.5 s, days p95 of 97 / 26 / 66 ms and
sync→score p95 of 4.4 / 0.9 / 4.6 s. The runs are bimodal: either every upload takes about 100 to 250 ms (p95 under
0.5 s) or the burst of 50 queues behind Postgres (WAL and fsync on the VM disk, the shared VM) and takes 2 to 4 s. The
days p50 alone moved between 7 and 18 ms. **Nothing here is stable enough to gate on except the error rate.** The later
nightly job therefore gates only on the error rate (one failed sync of 50 is 2 %, above the 1 % limit); latency is
reported, not gated, until a quieter runner exists. With n = 50 syncs per run the p95 sits between the 3rd and 2nd
slowest upload and the p99 is about the maximum, so they are single events, not distributions. `GET /v1/days` reads
one scored day each (the users have a single synced day), lighter than a tester with months of history.

A check of the revised script on a quieter machine (no other database in the VM) gave sync p95 of 83 and 73 ms and days
p95 of 8 and 6 ms over two measured runs, an order of magnitude below the table: the same code, so the table mostly
measures the machine's state that day.

The table was measured with the first version of the tooling (tree of `fb7b971` plus the first commit of the load
scripts); the verdict, threshold, output-directory and drain logic changed afterwards (review fixes).

## Logs

One JSON object per line on stdout, written by `internal/logx`. `LOG_LEVEL`
(`debug`, `info` — default —, `warn`, `error`) sets the level; it is set in `deploy/k8s/base/<service>/config.env` (migrate's, in `deploy/k8s/base/migrate/configmap.yaml`).

| Field | On | Meaning |
| --- | --- | --- |
| `time`, `level`, `msg`, `service` | every line | `service` is `ingest`, `scoring` or `api`. |
| `request_id` | every line logged while serving a request; scoring lines for an event | The ID the server gave the request (UUIDv7, also the `X-Request-ID` response header and `request_id` in error bodies). Scoring logs the ID of the sync or recompute request that caused the event, so one ID follows a sync from ingest through scoring. |
| `client_request_id` | same | A client-sent `X-Request-ID` (if it looks like an ID). Logged only, never used as ours. |
| `user_id` | once the token is verified; scoring per event and per user in the 15-minute pass | The Supabase user ID (`sub`). |
| `route` | request lines once routed | ServeMux pattern, e.g. `GET /v1/days/{day}`. |
| `method`, `path`, `status`, `bytes`, `dur_ms` | the access line (`msg="http"`, one per request) | `status` `499`: the client gave up before the answer (logged at info, not an error). The `path` of `DELETE /v1/admin/invites/{code}` is that pattern, not the code (D160). |
| `code` | the access line of a refused request | The error code (`invite_required`, `consent_required`, `import_in_progress`) or, for a `401`/auth `503`, the reason (`expired`, `signature`, `deleted`, …). |
| `result`, `plans`, `sessions`, `created`, `unchanged`, `updated`, `rejected`, `days`, `duration_ms` | the api's `train import`, one per import that took the lease | Its result as in `ryft_train_imports_total`, the items sent and what happened to them (counts over plans and sessions), the days queued. Never a title, a note, a value or an id list (D114, D117; `cmd/api/train_import_logs_test.go`). |
| `gate` | the access line of a request a gate in `warn` mode let through | The code(s) it would have been refused with, as in `X-RYFT-Gate`. |
| `err` | errors and warnings | The internal error. Clients only ever see `"internal error"` and the request ID. |
| `day`, `model_version`, `inputs_changed` | scoring's `scored day`, one per day scored | The day, the model that scored it, and whether its history inputs changed. The days after it are scored again when they did or when the event was a retry (`inputs_changed` is then often `false`: the first attempt wrote the change): `rescored days after a history change`. No score, input or state: those are in `daily_scores` and the admin view. |

**Lifecycle lines.** A service started through `internal/app` (scoring and notify; api and
ingest follow) logs its start and stop with these lines and keys (the closed sets are
pinned by `internal/app/logs_test.go`, D114, D117). OPERATIONS → A deploy: what the old pod
logs has the Insights queries.

| Message | Keys | When |
| --- | --- | --- |
| `listening`, `metrics listening` | `addr` | the HTTP port is open (last step of the start), the `/metrics` port is open |
| `consuming ingest events` (scoring), `consuming scored events` (notify) | none | the consumer starts |
| `config`, `beta config`, `apns config`, `apns`, `db`, `events`, `build`, `serve` | `err` | the start failed in that area; the process exits 1. `serve`: the HTTP port would not bind (it used to be logged while the service ran on without probes) |
| `consume`, `job`, `serve`, `background`, `metrics` | `task`, `err` | a task of that tier returned an error before the shutdown: it stops the service (exit 1). `task` is `ingested events`, `scored events`, `scoring pass`, `stale check`, `http`, `outbox relay`, `db watch`, `metrics` |
| `panic` | `task` (or `check` for a `/readyz` check), `panic`, `stack` | a task panicked: counted in `ryft_goroutine_panics_total`, the service shuts down in order and exits 2 |
| `shutting down` | `reason` (`signal`, `task`, `start`), `task`, `err` | the shutdown began; `err` is one line, cut |
| `shutdown step` | `step` (`drain`, `consumers`, `http`, `jobs`, `relay`, `broker`, `pool`, `metrics`), `dur_ms`, `err` | one per step, at info; at warn with `err` when it failed or overran its bound |
| `shut down` | `dur_ms`, `exit` (0, 1, 2) | the shutdown is over; `exit` is the process's exit code |
| `shutdown overdue` | `step`, `elapsed_ms` | the watchdog: the shutdown passed 26 s, the process exits at once (1, or 2 after a panic) |
| `task ended with error during shutdown` | `task`, `err` | a task returned an error after the shutdown began (warn, not a failure) |

Never logged: health values (scores, samples, sleep, workouts, baselines, the learned
sleep need, the readiness state), tokens (APNs device tokens included), the
`Authorization` header, email addresses, device names, request bodies, feedback text.
OPERATIONS → Data protection (logs) has the two exceptions (a panic's stack, a pgx
encode error), the tests that pin the rule and what older lines still hold. The lines
tie `user_id` to paths with dates, so limit who can read them and keep them no longer
than you need.

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
Today that is the account's administrators only. Treat any grant as access to the users'
personal data, and until 14 days after the deploy that stopped logging health values
(OPERATIONS → Data protection (logs), older lines) to their scores: no broad
`CloudWatchLogsReadOnlyAccess` for others, and no export to buckets or third parties. Cost at alpha volume is around €1 a month, plus €1 for the key.
