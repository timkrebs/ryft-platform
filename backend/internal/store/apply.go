package store

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
)

// Apply is what one sync asks of the apply transaction.
type Apply struct {
	Days    []string                     // local days the request's records touched
	Deleted []model.DeletedRecord        // the request's deletions (any kind; unknown kinds are skipped)
	Loc     *time.Location               // the user's tz, for the days of deleted rows
	Emit    func(pgx.Tx, []string) error // runs in the transaction with the final days (the outbox insert)

	// Fold, when set, folds same-source copies of the request's new workouts (D-c) after the
	// deletions: the request's sources and its starts +-1 day.
	Fold *FoldScope
}

// Applied is the outcome of Apply.
type Applied struct {
	Deleted     model.SyncDeleted
	Unknown     model.SyncDeleted // distinct ids that named nothing deletable, per kind
	Days        []string          // Apply.Days plus the days of deleted and folded rows, sorted, distinct
	Folded      int               // workouts the same-source fold removed; not part of Deleted
	FoldSkipped bool              // the fold found more than FoldMaxRows rows in scope and did nothing
}

// ApplySync deletes what the request's deletions name (after its upserts, which
// are already stored), queues every touched day and runs Emit, in one
// transaction under the user's apply lock. Everything is scoped to userID: an id
// of another user, an unknown id and a ryft-session workout are all "unknown".
// A failure rolls everything back; sending the request again is harmless.
//
// The transaction is applyTx's steps in this order and no other (D145, D146): lock,
// deletions (samples, sleep, workouts), the same-source fold, queue, Emit.
// apply_trace_test.go pins the statements and their order.
func (s *Store) ApplySync(ctx context.Context, userID uuid.UUID, a Apply) (Applied, error) {
	del := dedupeDeletions(a.Deleted) // reads no row: outside the transaction, before the lock
	var out Applied
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = applyTx(ctx, tx, userID, a, del)
		return err
	})
	if err != nil {
		return Applied{}, err
	}
	countFold(out)
	return out, nil
}

// applyTx is the apply transaction's body. The order matters: the lock first, so two syncs
// of a user (and `ingest fold-workouts`) never interleave their deletes and fold (D145);
// deletions before the fold, so a copy the request deletes is not also folded and counted
// twice (D146); queue and Emit last, with the final days, so the event and the queue rows
// commit or roll back with the deletions (D101, D145).
func applyTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, a Apply, del deletions) (Applied, error) {
	days := newDaySet(a.Loc, a.Days)
	if _, err := tx.Exec(ctx, lockApply, userID); err != nil {
		return Applied{}, err
	}
	var out Applied
	var err error
	if out.Deleted, err = applyDeletions(ctx, tx, userID, del, days); err != nil {
		return Applied{}, err
	}
	if a.Fold != nil {
		if out.Folded, out.FoldSkipped, err = applyFold(ctx, tx, userID, a, days); err != nil {
			return Applied{}, err
		}
	}
	out.Unknown = del.unknown(out.Deleted)
	out.Days = days.sorted()
	if err := queueAndEmit(ctx, tx, userID, out.Days, a.Emit); err != nil {
		return Applied{}, err
	}
	return out, nil
}

// deletions is a request's deletions after dedupe.
type deletions struct {
	ids      map[string][]uuid.UUID // per kind, each id once; the nil id is not asked for
	distinct model.SyncDeleted      // per kind, the distinct ids named (the nil id counts)
}

// dedupeDeletions keeps the three kinds this server knows, each id once. A kind of a later
// app is skipped and not counted; the nil id is counted (it will be unknown) but not looked up.
func dedupeDeletions(deleted []model.DeletedRecord) deletions {
	d := deletions{ids: map[string][]uuid.UUID{}}
	seen := map[string]map[uuid.UUID]struct{}{}
	for _, r := range deleted {
		switch r.Kind {
		case model.DeletedSample, model.DeletedSleep, model.DeletedWorkout:
		default:
			continue
		}
		if seen[r.Kind] == nil {
			seen[r.Kind] = map[uuid.UUID]struct{}{}
		}
		if _, dup := seen[r.Kind][r.ID]; dup {
			continue
		}
		seen[r.Kind][r.ID] = struct{}{}
		if r.ID != uuid.Nil {
			d.ids[r.Kind] = append(d.ids[r.Kind], r.ID)
		}
	}
	d.distinct = model.SyncDeleted{
		Samples:  len(seen[model.DeletedSample]),
		Sleep:    len(seen[model.DeletedSleep]),
		Workouts: len(seen[model.DeletedWorkout]),
	}
	return d
}

// unknown is what named nothing deletable: the distinct ids less the rows removed.
func (d deletions) unknown(deleted model.SyncDeleted) model.SyncDeleted {
	return model.SyncDeleted{
		Samples:  d.distinct.Samples - deleted.Samples,
		Sleep:    d.distinct.Sleep - deleted.Sleep,
		Workouts: d.distinct.Workouts - deleted.Workouts,
	}
}

// daySet is the local days an apply touches, in the user's zone.
type daySet struct {
	loc  *time.Location
	days map[string]struct{}
}

func newDaySet(loc *time.Location, days []string) *daySet {
	s := &daySet{loc: loc, days: map[string]struct{}{}}
	for _, d := range days {
		s.days[d] = struct{}{}
	}
	return s
}

func (s *daySet) addDay(d string)     { s.days[d] = struct{}{} }
func (s *daySet) addTime(t time.Time) { s.days[t.In(s.loc).Format("2006-01-02")] = struct{}{} }

