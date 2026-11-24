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
)

// Connect configures direct connections (D257, design 9) in api and ingest.
type Connect struct {
	// Providers are the served providers (CONNECT_PROVIDERS, comma-separated ids, sorted, each once); empty serves
	// none, which is production until phase 2. api and ingest must be given the same list (check_cloudrun.py).
	Providers []string
	// Fake is CONNECT_FAKE=on: the fake provider may be served. Compose only; refused on Cloud Run.
	Fake bool
	// FakePushSecret signs and verifies the fake's pushes (CONNECT_FAKE_PUSH_SECRET, ingest and the smoke).
	FakePushSecret string

	// MinPolicyVersion is CONNECT_MIN_POLICY_VERSION (ingest only): the oldest privacy policy version whose consent the
	// hook and the consumer accept (D259). Trimmed; empty or a YYYY-MM-DD date not after PRIVACY_POLICY_VERSION. Unset it
	// is PRIVACY_POLICY_VERSION itself, the policy in force. The api's start and confirm ignore it.
	MinPolicyVersion string

	// The rest is the api's. CallbackBase is CONNECT_CALLBACK_BASE: the redirect URI is
	// <base>/v1/connect/<provider>/callback. Caps are CONNECT_<PROVIDER>_USER_CAP, each at least 1 for a served
	// provider. Clients are the real providers' OAuth client id and secret (CONNECT_<PROVIDER>_CLIENT_ID and
	// _CLIENT_SECRET; the fake has none). DataKey is DATA_ENCRYPTION_KEY, which seals the tokens.
	CallbackBase string
	Caps         map[string]int
	Clients      map[string]ConnectClient
	DataKey      string
}

// ConnectClient is a provider's OAuth client.
type ConnectClient struct{ ID, Secret string }

// FakeName is the fake provider's id.
const FakeName = "fake"

var connectProviderID = regexp.MustCompile(`^[a-z][a-z0-9]{1,31}$`)

// MinFakeSecret is the shortest CONNECT_FAKE_PUSH_SECRET.
const MinFakeSecret = 16

// ConnectEnvName is the upper-case form of a provider id in a variable name.
func ConnectEnvName(provider string) string { return strings.ToUpper(provider) }

