# Policy terraform on the in-cluster Vault: what infra/vault's provider vault.k8s may do (k8s.tf, D191, D193).
#
# Written by hand at the bootstrap and after a reviewed change of this file, never by Terraform, so this root
# cannot widen its own token:
#   vault policy write terraform infra/vault/bootstrap/terraform.hcl
# The token: `vault token create -policy=terraform -ttl=1h -explicit-max-ttl=1h -display-name=terraform`, a child
# of the operator's admin login (revoking that login revokes it), handed to Terraform as TF_VAR_vault_k8s_token
# (docs/OPERATIONS.md -> Vault -> Terraform through the port-forward).
#
# Bounded, not a sandbox: it can write a policy granting more and bind a role to it, and this root also creates
# Kubernetes service accounts, so it is admin-equivalent through that path. It holds every secret anyway (the
# state). What it cannot do: touch the audit devices, the seal, Raft, generate-root, the OIDC login, identities or
# tokens other than its own, or the policies admin, terraform and default; and it cannot outlive its hour.
#
# Vault's globs: `*` only at the end (a prefix), `+` one whole path segment. So `sys/mounts/kv-*` also covers a
# mount's tune endpoint, and the KV data of every kv-<svc> mount (and of kv-operator, the demo password) is
# `+/data/*`: the only KV v2 mounts on this Vault are the kv-* ones, and no other engine here has data/ paths.
# Which endpoints need sudo on 2.2.1, and whether a line is missing, the kind rehearsal settles: it applies
# infra/vault with exactly this policy, and every 403 there is a missing line (design Appendix B #2).

# Secret mounts kv-<svc> and kv-operator (vault_mount).
path "sys/mounts" {
  capabilities = ["read"]
}
path "sys/mounts/kv-*" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}

# The one Kubernetes auth mount (vault_auth_backend, vault_kubernetes_auth_backend_config and _role).
path "sys/auth" {
  capabilities = ["read"]
}
path "sys/auth/kubernetes" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/auth/kubernetes/tune" {
  capabilities = ["read", "update"]
}
path "auth/kubernetes/config" {
  capabilities = ["create", "read", "update"]
}
path "auth/kubernetes/role/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

# The services' policies and vault-snapshot (vault_policy); never admin, terraform or default.
path "sys/policies/acl" {
  capabilities = ["list"]
}
path "sys/policies/acl/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/admin" {
  capabilities = ["deny"]
}
path "sys/policies/acl/terraform" {
  capabilities = ["deny"]
}
path "sys/policies/acl/default" {
  capabilities = ["deny"]
}

# The secrets (vault_kv_secret_v2 with delete_all_versions).
path "+/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "+/metadata/*" {
  capabilities = ["read", "delete", "list"]
}

# Nothing on sys/audit, sys/config/auditing, sys/seal, sys/storage/raft, sys/generate-root, sys/rekey, auth/oidc,
# sys/auth/oidc, identity/*, auth/token/* beyond the default policy's own-token paths.
