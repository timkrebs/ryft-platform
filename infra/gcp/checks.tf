# Misconfiguration that fails silently: a warning on every plan and apply until it is fixed.

# With _Default still on, every log also lands in the global _Default bucket for 30 days, where the
# privacy text promises 14 days in the EU (design 6). Switched off by hand once (logging.tf).
check "default_log_sink_disabled" {
  data "google_logging_sink" "default" {
    id = "projects/${var.project_id}/sinks/_Default"
  }

  assert {
    condition     = data.google_logging_sink.default.disabled
    error_message = "The _Default log sink is on, so logs also go to the global _Default bucket for 30 days. Once: gcloud logging sinks update _Default --disabled --project=${var.project_id} (docs/OPERATIONS.md -> GCP, step 4)."
  }
}

# A mapping whose certificate never issues looks fine in the plan. Pending is normal for 15 minutes to
# 24 hours after apply 2; after 24 hours it is step 9's stop signal (design 1, the fallback).
check "domain_mappings_ready" {
  assert {
    condition = alltrue([
      for m in google_cloud_run_domain_mapping.host :
      anytrue([for c in flatten(m.status[*].conditions) : c.type == "Ready" && c.status == "True"])
    ])
    error_message = "A domain mapping is not Ready (its certificate is pending). gcloud beta run domain-mappings describe --domain=<host> --region=${var.region} names the condition."
  }
}

# A disabled channel is still a valid notification_channels entry: the policy opens incidents and nobody
# hears of them (D236). Both are read only once the alerting gate is on (alerts.tf).
check "alert_channels_enabled" {
  assert {
    condition = alltrue([
      for c in concat(data.google_monitoring_notification_channel.email, data.google_monitoring_notification_channel.app) : c.enabled
    ])
    error_message = "A notification channel of the alert policies is disabled, so its alerts reach nobody. Monitoring -> Alerting -> Edit notification channels: enable it (docs/OPERATIONS.md -> GCP, step 11)."
  }
}
