#!/usr/bin/env bash
# scripts/check_e2e_ref.sh in a throwaway clone (make test-scripts): which refs the admission-e2e
# workflow may test.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_e2e_ref_test: $*" >&2; exit 1; }
S="$HERE/scripts/check_e2e_ref.sh"

# an "origin" with main, a feature branch, a tag, and a commit that no branch holds (a fork's)
git init -q -b main "$tmp/origin"
cd "$tmp/origin"
git config user.email t@example.com && git config user.name test
echo 1 > f; git add f; git commit -qm one
git tag v1
git checkout -q -b feature; echo 2 > f; git commit -qam two; feature_sha=$(git rev-parse HEAD)
git checkout -q main
git checkout -q -b hidden; echo 3 > f; git commit -qam three; hidden_sha=$(git rev-parse HEAD)
git checkout -q main; git branch -q -D hidden
git update-ref refs/pull/7/head "$hidden_sha"       # only a pull request carries it
git clone -q "$tmp/origin" "$tmp/clone"
cd "$tmp/clone"
git fetch -q origin "refs/pull/7/head:refs/tmp/pr-7"
git update-ref -d refs/tmp/pr-7
git cat-file -e "$hidden_sha" || fail "setup: the pull request's commit is not in the clone"   # the object is there, no branch holds it

ok() { out=$("$S" "$@" 2>/dev/null) || fail "expected ok: $*"; echo "$out"; }
no() { if "$S" "$@" >/dev/null 2>&1; then fail "expected a refusal: $*"; fi; }

[ "$(ok feature refs/heads/feature main)" = "$feature_sha" ] || fail "feature did not resolve to its commit"
ok v1 refs/heads/feature main >/dev/null
ok "${feature_sha:0:10}" refs/heads/feature main >/dev/null
ok main refs/heads/feature main >/dev/null
no feature refs/heads/main main                      # dispatched from the default branch
no refs/pull/7/head refs/heads/feature main          # a pull request ref
no pull/7/head refs/heads/feature main
no "${hidden_sha:0:10}" refs/heads/feature main      # a commit on no branch
no "$hidden_sha" refs/heads/feature main
no nosuchbranch refs/heads/feature main
no "" refs/heads/feature main
no -x refs/heads/feature main

# a commit that only another remote's branch holds (a fork added as a remote) is on no origin/* branch
tree=$(git rev-parse 'HEAD^{tree}')
forked=$(git -c user.name=t -c user.email=t@example.com commit-tree "$tree" -p HEAD -m "only the fork has it")
git update-ref refs/remotes/fork/other "$forked"
[ -n "$(git branch -r --contains "$forked")" ] || fail "setup: the fork's branch does not contain its commit"
no "${forked:0:10}" refs/heads/feature main
no "$forked" refs/heads/feature main

# the ref is user input in a workflow command: a line break or % must not survive, and the line is cut
for bad in $'x\n::error title=pwn::boom' 'a%0A::warning::x' "$(printf 'z%.0s' $(seq 1 300))"; do
  err=$("$S" "$bad" refs/heads/feature main 2>&1 >/dev/null || true)
  [ "$(printf '%s\n' "$err" | wc -l | tr -d ' ')" = 1 ] || fail "the error for a ref with a line break is more than one line: $err"
  case "$err" in "::error title=ref::"*) ;; *) fail "unexpected error line: $err" ;; esac
  case "$err" in *%*|*$'\r'*) fail "a % or CR survived in: $err" ;; esac
  [ "${#err}" -lt 200 ] || fail "a 300-character ref was echoed whole: ${#err}"
done
echo "✓ check_e2e_ref_test"
