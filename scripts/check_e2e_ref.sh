#!/usr/bin/env bash
# Which ref may the admission-e2e workflow test? (.github/workflows/admission-e2e.yml)
#   scripts/check_e2e_ref.sh <ref> <running-ref> <default-branch>
# The tree under test is code that runs on the runner (kustomize, Terraform, Argo CD in kind), so:
#   - the workflow must be dispatched from the branch under test, not from the default branch
#     (<running-ref> is github.ref, e.g. refs/heads/ci/admission-kind);
#   - refs/pull/* is refused (a pull request, possibly from a fork);
#   - <ref> must resolve to a commit of this repository: a branch (refs/remotes/origin/<ref>), a tag, or
#     a hex commit id, and that commit must be on an origin/* branch other than origin/pr/* and origin/pull/*,
#     and remote.origin.fetch must be the default heads-only refspec (a commit that only a fork or a pull
#     request carries is on none).
# Prints the commit SHA to test on stdout, or an ::error line on stderr and exit 1. Needs a clone with
# all branches (checkout fetch-depth: 0).
set -uo pipefail
ref=${1:?usage: check_e2e_ref.sh <ref> <running-ref> <default-branch>}
run_ref=${2:?}
default_branch=${3:?}
# The ref is user input and ends up in a workflow command (::error): print it with every character
# outside a short safe set replaced (a line break would start a new command), cut to 80.
shown=$(printf '%.80s' "$ref" | LC_ALL=C tr -c 'A-Za-z0-9._/@^~+-' '?')
err() { echo "::error title=ref::$*" >&2; exit 1; }

[ "$run_ref" != "refs/heads/$default_branch" ] || err "With a ref, dispatch this workflow from the branch under test (Use workflow from), not from $default_branch."
case "$ref" in
  refs/pull/*|pull/*) err "A pull request ref is not tested here: test the branch it comes from." ;;
  -*|"") err "'$shown' is no ref." ;;
esac
sha=
if git rev-parse --verify --quiet "refs/remotes/origin/$ref^{commit}" >/dev/null; then
  sha=$(git rev-parse "refs/remotes/origin/$ref^{commit}")
elif git rev-parse --verify --quiet "refs/tags/$ref^{commit}" >/dev/null; then
  sha=$(git rev-parse "refs/tags/$ref^{commit}")
elif [[ $ref =~ ^[0-9a-f]{7,40}$ ]] && git rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
  sha=$(git rev-parse "$ref^{commit}")
fi
[ -n "$sha" ] || err "'$shown' is no branch, tag or commit of this repository."
# origin/* holds what `git fetch` put there: a user refspec such as +refs/pull/*/head:refs/remotes/origin/pr/*
# would put a fork's pull request commits there. So the remote must fetch heads only (the default
# refspec; FORCE=1 downgrades this to a warning), and origin/pr/* and origin/pull/* never count.
fetch=$(git config --get-all remote.origin.fetch 2>/dev/null | tr '\n' ' ' | LC_ALL=C tr -c 'A-Za-z0-9._/@^~+*:, -' '?' | cut -c1-120 || true)
if [ -n "$fetch" ] && [ "$fetch" != '+refs/heads/*:refs/remotes/origin/* ' ]; then
  if [ "${FORCE:-}" = 1 ]; then echo "::warning title=ref::remote.origin.fetch is '$fetch', not the default heads-only refspec: origin/* may hold pull request commits (FORCE=1)" >&2
  else err "remote.origin.fetch is '$fetch', not the default heads-only refspec: origin/* may hold pull request commits; refused (FORCE=1 overrides)."; fi
fi
if [ -z "$(git branch -r --list 'origin/*' --contains "$sha" 2>/dev/null | grep -vE '^[[:space:]]*origin/(HEAD([[:space:]]|$)|pr/|pull/)' || true)" ]; then
  err "$sha is on no branch of this repository (a commit of a fork or of a pull request?): refused."
fi
echo "$sha"
