# Unit tests: AWS, Helm and Kubernetes providers are mocked; nothing is created.
#   terraform init && terraform test

variables {
  acme_email = "ops@example.com"
}

mock_provider "aws" {
  mock_data "aws_eks_cluster" {
    defaults = {
      arn                   = "arn:aws:eks:eu-central-1:123456789012:cluster/ryft-prod-euc1"
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
  # infra/aws's unseal key (PR 1 of the Vault migration), found by its alias.
  mock_data "aws_kms_alias" {
    defaults = {
      arn            = "arn:aws:kms:eu-central-1:123456789012:alias/ryft-prod-euc1-vault-unseal"
      target_key_id  = "0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
      target_key_arn = "arn:aws:kms:eu-central-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
    }
  }
}

# infra/aws's snapshot key (alias/<cluster>-vault-snapshots): a key of its own, so the tests can tell it from the
# unseal key the provider-wide mock returns.
override_data {
  target = data.aws_kms_alias.vault_snapshots
  values = {
    arn            = "arn:aws:kms:eu-central-1:123456789012:alias/ryft-prod-euc1-vault-snapshots"
    target_key_id  = "7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"
    target_key_arn = "arn:aws:kms:eu-central-1:123456789012:key/7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"
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
      helm_release.argocd.version == "10.10.5" &&
      helm_release.vault.version == "0.34.1" &&
      helm_release.vault.repository == "https://helm.releases.hashicorp.com" &&
      helm_release.vault.chart == "vault"
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

# With sync impersonation on, every controller write moves to another username and P12 (names only)
# matches nothing: it is not an allowed key anywhere.
run "guard_catches_sync_impersonation" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = {
      controller = { clusterRoleRules = { enabled = true } }
      server     = { clusterRoleRules = { enabled = true } }
      configs = {
        cm     = { "application.sync.impersonation.enabled" = true }
        params = { "application.sync.impersonation.enabled" = true }
      }
    }
  }
  assert {
    condition     = strcontains(join(" | ", output.violations), "configs.cm.application.sync.impersonation.enabled") && strcontains(join(" | ", output.violations), "configs.params.application.sync.impersonation.enabled")
    error_message = "application.sync.impersonation.enabled must be caught in configs.cm and configs.params"
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

# --- Vault Community Edition (vault.tf, iam.tf, charts/vault-extras; D186-D196) ---
# The chart itself is mocked: these runs pin the values Helm gets. The render (Services, probes, securityContext
# as the chart writes them) is checked with helm template of the same values; the pods under Pod Security
# restricted only in kind.

run "vault_is_the_pinned_community_image_with_three_raft_replicas" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.image.repository == "docker.io/hashicorp/vault" &&
      yamldecode(helm_release.vault.values[0]).server.image.tag == "2.2.1@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2"
    )
    error_message = "Vault CE 2.2.1, pinned by its multi-arch index digest, from docker.io/hashicorp/vault (the reference admission P2 allows)"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.ha.enabled == true &&
      yamldecode(helm_release.vault.values[0]).server.ha.replicas == 3 &&
      yamldecode(helm_release.vault.values[0]).server.ha.raft.enabled == true &&
      yamldecode(helm_release.vault.values[0]).server.standalone.enabled == false
    )
    error_message = "three replicas with integrated storage (Raft), one per node"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).injector.enabled == false && yamldecode(helm_release.vault.values[0]).csi.enabled == false
    error_message = "no agent injector (no webhook) and no CSI provider (no DaemonSet): VSO syncs the Secrets"
  }
  assert {
    condition     = helm_release.vault.namespace == "vault" && kubernetes_namespace_v1.vault.metadata[0].name == "vault" && helm_release.vault_extras.namespace == "vault"
    error_message = "Vault and its extras live in namespace vault"
  }
}

