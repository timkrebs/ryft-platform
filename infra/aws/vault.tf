# Vault Community Edition in the cluster (design: docs/work/2026-11-05-vault-ce): the KMS
# key Vault auto-unseals with, the bucket its Raft snapshots go to, and that bucket's key.
# They live in this root, not in infra/aws/kubernetes, because they must outlive any
# rebuild of the Kubernetes layer: without the unseal key, Vault's data and every snapshot
# are unreadable. The Pod Identity roles that use them (vault/vault for the seal,
# vault/vault-snapshot for the upload) are in infra/aws/kubernetes and find the keys by
# alias, so nothing here names them.
#
# prevent_destroy on both keys and the bucket: Terraform refuses a plan that destroys or
# replaces them while the block is in the file. It is no guard against deleting the block
# (or vault.tf, or reverting the PR that added it): the protection goes with it. The keys'
# own policy is the guard that holds then (NoDeletionOrDisable below). Removing them on
# purpose: README → Destroying.

locals {
  # At most 49 characters (S3 allows 63): modules/eks caps local.name at 20.
  vault_snapshots_bucket     = "${local.name}-vault-snapshots-${local.account_id}"
  vault_snapshots_bucket_arn = "arn:${local.partition}:s3:::${local.vault_snapshots_bucket}"

  # The role infra/aws/kubernetes creates for vault/vault (the pod identity module adds a
  # generated suffix to the name), for var.vault_unseal_allowed_principal_arns.
  vault_unseal_role_arn_pattern = "arn:${local.partition}:iam::${local.account_id}:role/${local.name}-vault-unseal-*"
}

# Both keys: the account administers them and IAM policies decide who uses them (as
# kubernetes/logs.tf). The key policy names no role, so the roles can come and go with
# the Kubernetes layer; each role's own policy grants only the actions it needs.
data "aws_iam_policy_document" "vault_keys" {
  statement {
    sid       = "AccountAdministers"
    effect    = "Allow"
    actions   = ["kms:*"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["arn:${local.partition}:iam::${local.account_id}:root"]
    }
  }

  # Nobody, the account root and Terraform included, may schedule a key's deletion or
  # disable it. A destroy that slips past prevent_destroy (a revert, a deleted block)
  # fails here instead of starting the 30-day clock. To delete or disable a key on
  # purpose, a reviewed change drops this statement and is applied first (PutKeyPolicy
  # stays with the account, so there is no lockout): README → Destroying.
  statement {
    sid       = "NoDeletionOrDisable"
    effect    = "Deny"
    actions   = ["kms:ScheduleKeyDeletion", "kms:DisableKey"]
    resources = ["*"]

    principals {
      type        = "AWS"
      identifiers = ["*"]
    }
  }
}

# The unseal key: the same, plus, once var.vault_unseal_allowed_principal_arns is set, a
# deny of every cryptographic use to any principal outside that list. Off by default: a
# list that misses Vault's role leaves every restarted Vault pod sealed (README → Unseal
# key allowlist).
data "aws_iam_policy_document" "vault_unseal_key" {
  source_policy_documents = [data.aws_iam_policy_document.vault_keys.json]

  dynamic "statement" {
    for_each = length(var.vault_unseal_allowed_principal_arns) > 0 ? [var.vault_unseal_allowed_principal_arns] : []

    content {
      sid       = "OnlyListedPrincipalsUseTheUnsealKey"
      effect    = "Deny"
      actions   = ["kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*"]
      resources = ["*"]

      principals {
        type        = "AWS"
        identifiers = ["*"]
      }

      condition {
        test     = "ArnNotLike"
        variable = "aws:PrincipalArn"
        values   = statement.value
      }
    }
  }
}

# Set the allowlist without Vault's role and the next Vault pod that starts stays sealed;
# nothing fails at apply. Warn on every plan instead.
check "vault_unseal_allowlist_names_the_vault_role" {
  assert {
    condition = length(var.vault_unseal_allowed_principal_arns) == 0 || anytrue([
      for a in var.vault_unseal_allowed_principal_arns :
      startswith(a, "arn:${local.partition}:iam::${local.account_id}:role/${local.name}-vault-unseal")
    ])
    error_message = "vault_unseal_allowed_principal_arns is set but names no role arn:${local.partition}:iam::${local.account_id}:role/${local.name}-vault-unseal-…: Vault could no longer unseal. Add \"${local.vault_unseal_role_arn_pattern}\"."
  }
}

