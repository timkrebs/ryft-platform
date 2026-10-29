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

variable "mx_records" {
  description = "MX values (priority and host) of the mail service: Google Workspace"
  type        = list(string)
  default     = ["1 smtp.google.com"]
}

variable "google_site_verification" {
  description = "Google Workspace domain verification, an apex TXT value (no quotes)"
  type        = string
  default     = "google-site-verification=eUj_Gi_u5TMp0qhySZvarTISldu_UzkiJkRoIJBmfR8"
}

variable "spf" {
  description = "SPF, an apex TXT value (no quotes)"
  type        = string
  default     = "v=spf1 include:_spf.google.com ~all"

  validation {
    condition     = startswith(var.spf, "v=spf1 ")
    error_message = "spf must start with v=spf1."
  }
}

variable "dkim_selector" {
  description = "Selector of the Workspace DKIM key (google unless the key was generated with another)"
  type        = string
  default     = "google"
}

variable "dkim_record" {
  description = "The DKIM TXT value as Workspace shows it (v=DKIM1; k=rsa; p=...), one string without quotes; the record splits it into 255-character strings. No default on purpose: copy it from the live record so the import plans no change."
  type        = string

  validation {
    condition     = startswith(var.dkim_record, "v=DKIM1")
    error_message = "dkim_record must start with v=DKIM1 and carry the p= key."
  }
}

variable "dmarc" {
  description = "DMARC policy at _dmarc: p=none while reports are collected; quarantine after 2-4 clean weeks (OPERATIONS)."
  type        = string
  default     = "v=DMARC1; p=none; rua=mailto:support@ryftapp.de; adkim=s; aspf=s"

  validation {
    condition     = startswith(var.dmarc, "v=DMARC1;")
    error_message = "dmarc must start with v=DMARC1;."
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
