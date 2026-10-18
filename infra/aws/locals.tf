locals {
  # Every name derives from this: cluster "ryft-prod", VPC "ryft-prod",
  # node group "ryft-prod-default", IAM roles "ryft-prod-euc1-…".
  name = "${var.project}-${var.environment}-${var.location}"

  tags = merge(var.tags, {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}
