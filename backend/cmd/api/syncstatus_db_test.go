package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/connect"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// GET /v1/me/sync through the api's own mux on a migrated database: the sync button's fields (D297, D298). The
// window rules alone are in syncstatus_freshness_test.go. Every time that matters (queued_at, received_at) is set
// in SQL, so no case depends on how long the test takes; the route's own 10 s cache is dropped for the account
// before each call (the cache stays 10 s).

type syncRig struct{ *connRig }

func newSyncRig(t *testing.T, mod ...func(*config.Beta, *config.AppAttest)) *syncRig {
	t.Helper()
	// The fixtures take their days from syncDay (the UTC clock at the time of the call) and the handler from its own
	// clock, with the account pinned to UTC: the two agree except across 00:00 UTC. A test runs far under a minute,
	// so it is skipped within a minute of midnight rather than made to share one clock.
	if now := time.Now().UTC(); now.Hour() == 23 && now.Minute() == 59 || now.Hour() == 0 && now.Minute() == 0 {
		t.Skip("within a minute of 00:00 UTC: today would change under the test")
	}
	return &syncRig{newConnRig(t, mod...)}
}

// user is a new UTC account, with consent to the policy unless noConsent.
func (s *syncRig) user(noConsent ...bool) uuid.UUID {
	s.t.Helper()
	id := s.account(noConsent...)
	tz := "UTC"
	if _, err := s.st.UpdateUser(context.Background(), id, model.UserPatch{TZ: &tz}, nil); err != nil {
		s.t.Fatal(err)
	}
	return id
}

// syncDay is the UTC day n days back.
func syncDay(n int) string { return time.Now().UTC().AddDate(0, 0, -n).Format(time.DateOnly) }

// forgetSyncStatus drops the account's cached answer (and only that: other tests may run beside this one).
func forgetSyncStatus(id uuid.UUID) {
	syncStatuses.mu.Lock()
	defer syncStatuses.mu.Unlock()
	delete(syncStatuses.entries, id)
}

// status calls the route, past the cache, and checks the shape every answer has (case 10): sources and
// pending_days are arrays, never null, healthkit first, every source with its four keys, the three new fields
// present, freshness in its closed set, since null exactly while current.
func (s *syncRig) status(u uuid.UUID) model.SyncStatus {
	s.t.Helper()
	forgetSyncStatus(u)
	w := s.do(u, http.MethodGet, "/v1/me/sync", nil)
	if w.Code != http.StatusOK {
		s.t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		s.t.Fatalf("%v: %s", err, w.Body)
	}
	for _, k := range []string{"gate", "sources", "freshness", "scores_pending_since", "pending_days"} {
		if _, ok := raw[k]; !ok {
			s.t.Fatalf("no %q in %s", k, w.Body)
		}
	}
	for _, k := range []string{"sources", "pending_days"} {
		if string(raw[k]) == "null" {
			s.t.Fatalf("%s is null: %s", k, w.Body)
		}
	}
	var src []map[string]json.RawMessage
	if err := json.Unmarshal(raw["sources"], &src); err != nil || len(src) == 0 || string(src[0]["source"]) != `"healthkit"` {
		s.t.Fatalf("sources must be an array that starts with healthkit: %s (%v)", raw["sources"], err)
	}
	for _, o := range src {
		for _, k := range []string{"source", "last_sync_at", "status", "reason"} {
			if _, ok := o[k]; !ok {
				s.t.Fatalf("a source without %q: %s", k, raw["sources"])
			}
		}
	}
	var st model.SyncStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		s.t.Fatal(err)
	}
	switch st.Freshness {
	case model.FreshnessCurrent:
		if st.ScoresPendingSince != nil {
			s.t.Fatalf("current with scores_pending_since %v", st.ScoresPendingSince)
		}
	case model.FreshnessScoresPending:
		if st.ScoresPendingSince == nil {
			s.t.Fatal("scores_pending without scores_pending_since")
		}
	default:
		s.t.Fatalf("freshness %q is outside the closed set", st.Freshness)
	}
	return st
}

// sourceIDs is the ids of st.sources in order.
func sourceIDs(st model.SyncStatus) []string {
	ids := make([]string, len(st.Sources))
	for i, x := range st.Sources {
		ids[i] = x.Source
	}
	return ids
}

// source is the entry of provider; it fails the test when there is none.
func (s *syncRig) source(st model.SyncStatus, provider string) model.SyncSource {
	s.t.Helper()
	for _, x := range st.Sources {
		if x.Source == provider {
			return x
		}
	}
	s.t.Fatalf("no source %q in %v", provider, sourceIDs(st))
	return model.SyncSource{}
}

