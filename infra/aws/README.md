# RYFT on AWS (EKS)

Terraform for running the RYFT platform (`deploy/k8s`) on EKS behind
`https://api.ryftapp.de`. Three root modules with separate state, applied in order:

| Root | What | Lifetime |
| --- | --- | --- |
| `infra/aws/dns` | Route 53 zone `ryftapp.de`, CAA (Let's Encrypt only), the mail records (no mail by default, D212) | long-lived; survives cluster rebuilds |
| `infra/aws` | VPC + EKS cluster `ryft-prod-euc1`, node group, core add-ons; Vault's KMS keys and snapshot bucket | the cluster; the Vault keys and bucket outlive it (`prevent_destroy`) |
| `infra/aws/kubernetes` | in-cluster edge: AWS Load Balancer Controller, Traefik (NLB, HTTPS only), cert-manager + Let's Encrypt issuers, ExternalDNS, encrypted default StorageClass | the cluster's add-ons |

The zone has its own root so destroying the cluster never changes the nameservers at
GoDaddy. The add-ons have their own root because the Helm/Kubernetes providers must not
be configured from a cluster created in the same apply.

```
infra/aws/
├── terraform.tf providers.tf locals.tf main.tf variables.tf outputs.tf
├── vault.tf                   Vault's unseal key, snapshot key and snapshot bucket (prevent_destroy)
├── terraform.tfvars.example   copy to terraform.tfvars (git-ignored)
├── modules/
│   ├── network/               VPC, subnets, NAT, flow logs   (terraform-aws-modules/vpc v6)
│   └── eks/                   cluster, node group, add-ons, EBS CSI Pod Identity role
│                                                             (terraform-aws-modules/eks v21)
├── tests/ryft.tftest.hcl      terraform test, providers mocked
├── tests/vault.tftest.hcl     the Vault keys and bucket (mocked; greps prevent_destroy)
├── dns/                       root: zone + CAA + mail records      (tests/dns.tftest.hcl)
└── kubernetes/                root: Pod Identity roles, Helm releases, StorageClass
    ├── charts/cluster-issuers local chart: letsencrypt-staging / letsencrypt-prod (DNS-01)
    ├── charts/admission-policy local chart: ValidatingAdmissionPolicies P1-P11, P13 + bindings (D120)
    ├── admission.tf admission-values.yaml   the release (off), its committed values, the checks
    └── tests/addons.tftest.hcl tests/admission.tftest.hcl
```

## What it creates

| Resource | Name / value |
| --- | --- |
| Hosted zone | `ryftapp.de`, `prevent_destroy`; CAA `issue`/`issuewild` `letsencrypt.org`; mail (D212): by default null MX `0 .`, SPF `v=spf1 -all`, DMARC `p=reject` with strict alignment, no DKIM. A provider's values go in `dns/terraform.tfvars` (Google Workspace is the example in `dns/terraform.tfvars.example`) |
| VPC | `ryft-prod-euc1`, `10.0.0.0/16`, 3 AZs: private `/20` subnets for nodes and pods, public `/24` subnets for load balancers, one NAT gateway, flow logs (30 days) |
| EKS cluster | `ryft-prod-euc1`, Kubernetes 1.36, public API endpoint restricted to `endpoint_public_access_cidrs`, secrets encrypted with KMS, audit/api/authenticator logs |
| Node group | `ryft-prod-euc1-default-…`: 3 × t3.large (AL2023), one per zone |
| EKS add-ons | `vpc-cni` (NetworkPolicy enforcement on), `kube-proxy`, `coredns`, `eks-pod-identity-agent`, `aws-ebs-csi-driver`, `metrics-server` |
| Edge (Helm) | `aws-load-balancer-controller` 3.6.0, `traefik` 42.0.0 in `kube-system` behind an internet-facing NLB, port 443 only; `cert-manager` v1.21.2 with ClusterIssuers `letsencrypt-staging`/`-prod` (DNS-01 in Route 53); `external-dns` 1.23.0 (zone `ryftapp.de` only, TXT ownership) |
| Argo CD (Helm) | `argo-cd` 10.10.5 in `argocd`, non-HA, no SSO. Its ClusterRoles are **narrowed** (T12-f, D120): the controller has only the kinds that `deploy/argocd/apps` renders, no `secrets`, no `pods/exec`, no `pods/ephemeralcontainers`, nothing on `ValidatingAdmissionPolicy`/`-Binding`, no `escalate` on a ClusterRole and `bind` on one only (`messaging-topology-metrics-auth-role`; `escalate`/`bind` on the four operator Roles by name), `get`/`list`/`watch` on Applications cluster-wide (strict mode drops a kind it cannot list, silently); the server is read-only cluster-wide. This narrows what Argo CD can grant itself; it does not stop a stolen controller token from writing ConfigMaps and workloads in other namespaces (`aws-auth` included), nor from writing a ClusterRole and binding it to `system:serviceaccounts` (the controller's RBAC-kind rules have no `resourceNames`): the workload and RBAC writes close only with the admission policies P6 and P12 both in Deny and this change applied (what stays open even then: OPERATIONS, "What stays open"); `resource.respectRBAC: strict`; the ApplicationSet controller is at 0 replicas. The rules and their reasons are in `docs/OPERATIONS.md` (Deploy and roll back). A new kind in a synced path needs its rule in `kubernetes/argocd.tf` first |
| StorageClass | `gp3-encrypted` (default): gp3, encrypted, `WaitForFirstConsumer`, `Retain` |
| IAM | cluster/node roles; Pod Identity roles for EBS CSI, the LB controller, ExternalDNS and cert-manager (the last two scoped to the zone) |
| Vault keys (`vault.tf`) | KMS `alias/ryft-prod-euc1-vault-unseal` (Vault CE's auto-unseal, used only with `vault_seal = "awskms"` in `kubernetes/`; under the default Shamir seal it is kept unused, D208, about $1 a month) and `alias/ryft-prod-euc1-vault-snapshots` (the bucket): yearly rotation, 30-day deletion window, `prevent_destroy`. The key policy lets the account administer them and names no role, so IAM policies decide who uses them (the Pod Identity roles are in `kubernetes/`). It denies `ScheduleKeyDeletion` and `DisableKey` to everyone (`NoDeletionOrDisable`; see Destroying). The unseal key can additionally be limited to listed principals (below) |
| Vault snapshots (`vault.tf`) | S3 `ryft-prod-euc1-vault-snapshots-<account id>`, `prevent_destroy`: versioned, SSE-KMS with the snapshot key and the S3 bucket key, all four public access blocks, `BucketOwnerEnforced`; under `snapshots/` objects expire after 30 days, old versions 7 days later, incomplete uploads after 1; the policy denies requests without TLS, any `PutObject` that does not name the snapshot key's ARN (very likely also multipart part uploads, which send no key header: the job uploads with one `put-object`), and any `PutObject` without `If-None-Match` (`DenyPutWithoutIfNoneMatch`: create-only, no identity can overwrite a snapshot; CopyObject and part uploads are refused too) |

Request path: app → `api.ryftapp.de` (Route 53 alias, written by ExternalDNS) → NLB →
Traefik (Let's Encrypt certificate, TLS) → `/v1/sync` ingest pods, `/v1` api pods.

### Unseal key allowlist (optional, off by default)

Only meaningful with `vault_seal = "awskms"` in `kubernetes/`; under Shamir no role uses the key.
Without it, any identity in the account whose IAM policy allows `kms:Decrypt` on `*` (an
administrator, a broad CI role) can use Vault's unseal key. `vault_unseal_allowed_principal_arns`
adds a Deny to the unseal key's policy: `kms:Encrypt`, `kms:Decrypt`, `kms:ReEncrypt*` and
`kms:GenerateDataKey*` for every principal whose `aws:PrincipalArn` is not like one of the
listed patterns. The snapshot key is not affected.

It is off because a wrong list fails silently: nothing breaks at apply, but the next Vault
pod that starts cannot unseal and stays sealed. Turn it on only after the in-cluster Vault has
unsealed once with its Pod Identity role (`infra/aws/kubernetes`, `vault/vault`):

1. Check the role: `kubectl --context ryft-prod-euc1 -n vault exec vault-0 -- vault status`
   shows `Seal Type awskms` and `Sealed false`. CloudTrail shows `Decrypt` on the unseal key by
   a role named `<cluster>-vault-unseal-…`.
2. In `terraform.tfvars`, list Vault's role. Two forms:
   - **stricter, preferred once the role exists:** its exact ARN,
     `terraform -chdir=infra/aws/kubernetes output -raw vault_unseal_role_arn`. It has to be
     updated if the role is ever recreated: the module gives it a new generated suffix;
   - or the pattern `terraform -chdir=infra/aws output -raw vault_unseal_role_arn_pattern`
     (`role/<cluster>-vault-unseal-*`), which survives a recreated role. ArnLike's `*` also
     matches `/`, so the pattern admits any role whose path and name start with
     `<cluster>-vault-unseal-` (`role/<cluster>-vault-unseal-x/anything`). Anyone who may
     create IAM roles could make one that matches.

   Add your own admin role if you will ever restore a production snapshot outside the
   cluster, for example in the kind drill with the same key. For IAM Identity Center that is
   `arn:aws:iam::<account>:role/aws-reserved/sso.amazonaws.com/*AWSReservedSSO_AdministratorAccess_*`
   (the path may or may not hold a region segment; the `*` before the name covers both).
   Check the exact ARN first with `aws iam list-roles --path-prefix /aws-reserved/ --query
   'Roles[].Arn'`, and prefer it too.
3. Plan (only the unseal key's `policy` changes in place) and apply. Then prove it: delete one
   standby pod and watch it come back unsealed.

A `check` warns on every plan if the list holds neither the exact pattern nor the role's exact
ARN; a bare prefix without `-*` or a typo in the suffix matches no role, so it warns too. The
list is no barrier against an administrator: `AccountAdministers` keeps `kms:PutKeyPolicy`, so an
admin can rewrite the policy (CloudTrail records that). It also blocks every principal not
listed, the account root and the restore drill's identity included. Rollback: set the list
back to `[]` and apply.

## Usage

Requires Terraform 1.16.6, `terraform login` to HCP Terraform (organization
`tim-krebs-org`), AWS credentials for the target account, `aws` CLI, `kubectl` and `dig`.
Each root keeps its state in its own HCP Terraform workspace (`ryft-prod-euc1-platform`,
`-dns`, `-kubernetes`), with execution mode Local. Plans and applies run here, and HCP
stores, locks and versions the state (`docs/OPERATIONS.md` → Terraform state).

**1. DNS zone** — once:

```bash
cd infra/aws/dns
terraform init && terraform plan -out=tfplan && terraform apply tfplan
terraform output godaddy_instructions
```

At GoDaddy: My Products → ryftapp.de → DNS → Nameservers → **Change** → *I'll use my own
nameservers* → enter the four `ns-….awsdns-…` servers. DENIC usually publishes the
change within minutes; resolvers may cache the old ones for up to a day. Check:

```bash
dig NS ryftapp.de @a.nic.de +short      # → the four awsdns servers
```

**2. Cluster**:

```bash
cd infra/aws
cp terraform.tfvars.example terraform.tfvars   # set endpoint_public_access_cidrs to your IP
terraform init
terraform plan -out=tfplan
terraform apply tfplan                          # ~15 minutes on first apply
$(terraform output -raw configure_kubectl)      # adds context "ryft-prod-euc1" to ~/.kube/config
kubectl get nodes
```

**3. Edge add-ons** — needs steps 1 and 2:

```bash
cd infra/aws/kubernetes
cp terraform.tfvars.example terraform.tfvars   # set acme_email
terraform init && terraform plan -out=tfplan && terraform apply tfplan
```

Verify before deploying any app:

```bash
kubectl get sc                                  # only gp3-encrypted (default)
kubectl -n kube-system get deploy aws-load-balancer-controller traefik
kubectl -n cert-manager get pods; kubectl get clusterissuer   # READY True
kubectl -n external-dns logs deploy/external-dns | tail
$(terraform output -raw load_balancer_hostname_command)       # the NLB hostname
```

**4. RYFT** — `deploy/k8s/overlays/eks` (see `docs/OPERATIONS.md`, section EKS). It needs
images on GHCR (the first CI run) and, for the app's Release build, Supabase auth (M1).
Once applied, cert-manager issues the `api.ryftapp.de` certificate and ExternalDNS points
the name at the NLB within a minute or two.

## Admission policies (D120)

`infra/aws/kubernetes/admission.tf` installs twelve built-in ValidatingAdmissionPolicies
(P1–P11, P13) and seventeen bindings (P2 has one for templates and one for Pods; P1, P2 and P3
have a second set for namespace `vault` alone) from the local
chart `charts/admission-policy`, as the release `admission-policy` in `kube-system`. They
judge what PSA cannot: images per namespace, commands and service account tokens, the
auth-mode keys, NetworkPolicy and Traefik shapes, the Vault Secrets Operator objects, the
RabbitMQ operators' cluster power and reach, the broker, Argo CD's own objects, and the chain
of the `ryft-vault` issuer (P13: what it signs, which issuer may write the CA's Secrets, and
that Argo CD writes only selfSigned Issuers). The namespace `vault` is in scope for P1–P3 (PSA
restricted labels, the pinned images only: P2 judges images there, not commands, and egress to
the VPC CIDR on 443 and the Pod Identity agent on 80) through the bindings
`ryft-namespaces-vault`, `ryft-workloads-templates-vault`, `ryft-workloads-pods-vault` and
`ryft-netpol-vault` (`namespaceSelector` In `[vault]`; the general ones carry NotIn), so
those go to deny before the Vault bootstrap, whatever step the `ryft-*` rollout is at. No pods
run for them and no webhook can be down; the cost is nil (API server CPU only).

**It ships off.** `admission-values.yaml` holds `enabled: false`, so a plan creates nothing.
It also holds the Vault addresses P4 admits, `vaultAddresses` (the in-cluster
`https://vault.vault.svc:8200`, D207, and HCP's dead address until the follow-up PR that commits
the overlays infra/vault regenerates, `docs/work/2026-11-13-rebuild/followup-overlays.md`; a check
requires every generated `deploy/k8s/overlays/vault-namespaces/*/vault.yaml` to carry one of them),
Argo CD's service account names and the action of each binding, `audit` or `deny`:

| `actions` value | `validationActions` | Meaning |
| --- | --- | --- |
| `audit` (the default) | `Warn`, `Audit` | the request passes; `kubectl` warns and the audit log in CloudWatch records it |
| `deny` | `Deny`, `Audit` | the request is refused and recorded |
| break-glass | `Warn`, `Audit` for **every** binding | the policies stay visible and stop refusing |

`Deny` is never combined with `Warn` (the API server refuses that), and anything that is
not exactly `deny` is applied as `Warn`+`Audit`. A rollout step is a reviewed pull request
that edits `admission-values.yaml` (see the keys' comments for the steps A–D), followed by
your apply. Changing `enabled` to `false` later destroys the release and every policy.

**Order: T12-g before Step A.** P6 refuses the release's vendored cluster-wide ClusterRoles of
both RabbitMQ operators (Secrets, pods/exec, Pod writes), so T12-g (both operators scoped to
`ryft-rabbitmq`, `deploy/rabbitmq/operators/*/scoped-rbac.yaml`) has to be merged and synced
before `operators-cluster` goes to `deny`, ideally before Phase 1 ends. In Audit, P6 flags
those two roles on every refresh: expected, not an unexplained finding. The release refuses
`operators-cluster: deny` while T12-g's file is missing (a precondition; break-glass bypasses
it). After Step A, reverting T12-g by git needs the break-glass.

Variables (all optional; see `terraform.tfvars.example`): `admission_break_glass` (bool),
`admission_actions` (a one-plan override per binding), `admission_enabled` (a plan-only
override of the file; never apply with it set) and `admission_scoped_rbac_file` (a test knob
for the T12-g precondition). For Vault: `vault_seal` (`shamir`, the default: no seal stanza,
no unseal role or association, no unseal alias read, and the server's public 443 egress is
not rendered; `awskms`: the KMS auto-unseal, D208), and `vault_collector_peer` (bool, default false), which
admits the monitoring collector to Vault's 8200; true only after your own init (OPERATIONS →
Vault metrics (once)), false again before a restore into a fresh cluster.

Outputs: `admission_enabled` and `admission_binding_actions` (what each binding is given).
Three `lifecycle.precondition`s on the release stop a plan that sets `argocd` to deny while
`projects` is empty (P7 would refuse every AppProject write), `netpol` to deny while
`netpol.brokerEgressPublic` is true, or `operators-cluster` to deny before T12-g is in the tree
(the chart refuses to render the first two as well; break-glass bypasses all three). `check`
blocks warn on every plan about an action or binding the chart does not have, a Vault
address that differs from the generated `vault.yaml` (or a file without exactly one), the
variable `admission_enabled` standing in for the reviewed file, and **drift**: the cluster's
policies and bindings are compared with the chart's own render of the same values (a
`helm_template` data source): a binding or policy that is missing or was edited (including its
resource rules, selectors and matchPolicy; the server's defaults are not drift), a binding left
in break-glass, a stale policy after an upgrade that was not applied. The drift checks
read the cluster, so they need it like any plan, and they say so on the first plan after
enabling (nothing is there yet).

The bindings carry `admission.ryft.dev/intended-action` (the committed `audit` or `deny`),
`admission.ryft.dev/key`, and `admission.ryft.dev/break-glass: "true"` while Terraform's
break-glass is on, so T12-e can alert on a break-glass that was left set.

Chart values worth knowing (`charts/admission-policy/values.yaml`):

- `projects`: the decoded `deploy/argocd/project.yaml` documents P7 compares AppProjects
  against. Empty until T12-b wires it, which makes P7 refuse every AppProject write, so keep
  the `argocd` binding on `audit` until then.
- `netpol.brokerEgressPublic`: `true` while `ryft-rabbitmq` still has its 443 egress rule
  open to `0.0.0.0/0`; the PR that narrows it (T12-k) sets it to `false`.
- `images.vault`, `images.awsCli`: the anchored, digest-pinned image patterns P2 admits in
  `vault`. `awsCli` is the snapshot CronJob's upload image (AWS CLI 2.39.5 by index digest), the
  same reference as `vault_snapshot_aws_cli_image` in `kubernetes/vault.tf`; change both together
  (`tests/addons.tftest.hcl` and `scripts/tests/vault_extras_chart_test.sh` compare them). P2
  admits it only in a pod whose `serviceAccountName` is `vault.snapshotServiceAccount`
  (`vault-snapshot`, the one with the Pod Identity association).
- `vault.*`: what P13 lets the `ryft-vault` issuer sign (the server Certificate `vault-tls`
  with its DNS names, IPs and usages; the per-namespace CA Certificate `vault-ca` with the
  common name `vso.<namespace>` and no SAN), the one user that may create a CertificateRequest
  or write its status, and the root of the chain (`rootCASecret`, `bootstrapIssuer`,
  `clusterResourceNamespace`) that only its own Certificate and the ClusterIssuer may use.
- `vso.reservedServices`: the names infra/vault refuses as services; no namespace that maps to
  one may read `kv-<name>` (P4).
- `vso.kvMountPrefix`: the in-cluster Vault's mount per service (`kv-<service>`); `vso.kvMount`
  and `vso.vaultNamespacePrefix` hold the HCP shape until the decommission empties them.
- `breakGlass`, `actions`, `vpcCIDR`, `vaultAddresses`: set from the file and the variables.

Render and check the chart without a cluster (the values file is the CI input too):

```bash
helm lint infra/aws/kubernetes/charts/admission-policy -f infra/aws/kubernetes/admission-values.yaml
helm template admission-policy infra/aws/kubernetes/charts/admission-policy \
  -f infra/aws/kubernetes/admission-values.yaml                  # all Warn+Audit
helm template admission-policy infra/aws/kubernetes/charts/admission-policy \
  -f infra/aws/kubernetes/admission-values.yaml --set breakGlass=true,actions.vso=deny  # still Warn+Audit
```

**Offline CEL test.** `make admission-celtest` (`admission-celtest/`, its own Go module
outside the chart, so nothing of it can enter the Helm release) renders the chart with the committed values and the decoded `project.yaml`,
renders `deploy/` (`helm template`, `kubectl kustomize`; read-only, no cluster), and
evaluates every policy with the Kubernetes CEL libraries (the apiserver's compiler,
validator and cost budget) against:

- every object of those renders, on create and update, which must all be admitted;
- the hand-written fixtures in `admission-celtest/testdata/objects.json` and the mutations in
  the Go files, one or more refused fixture per rule, plus the exemptions and carve-outs that
  must pass (the broker, finalizer removal, the controller's status writes, P10's operations).

Each refusal's message is pinned in `testdata/messages.golden`, so a case refused by the
wrong rule shows as a diff (`UPDATE_GOLDEN=1 admission-celtest/run.sh` rewrites it after a
review). A CEL error is never an acceptable refusal. It exits non-zero on any mismatch.
Needs `helm`, `kubectl` (kustomize only), `yq` and Go (the module's `go` line follows
`k8s.io/apiserver`, v0.36 for EKS 1.36). `CELTEST_REPO=<copy of the tree>` renders another
tree's manifests against this checkout's chart and values; the run is green on the manifests of
T12-g (Roles in `ryft-rabbitmq`) and on those before it (the release's two ClusterRoles are
then expected to be refused by P6). **When the EKS minor changes, bump `k8s.io/*` in
`admission-celtest/go.mod` to the same minor first** (the CEL library of the new minor is
what the new API server runs), then run the target.
It proves the expressions compile, evaluate and decide as intended on those objects. It
does **not** involve an API server: `status.typeChecking`, CRD defaulting, admission
ordering and the apiserver-dependent claims are the kind run's job (next section). CI's
`admission` job runs this target first.

**Kind run (T12-d).** `scripts/admission_e2e.sh` runs the policies against a real API server
(kind, at EKS's minor, the node image pinned by digest), in two modes:

- `--mode ci`, CI's required job `admission` (about 5 minutes; on changes to `deploy/**`,
  `infra/aws/kubernetes/**`, `ci.yml`, the Makefile and the scripts of the job; skipped for a promote PR and for its merge to main; on a pull request it also fails when the chart's templates or values changed and `Chart.yaml`'s version did not go up):
  the CRDs of VSO 1.6.0, cert-manager, Traefik, RabbitMQ and Argo CD; the namespaces with
  infra/vault's labels; Argo CD's chart rendered with `argocd.tf`'s values (Terraform evaluates
  `local.argocd_values` offline, `scripts/lib/tf_values.sh`) and its rendered ClusterRoles
  compared with the rules in the values (chart drift) and checked for what T12-f forbids; the
  chart installed twice, in Audit with the committed values and in Deny with every binding
  (and `brokerEgressPublic: false`, the chart's guards); every Application's render
  `kubectl apply --server-side --dry-run=server` as Argo CD's controller (its real ClusterRole
  when the tree has T12-f's, the chart's wildcard role otherwise: the summary says which);
  `status.typeChecking` of every policy; the fixtures; claims a, b, c, d and h.
- `--mode full` (`make admission-e2e REPO=<path>`, the manual workflow `admission-e2e`, about
  25 minutes): the CI part, then cert-manager, Argo CD itself and the RabbitMQ operators and
  broker synced by it from the tree, the policies first in Audit, then in Deny with a manual
  sync of every app, the hooks, `terminate-op`, the broker's `rollout restart` (D98), the
  operators' logs and `kubectl auth can-i`, claims i, k and m.

The fixtures (`admission-e2e/fixtures`): `generated/` is **the harness's own output** (one
refused CREATE per distinct refusal of `admission-celtest`, written by `CELTEST_EMIT=<dir>
admission-celtest/run.sh`; CI fails when it differs from the committed files, so regenerate
and commit after a policy change), `manual/` holds the writes that are no CREATE (DELETEs,
patches) and the requests that must pass (the controller's status write, `terminate-op`, a
plain sync, pause, hard refresh), `claims/` and `baseline/` the rest. Each fixture says who
asks (`as:`), which binding must refuse and with which message; in Audit the same fixture
must pass with that message as a warning. See OPERATIONS → "Admission policies: the kind run"
for how to run it against a PR's tree and what a failure means.

Bump `version` in `charts/admission-policy/Chart.yaml` with every template or values change (CI's `admission` job fails a pull request that does not), or the
Helm provider does not upgrade the release.

**Break-glass (you only).** Bindings are exempt from admission, so this always works:
`kubectl patch validatingadmissionpolicybinding <name> --type=merge -p
'{"spec":{"validationActions":["Warn","Audit"]}}'` (or delete the binding). Restore, or plan
it ahead, with the release: `terraform apply -target=helm_release.admission_policy
[-var='admission_break_glass=true']` (after the break-glass, remove the variable and apply
again: the `break-glass` label is how a forgotten one is found); a patch is invisible to Helm, so restore a patched
binding with `-replace='helm_release.admission_policy[0]'` (quoted, because zsh globs the
brackets; the release has `count`, so the instance key is needed, and a replace uninstalls
every policy for a moment). A release stuck in `pending-upgrade`:
`helm -n kube-system history admission-policy`, then `helm -n kube-system rollback
admission-policy <last deployed revision>`. The full runbook comes with the later tickets.

## Cost

Roughly $350–370 per month in eu-central-1 before data transfer: control plane ~$73,
3 × t3.large ~$210, NAT gateway ~$38, NLB ~$16, three public IPv4 addresses ~$11, Route 53
zone $0.50, plus EBS volumes and CloudWatch logs. Vault's two KMS keys add $2 a month,
rising to $4 after the first yearly rotation and $6 after the second (AWS bills each new
key version for two rotations, then stops); its requests stay in the free 20,000 a month,
and the snapshot bucket (four objects of well under 1 MB a day, 30 days) costs cents.
Graviton nodes (`t4g.medium`,
`AL2023_ARM_64_STANDARD`; the RYFT images are multi-arch) and Spot capacity
(`node_group.capacity_type = "SPOT"`) cut the node share.

Pod capacity: a t3.large takes 35 pods (three ENIs of 12 addresses) and has 8 GiB, so
three nodes hold 105. RYFT, the add-ons and DaemonSets and the whole Grafana Cloud stack
(two collectors, node-exporter, Kepler, OpenCost, k8s-manifest-tail) use about 55,
which leaves the HPA maximums and a node rotation's surge room (D64). On 2026-10-19 two,
then three t3.medium (17 pods each) ran out of slots (D57).

**Changing node sizes:** the EKS module ignores `desired_size` after creation. Run
`aws eks update-nodegroup-config --scaling-config minSize=…,maxSize=…,desiredSize=…`,
then set the same numbers in `terraform.tfvars`. **Any change to the node group's tags or
launch template replaces every node** (20–40 minutes). PDBs let pods that are not ready
be evicted, so a drain never waits on a broken rollout.

## Destroying

Kubernetes creates AWS resources Terraform does not track: the NLB (from Traefik's
Service), Route 53 records (ExternalDNS) and EBS volumes (PVCs). Tear down in reverse so
the controllers are still running to clean up after themselves. **Each step only after the
previous one succeeded.** If the cluster root is destroyed while Traefik's NLB still exists,
the LB controller goes with EKS, the subnet and internet gateway deletes stall for about 20
minutes on `DependencyViolation`, and the orphaned NLB keeps billing.

**Step 0, once Vault runs in the cluster** (`infra/aws/kubernetes/vault.tf`): `infra/aws/kubernetes`
refuses its destroy, because the `vault` namespace and both Vault releases carry
`prevent_destroy`.
1. Take a final Raft snapshot and check that it is in the bucket (OPERATIONS → Vault →
   Snapshots).
2. Merge a reviewed pull request that removes exactly the three `prevent_destroy = true`
   lines in `infra/aws/kubernetes/vault.tf`, and apply it (the plan shows no change).

Then:

```bash
# 1. Workloads, then the add-ons; && stops at the first failure
kubectl --context ryft-prod-euc1 delete -k deploy/k8s/overlays/eks && \
  terraform -chdir=infra/aws/kubernetes destroy   # Ingress gone → ExternalDNS deletes api.ryftapp.de; Traefik gone → the LB controller deletes the NLB
# check the NLB is gone before going on (empty output):
aws elbv2 describe-load-balancers --query "LoadBalancers[?contains(LoadBalancerName, 'traefik')].LoadBalancerArn" --output text

# 2. gp3-encrypted retains volumes: delete the leftovers explicitly. The EBS CSI driver tags
#    each volume with its PVC's namespace (kubernetes.io/created-for/pvc/namespace); the
#    workloads run in ryft-* namespaces
aws ec2 describe-volumes --filters 'Name=tag:kubernetes.io/created-for/pvc/namespace,Values=ryft-*' \
  --query 'Volumes[].VolumeId' --output text   # then: aws ec2 delete-volume --volume-id …
# the vault namespace's volumes too (Raft data and audit): they hold Vault's seal-wrapped root
# key, and the unseal key outlives the cluster, so a leftover volume stays readable
aws ec2 describe-volumes --filters 'Name=tag:kubernetes.io/created-for/pvc/namespace,Values=vault' \
  --query 'Volumes[].VolumeId' --output text   # then: aws ec2 delete-volume --volume-id …

# 3. The cluster root, only after 1 succeeded
terraform -chdir=infra/aws apply -var deletion_protection=false && \
  terraform -chdir=infra/aws destroy -target=module.eks -target=module.network   # Vault's keys and bucket stay
```

The zone in `infra/aws/dns` stays; it costs $0.50 a month and keeps the delegation intact.

**Vault's keys and bucket stay too.** They carry `prevent_destroy`, so a plain
`terraform destroy` of this root stops with "Instance cannot be destroyed"; the targets
above leave them alone. `infra/aws/kubernetes` finds them again by name, and a Vault in a
rebuilt cluster unseals with the same key and can restore the old snapshots.

`prevent_destroy` guards only while its block is in the file. **Deleting the block, deleting
`vault.tf` or reverting the pull request that added them removes the protection with it**,
and the next plan destroys them. So, once applied, never revert that pull request. What
holds then is the keys' own policy: `NoDeletionOrDisable` denies `kms:ScheduleKeyDeletion`
and `kms:DisableKey` to everyone, the account root and Terraform included. Such an apply
fails at the keys. Before that, it has already done this damage:
- destroyed both **aliases** (`kms:DeleteAlias` is not denied). Every `infra/aws/kubernetes`
  plan then fails on `data.aws_kms_alias` until they exist again;
- destroyed the bucket's policy, public access block, encryption default and lifecycle,
  and suspended its versioning. The bucket itself then fails with `BucketNotEmpty`.

Restoring the blocks and applying re-creates the aliases and the bucket settings; the keys
are still in the state. Read every `-` line of a plan here.

To keep a resource in AWS but drop it from this root, replace its block with a `removed`
block: `removed { from = aws_kms_key.vault_unseal  lifecycle { destroy = false } }`
(one per resource; the plan shows "will no longer be managed", no destroy).

Removing them for good is deliberate and takes separate, reviewed steps:

1. A pull request drops `NoDeletionOrDisable` from `vault_keys` (and, for the bucket,
   sets `force_destroy = true`). You apply it: only the key policies and the bucket's
   `force_destroy` change. `force_destroy` must be in the state before the bucket is
   destroyed, so it needs this apply of its own. From then on, deleting the bucket
   deletes **every object version at once**: there is no window for snapshots.
2. A second pull request deletes the resources (or `vault.tf`). You plan, read the
   destroy lines, and apply. A KMS key is not deleted at once: it is scheduled for
   deletion in 30 days and unusable from that moment. Within the window,
   `aws kms cancel-key-deletion --key-id <arn>` and then `aws kms enable-key --key-id
   <arn>` bring it back (a cancelled key returns disabled).

**Incident kill switch.** To stop all use of a key at once:
1. Remove `NoDeletionOrDisable` from that key's policy by hand (`aws kms put-key-policy`).
2. Run `aws kms disable-key --key-id <arn>`.
3. **Commit `is_enabled = false` on that `aws_kms_key` at once**, in a reviewed pull request.
   `is_enabled` defaults to `true`, so without it the next unrelated apply of this root
   calls `EnableKey` (nothing denies that) and silently re-enables a compromised key.

The same plan shows the key's `policy` going back to the committed one, with
`NoDeletionOrDisable`. That is harmless: the key is already disabled, and keeping it disabled
needs no `DisableKey` call. Before you apply it, check that the plan does **not** show
`is_enabled: false -> true` on any key. A kill switch done only in code fails at
`DisableKey` by design: step 1 has to come first.

Disabling the unseal key keeps every Vault pod that restarts sealed. Disabling the
snapshot key stops every upload and every restore.

Without the unseal key, Vault's data and every snapshot are unreadable for good: only
schedule its deletion after the in-cluster Vault and its snapshots are gone.

## Checks

```bash
for d in . dns kubernetes; do (cd $d && terraform fmt -check -recursive && terraform validate && terraform test); done
tflint --init && tflint --recursive --config "$PWD/.tflint.hcl"
trivy config --tf-vars terraform.tfvars.example .
```

`terraform test` mocks every provider; nothing touches AWS or the cluster. trivy reports
five findings in the cluster root that are deliberate:

| Finding | Why it stays |
| --- | --- |
| AWS-0089 Vault snapshot bucket has no access logging | Server access logs would need a second bucket. CloudTrail records only the bucket's management events (policy, encryption, versioning changes): `GetObject`, `PutObject` and `DeleteObjectVersion` are data events, not recorded without an S3 data-event trail. Reads show up only indirectly, as `Decrypt` events on the snapshot key (with the S3 bucket key, not one per read). The bucket policy refuses every put that does not name that key; the snapshot job's role (PR 4 of the Vault work) is to get put only. A data-event trail for this bucket is the upgrade if object access must be audited |
| AWS-0040/0041 public API endpoint | `kubectl` from your machine; restricted to `endpoint_public_access_cidrs` (set it to your IP) |
| AWS-0104 node egress to 0.0.0.0/0 | Nodes pull images and call AWS APIs |
| AWS-0038 scheduler / controller-manager logs off | Audit, api and authenticator logs are on; the other two add CloudWatch cost for little value at this size |
