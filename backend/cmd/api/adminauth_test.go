package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

// Design 2026-11-05-admin-web §4.2 and §10: every bearer that is not the key or an allow-listed operator
// gets the same 401, byte for byte, and the reason is only in the log.
func TestAdminRejectionsAreByteIdentical(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op, noRow := uuid.New(), uuid.New()
	acc := newFakeAccounts(op)
	srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: acc})

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mod := func(base func(uuid.UUID) jwt.MapClaims, sub uuid.UUID, f func(jwt.MapClaims)) string {
		c := base(sub)
		f(c)
		return jwks.sign(t, c)
	}
	hs256, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, adminClaims(op)).SignedString([]byte("legacy-jwt-secret"))
	unsigned, _ := jwt.NewWithClaims(jwt.SigningMethodNone, adminClaims(op)).SignedString(jwt.UnsafeAllowNoneSignatureType)

	cases := map[string]string{
		"no bearer":                 "",
		"garbage":                   "not-a-token",
		"almost the key":            testAdminKey + "x",
		"the key minus one":         testAdminKey[:len(testAdminKey)-1],
		"the key in the wrong case": strings.ToUpper(testAdminKey),
		"a JWT-shaped garbage":      "aaa.bbb.ccc",
		"a lookalike with two dots": "x." + testAdminKey + ".y",
		"expired operator token": mod(adminClaims, op, func(c jwt.MapClaims) {
			c["exp"] = time.Now().Add(-time.Hour).Unix()
			c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
		}),
		"foreign issuer":              mod(adminClaims, op, func(c jwt.MapClaims) { c["iss"] = "https://evil.supabase.co/auth/v1" }),
		"foreign audience":            mod(adminClaims, op, func(c jwt.MapClaims) { c["aud"] = "somebody-else" }),
		"signed by another key":       signWith(t, other, testKID, adminClaims(op)),
		"unknown key id":              signWith(t, jwks.key, "nope", adminClaims(op)),
		"HS256 with a guessed secret": hs256,
		"alg none":                    unsigned,
		"anon role":                   mod(adminClaims, op, func(c jwt.MapClaims) { c["role"] = "anon" }),
		"service_role":                mod(adminClaims, op, func(c jwt.MapClaims) { c["role"] = "service_role" }),
		"top-level role admin":        mod(appClaims, op, func(c jwt.MapClaims) { c["role"] = "admin" }),
		"anonymous user":              mod(adminClaims, op, func(c jwt.MapClaims) { c["is_anonymous"] = true }),
		"an app user (no role claim)": mod(appClaims, op, func(jwt.MapClaims) {}),
		"role only in user_metadata":  mod(appClaims, op, func(c jwt.MapClaims) { c["user_metadata"] = map[string]any{"role": "admin"} }),
		"role in user_metadata, aal2": mod(adminClaims, op, func(c jwt.MapClaims) {
			c["app_metadata"] = map[string]any{}
			c["user_metadata"] = map[string]any{"role": "admin"}
		}),
		"app_metadata.role user":      mod(adminClaims, op, func(c jwt.MapClaims) { c["app_metadata"] = map[string]any{"role": "user"} }),
		"app_metadata.role a list":    mod(adminClaims, op, func(c jwt.MapClaims) { c["app_metadata"] = map[string]any{"role": []string{"admin"}} }),
		"app_metadata.role Admin":     mod(adminClaims, op, func(c jwt.MapClaims) { c["app_metadata"] = map[string]any{"role": "Admin"} }),
		"admin claim but no row":      mod(adminClaims, noRow, func(jwt.MapClaims) {}),
		"admin claim, no row, aal1":   mod(adminClaims, noRow, func(c jwt.MapClaims) { c["aal"] = "aal1"; delete(c, "amr") }),
		"an app user's aal1 token":    mod(appClaims, op, func(c jwt.MapClaims) { c["aal"] = "aal1" }),
		"a very long bearer":          "a." + strings.Repeat("b", 9<<10) + ".c",
		"a very long bearer, no dots": strings.Repeat("a", 70<<10),
	}
	var wantStatus, wantAuth, wantBody string
	for name, bearer := range cases {
		before := adminReq("unauthorized")
		// Without Instrument there is no request id: the whole answer is comparable byte for byte.
		a := do(srv, http.MethodGet, "/v1/admin/flags", bearer, "")
		if a.status != 401 {
			t.Errorf("%s: %d %s, want 401", name, a.status, a.body)
			continue
		}
		if adminReq("unauthorized") != before+1 {
			t.Errorf("%s: not counted as unauthorized", name)
		}
		if wantStatus == "" {
			wantStatus, wantAuth, wantBody = "401", a.header.Get("WWW-Authenticate"), a.body
			if wantAuth != `Bearer realm="ryft-admin"` || !strings.Contains(wantBody, "admin key or admin sign-in required") ||
				strings.Contains(wantBody, "code") {
				t.Fatalf("the 401: %q %s", wantAuth, wantBody)
			}
		}
		if a.header.Get("WWW-Authenticate") != wantAuth || a.body != wantBody ||
			a.header.Get("Retry-After") != "" || a.header.Get("Content-Type") != "application/json" {
			t.Errorf("%s: %v %q differs from the first 401: %q %q", name, a.header, a.body, wantAuth, wantBody)
		}
	}
	if len(*actors) != 0 {
		t.Errorf("a rejected bearer reached the handler: %v", *actors)
	}
}

