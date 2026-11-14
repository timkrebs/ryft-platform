#!/usr/bin/env bash
# Where scripts/lib/admin.sh (the admin key) and scripts/seed_demo.sh (the demo password) read Vault
# (make test-scripts): the in-cluster Vault is the only one (D207): kv-api/config and kv-operator/demo,
# without VAULT_NAMESPACE, only through a verified port-forward; RYFT_VAULT=hcp (or anything but ce) is
# refused before anything is asked. A fake `vault` and a fake `terraform` are first on PATH; nothing here
# reaches a Vault, a cluster or Supabase.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin" "$t/s/lib"
fail() { echo "✗ admin_vault_test: $*" >&2; exit 1; }
export FAKE_DIR=$t
unset ADMIN_API_KEY DEMO_PASSWORD RYFT_VAULT ADMIN_VAULT_NAMESPACE DEMO_VAULT_NAMESPACE VAULT_NAMESPACE VAULT_ADDR VAULT_SKIP_VERIFY

# vault: records its argv and VAULT_NAMESPACE (or <unset>); prints FAKE_VAULT_VALUE, or fails without it.
cat > "$t/bin/vault" <<'V'
#!/bin/sh
printf '%s | VAULT_NAMESPACE=%s\n' "$*" "${VAULT_NAMESPACE-<unset>}" >> "$FAKE_DIR/vault.calls"
[ -n "${FAKE_VAULT_VALUE:-}" ] || { echo "Error making API request" >&2; exit 2; }
printf '%s' "$FAKE_VAULT_VALUE"
V
# terraform: `output -raw admin_api_key` gives FAKE_TF_KEY, or "No outputs" (state not readable).
cat > "$t/bin/terraform" <<'T'
#!/bin/sh
echo "$*" >> "$FAKE_DIR/terraform.calls"
if [ -n "${FAKE_TF_KEY:-}" ]; then printf '%s' "$FAKE_TF_KEY"; else echo "Warning: No outputs found"; fi
T
chmod +x "$t/bin/"*
# PATH: the fakes first (they shadow any real vault or terraform), then the system directories.
for tool in python3 dirname cat; do ln -s "$(command -v "$tool")" "$t/bin/$tool"; done
FAKE_PATH=$t/bin:/usr/bin:/bin

reset() { rm -f "$t/vault.calls" "$t/terraform.calls"; touch "$t/vault.calls" "$t/terraform.calls"; }
# admin <env...>: ryft_admin_key in a fresh bash with these variables; its key in $t/key, stderr in $t/err.
admin() {
  reset
  local rc=0
  # shellcheck disable=SC2016 # expanded by the inner bash
  env -i HOME="$t" PATH="$FAKE_PATH" FAKE_DIR="$t" "$@" bash -c '. "$1"; ryft_admin_key || exit $?; printf "%s" "$ADMIN_API_KEY" > "$2"; printf "%s" "${VAULT_NAMESPACE-<unset>}" > "$2.ns"' \
    sh "$ROOT/scripts/lib/admin.sh" "$t/key" > /dev/null 2> "$t/err" || rc=$?
  return "$rc"
}
vault_calls() { cat "$t/vault.calls"; }
CE_CALL="kv get -mount=kv-api -field=ADMIN_API_KEY config | VAULT_NAMESPACE=<unset>"

