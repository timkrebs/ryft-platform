# RYFT — observability

Each service (ingest, scoring, api) serves Prometheus metrics on `GET /metrics`
(port 8080, no auth). The Ingress only routes `/v1/*`, so `/metrics` is reachable
inside the cluster only. Logs are JSON (`slog`) on stdout.

## Scraping

The pod templates carry the conventional annotations; there is no `ServiceMonitor`:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "8080"
prometheus.io/path: /metrics
```

A Prometheus that discovers pods by annotation needs a job like this (the standard
`kubernetes-pods` job from the Prometheus Helm chart does exactly this):

```yaml
- job_name: kubernetes-pods
  kubernetes_sd_configs: [{ role: pod }]
  relabel_configs:
    - { source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_scrape], action: keep, regex: "true" }
    - { source_labels: [__meta_kubernetes_pod_annotation_prometheus_io_path], target_label: __metrics_path__, regex: (.+) }
    - source_labels: [__address__, __meta_kubernetes_pod_annotation_prometheus_io_port]
      regex: ([^:]+)(?::\d+)?;(\d+)
      replacement: $1:$2
      target_label: __address__
    - { source_labels: [__meta_kubernetes_namespace], target_label: namespace }
    - { source_labels: [__meta_kubernetes_pod_label_app], target_label: app }
    - { source_labels: [__meta_kubernetes_pod_name], target_label: pod }
```

The queries below assume the `app` label from that relabelling (`app="ingest"` etc.).
kube-prometheus-stack ignores these annotations by default; there, add a `PodMonitor`
selecting `part-of: ryft` with `podMetricsEndpoints: [{port: http}]`.

The `NetworkPolicy` in `deploy/k8s` admits scrapes from the namespace named
`monitoring` only; change `ryft-allow-metrics` if Prometheus lives elsewhere.

## Metrics

| Metric | Type | Labels | Service | Meaning |
| --- | --- | --- | --- | --- |
| `ryft_http_requests_total` | counter | `route`, `code` | all | Requests by ServeMux pattern (`GET /v1/days/{day}`, never the raw path) and status. Unauthenticated requests carry the outer pattern `/v1/`; unknown routes `unmatched`. |
| `ryft_http_request_duration_seconds` | histogram | `route` | all | Request latency. |
| `ryft_samples_ingested_total` | counter | `kind`, `type` | ingest | Records accepted by `POST /v1/sync`: `kind="sample"` with the sample type, `kind="sleep"` with the stage, `kind="workout"`. |
| `ryft_last_sync_timestamp_seconds` | gauge | `device` | ingest | Unix time of the device's last successful sync. Resets on restart. |
| `ryft_events_published_total` | counter | `reason`, `result` | ingest, api | `ryft.ingested` publishes (`sync` from ingest, `recompute` from api), `ok`/`error`. A publish error does not fail the sync — this is the only place it shows. |
| `ryft_events_handled_total` | counter | `reason`, `result` | scoring | Consumed events: `ok`, `retry` (NAKed, redelivered in 30 s), `failed` (10th delivery failed — dropped), `bad` (unparseable). |
| `ryft_nats_redeliveries_total` | counter | `consumer` | scoring | Messages received with a delivery count > 1. |
| `ryft_days_scored_total` | counter | `result` | scoring | Days scored (`ok`/`error`), from events and the 15-minute timer. |
| `ryft_scoring_duration_seconds` | histogram | `result` | scoring | Time to score one day: load, compute, write. |
| `go_*`, `process_*` | | | all | Go runtime and process collectors. |

## Grafana dashboard — panel list

Variables: `$namespace` (default `ryft`). All rates over `[5m]` unless noted.

**Row: Health at a glance**

| Panel | Type | Query |
| --- | --- | --- |
| Time since last sync | stat (thresholds 6 h / 24 h) | `time() - max(ryft_last_sync_timestamp_seconds)` |
| Syncs (24 h) | stat | `sum(increase(ryft_http_requests_total{app="ingest",route="POST /v1/sync",code="200"}[24h]))` |
| Days scored (24 h) | stat | `sum(increase(ryft_days_scored_total{result="ok"}[24h]))` |
| API error ratio | stat (%) | `sum(rate(ryft_http_requests_total{app="api",code=~"5.."}[5m])) / sum(rate(ryft_http_requests_total{app="api"}[5m]))` |
| Pods up | stat | `sum by (app) (up{namespace="$namespace"})` |

**Row: Ingest**

| Panel | Type | Query |
| --- | --- | --- |
| Sync requests by status | time series | `sum by (code) (rate(ryft_http_requests_total{route="POST /v1/sync"}[5m]))` |
| Sync latency p50 / p95 / p99 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_http_request_duration_seconds_bucket{route="POST /v1/sync"}[5m])))` (repeat for 0.5, 0.99) |
| Samples ingested by type | stacked bars, 1 h buckets | `sum by (type) (increase(ryft_samples_ingested_total{kind="sample"}[1h]))` |
| Sleep segments / workouts | time series | `sum by (kind) (increase(ryft_samples_ingested_total{kind!="sample"}[1h]))` |
| Last sync per device | table | `ryft_last_sync_timestamp_seconds * 1000` (unit: datetime) |

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
| Memory (RSS) | time series | `process_resident_memory_bytes{namespace="$namespace"}` |
| Goroutines | time series | `go_goroutines{namespace="$namespace"}` |
| CPU | time series | `rate(process_cpu_seconds_total{namespace="$namespace"}[5m])` |

NATS exposes its own monitoring on `:8222` (`/varz`, `/jsz`); the
`prometheus-nats-exporter` turns it into metrics if you want stream depth. For
Postgres, `postgres_exporter` with the `ryft` credentials.

## Suggested alerts

| Alert | Expression | For |
| --- | --- | --- |
| NoSync | `time() - max(ryft_last_sync_timestamp_seconds) > 6*3600` | 15m |
| ScoringErrors | `sum(rate(ryft_days_scored_total{result="error"}[15m])) > 0` | 15m |
| EventsDropped | `sum(increase(ryft_events_handled_total{result=~"failed\|bad"}[1h])) > 0` | 0m |
| PublishFailing | `sum(rate(ryft_events_published_total{result="error"}[10m])) > 0` | 10m |
| API5xx | `sum(rate(ryft_http_requests_total{code=~"5.."}[5m])) > 0.05` | 10m |
| SyncSlow | `histogram_quantile(0.95, sum by (le) (rate(ryft_http_request_duration_seconds_bucket{route="POST /v1/sync"}[15m]))) > 10` | 15m |

`NoSync` needs a scrape since the last ingest restart (the gauge is in memory); after
a restart it is absent until the next sync, so pair it with `absent()` if you want it
to fire then too.
