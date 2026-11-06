# Unit tests: Vault, Kubernetes, AWS, local and random providers are mocked; nothing
# is created and no file is written.
#   terraform init -backend=false && terraform test
#
# The generated operator manifests are pinned in tests/golden/{hcp,k8s}/ (make k8s-validate
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
    "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
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
# The in-cluster Vault (providers.tf, k8s.tf). tests/k8s_offline.tftest.hcl runs the real provider instead.
mock_provider "vault" {
  alias = "k8s"
}
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
    condition     = local_file.vso["api"].content == file("tests/golden/hcp/api.vault.yaml")
    error_message = "api's VaultAuth/VaultStaticSecret differ from tests/golden/hcp/api.vault.yaml"
  }
  assert {
    condition     = local_file.vso["migrate"].content == file("tests/golden/hcp/migrate.vault.yaml")
    error_message = "migrate is a Job: a secret but nothing to restart (tests/golden/hcp/migrate.vault.yaml)"
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
      ingest         = ["AMQP_PASSWORD", "AMQP_USER", "DATABASE_URL", "SUPABASE_PROJECT_REF"]
      scoring        = ["AMQP_PASSWORD", "AMQP_USER", "DATABASE_URL"]
      api            = ["ADMIN_API_KEY", "AMQP_PASSWORD", "AMQP_USER", "APPLE_CLIENT_ID", "APPLE_KEY_ID", "APPLE_KEY_P8", "APPLE_TEAM_ID", "DATABASE_URL", "DATA_ENCRYPTION_KEY", "SUPABASE_PROJECT_REF", "SUPABASE_SERVICE_ROLE_KEY"]
      migrate        = ["DATABASE_URL", "DB_PASSWORD_API", "DB_PASSWORD_INGEST", "DB_PASSWORD_NOTIFY", "DB_PASSWORD_SCORING"]
      monitoring     = ["GC_TOKEN", "PROM_USERNAME"]
      notify         = ["AMQP_PASSWORD", "AMQP_USER", "APNS_KEY_ID", "APNS_KEY_P8", "APNS_TEAM_ID", "DATABASE_URL"]
      rabbitmq       = []
      smoke          = ["SMOKE_EMAIL", "SMOKE_PASSWORD", "SUPABASE_ANON_KEY", "SUPABASE_PROJECT_REF"]
      "whoop-import" = ["client_id", "client_secret", "refresh_token"]
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
      "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
      api            = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test", APPLE_KEY_ID = "…", APPLE_KEY_P8 = "-----BEGIN PRIVATE KEY-----\n…\n-----END PRIVATE KEY-----" }
      monitoring     = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
      smoke          = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "accepts_a_base64_encoded_apple_key" {
  command = plan

  variables {
    service_secrets = {
      "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
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
      "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
      api            = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test", APPLE_TEAM_ID = "M77SKHKG63", APPLE_CLIENT_ID = "dev.timkrebs.ryft" }
      monitoring     = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
      smoke          = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "warns_without_the_apple_keys" {
  command = apply

  variables {
    service_secrets = {
      "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
      api            = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test" }, monitoring = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }, smoke = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
      notify         = { APNS_KEY_ID = "ABCDE12345", APNS_TEAM_ID = "M77SKHKG63", APNS_KEY_P8 = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----" }
    }
  }
  expect_failures = [check.api_has_apple_keys]
}

run "rejects_a_partial_set_of_apns_keys" {
  command = plan

  variables {
    service_secrets = {
      "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
      api            = { SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test" }
      monitoring     = { GC_TOKEN = "glc_test", PROM_USERNAME = "123456" }
      smoke          = { SUPABASE_ANON_KEY = "sb_publishable_test", SMOKE_EMAIL = "smoke@example.com", SMOKE_PASSWORD = "smoke-password" }
      notify         = { APNS_KEY_ID = "ABCDE12345", APNS_TEAM_ID = "M77SKHKG63" }
    }
  }
  expect_failures = [var.service_secrets]
}

run "warns_without_the_apns_key" {
  command = apply

  variables {
    service_secrets = {
      "whoop-import" = { client_id = "test-client", client_secret = "test-secret", refresh_token = "test-refresh" }
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
    condition     = local_file.vso["rabbitmq"].content == file("tests/golden/hcp/rabbitmq.vault.yaml")
    error_message = "the broker's operator manifests differ from tests/golden/hcp/rabbitmq.vault.yaml (one labelled VaultStaticSecret per user)"
  }
  assert {
    condition     = strcontains(vault_policy.service["rabbitmq"].policy, "kv/data/amqp-user-*") && !strcontains(vault_policy.service["api"].policy, "amqp-user")
    error_message = "only the broker's namespace may read the users' credentials"
  }
}

# ---------------------------------------------------------------------------------------------------------------
# The in-cluster Vault (k8s.tf, targets.tf, design section 2.5). The committed switches are off; a test turns them
# on through test_override_targets, which check.targets_come_from_git reports (expected in every such run).

run "k8s_target_is_off_by_default" {
  command = plan

  assert {
    condition = (
      length(vault_auth_backend.k8s) + length(vault_kubernetes_auth_backend_config.k8s) + length(vault_mount.k8s_kv) +
      length(vault_policy.k8s_service) + length(vault_kubernetes_auth_backend_role.k8s_service) +
      length(vault_kv_secret_v2.k8s_config) + length(vault_kv_secret_v2.k8s_amqp_user) + length(vault_mount.k8s_operator) +
      length(vault_kv_secret_v2.k8s_demo) + length(vault_policy.k8s_snapshot) + length(vault_kubernetes_auth_backend_role.k8s_snapshot)
    ) == 0
    error_message = "with write_k8s false (the committed default) nothing is written to the in-cluster Vault"
  }
  assert {
    condition = alltrue([for s in ["api", "migrate", "rabbitmq"] :
    local_file.vso[s].content == file("tests/golden/hcp/${s}.vault.yaml")])
    error_message = "every namespace keeps the HCP shape, with the pre-staged Certificate vault-ca (tests/golden/hcp/)"
  }
  assert {
    condition = alltrue([for k, f in local_file.vso :
      strcontains(f.content, "kind: Certificate\nmetadata:\n  name: vault-ca\n  namespace: ${local.services[k].k8s_ns}\n") &&
      strcontains(f.content, "  commonName: vso.${local.services[k].k8s_ns}\n") &&
      !strcontains(f.content, "caCertSecretRef") && !strcontains(f.content, local.vault_k8s_address_for_cluster) &&
      !strcontains(f.content, "argocd.argoproj.io/sync-wave") && !strcontains(f.content, "kind: Secret")
    ])
    error_message = "every namespace, monitoring and the import service included, gets its Certificate vault-ca and nothing else new: no CA reference, no in-cluster address, no waves, no literal Secret"
  }
  # The Vault metrics scrape (#153, deploy/monitoring) reads the Secret vault-ca in namespace monitoring.
  assert {
    condition = length(local_file.vso) == length(var.services) && strcontains(local_file.vso["monitoring"].content,
    "kind: Certificate\nmetadata:\n  name: vault-ca\n  namespace: monitoring\n") && strcontains(local_file.vso["monitoring"].content, "  secretName: vault-ca\n")
    error_message = "one generated file per service, and monitoring's requests the Secret vault-ca in namespace monitoring (the Vault metrics scrape reads it)"
  }
  assert {
    condition     = output.vso_targets.write_k8s == false && alltrue([for k, t in output.vso_targets.services : t == "hcp"]) && length(output.vso_targets.services) == length(var.services)
    error_message = "the committed default: no CE writes, every namespace on HCP"
  }
}

run "each_service_gets_one_mount_one_role_one_policy" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = (
      toset(keys(vault_mount.k8s_kv)) == toset(keys(var.services)) &&
      toset(keys(vault_policy.k8s_service)) == toset(keys(var.services)) &&
      toset(keys(vault_kubernetes_auth_backend_role.k8s_service)) == toset(keys(var.services))
    )
    error_message = "exactly one mount, one policy and one role per service"
  }
  assert {
    condition = alltrue([for k, m in vault_mount.k8s_kv :
    m.path == "kv-${k}" && m.type == "kv" && m.options.version == "2"])
    error_message = "the mounts are kv-<svc>, KV version 2"
  }
  assert {
    condition = (
      length(vault_auth_backend.k8s) == 1 && vault_auth_backend.k8s[0].type == "kubernetes" && vault_auth_backend.k8s[0].path == "kubernetes" &&
      alltrue([for r in values(vault_kubernetes_auth_backend_role.k8s_service) : r.backend == "kubernetes"])
    )
    error_message = "one Kubernetes auth mount at kubernetes (the path P4 pins), which every role uses"
  }
  assert {
    condition     = length(vault_mount.k8s_operator) == 1 && vault_mount.k8s_operator[0].path == "kv-operator" && vault_kv_secret_v2.k8s_demo[0].mount == "kv-operator" && vault_kv_secret_v2.k8s_demo[0].name == "demo"
    error_message = "the demo password lives at kv-operator/demo"
  }
}

run "policies_read_only_their_own_config" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = alltrue([for k, p in vault_policy.k8s_service :
      p.name == k && can(regex("path \"kv-${k}/data/config\" \\{\\s*capabilities = \\[\"read\"\\]\\s*\\}", p.policy)) &&
      alltrue([for m in regexall("kv-[a-z0-9-]+/", p.policy) : m == "kv-${k}/"]) &&
      length(regexall("path \"", p.policy)) == (k == "rabbitmq" ? 2 : 1)
    ])
    error_message = "a policy reads kv-<its service>/data/config only (the broker also its users), and names no other mount"
  }
  assert {
    condition = alltrue([for k, p in vault_policy.k8s_service :
      strcontains(p.policy, "amqp-user-*") == (k == "rabbitmq") && !strcontains(p.policy, "metadata") &&
    !strcontains(p.policy, "kv-operator") && !strcontains(p.policy, "sys/")])
    error_message = "only the broker reads amqp-user-*; no policy reads metadata (VSO needs none, design 13 row 5), kv-operator or sys/"
  }
  assert {
    condition     = can(regex("path \"kv-rabbitmq/data/amqp-user-\\*\" \\{\\s*capabilities = \\[\"read\"\\]\\s*\\}", vault_policy.k8s_service["rabbitmq"].policy))
    error_message = "the broker reads its users' credentials (the Topology Operator)"
  }
}

run "roles_trust_one_namespace_and_one_service_account_on_k8s" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = alltrue([for k, r in vault_kubernetes_auth_backend_role.k8s_service :
      r.role_name == k && tolist(r.bound_service_account_names) == tolist([local.services[k].service_account]) &&
      tolist(r.bound_service_account_namespaces) == tolist([local.services[k].k8s_ns]) &&
      r.audience == "vault" && tolist(r.token_policies) == tolist([k]) &&
      r.token_ttl == 3600 && r.token_max_ttl == 14400
    ])
    error_message = "a role is bound to exactly its namespace and service account, audience vault, its own policy, the HCP TTLs"
  }
  assert {
    condition = alltrue([for k, r in vault_kubernetes_auth_backend_role.k8s_service :
      tolist(r.bound_service_account_names) == tolist(vault_kubernetes_auth_backend_role.service[k].bound_service_account_names) &&
      tolist(r.bound_service_account_namespaces) == tolist(vault_kubernetes_auth_backend_role.service[k].bound_service_account_namespaces) &&
    r.token_ttl == vault_kubernetes_auth_backend_role.service[k].token_ttl && r.token_max_ttl == vault_kubernetes_auth_backend_role.service[k].token_max_ttl])
    error_message = "each in-cluster role trusts exactly what its HCP role trusts"
  }
  assert {
    condition     = tolist(vault_kubernetes_auth_backend_role.k8s_service["monitoring"].bound_service_account_namespaces) == tolist(["monitoring"])
    error_message = "the collector's role is bound to namespace monitoring (no ryft- prefix)"
  }
}

# The snapshot CronJob (charts/vault-extras, PR 4) logs in as role vault-snapshot from vault/vault-snapshot with a
# 10-minute projected token for audience vault, and may save a snapshot, nothing else (restore is break-glass).
run "the_snapshot_job_saves_a_snapshot_and_nothing_else" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition     = length(vault_policy.k8s_snapshot) == 1 && vault_policy.k8s_snapshot[0].name == "snapshot" && vault_policy.k8s_snapshot[0].policy == "path \"sys/storage/raft/snapshot\" {\n  capabilities = [\"read\"]\n}\n"
    error_message = "policy snapshot is read on sys/storage/raft/snapshot and nothing else (no update: that is restore; no sudo, no other path)"
  }
  assert {
    condition = (
      length(vault_kubernetes_auth_backend_role.k8s_snapshot) == 1 &&
      vault_kubernetes_auth_backend_role.k8s_snapshot[0].backend == "kubernetes" &&
      vault_kubernetes_auth_backend_role.k8s_snapshot[0].role_name == "vault-snapshot" &&
      tolist(vault_kubernetes_auth_backend_role.k8s_snapshot[0].bound_service_account_names) == tolist(["vault-snapshot"]) &&
      tolist(vault_kubernetes_auth_backend_role.k8s_snapshot[0].bound_service_account_namespaces) == tolist(["vault"]) &&
      vault_kubernetes_auth_backend_role.k8s_snapshot[0].audience == "vault" &&
      tolist(vault_kubernetes_auth_backend_role.k8s_snapshot[0].token_policies) == tolist(["snapshot"]) &&
      vault_kubernetes_auth_backend_role.k8s_snapshot[0].token_ttl == 600 && vault_kubernetes_auth_backend_role.k8s_snapshot[0].token_max_ttl == 600
    )
    error_message = "role vault-snapshot is bound to vault/vault-snapshot, audience vault, policy snapshot only, tokens of 10 minutes at most"
  }
  assert {
    condition = toset(concat([for p in values(vault_policy.k8s_service) : p.name], [for p in vault_policy.k8s_snapshot : p.name])) == toset(concat(keys(var.services), ["snapshot"])) && toset(concat(
      [for r in values(vault_kubernetes_auth_backend_role.k8s_service) : r.role_name], [for r in vault_kubernetes_auth_backend_role.k8s_snapshot : r.role_name]
    )) == toset(concat(keys(var.services), ["vault-snapshot"]))
    error_message = "the in-cluster Vault gets exactly one policy per service plus snapshot, and one role per service plus vault-snapshot"
  }
  # What this root writes is exactly what its token may write: the policy and role paths of bootstrap/terraform.hcl.
  assert {
    condition = (
      toset([for p in local.bootstrap_policy_paths.terraform : trimprefix(p, "sys/policies/acl/") if startswith(p, "sys/policies/acl/")]) ==
      toset(concat([for p in values(vault_policy.k8s_service) : p.name], [for p in vault_policy.k8s_snapshot : p.name])) &&
      toset([for p in local.bootstrap_policy_paths.terraform : trimprefix(p, "auth/kubernetes/role/") if startswith(p, "auth/kubernetes/role/")]) ==
      toset(concat([for r in values(vault_kubernetes_auth_backend_role.k8s_service) : r.role_name], [for r in vault_kubernetes_auth_backend_role.k8s_snapshot : r.role_name]))
    )
    error_message = "the terraform policy names exactly the policies and roles k8s.tf writes: a missing one is a 403 at the apply, an extra one a right nobody uses"
  }
}