// wantSources fails unless the sources are exactly ids, in order.
func (s *syncRig) wantSources(st model.SyncStatus, ids ...string) {
	s.t.Helper()
	if got := sourceIDs(st); !slices.Equal(got, ids) {
		s.t.Fatalf("sources %v, want %v", got, ids)
	}
}

// wantSince fails unless scores_pending_since is exactly want (nil: null).
func (s *syncRig) wantSince(st model.SyncStatus, want *time.Time) {
	s.t.Helper()
	switch {
	case want == nil && st.ScoresPendingSince != nil:
		s.t.Fatalf("scores_pending_since %v, want null", st.ScoresPendingSince)
	case want != nil && (st.ScoresPendingSince == nil || !st.ScoresPendingSince.Equal(*want)):
		s.t.Fatalf("scores_pending_since %v, want %v", st.ScoresPendingSince, want)
	}
}

// wantPendingDays fails unless pending_days is exactly days.
func (s *syncRig) wantPendingDays(st model.SyncStatus, days ...string) {
	s.t.Helper()
	if !slices.Equal(st.PendingDays, append([]string{}, days...)) {
		s.t.Fatalf("pending_days %v, want %v", st.PendingDays, days)
	}
}

// queue queues day for u as a sync does, then sets when it was queued (the window is on queued_at). It returns
// queued_at as the database holds it.
func (s *syncRig) queue(u uuid.UUID, day string, queuedAgo time.Duration) time.Time {
	s.t.Helper()
	if err := s.st.QueueDays(context.Background(), u, []string{day}); err != nil {
		s.t.Fatal(err)
	}
	return s.setQueuedAt(u, day, time.Now().Add(-queuedAgo))
}

func (s *syncRig) setQueuedAt(u uuid.UUID, day string, at time.Time) time.Time {
	s.t.Helper()
	var got time.Time
	if err := s.pool.QueryRow(context.Background(), `UPDATE score_queue SET queued_at = $3 WHERE user_id = $1 AND day = $2::date RETURNING queued_at`,
		u, day, at).Scan(&got); err != nil {
		s.t.Fatal(err)
	}
	return got.UTC()
}

func (s *syncRig) queuedAt(u uuid.UUID, day string) time.Time {
	s.t.Helper()
	var got time.Time
	if err := s.pool.QueryRow(context.Background(), `SELECT queued_at FROM score_queue WHERE user_id = $1 AND day = $2::date`, u, day).Scan(&got); err != nil {
		s.t.Fatal(err)
	}
	return got.UTC()
}

// scored marks day scored by a run that started now (what scoring does when it finishes a day).
func (s *syncRig) scored(u uuid.UUID, day string) {
	s.t.Helper()
	if _, ok, err := s.st.MarkScored(context.Background(), u, day, time.Now().Add(time.Second)); err != nil || !ok {
		s.t.Fatalf("mark %s scored: %v %v", day, ok, err)
	}
}

// link gives u a connection to provider in status (the confirm flow has its own tests) and returns the provider's
// user id; a revoked one has none.
func (s *syncRig) link(u uuid.UUID, provider, status string) string {
	s.t.Helper()
	puid := "puid-" + uuid.NewString()
	var stored any = puid
	if status == model.ConnectionRevoked {
		stored = nil
	}
	if _, err := s.pool.Exec(context.Background(), `INSERT INTO connections (user_id, provider, provider_user_id, status, permissions, policy_version)
		VALUES ($1, $2, $3, $4, '{}', $5)`, u, provider, stored, status, connPolicy); err != nil {
		s.t.Fatal(err)
	}
	return puid
}

type delivery struct {
	id       int64
	recordID string
	received time.Time // received_at as the database holds it
}

