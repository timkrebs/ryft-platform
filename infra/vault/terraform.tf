terraform {
  required_version = "~> 1.10"

  required_providers {
    vault = {
      source  = "hashicorp/vault"
      version = "~> 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.38"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
  }

  # Remote state in the bucket from infra/aws/state (docs/OPERATIONS.md → Terraform
  # state). Bucket and KMS key come from the git-ignored infra/backend.hcl:
  #   terraform init -backend-config=$(git rev-parse --show-toplevel)/infra/backend.hcl
  # This state holds every credential the root writes into Vault (generated
  # passwords, the owner password, tokens) in plaintext; the bucket is private,
  # TLS-only and KMS-encrypted, so reading it needs both S3 and KMS permissions.
  backend "s3" {
    key          = "vault.tfstate"
    region       = "eu-central-1"
    encrypt      = true
    use_lockfile = true
  }
}
