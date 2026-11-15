// Package metrics owns every Prometheus metric the services export, declared in
// one place, and serves them on /metrics.
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
// The services still serve /metrics on their own port (METRICS_PORT); gauges a
// tick sets (ryft_db_up, the outbox gauges) show the last tick's values (D219).
//
// See docs/OBSERVABILITY.md (every metric, with the dashboard queries); D14.
package metrics
