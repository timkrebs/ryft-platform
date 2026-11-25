package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/timkrebs/ryft/internal/app"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
	"github.com/timkrebs/ryft/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "fold-workouts" {
		os.Exit(foldWorkouts(os.Args[2:]))
	}
	app.Main(spec())
}

// spec is ingest on app's lifecycle (A2), as the other three services run: the settings
// before anything connects, the port behind the start gate before the pool, the metrics
// push on every tick and at SIGTERM, and Cloud Run's 10 s shutdown budget. Ingest keeps
// its own mux (health with today's bodies, the tick, /v1/sync), so it hands app the whole
// of it (Parts.Handler).
func spec() app.Spec {
	var (
		authCfg config.Auth
		beta    config.Beta
		attest  config.AppAttest
		conn    config.Connect
	)
	return app.Spec{
		Name:       "ingest",
		Edge:       true,
		Outbox:     app.EventOutbox, // a sync's days and the event announcing them commit together (D101)
		TickBudget: tickBudget,
		// Before any connection: without the policy version ingest cannot check consent
		// (D78), so it exits at once instead of behind the DB retries.
		Load: func(log *slog.Logger) (err error) {
			if authCfg, err = config.LoadAuth(); err != nil {
				return app.ConfigError{Msg: "auth config", Err: err}
			}
			if beta, err = config.LoadBeta(); err == nil {
				err = beta.RequirePolicy()
			}
			if err != nil {
				return app.ConfigError{Msg: "beta config", Err: err}
			}
			log.Info("beta", "gate", beta.Gate, "consent", beta.Consent, "privacy_policy_version", beta.PrivacyPolicyVersion)
			// warn or on without the ring, or a malformed ring, stops the start (D247). Ingest checks
			// tokens only, so it needs no App ID. The line names no key and no account.
			if attest, err = config.LoadAppAttest(); err != nil {
				return app.ConfigError{Msg: "app attest config", Err: err}
			}
			log.Info("app attest", "mode", attest.Mode, "ring", attest.Ring != nil, "exempt", len(attest.ExemptUsers))
			// The fake on Cloud Run, a provider without its settings or a malformed id stops the start (D257).
			// The line names the providers, never a secret.
			if conn, err = config.LoadConnect("ingest"); err != nil {
				return app.ConfigError{Msg: "connect config", Err: err}
			}
			log.Info("connect", "providers", conn.Providers)
			return nil
		},
		Build: func(ctx context.Context, d app.Deps) (app.Parts, error) {
			return build(ctx, d, authCfg, beta, attest, conn)
		},
	}
}

// build makes ingest's mux on the opened pool and transport. ctx is Build's: the JWKS
// refresher runs on it.
func build(ctx context.Context, d app.Deps, authCfg config.Auth, beta config.Beta, attest config.AppAttest, conn config.Connect) (app.Parts, error) {
	verifier, err := auth.FromConfig(ctx, authCfg)
	if err != nil {
		return app.Parts{}, app.ConfigError{Msg: "auth", Err: err}
	}
	d.Log.Info("auth", "mode", authCfg.Mode)
	// The sync flushes the outbox before it answers (D219); the tick's sweep sends what a
	// flush could not (and what fold-workouts wrote).
	h := &handler{st: store.New(d.Pool), outbox: d.Outbox, log: d.Log, beta: beta, attest: attest}
	pool := d.Pool
	// The hook, its inbox and the consumer of its events (D257): the push halves of the served providers only.
	reg, err := pushRegistry(conn)
	if err != nil {
		return app.Parts{}, app.ConfigError{Msg: "connect config", Err: err}
	}
	h.connect = newConnectHook(h.st, reg, beta, conn.MinPolicyVersion, d.Outbox, d.Log)
	tick, err := d.Tick(ingestTicks( // the metrics push is the tick's final step (app)
		func(ctx context.Context) error { return db.Ping(ctx, pool, "ingest") }, d.Outbox.Sweep,
		eventbus.Tick{Name: "connect inbox", Run: h.connect.tick})...)
	if err != nil {
		return app.Parts{}, err
	}
	mux := newMux(h, d.Log, verifier, tick)
	if err := h.connect.register(mux, d.Bus); err != nil {
		return app.Parts{}, err
	}
	return app.Parts{Handler: mux}, nil
}

// tickBudget is ingest's POST /internal/tick, both steps together (D219).
const tickBudget = 45 * time.Second

