# The public zone ryftlab.de on Cloud DNS. It replaced the Route 53 zone (D214; deleted 2026-11-19,
# D246): the same records with the same values and TTLs, plus CAA's pki.goog and Search Console's
# verification string. GoDaddy stays the registrar; its name servers switched once to the four below
# (runbook step 6). DNSSEC stays off: there is no DS at the parent, and none may appear.
#
# Long-lived on purpose: a new zone gets new name servers and needs another change at GoDaddy.
#
# C1 (the site on Firebase Hosting) adds its own records in site.tf, from the variables the Hosting
# wizard fills (site.auto.tfvars): the apex A records, and www's A and TXT or its CNAME, as record sets
# of their own; its apex `hosting-site=` string joins local.apex_txt below.
resource "google_dns_managed_zone" "ryftlab_de" {
  project     = var.project_id
  name        = "ryftlab-de"
  dns_name    = "${local.domain}."
  description = "RYFT public zone, managed by infra/gcp"
  visibility  = "public"

  dnssec_config {
    state = "off"
  }

  lifecycle {
    prevent_destroy = true
  }
}

# Mail for the domain (D214): Microsoft 365 through GoDaddy, exactly the values the Route 53 zone held.
# Public values, so code, not variables. A change at the provider is a change here.
locals {
  mail_mx = ["0 ryftlab-de.mail.protection.outlook.com."]

  # The apex TXT set: one string per entry, each quoted for Cloud DNS (it splits an unquoted value at
  # spaces, which would break the SPF). The tenant's verification and the SPF, then Search Console's.
  m365_apex_txt = [
    "NETORGFT21311797.onmicrosoft.com",
    "v=spf1 include:secureserver.net -all",
  ]
  apex_txt = concat(
    local.m365_apex_txt,
    [var.google_site_verification],
    # C1: Firebase Hosting's `hosting-site=` value for the apex (site.tf), once the wizard showed it.
    var.site_apex_txt == "" ? [] : [var.site_apex_txt],
  )

  dmarc = "v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;"

  # Label below the domain => target.
  mail_cnames = {
    "autodiscover" = "autodiscover.outlook.com."
    # selector1 did not resolve on 2026-11-14 (nor in Route 53's zone). Once it does, one line here.
    "selector2._domainkey" = "selector2-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft."
    "email"                = "email.secureserver.net."
    "lyncdiscover"         = "webdir.online.lync.com."
    "sip"                  = "sipdir.online.lync.com."
    "msoid"                = "clientconfig.microsoftonline-p.net."
  }

  # Label below the domain => "priority weight port target".
  mail_srv = {
    "_sip._tls"              = "100 1 443 sipdir.online.lync.com."
    "_sipfederationtls._tcp" = "100 1 5061 sipfed.online.lync.com."
  }

  # Both certificate users of the domain ask for both CAs (design 8): Cloud Run's mappings (api.,
  # ingest.) and Firebase Hosting (the apex, www., C1). One set at the apex, and no `*.` set (RFC 8659):
  # a CA climbs from a name to the apex, and a `*.` set would answer for every name that does not
  # exist. issuewild stays Let's Encrypt only: neither service asks for a wildcard.
  caa = [
    "0 issue \"letsencrypt.org\"",
    "0 issue \"pki.goog\"",
    "0 issuewild \"letsencrypt.org\"",
  ]
}

resource "google_dns_record_set" "caa" {
  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = google_dns_managed_zone.ryftlab_de.dns_name
  type         = "CAA"
  ttl          = 3600
  rrdatas      = local.caa
}

resource "google_dns_record_set" "mx" {
  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = google_dns_managed_zone.ryftlab_de.dns_name
  type         = "MX"
  ttl          = 3600
  rrdatas      = local.mail_mx
}

resource "google_dns_record_set" "apex_txt" {
  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = google_dns_managed_zone.ryftlab_de.dns_name
  type         = "TXT"
  ttl          = 3600
  rrdatas      = [for v in local.apex_txt : "\"${v}\""]
}

resource "google_dns_record_set" "dmarc" {
  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "_dmarc.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "TXT"
  ttl          = 3600
  rrdatas      = ["\"${local.dmarc}\""]
}

resource "google_dns_record_set" "mail_cname" {
  for_each = local.mail_cnames

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "${each.key}.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "CNAME"
  ttl          = 3600
  rrdatas      = [each.value]
}

resource "google_dns_record_set" "mail_srv" {
  for_each = local.mail_srv

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "${each.key}.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "SRV"
  ttl          = 3600
  rrdatas      = [each.value]
}

# Apply 2: api. and ingest. point where their mapping says. For a subdomain Cloud Run returns one CNAME
# (ghs.googlehosted.com.); the records are read from the mapping's status, not typed in.
#
# The edge (D242): from edge = live, each is an A record at the load balancer's IPv4 address instead
# (no AAAA: the edge has no IPv6 address). Cloud DNS can't hold a CNAME and an A at one name, and two
# resources could create the A before the CNAME is gone, so it is one record set per host whose type
# and data follow the gate. The provider writes a type change as one Cloud DNS change that deletes the
# old set and adds the new one; a replacement (-/+) would delete first, too. From locked the mappings
# are gone, and the A records read nothing of them.
locals {
  # Host => the CNAME data its mapping returned (no entry once the mapping is gone, edge = locked).
  mapping_cnames = {
    for k, m in google_cloud_run_domain_mapping.host : k => [
      for rr in flatten(m.status[*].resource_records) : "${trimsuffix(rr.rrdata, ".")}." if rr.type == "CNAME"
    ]
  }
}

resource "google_dns_record_set" "host" {
  for_each = var.serve ? local.mapped_hosts : {}

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "${each.key}.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = local.edge_live ? "A" : "CNAME"
  ttl          = 300
  rrdatas      = local.edge_live ? google_compute_global_address.edge[*].address : lookup(local.mapping_cnames, each.key, [])

  lifecycle {
    precondition {
      condition     = local.edge_live || length(lookup(local.mapping_cnames, each.key, [])) == 1
      error_message = "The domain mapping did not return exactly one CNAME for this host; read its status (gcloud beta run domain-mappings describe) before anything else."
    }
  }
}

# The record sets were google_dns_record_set.mapping before D242: the same two, so edge = off plans no
# change.
moved {
  from = google_dns_record_set.mapping
  to   = google_dns_record_set.host
}
