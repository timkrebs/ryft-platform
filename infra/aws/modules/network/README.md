# network

VPC for the RYFT EKS cluster, wrapping
[`terraform-aws-modules/vpc/aws`](https://registry.terraform.io/modules/terraform-aws-modules/vpc/aws).

- Private and public subnets in 2–3 availability zones (local zones excluded).
- Subnets are derived from `cidr`: with a /16, three /20 private subnets (nodes and
  pods — the VPC CNI gives every pod a VPC address) and three /24 public subnets.
- NAT gateway for egress from the private subnets: one shared (default) or one per zone.
- Subnets carry the `kubernetes.io/role/elb` / `internal-elb` tags Kubernetes load
  balancers use for discovery.
- VPC flow logs to CloudWatch (10-minute aggregation), kept 30 days by default.

```hcl
module "network" {
  source = "./modules/network"

  name = "ryft-prod"
  cidr = "10.0.0.0/16"
}
```

| Input | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | `string` | — | VPC name, prefix for subnets and gateways |
| `cidr` | `string` | — | VPC CIDR, /16 to /20 |
| `az_count` | `number` | `3` | Availability zones (2 or 3) |
| `single_nat_gateway` | `bool` | `true` | One NAT gateway instead of one per zone |
| `flow_log_retention_in_days` | `number` | `30` | Flow-log retention in CloudWatch; `0` disables flow logs |
| `tags` | `map(string)` | `{}` | Tags for every resource |

Outputs: `vpc_id`, `vpc_cidr_block`, `azs`, `private_subnet_ids`,
`private_subnet_cidr_blocks`, `public_subnet_ids`, `public_subnet_cidr_blocks`.
