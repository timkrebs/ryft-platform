#!/usr/bin/env bash
# Compares secrets without showing them (the move from HCP Vault to the in-cluster Vault): HCP Vault
# against the in-cluster Vault before a cutover, and a service's synced Secret against Vault around
# it. It prints key names and one of `equal`, `differs`, `missing in <side>` per key, never a value,
# a token or a hash.
#
#   scripts/vault_diff.sh --context <ctx> --kv <svc>...       HCP admin/ryft/<svc> kv/<path> vs kv-<svc>/<path>
#   scripts/vault_diff.sh --context <ctx> --all               the same for every service, plus operator/demo
#                                                             vs kv-operator/demo
#   scripts/vault_diff.sh --context <ctx> --secret <svc>...   the service's synced Secrets (ryft-secrets; the
#                                                             broker's amqp-user-*) vs the in-cluster Vault
#                         [--vault hcp]                       ... vs HCP instead
#   scripts/vault_diff.sh --list                              services, namespaces, Vault paths, Secret names
#
# A cutover, per service: before the switch `--kv <svc>` and `--secret <svc> --vault hcp` are both
# equal (the Secret holds HCP's bytes and HCP equals the in-cluster Vault); after it, once the
# VaultStaticSecret is SecretSynced at its new generation, `--secret <svc>` is equal (the Secret holds
# the in-cluster Vault's bytes). Nothing is kept between the runs.
#
# The services and their paths come from the generated VSO objects in
# deploy/k8s/overlays/vault-namespaces/<svc>/vault.yaml (VAULT_DIFF_OVERLAYS overrides the directory).
#
# --context is required and must be one of `kubectl config get-contexts -o name`; kubectl's current
# context and KUBE_CONTEXT are never used (production is ryft-prod-euc1). The script starts no
# port-forward: run your own, e.g. `kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200`.
#
#   --hcp-addr URL    or VAULT_ADDR_HCP. While any generated VaultConnection still points at HCP, the
#                     address is that one (the default) and any other is refused; after that only
#                     https://<name>.hashicorp.cloud[:8200]
#   --k8s-addr URL    or VAULT_ADDR_K8S, default https://127.0.0.1:8200: your port-forward (https on
#                     127.0.0.1, localhost or [::1] only)
#   --k8s-cacert F    or VAULT_CACERT: the in-cluster Vault's CA (required; nothing is ever unverified)
#   --hcp-cacert F    or VAULT_CACERT_HCP: a CA for HCP (default: the system's trust store; CURL_CA_BUNDLE,
#                     SSL_CERT_FILE and SSL_CERT_DIR are ignored)
#   HCP_VAULT_NAMESPACE_ROOT   default admin/ryft (HCP's namespaces are admin/ryft/<svc>)
# Both addresses are shown (`-> HCP: <host>`, `-> in-cluster: <host>`) before a token is asked for.
#
# Tokens: asked for on the terminal, without echo (HCP first, then the in-cluster Vault). Use
# dedicated read-only, short-lived tokens, never your admin token, and revoke them afterwards:
#   in-cluster  a policy with "read" on kv-<svc>/data/* for each service and on kv-operator/data/demo
#               (a Vault glob takes `*` only at the end: list the mounts)
#   HCP         in each admin/ryft/<svc>: "read" on kv/data/*; in admin/ryft: "read" on operator/data/demo
#   create      vault token create -policy=<it> -ttl=30m -use-limit=<the reads of the run, plus one>
#               (one read per path and Vault: `--list` shows the paths; --all adds operator/demo)
#   afterwards  vault token revoke -self   (with that token)
#
# Safety: no `set -x` (switched off first, even when the caller exports it); umask 077; no core
# dumps; the tokens live in unexported shell variables only and reach curl as a config on its stdin
# (`curl -q --config -`, ~/.curlrc ignored), never in argv, an environment or a file; curl uses no
# proxy, follows no redirect and writes no TLS key log; each value goes from curl or kubectl
# straight into scripts/lib/vault_diff.py, which turns it into an HMAC-SHA256 with a random per-run
# salt (on its stdin, never in argv) and compares those. No file is written.
#
# Exit 0: everything equal. 1: a difference (anything but `equal`), or no key compared at all.
# 2: refused, bad usage, or a side could not be read (it stops there: nothing unread passes as equal).

