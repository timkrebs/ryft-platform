// ingest accepts HealthKit batches from the iOS app, stores them idempotently
// and tells scoring which local days changed.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
	"github.com/timkrebs/ryft/internal/store"
)

func main() {
	log := httpx.Logger("ingest")
	cfg, err := config.Load("ingest")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	authCfg, err := config.LoadAuth()
	if err != nil {
		log.Error("auth config", "err", err)
		os.Exit(1)
	}
	// Before any connection: without the policy version ingest cannot check consent
	// (D78), so it exits at once instead of behind the DB retries.
	beta, err := config.LoadBeta()
	if err == nil {
		err = beta.RequirePolicy()
	}
	if err != nil {
		log.Error("beta config", "err", err)
		os.Exit(1)
	}
	log.Info("beta", "gate", beta.Gate, "consent", beta.Consent, "privacy_policy_version", beta.PrivacyPolicyVersion)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Retries until the role can log in and the migrate job has applied the
	// schema; this service's role cannot change it.
	pool, err := db.Open(ctx, cfg.DatabaseURL, cfg.DBMaxConns, log)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	go db.Watch(ctx, pool, "ingest", log)
	st := store.New(pool)

	// RabbitMQ (D97), retried at start-up.
	bus, err := eventbus.Open(ctx, cfg, "ingest", log)
	if err != nil {
		log.Error("events", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	verifier, err := auth.FromConfig(ctx, authCfg)
	if err != nil {
		log.Error("auth", "err", err)
		os.Exit(1)
	}
	log.Info("auth", "mode", authCfg.Mode)

	// The outbox (D101): a sync's days and the event announcing them commit together;
	// the relay sends it to the broker.
	ob := outbox.New(pool, "ingest", bus, log)
	go ob.Run(ctx)

	h := &handler{st: st, bus: bus, outbox: ob, log: log, beta: beta}

	mux := http.NewServeMux()
	httpx.ServeMetrics(log, ":"+cfg.MetricsPort)
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		// Not the broker: a sync's event waits in the outbox while it is away (D101),
		// so ingest keeps taking syncs, like the api (AMQPDisconnected, OutboxBacklog).
		return verifier.Ready(ctx)
	})
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/sync", h.sync)
	api.HandleFunc("/v1/", httpx.NotFound)
	limits := ratelimit.NewUserLimits()
	mux.Handle("/v1/", auth.Middleware(auth.Options{
		Log:      log,
		Verifier: verifier,
		Limit: func(r *http.Request, p auth.Principal) (string, bool, time.Duration) {
			return limits.Allow(r, p.UserID.String())
		},
		OnAuth: auth.LoadUser(func(ctx context.Context, p auth.Principal) (model.User, error) {
			u, err := st.EnsureUser(ctx, store.Identity{ID: p.UserID, Email: p.Email, Provider: p.Provider, Name: p.Name})
			if errors.Is(err, store.ErrDeleted) { // a deleted account's token is a bad token
				return u, auth.Unauthorized("deleted", err)
			}
			return u, err
		}),
	}, httpx.Timeout(syncTimeout, httpx.WithRoutes(api))))
	mux.HandleFunc("/", httpx.NotFound)

	if err := httpx.Serve(log, ":"+cfg.Port, httpx.Instrument(log, httpx.Recover(httpx.SecurityHeaders(mux)))); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// syncTimeout bounds one upload's database work; it stays inside the server's
// 60 s write timeout so a slow upload gets a JSON 503, not a cut connection.
const syncTimeout = 50 * time.Second

// requestID is the ID Instrument gave this request; scoring logs it too.
func requestID(r *http.Request) string {
	if f := httpx.Info(r.Context()); f != nil {
		return f.RequestID
	}
	return ""
}

type handler struct {
	st     *store.Store
	bus    events.Bus
	outbox *outbox.Outbox
	log    *slog.Logger
	beta   config.Beta

	expMu  sync.Mutex // the cached sync-to-score estimate
	expSec int
	expAt  time.Time
}

// admitted says whether user may upload: beta access (BETA_GATE) and a consent
// to the current privacy policy (CONSENT_ENFORCE; GDPR Art. 9(2)(a), D78). A
// gate in warn mode lets the sync through with the code in X-RYFT-Gate (D87).
// The user row, consent included, is read fresh for every request, so a
// withdrawal counts at once.
func (h *handler) admitted(w http.ResponseWriter, r *http.Request, user model.User) bool {
	check := func(mode config.Mode, failed bool, code, msg string) bool {
		if mode == config.ModeOff || !failed {
			return true
		}
		metrics.GateRefusals.WithLabelValues(code, string(mode), "ingest").Inc()
		if mode == config.ModeWarn {
			w.Header().Add(httpx.GateHeader, code)
			return true
		}
		httpx.WriteCodedError(w, r, http.StatusForbidden, code, msg)
		return false
	}
	return check(h.beta.Gate, !user.BetaAccess, model.CodeInviteRequired, "this beta needs an invite code") &&
		check(h.beta.Consent, user.Consent == nil || user.Consent.PolicyVersion != h.beta.PrivacyPolicyVersion,
			model.CodeConsentRequired,
			"consent to the privacy policy "+h.beta.PrivacyPolicyVersion+" is required before uploading health data")
}

func (h *handler) sync(w http.ResponseWriter, r *http.Request) {
	// Before reading the body: a refused upload costs no 32 MiB parse.
	if user, _ := auth.UserFrom(r.Context()); !h.admitted(w, r, user) {
		return
	}
	var req model.SyncRequest
	// NULs removed, not refused: the app does not retry a 422, and its strings come
	// from HealthKit and the device, so one NUL would stall that stream for good.
	if err := httpx.ReadJSONStripNUL(w, r, &req, httpx.MaxBodySync); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if err := validate(&req); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ignored := screen(&req, time.Now())
	sent := model.SyncCounts{Samples: len(req.Samples), Sleep: len(req.Sleep), Workouts: len(req.Workouts)}
	ctx := r.Context()
	user, _ := auth.UserFrom(ctx)
	userID := user.ID
	loc, err := time.LoadLocation(user.TZ)
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("user tz: %w", err))
		return
	}
	if err := h.st.TouchDevice(ctx, userID, req.DeviceID, req.DeviceName, req.AppVersion, req.OSVersion); err != nil {
		httpx.Fail(w, r, fmt.Errorf("device: %w", err))
		return
	}

	ns, err := h.st.UpsertSamples(ctx, userID, req.Samples)
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("samples: %w", err))
		return
	}
	nsl, err := h.st.UpsertSleep(ctx, userID, req.Sleep)
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("sleep: %w", err))
		return
	}
	nw, err := h.st.UpsertWorkouts(ctx, userID, req.Workouts)
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("workouts: %w", err))
		return
	}
	// User.sources only: the data is stored, so a failure here does not fail the sync.
	if err := h.st.UpsertSources(ctx, userID, store.SourcesOf(req.Samples, req.Sleep, req.Workouts)); err != nil {
		h.log.ErrorContext(ctx, "sources", "err", err)
	}

	days := touchedDays(&req, loc)
	ignored.Samples.Duplicate, ignored.Sleep.Duplicate, ignored.Workouts.Duplicate = sent.Samples-ns, sent.Sleep-nsl, sent.Workouts-nw
	summary := map[string]any{"samples": ns, "sleep": nsl, "workouts": nw, "days": len(days), "app_version": req.AppVersion,
		"ignored": ignored}
	if err := h.st.RecordSync(ctx, userID, req.DeviceID, summary); err != nil {
		h.log.ErrorContext(ctx, "record sync", "err", err)
	}
	// The days for GET /v1/me/sync and the event for scoring, in one transaction
	// (D101). A failure does not fail the sync: the data is stored, and scoring's
	// periodic pass catches up.
	if len(days) > 0 {
		m, err := events.Ingested(model.IngestedEvent{UserID: userID, Days: days, Reason: "sync", RequestID: requestID(r)}, "")
		if err == nil {
			err = h.st.QueueDaysWith(ctx, userID, days, func(tx pgx.Tx) error { return outbox.Insert(ctx, tx, "ingest", m) })
		}
		if err != nil {
			h.log.ErrorContext(ctx, "queue days and their event", "err", err)
			metrics.EventsPublished.WithLabelValues("sync", "error").Inc()
		} else {
			h.outbox.Kick()
		}
	}
	countIngested(&req)
	h.log.InfoContext(ctx, "sync", "device", req.DeviceID, "samples", ns, "sleep", nsl, "workouts", nw, "days", len(days))
	httpx.WriteJSON(w, http.StatusOK, model.SyncResponse{Samples: ns, Sleep: nsl, Workouts: nw, Days: days,
		Accepted: model.SyncCounts{Samples: ns, Sleep: nsl, Workouts: nw}, Ignored: ignored,
		Scoring: model.SyncScoring{QueuedDays: days, ExpectedWithinSec: h.expectedSec(ctx)}})
}