run "vault_config_has_mlock_off_the_kms_seal_tls_reload_and_metrics" {
  command = plan

  assert {
    condition     = strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "disable_mlock = true")
    error_message = "disable_mlock = true is required with Raft (Vault refuses to start without it) and lets the pod run without IPC_LOCK"
  }
  assert {
    condition = (
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "seal \"awskms\" {") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "region     = \"eu-central-1\"") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "kms_key_id = \"arn:aws:kms:eu-central-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b\"") &&
      !strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "alias/")
    )
    error_message = "the awskms seal names the key's ARN from the alias (never the alias itself, which can be re-pointed) and the region (not left to the injected AWS_REGION)"
  }
  assert {
    condition = alltrue([for w in ["access_key", "secret_key", "session_token", "shared_creds", "role_arn", "web_identity_token_file"] :
    !strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, w)])
    error_message = "no static or IRSA credentials in the seal: Pod Identity only"
  }
  assert {
    condition = (
      length([for k in keys(yamldecode(helm_release.vault.values[0]).server.extraEnvironmentVars) : k if startswith(k, "AWS_")]) == 0 &&
      !can(yamldecode(helm_release.vault.values[0]).server.serviceAccount.annotations) &&
      !can(yamldecode(helm_release.vault.values[0]).server.extraSecretEnvironmentVars) &&
      !can(yamldecode(helm_release.vault.values[0]).server.extraVolumes)
    )
    error_message = "no AWS_* variable, no IRSA annotation on the service account, no secret env, no extra volumes (the draft's kms-creds): anything earlier in the SDK's chain would win over Pod Identity"
  }
  assert {
    condition = (
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "tls_reload_interval = \"5m\"") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "tls_cert_file       = \"/vault/userconfig/vault-tls/tls.crt\"") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "unauthenticated_metrics_access = true") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "service_registration \"kubernetes\" {}") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "prometheus_retention_time = \"2m\"")
    )
    error_message = "the listener reloads renewed certificates (5m), serves metrics without a token, and service_registration labels the active pod (vault-active has endpoints only with it)"
  }
  assert {
    condition = (
      length(regexall("retry_join \\{", yamldecode(helm_release.vault.values[0]).server.ha.raft.config)) == 3 &&
      alltrue([for i in range(3) : strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "leader_api_addr     = \"https://vault-${i}.vault-internal:8200\"")]) &&
      length(regexall("leader_ca_cert_file = \"/vault/userconfig/vault-tls/ca.crt\"", yamldecode(helm_release.vault.values[0]).server.ha.raft.config)) == 3 &&
      !strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "leader_client_")
    )
    error_message = "one retry_join per pod, trusting the ryft-vault CA, without the draft's client certificate (the listener requires none)"
  }
  assert {
    condition     = alltrue([for w in ["HOSTNAME", "POD_IP", "HOST_IP", "API_ADDR", "TRANSIT_ADDR", "RAFT_ADDR"] : !strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, w)])
    error_message = "the chart's start script replaces these words everywhere in the config"
  }
  # The listener block alone (from `listener "tcp" {` to the first `}` in column 0): the metrics setting is the
  # listener's, a top-level telemetry block with it would not open the endpoint.
  assert {
    condition = (
      length(regexall("(?s)\\nlistener \"tcp\" \\{\\n.*?\\n\\}\\n", yamldecode(helm_release.vault.values[0]).server.ha.raft.config)) == 1 &&
      can(regex("(?s)\\n  telemetry \\{\\n    unauthenticated_metrics_access = true\\n  \\}\\n", regex("(?s)\\nlistener \"tcp\" \\{\\n.*?\\n\\}\\n", yamldecode(helm_release.vault.values[0]).server.ha.raft.config))) &&
      strcontains(regex("(?s)\\nlistener \"tcp\" \\{\\n.*?\\n\\}\\n", yamldecode(helm_release.vault.values[0]).server.ha.raft.config), "tls_key_file        = \"/vault/userconfig/vault-tls/tls.key\"") &&
      strcontains(regex("(?s)\\nlistener \"tcp\" \\{\\n.*?\\n\\}\\n", yamldecode(helm_release.vault.values[0]).server.ha.raft.config), "tls_min_version     = \"tls12\"") &&
      !strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "tls_disable")
    )
    error_message = "one TLS listener: the vault-tls key and certificate, TLS 1.2 or later, and unauthenticated_metrics_access inside its telemetry block"
  }
  assert {
    condition = (
      startswith(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "ui            = true\ndisable_mlock = true\ncluster_name  = \"vault-integrated-storage\"\n") &&
      strcontains(yamldecode(helm_release.vault.values[0]).server.ha.raft.config, "storage \"raft\" {\n  path = \"/vault/data\"\n")
    )
    error_message = "the UI on, the cluster name, and Raft on the data volume (/vault/data, the chart's dataStorage mount)"
  }
}

