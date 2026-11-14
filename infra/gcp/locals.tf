locals {
  # The domain (D213). A constant: the mail records in dns.tf are this domain's.
  domain = "ryftlab.de"

  # The repository and the one workflow that may deploy (wif.tf). The IDs are variables; the name only
  # appears in job_workflow_ref, which GitHub writes as owner/repo/path@ref.
  github_repository   = "timkrebs/ryft-platform"
  github_workflow_ref = "${local.github_repository}/.github/workflows/deploy.yml@refs/heads/main"

  project_number = data.google_project.this.number

  # The four Cloud Run services and the job (ticket B's interfaces). CI creates them (deploy.yml);
  # this root names them, it never defines them.
  services        = toset(["api", "ingest", "scoring", "notify"])
  public_services = toset(["api", "ingest"])
  runtime         = toset(["api", "ingest", "scoring", "notify", "migrate"])

  # Account ID => display name. No key for any of them, ever: grafana-reader's one JSON key is made by
  # hand at runbook step 11 and lives only in Grafana Cloud (D224).
  accounts = merge(
    { for s in local.runtime : "ryft-${s}" => "RYFT ${s}: Cloud Run runtime" },
    {
      "ryft-push"      = "RYFT push and tick tokens (Pub/Sub OIDC)"
      "ryft-deployer"  = "CI: deploy.yml through GitHub WIF"
      "grafana-reader" = "Grafana Cloud: Cloud Monitoring, read only"
    },
  )
  runtime_accounts = toset([for s in local.runtime : "ryft-${s}"])

  # Built from the accounts' own arguments (known at plan, so the plan shows every member in full), which
  # also makes each binding depend on its account.
  sa     = { for k, a in google_service_account.this : k => "${a.account_id}@${a.project}.iam.gserviceaccount.com" }
  sa_id  = { for k, e in local.sa : k => "projects/${var.project_id}/serviceAccounts/${e}" }
  member = { for k, e in local.sa : k => "serviceAccount:${e}" }

  # The deterministic run.app URL of each service: the push endpoints' host and the OIDC audience.
  run_url = { for s in local.services : s => "https://${s}-${local.project_number}.${var.region}.run.app" }

  # Google's service agents in this project (project.tf makes sure they exist).
  pubsub_agent    = "serviceAccount:service-${local.project_number}@gcp-sa-pubsub.iam.gserviceaccount.com"
  scheduler_agent = "serviceAccount:service-${local.project_number}@gcp-sa-cloudscheduler.iam.gserviceaccount.com"

  # The gates (gates.auto.tfvars). public: allUsers on api and ingest. tick: the Scheduler job runs.
  # Both need the stop line open, so the stop line's gcloud commands and public_invokers = false agree.
  public = var.serve && var.public_invokers
  tick   = var.serve && var.public_invokers

  # The Grafana-managed rules exist once the data source does (runbook step 11).
  alerts = var.grafana_datasource_uid != ""
}
