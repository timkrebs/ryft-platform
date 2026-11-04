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

output "argocd_ui_command" {
  description = "Reaches the Argo CD UI and API on http://localhost:8080 without the Ingress"
  value       = "kubectl -n argocd port-forward svc/argocd-server 8080:80"
}
