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
	setAuthEnv(t, map[string]string{"SUPABASE_ISSUER": "https://auth.ryftlab.de/auth/v1/", "SUPABASE_JWKS_URL": "http://gotrue:9999/.well-known/jwks.json"})
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
	t.Setenv("ADMIN_WEB_ORIGINS", "")
	t.Setenv("PUBLIC_WEB_URL", "")
	if a, err := LoadAdmin(); err != nil || a.Key != "" || len(a.WebOrigins) != 0 || a.PublicWebURL != "https://ryftlab.de" {
		t.Fatalf("unset: off, no CORS, the default site: %+v %v", a, err)
	}
	t.Setenv("ADMIN_API_KEY", "short")
	if _, err := LoadAdmin(); err == nil {
		t.Fatal("a short key is refused")
	}
}

// ADMIN_WEB_ORIGINS: only bare origins the way a browser writes them; anything else stops the api.
func TestLoadAdminWebOrigins(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "")
	t.Setenv("PUBLIC_WEB_URL", "")
	for _, tc := range []struct {
		name, env string
		want      []string
	}{
		{"unset", "", nil},
		{"blank", "  \t ", nil},
		{"the site", "https://ryftlab.de", []string{"https://ryftlab.de"}},
		{"trimmed, deduplicated, ordered", " https://ryftlab.de , http://localhost:3000,https://ryftlab.de",
			[]string{"https://ryftlab.de", "http://localhost:3000"}},
		{"loopback over http", "http://127.0.0.1:8080,http://[::1]:3000,http://localhost", []string{"http://127.0.0.1:8080", "http://[::1]:3000", "http://localhost"}},
		{"explicit port", "https://admin.example.com:8443", []string{"https://admin.example.com:8443"}},
		{"five", "https://a.de,https://b.de,https://c.de,https://d.de,https://e.de",
			[]string{"https://a.de", "https://b.de", "https://c.de", "https://d.de", "https://e.de"}},
	} {
		t.Run("valid: "+tc.name, func(t *testing.T) {
			t.Setenv("ADMIN_WEB_ORIGINS", tc.env)
			a, err := LoadAdmin()
			if err != nil || strings.Join(a.WebOrigins, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("%+v %v, want %v", a.WebOrigins, err, tc.want)
			}
		})
	}
	for name, env := range map[string]string{
		"trailing slash":        "https://ryftlab.de/",
		"a path":                "https://ryftlab.de/admin",
		"a query":               "https://ryftlab.de?x=1",
		"a fragment":            "https://ryftlab.de#x",
		"userinfo":              "https://user:pw@ryftlab.de",
		"http outside loopback": "http://ryftlab.de",
		"http on a lookalike":   "http://localhost.evil.example",
		"wildcard":              "*",
		"wildcard subdomain":    "https://*.ryftlab.de",
		"null":                  "null",
		"no scheme":             "ryftlab.de",
		"another scheme":        "ftp://ryftlab.de",
		"upper-case host":       "https://RyftLab.de",
		"upper-case scheme":     "HTTPS://ryftlab.de",
		"default https port":    "https://ryftlab.de:443",
		"default http port":     "http://localhost:80",
		"empty port":            "https://ryftlab.de:",
		"bad port":              "https://ryftlab.de:abc",
		"empty entry":           "https://ryftlab.de,,https://b.de",
		"trailing comma":        "https://ryftlab.de,",
		"six origins":           "https://a.de,https://b.de,https://c.de,https://d.de,https://e.de,https://f.de",
		"quoted":                `"https://ryftlab.de"`,
		"inline comment":        "https://ryftlab.de # prod",
		"semicolon list":        "https://ryftlab.de;https://b.de",
		"host only colon":       "https://",
	} {
		t.Run("malformed: "+name, func(t *testing.T) {
			t.Setenv("ADMIN_WEB_ORIGINS", env)
			if a, err := LoadAdmin(); err == nil || !strings.Contains(err.Error(), "ADMIN_WEB_ORIGINS") {
				t.Fatalf("%q must stop the api: %+v %v", env, a.WebOrigins, err)
			}
		})
	}
}

func TestLoadAdminPublicWebURL(t *testing.T) {
	t.Setenv("ADMIN_API_KEY", "")
	t.Setenv("ADMIN_WEB_ORIGINS", "")
	t.Setenv("PUBLIC_WEB_URL", "https://staging.example.com")
	if a, err := LoadAdmin(); err != nil || a.PublicWebURL != "https://staging.example.com" {
		t.Fatalf("%+v %v", a, err)
	}
	for _, bad := range []string{"https://ryftlab.de/", "ryftlab.de", "http://ryftlab.de", "https://ryftlab.de/invite"} {
		t.Setenv("PUBLIC_WEB_URL", bad)
		if _, err := LoadAdmin(); err == nil || !strings.Contains(err.Error(), "PUBLIC_WEB_URL") {
			t.Errorf("%q must stop the api: %v", bad, err)
		}
	}
}

// The committed config line is parsed by the loader, so a value that would stop the api at start-up
// cannot merge, and production's value never allows a plain-http origin.
func TestCommittedAdminWebOrigins(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/k8s/base/api/config.env")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "ADMIN_WEB_ORIGINS=") {
			lines = append(lines, l)
		}
		if strings.Contains(l, "ADMIN_WEB_ORIGINS") && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "ADMIN_WEB_ORIGINS=") {
			t.Errorf("not a bare KEY=value line (kustomize keeps whatever else is on it): %q", l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("want exactly one ADMIN_WEB_ORIGINS line in deploy/k8s/base/api/config.env, have %d", len(lines))
	}
	t.Setenv("ADMIN_API_KEY", "")
	t.Setenv("PUBLIC_WEB_URL", "")
	t.Setenv("ADMIN_WEB_ORIGINS", strings.TrimPrefix(lines[0], "ADMIN_WEB_ORIGINS="))
	a, err := LoadAdmin()
	if err != nil {
		t.Fatalf("the committed value stops the api at start-up: %v", err)
	}
	for _, o := range a.WebOrigins {
		if strings.HasPrefix(o, "http:") {
			t.Errorf("production allows a plain-http origin: %s", o)
		}
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
