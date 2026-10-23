# Unit tests of admission.tf (D120): AWS, Helm and Kubernetes providers are mocked; nothing
# is created, and the committed admission-values.yaml is the input.
#   terraform init -backend=false && terraform test

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
# The cluster as it should look after a Phase 1 apply: every binding Warn+Audit. A run that
# wants other actions meets the drift check, and says so with expect_failures.
mock_provider "kubernetes" {
  mock_data "kubernetes_resources" {
    defaults = {
      objects = [
        { metadata = { labels = { "admission.ryft.dev/key" = "vso" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "operators-cluster" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "broker" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "namespaces" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "workloads-templates" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "netpol" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "auth-env" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "traefik" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "workloads-pods" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "operator-reach" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "argocd" } }, spec = { validationActions = ["Warn", "Audit"] } },
        { metadata = { labels = { "admission.ryft.dev/key" = "argocd-operation" } }, spec = { validationActions = ["Warn", "Audit"] } }
      ]
    }
  }
}

run "disabled_creates_nothing" {
  command = plan

  assert {
    condition     = local.admission_file.enabled == false && length(helm_release.admission_policy) == 0
    error_message = "admission-values.yaml ships enabled: false, and then the release must not exist"
  }

  assert {
    condition     = output.admission_enabled == false
    error_message = "the output must report the release as off"
  }
}

run "the_committed_file_names_every_binding_of_the_chart" {
  command = plan

  assert {
    condition     = toset(keys(local.admission_file.actions)) == toset(local.admission_keys) && length(local.admission_keys) == 12
    error_message = "admission-values.yaml must set an action for each of the chart's twelve bindings (P1-P11, P2 twice)"
  }

  assert {
    condition     = alltrue([for k, v in local.admission_file.actions : v == "audit"])
    error_message = "Phase 0/1: every committed action is audit; a step to deny is a reviewed change with its own PR"
  }
}

run "audit_by_default" {
  command = plan

  variables {
    admission_enabled = true
  }

  assert {
    condition     = length(helm_release.admission_policy) == 1
    error_message = "enabled must create the release"
  }

  assert {
    condition     = alltrue([for k, v in local.admission_effective : v == ["Warn", "Audit"]]) && length(local.admission_effective) == 12
    error_message = "by default every binding is Warn+Audit"
  }

  assert {
    condition     = yamldecode(helm_release.admission_policy[0].values[0]).breakGlass == false && alltrue([for k, v in yamldecode(helm_release.admission_policy[0].values[0]).actions : v == "audit"])
    error_message = "the chart must receive audit for every binding and no break-glass"
  }

  assert {
    condition     = helm_release.admission_policy[0].namespace == "kube-system" && helm_release.admission_policy[0].name == "admission-policy"
    error_message = "the break-glass runbook names helm -n kube-system admission-policy"
  }

  assert {
    condition     = !contains(keys(yamldecode(helm_release.admission_policy[0].values[0])), "enabled")
    error_message = "enabled is Terraform's flag, not a chart value"
  }

  assert {
    condition     = yamldecode(helm_release.admission_policy[0].values[0]).vpcCIDR == var.vpc_cidr
    error_message = "P3 must receive the VPC CIDR the node-metrics scrape uses"
  }
}

run "break_glass_is_warn_and_audit_everywhere" {
  command = plan

  variables {
    admission_enabled     = true
    admission_break_glass = true
    admission_actions = {
      vso               = "deny"
      namespaces        = "deny"
      workloads-pods    = "deny"
      argocd            = "deny"
      argocd-operation  = "deny"
      operators-cluster = "deny"
    }
  }

  assert {
    condition     = alltrue([for k, v in local.admission_effective : v == ["Warn", "Audit"]])
    error_message = "break-glass must give Warn+Audit to every binding, deny or not"
  }

  assert {
    condition     = yamldecode(helm_release.admission_policy[0].values[0]).breakGlass == true
    error_message = "the chart must be told about break-glass (it labels and sets the actions itself)"
  }

  assert {
    condition     = output.admission_binding_actions["workloads-pods"] == ["Warn", "Audit"]
    error_message = "the output shows the effective actions"
  }
}

run "deny_never_meets_warn" {
  command = plan

  variables {
    admission_enabled = true
    admission_actions = {
      vso               = "deny"
      broker            = "deny"
      operators-cluster = "deny"
      workloads-pods    = "deny"
    }
  }

  # the mocked cluster is all Warn+Audit, so asking for deny is drift to the check
  expect_failures = [check.admission_binding_drift]

  assert {
    condition     = local.admission_effective["vso"] == ["Deny", "Audit"] && local.admission_effective["workloads-pods"] == ["Deny", "Audit"] && local.admission_effective["netpol"] == ["Warn", "Audit"]
    error_message = "deny is Deny+Audit, everything else Warn+Audit"
  }

  assert {
    condition     = alltrue([for k, v in local.admission_effective : !(contains(v, "Deny") && contains(v, "Warn")) && contains(v, "Audit")])
    error_message = "Deny must never be combined with Warn (the API server refuses it), and every binding audits"
  }
}

run "rejects_an_action_that_is_not_audit_or_deny" {
  command = plan

  variables {
    admission_actions = { vso = "block" }
  }

  expect_failures = [var.admission_actions]
}

run "rejects_a_binding_the_chart_does_not_have" {
  command = plan

  variables {
    admission_actions = { nonsense = "deny" }
  }

  expect_failures = [var.admission_actions]
}

run "vault_address_matches_the_generated_vault_yaml" {
  command = plan

  assert {
    condition     = length(fileset(local.vault_namespaces_dir, "*/vault.yaml")) >= 8
    error_message = "the generated deploy/k8s/overlays/vault-namespaces/*/vault.yaml were not found: the test would compare nothing"
  }

  assert {
    condition     = length(local.vault_addresses) == 1 && local.vault_addresses[0] == local.admission_file.vaultAddress
    error_message = "vaultAddress in admission-values.yaml must equal the address in every generated vault.yaml (infra/vault); P4 pins it"
  }

  assert {
    condition     = startswith(local.admission_file.vaultAddress, "https://") && !endswith(local.admission_file.vaultAddress, "/")
    error_message = "the address is an https URL without a trailing slash, as infra/vault writes it"
  }
}

run "argocd_names_in_the_file_are_the_chart_defaults" {
  command = plan

  assert {
    condition = (
      local.admission_file.argocd.namespace == local.admission_defaults.argocd.namespace &&
      local.admission_file.argocd.controllerServiceAccount == local.admission_defaults.argocd.controllerServiceAccount &&
      local.admission_file.argocd.serverServiceAccount == local.admission_defaults.argocd.serverServiceAccount
    )
    error_message = "the committed Argo CD identities differ from the chart's defaults; was a service account renamed in argocd.tf?"
  }
}

run "p7_on_deny_without_projects_is_flagged" {
  command = plan

  variables {
    admission_enabled = true
    admission_actions = { argocd = "deny" }
  }

  # both the unwired constant and the drift against the mocked all-audit cluster
  expect_failures = [check.admission_projects_wired, check.admission_binding_drift]

  assert {
    condition     = local.admission_effective["argocd"] == ["Deny", "Audit"]
    error_message = "the request itself is honoured; the checks warn about it"
  }
}

run "p7_has_no_projects_until_t12b" {
  command = plan

  variables {
    admission_enabled = true
  }

  assert {
    condition     = yamldecode(helm_release.admission_policy[0].values[0]).projects == []
    error_message = "T12-b wires the decoded project.yaml; until then P7 gets an empty constant"
  }
}
