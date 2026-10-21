#!/usr/bin/env bash
# CI's promote job (docs/OPERATIONS.md → Deploy, D96), after the images of a push to
# main: pins deploy/k8s/overlays/eks to this commit on a branch deploy/<sha>, opens the
# PR "deploy: images <sha>" listing the commits since the running pin, closes an older
# deploy PR it supersedes, and turns auto-merge (squash) on. Does nothing when the
# commit is pinned already or nothing under backend/ changed since the running pin
# (which also keeps the merged pin commit from promoting itself).
#   GH_TOKEN  the GitHub App's installation token (PRs it opens run the checks)
#   SLUG      the App's slug (its bot user commits)
#   CHECK_REGISTRY  the image check (default scripts/check_registry.sh; tests override)
set -euo pipefail
cd "$(dirname "$0")/.."
: "${GH_TOKEN:?the GitHub App token}" "${SLUG:?the GitHub App slug}"
check=${CHECK_REGISTRY:-./scripts/check_registry.sh}
sha=$(git rev-parse --short=7 HEAD)
file=deploy/k8s/overlays/eks/kustomization.yaml
old=$(grep -A1 'ghcr.io/timkrebs/ryft/api$' "$file" | sed -nE 's/.*newTag: *"?([0-9a-f]+)"?.*/\1/p')
if [ "$old" = "$sha" ]; then echo "promote: already pinned to $sha"; exit 0; fi
known=; git cat-file -e "$old^{commit}" 2>/dev/null && known=1
if [ -n "$known" ] && git diff --quiet "$old" HEAD -- backend; then
  echo "promote: nothing under backend/ changed since the running pin $old: no deploy"; exit 0
fi
refs=()
for s in ingest scoring api notify migrate; do refs+=("ghcr.io/timkrebs/ryft/$s:$sha"); done
$check "${refs[@]}"
uid=$(gh api "/users/${SLUG}%5Bbot%5D" --jq .id)
git config user.name "${SLUG}[bot]"
git config user.email "${uid}+${SLUG}[bot]@users.noreply.github.com"
branch="deploy/$sha"
git switch -q -c "$branch"
./scripts/pin_images.sh "$sha" "$file" >/dev/null
git commit -qam "deploy: images $sha"
git push -q origin "$branch"
body=$(mktemp)
{
  echo "Pins ingest, scoring, api, notify and migrate to \`$sha\` (running: \`$old\`)."
  echo "Merges itself once the checks pass; Argo CD then syncs ryft-platform (migrate first, the smoke last)."
  echo "Close this PR to stop the deploy. Rollback: revert the merge commit."
  echo
  echo "Commits since the running pin:"
  echo
  if [ -n "$known" ]; then
    git log --no-merges --format='- %h %s' "$old..$sha" | head -100
  else
    echo "- (the running pin $old is not in this history)"
  fi
} > "$body"
# An older deploy PR edits the same lines and would conflict: close it as superseded.
# Not ryft-web's pin PRs (deploy/web-<sha>): they move only web's newTag, which no
# backend promote touches.
for n in $(gh pr list --state open --json number,headRefName --jq '.[] | select((.headRefName | startswith("deploy/")) and (.headRefName | startswith("deploy/web-") | not)) | .number'); do
  gh pr close "$n" --delete-branch --comment "Superseded by deploy: images $sha."
done
gh pr create --base main --head "$branch" --title "deploy: images $sha" --body-file "$body"
gh pr merge "$branch" --auto --squash
rm -f "$body"
echo "promote: $old → $sha in a PR"
