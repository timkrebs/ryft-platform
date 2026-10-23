#!/usr/bin/env bash
# Before make k8s-apply-eks: every ghcr.io image the EKS overlay pins exists in the
# registry, so a mistyped or not-yet-built newTag stops here instead of as
# ImagePullBackOff in the migrate job (docs/OPERATIONS.md → Deploy and roll back).
# The packages are public: an anonymous pull token per repository. Arguments, if
# given, are image references to check instead of the overlay's.
# The overlay is rendered with `kubectl kustomize` and the registry asked with `curl`: there is
# no environment switch for either (a workflow could set one); the tests put fake kubectl and
# curl on PATH. The render holds our six images and nothing else today (no third-party image in
# overlays/eks: PostgreSQL is Supabase, RabbitMQ and its operators are other Argo CD apps with
# their own manifests), so nothing else is allowed. If one is ever added, pin it by digest and
# list it here on purpose.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ $# -gt 0 ]; then refs=$(printf '%s\n' "$@")
else
  rendered=$(env GIT_ALLOW_PROTOCOL=file kubectl kustomize deploy/k8s/overlays/eks)
  refs=$(printf '%s\n' "$rendered" | sed -nE 's/^ *(- )?image: *//p' | tr -d "\"'" | sort -u)
fi
[ -n "$refs" ] || { echo "check_registry: no image to check (the render failed or the overlay is empty): refusing to pass on nothing" >&2; exit 1; }

# The registry answers for any ghcr.io image, so first: ours. Every ghcr.io reference is under
# ghcr.io/timkrebs/ryft/ (a newName in the overlay would render another owner's image and it
# would still "exist"), and, rendering the overlay, each of our images carries exactly the tag
# the line-based reading of the overlay (deploy_targets.sh --pins: what promote, hold.sh and the
# CI checks go by) says; none is missing, none is extra: any other image in the render fails.
ours=ghcr.io/timkrebs/ryft/
for ref in $(printf '%s\n' "$refs" | grep '^ghcr\.io/' || true); do
  case "$ref" in "$ours"*) ;; *) echo "✗ $ref is not under $ours"; exit 1 ;; esac
done
# Every reference, from the overlay's render or from the arguments, is <name>:<tag> of one of our
# six images (a typo such as ghrc.io/... used to pass silently: it is not ghcr.io, so nothing asked
# the registry); and something was checked.
[ -n "$refs" ] || { echo "✗ no image to check (the render is empty or no argument)"; exit 1; }
while IFS= read -r ref; do
  case "$ref" in
    "$ours"*) ;;
    *) if printf '%s\n' "$ref" | grep -qE '(^|/)(ingest|scoring|api|notify|migrate|web)([:@]|$)'; then echo "✗ $ref: one of our images from somewhere else"
       else echo "✗ $ref: not one of our six images. overlays/eks renders no third-party image; a new one needs a digest and an entry in scripts/check_registry.sh"; fi
       exit 1 ;;
  esac
  printf '%s\n' "$ref" | grep -qE "^${ours}(ingest|scoring|api|notify|migrate|web):[A-Za-z0-9_][A-Za-z0-9_.-]*\$" \
    || { echo "✗ $ref: not <name>:<tag> of one of our six images (a digest, or a name that is not ours)"; exit 1; }
done <<EOT
$refs
EOT
if [ $# -eq 0 ]; then
  pins=$(./scripts/deploy_targets.sh --pins)
  while IFS= read -r ref; do
    img=${ref%:*}; img=${img##*/}
    printf '%s\n' "$pins" | grep -qx "$img=${ref##*:}" || { echo "✗ $ref: the render has a tag the overlay's pin lines do not say (a newName/newTag trick, or a stale pin)"; exit 1; }
  done <<EOT
$refs
EOT
  for line in $pins; do
    printf '%s\n' "$refs" | grep -qx "${ours}${line%%=*}:${line#*=}" || { echo "✗ ${line%%=*} is pinned to ${line#*=} but the overlay renders another image"; exit 1; }
  done
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
if [ "$missing" != 0 ]; then
  if [ $# -eq 0 ]; then echo "✗ fix newTag in deploy/k8s/overlays/eks/kustomization.yaml (the short SHA of a green CI run on main)"
  else echo "✗ at least one of the given images is not in the registry (the short SHA of a green CI run on main)"; fi
  exit 1
fi
