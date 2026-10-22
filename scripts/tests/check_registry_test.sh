#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2015,SC2016
# scripts/check_registry.sh, the part that needs no network (CHECK_REGISTRY_OFFLINE): our
# images, only ours, and exactly the overlay's pin lines. kustomize is replaced by a script.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ check_registry_test: $*" >&2; exit 1; }
new_repo registry; cp "$HERE/scripts/check_registry.sh" scripts/
# render <image>... : a CHECK_RENDER that prints these as kustomize would.
render() { printf '#!/bin/sh\n' > "$tmp/render.sh"; for i in "$@"; do printf 'echo "        - image: %s"\n' "$i" >> "$tmp/render.sh"; done; chmod +x "$tmp/render.sh"; }
good=()
for i in ingest scoring api notify migrate web; do good+=("ghcr.io/timkrebs/ryft/$i:$first"); done
chk() { rc=0; CHECK_RENDER="$tmp/render.sh" CHECK_REGISTRY_OFFLINE=1 ./scripts/check_registry.sh >"$tmp/out" 2>&1 || rc=$?; }

render "${good[@]}" "postgres:17"; chk
[ "$rc" = 0 ] || fail "the overlay's own images must pass: $(cat "$tmp/out")"
# Another owner's image: it would exist, and it is not ours.
render "${good[@]:0:5}" "ghcr.io/attacker/web:$first"; chk
[ "$rc" = 1 ] && grep -q 'is not under ghcr.io/timkrebs/ryft/' "$tmp/out" || fail "another owner's image: rc=$rc $(cat "$tmp/out")"
# Our name from elsewhere, and a tag the pin lines do not say.
render "${good[@]:0:5}" "docker.io/evil/web:$first"; chk
[ "$rc" = 1 ] || fail "our image name from another registry must fail: $(cat "$tmp/out")"
render "${good[@]:0:5}" "ghcr.io/timkrebs/ryft/web:1234567"; chk
[ "$rc" = 1 ] && grep -q 'pin lines do not say' "$tmp/out" || fail "a tag other than the pin: $(cat "$tmp/out")"
# A pinned image the render does not contain.
render "${good[@]:0:5}"; chk
[ "$rc" = 1 ] && grep -q 'web is pinned' "$tmp/out" || fail "a missing image: $(cat "$tmp/out")"
# Arguments (promote's refs): the prefix rule applies to them too.
rc=0; CHECK_REGISTRY_OFFLINE=1 ./scripts/check_registry.sh ghcr.io/attacker/web:abcdef0 >"$tmp/out" 2>&1 || rc=$?
[ "$rc" = 1 ] || fail "an argument outside our prefix must fail"
echo "✓ check_registry_test: only our images, exactly the pin lines"
