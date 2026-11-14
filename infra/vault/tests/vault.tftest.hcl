# Unit tests: Vault, Kubernetes, AWS, local and random providers are mocked; nothing
# is created and no file is written (files are only read: the goldens and the bootstrap files).
#   terraform init -backend=false && terraform test
#
# The generated operator manifests are pinned in tests/golden/k8s/ (make k8s-validate checks those against the
# operator's CRD schemas). No run reads the committed deploy/k8s/overlays/vault-namespaces/: they stay HCP's render
# until the user's first apply of this root rewrites them, and the follow-up PR that commits the rewritten files
# adds the run that holds them to the generator (docs/work/2026-11-13-rebuild/followup-overlays.md). service_secrets
# below holds every key the checks of secrets.tf ask for, as production's terraform.tfvars must: the render here is
# then the one production's apply writes.

variables {
  supabase_project_ref    = "abcdefghijklmnopqrst"
  database                = { host = "aws-0-eu-central-1.pooler.supabase.com" }
  database_owner_password = "owner-password-123"
  vault_k8s_token         = "terraform-token"
  # Never the committed overlays: local is mocked, so no run writes a file, and if a run ever used the real provider,
  # an apply and its teardown would write and delete files under tests/ (git-ignored), not the generated overlays.
  vso_output_dir = "tests/.vso-render"
  # terraform test also reads a local terraform.tfvars: these runs assume none (one that still sets a retired HCP
  # variable fails every run, as it fails the plan).
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
mock_provider "local" {}
mock_provider "kubernetes" {}
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

run "operator_manifests_match_the_goldens" {
  command = plan

  assert {
    condition     = local_file.vso["api"].content == file("${path.module}/tests/golden/k8s/api.vault.yaml")
    error_message = "api's operator objects differ from tests/golden/k8s/api.vault.yaml (a Deployment to restart)"
  }
  assert {
    condition     = local_file.vso["migrate"].content == file("${path.module}/tests/golden/k8s/migrate.vault.yaml")
    error_message = "migrate is a Job: a secret but nothing to restart (tests/golden/k8s/migrate.vault.yaml)"
  }
  assert {
    condition     = output.login_check["api"] == "vault write -field=token_policies auth/kubernetes/login role=api jwt=\"$(kubectl --context ryft-prod-euc1 -n ryft-api create token api --audience vault --duration 10m)\""
    error_message = "login_check: the in-cluster role, no Vault namespace, the named context, only the token's policies printed"
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
    condition     = startswith(nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["api"].data_json).DATABASE_URL), "postgresql://ryft_api.abcdefghijklmnopqrst:generatedpassword0123456789@aws-0-eu-central-1.pooler.supabase.com:5432/postgres?sslmode=require")
    error_message = "api connects as its own role through the session pooler"
  }
  assert {
    condition     = startswith(nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["migrate"].data_json).DATABASE_URL), "postgresql://postgres.abcdefghijklmnopqrst:owner-password-123@")
    error_message = "only migrate holds the owner"
  }
  assert {
    condition = alltrue([for s in ["ingest", "scoring", "api", "notify"] :
    !strcontains(nonsensitive(vault_kv_secret_v2.k8s_config[s].data_json), "owner-password-123")])
    error_message = "the owner password must not leave the migrate namespace"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["scoring"].data_json).AMQP_USER) == "scoring"
    error_message = "each client is its own RabbitMQ user"
  }
  assert {
    condition = alltrue([for k, s in vault_kv_secret_v2.k8s_config :
    s.mount == "kv-${k}" && s.name == "config" && s.delete_all_versions == true])
    error_message = "every service's secrets are kv-<svc>/config (a destroy removes every version)"
  }
}

