# Unit tests: AWS, Helm and Kubernetes providers are mocked; nothing is created.
#   terraform init && terraform test

variables {
  acme_email = "ops@example.com"
}

mock_provider "aws" {
  mock_data "aws_eks_cluster" {
    defaults = {
      endpoint              = "https://mock.eks.eu-central-1.amazonaws.com"
      certificate_authority = [{ data = "bW9jaw==" }]
      vpc_config            = [{ vpc_id = "vpc-0123456789abcdef0" }]
    }
  }
  mock_data "aws_route53_zone" {
    defaults = {
      zone_id = "Z0123456789ABCDEFGHIJ"
      arn     = "arn:aws:route53:::hostedzone/Z0123456789ABCDEFGHIJ"
      name    = "ryftapp.de"
    }
  }
  mock_data "aws_partition" {
    defaults = {
      partition  = "aws"
      dns_suffix = "amazonaws.com"
    }
  }
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
      arn        = "arn:aws:iam::123456789012:user/terraform"
    }
  }
  mock_data "aws_region" {
    defaults = {
      region = "eu-central-1"
    }
  }
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}"
    }
  }
  mock_resource "aws_iam_role" {
    defaults = {
      arn = "arn:aws:iam::123456789012:role/mock"
    }
  }
  mock_resource "aws_iam_policy" {
    defaults = {
      arn = "arn:aws:iam::123456789012:policy/mock"
    }
  }
}

mock_provider "helm" {}
mock_provider "random" {
  mock_resource "random_password" {
    defaults = { result = "generatedpassword0123456789", bcrypt_hash = "$2a$10$mockbcrypthash" }
  }
}
mock_provider "kubernetes" {}

run "chart_versions_are_pinned" {
  command = plan

  assert {
    condition = (
      helm_release.aws_load_balancer_controller.version == "3.6.0" &&
      helm_release.traefik.version == "42.0.0" &&
      helm_release.cert_manager.version == "v1.21.2" &&
      helm_release.external_dns.version == "1.23.0" &&
      helm_release.vault_secrets_operator.version == "1.6.0" &&
      helm_release.fluent_bit.version == "0.2.0" &&
      helm_release.argocd.version == "10.10.5"
    )
    error_message = "every chart must be pinned to the reviewed version"
  }
}

run "traefik_is_https_only_behind_an_nlb_in_kube_system" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).ports.web.expose.default == false
    error_message = "the plain-HTTP entrypoint must not be exposed"
  }

  assert {
    condition     = yamldecode(helm_release.traefik.values[0]).service.annotations["service.beta.kubernetes.io/aws-load-balancer-type"] == "external"
    error_message = "the Service must be handed to the AWS Load Balancer Controller (NLB)"
  }

  assert {
    condition     = helm_release.traefik.namespace == "kube-system"
    error_message = "the RYFT NetworkPolicy admits Traefik from kube-system only"
  }
}

run "external_dns_manages_only_the_domain" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.external_dns.values[0]).domainFilters == ["ryftapp.de"]
    error_message = "ExternalDNS must be limited to the RYFT zone"
  }

  assert {
    condition     = yamldecode(helm_release.external_dns.values[0]).policy == "sync" && yamldecode(helm_release.external_dns.values[0]).txtOwnerId == "ryft-prod-euc1"
    error_message = "ExternalDNS must own its records via TXT so sync never deletes foreign ones"
  }
}

run "dns01_propagation_is_checked_publicly" {
  command = plan

  assert {
    condition     = contains(yamldecode(helm_release.cert_manager.values[0]).extraArgs, "--dns01-recursive-nameservers-only")
    error_message = "cert-manager must check DNS-01 against public resolvers, not the (caching) VPC resolver"
  }
}

run "issuers_solve_dns01_in_the_zone" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.cluster_issuers.values[0]).hostedZoneID == "Z0123456789ABCDEFGHIJ" && yamldecode(helm_release.cluster_issuers.values[0]).dnsZone == "ryftapp.de"
    error_message = "the ClusterIssuers must solve DNS-01 in the RYFT zone"
  }
}

run "default_storage_class_is_encrypted" {
  command = plan

  assert {
    condition = (
      kubernetes_storage_class_v1.gp3_encrypted.parameters["encrypted"] == "true" &&
      kubernetes_storage_class_v1.gp3_encrypted.metadata[0].annotations["storageclass.kubernetes.io/is-default-class"] == "true" &&
      kubernetes_storage_class_v1.gp3_encrypted.reclaim_policy == "Retain"
    )
    error_message = "the default StorageClass must be encrypted gp3 that retains volumes"
  }
}

