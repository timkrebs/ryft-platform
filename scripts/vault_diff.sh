#!/usr/bin/env bash
# Compares a service's synced Secrets with the in-cluster Vault without showing them: per key, the
# Secret the Vault Secrets Operator wrote (ryft-secrets; the broker's amqp-user-*) against the kv-v2
# secret it comes from (kv-<svc>/<path>). It prints key names and one of `equal`, `differs`,
# `missing in <side>` per key, never a value, a token or a hash.
#
#   scripts/vault_diff.sh --context <ctx> <svc>...   those services' synced Secrets vs the in-cluster Vault
#   scripts/vault_diff.sh --context <ctx> --all      every service with generated VSO objects
#   scripts/vault_diff.sh --list                     services, namespaces, Secret names, Vault paths
#
# When: after the first sync from an empty cluster (every Secret holds what Vault holds, and nothing
# applied by hand shadows one), and after a write to Vault (a rotation, an infra/vault apply) once the
# VaultStaticSecret has synced again (refreshAfter 1h), before relying on the new value. The in-cluster
# Vault is the only Vault (D207); there is no Vault-against-Vault comparison any more. `--secret`, this
# mode's old flag, is still accepted and changes nothing.
#
# The services and their paths come from the generated VSO objects in
# deploy/k8s/overlays/vault-namespaces/<svc>/vault.yaml (VAULT_DIFF_OVERLAYS overrides the directory).
#
# --context is required and must be one of `kubectl config get-contexts -o name`; kubectl's current
# context and KUBE_CONTEXT are never used (production is ryft-prod-euc1). The script starts no
# port-forward: run your own, e.g. `kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200`.
#
#   --k8s-addr URL    or VAULT_ADDR_K8S, default https://127.0.0.1:8200: your port-forward (https on
#                     127.0.0.1, localhost or [::1] only)
#   --k8s-cacert F    or VAULT_CACERT: the in-cluster Vault's CA (required; nothing is ever unverified)
# The address is shown (`-> in-cluster: <host>`) before the token is asked for.
#
# Token: asked for on the terminal, without echo. Use a dedicated read-only, short-lived token, never
# your admin token, and revoke it afterwards:
#   policy      "read" on kv-<svc>/data/* for each service compared (a Vault glob takes `*` only at
#               the end: list the mounts)
#   create      vault token create -policy=<it> -ttl=30m -use-limit=<the reads of the run, plus one>
#               (one read per Vault path: `--list` shows them)
#   afterwards  vault token revoke -self   (with that token)
# CE_VAULT_TOKEN in the environment works too, and says so.
#
# Safety: no `set -x` (switched off first, even when the caller exports it); umask 077; no core
# dumps; the token lives in an unexported shell variable only and reaches curl as a config on its
# stdin (`curl -q --config -`, ~/.curlrc ignored), never in argv, an environment or a file; curl uses
# no proxy, follows no redirect and writes no TLS key log; each value goes from curl or kubectl
# straight into scripts/lib/vault_diff.py, which turns it into an HMAC-SHA256 with a random per-run
# salt (on its stdin, never in argv) and compares those. No file is written.
#
# Exit 0: everything equal. 1: a difference (anything but `equal`), or no key compared at all.
# 2: refused, bad usage, or a side could not be read (it stops there: nothing unread passes as equal).

# First, before anything runs: a caller's `bash -x`, SHELLOPTS=xtrace or allexport must not trace
# or export the token below.
set +xav
ce_tok=${CE_VAULT_TOKEN:-}
# A HCP_VAULT_TOKEN left in a shell from before the rebuild is never used, and dropped like
# VAULT_TOKEN so that no process started here inherits it.
unset CE_VAULT_TOKEN VAULT_TOKEN HCP_VAULT_TOKEN
declare +x ce_tok
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
k8s_addr=${VAULT_ADDR_K8S:-https://127.0.0.1:8200}
k8s_ca=${VAULT_CACERT:-}
# Only --context names the cluster here.
unset KUBE_CONTEXT

salt=""
prompted=""
cleanup() {
  # A prompt interrupted by a signal must not leave the terminal without echo.
  if [ -n "$prompted" ]; then stty echo </dev/tty 2>/dev/null || true; fi
  ce_tok=""; salt=""
  unset ce_tok salt
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 131' QUIT
trap 'exit 143' TERM

die() { echo "✗ vault_diff: $*" >&2; exit 2; }
usage() {
  echo "usage: scripts/vault_diff.sh --context <ctx> (<svc>... | --all) | --list | --help" >&2
  exit 2
}
# The comparison against another Vault went with HCP Vault (D207).
gone() { die "$1 is gone with HCP Vault (D207): only the synced Secrets against the in-cluster Vault are compared (--help)"; }
py() { python3 -I "$HERE/lib/vault_diff.py" "$@"; }
matches() { printf '%s' "$1" | grep -Eq -- "$2"; }  # <text> <ERE>: the text on stdin, not in argv

ctx=""
all=""
list=""
services=()
while [ $# -gt 0 ]; do
  case "$1" in
    --context) [ $# -ge 2 ] && [ -n "$2" ] || usage; ctx=$2; shift 2 ;;
    --context=?*) ctx=${1#--context=}; shift ;;
    --secret) shift ;;  # the only mode's old flag
    --all) all=1; shift ;;
    --list) list=1; shift ;;
    --k8s-addr) [ $# -ge 2 ] || usage; k8s_addr=$2; shift 2 ;;
    --k8s-cacert) [ $# -ge 2 ] || usage; k8s_ca=$2; shift 2 ;;
    --kv|--vault|--vault=*|--hcp-addr|--hcp-addr=*|--hcp-cacert|--hcp-cacert=*) gone "${1%%=*}" ;;
    -h|--help) awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; exit 0 ;;
    -*) usage ;;
    *) services+=("$1"); shift ;;
  esac
