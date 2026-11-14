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

# Vault's auto-unseal (vault.tf, D189), only with var.vault_seal = "awskms": under Shamir (the default, D208) the
# alias is not read and neither the role nor the association exists. The key is infra/aws's (aws_kms_key.vault_unseal,
# prevent_destroy; it stays there under Shamir, so the switch needs no infra/aws apply), found by its alias; apply
# infra/aws first. The role names the key's ARN, never the alias: re-pointing the alias must not move what this role
# may use. Nothing else: no kms:GenerateDataKey, no ReEncrypt, no other key.
#
# Who gets the role: the association binds it to vault/vault (the chart's server service account; no
# eks.amazonaws.com/role-arn annotation on it, IRSA would come first in the SDK's chain), and the trust policy
# additionally requires the session tags EKS Pod Identity sets for that namespace and service account
# (aws:RequestTag/kubernetes-namespace and -service-account, the AWS docs' example, pod-id-role.html) and the
# eks-cluster-arn tag of this cluster (pod-id-abac.html), so a second association of this role to another service
# account, or from another cluster of the account, cannot assume it. Not conditioned yet (to verify for Pod
# Identity first): aws:SourceAccount and aws:SourceArn.
data "aws_kms_alias" "vault_unseal" {
  count = local.vault_seal_awskms ? 1 : 0

  name = "alias/${var.cluster_name}-vault-unseal"
}

locals {
  # The unseal key's ARN; null under Shamir, where the alias is not read (the seal stanza and the precondition on
  # helm_release.vault use it only with awskms).
  vault_unseal_key_arn = one(data.aws_kms_alias.vault_unseal[*].target_key_arn)

  vault_unseal_policy_statements = [{
    sid       = "AutoUnseal"
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:DescribeKey"]
    resources = [local.vault_unseal_key_arn]
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
  count   = local.vault_seal_awskms ? 1 : 0

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

# The Raft snapshot CronJob's upload (charts/vault-extras templates/snapshot.yaml, D192). The bucket and its key are
# infra/aws's (vault.tf there, prevent_destroy), found by name and alias; apply infra/aws first. The role may put
# objects under snapshots/ and have S3 generate the data key, and nothing else: no GetObject, ListBucket or
# DeleteObject, so a stolen job identity can add objects but cannot read, list, remove or (the bucket policy's
# DenyPutWithoutIfNoneMatch) overwrite a snapshot.
#  - PutObject: only into this account's bucket (s3:ResourceAccount, with the job's --expected-bucket-owner), and only
#    naming the snapshot key's full ARN (the bucket policy denies any other key header as well).
#  - GenerateDataKey: the snapshot key only, only when S3 in this region asks for it (kms:ViaService), and only for this
#    bucket: with the S3 Bucket Key the encryption context is the bucket's ARN, not the object's.
#  - No kms:Decrypt: one put-object is a single PUT, not a multipart upload (multipart needs Decrypt). Whether S3 asks
#    for Decrypt when it reuses a cached bucket key on a later PUT is not documented: a first run that fails with
#    AccessDenied on kms:Decrypt means it does (OPERATIONS -> Vault -> Snapshots).
# Trust: the same session-tag conditions as the unseal role, for vault/vault-snapshot in this cluster.
data "aws_kms_alias" "vault_snapshots" {
  name = "alias/${var.cluster_name}-vault-snapshots"

  lifecycle {
    postcondition {
      condition     = can(regex(local.vault_snapshot_key_arn_pattern, self.target_key_arn))
      error_message = "alias/${var.cluster_name}-vault-snapshots does not point at a KMS key of this account in ${var.region}: the bucket policy compares the key header with infra/aws's key ARN, so every upload would be refused."
    }
  }
}

locals {
  # infra/aws's name: <project>-<environment>-<location> (= the cluster name) plus the account (vault.tf there).
  vault_snapshots_bucket          = "${var.cluster_name}-vault-snapshots-${local.account_id}"
  vault_snapshot_service_account  = "vault-snapshot"
  vault_snapshot_key_arn_pattern  = "^arn:aws[a-z-]*:kms:${var.region}:${local.account_id}:key/[0-9a-f-]+$"
  vault_snapshot_bucket_arn_value = "arn:${local.partition}:s3:::${local.vault_snapshots_bucket}"

  vault_snapshot_policy_statements = [
    {
      sid       = "PutSnapshots"
      actions   = ["s3:PutObject"]
      resources = ["${local.vault_snapshot_bucket_arn_value}/snapshots/*"]
      condition = [
        { test = "StringEquals", variable = "s3:ResourceAccount", values = [local.account_id] },
        { test = "StringEquals", variable = "s3:x-amz-server-side-encryption-aws-kms-key-id", values = [data.aws_kms_alias.vault_snapshots.target_key_arn] },
      ]
    },
    {
      sid       = "SnapshotDataKey"
      actions   = ["kms:GenerateDataKey"]
      resources = [data.aws_kms_alias.vault_snapshots.target_key_arn]
      condition = [
        { test = "StringEquals", variable = "kms:ViaService", values = ["s3.${var.region}.amazonaws.com"] },
        { test = "StringEquals", variable = "kms:EncryptionContext:aws:s3:arn", values = [local.vault_snapshot_bucket_arn_value] },
      ]
    },
  ]

  vault_snapshot_trust_conditions = [
    { test = "StringEquals", variable = "aws:RequestTag/kubernetes-namespace", values = [local.vault_namespace] },
    { test = "StringEquals", variable = "aws:RequestTag/kubernetes-service-account", values = [local.vault_snapshot_service_account] },
    { test = "StringEquals", variable = "aws:RequestTag/eks-cluster-arn", values = [data.aws_eks_cluster.this.arn] },
  ]
}

module "vault_snapshot_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                    = "${var.cluster_name}-vault-snapshot"
  attach_custom_policy    = true
  policy_statements       = local.vault_snapshot_policy_statements
  trust_policy_conditions = local.vault_snapshot_trust_conditions

  associations = {
    snapshot = {
      cluster_name    = var.cluster_name
      namespace       = local.vault_namespace
      service_account = local.vault_snapshot_service_account
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
