package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/appattest"
	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Under APP_ATTEST=on ingest refuses POST /v1/sync without a valid app token (403
// app_attest_required, counted for service ingest, one warn line without the account) and lets it
// through with one, or for an exempt account. No route of ingest is exempt. The store is a closed
// pool: a request that passes the check fails later, never with app_attest_required.
func TestSyncNeedsAnAppTokenUnderOn(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://ingest@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	ring, err := appattest.ParseRing(appattesttest.RingLine(5, 0xbeef))
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("k", 32) // low entropy on purpose: gitleaks scans the history
	user, exempt := uuid.New(), uuid.New()
	var logs bytes.Buffer
	log := logx.NewWriter(&logs, "ingest", slog.LevelDebug)
	h := &handler{st: store.New(pool), log: log, beta: config.Beta{Gate: config.ModeOff, Consent: config.ModeOff, PrivacyPolicyVersion: "2026-10-26"},
		attest: config.AppAttest{Mode: config.ModeOn, Ring: ring, ExemptUsers: []uuid.UUID{exempt}},
		loadUser: func(_ context.Context, p auth.Principal) (model.User, error) {
			return model.User{ID: p.UserID, TZ: "UTC", BetaAccess: true}, nil
		}}
	mux := newMux(h, log, auth.NewAPIKey(key, uuid.New()), http.NotFoundHandler())
	sync := func(as uuid.UUID, token string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(`{"device_id":"`+uuid.NewString()+`","samples":[],"sleep":[],"workouts":[]}`))
		req.Header.Set("Authorization", "Bearer "+key+":"+as.String())
		if token != "" {
			req.Header.Set(model.HeaderAppToken, token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var body struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Code
	}

	before := testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues("missing", "ingest"))
	if status, code := sync(user, ""); status != http.StatusForbidden || code != model.CodeAppAttestRequired {
		t.Fatalf("no token: %d %q", status, code)
	}
	if got := testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues("missing", "ingest")) - before; got != 1 {
		t.Errorf("counted %v for ingest", got)
	}
	tok, _ := ring.Mint(user, [32]byte{7}, time.Now())
	if status, code := sync(user, tok); code == model.CodeAppAttestRequired {
		t.Fatalf("with a token: %d %q", status, code)
	}
	if status, code := sync(uuid.New(), tok); status != http.StatusForbidden || code != model.CodeAppAttestRequired {
		t.Fatalf("another account's token: %d %q", status, code)
	}
	if status, code := sync(exempt, ""); code == model.CodeAppAttestRequired {
		t.Fatalf("an exempt account: %d %q", status, code)
	}
	lines := 0
	for _, l := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(l, `"msg":"app attest"`) {
			continue
		}
		lines++
		if strings.Contains(l, user.String()) || strings.Contains(l, "user_id") || strings.Contains(l, tok[len(tok)-20:]) ||
			!strings.Contains(l, `"route_group":"sync"`) || !strings.Contains(l, `"service":"ingest"`) {
			t.Errorf("warn line: %s", l)
		}
	}
	if lines != 2 {
		t.Fatalf("want two warn lines (missing, other_account):\n%s", logs.String())
	}
}
