#!/usr/bin/env bash
# pin_images.sh on a copy of the EKS overlay's kustomization (make test-scripts).
set -euo pipefail
export PIN_QUIET=1
HERE=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ pin_images_test: $*" >&2; exit 1; }
pin_of() { grep -A1 "ghcr.io/timkrebs/ryft/$2\$" "$1" | sed -nE 's/.*newTag: *"?([^"]*)"?/\1/p'; }
# The real overlay's shape, not its state: holds stripped, in-force literals fixed (a hold on main or a
# new privacy version must not change this test).
live=$HERE/../deploy/k8s/overlays/eks/kustomization.yaml
real=$tmp/real.yaml
grep -v '^[[:space:]]*#[[:space:]]*hold:' "$live" | sed -E 's/(PRIVACY_POLICY_VERSION=)[0-9-]+\]/\12026-10-20]/' > "$real"
cp "$real" "$tmp/k.yaml"
before=$(grep -c 'newTag:' "$tmp/k.yaml")
old=$(pin_of "$tmp/k.yaml" api); oldweb=$(pin_of "$tmp/k.yaml" web)
h=$(git -C "$HERE/.." rev-parse HEAD | cut -c1-7)  # web pins must be commits of this history (or the one known foreign pin)

# services: the five images move together, web stays, the previous pin is printed
got=$("$HERE/pin_images.sh" abc1234 services "$tmp/k.yaml") || fail "pinning failed"
[ "$got" = "services=$old" ] || fail "prints the previous pin: got $got, want services=$old"
for s in ingest scoring api notify migrate; do [ "$(pin_of "$tmp/k.yaml" $s)" = abc1234 ] || fail "$s not pinned"; done
[ "$(pin_of "$tmp/k.yaml" web)" = "$oldweb" ] || fail "the web pin must not move with the services"
[ "$(grep -c 'newTag:' "$tmp/k.yaml")" = "$before" ] || fail "the number of pins changed"

# web alone: the service lines stay byte-identical, comments and all
cp "$real" "$tmp/k.yaml"
got=$("$HERE/pin_images.sh" "$h" web "$tmp/k.yaml") || fail "pinning web failed"
[ "$got" = "web=$oldweb" ] || fail "web: got $got"
[ "$(pin_of "$tmp/k.yaml" web)" = "$h" ] || fail "web not pinned"
nolit() { grep -v PRIVACY_POLICY_VERSION "$1"; }  # the in-force literals move with web (below); everything else must not
[ "$(diff <(nolit "$real") <(nolit "$tmp/k.yaml") | grep -c "^[<>]")" = 2 ] || fail "only web's newTag line may change: $(diff "$real" "$tmp/k.yaml")"