run "vault_server_values_are_pinned_field_by_field" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.vault.values[0]).global.enabled == true && yamldecode(helm_release.vault.values[0]).global.tlsDisable == false
    error_message = "global.tlsDisable false: the chart probes and the in-pod VAULT_ADDR use https"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.ha.apiAddr == "https://$(VAULT_K8S_POD_NAME).vault-internal.vault.svc:8200"
    error_message = "each pod advertises its name under the headless Service (on the certificate); VAULT_K8S_POD_NAME is defined before VAULT_API_ADDR, HOSTNAME after it (which would not expand)"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.ha.raft.setNodeId == true
    error_message = "the Raft node ID is the pod name, so remove-peer names a pod"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.serviceAccount.serviceDiscovery.enabled == false
    error_message = "the chart's discovery Role (every pod in the namespace) is off; vault-extras has one limited to the server pods"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.authDelegator.enabled == true
    error_message = "Vault reviews VSO's tokens as itself (system:auth-delegator, D187)"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.dataStorage.enabled == true &&
      yamldecode(helm_release.vault.values[0]).ui.enabled == true
    )
    error_message = "a data volume per pod (Raft), and the UI Service"
  }
  assert {
    condition = (
      one([for v in yamldecode(helm_release.vault.values[0]).server.volumes : v.secret.defaultMode if v.name == "vault-tls"]) == 288 &&
      yamldecode(helm_release.vault.values[0]).server.extraEnvironmentVars == { VAULT_CACERT = "/vault/userconfig/vault-tls/ca.crt" }
    )
    error_message = "the TLS Secret mounted 0440 (group fsGroup), and VAULT_CACERT the only extra variable"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.terminationGracePeriodSeconds == 30
    error_message = "30 s for the step-down and the Raft leave (the chart's default is 10)"
  }
  assert {
    condition = yamldecode(helm_release.vault.values[0]).server.topologySpreadConstraints == [{
      maxSkew           = 1
      minDomains        = 3
      topologyKey       = "topology.kubernetes.io/zone"
      whenUnsatisfiable = "DoNotSchedule"
      labelSelector     = { matchLabels = { "app.kubernetes.io/name" = "vault", "app.kubernetes.io/instance" = "vault", component = "server" } }
    }]
    error_message = "one server pod per zone (the EBS volume pins each pod's zone): maxSkew 1, minDomains 3 (with two zones the third pod stays Pending instead of 2+1), DoNotSchedule, the server pods only"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.topologySpreadConstraints[0].minDomains == yamldecode(helm_release.vault.values[0]).server.ha.replicas
    error_message = "minDomains follows the replicas: every pod needs a zone of its own"
  }
}

run "vault_is_reachable_inside_the_cluster_only" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).ui.serviceType == "ClusterIP" &&
      yamldecode(helm_release.vault.values[0]).server.ingress.enabled == false &&
      yamldecode(helm_release.vault.values[0]).server.route.enabled == false &&
      !strcontains(helm_release.vault.values[0], "LoadBalancer") && !strcontains(helm_release.vault.values[0], "NodePort")
    )
    error_message = "the UI and the API through the port-forward only: ClusterIP, no Ingress, no LoadBalancer (the draft's public NLB)"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.service.publishNotReadyAddresses == false && yamldecode(helm_release.vault.values[0]).ui.publishNotReadyAddresses == false
    error_message = "the vault, vault-active, vault-standby and vault-ui Services publish ready pods only (a sealed pod gets no traffic)"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.networkPolicy.enabled == false
    error_message = "the chart's own policy admits 8200/8201 from every namespace; charts/vault-extras has the default deny"
  }
  assert {
    condition     = output.vault_address_in_cluster == "https://vault.vault.svc:8200" && output.vault_port_forward_command == "kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200:8200"
    error_message = "the in-cluster address and the port-forward command (with the context spelt out)"
  }
}

run "vault_probes_are_lenient_alive_and_strict_ready" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.readinessProbe.enabled == true &&
      yamldecode(helm_release.vault.values[0]).server.readinessProbe.path == "/v1/sys/health?standbyok=true"
    )
    error_message = "ready = initialised, unsealed, active or standby (D196): a sealed or uninitialised pod is not ready"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.livenessProbe.enabled == true &&
      yamldecode(helm_release.vault.values[0]).server.livenessProbe.path == "/v1/sys/health?standbyok=true&sealedcode=204&uninitcode=204&haunhealthycode=204&removedcode=204" &&
      yamldecode(helm_release.vault.values[0]).server.livenessProbe.initialDelaySeconds == 60 &&
      yamldecode(helm_release.vault.values[0]).server.livenessProbe.periodSeconds == 5 &&
      yamldecode(helm_release.vault.values[0]).server.livenessProbe.failureThreshold == 6
    )
    error_message = "alive = any answer: sealed, uninitialised, a standby on a heartbeat gap (474) and a removed node (530) included; 6 x 5 s of silence before a restart"
  }
  assert {
    condition     = !strcontains(yamldecode(helm_release.vault.values[0]).server.readinessProbe.path, "code=")
    error_message = "readiness overrides no status code: sealed, uninitialised, 474 and 530 are not ready"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.ha.disruptionBudget.enabled == false
    error_message = "the chart's PDB is off (it can only say maxUnavailable and keeps the default unhealthy-pod policy); vault-extras has the budget"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault_extras.values[0]).replicas == yamldecode(helm_release.vault.values[0]).server.ha.replicas &&
      strcontains(file("${path.module}/charts/vault-extras/templates/pdb.yaml"), "  minAvailable: {{ add (div $replicas 2) 1 }}\n") &&
      strcontains(file("${path.module}/charts/vault-extras/templates/pdb.yaml"), "  unhealthyPodEvictionPolicy: AlwaysAllow\n")
    )
    error_message = "vault-extras' PDB keeps the quorum (minAvailable 2 of 3) and lets unready pods go (AlwaysAllow); the render is pinned by scripts/tests/vault_extras_chart_test.sh"
  }
}

