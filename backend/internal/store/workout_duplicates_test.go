package store

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/testsupport"
)

// Database-level tests of workouts.duplicate_of (migration 0031; design D-d): which role
// may write it, that it has no foreign key, and that the account merge (which runs as
// ryft_api) moves rows that carry it. Each statement runs through a pool opened AS the
// service role: what works as superuser has failed as a role before.

func dupRolePool(t *testing.T, admin *pgxpool.Pool, role string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pw := testsupport.RolePassword(role) // one password per role in every package: tests run at once on one server
	if err := testsupport.WithRoleLock(ctx, func() error { return db.SetRolePassword(ctx, admin, role, pw) }); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pw)
	p, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func dupUser(t *testing.T, st *Store) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := st.EnsureUser(context.Background(), Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	return id
}

func dupWorkout(t *testing.T, pool *pgxpool.Pool, user uuid.UUID, source string, start time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES ($1, $2, 'running', $3, $4, $5)`,
		id, user, start, start.Add(time.Hour), source); err != nil {
		t.Fatal(err)
	}
	return id
}

func sqlState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

// ryft_scoring may set and clear duplicate_of; ryft_api may read it and may not write it (42501). A row cannot
// point at itself (check), and the column has no foreign key: it may name a row that is
// gone, which is how a deleted primary shows its duplicate at once.
func TestDuplicateOfIsWrittenByScoringOnly(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	at := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	primary, dup := dupWorkout(t, admin, user, "Apple Watch", at), dupWorkout(t, admin, user, "Garmin Connect", at)

	scoring := dupRolePool(t, admin, "ryft_scoring")
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, user, dup, primary); err != nil {
		t.Fatalf("ryft_scoring sets duplicate_of: %v", err)
	}
	var got *uuid.UUID
	if err := admin.QueryRow(ctx, `SELECT duplicate_of FROM workouts WHERE id = $1`, dup).Scan(&got); err != nil || got == nil || *got != primary {
		t.Fatalf("duplicate_of = %v (%v), want %s", got, err, primary)
	}
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, user, dup, uuid.New()); err != nil {
		t.Errorf("no foreign key: a primary that is gone is a plain value: %v", err)
	}
	if _, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1 AND id = $2`, user, dup); err != nil {
		t.Errorf("ryft_scoring clears duplicate_of: %v", err)
	}
	_, err := scoring.Exec(ctx, `UPDATE workouts SET duplicate_of = id WHERE user_id = $1 AND id = $2`, user, dup)
	if sqlState(err) != "23514" {
		t.Errorf("a workout pointing at itself: %v, want a check violation (23514)", err)
	}

	// ryft_ingest holds table-level UPDATE on workouts since 0009 and cannot be narrowed
	// additively, so it can write the column; the grep test below pins that it never does.
	for _, role := range []string{"ryft_api"} {
		p := dupRolePool(t, admin, role)
		var n int
		if err := p.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NULL`, user).Scan(&n); err != nil || n != 2 {
			t.Errorf("%s reads duplicate_of: %d rows, %v", role, n, err)
		}
		if _, err := p.Exec(ctx, `UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, user, dup, primary); sqlState(err) != "42501" {
			t.Errorf("%s writes duplicate_of: %v, want permission denied (42501)", role, err)
		}
	}
	// The column is nullable and new rows are not duplicates.
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM workouts WHERE user_id = $1 AND duplicate_of IS NOT NULL`, user).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows with duplicate_of set: %d, %v, want 0", n, err)
	}
}

// The merge of two accounts runs as ryft_api and moves workouts with UPDATE user_id: a
// primary and its duplicate move together, duplicate_of stays as it was, the plan (dry
// run) works too. This is why the column has no (user_id, duplicate_of) foreign key.
func TestMergeAsAPIRoleKeepsDuplicateOf(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	from, to := dupUser(t, st), dupUser(t, st)
	at := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	primary, dup := dupWorkout(t, admin, from, "Apple Watch", at), dupWorkout(t, admin, from, "Garmin Connect", at)
	keep := dupWorkout(t, admin, to, "Apple Watch", at.Add(-24*time.Hour))
	if _, err := admin.Exec(ctx, `UPDATE workouts SET duplicate_of = $2 WHERE id = $1`, dup, primary); err != nil {
		t.Fatal(err)
	}

	api := New(dupRolePool(t, admin, "ryft_api"))
	for _, dry := range []bool{true, false} {
		if _, err := api.MergeUsers(ctx, from, to, dry); err != nil {
			t.Fatalf("merge as ryft_api (dry run %v): %v", dry, err)
		}
	}
	for _, id := range []uuid.UUID{primary, dup, keep} {
		var owner uuid.UUID
		if err := admin.QueryRow(ctx, `SELECT user_id FROM workouts WHERE id = $1`, id).Scan(&owner); err != nil || owner != to {
			t.Errorf("workout %s belongs to %s (%v), want the target account", id, owner, err)
		}
	}
	var got *uuid.UUID
	if err := admin.QueryRow(ctx, `SELECT duplicate_of FROM workouts WHERE id = $1`, dup).Scan(&got); err != nil || got == nil || *got != primary {
		t.Errorf("duplicate_of after the merge = %v (%v), want %s", got, err, primary)
	}
}

// ryft_ingest can technically update the column (table-level grant), so the code must never:
// no non-test Go file of cmd/ingest, nor store/fold.go and store/apply.go, writes duplicate_of.
func TestIngestCodeNeverWritesDuplicateOf(t *testing.T) {
	files, _ := filepath.Glob("../../cmd/ingest/*.go")
	files = append(files, "fold.go", "apply.go")
	write := regexp.MustCompile(`(?is)(UPDATE\s+workouts\s+SET[^;` + "`" + `]*duplicate_of|INSERT\s+INTO\s+workouts[^;` + "`" + `]*duplicate_of)`)
	checked := 0
	for _, f := range files {
		if filepath.Ext(f) != ".go" || regexp.MustCompile(`_test\.go$`).MatchString(f) {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		checked++
		if write.Match(raw) {
			t.Errorf("%s writes duplicate_of; only scoring may", f)
		}
	}
	if checked < 3 {
		t.Errorf("only %d source files checked", checked)
	}
}

// The visible-only read, as ryft_api and as ryft_scoring (session candidates): a duplicate is
// hidden only while its primary exists AND is itself visible. Marks that point at each
// other (stale passes of two days) hide nothing; a chain A->B->C lists A and C; a mark on a
// primary that is gone lists the row; a ryft-session workout is always listed.
func TestVisibleWorkoutsHideADuplicateOnlyOfAVisiblePrimary(t *testing.T) {
	st, admin := testStore(t)
	ctx := context.Background()
	user := dupUser(t, st)
	at := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	mark := func(dup, primary uuid.UUID) {
		t.Helper()
		if _, err := admin.Exec(ctx, `UPDATE workouts SET duplicate_of = $2 WHERE id = $1`, dup, primary); err != nil {
			t.Fatal(err)
		}
	}
	clear := func() {
		if _, err := admin.Exec(ctx, `UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1`, user); err != nil {
			t.Fatal(err)
		}
	}
	a, b, c := dupWorkout(t, admin, user, "Apple Watch", at), dupWorkout(t, admin, user, "Garmin Connect", at), dupWorkout(t, admin, user, "Third", at)
	own := uuid.New()
	if _, err := admin.Exec(ctx, `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source) VALUES ($1,$2,'strength',$3,$4,'ryft-session')`,
		own, user, at, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	listed := func(s *Store) []uuid.UUID {
		t.Helper()
		ws, err := s.VisibleWorkoutsBetween(ctx, user, at.Add(-time.Hour), at.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		var out []uuid.UUID
		for _, w := range ws {
			out = append(out, w.ID)
		}
		slices.SortFunc(out, func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
		return out
	}
	want := func(ids ...uuid.UUID) []uuid.UUID {
		slices.SortFunc(ids, func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
		return ids
	}
	for _, role := range []string{"ryft_api", "ryft_scoring"} {
		s := New(dupRolePool(t, admin, role))
		clear()
		mark(b, a)
		if got := listed(s); !slices.Equal(got, want(a, c, own)) {
			t.Errorf("%s: A<-B: %v", role, got)
		}
		clear()
		mark(a, b)
		mark(b, a)
		if got := listed(s); !slices.Equal(got, want(a, b, c, own)) {
			t.Errorf("%s: A<->B cycle hides the run: %v", role, got)
		}
		clear()
		mark(a, b)
		mark(b, c)
		if got := listed(s); !slices.Equal(got, want(a, c, own)) {
			t.Errorf("%s: chain A->B->C: %v, want A and C", role, got)
		}
		clear()
		mark(a, uuid.New())
		mark(own, a)
		if got := listed(s); !slices.Equal(got, want(a, b, c, own)) {
			t.Errorf("%s: a gone primary and a ryft-session mark: %v", role, got)
		}
	}
}
