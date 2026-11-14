# One account per service and job (D222), the push identity, CI's deployer and Grafana's reader. There
# is no google_service_account_key anywhere in this root, and there never will be: a key resource puts
# the private key into the state (tests/guards.tftest.hcl refuses one).
resource "google_service_account" "this" {
  for_each = local.accounts

  project      = var.project_id
  account_id   = each.key
  display_name = each.value
}

# Pub/Sub mints the OIDC token of every push and tick as ryft-push (design 2.2).
resource "google_service_account_iam_member" "pubsub_agent_token_creator" {
  service_account_id = local.sa_id["ryft-push"]
  role               = "roles/iam.serviceAccountTokenCreator"
  member             = local.pubsub_agent

  depends_on = [google_project_service_identity.agent]
}

# ---- ryft-deployer: deploy.yml on main, through the GitHub pool (wif.tf) ---------------------------
# It deploys the services and jobs that CI itself creates, so run.developer is project-wide: a binding
# on a service cannot exist before the service does (ticket B, design fix). run.developer holds no
# setIamPolicy, so the deployer can neither make scoring public nor grant itself anything. It may act
# as the five runtime accounts only (never ryft-push, never grafana-reader), and push to one repository
# (registry.tf). No IAM-admin role of any kind (tests/iam.tftest.hcl).
resource "google_project_iam_member" "deployer_run_developer" {
  project = var.project_id
  role    = "roles/run.developer"
  member  = local.member["ryft-deployer"]
}

resource "google_service_account_iam_member" "deployer_act_as" {
  for_each = local.runtime_accounts

  service_account_id = local.sa_id[each.key]
  role               = "roles/iam.serviceAccountUser"
  member             = local.member["ryft-deployer"]
}

# ---- grafana-reader: metrics only (no data, no logs), for Grafana Cloud's data source (D224) ------
resource "google_project_iam_member" "grafana_reader" {
  project = var.project_id
  role    = "roles/monitoring.viewer"
  member  = local.member["grafana-reader"]
}