# admin.sh: the environment, then terraform, then the in-cluster Vault (kv-api/config, no namespace).
admin ADMIN_API_KEY=from-env || fail "ADMIN_API_KEY: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-env ] && [ -z "$(vault_calls)" ] && [ ! -s "$t/terraform.calls" ] || fail "ADMIN_API_KEY was not taken as is"
admin ADMIN_API_KEY=from-env RYFT_VAULT=hcp VAULT_ADDR=https://vault.example.com:8200 || fail "ADMIN_API_KEY with a stale shell: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-env ] || fail "ADMIN_API_KEY from the environment needs no Vault"
admin FAKE_TF_KEY=from-tf FAKE_VAULT_VALUE=from-vault || fail "terraform: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-tf ] && [ -z "$(vault_calls)" ] || fail "terraform's output was not first: $(vault_calls)"
[ "$(cat "$t/err")" = "admin key: terraform output of infra/vault" ] || fail "terraform: $(cat "$t/err")"
# The default: the in-cluster Vault, even when the caller has a VAULT_NAMESPACE set (and that
# variable is left as it was for the caller).
admin FAKE_VAULT_VALUE=from-ce || fail "the default: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-ce ] || fail "the default: the key"
[ "$(vault_calls)" = "$CE_CALL" ] || fail "the default call: $(vault_calls)"
[ "$(tail -n 1 "$t/err")" = "admin key: the in-cluster Vault (kv-api/config)" ] || fail "the default: $(cat "$t/err")"
admin FAKE_VAULT_VALUE=from-ce VAULT_NAMESPACE=admin/ryft || fail "a stale VAULT_NAMESPACE: $(cat "$t/err")"
[ "$(vault_calls)" = "$CE_CALL" ] || fail "a stale VAULT_NAMESPACE reached the call: $(vault_calls)"
[ "$(cat "$t/key.ns")" = admin/ryft ] || fail "the caller's VAULT_NAMESPACE changed: $(cat "$t/key.ns")"
admin FAKE_VAULT_VALUE=from-ce RYFT_VAULT=ce || fail "RYFT_VAULT=ce (the old name): $(cat "$t/err")"
[ "$(vault_calls)" = "$CE_CALL" ] || fail "RYFT_VAULT=ce: $(vault_calls)"
# No namespace-per-service read is left (HCP's kv/config in admin/ryft/api).
! grep -Fq -e '-mount=kv ' -e 'admin/ryft' "$t/vault.calls" || fail "an HCP-shaped read: $(vault_calls)"
# Nothing anywhere: fails, and the hint names the in-cluster Vault only.
! admin || fail "no key anywhere still succeeded"
[ "$(vault_calls)" = "$CE_CALL" ] || fail "nothing: $(vault_calls)"
grep -Fq 'vault kv get -mount=kv-api -field=ADMIN_API_KEY config' "$t/err" || fail "the hint: $(cat "$t/err")"
grep -Fq 'vault_forward "$(vault_active)" 8200 active' "$t/err" || fail "the hint has no verified port-forward: $(cat "$t/err")"
! grep -Fq 'svc/vault-active' "$t/err" || fail "the hint sends a token through svc/vault-active: $(cat "$t/err")"
! grep -Fq -e 'VAULT_NAMESPACE=admin' -e 'RYFT_VAULT' "$t/err" || fail "the hint still names HCP: $(cat "$t/err")"

# RYFT_VAULT=hcp (a shell from before the rebuild) and any other value: refused before terraform or Vault.
for v in hcp HCP enterprise Ce; do
  ! admin FAKE_TF_KEY=from-tf FAKE_VAULT_VALUE=x RYFT_VAULT="$v" || fail "RYFT_VAULT=$v was accepted"
  grep -Fq 'HCP Vault is gone (D207)' "$t/err" && [ -z "$(vault_calls)" ] && [ ! -s "$t/terraform.calls" ] \
    || fail "RYFT_VAULT=$v: $(cat "$t/err") $(vault_calls)"
done

# Only your port-forward, verified: VAULT_ADDR unset or https loopback, no VAULT_SKIP_VERIFY; anything
# else (an old HCP address left in the shell) is refused before terraform or Vault is asked.
for addr in https://127.0.0.1:8200 https://localhost:8200 'https://[::1]:8200' https://127.0.0.1:8200/; do
  admin FAKE_VAULT_VALUE=from-ce VAULT_ADDR="$addr" || fail "VAULT_ADDR=$addr: $(cat "$t/err")"
done
for addr in https://vault.example.com:8200 https://ryft-vault-public-vault-0000.z1.hashicorp.cloud:8200 http://127.0.0.1:8200 \
  https://127.0.0.1:8200@evil.example https://127.0.0.1.evil.example:8200 https://10.0.0.1:8200 https://vault.vault.svc:8200; do
  ! admin FAKE_TF_KEY=from-tf FAKE_VAULT_VALUE=from-ce VAULT_ADDR="$addr" || fail "took VAULT_ADDR=$addr"
  grep -Fq 'VAULT_ADDR must be your port-forward' "$t/err" && [ -z "$(vault_calls)" ] && [ ! -s "$t/terraform.calls" ] \
    || fail "VAULT_ADDR=$addr: $(cat "$t/err") $(vault_calls)"
