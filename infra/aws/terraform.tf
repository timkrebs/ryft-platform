terraform {
  required_version = "~> 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.59"
    }
  }

  # State is local (terraform.tfstate, git-ignored) until you pick a backend.
  # HCP Terraform:
  # cloud {
  #   organization = "<your-org>"
  #   workspaces {
  #     name = "ryft-aws-prod"
  #   }
  # }
  #
  # Or S3 with native locking (Terraform >= 1.10):
  # backend "s3" {
  #   bucket       = "<state-bucket>"
  #   key          = "ryft/aws/prod.tfstate"
  #   region       = "eu-central-1"
  #   use_lockfile = true
  # }
}
