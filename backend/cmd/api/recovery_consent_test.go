package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
)

// A consent withdrawn after the request was admitted but before the write commits answers consent_required, as the
// gate does, and stores nothing (D300). Postgres: RYFT_TEST_POSTGRES_URL.

func TestRecoveryWritesRefuseAConsentRevokedAfterAdmission(t *testing.T) {
	var h *handler
	rr := newRecoveryRig(t, func(captured *handler) { h = captured })
	ctx := context.Background()
	user := rr.user()
	rr.flags(nil, []uuid.UUID{user})
	wake := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	workout := uuid.New()
	if _, err := rr.pool.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
		VALUES ($1, $2, 'running', $3, $4, 'Watch')`, workout, user, wake.Add(-20*time.Hour), wake.Add(-19*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// The same chain as the rig's, except that the withdrawal commits right after authentication loaded the user
	// (so the request is admitted on a consent that is gone by the time the write runs).
	revoke := false
	beta := h.beta
	mux := newMux(h, muxOptions{log: h.log, verifier: nutritionVerifier{}, ready: func(context.Context) error { return nil },
		tick: http.NotFoundHandler(), gate: beta.Gate, attest: config.AppAttest{Mode: config.ModeOff}, authMode: config.AuthAPIKey,
		clients: httpx.NewClientKeys(1),
		loadUser: func(ctx context.Context, p auth.Principal) (model.User, error) {
			u, err := ensureUser(rr.st)(ctx, p)
			if err == nil && revoke {
				err = rr.st.RevokeConsent(ctx, p.UserID)
			}
			return u, err
		}})
	srv := httpx.Instrument(h.log, nil, httpx.Recover(httpx.SecurityHeaders(mux)))
	send := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+user.String())
		resp := httptest.NewRecorder()
		srv.ServeHTTP(resp, req)
		return resp
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := rr.pool.QueryRow(ctx, query, user).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	grant := func() {
		t.Helper()
		if err := rr.st.GrantConsent(ctx, user, nutritionPolicy, nil); err != nil {
			t.Fatal(err)
		}
	}

	for _, mode := range []config.Mode{config.ModeOn, config.ModeWarn} { // warn acts as on for these writes
		h.beta.Consent = mode
		for _, c := range []struct {
			name, method, path, table string
			body                      any
		}{
			{"check-in", http.MethodPut, checkins + "2026-11-26", "recovery_checkins", night("2026-11-26", 7, 6)},
			{"session RPE", http.MethodPut, "/v1/workouts/" + workout.String() + "/rpe", "workout_ratings", map[string]any{"session_rpe": 8}},
		} {
			rows := `SELECT count(*) FROM ` + c.table + ` WHERE user_id=$1`
			grant()
			revoke = true
			resp := send(c.method, c.path, c.body)
			revoke = false
			if resp.Code != http.StatusForbidden || errorCode(t, resp.Body.Bytes()) != model.CodeConsentRequired {
				t.Fatalf("%s, consent %s, revoked after admission: %d %s", c.name, mode, resp.Code, resp.Body)
			}
			if n := count(rows); n != 0 {
				t.Fatalf("%s, consent %s: a refused write left %d rows", c.name, mode, n)
			}
			// Consent regranted, nothing withdrawn: the write goes through (a 200 and a row).
			grant()
			if resp := send(c.method, c.path, c.body); resp.Code != http.StatusOK {
				t.Fatalf("%s, consent %s, consented: %d %s", c.name, mode, resp.Code, resp.Body)
			}
			if n := count(rows); n != 1 {
				t.Fatalf("%s, consent %s: %d rows after the write, want 1", c.name, mode, n)
			}
			if _, err := rr.pool.Exec(ctx, `DELETE FROM `+c.table+` WHERE user_id=$1`, user); err != nil {
				t.Fatal(err)
			}
		}
	}
}
