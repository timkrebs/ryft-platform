#!/usr/bin/env bash
# Part of make k8s-validate. Every kustomization.yaml under deploy/ (and every one it reaches
# through `resources:`) is read by an ALLOWLIST, not a list of forbidden things:
#   - the file is a regular file named exactly kustomization.yaml (no symlink; Kustomization and
#     kustomization.yml are refused by name), one YAML document;
#   - its keys are matched exactly and case-sensitively against the schema in
#     scripts/lib/yamlpolicy.py (kustomize reads `Resources:` and a long-s `resources:` too, and
#     decodes !!binary, so no denylist of spellings can be complete): apiVersion, kind, namespace,
#     resources, patches (path, patch, target), images (name, newTag), labels (pairs,
#     includeSelectors), configMapGenerator / secretGenerator (name, namespace, behavior, envs,
#     literals, options.labels), generatorOptions (disableNameSuffixHash); nothing else, so no
#     generators, transformers, helmCharts, bases, components, crds, files, replacements;
#   - the only YAML tags are !!map !!seq !!str !!int !!bool !!null; `<<`, !!binary, anchors and
#     aliases are refused by name.
# Then every reference is a plain relative path (segments of [A-Za-z0-9._-] or ..), no symlink on
# the way, whose real path is inside deploy/: kustomize would otherwise fetch a remote base
# (`github.com/...//path?ref=...`, `https://...`) or read `../../../backend/...`. A resource is a
# directory with a regular kustomization.yaml or a *.yaml / *.yml file, a patch file is
# *.yaml / *.yml / *.json, a generator input is *.env (it may be generated and git-ignored, so
# only its location is checked). Inline patches are text in the kustomization, which
# scripts/check_bytes.sh covers.
# To add a key: change the schema in scripts/lib/yamlpolicy.py in the same PR as its first use.
# Parsed with yq (mikefarah v4); without it the check fails (exit 2).
# Usage: scripts/check_kustomize_refs.sh [dir-below-which-to-look]  (default deploy; check_argocd.sh
# runs it on every application's path). Exit 1: a finding. Exit 2: nothing to check, or no yq.
set -euo pipefail
cd "$(dirname "$0")/.."
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  echo "check_kustomize_refs: yq (mikefarah, v4) is not installed, so the kustomizations cannot be parsed (CI installs it; locally: brew install yq)" >&2
  exit 2
fi
exec python3 -B -I scripts/lib/check_kustomize_refs.py "${1:-deploy}"
