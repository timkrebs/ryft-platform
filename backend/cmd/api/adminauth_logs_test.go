package main

import (
	"bytes"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/ratelimit"
)

var (
	shortHex   = regexp.MustCompile(`^[0-9a-f]{8}$`)
	actorLabel = regexp.MustCompile(`^(key|user):[0-9a-f]{8}$`)
)

// The reasons a bearer is refused with, as the access line's code and the "admin auth rejected" line say.
var adminRejectReasons = []string{"missing", "not_a_token", "expired", "signature", "unknown_key", "role", "not_admin", "not_allowed"}

// adminLogKeys is every line the admin chain writes, with the keys it may carry besides logx's and the
// request's (docs/OPERATIONS.md → Data protection (logs)): the actor as a pseudonymous short id, a reason from
// a closed set, counts. Never a token, an email, an address, a note or a code.
var adminLogKeys = logcheck.Lines{
	"http":                logcheck.HTTP(append([]string{"mfa_required", "key_only", "jwks_unavailable", "account_check"}, adminRejectReasons...)...),
	"admin auth rejected": {"reason": logcheck.Enum(adminRejectReasons...)},
	"admin second factor required": {"reason": logcheck.Enum("mfa_missing", "mfa_stale"),
		"admin_actor": logcheck.Pattern(actorLabel)},
	"admin auth unavailable":       {"reason": logcheck.Enum("jwks_unavailable", "account_check")},
	"admin key-only route refused": {"admin_actor": logcheck.Pattern(actorLabel)},
	"admin feedback read": {"since": logcheck.Time, "n": logcheck.Count, "admin_actor": logcheck.Pattern(actorLabel),
		"admin_key": logcheck.Pattern(shortHex)},
	"admin users read": {"emails": logcheck.Bool, "n": logcheck.Count, "admin_actor": logcheck.Pattern(actorLabel),
		"admin_key": logcheck.Pattern(shortHex)},
	"invite created": {"max_uses": logcheck.Count, "admin_actor": logcheck.Pattern(actorLabel)},
	"beta access set by the operator": {"grant": logcheck.Bool, "target_user": logcheck.Pattern(shortHex), "admin_actor": logcheck.Pattern(actorLabel),
		"admin_key": logcheck.Pattern(shortHex)},
	// The JWKS client's own line when the keys cannot be fetched (the outage below): the URL is the public key set's.
	"Failed to refresh HTTP JWK Set from remote HTTP resource.": {"error": logcheck.Text(200), "url": logcheck.Text(200)},
	"request failed": logcheck.RequestFailed(),
}

