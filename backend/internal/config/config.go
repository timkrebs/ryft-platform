package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// DefaultUserID is the user apikey mode acts as when USER_ID is unset (the old
// single-tenant seed).
var DefaultUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// Auth modes.
const (
	AuthSupabase = "supabase" // Supabase access tokens (JWT), verified against the project JWKS
	AuthAPIKey   = "apikey"   // shared key, local development only
)

// Config is what every service reads: its name, ports, database URL and pool size, how it
// publishes to Pub/Sub and whose tokens its push and tick routes accept. The auth, beta,
// admin, APNs and Apple settings have their own loaders, because only some services need
// them.
type Config struct {
	ServiceName string
	Port        string
	MetricsPort string // METRICS_PORT, default 9090: /metrics only
	DatabaseURL string
	DBMaxConns  int // pool size; small because Supavisor session mode holds a server connection per client connection (D34)
	PubSub      PubSub
	Push        Push
	// XFFTrustedHops is XFF_TRUSTED_HOPS, 1 to 8 (default 1): how many X-Forwarded-For entries,
	// counted from the right after Google's own hops, our proxies append; the client is the last of
	// them (httpx.ClientKeys, the per-address limits of api and ingest). 1 on Cloud Run, whose front
	// end appends the caller's address; 2 behind the external Application Load Balancer, which
	// appends <client>,<lb-ip>, and only together with ingress internal-and-cloud-load-balancing:
	// with ingress all a caller reaching run.app directly makes its own forged entry the key (D240).
	XFFTrustedHops int
}

// DefaultXFFTrustedHops is XFF_TRUSTED_HOPS when unset: Cloud Run's front end appends one entry.
const DefaultXFFTrustedHops = 1

// MaxXFFTrustedHops bounds XFF_TRUSTED_HOPS: no chain of our proxies is longer.
const MaxXFFTrustedHops = 8

// DefaultPubSubEndpoint is Pub/Sub's regional endpoint in the services' region:
// ordering needs every message of a key published in the same region (D218).
const DefaultPubSubEndpoint = "https://europe-west1-pubsub.googleapis.com"

// EmulatorProject is the project the emulator's topology lives in when GCP_PROJECT is
// unset (deploy/pubsub-emulator/init.sh has the same default).
const EmulatorProject = "ryft-local"

// PubSub is how a service publishes: REST to Endpoint with an access token from the
// metadata server, or plain HTTP to the emulator without one.
type PubSub struct {
	Project      string // GCP_PROJECT; EmulatorProject by default with the emulator
	Endpoint     string // PUBSUB_ENDPOINT, an https origin; DefaultPubSubEndpoint by default
	EmulatorHost string // PUBSUB_EMULATOR_HOST, host:port; set: no token, Endpoint unused
}

// Push is whose OIDC tokens a service's /push/* and /internal/* routes accept: Pub/Sub
// push and the tick carry a token of the push service account (D218).
type Push struct {
	Audience      string // PUSH_AUDIENCE: the service's own run.app URL, exactly as the subscriptions set it
	SAEmail       string // PUSH_SA_EMAIL: the push service account (the token's email)
	SAID          string // PUSH_SA_ID: its unique ID (the token's sub)
	AllowInsecure bool   // ALLOW_INSECURE_PUSH=true: no token checked (local development only)
}

// cloudRunEnv are the variables Cloud Run sets in a service (K_SERVICE) and a job
// (CLOUD_RUN_JOB): with either, nothing insecure or emulated may start.
var cloudRunEnv = []string{"K_SERVICE", "CLOUD_RUN_JOB"}

