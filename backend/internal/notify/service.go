package notify

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/apns"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Store is what the service reads and writes (*store.Store as ryft_notify).
type Store interface {
	Recipient(ctx context.Context, id uuid.UUID) (store.Recipient, error)
	MorningScore(ctx context.Context, userID uuid.UUID, day string) (store.Morning, error)
	SessionNotice(ctx context.Context, userID, sessionID uuid.UUID) (store.SessionNotice, error)
	StaleCandidates(ctx context.Context, now time.Time) ([]store.Recipient, error)
	ClaimNotification(ctx context.Context, userID uuid.UUID, day, category string) (bool, error)
	ReleaseNotification(ctx context.Context, userID uuid.UUID, day, category string) error
	NotificationSent(ctx context.Context, userID uuid.UUID, day, category string, devices int) error
	DropPushToken(ctx context.Context, token string) error
	PushDelivered(ctx context.Context, token string) error
}

// Sender is APNs (*apns.Client).
type Sender interface {
	Send(ctx context.Context, n apns.Notification) (apns.Result, error)
}

// Service turns scoring's events and the stale check into pushes.
type Service struct {
	St   Store
	APNs Sender // nil: no APNs key configured; nothing is sent or claimed
	Log  *slog.Logger
	Now  func() time.Time

	refreshMu   sync.Mutex // the last silent refresh per user (RefreshEvery)
	lastRefresh map[uuid.UUID]time.Time
}

const (
	// Refresh is the silent push after a morning score: one per day, not a
	// user-facing category, so neither preferences nor quiet hours apply.
	Refresh = "REFRESH"
	// StaleAfter without a heart-rate or step sample, a user gets SYNC_STALE,
	// in the daytime only (StaleFromHour to StaleToHour local).
	StaleAfter    = 20 * time.Hour
	StaleFromHour = 9
	StaleToHour   = 21
	// SessionFresh: a session that ended longer ago (an offline queue
	// replayed days later) is scored, not announced.
	SessionFresh = 12 * time.Hour
)

const dayLayout = "2006-01-02"

func count(category, result string) {
	metrics.PushNotifications.WithLabelValues(category, result).Inc()
}

// Handle is one event from ConsumeNotify.
func (s *Service) Handle(ctx context.Context, m events.NotifyMessage) error {
	switch {
	case m.Scored != nil:
		return s.morning(ctx, *m.Scored)
	case m.Refresh != nil:
		return s.refresh(ctx, *m.Refresh)
	}
	return s.session(ctx, *m.SessionScored)
}

func (s *Service) recipient(ctx context.Context, id uuid.UUID, category string) (store.Recipient, bool, error) {
	r, err := s.St.Recipient(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		count(category, "stale")
		return r, false, events.Permanent(err) // the account was deleted
	}
	if err != nil {
		return r, false, err
	}
	if len(r.Tokens) == 0 {
		count(category, "no_token")
		return r, false, nil
	}
	return r, true, nil
}

// morning: the silent refresh, then MORNING_SCORE with the score as it is at
// send time; during quiet hours the message comes back when they end.
func (s *Service) morning(ctx context.Context, ev model.ScoredEvent) error {
	r, ok, err := s.recipient(ctx, ev.UserID, model.NotifyMorningScore)
	if !ok {
		return err
	}
	now := s.Now().In(r.Loc)
	if now.Format(dayLayout) != ev.Day {
		count(model.NotifyMorningScore, "stale")
		return nil
	}
	if err := s.deliver(ctx, &r, Refresh, ev.Day, apns.PushBackground, func(string) ([]byte, string, time.Time) {
		return SilentPayload(ev.Day), "refresh-" + ev.Day, now.Add(6 * time.Hour)
	}); err != nil {
		return err
	}
	s.refreshed(ev.UserID, now) // the per-sync refresh right after waits its turn
	if !Allowed(r.Prefs, model.NotifyMorningScore) {
		count(model.NotifyMorningScore, "muted")
		return nil
	}
	if until, quiet := QuietUntil(r.Prefs, now); quiet {
		if until.Format(dayLayout) != ev.Day {
			count(model.NotifyMorningScore, "quiet") // quiet until tomorrow: today's score is old news by then
			return nil
		}
		count(model.NotifyMorningScore, "deferred")
		return events.Defer(until.Sub(now))
	}
	score, err := s.St.MorningScore(ctx, ev.UserID, ev.Day)
	if errors.Is(err, store.ErrNotFound) || (err == nil && score.Recovery == nil) {
		count(model.NotifyMorningScore, "stale")
		return nil
	}
	if err != nil {
		return err
	}
	return s.deliver(ctx, &r, model.NotifyMorningScore, ev.Day, apns.PushAlert, func(lang string) ([]byte, string, time.Time) {
		m := Morning(ev.Day, *score.Recovery, score.Readiness, score.State, r.Loc, lang)
		return m.Payload(), m.Category + "-" + ev.Day, m.Expires
	})
}

