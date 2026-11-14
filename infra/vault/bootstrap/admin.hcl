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
# Everything for its hour, but nothing that outlives the session or opens another way in. It can do all that the
# blocks below leave: every secret, mount, lease and audit device (it is their repair path), the Raft peers
# (step-down, remove-peer), a tune of any mount (an auth mount's TTLs and token type too: the lifetime of a login
# that exists, never a new one), a snapshot save, and the 1-hour terraform token (token role terraform, a child
# that dies with it). It cannot:
# create any other token; write a token role (or read one), or enable, disable or move an auth method; write a
# policy, a role or the config of an auth method, or an identity entity, group or alias (it reads and lists them);
# restore a snapshot. Each of those decides who logs in later and with which policy, so each is a root token's, in a
# generate-root session. Never attach admin, or any policy, to an identity entity or group with that root token:
# the token role admin is the only way to it, so deleting that role (and revoking its tokens) revokes it.
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
# login path the session leaves behind is a standing credential. sys/auth (the list) stays readable. So does the
# tune under sys/mounts/auth/<m>/tune (sys/auth/<m>/tune is the same endpoint, denied here): TTLs and token type
# of a login that exists, capped by each role's own token_max_ttl, and no new principal.
path "sys/auth/*" {
  capabilities = ["deny"]
}

# No auth method moved either: sys/remount takes auth/kubernetes to a nested path (auth/a/b) as well, where
# auth/+/role/* and auth/+/config below no longer match, so a role written there and moved back would be a login
# no read-only block saw. A secrets mount moves with a root token too (no step needs it).
path "sys/remount" {
  capabilities = ["deny"]
}

# Every auth method's roles read-only (kubernetes, and OIDC if it comes back): a role with token_policies = ["admin"]
# (or any policy) bound to a service account the session can reach is a login that mints that policy after the
# session ends, even with the token role admin deleted. Reading and listing stay (the baseline after every apply).
# The terraform token writes the services' roles and the snapshot job's (bootstrap/terraform.hcl), a root token
# any other.
path "auth/+/role/*" {
  capabilities = ["read", "list"]
}

# Every auth method's config read-only: auth/kubernetes/config names the API server, its CA and the token reviewer,
# so another kubernetes_host (a cluster someone else controls) would vouch for that cluster's tokens on every role.
# The baseline reads kubernetes_host back. The terraform token writes it (in-cluster only), a root token any other.
path "auth/+/config" {
  capabilities = ["read"]
}

# Identity read-only: an entity or a group with policies (["admin"], or any), or an alias that ties one to a login
# that exists (a service account's kubernetes login), hands those policies to every later token of that login.
# Reading and listing stay. Nothing in the rebuild writes identity; a change is a root token's.
path "identity/*" {
  capabilities = ["read", "list"]
}

# Every policy read-only, on both endpoints (sys/policies/acl and the older sys/policy): an admin token that could
# write admin (in any spelling: Vault lower-cases a policy name on write, ACL paths match as written), terraform
# (whose token it mints) or default (which every token carries) would lift every block here for itself. A policy
# is a root token's to write, in a generate-root session; the terraform token writes the services' own
# (bootstrap/terraform.hcl).
path "sys/policies/acl/*" {
  capabilities = ["read", "list"]
}

path "sys/policy/*" {
  capabilities = ["read", "list"]
}

# A snapshot save, no restore: snapshot-force loads any Vault's snapshot (one whose shares someone else holds leaves
# this Vault sealed to ours and open to theirs), and a plain restore brings back every role, identity object and
# unexpired token deleted or revoked since it was taken. The disaster recovery restores with the throwaway root
# token of the new cluster's init (docs/OPERATIONS.md -> Vault -> Disaster recovery, step 4); a rollback of this
# cluster is a root token's, in a generate-root session.
path "sys/storage/raft/snapshot" {
  capabilities = ["read"]
}

path "sys/storage/raft/snapshot-force" {
  capabilities = ["deny"]
}
