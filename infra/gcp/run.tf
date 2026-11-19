# What this root owns of Cloud Run: who may invoke, and the custom hosts (the deployer's bindings on
# the services and the job are in iam.tf). The services and the job themselves belong to CI
# (deploy/run, deploy.yml; D223), which creates them at runbook step 8, before apply 2. So everything
# here names a service by name, and exists only once serve is true.
locals {
  # Host label => the service it maps to. A mapping routes only "/", hence one host per service (D217).
  mapped_hosts = {
    api    = "api"
    ingest = "ingest"
  }
}

# Who invokes each service, authoritatively for the role (local.invokers): ryft-push for Pub/Sub's pushes
# and the ticks on all four, and allUsers on api and ingest, where the app's own auth (and the push OIDC
# check) is the gate. Under the stop line (public_invokers = false) api and ingest keep ryft-push only.
# A binding, not members: any invoker added by hand (allUsers on scoring, say) is removed by the next
# apply. No other resource in this root may grant the role (tests/gates.tftest.hcl), or the two would
# fight. Nothing at all with serve = false: the services do not exist before step 8.
resource "google_cloud_run_v2_service_iam_binding" "invoker" {
  for_each = var.serve ? local.invokers : {}

  project  = var.project_id
  location = var.region
  name     = each.key
  role     = "roles/run.invoker"
  members  = each.value
}

# Preview in europe-west1 (D217). The caller must be a verified owner of ryftlab.de in Search Console:
# runbook step 6 adds hcp-terraform-apply@ryftlab-prod.iam.gserviceaccount.com as one, or this fails.
# Google issues the certificate once the CNAME (dns.tf) resolves: 15 minutes to 24 hours.
#
# The edge (D242): from edge = live, DNS names the load balancer, not ghs.googlehosted.com, so a
# mapping's certificate can no longer renew; the mappings stay through live as the way back while their
# certificates still hold. locked removes them. Going back from locked re-creates them, but their
# certificates issue only once DNS points at ghs again (live -> built): an outage of up to the issuance
# time, which is why locked waits for 7 clean days (runbook step 16).
resource "google_cloud_run_domain_mapping" "host" {
  for_each = var.serve && !local.edge_locked ? local.mapped_hosts : {}

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