run "rejects_invalid_acme_email" {
  command = plan

  variables {
    acme_email = "not-an-email"
  }

  expect_failures = [var.acme_email]
}

run "traefik_sees_client_ips" {
  command = plan

  assert {
    condition     = strcontains(yamldecode(helm_release.traefik.values[0]).service.annotations["service.beta.kubernetes.io/aws-load-balancer-target-group-attributes"], "preserve_client_ip.enabled=true")
    error_message = "the per-IP edge rate limit needs the NLB to preserve client IPs"
  }
}

run "vault_secrets_operator_has_no_default_identity" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault_secrets_operator.values[0]).defaultVaultConnection.enabled == false &&
      yamldecode(helm_release.vault_secrets_operator.values[0]).defaultAuthMethod.enabled == false &&
      yamldecode(helm_release.vault_secrets_operator.values[0]).controller.manager.globalTransformationOptions.excludeRaw == true
    )
    error_message = "every namespace brings its own VaultConnection and VaultAuth (infra/vault); no shared default"
  }
}

run "logs_ship_only_ryft_to_an_encrypted_group" {
  command = plan

  assert {
    condition     = aws_cloudwatch_log_group.containers.retention_in_days == 14 && aws_cloudwatch_log_group.containers.name == "/ryft/ryft-prod-euc1/containers"
    error_message = "one RYFT log group, kept 14 days"
  }
  assert {
    condition = alltrue([for p in split(",", yamldecode(helm_release.fluent_bit.values[0]).input.path) :
    strcontains(p, "_ryft-") || strcontains(p, "_kube-system_") && startswith(p, "/var/log/containers/traefik-")])
    error_message = "Fluent Bit tails only the RYFT namespaces and Traefik"
  }
  assert {
    condition     = yamldecode(helm_release.fluent_bit.values[0]).cloudWatchLogs.autoCreateGroup == false
    error_message = "the chart must not create an unencrypted group of its own"
  }
  assert {
    condition     = aws_kms_key.logs.enable_key_rotation
    error_message = "the log key rotates"
  }
}


