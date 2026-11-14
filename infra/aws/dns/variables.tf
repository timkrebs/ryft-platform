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

# Mail (D212). The defaults say "this domain sends and receives no mail": a null MX, SPF -all, DMARC reject and no
# DKIM key, so nobody can deliver to an @ryftapp.de address or send as one. A mail provider is one tfvars block
# away: terraform.tfvars.example has Google Workspace's values (D142) as the example.

variable "mx_records" {
  description = "MX values (priority and host) of the mail service. Default: the RFC 7505 null MX \"0 .\", the domain accepts no mail (senders bounce at once instead of retrying)."
  type        = list(string)
  default     = ["0 ."]

  validation {
    condition     = length(var.mx_records) > 0 && alltrue([for r in var.mx_records : can(regex("^[0-9]{1,5} [^ ]+$", r))])
    error_message = "mx_records must hold at least one value of the form \"<priority> <host>\", e.g. \"0 .\" or \"1 smtp.google.com\"."
  }

  validation {
    condition     = !contains(var.mx_records, "0 .") || length(var.mx_records) == 1
    error_message = "A null MX (\"0 .\") must be the only MX value (RFC 7505)."
  }
}

variable "apex_txt_verifications" {
  description = "Domain verification values a provider asks for at the apex (e.g. google-site-verification=...), each one TXT value without quotes, next to the SPF. Default none."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for v in var.apex_txt_verifications : length(v) > 0 && length(v) <= 255 && !strcontains(v, "\"") && !startswith(v, "v=spf1")])
    error_message = "Each apex_txt_verifications value is one TXT string of 1-255 characters without quotes, and not an SPF record (that is var.spf)."
  }
}

variable "spf" {
  description = "SPF, an apex TXT value (no quotes). Default: v=spf1 -all, no host may send mail as the domain."
  type        = string
  default     = "v=spf1 -all"

  validation {
    condition     = startswith(var.spf, "v=spf1 ")
    error_message = "spf must start with v=spf1."
  }
}

variable "dkim_selector" {
  description = "Selector of the provider's DKIM key, the label before ._domainkey (Google Workspace: google). Only with dkim_record."
  type        = string
  default     = null

  validation {
    condition     = var.dkim_selector == null || can(regex("^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$", var.dkim_selector))
    error_message = "dkim_selector must be one DNS label (letters, digits and inner hyphens)."
  }
}

variable "dkim_record" {
  description = "The DKIM TXT value as the provider shows it (v=DKIM1; k=rsa; p=...), one string without quotes; the record splits it into 255-character strings. Default null: no DKIM record (no mail is sent)."
  type        = string
  default     = null

  validation {
    condition     = var.dkim_record == null || can(regex("^v=DKIM1", var.dkim_record))
    error_message = "dkim_record must start with v=DKIM1 and carry the p= key."
  }

  validation {
    condition     = var.dkim_record == null || var.dkim_selector != null
    error_message = "dkim_record needs dkim_selector (the label before ._domainkey)."
  }
}

variable "dmarc" {
  description = "DMARC policy at _dmarc. Default: p=reject with strict alignment and no rua (no mailbox for reports). With a provider: p=none and rua while reports are collected, quarantine after 2-4 clean weeks (OPERATIONS)."
  type        = string
  default     = "v=DMARC1; p=reject; adkim=s; aspf=s"

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
