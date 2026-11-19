output "project_number" {
  description = "ryftlab-prod's project number: part of every run.app URL (deploy/run/config.env)"
  value       = local.project_number
}

output "push_sa_unique_id" {
  description = "ryft-push's unique ID, the sub of every push and tick token: PUSH_SA_ID in deploy/run/config.env"
  value       = google_service_account.this["ryft-push"].unique_id
}

output "push_sa_email" {
  description = "PUSH_SA_EMAIL in deploy/run"
  value       = local.sa["ryft-push"]
}

output "github_workload_identity_provider" {
  description = "workload_identity_provider for google-github-actions/auth in deploy.yml"
  value       = "${local.github_pool_name}/providers/${google_iam_workload_identity_pool_provider.github.workload_identity_pool_provider_id}"
}

output "deployer_principal_set" {
  description = "The GitHub pool's principals in the environment deploy: the only member that may become ryft-deployer"
  value       = local.github_principal_set["deploy"]
}

output "site_principal_set" {
  description = "The GitHub pool's principals in the environment site: runbook step 10a binds site-deployer (ryftlab-site) to it, and nothing else"
  value       = local.github_principal_set["site"]
}

output "deployer_email" {
  description = "service_account for google-github-actions/auth in deploy.yml"
  value       = local.sa["ryft-deployer"]
}

output "run_urls" {
  description = "Each service's run.app URL: PUSH_AUDIENCE in deploy/run, the host of its push endpoints"
  value       = local.run_url
}

output "artifact_registry" {
  description = "Where deploy.yml pushes the five images"
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.ryft.repository_id}"
}

output "gates" {
  description = "The gates in force (gates.auto.tfvars) and what they mean for the tick"
  value = {
    serve           = var.serve
    public_invokers = var.public_invokers
    tick_paused     = !local.tick
  }
}

output "monitoring" {
  description = "Cloud Monitoring (D236): whether alerting is on, its policies, the uptime checks and the log-based metrics, by name (runbook step 11)"
  value = {
    alerting = local.alerting
    policies = sort(concat(
      [for p in google_monitoring_alert_policy.uptime : p.display_name],
      [for p in google_monitoring_alert_policy.promql : p.display_name],
    ))
    uptime_checks = sort(keys(google_monitoring_uptime_check_config.this))
    log_metrics = sort(concat(
      [google_logging_metric.ticks.name, google_logging_metric.errors.name],
      [for m in google_logging_metric.edge : m.name],
    ))
    dashboard = local.dashboard_name
  }
}

output "edge_ip" {
  description = "The edge's IPv4 address (D242): null while edge is off. Runbook step 16 reads it with terraform output -raw edge_ip; it goes into no file"
  value       = one(google_compute_global_address.edge[*].address)
  # Not a secret, but no apply's summary or remote run log prints it: -raw still does.
  sensitive = true
}

output "name_servers" {
  description = "The four Cloud DNS name servers to set at the registrar (GoDaddy), runbook step 6"
  value       = google_dns_managed_zone.ryftlab_de.name_servers
}

output "godaddy_instructions" {
  description = "What to enter at GoDaddy to delegate the domain to Cloud DNS (after scripts/dns_compare.sh)"
  value       = <<-EOT
    First: scripts/dns_compare.sh <a Route 53 name server>   (must end with "every record matches")
    GoDaddy → My Products → ${local.domain} → DNS → Nameservers → Change →
    "I'll use my own nameservers" → enter (without the trailing dot):
      ${join("\n  ", [for ns in google_dns_managed_zone.ryftlab_de.name_servers : trimsuffix(ns, ".")])}
    Then check: dig +norecurse NS ${local.domain} @a.nic.de   (should list the ns-cloud servers)
    Route 53 stays live for 48 hours (docs/OPERATIONS.md -> GCP, step 6).
  EOT
}
