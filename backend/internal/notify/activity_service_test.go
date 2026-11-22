package notify

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// ---- the ledger as a fake: what store.DecideWorkoutAlert does under its lock ----

func (f *fakeStore) ActivityCandidates(_ context.Context, _ uuid.UUID, _ time.Time) ([]store.ActivityCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.ActivityCandidate
	for _, c := range f.cands {
		decided := false
		for _, a := range f.ledger {
			decided = decided || a.ID == c.ID
		}
		if !decided {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeStore) DecideWorkoutAlert(_ context.Context, _ uuid.UUID, mark store.AlertMark,
	decide func([]store.AlertMark, int) string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sent := 0
	for _, a := range f.ledger {
		if a.Day == mark.Day && (a.Outcome == store.AlertSending || a.Outcome == store.AlertSent) {
			sent++
		}
	}
	outcome := decide(append([]store.AlertMark(nil), f.ledger...), sent)
	if outcome != store.AlertDuplicate {
		members := mark.Members
		mark.Outcome, mark.Members = outcome, nil
		f.ledger = append(f.ledger, mark)
		// As the store does: the workouts hidden behind it are decided with it, inherited, unless they have a row.
		for _, m := range members {
			if !slices.ContainsFunc(f.ledger, func(a store.AlertMark) bool { return a.ID == m.ID }) {
				f.ledger = append(f.ledger, store.AlertMark{WorkoutRef: m, Day: mark.Day, Outcome: store.AlertInherited})
			}
		}
	}
	return outcome, nil
}

func (f *fakeStore) FinishWorkoutAlert(ctx context.Context, _, workoutID uuid.UUID, outcome string, devices int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saw("FinishWorkoutAlert", ctx)
	for k := range f.ledger {
		if f.ledger[k].ID == workoutID && f.ledger[k].Outcome == store.AlertSending {
			f.ledger[k].Outcome = outcome
			if f.alertDevices == nil {
				f.alertDevices = map[uuid.UUID]int{}
			}
			f.alertDevices[workoutID] = devices
		}
	}
	return nil
}

func (f *fakeStore) ReleaseWorkoutAlert(ctx context.Context, _, workoutID uuid.UUID, memberIDs []uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saw("ReleaseWorkoutAlert", ctx)
	// As the store does: the sending row and, only with it, the members' inherited rows.
	released := false
	f.ledger = slices.DeleteFunc(f.ledger, func(a store.AlertMark) bool {
		if a.ID == workoutID && a.Outcome == store.AlertSending {
			released = true
			return true
		}
		return false
	})
	if released {
		f.ledger = slices.DeleteFunc(f.ledger, func(a store.AlertMark) bool {
			return a.Outcome == store.AlertInherited && slices.Contains(memberIDs, a.ID)
		})
	}
	return nil
}

// outcomes is the ledger's outcomes in decision order.
func (f *fakeStore) outcomes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.ledger))
	for k, a := range f.ledger {
		out[k] = a.Outcome
	}
	return out
}

// ---- scenarios ----

// A local evening: Tuesday 2026-10-20, 19:00 in Berlin.
var evening = time.Date(2026, 10, 20, 19, 0, 0, 0, berlin)

// ranAt is a candidate: a run of mins minutes that ended at end, strain 12.8, stored a minute ago.
func ranAt(end time.Time, mins int) store.ActivityCandidate {
	c := aCand()
	c.End, c.Start, c.CreatedAt = end, end.Add(-time.Duration(mins)*time.Minute), end
	return c
}

func refreshOf(st *fakeStore) events.NotifyMessage {
	return events.NotifyMessage{Refresh: &model.RefreshEvent{UserID: st.recipient.ID, Day: "2026-10-20"}}
}

