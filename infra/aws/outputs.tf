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

output "vault_snapshots_bucket" {
  description = "Name of the S3 bucket for Vault's Raft snapshots (the CronJob writes under snapshots/)"
  value       = aws_s3_bucket.vault_snapshots.bucket
}

output "vault_snapshots_bucket_arn" {
  description = "ARN of the S3 bucket for Vault's Raft snapshots"
  value       = aws_s3_bucket.vault_snapshots.arn
}

output "vault_snapshots_kms_alias" {
  description = "Alias of the KMS key that encrypts the snapshot bucket"
  value       = aws_kms_alias.vault_snapshots.name
}

output "vault_snapshots_kms_key_arn" {
  description = "ARN of the KMS key that encrypts the snapshot bucket; every PutObject must name it"
  value       = aws_kms_key.vault_snapshots.arn
}

output "vault_unseal_kms_alias" {
  description = "Alias of Vault's auto-unseal KMS key; infra/aws/kubernetes looks the key up by it"
  value       = aws_kms_alias.vault_unseal.name
}

output "vault_unseal_kms_key_arn" {
  description = "ARN of Vault's auto-unseal KMS key; the seal stanza names the ARN, not the alias"
  value       = aws_kms_key.vault_unseal.arn
}

output "vpc_id" {
  description = "ID of the VPC"
  value       = module.network.vpc_id
}
