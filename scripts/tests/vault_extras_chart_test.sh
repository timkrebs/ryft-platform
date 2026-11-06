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
#   - both Certificates are signed by the ClusterIssuer ryft-vault, which cluster-issuers renders from ryft-vault-ca;
#   - the snapshot CronJob (D192): every 6 hours, Forbid, history 3/3, the two images vault.tf pins (each matched by
#     the admission chart's P2 regex for vault), the active node, the projected vault token, create-only and
#     owner-checked put-object with the full key ARN, restricted securityContext, no API token; and it refuses to
#     render without a digest, with an alias for the key, or with a field missing.
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

# The snapshot job's images as vault.tf pins them, and the admission chart's P2 patterns for namespace vault.
VAULT_TF=$HERE/infra/aws/kubernetes/vault.tf
ADMISSION_VALUES=$HERE/infra/aws/kubernetes/charts/admission-policy/values.yaml
one() { # <what> <value>: exactly one non-empty line
  [ -n "$2" ] && [ "$(printf '%s\n' "$2" | wc -l | tr -d ' ')" = 1 ] || fail "cannot read exactly one $1"
  printf '%s' "$2"
}
VAULT_REPO=$(one "Vault repository in vault.tf" "$(sed -nE 's/^ +repository = "(docker[.]io\/hashicorp\/vault)"$/\1/p' "$VAULT_TF")")
VAULT_TAG=$(one "Vault tag in vault.tf" "$(sed -nE 's/^ +tag += "([0-9][0-9.]*@sha256:[0-9a-f]{64})"$/\1/p' "$VAULT_TF")")
VAULT_IMG=$VAULT_REPO:$VAULT_TAG
CLI_IMG=$(one "AWS CLI image in vault.tf" "$(sed -nE 's/^ +vault_snapshot_aws_cli_image = "([^"]+)"$/\1/p' "$VAULT_TF")")
RE_VAULT=$(one "images.vault in the admission values" "$(sed -nE "s/^  vault: '(.+)'$/\1/p" "$ADMISSION_VALUES")")
RE_CLI=$(one "images.awsCli in the admission values" "$(sed -nE "s/^  awsCli: '(.+)'$/\1/p" "$ADMISSION_VALUES")")
printf '%s' "$VAULT_IMG" | grep -Eq "$RE_VAULT" || fail "admission P2 (images.vault) does not admit vault.tf's server image $VAULT_IMG"
printf '%s' "$CLI_IMG" | grep -Eq "$RE_CLI" || fail "admission P2 (images.awsCli) does not admit vault.tf's AWS CLI image $CLI_IMG"
printf '%s' "$VAULT_IMG" | grep -Eq "$RE_CLI" && fail "images.awsCli must not admit the server image"
printf '%s' "$CLI_IMG" | grep -Eq "$RE_VAULT" && fail "images.vault must not admit the AWS CLI image"

KEY_ARN=arn:aws:kms:eu-central-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b
BUCKET=ryft-prod-euc1-vault-snapshots-123456789012
cat > "$tmp/snapshot.yaml" <<EOF
snapshot:
  schedule: "17 */6 * * *"
  images:
    vault: "$VAULT_IMG"
    awsCli: "$CLI_IMG"
  bucket: $BUCKET
  accountID: "123456789012"
  keyARN: $KEY_ARN
  region: eu-central-1
EOF

# As vault.tf passes them (tests/addons.tftest.hcl pins those): the VPC range, the replicas, the names, the job.
render() { # <out> [--set k=v]...
  local out=$1; shift
  helm template vault-extras "$EXTRAS" -n vault --set vpcCIDR=10.0.0.0/16 \
    --set 'tls.dnsNames={vault,vault-0.vault-internal}' --set 'tls.ipAddresses={127.0.0.1}' -f "$tmp/snapshot.yaml" "$@" > "$out"
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
if render "$tmp/bad.yaml" --set peers.vso.enabled=true --set peers.vso.namespace= 2>/dev/null; then
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
eq "$tmp/default.yaml" '[.kind] | sort' '["Certificate","Certificate","CronJob","NetworkPolicy","NetworkPolicy","NetworkPolicy","NetworkPolicy","PodDisruptionBudget","Role","RoleBinding","ServiceAccount"]' \
  "the chart renders exactly these objects"

# The snapshot CronJob (D192).
CJ='select(.kind == "CronJob" and .metadata.name == "vault-snapshot")'
POD="$CJ | .spec.jobTemplate.spec.template"
eq "$tmp/default.yaml" "[$CJ | .spec | pick([\"schedule\", \"timeZone\", \"concurrencyPolicy\", \"startingDeadlineSeconds\", \"successfulJobsHistoryLimit\", \"failedJobsHistoryLimit\"])]" \
  '[{"schedule":"17 */6 * * *","timeZone":"Etc/UTC","concurrencyPolicy":"Forbid","startingDeadlineSeconds":3600,"successfulJobsHistoryLimit":3,"failedJobsHistoryLimit":3}]' \
  "the CronJob runs every 6 hours in UTC, never overlapping, and keeps 3 runs of each outcome"
eq "$tmp/default.yaml" "[$CJ | .spec.jobTemplate.spec | pick([\"backoffLimit\", \"activeDeadlineSeconds\"])]" '[{"backoffLimit":2,"activeDeadlineSeconds":600}]' \
  "a run is bounded: two retries, 10 minutes (the vault token's lifetime)"
eq "$tmp/default.yaml" "[$POD | .metadata.labels]" "[$SNAP]" "the job's pods carry the label the NetworkPolicies select (vault-snapshot)"
eq "$tmp/default.yaml" "[$POD | .spec | pick([\"serviceAccountName\", \"automountServiceAccountToken\", \"restartPolicy\"])]" \
  '[{"serviceAccountName":"vault-snapshot","automountServiceAccountToken":false,"restartPolicy":"Never"}]' \
  "the pods run as vault/vault-snapshot (the Pod Identity association) without an API token"
eq "$tmp/default.yaml" '[select(.kind == "ServiceAccount") | {"name": .metadata.name, "automount": .automountServiceAccountToken}]' \
  '[{"name":"vault-snapshot","automount":false}]' "the only ServiceAccount is vault-snapshot, without an automounted token"
eq "$tmp/default.yaml" "[$POD | .spec.securityContext]" \
  '[{"runAsNonRoot":true,"runAsUser":100,"runAsGroup":1000,"fsGroup":1000,"seccompProfile":{"type":"RuntimeDefault"}}]' \
  "the pod runs as 100:1000, non-root, RuntimeDefault seccomp"
eq "$tmp/default.yaml" "[$POD | (.spec.initContainers[], .spec.containers[]) | pick([\"name\", \"image\", \"securityContext\"])]" \
  "[{\"name\":\"snapshot\",\"image\":\"$VAULT_IMG\",\"securityContext\":{\"allowPrivilegeEscalation\":false,\"capabilities\":{\"drop\":[\"ALL\"]},\"readOnlyRootFilesystem\":true}},{\"name\":\"upload\",\"image\":\"$CLI_IMG\",\"securityContext\":{\"allowPrivilegeEscalation\":false,\"capabilities\":{\"drop\":[\"ALL\"]},\"readOnlyRootFilesystem\":true}}]" \
  "one init container with the server's image and one upload container with the AWS CLI, both pinned, both restricted"
eq "$tmp/default.yaml" "[$POD | (.spec.initContainers[], .spec.containers[]) | ((.resources.limits.memory != null) and (.resources.requests.cpu != null))] | all" 'true' \
  "both containers have requests and a memory limit"
eq "$tmp/default.yaml" "[$POD | .spec.volumes[] | select(.projected) | .projected.sources]" \
  '[[{"serviceAccountToken":{"audience":"vault","expirationSeconds":600,"path":"token"}}]]' \
  "the only token is projected, for audience vault, for 10 minutes"
eq "$tmp/default.yaml" "[$POD | .spec.containers[0].volumeMounts[].name]" '["work"]' "the upload container sees the snapshot only (no token, no CA)"
eq "$tmp/default.yaml" "$POD | .spec.initContainers[0].env | map({\"key\": .name, \"value\": .value}) | from_entries | pick([\"VAULT_ADDR\", \"VAULT_AUTH_MOUNT\", \"VAULT_ROLE\"])" \
  '{"VAULT_ADDR":"https://vault-active.vault.svc:8200","VAULT_AUTH_MOUNT":"kubernetes","VAULT_ROLE":"vault-snapshot"}' \
  "the save goes to the active node (a standby's is incomplete) and logs in as vault-snapshot"
eq "$tmp/default.yaml" "$POD | .spec.containers[0].env | map({\"key\": .name, \"value\": .value}) | from_entries | pick([\"AWS_REGION\", \"BUCKET\", \"BUCKET_OWNER\", \"SNAPSHOT_KEY_ARN\"])" \
  "{\"AWS_REGION\":\"eu-central-1\",\"BUCKET\":\"$BUCKET\",\"BUCKET_OWNER\":\"123456789012\",\"SNAPSHOT_KEY_ARN\":\"$KEY_ARN\"}" \
  "the upload names the bucket, its owner and the key's full ARN"
script() { yq ea "$POD | .spec.$1.args[0]" "$tmp/default.yaml"; }
for want in 'token=$(vault write -field=token "auth/$VAULT_AUTH_MOUNT/login"' 'export VAULT_TOKEN="$token"' "trap 'exit 143' TERM INT" \
  "trap 'rc=\$?; [ \"\$rc\" = 0 ] || rm -f /work/vault.snap' EXIT" \
  'vault operator raft snapshot save /work/vault.snap & wait $!' 'vault operator raft snapshot inspect /work/vault.snap >/dev/null & wait $!'; do
  script initContainers[0] | grep -qF -- "$want" || fail "the snapshot script lacks: $want"
done
# The traps come before the login: a TERM during the login still ends through the EXIT trap.
[ "$(script initContainers[0] | grep -nF "trap 'exit 143' TERM INT" | cut -d: -f1)" -lt "$(script initContainers[0] | grep -nF 'token=$(vault write' | cut -d: -f1)" ] ||
  fail "the snapshot script must set its traps before the login"
for c in initContainers containers; do
  script "$c[0]" | sh -n || fail "the $c[0] script does not parse"
done
eq "$tmp/default.yaml" "[$POD | .spec.volumes[] | select(.name == \"work\") | .emptyDir]" '[{"medium":"Memory","sizeLimit":"64Mi"}]' \
  "the snapshot lives in memory (64Mi), never on the node's disk"
eq "$tmp/default.yaml" "[$POD | (.spec.initContainers[], .spec.containers[]) | .resources.limits.memory]" '["320Mi","320Mi"]' \
  "each container's memory limit holds the tmpfs snapshot (64Mi) as well"
script initContainers[0] | grep -qF 'token revoke' && fail "the snapshot token may not revoke itself (no default policy): no revoke attempt"
script initContainers[0] | grep -qE 'export VAULT_TOKEN="?\$\(' && fail "the login must be assigned before the export (export hides a failed substitution from -e)"
for want in 'set -o pipefail' "trap 'exit 143' TERM INT" "trap 'rm -f \"\$f\"' EXIT" 'aws s3api put-object' "--if-none-match '*'" '--expected-bucket-owner "$BUCKET_OWNER"' \
  '--server-side-encryption aws:kms --ssekms-key-id "$SNAPSHOT_KEY_ARN"' '--checksum-algorithm SHA256' '--key "$key"' 'key="snapshots/vault-'; do
  script containers[0] | grep -qF -- "$want" || fail "the upload script lacks: $want"
done
script containers[0] | grep -qE 'aws s3 (cp|sync|mv)|create-multipart|upload-part' && fail "the upload must be one put-object (multipart would need kms:Decrypt)"

# It refuses to render what could not upload, or what admission would refuse.
refuse() { # <message> [--set k=v]...
  local msg=$1; shift
  if render "$tmp/bad.yaml" "$@" 2>/dev/null; then fail "must not render: $msg"; fi
}
refuse "the AWS CLI by tag only" --set snapshot.images.awsCli=public.ecr.aws/aws-cli/aws-cli:2.39.5
refuse "the server image by tag only" --set snapshot.images.vault=docker.io/hashicorp/vault:2.2.1
refuse "an alias for the key" --set snapshot.keyARN=alias/ryft-prod-euc1-vault-snapshots
refuse "a key of another account" --set snapshot.keyARN=arn:aws:kms:eu-central-1:210987654321:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b
refuse "a key in another region" --set snapshot.keyARN=arn:aws:kms:eu-west-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b
refuse "no bucket" --set snapshot.bucket=
refuse "no schedule" --set snapshot.schedule=
refuse "no account" --set snapshot.accountID=

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
