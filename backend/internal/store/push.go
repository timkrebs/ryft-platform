package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// prefsOf is the stored notification settings as the API shows them: every
// category, on unless turned off.
func prefsOf(stored map[string]bool, from, to *string) model.NotificationPrefs {
	p := model.NotificationPrefs{Categories: make(map[string]bool, len(model.NotifyCategories))}
	for _, c := range model.NotifyCategories {
		on, set := stored[c]
		p.Categories[c] = !set || on
	}
	if from != nil && to != nil {
		p.QuietHours = &model.QuietHours{From: *from, To: *to}
	}
	return p
}

// UpdateNotificationPrefs merges categories into the stored ones and sets or
// clears the quiet hours (quiet.Set); it returns the updated user.
func (s *Store) UpdateNotificationPrefs(ctx context.Context, id uuid.UUID, p model.NotificationPatch) (model.User, error) {
	cats := p.Categories
	if cats == nil {
		cats = map[string]bool{}
	}
	raw, err := json.Marshal(cats)
	if err != nil {
		return model.User{}, err
	}
	var from, to *string
	if p.QuietHours.Value != nil {
		from, to = &p.QuietHours.Value.From, &p.QuietHours.Value.To
	}
	u, err := scanUser(s.pool.QueryRow(ctx, `UPDATE users SET
		notify_categories = notify_categories || $2::jsonb,
		quiet_from = CASE WHEN $3 THEN $4::time ELSE quiet_from END,
		quiet_to   = CASE WHEN $3 THEN $5::time ELSE quiet_to END
		WHERE id=$1 RETURNING `+userColumns, id, raw, p.QuietHours.Set, from, to))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// RegisterPushToken stores the install's token for the user. A token is one
// app install's: wherever else it was registered (another account signed in on
// the same phone, a reinstall under a new device id), it moves here.
func (s *Store) RegisterPushToken(ctx context.Context, userID, deviceID uuid.UUID, reg model.PushRegistration) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM push_tokens WHERE token=$3 AND (user_id, device_id) <> ($1, $2)`,
			userID, deviceID, reg.Token); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO push_tokens (user_id, device_id, token, env, locale, updated_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (user_id, device_id) DO UPDATE SET token=EXCLUDED.token, env=EXCLUDED.env,
			  locale=EXCLUDED.locale, updated_at=now()`, userID, deviceID, reg.Token, reg.Env, reg.Locale)
		return err
	})
}

// DeletePushToken removes the install's token (sign-out); none is fine.
func (s *Store) DeletePushToken(ctx context.Context, userID, deviceID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_tokens WHERE user_id=$1 AND device_id=$2`, userID, deviceID)
	return err
}

// ---- notify ---------------------------------------------------------------------

// Recipient is what notify needs of a user: where they are, what they want, and
// where their apps are.
type Recipient struct {
	ID         uuid.UUID
	Loc        *time.Location
	Prefs      model.NotificationPrefs
	Tokens     []model.PushToken
	LastSample *time.Time // newest heart-rate or step sample of the last 7 days (stale check only)
}

// Recipient loads one user for notify; ErrNotFound when the user is gone.
func (s *Store) Recipient(ctx context.Context, id uuid.UUID) (Recipient, error) {
	r, err := scanRecipient(s.pool.QueryRow(ctx, `SELECT id, tz, notify_categories,
		to_char(quiet_from, 'HH24:MI'), to_char(quiet_to, 'HH24:MI'), NULL::timestamptz
		FROM users WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	r.Tokens, err = s.pushTokens(ctx, id)
	return r, err
}

func scanRecipient(row pgx.Row) (Recipient, error) {
	var r Recipient
	var tz string
	var cats map[string]bool
	var from, to *string
	if err := row.Scan(&r.ID, &tz, &cats, &from, &to, &r.LastSample); err != nil {
		return r, err
	}
	r.Prefs = prefsOf(cats, from, to)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	r.Loc = loc
	return r, nil
}

func (s *Store) pushTokens(ctx context.Context, userID uuid.UUID) ([]model.PushToken, error) {
	rows, err := s.pool.Query(ctx, `SELECT device_id, token, env, locale FROM push_tokens WHERE user_id=$1 ORDER BY device_id`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.PushToken, error) {
		var t model.PushToken
		err := r.Scan(&t.DeviceID, &t.Token, &t.Env, &t.Locale)
		return t, err
	})
}

