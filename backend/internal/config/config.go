package config

import (
	"errors"
	"fmt"
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

// Config is what every service reads: its name, ports, database URL and pool size, and how
// it reaches the broker. The auth, beta, admin, APNs and Apple settings have their own
// loaders, because only some services need them.
type Config struct {
	ServiceName string
	Port        string
	MetricsPort string // METRICS_PORT, default 9090: /metrics only
	DatabaseURL string
	AMQP        AMQP
	DBMaxConns  int // pool size; small because Supavisor session mode holds a server connection per client connection (D34)
}

// AMQP is how a service reaches RabbitMQ: its URL, its own user, and its TLS identity
// (the broker verifies client certificates, D98).
type AMQP struct {
	URL, User, Password       string
	CAFile, CertFile, KeyFile string
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
// DATABASE_URL, half an AMQP login, a leftover EVENTS_BACKEND other than amqp, D102) so the
// pod fails at once instead of limping. service picks the pool size: scoring and notify
// hold fewer connections.
func Load(service string) (Config, error) {
	c := Config{
		ServiceName: service,
		Port:        getenv("PORT", "8080"),
		MetricsPort: getenv("METRICS_PORT", "9090"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		DBMaxConns:  4,
		AMQP: AMQP{
			URL:  getenv("AMQP_URL", "amqps://rabbitmq.ryft-rabbitmq.svc.cluster.local:5671/ryft"),
			User: os.Getenv("AMQP_USER"), Password: os.Getenv("AMQP_PASSWORD"),
			CAFile: os.Getenv("AMQP_CA_FILE"), CertFile: os.Getenv("AMQP_CERT_FILE"), KeyFile: os.Getenv("AMQP_KEY_FILE"),
		},
	}
	// EVENTS_BACKEND and EVENTS_CONSUME chose the broker during the cutover (D102);
	// RabbitMQ is the only one now, and a leftover nats is refused, not ignored.
	for _, k := range []string{"EVENTS_BACKEND", "EVENTS_CONSUME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" && v != "amqp" {
			return c, fmt.Errorf("%s=%q: RabbitMQ (amqp) is the only broker since NATS was removed (D102)", k, v)
		}
	}
	if (c.AMQP.User == "") != (c.AMQP.Password == "") {
		return c, fmt.Errorf("set both AMQP_USER and AMQP_PASSWORD, or neither")
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
