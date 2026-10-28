# Plan-only (the zone has prevent_destroy); the AWS provider is mocked.
mock_provider "aws" {}

# A 2048-bit key is ~400 characters: longer than one Route 53 string (255).
variables {
  dkim_record = "v=DKIM1; k=rsa; p=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
}

run "zone_and_caa_for_the_domain" {
  command = plan

  assert {
    condition     = aws_route53_zone.this.name == "ryftapp.de"
    error_message = "the zone must be for ryftapp.de"
  }

  assert {
    condition     = aws_route53_record.caa.records == toset(["0 issue \"letsencrypt.org\""])
    error_message = "only Let's Encrypt may issue for the apex"
  }

  assert {
    condition     = aws_route53_record.caa_wildcard.name == "*.ryftapp.de" && aws_route53_record.caa_wildcard.records == toset(["0 issuewild \"letsencrypt.org\""])
    error_message = "wildcard issuance must be limited to Let's Encrypt"
  }
}

run "google_workspace_mail_records" {
  command = plan

  assert {
    condition     = aws_route53_record.mx.name == "ryftapp.de" && aws_route53_record.mx.records == toset(["1 smtp.google.com"])
    error_message = "MX must point at Google Workspace"
  }

  assert {
    condition = aws_route53_record.apex_txt.records == toset([
      "google-site-verification=eUj_Gi_u5TMp0qhySZvarTISldu_UzkiJkRoIJBmfR8",
      "v=spf1 include:_spf.google.com ~all",
    ])
    error_message = "the apex TXT must hold verification and SPF as two separate values, without quotes"
  }

  assert {
    condition     = aws_route53_record.dkim.name == "google._domainkey.ryftapp.de"
    error_message = "DKIM sits at the selector under _domainkey"
  }

  assert {
    condition = (
      !startswith(one(aws_route53_record.dkim.records), "\"") &&
      !endswith(one(aws_route53_record.dkim.records), "\"") &&
      length(split("\" \"", one(aws_route53_record.dkim.records))) >= 2 &&
      alltrue([for s in split("\" \"", one(aws_route53_record.dkim.records)) : length(s) <= 255 && !strcontains(s, "\"")]) &&
      join("", split("\" \"", one(aws_route53_record.dkim.records))) == var.dkim_record
    )
    error_message = "the DKIM value must be chunk1\" \"chunk2 with no outer quotes (the provider adds them), chunks of at most 255 characters that join back to the key"
  }

  assert {
    condition     = aws_route53_record.dmarc.name == "_dmarc.ryftapp.de" && aws_route53_record.dmarc.records == toset(["v=DMARC1; p=none; rua=mailto:support@ryftapp.de; adkim=s; aspf=s"])
    error_message = "DMARC starts at p=none with strict alignment and reports to support@"
  }
}

run "rejects_a_dkim_value_that_is_not_dkim" {
  command = plan

  variables {
    dkim_record = "v=spf1 -all"
  }

  expect_failures = [var.dkim_record]
}

run "rejects_invalid_domain" {
  command = plan

  variables {
    domain = "RyftApp.DE"
  }

  expect_failures = [var.domain]
}
