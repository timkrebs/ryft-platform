# What this root owns of Cloud Run: who may invoke, and the custom hosts. The services and the job
# themselves belong to CI (deploy/run, deploy.yml; D223), which creates them at runbook step 8, before
# apply 2. So everything here names a service by name, and exists only once serve is true.
locals {
  # Host label => the service it maps to. A mapping routes only "/", hence one host per service (D217).
  mapped_hosts = {
    api    = "api"
    ingest = "ingest"
  }
}

# Pub/Sub's pushes and the ticks, on all four. On scoring and notify this is the only invoker.
resource "google_cloud_run_v2_service_iam_member" "push_invoker" {
  for_each = var.serve ? local.services : toset([])

  project  = var.project_id
  location = var.region
  name     = each.key
  role     = "roles/run.invoker"
  member   = local.member["ryft-push"]
}

# api and ingest are public; the app's own auth (and the push OIDC check) is the gate there. Off under
# the stop line (public_invokers = false).
resource "google_cloud_run_v2_service_iam_member" "public_invoker" {
  for_each = local.public ? local.public_services : toset([])

  project  = var.project_id
  location = var.region
  name     = each.key
  role     = "roles/run.invoker"
  member   = "allUsers"
}

# Preview in europe-west1 (D217). The caller must be a verified owner of ryftlab.de in Search Console:
# runbook step 6 adds hcp-terraform-apply@ryftlab-prod.iam.gserviceaccount.com as one, or this fails.
# Google issues the certificate once the CNAME (dns.tf) resolves: 15 minutes to 24 hours.
resource "google_cloud_run_domain_mapping" "host" {
  for_each = var.serve ? local.mapped_hosts : {}

  project  = var.project_id
  location = var.region
  name     = "${each.key}.${local.domain}"

  metadata {
    namespace = var.project_id
  }

  spec {
    route_name = each.value
  }
}
