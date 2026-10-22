#!/usr/bin/env bash
# scripts/check_policy_in_force.sh in a throwaway repository with a stubbed render (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_policy_in_force_test: $*" >&2; exit 1; }
mkdir -p "$tmp/scripts"; cp "$HERE/scripts/check_policy_in_force.sh" "$tmp/scripts/"
cd "$tmp"; git init -q -b main; git config user.email t@example.com; git config user.name test
L=web/content/legal
page() { mkdir -p "$L/$1/privacy"; echo "# $2" > "$L/$1/privacy/$2.mdx"; }
mkdir -p $L; echo '["2026-10-20","2026-11-02"]' > $L/privacy-versions.json
page en 2026-10-20; page de 2026-10-20
git add -A; git commit -qm "first"; first=$(git rev-parse HEAD | cut -c1-7)
page en 2026-11-02; page de 2026-11-02
git add -A; git commit -qm "new text"; second=$(git rev-parse HEAD | cut -c1-7)
page en 2026-12-01; git add -A; git commit -qm "only en"; en_only=$(git rev-parse HEAD | cut -c1-7)

# render <api-value> <ingest-value> <web-tag>: the parts of the overlay the check reads
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
  PRIVACY_POLICY_VERSION: $2
kind: ConfigMap
metadata:
  name: ryft-config-bbbbbbbbbb
  namespace: ryft-ingest
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
ok()  { ./scripts/check_policy_in_force.sh >"$tmp/out" 2>&1 || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { if ./scripts/check_policy_in_force.sh >"$tmp/out" 2>&1; then fail "should fail: $1"; fi; grep -q "$2" "$tmp/out" || fail "$1: message lacks '$2': $(cat "$tmp/out")"; }

render '"2026-10-20"' '"2026-10-20"' "\"$first\"";  ok "the first version with the first page"
render '"2026-11-02"' '"2026-11-02"' "\"$second\""; ok "a new version with the web commit that carries it"
render '"2026-11-02"' '"2026-11-02"' "$second";     ok "an unquoted tag"
render '"2026-11-02"' '"2026-10-20"' "\"$second\""; bad "api and ingest differ" "must agree"
render "'\"2026-11-02\"'" "'\"2026-11-02\"'" "\"$second\""; bad "a quoted value" "bare YYYY-MM-DD"
render '"2026-11-02 # x"' '"2026-11-02 # x"' "\"$second\""; bad "an inline comment" "bare YYYY-MM-DD"
render '"2026-12-31"' '"2026-12-31"' "\"$second\""; bad "an unlisted version" "not in"
render '"2026-11-02"' '"2026-11-02"' "\"$first\"";  bad "web rolled back past the version" "no en page"
echo '["2026-10-20","2026-11-02","2026-12-01"]' > $L/privacy-versions.json
render '"2026-12-01"' '"2026-12-01"' "\"$en_only\""; bad "the German page is missing" "no de page"
mkdir -p $L; echo '["2026-10-20","2026-11-02"]' > $L/privacy-versions.json
render '"2026-10-20"' '"2026-10-20"' '"f437747"';   ok "a pin outside this history with the first version"
render '"2026-11-02"' '"2026-11-02"' '"f437747"';   bad "a pin outside this history with a newer version" "not a commit of this history"
render '"2026-10-20"' '"2026-10-20"' '"f437747"'; sed -i.bak '/ryft-ingest/d' "$tmp/rendered.yaml"; rm -f "$tmp/rendered.yaml.bak"
bad "no ingest ConfigMap value" "renders no PRIVACY_POLICY_VERSION"
echo "✓ check_policy_in_force_test: agreement, date shape, listed, the pinned web commit carries en and de, unknown pins only with the first version"
