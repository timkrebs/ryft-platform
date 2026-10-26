#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2329
# scripts/lib/kind_guard.sh on fake kubeconfigs (make test-scripts): only a kind cluster's own
# single-context file with a server on this machine passes. Reads files with `kubectl config`;
# no cluster is contacted.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(cd "$(mktemp -d)" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
command -v kubectl >/dev/null || { echo "✓ kind_guard_test (skipped: no kubectl)"; exit 0; }
fail() { echo "✗ kind_guard_test: $*" >&2; exit 1; }
# shellcheck source=../lib/kind_guard.sh
. "$HERE/scripts/lib/kind_guard.sh"

mk() { # <file> <context> <server>: a kubeconfig with one context
  local f=$1
  KUBECONFIG="$f" kubectl config set-cluster "c-$2" --server "$3" >/dev/null
  KUBECONFIG="$f" kubectl config set-credentials "u-$2" --token x >/dev/null
  KUBECONFIG="$f" kubectl config set-context "$2" --cluster "c-$2" --user "u-$2" >/dev/null
  KUBECONFIG="$f" kubectl config use-context "$2" >/dev/null
}
ok() { ( KUBECONFIG="$1" HOME="${2:-$tmp/home}" kind_guard ) >/dev/null 2>&1 || fail "expected the guard to pass: $3"; }
no() { if ( KUBECONFIG="$1" HOME="${2:-$tmp/home}" kind_guard ) >/dev/null 2>&1; then fail "expected the guard to refuse: $3"; fi; }

mkdir -p "$tmp/home/.kube" "$tmp/k"
mk "$tmp/k/kind-ok" kind-adm https://127.0.0.1:41823
mk "$tmp/k/kind-localhost" kind-adm https://localhost:6443
mk "$tmp/k/kind-v6" kind-adm 'https://[::1]:6443'
ok "$tmp/k/kind-ok" "" "a kind context on 127.0.0.1"
ok "$tmp/k/kind-localhost" "" "a kind context on localhost"
ok "$tmp/k/kind-v6" "" "a kind context on ::1"

mk "$tmp/k/prod" arn:aws:eks:eu-central-1:123456789012:cluster/ryft https://ABC.gr7.eu-central-1.eks.amazonaws.com
no "$tmp/k/prod" "" "an EKS context"
mk "$tmp/k/kind-remote" kind-adm https://10.0.0.5:6443
no "$tmp/k/kind-remote" "" "a kind-named context on a remote server"
mk "$tmp/k/kind-evil" kind-adm https://127.0.0.1.example.com:6443
no "$tmp/k/kind-evil" "" "a server whose name merely starts with 127.0.0.1"
mk "$tmp/k/other" minikube https://127.0.0.1:8443
no "$tmp/k/other" "" "a local context that is not kind-*"
no "$tmp/k/missing" "" "a file that does not exist"
no "$tmp/k/kind-ok:$tmp/k/prod" "" "a list of files"
if ( unset KUBECONFIG; HOME="$tmp/home" kind_guard ) >/dev/null 2>&1; then fail "expected the guard to refuse an unset KUBECONFIG"; fi

# Two contexts in the file: refused even when the current one is fine.
cp "$tmp/k/kind-ok" "$tmp/k/two"
KUBECONFIG="$tmp/k/two" kubectl config set-cluster c-prod --server https://ABC.eks.amazonaws.com >/dev/null
KUBECONFIG="$tmp/k/two" kubectl config set-context prod --cluster c-prod --user u-kind-adm >/dev/null
no "$tmp/k/two" "" "a file with a second context"

# ~/.kube is never a kind file, however harmless it looks.
cp "$tmp/k/kind-ok" "$tmp/home/.kube/config"
no "$tmp/home/.kube/config" "$tmp/home" "a file in ~/.kube"
ln -s "$tmp/home/.kube" "$tmp/k/link"
no "$tmp/k/link/config" "$tmp/home" "a path through a link to ~/.kube"

# The wrappers name the kubeconfig and the context explicitly.
( KUBECONFIG="$tmp/k/kind-ok" HOME="$tmp/home" kind_guard
  kubectl() { printf '%s\n' "$*"; }; helm() { printf '%s\n' "$*"; }
  kc get ns | grep -q -- "--kubeconfig $tmp/k/kind-ok --context kind-adm get ns" || exit 1
  hc list | grep -q -- "--kubeconfig $tmp/k/kind-ok --kube-context kind-adm list" || exit 1 ) || fail "kc/hc do not pass the kubeconfig and the context"

echo "✓ kind_guard_test"
