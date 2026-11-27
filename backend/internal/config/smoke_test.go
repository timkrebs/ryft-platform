package config

import (
	"strings"
	"testing"
)

var smokeVars = []string{"SMOKE_OIDC_SA_EMAIL", "SMOKE_OIDC_SA_ID", "SMOKE_OIDC_AUDIENCE", "SMOKE_USER_ID"}

// setSmokeEnv sets the supabase-mode baseline plus the given variables; setAuthEnv clears the rest.
func setSmokeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	t.Setenv("K_SERVICE", "")
	t.Setenv("CLOUD_RUN_JOB", "")
	all := map[string]string{"SUPABASE_PROJECT_REF": "abcdefghijklmnopqrst"}
	for k, v := range env {
		all[k] = v
	}
	setAuthEnv(t, all)
	for _, k := range smokeVars {
		t.Setenv(k, env[k])
	}
}

func goodSmoke() map[string]string {
	return map[string]string{
		"SMOKE_OIDC_SA_EMAIL": "ryft-smoke@ryft-test.iam.gserviceaccount.com",
		"SMOKE_OIDC_SA_ID":    "104512345678901234567",
		"SMOKE_OIDC_AUDIENCE": "ryft-smoke",
		"SMOKE_USER_ID":       "7d0a4c2e-5b1f-4e0a-9a43-0d6f2a1b8c33",
	}
}

func TestLoadAuthSmokeOffAndOn(t *testing.T) {
	setSmokeEnv(t, nil)
	a, err := LoadAuth()
	if err != nil || a.SmokeOn() {
		t.Fatalf("none set: off, no error: %+v %v", a, err)
	}
	setSmokeEnv(t, goodSmoke())
	a, err = LoadAuth()
	if err != nil || !a.SmokeOn() || a.SmokeSAEmail != "ryft-smoke@ryft-test.iam.gserviceaccount.com" ||
		a.SmokeSAID != "104512345678901234567" || a.SmokeAudience != "ryft-smoke" ||
		a.SmokeUserID.String() != "7d0a4c2e-5b1f-4e0a-9a43-0d6f2a1b8c33" {
		t.Fatalf("all set: on: %+v %v", a, err)
	}
	t.Setenv("K_SERVICE", "api") // Cloud Run is where it runs
	if a, err = LoadAuth(); err != nil || !a.SmokeOn() {
		t.Fatalf("on Cloud Run: %+v %v", a, err)
	}
}

// All or nothing: each variable missing alone, and each one alone, is refused, and the error names
// the variables that are missing.
func TestLoadAuthSmokeIsAllOrNothing(t *testing.T) {
	for _, missing := range smokeVars {
		env := goodSmoke()
		delete(env, missing)
		setSmokeEnv(t, env)
		if _, err := LoadAuth(); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s missing: want an error naming it, got %v", missing, err)
		}
	}
	for _, only := range smokeVars {
		setSmokeEnv(t, map[string]string{only: goodSmoke()[only]})
		_, err := LoadAuth()
		if err == nil {
			t.Errorf("only %s: must be refused", only)
			continue
		}
		for _, other := range smokeVars {
			if other != only && !strings.Contains(err.Error(), other) {
				t.Errorf("only %s: the error must name %s: %v", only, other, err)
			}
		}
	}
	env := goodSmoke()
	env["SMOKE_OIDC_AUDIENCE"] = "  "
	setSmokeEnv(t, env)
	if _, err := LoadAuth(); err == nil {
		t.Error("a blank audience counts as unset")
	}
}

func TestLoadAuthSmokeShapes(t *testing.T) {
	cases := map[string]struct{ key, val string }{
		"email without a service account domain": {"SMOKE_OIDC_SA_EMAIL", "ryft-smoke@example.com"},
		"email of a user":                        {"SMOKE_OIDC_SA_EMAIL", "tim@gmail.com"},
		"email without a local part":             {"SMOKE_OIDC_SA_EMAIL", "@p.iam.gserviceaccount.com"},
		"email without a project":                {"SMOKE_OIDC_SA_EMAIL", "ryft-smoke@.iam.gserviceaccount.com"},
		"email with a space":                     {"SMOKE_OIDC_SA_EMAIL", "ryft smoke@p.iam.gserviceaccount.com"},
		"email with two ats":                     {"SMOKE_OIDC_SA_EMAIL", "a@b@p.iam.gserviceaccount.com"},
		"id with letters":                        {"SMOKE_OIDC_SA_ID", "12ab"},
		"id too long":                            {"SMOKE_OIDC_SA_ID", strings.Repeat("1", 33)},
		"id signed":                              {"SMOKE_OIDC_SA_ID", "-1"},
		"audience with a space":                  {"SMOKE_OIDC_AUDIENCE", "ryft smoke"},
		"audience with a control character":      {"SMOKE_OIDC_AUDIENCE", "ryft\x07smoke"},
		"audience not ASCII":                     {"SMOKE_OIDC_AUDIENCE", "ryft-smöke"},
		"audience too long":                      {"SMOKE_OIDC_AUDIENCE", strings.Repeat("a", 257)},
		"user not a UUID":                        {"SMOKE_USER_ID", "smoke"},
		"user nil UUID":                          {"SMOKE_USER_ID", "00000000-0000-0000-0000-000000000000"},
		"user in URN form":                       {"SMOKE_USER_ID", "urn:uuid:7d0a4c2e-5b1f-4e0a-9a43-0d6f2a1b8c33"},
		"user in braces":                         {"SMOKE_USER_ID", "{7d0a4c2e-5b1f-4e0a-9a43-0d6f2a1b8c33}"},
	}
	for name, tc := range cases {
		env := goodSmoke()
		env[tc.key] = tc.val
		setSmokeEnv(t, env)
		_, err := LoadAuth()
		if err == nil {
			t.Errorf("%s: must be refused", name)
		} else if !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: the error must name %s: %v", name, tc.key, err)
		}
	}
	env := goodSmoke()
	env["SMOKE_OIDC_AUDIENCE"] = strings.Repeat("a", 256)
	env["SMOKE_OIDC_SA_ID"] = strings.Repeat("9", 32)
	setSmokeEnv(t, env)
	if _, err := LoadAuth(); err != nil {
		t.Errorf("the limits themselves are allowed: %v", err)
	}
}