// The same through Instrument: the only thing that differs between two 401s is the request id.
func TestAdminRejectionDiffersOnlyByRequestID(t *testing.T) {
	jwks := newJWKSStub(t, false)
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: newFakeAccounts()})
	log := quietHandler().log
	chain := httpx.Instrument(log, srv)
	norm := func(a answer) string {
		id := a.header.Get(httpx.RequestIDHeader)
		if id == "" || !strings.Contains(a.body, id) {
			t.Fatalf("request id %q missing from %s", id, a.body)
		}
		return strings.Replace(a.body, id, "ID", 1)
	}
	garbage := norm(do(chain, "GET", "/v1/admin/flags", "garbage", ""))
	app := norm(do(chain, "GET", "/v1/admin/flags", jwks.sign(t, appClaims(uuid.New())), ""))
	none := norm(do(chain, "GET", "/v1/admin/flags", "", ""))
	if garbage != app || app != none {
		t.Errorf("%q %q %q", garbage, app, none)
	}
}

func TestAdminKeyWorksWhateverItLooksLike(t *testing.T) {
	jwks := newJWKSStub(t, false)
	cv := &countingVerifier{next: jwks.verifier(t)}
	for _, key := range []string{testAdminKey, "a.b." + strings.Repeat("k", 40), "Bearer-" + strings.Repeat("k", 40)} {
		srv, actors := authChain(quietHandler(), adminOptions{Key: key, Verifier: cv, Accounts: newFakeAccounts()})
		if a := do(srv, "GET", "/v1/admin/flags", key, ""); a.status != 204 {
			t.Errorf("key %q: %d", key, a.status)
		}
		if got := *actors; len(got) != 1 || !got[0].isKey() || got[0].fingerprint != keyFingerprint(key) {
			t.Errorf("actor %+v", got)
		}
	}
	if cv.calls != 0 {
		t.Errorf("the key is compared first: the token verifier ran %d times", cv.calls)
	}
}

// The scheme is case-insensitive, as auth.Middleware's.
func TestAdminBearerScheme(t *testing.T) {
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Accounts: newFakeAccounts()})
	for hdr, want := range map[string]int{"Bearer " + testAdminKey: 204, "bearer " + testAdminKey: 204, "BEARER " + testAdminKey: 204,
		"Basic " + testAdminKey: 401, testAdminKey: 401, "Bearer": 401, "Bearer  ": 401} {
		r := httptest.NewRequest(http.MethodGet, "/v1/admin/flags", nil)
		r.Header.Set("Authorization", hdr)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != want {
			t.Errorf("Authorization %q: %d, want %d", hdr, rec.Code, want)
		}
	}
}

