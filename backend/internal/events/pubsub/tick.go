package pubsub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
)

// Tick is one step of a service's tick (D219): what a timer did before Cloud Run
// (the db ping, the outbox sweep, scoring's pass, notify's stale check).
type Tick struct {
	Name string // a closed set, written in code: the log names the step
	Run  func(ctx context.Context) error
}

// TickOptions configure a service's tick handler.
type TickOptions struct {
	Service  string            // the ryft_ticks_total label: api, ingest, scoring or notify
	Verifier auth.PushVerifier // the tick's token is the push service account's, as on a push
	Budget   time.Duration     // for all steps together (api, ingest, notify 45 s; scoring 240 s)
	Log      *slog.Logger
	// Finally runs after the tick is counted and before it is answered, whatever the
	// steps did, bounded by FinallyBudget and the server's hard stop (httpx.Detach), not
	// by the caller's cancel: the hook for the metrics push (A2), so the push carries
	// this tick's ryft_ticks_total. Its error is logged. nil: nothing.
	Finally       func(ctx context.Context) error
	FinallyBudget time.Duration // default 10 s
}

// tickHandler is POST /internal/tick.
type tickHandler struct {
	o       TickOptions
	ticks   []Tick
	running atomic.Bool
}

// NewTick is the service's POST /internal/tick: it checks the token, then runs ticks in
// order within the budget and answers 204 whatever a step did (a step's error is
// logged; a failed tick is not retried, the next comes in 15 minutes). A tick that
// arrives while one runs in this process is answered 204 at once and counted as
// overlap. The envelope's ryft-key must be absent or "tick", and its messageId Pub/Sub's
// (else 400): both are logged, the key cut to maxLogKey bytes.
func NewTick(o TickOptions, ticks ...Tick) (http.Handler, error) {
	switch {
	case o.Service == "" || o.Log == nil:
		return nil, errors.New("pubsub: a tick handler needs its service and a logger")
	case o.Verifier == nil:
		return nil, errors.New("pubsub: a tick handler needs a verifier")
	case o.Budget <= 0:
		return nil, errors.New("pubsub: a tick handler needs a budget")
	}
	for _, t := range ticks {
		if t.Name == "" || t.Run == nil {
			return nil, errors.New("pubsub: every tick step needs a name and a Run")
		}
	}
	if o.FinallyBudget <= 0 {
		o.FinallyBudget = 10 * time.Second
	}
	return &tickHandler{o: o, ticks: append([]Tick(nil), ticks...)}, nil
}

func (h *tickHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !verified(w, r, h.o.Verifier, h.o.Log, PathTick) {
		return
	}
	env, status := readEnvelope(w, r)
	if status != 0 {
		h.o.Log.WarnContext(r.Context(), "tick: not a push envelope", "status", status)
		w.WriteHeader(status)
		return
	}
	if k, ok := env.Message.Attributes[AttrKey]; ok && k != KeyTick {
		h.o.Log.WarnContext(r.Context(), "tick: not a tick", "message_id", env.Message.MessageID, "key", clip(k, maxLogKey))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !h.running.CompareAndSwap(false, true) {
		metrics.Ticks.WithLabelValues(h.o.Service, "overlap").Inc()
		h.o.Log.InfoContext(r.Context(), "tick: the previous one still runs, skipping this one", "message_id", env.Message.MessageID)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	defer h.running.Store(false)
	start := time.Now()
	result := h.run(r.Context())
	metrics.Ticks.WithLabelValues(h.o.Service, result).Inc()
	h.o.Log.InfoContext(r.Context(), "tick", "result", result, "message_id", env.Message.MessageID,
		"took", time.Since(start).Round(time.Millisecond).String())
	if h.o.Finally != nil {
		// Past the caller's cancel (the push carries this tick's count either way), not past
		// the server's hard stop: a push still running when the shutdown's http step runs out
		// must let go of the pusher before the shutdown's own push (httpx.Detach).
		ctx, cancel := httpx.Detach(r.Context(), h.o.FinallyBudget)
		if err := h.step(ctx, Tick{Name: "finally", Run: h.o.Finally}); err != nil {
			h.o.Log.ErrorContext(ctx, "tick: the final step failed", "err", err)
		}
		cancel()
	}
	// Answered last: with request-based CPU nothing runs after the response.
	w.WriteHeader(http.StatusNoContent)
}

// run runs every step in order within the budget: ok when all succeeded, error when
// one failed or the budget ended before a step began (that step is skipped, logged).
func (h *tickHandler) run(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, h.o.Budget)
	defer cancel()
	result := "ok"
	for _, t := range h.ticks {
		if err := ctx.Err(); err != nil {
			h.o.Log.ErrorContext(ctx, "tick: the budget is spent, step skipped", "step", t.Name, "budget", h.o.Budget.String())
			result = "error"
			continue
		}
		start := time.Now()
		if err := h.step(ctx, t); err != nil {
			h.o.Log.ErrorContext(ctx, "tick: step failed", "step", t.Name, "err", err,
				"took", time.Since(start).Round(time.Millisecond).String())
			result = "error"
		}
	}
	return result
}

// step runs one step and turns a panic into its error: it is counted as a handler panic
// of POST /internal/tick (ryft_http_panics_total), and the next step still runs.
func (h *tickHandler) step(ctx context.Context, t Tick) (err error) {
	defer func() {
		if p := recover(); p != nil {
			metrics.Panics.WithLabelValues(http.MethodPost + " " + PathTick).Inc()
			h.o.Log.ErrorContext(ctx, "panic", "step", t.Name, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			err = fmt.Errorf("tick step %s panicked: %v", t.Name, p)
		}
	}()
	return t.Run(ctx)
}
