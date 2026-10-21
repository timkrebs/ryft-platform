#!/usr/bin/env bash
# Before make k8s-apply-eks: every ghcr.io image the EKS overlay pins exists in the
# registry, so a mistyped or not-yet-built newTag stops here instead of as
# ImagePullBackOff in the migrate job (docs/OPERATIONS.md → Deploy and roll back).
# The packages are public: an anonymous pull token per repository. Arguments, if
# given, are image references to check instead of the overlay's.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ $# -gt 0 ]; then refs=$(printf '%s\n' "$@")
else refs=$(kubectl kustomize deploy/k8s/overlays/eks | sed -nE 's/^ *(- )?image: *//p' | tr -d "\"'" | sort -u)
fi
accept='application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json'
missing=0
for ref in $(printf '%s\n' "$refs" | grep '^ghcr\.io/' || true); do
  name=${ref#ghcr.io/}
  case "$name" in
    *@*) repo=${name%@*}; reference=${name#*@} ;;
    *)   repo=${name%:*}; reference=${name##*:} ;;
  esac
  if ! token=$(curl -fsS "https://ghcr.io/token?scope=repository:$repo:pull" 2>/dev/null \
      | python3 -c 'import json,sys; print(json.load(sys.stdin)["token"])' 2>/dev/null); then
    echo "✗ $ref: no pull token (the package is private or does not exist)"; missing=1; continue
  fi
  code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" -H "Accept: $accept" \
    "https://ghcr.io/v2/$repo/manifests/$reference")
  if [ "$code" = 200 ]; then echo "✓ $ref"
  else echo "✗ $ref: $code (not built yet, or a typo in newTag)"; missing=1
  fi
done
[ "$missing" = 0 ] || { echo "✗ fix newTag in deploy/k8s/overlays/eks/kustomization.yaml (the short SHA of a green CI run on main)"; exit 1; }
