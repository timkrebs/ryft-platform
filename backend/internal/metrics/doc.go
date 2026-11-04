// Package metrics owns every Prometheus metric the services export, declared in
// one place, and serves them on /metrics.
//
// It must not import another package of this module, and no metric takes a user
// id or free text as a label: HTTP metrics use the ServeMux route pattern, never
// the path, so cardinality stays bounded (D14).
//
// All service-specific metrics are vectors, and a vector appears in a service's
// output only once that service records a label set: ingest shows ingest
// metrics, scoring shows scoring metrics, and all four services (api, ingest,
// scoring, notify) show the HTTP, Go runtime and process metrics. The RabbitMQ
// connection metrics are registered by UseAMQP when a service connects, so a
// binary that never connects (migrate) exposes none.
//
// See docs/OBSERVABILITY.md (every metric, with the dashboard queries); D14.
package metrics