var (
	// pushSAEmail is a service account's address (name@project.iam.gserviceaccount.com).
	pushSAEmail = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`)
	// pushSAID is a service account's unique ID: digits only.
	pushSAID = regexp.MustCompile(`^[0-9]{6,32}$`)
	// gcpProject is a project ID: 6 to 30 lower-case letters, digits and hyphens.
	gcpProject = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
)

// loadPubSub reads the Pub/Sub and push settings and refuses what must not run: the
// insecure flag or the emulator on Cloud Run, a push identity half set or set to a
// placeholder, an endpoint that is not a bare https origin on googleapis.com, an
// audience that is not a bare https origin.
//
// A Cloud Run job (CLOUD_RUN_JOB without K_SERVICE: scoring rescore, ingest
// fold-workouts) publishes nothing and serves no push or tick, so it needs neither the
// push identity nor GCP_PROJECT; what it is given is still checked, and the two refusals
// above hold for it as for a service.
func loadPubSub() (PubSub, Push, error) {
	job := os.Getenv("CLOUD_RUN_JOB") != "" && os.Getenv("K_SERVICE") == ""
	ps := PubSub{
		Project:      strings.TrimSpace(os.Getenv("GCP_PROJECT")),
		Endpoint:     strings.TrimSpace(getenv("PUBSUB_ENDPOINT", DefaultPubSubEndpoint)),
		EmulatorHost: strings.TrimSpace(os.Getenv("PUBSUB_EMULATOR_HOST")),
	}
	p := Push{
		Audience: strings.TrimSpace(os.Getenv("PUSH_AUDIENCE")),
		SAEmail:  strings.TrimSpace(os.Getenv("PUSH_SA_EMAIL")),
		SAID:     strings.TrimSpace(os.Getenv("PUSH_SA_ID")),
	}
	switch v := strings.TrimSpace(os.Getenv("ALLOW_INSECURE_PUSH")); v {
	case "", "false":
	case "true":
		p.AllowInsecure = true
	default:
		return ps, p, fmt.Errorf("ALLOW_INSECURE_PUSH must be true or false, got %q", v)
	}
	for _, k := range cloudRunEnv {
		if os.Getenv(k) == "" {
			continue
		}
		if p.AllowInsecure {
			return ps, p, fmt.Errorf("ALLOW_INSECURE_PUSH=true is refused on Cloud Run (%s is set): push and tick tokens are always checked there (D218)", k)
		}
		if ps.EmulatorHost != "" {
			return ps, p, fmt.Errorf("PUBSUB_EMULATOR_HOST is refused on Cloud Run (%s is set): the emulator is for local development", k)
		}
	}
	if ps.EmulatorHost != "" {
		host, port, err := net.SplitHostPort(ps.EmulatorHost)
		if n, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
			return ps, p, fmt.Errorf("PUBSUB_EMULATOR_HOST must be host:port, got %q", ps.EmulatorHost)
		}
		if ps.Project == "" {
			ps.Project = EmulatorProject
		}
	}
	if !gcpProject.MatchString(ps.Project) && (!job || ps.Project != "") {
		return ps, p, fmt.Errorf("GCP_PROJECT must be the project ID (required unless PUBSUB_EMULATOR_HOST is set or in a Cloud Run job), got %q", ps.Project)
	}
	if err := googleAPIOrigin(ps.Endpoint); err != nil {
		return ps, p, fmt.Errorf("PUBSUB_ENDPOINT: %w", err)
	}
	if p.AllowInsecure || (job && p == Push{}) {
		return ps, p, nil
	}
	if err := bareHTTPSOrigin(p.Audience); err != nil {
		return ps, p, fmt.Errorf("PUSH_AUDIENCE (the service's run.app URL; required unless ALLOW_INSECURE_PUSH=true): %w", err)
	}
	if !pushSAEmail.MatchString(p.SAEmail) {
		return ps, p, fmt.Errorf("PUSH_SA_EMAIL must be the push service account's address (required unless ALLOW_INSECURE_PUSH=true), got %q", p.SAEmail)
	}
	if !pushSAID.MatchString(p.SAID) {
		return ps, p, fmt.Errorf("PUSH_SA_ID must be the push service account's unique ID, digits only (required unless ALLOW_INSECURE_PUSH=true), got %q", p.SAID)
	}
	return ps, p, nil
}

// googleAPIHost is a host under googleapis.com: pubsub.googleapis.com,
// europe-west1-pubsub.googleapis.com.
var googleAPIHost = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+googleapis\.com$`)

// googleAPIOrigin accepts https://<host>.googleapis.com and nothing else: no port, path,
// query, fragment or userinfo. The service account's access token goes with every publish,
// so the endpoint must be Google's (a typo or an injected variable would hand the token
// to another host).
func googleAPIOrigin(s string) error {
	if err := bareHTTPSOrigin(s); err != nil {
		return err
	}
	if u, _ := url.Parse(s); !googleAPIHost.MatchString(u.Host) {
		return fmt.Errorf("%q must be a Google API host (https://<name>.googleapis.com, no port): the access token is sent there", s)
	}
	return nil
}

