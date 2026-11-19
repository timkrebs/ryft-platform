# The edge (D242): a global external Application Load Balancer with Cloud Armor in front of api and
# ingest. Gated by var.edge (gates.auto.tfvars, one phase a PR; runbook step 16):
#   built   everything in this file; nothing points at it (DNS still names the domain mappings);
#   live    api. and ingest. are A records at its address (dns.tf);
#   locked  the domain mappings are gone (run.tf), after the deploy PR's ingress
#           internal-and-cloud-load-balancing and XFF_TRUSTED_HOPS=2 (scripts/check_cloudrun.py
#           enforces that order against this gate, D242).
# The sink's exclusions are in logging.tf, the two log-based metrics and the dashboard's tiles in
# monitoring.tf.
#
# IPv4 only, on purpose: Cloud Armor documents no prefix key for a rate limit, so an IPv6 client could
# take a new key per address of its /64 (and a /48 holds 65,536 of them). Without an AAAA record an
# IPv6-only phone comes in through its carrier's NAT64, and once locked no IPv6 address reaches D240's
# per-address limit at all.
#
# References between these resources are built from the resources' own names, so the plan shows them
# in full (the same strings as their ids) and each still depends on what it names; locals.tf's
# accounts do the same.
locals {
  # Host label => the Cloud Run service behind it: the mapped hosts (run.tf), only once built.
  edge_hosts = local.edge_built ? local.mapped_hosts : {}

  edge_name = "ryft-edge"

  # Cloud Armor's preconfigured WAF rules, one rule per set (v33-stable, sensitivity 1), so each is
  # enforced on its own: set => preview. Enforcing one is its line here set to false, in a PR of one
  # line, after 7 days of real traffic without a preview match at its priority (runbook step 16 ->
  # Enforcing a WAF rule). In preview a match is logged and counted (monitoring.tf), never refused.
  edge_waf_preview = {
    "sqli-v33-stable"             = true
    "xss-v33-stable"              = true
    "lfi-v33-stable"              = true
    "rce-v33-stable"              = true
    "scannerdetection-v33-stable" = true
    "protocolattack-v33-stable"   = true
  }

  # Set => its rule's priority. All before the rate rule: a throttle's conform action allow ends the
  # evaluation, so a rule after it would never run.
  edge_waf_priority = {
    "sqli-v33-stable"             = 1000
    "xss-v33-stable"              = 1001
    "lfi-v33-stable"              = 1002
    "rce-v33-stable"              = 1003
    "scannerdetection-v33-stable" = 1004
    "protocolattack-v33-stable"   = 1005
  }

  # The rate rule, enforced, keyed on the address the load balancer sees (IP; never XFF_IP, which a
  # client writes). 300 requests per 10 s is above the most D53's bucket in one instance admits in 10 s
  # (50 + 20 x 10 = 250), so the instance's own limit acts first; above one user's per-user ceilings on
  # both hosts together (API.md: api 60 + 10/s, 160 per 10 s; ingest's sync 60 + 2/s, 80: 240); below
  # three api instances' worth (750), so a flood spread over instances ends here. The ban counts only
  # the requests over the rate limit (the API reference, search): 6,000 of them in 300 s is about 50 a
  # second for 5 minutes, which no app does, and it lasts 10 minutes. Google's limits (search):
  # interval_sec one of 10, 30, 60, 120, 180, 240, 300, 600, 900, 1200, 1800, 2700, 3600; a
  # rate_based_ban's counts 1 to 10,000; ban_duration_sec one of 60, 120, 180, 240, 300, 600, ... The
  # uptime checks (2 hosts x 3 locations every 5 minutes), the smoke (sequential) and the tick (Pub/Sub
  # to run.app, never through here) are nowhere near it.
  edge_rate = {
    priority         = 2000
    count            = 300
    interval_sec     = 10
    ban_count        = 6000
    ban_interval_sec = 300
    ban_duration_sec = 600
  }
}

