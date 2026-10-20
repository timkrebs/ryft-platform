package store

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/timkrebs/ryft/internal/model"
)

// SourceSeen is one source and kind in a sync: its latest record's end and
// the bundle id it came with, if any.
type SourceSeen struct {
	Name, Kind string
	BundleID   *string
	LastSeen   time.Time
}

// maxSourceName bounds user_sources.name (migration 0020).
const maxSourceName = 200

// SourcesOf lists the sources and kinds of a sync, sorted by name and kind.
// Records without a source name, or with an absurdly long one, are left out.
func SourcesOf(samples []model.Sample, sleep []model.SleepSegment, workouts []model.Workout) []SourceSeen {
	type key struct{ name, kind string }
	seen := map[key]*SourceSeen{}
	add := func(name, kind string, bundle *string, end time.Time) {
		if name == "" || len(name) > maxSourceName || kind == "" {
			return
		}
		s := seen[key{name, kind}]
		if s == nil {
			s = &SourceSeen{Name: name, Kind: kind}
			seen[key{name, kind}] = s
		}
		if end.After(s.LastSeen) {
			s.LastSeen = end
		}
		if bundle != nil && *bundle != "" {
			s.BundleID = bundle
		}
	}
	for _, x := range samples {
		add(x.Source, model.SampleKinds[x.Type], x.SourceBundleID, x.End)
	}
	for _, x := range sleep {
		add(x.Source, model.KindSleep, x.SourceBundleID, x.End)
	}
	for _, x := range workouts {
		add(x.Source, model.KindWorkout, x.SourceBundleID, x.End)
	}
	out := make([]SourceSeen, 0, len(seen))
	for _, s := range seen {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b SourceSeen) int { return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Kind, b.Kind)) })
	return out
}

// UpsertSources records a sync's sources: last_seen only moves forward, a
// bundle id once sent is kept.
func (s *Store) UpsertSources(ctx context.Context, userID uuid.UUID, seen []SourceSeen) error {
	if len(seen) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, x := range seen {
		b.Queue(`INSERT INTO user_sources (user_id, name, kind, bundle_id, last_seen) VALUES ($1,$2,$3,$4,$5)
			ON CONFLICT (user_id, name, kind) DO UPDATE SET
			  last_seen = GREATEST(user_sources.last_seen, EXCLUDED.last_seen),
			  bundle_id = COALESCE(EXCLUDED.bundle_id, user_sources.bundle_id)`,
			userID, x.Name, x.Kind, x.BundleID, x.LastSeen)
	}
	return s.pool.SendBatch(ctx, b).Close()
}

// RecordSync stores what a device's sync carried on the device row (the admin
// view's "last sync").
func (s *Store) RecordSync(ctx context.Context, userID, deviceID uuid.UUID, summary map[string]any) error {
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE devices SET last_sync_summary = $3::jsonb WHERE user_id = $1 AND id = $2`,
		userID, deviceID, string(raw))
	return err
}

// UserSources lists the sources with a kind seen since the given time, one per
// name (its kinds sorted, the latest bundle id), newest first.
func (s *Store) UserSources(ctx context.Context, userID uuid.UUID, since time.Time) ([]model.UserSource, error) {
	rows, err := s.pool.Query(ctx, `SELECT name,
		  (array_agg(bundle_id ORDER BY last_seen DESC) FILTER (WHERE bundle_id IS NOT NULL))[1],
		  array_agg(kind ORDER BY kind), max(last_seen)
		FROM user_sources WHERE user_id = $1 AND last_seen >= $2
		GROUP BY name ORDER BY max(last_seen) DESC, name`, userID, since)
	if err != nil {
		return nil, err
	}
	out := []model.UserSource{}
	var u model.UserSource
	_, err = pgx.ForEachRow(rows, []any{&u.Name, &u.BundleID, &u.Kinds, &u.LastSeen}, func() error {
		u.LastSeen = u.LastSeen.UTC()
		out = append(out, u)
		u = model.UserSource{}
		return nil
	})
	return out, err
}
