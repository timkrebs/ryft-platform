#!/usr/bin/env bash
# scripts/rotate_db_password.sh on a copy of the repository's layout (the script and deploy/run):
# it moves exactly the two pins of db-password-<svc> (PGPASSWORD in <svc>.yaml, DB_PASSWORD_<SVC> in
# migrate.yaml) and nothing else, says when there is nothing to do, rolls back, aligns pins that
# differed, refuses wrong arguments (exit 2) and fails closed on a file it does not recognise (exit 1,
# neither file written, nothing left behind). With yq the result passes scripts/check_cloudrun.py.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
unset GITHUB_ACTIONS
fail() { echo "✗ rotate_db_password_test: $*" >&2; exit 1; }
r=$tmp/repo
run=$r/deploy/run
fresh() {
  rm -rf "$r"
  mkdir -p "$r/scripts" "$r/deploy"
  cp "$HERE/scripts/rotate_db_password.sh" "$r/scripts/"
  cp -R "$HERE/deploy/run" "$run"
}
rot() { bash "$r/scripts/rotate_db_password.sh" "$@"; }
pin_of() { sed -nE "s/.*name: $2, key: \"([0-9]+)\".*/\1/p" "$3/$1"; }  # <file> <secret> <dir>
# changed <file>: lines of the copy that differ from the committed file.
changed() { { diff "$HERE/deploy/run/$1" "$run/$1" || true; } | grep -c '^>' || true; }
same_as() { # <dir>: every file of the copy equals <dir>'s
  diff -r "$1" "$run" >/dev/null || fail "$2: the files changed: $(diff -r "$1" "$run" || true)"
}
leftovers() { [ -z "$(find "$run" -name '*.rotate-new')" ] || fail "$1: a .rotate-new file is left"; }
yq_ok=false
if command -v yq >/dev/null 2>&1 && yq --version 2>&1 | grep -q mikefarah; then yq_ok=true
elif [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI: the rotated copy cannot be checked"
fi
checked() {
  if $yq_ok; then
    local o
    o=$(python3 -I "$HERE/scripts/check_cloudrun.py" --dir "$run" 2>&1) || fail "$1: check_cloudrun.py fails on the result: $o"
  fi
}

# A rotation: the two pins move, nothing else does.
fresh
a=$(pin_of api.yaml db-password-api "$HERE/deploy/run")
[ -n "$a" ] && [ "$a" = "$(pin_of migrate.yaml db-password-api "$HERE/deploy/run")" ] ||
  fail "fixture: deploy/run pins db-password-api at '$a' in api.yaml and differently (or not) in migrate.yaml"
new=$((a + 1))
o=$(rot api "$new" 2>&1) || fail "rotate api $new failed: $o"
[ "$(pin_of api.yaml db-password-api "$run")" = "$new" ] || fail "api.yaml's PGPASSWORD is not at $new"
[ "$(pin_of migrate.yaml db-password-api "$run")" = "$new" ] || fail "migrate.yaml's DB_PASSWORD_API is not at $new"
[ "$(changed api.yaml)" = 1 ] || fail "api.yaml: want exactly one changed line, got $(changed api.yaml)"
[ "$(changed migrate.yaml)" = 1 ] || fail "migrate.yaml: want exactly one changed line, got $(changed migrate.yaml)"
for f in ingest.yaml scoring.yaml notify.yaml config.env; do
  [ "$(changed "$f")" = 0 ] || fail "$f changed on a rotation of api"
done
old_key="key: \"$a\"" new_key="key: \"$new\""  # no glob characters, so the pattern is literal
for f in api.yaml migrate.yaml; do
  want=$(grep -F 'name: db-password-api,' "$HERE/deploy/run/$f")
  grep -qxF -- "${want/$old_key/$new_key}" "$run/$f" || fail "$f: the pin line is not the committed one with $new_key"
done
case $o in *"pinned at $new"*"(PGPASSWORD, was $a)"*"(DB_PASSWORD_API, was $a)"*) ;; *) fail "the report does not say what moved: $o" ;; esac
case $o in *"gcloud secrets versions disable $a --secret=db-password-api --project=ryftlab-prod"*) ;; *) fail "the next steps do not name the old version: $o" ;; esac
case $o in *"gcloud secrets versions describe $new --secret=db-password-api"*) ;; *) fail "the next steps do not check the new version: $o" ;; esac
leftovers "a rotation"
checked "a rotation"

# Again with the same version: nothing to do, nothing written.
cp -R "$run" "$tmp/after"
o=$(rot api "$new" 2>&1) || fail "rotate api $new again failed: $o"
case $o in *"already pinned at $new"*"nothing changed"*) ;; *) fail "a repeat does not say nothing changed: $o" ;; esac
same_as "$tmp/after" "a repeat"

