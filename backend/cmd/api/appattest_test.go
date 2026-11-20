package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/appattest"
	"github.com/timkrebs/ryft/internal/appattest/appattesttest"
	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// ---- a fake store ----

// fakeAttestStore keeps challenges and keys in memory with the store's semantics (single use, expiry,
// per account; counter compare-and-set). The caps and the purge are the store's tests'.
type fakeAttestStore struct {
	mu         sync.Mutex
	challenges map[uuid.UUID]map[[32]byte]time.Time
	keys       map[uuid.UUID]map[[32]byte]store.AppAttestKey
	loseRace   bool // AdvanceAttestCounter answers false, as when another renewal stored a higher counter
}

func newFakeAttestStore() *fakeAttestStore {
	return &fakeAttestStore{challenges: map[uuid.UUID]map[[32]byte]time.Time{}, keys: map[uuid.UUID]map[[32]byte]store.AppAttestKey{}}
}

func (f *fakeAttestStore) CreateAttestChallenge(_ context.Context, user uuid.UUID, hash [32]byte, expires time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.challenges[user] == nil {
		f.challenges[user] = map[[32]byte]time.Time{}
	}
	f.challenges[user][hash] = expires
	return nil
}

func (f *fakeAttestStore) ConsumeAttestChallenge(_ context.Context, user uuid.UUID, hash [32]byte, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	exp, ok := f.challenges[user][hash]
	delete(f.challenges[user], hash)
	return ok && exp.After(now), nil
}

func (f *fakeAttestStore) AttestKey(_ context.Context, user uuid.UUID, keyID [32]byte) (store.AppAttestKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[user][keyID]
	if !ok {
		return store.AppAttestKey{}, store.ErrNotFound
	}
	return k, nil
}

func (f *fakeAttestStore) AttestationsSince(_ context.Context, user uuid.UUID, since time.Time) (int, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, oldest := 0, time.Time{}
	for _, k := range f.keys[user] {
		if !k.AttestedAt.Before(since) {
			n++
			if oldest.IsZero() || k.AttestedAt.Before(oldest) {
				oldest = k.AttestedAt
			}
		}
	}
	return n, oldest, nil
}

func (f *fakeAttestStore) PutAttestKey(_ context.Context, k store.AppAttestKey) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys[k.UserID] == nil {
		f.keys[k.UserID] = map[[32]byte]store.AppAttestKey{}
	}
	k.Counter, k.LastUsedAt = 0, k.AttestedAt
	f.keys[k.UserID][k.KeyID] = k
	return nil
}

func (f *fakeAttestStore) AdvanceAttestCounter(_ context.Context, user uuid.UUID, keyID [32]byte, counter uint32, now time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.keys[user][keyID]
	if !ok || f.loseRace || counter <= k.Counter {
		return false, nil
	}
	k.Counter, k.LastUsedAt = counter, now
	f.keys[user][keyID] = k
	return true, nil
}

// ---- the bench: the api's real mux, a fixed user, no database ----

const attestAPIKey = "app-attest-test-key-0123456789"

// testClock is the routes' and the verifier's clock; the token check runs on the system clock, so it
// starts at the real time.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *testClock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type attestBench struct {
	t      *testing.T
	app    *appattesttest.App
	ring   *appattest.Ring
	st     *fakeAttestStore
	clock  *testClock
	logs   *lockedBuffer
	srv    http.Handler
	user   uuid.UUID
	access bool // the fixed user's beta access
}

type benchOpt struct {
	mode    config.Mode // APP_ATTEST
	noRing  bool
	gate    config.Mode // BETA_GATE
	exempt  []uuid.UUID
	noBeta  bool // the user has no beta access
	adminOn bool
	pg      *store.Store // a real store: the routes keep their state in it and the user row is ensureUser's
}

// closedStore is a store whose every query fails (a closed pool): routes past the gates answer 5xx,
// never anything App Attest says.
func closedStore(t *testing.T) *store.Store {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://api@127.0.0.1:1/ryft")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	return store.New(pool)
}

