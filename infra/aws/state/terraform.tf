terraform {
  required_version = "~> 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }

  # Bootstrap: this root's own state stays local (git-ignored). It holds only the
  # bucket and key configuration, no secrets. Recreate it with `terraform import` if
  # lost.
}
