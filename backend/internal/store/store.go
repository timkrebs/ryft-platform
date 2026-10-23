// Package store is the only place SQL lives. Services call these methods;
// nothing else touches the pool directly.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
)

type Store struct{ pool *pgxpool.Pool }

// ErrNotFound is returned by single-row reads when the row does not exist.
var ErrNotFound = errors.New("not found")

// ErrDeleted is returned by EnsureUser for an account deleted with
// DeleteUser: its tokens stay valid until they expire but must not bring the
// account back.
var ErrDeleted = errors.New("account deleted")

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Ready(ctx context.Context) error { return s.pool.Ping(ctx) }

// ---- users / devices -------------------------------------------------------

const userColumns = `id, name, email, provider, tz, max_hr, sleep_base_min, sleep_base_source,
	sleep_base_learned_min, sleep_base_learned_at, notify_categories,
	to_char(quiet_from, 'HH24:MI'), to_char(quiet_to, 'HH24:MI'), beta_access_at IS NOT NULL,
	(SELECT c.policy_version FROM consents c WHERE c.user_id = users.id AND c.kind = 'health_processing'
	   AND c.revoked_at IS NULL ORDER BY c.granted_at DESC LIMIT 1),
	(SELECT c.granted_at FROM consents c WHERE c.user_id = users.id AND c.kind = 'health_processing'
	   AND c.revoked_at IS NULL ORDER BY c.granted_at DESC LIMIT 1),
	birth_year, max_hr_source`

func scanUser(row pgx.Row, extra ...any) (model.User, error) {
	var u model.User
	var cats map[string]bool
	var from, to, consent *string
	var consentAt *time.Time
	err := row.Scan(append([]any{&u.ID, &u.Name, &u.Email, &u.Provider, &u.TZ, &u.MaxHR, &u.SleepBaseMin,
		&u.SleepBaseSource, &u.SleepBaseLearnedMin, &u.SleepBaseLearnedAt, &cats, &from, &to,
		&u.BetaAccess, &consent, &consentAt, &u.BirthYear, &u.MaxHRSource}, extra...)...)
	u.Notifications = prefsOf(cats, from, to)
	if consent != nil && consentAt != nil {
		u.Consent = &model.Consent{PolicyVersion: *consent, GrantedAt: consentAt.UTC()}
	}
	return u, err
}

// GetUser loads a user and their timezone. ErrNotFound when there is no such user.
func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (model.User, *time.Location, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, nil, ErrNotFound
	}
	if err != nil {
		return u, nil, err
	}
	loc, err := time.LoadLocation(u.TZ)
	if err != nil {
		return u, nil, fmt.Errorf("user tz %q: %w", u.TZ, err)
	}
	return u, loc, nil
}

// Identity is what a verified token says about its user.
type Identity struct {
	ID       uuid.UUID
	Email    string // "" = unknown, keeps the stored value
	Provider string
	Name     string
}

// seenEvery bounds how often an active user's row is written (last_seen_at).
const seenEvery = 5 * time.Minute

// lockUser serialises EnsureUser's writes and DeleteUser for one user, so a
// request racing a deletion either finishes first or sees the tombstone.
const lockUser = `SELECT pg_advisory_xact_lock(7419002, hashtext($1::text))`

// EnsureUser returns the caller's user row, creating it on the first request
// (users come from Supabase Auth; there is no sign-up endpoint). The common
// case is a single read; the row is only written when it is missing, its
// last_seen_at is older than seenEvery, or the token carries a new email,
// provider or name. A deleted account is ErrDeleted.
func (s *Store) EnsureUser(ctx context.Context, id Identity) (model.User, error) {
	var lastSeen *time.Time
	u, err := scanUser(s.pool.QueryRow(ctx, `SELECT `+userColumns+`, last_seen_at FROM users WHERE id=$1`, id.ID), &lastSeen)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return u, err
	case lastSeen != nil && time.Since(*lastSeen) < seenEvery && !identityChanged(u, id):
		return u, nil
	}
	var out model.User
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockUser, id.ID); err != nil {
			return err
		}
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM deleted_users WHERE id=$1)`, id.ID).Scan(&deleted); err != nil {
			return err
		}
		if deleted {
			return ErrDeleted
		}
		var err error
		out, err = scanUser(tx.QueryRow(ctx, `INSERT INTO users (id, name, email, provider, last_seen_at)
			VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), now())
			ON CONFLICT (id) DO UPDATE SET last_seen_at = now(),
			  name     = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE users.name END,
			  email    = COALESCE(EXCLUDED.email, users.email),
			  provider = COALESCE(EXCLUDED.provider, users.provider)
			RETURNING `+userColumns, id.ID, id.Name, id.Email, id.Provider))
		return err
	})
	return out, err
}

// DeleteUser erases a user: every row they own and the user row, in one
// transaction, leaving only a tombstone (deleted_users) so their still-valid
// tokens cannot recreate the account. Idempotent. The user row is locked
// first: syncs in flight (which hold a key-share lock through their foreign
// keys) finish before the delete, later ones fail their foreign-key check.
func (s *Store) DeleteUser(ctx context.Context, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, lockUser, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO deleted_users (id) VALUES ($1) ON CONFLICT (id) DO NOTHING`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, id); err != nil {
			return err
		}
		// An Apple refresh token moves, still encrypted, to the tombstone until
		// Apple confirms its revocation (MarkAppleRevoked).
		if _, err := tx.Exec(ctx, `UPDATE deleted_users d SET apple_token_enc = t.token_enc, apple_revoked_at = NULL
			FROM apple_tokens t WHERE t.user_id = $1 AND d.id = $1`, id); err != nil {
			return fmt.Errorf("keep apple token: %w", err)
		}
		for _, q := range []string{
			`DELETE FROM apple_tokens WHERE user_id=$1`,
			`DELETE FROM push_tokens WHERE user_id=$1`,
			`DELETE FROM consents WHERE user_id=$1`,
			`DELETE FROM user_sources WHERE user_id=$1`,
			`DELETE FROM feedback WHERE user_id=$1`,
			`DELETE FROM score_queue WHERE user_id=$1`,
			`DELETE FROM outbox WHERE user_id=$1`,
			`DELETE FROM notifications_sent WHERE user_id=$1`,
			// Journal: entries before the custom behaviors they name.
			`DELETE FROM journal_entries WHERE user_id=$1`,
			`DELETE FROM journal_days WHERE user_id=$1`,
			`DELETE FROM user_behaviors WHERE user_id=$1`,
			`DELETE FROM behaviors WHERE user_id=$1`,
			// Train: sessions cascade to their exercises and sets; plans to
			// their days, exercises and enrolments.
			`DELETE FROM personal_records WHERE user_id=$1`,
			`DELETE FROM sessions WHERE user_id=$1`,
			`DELETE FROM plan_enrollments WHERE user_id=$1`,
			`DELETE FROM plans WHERE user_id=$1`,
			`DELETE FROM exercises WHERE user_id=$1`,
			`DELETE FROM samples WHERE user_id=$1`,
			`DELETE FROM sleep_segments WHERE user_id=$1`,
			`DELETE FROM workouts WHERE user_id=$1`,
			`DELETE FROM daily_scores WHERE user_id=$1`,
			`DELETE FROM devices WHERE user_id=$1`,
			`DELETE FROM users WHERE id=$1`,
		} {
			if _, err := tx.Exec(ctx, q, id); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	})
}

