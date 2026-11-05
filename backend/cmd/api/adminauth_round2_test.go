package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// An operator's token gets a server-side projection of the users list and of the feedback: the account and
// its access state, the message and where it stands. None of the health-derived, device or diagnostic
// fields leave the server for a browser; the key gets the whole answers (docs/API.md → Admin routes).
func TestAdminTokenGetsAProjection(t *testing.T) {
	d := newAdminDB(t)
	ctx := context.Background()
	email := "tester.private@example.test"
	u, err := d.st.EnsureUser(ctx, store.Identity{ID: uuid.New(), Email: email, Provider: "apple"})
	if err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	fid, err := d.st.AddFeedback(ctx, u.ID, model.FeedbackPost{Message: "Sleep stages are empty", AppVersion: str("0.9.0"),
		Build: str("41"), DeviceModel: str("iPhone17,1"), OSVersion: str("26.1"), RequestIDs: []string{"01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"},
		Diagnostics: json.RawMessage(`{"hrv_ms": 61.7}`)}, "bug")
	if err != nil {
		t.Fatal(err)
	}
	tok := d.token(d.op, nil)

	// The fields only the key sees (health-derived data quality, device, consent, feedback counts).
	droppedUser := []string{"days_scored_14d", "nights_with_stages_14d", "days_with_hrv_14d", "calibrating", "sources", "max_hr_source",
		"consent_version", "app_version", "os_version", "open_feedback", "dup"}
	droppedFeedback := []string{"diagnostics", "request_ids", "device_model", "os_version", "app_version", "build", "user_id"}

	asKey := do(d.srv, "GET", "/v1/admin/users?emails=1", testAdminKey, "")
	asTok := do(d.srv, "GET", "/v1/admin/users?emails=1", tok, "")
	if asKey.status != 200 || asTok.status != 200 {
		t.Fatalf("%d %d", asKey.status, asTok.status)
	}
	for _, k := range droppedUser {
		if !bytes.Contains([]byte(asKey.body), []byte(`"`+k+`"`)) {
			t.Errorf("the key's users answer has %q", k)
		}
		if bytes.Contains([]byte(asTok.body), []byte(`"`+k+`"`)) {
			t.Errorf("the token's users answer must not carry %q: %s", k, asTok.body)
		}
	}
	// Decoding strictly into the brief type proves there is nothing else in it.
	var users []model.AdminUserBrief
	dec := json.NewDecoder(strings.NewReader(asTok.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&users); err != nil {
		t.Fatalf("the token's answer is the brief shape and nothing more: %v %s", err, asTok.body)
	}
	found := false
	for _, b := range users {
		if b.ID == u.ID {
			found = b.Email != nil && *b.Email == email && b.ShortID == u.ID.String()[:8] && b.Provider != nil && !b.BetaAccess
		}
	}
	if !found {
		t.Errorf("the brief keeps id, short id, email (asked for), provider and access state: %+v", users)
	}
	// Without ?emails=1 the brief has no email at all, as for the key.
	if a := do(d.srv, "GET", "/v1/admin/users", tok, ""); strings.Contains(a.body, `"email"`) || strings.Contains(a.body, email) {
		t.Errorf("no email unless asked: %s", a.body)
	}

	kf := do(d.srv, "GET", "/v1/admin/feedback", testAdminKey, "")
	tf := do(d.srv, "GET", "/v1/admin/feedback", tok, "")
	if kf.status != 200 || tf.status != 200 {
		t.Fatalf("%d %d", kf.status, tf.status)
	}
	for _, k := range droppedFeedback {
		if !bytes.Contains([]byte(kf.body), []byte(`"`+k+`"`)) {
			t.Errorf("the key's feedback answer has %q", k)
		}
		if bytes.Contains([]byte(tf.body), []byte(`"`+k+`"`)) {
			t.Errorf("the token's feedback answer must not carry %q: %s", k, tf.body)
		}
	}
	for _, leak := range []string{"iPhone17,1", "61.7", "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c", u.ID.String()} {
		if strings.Contains(tf.body, leak) {
			t.Errorf("the token's feedback answer leaks %q: %s", leak, tf.body)
		}
	}
	var fb []model.FeedbackBrief
	dec = json.NewDecoder(strings.NewReader(tf.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fb); err != nil || len(fb) != 1 || fb[0].ID != fid || fb[0].Message != "Sleep stages are empty" ||
		fb[0].Category != "bug" || fb[0].UserShortID != u.ID.String()[:8] || fb[0].ResolvedAt != nil {
		t.Fatalf("the brief keeps id, short user ref, category, message, times: %v %+v", err, fb)
	}
	// The key's answers decode into the whole types, unchanged.
	var whole []model.Feedback
	if err := json.Unmarshal([]byte(kf.body), &whole); err != nil || len(whole) != 1 || whole[0].DeviceModel == nil || whole[0].Diagnostics == nil {
		t.Errorf("the key's feedback is whole: %v %+v", err, whole)
	}
	// An actor-less call (never through the chain) gets the projection too.
	if wholeView(httptest.NewRequest(http.MethodGet, "/v1/admin/users", nil)) {
		t.Error("an unknown actor must not get the whole view")
	}
}

// A refusal of a real operator for want of a fresh second factor is audited: who, which route, 403, no
// details; the rows are bounded; a stranger's refusal writes none.
func TestAdminAuditsMFARefusals(t *testing.T) {
	d := newAdminDB(t)
	aal1 := d.token(d.op, func(c jwt.MapClaims) { c["aal"] = "aal1"; delete(c, "amr") })
	stale := d.token(d.op, func(c jwt.MapClaims) {
		c["amr"] = []map[string]any{{"method": "totp", "timestamp": time.Now().Add(-30 * time.Hour).Unix()}}
	})
	// Strangers: no row, no claim, no allow-list entry. Nothing is written for them.
	for _, b := range []string{"garbage", d.token(d.noRow, func(c jwt.MapClaims) { c["aal"] = "aal1" }), d.jwks.sign(t, appClaims(uuid.New()))} {
		if a := do(d.srv, "GET", "/v1/admin/invites", b, ""); a.status != 401 {
			t.Fatalf("%d", a.status)
		}
	}
	if rows := d.audit(); len(rows) != 0 {
		t.Fatalf("a refused stranger is not audited: %+v", rows)
	}
	if a := do(d.srv, "DELETE", "/v1/admin/invites/RYFT-2222-2222", aal1, ""); a.status != 403 || a.code() != model.CodeMFARequired {
		t.Fatalf("%d %s", a.status, a.body)
	}
	if a := do(d.srv, "PUT", "/v1/admin/flags/zz.x", stale, `{}`); a.status != 403 || a.code() != model.CodeMFARequired {
		t.Fatalf("%d %s", a.status, a.body)
	}
	rows := d.audit()
	if len(rows) != 2 {
		t.Fatalf("two refusals, two rows: %+v", rows)
	}
	for i, want := range []string{"PUT /v1/admin/flags/{key}", "DELETE /v1/admin/invites/{code}"} { // newest first
		r := rows[i]
		if r.Action != want || r.Status != 403 || r.Actor.Kind != "user" || r.Actor.ID == nil || *r.Actor.ID != d.op.String() ||
			r.KeyFingerprint != "" || string(r.Details) != "{}" || r.RequestID == "" {
			t.Errorf("row %d: %+v (details %s), want %s", i, r, r.Details, want)
		}
	}
	// A stale session that keeps retrying writes a bounded number of rows (a burst of 3 per operator and pod).
	for i := 0; i < 20; i++ {
		do(d.srv, "GET", "/v1/admin/invites", aal1, "")
	}
	if n := len(d.audit()); n > 3 {
		t.Errorf("%d audit rows after 22 refusals, want at most the burst of 3", n)
	}
}

// Nothing is registered on the admin mux except through the classified table (adminRoute.tokenOK, zero value
// key-only), apart from the catch-all that mounts the chain. A route added by a direct HandleFunc would reach
// a token without anyone having classified it.
func TestAdminRoutesAreOnlyRegisteredThroughTheTable(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seenCatchAll := false
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, _ := strconv.Unquote(lit.Value)
			if !strings.Contains(pattern, "/v1/admin") {
				return true
			}
			if pattern == "/v1/admin/" {
				seenCatchAll = true
				return true
			}
			t.Errorf("%s: %s registers %q directly: put an admin route in admin.routes() with its class", fset.Position(call.Pos()), sel.Sel.Name, pattern)
			return true
		})
	}
	if !seenCatchAll {
		t.Error("the scan found no /v1/admin/ registration at all: it is broken")
	}
}

// A malformed ADMIN_WEB_ORIGINS must stop the api before it waits for any dependency: LoadAdmin is called
// in main before db.Open, eventbus.Open and auth.FromConfig.
func TestMainLoadsAdminConfigBeforeDependencies(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]token.Pos{}
	ast.Inspect(file, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name != "main" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok {
				name := id.Name + "." + sel.Sel.Name
				if _, seen := pos[name]; !seen {
					pos[name] = call.Pos()
				}
			}
		}
		return true
	})
	admin, ok := pos["config.LoadAdmin"]
	if !ok {
		t.Fatal("main does not call config.LoadAdmin")
	}
	for _, later := range []string{"db.Open", "eventbus.Open", "auth.FromConfig"} {
		p, ok := pos[later]
		if !ok {
			t.Fatalf("main does not call %s: the check is stale", later)
		}
		if admin > p {
			t.Errorf("config.LoadAdmin must come before %s in main, so a malformed value fails before any dependency wait", later)
		}
	}
}