run "argocd_is_behind_traefik_single_replica_and_keeps_no_initial_secret" {
  # apply (mocked): the values hold the generated password's hash
  command = apply

  assert {
    condition = (
      yamldecode(helm_release.argocd.values[0]).global.domain == "argocd.ryftapp.de" &&
      yamldecode(helm_release.argocd.values[0]).server.ingress.ingressClassName == "traefik" &&
      yamldecode(helm_release.argocd.values[0]).server.ingress.tls == true &&
      yamldecode(helm_release.argocd.values[0]).server.ingress.annotations["cert-manager.io/cluster-issuer"] == "letsencrypt-prod" &&
      yamldecode(helm_release.argocd.values[0]).server.ingress.annotations["traefik.ingress.kubernetes.io/router.entrypoints"] == "websecure" &&
      strcontains(yamldecode(helm_release.argocd.values[0]).server.ingress.annotations["traefik.ingress.kubernetes.io/router.middlewares"], "argocd-ratelimit@kubernetescrd")
    )
    error_message = "the UI is public only through Traefik's HTTPS entrypoint, with a Let's Encrypt certificate and the per-IP limit (D103)"
  }
  assert {
    condition = alltrue([for name in ["ratelimit", "hsts"] :
      contains([for o in yamldecode(helm_release.argocd.values[0]).extraObjects : o.metadata.name if o.kind == "Middleware"], name)
    ])
    error_message = "the Ingress's middlewares must exist in namespace argocd"
  }
  assert {
    condition     = yamldecode(helm_release.argocd.values[0]).configs.params["server.insecure"] == true && yamldecode(helm_release.argocd.values[0]).dex.enabled == false
    error_message = "TLS ends at Traefik (argocd-server plain HTTP behind it), and no SSO"
  }
  assert {
    condition     = output.argocd_url == "https://argocd.ryftapp.de"
    error_message = "argocd_url names the public host"
  }
  assert {
    condition = alltrue([for c in ["controller", "server", "repoServer", "applicationSet"] :
    yamldecode(helm_release.argocd.values[0])[c].replicas == (c == "applicationSet" ? 0 : 1) && can(yamldecode(helm_release.argocd.values[0])[c].resources.requests.cpu)])
    error_message = "non-HA (the ApplicationSet controller is off, T12-f), and every component with explicit requests (the nodes are CPU-bound, D94)"
  }
  assert {
    condition     = yamldecode(helm_release.argocd.values[0]).configs.secret.argocdServerAdminPassword == "$2a$10$mockbcrypthash"
    error_message = "the admin password is Terraform's bcrypt hash, so Argo CD writes no argocd-initial-admin-secret"
  }
  assert {
    condition     = strcontains(yamldecode(helm_release.argocd.values[0]).configs.cm["resource.customizations.health.autoscaling_HorizontalPodAutoscaler"], "FailedGetResourceMetric")
    error_message = "an HPA without metrics yet (right after a rollout) must not fail the sync (D105)"
  }
  assert {
    condition     = can(yamldecode(helm_release.argocd.values[0]).configs.cm["resource.customizations.health.argoproj.io_Application"])
    error_message = "Application health must be restored, or the app-of-apps sync waves do not wait for each other"
  }
  # D121: Argo CD's Lua VM has no string library; `make argocd-luatest` runs the scripts in it.
  assert {
    condition = alltrue([for k, v in yamldecode(helm_release.argocd.values[0]).configs.cm :
      !strcontains(v, "string.") && !strcontains(v, ":find(") && !strcontains(v, ":match(") && !strcontains(v, ":gsub(")
    if startswith(k, "resource.customizations.health.")])
    error_message = "a health script must not use the string library (Argo CD does not open it, D121)"
  }
  assert {
    condition = length([for k, v in yamldecode(helm_release.argocd.values[0]).configs.cm : k
      if startswith(k, "resource.customizations.health.rabbitmq.com_")]) == 9 && !anytrue([for k, v in yamldecode(helm_release.argocd.values[0]).configs.cm : true
    if startswith(k, "resource.customizations") && (strcontains(k, "useOpenLibs") || strcontains(tostring(v), "useOpenLibs"))])
    error_message = "one health check per RabbitMQ kind, and no useOpenLibs, as a key or inside a resource.customizations YAML block (D121)"
  }
  assert {
    condition     = kubernetes_namespace_v1.argocd.metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted"
    error_message = "the argocd namespace enforces Pod Security restricted"
  }
}

