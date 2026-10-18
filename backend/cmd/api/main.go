// api is the read side the iOS app talks to: daily scores, trends, workouts,
// user settings and a recompute trigger.
package main

import (
	"context"
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
	if err := db.Migrate(ctx, pool, log); err != nil {
		log.Error("migrate", "err", err)
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

	h := &handler{st: st, bus: bus, log: log, cache: newInsightCache()}

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
	mux.Handle("/v1/", auth.Middleware(auth.Options{
		Log:      log,
		Verifier: verifier,
		OnAuth:   auth.LoadUser(ensureUser(st)),
	}, httpx.WithRoutes(api)))
	mux.HandleFunc("/", httpx.NotFound)

	if err := httpx.Serve(log, ":"+cfg.Port, httpx.Instrument(log, mux)); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

// ensureUser finds or creates the caller's user row from the verified token.
func ensureUser(st *store.Store) func(context.Context, auth.Principal) (model.User, error) {
	return func(ctx context.Context, p auth.Principal) (model.User, error) {
		return st.EnsureUser(ctx, store.Identity{ID: p.UserID, Email: p.Email, Provider: p.Provider, Name: p.Name})
	}
}

type handler struct {
	st    *store.Store
	bus   *events.Bus
	log   *slog.Logger
	cache *insightCache
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, 200, userOf(r))
}

func (h *handler) patchMe(w http.ResponseWriter, r *http.Request) {
	var p model.UserPatch
	if err := httpx.ReadJSON(r, &p); err != nil {
		httpx.WriteError(w, 400, err.Error())
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
		httpx.WriteError(w, 500, err.Error())
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
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, 400, err.Error())
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
		httpx.WriteError(w, 500, err.Error())
		return
	}
	if !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(recomputeEvery.Seconds())))
		httpx.WriteError(w, http.StatusTooManyRequests, "a recompute was requested less than a minute ago")
		return
	}
	ev := model.IngestedEvent{UserID: u.ID, Days: []string{req.From, req.To}, Reason: "recompute"}
	dedup := fmt.Sprintf("recompute:%s:%s:%s", u.ID, req.From, req.To)
	if err := h.bus.PublishIngested(r.Context(), ev, dedup); err != nil {
		httpx.WriteError(w, 503, "queue unavailable: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "from": req.From, "to": req.To})
}
