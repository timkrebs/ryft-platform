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
  # Vault's unseal key (vault.tf's check wants an ARN of this account and region).
  mock_data "aws_kms_alias" {
    defaults = {
      target_key_arn = "arn:aws:kms:eu-central-1:123456789012:key/0d6f1a2b-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
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
# same thirteen policies and eighteen bindings (P12 and P13 included; P1, P2 twice and P3 also for vault), every binding Warn+Audit. The drift checks compare
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
        kind: ValidatingAdmissionPolicy
        metadata:
          name: ryft-argo-scope
        spec:
          failurePolicy: Fail
          matchConstraints:
            resourceRules:
              - resources: ["*/*"]
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
          name: ryft-vault-trust
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

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-argo-scope
        spec:
          policyName: ryft-argo-scope
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-vault-trust
        spec:
          policyName: ryft-vault-trust
          validationActions: [Warn, Audit]

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-namespaces-vault
        spec:
          policyName: ryft-namespaces
          validationActions: [Warn, Audit]
          matchResources:
            namespaceSelector:
              matchExpressions:
                - { key: kubernetes.io/metadata.name, operator: In, values: [vault] }

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-workloads-templates-vault
        spec:
          policyName: ryft-workloads
          validationActions: [Warn, Audit]
          matchResources:
            namespaceSelector:
              matchExpressions:
                - { key: kubernetes.io/metadata.name, operator: In, values: [vault] }

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-workloads-pods-vault
        spec:
          policyName: ryft-workloads
          validationActions: [Warn, Audit]
          matchResources:
            namespaceSelector:
              matchExpressions:
                - { key: kubernetes.io/metadata.name, operator: In, values: [vault] }

        ---
        apiVersion: admissionregistration.k8s.io/v1
        kind: ValidatingAdmissionPolicyBinding
        metadata:
          name: ryft-netpol-vault
        spec:
          policyName: ryft-netpol
          validationActions: [Warn, Audit]
          matchResources:
            namespaceSelector:
              matchExpressions:
                - { key: kubernetes.io/metadata.name, operator: In, values: [vault] }
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
      { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
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
      { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
      { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
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
    condition     = toset(keys(local.admission_file.actions)) == toset(local.admission_keys) && length(local.admission_keys) == 18
    error_message = "admission-values.yaml must set an action for each of the chart's eighteen bindings (P1-P13, P2 twice, and the vault bindings of P1, P2 (twice) and P3)"
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
    condition     = alltrue([for k, v in local.admission_effective : v == ["Warn", "Audit"]]) && length(local.admission_effective) == 18
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
    condition     = length(local.admission_rendered_policies) == 13 && length(local.admission_rendered_bindings) == 18
    error_message = "the mocked render holds the thirteen policies and eighteen bindings the drift checks compare"
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
    admission_scoped_rbac_file = "admission-values.yaml"
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

  # The check's own condition on the committed tree (a check failure is only a warning in a run, so
  # the run asserts it; the runs below feed it bad trees and expect the check to fail).
  assert {
    condition     = local.admission_vault_address_ok
    error_message = "check.admission_vault_address fails on the committed tree: a generated vault.yaml (infra/vault) names an address that is not in vaultAddresses of admission-values.yaml, or holds not exactly one"
  }

  assert {
    condition     = !contains(keys(local.admission_file), "vaultAddress")
    error_message = "vaultAddress is now vaultAddresses (a list); the chart refuses the old key"
  }

  assert {
    condition     = alltrue([for a in local.admission_file.vaultAddresses : startswith(a, "https://") && !endswith(a, "/")]) && length(distinct(local.admission_file.vaultAddresses)) == length(local.admission_file.vaultAddresses)
    error_message = "every address is an https URL without a trailing slash, as infra/vault writes it, and none is listed twice"
  }

  assert {
    condition     = contains(local.admission_file.vaultAddresses, "https://vault.vault.svc:8200") && length(local.admission_file.vaultAddresses) <= 2
    error_message = "during the cutover vaultAddresses holds HCP's address and the in-cluster Vault's (https://vault.vault.svc:8200, design section 5), nothing else; P4 must admit the in-cluster shape before the first cutover PR"
  }
}

# The cutover: a tree where one namespace names the in-cluster Vault and another still HCP's passes
# the check; an address in no list, or a file with two addresses, fails it.
run "vault_address_check_admits_both_targets_during_the_cutover" {
  command = plan

  variables {
    admission_vault_namespaces_dir = "tests/fixtures/vault-namespaces-cutover"
  }

  assert {
    condition     = local.admission_vault_address_ok && length(local.vault_addresses) == 2
    error_message = "a tree half moved to the in-cluster Vault must pass check.admission_vault_address"
  }
}

run "vault_address_check_refuses_a_foreign_address" {
  command = plan

  variables {
    admission_vault_namespaces_dir = "tests/fixtures/vault-namespaces-foreign"
  }

  expect_failures = [check.admission_vault_address]
}

run "vault_address_check_refuses_two_addresses_in_one_file" {
  command = plan

  variables {
    admission_vault_namespaces_dir = "tests/fixtures/vault-namespaces-two"
  }

  expect_failures = [check.admission_vault_address]
}

run "vault_address_check_refuses_an_empty_tree" {
  command = plan

  variables {
    admission_vault_namespaces_dir = "tests/fixtures/no-such-dir"
  }

  expect_failures = [check.admission_vault_address]
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

run "argo_scope_ships_in_audit_with_the_scope_of_the_design" {
  command = plan

  assert {
    condition     = contains(local.admission_keys, "argo-scope") && local.admission_file.actions["argo-scope"] == "audit" && local.admission_effective["argo-scope"] == ["Warn", "Audit"]
    error_message = "P12 ships in Warn+Audit like the other policies (Deny is a reviewed step, Step B)"
  }

  assert {
    condition = (
      !contains(keys(local.admission_defaults.argoScope), "namespacePrefixes") &&
      local.admission_defaults.argoScope.namespaces == ["ryft-api", "ryft-ingest", "ryft-migrate", "ryft-notify", "ryft-rabbitmq", "ryft-scoring", "ryft-smoke", "ryft-web", "rabbitmq-system"] &&
      local.admission_defaults.argoScope.requireClosedRoutes == true &&
      local.admission_defaults.argoScope.namespaceObjects == ["ryft-web", "rabbitmq-system"] &&
      local.admission_defaults.argoScope.crdGroups == ["rabbitmq.com"] &&
      local.admission_defaults.argoScope.serverSecret == "argocd-secret" &&
      local.admission_defaults.argoScope.serverInitKey == "server.secretkey" &&
      local.admission_defaults.argoScope.redisSecretInit.serviceAccount == "argocd-redis-secret-init" &&
      local.admission_defaults.argoScope.redisSecretInit.secret == "argocd-redis"
    )
    error_message = "the chart's argoScope defaults differ from the design (p12-argo-scope.md): a change of where Argo CD may write is a reviewed decision"
  }

  assert {
    condition     = !contains(keys(local.admission_file), "argoScope")
    error_message = "production overrides nothing of argoScope (the chart's defaults are the scope); an override needs its own reason in admission-values.yaml and this test"
  }

  assert {
    condition     = can(regex("depends_on\\s*=\\s*\\[helm_release\\.argocd\\]", file("${path.module}/admission.tf")))
    error_message = "helm_release.admission_policy must depend on helm_release.argocd: on a fresh cluster Argo CD writes its first server.secretkey before any policy exists (P12)"
  }
}

# The namespaces of P12 are an explicit list, and every Application of deploy/argocd/apps is synced into one.
run "argo_scope_namespaces_cover_the_apps_and_are_no_prefix" {
  command = plan

  assert {
    condition     = length(local.admission_app_destinations) >= 8 && alltrue([for n in local.admission_app_destinations : contains(local.admission_defaults.argoScope.namespaces, n)])
    error_message = "every destination namespace of deploy/argocd/apps must be in argoScope.namespaces (check admission_argo_scope)"
  }

  assert {
    condition     = !contains(local.admission_defaults.argoScope.namespaces, local.admission_argocd_namespace) && alltrue([for n in local.admission_defaults.argoScope.namespaces : !strcontains(n, "*")])
    error_message = "argoScope.namespaces holds names only, never the argocd namespace (there the controller may write Applications and events only)"
  }
}

# vault and cert-manager are never in P12's scope (D206): the never-list is built from the namespaces this
# root creates (the Vault add-on's, cert-manager's release, Argo CD's) and the chart's vault values, and the
# committed scope holds none of them, so the precondition and check "admission_argo_scope_never" pass. (The
# chart refuses the same render: scripts/tests/admission_chart_guards_test.sh lists vault and cert-manager.)
run "argo_scope_never_holds_vault_or_cert_manager" {
  command = plan

  assert {
    condition = toset(local.admission_argo_never) == toset([
      "vault", "cert-manager", "argocd", "kube-system",
      "external-dns", "vault-secrets-operator-system", "amazon-cloudwatch", "monitoring", "default", "kube-public", "kube-node-lease",
    ])
    error_message = "the never-list must be vault, cert-manager, argocd, kube-system and every add-on namespace (S3): ${join(", ", local.admission_argo_never)}"
  }

  assert {
    condition     = length(local.admission_argo_never_listed) == 0 && !contains(local.admission_defaults.argoScope.namespaces, "vault") && !contains(local.admission_defaults.argoScope.namespaces, "cert-manager")
    error_message = "argoScope.namespaces and namespaceObjects must not hold vault, cert-manager, argocd or kube-system"
  }

  assert {
    condition     = alltrue([for n in local.admission_defaults.argoScope.namespaceObjects : startswith(n, "ryft-") || n == "rabbitmq-system"])
    error_message = "every Namespace object Argo CD may create is one P1 holds to Pod Security restricted (ryft-* or rabbitmq-system)"
  }
}

# S3: every namespace a Helm release of this root installs into is on the chart's own never-list (so a render
# outside Terraform refuses it too), and argoScope.namespaces holds ryft-* or the operators' namespace only.
# Five today: kube-system (ALB controller, Traefik), cert-manager (twice), external-dns, VSO's, amazon-cloudwatch.
run "argo_scope_never_covers_every_addon_namespace" {
  command = plan

  assert {
    condition     = length(local.admission_addon_namespaces) == 5 && length(local.admission_argo_never_uncovered) == 0
    error_message = "an add-on namespace of infra/aws/kubernetes is not on the chart's never-list (argoScope.neverNamespaces): ${join(", ", local.admission_argo_never_uncovered)}"
  }

  assert {
    condition     = length(local.admission_argo_scope_off_shape) == 0
    error_message = "argoScope.namespaces holds a namespace that is neither ryft-* nor the operators' namespace: ${join(", ", local.admission_argo_scope_off_shape)}"
  }
}

# The scope judgement itself (modules/argo-scope-check), with scopes the committed file does not have.
run "argo_scope_check_clean" {
  command = plan
  module {
    source = "./modules/argo-scope-check"
  }
  variables {
    namespaces          = ["ryft-api", "rabbitmq-system"]
    namespace_objects   = ["rabbitmq-system"]
    never               = ["vault", "kube-system"]
    addon_namespaces    = ["kube-system"]
    never_chart         = ["kube-system"]
    operators_namespace = "rabbitmq-system"
  }
  assert {
    condition     = length(output.never_listed) == 0 && length(output.off_shape) == 0 && length(output.never_uncovered) == 0
    error_message = "a ryft-* and the operators' namespace, nothing on the never-list, every add-on covered: clean"
  }
}

run "argo_scope_check_refuses_a_namespace_outside_ryft" {
  command = plan
  module {
    source = "./modules/argo-scope-check"
  }
  variables {
    namespaces          = ["ryft-api", "rabbitmq-system", "monitoring", "default"]
    namespace_objects   = ["rabbitmq-system"]
    never               = ["vault"]
    addon_namespaces    = []
    never_chart         = []
    operators_namespace = "rabbitmq-system"
  }
  assert {
    condition     = join(",", output.off_shape) == "default,monitoring"
    error_message = "monitoring and default are neither ryft-* nor the operators' namespace: ${join(",", output.off_shape)}"
  }
}

run "argo_scope_check_refuses_a_never_namespace" {
  command = plan
  module {
    source = "./modules/argo-scope-check"
  }
  variables {
    namespaces          = ["ryft-api", "vault-secrets-operator-system"]
    namespace_objects   = ["kube-system"]
    never               = ["vault-secrets-operator-system", "kube-system"]
    addon_namespaces    = []
    never_chart         = []
    operators_namespace = "rabbitmq-system"
  }
  assert {
    condition     = join(",", output.never_listed) == "kube-system,vault-secrets-operator-system"
    error_message = "a namespace on the never-list in namespaces or namespaceObjects is listed: ${join(",", output.never_listed)}"
  }
}

run "argo_scope_check_refuses_an_uncovered_addon" {
  command = plan
  module {
    source = "./modules/argo-scope-check"
  }
  variables {
    namespaces          = ["ryft-api"]
    namespace_objects   = []
    never               = []
    addon_namespaces    = ["external-dns", "kube-system"]
    never_chart         = ["kube-system"]
    operators_namespace = "rabbitmq-system"
  }
  assert {
    condition     = join(",", output.never_uncovered) == "external-dns"
    error_message = "an add-on namespace the chart's never-list misses is listed: ${join(",", output.never_uncovered)}"
  }
}

# The RBAC gate on the tree as it is: #47 (T12-f) is merged, so argocd.tf renders a narrowed controller role
# and argo-scope: deny with P6, P1 and P2 in deny is planned WITHOUT the stand-in variable.
run "argo_scope_deny_reads_the_narrowed_role_of_argocd_tf" {
  command = plan

  variables {
    admission_enabled          = true
    admission_actions          = { argo-scope = "deny", operators-cluster = "deny", namespaces = "deny", workloads-templates = "deny" }
    admission_scoped_rbac_file = "admission-values.yaml"
  }

  expect_failures = [check.admission_enabled_override]

  assert {
    condition     = local.admission_argo_rbac_narrowed_rendered == true && local.admission_argo_rbac_narrowed == true
    error_message = "argocd.tf (T12-f, #47) renders controller.clusterRoleRules enabled with no wildcard and no sync impersonation: the gate must read it as narrowed"
  }

  assert {
    condition     = length(helm_release.admission_policy) == 1 && local.admission_effective["argo-scope"] == ["Deny", "Audit"]
    error_message = "with the tree's narrowed role and P6, P1, P2 in deny, argo-scope: deny is planned"
  }
}

# argo-scope: deny needs P6, P1 and P2 in deny (the RBAC route) AND a narrowed controller role (#47).
run "argo_scope_deny_alone_is_refused" {
  command = plan

  variables {
    admission_enabled            = true
    admission_actions            = { argo-scope = "deny" }
    admission_argo_rbac_narrowed = true
  }

  expect_failures = [helm_release.admission_policy, check.admission_enabled_override, check.admission_gate_override]
}

run "argo_scope_deny_without_p2_is_refused" {
  command = plan

  variables {
    admission_enabled            = true
    admission_actions            = { argo-scope = "deny", operators-cluster = "deny", namespaces = "deny" }
    admission_scoped_rbac_file   = "admission-values.yaml"
    admission_argo_rbac_narrowed = true
  }

  expect_failures = [helm_release.admission_policy, check.admission_enabled_override, check.admission_gate_override]
}

run "argo_scope_deny_with_the_wildcard_controller_role_is_refused" {
  command = plan

  variables {
    admission_enabled            = true
    admission_actions            = { argo-scope = "deny", operators-cluster = "deny", namespaces = "deny", workloads-templates = "deny" }
    admission_scoped_rbac_file   = "admission-values.yaml"
    admission_argo_rbac_narrowed = false
  }

  expect_failures = [helm_release.admission_policy, check.admission_enabled_override, check.admission_gate_override]
}

run "argo_scope_deny_is_deny_and_audit_with_the_route_closed_and_rbac_narrowed" {
  command = plan

  variables {
    admission_enabled            = true
    admission_actions            = { argo-scope = "deny", operators-cluster = "deny", namespaces = "deny", workloads-templates = "deny" }
    admission_scoped_rbac_file   = "admission-values.yaml"
    admission_argo_rbac_narrowed = true
  }

  expect_failures = [check.admission_enabled_override, check.admission_gate_override]

  assert {
    condition     = local.admission_effective["argo-scope"] == ["Deny", "Audit"] && yamldecode(helm_release.admission_policy[0].values[0]).actions["argo-scope"] == "deny"
    error_message = "argo-scope on deny must reach the chart as deny and be Deny+Audit"
  }

  assert {
    condition     = length(helm_release.admission_policy) == 1
    error_message = "with P6, P1 and P2 in deny and the controller's role narrowed, the release is planned"
  }
}

# The gate's expression itself (the guard module's controller_rbac_narrowed), with values argocd.tf does not
# have: a wildcard in any field of any rule, the chart's default role, sync impersonation on. Each is "not
# narrowed", so argo-scope: deny is refused; a narrowed role without impersonation is.
run "argo_rbac_gate_narrowed_role_passes" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }] } }, configs = { cm = { "resource.respectRBAC" = "strict" } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == true
    error_message = "a narrowed controller role without impersonation is narrowed"
  }
}