// SaveAppleToken stores (or replaces) the user's encrypted Sign in with Apple
// refresh token.
func (s *Store) SaveAppleToken(ctx context.Context, userID uuid.UUID, enc []byte) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO apple_tokens (user_id, token_enc) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET token_enc = EXCLUDED.token_enc, created_at = now()`, userID, enc)
	return err
}

// DeletedAppleToken is the encrypted Apple token of a deleted account that
// still has to be revoked; ErrNotFound when there is none (never stored, or
// already revoked).
func (s *Store) DeletedAppleToken(ctx context.Context, id uuid.UUID) ([]byte, error) {
	var enc []byte
	err := s.pool.QueryRow(ctx, `SELECT apple_token_enc FROM deleted_users
		WHERE id=$1 AND apple_token_enc IS NOT NULL AND apple_revoked_at IS NULL`, id).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return enc, err
}

// MarkAppleRevoked drops the token from the tombstone once Apple revoked it.
func (s *Store) MarkAppleRevoked(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE deleted_users SET apple_token_enc = NULL, apple_revoked_at = now() WHERE id=$1`, id)
	return err
}

// PendingAppleRevocations lists deleted accounts whose Apple token is not
// revoked yet, oldest first.
func (s *Store) PendingAppleRevocations(ctx context.Context, limit int) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM deleted_users
		WHERE apple_token_enc IS NOT NULL AND apple_revoked_at IS NULL ORDER BY deleted_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// MarkAuthDeleted records that the Supabase auth user is gone too.
