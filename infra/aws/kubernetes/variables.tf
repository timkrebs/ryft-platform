variable "acme_email" {
  description = "Email for the Let's Encrypt account (expiry and policy notices)"
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.acme_email))
    error_message = "acme_email must be an email address."
  }
}

variable "cluster_name" {
  description = "Name of the EKS cluster created by infra/aws"
  type        = string
  default     = "ryft-prod"
}

variable "domain" {
  description = "Route 53 zone created by infra/aws/dns; Ingress hosts live under it"
  type        = string
  default     = "ryftapp.de"
}

variable "region" {
  description = "AWS region of the cluster"
  type        = string
  default     = "eu-central-1"
}

variable "tags" {
  description = "Extra tags for the IAM roles"
  type        = map(string)
  default     = {}
}
