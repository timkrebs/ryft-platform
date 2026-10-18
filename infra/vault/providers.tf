# Vault: the address and parent namespace come from the variables (so a missing
# VAULT_ADDR cannot break an apply halfway); only the token comes from the
# environment (VAULT_TOKEN, or ~/.vault-token), never from a file in this
# directory. The token needs sys/namespaces, sys/auth, sys/mounts and
# sys/policies in the parent namespace (an HCP admin token does).
provider "vault" {
  address   = coalesce(var.vault_address, var.vault_address_for_cluster)
  namespace = var.vault_parent_namespace_root != "" ? var.vault_parent_namespace_root : null
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
