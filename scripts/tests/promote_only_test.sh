#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091
# scripts/promote_only.sh in throwaway repositories (make test-scripts): the diff of a promote PR
# (pins, the policy-version literals, holds) is "promote only"; anything else in the overlay, or
# another file, is not.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ promote_only_test: $*" >&2; exit 1; }
setup() { new_repo "$1"; cp "$HERE/scripts/promote_only.sh" "$HERE/scripts/hold.sh" scripts/; base=$(git rev-parse HEAD); }
yes() { ./scripts/promote_only.sh "$base" "$(git rev-parse HEAD)" >/dev/null 2>&1 || fail "$1: expected promote only"; }
no() { if ./scripts/promote_only.sh "$base" "$(git rev-parse HEAD)" >/dev/null 2>&1; then fail "$1: expected NOT promote only"; fi; }

# What promote does: both groups pinned (and the policy literals moved with web).
setup promote
commit_file web/index.txt v2 "web: a change"; base=$(git rev-parse HEAD)
sed -i.bak -E "s/PRIVACY_POLICY_VERSION=[0-9-]+\\]/PRIVACY_POLICY_VERSION=2026-11-02]/" "$OVERLAY"; rm -f "$OVERLAY.bak"; git commit -qam "policy"
pin services web "$(short)"
yes "pins and policy literals"

# A rollback: old pins and a hold in one commit; a release of the hold.
setup rollback
commit_file backend/main.go v2 "backend: change"; base=$(git rev-parse HEAD); pin services "$(short)"
TODAY=2026-10-22 ./scripts/hold.sh add services "bad migration, fix in #99" >/dev/null; git commit -qam "hold"
yes "pins plus a hold"
base=$(git rev-parse HEAD)
./scripts/hold.sh rm services >/dev/null; git commit -qam "release"
yes "a released hold"

# Portable edits of the overlay (BSD sed has no 0,/re/ and no \n in a replacement).
# edit_first <regex> <replacement>: the first matching line, rewritten.
edit_first() { awk -v pat="$1" -v rep="$2" 'done == 0 && $0 ~ pat { sub(pat, rep); done = 1 } { print }' "$OVERLAY" > "$OVERLAY.new" && mv "$OVERLAY.new" "$OVERLAY"; }
# insert_before <exact line> <text>: <text> as a new line above the first line equal to <exact line>.
insert_before() { awk -v at="$1" -v add="$2" 'done == 0 && $0 == at { print add; done = 1 } { print }' "$OVERLAY" > "$OVERLAY.new" && mv "$OVERLAY.new" "$OVERLAY"; }

# Not promote's: a quoted tag that is not seven hex digits, a bare tag, an upper-case one.
for bad in 'zzzzzzz' '123456' '1234567890' 'ABCDEF0'; do
  setup "badtag-$bad"
  edit_first 'newTag: "[0-9a-f]+"' "newTag: \"$bad\""; git commit -qam "hand pin"
  no "tag $bad"
done
setup unquoted
edit_first 'newTag: "[0-9a-f]+"' 'newTag: 1234567'; git commit -qam "hand pin"
no "an unquoted tag"

# Not promote's: another line of the overlay (a new patch, a changed name, a comment).
setup patch
insert_before '  - path: events.yaml' '  - path: extra.yaml'; git commit -qam "a patch"
no "a new patch"
setup rename
edit_first '- name: ghcr.io/timkrebs/ryft/ingest' '- name: ghcr.io/other/ingest'; git commit -qam "rename"
no "a changed image name"
setup comment
insert_before 'images:' '# a comment'; git commit -qam "comment"
no "a comment"
setup hold-shape
insert_before '  - name: ghcr.io/timkrebs/ryft/web' '  # hold: web tomorrow because'; git commit -qam "odd hold"
no "a hold line that hold.sh would not write"
setup policy-shape
sed -i.bak -E 's/PRIVACY_POLICY_VERSION=[0-9-]+\]/PRIVACY_POLICY_VERSION=draft]/' "$OVERLAY"; rm -f "$OVERLAY.bak"; git commit -qam "policy"
no "a policy version that is no date"

# Not promote's: any other file, with or without pins; nothing changed at all.
setup otherfile
commit_file backend/main.go v2 "backend: change"; base=$(git rev-parse HEAD); pin services "$(short)"
commit_file deploy/k8s/base/new.yaml 'a: b' "deploy: a file"
no "a pin plus another file"
setup nothing
no "an empty diff"
setup mode
chmod +x "$OVERLAY"; git -c core.fileMode=true commit -qam "mode"
no "a mode change"

echo "✓ promote_only_test"
