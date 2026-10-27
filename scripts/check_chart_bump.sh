#!/usr/bin/env bash
# The admission-policy chart's version moves with its templates (CI's `admission` job, on a pull request).
#   scripts/check_chart_bump.sh <base-sha> <head-sha> [chart-dir]
# The Helm provider upgrades a release only when the chart's version changes (Chart.yaml says so:
# "bump on every template change"), so a change to templates/ or values.yaml of the chart without a
# higher `version:` in Chart.yaml would be merged, applied by Terraform and silently do nothing.
# Exit 0: nothing in the chart's templates or values changed, the chart is new, or the version went up.
# Exit 1: they changed and the version did not go up. Reads git only (diff of base..head). Fails closed.
set -uo pipefail
base=${1:?usage: check_chart_bump.sh <base-sha> <head-sha> [chart-dir]}
head=${2:?usage: check_chart_bump.sh <base-sha> <head-sha> [chart-dir]}
chart=${3:-infra/aws/kubernetes/charts/admission-policy}

err() { echo "::error title=chart version::$*" >&2; echo "check_chart_bump: $*" >&2; }
version_of() { # <rev>: the chart's version at that revision, empty when there is no Chart.yaml
  git show "$1:$chart/Chart.yaml" 2>/dev/null | sed -nE 's/^version:[[:space:]]*"?([0-9][^"# ]*)"?.*$/\1/p' | head -1
}

changed=$(git diff --name-only "$base" "$head" -- "$chart/templates" "$chart/values.yaml") || { err "git diff failed"; exit 1; }
if [ -z "$changed" ]; then
  echo "check_chart_bump: no change to $chart/templates or values.yaml: no version bump needed"
  exit 0
fi
old=$(version_of "$base")
new=$(version_of "$head")
if [ -z "$old" ]; then
  echo "check_chart_bump: $chart is new on this branch (version ${new:-?}): nothing to compare"
  exit 0
fi
if [ -z "$new" ]; then err "$chart/Chart.yaml has no readable version: line"; exit 1; fi
if [ "$old" = "$new" ]; then
  err "$(printf '%s' "$changed" | tr '\n' ' ')changed but the version in $chart/Chart.yaml is still $old: bump it, or the Helm provider will not upgrade the release"
  exit 1
fi
if [ "$(printf '%s\n%s\n' "$old" "$new" | sort -V | tail -1)" != "$new" ]; then
  err "the version in $chart/Chart.yaml went from $old to $new: it must go up"
  exit 1
fi
echo "check_chart_bump: the chart's version went from $old to $new with its templates or values"
