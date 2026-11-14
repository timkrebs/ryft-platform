# The Cloud DNS zone carries every record of infra/aws/dns (D214), with the same values and TTLs, plus
# exactly CAA's pki.goog and Search Console's string. Plan-only, mocked. rrdatas is a list, so the
# literals are tolist([...]): a list never equals a tuple.
mock_provider "google" {
  mock_data "google_logging_sink" {
    defaults = {
      disabled = true
    }
  }
}
mock_provider "google-beta" {}
mock_provider "grafana" {}

override_data {
  target = data.google_project.this
  values = {
    number = "123456789012"
  }
}

variables {
  github_repository_id       = "100000001"
  github_repository_owner_id = "200000002"
  google_site_verification   = "google-site-verification=example-only"
}

run "zone_for_the_domain" {
  command = plan

  assert {
    condition = (
      google_dns_managed_zone.ryftlab_de.dns_name == "ryftlab.de." &&
      google_dns_managed_zone.ryftlab_de.visibility == "public" &&
      google_dns_managed_zone.ryftlab_de.dnssec_config[0].state == "off"
    )
    error_message = "the public zone ryftlab.de, DNSSEC off (no DS may appear at the parent)"
  }

  assert {
    condition = alltrue([
      for n in concat(
        [google_dns_record_set.caa.name, google_dns_record_set.mx.name, google_dns_record_set.apex_txt.name, google_dns_record_set.dmarc.name],
        [for r in google_dns_record_set.mail_cname : r.name],
        [for r in google_dns_record_set.mail_srv : r.name],
      ) : n == "ryftlab.de." || endswith(n, ".ryftlab.de.")
    ])
    error_message = "every record is in ryftlab.de, fully qualified"
  }

  assert {
    condition = alltrue(concat(
      [google_dns_record_set.caa.ttl == 3600, google_dns_record_set.mx.ttl == 3600, google_dns_record_set.apex_txt.ttl == 3600, google_dns_record_set.dmarc.ttl == 3600],
      [for r in google_dns_record_set.mail_cname : r.ttl == 3600],
      [for r in google_dns_record_set.mail_srv : r.ttl == 3600],
    ))
    error_message = "the TTLs of the Route 53 records (3600)"
  }
}

