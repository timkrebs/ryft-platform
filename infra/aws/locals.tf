locals {
  # Every name derives from this: cluster "ryft-prod", VPC "ryft-prod",
  # node group "ryft-prod-default", IAM roles "ryft-prod-…".
  name = "${var.project}-${var.environment}"

  tags = merge(var.tags, {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}
