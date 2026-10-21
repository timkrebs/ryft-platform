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
  default     = "ryft-prod-euc1"
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

variable "vpc_cidr" {
  description = "The VPC's CIDR (infra/aws vpc_cidr): node addresses, from which the host-network metrics collectors scrape"
  type        = string
  default     = "10.0.0.0/16"

  validation {
    condition     = can(cidrhost(var.vpc_cidr, 0))
    error_message = "vpc_cidr must be a CIDR block."
  }
}

variable "tags" {
  description = "Extra tags for the IAM roles"
  type        = map(string)
  default     = {}
}
