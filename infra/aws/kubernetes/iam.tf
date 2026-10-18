# AWS permissions for the in-cluster controllers, handed out through EKS Pod
# Identity (the agent add-on is installed by ../modules/eks). Each role is bound
# to exactly one service account.

module "aws_load_balancer_controller_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                            = "${var.cluster_name}-aws-lb-controller"
  attach_aws_lb_controller_policy = true

  associations = {
    controller = {
      cluster_name    = var.cluster_name
      namespace       = "kube-system"
      service_account = "aws-load-balancer-controller"
    }
  }

  tags = local.tags
}

module "external_dns_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                          = "${var.cluster_name}-external-dns"
  attach_external_dns_policy    = true
  external_dns_hosted_zone_arns = [data.aws_route53_zone.this.arn]

  associations = {
    controller = {
      cluster_name    = var.cluster_name
      namespace       = "external-dns"
      service_account = "external-dns"
    }
  }

  tags = local.tags
}

module "cert_manager_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                          = "${var.cluster_name}-cert-manager"
  attach_cert_manager_policy    = true
  cert_manager_hosted_zone_arns = [data.aws_route53_zone.this.arn]

  associations = {
    controller = {
      cluster_name    = var.cluster_name
      namespace       = "cert-manager"
      service_account = "cert-manager"
    }
  }

  tags = local.tags
}
