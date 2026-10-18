variable "az_count" {
  description = "Number of availability zones to spread the subnets over"
  type        = number
  default     = 3

  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3 (EKS needs subnets in at least two zones)."
  }
}

variable "cidr" {
  description = "IPv4 CIDR block of the VPC, /16 to /20"
  type        = string

  validation {
    condition     = can(cidrhost(var.cidr, 0)) && tonumber(split("/", var.cidr)[1]) >= 16 && tonumber(split("/", var.cidr)[1]) <= 20
    error_message = "cidr must be a valid IPv4 CIDR block between /16 and /20."
  }
}

variable "flow_log_retention_in_days" {
  description = "Days to keep VPC flow logs in CloudWatch; 0 disables flow logs"
  type        = number
  default     = 30

  validation {
    condition     = var.flow_log_retention_in_days == 0 || contains([1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653], var.flow_log_retention_in_days)
    error_message = "flow_log_retention_in_days must be 0 or a CloudWatch Logs retention period (1, 3, 5, 7, 14, 30, 60, 90, ...)."
  }
}

variable "name" {
  description = "Name of the VPC, used as prefix for its subnets, route tables and gateways"
  type        = string
}

variable "single_nat_gateway" {
  description = "Use one NAT gateway for all private subnets (cheaper) instead of one per availability zone (zone-redundant egress)"
  type        = bool
  default     = true
}

variable "tags" {
  description = "Tags applied to every resource"
  type        = map(string)
  default     = {}
}