# One random value, two writes: the CE secrets are built from the same locals and random_* as HCP's, so the
# migration is a copy. Marked values per credential (overrides, in a state of its own so the random_* are
# created here), so a write that took another random_* (or a fresh one) would differ.
run "same_values_on_both_targets" {
  command   = apply
  state_key = "same_values"

  variables {
    test_override_targets   = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token         = "terraform-token"
    database_owner_password = "owner-password-for-both"
    shared_secrets          = { BETA_GATE = "warn", CONSENT_ENFORCE = "on" }
  }
  expect_failures = [check.targets_come_from_git]

  override_resource {
    target = random_password.amqp
    values = { result = "amqp-password-from-the-one-random" }
  }
  override_resource {
    target = random_password.demo
    values = { result = "demo-password-from-the-one-random" }
  }

  assert {
    condition = (
      toset(keys(vault_kv_secret_v2.k8s_config)) == toset(keys(vault_kv_secret_v2.config)) &&
      alltrue([for k, s in vault_kv_secret_v2.k8s_config : s.data_json == vault_kv_secret_v2.config[k].data_json])
    )
    error_message = "every service's kv-<svc>/config holds exactly what its HCP kv/config holds"
  }
  assert {
    condition = (
      toset(keys(vault_kv_secret_v2.k8s_amqp_user)) == toset(keys(vault_kv_secret_v2.amqp_user)) &&
      alltrue([for k, s in vault_kv_secret_v2.k8s_amqp_user : s.data_json == vault_kv_secret_v2.amqp_user[k].data_json && s.name == "amqp-user-${k}" && s.mount == "kv-rabbitmq"]) &&
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_amqp_user["api"].data_json).password) == "amqp-password-from-the-one-random"
    )
    error_message = "the broker's amqp-user-<c> are the same on both targets, from the one random_password.amqp"
  }
  assert {
    condition = (
      vault_kv_secret_v2.k8s_demo[0].data_json == vault_kv_secret_v2.demo.data_json &&
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_demo[0].data_json).password) == "demo-password-from-the-one-random"
    )
    error_message = "the demo password is the same on both targets"
  }
  assert {
    condition = (
      strcontains(nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["migrate"].data_json).DATABASE_URL), ":owner-password-for-both@") &&
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["api"].data_json).AMQP_PASSWORD) == "amqp-password-from-the-one-random"
    )
    error_message = "the values reach the in-cluster Vault unchanged (the owner URL, the broker password)"
  }
  assert {
    condition     = length(random_password.amqp) == length(local.amqp_clients) && length(random_password.db) == length(local.db_roles)
    error_message = "one random_password per credential: the in-cluster Vault adds none"
  }
  assert {
    condition     = !contains(keys(vault_kv_secret_v2.k8s_config), "rabbitmq")
    error_message = "the broker's mount has no config on the in-cluster Vault either"
  }
}

