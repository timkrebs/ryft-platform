# Plan-only (the zone has prevent_destroy); the AWS provider is mocked.
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

run "rejects_invalid_domain" {
  command = plan

  variables {
    domain = "RyftApp.DE"
  }

  expect_failures = [var.domain]
}
