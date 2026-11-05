package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
)

// The statement trace of ApplySync (D145): the SQL a call sends, in order, per scenario,
// recorded through a pgx query tracer and compared with testdata/applysync_trace.golden.
// "Same SQL, same order, same transaction boundaries and the lock first" is the promise of
// restructuring ApplySync into steps; this is where it is checked, not read off a diff.
// The text is the statement with its whitespace collapsed and the number of arguments;
// values are not recorded (ids are random). BEGIN and COMMIT/ROLLBACK are part of it.
//
// A change of the SQL on purpose regenerates the file: RYFT_UPDATE_TRACE=1 go test
// ./internal/store -run TestApplySyncStatementTrace, and the diff is the review.

type sqlTrace struct {
	mu    sync.Mutex
	stmts []string
}

func (s *sqlTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	q := strings.Join(strings.Fields(d.SQL), " ")
	if q == ";" { // the pool's liveness ping
		return ctx
	}
	s.mu.Lock()
	s.stmts = append(s.stmts, fmt.Sprintf("%s [args=%d]", q, len(d.Args)))
	s.mu.Unlock()
	return ctx
}

func (*sqlTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type traceScenario struct {
	name  string
	setup func(t *testing.T, pool *pgxpool.Pool, user uuid.UUID) Apply // seeds, and returns the call
	emit  bool                                                         // pass an Emit that inserts the outbox row
}

func failTrigger(t *testing.T, pool *pgxpool.Pool, table, when string) {
	t.Helper()
	ctx := context.Background()
	fn := "fail_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	for _, q := range []string{
		`CREATE FUNCTION ` + fn + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'step failed' USING ERRCODE = 'XX001'; END $$`,
		`CREATE TRIGGER ` + fn + ` BEFORE ` + when + ` ON ` + table + ` FOR EACH STATEMENT EXECUTE FUNCTION ` + fn + `()`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP TRIGGER `+fn+` ON `+table)
		_, _ = pool.Exec(ctx, `DROP FUNCTION `+fn+`()`)
	})
}

