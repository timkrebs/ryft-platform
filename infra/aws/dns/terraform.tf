terraform {
  required_version = "1.16.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.59"
    }
  }


  # State in HCP Terraform (docs/OPERATIONS.md → Terraform state): stored, locked
  # and versioned there; plans and applies run locally (execution mode Local).
  # One workspace per root; this one holds the ryftapp.de zone, which outlives clusters.
  cloud {
    organization = "tim-krebs-org"

    workspaces {
      name = "ryft-prod-euc1-dns"
    }
  }
}
