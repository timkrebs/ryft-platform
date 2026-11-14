# Plan-only (the zone has prevent_destroy); the AWS provider is mocked. The file sets no variables: the defaults
# are the production values (D212, no mail).
mock_provider "aws" {}

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

# D212: no mail provider yet. The defaults say the domain neither receives nor sends mail.
run "no_mail_by_default" {
  command = plan

  assert {
    condition     = aws_route53_record.mx.name == "ryftapp.de" && aws_route53_record.mx.records == toset(["0 ."])
    error_message = "the default MX is the RFC 7505 null MX (0 .): the domain accepts no mail"
  }

  assert {
    condition     = aws_route53_record.apex_txt.name == "ryftapp.de" && aws_route53_record.apex_txt.records == toset(["v=spf1 -all"])
    error_message = "the default apex TXT is SPF -all alone: no host may send as the domain, and no provider verification"
  }

  assert {
    condition     = aws_route53_record.dmarc.name == "_dmarc.ryftapp.de" && aws_route53_record.dmarc.records == toset(["v=DMARC1; p=reject; adkim=s; aspf=s"])
    error_message = "the default DMARC rejects with strict alignment and has no rua (there is no mailbox for reports)"
  }

  assert {
    condition     = length(aws_route53_record.dkim) == 0
    error_message = "no DKIM record without a provider's key"
  }

  assert {
    condition = !anytrue([
      for v in concat(tolist(aws_route53_record.mx.records), tolist(aws_route53_record.apex_txt.records), tolist(aws_route53_record.dmarc.records)) :
      strcontains(v, "google") || strcontains(v, "mailto:")
    ])
    error_message = "the no-mail set names no provider and no report address"
  }
}

# The Google Workspace set of terraform.tfvars.example (D142) is one tfvars block away and renders the records as
# they were before D212. A 2048-bit key is ~400 characters: longer than one Route 53 string (255).
run "google_workspace_is_one_tfvars_block_away" {
  command = plan

  variables {
    mx_records             = ["1 smtp.google.com"]
    apex_txt_verifications = ["google-site-verification=eUj_Gi_u5TMp0qhySZvarTISldu_UzkiJkRoIJBmfR8"]
    spf                    = "v=spf1 include:_spf.google.com ~all"
    dkim_selector          = "google"
    dkim_record            = "v=DKIM1; k=rsa; p=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
    dmarc                  = "v=DMARC1; p=none; rua=mailto:support@ryftapp.de; adkim=s; aspf=s"
  }

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
    condition     = length(aws_route53_record.dkim) == 1 && aws_route53_record.dkim[0].name == "google._domainkey.ryftapp.de"
    error_message = "DKIM sits at the selector under _domainkey"
  }

  assert {
    condition = (
      length(var.dkim_record) > 255 &&
      !startswith(one(aws_route53_record.dkim[0].records), "\"") &&
      !endswith(one(aws_route53_record.dkim[0].records), "\"") &&
      length(split("\" \"", one(aws_route53_record.dkim[0].records))) >= 2 &&
      alltrue([for s in split("\" \"", one(aws_route53_record.dkim[0].records)) : length(s) <= 255 && !strcontains(s, "\"")]) &&
      join("", split("\" \"", one(aws_route53_record.dkim[0].records))) == var.dkim_record
    )
    error_message = "the DKIM value must be chunk1\" \"chunk2 with no outer quotes (the provider adds them), chunks of at most 255 characters that join back to the key"
  }

  assert {
    condition     = aws_route53_record.dmarc.name == "_dmarc.ryftapp.de" && aws_route53_record.dmarc.records == toset(["v=DMARC1; p=none; rua=mailto:support@ryftapp.de; adkim=s; aspf=s"])
    error_message = "DMARC starts at p=none with strict alignment and reports to support@"
  }

  # The example file carries the same set, commented out.
  assert {
    condition = alltrue([for l in [
      "# mx_records             = [\"1 smtp.google.com\"]",
      "# apex_txt_verifications = [\"google-site-verification=eUj_Gi_u5TMp0qhySZvarTISldu_UzkiJkRoIJBmfR8\"]",
      "# spf                    = \"v=spf1 include:_spf.google.com ~all\"",
      "# dkim_selector          = \"google\"",
      "# dmarc                  = \"v=DMARC1; p=none; rua=mailto:support@ryftapp.de; adkim=s; aspf=s\"",
    ] : strcontains(file("${path.module}/terraform.tfvars.example"), l)])
    error_message = "terraform.tfvars.example must keep the Google Workspace set this run plans"
  }
}

run "rejects_a_dkim_value_that_is_not_dkim" {
  command = plan

  variables {
    dkim_selector = "google"
    dkim_record   = "v=spf1 -all"
  }

  expect_failures = [var.dkim_record]
}

run "rejects_a_dkim_record_without_a_selector" {
  command = plan

  variables {
    dkim_record = "v=DKIM1; k=rsa; p=AAAA"
  }

  expect_failures = [var.dkim_record]
}

run "rejects_a_null_mx_next_to_another_mx" {
  command = plan

  variables {
    mx_records = ["0 .", "1 smtp.google.com"]
  }

  expect_failures = [var.mx_records]
}

run "rejects_an_spf_among_the_verifications" {
  command = plan

  variables {
    apex_txt_verifications = ["v=spf1 include:_spf.google.com ~all"]
  }

  expect_failures = [var.apex_txt_verifications]
}

run "rejects_invalid_domain" {
  command = plan

  variables {
    domain = "RyftApp.DE"
  }

  expect_failures = [var.domain]
}
