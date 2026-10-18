module "network" {
  source = "./modules/network"

  name                       = local.name
  cidr                       = var.vpc_cidr
  single_nat_gateway         = var.single_nat_gateway
  flow_log_retention_in_days = var.vpc_flow_log_retention_in_days

  tags = local.tags
}

module "eks" {
  source = "./modules/eks"

  name               = local.name
  kubernetes_version = var.kubernetes_version

  vpc_id     = module.network.vpc_id
  subnet_ids = module.network.private_subnet_ids

  endpoint_public_access_cidrs = var.endpoint_public_access_cidrs
  deletion_protection          = var.deletion_protection
  node_group                   = var.node_group

  tags = local.tags
}
