package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// adminDB is the real chain (CORS, limit, bearer check, audit, routes) on a migrated Postgres, with an
// operator who has a row, one who has none and one whose account was deleted.
type adminDB struct {
	t                 *testing.T
	pool              *pgxpool.Pool
	st                *store.Store
	jwks              *jwksStub
	srv               http.Handler
	op, noRow, tombed uuid.UUID
	target            model.User // an app user the routes can act on
	startedAt         time.Time
}

func newAdminDB(t *testing.T, origins ...string) *adminDB {
	t.Helper()
	return newAdminDBLog(t, quietHandler().log, origins...)
}

// newAdminDBLog is newAdminDB logging to log.
func newAdminDBLog(t *testing.T, log *slog.Logger, origins ...string) *adminDB {
	t.Helper()
	pool := testPool(t)
	st := store.New(pool)
	h := quietHandler()
	h.st, h.log = st, log
	d := &adminDB{t: t, pool: pool, st: st, jwks: newJWKSStub(t, false), op: uuid.New(), noRow: uuid.New(), tombed: uuid.New(),
		startedAt: time.Now().Add(-time.Second)}
	ctx := context.Background()
	for _, q := range []struct {
		sql string
		id  uuid.UUID
	}{
		{`INSERT INTO admin_accounts (user_id, note) VALUES ($1, 'operator')`, d.op},
		{`INSERT INTO admin_accounts (user_id, note) VALUES ($1, 'deleted operator')`, d.tombed},
		{`INSERT INTO deleted_users (id) VALUES ($1)`, d.tombed},
	} {
		if _, err := pool.Exec(ctx, q.sql, q.id); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	if d.target, err = st.EnsureUser(ctx, store.Identity{ID: uuid.New()}); err != nil {
		t.Fatal(err)
	}
	// Instrument as in main: the request id and the matched route (the audit's action) come from it.
	d.srv = httpx.Instrument(h.log, adminRoutes(h, adminOptions{Key: testAdminKey, Verifier: d.jwks.verifier(t), Origins: origins,
		Limiter: wide(), Timeout: 5 * time.Second}))
	return d
}

func (d *adminDB) token(sub uuid.UUID, mod func(jwt.MapClaims)) string {
	c := adminClaims(sub)
	if mod != nil {
		mod(c)
	}
	return d.jwks.sign(d.t, c)
}

// audit lists the audit rows since the test began.
func (d *adminDB) audit() []model.AuditRecord {
	d.t.Helper()
	rows, err := d.st.AuditSince(context.Background(), d.startedAt, 1000)
	if err != nil {
		d.t.Fatal(err)
	}
	return rows
}

type adminRouteCall struct {
	method, path, body string
	keyOnly            bool
}

// calls is every route of adminRoutes with a request that reaches its handler, and its class (design §4.3).
func (d *adminDB) calls() []adminRouteCall {
	u8 := d.target.ID.String()[:8]
	return []adminRouteCall{
		{"GET", "/v1/admin/flags", "", false},
		{"PUT", "/v1/admin/flags/zz.matrix", `{"enabled": false, "rollout_pct": 0}`, true},
		{"DELETE", "/v1/admin/flags/zz.matrix", "", true},
		{"POST", "/v1/admin/invites", `{"note": "matrix"}`, false},
		{"GET", "/v1/admin/invites", "", false},
		{"DELETE", "/v1/admin/invites/RYFT-2222-2222", "", false},
		{"GET", "/v1/admin/feedback", "", false},
		{"POST", "/v1/admin/feedback/1/resolve", "", false},
		{"GET", "/v1/admin/users", "", false},
		{"GET", "/v1/admin/users/" + u8, "", true},
		{"POST", "/v1/admin/users/" + u8 + "/access", "", false},
		{"DELETE", "/v1/admin/users/" + u8 + "/access", "", false},
		{"POST", "/v1/admin/users/ffffffff/merge-into/fffffffe", "", true},
		{"POST", "/v1/admin/users/" + u8 + "/resync-hint", "", true},
		{"GET", "/v1/admin/audit", "", false},
	}
}

// Every route is in exactly one class, and the classification is the design's table (§4.3): the page may
// reach what it needs and nothing destructive. Adding a route without saying who may call it fails here.
func TestAdminRoutesAreClassified(t *testing.T) {
	want := map[string]bool{ // pattern -> key-only
		"GET /v1/admin/flags": false, "PUT /v1/admin/flags/{key}": true, "DELETE /v1/admin/flags/{key}": true,
		"POST /v1/admin/invites": false, "GET /v1/admin/invites": false, "DELETE /v1/admin/invites/{code}": false,
		"GET /v1/admin/feedback": false, "POST /v1/admin/feedback/{id}/resolve": false,
		"GET /v1/admin/users": false, "GET /v1/admin/users/{id}": true,
		"POST /v1/admin/users/{id}/access": false, "DELETE /v1/admin/users/{id}/access": false,
		"POST /v1/admin/users/{id}/merge-into/{to}": true, "POST /v1/admin/users/{id}/resync-hint": true,
		"GET /v1/admin/audit": false,
	}
	got := map[string]bool{}
	for _, r := range (admin{}).routes() {
		if _, dup := got[r.pattern]; dup {
			t.Errorf("%s is registered twice", r.pattern)
		}
		got[r.pattern] = r.keyOnly
	}
	for p, k := range got {
		if w, ok := want[p]; !ok {
			t.Errorf("%s is a new admin route: add it to this table with its class (key-only or token), and to docs/API.md → Admin routes", p)
		} else if w != k {
			t.Errorf("%s: key-only %v, the design says %v", p, k, w)
		}
	}
	for p := range want {
		if _, ok := got[p]; !ok {
			t.Errorf("%s is gone", p)
		}
	}
}

// T1.3: every admin route × every kind of bearer → the expected status, the key-only routes with a token
// → 403 (no code) and an audit row naming the user, the key still works everywhere.
func TestAdminRouteMatrix(t *testing.T) {
	d := newAdminDB(t)
	calls := d.calls()
	if len(calls) != len((admin{}).routes()) {
		t.Fatalf("the matrix has %d calls for %d routes", len(calls), len((admin{}).routes()))
	}
	for _, c := range calls { // every route of the table is called
		found := false
		for _, r := range (admin{}).routes() {
			p := r.pattern
			found = found || (strings.HasPrefix(p, c.method+" ") && sameShape(strings.TrimPrefix(p, c.method+" "), c.path) && r.keyOnly == c.keyOnly)
		}
		if !found {
			t.Errorf("%s %s does not match a route of the table with its class", c.method, c.path)
		}
	}

	bearers := []struct {
		name   string
		bearer string
		// want says the outcome for a token route and for a key-only route.
		tokenRoute, keyRoute string
	}{
		{"no bearer", "", "401", "401"},
		{"garbage", "garbage", "401", "401"},
		{"an app user's token", d.jwks.sign(t, appClaims(uuid.New())), "401", "401"},
		{"an app user's token with a top-level admin role", d.jwks.sign(t, func() jwt.MapClaims { c := appClaims(uuid.New()); c["role"] = "admin"; return c }()), "401", "401"},
		{"role in user_metadata only", d.jwks.sign(t, func() jwt.MapClaims {
			c := adminClaims(d.op)
			c["app_metadata"] = map[string]any{}
			c["user_metadata"] = map[string]any{"role": "admin"}
			return c
		}()), "401", "401"},
		{"an operator claim without a row", d.token(d.noRow, nil), "401", "401"},
		{"a deleted operator (row and tombstone)", d.token(d.tombed, nil), "401", "401"},
		{"an operator, aal1", d.token(d.op, func(c jwt.MapClaims) { c["aal"] = "aal1"; delete(c, "amr") }), "mfa", "mfa"},
		{"an operator, TOTP 13 h old", d.token(d.op, func(c jwt.MapClaims) {
			c["amr"] = []map[string]any{{"method": "totp", "timestamp": time.Now().Add(-13 * time.Hour).Unix()}}
		}), "mfa", "mfa"},
		{"an operator, aal2 and fresh", d.token(d.op, nil), "handler", "key-only"},
		{"the key", testAdminKey, "handler", "handler"},
	}
	for _, b := range bearers {
		for _, c := range calls {
			a := do(d.srv, c.method, c.path, b.bearer, c.body)
			want := b.tokenRoute
			if c.keyOnly {
				want = b.keyRoute
			}
			ctx := b.name + ": " + c.method + " " + c.path
			switch want {
			case "401":
				if a.status != 401 || a.code() != "" || a.header.Get("WWW-Authenticate") != `Bearer realm="ryft-admin"` {
					t.Errorf("%s: %d %s", ctx, a.status, a.body)
				}
			case "mfa":
				if a.status != 403 || a.code() != model.CodeMFARequired {
					t.Errorf("%s: %d %s", ctx, a.status, a.body)
				}
			case "key-only":
				if a.status != 403 || a.code() != "" || !strings.Contains(a.body, "this route needs the admin key") {
					t.Errorf("%s: %d %s", ctx, a.status, a.body)
				}
			case "handler":
				if a.status == 401 || a.status == 403 || a.status == 503 || a.status == 500 {
					t.Errorf("%s: %d %s, want the handler's own answer", ctx, a.status, a.body)
				}
			}
		}
	}
}

// sameShape reports whether path is an instance of the ServeMux pattern ({x} matches one segment).
func sameShape(pattern, path string) bool {
	ps, as := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(ps) != len(as) {
		return false
	}
	for i := range ps {
		wildcard := strings.HasPrefix(ps[i], "{") && strings.HasSuffix(ps[i], "}")
		if !wildcard && ps[i] != as[i] {
			return false
		}
	}
	return true
}

// The audit names who acted (D-E): the user id for a token, the fingerprint for the key; a refused
// key-only attempt is recorded, a refused bearer is not.
func TestAdminAuditNamesTheActor(t *testing.T) {
	d := newAdminDB(t)
	tok := d.token(d.op, nil)
	reqs := []struct {
		method, path, bearer string
		want                 int
	}{
		{"GET", "/v1/admin/users", tok, 200},
		{"PUT", "/v1/admin/flags/zz.nope", tok, 403},
		{"POST", "/v1/admin/users/" + d.target.ID.String()[:8] + "/resync-hint", tok, 403},
		{"GET", "/v1/admin/flags", testAdminKey, 200},
		{"GET", "/v1/admin/flags", "garbage", 401},
		{"GET", "/v1/admin/users", d.token(d.noRow, nil), 401},
	}
	for _, r := range reqs {
		if a := do(d.srv, r.method, r.path, r.bearer, `{"enabled": true, "rollout_pct": 0}`); a.status != r.want {
			t.Fatalf("%s %s: %d %s, want %d", r.method, r.path, a.status, a.body, r.want)
		}
	}
	rows := d.audit()
	if len(rows) != 4 {
		t.Fatalf("four authorized calls, four rows (the 401s are not audited): %+v", rows)
	}
	// Newest first.
	type row struct {
		kind, id, action string
		status           int
	}
	var got []row
	for _, r := range rows {
		id := ""
		if r.Actor.ID != nil {
			id = *r.Actor.ID
		}
		got = append(got, row{r.Actor.Kind, id, r.Action, r.Status})
		if r.RequestID == "" {
			t.Errorf("an audit row carries the request id: %+v", r)
		}
	}
	slices.Reverse(got)
	want := []row{
		{"user", d.op.String(), "GET /v1/admin/users", 200},
		{"user", d.op.String(), "PUT /v1/admin/flags/{key}", 403},
		{"user", d.op.String(), "POST /v1/admin/users/{id}/resync-hint", 403},
		{"key", keyFingerprint(testAdminKey), "GET /v1/admin/flags", 200},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("audit rows (oldest first):\n%+v\nwant\n%+v", got, want)
	}
	for _, r := range rows {
		if (r.Actor.Kind == "user") != (r.KeyFingerprint == "") {
			t.Errorf("key_fingerprint is empty exactly for user actors: %+v", r)
		}
	}

	// And GET /v1/admin/audit shows the same, as the contract documents (API.md → The actor).
	a := do(d.srv, "GET", "/v1/admin/audit?since="+d.startedAt.UTC().Format(time.RFC3339), tok, "")
	var list []model.AuditRecord
	if a.status != 200 || json.Unmarshal([]byte(a.body), &list) != nil || len(list) < 4 {
		t.Fatalf("audit: %d %s", a.status, a.body)
	}
	if !strings.Contains(a.body, `"actor":{"kind":"user","id":"`+d.op.String()+`"}`) || !strings.Contains(a.body, `"actor":{"kind":"key","id":"`+keyFingerprint(testAdminKey)+`"}`) {
		t.Errorf("the audit answer does not carry the actors: %s", a.body)
	}
}

// A token that passes through the chain is a working admin: the page's calls succeed (design §7.4).
func TestAdminTokenReachesThePageRoutes(t *testing.T) {
	d := newAdminDB(t)
	tok := d.token(d.op, nil)
	inv := do(d.srv, "POST", "/v1/admin/invites", tok, `{"note": "from the page", "max_uses": 1}`)
	var created model.BetaInvite
	if inv.status != 201 || json.Unmarshal([]byte(inv.body), &created) != nil || created.Code == "" {
		t.Fatalf("create invite: %d %s", inv.status, inv.body)
	}
	if a := do(d.srv, "GET", "/v1/admin/invites", tok, ""); a.status != 200 || !strings.Contains(a.body, created.Code) {
		t.Fatalf("list invites: %d %s", a.status, a.body)
	}
	if a := do(d.srv, "POST", "/v1/admin/users/"+d.target.ID.String()[:8]+"/access", tok, ""); a.status != 204 {
		t.Fatalf("grant: %d %s", a.status, a.body)
	}
	if a := do(d.srv, "GET", "/v1/admin/flags", tok, ""); a.status != 200 {
		t.Fatalf("flags: %d %s", a.status, a.body)
	}
	if a := do(d.srv, "DELETE", "/v1/admin/invites/"+created.Code, tok, ""); a.status != 204 {
		t.Fatalf("delete invite: %d %s", a.status, a.body)
	}
	// Deleting the row ends the session on the next call; the key is unaffected.
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM admin_accounts WHERE user_id = $1`, d.op); err != nil {
		t.Fatal(err)
	}
	if a := do(d.srv, "GET", "/v1/admin/flags", tok, ""); a.status != 401 {
		t.Fatalf("after the row is deleted: %d %s", a.status, a.body)
	}
	if a := do(d.srv, "GET", "/v1/admin/flags", testAdminKey, ""); a.status != 200 {
		t.Fatalf("the key: %d", a.status)
	}
}
