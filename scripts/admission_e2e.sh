#!/usr/bin/env bash
# The admission policies (D120, T12-d) against a real API server in kind. Two modes:
#
#   --mode ci    (CI's `admission` job; about 5 minutes) a kind cluster at EKS's minor, the CRDs of
#                the add-ons and of Argo CD, the namespaces with their labels, the chart installed
#                twice (the committed values: Audit; every binding Deny), every Application's render
#                dry-run applied as Argo CD's controller, one refused fixture per rule, status.typeChecking
#                of every policy, claims a, b, c, d and h (the negative test of T12-f).
#   --mode full  (make admission-e2e; the manual workflow; about 25 minutes) the above, plus the real
#                stack: cert-manager, Argo CD from argocd.tf's values with T12-f's RBAC, the operators
#                and the broker synced by Argo CD from this tree, hooks, terminate-op, the broker restart
#                of D98, the operators' reach, claims i and m. See scripts/lib/admission_full.sh.
#
#   scripts/admission_e2e.sh [--mode ci|full] [--repo DIR] [--summary FILE] [--keep]
#                            [--kubeconfig FILE] [--argocd-values FILE]
#
#   --repo DIR       the tree under test: deploy/ and infra/aws/kubernetes/{main,argocd}.tf are read from
#                    it (a copy of #47's or #51's branch, say). The chart, the committed values and the
#                    fixtures are always this checkout's. Default: this checkout.
#   --kubeconfig F   use that existing kind cluster instead of creating one (it must be a kind-* context
#                    with a server on this machine; see scripts/lib/kind_guard.sh). Not deleted afterwards.
#   --keep           keep the cluster this run created (its kubeconfig is printed).
#   --argocd-values  Argo CD's chart values as JSON, instead of evaluating argocd.tf with terraform.
#
# SAFETY: this machine's kubectl may point at production. The script never uses the default kubeconfig:
# it creates its own cluster with `kind create cluster --kubeconfig <temp>/kind.kubeconfig`, exports
# KUBECONFIG to that file, passes --kubeconfig and --context on every kubectl and helm call, and
# refuses to run unless the context is kind-* with a server on this machine (scripts/lib/kind_guard.sh).
# Exit status: 0 when no check failed.
set -uo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
MODE=ci REPO="" SUMMARY="" KEEP=0 GIVEN_KUBECONFIG="" ARGO_VALUES_FILE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --mode) MODE=${2:?}; shift 2 ;;
    --repo) REPO=${2:?}; shift 2 ;;
    --summary) SUMMARY=${2:?}; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --kubeconfig) GIVEN_KUBECONFIG=${2:?}; shift 2 ;;
    --argocd-values) ARGO_VALUES_FILE=${2:?}; shift 2 ;;
    -h|--help) sed -n '2,/^set -uo/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "admission_e2e: unknown argument $1" >&2; exit 2 ;;
  esac
done
case "$MODE" in ci|full) ;; *) echo "admission_e2e: --mode is ci or full" >&2; exit 2 ;; esac
REPO=${REPO:-$ROOT}
REPO=$(cd "$REPO" 2>/dev/null && pwd) || { echo "admission_e2e: --repo is not a directory" >&2; exit 2; }
[ -d "$REPO/deploy/argocd" ] || { echo "admission_e2e: $REPO has no deploy/argocd" >&2; exit 2; }
export ROOT REPO MODE ARGO_VALUES_FILE

for tool in kind kubectl helm yq jq; do
  command -v "$tool" >/dev/null 2>&1 || { echo "admission_e2e: $tool is not installed (this script installs no software)" >&2; exit 2; }
done
yq --version 2>&1 | grep -q mikefarah || { echo "admission_e2e: yq must be mikefarah's v4 (scripts/install_yq.sh in CI)" >&2; exit 2; }

# The node image: EKS's minor (infra/aws kubernetes_version = 1.36), pinned by the digest of the
# multi-arch index. Move it with the EKS minor (docs/OPERATIONS.md -> Upgrading).
KIND_NODE_IMAGE='docker.io/kindest/node:v1.36.4@sha256:099e049362a1526b2db71494e1947aae99bd16290d7c895f2b7ea312e3cbfaed'