// The window a record must fall in (SyncResponse → ignored.outside_window): a
// day of clock skew ahead, and as far back as a score reaches.
const (
	futureSlack = 24 * time.Hour
	pastWindow  = 400 * 24 * time.Hour
)

// screen drops the records the server does not store and counts them: unknown
// sample types and sleep stages (an app reading a new HealthKit type keeps
// syncing instead of failing with 422), and records outside the window.
func screen(req *model.SyncRequest, now time.Time) model.SyncIgnored {
	var ig model.SyncIgnored
	outside := func(start, end time.Time) bool {
		return start.After(now.Add(futureSlack)) || end.Before(now.Add(-pastWindow))
	}
	samples := req.Samples[:0]
	for _, s := range req.Samples {
		switch {
		case !model.KnownSampleTypes[s.Type]:
			ig.Samples.UnknownType++
		case outside(s.Start, s.End):
			ig.Samples.OutsideWindow++
		default:
			samples = append(samples, s)
		}
	}
	req.Samples = samples
	sleep := req.Sleep[:0]
	for _, s := range req.Sleep {
		switch {
		case !model.KnownSleepStages[s.Stage]:
			ig.Sleep.UnknownType++
		case outside(s.Start, s.End):
			ig.Sleep.OutsideWindow++
		default:
			sleep = append(sleep, s)
		}
	}
	req.Sleep = sleep
	workouts := req.Workouts[:0]
	for _, wk := range req.Workouts {
		if outside(wk.Start, wk.End) {
			ig.Workouts.OutsideWindow++
			continue
		}
		workouts = append(workouts, wk)
	}
	req.Workouts = workouts
	return ig
}

