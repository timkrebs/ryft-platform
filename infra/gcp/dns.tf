# The public zone ryftlab.de on Cloud DNS. It replaces infra/aws/dns's Route 53 zone (D214): the same
# records with the same values and TTLs, plus CAA's pki.goog and Search Console's verification string.
# GoDaddy stays the registrar; its name servers switch once to the four below (runbook step 6, after
# scripts/dns_compare.sh). DNSSEC stays off: there is no DS at the parent, and none may appear.
#
# Long-lived on purpose: a new zone gets new name servers and needs another change at GoDaddy.
#
# C1 (the site on Firebase Hosting) adds its own records from variables the Hosting wizard fills: the
# apex A records, and the A and TXT records of www, as record sets of their own; its apex
# `hosting-site=` string joins local.apex_txt below. Nothing of C1 is built here.
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

# Mail for the domain (D214): Microsoft 365 through GoDaddy, the values of infra/aws/dns/mail.tf exactly.
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
    # C1: the site's apex `hosting-site=` value (a variable of C1's) goes here, one line.
  )

  dmarc = "v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;"

  # Label below the domain => target.
  mail_cnames = {
    "autodiscover" = "autodiscover.outlook.com."
    # selector1 did not resolve on 2026-11-14 (infra/aws/dns/mail.tf). Once it does, one line here.
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
resource "google_dns_record_set" "mapping" {
  for_each = var.serve ? local.mapped_hosts : {}

  project      = var.project_id
  managed_zone = google_dns_managed_zone.ryftlab_de.name
  name         = "${each.key}.${google_dns_managed_zone.ryftlab_de.dns_name}"
  type         = "CNAME"
  ttl          = 300
  rrdatas = [
    for rr in flatten(google_cloud_run_domain_mapping.host[each.key].status[*].resource_records) :
    "${trimsuffix(rr.rrdata, ".")}." if rr.type == "CNAME"
  ]

  lifecycle {
    precondition {
      condition = length([
        for rr in flatten(google_cloud_run_domain_mapping.host[each.key].status[*].resource_records) : rr if rr.type == "CNAME"
      ]) == 1
      error_message = "The domain mapping did not return exactly one CNAME for this host; read its status (gcloud beta run domain-mappings describe) before anything else."
    }
  }
}
