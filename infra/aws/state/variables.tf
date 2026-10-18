variable "region" {
  description = "Region of the state bucket (the cluster's region)"
  type        = string
  default     = "eu-central-1"
}

variable "noncurrent_days" {
  description = "Days an overwritten state version is kept (recovery from a bad apply)"
  type        = number
  default     = 90
}
