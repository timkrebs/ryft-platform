# Cloud Monitoring's alert policies (D236, D244): six conditions, one policy each, all to both channels.
# The cap is six (tests/monitoring.tftest.hcl): every condition has a monthly price (D236 -> Cost, D244
# -> The price), and a seventh needs a decision, not a line here. The sixth was the user's (2026-11-19);
# the direct connections' notices joined it rather than adding a seventh (D257).
#
# Gated: nothing in this file exists until alert_app_channel is set (local.alerting, runbook step
# 11.1). Then the email channel is read by its display name: none, or two with that name, fails the
# plan (step 11.2's stop rule), and so does an app channel that is the email channel (D237). The
# channels themselves are the user's, made by hand in the console: an email to ryft-timkrebs and the
# Google Cloud app's device, which only the console can pick. The app's comes in by its resource name
# (variables.tf), since no lookup by display name can find it (D237).
#
# Queries are PromQL on Google's metric names, the threshold part of the query (any series it returns is
# an open incident). Condition 1 is a classic threshold: check_passed is a BOOL, which PromQL can't count
# per location without a cast. Condition 6 is one too: a single feedback is one point of a sparse DELTA
# counter, and increase() over one was never verified (D236). First looks: docs/OPERATIONS.md -> GCP ->
# When an alert fires.

data "google_monitoring_notification_channel" "email" {
  count = local.alerting ? 1 : 0

  project      = var.project_id
  type         = "email"
  display_name = "ryft-timkrebs"

  lifecycle {
    # The app channel must be another channel than this one, or every alert goes to the email twice
    # and to no app (D237). By the channel number, the name's last segment: projects/<id> and
    # projects/<number> are the same project, so the whole names may differ for the same channel.
    postcondition {
      condition     = reverse(split("/", self.name))[0] != reverse(split("/", var.alert_app_channel))[0]
      error_message = "alert_app_channel is the email channel ryft-timkrebs (${self.name}). It must be the Google Cloud app's channel: the other entry in the notificationChannels of the policy you attached the device to (docs/OPERATIONS.md -> GCP, step 11.1)."
    }
  }
}

