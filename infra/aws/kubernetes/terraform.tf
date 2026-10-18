terraform {
  required_version = "~> 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.59"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "~> 3.3"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 3.3"
    }
  }


  # Remote state in the bucket from infra/aws/state (docs/OPERATIONS.md → Terraform
  # state). Bucket and KMS key come from the git-ignored infra/backend.hcl:
  #   terraform init -backend-config=$(git rev-parse --show-toplevel)/infra/backend.hcl
  backend "s3" {
    key          = "aws/kubernetes.tfstate"
    region       = "eu-central-1"
    encrypt      = true
    use_lockfile = true
  }
}
