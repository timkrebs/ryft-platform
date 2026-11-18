# Logs stay at Google, in the EU, for 14 days: what the privacy text promises (design 6, D224).
# Outside this bucket: _Required (audit logs, Google's fixed 400 days).
resource "google_logging_project_bucket_config" "ryft_logs" {
  project        = var.project_id
  location       = var.region
  bucket_id      = "ryft-logs"
  retention_days = 14
  description    = "Every log of ryftlab-prod, 14 days (infra/gcp)"
}

# The edge's exclusions (D242), from edge = built on: name => what it drops, in Logging's query
# language (lines are ANDed). What no sink takes is stored nowhere: _Default is off (checks.tf), and
# _Required takes audit logs only. So with the edge a client's IP address is stored only with a
# request Cloud Armor denied or a preview rule flagged, or one that failed with a 5xx on api or ingest
# (Cloud Run's request log, below), here for 14 days.
locals {
  log_exclusions = {
    # The load balancer logs every request (sample 1.0, edge.tf) with the client's IP, the URL and the
    # user agent. Kept: Cloud Armor's denials (a WAF rule enforced, the rate rule's 429 and ban:
    # enforcedSecurityPolicy.outcome DENY) and preview matches (previewSecurityPolicy, present only when
    # a preview rule matched; outcome DENY for a deny rule). Everything else goes, the redirects to
    # the site and to HTTPS included.
    "lb-requests-unflagged" = {
      description = "The edge's request logs but Cloud Armor's denials and preview matches (infra/gcp, D242)"
      query = join("\n", [
        "resource.type=\"http_load_balancer\"",
        "NOT (jsonPayload.enforcedSecurityPolicy.outcome=\"DENY\" OR jsonPayload.previewSecurityPolicy.outcome=\"DENY\")",
      ])
    }

    # Cloud Run's own request log of api and ingest: every caller's IP, URL and user agent. Without
    # this exclusion an IP would be kept for every request to them. Kept: their 5xx, the only record
    # of a failure Cloud Run makes itself (a request timeout, no instance), which the services' access
    # line never sees and the 5xx alert's text sends you to (alerts.tf: httpRequest.status>=500). An
    # entry without a status is kept too (the comparison fails). scoring's and notify's request logs
    # stay whole: their callers are Pub/Sub's pushes, Google's addresses, and the event backlog alert
    # reads their 429s. For the user's confirmation (D242).
    "run-requests" = {
      description = "Cloud Run's request log of api and ingest below 5xx, with every caller's IP (infra/gcp, D242)"
      query = join("\n", [
        "logName=\"projects/${var.project_id}/logs/run.googleapis.com%2Frequests\"",
        "resource.labels.service_name=(\"api\" OR \"ingest\")",
        "httpRequest.status<500",
      ])
    }
  }
}

# Everything into ryft-logs (no inclusion filter), from edge = built on less the two exclusions above.
# The bucket is in this project, so the sink's writer identity needs no grant.
resource "google_logging_project_sink" "ryft_logs" {
  project                = var.project_id
  name                   = "ryft-logs"
  destination            = "logging.googleapis.com/projects/${var.project_id}/locations/${google_logging_project_bucket_config.ryft_logs.location}/buckets/${google_logging_project_bucket_config.ryft_logs.bucket_id}"
  description            = "Every log into the regional bucket ryft-logs (infra/gcp)"
  unique_writer_identity = true

  dynamic "exclusions" {
    for_each = local.edge_built ? local.log_exclusions : {}

    content {
      name        = exclusions.key
      description = exclusions.value.description
      filter      = exclusions.value.query
    }
  }
}

# The global _Default sink (30 days, global bucket) is switched off by hand once, after apply 1, with
#   gcloud logging sinks update _Default --disabled --project=ryftlab-prod
# (runbook step 4). Not an import block: it would make every `terraform test` run import through the
# mocked provider, and a destroy of this root would delete the project's _Default sink. checks.tf warns
# on every plan while it is still on.
