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

# Mail for the domain: Google Workspace (D142). The records were first created by hand
# in the console and are imported (docs/OPERATIONS.md, "Mail for ryftapp.de").
resource "aws_route53_record" "mx" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "MX"
  ttl     = 3600
  records = var.mx_records
}

# One record set, two values (verification and SPF). The hand-made record had both
# inside one string; a TXT value is one string, so they are separate values here.
resource "aws_route53_record" "apex_txt" {
  zone_id = aws_route53_zone.this.zone_id
  name    = var.domain
  type    = "TXT"
  ttl     = 3600
  records = [var.google_site_verification, var.spf]
}

# Route 53 takes strings of at most 255 characters; a longer value is several strings in
# one value, which resolvers join again. The AWS provider wraps the value in quotes itself,
# so only the joints carry quotes: chunk1" "chunk2, no outer quotes (the state holds it so).
resource "aws_route53_record" "dkim" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "${var.dkim_selector}._domainkey.${var.domain}"
  type    = "TXT"
  ttl     = 3600
  records = [join("\" \"", regexall(".{1,255}", var.dkim_record))]
}

resource "aws_route53_record" "dmarc" {
  zone_id = aws_route53_zone.this.zone_id
  name    = "_dmarc.${var.domain}"
  type    = "TXT"
  ttl     = 3600
  records = [var.dmarc]
}