func (s *Store) MarkAuthDeleted(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE deleted_users SET auth_deleted_at = now() WHERE id=$1 AND auth_deleted_at IS NULL`, id)
	return err
}

// PendingAuthDeletions lists deleted accounts whose Supabase auth user has not
// been deleted yet, oldest first.
func (s *Store) PendingAuthDeletions(ctx context.Context, limit int) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM deleted_users WHERE auth_deleted_at IS NULL ORDER BY deleted_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// ResurrectedUsers lists deleted accounts that have a user row again, e.g.
// after a database restore from a backup taken before the deletion (with the
// tombstones re-inserted afterwards; see docs/OPERATIONS.md).
func (s *Store) ResurrectedUsers(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.id FROM deleted_users d JOIN users u ON u.id = d.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

func identityChanged(u model.User, id Identity) bool {
	return (id.Name != "" && id.Name != u.Name) ||
		(id.Email != "" && (u.Email == nil || *u.Email != id.Email)) ||
		(id.Provider != "" && (u.Provider == nil || *u.Provider != id.Provider))
}

// defaultSleepBase and defaultMaxHR are the columns' defaults (migration 0001;
// scoring.DefaultMaxHR).
const (
	defaultSleepBase = 460
	defaultMaxHR     = 190
)

// UpdateUser applies a settings patch and returns the updated row. A
// sleep_base_min makes the baseline manual; sleep_base_manual false goes back
// to the learned estimate, or to the default without one (D67). max_hr works
// the same way with the estimate for the birth year in effect after the patch
// (scoring.EstimatedMaxHR; nil without a birth year): a birth year replaces
// max_hr unless it is manual (D81).
func (s *Store) UpdateUser(ctx context.Context, id uuid.UUID, p model.UserPatch, maxHREstimate *int) (model.User, error) {
	if p.TZ != nil {
		if _, err := time.LoadLocation(*p.TZ); err != nil {
			return model.User{}, fmt.Errorf("invalid tz: %w", err)
		}
	}
	manual := p.SleepBaseManual
	if p.SleepBaseMin != nil {
		t := true
		manual = &t
	}
	u, err := scanUser(s.pool.QueryRow(ctx, `UPDATE users SET
		tz = COALESCE($2, tz),
		birth_year = COALESCE($8::int, birth_year),
		max_hr = CASE
		  WHEN $3::int IS NOT NULL THEN $3::int
		  WHEN $7::boolean THEN max_hr
		  WHEN $7::boolean = false THEN COALESCE($9::int, $10::int)
		  WHEN max_hr_source = 'manual' THEN max_hr
		  WHEN $8::int IS NOT NULL THEN $9::int
		  ELSE max_hr END,
		max_hr_source = CASE
		  WHEN $3::int IS NOT NULL OR $7::boolean THEN 'manual'
		  WHEN $7::boolean = false AND $9::int IS NOT NULL THEN 'estimated'
		  WHEN $7::boolean = false THEN 'default'
		  WHEN max_hr_source = 'manual' THEN 'manual'
		  WHEN $8::int IS NOT NULL THEN 'estimated'
		  ELSE max_hr_source END,
		sleep_base_manual = COALESCE($5, sleep_base_manual),
		sleep_base_min = CASE
		  WHEN $4::int IS NOT NULL THEN $4
		  WHEN $5 = false THEN COALESCE(sleep_base_learned_min, $6)
		  ELSE sleep_base_min END,
		sleep_base_source = CASE
		  WHEN COALESCE($5, sleep_base_manual) THEN 'manual'
		  WHEN $5 = false AND sleep_base_learned_min IS NOT NULL THEN 'learned'
		  WHEN $5 = false THEN 'default'
		  ELSE sleep_base_source END
		WHERE id=$1 RETURNING `+userColumns, id, p.TZ, p.MaxHR, p.SleepBaseMin, manual, defaultSleepBase,
		p.MaxHRManual, p.BirthYear, maxHREstimate, defaultMaxHR))
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// SetLearnedBase stores a new learned estimate and, unless the user set the
// baseline by hand, makes it the baseline in use. changed says whether the
// baseline in use moved (its days then need rescoring).
func (s *Store) SetLearnedBase(ctx context.Context, id uuid.UUID, minutes int) (changed bool, err error) {
	err = s.pool.QueryRow(ctx, `WITH old AS (SELECT sleep_base_min FROM users WHERE id=$1 FOR UPDATE)
		UPDATE users u SET sleep_base_learned_min=$2, sleep_base_learned_at=now(),
		  sleep_base_min = CASE WHEN u.sleep_base_manual THEN u.sleep_base_min ELSE $2 END,
		  sleep_base_source = CASE WHEN u.sleep_base_manual THEN 'manual' ELSE 'learned' END
		FROM old WHERE u.id=$1
		RETURNING NOT u.sleep_base_manual AND old.sleep_base_min IS DISTINCT FROM $2`, id, minutes).Scan(&changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return changed, err
}

// TouchDevice records a sync from a device, with the app and OS version it
// sent (nil keeps the stored one). Device IDs are per install, so one phone
// used by two accounts has a row per account.
func (s *Store) TouchDevice(ctx context.Context, userID, deviceID uuid.UUID, name string, appVersion, osVersion *string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO devices (id, user_id, name, last_sync_at, app_version, os_version)
		VALUES ($1,$2,$3,now(),$4,$5)
		ON CONFLICT (user_id, id) DO UPDATE SET last_sync_at = now(),
		  name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE devices.name END,
		  app_version = COALESCE(EXCLUDED.app_version, devices.app_version),
		  os_version = COALESCE(EXCLUDED.os_version, devices.os_version)`,
		deviceID, userID, name, appVersion, osVersion)
	return err
}

// UserIDs lists every user, for a full rescore.
func (s *Store) UserIDs(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// SyncAges counts accounts by the age of their newest device sync, for the
// "testers by last sync" panel (ryft_users_by_last_sync): no user ids leave
// the database.
func (s *Store) SyncAges(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT CASE
		    WHEN d.last IS NULL THEN 'never'
		    WHEN d.last > now() - interval '6 hours' THEN 'under_6h'
		    WHEN d.last > now() - interval '24 hours' THEN '6h_to_24h'
		    WHEN d.last > now() - interval '3 days' THEN '1d_to_3d'
		    ELSE 'over_3d' END, count(*)
		FROM users u LEFT JOIN (SELECT user_id, max(last_sync_at) AS last FROM devices GROUP BY user_id) d ON d.user_id = u.id
		GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	out := map[string]int{"under_6h": 0, "6h_to_24h": 0, "1d_to_3d": 0, "over_3d": 0, "never": 0}
	var age string
	var n int
	_, err = pgx.ForEachRow(rows, []any{&age, &n}, func() error {
		out[age] = n
		return nil
	})
	return out, err
}

// ActiveUsers lists users with a device that synced since the given time.
func (s *Store) ActiveUsers(ctx context.Context, since time.Time) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT user_id FROM devices WHERE last_sync_at >= $1`, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// TryRequestRecompute rate-limits recompute requests per user across all api
// replicas: true (and the timestamp recorded) only if the previous request is
// at least minInterval ago.
func (s *Store) TryRequestRecompute(ctx context.Context, userID uuid.UUID, minInterval time.Duration) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET recompute_requested_at = now()
		WHERE id=$1 AND (recompute_requested_at IS NULL OR recompute_requested_at < now() - make_interval(secs => $2))`,
		userID, minInterval.Seconds())
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ---- ingest ----------------------------------------------------------------

func (s *Store) UpsertSamples(ctx context.Context, userID uuid.UUID, in []model.Sample) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, x := range in {
		// An identical re-send is neither rewritten nor counted (SyncResponse → ignored.duplicate).
		b.Queue(`INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (user_id, type, start_at, end_at, source) DO UPDATE SET value = EXCLUDED.value
			WHERE samples.value IS DISTINCT FROM EXCLUDED.value`,
			userID, x.Type, x.Start, x.End, x.Value, x.Unit, x.Source)
	}
	return s.runBatch(ctx, b)
}

