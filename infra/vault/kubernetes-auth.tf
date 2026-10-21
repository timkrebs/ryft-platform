# Each Vault service namespace gets its own Kubernetes auth mount that trusts ONLY
# the matching Kubernetes namespace and service account.
#
# Token review: Vault verifies a pod's service-account JWT by calling the cluster's
# TokenReview API, so HCP Vault must reach the EKS API endpoint (../aws
# endpoint_public_access_cidrs must admit Vault's egress addresses). For that it uses
# a `vault-auth` service account per Kubernetes namespace bound to the
# `system:auth-delegator` cluster role; its long-lived token is the mount's
# token_reviewer_jwt. That role can review any token in the cluster, so a leaked
# reviewer token lets someone check tokens (not read secrets). Keeping one per
# namespace means rotating or revoking one leaves the other mounts working.

resource "kubernetes_service_account_v1" "vault_auth" {
  for_each = local.services

  metadata {
    name      = "vault-auth"
    namespace = kubernetes_namespace_v1.service[each.key].metadata[0].name
  }
  automount_service_account_token = false
}

resource "kubernetes_secret_v1" "vault_auth_token" {
  for_each = local.services

  metadata {
    name      = "vault-auth-token"
    namespace = kubernetes_namespace_v1.service[each.key].metadata[0].name
    annotations = {
      "kubernetes.io/service-account.name" = kubernetes_service_account_v1.vault_auth[each.key].metadata[0].name
    }
  }
  type                           = "kubernetes.io/service-account-token"
  wait_for_service_account_token = true
}

resource "kubernetes_cluster_role_binding_v1" "vault_auth_delegator" {
  for_each = local.services

  metadata {
    name = "${var.project}-${each.key}-vault-auth-delegator"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "system:auth-delegator"
  }
  subject {
    kind      = "ServiceAccount"
    name      = kubernetes_service_account_v1.vault_auth[each.key].metadata[0].name
    namespace = kubernetes_namespace_v1.service[each.key].metadata[0].name
  }
}

# The workload service account the pods run as (api, ingest, scoring, notify, migrate, …);
# deploy/k8s sets `serviceAccountName: <service>`, and the role below is bound to it.
# The Vault Secrets Operator requests short-lived tokens for it (audience "vault");
# the pods themselves never get a token, since none of them calls the Kubernetes API.
resource "kubernetes_service_account_v1" "workload" {
  for_each = local.services

  metadata {
    name      = each.value.service_account
    namespace = kubernetes_namespace_v1.service[each.key].metadata[0].name
  }
  automount_service_account_token = false
}

resource "vault_auth_backend" "kubernetes" {
  for_each = local.services

  namespace   = vault_namespace.service[each.key].path_fq
  type        = "kubernetes"
  path        = "kubernetes"
  description = "Kubernetes auth for ${each.value.k8s_ns} on ${var.eks_cluster_name}"
}

resource "vault_kubernetes_auth_backend_config" "this" {
  for_each = local.services

  namespace          = vault_namespace.service[each.key].path_fq
  backend            = vault_auth_backend.kubernetes[each.key].path
  kubernetes_host    = data.aws_eks_cluster.this.endpoint
  kubernetes_ca_cert = base64decode(data.aws_eks_cluster.this.certificate_authority[0].data)
  token_reviewer_jwt = kubernetes_secret_v1.vault_auth_token[each.key].data["token"]
  # Issuer validation stays disabled (the Vault >= 1.9 default): every login is verified
  # through the API server's TokenReview, which is the stronger check.
}

resource "vault_kubernetes_auth_backend_role" "service" {
  for_each = local.services

  namespace                        = vault_namespace.service[each.key].path_fq
  backend                          = vault_auth_backend.kubernetes[each.key].path
  role_name                        = each.key
  bound_service_account_names      = [each.value.service_account]
  bound_service_account_namespaces = [each.value.k8s_ns]
  audience                         = "vault" # VSO requests tokens with this audience
  token_policies                   = [vault_policy.service[each.key].name]
  token_ttl                        = var.token_ttl
  token_max_ttl                    = var.token_ttl * 4
}