// A delivered refresh twice (D100) sends once per device; the payload is the contract's.
func TestActivityRefreshSendsOnceAndIsIdempotent(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	clock := evening
	s.Now = func() time.Time { return clock }
	c := ranAt(evening.Add(-30*time.Minute), 45)
	st.cands = []store.ActivityCandidate{c}

	for _, delay := range []time.Duration{0, 0, 6 * time.Minute} { // the same event again, then once more after the throttle
		clock = evening.Add(delay)
		if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
			t.Fatal(err)
		}
	}
	if n := ap.count(apns.PushAlert); n != len(tokens) {
		t.Fatalf("%d alerts, want one per device (%d)", n, len(tokens))
	}
	if got := st.outcomes(); len(got) != 1 || got[0] != store.AlertSent || st.alertDevices[c.ID] != len(tokens) {
		t.Fatalf("ledger %v devices %v", got, st.alertDevices)
	}
	var alert apns.Notification
	for _, n := range ap.sent {
		if n.PushType == apns.PushAlert {
			alert = n
			break
		}
	}
	if want := "ACTIVITY_SCORED-" + c.ID.String(); alert.CollapseID != want || len(alert.CollapseID) != 52 {
		t.Errorf("collapse id %q", alert.CollapseID)
	}
	if want := c.End.Add(12 * time.Hour); !alert.Expiration.Equal(want) {
		t.Errorf("expires %v, want the end + 12 h", alert.Expiration)
	}
	var p struct {
		RYFT map[string]string `json:"ryft"`
	}
	if err := json.Unmarshal(alert.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.RYFT["account"] != st.recipient.ID.String() || p.RYFT["category"] != "ACTIVITY_SCORED" ||
		p.RYFT["day"] != "2026-10-20" || p.RYFT["url"] != "https://ryftlab.de/activity/"+c.ID.String() {
		t.Errorf("ryft keys %v", p.RYFT)
	}
	// The silent pushes carry the account too.
	for _, n := range ap.sent {
		if n.PushType == apns.PushBackground && !jsonHas(n.Payload, "account", st.recipient.ID.String()) {
			t.Errorf("silent push without the account: %s", n.Payload)
		}
	}
}

func jsonHas(payload []byte, key, want string) bool {
	var p struct {
		RYFT map[string]string `json:"ryft"`
	}
	return json.Unmarshal(payload, &p) == nil && p.RYFT[key] == want
}

// Five workouts on one local day: three alerts, two capped, in start order; the next day's goes out.
func TestActivityCapsThreeADayAndTheNextDayIsFree(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 30, 0, 0, berlin) // after midnight: the evening's runs are within 12 h
	s, st, ap := service(now, model.NotificationPrefs{})
	for k := range 5 {
		end := time.Date(2026, 10, 20, 19+k, 0, 0, 0, berlin)
		st.cands = append(st.cands, ranAt(end, 30))
	}
	st.cands = append(st.cands, ranAt(time.Date(2026, 10, 21, 0, 25, 0, 0, berlin), 20)) // starts on the 21st
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	want := []string{"sent", "sent", "sent", "capped", "capped", "sent"}
	if got := st.outcomes(); !equalStrings(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	if n := ap.count(apns.PushAlert); n != 4*len(tokens) {
		t.Fatalf("%d alerts, want four workouts to every device", n)
	}
	// The day on the capped rows is the local day of the start.
	if st.ledger[3].Day != "2026-10-20" || st.ledger[5].Day != "2026-10-21" {
		t.Errorf("days %s %s", st.ledger[3].Day, st.ledger[5].Day)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

// Quiet hours drop it, and nothing comes when they end; the category off is muted; neither counts toward the cap.
func TestActivityQuietAndMuted(t *testing.T) {
	night := time.Date(2026, 10, 20, 23, 30, 0, 0, berlin)
	s, st, ap := service(night, model.NotificationPrefs{QuietHours: &model.QuietHours{From: "22:00", To: "07:00"}})
	clock := night
	s.Now = func() time.Time { return clock }
	st.cands = []store.ActivityCandidate{ranAt(night.Add(-20*time.Minute), 45)}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	clock = time.Date(2026, 10, 21, 7, 30, 0, 0, berlin) // quiet hours are over: the workout was decided, so it stays dropped
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushAlert) != 0 || !equalStrings(st.outcomes(), []string{"quiet"}) {
		t.Fatalf("alerts %d, ledger %v", ap.count(apns.PushAlert), st.outcomes())
	}

	s, st, ap = service(evening, model.NotificationPrefs{Categories: map[string]bool{model.NotifyActivityScored: false}})
	for k := range 4 {
		st.cands = append(st.cands, ranAt(evening.Add(-time.Duration(k+1)*time.Hour), 30))
	}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushAlert) != 0 || !equalStrings(st.outcomes(), []string{"muted", "muted", "muted", "muted"}) {
		t.Fatalf("muted: alerts %d, ledger %v", ap.count(apns.PushAlert), st.outcomes())
	}
	// Switched on afterwards: those four are decided, a new one is not capped by them.
	st.recipient.Prefs = model.NotificationPrefs{Categories: map[string]bool{model.NotifyActivityScored: true}}
	st.cands = append(st.cands, ranAt(evening.Add(-10*time.Minute), 30))
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushAlert) != len(tokens) || st.outcomes()[4] != "sent" {
		t.Fatalf("after switching on: alerts %d, ledger %v", ap.count(apns.PushAlert), st.outcomes())
	}
}

