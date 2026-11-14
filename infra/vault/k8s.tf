# The in-cluster Vault (Community Edition, namespace vault; infra/aws/kubernetes/vault.tf), the only Vault this
# root writes (D207), through the provider in providers.tf (the port-forward, the CA file, the terraform token).
# Every Vault object of this root is in this file (tests/vault.tftest.hcl).
#
# CE has no namespaces, so the isolation is the mount, the role and the policy (D187):
#   auth/kubernetes        one mount; Vault reviews the tokens as its own service account (system:auth-delegator,
#                          the chart's vault-server-binding): no reviewer JWT, no CA, nothing long-lived
#   role <svc>             bound to exactly [<sa>] x [<k8s_ns>], audience vault
#   policy <svc>           read kv-<svc>/data/config, nothing else (the broker also its users' amqp-user-*)
#   kv-<svc>/ (v2)         the service's secrets: config (locals.tf, from the random_* of secrets.tf and the tfvars)
#   kv-operator/ (v2)      the demo password; no role's policy names it
#   role vault-snapshot    the snapshot CronJob (vault/vault-snapshot, charts/vault-extras): policy snapshot, which
#                          saves a Raft snapshot and nothing else
#
# No mount or auth backend here sets audit_non_hmac_*, passthrough_request_headers, allowed_response_headers or
# listing_visibility (tests/vault.tftest.hcl): the first two would put secret values into both audit devices in
# clear, and the policy terraform cannot refuse them inside a mount's creation (bootstrap/terraform.hcl.tftpl).
#
# The bootstrap writes what Terraform must not own (docs/OPERATIONS.md -> Vault -> Bootstrap): the audit devices,
# the token roles admin and terraform and the policies admin and terraform (bootstrap/, reviewed here, written by
# hand, so this root cannot rewrite its own token's policy).

resource "vault_auth_backend" "k8s" {
  type        = "kubernetes"
  path        = "kubernetes"
  description = "Kubernetes auth for ${var.eks_cluster_name} (Vault in the cluster)"
}

# The reviewer is Vault's own service account: the token and CA it re-reads from its pod (versions.md section 6).
# Issuer validation stays off (the deprecated field's default): every login is checked by TokenReview.
resource "vault_kubernetes_auth_backend_config" "k8s" {
  backend              = vault_auth_backend.k8s.path
  kubernetes_host      = "https://kubernetes.default.svc"
  disable_local_ca_jwt = false
}

resource "vault_mount" "k8s_kv" {
  for_each = local.services

  path        = local.k8s_kv_mount[each.key]
  type        = "kv"
  options     = { version = "2" }
  description = "Application secrets for ${each.key}"
}

resource "vault_policy" "k8s_service" {
  for_each = local.services

  name   = each.key
  policy = <<-HCL
    # ${each.key}: read its own config, nothing else.
    path "${local.k8s_kv_mount[each.key]}/data/config" {
      capabilities = ["read"]
    }
    %{~if each.value.amqp == "server"}
    # The RabbitMQ users' credentials, for the Topology Operator.
    path "${local.k8s_kv_mount[each.key]}/data/amqp-user-*" {
      capabilities = ["read"]
    }
    %{~endif}
  HCL
}

resource "vault_kubernetes_auth_backend_role" "k8s_service" {
  for_each = local.services

  backend                          = vault_auth_backend.k8s.path
  role_name                        = each.key
  bound_service_account_names      = [each.value.service_account]
  bound_service_account_namespaces = [each.value.k8s_ns]
  audience                         = "vault" # VSO requests tokens with this audience
  token_policies                   = [vault_policy.k8s_service[each.key].name]
  token_ttl                        = var.token_ttl
  token_max_ttl                    = var.token_ttl * 4
}

# Exactly the keys each service is entitled to (locals.tf). A -replace of a random_* rotates the value here in one
# apply; the Vault Secrets Operator picks it up within refreshAfter.
resource "vault_kv_secret_v2" "k8s_config" {
  for_each = local.services_with_secrets

  mount               = vault_mount.k8s_kv[each.key].path
  name                = "config"
  delete_all_versions = true
  data_json           = jsonencode(local.service_kv[each.key])
}

resource "vault_kv_secret_v2" "k8s_amqp_user" {
  for_each = local.amqp_server == null ? toset([]) : toset(local.amqp_clients)

  mount               = vault_mount.k8s_kv[local.amqp_server].path
  name                = "amqp-user-${each.key}"
  delete_all_versions = true
  data_json           = jsonencode({ username = each.key, password = random_password.amqp[each.key].result })
}

resource "vault_mount" "k8s_operator" {
  path        = "kv-operator"
  type        = "kv"
  options     = { version = "2" }
  description = "Operator-only secrets (no workload policy)"
}

resource "vault_kv_secret_v2" "k8s_demo" {
  mount               = vault_mount.k8s_operator.path
  name                = "demo"
  delete_all_versions = true
  data_json           = jsonencode({ email = var.demo_email, password = random_password.demo.result })
}

# The snapshot CronJob's identity (charts/vault-extras; local.k8s_snapshot): it may save a snapshot and nothing
# else. Restore is `update` on the same path, break-glass only, with the admin token. The job's projected token
# lives 10 minutes, and so does the Vault token it gets.
resource "vault_policy" "k8s_snapshot" {
  name   = local.k8s_snapshot.policy
  policy = <<-HCL
    path "sys/storage/raft/snapshot" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_kubernetes_auth_backend_role" "k8s_snapshot" {
  backend                          = vault_auth_backend.k8s.path
  role_name                        = local.k8s_snapshot.name
  bound_service_account_names      = [local.k8s_snapshot.service_account]
  bound_service_account_namespaces = [local.k8s_snapshot.k8s_ns]
  audience                         = "vault"
  token_policies                   = [vault_policy.k8s_snapshot.name]
  token_ttl                        = 600
  token_max_ttl                    = 600
  # A revocable service token with policy snapshot alone: without default it cannot even look itself up or revoke
  # itself, so the job lets it expire (at most 10 minutes).
  token_type              = "service"
  token_no_default_policy = true
}
