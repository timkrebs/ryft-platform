# Unit tests: the AWS provider is mocked; nothing is created.
#   terraform init && terraform test

mock_provider "aws" {
  mock_data "aws_caller_identity" {
    defaults = { account_id = "123456789012", arn = "arn:aws:iam::123456789012:user/terraform" }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_resource "aws_kms_key" {
    defaults = { arn = "arn:aws:kms:eu-central-1:123456789012:key/00000000-0000-0000-0000-000000000000" }
  }
  mock_resource "aws_s3_bucket" {
    defaults = { arn = "arn:aws:s3:::ryft-tfstate-123456789012-eu-central-1" }
  }
}

run "the_state_bucket_is_private_versioned_and_kms_encrypted" {
  command = plan

  assert {
    condition     = aws_s3_bucket.state.bucket == "ryft-tfstate-123456789012-eu-central-1"
    error_message = "bucket name from account and region"
  }
  assert {
    condition = (
      aws_s3_bucket_public_access_block.state.block_public_acls && aws_s3_bucket_public_access_block.state.block_public_policy &&
      aws_s3_bucket_public_access_block.state.ignore_public_acls && aws_s3_bucket_public_access_block.state.restrict_public_buckets
    )
    error_message = "no public access in any form"
  }
  assert {
    condition     = aws_s3_bucket_versioning.state.versioning_configuration[0].status == "Enabled"
    error_message = "versioned, so a bad apply can be rolled back"
  }
  assert {
    condition     = one(aws_s3_bucket_server_side_encryption_configuration.state.rule).apply_server_side_encryption_by_default[0].sse_algorithm == "aws:kms"
    error_message = "encrypted with the state key"
  }
  assert {
    condition     = aws_kms_key.state.enable_key_rotation
    error_message = "the key rotates"
  }
}
