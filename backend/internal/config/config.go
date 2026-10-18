// Package config reads service configuration from the environment.
package config

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

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

type Config struct {
	ServiceName string
	Port        string
	MetricsPort string // METRICS_PORT, default 9090: /metrics only
	DatabaseURL string
	NatsURL     string
	NatsUser    string // NATS_USER/NATS_PASSWORD: the service's own NATS user; both or neither
	NatsPass    string
	DBMaxConns  int // pool size; small because Supavisor session mode holds a server connection per client connection
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

func Load(service string) (Config, error) {
	c := Config{
		ServiceName: service,
		Port:        getenv("PORT", "8080"),
		MetricsPort: getenv("METRICS_PORT", "9090"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		NatsURL:     getenv("NATS_URL", "nats://nats:4222"),
		DBMaxConns:  4,
	}
	if service == "scoring" {
		c.DBMaxConns = 2
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	c.NatsUser, c.NatsPass = os.Getenv("NATS_USER"), os.Getenv("NATS_PASSWORD")
	if (c.NatsUser == "") != (c.NatsPass == "") {
		return c, fmt.Errorf("set both NATS_USER and NATS_PASSWORD, or neither")
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
			if !projectRef.MatchString(a.ProjectRef) {
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

// Apple configures Sign in with Apple token revocation (api only): the
// developer-account values and the key that encrypts stored refresh tokens.
type Apple struct {
	TeamID, KeyID, ClientID, PrivateKey string
	DataKey                             string // DATA_ENCRYPTION_KEY, base64 32 bytes (infra/vault generates it)
}

// LoadApple reads APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_CLIENT_ID, APPLE_KEY_P8 and
// DATA_ENCRYPTION_KEY. All set: enabled; none: disabled (ok false); some: an
// error, so a half-configured deployment does not start.
func LoadApple() (Apple, bool, error) {
	a := Apple{
		TeamID: strings.TrimSpace(os.Getenv("APPLE_TEAM_ID")), KeyID: strings.TrimSpace(os.Getenv("APPLE_KEY_ID")),
		ClientID: strings.TrimSpace(os.Getenv("APPLE_CLIENT_ID")), PrivateKey: os.Getenv("APPLE_KEY_P8"),
		DataKey: os.Getenv("DATA_ENCRYPTION_KEY"),
	}
	set := 0
	for _, v := range []string{a.TeamID, a.KeyID, a.ClientID, a.PrivateKey, a.DataKey} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		return a, false, nil
	case 5:
		return a, true, nil
	default:
		return a, false, fmt.Errorf("apple token revocation needs APPLE_TEAM_ID, APPLE_KEY_ID, APPLE_CLIENT_ID, APPLE_KEY_P8 and DATA_ENCRYPTION_KEY together (%d of 5 set)", set)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
