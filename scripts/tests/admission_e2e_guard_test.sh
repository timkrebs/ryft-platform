#!/usr/bin/env bash
# The provenance check of scripts/admission_e2e.sh --repo (make test-scripts): a tree that is not this
# checkout runs only when its HEAD commit is on an origin/* branch of this checkout and its tracked files
# under deploy/ and infra/aws/kubernetes/ are unmodified; --force runs it with a warning. No cluster is
# touched: the script runs with a PATH that holds git and the coreutils it needs and NO kind, kubectl or
# helm, so a tree that passes the guard stops at "kind is not installed".
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ admission_e2e_guard_test: $*" >&2; exit 1; }
gi() { git -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@"; }

# A PATH without kind (or kubectl, helm, yq, jq, docker): only what the guard needs.
mkdir -p "$tmp/tools"
for t in git dirname basename sed grep tr cat env bash head awk wc mktemp rm mkdir; do
  p=$(command -v "$t") || fail "setup: $t not found"
  ln -sf "$p" "$tmp/tools/$t"
done
for t in kind kubectl helm; do [ ! -e "$tmp/tools/$t" ] || fail "setup: $t is in the test PATH"; done

# origin with deploy/argocd, a checkout "root" that holds the scripts (the script finds its ROOT from its own path)
gi init -q -b main "$tmp/origin"
mkdir -p "$tmp/origin/deploy/argocd" "$tmp/origin/infra/aws/kubernetes" "$tmp/origin/scripts/lib"
echo 'a: 1' > "$tmp/origin/deploy/argocd/a.yaml"
echo 'locals {}' > "$tmp/origin/infra/aws/kubernetes/main.tf"
cp "$HERE/scripts/admission_e2e.sh" "$HERE/scripts/check_e2e_ref.sh" "$tmp/origin/scripts/"
cp "$HERE"/scripts/lib/*.sh "$tmp/origin/scripts/lib/"
git -C "$tmp/origin" add -A; gi -C "$tmp/origin" commit -qm one
git clone -q "$tmp/origin" "$tmp/root"
git clone -q "$tmp/origin" "$tmp/same"                         # HEAD = origin/main of root
git clone -q "$tmp/origin" "$tmp/ahead"                        # a commit root's origin does not have
echo 'a: 2' > "$tmp/ahead/deploy/argocd/a.yaml"; git -C "$tmp/ahead" add -A; gi -C "$tmp/ahead" commit -qm local
git clone -q "$tmp/origin" "$tmp/dirty"                        # origin's HEAD, but a tracked file edited
echo 'a: 3' > "$tmp/dirty/deploy/argocd/a.yaml"
git clone -q "$tmp/origin" "$tmp/fork"                         # a commit that only another remote of root has
echo 'a: 4' > "$tmp/fork/deploy/argocd/a.yaml"; git -C "$tmp/fork" add -A; gi -C "$tmp/fork" commit -qm forked
git -C "$tmp/root" fetch -q "$tmp/fork" "+HEAD:refs/remotes/fork/x"
git clone -q "$tmp/origin" "$tmp/untracked"                    # untracked and ignored files do not count
echo junk > "$tmp/untracked/deploy/argocd/untracked.yaml"
mkdir -p "$tmp/plain/deploy/argocd"                            # not a git checkout at all

run() { # <args...>: the script of root, with no kind on the PATH
  PATH="$tmp/tools" env -u GITHUB_ACTIONS "$BASH" "$tmp/root/scripts/admission_e2e.sh" "$@" >"$tmp/out" 2>&1 && rc=0 || rc=$?
}
refused() { run --repo "$2" "${@:3}"; [ "$rc" = 2 ] || fail "$1: expected exit 2, got $rc: $(cat "$tmp/out")"; grep -q 'admission_e2e: refused' "$tmp/out" || fail "$1: no refusal message: $(cat "$tmp/out")"; }
passed() { run "${@:2}"; grep -q 'is not installed' "$tmp/out" || fail "$1: the guard stopped it: $(cat "$tmp/out")"; if grep -q 'admission_e2e: refused' "$tmp/out"; then fail "$1: refused"; fi; }

passed same --repo "$tmp/same"
passed untracked --repo "$tmp/untracked"
passed "the default tree (this checkout)"
passed "this checkout named by --repo" --repo "$tmp/root"
refused ahead "$tmp/ahead"
refused dirty "$tmp/dirty"
refused fork "$tmp/fork"
refused "not a checkout" "$tmp/plain"

# --force: runs with a loud warning (and stops at the missing kind)
passed "ahead with --force" --repo "$tmp/ahead" --force
grep -q '!!!! --force' "$tmp/out" || fail "--force without a warning: $(cat "$tmp/out")"
passed "dirty with --force" --repo "$tmp/dirty" --force
grep -q 'modified' "$tmp/out" || fail "--force did not say why: $(cat "$tmp/out")"
# a clean tree with --force needs no warning
passed "same with --force" --repo "$tmp/same" --force
if grep -q '!!!!' "$tmp/out"; then fail "a tree that passed got a warning"; fi

# in GitHub Actions the workflow has checked the ref (scripts/check_e2e_ref.sh) and the checkout is one commit
PATH="$tmp/tools" GITHUB_ACTIONS=true "$BASH" "$tmp/root/scripts/admission_e2e.sh" --repo "$tmp/ahead" >"$tmp/out" 2>&1 && rc=0 || rc=$?
grep -q 'is not installed' "$tmp/out" || fail "GITHUB_ACTIONS: the guard ran: $(cat "$tmp/out")"
grep -q 'notice: GITHUB_ACTIONS=true, so the provenance check' "$tmp/out" || fail "GITHUB_ACTIONS: the skipped guard is not announced: $(cat "$tmp/out")"
# and not announced where it did not apply (this checkout)
PATH="$tmp/tools" GITHUB_ACTIONS=true "$BASH" "$tmp/root/scripts/admission_e2e.sh" >"$tmp/out" 2>&1 || true
if grep -q 'notice:' "$tmp/out"; then fail "a notice for this checkout, which has no guard: $(cat "$tmp/out")"; fi

echo "✓ admission_e2e_guard_test"
