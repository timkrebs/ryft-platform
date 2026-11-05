variable "admin_principal_arns" {
  description = "Extra cluster admins by label => IAM role or user ARN, each given AmazonEKSClusterAdminPolicy at cluster scope through an access entry. The identity that runs the apply is always an admin already (cluster_creator); list only a principal other than the apply identity (the same ARN fails with ResourceInUseException)."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for a in values(var.admin_principal_arns) : can(regex("^arn:aws[a-z-]*:iam::[0-9]{12}:(role|user)/.+$", a))])
    error_message = "admin_principal_arns values must be IAM role or user ARNs."
  }
}

variable "authentication_mode" {
  description = "API_AND_CONFIG_MAP (access entries and the aws-auth ConfigMap) or API (access entries only; aws-auth is ignored). The change to API cannot be reverted."
  type        = string
  default     = "API_AND_CONFIG_MAP"

  validation {
    condition     = contains(["API_AND_CONFIG_MAP", "API"], var.authentication_mode)
    error_message = "authentication_mode must be API_AND_CONFIG_MAP or API (CONFIG_MAP is not reachable from a cluster that already uses access entries)."
  }
}

variable "deletion_protection" {
  description = "Protect the cluster from deletion; must be turned off before `terraform destroy`"
  type        = bool
  default     = false
}

variable "endpoint_public_access" {
  description = "Expose the Kubernetes API endpoint publicly (restricted to endpoint_public_access_cidrs)"
  type        = bool
  default     = true
}

variable "endpoint_public_access_cidrs" {
  description = "CIDR blocks allowed to reach the public Kubernetes API endpoint"
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = alltrue([for c in var.endpoint_public_access_cidrs : can(cidrhost(c, 0))])
    error_message = "endpoint_public_access_cidrs must contain valid IPv4 CIDR blocks."
  }
}

variable "kubernetes_version" {
  description = "Kubernetes minor version of the control plane, e.g. 1.36"
  type        = string

  validation {
    condition     = can(regex("^1\\.[0-9]+$", var.kubernetes_version))
    error_message = "kubernetes_version must look like 1.36."
  }
}

variable "name" {
  description = "Name of the cluster, also the prefix for its IAM roles, node group and security groups (max 20 characters)"
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,19}$", var.name))
    error_message = "name must be 2-20 lowercase letters, digits or hyphens, starting with a letter."
  }
}

variable "node_access_entry" {
  description = "Create the EC2_LINUX access entry for the managed node group's role. Off: EKS creates it itself with the node group. Turn on only if `aws eks list-access-entries` does not list the role before the switch to API. Never set it back to false once true or imported: in mode API that destroys the node entry and the nodes go NotReady."
  type        = bool
  default     = false
}

variable "node_group" {
  description = "The managed node group every workload runs on"
  type = object({
    ami_type       = string
    capacity_type  = string
    instance_types = list(string)
    min_size       = number
    max_size       = number
    desired_size   = number
  })
}

variable "subnet_ids" {
  description = "Private subnets for the control plane ENIs and the nodes, in at least two availability zones"
  type        = list(string)
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}

variable "vpc_id" {
  description = "VPC the cluster runs in"
  type        = string
}
