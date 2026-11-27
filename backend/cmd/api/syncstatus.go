package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
)

// staleAfter: no new record for this long makes today stale (D91).
const staleAfter = 20 * time.Hour

// todayState is today's scoring state: pending while a sync's days wait for
// scoring (or recent data has no score yet), stale without data for staleAfter,
// waiting_for_sleep until a night arrived, scored otherwise.
func todayState(st store.SyncState, today string, now time.Time) string {
	newest := st.NewestRecord()
	switch {
	case st.PendingSet()[today]:
		return model.ScoringPending
	case newest.IsZero() || now.Sub(newest) > staleAfter:
		return model.ScoringStale
	case st.TodayScoredAt == nil:
		return model.ScoringPending
	case !st.TodayHasSleep:
		return model.ScoringWaitingForSleep
	}
	return model.ScoringScored
}

// gateFor is the gate between the user and their scores, also in warn mode
// (GET /v1/me/sync → gate): nil when none.
func (h *handler) gateFor(u model.User) *string {
	code := ""
	switch {
	case h.beta.Gate != config.ModeOff && !u.BetaAccess:
		code = model.CodeInviteRequired
	case h.beta.Consent != config.ModeOff && !gate.Consented(h.beta, u):
		code = model.CodeConsentRequired
	default:
		return nil
	}
	return &code
}

// localToday is the user's today and its location.
func (h *handler) localToday(r *http.Request) (string, *time.Location, error) {
	loc, err := h.location(r)
	if err != nil {
		return "", nil, err
	}
	return time.Now().In(loc).Format(dayLayout), loc, nil
}

// withScoringStates sets each row's scoring_state: pending, today's state, or scored.
func (h *handler) withScoringStates(ctx context.Context, userID uuid.UUID, today string, rows []model.DailyScore) error {
	st, err := h.st.SyncStateOf(ctx, userID, today)
	if err != nil {
		return err
	}
	pending := st.PendingSet()
	for k := range rows {
		switch {
		case rows[k].Day == today:
			rows[k].ScoringState = todayState(st, today, time.Now())
		case pending[rows[k].Day]:
			rows[k].ScoringState = model.ScoringPending
		default:
			rows[k].ScoringState = model.ScoringScored
		}
	}
	return nil
}

// syncCacheFor is how long GET /v1/me/sync answers from memory.
const syncCacheFor = 10 * time.Second

type syncCache struct {
	mu      sync.Mutex
	entries map[uuid.UUID]syncEntry
}

type syncEntry struct {
	at     time.Time
	status model.SyncStatus
}

var syncStatuses = &syncCache{entries: map[uuid.UUID]syncEntry{}}

func (c *syncCache) get(id uuid.UUID, now time.Time) (model.SyncStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	return e.status, ok && now.Sub(e.at) < syncCacheFor
}

func (c *syncCache) put(id uuid.UUID, now time.Time, s model.SyncStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 10_000 {
		clear(c.entries)
	}
	c.entries[id] = syncEntry{at: now, status: s}
}

