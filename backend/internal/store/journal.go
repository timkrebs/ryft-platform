package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/model"
)

// ErrLimit: the user has the most custom behaviors allowed (422).
var ErrLimit = errors.New("limit reached")

const behaviorColumns = `b.id, b.slug, b.name_en, b.name_de, b.category, b.kind, b.default_on,
	COALESCE(ub.enabled, b.default_on), b.user_id IS NOT NULL, b.sort`

func scanBehavior(r pgx.Row) (model.Behavior, error) {
	var b model.Behavior
	err := r.Scan(&b.ID, &b.Slug, &b.NameEN, &b.NameDE, &b.Category, &b.Kind, &b.DefaultOn, &b.Enabled, &b.IsCustom, &b.Sort)
	b.Name = b.NameEN
	return b, err
}

// Behaviors is the catalog for a user: every built-in and the user's own, not
// archived, with the user's choice; by category (model.BehaviorCategories
// order), then sort and name.
func (s *Store) Behaviors(ctx context.Context, userID uuid.UUID) ([]model.Behavior, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+behaviorColumns+` FROM behaviors b
		LEFT JOIN user_behaviors ub ON ub.behavior_id = b.id AND ub.user_id = $1
		WHERE (b.user_id IS NULL OR b.user_id = $1) AND NOT b.archived
		ORDER BY array_position($2::text[], b.category), b.sort, b.name_en`, userID, model.BehaviorCategories)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.Behavior, error) { return scanBehavior(r) })
}

// SetBehaviorsEnabled stores the user's choice. ErrNotFound when an id is not a
// built-in or the user's own (or is archived); nothing is changed then.
func (s *Store) SetBehaviorsEnabled(ctx context.Context, userID uuid.UUID, enabled map[uuid.UUID]bool) error {
	ids := make([]uuid.UUID, 0, len(enabled))
	on := make([]bool, 0, len(enabled))
	for id, v := range enabled {
		ids, on = append(ids, id), append(on, v)
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var visible int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM behaviors WHERE id = ANY($2) AND NOT archived
			AND (user_id IS NULL OR user_id = $1)`, userID, ids).Scan(&visible); err != nil {
			return err
		}
		if visible != len(ids) {
			return ErrNotFound
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_behaviors (user_id, behavior_id, enabled)
			SELECT $1, id, v FROM unnest($2::uuid[], $3::boolean[]) AS t(id, v)
			ON CONFLICT (user_id, behavior_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`, userID, ids, on)
		return err
	})
}

// SaveCustomBehavior creates the user's behavior (enabled) or, for an id sent
// again, updates its names, kind and category (created false). ErrTaken when the
// id is a built-in or another user's; ErrLimit past behavior.MaxCustom.
func (s *Store) SaveCustomBehavior(ctx context.Context, userID uuid.UUID, b model.Behavior) (model.Behavior, bool, error) {
	var created bool
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		own, exists, err := owner(ctx, tx, "behaviors", b.ID)
		if err != nil {
			return err
		}
		if exists && (own == nil || *own != userID) {
			return ErrTaken
		}
		if exists {
			_, err := tx.Exec(ctx, `UPDATE behaviors SET name_en=$3, name_de=$4, kind=$5, category=$6, archived=false, updated_at=now()
				WHERE id=$1 AND user_id=$2`, b.ID, userID, b.NameEN, b.NameDE, b.Kind, b.Category)
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM behaviors WHERE user_id=$1 AND NOT archived`, userID).Scan(&n); err != nil {
			return err
		}
		if n >= behavior.MaxCustom {
			return ErrLimit
		}
		slug, err := freeBehaviorSlug(ctx, tx, userID, behavior.Slug(b.NameEN))
		if err != nil {
			return err
		}
		created = true
		if _, err := tx.Exec(ctx, `INSERT INTO behaviors (id, user_id, slug, name_en, name_de, category, kind, default_on, sort)
			VALUES ($1,$2,$3,$4,$5,$6,$7,false,1000)`, b.ID, userID, slug, b.NameEN, b.NameDE, b.Category, b.Kind); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO user_behaviors (user_id, behavior_id, enabled) VALUES ($1,$2,true)
			ON CONFLICT (user_id, behavior_id) DO UPDATE SET enabled = true, updated_at = now()`, userID, b.ID)
		return err
	})
	if err != nil {
		return model.Behavior{}, false, err
	}
	out, err := scanBehavior(s.pool.QueryRow(ctx, `SELECT `+behaviorColumns+` FROM behaviors b
		LEFT JOIN user_behaviors ub ON ub.behavior_id = b.id AND ub.user_id = $1 WHERE b.id = $2`, userID, b.ID))
	return out, created, err
}

