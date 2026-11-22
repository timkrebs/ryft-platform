package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// The service metrics. Each metric's Help string says what it counts and what its
// labels mean (the Prometheus name is the contract with the dashboards and alerts,
// so a rename is a dashboard change); a comment below adds only what Help cannot.
var (
	// HTTPRequests counts HTTP answers; only httpx.Instrument records it, so only api
	// and ingest emit it. route is the ServeMux pattern ("GET /v1/days/{day}"), never
	// the raw path, so cardinality stays bounded (D14).
	HTTPRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_requests_total",
		Help: "HTTP requests by route pattern and status code.",
	}, []string{"route", "code"})
	HTTPDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ryft_http_request_duration_seconds",
		Help:    "HTTP request latency by route pattern.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"route"})
	// TextRefused counts requests refused for text Postgres cannot store, by where it
	// was found: body, url or database. A database hit means a path bypassed the first
	// two checks, so it should stay zero.
	TextRefused = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_text_refused_total",
		Help: "Requests refused for text Postgres cannot store (a NUL, or bytes that are not UTF-8), by route pattern " +
			"and where it was found: body (422), url (400) or database (422; a path that bypassed the first two).",
	}, []string{"route", "at"})
	NULStripped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_nul_stripped_total",
		Help: "Requests accepted after the NUL characters were removed from their body's strings (POST /v1/sync), by route pattern.",
	}, []string{"route"})

	Ingested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_samples_ingested_total",
		Help: "Records accepted by POST /v1/sync. kind=sample (type = sample type), sleep (type = stage) or workout.",
	}, []string{"kind", "type"})
	SyncDeleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_sync_deleted_total",
		Help: "Rows removed by the deletions of POST /v1/sync, by kind (sample, sleep, workout).",
	}, []string{"kind"})
	SyncIgnored = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_sync_ignored_total",
		Help: "Records and deletions of POST /v1/sync not applied, by kind (sample, sleep, workout) and reason " +
			"(duplicate, outside_window, unknown_type, unknown_id).",
	}, []string{"kind", "reason"})
	WorkoutsFolded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_workouts_folded_total",
		Help: "Duplicate workouts handled, by reason: same_source (a copy removed, ingest sync or fold-workouts) " +
			"or cross_source (a workout newly marked as another source's duplicate by scoring).",
	}, []string{"reason"})
	WorkoutsFoldSkipped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_workouts_fold_skipped_total",
		Help: "Syncs whose same-source workout fold was skipped, by reason (too_many: more workouts in scope than the fold's bound).",
	}, []string{"reason"})
	LastSync = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_last_sync_timestamp_seconds",
		Help: "Unix time of the last successful sync (any user; per-user detail would be unbounded).",
	}, []string{})

	// Panics counts handler panics that Recover turned into a 500. Any increase is a
	// bug to fix; the response body stays generic (D37).
	Panics = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_http_panics_total",
		Help: "Handler panics recovered (answered 500; a tick step's: the tick goes on and answers 204), by route pattern.",
	}, []string{"route"})

	// GoroutinePanics counts panics that internal/app recovered in a service's own
	// goroutines (the HTTP and /metrics servers; consumers, timers and the relay are
	// push routes, tick steps and inline flushes now, D219). The service then shuts
	// down in order and exits 2, as an unrecovered panic does. A panic in a /readyz
	// check is counted too, and the service keeps running. Any increase is a bug to
	// fix. It has no label: the log line "panic" names the task or the check, and a
	// handler's or a tick step's panic is counted by Panics or ConsumerPanics instead.
	GoroutinePanics = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_goroutine_panics_total",
		Help: "Panics recovered in a service's own goroutines (its servers); the service then shuts down and exits 2 (a /readyz check's panic does not stop it).",
	})

	// RateLimited counts requests answered 429 by the per-instance, per-user token bucket
	// (ingest and api, D36), by the per-address limit at the edge of both, before any token
	// (ip, D53), and by the one of /v1/admin (admin_ip). It is the edge limit's only trace
	// besides the access line's code: the limit itself logs nothing (D222).
	RateLimited = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_rate_limited_total",
		Help: "Requests answered 429 by the per-user limit, by class (sync, default, …), by the per-address limit at the edge of api and ingest (ip), and by the per-address limit of /v1/admin (admin_ip).",
	}, []string{"class"})
	// AuthFailures also counts the push and tick tokens refused on /push/* and /internal/*
	// (Google OIDC, D218) with the same reasons: subject there is a token of another
	// account than the push service account (its sub, email or email_verified), and
	// jwks_unavailable means Google's signing keys cannot be loaded.
	AuthFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_auth_failures_total",
		Help: "Rejected /v1 requests and push/tick tokens by reason: missing_token, malformed, expired, signature, issuer, audience, " +
			"unknown_key, key_mismatch, invalid, role, anonymous, subject, api_key, deleted, … (401) and jwks_unavailable, " +
			"jwks_refresh_limited, internal (503).",
	}, []string{"reason"})
	AccountDeletions = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_account_deletions_total",
		Help: "Account deletion steps: step=data (our rows), apple (revoking the Sign in with Apple token) or auth (the Supabase auth user); result=ok, error or skipped (not configured).",
	}, []string{"step", "result"})

	// EventsPublished counts publishes to Pub/Sub (ingest, api, scoring, and notify's
	// deferrals into ryft-notify-wait). An error from the relay is retried from the
	// outbox row; a key without a topic is an error too, a rejection, and the outbox sets
	// its row aside at once. A sync and a session completion write their row with outbox.Insert
	// in their own transaction, so a failure there rolls the request back. An error from
	// outbox.Send means its INSERT failed, so there is no row, and the caller decides:
	// recompute and session edits answer 503, while merge and scoring's three publishes
	// only log it and that event is lost (D101).
	EventsPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_published_total",
		Help: "Events published to Pub/Sub, by reason (sync, recompute, session, session_completed, scored, session_scored, refresh) " +
			"and result (ok, error: refused, unreachable, or a key without a topic).",
	}, []string{"reason", "result"})
	EventsHandled = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_handled_total",
		Help: "Pushed events handled, by reason and result: ok, retry (nacked, Pub/Sub delivers it again), failed (nacked on its " +
			"last delivery attempt: Pub/Sub dead-letters it), bad (unparseable or the wrong key, dead-lettered by the consumer), " +
			"dropped (cannot succeed, e.g. the user was deleted), deferred (notify: quiet hours, held in the outbox).",
	}, []string{"reason", "result"})
	// Redeliveries counts messages a consumer received again. The metric name is from
	// NATS, kept so the dashboards and history stay (D102).
	Redeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_nats_redeliveries_total",
		Help: "Messages received again, per consumer: a Pub/Sub delivery attempt above 1, or a dead letter replayed by hand (ryft-replayed).",
	}, []string{"consumer"})

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

	// PushNotifications counts what notify decided per category, sent or not (notify).
	PushNotifications = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_push_notifications_total",
		Help: "Notifications notify decided on, by category (MORNING_SCORE, SESSION_SCORED, PR, SYNC_STALE, ACTIVITY_SCORED, REFRESH = the silent push) and result: sent, failed, muted (category off), quiet, deferred (until quiet hours end), duplicate (already sent or decided), throttled, stale, import (an imported session, never announced), no_token, unconfigured (no APNs key); ACTIVITY_SCORED only: capped (the 4th and later of a local day), backfill (first stored by a new device's sync), inherited (a copy of a workout that was decided).",
	}, []string{"category", "result"})
	APNsResponses = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_apns_responses_total",
		Help: "APNs answers per device request, by push type (alert, background) and HTTP status (or error: no answer).",
	}, []string{"push_type", "status"})
	PushTokensPruned = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_push_tokens_pruned_total",
		Help: "Device tokens dropped because APNs said they are gone (410, BadDeviceToken, DeviceTokenNotForTopic).",
	})

	// GateRefusals counts the requests a gate refuses or, in warn mode, would refuse
	// (api, ingest, scoring); warn mode is how a gate is rehearsed before it is on (D87).
	GateRefusals = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_gate_refusals_total",
		Help: "Requests a gate would refuse: invite_required (beta gate) or consent_required (a sync or a training import without consent; in scoring a day); mode on = refused with 403, warn = served with X-RYFT-Gate.",
	}, []string{"code", "mode", "service"})
	// AppAttestRefusals counts the requests without a valid app token under APP_ATTEST warn
	// (served) and on (refused): one per warn line "app attest" (D245, D247). Never the build,
	// the route or an account as a label.
	AppAttestRefusals = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_app_attest_refusals_total",
		Help: "Requests without a valid app token (X-RYFT-App-Token) under APP_ATTEST warn (served) or on (403 app_attest_required), " +
			"by reason (missing, invalid, expired, other_account) and service (api, ingest).",
	}, []string{"reason", "service"})
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
		Help: "1 when the service's last database ping succeeded, 0 when it failed; set by each tick.",
	}, []string{"service"})
	DBPingFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_db_ping_failures_total",
		Help: "Failed database pings, set by each tick: Supabase or its pooler unreachable, or the role refused.",
	}, []string{"service"})
	// Ticks counts the POST /internal/tick a service answered (D219): one per tick
	// after authentication, whatever its steps did. TickMissing reads it, so it is
	// counted before the tick's metrics push.
	Ticks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_ticks_total",
		Help: "Ticks handled (POST /internal/tick), by service and result: ok (every step succeeded), error (a step failed or " +
			"the budget ran out), overlap (a tick was still running in this instance).",
	}, []string{"service", "result"})
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
		Help: "Event handlers that panicked (the push is nacked, delivered again and, after the last attempt, dead-lettered by Pub/Sub), by event key.",
	}, []string{"subject"})
	EventsDLQ = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_events_dlq_total",
		Help: "Bad events a consumer dead-lettered itself (ryft-dlq-scoring, ryft-dlq-notify), by event key (unknown: no valid key): ok, " +
			"or error (the publish failed; the push was nacked and Pub/Sub dead-letters it after its attempts).",
	}, []string{"subject", "result"})
	FeedbackReceived = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_feedback_received_total",
		Help: "Reports stored by POST /v1/feedback, by category (bug, idea, data, other).",
	}, []string{"category"})

	JournalSaves = promauto.NewCounter(prometheus.CounterOpts{
		Name: "ryft_journal_saves_total",
		Help: "Journal days saved (PUT /v1/journal/{day}).",
	})
	BehaviorImpactSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ryft_behavior_impact_seconds",
		Help:    "CPU time of the behavior-impact computation per request (GET /v1/behaviors/impact, the insight), by caller; above ~0.1 s at p95 it belongs in a worker (D76).",
		Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5},
	}, []string{"caller"})

	// FlagEvaluations counts feature flags evaluated for GET /v1/config (api). The key
	// label is a flag key from the flags table, a bounded set.
	FlagEvaluations = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_flag_evaluations_total",
		Help: "Feature flags evaluated for GET /v1/config, by key and result (on, off).",
	}, []string{"key", "result"})
	AdminRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_admin_requests_total",
		Help: "Requests to /v1/admin, by result (ok, invalid, error, unauthorized, mfa_required, forbidden). " +
			"unauthorized: no bearer, or one that is neither the key nor an allow-listed admin's token; mfa_required: a real admin " +
			"without a second factor of the last 12 hours; forbidden: a token on a key-only route.",
	}, []string{"result"})
	// AdminAuthorized counts the /v1/admin requests that passed the bearer check, by who: the key, or
	// an admin's Supabase token (the operator's page). No label holds an id.
	AdminAuthorized = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_admin_authorized_total",
		Help: "Requests to /v1/admin that passed the bearer check, by actor (key, user).",
	}, []string{"actor"})

	// InviteEmailPurges counts the passes of the job that clears the emails of codes that can no longer be
	// redeemed (D161): ok (also a pass that cleared nothing) or error. A failing job keeps addresses of
	// non-users longer than the privacy text says, so a run of errors is an alert.
	InviteEmailPurges = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_invite_email_purge_total",
		Help: "Passes of the job that clears bound invite emails 30 days after the code stopped being redeemable: ok, error.",
	}, []string{"result"})
	// InviteBindingAvailable is 1 while email-bound invites can be created and redeemed: the Supabase project
	// confirms emails (mailer_autoconfirm false, read from its public settings) and the api can ask Supabase
	// for an account's email. 0 while it is true, unknown or the api has no admin client (D159).
	InviteBindingAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ryft_invite_binding_available",
		Help: "1 while email-bound invites work (Supabase confirms emails and the admin key is set), else 0.",
	})
	// InviteSettingsChecks counts the reads of the project's public auth settings: ok (Confirm email on),
	// autoconfirm (it is off: bound codes refuse) and error (unknown: bound codes refuse).
	InviteSettingsChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_invite_settings_checks_total",
		Help: "Reads of the Supabase auth settings for email-bound invites: ok, autoconfirm, error.",
	}, []string{"result"})

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
	// TrainImports counts POST /v1/train/import requests that passed authentication
	// (api, D124), by how they ended.
	TrainImports = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_train_imports_total",
		Help: "POST /v1/train/import requests past authentication, by result: ok (200, also when every item was rejected), " +
			"invalid (400/413/422 or 403 consent_required: nothing processed), disabled (403 import_not_enabled: the flag " +
			"train.import is off for the account), busy (429 import_in_progress), " +
			"timeout (503 after the 50 s budget), error (503: the database failed; what was stored stays).",
	}, []string{"result"})
	TrainImportItems = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_train_import_items_total",
		Help: "Items of the training import, by kind (plan, session) and status (created, unchanged, updated, rejected).",
	}, []string{"kind", "status"})
	TrainImportSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "ryft_train_import_seconds",
		Help:    "Duration of a POST /v1/train/import that took the lease: the items, the records and the queueing (its budget is 50 s).",
		Buckets: []float64{.1, .25, .5, 1, 2, 5, 10, 20, 30, 50},
	})
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
	// OutboxPending, OutboxOldest and OutboxStuck are set by the tick's outbox Sweep
	// (D219), so between two ticks they show the last sweep's numbers.
	OutboxPending = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_outbox_pending",
		Help: "Due outbox events not yet accepted by Pub/Sub; set by the tick's sweep.",
	}, []string{"publisher"})
	OutboxOldest = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_outbox_oldest_seconds",
		Help: "Seconds the oldest due event has waited since it became due; 0 when none.",
	}, []string{"publisher"})
	OutboxStuck = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ryft_outbox_stuck",
		Help: "Outbox events set aside: after 50 failed sweeps, or at once when the transport rejects them for good (a key no topic takes, a body Pub/Sub refuses); they need a person.",
	}, []string{"publisher"})
	OutboxRelayed = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ryft_outbox_relayed_total",
		Help: "Outbox events sent by flush or sweep, by result: ok, error (refused: a sweep's counts an attempt, a rejection sets the row aside).",
	}, []string{"publisher", "result"})
)
