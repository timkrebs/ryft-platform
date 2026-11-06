#!/usr/bin/env bash
# The render of infra/aws/kubernetes/charts/vault-extras (and of cluster-issuers' Vault CA), which
# tests/addons.tftest.hcl cannot see (the Helm provider is mocked there): helm template with the values vault.tf
# passes, then yq on the objects and kubeconform on all of them.
#   - by default no ingress from VSO or the collector on 8200 (only the snapshot job and the peers);
#     with peers.vso.enabled and peers.collector.enabled, exactly those two rules more;
#   - the peers reach each other on 8200 and 8201, both ways;
#   - 0.0.0.0/0 only in the two egress rules to TCP 443, with the private ranges excepted; no ingress ipBlock;
#   - the PDB keeps the quorum (minAvailable 2 of 3, 3 of 5), AlwaysAllow, the server pods;
#   - the service-registration Role names the server pods (vault-0..N-1), get/update/patch on pods, nothing else;
#   - both Certificates are signed by the ClusterIssuer ryft-vault, which cluster-issuers renders from ryft-vault-ca.
# yq has no deep equality, so every comparison is of compact JSON (the render's key order is pinned with it).
# Needs helm, yq (mikefarah) and kubeconform (it fetches the schemas). Locally it skips without them; in CI's
# manifests job (GITHUB_JOB=manifests) or with VAULT_EXTRAS_CHART_REQUIRED=1 it FAILS without them.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
EXTRAS=$HERE/infra/aws/kubernetes/charts/vault-extras
ISSUERS=$HERE/infra/aws/kubernetes/charts/cluster-issuers
fail() { echo "✗ vault_extras_chart_test: $*" >&2; exit 1; }
skip() {
  if [ -n "${VAULT_EXTRAS_CHART_REQUIRED:-}" ] || [ "${GITHUB_JOB:-}" = manifests ]; then fail "$1, and this run must not skip"; fi
  echo "✓ vault_extras_chart_test (skipped: $1)"; exit 0
}
for t in helm yq kubeconform; do command -v "$t" >/dev/null 2>&1 || skip "no $t"; done
yq --version 2>&1 | grep -q mikefarah || skip "yq is not mikefarah's"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# As vault.tf passes them (tests/addons.tftest.hcl pins those): the VPC range, the replicas, the names.
render() { # <out> [--set k=v]...
  local out=$1; shift
  helm template vault-extras "$EXTRAS" -n vault --set vpcCIDR=10.0.0.0/16 \
    --set 'tls.dnsNames={vault,vault-0.vault-internal}' --set 'tls.ipAddresses={127.0.0.1}' "$@" > "$out"
}
# eq <file> <yq expression over all documents> <expected compact JSON> <message>
eq() {
  local got
  got=$(yq ea "$2 | to_json(0)" "$1" 2>&1) || fail "$4 (yq: $got)"
  [ "$got" = "$3" ] || fail "$4: got $got"
}

render "$tmp/default.yaml"
render "$tmp/peers.yaml" --set peers.vso.enabled=true --set peers.collector.enabled=true
render "$tmp/five.yaml" --set replicas=5

SERVER='select(.kind == "NetworkPolicy" and .metadata.name == "vault-server")'
P8200='[{"protocol":"TCP","port":8200}]'
PEERS='[{"protocol":"TCP","port":8200},{"protocol":"TCP","port":8201}]'
SEL='{"app.kubernetes.io/name":"vault","component":"server"}'
SNAP='{"app.kubernetes.io/name":"vault-snapshot"}'

# Peers off by default: the snapshot job and the server pods, nothing from another namespace.
eq "$tmp/default.yaml" "$SERVER | .spec.ingress" \
  "[{\"from\":[{\"podSelector\":{\"matchLabels\":$SNAP}}],\"ports\":$P8200},{\"from\":[{\"podSelector\":{\"matchLabels\":$SEL}}],\"ports\":$PEERS}]" \
  "by default vault-server admits the snapshot job on 8200 and the server pods on 8200/8201 only"
