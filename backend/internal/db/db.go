package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/retry"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Connect opens a pool on PoolConfig(url, maxConns) and pings it.
func Connect(ctx context.Context, url string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := PoolConfig(url, maxConns)
	if err != nil {
		return nil, err
	}
	return connect(ctx, cfg)
}

// PoolConfig is the pool every service, the migrate job and the tests open. pgx
// reads PGPASSWORD when url has none (Cloud Run's DATABASE_URL never has one, D221).
// On Cloud Run (K_SERVICE or CLOUD_RUN_JOB set) url must also keep the rules of
// cloudrun.go (urlFindings, serviceFindings), and an error never repeats url.
//
// maxConns caps the pool (0 keeps pgx's default). Connections are recycled every
// 30 minutes so pooler and server restarts are absorbed.
//
// The pool keeps no session state on a server connection (design §4: the services
// reach Supavisor's transaction pooler, which hands each transaction whichever server
// connection is free): no startup parameter beyond what url names (serviceFindings
// refuses any but application_name on a service), no hook that sends a statement.
// AfterConnect only maps types on the client (registerTypes). TestPoolConfigHooks
// pins this.
func PoolConfig(url string, maxConns int) (*pgxpool.Config, error) {
	env, service := cloudRun()
	var findings []string
	if env != "" {
		findings = urlFindings(url)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		if env == "" {
			return nil, fmt.Errorf("pgxpool: %w", err) // pgx's text: the URL with the password masked
		}
		return nil, refused(env, append(findings, "pgx cannot read it: "+parseReason(err)))
	}
	if service {
		findings = append(findings, serviceFindings(cfg.ConnConfig)...)
	}
	if len(findings) > 0 {
		return nil, refused(env, findings)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.AfterConnect = func(_ context.Context, c *pgx.Conn) error {
		registerTypes(c.TypeMap())
		return nil
	}
	return cfg, nil
}

// registerTypes is all that AfterConnect does: mappings in the connection's type map,
// no statement and no round trip.
//   - timestamptz values scan as UTC, so every timestamp the API emits is UTC whatever
//     the container's TZ.
//   - []uuid.UUID is uuid[] when pgx has no parameter type to go by: with
//     default_query_exec_mode=exec (the services on Cloud Run) parameters go out as text
//     typed by their Go type alone, and pgx knows [16]byte but not slices of a named
//     type, so the store's id slices (ANY($n), unnest($n::uuid[])) would not encode.
//     In the default mode the server's parameter type decides, so this changes nothing
//     there (TestExecModeEncoding).
func registerTypes(m *pgtype.Map) {
	m.RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID,
		Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
	m.RegisterDefaultPgType([]uuid.UUID(nil), "_uuid")
}

// connect opens a pool on a copy of cfg and pings it.
func connect(ctx context.Context, cfg *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// MigrateWait is how long a service waits at start-up for the database and
// the migrate job before it gives up and exits (Kubernetes restarts it).
const MigrateWait = 10 * time.Minute

// Open is how a service connects: it retries with backoff while the database
// or the service's role is not ready yet (the migrate job sets the role
// passwords), then waits until the schema is current, all within MigrateWait.
//
// A DATABASE_URL that PoolConfig refuses is a setting, not a state: it fails at
// once. On Cloud Run a refused login fails at once as well (stopRetrying): each
// failed login feeds Supavisor's circuit breaker, which then blocks the whole role
// (design §4), and Cloud Run's next cold start is the retry.
func Open(ctx context.Context, url string, maxConns int, log *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := PoolConfig(url, maxConns)
	if err != nil {
		return nil, err
	}
	env, _ := cloudRun()
	wait, cancel := context.WithTimeout(ctx, MigrateWait)
	defer cancel()
	var pool *pgxpool.Pool
	var final error // a login refused on Cloud Run: ends the wait
	logins := 0
	err = retry.Until(wait, log, "database", retry.Startup, func(context.Context) error {
		p, err := connect(ctx, cfg) // the pool outlives the wait
		pool = p
		if err != nil && env != "" && stopRetrying(err, &logins) {
			final = err
			cancel()
		}
		return err
	})
	if final != nil {
		return nil, fmt.Errorf("database: %w (not retried on Cloud Run, %s is set: every failed login feeds "+
			"Supavisor's circuit breaker for the whole role)", final, env)
	}
	if err != nil {
		return nil, err
	}
	if err := WaitMigrated(wait, pool, log); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// migrationLock serialises migrators across pods. It is a transaction-level
// advisory lock: it disappears with the transaction even if the pod dies or a
// pooler hands the session to someone else.
//
// Other advisory-lock classes (7419002 and up) are listed in internal/store/locks.go.
const migrationLock = 7_419_001

// lockTimeout bounds how long any statement of a migration waits for a lock
// (Postgres lock_timeout, error 55P03). Without it a migration that needs a strong
// lock on a busy table waits indefinitely and every later query queues behind its
// request. A var so tests can shorten it; a migration may still set its own with
// SET LOCAL, which runs after this and wins (0027 does, 5 s). Tests that change it
// must not run in parallel; locked reads it once per call.
var lockTimeout = 5 * time.Second

// Migrate applies every migrations/NNNN_*.sql file not yet recorded in
// schema_migrations, in numeric order, each in its own transaction under the
// migration lock (re-checking schema_migrations inside it). cmd/migrate runs
// it as the schema owner; concurrent runs are safe.
func Migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	return migrateTo(ctx, pool, log, 0)
}

// migrateTo applies migrations up to and including version upTo (0 = all).
func migrateTo(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, upTo int) error {
	if err := locked(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version int PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`)
		return err
	}); err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %q: bad version prefix", e.Name())
		}
		if upTo > 0 && version > upTo {
			break
		}
		sqlBytes, err := migrations.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		applied := false
		err = locked(ctx, pool, func(tx pgx.Tx) error {
			var done bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&done); err != nil {
				return err
			}
			if done {
				return nil
			}
			if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) && pgErr.Code == "55P03" { // lock_not_available
					where := ""
					if pgErr.Where != "" { // e.g. `while updating tuple (0,1) in relation "t"`: ids and names, no row values
						where = " [" + pgErr.Where + "]"
					}
					return fmt.Errorf("apply %s: hit the lock timeout%s (this migration rolled back, earlier ones of this run stay applied; "+
						"find the blocker in pg_stat_activity and pg_locks, then rerun): %w", e.Name(), where, err)
				}
				return fmt.Errorf("apply %s: %w", e.Name(), err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1)`, version); err != nil {
				return err
			}
			applied = true
			return nil
		})
		if err != nil {
			return err
		}
		if applied {
			log.InfoContext(ctx, "migration applied", "file", e.Name())
		}
	}
	return nil
}

