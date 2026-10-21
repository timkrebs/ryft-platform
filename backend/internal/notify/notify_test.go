package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

var berlin, _ = time.LoadLocation("Europe/Berlin")

func TestMorningPayload(t *testing.T) {
	r := 78
	m := Morning("2026-10-19", 82, &r, model.StateBalanced, berlin, "en")
	var p struct {
		APS struct {
			Alert struct {
				TitleKey  string   `json:"title-loc-key"`
				TitleArgs []string `json:"title-loc-args"`
				Key       string   `json:"loc-key"`
				Args      []string `json:"loc-args"`
			} `json:"alert"`
			Category string `json:"category"`
			Mutable  int    `json:"mutable-content"`
		} `json:"aps"`
		RYFT map[string]string `json:"ryft"`
	}
	if err := json.Unmarshal(m.Payload(), &p); err != nil {
		t.Fatal(err)
	}
	if p.APS.Alert.TitleKey != "MORNING_SCORE_TITLE" || p.APS.Alert.TitleArgs[0] != "82%" ||
		p.APS.Alert.Key != "MORNING_SCORE_BODY_BALANCED" || p.APS.Alert.Args[0] != "78" ||
		p.APS.Category != "MORNING_SCORE" || p.APS.Mutable != 1 || p.RYFT["url"] != "https://ryftapp.de/day/2026-10-19" {
		t.Fatalf("payload %s", m.Payload())
	}
	if got := Render(m.TitleKey, m.TitleArgs, "en"); got != "Recovery 82%" {
		t.Fatalf("title %q", got)
	}
	de := Morning("2026-10-19", 82, &r, model.StateDepleted, berlin, "de")
	if Render(de.TitleKey, de.TitleArgs, "de") != "Erholung 82 %" || de.BodyKey != "MORNING_SCORE_BODY_DEPLETED" {
		t.Fatalf("german: %+v", de)
	}
	if !de.Expires.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, berlin)) {
		t.Fatalf("expires at the end of the local day: %v", de.Expires)
	}
	if Morning("2026-10-19", 60, nil, model.StateCalibrating, berlin, "en").BodyArgs[0] != "60" {
		t.Fatal("without readiness the recovery stands in")
	}
}

func TestSessionAndRecordMessages(t *testing.T) {
	now := time.Now()
	for records, key := range map[int]string{0: "SESSION_SCORED_BODY", 1: "SESSION_SCORED_BODY_PR", 3: "SESSION_SCORED_BODY_PRS"} {
		m := Session("2026-10-19", "s1", "Upper A", 11.24, records, now, "en")
		if m.BodyKey != key {
			t.Errorf("%d records: %s", records, m.BodyKey)
		}
	}
	m := Session("2026-10-19", "s1", "Upper A", 11.24, 3, now, "de")
	if got := Render(m.BodyKey, m.BodyArgs, "de"); got != "Belastung 11,2, 3 neue Rekorde." {
		t.Fatalf("german body %q", got)
	}
	if got := Render(m.TitleKey, m.TitleArgs, "en"); got != "Upper A scored" {
		t.Fatalf("title %q", got)
	}
	if u := Session("d", "s", " ", 9, 0, now, "en"); u.TitleKey != "SESSION_SCORED_TITLE_UNTITLED" || len(u.TitleArgs) != 0 {
		t.Fatalf("untitled: %+v", u)
	}
	if got := decimal(12.0, "en"); got != "12" {
		t.Fatalf("whole numbers without a decimal: %s", got)
	}
	pr := Record("d", "s", "Kniebeuge", model.PRMaxWeight, 122.5, now, "de")
	if got := Render(pr.BodyKey, pr.BodyArgs, "de"); got != "122,5 kg, dein schwerster Satz bisher." {
		t.Fatalf("record %q", got)
	}
	if reps := Record("d", "s", "Pull-up", model.PRMaxReps, 12, now, "en"); reps.BodyArgs[0] != "12" {
		t.Fatalf("reps: %v", reps.BodyArgs)
	}
}

