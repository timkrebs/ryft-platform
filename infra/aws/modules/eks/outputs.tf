output "cluster_certificate_authority_data" {
  description = "Base64-encoded certificate data of the cluster CA"
  value       = module.eks.cluster_certificate_authority_data
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

output "cluster_version" {
  description = "Kubernetes version of the control plane"
  value       = module.eks.cluster_version
}

output "ebs_csi_role_arn" {
  description = "IAM role the EBS CSI controller assumes through Pod Identity"
  value       = module.ebs_csi_pod_identity.iam_role_arn
}

output "node_security_group_id" {
  description = "Security group attached to the nodes"
  value       = module.eks.node_security_group_id
}

output "oidc_provider_arn" {
  description = "ARN of the cluster's IAM OIDC provider, for IRSA roles"
  value       = module.eks.oidc_provider_arn
}