resource "aws_kms_key" "vault_unseal" {
  description             = "Vault auto-unseal for ${local.name}. Deleting or disabling it makes the Vault data unreadable."
  key_usage               = "ENCRYPT_DECRYPT"
  enable_key_rotation     = true # KMS keeps the old material; Vault decrypts with the ciphertext's version
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.vault_unseal_key.json
  tags                    = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "vault_unseal" {
  name          = "alias/${local.name}-vault-unseal"
  target_key_id = aws_kms_key.vault_unseal.key_id
}

# A key of its own for the bucket, so the snapshot job's role never holds the key that
# unwraps Vault's root key.
resource "aws_kms_key" "vault_snapshots" {
  description             = "Vault snapshots in S3 for ${local.name}. Deleting or disabling it makes every snapshot unreadable."
  key_usage               = "ENCRYPT_DECRYPT"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.vault_keys.json
  tags                    = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_kms_alias" "vault_snapshots" {
  name          = "alias/${local.name}-vault-snapshots"
  target_key_id = aws_kms_key.vault_snapshots.key_id
}

# Raft snapshots (barrier-encrypted; they open only with the unseal key). The CronJob in
# namespace vault puts one object under snapshots/ every 6 hours, with a single PUT that
# names the snapshot key. Restores read through the account's own IAM identities.
resource "aws_s3_bucket" "vault_snapshots" {
  bucket        = local.vault_snapshots_bucket
  force_destroy = false
  tags          = local.tags

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_ownership_controls" "vault_snapshots" {
  bucket = aws_s3_bucket.vault_snapshots.id

  rule {
    object_ownership = "BucketOwnerEnforced" # no ACLs; the account owns every object
  }
}

resource "aws_s3_bucket_public_access_block" "vault_snapshots" {
  bucket = aws_s3_bucket.vault_snapshots.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "vault_snapshots" {
  bucket = aws_s3_bucket.vault_snapshots.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "vault_snapshots" {
  bucket = aws_s3_bucket.vault_snapshots.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.vault_snapshots.arn
    }
    bucket_key_enabled = true # one data key per bucket, not one KMS call per object
  }
}

# Current snapshots expire after 30 days, the versions left behind after 7 more (an
# overwrite or a delete is undone within that week), and an aborted upload after a day.
resource "aws_s3_bucket_lifecycle_configuration" "vault_snapshots" {
  bucket = aws_s3_bucket.vault_snapshots.id

  rule {
    id     = "snapshots"
    status = "Enabled"

    filter {
      prefix = "snapshots/"
    }

    expiration {
      days = 30
    }

    noncurrent_version_expiration {
      noncurrent_days = 7
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }

  # The noncurrent rule needs versioning in place first.
  depends_on = [aws_s3_bucket_versioning.vault_snapshots]
}

# Two denies for everyone, the account included: no request without TLS, and no object
# that is not encrypted with the snapshot key. The second also refuses a PUT without the
# key header (the bucket default would encrypt it, but the deny makes the key part of
# every request rather than only of today's default) and, very likely, multipart part
# uploads (UploadPart sends no key header), so the job uploads with one put-object.
data "aws_iam_policy_document" "vault_snapshots_bucket" {
  statement {
    sid       = "DenyInsecureTransport"
    effect    = "Deny"
    actions   = ["s3:*"]
    resources = [local.vault_snapshots_bucket_arn, "${local.vault_snapshots_bucket_arn}/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "Bool"
      variable = "aws:SecureTransport"
      values   = ["false"]
    }
  }

  statement {
    sid       = "DenyPutWithoutSnapshotKey"
    effect    = "Deny"
    actions   = ["s3:PutObject"]
    resources = ["${local.vault_snapshots_bucket_arn}/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }

    condition {
      test     = "StringNotEquals"
      variable = "s3:x-amz-server-side-encryption-aws-kms-key-id"
      values   = [aws_kms_key.vault_snapshots.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "vault_snapshots" {
  bucket = aws_s3_bucket.vault_snapshots.id
  policy = data.aws_iam_policy_document.vault_snapshots_bucket.json

  # S3 can refuse concurrent changes to one bucket's configuration (OperationAborted).
  depends_on = [aws_s3_bucket_public_access_block.vault_snapshots]
}
