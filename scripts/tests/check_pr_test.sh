#!/usr/bin/env bash
# scripts/check_pr.sh (CI's pr-title job): the generic "area: what changed" rule and the
# brand check on the title and on the commit messages of a PR (make test-scripts).
# The denylisted brand is never written down here: the test builds a string the denylist
# matches from web/config/brand-denylist.json at run time.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_pr_test: $*" >&2; exit 1; }
command -v node >/dev/null || fail "node is needed (web/scripts/check-brand.mjs)"

# The first hard rule, with each group and class replaced by its first alternative.
brand=$(node --input-type=module - "$HERE/web/config/brand-denylist.json" <<'JS'
import { readFileSync } from 'node:fs';
const rule = JSON.parse(readFileSync(process.argv[2], 'utf8')).hard[0];
// First alternative of every group, first character of every class, escapes unescaped.
function parse(p, i) {
  const alts = [[]];
  while (i < p.length && p[i] !== ')') {
    const c = p[i];
    if (c === '|') { alts.push([]); i++; }
    else if (c === '\\') { alts.at(-1).push(p[i + 1]); i += 2; }
    else if (c === '[') {
      i++; let first = null;
      while (p[i] !== ']') { if (p[i] === '\\') i++; if (first === null) first = p[i]; i++; }
      i++; alts.at(-1).push(first);
    } else if (c === '(') {
      i++; if (p.startsWith('?:', i)) i += 2;
      const [sub, j] = parse(p, i); alts.at(-1).push(sub[0].join('')); i = j + 1;
    } else { alts.at(-1).push(c); i++; }
  }
  return [alts, i];
}
const gen = (p) => parse(p, 0)[0][0].join('');
const s = gen(rule.pattern);
if (!new RegExp(rule.pattern, `g${rule.flags ?? 'iu'}`).test(s)) { console.error('could not build a matching string'); process.exit(1); }
process.stdout.write(s);
JS
) || fail "could not build a denylisted string from the denylist"

# A throwaway repository holding the script and the brand check.
mkdir -p "$tmp/r/scripts" "$tmp/r/web"
cp "$HERE/scripts/check_pr.sh" "$tmp/r/scripts/"; cp -R "$HERE/web/scripts" "$HERE/web/config" "$tmp/r/web/"
cd "$tmp/r"
git init -q -b main && git config user.email t@example.com && git config user.name test
git add -A && git commit -qm "base" && base=$(git rev-parse HEAD)
check() { ./scripts/check_pr.sh "$@" >"$tmp/out" 2>&1; }
ok()  { check "$@" || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { check "$@" && fail "should fail: $1"; true; }

# The title: any lower-case area, path segments, a scope; "what changed" of at least three characters.
for t in "web: fix the footer" "deploy: images abc1234" "infra/aws/kubernetes: tune the alb" "infra/vault: rotate" \
         "events: retry tiers" "scoring: a threshold" "ops: runbook" "ci(deps): bump actions" "docs: web lives in plattform/web"; do ok "$t"; done
bad "Fix stuff"; bad "web:no space"; bad "Web: capital area"; bad "web: "; bad "web: x"; bad "feat!: no"; bad ""
grep -q '::error' "$tmp/out" || true

# The brand in the title
bad "web: a note on $brand support"
bad "web: $(printf '%s' "$brand" | tr a-z A-Z) in capitals"

# The brand in a commit message of the PR, in any commit of the range
echo a > f; git add f; git commit -qm "web: clean first"
echo b > f; git add f; git commit -qm "web: tidy" -m "context about $brand compatibility"
echo c > f; git add f; git commit -qm "web: clean last"; head=$(git rev-parse HEAD)
bad "web: clean title" "$base" "$head"
grep -q 'names the denylisted brand' "$tmp/out" || fail "the failing commit is not named: $(cat "$tmp/out")"
if grep -qi "$brand" "$tmp/out"; then fail "the output must not repeat the brand"; fi
# a range without that commit passes
ok "web: clean title" "$(git rev-parse HEAD)" "$head"
git checkout -q -b clean "$base"; echo d > g; git add g; git commit -qm "web: only clean commits"
ok "web: clean title" "$base" "$(git rev-parse HEAD)"

echo "✓ check_pr_test: generic title rule, brand in the title and in every commit message of the range"
