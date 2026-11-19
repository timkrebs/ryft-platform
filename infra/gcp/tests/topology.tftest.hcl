# The topology variables' validations, each against a small topology that breaks one rule. A run that
# replaces one collection keeps the file-level others. A validation that reads another variable runs
# only once that variable's own validations have passed (push subscriptions read the topics, hold
# subscriptions read the push subscriptions), so a run expects exactly the first collection in that
# order that breaks a rule: the ones after it are never checked, and are not listed. Plan-only, mocked.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

# Cloud Run fills in a domain mapping's status only at apply, so with serve = true checks.tf's
# domain_mappings_ready is unknown in a plan, and an unknown check fails the run. Mocked as a Ready
# mapping with the one CNAME Cloud Run returns for a subdomain (dns.tf's records read it); the per-host
# fields nothing reads stay empty. In every test file: once gates.auto.tfvars says serve = true, every
# run without its own serve plans the mappings.
override_resource {
  target          = google_cloud_run_domain_mapping.host
  override_during = plan
  values = {
    status = [{
      conditions          = [{ type = "Ready", status = "True", reason = "", message = "" }]
      mapped_route_name   = ""
      observed_generation = 1
      resource_records    = [{ name = "", type = "CNAME", rrdata = "ghs.googlehosted.com." }]
    }]
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
  # The edge's phases are tests/edge.tftest.hcl; here off, whatever gates.auto.tfvars holds.
  edge = "off"

  # A valid small topology; the runs below break one rule each.
  pubsub_topics = {
    "ryft-ingested"    = { publishers = ["ingest"] }
    "ryft-dlq-scoring" = { publishers = ["scoring"] }
    "ryft-tick"        = { publishers = [] }
  }
  pubsub_push_subscriptions = {
    "scoring-ingested" = {
      topic                     = "ryft-ingested"
      service                   = "scoring"
      path                      = "/push/ingested"
      ordered                   = true
      ack_deadline_seconds      = 300
      message_retention_seconds = 604800
      dead_letter_topic         = "ryft-dlq-scoring"
    }
    "tick-scoring" = {
      topic                     = "ryft-tick"
      service                   = "scoring"
      path                      = "/internal/tick"
      ordered                   = false
      ack_deadline_seconds      = 600
      message_retention_seconds = 600
      dead_letter_topic         = ""
    }
  }
  pubsub_hold_subscriptions = {
    "dlq-scoring-hold" = {
      topic                     = "ryft-dlq-scoring"
      message_retention_seconds = 1209600
    }
  }
}

run "a_small_valid_topology_plans" {
  command = plan

  assert {
    condition     = length(google_pubsub_topic.this) == 3 && length(google_pubsub_subscription.push) == 2 && length(google_pubsub_subscription.hold) == 1
    error_message = "the small topology plans as given"
  }
}

run "rejects_an_unknown_service" {
  command = plan

  variables {
    pubsub_push_subscriptions = {
      "scoring-ingested" = {
        topic                     = "ryft-ingested"
        service                   = "web"
        path                      = "/push/ingested"
        ordered                   = true
        ack_deadline_seconds      = 300
        message_retention_seconds = 604800
        dead_letter_topic         = "ryft-dlq-scoring"
      }
    }
  }

  expect_failures = [var.pubsub_push_subscriptions]
}

run "rejects_a_path_outside_push_and_tick" {
  command = plan

  variables {
    pubsub_push_subscriptions = {
      "scoring-ingested" = {
        topic                     = "ryft-ingested"
        service                   = "scoring"
        path                      = "/v1/sync"
        ordered                   = true
        ack_deadline_seconds      = 300
        message_retention_seconds = 604800
        dead_letter_topic         = "ryft-dlq-scoring"
      }
    }
  }

  expect_failures = [var.pubsub_push_subscriptions]
}

run "rejects_a_tick_on_a_push_path" {
  command = plan

  variables {
    pubsub_push_subscriptions = {
      "scoring-ingested" = {
        topic                     = "ryft-ingested"
        service                   = "scoring"
        path                      = "/push/ingested"
        ordered                   = true
        ack_deadline_seconds      = 300
        message_retention_seconds = 604800
        dead_letter_topic         = "ryft-dlq-scoring"
      }
      "tick-scoring" = {
        topic                     = "ryft-tick"
        service                   = "scoring"
        path                      = "/push/tick"
        ordered                   = false
        ack_deadline_seconds      = 600
        message_retention_seconds = 600
        dead_letter_topic         = ""
      }
    }
  }

  expect_failures = [var.pubsub_push_subscriptions]
}

# The DLQ is the source topic itself: refused. The hold, now on no DLQ, is never checked (see the top).
run "rejects_a_dead_letter_topic_equal_to_its_source" {
  command = plan

  variables {
    pubsub_push_subscriptions = {
      "scoring-ingested" = {
        topic                     = "ryft-ingested"
        service                   = "scoring"
        path                      = "/push/ingested"
        ordered                   = true
        ack_deadline_seconds      = 300
        message_retention_seconds = 604800
        dead_letter_topic         = "ryft-ingested"
      }
    }
  }

  expect_failures = [var.pubsub_push_subscriptions]
}

run "rejects_a_dead_letter_topic_not_in_the_map" {
  command = plan

  variables {
    pubsub_push_subscriptions = {
      "scoring-ingested" = {
        topic                     = "ryft-ingested"
        service                   = "scoring"
        path                      = "/push/ingested"
        ordered                   = true
        ack_deadline_seconds      = 300
        message_retention_seconds = 604800
        dead_letter_topic         = "ryft-dlq-nope"
      }
    }
  }

  expect_failures = [var.pubsub_push_subscriptions]
}

run "rejects_an_event_subscription_without_a_dead_letter_topic" {
  command = plan

  variables {
    pubsub_push_subscriptions = {
      "scoring-ingested" = {
        topic                     = "ryft-ingested"
        service                   = "scoring"
        path                      = "/push/ingested"
        ordered                   = true
        ack_deadline_seconds      = 300
        message_retention_seconds = 604800
        dead_letter_topic         = ""
      }
    }
  }

  expect_failures = [var.pubsub_push_subscriptions]
}

run "rejects_a_hold_subscription_on_a_topic_that_is_no_dlq" {
  command = plan

  variables {
    pubsub_hold_subscriptions = {
      "dlq-scoring-hold" = {
        topic                     = "ryft-ingested"
        message_retention_seconds = 1209600
      }
    }
  }

  expect_failures = [var.pubsub_hold_subscriptions]
}

# Letters published to a topic without a subscription are lost.
run "rejects_a_dlq_without_a_hold_subscription" {
  command = plan

  variables {
    pubsub_hold_subscriptions = {}
  }

  expect_failures = [var.pubsub_hold_subscriptions]
}

run "rejects_a_publisher_that_is_no_service" {
  command = plan

  variables {
    pubsub_topics = {
      "ryft-ingested"    = { publishers = ["ingest", "migrate"] }
      "ryft-dlq-scoring" = { publishers = ["scoring"] }
      "ryft-tick"        = { publishers = [] }
    }
  }

  expect_failures = [var.pubsub_topics]
}

run "rejects_a_service_publishing_ticks" {
  command = plan

  variables {
    pubsub_topics = {
      "ryft-ingested"    = { publishers = ["ingest"] }
      "ryft-dlq-scoring" = { publishers = ["scoring"] }
      "ryft-tick"        = { publishers = ["api"] }
    }
  }

  expect_failures = [var.pubsub_topics]
}
