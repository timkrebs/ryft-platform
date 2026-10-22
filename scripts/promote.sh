#!/usr/bin/env bash
# CI's promote job (docs/OPERATIONS.md → Deploy, D96/D107), after the images of a push to
# main: asks scripts/deploy_targets.sh which image groups differ from their running pin
# (services: backend/; web: web/), pins exactly those to this commit on a branch
# deploy/<sha> in ONE PR "deploy: images <sha>", closes every older deploy PR it
# supersedes, and turns auto-merge (squash) on. Does nothing when no group is targeted
# (docs-only, the merged pin commit, a held group). The registry is checked for the exact
# refs it will pin BEFORE any branch is created. Never force-pushes.
#
# Only the tip of main promotes: a re-run of an older run (its web-image failed, a newer
# push already opened deploy/<newer>) exits 0, because the newer run compares with the
# running pin and so pins a superset.
#
# A deploy PR is one that the App opened from this repository, from a branch named
# deploy/<7 hex>; a fork's PR, or a person's, with the same name is never touched.
# Re-runs:
#  - the App's open PR from deploy/<sha> exists: its branch is fetched; when this run pins
#    more than it does (a forced rebuild added a group) one fast-forward commit applies this
#    run's pins on top of its overlay (plain push) and the body is updated; the auto-merge
#    is asserted again;
#  - an earlier run died after its push: the branch is reused when it is exactly one commit
#    on this commit with this run's tree, else exit 1 with the command that deletes it;
#  - that PR was closed unmerged (someone stopped the deploy): not reopened, exit 0.
#   GH_TOKEN  the GitHub App's installation token (PRs it opens run the checks)
#   SLUG      the App's slug (its bot user commits)
#   FORCE     passed through to deploy_targets.sh (workflow_dispatch input)
#   CHECK_REGISTRY  the image check (default scripts/check_registry.sh; tests override)
set -euo pipefail
cd "$(dirname "$0")/.."
: "${GH_TOKEN:?the GitHub App token}" "${SLUG:?the GitHub App slug}"
case "$SLUG" in *[!a-z0-9-]*|"") echo "promote: odd App slug '$SLUG'" >&2; exit 2 ;; esac
check=${CHECK_REGISTRY:-./scripts/check_registry.sh}
# The tag is HEAD cut to 7 characters, like metadata-action's type=sha,format=short.
# Not `rev-parse --short=7`: that returns 8 when seven are ambiguous, and the image jobs
# have pushed the 7-character tag.
head=$(git rev-parse HEAD)
sha=$(printf '%s' "$head" | cut -c1-7)
file=deploy/k8s/overlays/eks/kustomization.yaml
branch="deploy/$sha"

git fetch -q origin main
if [ "$(git rev-parse origin/main)" != "$head" ]; then
  echo "promote: $sha is not the tip of main any more ($(git rev-parse origin/main | cut -c1-7) is): the newer run promotes, it compares with the running pin. Nothing to do."
  exit 0
fi

# The deploy PRs of this App: "<number> <head branch> <mergedAt>" lines.
deploy_prs() { # <open|closed>
  local expr='.[] | select(.isCrossRepository == false and .author.login == "app/SLUG" and (.headRefName | test("^deploy/[0-9a-f]{7}$"))) | "\(.number) \(.headRefName) \(.mergedAt // "")"'
  gh pr list --state "$1" --limit 200 --json number,headRefName,isCrossRepository,author,mergedAt --jq "${expr//SLUG/$SLUG}"
}
# Older deploy PRs edit the same lines and would conflict: close them as superseded (the web
# ones too: each new PR pins a superset of what any older one pinned, because main is
# linear). This run's own branch stays, and so does a PR for a commit that is not an
# ancestor of this one.
close_older() {
  local n b rest s list
  list=$(deploy_prs open)   # assigned first: a failing gh fails the script
  while read -r n b rest; do
    [ -n "$n" ] && [ "$b" != "$branch" ] || continue
    s=${b#deploy/}
    if git rev-parse -q --verify "$s^{commit}" >/dev/null 2>&1 && git merge-base --is-ancestor "$(git rev-parse "$s^{commit}")" "$head"; then
      gh pr close "$n" --delete-branch --comment "Superseded by deploy: images $sha."
    else
      echo "promote: leaving #$n ($b): not an ancestor of $sha"
    fi
  done <<EOT
$list
EOT
}

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
  # A hold is active and nothing else is pending: an open deploy PR that predates it would
  # undo the rollback when it merges.
  case "$(val services_why)$(val web_why)" in
    *held*) close_older ;;
  esac
  exit 0