# both, in either order and with the file first or last
cp "$real" "$tmp/k.yaml"
got=$("$HERE/pin_images.sh" "$h" web services "$tmp/k.yaml") || fail "pinning both failed"
printf '%s\n' "$got" | grep -qx "web=$oldweb" && printf '%s\n' "$got" | grep -qx "services=$old" || fail "both previous pins are printed: $got"
[ "$(grep -c "newTag: \"$h\"" "$tmp/k.yaml")" = 6 ] || fail "six pins expected"

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
"$HERE/pin_images.sh" "$h" web "$tmp/small.yaml" 2>/dev/null && fail "a group missing from the file must fail"
# the privacy policy in force (D78, F1): web moves it to base's version AS OF the pinned commit,
# services leave it alone. No date is hard-coded: the current value is read from the overlay and
# the "new" one is a date that is never real.
force() { sed -nE 's/^.*namespace: ryft-(api|ingest),.*PRIVACY_POLICY_VERSION=([^]]*)\].*$/\2/p' "$1" | paste -sd' ' -; }
cur=$(force "$real"); [ "${cur% *}" = "${cur#* }" ] && [ -n "$cur" ] || fail "the overlay needs both in-force literals, equal: $cur"
cur=${cur%% *}; NEW=2099-12-31
root=$tmp/root; mkdir -p "$root/scripts" "$root/deploy/k8s/base/api" "$root/deploy/k8s/base/ingest" "$root/web/content/legal"
cp "$HERE/pin_images.sh" "$root/scripts/"
echo '["2098-01-01","2099-12-31"]' > "$root/web/content/legal/privacy-versions.json"
base_env() { for s in api ingest; do printf 'PORT=8080\nPRIVACY_POLICY_VERSION=%s\n' "${2:-$1}" > "$root/deploy/k8s/base/$s/config.env"; done; [ -z "${2:-}" ] || printf 'PORT=8080\nPRIVACY_POLICY_VERSION=%s\n' "$1" > "$root/deploy/k8s/base/api/config.env"; }
rgit() { git -C "$root" -c user.email=t@example.com -c user.name=test "$@"; }
rgit init -q -b main
# a commit from before config.env existed (the running web pin of #22's day): the newest page decides
for l in en de; do mkdir -p "$root/web/content/legal/$l/privacy"; : > "$root/web/content/legal/$l/privacy/2097-05-05.mdx"; : > "$root/web/content/legal/$l/privacy/2097-01-01.mdx"; done
rgit add -A; rgit commit -qm pages; pages_sha=$(rgit rev-parse HEAD | cut -c1-7)
: > "$root/web/content/legal/de/privacy/2097-06-06.mdx"; rgit add -A; rgit commit -qm "de ahead"; ahead_sha=$(rgit rev-parse HEAD | cut -c1-7)
base_env 2098-01-01; rgit add -A; rgit commit -qm old; old_sha=$(rgit rev-parse HEAD | cut -c1-7)
base_env "$NEW"; rgit commit -qam new; new_sha=$(rgit rev-parse HEAD | cut -c1-7)
pinp() { "$root/scripts/pin_images.sh" "$@"; }
cp "$real" "$tmp/k.yaml"
pinp "$new_sha" services "$tmp/k.yaml" >/dev/null 2>&1 || fail "services pin failed"
[ "$(force "$tmp/k.yaml")" = "$cur $cur" ] || fail "pinning services must leave the version in force alone: $(force "$tmp/k.yaml")"
env -u PIN_QUIET "$root/scripts/pin_images.sh" "$new_sha" web "$tmp/k.yaml" >/dev/null 2>"$tmp/err" || fail "web pin failed"
[ "$(force "$tmp/k.yaml")" = "$NEW $NEW" ] || fail "pinning web must move the version in force to base's: $(force "$tmp/k.yaml")"
grep -qx "in force: $cur -> $NEW" "$tmp/err" || fail "the move is reported: $(cat "$tmp/err")"
# the base at the PINNED commit counts, not the working tree's
cp "$real" "$tmp/k.yaml"
pinp "$old_sha" web "$tmp/k.yaml" >/dev/null 2>&1 || fail "web pin at the older commit failed"
[ "$(force "$tmp/k.yaml")" = "2098-01-01 2098-01-01" ] || fail "the version comes from base at the pinned commit: $(force "$tmp/k.yaml")"
# a commit without config.env: the newest page, and both languages must agree
cp "$real" "$tmp/k.yaml"
pinp "$pages_sha" web "$tmp/k.yaml" >/dev/null 2>&1 || fail "web pin at a commit without config.env failed"
[ "$(force "$tmp/k.yaml")" = "2097-05-05 2097-05-05" ] || fail "without config.env the newest page decides: $(force "$tmp/k.yaml")"
cp "$real" "$tmp/k.yaml"
pinp "$ahead_sha" web "$tmp/k.yaml" >/dev/null 2>&1 && fail "en and de disagree on the newest page: must fail"
cmp -s "$real" "$tmp/k.yaml" || fail "a refused pin leaves the file untouched"
# the one known foreign pin carries 2026-10-20 whatever the list says; any other unknown sha is refused
pinp f437747 web "$tmp/k.yaml" >/dev/null 2>&1 || fail "the foreign pin f437747 failed"
[ "$(force "$tmp/k.yaml")" = "2026-10-20 2026-10-20" ] || fail "the foreign pin carries 2026-10-20: $(force "$tmp/k.yaml")"
cp "$real" "$tmp/k.yaml"
pinp abc1234 web "$tmp/k.yaml" >/dev/null 2>&1 && fail "an unknown sha must be refused, not treated as foreign"
cmp -s "$real" "$tmp/k.yaml" || fail "a refused pin leaves the file untouched"
# refusals leave the file untouched
cp "$real" "$tmp/k.yaml"
refuse() { # <why>
  rgit commit -qam "$1"; s=$(rgit rev-parse HEAD | cut -c1-7)
  pinp "$s" web "$tmp/k.yaml" >/dev/null 2>&1 && fail "must fail: $1"
  cmp -s "$real" "$tmp/k.yaml" || fail "a failed pin must leave the file untouched ($1)"
}
base_env "$NEW" 2099-12-30; refuse "base api and ingest disagree"
base_env "$NEW" "\"$NEW\""; refuse "a quoted version in config.env"
base_env 2099-02-30; refuse "a date that does not exist"
base_env "$(printf '\xd9\xa2\xd9\xa0\xd9\xa9\xd9\xa9-12-31')"; refuse "Unicode digits"
base_env "$NEW"; rgit commit -qam fine
grep -v 'namespace: ryft-ingest' "$real" > "$tmp/nolit.yaml"
pinp "$new_sha" web "$tmp/nolit.yaml" >/dev/null 2>&1 && fail "an overlay without both literals must fail when web is pinned"

kubectl kustomize "$(dirname "$live")" >/dev/null || fail "the real overlay no longer renders"
echo "✓ pin_images_test: per group, web alone leaves the services byte-identical, previous pins printed, typos refused"
