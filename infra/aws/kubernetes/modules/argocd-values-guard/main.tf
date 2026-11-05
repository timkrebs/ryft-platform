# What the Argo CD chart's values may and may not carry (T12-f, D120). The ClusterRoles' rules have
# their own allowlist in tests/addons.tftest.hcl; this module is for everything ELSE that decides
# what Argo CD's identities can do and that would fail silently:
#
#   - extraObjects ships arbitrary manifests with the release: only the kinds listed below. A
#     ClusterRoleBinding to cluster-admin there would pass every rule check on the two ClusterRoles.
#   - controller.roleRules (a Role in `argocd`, may hold `*`), createClusterRoles = false (the chart
#     renders no ClusterRole, so the rules we narrowed vanish and every check on them passes for
#     want of an object), configs.rbac (policy.default role:admin for every login), anonymous login
#     and server.disable.auth.
#   - any value key the chart has that this file does not expect: a key is added on purpose, here. The
#     allowlist is two levels deep, plus the keys of the maps listed in `allowed_nested` and configs.cm;
#     below that nothing is looked at.
#   - extraObjects is rendered by the chart with `tpl`: a `{{` in any of them is refused, and each allowed
#     kind has its one apiVersion (the kind run also renders templates/extra-manifests.yaml and counts).
#
# argocd.tf calls it with local.argocd_values, and a precondition of helm_release.argocd turns a violation
# into an error of plan and apply; tests/addons.tftest.hcl asserts the real values are clean and that each
# canned variant listed there is caught (a variant nobody wrote down is not tested for). It reads values
# only: no resource, no provider.

terraform {
  required_version = "1.16.6"
}

variable "values" {
  description = "The Argo CD chart's values as an object (local.argocd_values)."
  type        = any
}

