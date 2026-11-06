# shellcheck shell=bash
# Sourced by the operator scripts (beta_report.sh, trace.sh, user.sh): the api's URL
# and the admin key, found without typing it (docs/OPERATIONS.md → Beta). Works with
# macOS's bash 3.2.
#
#   API_URL       default https://api.ryftapp.de (compose: http://localhost:8082)
#   ADMIN_API_KEY else `terraform -chdir=infra/vault output -raw admin_api_key`,
#                 else Vault: VAULT_NAMESPACE=admin/ryft/api vault kv get -mount=kv -field=ADMIN_API_KEY config
#   RYFT_VAULT    which Vault that last step reads: hcp (the default, as above) or ce, the in-cluster
#                 Vault (Community Edition: no namespaces) through your own port-forward
#                 (VAULT_ADDR=https://127.0.0.1:8200, VAULT_CACERT, a token): vault kv get -mount=kv-api
#                 -field=ADMIN_API_KEY config, with VAULT_NAMESPACE removed for that call only
API_URL=${API_URL:-https://api.ryftapp.de}
RYFT_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

# ryft_admin_key exports ADMIN_API_KEY and says on stderr where it came from.
ryft_admin_key() {
  if [ -n "${ADMIN_API_KEY:-}" ]; then
    echo "admin key: ADMIN_API_KEY from the environment" >&2
    return 0
  fi
  local key=""
  case ${RYFT_VAULT:-hcp} in
    hcp|ce) ;;
    *) echo "✗ RYFT_VAULT is hcp (default) or ce, not \"$RYFT_VAULT\"" >&2; return 1 ;;
  esac
  if command -v terraform >/dev/null 2>&1; then
    key=$(terraform -chdir="$RYFT_ROOT/infra/vault" output -raw admin_api_key 2>/dev/null || true)
    case "$key" in *"No outputs"*|*"Warning"*|*"Error"*) key="" ;; esac
    if [ -n "$key" ]; then
      export ADMIN_API_KEY=$key
      echo "admin key: terraform output of infra/vault" >&2
      return 0
    fi
  fi
  if [ "${RYFT_VAULT:-hcp}" = ce ] && command -v vault >/dev/null 2>&1; then
    # In-cluster Vault: one mount per service, no namespace (a VAULT_NAMESPACE meant for HCP would
    # make the CLI ask for a namespace that does not exist).
    key=$( (unset VAULT_NAMESPACE; vault kv get -mount=kv-api -field=ADMIN_API_KEY config) 2>/dev/null || true)
    if [ -n "$key" ]; then
      export ADMIN_API_KEY=$key
      echo "admin key: the in-cluster Vault (kv-api/config)" >&2
      return 0
    fi
  elif command -v vault >/dev/null 2>&1; then
    # On HCP the namespace carries the admin/ parent prefix (D45).
    key=$(VAULT_NAMESPACE=${ADMIN_VAULT_NAMESPACE:-admin/ryft/api} vault kv get -mount=kv -field=ADMIN_API_KEY config 2>/dev/null || true)
    if [ -n "$key" ]; then
      export ADMIN_API_KEY=$key
      echo "admin key: Vault (${ADMIN_VAULT_NAMESPACE:-admin/ryft/api}, kv/config)" >&2
      return 0
    fi
  fi
  cat >&2 <<'MSG'
✗ no admin key. One of these has to work:
  export ADMIN_API_KEY=…                                         (compose: dev-admin-key-change-me-0123456789abcdef)
  terraform -chdir=infra/vault output -raw admin_api_key         (needs `terraform login` and `terraform init` there)
  VAULT_NAMESPACE=admin/ryft/api vault kv get -mount=kv -field=ADMIN_API_KEY config   (needs VAULT_ADDR and `vault login`)
  RYFT_VAULT=ce: vault kv get -mount=kv-api -field=ADMIN_API_KEY config   (the in-cluster Vault: VAULT_ADDR=https://127.0.0.1:8200 through your port-forward, VAULT_CACERT, VAULT_TOKEN)
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
