#!/usr/bin/env bash
# Where scripts/lib/admin.sh (the admin key) and scripts/seed_demo.sh (the demo password) read Vault
# (make test-scripts): by default exactly as before (HCP, a namespace per service), and with RYFT_VAULT=ce
# the in-cluster Vault (kv-api/config, kv-operator/demo, no VAULT_NAMESPACE). A fake `vault` and a fake
# `terraform` are first on PATH; nothing here reaches a Vault, a cluster or Supabase.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
mkdir -p "$t/bin" "$t/s/lib"
fail() { echo "✗ admin_vault_test: $*" >&2; exit 1; }
export FAKE_DIR=$t
unset ADMIN_API_KEY DEMO_PASSWORD RYFT_VAULT ADMIN_VAULT_NAMESPACE DEMO_VAULT_NAMESPACE VAULT_NAMESPACE

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

# admin.sh, the default: the environment, then terraform, then HCP with admin/ryft/api, kv/config.
admin ADMIN_API_KEY=from-env || fail "ADMIN_API_KEY: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-env ] && [ -z "$(vault_calls)" ] && [ ! -s "$t/terraform.calls" ] || fail "ADMIN_API_KEY was not taken as is"
admin FAKE_TF_KEY=from-tf FAKE_VAULT_VALUE=from-vault || fail "terraform: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-tf ] && [ -z "$(vault_calls)" ] || fail "terraform's output was not first: $(vault_calls)"
[ "$(cat "$t/err")" = "admin key: terraform output of infra/vault" ] || fail "terraform: $(cat "$t/err")"
admin FAKE_VAULT_VALUE=from-hcp || fail "HCP: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-hcp ] || fail "HCP: the key"
[ "$(vault_calls)" = "kv get -mount=kv -field=ADMIN_API_KEY config | VAULT_NAMESPACE=admin/ryft/api" ] || fail "HCP call: $(vault_calls)"
[ "$(tail -n 1 "$t/err")" = "admin key: Vault (admin/ryft/api, kv/config)" ] || fail "HCP: $(cat "$t/err")"
admin FAKE_VAULT_VALUE=from-hcp RYFT_VAULT=hcp || fail "RYFT_VAULT=hcp: $(cat "$t/err")"
[ "$(vault_calls)" = "kv get -mount=kv -field=ADMIN_API_KEY config | VAULT_NAMESPACE=admin/ryft/api" ] || fail "RYFT_VAULT=hcp is not the default: $(vault_calls)"
admin FAKE_VAULT_VALUE=from-hcp ADMIN_VAULT_NAMESPACE=admin/other/api || fail "ADMIN_VAULT_NAMESPACE: $(cat "$t/err")"
[ "$(vault_calls)" = "kv get -mount=kv -field=ADMIN_API_KEY config | VAULT_NAMESPACE=admin/other/api" ] || fail "ADMIN_VAULT_NAMESPACE: $(vault_calls)"
! admin || fail "no key anywhere still succeeded"
grep -Fq 'VAULT_NAMESPACE=admin/ryft/api vault kv get -mount=kv -field=ADMIN_API_KEY config' "$t/err" || fail "the HCP hint: $(cat "$t/err")"
grep -Fq 'RYFT_VAULT=ce: vault kv get -mount=kv-api -field=ADMIN_API_KEY config' "$t/err" || fail "the CE hint: $(cat "$t/err")"

# admin.sh, RYFT_VAULT=ce: kv-api/config without a namespace, even when the caller has one set (and
# the caller's own VAULT_NAMESPACE is left as it was). terraform's output still comes first.
admin FAKE_VAULT_VALUE=from-ce RYFT_VAULT=ce VAULT_NAMESPACE=admin/ryft || fail "CE: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-ce ] || fail "CE: the key"
[ "$(vault_calls)" = "kv get -mount=kv-api -field=ADMIN_API_KEY config | VAULT_NAMESPACE=<unset>" ] || fail "CE call: $(vault_calls)"
[ "$(cat "$t/key.ns")" = admin/ryft ] || fail "CE: the caller's VAULT_NAMESPACE changed: $(cat "$t/key.ns")"
[ "$(tail -n 1 "$t/err")" = "admin key: the in-cluster Vault (kv-api/config)" ] || fail "CE: $(cat "$t/err")"
admin FAKE_TF_KEY=from-tf FAKE_VAULT_VALUE=from-ce RYFT_VAULT=ce || fail "CE with terraform: $(cat "$t/err")"
[ "$(cat "$t/key")" = from-tf ] && [ -z "$(vault_calls)" ] || fail "CE: terraform's output was not first"
! admin RYFT_VAULT=ce || fail "CE without a key still succeeded"
[ "$(vault_calls)" = "kv get -mount=kv-api -field=ADMIN_API_KEY config | VAULT_NAMESPACE=<unset>" ] || fail "CE, nothing: $(vault_calls)"
! admin FAKE_VAULT_VALUE=x RYFT_VAULT=enterprise || fail "an unknown RYFT_VAULT was accepted"
grep -Fq 'RYFT_VAULT is hcp (default) or ce' "$t/err" && [ -z "$(vault_calls)" ] && [ ! -s "$t/terraform.calls" ] || fail "an unknown RYFT_VAULT: $(cat "$t/err") $(vault_calls)"

# CE goes only to your port-forward, verified: VAULT_ADDR unset or https loopback, no VAULT_SKIP_VERIFY.
# The default (HCP) does not look at either.
for addr in https://127.0.0.1:8200 https://localhost:8200 'https://[::1]:8200' https://127.0.0.1:8200/; do
  admin FAKE_VAULT_VALUE=from-ce RYFT_VAULT=ce VAULT_ADDR="$addr" || fail "CE with VAULT_ADDR=$addr: $(cat "$t/err")"
