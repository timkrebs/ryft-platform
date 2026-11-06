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

# Vault (Raft) and RabbitMQ (quorum queues) run three voters, one per node. On Spot, two
# interruptions at once lose the quorum: Vault stops serving and the broker stops
# accepting writes until a node returns. A plan with SPOT still works; it warns.
check "stateful_quorum_needs_on_demand" {
  assert {
    condition     = var.node_group.capacity_type == "ON_DEMAND"
    error_message = "node_group.capacity_type is ${var.node_group.capacity_type}: the Vault and RabbitMQ quorums (three voters on three nodes) need ON_DEMAND nodes. Spot needs a decision of its own (for example a separate on-demand group for the stateful workloads)."
  }
}