func (s *Store) UpsertSleep(ctx context.Context, userID uuid.UUID, in []model.SleepSegment) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, x := range in {
		b.Queue(`INSERT INTO sleep_segments (user_id, start_at, end_at, stage, source)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
			userID, x.Start, x.End, x.Stage, x.Source)
	}
	return s.runBatch(ctx, b)
}

func (s *Store) UpsertWorkouts(ctx context.Context, userID uuid.UUID, in []model.Workout) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, w := range in {
		b.Queue(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, avg_hr, max_hr, energy_kcal, distance_m, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (user_id, id) DO UPDATE SET activity=EXCLUDED.activity, start_at=EXCLUDED.start_at, end_at=EXCLUDED.end_at,
			  avg_hr=EXCLUDED.avg_hr, max_hr=EXCLUDED.max_hr, energy_kcal=EXCLUDED.energy_kcal,
			  distance_m=EXCLUDED.distance_m, source=EXCLUDED.source
			WHERE (workouts.activity, workouts.start_at, workouts.end_at, workouts.avg_hr, workouts.max_hr, workouts.energy_kcal,
			  workouts.distance_m, workouts.source) IS DISTINCT FROM (EXCLUDED.activity, EXCLUDED.start_at, EXCLUDED.end_at,
			  EXCLUDED.avg_hr, EXCLUDED.max_hr, EXCLUDED.energy_kcal, EXCLUDED.distance_m, EXCLUDED.source)`,
			w.ID, userID, w.Activity, w.Start, w.End, w.AvgHR, w.MaxHR, w.EnergyKcal, w.DistanceM, w.Source)
	}
	return s.runBatch(ctx, b)
}

func (s *Store) runBatch(ctx context.Context, b *pgx.Batch) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	res := tx.SendBatch(ctx, b)
	n := 0
	for i := 0; i < b.Len(); i++ {
		tag, err := res.Exec()
		if err != nil {
			_ = res.Close()
			return 0, fmt.Errorf("batch item %d: %w", i, err)
		}
		n += int(tag.RowsAffected())
	}
	if err := res.Close(); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// ---- reads for scoring -----------------------------------------------------

func (s *Store) SamplesBetween(ctx context.Context, userID uuid.UUID, typ string, from, to time.Time) ([]model.Sample, error) {
	rows, err := s.pool.Query(ctx, `SELECT type, start_at, end_at, value, unit, source FROM samples
		WHERE user_id=$1 AND type=$2 AND start_at >= $3 AND start_at < $4 ORDER BY start_at`,
		userID, typ, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Sample, error) {
		var x model.Sample
		err := r.Scan(&x.Type, &x.Start, &x.End, &x.Value, &x.Unit, &x.Source)
		return x, err
	})
}

// SleepEndingBetween returns segments whose end falls in [from,to).
func (s *Store) SleepEndingBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]model.SleepSegment, error) {
	rows, err := s.pool.Query(ctx, `SELECT start_at, end_at, stage, source FROM sleep_segments
		WHERE user_id=$1 AND end_at >= $2 AND end_at < $3 ORDER BY start_at`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.SleepSegment, error) {
		var x model.SleepSegment
		err := r.Scan(&x.Start, &x.End, &x.Stage, &x.Source)
		return x, err
	})
}

// WorkoutPeakHR is the highest heart-rate sample inside a workout, per local
// day of the workout's start, for workouts starting at or after since (the
// max-hr-observed card). The samples, not the app's max_hr: every source the
// same way, as scoring sees them.
func (s *Store) WorkoutPeakHR(ctx context.Context, userID uuid.UUID, since time.Time, tz string) (map[string]float64, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(w.start_at AT TIME ZONE $3, 'YYYY-MM-DD'), max(s.value)
		FROM workouts w JOIN samples s ON s.user_id = w.user_id AND s.type = 'heart_rate'
		  AND s.start_at >= w.start_at AND s.start_at <= w.end_at
		WHERE w.user_id = $1 AND w.start_at >= $2
		GROUP BY 1`, userID, since, tz)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	var day string
	var bpm float64
	_, err = pgx.ForEachRow(rows, []any{&day, &bpm}, func() error {
		out[day] = bpm
		return nil
	})
	return out, err
}

func (s *Store) WorkoutsBetween(ctx context.Context, userID uuid.UUID, from, to time.Time) ([]model.Workout, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, activity, start_at, end_at, avg_hr, max_hr, energy_kcal, distance_m, source, strain,
		muscular_load, session_id, model_version
		FROM workouts WHERE user_id=$1 AND start_at >= $2 AND start_at < $3 ORDER BY start_at`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Workout, error) {
		var w model.Workout
		err := r.Scan(&w.ID, &w.Activity, &w.Start, &w.End, &w.AvgHR, &w.MaxHR, &w.EnergyKcal, &w.DistanceM, &w.Source, &w.Strain,
			&w.MuscularLoad, &w.SessionID, &w.ModelVersion)
		return w, err
	})
}

