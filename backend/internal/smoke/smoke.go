// Package smoke is the production smoke after a deploy, from inside the cluster:
// Argo CD runs it as the PostSync hook of the ryft-platform application
// (`/app smoke` in the migrate image, namespace ryft-smoke; D95). It checks that
// every service is ready, signs the dedicated smoke user in, sends one tiny sync and
// waits until scoring has rescored that day with the model the api serves, which
// proves api, ingest, the broker and scoring end to end. It changes nothing but the
// smoke user's step count for today (and gives it the current consent).
//
// scripts/smoke_prod.sh does the same through the public edge and also probes the
// gates; that one stays the operator's check from outside.
package smoke

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// DeviceID is the smoke user's one device (the same as scripts/smoke_prod.sh).
var DeviceID = uuid.MustParse("5e0c0a5e-0000-4000-8000-0000000000aa")

type Config struct {
	APIURL    string   // e.g. http://api.ryft-api.svc.cluster.local:8080
	IngestURL string   // e.g. http://ingest.ryft-ingest.svc.cluster.local:8080
	Health    []string // readiness URLs of every service, each must answer 200

	// Either Token (a bearer, e.g. the local stack's API key) or a Supabase
	// password sign-in of the dedicated smoke user.
	Token                string
	SupabaseURL, AnonKey string
	Email, Password      string
	Wait                 time.Duration // for the day to be rescored; default 60 s
	Poll                 time.Duration // default 1 s
	ReadyWait            time.Duration // for each health URL; default 1 min
	HTTP                 *http.Client
	Log                  *slog.Logger
	Now                  func() time.Time
}

// Result is what a passing run saw.
type Result struct {
	UserID      uuid.UUID `json:"user_id"`
	Day         string    `json:"day"`
	Model       string    `json:"model"`
	Policy      string    `json:"policy"`
	ScoredAfter string    `json:"scored_after"` // from the sync to the score
}

