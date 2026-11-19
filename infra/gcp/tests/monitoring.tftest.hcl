# Cloud Monitoring (D236, D237, D244): the alerting gate, the app channel by id, the cap of six
# conditions, the uptime checks, the log-based metrics and the dashboard, and that Grafana is gone. Plan-only,
# mocked: this machine has real gcloud credentials, and a real provider here could create what it
# tests. Every channel in these runs is in the fake project fake-for-tests; the real app channel is a
# workspace variable and in no committed file.
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

# The email channel as the API would return it. The runs that turn alerting on set alert_app_channel
# to .../notificationChannels/2, another channel, so a policy that names one of them twice shows.
override_data {
  target = data.google_monitoring_notification_channel.email
  values = {
    enabled = true
    name    = "projects/fake-for-tests/notificationChannels/1"
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
  # The dashboard and the metrics as before D242, whatever gates.auto.tfvars holds; the edge's tiles and
  # metrics are tests/edge.tftest.hcl's.
  edge = "off"
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
      var.alert_app_channel == null &&
      length(data.google_monitoring_notification_channel.email) == 0 &&
      length(google_monitoring_alert_policy.uptime) == 0 &&
      length(google_monitoring_alert_policy.promql) == 0 &&
      length(google_monitoring_alert_policy.notices) == 0
    )
    error_message = "gate off (alert_app_channel null): no channel is read and no alert policy exists, the notices policy neither"
  }

  # What costs nothing does not wait for the gate.
  assert {
    condition = (
      length(google_monitoring_uptime_check_config.this) == 5 &&
      google_logging_metric.ticks.name == "ryft_ticks" &&
      google_logging_metric.errors.name == "ryft_errors" &&
      google_logging_metric.notices.name == "ryft_notices" &&
      jsondecode(google_monitoring_dashboard.overview.dashboard_json).displayName == "RYFT · Overview"
    )
    error_message = "gate off: the five uptime checks, the three log-based metrics (ryft_notices too) and the dashboard exist anyway"
  }

  assert {
    condition = (
      output.monitoring.alerting == false && length(output.monitoring.policies) == 0 &&
      output.monitoring.log_metrics == tolist(["ryft_errors", "ryft_notices", "ryft_ticks"])
    )
    error_message = "the monitoring output says alerting is off, lists no policy, and the three log-based metrics"
  }
}