run "vault_pod_is_pod_security_restricted" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.pod.runAsNonRoot == true &&
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.pod.runAsUser == 100 &&
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.pod.runAsGroup == 1000 &&
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.pod.fsGroup == 1000 &&
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.pod.seccompProfile.type == "RuntimeDefault"
    )
    error_message = "the pod context replaces the chart's entirely: non-root (vault, 100), group 1000, RuntimeDefault seccomp"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.container.allowPrivilegeEscalation == false &&
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.container.readOnlyRootFilesystem == true &&
      yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.container.capabilities.drop == ["ALL"] &&
      !can(yamldecode(helm_release.vault.values[0]).server.statefulSet.securityContext.container.capabilities.add)
    )
    error_message = "no privilege escalation, a read-only root filesystem, every capability dropped and none added (no IPC_LOCK)"
  }
  assert {
    condition = (
      contains([for v in yamldecode(helm_release.vault.values[0]).server.volumes : v.name if try(v.emptyDir.medium, "") == "Memory"], "tmp") &&
      contains([for m in yamldecode(helm_release.vault.values[0]).server.volumeMounts : m.mountPath if m.name == "tmp"], "/tmp") &&
      contains([for v in yamldecode(helm_release.vault.values[0]).server.volumes : v.name if try(v.secret.secretName, "") == "vault-tls"], "vault-tls") &&
      contains([for m in yamldecode(helm_release.vault.values[0]).server.volumeMounts : m.mountPath if m.name == "vault-tls" && try(m.readOnly, false)], "/vault/userconfig/vault-tls")
    )
    error_message = "/tmp is an emptyDir (the start script writes the config there) and the TLS Secret is a whole-directory read-only mount (no subPath, so renewals arrive)"
  }
  assert {
    condition = (
      kubernetes_namespace_v1.vault.metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted" &&
      kubernetes_namespace_v1.vault.metadata[0].labels["pod-security.kubernetes.io/warn"] == "restricted" &&
      kubernetes_namespace_v1.vault.metadata[0].labels["pod-security.kubernetes.io/audit"] == "restricted" &&
      length([for k, v in kubernetes_namespace_v1.vault.metadata[0].labels : k if endswith(k, "-version")]) == 0
    )
    error_message = "namespace vault is Pod Security restricted on enforce, warn and audit (versions absent = latest, as P1 wants)"
  }
}

run "vault_resources_and_storage" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.resources.requests.cpu == "100m" &&
      yamldecode(helm_release.vault.values[0]).server.resources.requests.memory == "256Mi" &&
      yamldecode(helm_release.vault.values[0]).server.resources.limits.memory == "512Mi" &&
      !can(yamldecode(helm_release.vault.values[0]).server.resources.limits.cpu) &&
      !can(yamldecode(helm_release.vault.values[0]).global.resources)
    )
    error_message = "100m / 256Mi requested, 512Mi memory limit, no CPU limit; no global.resources (not a chart key)"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault.values[0]).server.dataStorage.storageClass == "gp3-encrypted" &&
      yamldecode(helm_release.vault.values[0]).server.dataStorage.size == "10Gi" &&
      yamldecode(helm_release.vault.values[0]).server.auditStorage.enabled == true &&
      yamldecode(helm_release.vault.values[0]).server.auditStorage.storageClass == "gp3-encrypted" &&
      yamldecode(helm_release.vault.values[0]).server.auditStorage.size == "5Gi" &&
      yamldecode(helm_release.vault.values[0]).server.persistentVolumeClaimRetentionPolicy.whenDeleted == "Retain" &&
      yamldecode(helm_release.vault.values[0]).server.persistentVolumeClaimRetentionPolicy.whenScaled == "Retain"
    )
    error_message = "data 10Gi and audit 5Gi on the encrypted class, named explicitly (the field is immutable, D26); PVCs outlive the StatefulSet"
  }
  assert {
    condition     = yamldecode(helm_release.vault.values[0]).server.updateStrategyType == "OnDelete"
    error_message = "an apply restarts no Vault pod: upgrades are one pod at a time, by hand"
  }
}

