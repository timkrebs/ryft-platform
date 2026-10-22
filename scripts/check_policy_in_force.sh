#!/usr/bin/env bash
# CI's manifests job (and make k8s-validate): the privacy policy version in force on EKS is
# consistent with the site that is deployed (D78, F1). It renders overlays/eks and fails when
#  - api and ingest do not get the same PRIVACY_POLICY_VERSION,
#  - the value is not a bare YYYY-MM-DD date (quotes, spaces and comments are part of a value),
#  - the version is not in web/content/legal/privacy-versions.json,
#  - the web image's commit lacks web/content/legal/{en,de}/privacy/<version>.mdx. A pin that
#    is not a commit of this history (f437747 was built in the archived ryft-web repository)
#    passes only with the first listed version: that image carries nothing newer.
# So rolling web back past a policy version fails until the in-force pin goes back too.
#   scripts/check_policy_in_force.sh
#   RENDER_CMD  how the overlay is rendered (default: kubectl kustomize deploy/k8s/overlays/eks)
set -euo pipefail
cd "$(dirname "$0")/.."
render=${RENDER_CMD:-kubectl kustomize deploy/k8s/overlays/eks}
list=web/content/legal/privacy-versions.json
err() { echo "::error title=policy in force::$*" >&2; exit 1; }

rendered=$(mktemp); trap 'rm -f "$rendered"' EXIT
$render > "$rendered" || err "cannot render the EKS overlay"

# "<namespace> <raw value>" for every ConfigMap that carries the key, "web <tag>" for the site image.
found=$(python3 - "$rendered" <<'PY'
import re, sys
for d in open(sys.argv[1]).read().split("\n---\n"):
    ns = re.search(r"^  namespace: (\S+)\s*$", d, re.M)
    if re.search(r"^kind: ConfigMap$", d, re.M) and ns:
        m = re.search(r"^  PRIVACY_POLICY_VERSION: (.*)$", d, re.M)
        if m:
            v = m.group(1).rstrip()
            if v.startswith("'") and v.endswith("'") and len(v) >= 2:
                v = v[1:-1]
            elif v.startswith('"') and v.endswith('"') and len(v) >= 2:
                v = v[1:-1]
            print(f"cm {ns.group(1)} {v}")
    m = re.search(r"^\s+(?:- )?image: ghcr\.io/timkrebs/ryft/web:(\S+)\s*$", d, re.M)
    if m:
        print(f"web {m.group(1).strip(chr(34))}")
PY
)
val() { printf '%s\n' "$found" | sed -n "s/^cm $1 //p" | head -1; }
api=$(val ryft-api); ingest=$(val ryft-ingest)
[ -n "$api" ] && [ -n "$ingest" ] || err "the EKS overlay renders no PRIVACY_POLICY_VERSION for api and ingest (base/<service>/config.env, the pin in overlays/eks)"
[ "$api" = "$ingest" ] || err "api has PRIVACY_POLICY_VERSION '$api' and ingest '$ingest' in overlays/eks: they must agree"
v=$api
printf '%s' "$v" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' || err "PRIVACY_POLICY_VERSION '$v' is not a bare YYYY-MM-DD date (no quotes, spaces or comments in config.env or the overlay literal)"
[ -f "$list" ] || err "$list is missing"
jq -e --arg v "$v" 'index($v) != null' "$list" >/dev/null || err "the version in force, $v, is not in $list ($(jq -c . "$list"))"

tag=$(printf '%s\n' "$found" | sed -n 's/^web //p' | head -1)
[ -n "$tag" ] || err "the EKS overlay renders no ghcr.io/timkrebs/ryft/web image"
if git rev-parse -q --verify "$tag^{commit}" >/dev/null 2>&1; then
  for lang in en de; do
    git cat-file -e "$tag^{commit}:web/content/legal/$lang/privacy/$v.mdx" 2>/dev/null \
      || err "the version in force, $v, has no $lang page in the pinned web image ($tag): web/content/legal/$lang/privacy/$v.mdx is missing there. Pin web to a commit that has it, or set the in-force pin back."
  done
else
  first=$(jq -r '.[0]' "$list")
  [ "$v" = "$first" ] || err "web is pinned to $tag, which is not a commit of this history; that image carries only $first, but $v is in force"
fi
echo "policy in force: $v (api and ingest agree; web $tag carries its page)"
