variable "admin_principal_arns" {
  description = "Extra cluster admins by label => IAM role or user ARN (AmazonEKSClusterAdminPolicy, cluster scope, through access entries). The identity that runs the apply is always an admin (cluster_creator, and it follows whoever runs the plan); list only a principal other than the apply identity, the same ARN fails with ResourceInUseException. Empty = none."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for a in values(var.admin_principal_arns) : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:(role|user)/.+$", a))])
    error_message = "admin_principal_arns values must be IAM role or user ARNs."
  }
}

variable "authentication_mode" {
  description = "EKS access-entry authentication mode: API_AND_CONFIG_MAP (today) or API (aws-auth ignored). One-way: API cannot be reverted. Switch only after the pre-checks in docs/OPERATIONS.md (EKS authentication mode API): set it in terraform.tfvars and keep it there (a plan without it proposes API -> API_AND_CONFIG_MAP, which EKS rejects); a follow-up commit then changes this default."
  type        = string
  default     = "API_AND_CONFIG_MAP"

  validation {
    condition     = contains(["API_AND_CONFIG_MAP", "API"], var.authentication_mode)
    error_message = "authentication_mode must be API_AND_CONFIG_MAP or API."
  }
}

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
    error_message = "endpoint_public_access_cidrs must be a non-empty list of IPv4 CIDR blocks, e.g. [\"0.0.0.0/0\"]."
  }

  # EKS rejects the documentation ranges (RFC 5737), and an example address copied
  # into tfvars would otherwise fail only halfway through an apply.
  validation {
    condition = !anytrue([for c in var.endpoint_public_access_cidrs :
    can(regex("^(192\\.0\\.2|198\\.51\\.100|203\\.0\\.113)\\.", try(cidrhost(c, 0), "")))])
    error_message = "endpoint_public_access_cidrs holds a documentation address (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24), which EKS rejects. Use 0.0.0.0/0, or your own address together with HCP Vault's egress addresses."
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

variable "node_access_entry" {
  description = "Create the EC2_LINUX access entry for the node group's role. Leave false: EKS creates that entry itself. true only if `aws eks list-access-entries` does not list the node role before the switch to API (docs/OPERATIONS.md). Never set it back to false once true or imported: in mode API that destroys the node entry and the nodes go NotReady."
  type        = bool
  default     = false
}

variable "node_group" {
  description = "Managed node group all workloads run on. A t3.large takes 35 pods and 8 GiB; three hold RYFT, the add-ons and the whole Grafana Cloud stack with room for the HPAs (D64). A t3.medium (17 pods) no longer fits it."
  type = object({
    ami_type       = optional(string, "AL2023_x86_64_STANDARD")
    capacity_type  = optional(string, "ON_DEMAND")
    instance_types = optional(list(string), ["t3.large"])
    min_size       = optional(number, 3)
    max_size       = optional(number, 4)
    desired_size   = optional(number, 3)
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
