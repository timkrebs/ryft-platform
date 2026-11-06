#!/usr/bin/env bash
# Compares secrets without showing them (the move from HCP Vault to the in-cluster Vault): HCP Vault
# against the in-cluster Vault before a cutover, and a service's synced Secret against Vault (or
# against a fingerprint saved before the switch) after it. It prints key names and one of
# `equal`, `differs`, `missing in <side>` per key, never a value, a token or a hash.
#
#   scripts/vault_diff.sh --context <ctx> --kv <svc>...           HCP admin/ryft/<svc> kv/<path> vs kv-<svc>/<path>
#   scripts/vault_diff.sh --context <ctx> --all                   the same for every service, plus operator/demo
#                                                                 vs kv-operator/demo
#   scripts/vault_diff.sh --context <ctx> --secret <svc>...       the service's synced Secrets (ryft-secrets; the
#                                                                 broker's amqp-user-*) vs the in-cluster Vault
#                         [--vault hcp]                           ... vs HCP instead (before the switch)
#   scripts/vault_diff.sh --context <ctx> --secret <svc> --save <file>     the Secrets' fingerprint, before a switch
#   scripts/vault_diff.sh --context <ctx> --secret <svc> --against <file>  the Secrets now vs that fingerprint
#   scripts/vault_diff.sh --list                                  services, namespaces, Vault paths, Secret names
#
# The services and their paths come from the generated VSO objects in
# deploy/k8s/overlays/vault-namespaces/<svc>/vault.yaml (VAULT_DIFF_OVERLAYS overrides the directory).
#
# --context is required and must be one of `kubectl config get-contexts -o name`; kubectl's current
# context and KUBE_CONTEXT are never used (production is ryft-prod-euc1). The script starts no
# port-forward: run your own, e.g. `kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200`.
#
#   --hcp-addr URL    or VAULT_ADDR_HCP: HCP Vault (https only)
#   --k8s-addr URL    or VAULT_ADDR_K8S, default https://127.0.0.1:8200: the in-cluster Vault through your port-forward
#   --k8s-cacert F    or VAULT_CACERT: the in-cluster Vault's CA (required for it; nothing is ever unverified)
#   --hcp-cacert F    or VAULT_CACERT_HCP: a CA for HCP (default: the system's trust)
#   HCP_VAULT_TOKEN, CE_VAULT_TOKEN   the tokens (read once, then removed from the environment, so no
#                     child process inherits them); when a needed one is not set, it is asked for on
#                     the terminal without echo. The prompt is the safer way: a token handed in by the
#                     environment stays in this process's initial environment, which `ps e` shows to
#                     your own user and to root while it runs.
#   HCP_VAULT_NAMESPACE_ROOT          default admin/ryft (HCP's namespaces are admin/ryft/<svc>)
#
# Safety: no `set -x` (switched off first, even when the caller exports it); umask 077; the tokens
# live in unexported shell variables only and reach curl as a config on its stdin (`curl -q --config -`,
# ~/.curlrc ignored), never in argv, an environment or a file; curl follows no redirect; each value
# goes from curl or kubectl straight into scripts/lib/vault_diff.py, which turns it into an HMAC-SHA256
# with a random per-run salt (on its stdin, never in argv) and compares those. No file is written
# except the --save fingerprint (mode 0600, never overwritten): it holds the salt and the salted
# hashes, so a value with little entropy (a flag like on/off) can be guessed from it; keep it private
# and delete it after the cutover.
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
set -euo pipefail
umask 077

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
save=""
save_made=""
saved_ok=""
cleanup() {
  # A half-written fingerprint is no fingerprint (and a file this run did not create is never touched).
  if [ -n "$save_made" ] && [ -z "$saved_ok" ]; then rm -f -- "$save"; fi
  hcp_tok=""; ce_tok=""; salt=""
  unset hcp_tok ce_tok salt
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

die() { echo "✗ vault_diff: $*" >&2; exit 2; }
usage() {
  echo "usage: scripts/vault_diff.sh --context <ctx> (--kv <svc>... | --all | --secret <svc>... [--vault hcp|k8s] [--save F | --against F]) | --list | --help" >&2
  exit 2
}
py() { python3 -I "$HERE/lib/vault_diff.py" "$@"; }

ctx=""
mode=""
all=""
list=""
vault=k8s
against=""
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
    --save) [ $# -ge 2 ] && [ -n "$2" ] || usage; save=$2; shift 2 ;;
    --against) [ $# -ge 2 ] && [ -n "$2" ] || usage; against=$2; shift 2 ;;
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
  [ -z "$mode$all$save$against" ] && [ ${#services[@]} -eq 0 ] || usage
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
    *) printf '%s' "$svc" | grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' || die "\"$svc\" is not a service name"
       [ -f "$OVERLAYS/$svc/vault.yaml" ] || die "$svc: no generated VSO objects in $OVERLAYS/$svc (scripts/vault_diff.sh --list)" ;;
  esac
done
case $vault in hcp|k8s) ;; *) die "--vault is hcp or k8s" ;; esac
if [ -n "$save$against" ]; then
  [ "$mode" = secret ] && [ ${#services[@]} -eq 1 ] && [ -z "$all" ] || die "--save and --against take --secret and one service"
  [ -z "$save" ] || [ -z "$against" ] || die "--save or --against, not both"
  [ -z "$save" ] || [ ! -e "$save" ] || die "$save exists: a fingerprint is never overwritten"
  [ -z "$against" ] || [ -f "$against" ] || die "$against: no such fingerprint"
fi
[ "$mode" != kv ] || [ "$vault" = k8s ] || die "--vault is for --secret"

# Which Vaults this run reads.
need_hcp=""; need_k8s=""
if [ "$mode" = kv ]; then need_hcp=1; need_k8s=1
elif [ -z "$save$against" ]; then
  if [ "$vault" = hcp ]; then need_hcp=1; else need_k8s=1; fi
fi
check_addr() { # <name> <url>
  case $2 in
    https://*) ;;
    *) die "$1 must be an https:// address" ;;
  esac
  case $2 in *[!A-Za-z0-9.:/_\[\]-]*) die "$1 is not a plain address" ;; esac
}
if [ -n "$need_hcp" ]; then
  [ -n "$hcp_addr" ] || die "no HCP address: --hcp-addr or VAULT_ADDR_HCP"
  check_addr "the HCP address" "$hcp_addr"; hcp_addr=${hcp_addr%/}
  [ -z "$hcp_ca" ] || [ -f "$hcp_ca" ] || die "--hcp-cacert $hcp_ca: no such file"
