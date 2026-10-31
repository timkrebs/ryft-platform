package store

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// Apply is what one sync asks of the apply transaction.
type Apply struct {
	Days    []string                     // local days the request's records touched
	Deleted []model.DeletedRecord        // the request's deletions (any kind; unknown kinds are skipped)
	Loc     *time.Location               // the user's tz, for the days of deleted rows
	Emit    func(pgx.Tx, []string) error // runs in the transaction with the final days (the outbox insert)
}

// Applied is the outcome of Apply.
type Applied struct {
	Deleted model.SyncDeleted
	Unknown model.SyncDeleted // distinct ids that named nothing deletable, per kind
	Days    []string          // Apply.Days plus the days of deleted rows, sorted, distinct
}

// ApplySync deletes what the request's deletions name (after its upserts, which
// are already stored), queues every touched day and runs Emit, in one
// transaction under the user's apply lock. Everything is scoped to userID: an id
// of another user, an unknown id and a ryft-session workout are all "unknown".
// A failure rolls everything back; sending the request again is harmless.
func (s *Store) ApplySync(ctx context.Context, userID uuid.UUID, a Apply) (Applied, error) {
	var out Applied
	ids := map[string][]uuid.UUID{}
	seen := map[string]map[uuid.UUID]struct{}{}
	for _, d := range a.Deleted {
		switch d.Kind {
		case model.DeletedSample, model.DeletedSleep, model.DeletedWorkout:
		default:
			continue // a kind of a later app: skipped, not counted
		}
		if seen[d.Kind] == nil {
			seen[d.Kind] = map[uuid.UUID]struct{}{}
		}
		if _, dup := seen[d.Kind][d.ID]; dup {
			continue
		}
		seen[d.Kind][d.ID] = struct{}{}
		if d.ID != uuid.Nil { // the nil id is unknown without asking
			ids[d.Kind] = append(ids[d.Kind], d.ID)
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		days := map[string]struct{}{}
		for _, d := range a.Days {
			days[d] = struct{}{}
		}
		add := func(t time.Time) { days[t.In(a.Loc).Format("2006-01-02")] = struct{}{} }
		out = Applied{}
		if _, err := tx.Exec(ctx, lockApply, userID); err != nil {
			return err
		}
		if len(ids[model.DeletedSample]) > 0 {
			rows, err := tx.Query(ctx, `DELETE FROM samples WHERE user_id=$1 AND hk_id = ANY($2) RETURNING type, start_at`,
				userID, ids[model.DeletedSample])
			if err != nil {
				return fmt.Errorf("delete samples: %w", err)
			}
			var typ string
			var at time.Time
			n := 0
			if _, err := pgx.ForEachRow(rows, []any{&typ, &at}, func() error {
				n++
				add(at)
				if model.SampleSpillsToNextDay(typ) {
					add(at.Add(24 * time.Hour))
				}
				return nil
			}); err != nil {
				return fmt.Errorf("delete samples: %w", err)
			}
			out.Deleted.Samples = n
		}
		if len(ids[model.DeletedSleep]) > 0 {
			rows, err := tx.Query(ctx, `DELETE FROM sleep_segments WHERE user_id=$1 AND hk_id = ANY($2) RETURNING end_at`,
				userID, ids[model.DeletedSleep])
			if err != nil {
				return fmt.Errorf("delete sleep: %w", err)
			}
			var at time.Time
			n := 0
			if _, err := pgx.ForEachRow(rows, []any{&at}, func() error {
				n++
				add(at)
				return nil
			}); err != nil {
				return fmt.Errorf("delete sleep: %w", err)
			}
			out.Deleted.Sleep = n
		}
		if len(ids[model.DeletedWorkout]) > 0 {
			rows, err := tx.Query(ctx, `DELETE FROM workouts WHERE user_id=$1 AND id = ANY($2) AND source <> $3
				RETURNING start_at, session_id IS NOT NULL`, userID, ids[model.DeletedWorkout], model.SessionSource)
			if err != nil {
				return fmt.Errorf("delete workouts: %w", err)
			}
			var at time.Time
			var hasSession bool
			n := 0
			if _, err := pgx.ForEachRow(rows, []any{&at, &hasSession}, func() error {
				n++
				add(at)
				add(at.Add(24 * time.Hour)) // tomorrow's sleep need depends on today's strain
				if hasSession {
					add(at.Add(-24 * time.Hour)) // MatchWorkout looks ±12 h
				}
				return nil
			}); err != nil {
				return fmt.Errorf("delete workouts: %w", err)
			}
			out.Deleted.Workouts = n
		}
		out.Unknown = model.SyncDeleted{
			Samples:  len(seen[model.DeletedSample]) - out.Deleted.Samples,
			Sleep:    len(seen[model.DeletedSleep]) - out.Deleted.Sleep,
			Workouts: len(seen[model.DeletedWorkout]) - out.Deleted.Workouts,
		}
		out.Days = slices.Sorted(func(yield func(string) bool) {
			for d := range days {
				if !yield(d) {
					return
				}
			}
		})
		if len(out.Days) == 0 {
			return nil
		}
		if err := queueDays(ctx, tx, userID, out.Days); err != nil {
			return fmt.Errorf("queue days: %w", err)
		}
		if a.Emit != nil {
			return a.Emit(tx, out.Days)
		}
		return nil
	})
	if err != nil {
		return Applied{}, err
	}
	return out, nil
}
