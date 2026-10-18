# eks

EKS cluster for RYFT, wrapping
[`terraform-aws-modules/eks/aws`](https://registry.terraform.io/modules/terraform-aws-modules/eks/aws) v21.

- One managed node group (`<name>-default`, AL2023) in the given private subnets.
- Access entries: whoever runs `terraform apply` becomes cluster admin.
- Secrets encryption with a module-managed KMS key and control-plane logs
  (`audit`, `api`, `authenticator`) to CloudWatch, both module defaults.
- Managed add-ons, chosen for what `deploy/k8s` needs:

| Add-on | Why |
| --- | --- |
| `vpc-cni` with `enableNetworkPolicy` | Pod networking; enforces the NetworkPolicies in `deploy/k8s/base/networkpolicy.yaml` (otherwise silently ignored) |
| `eks-pod-identity-agent` | Pod Identity for the EBS CSI role |
| `kube-proxy`, `coredns` | Service routing and DNS |
| `aws-ebs-csi-driver` with `defaultStorageClass` | gp3 PersistentVolumes; creates the default StorageClass `ebs-csi-default-sc` the PVCs (no `storageClassName`) bind to |
| `metrics-server` | Resource metrics for the api/ingest HorizontalPodAutoscalers |

```hcl
module "eks" {
  source = "./modules/eks"

  name               = "ryft-prod"
  kubernetes_version = "1.36"
  vpc_id             = module.network.vpc_id
  subnet_ids         = module.network.private_subnet_ids

  node_group = {
    ami_type       = "AL2023_x86_64_STANDARD"
    capacity_type  = "ON_DEMAND"
    instance_types = ["t3.medium"]
    min_size       = 2
    max_size       = 4
    desired_size   = 2
  }
}
```

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | `string` | — | Cluster name and prefix for IAM roles, node group, security groups (≤ 20 chars) |
| `kubernetes_version` | `string` | — | Control-plane version, e.g. `1.36` |
| `vpc_id` | `string` | — | VPC of the cluster |
| `subnet_ids` | `list(string)` | — | Private subnets in ≥ 2 zones |
| `node_group` | `object` | — | `ami_type`, `capacity_type`, `instance_types`, `min_size`, `max_size`, `desired_size` |
| `endpoint_public_access` | `bool` | `true` | Public API endpoint |
| `endpoint_public_access_cidrs` | `list(string)` | `["0.0.0.0/0"]` | Who may reach the public endpoint |
| `deletion_protection` | `bool` | `false` | Block cluster deletion |
| `tags` | `map(string)` | `{}` | Tags for every resource |

Outputs: `cluster_name`, `cluster_endpoint`, `cluster_certificate_authority_data`,
`cluster_version`, `cluster_security_group_id`, `node_security_group_id`,
`oidc_provider_arn`, `ebs_csi_role_arn`.