# First, before anything runs: a caller's `bash -x`, SHELLOPTS=xtrace or allexport must not trace
# or export the tokens below.
set +xav
hcp_tok=${HCP_VAULT_TOKEN:-}
ce_tok=${CE_VAULT_TOKEN:-}
unset HCP_VAULT_TOKEN CE_VAULT_TOKEN VAULT_TOKEN
declare +x hcp_tok ce_tok
hcp_env=""; [ -z "$hcp_tok" ] || hcp_env=1
ce_env=""; [ -z "$ce_tok" ] || ce_env=1
# What the environment could otherwise change: the trust anchor, where curl writes TLS keys, which
# directory `cd` lands in.
unset CURL_CA_BUNDLE SSL_CERT_FILE SSL_CERT_DIR SSLKEYLOGFILE CDPATH
set -euo pipefail
umask 077
ulimit -c 0

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/.." && pwd)
OVERLAYS=${VAULT_DIFF_OVERLAYS:-$ROOT/deploy/k8s/overlays/vault-namespaces}
NS_ROOT=${HCP_VAULT_NAMESPACE_ROOT:-admin/ryft}
hcp_addr=${VAULT_ADDR_HCP:-}
k8s_addr=${VAULT_ADDR_K8S:-https://127.0.0.1:8200}
k8s_ca=${VAULT_CACERT:-}
hcp_ca=${VAULT_CACERT_HCP:-}
# Only --context names the cluster here.
unset KUBE_CONTEXT

salt=""
prompted=""
cleanup() {
  # A prompt interrupted by a signal must not leave the terminal without echo.
  if [ -n "$prompted" ]; then stty echo </dev/tty 2>/dev/null || true; fi
  hcp_tok=""; ce_tok=""; salt=""
  unset hcp_tok ce_tok salt
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 131' QUIT
trap 'exit 143' TERM

die() { echo "✗ vault_diff: $*" >&2; exit 2; }
usage() {
  echo "usage: scripts/vault_diff.sh --context <ctx> (--kv <svc>... | --all | --secret <svc>... [--vault hcp|k8s]) | --list | --help" >&2
  exit 2
}
py() { python3 -I "$HERE/lib/vault_diff.py" "$@"; }
matches() { printf '%s' "$1" | grep -Eq -- "$2"; }  # <text> <ERE>: the text on stdin, not in argv

ctx=""
mode=""
all=""
list=""
vault=k8s
services=()
while [ $# -gt 0 ]; do
  case "$1" in
    --context) [ $# -ge 2 ] && [ -n "$2" ] || usage; ctx=$2; shift 2 ;;
    --context=?*) ctx=${1#--context=}; shift ;;
    --kv) [ -z "$mode" ] || [ "$mode" = kv ] || usage; mode=kv; shift ;;
    --secret) [ -z "$mode" ] || [ "$mode" = secret ] || usage; mode=secret; shift ;;
    --all) all=1; shift ;;
    --list) list=1; shift ;;
    --vault) [ $# -ge 2 ] || usage; vault=$2; shift 2 ;;
    --hcp-addr) [ $# -ge 2 ] || usage; hcp_addr=$2; shift 2 ;;
    --k8s-addr) [ $# -ge 2 ] || usage; k8s_addr=$2; shift 2 ;;
    --hcp-cacert) [ $# -ge 2 ] || usage; hcp_ca=$2; shift 2 ;;
    --k8s-cacert) [ $# -ge 2 ] || usage; k8s_ca=$2; shift 2 ;;
    -h|--help) awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; exit 0 ;;
    -*) usage ;;
    *) services+=("$1"); shift ;;
  esac
done