// session: SESSION_SCORED, or PR when the user wants records but not session
// summaries. Quiet hours drop it (it would be stale by morning); an imported
// session never gets one.
func (s *Service) session(ctx context.Context, ev model.SessionScoredEvent) error {
	r, ok, err := s.recipient(ctx, ev.UserID, model.NotifySessionScored)
	if !ok {
		return err
	}
	n, err := s.St.SessionNotice(ctx, ev.UserID, ev.SessionID)
	now := s.Now()
	if errors.Is(err, store.ErrNotFound) || (err == nil && (n.Status != model.SessionCompleted || n.EndedAt == nil ||
		now.Sub(*n.EndedAt) > SessionFresh)) {
		count(model.NotifySessionScored, "stale")
		return nil
	}
	if err != nil {
		return err
	}
	if n.Origin == model.OriginImport {
		// Imported history is silent (D125): scoring sends no session.scored for an
		// import, and a re-complete of an imported session is not announced either.
		count(model.NotifySessionScored, "import")
		return nil
	}
	category := ""
	switch {
	case Allowed(r.Prefs, model.NotifySessionScored):
		category = model.NotifySessionScored
	case Allowed(r.Prefs, model.NotifyPR) && len(n.PRs) > 0:
		category = model.NotifyPR
	default:
		count(model.NotifySessionScored, "muted")
		return nil
	}
	if _, quiet := QuietUntil(r.Prefs, now.In(r.Loc)); quiet {
		count(category, "quiet")
		return nil
	}
	strain := 0.0
	if n.Strain != nil {
		strain = *n.Strain
	}
	return s.deliver(ctx, &r, category, ev.Day, apns.PushAlert, func(lang string) ([]byte, string, time.Time) {
		var m Message
		if category == model.NotifyPR {
			p := n.PRs[0]
			name := p.ExerciseEN
			if lang == "de" {
				name = p.ExerciseDE
			}
			m = Record(ev.Day, ev.SessionID.String(), name, p.Kind, p.Value, now, lang)
		} else {
			m = Session(ev.Day, ev.SessionID.String(), n.Title, strain, len(n.PRs), now, lang)
		}
		return m.Payload(), m.Category + "-" + ev.SessionID.String()[:8], m.Expires
	})
}

// CheckStale sends SYNC_STALE to users without new data for StaleAfter, once
// per local day, in the daytime and outside quiet hours. It does nothing
// without APNs.
func (s *Service) CheckStale(ctx context.Context) error {
	if s.APNs == nil {
		return nil
	}
	now := s.Now()
	cands, err := s.St.StaleCandidates(ctx, now)
	if err != nil {
		return err
	}
	for _, r := range cands {
		if r.LastSample == nil || len(r.Tokens) == 0 || now.Sub(*r.LastSample) < StaleAfter {
			continue
		}
		local := now.In(r.Loc)
		if local.Hour() < StaleFromHour || local.Hour() >= StaleToHour || !Allowed(r.Prefs, model.NotifySyncStale) {
			continue
		}
		if _, quiet := QuietUntil(r.Prefs, local); quiet {
			continue
		}
		day, hours := local.Format(dayLayout), int(now.Sub(*r.LastSample).Hours())
		// The stale loop's context has no user; the lines below get it from here,
		// as the event handlers' do from main, so no line carries user_id twice.
		f := logx.Fields{}
		if old := logx.FieldsFrom(ctx); old != nil {
			f = *old
		}
		f.UserID = r.ID.String()
		uctx := logx.WithFields(ctx, &f)
		if err := s.deliver(uctx, &r, model.NotifySyncStale, day, apns.PushAlert, func(string) ([]byte, string, time.Time) {
			m := Stale(day, hours, now)
			return m.Payload(), m.Category + "-" + day, m.Expires
		}); err != nil {
			s.Log.ErrorContext(uctx, "sync stale", "err", err)
		}
	}
	return nil
}

