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

mock_provider "kubernetes" {}

# The cluster as it should look after a Phase 1 apply, and the chart's render of it: the
# same eleven policies and twelve bindings, every binding Warn+Audit. The drift checks compare
# the two; a run that wants to see a drift overrides one side.
override_data {
  target = data.helm_template.admission
  values = {
    manifests = {
      "admission-policy/templates/all.yaml" = <<-EOT
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-vso
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-operators-cluster
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-broker
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-namespaces
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-workloads
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-netpol
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-auth-env
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-traefik
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-operator-reach
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-argocd
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-argocd-operation
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: [things]
                operations: [CREATE]
          matchConditions:
            - name: m
              expression: "true"
          variables:
            - name: v
              expression: "true"
          validations:
            - expression: "true"

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-vso
        spec:
          policyName: ryft-vso
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-operators-cluster
        spec:
          policyName: ryft-operators-cluster
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-broker
        spec:
          policyName: ryft-broker
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-namespaces
        spec:
          policyName: ryft-namespaces
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-workloads-templates
        spec:
          policyName: ryft-workloads
          validationActions: [Warn, Audit]
          matchResources:
            resourceRules:
              - resources: [deployments]
                operations: [CREATE]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-netpol
        spec:
          policyName: ryft-netpol
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-auth-env
        spec:
          policyName: ryft-auth-env
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-traefik
        spec:
          policyName: ryft-traefik
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-workloads-pods
        spec:
          policyName: ryft-workloads
          validationActions: [Warn, Audit]
          matchResources:
            resourceRules:
              - resources: [pods]
                operations: [CREATE]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-operator-reach
        spec:
          policyName: ryft-operator-reach
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-argocd
        spec:
          policyName: ryft-argocd
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-argocd-operation
        spec:
          policyName: ryft-argocd-operation
          validationActions: [Warn, Audit]
      EOT
    }
  }
}

