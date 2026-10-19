terraform {
  required_version = "1.16.6"

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


  # State in HCP Terraform (docs/OPERATIONS.md → Terraform state): stored, locked
  # and versioned there; plans and applies run locally (execution mode Local).
  # One workspace per root; this one holds the edge add-ons on the cluster.
  cloud {
    organization = "tim-krebs-org"

    workspaces {
      name = "ryft-prod-euc1-kubernetes"
    }
  }
}
