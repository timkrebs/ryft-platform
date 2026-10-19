package config

import (
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
	setAuthEnv(t, map[string]string{"SUPABASE_ISSUER": "https://auth.ryftapp.de/auth/v1/", "SUPABASE_JWKS_URL": "http://gotrue:9999/.well-known/jwks.json"})
	if a, err = LoadAuth(); err != nil || strings.HasSuffix(a.Issuer, "/") {
		t.Fatalf("%+v %v", a, err)
	}
}

func TestLoadNatsCredentialsComeInPairs(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	for env, ok := range map[[2]string]bool{
		{"", ""}: true, {"ingest", "secret"}: true, {"ingest", ""}: false, {"", "secret"}: false,
	} {
		t.Setenv("NATS_USER", env[0])
		t.Setenv("NATS_PASSWORD", env[1])
		c, err := Load("ingest")
		if (err == nil) != ok {
			t.Errorf("NATS_USER=%q NATS_PASSWORD=%q: err %v", env[0], env[1], err)
		}
		if ok && (c.NatsUser != env[0] || c.NatsPass != env[1]) {
			t.Errorf("got %q/%q", c.NatsUser, c.NatsPass)
		}
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
