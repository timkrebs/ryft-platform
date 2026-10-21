#!/usr/bin/env bash
# CI's promote job (docs/OPERATIONS.md → Deploy, D96/D107), after the images of a push to
# main: asks scripts/deploy_targets.sh which image groups differ from their running pin
# (services: backend/; web: web/), pins exactly those to this commit on a branch
# deploy/<sha> in ONE PR "deploy: images <sha>", closes every older open deploy/* PR it
# supersedes, and turns auto-merge (squash) on. Does nothing when no group is targeted
# (docs-only, the merged pin commit, a held group). The registry is checked for the exact
# refs it will pin BEFORE any branch is created.
#   GH_TOKEN  the GitHub App's installation token (PRs it opens run the checks)
#   SLUG      the App's slug (its bot user commits)
#   FORCE     passed through to deploy_targets.sh (workflow_dispatch input)
#   CHECK_REGISTRY  the image check (default scripts/check_registry.sh; tests override)
set -euo pipefail
cd "$(dirname "$0")/.."
: "${GH_TOKEN:?the GitHub App token}" "${SLUG:?the GitHub App slug}"
check=${CHECK_REGISTRY:-./scripts/check_registry.sh}
# The tag is HEAD cut to 7 characters, like metadata-action's type=sha,format=short.
# Not `rev-parse --short=7`: that returns 8 when seven are ambiguous, and the image jobs
# have pushed the 7-character tag.
sha=$(git rev-parse HEAD | cut -c1-7)
file=deploy/k8s/overlays/eks/kustomization.yaml

targets=$(./scripts/deploy_targets.sh)
val() { printf '%s\n' "$targets" | sed -n "s/^$1=//p" | head -1; }
groups=()
for g in services web; do
  if [ "$(val "$g")" != true ]; then continue; fi
  if [ "$(val "${g}_pin")" = "$sha" ]; then echo "promote: $g is pinned to $sha already"; continue; fi
  groups+=("$g")
done
if [ "${#groups[@]}" = 0 ]; then
  echo "promote: no group differs from its running pin (services: $(val services_why); web: $(val web_why)): no deploy"
  exit 0
fi

refs=()
for g in "${groups[@]}"; do
  case "$g" in
    services) for s in ingest scoring api notify migrate; do refs+=("ghcr.io/timkrebs/ryft/$s:$sha"); done ;;
    web) refs+=("ghcr.io/timkrebs/ryft/web:$sha") ;;
  esac
done
$check "${refs[@]}"

uid=$(gh api "/users/${SLUG}%5Bbot%5D" --jq .id)
git config user.name "${SLUG}[bot]"
git config user.email "${uid}+${SLUG}[bot]@users.noreply.github.com"
branch="deploy/$sha"
# Every older deploy/* PR edits the same lines and would conflict: close them all as
# superseded (the web ones too: each new PR pins a superset of what any older one pinned,
# because main is linear). This run's own branch is kept.
close_older() {
  local n
  for n in $(gh pr list --state open --json number,headRefName --jq ".[] | select((.headRefName | startswith(\"deploy/\")) and .headRefName != \"$branch\") | .number"); do
    gh pr close "$n" --delete-branch --comment "Superseded by deploy: images $sha."
  done
}
# A re-run of this job (nothing is force-pushed): the PR from the attempt before is open.
mine=$(gh pr list --state open --json number,headRefName --jq ".[] | select(.headRefName == \"$branch\") | .number")
if [ -n "$mine" ]; then
  echo "promote: already open: #$mine"
  close_older
  gh pr merge "$branch" --auto --squash
  exit 0
fi
git switch -q -c "$branch"
./scripts/pin_images.sh "$sha" "${groups[@]}" "$file" >/dev/null
git commit -qam "deploy: images $sha"
if git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
  # An earlier run died after its push, before the PR: reuse the branch when it holds what
  # this run would push.
  git fetch -q origin "$branch"
  if [ "$(git show "FETCH_HEAD:$file")" != "$(cat "$file")" ]; then
    echo "promote: origin/$branch exists with other pins than this run would make. Delete it and re-run the job: git push origin --delete $branch" >&2
    exit 1
  fi
  echo "promote: origin/$branch already holds these pins: opening the PR from it"
else
  git push -q origin "$branch"
fi

body=$(mktemp)
trap 'rm -f "$body"' EXIT
dir_of() { case "$1" in services) echo backend ;; web) echo web ;; esac; }
{
  echo "Pins ${groups[*]} to \`$sha\`. Merges itself once the checks pass; Argo CD then syncs ryft-platform (migrate first, the smoke last)."
  echo "Close this PR to stop the deploy. Rollback: revert the merge commit, and add \`# hold: <group> <reason>\` to the overlay so the next push does not pin the same code again (docs/OPERATIONS.md)."
  for g in services web; do
    pin=$(val "${g}_pin"); dir=$(dir_of "$g"); moved=
    for m in "${groups[@]}"; do [ "$m" = "$g" ] && moved=1; done
    echo
    if [ -n "$moved" ]; then
      echo "### $g: \`$pin\` → \`$sha\` ($(val "${g}_why"))"
      echo
      echo "Commits under \`$dir/\` since the running pin:"
      echo
      if git rev-parse -q --verify "$pin^{commit}" >/dev/null 2>&1; then
        git log --no-merges --format='- %h %s' "$pin..$sha" -- "$dir" | head -100
      else
        echo "- (the running pin $pin is not one commit of this history)"
      fi
    else
      echo "### $g: unchanged at \`$pin\` ($(val "${g}_why"))"
    fi
  done
} > "$body"

close_older
gh pr create --base main --head "$branch" --title "deploy: images $sha" --body-file "$body"
gh pr merge "$branch" --auto --squash
echo "promote: ${groups[*]} → $sha in a PR"