// sorted is the days in order; nil when there are none.
func (s *daySet) sorted() []string { return slices.Sorted(maps.Keys(s.days)) }

// applyDeletions deletes the named rows, samples then sleep then workouts, and adds the
// local days of every removed row to days. A statement runs only if its kind named an id.
func applyDeletions(ctx context.Context, tx pgx.Tx, userID uuid.UUID, d deletions, days *daySet) (model.SyncDeleted, error) {
	var out model.SyncDeleted
	var err error
	if ids := d.ids[model.DeletedSample]; len(ids) > 0 {
		if out.Samples, err = deleteSamples(ctx, tx, userID, ids, days); err != nil {
			return model.SyncDeleted{}, err
		}
	}
	if ids := d.ids[model.DeletedSleep]; len(ids) > 0 {
		if out.Sleep, err = deleteSleep(ctx, tx, userID, ids, days); err != nil {
			return model.SyncDeleted{}, err
		}
	}
	if ids := d.ids[model.DeletedWorkout]; len(ids) > 0 {
		if out.Workouts, err = deleteWorkouts(ctx, tx, userID, ids, days); err != nil {
			return model.SyncDeleted{}, err
		}
	}
	return out, nil
}

// deleteRows runs a DELETE ... RETURNING, calls each per removed row (its columns are in
// dest) and returns how many rows went. what names the table in the error.
func deleteRows(ctx context.Context, tx pgx.Tx, what, sql string, args, dest []any, each func()) (int, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("delete %s: %w", what, err)
	}
	n := 0
	if _, err := pgx.ForEachRow(rows, dest, func() error {
		n++
		each()
		return nil
	}); err != nil {
		return 0, fmt.Errorf("delete %s: %w", what, err)
	}
	return n, nil
}

// deleteSamples: a night sample (hrv, respiration, SpO2) also belongs to the next
// morning's score, so its next day is touched too (model.SampleSpillsToNextDay).
func deleteSamples(ctx context.Context, tx pgx.Tx, userID uuid.UUID, ids []uuid.UUID, days *daySet) (int, error) {
	var typ string
	var at time.Time
	return deleteRows(ctx, tx, "samples", `DELETE FROM samples WHERE user_id=$1 AND hk_id = ANY($2) RETURNING type, start_at`,
		[]any{userID, ids}, []any{&typ, &at}, func() {
			days.addTime(at)
			if model.SampleSpillsToNextDay(typ) {
				days.addTime(at.Add(24 * time.Hour))
			}
		})
}

// deleteSleep: a segment belongs to the day it ends on.
func deleteSleep(ctx context.Context, tx pgx.Tx, userID uuid.UUID, ids []uuid.UUID, days *daySet) (int, error) {
	var at time.Time
	return deleteRows(ctx, tx, "sleep", `DELETE FROM sleep_segments WHERE user_id=$1 AND hk_id = ANY($2) RETURNING end_at`,
		[]any{userID, ids}, []any{&at}, func() { days.addTime(at) })
}

// deleteWorkouts never removes a ryft-session workout (the training log's own row; scoring
// re-creates it). The days follow the fold's rule (planFold): the day, the next (tomorrow's
// sleep need depends on today's strain) and, when a Train session sits on the row, the day
// before (MatchWorkout looks +-12 h).
func deleteWorkouts(ctx context.Context, tx pgx.Tx, userID uuid.UUID, ids []uuid.UUID, days *daySet) (int, error) {
	var at time.Time
	var hasSession bool
	return deleteRows(ctx, tx, "workouts", `DELETE FROM workouts WHERE user_id=$1 AND id = ANY($2) AND source <> $3
				RETURNING start_at, session_id IS NOT NULL`,
		[]any{userID, ids, model.SessionSource}, []any{&at, &hasSession}, func() {
			days.addTime(at)
			days.addTime(at.Add(24 * time.Hour))
			if hasSession {
				days.addTime(at.Add(-24 * time.Hour))
			}
		})
}

// applyFold folds the request's same-source copies (D146) inside the apply transaction,
// which holds the lock, and adds the days of the removed rows. skipped: the scope held more
// than FoldMaxRows rows and nothing was folded.
func applyFold(ctx context.Context, tx pgx.Tx, userID uuid.UUID, a Apply, days *daySet) (folded int, skipped bool, err error) {
	groups, fdays, skipped, err := foldTx(ctx, tx, userID, *a.Fold, a.Loc, FoldMaxRows)
	if err != nil {
		return 0, false, err
	}
	for _, d := range fdays {
		days.addDay(d)
	}
	return foldedCount(groups), skipped, nil
}

// queueAndEmit queues the days for scoring and runs emit (the outbox insert) in the same
// transaction (D101, D145): the event exists if and only if the deletions committed. No
// days, no queue and no event.
func queueAndEmit(ctx context.Context, tx pgx.Tx, userID uuid.UUID, days []string, emit func(pgx.Tx, []string) error) error {
	if len(days) == 0 {
		return nil
	}
	if err := queueDays(ctx, tx, userID, days); err != nil {
		return fmt.Errorf("queue days: %w", err)
	}
	if emit != nil {
		return emit(tx, days)
	}
	return nil
}

// countFold counts a committed fold in the metrics; a rolled-back apply counts nothing.
func countFold(out Applied) {
	if out.FoldSkipped {
		metrics.WorkoutsFoldSkipped.WithLabelValues("too_many").Inc()
	}
	if out.Folded > 0 {
		metrics.WorkoutsFolded.WithLabelValues("same_source").Add(float64(out.Folded))
	}
}
