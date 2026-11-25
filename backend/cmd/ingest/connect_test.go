package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/connect/fake"
	"github.com/timkrebs/ryft/internal/eventbus"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/logx/logcheck"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// A built secret, not a literal in the source.
var fakeSecret = strings.Repeat("hk", 12)

var codeRe = regexp.MustCompile(`^[a-z_]{1,64}$`)

// stubStore is connectStore in memory: it records what the hook and the consumer ask of it.
type stubStore struct {
	mu sync.Mutex

	// the hook
	gotMin   string // the consent cutoff StoreInbox was given
	stored   [][]store.HookItem
	outcomes func(items []store.HookItem) []store.HookOutcome
	storeErr error

	// the consumer
	pending    []store.InboxItem // what PendingDirect lists: the account's other pending rows
	pendingErr error
	row        store.InboxItem
	rowErr     error
	bumps      int
	bumpN      int
	finished   []string // "state/code"
	user       model.User
	userErr    error
	applied    []store.DirectRecord
	applyErr   error
	alsoErr    bool // ApplyDirect fails while it is given other records, and works on its own
	applyOut   store.DirectOutcome
	deregCalls int
	permCalls  []bool

	// the tick
	republished int
	ages        store.InboxAges
}

func (s *stubStore) StoreInbox(_ context.Context, _, minPolicy string, items []store.HookItem, emit func(pgx.Tx, model.InboxReceivedEvent) error) ([]store.HookOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gotMin = minPolicy
	s.stored = append(s.stored, items)
	if s.storeErr != nil {
		return nil, s.storeErr
	}
	outs := make([]store.HookOutcome, len(items))
	for i := range outs {
		outs[i] = store.HookStored
	}
	if s.outcomes != nil {
		outs = s.outcomes(items)
	}
	for i, o := range outs {
		if o == store.HookStored || o == store.HookUpdated {
			if err := emit(nil, model.InboxReceivedEvent{InboxID: int64(i + 1), UserID: uuid.New(), Provider: "fake", Kind: items[i].Kind, Revision: 1}); err != nil {
				return nil, err
			}
		}
	}
	return outs, nil
}
func (s *stubStore) InboxRow(context.Context, int64) (store.InboxItem, error) { return s.row, s.rowErr }
func (s *stubStore) PendingDirect(context.Context, uuid.UUID, string, int64, int) ([]store.InboxItem, error) {
	return s.pending, s.pendingErr
}
func (s *stubStore) BumpInboxAttempt(context.Context, int64, int) (int, error) {
	s.bumps++
	return s.bumpN + s.bumps, nil
}
func (s *stubStore) FinishInbox(_ context.Context, _ int64, _ int, state, code string) error {
	s.finished = append(s.finished, state+"/"+code)
	return nil
}
func (s *stubStore) GetUser(context.Context, uuid.UUID) (model.User, *time.Location, error) {
	return s.user, time.UTC, s.userErr
}
func (s *stubStore) ApplyDirect(_ context.Context, r store.DirectRecord) (store.DirectResult, error) {
	s.applied = append(s.applied, r)
	if s.applyErr != nil || (s.alsoErr && len(r.Also) > 0) {
		if s.applyErr == nil {
			return store.DirectResult{}, errors.New("a record of the batch fails")
		}
		return store.DirectResult{}, s.applyErr
	}
	if r.Emit != nil {
		_ = r.Emit(nil, []string{"2026-11-24"})
	}
	o := s.applyOut
	if o == "" {
		o = store.DirectApplied
	}
	var also []int64
	for _, x := range r.Also {
		also = append(also, x.InboxID)
	}
	return store.DirectResult{Outcome: o, Days: []string{"2026-11-24"}, Also: also}, nil
}
func (s *stubStore) ApplyDeregistration(_ context.Context, _ int64, _ int, _ uuid.UUID, _, _ string, emit func(pgx.Tx, []string) error) (store.DirectResult, error) {
	s.deregCalls++
	_ = emit(nil, []string{"2026-11-24"})
	return store.DirectResult{Outcome: store.DirectApplied, Days: []string{"2026-11-24"}}, nil
}
func (s *stubStore) ApplyPermissions(_ context.Context, _ int64, _ int, _ uuid.UUID, _, _ string, missing bool) (store.DirectOutcome, error) {
	s.permCalls = append(s.permCalls, missing)
	return store.DirectApplied, nil
}
func (s *stubStore) RepublishInbox(_ context.Context, _ int, emit func(pgx.Tx, model.InboxReceivedEvent) error) (int, error) {
	for i := 0; i < s.republished; i++ {
		_ = emit(nil, model.InboxReceivedEvent{InboxID: int64(i + 1), Revision: 1})
	}
	return s.republished, nil
}
func (s *stubStore) ExpireInbox(context.Context, int) (int64, error) { return 0, nil }
func (s *stubStore) PurgeInbox(context.Context, int) (map[string]int64, error) {
	return map[string]int64{"processed": 2, "failed": 1}, nil
}
func (s *stubStore) InboxAges(context.Context) (store.InboxAges, error) { return s.ages, nil }

// hookRig is a connectHook on the stub, with the outbox's insert and flush recorded and its log captured.
type hookRig struct {
	st     *stubStore
	hook   *connectHook
	log    *bytes.Buffer
	events []events.Message
	flush  int
	now    time.Time
	srv    http.Handler
}

