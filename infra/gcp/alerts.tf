# The Grafana-managed rules on Cloud Monitoring's metrics (design 6): what the platform reports, read
# where it is through the Google Cloud Monitoring data source (grafana-reader, D224). The app's own
# ryft_* rules stay Prometheus rules in deploy/grafana/alerts (make alerts-apply); none is repeated here.
#
# Gated: nothing exists until grafana_datasource_uid is set (runbook step 11), so applies 1 and 2 need
# no Grafana token. Evaluated every 5 minutes, not every minute: every evaluation is a Monitoring API
# read (design 6). Queries are PromQL on Google's metric names (run.googleapis.com/request_count is
# run_googleapis_com:request_count). Routed by severity, like every other rule (OBSERVABILITY -> Alerts).
locals {
  gcp_rules = {
    # More than 2 % of api or ingest requests answer 5xx, Cloud Run's own 5xx (no instance, timeouts)
    # included. The app-metric rule of the same name it replaces used the same 2 %.
    HTTP5xxRatio = {
      expr      = <<-EOT
        sum by (service_name) (rate(run_googleapis_com:request_count{monitored_resource="cloud_run_revision", service_name=~"api|ingest", response_code_class="5xx"}[10m]))
          / sum by (service_name) (rate(run_googleapis_com:request_count{monitored_resource="cloud_run_revision", service_name=~"api|ingest"}[10m]))
      EOT
      window    = 900
      threshold = 0.02
      for       = "10m"
      severity  = "critical"
      summary   = "{{ $labels.service_name }}: more than 2 % of requests answer 5xx"
      look      = "Logs Explorer: resource.type=\"cloud_run_revision\" resource.labels.service_name=\"<svc>\" httpRequest.status>=500"
    }

    # ingest's p95 latency over 10 s (milliseconds here). Cloud Run's latencies have no path, so this
    # is every ingest request, ticks and health checks included: the sync dominates it.
    SyncSlow = {
      expr      = <<-EOT
        histogram_quantile(0.95, sum by (le) (rate(run_googleapis_com:request_latencies_bucket{monitored_resource="cloud_run_revision", service_name="ingest"}[15m])))
      EOT
      window    = 1200
      threshold = 10000
      for       = "15m"
      severity  = "warning"
      summary   = "Syncs take over 10 s (ingest p95)"
      look      = "Cloud Run -> ingest -> Metrics (latency, instance count); Supabase's pooler"
    }

    # A letter waits in a DLQ hold subscription (replaces RabbitMQDeadLetters).
    DeadLetters = {
      expr      = <<-EOT
        max by (subscription_id) (pubsub_googleapis_com:subscription_num_undelivered_messages{monitored_resource="pubsub_subscription", subscription_id=~"dlq-.+-hold"})
      EOT
      window    = 900
      threshold = 0
      for       = "5m"
      severity  = "warning"
      summary   = "{{ $labels.subscription_id }} holds dead letters (scripts/dlq.sh list)"
      look      = "scripts/dlq.sh list; the last error is in the consumer's log, keyed by messageId"
    }

    # A scoring or notify subscription's oldest unacked message is over 15 minutes old (replaces
    # RabbitMQBacklog). Tick subscriptions keep messages 10 minutes, so they never reach it.
    PushBacklog = {
      expr      = <<-EOT
        max by (subscription_id) (pubsub_googleapis_com:subscription_oldest_unacked_message_age{monitored_resource="pubsub_subscription", subscription_id=~"(scoring|notify)-.+"})
      EOT
      window    = 900
      threshold = 900
      for       = "5m"
      severity  = "warning"
      summary   = "{{ $labels.subscription_id }}: the oldest event waits over 15 minutes"
      look      = "the consumer's request log (429, 5xx), its max instances, then DeadLetters"
    }

    # Cloud Run answered 429: no instance was free at max instances. On api and ingest a 429 can also
    # be the app's own limiter (that one is RateLimited, from the app's metrics).
    Saturated = {
      expr      = <<-EOT
        sum by (service_name) (increase(run_googleapis_com:request_count{monitored_resource="cloud_run_revision", response_code="429"}[15m]))
      EOT
      window    = 900
      threshold = 0
      for       = "5m"
      severity  = "warning"
      summary   = "{{ $labels.service_name }} answers 429 (saturated, or the app's limiter)"
      look      = "Logs Explorer: a 429 from Cloud Run says no available instance; the app's has its JSON body"
    }
  }
}

resource "grafana_folder" "gcp" {
  count = local.alerts ? 1 : 0

  uid   = "ryft-gcp"
  title = "RYFT GCP (infra/gcp)"
}

resource "grafana_rule_group" "gcp" {
  count = local.alerts ? 1 : 0

  name             = "ryft-gcp"
  folder_uid       = grafana_folder.gcp[0].uid
  interval_seconds = 300

  dynamic "rule" {
    for_each = local.gcp_rules

    content {
      name           = rule.key
      condition      = "C"
      for            = rule.value.for
      no_data_state  = "OK"
      exec_err_state = "Error"

      labels = {
        severity = rule.value.severity
      }

      annotations = {
        summary     = rule.value.summary
        description = "First look: ${rule.value.look}. docs/OPERATIONS.md -> GCP."
      }

      data {
        ref_id         = "A"
        datasource_uid = var.grafana_datasource_uid

        relative_time_range {
          from = rule.value.window
          to   = 0
        }

        model = jsonencode({
          refId     = "A"
          queryType = "promQL"
          promQLQuery = {
            projectName = var.project_id
            expr        = trimspace(rule.value.expr)
            step        = "60s"
          }
          intervalMs    = 60000
          maxDataPoints = 43200
        })
      }

      data {
        ref_id         = "B"
        datasource_uid = "__expr__"

        relative_time_range {
          from = 0
          to   = 0
        }

        model = jsonencode({
          refId      = "B"
          type       = "reduce"
          expression = "A"
          reducer    = "last"
          settings   = { mode = "dropNN" }
          datasource = { type = "__expr__", uid = "__expr__" }
        })
      }

      data {
        ref_id         = "C"
        datasource_uid = "__expr__"

        relative_time_range {
          from = 0
          to   = 0
        }

        model = jsonencode({
          refId      = "C"
          type       = "threshold"
          expression = "B"
          conditions = [{ evaluator = { type = "gt", params = [rule.value.threshold] } }]
          datasource = { type = "__expr__", uid = "__expr__" }
        })
      }
    }
  }
}
