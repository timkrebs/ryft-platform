#!/usr/bin/env bash
# pin_images.sh on a copy of the EKS overlay's kustomization (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ pin_images_test: $*" >&2; exit 1; }
pin_of() { grep -A1 "ghcr.io/timkrebs/ryft/$2\$" "$1" | sed -nE 's/.*newTag: *"?([^"]*)"?/\1/p'; }
real=$HERE/../deploy/k8s/overlays/eks/kustomization.yaml
cp "$real" "$tmp/k.yaml"
before=$(grep -c 'newTag:' "$tmp/k.yaml")
old=$(pin_of "$tmp/k.yaml" api); oldweb=$(pin_of "$tmp/k.yaml" web)

# services: the five images move together, web stays, the previous pin is printed
got=$("$HERE/pin_images.sh" abc1234 services "$tmp/k.yaml") || fail "pinning failed"
[ "$got" = "services=$old" ] || fail "prints the previous pin: got $got, want services=$old"
for s in ingest scoring api notify migrate; do [ "$(pin_of "$tmp/k.yaml" $s)" = abc1234 ] || fail "$s not pinned"; done
[ "$(pin_of "$tmp/k.yaml" web)" = "$oldweb" ] || fail "the web pin must not move with the services"
[ "$(grep -c 'newTag:' "$tmp/k.yaml")" = "$before" ] || fail "the number of pins changed"

# web alone: the service lines stay byte-identical, comments and all
cp "$real" "$tmp/k.yaml"
got=$("$HERE/pin_images.sh" 1234abc web "$tmp/k.yaml") || fail "pinning web failed"
[ "$got" = "web=$oldweb" ] || fail "web: got $got"
[ "$(pin_of "$tmp/k.yaml" web)" = 1234abc ] || fail "web not pinned"
[ "$(diff "$real" "$tmp/k.yaml" | grep -c '^[<>]')" = 2 ] || fail "only web's newTag line may change: $(diff "$real" "$tmp/k.yaml")"

# both, in either order and with the file first or last
cp "$real" "$tmp/k.yaml"
got=$("$HERE/pin_images.sh" abc1234 web services "$tmp/k.yaml") || fail "pinning both failed"
printf '%s\n' "$got" | grep -qx "web=$oldweb" && printf '%s\n' "$got" | grep -qx "services=$old" || fail "both previous pins are printed: $got"
[ "$(grep -c 'newTag: "abc1234"' "$tmp/k.yaml")" = 6 ] || fail "six pins expected"

# a group that was pinned to different commits: warned, all of it moves, both are printed
cp "$real" "$tmp/k.yaml"
awk '/ryft\/ingest$/ { hit = 1; print; next } hit { print "    newTag: \"7654321\""; hit = 0; next } { print }' "$real" > "$tmp/k.yaml"
got=$("$HERE/pin_images.sh" abc1234 services "$tmp/k.yaml" 2>"$tmp/err") || fail "mixed pins"
grep -q 'different commits' "$tmp/err" || fail "no warning about mixed pins"
case "$got" in services=*,*) ;; *) fail "both old pins in the output: $got" ;; esac
[ "$(grep -c 'newTag: "abc1234"' "$tmp/k.yaml")" = 5 ] || fail "all five must move"

# refusals
"$HERE/pin_images.sh" 101bc7 services "$tmp/k.yaml" 2>/dev/null && fail "a 6-digit SHA (the typo of 2026-10-21) must be refused"
"$HERE/pin_images.sh" abc12345 web "$tmp/k.yaml" 2>/dev/null && fail "an 8-digit SHA must be refused (the tag is 7 characters)"
"$HERE/pin_images.sh" ABC1234 web "$tmp/k.yaml" 2>/dev/null && fail "upper case is not a short SHA"
"$HERE/pin_images.sh" abc1234 "$tmp/k.yaml" 2>/dev/null && fail "no group named: nothing to pin"
printf 'images:\n  - name: ghcr.io/timkrebs/ryft/api\n    newTag: "x"\n' > "$tmp/small.yaml"
"$HERE/pin_images.sh" abc1234 web "$tmp/small.yaml" 2>/dev/null && fail "a group missing from the file must fail"
kubectl kustomize "$(dirname "$real")" >/dev/null || fail "the real overlay no longer renders"
echo "✓ pin_images_test: per group, web alone leaves the services byte-identical, previous pins printed, typos refused"