done
for addr in https://vault.example.com:8200 http://127.0.0.1:8200 https://127.0.0.1:8200@evil.example https://127.0.0.1.evil.example:8200 \
  https://10.0.0.1:8200; do
  ! admin FAKE_TF_KEY=from-tf FAKE_VAULT_VALUE=from-ce RYFT_VAULT=ce VAULT_ADDR="$addr" || fail "CE took VAULT_ADDR=$addr"
  grep -Fq 'VAULT_ADDR must be your port-forward' "$t/err" && [ -z "$(vault_calls)" ] && [ ! -s "$t/terraform.calls" ] \
    || fail "CE, VAULT_ADDR=$addr: $(cat "$t/err") $(vault_calls)"
done
! admin FAKE_VAULT_VALUE=from-ce RYFT_VAULT=ce VAULT_SKIP_VERIFY=true || fail "CE took VAULT_SKIP_VERIFY"
grep -Fq 'unset VAULT_SKIP_VERIFY' "$t/err" && [ -z "$(vault_calls)" ] || fail "CE, VAULT_SKIP_VERIFY: $(cat "$t/err")"
admin FAKE_VAULT_VALUE=from-hcp VAULT_ADDR=https://vault.example.com:8200 VAULT_SKIP_VERIFY=true || fail "HCP looked at VAULT_ADDR: $(cat "$t/err")"
[ "$(vault_calls)" = "kv get -mount=kv -field=ADMIN_API_KEY config | VAULT_NAMESPACE=admin/ryft/api" ] || fail "HCP with VAULT_ADDR: $(vault_calls)"

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
seed FAKE_VAULT_VALUE=pw-hcp || fail "seed, HCP: $(cat "$t/err")"
[ "$(cat "$t/seeded")" = pw-hcp ] || fail "seed, HCP: the password"
[ "$(vault_calls)" = "kv get -mount=operator -field=password demo | VAULT_NAMESPACE=admin/ryft" ] || fail "seed, HCP call: $(vault_calls)"
seed FAKE_VAULT_VALUE=pw-hcp RYFT_VAULT=hcp DEMO_VAULT_NAMESPACE=admin/x || fail "seed, DEMO_VAULT_NAMESPACE: $(cat "$t/err")"
[ "$(vault_calls)" = "kv get -mount=operator -field=password demo | VAULT_NAMESPACE=admin/x" ] || fail "seed, DEMO_VAULT_NAMESPACE: $(vault_calls)"
seed FAKE_VAULT_VALUE=pw-ce RYFT_VAULT=ce VAULT_NAMESPACE=admin/ryft || fail "seed, CE: $(cat "$t/err")"
[ "$(cat "$t/seeded")" = pw-ce ] || fail "seed, CE: the password"
[ "$(vault_calls)" = "kv get -mount=kv-operator -field=password demo | VAULT_NAMESPACE=<unset>" ] || fail "seed, CE call: $(vault_calls)"
seed DEMO_PASSWORD=given RYFT_VAULT=ce || fail "seed, DEMO_PASSWORD: $(cat "$t/err")"
[ "$(cat "$t/seeded")" = given ] && [ -z "$(vault_calls)" ] || fail "seed: DEMO_PASSWORD still asked Vault"
! seed || fail "seed: HCP without a password still seeded"
{ [ ! -e "$t/seeded" ] && grep -Fq 'Vault did not give it (vault login' "$t/err"; } || fail "seed, HCP failure: $(cat "$t/err")"
! seed RYFT_VAULT=ce || fail "seed: CE without a password still seeded"
{ [ ! -e "$t/seeded" ] && grep -Fq 'the in-cluster Vault did not give it' "$t/err"; } || fail "seed, CE failure: $(cat "$t/err")"
! seed FAKE_VAULT_VALUE=x RYFT_VAULT=Ce || fail "seed: an unknown RYFT_VAULT was accepted"
[ ! -e "$t/seeded" ] && [ -z "$(vault_calls)" ] || fail "seed: an unknown RYFT_VAULT still read Vault"
seed FAKE_VAULT_VALUE=pw-ce RYFT_VAULT=ce VAULT_ADDR=https://127.0.0.1:8200 || fail "seed, CE loopback: $(cat "$t/err")"
! seed FAKE_VAULT_VALUE=pw-ce RYFT_VAULT=ce VAULT_ADDR=https://127.0.0.1:8200@evil.example || fail "seed: CE took a foreign VAULT_ADDR"
{ [ ! -e "$t/seeded" ] && [ -z "$(vault_calls)" ] && grep -Fq 'VAULT_ADDR must be your port-forward' "$t/err"; } || fail "seed, foreign VAULT_ADDR: $(cat "$t/err")"
! seed FAKE_VAULT_VALUE=pw-ce RYFT_VAULT=ce VAULT_SKIP_VERIFY=1 || fail "seed: CE took VAULT_SKIP_VERIFY"
{ [ ! -e "$t/seeded" ] && [ -z "$(vault_calls)" ]; } || fail "seed, VAULT_SKIP_VERIFY: $(cat "$t/err")"
seed FAKE_VAULT_VALUE=pw-hcp VAULT_ADDR=https://vault.example.com:8200 VAULT_SKIP_VERIFY=1 || fail "seed, HCP looked at VAULT_ADDR: $(cat "$t/err")"
echo "✓ admin_vault_test"