func newAttestBench(t *testing.T, o benchOpt) *attestBench {
	t.Helper()
	start := time.Now().UTC().Truncate(time.Second)
	b := &attestBench{t: t, clock: &testClock{t: start}, logs: &lockedBuffer{}, st: newFakeAttestStore(), user: uuid.New(),
		access: !o.noBeta}
	b.app = appattesttest.NewApp(t, "TEAMID1234.dev.example.app", start)
	var err error
	if b.ring, err = appattest.ParseRing(appattesttest.RingLine(9, 0xc0ffee)); err != nil {
		t.Fatal(err)
	}
	log := logx.NewWriter(b.logs, "api", slog.LevelDebug)
	prev := slog.Default()
	slog.SetDefault(log) // httpx.Fail logs through the default logger, as in main
	t.Cleanup(func() { slog.SetDefault(prev) })
	mode := cmpMode(o.mode)
	cfg := config.AppAttest{Mode: mode, ExemptUsers: o.exempt}
	h := &handler{st: closedStore(t), log: log, cache: newInsightCache(), accounts: &accounts{}, beta: defaultBeta(), attestMode: mode}
	var routes attestStore = b.st
	load := func(_ context.Context, p auth.Principal) (model.User, error) {
		return model.User{ID: p.UserID, TZ: "UTC", BetaAccess: b.access}, nil
	}
	if o.pg != nil {
		h.st, routes, load = o.pg, o.pg, nil
	}
	if !o.noRing {
		cfg.Ring = b.ring
		h.attest = newAppAttest(routes, appattest.NewVerifierWithRoot(b.app.AppID, b.app.Root.Cert, b.clock.now), b.ring, log, b.clock.now)
	}
	adminCfg := config.Admin{}
	if o.adminOn {
		adminCfg.Key = strings.Repeat("k", 40)
	}
	mux := newMux(h, muxOptions{log: log, verifier: auth.NewAPIKey(attestAPIKey, uuid.New()),
		ready: func(context.Context) error { return nil }, tick: http.NotFoundHandler(), gate: cmpMode(o.gate),
		attest: cfg, admin: adminCfg, authMode: config.AuthAPIKey, clients: httpx.NewClientKeys(1), loadUser: load})
	b.srv = httpx.Instrument(log, nil, httpx.Recover(httpx.SecurityHeaders(mux)))
	return b
}

func cmpMode(m config.Mode) config.Mode {
	if m == "" {
		return config.ModeOff
	}
	return m
}

type reply struct {
	status int
	header http.Header
	body   string
}

func (r reply) field(k string) string {
	var m map[string]any
	_ = json.Unmarshal([]byte(r.body), &m)
	s, _ := m[k].(string)
	return s
}