resource "google_compute_global_address" "edge" {
  count = local.edge_built ? 1 : 0

  project      = var.project_id
  name         = local.edge_name
  description  = "The edge's IPv4 address: api. and ingest.ryftlab.de from edge = live (infra/gcp, D242)"
  ip_version   = "IPV4"
  address_type = "EXTERNAL"
  # A global address is always Premium tier: the argument exists only on regional addresses.

  depends_on = [google_project_service.keep]
}

# One serverless NEG per service. Cloud Run's own service by name: CI owns it (deploy/run, D223).
resource "google_compute_region_network_endpoint_group" "edge" {
  for_each = local.edge_hosts

  project               = var.project_id
  region                = var.region
  name                  = "${local.edge_name}-${each.key}"
  description           = "Cloud Run ${each.value} behind the edge (infra/gcp, D242)"
  network_endpoint_type = "SERVERLESS"

  cloud_run {
    service = each.value
  }

  depends_on = [google_project_service.keep]
}

# No health check (a serverless NEG takes none), no timeout_sec (not allowed with one: Cloud Run's own
# request timeout holds, deploy/run), no CDN. The protocol: Google's setup of a global external
# Application Load Balancer with a serverless NEG (gcloud compute backend-services create
# --load-balancing-scheme=EXTERNAL_MANAGED --global) passes none, so the API's default, HTTP; Google
# documents it as ignored for serverless NEGs (search). Written out so the plan shows it. Every request
# is logged (sample 1.0); the sink keeps only those Cloud Armor denied or a preview rule flagged
# (logging.tf).
resource "google_compute_backend_service" "edge" {
  for_each = local.edge_hosts

  project               = var.project_id
  name                  = "${local.edge_name}-${each.key}"
  description           = "Cloud Run ${each.value} with Cloud Armor (infra/gcp, D242)"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  protocol              = "HTTP"
  enable_cdn            = false
  security_policy       = "projects/${var.project_id}/global/securityPolicies/${google_compute_security_policy.edge[0].name}"

  backend {
    group = "projects/${var.project_id}/regions/${var.region}/networkEndpointGroups/${google_compute_region_network_endpoint_group.edge[each.key].name}"
  }

  log_config {
    enable      = true
    sample_rate = 1.0
  }
}

# One policy, Cloud Armor Standard (billed per policy, per rule and per request: D242 -> Cost). Rules
# inline. NORMAL logging only: VERBOSE would put the matched part of a body (health data) into the log.
resource "google_compute_security_policy" "edge" {
  count = local.edge_built ? 1 : 0

  project     = var.project_id
  name        = local.edge_name
  description = "api and ingest: WAF in preview, a rate rule per address, allow by default (infra/gcp, D242)"
  type        = "CLOUD_ARMOR"

  advanced_options_config {
    json_parsing = "STANDARD"
    log_level    = "NORMAL"
  }

  dynamic "rule" {
    for_each = local.edge_waf_preview

    content {
      description = "WAF ${rule.key}, sensitivity 1${rule.value ? " (preview)" : ""}"
      action      = "deny(403)"
      priority    = local.edge_waf_priority[rule.key]
      preview     = rule.value

      match {
        expr {
          expression = "evaluatePreconfiguredWaf('${rule.key}', {'sensitivity': 1})"
        }
      }
    }
  }

  rule {
    description = "Per address: over ${local.edge_rate.count} in ${local.edge_rate.interval_sec} s 429; over ${local.edge_rate.ban_count} in ${local.edge_rate.ban_interval_sec} s banned for ${local.edge_rate.ban_duration_sec} s"
    action      = "rate_based_ban"
    priority    = local.edge_rate.priority
    preview     = false

    match {
      versioned_expr = "SRC_IPS_V1"

      config {
        src_ip_ranges = ["*"]
      }
    }

    rate_limit_options {
      enforce_on_key   = "IP"
      conform_action   = "allow"
      exceed_action    = "deny(429)"
      ban_duration_sec = local.edge_rate.ban_duration_sec

      rate_limit_threshold {
        count        = local.edge_rate.count
        interval_sec = local.edge_rate.interval_sec
      }

      ban_threshold {
        count        = local.edge_rate.ban_count
        interval_sec = local.edge_rate.ban_interval_sec
      }
    }
  }

  # No geo blocking.
  rule {
    description = "Default: allow"
    action      = "allow"
    priority    = 2147483647
    preview     = false

    match {
      versioned_expr = "SRC_IPS_V1"

      config {
        src_ip_ranges = ["*"]
      }
    }
  }

  depends_on = [google_project_service.keep]
}

