# Parent namespace for the project, then one child per service.
# Paths (relative to the provider's namespace, admin/ on HCP):
# ryft/ → ryft/ingest/, ryft/scoring/, ryft/api/, ryft/nats/, ryft/migrate/

resource "vault_namespace" "project" {
  path = var.project
}

resource "vault_namespace" "service" {
  for_each = local.services

  namespace = vault_namespace.project.path_fq
  path      = each.key
}

# Matching Kubernetes namespaces. Pod Security "restricted": no root, no privilege
# escalation, no host access, seccomp on; every RYFT pod is built for it.
resource "kubernetes_namespace_v1" "service" {
  for_each = local.services

  metadata {
    name = each.value.k8s_ns
    labels = {
      "app.kubernetes.io/part-of"          = var.project
      "ryft.dev/service"                   = each.key
      "ryft.dev/vault-namespace"           = replace(each.value.vault_ns_full, "/", "_")
      "pod-security.kubernetes.io/enforce" = "restricted"
      "pod-security.kubernetes.io/warn"    = "restricted"
      "pod-security.kubernetes.io/audit"   = "restricted"
    }
  }
}