run "argo_rbac_gate_refuses_a_wildcard_verb" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["admissionregistration.k8s.io"], resources = ["validatingadmissionpolicybindings"], verbs = ["*"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a wildcard verb in any rule is not narrowed"
  }
}

run "argo_rbac_gate_refuses_a_wildcard_resource" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = ["rbac.authorization.k8s.io"], resources = ["*"], verbs = ["get"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a wildcard resource is not narrowed"
  }
}

run "argo_rbac_gate_refuses_a_wildcard_group" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = ["*"], resources = ["configmaps"], verbs = ["get"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a wildcard API group is not narrowed"
  }
}

run "argo_rbac_gate_refuses_the_chart_default_role" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { replicas = 1 } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "without clusterRoleRules the chart's wildcard role is the controller's"
  }
}

run "argo_rbac_gate_refuses_sync_impersonation" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }] } }, configs = { cm = { "application.sync.impersonation.enabled" = "true" } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "sync impersonation makes P12 match nothing: not narrowed"
  }
}

# The allowlist (security review S1): each reviewer example, next to a rule the role really has, is not
# narrowed; the named escalate and bind of argocd.tf is.
run "argo_rbac_gate_admits_the_named_escalate_and_bind" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["roles"], verbs = ["escalate", "bind"], resourceNames = ["rabbitmq-cluster-operator-role", "messaging-topology-manager-role"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["bind"], resourceNames = ["messaging-topology-metrics-auth-role"] }, { nonResourceURLs = ["/metrics"], verbs = ["get"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == true
    error_message = "the four Roles escalate/bind and the one ClusterRole bind by name, /metrics get: narrowed"
  }
}

