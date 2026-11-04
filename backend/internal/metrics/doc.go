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
// wrap around their mux, so scoring and notify emit none. The RabbitMQ connection
// metrics are registered by UseAMQP, which amqpbus.New calls before it dials, so a
// binary that never creates a bus (migrate) exposes none.
//
// See docs/OBSERVABILITY.md (every metric, with the dashboard queries); D14.
package metrics
