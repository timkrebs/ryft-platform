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

# Every mocked policy document has the same json; this one gets its own, so a test can
# tell which document a key carries.
override_data {
  target          = data.aws_iam_policy_document.vault_unseal_key
  override_during = plan
  values = {
    json = "{\"Version\":\"2012-10-17\",\"Statement\":[],\"Id\":\"unseal\"}"
  }
}

run "both_keys_rotate_wait_30_days_and_are_administered_by_the_account_only" {
  command = plan

  assert {
    condition = alltrue([for k in [aws_kms_key.vault_unseal, aws_kms_key.vault_snapshots] :
      k.enable_key_rotation == true && k.deletion_window_in_days == 30 && k.key_usage == "ENCRYPT_DECRYPT" &&
      k.tags["ManagedBy"] == "terraform"
    ])
    error_message = "both Vault keys rotate, wait 30 days before deletion, encrypt and decrypt, and carry the common tags"
  }

  assert {
    condition = (
      aws_kms_key.vault_unseal.policy == data.aws_iam_policy_document.vault_unseal_key.json &&
      aws_kms_key.vault_snapshots.policy == data.aws_iam_policy_document.vault_keys.json &&
      data.aws_iam_policy_document.vault_unseal_key.source_policy_documents == tolist([data.aws_iam_policy_document.vault_keys.json])
    )
    error_message = "the unseal key carries vault_unseal_key (vault_keys plus the optional allowlist), the snapshot key vault_keys"
  }

  assert {
    condition = (
      length(data.aws_iam_policy_document.vault_keys.statement) == 2 &&
      data.aws_iam_policy_document.vault_keys.statement[0].sid == "AccountAdministers" &&
      data.aws_iam_policy_document.vault_keys.statement[0].effect == "Allow" &&
      toset(data.aws_iam_policy_document.vault_keys.statement[0].actions) == toset(["kms:*"]) &&
      toset(data.aws_iam_policy_document.vault_keys.statement[0].resources) == toset(["*"]) &&
      length(data.aws_iam_policy_document.vault_keys.statement[0].principals) == 1 &&
      one(data.aws_iam_policy_document.vault_keys.statement[0].principals).type == "AWS" &&
      toset(one(data.aws_iam_policy_document.vault_keys.statement[0].principals).identifiers) == toset(["arn:aws:iam::123456789012:root"]) &&
      length(data.aws_iam_policy_document.vault_keys.statement[0].condition) == 0
    )
    error_message = "the only Allow is AccountAdministers (the account root, kms:*): IAM policies decide who uses the keys, and no role is named"
  }

  assert {
    condition = (
      data.aws_iam_policy_document.vault_keys.statement[1].sid == "NoDeletionOrDisable" &&
      data.aws_iam_policy_document.vault_keys.statement[1].effect == "Deny" &&
      toset(data.aws_iam_policy_document.vault_keys.statement[1].actions) == toset(["kms:ScheduleKeyDeletion", "kms:DisableKey"]) &&
      toset(data.aws_iam_policy_document.vault_keys.statement[1].resources) == toset(["*"]) &&
      length(data.aws_iam_policy_document.vault_keys.statement[1].principals) == 1 &&
      one(data.aws_iam_policy_document.vault_keys.statement[1].principals).type == "AWS" &&
      toset(one(data.aws_iam_policy_document.vault_keys.statement[1].principals).identifiers) == toset(["*"]) &&
      length(data.aws_iam_policy_document.vault_keys.statement[1].condition) == 0
    )
    error_message = "both keys deny ScheduleKeyDeletion and DisableKey to every principal, unconditionally (PutKeyPolicy stays allowed: no lockout)"
  }

  assert {
    condition     = length(data.aws_iam_policy_document.vault_unseal_key.statement) == 0
    error_message = "without vault_unseal_allowed_principal_arns the unseal key has no allowlist statement"
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
      length(data.aws_iam_policy_document.vault_snapshots_bucket.statement) == 3 &&
      alltrue([for s in data.aws_iam_policy_document.vault_snapshots_bucket.statement :
        s.effect == "Deny" && length(s.principals) == 1 && one(s.principals).type == "*" && toset(one(s.principals).identifiers) == toset(["*"]) && length(s.condition) == 1
      ])
    )
    error_message = "the bucket policy holds exactly three statements, each a Deny for every principal with one condition: it grants nothing"
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

  # Create-only writes (PR #146's review, PR 4): without If-None-Match a put is denied, so an
  # existing snapshot cannot be overwritten, not by the job's identity either.
  assert {
    condition = (
      data.aws_iam_policy_document.vault_snapshots_bucket.statement[2].sid == "DenyPutWithoutIfNoneMatch" &&
      toset(data.aws_iam_policy_document.vault_snapshots_bucket.statement[2].actions) == toset(["s3:PutObject"]) &&
      toset(data.aws_iam_policy_document.vault_snapshots_bucket.statement[2].resources) == toset(["arn:aws:s3:::ryft-prod-euc1-vault-snapshots-123456789012/*"]) &&
      one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[2].condition).test == "Null" &&
      one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[2].condition).variable == "s3:if-none-match" &&
      tolist(one(data.aws_iam_policy_document.vault_snapshots_bucket.statement[2].condition).values) == tolist(["true"])
    )
    error_message = "a PutObject without If-None-Match is denied (create-only: nothing overwrites a snapshot)"
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

  # The flag must be a line of its own inside lifecycle { }: "(?m)^[ \t]*" refuses a
  # commented-out "# prevent_destroy = true".
  assert {
    condition = alltrue([
      for c in split("\nresource \"", file("${path.module}/vault.tf")) :
      can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true", c)) if can(regex("^(aws_kms_key|aws_s3_bucket)\" \"", c))
    ])
    error_message = "every aws_kms_key and aws_s3_bucket in vault.tf needs lifecycle { prevent_destroy = true }, not commented out"
  }

  # The pattern itself: it matches the flag, and neither an alias (no lifecycle block)
  # nor a commented-out flag.
  assert {
    condition = (
      can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true", "  lifecycle {\n    prevent_destroy = true\n  }\n")) &&
      !can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true", "  lifecycle {\n    # prevent_destroy = true\n  }\n")) &&
      !can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true", "  lifecycle {\n    // prevent_destroy = true\n  }\n")) &&
      !anytrue([
        for c in split("\nresource \"", file("${path.module}/vault.tf")) :
        can(regex("(?m)lifecycle\\s*\\{[^}]*^[ \\t]*prevent_destroy\\s*=\\s*true", c)) if startswith(c, "aws_kms_alias\" ")
      ])
    )
    error_message = "the prevent_destroy pattern is broken: it must match the flag and neither an alias nor a commented-out flag"
  }
}

run "the_unseal_allowlist_denies_every_other_principal_when_set" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = [
      "arn:aws:iam::123456789012:role/ryft-prod-euc1-vault-unseal-*",
      "arn:aws:iam::123456789012:role/aws-reserved/sso.amazonaws.com/*AWSReservedSSO_AdministratorAccess_*",
    ]
  }

  assert {
    condition = (
      length(data.aws_iam_policy_document.vault_unseal_key.statement) == 1 &&
      data.aws_iam_policy_document.vault_unseal_key.statement[0].sid == "OnlyListedPrincipalsUseTheUnsealKey" &&
      data.aws_iam_policy_document.vault_unseal_key.statement[0].effect == "Deny" &&
      toset(data.aws_iam_policy_document.vault_unseal_key.statement[0].actions) == toset(["kms:Decrypt", "kms:Encrypt", "kms:ReEncrypt*", "kms:GenerateDataKey*"]) &&
      toset(data.aws_iam_policy_document.vault_unseal_key.statement[0].resources) == toset(["*"]) &&
      one(data.aws_iam_policy_document.vault_unseal_key.statement[0].principals).type == "AWS" &&
      toset(one(data.aws_iam_policy_document.vault_unseal_key.statement[0].principals).identifiers) == toset(["*"]) &&
      length(data.aws_iam_policy_document.vault_unseal_key.statement[0].condition) == 1 &&
      one(data.aws_iam_policy_document.vault_unseal_key.statement[0].condition).test == "ArnNotLike" &&
      one(data.aws_iam_policy_document.vault_unseal_key.statement[0].condition).variable == "aws:PrincipalArn" &&
      tolist(one(data.aws_iam_policy_document.vault_unseal_key.statement[0].condition).values) == var.vault_unseal_allowed_principal_arns
    )
    error_message = "with the allowlist set, the unseal key denies Encrypt, Decrypt, ReEncrypt* and GenerateDataKey* to every principal whose ARN is not like one of the list"
  }

  assert {
    condition = (
      aws_kms_key.vault_unseal.policy == data.aws_iam_policy_document.vault_unseal_key.json &&
      aws_kms_key.vault_snapshots.policy == data.aws_iam_policy_document.vault_keys.json &&
      length(data.aws_iam_policy_document.vault_keys.statement) == 2
    )
    error_message = "the allowlist is on the unseal key only; the snapshot key keeps vault_keys"
  }

  assert {
    condition     = output.vault_unseal_role_arn_pattern == "arn:aws:iam::123456789012:role/ryft-prod-euc1-vault-unseal-*"
    error_message = "the output names the pattern of the role infra/aws/kubernetes creates for the seal"
  }
}

