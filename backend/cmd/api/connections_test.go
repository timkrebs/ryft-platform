package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/connect/fake"
	"github.com/timkrebs/ryft/internal/events/outbox"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/sealed"
	"github.com/timkrebs/ryft/internal/store"
)

// The Connections routes, the callback and the tick's steps on a migrated database, through the api's own mux
// (newMux): the bearer, the app token, the invite gate, the per-user limit, then the routes (D257).

const connPolicy = "2026-10-26"

// tokenVerifier takes the user's id as the token.
type tokenVerifier struct{}

func (tokenVerifier) Verify(_ context.Context, token string) (auth.Principal, error) {
	id, err := uuid.Parse(token)
	if err != nil {
		return auth.Principal{}, auth.Unauthorized("malformed", err)
	}
	return auth.Principal{UserID: id, Email: id.String()[:8] + "@example.com", Provider: "apple"}, nil
}
func (tokenVerifier) Ready(context.Context) error { return nil }

// asProvider is the fake under another name: a provider that has a flag (every real one does).
type asProvider struct {
	*fake.Provider
	name string
}

func (a asProvider) Name() string        { return a.name }
func (a asProvider) DisplayName() string { return strings.ToUpper(a.name[:1]) + a.name[1:] }

type connRig struct {
	t     *testing.T
	pool  *pgxpool.Pool
	st    *store.Store
	h     *handler
	conn  *connectAPI
	fake  *fake.Provider // served as "fake"
	other *fake.Provider // served as "garmin" (a flag gates it)
	srv   http.Handler
	logs  *lockedBuffer
	now   time.Time
}

func newConnRig(t *testing.T, mod ...func(*config.Beta, *config.AppAttest)) *connRig {
	t.Helper()
	pool := testPool(t)
	rg := &connRig{t: t, pool: pool, st: store.New(pool), logs: &lockedBuffer{}, fake: fake.New("x"), other: fake.New("x"), now: time.Now()}
	log := logx.NewWriter(rg.logs, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })
	reg, err := connect.NewRegistry(rg.fake, asProvider{rg.other, "garmin"})
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	key, err := sealed.ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	beta := config.Beta{Gate: config.ModeOff, Consent: config.ModeOn, PrivacyPolicyVersion: connPolicy}
	attest := config.AppAttest{Mode: config.ModeOff}
	for _, m := range mod {
		m(&beta, &attest)
	}
	cfg := config.Connect{Fake: true, Providers: []string{"fake", "garmin"}, CallbackBase: "http://localhost:8082",
		Caps: map[string]int{"fake": 5, "garmin": 5}}
	ob := outbox.New(pool, "api", nil, log)
	rg.conn = newConnectAPI(rg.st, reg, cfg, key, beta, ob, log)
	rg.conn.now = func() time.Time { return rg.now }
	rg.h = &handler{st: rg.st, log: log, cache: newInsightCache(), engine: scoring.NewEngine(rg.st, log), beta: beta, attestMode: attest.Mode,
		conn: rg.conn, accounts: &accounts{st: rg.st, log: log, conn: rg.conn}}
	mux := newMux(rg.h, muxOptions{log: log, verifier: tokenVerifier{}, ready: func(context.Context) error { return nil },
		tick: http.NotFoundHandler(), gate: beta.Gate, attest: attest, authMode: config.AuthAPIKey, clients: httpx.NewClientKeys(1)})
	rg.srv = httpx.Instrument(log, nil, httpx.Recover(httpx.SecurityHeaders(mux)))
	// The shipped flag is dark (0 %): garmin is on for allow-listed accounts only.
	return rg
}

// account is a new account with consent to the policy, unless noConsent.
func (rg *connRig) account(noConsent ...bool) uuid.UUID {
	rg.t.Helper()
	id := uuid.New()
	if _, err := rg.st.EnsureUser(context.Background(), store.Identity{ID: id, Email: "a@example.com", Provider: "apple"}); err != nil {
		rg.t.Fatal(err)
	}
	if len(noConsent) == 0 {
		if err := rg.st.GrantConsent(context.Background(), id, connPolicy, nil); err != nil {
			rg.t.Fatal(err)
		}
	}
	return id
}

func (rg *connRig) flag(provider string, allow ...uuid.UUID) {
	rg.t.Helper()
	if _, _, err := rg.st.PutFlag(context.Background(), model.FeatureFlag{Key: "connect." + provider, Enabled: len(allow) > 0, RolloutPct: 0, AllowUserIDs: allow}); err != nil {
		rg.t.Fatal(err)
	}
}

func (rg *connRig) do(user uuid.UUID, method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	rg.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	if user != uuid.Nil {
		r.Header.Set("Authorization", "Bearer "+user.String())
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	rg.srv.ServeHTTP(w, r)
	return w
}

// nullableBool is a bool or JSON null.
func nullableBool(v any) error {
	if v == nil {
		return nil
	}
	return logcheck.Bool(v)
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder, want int) T {
	t.Helper()
	var v T
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("%v: %s", err, w.Body)
	}
	return v
}

func codeOf(w *httptest.ResponseRecorder) string {
	var b struct{ Code string }
	_ = json.Unmarshal(w.Body.Bytes(), &b)
	return b.Code
}

// begin runs start and the provider's sheet: it returns the callback's redirect.
func (rg *connRig) begin(user uuid.UUID, provider string) (authorizeURL string, redirect *url.URL) {
	rg.t.Helper()
	st := decode[model.ConnectStart](rg.t, rg.do(user, "POST", "/v1/me/connections/"+provider+"/start", nil), 200)
	u, err := url.Parse(st.AuthorizeURL)
	if err != nil {
		rg.t.Fatal(err)
	}
	// The fake "authorises" at once: its authorize URL is the callback itself, on the api's own address.
	w := rg.do(uuid.Nil, "GET", u.RequestURI(), nil)
	if w.Code != 302 {
		rg.t.Fatalf("callback: %d %s", w.Code, w.Body)
	}
	loc, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		rg.t.Fatal(err)
	}
	return st.AuthorizeURL, loc
}

// connectAccount takes an account through start, callback and confirm; it returns the connection.
func (rg *connRig) connectAccount(user uuid.UUID, provider string) model.Connection {
	rg.t.Helper()
	_, loc := rg.begin(user, provider)
	if loc.Query().Get("status") != "ok" {
		rg.t.Fatalf("redirect %s", loc)
	}
	return decode[model.Connection](rg.t, rg.do(user, "POST", "/v1/me/connections/"+provider+"/confirm", model.ConnectConfirmPost{Ticket: loc.Query().Get("ticket")}), 200)
}

func TestConnectionsFlowEndToEnd(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	// Nothing connected; the fake is available (it has no flag), garmin is dark.
	list := decode[model.ConnectionsResponse](t, rg.do(u, "GET", "/v1/me/connections", nil), 200)
	if len(list.Connections) != 0 || len(list.Available) != 1 || list.Available[0].Provider != "fake" || !list.Available[0].Accepting ||
		list.Available[0].History != "none" || strings.Join(list.Available[0].Provides, ",") != "hrv,sleep" {
		t.Fatalf("list %+v", list)
	}
	authorize, loc := rg.begin(u, "fake")
	if loc.Scheme != "ryft" || loc.Host != "connect" || loc.Path != "/fake" || loc.Query().Get("status") != "ok" || len(loc.Query().Get("ticket")) != 32 {
		t.Fatalf("redirect %s", loc)
	}
	// Until the confirm the account has no connection.
	if l := decode[model.ConnectionsResponse](t, rg.do(u, "GET", "/v1/me/connections", nil), 200); len(l.Connections) != 0 {
		t.Fatalf("listed before the confirm: %+v", l.Connections)
	}
	ticket := loc.Query().Get("ticket")
	conn := decode[model.Connection](t, rg.do(u, "POST", "/v1/me/connections/fake/confirm", model.ConnectConfirmPost{Ticket: ticket}), 200)
	au, _ := url.Parse(authorize)
	if conn.Status != "connected" || conn.Provider != "fake" || conn.LastDataAt != nil || conn.History != "none" ||
		conn.ProviderUserID == nil || *conn.ProviderUserID != fake.UserID(au.Query().Get("state")) || len(conn.Permissions) != 2 {
		t.Fatalf("connection %+v", conn)
	}
	// A repeat of the confirm answers the connection again.
	if again := decode[model.Connection](t, rg.do(u, "POST", "/v1/me/connections/fake/confirm", model.ConnectConfirmPost{Ticket: ticket}), 200); again.Status != "connected" {
		t.Fatalf("repeat %+v", again)
	}
	// The used authorize URL is dead: status=expired.
	w := rg.do(uuid.Nil, "GET", func() string { x, _ := url.Parse(authorize); return x.RequestURI() }(), nil)
	if loc2, _ := url.Parse(w.Header().Get("Location")); w.Code != 302 || loc2.Query().Get("status") != "expired" || loc2.Query().Get("ticket") != "" {
		t.Fatalf("replay: %d %s", w.Code, w.Header().Get("Location"))
	}
	// Disconnect: the shape, then the same shape with null.
	del := decode[model.ConnectionDeleted](t, rg.do(u, "DELETE", "/v1/me/connections/fake", nil), 200)
	if del.Status != "disconnected" || del.Deregistered == nil || !*del.Deregistered || !del.DataDeleted {
		t.Fatalf("disconnect %+v", del)
	}
	if !rg.fake.Called("deregister", *conn.ProviderUserID) {
		t.Fatal("the provider was not asked to end the grant")
	}
	again := decode[model.ConnectionDeleted](t, rg.do(u, "DELETE", "/v1/me/connections/fake", nil), 200)
	if again.Deregistered != nil || again.Status != "disconnected" || !again.DataDeleted {
		t.Fatalf("second disconnect %+v", again)
	}
	if l := decode[model.ConnectionsResponse](t, rg.do(u, "GET", "/v1/me/connections", nil), 200); len(l.Connections) != 0 {
		t.Fatalf("connection left: %+v", l.Connections)
	}
	rg.assertNoSecretsInLogs(authorize, ticket)
}

