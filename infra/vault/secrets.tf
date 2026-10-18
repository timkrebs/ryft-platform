# Generated credentials: one Postgres password per service role and one NATS password
# per client. Nobody types or sees them; each lands only in the namespaces that use
# it (local.generated). Rotate one with
#   terraform apply -replace='random_password.db["api"]'
# then re-run the migrate job (it sets the new role password) and let the operator
# restart the service.
resource "random_password" "db" {
  for_each = local.db_roles

  length  = 40
  special = false # alphanumeric: safe in a URL and for SCRAM without SASLprep
}

resource "random_password" "nats" {
  for_each = toset(local.nats_clients)

  length  = 40
  special = false
}

# Encryption key for data a service stores encrypted (the api's Apple refresh
# tokens). Never replace it: what was sealed with the old key no longer opens
# (ciphertexts carry a version byte for a future, deliberate rotation).
resource "random_bytes" "data_key" {
  for_each = { for name, s in local.services : name => s if s.data_key }

  length = 32
}

# KV v2 per service namespace at kv/, one secret `config` holding exactly the keys that
# service is entitled to. The generated VaultStaticSecret turns it into the Kubernetes
# Secret `ryft-secrets` the workload reads with envFrom.
resource "vault_mount" "kv" {
  for_each = local.services

  namespace   = vault_namespace.service[each.key].path_fq
  path        = "kv"
  type        = "kv"
  options     = { version = "2" }
  description = "Application secrets for ${each.key}"
}

resource "vault_kv_secret_v2" "config" {
  for_each = local.services_with_secrets

  namespace           = vault_namespace.service[each.key].path_fq
  mount               = vault_mount.kv[each.key].path
  name                = "config"
  delete_all_versions = true
  data_json           = jsonencode(local.service_kv[each.key])
}

resource "vault_policy" "service" {
  for_each = local.services

  namespace = vault_namespace.service[each.key].path_fq
  name      = each.key
  policy    = <<-HCL
    # ${each.key}: read its own config, nothing else in this namespace.
    path "kv/data/config" {
      capabilities = ["read"]
    }
  HCL
}

# Account deletion needs the Supabase secret key in the api namespace; without it
# DELETE /v1/me erases the data but not the Supabase login (a warning, not an error).
check "api_has_apple_keys" {
  assert {
    condition = !contains(keys(local.services), "api") || alltrue([for k in ["APPLE_TEAM_ID", "APPLE_KEY_ID", "APPLE_CLIENT_ID", "APPLE_KEY_P8"] :
    contains(local.private_keys["api"], k)])
    error_message = "service_secrets.api lacks APPLE_TEAM_ID/APPLE_KEY_ID/APPLE_CLIENT_ID/APPLE_KEY_P8: deleting a Sign in with Apple account will not revoke its Apple token (App Store requirement)."
  }
}

check "monitoring_has_its_token" {
  assert {
    condition     = !contains(keys(local.services), "monitoring") || contains(local.private_keys["monitoring"], "GC_TOKEN")
    error_message = "service_secrets.monitoring.GC_TOKEN is not set: the Grafana Cloud collector cannot reach Fleet Management."
  }
}

check "api_has_supabase_admin_key" {
  assert {
    condition     = !contains(keys(local.services), "api") || contains(local.private_keys["api"], "SUPABASE_SERVICE_ROLE_KEY")
    error_message = "service_secrets.api.SUPABASE_SERVICE_ROLE_KEY is not set: DELETE /v1/me will erase the data but cannot delete the Supabase login."
  }
}