# Peers on: exactly the two rules more, first, each on 8200 only, with the charts' pod labels.
eq "$tmp/peers.yaml" "$SERVER | .spec.ingress | length" '4' "with both peers on, vault-server has four ingress rules"
eq "$tmp/peers.yaml" "$SERVER | .spec.ingress[0]" \
  "{\"from\":[{\"namespaceSelector\":{\"matchLabels\":{\"kubernetes.io/metadata.name\":\"vault-secrets-operator-system\"}},\"podSelector\":{\"matchLabels\":{\"app.kubernetes.io/name\":\"vault-secrets-operator\",\"control-plane\":\"controller-manager\"}}}],\"ports\":$P8200}" \
  "peers.vso.enabled admits the operator's pods on 8200 only"
eq "$tmp/peers.yaml" "$SERVER | .spec.ingress[1]" \
  "{\"from\":[{\"namespaceSelector\":{\"matchLabels\":{\"kubernetes.io/metadata.name\":\"monitoring\"}},\"podSelector\":{\"matchLabels\":{\"app.kubernetes.io/name\":\"alloy-deployment\"}}}],\"ports\":$P8200}" \
  "peers.collector.enabled admits the alloy-deployment pods on 8200 only"
eq "$tmp/peers.yaml" "[$SERVER | .spec.ingress[2,3]]" "$(yq ea "[$SERVER | .spec.ingress[]] | to_json(0)" "$tmp/default.yaml")" \
  "switching the peers on changes no other ingress rule"
if helm template vault-extras "$EXTRAS" -n vault --set vpcCIDR=10.0.0.0/16 --set peers.vso.enabled=true --set peers.vso.namespace= > /dev/null 2>&1; then
  fail "an enabled peer without a namespace must not render"
fi

# The server pods reach each other on 8200 and 8201 (the first egress rule).
eq "$tmp/default.yaml" "$SERVER | .spec.egress[0]" "{\"to\":[{\"podSelector\":{\"matchLabels\":$SEL}}],\"ports\":$PEERS}" \
  "the server pods reach each other on 8200 and 8201"

# 0.0.0.0/0 only in egress to TCP 443, never the private ranges or the link-local block (Pod Identity has its own
# /32 rule); no ingress rule admits any address range (not the VPC either, D51).
PUBLIC='{"to":[{"ipBlock":{"cidr":"0.0.0.0/0","except":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16","100.64.0.0/10","169.254.0.0/16"]}}],"ports":[{"protocol":"TCP","port":443}]}'
for f in default peers; do
  eq "$tmp/$f.yaml" '[select(.kind == "NetworkPolicy") | (.spec.ingress // [], .spec.egress // [])[] | select([(.from // [], .to // [])[] | .ipBlock.cidr] | any_c(. == "0.0.0.0/0"))]' \
    "[$PUBLIC,$PUBLIC]" "0.0.0.0/0 appears in exactly the two egress rules to TCP 443 (server, snapshot), private ranges excepted ($f)"
  eq "$tmp/$f.yaml" '[select(.kind == "NetworkPolicy") | .spec.ingress // [] | .[] | .from[] | select(has("ipBlock"))] | length' '0' \
    "no ingress rule admits an address range ($f)"
done
eq "$tmp/default.yaml" '[select(.kind == "NetworkPolicy" and .metadata.name == "default-deny") | .spec]' '[{"podSelector":{},"policyTypes":["Ingress","Egress"]}]' \
  "default-deny selects every pod, both ways"
# Every egress rule, whole: the peers, the API server in the VPC on 443, KMS/S3 on public 443, the Pod Identity
# agent on 80; the snapshot job has no ingress at all. DNS for every pod, to kube-dns only.
PODID='{"to":[{"ipBlock":{"cidr":"169.254.170.23/32"}}],"ports":[{"protocol":"TCP","port":80}]}'
for f in default peers; do
  eq "$tmp/$f.yaml" "$SERVER | .spec.egress" \
    "[{\"to\":[{\"podSelector\":{\"matchLabels\":$SEL}}],\"ports\":$PEERS},{\"to\":[{\"ipBlock\":{\"cidr\":\"10.0.0.0/16\"}}],\"ports\":[{\"protocol\":\"TCP\",\"port\":443}]},$PUBLIC,$PODID]" \
    "vault-server's egress is the peers, the VPC on 443, public 443 and the Pod Identity agent, nothing else ($f)"
  eq "$tmp/$f.yaml" '[select(.kind == "NetworkPolicy" and .metadata.name == "vault-snapshot") | .spec]' \
    "[{\"podSelector\":{\"matchLabels\":$SNAP},\"policyTypes\":[\"Ingress\",\"Egress\"],\"egress\":[{\"to\":[{\"podSelector\":{\"matchLabels\":$SEL}}],\"ports\":$P8200},$PUBLIC,$PODID]}]" \
    "vault-snapshot admits nothing and reaches the server on 8200, public 443 and the Pod Identity agent only ($f)"
