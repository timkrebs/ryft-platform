# Cloud Monitoring is RYFT's only observability (D236; it supersedes D224's Grafana half). What Google
# measures itself (Cloud Run, Pub/Sub, the uptime checks below) plus two log-based metrics counted from
# lines the services already write. Nothing in this file has a price at RYFT's volume (D236 -> Cost),
# so none of it waits for the alerting gate; the alert policies are in alerts.tf.

locals {
  # The services' own lines on Cloud Run: JSON on stdout in Cloud Logging's keys (D224: `message`,
  # `severity`), anything a crash writes to stderr. Cloud Run's request log
  # (run.googleapis.com/requests) is Google's, not ours, and stays out of both metrics.
  log_stdout = "logName=\"projects/${var.project_id}/logs/run.googleapis.com%2Fstdout\""
  log_stderr = "logName=\"projects/${var.project_id}/logs/run.googleapis.com%2Fstderr\""

  # The checker locations (D236). Exactly three: EUROPE is one location (Belgium), each USA_* is one,
  # and USA alone would be three (Oregon, Iowa, Virginia), which would make five. Three is also the
  # fewest Google accepts. Condition 1 (alerts.tf) fires from two of them.
  uptime_regions = ["EUROPE", "USA_IOWA", "USA_VIRGINIA"]
  uptime_period  = "300s"

  # The app's ID in the AASA (webcredentials and applinks); a site without it breaks universal links
  # and password AutoFill.
  app_id = "M77SKHKG63.dev.timkrebs.ryft"

  # Check name => host, path and the text the body must hold ("" for none). The site always (Firebase
  # Hosting, C1); api and ingest only while they're public: with serve = false the hosts don't exist,
  # and under the stop line they answer 403 to everyone.
  uptime_checks = merge(
    {
      "ryft-site-home"    = { host = local.domain, path = "/", content = "" }
      "ryft-site-privacy" = { host = local.domain, path = "/privacy", content = "" }
      "ryft-site-aasa"    = { host = local.domain, path = "/.well-known/apple-app-site-association", content = local.app_id }
    },
    {
      for s in local.public_services : "ryft-${s}-ready" => { host = "${s}.${local.domain}", path = "/health/ready", content = "" }
      if local.public
    },
  )

  dashboard_name = "RYFT · Overview"
}

# ---- the log-based metrics: counters of lines that exist (no Go change, D236) ------------------------
# Labels hold closed sets only: never an IP, a user id, an email or a message id. The service is the
# resource's own label (cloud_run_revision: service_name, revision_name, location), not one of ours.
# A counter writes no point for a minute without a matching line, so "no tick" is an absent series:
# condition 3 (alerts.tf) expects every service by name.

# One per POST /internal/tick a service handled (backend/internal/events/pubsub/tick.go: the line
# `tick`, with result ok or error). The line is at info: a LOG_LEVEL above info silences it, and then
# condition 3 pages for every service.
resource "google_logging_metric" "ticks" {
  project     = var.project_id
  name        = "ryft_ticks"
  description = "The services' tick lines (message tick), by result, ok or error (infra/gcp, D236)"
  filter = join("\n", [
    "resource.type=\"cloud_run_revision\"",
    local.log_stdout,
    "jsonPayload.message=\"tick\"",
  ])

  metric_descriptor {
    metric_kind  = "DELTA"
    value_type   = "INT64"
    unit         = "1"
    display_name = "RYFT ticks"

    labels {
      key         = "result"
      value_type  = "STRING"
      description = "The tick line's result: ok, or error (a step failed or ran out of budget)"
    }
  }

  label_extractors = {
    "result" = "EXTRACT(jsonPayload.result)"
  }
}

# The services' ERROR lines, panics included (msg panic, D224's severity), per service: a dashboard
# panel, no policy (the cap of five, D236).
resource "google_logging_metric" "errors" {
  project     = var.project_id
  name        = "ryft_errors"
  description = "The services' own lines at severity ERROR or above, stdout and stderr (infra/gcp, D236)"
  filter = join("\n", [
    "resource.type=\"cloud_run_revision\"",
    "(${local.log_stdout} OR ${local.log_stderr})",
    "severity>=ERROR",
  ])

  metric_descriptor {
    metric_kind  = "DELTA"
    value_type   = "INT64"
    unit         = "1"
    display_name = "RYFT error lines"
  }
}