run "vault_release_does_not_wait_and_starts_after_its_identity" {
  command = plan

  assert {
    condition     = helm_release.vault.wait == false
    error_message = "an uninitialised Vault is not ready by design: waiting would fail every first apply (D188)"
  }
  # depends_on and lifecycle are not visible to an assertion: read them from the source, within each resource's own
  # block, with every comment removed first (a commented-out line must not satisfy them).
  assert {
    condition = (
      length(regexall("(?s)resource \"helm_release\" \"vault\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/vault.tf"))) == 1 &&
      can(regex("depends_on\\s*=\\s*\\[[^\\]]*module\\.vault_unseal_pod_identity[,\\]]", replace(regex("(?s)resource \"helm_release\" \"vault\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/vault.tf"))[0], "/(?m)#.*$/", ""))) &&
      can(regex("depends_on\\s*=\\s*\\[[^\\]]*helm_release\\.vault_extras[,\\]]", replace(regex("(?s)resource \"helm_release\" \"vault\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/vault.tf"))[0], "/(?m)#.*$/", "")))
    )
    error_message = "helm_release.vault depends on the unseal pod identity association (credentials are injected at pod creation only) and on the extras (the TLS Secret and the default deny before the first pod)"
  }
  assert {
    condition = (
      can(regex("depends_on\\s*=\\s*\\[[^\\]]*helm_release\\.cluster_issuers[,\\]]", replace(regex("(?s)resource \"helm_release\" \"vault_extras\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/vault.tf"))[0], "/(?m)#.*$/", ""))) &&
      can(regex("depends_on\\s*=\\s*\\[[^\\]]*module\\.vault_snapshot_pod_identity[,\\]]", replace(regex("(?s)resource \"helm_release\" \"vault_extras\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/vault.tf"))[0], "/(?m)#.*$/", "")))
    )
    error_message = "the extras' Certificates need the ryft-vault ClusterIssuer first, and the snapshot CronJob's pods the association (credentials are injected at pod creation only)"
  }
  assert {
    condition = alltrue([for r in ["kubernetes_namespace_v1\" \"vault", "helm_release\" \"vault_extras", "helm_release\" \"vault"] :
      can(regex("(?s)lifecycle \\{\\s*prevent_destroy\\s*=\\s*true\\n", replace(regex("(?s)resource \"${r}\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/vault.tf"))[0], "/(?m)#.*$/", "")))
    ])
    error_message = "prevent_destroy on namespace vault and both releases: removing them is a separate, reviewed change"
  }
  assert {
    condition     = length(helm_release.vault.values) == 1 && helm_release.vault.set == null && helm_release.vault.set_sensitive == null
    error_message = "one values document and no set: what the tests read is what Helm gets"
  }
}

run "vault_unseal_role_may_only_use_its_key" {
  command = plan

  assert {
    condition = (
      length(local.vault_unseal_policy_statements) == 1 &&
      toset(local.vault_unseal_policy_statements[0].actions) == toset(["kms:Encrypt", "kms:Decrypt", "kms:DescribeKey"]) &&
      length(local.vault_unseal_policy_statements[0].actions) == 3 &&
      local.vault_unseal_policy_statements[0].resources == ["arn:aws:kms:eu-central-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"]
    )
    error_message = "the unseal role may Encrypt, Decrypt and DescribeKey on the unseal key's ARN, nothing else"
  }
  assert {
    condition     = data.aws_kms_alias.vault_unseal.name == "alias/ryft-prod-euc1-vault-unseal"
    error_message = "the key is infra/aws's, found by the alias PR 1 creates (alias/<cluster>-vault-unseal)"
  }
  assert {
    condition = (
      module.vault_unseal_pod_identity.associations["vault"].namespace == "vault" &&
      module.vault_unseal_pod_identity.associations["vault"].service_account == "vault" &&
      module.vault_unseal_pod_identity.associations["vault"].cluster_name == "ryft-prod-euc1" &&
      length(module.vault_unseal_pod_identity.associations) == 1 &&
      yamldecode(helm_release.vault.values[0]).server.serviceAccount.name == "vault"
    )
    error_message = "one association, to exactly vault/vault: the service account the chart creates for the server"
  }
  assert {
    condition = local.vault_unseal_trust_conditions == [
      { test = "StringEquals", variable = "aws:RequestTag/kubernetes-namespace", values = ["vault"] },
      { test = "StringEquals", variable = "aws:RequestTag/kubernetes-service-account", values = ["vault"] },
      { test = "StringEquals", variable = "aws:RequestTag/eks-cluster-arn", values = ["arn:aws:eks:eu-central-1:123456789012:cluster/ryft-prod-euc1"] },
    ]
    error_message = "the trust policy requires Pod Identity's session tags for vault/vault in this cluster, so another association of the role (another service account, another cluster) cannot assume it"
  }
  # The module's input is not an output: read it from iam.tf, within the module's own block, comments removed.
  assert {
    condition     = can(regex("\\n  trust_policy_conditions\\s*=\\s*local\\.vault_unseal_trust_conditions\\n", replace(regex("(?s)module \"vault_unseal_pod_identity\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/iam.tf"))[0], "/(?m)#.*$/", "")))
    error_message = "module.vault_unseal_pod_identity gets the trust conditions"
  }
}

