#!/usr/bin/env bash
# Which ref may the admission-e2e workflow test? (.github/workflows/admission-e2e.yml)
#   scripts/check_e2e_ref.sh <ref> <running-ref> <default-branch>
# The tree under test is code that runs on the runner (kustomize, Terraform, Argo CD in kind), so:
#   - the workflow must be dispatched from the branch under test, not from the default branch
#     (<running-ref> is github.ref, e.g. refs/heads/ci/admission-kind);
#   - refs/pull/* is refused (a pull request, possibly from a fork);
#   - <ref> must resolve to a commit of this repository: a branch (refs/remotes/origin/<ref>), a tag, or
#     a hex commit id, and that commit must be on a branch of the repository (a commit that only a fork
#     or a pull request carries is on none).
# Prints the commit SHA to test on stdout, or an ::error line on stderr and exit 1. Needs a clone with
# all branches (checkout fetch-depth: 0).
set -uo pipefail
ref=${1:?usage: check_e2e_ref.sh <ref> <running-ref> <default-branch>}
run_ref=${2:?}
default_branch=${3:?}
err() { echo "::error title=ref::$*" >&2; exit 1; }

[ "$run_ref" != "refs/heads/$default_branch" ] || err "With a ref, dispatch this workflow from the branch under test (Use workflow from), not from $default_branch."
case "$ref" in
  refs/pull/*|pull/*) err "A pull request ref is not tested here: test the branch it comes from." ;;
  -*|"") err "'$ref' is no ref." ;;
esac
sha=
if git rev-parse --verify --quiet "refs/remotes/origin/$ref^{commit}" >/dev/null; then
  sha=$(git rev-parse "refs/remotes/origin/$ref^{commit}")
elif git rev-parse --verify --quiet "refs/tags/$ref^{commit}" >/dev/null; then
  sha=$(git rev-parse "refs/tags/$ref^{commit}")
elif [[ $ref =~ ^[0-9a-f]{7,40}$ ]] && git rev-parse --verify --quiet "$ref^{commit}" >/dev/null; then
  sha=$(git rev-parse "$ref^{commit}")
fi
[ -n "$sha" ] || err "'$ref' is no branch, tag or commit of this repository."
if [ -z "$(git branch -r --contains "$sha" 2>/dev/null | grep -v 'origin/HEAD' || true)" ]; then
  err "$sha is on no branch of this repository (a commit of a fork or of a pull request?): refused."
fi
echo "$sha"