// ingestTicks are ingest's tick steps: the db ping (ryft_db_up), its own steps (the inbox's upkeep,
// D257) and the outbox sweep last, so the events a step wrote leave in the same tick: the work of the
// db watch and the relay goroutine before Cloud Run (D219).
func ingestTicks(ping, sweep func(context.Context) error, own ...eventbus.Tick) []eventbus.Tick {
	steps := []eventbus.Tick{{Name: "db ping", Run: ping}}
	steps = append(steps, own...)
	return append(steps, eventbus.Tick{Name: "outbox sweep", Run: sweep})
}

// newMux is ingest's routes: health, the tick, POST /v1/sync behind the auth middleware,
// the per-user limit and the app token check (h.attest; no route exempt, the exempt
// accounts are), and a JSON 404 for the rest. The tick sits beside /v1, outside all of
// them (D218): its token is the push service account's, which the transport's verifier
// checks.
func newMux(h *handler, log *slog.Logger, verifier auth.Verifier, tick http.Handler) *http.ServeMux {
	st := h.st
	mux := http.NewServeMux()
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		// Not Pub/Sub: a sync's event waits in the outbox when a flush cannot send it
		// (D101), so ingest keeps taking syncs, like the api (OutboxBacklog).
		return verifier.Ready(ctx)
	})
	mux.Handle(http.MethodPost+" "+eventbus.PathTick, tick)
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/sync", h.sync)
	api.HandleFunc("POST /v1/wearables/healthkit/sync", h.sync)
	api.HandleFunc("/v1/", httpx.NotFound)
	limits := ratelimit.NewUserLimits()
	load := h.loadUser
	if load == nil {
		load = func(ctx context.Context, p auth.Principal) (model.User, error) {
			u, err := st.EnsureUser(ctx, store.Identity{ID: p.UserID, Email: p.Email, Provider: p.Provider, Name: p.Name})
			if errors.Is(err, store.ErrDeleted) { // a deleted account's token is a bad token
				return u, auth.Unauthorized("deleted", err)
			}
			return u, err
		}
	}
	mux.Handle("/v1/", auth.Middleware(auth.Options{
		Log:      log,
		Verifier: verifier,
		Limit: func(r *http.Request, p auth.Principal) (string, bool, time.Duration) {
			return limits.Allow(r, p.UserID.String())
		},
		OnAuth: auth.LoadUser(load),
	}, gate.AppAttest(gate.AppAttestOptions{Config: h.attest, Service: "ingest", Log: log},
		httpx.Timeout(syncTimeout, httpx.WithRoutes(api)))))
	mux.HandleFunc("/", httpx.NotFound)
	return mux
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
	outbox *outbox.Outbox // what POST /v1/sync writes in its transaction and flushes before it answers
	log    *slog.Logger
	beta   config.Beta
	attest config.AppAttest // APP_ATTEST and its ring: the token check of /v1/ (no route is exempt)
	// connect is the hook, its consumer and the inbox's tick step (D257); nil without one (tests).
	connect *connectHook
	// loadUser finds or creates the caller's row; nil is st.EnsureUser (tests load a fixed user).
	loadUser func(context.Context, auth.Principal) (model.User, error)

	expMu  sync.Mutex // the cached sync-to-score estimate
	expSec int
	expAt  time.Time
}

