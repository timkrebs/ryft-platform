#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2329,SC2034
# The two binding checks of the admission kind run (scripts/lib/admission_lib.sh, T12-f) on canned input
# (make test-scripts): adm_check_argo_bindings on a render (the chart's own bindings pass; a
# ClusterRoleBinding to cluster-admin, or a binding of an argocd identity to another role, fails) and
# adm_check_argo_tf_bindings on a Terraform module (a kubernetes_cluster_role_binding_v1 or a manifest of
# a RoleBinding that names an argocd identity fails; one that names none does not). No cluster is contacted.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ argo_bindings_test: $*" >&2; exit 1; }
if ! command -v jq >/dev/null || ! command -v yq >/dev/null || ! yq --version 2>&1 | grep -q mikefarah; then
  echo "✓ argo_bindings_test (skipped: no jq or mikefarah yq)"; exit 0
fi

ROOT=$HERE WORK=$tmp REPO=$tmp/tree MODE=ci KIND_CLUSTER=test
export ROOT WORK REPO MODE
. "$HERE/scripts/lib/admission_lib.sh"
mkdir -p "$WORK/log"
adm_init
say() { :; }
res() { awk -F'\t' -v id="$1" '$1 == id { print $2 }' "$RESULTS" | tail -1; }
expect() { [ "$(res "$1")" = "$2" ] || fail "$3: expected $2, got $(res "$1") ($(tail -1 "$RESULTS"))"; }

# --- the render ---
cat > "$tmp/own.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: argocd-application-controller}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: argocd-application-controller}
subjects: [{kind: ServiceAccount, name: argocd-application-controller, namespace: argocd}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: argocd-server}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: argocd-server}
subjects: [{kind: ServiceAccount, name: argocd-server, namespace: argocd}]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: argocd-repo-server, namespace: argocd}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: argocd-repo-server}
subjects: [{kind: ServiceAccount, name: argocd-repo-server, namespace: argocd}]
EOF
ARGO_RENDER=$tmp/own.yaml
: > "$RESULTS"; adm_check_argo_bindings; expect argo-bindings PASS "the chart's own bindings"

cp "$tmp/own.yaml" "$tmp/admin.yaml"
cat >> "$tmp/admin.yaml" <<'EOF'
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: argocd-admin}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects: [{kind: ServiceAccount, name: argocd-application-controller, namespace: argocd}]
EOF
ARGO_RENDER=$tmp/admin.yaml
: > "$RESULTS"; adm_check_argo_bindings; expect argo-bindings FAIL "a ClusterRoleBinding to cluster-admin in extraObjects"

# the same name, another role
cat > "$tmp/swapped.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: argocd-server}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}
subjects: [{kind: ServiceAccount, name: argocd-server, namespace: argocd}]
EOF
ARGO_RENDER=$tmp/swapped.yaml
: > "$RESULTS"; adm_check_argo_bindings; expect argo-bindings FAIL "argocd-server bound to cluster-admin under its own name"

cat > "$tmp/rb.yaml" <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: argocd-server, namespace: kube-system}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: admin}
subjects: [{kind: ServiceAccount, name: argocd-server, namespace: argocd}]
EOF
ARGO_RENDER=$tmp/rb.yaml
: > "$RESULTS"; adm_check_argo_bindings; expect argo-bindings FAIL "a RoleBinding to the admin ClusterRole"
ARGO_RENDER=
: > "$RESULTS"; adm_check_argo_bindings; expect argo-bindings SKIP "no render"

# --- the module ---
mod=$REPO/$K8S_DIR_REL
mkdir -p "$mod/modules/m"
cat > "$mod/argocd.tf" <<'EOF'
# kubernetes_cluster_role_binding_v1 for argocd-application-controller in a comment is not a resource
resource "helm_release" "argocd" {
  name = "argocd"
}
EOF
: > "$RESULTS"; adm_check_argo_tf_bindings; expect argo-tf-bindings PASS "the module as it is"

cat > "$mod/other.tf" <<'EOF'
resource "kubernetes_cluster_role_binding_v1" "vso" {
  metadata { name = "vso" }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "view"
  }
  subject {
    kind      = "ServiceAccount"
    name      = "vault-secrets-operator"
    namespace = "vault-secrets-operator-system"
  }
}
EOF
: > "$RESULTS"; adm_check_argo_tf_bindings; expect argo-tf-bindings PASS "a binding that names no argocd identity"

cat > "$mod/modules/m/bad.tf" <<'EOF'
resource "kubernetes_cluster_role_binding_v1" "argocd_admin" {
  metadata { name = "argocd-admin" }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "cluster-admin"
  }
  subject {
    kind      = "ServiceAccount"
    name      = "argocd-application-controller"
    namespace = "argocd"
  }
}
EOF
: > "$RESULTS"; adm_check_argo_tf_bindings; expect argo-tf-bindings FAIL "a kubernetes_cluster_role_binding_v1 for the controller"
rm "$mod/modules/m/bad.tf"

cat > "$mod/m2.tf" <<'EOF'
resource "kubernetes_manifest" "x" {
  manifest = {
    apiVersion = "rbac.authorization.k8s.io/v1"
    kind       = "RoleBinding"
    metadata   = { name = "x", namespace = "kube-system" }
    roleRef    = { apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = "admin" }
    subjects   = [{ kind = "ServiceAccount", name = "argocd-server", namespace = "argocd" }]
  }
}
EOF
: > "$RESULTS"; adm_check_argo_tf_bindings; expect argo-tf-bindings FAIL "a manifest of a RoleBinding for the server"
rm "$mod/m2.tf"

cat > "$mod/m3.tf" <<'EOF'
resource "kubernetes_role_binding" "old" {
  metadata {
    name = "x"
  }
  subject {
    name = "argocd-server"
  }
}
EOF
: > "$RESULTS"; adm_check_argo_tf_bindings; expect argo-tf-bindings FAIL "the unversioned kubernetes_role_binding"

echo "✓ argo_bindings_test"
