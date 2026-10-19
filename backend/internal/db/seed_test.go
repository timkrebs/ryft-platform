package db

import (
	"context"
	"testing"

	"github.com/timkrebs/ryft/internal/train"
)

func TestBuiltinsParseAndCheck(t *testing.T) {
	b, err := LoadBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	if len(b.exercises) < 150 {
		t.Errorf("want the ~150-exercise library, got %d", len(b.exercises))
	}
	want := map[string]bool{"full-body-3x": true, "upper-lower-4x": true, "weightlifting-4x": true, "five-three-one-4x": true}
	for _, p := range b.plans {
		delete(want, p.Slug)
	}
	if len(want) != 0 {
		t.Errorf("missing plan templates: %v", want)
	}
}

// Integration (RYFT_TEST_POSTGRES_URL): the seeds load, a second run changes
// nothing, and the fixed ids are the ones the API documents.
func TestSeedIdempotent(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBuiltins()
	if err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	before := scalar[string](t, pool, `SELECT max(updated_at)::text FROM exercises`)
	if err := Seed(ctx, pool, quiet); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if after := scalar[string](t, pool, `SELECT max(updated_at)::text FROM exercises`); after != before {
		t.Error("a second seed must not touch unchanged rows")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM exercises WHERE user_id IS NULL AND NOT archived`); n != len(b.exercises) {
		t.Errorf("exercises: %d, want %d", n, len(b.exercises))
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM plans WHERE user_id IS NULL`); n != 4 {
		t.Errorf("plans: %d", n)
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM exercises WHERE id = $1 AND slug = 'back-squat'`, train.BuiltinExerciseID("back-squat")); n != 1 {
		t.Error("back-squat must carry its fixed id")
	}
	if n := scalar[int](t, pool, `SELECT count(*) FROM plan_days WHERE plan_id = $1`, train.BuiltinPlanID("weightlifting-4x")); n != 16 {
		t.Errorf("weightlifting-4x days: %d, want 16", n)
	}
	// A built-in that leaves the seeds is archived, not deleted.
	if _, err := pool.Exec(ctx, `INSERT INTO exercises (id, slug, name_en, name_de, kind, primary_muscles, load_type)
		VALUES (gen_random_uuid(), 'retired-lift', 'Retired', 'Alt', 'barbell', '{quads}', 'weight')`); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT archived FROM exercises WHERE slug = 'retired-lift'`) {
		t.Error("a built-in no longer in the seeds must be archived")
	}
}
