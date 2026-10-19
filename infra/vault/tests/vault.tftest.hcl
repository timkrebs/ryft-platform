# Unit tests: Vault, Kubernetes, AWS, local and random providers are mocked; nothing
# is created and no file is written.
#   terraform init -backend=false && terraform test
#
# The generated operator manifests are pinned in tests/golden/ (make k8s-validate
# checks those against the operator's CRD schemas).

variables {
  vault_parent_namespace_root = "admin"
  vault_address_for_cluster   = "https://vault.example.hashicorp.cloud:8200"
  supabase_project_ref        = "abcdefghijklmnopqrst"
  database                    = { host = "aws-0-eu-central-1.pooler.supabase.com" }
  database_owner_password     = "owner-password-123"
  service_secrets = {
    api = {
      SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test"
      APPLE_TEAM_ID             = "M77SKHKG63", APPLE_KEY_ID = "ABCDE12345", APPLE_CLIENT_ID = "dev.timkrebs.ryft"
      # The shape of a .p8, not a key: only the format is checked here.
      APPLE_KEY_P8 = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----"
    }
    monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
    notify     = { APNS_KEY_ID = "ABCDE12345", APNS_TEAM_ID = "M77SKHKG63", APNS_KEY_P8 = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----" }
  }
}

mock_provider "vault" {}
mock_provider "local" {}
mock_provider "kubernetes" {
  mock_resource "kubernetes_secret_v1" {
    defaults = { data = { token = "reviewer-jwt" } }
  }
}
mock_provider "aws" {
  mock_data "aws_eks_cluster" {
    defaults = {
      endpoint              = "https://mock.gr7.eu-central-1.eks.amazonaws.com"
      certificate_authority = [{ data = "bW9jaw==" }]
    }
  }
}
mock_provider "random" {
  mock_resource "random_password" {
    defaults = { result = "generatedpassword0123456789", bcrypt_hash = "$2a$10$mockbcrypthash" }
  }
  mock_resource "random_bytes" {
    defaults = { base64 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" }
  }
}

run "operator_manifests_use_the_full_namespace_path" {
  command = apply

  assert {
    condition     = local_file.vso["api"].content == file("tests/golden/api.vault.yaml")
    error_message = "api's VaultAuth/VaultStaticSecret differ from tests/golden/api.vault.yaml"
  }
  assert {
    condition     = local_file.vso["nats"].content == file("tests/golden/nats.vault.yaml")
    error_message = "nats must restart its StatefulSet on a new secret (tests/golden/nats.vault.yaml)"
  }
  assert {
    condition     = local_file.vso["migrate"].content == file("tests/golden/migrate.vault.yaml")
    error_message = "migrate is a Job: a secret but nothing to restart (tests/golden/migrate.vault.yaml)"
  }
  assert {
    condition     = output.login_check["api"] == "VAULT_NAMESPACE=admin/ryft/api vault write auth/kubernetes/login role=api jwt=\"$(kubectl -n ryft-api create token api --audience vault --duration 10m)\""
    error_message = "login_check must use the namespace from the root"
  }
}

run "each_namespace_holds_only_its_own_credentials" {
  command = apply

  assert {
    condition = jsonencode(output.secret_keys) == jsonencode({
      ingest     = ["DATABASE_URL", "NATS_PASSWORD", "NATS_USER", "SUPABASE_PROJECT_REF"]
      scoring    = ["DATABASE_URL", "NATS_PASSWORD", "NATS_USER"]
      api        = ["ADMIN_API_KEY", "APPLE_CLIENT_ID", "APPLE_KEY_ID", "APPLE_KEY_P8", "APPLE_TEAM_ID", "DATABASE_URL", "DATA_ENCRYPTION_KEY", "NATS_PASSWORD", "NATS_USER", "SUPABASE_PROJECT_REF", "SUPABASE_SERVICE_ROLE_KEY"]
      nats       = ["NATS_PASSWORD_API", "NATS_PASSWORD_INGEST", "NATS_PASSWORD_NOTIFY", "NATS_PASSWORD_SCORING"]
      migrate    = ["DATABASE_URL", "DB_PASSWORD_API", "DB_PASSWORD_INGEST", "DB_PASSWORD_NOTIFY", "DB_PASSWORD_SCORING"]
      monitoring = ["GC_TOKEN", "PROM_USERNAME"]
      notify     = ["APNS_KEY_ID", "APNS_KEY_P8", "APNS_TEAM_ID", "DATABASE_URL", "NATS_PASSWORD", "NATS_USER"]
    })
    error_message = "secret keys per namespace: ${jsonencode(output.secret_keys)}"
  }
  assert {
    condition     = startswith(nonsensitive(jsondecode(vault_kv_secret_v2.config["api"].data_json).DATABASE_URL), "postgresql://ryft_api.abcdefghijklmnopqrst:generatedpassword0123456789@aws-0-eu-central-1.pooler.supabase.com:5432/postgres?sslmode=require")
    error_message = "api connects as its own role through the session pooler"
  }
  assert {
    condition     = startswith(nonsensitive(jsondecode(vault_kv_secret_v2.config["migrate"].data_json).DATABASE_URL), "postgresql://postgres.abcdefghijklmnopqrst:owner-password-123@")
    error_message = "only migrate holds the owner"
  }
  assert {
    condition = alltrue([for s in ["ingest", "scoring", "api", "nats"] :
    !strcontains(nonsensitive(vault_kv_secret_v2.config[s].data_json), "owner-password-123")])
    error_message = "the owner password must not leave the migrate namespace"
  }
  assert {
    condition = alltrue([for k, v in nonsensitive(jsondecode(vault_kv_secret_v2.config["nats"].data_json)) :
    startswith(v, "$2a$")])
    error_message = "the NATS server holds bcrypt hashes only, never a usable password"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["scoring"].data_json).NATS_USER) == "scoring"
    error_message = "each client is its own NATS user"
  }
}