func newRig(t *testing.T) *hookRig {
	t.Helper()
	rg := &hookRig{st: &stubStore{}, log: &bytes.Buffer{}, now: time.Date(2026, 11, 24, 6, 5, 0, 0, time.UTC)}
	lg := logx.NewWriter(rg.log, "ingest", slog.LevelDebug)
	reg, err := pushRegistry(config.Connect{Fake: true, FakePushSecret: fakeSecret, Providers: []string{"fake"}})
	if err != nil {
		t.Fatal(err)
	}
	rg.hook = newConnectHook(rg.st, reg, config.Beta{PrivacyPolicyVersion: "2026-10-26", Consent: config.ModeOn}, "", nil, lg)
	rg.hook.insert = func(_ context.Context, _ pgx.Tx, m events.Message) error {
		rg.events = append(rg.events, m)
		return nil
	}
	rg.hook.flush = func(context.Context) { rg.flush++ }
	rg.hook.now = func() time.Time { return rg.now }
	mux := http.NewServeMux()
	mux.HandleFunc("POST /connect/{provider}/push", rg.hook.push)
	mux.HandleFunc("/", httpx.NotFound)
	rg.srv = httpx.Instrument(lg, nil, httpx.Recover(mux))
	return rg
}

func (rg *hookRig) post(provider, body string, sig string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/connect/"+provider+"/push", strings.NewReader(body))
	if sig != "" {
		r.Header.Set(fake.SignatureHeader, sig)
	}
	w := httptest.NewRecorder()
	rg.srv.ServeHTTP(w, r)
	return w
}

func (rg *hookRig) signed(body string) *httptest.ResponseRecorder {
	return rg.post("fake", body, fake.Sign(fakeSecret, []byte(body)))
}

const goodPush = `{"items":[{"kind":"sleep","record_id":"n-1","user":"puid-1","version":1,"night":{"state":"final","segments":[
 {"start":"2026-11-23T22:30:00Z","end":"2026-11-24T00:30:00Z","stage":"light"}]}},
 {"kind":"hrv","record_id":"h-1","user":"puid-1","version":1,"hrv":{"start":"2026-11-24T02:00:00Z","end":"2026-11-24T02:05:00Z","rmssd_ms":48.5}}]}`

type oversizedSplit struct{ connect.Provider }

func (oversizedSplit) SplitPush([]byte) ([]connect.PushItem, error) {
	return make([]connect.PushItem, connect.MaxItems+1), nil
}

func TestHookRejectsOverLimitSplitBeforeStorage(t *testing.T) {
	rg := newRig(t)
	reg, err := connect.NewRegistry(oversizedSplit{fake.New(fakeSecret)})
	if err != nil {
		t.Fatal(err)
	}
	rg.hook.reg = reg.PushSide()
	w := rg.signed(goodPush)
	if w.Code != http.StatusBadRequest || len(rg.st.stored) != 0 || len(rg.events) != 0 || rg.flush != 0 {
		t.Fatalf("status=%d stored=%d events=%d flushes=%d", w.Code, len(rg.st.stored), len(rg.events), rg.flush)
	}
}

func TestHookStoresAndAnswers200WithoutProcessing(t *testing.T) {
	rg := newRig(t)
	w := rg.signed(goodPush)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(rg.st.stored) != 1 || len(rg.st.stored[0]) != 2 {
		t.Fatalf("stored %+v", rg.st.stored)
	}
	it := rg.st.stored[0][0]
	if it.Kind != "sleep" || it.RecordID != "n-1" || it.ProviderUserID != "puid-1" || it.Version != 1 || !strings.Contains(string(it.Payload), `"stage":"light"`) {
		t.Fatalf("item %+v", it)
	}
	// One event per new row, in the same transaction (the stub calls emit as the store does), and a flush.
	if len(rg.events) != 2 || rg.events[0].Key != events.KeyInboxReceived || rg.flush != 1 {
		t.Fatalf("events %d, flushes %d", len(rg.events), rg.flush)
	}
	// Nothing was processed inline.
	if len(rg.st.applied) != 0 || rg.st.bumps != 0 {
		t.Fatal("the hook processed a record")
	}
}

func TestHookRefusals(t *testing.T) {
	rg := newRig(t)
	for name, c := range map[string]struct {
		do   func() *httptest.ResponseRecorder
		want int
	}{
		"unknown provider": {func() *httptest.ResponseRecorder { return rg.post("nope", goodPush, "") }, 404},
		"no signature":     {func() *httptest.ResponseRecorder { return rg.post("fake", goodPush, "") }, 401},
		"wrong signature": {func() *httptest.ResponseRecorder {
			return rg.post("fake", goodPush, fake.Sign("x"+fakeSecret, []byte(goodPush)))
		}, 401},
		"body not json":     {func() *httptest.ResponseRecorder { return rg.signed(`{"items": [`) }, 400},
		"no items":          {func() *httptest.ResponseRecorder { return rg.signed(`{"items": []}`) }, 400},
		"body over the cap": {func() *httptest.ResponseRecorder { return rg.signed(strings.Repeat("x", connect.MaxPushBytes+1)) }, 413},
	} {
		if w := c.do(); w.Code != c.want {
			t.Errorf("%s: %d, want %d", name, w.Code, c.want)
		}
	}
	if len(rg.st.stored) != 0 || len(rg.events) != 0 {
		t.Fatalf("a refused push stored something: %+v", rg.st.stored)
	}
	// A body under the cap that the cap's reader would pass whole, chunked (no Content-Length), is cut too.
	r := httptest.NewRequest(http.MethodPost, "/connect/fake/push", strings.NewReader(strings.Repeat("y", connect.MaxPushBytes+10)))
	r.ContentLength = -1
	w := httptest.NewRecorder()
	rg.srv.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatalf("an unbounded body: %d", w.Code)
	}
}