run "argo_rbac_gate_refuses_impersonates_service_accounts" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["serviceaccounts"], verbs = ["impersonate"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "impersonate on serviceaccounts is not narrowed"
  }
}

run "argo_rbac_gate_refuses_impersonates_users" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["users"], verbs = ["impersonate"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "impersonate on users is not narrowed"
  }
}

run "argo_rbac_gate_refuses_impersonates_groups" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["groups"], verbs = ["impersonate"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "impersonate on groups is not narrowed"
  }
}

run "argo_rbac_gate_refuses_patches_policy_bindings" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["admissionregistration.k8s.io"], resources = ["validatingadmissionpolicybindings"], verbs = ["patch"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "patch on validatingadmissionpolicybindings is not narrowed"
  }
}

run "argo_rbac_gate_refuses_deletes_policy_bindings" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["admissionregistration.k8s.io"], resources = ["validatingadmissionpolicybindings"], verbs = ["delete"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "delete on validatingadmissionpolicybindings is not narrowed"
  }
}

run "argo_rbac_gate_refuses_updates_policies" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["admissionregistration.k8s.io"], resources = ["validatingadmissionpolicies"], verbs = ["update"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "update on validatingadmissionpolicies is not narrowed"
  }
}

run "argo_rbac_gate_refuses_creates_mutating_policies" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["admissionregistration.k8s.io"], resources = ["mutatingadmissionpolicies", "mutatingadmissionpolicybindings"], verbs = ["create"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "create on the mutating admission policy kinds is not narrowed"
  }
}

