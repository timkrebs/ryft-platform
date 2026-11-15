#!/usr/bin/env bash
# Session state in the services' SQL (design §4, D221). On Cloud Run the services reach Postgres
# through Supavisor's transaction pooler (6543), which gives each transaction whichever server
# connection is free: whatever outlives a transaction stays on that server connection, for the
# next client, and is missing for this client's next statement. This fails (exit 1, one finding per
# line, file:line) on
#   - session SET: SET without LOCAL (SET x = …, SET SESSION …, SET ROLE …, SET TIME ZONE …), and
#     set_config(…) whose third argument is not literally true. SET LOCAL, SET TRANSACTION and
#     SET CONSTRAINTS end with their transaction;
#   - LISTEN and UNLISTEN;
#   - temporary tables: CREATE [GLOBAL|LOCAL] TEMP|TEMPORARY TABLE|VIEW|SEQUENCE, SELECT … INTO TEMP,
#     pg_temp.;
#   - session advisory locks: pg_advisory_lock, pg_try_advisory_lock, their _shared forms and
#     pg_advisory_unlock*. pg_advisory_xact_lock is the one to use (backend/internal/store/locks.go);
#   - prepared statements: PREPARE and DEALLOCATE, a Prepare( call (pgx, database/sql) and
#     pgx.QueryExecModeCacheStatement. They live on a server connection; DATABASE_URL's
#     default_query_exec_mode=exec sends unnamed ones;
#   - DECLARE … WITH HOLD: a cursor that outlives its transaction.
# It reads every .go and .sql file under the roots given (paths relative to the repository root, or
# absolute; default backend), except
#   - */internal/db/migrations/: the migrate job applies them on the session pooler (5432), each
#     file in a transaction of its own under one advisory lock;
#   - *_test.go and */internal/testsupport/: the tests talk to a throwaway server directly (CI's
#     pooler job sends only the store's own queries through PgBouncer), and testsupport's role lock is
#     a session lock on a direct connection by design;
#   - testdata/ directories: fixtures, not code.
# Go comments (// …) and SQL comments (-- …) are not read. A statement start is right after a quote,
# a backtick or a semicolon, the line after one that ends by opening a raw string (Go), or a line
# after a finished statement (.sql); an UPDATE's SET on its own line is none. Keywords are read in
# upper case, as this repository writes SQL; lower case only where prose cannot look the same: SET
# of the settings named below ("set X=true" and "listen on" are prose in error and log messages),
# the function names, CREATE TEMP TABLE and CURSOR … WITH HOLD.
# A line that must stay as it is carries `session-state: ok (<the reason>)`.
# Needs bash, find, sort and a POSIX awk (mawk, gawk, BSD awk). Exit 2: no file to read.
# Usage: scripts/check_session_state.sh [root…]
set -euo pipefail
cd "$(dirname "$0")/.."
[ "$#" -gt 0 ] || set -- backend

files=()
while IFS= read -r f; do files+=("$f"); done < <(
  find "$@" -type f \( -name '*.go' -o -name '*.sql' \) \
    -not -name '*_test.go' -not -path '*/internal/db/migrations/*' \
    -not -path '*/internal/testsupport/*' -not -path '*/testdata/*' | LC_ALL=C sort)
if [ "${#files[@]}" = 0 ]; then
  echo "check_session_state: no .go or .sql file under $*" >&2
  exit 2
fi

