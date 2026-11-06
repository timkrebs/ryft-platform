# The plan of this root must not need the in-cluster Vault while local.write_k8s is false (targets.tf): no
# port-forward, no CA file, no TF_VAR_vault_k8s_token (design 2.2 and Appendix B #1). Here the provider vault.k8s
# is NOT mocked: the real provider runs, configured with an address nothing listens on and a CA file that does not
# exist. The run passes only if the plan makes no request to the in-cluster Vault and needs neither the CA file
# nor a token. It does not prove that no resource is planned on vault.k8s (a planned create makes no request):
# tests/vault.tftest.hcl (k8s_target_is_off_by_default) pins that. Every other provider is mocked, as in
# vault.tftest.hcl; the services are a subset, so the committed policy render (default services) does not match.
#   terraform init -backend=false && terraform test -filter=tests/k8s_offline.tftest.hcl

variables {
  vault_parent_namespace_root = "admin"
  vault_address_for_cluster   = "https://vault.example.hashicorp.cloud:8200"
  supabase_project_ref        = "abcdefghijklmnopqrst"
  database                    = { host = "aws-0-eu-central-1.pooler.supabase.com" }
  database_owner_password     = "owner-password-123"
  shared_secrets              = {}
  services = {
    api      = { secret_keys = ["SUPABASE_PROJECT_REF"], database = "role", amqp = "client", data_key = true, admin_key = true }
    rabbitmq = { amqp = "server", workload_kind = "" }
    migrate  = { database = "owner", workload_kind = "" }
  }
  service_secrets = {
    api = {
      SUPABASE_SERVICE_ROLE_KEY = "sb_secret_test"
      APPLE_TEAM_ID             = "M77SKHKG63", APPLE_KEY_ID = "ABCDE12345", APPLE_CLIENT_ID = "dev.timkrebs.ryft"
      # The shape of a .p8, not a key: only the format is checked here.
      APPLE_KEY_P8 = "-----BEGIN PRIVATE KEY-----\nMIGTAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBHkwdwIBAQQgAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\nAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n-----END PRIVATE KEY-----"
    }
  }
  # Port 9 (discard) on loopback: nothing answers, so any request of the provider would fail the run.
  vault_k8s_address      = "https://127.0.0.1:9"
  vault_k8s_ca_cert_file = "/nonexistent/ryft/vault-ca.pem"
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

run "the_plan_needs_no_port_forward" {
  command         = plan
  expect_failures = [check.bootstrap_terraform_policy_matches_services]

  assert {
    condition     = length(vault_auth_backend.k8s) == 0 && length(vault_mount.k8s_kv) == 0 && length(local_file.vso) == 3
    error_message = "with write_k8s false the plan writes the generated files and nothing on the in-cluster Vault"
  }
  assert {
    condition     = strcontains(local_file.vso["api"].content, "address: https://vault.example.hashicorp.cloud:8200\n")
    error_message = "every namespace stays on HCP"
  }
}