// No state, code, ticket or token in any log line, and the access line of the callback carries no query (a test with
// logcheck, D257).
func (rg *connRig) assertNoSecretsInLogs(authorize, ticket string) {
	rg.t.Helper()
	u, _ := url.Parse(authorize)
	state, code := u.Query().Get("state"), u.Query().Get("code")
	out := rg.logs.String()
	for name, secret := range map[string]string{"state": state, "code": code, "ticket": ticket, "challenge-part-of-code": strings.SplitN(code, ".", 2)[0]} {
		if secret != "" && strings.Contains(out, secret) {
			rg.t.Errorf("a log line holds the %s", name)
		}
	}
	lines := logcheck.Lines{
		"http":          logcheck.HTTP(model.CodeConsentRequired, model.CodeUnknownProvider, model.CodeConnectionLimit, model.CodeConnectTicketInvalid, model.CodeConnectExpired, "app_attest_required"),
		"connect start": {"provider": logcheck.Enum("fake", "garmin", "unknown")},
		"connect callback": {"provider": logcheck.Enum("fake", "garmin", "unknown"), "status": logcheck.Enum("ok", "denied", "expired", "error"),
			"reason": logcheck.Enum("ok", "access_denied", "missing_permission", "state_unknown", "state_expired", "exchange_failed", "no_account", "no_consent", "cap", "provider_unknown")},
		"connect confirm":    {"provider": logcheck.Enum("fake", "garmin"), "outcome": logcheck.Enum("ok", "repeat", "replaced", "relinked")},
		"connect disconnect": {"provider": logcheck.Enum("fake", "garmin", "unknown"), "existed": logcheck.Bool},
		"connect purged":     {"pending": logcheck.Count, "tokens": logcheck.Count, "revoked": logcheck.Count},
		"account deleted": {"deleted_user": logcheck.UUID, "auth_user_deleted": logcheck.Bool, "apple_token_revoked": nullableBool,
			"connections_deregistered": nullableBool},
	}
	_, problems := logcheck.Check(out, logcheck.Standard("api"), lines, logcheck.Forbidden{Texts: []string{state, code, ticket}})
	for _, p := range problems {
		rg.t.Error(p)
	}
	// The callback's access line has its route and path, never the query.
	if strings.Contains(out, "?state=") || strings.Contains(out, "status=ok&ticket") {
		rg.t.Error("a line holds the callback's query")
	}
}

func TestStartRefusalsAndOrder(t *testing.T) {
	rg := newConnRig(t)
	u, noConsent := rg.account(), rg.account(true)
	for name, c := range map[string]struct {
		user   uuid.UUID
		path   string
		status int
		code   string
	}{
		"no consent":                   {noConsent, "/v1/me/connections/fake/start", 403, model.CodeConsentRequired},
		"no consent, unknown provider": {noConsent, "/v1/me/connections/nope/start", 403, model.CodeConsentRequired}, // consent first (API.md)
		"unknown provider":             {u, "/v1/me/connections/nope/start", 404, model.CodeUnknownProvider},
		"reserved id":                  {u, "/v1/me/connections/unknown/start", 404, model.CodeUnknownProvider},
		"flag off for the account":     {u, "/v1/me/connections/garmin/start", 404, model.CodeUnknownProvider},
	} {
		w := rg.do(c.user, "POST", c.path, nil)
		if w.Code != c.status || codeOf(w) != c.code {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body, c.status, c.code)
		}
	}
	// garmin is on for an allow-listed account; the flag gates available too.
	if l := decode[model.ConnectionsResponse](t, rg.do(u, "GET", "/v1/me/connections", nil), 200); len(l.Available) != 1 {
		t.Fatalf("available %+v", l.Available)
	}
	rg.flag("garmin", u)
	if l := decode[model.ConnectionsResponse](t, rg.do(u, "GET", "/v1/me/connections", nil), 200); len(l.Available) != 2 {
		t.Fatalf("available with the flag %+v", l.Available)
	}
	if w := rg.do(u, "POST", "/v1/me/connections/garmin/start", nil); w.Code != 200 {
		t.Fatalf("flagged start: %d %s", w.Code, w.Body)
	}
	// CONSENT_ENFORCE=warn acts as on here; a withdrawn consent too.
	warn := newConnRig(t, func(b *config.Beta, _ *config.AppAttest) { b.Consent = config.ModeWarn })
	if w := warn.do(warn.account(true), "POST", "/v1/me/connections/fake/start", nil); w.Code != 403 || codeOf(w) != model.CodeConsentRequired {
		t.Fatalf("warn: %d %s", w.Code, w.Body)
	}
}

// Cancel the sheet, tap Connect again at the last free place; another account is refused; the caller's own open flow
// never fills the place twice (the lead's decision of 2026-11-23).
func TestCapAtTheLastPlace(t *testing.T) {
	rg := newConnRig(t)
	rg.conn.cfg.Caps["fake"] = 1
	a, b := rg.account(), rg.account()
	for i := 0; i < 2; i++ {
		if w := rg.do(a, "POST", "/v1/me/connections/fake/start", nil); w.Code != 200 {
			t.Fatalf("start %d at the last place: %d %s", i+1, w.Code, w.Body)
		}
	}
	w := rg.do(b, "POST", "/v1/me/connections/fake/start", nil)
	if w.Code != 409 || codeOf(w) != model.CodeConnectionLimit {
		t.Fatalf("another account: %d %s", w.Code, w.Body)
	}
	la := decode[model.ConnectionsResponse](t, rg.do(a, "GET", "/v1/me/connections", nil), 200)
	lb := decode[model.ConnectionsResponse](t, rg.do(b, "GET", "/v1/me/connections", nil), 200)
	if !la.Available[0].Accepting || lb.Available[0].Accepting {
		t.Fatalf("accepting: holder %v, other %v", la.Available[0].Accepting, lb.Available[0].Accepting)
	}
	// The holder connects; a reconnect never meets the cap.
	rg.connectAccount(a, "fake")
	if w := rg.do(a, "POST", "/v1/me/connections/fake/start", nil); w.Code != 200 {
		t.Fatalf("reconnect: %d %s", w.Code, w.Body)
	}
	if w := rg.do(b, "POST", "/v1/me/connections/fake/start", nil); w.Code != 409 {
		t.Fatalf("other account after the connect: %d", w.Code)
	}
}

