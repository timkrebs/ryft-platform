#!/usr/bin/env bash
# CI's manifests job (not make k8s-validate: it needs the git history): the privacy policy
# version in force on EKS is consistent with the site that is deployed (D78, F1). It renders
# overlays/eks and fails when
#  - api and ingest do not get the same PRIVACY_POLICY_VERSION from their ConfigMap, or their
#    Deployment does not reference the ConfigMap that was checked, or a container env entry of
#    that name would override it,
#  - the value is not a bare, real YYYY-MM-DD date (quotes, spaces and comments are part of a
#    value; Go refuses 2026-11-31),
#  - the version is not in web/content/legal/privacy-versions.json,
#  - the web image's commit does not SERVE the version at /privacy: the site shows the newest
#    page per language (web/src/lib/content.ts), so the newest
#    web/content/legal/{en,de}/privacy/<version>.mdx there must be the version in force. A pin
#    that is not a commit of this history (f437747 was built in the archived ryft-web
#    repository) passes only with the first listed version: that image carries nothing newer,
#  - given a base ref (a pull request): the version in force changes to a page whose
#    frontmatter says status: draft. An unchanged draft stays allowed.
# So rolling web back past a policy version fails until the in-force pin goes back too, and a
# newer page in the pinned web commit fails until the pin follows it.
#   scripts/check_policy_in_force.sh [base-ref]        e.g. origin/main
#   RENDER_CMD  how the overlay is rendered (default: kubectl kustomize deploy/k8s/overlays/eks)
# The render is kustomize's canonical block-style YAML; the parse below reads only the metadata
# block and the keys under it, and fails closed on anything it cannot place.
set -euo pipefail
cd "$(dirname "$0")/.."
render=${RENDER_CMD:-kubectl kustomize deploy/k8s/overlays/eks}
base=${1:-}
list=web/content/legal/privacy-versions.json
overlay=deploy/k8s/overlays/eks/kustomization.yaml
err() { echo "::error title=policy in force::$*" >&2; exit 1; }

rendered=$(mktemp); trap 'rm -f "$rendered"' EXIT
$render > "$rendered" || err "cannot render the EKS overlay"

