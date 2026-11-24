mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = { disabled = true }
  }
}
mock_provider "google-beta" {}

override_data {
  target = data.google_project.this
  values = { number = "123456789012" }
}

override_resource {
  target          = google_cloud_run_domain_mapping.host
  override_during = plan
  values = {
    status = [{
      conditions       = [{ type = "Ready", status = "True", reason = "", message = "" }]
      resource_records = [{ name = "", type = "CNAME", rrdata = "ghs.googlehosted.com." }]
    }]
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
  alert_app_channel          = null
  edge                       = "off"
  serve                      = true
  public_invokers            = true
}

run "nutrition_off" {
  command = plan

  assert {
    condition = (
      length(google_service_account.nutrition_etl) == 0 &&
      length(google_secret_manager_secret.nutrition_etl_db) == 0 &&
      length(google_cloud_run_v2_job.nutrition_etl) == 0 &&
      length(google_cloud_scheduler_job.nutrition_etl) == 0 &&
      length(google_project_service.nutrition_cloud_sql) == 0
    )
    error_message = "The default root has no additional nutrition resources."
  }
}

run "nutrition_bootstrap" {
  command = plan

  variables {
    nutrition_etl = {}
  }

  assert {
    condition = (
      toset(keys(google_service_account.nutrition_etl)) == toset(["runtime", "scheduler"]) &&
      google_secret_manager_secret.nutrition_etl_db[0].secret_id == "nutrition-etl-db" &&
      google_secret_manager_secret_iam_binding.nutrition_etl_db[0].role == "roles/secretmanager.secretAccessor" &&
      google_secret_manager_secret_iam_binding.nutrition_etl_db[0].members == toset(["serviceAccount:ryft-nutrition-etl@ryftlab-prod.iam.gserviceaccount.com"]) &&
      length(google_cloud_run_v2_job.nutrition_etl) == 0 &&
      length(google_cloud_scheduler_job.nutrition_etl) == 0
    )
    error_message = "Bootstrap supplies only the separate identities and the runtime's scoped secret container."
  }
}

run "nutrition_bounded_with_existing_database" {
  command = plan

  variables {
    nutrition_etl = {
      image_digest            = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
      database_secret_version = 2
      row_limit               = 1000
    }
  }

  assert {
    condition = (
      google_cloud_run_v2_job.nutrition_etl[0].deletion_protection &&
      google_cloud_run_v2_job.nutrition_etl[0].template[0].task_count == 1 &&
      google_cloud_run_v2_job.nutrition_etl[0].template[0].parallelism == 1 &&
      alltrue([for t in google_cloud_run_v2_job.nutrition_etl[0].template[0].template : (
        t.timeout == "1800s" && t.max_retries == 1 && t.service_account == "ryft-nutrition-etl@ryftlab-prod.iam.gserviceaccount.com" &&
        length(t.volumes) == 0 && length(t.vpc_access) == 0 &&
        alltrue([for c in t.containers : (
          c.image == "europe-west1-docker.pkg.dev/ryftlab-prod/ryft/nutrition-etl-job@sha256:1111111111111111111111111111111111111111111111111111111111111111" &&
          c.args == tolist(["--source", "all", "--write", "--limit", "1000"]) &&
          c.resources[0].limits == tomap({ cpu = "2", memory = "4Gi" }) &&
          [for e in c.env : [e.name, e.value_source[0].secret_key_ref[0].secret, e.value_source[0].secret_key_ref[0].version]] == [["DATABASE_URL", "nutrition-etl-db", "2"]]
        )])
      )]) &&
      length(google_project_iam_member.nutrition_cloud_sql) == 0
    )
    error_message = "The bounded job pins its image and credential, uses existing PostgreSQL, and has no VPC or persistent volume."
  }

  assert {
    condition = (
      google_cloud_scheduler_job.nutrition_etl[0].paused &&
      google_cloud_scheduler_job.nutrition_etl[0].schedule == "0 4 1 * *" &&
      google_cloud_scheduler_job.nutrition_etl[0].time_zone == "Etc/UTC" &&
      google_cloud_scheduler_job.nutrition_etl[0].retry_config[0].retry_count == 0 &&
      google_cloud_scheduler_job.nutrition_etl[0].http_target[0].http_method == "POST" &&
      google_cloud_scheduler_job.nutrition_etl[0].http_target[0].uri == "https://run.googleapis.com/v2/projects/ryftlab-prod/locations/europe-west1/jobs/nutrition-etl-job:run" &&
      google_cloud_scheduler_job.nutrition_etl[0].http_target[0].oauth_token[0].service_account_email == "ryft-nutrition-schedule@ryftlab-prod.iam.gserviceaccount.com" &&
      google_cloud_run_v2_job_iam_member.nutrition_scheduler[0].member == "serviceAccount:ryft-nutrition-schedule@ryftlab-prod.iam.gserviceaccount.com" &&
      google_cloud_run_v2_job_iam_member.nutrition_scheduler[0].role == "roles/run.invoker" &&
      !contains(keys(google_service_account_iam_member.deployer_act_as), "ryft-nutrition-etl")
    )
    error_message = "Monthly OAuth invocation is paused by default and scoped to the scheduler identity and this job."
  }
}

run "nutrition_full_with_cloud_sql_socket" {
  command = plan

  variables {
    nutrition_etl = {
      image_digest            = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
      database_secret_version = 3
      schedule_enabled        = true
      full_import             = true
      cloud_sql = {
        instance_connection_name = "ryftlab-prod:europe-west1:existing-db"
        database                 = "ryft"
        user                     = "nutrition_etl_login"
      }
    }
  }

  assert {
    condition = (
      !google_cloud_scheduler_job.nutrition_etl[0].paused &&
      google_project_service.nutrition_cloud_sql[0].service == "sqladmin.googleapis.com" &&
      google_project_iam_member.nutrition_cloud_sql[0].role == "roles/cloudsql.client" &&
      google_project_iam_member.nutrition_cloud_sql[0].member == "serviceAccount:ryft-nutrition-etl@ryftlab-prod.iam.gserviceaccount.com" &&
      alltrue([for t in google_cloud_run_v2_job.nutrition_etl[0].template[0].template : (
        t.volumes[0].cloud_sql_instance[0].instances == tolist(["ryftlab-prod:europe-west1:existing-db"]) &&
        alltrue([for c in t.containers : (
          c.args == tolist(["--source", "all", "--write", "--full"]) && c.volume_mounts[0].mount_path == "/cloudsql" &&
          toset([for e in c.env : e.name]) == toset(["INSTANCE_CONNECTION_NAME", "PGDATABASE", "PGPASSWORD", "PGUSER"]) &&
          [for e in c.env : e.value_source[0].secret_key_ref[0].version if e.name == "PGPASSWORD"] == ["3"]
        )])
      )])
    )
    error_message = "Explicit enablement schedules the full import with the existing Cloud SQL socket and a scoped password."
  }
}

run "nutrition_scheduler_follows_stop_line" {
  command = plan

  variables {
    public_invokers = false
    nutrition_etl = {
      image_digest            = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
      database_secret_version = 1
      schedule_enabled        = true
    }
  }

  assert {
    condition     = google_cloud_scheduler_job.nutrition_etl[0].paused
    error_message = "The existing stop line pauses Nutrition Scheduler."
  }
}

run "nutrition_requires_both_pins" {
  command = plan

  variables {
    nutrition_etl = { schedule_enabled = true }
  }

  expect_failures = [var.nutrition_etl]
}
