# Mail for the domain (D214): Microsoft 365 through GoDaddy, live for support@ryftlab.de. The values are
# the ones GoDaddy's DNS shows for the domain, copied exactly. They are public (anyone can query them), so
# they are code, not tfvars. A change at the provider is a change here: edit the value, plan, apply.
locals {
  mail_mx = ["0 ryftlab-de.mail.protection.outlook.com."]

  # One TXT record set at the apex: the tenant's domain verification and the SPF, each its own string.
  apex_txt = [
    "NETORGFT21311797.onmicrosoft.com",
    "v=spf1 include:secureserver.net -all",
  ]

  dmarc = "v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;"

  # Label below the domain => target. Microsoft 365 publishes DKIM as CNAMEs to its own keys.
  mail_cnames = {
    "autodiscover" = "autodiscover.outlook.com."
    # selector1 did not resolve on 2026-11-14. Once it does, add it as GoDaddy shows it (likely the same
    # shape as selector2, with selector1), one line:
    # "selector1._domainkey" = "selector1-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft."
    "selector2._domainkey" = "selector2-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft."
    "email"                = "email.secureserver.net."
    "lyncdiscover"         = "webdir.online.lync.com."
    "sip"                  = "sipdir.online.lync.com."
    "msoid"                = "clientconfig.microsoftonline-p.net."
  }

  # Label below the domain => "priority weight port target".
  mail_srv = {
    "_sip._tls"              = "100 1 443 sipdir.online.lync.com."
    "_sipfederationtls._tcp" = "100 1 5061 sipfed.online.lync.com."
  }
}

resource "aws_route53_record" "mx" {
  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = local.domain
  type    = "MX"
  ttl     = 3600
  records = local.mail_mx
}

resource "aws_route53_record" "apex_txt" {
  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = local.domain
  type    = "TXT"
  ttl     = 3600
  records = local.apex_txt
}

resource "aws_route53_record" "dmarc" {
  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = "_dmarc.${local.domain}"
  type    = "TXT"
  ttl     = 3600
  records = [local.dmarc]
}

resource "aws_route53_record" "mail_cname" {
  for_each = local.mail_cnames

  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = "${each.key}.${local.domain}"
  type    = "CNAME"
  ttl     = 3600
  records = [each.value]
}

resource "aws_route53_record" "mail_srv" {
  for_each = local.mail_srv

  zone_id = aws_route53_zone.ryftlab_de.zone_id
  name    = "${each.key}.${local.domain}"
  type    = "SRV"
  ttl     = 3600
  records = [each.value]
}