// Every outcome of an item answers 200: the hook never tells a caller which provider users RYFT knows.
func TestHookAnswers200ForUnknownUserNoConsentAndCollision(t *testing.T) {
	rg := newRig(t)
	rg.st.outcomes = func(items []store.HookItem) []store.HookOutcome {
		return []store.HookOutcome{store.HookUnknownUser, store.HookNoConsent}
	}
	before := testutil.ToFloat64(metrics.ConnectPushItems.WithLabelValues("fake", "sleep", "unknown_user"))
	w := rg.signed(goodPush)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("%d %q", w.Code, w.Body)
	}
	if len(rg.events) != 0 || rg.flush != 0 {
		t.Fatal("events for nothing stored")
	}
	if got := testutil.ToFloat64(metrics.ConnectPushItems.WithLabelValues("fake", "sleep", "unknown_user")) - before; got != 1 {
		t.Fatalf("unknown_user counted %v", got)
	}
	rg.st.outcomes = func(items []store.HookItem) []store.HookOutcome {
		return []store.HookOutcome{store.HookCollision, store.HookDuplicate}
	}
	if w := rg.signed(goodPush); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if !strings.Contains(rg.log.String(), `"msg":"connect record id collision"`) || !strings.Contains(rg.log.String(), `"level":"ERROR"`) {
		t.Fatalf("no error line for a collision:\n%s", rg.log)
	}
}

func TestHookBadItems(t *testing.T) {
	rg := newRig(t)
	body := `{"items":[
	 {"kind":"steps","record_id":"a","user":"u","version":1},
	 {"kind":"hrv","record_id":"","user":"u","version":1},
	 {"kind":"hrv","record_id":"` + strings.Repeat("r", 129) + `","user":"u","version":1},
	 {"kind":"hrv","record_id":"nul\u0000","user":"u","version":1},
	 {"kind":"hrv","record_id":"b","user":"","version":1},
	 {"kind":"hrv","record_id":"c","user":"u","version":1,"note":"\u0000"},
	 {"kind":"hrv","record_id":"ok","user":"u","version":1,"hrv":{"start":"2026-11-24T02:00:00Z","end":"2026-11-24T02:05:00Z","rmssd_ms":40}}]}`
	if w := rg.signed(body); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(rg.st.stored) != 1 || len(rg.st.stored[0]) != 1 || rg.st.stored[0][0].RecordID != "ok" {
		t.Fatalf("only the good item is stored: %+v", rg.st.stored)
	}
	if !strings.Contains(rg.log.String(), `"bad_item":6`) {
		t.Fatalf("the line does not count the 6 bad items:\n%s", rg.log)
	}
}

func TestHookStoreFailureIs503AndFlushesNothing(t *testing.T) {
	rg := newRig(t)
	rg.st.storeErr = errors.New("connection refused")
	w := rg.signed(goodPush)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" || rg.flush != 0 {
		t.Fatalf("%d retry-after %q flushes %d", w.Code, w.Header().Get("Retry-After"), rg.flush)
	}
	if strings.Contains(w.Body.String(), "connection refused") {
		t.Fatal("the answer holds the database's error")
	}
}

// A provider whose verification cannot run now is 503 (it retries), a bad signature 401.
func TestHookVerificationUnavailableIs503(t *testing.T) {
	rg := newRig(t)
	rg.hook.reg = verifyDown()
	if w := rg.signed(goodPush); w.Code != 503 {
		t.Fatalf("%d", w.Code)
	}
	if len(rg.st.stored) != 0 {
		t.Fatal("stored without a verification")
	}
}

type downProvider struct{ *fake.Provider }

func (downProvider) VerifyPush(http.Header, []byte, time.Time) error {
	return errors.New("jwks unreachable")
}

func verifyDown() *connect.PushRegistry {
	reg, _ := connect.NewRegistry(downProvider{fake.New(fakeSecret)})
	return reg.PushSide()
}

// The pushes line counts outcomes and carries no user id, record id, address or body (design 7.2).
func TestHookLogLineCarriesNoIdentity(t *testing.T) {
	rg := newRig(t)
	rg.st.outcomes = func(items []store.HookItem) []store.HookOutcome {
		return []store.HookOutcome{store.HookStored, store.HookDuplicate}
	}
	rg.signed(goodPush)
	rg.post("fake", goodPush, "")
	lines := logcheck.Lines{
		"http": logcheck.HTTP(),
		"connect push": {"provider": logcheck.Enum("fake"), "kind": logcheck.Enum("sleep", "hrv", "deregistration", "permissions", "mixed", "unknown", "none"), "outcome": logcheck.Enum("accepted", "rejected", "too_large", "bad_body", "busy", "timeout", "error"),
			"stored": logcheck.Count, "duplicate": logcheck.Count, "bad_item": logcheck.Count},
	}
	_, problems := logcheck.Check(rg.log.String(), logcheck.Standard("ingest"), lines,
		logcheck.Forbidden{Texts: []string{"puid-1", "n-1", "h-1", "48.5", "rmssd", "2026-11-23T22", fakeSecret, fake.Sign(fakeSecret, []byte(goodPush))}, Numbers: []float64{48.5}})
	for _, p := range problems {
		t.Error(p)
	}
	if !strings.Contains(rg.log.String(), `"outcome":"rejected"`) || !strings.Contains(rg.log.String(), `"outcome":"accepted"`) {
		t.Fatalf("both pushes are logged:\n%s", rg.log)
	}
	// The refused push was never parsed: kind none; the accepted one mixed a night and an HRV.
	if !strings.Contains(rg.log.String(), `"kind":"none","outcome":"rejected"`) || !strings.Contains(rg.log.String(), `"kind":"mixed","outcome":"accepted"`) {
		t.Fatalf("kinds of the two lines:\n%s", rg.log)
	}
}

