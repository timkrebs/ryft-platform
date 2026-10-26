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

variable "admission_break_glass" {
  description = "Break-glass for the admission policies (D120): every binding gets Warn+Audit whatever admission-values.yaml says. Planned use: apply -target=helm_release.admission_policy with -var='admission_break_glass=true'; leave it false otherwise."
  type        = bool
  default     = false
}

variable "admission_actions" {
  description = "Overrides the action of single bindings over admission-values.yaml for one plan (binding => audit | deny). A step of the rollout is a reviewed change of the file instead."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for v in values(var.admission_actions) : contains(["audit", "deny"], v)])
    error_message = "Each admission action must be audit (Warn+Audit) or deny (Deny+Audit)."
  }

  validation {
    condition     = alltrue([for k in keys(var.admission_actions) : contains(keys(yamldecode(file("${path.module}/charts/admission-policy/values.yaml")).actions), k)])
    error_message = "admission_actions names a binding the admission-policy chart does not have."
  }
}

variable "admission_scoped_rbac_file" {
  description = "The file whose presence lets operators-cluster go to deny: T12-g's deploy/rabbitmq/operators/cluster-operator/scoped-rbac.yaml. null uses that path; a test points it elsewhere."
  type        = string
  default     = null
}

variable "admission_enabled" {
  description = "Overrides `enabled` of admission-values.yaml for one plan (tests, a dry look at the release). null uses the file; the real switch is the reviewed file, so do not run an apply with this set."
  type        = bool
  default     = null
}
