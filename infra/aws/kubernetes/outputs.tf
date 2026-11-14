output "cluster_issuers" {
  description = "ClusterIssuers to reference from Ingress annotations (cert-manager.io/cluster-issuer)"
  value       = ["letsencrypt-staging", "letsencrypt-prod"]
}

output "default_storage_class" {
  description = "Encrypted default StorageClass"
  value       = kubernetes_storage_class_v1.gp3_encrypted.metadata[0].name
}

output "load_balancer_hostname_command" {
  description = "Prints the NLB hostname Traefik is reachable at"
  value       = "kubectl -n kube-system get svc traefik -o jsonpath='{.status.loadBalancer.ingress[0].hostname}'"
}

output "argocd_admin_password" {
  description = "Password of Argo CD's admin account (user admin, at argocd_url)"
  value       = random_password.argocd_admin.result
  sensitive   = true
}

output "argocd_url" {
  description = "The Argo CD UI and API (D103); the CLI: argocd login <host> --username admin --grpc-web"
  value       = "https://${local.argocd_host}"
}

output "admission_enabled" {
  description = "Whether the admission-policy release is installed (admission-values.yaml, D120)"
  value       = local.admission_enabled
}

output "admission_binding_actions" {
  description = "validationActions each ValidatingAdmissionPolicyBinding is given (binding => actions); Warn+Audit everywhere under break-glass"
  value       = local.admission_effective
}

output "vault_address_in_cluster" {
  description = "Vault's API inside the cluster (VaultConnection.address of the in-cluster target, infra/vault)"
  value       = "https://vault.${local.vault_namespace}.svc:8200"
}

output "vault_port_forward_command" {
  description = "Reaches Vault's API and UI on https://127.0.0.1:8200 (the only way in from outside; trust the vault-ca Secret's ca.crt)"
  value       = "kubectl --context ${var.cluster_name} -n ${local.vault_namespace} port-forward svc/vault-active 8200:8200"
}

output "vault_seal" {
  description = "Vault's seal (var.vault_seal, D208): shamir (every pod that starts is unsealed by hand with the key shares of the operator's init) or awskms (auto-unseal with infra/aws's unseal key)"
  value       = var.vault_seal
}

output "vault_unseal_role_arn" {
  description = "The Pod Identity role vault/vault unseals with under awskms (kms:Encrypt, kms:Decrypt, kms:DescribeKey on the unseal key only); null under shamir, where no role exists"
  value       = one(module.vault_unseal_pod_identity[*].iam_role_arn)
}

output "vault_snapshot_role_arn" {
  description = "The Pod Identity role vault/vault-snapshot uploads with (s3:PutObject under snapshots/, kms:GenerateDataKey through S3 on the snapshot key only)"
  value       = module.vault_snapshot_pod_identity.iam_role_arn
}

output "vault_snapshot_list_command" {
  description = "Lists the Raft snapshots in the bucket, newest last (with the operator's AWS identity; the job's role cannot list)"
  value       = "aws s3 ls s3://${local.vault_snapshots_bucket}/snapshots/ --region ${var.region}"
}

output "argocd_ui_command" {
  description = "Reaches the Argo CD UI and API on http://localhost:8080 without the Ingress"
  value       = "kubectl -n argocd port-forward svc/argocd-server 8080:80"
}
