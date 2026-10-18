# RYFT on AWS (EKS)

Terraform for an EKS cluster that runs the RYFT platform (`deploy/k8s`): a VPC, an
EKS control plane with one managed node group, and the add-ons the manifests rely on.

```
infra/aws/
├── terraform.tf               Terraform + provider versions, backend (commented)
├── providers.tf               AWS provider
├── locals.tf                  naming (<project>-<environment>) and tags
├── main.tf                    module "network" → module "eks"
├── variables.tf / outputs.tf
├── terraform.tfvars.example   copy to terraform.tfvars (git-ignored)
├── modules/
│   ├── network/               VPC, subnets, NAT, flow logs   (terraform-aws-modules/vpc v6)
│   └── eks/                   cluster, node group, add-ons, EBS CSI Pod Identity role
│                                                             (terraform-aws-modules/eks v21)
└── tests/ryft.tftest.hcl      terraform test, providers mocked
```

## What it creates

Everything is named `<project>-<environment>`, `ryft-prod` by default:

| Resource | Name / value |
| --- | --- |
| VPC | `ryft-prod`, `10.0.0.0/16`, 3 AZs: private `/20` subnets for nodes and pods, public `/24` subnets for load balancers, one NAT gateway, flow logs (30 days) |
| EKS cluster | `ryft-prod`, Kubernetes 1.36, public API endpoint restricted to `endpoint_public_access_cidrs`, secrets encrypted with KMS, audit/api/authenticator logs |
| Node group | `ryft-prod-default-…`: 2 × t3.medium (AL2023), scales 2–4 |
| IAM | cluster, node and `ryft-prod-ebs-csi-…` roles; the identity that runs `apply` becomes cluster admin (access entry) |
| Add-ons | `vpc-cni` (NetworkPolicy enforcement on), `kube-proxy`, `coredns`, `eks-pod-identity-agent`, `aws-ebs-csi-driver` (default gp3 StorageClass), `metrics-server` |

Tags on everything: `Project`, `Environment`, `ManagedBy = terraform`, plus `tags`.

## Usage

Requires Terraform ≥ 1.10, AWS credentials for the target account, `aws` CLI and `kubectl`.

```bash
cd infra/aws
cp terraform.tfvars.example terraform.tfvars   # set endpoint_public_access_cidrs to your IP
terraform init
terraform plan -out=tfplan
terraform apply tfplan                          # ~15 minutes
$(terraform output -raw configure_kubectl)      # adds context "ryft-prod" to ~/.kube/config
kubectl get nodes
```

State is local (`terraform.tfstate`, git-ignored) until you enable the HCP Terraform or
S3 backend in `terraform.tf`. Do that before a second person or CI touches the cluster.

## Running RYFT on it

Already handled by the add-ons: PVCs bind to the default `ebs-csi-default-sc` (gp3),
the NetworkPolicies in `deploy/k8s/base` are enforced, and metrics-server feeds the
HPAs. Still needed:

1. **An ingress controller.** EKS ships none. The quickest fit for the existing
   NetworkPolicy (it admits Traefik pods in `kube-system`) is Traefik's Helm chart:
   `helm repo add traefik https://traefik.github.io/charts && helm install traefik traefik/traefik -n kube-system`.
   Its `LoadBalancer` Service gets an
   AWS load balancer in the public subnets. Point DNS at it and set the host in
   `deploy/k8s/base/ingress.yaml`. With another controller, adjust
   `ryft-allow-ingress-controller`.
2. **Prometheus** in namespace `monitoring` if you want the metrics scraped
   (`docs/OBSERVABILITY.md`).
3. **Images**: the deployments pull `ghcr.io/timkrebs/ryft/*`; make the packages public or
   add an `imagePullSecret`.
4. Then `kubectl apply -k deploy/k8s` (or `deploy/k8s/vault` with the Vault Secrets
   Operator installed) as in `docs/OPERATIONS.md`.

## Cost

Roughly $180–200 per month in eu-central-1 before data transfer: control plane ~$73,
2 × t3.medium ~$70, NAT gateway ~$38, plus EBS volumes and CloudWatch logs. Graviton
nodes (`t4g.medium`, `AL2023_ARM_64_STANDARD`; the RYFT images are multi-arch) and Spot
capacity (`node_group.capacity_type = "SPOT"`) cut the node share.

## Destroying

Kubernetes creates AWS resources Terraform does not know about: EBS volumes for the PVCs
and the ingress controller's load balancer. Remove them first, or `destroy` leaves
orphaned volumes and fails to delete the VPC:

```bash
kubectl delete -k deploy/k8s                        # PVCs → EBS volumes deleted
helm uninstall traefik -n kube-system               # load balancer deleted
terraform apply -var deletion_protection=false      # if you enabled it
terraform destroy
```

## Checks

```bash
terraform fmt -check -recursive
terraform validate
terraform test                      # mocked providers, no AWS calls
tflint --init && tflint --recursive --config "$PWD/.tflint.hcl"
trivy config --tf-vars terraform.tfvars.example .
```

trivy reports four findings that are deliberate:

| Finding | Why it stays |
| --- | --- |
| AWS-0040/0041 public API endpoint | `kubectl` from your machine; restricted to `endpoint_public_access_cidrs` (set it to your IP) |
| AWS-0104 node egress to 0.0.0.0/0 | Nodes pull images and call AWS APIs |
| AWS-0038 scheduler / controller-manager logs off | Audit, api and authenticator logs are on; the other two add CloudWatch cost for little value at this size |
