# Policy terraform on the in-cluster Vault: what infra/vault's Vault provider may do (k8s.tf, D191, D207).
#
# GENERATED from bootstrap/terraform.hcl.tftpl with the services of var.services (bootstrap.tf): edit the template,
# then render it from a plan, not an apply (output bootstrap_terraform_policy; the commands and the order of commit,
# `vault policy write` and apply are in infra/vault/README.md -> bootstrap/). tests/vault.tftest.hcl and
# check.bootstrap_terraform_policy_matches_services pin the two equal.
# Written into Vault by hand, never by Terraform, so this root cannot rewrite its own token's policy:
#   vault policy write terraform infra/vault/bootstrap/terraform.hcl
# The token comes from the token role terraform (bootstrap/token-role-terraform.json): 1 hour, not renewable,
# usable from 127.0.0.1 only (the port-forward), a child of the session's admin token (token role admin, D209).
#
# What this token is: admin-equivalent for its hour, not a sandbox. It reads every credential (read on the KV data
# is needed: the provider reads each secret back), and it rewrites the policy of any role it manages (the services'
# and the snapshot job's), which a Kubernetes token for that role's service account (anyone with cluster access)
# then turns into those rights. What the lines below take away is the cheap paths around that:
#   - Exact paths only, no globs on policies, roles or mounts. Vault matches ACL paths case-sensitively but
#     lower-cases and trims a policy's name when it writes it, so a glob with a few denies would let `ADMIN`,
#     `Terraform` or `default ` overwrite the protected policies. With exact paths only the names below exist.
#   - A role may carry only its own policy and trust only its own namespace and service account
#     (allowed_parameters; Vault compares a list element by element with the exact allowed value, without
#     lower-casing or trimming: ["API"] and ["api "] are refused as well as ["admin"]).
#   - The auth mount stays type kubernetes, and its config keeps the in-cluster API server, so the login cannot be
#     re-pointed at a TokenReview server outside the cluster.
#   - A tune may not set the keys that switch audit HMAC off (audit_non_hmac_*: the values would reach both audit
#     devices and Grafana Cloud in clear) or expose headers and the mount in the UI. A mount's creation carries the
#     same keys inside `config`, which ACL parameters cannot reach: tests/vault.tftest.hcl pins that k8s.tf sets
#     none, and docs/OPERATIONS.md -> Vault -> Day 2 reads every tune back. The auth mount's tune also refuses
#     token_type: as batch, the tokens of every role that sets no token_type of its own could not be revoked.
#   - Nothing on sys/audit, sys/config/auditing, sys/seal, sys/storage/raft, sys/generate-root, sys/rekey,
#     sys/auth/oidc, auth/oidc, auth/token (beyond the default policy's own-token paths), identity/*.
# Never attach policies to an entity or a group (identity/*): admin comes from the token role admin only.
#
# Which endpoints need sudo on 2.2.1, and whether a line is missing, the kind rehearsal settles: it applies
# infra/vault with exactly this policy, and every 403 there is a missing line (design Appendix B #2).

