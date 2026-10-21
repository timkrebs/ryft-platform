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
    yamldecode(helm_release.argocd.values[0])[c].replicas == 1 && can(yamldecode(helm_release.argocd.values[0])[c].resources.requests.cpu)])
    error_message = "non-HA, and every component with explicit requests (the nodes are CPU-bound, D94)"
  }
  assert {
    condition     = yamldecode(helm_release.argocd.values[0]).configs.secret.argocdServerAdminPassword == "$2a$10$mockbcrypthash"
    error_message = "the admin password is Terraform's bcrypt hash, so Argo CD writes no argocd-initial-admin-secret"
  }
  assert {
    condition     = can(yamldecode(helm_release.argocd.values[0]).configs.cm["resource.customizations.health.argoproj.io_Application"])
    error_message = "Application health must be restored, or the app-of-apps sync waves do not wait for each other"
  }
  assert {
    condition     = kubernetes_namespace_v1.argocd.metadata[0].labels["pod-security.kubernetes.io/enforce"] == "restricted"
    error_message = "the argocd namespace enforces Pod Security restricted"
  }
}