// Every key a builder can produce has English and German text taking the
// same arguments.
func TestCatalog(t *testing.T) {
	placeholder := regexp.MustCompile(`%(\d\$)?@`)
	for key, texts := range Catalog {
		en, de := placeholder.FindAllString(texts[0], -1), placeholder.FindAllString(texts[1], -1)
		if len(en) != len(de) || texts[0] == "" || texts[1] == "" || texts[0] == texts[1] {
			t.Errorf("%s: en %q de %q", key, texts[0], texts[1])
		}
	}
	r := 70
	now := time.Now()
	for _, m := range []Message{Morning("d", 1, &r, model.StateBalanced, berlin, "en"), Morning("d", 1, &r, model.StateStrained, berlin, "en"),
		Morning("d", 1, &r, model.StateDepleted, berlin, "en"), Morning("d", 1, &r, model.StateCalibrating, berlin, "en"),
		Session("d", "s", "t", 1, 0, now, "en"), Session("d", "s", "", 1, 1, now, "en"), Session("d", "s", "t", 1, 2, now, "en"),
		Record("d", "s", "x", model.PRE1RM, 1, now, "en"), Record("d", "s", "x", model.PRMaxWeight, 1, now, "en"),
		Record("d", "s", "x", model.PRMaxReps, 1, now, "en"), Record("d", "s", "x", model.PRMaxVolume, 1, now, "en"),
		Stale("d", 21, now)} {
		for _, k := range []struct {
			key  string
			args []string
		}{{m.TitleKey, m.TitleArgs}, {m.BodyKey, m.BodyArgs}} {
			texts, ok := Catalog[k.key]
			if !ok {
				t.Errorf("%s: no catalog entry", k.key)
				continue
			}
			if n := len(placeholder.FindAllString(texts[0], -1)); n != len(k.args) {
				t.Errorf("%s takes %d arguments, gets %d", k.key, n, len(k.args))
			}
			if strings.Contains(Render(k.key, k.args, "de"), "%") && !strings.Contains(k.key, "TITLE") {
				t.Errorf("%s: unfilled placeholder", k.key)
			}
		}
	}
}

func TestQuietUntil(t *testing.T) {
	night := model.NotificationPrefs{QuietHours: &model.QuietHours{From: "22:00", To: "07:00"}}
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, 19+d, h, m, 0, 0, berlin) }
	for _, c := range []struct {
		now   time.Time
		quiet bool
		until time.Time
	}{
		{at(0, 23, 30), true, at(1, 7, 0)},
		{at(0, 6, 15), true, at(0, 7, 0)},
		{at(0, 7, 0), false, time.Time{}},
		{at(0, 21, 59), false, time.Time{}},
	} {
		until, quiet := QuietUntil(night, c.now)
		if quiet != c.quiet || !until.Equal(c.until) {
			t.Errorf("%v: quiet %v until %v", c.now, quiet, until)
		}
	}
	lunch := model.NotificationPrefs{QuietHours: &model.QuietHours{From: "13:00", To: "14:30"}}
	if until, quiet := QuietUntil(lunch, at(0, 13, 10)); !quiet || !until.Equal(at(0, 14, 30)) {
		t.Fatal("same-day quiet hours")
	}
	if _, quiet := QuietUntil(model.NotificationPrefs{QuietHours: &model.QuietHours{From: "08:00", To: "08:00"}}, at(0, 8, 0)); quiet {
		t.Fatal("from = to is no quiet time")
	}
	if _, quiet := QuietUntil(model.NotificationPrefs{}, at(0, 3, 0)); quiet {
		t.Fatal("no quiet hours set")
	}
}

// ---- the service, with a fake store and a fake APNs ----

type fakeStore struct {
	mu        sync.Mutex
	recipient store.Recipient
	morning   store.Morning
	session   store.SessionNotice
	stale     []store.Recipient
	claims    map[string]int // day/category → devices
	dropped   []string
	delivered []string
}

func key(day, category string) string { return day + "/" + category }