# Secret mounts: kv-<svc> per service and kv-operator (vault_mount, vault_kv_secret_v2 with delete_all_versions).
path "sys/mounts" {
  capabilities = ["read"]
}
path "sys/mounts/kv-api" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-api/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-api/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-api/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-ingest" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-ingest/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-ingest/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-ingest/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-migrate" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-migrate/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-migrate/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-migrate/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-monitoring" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-monitoring/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-monitoring/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-monitoring/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-notify" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-notify/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-notify/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-notify/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-rabbitmq" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-rabbitmq/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-rabbitmq/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-rabbitmq/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-scoring" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-scoring/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-scoring/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-scoring/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-smoke" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-smoke/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-smoke/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-smoke/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-whoop-import" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-whoop-import/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-whoop-import/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-whoop-import/metadata/*" {
  capabilities = ["read", "delete", "list"]
}
path "sys/mounts/kv-operator" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
}
path "sys/mounts/kv-operator/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
  }
}
path "kv-operator/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "kv-operator/metadata/*" {
  capabilities = ["read", "delete", "list"]
}

# The one Kubernetes auth mount (vault_auth_backend, vault_kubernetes_auth_backend_config). The provider creates
# and deletes it at sys/auth/kubernetes but reads and tunes it at sys/mounts/auth/kubernetes (provider 5.x).
path "sys/auth" {
  capabilities = ["read"]
}
path "sys/auth/kubernetes" {
  capabilities = ["create", "read", "update", "delete", "sudo"]
  allowed_parameters = {
    type = ["kubernetes"]
    "*"  = []
  }
}
path "sys/mounts/auth/kubernetes" {
  capabilities = ["read"]
}
path "sys/mounts/auth/kubernetes/tune" {
  capabilities = ["read", "update"]
  denied_parameters = {
    audit_non_hmac_request_keys  = []
    audit_non_hmac_response_keys = []
    passthrough_request_headers  = []
    allowed_response_headers     = []
    listing_visibility           = []
    token_type                   = []
  }
}
path "auth/kubernetes/config" {
  capabilities = ["create", "read", "update"]
  allowed_parameters = {
    kubernetes_host = ["https://kubernetes.default.svc"]
    "*"             = []
  }
}

# One role per service and the snapshot job's (vault_kubernetes_auth_backend_role): its own policy, namespace and
# service account only.
path "auth/kubernetes/role" {
  capabilities = ["list"]
}
path "auth/kubernetes/role/api" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["api"]
    policies                                 = ["api"]
    bound_service_account_names              = ["api"]
    bound_service_account_namespaces         = ["ryft-api"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/ingest" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["ingest"]
    policies                                 = ["ingest"]
    bound_service_account_names              = ["ingest"]
    bound_service_account_namespaces         = ["ryft-ingest"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/migrate" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["migrate"]
    policies                                 = ["migrate"]
    bound_service_account_names              = ["migrate"]
    bound_service_account_namespaces         = ["ryft-migrate"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/monitoring" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["monitoring"]
    policies                                 = ["monitoring"]
    bound_service_account_names              = ["monitoring"]
    bound_service_account_namespaces         = ["monitoring"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/notify" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["notify"]
    policies                                 = ["notify"]
    bound_service_account_names              = ["notify"]
    bound_service_account_namespaces         = ["ryft-notify"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/rabbitmq" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["rabbitmq"]
    policies                                 = ["rabbitmq"]
    bound_service_account_names              = ["rabbitmq"]
    bound_service_account_namespaces         = ["ryft-rabbitmq"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/scoring" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["scoring"]
    policies                                 = ["scoring"]
    bound_service_account_names              = ["scoring"]
    bound_service_account_namespaces         = ["ryft-scoring"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/smoke" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["smoke"]
    policies                                 = ["smoke"]
    bound_service_account_names              = ["smoke"]
    bound_service_account_namespaces         = ["ryft-smoke"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/whoop-import" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["whoop-import"]
    policies                                 = ["whoop-import"]
    bound_service_account_names              = ["whoop-import"]
    bound_service_account_namespaces         = ["ryft-whoop-import"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}
path "auth/kubernetes/role/vault-snapshot" {
  capabilities = ["create", "read", "update", "delete"]
  allowed_parameters = {
    token_policies                           = ["snapshot"]
    policies                                 = ["snapshot"]
    bound_service_account_names              = ["vault-snapshot"]
    bound_service_account_namespaces         = ["vault"]
    bound_service_account_namespace_selector = [""]
    "*"                                      = []
  }
}

# One policy per service and the snapshot job's (vault_policy); no other name, so never admin, terraform or
# default in any spelling.
path "sys/policies/acl" {
  capabilities = ["list"]
}
path "sys/policies/acl/api" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/ingest" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/migrate" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/monitoring" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/notify" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/rabbitmq" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/scoring" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/smoke" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/whoop-import" {
  capabilities = ["create", "read", "update", "delete"]
}
path "sys/policies/acl/snapshot" {
  capabilities = ["create", "read", "update", "delete"]
}