run "caa_set_at_the_apex" {
  command = plan

  assert {
    condition = (
      google_dns_record_set.caa.name == "ryftlab.de." &&
      google_dns_record_set.caa.type == "CAA" &&
      toset(google_dns_record_set.caa.rrdatas) == toset(["0 issue \"letsencrypt.org\"", "0 issue \"pki.goog\"", "0 issuewild \"letsencrypt.org\""]) &&
      length(google_dns_record_set.caa.rrdatas) == 3
    )
    error_message = "CAA: issue letsencrypt.org and pki.goog, issuewild letsencrypt.org, one set at the apex"
  }

  # RFC 8659: a `*.` CAA set would answer for every name that does not exist.
  assert {
    condition     = length(regexall("name\\s*=\\s*\"\\*\\.", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 0
    error_message = "no wildcard record set: the apex CAA set covers every name"
  }

  assert {
    condition     = length(regexall("type\\s*=\\s*\"CAA\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1
    error_message = "exactly one CAA record set in the root"
  }
}

run "microsoft_365_mail" {
  command = plan

  assert {
    condition     = google_dns_record_set.mx.rrdatas == tolist(["0 ryftlab-de.mail.protection.outlook.com."])
    error_message = "MX points at the tenant's Exchange Online endpoint"
  }

  assert {
    condition = (
      toset(google_dns_record_set.apex_txt.rrdatas) == toset([
        "\"NETORGFT21311797.onmicrosoft.com\"",
        "\"v=spf1 include:secureserver.net -all\"",
        "\"google-site-verification=example-only\"",
      ]) &&
      length(google_dns_record_set.apex_txt.rrdatas) == 3
    )
    error_message = "the apex TXT set: the tenant's verification, the SPF (one quoted string each) and Search Console's value"
  }

  assert {
    condition = (
      google_dns_record_set.dmarc.name == "_dmarc.ryftlab.de." &&
      google_dns_record_set.dmarc.type == "TXT" &&
      google_dns_record_set.dmarc.rrdatas == tolist(["\"v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;\""])
    )
    error_message = "DMARC quarantines with relaxed alignment and reports to GoDaddy's address"
  }

  assert {
    condition = (
      length(google_dns_record_set.mail_cname) == 6 &&
      alltrue([for r in google_dns_record_set.mail_cname : r.type == "CNAME"]) &&
      google_dns_record_set.mail_cname["autodiscover"].rrdatas == tolist(["autodiscover.outlook.com."]) &&
      google_dns_record_set.mail_cname["selector2._domainkey"].name == "selector2._domainkey.ryftlab.de." &&
      google_dns_record_set.mail_cname["selector2._domainkey"].rrdatas == tolist(["selector2-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft."]) &&
      google_dns_record_set.mail_cname["email"].rrdatas == tolist(["email.secureserver.net."]) &&
      google_dns_record_set.mail_cname["lyncdiscover"].rrdatas == tolist(["webdir.online.lync.com."]) &&
      google_dns_record_set.mail_cname["sip"].rrdatas == tolist(["sipdir.online.lync.com."]) &&
      google_dns_record_set.mail_cname["msoid"].rrdatas == tolist(["clientconfig.microsoftonline-p.net."])
    )
    error_message = "the six CNAMEs as GoDaddy shows them"
  }

  assert {
    condition     = !contains(keys(google_dns_record_set.mail_cname), "selector1._domainkey")
    error_message = "selector1 is not published until it resolves (as in infra/aws/dns)"
  }

  assert {
    condition = (
      length(google_dns_record_set.mail_srv) == 2 &&
      alltrue([for r in google_dns_record_set.mail_srv : r.type == "SRV"]) &&
      google_dns_record_set.mail_srv["_sip._tls"].name == "_sip._tls.ryftlab.de." &&
      google_dns_record_set.mail_srv["_sip._tls"].rrdatas == tolist(["100 1 443 sipdir.online.lync.com."]) &&
      google_dns_record_set.mail_srv["_sipfederationtls._tcp"].rrdatas == tolist(["100 1 5061 sipfed.online.lync.com."])
    )
    error_message = "the two SRV records as GoDaddy shows them"
  }
}

# While infra/aws/dns exists (until D3), every value and label here is also in its mail.tf, so the two
# zones cannot drift apart by a typo on one side. A conditional, not ||: only the chosen branch's errors
# count, so the run still passes once D3 has removed infra/aws.
run "the_same_values_as_route_53" {
  command = plan

  assert {
    condition = fileexists("${path.module}/../aws/dns/mail.tf") ? alltrue([
      for v in concat(
        google_dns_record_set.mx.rrdatas,
        [for r in google_dns_record_set.apex_txt.rrdatas : trim(r, "\"") if !startswith(r, "\"google-site-verification=")],
        [for r in google_dns_record_set.dmarc.rrdatas : trim(r, "\"")],
        flatten([for r in google_dns_record_set.mail_cname : r.rrdatas]),
        flatten([for r in google_dns_record_set.mail_srv : r.rrdatas]),
        keys(google_dns_record_set.mail_cname),
        keys(google_dns_record_set.mail_srv),
      ) : strcontains(file("${path.module}/../aws/dns/mail.tf"), "\"${v}\"")
    ]) : true
    error_message = "a record value or label here is not in infra/aws/dns/mail.tf"
  }
}

run "rejects_a_verification_value_without_its_prefix" {
  command = plan

  variables {
    google_site_verification = "abc123"
  }

  expect_failures = [var.google_site_verification]
}