// locked runs fn in a transaction holding the migration lock. lock_timeout is set
// only after the advisory lock is held, so a second migrator waits for the first
// (bounded by ctx) instead of being cut off; it is transaction-local, so it never
// reaches the pool.
func locked(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	timeout := lockTimeout
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	// Transaction-level, so a pod dying mid-migration cannot leave a lock on a pooled connection (D34).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLock); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", timeout.Milliseconds())); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PingTimeout bounds one Ping.
const PingTimeout = 5 * time.Second

// Pinger is what Ping pings: a *pgxpool.Pool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Ping pings the database once, within PingTimeout, and reports it as
// ryft_db_up{service} (1 or 0) and ryft_db_ping_failures_total{service}: the
// Supabase connection alert. Every service's tick calls it as its first step
// (D219); there is no watch goroutine any more, as Cloud Run gives a service CPU
// only while a request runs. The error is the tick's to log. A ping cut because ctx
// ended (the tick's caller hung up) is no verdict on the database: it sets nothing.
func Ping(ctx context.Context, pool Pinger, service string) error {
	short, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	err := pool.Ping(short)
	switch {
	case err != nil && ctx.Err() != nil:
		return err
	case err != nil:
		metrics.DBPingFailures.WithLabelValues(service).Inc()
		metrics.DBUp.WithLabelValues(service).Set(0)
		return fmt.Errorf("database unreachable: %w", err)
	}
	metrics.DBUp.WithLabelValues(service).Set(1)
	return nil
}
