package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/model"
)

// Integration tests, opt-in like internal/db's: RYFT_TEST_POSTGRES_URL is a
// superuser connection to a throwaway server.
func testStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	admin := os.Getenv("RYFT_TEST_POSTGRES_URL")
	if admin == "" {
		t.Skip("set RYFT_TEST_POSTGRES_URL to a throwaway Postgres server to run store tests")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	// The roles first: tests of other packages migrate on this server at the same time.
	if err := db.EnsureServiceRoles(ctx, root); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("ryft_storetest_%d", rand.Uint32())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := db.Connect(ctx, u.String(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	})
	if err := db.Migrate(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	return New(pool), pool
}

func sample(at time.Time) model.Sample {
	return model.Sample{Type: "heart_rate", Start: at, End: at, Value: 60, Unit: "bpm", Source: "test"}
}

// owned counts a user's rows in every table that has them.
func owned(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) int {
	t.Helper()
	var n int
	err := pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM users WHERE id=$1) + (SELECT count(*) FROM devices WHERE user_id=$1) +
		(SELECT count(*) FROM samples WHERE user_id=$1) + (SELECT count(*) FROM sleep_segments WHERE user_id=$1) +
		(SELECT count(*) FROM workouts WHERE user_id=$1) + (SELECT count(*) FROM daily_scores WHERE user_id=$1) +
		(SELECT count(*) FROM push_tokens WHERE user_id=$1) + (SELECT count(*) FROM notifications_sent WHERE user_id=$1) +
		(SELECT count(*) FROM user_sources WHERE user_id=$1) + (SELECT count(*) FROM feedback WHERE user_id=$1) +
		(SELECT count(*) FROM score_queue WHERE user_id=$1) + (SELECT count(*) FROM outbox WHERE user_id=$1)`, id).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func seedUser(t *testing.T, st *Store, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := st.EnsureUser(ctx, Identity{ID: id, Email: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	// An event for the account waiting in (or published from) the outbox (D101).
	if _, err := pool.Exec(ctx, `INSERT INTO outbox (publisher, user_id, routing_key, payload) VALUES ('ingest', $1, 'ingested', '{}')`, id); err != nil {
		t.Fatal(err)
	}
	device := uuid.New()
	if err := st.TouchDevice(ctx, id, device, "phone", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, id, []model.Sample{sample(now), sample(now.Add(-time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSleep(ctx, id, []model.SleepSegment{{Start: now.Add(-8 * time.Hour), End: now, Stage: "core", Source: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertWorkouts(ctx, id, []model.Workout{{ID: uuid.New(), Activity: "running", Start: now.Add(-time.Hour), End: now, Source: "test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day) VALUES ($1, current_date)`, id); err != nil {
		t.Fatal(err)
	}
	if err := st.RegisterPushToken(ctx, id, device, model.PushRegistration{Token: hexToken(id), Env: "sandbox", Locale: "en"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertSources(ctx, id, []SourceSeen{{Name: "test", Kind: model.KindHR, LastSeen: now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(ctx, id, model.FeedbackPost{Message: "bye"}, model.FeedbackOther); err != nil {
		t.Fatal(err)
	}
	if err := st.QueueDays(ctx, id, []string{now.Format("2006-01-02")}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNotification(ctx, id, now.Format("2006-01-02"), model.NotifyMorningScore); err != nil {
		t.Fatal(err)
	}
}

// hexToken is a well-formed, per-user APNs token.
func hexToken(id uuid.UUID) string {
	return strings.ReplaceAll(id.String(), "-", "") + strings.ReplaceAll(id.String(), "-", "")
}

func TestDeleteUserErasesEverythingAndStaysDeleted(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()
	seedUser(t, st, pool, a)
	seedUser(t, st, pool, b)
	before := owned(t, pool, b)

	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := owned(t, pool, a); n != 0 {
		t.Fatalf("%d rows of the deleted user left", n)
	}
	if n := owned(t, pool, b); n != before {
		t.Fatalf("the other user lost rows: %d → %d", before, n)
	}
	if _, err := st.EnsureUser(ctx, Identity{ID: a}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("a deleted account's token must not recreate it: %v", err)
	}
	if n := owned(t, pool, a); n != 0 {
		t.Fatal("EnsureUser recreated the deleted user")
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatalf("deleting again must be a no-op: %v", err)
	}

	pending, err := st.PendingAuthDeletions(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0] != a {
		t.Fatalf("pending %v %v", pending, err)
	}
	if err := st.MarkAuthDeleted(ctx, a); err != nil {
		t.Fatal(err)
	}
	if pending, _ = st.PendingAuthDeletions(ctx, 10); len(pending) != 0 {
		t.Fatalf("still pending after MarkAuthDeleted: %v", pending)
	}

	// A restore from an older backup brings the user row back.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id) VALUES ($1)`, a); err != nil {
		t.Fatal(err)
	}
	if back, err := st.ResurrectedUsers(ctx); err != nil || len(back) != 1 || back[0] != a {
		t.Fatalf("resurrected %v %v", back, err)
	}
}

func TestDeleteUserWaitsForASyncInFlight(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := uuid.New()
	seedUser(t, st, pool, a)

	// A sync's transaction has inserted a sample but not committed yet.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		VALUES ($1, 'steps', now(), now(), 10, 'count', 'test')`, a); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- st.DeleteUser(ctx, a) }()
	select {
	case err := <-done:
		t.Fatalf("DeleteUser must wait for the sync in flight, returned %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := owned(t, pool, a); n != 0 {
		t.Fatalf("the in-flight sync's rows survived: %d", n)
	}
	// A sync that arrives after the deletion cannot write.
	if _, err := st.UpsertSamples(ctx, a, []model.Sample{sample(time.Now())}); err == nil {
		t.Fatal("a sync after the deletion must fail")
	}
}

func TestEnsureUserWaitsForADeletionInProgress(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := uuid.New()
	seedUser(t, st, pool, a)
	// Stale, so the next EnsureUser takes the write path.
	if _, err := pool.Exec(ctx, `UPDATE users SET last_seen_at = now() - interval '1 hour' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}

	// DeleteUser's transaction, stopped after the tombstone.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, lockUser, a); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deleted_users (id) VALUES ($1)`, a); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { _, err := st.EnsureUser(ctx, Identity{ID: a}); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("EnsureUser must wait for the deletion, returned %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrDeleted) {
		t.Fatalf("after the deletion commits EnsureUser must see the tombstone: %v", err)
	}
}

func TestAppleTokenMovesToTheTombstoneUntilRevoked(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	a := uuid.New()
	seedUser(t, st, pool, a)
	if err := st.SaveAppleToken(ctx, a, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAppleToken(ctx, a, []byte("sealed-token")); err != nil {
		t.Fatalf("a newer code replaces the token: %v", err)
	}
	if _, err := st.DeletedAppleToken(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a live account has no tombstone token: %v", err)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM apple_tokens WHERE user_id=$1`, a).Scan(&left); err != nil || left != 0 {
		t.Fatalf("apple_tokens must be empty after deletion: %d %v", left, err)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	enc, err := st.DeletedAppleToken(ctx, a)
	if err != nil || string(enc) != "sealed-token" {
		t.Fatalf("the tombstone keeps the token, also across a repeated delete: %q %v", enc, err)
	}
	if pending, _ := st.PendingAppleRevocations(ctx, 10); len(pending) != 1 || pending[0] != a {
		t.Fatalf("pending %v", pending)
	}
	if err := st.MarkAppleRevoked(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeletedAppleToken(ctx, a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked token is gone: %v", err)
	}
	if pending, _ := st.PendingAppleRevocations(ctx, 10); len(pending) != 0 {
		t.Fatalf("still pending: %v", pending)
	}
}
