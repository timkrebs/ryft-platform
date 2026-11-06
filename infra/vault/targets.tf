# Which Vault this root writes, and which Vault each namespace's operator objects read (D191).
#
# Two Vaults during the migration: HCP Vault Dedicated (the default provider, every namespace reads it today) and
# Vault Community Edition in the cluster (provider vault.k8s, reached through the operator's port-forward, k8s.tf).
# The switches are committed locals, not tfvars: tfvars are the operator's secret file and nobody reviews them,
# while a change here is a reviewed PR, so git is the record of which namespace reads which Vault.
#
#   committed_write_k8s   false until PR 3b ("vault: write the in-cluster Vault"), after the bootstrap
#                         (docs/OPERATIONS.md -> Vault -> Bootstrap). From then on every apply writes BOTH Vaults
#                         with the same values (dual-write until the decommission, so a rollback to HCP never hands
#                         out a stale credential).
#   committed_vso_on_k8s  the services whose generated VaultConnection, VaultAuth and VaultStaticSecret read the
#                         in-cluster Vault; one name more per cutover PR (5a .. 5i). Requires write_k8s.
#
# Both preconditions are on local_file.vso (vso.tf): a name that is no service, or a name while write_k8s is false,
# fails plan and apply.
locals {
  committed_write_k8s  = false
  committed_vso_on_k8s = toset([])

  write_k8s  = var.test_override_targets == null ? local.committed_write_k8s : var.test_override_targets.write_k8s
  vso_on_k8s = var.test_override_targets == null ? local.committed_vso_on_k8s : toset(var.test_override_targets.vso_on_k8s)

  # What the in-cluster Vault serves (the chart in infra/aws/kubernetes, output vault_address_in_cluster). A local,
  # not a variable: the admission chart's P4 admits exactly this address (vaultAddresses).
  vault_k8s_address_for_cluster = "https://vault.vault.svc:8200"

  # One KV v2 mount per service on the in-cluster Vault (CE has no namespaces: the mount, the role and the policy
  # are the isolation, D187). The generator and k8s.tf read the same names.
  k8s_kv_mount = { for name in keys(var.services) : name => "kv-${name}" }

  # The snapshot CronJob's identity on the in-cluster Vault (charts/vault-extras, k8s.tf): role vault-snapshot,
  # bound to vault/vault-snapshot, carrying policy snapshot. k8s.tf and the terraform policy's render
  # (bootstrap.tf) read these names, so the token can write exactly this role and this policy.
  k8s_snapshot = { name = "vault-snapshot", policy = "snapshot", service_account = "vault-snapshot", k8s_ns = "vault" }

  # Names a service may not have on the in-cluster Vault: policies and roles share the root namespace with Vault's
  # own (default, root), the bootstrap's (admin, terraform) and the snapshot job's (role vault-snapshot, policy
  # snapshot), and kv-operator holds the demo password (variables.tf validates var.services against this list). The
  # admission chart's vso.reservedServices names the same seven names (keep the two lists equal); a kv-snapshot
  # mount cannot exist while no service has that name.
  reserved_service_names = ["default", "root", "admin", "terraform", "vault-snapshot", "snapshot", "operator"]
}

# The override exists for terraform test only (a test cannot set a local). Set anywhere else it takes the switches
# out of git: the plan says so.
check "targets_come_from_git" {
  assert {
    condition     = var.test_override_targets == null
    error_message = "test_override_targets is set: which Vault this root writes and which namespaces read the in-cluster Vault no longer come from targets.tf. It is for terraform test only; remove it from terraform.tfvars and the environment."
  }
}