func TestConfirmRefusals(t *testing.T) {
	rg := newConnRig(t)
	a, b := rg.account(), rg.account()
	_, loc := rg.begin(a, "fake")
	ticket := loc.Query().Get("ticket")
	post := func(user uuid.UUID, provider string, body any) *httptest.ResponseRecorder {
		return rg.do(user, "POST", "/v1/me/connections/"+provider+"/confirm", body)
	}
	if w := post(b, "fake", model.ConnectConfirmPost{Ticket: ticket}); w.Code != 409 || codeOf(w) != model.CodeConnectTicketInvalid {
		t.Fatalf("another account's ticket: %d %s", w.Code, w.Body)
	}
	for name, body := range map[string]any{"missing": `{}`, "unknown field": `{"ticket":"` + ticket + `","x":1}`} {
		if w := post(a, "fake", body); w.Code != 400 {
			t.Errorf("%s: %d", name, w.Code)
		}
	}
	if w := post(a, "fake", model.ConnectConfirmPost{Ticket: "not-a-ticket"}); w.Code != 409 || codeOf(w) != model.CodeConnectTicketInvalid {
		t.Fatalf("malformed ticket: %d %s", w.Code, w.Body)
	}
	if w := post(a, "fake", model.ConnectConfirmPost{Ticket: strings.Repeat("a", 32)}); w.Code != 409 || codeOf(w) != model.CodeConnectTicketInvalid {
		t.Fatalf("wrong ticket: %d %s", w.Code, w.Body)
	}
	// Withdrawn consent: 403, and the ticket stays usable.
	if err := rg.st.RevokeConsent(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if w := post(a, "fake", model.ConnectConfirmPost{Ticket: ticket}); w.Code != 403 || codeOf(w) != model.CodeConsentRequired {
		t.Fatalf("no consent: %d %s", w.Code, w.Body)
	}
	if err := rg.st.GrantConsent(context.Background(), a, connPolicy, nil); err != nil {
		t.Fatal(err)
	}
	// Past the 10 minutes: 410, the grant is dropped and queued, and a repeat of a confirm that worked is 410 too.
	if _, err := rg.pool.Exec(context.Background(), `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if w := post(a, "fake", model.ConnectConfirmPost{Ticket: ticket}); w.Code != 410 || codeOf(w) != model.CodeConnectExpired {
		t.Fatalf("late confirm: %d %s", w.Code, w.Body)
	}
	if !rg.fake.Called("deregister", rg.fakePUID(loc, a)) && len(rg.fake.Calls()) == 0 {
		t.Log("the dropped grant is queued for the tick or was ended at once")
	}
	confirmed := rg.connectAccount(b, "fake")
	_, err := rg.pool.Exec(context.Background(), `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second' WHERE user_id=$1`, b)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Status != "connected" {
		t.Fatal(confirmed)
	}
}

func (rg *connRig) fakePUID(*url.URL, uuid.UUID) string { return "" }

// The flag gates confirm too: with it off, a pending grant is dropped like an expired one (queued; the provider is
// asked to end it) and the answer is 404 unknown_provider.
func TestConfirmWithTheFlagOffDropsTheGrant(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	rg.flag("garmin", u)
	_, loc := rg.begin(u, "garmin")
	ticket := loc.Query().Get("ticket")
	if loc.Query().Get("status") != "ok" {
		t.Fatal(loc)
	}
	rg.flag("garmin") // turned off
	w := rg.do(u, "POST", "/v1/me/connections/garmin/confirm", model.ConnectConfirmPost{Ticket: ticket})
	if w.Code != 404 || codeOf(w) != model.CodeUnknownProvider {
		t.Fatalf("confirm with the flag off: %d %s", w.Code, w.Body)
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_pending WHERE user_id=$1`, u); n != 0 {
		t.Fatal("the pending grant stays")
	}
	if len(rg.other.Calls()) == 0 || rg.other.Calls()[len(rg.other.Calls())-1].Op != "deregister" {
		t.Fatalf("the provider was not asked to end the grant: %+v", rg.other.Calls())
	}
	// With the flag back on, the ticket is gone.
	rg.flag("garmin", u)
	if w := rg.do(u, "POST", "/v1/me/connections/garmin/confirm", model.ConnectConfirmPost{Ticket: ticket}); w.Code != 409 {
		t.Fatalf("after the drop: %d", w.Code)
	}
}

// The flag off for a connected account: the connection stays listed, available does not list the provider, and the
// account can still disconnect.
func TestADarkProviderCanStillBeDisconnected(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	rg.flag("garmin", u)
	rg.connectAccount(u, "garmin")
	rg.flag("garmin")
	l := decode[model.ConnectionsResponse](t, rg.do(u, "GET", "/v1/me/connections", nil), 200)
	if len(l.Connections) != 1 || len(l.Available) != 1 || l.Available[0].Provider != "fake" {
		t.Fatalf("%+v", l)
	}
	if del := decode[model.ConnectionDeleted](t, rg.do(u, "DELETE", "/v1/me/connections/garmin", nil), 200); del.Deregistered == nil || !*del.Deregistered {
		t.Fatalf("%+v", del)
	}
	// A provider this server never heard of: still a clean answer, a well-formed id only.
	if del := decode[model.ConnectionDeleted](t, rg.do(u, "DELETE", "/v1/me/connections/suunto", nil), 200); del.Deregistered != nil {
		t.Fatalf("%+v", del)
	}
	if w := rg.do(u, "DELETE", "/v1/me/connections/Not-Valid", nil); w.Code != 404 || codeOf(w) != "" {
		t.Fatalf("a malformed id: %d %s", w.Code, w.Body)
	}
}

// The callback never answers anything but 302 and never takes a bearer; its redirect holds no secret.
func TestCallbackStatuses(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	rg.flag("garmin", u)
	var last uuid.UUID // the account of the latest start: a start is limited to 5 in a burst per account
	start := func(provider string) (state, code string) {
		last = rg.account()
		rg.flag("garmin", u, last)
		st := decode[model.ConnectStart](t, rg.do(last, "POST", "/v1/me/connections/"+provider+"/start", nil), 200)
		x, _ := url.Parse(st.AuthorizeURL)
		return x.Query().Get("state"), x.Query().Get("code")
	}
	get := func(path string) (string, http.Header) {
		w := rg.do(uuid.Nil, "GET", path, nil)
		if w.Code != 302 || w.Body.Len() != 0 {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body)
		}
		return w.Header().Get("Location"), w.Header()
	}
	state, code := start("fake")
	for name, c := range map[string]struct{ path, want string }{
		"unknown provider":          {"/v1/connect/nope/callback?state=" + state + "&code=" + code, "ryft://connect/nope?status=error"},
		"malformed provider":        {"/v1/connect/No-Pe/callback?state=" + state, "ryft://connect/unknown?status=error"},
		"the reserved provider":     {"/v1/connect/unknown/callback?state=" + state, "ryft://connect/unknown?status=error"},
		"no state":                  {"/v1/connect/fake/callback?code=" + code, "ryft://connect/fake?status=expired"},
		"state of a bad form":       {"/v1/connect/fake/callback?state=--%27%20OR%201=1&code=x", "ryft://connect/fake?status=expired"},
		"unknown state":             {"/v1/connect/fake/callback?state=" + strings.Repeat("a", 64) + "&code=x", "ryft://connect/fake?status=expired"},
		"state of another provider": {"/v1/connect/garmin/callback?state=" + state + "&code=" + code, "ryft://connect/garmin?status=expired"},
	} {
		loc, h := get(c.path)
		if loc != c.want || h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: %q, want %q (headers %v)", name, loc, c.want, h)
		}
	}
	// The user declined, the provider's error, a missing code, a wrong verifier.
	s2, _ := start("fake")
	if loc, _ := get("/v1/connect/fake/callback?state=" + s2 + "&error=access_denied"); loc != "ryft://connect/fake?status=denied" {
		t.Errorf("denied: %s", loc)
	}
	s3, _ := start("fake")
	if loc, _ := get("/v1/connect/fake/callback?state=" + s3 + "&error=server_error"); loc != "ryft://connect/fake?status=error" {
		t.Errorf("provider error: %s", loc)
	}
	s4, c4 := start("fake")
	if loc, _ := get("/v1/connect/fake/callback?state=" + s4); loc != "ryft://connect/fake?status=error" {
		t.Errorf("no code: %s", loc)
	}
	_ = c4
	s5, _ := start("fake")
	if loc, _ := get("/v1/connect/fake/callback?state=" + s5 + "&code=" + strings.Repeat("c", maxCodeLen+1)); loc != "ryft://connect/fake?status=error" {
		t.Errorf("long code: %s", loc)
	}
	s6, c6 := start("fake")
	other := connect.Challenge(connect.NewVerifier()) + "." + strings.SplitN(c6, ".", 2)[1]
	if loc, _ := get("/v1/connect/fake/callback?state=" + s6 + "&code=" + other); loc != "ryft://connect/fake?status=error" {
		t.Errorf("wrong PKCE pair: %s", loc)
	}
	// An old state is expired, not an error.
	s7, c7 := start("fake")
	rg.now = rg.now.Add(11 * time.Minute)
	if loc, _ := get("/v1/connect/fake/callback?state=" + s7 + "&code=" + c7); loc != "ryft://connect/fake?status=expired" {
		t.Errorf("old state: %s", loc)
	}
	rg.now = time.Now()
	// The provider failing is an error, not retried.
	rg.fake.ExchangeErr = errors.New("provider down")
	s8, c8 := start("fake")
	if loc, _ := get("/v1/connect/fake/callback?state=" + s8 + "&code=" + c8); loc != "ryft://connect/fake?status=error" {
		t.Errorf("exchange failure: %s", loc)
	}
	rg.fake.ExchangeErr = connect.ErrDenied
	s9, c9 := start("fake")
	if loc, _ := get("/v1/connect/fake/callback?state=" + s9 + "&code=" + c9); loc != "ryft://connect/fake?status=denied" {
		t.Errorf("denied at the exchange: %s", loc)
	}
	rg.fake.ExchangeErr = nil
	// A withdrawn consent between start and callback.
	s10, c10 := start("fake")
	if err := rg.st.RevokeConsent(context.Background(), last); err != nil {
		t.Fatal(err)
	}
	if loc, _ := get("/v1/connect/fake/callback?state=" + s10 + "&code=" + c10); loc != "ryft://connect/fake?status=error" {
		t.Errorf("no consent: %s", loc)
	}
	// A deleted account.
	d := rg.account()
	st := decode[model.ConnectStart](t, rg.do(d, "POST", "/v1/me/connections/fake/start", nil), 200)
	if err := rg.st.DeleteUser(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	x, _ := url.Parse(st.AuthorizeURL)
	// Its open states went with it, so the flow is unknown: expired (API.md: indistinguishable on purpose).
	if loc, _ := get(x.RequestURI()); loc != "ryft://connect/fake?status=expired" {
		t.Errorf("deleted account: %s", loc)
	}
}

// A grant that lacks a permission RYFT needs is handed back: denied, queued, and the provider is asked to end it.
func TestCallbackMissingPermission(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	prov := &thinProvider{Provider: rg.fake}
	reg, _ := connect.NewRegistry(prov)
	rg.conn.reg = reg
	_, loc := rg.begin(u, "fake")
	if loc.Query().Get("status") != "denied" || loc.Query().Get("ticket") != "" {
		t.Fatalf("redirect %s", loc)
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations WHERE reason='missing_permission'`); n != 1 {
		t.Fatalf("queued %d", n)
	}
	rg.conn.deregisterStep(context.Background())
	if !rg.fake.Called("deregister", rg.lastPUID()) {
		t.Fatal("the handed-back grant was not ended at the provider")
	}
}

func (rg *connRig) lastPUID() string {
	calls := rg.fake.Calls()
	return calls[0].ProviderUserID
}

// thinProvider grants one permission only.
type thinProvider struct{ *fake.Provider }

func (p *thinProvider) Exchange(ctx context.Context, code, verifier, redirect string) (connect.Grant, error) {
	g, err := p.Provider.Exchange(ctx, code, verifier, redirect)
	g.Permissions = []string{"read_sleep"}
	return g, err
}

func rowsOf(t *testing.T, pool *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Review B2: no deregistration ever ends a live grant. One case per reason queues a row for a provider account that
// another account holds live; deregisterOne calls nothing, deletes the row and its tokens, and leaves the holder's
// connection and data as they were. A last case queues a disconnect, relinks the provider user to another account
// and asserts the provider was never asked.
func TestDeregisterSkipsALiveGrant(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	holder := rg.account()
	conn := rg.connectAccount(holder, "fake")
	puid := *conn.ProviderUserID
	night := time.Now().UTC().Add(-10 * time.Hour).Truncate(time.Second)
	if _, err := rg.pool.Exec(ctx, `INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source, origin, vendor, record_id, record_state)
		VALUES ($1,$2,$3,'deep','ryft-direct-fake','direct','fake','sleep:n','final')`, holder, night, night.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	attacker := rg.account()
	for _, reason := range []string{"disconnect", "account_deleted", "confirm_expired", "cap", "missing_permission", "replaced"} {
		before := testutil.ToFloat64(metrics.ConnectDeregistrations.WithLabelValues("fake", reason, depSkipped))
		a, _ := rg.conn.seal("attacker-access", adPendingAccess("fake", attacker))
		r, _ := rg.conn.seal("attacker-refresh", adPendingRefresh("fake", attacker))
		id, err := rg.st.QueueDeregistration(ctx, attacker, "fake", puid, reason, a, r)
		if err != nil {
			t.Fatal(err)
		}
		d, err := rg.st.Deregistration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := rg.conn.deregisterOne(ctx, d); got != depSkipped {
			t.Fatalf("%s: outcome %s, want %s", reason, got, depSkipped)
		}
		if _, err := rg.st.Deregistration(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: the queue row and its tokens must be deleted: %v", reason, err)
		}
		if got := testutil.ToFloat64(metrics.ConnectDeregistrations.WithLabelValues("fake", reason, depSkipped)) - before; got != 1 {
			t.Errorf("%s: skipped_live counted %v", reason, got)
		}
	}
	if calls := rg.fake.Calls(); len(calls) != 1 || calls[0].Op != "exchange" {
		t.Fatalf("the provider was called: %+v", calls)
	}
	rows, _ := rg.st.Connections(ctx, holder)
	if len(rows) != 1 || rows[0].Status != model.ConnectionConnected || *rows[0].ProviderUserID != puid {
		t.Fatalf("the holder's connection changed: %+v", rows)
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM sleep_segments WHERE user_id=$1 AND origin='direct'`, holder); n != 1 {
		t.Fatal("the holder's data changed")
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_tokens WHERE user_id=$1`, holder); n != 1 {
		t.Fatal("the holder's tokens changed")
	}

	// A link that became live again between queueing and the attempt: queue a disconnect of one account's
	// connection, then relink that provider user to another account before the attempt.
	first, second := rg.account(), rg.account()
	c1 := rg.connectAccount(first, "fake")
	out, err := rg.st.Disconnect(ctx, first, "fake", nil)
	if err != nil || len(out.Queued) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := rg.pool.Exec(ctx, `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1,'fake',$2,'connected','{}',$3)`, second, *c1.ProviderUserID, connPolicy); err != nil {
		t.Fatal(err)
	}
	before := len(rg.fake.Calls())
	rg.conn.deregisterStep(ctx)
	if len(rg.fake.Calls()) != before || rg.fake.Called("deregister", *c1.ProviderUserID) {
		t.Fatal("a grant relinked to another account was deregistered")
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations`); n != 0 {
		t.Fatalf("%d queue rows left", n)
	}
	if rows, _ := rg.st.Connections(ctx, second); len(rows) != 1 || rows[0].Status != model.ConnectionConnected {
		t.Fatalf("the relinked account's connection: %+v", rows)
	}
}

// P2-6: a grant another account has waiting for its confirm (no connection yet) is live for the B2 rule too: the
// attempt calls nothing, deletes the queue row, and the waiting account's confirm still works. Once that pending
// grant is past its window nothing protects the provider user and the provider is asked.
func TestDeregisterSkipsAnotherAccountsPendingGrant(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	waiting, attacker := rg.account(), rg.account()
	authorize, loc := rg.begin(waiting, "fake")
	au, _ := url.Parse(authorize)
	puid := fake.UserID(au.Query().Get("state"))
	queue := func(reason string) store.Dereg {
		a, _ := rg.conn.seal("attacker-access", adPendingAccess("fake", attacker))
		r, _ := rg.conn.seal("attacker-refresh", adPendingRefresh("fake", attacker))
		id, err := rg.st.QueueDeregistration(ctx, attacker, "fake", puid, reason, a, r)
		if err != nil {
			t.Fatal(err)
		}
		d, err := rg.st.Deregistration(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := queue("confirm_expired")
	if got := rg.conn.deregisterOne(ctx, d); got != depSkipped {
		t.Fatalf("outcome %s, want %s", got, depSkipped)
	}
	if rg.fake.Called("deregister", puid) {
		t.Fatal("a grant another account is about to confirm was deregistered")
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations WHERE id=$1`, d.ID); n != 0 {
		t.Fatal("the skipped queue row stays")
	}
	conn := decode[model.Connection](t, rg.do(waiting, "POST", "/v1/me/connections/fake/confirm", model.ConnectConfirmPost{Ticket: loc.Query().Get("ticket")}), 200)
	if conn.Status != model.ConnectionConnected {
		t.Fatalf("the waiting account's confirm: %+v", conn)
	}

	// A pending grant past its window protects nothing.
	authorize2, _ := rg.begin(rg.account(), "fake")
	au2, _ := url.Parse(authorize2)
	puid2 := fake.UserID(au2.Query().Get("state"))
	if _, err := rg.pool.Exec(ctx, `UPDATE connection_pending SET confirm_expires_at = now() - interval '1 second' WHERE provider_user_id=$1`, puid2); err != nil {
		t.Fatal(err)
	}
	puid = puid2
	d = queue("confirm_expired")
	if got := rg.conn.deregisterOne(ctx, d); got != depOK {
		t.Fatalf("outcome %s, want %s", got, depOK)
	}
	if !rg.fake.Called("deregister", puid2) {
		t.Fatal("an expired pending grant kept the provider from being asked")
	}
}

// P2-13 (API.md wins): a grant not called because another RYFT account holds that provider account live is not
// ended: attempt says false, so `deregistered` and `connections_deregistered` are false. An ended or already gone
// grant is true.
func TestSkippedLiveGrantIsNotReportedAsEnded(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	first, second := rg.account(), rg.account()
	c1 := rg.connectAccount(first, "fake")
	out, err := rg.st.Disconnect(ctx, first, "fake", nil)
	if err != nil || len(out.Queued) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := rg.pool.Exec(ctx, `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1,'fake',$2,'connected','{}',$3)`, second, *c1.ProviderUserID, connPolicy); err != nil {
		t.Fatal(err)
	}
	if rg.conn.attempt(ctx, out.Queued) {
		t.Fatal("a grant another account holds live was reported as ended")
	}
	if rg.fake.Called("deregister", *c1.ProviderUserID) {
		t.Fatal("the provider was called")
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations`); n != 0 {
		t.Fatal("the skipped queue row stays")
	}
	// The classification the answers use: ended or already gone is true, nothing else.
	if !ended(depOK) || !ended(depGone) || ended(depSkipped) || ended(depRetry) || ended(depGivenUp) {
		t.Fatal("ended() classifies the outcomes wrongly")
	}
}

// failingPending is the store whose pending write fails after the exchange.
type failingPending struct {
	connectStore
	err error
}

func (f failingPending) PutPendingCapped(context.Context, store.Pending, int) error { return f.err }

// P2-15: a grant the exchange made but the pending write lost (no account, a database error) is queued, so the
// provider is asked to end it.
func TestCallbackQueuesAGrantItCouldNotStore(t *testing.T) {
	for name, perr := range map[string]error{"no account": store.ErrConnectNoAccount, "database": errors.New("db down")} {
		t.Run(name, func(t *testing.T) {
			rg := newConnRig(t)
			rg.conn.st = failingPending{connectStore: rg.st, err: perr}
			u := rg.account()
			_, loc := rg.begin(u, "fake")
			if loc.Query().Get("status") != "error" || loc.Query().Get("ticket") != "" {
				t.Fatalf("redirect %s", loc)
			}
			if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations WHERE user_id=$1 AND reason='confirm_expired' AND access_enc IS NOT NULL`, u); n != 1 {
				t.Fatalf("%d grants queued", n)
			}
			rg.conn.st = rg.st
			rg.conn.deregisterStep(context.Background())
			if len(rg.fake.Calls()) < 2 || rg.fake.Calls()[1].Op != "deregister" {
				t.Fatalf("the provider was not asked to end the grant: %+v", rg.fake.Calls())
			}
		})
	}
}

