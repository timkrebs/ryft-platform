# AWS permissions for the in-cluster controllers, handed out through EKS Pod
# Identity (the agent add-on is installed by ../modules/eks). Each role is bound
# to exactly one service account.

module "aws_load_balancer_controller_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                            = "${var.cluster_name}-aws-lb-controller"
  attach_aws_lb_controller_policy = true

  associations = {
    controller = {
      cluster_name    = var.cluster_name
      namespace       = "kube-system"
      service_account = "aws-load-balancer-controller"
    }
  }

  tags = local.tags
}

module "external_dns_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                          = "${var.cluster_name}-external-dns"
  attach_external_dns_policy    = true
  external_dns_hosted_zone_arns = [data.aws_route53_zone.this.arn]

  associations = {
    controller = {
      cluster_name    = var.cluster_name
      namespace       = "external-dns"
      service_account = "external-dns"
    }
  }

  tags = local.tags
}

# Vault's auto-unseal (vault.tf, D189). The key is infra/aws's (aws_kms_key.vault_unseal, prevent_destroy), found
# by its alias; apply infra/aws first. The role names the key's ARN, never the alias: re-pointing the alias must not
# move what this role may use. Nothing else: no kms:GenerateDataKey, no ReEncrypt, no other key.
#
# Who gets the role: the association binds it to vault/vault (the chart's server service account; no
# eks.amazonaws.com/role-arn annotation on it, IRSA would come first in the SDK's chain), and the trust policy
# additionally requires the session tags EKS Pod Identity sets for that namespace and service account
# (aws:RequestTag/kubernetes-namespace and -service-account, the AWS docs' example, pod-id-role.html) and the
# eks-cluster-arn tag of this cluster (pod-id-abac.html), so a second association of this role to another service
# account, or from another cluster of the account, cannot assume it. Not conditioned yet (to verify for Pod
# Identity first): aws:SourceAccount and aws:SourceArn.
data "aws_kms_alias" "vault_unseal" {
  name = "alias/${var.cluster_name}-vault-unseal"
}

locals {
  vault_unseal_policy_statements = [{
    sid       = "AutoUnseal"
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:DescribeKey"]
    resources = [data.aws_kms_alias.vault_unseal.target_key_arn]
  }]

  vault_unseal_trust_conditions = [
    { test = "StringEquals", variable = "aws:RequestTag/kubernetes-namespace", values = [local.vault_namespace] },
    { test = "StringEquals", variable = "aws:RequestTag/kubernetes-service-account", values = [local.vault_service_account] },
    { test = "StringEquals", variable = "aws:RequestTag/eks-cluster-arn", values = [data.aws_eks_cluster.this.arn] },
  ]
}

module "vault_unseal_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                    = "${var.cluster_name}-vault-unseal"
  attach_custom_policy    = true
  policy_statements       = local.vault_unseal_policy_statements
  trust_policy_conditions = local.vault_unseal_trust_conditions

  associations = {
    vault = {
      cluster_name    = var.cluster_name
      namespace       = local.vault_namespace
      service_account = local.vault_service_account
    }
  }

  tags = local.tags
}

module "cert_manager_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                          = "${var.cluster_name}-cert-manager"
  attach_cert_manager_policy    = true
  cert_manager_hosted_zone_arns = [data.aws_route53_zone.this.arn]

  associations = {
    controller = {
      cluster_name    = var.cluster_name
      namespace       = "cert-manager"
      service_account = "cert-manager"
    }
  }

  tags = local.tags
}