// StaleCandidates are the users with a push token and their newest heart-rate
// or step sample of the 7 days before now (nil: none, they stopped using RYFT).
func (s *Store) StaleCandidates(ctx context.Context, now time.Time) ([]Recipient, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.tz, u.notify_categories,
		to_char(u.quiet_from, 'HH24:MI'), to_char(u.quiet_to, 'HH24:MI'),
		greatest(
		  (SELECT max(start_at) FROM samples s WHERE s.user_id = u.id AND s.type = 'heart_rate' AND s.start_at > $1::timestamptz - interval '7 days'),
		  (SELECT max(start_at) FROM samples s WHERE s.user_id = u.id AND s.type = 'steps' AND s.start_at > $1::timestamptz - interval '7 days'))
		FROM users u WHERE EXISTS (SELECT 1 FROM push_tokens p WHERE p.user_id = u.id) ORDER BY u.id`, now)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Recipient, error) { return scanRecipient(r) })
	if err != nil {
		return nil, err
	}
	for k := range out {
		if out[k].Tokens, err = s.pushTokens(ctx, out[k].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ClaimNotification reserves the user's one notification of category for day;
// false when it was claimed before (sent, or being sent by another delivery).
func (s *Store) ClaimNotification(ctx context.Context, userID uuid.UUID, day, category string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `INSERT INTO notifications_sent (user_id, day, category) VALUES ($1, $2::date, $3)
		ON CONFLICT DO NOTHING`, userID, day, category)
	return err == nil && tag.RowsAffected() == 1, err
}

// ReleaseNotification gives a claim back when nothing reached a device, so a
// retry may send it.
func (s *Store) ReleaseNotification(ctx context.Context, userID uuid.UUID, day, category string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM notifications_sent WHERE user_id=$1 AND day=$2::date AND category=$3`,
		userID, day, category)
	return err
}

// NotificationSent records on how many devices APNs accepted the claimed send.
func (s *Store) NotificationSent(ctx context.Context, userID uuid.UUID, day, category string, devices int) error {
	_, err := s.pool.Exec(ctx, `UPDATE notifications_sent SET devices=$4, sent_at=now()
		WHERE user_id=$1 AND day=$2::date AND category=$3`, userID, day, category, devices)
	return err
}

// DropPushToken removes a token APNs reported as no longer valid.
func (s *Store) DropPushToken(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_tokens WHERE token=$1`, token)
	return err
}

// PushDelivered records that APNs accepted a notification for token (the
// admin view's "last success").
func (s *Store) PushDelivered(ctx context.Context, token string) error {
	_, err := s.pool.Exec(ctx, `UPDATE push_tokens SET last_success_at = now() WHERE token=$1`, token)
	return err
}

// SessionNotice is what the session and record notifications say.
type SessionNotice struct {
	Title         string
	Status        string
	EndedAt       *time.Time
	Strain        *float64
	MuscularTRIMP *float64
	PRs           []PRNotice
}

// PRNotice is one record a session set, with the exercise's names.
type PRNotice struct {
	ExerciseEN, ExerciseDE string
	Kind                   string
	Value                  float64
}

// SessionNotice loads a session for its notification; ErrNotFound when it is gone.
func (s *Store) SessionNotice(ctx context.Context, userID, sessionID uuid.UUID) (SessionNotice, error) {
	var n SessionNotice
	err := s.pool.QueryRow(ctx, `SELECT title, status, ended_at, strain, muscular_trimp FROM sessions
		WHERE user_id=$1 AND id=$2`, userID, sessionID).Scan(&n.Title, &n.Status, &n.EndedAt, &n.Strain, &n.MuscularTRIMP)
	if errors.Is(err, pgx.ErrNoRows) {
		return n, ErrNotFound
	}
	if err != nil {
		return n, err
	}
	rows, err := s.pool.Query(ctx, `SELECT e.name_en, e.name_de, p.kind, p.value FROM personal_records p
		JOIN exercises e ON e.id = p.exercise_id WHERE p.user_id=$1 AND p.session_id=$2
		ORDER BY p.kind = 'e1rm' DESC, e.name_en, p.kind`, userID, sessionID)
	if err != nil {
		return n, err
	}
	n.PRs, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (PRNotice, error) {
		var p PRNotice
		err := r.Scan(&p.ExerciseEN, &p.ExerciseDE, &p.Kind, &p.Value)
		return p, err
	})
	return n, err
}

// Morning is a day's score as the morning push states it.
type Morning struct {
	Recovery  *int
	Readiness *int
	State     string
}

// MorningScore reads day's score at send time (not the event's copy, which a
// deferral may have outdated); ErrNotFound when the day has no score.
func (s *Store) MorningScore(ctx context.Context, userID uuid.UUID, day string) (Morning, error) {
	var m Morning
	err := s.pool.QueryRow(ctx, `SELECT recovery, readiness, state FROM daily_scores WHERE user_id=$1 AND day=$2::date`,
		userID, day).Scan(&m.Recovery, &m.Readiness, &m.State)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}
