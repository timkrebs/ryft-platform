#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2016
# scripts/coverage.sh on a hand-made profile and a fake `gh` (make test-scripts): per-package
# numbers, the internal/ total (cmd/ is not in it), the gate at the 1 point line, a missing baseline.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ coverage_test: $*" >&2; exit 1; }
C="$HERE/scripts/coverage.sh"
M=github.com/timkrebs/ryft

# internal/a: 3 of 4 statements (the duplicate block counts once, covered by either run);
# internal/b/sub: 0 of 6; cmd/x: 10 of 10, outside the internal/ total.
cat > "$tmp/p.out" <<EOP
mode: atomic
$M/internal/a/a.go:1.1,2.2 2 1
$M/internal/a/a.go:3.1,4.2 1 0
$M/internal/a/a.go:3.1,4.2 1 5
$M/internal/a/a.go:5.1,6.2 1 0
$M/internal/b/sub/b.go:1.1,2.2 6 0
$M/cmd/x/main.go:1.1,2.2 10 3
EOP
out=$(env -u GITHUB_STEP_SUMMARY "$C" report "$tmp/p.out" "$tmp/o") || fail "report failed"
grep -qxF '| internal/a | 75.0% | 4 |' <<<"$out" || fail "internal/a row wrong: $out"
grep -qxF '| internal/b/sub | 0.0% | 6 |' <<<"$out" || fail "internal/b/sub row wrong"
grep -qxF '| cmd/x | 100.0% | 10 |' <<<"$out" || fail "cmd/x row wrong"
[ "$(cat "$tmp/o/total.txt")" = 30.00 ] || fail "internal total should be 3/10 = 30.00, got $(cat "$tmp/o/total.txt")"
grep -q 'internal/ total: 30.00%' <<<"$out" || fail "total line missing"
# With GITHUB_STEP_SUMMARY set the table goes there, not to stdout.
: > "$tmp/sum"
[ -z "$(GITHUB_STEP_SUMMARY=$tmp/sum "$C" report "$tmp/p.out" "$tmp/o2")" ] || fail "table leaked to stdout"
grep -q '^### Go coverage' "$tmp/sum" || fail "table not in the summary"
if "$C" report "$tmp/none.out" "$tmp/o3" 2>/dev/null; then fail "a missing profile must fail"; fi

# gate (the summary is a file of the test, never the run's own)
echo 70.00 > "$tmp/base"
echo 69.00 > "$tmp/cur"
"$C" gate "$tmp/cur" "$tmp/base" >/dev/null || fail "exactly 1.0 point lower must pass"
echo 68.99 > "$tmp/cur"
if "$C" gate "$tmp/cur" "$tmp/base" >/dev/null 2>&1; then fail "more than 1.0 lower must fail"; fi
echo 90.00 > "$tmp/cur"
"$C" gate "$tmp/cur" "$tmp/base" >/dev/null || fail "higher must pass"
if MAX_DROP=5 "$C" gate "$tmp/o/total.txt" "$tmp/base" >/dev/null 2>&1; then fail "40 points lower must fail even with MAX_DROP=5"; fi
out=$(env -u GITHUB_STEP_SUMMARY "$C" gate "$tmp/cur" "$tmp/none"); grep -q "no baseline" <<<"$out" || fail "no baseline must pass and say so"
echo abc > "$tmp/base"
if "$C" gate "$tmp/cur" "$tmp/base" >/dev/null 2>&1; then fail "unreadable baseline must fail"; fi

# baseline: newest main artifact wins; PR, forked and expired ones are ignored; errors mean no baseline.
if ! command -v jq >/dev/null || ! command -v zip >/dev/null || ! command -v unzip >/dev/null; then
  echo "coverage_test: jq/zip/unzip missing, baseline cases skipped"; echo "✓ coverage_test"; exit 0
fi
mkdir -p "$tmp/bin" "$tmp/zip"
echo 55.5 > "$tmp/zip/total.txt"
(cd "$tmp/zip" && zip -q ../a.zip total.txt)
cat > "$tmp/bin/gh" <<'EOG'
#!/usr/bin/env bash
# gh api <url> [--jq <filter>]
case "$2" in
  *"/artifacts?name=coverage"*) [ "${FAKE_GH:-ok}" != fail ] || exit 1
    jq -r "$4" "$FAKE_LIST" ;;
  *"/artifacts/"*"/zip") echo "$2" > "$FAKE_ASKED"; cat "$FAKE_ZIP" ;;
esac
EOG
chmod +x "$tmp/bin/gh"
cat > "$tmp/list.json" <<'EOJ'
{"artifacts":[
 {"id":1,"expired":false,"created_at":"2026-11-01T00:00:00Z","workflow_run":{"head_branch":"main","head_repository_id":9,"repository_id":9}},
 {"id":2,"expired":false,"created_at":"2026-11-03T00:00:00Z","workflow_run":{"head_branch":"main","head_repository_id":9,"repository_id":9}},
 {"id":3,"expired":false,"created_at":"2026-11-04T00:00:00Z","workflow_run":{"head_branch":"feat/x","head_repository_id":9,"repository_id":9}},
 {"id":4,"expired":false,"created_at":"2026-11-04T01:00:00Z","workflow_run":{"head_branch":"main","head_repository_id":7,"repository_id":9}},
 {"id":5,"expired":true,"created_at":"2026-11-04T02:00:00Z","workflow_run":{"head_branch":"main","head_repository_id":9,"repository_id":9}}]}
EOJ
export PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_LIST="$tmp/list.json" FAKE_ZIP="$tmp/a.zip" FAKE_ASKED="$tmp/asked"
"$C" baseline "$tmp/b1" >/dev/null || fail "baseline failed"
[ "$(cat "$tmp/b1/main-total.txt")" = 55.5 ] || fail "baseline value not fetched"
grep -q '/artifacts/2/zip' "$tmp/asked" || fail "wanted artifact 2 (newest, from main, same repo), asked $(cat "$tmp/asked")"
echo '{"artifacts":[]}' > "$tmp/list.json"
out=$(env -u GITHUB_STEP_SUMMARY "$C" baseline "$tmp/b2"); grep -q "no coverage artifact" <<<"$out" || fail "empty list must say so"
[ ! -e "$tmp/b2/main-total.txt" ] || fail "no baseline file expected"
FAKE_GH=fail "$C" baseline "$tmp/b3" >/dev/null 2>&1 || fail "an API error must not fail the run (no baseline)"
[ ! -e "$tmp/b3/main-total.txt" ] || fail "no baseline file expected after an error"
echo "✓ coverage_test"
