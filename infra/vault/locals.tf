locals {
  # ryft/api in Vault, ryft-api in Kubernetes, role/policy/SA named after the service.
  # Resources take Vault namespaces relative to the provider's namespace (vault_ns);
  # the operator and the vault CLI need the path from the root (vault_ns_full), which
  # on HCP starts with admin/.
  services = {
    for name, s in var.services : name => {
      vault_ns        = "${var.project}/${name}"
      vault_ns_full   = join("/", compact([var.vault_parent_namespace_root, var.project, name]))
      k8s_ns          = "${var.project}-${name}"
      service_account = coalesce(s.service_account, name)
      secret_keys     = s.secret_keys
      database        = s.database
      nats            = s.nats
      workload_kind   = s.workload_kind
      data_key        = s.data_key
    }
  }

  # Postgres role per service (created by migration 0009; the migrate job sets the
  # passwords generated here) and NATS user per client (deploy/k8s/base/nats/nats.conf).
  db_roles     = { for name, s in local.services : name => "${var.project}_${name}" if s.database == "role" }
  nats_clients = sort([for name, s in local.services : name if s.nats == "client"])

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
      s.nats == "client" ? ["NATS_USER", "NATS_PASSWORD"] : [],
      s.nats == "server" ? [for c in local.nats_clients : "NATS_PASSWORD_${upper(c)}"] : [],
      s.data_key ? ["DATA_ENCRYPTION_KEY"] : [],
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
  # owner's; the NATS server only bcrypt hashes; the owner only in migrate.
  generated = {
    for name, s in local.services : name => merge(
      contains(s.secret_keys, "SUPABASE_PROJECT_REF") ? { SUPABASE_PROJECT_REF = var.supabase_project_ref } : {},
      s.database == "role" ? { DATABASE_URL = local.db_url[name] } : {},
      s.database == "owner" ? merge(
        { DATABASE_URL = local.owner_url },
        { for c in keys(local.db_roles) : "DB_PASSWORD_${upper(c)}" => random_password.db[c].result },
      ) : {},
      s.nats == "client" ? { NATS_USER = name, NATS_PASSWORD = random_password.nats[name].result } : {},
      s.nats == "server" ? { for c in local.nats_clients : "NATS_PASSWORD_${upper(c)}" => random_password.nats[c].bcrypt_hash } : {},
      s.data_key ? { DATA_ENCRYPTION_KEY = random_bytes.data_key[name].base64 } : {},
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
