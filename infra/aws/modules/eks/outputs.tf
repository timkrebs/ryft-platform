output "access_entry_keys" {
  description = "Keys of the access entries this module manages (cluster_creator, admin-<label>), not the one EKS creates for the node group"
  value       = keys(module.eks.access_entries)
}

output "authentication_mode" {
  description = "Authentication mode requested in the configuration, not read back from AWS"
  value       = var.authentication_mode
}

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

output "node_access_entry_principal" {
  description = "Node role the EC2_LINUX access entry is managed for; null while EKS owns that entry"
  value       = try(aws_eks_access_entry.node[0].principal_arn, null)
}

output "node_role_arn" {
  description = "IAM role of the managed node group; the role aws eks list-access-entries must show as EC2_LINUX before the switch to API"
  value       = module.eks.eks_managed_node_groups["default"].iam_role_arn
}

output "node_security_group_id" {
  description = "Security group attached to the nodes"
  value       = module.eks.node_security_group_id
}

output "oidc_provider_arn" {
  description = "ARN of the cluster's IAM OIDC provider, for IRSA roles"
  value       = module.eks.oidc_provider_arn
}