run "unseal_key_in_another_region_fails_the_plan" {
  command = plan

  override_data {
    target = data.aws_kms_alias.vault_unseal
    values = {
      target_key_arn = "arn:aws:kms:us-east-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
    }
  }

  expect_failures = [helm_release.vault]
}

run "unseal_key_in_another_account_fails_the_plan" {
  command = plan

  override_data {
    target = data.aws_kms_alias.vault_unseal
    values = {
      target_key_arn = "arn:aws:kms:eu-central-1:210987654321:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
    }
  }

  expect_failures = [helm_release.vault]
}

run "vault_extras_peers_off_by_default_and_the_role_names_the_pods" {
  command = plan

  assert {
    condition = (
      yamldecode(helm_release.vault_extras.values[0]).peers.vso.enabled == false &&
      yamldecode(file("${path.module}/charts/vault-extras/values.yaml")).peers.vso.enabled == false &&
      yamldecode(file("${path.module}/charts/vault-extras/values.yaml")).peers.collector.enabled == false
    )
    error_message = "no ingress from VSO until the first cutover; both peers are off in the chart's defaults"
  }
  assert {
    condition     = yamldecode(helm_release.vault_extras.values[0]).peers.collector.enabled == false
    error_message = "the collector may not reach 8200 (PUT /v1/sys/init is unauthenticated before the init) until var.vault_collector_peer is set after the operator's own init"
  }
  assert {
    condition = (
      strcontains(file("${path.module}/charts/vault-extras/templates/rbac.yaml"), "    verbs: [get, update, patch]\n") &&
      strcontains(file("${path.module}/charts/vault-extras/templates/rbac.yaml"), "      - vault-{{ $i }}\n")
    )
    error_message = "the service-registration Role names the server pods and grants get, update and patch only"
  }
}

run "vault_collector_peer_opens_only_the_collector" {
  command = plan

  variables {
    vault_collector_peer = true
  }

  assert {
    condition = (
      yamldecode(helm_release.vault_extras.values[0]).peers.collector.enabled == true &&
      yamldecode(helm_release.vault_extras.values[0]).peers.vso.enabled == false
    )
    error_message = "vault_collector_peer admits the collector's scrape (job ryft/vault, D194) and leaves VSO off"
  }
}

run "vault_server_certificate_names_every_service_and_pod" {
  command = plan

  assert {
    condition = yamldecode(helm_release.vault_extras.values[0]).tls.dnsNames == [
      "vault", "vault.vault", "vault.vault.svc", "vault.vault.svc.cluster.local",
      "vault-active", "vault-active.vault.svc", "vault-active.vault.svc.cluster.local",
      "vault-standby", "vault-standby.vault.svc",
      "vault-internal", "vault-internal.vault.svc",
      "vault-0.vault-internal", "vault-1.vault-internal", "vault-2.vault-internal",
      "vault-0.vault-internal.vault.svc", "vault-1.vault-internal.vault.svc", "vault-2.vault-internal.vault.svc",
      "localhost",
    ]
    error_message = "the server certificate carries the design's DNS name list (section 4)"
  }
  assert {
    condition     = length([for n in yamldecode(helm_release.vault_extras.values[0]).tls.dnsNames : n if can(regex("^vault-[0-9]+\\.vault-internal$", n))]) == yamldecode(helm_release.vault.values[0]).server.ha.replicas
    error_message = "one vault-N.vault-internal name per replica: retry_join reaches each peer by it"
  }
  assert {
    condition     = yamldecode(helm_release.vault_extras.values[0]).tls.ipAddresses == ["127.0.0.1"] && yamldecode(helm_release.vault_extras.values[0]).vpcCIDR == "10.0.0.0/16"
    error_message = "127.0.0.1 for the port-forward and the in-pod CLI; the VPC range for the API server egress"
  }
  assert {
    condition     = yamldecode(file("${path.module}/charts/cluster-issuers/values.yaml")).vaultCA.enabled == true && strcontains(file("${path.module}/charts/cluster-issuers/templates/vault-ca.yaml"), "name: ryft-vault\n")
    error_message = "the cluster-issuers chart renders the ClusterIssuer ryft-vault by default"
  }
}

