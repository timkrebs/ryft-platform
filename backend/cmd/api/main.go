// api is the read side the iOS app talks to: daily scores, trends, workouts,
// user settings and a recompute trigger.
package main

import (
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
	"github.com/timkrebs/ryft/internal/events"
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// The migrate job applies migrations; this service's role cannot.
	waitCtx, cancelWait := context.WithTimeout(ctx, db.MigrateWait)
	err = db.WaitMigrated(waitCtx, pool, log)
	cancelWait()
	if err != nil {
		log.Error("schema", "err", err)
		os.Exit(1)
	}
	st := store.New(pool)

	bus, err := events.Connect(ctx, cfg.NatsURL, "api")
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

	acct, err := newAccounts(st, log, authCfg)
	if err != nil {
		log.Error("account deletion", "err", err)
		os.Exit(1)
	}
	go acct.reconcileEvery(ctx, reconcileEvery)

	h := &handler{st: st, bus: bus, log: log, cache: newInsightCache(), accounts: acct}

	mux := http.NewServeMux()
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		return verifier.Ready(ctx)
	})

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me", h.me)
	api.HandleFunc("PATCH /v1/me", h.patchMe)
	api.HandleFunc("GET /v1/days", h.days)
	api.HandleFunc("GET /v1/days/{day}", h.day)
	api.HandleFunc("GET /v1/sleep", h.sleepRange)
	api.HandleFunc("GET /v1/workouts", h.workouts)
	api.HandleFunc("GET /v1/workouts/{id}", h.workout)
	api.HandleFunc("GET /v1/trends", h.trends)
	api.HandleFunc("GET /v1/insights", h.insights)
	api.HandleFunc("POST /v1/recompute", h.recompute)
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
	mux.Handle("/v1/", auth.Middleware(authOpts, httpx.Timeout(readTimeout, httpx.WithRoutes(api))))
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

type handler struct {
	st       *store.Store
	bus      *events.Bus
	log      *slog.Logger
	cache    *insightCache
	accounts *accounts
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, 200, userOf(r))
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
	if p.TZ != nil {
		if _, err := time.LoadLocation(*p.TZ); err != nil || *p.TZ == "" {
			httpx.WriteError(w, 422, "tz must be an IANA time zone name")
			return
		}
	}
	u, err := h.st.UpdateUser(r.Context(), userOf(r).ID, p)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, u)
}

// recomputeEvery is the minimum gap between two recompute requests of a user.
const recomputeEvery = time.Minute

// recompute asks scoring (via NATS) to rebuild a date range, oldest first.
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
