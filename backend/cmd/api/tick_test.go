package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/metrics"
)

// tickEnvelope is a push from the tick topic: data {} and ryft-key=tick.
const tickEnvelope = `{"message":{"data":"e30=","attributes":{"ryft-key":"tick"},"messageId":"77"},"subscription":"projects/ryft-local/subscriptions/tick-api","deliveryAttempt":1}`

// localTransport is a real transport with compose's local settings: it dials nothing, and its tick checks no
// token.
func localTransport(t *testing.T) *eventbus.Transport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tr, err := eventbus.Open(ctx, config.Config{
		PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: "127.0.0.1:1"},
		Push:   config.Push{AllowInsecure: true},
	}, "api", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// steps records the tick's steps in the order they ran.
type steps struct {
	mu        sync.Mutex
	ran       []string
	reconcile time.Duration // what the reconcile had left of its budget
	purgeErr  error
}

func (s *steps) add(name string) { s.mu.Lock(); s.ran = append(s.ran, name); s.mu.Unlock() }

func (s *steps) list() []string { s.mu.Lock(); defer s.mu.Unlock(); return slices.Clone(s.ran) }

// tickSteps is the api's steps as main wires them, with fakes for the database and the outbox and the real
// confirm guard on a stub of Supabase's settings.
func (s *steps) tickSteps(guard *confirmGuard) tickSteps {
	return tickSteps{
		ping:  func(context.Context) error { s.add("db ping"); return nil },
		sweep: func(context.Context) error { s.add("outbox sweep"); return nil },
		reconcile: func(ctx context.Context) {
			dl, _ := ctx.Deadline()
			s.mu.Lock()
			s.reconcile = time.Until(dl)
			s.mu.Unlock()
			s.add("account reconcile")
		},
		settings: func(ctx context.Context) { s.add("invite settings"); guard.Refresh(ctx) },
		purge:    func(context.Context) error { s.add("invite email purge"); return s.purgeErr },
		attest:   func(context.Context) error { s.add("app attest purge"); return nil },
	}
}

// The api's tick runs its six steps in order (after the db ping and the sweep, the three that were timers and
// the App Attest purge),
// all of them even when one fails, and answers 204. Building it reads nothing from Supabase: the api starts
// without asking (D219), and the tick's "invite settings" is the first read.
func TestTickRunsTheAPISteps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		purgeErr error
		result   string
	}{
		{"all ok", nil, "ok"},
		{"the purge fails, the tick says so", errors.New("closed pool"), "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &stubSettings{}
			guard := newConfirmGuard(src, quietHandler().log)
			s := &steps{purgeErr: tc.purgeErr}
			tick, err := localTransport(t).Tick(tickBudget, nil, s.tickSteps(guard).ticks()...)
			if err != nil {
				t.Fatal(err)
			}
			h := quietHandler()
			h.binding = guard
			mux := newMux(h, muxOptions{log: h.log, verifier: refuseAll{}, ready: func(context.Context) error { return nil }, tick: tick})
			if src.calls != 0 {
				t.Fatalf("the start asked Supabase %d times", src.calls)
			}

			before := testutil.ToFloat64(metrics.Ticks.WithLabelValues("api", tc.result))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/internal/tick", strings.NewReader(tickEnvelope)))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("tick = %d %s", rec.Code, rec.Body)
			}
			want := []string{"db ping", "outbox sweep", "account reconcile", "invite settings", "invite email purge", "app attest purge"}
			if got := s.list(); !slices.Equal(got, want) {
				t.Errorf("steps %v, want %v", got, want)
			}
			if s.reconcile <= reconcileBudget-5*time.Second || s.reconcile > reconcileBudget {
				t.Errorf("the reconcile had %v, its budget is %v", s.reconcile, reconcileBudget)
			}
			if src.calls != 1 || guard.State() != bindOK {
				t.Errorf("the tick read the settings %d times, state %s", src.calls, guard.State())
			}
			if got := testutil.ToFloat64(metrics.Ticks.WithLabelValues("api", tc.result)) - before; got != 1 {
				t.Errorf("ryft_ticks_total{api,%s} rose by %v", tc.result, got)
			}
		})
	}
}

// The reconcile step fails when its share of the budget ended it, so the tick shows the deletions left over.
func TestReconcileStepReportsItsBudget(t *testing.T) {
	st := tickSteps{reconcile: func(ctx context.Context) { <-ctx.Done() }}.ticks()
	var step eventbus.Tick
	for _, s := range st {
		if s.Name == "account reconcile" {
			step = s
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := step.Run(ctx); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "next tick") {
		t.Fatalf("a reconcile the budget ended = %v", err)
	}
}

// refuseAll is a token check that refuses every token: what /v1 does without a valid bearer.
type refuseAll struct{}

func (refuseAll) Verify(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.Unauthorized("invalid", errors.New("no"))
}

func (refuseAll) Ready(context.Context) error { return nil }

// The tick sits beside /v1, outside its auth and its per-user limit (D218): it is reached without a user's
// bearer (the transport's verifier checks the push token), while /v1 refuses the same request. The api has
// no push routes: /push/* is the JSON 404.
func TestMuxPutsTheTickOutsideV1(t *testing.T) {
	var ticked int
	h := quietHandler()
	mux := newMux(h, muxOptions{log: h.log, verifier: refuseAll{}, ready: func(context.Context) error { return nil },
		tick: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { ticked++; w.WriteHeader(http.StatusNoContent) })})
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/internal/tick", http.StatusNoContent},
		{http.MethodGet, "/v1/me", http.StatusUnauthorized},
		{http.MethodPost, "/v1/recompute", http.StatusUnauthorized},
		{http.MethodPost, "/push/ingested", http.StatusNotFound},
		{http.MethodPost, "/push/scored", http.StatusNotFound},
		{http.MethodGet, "/healthz", http.StatusOK},
		{http.MethodGet, "/readyz", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tickEnvelope)))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	if ticked != 1 {
		t.Errorf("the tick ran %d times, want 1", ticked)
	}
}
