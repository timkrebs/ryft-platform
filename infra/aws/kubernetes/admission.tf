# Admission policies for ryft-prod-euc1 (D120): eleven ValidatingAdmissionPolicies P1-P11
# and their bindings, from the local chart charts/admission-policy, in kube-system. The
# production values are committed in admission-values.yaml (a reviewed PR, then your apply);
# `enabled: false` there creates nothing. Bindings are exempt from admission, so RBAC guards
# them (T12-f) and the break-glass is yours alone: kubectl patch or delete a binding, then
# restore it with this release (docs/OPERATIONS.md).
#
# The AppProjects that P7 compares against (T12-b) are not wired yet: local.admission_projects
# stays empty until then, so P7 refuses every AppProject write; the release refuses to be
# planned with that binding on deny.

locals {
  admission_file     = yamldecode(file("${path.module}/admission-values.yaml"))
  admission_defaults = yamldecode(file("${path.module}/charts/admission-policy/values.yaml"))

  # The committed flag, unless a plan overrides it (tests; never for an apply).
  admission_enabled = coalesce(var.admission_enabled, try(local.admission_file.enabled, false))

  # Every binding of the chart, and the action each is asked for: the chart's defaults, the
  # committed file, then the variable.
  admission_keys = sort(keys(local.admission_defaults.actions))
  admission_requested = merge(
    local.admission_defaults.actions,
    try(local.admission_file.actions, {}),
    var.admission_actions,
  )

  # What each binding's validationActions end up as. Break-glass: Warn+Audit everywhere.
  # Anything that is not exactly "deny" is Warn+Audit (the safe side), and Deny never meets
  # Warn: the API server refuses that combination. The chart derives the same lists from
  # `actions` and `breakGlass`; the drift check compares the cluster with the chart's render.
  admission_effective = {
    for k, v in local.admission_requested :
    k => (var.admission_break_glass || v != "deny") ? ["Warn", "Audit"] : ["Deny", "Audit"]
  }

  # The decoded deploy/argocd/project.yaml documents P7 compares AppProjects with. T12-b
  # (Terraform-owned AppProjects) wires them; until then P7 has no constant.
  admission_projects = []

  # T12-g's file; operators-cluster on deny is refused without it (P6 refuses the release's
  # cluster-wide roles). The variable lets a test point elsewhere.
  admission_scoped_rbac_file = coalesce(var.admission_scoped_rbac_file, "${path.module}/../../../deploy/rabbitmq/operators/cluster-operator/scoped-rbac.yaml")

  # Netpol's open broker egress rule is admitted while this is true (T12-k closes it).
  admission_broker_egress_public = try(local.admission_file.netpol.brokerEgressPublic, local.admission_defaults.netpol.brokerEgressPublic)

  # The VaultConnections infra/vault generates: every file must hold exactly one address
  # (quotes stripped); a check and a test compare it with admission-values.yaml.
  vault_namespaces_dir = "${path.module}/../../../deploy/k8s/overlays/vault-namespaces"
  vault_address_by_file = {
    for f in fileset(local.vault_namespaces_dir, "*/vault.yaml") :
    f => [for m in regexall("(?m)^\\s+address:\\s*\"?([^\"\\s]+)\"?\\s*$", file("${local.vault_namespaces_dir}/${f}")) : m[0]]
  }
  vault_addresses = distinct(flatten(values(local.vault_address_by_file)))

  # Handed to the chart. The file's keys but `enabled`, with the full action map.
  admission_values = merge(
    { for k, v in local.admission_file : k => v if k != "enabled" },
    {
      actions    = local.admission_requested
      breakGlass = var.admission_break_glass
      vpcCIDR    = var.vpc_cidr
      projects   = local.admission_projects
    },
  )

  # The chart as it renders with exactly those values (offline), as objects: what the
  # cluster should hold. Null documents (comments only) are dropped.
  # (The data sources have count 0 while disabled, so these loops are empty then; a splat of
  # null is an empty list, which keeps the types of the two branches out of it.)
  admission_rendered = [
    for d in flatten([
      for t in data.helm_template.admission : [
        for path, m in t.manifests :
        [for doc in split("\n---\n", m) : try(yamldecode(doc), null)]
      ]
    ]) : d if d != null
  ]
  admission_rendered_policies = { for d in local.admission_rendered : d.metadata.name => d if d.kind == "ValidatingAdmissionPolicy" }
  admission_rendered_bindings = { for d in local.admission_rendered : d.metadata.name => d if d.kind == "ValidatingAdmissionPolicyBinding" }

  # The drift checks compare these. Of a match (a binding's matchResources, a policy's
  # matchConstraints) they keep what the chart sets and what the server defaults: rules and
  # excluded rules with group, version, resource and operation (the defaulted `scope` is
  # dropped), the selectors (absent is {}) and matchPolicy (absent is Equivalent).
  admission_norm = {
    for src, objs in {
      rendered_policies = local.admission_rendered_policies
      rendered_bindings = local.admission_rendered_bindings
      live_policies     = local.admission_live_policies
      live_bindings     = local.admission_live_bindings
      } : src => {
      for n, o in objs : n => jsonencode({
        rules = [for r in try(try(o.spec.matchConstraints, o.spec.matchResources).resourceRules, []) : {
          g = try(r.apiGroups, []), v = try(r.apiVersions, []), r = try(r.resources, []), o = try(r.operations, [])
        }]
        excluded = [for r in try(try(o.spec.matchConstraints, o.spec.matchResources).excludeResourceRules, []) : {
          g = try(r.apiGroups, []), v = try(r.apiVersions, []), r = try(r.resources, []), o = try(r.operations, [])
        }]
        namespaceSelector = jsonencode(try(try(o.spec.matchConstraints, o.spec.matchResources).namespaceSelector, {})) == "null" ? "{}" : jsonencode(try(try(o.spec.matchConstraints, o.spec.matchResources).namespaceSelector, {}))
        objectSelector    = jsonencode(try(try(o.spec.matchConstraints, o.spec.matchResources).objectSelector, {})) == "null" ? "{}" : jsonencode(try(try(o.spec.matchConstraints, o.spec.matchResources).objectSelector, {}))
        matchPolicy       = coalesce(try(try(o.spec.matchConstraints, o.spec.matchResources).matchPolicy, null), "Equivalent")
      })
    }
  }

  # What the cluster holds (null objects when the list returned nothing, or under a mock).
  admission_live_policies = { for o in flatten([for t in data.kubernetes_resources.admission_policies : t.objects[*]]) : o.metadata.name => o }
  admission_live_bindings = { for o in flatten([for t in data.kubernetes_resources.admission_bindings : t.objects[*]]) : o.metadata.name => o }
}

