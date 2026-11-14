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
# subdomains, wildcards included. One set at the apex, no `*.` CAA set (RFC 8659): a CA climbs from
# a name to the apex, and checks a wildcard certificate at its base name, i.e. here. A `*.` set
# would answer for every name that does not exist and, holding only issuewild (which a CA ignores
# for a normal certificate), would leave those names unrestricted.
resource "aws_route53_record" "caa" {
  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = local.domain
  type    = "CAA"
  ttl     = 3600
  records = concat(
    [for ca in var.caa_issuers : "0 issue \"${ca}\""],
    [for ca in var.caa_issuers : "0 issuewild \"${ca}\""],
  )
}