run "shared_secrets_reach_only_the_services_that_list_them" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token       = "terraform-token"
    shared_secrets        = { BETA_GATE = "on", CONSENT_ENFORCE = "on" }
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = alltrue([for k, s in vault_kv_secret_v2.k8s_config :
    contains(keys(jsondecode(nonsensitive(s.data_json))), "BETA_GATE") == contains(["api", "ingest"], k)])
    error_message = "BETA_GATE reaches kv-api and kv-ingest only (design 13 row 6: filtered by secret_keys, not every mount)"
  }
  assert {
    condition = alltrue([for k, s in vault_kv_secret_v2.k8s_config :
    contains(keys(jsondecode(nonsensitive(s.data_json))), "CONSENT_ENFORCE") == contains(["api", "ingest", "scoring"], k)])
    error_message = "CONSENT_ENFORCE reaches kv-api, kv-ingest and kv-scoring only"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["scoring"].data_json).CONSENT_ENFORCE) == "on"
    error_message = "scoring gets the value"
  }
}

run "auth_config_is_in_cluster" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = (
      vault_kubernetes_auth_backend_config.k8s[0].backend == "kubernetes" &&
      vault_kubernetes_auth_backend_config.k8s[0].kubernetes_host == "https://kubernetes.default.svc" &&
      vault_kubernetes_auth_backend_config.k8s[0].disable_local_ca_jwt == false &&
      vault_kubernetes_auth_backend_config.k8s[0].token_reviewer_jwt == null &&
      vault_kubernetes_auth_backend_config.k8s[0].pem_keys == null
    )
    error_message = "Kubernetes auth uses the in-cluster API and Vault's own pod token and CA: no reviewer JWT, no keys"
  }
  assert {
    # kubernetes_ca_cert is computed (a mock would invent one), so the configuration is read instead.
    condition = length(vault_kubernetes_auth_backend_config.k8s) == 1 && length(regexall("(?s)resource \"vault_kubernetes_auth_backend_config\" \"k8s\" \\{.*?\n\\}", file("k8s.tf"))) == 1 && alltrue([
      for b in regexall("(?s)resource \"vault_kubernetes_auth_backend_config\" \"k8s\" \\{.*?\n\\}", file("k8s.tf")) :
    !strcontains(b, "kubernetes_ca_cert") && !strcontains(b, "token_reviewer_jwt") && !strcontains(b, "issuer")])
    error_message = "the in-cluster auth config sets no CA, reviewer JWT or issuer"
  }
}

