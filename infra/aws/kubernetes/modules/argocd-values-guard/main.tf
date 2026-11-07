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
#   - the chart renders many values with `tpl`: a `{{` anywhere in the values is refused; a line break in a
#     value starts another YAML document in the render (U+2028, U+2029 and U+0085 are line breaks too): the
#     values are ASCII only, with no control character or escape, except newlines in the health
#     customizations (Lua) of configs.cm; each allowed extraObjects kind has its one apiVersion. The kind run counts the whole
#     render by (kind, namespace) as the backstop for what these rules miss.
#   - values_yaml is what the release must use (its postcondition), so what is checked is what Helm gets.
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

  # The values without the health customizations (the one place that holds multi-line strings).
  v_without_health = merge(local.v, {
    configs = merge(try(local.v.configs, {}), {
      cm = { for k, x in try(local.v.configs.cm, {}) : k => x if !startswith(k, "resource.customizations.health.") }
    })
  })

  # The health customizations alone: Lua, the one place for newlines.
  v_health = { for k, x in try(local.v.configs.cm, {}) : k => x if startswith(k, "resource.customizations.health.") }

  # What jsonencode writes for a character that YAML reads as a line break or a control: \n \r \t \f \b,
  # every \u00XX control, U+2028 and U+2029 (jsonencode writes those two as escapes). U+0085 and the rest
  # of non-ASCII come out raw, so any non-ASCII is refused as well. A literal backslash is written as two
  # by jsonencode, so a false positive fails closed.
  escape_pattern_all    = "\\\\(?:[nrtfb]|u00[01][0-9a-fA-F]|u202[89])"
  escape_pattern_health = "\\\\(?:[rtfb]|u00[01][0-9a-fA-F]|u202[89])"

  # A tuple of objects of different shapes: iterate it as it is (tolist would fail on it).
  extra_objects = try(local.v.extraObjects, [])

  violations = concat(
    flatten([for path, allowed in local.allowed_nested :
      [for k in try(keys(local.nested[path]), []) : "unexpected value \"${path}.${k}\"" if !contains(allowed, k)]
    ]),
    [for k in try(keys(local.v.configs.cm), []) : "unexpected value \"configs.cm.${k}\"" if !contains(local.allowed_cm_keys, k) && !startswith(k, "resource.customizations.health.")],
    try(local.v.global.networkPolicy.create, true) != true ? ["global.networkPolicy.create is not true: the chart's NetworkPolicies are off"] : [],
    # The chart renders many values with `tpl` (extraObjects, ingress annotations, ...): a template
    # expression anywhere in the values is code that runs at render time (a base64-decoded document in an
    # apiVersion renders a whole other object, `lookup` reads the cluster). None is wanted: refuse `{{`
    # in the whole tree. (nonsensitive: the tree holds the admin password's hash; the answer is only yes or no.)
    nonsensitive(strcontains(jsonencode(local.v), "{{")) ? ["the values carry a template expression ({{): the chart renders values with tpl, so a value can produce any object or read the cluster"] : [],
    # A line break inside a value ends it in the rendered YAML and starts another document
    # (ingressClassName, global.domain or global.image.tag with "\n---\n<any object>"), and so do U+2028,
    # U+2029 and U+0085, which the chart writes unquoted. So: ASCII only, no control character or escape,
    # in keys and values alike, except the newlines of the health customizations (Lua) of configs.cm, which
    # the chart writes as block scalars (those pass the same rules otherwise).
    nonsensitive(can(regex("[^\\x00-\\x7f]", jsonencode(local.v)))) ? ["the values carry a non-ASCII character (in a key or a value): U+2028, U+2029 and U+0085 are line breaks in YAML"] : [],
    nonsensitive(can(regex(local.escape_pattern_all, jsonencode(local.v_without_health)))) || nonsensitive(can(regex(local.escape_pattern_health, jsonencode(local.v_health)))) ? ["a value or key holds a control character or an escape of one (line break, tab, U+2028/2029, u00XX), except newlines in the health customizations of configs.cm: it would end the value in the rendered YAML and start another document"] : [],
    # Allowed keys whose values switch the narrowed model off: a terminal into pods from the UI, and a
    # controller that fails on, instead of skipping, the kinds it may not list.
    try(local.v.configs.cm["exec.enabled"], false) != false ? ["configs.cm exec.enabled is not false: no terminal into pods from the UI"] : [],
    try(local.v.configs.cm["resource.respectRBAC"], "strict") != "strict" ? ["configs.cm resource.respectRBAC is not strict"] : [],
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

# For P12's Deny gate (admission.tf, D206): the controller's ClusterRole is the allowlist below, and sync
# impersonation is off. Not a violation (the chart's default role is allowed for a release); a bool, so that
# tests/admission.tftest.hcl can run the real expression through this module with other values.
#
# The allowlist (security review S1 of #121): a rule may hold only the verbs get, list, watch, create, update,
# patch, delete, escalate and bind (no impersonate, deletecollection, approve, sign, ...); escalate and bind
# only in a rule of their own on rbac.authorization.k8s.io with resourceNames, escalate/bind on the four
# operator Roles and bind (not escalate) on the one ClusterRole; nothing on the admission policy kinds; no
# subresource (serviceaccounts/token, pods/exec, .../scale) and no Secrets; non-resource URLs only /metrics
# with get; no "*" inside any string. The names are argocd.tf's (argocd_operator_roles and
# argocd_operator_bound_cluster_roles), repeated here on purpose: a new name is a change of this guard.
locals {
  rbac_verbs                 = ["get", "list", "watch", "create", "update", "patch", "delete", "escalate", "bind"]
  rbac_escalate_bind_roles   = ["rabbitmq-cluster-leader-election-role", "messaging-topology-leader-election-role", "rabbitmq-cluster-operator-role", "messaging-topology-manager-role"]
  rbac_bind_cluster_roles    = ["messaging-topology-metrics-auth-role"]
  rbac_admission_policy_kind = ["validatingadmissionpolicies", "validatingadmissionpolicybindings", "mutatingadmissionpolicies", "mutatingadmissionpolicybindings"]

  controller_rules = try(local.v.controller.clusterRoleRules.rules, [])
  controller_rbac_faults = flatten([
    for i, r in local.controller_rules : [
      for fault in [
        anytrue([for f in ["apiGroups", "resources", "verbs", "resourceNames", "nonResourceURLs"] :
        anytrue([for x in try(tolist(r[f]), []) : strcontains(tostring(x), "*")])]) ? "a wildcard" : "",
        length(setsubtract(try(tolist(r.verbs), []), local.rbac_verbs)) > 0 ? "a verb outside the allowlist: ${join(", ", setsubtract(try(tolist(r.verbs), []), local.rbac_verbs))}" : "",
        length(try(tolist(r.verbs), [])) == 0 ? "no verbs" : "",
        anytrue([for x in try(tolist(r.resources), []) : strcontains(tostring(x), "/")]) ? "a subresource" : "",
        contains(try(tolist(r.resources), []), "secrets") ? "secrets" : "",
        length(setintersection(try(tolist(r.resources), []), local.rbac_admission_policy_kind)) > 0 ? "an admission policy kind" : "",
        # escalate or bind: a rule of its own, by name, on the four Roles (escalate, bind) or the one ClusterRole (bind)
        length(setintersection(try(tolist(r.verbs), []), ["escalate", "bind"])) == 0 ? "" : (
          # (lists compared as joined strings: a list(string) never equals a tuple literal)
          join(",", try(tolist(r.apiGroups), [])) == "rbac.authorization.k8s.io" &&
          length(setsubtract(try(tolist(r.verbs), []), ["escalate", "bind"])) == 0 &&
          length(try(tolist(r.resourceNames), [])) > 0 &&
          (
            (join(",", try(tolist(r.resources), [])) == "roles" && length(setsubtract(try(tolist(r.resourceNames), []), local.rbac_escalate_bind_roles)) == 0) ||
            (join(",", try(tolist(r.resources), [])) == "clusterroles" && join(",", try(tolist(r.verbs), [])) == "bind" && length(setsubtract(try(tolist(r.resourceNames), []), local.rbac_bind_cluster_roles)) == 0)
          ) ? "" : "escalate or bind outside the named operator Roles and the one ClusterRole"
        ),
        # a non-resource rule: /metrics get, nothing else in it
        !can(r.nonResourceURLs) ? "" : (
          join(",", try(tolist(r.nonResourceURLs), [])) == "/metrics" && join(",", try(tolist(r.verbs), [])) == "get" && !can(r.resources) && !can(r.apiGroups)
        ) ? "" : "a non-resource URL other than /metrics get",
      ] : "rules[${i}]: ${fault}" if fault != ""
    ]
  ])
}

output "controller_rbac_faults" {
  description = "Why the controller's ClusterRole is not the allowlist of controller_rbac_narrowed: one line per rule and fault; empty when it is."
  value       = local.controller_rbac_faults
}

output "controller_rbac_narrowed" {
  description = "true when the values render the controller's ClusterRole as the allowlist (see the comment) and leave application.sync.impersonation.enabled off: the condition admission.tf puts on argo-scope: deny."
  value = (
    try(local.v.controller.clusterRoleRules.enabled, false) == true &&
    length(local.controller_rules) > 0 &&
    length(local.controller_rbac_faults) == 0 &&
    tostring(try(local.v.configs.cm["application.sync.impersonation.enabled"], "false")) != "true"
  )
}

output "values_yaml" {
  description = "The values document the release must use: what this module checked, as YAML. helm_release.argocd takes it, and its postcondition requires it to be the release's only values."
  value       = yamlencode(local.v)
  sensitive   = true # the admin password's hash is in it
}
