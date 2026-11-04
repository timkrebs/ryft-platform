package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReadyzShape(t *testing.T) {
	authOK := func(context.Context) error { return nil }
	amqpDown := errors.New("amqp not connected for 3m0s")
	for _, tc := range []struct {
		name           string
		brokerRequired bool
		auth           func(context.Context) error
		broker, db     error
		code           int
		body           string
	}{
		{name: "all ok", auth: authOK, code: 200,
			body: `{"status":"ready","checks":{"auth":"ok","broker":"ok","db":"ok"}}`},
		{name: "a required check is down", auth: func(context.Context) error { return errors.New("no JWKS keys loaded") }, code: 503,
			body: `{"status":"unready","error":"auth: no JWKS keys loaded","checks":{"auth":"down: no JWKS keys loaded","broker":"ok","db":"ok"}}`},
		{name: "the broker is shown, not required (api, ingest)", auth: authOK, broker: amqpDown, code: 200,
			body: `{"status":"ready","checks":{"auth":"ok","broker":"down: amqp not connected for 3m0s","db":"ok"}}`},
		{name: "the broker is required (scoring, notify)", brokerRequired: true, auth: authOK, broker: amqpDown, code: 503,
			body: `{"status":"unready","error":"broker: amqp not connected for 3m0s","checks":{"auth":"ok","broker":"down: amqp not connected for 3m0s","db":"ok"}}`},
		{name: "the first failing required check is named", brokerRequired: true, auth: authOK, broker: amqpDown, db: errors.New("closed pool"), code: 503,
			body: `{"status":"unready","error":"db: closed pool","checks":{"auth":"ok","broker":"down: amqp not connected for 3m0s","db":"down: closed pool"}}`},
		{name: "a check slower than the deadline", auth: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, code: 503,
			body: `{"status":"unready","error":"auth: timed out after 100ms","checks":{"auth":"down: timed out after 100ms","broker":"ok","db":"ok"}}`},
		{name: "a stale dependency still serves", auth: func(context.Context) error { return Stale(3 * time.Minute) }, code: 200,
			body: `{"status":"ready","checks":{"auth":"stale 3m","broker":"ok","db":"ok"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newRig()
			g.e.readyTimeout = 100 * time.Millisecond
			g.bus.ready = func(context.Context) error { return tc.broker }
			g.e.ping = func(context.Context, *pgxpool.Pool) error { return tc.db }
			g.parts.Checks = []Check{{Name: "auth", Required: true, Fn: tc.auth}}
			s := g.spec()
			s.BrokerRequired = tc.brokerRequired
			ctx, cancel := context.WithCancel(context.Background())
			done := g.serve(t, ctx, s)
			code, body := g.get(t, "/readyz")
			if code != tc.code || body != tc.body {
				t.Errorf("/readyz = %d %s\nwant     %d %s", code, body, tc.code, tc.body)
			}
			if code, body := g.get(t, "/healthz"); code != 200 || body != `{"status":"ok"}` {
				t.Errorf("/healthz = %d %s: liveness does not depend on the checks", code, body)
			}
			cancel()
			if err := result(t, done); err != nil {
				t.Error(err)
			}
		})
	}
}

// Behind Instrument the answer carries the request id, as every error body does.
func TestReadyzOnTheEdge(t *testing.T) {
	for _, edge := range []bool{true, false} {
		g := newRig()
		s := g.spec()
		s.Edge = edge
		ctx, cancel := context.WithCancel(context.Background())
		done := g.serve(t, ctx, s)
		_, header, body := g.do(t, "/readyz")
		var got struct {
			Status    string `json:"status"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil || got.Status != "ready" {
			t.Fatalf("edge=%v: %s (%v)", edge, body, err)
		}
		if edge != (got.RequestID != "" && got.RequestID == header.Get("X-Request-ID")) {
			t.Errorf("edge=%v: request_id %q, header %q", edge, got.RequestID, header.Get("X-Request-ID"))
		}
		if edge != (header.Get("Cache-Control") == "no-store") {
			t.Errorf("edge=%v: the security headers are %v", edge, header)
		}
		cancel()
		if err := result(t, done); err != nil {
			t.Error(err)
		}
	}
}

// The routes mount next to the health endpoints, and unknown paths answer the API's
// JSON 404.
func TestRoutesAndNotFound(t *testing.T) {
	g := newRig()
	g.parts.Routes = func(mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) })
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, g.spec())
	if code, body := g.get(t, "/v1/ping"); code != 200 || body != "pong" {
		t.Errorf("/v1/ping = %d %s", code, body)
	}
	if code, body := g.get(t, "/nope"); code != 404 || !strings.Contains(body, `"error":"not found"`) {
		t.Errorf("/nope = %d %s", code, body)
	}
	cancel()
	if err := result(t, done); err != nil {
		t.Error(err)
	}
}

// During the drain delay the service still serves; /readyz says draining (503) and
// /healthz stays 200 so liveness never fires on a pod that is leaving.
func TestDrainKeepsServing(t *testing.T) {
	g := newRig()
	g.parts.Routes = func(mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/ping", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("pong")) })
	}
	s := g.spec()
	s.DrainDelay = 400 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := g.serve(t, ctx, s)
	if code, _ := g.get(t, "/readyz"); code != 200 {
		t.Fatalf("/readyz before the signal = %d", code)
	}
	cancel()
	want := `{"status":"draining","error":"draining","checks":{"broker":"ok","db":"ok"}}`
	eventually(t, func() bool {
		code, body := g.get(t, "/readyz")
		return code == 503 && body == want
	}, "/readyz to say draining")
	if code, body := g.get(t, "/healthz"); code != 200 || body != `{"status":"ok"}` {
		t.Errorf("/healthz while draining = %d %s", code, body)
	}
	if code, body := g.get(t, "/v1/ping"); code != 200 || body != "pong" {
		t.Errorf("a new request during the drain = %d %s", code, body)
	}
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestStaleText(t *testing.T) {
	for age, want := range map[time.Duration]string{
		0: "stale 0s", 59 * time.Second: "stale 59s", 3 * time.Minute: "stale 3m", 90 * time.Second: "stale 1m",
		59 * time.Minute: "stale 59m", 2 * time.Hour: "stale 2h",
	} {
		if got := Stale(age).Error(); got != want {
			t.Errorf("Stale(%v) = %q, want %q", age, got, want)
		}
	}
}

func TestReasonIsShortAndOneLine(t *testing.T) {
	got := reason(errors.New("dial tcp 10.0.0.1:5671:\n\tconnection refused  " + strings.Repeat("x", 200)))
	if strings.ContainsAny(got, "\n\t") || strings.Contains(got, "  ") || len([]rune(got)) != maxReason+3 {
		t.Errorf("reason = %q (%d runes)", got, len([]rune(got)))
	}
}
