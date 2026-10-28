# RYFT on AWS (EKS)

Terraform for running the RYFT platform (`deploy/k8s`) on EKS behind
`https://api.ryftapp.de`. Three root modules with separate state, applied in order:

| Root | What | Lifetime |
| --- | --- | --- |
| `infra/aws/dns` | Route 53 zone `ryftapp.de`, CAA (Let's Encrypt only) | long-lived; survives cluster rebuilds |
| `infra/aws` | VPC + EKS cluster `ryft-prod-euc1`, node group, core add-ons | the cluster |
| `infra/aws/kubernetes` | in-cluster edge: AWS Load Balancer Controller, Traefik (NLB, HTTPS only), cert-manager + Let's Encrypt issuers, ExternalDNS, encrypted default StorageClass | the cluster's add-ons |

The zone has its own root so destroying the cluster never changes the nameservers at
GoDaddy. The add-ons have their own root because the Helm/Kubernetes providers must not
be configured from a cluster created in the same apply.

```
infra/aws/
├── terraform.tf providers.tf locals.tf main.tf variables.tf outputs.tf
├── terraform.tfvars.example   copy to terraform.tfvars (git-ignored)
├── modules/
│   ├── network/               VPC, subnets, NAT, flow logs   (terraform-aws-modules/vpc v6)
│   └── eks/                   cluster, node group, add-ons, EBS CSI Pod Identity role
│                                                             (terraform-aws-modules/eks v21)
├── tests/ryft.tftest.hcl      terraform test, providers mocked
├── dns/                       root: zone + CAA                     (tests/dns.tftest.hcl)
└── kubernetes/                root: Pod Identity roles, Helm releases, StorageClass
    ├── charts/cluster-issuers local chart: letsencrypt-staging / letsencrypt-prod (DNS-01)
    ├── charts/admission-policy local chart: ValidatingAdmissionPolicies P1-P11 + bindings (D120)
    ├── admission.tf admission-values.yaml   the release (off), its committed values, the checks
    └── tests/addons.tftest.hcl tests/admission.tftest.hcl
```

## What it creates

| Resource | Name / value |
| --- | --- |
| Hosted zone | `ryftapp.de`, `prevent_destroy`; CAA `issue`/`issuewild` `letsencrypt.org` |
| VPC | `ryft-prod-euc1`, `10.0.0.0/16`, 3 AZs: private `/20` subnets for nodes and pods, public `/24` subnets for load balancers, one NAT gateway, flow logs (30 days) |
| EKS cluster | `ryft-prod-euc1`, Kubernetes 1.36, public API endpoint restricted to `endpoint_public_access_cidrs`, secrets encrypted with KMS, audit/api/authenticator logs |
| Node group | `ryft-prod-euc1-default-…`: 3 × t3.large (AL2023), one per zone |
| EKS add-ons | `vpc-cni` (NetworkPolicy enforcement on), `kube-proxy`, `coredns`, `eks-pod-identity-agent`, `aws-ebs-csi-driver`, `metrics-server` |
| Edge (Helm) | `aws-load-balancer-controller` 3.6.0, `traefik` 42.0.0 in `kube-system` behind an internet-facing NLB, port 443 only; `cert-manager` v1.21.2 with ClusterIssuers `letsencrypt-staging`/`-prod` (DNS-01 in Route 53); `external-dns` 1.23.0 (zone `ryftapp.de` only, TXT ownership) |
| Argo CD (Helm) | `argo-cd` 10.10.5 in `argocd`, non-HA, no SSO. Its ClusterRoles are **narrowed** (T12-f, D120): the controller has only the kinds that `deploy/argocd/apps` renders, no `secrets`, no `pods/exec`, no `pods/ephemeralcontainers`, nothing on `ValidatingAdmissionPolicy`/`-Binding`, `escalate`/`bind` only by name; the server has nothing on `admissionregistration.k8s.io`; `resource.respectRBAC: strict`; the ApplicationSet controller is at 0 replicas. The rules and their reasons are in `docs/OPERATIONS.md` (Deploy and roll back). A new kind in a synced path needs its rule in `kubernetes/argocd.tf` first |
| StorageClass | `gp3-encrypted` (default): gp3, encrypted, `WaitForFirstConsumer`, `Retain` |
| IAM | cluster/node roles; Pod Identity roles for EBS CSI, the LB controller, ExternalDNS and cert-manager (the last two scoped to the zone) |

Request path: app → `api.ryftapp.de` (Route 53 alias, written by ExternalDNS) → NLB →
Traefik (Let's Encrypt certificate, TLS) → `/v1/sync` ingest pods, `/v1` api pods.

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

`infra/aws/kubernetes/admission.tf` installs eleven built-in ValidatingAdmissionPolicies
(P1–P11) and twelve bindings (P2 has one for templates and one for Pods) from the local
chart `charts/admission-policy`, as the release `admission-policy` in `kube-system`. They
judge what PSA cannot: images per namespace, commands and service account tokens, the
auth-mode keys, NetworkPolicy and Traefik shapes, the Vault Secrets Operator objects, the
RabbitMQ operators' cluster power and reach, the broker, and Argo CD's own objects. No pods
run for them and no webhook can be down; the cost is nil (API server CPU only).

**It ships off.** `admission-values.yaml` holds `enabled: false`, so a plan creates nothing.
It also holds the Vault address P4 pins (a test compares it with the generated
`deploy/k8s/overlays/vault-namespaces/*/vault.yaml`), Argo CD's service account names and
the action of each binding, `audit` or `deny`:

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
for the T12-g precondition).

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
- `breakGlass`, `actions`, `vpcCIDR`, `vaultAddress`: set from the file and the variables.

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
ordering and the apiserver-dependent claims (a–d, f–m of the design) are T12-d's kind job.
It is not in CI yet.

Bump `version` in `charts/admission-policy/Chart.yaml` with every template change, or the
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
zone $0.50, plus EBS volumes and CloudWatch logs. Graviton nodes (`t4g.medium`,
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
the controllers are still running to clean up after themselves:

```bash
kubectl delete -k deploy/k8s/overlays/eks       # Ingress gone → ExternalDNS deletes api.ryftapp.de
(cd infra/aws/kubernetes && terraform destroy)  # Traefik Service gone → LB controller deletes the NLB
# gp3-encrypted retains volumes: delete the leftovers explicitly
aws ec2 describe-volumes --filters Name=tag:kubernetes.io/created-for/pvc/namespace,Values=ryft \
  --query 'Volumes[].VolumeId' --output text   # then: aws ec2 delete-volume --volume-id …
(cd infra/aws && terraform apply -var deletion_protection=false && terraform destroy)
```

The zone in `infra/aws/dns` stays; it costs $0.50 a month and keeps the delegation intact.

## Checks

```bash
for d in . dns kubernetes; do (cd $d && terraform fmt -check -recursive && terraform validate && terraform test); done
tflint --init && tflint --recursive --config "$PWD/.tflint.hcl"
trivy config --tf-vars terraform.tfvars.example .
```

`terraform test` mocks every provider; nothing touches AWS or the cluster. trivy reports
four findings in the cluster root that are deliberate:

| Finding | Why it stays |
| --- | --- |
| AWS-0040/0041 public API endpoint | `kubectl` from your machine; restricted to `endpoint_public_access_cidrs` (set it to your IP) |
| AWS-0104 node egress to 0.0.0.0/0 | Nodes pull images and call AWS APIs |
| AWS-0038 scheduler / controller-manager logs off | Audit, api and authenticator logs are on; the other two add CloudWatch cost for little value at this size |
