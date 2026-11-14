# Logs stay at Google, in the EU, for 14 days: what the privacy text promises (design 6, D224).
# Outside this bucket: _Required (audit logs, Google's fixed 400 days).
resource "google_logging_project_bucket_config" "ryft_logs" {
  project        = var.project_id
  location       = var.region
  bucket_id      = "ryft-logs"
  retention_days = 14
  description    = "Every log of ryftlab-prod, 14 days (infra/gcp)"
}

# Everything (no filter) into ryft-logs. The bucket is in this project, so the sink's writer identity
# needs no grant.
resource "google_logging_project_sink" "ryft_logs" {
  project                = var.project_id
  name                   = "ryft-logs"
  destination            = "logging.googleapis.com/projects/${var.project_id}/locations/${google_logging_project_bucket_config.ryft_logs.location}/buckets/${google_logging_project_bucket_config.ryft_logs.bucket_id}"
  description            = "Every log into the regional bucket ryft-logs (infra/gcp)"
  unique_writer_identity = true
}

# The global _Default sink (30 days, global bucket) is switched off by hand once, after apply 1, with
#   gcloud logging sinks update _Default --disabled --project=ryftlab-prod
# (runbook step 4). Not an import block: it would make every `terraform test` run import through the
# mocked provider, and a destroy of this root would delete the project's _Default sink. checks.tf warns
# on every plan while it is still on.
