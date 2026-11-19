# The edge (D242, edge.tf): each phase of var.edge set explicitly, so a flip of gates.auto.tfvars never
# changes what these runs pin. Plan-only, every provider mocked: this machine has real gcloud
# credentials, and a real provider here could create what it tests.
#   off     as before D242: no edge resource, 16 APIs, the sink without exclusions, today's dashboard,
#           api. and ingest. CNAMEs to the mappings
#   built   the load balancer, Cloud Armor, the certificates, 18 APIs, the sink's two exclusions, the
#           metrics and tiles; DNS still CNAMEs, the mappings stay
#   live    api. and ingest. A records at the address; the mappings stay
#   locked  no mapping; the A records stay
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
      name    = "projects/fake-for-tests/notificationChannels/1"
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

# See gates.tftest.hcl: the mappings' status is known only at apply.
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

# Google picks the address at apply. A documentation address (RFC 5737), never a real one.
override_resource {
  target          = google_compute_global_address.edge
  override_during = plan
  values = {
    address = "203.0.113.10"
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
  serve                      = true
  public_invokers            = true
}

# ---- off: the plan of the merge ---------------------------------------------------------------------------

run "off_is_as_before_the_edge" {
  command = plan

  variables {
    edge = "off"
  }

  assert {
    condition = (
      length(google_compute_global_address.edge) == 0 &&
      length(google_compute_region_network_endpoint_group.edge) == 0 &&
      length(google_compute_backend_service.edge) == 0 &&
      length(google_compute_security_policy.edge) == 0 &&
      length(google_compute_url_map.edge) == 0 &&
      length(google_compute_url_map.edge_http) == 0 &&
      length(google_compute_ssl_policy.edge) == 0 &&
      length(google_compute_target_https_proxy.edge) == 0 &&
      length(google_compute_target_http_proxy.edge) == 0 &&
      length(google_compute_global_forwarding_rule.edge_https) == 0 &&
      length(google_compute_global_forwarding_rule.edge_http) == 0 &&
      length(google_certificate_manager_dns_authorization.edge) == 0 &&
      length(google_dns_record_set.edge_dns_authorization) == 0 &&
      length(google_certificate_manager_certificate.edge) == 0 &&
      length(google_certificate_manager_certificate_map.edge) == 0 &&
      length(google_certificate_manager_certificate_map_entry.edge) == 0 &&
      length(google_logging_metric.edge) == 0
    )
    error_message = "edge off: no edge resource at all"
  }

  assert {
    condition = (
      length(google_project_service.keep) == 16 &&
      !contains(keys(google_project_service.keep), "compute.googleapis.com") &&
      !contains(keys(google_project_service.keep), "certificatemanager.googleapis.com")
    )
    error_message = "edge off: the keep list is step 2a's 16, without Compute Engine and Certificate Manager"
  }

  assert {
    condition     = length(google_logging_project_sink.ryft_logs.exclusions) == 0 && google_logging_project_sink.ryft_logs.filter == null
    error_message = "edge off: the sink takes everything, no exclusion"
  }

  # Today's tiles, in today's order (tests/monitoring.tftest.hcl pins their positions).
  assert {
    condition = [for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : t.widget.title] == [
      "Requests per second, by service",
      "5xx per second, by service",
      "p95 latency (ms), by service (SyncSlow: ingest over 10,000)",
      "Instances, by service and state",
      "429 per second, by service (Saturated: Cloud Run's or the app's limiter)",
      "Ticks per hour, by service and result",
      "Oldest unacked event (s), by subscription",
      "Undelivered events, by subscription",
      "Dead letters held (scripts/dlq.sh list)",
      "ERROR lines per hour, by service",
      "Uptime: share of locations passing, by check",
      "Certificate: days left, by check",
      "ERROR lines (the services' own)",
    ]
    error_message = "edge off: the dashboard has today's 13 tiles and nothing of the edge"
  }

  assert {
    condition = (
      toset(keys(google_dns_record_set.host)) == toset(["api", "ingest"]) &&
      alltrue([
        for k, r in google_dns_record_set.host :
        r.name == "${k}.ryftlab.de." && r.type == "CNAME" && r.ttl == 300 && r.rrdatas == tolist(["ghs.googlehosted.com."])
      ]) &&
      toset(keys(google_cloud_run_domain_mapping.host)) == toset(["api", "ingest"])
    )
    error_message = "edge off: api. and ingest. are CNAMEs (TTL 300) from their mappings, which exist"
  }

  assert {
    condition     = output.edge_ip == null && output.monitoring.log_metrics == tolist(["ryft_errors", "ryft_notices", "ryft_ticks"])
    error_message = "edge off: no address, and the log-based metrics of D236 and D244 only"
  }
}

# ---- built: everything, nothing pointing at it ------------------------------------------------------------

run "built_has_the_whole_edge" {
  command = plan

  variables {
    edge = "built"
  }

  assert {
    condition = toset(keys(google_project_service.keep)) == toset([
      "artifactregistry.googleapis.com", "billingbudgets.googleapis.com", "cloudbilling.googleapis.com",
      "cloudresourcemanager.googleapis.com", "cloudscheduler.googleapis.com", "cloudtrace.googleapis.com",
      "dns.googleapis.com", "iam.googleapis.com", "iamcredentials.googleapis.com", "logging.googleapis.com",
      "monitoring.googleapis.com", "pubsub.googleapis.com", "run.googleapis.com", "secretmanager.googleapis.com",
      "serviceusage.googleapis.com", "sts.googleapis.com",
      "compute.googleapis.com", "certificatemanager.googleapis.com",
    ]) && length(google_project_service.keep) == 18 && alltrue([for s in google_project_service.keep : s.disable_on_destroy == false])
    error_message = "edge built: the 16 and Compute Engine and Certificate Manager, 18; a destroy disables none"
  }

  # IPv4 only (D242): one global address, no IPv6 address and no AAAA record anywhere in the root.
  assert {
    condition = (
      length(google_compute_global_address.edge) == 1 &&
      google_compute_global_address.edge[0].name == "ryft-edge" &&
      google_compute_global_address.edge[0].ip_version == "IPV4" &&
      google_compute_global_address.edge[0].address_type == "EXTERNAL" &&
      length(regexall("(?m)^\\s*resource\\s+\"google_compute_global_address\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      length(regexall("(?m)^[^#\\n]*(\"IPV6\"|\"AAAA\")", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    )
    error_message = "edge built: one global IPv4 address (EXTERNAL), no IPv6 address, no AAAA record"
  }

  assert {
    condition = (
      toset(keys(google_compute_region_network_endpoint_group.edge)) == toset(["api", "ingest"]) &&
      alltrue([
        for k, n in google_compute_region_network_endpoint_group.edge :
        n.name == "ryft-edge-${k}" && n.region == "europe-west1" && n.network_endpoint_type == "SERVERLESS" &&
        [for c in n.cloud_run : c.service] == [k]
      ])
    )
    error_message = "edge built: a serverless NEG per service in europe-west1, on the Cloud Run service of that name"
  }

  # Each backend: its NEG, the policy, every request logged; no health check, no CDN.
  assert {
    condition = (
      toset(keys(google_compute_backend_service.edge)) == toset(["api", "ingest"]) &&
      alltrue([
        for k, b in google_compute_backend_service.edge :
        b.name == "ryft-edge-${k}" &&
        b.load_balancing_scheme == "EXTERNAL_MANAGED" &&
        b.protocol == "HTTP" &&
        b.enable_cdn == false &&
        b.security_policy == "projects/ryftlab-prod/global/securityPolicies/ryft-edge" &&
        [for x in b.backend : x.group] == ["projects/ryftlab-prod/regions/europe-west1/networkEndpointGroups/ryft-edge-${k}"] &&
        [for l in b.log_config : [l.enable, l.sample_rate]] == [[true, 1]] &&
        try(length(b.health_checks), 0) == 0
      ]) &&
      length(regexall("(?m)^\\s*timeout_sec\\s*=", file("${path.module}/edge.tf"))) == 0
    )
    error_message = "edge built: each backend service on its NEG, with the policy ryft-edge, logging every request (1.0), no health check, no timeout_sec, no CDN"
  }

  # By host, and any other host to the site without a backend.
  assert {
    condition = (
      length(google_compute_url_map.edge) == 1 &&
      toset([for h in google_compute_url_map.edge[0].host_rule : "${one(h.hosts)} => ${h.path_matcher}"]) == toset(["api.ryftlab.de => api", "ingest.ryftlab.de => ingest"]) &&
      tomap({ for p in google_compute_url_map.edge[0].path_matcher : p.name => p.default_service }) == tomap({
        api    = "projects/ryftlab-prod/global/backendServices/ryft-edge-api"
        ingest = "projects/ryftlab-prod/global/backendServices/ryft-edge-ingest"
      }) &&
      google_compute_url_map.edge[0].default_service == null &&
      [for r in google_compute_url_map.edge[0].default_url_redirect : [r.https_redirect, r.host_redirect, r.path_redirect, r.strip_query, r.redirect_response_code]] == [[true, "ryftlab.de", "/", true, "MOVED_PERMANENTLY_DEFAULT"]]
    )
    error_message = "edge built: api.ryftlab.de to api, ingest.ryftlab.de to ingest; any other host 301 to https://ryftlab.de/, no default backend"
  }

  assert {
    condition = (
      length(google_compute_ssl_policy.edge) == 1 &&
      google_compute_ssl_policy.edge[0].profile == "MODERN" &&
      google_compute_ssl_policy.edge[0].min_tls_version == "TLS_1_2"
    )
    error_message = "edge built: the SSL policy is MODERN, TLS 1.2 or newer (Google's default admits 1.0)"
  }

  assert {
    condition = (
      length(google_compute_target_https_proxy.edge) == 1 &&
      google_compute_target_https_proxy.edge[0].url_map == "projects/ryftlab-prod/global/urlMaps/ryft-edge" &&
      google_compute_target_https_proxy.edge[0].ssl_policy == "projects/ryftlab-prod/global/sslPolicies/ryft-edge" &&
      google_compute_target_https_proxy.edge[0].certificate_map == "//certificatemanager.googleapis.com/projects/ryftlab-prod/locations/global/certificateMaps/ryft-edge" &&
      google_compute_global_forwarding_rule.edge_https[0].port_range == "443" &&
      google_compute_global_forwarding_rule.edge_https[0].load_balancing_scheme == "EXTERNAL_MANAGED" &&
      google_compute_global_forwarding_rule.edge_https[0].ip_address == "projects/ryftlab-prod/global/addresses/ryft-edge" &&
      google_compute_global_forwarding_rule.edge_https[0].target == "projects/ryftlab-prod/global/targetHttpsProxies/ryft-edge"
    )
    error_message = "edge built: 443 on the address, to the HTTPS proxy with the URL map, the SSL policy and the certificate map"
  }

  # 80 redirects to HTTPS on the same address and reaches no backend.
  assert {
    condition = (
      google_compute_global_forwarding_rule.edge_http[0].port_range == "80" &&
      google_compute_global_forwarding_rule.edge_http[0].load_balancing_scheme == "EXTERNAL_MANAGED" &&
      google_compute_global_forwarding_rule.edge_http[0].ip_address == "projects/ryftlab-prod/global/addresses/ryft-edge" &&
      google_compute_global_forwarding_rule.edge_http[0].target == "projects/ryftlab-prod/global/targetHttpProxies/ryft-edge-http" &&
      google_compute_target_http_proxy.edge[0].url_map == "projects/ryftlab-prod/global/urlMaps/ryft-edge-http" &&
      google_compute_url_map.edge_http[0].default_service == null &&
      length(google_compute_url_map.edge_http[0].host_rule) == 0 &&
      [for r in google_compute_url_map.edge_http[0].default_url_redirect : [r.https_redirect, r.strip_query, r.redirect_response_code]] == [[true, false, "MOVED_PERMANENTLY_DEFAULT"]]
    )
    error_message = "edge built: 80 on the same address, 301 to https:// for every host, no backend"
  }

  # Certificate Manager with DNS authorization: each certificate goes ACTIVE before any traffic moves.
  assert {
    condition = (
      toset(keys(google_certificate_manager_dns_authorization.edge)) == toset(["api", "ingest"]) &&
      alltrue([for k, a in google_certificate_manager_dns_authorization.edge : a.domain == "${k}.ryftlab.de" && a.location == "global"]) &&
      toset(keys(google_dns_record_set.edge_dns_authorization)) == toset(["api", "ingest"]) &&
      alltrue([for r in google_dns_record_set.edge_dns_authorization : r.managed_zone == "ryftlab-de" && r.ttl == 300]) &&
      alltrue([
        for k, c in google_certificate_manager_certificate.edge :
        c.location == "global" &&
        [for m in c.managed : [m.domains, m.dns_authorizations]] == [[
          tolist(["${k}.ryftlab.de"]),
          tolist(["projects/ryftlab-prod/locations/global/dnsAuthorizations/ryft-edge-${k}"]),
        ]]
      ]) &&
      length(google_certificate_manager_certificate.edge) == 2 &&
      length(google_certificate_manager_certificate_map.edge) == 1 &&
      alltrue([
        for k, e in google_certificate_manager_certificate_map_entry.edge :
        e.map == "ryft-edge" && e.hostname == "${k}.ryftlab.de" &&
        e.certificates == tolist(["projects/ryftlab-prod/locations/global/certificates/ryft-edge-${k}"])
      ]) &&
      length(google_certificate_manager_certificate_map_entry.edge) == 2
    )
    error_message = "edge built: per host a DNS authorization with its CNAME in the zone, a managed certificate by that authorization, and a map entry for the hostname"
  }

  # Nothing points at it yet.
  assert {
    condition = (
      alltrue([for r in google_dns_record_set.host : r.type == "CNAME" && r.rrdatas == tolist(["ghs.googlehosted.com."]) && r.ttl == 300]) &&
      length(google_dns_record_set.host) == 2 &&
      toset(keys(google_cloud_run_domain_mapping.host)) == toset(["api", "ingest"])
    )
    error_message = "edge built: api. and ingest. are still CNAMEs to the mappings, which exist"
  }

  assert {
    condition     = output.edge_ip == "203.0.113.10"
    error_message = "edge built: the output edge_ip is the address (mocked here)"
  }
}

# ---- the policy ---------------------------------------------------------------------------------------------

run "the_policy" {
  command = plan

  variables {
    edge = "built"
  }

  assert {
    condition = (
      length(google_compute_security_policy.edge) == 1 &&
      google_compute_security_policy.edge[0].name == "ryft-edge" &&
      google_compute_security_policy.edge[0].type == "CLOUD_ARMOR" &&
      [for a in google_compute_security_policy.edge[0].advanced_options_config : [a.json_parsing, a.log_level]] == [["STANDARD", "NORMAL"]] &&
      length([for r in google_compute_security_policy.edge[0].rule : r.priority]) == 8
    )
    error_message = "the policy: Cloud Armor, JSON parsing STANDARD, log level NORMAL (never VERBOSE: body snippets), 8 rules"
  }

  # The rate rule, enforced, keyed on the address the load balancer sees, exactly D242's values.
  assert {
    condition = [
      for r in google_compute_security_policy.edge[0].rule : [
        r.priority, r.preview,
        r.match[0].versioned_expr, r.match[0].config[0].src_ip_ranges,
        r.rate_limit_options[0].enforce_on_key, r.rate_limit_options[0].conform_action, r.rate_limit_options[0].exceed_action,
        r.rate_limit_options[0].rate_limit_threshold[0].count, r.rate_limit_options[0].rate_limit_threshold[0].interval_sec,
        r.rate_limit_options[0].ban_threshold[0].count, r.rate_limit_options[0].ban_threshold[0].interval_sec,
        r.rate_limit_options[0].ban_duration_sec,
      ] if r.action == "rate_based_ban"
      ] == [[
        2000, false,
        "SRC_IPS_V1", toset(["*"]),
        "IP", "allow", "deny(429)",
        300, 10,
        6000, 300,
        600,
    ]]
    error_message = "the rate rule: one rate_based_ban at 2000, enforced, every address, key IP, conform allow, exceed deny(429), 300 per 10 s, ban at 6,000 per 300 s for 600 s"
  }

  assert {
    condition     = length(regexall("XFF_IP|enforce_on_key_name", file("${path.module}/edge.tf"))) == 1
    error_message = "no rate key from a header a client writes: edge.tf names XFF_IP only in the comment that rules it out"
  }

  # The six WAF sets, one rule each, all in preview, each before the rate rule (whose allow ends the
  # evaluation). An enforcement PR sets its set's line in edge.tf to false and its pin here.
  assert {
    condition = tomap({
      for r in google_compute_security_policy.edge[0].rule :
      regex("^evaluatePreconfiguredWaf\\('([a-z]+-v33-stable)', \\{'sensitivity': 1\\}\\)$", r.match[0].expr[0].expression)[0] => [r.action, r.preview, r.priority < 2000]
      if length(r.match[0].expr) > 0
      }) == tomap({
      "sqli-v33-stable"             = ["deny(403)", true, true]
      "xss-v33-stable"              = ["deny(403)", true, true]
      "lfi-v33-stable"              = ["deny(403)", true, true]
      "rce-v33-stable"              = ["deny(403)", true, true]
      "scannerdetection-v33-stable" = ["deny(403)", true, true]
      "protocolattack-v33-stable"   = ["deny(403)", true, true]
    })
    error_message = "the WAF: sqli, xss, lfi, rce, scannerdetection and protocolattack (v33-stable, sensitivity 1), one rule each, deny(403) in preview, each at a priority before the rate rule's 2000"
  }

  assert {
    condition = length(distinct([
      for r in google_compute_security_policy.edge[0].rule : r.priority if length(r.match[0].expr) > 0
    ])) == 6
    error_message = "the WAF rules have six priorities of their own"
  }

  # No geo blocking: allow for everything else.
  assert {
    condition = [
      for r in google_compute_security_policy.edge[0].rule : [r.action, r.preview, r.match[0].versioned_expr, r.match[0].config[0].src_ip_ranges]
      if r.priority == 2147483647
    ] == [["allow", false, "SRC_IPS_V1", toset(["*"])]]
    error_message = "the default rule (2147483647) allows every address"
  }
}

# ---- the logs: what the sink keeps, and the two counters ----------------------------------------------------

run "built_logs_keep_only_denials_and_preview_matches" {
  command = plan

  variables {
    edge = "built"
  }

  assert {
    condition = (
      [for e in google_logging_project_sink.ryft_logs.exclusions : e.name] == ["lb-requests-unflagged", "run-requests"] &&
      google_logging_project_sink.ryft_logs.filter == null &&
      google_logging_project_sink.ryft_logs.destination == "logging.googleapis.com/projects/ryftlab-prod/locations/europe-west1/buckets/ryft-logs"
    )
    error_message = "edge built: everything into ryft-logs but exactly the two exclusions"
  }

  assert {
    condition = [
      for e in google_logging_project_sink.ryft_logs.exclusions : e.filter if e.name == "lb-requests-unflagged"
      ] == [join("\n", [
        "resource.type=\"http_load_balancer\"",
        "NOT (jsonPayload.enforcedSecurityPolicy.outcome=\"DENY\" OR jsonPayload.previewSecurityPolicy.outcome=\"DENY\")",
    ])]
    error_message = "the load balancer's entries go unless Cloud Armor denied the request or a preview rule matched it"
  }

  assert {
    condition = [
      for e in google_logging_project_sink.ryft_logs.exclusions : e.filter if e.name == "run-requests"
      ] == [join("\n", [
        "logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Frequests\"",
        "resource.labels.service_name=(\"api\" OR \"ingest\")",
        "httpRequest.status<500",
    ])]
    error_message = "api's and ingest's Cloud Run request log goes below 5xx (every caller's IP, URL and user agent; the user's decision, D242); their 5xx and scoring's and notify's stay"
  }

  # Neither drops the services' own lines, the metrics' and the runbook's source.
  assert {
    condition = alltrue([
      for e in google_logging_project_sink.ryft_logs.exclusions :
      !strcontains(e.filter, "run.googleapis.com%2Fstdout") && !strcontains(e.filter, "run.googleapis.com%2Fstderr") && !strcontains(e.filter, "cloud_run_revision")
    ])
    error_message = "no exclusion touches the services' stdout or stderr"
  }

  assert {
    condition = (
      toset(keys(google_logging_metric.edge)) == toset(["ryft_edge_denials", "ryft_edge_preview_matches"]) &&
      google_logging_metric.edge["ryft_edge_denials"].filter == "resource.type=\"http_load_balancer\"\njsonPayload.enforcedSecurityPolicy.outcome=\"DENY\"" &&
      google_logging_metric.edge["ryft_edge_preview_matches"].filter == "resource.type=\"http_load_balancer\"\njsonPayload.previewSecurityPolicy.outcome=\"DENY\"" &&
      google_logging_metric.edge["ryft_edge_denials"].label_extractors == tomap({ priority = "EXTRACT(jsonPayload.enforcedSecurityPolicy.priority)" }) &&
      google_logging_metric.edge["ryft_edge_preview_matches"].label_extractors == tomap({ priority = "EXTRACT(jsonPayload.previewSecurityPolicy.priority)" }) &&
      alltrue([
        for m in google_logging_metric.edge :
        [for d in m.metric_descriptor : [d.metric_kind, d.value_type]] == [["DELTA", "INT64"]] &&
        [for l in m.metric_descriptor[0].labels : l.key] == ["priority"]
      ])
    )
    error_message = "edge built: denials and preview matches counted by rule priority, their one label"
  }

  # Labels hold closed sets only: never the IP (rateLimitAction.key holds it), a URL or a user agent.
  assert {
    condition = length(regexall(
      "(?i)httpRequest|remoteIp|rateLimitAction|requestUrl|userAgent|\\.key\\b",
      join(" ", flatten([for m in google_logging_metric.edge : concat(keys(m.label_extractors), values(m.label_extractors))])),
    )) == 0
    error_message = "no label of the edge's metrics extracts an IP, a rate-limit key, a URL or a user agent"
  }

  assert {
    condition     = output.monitoring.log_metrics == tolist(["ryft_edge_denials", "ryft_edge_preview_matches", "ryft_errors", "ryft_notices", "ryft_ticks"])
    error_message = "the monitoring output lists the five log-based metrics"
  }
}

# ---- the dashboard's tiles, and no alert of the edge's own ----------------------------------------------------

run "built_dashboard_has_the_edge_tiles" {
  command = plan

  variables {
    edge = "built"
  }

  assert {
    condition = (
      length(jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles) == 17 &&
      [for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : t.widget.title if startswith(t.widget.title, "Edge:") || startswith(t.widget.title, "Cloud Armor:")] == [
        "Edge: requests per second, by backend and code class",
        "Edge: 429 per second, by backend (Cloud Armor's and the services')",
        "Cloud Armor: requests per second, blocked or not, by backend",
        "Cloud Armor: denials and preview matches per hour, by rule priority",
      ]
    )
    error_message = "edge built: 17 tiles, the four edge tiles among them"
  }

  # Two a row, the logs panel on its own row below; the zero-xPos rule (monitoring.tf) holds.
  assert {
    condition = (
      [for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : [try(t.xPos, 0), try(t.yPos, 0), t.width]] == concat(
        [for i in range(16) : [(i % 2) * 24, floor(i / 2) * 16, 24]],
        [[0, 128, 48]],
      ) &&
      alltrue([for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : try(t.xPos, 1) != 0 && try(t.yPos, 1) != 0])
    )
    error_message = "edge built: two tiles a row, the logs panel at y 128 across the row; no tile writes xPos or yPos 0"
  }

  # The queries as the dashboard holds them (decoded: in the JSON text every quote is escaped).
  assert {
    condition = alltrue([
      for m in [
        "sum by (backend_target_name, response_code_class) (rate(loadbalancing_googleapis_com:https_request_count{monitored_resource=\"https_lb_rule\"}[5m]))",
        "sum by (backend_target_name) (rate(loadbalancing_googleapis_com:https_request_count{monitored_resource=\"https_lb_rule\", response_code=\"429\"}[5m]))",
        "sum by (backend_target_name, blocked) (rate(networksecurity_googleapis_com:https_request_count{monitored_resource=\"network_security_policy\"}[5m]))",
        "label_replace(sum by (priority) (increase(logging_googleapis_com:user_ryft_edge_denials[1h])), \"kind\", \"denied\", \"\", \"\")",
        "label_replace(sum by (priority) (increase(logging_googleapis_com:user_ryft_edge_preview_matches[1h])), \"kind\", \"preview\", \"\", \"\")",
        ] : strcontains(join("\n", [
          for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles :
          try(t.widget.xyChart.dataSets[0].timeSeriesQuery.prometheusQuery, "")
      ]), m)
    ])
    error_message = "the edge tiles: the load balancer's requests by backend and code class and its 429s by backend, Cloud Armor's blocked or not by backend, the denials and preview matches by priority"
  }

  # The logs panel is unchanged: the services' own ERROR lines.
  assert {
    condition = [
      for t in jsondecode(google_monitoring_dashboard.overview.dashboard_json).mosaicLayout.tiles : t.widget.logsPanel.filter if can(t.widget.logsPanel)
    ] == ["resource.type=\"cloud_run_revision\"\n(logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout\" OR logName=\"projects/ryftlab-prod/logs/run.googleapis.com%2Fstderr\")\nseverity>=ERROR"]
    error_message = "edge built: the logs panel still reads the services' own ERROR lines"
  }

  # The uptime checks keep their hosts: the app's names, whichever way DNS points.
  assert {
    condition = (
      length(google_monitoring_uptime_check_config.this) == 5 &&
      google_monitoring_uptime_check_config.this["ryft-api-ready"].monitored_resource[0].labels["host"] == "api.ryftlab.de" &&
      google_monitoring_uptime_check_config.this["ryft-ingest-ready"].monitored_resource[0].labels["host"] == "ingest.ryftlab.de"
    )
    error_message = "edge built: the five uptime checks, api's and ingest's on their own hosts"
  }
}

run "built_with_alerting_still_has_six_conditions" {
  command = plan

  variables {
    edge              = "built"
    alert_app_channel = "projects/fake-for-tests/notificationChannels/2"
  }

  assert {
    condition = length(flatten([
      for p in concat(google_monitoring_alert_policy.uptime, google_monitoring_alert_policy.notices, values(google_monitoring_alert_policy.promql)) : p.conditions
    ])) == 6
    error_message = "the edge adds no alert condition: six, the cap (D236, D244)"
  }
}

# ---- live and locked ---------------------------------------------------------------------------------------

run "live_points_the_hosts_at_the_edge" {
  command = plan

  variables {
    edge = "live"
  }

  assert {
    condition = (
      toset(keys(google_dns_record_set.host)) == toset(["api", "ingest"]) &&
      alltrue([
        for k, r in google_dns_record_set.host :
        r.name == "${k}.ryftlab.de." && r.type == "A" && r.ttl == 300 && r.rrdatas == tolist(["203.0.113.10"])
      ])
    )
    error_message = "edge live: api. and ingest. are A records at the edge's address, TTL 300"
  }

  # Cloud DNS holds no CNAME next to an A: none of the root's CNAME sets is at either name.
  assert {
    condition = length([
      for r in concat(values(google_dns_record_set.mail_cname), google_dns_record_set.site_www_cname) : r.name
      if contains(["api.ryftlab.de.", "ingest.ryftlab.de."], r.name)
    ]) == 0
    error_message = "edge live: no CNAME at api. or ingest."
  }

  assert {
    condition     = toset(keys(google_cloud_run_domain_mapping.host)) == toset(["api", "ingest"]) && length(google_compute_global_address.edge) == 1
    error_message = "edge live: the mappings stay (the way back), the edge too"
  }
}

run "locked_drops_the_mappings" {
  command = plan

  variables {
    edge = "locked"
  }

  assert {
    condition     = length(google_cloud_run_domain_mapping.host) == 0
    error_message = "edge locked: no domain mapping"
  }

  assert {
    condition = (
      length(google_dns_record_set.host) == 2 &&
      alltrue([for r in google_dns_record_set.host : r.type == "A" && r.ttl == 300 && r.rrdatas == tolist(["203.0.113.10"])]) &&
      length(google_compute_backend_service.edge) == 2 &&
      length(google_compute_global_forwarding_rule.edge_https) == 1 &&
      length(google_logging_project_sink.ryft_logs.exclusions) == 2
    )
    error_message = "edge locked: the A records, the edge and the sink's exclusions stay"
  }

  # The invokers and the deployer's bindings don't follow the edge.
  assert {
    condition = (
      toset(keys(google_cloud_run_v2_service_iam_binding.invoker)) == toset(["api", "ingest", "scoring", "notify"]) &&
      length(google_cloud_run_v2_service_iam_member.deployer_run_developer) == 4
    )
    error_message = "edge locked: the invoker bindings and the deployer's stay"
  }
}

# ---- the gate's validation ---------------------------------------------------------------------------------

run "an_edge_without_serve_is_refused" {
  command = plan

  variables {
    serve = false
    edge  = "built"
  }

  expect_failures = [var.edge]
}

run "an_unknown_phase_is_refused" {
  command = plan

  variables {
    edge = "on"
  }

  expect_failures = [var.edge]
}
