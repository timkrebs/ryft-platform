// Package metrics declares every Prometheus metric the three services export
// and serves them on /metrics. All service-specific metrics are vectors: a
// vector only appears in a service's output once that service records a label
// set, so ingest shows ingest metrics, scoring shows scoring metrics, and all
// three show the HTTP, Go runtime and process metrics. docs/OBSERVABILITY.md
// lists them with the dashboard queries.
package metrics

import (
	"net/http"
	"sync"

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
	TextRefused = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_text_refused_total",
		Help: "Requests refused for text Postgres cannot store (a NUL, or bytes that are not UTF-8), by route pattern " +
			"and where it was found: body (422), url (400) or database (422; a path that bypassed the first two).",
	}, []string{"route", "at"})

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

	// Events (RabbitMQ): ingest, api and scoring publish, scoring and notify consume.
	EventsPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_published_total",
		Help: "Events sent to the broker, by reason (sync, recompute, session, session_completed, scored, session_scored, refresh) " +
			"and result (ok, error, unroutable: no queue bound for the key).",
	}, []string{"reason", "result"})
	EventsHandled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_handled_total",
		Help: "Events consumed, by reason and result: ok, retry (to the next retry tier), failed (the last attempt failed, " +
			"dead-lettered), bad (unparseable, dead-lettered), dropped (cannot succeed, e.g. the user was deleted), deferred (notify: quiet hours).",
	}, []string{"reason", "result"})
	// The name is from NATS, kept so the dashboards and history stay (D102).
	Redeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_nats_redeliveries_total",
		Help: "Messages received again, per queue: redelivered by the broker, or a retry tier's copy.",
	}, []string{"consumer"})

	// scoring.
	DaysScored = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_days_scored_total",
		Help: "Days scored, by result (ok, error, no_consent: skipped, the user withdrew consent).",
	}, []string{"result"})
	ScoringDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ryft_scoring_duration_seconds",
		Help:    "Time to score one day (load, compute, write), by result.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	}, []string{"result"})

	DaysRescored = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_days_rescored_total",
		Help: "Stored days scored again beyond the days asked for, because their history changed, by reason " +
			"(history: an earlier day's baseline inputs changed in the same pass, or a retried event; behind: the 15-minute " +
			"pass found a day whose baseline_days or baselines fall short of what its stored history now holds).",
	}, []string{"reason"})

	SleepBaseLearned = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_sleep_base_learned_total",
		Help: "Weekly sleep-baseline estimates, by outcome (applied: the baseline in use moved and the last 7 days were rescored; kept: the same value, or set by hand; insufficient: too few qualifying nights; error).",
	}, []string{"outcome"})

	// Push (notify).
	PushNotifications = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_push_notifications_total",
		Help: "Notifications notify decided on, by category (MORNING_SCORE, SESSION_SCORED, PR, SYNC_STALE, REFRESH = the silent push) and result: sent, failed, muted (category off), quiet, deferred (until quiet hours end), duplicate (already sent), stale, no_token, unconfigured (no APNs key).",
	}, []string{"category", "result"})
	APNsResponses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_apns_responses_total",
		Help: "APNs answers per device request, by push type (alert, background) and HTTP status (or error: no answer).",
	}, []string{"push_type", "status"})
	PushTokensPruned = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_push_tokens_pruned_total",
		Help: "Device tokens dropped because APNs said they are gone (410, BadDeviceToken, DeviceTokenNotForTopic).",
	})

	// Beta (api, ingest).
	GateRefusals = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_gate_refusals_total",
		Help: "Requests a gate would refuse: invite_required (beta gate) or consent_required (sync without consent); mode on = refused with 403, warn = served with X-RYFT-Gate.",
	}, []string{"code", "mode", "service"})
	Consents = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_consents_total",
		Help: "Consent changes: granted, revoked, stale (POST with an outdated policy version).",
	}, []string{"action"})
	InviteRedemptions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_invite_redemptions_total",
		Help: "POST /v1/me/invite: ok (a use spent), already (the account had access), invalid.",
	}, []string{"result"})
	DBUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_db_up",
		Help: "1 when the service's last database ping (every 30 s) succeeded, 0 when it failed.",
	}, []string{"service"})
	DBPingFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_db_ping_failures_total",
		Help: "Failed database pings (every 30 s, 5 s timeout): Supabase or its pooler unreachable, or the role refused.",
	}, []string{"service"})
	UsersByLastSync = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_users_by_last_sync",
		Help: "Accounts by the age of their newest device sync (scoring, every 15 min): under_6h, 6h_to_24h, 1d_to_3d, over_3d, never.",
	}, []string{"age"})
	SyncToScore = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ryft_sync_to_score_seconds",
		Help:    "From ingest accepting a sync to scoring finishing one of the days it queued (per day; D92).",
		Buckets: []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800},
	})
	ConsumerPanics = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_event_handler_panics_total",
		Help: "Event handlers that panicked (the message is retried and, at the last delivery, dead-lettered), by subject.",
	}, []string{"subject"})
	EventsDLQ = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_dlq_total",
		Help: "Events dead-lettered (dlq.scoring, dlq.notify), by routing key: ok, or error (the publish failed; " +
			"the message was rejected and the broker's delivery limit dead-letters it).",
	}, []string{"subject", "result"})
	FeedbackReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_feedback_received_total",
		Help: "Reports stored by POST /v1/feedback, by category (bug, idea, data, other).",
	}, []string{"category"})

	// Journal (api).
	JournalSaves = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_journal_saves_total",
		Help: "Journal days saved (PUT /v1/journal/{day}).",
	})
	BehaviorImpactSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ryft_behavior_impact_seconds",
		Help:    "CPU time of the behavior-impact computation per request (GET /v1/behaviors/impact, the insight), by caller; above ~0.1 s at p95 it belongs in a worker (D76).",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5},
	}, []string{"caller"})

	// Remote config (api).
	FlagEvaluations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_flag_evaluations_total",
		Help: "Feature flags evaluated for GET /v1/config, by key and result (on, off).",
	}, []string{"key", "result"})
	AdminRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_admin_requests_total",
		Help: "Requests to /v1/admin, by result (ok, unauthorized, invalid, error).",
	}, []string{"result"})

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

