# Vault Community Edition in the cluster (k8s.tf, D191), the only Vault this root writes (D207), through
# `kubectl port-forward` on the operator's machine (docs/OPERATIONS.md -> Vault -> Terraform through the
# port-forward). The arguments that decide where it goes and as whom are set here, so a shell's VAULT_ADDR,
# VAULT_TOKEN and VAULT_CACERT cannot redirect it: the token from TF_VAR_vault_k8s_token only (ephemeral: in neither
# plan nor state; required, so the provider never falls back to VAULT_TOKEN or ~/.vault-token), the CA from a file
# of its own, the address the loopback end of the port-forward, TLS verification on. The provider still reads a few
# variables this block cannot pin (an empty string in config falls through to the environment, provider 5.x
# meta.go): VAULT_NAMESPACE, VAULT_TLS_SERVER_NAME, VAULT_CAPATH, VAULT_CACERT_BYTES, VAULT_CLIENT_CERT/
# VAULT_CLIENT_KEY and VAULT_MAX_RETRIES. The runbook's session shell unsets them; never export them while running
# Terraform.
provider "vault" {
  address          = var.vault_k8s_address
  ca_cert_file     = pathexpand(var.vault_k8s_ca_cert_file)
  skip_tls_verify  = false # explicit: unset, the provider would take VAULT_SKIP_VERIFY from the environment
  token            = var.vault_k8s_token
  skip_child_token = true # the token is already a 1-hour, non-renewable child of the session's admin token (token role terraform)
}

provider "aws" {
  region = var.region
}

# The EKS cluster built by ../aws. Endpoint and CA feed the Kubernetes provider (the namespaces and service
# accounts); the exec block signs kubectl calls with your AWS identity (the cluster admin that ran
# `terraform apply` in ../aws).
data "aws_eks_cluster" "this" {
  name = var.eks_cluster_name
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.this.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.this.certificate_authority[0].data)

  exec {
    api_version = "client.authentication.k8s.io/v1beta1"
    command     = "aws"
    args        = ["eks", "get-token", "--cluster-name", var.eks_cluster_name, "--region", var.region]
  }
}
