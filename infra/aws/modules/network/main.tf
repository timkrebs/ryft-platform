# Local zones are not supported by EKS managed node groups.
data "aws_availability_zones" "available" {
  filter {
    name   = "opt-in-status"
    values = ["opt-in-not-required"]
  }
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, var.az_count)

  # /16 → three /20 private subnets (nodes and pods: the VPC CNI gives every pod
  # a VPC address) and three /24 public subnets (load balancers, NAT) from the
  # top of the range.
  private_subnets = [for k, _ in local.azs : cidrsubnet(var.cidr, 4, k)]
  public_subnets  = [for k, _ in local.azs : cidrsubnet(var.cidr, 8, k + 48)]
}

module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 6.7"

  name = var.name
  cidr = var.cidr
  azs  = local.azs

  private_subnets = local.private_subnets
  public_subnets  = local.public_subnets

  enable_nat_gateway   = true
  single_nat_gateway   = var.single_nat_gateway
  enable_dns_hostnames = true

  # VPC flow logs to CloudWatch, for auditing who talked to what.
  enable_flow_log                                 = var.flow_log_retention_in_days > 0
  create_flow_log_cloudwatch_log_group            = var.flow_log_retention_in_days > 0
  create_flow_log_cloudwatch_iam_role             = var.flow_log_retention_in_days > 0
  flow_log_cloudwatch_log_group_retention_in_days = var.flow_log_retention_in_days
  flow_log_max_aggregation_interval               = 600

  # Subnet discovery for Kubernetes load balancers.
  public_subnet_tags = {
    "kubernetes.io/role/elb" = 1
  }
  private_subnet_tags = {
    "kubernetes.io/role/internal-elb" = 1
  }

  tags = var.tags
}
