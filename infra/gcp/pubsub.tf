# The topology (design 2.1, D218) from pubsub.auto.tfvars.json, which cmd/pubsub-topology writes. One
# topic per event key, so publish rights are per key (D99's matrix). The service agents' bindings
# are added here and are not in the file.
locals {
  tick_topic = "ryft-tick"

  dlq_topics = toset([for s in var.pubsub_push_subscriptions : s.dead_letter_topic if s.dead_letter_topic != ""])

  # Topic => every member of roles/pubsub.publisher on it: the file's services, the Pub/Sub agent on a
  # dead-letter topic (it forwards there) and the Scheduler agent on the tick topic.
  topic_publishers = {
    for t, v in var.pubsub_topics : t => sort(concat(
      [for svc in v.publishers : local.member["ryft-${svc}"]],
      contains(local.dlq_topics, t) ? [local.pubsub_agent] : [],
      t == local.tick_topic ? [local.scheduler_agent] : [],
    ))
  }

  dead_lettered = { for k, s in var.pubsub_push_subscriptions : k => s if s.dead_letter_topic != "" }

  # Constants (D218): the allowed retry range, and 20 delivery attempts before the DLQ.
  retry_minimum_backoff = "10s"
  retry_maximum_backoff = "600s"
  max_delivery_attempts = 20
}

resource "google_pubsub_topic" "this" {
  for_each = var.pubsub_topics

  project = var.project_id
  name    = each.key

  # Messages are stored in the region only. enforce_in_transit stays off (ticket B): it refuses any
  # publish or pull the global endpoint routes to another region, which would also catch the Scheduler's
  # publish and an operator's `gcloud pubsub` from a laptop, and the storage policy already keeps the
  # messages in europe-west1 at rest. The services publish to the regional endpoint (PUBSUB_ENDPOINT).
  message_storage_policy {
    allowed_persistence_regions = [var.region]
  }
}

# Authoritative for the role on each topic: nobody publishes who is not listed.
resource "google_pubsub_topic_iam_binding" "publisher" {
  for_each = { for t, m in local.topic_publishers : t => m if length(m) > 0 }

  project = var.project_id
  topic   = google_pubsub_topic.this[each.key].name
  role    = "roles/pubsub.publisher"
  members = each.value

  depends_on = [google_project_service_identity.agent]
}

resource "google_pubsub_subscription" "push" {
  for_each = var.pubsub_push_subscriptions

  project = var.project_id
  name    = each.key
  topic   = google_pubsub_topic.this[each.value.topic].name

  ack_deadline_seconds       = each.value.ack_deadline_seconds
  message_retention_duration = "${each.value.message_retention_seconds}s"
  enable_message_ordering    = each.value.ordered

  # The run.app URL, never a custom domain: only that admits Pub/Sub to an ingress-internal service.
  # The audience is the URL without the path, set explicitly, because the services check aud against
  # their own PUSH_AUDIENCE (design 2.2).
  push_config {
    push_endpoint = "${local.run_url[each.value.service]}${each.value.path}"

    oidc_token {
      service_account_email = local.sa["ryft-push"]
      audience              = local.run_url[each.value.service]
    }
  }

  retry_policy {
    minimum_backoff = local.retry_minimum_backoff
    maximum_backoff = local.retry_maximum_backoff
  }

  dynamic "dead_letter_policy" {
    for_each = each.value.dead_letter_topic == "" ? [] : [each.value.dead_letter_topic]

    content {
      dead_letter_topic     = "projects/${var.project_id}/topics/${google_pubsub_topic.this[dead_letter_policy.value].name}"
      max_delivery_attempts = local.max_delivery_attempts
    }
  }

  # The default deletes a subscription after 31 idle days (ticket B, design fix).
  expiration_policy {
    ttl = ""
  }
}

# Delivery attempts are counted, and letters forwarded, only when the Pub/Sub agent may pull from the
# source subscription (and publish to the DLQ topic, above).
resource "google_pubsub_subscription_iam_binding" "dead_letter_source" {
  for_each = local.dead_lettered

  project      = var.project_id
  subscription = google_pubsub_subscription.push[each.key].name
  role         = "roles/pubsub.subscriber"
  members      = [local.pubsub_agent]

  depends_on = [google_project_service_identity.agent]
}

# The DLQs' pull subscriptions: 14 days, read only by the operator (scripts/dlq.sh). 60 s to ack, so a
# replay (pull, republish, ack) fits in one lease.
resource "google_pubsub_subscription" "hold" {
  for_each = var.pubsub_hold_subscriptions

  project = var.project_id
  name    = each.key
  topic   = google_pubsub_topic.this[each.value.topic].name

  ack_deadline_seconds       = 60
  message_retention_duration = "${each.value.message_retention_seconds}s"

  expiration_policy {
    ttl = ""
  }
}