// No registered device: no_token, decided; a token registered afterwards does not bring it back.
func TestActivityNoTokenIsDecided(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	st.recipient.Tokens = nil
	st.cands = []store.ActivityCandidate{ranAt(evening.Add(-time.Hour), 45)}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	st.recipient.Tokens = tokens
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushAlert) != 0 || !equalStrings(st.outcomes(), []string{"no_token"}) {
		t.Fatalf("alerts %d, ledger %v", ap.count(apns.PushAlert), st.outcomes())
	}
}

// History from a device first seen under an hour before is silent. A copy of a decided
// workout under a new id (a rewrite), or a primary that hides a decided one, inherits.
func TestActivityBackfillAndInheritance(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	seen := evening.Add(-3 * time.Hour)
	history := ranAt(evening.Add(-2*time.Hour), 40)
	history.CreatedAt, history.DeviceSeen = seen.Add(10*time.Minute), &seen
	device := uuid.New()
	history.DeviceID = &device
	st.cands = []store.ActivityCandidate{history}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushAlert) != 0 || !equalStrings(st.outcomes(), []string{"backfill"}) {
		t.Fatalf("backfill: alerts %d ledger %v", ap.count(apns.PushAlert), st.outcomes())
	}

	// W is sent. The same source writes it again under a new id: inherited, nothing more.
	w := ranAt(evening.Add(-time.Hour), 45)
	rewrite := w
	rewrite.ID = uuid.New()
	rewrite.CreatedAt = w.CreatedAt.Add(time.Minute)
	st.cands = []store.ActivityCandidate{w}
	_ = s.Handle(context.Background(), refreshOf(st))
	if got := st.outcomes(); len(got) != 2 || got[1] != "sent" {
		t.Fatalf("W: %v", got)
	}
	st.cands = []store.ActivityCandidate{w, rewrite}
	_ = s.Handle(context.Background(), refreshOf(st))
	if got := st.outcomes(); len(got) != 3 || got[2] != "inherited" {
		t.Fatalf("a rewrite under a new id: %v", got)
	}

	// G, another source's recording, becomes the primary and W is hidden behind it.
	g := ranAt(evening.Add(-30*time.Minute), 45)
	g.Source, g.Activity = "Other tracker", "running"
	g.Start, g.End = w.Start, w.End
	g.Members = []store.WorkoutRef{w.WorkoutRef}
	st.cands = []store.ActivityCandidate{w, rewrite, g}
	_ = s.Handle(context.Background(), refreshOf(st))
	if got := st.outcomes(); len(got) != 4 || got[3] != "inherited" {
		t.Fatalf("a primary hiding a decided workout: %v", got)
	}
	if n := ap.count(apns.PushAlert); n != len(tokens) {
		t.Fatalf("one alert in all, %d sent", n)
	}
}

