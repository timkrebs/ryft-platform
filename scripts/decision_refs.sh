#!/usr/bin/env bash
# Decisions whose code never says so: lists the D-numbers defined in docs/DECISIONS.md that no tracked source or
# config file mentions, so a reader of the code can find the reason for a branch, a constant or a manifest.
#   scripts/decision_refs.sh            warn about each unreferenced D<n>; exit 0 (`make lint`, CI `bytes`)
#   scripts/decision_refs.sh --strict   the same, exit 1 when there is a warning (for a later promotion to a failure)
# Mentions come from `git ls-files` (so worktrees, node_modules and build output are out), minus markdown, docs/,
# test data, lock files, generated outputs, vendored operator releases and web/ (the site has its own
# web/docs/DECISIONS.md with its own D1..Dn, so a D<n> there is not this file's). A range counts for each member:
# D74-D77, D74-77 and D74<en dash>D77.
# scripts/decision_refs.skip holds the decisions without a code consequence, one `D<n> reason` per line; a reason is
# required, a skipped decision that gets mentioned is reported as stale, and `#` lines and blank lines are ignored.
# Exit 0: only warnings (or none). 1: --strict and there is a warning. 2: it cannot work: no DECISIONS.md, not a git
# checkout, bad usage, a skip line without a reason or for a decision with no entry. A broken check must not read as
# "all linked".
# Under GITHUB_ACTIONS one ::warning annotation carries the count; the per-decision lines stay in the log.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
decisions=$root/docs/DECISIONS.md
skipfile=$root/scripts/decision_refs.skip

die() { echo "decision_refs: $*" >&2; exit 2; }

strict=0
case "${1:-}" in
  "") ;;
  --strict) strict=1 ;;
  *) die "usage: scripts/decision_refs.sh [--strict]" ;;
esac
[ $# -le 1 ] || die "usage: scripts/decision_refs.sh [--strict]"

[ -f "$decisions" ] || die "$decisions does not exist"
cd "$root"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "$root is not a git checkout"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# <n> <heading> per defined decision, in number order.
sed -n 's/^## D\([0-9][0-9]*\) *[—–-]* *\(.*\)$/\1 \2/p' "$decisions" | sort -n -k1,1 > "$tmp/defined"
[ -s "$tmp/defined" ] || die "no '## D<n>' heading in $decisions"

# <n> <reason> per skipped decision.
: > "$tmp/skipped"
if [ -f "$skipfile" ]; then
  lineno=0
  while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    case "$line" in ''|'#'*) continue ;; esac
    if ! [[ $line =~ ^D([0-9]+)[[:space:]]+([^[:space:]].*)$ ]]; then
      die "$skipfile:$lineno: expected 'D<n> <reason>': $line"
    fi
    n=${BASH_REMATCH[1]}
    awk -v n="$n" '$1 == n { found = 1 } END { exit !found }' "$tmp/defined" || die "$skipfile:$lineno: D$n has no entry in docs/DECISIONS.md"
    printf '%s %s\n' "$n" "${BASH_REMATCH[2]}" >> "$tmp/skipped"
  done < "$skipfile"
fi

# The files that may mention a decision: tracked, text, not documentation, test data, a lock file or generated.
git ls-files -z \
  | grep -zvE '(\.md$|^docs/|/testdata/|(^|/)package-lock\.json$|(^|/)go\.sum$|\.terraform\.lock\.hcl$|^web/|^deploy/rabbitmq/operators/(SHA256SUMS|.*/(cluster-operator|messaging-topology-operator)-[0-9.]+\.ya?ml)$|^deploy/rabbitmq/topology/|^deploy/k8s/overlays/vault-namespaces/|^scripts/decision_refs\.(sh|skip)$|^scripts/tests/decision_refs_test\.sh$)' \
  > "$tmp/candidates" || true
: > "$tmp/mentions"
if [ -s "$tmp/candidates" ]; then
  xargs -0 grep -Il --null -E 'D[0-9]' < "$tmp/candidates" > "$tmp/texts" 2>/dev/null || true
fi
# One awk pass over all of them: "<n> <file>" per mention. A mention is D<digits> that does not continue a word
# (not a hash, a colour or HD5), optionally followed by - or an en dash and a second number (with or without D).
if [ -s "$tmp/texts" ]; then
  # shellcheck disable=SC2016  # the awk program is not shell
  xargs -0 awk '
    function wordchar(c) { return c ~ /[A-Za-z0-9_]/ }
    {
      s = $0
      while (match(s, /D[0-9]+/)) {
        pre = (RSTART > 1) ? substr(s, RSTART - 1, 1) : ""
        lo = substr(s, RSTART + 1, RLENGTH - 1) + 0
        rest = substr(s, RSTART + RLENGTH)
        s = rest
        if (wordchar(pre)) continue
        hi = lo
        if (match(rest, /^(-|–)D?[0-9]+/)) {
          tail = substr(rest, RSTART, RLENGTH)
          sub(/^(-|–)D?/, "", tail)
          hi = tail + 0
          s = substr(rest, RLENGTH + 1)
        } else if (rest ~ /^[A-Za-z0-9_]/) continue
        if (hi < lo || hi - lo > 200) hi = lo
        for (k = lo; k <= hi; k++) print k, FILENAME
      }
    }' < "$tmp/texts" | sort -u > "$tmp/mentions"
fi

warnings=0
unlinked=0
while read -r n heading; do
  skip=$(awk -v n="$n" '$1 == n { sub(/^[^ ]+ /, ""); print; exit }' "$tmp/skipped")
  where=$(awk -v n="$n" '$1 == n { print $2; exit }' "$tmp/mentions")
  if [ -n "$skip" ]; then
    if [ -n "$where" ]; then
      echo "warning: D$n is in scripts/decision_refs.skip ($skip) but is mentioned in $where; drop it from the skip list"
      warnings=$((warnings + 1))
    fi
  elif [ -z "$where" ]; then
    echo "warning: D$n ($heading) has no reference in code or config; link it where it explains a branch, or add it to scripts/decision_refs.skip with a reason"
    warnings=$((warnings + 1))
    unlinked=$((unlinked + 1))
  fi
done < "$tmp/defined"

total=$(wc -l < "$tmp/defined" | tr -d ' ')
skipped=$(wc -l < "$tmp/skipped" | tr -d ' ')
echo "decision_refs: $unlinked of $total decisions have no reference in code or config ($skipped skipped)"
if [ "$warnings" -gt 0 ] && [ "${GITHUB_ACTIONS:-}" = "true" ]; then
  echo "::warning title=decision references::$warnings decision reference warning(s), $unlinked unreferenced D-numbers of $total; run scripts/decision_refs.sh for the list"
fi
if [ "$strict" = 1 ] && [ "$warnings" -gt 0 ]; then exit 1; fi
exit 0
