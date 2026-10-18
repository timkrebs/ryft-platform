output "bucket" {
  description = "State bucket"
  value       = aws_s3_bucket.state.bucket
}

output "backend_hcl" {
  description = "Partial backend configuration for every root: terraform output -raw backend_hcl > ../../backend.hcl"
  value       = <<-HCL
    bucket     = "${aws_s3_bucket.state.bucket}"
    kms_key_id = "${aws_kms_key.state.arn}"
  HCL
}