run "argo_rbac_gate_refuses_escalates_every_cluster_role" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["escalate"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "escalate on every ClusterRole is not narrowed"
  }
}

run "argo_rbac_gate_refuses_binds_every_cluster_role" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["bind"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "bind on every ClusterRole is not narrowed"
  }
}

run "argo_rbac_gate_refuses_binds_cluster_admin" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["bind"], resourceNames = ["cluster-admin"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "bind on a ClusterRole that is not the named one is not narrowed"
  }
}

run "argo_rbac_gate_refuses_escalates_the_named_cluster_role" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["clusterroles"], verbs = ["escalate", "bind"], resourceNames = ["messaging-topology-metrics-auth-role"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "escalate on the one ClusterRole it may only bind is not narrowed"
  }
}

run "argo_rbac_gate_refuses_escalates_every_role" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["roles"], verbs = ["escalate", "bind"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "escalate and bind on Roles without resourceNames is not narrowed"
  }
}

run "argo_rbac_gate_refuses_escalates_another_role" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["roles"], verbs = ["escalate"], resourceNames = ["admin"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "escalate on a Role that is not one of the four is not narrowed"
  }
}

run "argo_rbac_gate_refuses_escalates_roles_and_writes_bindings" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["roles", "rolebindings"], verbs = ["bind"], resourceNames = ["rabbitmq-cluster-operator-role"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "bind in a rule that also names another resource is not narrowed"
  }
}