// The smoke identity is not the push identity: a push token must not become the smoke user.
func TestLoadAuthSmokeRefusesThePushIdentity(t *testing.T) {
	cases := map[string]struct{ smoke, push, value string }{
		"email":    {"SMOKE_OIDC_SA_EMAIL", "PUSH_SA_EMAIL", "ryft-push@leaky-project.iam.gserviceaccount.com"},
		"audience": {"SMOKE_OIDC_AUDIENCE", "PUSH_AUDIENCE", "https://leaky-audience.europe-west1.run.app"},
	}
	for name, tc := range cases {
		env := goodSmoke()
		env[tc.smoke] = tc.value
		setSmokeEnv(t, env)
		t.Setenv(tc.push, tc.value)
		_, err := LoadAuth()
		if err == nil || !strings.Contains(err.Error(), tc.smoke) || !strings.Contains(err.Error(), tc.push) {
			t.Errorf("%s: want a refusal naming both variables, got %v", name, err)
		}
		if err != nil && (strings.Contains(err.Error(), "leaky") || strings.Contains(err.Error(), tc.value)) {
			t.Errorf("%s: the error holds a value: %v", name, err)
		}
		// Different push values do not matter.
		t.Setenv(tc.push, "something-else")
		if _, err := LoadAuth(); err != nil {
			t.Errorf("%s: a different push value is fine: %v", name, err)
		}
	}
}

func TestLoadAuthSmokeRefusedInAPIKeyMode(t *testing.T) {
	apikey := map[string]string{"AUTH_MODE": "apikey", "API_KEY": strings.Repeat("k", 16), "ALLOW_INSECURE_APIKEY_AUTH": "true"}
	for _, v := range smokeVars {
		env := map[string]string{v: goodSmoke()[v]}
		for k, val := range apikey {
			env[k] = val
		}
		setSmokeEnv(t, env)
		if _, err := LoadAuth(); err == nil || !strings.Contains(err.Error(), v) {
			t.Errorf("%s with apikey mode: must be refused naming it, got %v", v, err)
		}
	}
	env := goodSmoke()
	for k, val := range apikey {
		env[k] = val
	}
	setSmokeEnv(t, env)
	if _, err := LoadAuth(); err == nil {
		t.Error("apikey mode with the whole smoke sign-in must be refused")
	}
}

// No error holds a value: not the account's address, its ID, the audience or the user.
func TestLoadAuthSmokeErrorsHoldNoValue(t *testing.T) {
	good := map[string]string{
		"SMOKE_OIDC_SA_EMAIL": "leaky-name@leaky-project.iam.gserviceaccount.com",
		"SMOKE_OIDC_SA_ID":    "987654321987654321",
		"SMOKE_OIDC_AUDIENCE": "leaky-audience",
		"SMOKE_USER_ID":       "0f1e2d3c-4b5a-4968-8776-655443322110",
	}
	bad := map[string]string{
		"SMOKE_OIDC_SA_EMAIL": "leaky-name@leaky-project.example.com",
		"SMOKE_OIDC_SA_ID":    "leaky987654321",
		"SMOKE_OIDC_AUDIENCE": "leaky audience",
		"SMOKE_USER_ID":       "leaky-user",
	}
	check := func(label string, env map[string]string) {
		t.Helper()
		setSmokeEnv(t, env)
		_, err := LoadAuth()
		if err == nil {
			t.Errorf("%s: must be refused", label)
			return
		}
		for _, v := range env {
			if len(v) > 8 && strings.Contains(err.Error(), v) {
				t.Errorf("%s: the error holds a value: %v", label, err)
			}
		}
		for _, frag := range []string{"leaky", "987654321", "0f1e2d3c"} {
			if strings.Contains(err.Error(), frag) {
				t.Errorf("%s: the error holds a value fragment %q: %v", label, frag, err)
			}
		}
	}
	for k := range good {
		check("only "+k, map[string]string{k: good[k]})
		env := map[string]string{}
		for kk, vv := range good {
			env[kk] = vv
		}
		env[k] = bad[k]
		check("bad "+k, env)
	}
	env := map[string]string{"AUTH_MODE": "apikey", "API_KEY": strings.Repeat("k", 16), "ALLOW_INSECURE_APIKEY_AUTH": "true"}
	for k, v := range good {
		env[k] = v
	}
	check("apikey", env)
}
