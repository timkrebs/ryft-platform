#!/usr/bin/env bash
# shellcheck shell=bash
# Sourced by scripts/admission_e2e.sh. The kubectl of this machine's own kubeconfig can point at
# production, so every cluster call of these scripts goes through the wrappers below, which pass the
# kubeconfig and the context explicitly, and kind_guard refuses to let anything run unless that
# kubeconfig is a kind cluster's own:
#   - KUBECONFIG is set, names exactly one existing file (no list), and that file, with links resolved
#     (the directory and the file itself), is not in ~/.kube (kind's own file lives in a temporary
#     directory: `kind create cluster --kubeconfig`);
#   - its current context is called kind-*, and the file holds no other context;
#   - the context's server is exactly https://127.0.0.1:<port> or https://[::1]:<port> (no user
#     information, no path, no proxy-url), and the user runs no plugin (exec, auth-provider);
#   - HELM_KUBE* variables, which would override helm's connection, are unset.
# kind_guard_verify_with_kind <name> additionally compares the file with `kind get kubeconfig` (the
# server and the certificates), for a cluster the user hands over with --kubeconfig.
# After kind_guard: kc, kca (kubectl, the latter as another user) and hc (helm), and KIND_CONTEXT.
# Nothing else may talk to a cluster. The wrappers refuse any argument that would choose another
# cluster, user or server (a value of the tree under test reaching the command line cannot override
# the kubeconfig): --kubeconfig, --context, --cluster, --server, -s, --token, --user, --kube-*,
# --as* (impersonation has its own wrapper, kca), --certificate-authority, --client-*, --insecure-*,
# --tls-server-name, --username, --password, --proxy-url, --post-renderer; arguments after a
# standalone `--` are the command of `kubectl exec`, not flags, and are not looked at.
# The guard calls kubectl only for `config` subcommands (they read the file, never a cluster).

kind_guard_fail() {
  echo "kind_guard: refusing to run: $*" >&2
  return 1
}

# kind_guard_args_ok <args...>: no argument before a standalone `--` selects a connection.
kind_guard_args_ok() {
  local a
  for a in "$@"; do
    case "$a" in
      --) return 0 ;;
      --kubeconfig|--kubeconfig=*|--context|--context=*|--cluster|--cluster=*|--server|--server=*|-s|-s?*|--token|--token=*|--user|--user=*|--kube-*|--as|--as=*|--as-*|--certificate-authority|--certificate-authority=*|--client-*|--insecure-*|--tls-server-name|--tls-server-name=*|--username|--username=*|--password|--password=*|--proxy-url|--proxy-url=*|--post-renderer|--post-renderer=*|--post-renderer-*)
        echo "kind_guard: refusing the argument '$a': it would choose a cluster, a user or a plugin" >&2
        return 1 ;;
    esac
  done
  return 0
}