func (f *fakeStore) Recipient(context.Context, uuid.UUID) (store.Recipient, error) {
	return f.recipient, nil
}
func (f *fakeStore) MorningScore(context.Context, uuid.UUID, string) (store.Morning, error) {
	return f.morning, nil
}
func (f *fakeStore) SessionNotice(context.Context, uuid.UUID, uuid.UUID) (store.SessionNotice, error) {
	return f.session, nil
}
func (f *fakeStore) StaleCandidates(context.Context, time.Time) ([]store.Recipient, error) {
	return f.stale, nil
}
func (f *fakeStore) ClaimNotification(_ context.Context, _ uuid.UUID, day, category string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.claims[key(day, category)]; ok {
		return false, nil
	}
	f.claims[key(day, category)] = -1
	return true, nil
}
func (f *fakeStore) ReleaseNotification(_ context.Context, _ uuid.UUID, day, category string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.claims, key(day, category))
	return nil
}
func (f *fakeStore) NotificationSent(_ context.Context, _ uuid.UUID, day, category string, devices int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims[key(day, category)] = devices
	return nil
}
func (f *fakeStore) PushDelivered(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, token)
	return nil
}
func (f *fakeStore) DropPushToken(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dropped = append(f.dropped, token)
	return nil
}

type fakeAPNs struct {
	mu   sync.Mutex
	sent []apns.Notification
	down bool
}

func (f *fakeAPNs) Send(_ context.Context, n apns.Notification) (apns.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return apns.Result{}, errors.New("connection refused")
	}
	f.sent = append(f.sent, n)
	if strings.HasPrefix(n.Token, "dead") {
		return apns.Result{Status: 410, Reason: "Unregistered"}, nil
	}
	return apns.Result{Status: 200}, nil
}

func (f *fakeAPNs) count(pushType string) int {
	n := 0
	for _, s := range f.sent {
		if s.PushType == pushType {
			n++
		}
	}
	return n
}

var tokens = []model.PushToken{{DeviceID: uuid.New(), Token: "aaaa", Env: apns.Sandbox, Locale: "en"},
	{DeviceID: uuid.New(), Token: "bbbb", Env: apns.Production, Locale: "de"}}

func service(now time.Time, prefs model.NotificationPrefs) (*Service, *fakeStore, *fakeAPNs) {
	rec, ready := 82, 78
	st := &fakeStore{claims: map[string]int{},
		recipient: store.Recipient{ID: uuid.New(), Loc: berlin, Prefs: prefs, Tokens: tokens},
		morning:   store.Morning{Recovery: &rec, Readiness: &ready, State: model.StateBalanced}}
	ap := &fakeAPNs{}
	return &Service{St: st, APNs: ap, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}, st, ap
}

func morningEvent(day string) events.NotifyMessage {
	return events.NotifyMessage{Scored: &model.ScoredEvent{UserID: uuid.New(), Day: day, Recovery: 80}}
}

