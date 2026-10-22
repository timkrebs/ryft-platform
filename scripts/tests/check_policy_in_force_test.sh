#!/usr/bin/env bash
# scripts/check_policy_in_force.sh in a throwaway repository with a stubbed render (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_policy_in_force_test: $*" >&2; exit 1; }
mkdir -p "$tmp/scripts"; cp "$HERE/scripts/check_policy_in_force.sh" "$tmp/scripts/"
cd "$tmp"; git init -q -b main; git config user.email t@example.com; git config user.name test
L=web/content/legal
OV=deploy/k8s/overlays/eks/kustomization.yaml
page() { # <lang> <version> [status]
  mkdir -p "$L/$1/privacy"; printf -- '---\ntitle: "Privacy policy"\nversion: %s\nstatus: %s\n---\n\ntext\n' "'$2'" "${3:-published}" > "$L/$1/privacy/$2.mdx"
}
overlay() { mkdir -p "$(dirname "$OV")"; printf 'configMapGenerator:\n  - { name: ryft-config, namespace: ryft-api, behavior: merge, literals: [PRIVACY_POLICY_VERSION=%s] }\n' "$1" > "$OV"; }
mkdir -p $L; echo '["2026-10-20","2026-11-02","2026-12-01","2026-11-31"]' > $L/privacy-versions.json
page en 2026-10-20 draft; page de 2026-10-20 draft
git add -A; git commit -qm "first"; first=$(git rev-parse HEAD | cut -c1-7)
page en 2026-11-02; page de 2026-11-02
git add -A; git commit -qm "new text"; second=$(git rev-parse HEAD | cut -c1-7)
page en 2026-12-01; git add -A; git commit -qm "only en"; en_only=$(git rev-parse HEAD | cut -c1-7)
page de 2026-12-01; page en 2026-11-31; page de 2026-11-31 draft; git add -A; git commit -qm "both"; both=$(git rev-parse HEAD | cut -c1-7)

