#!/usr/bin/env bash
# scripts/admission_intended.sh: the committed file equals the values, and a changed value,
# a bad action or a missing binding fails the check.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
script=$here/../admission_intended.sh
root=$(cd "$here/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

"$script" --check || fail "the committed deploy/grafana/alerts/admission-intended.yaml differs from admission-values.yaml"

# a changed action must fail the check
sed 's/^  vso: audit/  vso: deny/' "$root/infra/aws/kubernetes/admission-values.yaml" > "$tmp/v.yaml"
if ADMISSION_VALUES=$tmp/v.yaml "$script" --check >/dev/null 2>&1; then fail "a changed action went unnoticed"; fi
ADMISSION_VALUES=$tmp/v.yaml ADMISSION_INTENDED_OUT=$tmp/o.yaml "$script"
grep -q 'ryft-vso, intended_action: deny' "$tmp/o.yaml" || fail "the generated file lacks the deny"

# an unknown action, a lost binding
sed 's/^  vso: audit/  vso: maybe/' "$root/infra/aws/kubernetes/admission-values.yaml" > "$tmp/bad.yaml"
if ADMISSION_VALUES=$tmp/bad.yaml ADMISSION_INTENDED_OUT=$tmp/o2.yaml "$script" >/dev/null 2>&1; then fail "a bad action was accepted"; fi
grep -v '^  vso: ' "$root/infra/aws/kubernetes/admission-values.yaml" > "$tmp/short.yaml"
if ADMISSION_VALUES=$tmp/short.yaml ADMISSION_INTENDED_OUT=$tmp/o3.yaml "$script" >/dev/null 2>&1; then fail "16 bindings were accepted"; fi
# the keys must equal the chart's: a duplicate (which the render alone would count as a
# 17th binding) and a renamed key both fail
sed 's/^  broker: audit/  vso: audit/' "$root/infra/aws/kubernetes/admission-values.yaml" > "$tmp/dup.yaml"
if ADMISSION_VALUES=$tmp/dup.yaml ADMISSION_INTENDED_OUT=$tmp/o4.yaml "$script" >/dev/null 2>&1; then fail "a duplicate key replacing a binding was accepted"; fi
sed 's/^  broker: audit/  brokr: audit/' "$root/infra/aws/kubernetes/admission-values.yaml" > "$tmp/typo.yaml"
if ADMISSION_VALUES=$tmp/typo.yaml ADMISSION_INTENDED_OUT=$tmp/o5.yaml "$script" >/dev/null 2>&1; then fail "a renamed key was accepted"; fi

# AdmissionBindingMissing must count the chart's bindings: a stale threshold or summary fails --check
# (two PRs that each add a binding would otherwise both pass and leave the alert one short)
alerts=$root/deploy/grafana/alerts/ryft.yaml
want=$(sed -n 's/^want_bindings=\([0-9]*\).*/\1/p' "$script")
grep -q "or vector(0)) < $want\$" "$alerts" || fail "the committed alert does not count $want bindings"
sed "s/or vector(0)) < $want\$/or vector(0)) < $((want - 1))/" "$alerts" > "$tmp/stale-lt.yaml"
if ADMISSION_ALERTS=$tmp/stale-lt.yaml "$script" --check >/dev/null 2>&1; then fail "an alert threshold one short was accepted"; fi
sed "s/ of $want admission policy bindings exist/ of $((want - 1)) admission policy bindings exist/" "$alerts" > "$tmp/stale-of.yaml"
if ADMISSION_ALERTS=$tmp/stale-of.yaml "$script" --check >/dev/null 2>&1; then fail "a stale alert summary was accepted"; fi
grep -v 'or vector(0)) <' "$alerts" > "$tmp/no-lt.yaml"
if ADMISSION_ALERTS=$tmp/no-lt.yaml "$script" --check >/dev/null 2>&1; then fail "an alert without its threshold was accepted"; fi
echo ok
