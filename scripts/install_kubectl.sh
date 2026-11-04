#!/usr/bin/env bash
# CI: install the pinned kubectl release binary, checked against a sha256 kept HERE, and put it first
# on PATH for the later steps (GITHUB_PATH). Used by the `admission` job and the admission-e2e
# workflow (scripts/admission_e2e.sh), so that the kustomize inside kubectl and the client the run
# talks to the API server with do not move with the runner image. The version is the EKS minor of
# infra/aws (kubernetes_version) and of the kind node image in scripts/admission_e2e.sh: move all
# three together (docs/OPERATIONS.md -> Admission policies: Upgrading it). To move it: the new tag and
# the checksum of the linux-amd64 file (https://dl.k8s.io/release/<tag>/bin/linux/amd64/kubectl.sha256;
# cross-check by hashing the downloaded file). Linux amd64 only (the runners).
set -euo pipefail
KUBECTL_VERSION=v1.36.5
KUBECTL_SHA256=33bc88a24c3b09cf55bfd59e6ca977881b61fc3f25ab65bc80c65b430248648e
dir="${RUNNER_TEMP:?RUNNER_TEMP is not set: this script is for CI runners}/kubectl-bin"
mkdir -p "$dir"
curl -fsSL --retry 3 --retry-delay 2 --proto '=https' --tlsv1.2 -o "$dir/kubectl" \
  "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"
echo "${KUBECTL_SHA256}  $dir/kubectl" | sha256sum -c -
chmod 0755 "$dir/kubectl"
echo "$dir" >> "${GITHUB_PATH:?GITHUB_PATH is not set: this script is for CI runners}"
"$dir/kubectl" version --client | grep -F "Client Version: ${KUBECTL_VERSION}"