// admitted says whether user may upload: beta access (BETA_GATE) and a consent
// to the current privacy policy (CONSENT_ENFORCE; GDPR Art. 9(2)(a), D78). A
// gate in warn mode lets the sync through with the code in X-RYFT-Gate (D87).
// The consent rule is the api's import's as well (internal/gate, D129).
func (h *handler) admitted(w http.ResponseWriter, r *http.Request, user model.User) bool {
	return gate.Invite(w, r, h.beta.Gate, user, "ingest") && gate.Consent(w, r, h.beta, user, "ingest")
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
	ctx := r.Context()
	user, _ := auth.UserFrom(ctx)
	userID := user.ID
	if hasBodyMass(req) {
		allowed, err := h.st.HealthKitBodyMassAllowed(ctx, userID)
		if err != nil {
			h.unavailable(w, r, fmt.Errorf("optional HealthKit stream: %w", err))
			return
		}
		if !allowed {
			ignored.Samples.UnknownType += dropBodyMass(&req)
		}
	}
	sent := model.SyncCounts{Samples: len(req.Samples), Sleep: len(req.Sleep), Workouts: len(req.Workouts)}
	loc, err := time.LoadLocation(user.TZ)
	if err != nil {
		httpx.Fail(w, r, fmt.Errorf("user tz: %w", err))
		return
	}
	var grant *model.Consent
	if h.beta.Consent == config.ModeOn {
		grant = user.Consent // the cheap gate above already validated its policy version
	}
	consentChanged := func(err error) bool {
		if !errors.Is(err, store.ErrHealthKitConsentChanged) {
			return false
		}
		gate.Consent(w, r, h.beta, model.User{}, "ingest")
		return true
	}
	counts, sourceErr, err := h.st.WriteHealthKitRecords(ctx, userID, grant, req)
	if consentChanged(err) {
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ns, nsl, nw := counts.Samples, counts.Sleep, counts.Workouts
	// User.sources only: the data is stored, so a failure here does not fail the sync.
	if sourceErr != nil {
		h.log.ErrorContext(ctx, "sources", "err", sourceErr)
	}

	// The deletions, the days for GET /v1/me/sync and the event for scoring, in one
	// transaction (D101, D-b) after the records are stored. A failure is a 503, not
	// swallowed: the records are idempotent, the app sends the request again, and a
	// deletion that already happened comes back as unknown_id.
	reqDays := touchedDays(&req, loc)
	var applied store.Applied
	eventFailed := false
	if len(reqDays) > 0 || len(req.Deleted) > 0 || req.Cursor != nil {
		applied, err = h.st.ApplyHealthKit(ctx, userID, grant, req.DeviceID, req.Cursor, store.Apply{Days: reqDays, Deleted: req.Deleted, Loc: loc, Fold: foldRequest(req.Workouts),
			Emit: func(tx pgx.Tx, days []string) error {
				m, err := events.Ingested(model.IngestedEvent{UserID: userID, Days: days, Reason: "sync", RequestID: requestID(r)}, "")
				if err != nil {
					return err
				}
				err = outbox.Insert(ctx, tx, "ingest", m)
				eventFailed = err != nil
				return err
			}})
		if consentChanged(err) {
			return
		}
		if err != nil {
			if eventFailed {
				metrics.EventsPublished.WithLabelValues("sync", "error").Inc()
			}
			countIngested(&req) // the records are stored whatever happened to the deletions
			h.unavailable(w, r, fmt.Errorf("apply: %w", err))
			return
		}
	}
	if applied.FoldSkipped {
		h.log.WarnContext(ctx, "workout fold skipped, too many workouts in scope", "workouts", len(req.Workouts), "limit", store.FoldMaxRows)
	}
	if len(applied.Days) > 0 {
		h.outbox.Flush(ctx) // before the answer: nothing runs after it (D219)
	}
	days := applied.Days
	if days == nil {
		days = []string{}
	}
	ignored.Samples.Duplicate, ignored.Sleep.Duplicate, ignored.Workouts.Duplicate = sent.Samples-ns, sent.Sleep-nsl, sent.Workouts-nw
	ignored.Samples.UnknownID, ignored.Sleep.UnknownID, ignored.Workouts.UnknownID =
		applied.Unknown.Samples, applied.Unknown.Sleep, applied.Unknown.Workouts
	summary := map[string]any{"samples": ns, "sleep": nsl, "workouts": nw, "days": len(days), "app_version": req.AppVersion,
		"ignored": ignored, "deleted": applied.Deleted}
	if err := h.st.RecordHealthKitSync(ctx, userID, grant, req.DeviceID, summary); err != nil && !errors.Is(err, store.ErrHealthKitConsentChanged) {
		h.log.ErrorContext(ctx, "record sync", "err", err)
	}
	countIngested(&req)
	countOutcomes(ignored, applied.Deleted)
	h.log.InfoContext(ctx, "sync", "device", req.DeviceID, "samples", ns, "sleep", nsl, "workouts", nw, "days", len(days))
	httpx.WriteJSON(w, http.StatusOK, model.SyncResponse{Samples: ns, Sleep: nsl, Workouts: nw, Days: days, Deleted: applied.Deleted,
		Accepted: model.SyncCounts{Samples: ns, Sleep: nsl, Workouts: nw}, Ignored: ignored,
		Scoring: model.SyncScoring{QueuedDays: days, ExpectedWithinSec: h.expectedSec(ctx)}})
}

// The window a record must fall in (SyncResponse → ignored.outside_window): a
// day of clock skew ahead, and as far back as a score reaches.
const (
	futureSlack = 24 * time.Hour
	pastWindow  = 400 * 24 * time.Hour
)

func bodyMassSample(s model.Sample) bool { return s.Type == model.SampleBodyMass }

func hasBodyMass(req model.SyncRequest) bool {
	return (req.Cursor != nil && req.Cursor.Stream == model.SampleBodyMass) || slices.ContainsFunc(req.Samples, bodyMassSample)
}

func dropBodyMass(req *model.SyncRequest) int {
	before := len(req.Samples)
	req.Samples = slices.DeleteFunc(req.Samples, bodyMassSample)
	if req.Cursor != nil && req.Cursor.Stream == model.SampleBodyMass {
		req.Cursor = nil
	}
	return before - len(req.Samples)
}

// screen drops the records the server does not store and counts them: unknown
// sample types and sleep stages (an app reading a new HealthKit type keeps
// syncing instead of failing with 422), records whose source is the reserved
// ryft-direct-<provider> of a direct connection (D257: a sync can neither mimic one
// nor overwrite its rows; counted unknown_type), and records outside the window.
func screen(req *model.SyncRequest, now time.Time) model.SyncIgnored {
	var ig model.SyncIgnored
	outside := func(start, end time.Time) bool {
		return start.After(now.Add(futureSlack)) || end.Before(now.Add(-pastWindow))
	}
	samples := req.Samples[:0]
	for _, s := range req.Samples {
		switch {
		case !model.KnownSampleTypes[s.Type] || connect.IsReservedSource(s.Source):
			ig.Samples.UnknownType++
		case outside(s.Start, s.End):
			ig.Samples.OutsideWindow++
		case s.WasUserEntered && s.Type != model.SampleBodyMass:
			ig.Samples.UnknownType++
		default:
			samples = append(samples, s)
		}
	}
	req.Samples = samples
	sleep := req.Sleep[:0]
	for _, s := range req.Sleep {
		switch {
		case !model.KnownSleepStages[s.Stage] || connect.IsReservedSource(s.Source):
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
	if c := req.Cursor; c != nil && (len(c.Stream) == 0 || len(c.Stream) > 48 || c.Sequence < 0 || len(c.Value) > 4096) {
		return fmt.Errorf("invalid HealthKit cursor")
	}
	if len(req.Deleted) > model.MaxDeleted {
		return fmt.Errorf("deleted: at most %d entries per request", model.MaxDeleted)
	}
	if len(req.Workouts) > model.MaxWorkouts {
		return fmt.Errorf("workouts: at most %d entries per request", model.MaxWorkouts)
	}
	for i := range req.Samples {
		s := &req.Samples[i]
		if s.Type == model.SampleBodyMass && (s.Unit != "kg" || s.Value < 25 || s.Value > 350) {
			return fmt.Errorf("samples[%d]: body_mass must be in kg and in range", i)
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
		if wk.TZOffsetSec != nil && (*wk.TZOffsetSec < -64800 || *wk.TZOffsetSec > 64800) {
			return fmt.Errorf("workouts[%d]: invalid timezone offset", i)
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
// The day after is +24 absolute hours, not the next local date, so a clock-change
// day can miss it; pinned as it is, not fixed (D178).
func touchedDays(req *model.SyncRequest, loc *time.Location) []string {
	set := map[string]struct{}{}
	add := func(t time.Time) { set[t.In(loc).Format("2006-01-02")] = struct{}{} }
	for _, s := range req.Samples {
		add(s.Start)
		if model.SampleSpillsToNextDay(s.Type) {
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

// countOutcomes feeds ryft_sync_deleted_total and ryft_sync_ignored_total.
func countOutcomes(ig model.SyncIgnored, del model.SyncDeleted) {
	for kind, n := range map[string]int{"sample": del.Samples, "sleep": del.Sleep, "workout": del.Workouts} {
		if n > 0 {
			metrics.SyncDeleted.WithLabelValues(kind).Add(float64(n))
		}
	}
	for kind, c := range map[string]model.IgnoredCounts{"sample": ig.Samples, "sleep": ig.Sleep, "workout": ig.Workouts} {
		for reason, n := range map[string]int{"duplicate": c.Duplicate, "outside_window": c.OutsideWindow,
			"unknown_type": c.UnknownType, "unknown_id": c.UnknownID} {
			if n > 0 {
				metrics.SyncIgnored.WithLabelValues(kind, reason).Add(float64(n))
			}
		}
	}
}

// unavailable answers 503 with Retry-After: the request can be sent again as it is
// (records dedupe, a deletion already done is unknown_id).
func (h *handler) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(r.Context().Err(), context.Canceled) {
		httpx.Fail(w, r, err) // timeout and abandoned requests have their own answers
		return
	}
	h.log.ErrorContext(r.Context(), "sync not applied", "err", err)
	w.Header().Set("Retry-After", "5")
	httpx.WriteError(w, http.StatusServiceUnavailable, "temporarily unavailable, send the request again")
}

// fail is httpx.Fail, except that a conflict between two syncs of one user (a
// unique violation on a HealthKit id, a deadlock, a serialization failure) is a
// retryable 503.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "40P01" || pg.Code == "40001") {
		h.unavailable(w, r, err)
		return
	}
	httpx.Fail(w, r, err)
}