// deliver stores a provider's push as the hook does (a pending inbox row). receivedAgo, when positive, sets
// received_at in SQL.
func (s *syncRig) deliver(provider, puid string, receivedAgo time.Duration) delivery {
	s.t.Helper()
	ctx := context.Background()
	d := delivery{recordID: "rec-" + uuid.NewString()}
	outs, err := s.st.StoreInbox(ctx, provider, connPolicy, []store.HookItem{{Kind: connect.KindSleep, RecordID: d.recordID, ProviderUserID: puid,
		Version: 1, Payload: []byte(`{"n":1}`)}}, nil)
	if err != nil || len(outs) != 1 || outs[0] != store.HookStored {
		s.t.Fatalf("store the push: %v %v", outs, err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT id FROM provider_inbox WHERE provider = $1 AND kind = $2 AND record_id = $3`,
		provider, connect.KindSleep, d.recordID).Scan(&d.id); err != nil {
		s.t.Fatal(err)
	}
	if receivedAgo > 0 {
		if _, err := s.pool.Exec(ctx, `UPDATE provider_inbox SET received_at = $2 WHERE id = $1`, d.id, time.Now().Add(-receivedAgo)); err != nil {
			s.t.Fatal(err)
		}
	}
	if err := s.pool.QueryRow(ctx, `SELECT received_at FROM provider_inbox WHERE id = $1`, d.id).Scan(&d.received); err != nil {
		s.t.Fatal(err)
	}
	d.received = d.received.UTC()
	return d
}

// 1. Nothing synced: healthkit alone, nothing yet, ok; current; no time.
func TestSyncStatusFreshAccount(t *testing.T) {
	rg := newSyncRig(t)
	u := rg.user()
	st := rg.status(u)
	rg.wantSources(st, "healthkit")
	if hk := st.Sources[0]; hk.LastSyncAt != nil || hk.Status != model.SyncSourceOK || hk.Reason != nil {
		t.Fatalf("healthkit before the first sync: %+v", hk)
	}
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("freshness %s", st.Freshness)
	}
	rg.wantSince(st, nil)
	rg.wantPendingDays(st)
	if st.Gate != nil {
		t.Fatalf("gate %v", *st.Gate)
	}
	// The wire form, not only the decoded one: the four keys, nulls spelled out.
	forgetSyncStatus(u)
	var body struct {
		Sources json.RawMessage `json:"sources"`
	}
	if err := json.Unmarshal(rg.do(u, http.MethodGet, "/v1/me/sync", nil).Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, body.Sources); err != nil || compact.String() != `[{"source":"healthkit","last_sync_at":null,"status":"ok","reason":null}]` {
		t.Fatalf("sources on the wire: %s (%v)", compact.String(), err)
	}

	// After a sync the source carries the route's last_sync_at.
	if err := rg.st.TouchDevice(context.Background(), u, uuid.New(), "iPhone", nil, nil); err != nil {
		t.Fatal(err)
	}
	st = rg.status(u)
	if st.LastSyncAt == nil || st.Sources[0].LastSyncAt == nil || !st.Sources[0].LastSyncAt.Equal(*st.LastSyncAt) {
		t.Fatalf("healthkit last_sync_at %v, the route's %v", st.Sources[0].LastSyncAt, st.LastSyncAt)
	}
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("a sync that queued nothing holds nothing: %s", st.Freshness)
	}
}

// 2. A sync queued today holds the button, since is its queued_at; scoring it releases the button; a re-queue after
// the run started stays pending.
func TestSyncStatusQueuedToday(t *testing.T) {
	rg := newSyncRig(t)
	u := rg.user()
	today := syncDay(0)
	queued := rg.queue(u, today, time.Minute)

	st := rg.status(u)
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("freshness %s, want scores_pending", st.Freshness)
	}
	rg.wantSince(st, &queued)
	rg.wantPendingDays(st, today)
	if st.Today.State != model.ScoringPending {
		t.Fatalf("today %s", st.Today.State)
	}

	rg.scored(u, today)
	st = rg.status(u)
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("after scoring: %s", st.Freshness)
	}
	rg.wantSince(st, nil)
	rg.wantPendingDays(st)

	// A sync of the day again while a run is in flight: the run's mark does not finish it.
	rg.queue(u, today, time.Minute)
	started := time.Now()
	requeued := rg.setQueuedAt(u, today, started.Add(time.Second))
	if _, ok, err := rg.st.MarkScored(context.Background(), u, today, started); err != nil || ok {
		t.Fatalf("a run that started before the re-queue must not finish it: %v %v", ok, err)
	}
	st = rg.status(u)
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("after a re-queue during the run: %s", st.Freshness)
	}
	rg.wantSince(st, &requeued) // "a day queued again restarts its time"
	rg.wantPendingDays(st, today)

	// Yesterday holds the button as well, whatever the age of its row.
	rg.scored(u, today) // now(): started later than the re-queue
	yesterday := syncDay(1)
	oldQueued := rg.queue(u, yesterday, 3*24*time.Hour)
	st = rg.status(u)
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("yesterday queued 3 days ago: %s", st.Freshness)
	}
	rg.wantSince(st, &oldQueued)
}

// 3. A late past day: its rescore reaches today, so it holds the button for 10 minutes, no longer; 30 days back is
// the limit. pending_days keeps listing every day, held or not.
func TestSyncStatusLatePastDay(t *testing.T) {
	rg := newSyncRig(t)
	for _, c := range []struct {
		name   string
		back   int
		ago    time.Duration
		holds  bool
		reason string
	}{
		{"10 days back, queued now", 10, 0, true, "its rescore reaches today"},
		{"10 days back, 9 minutes ago", 10, 9 * time.Minute, true, "inside the window"},
		{"10 days back, 11 minutes ago", 10, 11 * time.Minute, false, "the run had its time"},
		{"30 days back, queued now", 30, 0, true, "the oldest day a rescore of the window reaches"},
		{"31 days back, queued now", 31, 0, false, "its rescore stops before today"},
		{"40 days back, queued now", 40, 0, false, "far past"},
		{"yesterday, queued 3 days ago", 1, 3 * 24 * time.Hour, true, "yesterday at any age"},
		{"today, queued 3 days ago", 0, 3 * 24 * time.Hour, true, "today at any age"},
		{"2 days back, queued 3 days ago", 2, 3 * 24 * time.Hour, false, "a stuck day"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rg.t = t
			u := rg.user()
			day := syncDay(c.back)
			queued := rg.queue(u, day, c.ago)
			st := rg.status(u)
			rg.wantPendingDays(st, day)
			if c.holds {
				if st.Freshness != model.FreshnessScoresPending {
					t.Fatalf("current, want scores_pending (%s)", c.reason)
				}
				rg.wantSince(st, &queued)
				return
			}
			if st.Freshness != model.FreshnessCurrent {
				t.Fatalf("scores_pending, want current (%s)", c.reason)
			}
			rg.wantSince(st, nil)
		})
	}

	// The same row, later: it stops holding the button once its window has passed, and stays in pending_days.
	rg.t = t
	u := rg.user()
	day := syncDay(10)
	rg.queue(u, day, 0)
	if st := rg.status(u); st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("queued now: %s", st.Freshness)
	}
	rg.setQueuedAt(u, day, time.Now().Add(-11*time.Minute))
	st := rg.status(u)
	rg.wantPendingDays(st, day)
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("the same row 11 minutes later: %s", st.Freshness)
	}
}

// 4. A connected provider whose delivery is waiting: pending, since is received_at; stored (ApplyDirect): ok with
// the new last_sync_at, the day pending since its queue time; scored: current.
func TestSyncStatusProviderDelivery(t *testing.T) {
	rg := newSyncRig(t)
	ctx := context.Background()
	u := rg.user()
	puid := rg.link(u, "fake", model.ConnectionConnected)

	st := rg.status(u)
	rg.wantSources(st, "healthkit", "fake")
	if x := rg.source(st, "fake"); x.Status != model.SyncSourceOK || x.LastSyncAt != nil || x.Reason != nil {
		t.Fatalf("connected, nothing delivered: %+v", x)
	}
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("a connection alone holds nothing: %s", st.Freshness)
	}

	d := rg.deliver("fake", puid, 0)
	st = rg.status(u)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourcePending || x.LastSyncAt != nil || x.Reason != nil {
		t.Fatalf("a delivery waiting: %+v", x)
	}
	if hk := rg.source(st, "healthkit"); hk.Status != model.SyncSourceOK {
		t.Fatalf("healthkit: %+v", hk)
	}
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("a source pending: %s", st.Freshness)
	}
	rg.wantSince(st, &d.received)
	rg.wantPendingDays(st)

	// Ingest stores it: one night ending today.
	today := syncDay(0)
	start, err := time.Parse(time.DateOnly, today)
	if err != nil {
		t.Fatal(err)
	}
	res, err := rg.st.ApplyDirect(ctx, store.DirectRecord{InboxID: d.id, Revision: 1, UserID: u, Provider: "fake", Kind: connect.KindSleep,
		RecordID: d.recordID, MinPolicy: connPolicy, NightState: model.RecordFinal, Loc: time.UTC,
		Sleep: []model.SleepSegment{{Start: start.Add(30 * time.Minute), End: start.Add(150 * time.Minute), Stage: model.SleepCore}}})
	if err != nil || res.Outcome != store.DirectApplied || !slices.Equal(res.Days, []string{today}) {
		t.Fatalf("apply: %+v %v", res, err)
	}
	var lastData time.Time
	if err := rg.pool.QueryRow(ctx, `SELECT last_data_at FROM connections WHERE user_id = $1 AND provider = 'fake'`, u).Scan(&lastData); err != nil {
		t.Fatal(err)
	}
	queued := rg.queuedAt(u, today)

	st = rg.status(u)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourceOK || x.LastSyncAt == nil || !x.LastSyncAt.Equal(lastData) {
		t.Fatalf("stored: %+v, want ok with last_data_at %v", x, lastData)
	}
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("the stored day waits for its score: %s", st.Freshness)
	}
	rg.wantSince(st, &queued)
	rg.wantPendingDays(st, today)

	rg.scored(u, today)
	st = rg.status(u)
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("scored: %s", st.Freshness)
	}
	rg.wantSince(st, nil)
	rg.wantPendingDays(st)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourceOK || x.LastSyncAt == nil || !x.LastSyncAt.Equal(lastData) {
		t.Fatalf("after scoring: %+v", x)
	}
}

// 4, the edges: the earliest waiting delivery counts, across rows and across providers.
func TestSyncStatusEarliestWaitingDelivery(t *testing.T) {
	rg := newSyncRig(t)
	u := rg.user()
	rg.flag("garmin", u)
	fakeID := rg.link(u, "fake", model.ConnectionConnected)
	garminID := rg.link(u, "garmin", model.ConnectionConnected)

	rg.deliver("fake", fakeID, 2*time.Minute)
	first := rg.deliver("fake", fakeID, 5*time.Minute)
	garmin := rg.deliver("garmin", garminID, 4*time.Minute)

	st := rg.status(u)
	rg.wantSources(st, "healthkit", "fake", "garmin")
	for _, id := range []string{"fake", "garmin"} {
		if x := rg.source(st, id); x.Status != model.SyncSourcePending {
			t.Fatalf("%s: %+v", id, x)
		}
	}
	rg.wantSince(st, &first.received)

	// The earliest one is stored (here: failed); the next earliest is garmin's.
	if _, err := rg.pool.Exec(context.Background(), `UPDATE provider_inbox SET state = 'failed', error = 'test_failed' WHERE id = $1`, first.id); err != nil {
		t.Fatal(err)
	}
	st = rg.status(u)
	rg.wantSince(st, &garmin.received)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourcePending {
		t.Fatalf("fake still has a delivery waiting: %+v", x)
	}
}

// 5. needs_reauth is an error with its reason, revoked is absent, a failed delivery changes nothing.
func TestSyncStatusProviderStatuses(t *testing.T) {
	rg := newSyncRig(t)
	ctx := context.Background()
	u := rg.user()
	puid := rg.link(u, "fake", model.ConnectionConnected)

	// A failed inbox row is not waiting: the source stays ok and nothing is pending.
	failed := rg.deliver("fake", puid, time.Minute)
	if _, err := rg.pool.Exec(ctx, `UPDATE provider_inbox SET state = 'failed', error = 'test_failed' WHERE id = $1`, failed.id); err != nil {
		t.Fatal(err)
	}
	st := rg.status(u)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourceOK || x.Reason != nil {
		t.Fatalf("a failed delivery: %+v", x)
	}
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("a failed delivery holds nothing: %s", st.Freshness)
	}
	rg.wantSince(st, nil)

	if _, err := rg.pool.Exec(ctx, `UPDATE connections SET status = 'needs_reauth', status_reason = 'refresh_failed' WHERE user_id = $1 AND provider = 'fake'`, u); err != nil {
		t.Fatal(err)
	}
	st = rg.status(u)
	rg.wantSources(st, "healthkit", "fake")
	if x := rg.source(st, "fake"); x.Status != model.SyncSourceError || x.Reason == nil || *x.Reason != model.SyncReasonNeedsReauth {
		t.Fatalf("needs_reauth: %+v", x)
	}
	if hk := rg.source(st, "healthkit"); hk.Status != model.SyncSourceOK {
		t.Fatalf("a provider's error is not healthkit's: %+v", hk)
	}
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("an error is not scores pending: %s", st.Freshness)
	}

	// A waiting delivery of a needs_reauth connection: pending, as API.md's table says ("connected or
	// needs_reauth, and a delivery has arrived that ingest has not stored yet"), and it holds freshness.
	waiting := rg.deliver("fake", puid, time.Minute)
	st = rg.status(u)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourcePending || x.Reason != nil {
		t.Fatalf("needs_reauth with a delivery waiting: %+v", x)
	}
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("a waiting delivery of a needs_reauth connection: %s", st.Freshness)
	}
	rg.wantSince(st, &waiting.received)
	// Stored (processed): the error again.
	if _, err := rg.pool.Exec(ctx, `UPDATE provider_inbox SET state = 'processed', payload = NULL WHERE id = $1`, waiting.id); err != nil {
		t.Fatal(err)
	}
	st = rg.status(u)
	if x := rg.source(st, "fake"); x.Status != model.SyncSourceError || x.Reason == nil || *x.Reason != model.SyncReasonNeedsReauth {
		t.Fatalf("needs_reauth once the delivery is stored: %+v", x)
	}

	if _, err := rg.pool.Exec(ctx, `UPDATE connections SET status = 'revoked', status_reason = 'provider_revoked', provider_user_id = NULL, permissions = '{}'
		WHERE user_id = $1 AND provider = 'fake'`, u); err != nil {
		t.Fatal(err)
	}
	st = rg.status(u)
	rg.wantSources(st, "healthkit")
}

// 6. A row exists but the provider is not listed: its flag is off for the account, or this server does not serve it.
func TestSyncStatusProviderListing(t *testing.T) {
	rg := newSyncRig(t)
	u, other := rg.user(), rg.user()
	garminID := rg.link(u, "garmin", model.ConnectionConnected)
	rg.link(u, "suunto", model.ConnectionConnected) // not in the registry: not served
	rg.link(other, "garmin", model.ConnectionConnected)

	// The shipped flag is dark: absent although the row exists.
	rg.wantSources(rg.status(u), "healthkit")

	// A pending delivery of a provider that is not listed holds nothing.
	rg.deliver("garmin", garminID, time.Minute)
	if st := rg.status(u); st.Freshness != model.FreshnessCurrent {
		t.Fatalf("a delivery of an unlisted provider: %s", st.Freshness)
	}

	// On for this account only (the allow-list): listed for u, not for the other account.
	rg.flag("garmin", u)
	st := rg.status(u)
	rg.wantSources(st, "healthkit", "garmin")
	if x := rg.source(st, "garmin"); x.Status != model.SyncSourcePending {
		t.Fatalf("garmin with its delivery now listed: %+v", x)
	}
	rg.wantSources(rg.status(other), "healthkit")

	// Off again.
	rg.flag("garmin")
	rg.wantSources(rg.status(u), "healthkit")

	// On for everyone (100 %): both.
	if _, _, err := rg.st.PutFlag(context.Background(), model.FeatureFlag{Key: "connect.garmin", Enabled: true, RolloutPct: 100}); err != nil {
		t.Fatal(err)
	}
	rg.wantSources(rg.status(other), "healthkit", "garmin")
	// suunto stays absent: the server does not serve it, whatever its flag.
	if _, _, err := rg.st.PutFlag(context.Background(), model.FeatureFlag{Key: "connect.suunto", Enabled: true, RolloutPct: 100}); err != nil {
		t.Fatal(err)
	}
	rg.wantSources(rg.status(u), "healthkit", "garmin")
}

// 7. The gates through the route: each against its own mode; `gate` as before (the first failing gate, any mode).
func TestSyncStatusGates(t *testing.T) {
	type want struct {
		reason string // healthkit's reason; "" is ok
		gate   string // the route's `gate`; "" is null
	}
	run := func(t *testing.T, mod func(*config.Beta, *config.AppAttest), cases []struct {
		name            string
		invite, consent bool
		want            want
	}) {
		rg := newSyncRig(t, mod)
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				rg.t = t
				var u uuid.UUID
				if c.consent {
					u = rg.user()
				} else {
					u = rg.user(true) // no consent
				}
				if c.invite {
					if err := rg.st.SetBetaAccess(context.Background(), u, true); err != nil {
						t.Fatal(err)
					}
				}
				st := rg.status(u)
				hk := st.Sources[0]
				if c.want.reason == "" {
					if hk.Status != model.SyncSourceOK || hk.Reason != nil {
						t.Fatalf("healthkit %+v, want ok", hk)
					}
				} else if hk.Status != model.SyncSourceError || hk.Reason == nil || *hk.Reason != c.want.reason {
					t.Fatalf("healthkit %+v, want error/%s", hk, c.want.reason)
				}
				switch {
				case c.want.gate == "" && st.Gate != nil:
					t.Fatalf("gate %q, want null", *st.Gate)
				case c.want.gate != "" && (st.Gate == nil || *st.Gate != c.want.gate):
					t.Fatalf("gate %v, want %q", st.Gate, c.want.gate)
				}
				if st.Freshness != model.FreshnessCurrent {
					t.Fatalf("a gate is not scores pending: %s", st.Freshness)
				}
			})
		}
	}
	type cases = []struct {
		name            string
		invite, consent bool
		want            want
	}
	t.Run("beta on, consent on", func(t *testing.T) {
		run(t, func(b *config.Beta, _ *config.AppAttest) { b.Gate, b.Consent = config.ModeOn, config.ModeOn }, cases{
			{"no invite, no consent: the invite first", false, false, want{model.CodeInviteRequired, model.CodeInviteRequired}},
			{"no invite, consent", false, true, want{model.CodeInviteRequired, model.CodeInviteRequired}},
			{"invite, no consent", true, false, want{model.CodeConsentRequired, model.CodeConsentRequired}},
			{"invite, consent", true, true, want{"", ""}},
		})
	})
	t.Run("beta warn, consent on", func(t *testing.T) {
		run(t, func(b *config.Beta, _ *config.AppAttest) { b.Gate, b.Consent = config.ModeWarn, config.ModeOn }, cases{
			// The invite is only warned about (gate), the consent is enforced (status).
			{"no invite, no consent", false, false, want{model.CodeConsentRequired, model.CodeInviteRequired}},
			{"no invite, consent", false, true, want{"", model.CodeInviteRequired}},
			{"invite, no consent", true, false, want{model.CodeConsentRequired, model.CodeConsentRequired}},
		})
	})
	t.Run("both warn", func(t *testing.T) {
		run(t, func(b *config.Beta, _ *config.AppAttest) { b.Gate, b.Consent = config.ModeWarn, config.ModeWarn }, cases{
			{"no invite, no consent: ok, the gate says what is missing", false, false, want{"", model.CodeInviteRequired}},
			{"invite, no consent", true, false, want{"", model.CodeConsentRequired}},
			{"invite, consent", true, true, want{"", ""}},
		})
	})
}

// 8. Not tracked (D297): an account with Nutrition-to-Recovery whose morning baseline is older than today's score
// and a coaching delivery nobody processed yet, with nothing queued: current. The pilot's partner link is no source.
func TestSyncStatusDoesNotTrackN2RAndCoaching(t *testing.T) {
	rg := newSyncRig(t)
	ctx := context.Background()
	u := rg.user()
	today := syncDay(0)
	for _, q := range []string{
		`INSERT INTO nutrition.n2r_profiles (user_id, body_mass_kg, sex) VALUES ($1, 70, 'male')`,
		// The baseline of today's wake day, computed three days ago: older than today's score.
		`INSERT INTO nutrition.daily_biometric_baselines (user_id, wake_date, change, alcohol_g_kg, late_meal_penalty,
			effective_caffeine_half_life_h, n2r_score_breakdown, processed_at)
			VALUES ($1, $2::date, 'stable', 0, 0, 5, '{}'::jsonb, now() - interval '3 days')`,
		`INSERT INTO daily_scores (user_id, day, sleep_actual_min, computed_at) VALUES ($1, $2::date, 420, now())`,
		`INSERT INTO nutrition.user_goal_profiles (user_id, settings, goal, active_from) VALUES ($1, '{}'::jsonb, 'HYPERTROPHY', $2::date)`,
		`INSERT INTO nutrition.wearable_links (provider, provider_user, user_id) VALUES ('garmin', 'fixture-pilot', $1)`,
		`INSERT INTO nutrition.wearable_inbox (provider, provider_user, digest, body) VALUES ('garmin', 'fixture-pilot', '\x01', '{"n":1}'::jsonb)`,
	} {
		// Only the parameters the statement names: the inbox row has none (it is keyed by provider_user).
		var args []any
		if strings.Contains(q, "$1") {
			args = append(args, u)
		}
		if strings.Contains(q, "$2") {
			args = append(args, today)
		}
		if _, err := rg.pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var open int
	if err := rg.pool.QueryRow(ctx, `SELECT count(*) FROM nutrition.wearable_inbox WHERE completed_at IS NULL`).Scan(&open); err != nil || open != 1 {
		t.Fatalf("the fixture's open delivery: %d %v", open, err)
	}

	st := rg.status(u)
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("the baseline and the coaching delivery are not tracked: %s", st.Freshness)
	}
	rg.wantSince(st, nil)
	rg.wantPendingDays(st)
	rg.wantSources(st, "healthkit")
}

// 9. The invariant over a set of fixtures: freshness is scores_pending exactly when a queued day holds the button
// or a source is pending; the source is pending exactly when a delivery waits; since is the earliest of what waits.
func TestSyncStatusFreshnessInvariant(t *testing.T) {
	rg := newSyncRig(t)
	type queuedDay struct {
		back  int
		ago   time.Duration
		holds bool // the intent of the fixture, not the implementation's answer
	}
	cases := []struct {
		name     string
		days     []queuedDay
		inbox    []time.Duration // waiting deliveries of the connected fake, received this long ago; nil: no connection
		linked   bool
		wantDays bool
		wantSrc  bool
	}{
		{name: "nothing"},
		{name: "a connection, nothing waits", linked: true},
		{name: "today", days: []queuedDay{{0, 2 * time.Minute, true}}, wantDays: true},
		{name: "yesterday, an old row", days: []queuedDay{{1, 2 * 24 * time.Hour, true}}, wantDays: true},
		{name: "two days back, an old row", days: []queuedDay{{2, 2 * 24 * time.Hour, false}}},
		{name: "10 days back, fresh", days: []queuedDay{{10, time.Minute, true}}, wantDays: true},
		{name: "10 days back, stale", days: []queuedDay{{10, 11 * time.Minute, false}}},
		{name: "40 days back, fresh", days: []queuedDay{{40, 0, false}}},
		{name: "a delivery waits", linked: true, inbox: []time.Duration{30 * time.Second}, wantSrc: true},
		{name: "a delivery waits, a far day is queued", linked: true, inbox: []time.Duration{time.Minute}, days: []queuedDay{{40, 0, false}}, wantSrc: true},
		{name: "a delivery waits, a stuck day", linked: true, inbox: []time.Duration{time.Minute}, days: []queuedDay{{2, 3 * time.Hour, false}}, wantSrc: true},
		{name: "a delivery older than today's queue", linked: true, inbox: []time.Duration{5 * time.Minute}, days: []queuedDay{{0, 2 * time.Minute, true}},
			wantDays: true, wantSrc: true},
		{name: "a day older than the delivery", linked: true, inbox: []time.Duration{time.Minute}, days: []queuedDay{{0, 4 * time.Minute, true}, {10, 20 * time.Minute, false}},
			wantDays: true, wantSrc: true},
		{name: "two deliveries and two days", linked: true, inbox: []time.Duration{3 * time.Minute, 90 * time.Second},
			days: []queuedDay{{1, time.Hour, true}, {0, time.Minute, true}}, wantDays: true, wantSrc: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rg.t = t
			u := rg.user()
			var waiting []time.Time // what holds the button: its queued_at / received_at
			for _, d := range c.days {
				queued := rg.queue(u, syncDay(d.back), d.ago)
				if d.holds {
					waiting = append(waiting, queued)
				}
			}
			if c.linked {
				puid := rg.link(u, "fake", model.ConnectionConnected)
				for _, ago := range c.inbox {
					waiting = append(waiting, rg.deliver("fake", puid, ago).received)
				}
			}
			st := rg.status(u)
			if got, want := st.Freshness == model.FreshnessScoresPending, c.wantDays || c.wantSrc; got != want {
				t.Fatalf("freshness %s, want scores_pending=%v (days hold %v, source pending %v)", st.Freshness, want, c.wantDays, c.wantSrc)
			}
			if c.linked {
				if got := rg.source(st, "fake").Status == model.SyncSourcePending; got != c.wantSrc {
					t.Fatalf("fake pending %v, want %v", got, c.wantSrc)
				}
			}
			if !c.wantDays && !c.wantSrc {
				rg.wantSince(st, nil)
				return
			}
			earliest := slices.MinFunc(waiting, func(a, b time.Time) int { return a.Compare(b) })
			rg.wantSince(st, &earliest)
		})
	}
}

// Withdrawn consent (D298, the user's answer 2): the queued days of an account that scoring will not score stay in
// pending_days until scoring closes them; once it has, they hold nothing, and healthkit says why.
func TestSyncStatusWithdrawnConsent(t *testing.T) {
	rg := newSyncRig(t) // consent on
	ctx := context.Background()
	u, bystander := rg.user(), rg.user()
	today, yesterday := syncDay(0), syncDay(1)
	rg.queue(u, today, time.Minute)
	rg.queue(u, yesterday, 10*time.Minute)
	rg.queue(bystander, today, time.Minute)

	if err := rg.st.RevokeConsent(ctx, u); err != nil {
		t.Fatal(err)
	}
	st := rg.status(u)
	if st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("until scoring has seen the withdrawal the days wait: %s", st.Freshness)
	}
	if hk := st.Sources[0]; hk.Status != model.SyncSourceError || hk.Reason == nil || *hk.Reason != model.CodeConsentRequired {
		t.Fatalf("healthkit after the withdrawal: %+v", hk)
	}

	n, err := rg.st.CloseQueue(ctx, u, store.QueueClosedNoConsent, time.Now().Add(time.Minute))
	if err != nil || n != 2 {
		t.Fatalf("close: %d %v", n, err)
	}
	st = rg.status(u)
	if st.Freshness != model.FreshnessCurrent {
		t.Fatalf("closed days hold nothing: %s", st.Freshness)
	}
	rg.wantSince(st, nil)
	rg.wantPendingDays(st)
	if hk := st.Sources[0]; hk.Status != model.SyncSourceError || hk.Reason == nil || *hk.Reason != model.CodeConsentRequired {
		t.Fatalf("healthkit still says why: %+v", hk)
	}
	if st.Today.State == model.ScoringPending {
		t.Fatalf("today is not pending for a day that will not be scored")
	}

	// Another account's days are its own.
	if other := rg.status(bystander); other.Freshness != model.FreshnessScoresPending {
		t.Fatalf("the other account: %s", other.Freshness)
	}

	// A sync after the withdrawal queues the day again; it waits until scoring closes it again.
	rg.queue(u, today, time.Second)
	if st = rg.status(u); st.Freshness != model.FreshnessScoresPending {
		t.Fatalf("a day queued again: %s", st.Freshness)
	}
}
