#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2016
# scripts/hold.sh in throwaway repositories (make test-scripts): writes and removes the exact
# line deploy_targets.sh --check accepts, idempotently, and puts the file back on a failure.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ hold_test: $*" >&2; exit 1; }
setup() { new_repo "$1"; cp "$HERE/scripts/hold.sh" scripts/; }
mode_of() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }

setup add
cp "$OVERLAY" "$tmp/orig"
TODAY=2026-10-22 ./scripts/hold.sh add web "broken AASA, fix in #57" >/dev/null || fail "add web failed"
grep -qxF '  # hold: web 2026-10-22 broken AASA, fix in #57' "$OVERLAY" || fail "the exact line is missing"
[ "$(grep -c '# hold:' "$OVERLAY")" = 1 ] || fail "one hold line expected"
line=$(grep -n '# hold: web' "$OVERLAY" | cut -d: -f1)
[ "$(sed -n "$((line + 1))p" "$OVERLAY")" = '  - name: ghcr.io/timkrebs/ryft/web' ] || fail "not right above the web image"
[ "$(diff "$tmp/orig" "$OVERLAY" | grep -c '^[<>]')" = 1 ] || fail "more than one line changed"
./scripts/deploy_targets.sh --check >/dev/null 2>&1 || fail "--check rejects the hold"
# Idempotent: the same call changes nothing, the date stays, even on a later day.
cp "$OVERLAY" "$tmp/once"
TODAY=2026-10-25 ./scripts/hold.sh add web "broken AASA, fix in #57" >/dev/null || fail "second add failed"
cmp -s "$tmp/once" "$OVERLAY" || fail "a repeated add changed the file"
# A different reason is refused, the file stays.
if TODAY=2026-10-25 ./scripts/hold.sh add web "another reason" 2>/dev/null; then fail "a second reason must be refused"; fi
cmp -s "$tmp/once" "$OVERLAY" || fail "a refused add changed the file"
# The held group is not targeted although web/ changed.
commit_file web/index.txt v2 "web: a change"
[ "$(./scripts/deploy_targets.sh 2>/dev/null | sed -n 's/^web=//p')" = false ] || fail "a held web must not be targeted"
# rm: removes, idempotent.
./scripts/hold.sh rm web >/dev/null || fail "rm failed"
cmp -s "$tmp/orig" "$OVERLAY" || fail "rm did not restore the file byte for byte"
./scripts/hold.sh rm web | grep -q 'not held' || fail "rm of nothing must say so"
cmp -s "$tmp/orig" "$OVERLAY" || fail "rm of nothing changed the file"
[ "$(./scripts/deploy_targets.sh 2>/dev/null | sed -n 's/^web=//p')" = true ] || fail "the released web must be targeted"

# services: above ingest; rm of one group leaves the other.
setup services
TODAY=2026-10-22 ./scripts/hold.sh add services "bad migration" >/dev/null
TODAY=2026-10-22 ./scripts/hold.sh add web "x" >/dev/null
line=$(grep -n '# hold: services' "$OVERLAY" | cut -d: -f1)
[ "$(sed -n "$((line + 1))p" "$OVERLAY")" = '  - name: ghcr.io/timkrebs/ryft/ingest' ] || fail "services hold not above ingest"
./scripts/hold.sh rm services >/dev/null
grep -q '# hold: web 2026-10-22 x' "$OVERLAY" || fail "rm services removed the web hold"
if grep -q '# hold: services' "$OVERLAY"; then fail "rm services left its line"; fi

# The reason is stored verbatim (trimmed); the file mode survives.
setup verbatim
chmod 644 "$OVERLAY"; before=$(mode_of "$OVERLAY")
reason='  a\nb & c/d $HOME "q" '"'s"'  '
TODAY=2026-10-22 ./scripts/hold.sh add web "$reason" >/dev/null || fail "special characters refused"
want='  # hold: web 2026-10-22 a\nb & c/d $HOME "q" '"'s"
grep -qxF "$want" "$OVERLAY" || fail "reason not verbatim: $(grep '# hold' "$OVERLAY")"
[ "$before" = "$(mode_of "$OVERLAY")" ] || fail "file mode changed"

# Refusals: exit 2, nothing written.
setup bad; cp "$OVERLAY" "$tmp/orig"
refuse() { local rc=0; ./scripts/hold.sh "$@" >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "'hold.sh $*' must exit 2 (got $rc)"; }
refuse; refuse add; refuse add web; refuse add web ""; refuse add web "   "; refuse add webby x; refuse add all x
refuse rm; refuse rm nothing; refuse frob web; refuse add web a b; refuse rm web extra
refuse add web "$(printf 'two\nlines')"
cmp -s "$tmp/orig" "$OVERLAY" || fail "a refused call changed the file"

# --check rejects the result (a malformed hold-like line already there): file restored, exit 1.
setup restore
awk '/ryft\/ingest$/ { print "  # hold: servicess 2026-10-22 typo" } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"
cp "$OVERLAY" "$tmp/orig"
rc=0; ./scripts/hold.sh add web "x" >/dev/null 2>&1 || rc=$?
[ "$rc" = 1 ] || fail "--check failure must exit 1 (got $rc)"
cmp -s "$tmp/orig" "$OVERLAY" || fail "the file was not restored"
echo "✓ hold_test"
