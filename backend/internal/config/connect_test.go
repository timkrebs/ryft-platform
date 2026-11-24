package config

import (
	"strings"
	"testing"
)

// env sets the variables of one case and clears the others this package reads.
func connectEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for _, k := range []string{"CONNECT_FAKE", "CONNECT_PROVIDERS", "CONNECT_FAKE_PUSH_SECRET", "CONNECT_CALLBACK_BASE", "CONNECT_FAKE_USER_CAP",
		"CONNECT_GARMIN_USER_CAP", "CONNECT_GARMIN_CLIENT_ID", "CONNECT_GARMIN_CLIENT_SECRET", "CONNECT_MIN_POLICY_VERSION", "PRIVACY_POLICY_VERSION", "DATA_ENCRYPTION_KEY", "K_SERVICE", "CLOUD_RUN_JOB"} {
		t.Setenv(k, "")
	}
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadConnectProduction(t *testing.T) {
	// Production until phase 2: no provider, and a cap of 0 is fine because none is served.
	connectEnv(t, map[string]string{"K_SERVICE": "ryft-api", "CONNECT_GARMIN_USER_CAP": "0", "CONNECT_CALLBACK_BASE": "https://api.ryftlab.de"})
	for _, svc := range []string{"api", "ingest"} {
		c, err := LoadConnect(svc)
		if err != nil || len(c.Providers) != 0 || c.Fake {
			t.Fatalf("%s: %+v %v", svc, c, err)
		}
	}
}

func TestLoadConnectFakeInCompose(t *testing.T) {
	connectEnv(t, map[string]string{"CONNECT_FAKE": "on", "CONNECT_PROVIDERS": "fake", "CONNECT_FAKE_PUSH_SECRET": strings.Repeat("s", 20),
		"CONNECT_CALLBACK_BASE": "http://localhost:8082", "CONNECT_FAKE_USER_CAP": "5", "DATA_ENCRYPTION_KEY": "x"})
	c, err := LoadConnect("api")
	if err != nil || !c.Fake || c.Caps["fake"] != 5 || c.CallbackBase != "http://localhost:8082" || len(c.Clients) != 0 {
		t.Fatalf("api: %+v %v", c, err)
	}
	c, err = LoadConnect("ingest")
	if err != nil || c.FakePushSecret == "" || len(c.Providers) != 1 {
		t.Fatalf("ingest: %+v %v", c, err)
	}
}

func TestLoadConnectRefusals(t *testing.T) {
	good := map[string]string{"CONNECT_FAKE": "on", "CONNECT_PROVIDERS": "fake", "CONNECT_FAKE_PUSH_SECRET": strings.Repeat("s", 20),
		"CONNECT_CALLBACK_BASE": "http://localhost:8082", "CONNECT_FAKE_USER_CAP": "5", "DATA_ENCRYPTION_KEY": "x"}
	with := func(kv map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range kv {
			m[k] = v
		}
		return m
	}
	for name, c := range map[string]struct {
		env map[string]string
		svc string
		msg string
	}{
		"the fake on Cloud Run":            {with(map[string]string{"K_SERVICE": "ryft-api"}), "api", "Cloud Run"},
		"the fake in a Cloud Run job":      {with(map[string]string{"CLOUD_RUN_JOB": "fold"}), "ingest", "Cloud Run"},
		"the fake on Cloud Run, no list":   {with(map[string]string{"K_SERVICE": "ryft-ingest", "CONNECT_PROVIDERS": ""}), "ingest", "Cloud Run"},
		"fake served without the switch":   {with(map[string]string{"CONNECT_FAKE": ""}), "api", "CONNECT_FAKE=on"},
		"a bad switch":                     {with(map[string]string{"CONNECT_FAKE": "yes"}), "api", "CONNECT_FAKE must be"},
		"a malformed id":                   {with(map[string]string{"CONNECT_PROVIDERS": "Fake"}), "api", "not a provider id"},
		"the reserved id":                  {with(map[string]string{"CONNECT_PROVIDERS": "unknown"}), "api", "not a provider id"},
		"a repeated id":                    {with(map[string]string{"CONNECT_PROVIDERS": "fake,fake"}), "api", "twice"},
		"no cap":                           {with(map[string]string{"CONNECT_FAKE_USER_CAP": ""}), "api", "USER_CAP"},
		"a cap of 0":                       {with(map[string]string{"CONNECT_FAKE_USER_CAP": "0"}), "api", "USER_CAP"},
		"no data key":                      {with(map[string]string{"DATA_ENCRYPTION_KEY": ""}), "api", "DATA_ENCRYPTION_KEY"},
		"http callback base":               {with(map[string]string{"CONNECT_CALLBACK_BASE": "http://api.ryftlab.de"}), "api", "https"},
		"a callback base with a path":      {with(map[string]string{"CONNECT_CALLBACK_BASE": "https://api.ryftlab.de/v1"}), "api", "origin only"},
		"no callback base":                 {with(map[string]string{"CONNECT_CALLBACK_BASE": ""}), "api", "CONNECT_CALLBACK_BASE"},
		"a short fake secret":              {with(map[string]string{"CONNECT_FAKE_PUSH_SECRET": "short"}), "ingest", "CONNECT_FAKE_PUSH_SECRET"},
		"a real provider without a client": {with(map[string]string{"CONNECT_PROVIDERS": "garmin", "CONNECT_GARMIN_USER_CAP": "10"}), "api", "CLIENT_ID"},
	} {
		connectEnv(t, c.env)
		_, err := LoadConnect(c.svc)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("%s: %v, want an error naming %q", name, err, c.msg)
		}
	}
}