done

# Everything that needs no cluster and no Vault is checked first.
if [ -n "$list" ]; then
  [ -z "$all" ] && [ ${#services[@]} -eq 0 ] || usage
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
if [ -n "$all" ]; then
  [ ${#services[@]} -eq 0 ] || usage
  while IFS= read -r svc; do services+=("$svc"); done < <(py services "$OVERLAYS")
fi
[ ${#services[@]} -gt 0 ] || usage
for svc in "${services[@]}"; do
  matches "$svc" '^[a-z0-9]([a-z0-9-]*[a-z0-9])?$' || die "\"$svc\" is not a service name"
  [ -f "$OVERLAYS/$svc/vault.yaml" ] || die "$svc: no generated VSO objects in $OVERLAYS/$svc, so no synced Secret (scripts/vault_diff.sh --list)"
done
host() { local h=${1#https://}; printf '%s' "${h%%/*}"; }
k8s_addr=${k8s_addr%/}
matches "$k8s_addr" '^https://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$' \
  || die "--k8s-addr must be your port-forward: https://127.0.0.1, localhost or [::1], with a port"
[ -n "$k8s_ca" ] || die "no CA for the in-cluster Vault: --k8s-cacert or VAULT_CACERT"
[ -f "$k8s_ca" ] || die "--k8s-cacert $k8s_ca: no such file"

# The cluster: named, listed, never the current context (scripts/lib/kube.sh, D150).
# shellcheck source=lib/kube.sh
. "$HERE/lib/kube.sh"
kube_context "$ctx" || exit 2
echo "-> in-cluster: $(host "$k8s_addr")" >&2

# The token: asked for on the terminal without echo, read straight into the variable (no
# subshell). The environment still works, and says so.
token_ok() { # <token> <name>: refuse what curl's config syntax or a header could not carry, without echoing it
  case $1 in
    "") die "empty $2" ;;
    *[!A-Za-z0-9._+/=-]*) die "$2 has characters a Vault token does not have" ;;
  esac
}
no_tty() { die "no terminal to ask for $1"; }
if [ -n "$ce_env" ]; then
  echo "CE_VAULT_TOKEN: taken from the environment (the prompt is safer: scripts/vault_diff.sh --help)" >&2
else
  [ -r /dev/tty ] || no_tty CE_VAULT_TOKEN
  prompted=1
  IFS= read -rs -p "CE_VAULT_TOKEN: " ce_tok </dev/tty || no_tty CE_VAULT_TOKEN
  echo >&2
fi
token_ok "$ce_tok" CE_VAULT_TOKEN

# The salt: 32 random bytes, in this shell only (each hasher reads it from its stdin).
salt=$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')
matches "$salt" '^[0-9a-f]{64}$' || die "no random salt"

# curl's config on its stdin: printf is a builtin, so the token is in no argv and no file.
token_config() { printf 'header = "X-Vault-Token: %s"\n' "$ce_tok"; }
# vault_hashes <mount> <path>: the fingerprints of one kv-v2 secret in the in-cluster Vault.
vault_hashes() {
  {
    printf '%s\n' "$salt"
    # -q first: no ~/.curlrc (a `verbose` there would print the token header). No proxy from the
    # environment (it would see the request, and route even loopback). No -L: a token is never
    # sent to where a redirect points. A failed call leaves status 000 for the hasher.
    token_config | curl -q --config - --silent --show-error --proto =https --max-redirs 0 --noproxy '*' \
      --connect-timeout 10 --max-time 30 --cacert "$k8s_ca" --header 'Accept: application/json' \
      --write-out '\n%{http_code}' -- "$k8s_addr/v1/$1/data/$2" || true
  } | py vault
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
  objs=$(py objects "$OVERLAYS" "$svc") || die "$svc: cannot read its generated VSO objects"
  while IFS=$'\t' read -r ns path dest; do
    [ -n "$path" ] || continue
    s=$(secret_hashes "$ns" "$dest") || die "$ns/$dest: the Secret could not be read"
    a=$(vault_hashes "kv-$svc" "$path") || die "kv-$svc/$path: the in-cluster Vault could not be read"
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
