# D270: N2R runs in the existing API and tick. Only catalog imports need a separate job.
variable "nutrition_etl" {
  description = "Opt-in Nutrition ETL. An empty object creates the identities and secret container. Pin an image digest and a secret version to create the job."
  type = object({
    image_digest            = optional(string)
    database_secret_version = optional(number)
    schedule_enabled        = optional(bool, false)
    row_limit               = optional(number, 10000)
    full_import             = optional(bool, false)
    cloud_sql = optional(object({
      instance_connection_name = string
      database                 = string
      user                     = string
    }))
  })
  default = null

  validation {
    condition = var.nutrition_etl == null ? true : (
      (var.nutrition_etl.image_digest == null && var.nutrition_etl.database_secret_version == null && !var.nutrition_etl.schedule_enabled) || (
        can(regex("^sha256:[a-f0-9]{64}$", var.nutrition_etl.image_digest)) &&
        try(var.nutrition_etl.database_secret_version >= 1 && floor(var.nutrition_etl.database_secret_version) == var.nutrition_etl.database_secret_version, false)
      )
    )
    error_message = "Set an immutable image_digest and a positive integer database_secret_version together before creating or scheduling the job."
  }

  validation {
    condition     = var.nutrition_etl == null ? true : var.nutrition_etl.row_limit >= 1 && var.nutrition_etl.row_limit <= 5000000 && floor(var.nutrition_etl.row_limit) == var.nutrition_etl.row_limit
    error_message = "The row_limit must be an integer from 1 to 5000000. Use full_import only after validating a bounded import."
  }

  validation {
    condition = try(var.nutrition_etl.cloud_sql, null) == null ? true : (
      can(regex("^${var.project_id}:[a-z0-9-]+:[a-z0-9][a-z0-9-]*$", var.nutrition_etl.cloud_sql.instance_connection_name)) &&
      can(regex("^[a-zA-Z0-9_-]+$", var.nutrition_etl.cloud_sql.database)) &&
      can(regex("^[a-zA-Z0-9_-]+$", var.nutrition_etl.cloud_sql.user))
    )
    error_message = "Cloud SQL requires an existing instance connection name in this project and a database and user name."
  }
}

locals {
  nutrition_etl_name = "nutrition-etl-job"
  nutrition_etl_identities = var.nutrition_etl == null ? {} : {
    runtime   = "ryft-nutrition-etl"
    scheduler = "ryft-nutrition-schedule"
  }
  nutrition_etl_job       = try(var.nutrition_etl.image_digest, null) != null
  nutrition_etl_cloud_sql = try(var.nutrition_etl.cloud_sql, null)
  nutrition_etl_sa        = { for k, a in google_service_account.nutrition_etl : k => "${a.account_id}@${a.project}.iam.gserviceaccount.com" }
}

resource "google_service_account" "nutrition_etl" {
  for_each = local.nutrition_etl_identities

  project      = var.project_id
  account_id   = each.value
  display_name = "RYFT Nutrition ETL ${each.key}"
}

resource "google_secret_manager_secret" "nutrition_etl_db" {
  count = var.nutrition_etl != null ? 1 : 0

  project             = var.project_id
  secret_id           = "nutrition-etl-db"
  version_destroy_ttl = "2592000s"

  replication {
    user_managed {
      replicas {
        location = var.region
      }
    }
  }

  lifecycle {
    prevent_destroy = true
  }
}

resource "google_secret_manager_secret_iam_binding" "nutrition_etl_db" {
  count = var.nutrition_etl != null ? 1 : 0

  project   = var.project_id
  secret_id = google_secret_manager_secret.nutrition_etl_db[0].secret_id
  role      = "roles/secretmanager.secretAccessor"
  members   = ["serviceAccount:${local.nutrition_etl_sa["runtime"]}"]
}