run "roles_trust_one_namespace_and_one_service_account" {
  command = apply

  assert {
    condition = alltrue([for k, r in vault_kubernetes_auth_backend_role.k8s_service :
      r.backend == "kubernetes" && r.role_name == k &&
      tolist(r.bound_service_account_names) == tolist([local.services[k].service_account]) &&
      tolist(r.bound_service_account_namespaces) == tolist([k == "monitoring" ? "monitoring" : "ryft-${k}"]) &&
      r.audience == "vault" && tolist(r.token_policies) == tolist([k]) &&
      r.token_ttl == 3600 && r.token_max_ttl == 14400
    ])
    error_message = "a role is bound to exactly its namespace and service account, audience vault, its own policy, 1 hour (4 at most)"
  }
  assert {
    condition     = tolist(vault_kubernetes_auth_backend_role.k8s_service["monitoring"].bound_service_account_namespaces) == tolist(["monitoring"])
    error_message = "the collector's role is bound to namespace monitoring (no ryft- prefix)"
  }
  assert {
    condition     = jsonencode(output.auth_roles["monitoring"]) == jsonencode({ mount = "kubernetes", role = "monitoring", sa = "monitoring", k8s_ns = "monitoring" })
    error_message = "auth_roles: ${jsonencode(output.auth_roles["monitoring"])}"
  }
  assert {
    condition = alltrue([for k, n in kubernetes_namespace_v1.service :
    n.metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted" if k != "monitoring"])
    error_message = "every RYFT namespace enforces Pod Security restricted"
  }
  # Nothing selects on the HCP namespace label (charts, admission, deploy): CE has no namespaces, so it is gone.
  assert {
    condition = alltrue([for k, n in kubernetes_namespace_v1.service :
      !contains(keys(n.metadata[0].labels), "ryft.dev/vault-namespace") &&
      n.metadata[0].labels["ryft.dev/service"] == k && n.metadata[0].labels["app.kubernetes.io/part-of"] == "ryft"
    ])
    error_message = "a namespace carries ryft.dev/service and part-of ryft, and no ryft.dev/vault-namespace label (there is no Vault namespace)"
  }
  assert {
    condition = alltrue([for k, sa in kubernetes_service_account_v1.workload :
    sa.automount_service_account_token == false && sa.metadata[0].namespace == local.services[k].k8s_ns])
    error_message = "workload pods get no Kubernetes API token"
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
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["api"].data_json).DATA_ENCRYPTION_KEY) == "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
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
      strcontains(local_file.vso["monitoring"].content, "  mount: kv-monitoring\n") &&
      strcontains(local_file.vso["monitoring"].content,
      "- kind: DaemonSet\n      name: grafana-cloud-alloy-daemonset\n    - kind: Deployment\n      name: grafana-cloud-alloy-deployment\n")
    )
    error_message = "monitoring (no ryft- prefix) reads kv-monitoring and restarts both collectors"
  }
  # The Vault metrics scrape (#153, deploy/monitoring) reads the Secret vault-ca in namespace monitoring.
  assert {
    condition = strcontains(local_file.vso["monitoring"].content,
    "kind: Certificate\nmetadata:\n  name: vault-ca\n  namespace: monitoring\n") && strcontains(local_file.vso["monitoring"].content, "  secretName: vault-ca\n")
    error_message = "monitoring's file requests the Secret vault-ca in namespace monitoring (the Vault metrics scrape reads it)"
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
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["api"].data_json).ADMIN_API_KEY) == "generatedpassword0123456789"
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
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["ingest"].data_json).BETA_GATE) == "on"
    error_message = "ingest gets the value"
  }
}

run "the_demo_password_stays_with_the_operator" {
  command = apply

  assert {
    condition     = vault_mount.k8s_operator.path == "kv-operator" && vault_kv_secret_v2.k8s_demo.mount == "kv-operator" && vault_kv_secret_v2.k8s_demo.name == "demo"
    error_message = "the demo password lives at kv-operator/demo"
  }
  assert {
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_demo.data_json).email) == "review@ryftlab.de"
    error_message = "the demo account is review@ryftlab.de"
  }
  assert {
    condition     = alltrue([for s, v in vault_policy.k8s_service : !strcontains(v.policy, "operator")]) && !strcontains(vault_policy.k8s_snapshot.policy, "operator")
    error_message = "no role's policy reaches the operator mount"
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
    condition     = nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["scoring"].data_json).CONSENT_ENFORCE) == "warn"
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
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config[c].data_json).AMQP_USER) == c &&
    nonsensitive(jsondecode(vault_kv_secret_v2.k8s_amqp_user[c].data_json).username) == c])
    error_message = "each client is its own RabbitMQ user, and the broker's mount has its credentials"
  }
  assert {
    condition = toset(keys(vault_kv_secret_v2.k8s_amqp_user)) == toset(["ingest", "api", "scoring", "notify"]) && alltrue([for c, s in vault_kv_secret_v2.k8s_amqp_user :
    s.name == "amqp-user-${c}" && s.mount == "kv-rabbitmq" && s.delete_all_versions == true])
    error_message = "the users' credentials live in the broker's mount kv-rabbitmq, one secret per user"
  }
  assert {
    condition     = !contains(keys(vault_kv_secret_v2.k8s_config), "rabbitmq")
    error_message = "the broker's mount has no config (no ryft-secrets): nothing of the database or the other services"
  }
  assert {
    condition     = alltrue([for name, s in local.service_secret_keys : contains(["ingest", "api", "scoring", "notify"], name) == contains(s, "AMQP_PASSWORD")])
    error_message = "AMQP_PASSWORD reaches the four clients only"
  }
  assert {
    condition     = local_file.vso["rabbitmq"].content == file("${path.module}/tests/golden/k8s/rabbitmq.vault.yaml")
    error_message = "the broker's operator manifests differ from tests/golden/k8s/rabbitmq.vault.yaml (one labelled VaultStaticSecret per user)"
  }
  assert {
    condition     = strcontains(vault_policy.k8s_service["rabbitmq"].policy, "kv-rabbitmq/data/amqp-user-*") && !strcontains(vault_policy.k8s_service["api"].policy, "amqp-user")
    error_message = "only the broker's policy may read the users' credentials"
  }
}

# ---------------------------------------------------------------------------------------------------------------
# The in-cluster Vault (k8s.tf, D207): the only Vault this root writes, and the only one the generated objects read.

