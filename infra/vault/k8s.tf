# The in-cluster Vault (Community Edition, namespace vault; infra/aws/kubernetes/vault.tf), written beside HCP
# while local.write_k8s is true (targets.tf, D191). Every resource here is new: nothing on HCP changes, and with
# write_k8s false none of them exists (the plan of PR 3 adds nothing here).
#
# CE has no namespaces, so the isolation is the mount, the role and the policy (D187):
#   auth/kubernetes        one mount; Vault reviews the tokens as its own service account (system:auth-delegator,
#                          the chart's vault-server-binding): no reviewer JWT, no CA, nothing long-lived
#   role <svc>             bound to exactly [<sa>] x [<k8s_ns>], audience vault, the HCP TTLs
#   policy <svc>           read kv-<svc>/data/config, nothing else (the broker also its users' amqp-user-*)
#   kv-<svc>/ (v2)         the same secrets as HCP's kv/ in admin/ryft/<svc>, from the same locals and random_*
#   kv-operator/ (v2)      the demo password; no role's policy names it
#   policy and role vault-snapshot   the snapshot CronJob's identity (used from PR 4 on)
#
# The bootstrap writes what Terraform must not own (docs/OPERATIONS.md -> Vault -> Bootstrap): the audit devices,
# the OIDC login, and the policies admin and terraform (bootstrap/*.hcl, reviewed here, written by hand, so this
# root cannot widen its own token).

locals {
  k8s_services = local.write_k8s ? local.services : {}
}

resource "vault_auth_backend" "k8s" {
  count    = local.write_k8s ? 1 : 0
  provider = vault.k8s

  type        = "kubernetes"
  path        = "kubernetes"
  description = "Kubernetes auth for ${var.eks_cluster_name} (Vault in the cluster)"
}

# The reviewer is Vault's own service account: the token and CA it re-reads from its pod (versions.md section 6).
# Issuer validation stays off (the deprecated field's default): every login is checked by TokenReview.
resource "vault_kubernetes_auth_backend_config" "k8s" {
  count    = local.write_k8s ? 1 : 0
  provider = vault.k8s

  backend              = vault_auth_backend.k8s[0].path
  kubernetes_host      = "https://kubernetes.default.svc"
  disable_local_ca_jwt = false
}

resource "vault_mount" "k8s_kv" {
  for_each = local.k8s_services
  provider = vault.k8s

  path        = local.k8s_kv_mount[each.key]
  type        = "kv"
  options     = { version = "2" }
  description = "Application secrets for ${each.key}"
}

resource "vault_policy" "k8s_service" {
  for_each = local.k8s_services
  provider = vault.k8s

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
  for_each = local.k8s_services
  provider = vault.k8s

  backend                          = vault_auth_backend.k8s[0].path
  role_name                        = each.key
  bound_service_account_names      = [each.value.service_account]
  bound_service_account_namespaces = [each.value.k8s_ns]
  audience                         = "vault" # VSO requests tokens with this audience
  token_policies                   = [vault_policy.k8s_service[each.key].name]
  token_ttl                        = var.token_ttl
  token_max_ttl                    = var.token_ttl * 4
}

# The same locals as the HCP side (secrets.tf, operator.tf): the same values, a copy, not a rotation. A
# -replace of a random_* rotates both Vaults in one apply.
resource "vault_kv_secret_v2" "k8s_config" {
  for_each = local.write_k8s ? local.services_with_secrets : {}
  provider = vault.k8s

  mount               = vault_mount.k8s_kv[each.key].path
  name                = "config"
  delete_all_versions = true
  data_json           = jsonencode(local.service_kv[each.key])
}

resource "vault_kv_secret_v2" "k8s_amqp_user" {
  for_each = local.write_k8s && local.amqp_server != null ? toset(local.amqp_clients) : toset([])
  provider = vault.k8s

  mount               = vault_mount.k8s_kv[local.amqp_server].path
  name                = "amqp-user-${each.key}"
  delete_all_versions = true
  data_json           = jsonencode({ username = each.key, password = random_password.amqp[each.key].result })
}

resource "vault_mount" "k8s_operator" {
  count    = local.write_k8s ? 1 : 0
  provider = vault.k8s

  path        = "kv-operator"
  type        = "kv"
  options     = { version = "2" }
  description = "Operator-only secrets (no workload policy)"
}

resource "vault_kv_secret_v2" "k8s_demo" {
  count    = local.write_k8s ? 1 : 0
  provider = vault.k8s

  mount               = vault_mount.k8s_operator[0].path
  name                = "demo"
  delete_all_versions = true
  data_json           = jsonencode({ email = var.demo_email, password = random_password.demo.result })
}

# The snapshot CronJob's identity (charts/vault-extras, PR 4): it may save a snapshot and nothing else (restore
# is `update` on the same path, break-glass only, with the admin login).
resource "vault_policy" "k8s_snapshot" {
  count    = local.write_k8s ? 1 : 0
  provider = vault.k8s

  name   = "vault-snapshot"
  policy = <<-HCL
    # vault-snapshot: save a Raft snapshot, nothing else.
    path "sys/storage/raft/snapshot" {
      capabilities = ["read"]
    }
  HCL
}

resource "vault_kubernetes_auth_backend_role" "k8s_snapshot" {
  count    = local.write_k8s ? 1 : 0
  provider = vault.k8s

  backend                          = vault_auth_backend.k8s[0].path
  role_name                        = "vault-snapshot"
  bound_service_account_names      = ["vault-snapshot"]
  bound_service_account_namespaces = ["vault"]
  audience                         = "vault"
  token_policies                   = [vault_policy.k8s_snapshot[0].name]
  token_ttl                        = 600
  token_max_ttl                    = 600
}