// disconnectsDuringRefresh is the fake whose refresh runs while the account disconnects.
type disconnectsDuringRefresh struct {
	*fake.Provider
	run func()
}

func (d *disconnectsDuringRefresh) Refresh(ctx context.Context, refresh string) (connect.Grant, error) {
	g, err := d.Provider.Refresh(ctx, refresh)
	d.run()
	return g, err
}

// P2-19: a refresh that finds its token row gone (a disconnect ran while the provider answered) queues the new
// tokens, so the grant the provider holds now is ended.
func TestRefreshAfterADisconnectQueuesTheNewTokens(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	conn := rg.connectAccount(u, "fake")
	rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	reg, _ := connect.NewRegistry(&disconnectsDuringRefresh{Provider: rg.fake, run: func() {
		if _, err := rg.st.Disconnect(ctx, u, "fake", nil); err != nil {
			t.Error(err)
		}
	}})
	rg.conn.reg = reg
	due, err := rg.st.ClaimRefreshes(ctx, 10)
	if err != nil || len(due) != 1 || due[0].ProviderUserID != *conn.ProviderUserID {
		t.Fatalf("%+v %v", due, err)
	}
	// The disconnect queued the old tokens (one row); the refresh queues the new ones (a second).
	rg.conn.refreshOne(ctx, due[0])
	rows, err := rg.st.UserDeregistrations(ctx, u)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%d queue rows, want 2: %+v %v", len(rows), rows, err)
	}
	last := rows[1]
	a, err := rg.conn.open(last.AccessEnc, adAccess("fake", u))
	if err != nil || !strings.HasPrefix(a, "fake-access.") || last.ProviderUserID != *conn.ProviderUserID {
		t.Fatalf("the queued tokens: %q %v %+v", a, err, last)
	}
	if _, err := rg.conn.open(last.RefreshEnc, adRefresh("fake", u)); err != nil {
		t.Fatalf("the refresh token does not open: %v", err)
	}
}

