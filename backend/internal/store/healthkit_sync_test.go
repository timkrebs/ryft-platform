package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

func TestHealthKitWithdrawalHonoredByBothSyncStages(t *testing.T) {
	ctx := context.Background()
	owner, root := testStore(t)
	cfg := root.Config()
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE ryft_ingest`)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ingest := New(pool)
	user, device, sample := uuid.New(), uuid.New(), uuid.New()
	if _, err := owner.EnsureUser(ctx, Identity{ID: user}); err != nil {
		t.Fatal(err)
	}
	grant := func() *model.Consent {
		t.Helper()
		if err := owner.GrantConsent(ctx, user, "2026-01-01", nil); err != nil {
			t.Fatal(err)
		}
		profile, _, err := ingest.GetUser(ctx, user)
		if err != nil || profile.Consent == nil {
			t.Fatalf("read fixture grant: %v", err)
		}
		return profile.Consent
	}
	withdraw := func() {
		t.Helper()
		if err := owner.RevokeConsent(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, want int) {
		t.Helper()
		var got int
		if err := root.QueryRow(ctx, sql, user).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d (%v)", sql, got, want, err)
		}
	}
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	req := model.SyncRequest{DeviceID: device, Samples: []model.Sample{
		{ID: &sample, Type: "steps", Start: start, End: start.Add(time.Minute), Value: 10, Unit: "count", Source: "fixture"},
	}}
	old := grant()
	withdraw()
	if _, _, err := ingest.WriteHealthKitRecords(ctx, user, old, req); !errors.Is(err, ErrHealthKitConsentChanged) {
		t.Fatalf("stale grant wrote health records: %v", err)
	}
	count(`SELECT count(*) FROM devices WHERE user_id=$1`, 0)
	count(`SELECT count(*) FROM samples WHERE user_id=$1`, 0)
	count(`SELECT count(*) FROM user_sources WHERE user_id=$1`, 0)
	active := grant()
	if _, _, err := ingest.WriteHealthKitRecords(ctx, user, old, req); !errors.Is(err, ErrHealthKitConsentChanged) {
		t.Fatalf("new consent revived a withdrawn grant: %v", err)
	}
	if got, sourceErr, err := ingest.WriteHealthKitRecords(ctx, user, active, req); err != nil || sourceErr != nil || got.Samples != 1 {
		t.Fatalf("current grant: %+v, %v, %v", got, sourceErr, err)
	}
	withdraw()
	cursor := &model.HealthKitCursor{Stream: "steps", Sequence: 1, Value: "opaque-fixture"}
	apply := Apply{Days: []string{"2026-01-01"}, Loc: time.UTC, Deleted: []model.DeletedRecord{{Kind: model.DeletedSample, ID: sample}}}
	if _, err := ingest.ApplyHealthKit(ctx, user, active, device, cursor, apply); !errors.Is(err, ErrHealthKitConsentChanged) {
		t.Fatalf("withdrawal after upload allowed further processing: %v", err)
	}
	if err := ingest.RecordHealthKitSync(ctx, user, active, device, map[string]any{"samples": 1}); !errors.Is(err, ErrHealthKitConsentChanged) {
		t.Fatalf("withdrawal after upload allowed summary write: %v", err)
	}
	count(`SELECT count(*) FROM samples WHERE user_id=$1`, 1)
	count(`SELECT count(*) FROM score_queue WHERE user_id=$1`, 0)
	count(`SELECT count(*) FROM nutrition.wearable_sync_cursors WHERE user_id=$1`, 0)
	latest := grant()
	if out, err := ingest.ApplyHealthKit(ctx, user, latest, device, cursor, apply); err != nil || out.Deleted.Samples != 1 {
		t.Fatalf("fresh upload apply: %+v, %v", out, err)
	}
	count(`SELECT count(*) FROM samples WHERE user_id=$1`, 0)
	count(`SELECT count(*) FROM score_queue WHERE user_id=$1`, 1)
	count(`SELECT count(*) FROM nutrition.wearable_sync_cursors WHERE user_id=$1`, 1)
}