// bareHTTPSOrigin accepts https://host[:port] and nothing else: no path (a trailing
// slash included), query, fragment or userinfo. The push audience is compared for
// equality, so a trailing slash would refuse every token.
func bareHTTPSOrigin(s string) error {
	u, err := url.Parse(s)
	switch {
	case s == "":
		return errors.New("empty")
	case err != nil:
		return fmt.Errorf("%q is not a URL", s)
	case u.Scheme != "https" || u.Host == "" || u.Hostname() == "":
		return fmt.Errorf("%q must be https://host", s)
	case u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "" || strings.HasSuffix(u.Host, ":"):
		return fmt.Errorf("%q must be an origin only: no path, trailing slash, query, fragment or userinfo", s)
	case u.Host != strings.ToLower(u.Host):
		return fmt.Errorf("%q: the host must be lower case", s)
	}
	return nil
}

// Auth configures how ingest and api authenticate requests.
type Auth struct {
	Mode string

	// supabase mode
	ProjectRef string // <ref> in https://<ref>.supabase.co
	Issuer     string // expected iss; default https://<ref>.supabase.co/auth/v1
	JWKSURL    string // default <Issuer>/.well-known/jwks.json
	AdminKey   string // SUPABASE_SERVICE_ROLE_KEY, api only (account deletion); optional

	// apikey mode
	APIKey        string
	APIKeyUserID  uuid.UUID
	AllowInsecure bool
}

// Load reads the settings common to all services. It refuses what cannot run (no
// DATABASE_URL, a Pub/Sub or push setting loadPubSub refuses, XFF_TRUSTED_HOPS outside
// 1 to 8) so the service fails at start-up, before it connects to anything, instead of
// limping. service picks the pool size: scoring and notify hold fewer connections.
func Load(service string) (Config, error) {
	c := Config{
		ServiceName:    service,
		Port:           getenv("PORT", "8080"),
		MetricsPort:    getenv("METRICS_PORT", "9090"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		DBMaxConns:     4,
		XFFTrustedHops: DefaultXFFTrustedHops,
	}
	var err error
	if c.PubSub, c.Push, err = loadPubSub(); err != nil {
		return c, err
	}
	if service == "scoring" || service == "notify" {
		c.DBMaxConns = 2
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	if v := os.Getenv("DB_MAX_CONNS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return c, fmt.Errorf("DB_MAX_CONNS must be 1-100, got %q", v)
		}
		c.DBMaxConns = n
	}
	// A wrong hop count moves the limits' key: one too many makes a caller's forged entry its key,
	// so anything but a whole number in range stops the start.
	if v := os.Getenv("XFF_TRUSTED_HOPS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > MaxXFFTrustedHops {
			return c, fmt.Errorf("XFF_TRUSTED_HOPS must be 1-%d (the X-Forwarded-For entries our proxies append), got %q", MaxXFFTrustedHops, v)
		}
		c.XFFTrustedHops = n
	}
	return c, nil
}

// projectRef matches a Supabase project reference (lowercase letters/digits).
var projectRef = regexp.MustCompile(`^[a-z0-9]{10,40}$`)

