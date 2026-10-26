#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2329,SC2034
# The audit-log findings of the admission kind run (scripts/lib/admission_full.sh, adm_full_findings and
# adm_full_explained) on canned audit lines (make test-scripts): a validation failure is explained only
# with the policy, requester, resource and message of what the tree has not fixed yet, and only while the
# tree lacks that; everything else is a failure. A missing audit log is a failure for a cluster the run
# created and a skip for one handed over. No cluster is contacted.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ admission_findings_test: $*" >&2; exit 1; }
command -v jq >/dev/null || { echo "✓ admission_findings_test (skipped: no jq)"; exit 0; }

ROOT=$HERE WORK=$tmp REPO=$tmp/tree MODE=full KIND_CLUSTER=test
export ROOT WORK REPO MODE
. "$HERE/scripts/lib/admission_lib.sh"
. "$HERE/scripts/lib/admission_full.sh"
mkdir -p "$WORK/log"
adm_init   # RESULTS, FAILED
say() { :; }

SCOPED=0 PUBLIC=0 AUDIT="" AUDIT_RC=0 CREATED=1
tree_has_scoped_operators() { [ "$SCOPED" = 1 ]; }
tree_broker_egress_public() { [ "$PUBLIC" = 1 ]; }
adm_audit_grep() { [ "$AUDIT_RC" = 0 ] || return "$AUDIT_RC"; [ -z "$AUDIT" ] || cat "$AUDIT"; }

# line <policy> <binding> <actions> <user> <resource> <uri> <message> <timestamp>
line() {
  jq -nc --arg p "$1" --arg b "$2" --arg a "$3" --arg u "$4" --arg r "$5" --arg uri "$6" --arg m "$7" --arg t "$8" '{
    requestReceivedTimestamp: $t, requestURI: $uri, user: {username: $u}, objectRef: {resource: $r},
    annotations: {"validation.policy.admission.k8s.io/validation_failure": ([{message: $m, policy: $p, binding: $b, expressionIndex: 0, validationActions: ($a | split("+"))}] | tojson)}}'
}
CTRL=system:serviceaccount:argocd:argocd-application-controller
P6_WILD="P6: a Role or ClusterRole of the operators must not hold a wildcard verb, escalate, bind, impersonate, a resource outside the operators' allowlist, or a non-resource URL but /metrics."
P6_RO="P6: a ClusterRole of the operators is read-only (get, list, watch) except for rabbitmq.com, events and the tokenreview and subjectaccessreview creates."
P3="P3: an egress ipBlock must be 0.0.0.0/0 with the private ranges excepted on 443/5432, or the broker's 443 rule."
NEW=2026-10-26T12:00:01.000000Z OLD=2026-10-26T11:00:00.000000Z SINCE=2026-10-26T12:00:00.000000Z

# run <stage> <held> -> the result of the findings check in $RES (PASS|FAIL|SKIP)
run() {
  : > "$RESULTS"
  adm_full_findings "findings" "$SINCE" "$(adm_full_explained "$1" "${2:-}")"
  RES=$(awk -F'\t' '$1 == "findings" { print $2 }' "$RESULTS")
}
expect() { [ "$RES" = "$1" ] || fail "$2: expected $1, got $RES ($(cat "$RESULTS"))"; }

AUDIT=$tmp/audit; : > "$AUDIT"
run audit; expect PASS "no failures logged"

{ line ryft-operators-cluster ryft-operators-cluster Warn+Audit "$CTRL" clusterroles "/apis/rbac/v1/clusterroles" "$P6_WILD" "$NEW"
  line ryft-operators-cluster ryft-operators-cluster Warn+Audit "$CTRL" clusterroles "/apis/rbac/v1/clusterroles?dryRun=All" "$P6_RO" "$NEW"; } > "$AUDIT"
SCOPED=0; run audit; expect PASS "the vendored operator ClusterRoles before T12-g"
SCOPED=1; run audit; expect FAIL "the same findings once the tree has T12-g"
SCOPED=0

line ryft-operators-cluster ryft-operators-cluster Warn+Audit "$CTRL" clusterroles /x "$P6_WILD" "$OLD" > "$AUDIT"
run audit; expect PASS "a finding from before the stage began is not looked at"

# the canary of adm_wait_enforced is refused on purpose and is not a finding; the same refusal of another object is
jq -nc --arg t "$NEW" '{requestReceivedTimestamp: $t, requestURI: "/apis/x?dryRun=All", user: {username: "kubernetes-admin"}, objectRef: {resource: "vaultdynamicsecrets", name: "zz-canary"},
  annotations: {"validation.policy.admission.k8s.io/validation_failure": ([{message: "P4: only VaultConnection, VaultAuth and VaultStaticSecret are allowed.", policy: "ryft-vso", binding: "ryft-vso", expressionIndex: 0, validationActions: ["Deny"]}] | tojson)}}' > "$AUDIT"
run deny; expect PASS "the canary's own refusal"
jq -c '.objectRef.name = "x"' "$AUDIT" > "$AUDIT.2"; mv "$AUDIT.2" "$AUDIT"
run deny; expect FAIL "the same refusal of an object that is not the canary"

line ryft-operators-cluster ryft-operators-cluster Warn+Audit "system:serviceaccount:argocd:argocd-server" clusterroles /x "$P6_WILD" "$NEW" > "$AUDIT"
run audit; expect FAIL "the P6 shape from another requester"
line ryft-operators-cluster ryft-operators-cluster Warn+Audit "$CTRL" clusterrolebindings /x "$P6_WILD" "$NEW" > "$AUDIT"
run audit; expect FAIL "the P6 shape on another resource"
line ryft-operators-cluster ryft-operators-cluster Warn+Audit "$CTRL" clusterroles /x "P6: a ClusterRoleBinding must bind the operators' ClusterRoles to the two operator service accounts." "$NEW" > "$AUDIT"
run audit; expect FAIL "another P6 message"
line ryft-vso ryft-vso Warn+Audit "$CTRL" vaultstaticsecrets /x "P4: something" "$NEW" > "$AUDIT"
run audit; expect FAIL "another policy"
line ryft-operators-cluster ryft-operators-cluster Deny "$CTRL" clusterroles /x "$P6_WILD" "$NEW" > "$AUDIT"
run audit; expect FAIL "the P6 shape under Deny (the action is not an audit one)"

# P3: explained only in the deny stage, only while the broker's egress is open and the binding is held
line ryft-netpol ryft-netpol Warn+Audit "$CTRL" networkpolicies /x "$P3" "$NEW" > "$AUDIT"
PUBLIC=1; run deny netpol; expect PASS "the broker's open egress in the deny stage with netpol held"
PUBLIC=1; run audit; expect FAIL "the same in the audit stage"
PUBLIC=1; run deny ""; expect FAIL "the same with netpol not held"
PUBLIC=0; run deny netpol; expect FAIL "the same once the tree has T12-k"
PUBLIC=0

# an unreadable audit log: a FAIL for a cluster the run created, a SKIP for one handed over
AUDIT_RC=2
CREATED=1; run audit; expect FAIL "no audit log on a cluster the run created"
CREATED=0; run audit; expect SKIP "no audit log on a cluster handed over"

echo "✓ admission_findings_test"
