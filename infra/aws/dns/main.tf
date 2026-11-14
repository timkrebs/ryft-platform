locals {
  # The domain (D213). A constant, not a variable: the mail records in mail.tf are this domain's.
  domain = "ryftlab.de"

  tags = merge(var.tags, {
    Project   = "ryft"
    ManagedBy = "terraform"
  })
}

# The public zone for the whole domain. GoDaddy stays the registrar; its nameservers are
# switched once to the four Route 53 assigns here (see outputs).
# Long-lived on purpose: recreating it would assign new nameservers and need another change at
# GoDaddy, so it lives outside the cluster's root module.
#
# The previous zone (aws_route53_zone.this, the old domain) left this configuration with D213: the
# next plan destroys it and its records. prevent_destroy guards only a resource that is still
# configured, so removing the block is what lets that happen.
resource "aws_route53_zone" "ryftlab_de" {
  name    = local.domain
  comment = "RYFT public zone, managed by infra/aws/dns"

  tags = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

# Only Let's Encrypt (cert-manager in the cluster) may issue certificates for the domain and its
# subdomains.
resource "aws_route53_record" "caa" {
  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = local.domain
  type    = "CAA"
  ttl     = 3600
  records = [for ca in var.caa_issuers : "0 issue \"${ca}\""]
}

resource "aws_route53_record" "caa_wildcard" {
  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = "*.${local.domain}"
  type    = "CAA"
  ttl     = 3600
  records = [for ca in var.caa_issuers : "0 issuewild \"${ca}\""]
}
