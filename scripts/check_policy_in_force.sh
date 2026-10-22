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
#    that is not a commit of this history passes only when it is the known foreign pin
#    f437747 (built in the archived ryft-web repository) AND the version is the first listed
#    one: that image carries nothing newer. Any other sha must be exactly one commit here,
#  - given a base ref (a pull request; it must resolve): the version in force, or the one in
#    base's config.env, changes to a version whose page is not status: published in both
#    languages (frontmatter read like the site does; only exactly "published" passes). An
#    unchanged draft stays allowed, and so does going back to a version listed before the
#    one in force now.
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
# The pin must be exactly one commit of this history, or the one known foreign pin (built in the
# archived ryft-web repository). An unknown or ambiguous short SHA is not "foreign".
FOREIGN=f437747
n_commits=0; n_objects=0; full=
for o in $(git rev-parse --disambiguate="$tag" 2>/dev/null); do
  n_objects=$((n_objects + 1))
  if [ "$(git cat-file -t "$o")" = commit ]; then n_commits=$((n_commits + 1)); full=$o; fi
done
in_history=
if [ "$n_commits" = 1 ]; then in_history=1
elif [ "$n_objects" = 0 ] && [ "$tag" = "$FOREIGN" ]; then in_history=
else err "web is pinned to $tag, which is not exactly one commit of this history ($n_commits found) and not the known foreign pin $FOREIGN"
fi
page() { # <lang> <version>: the page file's content in the pinned web image's tree
  if [ -n "$in_history" ]; then git show "$full:web/content/legal/$1/privacy/$2.mdx" 2>/dev/null
  else cat "web/content/legal/$1/privacy/$2.mdx" 2>/dev/null; fi
}
if [ -n "$in_history" ]; then
  for lang in en de; do
    git cat-file -e "$full:web/content/legal/$lang/privacy/$v.mdx" 2>/dev/null \
      || err "the version in force, $v, has no $lang page in the pinned web image ($tag): web/content/legal/$lang/privacy/$v.mdx is missing there. Pin web to a commit that has it, or set the in-force pin back with it."
    # /privacy serves the newest file (web/src/lib/content.ts listMdx sorts and reverses).
    newest=$(git ls-tree --name-only "$full:web/content/legal/$lang/privacy/" | sed -n 's/\.mdx$//p' | LC_ALL=C sort | tail -1)
    [ "$newest" = "$v" ] || err "web $tag serves $newest at /privacy ($lang), but $v is in force: users would consent to $v while reading $newest. Move web and the in-force pin together."
  done
else
  first=$(jq -r '.[0]' "$list")
  [ "$v" = "$first" ] || err "web is pinned to $tag, which is not a commit of this history; that image carries only $first, but $v is in force"
fi

# Draft guard, on a pull request (a base ref is given). A version may BECOME the one in force, or
# the published one in base's config.env, only when its page says status: published in both
# languages. The frontmatter is read the way the site reads it (gray-matter): a BOM, CRLF, quotes
# on key or value, a trailing comment and spaces after the dashes are all normalised, and anything
# that is not exactly "published" refuses. A version listed BEFORE the one that is in force now was
# in force before, so going back to it is allowed (move web and the in-force pin back together).
published() { # stdin: a page; exit 0 only when its frontmatter status is exactly published
  LC_ALL=C awk '{ sub(/\r$/, "") } NR == 1 { sub(/^\357\273\277/, ""); if ($0 !~ /^---[ \t]*$/) exit } NR > 1 && /^---[ \t]*$/ { exit } /^["'"'"']?status["'"'"']?[ \t]*:/ { s = $0; sub(/^[^:]*:[ \t]*/, "", s); sub(/[ \t]+#.*$/, "", s); gsub(/["'"'"' \t]/, "", s); st = s } END { exit !(st == "published") }'
}
listed_before() { # <candidate> <current>: the candidate is listed before the current one
  jq -e --arg a "$1" --arg b "$2" '(index($a)) as $i | (index($b)) as $j | $i != null and $j != null and $i < $j' "$list" >/dev/null
}
if [ -n "$base" ]; then
  git rev-parse -q --verify "$base^{commit}" >/dev/null 2>&1 || err "the base ref '$base' does not resolve: the draft guard cannot compare (fetch it, or pass origin/<base branch>)"
  git cat-file -e "$base:$overlay" 2>/dev/null || err "$overlay does not exist on $base"
  base_overlay=$(git show "$base:$overlay")
  was=$(printf '%s\n' "$base_overlay" | sed -n 's/^.*namespace: ryft-api,.*PRIVACY_POLICY_VERSION=\([^]]*\)\].*$/\1/p' | head -1)
  if [ -z "$was" ] && printf '%s\n' "$base_overlay" | grep -q PRIVACY_POLICY_VERSION; then
    err "$overlay on $base mentions PRIVACY_POLICY_VERSION but the api literal does not parse: the draft guard cannot tell what was in force"
  fi
  # 1. the version in force changes (the deploy PR, or a hand-made pin)
  if [ -n "$was" ] && [ "$was" != "$v" ] && ! listed_before "$v" "$was"; then
    for lang in en de; do
      page "$lang" "$v" | published \
        || err "the version in force would change from $was to $v, and its $lang page in web $tag is not status: published. Publish the text first (the user's legal call), or go back to a version listed before $was."
    done
  fi
  # 2. the published version in base's config.env changes (the person's policy PR, red before the deploy PR)
  for s in api ingest; do
    f=deploy/k8s/base/$s/config.env
    old=$(git show "$base:$f" 2>/dev/null | sed -n 's/^PRIVACY_POLICY_VERSION=//p' | head -1 || true)
    [ -n "$old" ] || continue
    [ -f "$f" ] || err "$f exists on $base but not in this change"
    new=$(sed -n 's/^PRIVACY_POLICY_VERSION=//p' "$f" | head -1)
    if [ "$old" != "$new" ] && ! listed_before "$new" "$old"; then
      for lang in en de; do
        published < "web/content/legal/$lang/privacy/$new.mdx" \
          || err "$f changes PRIVACY_POLICY_VERSION from $old to $new, and the $lang page web/content/legal/$lang/privacy/$new.mdx is not status: published (or missing). A draft version on main blocks every deploy PR until it is published or web is held (scripts/hold.sh add web \"<reason>\")."
      done
    fi
  done
fi
echo "policy in force: $v (api and ingest agree; web $tag serves it at /privacy)"