// SetWorkoutLoad stores what scoring computed for one workout: strain, TRIMP
// and minutes in the five heart-rate-reserve zones.
func (s *Store) SetWorkoutLoad(ctx context.Context, userID, id uuid.UUID, strain, trimp float64, zones [5]float64, version string) error {
	z, err := json.Marshal(roundZones(zones[:]))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE workouts SET strain=$3, trimp=$4, zone_minutes=$5, model_version=$6 WHERE user_id=$1 AND id=$2`,
		userID, id, strain, math.Round(trimp*10)/10, z, version)
	return err
}

// WorkoutRow is a workout with the load scoring attached to it. ZoneMinutes and
// TRIMP are nil until the workout's day has been scored.
type WorkoutRow struct {
	model.Workout
	ZoneMinutes []float64
	TRIMP       *float64
}

func (s *Store) Workout(ctx context.Context, userID, id uuid.UUID) (WorkoutRow, error) {
	var w WorkoutRow
	var zones []byte
	err := s.pool.QueryRow(ctx, `SELECT id, activity, start_at, end_at, avg_hr, max_hr, energy_kcal, distance_m,
		source, strain, zone_minutes, trimp, muscular_load, session_id, model_version FROM workouts WHERE user_id=$1 AND id=$2`, userID, id).
		Scan(&w.ID, &w.Activity, &w.Start, &w.End, &w.AvgHR, &w.MaxHR, &w.EnergyKcal, &w.DistanceM,
			&w.Source, &w.Strain, &zones, &w.TRIMP, &w.MuscularLoad, &w.SessionID, &w.ModelVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return w, ErrNotFound
	}
	if err != nil {
		return w, err
	}
	if len(zones) > 0 {
		if err := json.Unmarshal(zones, &w.ZoneMinutes); err != nil {
			return w, fmt.Errorf("workout %s zone_minutes: %w", id, logx.JSONError(err))
		}
	}
	return w, nil
}

func roundZones(z []float64) []float64 {
	out := make([]float64, len(z))
	for k, v := range z {
		out[k] = math.Round(v*10) / 10
	}
	return out
}

// History is the per-day input the scoring baselines need. What the later
// days' rows are computed from (HRV, RHR, RespRate, Strain, SleepNeedMin,
// SleepActualMin, SleepStart, SleepEnd, NapMin: the next day's need subtracts
// this day's naps, which it reads from the raw segments; and HasData: whether
// the day holds any data, calibrating_reason's window) are the row's history
// inputs, historyInputs; the others are left out on purpose.
// TestHistoryInputsCoverHistory fails until a new field is one or the other.
type History struct {
	Day            string
	HRV            *float64
	RHR            *float64
	RespRate       *float64
	Strain         *float64
	SleepNeedMin   *int
	SleepActualMin *int
	Recovery       *int       // the learned baseline's qualifier
	NapMin         *int       // the next night's nap credit
	SleepStart     *time.Time // consistency
	SleepEnd       *time.Time
	Efficiency     *int // the planner (asleep / in bed)
	// HasData: one of dataColumns is set: an overnight HRV, a resting HR or a
	// respiratory rate, a strain (heart-rate minutes or a logged session), a
	// night's sleep, steps or active energy. A range or a rescore also stores
	// days before an account's first data, with none of it; they do not count
	// as history for calibrating_reason. Neither does a day with only an SpO2,
	// a nap, time in bed or a workout without heart rate: rare, and it errs
	// toward short_history.
	HasData bool
}

// dataColumns are the daily_scores columns History.HasData looks at. Each is a
// history input (historyInputs, by value or by presence), so a day gaining or
// losing data re-scores the days after it; TestHistoryInputsCoverHistory
// checks that.
var dataColumns = []string{"hrv_ms", "rhr", "resp_rate", "strain", "sleep_actual_min", "steps", "active_kcal"}

var hasData = "num_nonnulls(" + strings.Join(dataColumns, ", ") + ") > 0"

// ScoreHistory returns up to n scored days strictly before `day`, newest first.
func (s *Store) ScoreHistory(ctx context.Context, userID uuid.UUID, day string, n int) ([]History, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), hrv_ms, rhr, resp_rate, strain, sleep_need_min, sleep_actual_min,
		recovery, nap_min, sleep_start, sleep_end,
		CASE WHEN in_bed_min > 0 THEN round(100.0 * least(sleep_actual_min, in_bed_min) / in_bed_min)::int END,
		`+hasData+`
		FROM daily_scores WHERE user_id=$1 AND day < $2::date ORDER BY day DESC LIMIT $3`, userID, day, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (History, error) {
		var h History
		err := r.Scan(&h.Day, &h.HRV, &h.RHR, &h.RespRate, &h.Strain, &h.SleepNeedMin, &h.SleepActualMin,
			&h.Recovery, &h.NapMin, &h.SleepStart, &h.SleepEnd, &h.Efficiency, &h.HasData)
		return h, err
	})
}

// ---- daily scores ----------------------------------------------------------

// SleepNight is the stored summary of the night that ended on Day. Pointers
// are nil for rows scored before migration 0003.
type SleepNight struct {
	Day         string
	Start, End  *time.Time
	TotalMin    *int
	InBedMin    *int
	DeepMin     *int
	LightMin    *int // HealthKit "core"
	REMMin      *int
	AwakeMin    *int
	HasStages   bool
	Performance *int
	// Read back only; scoring writes them from DailyScore.
	Quality     *int
	Consistency *int
	NapMin      *int
}

// DayLoad is the heart-rate load behind a day's strain. Nil fields mean no
// heart-rate data that day (or a row scored before migration 0004).
type DayLoad struct {
	TRIMP         *float64
	HRMin         *int
	ZoneMinutes   []float64
	MuscularTRIMP *float64 // the day's strength sessions; nil without
	Steps         *int     // read back for DayStrain; written from DailyScore
	ActiveKcal    *int
}

// ScoreRow is everything scoring writes for one day: the score the API serves
// plus the typed columns other views are assembled from.
type ScoreRow struct {
	model.DailyScore
	Sleep SleepNight // zero when no sleep was recorded
	Load  DayLoad
}

