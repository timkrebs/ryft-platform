#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2015,SC2016
# scripts/check_registry.sh: our images, only ours, exactly the overlay's pin lines, and the
# registry's answer. kubectl and curl are fakes on PATH (the script has no environment hooks).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ check_registry_test: $*" >&2; exit 1; }
new_repo registry; cp "$HERE/scripts/check_registry.sh" scripts/
mkdir -p "$tmp/fakebin"
# kubectl kustomize <dir>: prints the file the test wrote.
cat > "$tmp/fakebin/kubectl" <<'G'
#!/bin/sh
[ "$1" = kustomize ] && [ "$2" = deploy/k8s/overlays/eks ] || { echo "fake kubectl: unexpected $*" >&2; exit 2; }
cat "$FAKE_RENDER"
G
# curl: the pull token for ghcr.io/token, the status code (FAKE_CODE, default 200) for the manifest.
cat > "$tmp/fakebin/curl" <<'G'
#!/bin/sh
echo "$*" >> "$FAKE_CURL_LOG"
case "$*" in
  *"/token?"*) [ -z "${FAKE_NO_TOKEN:-}" ] || exit 22; echo '{"token":"t"}' ;;
  *) printf '%s' "${FAKE_CODE:-200}" ;;
esac
G
chmod +x "$tmp/fakebin/kubectl" "$tmp/fakebin/curl"
export FAKE_RENDER="$tmp/render.yaml" FAKE_CURL_LOG="$tmp/curl.log"
# render <image>... : what kustomize prints, as image lines.
render() { : > "$FAKE_RENDER"; for i in "$@"; do printf '        - image: %s\n' "$i" >> "$FAKE_RENDER"; done; : > "$FAKE_CURL_LOG"; }
good=()
for i in ingest scoring api notify migrate web; do good+=("ghcr.io/timkrebs/ryft/$i:$first"); done
chk() { rc=0; PATH="$tmp/fakebin:$PATH" ./scripts/check_registry.sh "$@" >"$tmp/out" 2>&1 || rc=$?; }

render "${good[@]}"; chk
[ "$rc" = 0 ] || fail "the overlay's own images must pass: $(cat "$tmp/out")"
[ "$(grep -c manifests "$FAKE_CURL_LOG")" = 6 ] || fail "the registry is asked once per image: $(cat "$FAKE_CURL_LOG")"
# The same image twice in the render (migrate and smoke share one): once.
render "${good[@]}" "${good[4]}"; chk
[ "$rc" = 0 ] || fail "a repeated image is fine: $(cat "$tmp/out")"

# Any other image fails, from any registry: the render has no third-party image today.
for x in "postgres:17" "docker.io/attacker/miner:1" "docker.io/attacker/miner@sha256:$(printf 'a%.0s' $(seq 64))" "quay.io/x/y:1" "busybox" "registry.k8s.io/pause:3.9"; do
  render "${good[@]}" "$x"; chk
  [ "$rc" = 1 ] && grep -q 'not one of our six images' "$tmp/out" || fail "an extra image '$x' must fail: rc=$rc $(cat "$tmp/out")"
done
# An extra ghcr.io image of ours that is not one of the six, and one by digest.
render "${good[@]}" "ghcr.io/timkrebs/ryft/miner:$first"; chk
[ "$rc" = 1 ] && grep -q 'not <name>:<tag>' "$tmp/out" || fail "an unknown ryft image: rc=$rc $(cat "$tmp/out")"
render "${good[@]:0:5}" "ghcr.io/timkrebs/ryft/web@sha256:$(printf 'a%.0s' $(seq 64))"; chk
[ "$rc" = 1 ] || fail "a digest instead of the tag must fail: $(cat "$tmp/out")"
# Another owner's image: it would exist, and it is not ours.
render "${good[@]:0:5}" "ghcr.io/attacker/web:$first"; chk
[ "$rc" = 1 ] && grep -q 'is not under ghcr.io/timkrebs/ryft/' "$tmp/out" || fail "another owner's image: rc=$rc $(cat "$tmp/out")"
# Our name from elsewhere, and a tag the pin lines do not say.
render "${good[@]:0:5}" "docker.io/evil/web:$first"; chk
[ "$rc" = 1 ] && grep -q 'from somewhere else' "$tmp/out" || fail "our image name from another registry must fail: $(cat "$tmp/out")"
render "${good[@]:0:5}" "ghcr.io/timkrebs/ryft/web:1234567"; chk
[ "$rc" = 1 ] && grep -q 'pin lines do not say' "$tmp/out" || fail "a tag other than the pin: $(cat "$tmp/out")"
# A pinned image the render does not contain; an empty render.
render "${good[@]:0:5}"; chk
[ "$rc" = 1 ] && grep -q 'web is pinned' "$tmp/out" || fail "a missing image: $(cat "$tmp/out")"
render; chk
[ "$rc" = 1 ] || fail "an empty render must fail: $(cat "$tmp/out")"

# The registry's answer.
render "${good[@]}"; FAKE_CODE=404 chk
[ "$rc" = 1 ] && grep -q '404' "$tmp/out" || fail "a tag the registry does not know: rc=$rc $(cat "$tmp/out")"
render "${good[@]}"; FAKE_NO_TOKEN=1 chk
[ "$rc" = 1 ] && grep -q 'no pull token' "$tmp/out" || fail "no pull token: rc=$rc $(cat "$tmp/out")"
# The environment cannot switch the check off.
render "${good[@]}" "docker.io/attacker/miner:1"
rc=0; CHECK_REGISTRY_OFFLINE=1 CHECK_RENDER=true RENDER_CMD=true CHECK_REGISTRY=true PATH="$tmp/fakebin:$PATH" ./scripts/check_registry.sh >"$tmp/out" 2>&1 || rc=$?
[ "$rc" = 1 ] || fail "no environment variable may skip the check: $(cat "$tmp/out")"
# Arguments (promote's refs): the prefix rule applies to them too.
chk ghcr.io/attacker/web:abcdef0
[ "$rc" = 1 ] || fail "an argument outside our prefix must fail"
for a in "ghrc.io/timkrebs/ryft/web:$first" "docker.io/library/postgres:17" "ghcr.io/timkrebs/ryft/miner:$first" "ghcr.io/timkrebs/ryft/web" "ghcr.io/timkrebs/ryft/web@sha256:$(printf 'a%.0s' $(seq 64))"; do
  chk "ghcr.io/timkrebs/ryft/web:$first" "$a"; [ "$rc" = 1 ] || fail "the argument '$a' must fail even next to a good one (rc=$rc): $(cat "$tmp/out")"
done
chk ""; [ "$rc" = 1 ] || fail "an empty argument must fail (rc=$rc)"
chk "ghcr.io/timkrebs/ryft/web:$first"
[ "$rc" = 0 ] || fail "an argument of ours that the registry has: $(cat "$tmp/out")"
echo "✓ check_registry_test: only our six images, exactly the pin lines, the registry's answer, no environment switch"