// do sends a request as the bench's user (bearer "" sends none); headers are name, value pairs.
func (b *attestBench) do(method, path, body, bearerUser string, headers ...string) reply {
	b.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if bearerUser != "" {
		req.Header.Set("Authorization", "Bearer "+attestAPIKey+":"+bearerUser)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	b.srv.ServeHTTP(rec, req)
	return reply{rec.Code, rec.Header(), rec.Body.String()}
}

func (b *attestBench) me() string { return b.user.String() }

func mustJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// challenge gets a challenge for the bench's user.
func (b *attestBench) challenge() string {
	b.t.Helper()
	r := b.do(http.MethodPost, "/v1/app-attest/challenge", "", b.me())
	c := r.field("challenge")
	if r.status != 200 || c == "" {
		b.t.Fatalf("challenge: %d %s", r.status, r.body)
	}
	return c
}

// attest runs challenge and keys for the bench's key.
func (b *attestBench) attest() {
	b.t.Helper()
	c := b.challenge()
	att := base64.StdEncoding.EncodeToString(b.app.Attestation(b.t, appattest.ClientDataHash(c)))
	r := b.do(http.MethodPost, "/v1/app-attest/keys", mustJSON(model.AppAttestKeyPost{KeyID: b.app.KeyIDString(), Attestation: att, Challenge: c}), b.me())
	if r.status != 201 || r.field("key_id") != b.app.KeyIDString() {
		b.t.Fatalf("keys: %d %s", r.status, r.body)
	}
}

// token runs challenge and token with the assertion's counter.
func (b *attestBench) token(counter uint32) reply {
	b.t.Helper()
	c := b.challenge()
	as := base64.StdEncoding.EncodeToString(b.app.Assertion(b.t, counter, appattest.ClientDataHash(c)))
	return b.do(http.MethodPost, "/v1/app-attest/token", mustJSON(model.AppAttestTokenPost{KeyID: b.app.KeyIDString(), Challenge: c, Assertion: as}), b.me())
}

// appAttestLogKeys are the keys of the lines the routes and the token check write, besides the
// standard ones and the access line's.
var appAttestLogKeys = logcheck.Lines{
	"http": logcheck.HTTP(model.CodeAppAttestRequired, model.CodeAppAttestChallengeInvalid, model.CodeAppAttestInvalid,
		model.CodeAppAttestKeyUnknown, model.CodeInviteRequired, model.CodeConsentRequired),
	"request failed":                 {"status": logcheck.IntBetween(500, 599), "err": logcheck.Text(1024)},
	"app attest challenge refused":   {},
	"app attest key unknown":         {},
	"app attest key attested":        {},
	"app attest attestation refused": {"step": logcheck.Enum("cbor", "fmt", "x5c", "chain", "nonce", "key_id", "credential_id", "rp_id", "counter", "aaguid", "signature")},
	"app attest assertion refused":   {"step": logcheck.Enum("cbor", "signature", "rp_id", "counter")},
	"app attest": {
		"code": logcheck.Enum(model.CodeAppAttestRequired), "reason": logcheck.Enum("missing", "invalid", "expired", "other_account"),
		"mode": logcheck.Enum("warn", "on"), "route_group": logcheck.Enum("sync", "train", "journal", "account", "config", "reads", "other"),
		"method":    logcheck.Enum("GET", "POST", "PUT", "PATCH", "DELETE"),
		"app_build": logcheck.Pattern(regexp.MustCompile(`^$|^[0-9]+(\.[0-9]+){0,3}$`)), "app_device": logcheck.Bool,
	},
}

// checkLogs fails on a line or key appAttestLogKeys does not allow, and on any of the values.
func (b *attestBench) checkLogs(values ...string) map[string][]map[string]any {
	b.t.Helper()
	lines, problems := logcheck.Check(b.logs.String(), logcheck.Standard("api"), appAttestLogKeys, logcheck.Forbidden{Texts: values})
	for _, p := range problems {
		b.t.Error(p)
	}
	return lines
}

// The whole flow under on: a challenge, a key, a token; the token then opens the routes the check
// covers, and nothing in any line is a value of the exchange.
func TestAppAttestFlow(t *testing.T) {
	b := newAttestBench(t, benchOpt{mode: config.ModeOn})
	if r := b.do(http.MethodGet, "/v1/model", "", b.me()); r.status != 403 || r.field("code") != model.CodeAppAttestRequired {
		t.Fatalf("without a token under on: %d %s", r.status, r.body)
	}
	b.attest()
	r := b.token(1)
	var tok model.AppAttestToken
	if r.status != 200 || json.Unmarshal([]byte(r.body), &tok) != nil || tok.Token == "" || tok.TTLSec != 3600 ||
		!tok.ExpiresAt.Equal(b.clock.now().Add(time.Hour)) {
		t.Fatalf("token: %d %s", r.status, r.body)
	}
	if appattest.TokenTTL < appattest.MinTokenTTL || tok.TTLSec < int(appattest.MinTokenTTL/time.Second) {
		t.Fatalf("ttl_sec %d is below the floor of AA3", tok.TTLSec)
	}
	if r := b.do(http.MethodGet, "/v1/model", "", b.me(), model.HeaderAppToken, tok.Token); r.status != 200 {
		t.Fatalf("with the token under on: %d %s", r.status, r.body)
	}
	// A later renewal: the counter advances.
	if r := b.token(2); r.status != 200 {
		t.Fatalf("second token: %d %s", r.status, r.body)
	}
	// Another account's bearer with this token: other_account, refused.
	if r := b.do(http.MethodGet, "/v1/model", "", uuid.NewString(), model.HeaderAppToken, tok.Token); r.status != 403 {
		t.Fatalf("another account's token: %d", r.status)
	}
	b.checkLogs(tok.Token, tok.Token[len(tok.Token)-24:], b.app.KeyIDString(), b.app.KeyIDString()[4:24])
}

// Without the ring the three routes answer 501 in every mode with one fixed text, outside the beta
// gate (an account without access gets the 501, not invite_required).
func TestAppAttestRoutesWithoutRing(t *testing.T) {
	b := newAttestBench(t, benchOpt{noRing: true, gate: config.ModeOn, noBeta: true})
	for _, route := range []string{"challenge", "keys", "token"} {
		r := b.do(http.MethodPost, "/v1/app-attest/"+route, `{}`, b.me())
		if r.status != http.StatusNotImplemented || r.field("error") != "App Attest is not set up on this server" || r.field("code") != "" {
			t.Errorf("%s without a ring: %d %s", route, r.status, r.body)
		}
	}
	if r := b.do(http.MethodGet, "/v1/days", "", b.me()); r.field("code") != model.CodeInviteRequired {
		t.Fatalf("the beta gate is on in this bench: %d %s", r.status, r.body)
	}
}

// Every refusal of the three routes, with its status and code; AA4: no answer and no log line holds a
// value of a request (key_id, challenge, attestation, assertion).
func TestAppAttestRouteRefusals(t *testing.T) {
	b := newAttestBench(t, benchOpt{gate: config.ModeOn}) // off: the routes are served in every mode
	keyID := b.app.KeyIDString()
	var sent []string // every request value, for the AA4 scan
	att := func(c string) string {
		s := base64.StdEncoding.EncodeToString(b.app.Attestation(t, appattest.ClientDataHash(c)))
		sent = append(sent, s[40:72])
		return s
	}
	asr := func(counter uint32, c string) string {
		s := base64.StdEncoding.EncodeToString(b.app.Assertion(t, counter, appattest.ClientDataHash(c)))
		sent = append(sent, s[20:52])
		return s
	}
	// A minute between two calls keeps the per-account budgets (one a minute) out of this test.
	ch := func() string {
		b.clock.add(time.Minute)
		c := b.challenge()
		sent = append(sent, c, c[:16])
		return c
	}
	keys := func(k, a, c string) reply {
		return b.do(http.MethodPost, "/v1/app-attest/keys", mustJSON(model.AppAttestKeyPost{KeyID: k, Attestation: a, Challenge: c}), b.me())
	}
	token := func(k, a, c string) reply {
		b.clock.add(time.Minute)
		return b.do(http.MethodPost, "/v1/app-attest/token", mustJSON(model.AppAttestTokenPost{KeyID: k, Challenge: c, Assertion: a}), b.me())
	}
	other := appattesttest.NewApp(t, b.app.AppID, b.clock.now()) // another install, under another root
	type want struct {
		status int
		code   string
	}
	var replies []reply
	check := func(name string, r reply, w want) {
		t.Helper()
		replies = append(replies, r)
		if r.status != w.status || r.field("code") != w.code {
			t.Errorf("%s: %d %q %s, want %d %q", name, r.status, r.field("code"), r.body, w.status, w.code)
		}
	}
	devAAGUID := []byte("appattestdevelop")

	// POST /v1/app-attest/keys
	check("keys: not JSON", b.do(http.MethodPost, "/v1/app-attest/keys", `{"key_id": `+keyID, b.me()), want{400, ""})
	check("keys: an unknown field", b.do(http.MethodPost, "/v1/app-attest/keys",
		`{"key_id":"`+keyID+`","attestation":"AAAA","challenge":"x","note_9f3e":"x"}`, b.me()), want{400, ""})
	check("keys: no challenge", keys(keyID, att(ch()), ""), want{400, ""})
	c := ch()
	check("keys: key_id of 43 characters", keys(keyID[:43], att(c), c), want{400, ""})
	check("keys: the challenge was used up by the 400", keys(keyID, att(c), c), want{422, model.CodeAppAttestChallengeInvalid})
	c = ch()
	check("keys: key_id as base64url", keys(keyID[:42]+"-=", att(c), c), want{400, ""})
	check("keys: attestation not base64", keys(keyID, "o2Nm*bXRv", ch()), want{400, ""})
	check("keys: attestation over 32 KiB", keys(keyID, strings.Repeat("AAAA", 8193), ch()), want{400, ""})
	check("keys: an unknown challenge", keys(keyID, att("never-issued-3c7a"), "never-issued-3c7a"), want{422, model.CodeAppAttestChallengeInvalid})
	c = ch()
	check("keys: once", keys(keyID, att(c), c), want{201, ""})
	check("keys: the same challenge again", keys(keyID, att(c), c), want{422, model.CodeAppAttestChallengeInvalid})
	c = ch()
	b.clock.add(appattest.ChallengeTTL + time.Second)
	check("keys: an expired challenge", keys(keyID, att(c), c), want{422, model.CodeAppAttestChallengeInvalid})
	theirs := b.do(http.MethodPost, "/v1/app-attest/challenge", "", uuid.NewString()).field("challenge")
	sent = append(sent, theirs)
	check("keys: another account's challenge", keys(keyID, att(theirs), theirs), want{422, model.CodeAppAttestChallengeInvalid})
	c = ch()
	dev := base64.StdEncoding.EncodeToString(b.app.AttestationWith(t, appattest.ClientDataHash(c), devAAGUID))
	sent = append(sent, dev[40:72])
	check("keys: a development attestation", keys(keyID, dev, c), want{422, model.CodeAppAttestInvalid})
	c = ch()
	check("keys: an attestation over another challenge", keys(keyID, att(ch()), c), want{422, model.CodeAppAttestInvalid})
	c = ch()
	oa := base64.StdEncoding.EncodeToString(other.Attestation(t, appattest.ClientDataHash(c)))
	sent = append(sent, oa[40:72], other.KeyIDString())
	check("keys: a chain to another root", keys(other.KeyIDString(), oa, c), want{422, model.CodeAppAttestInvalid})
	c = ch()
	check("keys: another key's id", keys(other.KeyIDString(), att(c), c), want{422, model.CodeAppAttestInvalid})

	// POST /v1/app-attest/token (the bench's key is attested above)
	unknown := base64.StdEncoding.EncodeToString(make([]byte, 32))
	c = ch()
	check("token: an unknown key", token(unknown, asr(1, c), c), want{404, model.CodeAppAttestKeyUnknown})
	check("token: an unknown key before an unknown challenge", token(unknown, asr(1, "nope-41c2"), "nope-41c2"), want{404, model.CodeAppAttestKeyUnknown})
	check("token: an unknown challenge", token(keyID, asr(1, "nope-41c2"), "nope-41c2"), want{422, model.CodeAppAttestChallengeInvalid})
	c = ch()
	check("token: an assertion over another challenge", token(keyID, asr(1, ch()), c), want{422, model.CodeAppAttestInvalid})
	c = ch()
	oas := base64.StdEncoding.EncodeToString(other.Assertion(t, 1, appattest.ClientDataHash(c)))
	sent = append(sent, oas[20:52])
	check("token: another key's signature", token(keyID, oas, c), want{422, model.CodeAppAttestInvalid})
	c = ch()
	check("token: counter 1", token(keyID, asr(1, c), c), want{200, ""})
	c = ch()
	check("token: counter 1 again (a replay)", token(keyID, asr(1, c), c), want{422, model.CodeAppAttestInvalid})
	c = ch()
	b.st.loseRace = true
	check("token: another renewal stored a higher counter first", token(keyID, asr(5, c), c), want{422, model.CodeAppAttestInvalid})
	b.st.loseRace = false
	check("token: assertion over 1 KiB", token(keyID, strings.Repeat("AAAA", 257), ch()), want{400, ""})
	check("token: key_id not base64", token("not a key id, 44 characters long, at all!!!", asr(1, "x"), ch()), want{400, ""})
	check("token: no challenge", token(keyID, asr(1, "x"), ""), want{400, ""})
	check("token: an unknown field", b.do(http.MethodPost, "/v1/app-attest/token",
		`{"key_id":"`+keyID+`","challenge":"x","assertion":"AAAA","kid_77ab":1}`, b.me()), want{400, ""})

	// POST /v1/app-attest/challenge
	check("challenge: a body with a field", b.do(http.MethodPost, "/v1/app-attest/challenge", `{"nonce_5e1d":"x"}`, b.me()), want{400, ""})
	check("challenge: {}", b.do(http.MethodPost, "/v1/app-attest/challenge", `{}`, b.me()), want{200, ""})

	sent = append(sent, keyID, keyID[8:30], unknown, other.KeyIDString()[8:30], "note_9f3e", "kid_77ab", "nonce_5e1d", "never-issued-3c7a", "nope-41c2")
	for _, r := range replies {
		if r.status < 400 {
			continue
		}
		for _, v := range sent {
			if strings.Contains(r.body, v) {
				t.Errorf("AA4: the answer %d %s holds %q of the request", r.status, r.body, v)
			}
		}
	}
	lines := b.checkLogs(sent...)
	if len(lines["app attest attestation refused"]) != 4 || len(lines["app attest assertion refused"]) != 4 {
		t.Errorf("want 4 attestation and 4 assertion refusals logged with their step:\n%s", b.logs.String())
	}
}

// The budgets: challenge and token 10 at once, then one a minute, each its own; keys 10 in 24 hours,
// Retry-After until the oldest is 24 hours old.
func TestAppAttestRouteLimits(t *testing.T) {
	b := newAttestBench(t, benchOpt{})
	for i := range 10 {
		if r := b.do(http.MethodPost, "/v1/app-attest/challenge", "", b.me()); r.status != 200 {
			t.Fatalf("challenge %d: %d", i+1, r.status)
		}
	}
	r := b.do(http.MethodPost, "/v1/app-attest/challenge", "", b.me())
	if r.status != 429 || r.header.Get("Retry-After") != "60" {
		t.Fatalf("the 11th challenge: %d Retry-After %q", r.status, r.header.Get("Retry-After"))
	}
	if r := b.do(http.MethodPost, "/v1/app-attest/challenge", "", uuid.NewString()); r.status != 200 {
		t.Fatalf("another account's budget is its own: %d", r.status)
	}
	// The token budget is its own: ten token requests go through to their own refusals.
	for i := range 10 {
		if r := b.do(http.MethodPost, "/v1/app-attest/token", `{}`, b.me()); r.status != 400 {
			t.Fatalf("token %d: %d", i+1, r.status)
		}
	}
	if r := b.do(http.MethodPost, "/v1/app-attest/token", `{}`, b.me()); r.status != 429 || r.header.Get("Retry-After") != "60" {
		t.Fatalf("the 11th token: %d %v", r.status, r.header)
	}
	b.clock.add(time.Minute)
	if r := b.do(http.MethodPost, "/v1/app-attest/challenge", "", b.me()); r.status != 200 {
		t.Fatalf("a minute later: %d", r.status)
	}
	if r := b.do(http.MethodPost, "/v1/app-attest/challenge", "", b.me()); r.status != 429 {
		t.Fatalf("one a minute: %d", r.status)
	}

	// keys: 10 attested in the last 24 hours, the oldest 23 hours ago.
	k := newAttestBench(t, benchOpt{})
	now := k.clock.now()
	for i := range 10 {
		id := [32]byte{byte(i + 1)}
		_ = k.st.PutAttestKey(context.Background(), store.AppAttestKey{UserID: k.user, KeyID: id, AttestedAt: now.Add(-23*time.Hour + time.Duration(i)*time.Minute)})
	}
	r = k.do(http.MethodPost, "/v1/app-attest/keys", `{}`, k.me())
	if r.status != 429 || r.header.Get("Retry-After") != "3600" {
		t.Fatalf("the 11th key in 24 hours: %d Retry-After %q", r.status, r.header.Get("Retry-After"))
	}
	k.clock.add(time.Hour + time.Second)
	if r := k.do(http.MethodPost, "/v1/app-attest/keys", `{}`, k.me()); r.status != 400 {
		t.Fatalf("once the oldest is 24 hours old: %d %s", r.status, r.body)
	}
}

// The token check through the api's mux: off never reads the header; warn serves and logs; on refuses
// every covered route, the import included; the exempt routes and accounts pass under on; DELETE
// /v1/me and /v1/admin/* are not wrapped.
func TestAppAttestModesThroughTheMux(t *testing.T) {
	refusedBy := func(r reply) bool { return r.status == 403 && r.field("code") == model.CodeAppAttestRequired }

	off := newAttestBench(t, benchOpt{mode: config.ModeOff})
	if r := off.do(http.MethodGet, "/v1/model", "", off.me(), model.HeaderAppToken, "garbage"); r.status != 200 ||
		strings.Contains(off.logs.String(), `"msg":"app attest"`) {
		t.Fatalf("off with a garbage token: %d\n%s", r.status, off.logs.String())
	}

	warn := newAttestBench(t, benchOpt{mode: config.ModeWarn})
	before := testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues("missing", "api"))
	if r := warn.do(http.MethodGet, "/v1/model", "", warn.me(), model.HeaderAppBuild, "20261119.091500"); r.status != 200 || r.header.Get(httpx.GateHeader) != "" {
		t.Fatalf("warn without a token: %d %v", r.status, r.header)
	}
	if got := testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues("missing", "api")) - before; got != 1 {
		t.Errorf("warn counted %v", got)
	}
	lines := warn.checkLogs()
	if len(lines["app attest"]) != 1 || lines["app attest"][0]["route_group"] != "config" || lines["app attest"][0]["app_build"] != "20261119.091500" {
		t.Fatalf("warn line:\n%s", warn.logs.String())
	}
	// The access line names the account (user_id, as every request line does); the warn line does not.
	for _, l := range strings.Split(warn.logs.String(), "\n") {
		if strings.Contains(l, `"msg":"app attest"`) && (strings.Contains(l, warn.me()) || strings.Contains(l, "user_id")) {
			t.Fatalf("the warn line names the account: %s", l)
		}
	}
	if !strings.Contains(warn.logs.String(), warn.me()) {
		t.Fatal("no line names the account, so the check above proves nothing")
	}

	on := newAttestBench(t, benchOpt{mode: config.ModeOn, adminOn: true})
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/config", ""}, {http.MethodGet, "/v1/me", ""}, {http.MethodGet, "/v1/days", ""},
		{http.MethodPut, "/v1/sessions/" + uuid.NewString(), "{}"}, {http.MethodPost, "/v1/train/import", "{}"},
		{http.MethodPost, "/v1/devices/" + uuid.NewString() + "/push", "{}"}, {http.MethodGet, "/v1/no-such-route", ""},
		{http.MethodPost, "/v1/app-attest/other", "{}"},
	} {
		if r := on.do(c.method, c.path, c.body, on.me()); !refusedBy(r) {
			t.Errorf("on: %s %s without a token = %d %s", c.method, c.path, r.status, r.body)
		}
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/app-attest/challenge", ""}, {http.MethodPost, "/v1/app-attest/keys", "{}"},
		{http.MethodPost, "/v1/app-attest/token", "{}"}, {http.MethodPost, "/v1/me/apple-authorization", "{}"},
		{http.MethodDelete, "/v1/me/consent", ""}, {http.MethodDelete, "/v1/devices/" + uuid.NewString() + "/push", ""},
		{http.MethodPost, "/v1/feedback", "{}"}, {http.MethodDelete, "/v1/me", ""},
		{http.MethodGet, "/v1/admin/flags", ""}, {http.MethodOptions, "/v1/admin/flags", ""},
	} {
		if r := on.do(c.method, c.path, c.body, on.me()); refusedBy(r) {
			t.Errorf("on: exempt %s %s was refused", c.method, c.path)
		}
	}
	// The import with a token reaches its handler.
	on.attest()
	tok := on.token(1).field("token")
	if r := on.do(http.MethodPost, "/v1/train/import", "{}", on.me(), model.HeaderAppToken, tok); refusedBy(r) {
		t.Fatal("the import with a token was refused")
	}

	exempt := uuid.New()
	ex := newAttestBench(t, benchOpt{mode: config.ModeOn, exempt: []uuid.UUID{exempt}})
	if r := ex.do(http.MethodGet, "/v1/model", "", exempt.String()); r.status != 200 {
		t.Fatalf("an exempt account under on: %d", r.status)
	}
	if r := ex.do(http.MethodGet, "/v1/model", "", ex.me()); !refusedBy(r) {
		t.Fatalf("another account under on: %d", r.status)
	}
}

