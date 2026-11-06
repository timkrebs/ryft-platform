# Vault: the address and parent namespace come from the variables (so a missing
# VAULT_ADDR cannot break an apply halfway); only the token comes from the
# environment (VAULT_TOKEN, or ~/.vault-token), never from a file in this
# directory. The token needs sys/namespaces, sys/auth, sys/mounts and
# sys/policies in the parent namespace (an HCP admin token does).
provider "vault" {
  address   = coalesce(var.vault_address, var.vault_address_for_cluster)
  namespace = var.vault_parent_namespace_root != "" ? var.vault_parent_namespace_root : null
}

# Vault Community Edition in the cluster (k8s.tf, D191), through `kubectl port-forward` on the operator's machine
# (docs/OPERATIONS.md -> Vault -> Terraform through the port-forward). The arguments that decide where it goes and
# as whom are set here, so the HCP shell's VAULT_ADDR, VAULT_TOKEN and VAULT_CACERT cannot reach it: the token from
# TF_VAR_vault_k8s_token only (ephemeral: in neither plan nor state), the CA from a file of its own, the address the
# loopback end of the port-forward, TLS verification on. The provider still reads a few variables this block
# cannot pin (an empty string in config falls through to the environment, provider 5.x meta.go):
# VAULT_NAMESPACE, VAULT_TLS_SERVER_NAME, VAULT_CAPATH, VAULT_CACERT_BYTES, VAULT_CLIENT_CERT/VAULT_CLIENT_KEY and
# VAULT_MAX_RETRIES. The runbook's Terraform shell unsets them; never export
# them, nor VAULT_CACERT, while running Terraform (the HCP provider would read those too).
#
# While write_k8s is false (targets.tf) no resource instance uses this provider, and the plan must not need the
# port-forward, the CA file or the token: the provider builds its client only on the first request, the token is
# a placeholder (never the HCP token from VAULT_TOKEN, which an empty token would fall back to) and the CA file is
# not read. tests/k8s_offline.tftest.hcl plans with the real provider against a closed port to hold that.
provider "vault" {
  alias            = "k8s"
  address          = var.vault_k8s_address
  ca_cert_file     = local.write_k8s ? pathexpand(var.vault_k8s_ca_cert_file) : null
  skip_tls_verify  = false # explicit: unset, the provider would take VAULT_SKIP_VERIFY from the environment
  token            = local.write_k8s ? var.vault_k8s_token : "write-k8s-is-off"
  skip_child_token = true # the token is already a 1-hour, non-renewable child of the operator's login (token role terraform)
}

provider "aws" {
  region = var.region
}

# The EKS cluster built by ../aws. Endpoint and CA feed the Kubernetes auth mounts;
# the exec block signs kubectl calls with your AWS identity (the cluster admin that
# ran `terraform apply` in ../aws).
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