// syncStatus is GET /v1/me/sync (D91).
func (h *handler) syncStatus(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	now := time.Now()
	if s, ok := syncStatuses.get(u.ID, now); ok {
		httpx.WriteJSON(w, 200, s)
		return
	}
	today, _, err := h.localToday(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	ctx := r.Context()
	st, err := h.st.SyncStateOf(ctx, u.ID, today)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	expected, err := h.st.ExpectedScoreSec(ctx)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	sources, srcSince, err := h.syncSources(ctx, u, st)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pending, since := syncFreshness(st.Queued, today, now)
	if srcSince != nil {
		pending = true
		if since == nil || srcSince.Before(*since) {
			since = srcSince
		}
	}
	freshness := model.FreshnessCurrent
	if pending {
		freshness = model.FreshnessScoresPending
	} else {
		since = nil
	}
	s := model.SyncStatus{Gate: h.gateFor(u), LastSyncAt: utc(st.LastSyncAt), LastSyncDevice: st.LastSyncDevice, Latest: st.Latest,
		Today: model.TodayState{Day: today, State: todayState(st, today, now), ScoredAt: utc(st.TodayScoredAt),
			ModelVersion: scoring.ModelVersion},
		PendingDays: st.Pending, ExpectedWithinSec: expected, ResyncRequestedAt: utc(st.ResyncAt),
		Sources: sources, Freshness: freshness, ScoresPendingSince: since}
	syncStatuses.put(u.ID, now, s)
	httpx.WriteJSON(w, 200, s)
}

// Window of the sync status's pending days (D297): scoring a past day rescores up to baselineDays (30) stored days
// after it in the same pass, today included, so a day queued within syncRescoreWindow holds the button.
const (
	syncPastDays      = 30
	syncRescoreWindow = 10 * time.Minute
)

// syncFreshness says whether the queued days hold today's scores back (D297): a day does when it is today or
// yesterday (any age of its queue row), or when it is one of the last 30 days and was queued in the last 10
// minutes. since is the earliest queued_at among those days (UTC); nil when none. today is the user's local day.
func syncFreshness(days []store.PendingDay, today string, now time.Time) (pending bool, since *time.Time) {
	t, err := time.Parse(dayLayout, today)
	if err != nil {
		return false, nil
	}
	yesterday := t.AddDate(0, 0, -1).Format(dayLayout)
	oldest := t.AddDate(0, 0, -syncPastDays).Format(dayLayout)
	for _, d := range days {
		hold := d.Day == today || d.Day == yesterday ||
			(d.Day >= oldest && d.QueuedAt.After(now.Add(-syncRescoreWindow)))
		if !hold {
			continue
		}
		pending = true
		if q := d.QueuedAt.UTC(); since == nil || q.Before(*since) {
			since = &q
		}
	}
	return pending, since
}

// syncSources is GET /v1/me/sync → sources (D297): healthkit first, then the account's connections that are
// listed (connected or needs_reauth, the provider served and its flag on). The time is the earliest arrival among
// the providers whose delivery is waiting to be stored (status pending); nil when none.
func (h *handler) syncSources(ctx context.Context, u model.User, st store.SyncState) ([]model.SyncSource, *time.Time, error) {
	hk := model.SyncSource{Source: model.SyncSourceHealthKit, LastSyncAt: utc(st.LastSyncAt), Status: model.SyncSourceOK}
	// Each gate against its own mode (gateFor answers the first failing gate whatever its mode, for the gate field).
	switch {
	case h.beta.Gate == config.ModeOn && !u.BetaAccess:
		hk.Status, hk.Reason = model.SyncSourceError, ptr(model.CodeInviteRequired)
	case h.beta.Consent == config.ModeOn && !gate.Consented(h.beta, u):
		hk.Status, hk.Reason = model.SyncSourceError, ptr(model.CodeConsentRequired)
	}
	sources := []model.SyncSource{hk}
	if h.conn == nil {
		return sources, nil, nil
	}
	conns, err := h.st.SyncConnections(ctx, u.ID)
	if err != nil {
		return nil, nil, err
	}
	var since *time.Time
	for _, c := range conns {
		_, ok, err := h.conn.served(ctx, c.Provider, u.ID)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		src := model.SyncSource{Source: c.Provider, LastSyncAt: utc(c.LastDataAt), Status: model.SyncSourceOK}
		// API.md: a delivery waiting to be stored is pending for connected and needs_reauth alike (it is stored
		// whatever the grant's state, usually within seconds); the needs_reauth error shows once it is.
		switch {
		case c.PendingSince != nil:
			src.Status = model.SyncSourcePending
			if p := c.PendingSince.UTC(); since == nil || p.Before(*since) {
				since = &p
			}
		case c.Status == model.ConnectionNeedsReauth:
			src.Status, src.Reason = model.SyncSourceError, ptr(model.SyncReasonNeedsReauth)
		}
		sources = append(sources, src)
	}
	return sources, since, nil
}

func ptr[T any](v T) *T { return &v }

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC()
	return &v
}

// DeviceHeader is the install's device id (API.md → Conventions).
const DeviceHeader = "X-RYFT-Device-ID"

// accountHint is GET /v1/me → account_hint: this account has no data and its
// device synced another account in the last 30 days (D91).
func (h *handler) accountHint(r *http.Request, u model.User) (*model.AccountHint, error) {
	device, ok := deviceOf(r)
	if !ok {
		return nil, nil // no (or no valid) device header: nothing to compare
	}
	ctx := r.Context()
	has, err := h.st.HasData(ctx, u.ID)
	if err != nil || has {
		return nil, err
	}
	provider, last, found, err := h.st.OtherAccountOnDevice(ctx, u.ID, device)
	if err != nil || !found {
		return nil, err
	}
	return &model.AccountHint{Kind: "other_account_on_device", OtherProvider: provider, OtherLastSync: last}, nil
}

// deviceOf reads X-RYFT-Device-ID.
func deviceOf(r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.Header.Get(DeviceHeader))
	return id, err == nil
}
