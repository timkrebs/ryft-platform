package db

import (
	"context"
	"testing"
)

// 0036: analytics.enabled ships dark (enabled, 0 %, no allow-list), and a rerun of the file keeps
// what the operator set since (D231).
func TestMigrateAnalyticsFlag(t *testing.T) {
	pool := freshDB(t, false)
	ctx := context.Background()
	if err := Migrate(ctx, pool, quiet); err != nil {
		t.Fatal(err)
	}
	if !scalar[bool](t, pool, `SELECT enabled AND rollout_pct = 0 AND cardinality(allow_user_ids) = 0
		FROM feature_flags WHERE key = 'analytics.enabled'`) {
		t.Fatal("analytics.enabled ships enabled at 0 % with an empty allow-list")
	}
	if _, err := pool.Exec(ctx, `UPDATE feature_flags SET enabled = false, rollout_pct = 100 WHERE key = 'analytics.enabled'`); err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/0036_analytics_flag.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("0036 a second time: %v", err)
	}
	if !scalar[bool](t, pool, `SELECT NOT enabled AND rollout_pct = 100 FROM feature_flags WHERE key = 'analytics.enabled'`) {
		t.Error("a rerun of 0036 overwrote the operator's setting")
	}
}
