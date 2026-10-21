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
  # terraform test also reads a local terraform.tfvars: these runs assume none.
  shared_secrets = {}
  service_secrets = {
    api = {
      SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test"
      APPLE_TEAM_ID             = "M77SKHKG63", APPLE_KEY_ID = "ABCDE12345", APPLE_CLIENT_ID = "dev.timkrebs.ryft"
      # The shape of a .p8, not a key: only the format is checked here.
      APPLE_KEY_P8 = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----"
    }
    monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
    smoke      = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
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
      ingest     = ["AMQP_PASSWORD", "AMQP_USER", "DATABASE_URL", "SUPABASE_PROJECT_REF"]
      scoring    = ["AMQP_PASSWORD", "AMQP_USER", "DATABASE_URL"]
      api        = ["ADMIN_API_KEY", "AMQP_PASSWORD", "AMQP_USER", "APPLE_CLIENT_ID", "APPLE_KEY_ID", "APPLE_KEY_P8", "APPLE_TEAM_ID", "DATABASE_URL", "DATA_ENCRYPTION_KEY", "SUPABASE_PROJECT_REF", "SUPABASE_SERVICE_ROLE_KEY"]
      migrate    = ["DATABASE_URL", "DB_PASSWORD_API", "DB_PASSWORD_INGEST", "DB_PASSWORD_NOTIFY", "DB_PASSWORD_SCORING"]
      monitoring = ["GC_TOKEN", "PROM_USERNAME"]
      notify     = ["AMQP_PASSWORD", "AMQP_USER", "APNS_KEY_ID", "APNS_KEY_P8", "APNS_TEAM_ID", "DATABASE_URL"]
      rabbitmq   = []
      smoke      = ["SMOKE_EMAIL", "SMOKE_PASSWORD", "SUPABASE_ANON_KEY", "SUPABASE_PROJECT_REF"]
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
    condition = alltrue([for s in ["ingest", "scoring", "api", "notify"] :
    !strcontains(nonsensitive(vault_kv_secret_v2.config[s].data_json), "owner-password-123")])
    error_message = "the owner password must not leave the migrate namespace"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["scoring"].data_json).AMQP_USER) == "scoring"
    error_message = "each client is its own RabbitMQ user"
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
      smoke      = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
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
      smoke      = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
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
      smoke      = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "warns_without_the_apple_keys" {
  command = apply

  variables {
    service_secrets = {
      api    = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test" }, monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }, smoke = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
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
      smoke      = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
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
      smoke      = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
    }
  }
  expect_failures = [check.notify_has_apns_key]

  assert {
    condition     = jsonencode(output.secret_keys["notify"]) == jsonencode(["AMQP_PASSWORD", "AMQP_USER", "DATABASE_URL"])
    error_message = "without the key notify still gets its database and broker credentials"
  }
}

run "the_data_key_reaches_only_the_api" {
  command = apply

  assert {
    condition = alltrue([for s in ["ingest", "scoring", "notify", "migrate"] :
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
    condition = alltrue([for s in ["ingest", "scoring", "rabbitmq", "migrate", "monitoring", "notify"] :
    !contains(output.secret_keys[s], "ADMIN_API_KEY")]) && contains(output.secret_keys["api"], "ADMIN_API_KEY")
    error_message = "only the api holds the admin key"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["api"].data_json).ADMIN_API_KEY) == "generatedpassword0123456789"
    error_message = "the api gets the generated admin key"
  }
}

run "the_beta_gate_reaches_api_and_ingest" {
  command = apply

  variables {
    shared_secrets = { BETA_GATE = "on" }
  }
  assert {
    condition = contains(output.secret_keys["api"], "BETA_GATE") && contains(output.secret_keys["ingest"], "BETA_GATE") && alltrue([
    for s in ["scoring", "notify", "rabbitmq", "migrate", "monitoring"] : !contains(output.secret_keys[s], "BETA_GATE")])
    error_message = "BETA_GATE goes to the api and ingest only"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["ingest"].data_json).BETA_GATE) == "on"
    error_message = "ingest gets the value"
  }
}

