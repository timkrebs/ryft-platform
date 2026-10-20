# Operator-only secrets in the project namespace. No workload has a policy for this
# mount; the operator reads it with an admin token:
#   VAULT_NAMESPACE=admin/ryft vault kv get -mount=operator demo
#
# The App Review demo account's password (scripts/seed_demo.sh, docs/OPERATIONS.md →
# App Review demo account). Rotate it with
#   terraform apply -replace=random_password.demo
# then run scripts/seed_demo.sh again and update App Store Connect.
resource "random_password" "demo" {
  length  = 24
  special = false
}

resource "vault_mount" "operator" {
  namespace   = vault_namespace.project.path_fq
  path        = "operator"
  type        = "kv"
  options     = { version = "2" }
  description = "Operator-only secrets (no workload policy)"
}

resource "vault_kv_secret_v2" "demo" {
  namespace           = vault_namespace.project.path_fq
  mount               = vault_mount.operator.path
  name                = "demo"
  delete_all_versions = true
  data_json           = jsonencode({ email = var.demo_email, password = random_password.demo.result })
}
