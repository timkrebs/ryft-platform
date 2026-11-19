# What the destroy plan of the empty root needs (PR D2): the provider that deletes the
# resources in the state. region keeps variables.tf's old default, and a terraform.tfvars
# that still sets it stays valid (its other entries now only warn: undeclared).
variable "region" {
  description = "AWS region of the resources in the state"
  type        = string
  default     = "eu-central-1"
}

provider "aws" {
  region = var.region
}
