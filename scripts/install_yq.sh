#!/usr/bin/env bash
# CI: install the pinned yq (mikefarah v4) release binary, checked against a sha256 kept HERE, and
# put it first on PATH for the later steps of the job (GITHUB_PATH). Used by every job that runs a
# check which parses with yq (manifests, smoke): one place for the version and the checksum, no Go
# toolchain download per run. To move it: the new tag and `sha256sum yq_linux_amd64` of that release
# (cross-check with the release's `checksums` file). Linux amd64 only (the runners).
set -euo pipefail
YQ_VERSION=v4.54.1
YQ_SHA256=8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f
dir="${RUNNER_TEMP:?RUNNER_TEMP is not set: this script is for CI runners}/yq-bin"
mkdir -p "$dir"
curl -fsSL --retry 3 --retry-delay 2 --proto '=https' --tlsv1.2 -o "$dir/yq" \
  "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/yq_linux_amd64"
echo "${YQ_SHA256}  $dir/yq" | sha256sum -c -
chmod 0755 "$dir/yq"
echo "$dir" >> "${GITHUB_PATH:?GITHUB_PATH is not set: this script is for CI runners}"
"$dir/yq" --version | grep -F "version ${YQ_VERSION}"
