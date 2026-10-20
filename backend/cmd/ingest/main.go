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
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
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
	st := store.New(pool)

	bus, err := events.Open(ctx, cfg.NatsURL, events.Options{Name: "ingest", User: cfg.NatsUser, Password: cfg.NatsPass}, log)
	if err != nil {
		log.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	verifier, err := auth.FromConfig(ctx, authCfg)
	if err != nil {
		log.Error("auth", "err", err)
		os.Exit(1)
	}
	log.Info("auth", "mode", authCfg.Mode)
	beta, err := config.LoadBeta()
	if err != nil {
		log.Error("beta config", "err", err)
		os.Exit(1)
	}
	log.Info("beta", "gate", beta.Gate, "privacy_policy_version", beta.PrivacyPolicyVersion)

	h := &handler{st: st, bus: bus, log: log, beta: beta}

	mux := http.NewServeMux()
	httpx.ServeMetrics(log, ":"+cfg.MetricsPort)
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		if err := bus.Ready(ctx); err != nil {
			return err
		}
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
	st   *store.Store
	bus  *events.Bus
	log  *slog.Logger
	beta config.Beta
}

// admitted says whether user may upload: beta access while the gate is on, and
// a consent to the current privacy policy always (GDPR Art. 9(2)(a), D78). The
// user row, consent included, is read fresh for every request, so a withdrawal
// counts at once.
func (h *handler) admitted(w http.ResponseWriter, user model.User) bool {
	if h.beta.Gate && !user.BetaAccess {
		metrics.BetaDenied.WithLabelValues(model.CodeInviteRequired, "ingest").Inc()
		httpx.WriteCodedError(w, http.StatusForbidden, model.CodeInviteRequired, "this beta needs an invite code")
		return false
	}
	if user.Consent == nil || user.Consent.PolicyVersion != h.beta.PrivacyPolicyVersion {
		metrics.BetaDenied.WithLabelValues(model.CodeConsentRequired, "ingest").Inc()
		httpx.WriteCodedError(w, http.StatusForbidden, model.CodeConsentRequired,
			"consent to the privacy policy "+h.beta.PrivacyPolicyVersion+" is required before uploading health data")
		return false
	}
	return true
}

func (h *handler) sync(w http.ResponseWriter, r *http.Request) {
	// Before reading the body: a refused upload costs no 32 MiB parse.
	if user, _ := auth.UserFrom(r.Context()); !h.admitted(w, user) {
		return
	}
	var req model.SyncRequest
	if err := httpx.ReadJSON(w, r, &req, httpx.MaxBodySync); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if err := validate(&req); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
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
	if len(days) > 0 {
		if err := h.bus.PublishIngested(ctx, model.IngestedEvent{UserID: userID, Days: days, Reason: "sync", RequestID: requestID(r)}, ""); err != nil {
			// Data is stored; scoring's periodic pass will catch up. Report, don't fail the sync.
			h.log.ErrorContext(ctx, "publish ingested event", "err", err)
		}
	}
	countIngested(&req)
	h.log.InfoContext(ctx, "sync", "device", req.DeviceID, "samples", ns, "sleep", nsl, "workouts", nw, "days", len(days))
	httpx.WriteJSON(w, http.StatusOK, model.SyncResponse{Samples: ns, Sleep: nsl, Workouts: nw, Days: days})
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
		if !model.KnownSampleTypes[s.Type] {
			return fmt.Errorf("samples[%d]: unknown type %q", i, s.Type)
		}
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
		if !model.KnownSleepStages[s.Stage] {
			return fmt.Errorf("sleep[%d]: unknown stage %q", i, s.Stage)
		}
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
