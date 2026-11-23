# What must never change silently: no key, no secret value in state, prevent_destroy where data
# lives, the API keep list, the registry's cleanup (the 5 newest kept, everything else gone after 30
# days), the log bucket. Plan-only, mocked; the file checks read the root's *.tf.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

# Cloud Run fills in a domain mapping's status only at apply, so with serve = true checks.tf's
# domain_mappings_ready is unknown in a plan, and an unknown check fails the run. Mocked as a Ready
# mapping with the one CNAME Cloud Run returns for a subdomain (dns.tf's records read it); the per-host
# fields nothing reads stay empty. In every test file: once gates.auto.tfvars says serve = true, every
# run without its own serve plans the mappings.
override_resource {
  target          = google_cloud_run_domain_mapping.host
  override_during = plan
  values = {
    status = [{
      conditions          = [{ type = "Ready", status = "True", reason = "", message = "" }]
      mapped_route_name   = ""
      observed_generation = 1
      resource_records    = [{ name = "", type = "CNAME", rrdata = "ghs.googlehosted.com." }]
    }]
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
  # The keep list and the sink as before D242, whatever gates.auto.tfvars holds; the edge's phases are
  # tests/edge.tftest.hcl.
  edge = "off"
}

run "no_key_and_no_secret_value_in_state" {
  command = plan

  # Blocks only: a comment may name them (iam.tf and secrets.tf do, to say there are none).
  assert {
    condition     = length(regexall("(?m)^\\s*(resource|data)\\s+\"google_service_account_key", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no google_service_account_key resource or data block in any file: a key would put the private key into the state"
  }

  assert {
    condition     = length(regexall("(?m)^\\s*(resource|data|ephemeral)\\s+\"google_secret_manager_(regional_)?secret_version", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no google_secret_manager_(regional_)secret_version* resource, data or ephemeral block (_access included): values never pass through Terraform"
  }

  # App Attest's token ring (D247) is a container like the others: the guard above covers it, and no
  # argument anywhere carries a secret's payload.
  assert {
    condition = (
      contains(keys(google_secret_manager_secret.this), "app-attest-token-key") &&
      length(regexall("(?m)^\\s*secret_data(_wo)?\\s*=", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    )
    error_message = "app-attest-token-key is a container only: its value is added with gcloud (OPERATIONS -> GCP -> App Attest), never as secret_data"
  }

  # Garmin's client secret (D257) likewise: an empty container in phase 1, its value added with gcloud
  # in phase 2, kept by prevent_destroy (`this`).
  assert {
    condition = (
      contains(keys(google_secret_manager_secret.this), "connect-garmin-client-secret") &&
      !contains(keys(google_secret_manager_secret.retired), "connect-garmin-client-secret")
    )
    error_message = "connect-garmin-client-secret is a container with prevent_destroy (this), its value never in Terraform"
  }
}

run "prevent_destroy_and_delayed_destruction" {
  command = plan

  assert {
    condition = (
      length(regexall("(?m)^\\s*prevent_destroy\\s*=\\s*true", file("${path.module}/secrets.tf"))) == 1 &&
      length(regexall("(?m)^\\s*version_destroy_ttl\\s*=\\s*\"2592000s\"", file("${path.module}/secrets.tf"))) == 2 &&
      length(regexall("(?m)^\\s*prevent_destroy\\s*=\\s*true", file("${path.module}/dns.tf"))) == 1
    )
    error_message = "the secrets and the zone keep prevent_destroy; both secret blocks (this, retired) keep the 30-day destroy delay"
  }

  # The one prevent_destroy of secrets.tf sits in `this`, in its lifecycle, not commented out (Terraform
  # test doesn't enforce it, so the file is checked).
  assert {
    condition = (
      length([for c in split("\nresource \"", file("${path.module}/secrets.tf")) : c if startswith(c, "google_secret_manager_secret\" \"this\" {")]) == 1 &&
      alltrue([
        for c in split("\nresource \"", file("${path.module}/secrets.tf")) :
        can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true$", c)) if startswith(c, "google_secret_manager_secret\" \"this\" {")
      ])
    )
    error_message = "google_secret_manager_secret.this (secrets.tf) needs lifecycle { prevent_destroy = true }: the data key's container is among them"
  }

  # Two blocks until step 11.6's PR 2 (docs/OPERATIONS.md -> GCP): `this`, with prevent_destroy, and
  # `retired`, without, for otlp-token alone. Nothing else may sit in the one without.
  assert {
    condition     = length(regexall("(?m)^\\s*resource\\s+\"google_secret_manager_secret\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 2
    error_message = "two google_secret_manager_secret blocks: this (prevent_destroy) and retired (step 11.6)"
  }

  assert {
    condition     = toset(keys(google_secret_manager_secret.retired)) == toset(["otlp-token"])
    error_message = "only otlp-token is retired: every other secret keeps prevent_destroy"
  }

  assert {
    condition     = length(regexall("(?m)^\\s*resource\\s+\"google_dns_managed_zone\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    error_message = "one zone (the one with prevent_destroy)"
  }

  assert {
    condition     = alltrue([for s in concat(values(google_secret_manager_secret.this), values(google_secret_manager_secret.retired)) : s.version_destroy_ttl == "2592000s"])
    error_message = "every secret's versions, the retired ones' included, are restorable for 30 days after a destroy"
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
    ]) && length(google_project_service.keep) == 16
    error_message = "edge off: exactly runbook step 2a's keep list of 16 (no container scanning, no Cloud Build, no Compute Engine)"
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

  # No inclusion filter: the one `filter =` in logging.tf is the exclusions' (D242), and with the edge
  # off only connect-callback (D257) is one.
  assert {
    condition = (
      google_logging_project_sink.ryft_logs.destination == "logging.googleapis.com/projects/ryftlab-prod/locations/europe-west1/buckets/ryft-logs" &&
      length(regexall("(?m)^\\s*filter\\s*=", file("${path.module}/logging.tf"))) == 1 &&
      length(regexall("(?m)^\\s*content\\s*\\{[^}]*\\n\\s*filter\\s*=\\s*exclusions\\.value\\.query\\n", file("${path.module}/logging.tf"))) == 1 &&
      google_logging_project_sink.ryft_logs.filter == null &&
      [for e in google_logging_project_sink.ryft_logs.exclusions : e.name] == ["connect-callback"]
    )
    error_message = "the sink routes everything into ryft-logs: no inclusion filter, and with the edge off only connect-callback (D257)"
  }

  assert {
    condition     = length(regexall("(?m)^\\s*import\\s*\\{", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no import block: _Default is switched off by the runbook's gcloud command (logging.tf)"
  }
}

# Review S4 (D257): the callback's URL, its OAuth state and code, is kept nowhere, in any phase of
# the edge. One sink in the root, the one into ryft-logs (_Default is off, _Required takes audit logs
# only), and it drops every entry with that URL, whatever else would keep it. Edge off first: api's
# Cloud Run request log then keeps every request, the callback's included, but for this exclusion.
run "no_sink_keeps_a_connect_callback_url_edge_off" {
  command = plan

  assert {
    condition = (
      length(regexall("(?m)^\\s*resource\\s+\"google_logging_(project|folder|organization|billing_account)_sink\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      google_logging_project_sink.ryft_logs.filter == null &&
      [for e in google_logging_project_sink.ryft_logs.exclusions : [e.name, e.filter]] == [["connect-callback", "httpRequest.requestUrl=~\"^https?://[^/]+/v1/connect/[a-z0-9]+/callback([?]|$)\""]] &&
      alltrue([for e in google_logging_project_sink.ryft_logs.exclusions : e.disabled != true])
    )
    error_message = "edge off: one sink, and it excludes every entry whose URL is a direct-connection callback (connect-callback, anchored, enabled), unconditionally"
  }
}

run "no_sink_keeps_a_connect_callback_url" {
  command = plan

  variables {
    edge = "live"
  }

  assert {
    condition = (
      length(regexall("(?m)^\\s*resource\\s+\"google_logging_(project|folder|organization|billing_account)_sink\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      google_logging_project_sink.ryft_logs.filter == null &&
      [for e in google_logging_project_sink.ryft_logs.exclusions : e.filter if e.name == "connect-callback"] == ["httpRequest.requestUrl=~\"^https?://[^/]+/v1/connect/[a-z0-9]+/callback([?]|$)\""] &&
      alltrue([for e in google_logging_project_sink.ryft_logs.exclusions : e.disabled != true])
    )
    error_message = "one sink, and from edge = built on it excludes every entry whose URL is a direct-connection callback (connect-callback, anchored, enabled)"
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
