package config

import (
	"os"
	"strings"
	"testing"
)

// testPostHogKey is a project key of the right form, built at run time (no key-shaped
// literal in the repository for the secret scanners to report).
func testPostHogKey() string { return "phc_" + strings.Repeat("Ab1", 15) }

func setAnalyticsEnv(t *testing.T, host, key string) {
	t.Helper()
	for k, v := range map[string]string{"POSTHOG_HOST": host, "POSTHOG_PROJECT_KEY": key} {
		t.Setenv(k, v) // restored after the test
		if v == "" {
			os.Unsetenv(k)
		}
	}
}

// Unset is off with the EU default host; set, the key is served as given (trimmed).
func TestLoadAnalytics(t *testing.T) {
	key := testPostHogKey()
	// PostHog makes a project key from 32 random bytes, URL-safe base64: 43 characters, - and _ among them.
	urlSafe := "phc_" + strings.Repeat("a-_9", 10) + "Zz-"
	longest := "phc_" + strings.Repeat("k", 128)
	for _, c := range []struct {
		name, host, key string
		want            Analytics
	}{
		{"unset", "", "", Analytics{Host: DefaultPostHogHost}},
		{"key only", "", key, Analytics{Host: DefaultPostHogHost, ProjectKey: key}},
		{"a URL-safe key", "", urlSafe, Analytics{Host: DefaultPostHogHost, ProjectKey: urlSafe}},
		{"a key of 128 characters after phc_", "", longest, Analytics{Host: DefaultPostHogHost, ProjectKey: longest}},
		{"a secret file's newline", "", key + "\n", Analytics{Host: DefaultPostHogHost, ProjectKey: key}},
		{"blank host is the default", "  ", key, Analytics{Host: DefaultPostHogHost, ProjectKey: key}},
		{"another https origin", "https://e.ryftlab.de", key, Analytics{Host: "https://e.ryftlab.de", ProjectKey: key}},
		{"host without a key", "https://eu.i.posthog.com", "", Analytics{Host: DefaultPostHogHost}},
	} {
		setAnalyticsEnv(t, c.host, c.key)
		got, err := LoadAnalytics()
		if err != nil || got != c.want {
			t.Errorf("%s: %+v, %v; want %+v", c.name, got, err, c.want)
		}
	}
}

// A host that is not an https origin stops the start, with or without a key; so does a
// key that is not a project key. No error carries the host or the key.
func TestLoadAnalyticsRefuses(t *testing.T) {
	key := testPostHogKey()
	for _, c := range []struct{ name, host, key string }{
		{"plain http", "http://eu.i.posthog.com", key},
		{"plain http without a key", "http://eu.i.posthog.com", ""},
		{"no scheme", "eu.i.posthog.com", key},
		{"another scheme", "ftp://eu.i.posthog.com", key},
		{"a path", "https://eu.i.posthog.com/capture", key},
		{"a trailing slash", "https://eu.i.posthog.com/", key},
		{"a query", "https://eu.i.posthog.com?k=v", key},
		{"a fragment", "https://eu.i.posthog.com#x", key},
		{"userinfo", "https://user:" + key + "@eu.i.posthog.com", key},
		{"a port", "https://eu.i.posthog.com:8443", key},
		{"upper case", "https://EU.i.posthog.com", key},
		{"an upper-case scheme", "HTTPS://eu.i.posthog.com", key},
		{"an IP address", "https://192.0.2.10", key},
		{"an IPv6 address", "https://[2001:db8::1]", key},
		{"a trailing dot", "https://eu.i.posthog.com.", key},
		{"a comma in the host", "https://eu,i.posthog.com", key},
		{"a single label", "https://localhost", key},
		{"the key in the host", "https://" + key + "/", key},
		{"the key as the host", key, key},
		{"the key as a host name", "https://" + strings.ToLower(key) + ".com", key},
		{"a personal API key", "", "phx_" + key[4:]},
		{"a feature flags secure key", "", "phs_" + key[4:]},
		{"a key too long", "", "phc_" + strings.Repeat("k", 129)},
		{"a key too short", "", "phc_abc"},
		{"a key with a space", "", key[:10] + " " + key[10:]},
		{"a key without its prefix", "", key[4:]},
	} {
		setAnalyticsEnv(t, c.host, c.key)
		got, err := LoadAnalytics()
		if err == nil {
			t.Errorf("%s: accepted %+v", c.name, got)
			continue
		}
		if got != (Analytics{}) {
			t.Errorf("%s: an error with a value: %+v", c.name, got)
		}
		msg := err.Error()
		// The message names the default host; a host that is part of it says nothing here.
		quotesHost := c.host != "" && !strings.Contains(DefaultPostHogHost, c.host) && strings.Contains(msg, c.host)
		if strings.Contains(msg, key[4:20]) || quotesHost {
			t.Errorf("%s: the error quotes a value: %v", c.name, err)
		}
		if !strings.HasPrefix(msg, "POSTHOG_") {
			t.Errorf("%s: the error does not name the variable: %v", c.name, err)
		}
	}
}
