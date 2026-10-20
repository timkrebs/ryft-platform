package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// FeedbackPerDay is how many reports a user may send in 24 hours.
const FeedbackPerDay = 10

// ErrTooMuchFeedback: the user sent FeedbackPerDay reports in the last 24
// hours; RetryAfter says when the oldest of them stops counting.
type ErrTooMuchFeedback struct{ RetryAfter time.Duration }

func (e ErrTooMuchFeedback) Error() string {
	return fmt.Sprintf("at most %d reports a day", FeedbackPerDay)
}

// AddFeedback stores a report unless the user already sent FeedbackPerDay
// in the last 24 hours. Counted in the database, so it holds across pods;
// the user lock makes two concurrent reports count one after the other.
func (s *Store) AddFeedback(ctx context.Context, userID uuid.UUID, f model.FeedbackPost, category string) (int64, error) {
	var id int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockUser, userID); err != nil {
			return err
		}
		var n int
		var oldest *time.Time
		if err := tx.QueryRow(ctx, `SELECT count(*), min(created_at) FROM feedback
			WHERE user_id = $1 AND created_at > now() - interval '1 day'`, userID).Scan(&n, &oldest); err != nil {
			return err
		}
		if n >= FeedbackPerDay && oldest != nil {
			return ErrTooMuchFeedback{RetryAfter: max(time.Until(oldest.Add(24*time.Hour)), time.Second)}
		}
		var diag any
		if len(f.Diagnostics) > 0 && string(f.Diagnostics) != "null" {
			diag = string(f.Diagnostics)
		}
		ids := f.RequestIDs
		if ids == nil {
			ids = []string{}
		}
		return tx.QueryRow(ctx, `INSERT INTO feedback (user_id, category, message, app_version, build, device_model,
			os_version, request_ids, diagnostics) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb) RETURNING id`,
			userID, category, f.Message, f.AppVersion, f.Build, f.DeviceModel, f.OSVersion, ids, diag).Scan(&id)
	})
	return id, err
}

// FeedbackSince lists the reports created at or after since, newest first,
// at most limit.
func (s *Store) FeedbackSince(ctx context.Context, since time.Time, limit int) ([]model.Feedback, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, user_id, category, message, app_version, build, device_model,
		os_version, request_ids, diagnostics, created_at, resolved_at
		FROM feedback WHERE created_at >= $1 ORDER BY created_at DESC, id DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	out := []model.Feedback{}
	var f model.Feedback
	var diag []byte
	_, err = pgx.ForEachRow(rows, []any{&f.ID, &f.UserID, &f.Category, &f.Message, &f.AppVersion, &f.Build,
		&f.DeviceModel, &f.OSVersion, &f.RequestIDs, &diag, &f.CreatedAt, &f.ResolvedAt}, func() error {
		f.Diagnostics = nil
		if diag != nil {
			f.Diagnostics = append([]byte(nil), diag...)
		}
		out = append(out, f)
		return nil
	})
	return out, err
}

// ResolveFeedback marks a report as dealt with (again: a no-op). ErrNotFound
// when there is no such report.
func (s *Store) ResolveFeedback(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE feedback SET resolved_at = COALESCE(resolved_at, now()) WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// AdminUsers is the data-quality view: every user with what the operator
// needs to see whose data is broken. Emails only when asked for. The 14 days
// are UTC days.
func (s *Store) AdminUsers(ctx context.Context, emails bool) ([]model.AdminUser, error) {
	rows, err := s.pool.Query(ctx, `SELECT u.id, u.created_at, u.provider, CASE WHEN $1 THEN u.email END,
		  u.beta_access_at IS NOT NULL,
		  (SELECT c.policy_version FROM consents c WHERE c.user_id = u.id AND c.kind = 'health_processing'
		     AND c.revoked_at IS NULL ORDER BY c.granted_at DESC LIMIT 1),
		  d.app_version, d.os_version, d.last_sync_at,
		  COALESCE((SELECT json_agg(json_build_object('name', x.name, 'kinds', x.kinds) ORDER BY x.seen DESC, x.name)
		     FROM (SELECT name, array_agg(kind ORDER BY kind) AS kinds, max(last_seen) AS seen FROM user_sources
		           WHERE user_id = u.id AND last_seen > now() - interval '30 days' GROUP BY name) x), '[]'::json),
		  q.days, q.staged, q.hrv,
		  (SELECT s.calibrating FROM daily_scores s WHERE s.user_id = u.id ORDER BY s.day DESC LIMIT 1),
		  u.max_hr_source,
		  (SELECT count(*) FROM feedback f WHERE f.user_id = u.id AND f.resolved_at IS NULL)
		FROM users u
		LEFT JOIN LATERAL (SELECT app_version, os_version, last_sync_at FROM devices
		  WHERE user_id = u.id ORDER BY last_sync_at DESC NULLS LAST LIMIT 1) d ON true
		CROSS JOIN LATERAL (SELECT count(*) AS days,
		    count(*) FILTER (WHERE COALESCE(s.deep_min, 0) + COALESCE(s.rem_min, 0) > 0) AS staged,
		    count(*) FILTER (WHERE s.hrv_ms IS NOT NULL) AS hrv
		  FROM daily_scores s WHERE s.user_id = u.id AND s.day > current_date - 14) q
		ORDER BY d.last_sync_at DESC NULLS LAST, u.created_at`, emails)
	if err != nil {
		return nil, err
	}
	out := []model.AdminUser{}
	var a model.AdminUser
	_, err = pgx.ForEachRow(rows, []any{&a.ID, &a.CreatedAt, &a.Provider, &a.Email, &a.BetaAccess, &a.ConsentVersion,
		&a.AppVersion, &a.OSVersion, &a.LastSyncAt, &a.Sources, &a.DaysScored14, &a.NightsStaged14, &a.DaysHRV14,
		&a.Calibrating, &a.MaxHRSource, &a.OpenFeedback}, func() error {
		a.ShortID = a.ID.String()[:8]
		out = append(out, a)
		a = model.AdminUser{}
		return nil
	})
	return out, err
}

// IsTooMuchFeedback reports whether err is the daily feedback limit.
func IsTooMuchFeedback(err error) (ErrTooMuchFeedback, bool) {
	var e ErrTooMuchFeedback
	ok := errors.As(err, &e)
	return e, ok
}
