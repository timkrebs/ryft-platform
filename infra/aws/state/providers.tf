provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = "ryft"
      ManagedBy = "terraform"
      Root      = "infra/aws/state"
    }
  }
}
