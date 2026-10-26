#!/usr/bin/env bash
# CI: install the pinned kind release binary, checked against a sha256 kept HERE, and put it first on
# PATH for the later steps (GITHUB_PATH). Used by the `admission` job and the admission-e2e workflow,
# which create a kind cluster (scripts/admission_e2e.sh). To move it: the new tag and the checksum of
# the linux-amd64 file of that release (cross-check with the release's .sha256sum file). Linux amd64
# only (the runners). The node image is pinned in scripts/admission_e2e.sh.
set -euo pipefail
KIND_VERSION=v0.33.0
KIND_SHA256=aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d
dir="${RUNNER_TEMP:?RUNNER_TEMP is not set: this script is for CI runners}/kind-bin"
mkdir -p "$dir"
curl -fsSL --retry 3 --retry-delay 2 --proto '=https' --tlsv1.2 -o "$dir/kind" \
  "https://github.com/kubernetes-sigs/kind/releases/download/${KIND_VERSION}/kind-linux-amd64"
echo "${KIND_SHA256}  $dir/kind" | sha256sum -c -
chmod 0755 "$dir/kind"
echo "$dir" >> "${GITHUB_PATH:?GITHUB_PATH is not set: this script is for CI runners}"
"$dir/kind" version | grep -F "kind ${KIND_VERSION}"