// expectedEvery is how long ingest reuses the sync-to-score estimate.
const expectedEvery = time.Minute

// expectedSec is scoring's recent sync-to-score p95, refreshed once a minute.
func (h *handler) expectedSec(ctx context.Context) int {
	h.expMu.Lock()
	defer h.expMu.Unlock()
	if time.Since(h.expAt) < expectedEvery && h.expSec > 0 {
		return h.expSec
	}
	sec, err := h.st.ExpectedScoreSec(ctx)
	if err != nil {
		h.log.WarnContext(ctx, "expected score time", "err", err)
	}
	h.expSec, h.expAt = sec, time.Now()
	return sec
}

// Limits of the free-text fields (migration 0020).
const (
	maxVersionLen  = 64
	maxBundleIDLen = 255
)

func tooLong(s *string, n int) bool { return s != nil && len(*s) > n }

func validate(req *model.SyncRequest) error {
	if req.DeviceID == uuid.Nil {
		return fmt.Errorf("device_id is required")
	}
	if tooLong(req.AppVersion, maxVersionLen) || tooLong(req.OSVersion, maxVersionLen) {
		return fmt.Errorf("app_version and os_version: at most %d characters", maxVersionLen)
	}
	for i := range req.Samples {
		s := &req.Samples[i]
		if s.Start.IsZero() {
			return fmt.Errorf("samples[%d]: start is required", i)
		}
		if s.End.Before(s.Start) {
			return fmt.Errorf("samples[%d]: end before start", i)
		}
		if tooLong(s.SourceBundleID, maxBundleIDLen) {
			return fmt.Errorf("samples[%d]: source_bundle_id too long", i)
		}
	}
	for i := range req.Sleep {
		s := &req.Sleep[i]
		if !s.End.After(s.Start) {
			return fmt.Errorf("sleep[%d]: end must be after start", i)
		}
		if tooLong(s.SourceBundleID, maxBundleIDLen) {
			return fmt.Errorf("sleep[%d]: source_bundle_id too long", i)
		}
	}
	for i := range req.Workouts {
		wk := &req.Workouts[i]
		if wk.ID == uuid.Nil {
			return fmt.Errorf("workouts[%d]: id is required", i)
		}
		if !wk.End.After(wk.Start) {
			return fmt.Errorf("workouts[%d]: end must be after start", i)
		}
		if tooLong(wk.SourceBundleID, maxBundleIDLen) {
			return fmt.Errorf("workouts[%d]: source_bundle_id too long", i)
		}
		if wk.Activity == "" {
			wk.Activity = "other"
		}
	}
	return nil
}

// touchedDays lists every local day whose score could change. A sleep segment
// affects the day it ends on; a night-time HRV sample affects the next day,
// so we conservatively add both the sample's own day and the day after.
func touchedDays(req *model.SyncRequest, loc *time.Location) []string {
	set := map[string]struct{}{}
	add := func(t time.Time) { set[t.In(loc).Format("2006-01-02")] = struct{}{} }
	for _, s := range req.Samples {
		add(s.Start)
		if s.Type == model.SampleHRVSDNN || s.Type == model.SampleRespiratoryRate || s.Type == model.SampleSpO2 {
			add(s.Start.Add(24 * time.Hour))
		}
	}
	for _, s := range req.Sleep {
		add(s.End)
	}
	for _, w := range req.Workouts {
		add(w.Start)
		add(w.Start.Add(24 * time.Hour)) // tomorrow's sleep need depends on today's strain
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// countIngested records a successful sync's records by kind and type (both
// closed vocabularies after validate, so label cardinality is bounded).
func countIngested(req *model.SyncRequest) {
	samples, stages := map[string]int{}, map[string]int{}
	for _, s := range req.Samples {
		samples[s.Type]++
	}
	for _, s := range req.Sleep {
		stages[s.Stage]++
	}
	for typ, n := range samples {
		metrics.Ingested.WithLabelValues("sample", typ).Add(float64(n))
	}
	for stage, n := range stages {
		metrics.Ingested.WithLabelValues("sleep", stage).Add(float64(n))
	}
	if len(req.Workouts) > 0 {
		metrics.Ingested.WithLabelValues("workout", "workout").Add(float64(len(req.Workouts)))
	}
	metrics.LastSync.WithLabelValues().SetToCurrentTime()
}
