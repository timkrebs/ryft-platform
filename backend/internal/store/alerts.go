package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// ACTIVITY_SCORED's reads and its ledger (0041, D253, D254). notify decides; this file
// only reads what the rules need and records the decision under a per-user lock.

// The outcomes a workout_alerts row carries (the table's CHECK lists the same nine).
const (
	AlertSending   = "sending"   // claimed, being sent: counts toward the cap
	AlertSent      = "sent"      // at least one device took it: counts toward the cap
	AlertFailed    = "failed"    // sent, no device took it
	AlertCapped    = "capped"    // the 4th or later of its local day
	AlertMuted     = "muted"     // the category is off
	AlertQuiet     = "quiet"     // quiet hours
	AlertNoToken   = "no_token"  // no registered device
	AlertBackfill  = "backfill"  // first stored by a new device's sync
	AlertInherited = "inherited" // a copy of a workout that was decided
	// AlertDuplicate is never stored: DecideWorkoutAlert's answer when the workout is decided already.
	AlertDuplicate = "duplicate"
)

// ActivityWindow is how long after its end a workout may still be announced; the scan's
// SQL and the pure rule in notify share it.
const ActivityWindow = 12 * time.Hour

// insertAlert writes one ledger row; a workout already there keeps its row.
const insertAlert = `INSERT INTO workout_alerts (user_id, workout_id, source, activity, start_at, end_at, day, outcome)
	VALUES ($1, $2, $3, $4, $5, $6, $7::text::date, $8) ON CONFLICT DO NOTHING`

// activityScanDays bounds the scan by workouts_user_start: a workout that ended within
// ActivityWindow started within this many days.
const activityScanDays = 7

// WorkoutRef is one stored workout as inheritance compares it (the fold's own fields).
type WorkoutRef struct {
	ID       uuid.UUID
	Source   string
	Activity string
	Start    time.Time
	End      time.Time
}

// AlertMark is a decided workout: the ledger row. Day is the local day of Start at the
// decision ("YYYY-MM-DD", the day the cap counts). Outcome is set on the rows
// DecideWorkoutAlert reads; for the row it writes the decide function returns it.
// Members is input of DecideWorkoutAlert only (never set on a row it reads): the workouts hidden
// behind the one being decided. They are recorded with it, as inherited, so the group's decision
// outlives its primary: delete the primary and a member that becomes visible is decided already.
type AlertMark struct {
	WorkoutRef
	Day     string
	Outcome string
	Members []WorkoutRef
}

// ActivityCandidate is a workout the scan found: stored, visible, not decided, ended within
// ActivityWindow. It carries what the rules read and nothing else of the workout (no heart
// rate, energy or distance: ryft_notify may not read them).
type ActivityCandidate struct {
	WorkoutRef
	SourceBundleID *string
	DeviceID       *uuid.UUID
	Strain         *float64
	SessionID      *uuid.UUID
	CreatedAt      time.Time    // when the row was inserted: the sync that first stored it
	DeviceSeen     *time.Time   // devices.created_at of (user, DeviceID); nil: no device on record
	Members        []WorkoutRef // the workouts hidden behind this one (duplicate_of = ID)
}

// ActivityCandidates are the user's workouts that ended within ActivityWindow before now, are
// visible (the ListWorkoutsBetween rule: not a duplicate whose primary exists), and have no
// ledger row. Oldest start first, so a day's cap goes to the earlier workouts. Every rule but
// the three in the WHERE is the pure notify.ActivityRule's.
func (s *Store) ActivityCandidates(ctx context.Context, userID uuid.UUID, now time.Time) ([]ActivityCandidate, error) {
	rows, err := s.pool.Query(ctx, `SELECT w.id, w.source, w.activity, w.start_at, w.end_at, w.source_bundle_id, w.device_id,
		  w.strain, w.session_id, w.created_at, d.created_at
		FROM workouts w LEFT JOIN devices d ON d.user_id = w.user_id AND d.id = w.device_id
		WHERE w.user_id = $1
		  AND w.start_at >= $2::timestamptz - make_interval(days => $4)
		  AND w.end_at >= $2::timestamptz - make_interval(secs => $5)
		  AND NOT (w.duplicate_of IS NOT NULL AND w.source <> $3 AND EXISTS (SELECT 1 FROM workouts p
		      WHERE p.user_id = w.user_id AND p.id = w.duplicate_of AND p.duplicate_of IS NULL))
		  AND NOT EXISTS (SELECT 1 FROM workout_alerts a WHERE a.user_id = w.user_id AND a.workout_id = w.id)
		ORDER BY w.start_at, w.id`, userID, now, model.SessionSource, activityScanDays, ActivityWindow.Seconds())
	if err != nil {
		return nil, fmt.Errorf("activity candidates: %w", err)
	}
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ActivityCandidate, error) {
		var c ActivityCandidate
		err := r.Scan(&c.ID, &c.Source, &c.Activity, &c.Start, &c.End, &c.SourceBundleID, &c.DeviceID,
			&c.Strain, &c.SessionID, &c.CreatedAt, &c.DeviceSeen)
		return c, err
	})
	if err != nil || len(cands) == 0 {
		return cands, err
	}
	// The members of every candidate in one read; the start bound is the scan's, padded by
	// two days as ListWorkoutsBetween's duplicate read is.
	ids := make([]uuid.UUID, len(cands))
	at := make(map[uuid.UUID]int, len(cands))
	for k, c := range cands {
		ids[k], at[c.ID] = c.ID, k
	}
	mrows, err := s.pool.Query(ctx, `SELECT duplicate_of, id, source, activity, start_at, end_at FROM workouts
		WHERE user_id = $1 AND duplicate_of = ANY($2)
		  AND start_at >= $3::timestamptz - make_interval(days => $4)
		ORDER BY start_at, id`, userID, ids, now, activityScanDays+2)
	if err != nil {
		return nil, fmt.Errorf("activity members: %w", err)
	}
	type member struct {
		of  uuid.UUID
		ref WorkoutRef
	}
	members, err := pgx.CollectRows(mrows, func(r pgx.CollectableRow) (member, error) {
		var m member
		err := r.Scan(&m.of, &m.ref.ID, &m.ref.Source, &m.ref.Activity, &m.ref.Start, &m.ref.End)
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("activity members: %w", err)
	}
	for _, m := range members {
		if k, ok := at[m.of]; ok {
			cands[k].Members = append(cands[k].Members, m.ref)
		}
	}
	return cands, nil
}