run "argocd_clusterroles_are_an_allowlist" {
  # T12-f (D120): the rendered values, not the chart's `*` defaults. The checks are ALLOWLISTS
  # (verbs, kinds, non-resource URLs): a rule that is new in kind, as `impersonate`, `pods/attach`,
  # `nodes/proxy`, a signer or `deletecollection` would be, fails here instead of passing for want
  # of a matching denylist entry. A kind the apps newly render is added to the map below on purpose.
  command = apply

  assert {
    condition = (
      yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.enabled == true &&
      yamldecode(helm_release.argocd.values[0]).server.clusterRoleRules.enabled == true
    )
    error_message = "both ClusterRoles must come from our rules, or the chart gives the controller * and the server delete/get/patch on everything"
  }
  # Verbs: only these. No `*`, impersonate, deletecollection, approve, sign, use, ...
  assert {
    condition = alltrue(flatten([for c in ["controller", "server"] : [
      for r in yamldecode(helm_release.argocd.values[0])[c].clusterRoleRules.rules :
      length(r.verbs) > 0 && alltrue([for v in r.verbs : contains(["get", "list", "watch", "create", "update", "patch", "delete", "escalate", "bind"], v)])
    ]]))
    error_message = "verbs are limited to get, list, watch, create, update, patch, delete, escalate and bind: no wildcard, impersonate, deletecollection, approve or sign"
  }
  # Kinds: the controller's apiGroup -> resources, exactly what the paths render plus the reads.
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      can(r.nonResourceURLs) ? true : (can(r.apiGroups) && can(r.resources) && length(r.resources) > 0 && alltrue([for g in r.apiGroups : alltrue([for res in r.resources :
        contains(lookup({
          ""                             = ["configmaps", "services", "serviceaccounts", "pods", "namespaces"]
          "apps"                         = ["deployments", "replicasets", "statefulsets"]
          "autoscaling"                  = ["horizontalpodautoscalers"]
          "batch"                        = ["jobs"]
          "networking.k8s.io"            = ["ingresses", "networkpolicies"]
          "policy"                       = ["poddisruptionbudgets"]
          "traefik.io"                   = ["middlewares", "serverstransports"]
          "cert-manager.io"              = ["certificates", "issuers"]
          "secrets.hashicorp.com"        = ["vaultauths", "vaultconnections", "vaultstaticsecrets"]
          "rabbitmq.com"                 = ["rabbitmqclusters", "vhosts", "exchanges", "queues", "bindings", "policies", "users", "permissions", "topicpermissions"]
          "apiextensions.k8s.io"         = ["customresourcedefinitions"]
          "rbac.authorization.k8s.io"    = ["clusterroles", "clusterrolebindings", "roles", "rolebindings"]
          "admissionregistration.k8s.io" = ["mutatingwebhookconfigurations", "validatingwebhookconfigurations"]
          "authentication.k8s.io"        = ["tokenreviews"]
          "authorization.k8s.io"         = ["subjectaccessreviews"]
          "argoproj.io"                  = ["applications"]
        }, g, []), res)
    ])]))])
    error_message = "the controller's kinds are exactly the allowlist (no secrets, nodes, pods/exec, pods/attach, pods/portforward, */proxy, certificatesigningrequests, signers, VAP/VAPB, '*'): a new kind is added here on purpose"
  }
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).server.clusterRoleRules.rules :
      !can(r.nonResourceURLs) && length(try(r.resources, [])) > 0 && alltrue([for g in r.apiGroups : alltrue([for res in r.resources :
        contains(lookup({
          ""                          = ["configmaps", "services", "serviceaccounts", "namespaces", "pods", "pods/log", "events"]
          "apps"                      = ["deployments", "replicasets", "statefulsets"]
          "autoscaling"               = ["horizontalpodautoscalers"]
          "batch"                     = ["jobs"]
          "networking.k8s.io"         = ["ingresses", "networkpolicies"]
          "policy"                    = ["poddisruptionbudgets"]
          "traefik.io"                = ["middlewares", "serverstransports"]
          "cert-manager.io"           = ["certificates", "issuers"]
          "secrets.hashicorp.com"     = ["vaultauths", "vaultconnections", "vaultstaticsecrets"]
          "rabbitmq.com"              = ["rabbitmqclusters", "vhosts", "exchanges", "queues", "bindings", "policies", "users", "permissions", "topicpermissions"]
          "apiextensions.k8s.io"      = ["customresourcedefinitions"]
          "rbac.authorization.k8s.io" = ["clusterroles", "clusterrolebindings", "roles", "rolebindings"]
        }, g, []), res)
      ])])
    ])
    error_message = "the server's kinds are exactly the allowlist (no admissionregistration.k8s.io, no secrets, no pods/exec, no non-resource URL)"
  }
  # Non-resource URLs: the controller holds /metrics get (the operators' metrics-reader role) and nothing else.
  assert {
    condition = (
      [for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules : r if can(r.nonResourceURLs)] ==
      [{ nonResourceURLs = ["/metrics"], verbs = ["get"] }] &&
      length([for r in yamldecode(helm_release.argocd.values[0]).server.clusterRoleRules.rules : r if can(r.nonResourceURLs)]) == 0
    )
    error_message = "the only non-resource URL is the controller's get on /metrics; the server has none"
  }
  assert {
    condition = alltrue(flatten([for c in ["controller", "server"] : [
      for r in yamldecode(helm_release.argocd.values[0])[c].clusterRoleRules.rules :
      !can(r.resourceNames) ? true : (length(r.resourceNames) > 0 && alltrue([for n in r.resourceNames : !strcontains(n, "*")]))
    ]]))
    error_message = "a rule with resourceNames names at least one object (an empty list means every name) and no wildcard"
  }
  # escalate and bind: only on Roles (both) and on one ClusterRole (bind alone), only with names.
  assert {
    condition = alltrue(flatten([for c in ["controller", "server"] : [
      for r in yamldecode(helm_release.argocd.values[0])[c].clusterRoleRules.rules :
      length(setintersection(r.verbs, ["escalate", "bind"])) == 0 || (
        try(r.apiGroups, []) == ["rbac.authorization.k8s.io"] && length(try(r.resourceNames, [])) > 0 && (
          try(r.resources, []) == ["roles"] || (try(r.resources, []) == ["clusterroles"] && r.verbs == ["bind"])
        )
      )
    ]]))
    error_message = "escalate and bind only on roles, bind alone on one ClusterRole, and only by a non-empty resourceNames"
  }
  assert {
    condition = (
      toset(flatten([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
        r.resourceNames if contains(r.verbs, "escalate")])) == toset([
        "rabbitmq-cluster-leader-election-role", "messaging-topology-leader-election-role",
        "rabbitmq-cluster-operator-role", "messaging-topology-manager-role",
      ]) &&
      toset(flatten([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
        r.resourceNames if contains(r.verbs, "bind") && try(r.resources, []) == ["roles"]])) == toset([
        "rabbitmq-cluster-leader-election-role", "messaging-topology-leader-election-role",
        "rabbitmq-cluster-operator-role", "messaging-topology-manager-role",
      ]) &&
      toset(flatten([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
        r.resourceNames if contains(r.verbs, "bind") && try(r.resources, []) == ["clusterroles"]])) == toset([
        "messaging-topology-metrics-auth-role",
      ]) &&
      length([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      r if contains(r.verbs, "escalate") && contains(try(r.resources, []), "clusterroles")]) == 0
    )
    error_message = "escalate names exactly the operators' four Roles and never a ClusterRole; bind names the same four Roles and the one ClusterRole of the topology operator's binding"
  }
  # The server cannot write anything cluster-wide.
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).server.clusterRoleRules.rules :
    alltrue([for v in r.verbs : contains(["get", "list", "watch"], v)])])
    error_message = "the server has read verbs only: a leaked token of the public UI (D103) must not patch or delete workloads or forge Events"
  }
  # Read-only kinds and the two review creates.
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      alltrue([for v in r.verbs : contains(["get", "list", "watch"], v)])
      if length(setintersection(try(r.resources, []), ["pods", "replicasets", "statefulsets", "applications"])) > 0
    ])
    error_message = "pods, replicasets, statefulsets and applications are read-only for the controller"
  }
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      r.verbs == ["create"]
      if length(setintersection(try(r.resources, []), ["tokenreviews", "subjectaccessreviews"])) > 0
    ])
    error_message = "tokenreviews and subjectaccessreviews: create only"
  }
  assert {
    condition = length([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
    r if contains(try(r.resources, []), "applications") && contains(r.verbs, "list") && !can(r.resourceNames)]) == 1
    error_message = "the controller lists applications cluster-wide: under respectRBAC strict the kind is otherwise dropped silently (D151 amended)"
  }
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      !contains(r.verbs, "delete")
    if can(r.resources) && (contains(r.resources, "namespaces") || contains(r.resources, "customresourcedefinitions"))])
    error_message = "the controller must not delete namespaces or CRDs (that takes their objects, the broker's data, along)"
  }
  # Webhook configurations and CRDs: changed only by name.
  assert {
    condition = alltrue([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      can(r.resourceNames)
      if(contains(try(r.apiGroups, []), "admissionregistration.k8s.io") || contains(try(r.apiGroups, []), "apiextensions.k8s.io")) && length(setintersection(r.verbs, ["update", "patch", "delete"])) > 0
    ])
    error_message = "changing or deleting a webhook configuration, and updating or patching a CRD, only by name"
  }
  assert {
    condition = (
      toset(flatten([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
        r.resourceNames if contains(try(r.resources, []), "mutatingwebhookconfigurations") && can(r.resourceNames)])) == toset([
        "cluster-operator-mutating-webhook-configuration", "messaging-topology-mutating-webhook-configuration",
      ]) &&
      toset(flatten([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
        r.resourceNames if contains(try(r.resources, []), "validatingwebhookconfigurations") && can(r.resourceNames)])) == toset([
        "cluster-operator-validating-webhook-configuration", "messaging-topology-validating-webhook-configuration",
      ])
    )
    error_message = "webhook configurations are changed or deleted only by the operators' four names"
  }
  assert {
    condition = (
      toset(flatten([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
        r.resourceNames if contains(try(r.resources, []), "customresourcedefinitions") && can(r.resourceNames)])) == toset([
        "bindings.rabbitmq.com", "exchanges.rabbitmq.com", "federations.rabbitmq.com",
        "operatorpolicies.rabbitmq.com", "permissions.rabbitmq.com", "policies.rabbitmq.com",
        "queues.rabbitmq.com", "rabbitmqclusters.rabbitmq.com", "schemareplications.rabbitmq.com",
        "shovels.rabbitmq.com", "superstreams.rabbitmq.com", "topicpermissions.rabbitmq.com",
        "users.rabbitmq.com", "vhosts.rabbitmq.com",
      ]) &&
      !anytrue([for r in yamldecode(helm_release.argocd.values[0]).controller.clusterRoleRules.rules :
      true if contains(try(r.resources, []), "customresourcedefinitions") && !can(r.resourceNames) && length(setintersection(r.verbs, ["update", "patch"])) > 0])
    )
    error_message = "CRDs are updated or patched only by the operators' 14 names (no conversion webhook on applications.argoproj.io)"
  }
  assert {
    condition = (
      yamldecode(helm_release.argocd.values[0]).configs.cm["resource.respectRBAC"] == "strict" &&
      yamldecode(helm_release.argocd.values[0]).configs.cm["exec.enabled"] == false
    )
    error_message = "resource.respectRBAC must be strict and the UI terminal off"
  }
  assert {
    condition     = yamldecode(helm_release.argocd.values[0]).applicationSet.replicas == 0
    error_message = "the ApplicationSet controller is off (this chart version has no enabled flag)"
  }
}