// APNs unreachable: the claim is released and the event retried; the retry sends.
func TestActivityAPNsDownReleasesAndRetries(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	clock := evening
	s.Now = func() time.Time { return clock }
	st.cands = []store.ActivityCandidate{ranAt(evening.Add(-time.Hour), 45)}
	ap.down = true
	before := testutil.ToFloat64(metrics.PushNotifications.WithLabelValues(model.NotifyActivityScored, "failed"))
	if err := s.Handle(context.Background(), refreshOf(st)); err == nil {
		t.Fatal("APNs unreachable: the event is retried")
	}
	if len(st.outcomes()) != 0 {
		t.Fatalf("the claim is released: %v", st.outcomes())
	}
	if got := testutil.ToFloat64(metrics.PushNotifications.WithLabelValues(model.NotifyActivityScored, "failed")) - before; got != 1 {
		t.Fatalf("failed counted %v times", got)
	}
	ap.down = false
	clock = evening.Add(time.Minute)
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil || ap.count(apns.PushAlert) != len(tokens) {
		t.Fatalf("the retry sends: %v, %d alerts", err, ap.count(apns.PushAlert))
	}
}

// A workout with a copy from another source: the claim records the copy as inherited, and an APNs
// outage must give it back too, or the retry finds the copy in the ledger and never sends.
func TestActivityAPNsDownWithMembersReleasesTheGroupAndRetries(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	clock := evening
	s.Now = func() time.Time { return clock }
	c := ranAt(evening.Add(-time.Hour), 45)
	member := store.WorkoutRef{ID: uuid.New(), Source: "Fitbit", Activity: c.Activity, Start: c.Start.Add(20 * time.Second), End: c.End}
	c.Members = []store.WorkoutRef{member}
	st.cands = []store.ActivityCandidate{c}

	ap.down = true
	if err := s.Handle(context.Background(), refreshOf(st)); err == nil {
		t.Fatal("APNs unreachable: the event is retried")
	}
	if got := st.outcomes(); len(got) != 0 {
		t.Fatalf("neither the primary nor its member stays in the ledger: %v", got)
	}

	ap.down = false
	clock = evening.Add(time.Minute)
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if n := ap.count(apns.PushAlert); n != len(tokens) {
		t.Fatalf("the retry sends once: %d alerts, want %d", n, len(tokens))
	}
	if got := st.outcomes(); !equalStrings(got, []string{store.AlertSent, store.AlertInherited}) {
		t.Fatalf("ledger %v, want the primary sent and its member inherited", got)
	}
	if st.ledger[1].ID != member.ID {
		t.Errorf("the inherited row is %v, want the member %v", st.ledger[1].ID, member.ID)
	}
}

// Without an APNs key nothing is decided: when one is configured later the workout is still new.
func TestActivityWithoutAPNsDecidesNothing(t *testing.T) {
	s, st, _ := service(evening, model.NotificationPrefs{})
	s.APNs = nil
	st.cands = []store.ActivityCandidate{ranAt(evening.Add(-time.Hour), 45)}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil || len(st.outcomes()) != 0 {
		t.Fatalf("%v, ledger %v", err, st.outcomes())
	}
}

// The silent push waits its 5 minutes; the scan does not.
func TestActivityThrottledRefreshStillDecides(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	clock := evening
	s.Now = func() time.Time { return clock }
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	clock = evening.Add(2 * time.Minute)
	st.cands = []store.ActivityCandidate{ranAt(clock.Add(-10*time.Minute), 45)}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if ap.count(apns.PushBackground) != len(tokens) || ap.count(apns.PushAlert) != len(tokens) {
		t.Fatalf("silent %d (one round), alerts %d", ap.count(apns.PushBackground), ap.count(apns.PushAlert))
	}
}