run "argo_rbac_gate_refuses_creates_service_account_tokens" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["serviceaccounts/token"], verbs = ["create"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "create serviceaccounts/token is not narrowed"
  }
}

run "argo_rbac_gate_refuses_creates_pods_exec" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["pods/exec"], verbs = ["create"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "create pods/exec is not narrowed"
  }
}

run "argo_rbac_gate_refuses_holds_a_partial_subresource_wildcard" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["pods/*"], verbs = ["create"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "pods/* is not narrowed"
  }
}

run "argo_rbac_gate_refuses_holds_a_partial_resource_wildcard" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["apps"], resources = ["*/scale"], verbs = ["update"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "*/scale is not narrowed"
  }
}

run "argo_rbac_gate_refuses_holds_a_wildcard_resource_name" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["rbac.authorization.k8s.io"], resources = ["roles"], verbs = ["escalate"], resourceNames = ["rabbitmq-*"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a wildcard inside resourceNames is not narrowed"
  }
}

run "argo_rbac_gate_refuses_holds_a_partial_group_wildcard" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = ["*.k8s.io"], resources = ["configmaps"], verbs = ["get"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a wildcard inside an API group is not narrowed"
  }
}

run "argo_rbac_gate_refuses_holds_a_verb_outside_the_list" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["configmaps"], verbs = ["deletecollection"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "deletecollection is not narrowed"
  }
}