# T12-f: what the ClusterRoles' rules above do not see. A rule check on the two ClusterRoles passes
# when the chart renders no ClusterRole, or when a ClusterRoleBinding to cluster-admin comes in
# through extraObjects, or when Argo CD's own RBAC makes every login an admin. The values are
# inspected by modules/argocd-values-guard, a precondition of helm_release.argocd (argocd.tf).
run "argocd_values_carry_nothing_else_that_grants" {
  command = apply

  assert {
    condition     = module.argocd_values_guard.violations == []
    error_message = "the Argo CD values carry something they must not: ${join("; ", module.argocd_values_guard.violations)}"
  }
  # The same, read from the release itself, so removing the module call does not remove the test.
  assert {
    condition = alltrue([for o in yamldecode(helm_release.argocd.values[0]).extraObjects :
    contains(["NetworkPolicy", "Middleware"], o.kind) && o.metadata.namespace == "argocd"])
    error_message = "extraObjects may only ship a NetworkPolicy or a Middleware in argocd (a ClusterRoleBinding there would bypass every rule check)"
  }
  assert {
    condition = (
      !can(yamldecode(helm_release.argocd.values[0]).controller.roleRules) &&
      !can(yamldecode(helm_release.argocd.values[0]).server.roleRules) &&
      try(yamldecode(helm_release.argocd.values[0]).createClusterRoles, true) != false
    )
    error_message = "no roleRules of ours, and the chart renders its ClusterRoles (createClusterRoles is not false)"
  }
  assert {
    condition = (
      !can(yamldecode(helm_release.argocd.values[0]).configs.rbac) &&
      !can(yamldecode(helm_release.argocd.values[0]).configs.cm["users.anonymous.enabled"]) &&
      !can(yamldecode(helm_release.argocd.values[0]).configs.params["server.disable.auth"])
    )
    error_message = "no policy.default, no anonymous login, no disabled authentication on the public UI"
  }
}