// A workout that fails a rule leaves nothing behind: the next scan looks again (here the strain arrives).
func TestActivitySkipLeavesNoTrace(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	c := ranAt(evening.Add(-time.Hour), 45)
	c.Strain = nil
	st.cands = []store.ActivityCandidate{c}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil || len(st.outcomes()) != 0 || ap.count(apns.PushAlert) != 0 {
		t.Fatalf("unscored: %v ledger %v", err, st.outcomes())
	}
	st.cands[0].Strain = fptr(9.1)
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil || ap.count(apns.PushAlert) != len(tokens) {
		t.Fatalf("scored: %v, %d alerts", err, ap.count(apns.PushAlert))
	}
}

// A duplicate group is decided with its primary: W (the watch copy) is sent with G (another source's
// copy) hidden behind it. The user deletes W; G becomes visible with no members and no ledger row of
// its own, and must not alert again. The inherited row does not count toward the day's three.
func TestActivityGroupDecisionOutlivesItsPrimary(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	x1, x2 := ranAt(evening.Add(-4*time.Hour), 30), ranAt(evening.Add(-3*time.Hour), 30)
	x1.Activity, x2.Activity = "cycling", "walking"
	w := ranAt(evening.Add(-time.Hour), 45)
	w.Source = "Apple Watch"
	g := ranAt(evening.Add(-time.Hour), 45) // Garmin Connect, the same workout
	w.Members = []store.WorkoutRef{g.WorkoutRef}
	st.cands = []store.ActivityCandidate{x1, x2, w}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	// W is the third alert of its day: G's row (inherited) is not counted ahead of it.
	if got, want := st.outcomes(), []string{"sent", "sent", "sent", "inherited"}; !equalStrings(got, want) {
		t.Fatalf("outcomes %v, want %v", got, want)
	}
	if st.ledger[3].ID != g.ID || st.ledger[3].Source != "Garmin Connect" || !st.ledger[3].Start.Equal(g.Start) {
		t.Fatalf("G's row keeps its own identity: %+v", st.ledger[3])
	}

	// W is deleted: G is a candidate on its own, with no members.
	st.cands = []store.ActivityCandidate{x1, x2, g}
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	if got := st.outcomes(); len(got) != 4 {
		t.Fatalf("G was decided again: %v", got)
	}
	if n := ap.count(apns.PushAlert); n != 3*len(tokens) {
		t.Fatalf("%d alerts, want 3 workouts to every device", n)
	}

	// Even a scan that does hand G over (a reader without the ledger filter) decides nothing new.
	if got := ActivityOutcome(g, st.ledger, 0, "", ActivityDailyCap); got != store.AlertDuplicate {
		t.Errorf("G against the ledger: %q", got)
	}
}

// The release and the record of a send run on a context the caller's cancellation does not reach: when
// the Pub/Sub deadline or a shutdown ends ctx during the APNs send, a claim must not stay "sending".
func TestActivityClaimIsSettledAfterTheCallerIsCancelled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fails bool // the send fails with the context's error; else APNs had answered 200
		call  string
		want  []string // the ledger afterwards
	}{
		{"cancelled while APNs was unreachable: released", true, "ReleaseWorkoutAlert", nil},
		{"cancelled after APNs answered: recorded", false, "FinishWorkoutAlert", []string{"sent"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, ap := service(evening, model.NotificationPrefs{})
			st.cands = []store.ActivityCandidate{ranAt(evening.Add(-time.Hour), 45)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ap.cancelOnAlert, ap.cancelFails = cancel, tc.fails
			err := s.Handle(ctx, refreshOf(st))
			if ctx.Err() == nil {
				t.Fatal("the test's context was not cancelled")
			}
			if (err != nil) != tc.fails {
				t.Fatalf("error %v, want one: %v", err, tc.fails)
			}
			seen, ok := st.ctxSeen[tc.call]
			if !ok {
				t.Fatalf("%s was not called: %v", tc.call, st.ctxSeen)
			}
			if seen.err != nil || !seen.deadline {
				t.Errorf("%s got a context that is cancelled (%v) or has no timeout (%v)", tc.call, seen.err, seen.deadline)
			}
			if got := st.outcomes(); !equalStrings(got, tc.want) {
				t.Errorf("ledger %v, want %v", got, tc.want)
			}
		})
	}
}

