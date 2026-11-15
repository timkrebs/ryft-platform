// Package metrics owns every Prometheus metric the services export, declared in
// one place, and serves them on /metrics or pushes them by OTLP.
//
// It must not import another package of this module, and no metric takes a user
// id or free text as a label: HTTP metrics use the ServeMux route pattern, never
// the path, so cardinality stays bounded (D14).
//
// Every service registers every metric declared here, but most are vectors, and a
// vector appears in a service's output only once that service records a label
// set; the few plain counters and histograms (journal saves, pruned push tokens,
// sync-to-score, the Train totals) are exported at zero by every service. The
// ryft_http_* metrics are recorded by httpx.Instrument, which only api and ingest
// wrap around their mux, so scoring and notify emit none (their push and tick
// routes count in ryft_events_handled_total and ryft_ticks_total). There are no
// transport connection metrics: publishing to Pub/Sub is one REST call per event,
// with no connection to watch (D218).
//
// # Where the metrics go
//
// On Cloud Run nothing scrapes: OTLP pushes the registry to Grafana Cloud's OTLP
// gateway at the end of each tick and once on SIGTERM (D224), so gauges a tick sets
// (ryft_db_up, the outbox gauges) arrive with the tick that set them. Without Cloud
// Run (compose) the services serve /metrics on their own port (METRICS_PORT), and
// OTLP_ENDPOINT is empty, so nothing is pushed.
//
// A push is one OTLP/HTTP request, POST <endpoint>/v1/metrics, JSON (the proto3 JSON
// mapping: 64-bit integers as strings, enums as numbers, NaN and ±Inf as strings),
// gzipped, with basic auth (instance ID, token). The resource is service.namespace
// "ryft", service.name and service.instance.id (random per process unless given),
// which Grafana Cloud turns into job="ryft/<svc>" and instance; the series' labels are
// the points' attributes (an empty value left out). Counters become cumulative
// monotonic Sums, gauges and untyped values Gauges, histograms explicit-bucket
// Histograms (per-bucket counts from Prometheus' cumulative ones; the bounds without
// +Inf, whose count is the total less the last finite bucket), summaries Summaries.
// Cumulative points start at the process start. A family without series is left out,
// and so is a gauge histogram (none is registered).
//
// # Names: one _total, no unit
//
// The alerts and dashboards read the names the registry has (ryft_ticks_total), so
// each metric is sent under exactly that name, with no unit and no scope. Reasoning,
// from the Prometheus OTLP translator Mimir uses (prometheus/otlptranslator, earlier
// the collector's pkg/translator/prometheus), as remembered and not checked against
// Grafana Cloud's current deployment:
//   - With metric suffixes off (Mimir's default, -distributor.otel-metric-suffixes-enabled)
//     the name is only sanitised: ryft_ticks_total stays ryft_ticks_total.
//   - With suffixes on, a monotonic Sum gets "_total" after every "total" token of
//     the name is removed (or only when the name lacks the suffix, in the UTF-8 path),
//     so ryft_ticks_total stays ryft_ticks_total, not ryft_ticks_total_total. No
//     registered name has "total" anywhere but at its end. A unit would add its suffix
//     (unless the name holds it already), and unit "1" adds _ratio to a gauge; an empty
//     unit adds nothing, so none is sent: ryft_http_request_duration_seconds keeps its
//     name and gains only _bucket, _sum and _count.
//   - Stripping _total and leaving it to the translator would be right only with
//     suffixes on; keeping it is right either way.
//
// Unsure, to check after the first push (OBSERVABILITY.md → The OTLP push): whether
// Grafana Cloud also promotes resource attributes to labels (service_name, …), which
// adds labels but changes no name; whether it uses startTimeUnixNano to write a zero
// sample at the start (created-timestamp ingestion), which would let increase() see a
// fresh instance's first count and make the rules that add increase() and the
// new-series term (RateLimited, AdminUnauthorized) count that instance twice; and
// whether a scope-less request gets otel_scope_* labels anyway (empty, so dropped).
//
// See docs/OBSERVABILITY.md (every metric, with the dashboard queries, and the push);
// D14, D224.
package metrics
