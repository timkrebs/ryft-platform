#!/usr/bin/env bash
# shellcheck disable=SC2154,SC2015,SC2016
# scripts/check_kustomize_refs.sh and scripts/check_argocd.sh on a small repository built here (no
# file of the live deploy/ is read or copied, so editing the real tree cannot turn this red or hide a
# regression): the key and tag ALLOWLISTS (case variants, Unicode folding, !!binary, <<, anchors, a
# second document, quoted escapes), remote bases under any spelling (decoy directories included),
# symlinks, reference types, paths out of deploy/, Argo CD applications that carry anything but one
# plain spec.source, and the production invariants of the rendered output. The live deploy/ passes at
# the end (that one reads the real tree on purpose).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_kustomize_refs_test: $*" >&2; exit 1; }
if ! command -v yq >/dev/null 2>&1 || ! yq --version 2>&1 | grep -q mikefarah; then
  if [ -n "${CI:-}" ]; then fail "yq (mikefarah v4) is missing in CI"; fi
  echo "- check_kustomize_refs_test: SKIPPED, yq (mikefarah v4) is not installed locally (brew install yq). CI does not skip."
  exit 0
fi
REPO=https://github.com/timkrebs/ryft-platform.git

# A repository with one workload app and the root app. Every file is written here.
fixture() {
  rm -rf "$tmp/r"; mkdir -p "$tmp/r/scripts/lib" "$tmp/r/deploy/argocd/apps" "$tmp/r/deploy/k8s/base" "$tmp/r/deploy/k8s/svc"
  cp "$HERE/scripts/check_kustomize_refs.sh" "$HERE/scripts/check_argocd.sh" "$tmp/r/scripts/"
  cp "$HERE"/scripts/lib/{yamlpolicy,charpolicy,renderpolicy,check_kustomize_refs,check_argocd}.py "$tmp/r/scripts/lib/"
  cd "$tmp/r"
  cat > deploy/argocd/project.yaml <<EOF
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata: { name: ryft, namespace: argocd }
spec:
  destinations:
    - { server: https://kubernetes.default.svc, namespace: "ryft-*" }
---
apiVersion: argoproj.io/v1alpha1
kind: AppProject
metadata: { name: ryft-root, namespace: argocd }
spec:
  destinations:
    - { server: https://kubernetes.default.svc, namespace: argocd }
EOF
  cat > deploy/argocd/root.yaml <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: ryft-root
  namespace: argocd
spec:
  project: ryft-root
  source:
    repoURL: $REPO
    targetRevision: main
    path: deploy/argocd/apps
  destination:
    server: https://kubernetes.default.svc
    namespace: argocd
  syncPolicy:
    automated: { prune: true, selfHeal: true }
EOF
  cat > deploy/argocd/apps/svc.yaml <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: svc
  namespace: argocd
  annotations:
    argocd.argoproj.io/compare-options: ServerSideDiff=true
    argocd.argoproj.io/sync-wave: "3"
spec:
  project: ryft
  source:
    repoURL: $REPO
    targetRevision: main
    path: deploy/k8s/svc
  destination:
    server: https://kubernetes.default.svc
    namespace: ryft-api
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [ServerSideApply=true, PruneLast=true]
EOF
  cat > deploy/k8s/base/kustomization.yaml <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - api.yaml
  - deny.yaml
configMapGenerator:
  - name: ryft-config
    envs: [config.env]
EOF
  printf 'PORT=8080\nAUTH_MODE=supabase\n' > deploy/k8s/base/config.env
  cat > deploy/k8s/base/api.yaml <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: { name: api }
spec:
  selector: { matchLabels: { app: api } }
  template:
    metadata: { labels: { app: api } }
    spec:
      containers:
        - name: api
          image: ghcr.io/timkrebs/ryft/api:0000000
          env:
            - { name: PORT, value: "8080" }
EOF
  cat > deploy/k8s/base/deny.yaml <<EOF
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: { name: default-deny }
spec:
  podSelector: {}
  policyTypes: [Ingress, Egress]
EOF
  cat > deploy/k8s/svc/extra.yaml <<EOF
apiVersion: v1
kind: Service
metadata: { name: api }
spec: { selector: { app: api }, ports: [{ port: 80 }] }
EOF
  cat > deploy/k8s/svc/kustomization.yaml <<EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: ryft-api
resources:
  - ../base
  - extra.yaml
images:
  - name: ghcr.io/timkrebs/ryft/api
    newTag: "abc1234"
EOF
}
K=deploy/k8s/svc/kustomization.yaml
A=deploy/argocd/apps/svc.yaml
refs() { rc=0; ./scripts/check_kustomize_refs.sh "${1:-deploy/k8s/svc}" >"$tmp/out" 2>"$tmp/err" || rc=$?; }
argo() { rc=0; ./scripts/check_argocd.sh >"$tmp/out" 2>&1 || rc=$?; cp "$tmp/out" "$tmp/err"; }
expect() { # <what> <message fragment>: the last run failed with exit 1, said this, and did not crash
  [ "$rc" = 1 ] || fail "$1 must fail with 1 (rc=$rc): $(cat "$tmp/err")"
  grep -q -- "$2" "$tmp/err" || fail "$1: message lacks '$2': $(cat "$tmp/err")"
  ! grep -q Traceback "$tmp/err" || fail "$1: a traceback instead of findings: $(cat "$tmp/err")"
}
# append <name> <message> <text>: text added to the end of the kustomization
append() { fixture; printf '%b\n' "$3" >> "$K"; refs; expect "$1" "$2"; }
# resource <name> <message> <entry>: an extra entry under resources:
resource() { fixture; sed "s|^  - extra.yaml|  - extra.yaml\n  - $3|" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "$1" "$2"; }
# subk <name> <message> <sed expression on the kustomization>
subk() { fixture; sed "$3" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "$1" "$2"; }
# badapp <name> <message> <line after which / text>: lines inserted after `    path:` of the app
badapp() { fixture; printf '%b\n' "$3" > "$tmp/ins"; awk -v f="$tmp/ins" '{ print } /^    path:/ { while ((getline l < f) > 0) print l }' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "$1" "$2"; }
# renders <name> <message> <file> <text>: text appended to a file of the fixture, then check_argocd
renders() { fixture; printf '%b\n' "$4" >> "$3"; argo; expect "$1" "$2"; }

fixture; refs; [ "$rc" = 0 ] || fail "the fixture's own kustomizations must pass: $(cat "$tmp/err")"
grep -q 'kustomizations' "$tmp/out" || fail "no summary"
argo; [ "$rc" = 0 ] || fail "the fixture's own apps must pass: $(cat "$tmp/err")"

# --- references: remote bases under any spelling, and decoy directories of the same name ---
resource "a remote base" 'not a plain relative path' 'https://example.com/x.yaml'
resource "a git:: base" 'not a plain relative path' 'git::ssh://git@host/r.git//p'
resource "a file:// base" 'not a plain relative path' 'file:///tmp/x.git//ryft?ref=main'
resource "github.com/..?ref= (not a plain path)" 'not a plain relative path' 'github.com/attacker/repo//k8s?ref=main'
# Every decoy at once: each directory exists with a kustomization, and each must be named in a finding.
fixture
for d in github.com/a/b GitHub.com/a/b GITHUB.COM/a/b gitlab.com/a/b example.org/x.git; do
  mkdir -p "deploy/k8s/svc/$d"; cp deploy/k8s/base/kustomization.yaml "deploy/k8s/svc/$d/"
  sed "s|^  - extra.yaml|  - extra.yaml\n  - $d|" "$K" > "$K.new"; mv "$K.new" "$K"
done
refs; expect "the decoy directories" 'contains a dot'
for d in github.com/a/b GitHub.com/a/b GITHUB.COM/a/b gitlab.com/a/b example.org/x.git; do grep -qF "'$d'" "$tmp/err" || fail "the decoy directory $d is not refused: $(cat "$tmp/err")"; done
fixture; mkdir -p deploy/k8s/svc/repo.git; sed "s|^  - extra.yaml|  - extra.yaml\n  - repo.git|" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "a directory named x.git" 'contains a dot'
fixture; cp deploy/k8s/svc/extra.yaml deploy/k8s/svc/more.yaml; sed "s|^  - extra.yaml|  - extra.yaml\n  - more.yaml|" "$K" > "$K.new"; mv "$K.new" "$K"; refs; [ "$rc" = 0 ] || fail "a plain extra *.yaml file in the directory is fine: $(cat "$tmp/err")"
resource "a path out of deploy/" 'outside' '../../../../backend/internal/db'
resource "a missing resource" 'does not exist' 'nothing.yaml'
resource "a trailing slash" 'leading, trailing or doubled slash' '../base/'
resource "a doubled slash" 'leading, trailing or doubled slash' '..//base'
resource "a directory without kustomization.yaml" 'without a regular kustomization.yaml' '../../argocd'
fixture; printf '{}\n' > deploy/k8s/svc/x.json; sed "s|^  - extra.yaml|  - extra.yaml\n  - x.json|" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "a json resource" 'contains a dot'
fixture; printf 'kind: ConfigMap\n' > deploy/k8s/svc/x.png; sed "s|^  - extra.yaml|  - extra.yaml\n  - x.png|" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "a png resource" 'contains a dot'
append "a patch out of deploy/" 'outside' 'patches:\n  - path: ../../../../backend/evil.yaml'
append "a patch that is not yaml or json" 'not a \*.yaml' 'patches:\n  - path: ../base/config.env'
append "an envs input that is a png" 'not a \*.env' 'configMapGenerator:\n  - name: x\n    envs: [defaults.png]'
append "an envs input out of deploy/" 'outside' 'configMapGenerator:\n  - name: x\n    envs: [../../../../backend/x.env]'

# --- key allowlist: exact, case-sensitive, nothing else ---
fixture; sed 's/^resources:/Resources:/' "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "Resources:" "key 'Resources' is not allowed"
fixture; sed 's/^resources:/RESOURCES:/' "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "RESOURCES:" "key 'RESOURCES' is not allowed"
fixture; ls_=$(printf 're\xc5\xbfources'); sed "s/^resources:/$ls_:/" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "a long-s (U+017F) resources: key" 'is not allowed'
fixture; printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nResources:\n  - file:///tmp/x.git//ryft?ref=main\nresources: [../base]\n' > "$K"; refs; expect "a remote base under Resources: next to resources:" "key 'Resources' is not allowed"
fixture; printf '{apiVersion: kustomize.config.k8s.io/v1beta1, kind: Kustomization, "Resources": [x]}\n' > "$K"; refs; expect "a flow-style document with Resources" "key 'Resources' is not allowed"
# Every key kustomize reads that the repository does not use, at once: each one is named in a finding.
fixture; keys="bases components crds configurations patchesStrategicMerge patchesJson6902 replacements openapi generators transformers helmCharts namePrefix commonLabels"
for k in $keys; do printf '%s: [x]\n' "$k" >> "$K"; done
refs; expect "unused kustomize keys" 'is not allowed'
for k in $keys; do grep -q "key '$k' is not allowed" "$tmp/err" || fail "the key $k is not refused: $(cat "$tmp/err")"; done
append "a generator file input" "key 'files' is not allowed" 'configMapGenerator:\n  - name: x\n    files: [k=../../../../backend/go.mod]'
append "a generator env (singular)" "key 'env' is not allowed" 'configMapGenerator:\n  - name: x\n    env: config.env'
subk "an images newName" "key 'newName' is not allowed" 's|^    newTag:|    newName: docker.io/attacker/api\n    newTag:|'
append "a patch target selector" "key 'labelSelector' is not allowed" 'patches:\n  - patch: |-\n      []\n    target: {labelSelector: a=b}'
append "a patch key" "key 'options' is not allowed" 'patches:\n  - patch: |-\n      []\n    options: {allowNameChange: true}'
append "an unknown generator option" "key 'annotations' is not allowed" 'configMapGenerator:\n  - name: x\n    options: {annotations: {a: b}}'
append "a wrong apiVersion" "apiVersion" 'apiVersion: kustomize.config.k8s.io/v1'
append "a wrong kind" "kind" 'kind: Component'
subk "a number where a string is" "must be a string" 's|newTag: .*|newTag: 1234567|'
append "a duplicate key" "duplicate key" 'namespace: ryft-api\nnamespace: ryft-api'
append "labels in selectors" "must be False" 'labels:\n  - pairs: { ryft.dev/stage: first }\n    includeSelectors: true'
append "another label pair" "key 'tier' is not allowed" 'labels:\n  - pairs: { tier: x }\n    includeSelectors: false'
fixture; printf 'FROM_THE_RENDERERS_ENVIRONMENT\n' >> deploy/k8s/base/config.env; refs; expect "an env line without =" "has no '='"
fixture; printf '# a comment\n\nKEY=\n' >> deploy/k8s/base/config.env; refs; [ "$rc" = 0 ] || fail "comments, blank lines and KEY= are fine: $(cat "$tmp/err")"
# A wrong value TYPE is a finding that names the file, not a traceback that hides the rest.
append "an empty patches:" "must be a list" 'patches:'
append "an empty labels pairs" "must be a mapping" 'labels:\n  - pairs:'
fixture; printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: [../base]\npatches:\nimages:\n' > "$K"; mkdir -p deploy/k8s/two; cp deploy/k8s/base/kustomization.yaml deploy/k8s/two/; printf 'bases: [x]\n' >> deploy/k8s/two/kustomization.yaml
refs deploy/k8s; expect "two broken files" "must be a list"; grep -q "two/kustomization.yaml" "$tmp/err" || fail "the second file's finding is still reported (no early crash): $(cat "$tmp/err")"

# --- YAML tags, anchors, documents, quoted escapes ---
append "a !!binary key (kustomize decodes it to resources)" '!!binary' '!!binary cmVzb3VyY2Vz: [x]'
append "a !!binary value" '!!binary' 'namespace: !!binary cnlmdC1hcGk='
append "a custom tag" "tag '!x'" 'namespace: !x ryft-api'
subk "a !!float" '!!float' 's|newTag: .*|newTag: !!float 1.5|'
append "a !!timestamp" '!!timestamp' 'namespace: !!timestamp 2026-01-01'
append "a merge key" '!!merge' 'x: &a {p: 1}\ny:\n  <<: *a'
append "an anchor" 'anchor' 'namespace: &n ryft-api'
append "an alias" 'alias' 'x: &a b\ny: *a'
append "a second document" "exactly one" '---\napiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nResources: [x]'
append "a trailing ---" "trailing ---" '---'
# A double-quoted patch decodes escapes (\L, \N, \_, \x85 and \P are line breaks and a no-break space).
fixture
for esc in '\\L' '\\N' '\\_' '\\x85' '\\P'; do
  printf 'patches:\n  - target: { kind: NetworkPolicy, name: default-deny }\n    patch: "- op: add\\\\n  path: /metadata/annotations\\\\n  value: {a: b}  # one op%b- op: add\\\\n  path: /spec/egress\\\\n  value: [{}]"\n' "$esc" >> "$K.p"
done
# one `patches:` key with five entries
{ echo patches:; grep -v '^patches:' "$K.p"; } >> "$K"; rm -f "$K.p"
refs; expect "double-quoted patches with escapes" 'literal block'
for u in 2028 0085 00A0 2029; do grep -q "U+$u" "$tmp/err" || fail "the escape that decodes to U+$u is not seen: $(cat "$tmp/err")"; done
append "a single-quoted patch" 'literal block' "patches:\n  - target: { kind: NetworkPolicy, name: default-deny }\n    patch: '[]'"
append "a folded patch" 'literal block' 'patches:\n  - patch: >-\n      []'
fixture; printf 'apiVersion: v1\nkind: ConfigMap\nmetadata: { name: x }\ndata: { a: "one\\Ltwo" }\n' > deploy/k8s/base/leak.yaml; refs deploy; expect "an escaped line break in any YAML under deploy/" 'decoded text holds U+2028'
fixture; printf 'apiVersion: v1\nkind: ConfigMap\nmetadata: { name: x }\ndata: { a: "one\\x85two", b: "\\u3164" }\n' > deploy/k8s/base/leak.yaml; refs deploy; expect "an escaped NEL in any YAML under deploy/" 'decoded text holds U+0085'
fixture; printf '{"a": "x\\u2028y"}\n' > deploy/k8s/base/leak.json; refs deploy; expect "an escaped line break in a JSON under deploy/" 'decoded text holds U+2028'

# --- the schema and the walk cannot drift apart ---
fixture
python3 -B -I - "$HERE/scripts/lib" <<'PY' || fail "a path-typed schema field is not followed by the reference walk"
import sys
sys.path.insert(0, sys.argv[1])
import yamlpolicy as y
schema = y.Map({"files": y.List(y.Path("file")), "resources": y.List(y.Path("res"))})
errs, paths = [], []
y.validate({"files": ["k=../x"], "resources": ["../r"]}, schema, "", errs, paths)
assert paths == [("file", "k=../x", "files[0]"), ("res", "../r", "resources[0]")], paths
# every kind used in the real schema is one that ref() knows
kinds = set()
def walk(s):
    if isinstance(s, y.Path): kinds.add(s.kind)
    for a in ("keys", "item", "value"):
        v = getattr(s, a, None)
        if isinstance(v, dict):
            [walk(x) for x in v.values()]
        elif v is not None:
            walk(v)
walk(y.KUSTOMIZATION)
assert kinds <= {"res", "patch", "env", "file"}, kinds
PY
# A path-carrying key added to the schema is checked: the same fixture with `files` allowed.
sed 's|"envs": List(Path("env")),|"envs": List(Path("env")), "files": List(Path("file")),|' "$HERE/scripts/lib/yamlpolicy.py" > scripts/lib/yamlpolicy.py
printf 'configMapGenerator:\n  - name: x\n    files: [k=../../../../backend/go.mod]\n' >> "$K"; refs; expect "a path-carrying key added to the schema" 'is not a plain relative path'

# --- files ---
fixture; ln -s ../../../../../backend deploy/k8s/svc/escape; sed "s|^  - extra.yaml|  - extra.yaml\n  - escape|" "$K" > "$K.new"; mv "$K.new" "$K"
refs; expect "a symlink out of deploy/" 'outside'
fixture; ln -s kustomization.yaml deploy/k8s/svc/icon.png; refs; expect "a symlink named .png under deploy/" 'symlink'
fixture; mv "$K" deploy/k8s/svc/og.png; ln -s og.png "$K"; refs; expect "a symlinked kustomization" 'symlink'
fixture; ln -s ../base deploy/k8s/svc/baselink; sed "s|^  - extra.yaml|  - extra.yaml\n  - baselink|" "$K" > "$K.new"; mv "$K.new" "$K"; refs; expect "a resource directory that is a symlink" 'symlink'
fixture; cp "$K" deploy/k8s/svc/Kustomization; refs; expect "a file named Kustomization" 'is not allowed; use kustomization.yaml'
fixture; cp "$K" deploy/k8s/svc/kustomization.yml; refs; expect "a file named kustomization.yml" 'is not allowed; use kustomization.yaml'
fixture; rc=0; ./scripts/check_kustomize_refs.sh /nonexistent >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "nothing to check must exit 2 (rc=$rc)"
fixture; rc=0; PATH=/usr/bin:/bin ./scripts/check_kustomize_refs.sh >/dev/null 2>&1 || rc=$?
if ! PATH=/usr/bin:/bin command -v yq >/dev/null 2>&1; then [ "$rc" = 2 ] || fail "without yq the check must exit 2 (rc=$rc)"; fi

# --- Argo CD: files ---
# Argo CD's directory source applies .yml, .json and .jsonnet from apps/ too.
fixture; for n in app.json app.yml app.jsonnet notes.txt .hidden.yaml; do cp "$A" "deploy/argocd/apps/$n"; done; argo
expect "non-yaml files in apps/" 'only regular \*.yaml files'
for n in app.json app.yml app.jsonnet notes.txt .hidden.yaml; do grep -qF "apps/$n" "$tmp/err" || fail "apps/$n is not refused: $(cat "$tmp/err")"; done
fixture; mkdir deploy/argocd/apps/sub; argo; expect "a directory in apps/" 'only regular'
fixture; cp "$K" deploy/argocd/apps/kustomization.yaml; argo; expect "a kustomization.yaml in apps/" 'kustomize app'
fixture; ln -s svc.yaml deploy/argocd/apps/zz-link.yaml; argo; expect "a symlink in apps/" 'only regular'

# --- Argo CD: what an application may say ---
fixture; sed 's|path: deploy/k8s/svc|path: backend/tools|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "an app path outside deploy/" 'below deploy/'
fixture; sed 's|path: deploy/k8s/svc|path: deploy/../backend|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "an app path with .." 'below deploy/'
fixture; mkdir -p backend/tools deploy/k8s/linked; ln -s ../../../backend/tools deploy/k8s/linked/x; sed 's|path: deploy/k8s/svc|path: deploy/k8s/linked/x|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "an app path through a symlink" 'symlink'
fixture; { echo '---'; sed -n '/^apiVersion/,$p' "$A" | sed 's/name: svc/name: zz-extra/'; } > "$tmp/second"; { echo; cat "$tmp/second"; } >> "$A"; argo; expect "a second document in an app" 'exactly one'
badapp "source.kustomize" "key 'kustomize' is not allowed" '    kustomize: { images: ["ghcr.io/timkrebs/ryft/api=docker.io/attacker/api:1"] }'
badapp "source.helm" "key 'helm' is not allowed" '    helm: { valueFiles: [x.yaml] }'
badapp "source.plugin" "key 'plugin' is not allowed" '    plugin: { name: x }'
badapp "source.directory" "key 'directory' is not allowed" '    directory: { recurse: true }'
fixture; sed 's|^  source:|  sources:\n    - repoURL: x\n  source:|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "sources" "key 'sources' is not allowed"
fixture; sed 's|targetRevision: main|targetRevision: evil|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "a source on another revision" 'targetRevision'
fixture; sed 's|repoURL: .*|repoURL: https://github.com/attacker/ryft-platform.git|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "a source on another repository" 'repoURL'
fixture; sed 's|^  source:|  Source:|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "a Spec key in other case" "key 'Source' is not allowed"
fixture; sed 's|PruneLast=true|PruneLast=false|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "a changed syncOption" 'fixed values are listed in scripts/lib/yamlpolicy.py'
fixture; sed 's|^spec:|spec:\n  x: \&a {p: 1}\n  y:\n    <<: *a|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "an app with a merge key" '!!merge'
# A wrong type is a finding with the file, not a traceback.
fixture; sed 's|^    argocd.argoproj.io/compare-options.*||; s|^    argocd.argoproj.io/sync-wave.*||' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "empty annotations" 'must be a mapping'
fixture; sed 's|^    automated:.*|    automated:|; s|^    automated: .*||' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "an empty automated:" 'must be a mapping'
grep -q 'svc.yaml' "$tmp/err" || fail "the finding names the file: $(cat "$tmp/err")"
# B3: the root project is root.yaml's alone, and no path may render an Application.
fixture; sed 's|project: ryft$|project: ryft-root|; s|namespace: ryft-api|namespace: argocd|' "$A" > "$A.new"; mv "$A.new" "$A"; argo; expect "a second app in the root project" 'for deploy/argocd/root.yaml only'
fixture; cat > deploy/k8s/svc/previews.yaml <<EOF
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: { name: previews, namespace: argocd }
spec:
  project: ryft-root
  source: { repoURL: $REPO, targetRevision: main, path: deploy/k8s/svc, kustomize: { images: ["a=docker.io/x/y:1"] } }
  destination: { server: https://kubernetes.default.svc, namespace: argocd }
EOF
sed "s|^  - extra.yaml|  - extra.yaml\n  - previews.yaml|" "$K" > "$K.new"; mv "$K.new" "$K"; argo; expect "a nested Application rendered by an app path" 'argoproj.io'

# --- B4: the production invariants of the render ---
fixture; sed 's/AUTH_MODE=supabase/AUTH_MODE=apikey/' deploy/k8s/base/config.env > "$tmp/x"; cp "$tmp/x" deploy/k8s/base/config.env; argo; expect "config.env AUTH_MODE=apikey (last wins)" 'AUTH_MODE='
fixture; printf 'ALLOW_INSECURE_APIKEY_AUTH=true\n' >> deploy/k8s/base/config.env; argo; expect "config.env ALLOW_INSECURE_APIKEY_AUTH" 'ALLOW_INSECURE_APIKEY_AUTH'
fixture; printf 'API_KEY=x\n' >> deploy/k8s/base/config.env; argo; expect "config.env API_KEY" 'API_KEY'
fixture; printf 'configMapGenerator:\n  - { name: ryft-config, behavior: merge, literals: [AUTH_MODE=apikey] }\n' >> "$K"; argo; expect "a generator merge that sets AUTH_MODE" 'AUTH_MODE='
fixture; printf '            - { name: API_KEY, value: x }\n' >> deploy/k8s/base/api.yaml; argo; expect "an API_KEY env var" 'environment variable API_KEY'
fixture; printf '            - { name: AUTH_MODE, value: apikey }\n' >> deploy/k8s/base/api.yaml; argo; expect "an AUTH_MODE env var" 'AUTH_MODE='
fixture; printf 'PORT=8080\n' > deploy/k8s/base/config.env; argo; expect "no AUTH_MODE at all in ryft-api" 'no ConfigMap with AUTH_MODE=supabase'
fixture; sed 's|ghcr.io/timkrebs/ryft/api:0000000|docker.io/attacker/miner:1|' deploy/k8s/base/api.yaml > "$tmp/x"; cp "$tmp/x" deploy/k8s/base/api.yaml; argo; expect "a third-party image" 'only our six images'
fixture; sed 's|newTag: "abc1234"|newTag: "latest"|' "$K" > "$K.new"; mv "$K.new" "$K"; argo; expect "the tag latest" 'only our six images'
fixture; sed 's|newTag: "abc1234"|newTag: "abc1234abc"|' "$K" > "$K.new"; mv "$K.new" "$K"; argo; expect "a long hex tag" 'only our six images'
fixture; sed 's|newTag: "abc1234"|newTag: "ABC1234"|' "$K" > "$K.new"; mv "$K.new" "$K"; argo; expect "an upper-case tag" 'only our six images'
fixture; : > deploy/k8s/base/deny.yaml; printf 'apiVersion: v1\nkind: ConfigMap\nmetadata: { name: nothing }\n' > deploy/k8s/base/deny.yaml; argo; expect "no default-deny in a namespace with a workload" 'no intact default-deny'
fixture; printf '  egress:\n    - {}\n' >> deploy/k8s/base/deny.yaml; argo; expect "an allow-all egress on default-deny" 'empty egress rule'
fixture; sed 's/policyTypes: \[Ingress, Egress\]/policyTypes: [Ingress]/' deploy/k8s/base/deny.yaml > "$tmp/x"; cp "$tmp/x" deploy/k8s/base/deny.yaml; argo; expect "default-deny without Egress" 'no intact default-deny'
fixture; sed 's/podSelector: {}/podSelector: { matchLabels: { app: none } }/' deploy/k8s/base/deny.yaml > "$tmp/x"; cp "$tmp/x" deploy/k8s/base/deny.yaml; argo; expect "default-deny that selects nothing" 'no intact default-deny'
fixture; printf 'patches:\n  - target: { kind: NetworkPolicy, name: default-deny }\n    patch: |-\n      - { op: add, path: /spec/ingress, value: [{}] }\n' >> "$K"; argo; expect "default-deny patched to allow all ingress" 'empty ingress rule'
fixture; printf 'patches:\n  - target: { kind: NetworkPolicy, name: default-deny }\n    patch: |-\n      $patch: delete\n      apiVersion: networking.k8s.io/v1\n      kind: NetworkPolicy\n      metadata: { name: default-deny }\n' >> "$K"; argo; expect "default-deny deleted by a patch" 'no intact default-deny'

# --- round 3: shadow apps, project binding, the effective environment, allow-all peers ---
fixture; sed 's|name: svc|name: aa-shadow|; s|project: ryft$|project: ryft-operators|' "$A" > deploy/argocd/apps/aa-shadow.yaml
printf -- '---\napiVersion: argoproj.io/v1alpha1\nkind: AppProject\nmetadata: { name: ryft-operators, namespace: argocd }\nspec:\n  destinations:\n    - { server: https://kubernetes.default.svc, namespace: "ryft-*" }\n' >> deploy/argocd/project.yaml
argo; expect "an operators-project app on a workload path (shadowing the real app)" 'may only deploy'
fixture; sed 's|name: svc|name: zz-twin|' "$A" > deploy/argocd/apps/zz-twin.yaml; argo; expect "two apps on one path" 'one application per path'
fixture; sed 's|project: ryft$|project: default|' "$A" > "$A.new"; mv "$A.new" "$A"; printf -- '---\napiVersion: argoproj.io/v1alpha1\nkind: AppProject\nmetadata: { name: default, namespace: argocd }\nspec:\n  destinations:\n    - { server: "*", namespace: "*" }\n' >> deploy/argocd/project.yaml; argo; expect "a project that is defined but not one of ours" 'is not one of'
fixture; printf '            - { name: USER_ID, value: x }\n' >> deploy/k8s/base/api.yaml; argo; expect "a USER_ID env var" 'environment variable USER_ID'
fixture; printf '          envFrom:\n            - { prefix: A, secretRef: { name: tuning } }\n' >> deploy/k8s/base/api.yaml; printf 'secretGenerator:\n  - name: tuning\n    literals: [UTH_MODE=apikey, PI_KEY=x, LLOW_INSECURE_APIKEY_AUTH=true]\n' >> "$K"; argo; expect "envFrom with a prefix that renames keys" 'envFrom with a prefix'
fixture; printf '            - { name: AUTH_MODE, valueFrom: { configMapKeyRef: { name: ryft-config, key: MODE } } }\n' >> deploy/k8s/base/api.yaml; printf 'MODE=apikey\n' >> deploy/k8s/base/config.env; argo; expect "AUTH_MODE from valueFrom" 'valueFrom'
fixture; printf '            - { name: API_KEY, valueFrom: { secretKeyRef: { name: s, key: k } } }\n' >> deploy/k8s/base/api.yaml; argo; expect "API_KEY from valueFrom" 'valueFrom'
fixture; printf 'secretGenerator:\n  - name: tuning\n    literals: [AUTH_MODE=apikey]\n' >> "$K"; argo; expect "AUTH_MODE as a Secret key (base64 data decoded)" 'AUTH_MODE='
fixture; printf 'secretGenerator:\n  - name: tuning\n    literals: [API_KEY=x]\n' >> "$K"; argo; expect "API_KEY as a Secret key" 'API_KEY'
fixture; printf '  ingress:\n    - from: []\n' >> deploy/k8s/base/deny.yaml; argo; expect "ingress from: []" 'without peers'
fixture; printf '  egress:\n    - to: []\n      ports: [{ port: 443 }]\n' >> deploy/k8s/base/deny.yaml; argo; expect "egress to: []" 'without peers'
fixture; printf '  egress:\n    - ports: [{ port: 443 }]\n' >> deploy/k8s/base/deny.yaml; argo; expect "an egress rule with only ports" 'without peers'
# A trailing newline does not pass a pattern (fullmatch, not match with $).
fixture
python3 -B -I - "$HERE/scripts/lib" <<'PY' || fail "a trailing newline passed a pattern"
import sys
sys.path.insert(0, sys.argv[1])
import renderpolicy as r
assert not r.OUR_IMAGE.fullmatch("ghcr.io/timkrebs/ryft/api:abc1234\n")
assert r.OUR_IMAGE.fullmatch("ghcr.io/timkrebs/ryft/api:abc1234")
PY

# --- git may only use file:// while kustomize renders ---
fixture; mkdir "$tmp/bin"; real_kubectl=$(command -v kubectl)
printf '#!/bin/sh\necho "$GIT_ALLOW_PROTOCOL" >> "%s/protocol"\nexec "%s" "$@"\n' "$tmp" "$real_kubectl" > "$tmp/bin/kubectl"; chmod +x "$tmp/bin/kubectl"
rc=0; PATH="$tmp/bin:$PATH" ./scripts/check_argocd.sh >"$tmp/out" 2>&1 || rc=$?; [ "$rc" = 0 ] || fail "the fixture passes with the wrapper: $(cat "$tmp/out")"
grep -qx file "$tmp/protocol" || fail "kubectl kustomize must run with GIT_ALLOW_PROTOCOL=file: $(cat "$tmp/protocol" 2>&1)"

# --- the repository's own deploy/ passes ---
cd "$HERE"; rc=0; ./scripts/check_kustomize_refs.sh >"$tmp/out" 2>"$tmp/err" || rc=$?; [ "$rc" = 0 ] || fail "the live kustomizations must pass: $(cat "$tmp/err")"
rc=0; ./scripts/check_argocd.sh >"$tmp/out" 2>&1 || rc=$?; [ "$rc" = 0 ] || fail "the live apps must pass: $(cat "$tmp/out")"

echo "✓ check_kustomize_refs_test: key and tag allowlists, decoded escapes, remote bases and decoys, symlinks, reference types, Argo CD overrides and the render's production invariants fail"
