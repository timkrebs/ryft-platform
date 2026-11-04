output "authentication_mode" {
  description = "Authentication mode the cluster is configured with (API makes kube-system/aws-auth inert)"
  value       = module.eks.authentication_mode
}

output "cluster_endpoint" {
  description = "Endpoint of the Kubernetes API server"
  value       = module.eks.cluster_endpoint
}

output "cluster_name" {
  description = "Name of the EKS cluster"
  value       = module.eks.cluster_name
}

output "cluster_security_group_id" {
  description = "Security group attached to the control plane"
  value       = module.eks.cluster_security_group_id
}

output "configure_kubectl" {
  description = "Command that adds the cluster to your kubeconfig"
  value       = "aws eks update-kubeconfig --region ${var.region} --name ${module.eks.cluster_name} --alias ${module.eks.cluster_name}"
}

output "node_security_group_id" {
  description = "Security group attached to the nodes"
  value       = module.eks.node_security_group_id
}

output "node_role_arn" {
  description = "IAM role of the managed node group; must be listed by aws eks list-access-entries (EC2_LINUX) before authentication_mode becomes API"
  value       = module.eks.node_role_arn
}

output "oidc_provider_arn" {
  description = "ARN of the cluster's IAM OIDC provider, for IRSA roles"
  value       = module.eks.oidc_provider_arn
}

output "private_subnet_ids" {
  description = "IDs of the private subnets"
  value       = module.network.private_subnet_ids
}

output "region" {
  description = "AWS region"
  value       = var.region
}

output "vpc_id" {
  description = "ID of the VPC"
  value       = module.network.vpc_id
}
