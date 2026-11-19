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

// P5 in the code (D222): on Cloud Run, a service or a job, apikey mode and its variables
// never start; supabase mode does.
func TestLoadAuthRefusesAPIKeyOnCloudRun(t *testing.T) {
	key := strings.Repeat("k", 16) // built at runtime: no key-like literal
	for _, run := range []string{"K_SERVICE", "CLOUD_RUN_JOB"} {
		t.Run(run, func(t *testing.T) {
			t.Setenv("K_SERVICE", "")
			t.Setenv("CLOUD_RUN_JOB", "")
			t.Setenv(run, "api")
			cases := map[string]map[string]string{
				"apikey mode":   {"AUTH_MODE": "apikey", "API_KEY": key, "ALLOW_INSECURE_APIKEY_AUTH": "true"},
				"insecure flag": {"SUPABASE_PROJECT_REF": "abcdefghijklmnopqrst", "ALLOW_INSECURE_APIKEY_AUTH": "true"},
				"API_KEY":       {"SUPABASE_PROJECT_REF": "abcdefghijklmnopqrst", "API_KEY": key},
				"USER_ID":       {"SUPABASE_PROJECT_REF": "abcdefghijklmnopqrst", "USER_ID": DefaultUserID.String()},
			}
			for name, env := range cases {
				setAuthEnv(t, env)
				_, err := LoadAuth()
				if err == nil || !strings.Contains(err.Error(), "P5") {
					t.Errorf("%s: must be refused under P5, got %v", name, err)
				}
				if err != nil && strings.Contains(err.Error(), key) {
					t.Errorf("%s: the error quotes the key", name)
				}
			}
			setAuthEnv(t, map[string]string{"SUPABASE_PROJECT_REF": "abcdefghijklmnopqrst"})
			if _, err := LoadAuth(); err != nil {
				t.Fatalf("supabase mode on Cloud Run: %v", err)
			}
		})
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

// pubsubKeys is every variable loadPubSub reads, and the Cloud Run markers.
var pubsubKeys = []string{"GCP_PROJECT", "PUBSUB_ENDPOINT", "PUBSUB_EMULATOR_HOST", "PUSH_AUDIENCE", "PUSH_SA_EMAIL",
	"PUSH_SA_ID", "ALLOW_INSECURE_PUSH", "K_SERVICE", "CLOUD_RUN_JOB"}

// setPubSubEnv sets exactly env (everything else Load reads empty), so the developer's
// shell cannot change the outcome.
func setPubSubEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range append([]string{"PORT", "METRICS_PORT", "DB_MAX_CONNS", "XFF_TRUSTED_HOPS"}, pubsubKeys...) {
		t.Setenv(k, env[k])
	}
	t.Setenv("DATABASE_URL", "postgres://x")
}

// XFF_TRUSTED_HOPS (D240): 1 when unset (Cloud Run's front end), 2 behind the load balancer, up to 8;
// anything else stops the start, since a wrong count moves the per-address limits' key.
func TestLoadXFFTrustedHops(t *testing.T) {
	local := map[string]string{"PUBSUB_EMULATOR_HOST": "pubsub:8085", "ALLOW_INSECURE_PUSH": "true"}
	for v, want := range map[string]int{"": 1, "1": 1, "2": 2, "8": 8} {
		setPubSubEnv(t, with(local, "XFF_TRUSTED_HOPS", v))
		if cfg, err := Load("api"); err != nil || cfg.XFFTrustedHops != want {
			t.Errorf("XFF_TRUSTED_HOPS=%q: %d, %v; want %d", v, cfg.XFFTrustedHops, err, want)
		}
	}
	for _, v := range []string{"0", "9", "-1", "1.5", "two", " 2", "2 ", "0x2", "+2x"} {
		setPubSubEnv(t, with(local, "XFF_TRUSTED_HOPS", v))
		if _, err := Load("ingest"); err == nil || !strings.Contains(err.Error(), "XFF_TRUSTED_HOPS must be 1-8") {
			t.Errorf("XFF_TRUSTED_HOPS=%q: %v, want the config error", v, err)
		}
	}
}

// cloudRun is production's push configuration (deploy/run): the service's run.app URL,
// the push service account and its unique ID, built here so no test value looks like a
// real one.
func cloudRun() map[string]string {
	return map[string]string{
		"GCP_PROJECT":   "ryftlab-prod",
		"PUSH_AUDIENCE": "https://scoring-" + strings.Repeat("1", 12) + ".europe-west1.run.app",
		"PUSH_SA_EMAIL": "ryft-push@ryftlab-prod.iam.gserviceaccount.com",
		"PUSH_SA_ID":    strings.Repeat("1", 21),
		"K_SERVICE":     "scoring",
	}
}

func with(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func TestLoadPubSubCloudRun(t *testing.T) {
	setPubSubEnv(t, cloudRun())
	cfg, err := Load("scoring")
	if err != nil {
		t.Fatal(err)
	}
	want := cloudRun()
	if cfg.PubSub != (PubSub{Project: "ryftlab-prod", Endpoint: DefaultPubSubEndpoint}) ||
		cfg.Push != (Push{Audience: want["PUSH_AUDIENCE"], SAEmail: want["PUSH_SA_EMAIL"], SAID: want["PUSH_SA_ID"]}) {
		t.Fatalf("%+v %+v", cfg.PubSub, cfg.Push)
	}
	setPubSubEnv(t, with(cloudRun(), "PUBSUB_ENDPOINT", "https://pubsub.googleapis.com", "ALLOW_INSECURE_PUSH", "false"))
	if cfg, err = Load("api"); err != nil || cfg.PubSub.Endpoint != "https://pubsub.googleapis.com" || cfg.Push.AllowInsecure {
		t.Fatalf("explicit endpoint, insecure false: %+v %v", cfg.PubSub, err)
	}
	setPubSubEnv(t, with(cloudRun(), "PUBSUB_ENDPOINT", "https://europe-west1-pubsub.googleapis.com"))
	if cfg, err = Load("api"); err != nil || cfg.PubSub.Endpoint != DefaultPubSubEndpoint {
		t.Fatalf("the regional endpoint, as deploy/run sets it: %+v %v", cfg.PubSub, err)
	}
}

// Local development (compose): the emulator and no token check, with the project
// defaulting to the emulator's.
func TestLoadPubSubLocal(t *testing.T) {
	setPubSubEnv(t, map[string]string{"PUBSUB_EMULATOR_HOST": "pubsub:8085", "ALLOW_INSECURE_PUSH": "true"})
	cfg, err := Load("notify")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PubSub.Project != EmulatorProject || cfg.PubSub.EmulatorHost != "pubsub:8085" || !cfg.Push.AllowInsecure {
		t.Fatalf("%+v %+v", cfg.PubSub, cfg.Push)
	}
	setPubSubEnv(t, map[string]string{"PUBSUB_EMULATOR_HOST": "[::1]:8085", "GCP_PROJECT": "ryft-test", "ALLOW_INSECURE_PUSH": "true"})
	if cfg, err = Load("notify"); err != nil || cfg.PubSub.Project != "ryft-test" {
		t.Fatalf("an explicit project with the emulator: %+v %v", cfg.PubSub, err)
	}
	// The emulator with the push check on is allowed: the tokens are then checked as on Cloud Run.
	setPubSubEnv(t, with(cloudRun(), "K_SERVICE", "", "PUBSUB_EMULATOR_HOST", "localhost:8085"))
	if _, err = Load("notify"); err != nil {
		t.Fatalf("the emulator with a push identity: %v", err)
	}
}

// Every refusal names its variable, before any connection (Load is pure).
func TestLoadPubSubRefuses(t *testing.T) {
	local := map[string]string{"PUBSUB_EMULATOR_HOST": "pubsub:8085", "ALLOW_INSECURE_PUSH": "true"}
	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"insecure on a Cloud Run service":  {with(cloudRun(), "ALLOW_INSECURE_PUSH", "true"), "ALLOW_INSECURE_PUSH=true is refused on Cloud Run (K_SERVICE"},
		"insecure in a Cloud Run job":      {with(local, "CLOUD_RUN_JOB", "migrate", "PUBSUB_EMULATOR_HOST", ""), "ALLOW_INSECURE_PUSH=true is refused on Cloud Run (CLOUD_RUN_JOB"},
		"the emulator on Cloud Run":        {with(cloudRun(), "PUBSUB_EMULATOR_HOST", "pubsub:8085"), "PUBSUB_EMULATOR_HOST is refused on Cloud Run"},
		"the emulator and insecure on Run": {with(local, "K_SERVICE", "api"), "refused on Cloud Run"},
		"insecure is not a boolean":        {with(local, "ALLOW_INSECURE_PUSH", "1"), "ALLOW_INSECURE_PUSH must be true or false"},
		"no project":                       {with(cloudRun(), "GCP_PROJECT", ""), "GCP_PROJECT"},
		"a placeholder project":            {with(cloudRun(), "GCP_PROJECT", "REPLACE_ME"), "GCP_PROJECT"},
		"no project, insecure":             {map[string]string{"ALLOW_INSECURE_PUSH": "true"}, "GCP_PROJECT"},
		"emulator without a port":          {with(local, "PUBSUB_EMULATOR_HOST", "pubsub"), "PUBSUB_EMULATOR_HOST must be host:port"},
		"emulator as a URL":                {with(local, "PUBSUB_EMULATOR_HOST", "http://pubsub:8085"), "PUBSUB_EMULATOR_HOST must be host:port"},
		"emulator port out of range":       {with(local, "PUBSUB_EMULATOR_HOST", "pubsub:99999"), "PUBSUB_EMULATOR_HOST must be host:port"},
		"endpoint over http":               {with(cloudRun(), "PUBSUB_ENDPOINT", "http://europe-west1-pubsub.googleapis.com"), "PUBSUB_ENDPOINT"},
		"endpoint with a path":             {with(cloudRun(), "PUBSUB_ENDPOINT", "https://pubsub.googleapis.com/v1"), "PUBSUB_ENDPOINT"},
		// The access token goes with every publish: only Google's API hosts.
		"endpoint on another host":       {with(cloudRun(), "PUBSUB_ENDPOINT", "https://pubsub.example.com"), "must be a Google API host"},
		"endpoint under another domain":  {with(cloudRun(), "PUBSUB_ENDPOINT", "https://pubsub.googleapis.com.example.com"), "must be a Google API host"},
		"endpoint ending like Google's":  {with(cloudRun(), "PUBSUB_ENDPOINT", "https://evilgoogleapis.com"), "must be a Google API host"},
		"endpoint googleapis.com itself": {with(cloudRun(), "PUBSUB_ENDPOINT", "https://googleapis.com"), "must be a Google API host"},
		"endpoint with a port":           {with(cloudRun(), "PUBSUB_ENDPOINT", "https://pubsub.googleapis.com:8443"), "must be a Google API host"},
		"endpoint with userinfo":         {with(cloudRun(), "PUBSUB_ENDPOINT", "https://u@pubsub.googleapis.com"), "PUBSUB_ENDPOINT"},
		"endpoint with a fragment":       {with(cloudRun(), "PUBSUB_ENDPOINT", "https://example.com#.googleapis.com"), "PUBSUB_ENDPOINT"},
		"endpoint on localhost":          {with(cloudRun(), "PUBSUB_ENDPOINT", "https://127.0.0.1"), "must be a Google API host"},
		"no audience":                    {with(cloudRun(), "PUSH_AUDIENCE", ""), "PUSH_AUDIENCE"},
		"audience over http":             {with(cloudRun(), "PUSH_AUDIENCE", "http://scoring.run.app"), "PUSH_AUDIENCE"},
		"audience with a trailing slash": {with(cloudRun(), "PUSH_AUDIENCE", "https://scoring.run.app/"), "PUSH_AUDIENCE"},
		"audience with a path":           {with(cloudRun(), "PUSH_AUDIENCE", "https://scoring.run.app/push/ingested"), "PUSH_AUDIENCE"},
		"audience with a query":          {with(cloudRun(), "PUSH_AUDIENCE", "https://scoring.run.app?x=1"), "PUSH_AUDIENCE"},
		"audience with userinfo":         {with(cloudRun(), "PUSH_AUDIENCE", "https://u@scoring.run.app"), "PUSH_AUDIENCE"},
		"audience in upper case":         {with(cloudRun(), "PUSH_AUDIENCE", "https://Scoring.run.app"), "PUSH_AUDIENCE"},
		"audience without a host":        {with(cloudRun(), "PUSH_AUDIENCE", "https://"), "PUSH_AUDIENCE"},
		"no SA email":                    {with(cloudRun(), "PUSH_SA_EMAIL", ""), "PUSH_SA_EMAIL"},
		"a user's email":                 {with(cloudRun(), "PUSH_SA_EMAIL", "someone@example.com"), "PUSH_SA_EMAIL"},
		"no SA ID":                       {with(cloudRun(), "PUSH_SA_ID", ""), "PUSH_SA_ID"},
		"a placeholder SA ID":            {with(cloudRun(), "PUSH_SA_ID", "<push_sa_unique_id>"), "PUSH_SA_ID"},
		"the SA's email as its ID":       {with(cloudRun(), "PUSH_SA_ID", "ryft-push@ryftlab-prod.iam.gserviceaccount.com"), "PUSH_SA_ID"},
		"no push identity, no flag":      {map[string]string{"GCP_PROJECT": "ryftlab-prod"}, "PUSH_AUDIENCE"},
		"emulator, no identity, no flag": {map[string]string{"PUBSUB_EMULATOR_HOST": "pubsub:8085"}, "PUSH_AUDIENCE"},
	} {
		t.Run(name, func(t *testing.T) {
			setPubSubEnv(t, c.env)
			if _, err := Load("scoring"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

// A Cloud Run job (scoring rescore, ingest fold-workouts) publishes nothing and serves no
// push: it starts without the push identity and the project, and what it is given is
// still checked as a service's would be.
func TestLoadPubSubCloudRunJob(t *testing.T) {
	job := map[string]string{"CLOUD_RUN_JOB": "scoring-rescore"}
	setPubSubEnv(t, job)
	cfg, err := Load("scoring")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PubSub != (PubSub{Endpoint: DefaultPubSubEndpoint}) || cfg.Push != (Push{}) {
		t.Fatalf("a bare job: %+v %+v", cfg.PubSub, cfg.Push)
	}
	setPubSubEnv(t, with(job, "GCP_PROJECT", "ryftlab-prod"))
	if cfg, err = Load("ingest"); err != nil || cfg.PubSub.Project != "ryftlab-prod" {
		t.Fatalf("a job with its project: %+v %v", cfg.PubSub, err)
	}
	// The service's whole environment, as a job copied from it would have it.
	setPubSubEnv(t, with(cloudRun(), "K_SERVICE", "", "CLOUD_RUN_JOB", "ingest-fold-workouts"))
	if cfg, err = Load("ingest"); err != nil || cfg.Push.SAID != cloudRun()["PUSH_SA_ID"] {
		t.Fatalf("a job with the service's push identity: %+v %v", cfg.Push, err)
	}

	for name, c := range map[string]struct {
		env  map[string]string
		want string
	}{
		"insecure":                  {with(job, "ALLOW_INSECURE_PUSH", "true"), "ALLOW_INSECURE_PUSH=true is refused on Cloud Run (CLOUD_RUN_JOB"},
		"the emulator":              {with(job, "PUBSUB_EMULATOR_HOST", "pubsub:8085"), "PUBSUB_EMULATOR_HOST is refused on Cloud Run (CLOUD_RUN_JOB"},
		"a placeholder project":     {with(job, "GCP_PROJECT", "REPLACE_ME"), "GCP_PROJECT"},
		"an endpoint off Google":    {with(job, "PUBSUB_ENDPOINT", "https://pubsub.example.com"), "must be a Google API host"},
		"half a push identity":      {with(job, "PUSH_AUDIENCE", cloudRun()["PUSH_AUDIENCE"]), "PUSH_SA_EMAIL"},
		"an audience with a path":   {with(cloudRun(), "K_SERVICE", "", "CLOUD_RUN_JOB", "x", "PUSH_AUDIENCE", "https://scoring.run.app/x"), "PUSH_AUDIENCE"},
		"a placeholder SA ID":       {with(job, "PUSH_SA_ID", "<push_sa_unique_id>"), "PUSH_AUDIENCE"},
		"a service, not a job":      {with(job, "K_SERVICE", "scoring", "GCP_PROJECT", "ryftlab-prod"), "PUSH_AUDIENCE"},
		"a service with no project": {with(job, "K_SERVICE", "scoring"), "GCP_PROJECT"},
	} {
		t.Run(name, func(t *testing.T) {
			setPubSubEnv(t, c.env)
			if _, err := Load("scoring"); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
		})
	}
}

func TestLoadNeedsADatabase(t *testing.T) {
	setPubSubEnv(t, cloudRun())
	t.Setenv("DATABASE_URL", "")
	if _, err := Load("api"); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("got %v", err)
	}
}
