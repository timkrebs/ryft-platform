# Generated credentials: one Postgres password per service role and one RabbitMQ
# password per client. Nobody types or sees them; each lands only in the namespaces that use
# it (local.generated). Rotate one with
#   terraform apply -replace='random_password.db["api"]'
# then re-run the migrate job (it sets the new role password) and let the operator
# restart the service.
resource "random_password" "db" {
  for_each = local.db_roles

  length  = 40
  special = false # alphanumeric: safe in a URL and for SCRAM without SASLprep
}

# RabbitMQ (D97): a password per client. The client's own mount gets it in
# kv-<client>/config (AMQP_PASSWORD); the broker's mount gets {username, password} at
# kv-rabbitmq/amqp-user-<client> (k8s.tf), which the Topology Operator's User object imports (the
# broker holds plaintext here: its namespace already holds the operator's administrator, so a
# hash would protect nothing more). Rotate one with
#   terraform apply -replace='random_password.amqp["api"]'
# the operator updates the user, and the Vault Secrets Operator restarts the client.
resource "random_password" "amqp" {
  for_each = toset(local.amqp_clients)

  length  = 40
  special = false
}

# Encryption key for data a service stores encrypted (the api's Apple refresh
# tokens). Never replace it: what was sealed with the old key no longer opens
# (ciphertexts carry a version byte for a future, deliberate rotation). The rebuild
# imports the old value into random_bytes.data_key["api"] before the first apply (D211,
# docs/OPERATIONS.md -> Vault -> The first apply), so this address must not change; the
# import sets length from the value (32 bytes), which equals the configuration.
#
# prevent_destroy: after the import this state is the key's only copy (D211). A plan that
# would destroy or replace it (a -replace, a changed length, the api's data_key flag
# dropped, a destroy) fails instead. A deliberate rotation is a reviewed PR that removes
# this line first. random_password.demo has no such guard: its rotation is a -replace
# (README -> The App Review demo password).
resource "random_bytes" "data_key" {
  for_each = { for name, s in local.services : name => s if s.data_key }

  length = 32

  lifecycle {
    prevent_destroy = true
  }
}

# Bearer of the api's /v1/admin routes (feature flags). Rotate it with
#   terraform apply -replace='random_password.admin_key["api"]'
# the operator then restarts the api with the new key.
resource "random_password" "admin_key" {
  for_each = { for name, s in local.services : name => s if s.admin_key }

  length  = 48
  special = false
}

# Each service's KV v2 mount kv-<svc> holds one secret `config` with exactly the keys that
# service is entitled to (k8s.tf, vault_kv_secret_v2.k8s_config). The generated
# VaultStaticSecret turns it into the Kubernetes Secret `ryft-secrets` the workload reads
# with envFrom.

check "whoop_import_has_its_keys" {
  assert {
    condition = !contains(keys(local.services), "whoop-import") || alltrue([for k in ["client_id", "client_secret", "refresh_token"] :
    contains(local.private_keys["whoop-import"], k)])
    error_message = "service_secrets.whoop-import needs client_id, client_secret and refresh_token."
  }
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

# Without the APNs key notify runs but sends nothing (the Apple Developer
# Program membership comes first): a warning, not an error.
check "notify_has_apns_key" {
  assert {
    condition     = !contains(keys(local.services), "notify") || contains(local.private_keys["notify"], "APNS_KEY_P8")
    error_message = "service_secrets.notify lacks APNS_KEY_ID/APNS_TEAM_ID/APNS_KEY_P8: notify runs but sends no push notifications."
  }
}

check "monitoring_has_its_token" {
  assert {
    condition = !contains(keys(local.services), "monitoring") || (contains(local.private_keys["monitoring"], "GC_TOKEN")
    && contains(local.private_keys["monitoring"], "PROM_USERNAME"))
    error_message = "service_secrets.monitoring needs GC_TOKEN (the Grafana Cloud token: Fleet Management, OpenCost) and PROM_USERNAME (the stack's Prometheus user, for OpenCost)."
  }
}

# Without them Argo CD's PostSync smoke fails, and every sync shows Degraded.
check "smoke_has_its_sign_in" {
  assert {
    condition = !contains(keys(local.services), "smoke") || alltrue([for k in ["SUPABASE_ANON_KEY", "SMOKE_EMAIL", "SMOKE_PASSWORD"] :
    contains(local.private_keys["smoke"], k)])
    error_message = "service_secrets.smoke needs SUPABASE_ANON_KEY, SMOKE_EMAIL and SMOKE_PASSWORD (the dedicated smoke user, as for make smoke-prod): Argo CD's PostSync smoke cannot sign in without them."
  }
}

check "api_has_supabase_admin_key" {
  assert {
    condition     = !contains(keys(local.services), "api") || contains(local.private_keys["api"], "SUPABASE_SERVICE_ROLE_KEY")
    error_message = "service_secrets.api.SUPABASE_SERVICE_ROLE_KEY is not set: DELETE /v1/me will erase the data but cannot delete the Supabase login."
  }
}
