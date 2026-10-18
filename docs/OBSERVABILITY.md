# RYFT — observability

Each service (ingest, scoring, api) serves Prometheus metrics on `GET /metrics` on its
own port, **9090** (`METRICS_PORT`, no auth), apart from the API and probe port 8080.
The Ingress only routes `/v1/*`, so `/metrics` is never public. Logs are JSON (`slog`)
on stdout and shipped to CloudWatch; see **Logs** below.

## Collection: Grafana Cloud

Metrics go to Grafana Cloud (free tier; D51). The collector is Grafana's
`k8s-monitoring` chart (`deploy/monitoring/k8s-monitoring.yaml`, `make
monitoring-apply`) in namespace `monitoring`. Its pipelines are configured remotely in
Grafana Cloud's Fleet Management, and it discovers the RYFT pods through their
annotations:

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

Alert rules: `deploy/monitoring/alerts.yaml` (Prometheus format; load with `mimirtool
rules load`, or recreate them under Alerting); the table below explains them.

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
| `ryft_rate_limited_total` | counter | `class` | ingest, api | Requests answered `429` by the per-user rate limit: `class="sync"` (`POST /v1/sync`) or `default`. The recompute gate (one per minute) is not counted here. |
| `ryft_http_panics_total` | counter | `route` | all | Handler panics turned into a `500` by the recover middleware. Each also logs `msg="panic"` with the stack. |
| `ryft_auth_failures_total` | counter | `reason` | ingest, api | Rejected `/v1` requests. 401 reasons: `missing_token`, `malformed`, `expired`, `signature`, `issuer`, `audience`, `unknown_key`, `key_mismatch`, `role`, `anonymous`, `subject`, `api_key`, `deleted` (the account was deleted), `invalid`. 503 reasons: `jwks_unavailable`, `jwks_refresh_limited`, `internal` (user lookup failed). |
| `ryft_account_deletions_total` | counter | `step`, `result` | api | `DELETE /v1/me` and the reconciler. `step="data"` (our rows), `apple` (revoking the Sign in with Apple token) or `auth` (the Supabase auth user); `result` `ok`, `error` or `skipped` (not configured: no `APPLE_*` / `SUPABASE_SERVICE_ROLE_KEY`, or apikey mode). |
| `ryft_events_published_total` | counter | `reason`, `result` | ingest, api | `ryft.ingested` publishes (`sync` from ingest, `recompute` from api), `ok`/`error`. A publish error does not fail the sync — this is the only place it shows. |
| `ryft_events_handled_total` | counter | `reason`, `result` | scoring | Consumed events: `ok`, `retry` (NAKed, redelivered in 30 s), `failed` (10th delivery failed — dropped), `bad` (unparseable), `dropped` (cannot succeed, e.g. the user was deleted). |
| `ryft_nats_redeliveries_total` | counter | `consumer` | scoring | Messages received with a delivery count > 1. |
| `ryft_days_scored_total` | counter | `result` | scoring | Days scored (`ok`/`error`), from events and the 15-minute timer. |
| `ryft_scoring_duration_seconds` | histogram | `result` | scoring | Time to score one day: load, compute, write. |
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
| Events handled by result | time series | `sum by (reason, result) (rate(ryft_events_handled_total[5m]))` |
| NATS redeliveries | time series | `sum(rate(ryft_nats_redeliveries_total[5m]))` |
| Publish errors | time series | `sum by (app, reason) (rate(ryft_events_published_total{result="error"}[5m]))` |

**Row: API**

| Panel | Type | Query |
| --- | --- | --- |
| Requests by route | time series | `sum by (route) (rate(ryft_http_requests_total{app="api",route=~".*/v1/.*"}[5m]))` |
| Latency p95 by route | time series | `histogram_quantile(0.95, sum by (route, le) (rate(ryft_http_request_duration_seconds_bucket{app="api"}[5m])))` |
| 4xx / 5xx by route | time series | `sum by (route, code) (rate(ryft_http_requests_total{app="api",code=~"[45].."}[5m]))` |

**Row: Runtime** (repeat by `app`)

