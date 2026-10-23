#!/usr/bin/env bash
# Part of make k8s-validate (offline): every Argo CD application in deploy/argocd/apps (and
# root.yaml) reads this repository, belongs to a project in deploy/argocd/project.yaml, deploys
# into a namespace that project admits, and points at a path that renders. Parsed with yq, and
# read by an ALLOWLIST (scripts/lib/yamlpolicy.py, APPLICATION), not by regex:
#   - exactly one YAML document, a regular file, only the YAML tags !!map !!seq !!str !!int
#     !!bool !!null, no anchors, aliases or `<<`;
#   - only the keys the applications use today. In particular exactly one `spec.source` with
#     repoURL, targetRevision (main) and path: `sources`, `source.kustomize` (images, patches),
#     `helm`, `plugin` and `directory` are refused, because each changes what Argo CD deploys
#     from the path without a line in the path's own files;
#   - the path is plain, below deploy/, with no symlink on the way, and the kustomizations it
#     holds or reaches pass scripts/check_kustomize_refs.sh; every path except apps/ (the app
#     of apps' plain manifests) has a regular kustomization.yaml.
#   - only deploy/argocd/root.yaml may use the project ryft-root (the only project that may create
#     Applications), and every other path is RENDERED (with git limited to file://, so a remote base
#     that slipped through fails instead of being fetched) and the render must keep the production
#     invariants (scripts/lib/renderpolicy.py): no argoproj.io object, only our six images with a
#     7-hex tag (and the broker's digest-pinned rabbitmq), AUTH_MODE=supabase and no API_KEY or
#     ALLOW_INSECURE_APIKEY_AUTH, the default-deny NetworkPolicy intact in every ryft-* namespace.
# apps/ holds only regular *.yaml files (Argo applies .yml, .json and .jsonnet there too), and no
# kustomization.yaml. Only a vault-namespaces directory may be missing: infra/vault writes it on
# its first apply after a service is added (docs/OPERATIONS.md -> Adding a service).
# Needs yq (mikefarah v4), python3 and kubectl. Exit 1: a finding. Exit 2: no yq.
set -euo pipefail
cd "$(dirname "$0")/.."
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  echo "check_argocd: yq (mikefarah, v4) is not installed, so the applications cannot be parsed (CI installs it; locally: brew install yq)" >&2
  exit 2
fi
exec python3 -B -I scripts/lib/check_argocd.py
