# Plan-only; every provider is mocked, nothing is created. The topology is the committed
# pubsub.auto.tfvars.json (terraform test loads the root's *.auto.tfvars), the gates are gates.auto.tfvars.
# The project number is overridden, so every run.app URL and service agent below is exact.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}
mock_provider "grafana" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
}

# D99's matrix, per topic: exactly these publishers, the agents added by Terraform.
run "publisher_matrix_per_topic" {
  command = plan

  assert {
    condition     = length(google_pubsub_topic.this) == 9 && length(google_pubsub_subscription.push) == 10 && length(google_pubsub_subscription.hold) == 2
    error_message = "the committed topology has 9 topics, 10 push and 2 hold subscriptions"
  }

  assert {
    condition = (
      length(google_pubsub_topic_iam_binding.publisher) == 9 &&
      alltrue([for b in google_pubsub_topic_iam_binding.publisher : b.role == "roles/pubsub.publisher"])
    )
    error_message = "one authoritative publisher binding per topic"
  }

  assert {
    condition = google_pubsub_topic_iam_binding.publisher["ryft-ingested"].members == toset([
      "serviceAccount:ryft-api@ryftlab-prod.iam.gserviceaccount.com",
      "serviceAccount:ryft-ingest@ryftlab-prod.iam.gserviceaccount.com",
    ])
    error_message = "ryft-ingested: api and ingest publish"
  }

  assert {
    condition     = google_pubsub_topic_iam_binding.publisher["ryft-session-completed"].members == toset(["serviceAccount:ryft-api@ryftlab-prod.iam.gserviceaccount.com"])
    error_message = "ryft-session-completed: api alone publishes"
  }

  assert {
    condition     = !contains(google_pubsub_topic_iam_binding.publisher["ryft-session-completed"].members, "serviceAccount:ryft-ingest@ryftlab-prod.iam.gserviceaccount.com")
    error_message = "D99: ingest cannot publish session.completed"
  }

  assert {
    condition = alltrue([
      for t in ["ryft-scored", "ryft-session-scored", "ryft-refresh"] :
      google_pubsub_topic_iam_binding.publisher[t].members == toset(["serviceAccount:ryft-scoring@ryftlab-prod.iam.gserviceaccount.com"])
    ])
    error_message = "ryft-scored, ryft-session-scored and ryft-refresh: scoring alone publishes"
  }

  assert {
    condition     = google_pubsub_topic_iam_binding.publisher["ryft-notify-wait"].members == toset(["serviceAccount:ryft-notify@ryftlab-prod.iam.gserviceaccount.com"])
    error_message = "ryft-notify-wait: notify alone publishes (its deferrals)"
  }

  assert {
    condition     = google_pubsub_topic_iam_binding.publisher["ryft-tick"].members == toset(["serviceAccount:service-123456789012@gcp-sa-cloudscheduler.iam.gserviceaccount.com"])
    error_message = "ryft-tick: only the Cloud Scheduler service agent publishes"
  }

  assert {
    condition = (
      google_pubsub_topic_iam_binding.publisher["ryft-dlq-scoring"].members == toset([
        "serviceAccount:ryft-scoring@ryftlab-prod.iam.gserviceaccount.com",
        "serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com",
      ]) &&
      google_pubsub_topic_iam_binding.publisher["ryft-dlq-notify"].members == toset([
        "serviceAccount:ryft-notify@ryftlab-prod.iam.gserviceaccount.com",
        "serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com",
      ])
    )
    error_message = "a DLQ topic: its own consumer (for bad events) and the Pub/Sub agent (forwarding), nobody else"
  }

  assert {
    condition = alltrue([
      for t in google_pubsub_topic.this :
      toset(t.message_storage_policy[0].allowed_persistence_regions) == toset(["europe-west1"])
    ])
    error_message = "every topic stores its messages in europe-west1 only"
  }

  # Only authoritative bindings on topics and subscriptions: a *_iam_member next to them would fight them.
  assert {
    condition     = length(regexall("google_pubsub_(topic|subscription)_iam_(member|policy)", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "Pub/Sub IAM is google_pubsub_*_iam_binding only"
  }
}

run "push_subscriptions" {
  command = plan

  # The audience is the run.app URL without the path, and the endpoint is that URL plus the path.
  assert {
    condition = alltrue([
      for k, s in google_pubsub_subscription.push :
      s.push_config[0].oidc_token[0].audience == "https://${var.pubsub_push_subscriptions[k].service}-123456789012.europe-west1.run.app" &&
      s.push_config[0].push_endpoint == "${s.push_config[0].oidc_token[0].audience}${var.pubsub_push_subscriptions[k].path}" &&
      !strcontains(trimprefix(s.push_config[0].oidc_token[0].audience, "https://"), "/")
    ])
    error_message = "push audience = the service's run.app URL without the path; endpoint = audience + path"
  }

  assert {
    condition = (
      google_pubsub_subscription.push["scoring-ingested"].push_config[0].push_endpoint == "https://scoring-123456789012.europe-west1.run.app/push/ingested" &&
      google_pubsub_subscription.push["scoring-ingested"].push_config[0].oidc_token[0].audience == "https://scoring-123456789012.europe-west1.run.app" &&
      google_pubsub_subscription.push["notify-session-scored"].push_config[0].push_endpoint == "https://notify-123456789012.europe-west1.run.app/push/session.scored" &&
      google_pubsub_subscription.push["tick-api"].push_config[0].push_endpoint == "https://api-123456789012.europe-west1.run.app/internal/tick"
    )
    error_message = "the endpoints and audience as ticket B's interfaces name them"
  }

  assert {
    condition = alltrue([
      for s in google_pubsub_subscription.push : s.push_config[0].oidc_token[0].service_account_email == "ryft-push@ryftlab-prod.iam.gserviceaccount.com"
    ])
    error_message = "every push and tick carries an OIDC token of ryft-push"
  }

  assert {
    condition = alltrue([
      for s in google_pubsub_subscription.push : s.retry_policy[0].minimum_backoff == "10s" && s.retry_policy[0].maximum_backoff == "600s"
    ])
    error_message = "retry policy 10 s to 600 s on every push subscription (D218)"
  }

  assert {
    condition = (
      google_pubsub_subscription.push["scoring-ingested"].enable_message_ordering &&
      google_pubsub_subscription.push["scoring-session-completed"].enable_message_ordering &&
      length([for k, s in google_pubsub_subscription.push : k if s.enable_message_ordering]) == 2
    )
    error_message = "ordering on scoring's two subscriptions only"
  }

  assert {
    condition = alltrue([
      for k, s in google_pubsub_subscription.push :
      startswith(k, "tick-") ? length(s.dead_letter_policy) == 0 : (
        length(s.dead_letter_policy) == 1 &&
        s.dead_letter_policy[0].max_delivery_attempts == 20 &&
        s.dead_letter_policy[0].dead_letter_topic == "projects/ryftlab-prod/topics/ryft-dlq-${split("-", k)[0]}"
      )
    ])
    error_message = "every event subscription dead-letters to its consumer's DLQ after 20 attempts; ticks have none"
  }

  assert {
    condition = alltrue([
      for k, s in google_pubsub_subscription.push :
      startswith(k, "tick-") ? (s.message_retention_duration == "600s" && s.ack_deadline_seconds == 600) : true
    ])
    error_message = "ticks keep messages 10 minutes and have 600 s to answer"
  }

  assert {
    condition = (
      google_pubsub_subscription.push["scoring-ingested"].ack_deadline_seconds == 300 &&
      google_pubsub_subscription.push["notify-scored"].ack_deadline_seconds == 60
    )
    error_message = "ack deadlines: scoring 300 s, notify 60 s (design 2.2)"
  }

  assert {
    condition = alltrue(concat(
      [for s in google_pubsub_subscription.push : s.expiration_policy[0].ttl == ""],
      [for s in google_pubsub_subscription.hold : s.expiration_policy[0].ttl == ""],
    ))
    error_message = "no subscription expires (the default deletes one after 31 idle days)"
  }

  assert {
    condition = (
      google_pubsub_subscription.hold["dlq-scoring-hold"].topic == "ryft-dlq-scoring" &&
      google_pubsub_subscription.hold["dlq-notify-hold"].topic == "ryft-dlq-notify" &&
      alltrue([for s in google_pubsub_subscription.hold : s.message_retention_duration == "1209600s" && length(s.push_config) == 0])
    )
    error_message = "the DLQs' hold subscriptions are pull and keep letters 14 days"
  }

  assert {
    condition = (
      toset(keys(google_pubsub_subscription_iam_binding.dead_letter_source)) == toset([
        "notify-refresh", "notify-scored", "notify-session-scored", "notify-wait", "scoring-ingested", "scoring-session-completed",
      ]) &&
      alltrue([
        for b in google_pubsub_subscription_iam_binding.dead_letter_source :
        b.role == "roles/pubsub.subscriber" && b.members == toset(["serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com"])
      ])
    )
    error_message = "the Pub/Sub agent is subscriber on each dead-lettering subscription, and nobody else"
  }

  assert {
    condition = (
      google_service_account_iam_member.pubsub_agent_token_creator.service_account_id == "projects/ryftlab-prod/serviceAccounts/ryft-push@ryftlab-prod.iam.gserviceaccount.com" &&
      google_service_account_iam_member.pubsub_agent_token_creator.role == "roles/iam.serviceAccountTokenCreator" &&
      google_service_account_iam_member.pubsub_agent_token_creator.member == "serviceAccount:service-123456789012@gcp-sa-pubsub.iam.gserviceaccount.com"
    )
    error_message = "the Pub/Sub agent mints ryft-push's tokens"
  }

  assert {
    condition     = toset(keys(google_project_service_identity.agent)) == toset(["pubsub.googleapis.com", "cloudscheduler.googleapis.com"])
    error_message = "both service agents are made to exist before they are bound"
  }
}

run "tick_job" {
  command = plan

  assert {
    condition = (
      google_cloud_scheduler_job.tick.name == "ryft-tick" &&
      google_cloud_scheduler_job.tick.region == "europe-west1" &&
      google_cloud_scheduler_job.tick.schedule == "*/15 * * * *" &&
      google_cloud_scheduler_job.tick.time_zone == "Etc/UTC" &&
      google_cloud_scheduler_job.tick.pubsub_target[0].topic_name == "projects/ryftlab-prod/topics/ryft-tick" &&
      google_cloud_scheduler_job.tick.pubsub_target[0].data == base64encode("{}") &&
      google_cloud_scheduler_job.tick.pubsub_target[0].attributes == tomap({ "ryft-key" = "tick" })
    )
    error_message = "the tick: every 15 minutes in UTC, {} with ryft-key=tick to ryft-tick"
  }

  assert {
    condition     = toset([for k, s in var.pubsub_push_subscriptions : s.service if s.topic == "ryft-tick"]) == toset(["api", "ingest", "scoring", "notify"])
    error_message = "the tick reaches all four services"
  }
}