# Each variant that passed the ClusterRole allowlist and is dangerous, run through the guard module
# alone: it must be caught, by name.
run "guard_clean_values_pass" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller   = { clusterRoleRules = { enabled = true } }
      server       = { clusterRoleRules = { enabled = true } }
      configs      = { cm = { "resource.respectRBAC" = "strict" }, params = { "server.insecure" = true } }
      extraObjects = [{ apiVersion = "networking.k8s.io/v1", kind = "NetworkPolicy", metadata = { name = "x", namespace = "argocd" } }, { apiVersion = "traefik.io/v1alpha1", kind = "Middleware", metadata = { name = "y", namespace = "argocd" } }]
    }
  }
  assert {
    condition     = output.violations == []
    error_message = "a clean set of values must pass the guard"
  }
}

run "guard_catches_a_cluster_admin_binding_in_extraobjects" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      extraObjects = [{
        apiVersion = "rbac.authorization.k8s.io/v1"
        kind       = "ClusterRoleBinding"
        metadata   = { name = "argocd-admin" }
        roleRef    = { apiGroup = "rbac.authorization.k8s.io", kind = "ClusterRole", name = "cluster-admin" }
        subjects   = [{ kind = "ServiceAccount", name = "argocd-application-controller", namespace = "argocd" }]
      }]
    }
  }
  assert {
    condition     = length(output.violations) == 2 && strcontains(join(" ", output.violations), "kind \"ClusterRoleBinding\" is not one of")
    error_message = "a ClusterRoleBinding in extraObjects must be caught (kind, and no namespace)"
  }
}

