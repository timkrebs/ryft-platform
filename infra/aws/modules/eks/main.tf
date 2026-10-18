data "aws_partition" "current" {}

# IAM role for the EBS CSI controller, handed to it through EKS Pod Identity
# (what AWS recommends over IRSA for the add-on). AmazonEBSCSIDriverPolicyV2 only
# lets the driver touch volumes it tagged itself.
module "ebs_csi_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name = "${var.name}-ebs-csi"

  additional_policy_arns = {
    AmazonEBSCSIDriverPolicyV2 = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonEBSCSIDriverPolicyV2"
  }

  tags = var.tags
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 21.29"

  name               = var.name
  kubernetes_version = var.kubernetes_version

  endpoint_public_access       = var.endpoint_public_access
  endpoint_public_access_cidrs = var.endpoint_public_access_cidrs
  deletion_protection          = var.deletion_protection

  # The identity running terraform apply becomes cluster admin via an access entry.
  enable_cluster_creator_admin_permissions = true

  vpc_id     = var.vpc_id
  subnet_ids = var.subnet_ids

  addons = {
    # Needed by the nodes themselves, so installed before the node group.
    vpc-cni = {
      before_compute = true
      # Enforce Kubernetes NetworkPolicy (deploy/k8s ships a default-deny policy);
      # without this the policies are accepted but ignored.
      configuration_values = jsonencode({ enableNetworkPolicy = "true" })
    }
    eks-pod-identity-agent = {
      before_compute = true
    }
    kube-proxy = {}
    coredns    = {}

    # PersistentVolumes for Postgres, NATS and backups. EKS has no default
    # StorageClass; this creates gp3 "ebs-csi-default-sc" and marks it default.
    aws-ebs-csi-driver = {
      configuration_values = jsonencode({ defaultStorageClass = { enabled = true } })
      pod_identity_association = [{
        role_arn        = module.ebs_csi_pod_identity.iam_role_arn
        service_account = "ebs-csi-controller-sa"
      }]
    }

    # Resource metrics for the HorizontalPodAutoscalers and `kubectl top`
    # (EKS community add-on).
    metrics-server = {}
  }

  eks_managed_node_groups = {
    default = {
      name           = "${var.name}-default"
      ami_type       = var.node_group.ami_type
      instance_types = var.node_group.instance_types
      capacity_type  = var.node_group.capacity_type

      min_size     = var.node_group.min_size
      max_size     = var.node_group.max_size
      desired_size = var.node_group.desired_size
    }
  }

  tags = var.tags
}