data "helm_template" "admission" {
  count = local.admission_enabled ? 1 : 0

  name      = "admission-policy"
  namespace = "kube-system"
  chart     = "${path.module}/charts/admission-policy"
  values    = [yamlencode(local.admission_values)]
}

data "kubernetes_resources" "admission_policies" {
  count = local.admission_enabled ? 1 : 0

  api_version    = "admissionregistration.k8s.io/v1"
  kind           = "ValidatingAdmissionPolicy"
  label_selector = "app.kubernetes.io/part-of=admission-policy"
}

data "kubernetes_resources" "admission_bindings" {
  count = local.admission_enabled ? 1 : 0

  api_version    = "admissionregistration.k8s.io/v1"
  kind           = "ValidatingAdmissionPolicyBinding"
  label_selector = "app.kubernetes.io/part-of=admission-policy"
}

resource "helm_release" "admission_policy" {
  count = local.admission_enabled ? 1 : 0

  name        = "admission-policy"
  namespace   = "kube-system"
  chart       = "${path.module}/charts/admission-policy"
  max_history = 10 # `helm -n kube-system rollback admission-policy` after a stuck upgrade

  values = [yamlencode(local.admission_values)]

  lifecycle {
    # A deny that would refuse every request of its kind. The chart refuses to render these
    # too; here the plan stops before Helm runs.
    precondition {
      condition     = !(contains(local.admission_effective["argocd"], "Deny") && length(local.admission_projects) == 0)
      error_message = "The argocd binding (P7) is set to deny, but the AppProjects it compares with are not wired (T12-b): it would refuse every AppProject write."
    }
    precondition {
      condition     = !(contains(local.admission_effective["operators-cluster"], "Deny") && !fileexists(local.admission_scoped_rbac_file))
      error_message = "The operators-cluster binding (P6) is set to deny, but deploy/rabbitmq/operators/cluster-operator/scoped-rbac.yaml (T12-g) is not in the tree: P6 refuses the vendored cluster-wide ClusterRoles, so rabbitmq-operators would stop syncing. Merge T12-g first."
    }
    precondition {
      condition     = !(contains(local.admission_effective["netpol"], "Deny") && local.admission_broker_egress_public)
      error_message = "The netpol binding (P3) is set to deny while netpol.brokerEgressPublic is true: ryft-rabbitmq's open 443 egress rule would be refused. Close it first (T12-k)."
    }
  }
}

