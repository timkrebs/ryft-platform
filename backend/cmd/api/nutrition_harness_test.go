package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/store"
)

const nutritionPolicy = "2026-10-26"

type nutritionVerifier struct{}

func (nutritionVerifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	id, err := uuid.Parse(token)
	if err != nil {
		return auth.Principal{}, auth.Unauthorized("malformed", err)
	}
	return auth.Principal{UserID: id, Email: id.String()[:8] + "@example.com", Provider: "apple"}, nil
}

func (nutritionVerifier) Ready(context.Context) error { return nil }

type nutritionRig struct {
	t    *testing.T
	pool *pgxpool.Pool
	st   *store.Store
	srv  http.Handler
	logs *lockedBuffer
}

func newNutritionRig(t *testing.T, configure ...func(*handler)) *nutritionRig {
	t.Helper()
	pool := testPool(t)
	rig := &nutritionRig{t: t, pool: pool, st: store.New(pool), logs: &lockedBuffer{}}
	log := logx.NewWriter(rig.logs, "api", slog.LevelDebug)
	beta := config.Beta{Gate: config.ModeOff, Consent: config.ModeOn, PrivacyPolicyVersion: nutritionPolicy}
	attest := config.AppAttest{Mode: config.ModeOff}
	h := &handler{st: rig.st, log: log, beta: beta, attestMode: attest.Mode, accounts: &accounts{st: rig.st, log: log}}
	for _, fn := range configure {
		fn(h)
	}
	mux := newMux(h, muxOptions{log: log, verifier: nutritionVerifier{}, ready: func(context.Context) error { return nil },
		tick: http.NotFoundHandler(), gate: beta.Gate, attest: attest, authMode: config.AuthAPIKey, clients: httpx.NewClientKeys(1)})
	rig.srv = httpx.Instrument(log, nil, httpx.Recover(httpx.SecurityHeaders(mux)))
	return rig
}

func (rig *nutritionRig) account(noConsent ...bool) uuid.UUID {
	rig.t.Helper()
	id := uuid.New()
	ctx := context.Background()
	if _, err := rig.st.EnsureUser(ctx, store.Identity{ID: id, Email: "a@example.com", Provider: "apple"}); err != nil {
		rig.t.Fatal(err)
	}
	if len(noConsent) == 0 {
		if err := rig.st.GrantConsent(ctx, id, nutritionPolicy, nil); err != nil {
			rig.t.Fatal(err)
		}
	}
	return id
}

func (rig *nutritionRig) do(user uuid.UUID, method, path string, body any) *httptest.ResponseRecorder {
	rig.t.Helper()
	var payload io.Reader
	switch data := body.(type) {
	case nil:
	case string:
		payload = strings.NewReader(data)
	default:
		raw, err := json.Marshal(data)
		if err != nil {
			rig.t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, payload)
	if user != uuid.Nil {
		req.Header.Set("Authorization", "Bearer "+user.String())
	}
	resp := httptest.NewRecorder()
	rig.srv.ServeHTTP(resp, req)
	return resp
}
