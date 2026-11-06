# Policy admin on the in-cluster Vault: the operator's identity (OIDC role admin, Google Workspace ryftapp.de,
# D193). It replaces the root token, which the bootstrap revokes, so every administrative request is audited under
# the operator's pseudonymous OIDC subject instead of an anonymous root token.
#
# Written by hand at the bootstrap and after a reviewed change of this file, never by Terraform (the terraform
# policy denies this name):
#   vault policy write admin infra/vault/bootstrap/admin.hcl
# docs/OPERATIONS.md -> Vault -> Bootstrap.
#
# Everything: it is the repair path for the OIDC configuration, the audit devices and the policies themselves. Its
# tokens last 1 hour (8 at most), are created only by the OIDC login of one Google account, and are used for
# runbooks and to create the 1-hour terraform token.
path "*" {
  capabilities = ["create", "read", "update", "patch", "delete", "list", "sudo"]
}
