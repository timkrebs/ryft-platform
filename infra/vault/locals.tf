locals {
  # ryft-api in Kubernetes; role, policy, mount kv-api and service account named after the service on the
  # in-cluster Vault (k8s.tf). CE has no namespaces: the isolation is the mount, the role and the policy (D187).
  services = {
    for name, s in var.services : name => {
      k8s_ns          = coalesce(s.k8s_namespace, "${var.project}-${name}")
      service_account = coalesce(s.service_account, name)
      secret_keys     = s.secret_keys
      database        = s.database
      amqp            = s.amqp
      workload_kind   = s.workload_kind
      data_key        = s.data_key
      admin_key       = s.admin_key
      pod_security    = s.pod_security
      workload_name   = coalesce(s.workload_name, name)
      restart_targets = concat(s.workload_kind == "" ? [] : [{ kind = s.workload_kind, name = coalesce(s.workload_name, name) }], s.extra_restarts)
    }
  }

  # What the in-cluster Vault serves (the chart in infra/aws/kubernetes, output vault_address_in_cluster): every
  # generated VaultConnection names it (D207). A local, not a variable: the admission chart's P4 admits exactly this
  # address (vaultAddresses).
  vault_k8s_address_for_cluster = "https://vault.vault.svc:8200"

  # One KV v2 mount per service (k8s.tf). The generator and the terraform policy's render (bootstrap.tf) read the
  # same names.
  k8s_kv_mount = { for name in keys(var.services) : name => "kv-${name}" }

  # The snapshot CronJob's identity (charts/vault-extras, k8s.tf): role vault-snapshot, bound to vault/vault-snapshot,
  # carrying policy snapshot. k8s.tf and the terraform policy's render (bootstrap.tf) read these names, so the token
  # can write exactly this role and this policy.
  k8s_snapshot = { name = "vault-snapshot", policy = "snapshot", service_account = "vault-snapshot", k8s_ns = "vault" }

  # Names a service may not have: policies and roles share the root namespace with Vault's own (default, root), the
  # bootstrap's (admin, terraform) and the snapshot job's (role vault-snapshot, policy snapshot), and kv-operator
  # holds the demo password (variables.tf validates var.services against this list). The admission chart's
  # vso.reservedServices names the same seven names (keep the two lists equal); a kv-snapshot mount cannot exist
  # while no service has that name.
  reserved_service_names = ["default", "root", "admin", "terraform", "vault-snapshot", "snapshot", "operator"]

  # Postgres role per service (created by migration 0009; the migrate job sets the
  # passwords generated here) and RabbitMQ user per client (deploy/rabbitmq/topology).
  db_roles     = { for name, s in local.services : name => "${var.project}_${name}" if s.database == "role" }
  amqp_clients = sort([for name, s in local.services : name if s.amqp == "client"])
  amqp_server  = one([for name, s in local.services : name if s.amqp == "server"])

  db_location = "${var.database.host}:${var.database.port}/${var.database.name}?sslmode=${var.database.sslmode}"
  db_url      = { for name, role in local.db_roles : name => "postgresql://${role}.${var.supabase_project_ref}:${random_password.db[name].result}@${local.db_location}" }
  owner_url   = "postgresql://${var.database.owner}.${var.supabase_project_ref}:${urlencode(var.database_owner_password)}@${local.db_location}"

  # Key names are not secret; they drive for_each and the outputs, which may not
  # depend on sensitive values.
  shared_keys  = nonsensitive(keys(var.shared_secrets))
  private_keys = { for name in keys(local.services) : name => nonsensitive(keys(lookup(var.service_secrets, name, {}))) }

  generated_keys = {
    for name, s in local.services : name => concat(
      contains(s.secret_keys, "SUPABASE_PROJECT_REF") ? ["SUPABASE_PROJECT_REF"] : [],
      s.database == "role" ? ["DATABASE_URL"] : [],
      s.database == "owner" ? concat(["DATABASE_URL"], [for c in keys(local.db_roles) : "DB_PASSWORD_${upper(c)}"]) : [],
      s.amqp == "client" ? ["AMQP_USER", "AMQP_PASSWORD"] : [],
      s.data_key ? ["DATA_ENCRYPTION_KEY"] : [],
      s.admin_key ? ["ADMIN_API_KEY"] : [],
    )
  }

  service_secret_keys = {
    for name, s in local.services : name => sort(distinct(concat(
      [for k in s.secret_keys : k if contains(local.shared_keys, k)],
      local.generated_keys[name],
      local.private_keys[name],
    )))
  }

  # What each service may read, and nothing more: a role's own URL, never the
  # owner's; the owner only in migrate.
  generated = {
    for name, s in local.services : name => merge(
      contains(s.secret_keys, "SUPABASE_PROJECT_REF") ? { SUPABASE_PROJECT_REF = var.supabase_project_ref } : {},
      s.database == "role" ? { DATABASE_URL = local.db_url[name] } : {},
      s.database == "owner" ? merge(
        { DATABASE_URL = local.owner_url },
        { for c in keys(local.db_roles) : "DB_PASSWORD_${upper(c)}" => random_password.db[c].result },
      ) : {},
      s.amqp == "client" ? { AMQP_USER = name, AMQP_PASSWORD = random_password.amqp[name].result } : {},
      s.data_key ? { DATA_ENCRYPTION_KEY = random_bytes.data_key[name].base64 } : {},
      s.admin_key ? { ADMIN_API_KEY = random_password.admin_key[name].result } : {},
    )
  }

  service_kv = {
    for name, s in local.services : name => merge(
      { for k in s.secret_keys : k => var.shared_secrets[k] if contains(local.shared_keys, k) },
      local.generated[name],
      lookup(var.service_secrets, name, {}),
    )
  }

  services_with_secrets = { for name, keys in local.service_secret_keys : name => name if length(keys) > 0 }
}
