# The bootstrap's hand-written files (bootstrap/, docs/OPERATIONS.md -> Vault -> Bootstrap): the policies admin
# and terraform and the token roles admin and terraform. Nothing in this root writes them to Vault (so the terraform
# token cannot rewrite its own policy or mint an admin token); they are read here so that every plan checks them,
# and tests/vault.tftest.hcl holds them to an exact allowlist (a test failure, not a warning).
#
# The operator's login is a generate-root session (D209): three unseal key shares, a root token that creates one
# token of the token role admin and is revoked at once. The OIDC files (oidc-*.json) are the documented later
# option, written nowhere in the rebuild: they are the Google Workspace version (D193, bound to hd = ryftapp.de,
# and Google Workspace is gone), so a personal Google account needs a reviewed change of them first
# (infra/vault/README.md -> bootstrap/). They stay pinned so that change is a deliberate one.
locals {
  bootstrap = {
    oidc_config          = jsondecode(file("${path.module}/bootstrap/oidc-config.json"))
    oidc_role_admin      = jsondecode(file("${path.module}/bootstrap/oidc-role-admin.json"))
    oidc_role_enroll     = jsondecode(file("${path.module}/bootstrap/oidc-role-enroll.json"))
    token_role_admin     = jsondecode(file("${path.module}/bootstrap/token-role-admin.json"))
    token_role_terraform = jsondecode(file("${path.module}/bootstrap/token-role-terraform.json"))
    terraform_policy     = file("${path.module}/bootstrap/terraform.hcl")
    admin_policy         = file("${path.module}/bootstrap/admin.hcl")
  }

  # The terraform policy names every managed object by its exact path (no globs: Vault matches ACL paths
  # case-sensitively but canonicalises policy names on write, the template's header), so it follows the services
  # and the snapshot job's role and policy (k8s.tf).
  bootstrap_terraform_policy_rendered = templatefile("${path.module}/bootstrap/terraform.hcl.tftpl", {
    mounts = concat([for name in sort(keys(local.services)) : local.k8s_kv_mount[name]], ["kv-operator"])
    roles = concat(
      [for name in sort(keys(local.services)) : { name = name, policy = name, service_account = local.services[name].service_account, k8s_ns = local.services[name].k8s_ns }],
      [local.k8s_snapshot],
    )
    policies = concat(sort(keys(local.services)), [local.k8s_snapshot.policy])
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
  # What Vault parses but the two maps above do not see: the text without its `#` comment lines and without every
  # matched block (must be whitespace only), and the text without its comment lines and quoted strings (must hold
  # no /* */ or // comment and must not be JSON).
  bootstrap_policy_residue = {
    for k, text in { terraform = local.bootstrap.terraform_policy, admin = local.bootstrap.admin_policy } :
    k => replace(replace(text, "/(?m)^[ \\t]*#.*$/", ""), "/(?ms)^path \"[^\"]*\" \\{\\n.*?^\\}$/", "")
  }
  bootstrap_policy_code = {
    for k, text in { terraform = local.bootstrap.terraform_policy, admin = local.bootstrap.admin_policy } :
    k => replace(replace(text, "/(?m)^[ \\t]*#.*$/", ""), "/\"[^\"\\n]*\"/", "")
  }
}

# If OIDC comes back (the later option above), the login puts a pseudonymous id into every audit line, never an
# email or a name (D198): those lines leave the cluster (Grafana Cloud, addendum-1 section 1.7).
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
    error_message = "bootstrap/terraform.hcl is not the render of bootstrap/terraform.hcl.tftpl for these services. Render it from a plan, no apply: terraform -chdir=infra/vault plan -out=tfplan && terraform -chdir=infra/vault show -json tfplan | jq -r '.planned_values.outputs.bootstrap_terraform_policy.value' > infra/vault/bootstrap/terraform.hcl, then commit it. A new service: merge, `vault policy write terraform infra/vault/bootstrap/terraform.hcl` with the admin token, THEN apply (otherwise the apply stops on a 403 halfway). A removed service: apply first, then write the narrower policy (infra/vault/README.md -> bootstrap/)."
  }
}

check "bootstrap_policies_have_one_block_per_path" {
  assert {
    condition = alltrue([for k in ["terraform", "admin"] :
      length(local.bootstrap_policy_paths[k]) == length(keys(local.bootstrap_policy_blocks[k])) &&
      length(local.bootstrap_policy_paths[k]) == length(toset(local.bootstrap_policy_paths[k])) &&
      trimspace(local.bootstrap_policy_residue[k]) == "" &&
      length(regexall("/\\*|\\*/|//", local.bootstrap_policy_code[k])) == 0 && !can(regex("^\\s*[\\{\\[]", local.bootstrap_policy_code[k]))
    ])
    error_message = "bootstrap/terraform.hcl or bootstrap/admin.hcl has a `path` that does not open its own block at the start of a line, a path twice, anything besides `#` comment lines and such blocks, a /* */ or // comment, or is JSON: the tests' allowlist would not see it the way Vault does."
  }
}
