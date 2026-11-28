package db

import (
	"context"
	"testing"
)

// 0072 (D314): app.structure_v2 ships dark (enabled, 0 %, an empty allow-list), and a rerun keeps what the
// operator set since.
func TestMigrateStructureV2Flag(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 AND cardinality(allow_user_ids) = 0
		FROM feature_flags WHERE key = 'app.structure_v2'`) {
		t.Fatal("app.structure_v2 ships enabled at 0 % with an empty allow-list")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = false, rollout_pct = 100 WHERE key = 'app.structure_v2'`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0072_app_structure_v2_flag.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0072 a second time: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT NOT enabled AND rollout_pct = 100 FROM feature_flags WHERE key = 'app.structure_v2'`) {
		t.Error("a rerun of 0072 overwrote the operator's setting")
	}
}