// LoadAuth reads and validates AUTH_MODE and its settings. It refuses
// placeholders and insecure combinations rather than starting half-configured.
func LoadAuth() (Auth, error) {
	a := Auth{
		Mode:          getenv("AUTH_MODE", AuthSupabase),
		ProjectRef:    strings.TrimSpace(os.Getenv("SUPABASE_PROJECT_REF")),
		Issuer:        strings.TrimRight(os.Getenv("SUPABASE_ISSUER"), "/"),
		JWKSURL:       os.Getenv("SUPABASE_JWKS_URL"),
		AdminKey:      strings.TrimSpace(os.Getenv("SUPABASE_SERVICE_ROLE_KEY")),
		APIKey:        os.Getenv("API_KEY"),
		APIKeyUserID:  DefaultUserID,
		AllowInsecure: os.Getenv("ALLOW_INSECURE_APIKEY_AUTH") == "true",
	}
	if err := refuseAPIKeyOnCloudRun(a.Mode); err != nil {
		return a, err
	}
	switch a.Mode {
	case AuthSupabase:
		if a.Issuer == "" || a.JWKSURL == "" {
			if !projectRef.MatchString(a.ProjectRef) { // D33: the services refuse to start on the placeholder
				return a, fmt.Errorf("SUPABASE_PROJECT_REF must be your project reference (Supabase → Project Settings), got %q", a.ProjectRef)
			}
		}
		if a.Issuer == "" {
			a.Issuer = "https://" + a.ProjectRef + ".supabase.co/auth/v1"
		}
		if a.JWKSURL == "" {
			a.JWKSURL = a.Issuer + "/.well-known/jwks.json"
		}
	case AuthAPIKey:
		if !a.AllowInsecure {
			return a, fmt.Errorf("AUTH_MODE=apikey is for local development only; set ALLOW_INSECURE_APIKEY_AUTH=true to use it")
		}
		if len(a.APIKey) < 16 {
			return a, fmt.Errorf("AUTH_MODE=apikey needs API_KEY with at least 16 characters")
		}
		if v := os.Getenv("USER_ID"); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				return a, fmt.Errorf("USER_ID: %w", err)
			}
			a.APIKeyUserID = id
		}
	default:
		return a, fmt.Errorf("AUTH_MODE must be %q or %q, got %q", AuthSupabase, AuthAPIKey, a.Mode)
	}
	return a, nil
}

// apiKeyEnv are apikey mode's variables: local development only.
var apiKeyEnv = []string{"ALLOW_INSECURE_APIKEY_AUTH", "API_KEY", "USER_ID"}

// refuseAPIKeyOnCloudRun is admission policy P5 in the code (D222): with K_SERVICE or
// CLOUD_RUN_JOB set, AUTH_MODE=apikey and apikey mode's variables are refused, so a
// misconfigured revision cannot start with a shared key. scripts/check_cloudrun.py
// rejects the same names in deploy/run. The error names the variable, never its value.
func refuseAPIKeyOnCloudRun(mode string) error {
	for _, k := range cloudRunEnv {
		if os.Getenv(k) == "" {
			continue
		}
		if mode == AuthAPIKey {
			return fmt.Errorf("AUTH_MODE=apikey is refused on Cloud Run (%s is set): only supabase there (P5, D222)", k)
		}
		for _, n := range apiKeyEnv {
			if os.Getenv(n) != "" {
				return fmt.Errorf("%s is refused on Cloud Run (%s is set): it belongs to apikey mode, local development only (P5, D222)", n, k)
			}
		}
	}
	return nil
}

// Mode is how a gate acts on a request it would refuse (D87).
type Mode string

// The gate modes. off skips the check, warn serves the request and reports the code, on
// refuses it.
const (
	ModeOff  Mode = "off"  // not checked
	ModeWarn Mode = "warn" // served, with the code in X-RYFT-Gate, counted and logged
	ModeOn   Mode = "on"   // refused with 403 and the code
)

func parseMode(name, def string) (Mode, error) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	if v == "" {
		v = def
	}
	switch m := Mode(v); m {
	case ModeOff, ModeWarn, ModeOn:
		return m, nil
	}
	return "", fmt.Errorf("%s must be off, warn or on, got %q", name, v)
}

// Beta configures the invite gate, the consent check and the privacy policy
// consent is given to (api, ingest; scoring reads Consent).
type Beta struct {
	Gate                 Mode   // BETA_GATE: accounts without a redeemed code (default off)
	Consent              Mode   // CONSENT_ENFORCE: syncs and scoring without consent (default on)
	PrivacyPolicyVersion string // PRIVACY_POLICY_VERSION, a YYYY-MM-DD date; empty when unset
	PrivacyPolicyURL     string // PRIVACY_POLICY_URL
}