// ---- the consumer -----------------------------------------------------------------------------------------------

func (rg *hookRig) consume(row store.InboxItem) error {
	rg.st.row = row
	return rg.hook.consume(context.Background(), model.InboxReceivedEvent{
		InboxID: row.ID, UserID: row.UserID, Provider: row.Provider, Kind: row.Kind, Revision: row.Revision,
	})
}

func consented() model.User {
	return model.User{Consent: &model.Consent{PolicyVersion: "2026-10-26", GrantedAt: time.Now()}}
}

func sleepRow(now time.Time) store.InboxItem {
	start := now.Add(-9 * time.Hour).Format(time.RFC3339)
	end := now.Add(-2 * time.Hour).Format(time.RFC3339)
	return store.InboxItem{ID: 7, UserID: uuid.New(), Provider: "fake", Kind: "sleep", RecordID: "n-1", State: "pending", Revision: 1,
		Payload: []byte(`{"kind":"sleep","night":{"state":"final","segments":[{"start":"` + start + `","end":"` + end + `","stage":"deep"}]}}`)}
}

func TestConsumerIgnoresMismatchedEvents(t *testing.T) {
	rg := newRig(t)
	row := sleepRow(rg.now)
	row.Revision = 2
	rg.st.row = row
	rg.st.user = consented()
	for name, alter := range map[string]func(*model.InboxReceivedEvent){
		"superseded revision": func(ev *model.InboxReceivedEvent) { ev.Revision-- },
		"wrong owner":         func(ev *model.InboxReceivedEvent) { ev.UserID = uuid.New() },
		"wrong provider":      func(ev *model.InboxReceivedEvent) { ev.Provider = "other" },
		"wrong kind":          func(ev *model.InboxReceivedEvent) { ev.Kind = connect.KindHRV },
	} {
		t.Run(name, func(t *testing.T) {
			ev := model.InboxReceivedEvent{InboxID: row.ID, UserID: row.UserID, Provider: row.Provider, Kind: row.Kind, Revision: row.Revision}
			alter(&ev)
			if err := rg.hook.consume(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
			if rg.st.bumps != 0 || len(rg.st.finished) != 0 || len(rg.st.applied) != 0 || len(rg.events) != 0 || rg.flush != 0 {
				t.Fatal("a mismatched event changed the inbox or caused a side effect")
			}
		})
	}
	if err := rg.consume(row); err != nil || rg.st.bumps != 1 || len(rg.st.applied) != 1 || len(rg.events) != 1 || rg.flush != 1 {
		t.Fatalf("current revision: %v, attempts=%d, applied=%d, events=%d, flushes=%d",
			err, rg.st.bumps, len(rg.st.applied), len(rg.events), rg.flush)
	}
}

func TestConsumerAppliesARecord(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	if err := rg.consume(sleepRow(rg.now)); err != nil {
		t.Fatal(err)
	}
	if len(rg.st.applied) != 1 {
		t.Fatalf("applied %d", len(rg.st.applied))
	}
	r := rg.st.applied[0]
	if r.Provider != "fake" || r.Kind != "sleep" || r.RecordID != "n-1" || r.NightState != model.RecordFinal || len(r.Sleep) != 1 || r.Sleep[0].Stage != "deep" {
		t.Fatalf("record %+v", r)
	}
	if len(rg.events) != 1 || rg.events[0].Key != events.KeyIngested || rg.events[0].Reason != model.ReasonConnect || rg.flush != 1 {
		t.Fatalf("events %+v flush %d", rg.events, rg.flush)
	}
	if !strings.Contains(string(rg.events[0].Body), "2026-11-24") || strings.Contains(string(rg.events[0].Body), "deep") {
		t.Fatalf("event body %s", rg.events[0].Body)
	}
}

// P2-3: the account's other pending rows are stored in the same transaction and announced by one event, so a big
// push does not queue one ingested event per row ahead of the account's next sync in scoring's per-user order. A
// row that does not parse is left to its own delivery.
func TestConsumerBatchesTheAccountsOtherPendingRows(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	row := sleepRow(rg.now)
	two, three, bad := sleepRow(rg.now), sleepRow(rg.now), sleepRow(rg.now)
	two.ID, two.RecordID, three.ID, three.RecordID = 8, "n-2", 9, "n-3"
	bad.ID, bad.RecordID, bad.Payload = 10, "n-4", []byte(`{"kind":"sleep","night":`)
	for _, x := range []*store.InboxItem{&two, &three, &bad} {
		x.UserID = row.UserID
	}
	rg.st.pending = []store.InboxItem{two, bad, three}
	before := testutil.ToFloat64(metrics.ConnectInboxProcessed.WithLabelValues("fake", "sleep", "processed"))
	if err := rg.consume(row); err != nil {
		t.Fatal(err)
	}
	if len(rg.st.applied) != 1 || len(rg.st.applied[0].Also) != 2 {
		t.Fatalf("applied %+v", rg.st.applied)
	}
	if a := rg.st.applied[0].Also; a[0].InboxID != 8 || a[1].InboxID != 9 || a[0].RecordID != "n-2" {
		t.Fatalf("batched %+v", a)
	}
	if len(rg.events) != 1 || rg.flush != 1 {
		t.Fatalf("one event for three records: %d events, flush %d", len(rg.events), rg.flush)
	}
	if got := testutil.ToFloat64(metrics.ConnectInboxProcessed.WithLabelValues("fake", "sleep", "processed")) - before; got != 3 {
		t.Fatalf("processed counted %v, want 3", got)
	}
	if len(rg.st.finished) != 0 {
		t.Fatalf("the row that does not parse was finished here: %v", rg.st.finished)
	}
}

// A batch that fails does not fail the row it was started for: that row is applied alone.
func TestConsumerBatchFailureFallsBackToTheRowAlone(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	row, two := sleepRow(rg.now), sleepRow(rg.now)
	two.ID, two.RecordID, two.UserID = 8, "n-2", row.UserID
	rg.st.pending = []store.InboxItem{two}
	rg.st.alsoErr = true
	if err := rg.consume(row); err != nil {
		t.Fatalf("the row failed with the batch: %v", err)
	}
	if len(rg.st.applied) != 2 || len(rg.st.applied[0].Also) != 1 || len(rg.st.applied[1].Also) != 0 {
		t.Fatalf("applied %+v", rg.st.applied)
	}
	if len(rg.events) != 1 {
		t.Fatalf("%d events", len(rg.events))
	}
	// A failure of the row itself is still a retry.
	rg2 := newRig(t)
	rg2.st.user = consented()
	rg2.st.applyErr = errors.New("db down")
	rg2.st.pending = []store.InboxItem{two}
	if err := rg2.consume(row); err == nil {
		t.Fatal("a database error was swallowed")
	}
}

// D100: the event delivered again for a row that is no longer pending changes nothing.
func TestConsumerIsIdempotent(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	row := sleepRow(rg.now)
	row.State = "processed"
	row.Payload = nil
	for i := 0; i < 3; i++ {
		if err := rg.consume(row); err != nil {
			t.Fatal(err)
		}
	}
	if len(rg.st.applied) != 0 || rg.st.bumps != 0 || len(rg.events) != 0 || len(rg.st.finished) != 0 {
		t.Fatalf("a processed row was touched: applied %d, bumps %d, events %d", len(rg.st.applied), rg.st.bumps, len(rg.events))
	}
	// A row that is gone (deleted by a disconnect) is acked too.
	rg.st.rowErr = store.ErrNotFound
	if err := rg.consume(row); err != nil {
		t.Fatal(err)
	}
	// And one that is pending, delivered three times where the store reports it already applied.
	rg.st.rowErr = nil
	rg.st.applyOut = store.DirectNotPending
	for i := 0; i < 3; i++ {
		if err := rg.consume(sleepRow(rg.now)); err != nil {
			t.Fatal(err)
		}
	}
	if rg.flush != 0 {
		t.Fatal("flushed for a repeat")
	}
}

func TestConsumerDropsWithoutConsentInForce(t *testing.T) {
	for name, user := range map[string]model.User{
		"no consent":     {},
		"older policy":   {Consent: &model.Consent{PolicyVersion: "2026-01-01", GrantedAt: time.Now()}},
		"consent is off": {}, // CONSENT_ENFORCE=off changes nothing here
	} {
		rg := newRig(t)
		rg.hook.beta.Consent = config.ModeOff
		rg.st.user = user
		if err := rg.consume(sleepRow(rg.now)); err != nil {
			t.Fatal(err)
		}
		if len(rg.st.applied) != 0 || len(rg.st.finished) != 1 || rg.st.finished[0] != "dropped/no_consent" {
			t.Errorf("%s: applied %d, finished %v", name, len(rg.st.applied), rg.st.finished)
		}
	}
}

func TestConsumerStates(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	// A user who is gone.
	rg.st.userErr = store.ErrNotFound
	_ = rg.consume(sleepRow(rg.now))
	rg.st.userErr = nil
	// A record that does not parse: failed, payload kept by the store for 7 days.
	bad := sleepRow(rg.now)
	bad.Payload = []byte(`{"night":{"state":"maybe","segments":[]}}`)
	_ = rg.consume(bad)
	// A record entirely outside the window: dropped as stale.
	old := sleepRow(rg.now.Add(-500 * 24 * time.Hour))
	_ = rg.consume(old)
	// A record that spans more than a night and its evening.
	wide := sleepRow(rg.now)
	wide.Payload = []byte(`{"night":{"state":"final","segments":[{"start":"2026-11-20T00:00:00Z","end":"2026-11-22T00:00:00Z","stage":"deep"}]}}`)
	rg.now = time.Date(2026, 11, 22, 6, 0, 0, 0, time.UTC)
	_ = rg.consume(wide)
	// A provider that is no longer served.
	gone := sleepRow(rg.now)
	gone.Provider = "garmin"
	_ = rg.consume(gone)
	want := []string{"dropped/no_connection", "failed/parse", "dropped/stale", "failed/parse", "failed/provider_off"}
	if strings.Join(rg.st.finished, ",") != strings.Join(want, ",") {
		t.Fatalf("finished %v, want %v", rg.st.finished, want)
	}
	if len(rg.st.applied) != 0 {
		t.Fatal("applied a record that must not be")
	}
}

func TestConsumerAttemptsAndTransientErrors(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	// The 20th delivery fails the row without applying it.
	rg.st.bumpN = store.MaxInboxAttempts - 1
	if err := rg.consume(sleepRow(rg.now)); err != nil || len(rg.st.finished) != 1 || rg.st.finished[0] != "failed/attempts" || len(rg.st.applied) != 0 {
		t.Fatalf("finished %v applied %d err %v", rg.st.finished, len(rg.st.applied), err)
	}
	// A database error is returned (nack, Pub/Sub delivers again) and the row stays pending.
	rg = newRig(t)
	rg.st.user = consented()
	rg.st.applyErr = errors.New("deadlock detected")
	before := testutil.ToFloat64(metrics.ConnectInboxProcessed.WithLabelValues("fake", "sleep", "retry"))
	if err := rg.consume(sleepRow(rg.now)); err == nil {
		t.Fatal("a transient error was acked")
	}
	if len(rg.st.finished) != 0 || testutil.ToFloat64(metrics.ConnectInboxProcessed.WithLabelValues("fake", "sleep", "retry"))-before != 1 {
		t.Fatalf("finished %v", rg.st.finished)
	}
	// The row is gone while we looked: a disconnect won the race.
	rg = newRig(t)
	rg.st.user = consented()
	rg.st.applyOut = store.DirectNoConnection
	if err := rg.consume(sleepRow(rg.now)); err != nil || rg.flush != 0 {
		t.Fatalf("%v %d", err, rg.flush)
	}
}

func TestConsumerDeregistrationAndPermissions(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	row := store.InboxItem{ID: 8, UserID: uuid.New(), Provider: "fake", Kind: "deregistration", RecordID: "d", State: "pending", Payload: []byte(`{"kind":"deregistration"}`)}
	if err := rg.consume(row); err != nil || rg.st.deregCalls != 1 {
		t.Fatalf("%v %d", err, rg.st.deregCalls)
	}
	if len(rg.events) != 1 || rg.events[0].Reason != model.ReasonDisconnect {
		t.Fatalf("events %+v", rg.events)
	}
	for _, c := range []struct {
		perms   string
		missing bool
	}{{`["read_hrv","read_sleep"]`, false}, {`["read_sleep"]`, true}, {`[]`, true}} {
		p := store.InboxItem{ID: 9, UserID: uuid.New(), Provider: "fake", Kind: "permissions", RecordID: "p", State: "pending", Payload: []byte(`{"permissions":` + c.perms + `}`)}
		if err := rg.consume(p); err != nil {
			t.Fatal(err)
		}
		if got := rg.st.permCalls[len(rg.st.permCalls)-1]; got != c.missing {
			t.Errorf("permissions %s: missing %v, want %v", c.perms, got, c.missing)
		}
	}
}

// The consumer's lines carry the provider, the kind and a code: never an id, a payload or a health value.
func TestConsumerLogCarriesNoPayload(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	_ = rg.consume(sleepRow(rg.now))
	bad := sleepRow(rg.now)
	bad.Payload = []byte(`{"night":{"state":"secret-state-61.7","segments":[]}}`)
	_ = rg.consume(bad)
	lines := logcheck.Lines{
		"connect inbox": {"provider": logcheck.Enum("fake"), "kind": logcheck.Enum("sleep", "hrv", "deregistration", "permissions"),
			"outcome": logcheck.Enum("processed", "no_consent", "no_connection", "stale", "failed"), "days": logcheck.Count, "code": logcheck.Pattern(codeRe)},
	}
	_, problems := logcheck.Check(rg.log.String(), logcheck.Standard("ingest"), lines,
		logcheck.Forbidden{Texts: []string{"secret-state", "n-1", "deep", rg.st.row.UserID.String()}})
	for _, p := range problems {
		t.Error(p)
	}
}

// ---- the tick ---------------------------------------------------------------------------------------------------

func TestTickStepUpkeepAndDailyNotice(t *testing.T) {
	rg := newRig(t)
	rg.st.republished = 2
	rg.st.ages = store.InboxAges{Pending: 120, Failed: 3600, FailedRows: 2}
	if err := rg.hook.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rg.events) != 2 || rg.events[0].Key != events.KeyInboxReceived {
		t.Fatalf("republished events %d", len(rg.events))
	}
	if got := testutil.ToFloat64(metrics.ConnectInboxOldest.WithLabelValues("failed")); got != 3600 {
		t.Fatalf("oldest failed %v", got)
	}
	if got := testutil.ToFloat64(metrics.ConnectInboxPurged.WithLabelValues("processed")); got < 2 {
		t.Fatalf("purged %v", got)
	}
	if n := strings.Count(rg.log.String(), `"msg":"connect inbox failed"`); n != 1 {
		t.Fatalf("%d notices at 06:05 UTC, want 1", n)
	}
	// Any other tick of the day writes none.
	rg.log.Reset()
	for _, hm := range [][2]int{{6, 15}, {6, 30}, {7, 0}, {5, 59}, {0, 0}, {18, 5}} {
		rg.now = time.Date(2026, 11, 24, hm[0], hm[1], 0, 0, time.UTC)
		_ = rg.hook.tick(context.Background())
	}
	if strings.Contains(rg.log.String(), "connect inbox failed") {
		t.Fatal("a notice outside the 06:00 to 06:14 slot")
	}
	// No failed rows, no notice.
	rg.st.ages.FailedRows = 0
	rg.now = time.Date(2026, 11, 24, 6, 1, 0, 0, time.UTC)
	rg.log.Reset()
	_ = rg.hook.tick(context.Background())
	if strings.Contains(rg.log.String(), "connect inbox failed") {
		t.Fatal("a notice without failed rows")
	}
}

