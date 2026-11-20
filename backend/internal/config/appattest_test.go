package config

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
)

var appAttestEnv = []string{"APP_ATTEST", "APP_ATTEST_EXEMPT_USERS", "APP_ATTEST_TOKEN_KEYS", "APPLE_TEAM_ID", "APPLE_CLIENT_ID"}

func clearAppAttest(t *testing.T) {
	t.Helper()
	for _, k := range appAttestEnv {
		t.Setenv(k, "")
	}
}

// Unset is off, no exempt account, no ring; the App ID joins the two Apple values.
func TestLoadAppAttestDefaults(t *testing.T) {
	clearAppAttest(t)
	a, err := LoadAppAttest()
	if err != nil || a.Mode != ModeOff || a.Ring != nil || len(a.ExemptUsers) != 0 {
		t.Fatalf("unset: %+v %v", a, err)
	}
	if err := a.RequireAppID(); err != nil {
		t.Fatalf("no ring needs no App ID: %v", err)
	}
	t.Setenv("APPLE_TEAM_ID", "M77SKHKG63")
	t.Setenv("APPLE_CLIENT_ID", "dev.timkrebs.ryft")
	t.Setenv("APP_ATTEST", "WARN")
	t.Setenv("APP_ATTEST_TOKEN_KEYS", appattesttest.RingLine(1, 10)+"\n"+appattesttest.RingLine(40, 11)+"\n")
	a, err = LoadAppAttest()
	if err != nil || a.Mode != ModeWarn || a.Ring == nil || a.AppID() != "M77SKHKG63.dev.timkrebs.ryft" || a.RequireAppID() != nil {
		t.Fatalf("warn with a ring of two: %+v %v", a, err)
	}
}

func TestLoadAppAttestExemptUsers(t *testing.T) {
	ids := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
	for _, c := range []struct {
		name, list string
		want       int // -1: refused
	}{
		{"empty", "", 0},
		{"one", ids[0], 1},
		{"five", strings.Join(ids[:5], ","), 5},
		{"six", strings.Join(ids, ","), -1},
		{"upper case", strings.ToUpper(ids[0]), -1},
		{"a space", ids[0] + ", " + ids[1], -1},
		{"an empty entry", ids[0] + ",", -1},
		{"twice", ids[0] + "," + ids[0], -1},
		{"braces", "{" + ids[0] + "}", -1},
		{"urn", "urn:uuid:" + ids[0], -1},
		{"no hyphens", strings.ReplaceAll(ids[0], "-", ""), -1},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearAppAttest(t)
			t.Setenv("APP_ATTEST_EXEMPT_USERS", c.list)
			a, err := LoadAppAttest()
			if c.want < 0 {
				if err == nil {
					t.Fatalf("%q accepted: %+v", c.list, a)
				}
				for _, id := range ids {
					if strings.Contains(strings.ToLower(err.Error()), id[:8]) {
						t.Fatalf("the error names an account: %v", err)
					}
				}
				return
			}
			if err != nil || len(a.ExemptUsers) != c.want {
				t.Fatalf("%q: %d accounts, %v; want %d", c.list, len(a.ExemptUsers), err, c.want)
			}
			if c.want > 0 && (!a.Exempt(uuid.MustParse(ids[0])) || a.Exempt(uuid.New())) {
				t.Fatal("Exempt does not answer the list")
			}
		})
	}
}

// The start refusals: warn or on without a ring (both services), a malformed ring in any mode, a mode
// that is none, and on api a ring without either Apple value. No error holds a key's bytes.
func TestLoadAppAttestRefusals(t *testing.T) {
	good := appattesttest.RingLine(7, 0xa)
	key := good[strings.IndexByte(good, ':')+1:]
	raw, _ := base64.StdEncoding.DecodeString(key)
	for _, c := range []struct {
		name, mode, ring string
	}{
		{"warn without a ring", "warn", ""},
		{"on without a ring", "on", ""},
		{"a mode that is none", "strict", good},
		{"off with a short key", "off", good[:20]},
		{"off with a kid in upper case", "off", strings.ToUpper(good[:8]) + good[8:]},
		{"off with four lines", "off", strings.Join([]string{good, appattesttest.RingLine(50, 1), appattesttest.RingLine(90, 2), appattesttest.RingLine(130, 3)}, "\n")},
		{"warn with a key of 31 bytes", "warn", good[:9] + base64.StdEncoding.EncodeToString(raw[:31])},
		{"on with the same kid twice", "on", good + "\n" + good},
		{"a ring of whitespace", "off", "  "},
	} {
		t.Run(c.name, func(t *testing.T) {
			clearAppAttest(t)
			t.Setenv("APP_ATTEST", c.mode)
			t.Setenv("APP_ATTEST_TOKEN_KEYS", c.ring)
			_, err := LoadAppAttest()
			if err == nil {
				t.Fatal("accepted")
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), key[:12]) {
				t.Fatalf("the error holds key material: %v", err)
			}
		})
	}
	for _, missing := range []string{"APPLE_TEAM_ID", "APPLE_CLIENT_ID"} {
		clearAppAttest(t)
		t.Setenv("APPLE_TEAM_ID", "M77SKHKG63")
		t.Setenv("APPLE_CLIENT_ID", "dev.timkrebs.ryft")
		t.Setenv(missing, "")
		t.Setenv("APP_ATTEST_TOKEN_KEYS", good)
		a, err := LoadAppAttest()
		if err != nil {
			t.Fatalf("ingest starts with the ring alone: %v", err)
		}
		if err := a.RequireAppID(); err == nil {
			t.Errorf("api with a ring and no %s starts", missing)
		}
	}
}