// The queue and the tick against a provider whose network fails: the row stays with a backoff, then succeeds.
func TestDeregistrationRetriesAndGivesUp(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	conn := rg.connectAccount(u, "fake")
	rg.fake.DeregisterErr = errors.New("network down")
	del := decode[model.ConnectionDeleted](t, rg.do(u, "DELETE", "/v1/me/connections/fake", nil), 200)
	if del.Deregistered == nil || *del.Deregistered || !del.DataDeleted {
		t.Fatalf("an unreachable provider: %+v", del)
	}
	var attempts int
	var next time.Time
	var lastErr string
	if err := rg.pool.QueryRow(ctx, `SELECT attempts, next_attempt_at, last_error FROM connection_deregistrations`).Scan(&attempts, &next, &lastErr); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || lastErr != "unreachable" || next.Before(time.Now().Add(10*time.Minute)) {
		t.Fatalf("attempts %d next %v err %q", attempts, next, lastErr)
	}
	// The tick finds nothing due until the backoff passes, then retries; the network back, the row goes.
	rg.conn.deregisterStep(ctx)
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations`); n != 1 {
		t.Fatal("the row left early")
	}
	if _, err := rg.pool.Exec(ctx, `UPDATE connection_deregistrations SET next_attempt_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	rg.conn.deregisterStep(ctx) // still down: attempts 2
	rg.pool.QueryRow(ctx, `SELECT attempts FROM connection_deregistrations`).Scan(&attempts)
	if attempts != 2 {
		t.Fatalf("attempts %d", attempts)
	}
	rg.fake.DeregisterErr = nil
	rg.pool.Exec(ctx, `UPDATE connection_deregistrations SET next_attempt_at = now() - interval '1 second'`)
	rg.conn.deregisterStep(ctx)
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations`); n != 0 || !rg.fake.Called("deregister", *conn.ProviderUserID) {
		t.Fatalf("rows %d", n)
	}
	// 30 days on, it is given up and counted.
	rg.fake.DeregisterErr = errors.New("gone for good")
	u2 := rg.account()
	rg.connectAccount(u2, "fake")
	rg.do(u2, "DELETE", "/v1/me/connections/fake", nil)
	rg.pool.Exec(ctx, `UPDATE connection_deregistrations SET created_at = now() - interval '31 days'`)
	before := testutil.ToFloat64(metrics.ConnectDeregistrations.WithLabelValues("fake", "disconnect", depGivenUp))
	rg.conn.deregisterStep(ctx)
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations`); n != 0 || testutil.ToFloat64(metrics.ConnectDeregistrations.WithLabelValues("fake", "disconnect", depGivenUp))-before != 1 {
		t.Fatalf("rows %d", n)
	}
}

// An expired access token: the first call is refused, the grant is refreshed in memory and the call repeated.
func TestDeregisterRefreshesAnExpiredAccessToken(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	conn := rg.connectAccount(u, "fake")
	exp := &expiring{Provider: rg.fake}
	reg, _ := connect.NewRegistry(exp)
	rg.conn.reg = reg
	rows, _ := rg.st.Connections(ctx, u)
	_ = rows
	out, err := rg.st.Disconnect(ctx, u, "fake", nil)
	if err != nil || len(out.Queued) != 1 {
		t.Fatal(err)
	}
	if !rg.conn.attempt(ctx, out.Queued) || exp.deregs != 2 || exp.refreshes != 1 {
		t.Fatalf("deregs %d refreshes %d", exp.deregs, exp.refreshes)
	}
	_ = conn
}

type expiring struct {
	*fake.Provider
	deregs, refreshes int
}

func (e *expiring) Deregister(ctx context.Context, g connect.Grant) error {
	e.deregs++
	if e.deregs == 1 {
		return connect.ErrInvalidGrant
	}
	return e.Provider.Deregister(ctx, g)
}

func (e *expiring) Refresh(ctx context.Context, r string) (connect.Grant, error) {
	e.refreshes++
	return e.Provider.Refresh(ctx, r)
}