func TestMorningSendsOncePerDay(t *testing.T) {
	now := time.Date(2026, 10, 19, 7, 30, 0, 0, berlin)
	s, st, ap := service(now, model.NotificationPrefs{})
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushAlert) != 2 || ap.count(apns.PushBackground) != 2 || st.claims[key("2026-10-19", model.NotifyMorningScore)] != 2 {
		t.Fatalf("sent %+v claims %v", ap.sent, st.claims)
	}
	var en, de []string
	for _, n := range ap.sent {
		if n.PushType == apns.PushAlert {
			var p struct {
				APS struct {
					Alert struct {
						Args []string `json:"title-loc-args"`
					} `json:"alert"`
				} `json:"aps"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			if n.Token == "aaaa" {
				en = p.APS.Alert.Args
			} else {
				de = p.APS.Alert.Args
			}
		}
	}
	if en[0] != "82%" || de[0] != "82 %" {
		t.Fatalf("arguments per device language: en %v de %v", en, de)
	}
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil || len(ap.sent) != 4 {
		t.Fatalf("a redelivered event sends nothing: %v, %d sent", err, len(ap.sent))
	}
}

func TestMorningWaitsForQuietHoursAndRespectsPrefs(t *testing.T) {
	quiet := model.NotificationPrefs{QuietHours: &model.QuietHours{From: "22:30", To: "07:00"}}
	s, _, ap := service(time.Date(2026, 10, 19, 6, 20, 0, 0, berlin), quiet)
	err := s.Handle(context.Background(), morningEvent("2026-10-19"))
	if err == nil || err.Error() != events.Defer(40*time.Minute).Error() {
		t.Fatalf("deferred until 07:00: %v", err)
	}
	if ap.count(apns.PushBackground) != 2 || ap.count(apns.PushAlert) != 0 {
		t.Fatal("the silent refresh goes at once, the alert waits")
	}
	muted := model.NotificationPrefs{Categories: map[string]bool{model.NotifyMorningScore: false}}
	s, _, ap = service(time.Date(2026, 10, 19, 8, 0, 0, 0, berlin), muted)
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil || ap.count(apns.PushAlert) != 0 {
		t.Fatalf("muted: %v %d", err, ap.count(apns.PushAlert))
	}
	s, _, ap = service(time.Date(2026, 10, 20, 8, 0, 0, 0, berlin), model.NotificationPrefs{})
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil || len(ap.sent) != 0 {
		t.Fatal("yesterday's score is not sent today")
	}
}

func TestDeliveryPrunesAndRetries(t *testing.T) {
	s, st, _ := service(time.Date(2026, 10, 19, 8, 0, 0, 0, berlin), model.NotificationPrefs{})
	st.recipient.Tokens = []model.PushToken{{Token: "dead1", Env: apns.Sandbox}, {Token: "cccc", Env: apns.Sandbox}}
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil {
		t.Fatal(err)
	}
	if len(st.dropped) != 1 || st.dropped[0] != "dead1" {
		t.Fatalf("a gone token is dropped (once, on the first send): %v", st.dropped)
	}
	s, st, ap := service(time.Date(2026, 10, 19, 8, 0, 0, 0, berlin), model.NotificationPrefs{})
	ap.down = true
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err == nil {
		t.Fatal("APNs unreachable: the event is retried")
	}
	if len(st.claims) != 0 {
		t.Fatalf("and the claims are released for the retry: %v", st.claims)
	}
	ap.down = false
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil || ap.count(apns.PushAlert) != 2 {
		t.Fatalf("the retry sends: %v", err)
	}
	s.APNs = nil
	st.claims = map[string]int{}
	if err := s.Handle(context.Background(), morningEvent("2026-10-19")); err != nil || len(st.claims) != 0 {
		t.Fatal("without APNs nothing is claimed")
	}
}

func TestSessionNotifications(t *testing.T) {
	now := time.Date(2026, 10, 19, 19, 0, 0, 0, berlin)
	ended, strain := now.Add(-20*time.Minute), 11.2
	notice := store.SessionNotice{Title: "Upper A", Status: model.SessionCompleted, EndedAt: &ended, Strain: &strain,
		PRs: []store.PRNotice{{ExerciseEN: "Bench press", ExerciseDE: "Bankdrücken", Kind: model.PRE1RM, Value: 101.3}}}
	ev := events.NotifyMessage{SessionScored: &model.SessionScoredEvent{UserID: uuid.New(), SessionID: uuid.New(), Day: "2026-10-19"}}

	s, st, ap := service(now, model.NotificationPrefs{})
	st.session = notice
	if err := s.Handle(context.Background(), ev); err != nil || len(ap.sent) != 2 || !strings.Contains(string(ap.sent[0].Payload), "SESSION_SCORED_BODY_PR") {
		t.Fatalf("session scored: %v %+v", err, ap.sent)
	}
	s, st, ap = service(now, model.NotificationPrefs{Categories: map[string]bool{model.NotifySessionScored: false}})
	st.session = notice
	_ = s.Handle(context.Background(), ev)
	if len(ap.sent) != 2 || !strings.Contains(string(ap.sent[1].Payload), "Bankdrücken") || st.claims[key("2026-10-19", model.NotifyPR)] != 2 {
		t.Fatalf("records only: %+v", ap.sent)
	}
	s, st, ap = service(now, model.NotificationPrefs{})
	old := now.Add(-13 * time.Hour)
	st.session = notice
	st.session.EndedAt = &old
	if _ = s.Handle(context.Background(), ev); len(ap.sent) != 0 {
		t.Fatal("a session replayed long after is not announced")
	}
	s, st, ap = service(now, model.NotificationPrefs{QuietHours: &model.QuietHours{From: "18:00", To: "20:00"}})
	st.session = notice
	if _ = s.Handle(context.Background(), ev); len(ap.sent) != 0 {
		t.Fatal("quiet hours drop it")
	}
}

func TestCheckStale(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, berlin)
	s, st, ap := service(now, model.NotificationPrefs{})
	old, fresh := now.Add(-21*time.Hour), now.Add(-2*time.Hour)
	stale := st.recipient
	stale.LastSample = &old
	active := st.recipient
	active.ID, active.LastSample = uuid.New(), &fresh
	gone := st.recipient
	gone.ID, gone.LastSample = uuid.New(), nil
	st.stale = []store.Recipient{stale, active, gone}
	if err := s.CheckStale(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ap.sent) != 2 || !strings.Contains(string(ap.sent[0].Payload), `"loc-args":["21"]`) {
		t.Fatalf("only the stale user, both devices: %+v", ap.sent)
	}
	_ = s.CheckStale(context.Background())
	if len(ap.sent) != 2 {
		t.Fatal("once per day")
	}
	s.Now = func() time.Time { return time.Date(2026, 10, 19, 22, 0, 0, 0, berlin) }
	st.claims = map[string]int{}
	_ = s.CheckStale(context.Background())
	if len(ap.sent) != 2 {
		t.Fatal("not at night")
	}
}

// D92: a silent refresh after a sync's days are scored, at most one a user every
// RefreshEvery, shared with the morning one; not claimed per day.
func TestRefreshAfterSync(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, berlin)
	s, st, ap := service(now, model.NotificationPrefs{})
	clock := now
	s.Now = func() time.Time { return clock }
	ev := events.NotifyMessage{Refresh: &model.RefreshEvent{UserID: st.recipient.ID, Day: "2026-10-21"}}
	if err := s.Handle(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushBackground) != len(tokens) || len(st.claims) != 0 {
		t.Fatalf("one silent push per device, no daily claim: %d sent, claims %v", ap.count(apns.PushBackground), st.claims)
	}
	var payload map[string]any
	_ = json.Unmarshal(ap.sent[0].Payload, &payload)
	if r, _ := payload["ryft"].(map[string]any); r["kind"] != "refresh" || r["day"] != "2026-10-21" || ap.sent[0].CollapseID != "refresh-2026-10-21" {
		t.Fatalf("payload %s, collapse %s", ap.sent[0].Payload, ap.sent[0].CollapseID)
	}
	clock = now.Add(4 * time.Minute)
	_ = s.Handle(context.Background(), ev)
	if ap.count(apns.PushBackground) != len(tokens) {
		t.Fatal("within five minutes: throttled")
	}
	clock = now.Add(6 * time.Minute)
	_ = s.Handle(context.Background(), ev)
	if ap.count(apns.PushBackground) != 2*len(tokens) {
		t.Fatal("after five minutes: sent again")
	}
	// The morning push's own refresh counts too: a sync's refresh right after waits.
	s2, st2, ap2 := service(now, model.NotificationPrefs{})
	morning := morningEvent("2026-10-21")
	morning.Scored.UserID = st2.recipient.ID
	if err := s2.Handle(context.Background(), morning); err != nil {
		t.Fatal(err)
	}
	before := ap2.count(apns.PushBackground)
	_ = s2.Handle(context.Background(), events.NotifyMessage{Refresh: &model.RefreshEvent{UserID: st2.recipient.ID, Day: "2026-10-21"}})
	if ap2.count(apns.PushBackground) != before {
		t.Fatal("right after the morning refresh: throttled")
	}
}

// D100: without publish dedup a session.scored can arrive twice; the claim per user,
// day and category keeps it to one push (scored: TestMorningSendsOncePerDay; refresh:
// the 5-minute throttle in TestRefreshAfterSync).
func TestDuplicateSessionScoredSendsOnce(t *testing.T) {
	now := time.Date(2026, 10, 19, 19, 0, 0, 0, berlin)
	ended, strain := now.Add(-20*time.Minute), 11.2
	s, st, ap := service(now, model.NotificationPrefs{})
	st.session = store.SessionNotice{Title: "Upper A", Status: model.SessionCompleted, EndedAt: &ended, Strain: &strain}
	ev := events.NotifyMessage{SessionScored: &model.SessionScoredEvent{UserID: uuid.New(), SessionID: uuid.New(), Day: "2026-10-19"}}
	for range 3 {
		if err := s.Handle(context.Background(), ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(ap.sent) != len(tokens) {
		t.Fatalf("three deliveries, one push per device: %d sent", len(ap.sent))
	}
}