# By host: api. to api, ingest. to ingest. Any other host (the bare address, a scanner's guess) is
# redirected to the site and reaches no backend.
resource "google_compute_url_map" "edge" {
  count = local.edge_built ? 1 : 0

  project     = var.project_id
  name        = local.edge_name
  description = "api. and ingest.ryftlab.de by host; anything else 301 to https://ryftlab.de/ (infra/gcp, D242)"

  default_url_redirect {
    https_redirect         = true
    host_redirect          = local.domain
    path_redirect          = "/"
    strip_query            = true
    redirect_response_code = "MOVED_PERMANENTLY_DEFAULT"
  }

  dynamic "host_rule" {
    for_each = local.edge_hosts

    content {
      hosts        = ["${host_rule.key}.${local.domain}"]
      path_matcher = host_rule.key
    }
  }

  dynamic "path_matcher" {
    for_each = local.edge_hosts

    content {
      name            = path_matcher.key
      default_service = "projects/${var.project_id}/global/backendServices/${google_compute_backend_service.edge[path_matcher.key].name}"
    }
  }
}

# Google's default SSL policy admits TLS 1.0. MODERN with 1.2 at least.
resource "google_compute_ssl_policy" "edge" {
  count = local.edge_built ? 1 : 0

  project         = var.project_id
  name            = local.edge_name
  description     = "TLS 1.2 or newer, MODERN ciphers (infra/gcp, D242)"
  profile         = "MODERN"
  min_tls_version = "TLS_1_2"

  depends_on = [google_project_service.keep]
}

# ---- the certificates: Certificate Manager with DNS authorization ---------------------------------------
# A DNS authorization proves the domain by its own CNAME (below), not by where the host points, so both
# certificates go ACTIVE while api. and ingest. still name the mappings (runbook step 16, built's
# checks). CAA already allows pki.goog (dns.tf).

resource "google_certificate_manager_dns_authorization" "edge" {
  for_each = local.edge_hosts

  project     = var.project_id
  name        = "${local.edge_name}-${each.key}"
  location    = "global"
  description = "${each.key}.${local.domain} for the edge's certificate (infra/gcp, D242)"
  domain      = "${each.key}.${local.domain}"

  depends_on = [google_project_service.keep]
}

# The authorization's CNAME (_acme-challenge.<host>.), as Certificate Manager returns it.
resource "google_dns_record_set" "edge_dns_authorization" {
  for_each = local.edge_hosts

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = google_certificate_manager_dns_authorization.edge[each.key].dns_resource_record[0].name
  type         = google_certificate_manager_dns_authorization.edge[each.key].dns_resource_record[0].type
  ttl          = 300
  rrdatas      = [google_certificate_manager_dns_authorization.edge[each.key].dns_resource_record[0].data]
}

resource "google_certificate_manager_certificate" "edge" {
  for_each = local.edge_hosts

  project     = var.project_id
  name        = "${local.edge_name}-${each.key}"
  location    = "global"
  description = "${each.key}.${local.domain}, Google-managed, by DNS authorization (infra/gcp, D242)"

  managed {
    domains            = ["${each.key}.${local.domain}"]
    dns_authorizations = ["projects/${var.project_id}/locations/global/dnsAuthorizations/${google_certificate_manager_dns_authorization.edge[each.key].name}"]
  }

  # Issued only once the authorization's CNAME is in the zone.
  depends_on = [google_dns_record_set.edge_dns_authorization]
}

