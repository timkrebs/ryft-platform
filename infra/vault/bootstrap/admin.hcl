# Policy admin on the in-cluster Vault: the operator's session token (D209). A session starts with
# `vault operator generate-root` and three unseal key shares; that root token creates one token of the token role
# admin (bootstrap/token-role-admin.json: this policy only, 1 hour, not renewable, an orphan, usable through the
# port-forward only) and is revoked at once, so every administrative request after that is audited under the admin
# token's accessor instead of a root token (docs/OPERATIONS.md -> Vault -> Login).
#
# Written by hand at the bootstrap and after a reviewed change of this file, never by Terraform (the terraform
# policy denies this name):
#   vault policy write admin infra/vault/bootstrap/admin.hcl
# docs/OPERATIONS.md -> Vault -> Bootstrap.
#
# Everything: it is the repair path for the audit devices, the token roles and the policies themselves, and it
# creates the 1-hour terraform token (token role terraform). Never attach it, or any policy, to an identity entity
# or group: the token role admin is the only way to it, so deleting that role (and revoking its tokens) revokes it.
path "*" {
  capabilities = ["create", "read", "update", "patch", "delete", "list", "sudo"]
}