// ---- the reserved source and the push registry -------------------------------------------------------------------

func TestSyncScreenDropsTheReservedSource(t *testing.T) {
	now := time.Now()
	req := model.SyncRequest{
		Samples: []model.Sample{{Type: "hrv_sdnn", Start: now, End: now, Source: "ryft-direct-garmin"}, {Type: "hrv_sdnn", Start: now, End: now, Source: "Garmin Connect"},
			{Type: "hrv_rmssd", Start: now, End: now, Source: "App"}},
		Sleep: []model.SleepSegment{{Start: now, End: now.Add(time.Hour), Stage: "core", Source: "ryft-direct-fake"}, {Start: now, End: now.Add(time.Hour), Stage: "core", Source: "Watch"}},
	}
	ig := screen(&req, now)
	if len(req.Samples) != 2 || len(req.Sleep) != 1 || ig.Samples.UnknownType != 1 || ig.Sleep.UnknownType != 1 {
		t.Fatalf("samples %d sleep %d ignored %+v", len(req.Samples), len(req.Sleep), ig)
	}
	for _, s := range req.Samples {
		if connect.IsReservedSource(s.Source) {
			t.Fatal("a reserved source passed the screen")
		}
	}
}

func TestPushRegistry(t *testing.T) {
	if _, err := pushRegistry(config.Connect{Providers: []string{"garmin"}}); err == nil {
		t.Fatal("a provider without an adapter started")
	}
	reg, err := pushRegistry(config.Connect{})
	if err != nil || len(reg.Names()) != 0 {
		t.Fatalf("%v %v", reg, err)
	}
	// The registry holds the push half only: its static type has no OAuth methods.
	var _ connect.PushSide
}

