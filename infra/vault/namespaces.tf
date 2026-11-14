# One Kubernetes namespace per service (ryft-ingest, ryft-scoring, ryft-api, …, monitoring), and the service
# account its workload runs as. Pod Security "restricted" (no root, no privilege escalation, no host access,
# seccomp on), which every RYFT pod is built for; only the monitoring collector's namespace is "privileged" (D51).
# The label ryft.dev/vault-namespace (HCP's namespace path) is gone with HCP (D207): nothing selected on it.
resource "kubernetes_namespace_v1" "service" {
  for_each = local.services

  metadata {
    name = each.value.k8s_ns
    labels = {
      "app.kubernetes.io/part-of"          = var.project
      "ryft.dev/service"                   = each.key
      "pod-security.kubernetes.io/enforce" = each.value.pod_security
      "pod-security.kubernetes.io/warn"    = each.value.pod_security
      "pod-security.kubernetes.io/audit"   = each.value.pod_security
    }
  }
}

# The workload service account the pods run as (api, ingest, scoring, notify, migrate, …);
# deploy/k8s sets `serviceAccountName: <service>`, and the service's role on the in-cluster Vault (k8s.tf) is bound
# to it. The Vault Secrets Operator requests short-lived tokens for it (audience "vault"), which Vault reviews as its
# own service account (TokenReview, D187); the pods themselves never get a token, since none of them calls the
# Kubernetes API.
resource "kubernetes_service_account_v1" "workload" {
  for_each = local.services

  metadata {
    name      = each.value.service_account
    namespace = kubernetes_namespace_v1.service[each.key].metadata[0].name
  }
  automount_service_account_token = false
}
