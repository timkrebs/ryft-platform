# Vault's KMS keys and snapshot bucket (vault.tf). Every provider is mocked, nothing is
# created in AWS. Plan runs, plus one mocked apply for the value only apply knows. The two
# keys get distinct ARNs during plan, so a test can tell which key the bucket uses.
#   terraform init -backend=false && terraform test -filter=tests/vault.tftest.hcl

# The upstream modules validate ARNs and IAM policy JSON, so the mocks return
# well-formed values instead of random strings (as in ryft.tftest.hcl).
mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = {
      names = ["eu-central-1a", "eu-central-1b", "eu-central-1c"]
    }
  }
  mock_data "aws_caller_identity" {
    defaults = {
      account_id = "123456789012"
      arn        = "arn:aws:iam::123456789012:user/terraform"
    }
  }
  mock_data "aws_iam_session_context" {
    defaults = {
      issuer_arn = "arn:aws:iam::123456789012:user/terraform"
    }
  }
  mock_data "aws_partition" {
    defaults = {
      partition  = "aws"
      dns_suffix = "amazonaws.com"
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
  mock_resource "aws_launch_template" {
    defaults = {
      id = "lt-0123456789abcdef0"
    }
  }
  mock_resource "aws_iam_policy" {
    defaults = {
      arn = "arn:aws:iam::123456789012:policy/mock"
    }
  }
  mock_resource "aws_kms_key" {
    defaults = {
      arn = "arn:aws:kms:eu-central-1:123456789012:key/00000000-0000-0000-0000-000000000000"
    }
  }
  mock_resource "aws_cloudwatch_log_group" {
    defaults = {
      arn = "arn:aws:logs:eu-central-1:123456789012:log-group:mock"
    }
  }
  mock_data "aws_eks_addon_version" {
    defaults = {
      version = "v1.0.0-eksbuild.1"
    }
  }
  mock_resource "aws_eks_cluster" {
    defaults = {
      arn                   = "arn:aws:eks:eu-central-1:123456789012:cluster/mock"
      certificate_authority = [{ data = "bW9jaw==" }]
      identity = [{
        oidc = [{
          issuer = "https://oidc.eks.eu-central-1.amazonaws.com/id/MOCK"
        }]
      }]
    }
  }
}

mock_provider "tls" {}
mock_provider "time" {}
mock_provider "null" {}
mock_provider "cloudinit" {}

override_resource {
  target          = aws_kms_key.vault_unseal
  override_during = plan
  values = {
    arn    = "arn:aws:kms:eu-central-1:123456789012:key/11111111-1111-1111-1111-111111111111"
    key_id = "11111111-1111-1111-1111-111111111111"
  }
}

override_resource {
  target          = aws_kms_key.vault_snapshots
  override_during = plan
  values = {
    arn    = "arn:aws:kms:eu-central-1:123456789012:key/22222222-2222-2222-2222-222222222222"
    key_id = "22222222-2222-2222-2222-222222222222"
  }
}

override_resource {
  target          = aws_s3_bucket.vault_snapshots
  override_during = plan
  values = {
    arn = "arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012"
  }
}

run "both_keys_rotate_wait_30_days_and_are_administered_by_the_account_only" {
  command = plan

  assert {
    condition = alltrue([for k in [aws_kms_key.vault_unseal, aws_kms_key.vault_snapshots] :
      k.enable_key_rotation == true && k.deletion_window_in_days == 30 && k.key_usage == "ENCRYPT_DECRYPT" &&
      k.policy == data.aws_iam_policy_document.vault_keys.json && k.tags["ManagedBy"] == "terraform"
    ])
    error_message = "both Vault keys rotate, wait 30 days before deletion, encrypt and decrypt, and carry the vault_keys policy and the common tags"
  }

  assert {
    condition = (
      length(data.aws_iam_policy_document.vault_keys.statement) == 1 &&
      data.aws_iam_policy_document.vault_keys.statement[0].sid == "AccountAdministers" &&
      data.aws_iam_policy_document.vault_keys.statement[0].effect == "Allow" &&
      toset(data.aws_iam_policy_document.vault_keys.statement[0].actions) == toset(["kms:*"]) &&
      toset(data.aws_iam_policy_document.vault_keys.statement[0].resources) == toset(["*"]) &&
      length(data.aws_iam_policy_document.vault_keys.statement[0].principals) == 1 &&
      one(data.aws_iam_policy_document.vault_keys.statement[0].principals).type == "AWS" &&
      toset(one(data.aws_iam_policy_document.vault_keys.statement[0].principals).identifiers) == toset(["arn:aws:iam::123456789012:root"]) &&
      length(data.aws_iam_policy_document.vault_keys.statement[0].condition) == 0
    )
    error_message = "the key policy is the one AccountAdministers statement (the account root, kms:*): IAM policies decide who uses the keys, and no role is named"
  }

  assert {
    condition = (
      aws_kms_alias.vault_unseal.name == "alias/ryft-prod-euc1-vault-unseal" &&
      aws_kms_alias.vault_unseal.target_key_id == aws_kms_key.vault_unseal.key_id &&
      aws_kms_alias.vault_snapshots.name == "alias/ryft-prod-euc1-vault-snapshots" &&
      aws_kms_alias.vault_snapshots.target_key_id == aws_kms_key.vault_snapshots.key_id
    )
    error_message = "the aliases are alias/<cluster>-vault-unseal and alias/<cluster>-vault-snapshots (infra/aws/kubernetes looks them up by these names), each on its own key"
  }
}

run "the_bucket_is_private_versioned_and_encrypted_with_the_snapshot_key" {
  command = plan

  assert {
    condition     = aws_s3_bucket.vault_snapshots.bucket == "ryft-prod-euc1-vault-snapshots-123456789012" && aws_s3_bucket.vault_snapshots.force_destroy == false
    error_message = "the bucket is <cluster>-vault-snapshots-<account id>, and Terraform never empties it"
  }

  assert {
    condition     = aws_s3_bucket_versioning.vault_snapshots.versioning_configuration[0].status == "Enabled"
    error_message = "versioning is Enabled"
  }

  assert {
    condition = (
      length(aws_s3_bucket_server_side_encryption_configuration.vault_snapshots.rule) == 1 &&
      one(aws_s3_bucket_server_side_encryption_configuration.vault_snapshots.rule).bucket_key_enabled == true &&
      one(one(aws_s3_bucket_server_side_encryption_configuration.vault_snapshots.rule).apply_server_side_encryption_by_default).sse_algorithm == "aws:kms" &&
      one(one(aws_s3_bucket_server_side_encryption_configuration.vault_snapshots.rule).apply_server_side_encryption_by_default).kms_master_key_id == aws_kms_key.vault_snapshots.arn &&
      one(one(aws_s3_bucket_server_side_encryption_configuration.vault_snapshots.rule).apply_server_side_encryption_by_default).kms_master_key_id != aws_kms_key.vault_unseal.arn
    )
    error_message = "the default encryption is SSE-KMS with the snapshot key (never the unseal key) and the S3 bucket key"
  }

  assert {
    condition = (
      aws_s3_bucket_public_access_block.vault_snapshots.block_public_acls &&
      aws_s3_bucket_public_access_block.vault_snapshots.block_public_policy &&
      aws_s3_bucket_public_access_block.vault_snapshots.ignore_public_acls &&
      aws_s3_bucket_public_access_block.vault_snapshots.restrict_public_buckets
    )
    error_message = "all four public access blocks are on"
  }

  assert {
    condition     = one(aws_s3_bucket_ownership_controls.vault_snapshots.rule).object_ownership == "BucketOwnerEnforced"
    error_message = "ACLs are off: the bucket owner owns every object"
  }
}

run "snapshots_expire_after_30_days_old_versions_after_7_and_stale_uploads_after_1" {
  command = plan

  assert {
    condition = (
      length(aws_s3_bucket_lifecycle_configuration.vault_snapshots.rule) == 1 &&
      aws_s3_bucket_lifecycle_configuration.vault_snapshots.rule[0].status == "Enabled" &&
      one(aws_s3_bucket_lifecycle_configuration.vault_snapshots.rule[0].filter).prefix == "snapshots/" &&
      one(aws_s3_bucket_lifecycle_configuration.vault_snapshots.rule[0].expiration).days == 30 &&
      one(aws_s3_bucket_lifecycle_configuration.vault_snapshots.rule[0].noncurrent_version_expiration).noncurrent_days == 7 &&
      one(aws_s3_bucket_lifecycle_configuration.vault_snapshots.rule[0].abort_incomplete_multipart_upload).days_after_initiation == 1
    )
    error_message = "one enabled rule on snapshots/: current objects expire after 30 days, noncurrent versions after 7, incomplete uploads are aborted after 1"
  }
}

run "the_bucket_policy_denies_plain_http_and_puts_without_the_snapshot_key" {
  command = plan

  assert {
    condition = (
      length(data.aws_iam_policy_document.vault_snapshots_bucket.statement) == 2 &&
      alltrue([for s in data.aws_iam_policy_document.vault_snapshots_bucket.statement :
        s.effect == "Deny" && length(s.principals) == 1 && one(s.principals).type == "*" && toset(one(s.principals).identifiers) == toset(["*"]) && length(s.condition) == 1
      ])
    )
    error_message = "the bucket policy holds exactly two statements, both a Deny for every principal with one condition: it grants nothing"
  }

  assert {
    condition = (
      data.aws_iam_policy_document.vault_snapshots_bucket.statement[0].sid == "DenyInsecureTransport" &&
      toset(data.aws_iam_policy_document.vault_snapshots_bucket.statement[0].actions) == toset(["s3:*"]) &&
      toset(data.aws_iam_policy_document.vault_snapshots_bucket.statement[0].resources) == toset([
        "arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012",
        "arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012/*",
      ]) &&
      one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[0].condition).test == "Bool" &&
      one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[0].condition).variable == "aws:SecureTransport" &&
      tolist(one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[0].condition).values) == tolist(["false"])
    )
    error_message = "every request without TLS is denied, on the bucket and on its objects"
  }

  assert {
    condition = (
      data.aws_iam_policy_document.vault_snapshots_bucket.statement[1].sid == "DenyPutWithoutSnapshotKey" &&
      toset(data.aws_iam_policy_document.vault_snapshots_bucket.statement[1].actions) == toset(["s3:PutObject"]) &&
      toset(data.aws_iam_policy_document.vault_snapshots_bucket.statement[1].resources) == toset(["arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012/*"]) &&
      one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[1].condition).test == "StringNotEquals" &&
      one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[1].condition).variable == "s3:x-amz-server-side-encryption-aws-kms-key-id" &&
      tolist(one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[1].condition).values) == tolist([aws_kms_key.vault_snapshots.arn])
    )
    error_message = "a PutObject that does not name the snapshot key's ARN is denied"
  }
}