// ScoreWrite is what UpsertDailyScore changed.
type ScoreWrite struct {
	// FirstRecovery: the row had no recovery before this write and has one now
	// (the morning push, scored).
	FirstRecovery bool
	// InputsChanged: the row is new, or one of its historyInputs changed, so
	// the stored days whose ScoreHistory window holds it were scored against a
	// history that is gone. Recovery and baseline_days (with its counts and
	// calibrating_reason) are left out on purpose: they follow from the
	// baselines, so a re-score of the days after a change settles instead of
	// chasing itself.
	InputsChanged bool
}

// historyInputs are the daily_scores columns the later days are computed from
// (History): UpsertDailyScore reports a change in any of them. An entry is a
// column, compared by value, or "column IS NULL", compared by presence only
// (HasData needs no more: the steps a day gains with every sync change no
// later day). Constants only; the comparison below is built from them once.
var historyInputs = []string{"hrv_ms", "rhr", "resp_rate", "strain", "sleep_need_min", "sleep_actual_min",
	"sleep_start", "sleep_end", "nap_min", "steps IS NULL", "active_kcal IS NULL"}

var (
	historyColumns   = strings.Join(inputColumns(historyInputs), ", ")
	oldHistoryInputs = "o." + strings.Join(historyInputs, ", o.")
	newHistoryInputs = "daily_scores." + strings.Join(historyInputs, ", daily_scores.")
)

// inputColumns is the column each historyInputs entry reads, once each.
func inputColumns(inputs []string) []string {
	var cols []string
	for _, in := range inputs {
		if c := strings.Fields(in)[0]; !slices.Contains(cols, c) {
			cols = append(cols, c)
		}
	}
	return cols
}

func (s *Store) UpsertDailyScore(ctx context.Context, userID uuid.UUID, row ScoreRow) (ScoreWrite, error) {
	var w ScoreWrite
	d, n, l := row.DailyScore, row.Sleep, row.Load
	details, err := json.Marshal(d.Details)
	if err != nil {
		return w, err
	}
	var zones []byte
	if l.ZoneMinutes != nil {
		if zones, err = json.Marshal(roundZones(l.ZoneMinutes)); err != nil {
			return w, err
		}
	}
	err = s.pool.QueryRow(ctx, `WITH old AS (SELECT recovery, `+historyColumns+`
		  FROM daily_scores WHERE user_id=$1 AND day=$2::date)
		INSERT INTO daily_scores
		(user_id, day, recovery, strain, sleep_performance, sleep_need_min, sleep_actual_min, sleep_debt_min,
		 hrv_ms, rhr, resp_rate, spo2, calibrating, details, readiness, state,
		 sleep_start, sleep_end, in_bed_min, deep_min, light_min, rem_min, awake_min, sleep_has_stages,
		 zone_minutes, trimp, hr_min, hrv_baseline_ms, rhr_baseline, baseline_days, steps, active_kcal,
		 muscular_trimp, strength_min, volume_kg, nap_min, sleep_quality, sleep_consistency,
		 sleep_need_base_min, sleep_need_strain_min, sleep_need_debt_min, sleep_need_nap_min, model_version,
		 baseline_hrv_days, baseline_rhr_days, calibrating_reason, computed_at)
		VALUES ($1,$2::date,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,
		 $25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,now())
		ON CONFLICT (user_id, day) DO UPDATE SET
		 readiness=EXCLUDED.readiness, state=EXCLUDED.state, recovery=EXCLUDED.recovery,
		 strain=EXCLUDED.strain, sleep_performance=EXCLUDED.sleep_performance,
		 sleep_need_min=EXCLUDED.sleep_need_min, sleep_actual_min=EXCLUDED.sleep_actual_min, sleep_debt_min=EXCLUDED.sleep_debt_min,
		 hrv_ms=EXCLUDED.hrv_ms, rhr=EXCLUDED.rhr, resp_rate=EXCLUDED.resp_rate, spo2=EXCLUDED.spo2,
		 calibrating=EXCLUDED.calibrating, details=EXCLUDED.details,
		 sleep_start=EXCLUDED.sleep_start, sleep_end=EXCLUDED.sleep_end, in_bed_min=EXCLUDED.in_bed_min,
		 deep_min=EXCLUDED.deep_min, light_min=EXCLUDED.light_min, rem_min=EXCLUDED.rem_min,
		 awake_min=EXCLUDED.awake_min, sleep_has_stages=EXCLUDED.sleep_has_stages,
		 zone_minutes=EXCLUDED.zone_minutes, trimp=EXCLUDED.trimp, hr_min=EXCLUDED.hr_min,
		 hrv_baseline_ms=EXCLUDED.hrv_baseline_ms, rhr_baseline=EXCLUDED.rhr_baseline,
		 baseline_days=EXCLUDED.baseline_days, steps=EXCLUDED.steps, active_kcal=EXCLUDED.active_kcal,
		 muscular_trimp=EXCLUDED.muscular_trimp, strength_min=EXCLUDED.strength_min, volume_kg=EXCLUDED.volume_kg,
		 nap_min=EXCLUDED.nap_min, sleep_quality=EXCLUDED.sleep_quality, sleep_consistency=EXCLUDED.sleep_consistency,
		 sleep_need_base_min=EXCLUDED.sleep_need_base_min, sleep_need_strain_min=EXCLUDED.sleep_need_strain_min,
		 sleep_need_debt_min=EXCLUDED.sleep_need_debt_min, sleep_need_nap_min=EXCLUDED.sleep_need_nap_min,
		 model_version=EXCLUDED.model_version, baseline_hrv_days=EXCLUDED.baseline_hrv_days,
		 baseline_rhr_days=EXCLUDED.baseline_rhr_days, calibrating_reason=EXCLUDED.calibrating_reason, computed_at=now()
		RETURNING (SELECT recovery FROM old) IS NULL AND recovery IS NOT NULL,
		 NOT EXISTS (SELECT 1 FROM old o WHERE (`+oldHistoryInputs+`) IS NOT DISTINCT FROM (`+newHistoryInputs+`))`,
		userID, d.Day, d.Recovery, d.Strain, d.SleepPerformance, d.SleepNeedMin, d.SleepActualMin, d.SleepDebtMin,
		d.HRVms, d.RHR, d.RespRate, d.SpO2, d.Calibrating, details, d.Readiness, d.State,
		n.Start, n.End, n.InBedMin, n.DeepMin, n.LightMin, n.REMMin, n.AwakeMin, n.HasStages,
		zones, l.TRIMP, l.HRMin, d.HRVBaselineMS, d.RHRBaseline, d.BaselineDays, d.Steps, d.ActiveKcal,
		l.MuscularTRIMP, d.StrengthMin, d.VolumeKG, d.NapMin, d.SleepQuality, d.SleepConsistency,
		d.SleepNeedBaseMin, d.SleepNeedStrainMin, d.SleepNeedDebtMin, d.SleepNeedNapMin, d.ModelVersion,
		d.BaselineHRVDays, d.BaselineRHRDays, d.CalibratingReason).Scan(&w.FirstRecovery, &w.InputsChanged)
	return w, err
}

