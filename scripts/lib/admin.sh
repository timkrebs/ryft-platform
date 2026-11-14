# shellcheck shell=bash
# Sourced by the operator scripts (beta_report.sh, trace.sh, user.sh): the api's URL
# and the admin key, found without typing it (docs/OPERATIONS.md → Beta). Works with
# macOS's bash 3.2.
#
#   API_URL       default https://api.ryftapp.de (compose: http://localhost:8082)
#   ADMIN_API_KEY else `terraform -chdir=infra/vault output -raw admin_api_key`,
#                 else the in-cluster Vault (the only Vault, D207; Community Edition: no namespaces)
#                 through your own port-forward (VAULT_ADDR=https://127.0.0.1:8200, VAULT_CACERT, a
#                 token): vault kv get -mount=kv-api -field=ADMIN_API_KEY config, with VAULT_NAMESPACE
#                 removed for that call only. Refused before anything is asked unless VAULT_ADDR is
#                 unset or https loopback and VAULT_SKIP_VERIFY is unset.
#   RYFT_VAULT    gone with HCP Vault: unset, or `ce` (the old name of the in-cluster Vault; changes
#                 nothing). Any other value is refused.
API_URL=${API_URL:-https://api.ryftapp.de}
RYFT_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

# ryft_vault_ok: the vault CLI talks to your port-forward, verified: VAULT_ADDR unset (the CLI's
# default is https://127.0.0.1:8200) or https on 127.0.0.1, localhost or [::1], and no
# VAULT_SKIP_VERIFY. Anything else would hand the token to another host (an old HCP address left in
# the shell, say) or an unverified one.
ryft_vault_ok() {
  local loopback='^https://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?/?$'
  if [ -n "${VAULT_ADDR:-}" ] && ! [[ $VAULT_ADDR =~ $loopback ]]; then
    echo "✗ the in-cluster Vault: VAULT_ADDR must be your port-forward (https://127.0.0.1:8200), not $VAULT_ADDR" >&2
    return 1
  fi
  if [ -n "${VAULT_SKIP_VERIFY:-}" ]; then
    echo "✗ the in-cluster Vault: unset VAULT_SKIP_VERIFY (set VAULT_CACERT to the in-cluster Vault's CA instead)" >&2
    return 1
  fi
}

# ryft_admin_key exports ADMIN_API_KEY and says on stderr where it came from.
ryft_admin_key() {
  if [ -n "${ADMIN_API_KEY:-}" ]; then
    echo "admin key: ADMIN_API_KEY from the environment" >&2
    return 0
  fi
  local key=""
  case ${RYFT_VAULT:-ce} in
    ce) ;;
    *) echo "✗ RYFT_VAULT=\"$RYFT_VAULT\": HCP Vault is gone (D207) and the in-cluster Vault is the only one; unset RYFT_VAULT" >&2; return 1 ;;
  esac
  ryft_vault_ok || return 1
  if command -v terraform >/dev/null 2>&1; then
    key=$(terraform -chdir="$RYFT_ROOT/infra/vault" output -raw admin_api_key 2>/dev/null || true)
    case "$key" in *"No outputs"*|*"Warning"*|*"Error"*) key="" ;; esac
    if [ -n "$key" ]; then
      export ADMIN_API_KEY=$key
      echo "admin key: terraform output of infra/vault" >&2
      return 0
    fi
  fi
  if command -v vault >/dev/null 2>&1; then
    # One mount per service, no namespace (a VAULT_NAMESPACE left in the shell would make the CLI
    # ask for a namespace that Community Edition does not have).
    key=$( (unset VAULT_NAMESPACE; vault kv get -mount=kv-api -field=ADMIN_API_KEY config) 2>/dev/null || true)
    if [ -n "$key" ]; then
      export ADMIN_API_KEY=$key
      echo "admin key: the in-cluster Vault (kv-api/config)" >&2
      return 0
    fi
  fi
  cat >&2 <<'MSG'
✗ no admin key. One of these has to work:
  export ADMIN_API_KEY=…                                         (compose: dev-admin-key-change-me-0123456789abcdef)
  terraform -chdir=infra/vault output -raw admin_api_key         (needs `terraform login` and `terraform init` there)
  vault kv get -mount=kv-api -field=ADMIN_API_KEY config         (the in-cluster Vault: your port-forward
      kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200,
      VAULT_ADDR=https://127.0.0.1:8200 or unset, VAULT_CACERT, VAULT_TOKEN)
MSG
  return 1
}

# ryft_admin METHOD PATH [JSON] prints the answer's body; on a non-2xx answer it
# prints the status and body to stderr and fails.
ryft_admin() {
  local method=$1 path=$2 data=${3:-} out status
  out=$(mktemp)
  status=$(curl -sS -o "$out" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $ADMIN_API_KEY" \
    -H 'Content-Type: application/json' ${data:+-d "$data"} "$API_URL$path") || { rm -f "$out"; echo "✗ $API_URL unreachable" >&2; return 1; }
  case "$status" in
    2??) cat "$out"; rm -f "$out"; return 0 ;;
  esac
  echo "✗ $method $path: $status $(cat "$out")" >&2
  rm -f "$out"
  return 1
}
