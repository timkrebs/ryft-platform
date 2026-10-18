// ingest accepts HealthKit batches from the iOS app, stores them idempotently
// and tells scoring which local days changed.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func main() {
	log := httpx.Logger("ingest")
	cfg, err := config.Load("ingest")
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

	bus, err := events.Connect(ctx, cfg.NatsURL, "ingest")
	if err != nil {
		log.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	h := &handler{cfg: cfg, st: st, bus: bus, log: log}

	mux := http.NewServeMux()
	httpx.Health(mux, func(ctx context.Context) error {
		if err := st.Ready(ctx); err != nil {
			return err
		}
		return bus.Ready(ctx)
	})
	api := http.NewServeMux()
	api.HandleFunc("POST /v1/sync", h.sync)
	mux.Handle("/v1/", httpx.BearerAuth(cfg.APIKey, api))

	if err := httpx.Serve(log, ":"+cfg.Port, httpx.Instrument(log, mux)); err != nil {
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

func (h *handler) sync(w http.ResponseWriter, r *http.Request) {
	var req model.SyncRequest
	if err := httpx.ReadJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if err := validate(&req); err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	ctx := r.Context()
	userID := h.cfg.UserID

	_, loc, err := h.st.GetUser(ctx, userID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "user: "+err.Error())
		return
	}
	if err := h.st.TouchDevice(ctx, userID, req.DeviceID, req.DeviceName); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "device: "+err.Error())
		return
	}

	ns, err := h.st.UpsertSamples(ctx, userID, req.Samples)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "samples: "+err.Error())
		return
	}
	nsl, err := h.st.UpsertSleep(ctx, userID, req.Sleep)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "sleep: "+err.Error())
		return
	}
	nw, err := h.st.UpsertWorkouts(ctx, userID, req.Workouts)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "workouts: "+err.Error())
		return
	}

	days := touchedDays(&req, loc)
	if len(days) > 0 {
		if err := h.bus.PublishIngested(ctx, model.IngestedEvent{UserID: userID, Days: days, Reason: "sync"}); err != nil {
			// Data is stored; scoring's periodic pass will catch up. Report, don't fail the sync.
			h.log.Error("publish ingested event", "err", err)
		}
	}
	countIngested(&req)
	h.log.Info("sync", "device", req.DeviceID, "samples", ns, "sleep", nsl, "workouts", nw, "days", len(days))
	httpx.WriteJSON(w, http.StatusOK, model.SyncResponse{Samples: ns, Sleep: nsl, Workouts: nw, Days: days})
}

func validate(req *model.SyncRequest) error {
	if req.DeviceID == uuid.Nil {
		return fmt.Errorf("device_id is required")
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
	}
	for i := range req.Sleep {
		s := &req.Sleep[i]
		if !model.KnownSleepStages[s.Stage] {
			return fmt.Errorf("sleep[%d]: unknown stage %q", i, s.Stage)
		}
		if !s.End.After(s.Start) {
			return fmt.Errorf("sleep[%d]: end must be after start", i)
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
	metrics.LastSync.WithLabelValues(req.DeviceID.String()).SetToCurrentTime()
}
