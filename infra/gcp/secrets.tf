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
    # PostHog's project API key, served to the app by GET /v1/config (D231). Its pin in deploy/run/api.yaml
    # comes after the first version: a revision that reads a missing version does not start.
    "posthog-project-key" = ["ryft-api"]
    # App Attest's token ring (D245, D247), read as APP_ATTEST_TOKEN_KEYS: 1 to 3 lines
    # `<kid>:<base64 of 32 bytes>`; the first signs (api), every line verifies (api and ingest). The value
    # never passes through Terraform (OPERATIONS -> GCP -> App Attest); deploy/run pins it from the warn flip on.
    "app-attest-token-key" = ["ryft-api", "ryft-ingest"]
    # Garmin's OAuth client secret for direct connections (D257), read as CONNECT_GARMIN_CLIENT_SECRET:
    # api alone exchanges and refreshes the tokens. An empty container in phase 1; the user adds the
    # version in phase 2 (after the programme's approval), and deploy/run/api.yaml pins it only then.
    "connect-garmin-client-secret" = ["ryft-api"]
  }

  # Retired secrets, on their way out (docs/OPERATIONS.md -> GCP, step 11.6). prevent_destroy on
  # google_secret_manager_secret.this stops a plain removal, so a retired secret first moves here, the
  # same container and binding without prevent_destroy (apply 1: 0 to add, change or destroy, 2 moved),
  # then its own PR deletes these blocks (apply 2: 2 to destroy). tests/guards.tftest.hcl pins the keys.
  retired_secret_accessors = {
    # Unread since D236: deploy/run pins it no more, and no serving revision read it on 2026-11-18.
    "otlp-token" = ["ryft-api", "ryft-ingest", "ryft-scoring", "ryft-notify"]
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

# The retired secrets: the same arguments as above, so the move changes nothing; no prevent_destroy.
resource "google_secret_manager_secret" "retired" {
  for_each = local.retired_secret_accessors

  project   = var.project_id
  secret_id = each.key

  version_destroy_ttl = "2592000s"

  replication {
    user_managed {
      replicas {
        location = var.region
      }
    }
  }
}

resource "google_secret_manager_secret_iam_binding" "retired" {
  for_each = local.retired_secret_accessors

  project   = var.project_id
  secret_id = google_secret_manager_secret.retired[each.key].secret_id
  role      = "roles/secretmanager.secretAccessor"
  members   = [for a in each.value : local.member[a]]
}

moved {
  from = google_secret_manager_secret.this["otlp-token"]
  to   = google_secret_manager_secret.retired["otlp-token"]
}

moved {
  from = google_secret_manager_secret_iam_binding.accessor["otlp-token"]
  to   = google_secret_manager_secret_iam_binding.retired["otlp-token"]
}