run "outputs_hand_the_names_to_infra_aws_kubernetes" {
  command = plan

  assert {
    condition = (
      output.vault_unseal_kms_alias == "alias/ryft-prod-euc1-vault-unseal" &&
      output.vault_unseal_kms_key_arn == aws_kms_key.vault_unseal.arn &&
      output.vault_snapshots_kms_alias == "alias/ryft-prod-euc1-vault-snapshots" &&
      output.vault_snapshots_kms_key_arn == aws_kms_key.vault_snapshots.arn &&
      output.vault_snapshots_bucket == "ryft-prod-euc1-vault-snapshots-123456789012" &&
      output.vault_snapshots_bucket_arn == "arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012"
    )
    error_message = "the key ARNs, aliases and the bucket's name and ARN are outputs"
  }
}

run "another_environment_gets_its_own_keys_and_bucket" {
  command = plan

  variables {
    environment = "dev"
  }

  assert {
    condition = (
      aws_kms_alias.vault_unseal.name == "alias/ryft-dev-euc1-vault-unseal" &&
      aws_kms_alias.vault_snapshots.name == "alias/ryft-dev-euc1-vault-snapshots" &&
      aws_s3_bucket.vault_snapshots.bucket == "ryft-dev-euc1-vault-snapshots-123456789012"
    )
    error_message = "the aliases and the bucket name derive from <project>-<environment>-<location>"
  }
}