fi

# Someone closed this deploy's PR without merging it: the deploy was stopped on purpose.
closed_list=$(deploy_prs closed)
stopped=$(printf '%s\n' "$closed_list" | awk -v b="$branch" '$2 == b && NF == 2 { print $1; exit }')
if [ -n "$stopped" ]; then
  echo "promote: #$stopped ($branch) was closed without merging: the deploy of $sha was stopped on purpose and is not reopened. To deploy it after all: reopen the PR and re-run the job, or push a new commit."
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

# What this run pins: one commit on this commit.
git switch -q -c "$branch"
./scripts/pin_images.sh "$sha" "${groups[@]}" "$file" >/dev/null
git commit -qam "deploy: images $sha"
mine=$(git rev-parse HEAD)

body=$(mktemp)
trap 'rm -f "$body"' EXIT
dir_of() { case "$1" in services) echo backend ;; web) echo web ;; esac; }
{
  echo "Pins ${groups[*]} to \`$sha\`. Merges itself once the checks pass; Argo CD then syncs ryft-platform (migrate first, the smoke last)."
  echo "Close this PR to stop the deploy. Rollback: revert the merge commit, and add \`# hold: <group> <YYYY-MM-DD> <reason>\` to the overlay so the next push does not pin the same code again (docs/OPERATIONS.md)."
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
        git log -n 100 --no-merges --format='- %h %s' "$pin..$sha" -- "$dir"
      else
        echo "- (the running pin $pin is not one commit of this history)"
      fi
    else
      echo "### $g: unchanged at \`$pin\` ($(val "${g}_why"))"
    fi
  done
} > "$body"

open_list=$(deploy_prs open)
open_pr=$(printf '%s\n' "$open_list" | awk -v b="$branch" '$2 == b { print $1; exit }')
remote=
if git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
  remote=1
  git fetch -q origin "$branch"
  theirs=$(git rev-parse FETCH_HEAD)
  stray=$(git diff --name-only "$head" "$theirs" | grep -v -x "$file" || true)
  if [ -n "$stray" ]; then
    echo "promote: origin/$branch changes more than $file ($(echo "$stray" | head -3 | paste -sd' ' -)). Delete it and re-run the job: git push origin --delete $branch" >&2
    exit 1
  fi
fi

if [ -n "$open_pr" ]; then
  if [ -z "$remote" ]; then echo "promote: #$open_pr is open but origin/$branch is gone" >&2; exit 1; fi
  # This run's pins go on top of what the PR has (a union: a re-run that pins more than the
  # first attempt did, say FORCE added a group): one fast-forward commit, a plain push.
  git reset -q --hard "$theirs"
  ./scripts/pin_images.sh "$sha" "${groups[@]}" "$file" >/dev/null
  if git diff --quiet; then
    echo "promote: already open: #$open_pr"
  else
    git commit -qam "deploy: images $sha (${groups[*]})"
    git push -q origin "$branch"
    gh pr edit "$open_pr" --body-file "$body"
    echo "promote: #$open_pr updated with ${groups[*]}"
  fi
  close_older
  gh pr merge "$open_pr" --auto --squash
  exit 0
fi

if [ -n "$remote" ]; then
  # An earlier run died after its push, before the PR: the branch must be exactly this run's.
  if [ "$(git rev-parse "$theirs^")" != "$head" ] || [ "$(git rev-parse "$theirs^{tree}")" != "$(git rev-parse "$mine^{tree}")" ]; then
    echo "promote: origin/$branch exists but is not one commit on $sha with this run's pins. Delete it and re-run the job: git push origin --delete $branch" >&2
    exit 1
  fi
  echo "promote: origin/$branch already holds these pins: opening the PR from it"
else
  git push -q origin "$branch"
fi

close_older
url=$(gh pr create --base main --head "$branch" --title "deploy: images $sha" --body-file "$body")
gh pr merge "$url" --auto --squash
echo "promote: ${groups[*]} → $sha in a PR"
