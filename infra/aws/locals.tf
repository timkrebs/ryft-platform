data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  # Every name derives from this: cluster "ryft-prod", VPC "ryft-prod",
  # node group "ryft-prod-default", IAM roles "ryft-prod-euc1-…".
  name = "${var.project}-${var.environment}-${var.location}"

  account_id = data.aws_caller_identity.current.account_id
  partition  = data.aws_partition.current.partition

  tags = merge(var.tags, {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}
