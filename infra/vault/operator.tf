# The App Review demo account's password (scripts/seed_demo.sh, docs/OPERATIONS.md →
# App Review demo account), written to the operator-only mount kv-operator (k8s.tf), which no
# workload's policy names. Read it with `terraform output -raw demo_password`, or with the
# admin token:
#   vault kv get -mount=kv-operator demo
# Rotate it with
#   terraform apply -replace=random_password.demo
# then run scripts/seed_demo.sh again and update App Store Connect.
#
# The rebuild imports the old value into this address before the first apply (D211,
# docs/OPERATIONS.md -> Vault -> The first apply). An import sets length from the value and
# every character-class setting to the provider's default (special = true among them), not
# from this block, and each of them forces a replacement when it differs: ignore_changes keeps
# the imported value. A new password is still generated with these settings (ignore_changes
# applies to updates, not to the create), and a deliberate change of shape is a -replace.
resource "random_password" "demo" {
  length  = 24
  special = false

  lifecycle {
    ignore_changes = [length, special, upper, lower, numeric, min_lower, min_upper, min_numeric, min_special, override_special]
  }
}