// Without a verifier (AUTH_MODE=apikey) only the key is accepted; a JWT-shaped bearer costs nothing.
func TestAdminNoVerifierAdmitsOnlyTheKey(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op := uuid.New()
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Accounts: newFakeAccounts(op)})
	if a := do(srv, "GET", "/v1/admin/flags", jwks.sign(t, adminClaims(op)), ""); a.status != 401 {
		t.Fatalf("a valid operator token without a verifier: %d", a.status)
	}
	if a := do(srv, "GET", "/v1/admin/flags", testAdminKey, ""); a.status != 204 {
		t.Fatalf("the key: %d", a.status)
	}
}

func TestAdminOversizedBearerNeverReachesTheVerifier(t *testing.T) {
	jwks := newJWKSStub(t, false)
	cv := &countingVerifier{next: jwks.verifier(t)}
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: cv, Accounts: newFakeAccounts()})
	for _, b := range []string{"a." + strings.Repeat("b", 9<<10) + ".c", "no-dots", "one.dot", "a.b.c.d"} {
		if a := do(srv, "GET", "/v1/admin/flags", b, ""); a.status != 401 {
			t.Errorf("%.20q: %d", b, a.status)
		}
	}
	if cv.calls != 0 {
		t.Errorf("the verifier ran %d times for bearers that cannot be tokens", cv.calls)
	}
}

// §3.3 and §4.2: the second factor is checked after the role and the row, so only a real operator
// ever sees mfa_required.
func TestAdminMFAFreshness(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op := uuid.New()
	acc := newFakeAccounts(op)
	srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: acc})
	totp := func(at time.Time) []map[string]any {
		return []map[string]any{{"method": "password", "timestamp": time.Now().Unix()}, {"method": "totp", "timestamp": at.Unix()}}
	}
	cases := []struct {
		name string
		mod  func(jwt.MapClaims)
		want int
	}{
		{"aal2 and a TOTP five minutes ago", func(jwt.MapClaims) {}, 204},
		{"a TOTP 11 h 50 min ago", func(c jwt.MapClaims) { c["amr"] = totp(time.Now().Add(-11*time.Hour - 50*time.Minute)) }, 204},
		{"a TOTP 12 h 10 min ago", func(c jwt.MapClaims) { c["amr"] = totp(time.Now().Add(-12*time.Hour - 10*time.Minute)) }, 403},
		{"a TOTP a day ago", func(c jwt.MapClaims) { c["amr"] = totp(time.Now().Add(-24 * time.Hour)) }, 403},
		{"a TOTP 10 s in the future (skew)", func(c jwt.MapClaims) { c["amr"] = totp(time.Now().Add(10 * time.Second)) }, 204},
		{"a TOTP 5 min in the future", func(c jwt.MapClaims) { c["amr"] = totp(time.Now().Add(5 * time.Minute)) }, 403},
		{"the newest TOTP counts", func(c jwt.MapClaims) {
			c["amr"] = append(totp(time.Now().Add(-20*time.Hour)), map[string]any{"method": "totp", "timestamp": time.Now().Add(-time.Minute).Unix()})
		}, 204},
		{"aal1 with a fresh TOTP entry", func(c jwt.MapClaims) { c["aal"] = "aal1" }, 403},
		{"aal2 without amr", func(c jwt.MapClaims) { delete(c, "amr") }, 403},
		{"aal2 and only a password entry", func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "password", "timestamp": time.Now().Unix()}}
		}, 403},
		{"no aal claim", func(c jwt.MapClaims) { delete(c, "aal") }, 403},
		{"aal3", func(c jwt.MapClaims) { c["aal"] = "aal3" }, 403},
		{"aal as a number", func(c jwt.MapClaims) { c["aal"] = 2 }, 403},
		{"amr in RFC 8176 strings", func(c jwt.MapClaims) { c["amr"] = []string{"pwd", "otp"} }, 403},
		{"amr a plain string", func(c jwt.MapClaims) { c["amr"] = "totp" }, 403},
		{"amr timestamp a string", func(c jwt.MapClaims) { c["amr"] = []map[string]any{{"method": "totp", "timestamp": "now"}} }, 403},
		{"amr as an object", func(c jwt.MapClaims) { c["amr"] = map[string]any{"method": "totp", "timestamp": time.Now().Unix()} }, 403},
		{"amr timestamp absurd", func(c jwt.MapClaims) { c["amr"] = []map[string]any{{"method": "totp", "timestamp": 1e300}} }, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := adminClaims(op)
			tc.mod(c)
			before := adminReq("mfa_required")
			a := do(srv, "GET", "/v1/admin/flags", jwks.sign(t, c), "")
			if a.status != tc.want {
				t.Fatalf("%d %s, want %d", a.status, a.body, tc.want)
			}
			if tc.want == 403 {
				if a.code() != model.CodeMFARequired || a.header.Get("WWW-Authenticate") != "" || adminReq("mfa_required") != before+1 {
					t.Errorf("want 403 mfa_required, counted: %s %v", a.body, a.header)
				}
			}
		})
	}
	if n := len(*actors); n != 4 {
		t.Errorf("the handler ran %d times, want 4", n)
	}
}