done
! admin FAKE_VAULT_VALUE=from-ce VAULT_SKIP_VERIFY=true || fail "took VAULT_SKIP_VERIFY"
grep -Fq 'unset VAULT_SKIP_VERIFY' "$t/err" && [ -z "$(vault_calls)" ] || fail "VAULT_SKIP_VERIFY: $(cat "$t/err")"

# seed_demo.sh, from a copy whose seed_demo.py only records whether it got the password.
cp "$ROOT/scripts/seed_demo.sh" "$t/s/seed_demo.sh"
cat > "$t/s/seed_demo.py" <<'P'
import os
open(os.path.join(os.environ["FAKE_DIR"], "seeded"), "w").write(os.environ.get("DEMO_PASSWORD", "<none>"))
P
seed() { # <env...>
  reset; rm -f "$t/seeded"
  local rc=0
  env -i HOME="$t" PATH="$FAKE_PATH" FAKE_DIR="$t" SUPABASE_URL=https://example.invalid SUPABASE_ANON_KEY=a SUPABASE_SECRET_KEY=s "$@" \
    bash "$t/s/seed_demo.sh" > /dev/null 2> "$t/err" || rc=$?
  return "$rc"
}
SEED_CALL="kv get -mount=kv-operator -field=password demo | VAULT_NAMESPACE=<unset>"
seed FAKE_VAULT_VALUE=pw-ce || fail "seed, the default: $(cat "$t/err")"
[ "$(cat "$t/seeded")" = pw-ce ] || fail "seed: the password"
[ "$(vault_calls)" = "$SEED_CALL" ] || fail "seed call: $(vault_calls)"
seed FAKE_VAULT_VALUE=pw-ce VAULT_NAMESPACE=admin/ryft RYFT_VAULT=ce || fail "seed, a stale VAULT_NAMESPACE: $(cat "$t/err")"
[ "$(vault_calls)" = "$SEED_CALL" ] || fail "seed, a stale VAULT_NAMESPACE reached the call: $(vault_calls)"
seed DEMO_PASSWORD=given || fail "seed, DEMO_PASSWORD: $(cat "$t/err")"
[ "$(cat "$t/seeded")" = given ] && [ -z "$(vault_calls)" ] || fail "seed: DEMO_PASSWORD still asked Vault"
seed DEMO_PASSWORD=given RYFT_VAULT=hcp VAULT_ADDR=https://vault.example.com:8200 || fail "seed, DEMO_PASSWORD with a stale shell: $(cat "$t/err")"
[ "$(cat "$t/seeded")" = given ] || fail "seed: DEMO_PASSWORD needs no Vault"
! seed || fail "seed: no password still seeded"
{ [ ! -e "$t/seeded" ] && grep -Fq 'the in-cluster Vault did not give it' "$t/err"; } || fail "seed, failure: $(cat "$t/err")"
for v in hcp enterprise Ce; do
  ! seed FAKE_VAULT_VALUE=x RYFT_VAULT="$v" || fail "seed: RYFT_VAULT=$v was accepted"
  { [ ! -e "$t/seeded" ] && [ -z "$(vault_calls)" ] && grep -Fq 'HCP Vault is gone (D207)' "$t/err"; } || fail "seed, RYFT_VAULT=$v: $(cat "$t/err")"
done
seed FAKE_VAULT_VALUE=pw-ce VAULT_ADDR=https://127.0.0.1:8200 || fail "seed, loopback: $(cat "$t/err")"
for addr in https://127.0.0.1:8200@evil.example https://ryft-vault-public-vault-0000.z1.hashicorp.cloud:8200 https://vault.vault.svc:8200; do
  ! seed FAKE_VAULT_VALUE=pw-ce VAULT_ADDR="$addr" || fail "seed: took VAULT_ADDR=$addr"
  { [ ! -e "$t/seeded" ] && [ -z "$(vault_calls)" ] && grep -Fq 'VAULT_ADDR must be your port-forward' "$t/err"; } || fail "seed, VAULT_ADDR=$addr: $(cat "$t/err")"
done
! seed FAKE_VAULT_VALUE=pw-ce VAULT_SKIP_VERIFY=1 || fail "seed: took VAULT_SKIP_VERIFY"
{ [ ! -e "$t/seeded" ] && [ -z "$(vault_calls)" ] && grep -Fq 'unset VAULT_SKIP_VERIFY' "$t/err"; } || fail "seed, VAULT_SKIP_VERIFY: $(cat "$t/err")"
echo "✓ admin_vault_test"
