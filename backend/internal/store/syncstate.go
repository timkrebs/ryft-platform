package store

import (
	"context"
	"errors"
	"math"

	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultExpectedSec is the sync-to-score estimate before there is a history.
const DefaultExpectedSec = 30

// QueueDays records that a sync queued days for scoring (D91): pending until
// scoring finishes each.
func (s *Store) QueueDays(ctx context.Context, userID uuid.UUID, days []string) error {
	if len(days) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at)
		SELECT $1, d::date, now(), NULL FROM unnest($2::text[]) d
		ON CONFLICT (user_id, day) DO UPDATE SET queued_at = now(), scored_at = NULL`, userID, days)
	return err
}

// MarkScored records that scoring finished day for a computation that started
// at started: only a queue entry from before then is done (a sync during the
// computation stays pending). It returns when that entry was queued, for the
// latency metric; ok false when nothing was pending.
func (s *Store) MarkScored(ctx context.Context, userID uuid.UUID, day string, started time.Time) (queuedAt time.Time, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `UPDATE score_queue SET scored_at = now()
		WHERE user_id = $1 AND day = $2::date AND scored_at IS NULL AND queued_at <= $3 RETURNING queued_at`,
		userID, day, started).Scan(&queuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return queuedAt, err == nil, err
}

// PruneScoreQueue forgets entries scored more than a day ago.
func (s *Store) PruneScoreQueue(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM score_queue WHERE scored_at < now() - interval '1 day'`)
	return err
}

// PendingDays lists a user's days queued and not yet scored, oldest first.
func (s *Store) PendingDays(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD') FROM score_queue
		WHERE user_id = $1 AND scored_at IS NULL ORDER BY day`, userID)
	if err != nil {
		return nil, err
	}
	days, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if days == nil {
		days = []string{}
	}
	return days, err
}

// ExpectedScoreSec is the 95th percentile of sync-to-score over every account
// in the last 6 hours, in whole seconds (at least 1); DefaultExpectedSec with
// fewer than 5 entries.
func (s *Store) ExpectedScoreSec(ctx context.Context) (int, error) {
	var n int
	var p95 *float64
	err := s.pool.QueryRow(ctx, `SELECT count(*), percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM scored_at - queued_at))
		FROM score_queue WHERE scored_at > now() - interval '6 hours'`).Scan(&n, &p95)
	if err != nil || n < 5 || p95 == nil {
		return DefaultExpectedSec, err
	}
	return max(1, int(math.Ceil(*p95))), nil
}

// SyncState is what GET /v1/me/sync reads from the database.
type SyncState struct {
	LastSyncAt     *time.Time
	LastSyncDevice *string
	Latest         map[string]time.Time
	Pending        []string
	ResyncAt       *time.Time
	TodayScoredAt  *time.Time
	TodayHasSleep  bool
}

// SyncStateOf reads a user's sync state for the local day today.
func (s *Store) SyncStateOf(ctx context.Context, userID uuid.UUID, today string) (SyncState, error) {
	st := SyncState{Latest: map[string]time.Time{}}
	err := s.pool.QueryRow(ctx, `SELECT d.last_sync_at, d.name, u.resync_requested_at, ds.computed_at, ds.sleep_actual_min IS NOT NULL
		FROM users u
		LEFT JOIN LATERAL (SELECT last_sync_at, NULLIF(name, '') AS name FROM devices WHERE user_id = u.id AND last_sync_at IS NOT NULL
		  ORDER BY last_sync_at DESC LIMIT 1) d ON true
		LEFT JOIN daily_scores ds ON ds.user_id = u.id AND ds.day = $2::date
		WHERE u.id = $1`, userID, today).Scan(&st.LastSyncAt, &st.LastSyncDevice, &st.ResyncAt, &st.TodayScoredAt, &st.TodayHasSleep)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotFound
	}
	if err != nil {
		return st, err
	}
	rows, err := s.pool.Query(ctx, `SELECT kind, max(last_seen) FROM user_sources WHERE user_id = $1 GROUP BY kind`, userID)
	if err != nil {
		return st, err
	}
	var kind string
	var seen time.Time
	if _, err := pgx.ForEachRow(rows, []any{&kind, &seen}, func() error {
		st.Latest[kind] = seen.UTC()
		return nil
	}); err != nil {
		return st, err
	}
	st.Pending, err = s.PendingDays(ctx, userID)
	return st, err
}

// NewestRecord is the latest of Latest; zero without any.
func (st SyncState) NewestRecord() time.Time {
	var t time.Time
	for _, v := range st.Latest {
		if v.After(t) {
			t = v
		}
	}
	return t
}

// PendingSet is Pending as a set.
func (st SyncState) PendingSet() map[string]bool {
	m := make(map[string]bool, len(st.Pending))
	for _, d := range st.Pending {
		m[d] = true
	}
	return m
}

// RequestResync records the operator's request for a full re-sync.
func (s *Store) RequestResync(ctx context.Context, userID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET resync_requested_at = now() WHERE id = $1`, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// OtherAccountOnDevice finds another account that synced from device in the
// last 30 days, the most recent: its provider and last sync.
func (s *Store) OtherAccountOnDevice(ctx context.Context, userID, deviceID uuid.UUID) (provider *string, lastSync time.Time, found bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT u.provider, d.last_sync_at FROM devices d JOIN users u ON u.id = d.user_id
		WHERE d.id = $2 AND d.user_id <> $1 AND d.last_sync_at > now() - interval '30 days'
		ORDER BY d.last_sync_at DESC LIMIT 1`, userID, deviceID).Scan(&provider, &lastSync)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, false, nil
	}
	return provider, lastSync.UTC(), err == nil, err
}

// HasData says whether a user has sent anything (any source seen).
func (s *Store) HasData(ctx context.Context, userID uuid.UUID) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_sources WHERE user_id = $1)`, userID).Scan(&ok)
	return ok, err
}