run "golden_k8s" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = ["api", "migrate", "rabbitmq"] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = alltrue([for s in ["api", "migrate", "rabbitmq"] :
    local_file.vso[s].content == file("tests/golden/k8s/${s}.vault.yaml")])
    error_message = "the in-cluster shape differs from tests/golden/k8s/"
  }
  assert {
    condition = alltrue([for k, f in local_file.vso :
      strcontains(f.content, "address: ${var.vault_address_for_cluster}\n") && strcontains(f.content, "namespace: admin/ryft/${k}\n")
    if !contains(["api", "migrate", "rabbitmq"], k)])
    error_message = "a namespace not in vso_on_k8s keeps reading HCP"
  }
  assert {
    condition     = output.vso_targets.services["api"] == "k8s" && output.vso_targets.services["ingest"] == "hcp"
    error_message = "the output says which namespace reads which Vault"
  }
}

# V3: the generated objects for the in-cluster Vault differ from the HCP ones in the VaultConnection's address
# and caCertSecretRef, the VaultAuth's Vault namespace and the VaultStaticSecrets' mount, plus the in-app sync
# waves (design 1.3); nothing else (destination, excludeRaw, labels, restart targets, role, audience, Certificate).
run "overlay_differs_only_in_mount_address_ca_and_namespace" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = ["api", "migrate", "rabbitmq", "monitoring"] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [check.targets_come_from_git]

  assert {
    condition = alltrue([for s in ["api", "migrate", "rabbitmq"] :
      jsonencode([for d in split("\n---\n", local_file.vso[s].content) : {
        apiVersion = yamldecode(d).apiVersion
        kind       = yamldecode(d).kind
        metadata   = { for mk, mv in yamldecode(d).metadata : mk => mv if mk != "annotations" }
        spec       = { for sk, sv in yamldecode(d).spec : sk => sv if !contains(yamldecode(d).kind == "VaultStaticSecret" ? ["mount"] : ["address", "caCertSecretRef", "namespace"], sk) }
      }]) ==
      jsonencode([for d in split("\n---\n", file("tests/golden/hcp/${s}.vault.yaml")) : {
        apiVersion = yamldecode(d).apiVersion
        kind       = yamldecode(d).kind
        metadata   = { for mk, mv in yamldecode(d).metadata : mk => mv if mk != "annotations" }
        spec       = { for sk, sv in yamldecode(d).spec : sk => sv if !contains(yamldecode(d).kind == "VaultStaticSecret" ? ["mount"] : ["address", "caCertSecretRef", "namespace"], sk) }
      }])
    ])
    error_message = "the two shapes differ in more than address, caCertSecretRef, the Vault namespace, the mount and the waves"
  }
  assert {
    condition = alltrue([for s in ["api", "migrate", "rabbitmq", "monitoring"] : alltrue([for d in split("\n---\n", local_file.vso[s].content) :
      yamldecode(d).kind == "Certificate" ? !can(yamldecode(d).metadata.annotations) : (
        yamldecode(d).metadata.annotations == { "argocd.argoproj.io/sync-wave" = (yamldecode(d).kind == "VaultStaticSecret" ? "1" : "0") } &&
        (yamldecode(d).kind != "VaultConnection" || (yamldecode(d).spec.address == "https://vault.vault.svc:8200" && yamldecode(d).spec.caCertSecretRef == "vault-ca")) &&
        (yamldecode(d).kind != "VaultAuth" || !can(yamldecode(d).spec.namespace)) &&
        (yamldecode(d).kind != "VaultStaticSecret" || yamldecode(d).spec.mount == "kv-${s}")
    )])])
    error_message = "in-cluster shape: connection and auth in wave 0, the secrets in wave 1, the in-cluster address with vault-ca, no Vault namespace, the mount kv-<svc>"
  }
  assert {
    condition = (
      strcontains(local_file.vso["monitoring"].content, "  mount: kv-monitoring\n") &&
      strcontains(local_file.vso["monitoring"].content, "- kind: DaemonSet\n      name: grafana-cloud-alloy-daemonset\n    - kind: Deployment\n      name: grafana-cloud-alloy-deployment\n")
    )
    error_message = "monitoring (no ryft- prefix) reads kv-monitoring and still restarts both collectors"
  }
}

