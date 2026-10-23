// Package db owns the Postgres connection pool and embedded SQL migrations.
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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/retry"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Connect opens a pool and pings it. timestamptz values scan as UTC so every
// timestamp the API emits is UTC regardless of the container's TZ.
//
// maxConns caps the pool (0 keeps pgx's default). Behind Supabase's Supavisor
// in session mode every pooled connection holds a server connection, so the
// services keep their pools small (DB_MAX_CONNS). Connections are recycled
// every 30 minutes so pooler and server restarts are absorbed.
func Connect(ctx context.Context, url string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = time.Minute
	cfg.AfterConnect = func(_ context.Context, c *pgx.Conn) error {
		c.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID,
			Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
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
func Open(ctx context.Context, url string, maxConns int, log *slog.Logger) (*pgxpool.Pool, error) {
	wait, cancel := context.WithTimeout(ctx, MigrateWait)
	defer cancel()
	var pool *pgxpool.Pool
	err := retry.Until(wait, log, "database", retry.Startup, func(context.Context) error {
		p, err := Connect(ctx, url, maxConns) // the pool outlives the wait
		pool = p
		return err
	})
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
const migrationLock = 7_419_001

// lockTimeout bounds how long any statement of a migration waits for a lock
// (Postgres lock_timeout, error 55P03). Without it a migration that needs a strong
// lock on a busy table waits indefinitely and every later query queues behind its
// request. A var so tests can shorten it; a migration may still set its own with
// SET LOCAL, which runs after this and wins (0027 does, 5 s).
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
					return fmt.Errorf("apply %s: hit the lock timeout (%s default; nothing applied, schema_migrations unchanged; "+
						"find the blocker in pg_stat_activity and pg_locks, then rerun): %w", e.Name(), lockTimeout, err)
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after Commit
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLock); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", lockTimeout.Milliseconds())); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// WatchEvery is how often Watch pings the database.
const WatchEvery = 30 * time.Second

// Watch pings the database every WatchEvery until ctx ends and reports it as
// ryft_db_up{service} (1 or 0) and ryft_db_ping_failures_total{service}: the
// Supabase connection alert, independent of traffic. A failure and the
// recovery after it are logged once each.
func Watch(ctx context.Context, pool *pgxpool.Pool, service string, log *slog.Logger) {
	t := time.NewTicker(WatchEvery)
	defer t.Stop()
	up := true
	for {
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := pool.Ping(short)
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			metrics.DBPingFailures.WithLabelValues(service).Inc()
			metrics.DBUp.WithLabelValues(service).Set(0)
			if up {
				log.Warn("database unreachable", "err", err)
			}
			up = false
		default:
			metrics.DBUp.WithLabelValues(service).Set(1)
			if !up {
				log.Info("database reachable again")
			}
			up = true
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