run "the_demo_password_stays_with_the_operator" {
  command = apply

  assert {
    condition     = vault_kv_secret_v2.demo.namespace == vault_namespace.project.path_fq && vault_kv_secret_v2.demo.mount == "operator"
    error_message = "the demo password lives in the project namespace's operator mount"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.demo.data_json).email) == "review@ryftapp.de"
    error_message = "the demo account is review@ryftapp.de"
  }
  assert {
    condition     = alltrue([for s, v in vault_policy.service : !strcontains(v.policy, "operator")])
    error_message = "no service policy reaches the operator mount"
  }
}

run "the_consent_mode_reaches_api_ingest_and_scoring" {
  command = apply

  variables {
    shared_secrets = { BETA_GATE = "warn", CONSENT_ENFORCE = "warn" }
  }
  assert {
    condition = alltrue([for s in ["api", "ingest", "scoring"] : contains(output.secret_keys[s], "CONSENT_ENFORCE")]) && alltrue([
    for s in ["notify", "rabbitmq", "migrate", "monitoring"] : !contains(output.secret_keys[s], "CONSENT_ENFORCE")])
    error_message = "CONSENT_ENFORCE goes to api, ingest and scoring only"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.config["scoring"].data_json).CONSENT_ENFORCE) == "warn"
    error_message = "scoring gets the value"
  }
  assert {
    condition     = !contains(output.secret_keys["scoring"], "BETA_GATE")
    error_message = "scoring has no use for the beta gate"
  }
}

run "the_smoke_hook_gets_only_its_sign_in" {
  command = plan

  assert {
    condition     = local.services["smoke"].k8s_ns == "ryft-smoke" && length(local.services["smoke"].restart_targets) == 0
    error_message = "the smoke hook is a Job in ryft-smoke: nothing to restart when its secret changes"
  }
  assert {
    condition     = alltrue([for name, keys in local.service_secret_keys : name == "smoke" || !contains(keys, "SMOKE_PASSWORD")])
    error_message = "the smoke user's password must not reach any other namespace"
  }
}

run "rabbitmq_users_reach_the_broker_and_their_own_namespace_only" {
  command = apply

  assert {
    condition = alltrue([for c in ["ingest", "api", "scoring", "notify"] :
      nonsensitive(jsondecode(vault_kv_secret_v2.config[c].data_json).AMQP_USER) == c &&
    nonsensitive(jsondecode(vault_kv_secret_v2.amqp_user[c].data_json).username) == c])
    error_message = "each client is its own RabbitMQ user, and the broker's namespace has its credentials"
  }
  assert {
    condition = alltrue([for c in ["ingest", "api", "scoring", "notify"] :
      vault_kv_secret_v2.amqp_user[c].name == "amqp-user-${c}" &&
    vault_kv_secret_v2.amqp_user[c].namespace == vault_namespace.service["rabbitmq"].path_fq])
    error_message = "the users' credentials live in the broker's Vault namespace, one secret per user"
  }
  assert {
    condition     = !contains(keys(vault_kv_secret_v2.config), "rabbitmq")
    error_message = "the broker's namespace has no ryft-secrets: nothing of the database or the other services"
  }
  assert {
    condition     = alltrue([for name, s in local.service_secret_keys : contains(["ingest", "api", "scoring", "notify"], name) == contains(s, "AMQP_PASSWORD")])
    error_message = "AMQP_PASSWORD reaches the four clients only"
  }
  assert {
    condition     = local_file.vso["rabbitmq"].content == file("tests/golden/rabbitmq.vault.yaml")
    error_message = "the broker's operator manifests differ from tests/golden/rabbitmq.vault.yaml (one labelled VaultStaticSecret per user)"
  }
  assert {
    condition     = strcontains(vault_policy.service["rabbitmq"].policy, "kv/data/amqp-user-*") && !strcontains(vault_policy.service["api"].policy, "amqp-user")
    error_message = "only the broker's namespace may read the users' credentials"
  }
}
