# shellcheck shell=bash
# Shared by deploy_targets_test.sh and promote_test.sh (sourced; not a test itself).
# A throwaway repository shaped like this one for the pieces the promote needs: backend/,
# web/, the EKS overlay (the real file) and the scripts, with both image groups pinned to
# the first commit and a bare remote.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
OVERLAY=deploy/k8s/overlays/eks/kustomization.yaml
tmp=$(mktemp -d)
# Never write into the real job summary of the run that executes the tests.
export GITHUB_STEP_SUMMARY="$tmp/step_summary"
trap 'rm -rf "$tmp"' EXIT
n=0

short() { git rev-parse HEAD | cut -c1-7; }
commit_file() { # <path> <content> <message>
  mkdir -p "$(dirname "$1")"; printf '%s\n' "$2" > "$1"; git add -A; git commit -qm "$3"
}
# pin <group>... <sha>: what a merged deploy PR does, as a commit.
pin() {
  local sha=${!#}; local gs=("${@:1:$#-1}")
  ./scripts/pin_images.sh "$sha" "${gs[@]}" "$OVERLAY" >/dev/null
  git commit -qam "deploy: images $sha"
}
# hold <group> <reason>: the hold line, right above the group's first image.
hold() {
  local img=web; [ "$1" != services ] || img=ingest
  awk -v g="$1" -v r="$2" -v f="  - name: ghcr.io/timkrebs/ryft/$img" '$0 == f { print "  # hold: " g " 2026-10-23 " r } { print }' "$OVERLAY" > "$OVERLAY.new"
  mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: hold $1"
}
unhold() { grep -v '^ *# hold: ' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: release the hold"; }
# new_repo <name>: cd into a fresh repository, both groups pinned to its first commit.
new_repo() {
  n=$((n+1)); r="$tmp/$1.$n"
  mkdir -p "$r/repo/scripts" "$r/repo/deploy/k8s/overlays/eks" "$r/repo/backend" "$r/repo/web"
  cp "$HERE"/scripts/{promote,pin_images,deploy_targets}.sh "$r/repo/scripts/"
  cp "$HERE/$OVERLAY" "$r/repo/$OVERLAY"
  cd "$r/repo"
  git init -q -b main && git config user.email t@example.com && git config user.name test
  echo v1 > backend/main.go; echo v1 > web/index.txt; git add -A; git commit -qm "first"
  first=$(short)
  pin services web "$first"
  git init -q --bare "$r/remote.git" && git remote add origin "$r/remote.git" && git push -q origin main
}
# The overlay's pins, as `image=tag` lines.
pins() { awk '/- name: ghcr.io\/timkrebs\/ryft\// { sub(/.*ryft\//, ""); n = $0 } /newTag:/ { gsub(/[" ]|newTag:/, ""); print n "=" $0 }' "$1"; }
# hand_pin <image> <tag>: one image pinned by hand, as a commit (what a typo or a half-done revert looks like).
hand_pin() {
  awk -v want="  - name: ghcr.io/timkrebs/ryft/$1" -v t="$2" 'hit { print "    newTag: \"" t "\""; hit = 0; next } { print } $0 == want { hit = 1 }' "$OVERLAY" > "$OVERLAY.new"
  mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: $1 by hand"
}
# drop_pin <image>: the image's two lines removed.
drop_pin() {
  awk -v want="  - name: ghcr.io/timkrebs/ryft/$1" '$0 == want { skip = 2 } skip > 0 { skip--; next } { print }' "$OVERLAY" > "$OVERLAY.new"
  mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: no $1 pin"
}