run "argo_rbac_gate_refuses_reads_secrets" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { apiGroups = [""], resources = ["secrets"], verbs = ["get"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "secrets is not narrowed"
  }
}

run "argo_rbac_gate_refuses_reads_another_non_resource_url" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { nonResourceURLs = ["/api/*"], verbs = ["get"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a non-resource URL other than /metrics is not narrowed"
  }
}

run "argo_rbac_gate_refuses_writes_the_metrics_url" {
  command = plan
  module {
    source = "./modules/argocd-values-guard"
  }
  variables {
    values = { controller = { clusterRoleRules = { enabled = true, rules = [{ apiGroups = [""], resources = ["configmaps"], verbs = ["get", "patch"] }, { nonResourceURLs = ["/metrics"], verbs = ["get", "post"] }] } } }
  }
  assert {
    condition     = output.controller_rbac_narrowed == false
    error_message = "a verb other than get on /metrics is not narrowed"
  }
}

# The rendered Argo CD RBAC is what the gate reads: no clusterRoleRules, or one with a wildcard, or sync
# impersonation, is not narrowed.
run "the_rbac_gate_reads_argocd_tf" {
  command = plan

  assert {
    condition     = local.admission_argo_rbac_narrowed == module.argocd_values_guard.controller_rbac_narrowed
    error_message = "without the variable the gate is exactly the guard module's output on the values of argocd.tf"
  }

  # argocd.tf's controller rules are the allowlist (S1): the gate is open for them, and with no fault.
  assert {
    condition     = module.argocd_values_guard.controller_rbac_narrowed == true && length(module.argocd_values_guard.controller_rbac_faults) == 0
    error_message = "argocd.tf's controller ClusterRole is not the allowlist of controller_rbac_narrowed: ${join("; ", module.argocd_values_guard.controller_rbac_faults)}"
  }

  assert {
    condition     = !contains(keys(try(local.argocd_values.configs.cm, {})), "application.sync.impersonation.enabled")
    error_message = "application.sync.impersonation.enabled makes Argo CD write as other service accounts: P12 would match nothing (see p12-argo-scope.md)"
  }
}

run "argo_scope_under_break_glass_is_warn_and_audit" {
  command = plan

  variables {
    admission_enabled     = true
    admission_break_glass = true
    admission_actions     = { argo-scope = "deny" }
  }

  expect_failures = [check.admission_enabled_override]

  assert {
    condition     = length(helm_release.admission_policy) == 1
    error_message = "under break-glass the guards of a deny do not fire (the effective action is Warn+Audit)"
  }

  assert {
    condition     = local.admission_effective["argo-scope"] == ["Warn", "Audit"] && output.admission_binding_actions["argo-scope"] == ["Warn", "Audit"]
    error_message = "break-glass must give P12 Warn+Audit too"
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
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing=ryft-vso extra= policyName= actions= match="
    error_message = "the live list of this run must differ from the render in exactly: missing=ryft-vso"
  }
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
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing= extra= policyName= actions=ryft-workloads-pods match="
    error_message = "the live list of this run must differ from the render in exactly: actions=ryft-workloads-pods"
  }
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
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing= extra= policyName=ryft-netpol actions= match="
    error_message = "the live list of this run must differ from the render in exactly: policyName=ryft-netpol"
  }
}

