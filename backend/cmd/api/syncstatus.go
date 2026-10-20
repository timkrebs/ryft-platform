package main

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/config"
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
	case h.beta.Consent != config.ModeOff && (u.Consent == nil || u.Consent.PolicyVersion != h.beta.PrivacyPolicyVersion):
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
	s := model.SyncStatus{Gate: h.gateFor(u), LastSyncAt: utc(st.LastSyncAt), LastSyncDevice: st.LastSyncDevice, Latest: st.Latest,
		Today: model.TodayState{Day: today, State: todayState(st, today, now), ScoredAt: utc(st.TodayScoredAt),
			ModelVersion: scoring.ModelVersion},
		PendingDays: st.Pending, ExpectedWithinSec: expected, ResyncRequestedAt: utc(st.ResyncAt)}
	syncStatuses.put(u.ID, now, s)
	httpx.WriteJSON(w, 200, s)
}

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