// mfa_required comes only after the role and the row: for everybody else the answer is the 401.
func TestAdminMFAIsNoOracleForNonOperators(t *testing.T) {
	jwks := newJWKSStub(t, false)
	noRow, op := uuid.New(), uuid.New()
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: newFakeAccounts(op)})
	aal1 := func(c jwt.MapClaims) jwt.MapClaims { c["aal"] = "aal1"; delete(c, "amr"); return c }
	for name, c := range map[string]jwt.MapClaims{
		"an app user, aal1":                     aal1(appClaims(op)),
		"the claim without the row, aal1":       aal1(adminClaims(noRow)),
		"the claim without the row, stale TOTP": adminClaims(noRow),
	} {
		if name == "the claim without the row, stale TOTP" {
			c["amr"] = []map[string]any{{"method": "totp", "timestamp": time.Now().Add(-48 * time.Hour).Unix()}}
		}
		if a := do(srv, "GET", "/v1/admin/flags", jwks.sign(t, c), ""); a.status != 401 || a.code() != "" {
			t.Errorf("%s: %d %s", name, a.status, a.body)
		}
	}
}

// Deleting the row ends the session on the next request (the token is still valid for up to an hour).
func TestAdminRevocationIsImmediate(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op := uuid.New()
	acc := newFakeAccounts(op)
	srv, _ := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: acc})
	tok := jwks.sign(t, adminClaims(op))
	if a := do(srv, "GET", "/v1/admin/flags", tok, ""); a.status != 204 {
		t.Fatalf("with the row: %d", a.status)
	}
	acc.set(op, false)
	if a := do(srv, "GET", "/v1/admin/flags", tok, ""); a.status != 401 {
		t.Fatalf("after the row is deleted: %d %s", a.status, a.body)
	}
	acc.set(op, true)
	if a := do(srv, "GET", "/v1/admin/flags", tok, ""); a.status != 204 {
		t.Fatalf("with the row again: %d", a.status)
	}
}