func TestIngestTicksOrder(t *testing.T) {
	steps := ingestTicks(func(context.Context) error { return nil }, func(context.Context) error { return nil })
	if len(steps) != 2 || steps[0].Name != "db ping" || steps[1].Name != "outbox sweep" {
		t.Fatalf("%+v", steps)
	}
}

// ingest's one event push, /push/inbox.received, is served behind the transport's token check (D218) and hands the
// consumer its event; /push/ingested stays scoring's.
func TestInboxPushRoute(t *testing.T) {
	rg := newRig(t)
	log := slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tr, err := eventbus.Open(ctx, config.Config{PubSub: config.PubSub{Project: config.EmulatorProject, EmulatorHost: "127.0.0.1:1"},
		Push: config.Push{AllowInsecure: true}}, "ingest", log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := rg.hook.register(mux, tr); err != nil {
		t.Fatal(err)
	}
	rg.st.rowErr = store.ErrNotFound // a repeat for a row that is gone: acked
	env := func(key, data string) string {
		return `{"message":{"data":"` + base64.StdEncoding.EncodeToString([]byte(data)) + `","attributes":{"ryft-key":"` + key + `"},"messageId":"91"},` +
			`"subscription":"projects/ryft-local/subscriptions/ingest-inbox-received","deliveryAttempt":1}`
	}
	for path, c := range map[string]struct {
		body string
		want int
	}{
		"/push/inbox.received": {env("inbox.received", `{"inbox_id":5,"user_id":"`+uuid.NewString()+`","provider":"fake","kind":"sleep","revision":1}`), 204},
		"/push/ingested":       {env("ingested", `{}`), 404},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(c.body)))
		if w.Code != c.want {
			t.Errorf("%s: %d, want %d", path, w.Code, c.want)
		}
	}
}