# --- The Raft snapshot CronJob (charts/vault-extras templates/snapshot.yaml, iam.tf; D192) ---
# The render itself (script, securityContext, token) is scripts/tests/vault_extras_chart_test.sh's.

run "vault_snapshot_role_may_only_put_snapshots" {
  command = plan

  assert {
    condition = local.vault_snapshot_policy_statements == [
      {
        sid       = "PutSnapshots"
        actions   = ["s3:PutObject"]
        resources = ["arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012/snapshots/*"]
        condition = [
          { test = "StringEquals", variable = "s3:ResourceAccount", values = ["123456789012"] },
          { test = "StringEquals", variable = "s3:x-amz-server-side-encryption-aws-kms-key-id", values = ["arn:aws:kms:eu-central-1:123456789012:key/7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"] },
        ]
      },
      {
        sid       = "SnapshotDataKey"
        actions   = ["kms:GenerateDataKey"]
        resources = ["arn:aws:kms:eu-central-1:123456789012:key/7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"]
        condition = [
          { test = "StringEquals", variable = "kms:ViaService", values = ["s3.eu-central-1.amazonaws.com"] },
          { test = "StringEquals", variable = "kms:EncryptionContext:aws:s3:arn", values = ["arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012"] },
        ]
      },
    ]
    error_message = "the snapshot role: PutObject under snapshots/ of this account's bucket naming the snapshot key's ARN, and GenerateDataKey on that key through S3 in this region for this bucket; nothing else (no Get, List, Delete, Decrypt)"
  }
  assert {
    condition     = length(distinct(flatten([for s in local.vault_snapshot_policy_statements : s.actions]))) == 2
    error_message = "exactly two actions in the snapshot role"
  }
  assert {
    condition     = data.aws_kms_alias.vault_snapshots.name == "alias/ryft-prod-euc1-vault-snapshots" && local.vault_snapshots_bucket == "ryft-prod-euc1-vault-snapshots-123456789012"
    error_message = "the key and the bucket are infra/aws's (alias/<cluster>-vault-snapshots, <cluster>-vault-snapshots-<account>)"
  }
  assert {
    condition     = !contains(flatten([for s in local.vault_snapshot_policy_statements : s.resources]), data.aws_kms_alias.vault_unseal.target_key_arn)
    error_message = "the snapshot role never names the unseal key"
  }
  assert {
    condition = (
      module.vault_snapshot_pod_identity.associations["snapshot"].namespace == "vault" &&
      module.vault_snapshot_pod_identity.associations["snapshot"].service_account == "vault-snapshot" &&
      module.vault_snapshot_pod_identity.associations["snapshot"].cluster_name == "ryft-prod-euc1" &&
      length(module.vault_snapshot_pod_identity.associations) == 1
    )
    error_message = "one association, to exactly vault/vault-snapshot (the CronJob's service account)"
  }
  assert {
    condition = local.vault_snapshot_trust_conditions == [
      { test = "StringEquals", variable = "aws:RequestTag/kubernetes-namespace", values = ["vault"] },
      { test = "StringEquals", variable = "aws:RequestTag/kubernetes-service-account", values = ["vault-snapshot"] },
      { test = "StringEquals", variable = "aws:RequestTag/eks-cluster-arn", values = ["arn:aws:eks:eu-central-1:123456789012:cluster/ryft-prod-euc1"] },
    ]
    error_message = "the trust policy requires Pod Identity's session tags for vault/vault-snapshot in this cluster"
  }
  assert {
    condition = (
      can(regex("\\n  trust_policy_conditions\\s*=\\s*local\\.vault_snapshot_trust_conditions\\n", replace(regex("(?s)module \"vault_snapshot_pod_identity\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/iam.tf"))[0], "/(?m)#.*$/", ""))) &&
      can(regex("\\n  policy_statements\\s*=\\s*local\\.vault_snapshot_policy_statements\\n", replace(regex("(?s)module \"vault_snapshot_pod_identity\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/iam.tf"))[0], "/(?m)#.*$/", ""))) &&
      !can(regex("attach_[a-z0-9_]+_policy\\s*=\\s*true", replace(replace(regex("(?s)module \"vault_snapshot_pod_identity\" \\{\\n(.*?)\\n\\}\\n", file("${path.module}/iam.tf"))[0], "/(?m)#.*$/", ""), "attach_custom_policy", "")))
    )
    error_message = "module.vault_snapshot_pod_identity gets exactly these statements and trust conditions, and no module-provided policy"
  }
}

