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
