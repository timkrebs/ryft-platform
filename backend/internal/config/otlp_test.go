package config

import (
	"strings"
	"testing"
)

const gateway = "https://otlp-gateway-prod-eu-west-2.grafana.net/otlp"

// testOTLPToken is built at run time: no literal that looks like a credential.
func testOTLPToken() string { return "glc_" + strings.Repeat("x7", 24) }

func setOTLPEnv(t *testing.T, endpoint, user, token string) {
	t.Helper()
	t.Setenv("OTLP_ENDPOINT", endpoint)
	t.Setenv("OTLP_USER", user)
	t.Setenv("OTLP_TOKEN", token)
}

func TestLoadOTLPOff(t *testing.T) {
	// Cloud Run injects the token into every service, configured or not: it is dropped.
	for _, token := range []string{"", testOTLPToken(), "short"} {
		setOTLPEnv(t, " ", "", token)
		o, err := LoadOTLP()
		if err != nil || o != (OTLP{}) {
			t.Fatalf("both empty (token %d chars): %+v %v", len(token), o, err)
		}
	}
}

func TestLoadOTLPOn(t *testing.T) {
	tok := testOTLPToken()
	setOTLPEnv(t, " "+gateway+" ", " 1234567 ", tok+"\n") // a secret file's newline is trimmed
	o, err := LoadOTLP()
	if err != nil {
		t.Fatal(err)
	}
	if o.Endpoint != gateway || o.User != "1234567" || o.Token != tok {
		t.Fatalf("got endpoint %q, user %q, token of %d chars", o.Endpoint, o.User, len(o.Token))
	}
	for _, host := range []string{"otlp-gateway-prod-us-central-0", "otlp-gateway-prod-gb-south-0", "otlp-gateway-x"} {
		setOTLPEnv(t, "https://"+host+".grafana.net/otlp", "42", tok)
		if _, err := LoadOTLP(); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
}

func TestLoadOTLPRefuses(t *testing.T) {
	tok := testOTLPToken()
	cases := []struct{ name, endpoint, user, token string }{
		{"user without endpoint", "", "1234567", tok},
		{"endpoint without user", gateway, "", tok},
		{"no token", gateway, "1234567", ""},
		{"a blank token", gateway, "1234567", "  \n"},
		{"a short token", gateway, "1234567", "glc_abc"},
		{"a long token", gateway, "1234567", strings.Repeat("a", 4097)},
		{"a space in the token", gateway, "1234567", tok[:10] + " " + tok[10:]},
		{"a newline in the token", gateway, "1234567", tok[:10] + "\n" + tok[10:]},
		{"a byte outside ASCII", gateway, "1234567", tok + "é"},
		{"a user that is no instance ID", gateway, "ryft", tok},
		{"a negative user", gateway, "-1", tok},
		{"http", "http://otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1", tok},
		{"no scheme", "otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1", tok},
		{"not a URL", "https://otlp gateway/otlp", "1", tok},
		{"opaque", "https:otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1", tok},
		{"no host", "https:///otlp", "1", tok},
		{"a port", "https://otlp-gateway-prod-eu-west-2.grafana.net:443/otlp", "1", tok},
		{"an empty port", "https://otlp-gateway-prod-eu-west-2.grafana.net:/otlp", "1", tok},
		{"another host under grafana.net", "https://ryft.grafana.net/otlp", "1", tok},
		{"a deeper name", "https://a.otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1", tok},
		{"a look-alike domain", "https://otlp-gateway-prod-eu-west-2.grafana.net.example.com/otlp", "1", tok},
		{"a trailing hyphen", "https://otlp-gateway-.grafana.net/otlp", "1", tok},
		{"upper case", "https://OTLP-gateway-prod-eu-west-2.grafana.net/otlp", "1", tok},
		{"an IP address", "https://192.0.2.1/otlp", "1", tok},
		{"no path", "https://otlp-gateway-prod-eu-west-2.grafana.net", "1", tok},
		{"a trailing slash", gateway + "/", "1", tok},
		{"the whole push URL", gateway + "/v1/metrics", "1", tok},
		{"another path", "https://otlp-gateway-prod-eu-west-2.grafana.net/api/prom/push", "1", tok},
		{"an escaped path", "https://otlp-gateway-prod-eu-west-2.grafana.net/%6Ftlp", "1", tok},
		{"a query", gateway + "?x=1", "1", tok},
		{"an empty query", gateway + "?", "1", tok},
		{"a fragment", gateway + "#x", "1", tok},
		{"userinfo", "https://1234567:" + tok + "@otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1", tok},
	}
	for _, c := range cases {
		setOTLPEnv(t, c.endpoint, c.user, c.token)
		o, err := LoadOTLP()
		if err == nil {
			t.Errorf("%s: accepted %+v", c.name, o)
			continue
		}
		if o != (OTLP{}) {
			t.Errorf("%s: an error with a value: %+v", c.name, o)
		}
	}
}

// TestLoadOTLPErrorsQuoteNoSecret puts the token where it does not belong (the user,
// the endpoint's userinfo or query) and checks that no error carries it, nor the values.
func TestLoadOTLPErrorsQuoteNoSecret(t *testing.T) {
	tok := testOTLPToken()
	for _, env := range [][3]string{
		{gateway, tok, tok},
		{"https://1234567:" + tok + "@otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1234567", tok},
		{"https://" + tok + "@otlp-gateway-prod-eu-west-2.grafana.net/otlp", "1234567", tok},
		{gateway + "?token=" + tok, "1234567", tok},
		{"https://" + tok + ".example.com/otlp", "1234567", tok},
		{gateway, "1234567", tok + " " + tok},
		{"", tok, ""},
	} {
		setOTLPEnv(t, env[0], env[1], env[2])
		_, err := LoadOTLP()
		if err == nil {
			t.Fatalf("accepted %q", env[0])
		}
		if strings.Contains(err.Error(), tok[4:20]) {
			t.Errorf("the error carries the token: %v", err)
		}
	}
}
