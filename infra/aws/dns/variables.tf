variable "caa_issuers" {
  description = "Certificate authorities allowed to issue for the domain (CAA). cert-manager uses Let's Encrypt."
  type        = list(string)
  default     = ["letsencrypt.org"]

  validation {
    condition     = length(var.caa_issuers) > 0
    error_message = "caa_issuers must name at least one certificate authority."
  }
}

variable "domain" {
  description = "Registered domain whose DNS Route 53 hosts"
  type        = string
  default     = "ryftapp.de"

  validation {
    condition     = can(regex("^([a-z0-9-]+\\.)+[a-z]{2,}$", var.domain))
    error_message = "domain must be a lowercase DNS name like ryftapp.de."
  }
}

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
