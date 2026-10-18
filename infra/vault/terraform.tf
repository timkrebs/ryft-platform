terraform {
  required_version = "~> 1.10"

  required_providers {
    vault = {
      source  = "hashicorp/vault"
      version = "~> 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.38"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    local = {
      source  = "hashicorp/local"
      version = "~> 2.5"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
  }

  # Same rule as infra/aws: local state until you pick a backend. The state holds
  # every credential this root writes into Vault (generated passwords, the owner
  # password) in plaintext: keep it on an encrypted disk, or move it to an encrypted
  # remote backend before anyone else runs this.
  # backend "s3" {
  #   bucket       = "<state-bucket>"
  #   key          = "ryft/vault/prod.tfstate"
  #   region       = "eu-central-1"
  #   use_lockfile = true
  # }
}