// ScoredDaysAfter returns up to n stored days in (after, through], oldest
// first: the rows whose history a change on `after` reaches.
func (s *Store) ScoredDaysAfter(ctx context.Context, userID uuid.UUID, after, through string, n int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD') FROM daily_scores
		WHERE user_id=$1 AND day > $2::date AND day <= $3::date ORDER BY day LIMIT $4`, userID, after, through, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// BehindHistoryDays returns up to n stored days from `from` on, oldest first,
// that were scored before their history arrived, judged against the `window`
// stored rows before them as computeDay counts them from ScoreHistory: a
// baseline_days below the smaller of their HRV and resting-HR nights now, an
// HRV or resting-HR baseline missing although the window has such a night (an
// account without HRV has baseline_days 0 either way), or one of the two
// counts behind baseline_days (0027) below its nights now: the larger count
// moves without baseline_days, and its baseline with it. Rows scored before
// 0027 have no counts and are not picked for that. It sees counts and missing
// baselines, not a stale value behind a right count. The scan starts
// 2×window days before `from`, so the windows of daily rows are whole; a
// window cut short by a gap only undercounts, so it can hide a row but never
// picks one a re-score would leave as it is. One pass over the user's rows by
// the primary key, no index of its own.
func (s *Store) BehindHistoryDays(ctx context.Context, userID uuid.UUID, from string, window, n int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD') FROM (
		  SELECT day, baseline_days, hrv_baseline_ms, rhr_baseline, baseline_hrv_days, baseline_rhr_days,
		    count(*) FILTER (WHERE hrv_ms > 0) OVER w AS n_hrv,
		    count(*) FILTER (WHERE rhr > 0) OVER w AS n_rhr
		  FROM daily_scores WHERE user_id=$1 AND day >= $2::date - 2 * $3::int
		  WINDOW w AS (ORDER BY day ROWS BETWEEN $3::int PRECEDING AND 1 PRECEDING)
		) h
		WHERE day >= $2::date AND (baseline_days < least(n_hrv, n_rhr)
		  OR (hrv_baseline_ms IS NULL AND n_hrv > 0) OR (rhr_baseline IS NULL AND n_rhr > 0)
		  OR baseline_hrv_days < n_hrv OR baseline_rhr_days < n_rhr)
		ORDER BY day LIMIT $4`, userID, from, window, n)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// DayLoadOf returns the stored heart-rate load of one day (zero DayLoad when