# Everything that needs no cluster and no Vault is checked first.
if [ -n "$list" ]; then
  [ -z "$mode$all" ] && [ ${#services[@]} -eq 0 ] || usage
  for svc in $(py services "$OVERLAYS"); do
    py objects "$OVERLAYS" "$svc" | while IFS=$'\t' read -r ns path dest; do
      printf '%s  %s  %s/%s  kv-%s/%s\n' "$svc" "$ns" "$ns" "$dest" "$svc" "$path"
    done
  done
  exit 0
fi
# No default, not KUBE_CONTEXT (removed above), not kubectl's current context: production is one
# `kubectl config use-context` away on the operator's machine.
[ -n "$ctx" ] || die "--context <ctx> is required (one of \`kubectl config get-contexts -o name\`); kubectl's current context is never used"
[ -n "$mode" ] || { [ -n "$all" ] && mode=kv; } || usage
if [ -n "$all" ]; then
  [ ${#services[@]} -eq 0 ] || usage
  while IFS= read -r svc; do services+=("$svc"); done < <(py services "$OVERLAYS")
  [ "$mode" != kv ] || services+=(operator)
fi
[ ${#services[@]} -gt 0 ] || usage
for svc in "${services[@]}"; do
  case $svc in
    operator) [ "$mode" = kv ] || die "operator has no synced Secret (only --kv)" ;;
    *) matches "$svc" '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' || die "\"$svc\" is not a service name"
       [ -f "$OVERLAYS/$svc/vault.yaml" ] || die "$svc: no generated VSO objects in $OVERLAYS/$svc (scripts/vault_diff.sh --list)" ;;
  esac
done
case $vault in hcp|k8s) ;; *) die "--vault is hcp or k8s" ;; esac
[ "$mode" != kv ] || [ "$vault" = k8s ] || die "--vault is for --secret"

# Which Vaults this run reads.
need_hcp=""; need_k8s=""
if [ "$mode" = kv ]; then need_hcp=1; need_k8s=1
elif [ "$vault" = hcp ]; then need_hcp=1
else need_k8s=1
fi
host() { local h=${1#https://}; printf '%s' "${h%%/*}"; }
if [ -n "$need_hcp" ]; then
  # The HCP token goes to one place: the address the generated VaultConnections use, as long as
  # any of them still points at HCP; after the last cutover, a name under hashicorp.cloud.
  pinned=$(py hcp-address "$OVERLAYS") || die "the generated VaultConnections do not agree on one HCP address"
  hcp_addr=${hcp_addr%/}
  if [ -n "$pinned" ]; then
    [ -n "$hcp_addr" ] || hcp_addr=$pinned
    [ "$hcp_addr" = "$pinned" ] || die "--hcp-addr $hcp_addr is not HCP's address in the generated VaultConnections ($pinned)"
  else
    [ -n "$hcp_addr" ] || die "no HCP address: --hcp-addr or VAULT_ADDR_HCP (no generated VaultConnection points at HCP any more)"
    matches "$hcp_addr" '^https://[a-z0-9.-]+\.hashicorp\.cloud(:8200)?$' || die "--hcp-addr must be https://<name>.hashicorp.cloud[:8200]"
  fi
  [ -z "$hcp_ca" ] || [ -f "$hcp_ca" ] || die "--hcp-cacert $hcp_ca: no such file"
fi
if [ -n "$need_k8s" ]; then
  k8s_addr=${k8s_addr%/}
  matches "$k8s_addr" '^https://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$' \
    || die "--k8s-addr must be your port-forward: https://127.0.0.1, localhost or [::1], with a port"
  [ -n "$k8s_ca" ] || die "no CA for the in-cluster Vault: --k8s-cacert or VAULT_CACERT"
  [ -f "$k8s_ca" ] || die "--k8s-cacert $k8s_ca: no such file"
fi
case $NS_ROOT in ""|/*|*/|*[!A-Za-z0-9/_-]*) die "HCP_VAULT_NAMESPACE_ROOT is not a namespace path" ;; esac

# The cluster: named, listed, never the current context (scripts/lib/kube.sh, D150).
# shellcheck source=lib/kube.sh
. "$HERE/lib/kube.sh"
kube_context "$ctx" || exit 2
[ -z "$need_hcp" ] || echo "-> HCP: $(host "$hcp_addr")" >&2
[ -z "$need_k8s" ] || echo "-> in-cluster: $(host "$k8s_addr")" >&2

# The tokens: asked for on the terminal without echo, read straight into the variable (no
# subshell). The environment still works, and says so.
token_ok() { # <token> <name>: refuse what curl's config syntax or a header could not carry, without echoing it
  case $1 in
    "") die "empty $2" ;;
    *[!A-Za-z0-9._+/=-]*) die "$2 has characters a Vault token does not have" ;;
  esac
}
no_tty() { die "no terminal to ask for $1"; }
if [ -n "$need_hcp" ]; then
  if [ -n "$hcp_env" ]; then
    echo "HCP_VAULT_TOKEN: taken from the environment (the prompt is safer: scripts/vault_diff.sh --help)" >&2
  else
    [ -r /dev/tty ] || no_tty HCP_VAULT_TOKEN
    prompted=1
    IFS= read -rs -p "HCP_VAULT_TOKEN: " hcp_tok </dev/tty || no_tty HCP_VAULT_TOKEN
    echo >&2
  fi
  token_ok "$hcp_tok" HCP_VAULT_TOKEN
fi
if [ -n "$need_k8s" ]; then
  if [ -n "$ce_env" ]; then
    echo "CE_VAULT_TOKEN: taken from the environment (the prompt is safer: scripts/vault_diff.sh --help)" >&2
  else
    [ -r /dev/tty ] || no_tty CE_VAULT_TOKEN
    prompted=1
    IFS= read -rs -p "CE_VAULT_TOKEN: " ce_tok </dev/tty || no_tty CE_VAULT_TOKEN
    echo >&2
  fi
  token_ok "$ce_tok" CE_VAULT_TOKEN
fi
# A token this run does not need is dropped.
[ -n "$need_hcp" ] || hcp_tok=""
[ -n "$need_k8s" ] || ce_tok=""

# The salt: 32 random bytes, in this shell only (each hasher reads it from its stdin).
salt=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
matches "$salt" '^[0-9a-f]{64}$' || die "no random salt"

# curl's config on its stdin: printf is a builtin, so the token is in no argv and no file.
token_config() { # <hcp|k8s> <namespace or "">
  if [ "$1" = hcp ]; then
    printf 'header = "X-Vault-Token: %s"\n' "$hcp_tok"
    printf 'header = "X-Vault-Namespace: %s"\n' "$2"
  else
    printf 'header = "X-Vault-Token: %s"\n' "$ce_tok"
  fi
}
# vault_hashes <hcp|k8s> <namespace or ""> <mount> <path>: the fingerprints of one kv-v2 secret.
vault_hashes() {
  local addr ca
  if [ "$1" = hcp ]; then addr=$hcp_addr; ca=$hcp_ca; else addr=$k8s_addr; ca=$k8s_ca; fi
  local tls=()
  [ -z "$ca" ] || tls=(--cacert "$ca")
  {
    printf '%s\n' "$salt"
    # -q first: no ~/.curlrc (a `verbose` there would print the token header). No proxy from the
    # environment (it would see the request, and route even loopback). No -L: a token is never
    # sent to where a redirect points. A failed call leaves status 000 for the hasher.
    token_config "$1" "$2" | curl -q --config - --silent --show-error --proto =https --max-redirs 0 --noproxy '*' \
      --connect-timeout 10 --max-time 30 ${tls[@]+"${tls[@]}"} --header 'Accept: application/json' \
      --write-out '\n%{http_code}' -- "$addr/v1/$3/data/$4" || true
  } | py vault "$1"
}
# secret_hashes <namespace> <name>: the fingerprints of a Secret's data (kubectl's JSON never reaches the terminal).
secret_hashes() {
  { printf '%s\n' "$salt"; k -n "$1" get secret "$2" --ignore-not-found -o json; } | py secret
}

total=0; bad=0
report() { # <compare output>: printed, and counted
  local line
  while IFS= read -r line; do
    [ -n "$line" ] || continue
    printf '%s\n' "$line"
    total=$((total + 1))
    case $line in *"  equal") ;; *) bad=$((bad + 1)) ;; esac
  done < <(printf '%s\n' "$1")
}
compare() { # <svc> <obj> <A> <B> <A's hashes> <B's hashes>
  local out rc=0
  out=$(printf '%s\n#--\n%s\n' "$5" "$6" | py compare "$1" "$2" "$3" "$4") || rc=$?
  [ "$rc" -le 1 ] || die "$1 $2: the comparison failed"
  report "$out"
}

