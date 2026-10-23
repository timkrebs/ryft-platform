#!/usr/bin/env bash
# shellcheck disable=SC2154,SC2015,SC2016
# scripts/check_kustomize_refs.sh and scripts/check_argocd.sh's apps/ rule on copies of deploy/:
# a remote base, a path out of deploy/, a missing file, a .json patch, code-running keys, a
# non-yaml file in apps/ all fail; the repository's own deploy/ passes.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_kustomize_refs_test: $*" >&2; exit 1; }
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  if [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI"; fi
  echo "- check_kustomize_refs_test: SKIPPED, yq (mikefarah v4) is not installed locally (brew install yq). CI does not skip."
  exit 0
fi
fresh() { rm -rf "$tmp/r"; mkdir -p "$tmp/r/scripts"; cp -R "$HERE/deploy" "$tmp/r/deploy"; cp "$HERE/scripts/check_kustomize_refs.sh" "$HERE/scripts/check_argocd.sh" "$tmp/r/scripts/"; cd "$tmp/r"; }
refs() { rc=0; ./scripts/check_kustomize_refs.sh >"$tmp/out" 2>"$tmp/err" || rc=$?; }
K=deploy/k8s/services/api/kustomization.yaml
# bad <name> <expected message> <awk program on $K>
bad() { fresh; awk "$3" "$K" > "$K.new"; mv "$K.new" "$K"; refs; [ "$rc" = 1 ] || fail "$1 must fail (rc=$rc): $(cat "$tmp/err")"; grep -q "$2" "$tmp/err" || fail "$1: message lacks '$2': $(cat "$tmp/err")"; }

fresh; refs; [ "$rc" = 0 ] || fail "the repository's own kustomizations must pass: $(cat "$tmp/err")"
grep -q 'kustomizations' "$tmp/out" || fail "no summary"

bad "a remote base" 'not a plain relative path' '/^resources:/ { print; print "  - github.com/attacker/repo//k8s?ref=main"; next } { print }'
bad "an https base" 'not a plain relative path' '/^resources:/ { print; print "  - https://example.com/x.yaml"; next } { print }'
bad "a git:: base" 'not a plain relative path' '/^resources:/ { print; print "  - git::ssh://git@host/r.git//p"; next } { print }'
bad "a path out of deploy/" 'outside' '/^resources:/ { print; print "  - ../../../../backend/internal/db"; next } { print }'
bad "a path out of deploy/ in flow style" 'outside' '{ print } END { print "bases: [\"../../../../backend\"]" }'
bad "a missing resource" 'does not exist' '/^resources:/ { print; print "  - nothing.yaml"; next } { print }'
bad "a directory without kustomization.yaml" 'without kustomization.yaml' '/^resources:/ { print; print "  - ../../base"; next } { print }'
bad "a json resource" 'neither a directory' '/^resources:/ { print; print "  - ../../../rabbitmq/dev/definitions.json"; next } { print }'
bad "a patch out of deploy/" 'outside' '{ print } END { print "patches:"; print "  - path: ../../../../backend/evil.yaml" }'
bad "a patch via patchesStrategicMerge out of deploy/" 'outside' '{ print } END { print "patchesStrategicMerge: [../../../../backend/evil.yaml]" }'
bad "a generator file out of deploy/" 'outside' '{ print } END { print "configMapGenerator:"; print "  - name: x"; print "    files: [k=../../../../backend/go.mod]" }'
bad "a generator plugin" 'is not allowed' '{ print } END { print "generators: [gen.yaml]" }'
bad "a helm chart" 'is not allowed' '{ print } END { print "helmCharts: [{name: x}]" }'
# A symlink that leaves deploy/.
fresh; ln -s ../../../../../backend deploy/k8s/services/api/escape; awk '/^resources:/ { print; print "  - escape"; next } { print }' "$K" > "$K.new"; mv "$K.new" "$K"
refs; [ "$rc" = 1 ] && grep -q 'outside' "$tmp/err" || fail "a symlink out of deploy/ must fail (rc=$rc): $(cat "$tmp/err")"
# The file named Kustomization (the byte check refuses the name; the walker still reads it).
fresh; cp "$K" deploy/k8s/services/api/Kustomization; echo "  - github.com/x/y" >> deploy/k8s/services/api/Kustomization
refs; [ "$rc" = 1 ] || fail "a file named Kustomization is read too (rc=$rc)"
# No kustomization at all: fails closed.
fresh; rc=0; ./scripts/check_kustomize_refs.sh /nonexistent >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "nothing to check must exit 2 (rc=$rc)"

# Argo CD's directory source applies .yml, .json and .jsonnet from apps/ too.
fresh; ./scripts/check_argocd.sh >/dev/null 2>&1 || fail "the repository's own apps must pass"
for n in app.json app.yml app.jsonnet notes.txt .hidden.yaml; do
  fresh; cp deploy/argocd/apps/vault-api.yaml "deploy/argocd/apps/$n"
  rc=0; ./scripts/check_argocd.sh >"$tmp/out" 2>&1 || rc=$?
  { [ "$rc" = 1 ] && grep -q 'only regular \*.yaml files' "$tmp/out"; } || fail "apps/$n must fail (rc=$rc): $(cat "$tmp/out")"
done
fresh; mkdir deploy/argocd/apps/sub; rc=0; ./scripts/check_argocd.sh >"$tmp/out" 2>&1 || rc=$?; [ "$rc" = 1 ] || fail "a directory in apps/ must fail (rc=$rc)"
echo "✓ check_kustomize_refs_test: remote bases, paths out of deploy/, missing files, plugins, symlinks and non-yaml files in apps/ fail"
