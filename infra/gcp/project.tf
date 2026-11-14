data "google_project" "this" {
  project_id = var.project_id
}

locals {
  # Runbook step 2a's keep list, exactly. The bootstrap turns everything else off by hand.
  apis = toset([
    "artifactregistry.googleapis.com",
    "billingbudgets.googleapis.com",
    "cloudbilling.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "cloudscheduler.googleapis.com",
    "cloudtrace.googleapis.com",
    "dns.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "logging.googleapis.com",
    "monitoring.googleapis.com",
    "pubsub.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
    "serviceusage.googleapis.com",
    "sts.googleapis.com",
  ])

  # Service agents that a new project may not have until something asks for them, and that this root
  # binds (pubsub.tf, iam.tf).
  agents = toset(["pubsub.googleapis.com", "cloudscheduler.googleapis.com"])

  # Data Access audit logs (DATA_READ), below.
  #   secretmanager:  every secret read (who, which version; D222);
  #   sts:            every token exchange through a pool (GitHub -> ryft-deployer, HCP Terraform);
  #   iamcredentials: every token minted for an account through the IAM credentials API (the
  #                   deployer's access token after the exchange, anyone impersonating an account).
  data_read_audit = toset(["secretmanager.googleapis.com", "sts.googleapis.com", "iamcredentials.googleapis.com"])
}

resource "google_project_service" "keep" {
  for_each = local.apis

  project = var.project_id
  service = each.key

  # A destroy of this root must never switch an API off under the running services.
  disable_on_destroy = false
}

# Cloud Audit Logs: Admin Activity is always on; Data Access is on for the three services above only,
# so every secret read and every token mint is logged. The volume is small (secret reads at each
# instance start, token mints at each deploy and apply). Data Access entries are not _Required's: the
# sink takes them into ryft-logs (logging.tf), 14 days.
resource "google_project_iam_audit_config" "data_read" {
  for_each = local.data_read_audit

  project = var.project_id
  service = each.key

  audit_log_config {
    log_type = "DATA_READ"
  }
}

# service-<number>@gcp-sa-pubsub and @gcp-sa-cloudscheduler: created now if the project has none yet,
# so the bindings in pubsub.tf and iam.tf never name an account that does not exist. The bindings use
# the deterministic address (locals.tf) and depend on this resource. Beta-only in the provider. Once
# created it is never deleted: a destroy only drops it from the state.
resource "google_project_service_identity" "agent" {
  provider = google-beta
  for_each = local.agents

  project = var.project_id
  service = each.key

  depends_on = [google_project_service.keep]
}
