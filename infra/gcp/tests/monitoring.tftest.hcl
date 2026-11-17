# Cloud Monitoring (D236): the alerting gate, the cap of five conditions, the uptime checks, the
# log-based metrics and the dashboard, and that Grafana is gone. Plan-only, mocked: this machine has real
# gcloud credentials, and a real provider here could create what it tests. The app channel's name in
# these runs is a fake one; the real one is a sensitive workspace variable and in no file.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }

  # A mocked bool is false, which checks.tf's alert_channels_enabled would refuse.
  mock_data "google_monitoring_notification_channel" {
    defaults = {
      enabled = true
      name    = "projects/p/notificationChannels/0"
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

# Two channels with two names, so a policy that names one of them twice shows.
override_data {
  target = data.google_monitoring_notification_channel.email
  values = {
    enabled = true
    name    = "projects/p/notificationChannels/1"
  }
}

override_data {
  target = data.google_monitoring_notification_channel.app
  values = {
    enabled = true
    name    = "projects/p/notificationChannels/2"
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
}

# ---- the gate -----------------------------------------------------------------------------------------

run "alerting_off_while_the_app_channel_is_unset" {
  command = plan

  variables {
    serve           = true
    public_invokers = true
  }

  assert {
    condition = (
      length(data.google_monitoring_notification_channel.email) == 0 &&
      length(data.google_monitoring_notification_channel.app) == 0 &&
      length(google_monitoring_alert_policy.uptime) == 0 &&
      length(google_monitoring_alert_policy.promql) == 0
    )
    error_message = "gate off: no channel is read and no alert policy exists"
  }

  # What costs nothing does not wait for the gate.
  assert {
    condition = (
      length(google_monitoring_uptime_check_config.this) == 5 &&
      google_logging_metric.ticks.name == "ryft_ticks" &&
      google_logging_metric.errors.name == "ryft_errors" &&
      jsondecode(google_monitoring_dashboard.overview.dashboard_json).displayName == "RYFT · Overview"
    )
    error_message = "gate off: the five uptime checks, both log-based metrics and the dashboard exist anyway"
  }

  assert {
    condition     = output.monitoring.alerting == false && length(output.monitoring.policies) == 0
    error_message = "the monitoring output says alerting is off and lists no policy"
  }
}

run "alerting_on_once_the_app_channel_is_set" {
  command = plan

  variables {
    serve                  = true
    public_invokers        = true
    alert_app_channel_name = "app-channel-for-tests"
  }

  assert {
    condition = (
      length(data.google_monitoring_notification_channel.email) == 1 &&
      data.google_monitoring_notification_channel.email[0].type == "email" &&
      data.google_monitoring_notification_channel.email[0].display_name == "ryft-timkrebs" &&
      data.google_monitoring_notification_channel.email[0].project == "ryftlab-prod" &&
      length(data.google_monitoring_notification_channel.app) == 1 &&
      data.google_monitoring_notification_channel.app[0].project == "ryftlab-prod"
    )
    error_message = "gate on: the email channel ryft-timkrebs and the app channel are read, in ryftlab-prod"
  }

  assert {
    condition = (
      length(google_monitoring_alert_policy.uptime) == 1 &&
      toset(keys(google_monitoring_alert_policy.promql)) == toset(["http-5xx", "dead-letters", "push-backlog", "tick-missing"])
    )
    error_message = "gate on, ticking: the uptime policy and the four PromQL policies"
  }

  assert {
    condition = alltrue([
      for p in concat(google_monitoring_alert_policy.uptime, values(google_monitoring_alert_policy.promql)) :
      p.notification_channels == tolist(["projects/p/notificationChannels/1", "projects/p/notificationChannels/2"]) &&
      p.project == "ryftlab-prod" && p.combiner == "OR" && length(p.conditions) == 1 &&
      contains(["CRITICAL", "WARNING"], p.severity) &&
      startswith(p.display_name, "RYFT · ") &&
      [for d in p.documentation : d.mime_type] == ["text/markdown"] &&
      alltrue([for d in p.documentation : strcontains(d.content, "First look") && strcontains(d.content, "docs/OPERATIONS.md -> GCP")]) &&
      [for s in p.alert_strategy : s.auto_close] == ["1800s"]
    ])
    error_message = "every policy: both channels, one condition, a severity, markdown documentation with a first look, closes 30 minutes after its data stops"
  }

  assert {
    condition = (
      output.monitoring.alerting == true &&
      length(output.monitoring.policies) == 5 &&
      length(distinct(output.monitoring.policies)) == 5
    )
    error_message = "the monitoring output lists the five policies, each its own name"
  }
}

# The price is per condition (D236 -> Cost): five at most, with everything on, in two resource blocks.
run "five_conditions_at_most" {
  command = plan

  variables {
    serve                  = true
    public_invokers        = true
    alert_app_channel_name = "app-channel-for-tests"
  }

  assert {
    condition = length(flatten([
      for p in concat(google_monitoring_alert_policy.uptime, values(google_monitoring_alert_policy.promql)) : p.conditions
    ])) == 5
    error_message = "five conditions with every gate open, never more: a sixth is a decision (D236), not a line"
  }

  assert {
    condition = (
      length(regexall("(?m)^\\s*resource\\s+\"google_monitoring_alert_policy\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 2 &&
      length(regexall("(?m)^\\s*resource\\s+\"google_monitoring_notification_channel\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    )
    error_message = "the alert policies are alerts.tf's two blocks; no channel is made here (the user's, by hand)"
  }

  # Evaluated every minute at the most often, every 5 minutes but for the 5xx ratio.
  assert {
    condition = alltrue([
      for k, p in google_monitoring_alert_policy.promql :
      tonumber(trimsuffix(p.conditions[0].condition_prometheus_query_language[0].evaluation_interval, "s")) == (k == "http-5xx" ? 60 : 300)
    ])
    error_message = "the 5xx ratio is evaluated every 60 s, every other PromQL condition every 300 s"
  }
}

run "the_conditions" {
  command = plan

  variables {
    serve                  = true
    public_invokers        = true
    alert_app_channel_name = "app-channel-for-tests"
  }

  # 1: more than one of the three locations false, per check, held 10 minutes.
  assert {
    condition = alltrue([
      for c in google_monitoring_alert_policy.uptime[0].conditions[0].condition_threshold :
      c.filter == "metric.type=\"monitoring.googleapis.com/uptime_check/check_passed\" AND resource.type=\"uptime_url\"" &&
      c.comparison == "COMPARISON_GT" && c.threshold_value == 1 && c.duration == "600s" &&
      [for a in c.aggregations : [a.per_series_aligner, a.cross_series_reducer, a.alignment_period]] == [["ALIGN_NEXT_OLDER", "REDUCE_COUNT_FALSE", "1200s"]] &&
      contains(c.aggregations[0].group_by_fields, "metric.label.check_id")
    ])
    error_message = "condition 1: an uptime check false in more than one location (REDUCE_COUNT_FALSE > 1, by check_id) for 600 s"
  }

  # 2: the 5xx ratio of api and ingest over 2 % for 10 minutes, and at least 3 of them.
  assert {
    condition = alltrue([
      for c in google_monitoring_alert_policy.promql["http-5xx"].conditions[0].condition_prometheus_query_language : (
        strcontains(c.query, "service_name=~\"api|ingest\"") &&
        strcontains(c.query, "response_code_class=\"5xx\"") &&
        strcontains(c.query, ") > 0.02") &&
        strcontains(c.query, ">= 3") &&
        strcontains(c.query, "sum by (service_name)") &&
        c.duration == "600s"
      )
    ])
    error_message = "condition 2: 5xx over 2 % by service_name on api and ingest, at least 3, held 600 s"
  }

  # 3: every service by name, an ok tick in the last 45 minutes, from the log-based metric.
  assert {
    condition = alltrue([
      for c in google_monitoring_alert_policy.promql["tick-missing"].conditions[0].condition_prometheus_query_language : (
        alltrue([for s in ["api", "ingest", "scoring", "notify"] : strcontains(c.query, "\"service_name\", \"${s}\"")]) &&
        strcontains(c.query, "logging_googleapis_com:user_ryft_ticks{") &&
        strcontains(c.query, "result=\"ok\"}[45m]") &&
        strcontains(c.query, "unless on (service_name)") &&
        c.disable_metric_validation == true
      )
    ])
    error_message = "condition 3: each of the four services without an ok tick for 45 minutes, an absent series included"
  }

  # 4 and 5: the subscriptions by name, from the topology.
  assert {
    condition = alltrue([
      for c in google_monitoring_alert_policy.promql["dead-letters"].conditions[0].condition_prometheus_query_language : (
        strcontains(c.query, "subscription_num_undelivered_messages{") &&
        strcontains(c.query, "subscription_id=~\"dlq-notify-hold|dlq-scoring-hold\"") &&
        endswith(c.query, "> 0")
      )
    ])
    error_message = "condition 4: a DLQ hold subscription holds a message"
  }

  assert {
    condition = alltrue([
      for c in google_monitoring_alert_policy.promql["push-backlog"].conditions[0].condition_prometheus_query_language : (
        strcontains(c.query, "subscription_oldest_unacked_message_age{") &&
        strcontains(c.query, "subscription_id=~\"notify-refresh|notify-scored|notify-session-scored|notify-wait|scoring-ingested|scoring-session-completed\"") &&
        !strcontains(c.query, "tick-") && !strcontains(c.query, "-hold") &&
        endswith(c.query, "> 900")
      )
    ])
    error_message = "condition 5: an event subscription's oldest unacked event over 900 s; no tick, no hold"
  }
}

# The stop line pauses the tick and closes api and ingest to everyone: neither has anything to alert on.
run "the_stop_line_drops_the_tick_policy_and_the_service_checks" {
  command = plan

  variables {
    serve                  = true
    public_invokers        = false
    alert_app_channel_name = "app-channel-for-tests"
  }

  assert {
    condition = (
      toset(keys(google_monitoring_alert_policy.promql)) == toset(["http-5xx", "dead-letters", "push-backlog"]) &&
      length(google_monitoring_alert_policy.uptime) == 1
    )
    error_message = "the stop line: no tick-missing policy (the tick is paused), the other four stay"
  }

  assert {
    condition     = toset(keys(google_monitoring_uptime_check_config.this)) == toset(["ryft-site-home", "ryft-site-privacy", "ryft-site-aasa"])
    error_message = "the stop line: the site's three checks only, none on api or ingest"
  }
}

run "serve_false_has_no_tick_policy_and_no_service_checks" {
  command = plan

  variables {
    serve                  = false
    public_invokers        = true
    alert_app_channel_name = "app-channel-for-tests"
  }

  assert {
    condition = (
      !contains(keys(google_monitoring_alert_policy.promql), "tick-missing") &&
      toset(keys(google_monitoring_uptime_check_config.this)) == toset(["ryft-site-home", "ryft-site-privacy", "ryft-site-aasa"])
    )
    error_message = "serve = false: no tick policy, and no check on hosts that do not exist yet"
  }
}

# A disabled channel is a silent policy: checks.tf warns on every plan.
run "a_disabled_channel_is_flagged" {
  command = plan

  variables {
    serve                  = true
    public_invokers        = true
    alert_app_channel_name = "app-channel-for-tests"
  }

  override_data {
    target = data.google_monitoring_notification_channel.app
    values = {
      enabled = false
      name    = "projects/p/notificationChannels/2"
    }
  }

  expect_failures = [check.alert_channels_enabled]
}

# ---- what costs nothing ----------------------------------------------------------------------------------

run "the_uptime_checks" {
  command = plan

  variables {
    serve           = true
    public_invokers = true
  }

  assert {
    condition = toset(keys(google_monitoring_uptime_check_config.this)) == toset([
      "ryft-site-home", "ryft-site-privacy", "ryft-site-aasa", "ryft-api-ready", "ryft-ingest-ready",
    ])
    error_message = "the site's three checks and api's and ingest's readiness while they're public"
  }

  assert {
    condition = alltrue([
      for k, c in google_monitoring_uptime_check_config.this :
      c.display_name == k && c.project == "ryftlab-prod" && c.period == "300s" && c.timeout == "10s" &&
      toset(c.selected_regions) == toset(["EUROPE", "USA_IOWA", "USA_VIRGINIA"]) && length(c.selected_regions) == 3 &&
      [for h in c.http_check : [h.request_method, h.port, h.use_ssl, h.validate_ssl]] == [["GET", 443, true, true]] &&
      [for s in c.http_check[0].accepted_response_status_codes : s.status_value] == [200] &&
      c.monitored_resource[0].type == "uptime_url" && c.monitored_resource[0].labels["project_id"] == "ryftlab-prod"
    ])
    error_message = "every check: every 300 s from exactly three locations (EUROPE, USA_IOWA, USA_VIRGINIA), 10 s, GET over validated TLS, 200 only"
  }

  assert {
    condition = (
      google_monitoring_uptime_check_config.this["ryft-site-home"].monitored_resource[0].labels["host"] == "ryftlab.de" &&
      google_monitoring_uptime_check_config.this["ryft-site-home"].http_check[0].path == "/" &&
      google_monitoring_uptime_check_config.this["ryft-site-privacy"].http_check[0].path == "/privacy" &&
      google_monitoring_uptime_check_config.this["ryft-site-aasa"].http_check[0].path == "/.well-known/apple-app-site-association" &&
      [for m in google_monitoring_uptime_check_config.this["ryft-site-aasa"].content_matchers : [m.content, m.matcher]] == [["M77SKHKG63.dev.timkrebs.ryft", "CONTAINS_STRING"]] &&
      length(google_monitoring_uptime_check_config.this["ryft-site-home"].content_matchers) == 0
    )
    error_message = "the site: /, /privacy and the AASA on ryftlab.de; the AASA must hold the app's ID"
  }

  assert {
    condition = alltrue([
      for s in ["api", "ingest"] :
      google_monitoring_uptime_check_config.this["ryft-${s}-ready"].monitored_resource[0].labels["host"] == "${s}.ryftlab.de" &&
      google_monitoring_uptime_check_config.this["ryft-${s}-ready"].http_check[0].path == "/health/ready"
    ])
    error_message = "api and ingest: /health/ready on their own hosts (the database behind them)"
  }

  # Executions a month: checks x locations x 12 an hour x 730 hours, under the free million (D236).
  assert {
    condition     = length(google_monitoring_uptime_check_config.this) * 3 * 12 * 730 == 131400
    error_message = "5 checks x 3 locations every 5 minutes are 131,400 executions a month"
  }
}

# Labels hold closed sets only: never an IP, a user id, an email or a message id.
run "the_log_based_metrics" {
  command = plan

  assert {
    condition = (
      strcontains(google_logging_metric.ticks.filter, "resource.type=\"cloud_run_revision\"") &&
      strcontains(google_logging_metric.ticks.filter, "logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout\"") &&
      strcontains(google_logging_metric.ticks.filter, "jsonPayload.message=\"tick\"") &&
      google_logging_metric.ticks.label_extractors == tomap({ result = "EXTRACT(jsonPayload.result)" }) &&
      [for d in google_logging_metric.ticks.metric_descriptor : [d.metric_kind, d.value_type]] == [["DELTA", "INT64"]] &&
      [for l in google_logging_metric.ticks.metric_descriptor[0].labels : l.key] == ["result"]
    )
    error_message = "ryft_ticks counts the services' tick lines by result, its one label"
  }

  assert {
    condition = (
      strcontains(google_logging_metric.errors.filter, "severity>=ERROR") &&
      !strcontains(google_logging_metric.errors.filter, "run.googleapis.com%2Frequests") &&
      try(length(google_logging_metric.errors.label_extractors), 0) == 0 &&
      length(google_logging_metric.errors.metric_descriptor[0].labels) == 0
    )
    error_message = "ryft_errors counts the services' own ERROR lines (not Cloud Run's request log), without a label"
  }

  assert {
    condition = length(regexall(
      "(?i)ip|user|email|mail|message_id|messageid|device|token",
      join(" ", concat(keys(google_logging_metric.ticks.label_extractors), values(google_logging_metric.ticks.label_extractors))),
    )) == 0
    error_message = "no label extracts an IP, a user, an email, a message id, a device or a token"
  }
}

run "the_dashboard" {
  command = plan

  variables {
    serve           = true
    public_invokers = true
  }

  assert {
    condition = (
      jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.columns == 48 &&
      length(jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles) == 13 &&
      alltrue([for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : t.xPos + t.width <= 48])
    )
    error_message = "the dashboard: 13 tiles on 48 columns, none past the edge"
  }

  assert {
    condition = (
      [
        for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles :
        [t.widget.logsPanel.filter, t.widget.logsPanel.resourceNames] if can(t.widget.logsPanel)
        ] == [[
          "resource.type=\"cloud_run_revision\"\nseverity>=ERROR",
          ["projects/ryftlab-prod/locations/europe-west1/buckets/ryft-logs/views/_AllLogs"],
      ]]
    )
    error_message = "one logs panel of ERROR lines, read from ryft-logs"
  }

  assert {
    condition = alltrue([
      for m in [
        "run_googleapis_com:request_count", "run_googleapis_com:request_latencies_bucket", "run_googleapis_com:container_instance_count",
        "pubsub_googleapis_com:subscription_oldest_unacked_message_age", "pubsub_googleapis_com:subscription_num_undelivered_messages",
        "logging_googleapis_com:user_ryft_ticks", "logging_googleapis_com:user_ryft_errors",
        "uptime_check/check_passed", "uptime_check/time_until_ssl_cert_expires",
      ] : strcontains(google_monitoring_dashboard.overview.dashboard_json, m)
    ])
    error_message = "the panels: requests, 5xx, latency, instances, backlog, DLQ holds, ticks, error lines, uptime and certificates"
  }
}

# ---- Grafana is gone, and the app channel's name stays out of every file ------------------------------

run "no_grafana" {
  command = plan

  assert {
    condition = (
      !strcontains(lower(file("${path.module}/terraform.tf")), "grafana") &&
      !strcontains(lower(file("${path.module}/providers.tf")), "grafana") &&
      !strcontains(lower(file("${path.module}/.terraform.lock.hcl")), "grafana") &&
      !contains(keys(google_service_account.this), "grafana-reader")
    )
    error_message = "no grafana provider (terraform.tf, providers.tf, the lock file) and no grafana-reader account"
  }
}

run "the_app_channel_name_is_sensitive_and_in_no_file" {
  command = plan

  assert {
    condition     = length(regexall("variable \"alert_app_channel_name\" \\{[^}]*\\n\\s*sensitive\\s*=\\s*true", file("${path.module}/variables.tf"))) == 1
    error_message = "alert_app_channel_name is sensitive"
  }

  # The committed tfvars set it nowhere (terraform.tfvars itself is the user's, and never read here).
  assert {
    condition = length(regexall(
      "(?m)^\\s*\"?alert_app_channel_name\"?\\s*[=:]",
      join("\n", concat(
        [for f in fileset(path.module, "*.auto.tfvars") : file("${path.module}/${f}")],
        [for f in fileset(path.module, "*.auto.tfvars.json") : file("${path.module}/${f}")],
        [file("${path.module}/terraform.tfvars.example")],
      )),
    )) == 0
    error_message = "no committed tfvars sets alert_app_channel_name: it is a sensitive workspace variable only"
  }

  # The tests set it to the fake name only.
  assert {
    condition = toset(flatten(regexall(
      "alert_app_channel_name\\s*=\\s*\"([^\"]*)\"",
      join("\n", [for f in fileset("${path.module}/tests", "*.tftest.hcl") : file("${path.module}/tests/${f}")]),
    ))) == toset(["app-channel-for-tests"])
    error_message = "every test sets alert_app_channel_name to app-channel-for-tests, never a real-looking name"
  }
}
