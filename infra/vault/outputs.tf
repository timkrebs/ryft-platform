output "kubernetes_namespaces" {
  description = "Kubernetes namespace per service"
  value       = { for k, v in local.services : k => v.k8s_ns }
}

output "auth_roles" {
  description = "Kubernetes auth mount, role, service account and namespace per service on the in-cluster Vault (what each VaultAuth references)"
  # Over the roles, not local.services: `terraform import` evaluates outputs against the state, where a service
  # added since the last apply (every service, before the first apply) has no role yet.
  value = {
    for k, r in vault_kubernetes_auth_backend_role.k8s_service : k => {
      mount  = r.backend
      role   = r.role_name
      sa     = local.services[k].service_account
      k8s_ns = local.services[k].k8s_ns
    }
  }
}

output "secret_keys" {
  description = "Which keys each service's kv-<svc>/config holds (names only)"
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

output "bootstrap_terraform_policy" {
  description = "The policy terraform for the in-cluster Vault, rendered for these services (bootstrap/terraform.hcl.tftpl): the content bootstrap/terraform.hcl must hold. Read it from a plan (`show -json tfplan`), not after an apply (infra/vault/README.md -> bootstrap/). No secret; written into Vault by hand (docs/OPERATIONS.md -> Vault)."
  value       = local.bootstrap_terraform_policy_rendered
}

output "login_check" {
  description = "Manual check, in the session shell (VAULT_ADDR and VAULT_CACERT of the port-forward): log in as a service with a token the operator would use; prints the token's policies only"
  value = {
    for k, v in local.services : k => "vault write -field=token_policies auth/kubernetes/login role=${k} jwt=\"$(kubectl --context ${var.eks_cluster_name} -n ${v.k8s_ns} create token ${v.service_account} --audience vault --duration 10m)\""
  }
}

output "demo_password" {
  description = "Password of the App Review demo account, also in Vault at kv-operator/demo (docs/OPERATIONS.md → App Review demo account)"
  value       = random_password.demo.result
  sensitive   = true
}

output "admin_api_key" {
  description = "Bearer of the api's /v1/admin routes (docs/OPERATIONS.md → Feature flags)"
  value       = try(random_password.admin_key["api"].result, null)
  sensitive   = true
}
