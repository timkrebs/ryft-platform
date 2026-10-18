# Container logs → CloudWatch Logs (docs/OBSERVABILITY.md → Logs, D52). Only the RYFT
# namespaces and Traefik are shipped. The services' lines carry user IDs and daily
# scores, so this is health data: one log group, KMS-encrypted, kept 14 days,
# readable only by identities with access to this account's logs and key.

data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

locals {
  log_group_name = "/ryft/${var.cluster_name}/containers"
  account_id     = data.aws_caller_identity.current.account_id
  partition      = data.aws_partition.current.partition
}

data "aws_iam_policy_document" "logs_key" {
  statement {
    sid       = "AccountAdministers"
    actions   = ["kms:*"]
    resources = ["*"]
    principals {
      type        = "AWS"
      identifiers = ["arn:${local.partition}:iam::${local.account_id}:root"]
    }
  }
  # CloudWatch Logs may use the key for RYFT's log groups only.
  statement {
    sid       = "CloudWatchLogsForRyft"
    actions   = ["kms:Encrypt*", "kms:Decrypt*", "kms:ReEncrypt*", "kms:GenerateDataKey*", "kms:Describe*"]
    resources = ["*"]
    principals {
      type        = "Service"
      identifiers = ["logs.${var.region}.amazonaws.com"]
    }
    condition {
      test     = "ArnLike"
      variable = "kms:EncryptionContext:aws:logs:arn"
      values   = ["arn:${local.partition}:logs:${var.region}:${local.account_id}:log-group:/ryft/*"]
    }
  }
}

resource "aws_kms_key" "logs" {
  description             = "RYFT container logs in CloudWatch (health data)"
  enable_key_rotation     = true
  deletion_window_in_days = 30
  policy                  = data.aws_iam_policy_document.logs_key.json
  tags                    = local.tags
}

resource "aws_kms_alias" "logs" {
  name          = "alias/${var.cluster_name}-logs"
  target_key_id = aws_kms_key.logs.key_id
}

resource "aws_cloudwatch_log_group" "containers" {
  name              = local.log_group_name
  retention_in_days = 14
  kms_key_id        = aws_kms_key.logs.arn
  tags              = local.tags
}

module "fluent_bit_pod_identity" {
  source  = "terraform-aws-modules/eks-pod-identity/aws"
  version = "~> 2.10"

  name                 = "${var.cluster_name}-fluent-bit"
  attach_custom_policy = true
  policy_statements = [
    {
      sid       = "WriteRyftLogs"
      actions   = ["logs:CreateLogStream", "logs:PutLogEvents", "logs:DescribeLogStreams"]
      resources = [aws_cloudwatch_log_group.containers.arn, "${aws_cloudwatch_log_group.containers.arn}:*"]
    },
    {
      sid       = "FindLogGroups"
      actions   = ["logs:DescribeLogGroups"]
      resources = ["*"]
    },
  ]

  associations = {
    fluent_bit = {
      cluster_name    = var.cluster_name
      namespace       = "amazon-cloudwatch"
      service_account = "aws-for-fluent-bit"
    }
  }

  tags = local.tags
}

# Fluent Bit as a DaemonSet (it reads /var/log on every node, so its namespace
# cannot be Pod Security restricted). JSON lines from the services are parsed into
# fields (data.request_id, data.user_id, …) for CloudWatch Logs Insights.
resource "helm_release" "fluent_bit" {
  name             = "aws-for-fluent-bit"
  namespace        = "amazon-cloudwatch"
  create_namespace = true
  repository       = local.charts.aws_for_fluent_bit.repository
  chart            = local.charts.aws_for_fluent_bit.chart
  version          = local.charts.aws_for_fluent_bit.version

  values = [yamlencode({
    serviceAccount = {
      create = true
      name   = "aws-for-fluent-bit"
    }
    input = {
      path = "/var/log/containers/*_ryft-*_*.log,/var/log/containers/traefik-*_kube-system_*.log"
    }
    cloudWatchLogs = {
      enabled         = true
      region          = var.region
      logGroupName    = aws_cloudwatch_log_group.containers.name
      logStreamPrefix = "pod-"
      autoCreateGroup = false # Terraform owns it, with the key and retention
    }
    resources = {
      requests = { cpu = "20m", memory = "64Mi" }
      limits   = { memory = "250Mi" }
    }
  })]

  depends_on = [module.fluent_bit_pod_identity]
}