# Every generated file, monitoring and the import service included: the Certificate vault-ca, the connection to the
# in-cluster address trusting vault-ca, the auth without a Vault namespace, the secrets from kv-<svc>, and the in-app
# waves (connection and auth in 0, the secrets in 1, design 1.3); never a literal Secret, never an HCP address.
run "every_namespace_reads_the_in_cluster_vault" {
  command = plan

  assert {
    condition = length(local_file.vso) == length(var.services) && alltrue([for k, f in local_file.vso :
      strcontains(f.content, "kind: Certificate\nmetadata:\n  name: vault-ca\n  namespace: ${local.services[k].k8s_ns}\n") &&
      strcontains(f.content, "  commonName: vso.${local.services[k].k8s_ns}\n") &&
      strcontains(f.content, "  address: https://vault.vault.svc:8200\n") && strcontains(f.content, "  caCertSecretRef: vault-ca\n") &&
      strcontains(f.content, "  skipTLSVerify: false\n") &&
      !strcontains(f.content, "hashicorp.cloud") && !strcontains(f.content, "admin/") && !strcontains(f.content, "kind: Secret\n")
    ])
    error_message = "every namespace gets its Certificate vault-ca and a connection to https://vault.vault.svc:8200 with caCertSecretRef vault-ca; no HCP address or namespace path, no literal Secret"
  }
  assert {
    # try(): the operands of || and && are all evaluated, and a kind without the attribute must not fail the run.
    condition = alltrue([for s, f in local_file.vso : alltrue([for d in split("\n---\n", f.content) :
      yamldecode(d).kind == "Certificate" ? !can(yamldecode(d).metadata.annotations) : (
        try(yamldecode(d).metadata.annotations, {}) == { "argocd.argoproj.io/sync-wave" = (yamldecode(d).kind == "VaultStaticSecret" ? "1" : "0") } &&
        (yamldecode(d).kind != "VaultAuth" || (!can(yamldecode(d).spec.namespace) && try(yamldecode(d).spec.kubernetes.role, "") == s && try(yamldecode(d).spec.mount, "") == "kubernetes")) &&
        (yamldecode(d).kind != "VaultStaticSecret" || try(yamldecode(d).spec.mount, "") == "kv-${s}")
    )])])
    error_message = "connection and auth in wave 0, the secrets in wave 1; the auth uses mount kubernetes and the service's role, without a Vault namespace; every VaultStaticSecret reads kv-<svc>"
  }
  assert {
    condition = alltrue([for s, f in local_file.vso :
      strcontains(f.content, "kind: VaultStaticSecret\n") == (contains(keys(local.services_with_secrets), s) || local.services[s].amqp == "server")
    ])
    error_message = "a service gets a VaultStaticSecret exactly when it has secrets (the broker: its users'); none for a service without"
  }
}

run "each_service_gets_one_mount_one_role_one_policy" {
  command = plan

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
      vault_auth_backend.k8s.type == "kubernetes" && vault_auth_backend.k8s.path == "kubernetes" &&
      alltrue([for r in values(vault_kubernetes_auth_backend_role.k8s_service) : r.backend == "kubernetes"]) &&
      vault_kubernetes_auth_backend_role.k8s_snapshot.backend == "kubernetes"
    )
    error_message = "one Kubernetes auth mount at kubernetes (the path P4 pins), which every role uses"
  }
  assert {
    condition     = vault_mount.k8s_operator.path == "kv-operator" && vault_mount.k8s_operator.type == "kv" && vault_mount.k8s_operator.options.version == "2"
    error_message = "the operator's mount is kv-operator, KV version 2"
  }
}

