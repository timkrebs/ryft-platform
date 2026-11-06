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
if render --set images.awsCli= | grep -q 'aws-cli'; then fail "an empty awsCli must not appear in P2 (it would match every image)"; fi
# The default is the snapshot job's pin (vault.tf vault_snapshot_aws_cli_image), and only it.
grep -qF 'c.image.matches("^public[.]ecr[.]aws/aws-cli/aws-cli:2[.]39[.]5@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292$")' <(render) ||
  fail "the default awsCli must be the snapshot job's pinned AWS CLI"
[ "$(render | grep -o 'aws-cli/aws-cli:[^@]*@sha256:[0-9a-f]*' | sort -u | wc -l | tr -d ' ')" = 1 ] || fail "the default render must admit exactly one AWS CLI reference"
# The AWS CLI only under the snapshot job's service account (the one with AWS credentials).
grep -qF '(has(variables.podSpec.serviceAccountName) && variables.podSpec.serviceAccountName == "vault-snapshot" && c.image.matches("^public[.]ecr[.]aws/aws-cli/' <(render) ||
  fail "P2 must admit the AWS CLI only in a pod of the service account vault-snapshot"
refused 'vault.snapshotServiceAccount is required with images.awsCli' --set vault.snapshotServiceAccount=
renders --set vault.snapshotServiceAccount= --set images.awsCli=

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

# P12 (D206): vault, cert-manager (and vault.clusterResourceNamespace), argocd and kube-system are never in
# argoScope.namespaces or namespaceObjects; every Namespace object is P1's (ryft-* or rabbitmq-system) and
# one P12 lets the controller write in.
scope='ryft-api,ryft-ingest,ryft-migrate,ryft-notify,ryft-rabbitmq,ryft-scoring,ryft-smoke,ryft-web,rabbitmq-system'
for ns in vault cert-manager argocd kube-system; do
  refused "argoScope.namespaces must not hold $ns" --set "argoScope.namespaces={$scope,$ns}"
  refused "argoScope.namespaceObjects must not hold $ns" --set "argoScope.namespaceObjects={ryft-web,rabbitmq-system,$ns}"
done
refused "argoScope.namespaces must not hold vault-ns" --set vault.namespace=vault-ns --set "argoScope.namespaces={$scope,vault-ns}"
refused "argoScope.namespaces must not hold cm-resources" --set vault.clusterResourceNamespace=cm-resources --set "argoScope.namespaces={$scope,cm-resources}"
refused 'argoScope.namespaceObjects must hold ryft-* or rabbitmq-system Namespaces only' --set 'argoScope.namespaceObjects={ryft-web,team-x}'
# S3 (security review): argoScope.namespaces has the same shape as namespaceObjects (ryft-* or the operators'
# namespace), and the add-on namespaces are on the never-list too (vault-secrets-operator-system: a
# Deployment as the VSO service account would write Secrets cluster-wide and reach vault unmatched).
for ns in vault-secrets-operator-system monitoring amazon-cloudwatch external-dns default kube-public kube-node-lease; do
  refused "argoScope.namespaces must not hold $ns" --set "argoScope.namespaces={$scope,$ns}"
done
refused 'argoScope.namespaces must hold ryft-* or rabbitmq-system namespaces only' --set "argoScope.namespaces={$scope,team-x}"
refused 'argoScope.namespaces must hold ryft-* or rabbitmq-system namespaces only' --set 'argoScope.neverNamespaces={unused}' --set "argoScope.namespaces={$scope,monitoring}"
refused 'argoScope.neverNamespaces must be a non-empty list of strings' --set argoScope.neverNamespaces=null
refused 'argoScope.neverNamespaces must be a non-empty list of strings' --set 'argoScope.neverNamespaces={}'
# S2 (security review): every list spliced into P12's CEL (and P1's) is a non-empty list of strings. A map
# renders `in {map}`, which CEL answers on its keys (so {vault: x, kube-system: x} would admit writes there),
# and null renders `in null`, a runtime error that refuses every Argo CD write (failurePolicy Fail).
printf 'argoScope:\n  namespaces:\n    vault: x\n    kube-system: x\n' > "$tmp/map-ns.yaml"
refused 'argoScope.namespaces must be a non-empty list of strings' -f "$tmp/map-ns.yaml"
printf 'argoScope:\n  namespaces:\n    - ryft-api\n    - [kube-system]\n' > "$tmp/nested-ns.yaml"
refused 'argoScope.namespaces must be a non-empty list of strings' -f "$tmp/nested-ns.yaml"
for key in argoScope.namespaces argoScope.namespaceObjects argoScope.crdGroups operators.clusterRoleNamePrefixes operators.webhookConfigurations; do
  refused "$key must be a non-empty list of strings" --set "$key=null"
  printf '%s:\n  %s:\n    a: x\n' "${key%%.*}" "${key#*.}" > "$tmp/map.yaml"
  refused "$key must be a non-empty list of strings" -f "$tmp/map.yaml"