done
eq "$tmp/default.yaml" '[select(.kind == "NetworkPolicy" and .metadata.name == "allow-dns") | .spec]' \
  '[{"podSelector":{},"policyTypes":["Egress"],"egress":[{"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"k8s-app":"kube-dns"}}}],"ports":[{"protocol":"UDP","port":53},{"protocol":"TCP","port":53}]}]}]' \
  "allow-dns: every pod to kube-dns on 53, nothing else"

# The disruption budget.
PDBSEL='{"matchLabels":{"app.kubernetes.io/name":"vault","app.kubernetes.io/instance":"vault","component":"server"}}'
eq "$tmp/default.yaml" '[select(.kind == "PodDisruptionBudget") | .spec]' "[{\"minAvailable\":2,\"unhealthyPodEvictionPolicy\":\"AlwaysAllow\",\"selector\":$PDBSEL}]" \
  "one PDB: 2 of 3 server pods, AlwaysAllow, the server pods"
eq "$tmp/five.yaml" '[select(.kind == "PodDisruptionBudget") | .spec.minAvailable]' '[3]' "the PDB keeps the quorum of five (3)"

# The service-registration Role and its binding; nothing cluster-wide.
eq "$tmp/default.yaml" '[select(.kind == "Role") | .rules]' '[[{"apiGroups":[""],"resources":["pods"],"resourceNames":["vault-0","vault-1","vault-2"],"verbs":["get","update","patch"]}]]' \
  "one Role: get, update, patch on vault-0, vault-1, vault-2 only"
eq "$tmp/five.yaml" '[select(.kind == "Role") | .rules[0].resourceNames]' '[["vault-0","vault-1","vault-2","vault-3","vault-4"]]' "the Role names every replica"
eq "$tmp/default.yaml" '[select(.kind == "RoleBinding") | {"roleRef": .roleRef, "subjects": .subjects}]' \
  '[{"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"vault-service-registration"},"subjects":[{"kind":"ServiceAccount","name":"vault","namespace":"vault"}]}]' \
  "the RoleBinding gives the Role to vault/vault only"
eq "$tmp/default.yaml" '[select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding")] | length' '0' "nothing cluster-wide"
eq "$tmp/default.yaml" '[.kind] | sort' '["Certificate","Certificate","NetworkPolicy","NetworkPolicy","NetworkPolicy","NetworkPolicy","PodDisruptionBudget","Role","RoleBinding"]' \
  "the chart renders exactly these objects"

# The Certificates and their issuer.
eq "$tmp/default.yaml" '[select(.kind == "Certificate") | .metadata.name + " " + (.spec.issuerRef | to_json(0))]' \
  '["vault-tls {\"name\":\"ryft-vault\",\"kind\":\"ClusterIssuer\",\"group\":\"cert-manager.io\"}","vault-ca {\"name\":\"ryft-vault\",\"kind\":\"ClusterIssuer\",\"group\":\"cert-manager.io\"}"]' \
  "vault-tls and vault-ca are signed by the ClusterIssuer ryft-vault"
helm template cluster-issuers "$ISSUERS" -n cert-manager --set email=ops@example.com --set region=eu-central-1 \
  --set hostedZoneID=Z0000000000000000000 --set dnsZone=example.com > "$tmp/issuers.yaml"
eq "$tmp/issuers.yaml" '[select(.kind == "ClusterIssuer" and .metadata.name == "ryft-vault") | .spec]' '[{"ca":{"secretName":"ryft-vault-ca"}}]' \
  "cluster-issuers renders the ClusterIssuer ryft-vault from the ryft-vault-ca Secret"

# Schemas: core kinds from kubeconform's default, the cert-manager kinds from the CRDs catalog (as make k8s-validate).
for f in default peers five issuers; do
  kubeconform -strict -summary -schema-location default \
    -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' \
    - < "$tmp/$f.yaml" > "$tmp/kubeconform.log" 2>&1 || { cat "$tmp/kubeconform.log" >&2; fail "kubeconform refused the $f render"; }
done

echo "✓ vault_extras_chart_test"