// LoadBeta reads BETA_GATE (off|warn|on, default off), CONSENT_ENFORCE (off|warn|on,
// default on), PRIVACY_POLICY_VERSION (no default: trimmed, and a YYYY-MM-DD date
// when set) and PRIVACY_POLICY_URL (default the site's /privacy). An unset version is
// no error here, so scoring and its subcommands start without it; api and ingest
// call RequirePolicy.
func LoadBeta() (Beta, error) {
	b := Beta{PrivacyPolicyVersion: strings.TrimSpace(os.Getenv("PRIVACY_POLICY_VERSION")),
		PrivacyPolicyURL: getenv("PRIVACY_POLICY_URL", "https://ryftlab.de/privacy")}
	var err error
	if b.Gate, err = parseMode("BETA_GATE", string(ModeOff)); err != nil {
		return b, err
	}
	if b.Consent, err = parseMode("CONSENT_ENFORCE", string(ModeOn)); err != nil {
		return b, err
	}
	// Kustomize env files keep quotes and inline comments in the value: they fail
	// here instead of becoming the version users consent to.
	if v := b.PrivacyPolicyVersion; v != "" {
		if _, err := time.Parse(time.DateOnly, v); err != nil {
			return b, fmt.Errorf("PRIVACY_POLICY_VERSION must be a date (YYYY-MM-DD), got %q", v)
		}
	}
	return b, nil
}

// RequirePolicy refuses a Beta without a privacy policy version. api and ingest
// call it at start-up, before they connect to anything; scoring does not (any
// recorded version counts there, D78).
func (b Beta) RequirePolicy() error {
	if b.PrivacyPolicyVersion == "" {
		return errors.New("PRIVACY_POLICY_VERSION is required: api and ingest record and enforce consent against it (D78); " +
			"set the published version (deploy/k8s/base/<service>/config.env, docker-compose.yml)")
	}
	return nil
}

// Admin configures the operator's /v1/admin routes (api only).
type Admin struct {
	Key          string   // ADMIN_API_KEY; empty: the routes are off
	WebOrigins   []string // ADMIN_WEB_ORIGINS: the origins of the operator's page; empty: no CORS at all
	PublicWebURL string   // PUBLIC_WEB_URL: the site's origin, the base of an invite link
}

// DefaultPublicWebURL is the site that serves the invite link and the operator's page.
const DefaultPublicWebURL = "https://ryftlab.de"

// maxWebOrigins bounds ADMIN_WEB_ORIGINS: the page and a development origin or two.
const maxWebOrigins = 5

// LoadAdmin reads ADMIN_API_KEY, the bearer of the operator's /v1/admin routes (api only; infra/vault
// generates it). Empty disables them; a short key is refused rather than guessable.
//
// ADMIN_WEB_ORIGINS is a comma-separated list of the exact origins whose pages may read the admin
// routes' answers (CORS); unset or empty means none. An entry that is not a bare origin stops the
// api at start-up (a typo must not silently open or close the page): see parseOrigin.
// PUBLIC_WEB_URL defaults to DefaultPublicWebURL and must be one origin of the same form.
func LoadAdmin() (Admin, error) {
	a := Admin{Key: strings.TrimSpace(os.Getenv("ADMIN_API_KEY"))}
	if a.Key != "" && len(a.Key) < 32 {
		return Admin{}, fmt.Errorf("ADMIN_API_KEY must have at least 32 characters")
	}
	var err error
	if a.WebOrigins, err = parseOrigins(os.Getenv("ADMIN_WEB_ORIGINS")); err != nil {
		return Admin{}, fmt.Errorf("ADMIN_WEB_ORIGINS: %w", err)
	}
	a.PublicWebURL = strings.TrimSpace(getenv("PUBLIC_WEB_URL", DefaultPublicWebURL))
	if err := parseOrigin(a.PublicWebURL); err != nil {
		return Admin{}, fmt.Errorf("PUBLIC_WEB_URL: %w", err)
	}
	return a, nil
}

// parseOrigins reads a comma-separated list of at most 5 origins, trimmed and deduplicated. Blank
// means none; an empty entry inside a list is an error.
func parseOrigins(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []string
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if err := parseOrigin(entry); err != nil {
			return nil, err
		}
		if !slices.Contains(out, entry) {
			out = append(out, entry)
		}
	}
	if len(out) > maxWebOrigins {
		return nil, fmt.Errorf("at most %d origins, got %d", maxWebOrigins, len(out))
	}
	return out, nil
}