// DELETE /v1/me ends the account's connections: the field is null, true or false (the retry runs in the background).
func TestDeleteMeConnectionsDeregistered(t *testing.T) {
	rg := newConnRig(t)
	none := rg.account()
	got := decode[AccountDeleted](t, rg.do(none, "DELETE", "/v1/me", nil), 200)
	if got.ConnectionsDeregistered != nil {
		t.Fatalf("no connection: %v", *got.ConnectionsDeregistered)
	}
	ok := rg.account()
	rg.connectAccount(ok, "fake")
	got = decode[AccountDeleted](t, rg.do(ok, "DELETE", "/v1/me", nil), 200)
	if got.ConnectionsDeregistered == nil || !*got.ConnectionsDeregistered {
		t.Fatalf("reachable provider: %v", got.ConnectionsDeregistered)
	}
	down := rg.account()
	rg.connectAccount(down, "fake")
	rg.fake.DeregisterErr = errors.New("network down")
	raw := rg.do(down, "DELETE", "/v1/me", nil)
	got = decode[AccountDeleted](t, raw, 200)
	if got.ConnectionsDeregistered == nil || *got.ConnectionsDeregistered {
		t.Fatalf("unreachable provider: %v", got.ConnectionsDeregistered)
	}
	if !strings.Contains(raw.Body.String(), `"connections_deregistered":false`) {
		t.Fatalf("body %s", raw.Body)
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations WHERE user_id=$1 AND reason='account_deleted'`, down); n != 1 {
		t.Fatal("the tokens are not queued for the retry")
	}
	// The tick retries it after the provider is back.
	rg.fake.DeregisterErr = nil
	rg.pool.Exec(context.Background(), `UPDATE connection_deregistrations SET next_attempt_at = now() - interval '1 second'`)
	rg.conn.deregisterStep(context.Background())
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations`); n != 0 {
		t.Fatal("the retry did not run")
	}
}

// DELETE /v1/me/connections/{provider} needs neither the app token nor beta access: a connection can always be ended.
func TestDisconnectIsExemptFromAppAttestAndTheInviteGate(t *testing.T) {
	rg := newConnRig(t, func(b *config.Beta, a *config.AppAttest) { b.Gate = config.ModeOn; a.Mode = config.ModeOn })
	u := rg.account()
	// Everything else on the Connections routes is gated: no app token, 403 app_attest_required.
	if w := rg.do(u, "GET", "/v1/me/connections", nil); w.Code != 403 || codeOf(w) != model.CodeAppAttestRequired {
		t.Fatalf("list without a token: %d %s", w.Code, w.Body)
	}
	if w := rg.do(u, "POST", "/v1/me/connections/fake/start", nil); w.Code != 403 || codeOf(w) != model.CodeAppAttestRequired {
		t.Fatalf("start without a token: %d %s", w.Code, w.Body)
	}
	// The disconnect passes with neither (the account has no beta access either).
	del := decode[model.ConnectionDeleted](t, rg.do(u, "DELETE", "/v1/me/connections/fake", nil), 200)
	if del.Status != "disconnected" || !del.DataDeleted {
		t.Fatalf("%+v", del)
	}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"DELETE", "/v1/me/connections/fake", true}, {"DELETE", "/v1/me/connections/garmin", true},
		{"DELETE", "/v1/me/connections/", false}, {"DELETE", "/v1/me/connections/fake/start", false},
		{"GET", "/v1/me/connections/fake", false}, {"POST", "/v1/me/connections/fake/confirm", false}, {"DELETE", "/v1/me", false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := attestExempt(r); got != c.want || isDisconnect(r) != c.want {
			t.Errorf("%s %s: exempt %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestRefreshStep(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	ok, bad, flaky := rg.account(), rg.account(), rg.account()
	rg.connectAccount(ok, "fake")
	rg.connectAccount(bad, "fake")
	rg.connectAccount(flaky, "fake")
	due := func() {
		rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	}
	var oldAccess []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, ok).Scan(&oldAccess)
	due()
	counter := func(o string) float64 { return testutil.ToFloat64(metrics.ConnectRefreshes.WithLabelValues("fake", o)) }
	b0, n0, e0 := counter("ok"), counter("needs_reauth"), counter("error")
	// Refresh succeeds for all but a failing network.
	rg.fake.RefreshErr = errors.New("network down")
	if err := rg.conn.refreshStep(ctx); err != nil {
		t.Fatal(err)
	}
	if counter("error")-e0 != 3 {
		t.Fatalf("errors counted %v", counter("error")-e0)
	}
	var attempts int
	var dueAt time.Time
	rg.pool.QueryRow(ctx, `SELECT refresh_attempts, refresh_due_at FROM connection_tokens WHERE user_id=$1`, flaky).Scan(&attempts, &dueAt)
	if attempts != 1 || dueAt.Before(time.Now().Add(14*time.Minute)) {
		t.Fatalf("backoff: attempts %d due %v", attempts, dueAt)
	}
	// A transient failure keeps the link connected.
	if rows, _ := rg.st.Connections(ctx, flaky); rows[0].Status != model.ConnectionConnected {
		t.Fatal(rows[0].Status)
	}
	// The network is back: the tokens are re-sealed and the due time moves.
	rg.fake.RefreshErr = nil
	due()
	rg.conn.refreshStep(ctx)
	if counter("ok")-b0 != 3 {
		t.Fatalf("ok counted %v", counter("ok")-b0)
	}
	var newAccess []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, ok).Scan(&newAccess)
	if bytes.Equal(oldAccess, newAccess) {
		t.Fatal("the access token was not replaced")
	}
	a, err := rg.conn.open(newAccess, adAccess("fake", ok))
	if err != nil || !strings.HasPrefix(a, "fake-access.") {
		t.Fatalf("%q %v", a, err)
	}
	// The provider refuses the grant: needs_reauth, tokens kept, no more refreshes.
	rg.fake.RefreshErr = connect.ErrInvalidGrant
	due()
	rg.conn.refreshStep(ctx)
	if counter("needs_reauth")-n0 != 3 {
		t.Fatalf("needs_reauth counted %v", counter("needs_reauth")-n0)
	}
	rows, _ := rg.st.Connections(ctx, bad)
	if rows[0].Status != model.ConnectionNeedsReauth {
		t.Fatal(rows[0].Status)
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_tokens WHERE access_enc IS NOT NULL`); n != 3 {
		t.Fatal("tokens of a needs_reauth link must be kept")
	}
	rg.fake.RefreshErr = nil
	due()
	before := len(rg.fake.Calls())
	rg.conn.refreshStep(ctx)
	if len(rg.fake.Calls()) != before {
		t.Fatal("a needs_reauth link was refreshed")
	}
	// A needs_reauth link still counts as live (B2) and a reconnect replaces its grant.
	if l := decode[model.ConnectionsResponse](t, rg.do(bad, "GET", "/v1/me/connections", nil), 200); l.Connections[0].Status != "needs_reauth" {
		t.Fatal(l)
	}
}

func TestPurgeAndGaugesStep(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	rg.conn.cfg.Caps["fake"] = 3
	a, b, c := rg.account(), rg.account(), rg.account()
	for _, u := range []uuid.UUID{a, b, c} {
		rg.connectAccount(u, "fake")
	}
	rg.pool.Exec(ctx, `UPDATE connections SET connected_at = now() - interval '2 days' WHERE user_id=$1`, a)
	// 06:05 UTC: the day's notices.
	rg.now = time.Date(2026, 11, 24, 6, 5, 0, 0, time.UTC)
	rg.logs.buf.Reset()
	if err := rg.conn.gaugesStep(ctx); err != nil {
		t.Fatal(err)
	}
	out := rg.logs.String()
	for _, msg := range []string{"connect capacity", "connect no data"} {
		if strings.Count(out, `"msg":"`+msg+`"`) != 1 {
			t.Errorf("%q written %d times at 06:05:\n%s", msg, strings.Count(out, `"msg":"`+msg+`"`), out)
		}
	}
	if strings.Contains(out, "connect deregistration stuck") {
		t.Error("a stuck notice without a stuck row")
	}
	if got := testutil.ToFloat64(metrics.ConnectCapacity.WithLabelValues("fake")); got != 1 {
		t.Errorf("capacity ratio %v", got)
	}
	if got := testutil.ToFloat64(metrics.ConnectStale.WithLabelValues("fake")); got != 1 {
		t.Errorf("stale %v", got)
	}
	if got := testutil.ToFloat64(metrics.ConnectConnections.WithLabelValues("fake", "connected")); got != 3 {
		t.Errorf("connected %v", got)
	}
	// 06:20 and 12:00: none.
	for _, at := range []time.Time{time.Date(2026, 11, 24, 6, 15, 0, 0, time.UTC), time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)} {
		rg.now = at
		rg.logs.buf.Reset()
		rg.conn.gaugesStep(ctx)
		if strings.Contains(rg.logs.String(), "connect capacity") || strings.Contains(rg.logs.String(), "connect no data") {
			t.Errorf("a notice at %s", at)
		}
	}
	// A deregistration stuck for over a day.
	rg.do(a, "DELETE", "/v1/me/connections/fake", nil)
	rg.fake.DeregisterErr = errors.New("down")
	rg.do(b, "DELETE", "/v1/me/connections/fake", nil)
	rg.pool.Exec(ctx, `UPDATE connection_deregistrations SET created_at = now() - interval '25 hours'`)
	rg.now = time.Date(2026, 11, 24, 6, 1, 0, 0, time.UTC)
	rg.logs.buf.Reset()
	rg.conn.gaugesStep(ctx)
	if !strings.Contains(rg.logs.String(), `"msg":"connect deregistration stuck"`) {
		t.Errorf("no stuck notice:\n%s", rg.logs)
	}
	// The purge: expired pending grants are queued, the tokens of a revoked link and old revoked entries go.
	rg.pool.Exec(ctx, `UPDATE connections SET status='revoked', status_reason='provider_revoked', provider_user_id=NULL, permissions='{}', status_at = now() - interval '91 days' WHERE user_id=$1`, c)
	if err := rg.conn.purgeStep(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _ := rg.st.Connections(ctx, c); len(rows) != 0 {
		t.Fatalf("revoked entry kept past 90 days: %+v", rows)
	}
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_tokens WHERE user_id=$1`, c); n != 0 {
		t.Fatal("tokens of a revoked link kept")
	}
}

