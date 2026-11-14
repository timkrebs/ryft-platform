#!/usr/bin/env bash
# make alerts-retire-eks deletes exactly the rule groups the move to Cloud Run left live from EKS days:
# the groups of the two files alerts-apply loaded there and no longer loads (eks-admission.yaml holds
# what was ryft.yaml's ryft-admission; admission-intended.yaml is its own namespace), plus ryft-vault,
# which left ryft.yaml; and none of the groups ryft.yaml holds now, the only file alerts-apply loads
# (their load replaces them). Read from the files, no tools beyond sed and grep.
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

# The live names are where alerts-apply loaded the groups on EKS: eks-admission.yaml's groups were
# ryft.yaml's, in namespace ryft, whatever namespace the file names now (mimirtool checks it in a run
# of its own); admission-intended.yaml was loaded with its own.
live=ryft
want=$({ groups "$A/eks-admission.yaml" "$live"; groups "$A/admission-intended.yaml"; echo "$live/ryft-vault"; } | sort -u)
have=$(sed -n 's/^EKS_RULE_GROUPS[[:space:]]*:=[[:space:]]*//p' "$HERE/Makefile" | tr -s ' ' '\n' | sed '/^$/d' | sort -u)
[ "$(grep -c . <<<"$want")" = 3 ] || fail "fixture: want three leftover groups, the files give [$(flat "$want")]"
[ "$have" = "$want" ] || fail "EKS_RULE_GROUPS in the Makefile is [$(flat "$have")], want [$(flat "$want")]"

loaded=$(groups "$A/ryft.yaml")
[ "$(flat "$loaded")" = "ryft/ryft ryft/ryft-pipeline ryft/ryft-ops" ] || fail "ryft.yaml's groups are [$(flat "$loaded")]: update this test and EKS leftovers in docs/OBSERVABILITY.md"
while read -r g; do
  ! grep -qxF "$g" <<<"$loaded" || fail "EKS_RULE_GROUPS names $g, which ryft.yaml loads"
done <<<"$have"

# alerts-apply loads ryft.yaml and nothing else.
loads=$(grep -o 'rules load [^;]*' "$HERE/Makefile")
[ "$loads" = "rules load /alerts/ryft.yaml" ] || fail "alerts-apply must load /alerts/ryft.yaml only, has: $loads"

echo "✓ alerts_retire_test: alerts-retire-eks deletes exactly $(flat "$have"), none of ryft.yaml's groups, and alerts-apply loads ryft.yaml only"