// deliver claims (user, day, category) and sends to every device, built per
// device language. Tokens APNs reports as gone are dropped. When no device was
// reached because APNs could not be reached, the claim is released and the
// error returned, so the event is retried.
func (s *Service) deliver(ctx context.Context, r *store.Recipient, category, day, pushType string,
	build func(lang string) (payload []byte, collapse string, expires time.Time)) error {
	if s.APNs == nil {
		count(category, "unconfigured")
		return nil
	}
	claimed, err := s.St.ClaimNotification(ctx, r.ID, day, category)
	if err != nil {
		return err
	}
	if !claimed {
		count(category, "duplicate")
		return nil
	}
	accepted, lastErr := s.sendAll(ctx, r, category, pushType, build)
	if accepted == 0 && lastErr != nil {
		if err := s.St.ReleaseNotification(ctx, r.ID, day, category); err != nil {
			s.Log.WarnContext(ctx, "release claim", "err", err)
		}
		count(category, "failed")
		return lastErr
	}
	if err := s.St.NotificationSent(ctx, r.ID, day, category, accepted); err != nil {
		s.Log.WarnContext(ctx, "record send", "err", err)
	}
	result := "sent"
	if accepted == 0 {
		result = "failed"
	}
	count(category, result)
	s.Log.InfoContext(ctx, "push", "category", category, "day", day, "devices", accepted, "of", len(r.Tokens))
	return nil
}

// sendAll sends to every device of r, built per device language, and drops the
// tokens APNs reports as gone: how many devices took it, and the last error of
// one APNs could not be reached for.
func (s *Service) sendAll(ctx context.Context, r *store.Recipient, category, pushType string,
	build func(lang string) (payload []byte, collapse string, expires time.Time)) (int, error) {
	accepted := 0
	var lastErr error
	kept := r.Tokens[:0:0]
	for _, t := range r.Tokens {
		payload, collapse, expires := build(Language(t.Locale))
		res, err := s.APNs.Send(ctx, apns.Notification{Token: t.Token, Env: t.Env, PushType: pushType,
			Payload: payload, CollapseID: collapse, Expiration: expires})
		if err != nil {
			metrics.APNsResponses.WithLabelValues(pushType, "error").Inc()
			lastErr = err
			kept = append(kept, t)
			continue
		}
		metrics.APNsResponses.WithLabelValues(pushType, strconv.Itoa(res.Status)).Inc()
		if !res.Gone() {
			kept = append(kept, t)
		}
		switch {
		case res.OK():
			accepted++
			if err := s.St.PushDelivered(ctx, t.Token); err != nil {
				s.Log.WarnContext(ctx, "record push delivered", "err", err)
			}
		case res.Gone():
			if err := s.St.DropPushToken(ctx, t.Token); err != nil {
				s.Log.WarnContext(ctx, "drop push token", "err", err)
			} else {
				metrics.PushTokensPruned.Inc()
			}
		default:
			s.Log.WarnContext(ctx, "apns refused", "category", category, "status", res.Status, "reason", res.Reason,
				"device_id", t.DeviceID)
		}
	}
	r.Tokens = kept // a later send of the same event skips the dropped ones
	return accepted, lastErr
}

// RefreshEvery is the least time between two silent refreshes of a user: iOS
// budgets background pushes, and the app polls GET /v1/me/sync meanwhile (D92).
const RefreshEvery = 5 * time.Minute

// refreshed reports whether user got a silent refresh within RefreshEvery,
// and records now otherwise (one notify replica: memory is enough).
func (s *Service) refreshed(user uuid.UUID, now time.Time) bool {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.lastRefresh == nil {
		s.lastRefresh = map[uuid.UUID]time.Time{}
	}
	if last, ok := s.lastRefresh[user]; ok && now.Sub(last) < RefreshEvery {
		return true
	}
	if len(s.lastRefresh) > 50_000 {
		clear(s.lastRefresh)
	}
	s.lastRefresh[user] = now
	return false
}

// refresh: the silent push after scoring finished a sync's days, at most one
// per user every RefreshEvery (shared with the morning one), collapsed per day.
func (s *Service) refresh(ctx context.Context, ev model.RefreshEvent) error {
	if s.APNs == nil {
		count(Refresh, "unconfigured")
		return nil
	}
	r, ok, err := s.recipient(ctx, ev.UserID, Refresh)
	if !ok {
		return err
	}
	now := s.Now()
	if s.refreshed(ev.UserID, now) {
		count(Refresh, "throttled")
		return nil
	}
	accepted, lastErr := s.sendAll(ctx, &r, Refresh, apns.PushBackground, func(string) ([]byte, string, time.Time) {
		return SilentPayload(ev.Day), "refresh-" + ev.Day, now.Add(6 * time.Hour)
	})
	// A refresh minutes late is no use: a failure is logged and counted, not retried.
	result := "sent"
	if accepted == 0 {
		result = "failed"
		if lastErr != nil {
			s.Log.WarnContext(ctx, "refresh push", "err", lastErr)
		}
	}
	count(Refresh, result)
	return nil
}
