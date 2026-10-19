terraform {
  required_version = "1.16.6"

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

  # State in HCP Terraform (docs/OPERATIONS.md → Terraform state): stored, locked
  # and versioned there; plans and applies run locally (execution mode Local).
  # One workspace per root; this one holds Vault namespaces, auth and secrets.
  # This state holds every credential the root writes into Vault (generated
  # passwords, the owner password, tokens): workspace access is secret access.
  cloud {
    organization = "tim-krebs-org"

    workspaces {
      name = "ryft-prod-euc1-vault"
    }
  }
}