// The same for the other categories' claim (deliver).
func TestDeliverClaimIsSettledAfterTheCallerIsCancelled(t *testing.T) {
	now := time.Date(2026, 10, 19, 8, 0, 0, 0, berlin)
	morning := key("2026-10-19", model.NotifyMorningScore)
	for _, tc := range []struct {
		name    string
		fails   bool
		call    string
		claimed bool // the claim is still there afterwards
	}{
		{"cancelled while APNs was unreachable: released", true, "ReleaseNotification", false},
		{"cancelled after APNs answered: recorded", false, "NotificationSent", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, ap := service(now, model.NotificationPrefs{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ap.cancelOnAlert, ap.cancelFails = cancel, tc.fails
			_ = s.Handle(ctx, morningEvent("2026-10-19"))
			if ctx.Err() == nil {
				t.Fatal("the test's context was not cancelled")
			}
			seen, ok := st.ctxSeen[tc.call]
			if !ok || seen.err != nil || !seen.deadline {
				t.Errorf("%s: called %v, context cancelled %v, has timeout %v", tc.call, ok, seen.err, seen.deadline)
			}
			if devices, claimed := st.claims[morning]; claimed != tc.claimed || (tc.claimed && devices != len(tokens)) {
				t.Errorf("claim %v (%d devices), want claimed %v", claimed, devices, tc.claimed)
			}
		})
	}
}

// A deleted account: the refresh is dropped for good (the transport does not retry it) and decides nothing.
func TestActivityRefreshOfADeletedAccountIsPermanent(t *testing.T) {
	s, st, ap := service(evening, model.NotificationPrefs{})
	st.cands = []store.ActivityCandidate{ranAt(evening.Add(-time.Hour), 45)}
	st.recipientErr = store.ErrNotFound
	err := s.Handle(context.Background(), refreshOf(st))
	if !events.IsPermanent(err) || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("error %v, want a permanent ErrNotFound", err)
	}
	if len(st.outcomes()) != 0 || len(ap.sent) != 0 {
		t.Fatalf("a deleted account: ledger %v, %d pushes", st.outcomes(), len(ap.sent))
	}
}

// The counts: one per decided workout, by outcome; a skip counts nothing.
func TestActivityCountsOutcomes(t *testing.T) {
	read := func(result string) float64 {
		return testutil.ToFloat64(metrics.PushNotifications.WithLabelValues(model.NotifyActivityScored, result))
	}
	results := []string{"sent", "capped", "muted", "backfill", "inherited", "no_token", "quiet"}
	before := map[string]float64{}
	for _, r := range results {
		before[r] = read(r)
	}
	s, st, _ := service(evening, model.NotificationPrefs{})
	for k := range 4 {
		st.cands = append(st.cands, ranAt(evening.Add(-time.Duration(k+1)*time.Hour), 30))
	}
	skipped := ranAt(evening.Add(-5*time.Hour), 30)
	skipped.Strain = nil
	st.cands = append(st.cands, skipped)
	if err := s.Handle(context.Background(), refreshOf(st)); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, r := range results {
		got[r] = read(r) - before[r]
	}
	if got["sent"] != 3 || got["capped"] != 1 || got["muted"]+got["backfill"]+got["inherited"]+got["no_token"]+got["quiet"] != 0 {
		t.Fatalf("counts %v", got)
	}
}