run "drift_a_binding_patched_with_a_namespace_selector" {
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
        { metadata = { name = "ryft-netpol" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchLabels = { nothing = "matches" } } } } },
        { metadata = { name = "ryft-auth-env" }, spec = { policyName = "ryft-auth-env", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-traefik" }, spec = { policyName = "ryft-traefik", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing= extra= policyName= actions= match=ryft-netpol"
    error_message = "the live list of this run must differ from the render in exactly: match=ryft-netpol"
  }
}

run "server_defaults_are_not_drift" {
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
        { metadata = { name = "ryft-netpol" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = {}, objectSelector = {}, matchPolicy = "Equivalent" } } },
        { metadata = { name = "ryft-auth-env" }, spec = { policyName = "ryft-auth-env", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-traefik" }, spec = { policyName = "ryft-traefik", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = {}, objectSelector = {}, matchPolicy = "Equivalent" } } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_enabled_override]
}

# P12's binding has no matchResources of its own (it matches everything): a live binding that
# carries a namespace selector is a patched binding that excludes namespaces from the policy.
run "drift_p12_binding_patched_with_a_namespace_selector" {
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
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "NotIn", values = ["kube-system"] }] } } } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing= extra= policyName= actions= match=ryft-argo-scope"
    error_message = "the live list of this run must differ from the render in exactly: match=ryft-argo-scope"
  }
}

run "operators_cluster_deny_needs_t12g" {
  command = plan

  variables {
    admission_enabled          = true
    admission_actions          = { operators-cluster = "deny" }
    admission_scoped_rbac_file = "no-such-scoped-rbac.yaml"
  }

  expect_failures = [helm_release.admission_policy, check.admission_enabled_override]
}

run "operators_cluster_deny_with_t12g_in_the_tree" {
  command = plan

  variables {
    admission_enabled          = true
    admission_actions          = { operators-cluster = "deny" }
    admission_scoped_rbac_file = "admission-values.yaml"
  }

  expect_failures = [check.admission_enabled_override]

  assert {
    condition     = local.admission_effective["operators-cluster"] == ["Deny", "Audit"]
    error_message = "with T12-g's file present the deny is honoured"
  }
}

run "operators_cluster_deny_under_break_glass_needs_no_t12g" {
  command = plan

  variables {
    admission_enabled          = true
    admission_break_glass      = true
    admission_actions          = { operators-cluster = "deny" }
    admission_scoped_rbac_file = "no-such-scoped-rbac.yaml"
  }

  expect_failures = [check.admission_enabled_override]
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
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing=ryft-argocd extra= validations= matchConditions= variables= match= failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: missing=ryft-argocd"
  }
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
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing= extra= validations=ryft-vso matchConditions= variables= match= failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: validations=ryft-vso"
  }
}

run "drift_a_policy_with_an_edited_match_condition" {
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
        { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "false" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing= extra= validations= matchConditions=ryft-argo-scope variables= match= failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: matchConditions=ryft-argo-scope"
  }
}