// §8: an outage of the signing keys or the database is a 503 with Retry-After, never a sign-out, and
// the key keeps working.
func TestAdminOutagesAre503NotUnauthorized(t *testing.T) {
	op := uuid.New()
	t.Run("JWKS down", func(t *testing.T) {
		jwks := newJWKSStub(t, true) // never had keys
		srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: newFakeAccounts(op)})
		unauth := adminReq("unauthorized")
		a := do(srv, "GET", "/v1/admin/flags", jwks.sign(t, adminClaims(op)), "")
		if a.status != 503 || a.header.Get("Retry-After") != "5" || a.header.Get("WWW-Authenticate") != "" {
			t.Fatalf("a token while the JWKS is down: %d %v %s", a.status, a.header, a.body)
		}
		if adminReq("unauthorized") != unauth {
			t.Error("an outage is not a refused credential")
		}
		if a := do(srv, "GET", "/v1/admin/flags", testAdminKey, ""); a.status != 204 {
			t.Fatalf("the key does not depend on Supabase: %d", a.status)
		}
		if len(*actors) != 1 {
			t.Errorf("handler ran %d times", len(*actors))
		}
	})
	t.Run("database down for the allow-list check", func(t *testing.T) {
		jwks := newJWKSStub(t, false)
		acc := newFakeAccounts(op)
		acc.err = errDown
		srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: acc})
		internal := count(metrics.AuthFailures.WithLabelValues("internal"))
		a := do(srv, "GET", "/v1/admin/flags", jwks.sign(t, adminClaims(op)), "")
		if a.status != 503 || a.header.Get("Retry-After") != "5" || strings.Contains(a.body, "db down") {
			t.Fatalf("%d %v %s", a.status, a.header, a.body)
		}
		if count(metrics.AuthFailures.WithLabelValues("internal")) != internal+1 {
			t.Error("the failed allow-list check counts as ryft_auth_failures_total{reason=internal}, which AuthUnavailable watches")
		}
		if a := do(srv, "GET", "/v1/admin/flags", testAdminKey, ""); a.status != 204 {
			t.Fatalf("the key: %d", a.status)
		}
		if len(*actors) != 1 {
			t.Errorf("handler ran %d times", len(*actors))
		}
	})
}

// §3.1: an app user is not affected by the new claims, whatever they look like: the app path is auth.Middleware,
// which this change does not touch, and the verifier is the shared one.
func TestAdminActorIsTheTokenUser(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op := uuid.New()
	srv, actors := authChain(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: newFakeAccounts(op)})
	before := count(adminAuthorized("user"))
	if a := do(srv, "GET", "/v1/admin/flags", jwks.sign(t, adminClaims(op)), ""); a.status != 204 {
		t.Fatalf("%d %s", a.status, a.body)
	}
	got := *actors
	if len(got) != 1 || got[0].isKey() || got[0].user != op || got[0].kind() != "user" || got[0].label() != "user:"+op.String()[:8] {
		t.Fatalf("actor %+v", got)
	}
	if count(adminAuthorized("user")) != before+1 {
		t.Error("ryft_admin_authorized_total{actor=user} not counted")
	}
}