# The edge's (D242), from edge = built on: Cloud Armor's denials and preview matches, by the rule's
# priority (edge.tf: 1000 to 1005 the WAF sets, 2000 the rate rule; a closed set). Counted from the
# load balancer's request log entries, the same ones the sink keeps (logging.tf). Never an IP (the
# rate rule's rateLimitAction.key holds it), a URL or a user agent as a label.
locals {
  edge_log_metrics = local.edge_built ? {
    ryft_edge_denials = {
      description = "Cloud Armor's denials at the edge (a WAF rule enforced, the rate rule's 429 and ban), by rule priority (infra/gcp, D242)"
      title       = "RYFT edge denials"
      field       = "enforcedSecurityPolicy"
    }
    ryft_edge_preview_matches = {
      description = "Requests a preview rule at the edge would have denied, by rule priority (infra/gcp, D242)"
      title       = "RYFT edge preview matches"
      field       = "previewSecurityPolicy"
    }
  } : {}
}

resource "google_logging_metric" "edge" {
  for_each = local.edge_log_metrics

  project     = var.project_id
  name        = each.key
  description = each.value.description
  filter = join("\n", [
    "resource.type=\"http_load_balancer\"",
    "jsonPayload.${each.value.field}.outcome=\"DENY\"",
  ])

  metric_descriptor {
    metric_kind  = "DELTA"
    value_type   = "INT64"
    unit         = "1"
    display_name = each.value.title

    labels {
      key         = "priority"
      value_type  = "STRING"
      description = "The Cloud Armor rule's priority (edge.tf: 1000 to 1005 WAF, 2000 the rate rule)"
    }
  }

  label_extractors = {
    "priority" = "EXTRACT(jsonPayload.${each.value.field}.priority)"
  }
}

# ---- the uptime checks: the site and the two public services from outside --------------------------
# Every 5 minutes from three locations, 10 s each, TLS validated, exactly 200, no redirect accepted.
# 5 checks x 3 locations x 8,760 runs a month per location (12 an hour x 730 hours) = 131,400 executions, under
# the free million (D236 -> Cost). /health/ready is the readiness path, the database behind it
# (OBSERVABILITY -> Health paths), never the startup probe's /health/live.
resource "google_monitoring_uptime_check_config" "this" {
  for_each = local.uptime_checks

  project          = var.project_id
  display_name     = each.key
  period           = local.uptime_period
  timeout          = "10s"
  selected_regions = local.uptime_regions
  checker_type     = "STATIC_IP_CHECKERS"

  http_check {
    request_method = "GET"
    path           = each.value.path
    port           = 443
    use_ssl        = true
    validate_ssl   = true

    accepted_response_status_codes {
      status_value = 200
    }
  }

  monitored_resource {
    type = "uptime_url"
    labels = {
      project_id = var.project_id
      host       = each.value.host
    }
  }

  dynamic "content_matchers" {
    for_each = each.value.content == "" ? [] : [each.value.content]

    content {
      content = content_matchers.value
      matcher = "CONTAINS_STRING"
    }
  }
}

