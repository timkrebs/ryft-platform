package db

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The test's stand-ins for deploy/run/config.env (SUPABASE_PROJECT_REF, SUPABASE_POOLER_HOST) and the
// image's CA path: the URLs below have scripts/cloudrun_render.py's shape, and the CA is the file the
// image bakes in (ca_test.go), so pgx's parse reads a real certificate.
var (
	testRef    = strings.Repeat("q", 20)
	testPooler = "aws-1-eu-central-1.pooler.supabase.com"
)

// runURL is a DATABASE_URL as the renderer writes it, with userinfo, port and query given.
func runURL(userinfo, port, query string) string {
	return "postgresql://" + userinfo + "@" + testPooler + ":" + port + "/postgres?" + query
}

const (
	tlsQuery     = "sslmode=verify-full&sslrootcert=" + supabaseCAFile
	serviceQuery = tlsQuery + "&default_query_exec_mode=exec"
)

// cloudRunEnv sets the process as Cloud Run would (env "K_SERVICE", "CLOUD_RUN_JOB" or "" for
// neither) and clears the PG* variables that pgx folds into the config.
func cloudRunEnv(t *testing.T, env string) {
	t.Helper()
	for _, k := range []string{"K_SERVICE", "CLOUD_RUN_JOB", "PGPASSWORD", "PGOPTIONS", "PGTZ", "PGAPPNAME",
		"PGPORT", "PGSSLMODE", "PGSSLROOTCERT", "PGHOST", "PGUSER", "PGDATABASE", "PGPASSFILE", "PGSERVICE"} {
		t.Setenv(k, "")
	}
	switch env {
	case "K_SERVICE":
		t.Setenv("K_SERVICE", "api")
	case "CLOUD_RUN_JOB":
		t.Setenv("CLOUD_RUN_JOB", "migrate")
	}
}

