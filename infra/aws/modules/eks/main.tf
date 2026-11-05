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

  # How the API server authenticates IAM principals. API_AND_CONFIG_MAP reads both access
  # entries and the kube-system/aws-auth ConfigMap; API reads access entries only and makes
  # aws-auth inert. The change to API is one-way in EKS (docs/OPERATIONS.md -> EKS
  # authentication mode API).
  authentication_mode = var.authentication_mode

  # The identity running terraform apply becomes cluster admin via an access entry.
  enable_cluster_creator_admin_permissions = true

  # Further cluster admins (AmazonEKSClusterAdminPolicy, cluster scope), only for a principal
  # that uses kubectl but does not run terraform. Empty by default.
  access_entries = {
    for label, arn in var.admin_principal_arns : "admin-${label}" => {
      principal_arn = arn
      type          = "STANDARD"
      policy_associations = {
        admin = {
          policy_arn   = "arn:${data.aws_partition.current.partition}:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
          access_scope = { type = "cluster" }
        }
      }
    }
  }

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

    # PersistentVolumes for RabbitMQ, backups (and Postgres until Supabase). The
    # add-on's own default StorageClass is left off: it would create unencrypted
    # volumes. infra/aws/kubernetes defines the encrypted default "gp3-encrypted".
    aws-ebs-csi-driver = {
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

# The managed node group's role. EKS creates this access entry itself when the node group is
# created in a cluster whose mode includes API (the upstream module does not manage it), so
# this stays off unless `aws eks list-access-entries` shows the role missing BEFORE the switch
# to API. Turning it on while EKS already created it fails with ResourceInUseException.
resource "aws_eks_access_entry" "node" {
  count = var.node_access_entry ? 1 : 0

  cluster_name  = module.eks.cluster_name
  principal_arn = module.eks.eks_managed_node_groups["default"].iam_role_arn
  type          = "EC2_LINUX"

  tags = var.tags
}