// The order: the bearer first (no bearer is 401, nothing counted or logged), then the token, then
// the beta gate (without a token the answer is app_attest_required, with one invite_required).
func TestAppAttestOrder(t *testing.T) {
	b := newAttestBench(t, benchOpt{mode: config.ModeOn, gate: config.ModeOn, noBeta: true})
	before := testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues("missing", "api"))
	if r := b.do(http.MethodGet, "/v1/days", "", ""); r.status != 401 {
		t.Fatalf("no bearer: %d", r.status)
	}
	if r := b.do(http.MethodPost, "/v1/train/import", "{}", ""); r.status != 401 {
		t.Fatalf("no bearer on the import: %d", r.status)
	}
	if testutil.ToFloat64(metrics.AppAttestRefusals.WithLabelValues("missing", "api")) != before ||
		strings.Contains(b.logs.String(), `"msg":"app attest"`) {
		t.Fatalf("a request without a bearer reached the token check:\n%s", b.logs.String())
	}
	if r := b.do(http.MethodGet, "/v1/days", "", b.me()); r.field("code") != model.CodeAppAttestRequired {
		t.Fatalf("no token, no access: %d %s", r.status, r.body)
	}
	b.attest() // outside the beta gate
	tok := b.token(1).field("token")
	if r := b.do(http.MethodGet, "/v1/days", "", b.me(), model.HeaderAppToken, tok); r.field("code") != model.CodeInviteRequired {
		t.Fatalf("a token, no access: %d %s", r.status, r.body)
	}
}