resource "google_certificate_manager_certificate_map" "edge" {
  count = local.edge_built ? 1 : 0

  project     = var.project_id
  name        = local.edge_name
  description = "The edge's certificates by hostname (infra/gcp, D242)"

  depends_on = [google_project_service.keep]
}

resource "google_certificate_manager_certificate_map_entry" "edge" {
  for_each = local.edge_hosts

  project      = var.project_id
  name         = "${local.edge_name}-${each.key}"
  description  = "${each.key}.${local.domain} (infra/gcp, D242)"
  map          = google_certificate_manager_certificate_map.edge[0].name
  hostname     = "${each.key}.${local.domain}"
  certificates = ["projects/${var.project_id}/locations/global/certificates/${google_certificate_manager_certificate.edge[each.key].name}"]
}

# ---- the front: HTTPS on 443, and 80 redirected to it, on the one address ---------------------------------

resource "google_compute_target_https_proxy" "edge" {
  count = local.edge_built ? 1 : 0

  project         = var.project_id
  name            = local.edge_name
  description     = "The edge's HTTPS proxy (infra/gcp, D242)"
  url_map         = "projects/${var.project_id}/global/urlMaps/${google_compute_url_map.edge[0].name}"
  ssl_policy      = "projects/${var.project_id}/global/sslPolicies/${google_compute_ssl_policy.edge[0].name}"
  certificate_map = "//certificatemanager.googleapis.com/projects/${var.project_id}/locations/global/certificateMaps/${google_certificate_manager_certificate_map.edge[0].name}"

  # The map serves nothing before its entries exist.
  depends_on = [google_certificate_manager_certificate_map_entry.edge]
}

resource "google_compute_global_forwarding_rule" "edge_https" {
  count = local.edge_built ? 1 : 0

  project               = var.project_id
  name                  = "${local.edge_name}-https"
  description           = "The edge on 443 (infra/gcp, D242)"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  ip_protocol           = "TCP"
  port_range            = "443"
  ip_address            = "projects/${var.project_id}/global/addresses/${google_compute_global_address.edge[0].name}"
  target                = "projects/${var.project_id}/global/targetHttpsProxies/${google_compute_target_https_proxy.edge[0].name}"
}

# http:// to https://, same host, path and query, 301. No backend is reached on 80.
resource "google_compute_url_map" "edge_http" {
  count = local.edge_built ? 1 : 0

  project     = var.project_id
  name        = "${local.edge_name}-http"
  description = "http:// -> https://, 301 (infra/gcp, D242)"

  default_url_redirect {
    https_redirect         = true
    strip_query            = false
    redirect_response_code = "MOVED_PERMANENTLY_DEFAULT"
  }

  depends_on = [google_project_service.keep]
}

resource "google_compute_target_http_proxy" "edge" {
  count = local.edge_built ? 1 : 0

  project     = var.project_id
  name        = "${local.edge_name}-http"
  description = "The edge's redirect to HTTPS (infra/gcp, D242)"
  url_map     = "projects/${var.project_id}/global/urlMaps/${google_compute_url_map.edge_http[0].name}"
}

resource "google_compute_global_forwarding_rule" "edge_http" {
  count = local.edge_built ? 1 : 0

  project               = var.project_id
  name                  = "${local.edge_name}-http"
  description           = "The edge on 80, redirected to 443 (infra/gcp, D242)"
  load_balancing_scheme = "EXTERNAL_MANAGED"
  ip_protocol           = "TCP"
  port_range            = "80"
  ip_address            = "projects/${var.project_id}/global/addresses/${google_compute_global_address.edge[0].name}"
  target                = "projects/${var.project_id}/global/targetHttpProxies/${google_compute_target_http_proxy.edge[0].name}"
}
