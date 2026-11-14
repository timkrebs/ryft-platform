# The gates' states (gates.auto.tfvars), each set explicitly here so a flip of the committed file never
# changes what these runs pin. Plan-only, mocked.
#   serve = false                          apply 1: no invoker binding, nothing mapped, the tick paused,
#                                          the deployer's run.developer project-wide
#   serve = true,  public_invokers = true  apply 2: one invoker binding per service (ryft-push on all
#                                          four, allUsers on api and ingest), mapped, ticking; the
#                                          deployer's run.developer on the four services and the job only
#   serve = true,  public_invokers = false the 20 EUR stop line: ryft-push alone on every service, the
#                                          tick paused, the rest stays
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

run "the_gates_file_sets_both_gates" {
  command = plan

  assert {
    condition = (
      length(regexall("(?m)^serve\\s*=\\s*(true|false)\\s*$", file("${path.module}/gates.auto.tfvars"))) == 1 &&
      length(regexall("(?m)^public_invokers\\s*=\\s*(true|false)\\s*$", file("${path.module}/gates.auto.tfvars"))) == 1 &&
      length(regexall("(?m)^[a-z_]+\\s*=", file("${path.module}/gates.auto.tfvars"))) == 2
    )
    error_message = "gates.auto.tfvars sets serve and public_invokers, once each, and nothing else"
  }
}

run "apply_1_serve_false" {
  command = plan

  variables {
    serve           = false
    public_invokers = true
  }

  assert {
    condition = (
      length(google_cloud_run_v2_service_iam_binding.invoker) == 0 &&
      length(google_cloud_run_domain_mapping.host) == 0 &&
      length(google_dns_record_set.mapping) == 0
    )
    error_message = "apply 1: no invoker binding (the services do not exist yet), no mapping, no mapping record"
  }

  # CI creates the services and the job at step 8, so the deployer needs run.developer project-wide.
  assert {
    condition = (
      length(google_project_iam_member.deployer_run_developer) == 1 &&
      google_project_iam_member.deployer_run_developer[0].role == "roles/run.developer" &&
      google_project_iam_member.deployer_run_developer[0].member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com" &&
      length(google_cloud_run_v2_service_iam_member.deployer_run_developer) == 0 &&
      length(google_cloud_run_v2_job_iam_member.deployer_run_developer) == 0
    )
    error_message = "apply 1: the deployer's run.developer is project-wide, and on no service or job"
  }

  assert {
    condition     = google_cloud_scheduler_job.tick.paused
    error_message = "apply 1: the tick is paused"
  }

  assert {
    condition     = output.gates.serve == false && output.gates.public_invokers == true && output.gates.tick_paused == true
    error_message = "the gates output says what is in force"
  }
}