for svc in "${services[@]}"; do
  if [ "$svc" = operator ]; then
    objs=$(printf '%s\t%s\t%s\n' - demo -)
  else
    objs=$(py objects "$OVERLAYS" "$svc") || die "$svc: cannot read its generated VSO objects"
  fi
  while IFS=$'\t' read -r ns path dest; do
    [ -n "$path" ] || continue
    if [ "$mode" = kv ]; then
      if [ "$svc" = operator ]; then
        a=$(vault_hashes hcp "$NS_ROOT" operator "$path") || die "operator/$path: HCP could not be read"
        b=$(vault_hashes k8s "" kv-operator "$path") || die "kv-operator/$path: the in-cluster Vault could not be read"
      else
        a=$(vault_hashes hcp "$NS_ROOT/$svc" kv "$path") || die "$svc $path: HCP could not be read"
        b=$(vault_hashes k8s "" "kv-$svc" "$path") || die "$svc $path: the in-cluster Vault could not be read"
      fi
      compare "$svc" "$path" HCP k8s "$a" "$b"
      continue
    fi
    s=$(secret_hashes "$ns" "$dest") || die "$ns/$dest: the Secret could not be read"
    if [ "$vault" = hcp ]; then
      a=$(vault_hashes hcp "$NS_ROOT/$svc" kv "$path") || die "$svc $path: HCP could not be read"
    else
      a=$(vault_hashes k8s "" "kv-$svc" "$path") || die "$svc $path: the in-cluster Vault could not be read"
    fi
    compare "$svc" "$dest" Vault Secret "$a" "$s"
  done < <(printf '%s\n' "$objs")
done

if [ "$total" -eq 0 ]; then
  echo "vault_diff: nothing to compare (no keys on either side)" >&2
  exit 1
fi
if [ "$bad" -gt 0 ]; then
  echo "vault_diff: $total keys, $bad not equal" >&2
  exit 1
fi
echo "vault_diff: $total keys, all equal" >&2