# render <api-value> <ingest-value> <web-tag> [api env block] [ingest cm name in the Deployment] [ingest data extra]
render() {
  cat > "$tmp/rendered.yaml" <<EOF
apiVersion: v1
data:
  PRIVACY_POLICY_VERSION: $1
kind: ConfigMap
metadata:
  name: ryft-config-aaaaaaaaaa
  namespace: ryft-api
---
apiVersion: v1
data:
${6:-  OTHER: x}
  PRIVACY_POLICY_VERSION: $2
kind: ConfigMap
metadata:
  name: ryft-config-bbbbbbbbbb
  namespace: ryft-ingest
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: ryft-api
spec:
  template:
    spec:
      containers:
      - envFrom:
        - configMapRef:
            name: ryft-config-aaaaaaaaaa
        - secretRef:
            name: ryft-secrets
${4:-        name: api}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ingest
  namespace: ryft-ingest
spec:
  template:
    spec:
      containers:
      - envFrom:
        - configMapRef:
            name: ${5:-ryft-config-bbbbbbbbbb}
        name: ingest
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web
  namespace: ryft-web
spec:
  template:
    spec:
      containers:
      - image: ghcr.io/timkrebs/ryft/web:$3
        name: web
EOF
}
export RENDER_CMD="cat $tmp/rendered.yaml"
ok()  { ./scripts/check_policy_in_force.sh "${BASE:-}" >"$tmp/out" 2>&1 || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { if ./scripts/check_policy_in_force.sh "${BASE:-}" >"$tmp/out" 2>&1; then fail "should fail: $1"; fi; grep -q "$2" "$tmp/out" || fail "$1: message lacks '$2': $(cat "$tmp/out")"; }

render '"2026-10-20"' '"2026-10-20"' "\"$first\"";  ok "the first version with the first page"
render '"2026-11-02"' '"2026-11-02"' "\"$second\""; ok "a new version with the web commit whose newest page it is"
render '"2026-11-02"' '"2026-11-02"' "$second";     ok "an unquoted tag"
render '"2026-11-02"' '"2026-10-20"' "\"$second\""; bad "api and ingest differ" "must agree"
render "'\"2026-11-02\"'" "'\"2026-11-02\"'" "\"$second\""; bad "a quoted value" "bare YYYY-MM-DD"
render '"2026-11-02 # x"' '"2026-11-02 # x"' "\"$second\""; bad "an inline comment" "bare YYYY-MM-DD"
render '"2026-11-31"' '"2026-11-31"' "\"$both\"";   bad "a date that does not exist" "real calendar date"
render '"2026-12-31"' '"2026-12-31"' "\"$second\""; bad "an unlisted version" "not in"
render '"2026-11-02"' '"2026-11-02"' "\"$first\"";  bad "web rolled back past the version" "no en page"
render '"2026-12-01"' '"2026-12-01"' "\"$en_only\""; bad "the German page is missing" "no de page"
render '"2026-10-20"' '"2026-10-20"' "\"$second\""; bad "a newer page is served than the one in force" "serves 2026-11-02"
render '"2026-10-20"' '"2026-10-20"' "\"$both\"";   bad "a newer page in en and de than the one in force" "serves 2026-12-01\|serves 2026-11-31"
render '"2026-10-20"' '"2026-10-20"' '"f437747"';   ok "a pin outside this history with the first version"
render '"2026-11-02"' '"2026-11-02"' '"f437747"';   bad "a pin outside this history with a newer version" "not a commit of this history"
# the stand-ins the check must not be fooled by
render '"2026-11-02"' '"2026-11-02"' "\"$second\"" '        env:
        - name: PRIVACY_POLICY_VERSION
          value: "2026-10-20"
        name: api'
bad "a container env entry overrides the ConfigMap" "overrides the ConfigMap"
render '"2026-11-02"' '"2026-11-02"' "\"$second\"" '' ryft-config-cccccccccc
bad "the Deployment reads another ConfigMap" "not the checked"
render '"2026-11-02"' '"2026-11-02"' "\"$second\"" '' '' '  namespace: ryft-api'
ok "a data key named namespace does not change which namespace the ConfigMap is in"
render '"2026-10-20"' '"2026-10-20"' '"f437747"'; sed -i.bak '/^  namespace: ryft-ingest/d' "$tmp/rendered.yaml"; rm -f "$tmp/rendered.yaml.bak"
bad "no ingest ConfigMap namespace" "no metadata namespace\|renders no PRIVACY_POLICY_VERSION"

# the draft guard: only a CHANGE of the version in force to a draft page is refused
overlay 2026-10-20; git add -A; git commit -qm "overlay at 2026-10-20"; git branch -q -f base HEAD; BASE=base
render '"2026-10-20"' '"2026-10-20"' "\"$first\"";  ok "an unchanged version in force that is a draft"
echo '["2026-10-20","2026-11-02","2026-12-01","2026-11-31","2027-01-01","2027-02-01"]' > $L/privacy-versions.json
page en 2027-01-01; page de 2027-01-01; git add -A; git commit -qm "published 2027-01-01"; pub=$(git rev-parse HEAD | cut -c1-7)
render '"2027-01-01"' '"2027-01-01"' "\"$pub\""; ok "a change to a published page"
page en 2027-02-01; page de 2027-02-01 draft; git add -A; git commit -qm "de draft"; dr=$(git rev-parse HEAD | cut -c1-7)
render '"2027-02-01"' '"2027-02-01"' "\"$dr\""; bad "a change to a page that is a draft in German" "status: draft"
page de 2027-02-01 "'draft'"; git add -A; git commit -qm "quoted draft"; dq=$(git rev-parse HEAD | cut -c1-7)
render '"2027-02-01"' '"2027-02-01"' "\"$dq\""; bad "a quoted draft status" "status: draft"
BASE=
echo "✓ check_policy_in_force_test: agreement, date shape and calendar, listed, the pinned web commit serves it (newest page), no env override, the checked ConfigMap is the one read, drafts only unchanged"