run "apply_2_serve_true_public_true" {
  command = plan

  variables {
    serve           = true
    public_invokers = true
  }

  assert {
    condition = (
      toset(keys(google_cloud_run_v2_service_iam_binding.invoker)) == toset(["api", "ingest", "scoring", "notify"]) &&
      alltrue([
        for k, b in google_cloud_run_v2_service_iam_binding.invoker :
        b.name == k && b.location == "europe-west1" && b.project == "ryftlab-prod" && b.role == "roles/run.invoker"
      ])
    )
    error_message = "apply 2: one run.invoker binding on each of the four services, named by name"
  }

  # Authoritative: these members and nobody else, so a hand-added allUsers on scoring goes at the next apply.
  assert {
    condition = alltrue([
      for s in ["api", "ingest"] :
      google_cloud_run_v2_service_iam_binding.invoker[s].members == toset([
        "serviceAccount:ryft-push@ryftlab-prod.iam.gserviceaccount.com",
        "allUsers",
      ])
    ])
    error_message = "apply 2: api and ingest are invoked by ryft-push and allUsers, nobody else"
  }

  assert {
    condition = alltrue([
      for s in ["scoring", "notify"] :
      google_cloud_run_v2_service_iam_binding.invoker[s].members == toset(["serviceAccount:ryft-push@ryftlab-prod.iam.gserviceaccount.com"])
    ])
    error_message = "apply 2: scoring and notify are invoked by ryft-push alone, never by allUsers"
  }

  # The deployer narrowed: run.developer on what exists, nothing project-wide. The fallback PR of runbook
  # step 9 (and a new service's first deploy) sets the project-wide binding's count to 1, and this
  # assertion with it.
  assert {
    condition     = length(google_project_iam_member.deployer_run_developer) == 0
    error_message = "apply 2: the deployer's project-wide run.developer is gone"
  }

  assert {
    condition = (
      toset(keys(google_cloud_run_v2_service_iam_member.deployer_run_developer)) == toset(["api", "ingest", "scoring", "notify"]) &&
      toset(keys(google_cloud_run_v2_job_iam_member.deployer_run_developer)) == toset(["migrate"]) &&
      alltrue([
        for k, b in google_cloud_run_v2_service_iam_member.deployer_run_developer :
        b.name == k && b.location == "europe-west1" && b.project == "ryftlab-prod" && b.role == "roles/run.developer" &&
        b.member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"
      ]) &&
      alltrue([
        for k, b in google_cloud_run_v2_job_iam_member.deployer_run_developer :
        b.name == k && b.location == "europe-west1" && b.project == "ryftlab-prod" && b.role == "roles/run.developer" &&
        b.member == "serviceAccount:ryft-deployer@ryftlab-prod.iam.gserviceaccount.com"
      ])
    )
    error_message = "apply 2: the deployer's run.developer is on the four services and the job migrate, each by name"
  }

  assert {
    condition = (
      google_cloud_run_domain_mapping.host["api"].name == "api.ryftlab.de" &&
      google_cloud_run_domain_mapping.host["api"].spec[0].route_name == "api" &&
      google_cloud_run_domain_mapping.host["ingest"].name == "ingest.ryftlab.de" &&
      google_cloud_run_domain_mapping.host["ingest"].spec[0].route_name == "ingest" &&
      alltrue([for m in google_cloud_run_domain_mapping.host : m.location == "europe-west1" && m.metadata[0].namespace == "ryftlab-prod"])
    )
    error_message = "apply 2: api.ryftlab.de maps to api, ingest.ryftlab.de to ingest"
  }

  assert {
    condition = (
      toset(keys(google_dns_record_set.mapping)) == toset(["api", "ingest"]) &&
      google_dns_record_set.mapping["api"].name == "api.ryftlab.de." &&
      google_dns_record_set.mapping["ingest"].name == "ingest.ryftlab.de." &&
      alltrue([for r in google_dns_record_set.mapping : r.type == "CNAME"])
    )
    error_message = "apply 2: one CNAME per mapped host, from the mapping's status"
  }

  assert {
    condition     = !google_cloud_scheduler_job.tick.paused
    error_message = "apply 2: the tick runs"
  }

  assert {
    condition     = output.gates.serve == true && output.gates.public_invokers == true && output.gates.tick_paused == false
    error_message = "the gates output says what is in force"
  }
}

run "stop_line_serve_true_public_false" {
  command = plan

  variables {
    serve           = true
    public_invokers = false
  }

  # The bindings stay (no destroy): api and ingest keep ryft-push alone, which is what the stop line's
  # gcloud commands leave, so the next apply neither undoes them nor adds allUsers back.
  assert {
    condition = (
      toset(keys(google_cloud_run_v2_service_iam_binding.invoker)) == toset(["api", "ingest", "scoring", "notify"]) &&
      alltrue([
        for b in google_cloud_run_v2_service_iam_binding.invoker :
        b.members == toset(["serviceAccount:ryft-push@ryftlab-prod.iam.gserviceaccount.com"])
      ])
    )
    error_message = "the stop line: every service keeps its binding with ryft-push alone; no allUsers anywhere"
  }

  assert {
    condition     = google_cloud_scheduler_job.tick.paused
    error_message = "the stop line: the tick stays paused"
  }

  assert {
    condition = (
      length(google_cloud_run_domain_mapping.host) == 2 &&
      length(google_dns_record_set.mapping) == 2 &&
      length(google_cloud_run_v2_service_iam_member.deployer_run_developer) == 4 &&
      length(google_cloud_run_v2_job_iam_member.deployer_run_developer) == 1
    )
    error_message = "the stop line leaves the mappings, their records and the deployer's bindings alone (no destroy at all)"
  }
}