func TestPushKind(t *testing.T) {
	item := func(kind string) connect.PushItem { return connect.PushItem{Kind: kind} }
	for name, c := range map[string]struct {
		in   []connect.PushItem
		want string
	}{
		"none": {nil, "none"}, "one kind": {[]connect.PushItem{item("sleep"), item("sleep")}, "sleep"},
		"mixed":        {[]connect.PushItem{item("sleep"), item("hrv")}, "mixed"},
		"unknown kind": {[]connect.PushItem{item("steps")}, "unknown"},
	} {
		if got := pushKind(c.in); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}

// ---- P2-2: the hook's in-flight cap and its body deadline ----------------------------------------------------------

// failReader fails the test when anything reads it: a push refused for the cap must read nothing.
type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Error("the hook read the body of a push it refused")
	return 0, errors.New("read")
}

// slowPush opens a connection to srv and sends the head of a push whose body never completes. It returns the
// connection; the caller closes it.
func slowPush(t *testing.T, srvURL string) net.Conn {
	t.Helper()
	u, _ := url.Parse(srvURL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	head := "POST /connect/fake/push HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n{\"items\":"
	if _, err := conn.Write([]byte(head)); err != nil {
		t.Fatal(err)
	}
	return conn
}

func pushCount(outcome string) float64 {
	return testutil.ToFloat64(metrics.ConnectPushes.WithLabelValues("fake", "none", outcome))
}

func TestHookCapAnswers503WithoutReadingAndSyncKeepsItsSlots(t *testing.T) {
	rg := newRig(t)
	rg.hook.slots = make(chan struct{}, 1)
	rg.hook.readTimeout = 5 * time.Second
	srv := httptest.NewServer(rg.srv)
	defer srv.Close()
	// One slow push holds the only slot (its body never completes).
	held := slowPush(t, srv.URL)
	defer held.Close()
	deadline := time.Now().Add(3 * time.Second)
	for len(rg.hook.slots) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slow push never took the slot")
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := pushCount("busy")
	r := httptest.NewRequest(http.MethodPost, "/connect/fake/push", failReader{t})
	r.Header.Set(fake.SignatureHeader, "00")
	w := httptest.NewRecorder()
	rg.srv.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("a full hook answered %d, Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
	if len(rg.st.stored) != 0 || len(rg.events) != 0 {
		t.Fatal("a refused push was stored")
	}
	if got := pushCount("busy") - before; got != 1 {
		t.Fatalf("busy counted %v", got)
	}
	// An unknown provider is still 404, with or without a slot.
	if w := rg.post("nope", "{}", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", w.Code)
	}
	// The slot is let go when the push ends: the next signed push is stored.
	held.Close()
	for i := 0; len(rg.hook.slots) != 0; i++ {
		if i > 600 {
			t.Fatal("the slot was not released")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if w := rg.signed(goodPush); w.Code != 200 {
		t.Fatalf("after the slot was free: %d %s", w.Code, w.Body)
	}
}

// The hook leaves ingest's other slots to /v1/sync: its cap is below containerConcurrency in the Cloud Run template.
func TestHookCapLeavesRoomForSync(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/run/ingest.yaml")
	if err != nil {
		t.Skipf("no template: %v", err)
	}
	m := regexp.MustCompile(`containerConcurrency:\s*(\d+)`).FindSubmatch(raw)
	if m == nil {
		t.Skip("no containerConcurrency in the template")
	}
	conc, _ := strconv.Atoi(string(m[1]))
	if maxHookInflight >= conc {
		t.Fatalf("maxHookInflight %d takes every one of ingest's %d concurrent requests", maxHookInflight, conc)
	}
	if got := cap(newConnectHook(&stubStore{}, nil, config.Beta{}, "", nil, slog.Default()).slots); got != maxHookInflight {
		t.Fatalf("a new hook has %d slots", got)
	}
}

func TestHookSlowBodyIsCutAndNothingIsStored(t *testing.T) {
	rg := newRig(t)
	rg.hook.readTimeout = 200 * time.Millisecond
	srv := httptest.NewServer(rg.srv)
	defer srv.Close()
	before := pushCount("timeout")
	conn := slowPush(t, srv.URL)
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a body that stalled: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("a stalled body answered %d", resp.StatusCode)
	}
	if len(rg.st.stored) != 0 || len(rg.events) != 0 {
		t.Fatal("a stalled push was stored")
	}
	if got := pushCount("timeout") - before; got != 1 {
		t.Fatalf("timeout counted %v", got)
	}
	if len(rg.hook.slots) != 0 {
		t.Fatal("the stalled push kept its slot")
	}
}

// D259: the hook is given the cutoff (the policy in force when none is set), and the consumer keeps an account whose
// consent is to the cutoff or later, and drops one below it or withdrawn.
func TestConsentCutoffInHookAndConsumer(t *testing.T) {
	rg := newRig(t)
	rg.signed(goodPush)
	if rg.st.gotMin != "2026-10-26" {
		t.Fatalf("the hook was given cutoff %q, want the policy in force", rg.st.gotMin)
	}
	for name, tc := range map[string]struct {
		cutoff string
		user   model.User
		kept   bool
	}{
		"older than the policy, at the cutoff":    {"2026-09-01", model.User{Consent: &model.Consent{PolicyVersion: "2026-09-01", GrantedAt: time.Now()}}, true},
		"older than the policy, after the cutoff": {"2026-06-01", model.User{Consent: &model.Consent{PolicyVersion: "2026-09-01", GrantedAt: time.Now()}}, true},
		"the policy in force":                     {"2026-09-01", consented(), true},
		"below the cutoff":                        {"2026-09-01", model.User{Consent: &model.Consent{PolicyVersion: "2026-08-31", GrantedAt: time.Now()}}, false},
		"withdrawn (no current consent)":          {"2026-06-01", model.User{}, false},
	} {
		rg := newRig(t)
		rg.hook.minPolicy = tc.cutoff
		rg.st.user = tc.user
		if err := rg.consume(sleepRow(rg.now)); err != nil {
			t.Fatal(err)
		}
		if got := len(rg.st.applied) == 1; got != tc.kept {
			t.Errorf("%s: applied %d, finished %v, want kept=%v", name, len(rg.st.applied), rg.st.finished, tc.kept)
		}
		if !tc.kept && (len(rg.st.finished) != 1 || rg.st.finished[0] != "dropped/no_consent") {
			t.Errorf("%s: finished %v", name, rg.st.finished)
		}
	}
	// A cutoff configured is the hook's.
	reg, _ := pushRegistry(config.Connect{Fake: true, FakePushSecret: fakeSecret, Providers: []string{"fake"}})
	st := &stubStore{}
	h := newConnectHook(st, reg, config.Beta{PrivacyPolicyVersion: "2026-10-26"}, "2026-06-01", nil, slog.Default())
	if h.minPolicy != "2026-06-01" {
		t.Fatalf("minPolicy %q", h.minPolicy)
	}
}

// P2-12: the consumer applies the revision it read.
func TestConsumerPassesTheRevisionItRead(t *testing.T) {
	rg := newRig(t)
	rg.st.user = consented()
	row := sleepRow(rg.now)
	row.Revision = 3
	other := sleepRow(rg.now)
	other.ID, other.RecordID, other.Revision, other.UserID = 8, "n-2", 2, row.UserID
	rg.st.pending = []store.InboxItem{other}
	if err := rg.consume(row); err != nil {
		t.Fatal(err)
	}
	if len(rg.st.applied) != 1 || rg.st.applied[0].Revision != 3 || len(rg.st.applied[0].Also) != 1 || rg.st.applied[0].Also[0].Revision != 2 {
		t.Fatalf("applied %+v", rg.st.applied)
	}
}

// P2-17: an item the store reports as bad_item is counted and the push is still answered 200.
func TestHookCountsABadItemFromTheStore(t *testing.T) {
	rg := newRig(t)
	rg.st.outcomes = func(items []store.HookItem) []store.HookOutcome {
		return []store.HookOutcome{store.HookBadItem, store.HookStored}
	}
	before := testutil.ToFloat64(metrics.ConnectPushItems.WithLabelValues("fake", "sleep", "bad_item"))
	if w := rg.signed(goodPush); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := testutil.ToFloat64(metrics.ConnectPushItems.WithLabelValues("fake", "sleep", "bad_item")) - before; got != 1 {
		t.Fatalf("bad_item counted %v", got)
	}
	if !strings.Contains(rg.log.String(), `"bad_item":1`) {
		t.Fatalf("the line lacks the count:\n%s", rg.log)
	}
}
