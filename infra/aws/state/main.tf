# RETIRED (D56): the four roots keep their state in HCP Terraform now. This root
# only exists so that `terraform apply` then `terraform destroy` here removes the
# bucket and its key; delete the directory afterwards.
#
# One bucket for the remote state of every root (infra/aws, infra/aws/dns,
# infra/aws/kubernetes, infra/vault), one key each, locked with S3 lockfiles.
# The states hold every credential infra/vault writes, so the bucket is private,
# TLS-only, versioned and encrypted with its own KMS key: reading state needs both
# S3 and KMS permissions.

data "aws_caller_identity" "current" {}

locals {
  bucket = "ryft-tfstate-${data.aws_caller_identity.current.account_id}-${var.region}"
}

resource "aws_kms_key" "state" {
  description             = "RYFT Terraform state"
  enable_key_rotation     = true
  deletion_window_in_days = 30
}

resource "aws_kms_alias" "state" {
  name          = "alias/ryft-tfstate"
  target_key_id = aws_kms_key.state.key_id
}

resource "aws_s3_bucket" "state" {
  bucket = local.bucket
  # Retired: the states moved to HCP Terraform (D56). force_destroy lets
  # `terraform destroy` remove the bucket with every object version.
  force_destroy = true
}

resource "aws_s3_bucket_ownership_controls" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  bucket                  = aws_s3_bucket.state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "state" {
  bucket = aws_s3_bucket.state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm     = "aws:kms"
      kms_master_key_id = aws_kms_key.state.arn
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  bucket = aws_s3_bucket.state.id
  rule {
    id     = "old-state-versions"
    status = "Enabled"
    filter {}
    noncurrent_version_expiration {
      noncurrent_days = var.noncurrent_days
    }
    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }
}

data "aws_iam_policy_document" "state" {
  statement {
    sid     = "DenyInsecureTransport"
    effect  = "Deny"
    actions = ["s3:*"]
    resources = [
      aws_s3_bucket.state.arn,
      "${aws_s3_bucket.state.arn}/*",
    ]
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
}

resource "aws_s3_bucket_policy" "state" {
  bucket = aws_s3_bucket.state.id
  policy = data.aws_iam_policy_document.state.json

  depends_on = [aws_s3_bucket_public_access_block.state]
}
