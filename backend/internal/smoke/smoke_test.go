package smoke

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// fake is Supabase, the api and ingest in one server; scoring "rescores" the day
// after polls polls.
type fake struct {
	mu         sync.Mutex
	polls      int
	scoreAfter int
	model      string
	scored     string // the model the day is scored with
	beta       bool
	consent    string
	consented  []string
	synced     int
	ready      map[string]int // path → status
}

func (f *fake) handler(t *testing.T, now time.Time) http.Handler {
	user := uuid.MustParse("671779e8-0000-4000-8000-000000000001")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/v1/token", func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Query().Get("grant_type") != "password" || r.Header.Get("apikey") != "anon" || in["password"] != "pw" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
	})
	authed := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			fn(w, r)
		}
	}
	mux.HandleFunc("GET /readyz/{svc}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if st := f.ready[r.PathValue("svc")]; st != 0 {
			w.WriteHeader(st)
		}
	})
	mux.HandleFunc("GET /v1/me", authed(func(w http.ResponseWriter, r *http.Request) {
		u := model.User{ID: user, TZ: "Europe/Berlin", BetaAccess: f.beta}
		if f.consent != "" {
			u.Consent = &model.Consent{PolicyVersion: f.consent}
		}
		_ = json.NewEncoder(w).Encode(u)
	}))
	mux.HandleFunc("GET /v1/days", authed(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // the beta gate on, for a user without access
	}))
	mux.HandleFunc("GET /v1/config", authed(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.RemoteConfig{PrivacyPolicyVersion: "2026-10-20"})
	}))
	mux.HandleFunc("POST /v1/me/consent", authed(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.consented = append(f.consented, in["policy_version"])
		f.consent = in["policy_version"]
	}))
	mux.HandleFunc("GET /v1/model", authed(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(model.ModelInfo{Current: f.model})
	}))
	mux.HandleFunc("POST /v1/sync", authed(func(w http.ResponseWriter, r *http.Request) {
		var in model.SyncRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || len(in.Samples) != 1 || in.DeviceID != DeviceID {
			http.Error(w, `{"error":"bad sync"}`, http.StatusUnprocessableEntity)
			return
		}
		f.synced++
		day := in.Samples[0].Start.In(time.FixedZone("CEST", 2*3600)).Format("2006-01-02")
		_ = json.NewEncoder(w).Encode(model.SyncResponse{Days: []string{day}})
	}))
	mux.HandleFunc("GET /v1/days/{day}", authed(func(w http.ResponseWriter, r *http.Request) {
		f.polls++
		d := model.DayDetail{Score: model.DailyScore{Day: r.PathValue("day"), ComputedAt: now.Add(-time.Hour)}}
		if f.polls > f.scoreAfter {
			m := f.scored
			d.Score.ComputedAt, d.Score.ModelVersion = now.Add(3*time.Second), &m
		}
		_ = json.NewEncoder(w).Encode(d)
	}))
	return mux
}

func run(t *testing.T, f *fake, mod func(*Config)) (Result, error) {
	t.Helper()
	now := time.Date(2026, 10, 21, 22, 30, 0, 0, time.UTC) // 00:30 in Berlin: the next local day
	srv := httptest.NewServer(f.handler(t, now))
	t.Cleanup(srv.Close)
	c := Config{APIURL: srv.URL, IngestURL: srv.URL, Health: []string{srv.URL + "/readyz/api", srv.URL + "/readyz/scoring"},
		SupabaseURL: srv.URL, AnonKey: "anon", Email: "smoke@ryftapp.de", Password: "pw",
		Wait: 300 * time.Millisecond, Poll: 10 * time.Millisecond, ReadyWait: 100 * time.Millisecond, Now: func() time.Time { return now }}
	if mod != nil {
		mod(&c)
	}
	return Run(context.Background(), c)
}

func TestSmokePasses(t *testing.T) {
	f := &fake{scoreAfter: 3, model: "2026.10.1", scored: "2026.10.1", beta: true}
	res, err := run(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Day != "2026-10-22" || res.Model != "2026.10.1" || res.ScoredAfter != "3s" || res.Policy != "2026-10-20" {
		t.Fatalf("result %+v: the day is the user's local day, the model the api's", res)
	}
	if len(f.consented) != 1 || f.synced != 1 || f.polls != 4 {
		t.Fatalf("consented %v (once, it had none), synced %d, polls %d", f.consented, f.synced, f.polls)
	}
	// Consent already current: not posted again.
	f2 := &fake{model: "m", scored: "m", beta: true, consent: "2026-10-20"}
	if _, err := run(t, f2, nil); err != nil || len(f2.consented) != 0 {
		t.Fatalf("err %v, consented %v", err, f2.consented)
	}
}

func TestSmokeFails(t *testing.T) {
	for name, c := range map[string]struct {
		f    *fake
		mod  func(*Config)
		want string
	}{
		"another model":  {&fake{model: "2026.10.1", scored: "2026.09.1", beta: true}, nil, `scored with model "2026.09.1", the api serves "2026.10.1"`},
		"never rescored": {&fake{scoreAfter: 1 << 30, model: "m", scored: "m", beta: true}, nil, "was not rescored within 300ms"},
		"gate on":        {&fake{model: "m", scored: "m"}, nil, "BETA_GATE is on and the smoke user"},
		"bad password":   {&fake{model: "m", scored: "m", beta: true}, func(c *Config) { c.Password = "nope" }, "sign-in of the smoke user (SMOKE_EMAIL): 400"},
		"not ready":      {&fake{model: "m", scored: "m", beta: true, ready: map[string]int{"scoring": 503}}, nil, "/readyz/scoring: 503, want 200"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := run(t, c.f, c.mod)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want %q", err, c.want)
			}
			// The job logs the error, and no email goes into a log.
			if strings.Contains(err.Error(), "@") {
				t.Fatalf("the error names an email address: %v", err)
			}
		})
	}
}

func TestSmokeWithAToken(t *testing.T) {
	f := &fake{model: "m", scored: "m", beta: true}
	if _, err := run(t, f, func(c *Config) { c.Token, c.Password = "tok", "" }); err != nil {
		t.Fatalf("a bearer instead of the sign-in: %v", err)
	}
}