run "alerting_on_once_the_app_channel_is_set" {
  command = plan

  variables {
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
  }

  assert {
    condition = (
      length(data.google_monitoring_notification_channel.email) == 1 &&
      data.google_monitoring_notification_channel.email[0].type == "email" &&
      data.google_monitoring_notification_channel.email[0].display_name == "ryft-timkrebs" &&
      data.google_monitoring_notification_channel.email[0].project == "ryftlab-prod"
    )
    error_message = "gate on: the email channel ryft-timkrebs is read, in ryftlab-prod, and no other channel (the app's is an id)"
  }

  # Exactly the email channel's name, then the variable, in every policy; and in the plan in clear, so
  # the user can read the id there (D237: not sensitive).
  assert {
    condition = alltrue([
      for p in concat(google_monitoring_alert_policy.uptime, google_monitoring_alert_policy.notices, values(google_monitoring_alert_policy.promql)) :
      p.notification_channels == tolist([data.google_monitoring_notification_channel.email[0].name, var.alert_app_channel]) &&
      p.notification_channels == tolist(["projects/fake-for-tests/notificationChannels/1", "projects/fake-for-tests/notificationChannels/2"]) &&
      !issensitive(p.notification_channels)
    ])
    error_message = "every policy's notification_channels is [the email channel's name, alert_app_channel], in that order, not sensitive"
  }

  assert {
    condition = (
      length(google_monitoring_alert_policy.uptime) == 1 &&
      toset(keys(google_monitoring_alert_policy.promql)) == toset(["http-5xx", "dead-letters", "push-backlog", "tick-missing"]) &&
      length(google_monitoring_alert_policy.notices) == 1
    )
    error_message = "gate on, ticking: the uptime policy, the four PromQL policies and the notices policy"
  }

  assert {
    condition = alltrue([
      for p in concat(google_monitoring_alert_policy.uptime, google_monitoring_alert_policy.notices, values(google_monitoring_alert_policy.promql)) :
      length(p.notification_channels) == 2 &&
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
      length(output.monitoring.policies) == 6 &&
      length(distinct(output.monitoring.policies)) == 6 &&
      contains(output.monitoring.policies, "RYFT · Feedback and admin access")
    )
    error_message = "the monitoring output lists the six policies, each its own name, the notices policy among them"
  }
}

# The price is per condition (D236 -> Cost, D244 -> The price): six at most, with everything on, in three
# resource blocks. Five until the user's decision of 2026-11-19 (D244): feedback and admin access had no
# alert since D236, and the sixth is the notices policy. A seventh is a decision again, not a line.
run "six_conditions_at_most" {
  command = plan

  variables {
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
  }

  assert {
    condition = length(flatten([
      for p in concat(google_monitoring_alert_policy.uptime, google_monitoring_alert_policy.notices, values(google_monitoring_alert_policy.promql)) : p.conditions
    ])) == 6
    error_message = "six conditions with every gate open, never more: the sixth was the user's decision (D244), a seventh is a decision too, not a line"
  }

  assert {
    condition = (
      length(regexall("(?m)^\\s*resource\\s+\"google_monitoring_alert_policy\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 3 &&
      length(regexall("(?m)^\\s*resource\\s+\"google_monitoring_notification_channel\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0 &&
      length(regexall("(?m)^\\s*data\\s+\"google_monitoring_notification_channel\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    )
    error_message = "the alert policies are alerts.tf's three blocks; no channel is made here (the user's, by hand), and only the email's is looked up (the app's is an id, D237)"
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
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
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

  # 6 (D244): a classic threshold, not PromQL (one feedback is one point of a sparse DELTA counter). Any
  # line of ryft_notices in 5 minutes, one series per kind, held 60 s; missing data closes it, so the
  # next line is a new incident and a new notification.
  assert {
    condition = (
      length(google_monitoring_alert_policy.notices[0].conditions[0].condition_prometheus_query_language) == 0 &&
      length(google_monitoring_alert_policy.notices[0].conditions[0].condition_threshold) == 1 &&
      alltrue([
        for c in google_monitoring_alert_policy.notices[0].conditions[0].condition_threshold :
        c.filter == "metric.type=\"logging.googleapis.com/user/ryft_notices\" AND resource.type=\"cloud_run_revision\"" &&
        c.comparison == "COMPARISON_GT" && c.threshold_value == 0 && c.duration == "60s" &&
        c.evaluation_missing_data == "EVALUATION_MISSING_DATA_INACTIVE" &&
        [for a in c.aggregations : [a.alignment_period, a.per_series_aligner, a.cross_series_reducer]] == [["300s", "ALIGN_SUM", "REDUCE_SUM"]] &&
        c.aggregations[0].group_by_fields == tolist(["metric.label.kind"]) &&
        [for t in c.trigger : t.count] == [1]
      ])
    )
    error_message = "condition 6: ryft_notices summed over 300 s, by metric.label.kind, > 0 held 60 s, missing data inactive, trigger 1"
  }

  assert {
    condition = (
      google_monitoring_alert_policy.notices[0].display_name == "RYFT · Feedback and admin access" &&
      google_monitoring_alert_policy.notices[0].severity == "WARNING" &&
      [for s in google_monitoring_alert_policy.notices[0].alert_strategy : s.notification_prompts] == [tolist(["OPENED"])]
    )
    error_message = "the notices policy: its name, severity WARNING, and only the opening notified (no closing mail for every feedback)"
  }

  # The kind in the subject and the text (a label group_by keeps, which Google renders); a first look per
  # kind; no identifier and no ${log.*} variable, which would put the line's fields into the mail.
  assert {
    condition = length(google_monitoring_alert_policy.notices[0].documentation) == 1 && alltrue([
      for d in google_monitoring_alert_policy.notices[0].documentation : (
        strcontains(d.subject, "$${metric.label.kind}") && strcontains(d.content, "$${metric.label.kind}") &&
        alltrue([for k in ["feedback received", "admin signed in", "admin auth rejected"] : strcontains(d.content, "**${k}**")]) &&
        strcontains(d.content, "/admin -> Feedback") && strcontains(d.content, "scripts/beta_report.sh feedback") &&
        strcontains(d.content, "Lost phone, or any doubt about the admin account") && strcontains(d.content, "/admin -> Audit") &&
        strcontains(d.content, "jsonPayload.message=\"admin auth rejected\"") &&
        length(regexall("(?i)user_id|email|feedback_id|request_id|admin_actor", "${d.subject} ${d.content}")) == 0 &&
        !strcontains("${d.subject} ${d.content}", "$${log.")
      )
    ])
    error_message = "the notices policy's documentation: the kind, the three kinds with /admin, the script, the lost-phone runbook and the reject line; no identifier, no $${log.*} variable"
  }
}

# The stop line pauses the tick and closes api and ingest to everyone: neither has anything to alert on.
run "the_stop_line_drops_the_tick_policy_and_the_service_checks" {
  command = plan

  variables {
    serve             = true
    public_invokers   = false
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
  }

  assert {
    condition = (
      toset(keys(google_monitoring_alert_policy.promql)) == toset(["http-5xx", "dead-letters", "push-backlog"]) &&
      length(google_monitoring_alert_policy.uptime) == 1 &&
      length(google_monitoring_alert_policy.notices) == 1 &&
      length(output.monitoring.policies) == 5
    )
    error_message = "the stop line: no tick-missing policy (the tick is paused), the other five stay, the notices policy among them"
  }

  assert {
    condition     = toset(keys(google_monitoring_uptime_check_config.this)) == toset(["ryft-site-home", "ryft-site-privacy", "ryft-site-aasa"])
    error_message = "the stop line: the site's three checks only, none on api or ingest"
  }
}

run "serve_false_has_no_tick_policy_and_no_service_checks" {
  command = plan

  variables {
    serve             = false
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
  }

  assert {
    condition = (
      !contains(keys(google_monitoring_alert_policy.promql), "tick-missing") &&
      toset(keys(google_monitoring_uptime_check_config.this)) == toset(["ryft-site-home", "ryft-site-privacy", "ryft-site-aasa"])
    )
    error_message = "serve = false: no tick policy, and no check on hosts that do not exist yet"
  }

  assert {
    condition     = length(google_monitoring_alert_policy.notices) == 1 && google_logging_metric.notices.name == "ryft_notices"
    error_message = "serve = false: the notices policy and its metric stay (no line comes, so it is quiet)"
  }
}

# A disabled channel is a silent policy: checks.tf warns on every plan. The email's only: the app's is
# an id that no data source reads (D237), and 11.4's test alert proves it.
run "a_disabled_channel_is_flagged" {
  command = plan

  variables {
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
  }

  override_data {
    target = data.google_monitoring_notification_channel.email
    values = {
      enabled = false
      name    = "projects/fake-for-tests/notificationChannels/1"
    }
  }

  expect_failures = [check.alert_channels_enabled]
}

# ---- the app channel by id (D237) ---------------------------------------------------------------------

# The finding of 2026-11-18: the app channel resolved to the email channel, and every policy listed it
# twice. The email data source's postcondition fails the plan, not a check's warning.
run "the_app_channel_equal_to_the_email_channel_fails_the_plan" {
  command = plan

  variables {
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/1"
  }

  expect_failures = [data.google_monitoring_notification_channel.email]
}

# The same channel under the project's number: compared by the channel number, so the project segment
# can't hide it.
run "the_email_channel_under_the_project_number_fails_the_plan" {
  command = plan

  variables {
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/1"
  }

  override_data {
    target = data.google_monitoring_notification_channel.email
    values = {
      enabled = true
      name    = "projects/123456789012/notificationChannels/1"
    }
  }

  expect_failures = [data.google_monitoring_notification_channel.email]
}

# The whole number, not its end: 11 is not 1.
run "a_number_that_only_ends_alike_is_another_channel" {
  command = plan

  variables {
    serve             = true
    public_invokers   = true
    alert_app_channel = "projects/fake-for-tests/notificationChannels/11"
  }

  assert {
    condition = alltrue([
      for p in concat(google_monitoring_alert_policy.uptime, google_monitoring_alert_policy.notices, values(google_monitoring_alert_policy.promql)) :
      p.notification_channels == tolist(["projects/fake-for-tests/notificationChannels/1", "projects/fake-for-tests/notificationChannels/11"])
    ])
    error_message = "channel 11 is another channel than 1: the plan goes on, both in every policy"
  }
}

# The validation: a full resource name of a notification channel, or nothing.
run "a_display_name_is_refused" {
  command = plan

  variables {
    alert_app_channel = "fake-for-tests app device"
  }

  expect_failures = [var.alert_app_channel]
}

run "an_empty_string_is_refused" {
  command = plan

  variables {
    alert_app_channel = ""
  }

  expect_failures = [var.alert_app_channel]
}

run "another_collection_is_refused" {
  command = plan

  variables {
    alert_app_channel = "projects/fake-for-tests/alertPolicies/1"
  }

  expect_failures = [var.alert_app_channel]
}

run "a_trailing_slash_is_refused" {
  command = plan

  variables {
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2/"
  }

  expect_failures = [var.alert_app_channel]
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

  # D244: api's stdout only, the three messages, and the reject line but for reasons missing and expired
  # (a negative list: a new reason counts). Every OR group in parentheses: in Logging's query language OR
  # binds tighter than AND.
  assert {
    condition = (
      google_logging_metric.notices.filter == join("\n", [
        "resource.type=\"cloud_run_revision\"",
        "resource.labels.service_name=\"api\"",
        "logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout\"",
        "(jsonPayload.message=\"feedback received\" OR jsonPayload.message=\"admin signed in\" OR (jsonPayload.message=\"admin auth rejected\" AND NOT jsonPayload.reason=\"missing\" AND NOT jsonPayload.reason=\"expired\"))",
      ]) &&
      !strcontains(google_logging_metric.notices.filter, "%2Fstderr") &&
      !strcontains(google_logging_metric.notices.filter, "run.googleapis.com%2Frequests") &&
      !strcontains(google_logging_metric.notices.filter, "admin second factor required")
    )
    error_message = "ryft_notices counts api's stdout lines feedback received, admin signed in and admin auth rejected (not for reason missing or expired), nothing else"
  }

  assert {
    condition = (
      google_logging_metric.notices.label_extractors == tomap({ kind = "EXTRACT(jsonPayload.message)" }) &&
      [for d in google_logging_metric.notices.metric_descriptor : [d.metric_kind, d.value_type]] == [["DELTA", "INT64"]] &&
      [for l in google_logging_metric.notices.metric_descriptor[0].labels : l.key] == ["kind"]
    )
    error_message = "ryft_notices has one label, kind, the line's message (a closed set of three by the filter)"
  }

  assert {
    condition = length(regexall(
      "(?i)ip|user|email|mail|message_id|messageid|device|token",
      join(" ", concat(
        keys(google_logging_metric.ticks.label_extractors), values(google_logging_metric.ticks.label_extractors),
        keys(google_logging_metric.notices.label_extractors), values(google_logging_metric.notices.label_extractors),
      )),
    )) == 0
    error_message = "no label extracts an IP, a user, an email, a message id, a device or a token"
  }

  # The filter matches strings the api writes: a message or a reason renamed in Go would silence the
  # sixth condition with no test failing (D244). Pinned to the lines that write them.
  assert {
    condition = (
      strcontains(file("${path.module}/../../backend/cmd/api/feedback.go"), "h.log.InfoContext(r.Context(), \"feedback received\",") &&
      strcontains(file("${path.module}/../../backend/cmd/api/adminauth.go"), "a.log.InfoContext(r.Context(), \"admin signed in\")") &&
      strcontains(file("${path.module}/../../backend/cmd/api/adminauth.go"), "a.log.InfoContext(r.Context(), \"admin auth rejected\", \"reason\", reason)") &&
      strcontains(file("${path.module}/../../backend/cmd/api/adminauth.go"), "a.reject(w, r, \"missing\")") &&
      strcontains(file("${path.module}/../../backend/internal/auth/supabase.go"), "Unauthorized(\"expired\",")
    )
    error_message = "ryft_notices' messages and excluded reasons are the ones the api writes (feedback.go, adminauth.go, internal/auth/supabase.go)"
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
      alltrue([for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : try(t.xPos, 0) + t.width <= 48])
    )
    error_message = "the dashboard: 13 tiles on 48 columns, none past the edge"
  }

  # The API drops a zero position from what it returns; one written in the config would show the
  # dashboard changed at every plan.
  assert {
    condition = alltrue([
      for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles :
      try(t.xPos, 1) != 0 && try(t.yPos, 1) != 0
    ])
    error_message = "no tile writes xPos = 0 or yPos = 0 (the API leaves zeros out: a perpetual diff)"
  }

  assert {
    condition = (
      [for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : [try(t.xPos, 0), try(t.yPos, 0)]]
      == [[0, 0], [24, 0], [0, 16], [24, 16], [0, 32], [24, 32], [0, 48], [24, 48], [0, 64], [24, 64], [0, 80], [24, 80], [0, 96]]
    )
    error_message = "two tiles a row, then the logs panel on its own row"
  }

  # The AASA check matches the app id the site serves (web/public/.well-known).
  assert {
    condition = strcontains(
      file("${path.module}/../../web/public/.well-known/apple-app-site-association"),
      "\"${one(google_monitoring_uptime_check_config.this["ryft-site-aasa"].content_matchers).content}\"",
    )
    error_message = "the AASA check's content is the app id in web/public/.well-known/apple-app-site-association"
  }

  assert {
    condition = (
      [
        for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles :
        [t.widget.logsPanel.filter, t.widget.logsPanel.resourceNames] if can(t.widget.logsPanel)
        ] == [[
          "resource.type=\"cloud_run_revision\"\n(logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout\" OR logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Fstderr\")\nseverity>=ERROR",
          ["projects/ryftlab-prod/locations/europe-west1/buckets/ryft-logs/views/_AllLogs"],
      ]]
    )
    error_message = "one logs panel of the services' own ERROR lines (not Cloud Run's request log, which has IPs), read from ryft-logs"
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

# ---- Grafana is gone, and the app channel stays out of every committed file ---------------------------

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

run "the_app_channel_is_an_id_in_no_committed_file" {
  command = plan

  # D237: null by default (unset, the gate), and not sensitive (an opaque id the plan shows).
  assert {
    condition = (
      length(regexall("variable \"alert_app_channel\" \\{[^}]*\\n\\s*default\\s*=\\s*null\\n", file("${path.module}/variables.tf"))) == 1 &&
      length(regexall("variable \"alert_app_channel\" \\{[^}]*\\n\\s*(sensitive|ephemeral)\\s*=", file("${path.module}/variables.tf"))) == 0
    )
    error_message = "alert_app_channel defaults to null (unset: no alert policy) and is neither sensitive nor ephemeral (D237)"
  }

  # The committed tfvars set it nowhere (terraform.tfvars itself is the user's, and never read here), so a
  # plan without the workspace variable has it null.
  assert {
    condition = var.alert_app_channel == null && length(regexall(
      "(?m)^\\s*\"?alert_app_channel\"?\\s*[=:]",
      join("\n", concat(
        [for f in fileset(path.module, "*.auto.tfvars") : file("${path.module}/${f}")],
        [for f in fileset(path.module, "*.auto.tfvars.json") : file("${path.module}/${f}")],
        [file("${path.module}/terraform.tfvars.example")],
      )),
    )) == 0
    error_message = "no committed tfvars sets alert_app_channel: it is per device, a workspace variable only"
  }

  # No channel id in the root at all: neither a default nor a constant nor a tfvars value.
  assert {
    condition = length(regexall(
      "notificationChannels/[0-9]",
      join("\n", concat(
        [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")],
        [for f in fileset(path.module, "*.auto.tfvars") : file("${path.module}/${f}")],
        [for f in fileset(path.module, "*.auto.tfvars.json") : file("${path.module}/${f}")],
        [file("${path.module}/terraform.tfvars.example")],
      )),
    )) == 0
    error_message = "no .tf or committed tfvars holds a notification channel's id (the app's is a workspace variable, D237)"
  }

  # The tests set it to obviously fake values only: the project fake-for-tests, or the empty string the
  # validation refuses.
  assert {
    condition = (
      length(regexall(
        "alert_app_channel\\s*=\\s*\"",
        join("\n", [for f in fileset("${path.module}/tests", "*.tftest.hcl") : file("${path.module}/tests/${f}")]),
      )) > 0 &&
      alltrue([
        for v in flatten(regexall(
          "alert_app_channel\\s*=\\s*\"([^\"]*)\"",
          join("\n", [for f in fileset("${path.module}/tests", "*.tftest.hcl") : file("${path.module}/tests/${f}")]),
        )) : v == "" || strcontains(v, "fake-for-tests")
      ])
    )
    error_message = "every test sets alert_app_channel to a value in the fake project fake-for-tests (or to \"\"), never a real-looking one"
  }
}
