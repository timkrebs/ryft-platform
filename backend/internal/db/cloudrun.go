package db

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DATABASE_URL on Cloud Run (D221; design §4; docs/work/2026-11-14-gcp/ticket-b.md →
// Interfaces A must match). scripts/cloudrun_render.py renders it:
//
//	services  postgresql://ryft_<svc>.<ref>@<pooler host>:6543/postgres?sslmode=verify-full&
//	          sslrootcert=/etc/ryft/supabase-ca.crt&default_query_exec_mode=exec
//	migrate   postgresql://postgres.<ref>@<pooler host>:5432/postgres?sslmode=verify-full&
//	          sslrootcert=/etc/ryft/supabase-ca.crt
//
// The link crosses the internet from Google to AWS and carries health data and the
// SCRAM login, so TLS verifies Supabase's CA (baked into the image, backend/Dockerfile)
// and the host name. DATABASE_URL is plain configuration in git: the password comes only
// as PGPASSWORD from Secret Manager, which pgx reads when the URL has none. A service
// talks to Supavisor's transaction pooler (6543), which keeps no prepared statement and
// hands each transaction whichever server connection is free; the migrate job uses the
// session pooler (5432) as the schema owner.

// transactionPoolerPort is Supavisor's transaction mode.
const transactionPoolerPort = 6543

// cloudRun names the variable Cloud Run set for this process: K_SERVICE in a service
// (service true), CLOUD_RUN_JOB in a job, "" elsewhere. With both, the stricter service
// rules apply.
func cloudRun() (env string, service bool) {
	switch {
	case os.Getenv("K_SERVICE") != "":
		return "K_SERVICE", true
	case os.Getenv("CLOUD_RUN_JOB") != "":
		return "CLOUD_RUN_JOB", false
	}
	return "", false
}

// urlFindings reads raw as text, before pgx does: a postgresql:// URL without a
// password (none in the userinfo, no password parameter), with sslmode=verify-full and
// an sslrootcert. What it reports names the setting, never the URL or its userinfo.
func urlFindings(raw string) []string {
	if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
		return []string{"it must be a postgresql:// URL"}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return []string{"it does not parse as a URL"} // url.Error's text holds the URL: not shown
	}
	var f []string
	q := u.Query()
	if _, ok := u.User.Password(); ok || q.Has("password") {
		f = append(f, "it carries a password (the password comes only as PGPASSWORD, from Secret Manager)")
	}
	if m := q.Get("sslmode"); m != "verify-full" {
		f = append(f, fmt.Sprintf("sslmode must be verify-full, is %q", m))
	}
	if q.Get("sslrootcert") == "" {
		f = append(f, "sslrootcert is missing (/etc/ryft/supabase-ca.crt)")
	}
	return f
}

// serviceFindings are a service's rules on the parsed config: the transaction pooler's
// port on every host, pgx's exec mode (unnamed statements, sent with their bind in one
// round trip), and no startup parameter but application_name. A transaction-mode pooler
// refuses startup parameters it does not know (PgBouncer) or cannot keep them per client,
// and pgx turns PGOPTIONS and PGTZ into startup parameters too, so they are read after the
// parse.
func serviceFindings(cfg *pgx.ConnConfig) []string {
	var f []string
	ports := []uint16{cfg.Port}
	for _, fb := range cfg.Fallbacks {
		ports = append(ports, fb.Port)
	}
	for _, p := range ports {
		if p != transactionPoolerPort {
			f = append(f, fmt.Sprintf("the port must be %d (Supavisor's transaction mode), is %d", transactionPoolerPort, p))
			break
		}
	}
	if m := cfg.DefaultQueryExecMode; m != pgx.QueryExecModeExec {
		f = append(f, fmt.Sprintf("default_query_exec_mode must be exec (unnamed statements: the transaction pooler "+
			"keeps no prepared statement), is %q", m.String()))
	}
	var params []string
	for k := range cfg.RuntimeParams {
		if k != "application_name" {
			params = append(params, k)
		}
	}
	sort.Strings(params)
	for _, k := range params {
		f = append(f, fmt.Sprintf("startup parameter %q (from the URL or a PG* variable) is refused: the transaction "+
			"pooler would refuse it or apply it to whichever server connection comes next; set it per transaction "+
			"with SET LOCAL", k))
	}
	return f
}

func refused(env string, findings []string) error {
	return fmt.Errorf("DATABASE_URL is refused on Cloud Run (%s is set): %s", env, strings.Join(findings, "; "))
}

// parseReason is pgx's reason without the connection string, which its error repeats
// (the password masked, the user and host not).
func parseReason(err error) string {
	var pe *pgconn.ParseConfigError
	if errors.As(err, &pe) {
		if inner := errors.Unwrap(pe); inner != nil {
			return inner.Error()
		}
	}
	return "the reason is not shown, as pgx's message repeats the URL"
}

// refusedLogin is Open's error when it stopped on Cloud Run after a refused login or
// an open circuit breaker (stopRetrying).
type refusedLogin struct {
	env string // the variable that made it Cloud Run: K_SERVICE or CLOUD_RUN_JOB
	err error  // the last attempt's
}

func (e *refusedLogin) Error() string {
	return fmt.Sprintf("database: %v (not retried in a loop on Cloud Run, %s is set: every failed login feeds "+
		"Supavisor's circuit breaker for the whole role)", e.err, e.env)
}

func (e *refusedLogin) Unwrap() error { return e.err }

// LoginRefused reports whether err is (or wraps) Open's verdict on Cloud Run that the
// login was refused (SQLSTATE class 28) or Supavisor's circuit breaker is open
// (ECIRCUITBREAKER): a state that another try right away only makes worse, as every
// failed login feeds the breaker for the whole role. Off Cloud Run Open retries those
// like anything else, so it is never true there; nor for a refused DATABASE_URL, a
// network error or a context's end.
func LoginRefused(err error) bool {
	var e *refusedLogin
	return errors.As(err, &e)
}

// stopRetrying says whether Open stops after err on Cloud Run: the circuit breaker is
// open (ECIRCUITBREAKER), or the login was refused (SQLSTATE class 28). One refusal
// whose hint says the password was recently changed is retried: Supavisor still had the
// role's previous verifier, and fetches the new one with that refusal (D93, as
// rolePasswordCurrent does). logins counts the refusals so far.
func stopRetrying(err error, logins *int) bool {
	if strings.Contains(err.Error(), "ECIRCUITBREAKER") {
		return true
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || !strings.HasPrefix(pe.Code, "28") {
		return false
	}
	*logins++
	return *logins > 1 || !strings.Contains(pe.Hint, "recently changed")
}