func traceScenarios() []traceScenario {
	at := utc("2026-01-12T10:00:00Z")
	allKinds := func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) []model.DeletedRecord {
		return []model.DeletedRecord{
			del(model.DeletedSample, seedSample(t, pool, u, model.SampleHRVSDNN, at)),
			del(model.DeletedSleep, seedSleep(t, pool, u, at)),
			del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: at, session: true})),
		}
	}
	copies := func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) {
		seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at.Add(-time.Hour)})
		seedWorkout(t, pool, u, seedW{source: "Garmin", start: at, created: at})
	}
	return []traceScenario{
		{"days only, no emit", func(*testing.T, *pgxpool.Pool, uuid.UUID) Apply {
			return Apply{Days: []string{"2026-01-12"}}
		}, false},
		{"days only, emit", func(*testing.T, *pgxpool.Pool, uuid.UUID) Apply {
			return Apply{Days: []string{"2026-01-12", "2026-01-11"}}
		}, true},
		{"nothing at all", func(*testing.T, *pgxpool.Pool, uuid.UUID) Apply { return Apply{} }, true},
		{"unknown kind and nil ids only", func(*testing.T, *pgxpool.Pool, uuid.UUID) Apply {
			return Apply{Deleted: []model.DeletedRecord{del("ring", uuid.New()), del(model.DeletedSample, uuid.Nil)}}
		}, true},
		{"unknown ids of every kind", func(*testing.T, *pgxpool.Pool, uuid.UUID) Apply {
			return Apply{Deleted: []model.DeletedRecord{del(model.DeletedSample, uuid.New()), del(model.DeletedSleep, uuid.New()), del(model.DeletedWorkout, uuid.New())}}
		}, true},
		{"sample deletion only", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			return Apply{Deleted: []model.DeletedRecord{del(model.DeletedSample, seedSample(t, pool, u, model.SampleSteps, at))}}
		}, true},
		{"sleep deletion only", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			return Apply{Deleted: []model.DeletedRecord{del(model.DeletedSleep, seedSleep(t, pool, u, at))}}
		}, true},
		{"workout deletion only", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			return Apply{Deleted: []model.DeletedRecord{del(model.DeletedWorkout, seedWorkout(t, pool, u, seedW{source: "W", start: at}))}}
		}, true},
		{"all three deletions, emit", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			return Apply{Days: []string{"2026-01-10"}, Deleted: allKinds(t, pool, u)}
		}, true},
		{"fold that folds", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			copies(t, pool, u)
			return Apply{Fold: foldScope("Garmin", at)}
		}, true},
		{"fold with nothing to fold", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			seedWorkout(t, pool, u, seedW{source: "Garmin", start: at})
			return Apply{Days: []string{"2026-01-12"}, Fold: foldScope("Garmin", at)}
		}, true},
		{"deletions and fold together", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			copies(t, pool, u)
			return Apply{Days: []string{"2026-01-09"}, Deleted: allKinds(t, pool, u), Fold: foldScope("Garmin", at)}
		}, true},
		{"fold skipped above the row bound", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			if _, err := pool.Exec(context.Background(), `INSERT INTO workouts (id, user_id, activity, start_at, end_at, source)
				SELECT gen_random_uuid(), $1, 'running', $2::timestamptz + n * interval '1 day', $2::timestamptz + n * interval '1 day' + interval '1 h', 'Garmin'
				FROM generate_series(1, $3) AS n`, u, at, FoldMaxRows+1); err != nil {
				t.Fatal(err)
			}
			return Apply{Days: []string{"2026-01-12"}, Fold: &FoldScope{Sources: []string{"Garmin"}, From: at, To: at.AddDate(30, 0, 0)}}
		}, true},
		{"sleep deletion fails (rollback)", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			failTrigger(t, pool, "sleep_segments", "DELETE")
			return Apply{Deleted: allKinds(t, pool, u)}
		}, true},
		{"fold delete fails (rollback)", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			copies(t, pool, u)
			failTrigger(t, pool, "workouts", "DELETE")
			return Apply{Fold: foldScope("Garmin", at)}
		}, true},
		{"queue fails (rollback)", func(t *testing.T, pool *pgxpool.Pool, u uuid.UUID) Apply {
			failTrigger(t, pool, "score_queue", "INSERT")
			return Apply{Days: []string{"2026-01-12"}}
		}, true},
	}
}

func TestApplySyncStatementTrace(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	cfg := pool.Config().Copy()
	tr := &sqlTrace{}
	cfg.ConnConfig.Tracer = tr
	traced, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(traced.Close)
	tst := New(traced)

	var b strings.Builder
	b.WriteString("# ApplySync: the statements one call sends, in order (apply_trace_test.go).\n")
	for _, sc := range traceScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			u := applyUser(t, st)
			a := sc.setup(t, pool, u)
			a.Loc = berlin
			if sc.emit {
				a.Emit = (&emitter{user: u}).emit
			}
			tr.mu.Lock()
			tr.stmts = nil
			tr.mu.Unlock()
			_, _ = tst.ApplySync(ctx, u, a) // the failing scenarios fail on purpose
			tr.mu.Lock()
			// Lock first, asserted here and not only by the golden (which RYFT_UPDATE_TRACE
			// regenerates): the transaction begins, then the user's apply lock, then anything.
			if len(tr.stmts) < 2 || !strings.HasPrefix(tr.stmts[0], "begin ") ||
				!strings.HasPrefix(tr.stmts[1], "SELECT pg_advisory_xact_lock(") {
				t.Errorf("the statements must be begin, then the apply lock; got %q", tr.stmts)
			}
			fmt.Fprintf(&b, "\n## %s\n", sc.name)
			for i, s := range tr.stmts {
				fmt.Fprintf(&b, "%d. %s\n", i+1, s)
			}
			tr.mu.Unlock()
		})
	}
	const golden = "testdata/applysync_trace.golden"
	if os.Getenv("RYFT_UPDATE_TRACE") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != string(want) {
		t.Fatalf("the statements ApplySync sends changed (RYFT_UPDATE_TRACE=1 regenerates %s if on purpose).\n--- got ---\n%s", golden, got)
	}
}