// DecideWorkoutAlert records the decision for one workout, once and under the user's lock
// (7419008): it prunes the ledger rows whose workout ended over two days ago, reads the
// user's rows, and asks decide for the outcome given them and how many alerts of the mark's
// local day are sending or sent. decide returns AlertDuplicate when the workout is decided
// already (nothing is written), else the outcome to store. The caller sends when the answer
// is AlertSending and then calls FinishWorkoutAlert, or ReleaseWorkoutAlert when APNs could not
// be reached. Two notify instances never both pass the cap or both claim a workout.
func (s *Store) DecideWorkoutAlert(ctx context.Context, userID uuid.UUID, mark AlertMark,
	decide func(ledger []AlertMark, sentOnDay int) string) (string, error) {
	outcome := ""
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		outcome = ""
		if _, err := tx.Exec(ctx, lockAlerts, userID); err != nil {
			return fmt.Errorf("lock: %w", err)
		}
		// A row whose end is over two days old cannot match a candidate (its end is within 12 h, the
		// fold's rule 120 s): data minimisation, the ledger stays a few rows.
		if _, err := tx.Exec(ctx, `DELETE FROM workout_alerts WHERE user_id = $1 AND end_at < now() - interval '2 days'`, userID); err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT workout_id, source, activity, start_at, end_at, to_char(day, 'YYYY-MM-DD'), outcome
			FROM workout_alerts WHERE user_id = $1`, userID)
		if err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
		ledger, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AlertMark, error) {
			var a AlertMark
			err := r.Scan(&a.ID, &a.Source, &a.Activity, &a.Start, &a.End, &a.Day, &a.Outcome)
			return a, err
		})
		if err != nil {
			return fmt.Errorf("ledger: %w", err)
		}
		sentOnDay := 0
		for _, a := range ledger {
			if a.Day == mark.Day && (a.Outcome == AlertSending || a.Outcome == AlertSent) {
				sentOnDay++
			}
		}
		outcome = decide(ledger, sentOnDay)
		if outcome == AlertDuplicate {
			return nil
		}
		tag, err := tx.Exec(ctx, insertAlert, userID, mark.ID, mark.Source, mark.Activity, mark.Start, mark.End, mark.Day, outcome)
		if err != nil {
			return fmt.Errorf("record: %w", err)
		}
		if tag.RowsAffected() == 0 { // impossible under the lock; kept so a row never is sent twice
			outcome = AlertDuplicate
			return nil
		}
		// The workouts hidden behind this one are decided with it, in the same transaction. They are
		// inherited, so they never count toward the cap (it counts sending and sent), and one that
		// has a row already keeps it.
		for _, m := range mark.Members {
			if _, err := tx.Exec(ctx, insertAlert, userID, m.ID, m.Source, m.Activity, m.Start, m.End, mark.Day, AlertInherited); err != nil {
				return fmt.Errorf("record member: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return outcome, nil
}

// FinishWorkoutAlert records how a claimed send ended: AlertSent or AlertFailed and how many
// devices APNs accepted. Only a row still sending changes.
func (s *Store) FinishWorkoutAlert(ctx context.Context, userID, workoutID uuid.UUID, outcome string, devices int) error {
	_, err := s.pool.Exec(ctx, `UPDATE workout_alerts SET outcome = $3, devices = $4
		WHERE user_id = $1 AND workout_id = $2 AND outcome = 'sending'`, userID, workoutID, outcome, devices)
	return err
}

// ReleaseWorkoutAlert gives a claim back when no device was reached because APNs could not be:
// the row goes, the event is retried and decides the workout again. Only a row still sending.
// memberIDs are the ids DecideWorkoutAlert recorded as inherited with it (AlertMark.Members): their
// inherited rows go in the same statement, and only when the sending row did. A claim reaches sending
// only when no member had a row (else it would be inherited), so this claim wrote every one of them;
// left behind, they would make the retry find its own group "decided" and never send. Idempotent.
func (s *Store) ReleaseWorkoutAlert(ctx context.Context, userID, workoutID uuid.UUID, memberIDs []uuid.UUID) error {
	if memberIDs == nil {
		memberIDs = []uuid.UUID{}
	}
	_, err := s.pool.Exec(ctx, `WITH released AS (
			DELETE FROM workout_alerts WHERE user_id = $1 AND workout_id = $2 AND outcome = 'sending' RETURNING 1)
		DELETE FROM workout_alerts WHERE user_id = $1 AND workout_id = ANY($3) AND outcome = 'inherited'
			AND EXISTS (SELECT 1 FROM released)`, userID, workoutID, memberIDs)
	return err
}
