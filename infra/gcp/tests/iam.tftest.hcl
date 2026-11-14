# Who may do what: the accounts, the secret accessors, the deployer, the GitHub pool. Plan-only, mocked.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}
mock_provider "grafana" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
}

run "the_accounts" {
  command = plan

  assert {
    condition = toset(keys(google_service_account.this)) == toset([
      "ryft-api", "ryft-ingest", "ryft-scoring", "ryft-notify", "ryft-migrate", "ryft-push", "ryft-deployer", "grafana-reader",
    ])
    error_message = "five runtime accounts, ryft-push, ryft-deployer and grafana-reader, nothing else"
  }

  assert {
    condition     = alltrue([for a in google_service_account.this : a.project == "ryftlab-prod"])
    error_message = "every account lives in ryftlab-prod"
  }
}

# Ticket B's table, exactly: each secret's accessors, bound on the secret.
run "secret_accessors_exactly" {
  command = plan

  assert {
    condition = toset(keys(google_secret_manager_secret.this)) == toset([
      "db-password-api", "db-password-ingest", "db-password-scoring", "db-password-notify", "db-password-owner",
      "supabase-secret-key", "admin-api-key", "apple-key-p8", "apns-key-p8", "data-key", "otlp-token",
    ])
    error_message = "the 11 secret containers of ticket B"
  }

  assert {
    condition = alltrue([
      for k, b in google_secret_manager_secret_iam_binding.accessor :
      b.role == "roles/secretmanager.secretAccessor" && b.secret_id == k
    ])
    error_message = "one accessor binding per secret, on that secret"
  }

  assert {
    condition = alltrue([
      for s in ["api", "ingest", "scoring", "notify"] :
      google_secret_manager_secret_iam_binding.accessor["db-password-${s}"].members == toset([
        "serviceAccount:ryft-${s}@ryftlab-prod.iam.gserviceaccount.com",
        "serviceAccount:ryft-migrate@ryftlab-prod.iam.gserviceaccount.com",
      ])
    ])
    error_message = "a role's password: that service and migrate"
  }

  assert {
    condition     = google_secret_manager_secret_iam_binding.accessor["db-password-owner"].members == toset(["serviceAccount:ryft-migrate@ryftlab-prod.iam.gserviceaccount.com"])
    error_message = "the owner's password: migrate only"
  }

  assert {
    condition = alltrue([
      for s in ["supabase-secret-key", "admin-api-key", "apple-key-p8", "data-key"] :
      google_secret_manager_secret_iam_binding.accessor[s].members == toset(["serviceAccount:ryft-api@ryftlab-prod.iam.gserviceaccount.com"])
    ])
    error_message = "the Supabase secret key, ADMIN_API_KEY, the Apple key and the data key: api only"
  }

  assert {
    condition     = google_secret_manager_secret_iam_binding.accessor["apns-key-p8"].members == toset(["serviceAccount:ryft-notify@ryftlab-prod.iam.gserviceaccount.com"])
    error_message = "the APNs key: notify only"
  }

  assert {
    condition = google_secret_manager_secret_iam_binding.accessor["otlp-token"].members == toset([
      "serviceAccount:ryft-api@ryftlab-prod.iam.gserviceaccount.com",
      "serviceAccount:ryft-ingest@ryftlab-prod.iam.gserviceaccount.com",
      "serviceAccount:ryft-scoring@ryftlab-prod.iam.gserviceaccount.com",
      "serviceAccount:ryft-notify@ryftlab-prod.iam.gserviceaccount.com",
    ])
    error_message = "the OTLP token: the four services, not migrate"
  }

  assert {
    condition = alltrue([
      for s in google_secret_manager_secret.this :
      s.version_destroy_ttl == "2592000s" &&
      toset(flatten([for rep in s.replication : [for um in rep.user_managed : [for r in um.replicas : r.location]]])) == toset(["europe-west1"]) &&
      length(flatten([for rep in s.replication : [for um in rep.user_managed : um.replicas]])) == 1
    ])
    error_message = "every secret: delayed destruction of 30 days, one replica in europe-west1"
  }

  # Never at project level: the role appears once in the whole root (the per-secret binding), and no
  # authoritative project IAM resource exists (it would also wipe the bootstrap's bindings).
  assert {
    condition = (
      length(regexall("secretmanager\\.secretAccessor", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      length(regexall("resource \"google_project_iam_(binding|policy)\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0 &&
      length(regexall("google_secret_manager_secret_iam_(member|policy)", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    )
    error_message = "secretAccessor only on each secret, authoritatively; no project-level grant, no project IAM binding or policy"
  }

  assert {
    condition = alltrue(concat(
      [for b in google_project_iam_member.deployer_run_developer : b.role != "roles/secretmanager.secretAccessor"],
      [google_project_iam_member.grafana_reader.role != "roles/secretmanager.secretAccessor"],
    ))
    error_message = "no project-level secretAccessor"
  }
}

# Every secret read and every token mint is logged: Secret Manager, the token exchange (STS) and the
# IAM credentials API (the deployer's access token after the exchange).
run "data_access_audit_logs" {
  command = plan

  assert {
    condition = (
      toset(keys(google_project_iam_audit_config.data_read)) == toset([
        "secretmanager.googleapis.com", "sts.googleapis.com", "iamcredentials.googleapis.com",
      ]) &&
      alltrue([
        for k, a in google_project_iam_audit_config.data_read :
        a.project == "ryftlab-prod" && a.service == k && [for c in a.audit_log_config : c.log_type] == ["DATA_READ"]
      ])
    )
    error_message = "Data Access audit logs (DATA_READ) for Secret Manager, STS and IAM credentials, nothing else"
  }

  assert {
    condition     = length(regexall("resource \"google_project_iam_audit_config\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    error_message = "one audit config resource (data_read)"
  }
}

# The deployer before apply 2: run.developer project-wide, because CI creates the services at step 8.
run "deployer_before_apply_2" {
  command = plan

  variables {
    serve = false
  }

  assert {
    condition = (
      length(google_project_iam_member.deployer_run_developer) == 1 &&
      google_project_iam_member.deployer_run_developer[0].role == "roles/run.developer" &&
      google_project_iam_member.deployer_run_developer[0].member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com" &&
      length(google_cloud_run_v2_service_iam_member.deployer_run_developer) == 0 &&
      length(google_cloud_run_v2_job_iam_member.deployer_run_developer) == 0
    )
    error_message = "before apply 2 the deployer's one project role is run.developer"
  }

  assert {
    condition = toset(concat(
      [for b in google_project_iam_member.deployer_run_developer : b.role],
      [for b in google_cloud_run_v2_service_iam_member.deployer_run_developer : b.role],
      [for b in google_cloud_run_v2_job_iam_member.deployer_run_developer : b.role],
      [for b in google_service_account_iam_member.deployer_act_as : b.role],
      [google_artifact_registry_repository_iam_member.deployer_writer.role],
    )) == toset(["roles/run.developer", "roles/iam.serviceAccountUser", "roles/artifactregistry.writer"])
    error_message = "the deployer holds exactly run.developer, serviceAccountUser (runtime accounts) and artifactregistry.writer"
  }
}

# From apply 2 on, run.developer only on the four services and the job (tests/gates.tftest.hcl pins
# the project-wide binding gone). The roles below hold either way, the fallback PR included.
run "deployer_holds_no_iam_admin_role" {
  command = plan

  variables {
    serve = true
  }

  assert {
    condition = (
      length(google_cloud_run_v2_service_iam_member.deployer_run_developer) == 4 &&
      length(google_cloud_run_v2_job_iam_member.deployer_run_developer) == 1 &&
      alltrue(concat(
        [for b in google_cloud_run_v2_service_iam_member.deployer_run_developer : b.role == "roles/run.developer"],
        [for b in google_cloud_run_v2_job_iam_member.deployer_run_developer : b.role == "roles/run.developer"],
      ))
    )
    error_message = "after apply 2 the deployer's run.developer is on the four services and the job"
  }

  assert {
    condition = (
      toset([for b in google_service_account_iam_member.deployer_act_as : b.service_account_id]) == toset([
        for s in ["api", "ingest", "scoring", "notify", "migrate"] : "projects/ryftlab-prod/serviceAccounts/ryft-${s}@ryftlab-prod.iam.gserviceaccount.com"
      ]) &&
      alltrue([
        for b in google_service_account_iam_member.deployer_act_as :
        b.role == "roles/iam.serviceAccountUser" && b.member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"
      ])
    )
    error_message = "actAs on the five runtime accounts, never on ryft-push or grafana-reader"
  }

  assert {
    condition = (
      google_artifact_registry_repository_iam_member.deployer_writer.role == "roles/artifactregistry.writer" &&
      google_artifact_registry_repository_iam_member.deployer_writer.repository == "ryft" &&
      google_artifact_registry_repository_iam_member.deployer_writer.member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"
    )
    error_message = "the deployer writes to the one repository"
  }

  # Every role the deployer holds, collected from every binding that names it.
  assert {
    condition = toset(concat(
      [for b in google_project_iam_member.deployer_run_developer : b.role],
      [for b in google_cloud_run_v2_service_iam_member.deployer_run_developer : b.role],
      [for b in google_cloud_run_v2_job_iam_member.deployer_run_developer : b.role],
      [for b in google_service_account_iam_member.deployer_act_as : b.role],
      [google_artifact_registry_repository_iam_member.deployer_writer.role],
    )) == toset(["roles/run.developer", "roles/iam.serviceAccountUser", "roles/artifactregistry.writer"])
    error_message = "the deployer holds exactly run.developer, serviceAccountUser (runtime accounts) and artifactregistry.writer"
  }

  # No binding elsewhere names the deployer: exactly the five above reference its member.
  assert {
    condition     = length(regexall("member\\[\"ryft-deployer\"\\]", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 5
    error_message = "the deployer's member appears in exactly five bindings (run.developer project-wide, on the services, on the job; actAs; the repository)"
  }

  assert {
    condition = !anytrue([
      for r in concat(
        [for b in google_project_iam_member.deployer_run_developer : b.role],
        [for b in google_cloud_run_v2_service_iam_member.deployer_run_developer : b.role],
        [for b in google_cloud_run_v2_job_iam_member.deployer_run_developer : b.role],
        [for b in google_service_account_iam_member.deployer_act_as : b.role],
        [google_artifact_registry_repository_iam_member.deployer_writer.role],
      ) : can(regex("(?i)admin|owner|editor|iam\\.securityReviewer|tokenCreator|workloadIdentityUser|run\\.invoker", r))
    ])
    error_message = "no admin, owner, editor, token or invoker role for the deployer"
  }

  assert {
    condition = (
      google_project_iam_member.grafana_reader.role == "roles/monitoring.viewer" &&
      google_project_iam_member.grafana_reader.member == "serviceAccount:grafana-reader@ryftlab-prod.iam.gserviceaccount.com" &&
      length(regexall("member\\[\"grafana-reader\"\\]", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    )
    error_message = "grafana-reader holds monitoring.viewer and nothing else"
  }
}

run "wif_condition_by_ids" {
  command = plan

  assert {
    condition = google_iam_workload_identity_pool_provider.github.attribute_condition == join(" && ", [
      "assertion.repository_id == '100000001'",
      "assertion.repository_owner_id == '200000002'",
      "assertion.ref == 'refs/heads/main'",
      "assertion.event_name in ['push', 'workflow_dispatch']",
      "assertion.environment == 'deploy'",
      "assertion.job_workflow_ref == 'timkrebs/ryft-platform/.github/workflows/deploy.yml@refs/heads/main'",
      "assertion.runner_environment == 'github-hosted'",
    ])
    error_message = "the condition names repository_id, repository_owner_id, ref, event_name, environment, job_workflow_ref (by ID) and a GitHub-hosted runner"
  }

  assert {
    condition = (
      !strcontains(google_iam_workload_identity_pool_provider.github.attribute_condition, "assertion.repository ==") &&
      !strcontains(google_iam_workload_identity_pool_provider.github.attribute_condition, "assertion.repository_owner ==")
    )
    error_message = "no name-based repository or owner check: names can be re-registered"
  }

  assert {
    condition = (
      google_iam_workload_identity_pool.github.workload_identity_pool_id == "github" &&
      google_iam_workload_identity_pool_provider.github.workload_identity_pool_provider_id == "github-actions" &&
      google_iam_workload_identity_pool_provider.github.oidc[0].issuer_uri == "https://token.actions.githubusercontent.com" &&
      google_iam_workload_identity_pool_provider.github.attribute_mapping["attribute.repository_id"] == "assertion.repository_id" &&
      google_iam_workload_identity_pool_provider.github.attribute_mapping["google.subject"] == "assertion.sub"
    )
    error_message = "the pool github, the provider github-actions on GitHub's issuer, repository_id mapped"
  }

  assert {
    condition = (
      google_service_account_iam_member.deployer_workload_identity.service_account_id == "projects/ryftlab-prod/serviceAccounts/ryft-deployer@ryftlab-prod.iam.gserviceaccount.com" &&
      google_service_account_iam_member.deployer_workload_identity.role == "roles/iam.workloadIdentityUser" &&
      google_service_account_iam_member.deployer_workload_identity.member == "principalSet://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/github/attribute.repository_id/100000001"
    )
    error_message = "the deployer is usable by the repository's principal set in the GitHub pool only"
  }

  assert {
    condition = (
      output.github_workload_identity_provider == "projects/123456789012/locations/global/workloadIdentityPools/github/providers/github-actions" &&
      output.deployer_email == "ryft-deployer@ryftlab-prod.iam.gserviceaccount.com" &&
      output.github_principal_set == google_service_account_iam_member.deployer_workload_identity.member
    )
    error_message = "the outputs deploy.yml and step 10 use"
  }
}

run "rejects_a_repository_name_for_its_id" {
  command = plan

  variables {
    github_repository_id = "timkrebs/ryft-platform"
  }

  expect_failures = [var.github_repository_id]
}

run "rejects_an_owner_name_for_its_id" {
  command = plan

  variables {
    github_repository_owner_id = "timkrebs"
  }

  expect_failures = [var.github_repository_owner_id]
}
