# Every timer of the services is one job (D219): every 15 minutes it publishes {} with ryft-key=tick to
# ryft-tick, whose subscriptions tick-<svc> push it to POST /internal/tick (pubsub.tf). Paused until
# apply 2, and again whenever the stop line is in force (public_invokers = false).
resource "google_cloud_scheduler_job" "tick" {
  project     = var.project_id
  region      = var.region
  name        = "ryft-tick"
  description = "POST /internal/tick on every service through the topic ryft-tick (infra/gcp, D219)"
  schedule    = "*/15 * * * *"
  time_zone   = "Etc/UTC"
  paused      = !local.tick

  pubsub_target {
    topic_name = "projects/${var.project_id}/topics/${google_pubsub_topic.this[local.tick_topic].name}"
    data       = base64encode("{}")
    attributes = {
      "ryft-key" = "tick"
    }
  }

  depends_on = [google_pubsub_topic_iam_binding.publisher]
}
