package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MergeTable is what a merge does to one table: rows moved to the kept account,
// and rows of the merged account dropped because the kept one has the same (its
// row wins).
type MergeTable struct {
	Table   string `json:"table"`
	Moved   int64  `json:"moved"`
	Dropped int64  `json:"dropped"`
	Rule    string `json:"rule"`
}

// MergeResult is the plan (a dry run) or the outcome of a merge.
type MergeResult struct {
	Tables      []MergeTable
	Consent     string     // "copied", "kept" (to has one) or "none"
	BetaAccess  string     // "copied", "kept" or "none"
	FirstRecord *time.Time // the earliest moved record: where the rescore starts
	ExerciseIDs []uuid.UUID
}

// ErrFromNotFound and ErrToNotFound: one of the two accounts is not there (a
// second merge finds from gone).
var (
	ErrFromNotFound = errors.New("from not found")
	ErrToNotFound   = errors.New("to not found")
)

// ScoredDays counts an account's scored days (merging the smaller into the larger).
func (s *Store) ScoredDays(ctx context.Context, id uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM daily_scores WHERE user_id = $1`, id).Scan(&n)
	return n, err
}

// MergeUsers moves everything of from to to, in one transaction (D90): raw data,
// devices and push tokens, Train, the journal, sources, feedback; a consent and
// beta access only when to has none. Where both have the same row (a sample, a
// device id, a session id, a journal day), to's is kept and from's dropped.
// Scores are not moved: the caller rescores to. from itself is left for the
// account deletion path. With dryRun nothing is kept, and the counts are exact.
func (s *Store) MergeUsers(ctx context.Context, from, to uuid.UUID, dryRun bool) (MergeResult, error) {
	var res MergeResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	first, second := from, to // lock in one order, as two merges may cross
	if strings.Compare(from.String(), to.String()) > 0 {
		first, second = to, from
	}
	for _, id := range []uuid.UUID{first, second} {
		if _, err := tx.Exec(ctx, lockUser, id); err != nil {
			return res, fmt.Errorf("lock: %w", err)
		}
	}
	for id, missing := range map[uuid.UUID]error{from: ErrFromNotFound, to: ErrToNotFound} {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&ok); err != nil {
			return res, fmt.Errorf("users: %w", err)
		}
		if !ok {
			return res, missing
		}
	}
	m := merger{ctx: ctx, tx: tx, from: from, to: to}
	if err := m.run(&res); err != nil {
		return res, err
	}
	if dryRun {
		return res, nil
	}
	return res, tx.Commit(ctx)
}

type merger struct {
	ctx      context.Context
	tx       pgx.Tx
	from, to uuid.UUID
}

// stmt names a statement in an error: its first words.
func stmt(q string) string {
	q = strings.Join(strings.Fields(q), " ")
	if len(q) > 70 {
		q = q[:70] + "…"
	}
	return q
}

func (m merger) count(q string, args ...any) (int64, error) {
	var n int64
	if err := m.tx.QueryRow(m.ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("%s: %w", stmt(q), err)
	}
	return n, nil
}

func (m merger) exec(q string, args ...any) (int64, error) {
	tag, err := m.tx.Exec(m.ctx, q, args...)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", stmt(q), err)
	}
	return tag.RowsAffected(), nil
}

// move updates from's rows to to where keep (a condition on the row s) holds,
// and counts the rest as dropped.
func (m merger) move(table, keep, rule string) (MergeTable, error) {
	t := MergeTable{Table: table, Rule: rule}
	total, err := m.count(`SELECT count(*) FROM `+table+` WHERE user_id = $1`, m.from)
	if err != nil {
		return t, fmt.Errorf("%s: %w", table, err)
	}
	if t.Moved, err = m.exec(`UPDATE `+table+` s SET user_id = $2 WHERE s.user_id = $1 AND `+keep, m.from, m.to); err != nil {
		return t, fmt.Errorf("%s: %w", table, err)
	}
	t.Dropped = total - t.Moved
	return t, nil
}

// columns lists a table's columns in order, for copies that change user_id.
func (m merger) columns(table string) ([]string, error) {
	rows, err := m.tx.Query(m.ctx, `SELECT attname::text FROM pg_attribute WHERE attrelid = $1::regclass AND attnum > 0
		AND NOT attisdropped ORDER BY attnum`, "public."+table)
	if err != nil {
		return nil, fmt.Errorf("columns of %s: %w", table, err)
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("columns of %s: %w", table, err)
	}
	return cols, nil
}

// copyRows inserts copies of from's rows (where where holds on s) as to's.
// Used where children reference a parent by (user_id, id): a parent's user_id
// cannot change under its children, so both are copied and from's originals go
// with the account.
func (m merger) copyRows(table, where string, args ...any) (int64, error) {
	cols, err := m.columns(table)
	if err != nil {
		return 0, err
	}
	sel := make([]string, len(cols))
	for k, c := range cols {
		sel[k] = "s." + c
		if c == "user_id" {
			sel[k] = "$2::uuid"
		}
	}
	return m.exec(`INSERT INTO `+table+` (`+strings.Join(cols, ", ")+`) SELECT `+strings.Join(sel, ", ")+
		` FROM `+table+` s WHERE s.user_id = $1 AND `+where, append([]any{m.from, m.to}, args...)...)
}

// ids collects one uuid column of from's rows, before a copy changes the picture.
func (m merger) ids(q string) ([]uuid.UUID, error) {
	rows, err := m.tx.Query(m.ctx, q, m.from, m.to)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", stmt(q), err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("%s: %w", stmt(q), err)
	}
	return ids, nil
}

func (m merger) run(res *MergeResult) error {
	if err := m.tx.QueryRow(m.ctx, `SELECT min(t) FROM (
		  SELECT min(start_at) AS t FROM samples WHERE user_id = $1
		  UNION ALL SELECT min(start_at) FROM sleep_segments WHERE user_id = $1
		  UNION ALL SELECT min(start_at) FROM workouts WHERE user_id = $1
		  UNION ALL SELECT min(started_at) FROM sessions WHERE user_id = $1) x`, m.from).Scan(&res.FirstRecord); err != nil {
		return fmt.Errorf("first record: %w", err)
	}
	add := func(t MergeTable, err error) error {
		if err == nil {
			res.Tables = append(res.Tables, t)
		}
		return err
	}
	// Raw data: the same record from both accounts is one record.
	if err := add(m.move("samples", `NOT EXISTS (SELECT 1 FROM samples t WHERE t.user_id = $2 AND t.type = s.type
		AND t.start_at = s.start_at AND t.end_at = s.end_at AND t.source = s.source)`, "same type, start, end and source: to's kept")); err != nil {
		return err
	}
	if err := add(m.move("sleep_segments", `NOT EXISTS (SELECT 1 FROM sleep_segments t WHERE t.user_id = $2 AND t.start_at = s.start_at
		AND t.end_at = s.end_at AND t.stage = s.stage AND t.source = s.source)`, "same start, end, stage and source: to's kept")); err != nil {
		return err
	}
	if err := add(m.move("workouts", `NOT EXISTS (SELECT 1 FROM workouts t WHERE t.user_id = $2 AND t.id = s.id)`,
		"same workout id: to's kept")); err != nil {
		return err
	}
	if err := add(m.move("devices", `NOT EXISTS (SELECT 1 FROM devices t WHERE t.user_id = $2 AND t.id = s.id)`,
		"same device id: to's kept")); err != nil {
		return err
	}
	if err := add(m.move("push_tokens", `NOT EXISTS (SELECT 1 FROM push_tokens t WHERE t.user_id = $2 AND t.device_id = s.device_id)`,
		"same device: to's token kept")); err != nil {
		return err
	}
	if err := m.train(res); err != nil {
		return err
	}
	if err := m.journal(res); err != nil {
		return err
	}
	// Sources: one row per name and kind, the later last_seen.
	n, err := m.exec(`INSERT INTO user_sources (user_id, name, kind, bundle_id, last_seen)
		SELECT $2::uuid, name, kind, bundle_id, last_seen FROM user_sources WHERE user_id = $1
		ON CONFLICT (user_id, name, kind) DO UPDATE SET last_seen = GREATEST(user_sources.last_seen, EXCLUDED.last_seen),
		  bundle_id = COALESCE(user_sources.bundle_id, EXCLUDED.bundle_id)`, m.from, m.to)
	if err != nil {
		return err
	}
	res.Tables = append(res.Tables, MergeTable{Table: "user_sources", Moved: n, Rule: "merged per name and kind, the later last_seen"})
	if err := add(m.move("feedback", "true", "all moved")); err != nil {
		return err
	}
	// A consent and beta access only when to has none: the same person gave them.
	var toConsent, fromConsent bool
	if err := m.tx.QueryRow(m.ctx, `SELECT
		  EXISTS (SELECT 1 FROM consents WHERE user_id = $2 AND kind = 'health_processing' AND revoked_at IS NULL),
		  EXISTS (SELECT 1 FROM consents WHERE user_id = $1 AND kind = 'health_processing' AND revoked_at IS NULL)`,
		m.from, m.to).Scan(&toConsent, &fromConsent); err != nil {
		return fmt.Errorf("consents: %w", err)
	}
	switch {
	case toConsent:
		res.Consent = "kept"
	case fromConsent:
		if _, err := m.exec(`INSERT INTO consents (user_id, kind, policy_version, granted_at, app_version)
			SELECT $2::uuid, kind, policy_version, granted_at, app_version FROM consents
			WHERE user_id = $1 AND kind = 'health_processing' AND revoked_at IS NULL ORDER BY granted_at DESC LIMIT 1`, m.from, m.to); err != nil {
			return err
		}
		res.Consent = "copied"
	default:
		res.Consent = "none"
	}
	n, err = m.exec(`UPDATE users t SET beta_access_at = f.beta_access_at FROM users f
		WHERE t.id = $2 AND f.id = $1 AND t.beta_access_at IS NULL AND f.beta_access_at IS NOT NULL`, m.from, m.to)
	if err != nil {
		return err
	}
	var toAccess bool
	if err := m.tx.QueryRow(m.ctx, `SELECT beta_access_at IS NOT NULL FROM users WHERE id = $1`, m.to).Scan(&toAccess); err != nil {
		return fmt.Errorf("beta access: %w", err)
	}
	switch {
	case n > 0:
		res.BetaAccess = "copied"
	case toAccess:
		res.BetaAccess = "kept"
	default:
		res.BetaAccess = "none"
	}
	return nil
}

// train moves custom exercises (one of the same slug on to: from's references
// point to to's), custom plans, enrolments (to's active one wins) and sessions.
func (m merger) train(res *MergeResult) error {
	remap, err := m.count(`WITH pair AS (SELECT f.id AS old, t.id AS new FROM exercises f JOIN exercises t
		  ON t.user_id = $2 AND t.slug = f.slug WHERE f.user_id = $1),
		a AS (UPDATE session_exercises s SET exercise_id = pair.new FROM pair WHERE s.user_id = $1 AND s.exercise_id = pair.old RETURNING 1),
		b AS (UPDATE plan_exercises s SET exercise_id = pair.new FROM pair WHERE s.user_id = $1 AND s.exercise_id = pair.old RETURNING 1)
		SELECT count(*) FROM pair`, m.from, m.to)
	if err != nil {
		return fmt.Errorf("exercises: %w", err)
	}
	if _, err := m.exec(`DELETE FROM personal_records WHERE user_id = $1`, m.from); err != nil { // recomputed for to
		return err
	}
	t, err := m.move("exercises", `NOT EXISTS (SELECT 1 FROM exercises t WHERE t.user_id = $2 AND t.slug = s.slug)`,
		"same slug: to's kept, from's sessions and plans point to it")
	if err != nil {
		return err
	}
	t.Dropped = remap
	res.Tables = append(res.Tables, t)
	for _, table := range []string{"plans", "plan_days", "plan_exercises"} {
		t, err := m.move(table, "true", "all moved (custom plans)")
		if err != nil {
			return err
		}
		res.Tables = append(res.Tables, t)
	}
	t, err = m.move("plan_enrollments", `(s.status <> 'active' OR NOT EXISTS (SELECT 1 FROM plan_enrollments t
		WHERE t.user_id = $2 AND t.status = 'active'))`, "to's active enrolment kept")
	if err != nil {
		return err
	}
	res.Tables = append(res.Tables, t)

	// Sessions with their exercises and sets, copied (they reference each other by
	// user): only the sessions to does not have, and exactly their children.
	sessions, err := m.ids(`SELECT s.id FROM sessions s WHERE s.user_id = $1
		AND NOT EXISTS (SELECT 1 FROM sessions t WHERE t.user_id = $2 AND t.id = s.id)`)
	if err != nil {
		return err
	}
	exRows, err := m.ids(`SELECT se.id FROM session_exercises se JOIN sessions s ON s.user_id = se.user_id AND s.id = se.session_id
		WHERE se.user_id = $1 AND NOT EXISTS (SELECT 1 FROM sessions t WHERE t.user_id = $2 AND t.id = s.id)`)
	if err != nil {
		return err
	}
	if res.ExerciseIDs, err = m.ids(`SELECT DISTINCT se.exercise_id FROM session_exercises se JOIN sessions s
		ON s.user_id = se.user_id AND s.id = se.session_id WHERE se.user_id = $1
		AND NOT EXISTS (SELECT 1 FROM sessions t WHERE t.user_id = $2 AND t.id = s.id)`); err != nil {
		return err
	}
	total, err := m.count(`SELECT count(*) FROM sessions WHERE user_id = $1`, m.from)
	if err != nil {
		return err
	}
	moved, err := m.copyRows("sessions", `s.id = ANY($3)`, sessions)
	if err != nil {
		return fmt.Errorf("sessions: %w", err)
	}
	if _, err := m.copyRows("session_exercises", `s.id = ANY($3)`, exRows); err != nil {
		return fmt.Errorf("session_exercises: %w", err)
	}
	if _, err := m.copyRows("session_sets", `s.session_exercise_id = ANY($3)`, exRows); err != nil {
		return fmt.Errorf("session_sets: %w", err)
	}
	res.Tables = append(res.Tables, MergeTable{Table: "sessions", Moved: moved, Dropped: total - moved,
		Rule: "same session id: to's kept (with its exercises and sets)"})
	return nil
}

// journal moves custom behaviors (one of the same slug on to: from's entries
// point to it), choices and days (to's day kept, with its entries).
func (m merger) journal(res *MergeResult) error {
	remap, err := m.count(`WITH pair AS (SELECT f.id AS old, t.id AS new FROM behaviors f JOIN behaviors t
		  ON t.user_id = $2 AND t.slug = f.slug WHERE f.user_id = $1),
		a AS (UPDATE journal_entries s SET behavior_id = pair.new FROM pair WHERE s.user_id = $1 AND s.behavior_id = pair.old RETURNING 1),
		b AS (DELETE FROM user_behaviors s USING pair WHERE s.user_id = $1 AND s.behavior_id = pair.old RETURNING 1)
		SELECT count(*) FROM pair`, m.from, m.to)
	if err != nil {
		return fmt.Errorf("behaviors: %w", err)
	}
	t, err := m.move("behaviors", `NOT EXISTS (SELECT 1 FROM behaviors t WHERE t.user_id = $2 AND t.slug = s.slug)`,
		"same slug: to's kept, from's entries point to it")
	if err != nil {
		return err
	}
	t.Dropped = remap
	res.Tables = append(res.Tables, t)
	if t, err = m.move("user_behaviors", `NOT EXISTS (SELECT 1 FROM user_behaviors t WHERE t.user_id = $2 AND t.behavior_id = s.behavior_id)`,
		"to's choice kept"); err != nil {
		return err
	}
	res.Tables = append(res.Tables, t)
	total, err := m.count(`SELECT count(*) FROM journal_days WHERE user_id = $1`, m.from)
	if err != nil {
		return err
	}
	// The days to does not have, fixed before the copy: a day to already had keeps
	// exactly its own entries.
	var days []time.Time
	rows, err := m.tx.Query(m.ctx, `SELECT s.day FROM journal_days s WHERE s.user_id = $1
		AND NOT EXISTS (SELECT 1 FROM journal_days t WHERE t.user_id = $2 AND t.day = s.day)`, m.from, m.to)
	if err != nil {
		return fmt.Errorf("journal days: %w", err)
	}
	if days, err = pgx.CollectRows(rows, pgx.RowTo[time.Time]); err != nil {
		return fmt.Errorf("journal days: %w", err)
	}
	moved, err := m.copyRows("journal_days", `s.day = ANY($3)`, days)
	if err != nil {
		return fmt.Errorf("journal_days: %w", err)
	}
	if _, err := m.copyRows("journal_entries", `s.day = ANY($3)`, days); err != nil {
		return fmt.Errorf("journal_entries: %w", err)
	}
	res.Tables = append(res.Tables, MergeTable{Table: "journal_days", Moved: moved, Dropped: total - moved,
		Rule: "same day: to's kept, with its entries"})
	return nil
}