run "k8s_source_needs_write_k8s" {
  command = plan

  variables {
    test_override_targets = { write_k8s = false, vso_on_k8s = ["api"] }
  }
  expect_failures = [local_file.vso, check.targets_come_from_git]
}

run "k8s_source_must_be_a_service" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = ["api", "no-such-service"] }
    vault_k8s_token       = "terraform-token"
  }
  expect_failures = [local_file.vso, check.targets_come_from_git]
}

run "write_k8s_needs_the_token" {
  command = plan

  variables {
    test_override_targets = { write_k8s = true, vso_on_k8s = [] }
  }
  expect_failures = [var.vault_k8s_token, check.targets_come_from_git]
}

run "reserved_service_names_are_refused" {
  command = plan

  variables {
    services = {
      api      = { database = "role", amqp = "client" }
      operator = { workload_kind = "" }
    }
  }
  expect_failures = [var.services]
}

# The snapshot job's policy is snapshot: a service of that name would share it.
run "the_snapshot_policy_name_is_reserved" {
  command = plan

  variables {
    services = {
      api      = { database = "role", amqp = "client" }
      snapshot = { workload_kind = "" }
    }
  }
  expect_failures = [var.services]
}

run "the_port_forward_is_the_only_address" {
  command = plan

  variables {
    vault_k8s_address = "https://vault.example.hashicorp.cloud:8200"
  }
  expect_failures = [var.vault_k8s_address]
}