// D221: on Cloud Run, PoolConfig refuses a DATABASE_URL with a password, or without
// sslmode=verify-full and sslrootcert; a service also needs port 6543, exec mode and no startup
// parameter but application_name. Every refusal names its setting and never the URL's userinfo;
// each one stands alone, and several are named together.
func TestCloudRunDatabaseURL(t *testing.T) {
	pw := strings.Repeat("Zq9", 4) // assembled at run time: no literal for a secret scanner
	svcUser, jobUser := "ryft_api."+testRef, "postgres."+testRef
	for _, c := range []struct {
		name, env, url string
		pgoptions      string
		want           []string // "" items are not used; nil means accepted
	}{
		{name: "the service URL", env: "K_SERVICE", url: runURL(svcUser, "6543", serviceQuery)},
		{name: "the service URL with application_name", env: "K_SERVICE", url: runURL(svcUser, "6543", serviceQuery+"&application_name=api")},
		{name: "a password in the userinfo", env: "K_SERVICE", url: runURL(svcUser+":"+pw, "6543", serviceQuery),
			want: []string{"carries a password"}},
		{name: "a password parameter", env: "K_SERVICE", url: runURL(svcUser, "6543", serviceQuery+"&password="+pw),
			want: []string{"carries a password"}},
		{name: "sslmode require", env: "K_SERVICE", url: runURL(svcUser, "6543", strings.Replace(serviceQuery, "verify-full", "require", 1)),
			want: []string{`sslmode must be verify-full, is "require"`}},
		{name: "no sslmode", env: "K_SERVICE", url: runURL(svcUser, "6543", "sslrootcert="+supabaseCAFile+"&default_query_exec_mode=exec"),
			want: []string{`sslmode must be verify-full, is ""`}},
		{name: "no sslrootcert", env: "K_SERVICE", url: runURL(svcUser, "6543", "sslmode=verify-full&default_query_exec_mode=exec"),
			want: []string{"sslrootcert is missing"}},
		{name: "the session port on a service", env: "K_SERVICE", url: runURL(svcUser, "5432", serviceQuery),
			want: []string{"the port must be 6543 (Supavisor's transaction mode), is 5432"}},
		{name: "no port on a service (pgx's default 5432)", env: "K_SERVICE",
			url:  "postgresql://" + svcUser + "@" + testPooler + "/postgres?" + serviceQuery,
			want: []string{"the port must be 6543"}},
		{name: "the default exec mode on a service", env: "K_SERVICE", url: runURL(svcUser, "6543", tlsQuery),
			want: []string{`default_query_exec_mode must be exec`, `is "cache statement"`}},
		{name: "the simple protocol on a service", env: "K_SERVICE", url: runURL(svcUser, "6543", tlsQuery+"&default_query_exec_mode=simple_protocol"),
			want: []string{`is "simple protocol"`}},
		{name: "a startup parameter in the URL", env: "K_SERVICE", url: runURL(svcUser, "6543", serviceQuery+"&search_path=app"),
			want: []string{`startup parameter "search_path"`, "SET LOCAL"}},
		{name: "a startup parameter from PGOPTIONS", env: "K_SERVICE", url: runURL(svcUser, "6543", serviceQuery),
			pgoptions: "-c statement_timeout=0", want: []string{`startup parameter "options"`}},
		{name: "a key=value string", env: "K_SERVICE", url: "host=" + testPooler + " port=6543 user=" + svcUser + " sslmode=verify-full",
			want: []string{"it must be a postgresql:// URL"}},
		{name: "a CA file that is not there", env: "K_SERVICE", url: runURL(svcUser, "6543", strings.Replace(serviceQuery, supabaseCAFile, "/nonexistent/ca.crt", 1)),
			want: []string{"pgx cannot read it: unable to read CA file"}},
		{name: "everything at once", env: "K_SERVICE", url: runURL(svcUser+":"+pw, "5432", "sslmode=prefer"),
			want: []string{"carries a password", `sslmode must be verify-full, is "prefer"`, "sslrootcert is missing",
				"the port must be 6543", "default_query_exec_mode must be exec"}},

		{name: "the migrate URL", env: "CLOUD_RUN_JOB", url: runURL(jobUser, "5432", tlsQuery)},
		{name: "a job keeps session-mode settings", env: "CLOUD_RUN_JOB", url: runURL(jobUser, "5432", tlsQuery+"&search_path=public")},
		{name: "a password in the job's URL", env: "CLOUD_RUN_JOB", url: runURL(jobUser+":"+pw, "5432", tlsQuery),
			want: []string{"(CLOUD_RUN_JOB is set)", "carries a password"}},
		{name: "the job without verify-full", env: "CLOUD_RUN_JOB", url: runURL(jobUser, "5432", "sslmode=require"),
			want: []string{`sslmode must be verify-full, is "require"`, "sslrootcert is missing"}},

		{name: "local: compose's URL", env: "", url: "postgres://ryft_api:" + pw + "@postgres:5432/ryft?sslmode=disable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cloudRunEnv(t, c.env)
			if c.pgoptions != "" {
				t.Setenv("PGOPTIONS", c.pgoptions)
			}
			cfg, err := PoolConfig(c.url, 4)
			if c.want == nil {
				if err != nil {
					t.Fatalf("PoolConfig = %v, want it accepted", err)
				}
				if cfg.AfterConnect == nil || cfg.MaxConns != 4 {
					t.Fatalf("the accepted config lacks its settings: AfterConnect %v, MaxConns %d", cfg.AfterConnect != nil, cfg.MaxConns)
				}
				return
			}
			if err == nil {
				t.Fatalf("PoolConfig accepted it, want %q", c.want)
			}
			msg := err.Error()
			if want := "DATABASE_URL is refused on Cloud Run (" + c.env + " is set): "; !strings.HasPrefix(msg, want) {
				t.Fatalf("error %q, want the prefix %q", msg, want)
			}
			for _, w := range c.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q lacks %q", msg, w)
				}
			}
			for _, secret := range []string{pw, testRef, svcUser, jobUser} {
				if strings.Contains(msg, secret) {
					t.Errorf("error %q repeats the URL's userinfo (%q)", msg, secret)
				}
			}
		})
	}
}

