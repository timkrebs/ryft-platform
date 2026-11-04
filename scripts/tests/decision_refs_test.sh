#!/usr/bin/env bash
# shellcheck disable=SC2154
# scripts/decision_refs.sh in a throwaway repository: a mention, a missing one, ranges (hyphen and en dash), the skip
# list, a stale skip, files that do not count (markdown, docs/, testdata, untracked, a longer word), --strict, the
# GitHub annotation, and the cases where the script itself cannot work (exit 2).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
unset GITHUB_ACTIONS  # CI sets it; only the annotation case below turns it on
fail() { echo "✗ decision_refs_test: $*" >&2; exit 1; }

repo="$tmp/repo"
mkdir -p "$repo/scripts" "$repo/docs" "$repo/backend/internal/x/testdata" "$repo/deploy"
cp "$HERE/scripts/decision_refs.sh" "$repo/scripts/"
cd "$repo"
git init -q -b main; git config user.email t@example.com; git config user.name test
{
  for n in $(seq 1 18); do printf '## D%s — Decision %s\n\nBody mentioning D%s.\n\n' "$n" "$n" "$n"; done
} > docs/DECISIONS.md
printf '// D1: linked.\n// D3–D5 and D6-D7 are ranges (en dash, hyphen); D8 stands alone.\npackage x\n' > backend/internal/x/x.go
printf '// D17-18 has no second D.\npackage x\n' > backend/internal/x/z.go
printf 'a: 1 # D10 is skipped but mentioned (stale)\nb: HD12 D120 ID13 #D1D1D1\n' > deploy/a.yaml
printf 'D11 in markdown\n' > deploy/README.md
printf 'D14 in testdata\n' > backend/internal/x/testdata/t.txt
printf 'D15 in a docs file\n' > docs/other.txt
printf 'D2,D16;\n' > deploy/untracked.txt  # not added: untracked files are not source
cat > scripts/decision_refs.skip <<'EOF'
# a comment, then a blank line

D9 range member mentioned? no: skipped for a reason
D10 skipped yet mentioned in deploy/a.yaml
EOF
git add -A -- . ':!deploy/untracked.txt'; git commit -qm first
run() { rc=0; ./scripts/decision_refs.sh "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?; }
has() { grep -q -- "$1" "$tmp/out" || fail "$2: missing '$1' in: $(cat "$tmp/out") $(cat "$tmp/err")"; }
hasnt() { ! grep -q -- "$1" "$tmp/out" || fail "$2: unexpected '$1' in: $(cat "$tmp/out")"; }

run; [ "$rc" = 0 ] || fail "warnings alone exit 0 (rc=$rc): $(cat "$tmp/err")"
has 'warning: D2 (Decision 2) has no reference' "D2 is mentioned nowhere"
has 'warning: D11 (Decision 11) has no reference' "markdown does not count"
has 'warning: D12 (Decision 12) has no reference' "HD12 and D120 are not D12"
has 'warning: D13 (Decision 13) has no reference' "ID13 is not D13"
has 'warning: D14 (Decision 14) has no reference' "testdata does not count"
has 'warning: D15 (Decision 15) has no reference' "docs/ does not count"
has 'warning: D16 (Decision 16) has no reference' "untracked files do not count"
has 'warning: D10 is in scripts/decision_refs.skip .* but is mentioned in deploy/a.yaml' "a stale skip"
for n in 1 3 4 5 6 7 8 17 18; do hasnt "warning: D$n " "D$n is mentioned (plain or in a range)"; done
hasnt 'warning: D9 ' "D9 is skipped and unmentioned: quiet"
has 'decision_refs: 7 of 18 decisions have no reference in code or config (2 skipped)' "summary"
hasnt '::warning' "no annotation outside GitHub Actions"

# Same output on a second run; the order is by number.
cp "$tmp/out" "$tmp/first"; run; cmp -s "$tmp/first" "$tmp/out" || fail "output is not deterministic"
grep -o 'warning: D[0-9]*' "$tmp/out" | sed 's/warning: D//' | sort -n -c || fail "not in number order"

run --strict; [ "$rc" = 1 ] || fail "--strict with warnings exits 1 (rc=$rc)"
GITHUB_ACTIONS=true run; [ "$rc" = 0 ] || fail "GITHUB_ACTIONS keeps exit 0"
[ "$(grep -c '^::warning' "$tmp/out")" = 1 ] || fail "exactly one annotation: $(cat "$tmp/out")"
has '^::warning title=decision references::' "annotation shape"

# Everything linked or skipped: quiet, exit 0, also with --strict.
{ for n in 2 9 11 12 13 14 15 16; do echo "// D$n"; done; } > backend/internal/x/y.go
printf '# nothing skipped\n' > scripts/decision_refs.skip
git add -A -- . ':!deploy/untracked.txt'; git commit -qm second
run --strict; [ "$rc" = 0 ] || fail "all linked: --strict exits 0 (rc=$rc): $(cat "$tmp/out")"
has '0 of 18 decisions' "all linked"
hasnt 'warning' "all linked: no warning"

# The script cannot work: exit 2, never "all linked".
printf 'D2\n' > scripts/decision_refs.skip; run; [ "$rc" = 2 ] || fail "a skip line without a reason must exit 2 (rc=$rc)"
grep -q "expected 'D<n> <reason>'" "$tmp/err" || fail "reasonless message: $(cat "$tmp/err")"
printf 'D99 no such decision\n' > scripts/decision_refs.skip; run; [ "$rc" = 2 ] || fail "a skip for an unknown D must exit 2 (rc=$rc)"
grep -q 'D99 has no entry' "$tmp/err" || fail "unknown D message: $(cat "$tmp/err")"
printf 'oops\n' > scripts/decision_refs.skip; run; [ "$rc" = 2 ] || fail "a malformed skip line must exit 2"
printf '# empty\n' > scripts/decision_refs.skip
run --bogus; [ "$rc" = 2 ] || fail "bad usage must exit 2"
mv docs/DECISIONS.md docs/DECISIONS.md.off; run; [ "$rc" = 2 ] || fail "no DECISIONS.md must exit 2"; mv docs/DECISIONS.md.off docs/DECISIONS.md
printf '# nothing here\n' > docs/DECISIONS.md; run; [ "$rc" = 2 ] || fail "no decision heading must exit 2"
git checkout -q -- docs/DECISIONS.md
mkdir -p "$tmp/plain/scripts" "$tmp/plain/docs"; cp scripts/decision_refs.sh "$tmp/plain/scripts/"; cp docs/DECISIONS.md "$tmp/plain/docs/"
rc=0; GIT_CEILING_DIRECTORIES="$tmp" "$tmp/plain/scripts/decision_refs.sh" >/dev/null 2>"$tmp/err" || rc=$?
[ "$rc" = 2 ] || fail "outside a git checkout must exit 2 (rc=$rc)"

# The real tree: the script works on it. Its warnings are not a test failure (the check is warning-only for now).
rc=0; "$HERE/scripts/decision_refs.sh" >"$tmp/out" 2>&1 || rc=$?
[ "$rc" = 0 ] || fail "the real tree must run (rc=$rc): $(cat "$tmp/out")"
grep -q '^decision_refs: [0-9]* of [0-9]* decisions' "$tmp/out" || fail "the real tree prints its summary: $(cat "$tmp/out")"
echo "✓ decision_refs_test"
