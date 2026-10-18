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
mock_provider "kubernetes" {}

run "chart_versions_are_pinned" {
  command = plan

  assert {
    condition = (
      helm_release.aws_load_balancer_controller.version == "3.6.0" &&
      helm_release.traefik.version == "42.0.0" &&
      helm_release.cert_manager.version == "v1.21.2" &&
      helm_release.external_dns.version == "1.23.0" &&
      helm_release.vault_secrets_operator.version == "1.6.0"
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

