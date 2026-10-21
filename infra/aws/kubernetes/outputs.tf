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
  description = "Password of Argo CD's admin account (the UI through kubectl -n argocd port-forward svc/argocd-server 8080:443)"
  value       = random_password.argocd_admin.result
  sensitive   = true
}

output "argocd_ui_command" {
  description = "Reaches the Argo CD UI and API on https://localhost:8080 (no Ingress, D94)"
  value       = "kubectl -n argocd port-forward svc/argocd-server 8080:443"
}
