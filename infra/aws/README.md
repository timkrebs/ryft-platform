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
    └── tests/addons.tftest.hcl
```

## What it creates

| Resource | Name / value |
| --- | --- |
| Hosted zone | `ryftapp.de`, `prevent_destroy`; CAA `issue`/`issuewild` `letsencrypt.org` |
| VPC | `ryft-prod-euc1`, `10.0.0.0/16`, 3 AZs: private `/20` subnets for nodes and pods, public `/24` subnets for load balancers, one NAT gateway, flow logs (30 days) |
| EKS cluster | `ryft-prod-euc1`, Kubernetes 1.36, public API endpoint restricted to `endpoint_public_access_cidrs`, secrets encrypted with KMS, audit/api/authenticator logs |
| Node group | `ryft-prod-euc1-default-…`: 2 × t3.medium (AL2023) |
| EKS add-ons | `vpc-cni` (NetworkPolicy enforcement on), `kube-proxy`, `coredns`, `eks-pod-identity-agent`, `aws-ebs-csi-driver`, `metrics-server` |
| Edge (Helm) | `aws-load-balancer-controller` 3.6.0, `traefik` 42.0.0 in `kube-system` behind an internet-facing NLB, port 443 only; `cert-manager` v1.21.2 with ClusterIssuers `letsencrypt-staging`/`-prod` (DNS-01 in Route 53); `external-dns` 1.23.0 (zone `ryftapp.de` only, TXT ownership) |
| StorageClass | `gp3-encrypted` (default): gp3, encrypted, `WaitForFirstConsumer`, `Retain` |
| IAM | cluster/node roles; Pod Identity roles for EBS CSI, the LB controller, ExternalDNS and cert-manager (the last two scoped to the zone) |

Request path: app → `api.ryftapp.de` (Route 53 alias, written by ExternalDNS) → NLB →
Traefik (Let's Encrypt certificate, TLS) → `/v1/sync` ingest pods, `/v1` api pods.

## Usage

Requires Terraform ≥ 1.10, AWS credentials for the target account, `aws` CLI, `kubectl`
and `dig`. Every root keeps local state (`terraform.tfstate`, git-ignored) until you
enable a backend (`terraform.tf`).

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

## Cost

Roughly $210–230 per month in eu-central-1 before data transfer: control plane ~$73,
2 × t3.medium ~$70, NAT gateway ~$38, NLB ~$16, three public IPv4 addresses ~$11, Route 53
zone $0.50, plus EBS volumes and CloudWatch logs. Graviton nodes (`t4g.medium`,
`AL2023_ARM_64_STANDARD`; the RYFT images are multi-arch) and Spot capacity
(`node_group.capacity_type = "SPOT"`) cut the node share.

Pod capacity: a t3.medium takes 17 pods, so two nodes hold 34. The add-ons use about
22, which leaves room for RYFT including its HPA maximums. Prometheus will need a third
node.

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
