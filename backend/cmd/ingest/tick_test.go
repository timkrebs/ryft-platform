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
	"testing"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/eventbus"
)

// tickEnvelope is a push from the tick topic: data {} and ryft-key=tick.
const tickEnvelope = `{"message":{"data":"e30=","attributes":{"ryft-key":"tick"},"messageId":"78"},"subscription":"projects/ryft-local/subscriptions/tick-ingest","deliveryAttempt":1}`

// refuseAll is a token check that refuses every token: what /v1 does without a valid bearer.
type refuseAll struct{}

func (refuseAll) Verify(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.Unauthorized("invalid", errors.New("no"))
}

func (refuseAll) Ready(context.Context) error { return nil }

// ingest's tick pings the database, then sweeps the outbox, both even when the ping
// fails; it sits beside /v1, outside its auth and per-user limit (D218), and ingest has
// no push routes.
func TestTickAndRoutes(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr, err := eventbus.Open(ctx, config.Config{
		PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: "127.0.0.1:1"},
		Push:   config.Push{AllowInsecure: true},
	}, "ingest", log)
	if err != nil {
		t.Fatal(err)
	}
	var ran []string
	tick, err := tr.Tick(tickBudget, nil, ingestTicks(
		func(context.Context) error { ran = append(ran, "db ping"); return errors.New("pooler unreachable") },
		func(context.Context) error { ran = append(ran, "outbox sweep"); return nil })...)
	if err != nil {
		t.Fatal(err)
	}
	mux := newMux(&handler{log: log}, log, refuseAll{}, tick)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/internal/tick", http.StatusNoContent},
		{http.MethodPost, "/v1/sync", http.StatusUnauthorized},
		{http.MethodPost, "/push/ingested", http.StatusNotFound},
		{http.MethodGet, "/healthz", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tickEnvelope)))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
	if want := []string{"db ping", "outbox sweep"}; !slices.Equal(ran, want) {
		t.Errorf("the tick ran %v, want %v", ran, want)
	}
}
