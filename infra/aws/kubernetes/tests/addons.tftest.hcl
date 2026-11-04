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
