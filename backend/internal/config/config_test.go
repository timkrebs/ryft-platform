package config

import (
	"os"
	"strings"
	"testing"
)

func setAuthEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{"AUTH_MODE", "SUPABASE_PROJECT_REF", "SUPABASE_ISSUER", "SUPABASE_JWKS_URL",
		"API_KEY", "USER_ID", "ALLOW_INSECURE_APIKEY_AUTH"} {
		t.Setenv(k, env[k])
	}
}

func TestLoadAuthSupabase(t *testing.T) {
	setAuthEnv(t, map[string]string{"SUPABASE_PROJECT_REF": "abcdefghijklmnopqrst"})
	a, err := LoadAuth()
	if err != nil {
		t.Fatal(err)
	}
	if a.Mode != AuthSupabase || a.Issuer != "https://abcdefghijklmnopqrst.supabase.co/auth/v1" ||
		a.JWKSURL != "https://abcdefghijklmnopqrst.supabase.co/auth/v1/.well-known/jwks.json" {
		t.Fatalf("derived %+v", a)
	}
}

func TestLoadAuthRefusesPlaceholdersAndInsecureModes(t *testing.T) {
	cases := map[string]map[string]string{
		"no project ref":          {},
		"placeholder project ref": {"SUPABASE_PROJECT_REF": "replace-with-project-ref"},
		"apikey without flag":     {"AUTH_MODE": "apikey", "API_KEY": "0123456789abcdef"},
		"apikey with short key":   {"AUTH_MODE": "apikey", "API_KEY": "short", "ALLOW_INSECURE_APIKEY_AUTH": "true"},
		"apikey with bad user":    {"AUTH_MODE": "apikey", "API_KEY": "0123456789abcdef", "ALLOW_INSECURE_APIKEY_AUTH": "true", "USER_ID": "me"},
		"unknown mode":            {"AUTH_MODE": "oauth"},
	}
	for name, env := range cases {
		setAuthEnv(t, env)
		if _, err := LoadAuth(); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestLoadAuthAPIKeyAndOverrides(t *testing.T) {
	setAuthEnv(t, map[string]string{"AUTH_MODE": "apikey", "API_KEY": "0123456789abcdef", "ALLOW_INSECURE_APIKEY_AUTH": "true"})
	a, err := LoadAuth()
	if err != nil || a.APIKeyUserID != DefaultUserID {
		t.Fatalf("%+v %v", a, err)
	}
	// Custom domain / local GoTrue: explicit issuer and JWKS URL need no project ref.
	setAuthEnv(t, map[string]string{"SUPABASE_ISSUER": "https://auth.ryftapp.de/auth/v1/", "SUPABASE_JWKS_URL": "http://gotrue:9999/.well-known/jwks.json"})
	if a, err = LoadAuth(); err != nil || strings.HasSuffix(a.Issuer, "/") {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestLoadAppleIsAllOrNothing(t *testing.T) {
	keys := []string{"APPLE_TEAM_ID", "APPLE_KEY_ID", "APPLE_CLIENT_ID", "APPLE_KEY_P8", "DATA_ENCRYPTION_KEY"}
	for _, k := range keys {
		t.Setenv(k, "")
	}
	if _, ok, err := LoadApple(); ok || err != nil {
		t.Fatalf("none set: disabled, no error (ok=%v err=%v)", ok, err)
	}
	// infra/vault always generates the data key; without the Apple keys the api
	// must still start, with revocation off.
	t.Setenv("DATA_ENCRYPTION_KEY", "x")
	if _, ok, err := LoadApple(); ok || err != nil {
		t.Fatalf("only the data key: disabled, no error (ok=%v err=%v)", ok, err)
	}
	t.Setenv("APPLE_TEAM_ID", "M77SKHKG63")
	if _, _, err := LoadApple(); err == nil {
		t.Fatal("a partial Apple configuration must be refused")
	}
	for _, k := range keys {
		t.Setenv(k, "x")
	}
	if a, ok, err := LoadApple(); !ok || err != nil || a.ClientID != "x" {
		t.Fatalf("all set: %+v %v %v", a, ok, err)
	}
	t.Setenv("DATA_ENCRYPTION_KEY", "")
	if _, _, err := LoadApple(); err == nil {
		t.Fatal("the Apple keys without the data key must be refused")
	}
}

func TestLoadAPNs(t *testing.T) {
	for _, k := range []string{"APNS_KEY_ID", "APNS_TEAM_ID", "APNS_KEY_P8", "APNS_TOPIC"} {
		t.Setenv(k, "")
	}
	if a, ok, err := LoadAPNs(); err != nil || ok || a.Topic != DefaultAPNsTopic {
		t.Fatalf("none set: off, default topic: %+v %v %v", a, ok, err)
	}
	t.Setenv("APNS_KEY_ID", "ABCDE12345")
	if _, _, err := LoadAPNs(); err == nil {
		t.Fatal("a partial set is refused")
	}
	t.Setenv("APNS_TEAM_ID", "TEAM123456")
	t.Setenv("APNS_KEY_P8", "pem")
	t.Setenv("APNS_TOPIC", "dev.example.app")
	if a, ok, err := LoadAPNs(); err != nil || !ok || a.Topic != "dev.example.app" {
		t.Fatalf("all set: %+v %v %v", a, ok, err)
	}
}

func TestLoadAdmin(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "")
	if k, err := LoadAdmin(); err != nil || k != "" {
		t.Fatal("unset: off")
	}
	t.Setenv("ADMIN_API_KEY", "short")
	if _, err := LoadAdmin(); err == nil {
		t.Fatal("a short key is refused")
	}
}

// D87: both gates off|warn|on; the beta gate defaults to off, the consent check to on.
func TestLoadBetaModes(t *testing.T) {
	t.Setenv("BETA_GATE", "")
	t.Setenv("CONSENT_ENFORCE", "")
	b, err := LoadBeta()
	if err != nil || b.Gate != ModeOff || b.Consent != ModeOn {
		t.Fatalf("defaults: %+v %v", b, err)
	}
	t.Setenv("BETA_GATE", "Warn")
	t.Setenv("CONSENT_ENFORCE", " warn ")
	if b, err = LoadBeta(); err != nil || b.Gate != ModeWarn || b.Consent != ModeWarn {
		t.Fatalf("warn: %+v %v", b, err)
	}
	t.Setenv("BETA_GATE", "on")
	t.Setenv("CONSENT_ENFORCE", "off")
	if b, err = LoadBeta(); err != nil || b.Gate != ModeOn || b.Consent != ModeOff {
		t.Fatalf("on/off: %+v %v", b, err)
	}
	for _, bad := range []string{"true", "1", "enforce"} {
		t.Setenv("CONSENT_ENFORCE", bad)
		if _, err := LoadBeta(); err == nil {
			t.Errorf("CONSENT_ENFORCE=%s must be refused", bad)
		}
	}
}

// F1: no default version. Unset, empty or blank is "" without an error (scoring's
// path); otherwise the trimmed value must be a YYYY-MM-DD date. Kustomize env files
// keep quotes and inline comments in the value, so those are refused, not stripped.
func TestLoadBetaPolicyVersion(t *testing.T) {
	const unset = "<unset>" // a marker, never set in the environment
	for _, c := range []struct {
		name, env, want, err string
	}{
		{"unset", unset, "", ""},
		{"empty", "", "", ""},
		{"whitespace", " \t ", "", ""},
		{"valid", "2026-11-02", "2026-11-02", ""},
		{"trimmed", " 2026-11-02 ", "2026-11-02", ""},
		{"double quotes", `"2026-11-02"`, "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "\"2026-11-02\""`},
		{"single quotes", `'2026-11-02'`, "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "'2026-11-02'"`},
		{"inline comment", "2026-11-02 # x", "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "2026-11-02 # x"`},
		{"no such day", "2026-02-30", "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "2026-02-30"`},
		{"no such month", "2026-13-01", "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "2026-13-01"`},
		{"unpadded", "2026-1-2", "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "2026-1-2"`},
		{"not a date", "latest", "", `PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "latest"`},
		{"40 characters", "2026-11-02" + strings.Repeat("0", 30), "",
			`PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got "2026-11-02` + strings.Repeat("0", 30) + `"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BETA_GATE", "")
			t.Setenv("CONSENT_ENFORCE", "")
			if c.env == unset {
				t.Setenv("PRIVACY_POLICY_VERSION", "") // restored after the test
				os.Unsetenv("PRIVACY_POLICY_VERSION")
			} else {
				t.Setenv("PRIVACY_POLICY_VERSION", c.env)
			}
			b, err := LoadBeta()
			if c.err != "" {
				if err == nil || err.Error() != c.err {
					t.Fatalf("got %v, want %s", err, c.err)
				}
				return
			}
			if err != nil || b.PrivacyPolicyVersion != c.want || b.Gate != ModeOff || b.Consent != ModeOn {
				t.Fatalf("got %+v %v, want version %q", b, err, c.want)
			}
		})
	}
}

// api and ingest refuse to start without a version, in every auth mode (F1).
func TestRequirePolicy(t *testing.T) {
	const want = "PRIVACY_POLICY_VERSION is required: api and ingest record and enforce consent against it (D78); " +
		"set the published version (deploy/k8s/base/<service>/config.env, docker-compose.yml)"
	if err := (Beta{}).RequirePolicy(); err == nil || err.Error() != want {
		t.Fatalf("empty: got %v, want %s", err, want)
	}
	if err := (Beta{PrivacyPolicyVersion: "2026-11-02"}).RequirePolicy(); err != nil {
		t.Fatalf("set: %v", err)
	}
}

// scoring, scoring rescore and learn-baselines read only the consent mode: LoadBeta
// alone, without RequirePolicy, must start them when no version is configured.
func TestLoadBetaScoringPathNeedsNoPolicy(t *testing.T) {
	t.Setenv("BETA_GATE", "")
	t.Setenv("CONSENT_ENFORCE", "warn")
	t.Setenv("PRIVACY_POLICY_VERSION", "")
	os.Unsetenv("PRIVACY_POLICY_VERSION")
	b, err := LoadBeta()
	if err != nil || b.Consent != ModeWarn {
		t.Fatalf("got %+v %v", b, err)
	}
	if b.RequirePolicy() == nil {
		t.Fatal("the same Beta must not satisfy api and ingest")
	}
}

// After the cutover (D102) RabbitMQ is the only broker: the old settings may still
// say amqp, a leftover nats is refused at start-up, and the user comes in pairs.
func TestEventsSettings(t *testing.T) {
	// Everything else Load reads, so the developer's shell cannot change the outcome.
	for _, k := range []string{"PORT", "METRICS_PORT", "AMQP_URL", "AMQP_CA_FILE", "AMQP_CERT_FILE", "AMQP_KEY_FILE", "DB_MAX_CONNS"} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
	for _, c := range []struct {
		backend, consume, user, pass string
		ok                           bool
	}{
		{"", "", "", "", true},
		{"amqp", "amqp", "ingest", "pw", true},
		{"nats", "", "", "", false},
		{"", "nats,amqp", "", "", false},
		{"amqp", "", "ingest", "", false},
	} {
		t.Setenv("EVENTS_BACKEND", c.backend)
		t.Setenv("EVENTS_CONSUME", c.consume)
		t.Setenv("AMQP_USER", c.user)
		t.Setenv("AMQP_PASSWORD", c.pass)
		cfg, err := Load("scoring")
		if (err == nil) != c.ok {
			t.Errorf("EVENTS_BACKEND=%q EVENTS_CONSUME=%q AMQP_USER=%q: %v", c.backend, c.consume, c.user, err)
		}
		if err == nil && cfg.AMQP.URL != "amqps://rabbitmq.ryft-rabbitmq.svc.cluster.local:5671/ryft" {
			t.Errorf("the cluster URL by default: %q", cfg.AMQP.URL)
		}
	}
}