locals {
  v = var.values

  allowed_extra_object_kinds = ["NetworkPolicy", "Middleware"]
  # Value keys that exist and are expected, per level. Anything else is a violation.
  allowed_top_level = ["global", "dex", "notifications", "configs", "controller", "server", "repoServer", "applicationSet", "redis", "extraObjects"]
  allowed_keys = {
    global         = ["domain", "image", "logging", "networkPolicy"]
    dex            = ["enabled"]
    notifications  = ["enabled"]
    configs        = ["params", "cm", "secret"]
    controller     = ["clusterRoleRules", "replicas", "podAnnotations", "resources"]
    server         = ["clusterRoleRules", "replicas", "podAnnotations", "resources", "ingress"]
    repoServer     = ["replicas", "podAnnotations", "resources"]
    applicationSet = ["replicas", "resources"]
    redis          = ["resources"]
  }

  # The one apiVersion each allowed kind may carry.
  extra_object_api_versions = {
    NetworkPolicy = "networking.k8s.io/v1"
    Middleware    = "traefik.io/v1alpha1"
  }

  # Below the second level only the places that decide something are listed (the keys of these maps,
  # and for configs.cm the three settings and the health customizations).
  nested = {
    "global.image"               = try(local.v.global.image, {})
    "global.logging"             = try(local.v.global.logging, {})
    "global.networkPolicy"       = try(local.v.global.networkPolicy, {})
    "configs.params"             = try(local.v.configs.params, {})
    "configs.secret"             = try(local.v.configs.secret, {})
    "server.ingress"             = try(local.v.server.ingress, {})
    "server.ingress.annotations" = try(local.v.server.ingress.annotations, {})
  }
  allowed_nested = {
    "global.image"         = ["tag"]
    "global.logging"       = ["format"]
    "global.networkPolicy" = ["create"]
    "configs.params"       = ["server.insecure"]
    "configs.secret"       = ["argocdServerAdminPassword", "argocdServerAdminPasswordMtime"]
    "server.ingress"       = ["enabled", "ingressClassName", "tls", "annotations"]
    "server.ingress.annotations" = [
      "cert-manager.io/cluster-issuer",
      "traefik.ingress.kubernetes.io/router.entrypoints",
      "traefik.ingress.kubernetes.io/router.tls",
      "traefik.ingress.kubernetes.io/router.middlewares",
    ]
  }
  allowed_cm_keys = ["admin.enabled", "exec.enabled", "resource.respectRBAC"]

  # A tuple of objects of different shapes: iterate it as it is (tolist would fail on it).
  extra_objects = try(local.v.extraObjects, [])

  violations = concat(
    flatten([for path, allowed in local.allowed_nested :
      [for k in try(keys(local.nested[path]), []) : "unexpected value \"${path}.${k}\"" if !contains(allowed, k)]
    ]),
    [for k in try(keys(local.v.configs.cm), []) : "unexpected value \"configs.cm.${k}\"" if !contains(local.allowed_cm_keys, k) && !startswith(k, "resource.customizations.health.")],
    try(local.v.global.networkPolicy.create, true) != true ? ["global.networkPolicy.create is not true: the chart's NetworkPolicies are off"] : [],
    # The chart renders every extraObject with `tpl`: a template expression in any field is code that
    # runs at render time (a base64-decoded document in apiVersion renders a whole other object).
    strcontains(jsonencode(local.extra_objects), "{{") ? ["extraObjects carry a template expression ({{): the chart renders them with tpl, so a field can produce any object"] : [],
    [for i, o in local.extra_objects :
      "extraObjects[${i}]: apiVersion \"${try(o.apiVersion, "")}\" is not ${local.extra_object_api_versions[o.kind]} (the one apiVersion of a ${o.kind})"
      if contains(keys(local.extra_object_api_versions), try(o.kind, "")) && try(o.apiVersion, "") != local.extra_object_api_versions[o.kind]
    ],
    [for k in keys(local.v) : "unexpected top-level value \"${k}\" (createAggregateRoles, rbac, crds, ... change who may do what)" if !contains(local.allowed_top_level, k)],
    flatten([for section, allowed in local.allowed_keys :
      [for k in try(keys(local.v[section]), []) : "unexpected value \"${section}.${k}\"" if !contains(allowed, k)]
    ]),
    [for i, o in local.extra_objects :
      "extraObjects[${i}]: kind \"${try(o.kind, "")}\" is not one of ${join(", ", local.allowed_extra_object_kinds)}"
      if !contains(local.allowed_extra_object_kinds, try(o.kind, ""))
    ],
    [for i, o in local.extra_objects :
      "extraObjects[${i}]: namespace \"${try(o.metadata.namespace, "")}\" is not argocd"
      if try(o.metadata.namespace, "") != "argocd"
    ],
    # Explicit, so the message names the danger (the key lists above catch them as well).
    can(local.v.controller.roleRules) ? ["controller.roleRules is set: a Role in argocd with rules of our own (the chart's default is none)"] : [],
    can(local.v.server.roleRules) ? ["server.roleRules is set"] : [],
    try(local.v.createClusterRoles, true) == false ? ["createClusterRoles is false: the chart renders no ClusterRole, so the narrowed rules do not exist"] : [],
    try(local.v.controller.clusterRoleRules.enabled, false) != true ? ["controller.clusterRoleRules is not enabled: the chart's wildcard ClusterRole applies"] : [],
    try(local.v.server.clusterRoleRules.enabled, false) != true ? ["server.clusterRoleRules is not enabled: the chart's ClusterRole applies"] : [],
    can(local.v.configs.rbac) ? ["configs.rbac is set: Argo CD's own RBAC (policy.default, policy.csv) is not changed here"] : [],
    can(local.v.configs.cm["users.anonymous.enabled"]) ? ["configs.cm users.anonymous.enabled is set: no anonymous login on the public UI (D103)"] : [],
    can(local.v.configs.params["server.disable.auth"]) ? ["configs.params server.disable.auth is set: no authentication on the public UI"] : [],
  )
}

output "violations" {
  description = "One line per thing the values carry that they must not; empty when clean."
  value       = local.violations
}
