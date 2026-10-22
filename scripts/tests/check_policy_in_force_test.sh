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
render '"2027-02-01"' '"2027-02-01"' "\"$dr\""; bad "a change to a page that is a draft in German" "not status: published"
page de 2027-02-01 "'draft'"; git add -A; git commit -qm "quoted draft"; dq=$(git rev-parse HEAD | cut -c1-7)
render '"2027-02-01"' '"2027-02-01"' "\"$dq\""; bad "a quoted draft status" "not status: published"
# the frontmatter is read the way the site reads it: only exactly "published" (after normalising) passes
n=0
variant() { # <name> <ok|bad> <printf format of the German page>
  n=$((n + 1)); local v; v=$(printf '2028-01-%02d' "$n")
  jq --arg v "$v" '. + [$v]' $L/privacy-versions.json > "$tmp/l.json"; mv "$tmp/l.json" $L/privacy-versions.json
  page en "$v"
  # shellcheck disable=SC2059
  printf -- "$3" > "$L/de/privacy/$v.mdx"; git add -A; git commit -qm "variant $1"
  render "\"$v\"" "\"$v\"" "\"$(git rev-parse HEAD | cut -c1-7)\""
  if [ "$2" = ok ]; then ok "$1"; else bad "$1" "not status: published"; fi
}
variant "status published" ok '---\nstatus: published\n---\ntext\n'
variant "quoted published with a comment" ok '---\r\ntitle: "x"\r\nstatus: "published"  # live\r\n---\r\ntext\r\n'
variant "a BOM" ok '\357\273\277---\nstatus: published\n---\n'
variant "CRLF draft" bad '---\r\nstatus: draft\r\n---\r\ntext\r\n'
variant "a BOM before a draft" bad '\357\273\277---\nstatus: draft\n---\n'
variant "dashes with a trailing space" bad '--- \nstatus: draft\n---\n'
variant "a quoted key" bad '---\n"status": draft\n---\n'
variant "a tagged value" bad '---\nstatus: !!str draft\n---\n'
variant "flow frontmatter" bad '---\n{status: draft}\n---\n'
variant "no status at all" bad '---\ntitle: x\n---\n'
variant "no frontmatter" bad 'status: published\n'
variant "an unknown status" bad '---\nstatus: review\n---\n'
# going back to a version that was in force before (listed before the current one) is allowed, a draft or not
overlay 2027-02-01; git add -A; git commit -qm "overlay at 2027-02-01"; git branch -q -f base HEAD
page de 2026-10-20 draft; page en 2026-10-20 draft; git add -A; git commit -qm "old drafts" >/dev/null || true
render '"2026-10-20"' '"2026-10-20"' '"f437747"'; ok "a change back to a version listed before the one in force (a draft)"
overlay 2026-10-20; git add -A; git commit -qm "overlay back"; git branch -q -f base HEAD
render '"2027-02-01"' '"2027-02-01"' "\"$dr\""; bad "forward again to a draft" "not status: published"
# the person's policy PR: the base version in config.env changes to a page that is not published
BC=deploy/k8s/base
mkdir -p $BC/api $BC/ingest; for s in api ingest; do printf 'PORT=8080\nPRIVACY_POLICY_VERSION=2027-01-01\n' > $BC/$s/config.env; done
git add -A; git commit -qm "base config.env"; git branch -q -f base HEAD
render '"2026-10-20"' '"2026-10-20"' "\"$first\""; ok "config.env unchanged"
for s in api ingest; do printf 'PORT=8080\nPRIVACY_POLICY_VERSION=2027-02-01\n' > $BC/$s/config.env; done
bad "config.env moves to a version whose German page is a draft" "config.env changes PRIVACY_POLICY_VERSION"
page de 2027-02-01; for s in api ingest; do printf 'PORT=8080\nPRIVACY_POLICY_VERSION=2027-01-01\n' > $BC/$s/config.env; done
ok "config.env back to what base has"
# the base ref must resolve and parse
BASE=origin/nope; bad "a base ref that does not resolve" "does not resolve"
BASE=base; git branch -q -f nolit HEAD~3; git show "base:$OV" > /dev/null
printf 'configMapGenerator: []\n' > "$OV"; git add -A; git commit -qm "overlay without literal"; git branch -q -f base HEAD; BASE=base
render '"2026-10-20"' '"2026-10-20"' "\"$first\""; ok "no literal on base yet (the PR that introduces it)"
printf '# PRIVACY_POLICY_VERSION is pinned here\nconfigMapGenerator: []\n' > "$OV"; git add -A; git commit -qm "overlay mentions it"; git branch -q -f base HEAD
bad "the base overlay mentions the key but nothing parses" "does not parse"
# an unknown short SHA is not "foreign"
BASE=
render '"2026-10-20"' '"2026-10-20"' '"abc1234"'; bad "an unknown short SHA" "not exactly one commit"
echo "✓ check_policy_in_force_test: agreement, date shape and calendar, listed, the pinned web commit serves it (newest page), no env override, the checked ConfigMap is the one read, drafts only unchanged"
