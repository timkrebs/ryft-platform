terraform {
  required_version = "~> 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.59"
    }
  }

  # Local state (git-ignored) unless you enable a backend; see ../terraform.tf.
}