# Rollback: the old version again gives the committed files back, with a note.
o=$(rot api "$a" 2>&1) || fail "rotate api $a (the rollback) failed: $o"
case $o in *"is older than $new"*) ;; *) fail "a rollback has no note: $o" ;; esac
same_as "$HERE/deploy/run" "a rollback"

# Another service moves only its own two lines.
n=$(pin_of notify.yaml db-password-notify "$HERE/deploy/run")
rot notify "$((n + 4))" >/dev/null 2>&1 || fail "rotate notify failed"
[ "$(changed notify.yaml)" = 1 ] && [ "$(changed migrate.yaml)" = 1 ] && [ "$(changed api.yaml)" = 0 ] ||
  fail "a rotation of notify moved more than its two pins"
[ "$(pin_of migrate.yaml db-password-notify "$run")" = "$((n + 4))" ] && [ "$(pin_of migrate.yaml db-password-api "$run")" = "$a" ] ||
  fail "migrate.yaml: the wrong DB_PASSWORD_ line moved"
checked "a rotation of notify"

# Pins that differed (a hand edit) are both at the new version afterwards, with a note.
fresh
s=$(pin_of scoring.yaml db-password-scoring "$run")
awk -v to="key: \"$((s + 5))\"" 'index($0, "name: db-password-scoring,") { sub(/key: "[0-9]+"/, to) } { print }' "$run/migrate.yaml" > "$tmp/m"
mv "$tmp/m" "$run/migrate.yaml"
o=$(rot scoring "$((s + 1))" 2>&1) || fail "rotate scoring after a hand edit failed: $o"
case $o in *"the two pins differed ($s and $((s + 5)))"*) ;; *) fail "no note about the pins that differed: $o" ;; esac
[ "$(pin_of scoring.yaml db-password-scoring "$run")" = "$((s + 1))" ] && [ "$(pin_of migrate.yaml db-password-scoring "$run")" = "$((s + 1))" ] ||
  fail "the pins that differed are not both at $((s + 1))"
checked "aligned pins"

# Wrong arguments: exit 2, nothing written.
usage() {
  local rc=0
  rot "$@" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "rotate $*: want exit 2, got $rc"
  same_as "$HERE/deploy/run" "rotate $* (a usage error)"
}
fresh
usage
usage api
usage api 2 3
usage web 2
usage owner 2
usage migrate 2
usage API 2
usage api 0
usage api 01
usage api -1
usage api 1.5
usage api latest
usage api ''
usage api 1234567
usage '' 2

# Files it does not recognise: exit 1 with a reason, neither file written, nothing left behind.
broken() { # <what> <text>: after the fixture's edit
  local rc=0 o
  cp -R "$run" "$tmp/broken"
  o=$(rot api "$((a + 1))" 2>&1) || rc=$?
  [ "$rc" = 1 ] || fail "$1: want exit 1, got $rc: $o"
  case $o in *"$2"*) ;; *) fail "$1: the message does not say '$2': $o" ;; esac
  diff -r "$tmp/broken" "$run" >/dev/null || fail "$1: a file was written: $(diff -r "$tmp/broken" "$run" || true)"
  leftovers "$1"
  rm -rf "$tmp/broken"
  fresh
}
edit() { # <file> <awk program>: the copy's file through awk
  awk "$2" "$run/$1" > "$tmp/e"
  mv "$tmp/e" "$run/$1"
}
edit api.yaml 'index($0, "name: db-password-api,") { sub(/key: "[0-9]+"/, "key: \"latest\"") } { print }'
broken "api.yaml at latest" 'has the key "latest"'
edit migrate.yaml 'index($0, "name: db-password-api,") { sub(/key: "[0-9]+"/, "key: \"latest\"") } { print }'
broken "migrate.yaml at latest" 'has the key "latest"'
edit migrate.yaml '!index($0, "name: db-password-api,")'
broken "migrate.yaml without DB_PASSWORD_API" "migrate.yaml names db-password-api on 0 lines"
rm "$run/migrate.yaml"
broken "no migrate.yaml" "migrate.yaml is missing"
edit api.yaml '{ print } index($0, "name: db-password-api,") { print "            # was: name: db-password-api, key \"0\"" }'
broken "a second mention" "api.yaml names db-password-api on 2 lines"
edit api.yaml 'index($0, "name: db-password-api,") { print "            - name: PGPASSWORD"; print "              valueFrom: { secretKeyRef: { name: db-password-api, key: \"1\" } }"; next } { print }'
broken "a pin on two lines" "no line is exactly"
edit migrate.yaml 'index($0, "name: db-password-api,") { sub(/DB_PASSWORD_API/, "PASSWORD_API") } { print }'
broken "another env name" "no line is exactly"

echo "✓ rotate_db_password_test: a rotation moves exactly the two pins (and passes check_cloudrun$($yq_ok || echo ', skipped without yq')), a repeat changes nothing, a rollback restores the files, differing pins align, wrong arguments exit 2 and unknown shapes fail closed"
