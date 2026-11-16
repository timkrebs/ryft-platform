package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/timkrebs/ryft/internal/app"
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

func main() { app.Main(spec()) }

// spec is the api on app's lifecycle (A2), as the other three services run: the settings
// before anything connects, the port behind the start gate before the pool, the metrics
// push on every tick and at SIGTERM, and Cloud Run's 10 s shutdown budget. The api keeps
// its own mux (health with today's bodies, the tick, /v1, /v1/admin), so it hands app the
// whole of it (Parts.Handler).
func spec() app.Spec {
	var (
		authCfg   config.Auth
		beta      config.Beta
		adminCfg  config.Admin
		analytics config.Analytics
	)
	return app.Spec{
		Name:       "api",
		Edge:       true,
		Outbox:     app.EventOutbox, // every event the api publishes goes through the outbox (D101)
		TickBudget: tickBudget,
		// Before any connection (D78): without the policy version the api cannot record or
		// check consent, and a malformed ADMIN_WEB_ORIGINS must fail at once, not behind
		// the database wait (TestMainRefusesBadAdminOriginsBeforeConnecting).
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
			if adminCfg, err = config.LoadAdmin(); err != nil {
				return app.ConfigError{Msg: "admin config", Err: err}
			}
			// A host that is no https origin stops the start (D231). The line says whether a
			// key is set, never the key or the host.
			if analytics, err = config.LoadAnalytics(); err != nil {
				return app.ConfigError{Msg: "analytics config", Err: err}
			}
			log.Info("analytics", "configured", analytics.ProjectKey != "")
			return nil
		},
		Build: func(ctx context.Context, d app.Deps) (app.Parts, error) {
			return build(ctx, d, authCfg, beta, adminCfg, analytics)
		},
	}
}

// build makes the api's mux on the opened pool and transport. ctx is Build's: the JWKS
// refresher runs on it.
func build(ctx context.Context, d app.Deps, authCfg config.Auth, beta config.Beta, adminCfg config.Admin,
	analytics config.Analytics) (app.Parts, error) {
	log := d.Log
	// The import's comparisons are internal/train's pure rules (D131).
	st := store.New(d.Pool).WithImportRules(importRules())
	verifier, err := auth.FromConfig(ctx, authCfg)
	if err != nil {
		return app.Parts{}, app.ConfigError{Msg: "auth", Err: err}
	}
	log.Info("auth", "mode", authCfg.Mode)
	acct, err := newAccounts(st, log, authCfg)
	if err != nil {
		return app.Parts{}, app.ConfigError{Msg: "account deletion", Err: err}
	}

	// A session's completion commits with its event, and the handler that wrote a row
	// flushes it before it answers (D219); the tick's sweep sends what a flush could not.
	ob := d.Outbox
	h := &handler{st: st, bus: events.Publisher{Sender: ob}, outbox: ob, log: log, cache: newInsightCache(), accounts: acct,
		engine: scoring.NewEngine(st, log), beta: beta,
		emails: acct, webURL: adminCfg.PublicWebURL, analytics: analyticsConfig(analytics)}
	// Email-bound invites need the Supabase project to confirm emails (D159). Nothing reads its setting at
	// start: the tick does, and the first request that needs it (binding.go). Until it is known to be on,
	// bound codes are neither made nor redeemed.
	var settings autoconfirmSource
	if acct.admin != nil {
		settings = acct.admin
	}
	guard := newConfirmGuard(settings, log)
	h.binding = guard
	if settings == nil {
		log.Warn("email-bound invites are unavailable: the api has no Supabase admin client (SUPABASE_SERVICE_ROLE_KEY unset, or apikey mode)")
	}
	purgeSeries()
	pool := d.Pool
	tick, err := d.Tick(tickSteps{ // the metrics push is the tick's final step (app)
		ping:      func(ctx context.Context) error { return db.Ping(ctx, pool, "api") },
		sweep:     ob.Sweep,
		reconcile: acct.reconcile,
		settings:  guard.Refresh,
		purge:     func(ctx context.Context) error { return purgeInviteEmails(ctx, st, log) },
	}.ticks()...)
	if err != nil {
		return app.Parts{}, err
	}
	return app.Parts{Handler: newMux(h, muxOptions{
		log:      log,
		verifier: verifier,
		ready: func(ctx context.Context) error {
			if err := st.Ready(ctx); err != nil {
				return err
			}
			return verifier.Ready(ctx)
		},
		tick:     tick,
		gate:     beta.Gate,
		admin:    adminCfg,
		authMode: authCfg.Mode,
	})}, nil
}

// muxOptions is what the api's routes are built from besides the handler.
type muxOptions struct {
	log      *slog.Logger
	verifier auth.Verifier
	ready    func(context.Context) error // /readyz
	tick     http.Handler                // POST /internal/tick (the transport's: its token is checked there)
	gate     config.Mode                 // BETA_GATE
	admin    config.Admin
	authMode string
}

// newMux is the api's routes: health, the tick, /v1 behind the auth middleware and the
// per-user limit, the admin routes and a JSON 404 for the rest. The tick sits beside
// /v1, outside both (D218): its token is the push service account's, which the
// transport's verifier checks, and Cloud Scheduler is no user to limit.
func newMux(h *handler, o muxOptions) *http.ServeMux {
	log, verifier, adminCfg := o.log, o.verifier, o.admin
	mux := http.NewServeMux()
	httpx.Health(mux, o.ready)
	mux.Handle(http.MethodPost+" "+eventbus.PathTick, o.tick)

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
	authOpts.OnAuth = auth.LoadUser(ensureUser(h.st))
	mux.Handle("/v1/", auth.Middleware(authOpts, betaGate(o.gate, httpx.Timeout(readTimeout, httpx.WithRoutes(api)))))
	// The training import gets 50 s instead of readTimeout, like a sync (D129).
	mux.Handle("POST /v1/train/import", importRoute(authOpts, o.gate, h.trainImport))
	// The operator's routes: ADMIN_API_KEY, or an allow-listed operator's Supabase token (only with
	// Supabase auth: a local apikey stack admits the key alone); absent without a key.
	if adminCfg.Key != "" {
		opts := adminOptions{Key: adminCfg.Key, Origins: adminCfg.WebOrigins, Timeout: readTimeout}
		if o.authMode == config.AuthSupabase {
			opts.Verifier = verifier
		}
		mux.Handle("/v1/admin/", adminRoutes(h, opts))
		log.Info("admin routes enabled", "token_sign_in", opts.Verifier != nil, "web_origins", len(adminCfg.WebOrigins))
	} else {
		mux.HandleFunc("/v1/admin/", httpx.NotFound)
	}
	mux.HandleFunc("/", httpx.NotFound)
	return mux
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
	// emails answers the confirmed email of an account, for a code bound to an email; nil: no code binds.
	emails inviteEmails
	// binding says whether email-bound invites work now (the Supabase project confirms emails); nil: they do not.
	binding bindingGate
	// webURL is PUBLIC_WEB_URL, the base of an invite link; "" is config.DefaultPublicWebURL.
	webURL string
	// analytics is GET /v1/config → analytics; nil (served as null) without a project key (D231).
	analytics *model.AnalyticsConfig
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
	if p.BirthYear != nil && !scoring.ValidBirthYear(*p.BirthYear, year) {
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