// hangingProvider is the fake whose network hangs: Refresh and Deregister return only when their context ends.
type hangingProvider struct {
	*fake.Provider
	calls atomic.Int32
}

func (h *hangingProvider) Refresh(ctx context.Context, _ string) (connect.Grant, error) {
	h.calls.Add(1)
	<-ctx.Done()
	return connect.Grant{}, ctx.Err()
}

func (h *hangingProvider) Deregister(ctx context.Context, _ connect.Grant) error {
	h.calls.Add(1)
	<-ctx.Done()
	return ctx.Err()
}

// P2-4: a provider whose network hangs ends its own step at its own deadline; the database steps, the daily notices
// among them, run before the provider's and are not held up; and the steps' order and names are the tick's.
func TestTickStepsHaveDeadlinesAndNoticesDoNotWaitForProviders(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	rg.conn.cfg.Caps["fake"] = 3
	a, b := rg.account(), rg.account()
	rg.connectAccount(a, "fake")
	rg.connectAccount(b, "fake")
	// A queued deregistration and two refreshes that are due, against a provider that hangs.
	del := rg.account()
	rg.connectAccount(del, "fake")
	rg.fake.DeregisterErr = errors.New("down")
	rg.do(del, "DELETE", "/v1/me/connections/fake", nil)
	rg.fake.DeregisterErr = nil
	rg.conn.cfg.Caps["fake"] = 2 // two live links of two: the capacity notice is due
	rg.pool.Exec(ctx, `UPDATE connection_deregistrations SET next_attempt_at = now() - interval '1 minute'`)
	rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	hang := &hangingProvider{Provider: rg.fake}
	reg, _ := connect.NewRegistry(hang)
	rg.conn.reg = reg
	rg.conn.dbBudget, rg.conn.providerBudget = 2*time.Second, 800*time.Millisecond
	rg.conn.callBudget, rg.conn.persist = 200*time.Millisecond, 100*time.Millisecond // a refresh needs 300 ms of its step
	rg.now = time.Date(2026, 11, 24, 6, 5, 0, 0, time.UTC)                           // the day's notices
	rg.logs.buf.Reset()

	steps := rg.conn.ticks()
	var names []string
	for _, s := range steps {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "connect purge,connect gauges,connect refresh,connect deregister" {
		t.Fatalf("steps %v: the database steps come before the provider's", names)
	}
	tick, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	took := map[string]time.Duration{}
	errs := map[string]error{}
	for _, s := range steps {
		start := time.Now()
		errs[s.Name] = s.Run(tick)
		took[s.Name] = time.Since(start)
	}
	if errs["connect gauges"] != nil || !strings.Contains(rg.logs.String(), `"msg":"connect capacity"`) {
		t.Fatalf("the daily notice was not written: %v\n%s", errs["connect gauges"], rg.logs)
	}
	for _, name := range []string{"connect refresh", "connect deregister"} {
		if took[name] > 3*time.Second {
			t.Errorf("%s took %v: a hanging provider held the step past its deadline", name, took[name])
		}
	}
	if err := errs["connect refresh"]; err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("refresh: %v", err)
	}
	if hang.calls.Load() == 0 {
		t.Fatal("the provider was never called")
	}
	if tick.Err() != nil {
		t.Fatal("the tick's own budget was spent by one step")
	}
	// A hung refresh loses no state: the rows are claimed ahead, not failed for good.
	if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connections WHERE status='connected'`); n != 2 {
		t.Fatalf("%d connected links after a hung refresh", n)
	}
}

// cancelsAfterRefresh is the fake that ends the step's context right after the provider answered a refresh: the
// provider has rotated the tokens by then, and the new ones must still be saved.
type cancelsAfterRefresh struct {
	*fake.Provider
	cancel context.CancelFunc
}

func (c *cancelsAfterRefresh) Refresh(ctx context.Context, refresh string) (connect.Grant, error) {
	g, err := c.Provider.Refresh(ctx, refresh)
	c.cancel()
	return g, err
}

// P2-4: a refreshed grant is saved on a context detached from the step's deadline.
func TestRefreshedGrantIsSavedAfterTheStepDeadline(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	rg.connectAccount(u, "fake")
	var before []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&before)
	rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	step, cancel := context.WithCancel(ctx)
	defer cancel()
	reg, _ := connect.NewRegistry(&cancelsAfterRefresh{Provider: rg.fake, cancel: cancel})
	rg.conn.reg = reg
	due, err := rg.st.ClaimRefreshes(ctx, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("%+v %v", due, err)
	}
	rg.conn.refreshOne(step, due[0])
	if step.Err() == nil {
		t.Fatal("the step's context did not end")
	}
	var after []byte
	var attempts int
	rg.pool.QueryRow(ctx, `SELECT access_enc, refresh_attempts FROM connection_tokens WHERE user_id=$1`, u).Scan(&after, &attempts)
	if bytes.Equal(before, after) {
		t.Fatal("the refreshed token the provider already rotated was dropped with the step's deadline")
	}
	if a, err := rg.conn.open(after, adAccess("fake", u)); err != nil || !strings.HasPrefix(a, "fake-access.") {
		t.Fatalf("saved tokens do not open: %q %v", a, err)
	}
}

// slowRotation rotates the token (the fake's Refresh), then takes its time: the provider's answer arrives after the
// step's deadline. It gives up only when its own context ends.
type slowRotation struct {
	*fake.Provider
	wait         time.Duration
	invalidFirst atomic.Bool // Deregister answers invalid_grant once (the access token has expired)
	deregistered atomic.Int32
}

func (s *slowRotation) Refresh(ctx context.Context, refresh string) (connect.Grant, error) {
	g, err := s.Provider.Refresh(ctx, refresh)
	select {
	case <-ctx.Done():
		return connect.Grant{}, ctx.Err()
	case <-time.After(s.wait):
	}
	return g, err
}

func (s *slowRotation) Deregister(ctx context.Context, g connect.Grant) error {
	if s.invalidFirst.CompareAndSwap(true, false) {
		return connect.ErrInvalidGrant
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.deregistered.Add(1)
	return s.Provider.Deregister(ctx, g)
}

// P2-24: the refresh call runs on a context of its own: a provider that rotates the token and answers after the
// step's deadline still has its new token saved.
func TestRefreshCallOutlivesTheStepDeadline(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	rg.connectAccount(u, "fake")
	var before []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&before)
	rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	reg, _ := connect.NewRegistry(&slowRotation{Provider: rg.fake, wait: 300 * time.Millisecond})
	rg.conn.reg = reg
	due, _ := rg.st.ClaimRefreshes(ctx, 10)
	step, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	rg.conn.refreshOne(step, due[0])
	var after []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&after)
	if bytes.Equal(before, after) {
		t.Fatal("a refresh cut by the step's deadline lost the token the provider rotated")
	}
	if rows, _ := rg.st.Connections(ctx, u); rows[0].Status != model.ConnectionConnected {
		t.Fatalf("status %s: a cut refresh must not end the link", rows[0].Status)
	}
}

// P2-24: a step with less than a call and its save left starts no refresh (the due rows come again), and an
// unlimited context does.
func TestRefreshStepStartsNoRefreshItCannotFinish(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	rg.connectAccount(rg.account(), "fake")
	rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	short, cancel := context.WithTimeout(ctx, time.Second) // far under providerTimeout + persistBudget
	defer cancel()
	if err := rg.conn.refreshStep(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if len(rg.fake.Calls()) != 1 { // the connect's exchange only
		t.Fatalf("the provider was called: %+v", rg.fake.Calls())
	}
	if !rg.conn.roomForRefresh(ctx) {
		t.Fatal("a context without a deadline has room")
	}
}

// P2-24: deregisterOne's in-memory refresh runs past the step's cancel where the tick has the time, so the grant is
// ended, not read as gone; the inline attempt (5 s) keeps its bound and still ends the grant when the calls are quick.
func TestDeregisterRefreshOutlivesTheCancelAndInlineLeavesTheRow(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	conn := rg.connectAccount(u, "fake")
	prov := &slowRotation{Provider: rg.fake, wait: 200 * time.Millisecond}
	reg, _ := connect.NewRegistry(prov)
	rg.conn.reg = reg
	out, err := rg.st.Disconnect(ctx, u, "fake", nil)
	if err != nil || len(out.Queued) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	d, _ := rg.st.Deregistration(ctx, out.Queued[0])
	// Inline (5 s): bounded, as before; a context that ends during the refresh is a retry, never "gone".
	prov.invalidFirst.Store(true)
	cut, ccancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer ccancel()
	if got := rg.conn.deregisterOne(cut, d); got != depRetry {
		t.Fatalf("inline cut: %s, want %s", got, depRetry)
	}
	if _, err := rg.st.Deregistration(ctx, d.ID); err != nil {
		t.Fatalf("the row is gone: %v", err)
	}
	// The tick: time enough, then the step's context ends during the refresh: the grant is still ended.
	prov.invalidFirst.Store(true)
	tick, tcancel := context.WithTimeout(ctx, time.Minute)
	go func() { time.Sleep(50 * time.Millisecond); tcancel() }()
	if got := rg.conn.deregisterOne(tick, d); got != depOK {
		t.Fatalf("tick: %s, want %s", got, depOK)
	}
	if !rg.fake.Called("deregister", *conn.ProviderUserID) {
		t.Fatal("the provider was never asked to end the grant")
	}
}

// P2-25: the save is a compare-and-swap on the claimed refresh token: a replace between the claim and the save keeps
// the new grant's tokens, and the old grant's rotated ones are queued for deregistration.
func TestRefreshAfterAReplaceDoesNotOverwriteTheNewGrant(t *testing.T) {
	rg := newConnRig(t)
	ctx := context.Background()
	u := rg.account()
	first := rg.connectAccount(u, "fake")
	rg.pool.Exec(ctx, `UPDATE connection_tokens SET refresh_due_at = now() - interval '1 minute'`)
	due, err := rg.st.ClaimRefreshes(ctx, 10)
	if err != nil || len(due) != 1 || due[0].ProviderUserID != *first.ProviderUserID {
		t.Fatalf("%+v %v", due, err)
	}
	second := rg.connectAccount(u, "fake") // a replace: another provider account, other tokens
	if *second.ProviderUserID == *first.ProviderUserID {
		t.Fatal("the replace kept the provider account")
	}
	var live []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&live)
	rg.conn.refreshOne(ctx, due[0])
	var after []byte
	rg.pool.QueryRow(ctx, `SELECT access_enc FROM connection_tokens WHERE user_id=$1`, u).Scan(&after)
	if !bytes.Equal(live, after) {
		t.Fatal("the old grant's refreshed tokens overwrote the new connection's")
	}
	if a, err := rg.conn.open(after, adAccess("fake", u)); err != nil || !strings.Contains(a, *second.ProviderUserID) {
		t.Fatalf("the live tokens are not the new grant's: %q %v", a, err)
	}
	// The replace's own queue row was attempted at once and is gone; the refresh queued the rotated tokens for the old
	// provider account.
	rows, err := rg.st.UserDeregistrations(ctx, u)
	if err != nil || len(rows) != 1 || rows[0].ProviderUserID != *first.ProviderUserID {
		t.Fatalf("queue rows for the replaced grant: %+v %v", rows, err)
	}
	if a, err := rg.conn.open(rows[0].AccessEnc, adAccess("fake", u)); err != nil || !strings.Contains(a, *first.ProviderUserID) {
		t.Fatalf("the queued tokens are not the old grant's: %q %v", a, err)
	}
}

// P2-29: a grant handed back is queued even when the request that found it is gone.
func TestHandBackSurvivesACancelledRequest(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for _, reason := range []string{"cap", "missing_permission", "confirm_expired"} {
		rg.conn.handBackAs(gone, u, "fake", "p-"+reason, reason, []byte("a"), []byte("r"))
		if n := rowsOf(t, rg.pool, `SELECT count(*) FROM connection_deregistrations WHERE user_id=$1 AND reason=$2`, u, reason); n != 1 {
			t.Fatalf("%s: %d rows queued", reason, n)
		}
	}
}

// The admin merge refuses while the source holds a connection, force or not; the dry run reports the count.
func TestAdminMergeRefusesWhileTheSourceHoldsAConnection(t *testing.T) {
	rg := newConnRig(t)
	from, to := rg.account(), rg.account()
	rg.connectAccount(from, "fake")
	key := "test-admin-key-0123456789abcdef0123456789"
	srv := httpx.Instrument(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, adminRoutes(rg.h, adminOptions{Key: key, Timeout: 30 * time.Second}))
	post := func(q string) (int, MergeAnswer, string) {
		r := httptest.NewRequest(http.MethodPost, "/v1/admin/users/"+from.String()[:8]+"/merge-into/"+to.String()[:8]+q, nil)
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var a MergeAnswer
		_ = json.Unmarshal(w.Body.Bytes(), &a)
		return w.Code, a, w.Body.String()
	}
	if code, a, body := post("?dry_run=1"); code != 200 || a.Connections != 1 {
		t.Fatalf("dry run: %d %s", code, body)
	}
	for _, q := range []string{"", "?force=1"} {
		code, _, body := post(q)
		if code != 409 || !strings.Contains(body, model.CodeMergeHasConnections) {
			t.Fatalf("merge%s: %d %s", q, code, body)
		}
	}
	if _, _, err := rg.st.GetUser(context.Background(), from); err != nil {
		t.Fatal("the source was deleted")
	}
	// Disconnected, it merges.
	rg.do(from, "DELETE", "/v1/me/connections/fake", nil)
	if code, _, body := post("?force=1"); code != 200 {
		t.Fatalf("after the disconnect: %d %s", code, body)
	}
}

// GET /v1/me names a direct source by the provider's display name, with origin direct and no bundle id.
func TestUserSourcesNameTheProvider(t *testing.T) {
	rg := newConnRig(t)
	u := rg.account()
	rg.connectAccount(u, "fake")
	now := time.Now().UTC().Truncate(time.Second)
	rg.pool.Exec(context.Background(), `INSERT INTO user_sources (user_id, name, kind, last_seen, origin) VALUES ($1,'ryft-direct-fake','sleep',$2,'direct')`, u, now)
	if err := rg.st.UpsertSources(context.Background(), u, []store.SourceSeen{{Name: "Watch", Kind: model.KindHR, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
	me := decode[model.User](t, rg.do(u, "GET", "/v1/me", nil), 200)
	got := map[string]model.UserSource{}
	for _, s := range me.Sources {
		got[s.Name] = s
	}
	if d, ok := got["Fake"]; !ok || d.Origin != "direct" || d.BundleID != nil || len(d.Kinds) != 1 {
		t.Fatalf("sources %+v", me.Sources)
	}
	if w := got["Watch"]; w.Origin != "healthkit" {
		t.Fatalf("sources %+v", me.Sources)
	}
	if _, leaked := got["ryft-direct-fake"]; leaked {
		t.Fatal("the reserved name is shown")
	}
}

func TestConnectRegistryRefusesAProviderWithoutAnAdapter(t *testing.T) {
	if _, err := connectRegistry(config.Connect{Providers: []string{"garmin"}}); err == nil {
		t.Fatal("garmin has no adapter in phase 1")
	}
	reg, err := connectRegistry(config.Connect{Providers: []string{"fake"}, Fake: true})
	if err != nil || len(reg.Names()) != 1 {
		t.Fatalf("%v %v", reg, err)
	}
}
