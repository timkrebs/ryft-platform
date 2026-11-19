terraform {
  required_version = "1.16.7"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.59"
    }
  }

  # State in HCP Terraform (docs/OPERATIONS.md → Terraform state): stored, locked
  # and versioned there; plans and applies run locally (execution mode Local).
  # The retirement (PR D2): this root declares nothing any more, so the next plan
  # against this workspace destroys everything its state holds: Vault's keys (scheduled,
  # 30 days), their aliases, the snapshot bucket and the budget (OPERATIONS → GCP 13.2).
  # tests/empty.tftest.hcl pins this block: a destroy against another workspace must not
  # be one edit away. PR D3 deletes the directory after step 14.
  cloud {
    organization = "tim-krebs-org"

    workspaces {
      name = "ryft-prod-euc1-platform"
    }
  }
}
