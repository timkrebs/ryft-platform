#!/usr/bin/env bash
# promote.sh in a throwaway repository with a fake gh and a local remote
# (make test-scripts): a backend change is pinned in a PR by the App's bot; nothing
# under backend/ does nothing; an older deploy PR is closed; the merged pin commit
# does not promote itself.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ promote_test: $*" >&2; exit 1; }
mkdir -p "$tmp/bin" "$tmp/repo/scripts" "$tmp/repo/deploy/k8s/overlays/eks" "$tmp/repo/backend"
cat > "$tmp/bin/gh" <<'G'
#!/bin/sh
echo "gh $*" >> "$GH_LOG"
case "$1 $2" in
  "api /users/"*) echo 4242 ;;
  "pr list") [ -n "${GH_OPEN_PR:-}" ] && echo "$GH_OPEN_PR" ;;
esac
exit 0
G
chmod +x "$tmp/bin/gh"
cp "$HERE/scripts/promote.sh" "$HERE/scripts/pin_images.sh" "$tmp/repo/scripts/"
cp "$HERE/deploy/k8s/overlays/eks/kustomization.yaml" "$tmp/repo/deploy/k8s/overlays/eks/"
cd "$tmp/repo"
git init -q -b main && git config user.email t@example.com && git config user.name test
echo v1 > backend/main.go && git add -A && git commit -qm "first"
first=$(git rev-parse --short=7 HEAD)
./scripts/pin_images.sh "$first" deploy/k8s/overlays/eks/kustomization.yaml >/dev/null
git commit -qam "deploy: images $first"
git init -q --bare "$tmp/remote.git" && git remote add origin "$tmp/remote.git" && git push -q origin main
run() { GH_LOG="$tmp/gh.log" PATH="$tmp/bin:$PATH" GH_TOKEN=t SLUG=ryft-deploy CHECK_REGISTRY=true ./scripts/promote.sh; }

# 1. docs only since the pin: no deploy
echo notes > README.md && git add README.md && git commit -qm "docs: notes"
out=$(run) && echo "$out" | grep -q 'nothing under backend/' || fail "a docs-only change must not deploy: $out"
[ ! -s "$tmp/gh.log" ] || fail "no gh call without a deploy: $(cat "$tmp/gh.log")"

# 2. a backend change: pinned on deploy/<sha>, PR with the commits, auto-merge
echo v2 > backend/main.go && git commit -qam "scoring: a change"
sha=$(git rev-parse --short=7 HEAD)
GH_OPEN_PR=7 run >/dev/null || fail "promote failed"
git show "origin/deploy/$sha:deploy/k8s/overlays/eks/kustomization.yaml" | grep -q "newTag: \"$sha\"" || fail "the branch does not pin $sha"
[ "$(git log -1 --format='%an <%ae> %s' "origin/deploy/$sha")" = "ryft-deploy[bot] <4242+ryft-deploy[bot]@users.noreply.github.com> deploy: images $sha" ] \
  || fail "the pin commit is not the bot's: $(git log -1 --format='%an <%ae> %s' "origin/deploy/$sha")"
grep -q "gh pr close 7 --delete-branch" "$tmp/gh.log" || fail "the older deploy PR is not closed"
grep -q "gh pr create --base main --head deploy/$sha --title deploy: images $sha" "$tmp/gh.log" || fail "no PR: $(cat "$tmp/gh.log")"
grep -q "gh pr merge deploy/$sha --auto --squash" "$tmp/gh.log" || fail "no auto-merge"

# 3. the merged pin commit on main: pinned already, nothing to do
git switch -q main && git merge -q --squash "deploy/$sha" >/dev/null && git commit -qm "deploy: images $sha (#8)"
: > "$tmp/gh.log"
out=$(run) && echo "$out" | grep -q 'nothing under backend/' || fail "the pin commit must not promote itself: $out"

echo "✓ promote_test: backend changes pinned in a PR by the bot, docs and pin commits skipped, older PRs closed"
