variable "deletion_protection" {
  description = "Protect the EKS cluster from deletion; turn off before `terraform destroy`"
  type        = bool
  default     = false
}

variable "endpoint_public_access_cidrs" {
  description = "CIDR blocks allowed to reach the public Kubernetes API endpoint; narrow this to your own IP(s)"
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = length(var.endpoint_public_access_cidrs) > 0 && alltrue([for c in var.endpoint_public_access_cidrs : can(cidrhost(c, 0))])
    error_message = "endpoint_public_access_cidrs must be a non-empty list of IPv4 CIDR blocks, e.g. [\"203.0.113.10/32\"]."
  }
}

variable "environment" {
  description = "Environment name, the second part of every resource name (e.g. prod, dev)"
  type        = string
  default     = "prod"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,8}$", var.environment))
    error_message = "environment must be 2-9 lowercase letters or digits, starting with a letter."
  }
}

variable "location" {
  description = "Location name, the third part of every resource name (e.g. euc1)"
  type        = string
  default     = "euc1"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,8}$", var.location))
    error_message = "location must be 2-9 lowercase letters or digits, starting with a letter."
  }
}

variable "kubernetes_version" {
  description = "Kubernetes minor version of the EKS control plane"
  type        = string
  default     = "1.36"
}

variable "node_group" {
  description = "Managed node group all workloads run on. t3.medium fits RYFT plus the add-ons with room for the HPAs; t3.small (11 pods per node) does not."
  type = object({
    ami_type       = optional(string, "AL2023_x86_64_STANDARD")
    capacity_type  = optional(string, "ON_DEMAND")
    instance_types = optional(list(string), ["t3.medium"])
    min_size       = optional(number, 2)
    max_size       = optional(number, 4)
    desired_size   = optional(number, 2)
  })
  default = {}

  validation {
    condition     = var.node_group.min_size <= var.node_group.desired_size && var.node_group.desired_size <= var.node_group.max_size
    error_message = "node_group sizes must satisfy min_size <= desired_size <= max_size."
  }

  validation {
    condition     = contains(["ON_DEMAND", "SPOT"], var.node_group.capacity_type)
    error_message = "node_group.capacity_type must be ON_DEMAND or SPOT."
  }
}

variable "project" {
  description = "Project name, the first part of every resource name"
  type        = string
  default     = "ryft"

  validation {
    condition     = can(regex("^[a-z][a-z0-9]{1,9}$", var.project))
    error_message = "project must be 2-10 lowercase letters or digits, starting with a letter."
  }
}

variable "region" {
  description = "AWS region"
  type        = string
  default     = "eu-central-1"
}

variable "single_nat_gateway" {
  description = "One NAT gateway for all zones (cheaper) instead of one per zone (zone-redundant egress)"
  type        = bool
  default     = true
}

variable "tags" {
  description = "Extra tags for every resource; Project, Environment and ManagedBy are always set"
  type        = map(string)
  default     = {}
}

variable "vpc_flow_log_retention_in_days" {
  description = "Days to keep VPC flow logs in CloudWatch; 0 disables them"
  type        = number
  default     = 30
}

variable "vpc_cidr" {
  description = "IPv4 CIDR block of the VPC"
  type        = string
  default     = "10.0.0.0/16"
}

variable "budget_alert_emails" {
  description = "Who gets the monthly cost alerts (80 % actual, 100 % forecast). Empty = no budget."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for e in var.budget_alert_emails : can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", e))])
    error_message = "budget_alert_emails must be email addresses."
  }
}

variable "monthly_budget_usd" {
  description = "Monthly cost budget in USD (AWS Budgets bills and alerts in USD); 165 ≈ €150"
  type        = number
  default     = 165
}
