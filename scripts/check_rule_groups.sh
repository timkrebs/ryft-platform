#!/usr/bin/env bash
# The size of every rule group in Prometheus rule files, against Grafana Cloud's ruler limit.
#
#   scripts/check_rule_groups.sh <rules.yaml>...    (make alerts-check runs it on both rule files)
#
# Grafana Cloud refuses a whole `mimirtool rules load` when one group holds more than 20 rules
# (alerts and recording rules count alike):
#   per-user rules per rule group limit (limit: 20 actual: 39) exceeded
# promtool and `mimirtool rules check` do not know the stack's limits, so this runs offline
# before them. RULE_GROUP_LIMIT overrides the limit (default 20). A group name used twice in one
# file fails as well (the second would replace the first on load). Exit 0 all groups fit, 1 a
# group is too large or named twice, 2 a file cannot be read or has no groups.
set -euo pipefail
limit=${RULE_GROUP_LIMIT:-20}
case $limit in '' | *[!0-9]*) echo "check_rule_groups: RULE_GROUP_LIMIT must be a number, not '$limit'" >&2; exit 2 ;; esac
[ "$#" -gt 0 ] || { echo "usage: scripts/check_rule_groups.sh <rules.yaml>..." >&2; exit 2; }

rc=0
for f in "$@"; do
  [ -r "$f" ] || { echo "check_rule_groups: cannot read $f" >&2; exit 2; }
  # One line per group: "<name>\t<rules>". A group is a `- name:` item at the indentation of the
  # first one under `groups:`; a rule is a `- alert:` or `- record:` item (block or flow style)
  # below it. Comments never count; a key at column 0 ends the list.
  out=$(awk '
    function emit() { if (name != "") printf "%s\t%d\n", name, n }
    /^groups:/ { ing = 1; next }
    !ing { next }
    /^[^ #]/ { emit(); name = ""; ing = 0; next }
    /^[ ]*#/ { next }
    {
      line = $0
      if (match(line, /^ *- name:/)) {
        ind = RLENGTH - length("- name:")
        if (gind == "" || ind == gind) {
          gind = ind; emit()
          name = substr(line, RLENGTH + 1)
          sub(/[ ]+#.*$/, "", name); gsub(/^[ ]+|[ ]+$/, "", name); gsub(/^["\047]|["\047]$/, "", name)
          n = 0; next
        }
      }
      if (name != "" && line ~ /^ *- *(\{ *)?(alert|record):/) n++
    }
    END { emit() }
  ' "$f")
  [ -n "$out" ] || { echo "check_rule_groups: $f has no rule groups under groups:" >&2; exit 2; }
  dups=$(printf '%s\n' "$out" | cut -f1 | sort | uniq -d)
  if [ -n "$dups" ]; then
    echo "check_rule_groups: $f names a group more than once: $(echo "$dups" | tr '\n' ' ')" >&2; rc=1
  fi
  while IFS=$'\t' read -r g n; do
    if [ "$n" -gt "$limit" ]; then
      echo "check_rule_groups: $f group $g has $n rules, Grafana Cloud takes at most $limit per group: split it by theme (OBSERVABILITY.md → Alerts)" >&2
      rc=1
    else
      echo "  $f: $g $n/$limit"
    fi
  done <<< "$out"
done
exit "$rc"