resource "google_project_service" "nutrition_cloud_sql" {
  count = local.nutrition_etl_cloud_sql != null ? 1 : 0

  project            = var.project_id
  service            = "sqladmin.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_iam_member" "nutrition_cloud_sql" {
  count = local.nutrition_etl_cloud_sql != null ? 1 : 0

  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${local.nutrition_etl_sa["runtime"]}"
}

resource "google_cloud_run_v2_job" "nutrition_etl" {
  count = local.nutrition_etl_job ? 1 : 0

  project             = var.project_id
  location            = var.region
  name                = local.nutrition_etl_name
  deletion_protection = true

  template {
    task_count  = 1
    parallelism = 1

    template {
      service_account       = local.nutrition_etl_sa["runtime"]
      execution_environment = "EXECUTION_ENVIRONMENT_GEN2"
      timeout               = "1800s"
      max_retries           = 1

      containers {
        image = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.ryft.repository_id}/${local.nutrition_etl_name}@${var.nutrition_etl.image_digest}"
        args = concat(
          ["--source", "all", "--write"],
          var.nutrition_etl.full_import ? ["--full"] : ["--limit", tostring(var.nutrition_etl.row_limit)],
        )

        resources {
          limits = { cpu = "2", memory = "4Gi" }
        }

        env {
          name = local.nutrition_etl_cloud_sql == null ? "DATABASE_URL" : "PGPASSWORD"
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.nutrition_etl_db[0].secret_id
              version = tostring(var.nutrition_etl.database_secret_version)
            }
          }
        }

        dynamic "env" {
          for_each = local.nutrition_etl_cloud_sql == null ? {} : {
            INSTANCE_CONNECTION_NAME = local.nutrition_etl_cloud_sql.instance_connection_name
            PGDATABASE               = local.nutrition_etl_cloud_sql.database
            PGUSER                   = local.nutrition_etl_cloud_sql.user
          }
          content {
            name  = env.key
            value = env.value
          }
        }

        dynamic "volume_mounts" {
          for_each = local.nutrition_etl_cloud_sql[*]
          content {
            name       = "cloudsql"
            mount_path = "/cloudsql"
          }
        }
      }

      dynamic "volumes" {
        for_each = local.nutrition_etl_cloud_sql[*]
        content {
          name = "cloudsql"
          cloud_sql_instance {
            instances = [volumes.value.instance_connection_name]
          }
        }
      }
    }
  }

  depends_on = [
    google_project_service.keep["run.googleapis.com"],
    google_project_service.nutrition_cloud_sql,
    google_project_iam_member.nutrition_cloud_sql,
    google_secret_manager_secret_iam_binding.nutrition_etl_db,
  ]
}

resource "google_cloud_run_v2_job_iam_member" "nutrition_scheduler" {
  count = local.nutrition_etl_job ? 1 : 0

  project  = var.project_id
  location = google_cloud_run_v2_job.nutrition_etl[0].location
  name     = google_cloud_run_v2_job.nutrition_etl[0].name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${local.nutrition_etl_sa["scheduler"]}"
}

resource "google_cloud_scheduler_job" "nutrition_etl" {
  count = local.nutrition_etl_job ? 1 : 0

  project          = var.project_id
  region           = var.region
  name             = local.nutrition_etl_name
  schedule         = "0 4 1 * *"
  time_zone        = "Etc/UTC"
  paused           = !var.nutrition_etl.schedule_enabled || !local.tick
  attempt_deadline = "60s"

  retry_config {
    retry_count = 0
  }

  http_target {
    http_method = "POST"
    uri         = "https://run.googleapis.com/v2/projects/${var.project_id}/locations/${var.region}/jobs/${google_cloud_run_v2_job.nutrition_etl[0].name}:run"
    oauth_token {
      service_account_email = local.nutrition_etl_sa["scheduler"]
      scope                 = "https://www.googleapis.com/auth/cloud-platform"
    }
  }

  depends_on = [google_cloud_run_v2_job_iam_member.nutrition_scheduler, google_project_service_identity.agent]
}