# ---- the dashboard "RYFT · Overview" ------------------------------------------------------------------
# PromQL on Google's metric names (run.googleapis.com/request_count is run_googleapis_com:request_count;
# the log-based metric logging.googleapis.com/user/ryft_ticks is logging_googleapis_com:user_ryft_ticks).
# SyncSlow and Saturated, Grafana's two rules without a policy now, are panels here: ingest's p95 and the
# 429s. Two tiles per row, 48 columns.
locals {
  run    = "monitored_resource=\"cloud_run_revision\""
  pubsub = "monitored_resource=\"pubsub_subscription\""

  # The subscriptions by name, from the topology (pubsub.auto.tfvars.json), so a new one is in the panels
  # and in conditions 4 and 5 (alerts.tf) without an edit here: the DLQ holds, and the event
  # subscriptions (every push subscription with a dead-letter topic; the ticks have none and keep a
  # message 10 minutes, so their backlog never means much). PromQL's =~ is anchored.
  dlq_holds           = join("|", sort(keys(var.pubsub_hold_subscriptions)))
  event_subscriptions = join("|", sort([for k, s in var.pubsub_push_subscriptions : k if s.dead_letter_topic != ""]))

  # Title => the PromQL of one line chart, in order.
  dashboard_promql = [
    ["Requests per second, by service", "sum by (service_name) (rate(run_googleapis_com:request_count{${local.run}}[5m]))"],
    ["5xx per second, by service", "sum by (service_name) (rate(run_googleapis_com:request_count{${local.run}, response_code_class=\"5xx\"}[5m]))"],
    ["p95 latency (ms), by service (SyncSlow: ingest over 10,000)", "histogram_quantile(0.95, sum by (service_name, le) (rate(run_googleapis_com:request_latencies_bucket{${local.run}}[5m])))"],
    ["Instances, by service and state", "sum by (service_name, state) (run_googleapis_com:container_instance_count{${local.run}})"],
    ["429 per second, by service (Saturated: Cloud Run's or the app's limiter)", "sum by (service_name) (rate(run_googleapis_com:request_count{${local.run}, response_code=\"429\"}[5m]))"],
    ["Ticks per hour, by service and result", "sum by (service_name, result) (increase(logging_googleapis_com:user_${google_logging_metric.ticks.name}{${local.run}}[1h]))"],
    ["Oldest unacked event (s), by subscription", "max by (subscription_id) (pubsub_googleapis_com:subscription_oldest_unacked_message_age{${local.pubsub}, subscription_id=~\"${local.event_subscriptions}\"})"],
    ["Undelivered events, by subscription", "max by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages{${local.pubsub}, subscription_id=~\"${local.event_subscriptions}\"})"],
    ["Dead letters held (scripts/dlq.sh list)", "max by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages{${local.pubsub}, subscription_id=~\"${local.dlq_holds}\"})"],
    ["ERROR lines per hour, by service", "sum by (service_name) (increase(logging_googleapis_com:user_${google_logging_metric.errors.name}{${local.run}}[1h]))"],
  ]

  # The uptime checks, by check (classic filters: check_passed is a BOOL, which PromQL would have to cast).
  dashboard_uptime = [
    ["Uptime: share of locations passing, by check", "monitoring.googleapis.com/uptime_check/check_passed", "ALIGN_FRACTION_TRUE", "REDUCE_MEAN"],
    ["Certificate: days left, by check", "monitoring.googleapis.com/uptime_check/time_until_ssl_cert_expires", "ALIGN_MIN", "REDUCE_MIN"],
  ]

  # The edge's (D242), from edge = built on, after the PromQL tiles above. The load balancer's metric is
  # loadbalancing.googleapis.com/https/request_count on https_lb_rule (backend_target_name is the
  # backend service, ryft-edge-api or ryft-edge-ingest; response_code_class is a number there, 500 not
  # 5xx); Cloud Armor's is networksecurity.googleapis.com/https/request_count on network_security_policy,
  # with blocked and backend_target_name (search). The log-based pair is one tile, its series by
  # priority and kind (denied, preview), with no monitored_resource selector: Logging writes them
  # against the load balancer's log resource, and that resource's name in Monitoring wasn't verified.
  lb    = "monitored_resource=\"https_lb_rule\""
  armor = "monitored_resource=\"network_security_policy\""
  dashboard_edge = local.edge_built ? [
    ["Edge: requests per second, by backend and code class", "sum by (backend_target_name, response_code_class) (rate(loadbalancing_googleapis_com:https_request_count{${local.lb}}[5m]))"],
    ["Edge: 429 per second, by backend (Cloud Armor's and the services')", "sum by (backend_target_name) (rate(loadbalancing_googleapis_com:https_request_count{${local.lb}, response_code=\"429\"}[5m]))"],
    ["Cloud Armor: requests per second, blocked or not, by backend", "sum by (backend_target_name, blocked) (rate(networksecurity_googleapis_com:https_request_count{${local.armor}}[5m]))"],
    ["Cloud Armor: denials and preview matches per hour, by rule priority", join(" or ", [
      "label_replace(sum by (priority) (increase(logging_googleapis_com:user_${google_logging_metric.edge["ryft_edge_denials"].name}[1h])), \"kind\", \"denied\", \"\", \"\")",
      "label_replace(sum by (priority) (increase(logging_googleapis_com:user_${google_logging_metric.edge["ryft_edge_preview_matches"].name}[1h])), \"kind\", \"preview\", \"\", \"\")",
    ])],
  ] : []

  dashboard_widgets = concat(
    [for w in concat(local.dashboard_promql, local.dashboard_edge) : {
      title = w[0]
      xyChart = {
        chartOptions = { mode = "COLOR" }
        dataSets = [{
          plotType        = "LINE"
          targetAxis      = "Y1"
          timeSeriesQuery = { prometheusQuery = w[1] }
        }]
      }
    }],
    [for w in local.dashboard_uptime : {
      title = w[0]
      xyChart = {
        chartOptions = { mode = "COLOR" }
        dataSets = [{
          plotType   = "LINE"
          targetAxis = "Y1"
          timeSeriesQuery = {
            timeSeriesFilter = {
              filter = "metric.type=\"${w[1]}\" resource.type=\"uptime_url\""
              aggregation = {
                alignmentPeriod    = local.uptime_period
                perSeriesAligner   = w[2]
                crossSeriesReducer = w[3]
                groupByFields      = ["metric.label.check_id"]
              }
            }
          }
        }]
      }
    }],
  )
}