// Every admin log line names its actor by a short pseudonymous id and holds no token, no email, no address
// and no body (design §4.4, §15). The key's lines keep admin_key for the queries that predate the token path.
func TestAdminLogsHoldNoTokensEmailsOrAddresses(t *testing.T) {
	buf := &bytes.Buffer{}
	log := logx.NewWriter(buf, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })
	d := newAdminDBLog(t, log, "https://ryftapp.de")

	const ip, mail = "203.0.113.77", "anna.private@example.test"
	tokOp := d.token(d.op, nil)
	tokNoRow := d.token(d.noRow, nil)
	tokAAL1 := d.token(d.op, func(c jwt.MapClaims) { c["aal"] = "aal1" })
	tokStale := d.token(d.op, func(c jwt.MapClaims) {
		c["amr"] = []map[string]any{{"method": "totp", "timestamp": time.Now().Add(-20 * time.Hour).Unix()}}
	})
	tokApp := d.jwks.sign(t, appClaims(uuid.New()))
	tokExpired := d.token(d.op, func(c jwt.MapClaims) {
		c["exp"] = time.Now().Add(-time.Hour).Unix()
		c["iat"] = time.Now().Add(-2 * time.Hour).Unix()
	})
	send := func(method, path, bearer, body string, want int) {
		t.Helper()
		if a := do(d.srv, method, path, bearer, body, "X-Forwarded-For", "198.51.100.1, "+ip, "Origin", "https://ryftapp.de"); a.status != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, a.status, a.body, want)
		}
	}
	send("GET", "/v1/admin/users", "", "", 401)
	send("GET", "/v1/admin/users", "garbage", "", 401)
	send("GET", "/v1/admin/users", tokApp, "", 401)
	send("GET", "/v1/admin/users", tokNoRow, "", 401)
	send("GET", "/v1/admin/users", tokExpired, "", 401)
	send("GET", "/v1/admin/users?emails=1", tokAAL1, "", 403)
	send("GET", "/v1/admin/users?emails=1", tokStale, "", 403)
	send("GET", "/v1/admin/users?emails=1", tokOp, "", 200)
	send("GET", "/v1/admin/users", testAdminKey, "", 200)
	send("GET", "/v1/admin/feedback", tokOp, "", 200)
	send("POST", "/v1/admin/invites", tokOp, `{"note": "for `+mail+`", "max_uses": 1}`, 201)
	send("POST", "/v1/admin/users/"+d.target.ID.String()[:8]+"/access", testAdminKey, "", 204)
	send("PUT", "/v1/admin/flags/zz.logs", tokOp, `{"enabled": true, "rollout_pct": 1}`, 403)

	// An outage of the signing keys, and a flood.
	down := newJWKSStub(t, true)
	outage := httpx.Instrument(log, adminRoutes(quietLogged(log), adminOptions{Key: testAdminKey, Verifier: down.verifier(t),
		Accounts: newFakeAccounts(d.op), Limiter: ratelimit.New(0.001, 1), Timeout: time.Second}))
	if a := do(outage, "GET", "/v1/admin/flags", down.sign(t, adminClaims(d.op)), "", "X-Forwarded-For", ip); a.status != 503 {
		t.Fatalf("outage: %d", a.status)
	}
	if a := do(outage, "GET", "/v1/admin/flags", "x", "", "X-Forwarded-For", ip); a.status != 429 {
		t.Fatalf("flood: %d", a.status)
	}

	forbid := logcheck.Forbidden{Texts: []string{tokOp, tokNoRow, tokAAL1, tokStale, tokApp, tokExpired, "garbage", testAdminKey,
		ip, "198.51.100.1", mail, "operator@example.test", "from the page", "Authorization", "Bearer"}}
	out, problems := logcheck.Check(buf.String(), logcheck.Standard("api"), adminLogKeys, forbid)
	for _, p := range problems {
		t.Error(p)
	}

	// The lines say what happened, with the actor where there is one.
	if n := len(out["admin auth rejected"]); n != 5 { // the five 401s above; the flood's 429 never gets to the bearer
		t.Errorf("admin auth rejected lines: %d", n)
	}
	for _, l := range out["admin feedback read"] {
		if l["admin_actor"] != "user:"+d.op.String()[:8] || l["admin_key"] != nil {
			t.Errorf("a token caller's line names the user and carries no admin_key: %v", l)
		}
	}
	keyLines := 0
	for _, l := range out["admin users read"] {
		if a, _ := l["admin_actor"].(string); a == "key:"+keyFingerprint(testAdminKey) {
			keyLines++
			if l["admin_key"] != keyFingerprint(testAdminKey) {
				t.Errorf("a key caller's line keeps admin_key: %v", l)
			}
		}
	}
	if keyLines != 1 {
		t.Errorf("one users read by the key: %d", keyLines)
	}
	if len(out["admin second factor required"]) != 2 || len(out["admin key-only route refused"]) != 1 || len(out["admin auth unavailable"]) != 1 {
		t.Errorf("mfa %d, key-only %d, unavailable %d", len(out["admin second factor required"]), len(out["admin key-only route refused"]),
			len(out["admin auth unavailable"]))
	}
	codes := map[string]int{}
	for _, l := range out["http"] {
		if c, ok := l["code"].(string); ok {
			codes[c]++
		}
	}
	for code, want := range map[string]int{"missing": 1, "not_a_token": 1, "not_admin": 1, "not_allowed": 1, "expired": 1, model.CodeMFARequired: 2,
		"key_only": 1, "jwks_unavailable": 1} {
		if codes[code] != want {
			t.Errorf("access lines with code %q: %d, want %d (%v)", code, codes[code], want, codes)
		}
	}
	if !strings.Contains(buf.String(), `"user_id":"`+d.op.String()+`"`) {
		t.Error("a token caller's request carries its user id like any user route")
	}
}

func quietLogged(log *slog.Logger) *handler {
	h := quietHandler()
	h.log = log
	return h
}
