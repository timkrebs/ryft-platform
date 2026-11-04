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
for bad in abc "" "  " "12.5%" "1e3" "1234.5" $'70\n80' $'70\n::error::x' "-5"; do
  printf '%s\n' "$bad" > "$tmp/base"
  if "$C" gate "$tmp/cur" "$tmp/base" >/dev/null 2>&1; then fail "malformed baseline '$bad' must exit 2"; fi
done
: > "$tmp/base"
rc=0; "$C" gate "$tmp/cur" "$tmp/base" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "an empty baseline must exit 2, not read as 0 (got $rc)"
echo 70.00 > "$tmp/base"
echo " " > "$tmp/badcur"
rc=0; "$C" gate "$tmp/badcur" "$tmp/base" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "a malformed current total must exit 2 (got $rc)"

# baseline: the run first (newest successful push run of ci.yml on main, same repository), then
# its own artifact; every error or oddity means no baseline and a pass.
if ! command -v jq >/dev/null || ! command -v zip >/dev/null || ! command -v unzip >/dev/null; then
  echo "coverage_test: jq/zip/unzip missing, baseline cases skipped"; echo "✓ coverage_test"; exit 0
fi
mkdir -p "$tmp/bin" "$tmp/zip" "$tmp/zbad"
echo 55.5 > "$tmp/zip/total.txt"
(cd "$tmp/zip" && zip -q ../a.zip total.txt)
printf '::error::pwned\n' > "$tmp/zbad/total.txt"
(cd "$tmp/zbad" && zip -q ../bad.zip total.txt)
# gh api <url> [--jq <filter>]; FAKE_RUNS / FAKE_ARTS are the JSON the two listings return.
cat > "$tmp/bin/gh" <<'EOG'
#!/usr/bin/env bash
echo "$2" >> "$FAKE_LOG"
case "$2" in
  *"/workflows/ci.yml/runs?"*) [ "${FAKE_GH:-ok}" != fail ] || exit 1
    case "$2" in *branch=main*event=push*status=success*) ;; *) echo "bad run query: $2" >&2; exit 1 ;; esac
    jq -r "$4" "$FAKE_RUNS" ;;
  *"/runs/"*"/artifacts?name=coverage") jq -r "$4" "$FAKE_ARTS" ;;
  *"/artifacts/"*"/zip") [ "${FAKE_GH:-ok}" != zipfail ] || exit 1; cat "$FAKE_ZIP" ;;
esac
EOG
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH" GITHUB_REPOSITORY=o/r FAKE_RUNS="$tmp/runs.json" FAKE_ARTS="$tmp/arts.json" FAKE_ZIP="$tmp/a.zip" FAKE_LOG="$tmp/log"
runs() { printf '{"workflow_runs":[{"id":%s,"head_branch":"%s","event":"%s","head_repository":{"id":%s},"repository":{"id":9}}]}\n' "$1" "$2" "$3" "$4" > "$tmp/runs.json"; }
echo '{"artifacts":[{"id":11,"name":"coverage","expired":true},{"id":12,"name":"coverage","expired":false},{"id":13,"name":"coverage","expired":false}]}' > "$tmp/arts.json"
base() { rm -rf "$tmp/b"; : > "$tmp/log"; env -u GITHUB_STEP_SUMMARY "$C" baseline "$tmp/b" 2>&1; }

runs 100 main push 9
out=$(base)
[ "$(cat "$tmp/b/main-total.txt")" = 55.5 ] || fail "baseline value not fetched: $out"
grep -q '/runs/100/artifacts' "$tmp/log" || fail "the artifacts of run 100 were not asked for"
grep -q '/artifacts/12/zip' "$tmp/log" || fail "wanted artifact 12 (first non-expired of the run)"
grep -q 'artifacts?name=coverage&per_page' "$tmp/log" && fail "the repository-wide artifact list must not be used"
grep -q '55.5%' <<<"$out" || fail "the checked value should be printed"
# A fork's run (head repository differs), a non-push event, another branch: no baseline.
for r in "100 main push 7" "100 main pull_request 9" "100 feat/x push 9"; do
  # shellcheck disable=SC2086
  runs $r; out=$(base)
  [ ! -e "$tmp/b/main-total.txt" ] || fail "'$r' must not give a baseline"
  grep -q 'artifacts' "$tmp/log" && fail "'$r': no artifact may be asked for"
  grep -q 'no coverage artifact from main yet' <<<"$out" || fail "'$r' must say so: $out"
  grep -q 'no coverage artifact from main yet' "$tmp/b/baseline-reason" || fail "'$r': reason file missing"
  grep -q '::warning' <<<"$out" && fail "'$r': an empty list is no warning"