run "guard_catches_an_object_in_another_namespace" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller   = { clusterRoleRules = { enabled = true } }
      server       = { clusterRoleRules = { enabled = true } }
      extraObjects = [{ apiVersion = "networking.k8s.io/v1", kind = "NetworkPolicy", metadata = { name = "x", namespace = "kube-system" } }]
    }
  }
  assert {
    condition     = length(output.violations) == 1 && strcontains(output.violations[0], "is not argocd")
    error_message = "an extra object outside argocd must be caught"
  }
}

run "guard_catches_controller_role_rules_with_wildcard" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = {
        clusterRoleRules = { enabled = true }
        roleRules        = [{ apiGroups = ["*"], resources = ["*"], verbs = ["*"] }]
      }
      server = { clusterRoleRules = { enabled = true } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "controller.roleRules is set")
    error_message = "controller.roleRules must be caught"
  }
}

run "guard_catches_createclusterroles_false" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      createClusterRoles = false
      controller         = { clusterRoleRules = { enabled = true } }
      server             = { clusterRoleRules = { enabled = true } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "createClusterRoles is false")
    error_message = "createClusterRoles = false must be caught"
  }
}

run "guard_catches_a_default_admin_policy" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      configs    = { rbac = { "policy.default" = "role:admin" } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "configs.rbac is set") && strcontains(join(" ", output.violations), "unexpected value \"configs.rbac\"")
    error_message = "configs.rbac policy.default must be caught"
  }
}

run "guard_catches_anonymous_login_and_disabled_auth" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      configs = {
        cm     = { "users.anonymous.enabled" = true }
        params = { "server.disable.auth" = true }
      }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "users.anonymous.enabled") && strcontains(join(" ", output.violations), "server.disable.auth")
    error_message = "an anonymous login and a disabled authentication must be caught"
  }
}

run "guard_catches_the_chart_default_clusterroles" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      createAggregateRoles = true
      controller           = { clusterRoleRules = { enabled = false } }
      server               = { clusterRoleRules = { enabled = true } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "controller.clusterRoleRules is not enabled") && strcontains(join(" ", output.violations), "unexpected top-level value \"createAggregateRoles\"")
    error_message = "the chart's own wildcard ClusterRole and an unexpected top-level value must be caught"
  }
}

# The chart renders extraObjects with `tpl`: a base64-decoded document inside apiVersion renders a whole
# ClusterRoleBinding to cluster-admin while kind, namespace and name look allowed (variant B6).
run "guard_catches_a_template_expression_in_an_extra_object" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      extraObjects = [{
        apiVersion = "networking.k8s.io/v1{{\"a2luZDogQ2x1c3RlclJvbGVCaW5kaW5n\"|b64dec}}"
        kind       = "NetworkPolicy"
        metadata   = { name = "x", namespace = "argocd" }
      }]
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "template expression ({{)") && strcontains(join(" ", output.violations), "is not networking.k8s.io/v1")
    error_message = "a template expression in an extra object, and a changed apiVersion, must be caught"
  }
}

run "guard_catches_a_wrong_api_version_and_a_string_extra_object" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      extraObjects = [
        { apiVersion = "traefik.io/v1", kind = "Middleware", metadata = { name = "y", namespace = "argocd" } },
        "kind: ClusterRoleBinding",
      ]
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "is not traefik.io/v1alpha1") && strcontains(join(" ", output.violations), "extraObjects[1]: kind")
    error_message = "another apiVersion of an allowed kind, and a string entry (the chart would tpl it), must be caught"
  }
}

# The release's lifecycle conditions (a `set` and a second values document, variants B1 and B2) cannot be
# injected from a test file (override_resource does not replace configured arguments):
# scripts/tests/argocd_release_guard_test.sh edits a copy of this module and runs this file against it.