# Lines: "cm <namespace> <configmap name> <raw value>", "deploy <namespace> <name> <refs, comma-joined>",
# "override <namespace> <name>" (a container entry named like the key), "web <tag>".
found=$(python3 - "$rendered" <<'PY'
import re, sys
KEY = "PRIVACY_POLICY_VERSION"
def top(doc):
    """top-level key -> list of its lines (kustomize output: block style, 2-space indent)."""
    out, cur = {}, None
    for l in doc.split("\n"):
        m = re.match(r"^([A-Za-z][A-Za-z0-9]*):(.*)$", l)
        if m:
            cur = m.group(1); out[cur] = [m.group(2).strip()]
        elif cur is not None:
            out[cur].append(l)
    return out
def child(block, key):
    for l in block[1:]:
        m = re.match(r"^  " + key + r": ?(.*)$", l)
        if m:
            return m.group(1).rstrip()
    return None
for doc in open(sys.argv[1]).read().split("\n---\n"):
    t = top(doc)
    kind = (t.get("kind") or [""])[0]
    meta = t.get("metadata")
    if not kind or meta is None:
        continue
    ns, name = child(meta, "namespace"), child(meta, "name")
    if kind == "ConfigMap" and "data" in t:
        v = child(t["data"], KEY)
        if v is not None:
            if not ns or not name:
                sys.exit("a ConfigMap with the key has no metadata namespace or name")
            if len(v) >= 2 and v[0] == v[-1] and v[0] in "'\"":
                v = v[1:-1]
            print(f"cm {ns} {name} {v}")
    if kind == "Deployment":
        if ns in ("ryft-api", "ryft-ingest"):
            if KEY in doc:
                print(f"override {ns} {name}")
            refs = re.findall(r"configMapRef:\n\s+name: (\S+)", doc)
            print(f"deploy {ns} {name} {','.join(refs)}")
        m = re.search(r"^\s+(?:- )?image: ghcr\.io/timkrebs/ryft/web:(\S+)\s*$", doc, re.M)
        if m:
            print(f"web {m.group(1).strip(chr(34))}")
PY
)
line() { printf '%s\n' "$found" | sed -n "s/^$1 //p" | head -1; }
for ns in ryft-api ryft-ingest; do
  [ -z "$(printf '%s\n' "$found" | sed -n "s/^override $ns //p")" ] \
    || err "a container of $ns sets PRIVACY_POLICY_VERSION in its env: it overrides the ConfigMap that the version in force is checked in. Remove it (overlays/eks patches)."
done
cm_api=$(line "cm ryft-api"); cm_ingest=$(line "cm ryft-ingest")
[ -n "$cm_api" ] && [ -n "$cm_ingest" ] || err "the EKS overlay renders no PRIVACY_POLICY_VERSION for api and ingest (base/<service>/config.env, the pin in overlays/eks)"
api=${cm_api#* }; ingest=${cm_ingest#* }
[ "$api" = "$ingest" ] || err "api has PRIVACY_POLICY_VERSION '$api' and ingest '$ingest' in overlays/eks: they must agree"
v=$api
same_cm() { # <namespace> <deployment> <checked ConfigMap name>
  local refs; refs=$(line "deploy $1 $2")
  [ "$refs" = "$3" ] || err "the $2 Deployment in $1 references ConfigMap(s) '${refs:-none}', not the checked $3"
}
same_cm ryft-api api "${cm_api%% *}"
same_cm ryft-ingest ingest "${cm_ingest%% *}"
printf '%s' "$v" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || err "PRIVACY_POLICY_VERSION '$v' is not a bare YYYY-MM-DD date (no quotes, spaces or comments in config.env or the overlay literal)"
python3 -c 'import datetime,sys; datetime.date.fromisoformat(sys.argv[1])' "$v" 2>/dev/null || err "PRIVACY_POLICY_VERSION '$v' is not a real calendar date (Go refuses it and the pods crash-loop)"
[ -f "$list" ] || err "$list is missing"
jq -e --arg v "$v" 'index($v) != null' "$list" >/dev/null || err "the version in force, $v, is not in $list ($(jq -c . "$list"))"

tag=$(printf '%s\n' "$found" | sed -n 's/^web //p' | head -1)
[ -n "$tag" ] || err "the EKS overlay renders no ghcr.io/timkrebs/ryft/web image"
page() { # <lang> <version>: the page file's content in the pinned web image's tree
  if [ -n "$in_history" ]; then git show "$tag^{commit}:web/content/legal/$1/privacy/$2.mdx" 2>/dev/null
  else cat "web/content/legal/$1/privacy/$2.mdx" 2>/dev/null; fi
}
in_history=
if git rev-parse -q --verify "$tag^{commit}" >/dev/null 2>&1; then
  in_history=1
  for lang in en de; do
    git cat-file -e "$tag^{commit}:web/content/legal/$lang/privacy/$v.mdx" 2>/dev/null \
      || err "the version in force, $v, has no $lang page in the pinned web image ($tag): web/content/legal/$lang/privacy/$v.mdx is missing there. Pin web to a commit that has it, or set the in-force pin back with it."
    # /privacy serves the newest file (web/src/lib/content.ts listMdx sorts and reverses).
    newest=$(git ls-tree --name-only "$tag^{commit}:web/content/legal/$lang/privacy/" | sed -n 's/\.mdx$//p' | LC_ALL=C sort | tail -1)
    [ "$newest" = "$v" ] || err "web $tag serves $newest at /privacy ($lang), but $v is in force: users would consent to $v while reading $newest. Move web and the in-force pin together."
  done
else
  first=$(jq -r '.[0]' "$list")
  [ "$v" = "$first" ] || err "web is pinned to $tag, which is not a commit of this history; that image carries only $first, but $v is in force"
fi

# A pull request that puts a draft text into force is refused; what is in force already stays.
if [ -n "$base" ]; then
  was=$(git show "$base:$overlay" 2>/dev/null | sed -n 's/^.*namespace: ryft-api,.*PRIVACY_POLICY_VERSION=\([^]]*\)\].*$/\1/p' | head -1 || true)
  if [ -n "$was" ] && [ "$was" != "$v" ]; then
    for lang in en de; do
      if page "$lang" "$v" | awk 'NR == 1 && $0 != "---" { exit } NR > 1 && $0 == "---" { exit } /^status:/ { s = $2; gsub(/["'"'"']/, "", s); if (s == "draft") f = 1 } END { exit !f }'; then
        err "the version in force would change from $was to $v, and its $lang page is status: draft in web $tag. Publish the text first (the user's legal call)."
      fi
    done
  fi
fi
echo "policy in force: $v (api and ingest agree; web $tag serves it at /privacy)"
