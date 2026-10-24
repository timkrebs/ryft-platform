#!/usr/bin/env bash
# shellcheck disable=SC2154
# scripts/check_operators_scope.sh on renders built here (make test-scripts): the real render
# passes; a renamed (so unmatched) delete target, a ClusterRole with secrets, an extra
# ClusterRoleBinding, a missing Role and a missing OPERATOR_SCOPE_NAMESPACE each fail.
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_operators_scope_test: $*" >&2; exit 1; }
cd "$HERE"

kubectl kustomize deploy/rabbitmq/operators > "$tmp/good.yaml"
./scripts/check_operators_scope.sh "$tmp/good.yaml" >/dev/null 2>&1 || fail "the real render is refused"

expect_fail() { # <name> <file> <message fragment>
  if ./scripts/check_operators_scope.sh "$2" >"$tmp/out" 2>&1; then fail "$1: accepted"; fi
  grep -qF -- "$3" "$tmp/out" || fail "$1: wrong finding: $(cat "$tmp/out")"
}

# A release that renamed the ClusterRole: the delete patch matches nothing, the old rules stay.
{ cat "$tmp/good.yaml"; cat <<'E'
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: messaging-topology-manager-role-renamed }
rules:
  - { apiGroups: [""], resources: [secrets], verbs: [get, list, watch] }
E
} > "$tmp/secrets.yaml"
expect_fail "renamed ClusterRole" "$tmp/secrets.yaml" "grants ['secrets'] cluster-wide"

{ cat "$tmp/good.yaml"; cat <<'E'
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: { name: x }
rules:
  - { apiGroups: [""], resources: [pods/exec], verbs: [create] }
E
} > "$tmp/exec.yaml"
expect_fail "pods/exec" "$tmp/exec.yaml" "pods/exec"

{ cat "$tmp/good.yaml"; cat <<'E'
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: { name: rabbitmq-cluster-operator-rolebinding }
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: whatever }
subjects: [{ kind: ServiceAccount, name: rabbitmq-cluster-operator, namespace: rabbitmq-system }]
E
} > "$tmp/crb.yaml"
expect_fail "extra ClusterRoleBinding" "$tmp/crb.yaml" "ClusterRoleBinding rabbitmq-cluster-operator-rolebinding is not allowed"

# A Role renamed, so the binding's target is gone.
sed 's/^  name: messaging-topology-manager-role$/  name: messaging-topology-manager-role-v2/' "$tmp/good.yaml" > "$tmp/role.yaml"
expect_fail "missing Role" "$tmp/role.yaml" "Role messaging-topology-manager-role is missing"

# A missing env, per operator: the patch's container name no longer matches (kustomize would add a container, not fail).
awk '$0 == "        - name: OPERATOR_SCOPE_NAMESPACE" { getline; next } { print }' "$tmp/good.yaml" > "$tmp/noenv.yaml"
cmp -s "$tmp/good.yaml" "$tmp/noenv.yaml" && fail "the fixture did not drop the env"
expect_fail "missing env" "$tmp/noenv.yaml" "lacks OPERATOR_SCOPE_NAMESPACE=ryft-rabbitmq"

# Only one of the two: the other container is named in the finding.
awk '$0 == "        - name: OPERATOR_SCOPE_NAMESPACE" && !done { getline; done = 1; next } { print }' "$tmp/good.yaml" > "$tmp/one.yaml"
expect_fail "one env missing" "$tmp/one.yaml" "OPERATOR_SCOPE_NAMESPACE"

# A wrong value.
sed 's/^          value: ryft-rabbitmq$/          value: ""/' "$tmp/good.yaml" > "$tmp/value.yaml"
expect_fail "empty scope" "$tmp/value.yaml" "lacks OPERATOR_SCOPE_NAMESPACE=ryft-rabbitmq"

echo "✓ check_operators_scope_test: the real render passes; a renamed ClusterRole, a secrets or pods/exec ClusterRole, an extra ClusterRoleBinding, a missing Role and a missing or empty OPERATOR_SCOPE_NAMESPACE each fail"