# shellcheck source=lib/kind_guard.sh
. "$ROOT/scripts/lib/kind_guard.sh"
# shellcheck source=lib/admission_lib.sh
. "$ROOT/scripts/lib/admission_lib.sh"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/admission-e2e.XXXXXX")
WORK=$(cd "$WORK" && pwd -P)
export WORK
CREATED=0 KIND_CLUSTER=
cleanup() {
  local rc=$?
  if declare -F adm_full_kill_bg >/dev/null 2>&1; then adm_full_kill_bg; fi
  if [ "$CREATED" = 1 ] && [ "$KEEP" = 0 ]; then
    say "deleting the kind cluster $KIND_CLUSTER"
    kind delete cluster --name "$KIND_CLUSTER" --kubeconfig "$WORK/kind.kubeconfig" >/dev/null 2>&1 || say "! could not delete the kind cluster $KIND_CLUSTER: kind delete cluster --name $KIND_CLUSTER"
  elif [ "$CREATED" = 1 ]; then
    say "kept: KUBECONFIG=$WORK/kind.kubeconfig (kind delete cluster --name $KIND_CLUSTER --kubeconfig $WORK/kind.kubeconfig)"
  fi
  # The temporary directory goes with the run; KEEP_WORK=1 (or --keep) keeps the logs and renders.
  if [ "$KEEP" = 0 ] && [ -z "${KEEP_WORK:-}" ]; then rm -rf "$WORK"; else say "work directory kept: $WORK"; fi
  exit "$rc"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------------------------
# the cluster

# Docker, or Podman when that is all there is.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  ENGINE=docker
elif command -v podman >/dev/null 2>&1 && podman info >/dev/null 2>&1; then
  ENGINE=podman
  export KIND_EXPERIMENTAL_PROVIDER=podman
else
  echo "admission_e2e: neither docker nor podman answers; a kind cluster cannot start" >&2
  exit 2
fi
export ENGINE

create_cluster() {
  KIND_CLUSTER="ryft-admission-$$"
  if kind get clusters 2>/dev/null | grep -qx "$KIND_CLUSTER"; then echo "admission_e2e: a kind cluster $KIND_CLUSTER exists already" >&2; exit 2; fi
  mkdir -p "$WORK/audit"
  cat > "$WORK/audit/policy.yaml" <<'EOF'
apiVersion: audit.k8s.io/v1
kind: Policy
omitStages: ["RequestReceived"]
rules:
  - level: None
    verbs: ["get", "list", "watch"]
  - level: None
    nonResourceURLs: ["/healthz*", "/readyz*", "/livez*", "/version", "/metrics"]
  - level: Metadata
EOF
  cat > "$WORK/kind-config.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        apiVersion: kubeadm.k8s.io/v1beta4
        kind: ClusterConfiguration
        apiServer:
          extraArgs:
            - name: audit-policy-file
              value: /etc/kubernetes/audit/policy.yaml
            - name: audit-log-path
              value: /var/log/kubernetes/audit.log
            - name: audit-log-maxsize
              value: "200"
          extraVolumes:
            - name: audit-policy
              hostPath: /etc/kubernetes/audit
              mountPath: /etc/kubernetes/audit
              readOnly: true
              pathType: DirectoryOrCreate
            - name: audit-log
              hostPath: /var/log/kubernetes
              mountPath: /var/log/kubernetes
              readOnly: false
              pathType: DirectoryOrCreate
    extraMounts:
      - hostPath: $WORK/audit/policy.yaml
        containerPath: /etc/kubernetes/audit/policy.yaml
        readOnly: true
EOF
  say "creating the kind cluster $KIND_CLUSTER ($KIND_NODE_IMAGE) with $ENGINE; kubeconfig $WORK/kind.kubeconfig"
  if ! kind create cluster --name "$KIND_CLUSTER" --kubeconfig "$WORK/kind.kubeconfig" --image "$KIND_NODE_IMAGE" --config "$WORK/kind-config.yaml" --wait 240s >"$WORK/log/kind-create.log" 2>&1; then
    cat "$WORK/log/kind-create.log" >&2
    echo "admission_e2e: kind could not create the cluster" >&2
    CREATED=1   # a half-made cluster is deleted too
    exit 2
  fi
  CREATED=1
  KUBECONFIG="$WORK/kind.kubeconfig"
  export KUBECONFIG
}

mkdir -p "$WORK/log"
if [ -n "$GIVEN_KUBECONFIG" ]; then
  KUBECONFIG=$GIVEN_KUBECONFIG
  export KUBECONFIG
  KIND_CLUSTER=$(kubectl --kubeconfig "$KUBECONFIG" config current-context 2>/dev/null | sed 's/^kind-//')
else
  # Our own file and nothing else: whatever KUBECONFIG held is not used.
  unset KUBECONFIG
  create_cluster
fi
export KIND_CLUSTER
kind_guard || exit 2
say "cluster: context $KIND_CONTEXT, kubeconfig $KUBECONFIG; tree under test: $REPO"

# ---------------------------------------------------------------------------------------------
# the checks

adm_init
rec "node-image" INFO "$(kc version -o json 2>/dev/null | jq -r '.serverVersion.gitVersion') ($KIND_NODE_IMAGE)"

group "CRDs and namespaces"
adm_install_crds
adm_create_namespaces
endgroup

group "Argo CD's chart, its RBAC and the identities"
adm_argo_values
adm_argo_render && adm_check_argo_rbac
adm_install_argo_identities
endgroup

# shellcheck source=lib/admission_ci.sh
. "$ROOT/scripts/lib/admission_ci.sh"
adm_ci_checks
if [ "$MODE" = full ]; then
  # shellcheck source=lib/admission_full.sh
  . "$ROOT/scripts/lib/admission_full.sh"
  adm_full
fi

adm_summary "${SUMMARY:-$WORK/summary.md}"
say "--"
cat "${SUMMARY:-$WORK/summary.md}" >&2
[ -z "$SUMMARY" ] || say "summary written to $SUMMARY"
if [ "$FAILED" != 0 ]; then say "admission e2e: FAILED"; exit 1; fi
say "admission e2e: ok"