done
# No run at all.
echo '{"workflow_runs":[]}' > "$tmp/runs.json"; base >/dev/null
[ ! -e "$tmp/b/main-total.txt" ] || fail "no run, no baseline"
# A run without a (live) artifact.
runs 100 main push 9; echo '{"artifacts":[{"id":11,"name":"coverage","expired":true}]}' > "$tmp/arts.json"
out=$(base); [ ! -e "$tmp/b/main-total.txt" ] || fail "only an expired artifact: no baseline"
grep -q 'no coverage artifact' <<<"$out" || fail "must say so: $out"
echo '{"artifacts":[{"id":12,"name":"coverage","expired":false}]}' > "$tmp/arts.json"
# Errors are not "no baseline yet": a warning, a reason file, and the gate prints that reason.
out=$(FAKE_GH=fail base) || fail "an API error must not fail the run"
[ ! -e "$tmp/b/main-total.txt" ] || fail "no baseline file expected after an error"
grep -q '::warning title=coverage-gate::' <<<"$out" || fail "an API error must warn: $out"
grep -q 'API error' "$tmp/b/baseline-reason" || fail "reason file should name the error"
grep -q 'yet' "$tmp/b/baseline-reason" && fail "an error must not read as 'no baseline yet'"
cp -R "$tmp/b" "$tmp/b-err"; echo 60.00 > "$tmp/cur"
out=$(env -u GITHUB_STEP_SUMMARY "$C" gate "$tmp/cur" "$tmp/b-err/main-total.txt") || fail "no baseline must pass"
grep -q 'API error' <<<"$out" || fail "the gate must print the reason: $out"
# A failed download.
out=$(FAKE_GH=zipfail base) || fail "a failed download must not fail the run"
[ ! -e "$tmp/b/main-total.txt" ] || fail "no baseline after a failed download"
grep -q '::warning' <<<"$out" && grep -q 'download' "$tmp/b/baseline-reason" || fail "a failed download must warn: $out"
# A zip without total.txt.
echo x > "$tmp/zip/other.txt"; (cd "$tmp/zip" && zip -q ../empty.zip other.txt)
out=$(FAKE_ZIP="$tmp/empty.zip" base) || fail "a zip without total.txt must not fail the run"
[ ! -e "$tmp/b/main-total.txt" ] || fail "no baseline from a zip without total.txt"
grep -q '::warning' <<<"$out" || fail "a zip without total.txt must warn: $out"
# A hostile total.txt: dropped, and never echoed.
out=$(FAKE_ZIP="$tmp/bad.zip" base) || fail "a bad total.txt must not fail the run"
[ ! -e "$tmp/b/main-total.txt" ] || fail "a malformed total.txt must be deleted"
grep -q 'pwned' <<<"$out" && fail "raw file content was echoed: $out"
grep -q 'unreadable' "$tmp/b/baseline-reason" || fail "must say unreadable"

# Module path: read from go.mod. A profile of another module has no internal/ statement: exit 2.
mod=$(awk '$1 == "module" { print $2; exit }' "$HERE/backend/go.mod")
[ -n "$mod" ] || fail "no module line in the real backend/go.mod"
printf 'mode: atomic\n%s/internal/z/z.go:1.1,2.2 4 1\n' "$mod" > "$tmp/real.out"
env -u GITHUB_STEP_SUMMARY "$C" report "$tmp/real.out" "$tmp/o4" >/dev/null || fail "a profile of the real module must report"
[ "$(cat "$tmp/o4/total.txt")" = 100.00 ] || fail "real module total"
printf 'mode: atomic\nexample.com/other/internal/z/z.go:1.1,2.2 4 1\n' > "$tmp/drift.out"
rc=0; env -u GITHUB_STEP_SUMMARY "$C" report "$tmp/drift.out" "$tmp/o5" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "module path drift must exit 2 (got $rc)"
printf 'mode: atomic\n%s/cmd/x/main.go:1.1,2.2 4 1\n' "$mod" > "$tmp/nointernal.out"
rc=0; env -u GITHUB_STEP_SUMMARY "$C" report "$tmp/nointernal.out" "$tmp/o6" >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "no internal/ statement must exit 2 (got $rc)"
for m in abc "" "1e2" "-1" "1.0%"; do
  rc=0; MAX_DROP=$m "$C" gate "$tmp/cur" "$tmp/b-err/main-total.txt" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "MAX_DROP='$m' must exit 2 (got $rc)"
done
echo "✓ coverage_test"
