package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/model"
)

func TestAdmitted(t *testing.T) {
	current := &model.Consent{PolicyVersion: "2026-10-20", GrantedAt: time.Now()}
	old := &model.Consent{PolicyVersion: "2026-01-01", GrantedAt: time.Now()}
	off, warn, on := config.ModeOff, config.ModeWarn, config.ModeOn
	for _, c := range []struct {
		name          string
		gate, consent config.Mode
		user          model.User
		code          string // "" = admitted
		header        string // X-RYFT-Gate when admitted in warn mode
	}{
		{"consented, gate off", off, on, model.User{Consent: current}, "", ""},
		{"no consent", off, on, model.User{BetaAccess: true}, model.CodeConsentRequired, ""},
		{"an older policy", off, on, model.User{BetaAccess: true, Consent: old}, model.CodeConsentRequired, ""},
		{"gate on, no access", on, on, model.User{Consent: current}, model.CodeInviteRequired, ""},
		{"gate on, access and consent", on, on, model.User{BetaAccess: true, Consent: current}, "", ""},
		{"gate on, neither: the invite first", on, on, model.User{}, model.CodeInviteRequired, ""},
		{"consent warn: served, flagged", off, warn, model.User{BetaAccess: true}, "", "consent_required"},
		{"both warn: both flagged", warn, warn, model.User{}, "", "invite_required,consent_required"},
		{"gate warn, consent on: still refused", warn, on, model.User{}, model.CodeConsentRequired, ""},
		{"consent off", off, off, model.User{}, "", ""},
	} {
		h := &handler{log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			beta: config.Beta{Gate: c.gate, Consent: c.consent, PrivacyPolicyVersion: "2026-10-20"}}
		rec := httptest.NewRecorder()
		ok := h.admitted(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", nil), c.user)
		var body struct{ Code string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		header := strings.Join(rec.Header().Values("X-RYFT-Gate"), ",")
		if ok != (c.code == "") || body.Code != c.code || (!ok && rec.Code != 403) || (ok && header != c.header) {
			t.Errorf("%s: admitted=%v status=%d code=%q header=%q", c.name, ok, rec.Code, body.Code, header)
		}
	}
}

func TestValidateBetaFields(t *testing.T) {
	long := func(n int) *string { s := strings.Repeat("x", n); return &s }
	now := time.Now()
	base := func() model.SyncRequest {
		return model.SyncRequest{DeviceID: uuid.New(), AppVersion: long(64), OSVersion: long(64),
			Samples:  []model.Sample{{Type: model.SampleSteps, Start: now, End: now, SourceBundleID: long(255)}},
			Sleep:    []model.SleepSegment{{Start: now, End: now.Add(time.Hour), Stage: "core", SourceBundleID: long(255)}},
			Workouts: []model.Workout{{ID: uuid.New(), Start: now, End: now.Add(time.Hour), SourceBundleID: long(255)}}}
	}
	ok := base()
	if err := validate(&ok); err != nil {
		t.Fatalf("at the limits: %v", err)
	}
	for name, breakIt := range map[string]func(*model.SyncRequest){
		"app_version": func(r *model.SyncRequest) { r.AppVersion = long(65) },
		"os_version":  func(r *model.SyncRequest) { r.OSVersion = long(65) },
		"sample":      func(r *model.SyncRequest) { r.Samples[0].SourceBundleID = long(256) },
		"sleep":       func(r *model.SyncRequest) { r.Sleep[0].SourceBundleID = long(256) },
		"workout":     func(r *model.SyncRequest) { r.Workouts[0].SourceBundleID = long(256) },
	} {
		r := base()
		breakIt(&r)
		if err := validate(&r); err == nil {
			t.Errorf("%s too long must be refused", name)
		}
	}
}

func TestScreen(t *testing.T) {
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return now.Add(d) }
	req := model.SyncRequest{
		Samples: []model.Sample{{Type: model.SampleSteps, Start: at(-time.Hour), End: at(-time.Hour)},
			{Type: "blood_glucose", Start: at(-time.Hour), End: at(-time.Hour)},
			{Type: model.SampleSteps, Start: at(30 * time.Hour), End: at(30 * time.Hour)},
			{Type: model.SampleSteps, Start: at(-401 * 24 * time.Hour), End: at(-401 * 24 * time.Hour)},
			{Type: model.SampleSteps, Start: at(23 * time.Hour), End: at(23 * time.Hour)},
			{Type: model.SampleBasalEnergy, Start: now, End: now},
			{Type: model.SampleHeartRateRecovery, Start: now, End: now},
			{Type: model.SampleHeartRateRecovery, Start: now, End: now, WasUserEntered: true},
			{Type: model.SampleBasalEnergy, Start: now, End: now, Source: "ryft-direct-garmin"},
			{Type: model.SampleBodyMass, Start: now, End: now, WasUserEntered: true}},
		Sleep: []model.SleepSegment{{Start: at(-8 * time.Hour), End: at(-time.Hour), Stage: "core"},
			{Start: at(-8 * time.Hour), End: at(-time.Hour), Stage: "nap"}},
		Workouts: []model.Workout{{ID: uuid.New(), Start: at(-2 * time.Hour), End: at(-time.Hour)},
			{ID: uuid.New(), Start: at(48 * time.Hour), End: at(49 * time.Hour)}},
	}
	ig := screen(&req, now)
	want := model.SyncIgnored{Samples: model.IgnoredCounts{OutsideWindow: 2, UnknownType: 3},
		Sleep: model.IgnoredCounts{UnknownType: 1}, Workouts: model.IgnoredCounts{OutsideWindow: 1}}
	if ig != want || len(req.Samples) != 5 || len(req.Sleep) != 1 || len(req.Workouts) != 1 {
		t.Fatalf("ignored %+v, kept %d/%d/%d", ig, len(req.Samples), len(req.Sleep), len(req.Workouts))
	}
	if req.Samples[2].Type != model.SampleBasalEnergy || req.Samples[3].Type != model.SampleHeartRateRecovery ||
		req.Samples[4].Type != model.SampleBodyMass || !req.Samples[4].WasUserEntered {
		t.Fatal("valid fitness samples and user-entered body mass should pass screening")
	}
	// An unknown type no longer fails the sync (it is ignored instead).
	if err := validate(&model.SyncRequest{DeviceID: uuid.New(), Samples: []model.Sample{{Type: "x", Start: now, End: now}}}); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// F1: without PRIVACY_POLICY_VERSION ingest exits 1 before it connects to
// anything. The database is a closed port: were the check behind db.Open, the
// start-up retries would outlast the timeout. The child gets only this
// environment, so nothing from the developer's shell reaches config.Load or
// LoadAuth; the version is unset, as in a pod without it.
func TestMainRefusesMissingPolicyBeforeConnecting(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainRefusesMissingPolicyBeforeConnecting$")
	cmd.Env = []string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://ingest@127.0.0.1:1/ryft",
		"AUTH_MODE=apikey", "API_KEY=0123456789abcdef", "ALLOW_INSECURE_APIKEY_AUTH=true",
		"PUBSUB_EMULATOR_HOST=127.0.0.1:1", "ALLOW_INSECURE_PUSH=true"} // compose's local Pub/Sub, never reached
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(out), `"msg":"beta config"`) ||
		!strings.Contains(string(out), "PRIVACY_POLICY_VERSION is required: ") {
		t.Fatalf("want exit 1 with the beta config error, got %v:\n%s", err, out)
	}
}