run "serve_false_public_false" {
  command = plan

  variables {
    serve           = false
    public_invokers = false
  }

  assert {
    condition = (
      length(google_cloud_run_v2_service_iam_binding.invoker) == 0 &&
      length(google_cloud_run_domain_mapping.host) == 0 &&
      length(google_project_iam_member.deployer_run_developer) == 1 &&
      length(google_cloud_run_v2_service_iam_member.deployer_run_developer) == 0 &&
      length(google_cloud_run_v2_job_iam_member.deployer_run_developer) == 0 &&
      google_cloud_scheduler_job.tick.paused
    )
    error_message = "serve = false wins: nothing invokes, nothing is mapped, the tick is paused, the deployer is project-wide"
  }
}

# run.invoker is granted in one place only, the authoritative binding: a member resource for the same
# role next to it would fight it on every apply, and a policy resource would also wipe the deployer's.
run "invokers_only_through_the_binding" {
  command = plan

  assert {
    condition = (
      length(regexall("\"roles/run\\.invoker\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      length(regexall("resource \"google_cloud_run_v2_service_iam_binding\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      length(regexall("google_cloud_run_(v2_)?(service|job)_iam_policy|google_cloud_run_service_iam_", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    )
    error_message = "run.invoker only in google_cloud_run_v2_service_iam_binding.invoker; no Cloud Run IAM policy resource, no v1 Cloud Run IAM resource"
  }
}

# The Grafana-managed rules exist only once the data source UID is set (runbook step 11).
run "no_grafana_rules_before_step_11" {
  command = plan

  assert {
    condition     = length(grafana_folder.gcp) == 0 && length(grafana_rule_group.gcp) == 0
    error_message = "no Grafana resource (and so no Grafana token) before the data source exists"
  }
}

run "grafana_rules_at_step_11" {
  command = plan

  variables {
    grafana_url            = "https://example.grafana.net"
    grafana_datasource_uid = "cloud-monitoring"
  }

  assert {
    condition = (
      length(grafana_folder.gcp) == 1 &&
      grafana_rule_group.gcp[0].interval_seconds == 300 &&
      toset([for r in grafana_rule_group.gcp[0].rule : r.name]) == toset(["HTTP5xxRatio", "SyncSlow", "DeadLetters", "PushBacklog", "Saturated"])
    )
    error_message = "one group, evaluated every 5 minutes, with the five Cloud Monitoring rules"
  }

  assert {
    condition = alltrue([
      for r in grafana_rule_group.gcp[0].rule :
      r.condition == "C" && contains(["critical", "warning"], r.labels["severity"]) &&
      [for d in r.data : d.datasource_uid] == ["cloud-monitoring", "__expr__", "__expr__"] &&
      jsondecode(r.data[0].model).queryType == "promQL" && jsondecode(r.data[0].model).promQLQuery.projectName == "ryftlab-prod"
    ])
    error_message = "every rule queries the Cloud Monitoring data source in ryftlab-prod, then reduces and thresholds"
  }

  assert {
    condition = alltrue([
      for r in grafana_rule_group.gcp[0].rule :
      !can(regex("ryft_[a-z_]+", jsondecode(r.data[0].model).promQLQuery.expr))
    ])
    error_message = "no app metric here: ryft_* rules live in deploy/grafana/alerts"
  }
}

run "rejects_a_datasource_without_a_grafana_url" {
  command = plan

  variables {
    grafana_datasource_uid = "cloud-monitoring"
  }

  expect_failures = [var.grafana_datasource_uid]
}