// The flow on Postgres (migration 0037, the api's grants, ensureUser's row): a key, two tokens, a
// replayed assertion refused, the challenge single use, and GET /v1/config under on with the token says
// app_attest "on" (AA2). Skips without RYFT_TEST_POSTGRES_URL.
func TestAppAttestOnPostgres(t *testing.T) {
	pool := testPool(t)
	b := newAttestBench(t, benchOpt{mode: config.ModeOn, pg: store.New(pool)})
	b.attest()
	tok := b.token(1).field("token")
	if tok == "" {
		t.Fatal("no token")
	}
	r := b.do(http.MethodGet, "/v1/config", "", b.me(), model.HeaderAppToken, tok)
	if r.status != 200 || r.field("app_attest") != model.AppAttestOn {
		t.Fatalf("GET /v1/config with the token: %d %s", r.status, r.body)
	}
	if r := b.token(2); r.status != 200 {
		t.Fatalf("a second token: %d %s", r.status, r.body)
	}
	c := b.challenge()
	as := base64.StdEncoding.EncodeToString(b.app.Assertion(t, 2, appattest.ClientDataHash(c)))
	body := mustJSON(model.AppAttestTokenPost{KeyID: b.app.KeyIDString(), Challenge: c, Assertion: as})
	if r := b.do(http.MethodPost, "/v1/app-attest/token", body, b.me()); r.status != 422 || r.field("code") != model.CodeAppAttestInvalid {
		t.Fatalf("a replayed counter: %d %s", r.status, r.body)
	}
	if r := b.do(http.MethodPost, "/v1/app-attest/token", body, b.me()); r.status != 422 || r.field("code") != model.CodeAppAttestChallengeInvalid {
		t.Fatalf("the challenge again: %d %s", r.status, r.body)
	}
}