# Misconfiguration that would not fail an apply: an action the chart cannot honour, a Vault
# address that no longer matches what infra/vault generates, a variable standing in for the
# reviewed file.
check "admission_values" {
  assert {
    condition = alltrue([
      for k, v in local.admission_requested :
      contains(local.admission_keys, k) && contains(["audit", "deny"], v)
    ])
    error_message = "admission-values.yaml (or admission_actions) names a binding the chart does not have, or an action that is not audit or deny; anything but deny is applied as Warn+Audit."
  }
}

check "admission_vault_address" {
  assert {
    condition = (
      length(local.vault_address_by_file) > 0 &&
      alltrue([for f, a in local.vault_address_by_file : length(a) == 1]) &&
      length(local.vault_addresses) == 1 && local.vault_addresses[0] == local.admission_file.vaultAddress
    )
    error_message = "vaultAddress in admission-values.yaml differs from the VaultConnections infra/vault generated (deploy/k8s/overlays/vault-namespaces/*/vault.yaml), or a file does not hold exactly one address. P4 would refuse them all (or none)."
  }
}

check "admission_enabled_override" {
  assert {
    condition     = var.admission_enabled == null
    error_message = "admission_enabled is set by variable. The switch is `enabled` in the reviewed admission-values.yaml; do not run an apply with this variable."
  }
}

# Drift: a policy or binding that is missing, was patched, or was left in break-glass by hand.
# Helm does not see a patch; this does (and the AdmissionBindingMissing / Drift alerts). The
# cluster is compared with the chart's own render of the same values: actions, policy name and
# resource rules of each binding; match constraints, conditions, variables and validations of
# each policy. Restore with
#   terraform apply -target=helm_release.admission_policy -replace='helm_release.admission_policy[0]'
# (quoted: zsh globs the brackets; a replace uninstalls every policy for a moment). It also
# warns once, before the first apply after `enabled: true`, that nothing is there yet.
check "admission_binding_drift" {
  assert {
    condition = !local.admission_enabled ? true : (
      length(local.admission_rendered_bindings) == length(local.admission_keys) &&
      length(local.admission_live_bindings) == length(local.admission_rendered_bindings) &&
      alltrue([
        for name, want in local.admission_rendered_bindings : (
          contains(keys(local.admission_live_bindings), name) &&
          want.spec.policyName == local.admission_live_bindings[name].spec.policyName &&
          toset(want.spec.validationActions) == toset(local.admission_live_bindings[name].spec.validationActions) &&
          local.admission_norm.rendered_bindings[name] == local.admission_norm.live_bindings[name]
        )
      ])
    )
    error_message = "The ValidatingAdmissionPolicyBindings in the cluster differ from the chart's render of admission-values.yaml: one is missing, was patched (actions, policy, resource rules, selectors, matchPolicy) or is left in break-glass (or nothing is applied yet, on the first plan after enabling). Restore with terraform apply -target=helm_release.admission_policy -replace='helm_release.admission_policy[0]' (a replace uninstalls every policy for a moment)."
  }
}

check "admission_policy_drift" {
  assert {
    condition = !local.admission_enabled ? true : (
      length(local.admission_rendered_policies) == 11 &&
      length(local.admission_live_policies) == length(local.admission_rendered_policies) &&
      alltrue([
        for name, want in local.admission_rendered_policies : (
          contains(keys(local.admission_live_policies), name) &&
          jsonencode([for v in want.spec.validations : v.expression]) == jsonencode([for v in local.admission_live_policies[name].spec.validations : v.expression]) &&
          jsonencode([for v in try(want.spec.matchConditions, []) : v.expression]) == jsonencode([for v in try(local.admission_live_policies[name].spec.matchConditions, []) : v.expression]) &&
          jsonencode([for v in try(want.spec.variables, []) : [v.name, v.expression]]) == jsonencode([for v in try(local.admission_live_policies[name].spec.variables, []) : [v.name, v.expression]]) &&
          local.admission_norm.rendered_policies[name] == local.admission_norm.live_policies[name] &&
          want.spec.failurePolicy == local.admission_live_policies[name].spec.failurePolicy
        )
      ])
    )
    error_message = "The ValidatingAdmissionPolicies in the cluster differ from the chart's render: one is missing, was edited (expressions, conditions, variables, match rules, selectors, failurePolicy), or an upgrade is not applied. Restore with terraform apply -target=helm_release.admission_policy -replace='helm_release.admission_policy[0]' (a replace uninstalls every policy for a moment)."
  }
}
