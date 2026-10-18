output "godaddy_instructions" {
  description = "What to enter at GoDaddy to delegate the domain to Route 53"
  value       = <<-EOT
    GoDaddy → My Products → ${var.domain} → DNS → Nameservers → Change →
    "I'll use my own nameservers" → enter:
      ${join("\n  ", aws_route53_zone.this.name_servers)}
    Then check: dig NS ${var.domain} @a.nic.de +short   (should list the awsdns servers)
  EOT
}

output "name_servers" {
  description = "Route 53 nameservers to set at the registrar (GoDaddy)"
  value       = aws_route53_zone.this.name_servers
}

output "zone_arn" {
  description = "ARN of the hosted zone (IAM scope for ExternalDNS and cert-manager)"
  value       = aws_route53_zone.this.arn
}

output "zone_id" {
  description = "ID of the hosted zone"
  value       = aws_route53_zone.this.zone_id
}
