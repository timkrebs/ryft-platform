// api is the side the iOS app talks to apart from uploads: daily scores,
// trends, workouts, user settings, a recompute trigger, and Train (exercises,
// plans, logged sessions).
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

func main() {
	log := httpx.Logger("api")
	cfg, err := config.Load("api")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	authCfg, err := config.LoadAuth()
	if err != nil {
		log.Error("auth config", "err", err)
		os.Exit(1)
	}
	// Before any connection: without the policy version the api cannot record or
	// check consent (D78), so it exits at once instead of behind the DB retries.
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
	go db.Watch(ctx, pool, "api", log)
	// The import's comparisons are internal/train's pure rules (D131).
	st := store.New(pool).WithImportRules(importRules())

	// RabbitMQ (D97), retried at start-up.
	bus, err := eventbus.Open(ctx, cfg, "api", log)
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
	adminKey, err := config.LoadAdmin()
	if err != nil {
		log.Error("admin config", "err", err)
		os.Exit(1)
	}

	acct, err := newAccounts(st, log, authCfg)
	if err != nil {
		log.Error("account deletion", "err", err)
		os.Exit(1)
	}
	go acct.reconcileEvery(ctx, reconcileEvery)

	// Every event the api publishes goes through the outbox (D101): durable while the
	// broker is down, and a session's completion commits with its event.
	ob := outbox.New(pool, "api", bus, log)
	go ob.Run(ctx)

	h := &handler{st: st, bus: events.Publisher{Sender: ob}, outbox: ob, log: log, cache: newInsightCache(), accounts: acct,
		engine: scoring.NewEngine(st, log), beta: beta}

	mux := http.NewServeMux()
	httpx.ServeMetrics(log, ":"+cfg.MetricsPort)
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		return verifier.Ready(ctx)
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/config", h.remoteConfig)
	api.HandleFunc("GET /v1/model", h.modelInfo)
	api.HandleFunc("GET /v1/me", h.me)
	api.HandleFunc("GET /v1/me/sync", h.syncStatus)
	api.HandleFunc("PATCH /v1/me", h.patchMe)
	api.HandleFunc("POST /v1/me/apple-authorization", h.appleAuthorization)
	api.HandleFunc("PATCH /v1/me/notifications", h.patchNotifications)
	api.HandleFunc("POST /v1/me/consent", h.postConsent)
	api.HandleFunc("DELETE /v1/me/consent", h.deleteConsent)
	api.HandleFunc("POST /v1/me/invite", h.redeemInvite)
	api.HandleFunc("POST /v1/feedback", h.postFeedback)
	api.HandleFunc("POST /v1/devices/{id}/push", h.registerPush)
	api.HandleFunc("DELETE /v1/devices/{id}/push", h.deletePush)
	api.HandleFunc("GET /v1/days", h.days)
	api.HandleFunc("GET /v1/days/{day}", h.day)
	api.HandleFunc("GET /v1/sleep", h.sleepRange)
	api.HandleFunc("GET /v1/sleep/plan", h.sleepPlan)
	api.HandleFunc("GET /v1/workouts", h.workouts)
	api.HandleFunc("GET /v1/workouts/{id}", h.workout)
	api.HandleFunc("GET /v1/trends", h.trends)
	api.HandleFunc("GET /v1/insights", h.insights)
	api.HandleFunc("POST /v1/recompute", h.recompute)
	api.HandleFunc("GET /v1/exercises", h.exercises)
	api.HandleFunc("POST /v1/exercises", h.createExercise)
	api.HandleFunc("PATCH /v1/exercises/{id}", h.patchExercise)
	api.HandleFunc("DELETE /v1/exercises/{id}", h.archiveExercise)
	api.HandleFunc("GET /v1/exercises/{id}/history", h.exerciseHistory)
	api.HandleFunc("GET /v1/plans", h.plans)
	api.HandleFunc("GET /v1/plans/today", h.planToday)
	api.HandleFunc("DELETE /v1/plans/enrollment", h.unenroll)
	api.HandleFunc("GET /v1/plans/{id}", h.plan)
	api.HandleFunc("PUT /v1/plans/{id}", h.putPlan)
	api.HandleFunc("DELETE /v1/plans/{id}", h.deletePlan)
	api.HandleFunc("POST /v1/plans/{id}/enroll", h.enroll)
	api.HandleFunc("GET /v1/sessions", h.sessions)
	api.HandleFunc("GET /v1/sessions/{id}", h.session)
	api.HandleFunc("PUT /v1/sessions/{id}", h.putSession)
	api.HandleFunc("DELETE /v1/sessions/{id}", h.deleteSession)
	api.HandleFunc("POST /v1/sessions/{id}/complete", h.completeSession)
	api.HandleFunc("GET /v1/behaviors", h.behaviors)
	api.HandleFunc("PATCH /v1/behaviors", h.patchBehaviors)
	api.HandleFunc("POST /v1/behaviors", h.createBehavior)
	api.HandleFunc("DELETE /v1/behaviors/{id}", h.deleteBehavior)
	api.HandleFunc("GET /v1/behaviors/impact", h.behaviorImpact)
	api.HandleFunc("GET /v1/journal/{day}", h.journalDay)
	api.HandleFunc("PUT /v1/journal/{day}", h.putJournal)
	api.HandleFunc("/v1/", httpx.NotFound)
	limits := ratelimit.NewUserLimits()
	authOpts := auth.Options{
		Log:      log,
		Verifier: verifier,
		Limit: func(r *http.Request, p auth.Principal) (string, bool, time.Duration) {
			return limits.Allow(r, p.UserID.String())
		},
	}
	// Account deletion skips the user lookup (a deleted account gets 401 from
	// it) so that a retry after a failed Supabase call still reaches the
	// handler, and its own deadline replaces readTimeout.
	del := http.NewServeMux()
	del.HandleFunc("DELETE /v1/me", h.deleteMe)
	mux.Handle("DELETE /v1/me", auth.Middleware(authOpts, httpx.WithRoutes(del)))
	authOpts.OnAuth = auth.LoadUser(ensureUser(st))
	mux.Handle("/v1/", auth.Middleware(authOpts, betaGate(beta.Gate, httpx.Timeout(readTimeout, httpx.WithRoutes(api)))))
	// The training import gets 50 s instead of readTimeout, like a sync (D129).
	mux.Handle("POST /v1/train/import", importRoute(authOpts, beta.Gate, h.trainImport))
	// The operator's routes: ADMIN_API_KEY instead of a user token; absent
	// without a key.
	if adminKey != "" {
		mux.Handle("/v1/admin/", adminRoutes(h, adminKey, readTimeout))
		log.Info("admin routes enabled")
	} else {
		mux.HandleFunc("/v1/admin/", httpx.NotFound)
	}
	mux.HandleFunc("/", httpx.NotFound)

	if err := httpx.Serve(log, ":"+cfg.Port, httpx.Instrument(log, httpx.Recover(httpx.SecurityHeaders(mux)))); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// readTimeout bounds the work of one API request (reads are a few queries).
const readTimeout = 15 * time.Second

// requestID is the ID Instrument gave this request, for events it causes.
func requestID(r *http.Request) string {
	if f := httpx.Info(r.Context()); f != nil {
		return f.RequestID
	}
	return ""
}

// ensureUser finds or creates the caller's user row from the verified token.
// A deleted account's token is rejected like any other bad token.
func ensureUser(st *store.Store) func(context.Context, auth.Principal) (model.User, error) {
	return func(ctx context.Context, p auth.Principal) (model.User, error) {
		u, err := st.EnsureUser(ctx, store.Identity{ID: p.UserID, Email: p.Email, Provider: p.Provider, Name: p.Name})
		if errors.Is(err, store.ErrDeleted) {
			return u, auth.Unauthorized("deleted", err)
		}
		return u, err
	}
}

// publisher is what the handlers publish (an events.Bus; a fake in tests).
type publisher interface {
	PublishIngested(ctx context.Context, ev model.IngestedEvent, dedupID string) error
	PublishSessionCompleted(ctx context.Context, ev model.SessionCompletedEvent, dedupID string) error
}

type handler struct {
	st       *store.Store
	bus      publisher
	outbox   *outbox.Outbox // nil in tests: a session's event then goes through bus
	log      *slog.Logger
	cache    *insightCache
	accounts *accounts
	engine   *scoring.Engine // pure scoring with store lookups, for session completion
	beta     config.Beta
	// importFinish is the training import's finish time; 0 is importFinishTimeout (tests
	// shorten it to make a slow finish fail fast).
	importFinish time.Duration
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	u, err := h.withSources(r, userOf(r))
	if err == nil {
		u.AccountHint, err = h.accountHint(r, u)
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, u)
}

// sourcesWindow is how far back User.sources looks.
const sourcesWindow = 30 * 24 * time.Hour

// withSources adds User.sources, which the per-request user row does not load.
func (h *handler) withSources(r *http.Request, u model.User) (model.User, error) {
	src, err := h.st.UserSources(r.Context(), u.ID, time.Now().Add(-sourcesWindow))
	u.Sources = src
	return u, err
}

func (h *handler) patchMe(w http.ResponseWriter, r *http.Request) {
	var p model.UserPatch
	if err := httpx.ReadJSON(w, r, &p, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if p.MaxHR != nil && (*p.MaxHR < 100 || *p.MaxHR > 230) {
		httpx.WriteError(w, 422, "max_hr out of range")
		return
	}
	if p.SleepBaseMin != nil && (*p.SleepBaseMin < 240 || *p.SleepBaseMin > 720) {
		httpx.WriteError(w, 422, "sleep_base_min out of range")
		return
	}
	if p.SleepBaseMin != nil && p.SleepBaseManual != nil && !*p.SleepBaseManual {
		httpx.WriteError(w, 422, "sleep_base_min sets the baseline by hand; it cannot go with sleep_base_manual false")
		return
	}
	if p.MaxHR != nil && p.MaxHRManual != nil && !*p.MaxHRManual {
		httpx.WriteError(w, 422, "max_hr sets it by hand; it cannot go with max_hr_manual false")
		return
	}
	year := time.Now().UTC().Year()
	if p.BirthYear != nil && (*p.BirthYear < scoring.MinBirthYear || *p.BirthYear > year-scoring.MinAge) {
		httpx.WriteError(w, 422, fmt.Sprintf("birth_year must be %d to %d", scoring.MinBirthYear, year-scoring.MinAge))
		return
	}
	// The estimate for the birth year in effect after this patch (D81).
	var estimate *int
	if birth := cmp.Or(p.BirthYear, userOf(r).BirthYear); birth != nil {
		e := scoring.EstimatedMaxHR(*birth, year)
		estimate = &e
	}
	if p.TZ != nil {
		if _, err := time.LoadLocation(*p.TZ); err != nil || *p.TZ == "" {
			httpx.WriteError(w, 422, "tz must be an IANA time zone name")
			return
		}
	}
	u, err := h.st.UpdateUser(r.Context(), userOf(r).ID, p, estimate)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h.cache.forget(u.ID) // max_hr and the baseline feed cards
	if u, err = h.withSources(r, u); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, u)
}

// recomputeEvery is the minimum gap between two recompute requests of a user.
const recomputeEvery = time.Minute

// recompute asks scoring (an ingested event) to rebuild a date range, oldest first.
// Use it after a big HealthKit backfill or after changing max_hr / sleep base.
func (h *handler) recompute(w http.ResponseWriter, r *http.Request) {
	var req model.RecomputeRequest
	if err := httpx.ReadJSON(w, r, &req, httpx.MaxBodyDefault); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	for _, d := range []string{req.From, req.To} {
		if _, err := time.Parse(dayLayout, d); err != nil {
			httpx.WriteError(w, 422, "from/to must be YYYY-MM-DD")
			return
		}
	}
	if req.From > req.To {
		httpx.WriteError(w, 422, "from must be <= to")
		return
	}
	if n, _ := scoring.RangeDays(req.From, req.To); n > scoring.MaxRangeDays {
		httpx.WriteError(w, 422, fmt.Sprintf("range too large (max %d days)", scoring.MaxRangeDays))
		return
	}
	u := userOf(r)
	// One recompute per user per minute across all replicas: each one can
	// queue 400 days of scoring on the single scoring consumer.
	ok, err := h.st.TryRequestRecompute(r.Context(), u.ID, recomputeEvery)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(recomputeEvery.Seconds())))
		httpx.WriteError(w, http.StatusTooManyRequests, "a recompute was requested less than a minute ago")
		return
	}
	ev := model.IngestedEvent{UserID: u.ID, Days: []string{req.From, req.To}, Reason: "recompute", RequestID: requestID(r)}
	dedup := fmt.Sprintf("recompute:%s:%s:%s", u.ID, req.From, req.To)
	if err := h.bus.PublishIngested(r.Context(), ev, dedup); err != nil {
		h.log.ErrorContext(r.Context(), "publish recompute", "err", err)
		w.Header().Set("Retry-After", "30")
		httpx.WriteError(w, http.StatusServiceUnavailable, "queue unavailable")
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "from": req.From, "to": req.To})
}