// parseOrigin accepts a bare origin exactly as a browser writes it in the Origin header: scheme
// https (http only for localhost, 127.0.0.1 and [::1]), a lower-case host, an explicit port only when
// it is not the scheme's default, and nothing else: no userinfo, path (a trailing slash included),
// query or fragment. CORS compares these strings for equality, so what the browser would not send
// can never match, and it is refused here instead of failing silently in production.
func parseOrigin(s string) error {
	if s == "" {
		return errors.New("empty origin")
	}
	if strings.ContainsAny(s, " \t\r\n\"'`\\*") {
		return fmt.Errorf("origin %q holds a space, quote, backslash or wildcard", s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("origin %q is not a URL", s)
	}
	host := u.Hostname()
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1"):
	default:
		return fmt.Errorf("origin %q: scheme must be https (http only for localhost, 127.0.0.1 and [::1])", s)
	}
	switch {
	case host == "" || host != strings.ToLower(host):
		return fmt.Errorf("origin %q: the host must be present and lower case", s)
	case u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "":
		return fmt.Errorf("origin %q: no userinfo, path, trailing slash, query or fragment", s)
	case (u.Scheme == "https" && u.Port() == "443") || (u.Scheme == "http" && u.Port() == "80"):
		return fmt.Errorf("origin %q: a browser leaves the default port out of the origin", s)
	case u.Scheme+"://"+u.Host != s || strings.HasSuffix(u.Host, ":"):
		return fmt.Errorf("origin %q is not written the way a browser sends it (%s://%s)", s, u.Scheme, u.Host)
	}
	return nil
}

// APNs configures notify's push sending: the APNs key (Apple Developer → Keys,
// "Apple Push Notifications service"), the team and the app's bundle id.
type APNs struct {
	KeyID, TeamID, PrivateKey, Topic string
}

// DefaultAPNsTopic is the app's bundle id.
const DefaultAPNsTopic = "dev.timkrebs.ryft"

// LoadAPNs reads APNS_KEY_ID, APNS_TEAM_ID, APNS_KEY_P8 and APNS_TOPIC. The
// three keys decide: all set enables sending, none disables it (ok false: notify
// runs and counts what it would have sent), some is an error.
func LoadAPNs() (a APNs, ok bool, err error) {
	a = APNs{KeyID: strings.TrimSpace(os.Getenv("APNS_KEY_ID")), TeamID: strings.TrimSpace(os.Getenv("APNS_TEAM_ID")),
		PrivateKey: os.Getenv("APNS_KEY_P8"), Topic: getenv("APNS_TOPIC", DefaultAPNsTopic)}
	set := 0
	for _, v := range []string{a.KeyID, a.TeamID, strings.TrimSpace(a.PrivateKey)} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		return a, false, nil
	case 3:
		return a, true, nil
	}
	return a, false, errors.New("set all of APNS_KEY_ID, APNS_TEAM_ID and APNS_KEY_P8, or none")
}

// Apple configures Sign in with Apple token revocation (api only): the
// developer-account values and the key that encrypts stored refresh tokens.
type Apple struct {
	TeamID, KeyID, ClientID, PrivateKey string
	DataKey                             string // DATA_ENCRYPTION_KEY, base64 32 bytes (infra/vault generates it)
}

// LoadApple reads APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_CLIENT_ID, APPLE_KEY_P8 and
// DATA_ENCRYPTION_KEY. The four APPLE_* decide: all set enables revocation, none
// disables it (ok false), some is an error, so a half-configured deployment does not
// start. DATA_ENCRYPTION_KEY is required only when revocation is enabled: infra/vault
// generates it for the api whether or not the Apple keys exist yet.
func LoadApple() (Apple, bool, error) {
	a := Apple{
		TeamID: strings.TrimSpace(os.Getenv("APPLE_TEAM_ID")), KeyID: strings.TrimSpace(os.Getenv("APPLE_KEY_ID")),
		ClientID: strings.TrimSpace(os.Getenv("APPLE_CLIENT_ID")), PrivateKey: os.Getenv("APPLE_KEY_P8"),
		DataKey: os.Getenv("DATA_ENCRYPTION_KEY"),
	}
	set := 0
	for _, v := range []string{a.TeamID, a.KeyID, a.ClientID, a.PrivateKey} {
		if v != "" {
			set++
		}
	}
	switch {
	case set == 0:
		return a, false, nil
	case set < 4:
		return a, false, fmt.Errorf("apple token revocation needs APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_CLIENT_ID and APPLE_KEY_P8 together (%d of 4 set)", set)
	case a.DataKey == "":
		return a, false, errors.New("apple token revocation needs DATA_ENCRYPTION_KEY to seal the stored refresh tokens")
	default:
		return a, true, nil
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