run "policies_read_only_their_own_config" {
  command = plan

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

# The snapshot CronJob (charts/vault-extras, PR 4) logs in as role vault-snapshot from vault/vault-snapshot with a
# 10-minute projected token for audience vault, and may save a snapshot, nothing else (restore is break-glass).
run "the_snapshot_job_saves_a_snapshot_and_nothing_else" {
  command = plan

  assert {
    condition     = vault_policy.k8s_snapshot.name == "snapshot" && vault_policy.k8s_snapshot.policy == "path \"sys/storage/raft/snapshot\" {\n  capabilities = [\"read\"]\n}\n"
    error_message = "policy snapshot is read on sys/storage/raft/snapshot and nothing else (no update: that is restore; no sudo, no other path)"
  }
  assert {
    condition = (
      vault_kubernetes_auth_backend_role.k8s_snapshot.backend == "kubernetes" &&
      vault_kubernetes_auth_backend_role.k8s_snapshot.role_name == "vault-snapshot" &&
      tolist(vault_kubernetes_auth_backend_role.k8s_snapshot.bound_service_account_names) == tolist(["vault-snapshot"]) &&
      tolist(vault_kubernetes_auth_backend_role.k8s_snapshot.bound_service_account_namespaces) == tolist(["vault"]) &&
      vault_kubernetes_auth_backend_role.k8s_snapshot.audience == "vault" &&
      tolist(vault_kubernetes_auth_backend_role.k8s_snapshot.token_policies) == tolist(["snapshot"]) &&
      vault_kubernetes_auth_backend_role.k8s_snapshot.token_ttl == 600 && vault_kubernetes_auth_backend_role.k8s_snapshot.token_max_ttl == 600 &&
      vault_kubernetes_auth_backend_role.k8s_snapshot.token_type == "service" &&
      vault_kubernetes_auth_backend_role.k8s_snapshot.token_no_default_policy == true
    )
    error_message = "role vault-snapshot is bound to vault/vault-snapshot, audience vault, policy snapshot only (no default policy), revocable service tokens of 10 minutes at most"
  }
  assert {
    condition = toset(concat([for p in values(vault_policy.k8s_service) : p.name], [vault_policy.k8s_snapshot.name])) == toset(concat(keys(var.services), ["snapshot"])) && toset(concat(
      [for r in values(vault_kubernetes_auth_backend_role.k8s_service) : r.role_name], [vault_kubernetes_auth_backend_role.k8s_snapshot.role_name]
    )) == toset(concat(keys(var.services), ["vault-snapshot"]))
    error_message = "the in-cluster Vault gets exactly one policy per service plus snapshot, and one role per service plus vault-snapshot"
  }
  # What this root writes is exactly what its token may write: the policy and role paths of bootstrap/terraform.hcl.
  assert {
    condition = (
      toset([for p in local.bootstrap_policy_paths.terraform : trimprefix(p, "sys/policies/acl/") if startswith(p, "sys/policies/acl/")]) ==
      toset(concat([for p in values(vault_policy.k8s_service) : p.name], [vault_policy.k8s_snapshot.name])) &&
      toset([for p in local.bootstrap_policy_paths.terraform : trimprefix(p, "auth/kubernetes/role/") if startswith(p, "auth/kubernetes/role/")]) ==
      toset(concat([for r in values(vault_kubernetes_auth_backend_role.k8s_service) : r.role_name], [vault_kubernetes_auth_backend_role.k8s_snapshot.role_name])) &&
      toset([for p in local.bootstrap_policy_paths.terraform : trimsuffix(trimprefix(p, "sys/mounts/"), "/tune") if startswith(p, "sys/mounts/kv-")]) ==
      toset(concat([for m in values(vault_mount.k8s_kv) : m.path], [vault_mount.k8s_operator.path]))
    )
    error_message = "the terraform policy names exactly the policies, roles and mounts k8s.tf writes: a missing one is a 403 at the apply, an extra one a right nobody uses"
  }
}

# One random value per credential: every Vault write takes it from the random_* of secrets.tf (and operator.tf), so
# an import there (the data key, the demo password) is what the services read. Marked values per credential
# (overrides, in a state of its own so the random_* are created here), so a write that took another random_* (or a
# fresh one) would differ.
run "one_random_value_per_credential" {
  command   = apply
  state_key = "one_random"

  variables {
    database_owner_password = "owner-password-for-the-test"
    shared_secrets          = { BETA_GATE = "warn", CONSENT_ENFORCE = "on" }
  }

  override_resource {
    target = random_password.amqp
    values = { result = "amqp-password-from-the-one-random" }
  }
  override_resource {
    target = random_password.demo
    values = { result = "demo-password-from-the-one-random" }
  }

  assert {
    condition = alltrue([for c, s in vault_kv_secret_v2.k8s_amqp_user :
      nonsensitive(jsondecode(s.data_json).password) == "amqp-password-from-the-one-random" &&
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config[c].data_json).AMQP_PASSWORD) == "amqp-password-from-the-one-random"
    ])
    error_message = "a client's AMQP_PASSWORD and the broker's amqp-user-<c> come from the one random_password.amqp[c]"
  }
  assert {
    condition = (
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_demo.data_json).password) == "demo-password-from-the-one-random" &&
      nonsensitive(output.demo_password) == "demo-password-from-the-one-random"
    )
    error_message = "kv-operator/demo and output demo_password are random_password.demo (the imported value)"
  }
  assert {
    condition = (
      nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["api"].data_json).DATA_ENCRYPTION_KEY) == nonsensitive(random_bytes.data_key["api"].base64) &&
      toset(keys(random_bytes.data_key)) == toset(["api"])
    )
    error_message = "the api's DATA_ENCRYPTION_KEY is random_bytes.data_key[\"api\"].base64 (the imported key), and only the api has one"
  }
  assert {
    condition     = strcontains(nonsensitive(jsondecode(vault_kv_secret_v2.k8s_config["migrate"].data_json).DATABASE_URL), ":owner-password-for-the-test@")
    error_message = "the owner URL reaches kv-migrate/config unchanged"
  }
  assert {
    condition     = length(random_password.amqp) == length(local.amqp_clients) && length(random_password.db) == length(local.db_roles)
    error_message = "one random_password per credential"
  }
}

# The two values the rebuild imports (docs/OPERATIONS.md -> Vault -> The first apply) keep their addresses and a
# configuration the import's attributes satisfy: random_bytes.data_key["api"] is 32 bytes (the import takes the
# length from the value), and random_password.demo ignores the attributes the import sets to defaults (length from
# the value, special/upper/lower/numeric true), so the first plan after the import changes neither.
run "the_imported_values_keep_their_addresses" {
  command = plan

  assert {
    condition = (
      toset(keys(random_bytes.data_key)) == toset(["api"]) && random_bytes.data_key["api"].length == 32 &&
      random_password.demo.length == 24 && random_password.demo.special == false
    )
    error_message = "random_bytes.data_key[\"api\"] (32 bytes) and random_password.demo (24, alphanumeric) are the import targets: same addresses, same arguments"
  }
  assert {
    condition = can(regex(
      "(?s)resource \"random_password\" \"demo\" \\{.*lifecycle \\{\\s*ignore_changes = \\[length, special, upper, lower, numeric, min_lower, min_upper, min_numeric, min_special, override_special\\]\\s*\\}",
      file("${path.module}/operator.tf")
    ))
    error_message = "random_password.demo must ignore the arguments an import cannot reproduce (otherwise the first plan replaces the imported password)"
  }
}

