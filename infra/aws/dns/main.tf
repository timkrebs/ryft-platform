locals {
  # The domain (D213). A constant, not a variable: the mail records in mail.tf are this domain's.
  domain = "ryftlab.de"

  tags = merge(var.tags, {
    Project   = "ryft"
    ManagedBy = "terraform"
  })
}

# The public zone for the whole domain. GoDaddy stays the registrar. Since the DNS move (OPERATIONS →
# GCP step 6) its nameservers are Cloud DNS's (infra/gcp/dns.tf), so nothing resolves through this
# zone any more.
#
# The retirement (design docs/work/2026-11-14-gcp, PR D1): no prevent_destroy, so step 12 (E) can
# destroy the zone and its records with `plan -destroy`. A revert would block E again.
#
# The previous zone (aws_route53_zone.this, the old domain) left this configuration with D213: the
# next plan destroys it and its records. prevent_destroy guards only a resource that is still
# configured, so removing the block is what lets that happen.
resource "aws_route53_zone" "ryftlab_de" {
  name    = local.domain
  comment = "RYFT public zone, managed by infra/aws/dns"

  tags = local.tags
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