# kind_guard_realpath <path>: the path with every link resolved, the file's own included.
kind_guard_realpath() {
  local f=$1 t n=0
  while [ -L "$f" ]; do
    n=$((n + 1)); [ "$n" -le 20 ] || return 1
    t=$(readlink "$f") || return 1
    case "$t" in /*) f=$t ;; *) f="$(dirname "$f")/$t" ;; esac
  done
  local dir
  dir=$(cd "$(dirname "$f")" 2>/dev/null && pwd -P) || return 1
  printf '%s/%s\n' "$dir" "$(basename "$f")"
}

kind_guard() {
  local cfg=${KUBECONFIG:-} ctx server count real home json v
  # Helm's own connection overrides, which a kubeconfig flag does not beat.
  unset HELM_KUBEAPISERVER HELM_KUBETOKEN HELM_KUBECAFILE HELM_KUBEINSECURE_SKIP_TLS_VERIFY HELM_KUBEASUSER HELM_KUBEASGROUPS HELM_KUBECONTEXT HELM_KUBETLS_SERVER_NAME KUBERNETES_MASTER
  [ -n "$cfg" ] || { kind_guard_fail "KUBECONFIG is not set (it must be the kind cluster's own file)"; return 1; }
  case "$cfg" in *:*) kind_guard_fail "KUBECONFIG is a list ('$cfg'): it must name exactly one file"; return 1 ;; esac
  [ -f "$cfg" ] || { kind_guard_fail "KUBECONFIG '$cfg' is not a file"; return 1; }
  real=$(kind_guard_realpath "$cfg") || { kind_guard_fail "cannot resolve $cfg"; return 1; }
  # ~/.kube, with links resolved on both sides (the home directory may itself be a link).
  home=$(cd "${HOME:-/nonexistent}" 2>/dev/null && pwd -P) || home=/nonexistent
  case "$real" in
    "$home"/.kube|"$home"/.kube/*|"${HOME:-/nonexistent}"/.kube|"${HOME:-/nonexistent}"/.kube/*|*/.kube/*)
      kind_guard_fail "KUBECONFIG is in ~/.kube: a kind cluster's file belongs in a temporary directory"; return 1 ;;
  esac
  ctx=$(kubectl --kubeconfig "$real" config current-context 2>/dev/null) || { kind_guard_fail "no current context in $cfg"; return 1; }
  case "$ctx" in kind-?*) ;; *) kind_guard_fail "current context '$ctx' is not a kind-* context"; return 1 ;; esac
  count=$(kubectl --kubeconfig "$real" config get-contexts -o name 2>/dev/null | wc -l | tr -d ' ')
  [ "$count" = 1 ] || { kind_guard_fail "$cfg holds $count contexts, a kind cluster's file holds exactly one"; return 1; }
  json=$(kubectl --kubeconfig "$real" config view --raw --minify --context "$ctx" -o json 2>/dev/null) || { kind_guard_fail "cannot read $cfg"; return 1; }
  server=$(printf '%s' "$json" | jq -r '.clusters[0].cluster.server // ""')
  # Anchored: no user information (https://127.0.0.1:1@host), no empty port, no path.
  if ! [[ $server =~ ^https://(127\.0\.0\.1|\[::1\]):[0-9]{1,5}$ ]]; then
    kind_guard_fail "the server of '$ctx' ('$server') is not https://127.0.0.1:<port> on this machine"; return 1
  fi
  v=$(printf '%s' "$json" | jq -r '.clusters[0].cluster | has("proxy-url")')
  [ "$v" = false ] || { kind_guard_fail "the cluster of '$ctx' sets proxy-url"; return 1; }
  v=$(printf '%s' "$json" | jq -r '.users[0].user // {} | (has("exec") or has("auth-provider") or has("tokenFile") or has("username"))')
  [ "$v" = false ] || { kind_guard_fail "the user of '$ctx' runs a plugin or names a token file (exec, auth-provider, tokenFile)"; return 1; }
  KUBECONFIG=$real
  export KUBECONFIG
  KIND_CONTEXT=$ctx
  export KIND_CONTEXT
}

# kind_guard_verify_with_kind <cluster name>: the file is what `kind get kubeconfig --name <name>`
# prints (same server and same certificates), not a file that merely looks like one.
kind_guard_verify_with_kind() {
  local name=${1:?} tmp want got
  command -v kind >/dev/null 2>&1 || { kind_guard_fail "kind is not installed: cannot compare the kubeconfig with kind's own"; return 1; }
  tmp=$(mktemp -d) || return 1
  if ! kind get kubeconfig --name "$name" > "$tmp/kind.kubeconfig" 2>"$tmp/err"; then
    kind_guard_fail "kind has no cluster '$name' ($(head -c 200 "$tmp/err"))"; rm -rf "$tmp"; return 1
  fi
  want=$(kubectl --kubeconfig "$tmp/kind.kubeconfig" config view --raw --minify -o json 2>/dev/null | jq -c '[.clusters[0].cluster.server, .clusters[0].cluster["certificate-authority-data"], .users[0].user["client-certificate-data"]]')
  got=$(kubectl --kubeconfig "$KUBECONFIG" config view --raw --minify -o json 2>/dev/null | jq -c '[.clusters[0].cluster.server, .clusters[0].cluster["certificate-authority-data"], .users[0].user["client-certificate-data"]]')
  rm -rf "$tmp"
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    kind_guard_fail "$KUBECONFIG is not the kubeconfig of the kind cluster '$name' (server or certificates differ from kind get kubeconfig)"; return 1
  fi
}

kc() { kind_guard_args_ok "$@" || return 126; kubectl --kubeconfig "$KUBECONFIG" --context "$KIND_CONTEXT" "$@"; }
# kca <user> [group] -- ...: kubectl as another user (and group), still on the kind cluster. The group is
# KCA_GROUP when set.
kca() {
  local who=$1; shift
  kind_guard_args_ok "$@" || return 126
  if [ -n "${KCA_GROUP:-}" ]; then
    kubectl --kubeconfig "$KUBECONFIG" --context "$KIND_CONTEXT" --as "$who" --as-group "$KCA_GROUP" "$@"
  else
    kubectl --kubeconfig "$KUBECONFIG" --context "$KIND_CONTEXT" --as "$who" "$@"
  fi
}
hc() { kind_guard_args_ok "$@" || return 126; helm --kubeconfig "$KUBECONFIG" --kube-context "$KIND_CONTEXT" "$@"; }