// §4.6: 10/s, burst 20 per address, keyed by the rightmost X-Forwarded-For entry, before the bearer.
func TestAdminPerAddressLimit(t *testing.T) {
	jwks := newJWKSStub(t, false)
	cv := &countingVerifier{next: jwks.verifier(t)}
	lim := ratelimit.New(0.001, 3) // a burst of 3 and no refill within the test
	srv := adminRoutes(quietHandler(), adminOptions{Key: testAdminKey, Verifier: cv, Accounts: newFakeAccounts(), Limiter: lim,
		Origins: []string{"https://ryftapp.de"}, Timeout: time.Second})
	tok := jwks.sign(t, appClaims(uuid.New()))
	hit := func(xff string) answer {
		return do(srv, "GET", "/v1/admin/flags", tok, "", "X-Forwarded-For", xff, "Origin", "https://ryftapp.de")
	}
	limited, unauth := count(rateLimited("admin_ip")), adminReq("unauthorized")
	for i := 0; i < 3; i++ {
		// A forged leftmost entry changes nothing: the proxy's entry is the key.
		if a := hit("10." + string(rune('0'+i)) + ".0.1, 203.0.113.7"); a.status != 401 {
			t.Fatalf("request %d: %d", i, a.status)
		}
	}
	a := hit("198.51.100.99, 203.0.113.7")
	if a.status != 429 || a.header.Get("Retry-After") == "" || a.header.Get("Retry-After") == "0" ||
		a.header.Get("Access-Control-Allow-Origin") != "https://ryftapp.de" || !strings.Contains(a.body, "too many requests") {
		t.Fatalf("the 4th request: %d %v %s", a.status, a.header, a.body)
	}
	if cv.calls != 3 {
		t.Errorf("the limit runs before the token check: the verifier ran %d times, want 3", cv.calls)
	}
	if count(rateLimited("admin_ip")) != limited+1 || adminReq("unauthorized") != unauth+3 {
		t.Error("the 429 is counted as rate limited, not as unauthorized")
	}
	// Another address has its own bucket.
	if a := hit("203.0.113.8"); a.status != 401 {
		t.Errorf("another client address: %d", a.status)
	}
	// A preflight is neither counted nor limited, even from the exhausted address.
	pre := do(srv, "OPTIONS", "/v1/admin/flags", "", "", "X-Forwarded-For", "203.0.113.7", "Origin", "https://ryftapp.de",
		"Access-Control-Request-Method", "GET", "Access-Control-Request-Headers", "authorization")
	if pre.status != 204 || pre.header.Get("Access-Control-Allow-Origin") != "https://ryftapp.de" {
		t.Errorf("preflight: %d %v", pre.status, pre.header)
	}
	// The default limiter: 10 a second, bursts of 20.
	def := adminRoutes(quietHandler(), adminOptions{Key: testAdminKey, Accounts: newFakeAccounts(), Timeout: time.Second})
	var ok, refused int
	for i := 0; i < 25; i++ {
		switch do(def, "GET", "/v1/admin/flags", "x", "", "X-Forwarded-For", "203.0.113.50").status {
		case 401:
			ok++
		case 429:
			refused++
		}
	}
	// 20 at once, plus at most a token or two refilled while the loop ran.
	if ok < 20 || ok > 22 || ok+refused != 25 {
		t.Errorf("default limit: %d passed, %d refused of 25, want about 20 and 5", ok, refused)
	}
}

