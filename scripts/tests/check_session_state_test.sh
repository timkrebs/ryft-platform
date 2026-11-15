#!/usr/bin/env bash
# scripts/check_session_state.sh: the committed backend passes; the transaction-scoped forms, the
# prose that looks like SQL and the excluded paths pass (and the excluded files are not counted); each
# session-level form fails and names its file, line and rule (Go and .sql, a raw string that starts
# on the next line, after a semicolon); a trailing comment hides nothing before it, an allow marker
# needs its reason; CI gets an annotation; no file to read is exit 2.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
script=$HERE/scripts/check_session_state.sh
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_session_state_test: $*" >&2; exit 1; }
run() { rc=0; env -u GITHUB_ACTIONS bash "$script" "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?; }

run
[ "$rc" = 0 ] || fail "the committed backend must pass (rc=$rc): $(cat "$tmp/err")"
grep -q '^✓ check_session_state: [0-9][0-9]* files' "$tmp/out" || fail "no summary line: $(cat "$tmp/out")"

# --- what passes -------------------------------------------------------------------------------
p=$tmp/pass/backend
mkdir -p "$p/internal/x" "$p/internal/db/migrations" "$p/internal/testsupport" "$p/internal/y/testdata"
cat > "$p/internal/x/ok.go" <<'EOF'
package x

// Comments may name anything: SET search_path = x, pg_advisory_lock(1), LISTEN ch, PREPARE q AS SELECT 1.
const (
	lock   = `SELECT pg_advisory_xact_lock($1, hashtext($2::text))`
	local  = `SET LOCAL statement_timeout = '5s'`
	conf   = `SELECT set_config('lock_timeout', $1, true)`
	nested = `SELECT set_config('lock_timeout', format('%sms', $1::int), true)`
	iso    = `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`
	defer_ = `SET CONSTRAINTS ALL DEFERRED`
	cursor = `DECLARE c CURSOR FOR SELECT 1`
	notify = `SELECT pg_notify('ch', $1)`
)

func f() {
	_ = `UPDATE users
		SET name = $2
		WHERE id = $1`
	_ = `
		UPDATE users SET name = $2 WHERE id = $1`
	_ = `INSERT INTO t (a) VALUES ($1) ON CONFLICT (a) DO UPDATE SET a = EXCLUDED.a`
	_ = "UPDATE t SET a = 1; SET LOCAL lock_timeout = '1s'"
	_ = errors.New("AUTH_MODE=apikey is local only; set ALLOW_INSECURE_APIKEY_AUTH=true to use it")
	_ = errors.New("set role password")
	log.Info("listen on", "addr", addr)
	ln, err := net.Listen("tcp", addr) // listen for SET search_path: a comment
	_ = errors.New("prepare the upload")
	_, _ = os.CreateTemp("", "x")
	_ = `SELECT 1` // trailing comment: pg_advisory_lock(1)
	_ = `SELECT x FROM pg_temp_notes`
	_ = `SET search_path = app` // session-state: ok (a one-off on a dedicated connection that is closed after)
}
EOF
cat > "$p/internal/x/queries.sql" <<'EOF'
-- a comment: SET search_path = x;
UPDATE t
   SET a = 1
 WHERE id = 2;

SELECT set_config('x', 'y', true);
EOF
printf 'SET search_path = app;\nCREATE TEMP TABLE t ();\n' > "$p/internal/db/migrations/0001_x.sql"
printf 'package x\nvar q = `SELECT pg_advisory_lock(1)`\n' > "$p/internal/x/x_test.go"
printf 'package testsupport\nvar q = `SELECT pg_advisory_lock(1)`\n' > "$p/internal/testsupport/roles.go"
printf 'LISTEN ch;\n' > "$p/internal/y/testdata/q.sql"
run "$tmp/pass"
[ "$rc" = 0 ] || fail "the allowed forms and excluded paths must pass (rc=$rc): $(cat "$tmp/err")"
grep -q '^✓ check_session_state: 2 files' "$tmp/out" || fail "want 2 files read (migrations, tests, testsupport, testdata excluded): $(cat "$tmp/out")"

# --- what fails: expect <rule> <file> <line>, the file's text on stdin ----------------------------
n=0
expect() {
  local rule=$1 name=$2 line=$3 d
  n=$((n + 1)); d=$tmp/fail$n/backend/internal/x; mkdir -p "$d"; cat > "$d/$name"
  run "$tmp/fail$n"
  [ "$rc" = 1 ] || fail "case $n ($rule in $name): want rc=1, got $rc: $(cat "$tmp/out" "$tmp/err")"
  grep -qF "$name:$line: $rule" "$tmp/err" || fail "case $n: want '$name:$line: $rule' in: $(cat "$tmp/err")"
}
S='session SET'
expect "$S" a.go 2 <<'EOF'
package x
var q = `SET search_path = app`
EOF
expect "$S" a.go 2 <<'EOF'
package x
var q = "SET SESSION statement_timeout = 0"
EOF
expect "$S" a.go 2 <<'EOF'
package x
var q = "SET ROLE ryft_api"
EOF
expect "$S" a.go 3 <<'EOF'
package x
var q = `
	SET lock_timeout = '1s'`
EOF
expect "$S" a.go 2 <<'EOF'
package x
var q = "SELECT 1; SET application_name TO 'x'"
EOF
expect "$S" a.go 2 <<'EOF'
package x
var q = `set search_path to app`
EOF
expect "$S" a.go 2 <<'EOF'
package x
var q = fmt.Sprintf("SET %s = %s", k, v)
EOF
expect "$S" b.sql 2 <<'EOF'
SELECT 1;
SET search_path = app;
EOF
expect "$S" a.go 2 <<'EOF'
package x
var q = `SET search_path = x` // session-state: ok
EOF
C='set_config without is_local true'
expect "$C" a.go 2 <<'EOF'
package x
var q = `SELECT set_config('search_path', 'app', false)`
EOF
expect "$C" a.go 2 <<'EOF'
package x
var q = `SELECT set_config('lock_timeout', $1, $2)`
EOF
L='LISTEN/UNLISTEN'
expect "$L" a.go 2 <<'EOF'
package x
var q = `LISTEN ryft_events`
EOF
expect "$L" a.go 2 <<'EOF'
package x
var q = "UNLISTEN *"
EOF
expect "$L" a.go 2 <<'EOF'
package x
var q = `LISTEN ch` // a trailing comment hides nothing before it
EOF
T='temporary table'
expect "$T" a.go 2 <<'EOF'
package x
var q = `CREATE TEMP TABLE t (id int)`
EOF
expect "$T" a.go 2 <<'EOF'
package x
var q = `CREATE LOCAL TEMPORARY TABLE t AS SELECT 1`
EOF
expect "$T" a.go 2 <<'EOF'
package x
var q = `SELECT * INTO TEMP t FROM x`
EOF
expect "$T" a.go 2 <<'EOF'
package x
var q = `SELECT count(*) FROM pg_temp.t`
EOF
A='session advisory lock'
for fn in 'pg_advisory_lock($1)' 'pg_try_advisory_lock(1, 2)' 'pg_advisory_lock_shared($1)' 'pg_advisory_unlock(1)' 'pg_advisory_unlock_all()'; do
  expect "$A" a.go 2 < <(printf 'package x\nvar q = `SELECT %s`\n' "$fn")
done
P='named prepared statement'
expect "$P" a.go 2 <<'EOF'
package x
var q = `PREPARE q (int) AS SELECT $1`
EOF
expect "$P" a.go 2 <<'EOF'
package x
var q = `DEALLOCATE ALL`
EOF
expect "$P" a.go 3 <<'EOF'
package x
func f() {
	_, err := conn.Prepare(ctx, "q", sql)
}
EOF
expect "$P" a.go 2 <<'EOF'
package x
var mode = pgx.QueryExecModeCacheStatement
EOF
expect 'cursor WITH HOLD' a.go 2 <<'EOF'
package x
var q = `DECLARE c CURSOR WITH HOLD FOR SELECT 1`
EOF

# Every finding is reported, with the count at the end, and CI gets an annotation per finding.
d=$tmp/many/backend; mkdir -p "$d"
printf 'package x\nvar a = `SET search_path = x`\nvar b = `LISTEN ch`\nvar c = `SELECT pg_advisory_lock(1)`\n' > "$d/m.go"
rc=0; GITHUB_ACTIONS=true bash "$script" "$tmp/many" >"$tmp/out" 2>"$tmp/err" || rc=$?
[ "$rc" = 1 ] || fail "three findings: want rc=1, got $rc"
grep -q '3 finding(s) in 1 files' "$tmp/err" || fail "want the count of 3: $(cat "$tmp/err")"
[ "$(grep -c '^::error file=.*m.go,line=[234],title=session state::' "$tmp/err")" = 3 ] || fail "want 3 annotations: $(cat "$tmp/err")"

# Nothing to read is an error, not a pass.
mkdir -p "$tmp/empty"
run "$tmp/empty"
[ "$rc" = 2 ] || fail "no .go or .sql file: want rc=2, got $rc"

echo "✓ check_session_state_test: the committed backend, the allowed forms and exclusions, and $n failing forms"
