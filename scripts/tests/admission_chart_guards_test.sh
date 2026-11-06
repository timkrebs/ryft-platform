#!/usr/bin/env bash
# The admission-policy chart's guards (templates/_helpers.tpl, admission.validate): values it must refuse
# to render, and the renders it must produce (the vault bindings, the decommission's P4, break-glass).
# helm template only, offline; no cluster. Skips without helm (the `admission` job installs the pinned one).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
chart=$root/infra/aws/kubernetes/charts/admission-policy
values=$root/infra/aws/kubernetes/admission-values.yaml
fail() { echo "✗ admission_chart_guards_test: $*" >&2; exit 1; }
command -v helm >/dev/null || { echo "admission_chart_guards_test: skipped (no helm)"; exit 0; }
command -v yq >/dev/null || { echo "admission_chart_guards_test: skipped (no yq)"; exit 0; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

render() { helm template admission-policy "$chart" -f "$values" "$@" 2>"$tmp/err"; }
# refused <what the error must say> <helm args...>
refused() {
  local want=$1; shift
  if render "$@" >/dev/null; then fail "rendered, but must be refused: $*"; fi
  grep -qF "$want" "$tmp/err" || fail "refused for another reason ($*): $(head -c 300 "$tmp/err")"
}
renders() { render "$@" >/dev/null || fail "must render: $* ($(head -c 300 "$tmp/err"))"; }

d64=962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2
img='images.vault must be one literal image reference pinned by digest'
cli='images.awsCli must be one literal image reference pinned by digest'

# Images (P2 in vault): one literal reference with a full digest, nothing a regex could widen.
refused "$img" --set "images.vault=^.*@sha256:$d64\$"
refused "$img" --set "images.vault=^docker[.]io/hashicorp/vault@sha256:.*\$"
refused "$img" --set 'images.vault=^…@sha256:00$|^.*$'
refused "$img" --set "images.vault=^docker[.]io/hashicorp/vault:2[.]2[.]1@sha256:$d64\$|^.*\$"
refused "$img" --set "images.vault=^(docker[.]io/hashicorp/vault:2[.]2[.]1)@sha256:$d64\$"
refused "$img" --set "images.vault=^docker.io/hashicorp/vault:2.2.1@sha256:$d64\$"
refused "$img" --set "images.vault=^docker[.]io/hashicorp/vault:2[.]2[.]1@sha256:[0-9a-f]{64}\$"
printf 'images:\n  vault: "^.{1,200}@sha256:%s$"\n' "$d64" > "$tmp/bounded.yaml" # a comma: --set would split it
refused "$img" -f "$tmp/bounded.yaml"
refused "$img" --set "images.vault=docker[.]io/hashicorp/vault:2[.]2[.]1@sha256:$d64"
refused "$img" --set "images.vault=^docker[.]io/hashicorp/vault:2[.]2[.]1\$"
refused "$img" --set "images.vault=^docker[.]io/hashicorp/vault:2[.]2[.]1@sha256:${d64}0\$"
refused 'images.vault is required' --set images.vault=
refused "$cli" --set 'images.awsCli=public.ecr.aws/aws-cli/aws-cli:2.0.0'
refused "$cli" --set "images.awsCli=^public[.]ecr[.]aws/aws-cli/aws-cli:2[.]0[.]0@sha256:[0-9a-f]{64}\$"
renders --set "images.awsCli=^public[.]ecr[.]aws/aws-cli/aws-cli:2[.]31[.]0@sha256:$d64\$"
grep -qF "c.image.matches(\"^public[.]ecr[.]aws/aws-cli/aws-cli:2[.]31[.]0@sha256:$d64\$\")" <(render --set "images.awsCli=^public[.]ecr[.]aws/aws-cli/aws-cli:2[.]31[.]0@sha256:$d64\$") ||
  fail "a pinned awsCli must appear in P2's vault branch"
if render | grep -q 'aws-cli'; then fail "an empty awsCli must not appear in P2 (it would match every image)"; fi

# The other guards of the vault values.
refused 'vaultAddress is now vaultAddresses' --set vaultAddress=https://x
refused 'every vaultAddresses entry must be an https:// URL' --set 'vaultAddresses[0]=http://x'
refused 'every vaultAddresses entry must be an https:// URL' --set 'vaultAddresses[0]=https://x/'
refused 'vault.namespace must be set' --set vault.namespace=ryft-vault
refused 'vault.namespace must be set' --set vault.namespace=argocd
refused 'vault.namespace must be set' --set vault.namespace=rabbitmq-system
refused 'vault.caUsages must not hold server auth' --set 'vault.caUsages[0]=server auth'
refused 'vso.kvMountPrefix is required' --set vso.kvMountPrefix=
refused 'vso.reservedServices is required' --set 'vso.reservedServices=null'
refused 'are required (P13)' --set vault.rootCASecret=
refused 'are required (P13)' --set vault.bootstrapIssuer=
refused 'netpol.podIdentityAgentCIDR is required' --set netpol.podIdentityAgentCIDR=
refused 'actions.vault-trust must be audit or deny' --set actions.vault-trust=block
refused 'actions.netpol-vault must be audit or deny' --set actions.netpol-vault=block
refused 'actions.netpol is deny while netpol.brokerEgressPublic is true' --set actions.netpol=deny

# The vault bindings: four of them, In [vault]; the general ones of the same policies NotIn [vault];
# each goes to deny alone, also while netpol's guard holds the general P3 binding on audit.
render > "$tmp/r.yaml" || fail "the committed values must render"
n=$(yq 'select(.kind == "ValidatingAdmissionPolicyBinding") | .metadata.name' "$tmp/r.yaml" | grep -vc '^---$')
[ "$n" = 17 ] || fail "the chart renders $n bindings, want 17"
n=$(yq 'select(.kind == "ValidatingAdmissionPolicy") | .metadata.name' "$tmp/r.yaml" | grep -vc '^---$')
[ "$n" = 12 ] || fail "the chart renders $n policies, want 12"
for b in ryft-namespaces-vault ryft-workloads-templates-vault ryft-workloads-pods-vault ryft-netpol-vault; do
  got=$(yq "select(.metadata.name == \"$b\" and .kind == \"ValidatingAdmissionPolicyBinding\") | .spec.matchResources.namespaceSelector.matchExpressions | tojson(0)" "$tmp/r.yaml")
  [ "$got" = '[{"key":"kubernetes.io/metadata.name","operator":"In","values":["vault"]}]' ] || fail "$b selects $got"
  general=${b%-vault}
  got=$(yq "select(.metadata.name == \"$general\" and .kind == \"ValidatingAdmissionPolicyBinding\") | .spec.matchResources.namespaceSelector.matchExpressions | tojson(0)" "$tmp/r.yaml")
  [ "$got" = '[{"key":"kubernetes.io/metadata.name","operator":"NotIn","values":["vault"]}]' ] || fail "$general selects $got"
done
for b in ryft-vso ryft-vault-trust ryft-operators-cluster; do
  got=$(yq "select(.metadata.name == \"$b\" and .kind == \"ValidatingAdmissionPolicyBinding\") | .spec.matchResources.namespaceSelector" "$tmp/r.yaml")
  [ "$got" = null ] || fail "$b must not select namespaces (it judges cluster-scoped objects or every namespace)"
done
render --set actions.netpol-vault=deny --set actions.namespaces-vault=deny --set actions.workloads-pods-vault=deny > "$tmp/d.yaml" ||
  fail "the vault bindings must go to deny while the general ones stay on audit"
[ "$(yq 'select(.metadata.name == "ryft-netpol-vault") | .spec.validationActions | join(",")' "$tmp/d.yaml")" = Deny,Audit ] || fail "netpol-vault deny is not Deny+Audit"
[ "$(yq 'select(.metadata.name == "ryft-netpol" and .kind == "ValidatingAdmissionPolicyBinding") | .spec.validationActions | join(",")' "$tmp/d.yaml")" = Warn,Audit ] || fail "netpol must stay Warn+Audit"
render --set breakGlass=true --set actions.netpol-vault=deny > "$tmp/b.yaml" || fail "break-glass must render"
[ "$(yq 'select(.metadata.name == "ryft-netpol-vault") | .spec.validationActions | join(",")' "$tmp/b.yaml")" = Warn,Audit ] || fail "break-glass must give netpol-vault Warn+Audit"
[ "$(yq 'select(.metadata.name == "ryft-netpol-vault") | .metadata.labels."admission.ryft.dev/intended-action"' "$tmp/b.yaml")" = deny ] || fail "break-glass keeps the intended action label"

# The decommission's P4: no Vault namespace, no mount kv, the reserved mounts never.
render --set vso.kvMount= --set vso.vaultNamespacePrefix= --set 'vaultAddresses={https://vault.vault.svc:8200}' > "$tmp/x.yaml" || fail "the decommission's values must render"
p4=$(yq 'select(.metadata.name == "ryft-vso" and .kind == "ValidatingAdmissionPolicy") | .spec.validations[2,3].expression' "$tmp/x.yaml")
grep -qF '!has(variables.spec.namespace) &&' <<<"$p4" || fail "after the decommission a VaultAuth must carry no Vault namespace"
grep -qF 'variables.spec.mount in [] ||' <<<"$p4" || fail "after the decommission the mount kv must be gone"
grep -qF '["kv-operator","kv-root","kv-admin","kv-terraform","kv-default","kv-vault-snapshot"]' <<<"$p4" || fail "the reserved mounts are not refused"
grep -qF '"https://vault.vault.svc:8200"]' <(yq 'select(.metadata.name == "ryft-vso" and .kind == "ValidatingAdmissionPolicy") | .spec.validations[1].expression' "$tmp/x.yaml") || fail "the decommission keeps the in-cluster address"
echo "✓ admission_chart_guards_test"