// pgx reads PGPASSWORD when the URL has none (no network: the parse only), which is how the
// password reaches Postgres on Cloud Run. The parsed config is what each rule promises: TLS
// against the baked CA and the pooler's name, no plaintext fallback, the port, the mode, no
// startup parameter.
func TestCloudRunPasswordFromPGPASSWORD(t *testing.T) {
	pem, err := os.ReadFile(supabaseCAFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		t.Fatal("the CA does not parse")
	}
	pw := strings.Join([]string{"from", "secret", "manager"}, "-")
	for _, c := range []struct {
		env, url string
		port     uint16
		mode     pgx.QueryExecMode
	}{
		{"K_SERVICE", runURL("ryft_api."+testRef, "6543", serviceQuery), 6543, pgx.QueryExecModeExec},
		{"CLOUD_RUN_JOB", runURL("postgres."+testRef, "5432", tlsQuery), 5432, pgx.QueryExecModeCacheStatement},
	} {
		t.Run(c.env, func(t *testing.T) {
			cloudRunEnv(t, c.env)
			t.Setenv("PGPASSWORD", pw)
			cfg, err := PoolConfig(c.url, 2)
			if err != nil {
				t.Fatal(err)
			}
			cc := cfg.ConnConfig
			if cc.Password != pw {
				t.Errorf("Password = %q, want PGPASSWORD's", cc.Password)
			}
			if cc.Port != c.port || cc.DefaultQueryExecMode != c.mode || len(cc.RuntimeParams) != 0 {
				t.Errorf("port %d, mode %s, startup parameters %v; want %d, %s, none", cc.Port, cc.DefaultQueryExecMode, cc.RuntimeParams, c.port, c.mode)
			}
			tls := cc.TLSConfig
			switch {
			case tls == nil:
				t.Fatal("no TLS")
			case tls.InsecureSkipVerify || tls.ServerName != testPooler:
				t.Errorf("TLS does not verify the pooler's name: skip %v, ServerName %q", tls.InsecureSkipVerify, tls.ServerName)
			case tls.RootCAs == nil || !tls.RootCAs.Equal(roots):
				t.Error("TLS does not trust exactly the baked CA")
			}
			if len(cc.Fallbacks) != 0 {
				t.Errorf("%d fallback configs: verify-full must have no plaintext fallback", len(cc.Fallbacks))
			}
		})
	}
}

// A refused DATABASE_URL is a setting, not a state: Open returns at once instead of retrying for
// MigrateWait (its first retry would wait 5 s).
func TestOpenRefusesWithoutRetrying(t *testing.T) {
	cloudRunEnv(t, "K_SERVICE")
	start := time.Now()
	_, err := Open(context.Background(), runURL("ryft_api."+testRef, "5432", serviceQuery), 2, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "the port must be 6543") {
		t.Fatalf("Open = %v, want the port refused", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Open took %v: it retried a refused DATABASE_URL", d)
	}
}

// On Cloud Run, Open stops after a refused login (class 28) or an open circuit breaker; one
// refusal with Supavisor's "recently changed" hint gets one more try (D93). Anything else (the
// network, a missing database) is retried as before.
func TestStopRetrying(t *testing.T) {
	wrap := func(e error) error { return fmt.Errorf("ping: %w", e) }
	refusedLogin := wrap(&pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"})
	stale := wrap(&pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: "password authentication failed",
		Hint: "the password was recently changed, retry"})
	for _, c := range []struct {
		name string
		errs []error
		want []bool
	}{
		{"connection refused", []error{wrap(&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})}, []bool{false}},
		{"no such database", []error{wrap(&pgconn.PgError{Code: "3D000"})}, []bool{false}},
		{"password refused", []error{refusedLogin}, []bool{true}},
		{"no pg_hba entry", []error{wrap(&pgconn.PgError{Code: "28000"})}, []bool{true}},
		{"circuit breaker", []error{wrap(&pgconn.PgError{Code: "XX000", Message: "(ECIRCUITBREAKER) too many authentication errors"})}, []bool{true}},
		{"a stale verifier, then the new one fails too", []error{stale, stale}, []bool{false, true}},
		{"a stale verifier after another refusal", []error{refusedLogin, stale}, []bool{true, true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			logins := 0
			for i, e := range c.errs {
				if got := stopRetrying(e, &logins); got != c.want[i] {
					t.Fatalf("attempt %d: stopRetrying(%v) = %v, want %v", i+1, e, got, c.want[i])
				}
			}
		})
	}
}