override_data {
  target = data.kubernetes_resources.admission_policies
  values = {
    objects = [
      { metadata = { name = "ryft-vso" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-operators-cluster" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-broker" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-namespaces" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-workloads" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-netpol" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-auth-env" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-traefik" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-operator-reach" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
    ]
  }
}

override_data {
  target = data.kubernetes_resources.admission_bindings
  values = {
    objects = [
      { metadata = { name = "ryft-vso" }, spec = { policyName = "ryft-vso", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-operators-cluster" }, spec = { policyName = "ryft-operators-cluster", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-broker" }, spec = { policyName = "ryft-broker", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-namespaces" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-workloads-templates" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["deployments"], operations = ["CREATE"] }] } } },
      { metadata = { name = "ryft-netpol" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-auth-env" }, spec = { policyName = "ryft-auth-env", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-traefik" }, spec = { policyName = "ryft-traefik", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
      { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
    ]
  }
}

run "disabled_creates_nothing" {
  command = plan

  assert {
    condition     = local.admission_file.enabled == false && length(helm_release.admission_policy) == 0
    error_message = "admission-values.yaml ships enabled: false, and then the release must not exist"
  }

  assert {
    condition     = length(data.helm_template.admission) == 0 && length(data.kubernetes_resources.admission_policies) == 0 && length(data.kubernetes_resources.admission_bindings) == 0
    error_message = "a disabled admission reads and renders nothing"
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

  expect_failures = [check.admission_enabled_override]

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

  assert {
    condition     = data.helm_template.admission[0].values == helm_release.admission_policy[0].values
    error_message = "the drift check must render the chart with exactly the release's values"
  }

  assert {
    condition     = length(local.admission_rendered_policies) == 11 && length(local.admission_rendered_bindings) == 12
    error_message = "the mocked render holds the eleven policies and twelve bindings the drift checks compare"
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
      argocd-operation  = "deny"
      operators-cluster = "deny"
    }
  }

  expect_failures = [check.admission_enabled_override]

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

run "break_glass_also_lets_a_guarded_deny_through" {
  command = plan

  variables {
    admission_enabled     = true
    admission_break_glass = true
    admission_actions     = { argocd = "deny", netpol = "deny" }
  }

  expect_failures = [check.admission_enabled_override]

  assert {
    condition     = length(helm_release.admission_policy) == 1
    error_message = "under break-glass the guards (which concern a deny) do not fire"
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

  expect_failures = [check.admission_enabled_override]

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

run "refuses_argocd_deny_without_projects" {
  command = plan

  variables {
    admission_enabled = true
    admission_actions = { argocd = "deny" }
  }

  expect_failures = [helm_release.admission_policy, check.admission_enabled_override]
}

run "refuses_netpol_deny_while_the_broker_egress_is_open" {
  command = plan

  variables {
    admission_enabled = true
    admission_actions = { netpol = "deny" }
  }

  assert {
    condition     = local.admission_broker_egress_public == true
    error_message = "the committed file keeps the open rule until T12-k closes it; this test then needs the flag"
  }

  expect_failures = [helm_release.admission_policy, check.admission_enabled_override]
}

run "the_variable_override_is_flagged" {
  command = plan

  variables {
    admission_enabled = true
  }

  expect_failures = [check.admission_enabled_override]
}

run "vault_address_matches_the_generated_vault_yaml" {
  command = plan

  assert {
    condition     = length(fileset(local.vault_namespaces_dir, "*/vault.yaml")) >= 8
    error_message = "the generated deploy/k8s/overlays/vault-namespaces/*/vault.yaml were not found: the test would compare nothing"
  }

  assert {
    condition     = alltrue([for f, a in local.vault_address_by_file : length(a) == 1])
    error_message = "every generated vault.yaml holds exactly one VaultConnection address"
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

run "argocd_and_vpc_values_follow_their_sources" {
  command = plan

  assert {
    condition = (
      local.admission_file.argocd.namespace == local.admission_defaults.argocd.namespace &&
      local.admission_file.argocd.controllerServiceAccount == local.admission_defaults.argocd.controllerServiceAccount &&
      local.admission_file.argocd.serverServiceAccount == local.admission_defaults.argocd.serverServiceAccount
    )
    error_message = "the committed Argo CD identities differ from the chart's defaults"
  }

  assert {
    condition = (
      strcontains(file("${path.module}/argocd.tf"), "name = \"${local.admission_file.argocd.namespace}\"") &&
      strcontains(file("${path.module}/argocd.tf"), "\"${local.admission_file.argocd.controllerServiceAccount}\"") &&
      strcontains(file("${path.module}/argocd.tf"), "\"${local.admission_file.argocd.serverServiceAccount}\"")
    )
    error_message = "argocd.tf no longer names the namespace or service accounts the policies judge (P1, P6, P7, P10): was one renamed?"
  }

  assert {
    condition     = var.vpc_cidr == local.admission_defaults.vpcCIDR
    error_message = "the chart's default vpcCIDR differs from this root's vpc_cidr default"
  }

  assert {
    condition     = can(regex("variable \"vpc_cidr\" \\{[^}]*default\\s*=\\s*\"${replace(var.vpc_cidr, ".", "\\.")}\"", file("${path.module}/../variables.tf")))
    error_message = "infra/aws' vpc_cidr default differs from this root's (and the chart's) copy"
  }
}

run "p7_has_no_projects_until_t12b" {
  command = plan

  variables {
    admission_enabled = true
  }

  expect_failures = [check.admission_enabled_override]

  assert {
    condition     = yamldecode(helm_release.admission_policy[0].values[0]).projects == []
    error_message = "T12-b wires the decoded project.yaml; until then P7 gets an empty constant"
  }
}

run "drift_a_missing_binding" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_bindings
    values = {
      objects = [
        { metadata = { name = "ryft-operators-cluster" }, spec = { policyName = "ryft-operators-cluster", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-broker" }, spec = { policyName = "ryft-broker", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-templates" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["deployments"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-netpol" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-auth-env" }, spec = { policyName = "ryft-auth-env", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-traefik" }, spec = { policyName = "ryft-traefik", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]
}

run "drift_a_binding_left_in_the_wrong_action" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_bindings
    values = {
      objects = [
        { metadata = { name = "ryft-vso" }, spec = { policyName = "ryft-vso", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-operators-cluster" }, spec = { policyName = "ryft-operators-cluster", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-broker" }, spec = { policyName = "ryft-broker", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-templates" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["deployments"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-netpol" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-auth-env" }, spec = { policyName = "ryft-auth-env", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-traefik" }, spec = { policyName = "ryft-traefik", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Deny", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]
}

run "drift_a_binding_pointed_at_another_policy" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_bindings
    values = {
      objects = [
        { metadata = { name = "ryft-vso" }, spec = { policyName = "ryft-vso", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-operators-cluster" }, spec = { policyName = "ryft-operators-cluster", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-broker" }, spec = { policyName = "ryft-broker", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-templates" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["deployments"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-netpol" }, spec = { policyName = "ryft-other", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-auth-env" }, spec = { policyName = "ryft-auth-env", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-traefik" }, spec = { policyName = "ryft-traefik", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]
}

run "drift_a_missing_policy" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_policies
    values = {
      objects = [
        { metadata = { name = "ryft-vso" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-operators-cluster" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-broker" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-namespaces" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-workloads" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-netpol" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-auth-env" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-traefik" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-operator-reach" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]
}

run "drift_an_edited_policy" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_policies
    values = {
      objects = [
        { metadata = { name = "ryft-vso" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "false" }] } },
        { metadata = { name = "ryft-operators-cluster" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-broker" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-namespaces" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-workloads" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-netpol" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-auth-env" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-traefik" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-operator-reach" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]
}
