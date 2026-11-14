# GitHub Actions -> ryft-deployer without a key (D223). The condition pins IDs, not names, because a
# renamed or re-created repository or owner gets new IDs (design 5.1). It admits exactly
# deploy.yml on main, in the environment `deploy`, run by a push or by hand (workflow_dispatch:
# runbook steps 8 and 10 dispatch it; ticket B, design fix), on a GitHub-hosted runner: a self-hosted
# runner registered to the repository later gets no token from this pool. The site job (C1) runs in
# the same workflow and environment, so the site's deploy SA in ryftlab-site binds this pool too
# (step 10).
locals {
  github_condition = join(" && ", [
    "assertion.repository_id == '${var.github_repository_id}'",
    "assertion.repository_owner_id == '${var.github_repository_owner_id}'",
    "assertion.ref == 'refs/heads/main'",
    "assertion.event_name in ['push', 'workflow_dispatch']",
    "assertion.environment == 'deploy'",
    "assertion.job_workflow_ref == '${local.github_workflow_ref}'",
    "assertion.runner_environment == 'github-hosted'",
  ])

  github_pool_name = "projects/${local.project_number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github.workload_identity_pool_id}"

  # Every principal of the repository that passed the condition (the deployer's and the site SA's binding).
  github_principal_set = "principalSet://iam.googleapis.com/${local.github_pool_name}/attribute.repository_id/${var.github_repository_id}"
}

resource "google_iam_workload_identity_pool" "github" {
  project                   = var.project_id
  workload_identity_pool_id = "github"
  display_name              = "GitHub Actions"
  description               = "deploy.yml on main of ${local.github_repository} (infra/gcp, D223)"
}

resource "google_iam_workload_identity_pool_provider" "github" {
  project                            = var.project_id
  workload_identity_pool_id          = google_iam_workload_identity_pool.github.workload_identity_pool_id
  workload_identity_pool_provider_id = "github-actions"
  display_name                       = "GitHub Actions OIDC"
  description                        = "Only deploy.yml on main, environment deploy, GitHub-hosted, by repository and owner ID"

  attribute_mapping = {
    "google.subject"                = "assertion.sub"
    "attribute.repository"          = "assertion.repository"
    "attribute.repository_id"       = "assertion.repository_id"
    "attribute.repository_owner_id" = "assertion.repository_owner_id"
    "attribute.ref"                 = "assertion.ref"
    "attribute.event_name"          = "assertion.event_name"
    "attribute.environment"         = "assertion.environment"
    "attribute.job_workflow_ref"    = "assertion.job_workflow_ref"
  }

  attribute_condition = local.github_condition

  oidc {
    issuer_uri = "https://token.actions.githubusercontent.com"
  }
}

resource "google_service_account_iam_member" "deployer_workload_identity" {
  service_account_id = local.sa_id["ryft-deployer"]
  role               = "roles/iam.workloadIdentityUser"
  member             = local.github_principal_set
}
