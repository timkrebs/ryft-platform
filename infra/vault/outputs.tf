output "vault_namespaces" {
  description = "Vault namespace per service, from the root (what VAULT_NAMESPACE and the operator use)"
  value       = { for k, v in local.services : k => v.vault_ns_full }
}

output "kubernetes_namespaces" {
  description = "Kubernetes namespace per service"
  value       = { for k, v in local.services : k => v.k8s_ns }
}

output "auth_roles" {
  description = "Kubernetes auth mount and role per service (what VaultAuth references)"
  # Over the roles, not local.services: `terraform import` evaluates outputs against
  # the state, where a service added since the last apply has no role yet.
  value = {
    for k, r in vault_kubernetes_auth_backend_role.service : k => {
      namespace = local.services[k].vault_ns_full
      mount     = r.backend
      role      = r.role_name
      sa        = local.services[k].service_account
    }
  }
}

output "secret_keys" {
  description = "Which keys each service's kv/config holds (names only)"
  value       = local.service_secret_keys
}

output "database_roles" {
  description = "Postgres role per service; its pooler user is <role>.<project ref>"
  value       = local.db_roles
}

output "vso_manifests" {
  description = "Generated kustomize directories"
  value       = { for k, f in local_file.vso : k => dirname(f.filename) }
}

output "login_check" {
  description = "Manual check: log in as a service with a token the operator would use"
  value = {
    for k, v in local.services : k => "VAULT_NAMESPACE=${v.vault_ns_full} vault write auth/kubernetes/login role=${k} jwt=\"$(kubectl -n ${v.k8s_ns} create token ${v.service_account} --audience vault --duration 10m)\""
  }
}