run "the_literal_ca_secret_is_retired" {
  command = plan

  variables {
    vault_ca_bundle_b64 = "LS0tLS1CRUdJTi0tLS0t"
  }
  expect_failures = [var.vault_ca_bundle_b64]
}

# Service names are canonical already: Vault lower-cases and trims policy names on write but matches ACL paths as
# written, so `Api` would be a different path in bootstrap/terraform.hcl than the policy it writes.
run "service_names_are_canonical" {
  command = plan

  variables {
    services = {
      api = { database = "role", amqp = "client" }
      Api = { workload_kind = "" }
    }
  }
  expect_failures = [var.services]
}

# Every resource in k8s.tf goes to the in-cluster Vault, and nothing else does: a dropped `provider = vault.k8s`
# would write that object (the demo password, a service's KV) into HCP's admin namespace instead.
run "the_k8s_resources_and_only_they_use_the_k8s_provider" {
  command = plan

  assert {
    condition = length(local.k8s_kv_mount) > 0 && alltrue([
      for b in regexall("(?ms)^resource \"([a-z0-9_]+)\" \"([a-z0-9_]+)\" \\{\\n(.*?)^\\}$", file("k8s.tf")) :
      startswith(b[0], "vault_") && can(regex("(?m)^  provider += vault\\.k8s$", b[2]))
    ]) && length(regexall("(?m)^resource ", file("k8s.tf"))) == 11
    error_message = "every resource block in k8s.tf (11) is a vault_ resource with provider = vault.k8s"
  }
  assert {
    condition = length(local.k8s_kv_mount) > 0 && alltrue([
      for f in fileset(".", "*.tf") : f == "k8s.tf" || length(regexall("provider\\s*=\\s*vault\\.k8s", file(f))) == 0
    ])
    error_message = "no file but k8s.tf points a resource at vault.k8s"
  }
}

