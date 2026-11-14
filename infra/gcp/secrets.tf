# Secret Manager containers only (D222). The values are added by the user with
# `gcloud secrets versions add ... --data-file=...` (runbook step 5) and never pass through Terraform:
# there is no google_secret_manager_secret_version in this root (tests/guards.tftest.hcl). Cloud Run
# pins each secret by version number (deploy/run), never `latest`.
locals {
  # Secret ID => the only accounts that may read it (ticket B's table). Bound on the secret itself,
  # authoritatively for the role, never at project level.
  secret_accessors = {
    "db-password-api"     = ["ryft-api", "ryft-migrate"]
    "db-password-ingest"  = ["ryft-ingest", "ryft-migrate"]
    "db-password-scoring" = ["ryft-scoring", "ryft-migrate"]
    "db-password-notify"  = ["ryft-notify", "ryft-migrate"]
    "db-password-owner"   = ["ryft-migrate"]
    "supabase-secret-key" = ["ryft-api"]
    "admin-api-key"       = ["ryft-api"]
    "apple-key-p8"        = ["ryft-api"]
    "apns-key-p8"         = ["ryft-notify"]
    "data-key"            = ["ryft-api"]
    "otlp-token"          = ["ryft-api", "ryft-ingest", "ryft-scoring", "ryft-notify"]
  }
}

resource "google_secret_manager_secret" "this" {
  for_each = local.secret_accessors

  project   = var.project_id
  secret_id = each.key

  # Delayed destruction (design 5.3): a destroyed version is disabled first and restorable for 30 days.
  version_destroy_ttl = "2592000s"

  replication {
    user_managed {
      replicas {
        location = var.region
      }
    }
  }

  # The data key's container among them: deleting a secret deletes every version at once.
  lifecycle {
    prevent_destroy = true
  }
}

resource "google_secret_manager_secret_iam_binding" "accessor" {
  for_each = local.secret_accessors

  project   = var.project_id
  secret_id = google_secret_manager_secret.this[each.key].secret_id
  role      = "roles/secretmanager.secretAccessor"
  members   = [for a in each.value : local.member[a]]
}