locals {
  # A tile's position. The Monitoring API leaves a zero xPos or yPos out of what it returns, and the
  # provider's diff ignores only keys the config lacks, so a zero is never written: else every plan
  # would show the dashboard changed (runbook 11.3's second plan says "No changes").
  # Two 24-wide tiles per row, 16 high; the logs panel (index n) spans the row below the last.
  tile_xy = { for i in range(length(local.dashboard_widgets) + 1) : i => (
    i < length(local.dashboard_widgets)
    ? { x = (i % 2) * 24, y = floor(i / 2) * 16 }
    : { x = 0, y = ceil(length(local.dashboard_widgets) / 2) * 16 }
  ) }
  tile_at = { for i, p in local.tile_xy : i => merge(
    p.x == 0 ? {} : { xPos = p.x },
    p.y == 0 ? {} : { yPos = p.y },
  ) }
}

resource "google_monitoring_dashboard" "overview" {
  project = var.project_id

  dashboard_json = jsonencode({
    displayName = local.dashboard_name
    mosaicLayout = {
      columns = 48
      tiles = concat(
        [for i, w in local.dashboard_widgets : merge(local.tile_at[i], {
          width  = 24
          height = 16
          widget = w
        })],
        # The services' own ERROR lines, from ryft-logs (every log is there, logging.tf; from edge = built
        # on less the edge's two exclusions). Only stdout and stderr: Cloud Run's request log
        # (run.googleapis.com/requests) has ERROR entries for every 5xx, and they carry the caller's IP,
        # the URL and the user agent.
        [merge(local.tile_at[length(local.dashboard_widgets)], {
          width  = 48
          height = 20
          widget = {
            title = "ERROR lines (the services' own)"
            logsPanel = {
              filter        = "resource.type=\"cloud_run_revision\"\n(${local.log_stdout} OR ${local.log_stderr})\nseverity>=ERROR"
              resourceNames = ["projects/${var.project_id}/locations/${google_logging_project_bucket_config.ryft_logs.location}/buckets/${google_logging_project_bucket_config.ryft_logs.bucket_id}/views/_AllLogs"]
            }
          }
        })],
      )
    }
  })
}
