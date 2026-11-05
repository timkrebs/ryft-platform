package auth

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// The operator claims of an access token (design 2026-11-05-admin-web §3.2): read from the right
// places only, and a claim with an unexpected shape yields the empty value but never rejects the
// token, so an app user is never signed out by it.
func TestSupabaseOperatorClaims(t *testing.T) {
	srv := newJWKSServer(t)
	key := srv.addKey(t, "k1")
	v := newVerifier(t, srv, SupabaseOptions{})
	t0, t1 := time.Now().Add(-3*time.Hour).Unix(), time.Now().Add(-time.Hour).Unix()
	cases := []struct {
		name    string
		mutate  func(c jwt.MapClaims)
		role    string
		aal     string
		mfa     time.Time // zero: none
		wantErr string    // a 401 reason
	}{
		{name: "app token without the claims", mutate: func(jwt.MapClaims) {}},
		{name: "app_metadata.role admin", mutate: func(c jwt.MapClaims) {
			c["app_metadata"] = map[string]any{"provider": "email", "role": "admin"}
		}, role: "admin"},
		{name: "user_metadata.role admin only (the user can write it)", mutate: func(c jwt.MapClaims) {
			c["user_metadata"] = map[string]any{"role": "admin"}
		}},
		{name: "top-level role admin is refused by the existing check", mutate: func(c jwt.MapClaims) { c["role"] = "admin" },
			wantErr: "role"},
		{name: "app_metadata.role is a number", mutate: func(c jwt.MapClaims) {
			c["app_metadata"] = map[string]any{"role": 7}
		}},
		{name: "app_metadata.role is a list", mutate: func(c jwt.MapClaims) {
			c["app_metadata"] = map[string]any{"role": []string{"admin"}}
		}},
		{name: "app_metadata.role is too long", mutate: func(c jwt.MapClaims) {
			c["app_metadata"] = map[string]any{"role": "admin" + string(make([]byte, 40))}
		}},
		{name: "app_metadata.role is null", mutate: func(c jwt.MapClaims) { c["app_metadata"] = map[string]any{"role": nil} }},
		{name: "aal2 with a totp entry", mutate: func(c jwt.MapClaims) {
			c["aal"] = "aal2"
			c["amr"] = []map[string]any{{"method": "password", "timestamp": t0}, {"method": "totp", "timestamp": t1}}
		}, aal: "aal2", mfa: time.Unix(t1, 0)},
		{name: "aal1 with a password entry", mutate: func(c jwt.MapClaims) {
			c["aal"] = "aal1"
			c["amr"] = []map[string]any{{"method": "password", "timestamp": t0}}
		}, aal: "aal1"},
		{name: "two totp entries: the newest counts", mutate: func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "totp", "timestamp": t1}, {"method": "totp", "timestamp": t0}}
		}, mfa: time.Unix(t1, 0)},
		{name: "amr in RFC 8176 strings", mutate: func(c jwt.MapClaims) { c["amr"] = []string{"pwd", "otp"} }},
		{name: "amr is a string", mutate: func(c jwt.MapClaims) { c["amr"] = "totp" }},
		{name: "amr is an object", mutate: func(c jwt.MapClaims) { c["amr"] = map[string]any{"method": "totp", "timestamp": t1} }},
		{name: "amr entry with a string timestamp", mutate: func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "totp", "timestamp": "yesterday"}}
		}},
		{name: "amr entry with a number as method", mutate: func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": 3, "timestamp": t1}}
		}},
		{name: "amr entry without a timestamp", mutate: func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "totp"}}
		}},
		{name: "amr entry with a negative timestamp", mutate: func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "totp", "timestamp": -5}}
		}},
		{name: "amr entry with an absurd timestamp", mutate: func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "totp", "timestamp": 1e300}}
		}},
		{name: "a good entry after bad ones is still read", mutate: func(c jwt.MapClaims) {
			c["amr"] = []any{"pwd", 7, nil, map[string]any{"method": "totp", "timestamp": t1}}
		}, mfa: time.Unix(t1, 0)},
		{name: "aal is aal3", mutate: func(c jwt.MapClaims) { c["aal"] = "aal3" }},
		{name: "aal is a number", mutate: func(c jwt.MapClaims) { c["aal"] = 2 }},
		{name: "aal is null", mutate: func(c jwt.MapClaims) { c["aal"] = nil }},
		{name: "aal in the wrong case", mutate: func(c jwt.MapClaims) { c["aal"] = "AAL2" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := claims()
			tc.mutate(c)
			p, err := v.Verify(context.Background(), sign(t, key, "k1", c))
			if tc.wantErr != "" {
				if status, reason := statusOf(t, err); status != 401 || reason != tc.wantErr {
					t.Fatalf("got %d %q, want 401 %q", status, reason, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("an app user's token must verify whatever the operator claims look like: %v", err)
			}
			if p.AppRole != tc.role || p.AAL != tc.aal {
				t.Errorf("AppRole %q AAL %q, want %q %q", p.AppRole, p.AAL, tc.role, tc.aal)
			}
			if tc.mfa.IsZero() != p.MFAAt.IsZero() || (!tc.mfa.IsZero() && !p.MFAAt.Equal(tc.mfa)) {
				t.Errorf("MFAAt %v, want %v", p.MFAAt, tc.mfa)
			}
		})
	}
}

// Many amr entries cost no more than a bounded read.
func TestParseTOTPAtBoundsEntries(t *testing.T) {
	raw := []byte(`[`)
	for i := 0; i < 100; i++ {
		raw = append(raw, `{"method":"password","timestamp":1},`...)
	}
	raw = append(raw, `{"method":"totp","timestamp":1730790000}]`...) // entry 101: not read
	if got := parseTOTPAt(raw); !got.IsZero() {
		t.Fatalf("entry past the bound was read: %v", got)
	}
}

// apikey mode never carries operator claims, so the token path of the admin routes admits nobody locally.
func TestAPIKeyHasNoOperatorClaims(t *testing.T) {
	v := NewAPIKey("k", uuid.New())
	p, err := v.Verify(context.Background(), "k:"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if p.AppRole != "" || p.AAL != "" || !p.MFAAt.IsZero() {
		t.Fatalf("%+v", p)
	}
}
