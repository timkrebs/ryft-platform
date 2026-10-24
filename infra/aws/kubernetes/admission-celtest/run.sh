#!/usr/bin/env bash
# Runs the CEL harness end to end, offline and read-only:
#   1. helm template of the chart with the committed values (admission-values.yaml) and,
#      for P7, the decoded deploy/argocd/project.yaml as `projects`;
#   2. the renders of the repository (kustomize, local only; the cluster is never
#      contacted) and the generated VSO objects and Argo CD manifests;
#   3. the Go harness evaluates the policies against all of them (go run .).
# Needs helm, kubectl (kustomize only), yq (mikefarah) and Go. Exit status is non-zero on
# any mismatch. Everything it renders goes to a temporary directory.
#
# CELTEST_REPO=<dir> reads deploy/ from a copy of the tree (to try a branch's manifests
# against this checkout's policies); the chart and the values are always this checkout's.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="${CELTEST_REPO:-$(cd "$here/../../../.." && pwd)}"
# The chart and the values are always this checkout's (the policies under test); the manifests
# come from $repo.
k8s="$(cd "$here/.." && pwd)"
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
export GIT_ALLOW_PROTOCOL=file

# The committed values, plus P7's constant exactly as T12-b will pass it.
yq eval-all '[select(di >= 0)] | {"projects": .}' "$repo/deploy/argocd/project.yaml" > "$out/projects-values.yaml"
helm template admission-policy "$k8s/charts/admission-policy" \
  -f "$k8s/admission-values.yaml" -f "$out/projects-values.yaml" > "$out/chart.yaml"

# What Argo CD applies, and what the controllers write on top.
kubectl kustomize "$repo/deploy/k8s/overlays/eks" > "$out/eks.yaml"
kubectl kustomize "$repo/deploy/rabbitmq" > "$out/rabbitmq.yaml"
kubectl kustomize "$repo/deploy/rabbitmq/operators" > "$out/operators.yaml"
awk 'FNR==1{print "---"} {print}' "$repo"/deploy/k8s/overlays/vault-namespaces/*/vault.yaml > "$out/vault.yaml"
awk 'FNR==1{print "---"} {print}' "$repo/deploy/argocd/project.yaml" "$repo/deploy/argocd/root.yaml" "$repo"/deploy/argocd/apps/*.yaml > "$out/argocd.yaml"

cd "$here"
go run . "$out/chart.yaml" "$out/eks.yaml" "$out/rabbitmq.yaml" "$out/operators.yaml" "$out/vault.yaml" "$out/argocd.yaml"