# After the import the state holds the data key's only copy (D211): a plan that destroys or replaces
# random_bytes.data_key fails instead. Terraform test does not enforce prevent_destroy (its teardown destroys every
# mocked resource), so the file itself is checked, as G4 is in infra/aws/kubernetes: the flag on a line of its own
# inside the block's lifecycle (a commented-out flag does not count). random_password.demo stays without it: its
# rotation is a -replace (README -> The App Review demo password).
run "the_data_key_cannot_be_destroyed" {
  command = plan

  assert {
    condition = length([for c in split("\nresource \"", file("${path.module}/secrets.tf")) : c if startswith(c, "random_bytes\" \"data_key\" {")]) == 1 && alltrue([
      for c in split("\nresource \"", file("${path.module}/secrets.tf")) :
      can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true$", c)) if startswith(c, "random_bytes\" \"data_key\" {")
    ])
    error_message = "random_bytes.data_key (secrets.tf) needs lifecycle { prevent_destroy = true }, not commented out: after the import the state is the key's only copy (D211)"
  }
  assert {
    condition = alltrue([for f in fileset(path.module, "*.tf") : alltrue([
      for c in split("\nresource \"", file("${path.module}/${f}")) :
      !can(regex("(?m)^[ \\t]*prevent_destroy\\s*=\\s*true", c)) if startswith(c, "random_password\" ")
    ])])
    error_message = "no random_password carries prevent_destroy: each rotates with -replace (random_password.demo among them, README)"
  }
  # The pattern itself: it matches the flag, and neither a commented-out flag nor a lifecycle without it (the demo
  # password's, which has ignore_changes only). Every assert here reads a file of the module (path.module): one that
  # refers to no object fails the run.
  assert {
    condition = (
      can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true$", "random_bytes\" \"x\" {\n  lifecycle {\n    prevent_destroy = true\n  }\n}\n")) &&
      !can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true$", "random_bytes\" \"x\" {\n  lifecycle {\n    # prevent_destroy = true\n  }\n}\n")) &&
      length([for c in split("\nresource \"", file("${path.module}/operator.tf")) : c if startswith(c, "random_password\" \"demo\" {") && strcontains(c, "lifecycle {")]) == 1 &&
      !anytrue([for c in split("\nresource \"", file("${path.module}/operator.tf")) :
      can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true$", c)) if startswith(c, "random_password\" \"demo\" {")])
    )
    error_message = "the prevent_destroy pattern is broken: it must match the flag, and neither a commented-out flag nor the demo password's lifecycle (ignore_changes only)"
  }
}

run "shared_secrets_reach_only_the_services_that_list_them" {
  command = plan

  variables {
    shared_secrets = { BETA_GATE = "on", CONSENT_ENFORCE = "on" }
  }

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

  assert {
    condition = (
      vault_kubernetes_auth_backend_config.k8s.backend == "kubernetes" &&
      vault_kubernetes_auth_backend_config.k8s.kubernetes_host == "https://kubernetes.default.svc" &&
      vault_kubernetes_auth_backend_config.k8s.disable_local_ca_jwt == false &&
      vault_kubernetes_auth_backend_config.k8s.token_reviewer_jwt == null &&
      vault_kubernetes_auth_backend_config.k8s.pem_keys == null
    )
    error_message = "Kubernetes auth uses the in-cluster API and Vault's own pod token and CA: no reviewer JWT, no keys"
  }
  assert {
    # kubernetes_ca_cert is computed (a mock would invent one), so the configuration is read instead.
    condition = length(regexall("(?s)resource \"vault_kubernetes_auth_backend_config\" \"k8s\" \\{.*?\n\\}", file("${path.module}/k8s.tf"))) == 1 && alltrue([
      for b in regexall("(?s)resource \"vault_kubernetes_auth_backend_config\" \"k8s\" \\{.*?\n\\}", file("${path.module}/k8s.tf")) :
    !strcontains(b, "kubernetes_ca_cert") && !strcontains(b, "token_reviewer_jwt") && !strcontains(b, "issuer")])
    error_message = "the in-cluster auth config sets no CA, reviewer JWT or issuer"
  }
}

# Every plan and apply writes the in-cluster Vault, so every one needs the one-hour terraform token.
run "the_token_is_required" {
  command = plan

  variables {
    vault_k8s_token = null
  }
  expect_failures = [var.vault_k8s_token]
}

