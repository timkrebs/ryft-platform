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
