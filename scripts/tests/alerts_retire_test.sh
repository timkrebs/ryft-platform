#!/usr/bin/env bash
# make alerts-retire-eks deletes exactly the rule groups the move to Cloud Run left live from EKS days:
# the groups of the two files loaded there that nothing loads now (eks-admission.yaml holds what was
# the old rules file's ryft-admission; admission-intended.yaml is its own namespace), plus ryft-vault,
# which left that file. And the Makefile loads no rule at all: RYFT on Cloud Run alerts from Cloud
# Monitoring (infra/gcp/alerts.tf, D236). Read from the files, no tools beyond sed and grep.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
A=$HERE/deploy/grafana/alerts
fail() { echo "✗ alerts_retire_test: $*" >&2; exit 1; }
# groups <file> [<namespace>]: namespace/group for each group of a rule file (the layout every file here
# has), in the file's own namespace unless one is given.
groups() {
  local ns=${2:-} g
  [ -n "$ns" ] || ns=$(sed -n 's/^namespace:[[:space:]]*//p' "$1")
  [ -n "$ns" ] || fail "$1 has no namespace: line"
  while read -r g; do printf '%s/%s\n' "$ns" "$g"; done < <(sed -n 's/^  - name:[[:space:]]*//p' "$1")
}
flat() { tr '\n' ' ' <<<"$1" | sed 's/ *$//'; }

# The live names are where the groups were loaded on EKS: eks-admission.yaml's groups were the old
# rules file's, in namespace ryft, whatever namespace the file names now (mimirtool checks it in a run
# of its own); admission-intended.yaml was loaded with its own.
live=ryft
want=$({ groups "$A/eks-admission.yaml" "$live"; groups "$A/admission-intended.yaml"; echo "$live/ryft-vault"; } | sort -u)
have=$(sed -n 's/^EKS_RULE_GROUPS[[:space:]]*:=[[:space:]]*//p' "$HERE/Makefile" | tr -s ' ' '\n' | sed '/^$/d' | sort -u)
[ "$(grep -c . <<<"$want")" = 3 ] || fail "fixture: want three leftover groups, the files give [$(flat "$want")]"
[ "$have" = "$want" ] || fail "EKS_RULE_GROUPS in the Makefile is [$(flat "$have")], want [$(flat "$want")]"

# The Cloud Run rules went with Grafana Cloud (D236): no file of theirs, no target that loads one.
[ ! -e "$A/ryft.yaml" ] || fail "deploy/grafana/alerts/ryft.yaml is back: Cloud Run's alerts are infra/gcp/alerts.tf (D236)"
! grep -q 'rules load' "$HERE/Makefile" || fail "the Makefile loads rules again: $(grep -o 'rules load [^;]*' "$HERE/Makefile")"
! grep -qE '^alerts-apply:' "$HERE/Makefile" || fail "alerts-apply is back (D236)"

echo "✓ alerts_retire_test: alerts-retire-eks deletes exactly $(flat "$have"), and nothing loads a rule"
