# What must never change silently: no key, no secret value in state, prevent_destroy where data
# lives, the API keep list, the registry's cleanup (the 5 newest kept, everything else gone after 30
# days), the log bucket. Plan-only, mocked; the file checks read the root's *.tf as dns.tftest.hcl in
# infra/aws/dns does.
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

run "no_key_and_no_secret_value_in_state" {
  command = plan

  assert {
    condition     = length(regexall("google_service_account_key", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no google_service_account_key (resource or data source) in any file: a key would put the private key into the state"
  }

  assert {
    condition     = length(regexall("secret_manager_(regional_)?secret_version", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no google_secret_manager_secret_version (resource or data source): values never pass through state"
  }
}

run "prevent_destroy_and_delayed_destruction" {
  command = plan

  assert {
    condition = (
      length(regexall("prevent_destroy\\s*=\\s*true", file("${path.module}/secrets.tf"))) == 1 &&
      length(regexall("version_destroy_ttl\\s*=\\s*\"2592000s\"", file("${path.module}/secrets.tf"))) == 1 &&
      length(regexall("prevent_destroy\\s*=\\s*true", file("${path.module}/dns.tf"))) == 1
    )
    error_message = "the secrets and the zone keep prevent_destroy; the secrets keep the 30-day destroy delay"
  }

  assert {
    condition     = length(regexall("resource \"google_secret_manager_secret\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    error_message = "one google_secret_manager_secret block (the one with prevent_destroy)"
  }

  assert {
    condition     = length(regexall("resource \"google_dns_managed_zone\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    error_message = "one zone (the one with prevent_destroy)"
  }

  assert {
    condition     = alltrue([for s in google_secret_manager_secret.this : s.version_destroy_ttl == "2592000s"])
    error_message = "every secret's versions are restorable for 30 days after a destroy"
  }
}

run "apis_keep_list" {
  command = plan

  assert {
    condition = toset(keys(google_project_service.keep)) == toset([
      "artifactregistry.googleapis.com", "billingbudgets.googleapis.com", "cloudbilling.googleapis.com",
      "cloudresourcemanager.googleapis.com", "cloudscheduler.googleapis.com", "cloudtrace.googleapis.com",
      "dns.googleapis.com", "iam.googleapis.com", "iamcredentials.googleapis.com", "logging.googleapis.com",
      "monitoring.googleapis.com", "pubsub.googleapis.com", "run.googleapis.com", "secretmanager.googleapis.com",
      "serviceusage.googleapis.com", "sts.googleapis.com",
    ])
    error_message = "exactly runbook step 2a's keep list (no container scanning, no Cloud Build)"
  }

  assert {
    condition     = alltrue([for s in google_project_service.keep : s.disable_on_destroy == false])
    error_message = "a destroy never disables an API"
  }
}

run "registry" {
  command = plan

  assert {
    condition = (
      google_artifact_registry_repository.ryft.repository_id == "ryft" &&
      google_artifact_registry_repository.ryft.location == "europe-west1" &&
      google_artifact_registry_repository.ryft.format == "DOCKER" &&
      google_artifact_registry_repository.ryft.cleanup_policy_dry_run == false &&
      [for v in google_artifact_registry_repository.ryft.vulnerability_scanning_config : v.enablement_config] == ["DISABLED"]
    )
    error_message = "repository ryft (docker, europe-west1), cleanup acting, vulnerability scanning off"
  }

  assert {
    condition = (
      length(google_artifact_registry_repository.ryft.cleanup_policies) == 3 &&
      anytrue([
        for p in google_artifact_registry_repository.ryft.cleanup_policies :
        p.action == "KEEP" && [for m in p.most_recent_versions : m.keep_count] == [5]
      ]) &&
      anytrue([
        for p in google_artifact_registry_repository.ryft.cleanup_policies :
        p.action == "DELETE" && [for c in p.condition : [c.tag_state, c.older_than]] == [["UNTAGGED", "2592000s"]]
      ]) &&
      anytrue([
        for p in google_artifact_registry_repository.ryft.cleanup_policies :
        p.action == "DELETE" && [for c in p.condition : [c.tag_state, c.older_than]] == [["ANY", "2592000s"]]
      ])
    )
    error_message = "keep the 5 newest versions of each image; delete every other version older than 30 days, tagged or not"
  }

  # The 5 newest are the only exception to the age rule: one KEEP rule (no tag-prefix KEEP that would
  # pin every short-SHA tag again), and no DELETE rule acts before 30 days.
  assert {
    condition = (
      length([for p in google_artifact_registry_repository.ryft.cleanup_policies : p if p.action == "KEEP"]) == 1 &&
      alltrue(flatten([
        for p in google_artifact_registry_repository.ryft.cleanup_policies :
        [for c in p.condition : c.older_than == "2592000s"] if p.action == "DELETE"
      ]))
    )
    error_message = "one KEEP rule (the 5 newest); every DELETE rule waits 30 days"
  }

  assert {
    condition     = output.artifact_registry == "europe-west1-docker.pkg.dev/ryftlab-prod/ryft"
    error_message = "the images go to europe-west1-docker.pkg.dev/ryftlab-prod/ryft"
  }
}

run "logs_in_the_eu_for_14_days" {
  command = plan

  assert {
    condition = (
      google_logging_project_bucket_config.ryft_logs.bucket_id == "ryft-logs" &&
      google_logging_project_bucket_config.ryft_logs.location == "europe-west1" &&
      google_logging_project_bucket_config.ryft_logs.retention_days == 14
    )
    error_message = "the bucket ryft-logs in europe-west1 keeps 14 days (the privacy text)"
  }

  assert {
    condition = (
      google_logging_project_sink.ryft_logs.destination == "logging.googleapis.com/projects/ryftlab-prod/locations/europe-west1/buckets/ryft-logs" &&
      length(regexall("filter\\s*=", file("${path.module}/logging.tf"))) == 0
    )
    error_message = "the sink routes everything (no filter) into ryft-logs"
  }

  assert {
    condition     = length(regexall("import\\s*\\{", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no import block: _Default is switched off by the runbook's gcloud command (logging.tf)"
  }
}

run "outputs_for_deploy_run" {
  command = plan

  assert {
    condition = (
      output.project_number == "123456789012" &&
      output.push_sa_email == "ryft-push@ryftlab-prod.iam.gserviceaccount.com" &&
      tomap(output.run_urls) == tomap({
        api     = "https://api-123456789012.europe-west1.run.app"
        ingest  = "https://ingest-123456789012.europe-west1.run.app"
        scoring = "https://scoring-123456789012.europe-west1.run.app"
        notify  = "https://notify-123456789012.europe-west1.run.app"
      })
    )
    error_message = "the project number, ryft-push and the four run.app URLs (ticket B's interfaces)"
  }
}
