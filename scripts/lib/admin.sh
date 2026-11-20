# shellcheck shell=bash
# Sourced by the operator scripts (beta_report.sh, user.sh): the api's URL and the
# admin key (docs/OPERATIONS.md → Beta). Works with macOS's bash 3.2.
#
#   API_URL       default https://api.ryftlab.de (compose: http://localhost:8082)
#   ADMIN_API_KEY from the environment, and nowhere else. Production's is the Secret Manager secret
#                 admin-api-key at the version deploy/run/api.yaml pins (docs/OPERATIONS.md → GCP);
#                 the message below shows how to read it without putting it on screen. Its two
#                 EKS-era sources (the Vault root's Terraform output, the in-cluster Vault) went
#                 with D3 (D246).
API_URL=${API_URL:-https://api.ryftlab.de}

# ryft_admin_key: ADMIN_API_KEY is set (and exported), or it says how to set it and fails.
ryft_admin_key() {
  if [ -n "${ADMIN_API_KEY:-}" ]; then
    export ADMIN_API_KEY
    echo "admin key: ADMIN_API_KEY from the environment" >&2
    return 0
  fi
  cat >&2 <<'MSG'
✗ no admin key: export ADMIN_API_KEY first.
  production (from the repository root; gcloud signed in with access to ryftlab-prod):
    V=$(sed -n 's/.*name: admin-api-key, key: "\([0-9]*\)".*/\1/p' deploy/run/api.yaml)
    ADMIN_API_KEY=$(gcloud secrets versions access "$V" --secret=admin-api-key --project=ryftlab-prod) && export ADMIN_API_KEY
  compose: export ADMIN_API_KEY=dev-admin-key-change-me-0123456789abcdef
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
