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

# Only the Grafana-managed rules (alerts.tf) use this provider. Until the data source exists (runbook
# step 11) grafana_datasource_uid is empty, nothing uses the provider, and it is configured with
# nothing at all: applies 1 and 2 need no Grafana token.
provider "grafana" {
  url  = local.alerts ? var.grafana_url : null
  auth = local.alerts ? var.grafana_auth : null
}