| Panel | Type | Query |
| --- | --- | --- |
| Memory (RSS) | time series | `process_resident_memory_bytes{namespace=~"$namespace"}` |
| Goroutines | time series | `go_goroutines{namespace=~"$namespace"}` |
| CPU | time series | `rate(process_cpu_seconds_total{namespace=~"$namespace"}[5m])` |

NATS exposes its own monitoring on `:8222` (`/varz`, `/jsz`); the
`prometheus-nats-exporter` turns it into metrics if you want stream depth (it needs a
NetworkPolicy rule in `ryft-nats` for 8222). For the in-cluster Postgres of
`local-postgres`, `postgres_exporter` with a read-only role.

## Suggested alerts

| Alert | Expression | For |
| --- | --- | --- |
| NoSync | `time() - max(ryft_last_sync_timestamp_seconds) > 6*3600` | 15m |
| ScoringErrors | `sum(rate(ryft_days_scored_total{result="error"}[15m])) > 0` | 15m |
| EventsDropped | `sum(increase(ryft_events_handled_total{result=~"failed\|bad"}[1h])) > 0` | 0m |
| AuthUnavailable | `sum(rate(ryft_auth_failures_total{reason=~"jwks_.*\|internal"}[5m])) > 0` | 5m |
| PublishFailing | `sum(rate(ryft_events_published_total{result="error"}[10m])) > 0` | 10m |
| API5xx | `sum(rate(ryft_http_requests_total{code=~"5.."}[5m])) > 0.05` | 10m |
| Panics | `sum(increase(ryft_http_panics_total[15m])) > 0` | 0m |
| AccountDeletionIncomplete | `sum(increase(ryft_account_deletions_total{step=~"auth\|apple",result=~"error\|skipped"}[1h])) > 0` (deleted accounts whose Supabase sign-in or Apple grant still exists; check the keys) | 0m |
| RateLimited | `sum(rate(ryft_rate_limited_total[10m])) > 1` (a client stuck in a retry loop, or limits too tight) | 10m |
| SyncSlow | `histogram_quantile(0.95, sum by (le) (rate(ryft_http_request_duration_seconds_bucket{route="POST /v1/sync"}[15m]))) > 10` | 15m |

`NoSync` needs a scrape since the last ingest restart (the gauge is in memory); after
a restart it is absent until the next sync, so pair it with `absent()` if you want it
to fire then too.

## Logs

One JSON object per line on stdout, written by `internal/logx`. `LOG_LEVEL`
(`debug`, `info` — default —, `warn`, `error`; ConfigMap `ryft-config`) sets the level.

| Field | On | Meaning |
| --- | --- | --- |
| `time`, `level`, `msg`, `service` | every line | `service` is `ingest`, `scoring` or `api`. |
| `request_id` | every line logged while serving a request; scoring lines for an event | The ID the server gave the request (UUIDv7, also the `X-Request-ID` response header and `request_id` in error bodies). Scoring logs the ID of the sync or recompute request that caused the event, so one ID follows a sync from ingest through scoring. |
| `client_request_id` | same | A client-sent `X-Request-ID` (if it looks like an ID). Logged only, never used as ours. |
| `user_id` | once the token is verified; scoring per event and per user in the 15-minute pass | The Supabase user ID (`sub`). |
| `route` | request lines once routed | ServeMux pattern, e.g. `GET /v1/days/{day}`. |
| `method`, `path`, `status`, `bytes`, `dur_ms` | the access line (`msg="http"`, one per request) | |
| `err` | errors and warnings | The internal error. Clients only ever see `"internal error"` and the request ID. |

Never logged: tokens, the `Authorization` header, email addresses, request bodies.
Scoring's `scored day` lines do carry a user's daily scores (readiness, HRV, resting HR)
next to `user_id`, so treat the logs as health data: limit who can read them and keep
them no longer than you need.

**Finding a request** a user reports (the app shows or logs `request_id`):

```sh
for s in ingest api scoring; do kubectl -n ryft-$s logs -l app=$s --prefix --since=24h --max-log-requests=10; done \
  | grep 01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c
```

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