// LoadConnect reads the settings of service ("api" or "ingest"). It refuses what must not run: CONNECT_FAKE on Cloud
// Run (K_SERVICE or CLOUD_RUN_JOB), "fake" served without CONNECT_FAKE=on, a malformed or repeated provider id (and
// the id "unknown", which the callback's redirect uses for a path without a provider), and for the api a provider
// without its cap (at least 1), the data key, a callback base that is not an https origin (http only for localhost)
// and, for a real provider, its OAuth client. No error holds a secret.
func LoadConnect(service string) (Connect, error) {
	c := Connect{Caps: map[string]int{}, Clients: map[string]ConnectClient{}}
	switch v := strings.TrimSpace(os.Getenv("CONNECT_FAKE")); v {
	case "", "off":
	case "on":
		c.Fake = true
	default:
		return c, fmt.Errorf("CONNECT_FAKE must be on or off, got %q", v)
	}
	if c.Fake {
		for _, k := range cloudRunEnv {
			if os.Getenv(k) != "" {
				return c, errors.New("CONNECT_FAKE=on is refused on Cloud Run (K_SERVICE or CLOUD_RUN_JOB is set): the fake provider is for compose and tests (D257)")
			}
		}
	}
	if raw := strings.TrimSpace(os.Getenv("CONNECT_PROVIDERS")); raw != "" {
		for _, p := range strings.Split(raw, ",") {
			p = strings.TrimSpace(p)
			switch {
			case !connectProviderID.MatchString(p) || p == "unknown":
				return c, fmt.Errorf("CONNECT_PROVIDERS: %q is not a provider id (lower-case letters and digits, 2 to 32 characters)", p)
			case slices.Contains(c.Providers, p):
				return c, fmt.Errorf("CONNECT_PROVIDERS: %q twice", p)
			}
			c.Providers = append(c.Providers, p)
		}
		slices.Sort(c.Providers)
	}
	if slices.Contains(c.Providers, FakeName) && !c.Fake {
		return c, errors.New("CONNECT_PROVIDERS names fake, which needs CONNECT_FAKE=on (compose only)")
	}
	if c.Fake {
		c.FakePushSecret = os.Getenv("CONNECT_FAKE_PUSH_SECRET")
		if service == "ingest" && slices.Contains(c.Providers, FakeName) && len(c.FakePushSecret) < MinFakeSecret {
			return c, fmt.Errorf("CONNECT_FAKE_PUSH_SECRET must be at least %d characters to serve fake", MinFakeSecret)
		}
	}
	if service == "ingest" {
		var err error
		if c.MinPolicyVersion, err = minPolicyVersion(os.Getenv("CONNECT_MIN_POLICY_VERSION"), os.Getenv("PRIVACY_POLICY_VERSION")); err != nil {
			return c, err
		}
	}
	if service != "api" {
		return c, nil
	}
	// The key is read whatever is served: the api pins it, and tokens queued earlier still have to be opened.
	c.DataKey = os.Getenv("DATA_ENCRYPTION_KEY")
	if len(c.Providers) == 0 {
		return c, nil
	}
	c.CallbackBase = strings.TrimSpace(os.Getenv("CONNECT_CALLBACK_BASE"))
	if err := originHTTPSOrLocal(c.CallbackBase); err != nil {
		return c, fmt.Errorf("CONNECT_CALLBACK_BASE: %w", err)
	}
	if c.DataKey == "" {
		return c, errors.New("serving a connection provider needs DATA_ENCRYPTION_KEY to seal its tokens")
	}
	for _, p := range c.Providers {
		name := ConnectEnvName(p)
		raw := strings.TrimSpace(os.Getenv("CONNECT_" + name + "_USER_CAP"))
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1_000_000 {
			return c, fmt.Errorf("CONNECT_%s_USER_CAP must be a whole number from 1 (the provider's user cap), got %q", name, raw)
		}
		c.Caps[p] = n
		if p == FakeName {
			continue
		}
		cl := ConnectClient{ID: strings.TrimSpace(os.Getenv("CONNECT_" + name + "_CLIENT_ID")), Secret: os.Getenv("CONNECT_" + name + "_CLIENT_SECRET")}
		if cl.ID == "" || cl.Secret == "" {
			return c, fmt.Errorf("serving %s needs CONNECT_%s_CLIENT_ID and CONNECT_%s_CLIENT_SECRET", p, name, name)
		}
		c.Clients[p] = cl
	}
	return c, nil
}

// minPolicyVersion validates CONNECT_MIN_POLICY_VERSION against PRIVACY_POLICY_VERSION (both as the environment gave
// them): the cutoff is trimmed, empty or a calendar date, and not after the policy in force; empty is the policy.
// An unset policy version is LoadBeta's and RequirePolicy's to refuse, so a cutoff is then taken as it is.
func minPolicyVersion(raw, policyRaw string) (string, error) {
	min, policy := strings.TrimSpace(raw), strings.TrimSpace(policyRaw)
	if min == "" {
		return policy, nil
	}
	if _, err := time.Parse(time.DateOnly, min); err != nil {
		return "", fmt.Errorf("CONNECT_MIN_POLICY_VERSION must be empty or a date (YYYY-MM-DD), got %q", min)
	}
	if policy != "" && min > policy {
		return "", fmt.Errorf("CONNECT_MIN_POLICY_VERSION %s is after PRIVACY_POLICY_VERSION %s: no consent could meet it", min, policy)
	}
	return min, nil
}

// originHTTPSOrLocal accepts https://host[:port] and, for localhost, 127.0.0.1 and [::1], http://host[:port]: an
// origin only, no path, query, fragment or userinfo.
func originHTTPSOrLocal(s string) error {
	u, err := url.Parse(s)
	switch {
	case s == "":
		return errors.New("required while a provider is served (https://api.ryftlab.de)")
	case err != nil || u.Host == "" || u.Hostname() == "":
		return fmt.Errorf("%q is not a URL", s)
	case u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || u.Opaque != "":
		return fmt.Errorf("%q must be an origin only: no path, trailing slash, query, fragment or userinfo", s)
	case u.Scheme == "https":
	case u.Scheme == "http" && slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname()):
	default:
		return fmt.Errorf("%q must be https (http only for localhost, 127.0.0.1 and [::1])", s)
	}
	return nil
}
