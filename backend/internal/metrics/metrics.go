// Package metrics declares every Prometheus metric the three services export
// and serves them on /metrics. All service-specific metrics are vectors: a
// vector only appears in a service's output once that service records a label
// set, so ingest shows ingest metrics, scoring shows scoring metrics, and all
// three show the HTTP, Go runtime and process metrics. docs/OBSERVABILITY.md
// lists them with the dashboard queries.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// HTTP, all services. route is the ServeMux pattern ("GET /v1/days/{day}"),
	// never the raw path, so cardinality stays bounded.
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_requests_total",
		Help: "HTTP requests by route pattern and status code.",
	}, []string{"route", "code"})
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ryft_http_request_duration_seconds",
		Help:    "HTTP request latency by route pattern.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"route"})

	// ingest.
	Ingested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_samples_ingested_total",
		Help: "Records accepted by POST /v1/sync. kind=sample (type = sample type), sleep (type = stage) or workout.",
	}, []string{"kind", "type"})
	LastSync = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_last_sync_timestamp_seconds",
		Help: "Unix time of the last successful sync (any user; per-user detail would be unbounded).",
	}, []string{})

	// all services.
	Panics = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_panics_total",
		Help: "Handler panics recovered (answered 500), by route pattern.",
	}, []string{"route"})

	// ingest + api.
	RateLimited = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_rate_limited_total",
		Help: "Requests answered 429 by the per-user limit, by class (sync, default).",
	}, []string{"class"})
	AuthFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_auth_failures_total",
		Help: "Rejected /v1 requests by reason: missing_token, expired, signature, issuer, audience, unknown_key, " +
			"role, anonymous, subject, api_key, deleted, … (401) and jwks_unavailable, jwks_refresh_limited, internal (503).",
	}, []string{"reason"})
	AccountDeletions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_account_deletions_total",
		Help: "Account deletion steps: step=data (our rows), apple (revoking the Sign in with Apple token) or auth (the Supabase auth user); result=ok, error or skipped (not configured).",
	}, []string{"step", "result"})

	// NATS, ingest + api publish, scoring consumes.
	EventsPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_published_total",
		Help: "Events published, by reason (sync, recompute, session, session_completed) and result (ok, error).",
	}, []string{"reason", "result"})
	EventsHandled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_handled_total",
		Help: "Events consumed (ryft.ingested, ryft.session.completed), by reason and result: ok, retry (NAKed for redelivery), " +
			"failed (last delivery failed, JetStream gives up), bad (unparseable, terminated), " +
			"dropped (cannot succeed, e.g. the user was deleted; terminated).",
	}, []string{"reason", "result"})
	Redeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_nats_redeliveries_total",
		Help: "Messages received with a delivery count above 1, per consumer.",
	}, []string{"consumer"})

	// scoring.
	DaysScored = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_days_scored_total",
		Help: "Days scored, by result (ok, error).",
	}, []string{"result"})
	ScoringDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ryft_scoring_duration_seconds",
		Help:    "Time to score one day (load, compute, write), by result.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"result"})

	SleepBaseLearned = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_sleep_base_learned_total",
		Help: "Weekly sleep-baseline estimates, by outcome (applied: the baseline in use moved and the last 7 days were rescored; kept: the same value, or set by hand; insufficient: too few qualifying nights; error).",
	}, []string{"outcome"})

	// Train (api).
	TrainSessionsCompleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_train_sessions_completed_total",
		Help: "Strength sessions completed (POST /v1/sessions/{id}/complete, repeats included).",
	})
	TrainSetsLogged = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_train_sets_logged_total",
		Help: "Completed sets that PUT /v1/sessions/{id} stored for the first time.",
	})
	TrainPRs = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_train_prs_total",
		Help: "Personal records set by a completed session, by kind (e1rm, max_weight, max_reps, max_volume).",
	}, []string{"kind"})
)

// Handler serves the default registry (these metrics plus go_* and process_*).
func Handler() http.Handler { return promhttp.Handler() }

// Result maps an error to the result label.
func Result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