func freeBehaviorSlug(ctx context.Context, tx pgx.Tx, userID uuid.UUID, base string) (string, error) {
	if base == "" {
		base = "behavior"
	}
	rows, err := tx.Query(ctx, `SELECT slug FROM behaviors WHERE user_id=$1 AND (slug = $2 OR slug LIKE $2 || '\_%')`, userID, base)
	if err != nil {
		return "", err
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, t := range taken {
		used[t] = true
	}
	slug := base
	for n := 2; used[slug]; n++ {
		slug = base + "_" + strconv.Itoa(n)
	}
	return slug, nil
}

// ArchiveBehavior removes the user's own behavior from the catalog (its
// entries stay). ErrForbidden for a built-in; an unknown or already archived id
// is fine (idempotent).
func (s *Store) ArchiveBehavior(ctx context.Context, userID, id uuid.UUID) error {
	var own *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT user_id FROM behaviors WHERE id=$1`, id).Scan(&own)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if own == nil {
		return ErrForbidden
	}
	if *own != userID {
		return nil // not the user's: as unknown
	}
	_, err = s.pool.Exec(ctx, `UPDATE behaviors SET archived=true, updated_at=now() WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}

// minutes and clock convert a time behavior's value ("HH:MM" ↔ minutes after midnight).
func minutes(hhmm string) float64 {
	t, _ := time.Parse("15:04", hhmm)
	return float64(t.Hour()*60 + t.Minute())
}

func clock(m float64) string {
	v := int(m)
	return fmt.Sprintf("%02d:%02d", v/60, v%60)
}

// JournalDay returns a day's saved entries; answeredAt nil when the day was never saved.
func (s *Store) JournalDay(ctx context.Context, userID uuid.UUID, day string) (*time.Time, []model.JournalEntry, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT answered_at FROM journal_days WHERE user_id=$1 AND day=$2::date`, userID, day).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, []model.JournalEntry{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT e.behavior_id, e.value_bool, e.value_num, b.kind FROM journal_entries e
		JOIN behaviors b ON b.id = e.behavior_id WHERE e.user_id=$1 AND e.day=$2::date
		ORDER BY array_position($3::text[], b.category), b.sort, b.name_en`, userID, day, model.BehaviorCategories)
	if err != nil {
		return nil, nil, err
	}
	entries, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (model.JournalEntry, error) {
		var e model.JournalEntry
		var num *float64
		var kind string
		if err := r.Scan(&e.BehaviorID, &e.ValueBool, &num, &kind); err != nil {
			return e, err
		}
		if num != nil {
			switch kind {
			case model.BehaviorCount:
				n := int(*num)
				e.ValueNum = &n
			case model.BehaviorTime:
				c := clock(*num)
				e.ValueTime = &c
			}
		}
		return e, nil
	})
	return at, entries, err
}

// PutJournalDay replaces a day's entries (kinds by behavior id, checked by the
// caller) and marks it answered now.
func (s *Store) PutJournalDay(ctx context.Context, userID uuid.UUID, day string, entries []model.JournalEntry, kinds map[uuid.UUID]string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO journal_days (user_id, day, answered_at) VALUES ($1, $2::date, now())
			ON CONFLICT (user_id, day) DO UPDATE SET answered_at = now()`, userID, day); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM journal_entries WHERE user_id=$1 AND day=$2::date`, userID, day); err != nil {
			return err
		}
		batch := &pgx.Batch{}
		for _, e := range entries {
			var num *float64
			switch {
			case kinds[e.BehaviorID] == model.BehaviorCount && e.ValueNum != nil:
				v := float64(*e.ValueNum)
				num = &v
			case kinds[e.BehaviorID] == model.BehaviorTime && e.ValueTime != nil:
				v := minutes(*e.ValueTime)
				num = &v
			}
			batch.Queue(`INSERT INTO journal_entries (user_id, day, behavior_id, value_bool, value_num) VALUES ($1, $2::date, $3, $4, $5)`,
				userID, day, e.BehaviorID, e.ValueBool, num)
		}
		return tx.SendBatch(ctx, batch).Close()
	})
}

// JournalAnswers are the saved entries on days [from, to], by behavior, oldest first.
func (s *Store) JournalAnswers(ctx context.Context, userID uuid.UUID, from, to string) (map[uuid.UUID][]behavior.Answer, error) {
	rows, err := s.pool.Query(ctx, `SELECT behavior_id, to_char(day, 'YYYY-MM-DD'), value_bool FROM journal_entries
		WHERE user_id=$1 AND day >= $2::date AND day <= $3::date ORDER BY day`, userID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID][]behavior.Answer{}
	for rows.Next() {
		var id uuid.UUID
		var a behavior.Answer
		if err := rows.Scan(&id, &a.Day, &a.Yes); err != nil {
			return nil, err
		}
		out[id] = append(out[id], a)
	}
	return out, rows.Err()
}