// §5: CORS around the whole chain, for the admin routes only.
func TestAdminCORSThroughTheChain(t *testing.T) {
	jwks := newJWKSStub(t, false)
	op := uuid.New()
	build := func(origins ...string) http.Handler {
		return adminRoutes(quietHandler(), adminOptions{Key: testAdminKey, Verifier: jwks.verifier(t), Accounts: newFakeAccounts(op),
			Origins: origins, Limiter: wide(), Timeout: time.Second})
	}
	page := "https://ryftapp.de"
	srv := build(page)
	pre := func(h http.Handler, origin, method, headers string) answer {
		return do(h, "OPTIONS", "/v1/admin/invites", "", "", "Origin", origin, "Access-Control-Request-Method", method,
			"Access-Control-Request-Headers", headers)
	}
	unauth, authorized := adminReq("unauthorized"), count(adminAuthorized("key"))+count(adminAuthorized("user"))
	a := pre(srv, page, "POST", "authorization,content-type")
	if a.status != 204 || a.header.Get("Access-Control-Allow-Origin") != page || a.header.Get("Access-Control-Max-Age") != "600" ||
		a.header.Get("Access-Control-Allow-Methods") != "GET, POST, DELETE" || a.header.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" {
		t.Fatalf("allowed preflight: %d %v", a.status, a.header)
	}
	for name, a := range map[string]answer{
		"another origin": pre(srv, "https://evil.example", "GET", "authorization"),
		"PUT":            pre(srv, page, "PUT", "authorization"),
		"x-admin-key":    pre(srv, page, "GET", "x-admin-key"),
	} {
		if a.status != 204 || a.header.Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("%s: refused preflight: %d %v", name, a.status, a.header)
		}
	}
	if adminReq("unauthorized") != unauth || count(adminAuthorized("key"))+count(adminAuthorized("user")) != authorized {
		t.Error("a preflight reaches neither the auth nor the metrics")
	}

	// The page reads the refusals: 401, 403 mfa_required and 503 carry the origin header too.
	tokAAL1 := jwks.sign(t, func() jwt.MapClaims { c := adminClaims(op); c["aal"] = "aal1"; return c }())
	for name, tc := range map[string]struct {
		bearer string
		want   int
	}{"401": {"garbage", 401}, "403 mfa_required": {tokAAL1, 403}} {
		a := do(srv, "GET", "/v1/admin/invites", tc.bearer, "", "Origin", page)
		if a.status != tc.want || a.header.Get("Access-Control-Allow-Origin") != page || a.header.Get("Vary") != "Origin" ||
			a.header.Get("Access-Control-Expose-Headers") != "X-Request-ID, Retry-After" {
			t.Errorf("%s: %d %v", name, a.status, a.header)
		}
	}
	down := adminRoutes(quietHandler(), adminOptions{Key: testAdminKey, Verifier: newJWKSStub(t, true).verifier(t), Accounts: newFakeAccounts(op),
		Origins: []string{page}, Limiter: wide(), Timeout: time.Second})
	if a := do(down, "GET", "/v1/admin/invites", jwks.sign(t, adminClaims(op)), "", "Origin", page); a.status != 503 ||
		a.header.Get("Access-Control-Allow-Origin") != page || a.header.Get("Retry-After") != "5" {
		t.Errorf("503: %d %v", a.status, a.header)
	}
	// Another origin, and no origin: served as usual, no Access-Control-*.
	for _, o := range []string{"https://evil.example", "null", ""} {
		hdr := []string{}
		if o != "" {
			hdr = []string{"Origin", o}
		}
		a := do(srv, "GET", "/v1/admin/invites", "garbage", "", hdr...)
		if a.status != 401 || a.header.Get("Access-Control-Allow-Origin") != "" || a.header.Get("Vary") != "Origin" {
			t.Errorf("origin %q: %d %v", o, a.status, a.header)
		}
	}
	// Empty config: no CORS at all, whoever asks.
	none := build()
	if a := pre(none, page, "GET", "authorization"); a.status != 204 || a.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("empty config, preflight: %d %v", a.status, a.header)
	}
	if a := do(none, "GET", "/v1/admin/invites", "garbage", "", "Origin", page); a.status != 401 || a.header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("empty config, request: %d %v", a.status, a.header)
	}
}

// Only /v1/admin/ sends CORS headers: a user route mounted next to it, as in main, has none.
func TestUserRoutesNeverSendCORS(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, _ *http.Request) { httpx.WriteJSON(w, 200, map[string]string{}) })
	mux.Handle("/v1/admin/", adminRoutes(quietHandler(), adminOptions{Key: testAdminKey, Accounts: newFakeAccounts(),
		Origins: []string{"https://ryftapp.de"}, Limiter: wide(), Timeout: time.Second}))
	for _, m := range []string{"GET", "OPTIONS"} {
		a := do(mux, m, "/v1/config", "", "", "Origin", "https://ryftapp.de", "Access-Control-Request-Method", "GET")
		for k := range a.header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s /v1/config sends %s", m, k)
			}
		}
		if a.header.Get("Vary") != "" {
			t.Errorf("%s /v1/config: Vary %q", m, a.header.Get("Vary"))
		}
	}
	if a := do(mux, "GET", "/v1/admin/flags", "x", "", "Origin", "https://ryftapp.de"); a.header.Get("Access-Control-Allow-Origin") == "" {
		t.Error("the admin routes do send it")
	}
}