// Run is one smoke; any failure is an error naming the step.
func Run(ctx context.Context, c Config) (Result, error) {
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 15 * time.Second}
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Wait <= 0 {
		c.Wait = 60 * time.Second
	}
	if c.Poll <= 0 {
		c.Poll = time.Second
	}
	if c.ReadyWait <= 0 {
		c.ReadyWait = time.Minute
	}
	var res Result
	for _, u := range c.Health {
		if err := ready(ctx, c, u); err != nil {
			return res, err
		}
	}
	token := c.Token
	if token == "" {
		t, err := signIn(ctx, c)
		if err != nil {
			// Not the address: the job logs this error, and no email goes into a log.
			return res, fmt.Errorf("sign-in of the smoke user (SMOKE_EMAIL): %w", err)
		}
		token = t
	}
	cl := client{http: c.HTTP, token: token}

	var me model.User
	if err := cl.do(ctx, http.MethodGet, c.APIURL+"/v1/me", nil, &me); err != nil {
		return res, fmt.Errorf("GET /v1/me: %w", err)
	}
	res.UserID = me.ID
	if !me.BetaAccess {
		// Fine while BETA_GATE is off; with it on the smoke user needs an operator
		// grant (scripts/user.sh <id> --grant).
		if st, _ := cl.status(ctx, http.MethodGet, c.APIURL+"/v1/days"); st == http.StatusForbidden {
			return res, fmt.Errorf("BETA_GATE is on and the smoke user %s has no beta access: scripts/user.sh %s --grant", me.ID, me.ID.String()[:8])
		}
		c.Log.Warn("the smoke user has no beta access: fine while BETA_GATE is off", "user_id", me.ID)
	}
	var conf model.RemoteConfig
	if err := cl.do(ctx, http.MethodGet, c.APIURL+"/v1/config", nil, &conf); err != nil {
		return res, fmt.Errorf("GET /v1/config: %w", err)
	}
	res.Policy = conf.PrivacyPolicyVersion
	if me.Consent == nil || me.Consent.PolicyVersion != conf.PrivacyPolicyVersion {
		body := map[string]string{"policy_version": conf.PrivacyPolicyVersion, "app_version": "smoke-hook"}
		if err := cl.do(ctx, http.MethodPost, c.APIURL+"/v1/me/consent", body, nil); err != nil {
			return res, fmt.Errorf("POST /v1/me/consent: %w", err)
		}
	}
	var info model.ModelInfo
	if err := cl.do(ctx, http.MethodGet, c.APIURL+"/v1/model", nil, &info); err != nil {
		return res, fmt.Errorf("GET /v1/model: %w", err)
	}
	res.Model = info.Current

	loc, err := time.LoadLocation(me.TZ)
	if err != nil {
		loc = time.UTC
	}
	now := c.Now().UTC().Truncate(time.Second)
	res.Day = now.In(loc).Format("2006-01-02")
	app := "smoke-hook"
	sync := model.SyncRequest{DeviceID: DeviceID, DeviceName: "smoke-hook", AppVersion: &app,
		Samples: []model.Sample{{Type: model.SampleSteps, Start: now, End: now, Value: 1, Unit: "count", Source: "smoke-hook"}},
		Sleep:   []model.SleepSegment{}, Workouts: []model.Workout{}}
	var synced model.SyncResponse
	if err := cl.do(ctx, http.MethodPost, c.IngestURL+"/v1/sync", sync, &synced); err != nil {
		return res, fmt.Errorf("POST /v1/sync: %w", err)
	}
	if !slices.Contains(synced.Days, res.Day) {
		return res, fmt.Errorf("the sync did not touch %s: %v", res.Day, synced.Days)
	}

	deadline := time.Now().Add(c.Wait)
	for {
		var day model.DayDetail
		if err := cl.do(ctx, http.MethodGet, c.APIURL+"/v1/days/"+res.Day, nil, &day); err != nil {
			return res, fmt.Errorf("GET /v1/days/%s: %w", res.Day, err)
		}
		if !day.Score.ComputedAt.Before(now) {
			got := ""
			if day.Score.ModelVersion != nil {
				got = *day.Score.ModelVersion
			}
			if got != res.Model {
				return res, fmt.Errorf("%s scored with model %q, the api serves %q", res.Day, got, res.Model)
			}
			res.ScoredAfter = day.Score.ComputedAt.Sub(now).Round(time.Second).String()
			return res, nil
		}
		if time.Now().After(deadline) {
			return res, fmt.Errorf("%s was not rescored within %s of the sync (scoring, the broker: docs/OPERATIONS.md → Dead letters)", res.Day, c.Wait)
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(c.Poll):
		}
	}
}

// ready waits up to ReadyWait for url to answer 200 (Argo runs PostSync once the
// application is healthy, so this is normally the first try).
func ready(ctx context.Context, c Config, url string) error {
	deadline := time.Now().Add(c.ReadyWait)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := c.HTTP.Do(req)
		status := 0
		if err == nil {
			status = resp.StatusCode
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if status == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("%s: %w", url, err)
			}
			return fmt.Errorf("%s: %d, want 200", url, status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(min(2*time.Second, c.ReadyWait/4)):
		}
	}
}

// signIn is GoTrue's password grant for the smoke user.
func signIn(ctx context.Context, c Config) (string, error) {
	if c.SupabaseURL == "" || c.AnonKey == "" || c.Email == "" || c.Password == "" {
		return "", errors.New("set SUPABASE_URL (or SUPABASE_PROJECT_REF), SUPABASE_ANON_KEY, SMOKE_EMAIL and SMOKE_PASSWORD, or SMOKE_TOKEN")
	}
	body, _ := json.Marshal(map[string]string{"email": c.Email, "password": c.Password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.SupabaseURL, "/")+"/auth/v1/token?grant_type=password", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", c.AnonKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("the answer is not JSON: %w", err)
	}
	if out.AccessToken == "" {
		return "", errors.New("no access token in the answer")
	}
	return out.AccessToken, nil
}

type client struct {
	http  *http.Client
	token string
}

func (c client) request(ctx context.Context, method, url string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// do sends in as JSON and decodes a 2xx answer into out (if not nil).
func (c client) do(ctx context.Context, method, url string, in, out any) error {
	resp, err := c.request(ctx, method, url, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c client) status(ctx context.Context, method, url string) (int, error) {
	resp, err := c.request(ctx, method, url, nil)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}
