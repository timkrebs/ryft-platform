output "godaddy_instructions" {
  description = "What to enter at GoDaddy to delegate the domain to Route 53"
  value       = <<-EOT
    GoDaddy → My Products → ${local.domain} → DNS → Nameservers → Change →
    "I'll use my own nameservers" → enter (without the trailing dot):
      ${join("\n  ", aws_route53_zone.ryftlab_de.name_servers)}
    Then check: dig +norecurse NS ${local.domain} @a.nic.de   (should list the awsdns servers)
  EOT
}

output "name_servers" {
  description = "The four Route 53 nameservers to set at the registrar (GoDaddy)"
  value       = aws_route53_zone.ryftlab_de.name_servers
}

output "zone_arn" {
  description = "ARN of the hosted zone (IAM scope for ExternalDNS and cert-manager)"
  value       = aws_route53_zone.ryftlab_de.arn
}

output "zone_id" {
  description = "ID of the hosted zone"
  value       = aws_route53_zone.ryftlab_de.zone_id
}