# No mount or auth backend this root writes on the in-cluster Vault switches audit HMAC off or exposes headers or
# the mount: the policy terraform refuses these keys on tune, but not inside a mount's creation (its header).
run "no_k8s_mount_switches_audit_hmac_off" {
  command = plan

  assert {
    condition = length(local.k8s_kv_mount) > 0 && length(regexall(
      "audit_non_hmac|passthrough_request_headers|allowed_response_headers|listing_visibility|(?m)^\\s*tune\\s*\\{",
      join("\n", [for l in split("\n", file("k8s.tf")) : l if !startswith(trimspace(l), "#")])
    )) == 0
    error_message = "k8s.tf sets audit_non_hmac_*, passthrough_request_headers, allowed_response_headers, listing_visibility or a tune block: secret values would reach both audit devices in clear"
  }
}

# The bootstrap's files (docs/OPERATIONS.md -> Vault -> Bootstrap) are written into Vault by hand, not by this root,
# so they are pinned here, as an allowlist: every path the policies name, wherever it stands, with exactly its
# capabilities and parameter limits (these runs use the default services, the ones the committed render is for).
run "the_terraform_policy_is_an_exact_allowlist" {
  command = plan

  assert {
    condition     = local.bootstrap.terraform_policy == local.bootstrap_terraform_policy_rendered && output.bootstrap_terraform_policy == local.bootstrap.terraform_policy
    error_message = "bootstrap/terraform.hcl is not the render of bootstrap/terraform.hcl.tftpl for the default services (terraform output -raw bootstrap_terraform_policy)"
  }
  assert {
    condition = (
      length(local.bootstrap_policy_paths.terraform) == length(keys(local.bootstrap_policy_blocks.terraform)) &&
      length(local.bootstrap_policy_paths.terraform) == length(toset(local.bootstrap_policy_paths.terraform))
    )
    error_message = "every `path` in the terraform policy opens its own block at the start of a line, once (no indented or nested path, no duplicate)"
  }
  assert {
    condition = jsonencode({ for p, body in local.bootstrap_policy_blocks.terraform :
      p => try(regex("^  capabilities = \\[([^\\]]*)\\]\\n", body)[0], "MISSING")
      }) == jsonencode(merge(
      {
        "sys/mounts"                      = "\"read\""
        "sys/auth"                        = "\"read\""
        "sys/auth/kubernetes"             = "\"create\", \"read\", \"update\", \"delete\", \"sudo\""
        "sys/mounts/auth/kubernetes"      = "\"read\""
        "sys/mounts/auth/kubernetes/tune" = "\"read\", \"update\""
        "auth/kubernetes/config"          = "\"create\", \"read\", \"update\""
        "auth/kubernetes/role"            = "\"list\""
        "sys/policies/acl"                = "\"list\""
      },
      merge([for m in concat([for s in keys(var.services) : "kv-${s}"], ["kv-operator"]) : {
        "sys/mounts/${m}"      = "\"create\", \"read\", \"update\", \"delete\", \"sudo\""
        "sys/mounts/${m}/tune" = "\"read\", \"update\""
        "${m}/data/*"          = "\"create\", \"read\", \"update\", \"delete\""
        "${m}/metadata/*"      = "\"read\", \"delete\", \"list\""
      }]...),
      merge([for s in keys(var.services) : {
        "auth/kubernetes/role/${s}" = "\"create\", \"read\", \"update\", \"delete\""
        "sys/policies/acl/${s}"     = "\"create\", \"read\", \"update\", \"delete\""
      }]...),
      {
        "auth/kubernetes/role/vault-snapshot" = "\"create\", \"read\", \"update\", \"delete\""
        "sys/policies/acl/snapshot"           = "\"create\", \"read\", \"update\", \"delete\""
      }
    ))
    error_message = "the terraform policy names exactly the paths it needs, each with exactly its capabilities: one mount, tune, data and metadata path per kv-<svc> and kv-operator, one role and one policy per service plus the snapshot job's (vault-snapshot, snapshot), the one auth mount; no glob on a policy, role or mount, nothing else"
  }
  assert {
    condition = alltrue([for p, body in local.bootstrap_policy_blocks.terraform :
      endswith(p, "/tune") ? (
        length(compact(split("\n", body))) == 8 && strcontains(body, "\n  denied_parameters = {\n") &&
        alltrue([for k in ["audit_non_hmac_request_keys", "audit_non_hmac_response_keys", "passthrough_request_headers", "allowed_response_headers", "listing_visibility"] :
        can(regex("\\n    ${k} *= \\[\\]\\n", body))])
        ) : p == "sys/auth/kubernetes" ? (
        length(compact(split("\n", body))) == 5 && can(regex("\\n  allowed_parameters = \\{\\n    type = \\[\"kubernetes\"\\]\\n    \"\\*\" += \\[\\]\\n  \\}\\n", body))
        ) : p == "auth/kubernetes/config" ? (
        length(compact(split("\n", body))) == 5 && can(regex("\\n  allowed_parameters = \\{\\n    kubernetes_host = \\[\"https://kubernetes\\.default\\.svc\"\\]\\n    \"\\*\" += \\[\\]\\n  \\}\\n", body))
        ) : startswith(p, "auth/kubernetes/role/") ? (
        # [policy, service account, namespace]: the snapshot job's literally, a service's from its locals.
        length(compact(split("\n", body))) == 9 && alltrue([for kv in [
          ["token_policies", p == "auth/kubernetes/role/vault-snapshot" ? "snapshot" : trimprefix(p, "auth/kubernetes/role/")],
          ["policies", p == "auth/kubernetes/role/vault-snapshot" ? "snapshot" : trimprefix(p, "auth/kubernetes/role/")],
          ["bound_service_account_names", p == "auth/kubernetes/role/vault-snapshot" ? "vault-snapshot" : local.services[trimprefix(p, "auth/kubernetes/role/")].service_account],
          ["bound_service_account_namespaces", p == "auth/kubernetes/role/vault-snapshot" ? "vault" : local.services[trimprefix(p, "auth/kubernetes/role/")].k8s_ns],
          ["bound_service_account_namespace_selector", ""],
        ] : can(regex("\\n    ${kv[0]} *= \\[\"${kv[1]}\"\\]\\n", body))])
      ) : length(compact(split("\n", body))) == 1
    ])
    error_message = "parameter limits: every tune refuses the audit HMAC, header and listing keys; the auth mount stays type kubernetes; its config keeps the in-cluster API server; a role carries only its own policy, namespace and service account; every other path has capabilities and nothing else"
  }
}

