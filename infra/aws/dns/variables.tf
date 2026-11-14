variable "caa_issuers" {
  description = "Certificate authorities allowed to issue for the domain (CAA). cert-manager uses Let's Encrypt."
  type        = list(string)
  default     = ["letsencrypt.org"]

  validation {
    condition     = length(var.caa_issuers) > 0
    error_message = "caa_issuers must name at least one certificate authority."
  }
}

# The domain is local.domain (main.tf) and the mail records are code (mail.tf, D214): neither is a variable.

variable "region" {
  description = "AWS region for the provider (Route 53 itself is global)"
  type        = string
  default     = "eu-central-1"
}

variable "tags" {
  description = "Extra tags for the hosted zone"
  type        = map(string)
  default     = {}
}
