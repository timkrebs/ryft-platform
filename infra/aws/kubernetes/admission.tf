# Admission policies for ryft-prod-euc1 (D120): eleven ValidatingAdmissionPolicies P1-P11
# and their bindings, from the local chart charts/admission-policy, in kube-system. The
# production values are committed in admission-values.yaml (a reviewed PR, then your apply);
# `enabled: false` there creates nothing. Bindings are exempt from admission, so RBAC guards
# them (T12-f) and the break-glass is yours alone: kubectl patch or delete a binding, then
# restore it with this release (docs/OPERATIONS.md).
#
# The AppProjects that P7 compares against (T12-b) are not wired yet: local.admission_projects
# stays empty until then, so P7 refuses every AppProject write; keep its binding on audit.

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
  # `actions` and `breakGlass`.
  admission_effective = {
    for k, v in local.admission_requested :
    k => (var.admission_break_glass || v != "deny") ? ["Warn", "Audit"] : ["Deny", "Audit"]
  }

  # The decoded deploy/argocd/project.yaml documents P7 compares AppProjects with. T12-b
  # (Terraform-owned AppProjects) wires them; until then P7 has no constant.
  admission_projects = []

  # The VaultConnections infra/vault generates (a check and a test compare their address).
  vault_namespaces_dir = "${path.module}/../../../deploy/k8s/overlays/vault-namespaces"
  vault_addresses = distinct(flatten([
    for f in fileset(local.vault_namespaces_dir, "*/vault.yaml") :
    [for m in regexall("(?m)^\\s+address:\\s*(\\S+)\\s*$", file("${local.vault_namespaces_dir}/${f}")) : m[0]]
  ]))

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
}

resource "helm_release" "admission_policy" {
  count = local.admission_enabled ? 1 : 0

  name        = "admission-policy"
  namespace   = "kube-system"
  chart       = "${path.module}/charts/admission-policy"
  max_history = 10 # `helm -n kube-system rollback admission-policy` after a stuck upgrade

  values = [yamlencode(local.admission_values)]
}

# Misconfiguration that would not fail an apply: an action the chart cannot honour, a Vault
# address that no longer matches what infra/vault generates, a P7 without its constant.
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
    condition     = length(local.vault_addresses) == 1 && local.vault_addresses[0] == local.admission_file.vaultAddress
    error_message = "vaultAddress in admission-values.yaml differs from the VaultConnections infra/vault generated (deploy/k8s/overlays/vault-namespaces/*/vault.yaml). P4 would refuse them all (or none)."
  }
}

check "admission_projects_wired" {
  assert {
    condition     = !(local.admission_enabled && contains(local.admission_effective["argocd"], "Deny") && length(local.admission_projects) == 0)
    error_message = "P7 (binding argocd) is set to deny, but the AppProjects it compares with are not wired (T12-b): it would refuse every AppProject write."
  }
}

# Drift: a binding patched, deleted or left in break-glass by hand. Helm does not see a
# patch; this does (and the AdmissionBindingMissing / Drift alerts). Restore with
#   terraform apply -target=helm_release.admission_policy -replace=helm_release.admission_policy
# It also warns once, before the first apply after `enabled: true`, that nothing is there yet.
check "admission_binding_drift" {
  data "kubernetes_resources" "bindings" {
    api_version    = "admissionregistration.k8s.io/v1"
    kind           = "ValidatingAdmissionPolicyBinding"
    label_selector = "app.kubernetes.io/part-of=admission-policy"
  }

  assert {
    # A conditional, not &&: Terraform evaluates both operands of &&, and the objects are
    # null when the provider is mocked or the list call returned nothing.
    condition = !local.admission_enabled ? true : (
      data.kubernetes_resources.bindings.objects == null ? false : (
        length(data.kubernetes_resources.bindings.objects) == length(local.admission_keys) &&
        alltrue([
          for o in data.kubernetes_resources.bindings.objects :
          toset(o.spec.validationActions) == toset(try(local.admission_effective[o.metadata.labels["admission.ryft.dev/key"]], []))
        ])
      )
    )
    error_message = "The ValidatingAdmissionPolicyBindings in the cluster differ from admission-values.yaml: one is missing, was patched, or is left in break-glass (not applied yet after enabling, if this is the first plan). Restore with terraform apply -target=helm_release.admission_policy -replace=helm_release.admission_policy."
  }
}
