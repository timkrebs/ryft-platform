#!/usr/bin/env bash
# scripts/check_rule_groups.sh: the committed rule files fit Grafana Cloud's 20 rules per group,
# 20 rules pass and 21 fail (alerts and recording rules alike, block and flow style), comments
# and other lists do not count, and a group named twice or a file without groups fails.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
script=$HERE/scripts/check_rule_groups.sh
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_rule_groups_test: $*" >&2; exit 1; }
run() { rc=0; "$script" "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?; }

run "$HERE/deploy/grafana/alerts/ryft.yaml" "$HERE/deploy/grafana/alerts/admission-intended.yaml"
[ "$rc" = 0 ] || fail "the committed rule files must fit (rc=$rc): $(cat "$tmp/err")"

# group <name> <alerts> <records>: a group in the layout of ryft.yaml; every fourth alert is in
# flow style, and each carries a comment that looks like a rule.
group() {
  local i
  printf '  - name: %s\n    rules:\n' "$1"
  for ((i = 1; i <= $2; i++)); do
    if ((i % 4 == 0)); then
      printf '      - { alert: A%s%d, expr: vector(1), labels: { severity: info } }\n' "$1" "$i"
    else
      printf '      # - alert: NotARule\n      - alert: A%s%d\n        expr: vector(1)\n        labels: { severity: info }\n' "$1" "$i"
    fi
  done
  for ((i = 1; i <= $3; i++)); do printf '      - record: r:%s:%d\n        expr: vector(1)\n' "$1" "$i"; done
}
file() { { printf '# fixture\nnamespace: t\ngroups:\n'; cat; } > "$tmp/$1"; }

{ group small 3 0; group full 18 2; } | file ok.yaml
run "$tmp/ok.yaml"; [ "$rc" = 0 ] || fail "groups of 3 and 20 rules must pass (rc=$rc): $(cat "$tmp/err")"
grep -q 'full 20/20' "$tmp/out" || fail "20 rules expected in group full: $(cat "$tmp/out")"
grep -q 'small 3/20' "$tmp/out" || fail "3 rules expected in group small (comments do not count): $(cat "$tmp/out")"

{ group small 3 0; group big 19 2; } | file over.yaml
run "$tmp/over.yaml"; [ "$rc" = 1 ] || fail "a group of 21 rules must fail (rc=$rc)"
grep -q 'group big has 21 rules' "$tmp/err" || fail "the finding names the group and its count: $(cat "$tmp/err")"
run "$tmp/ok.yaml" "$tmp/over.yaml"; [ "$rc" = 1 ] || fail "one bad file among good ones must fail (rc=$rc)"

# 21 alerts and no recording rule fail too, and so does the last group of a file that goes on
# with another top-level key.
{ group last 21 0; printf 'other:\n  - name: x\n    rules:\n      - alert: Y\n'; } | file tail.yaml
run "$tmp/tail.yaml"; [ "$rc" = 1 ] || fail "21 alerts in the last group must fail (rc=$rc)"
grep -q 'group last has 21 rules' "$tmp/err" || fail "the last group is counted: $(cat "$tmp/err")"

# A nested `- name:` (deeper than the groups) is not a group.
{ group outer 20 0; printf '      - alert: Nested\n        expr: vector(1)\n        labels:\n          - name: inner\n'; } | file nested.yaml
run "$tmp/nested.yaml"; [ "$rc" = 1 ] || fail "a nested '- name:' must not start a group (rc=$rc): $(cat "$tmp/out")"
grep -q 'group outer has 21 rules' "$tmp/err" || fail "the nested item stays in group outer: $(cat "$tmp/err")"

# The limit can be set; 20 still passes at the default.
RULE_GROUP_LIMIT=19 "$script" "$tmp/ok.yaml" >/dev/null 2>&1 && fail "RULE_GROUP_LIMIT=19 must refuse 20 rules"
rc=0; RULE_GROUP_LIMIT=x "$script" "$tmp/ok.yaml" >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "a bad RULE_GROUP_LIMIT is a usage error (rc=$rc)"

{ group same 2 0; group same 2 0; } | file dup.yaml
run "$tmp/dup.yaml"; [ "$rc" = 1 ] || fail "a group named twice must fail (rc=$rc)"
grep -q 'more than once: same' "$tmp/err" || fail "the finding names the group: $(cat "$tmp/err")"

printf 'namespace: t\n' > "$tmp/empty.yaml"
run "$tmp/empty.yaml"; [ "$rc" = 2 ] || fail "a file without groups must fail with 2 (rc=$rc)"
run "$tmp/missing.yaml"; [ "$rc" = 2 ] || fail "a missing file must fail with 2 (rc=$rc)"
run; [ "$rc" = 2 ] || fail "no argument is a usage error (rc=$rc)"

echo "✓ check_rule_groups_test"
