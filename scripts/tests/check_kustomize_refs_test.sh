#!/usr/bin/env bash
# shellcheck disable=SC2154,SC2015,SC2016
# scripts/check_kustomize_refs.sh and scripts/check_argocd.sh on copies of deploy/: the key and
# tag ALLOWLISTS (case variants, Unicode folding, !!binary, <<, anchors, a second document, a
# remote base under any spelling), symlinks, reference extensions, paths out of deploy/, and
# Argo CD applications that carry anything but one plain spec.source. The repository's own
# deploy/ passes.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_kustomize_refs_test: $*" >&2; exit 1; }
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  if [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI"; fi
  echo "- check_kustomize_refs_test: SKIPPED, yq (mikefarah v4) is not installed locally (brew install yq). CI does not skip."
  exit 0
fi
fresh() {
  rm -rf "$tmp/r"; mkdir -p "$tmp/r/scripts/lib"; cp -R "$HERE/deploy" "$tmp/r/deploy"
  cp "$HERE/scripts/check_kustomize_refs.sh" "$HERE/scripts/check_argocd.sh" "$tmp/r/scripts/"
  cp "$HERE"/scripts/lib/{yamlpolicy,check_kustomize_refs,check_argocd}.py "$tmp/r/scripts/lib/"
  cd "$tmp/r"
}
refs() { rc=0; ./scripts/check_kustomize_refs.sh >"$tmp/out" 2>"$tmp/err" || rc=$?; }
argo() { rc=0; ./scripts/check_argocd.sh >"$tmp/out" 2>&1 || rc=$?; cp "$tmp/out" "$tmp/err"; }
expect() { # <what> <message fragment>: the last run failed with exit 1 and said this
  [ "$rc" = 1 ] || fail "$1 must fail (rc=$rc): $(cat "$tmp/err")"
  grep -q -- "$2" "$tmp/err" || fail "$1: message lacks '$2': $(cat "$tmp/err")"
}
K=deploy/k8s/services/api/kustomization.yaml
A=deploy/argocd/apps/ryft-platform.yaml
# bad <name> <expected message> <awk program on $K>
bad() { fresh; awk "$3" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "$1" "$2"; }
# badapp <name> <expected message> <awk program on $A>
badapp() { fresh; awk "$3" "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "$1" "$2"; }
# Same key text, written to the end of the kustomization.
append() { fresh; printf '%b\n' "$3" >> "$K"; refs; expect "$1" "$2"; }

fresh; refs; [ "$rc" = 0 ] || fail "the repository's own kustomizations must pass: $(cat "$tmp/err")"
grep -q 'kustomizations' "$tmp/out" || fail "no summary"

# --- references ---
bad "a remote base" 'not a plain relative path' '/^resources:/ { print; print "  - github.com/attacker/repo//k8s?ref=main"; next } { print }'
bad "an https base" 'not a plain relative path' '/^resources:/ { print; print "  - https://example.com/x.yaml"; next } { print }'
bad "a git:: base" 'not a plain relative path' '/^resources:/ { print; print "  - git::ssh://git@host/r.git//p"; next } { print }'
bad "a file:// base" 'not a plain relative path' '/^resources:/ { print; print "  - file:///tmp/x.git//ryft?ref=main"; next } { print }'
bad "a path out of deploy/" 'outside' '/^resources:/ { print; print "  - ../../../../backend/internal/db"; next } { print }'
bad "a missing resource" 'does not exist' '/^resources:/ { print; print "  - nothing.yaml"; next } { print }'
bad "a directory without kustomization.yaml" 'without a regular kustomization.yaml' '/^resources:/ { print; print "  - ../../base"; next } { print }'
bad "a json resource" 'neither a directory' '/^resources:/ { print; print "  - ../../../rabbitmq/dev/definitions.json"; next } { print }'
fresh; printf 'kind: ConfigMap\n' > deploy/k8s/services/api/x.png; awk '/^resources:/ { print; print "  - x.png"; next } { print }' "$K" > "$K.new"; mv "$K.new" "$K"
refs; expect "a png resource" 'neither a directory'
append "a patch out of deploy/" 'outside' 'patches:\n  - path: ../../../../backend/evil.yaml'
append "a patch that is not yaml or json" 'not a \*.yaml' 'patches:\n  - path: ../../base/api/config.env'
append "an envs input that is a png" 'not a \*.env' 'configMapGenerator:\n  - name: x\n    envs: [defaults.png]'
append "an envs input out of deploy/" 'outside' 'configMapGenerator:\n  - name: x\n    envs: [../../../../backend/x.env]'

# --- key allowlist: exact, case-sensitive, nothing else ---
bad "Resources: (K1)" "key 'Resources' is not allowed" '/^resources:/ { print "Resources:"; next } { print }'
bad "RESOURCES:" "key 'RESOURCES' is not allowed" '/^resources:/ { print "RESOURCES:"; next } { print }'
fresh; ls_=$(printf 're\xc5\xbfources'); awk -v k="$ls_:" '/^resources:/ { print k; next } { print }' "$K" > "$K.new"; mv "$K.new" "$K"; refs
expect "a long-s resources: key (kustomize folds it)" 'is not allowed'
fresh; awk '/^resources:/ { print "Resources:"; print "  - file:///tmp/x.git//ryft?ref=main"; print "resources:"; next } { print }' "$K" > "$K.new"; mv "$K.new" "$K"; refs
expect "a remote base under Resources: next to resources: (K3)" "key 'Resources' is not allowed"
append "a quoted key" "is not allowed" '"Bases": [../../base]'
fresh; printf '{apiVersion: kustomize.config.k8s.io/v1beta1, kind: Kustomization, "Resources": [x]}\n' > "$K"; refs; expect "a flow-style document with Resources" "key 'Resources' is not allowed"
append "bases" "key 'bases' is not allowed" 'bases: [../../base]'
append "components" "key 'components' is not allowed" 'components: [x]'
append "crds" "key 'crds' is not allowed" 'crds: [x.json]'
append "configurations" "key 'configurations' is not allowed" 'configurations: [x.yaml]'
append "patchesStrategicMerge" "key 'patchesStrategicMerge' is not allowed" 'patchesStrategicMerge: [x.yaml]'
append "patchesJson6902" "key 'patchesJson6902' is not allowed" 'patchesJson6902: [{path: x.json}]'
append "replacements" "key 'replacements' is not allowed" 'replacements: [{path: x.yaml}]'
append "openapi" "key 'openapi' is not allowed" 'openapi: {path: x.json}'
append "a generator plugin" "key 'generators' is not allowed" 'generators: [gen.yaml]'
append "transformers" "key 'transformers' is not allowed" 'transformers: [t.yaml]'
append "a helm chart" "key 'helmCharts' is not allowed" 'helmCharts: [{name: x}]'
append "namePrefix" "key 'namePrefix' is not allowed" 'namePrefix: x-'
append "commonLabels" "key 'commonLabels' is not allowed" 'commonLabels: {a: b}'
append "a generator file input" "key 'files' is not allowed" 'configMapGenerator:\n  - name: x\n    files: [k=../../../../backend/go.mod]'
append "a generator env (singular)" "key 'env' is not allowed" 'configMapGenerator:\n  - name: x\n    env: config.env'
append "an images newName" "key 'newName' is not allowed" 'images:\n  - name: ghcr.io/timkrebs/ryft/api\n    newName: docker.io/attacker/api\n    newTag: "abc1234"'
append "a patch target selector" "key 'labelSelector' is not allowed" 'patches:\n  - patch: "[]"\n    target: {labelSelector: a=b}'
append "a patch key" "key 'options' is not allowed" 'patches:\n  - patch: "[]"\n    options: {allowNameChange: true}'
append "an unknown generator option" "key 'annotations' is not allowed" 'configMapGenerator:\n  - name: x\n    options: {annotations: {a: b}}'
append "a wrong apiVersion" "apiVersion" 'apiVersion: kustomize.config.k8s.io/v1'
append "a wrong kind" "kind" 'kind: Component'
append "a number where a string is" "must be a string" 'images:\n  - name: ghcr.io/timkrebs/ryft/api\n    newTag: 1234567'
append "a duplicate key" "duplicate key" 'namespace: ryft-api\nnamespace: ryft-api'

# --- YAML tags, anchors, documents ---
append "a !!binary key (kustomize decodes it to resources)" '!!binary' '!!binary cmVzb3VyY2Vz: [x]'
append "a !!binary value" '!!binary' 'namespace: !!binary cnlmdC1hcGk='
append "a custom tag" "tag '!x'" 'namespace: !x ryft-api'
append "a !!float" '!!float' 'images:\n  - name: ghcr.io/timkrebs/ryft/api\n    newTag: !!float 1.5'
append "a !!timestamp" '!!timestamp' 'namespace: !!timestamp 2026-01-01'
append "a merge key" '!!merge' 'x: &a {p: 1}\ny:\n  <<: *a'
append "an anchor" 'anchor' 'namespace: &n ryft-api'
append "an alias" 'alias' 'x: &a b\ny: *a'
append "a second document" "exactly one" '---\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nResources: [x]'

# --- files ---
# A symlink that leaves deploy/.
fresh; ln -s ../../../../../backend deploy/k8s/services/api/escape; awk '/^resources:/ { print; print "  - escape"; next } { print }' "$K" > "$K.new"; mv "$K.new" "$K"
refs; expect "a symlink out of deploy/" 'outside'
# A symlink inside deploy/ (the image of the 2026-10-22 round: a png that is a kustomization).
fresh; ln -s kustomization.yaml deploy/k8s/services/api/icon.png; refs; expect "a symlink named .png under deploy/" 'symlink'
fresh; rm "$K"; cp deploy/k8s/overlays/eks/events.yaml deploy/k8s/services/api/og.png; ln -s og.png "$K"; refs; expect "a symlinked kustomization" 'symlink'
fresh; ln -s ../../base/api deploy/k8s/services/api/baselink; awk '/^resources:/ { print; print "  - baselink"; next } { print }' "$K" > "$K.new"; mv "$K.new" "$K"
refs; expect "a resource directory that is a symlink" 'symlink'
# The names kustomize also reads.
fresh; cp "$K" deploy/k8s/services/api/Kustomization; echo "  - github.com/x/y" >> deploy/k8s/services/api/Kustomization
refs; expect "a file named Kustomization" 'is not allowed; use kustomization.yaml'
fresh; cp "$K" deploy/k8s/services/api/kustomization.yml; refs; expect "a file named kustomization.yml" 'is not allowed; use kustomization.yaml'
# No kustomization at all: fails closed.
fresh; rc=0; ./scripts/check_kustomize_refs.sh /nonexistent >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "nothing to check must exit 2 (rc=$rc)"
# No yq: fails closed.
fresh; rc=0; PATH=/usr/bin:/bin ./scripts/check_kustomize_refs.sh >/dev/null 2>&1 || rc=$?
if ! PATH=/usr/bin:/bin command -v yq >/dev/null 2>&1; then [ "$rc" = 2 ] || fail "without yq the check must exit 2 (rc=$rc)"; fi

# --- Argo CD ---
fresh; argo; [ "$rc" = 0 ] || fail "the repository's own apps must pass: $(cat "$tmp/out")"
# Argo CD's directory source applies .yml, .json and .jsonnet from apps/ too.
for n in app.json app.yml app.jsonnet notes.txt .hidden.yaml; do
  fresh; cp deploy/argocd/apps/vault-api.yaml "deploy/argocd/apps/$n"; argo; expect "apps/$n" 'only regular \*.yaml files'
done
fresh; mkdir deploy/argocd/apps/sub; argo; expect "a directory in apps/" 'only regular'
fresh; cp deploy/k8s/services/api/kustomization.yaml deploy/argocd/apps/kustomization.yaml; argo; expect "a kustomization.yaml in apps/" 'kustomize app'
fresh; ln -s vault-api.yaml deploy/argocd/apps/zz-link.yaml; argo; expect "a symlink in apps/" 'only regular'

# G2: a path outside deploy/ (its files are not covered by any other check).
badapp "an app path outside deploy/" 'below deploy/' '/^    path:/ { print "    path: backend/tools"; next } { print }'
badapp "an app path with .." 'below deploy/' '/^    path:/ { print "    path: deploy/../backend"; next } { print }'
fresh; mkdir -p backend/tools deploy/k8s/linked; ln -s ../../../backend/tools deploy/k8s/linked/x
awk '/^    path:/ { print "    path: deploy/k8s/linked/x"; next } { print }' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "an app path through a symlink" 'symlink'
# G3: a second document in an app file.
fresh; { echo '---'; sed -n '/^apiVersion/,$p' deploy/argocd/apps/vault-smoke.yaml | sed 's/name: vault-smoke/name: zz-extra/'; } > "$tmp/second"; { echo; cat "$tmp/second"; } >> deploy/argocd/apps/vault-smoke.yaml; argo; expect "a second document in an app" 'exactly one'
# G4: kustomize / helm / plugin / directory overrides, and sources.
badapp "source.kustomize" "key 'kustomize' is not allowed" '/^    path:/ { print; print "    kustomize: { images: [\"ghcr.io/timkrebs/ryft/api=docker.io/attacker/api:1\"] }"; next } { print }'
badapp "source.helm" "key 'helm' is not allowed" '/^    path:/ { print; print "    helm: { valueFiles: [x.yaml] }"; next } { print }'
badapp "source.plugin" "key 'plugin' is not allowed" '/^    path:/ { print; print "    plugin: { name: x }"; next } { print }'
badapp "source.directory" "key 'directory' is not allowed" '/^    path:/ { print; print "    directory: { recurse: true }"; next } { print }'
badapp "sources" "key 'sources' is not allowed" '/^  source:/ { print "  sources:"; print "    - repoURL: https://github.com/timkrebs/ryft-platform.git"; print "      path: deploy/k8s/overlays/eks"; print "  source:"; next } { print }'
badapp "a source on another revision" 'targetRevision' '/targetRevision:/ { print "    targetRevision: evil"; next } { print }'
badapp "a source on another repository" 'repoURL' '/repoURL:/ { print "    repoURL: https://github.com/attacker/ryft-platform.git"; next } { print }'
badapp "a Spec key in other case" "key 'Source' is not allowed" '/^  source:/ { print "  Source:"; next } { print }'
badapp "an app with a merge key" '!!merge' '/^  source:/ { print "  x: &a {p: 1}"; print "  y:"; print "    <<: *a" } { print }'
# An app whose path holds a bad kustomization: the reference check runs on every app path.
fresh; printf 'bases: [../../x]\n' >> deploy/k8s/overlays/eks/kustomization.yaml; argo; expect "a bad kustomization under an app path" "key 'bases' is not allowed"
fresh; rm deploy/k8s/overlays/eks/kustomization.yaml; argo; expect "an app path without kustomization.yaml" 'no regular kustomization.yaml'
# root.yaml is read the same way.
fresh; printf '    kustomize: {namePrefix: x}\n' > "$tmp/ins"; awk -v f="$tmp/ins" '/^    path:/ { print; while ((getline l < f) > 0) print l; next } { print }' deploy/argocd/root.yaml > "$tmp/root"; cp "$tmp/root" deploy/argocd/root.yaml
argo; expect "root.yaml with source.kustomize" "key 'kustomize' is not allowed"

echo "✓ check_kustomize_refs_test: key and tag allowlists (case, folding, !!binary, <<, anchors, documents), symlinks, reference types, remote bases, paths out of deploy/ and Argo CD overrides fail"
