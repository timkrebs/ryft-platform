#!/usr/bin/env bash
# pin_images.sh on a copy of the EKS overlay's kustomization (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ pin_images_test: $*" >&2; exit 1; }
cp "$HERE/../deploy/k8s/overlays/eks/kustomization.yaml" "$tmp/k.yaml"
before=$(grep -c 'newTag:' "$tmp/k.yaml")
old=$(grep -A1 'ghcr.io/timkrebs/ryft/api$' "$tmp/k.yaml" | sed -nE 's/.*newTag: *"?([^"]*)"?/\1/p')
got=$("$HERE/pin_images.sh" abc1234 "$tmp/k.yaml") || fail "pinning failed"
[ "$got" = "$old" ] || fail "prints the previous pin: got $got, want $old"
for s in ingest scoring api notify migrate; do
  grep -A1 "ghcr.io/timkrebs/ryft/$s\$" "$tmp/k.yaml" | grep -q 'newTag: "abc1234"' || fail "$s not pinned"
done
[ "$(grep -c 'newTag:' "$tmp/k.yaml")" = "$before" ] || fail "the number of pins changed"
grep -A1 'ghcr.io/timkrebs/ryft/web$' "$tmp/k.yaml" | grep -q 'newTag: "abc1234"' && fail "the web pin must not move"
"$HERE/pin_images.sh" 101bc7 "$tmp/k.yaml" 2>/dev/null && fail "a 6-digit SHA (the typo of 2026-10-21) must be refused"
"$HERE/pin_images.sh" ABC1234 "$tmp/k.yaml" 2>/dev/null && fail "upper case is not a short SHA"
kubectl kustomize "$(dirname "$HERE/../deploy/k8s/overlays/eks/kustomization.yaml")" >/dev/null || fail "the real overlay no longer renders"
echo "✓ pin_images_test: five images to one SHA, the previous pin printed, typos refused"