read -r -d '' PROG <<'AWK' || true
function trim(s) { sub(/^[[:space:]]+/, "", s); sub(/[[:space:]]+$/, "", s); return s }
function finding(rule) {
  printf "✗ %s:%d: %s: %s\n", FILENAME, FNR, rule, trim(orig) > "/dev/stderr"
  if (gha) printf "::error file=%s,line=%d,title=session state::%s (a transaction-mode pooler; scripts/check_session_state.sh)\n", FILENAME, FNR, rule > "/dev/stderr"
  bad++
}
# set_upper: a SET at a statement start whose next word is not LOCAL, TRANSACTION or CONSTRAINTS.
function set_upper(s,   t, w) {
  t = s
  while (match(t, /[`";][[:space:]]*SET[[:space:]]+[^[:space:]]+/)) {
    w = substr(t, RSTART, RLENGTH)
    sub(/^[`";][[:space:]]*SET[[:space:]]+/, "", w)
    if (w !~ /^(LOCAL|TRANSACTION|CONSTRAINTS)([^A-Za-z0-9_]|$)/) return 1
    t = substr(t, RSTART + RLENGTH)
  }
  return 0
}
FNR == 1 { files++; is_sql = (FILENAME ~ /\.sql$/); opened = 0; ended = 1 }
{
  orig = $0
  line = $0
  allowed = (line ~ /session-state: ok \([^)]+\)/)
  if (!is_sql) {
    if (line ~ /^[[:space:]]*\/\//) line = ""
    sub(/[[:space:]]\/\/[[:space:]].*$/, "", line)
  }
  if (line ~ /^[[:space:]]*--/) line = ""
  sub(/[[:space:]]--([[:space:]].*)?$/, "", line)

  # Mark the line's start as a statement start where it is one, so one pattern covers all cases.
  s = line
  if ((!is_sql && opened) || (is_sql && ended)) s = ";" s
  low = tolower(s)

  if (!allowed) {
    if (set_upper(s) || low ~ /[`";][[:space:]]*set[[:space:]]+(session[[:space:]]+)?(search_path|statement_timeout|lock_timeout|idle_in_transaction_session_timeout|idle_session_timeout|time[[:space:]]+zone|timezone|application_name|work_mem|datestyle|client_encoding|session[[:space:]]+authorization)([^a-z0-9_]|$)/)
      finding("session SET (use SET LOCAL inside the transaction)")
    else if (low ~ /set_config[[:space:]]*\(/ && low !~ /set_config[[:space:]]*\(.*,[[:space:]]*true[[:space:]]*\)/)
      finding("set_config without is_local true")
    else if (s ~ /(^|[^A-Za-z0-9_])(UN)?LISTEN([[:space:]]+[A-Za-z_"*]|[[:space:]]*[`";]|$)/)
      finding("LISTEN/UNLISTEN")
    else if (low ~ /create[[:space:]]+((global|local)[[:space:]]+)?temp(orary)?[[:space:]]+(table|view|sequence)([^a-z0-9_]|$)/ || s ~ /(^|[^A-Za-z0-9_])INTO[[:space:]]+TEMP(ORARY)?[[:space:]]/ || low ~ /(^|[^a-z0-9_])pg_temp\./)
      finding("temporary table")
    else if (low ~ /pg_(try_)?advisory_(lock|lock_shared|unlock|unlock_shared|unlock_all)[[:space:]]*\(/)
      finding("session advisory lock (use pg_advisory_xact_lock)")
    else if (s ~ /(^|[^A-Za-z0-9_])(PREPARE|DEALLOCATE)[[:space:]]+[A-Za-z_"]/ || line ~ /\.Prepare(Context)?\(/ || line ~ /QueryExecModeCacheStatement/)
      finding("named prepared statement")
    else if (s ~ /(^|[^A-Za-z0-9_])WITH[[:space:]]+HOLD([^A-Za-z0-9_]|$)/ || low ~ /cursor([[:space:]].*)?[[:space:]]with[[:space:]]+hold([^a-z0-9_]|$)/)
      finding("cursor WITH HOLD")
  }

  if (!is_sql) opened = (line ~ /`[[:space:]]*$/)
  else if (trim(line) != "") ended = (line ~ /;[[:space:]]*$/)
}
END {
  if (bad) {
    printf "✗ check_session_state: %d finding(s) in %d files. A transaction-mode pooler (Supavisor on 6543) hands each transaction any server connection: keep settings, locks, tables, statements and cursors inside the transaction (design §4, D221).\n", bad, files > "/dev/stderr"
    exit 1
  }
  printf "✓ check_session_state: %d files, no session SET, set_config(…, false), LISTEN, temporary table, session advisory lock, named prepared statement or WITH HOLD cursor (migrations, tests and testsupport excluded)\n", files
}
AWK

gha=0
[ "${GITHUB_ACTIONS:-}" != true ] || gha=1
awk -v gha="$gha" "$PROG" "${files[@]}"