func TestAttestExempt(t *testing.T) {
	id := uuid.NewString()
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, "/v1/app-attest/challenge", true}, {http.MethodPost, "/v1/app-attest/keys", true},
		{http.MethodPost, "/v1/app-attest/token", true}, {http.MethodPost, "/v1/me/apple-authorization", true},
		{http.MethodDelete, "/v1/me/consent", true}, {http.MethodPost, "/v1/feedback", true},
		{http.MethodDelete, "/v1/devices/" + id + "/push", true},
		{http.MethodGet, "/v1/app-attest/challenge", false}, {http.MethodPost, "/v1/me/consent", false},
		{http.MethodPost, "/v1/devices/" + id + "/push", false}, {http.MethodDelete, "/v1/devices//push", false},
		{http.MethodDelete, "/v1/devices/a/b/push", false}, {http.MethodDelete, "/v1/devices/" + id + "/push/x", false},
		{http.MethodPost, "/v1/feedback/", false}, {http.MethodGet, "/v1/me", false}, {http.MethodPost, "/v1/sync", false},
		{http.MethodPost, "/v1/train/import", false}, {http.MethodGet, "/v1/config", false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := attestExempt(r); got != c.want {
			t.Errorf("%s %s exempt = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestAttestModeValue(t *testing.T) {
	for m, want := range map[config.Mode]string{"": "off", config.ModeOff: "off", config.ModeWarn: "warn", config.ModeOn: "on"} {
		if got := attestModeValue(m); got != want {
			t.Errorf("%q: %q, want %q", m, got, want)
		}
	}
}
