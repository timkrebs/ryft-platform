#!/usr/bin/env bash
# scripts/golden_guard.sh: a golden user file changes only with a ModelVersion bump, its input
# digest or the field digest (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ golden_guard_test: $*" >&2; exit 1; }
d=backend/internal/scoring/testdata/golden
r=$tmp/repo  # the report goes to $tmp/out, outside the repository the checkouts switch
mkdir -p "$r/scripts" "$r/$d"; cp "$HERE/scripts/golden_guard.sh" "$r/scripts/"
cd "$r"; git init -q -b main; git config user.email t@example.com; git config user.name test
version() { printf 'package scoring\n\nconst ModelVersion = "%s"\n' "$1" > backend/internal/scoring/version.go; }
# manifest <digest-a> <digest-b> [fields]
manifest() { printf '{"fields":"%s","inputs":{"user-a":{"digest":"%s"},"user-b":{"digest":"%s"}},"model_version":"x"}\n' "${3:-10}" "$1" "$2" > $d/manifest.json; }
check() { ./scripts/golden_guard.sh origin/main >"$tmp/out" 2>&1; }
ok()  { check || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { check && fail "should fail: $1 ($(cat "$tmp/out"))"; true; }
commit() { git add -A; git commit -qm "$1"; }
# base <message>: the state on main, the PR branch from it.
base() { commit "$1"; git update-ref refs/remotes/origin/main HEAD; }
reset() { git checkout -q -B pr origin/main; }

version 2026.10.3; manifest 1 2; echo '{"recovery": 64}' > $d/user-a.json; echo '{"recovery": 50}' > $d/user-b.json
base "golden files"

reset; echo 'x' > README; commit "no golden change"; ok "nothing golden changed"
reset; echo '{"recovery": 61}' > $d/user-a.json; commit "a moved value"; bad "a value moved, nothing else did"
grep -q 'user-a.json changed' "$tmp/out" || fail "the error names the file: $(cat "$tmp/out")"
reset; echo '{"recovery": 61}' > $d/user-a.json; version 2026.10.4; commit "bump"; ok "ModelVersion bumped"
reset; echo '{"recovery": 61}' > $d/user-a.json; manifest 3 2; commit "generator"; ok "user-a's inputs changed"
reset; echo '{"recovery": 49}' > $d/user-b.json; manifest 3 2; commit "generator, wrong user"; bad "user-a's inputs changed, user-b's file moved"
reset; echo '{"recovery": 64, "new": 1}' > $d/user-a.json; manifest 1 2 11; commit "additive"; ok "the field digest changed (a new column)"
reset; echo '{"recovery": 1}' > $d/user-c.json; manifest 1 2; commit "a file without a manifest entry"; bad "a new user the manifest does not list"
reset; echo '{"recovery": 1}' > $d/user-c.json
jq -c '.inputs["user-c"] = {"digest": "7"}' $d/manifest.json > $d/m && mv $d/m $d/manifest.json; commit "a new user"; ok "a new user with its digest"
reset; git rm -q $d/user-b.json; jq -c 'del(.inputs["user-b"])' $d/manifest.json > $d/m && mv $d/m $d/manifest.json; commit "user-b gone"; ok "a user removed with its entry"
reset; echo '{"recovery": 61}' > $d/user-a.json; echo 'not json' > $d/manifest.json; commit "broken manifest"; bad "an unreadable manifest"
# The merge base, not origin/main's tip: a bump merged to main after the branch was cut does not count.
reset; echo '{"recovery": 61}' > $d/user-a.json; commit "moved on a branch"
git checkout -q main; version 2026.10.5; commit "a bump on main"; git update-ref refs/remotes/origin/main HEAD; git checkout -q pr
bad "the bump is on main, not in the change"
echo "✓ golden_guard_test: a golden file moves only with a bump, its digest or new fields"
