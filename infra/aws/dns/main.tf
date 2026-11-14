locals {
  tags = merge(var.tags, {
    Project   = "ryft"
    ManagedBy = "terraform"
  })
}

# The public zone for the whole domain. GoDaddy stays the registrar; its
# nameservers are switched once to the four Route 53 assigns here (see outputs).
# Long-lived on purpose: recreating it would assign new nameservers and need
# another change at GoDaddy, so it lives outside the cluster's root module.
resource "aws_route53_zone" "this" {
  name    = var.domain
  comment = "RYFT public zone, managed by infra/aws/dns"

  tags = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

# Only Let's Encrypt (cert-manager in the cluster) may issue certificates for
# the domain and its subdomains.
resource "aws_route53_record" "caa" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "CAA"
  ttl     = 3600
  records = [for ca in var.caa_issuers : "0 issue \"${ca}\""]
}

resource "aws_route53_record" "caa_wildcard" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "*.${var.domain}"
  type    = "CAA"
  ttl     = 3600
  records = [for ca in var.caa_issuers : "0 issuewild \"${ca}\""]
}

# Mail for the domain (D212): by default none at all (variables.tf: null MX, SPF -all, DMARC
# reject, no DKIM), until a provider is chosen; Google Workspace's values (D142, deleted) are the
# example in terraform.tfvars.example. The records were first created by hand and imported
# (docs/OPERATIONS.md, "Mail for ryftapp.de").
resource "aws_route53_record" "mx" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "MX"
  ttl     = 3600
  records = var.mx_records
}

# One record set: a provider's verification values (if any) and the SPF, each its own value. The
# hand-made record had two inside one string; a TXT value is one string, so they are separate.
resource "aws_route53_record" "apex_txt" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "TXT"
  ttl     = 3600
  records = concat(var.apex_txt_verifications, [var.spf])
}

# Only with a provider's key (dkim_record). Route 53 takes strings of at most 255 characters; a
# longer value is several strings in one value, which resolvers join again. The AWS provider wraps
# the value in quotes itself, so only the joints carry quotes: chunk1" "chunk2, no outer quotes
# (the state holds it so).
resource "aws_route53_record" "dkim" {
  count = var.dkim_record == null ? 0 : 1

  zone_id = aws_route53_zone.this.zone_id
  name    = "${var.dkim_selector}._domainkey.${var.domain}"
  type    = "TXT"
  ttl     = 3600
  records = [join("\" \"", regexall(".{1,255}", var.dkim_record))]
}

# The record without count is instance [0] now. Under the no-mail default the next plan shows it
# moved, then destroyed (count 0); with dkim_record set it stays where it is.
moved {
  from = aws_route53_record.dkim
  to   = aws_route53_record.dkim[0]
}

resource "aws_route53_record" "dmarc" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "_dmarc.${var.domain}"
  type    = "TXT"
  ttl     = 3600
  records = [var.dmarc]
}
