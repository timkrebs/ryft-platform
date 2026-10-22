#!/usr/bin/env bash
# scripts/check_privacy_versions.sh: the list of policy versions only grows (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_privacy_versions_test: $*" >&2; exit 1; }
mkdir -p "$tmp/scripts" "$tmp/web/content/legal"; cp "$HERE/scripts/check_privacy_versions.sh" "$tmp/scripts/"
cd "$tmp"; git init -q -b main; git config user.email t@example.com; git config user.name test
f=web/content/legal/privacy-versions.json
check() { ./scripts/check_privacy_versions.sh origin/main >"$tmp/out" 2>&1; }
ok()  { check || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { check && fail "should fail: $1"; true; }
git add -A; git commit -qm "no list yet"; git update-ref refs/remotes/origin/main HEAD
echo '["2026-10-20"]' > $f; ok "a list the base does not have yet"
git add -A; git commit -qm "list"; git update-ref refs/remotes/origin/main HEAD
ok "unchanged"
echo '["2026-10-20","2026-11-02"]' > $f; ok "a version appended"
echo '["2026-10-20","2026-11-02","2026-12-01"]' > $f; ok "two appended"
echo '["2026-10-19","2026-10-20"]' > $f; bad "a version inserted before"
echo '["2026-11-02"]' > $f; bad "the old version replaced"
echo '[]' > $f; bad "the list emptied"
echo '["2026-10-21"]' > $f; bad "the old version changed"
echo 'not json' > $f; bad "unreadable"
echo "✓ check_privacy_versions_test: append-only"
