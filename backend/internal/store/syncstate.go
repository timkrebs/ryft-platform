package store

import (
	"context"
	"errors"
	"math"
	"slices"

	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultExpectedSec is the sync-to-score estimate before there is a history.
const DefaultExpectedSec = 30

// QueueDays records that a sync queued days for scoring (D91): pending until
// scoring finishes each.
func (s *Store) QueueDays(ctx context.Context, userID uuid.UUID, days []string) error {
	return s.QueueDaysWith(ctx, userID, days, nil)
}

// QueueDaysWith is QueueDays with also run in the same transaction: the event that
// announces the days to scoring, written into the outbox (D101), so the days are
// queued and announced together or not at all.
func (s *Store) QueueDaysWith(ctx context.Context, userID uuid.UUID, days []string, also func(pgx.Tx) error) error {
	if len(days) == 0 {
		return nil
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := queueDays(ctx, tx, userID, days); err != nil {
			return err
		}
		if also != nil {
			return also(tx)
		}
		return nil
	})
}

// queueDays puts days (local "2006-01-02") into score_queue as pending, in tx.
// The rows are upserted in day order, distinct: two transactions queueing
// overlapping days (a sync and an import's item) lock them in the same order and
// cannot deadlock on them.
func queueDays(ctx context.Context, tx pgx.Tx, userID uuid.UUID, days []string) error {
	sorted := slices.Compact(slices.Sorted(slices.Values(days)))
	_, err := tx.Exec(ctx, `INSERT INTO score_queue (user_id, day, queued_at, scored_at)
		SELECT $1, d::date, now(), NULL FROM unnest($2::text[]) WITH ORDINALITY AS u(d, n) ORDER BY n
		ON CONFLICT (user_id, day) DO UPDATE SET queued_at = now(), scored_at = NULL`, userID, sorted)
	return err
}

// MarkScored records that scoring finished day for a computation that started
// at started: only a queue entry from before then is done (a sync during the
// computation stays pending). It returns when that entry was queued, for the
// latency metric; ok false when nothing was pending. A day really scored has no
// closed_reason (D298).
func (s *Store) MarkScored(ctx context.Context, userID uuid.UUID, day string, started time.Time) (queuedAt time.Time, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `UPDATE score_queue SET scored_at = now(), closed_reason = NULL
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

// QueueClosedNoConsent is score_queue.closed_reason of a row scoring closed without scoring it because the
// account has no current consent (D78, D298). A closed set: a new reason is a new migration.
const QueueClosedNoConsent = "no_consent"

// CloseQueue closes a user's unscored queue rows without scoring them, with reason (QueueClosedNoConsent): it
// sets scored_at, so nothing waits for them (pending_days, the sync status), and closed_reason, so the sync
// latency estimate skips them. It returns the number of rows closed; closing twice closes nothing the second
// time. Only scoring calls it (D298).
func (s *Store) CloseQueue(ctx context.Context, userID uuid.UUID, reason string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE score_queue SET scored_at = now(), closed_reason = $2
		WHERE user_id = $1 AND scored_at IS NULL`, userID, reason)
	return tag.RowsAffected(), err
}

// PendingDay is a queued, unscored day and when it was queued (again).
type PendingDay struct {
	Day      string // YYYY-MM-DD
	QueuedAt time.Time
}

// pendingQueue lists a user's days queued and not yet scored, oldest day first.
func (s *Store) pendingQueue(ctx context.Context, userID uuid.UUID) ([]PendingDay, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day, 'YYYY-MM-DD'), queued_at FROM score_queue
		WHERE user_id = $1 AND scored_at IS NULL ORDER BY day`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PendingDay, error) {
		var d PendingDay
		err := row.Scan(&d.Day, &d.QueuedAt)
		d.QueuedAt = d.QueuedAt.UTC()
		return d, err
	})
}

// PendingDays lists a user's days queued and not yet scored, oldest first.
func (s *Store) PendingDays(ctx context.Context, userID uuid.UUID) ([]string, error) {
	q, err := s.pendingQueue(ctx, userID)
	return pendingDayNames(q), err
}

// pendingDayNames is the days of q; never nil (it is a JSON array).
func pendingDayNames(q []PendingDay) []string {
	days := make([]string, len(q))
	for i, d := range q {
		days[i] = d.Day
	}
	return days
}

// ExpectedScoreSec is the 95th percentile of sync-to-score over every account
// in the last 6 hours, in whole seconds (at least 1); DefaultExpectedSec with
// fewer than 5 entries. Rows scoring closed without scoring them (closed_reason, D298) are no sync-to-score time.
func (s *Store) ExpectedScoreSec(ctx context.Context) (int, error) {
	var n int
	var p95 *float64
	err := s.pool.QueryRow(ctx, `SELECT count(*), percentile_cont(0.95) WITHIN GROUP (ORDER BY extract(epoch FROM scored_at - queued_at))
		FROM score_queue WHERE scored_at > now() - interval '6 hours' AND closed_reason IS NULL`).Scan(&n, &p95)
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
	Queued         []PendingDay // Pending with when each day was queued (again), same order
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
	st.Queued, err = s.pendingQueue(ctx, userID)
	st.Pending = pendingDayNames(st.Queued)
	return st, err
}

// SyncConnection is one of a user's connections that GET /v1/me/sync may list: Status is "connected" or
// "needs_reauth". LastDataAt is when RYFT last stored a record from it, PendingSince when the oldest delivery
// that ingest has not stored yet arrived; each nil when there is none.
type SyncConnection struct {
	Provider     string
	Status       string
	LastDataAt   *time.Time
	PendingSince *time.Time
}

// SyncConnections lists a user's connections that are connected or need renewing, by provider. The subquery
// probes provider_inbox_user_pending; the api reads received_at only, never a payload.
func (s *Store) SyncConnections(ctx context.Context, userID uuid.UUID) ([]SyncConnection, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.provider, c.status, c.last_data_at,
		(SELECT min(i.received_at) FROM provider_inbox i
		  WHERE i.user_id = c.user_id AND i.provider = c.provider AND i.state = 'pending') AS pending_since
		FROM connections c
		WHERE c.user_id = $1 AND c.status IN ('connected', 'needs_reauth')
		ORDER BY c.provider`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SyncConnection])
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