run "drift_a_policy_with_an_edited_variable" {
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
        { metadata = { name = "ryft-namespaces" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "false" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-workloads" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-netpol" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-auth-env" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-traefik" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-operator-reach" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing= extra= validations= matchConditions= variables=ryft-namespaces match= failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: variables=ryft-namespaces"
  }
}

run "drift_a_policy_with_narrowed_match_rules" {
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
        { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["configmaps"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing= extra= validations= matchConditions= variables= match=ryft-argo-scope failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: match=ryft-argo-scope"
  }
}

run "drift_a_policy_set_to_ignore_failures" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_policies
    values = {
      objects = [
        { metadata = { name = "ryft-vso" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-operators-cluster" }, spec = { failurePolicy = "Ignore", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-broker" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-namespaces" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-workloads" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-netpol" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-auth-env" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-traefik" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-operator-reach" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing= extra= validations= matchConditions= variables= match= failurePolicy=ryft-operators-cluster"
    error_message = "the live list of this run must differ from the render in exactly: failurePolicy=ryft-operators-cluster"
  }
}

run "drift_a_binding_renamed" {
  command = plan

  variables {
    admission_enabled = true
  }

  override_data {
    target = data.kubernetes_resources.admission_bindings
    values = {
      objects = [
        { metadata = { name = "ryft-vso-old" }, spec = { policyName = "ryft-vso", validationActions = ["Warn", "Audit"] } },
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
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing=ryft-vso extra=ryft-vso-old policyName= actions= match="
    error_message = "the live list of this run must differ from the render in exactly: missing=ryft-vso, extra=ryft-vso-old"
  }
}

run "drift_a_policy_renamed" {
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
        { metadata = { name = "ryft-argocd-old" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing=ryft-argocd extra=ryft-argocd-old validations= matchConditions= variables= match= failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: missing=ryft-argocd, extra=ryft-argocd-old"
  }
}

# A binding the chart does not render (labelled part-of=admission-policy, left from a rename or written by
# hand) is drift too; only the count comparison sees it.
run "drift_an_extra_binding" {
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
        { metadata = { name = "ryft-workloads-pods" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { resourceRules = [{ resources = ["pods"], operations = ["CREATE"] }] } } },
        { metadata = { name = "ryft-operator-reach" }, spec = { policyName = "ryft-operator-reach", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd" }, spec = { policyName = "ryft-argocd", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { policyName = "ryft-argocd-operation", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { policyName = "ryft-argo-scope", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { policyName = "ryft-vault-trust", validationActions = ["Warn", "Audit"] } },
        { metadata = { name = "ryft-namespaces-vault" }, spec = { policyName = "ryft-namespaces", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-templates-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-workloads-pods-vault" }, spec = { policyName = "ryft-workloads", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-netpol-vault" }, spec = { policyName = "ryft-netpol", validationActions = ["Warn", "Audit"], matchResources = { namespaceSelector = { matchExpressions = [{ key = "kubernetes.io/metadata.name", operator = "In", values = ["vault"] }] } } } },
        { metadata = { name = "ryft-stray" }, spec = { policyName = "ryft-vso", validationActions = ["Warn", "Audit"] } },
      ]
    }
  }

  expect_failures = [check.admission_binding_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s policyName=%s actions=%s match=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_bindings), keys(local.admission_live_bindings)))),
      join(",", sort(setsubtract(keys(local.admission_live_bindings), keys(local.admission_rendered_bindings)))),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if w.spec.policyName != try(local.admission_live_bindings[n].spec.policyName, w.spec.policyName)])),
      join(",", sort([for n, w in local.admission_rendered_bindings : n if toset(w.spec.validationActions) != toset(try(local.admission_live_bindings[n].spec.validationActions, w.spec.validationActions))])),
      join(",", sort([for n, w in local.admission_norm.rendered_bindings : n if w != try(local.admission_norm.live_bindings[n], w)]))
    ) == "missing= extra=ryft-stray policyName= actions= match="
    error_message = "the live list of this run must differ from the render in exactly: extra=ryft-stray"
  }
}

run "drift_an_extra_policy" {
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
        { metadata = { name = "ryft-argocd" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argocd-operation" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-argo-scope" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["*/*"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-vault-trust" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
        { metadata = { name = "ryft-stray" }, spec = { failurePolicy = "Fail", matchConstraints = { resourceRules = [{ resources = ["things"], operations = ["CREATE"] }] }, matchConditions = [{ name = "m", expression = "true" }], variables = [{ name = "v", expression = "true" }], validations = [{ expression = "true" }] } },
      ]
    }
  }

  expect_failures = [check.admission_policy_drift, check.admission_enabled_override]

  # The guard: the live list differs from the render in exactly the one change of this run, so the
  # check can only fail on the comparison under test (never on a short list).
  assert {
    condition = format("missing=%s extra=%s validations=%s matchConditions=%s variables=%s match=%s failurePolicy=%s",
      join(",", sort(setsubtract(keys(local.admission_rendered_policies), keys(local.admission_live_policies)))),
      join(",", sort(setsubtract(keys(local.admission_live_policies), keys(local.admission_rendered_policies)))),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in w.spec.validations : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.validations, w.spec.validations) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.matchConditions, []) : v.expression]) != jsonencode([for v in try(local.admission_live_policies[n].spec.matchConditions, try(w.spec.matchConditions, [])) : v.expression])])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if jsonencode([for v in try(w.spec.variables, []) : [v.name, v.expression]]) != jsonencode([for v in try(local.admission_live_policies[n].spec.variables, try(w.spec.variables, [])) : [v.name, v.expression]])])),
      join(",", sort([for n, w in local.admission_norm.rendered_policies : n if w != try(local.admission_norm.live_policies[n], w)])),
      join(",", sort([for n, w in local.admission_rendered_policies : n if w.spec.failurePolicy != try(local.admission_live_policies[n].spec.failurePolicy, w.spec.failurePolicy)]))
    ) == "missing= extra=ryft-stray validations= matchConditions= variables= match= failurePolicy="
    error_message = "the live list of this run must differ from the render in exactly: extra=ryft-stray"
  }
}