run "roles_trust_one_namespace_and_one_service_account" {
  command = apply

  assert {
    condition = alltrue([for k, r in vault_kubernetes_auth_backend_role.service :
      tolist(r.bound_service_account_namespaces) == tolist([k == "monitoring" ? "monitoring" : "ryft-${k}"]) && tolist(r.bound_service_account_names) == tolist([k]) &&
    r.audience == "vault" && r.role_name == k])
    error_message = "a role must be bound to exactly its own namespace and service account"
  }
  assert {
    condition     = jsonencode(output.auth_roles["monitoring"]) == jsonencode({ namespace = "admin/ryft/monitoring", mount = "kubernetes", role = "monitoring", sa = "monitoring" })
    error_message = "auth_roles: ${jsonencode(output.auth_roles["monitoring"])}"
  }
  assert {
    condition     = !strcontains(vault_policy.service["api"].policy, "*") && strcontains(vault_policy.service["api"].policy, "path \"kv/data/config\"")
    error_message = "the policy reads kv/config and nothing else"
  }
  assert {
    condition = alltrue([for k, n in kubernetes_namespace_v1.service :
    n.metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted" if k != "monitoring"])
    error_message = "every RYFT namespace enforces Pod Security restricted"
  }
  assert {
    condition = alltrue([for k, sa in kubernetes_service_account_v1.workload :
    sa.automount_service_account_token == false])
    error_message = "workload pods get no Kubernetes API token"
  }
}

run "self_managed_vault_has_no_admin_prefix" {
  command = plan

  variables {
    vault_parent_namespace_root = ""
  }
  assert {
    condition     = output.vault_namespaces["api"] == "ryft/api"
    error_message = "without a parent namespace the path starts at ryft/"
  }
}

run "rejects_the_direct_database_host" {
  command = plan

  variables {
    database = { host = "db.abcdefghijklmnopqrst.supabase.co" }
  }
  expect_failures = [var.database]
}

run "rejects_a_placeholder_project_ref" {
  command = plan

  variables {
    supabase_project_ref = "<ref>"
  }
  expect_failures = [var.supabase_project_ref]
}

run "rejects_placeholders_from_the_example" {
  command = plan

  variables {
    service_secrets = {
      api        = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test", APPLE_KEY_ID = "…", APPLE_KEY_P8 = "-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----" }
      monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "accepts_a_base64_encoded_apple_key" {
  command = plan

  variables {
    service_secrets = {
      api = {
        SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test"
        APPLE_TEAM_ID             = "M77SKHKG63", APPLE_KEY_ID = "ABCDE12345", APPLE_CLIENT_ID = "dev.timkrebs.ryft"
        APPLE_KEY_P8              = base64encode("-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----\n")
      }
      monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
      notify     = { APNS_KEY_ID = "ABCDE12345", APNS_TEAM_ID = "M77SKHKG63", APNS_KEY_P8 = base64encode("-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----") }
    }
  }
}

run "rejects_a_partial_set_of_apple_keys" {
  command = plan

  variables {
    service_secrets = {
      api        = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test", APPLE_TEAM_ID = "M77SKHKG63", APPLE_CLIENT_ID = "dev.timkrebs.ryft" }
      monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "warns_without_the_apple_keys" {
  command = apply

  variables {
    service_secrets = {
      api    = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test" }, monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
      notify = { APNS_KEY_ID = "ABCDE12345", APNS_TEAM_ID = "M77SKHKG63", APNS_KEY_P8 = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----" }
    }
  }
  expect_failures = [check.api_has_apple_keys]
}

run "rejects_a_partial_set_of_apns_keys" {
  command = plan

  variables {
    service_secrets = {
      api        = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test" }
      monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
      notify     = { APNS_KEY_ID = "ABCDE12345", APNS_TEAM_ID = "M77SKHKG63" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "warns_without_the_apns_key" {
  command = apply

  variables {
    service_secrets = {
      api = {
        SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test"
        APPLE_TEAM_ID             = "M77SKHKG63", APPLE_KEY_ID = "ABCDE12345", APPLE_CLIENT_ID = "dev.timkrebs.ryft"
        APPLE_KEY_P8              = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----"
      }
      monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
    }
  }
  expect_failures = [check.notify_has_apns_key]

  assert {
    condition     = jsonencode(output.secret_keys["notify"]) == jsonencode(["DATABASE_URL", "NATS_PASSWORD", "NATS_USER"])
    error_message = "without the key notify still gets its database and NATS credentials"
  }
}

run "the_data_key_reaches_only_the_api" {
  command = apply

  assert {
    condition = alltrue([for s in ["ingest", "scoring", "nats", "migrate"] :
    !contains(output.secret_keys[s], "DATA_ENCRYPTION_KEY")]) && !contains(output.secret_keys["monitoring"], "DATA_ENCRYPTION_KEY")
    error_message = "only the api may hold the data encryption key"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["api"].data_json).DATA_ENCRYPTION_KEY) == "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
    error_message = "the api gets the generated key (base64, 32 bytes)"
  }
}

run "monitoring_is_privileged_and_restarts_the_collector" {
  command = apply

  assert {
    condition = (
      kubernetes_namespace_v1.service["monitoring"].metadata[0].name == "monitoring" &&
      kubernetes_namespace_v1.service["monitoring"].metadata[0].labels["pod-security.kubernetes.io/enforce"] == "privileged" &&
      kubernetes_namespace_v1.service["api"].metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted"
    )
    error_message = "monitoring alone runs privileged, in namespace monitoring"
  }
  assert {
    condition = (
      strcontains(local_file.vso["monitoring"].content, "namespace: admin/ryft/monitoring") &&
      strcontains(local_file.vso["monitoring"].content,
      "- kind: DaemonSet\n      name: grafana-cloud-alloy-daemonset\n    - kind: Deployment\n      name: grafana-cloud-alloy-deployment\n")
    )
    error_message = "the operator logs in to admin/ryft/monitoring and restarts both collectors"
  }
}


run "the_admin_key_reaches_only_the_api" {
  command = apply

  assert {
    condition = alltrue([for s in ["ingest", "scoring", "nats", "migrate", "monitoring", "notify"] :
    !contains(output.secret_keys[s], "ADMIN_API_KEY")]) && contains(output.secret_keys["api"], "ADMIN_API_KEY")
    error_message = "only the api holds the admin key"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["api"].data_json).ADMIN_API_KEY) == "generatedpassword0123456789"
    error_message = "the api gets the generated admin key"
  }
}
