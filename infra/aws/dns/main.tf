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