fi
if [ -n "$need_k8s" ]; then
  check_addr "the in-cluster Vault's address" "$k8s_addr"; k8s_addr=${k8s_addr%/}
  [ -n "$k8s_ca" ] || die "no CA for the in-cluster Vault: --k8s-cacert or VAULT_CACERT"
  [ -f "$k8s_ca" ] || die "--k8s-cacert $k8s_ca: no such file"
fi
case $NS_ROOT in ""|/*|*/|*[!A-Za-z0-9/_-]*) die "HCP_VAULT_NAMESPACE_ROOT is not a namespace path" ;; esac

# The cluster: named, listed, never the current context (scripts/lib/kube.sh, D150).
# shellcheck source=lib/kube.sh
. "$HERE/lib/kube.sh"
kube_context "$ctx" || exit 2

# The tokens: from the environment (already removed from it) or the terminal, without echo.
ask() { # <name>: the answer on stdout (a command substitution: a fork, no new program, no argv)
  [ -r /dev/tty ] || die "no terminal to ask for $1: set it in the environment"
  local v=""
  IFS= read -rs -p "$1: " v </dev/tty || die "no $1"
  echo >&2
  printf '%s' "$v"
}
token_ok() { # <token> <name>: refuse what curl's config syntax or a header could not carry, without echoing it
  case $1 in
    "") die "empty $2" ;;
    *[!A-Za-z0-9._+/=-]*) die "$2 has characters a Vault token does not have" ;;
  esac
}
if [ -n "$need_hcp" ]; then
  [ -n "$hcp_tok" ] || hcp_tok=$(ask HCP_VAULT_TOKEN)
  token_ok "$hcp_tok" HCP_VAULT_TOKEN
fi
if [ -n "$need_k8s" ]; then
  [ -n "$ce_tok" ] || ce_tok=$(ask CE_VAULT_TOKEN)
  token_ok "$ce_tok" CE_VAULT_TOKEN
fi
# A token this run does not need is dropped.
[ -n "$need_hcp" ] || hcp_tok=""
[ -n "$need_k8s" ] || ce_tok=""

# The salt: 32 random bytes, in this shell only (each hasher reads it from its stdin).
if [ -n "$against" ]; then
  salt=$(sed -n 's/^salt \([0-9a-f]\{64\}\)$/\1/p' "$against")
  [ -n "$salt" ] || die "$against has no salt: not a fingerprint of this script"
  grep -Fqx "svc ${services[0]}" "$against" || die "$against is not a fingerprint of ${services[0]}"
else
  salt=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
  printf '%s' "$salt" | grep -Eq '^[0-9a-f]{64}$' || die "no random salt"
fi

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
    # -q first: no ~/.curlrc (a `verbose` there would print the token header). No -L: a token is
    # never sent to where a redirect points. A failed call leaves status 000 for the hasher.
    token_config "$1" "$2" | curl -q --config - --silent --show-error --proto =https --max-redirs 0 \
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

if [ -n "$save" ]; then
  set -C
  : > "$save" || die "cannot create $save"
  set +C
  save_made=1
  printf '# scripts/vault_diff.sh fingerprint: HMAC-SHA256 per key with the salt below, no values\nsalt %s\nsvc %s\n' \
    "$salt" "${services[0]}" >> "$save"
fi

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
    if [ -n "$save" ]; then
      while IFS= read -r line; do printf '%s\t%s\n' "$dest" "$line" >> "$save"; done < <(printf '%s\n' "$s")
      echo "$svc  $dest  saved" >&2
    elif [ -n "$against" ]; then
      a=$(sed -n "s/^${dest}"$'\t'"//p" "$against")
      [ -n "$a" ] || die "$against holds nothing for $dest"
      compare "$svc" "$dest" saved Secret "$a" "$s"
    elif [ "$vault" = hcp ]; then
      a=$(vault_hashes hcp "$NS_ROOT/$svc" kv "$path") || die "$svc $path: HCP could not be read"
      compare "$svc" "$dest" Vault Secret "$a" "$s"
    else
      a=$(vault_hashes k8s "" "kv-$svc" "$path") || die "$svc $path: the in-cluster Vault could not be read"
      compare "$svc" "$dest" Vault Secret "$a" "$s"
    fi
  done < <(printf '%s\n' "$objs")
done

if [ -n "$save" ]; then
  saved_ok=1
  echo "vault_diff: fingerprint of ${services[0]} in $save (salted hashes only; delete it after the cutover)" >&2
  exit 0
fi
if [ "$total" -eq 0 ]; then
  echo "vault_diff: nothing to compare (no keys on either side)" >&2
  exit 1
fi
if [ "$bad" -gt 0 ]; then
  echo "vault_diff: $total keys, $bad not equal" >&2
  exit 1
fi
echo "vault_diff: $total keys, all equal" >&2
