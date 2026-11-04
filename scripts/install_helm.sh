#!/usr/bin/env bash
# CI: install the pinned Helm release, checked against a sha256 kept HERE, and put it first on PATH for
# the later steps (GITHUB_PATH). Used by the `admission` job and the admission-e2e workflow
# (scripts/admission_e2e.sh): the chart is rendered and installed with this client, so it does not
# move with the runner image. To move it: the new tag and the checksum of the linux-amd64 tarball
# (https://get.helm.sh/helm-<tag>-linux-amd64.tar.gz.sha256sum; cross-check by hashing the downloaded
# file). Linux amd64 only (the runners).
set -euo pipefail
HELM_VERSION=v4.3.1
HELM_SHA256=0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b
dir="${RUNNER_TEMP:?RUNNER_TEMP is not set: this script is for CI runners}/helm-bin"
rm -rf "$dir"
mkdir -p "$dir/unpack"
curl -fsSL --retry 3 --retry-delay 2 --proto '=https' --tlsv1.2 -o "$dir/helm.tar.gz" \
  "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"
echo "${HELM_SHA256}  $dir/helm.tar.gz" | sha256sum -c -
tar -xzf "$dir/helm.tar.gz" -C "$dir/unpack" linux-amd64/helm
mv "$dir/unpack/linux-amd64/helm" "$dir/helm"
rm -rf "$dir/unpack" "$dir/helm.tar.gz"
chmod 0755 "$dir/helm"
echo "$dir" >> "${GITHUB_PATH:?GITHUB_PATH is not set: this script is for CI runners}"
"$dir/helm" version --short | grep -F "${HELM_VERSION}"
