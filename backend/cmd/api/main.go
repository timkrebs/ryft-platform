// api is the read side the iOS app talks to: daily scores, trends, workouts,
// user settings and a recompute trigger.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func main() {
	log := httpx.Logger("api")
	cfg, err := config.Load("api")
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if cfg.APIKey == "" {
		log.Error("API_KEY is required")
		os.Exit(1)
	}
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
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

	h := &handler{cfg: cfg, st: st, bus: bus, log: log}

	mux := http.NewServeMux()
	httpx.Health(mux, func(ctx context.Context) error { return st.Ready(ctx) })

	api := http.NewServeMux()
	api.HandleFunc("GET /v1/me", h.me)
	api.HandleFunc("PATCH /v1/me", h.patchMe)
	api.HandleFunc("GET /v1/days", h.days)
	api.HandleFunc("GET /v1/days/{day}", h.day)
	api.HandleFunc("GET /v1/workouts", h.workouts)
	api.HandleFunc("POST /v1/recompute", h.recompute)
	mux.Handle("/v1/", httpx.BearerAuth(cfg.APIKey, api))

	if err := httpx.Serve(log, ":"+cfg.Port, httpx.Logging(log, mux)); err != nil {
		log.Error("serve", "err", err)
		os.Exit(1)
	}
}

type handler struct {
	cfg config.Config
	st  *store.Store
	bus *events.Bus
	log *slog.Logger
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	u, _, err := h.st.GetUser(r.Context(), h.cfg.UserID)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	httpx.WriteJSON(w, 200, u)
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
	if err := h.st.UpdateUser(r.Context(), h.cfg.UserID, p); err != nil {
		httpx.WriteError(w, 422, err.Error())
		return
	}
	h.me(w, r)
}

// dateRange parses ?from=&to= (YYYY-MM-DD) with a default of the last 30 days.
func (h *handler) dateRange(r *http.Request) (string, string, error) {
	_, loc, err := h.st.GetUser(r.Context(), h.cfg.UserID)
	if err != nil {
		return "", "", err
	}
	now := time.Now().In(loc)
	from, to := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	if to == "" {
		to = now.Format("2006-01-02")
	}
	if from == "" {
		from = now.AddDate(0, 0, -29).Format("2006-01-02")
	}
	for _, d := range []string{from, to} {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return "", "", err
		}
	}
	return from, to, nil
}

func (h *handler) days(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		httpx.WriteError(w, 400, "from/to must be YYYY-MM-DD")
		return
	}
	rows, err := h.st.DailyScores(r.Context(), h.cfg.UserID, from, to)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []model.DailyScore{}
	}
	httpx.WriteJSON(w, 200, rows)
}

func (h *handler) day(w http.ResponseWriter, r *http.Request) {
	day := r.PathValue("day")
	_, loc, err := h.st.GetUser(r.Context(), h.cfg.UserID)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		httpx.WriteError(w, 400, "day must be YYYY-MM-DD")
		return
	}
	scores, err := h.st.DailyScores(r.Context(), h.cfg.UserID, day, day)
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	workouts, err := h.st.WorkoutsBetween(r.Context(), h.cfg.UserID, d, d.AddDate(0, 0, 1))
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	if workouts == nil {
		workouts = []model.Workout{}
	}
	out := model.DayDetail{Workouts: workouts, Score: model.DailyScore{Day: day, Details: map[string]any{}}}
	if len(scores) > 0 {
		out.Score = scores[0]
	}
	httpx.WriteJSON(w, 200, out)
}

func (h *handler) workouts(w http.ResponseWriter, r *http.Request) {
	from, to, err := h.dateRange(r)
	if err != nil {
		httpx.WriteError(w, 400, "from/to must be YYYY-MM-DD")
		return
	}
	_, loc, _ := h.st.GetUser(r.Context(), h.cfg.UserID)
	a, _ := time.ParseInLocation("2006-01-02", from, loc)
	b, _ := time.ParseInLocation("2006-01-02", to, loc)
	rows, err := h.st.WorkoutsBetween(r.Context(), h.cfg.UserID, a, b.AddDate(0, 0, 1))
	if err != nil {
		httpx.WriteError(w, 500, err.Error())
		return
	}
	if rows == nil {
		rows = []model.Workout{}
	}
	httpx.WriteJSON(w, 200, rows)
}

// recompute asks scoring (via NATS) to rebuild a date range, oldest first.
// Use it after a big HealthKit backfill or after changing max_hr / sleep base.
func (h *handler) recompute(w http.ResponseWriter, r *http.Request) {
	var req model.RecomputeRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, 400, err.Error())
		return
	}
	for _, d := range []string{req.From, req.To} {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			httpx.WriteError(w, 422, "from/to must be YYYY-MM-DD")
			return
		}
	}
	if req.From > req.To {
		httpx.WriteError(w, 422, "from must be <= to")
		return
	}
	ev := model.IngestedEvent{UserID: h.cfg.UserID, Days: []string{req.From, req.To}, Reason: "recompute"}
	if err := h.bus.PublishIngested(r.Context(), ev); err != nil {
		httpx.WriteError(w, 503, "queue unavailable: "+err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "from": req.From, "to": req.To})
}
