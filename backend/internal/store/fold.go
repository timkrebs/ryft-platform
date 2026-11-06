package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring/workoutdup"
)

// Same-source workout fold (design D-c). The rule and the choice of the copy to keep are
// the pure workoutdup.FoldClusters (a package of its own: scoring imports the store).
// The store owns the rest: which rows, the lock, the delete and the days.

// FoldScope is the part of a user's workouts a fold reads: Sources, and starts in
// [From, To]. The zero scope is the user's whole history, every source.
type FoldScope struct {
	Sources  []string
	From, To time.Time
}

// FoldGroup is one cluster the fold collapsed: the copy it keeps and the ones it removes.
type FoldGroup struct {
	Day    string      // local day of the kept workout's start
	Keep   uuid.UUID   // the most recently uploaded copy (created_at, then the higher id)
	Folded []uuid.UUID // the removed copies, by start then id
}

type foldRow struct {
	workoutdup.Copy
	hasSession bool
}

// foldRows reads the scope's workouts, ryft-session ones never (they are the training
// log's own rows). lock takes the rows FOR UPDATE.
//
// limit > 0 reads at most limit+1 rows, so the caller can tell "too many" without reading them all.
func foldRows(ctx context.Context, tx pgx.Tx, userID uuid.UUID, sc FoldScope, lock bool, limit int) ([]foldRow, error) {
	q := `SELECT id, activity, start_at, end_at, source, created_at, session_id IS NOT NULL
		FROM workouts WHERE user_id=$1 AND source <> $2`
	args := []any{userID, model.SessionSource}
	if sc.Sources != nil {
		q += ` AND source = ANY($3) AND start_at >= $4 AND start_at <= $5`
		args = append(args, sc.Sources, sc.From, sc.To)
	}
	q += ` ORDER BY start_at, id`
	if limit > 0 {
		q += fmt.Sprintf(` LIMIT %d`, limit+1)
	}
	if lock {
		q += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (foldRow, error) {
		var x foldRow
		err := r.Scan(&x.ID, &x.Activity, &x.Start, &x.End, &x.Source, &x.CreatedAt, &x.hasSession)
		return x, err
	})
}

// planFold folds rows by the same-source rule. days are the local days whose score can
// change, by the rules of a deletion: the day, the next day (tomorrow's sleep need uses
// today's strain) and, for a removed copy a session sits on, the day before.
func planFold(ctx context.Context, rows []foldRow, loc *time.Location) (groups []FoldGroup, days []string, drop []uuid.UUID, err error) {
	cs := make([]workoutdup.Copy, len(rows))
	session := map[uuid.UUID]bool{}
	for i, r := range rows {
		cs[i] = r.Copy
		session[r.ID] = r.hasSession
	}
	seen := map[string]struct{}{}
	add := func(t time.Time) { seen[t.In(loc).Format("2006-01-02")] = struct{}{} }
	folds, err := workoutdup.FoldClustersCtx(ctx, cs)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, f := range folds {
		g := FoldGroup{Day: f.Keep.Start.In(loc).Format("2006-01-02"), Keep: f.Keep.ID}
		add(f.Keep.Start)
		for _, d := range f.Drop {
			g.Folded = append(g.Folded, d.ID)
			drop = append(drop, d.ID)
			add(d.Start)
			// +-24 absolute hours, not the neighbouring local date: on a clock-change day the
			// neighbour can be missed, as in ApplySync and touchedDays (D178).
			add(d.Start.Add(24 * time.Hour))
			if session[d.ID] {
				add(d.Start.Add(-24 * time.Hour))
			}
		}
		groups = append(groups, g)
	}
	days = slices.Sorted(func(yield func(string) bool) {
		for d := range seen {
			if !yield(d) {
				return
			}
		}
	})
	return groups, days, drop, nil
}

// FoldMaxRows bounds the rows one sync's fold reads, locks and clusters. A scope with more
// is skipped (ryft_workouts_fold_skipped_total{reason="too_many"}); fold-workouts, a
// one-off per user, has no bound.
const FoldMaxRows = 5000

// foldTx folds the scope inside tx, which holds the user's apply lock. It returns the
// groups and the days to queue; limit > 0 skips the fold (skipped true) above that many rows.
func foldTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, sc FoldScope, loc *time.Location, limit int) (groups []FoldGroup, days []string, skipped bool, err error) {
	rows, err := foldRows(ctx, tx, userID, sc, true, limit)
	if err != nil {
		return nil, nil, false, fmt.Errorf("fold read: %w", err)
	}
	if limit > 0 && len(rows) > limit {
		return nil, nil, true, nil
	}
	groups, days, drop, err := planFold(ctx, rows, loc)
	if err != nil {
		return nil, nil, false, fmt.Errorf("fold: %w", err)
	}
	if len(drop) == 0 {
		return nil, nil, false, nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM workouts WHERE user_id=$1 AND id = ANY($2) AND source <> $3`,
		userID, drop, model.SessionSource); err != nil {
		return nil, nil, false, fmt.Errorf("fold delete: %w", err)
	}
	return groups, days, false, nil
}

func foldedCount(groups []FoldGroup) int {
	n := 0
	for _, g := range groups {
		n += len(g.Folded)
	}
	return n
}

// FoldWorkouts folds one user's whole history (ingest fold-workouts). With dryRun it
// only reads, takes no lock and writes nothing. Otherwise the fold, the queued days and
// emit (the outbox insert, with the days) run in one transaction under the user's apply
// lock, so a second run finds nothing. Nothing is queued or emitted when nothing folds.
func (s *Store) FoldWorkouts(ctx context.Context, userID uuid.UUID, dryRun bool,
	emit func(pgx.Tx, []string) error) ([]FoldGroup, error) {
	loc, err := s.userLocation(ctx, userID)
	if err != nil {
		return nil, err
	}
	if dryRun {
		var groups []FoldGroup
		err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
			rows, err := foldRows(ctx, tx, userID, FoldScope{}, false, 0)
			if err != nil {
				return err
			}
			groups, _, _, err = planFold(ctx, rows, loc)
			return err
		})
		return groups, err
	}
	var groups []FoldGroup
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		groups = nil
		if _, err := tx.Exec(ctx, lockApply, userID); err != nil {
			return err
		}
		g, days, _, err := foldTx(ctx, tx, userID, FoldScope{}, loc, 0)
		if err != nil || len(g) == 0 {
			return err
		}
		groups = g
		if err := queueDays(ctx, tx, userID, days); err != nil {
			return fmt.Errorf("queue days: %w", err)
		}
		if emit != nil {
			return emit(tx, days)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	metrics.WorkoutsFolded.WithLabelValues("same_source").Add(float64(foldedCount(groups)))
	return groups, nil
}

func (s *Store) userLocation(ctx context.Context, userID uuid.UUID) (*time.Location, error) {
	var tz string
	if err := s.pool.QueryRow(ctx, `SELECT tz FROM users WHERE id=$1`, userID).Scan(&tz); err != nil {
		return nil, fmt.Errorf("user tz: %w", err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("user tz %q: %w", tz, err)
	}
	return loc, nil
}