# Variant B7: values below the second level that decide something and passed the first version.
run "guard_catches_deeper_values" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      global = {
        image         = { repository = "evil.example/argocd" }
        networkPolicy = { create = false }
      }
      configs = {
        cm     = { "accounts.admin" = "apiKey, login" }
        params = { "server.insecure" = true, "server.x.frame.options" = "" }
        secret = { extra = { "server.secretkey" = "x" } }
      }
      controller = { clusterRoleRules = { enabled = true } }
      server = {
        clusterRoleRules = { enabled = true }
        ingress          = { enabled = true, annotations = { "nginx.ingress.kubernetes.io/configuration-snippet" = "x" } }
      }
    }
  }
  assert {
    condition = alltrue([for needle in [
      "global.image.repository", "global.networkPolicy.create is not true", "configs.cm.accounts.admin",
      "configs.params.server.x.frame.options", "configs.secret.extra", "server.ingress.annotations.nginx.ingress.kubernetes.io/configuration-snippet",
    ] : strcontains(join(" | ", output.violations), needle)])
    error_message = "the image repository, the chart's NetworkPolicies off, an admin API-key account, another param, extra secret keys and another ingress annotation must be caught"
  }
}

# Values of ALLOWED keys that still render other objects: a line break ends the value in the rendered YAML
# and starts another document (server.ingress.ingressClassName and global.domain are written into the
# Ingress and its annotations), and an annotation goes through `tpl`, so `{{ lookup ... }}` would run at
# render time.
run "guard_catches_a_line_break_in_ingressclassname" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server = {
        clusterRoleRules = { enabled = true }
        ingress          = { enabled = true, ingressClassName = "traefik\n---\nkind: ClusterRoleBinding\n" }
      }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "line break")
    error_message = "a line break in ingressClassName must be caught"
  }
}

run "guard_catches_a_line_break_in_the_domain" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      global     = { domain = "x.example.com\n---\nkind: DaemonSet\n" }
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "line break")
    error_message = "a line break in global.domain must be caught"
  }
}

run "guard_catches_a_template_expression_in_an_annotation" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server = {
        clusterRoleRules = { enabled = true }
        ingress          = { enabled = true, annotations = { "cert-manager.io/cluster-issuer" = "{{ (lookup \"v1\" \"Secret\" \"kube-system\" \"x\").data }}" } }
      }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "template expression ({{)")
    error_message = "a template expression in an allowed annotation must be caught"
  }
}

run "guard_allows_the_health_customizations_to_be_multi_line" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      configs    = { cm = { "resource.customizations.health.apps_Deployment" = "hs = {}\nreturn hs\n" } }
    }
  }
  assert {
    condition     = output.violations == []
    error_message = "the Lua health customizations are the one place for multi-line strings"
  }
}

# Unicode line breaks: jsonencode writes U+2028 and U+2029 as escapes (no \n in them) and U+0085 raw,
# and YAML reads all three as line breaks in the values the chart writes unquoted. Built with escapes
# here so this file stays ASCII.
run "guard_catches_u2028_in_the_domain" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      global     = { domain = "x.example.com${jsondecode("\"\\u2028\"")}kind: DaemonSet" }
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "control character or an escape")
    error_message = "U+2028 in global.domain must be caught"
  }
}

run "guard_catches_u2029_in_ingressclassname" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server = {
        clusterRoleRules = { enabled = true }
        ingress          = { enabled = true, ingressClassName = "traefik${jsondecode("\"\\u2029\"")}kind: ClusterRoleBinding" }
      }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "control character or an escape")
    error_message = "U+2029 in ingressClassName must be caught"
  }
}

run "guard_catches_u0085_in_the_image_tag" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      global     = { image = { tag = "v3.5.6${jsondecode("\"\\u0085\"")}kind: DaemonSet" } }
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "non-ASCII character")
    error_message = "U+0085 in global.image.tag must be caught"
  }
}

run "guard_catches_a_tab_and_non_ascii_in_the_health_lua" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      configs = { cm = {
        "resource.customizations.health.apps_Deployment" = "hs = {}\n\treturn hs\n"
        "resource.customizations.health.batch_Job"       = "-- caf${jsondecode("\"\\u00e9\"")}\nreturn {}\n"
        "resource.customizations.health.autoscaling_HPA" = "a${jsondecode("\"\\u2028\"")}b"
      } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "control character or an escape") && strcontains(join(" ", output.violations), "non-ASCII character")
    error_message = "a tab, U+2028 and non-ASCII in the Lua health customizations must be caught (only newlines are allowed there)"
  }
}

run "guard_catches_a_terminal_and_a_lax_respectrbac" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      configs    = { cm = { "exec.enabled" = true, "resource.respectRBAC" = "normal" } }
    }
  }
  assert {
    condition     = strcontains(join(" ", output.violations), "exec.enabled is not false") && strcontains(join(" ", output.violations), "respectRBAC is not strict")
    error_message = "a terminal in the UI and a respectRBAC that is not strict must be caught"
  }
}