run "the_admin_policy_and_the_login_hold_the_reviewed_shape" {
  command = plan

  assert {
    condition = (
      local.bootstrap_policy_paths.admin == ["*"] &&
      local.bootstrap_policy_blocks.admin == { "*" = "  capabilities = [\"create\", \"read\", \"update\", \"patch\", \"delete\", \"list\", \"sudo\"]\n" }
    )
    error_message = "admin is one path, \"*\", with every capability (the root token is revoked)"
  }
  assert {
    condition = alltrue([for r in [local.bootstrap.oidc_role_admin, local.bootstrap.oidc_role_enroll] : (
      r.role_type == "oidc" && r.user_claim == "sub" && r.verbose_oidc_logging == false &&
      r.bound_claims == { hd = "ryftapp.de" } && r.bound_claims_type == "string" && length(r.oidc_scopes) == 0 &&
      tolist(r.allowed_redirect_uris) == tolist(["http://localhost:8250/oidc/callback"]) &&
      r.max_age == 600 && tolist(r.token_bound_cidrs) == tolist(["127.0.0.1/32"]) && r.token_type == "service" &&
      length(setintersection(keys(r), ["claim_mappings", "groups_claim", "bound_subject", "user_claim_json_pointer", "bound_audiences"])) == 0
    )])
    error_message = "the OIDC roles: user_claim sub, the Workspace domain bound, no claim mappings or groups, no extra scopes, no verbose logging, the CLI callback only, a fresh Google sign-in (max_age 10 min), tokens usable through the port-forward only, and the subject not in git (it is passed at the bootstrap)"
  }
  assert {
    condition = (
      local.bootstrap.oidc_role_admin.token_policies == ["admin"] && local.bootstrap.oidc_role_admin.token_ttl == 3600 &&
      local.bootstrap.oidc_role_admin.token_max_ttl == 7200 &&
      local.bootstrap.oidc_role_enroll.token_policies == ["default"] && local.bootstrap.oidc_role_enroll.token_max_ttl == 300
    )
    error_message = "admin: policy admin, 1 hour (2 at most); enroll: the default policy only, 5 minutes"
  }
  assert {
    condition = (
      local.bootstrap.oidc_config.oidc_discovery_url == "https://accounts.google.com" && local.bootstrap.oidc_config.default_role == "admin" &&
      length(setintersection(keys(local.bootstrap.oidc_config), ["oidc_client_secret", "oidc_client_id", "provider_config"])) == 0
    )
    error_message = "the OIDC config names Google and the admin role; the client id and secret are passed at the bootstrap, never committed"
  }
  assert {
    condition = (
      local.bootstrap.token_role_terraform.allowed_policies == ["terraform"] &&
      local.bootstrap.token_role_terraform.renewable == false && local.bootstrap.token_role_terraform.orphan == false &&
      local.bootstrap.token_role_terraform.token_explicit_max_ttl == 3600 &&
      tolist(local.bootstrap.token_role_terraform.token_bound_cidrs) == tolist(["127.0.0.1/32"]) &&
      length(setintersection(keys(local.bootstrap.token_role_terraform), ["allowed_policies_glob", "token_period", "token_num_uses", "path_suffix", "allowed_entity_aliases"])) == 0
    )
    error_message = "the token role terraform: the policy terraform only, one hour, not renewable, a child of the admin login (not an orphan), through the port-forward only"
  }
}