func TestLoadConnectRealProvider(t *testing.T) {
	connectEnv(t, map[string]string{"K_SERVICE": "ryft-api", "CONNECT_PROVIDERS": "garmin", "CONNECT_GARMIN_USER_CAP": "25",
		"CONNECT_GARMIN_CLIENT_ID": "id", "CONNECT_GARMIN_CLIENT_SECRET": "secret", "CONNECT_CALLBACK_BASE": "https://api.ryftlab.de", "DATA_ENCRYPTION_KEY": "k"})
	c, err := LoadConnect("api")
	if err != nil || c.Caps["garmin"] != 25 || c.Clients["garmin"].ID != "id" {
		t.Fatalf("%+v %v", c, err)
	}
	// ingest needs none of the api's settings.
	connectEnv(t, map[string]string{"K_SERVICE": "ryft-ingest", "CONNECT_PROVIDERS": "garmin"})
	if c, err = LoadConnect("ingest"); err != nil || len(c.Providers) != 1 {
		t.Fatalf("ingest: %+v %v", c, err)
	}
}

func TestLoadConnectErrorsHoldNoSecret(t *testing.T) {
	const secret = "never-print-me-0123456789"
	connectEnv(t, map[string]string{"CONNECT_FAKE": "on", "CONNECT_PROVIDERS": "fake,fake", "CONNECT_FAKE_PUSH_SECRET": secret, "DATA_ENCRYPTION_KEY": secret})
	if _, err := LoadConnect("api"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("%v", err)
	}
}

// D259: the consent cutoff is trimmed, empty or a calendar date, not after the policy in force; empty is the policy.
func TestLoadConnectMinPolicyVersion(t *testing.T) {
	for _, tc := range []struct {
		name, min, policy, want, err string
	}{
		{"unset is the policy in force", "", "2026-10-26", "2026-10-26", ""},
		{"blank is the policy in force", "  ", "2026-10-26", "2026-10-26", ""},
		{"the policy itself", "2026-10-26", "2026-10-26", "2026-10-26", ""},
		{"older than the policy", "2026-10-26", "2027-01-15", "2026-10-26", ""},
		{"trimmed", " 2026-10-26 ", "2026-12-01", "2026-10-26", ""},
		{"after the policy", "2026-12-01", "2026-10-26", "", "after PRIVACY_POLICY_VERSION"},
		{"not a date", "26-10-2026", "2026-10-26", "", "must be empty or a date"},
		{"not a calendar date", "2026-02-30", "2026-10-26", "", "must be empty or a date"},
		{"quoted", `"2026-10-26"`, "2026-10-26", "", "must be empty or a date"},
		{"a comment", "2026-10-26 # the cutoff", "2026-10-26", "", "must be empty or a date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			connectEnv(t, map[string]string{"CONNECT_MIN_POLICY_VERSION": tc.min, "PRIVACY_POLICY_VERSION": tc.policy})
			c, err := LoadConnect("ingest")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || c.MinPolicyVersion != tc.want {
				t.Fatalf("%q %v, want %q", c.MinPolicyVersion, err, tc.want)
			}
		})
	}
	// The api does not read it: a bad value there is ingest's to refuse.
	connectEnv(t, map[string]string{"CONNECT_MIN_POLICY_VERSION": "garbage", "PRIVACY_POLICY_VERSION": "2026-10-26"})
	if c, err := LoadConnect("api"); err != nil || c.MinPolicyVersion != "" {
		t.Fatalf("api: %+v %v", c, err)
	}
}
