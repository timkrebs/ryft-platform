output "azs" {
  description = "Availability zones the subnets are in"
  value       = module.vpc.azs
}

output "private_subnet_cidr_blocks" {
  description = "CIDR blocks of the private subnets"
  value       = module.vpc.private_subnets_cidr_blocks
}

output "private_subnet_ids" {
  description = "IDs of the private subnets (EKS nodes and pods)"
  value       = module.vpc.private_subnets
}

output "public_subnet_cidr_blocks" {
  description = "CIDR blocks of the public subnets"
  value       = module.vpc.public_subnets_cidr_blocks
}

output "public_subnet_ids" {
  description = "IDs of the public subnets (internet-facing load balancers, NAT)"
  value       = module.vpc.public_subnets
}

output "vpc_cidr_block" {
  description = "CIDR block of the VPC"
  value       = module.vpc.vpc_cidr_block
}

output "vpc_id" {
  description = "ID of the VPC"
  value       = module.vpc.vpc_id
}
