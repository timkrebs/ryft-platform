# The bootstrap's hand-written files (bootstrap/, docs/OPERATIONS.md -> Vault -> Bootstrap): the policies admin
# and terraform, the token role terraform and the OIDC login. Nothing in this root writes them to Vault (so the
# terraform token cannot rewrite its own policy or reach the operator's login); they are read here so that every
# plan checks them, and tests/vault.tftest.hcl holds them to an exact allowlist (a test failure, not a warning).
locals {
  bootstrap = {
    oidc_config          = jsondecode(file("${path.module}/bootstrap/oidc-config.json"))
    oidc_role_admin      = jsondecode(file("${path.module}/bootstrap/oidc-role-admin.json"))
    oidc_role_enroll     = jsondecode(file("${path.module}/bootstrap/oidc-role-enroll.json"))
    token_role_terraform = jsondecode(file("${path.module}/bootstrap/token-role-terraform.json"))
    terraform_policy     = file("${path.module}/bootstrap/terraform.hcl")
    admin_policy         = file("${path.module}/bootstrap/admin.hcl")
  }

  # The terraform policy names every managed object by its exact path (no globs: Vault matches ACL paths
  # case-sensitively but canonicalises policy names on write, the template's header), so it follows the services.
  bootstrap_terraform_policy_rendered = templatefile("${path.module}/bootstrap/terraform.hcl.tftpl", {
    mounts   = concat([for name in sort(keys(local.services)) : local.k8s_kv_mount[name]], ["kv-operator"])
    services = [for name in sort(keys(local.services)) : { name = name, service_account = local.services[name].service_account, k8s_ns = local.services[name].k8s_ns }]
  })

  # The policies as the tests read them: every `path "..."` wherever it stands (indented or not), and each block's
  # capabilities and body. A block is `path "<p>" {` at the start of a line up to the next `}` at the start of one.
  bootstrap_policy_paths = {
    for k, text in { terraform = local.bootstrap.terraform_policy, admin = local.bootstrap.admin_policy } :
    k => [for m in regexall("(?m)^[ \\t]*path[ \\t]+\"([^\"]*)\"", text) : m[0]]
  }
  bootstrap_policy_blocks = {
    for k, text in { terraform = local.bootstrap.terraform_policy, admin = local.bootstrap.admin_policy } :
    k => { for m in regexall("(?ms)^path \"([^\"]*)\" \\{\\n(.*?)^\\}$", text) : m[0] => m[1] }
  }
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

check "bootstrap_terraform_policy_matches_services" {
  assert {
    condition     = local.bootstrap.terraform_policy == local.bootstrap_terraform_policy_rendered
    error_message = "bootstrap/terraform.hcl is not the render of bootstrap/terraform.hcl.tftpl for these services. After the apply: terraform -chdir=infra/vault output -raw bootstrap_terraform_policy > infra/vault/bootstrap/terraform.hcl, review and commit it, then `vault policy write terraform infra/vault/bootstrap/terraform.hcl` with the admin login (docs/OPERATIONS.md -> Vault). Until then the terraform token cannot manage the new service."
  }
}

check "bootstrap_policies_have_one_block_per_path" {
  assert {
    condition = alltrue([for k in ["terraform", "admin"] :
      length(local.bootstrap_policy_paths[k]) == length(keys(local.bootstrap_policy_blocks[k])) &&
      length(local.bootstrap_policy_paths[k]) == length(toset(local.bootstrap_policy_paths[k]))
    ])
    error_message = "bootstrap/terraform.hcl or bootstrap/admin.hcl has a `path` that does not open its own block at the start of a line, or a path twice: the tests' allowlist would not see it the way Vault does."
  }
}