run "vault_snapshot_job_runs_every_6_hours_with_pinned_images" {
  command = plan

  assert {
    condition     = yamldecode(helm_release.vault_extras.values[0]).snapshot.schedule == "17 */6 * * *"
    error_message = "every 6 hours at minute 17 (RPO 6 h; VaultSnapshotStale pages at 13 h)"
  }
  assert {
    condition = (
      strcontains(file("${path.module}/charts/vault-extras/templates/snapshot.yaml"), "\n  concurrencyPolicy: Forbid\n") &&
      strcontains(file("${path.module}/charts/vault-extras/templates/snapshot.yaml"), "\n  successfulJobsHistoryLimit: 3\n") &&
      strcontains(file("${path.module}/charts/vault-extras/templates/snapshot.yaml"), "\n  failedJobsHistoryLimit: 3\n")
    )
    error_message = "the CronJob never overlaps a run (Forbid) and keeps three runs of each outcome"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault_extras.values[0]).snapshot.images.vault == "docker.io/hashicorp/vault:2.2.1@sha256:962a92876e168d68eaaa24b0b8b49bbce1cb63024060a67bba95cd158a366fc2" &&
      yamldecode(helm_release.vault_extras.values[0]).snapshot.images.vault == "${yamldecode(helm_release.vault.values[0]).server.image.repository}:${yamldecode(helm_release.vault.values[0]).server.image.tag}"
    )
    error_message = "the init container runs the server's own pinned image (one pin, one P2 rule)"
  }
  assert {
    condition     = yamldecode(helm_release.vault_extras.values[0]).snapshot.images.awsCli == "public.ecr.aws/aws-cli/aws-cli:2.39.5@sha256:0cea63506f28774b70b1a3dd1934f49bc202d9784dee23370acc81438e8d0292"
    error_message = "the upload runs the AWS CLI 2.39.5 by its multi-arch index digest"
  }
  assert {
    condition = alltrue([
      for k in ["vault", "awsCli"] :
      yamldecode(file("${path.module}/charts/admission-policy/values.yaml")).images[k] != "" &&
      can(regex(yamldecode(file("${path.module}/charts/admission-policy/values.yaml")).images[k], yamldecode(helm_release.vault_extras.values[0]).snapshot.images[k]))
    ])
    error_message = "the admission chart's P2 (images.vault, images.awsCli) admits exactly the images the job runs: change both together"
  }
  assert {
    condition = (
      yamldecode(helm_release.vault_extras.values[0]).snapshot.bucket == "ryft-prod-euc1-vault-snapshots-123456789012" &&
      yamldecode(helm_release.vault_extras.values[0]).snapshot.accountID == "123456789012" &&
      yamldecode(helm_release.vault_extras.values[0]).snapshot.region == "eu-central-1" &&
      yamldecode(helm_release.vault_extras.values[0]).snapshot.keyARN == "arn:aws:kms:eu-central-1:123456789012:key/7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"
    )
    error_message = "the job names the bucket, its owner account (--expected-bucket-owner), the region and the snapshot key's full ARN (not the alias, not the unseal key)"
  }
}

run "snapshot_key_in_another_account_fails_the_plan" {
  command = plan

  override_data {
    target = data.aws_kms_alias.vault_snapshots
    values = {
      target_key_arn = "arn:aws:kms:eu-central-1:210987654321:key/7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"
    }
  }

  expect_failures = [data.aws_kms_alias.vault_snapshots]
}

run "snapshot_key_in_another_region_fails_the_plan" {
  command = plan

  override_data {
    target = data.aws_kms_alias.vault_snapshots
    values = {
      target_key_arn = "arn:aws:kms:us-east-1:123456789012:key/7e8f9a0b-1c2d-4e3f-9a8b-7c6d5e4f3a2b"
    }
  }

  expect_failures = [data.aws_kms_alias.vault_snapshots]
}
