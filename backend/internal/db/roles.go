package db

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ServiceRoles maps each service to its database role (migrations 0009, 0015).
// The migrate command reads the role's password from DB_PASSWORD_<SERVICE>.
var ServiceRoles = map[string]string{
	"ingest":  "ryft_ingest",
	"scoring": "ryft_scoring",
	"api":     "ryft_api",
	"notify":  "ryft_notify",
}

// SetRolePassword enables LOGIN for role and sets its password. Postgres is
// sent a SCRAM-SHA-256 verifier, never the password itself, so the password
// cannot end up in the server's statement log.
func SetRolePassword(ctx context.Context, pool *pgxpool.Pool, role, password string) error {
	if !slices.Contains(roleNames(), role) {
		return fmt.Errorf("unknown service role %q", role)
	}
	if password == "" {
		return errors.New("empty password")
	}
	verifier, err := scramVerifier(password, nil, scramIterations)
	if err != nil {
		return err
	}
	// ALTER ROLE takes no bind parameters; the verifier is base64 and '$:' only.
	_, err = pool.Exec(ctx, fmt.Sprintf(`ALTER ROLE %s WITH LOGIN PASSWORD '%s'`,
		pgx.Identifier{role}.Sanitize(), verifier))
	return err
}

// EnsureRolePassword sets role's password unless the role can already log in
// with it, and says whether it did. Setting a password draws a new SCRAM salt
// even when the password is the same, and Supabase's pooler (Supavisor) caches
// each role's verifier: until it notices, every service connection fails once
// ("password was recently changed"), and ten failures from one address in
// 150 s block that address, the whole cluster behind its NAT, for two minutes
// (D93). migrate runs on every deploy, so it only writes a password that
// changed. ownerURL is pool's connection string (DATABASE_URL): the role logs
// in at the same place, as the pooler's user for it ("postgres.<ref>" →
// "ryft_api.<ref>"). Any other failure of that login is returned: credentials
// are not rewritten blindly.
func EnsureRolePassword(ctx context.Context, pool *pgxpool.Pool, ownerURL, role, password string) (bool, error) {
	if !slices.Contains(roleNames(), role) {
		return false, fmt.Errorf("unknown service role %q", role)
	}
	if password == "" {
		return false, errors.New("empty password")
	}
	current, err := rolePasswordCurrent(ctx, pool, ownerURL, role, password)
	if err != nil || current {
		return false, err
	}
	return true, SetRolePassword(ctx, pool, role, password)
}

// rolePasswordCurrent logs in as role with password: true when that works,
// false when the role has no LOGIN or the server refuses the password.
func rolePasswordCurrent(ctx context.Context, pool *pgxpool.Pool, ownerURL, role, password string) (bool, error) {
	var canLogin bool
	if err := pool.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname = $1`, role).Scan(&canLogin); err != nil {
		return false, fmt.Errorf("role %s: %w", role, err)
	}
	if !canLogin {
		return false, nil
	}
	cfg, err := pgx.ParseConfig(ownerURL)
	if err != nil {
		return false, err
	}
	cfg.User, cfg.Password = poolerUser(cfg.User, role), password
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		conn, err := pgx.ConnectConfig(cctx, cfg)
		cancel()
		if err == nil {
			_ = conn.Close(ctx)
			return true, nil
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || !strings.HasPrefix(pgErr.Code, "28") {
			return false, fmt.Errorf("log in as %s to check its password: %w", role, err)
		}
		// Supavisor refused with a verifier it had cached before the last change
		// (the same 28P01): it has the new one now, so one more try tells.
		if attempt == 0 && strings.Contains(pgErr.Hint, "recently changed") {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}
		return false, nil
	}
}

// poolerUser is role's user name where owner logs in: Supavisor names the
// project after a dot ("postgres.<ref>"), Postgres itself does not.
func poolerUser(owner, role string) string {
	if _, tenant, ok := strings.Cut(owner, "."); ok {
		return role + "." + tenant
	}
	return role
}

func roleNames() []string {
	out := make([]string, 0, len(ServiceRoles))
	for _, r := range ServiceRoles {
		out = append(out, r)
	}
	return out
}

// scramIterations matches Postgres's default (scram_iterations, PG 16+).
const scramIterations = 4096

// scramVerifier builds the value Postgres stores for a SCRAM-SHA-256
// password (RFC 5802/7677): SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey>.
// The password must be ASCII without control characters, so SASLprep leaves it
// unchanged; the generated passwords are alphanumeric. A nil salt draws a
// random 16-byte one.
func scramVerifier(password string, salt []byte, iterations int) (string, error) {
	for _, c := range password {
		if c < 0x21 || c > 0x7e {
			return "", errors.New("password must be printable ASCII without spaces")
		}
	}
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := rand.Read(salt); err != nil {
			return "", err
		}
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	mac := func(key []byte, msg string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(msg))
		return h.Sum(nil)
	}
	clientKey := mac(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := mac(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(storedKey[:]), b64(serverKey)), nil
}

// embeddedVersions lists the migration versions compiled into this binary.
func embeddedVersions() ([]int, error) {
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var out []int
	for _, e := range entries {
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version prefix", e.Name())
		}
		out = append(out, v)
	}
	slices.Sort(out)
	return out, nil
}

// Pending returns the embedded migrations not yet recorded in
// schema_migrations. A database ahead of this binary (a newer migrate already
// ran during a rolling deploy) has nothing pending.
func Pending(ctx context.Context, pool *pgxpool.Pool) ([]int, error) {
	want, err := embeddedVersions()
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return want, err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return want, err
	}
	var missing []int
	for _, v := range want {
		if !slices.Contains(have, v) {
			missing = append(missing, v)
		}
	}
	return missing, nil
}

// WaitMigrated blocks until the schema has every migration this binary was
// built with. The services connect as least-privilege roles that cannot run
// DDL; the migrate job (cmd/migrate) applies the migrations, and a service
// that starts first waits for it. Gives up when ctx ends.
func WaitMigrated(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	const every = 2 * time.Second
	var lastLog time.Time
	for {
		missing, err := Pending(ctx, pool)
		if err == nil && len(missing) == 0 {
			return nil
		}
		if time.Since(lastLog) >= 30*time.Second {
			log.WarnContext(ctx, "waiting for the migrate job", "missing", missing, "err", err)
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("schema not migrated (missing %v): %w", missing, ctx.Err())
		case <-time.After(every):
		}
	}
}
