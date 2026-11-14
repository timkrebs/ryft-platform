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
# run.developer follows the serve gate. Before apply 2 (serve = false) it is project-wide: CI creates
# the four services and the job at runbook step 8, and a binding on a service cannot exist before the
# service does (ticket B, design fix). From apply 2 on (serve = true) it is bound on each service and
# on the job instead, and the project-wide binding goes: the deployer can then update what exists, and
# create nothing new.
#
# So a new service or job needs the project-wide binding back for its first deploy: a PR sets the
# count below to 1 (and its pin in tests/gates.tftest.hcl), the apply adds it, and the first deploy
# creates the service; then a PR adds the name to local.services or local.jobs and puts the count
# back (README -> The deployer). Runbook step 9's fallback is that first PR's count line, and nothing
# else.
#
# run.developer holds no setIamPolicy, so the deployer can neither make scoring public nor grant
# itself anything. It may act as the five runtime accounts only (never ryft-push, never
# grafana-reader), and push to one repository (registry.tf). No IAM-admin role of any kind
# (tests/iam.tftest.hcl).
resource "google_project_iam_member" "deployer_run_developer" {
  count = var.serve ? 0 : 1

  project = var.project_id
  role    = "roles/run.developer"
  member  = local.member["ryft-deployer"]
}

resource "google_cloud_run_v2_service_iam_member" "deployer_run_developer" {
  for_each = var.serve ? local.services : toset([])

  project  = var.project_id
  location = var.region
  name     = each.key
  role     = "roles/run.developer"
  member   = local.member["ryft-deployer"]
}

resource "google_cloud_run_v2_job_iam_member" "deployer_run_developer" {
  for_each = var.serve ? local.jobs : toset([])

  project  = var.project_id
  location = var.region
  name     = each.key
  role     = "roles/run.developer"
  member   = local.member["ryft-deployer"]
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
