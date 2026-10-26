#!/usr/bin/env bash
# shellcheck shell=bash
# Sourced by scripts/admission_e2e.sh and scripts/admission_ci.sh. The kubectl of this machine's
# own kubeconfig can point at production, so every cluster call of these scripts goes through the
# wrappers below, which pass the kubeconfig and the context explicitly, and kind_guard refuses to
# let anything run unless that kubeconfig is a kind cluster's own:
#   - KUBECONFIG is set, names exactly one existing file (no list), and that file is not in
#     ~/.kube (kind's own file lives in a temporary directory: `kind create cluster --kubeconfig`);
#   - its current context is called kind-*, and the file holds no other context;
#   - the context's server is on this machine (127.0.0.1, localhost or ::1).
# After kind_guard: kc (kubectl), hc (helm) and KIND_CONTEXT. Nothing else may talk to a cluster.
# The guard calls kubectl only for `config` subcommands (they read the file, never a cluster).

kind_guard_fail() {
  echo "kind_guard: refusing to run: $*" >&2
  return 1
}

kind_guard() {
  local cfg=${KUBECONFIG:-} ctx server host count dir real home
  [ -n "$cfg" ] || { kind_guard_fail "KUBECONFIG is not set (it must be the kind cluster's own file)"; return 1; }
  case "$cfg" in *:*) kind_guard_fail "KUBECONFIG is a list ('$cfg'): it must name exactly one file"; return 1 ;; esac
  [ -f "$cfg" ] || { kind_guard_fail "KUBECONFIG '$cfg' is not a file"; return 1; }
  dir=$(cd "$(dirname "$cfg")" && pwd -P) || { kind_guard_fail "cannot resolve $cfg"; return 1; }
  real="$dir/$(basename "$cfg")"
  # ~/.kube, with links resolved on both sides.
  home=$(cd "${HOME:-/nonexistent}" 2>/dev/null && pwd -P) || home=/nonexistent
  case "$real" in
    "$home"/.kube|"$home"/.kube/*) kind_guard_fail "KUBECONFIG is in ~/.kube: a kind cluster's file belongs in a temporary directory"; return 1 ;;
  esac
  ctx=$(kubectl --kubeconfig "$cfg" config current-context 2>/dev/null) || { kind_guard_fail "no current context in $cfg"; return 1; }
  case "$ctx" in kind-?*) ;; *) kind_guard_fail "current context '$ctx' is not a kind-* context"; return 1 ;; esac
  count=$(kubectl --kubeconfig "$cfg" config get-contexts -o name 2>/dev/null | wc -l | tr -d ' ')
  [ "$count" = 1 ] || { kind_guard_fail "$cfg holds $count contexts, a kind cluster's file holds exactly one"; return 1; }
  server=$(kubectl --kubeconfig "$cfg" config view --minify --context "$ctx" -o 'jsonpath={.clusters[0].cluster.server}' 2>/dev/null) || server=
  host=${server#*://}; host=${host%%/*}
  case "$host" in
    \[::1\]*) ;;
    127.0.0.1|127.0.0.1:*|localhost|localhost:*) ;;
    *) kind_guard_fail "the server of '$ctx' is not on this machine"; return 1 ;;
  esac
  KUBECONFIG=$real
  export KUBECONFIG
  KIND_CONTEXT=$ctx
  export KIND_CONTEXT
}

kc() { kubectl --kubeconfig "$KUBECONFIG" --context "$KIND_CONTEXT" "$@"; }
hc() { helm --kubeconfig "$KUBECONFIG" --kube-context "$KIND_CONTEXT" "$@"; }
