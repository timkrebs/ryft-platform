terraform {
  required_version = "1.16.6"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 8.11"
    }
    # Only for google_project_service_identity (project.tf), which the GA provider does not have.
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 8.11"
    }
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.55"
    }
  }

  # State and runs in HCP Terraform, org ryft-lab (D216, D223): CLI-driven, remote execution. The
  # workspace signs in to GCP with dynamic credentials (docs/OPERATIONS.md -> GCP, step 2), so no
  # credential appears in this root. CI only ever runs `init -backend=false`.
  cloud {
    organization = "ryft-lab"

    workspaces {
      name = "ryft-gcp"
    }
  }
}
