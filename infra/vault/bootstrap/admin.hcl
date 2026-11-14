# Policy admin on the in-cluster Vault: the operator's session token (D209). A session starts with
# `vault operator generate-root` and three unseal key shares; that root token creates one token of the token role
# admin (bootstrap/token-role-admin.json: this policy only, 1 hour, not renewable, an orphan, usable through the
# port-forward only) and is revoked at once, so every administrative request after that is audited under the admin
# token's accessor instead of a root token (docs/OPERATIONS.md -> Vault -> Login).
#
# Written by hand with a root token (the bootstrap's, later a generate-root session's) at the bootstrap and after a
# reviewed change of this file, never by Terraform (the terraform policy denies this name) and never by an admin
# token (the policy blocks below are read-only to it):
#   vault policy write admin infra/vault/bootstrap/admin.hcl
# docs/OPERATIONS.md -> Vault -> Bootstrap.
#
# Everything, but nothing that outlives the session or opens another way in: it is the repair path for the audit
# devices, and it creates the 1-hour terraform token (token role terraform, a child that dies with it). Token roles,
# auth methods and policies are a root token's. Never attach it, or any policy, to an identity entity or group: the
# token role admin is the only way to it, so deleting that role (and revoking its tokens) revokes it.
path "*" {
  capabilities = ["create", "read", "update", "patch", "delete", "list", "sudo"]
}

# No token outlives the session. auth/token/create makes an orphan with no_parent (the sudo of "*" allows it), and
# create-orphan is one by definition; an orphan is not revoked with this token.
path "auth/token/create" {
  capabilities = ["deny"]
}

path "auth/token/create-orphan" {
  capabilities = ["deny"]
}

# No token role but terraform: not admin (a fresh orphan admin token, then the next, for ever), in any spelling (ACL
# paths match case-sensitively), and not a role a later root session adds. The exact path below wins over this glob.
path "auth/token/create/*" {
  capabilities = ["deny"]
}

# The terraform token: a child of this token (token role terraform, orphan false), so it dies with the session.
path "auth/token/create/terraform" {
  capabilities = ["create", "update"]
}

# The token roles are the root token's (bootstrap step 6, and a generate-root session for a reviewed change): an
# admin token that could edit role admin or terraform (orphan, TTL, policies, CIDRs) would mint its successor.
path "auth/token/roles/*" {
  capabilities = ["deny"]
}

# No new auth method (userpass, approle, cert, a second kubernetes mount), and none disabled or re-enabled here: a
# login path the session leaves behind is a standing credential. sys/auth (the list) stays readable.
path "sys/auth/*" {
  capabilities = ["deny"]
}

# Every policy read-only, on both endpoints (sys/policies/acl and the older sys/policy): an admin token that could
# write admin (in any spelling: Vault lower-cases a policy name on write, ACL paths match as written), terraform
# (whose token it mints) or default (which every token carries) would lift the denies above for itself. A policy
# is a root token's to write, in a generate-root session; the terraform token writes the services' own
# (bootstrap/terraform.hcl).
path "sys/policies/acl/*" {
  capabilities = ["read", "list"]
}

path "sys/policy/*" {
  capabilities = ["read", "list"]
}
