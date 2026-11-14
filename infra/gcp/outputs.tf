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

output "github_principal_set" {
  description = "The repository's principal set in the GitHub pool: runbook step 10 binds the site's deploy SA to it"
  value       = local.github_principal_set
}

output "deployer_email" {
  description = "service_account for google-github-actions/auth in deploy.yml"
  value       = local.sa["ryft-deployer"]
}

output "grafana_reader_email" {
  description = "The account whose one JSON key goes into Grafana Cloud's data source (runbook step 11)"
  value       = local.sa["grafana-reader"]
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