run "the_token_may_not_be_empty" {
  command = plan

  variables {
    vault_k8s_token = ""
  }
  expect_failures = [var.vault_k8s_token]
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

# D207: a terraform.tfvars from the HCP era that still sets one of these fails the plan, naming the line to delete,
# instead of being ignored (a silently ignored HCP address would look like a working configuration).
run "the_hcp_variables_are_retired" {
  command = plan

  variables {
    vault_parent_namespace_root = "admin"
    vault_address               = "https://vault.example.hashicorp.cloud:8200"
    vault_address_for_cluster   = "https://vault.example.hashicorp.cloud:8200"
    vault_ca_bundle_b64         = "LS0tLS1CRUdJTi0tLS0t"
  }
  expect_failures = [var.vault_parent_namespace_root, var.vault_address, var.vault_address_for_cluster, var.vault_ca_bundle_b64]
}

# An empty string is a set line too (the tripwire is null, not "").
run "an_empty_retired_variable_still_fails" {
  command = plan

  variables {
    vault_parent_namespace_root = ""
  }
  expect_failures = [var.vault_parent_namespace_root]
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

# One Vault, one provider (D207): the default `vault` provider in providers.tf, which can only reach the port-forward
# (var.vault_k8s_address) with the ryft-vault CA and the terraform token. No alias and no `provider =` anywhere, so
# no object can be pointed at another Vault; every Vault object is in k8s.tf, where the terraform policy's allowlist
# is reviewed against it (the_snapshot_job_saves_a_snapshot_and_nothing_else).
#
# The patterns are anchored at the start of a line with the block's own indentation, so a `#` comment line or a
# line inside a heredoc (the policies) is never taken for configuration; a block inside /* */ would be counted,
# which can only fail a run, never pass one.
run "one_vault_provider_and_every_vault_object_in_k8s_tf" {
  command = plan

  assert {
    condition = (
      length(regexall("(?m)^provider \"vault\" \\{$", file("${path.module}/providers.tf"))) == 1 &&
      alltrue([for f in fileset(path.module, "*.tf") : f == "providers.tf" || length(regexall("(?m)^\\s*provider\\s+\"vault\"", file("${path.module}/${f}"))) == 0]) &&
      !can(regex("(?m)^  alias", regex("(?ms)^provider \"vault\" \\{\\n.*?^\\}$", file("${path.module}/providers.tf"))))
    )
    error_message = "exactly one provider \"vault\" block, in providers.tf, without an alias: one Vault, nothing to point a resource elsewhere"
  }
  assert {
    condition     = alltrue([for f in fileset(path.module, "*.tf") : length(regexall("(?m)^\\s*provider\\s*=", file("${path.module}/${f}"))) == 0])
    error_message = "no resource or data source sets provider =: every object goes to the one default provider of its type"
  }
  assert {
    condition = (
      alltrue([for f in fileset(path.module, "*.tf") : f == "k8s.tf" || length(regexall("(?m)^\\s*(resource|data|ephemeral)\\s+\"vault_", file("${path.module}/${f}"))) == 0]) &&
      length(regexall("(?m)^resource \"vault_[a-z0-9_]+\" \"[a-z0-9_]+\" \\{$", file("${path.module}/k8s.tf"))) == 11 &&
      length(regexall("(?m)^(resource|data|ephemeral|module|locals|variable|output)\\b", file("${path.module}/k8s.tf"))) == 11
    )
    error_message = "every vault_ object is a resource in k8s.tf (11 blocks, and nothing else there); no other file declares one"
  }
  assert {
    condition = alltrue([for re in [
      "(?m)^  address += var\\.vault_k8s_address$",
      "(?m)^  ca_cert_file += pathexpand\\(var\\.vault_k8s_ca_cert_file\\)$",
      "(?m)^  skip_tls_verify += false( +#.*)?$",
      "(?m)^  token += var\\.vault_k8s_token$",
    ] : can(regex(re, regex("(?ms)^provider \"vault\" \\{\\n.*?^\\}$", file("${path.module}/providers.tf"))))])
    error_message = "the Vault provider sets the port-forward address, the CA file, skip_tls_verify = false and the token explicitly (unset, it would take VAULT_ADDR, VAULT_CACERT, VAULT_SKIP_VERIFY or VAULT_TOKEN from the environment)"
  }
}

# No mount or auth backend this root writes on the in-cluster Vault switches audit HMAC off or exposes headers or
# the mount: the policy terraform refuses these keys on tune, but not inside a mount's creation (its header).
run "no_k8s_mount_switches_audit_hmac_off" {
  command = plan

  assert {
    condition = length(local.k8s_kv_mount) > 0 && length(regexall(
      "audit_non_hmac|passthrough_request_headers|allowed_response_headers|listing_visibility|(?m)^\\s*tune\\s*\\{",
      join("\n", [for l in split("\n", file("${path.module}/k8s.tf")) : l if !startswith(trimspace(l), "#")])
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
  # Every block's whole body, byte for byte (the capabilities line is pinned above): allowed_parameters cannot turn
  # into denied_parameters, the "*" key cannot be renamed or dropped, and no line can be added anywhere.
  assert {
    condition = alltrue([for p, body in local.bootstrap_policy_blocks.terraform : body == join("", [
      format("  capabilities = [%s]\n", try(regex("^  capabilities = \\[([^\\]\\n]*)\\]\\n", body)[0], "MISSING")),
      endswith(p, "/tune") ? join("", concat(
        ["  denied_parameters = {\n"],
        [for k in concat(
          ["audit_non_hmac_request_keys", "audit_non_hmac_response_keys", "passthrough_request_headers", "allowed_response_headers", "listing_visibility"],
          p == "sys/mounts/auth/kubernetes/tune" ? ["token_type"] : []
        ) : format("    %-28s = []\n", k)],
        ["  }\n"]
        )) : p == "sys/auth/kubernetes" ? join("", [
        "  allowed_parameters = {\n", format("    %-4s = [\"kubernetes\"]\n", "type"), format("    %-4s = []\n", "\"*\""), "  }\n"
        ]) : p == "auth/kubernetes/config" ? join("", [
        "  allowed_parameters = {\n", format("    %-15s = [\"https://kubernetes.default.svc\"]\n", "kubernetes_host"), format("    %-15s = []\n", "\"*\""), "  }\n"
        ]) : startswith(p, "auth/kubernetes/role/") ? join("", concat(
        ["  allowed_parameters = {\n"],
        # [policy, service account, namespace]: the snapshot job's literally, a service's from its locals.
        [for kv in [
          ["token_policies", p == "auth/kubernetes/role/vault-snapshot" ? "snapshot" : trimprefix(p, "auth/kubernetes/role/")],
          ["policies", p == "auth/kubernetes/role/vault-snapshot" ? "snapshot" : trimprefix(p, "auth/kubernetes/role/")],
          ["bound_service_account_names", p == "auth/kubernetes/role/vault-snapshot" ? "vault-snapshot" : try(local.services[trimprefix(p, "auth/kubernetes/role/")].service_account, "NO-SUCH-SERVICE")],
          ["bound_service_account_namespaces", p == "auth/kubernetes/role/vault-snapshot" ? "vault" : try(local.services[trimprefix(p, "auth/kubernetes/role/")].k8s_ns, "NO-SUCH-SERVICE")],
          ["bound_service_account_namespace_selector", ""],
        ] : format("    %-40s = [\"%s\"]\n", kv[0], kv[1])],
        [format("    %-40s = []\n", "\"*\""), "  }\n"]
      )) : "",
    ])])
    error_message = "parameter limits, as whole block bodies: every tune refuses the audit HMAC, header and listing keys (the auth mount's also token_type); the auth mount stays type kubernetes; its config keeps the in-cluster API server; a role carries only its own policy, namespace and service account, with \"*\" = [] for everything else; every other path has its capabilities line and nothing else"
  }
}

# Both policies are nothing but `#` comment lines and the blocks the allowlists read. Vault's parser also takes
# `path"x" {`, `"path" "x" {`, one-line blocks, /* */ and // comments and JSON; the regexes above would not see such
# a block. So: remove the comment lines and every matched block, and only whitespace may remain; with the comment
# lines and every quoted string removed, no /*, */ or // may remain (a glob like "kv-api/data/*" is inside a string).
run "both_policies_are_plain_blocks_only" {
  command = plan

  assert {
    condition     = alltrue([for k in ["terraform", "admin"] : trimspace(local.bootstrap_policy_residue[k]) == ""])
    error_message = "bootstrap/terraform.hcl or bootstrap/admin.hcl holds something besides `#` comment lines and `path \"<p>\" {` ... `}` blocks at the start of a line: a block the allowlist does not see"
  }
  assert {
    condition = alltrue([for k in ["terraform", "admin"] :
      length(regexall("/\\*|\\*/|//", local.bootstrap_policy_code[k])) == 0 && !can(regex("^\\s*[\\{\\[]", local.bootstrap_policy_code[k]))
    ])
    error_message = "bootstrap/terraform.hcl or bootstrap/admin.hcl uses /* */ or // comments, or is JSON: only `#` comments and HCL blocks, so that what the tests read is what Vault parses"
  }
}

# The login is a generate-root session (D209, docs/OPERATIONS.md -> Vault -> Login): the root token creates one
# token of the token role admin and is revoked; the admin token creates the terraform token. Both roles are pinned
# here as an exact allowlist of keys and values.
run "the_admin_policy_and_the_token_roles_hold_the_reviewed_shape" {
  command = plan

  # admin is everything but what outlives the session or opens another way in: no orphan, no token of a role but
  # terraform (in any spelling: the glob, which the exact terraform path overrides), no token role write, no auth
  # method enabled, disabled or moved, no write of an auth method's roles or config or of identity (a standing login:
  # a role or an identity with policy admin, a config that trusts another cluster), no policy write (otherwise it
  # rewrites admin, terraform or default and lifts the denies), and no snapshot restore. Paths in file order, each
  # block's whole body.
  assert {
    condition = (
      local.bootstrap_policy_paths.admin == [
        "*", "auth/token/create", "auth/token/create-orphan", "auth/token/create/*", "auth/token/create/terraform", "auth/token/roles/*",
        "sys/auth/*", "sys/remount", "auth/+/role/*", "auth/+/config", "identity/*", "sys/policies/acl/*", "sys/policy/*",
        "sys/storage/raft/snapshot", "sys/storage/raft/snapshot-force",
      ] &&
      jsonencode(local.bootstrap_policy_blocks.admin) == jsonencode({
        "*"                               = "  capabilities = [\"create\", \"read\", \"update\", \"patch\", \"delete\", \"list\", \"sudo\"]\n"
        "auth/token/create"               = "  capabilities = [\"deny\"]\n"
        "auth/token/create-orphan"        = "  capabilities = [\"deny\"]\n"
        "auth/token/create/*"             = "  capabilities = [\"deny\"]\n"
        "auth/token/create/terraform"     = "  capabilities = [\"create\", \"update\"]\n"
        "auth/token/roles/*"              = "  capabilities = [\"deny\"]\n"
        "sys/auth/*"                      = "  capabilities = [\"deny\"]\n"
        "sys/remount"                     = "  capabilities = [\"deny\"]\n"
        "auth/+/role/*"                   = "  capabilities = [\"read\", \"list\"]\n"
        "auth/+/config"                   = "  capabilities = [\"read\"]\n"
        "identity/*"                      = "  capabilities = [\"read\", \"list\"]\n"
        "sys/policies/acl/*"              = "  capabilities = [\"read\", \"list\"]\n"
        "sys/policy/*"                    = "  capabilities = [\"read\", \"list\"]\n"
        "sys/storage/raft/snapshot"       = "  capabilities = [\"read\"]\n"
        "sys/storage/raft/snapshot-force" = "  capabilities = [\"deny\"]\n"
      })
    )
    error_message = "admin is \"*\" with every capability, deny on auth/token/create, auth/token/create-orphan, auth/token/create/* (every token role), auth/token/roles/*, sys/auth/*, sys/remount and sys/storage/raft/snapshot-force, read and list only on sys/policies/acl/*, sys/policy/*, auth/+/role/* and identity/*, read only on auth/+/config and sys/storage/raft/snapshot (a save, no restore), and create and update on auth/token/create/terraform (the terraform token, a child of the session's token); no other path, no other line"
  }
  # No standing login (D209, the security re-review of the rebuild): with "*" alone, each of these would let the
  # admin token leave a login behind that mints admin (or any policy) after the session, with the token role admin
  # deleted: a kubernetes role bound to a service account, an identity group or entity with policies, an auth config
  # that trusts another cluster, an auth mount moved below a nested path (where the `+` blocks miss it) and back, a
  # restored snapshot. Named one by one so that a removal fails with this message.
  assert {
    condition = (
      alltrue([for p in ["auth/+/role/*", "identity/*"] : try(local.bootstrap_policy_blocks.admin[p], "") == "  capabilities = [\"read\", \"list\"]\n"]) &&
      alltrue([for p in ["auth/+/config", "sys/storage/raft/snapshot"] : try(local.bootstrap_policy_blocks.admin[p], "") == "  capabilities = [\"read\"]\n"]) &&
      alltrue([for p in ["sys/remount", "sys/storage/raft/snapshot-force"] : try(local.bootstrap_policy_blocks.admin[p], "") == "  capabilities = [\"deny\"]\n"])
    )
    error_message = "the admin token plants no standing login: auth/+/role/* and identity/* read and list only, auth/+/config and sys/storage/raft/snapshot read only, sys/remount and sys/storage/raft/snapshot-force denied"
  }
  # The terraform token comes from the admin token, so its role must stay a non-orphan child (dies with the
  # session), and the admin role an orphan that only a root token mints (the glob above refuses it to admin).
  assert {
    condition = (
      local.bootstrap.token_role_terraform.orphan == false && local.bootstrap.token_role_admin.orphan == true &&
      !contains(keys(local.bootstrap_policy_blocks.admin), "auth/token/create/admin") &&
      local.bootstrap_policy_blocks.admin["auth/token/create/*"] == "  capabilities = [\"deny\"]\n"
    )
    error_message = "the admin token mints terraform tokens (children) and nothing else: role admin is reached only by a root token (auth/token/create/* denied, no exact allow for admin)"
  }
  # An orphan, so revoking the root token that created it does not revoke it (and with it the terraform token).
  assert {
    condition = (
      jsonencode(local.bootstrap.token_role_admin) == jsonencode({
        allowed_policies        = ["admin"]
        disallowed_policies     = ["root"]
        orphan                  = true
        renewable               = false
        token_bound_cidrs       = ["127.0.0.1/32"]
        token_explicit_max_ttl  = 3600
        token_no_default_policy = false
        token_type              = "service"
      })
    )
    error_message = "the token role admin: the policy admin only (never root), an orphan (revoking the root token leaves it), one hour, not renewable, a revocable service token, through the port-forward only, and no other key"
  }
  assert {
    condition = (
      jsonencode(local.bootstrap.token_role_terraform) == jsonencode({
        allowed_policies        = ["terraform"]
        disallowed_policies     = ["admin", "root"]
        orphan                  = false
        renewable               = false
        token_bound_cidrs       = ["127.0.0.1/32"]
        token_explicit_max_ttl  = 3600
        token_no_default_policy = false
        token_type              = "service"
      })
    )
    error_message = "the token role terraform: the policy terraform only (never admin or root), a child of the admin token (not an orphan: it dies with the session), one hour, not renewable, a revocable service token, through the port-forward only, and no other key"
  }
}

# The OIDC files are the documented later option (bootstrap.tf's header): written nowhere in the rebuild, and still
# the Google Workspace version. Pinned so that the change for a personal Google account is a reviewed one.
run "the_oidc_files_stay_the_reviewed_later_option" {
  command = plan

  assert {
    condition = alltrue([for r in [local.bootstrap.oidc_role_admin, local.bootstrap.oidc_role_enroll] : (
      r.role_type == "oidc" && r.user_claim == "sub" && r.verbose_oidc_logging == false &&
      r.bound_claims == { hd = "ryftlab.de" } && r.bound_claims_type == "string" && length(r.oidc_scopes) == 0 &&
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
    error_message = "the OIDC config names Google and the admin role; the client id and secret would be passed at the setup, never committed"
  }
}
