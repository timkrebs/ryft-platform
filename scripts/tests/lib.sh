# shellcheck shell=bash
# Shared by check_workflows_test.sh and coverage_test.sh (sourced; not a test itself): HERE (the
# repository root), a temp directory tmp removed on exit, and no write into the real job summary.
# The promote fixtures (a throwaway repository with the EKS overlay) went with D3 (D246).
# shellcheck disable=SC2034  # read by the tests that source this file
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
tmp=$(mktemp -d)
# Never write into the real job summary of the run that executes the tests.
export GITHUB_STEP_SUMMARY="$tmp/step_summary"
trap 'rm -rf "$tmp"' EXIT
