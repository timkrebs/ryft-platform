# The bootstrap's hand-written files (bootstrap/, docs/OPERATIONS.md -> Vault -> Bootstrap): the policies admin
# and terraform and the OIDC login. Nothing in this root writes them to Vault (so the terraform token cannot widen
# itself or reach the operator's login); they are read here only so that a change to one is checked at every plan
# and pinned by tests/vault.tftest.hcl.
locals {
  bootstrap = {
    oidc_config      = jsondecode(file("${path.module}/bootstrap/oidc-config.json"))
    oidc_role_admin  = jsondecode(file("${path.module}/bootstrap/oidc-role-admin.json"))
    oidc_role_enroll = jsondecode(file("${path.module}/bootstrap/oidc-role-enroll.json"))
    terraform_policy = file("${path.module}/bootstrap/terraform.hcl")
    admin_policy     = file("${path.module}/bootstrap/admin.hcl")
  }

  # What the terraform token must never reach (design 2.3): the audit devices, the seal, Raft, generate-root and
  # rekey, the OIDC login, identities, other tokens; and no catch-all path.
  bootstrap_terraform_forbidden = "(?m)^path \"(sys/audit|sys/config|sys/seal|sys/unseal|sys/storage|sys/raw|sys/generate-root|sys/rekey|sys/replication|sys/auth/oidc|auth/oidc|auth/token|identity|\\*|\\+/\\*)"
}

# The operator's login puts a pseudonymous id into every audit line, never an email or a name (D198): those lines
# leave the cluster (Grafana Cloud, addendum-1 section 1.7).
check "bootstrap_oidc_roles_are_pseudonymous" {
  assert {
    condition = alltrue([for r in [local.bootstrap.oidc_role_admin, local.bootstrap.oidc_role_enroll] :
      r.role_type == "oidc" && r.user_claim == "sub" && r.verbose_oidc_logging == false && length(r.oidc_scopes) == 0 &&
      length(setintersection(keys(r), ["claim_mappings", "groups_claim", "user_claim_json_pointer"])) == 0
    ])
    error_message = "bootstrap/oidc-role-*.json must keep user_claim = \"sub\", no claim_mappings or groups_claim, no extra oidc_scopes and verbose_oidc_logging = false: otherwise the operator's email or name reaches every audit line (and Grafana Cloud)."
  }
}

check "bootstrap_terraform_policy_is_bounded" {
  assert {
    condition = (
      length(regexall(local.bootstrap_terraform_forbidden, local.bootstrap.terraform_policy)) == 0 &&
      alltrue([for p in ["admin", "terraform", "default"] :
      strcontains(local.bootstrap.terraform_policy, "path \"sys/policies/acl/${p}\" {\n  capabilities = [\"deny\"]\n}")])
    )
    error_message = "bootstrap/terraform.hcl may not grant audit, seal, Raft, generate-root, rekey, the OIDC login, identities, tokens or a catch-all path, and must deny the policies admin, terraform and default."
  }
}