// the day is not scored).
func (s *Store) DayLoadOf(ctx context.Context, userID uuid.UUID, day string) (DayLoad, error) {
	var l DayLoad
	var zones []byte
	err := s.pool.QueryRow(ctx, `SELECT trimp, hr_min, zone_minutes, muscular_trimp, steps, active_kcal FROM daily_scores
		WHERE user_id=$1 AND day=$2::date`,
		userID, day).Scan(&l.TRIMP, &l.HRMin, &zones, &l.MuscularTRIMP, &l.Steps, &l.ActiveKcal)
	if errors.Is(err, pgx.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if len(zones) > 0 {
		if err := json.Unmarshal(zones, &l.ZoneMinutes); err != nil {
			return l, fmt.Errorf("day %s zone_minutes: %w", day, logx.JSONError(err))
		}
	}
	return l, nil
}

// SleepNights returns the nights in [from, to] (inclusive local days) that have
// recorded sleep, oldest first.
func (s *Store) SleepNights(ctx context.Context, userID uuid.UUID, from, to string) ([]SleepNight, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), sleep_start, sleep_end, sleep_actual_min,
		in_bed_min, deep_min, light_min, rem_min, awake_min, sleep_has_stages, sleep_performance,
		sleep_quality, sleep_consistency, nap_min
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date AND sleep_actual_min IS NOT NULL
		ORDER BY day`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SleepNight, error) {
		var n SleepNight
		err := r.Scan(&n.Day, &n.Start, &n.End, &n.TotalMin, &n.InBedMin, &n.DeepMin, &n.LightMin,
			&n.REMMin, &n.AwakeMin, &n.HasStages, &n.Performance, &n.Quality, &n.Consistency, &n.NapMin)
		return n, err
	})
}

func (s *Store) DailyScores(ctx context.Context, userID uuid.UUID, from, to string) ([]model.DailyScore, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(day,'YYYY-MM-DD'), readiness, state, recovery, strain, steps, active_kcal,
		strength_min, volume_kg, sleep_performance, sleep_need_min,
		sleep_need_base_min, sleep_need_strain_min, sleep_need_debt_min, sleep_need_nap_min, nap_min, sleep_quality, sleep_consistency,
		sleep_actual_min, sleep_debt_min, hrv_ms, rhr, resp_rate, spo2, hrv_baseline_ms, rhr_baseline, baseline_days,
		baseline_hrv_days, baseline_rhr_days, calibrating, calibrating_reason, details, computed_at, model_version
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date ORDER BY day`, userID, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.DailyScore, error) {
		var d model.DailyScore
		var details []byte
		err := r.Scan(&d.Day, &d.Readiness, &d.State, &d.Recovery, &d.Strain, &d.Steps, &d.ActiveKcal, &d.StrengthMin, &d.VolumeKG,
			&d.SleepPerformance, &d.SleepNeedMin,
			&d.SleepNeedBaseMin, &d.SleepNeedStrainMin, &d.SleepNeedDebtMin, &d.SleepNeedNapMin, &d.NapMin, &d.SleepQuality, &d.SleepConsistency,
			&d.SleepActualMin,
			&d.SleepDebtMin, &d.HRVms, &d.RHR, &d.RespRate, &d.SpO2, &d.HRVBaselineMS, &d.RHRBaseline,
			&d.BaselineDays, &d.BaselineHRVDays, &d.BaselineRHRDays, &d.Calibrating, &d.CalibratingReason,
			&details, &d.ComputedAt, &d.ModelVersion)
		if err != nil {
			return d, err
		}
		knownCalibration(&d)
		if len(details) > 0 {
			_ = json.Unmarshal(details, &d.Details)
		}
		if d.Details == nil {
			d.Details = map[string]any{}
		}
		return d, nil
	})
}

// knownCalibration keeps a row's baseline_hrv_days, baseline_rhr_days and
// calibrating_reason only while they agree with its baseline_days and
// calibrating, as every row scoring writes does (baseline_days is the smaller
// count; a reason exactly when calibrating). A row that an older scoring image
// wrote again after this one (a rollback re-pins images; that image does not
// know the columns and leaves them as they were) no longer agrees, and reads as
// a row scored before the fields existed: all three null, "unknown"
// (docs/API.md → calibrating_reason), until it is scored again.
func knownCalibration(d *model.DailyScore) {
	h, r := d.BaselineHRVDays, d.BaselineRHRDays
	if h != nil && r != nil && min(*h, *r) == d.BaselineDays && (d.CalibratingReason != nil) == d.Calibrating {
		return
	}
	d.BaselineHRVDays, d.BaselineRHRDays, d.CalibratingReason = nil, nil, nil
}

// ---- trends ----------------------------------------------------------------

// trendColumns maps a trend metric to its daily_scores column. It is the
// whitelist: only these names are ever interpolated into SQL.
var trendColumns = map[string]string{
	model.MetricReadiness:   "readiness",
	model.MetricRecovery:    "recovery",
	model.MetricStrain:      "strain",
	model.MetricSleep:       "sleep_performance",
	model.MetricSleepMin:    "sleep_actual_min",
	model.MetricHRV:         "hrv_ms",
	model.MetricRHR:         "rhr",
	model.MetricSteps:       "steps",
	model.MetricActiveKcal:  "active_kcal",
	model.MetricStrengthMin: "strength_min",
	model.MetricVolumeKG:    "volume_kg",
	model.MetricSleepQual:   "sleep_quality",
	model.MetricSleepCons:   "sleep_consistency",
	model.MetricNapMin:      "nap_min",
}

var baselineColumns = map[string]string{
	model.MetricHRV: "hrv_baseline_ms",
	model.MetricRHR: "rhr_baseline",
}

// Trend returns metric's non-null values in [from, to] (inclusive local days),
// oldest first, and for hrv/rhr the baseline stored on the last day in the
// range that has one.
func (s *Store) Trend(ctx context.Context, userID uuid.UUID, metric, from, to string) ([]model.TrendPoint, *float64, error) {
	col, ok := trendColumns[metric]
	if !ok {
		return nil, nil, fmt.Errorf("unknown trend metric %q", metric)
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`SELECT to_char(day,'YYYY-MM-DD'), %[1]s::double precision
		FROM daily_scores WHERE user_id=$1 AND day >= $2::date AND day <= $3::date AND %[1]s IS NOT NULL
		ORDER BY day`, col), userID, from, to)
	if err != nil {
		return nil, nil, err
	}
	points, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.TrendPoint, error) {
		var p model.TrendPoint
		err := r.Scan(&p.Day, &p.Value)
		return p, err
	})
	if err != nil {
		return nil, nil, err
	}
	bcol, ok := baselineColumns[metric]
	if !ok {
		return points, nil, nil
	}
	var baseline *float64
	err = s.pool.QueryRow(ctx, fmt.Sprintf(`SELECT %[1]s FROM daily_scores
		WHERE user_id=$1 AND day >= $2::date AND day <= $3::date AND %[1]s IS NOT NULL
		ORDER BY day DESC LIMIT 1`, bcol), userID, from, to).Scan(&baseline)
	if errors.Is(err, pgx.ErrNoRows) {
		return points, nil, nil
	}
	return points, baseline, err
}
