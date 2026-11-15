package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// OTLP is where a service pushes its app metrics (D224): Grafana Cloud's OTLP gateway,
// with basic auth (User, Token). Endpoint and User both empty means no push; Token is
// then empty too.
type OTLP struct {
	Endpoint string // OTLP_ENDPOINT: https://otlp-gateway-<zone>.grafana.net/otlp
	User     string // OTLP_USER: the stack's instance ID (digits), the basic auth user
	Token    string // OTLP_TOKEN (secret): never in an error or a log line
}

// otlpGatewayHost is one of Grafana Cloud's OTLP gateways (otlp-gateway-prod-eu-west-2.grafana.net
// and its siblings), not any name under grafana.net: a stack's own <name>.grafana.net is
// named by whoever creates the stack, and the token goes with every push.
var otlpGatewayHost = regexp.MustCompile(`^otlp-gateway-[a-z0-9]([a-z0-9-]*[a-z0-9])?\.grafana\.net$`)

// otlpUser is a Grafana Cloud instance ID.
var otlpUser = regexp.MustCompile(`^[0-9]{1,20}$`)

// otlpPath is the gateway's OTLP base path; the push adds /v1/metrics. Pinned, so a URL
// pasted without it (or with /v1/metrics) fails the start instead of every push.
const otlpPath = "/otlp"

// LoadOTLP reads OTLP_ENDPOINT, OTLP_USER and OTLP_TOKEN. Both of the first two empty:
// no push, and the token is ignored (Cloud Run injects the otlp-token secret into every
// service either way). One without the other is refused. With both, the endpoint must be
// https://otlp-gateway-<zone>.grafana.net/otlp exactly (no port, userinfo, query or
// fragment, lower case), the user digits, and the token present (printable ASCII, no
// spaces, at least 16 characters; surrounding whitespace, a secret file's newline, is
// trimmed). No error quotes the endpoint, the user or the token: a token pasted into
// the wrong variable must not reach the log.
func LoadOTLP() (OTLP, error) {
	o := OTLP{
		Endpoint: strings.TrimSpace(os.Getenv("OTLP_ENDPOINT")),
		User:     strings.TrimSpace(os.Getenv("OTLP_USER")),
	}
	switch {
	case o.Endpoint == "" && o.User == "":
		return OTLP{}, nil
	case o.Endpoint == "":
		return OTLP{}, errors.New("OTLP_USER is set but OTLP_ENDPOINT is not: set both (the metrics push, D224) or neither")
	case o.User == "":
		return OTLP{}, errors.New("OTLP_ENDPOINT is set but OTLP_USER is not: set both (the metrics push, D224) or neither")
	}
	if err := otlpEndpoint(o.Endpoint); err != nil {
		return OTLP{}, fmt.Errorf("OTLP_ENDPOINT %w", err)
	}
	if !otlpUser.MatchString(o.User) {
		return OTLP{}, errors.New("OTLP_USER must be the Grafana Cloud instance ID, digits only (the stack's OpenTelemetry page)")
	}
	o.Token = strings.TrimSpace(os.Getenv("OTLP_TOKEN"))
	if err := otlpToken(o.Token); err != nil {
		return OTLP{}, err
	}
	return o, nil
}

// otlpEndpoint accepts https://otlp-gateway-<zone>.grafana.net/otlp and nothing else.
// Its errors begin with a verb, after the variable's name, and quote nothing of s.
func otlpEndpoint(s string) error {
	const want = "https://otlp-gateway-<zone>.grafana.net/otlp"
	u, err := url.Parse(s)
	switch {
	case err != nil:
		return fmt.Errorf("is not a URL; want %s", want)
	case u.Scheme != "https" || u.Opaque != "" || u.Host == "":
		return fmt.Errorf("must be %s (https)", want)
	case u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "":
		return fmt.Errorf("must hold no userinfo, query or fragment (the token goes in the Authorization header, from OTLP_TOKEN); want %s", want)
	case u.Port() != "" || strings.HasSuffix(u.Host, ":") || !otlpGatewayHost.MatchString(u.Host):
		return fmt.Errorf("must name Grafana Cloud's OTLP gateway, %s (lower case, no port): the token is sent there", want)
	case u.Path != otlpPath || u.RawPath != "":
		return fmt.Errorf("must end in the path /otlp, nothing after it (the push adds /v1/metrics); want %s", want)
	}
	return nil
}

// otlpToken checks the token's form only, and says nothing about its value.
func otlpToken(t string) error {
	switch {
	case t == "":
		return errors.New("OTLP_TOKEN (the otlp-token secret) is required when OTLP_ENDPOINT and OTLP_USER are set")
	case len(t) < 16:
		return errors.New("OTLP_TOKEN must have at least 16 characters: a Grafana Cloud access policy token")
	case len(t) > 4096:
		return errors.New("OTLP_TOKEN must have at most 4096 characters: a Grafana Cloud access policy token")
	}
	for i := 0; i < len(t); i++ {
		if c := t[i]; c <= ' ' || c > '~' {
			return errors.New("OTLP_TOKEN may hold printable ASCII only (no space, control character or other byte inside it)")
		}
	}
	return nil
}