// The transactional outbox (D101), per publishing service.
var (
	OutboxPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_outbox_pending",
		Help: "Events written to the outbox and not yet confirmed by the broker (updated every 15 s by the relay).",
	}, []string{"publisher"})
	OutboxOldest = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_outbox_oldest_seconds",
		Help: "Age of the oldest event waiting in the outbox (0 when none waits).",
	}, []string{"publisher"})
	OutboxStuck = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_outbox_stuck",
		Help: "Outbox events set aside after 50 failed sends (an event no queue takes); they need a person.",
	}, []string{"publisher"})
	OutboxRelayed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_outbox_relayed_total",
		Help: "Outbox events the relay sent, by result: ok (confirmed), error (retried).",
	}, []string{"publisher", "result"})
)

// The RabbitMQ connection's metrics, registered by UseAMQP when a service connects: a
// binary that never connects (migrate) exposes none, so a 0 is never a false alarm.
var (
	AMQPConnected = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "ryft_amqp_connected",
		Help: "1 while the service's connection to RabbitMQ is up, 0 while it reconnects.",
	})
	AMQPReconnects = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ryft_amqp_reconnects_total",
		Help: "Connections to RabbitMQ that closed and were dialled again.",
	})
	amqpOnce sync.Once
)

// UseAMQP registers the RabbitMQ connection metrics (once).
func UseAMQP() {
	amqpOnce.Do(func() { prometheus.MustRegister(AMQPConnected, AMQPReconnects) })
}
