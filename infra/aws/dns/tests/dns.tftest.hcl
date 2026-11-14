# Plan-only (the zone has prevent_destroy); the AWS provider is mocked. The file sets no variables: the
# defaults are the production values (D213 the domain, D214 the Microsoft 365 records).
mock_provider "aws" {}

run "zone_and_caa_for_the_domain" {
  command = plan

  assert {
    condition     = aws_route53_zone.ryftlab_de.name == "ryftlab.de"
    error_message = "the zone must be for ryftlab.de"
  }

  assert {
    condition     = aws_route53_record.caa.name == "ryftlab.de" && aws_route53_record.caa.records == toset(["0 issue \"letsencrypt.org\""])
    error_message = "only Let's Encrypt may issue for the apex"
  }

  assert {
    condition     = aws_route53_record.caa_wildcard.name == "*.ryftlab.de" && aws_route53_record.caa_wildcard.records == toset(["0 issuewild \"letsencrypt.org\""])
    error_message = "wildcard issuance must be limited to Let's Encrypt"
  }
}

# D213: the previous zone left the configuration, so the plan destroys it. One zone is configured, and it
# is not at the old address (a zone still at aws_route53_zone.this would be replaced in place, which
# prevent_destroy refuses).
run "the_old_zone_is_gone" {
  command = plan

  assert {
    condition = (
      length(regexall("resource \"aws_route53_zone\"", join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]))) == 1 &&
      !strcontains(join("\n", [for f in fileset(path.module, "*.tf") : file("${path.module}/${f}")]), "resource \"aws_route53_zone\" \"this\"")
    )
    error_message = "exactly one zone, aws_route53_zone.ryftlab_de; the old zone's block must stay removed"
  }

  assert {
    condition     = !strcontains(file("${path.module}/main.tf"), "moved {")
    error_message = "no moved block: the old records (dkim[0] among them) are destroyed with their zone, not moved"
  }

  assert {
    condition = alltrue([
      for n in concat(
        [aws_route53_record.caa.name, aws_route53_record.caa_wildcard.name, aws_route53_record.mx.name, aws_route53_record.apex_txt.name, aws_route53_record.dmarc.name],
        [for r in aws_route53_record.mail_cname : r.name],
        [for r in aws_route53_record.mail_srv : r.name],
      ) : n == "ryftlab.de" || endswith(n, ".ryftlab.de")
    ])
    error_message = "every record is in ryftlab.de"
  }
}

# D214: Microsoft 365 through GoDaddy, exactly the values GoDaddy shows.
run "microsoft_365_mail" {
  command = plan

  assert {
    condition     = aws_route53_record.mx.records == toset(["0 ryftlab-de.mail.protection.outlook.com."])
    error_message = "MX points at the tenant's Exchange Online endpoint"
  }

  assert {
    condition     = aws_route53_record.apex_txt.records == toset(["NETORGFT21311797.onmicrosoft.com", "v=spf1 include:secureserver.net -all"])
    error_message = "the apex TXT set holds the tenant verification and the SPF as two values, without quotes"
  }

  assert {
    condition     = aws_route53_record.dmarc.name == "_dmarc.ryftlab.de" && aws_route53_record.dmarc.records == toset(["v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;"])
    error_message = "DMARC quarantines with relaxed alignment and reports to GoDaddy's address"
  }

  assert {
    condition = (
      length(aws_route53_record.mail_cname) == 6 &&
      alltrue([for r in aws_route53_record.mail_cname : r.type == "CNAME" && r.ttl == 3600]) &&
      aws_route53_record.mail_cname["autodiscover"].records == toset(["autodiscover.outlook.com."]) &&
      aws_route53_record.mail_cname["selector2._domainkey"].name == "selector2._domainkey.ryftlab.de" &&
      aws_route53_record.mail_cname["selector2._domainkey"].records == toset(["selector2-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft."]) &&
      aws_route53_record.mail_cname["email"].records == toset(["email.secureserver.net."]) &&
      aws_route53_record.mail_cname["lyncdiscover"].records == toset(["webdir.online.lync.com."]) &&
      aws_route53_record.mail_cname["sip"].records == toset(["sipdir.online.lync.com."]) &&
      aws_route53_record.mail_cname["msoid"].records == toset(["clientconfig.microsoftonline-p.net."])
    )
    error_message = "the six CNAMEs as GoDaddy shows them"
  }

  assert {
    condition     = !contains(keys(aws_route53_record.mail_cname), "selector1._domainkey")
    error_message = "selector1 is not published until it resolves (one line in mail.tf adds it)"
  }

  assert {
    condition = (
      length(aws_route53_record.mail_srv) == 2 &&
      alltrue([for r in aws_route53_record.mail_srv : r.type == "SRV"]) &&
      aws_route53_record.mail_srv["_sip._tls"].name == "_sip._tls.ryftlab.de" &&
      aws_route53_record.mail_srv["_sip._tls"].records == toset(["100 1 443 sipdir.online.lync.com."]) &&
      aws_route53_record.mail_srv["_sipfederationtls._tcp"].records == toset(["100 1 5061 sipfed.online.lync.com."])
    )
    error_message = "the two SRV records as GoDaddy shows them"
  }
}

run "rejects_no_caa_issuer" {
  command = plan

  variables {
    caa_issuers = []
  }

  expect_failures = [var.caa_issuers]
}
