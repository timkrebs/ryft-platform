# europe-west1-docker.pkg.dev/ryftlab-prod/ryft/<api|ingest|scoring|notify|migrate>, deployed by digest.
resource "google_artifact_registry_repository" "ryft" {
  project       = var.project_id
  location      = var.region
  repository_id = "ryft"
  format        = "DOCKER"
  description   = "RYFT images, pushed by deploy.yml (infra/gcp)"

  # The policies act, they are not a preview. KEEP wins over DELETE: the 5 newest versions of each image
  # stay whatever their age (rollback targets); every other version older than 30 days goes, tagged or
  # not (the untagged rule is a subset of the ANY rule). Every push to main tags all five images with its
  # short SHA, so without the ANY rule no image would ever go.
  cleanup_policy_dry_run = false

  cleanup_policies {
    id     = "keep-5-most-recent"
    action = "KEEP"

    most_recent_versions {
      keep_count = 5
    }
  }

  cleanup_policies {
    id     = "delete-untagged-after-30-days"
    action = "DELETE"

    condition {
      tag_state  = "UNTAGGED"
      older_than = "2592000s"
    }
  }

  cleanup_policies {
    id     = "delete-any-after-30-days"
    action = "DELETE"

    condition {
      tag_state  = "ANY"
      older_than = "2592000s"
    }
  }

  # Scanning is billed per image and is not on the keep list (ticket B, design fix).
  vulnerability_scanning_config {
    enablement_config = "DISABLED"
  }
}

resource "google_artifact_registry_repository_iam_member" "deployer_writer" {
  project    = var.project_id
  location   = google_artifact_registry_repository.ryft.location
  repository = google_artifact_registry_repository.ryft.repository_id
  role       = "roles/artifactregistry.writer"
  member     = local.member["ryft-deployer"]
}