locals {
  # Email first, then the app, in every policy.
  alert_channels = concat(
    data.google_monitoring_notification_channel.email[*].name,
    local.alerting ? [var.alert_app_channel] : [],
  )

  alert_look = "docs/OPERATIONS.md -> GCP -> When an alert fires"

  # Condition 2 to 5. Name => display name, severity, the PromQL, how often it is evaluated and how long
  # it must hold, and the first look. Evaluated every 5 minutes but for the 5xx ratio (every minute): each
  # evaluation's series count towards the query price (D236 -> Cost), and none of the others moves faster.
  promql_policies = merge(
    {
      # More than 2 % of api's or ingest's requests answer 5xx for 10 minutes, Cloud Run's own 5xx (no
      # instance, a timeout) included, and at least 3 of them: at night the uptime checks are most of the
      # traffic, and one failed request in six is no outage.
      "http-5xx" = {
        name     = "RYFT · 5xx over 2 % (api, ingest)"
        severity = "CRITICAL"
        query    = <<-EOT
          (
            sum by (service_name) (rate(run_googleapis_com:request_count{${local.run}, service_name=~"api|ingest", response_code_class="5xx"}[10m]))
              / sum by (service_name) (rate(run_googleapis_com:request_count{${local.run}, service_name=~"api|ingest"}[10m]))
          ) > 0.02
          and on (service_name)
          sum by (service_name) (increase(run_googleapis_com:request_count{${local.run}, service_name=~"api|ingest", response_code_class="5xx"}[10m])) >= 3
        EOT
        every    = "60s"
        hold     = "600s"
        look     = "Logs Explorer: resource.type=\"cloud_run_revision\" resource.labels.service_name=\"<service>\" httpRequest.status>=500; then the service's ERROR lines (dashboard RYFT · Overview). On ingest, 503s on /connect/ are the hook's busy or a database error (dashboard RYFT · Connections; docs/OPERATIONS.md -> Direct connections -> The hook is busy or slow)."
      }

      # A letter waits in a DLQ hold subscription: an event a consumer failed on 20 times
      # (local.max_delivery_attempts, pubsub.tf).
      "dead-letters" = {
        name     = "RYFT · Dead letters held"
        severity = "WARNING"
        query    = <<-EOT
          max by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages{${local.pubsub}, subscription_id=~"${local.dlq_holds}"}) > 0
        EOT
        every    = "300s"
        hold     = "300s"
        look     = "scripts/dlq.sh --project ${var.project_id} list; the last error is in the consumer's log, keyed by messageId. Replay or purge (docs/OPERATIONS.md -> GCP -> Dead letters)."
      }

      # An event subscription's oldest unacked event is over 15 minutes old: a consumer that is down, or
      # fails every push, while its dead-letter attempts are still running out (scoring and notify are
      # the consumers today; condition 4 fires only after).
      "push-backlog" = {
        name     = "RYFT · Event backlog over 15 minutes"
        severity = "WARNING"
        query    = <<-EOT
          max by (subscription_id) (pubsub_googleapis_com:subscription_oldest_unacked_message_age{${local.pubsub}, subscription_id=~"${local.event_subscriptions}"}) > 900
        EOT
        every    = "300s"
        hold     = "300s"
        look     = "the consumer's request log (429, 5xx), its max instances, then Dead letters held."
      }
    },
    # A service has had no tick with result ok for 45 minutes (three ticks, every 15 minutes): the tick
    # is not delivered, or every one fails (outbox, the purges, the reconcile, a database that is down).
    # Every service by name, so a service with no tick line at all is in it too (an absent series). Only
    # while the tick runs: paused (serve = false, or the stop line), it would page for every service.
    local.tick ? {
      "tick-missing" = {
        name     = "RYFT · Tick missing"
        severity = "CRITICAL"
        query    = <<-EOT
          (
            ${join("\n  or ", [for s in sort(local.services) : "label_replace(vector(1), \"service_name\", \"${s}\", \"\", \"\")"])}
          )
          unless on (service_name)
          (sum by (service_name) (increase(logging_googleapis_com:user_${google_logging_metric.ticks.name}{${local.run}, result="ok"}[45m])) > 0)
        EOT
        every    = "300s"
        hold     = "300s"
        look     = "the service's tick lines (jsonPayload.message=\"tick\"): result error names the step; none at all, then tick-<service> (Pub/Sub) and the Scheduler job ryft-tick."
      }
    } : {},
  )
}

# Condition 1: an uptime check fails from at least two of its three locations for 10 minutes (more than
# one location false, per check). The pattern Google's console writes, with a 10-minute duration.
resource "google_monitoring_alert_policy" "uptime" {
  count = local.alerting ? 1 : 0

  project               = var.project_id
  display_name          = "RYFT · Uptime check failing"
  combiner              = "OR"
  severity              = "CRITICAL"
  notification_channels = local.alert_channels

  conditions {
    display_name = "An uptime check fails from 2 of 3 locations for 10 minutes"

    condition_threshold {
      filter          = "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\""
      comparison      = "COMPARISON_GT"
      threshold_value = 1
      duration        = "600s"

      aggregations {
        alignment_period     = "1200s"
        per_series_aligner   = "ALIGN_NEXT_OLDER"
        cross_series_reducer = "REDUCE_COUNT_FALSE"
        group_by_fields      = ["metric.label.check_id", "resource.label.host"]
      }

      trigger {
        count = 1
      }
    }
  }

  alert_strategy {
    auto_close = "1800s"
  }

  documentation {
    mime_type = "text/markdown"
    content   = "An uptime check (ryft-site-*, ryft-api-ready, ryft-ingest-ready) fails from two of three locations. First look: the check's page in Monitoring -> Uptime checks (which location, which status), then the host itself: ryftlab.de is Firebase Hosting (site job in deploy.yml), api. and ingest. are Cloud Run (/health/ready is the database behind them). ${local.alert_look}."
  }
}

# Condition 2 to 5.
resource "google_monitoring_alert_policy" "promql" {
  for_each = local.alerting ? local.promql_policies : {}

  project               = var.project_id
  display_name          = each.value.name
  combiner              = "OR"
  severity              = each.value.severity
  notification_channels = local.alert_channels

  conditions {
    display_name = each.value.name

    condition_prometheus_query_language {
      query               = trimspace(each.value.query)
      evaluation_interval = each.value.every
      duration            = each.value.hold
      # The tick's metric is new with this root (monitoring.tf) and has no point until the first tick
      # line after the apply.
      disable_metric_validation = each.key == "tick-missing"
    }
  }

  alert_strategy {
    auto_close = "1800s"
  }

  documentation {
    mime_type = "text/markdown"
    content   = "First look: ${each.value.look} ${local.alert_look}."
  }

  depends_on = [google_logging_metric.ticks]
}