done
refused 'argoScope.namespaces must be a non-empty list of strings' --set 'argoScope.namespaces={}'
refused 'argoScope.namespaceObjects holds ryft-extra, which argoScope.namespaces does not' --set 'argoScope.namespaceObjects={ryft-web,rabbitmq-system,ryft-extra}'
refused 'argoScope.namespaces must hold namespace names only' --set "argoScope.namespaces={$scope,ryft-*}"
renders --set "argoScope.namespaces={$scope,ryft-extra}" --set 'argoScope.namespaceObjects={ryft-web,rabbitmq-system,ryft-extra}'
grep -qF 'P1: Argo CD may write only the Namespaces ryft-web and rabbitmq-system, and deletes none.' <(render) ||
  fail "P1's Argo CD message must name argoScope.namespaceObjects"
grep -qF "in [\"ryft-web\",\"rabbitmq-system\"]" <(yq 'select(.metadata.name == "ryft-namespaces" and .kind == "ValidatingAdmissionPolicy") | .spec.validations[2].expression' <(render)) ||
  fail "P1's Argo CD rule must read argoScope.namespaceObjects"
# The Deny gate: argo-scope deny needs the general P6, P1 and P2 bindings in deny (not their vault twins).
refused 'actions.argo-scope is deny while actions.operators-cluster is not' --set actions.argo-scope=deny
refused 'actions.argo-scope is deny while actions.workloads-templates is not' --set actions.argo-scope=deny --set actions.operators-cluster=deny --set actions.namespaces=deny --set actions.workloads-templates-vault=deny --set actions.namespaces-vault=deny
renders --set actions.argo-scope=deny --set actions.operators-cluster=deny --set actions.namespaces=deny --set actions.workloads-templates=deny
renders --set actions.argo-scope=deny --set breakGlass=true

# The vault bindings: four of them, In [vault]; the general ones of the same policies NotIn [vault];
# each goes to deny alone, also while netpol's guard holds the general P3 binding on audit.
render > "$tmp/r.yaml" || fail "the committed values must render"
n=$(yq 'select(.kind == "ValidatingAdmissionPolicyBinding") | .metadata.name' "$tmp/r.yaml" | grep -vc '^---$')
[ "$n" = 18 ] || fail "the chart renders $n bindings, want 18"
n=$(yq 'select(.kind == "ValidatingAdmissionPolicy") | .metadata.name' "$tmp/r.yaml" | grep -vc '^---$')
[ "$n" = 13 ] || fail "the chart renders $n policies, want 13"
for b in ryft-namespaces-vault ryft-workloads-templates-vault ryft-workloads-pods-vault ryft-netpol-vault; do
  got=$(yq "select(.metadata.name == \"$b\" and .kind == \"ValidatingAdmissionPolicyBinding\") | .spec.matchResources.namespaceSelector.matchExpressions | tojson(0)" "$tmp/r.yaml")
  [ "$got" = '[{"key":"kubernetes.io/metadata.name","operator":"In","values":["vault"]}]' ] || fail "$b selects $got"
  general=${b%-vault}
  got=$(yq "select(.metadata.name == \"$general\" and .kind == \"ValidatingAdmissionPolicyBinding\") | .spec.matchResources.namespaceSelector.matchExpressions | tojson(0)" "$tmp/r.yaml")
  [ "$got" = '[{"key":"kubernetes.io/metadata.name","operator":"NotIn","values":["vault"]}]' ] || fail "$general selects $got"
done
for b in ryft-vso ryft-vault-trust ryft-operators-cluster ryft-argo-scope; do
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
grep -qF '["kv-operator","kv-root","kv-admin","kv-terraform","kv-default","kv-vault-snapshot","kv-snapshot"]' <<<"$p4" || fail "the reserved mounts are not refused"
grep -qF '"https://vault.vault.svc:8200"]' <(yq 'select(.metadata.name == "ryft-vso" and .kind == "ValidatingAdmissionPolicy") | .spec.validations[1].expression' "$tmp/x.yaml") || fail "the decommission keeps the in-cluster address"
echo "✓ admission_chart_guards_test"