run "an_allowlist_without_vault_s_role_warns" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["arn:aws:iam::123456789012:role/admin"]
  }

  expect_failures = [check.vault_unseal_allowlist_names_the_vault_role]
}

# A prefix without "-*" matches no role (ArnNotLike compares the whole ARN), nor does a
# typo in the pattern: both must warn like a missing entry.
run "an_allowlist_with_only_the_bare_prefix_warns" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["arn:aws:iam::123456789012:role/ryft-prod-euc1-vault-unseal"]
  }

  expect_failures = [check.vault_unseal_allowlist_names_the_vault_role]
}

run "an_allowlist_with_a_mistyped_pattern_warns" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["arn:aws:iam::123456789012:role/ryft-prod-euc1-vault-unseal-x*"]
  }

  expect_failures = [check.vault_unseal_allowlist_names_the_vault_role]
}

# The two accepted forms, and the rest pinned on the local the check reads (a passing
# check leaves no trace a run could assert on).
run "the_allowlist_check_accepts_the_exact_role_arn" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["arn:aws:iam::123456789012:role/ryft-prod-euc1-vault-unseal-20261106120000000000000001"]
  }

  assert {
    condition     = local.vault_unseal_allowlist_names_the_vault_role
    error_message = "the exact role ARN (the name plus the module's generated suffix) satisfies the check"
  }
}

run "the_allowlist_check_accepts_the_output_pattern" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["arn:aws:iam::123456789012:role/ryft-prod-euc1-vault-unseal-*"]
  }

  assert {
    condition     = local.vault_unseal_allowlist_names_the_vault_role
    error_message = "the pattern from the output vault_unseal_role_arn_pattern satisfies the check"
  }
}

run "rejects_an_allowlist_entry_that_is_no_iam_arn" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["*"]
  }

  expect_failures = [var.vault_unseal_allowed_principal_arns]
}

run "rejects_an_allowlist_entry_for_any_account" {
  command = plan

  variables {
    vault_unseal_allowed_principal_arns = ["arn:aws:iam::*:role/ryft-prod-euc1-vault-unseal-*"]
  }

  expect_failures = [var.vault_unseal_allowed_principal_arns]
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
