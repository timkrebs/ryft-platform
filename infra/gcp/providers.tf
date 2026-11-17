# No credentials: HCP Terraform's GCP dynamic credentials (TFC_GCP_* workspace variables, runbook
# step 2e) give the plan phase hcp-terraform-plan and the apply phase hcp-terraform-apply.
provider "google" {
  project = var.project_id
  region  = var.region

  default_labels = {
    project    = "ryft"
    managed-by = "terraform"
  }
}

# The same project and the same dynamic credentials; used by google_project_service_identity only.
provider "google-beta" {
  project = var.project_id
  region  = var.region
}
