data "aws_eks_cluster" "this" {
  name = var.cluster_name
}

# The zone from ../dns. Its nameservers must be live at GoDaddy before
# cert-manager can answer DNS-01 challenges.
data "aws_route53_zone" "this" {
  name         = var.domain
  private_zone = false
}

locals {
  vpc_id = data.aws_eks_cluster.this.vpc_config[0].vpc_id

  tags = merge(var.tags, {
    Project   = "ryft"
    ManagedBy = "terraform"
  })

  # Pinned chart versions; bump deliberately and read the changelog.
  charts = {
    aws_load_balancer_controller = { repository = "https://aws.github.io/eks-charts", chart = "aws-load-balancer-controller", version = "3.6.0" }
    traefik                      = { repository = "https://traefik.github.io/charts", chart = "traefik", version = "42.0.0" }
    cert_manager                 = { repository = "https://charts.jetstack.io", chart = "cert-manager", version = "v1.21.2" }
    external_dns                 = { repository = "https://kubernetes-sigs.github.io/external-dns", chart = "external-dns", version = "1.23.0" }
    vault_secrets_operator       = { repository = "https://helm.releases.hashicorp.com", chart = "vault-secrets-operator", version = "1.6.0" }
    aws_for_fluent_bit           = { repository = "https://aws.github.io/eks-charts", chart = "aws-for-fluent-bit", version = "0.2.0" }
    argocd                       = { repository = "https://argoproj.github.io/argo-helm", chart = "argo-cd", version = "10.10.5" }
  }
}