// App Attest's start refusals (D247): warn or on without the ring, and a malformed ring, stop ingest
// before it connects, and the error holds no key material; a good ring starts with one "app attest"
// line of the mode, whether a ring is set and the exempt count, never a key or an account.
func TestMainAppAttestConfig(t *testing.T) {
	if os.Getenv("RYFT_TEST_MAIN") == "1" {
		main()
		return
	}
	good := appattesttest.RingLine(11, 0x1d)
	key := good[strings.IndexByte(good, ':')+1:]
	exempt := uuid.NewString()
	run := func(wait time.Duration, env ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMainAppAttestConfig$")
		cmd.Env = append([]string{"RYFT_TEST_MAIN=1", "DATABASE_URL=postgres://ingest@127.0.0.1:1/ryft", "PORT=1", "METRICS_PORT=1",
			"AUTH_MODE=apikey", "API_KEY=0123456789abcdef", "ALLOW_INSECURE_APIKEY_AUTH=true", "PRIVACY_POLICY_VERSION=2026-10-26",
			"PUBSUB_EMULATOR_HOST=127.0.0.1:1", "ALLOW_INSECURE_PUSH=true"}, env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, env := range [][]string{
		{"APP_ATTEST=warn"},
		{"APP_ATTEST=on"},
		{"APP_ATTEST=off", "APP_ATTEST_TOKEN_KEYS=" + good[:30]},
	} {
		out, err := run(10*time.Second, env...)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(out, `"msg":"app attest config"`) {
			t.Fatalf("%v: want exit 1 with the app attest config error, got %v:\n%s", env, err, out)
		}
		if strings.Contains(out, `"msg":"listening"`) || strings.Contains(out, key[:12]) {
			t.Errorf("%v: listened or printed key material:\n%s", env, out)
		}
	}
	out, _ := run(3*time.Second, "APP_ATTEST=warn", "APP_ATTEST_TOKEN_KEYS="+good, "APP_ATTEST_EXEMPT_USERS="+exempt)
	said := false
	for _, line := range strings.Split(out, "\n") {
		said = said || (strings.Contains(line, `"msg":"app attest"`) && strings.Contains(line, `"mode":"warn"`) &&
			strings.Contains(line, `"ring":true`) && strings.Contains(line, `"exempt":1`))
	}
	if !said || strings.Contains(out, key[:12]) || strings.Contains(out, exempt) {
		t.Fatalf("want one app attest line without the key or the account:\n%s", out)
	}
}