# Terraform test does not enforce prevent_destroy (its teardown destroys every mocked
# resource), so the file itself is checked: every KMS key and every S3 bucket in
# vault.tf, the three that exist and any added later, carries prevent_destroy = true.
run "prevent_destroy_on_every_key_and_bucket_in_vault_tf" {
  command = plan

  assert {
    condition = toset([
      for c in split("\nresource \"", file("${path.module}/vault.tf")) :
      join(".", regex("^(aws_kms_key|aws_s3_bucket)\" \"([a-z0-9_]+)\"", c)) if can(regex("^(aws_kms_key|aws_s3_bucket)\" \"", c))
    ]) == toset(["aws_kms_key.vault_unseal", "aws_kms_key.vault_snapshots", "aws_s3_bucket.vault_snapshots"])
    error_message = "vault.tf declares the keys vault_unseal and vault_snapshots and the bucket vault_snapshots; update this test when that changes"
  }

  assert {
    condition = length([
      for c in split("\nresource \"", file("${path.module}/vault.tf")) : c
      if can(regex("^(aws_kms_key|aws_s3_bucket)\" \"", c))
    ]) == 3
    error_message = "vault.tf declares exactly two KMS keys and one S3 bucket"
  }

  assert {
    condition = alltrue([
      for c in split("\nresource \"", file("${path.module}/vault.tf")) :
      can(regex("lifecycle\\s*\\{[^}]*prevent_destroy\\s*=\\s*true", c)) if can(regex("^(aws_kms_key|aws_s3_bucket)\" \"", c))
    ])
    error_message = "every aws_kms_key and aws_s3_bucket in vault.tf needs lifecycle { prevent_destroy = true }"
  }

  # The pattern itself: it must not match a resource without the flag.
  assert {
    condition = !anytrue([
      for c in split("\nresource \"", file("${path.module}/vault.tf")) :
      can(regex("lifecycle\\s*\\{[^}]*prevent_destroy\\s*=\\s*true", c)) if startswith(c, "aws_kms_alias\" ")
    ])
    error_message = "the prevent_destroy pattern matched an alias, which has no lifecycle block: the check is broken"
  }
}

# The bucket policy's document names the snapshot key's ARN, so Terraform reads it only
# at apply (mocked here: nothing is created).
run "the_bucket_carries_that_policy" {
  command = apply

  # Apply-time values here: the plan-time overrides above would be filled with new
  # random values for the remaining attributes at apply.
  override_resource {
    target = aws_kms_key.vault_unseal
    values = {}
  }
  override_resource {
    target = aws_kms_key.vault_snapshots
    values = {}
  }
  override_resource {
    target = aws_s3_bucket.vault_snapshots
    values = {}
  }

  assert {
    condition     = aws_s3_bucket_policy.vault_snapshots.policy == data.aws_iam_policy_document.vault_snapshots_bucket.json
    error_message = "the bucket carries the vault_snapshots_bucket policy"
  }
}