# Condition 6 (D244, D257): a line of ryft_notices (monitoring.tf) in the last 5 minutes, one series per
# kind (feedback received, admin signed in, admin auth rejected; connect capacity, connect no data,
# connect deregistration stuck, connect inbox failed). Held 60 s, the shortest that lets missing data
# count: once 5 minutes pass without a line the series has no data, INACTIVE closes the incident, and
# the next line opens a new one and notifies again (with the default, an open incident would swallow it
# until auto_close). Only the opening is notified: no closing mail for every feedback. The connection
# notices come at most once a day each (the tick of 06:00-06:14 UTC), so each notifies once a day while
# its condition holds. One policy for all seven kinds, no seventh policy (D244's cap, D257). Gated by
# alerting alone, like 1, 2, 4 and 5: under the stop line or with serve = false no line comes, so it
# stays quiet, and the flips don't destroy and recreate it. Renaming it is an update in place.
# Documentation variables: Google renders ${metric.label.kind}, a label group_by keeps; written $${...}
# here so Terraform leaves it alone. No ${log.*} variable: the line's fields stay out of the notification.
resource "google_monitoring_alert_policy" "notices" {
  count = local.alerting ? 1 : 0

  project               = var.project_id
  display_name          = "RYFT · Notices: feedback, admin access, connections"
  combiner              = "OR"
  severity              = "WARNING"
  notification_channels = local.alert_channels

  conditions {
    display_name = "A feedback, an admin sign-in, a refused admin bearer or a connection notice, by kind"

    condition_threshold {
      filter                  = "metric.type=\"logging.googleapis.com/user/${google_logging_metric.notices.name}\" AND resource.type=\"cloud_run_revision\""
      comparison              = "COMPARISON_GT"
      threshold_value         = 0
      duration                = "60s"
      evaluation_missing_data = "EVALUATION_MISSING_DATA_INACTIVE"

      aggregations {
        alignment_period     = "300s"
        per_series_aligner   = "ALIGN_SUM"
        cross_series_reducer = "REDUCE_SUM"
        group_by_fields      = ["metric.label.kind"]
      }

      trigger {
        count = 1
      }
    }
  }

  alert_strategy {
    auto_close           = "1800s"
    notification_prompts = ["OPENED"]
  }

  documentation {
    mime_type = "text/markdown"
    subject   = "RYFT · $${metric.label.kind}"
    content = trimspace(<<-EOT
      **$${metric.label.kind}**: a notice, not an outage. It closes by itself about 5 minutes after the last such line. First look, by kind:

      - **feedback received** (api): a tester sent a report. ryftlab.de/admin -> Feedback (Resolve it there once read), or `scripts/beta_report.sh feedback`.
      - **admin signed in** (api): an operator session began on ryftlab.de/admin (password and a fresh TOTP). If it wasn't you: docs/OPERATIONS.md -> Beta -> Admin sign-in -> Lost phone, or any doubt about the admin account, at once; then /admin -> Audit.
      - **admin auth rejected** (api): an admin route refused a bearer (not a missing or an expired one). Logs Explorer: `jsonPayload.message="admin auth rejected"`, its `reason`.
      - **connect capacity** (api, once a day while it holds): a provider's connections reached 80 % of its user cap. docs/OPERATIONS.md -> Direct connections -> The cap.
      - **connect no data** (api, once a day): a connected account has stored nothing from its provider for 36 h. First the dashboard RYFT · Connections (rejected pushes: the provider's secret or verification), then docs/OPERATIONS.md -> Direct connections -> Connected but no data.
      - **connect deregistration stuck** (api, once a day): a provider deregistration has waited over 24 h. docs/OPERATIONS.md -> Direct connections -> The deregistration queue.
      - **connect inbox failed** (ingest, once a day): inbox rows ingest could not process. docs/OPERATIONS.md -> Direct connections -> Failed inbox rows.

      ${local.alert_look}.
    EOT
    )
  }
}
