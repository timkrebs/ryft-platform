# GitHub Actions -> ryft-deployer without a key (D223). The condition pins IDs, not names, because a
# renamed or re-created repository or owner gets new IDs (design 5.1). It admits exactly
# deploy.yml on main, in the environment `deploy` or `site`, run by a push or by hand
# (workflow_dispatch: runbook steps 8 and 10 dispatch it; ticket B, design fix), on a GitHub-hosted
# runner: a self-hosted runner registered to the repository later gets no token from this pool.
#
# One environment per deploy account (D233, the split after C1). `deploy.yml`'s `deploy` job runs in
# `deploy` and may become ryft-deployer; its `site` job runs in `site` and may become only
# site-deployer in ryftlab-site (bound by hand, runbook step 10a, to the `site` set below). So code in
# the site job (firebase-tools and the tree npx resolves) can't act as the services' deployer. The
# environment is the token's own claim, mapped to attribute.environment, and GitHub sets it from the
# job's `environment:`, whose deployment branch rule admits main only.
locals {
  github_environments = ["deploy", "site"]

  github_condition = join(" && ", [
    "assertion.repository_id == '${var.github_repository_id}'",
    "assertion.repository_owner_id == '${var.github_repository_owner_id}'",
    "assertion.ref == 'refs/heads/main'",
    "assertion.event_name in ['push', 'workflow_dispatch']",
    "assertion.environment in [${join(", ", [for e in local.github_environments : "'${e}'"])}]",
    "assertion.job_workflow_ref == '${local.github_workflow_ref}'",
    "assertion.runner_environment == 'github-hosted'",
  ])

  github_pool_name = "projects/${local.project_number}/locations/global/workloadIdentityPools/${google_iam_workload_identity_pool.github.workload_identity_pool_id}"

  # The principals that passed the condition, by environment: `deploy` for ryft-deployer, `site` for
  # site-deployer. Never the repository's whole set (attribute.repository_id), which held both jobs.
  github_principal_set = {
    for e in local.github_environments : e => "principalSet://iam.googleapis.com/${local.github_pool_name}/attribute.environment/${e}"
  }
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
  description                        = "Only deploy.yml on main, environments deploy and site, GitHub-hosted, by repository and owner ID"

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

# The deploy job's environment only. The member moved from the repository's set (D223) to the
# environment's (D233): a new member is a new binding, and create_before_destroy makes it before the
# old one goes, so no apply leaves ryft-deployer without one. Both sets admit the deploy job's token.
resource "google_service_account_iam_member" "deployer_workload_identity" {
  service_account_id = local.sa_id["ryft-deployer"]
  role               = "roles/iam.workloadIdentityUser"
  member             = local.github_principal_set["deploy"]

  lifecycle {
    create_before_destroy = true
  }
}
