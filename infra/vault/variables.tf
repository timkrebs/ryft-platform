variable "region" {
  description = "AWS region of the EKS cluster"
  type        = string
  default     = "eu-central-1"
}

variable "eks_cluster_name" {
  description = "Name of the EKS cluster from ../aws (terraform output cluster_name)"
  type        = string
  default     = "ryft-prod-euc1"
}

variable "project" {
  description = "Prefix for every Vault and Kubernetes namespace"
  type        = string
  default     = "ryft"
}

variable "vault_parent_namespace_root" {
  description = "Vault namespace the provider token lives in and under which the project namespace is created. Empty = root namespace (Vault Enterprise self-managed); \"admin\" on HCP Vault Dedicated."
  type        = string
  default     = ""
}

variable "vault_address" {
  description = "Vault address as reachable from where you run Terraform. Empty = vault_address_for_cluster (right for HCP's public endpoint)."
  type        = string
  default     = ""
}

variable "vault_address_for_cluster" {
  description = "Vault address as reachable FROM the EKS pods (used in the generated VaultConnection). Usually the same as VAULT_ADDR."
  type        = string

  validation {
    condition     = startswith(var.vault_address_for_cluster, "https://")
    error_message = "vault_address_for_cluster must be an https:// URL; the operator sends Vault tokens over it."
  }
}

variable "vault_ca_bundle_b64" {
  description = "Base64 PEM CA bundle for the Vault listener, if it is not publicly trusted; written into the VaultConnection"
  type        = string
  default     = ""
}

# One entry per workload. Each gets: a Vault namespace <project>/<name>, a Kubernetes
# namespace <project>-<name>, a Kubernetes auth mount in its Vault namespace bound to
# that Kubernetes namespace and service account, a KV v2 mount with exactly the
# credentials it uses, a read-only policy, and the VSO manifests.
variable "services" {
  description = <<-EOT
    Workloads that get their own Vault + Kubernetes namespace.
    service_account: the workload's SA (default: the name).
    secret_keys:     keys it may read from shared_secrets, plus SUPABASE_PROJECT_REF.
    database:        "role" = its own least-privilege Postgres role ryft_<name> (migration 0009),
                     "owner" = the schema owner and every role's password (the migrate job), "none".
    nats:            "client" = its own NATS user, "server" = the NATS server (bcrypt hashes), "none".
    workload_kind:   what the operator restarts when the secret changes ("" for a Job).
    data_key:        a generated DATA_ENCRYPTION_KEY (32 random bytes, base64) for data the
                     service stores encrypted (the api: Sign in with Apple refresh tokens).
    admin_key:       a generated ADMIN_API_KEY, the bearer of the operator's /v1/admin routes
                     (the api: feature flags); read it with `terraform output -raw admin_api_key`.
    k8s_namespace:   the Kubernetes namespace (default <project>-<name>).
    pod_security:    the namespace's Pod Security level: restricted (default), baseline, privileged.
    workload_name:   the workload the operator restarts (default the service name).
    extra_restarts:  further workloads it restarts, [{kind, name}] (the collector's
                     Deployment next to its DaemonSet).
  EOT
  type = map(object({
    service_account = optional(string)
    secret_keys     = optional(list(string), [])
    database        = optional(string, "none")
    nats            = optional(string, "none")
    workload_kind   = optional(string, "Deployment")
    data_key        = optional(bool, false)
    admin_key       = optional(bool, false)
    k8s_namespace   = optional(string)
    pod_security    = optional(string, "restricted")
    workload_name   = optional(string)
    extra_restarts  = optional(list(object({ kind = string, name = string })), [])
  }))
  default = {
    ingest  = { secret_keys = ["SUPABASE_PROJECT_REF", "BETA_GATE"], database = "role", nats = "client" }
    scoring = { database = "role", nats = "client" }
    api     = { secret_keys = ["SUPABASE_PROJECT_REF", "BETA_GATE"], database = "role", nats = "client", data_key = true, admin_key = true }
    # Push (M7): its own role and NATS user; the APNs key from service_secrets.notify.
    notify  = { database = "role", nats = "client" }
    nats    = { nats = "server", workload_kind = "StatefulSet" }
    migrate = { database = "owner", workload_kind = "" }
    # Grafana Cloud's k8s-monitoring collector (deploy/monitoring): node metrics,
    # Beyla and an SDK injector need root, host network and host files, so its
    # namespace cannot be restricted (D51). Only GC_TOKEN, from service_secrets.
    # Two collectors read the token (as Grafana Cloud's onboarding names them).
    monitoring = {
      k8s_namespace  = "monitoring", pod_security = "privileged",
      workload_kind  = "DaemonSet", workload_name = "grafana-cloud-alloy-daemonset",
      extra_restarts = [{ kind = "Deployment", name = "grafana-cloud-alloy-deployment" }]
    }
  }

  validation {
    condition = alltrue([for s in values(var.services) :
      contains(["none", "role", "owner"], s.database) && contains(["none", "client", "server"], s.nats) &&
      contains(["Deployment", "StatefulSet", "DaemonSet", ""], s.workload_kind) &&
      alltrue([for r in s.extra_restarts : contains(["Deployment", "StatefulSet", "DaemonSet"], r.kind)]) &&
    contains(["restricted", "baseline", "privileged"], s.pod_security)])
    error_message = "database must be none|role|owner, nats none|client|server, workload_kind Deployment|StatefulSet|DaemonSet or \"\", pod_security restricted|baseline|privileged."
  }
  validation {
    condition     = length([for s in values(var.services) : s if s.database == "owner"]) <= 1 && length([for s in values(var.services) : s if s.nats == "server"]) <= 1
    error_message = "At most one service may hold the database owner, and at most one may be the NATS server."
  }
}

variable "supabase_project_ref" {
  description = "Supabase project reference (the <ref> in https://<ref>.supabase.co); also the suffix of every pooler user name"
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]{10,40}$", var.supabase_project_ref))
    error_message = "supabase_project_ref must be the project reference (Supabase → Project Settings → General), 10-40 lowercase letters and digits."
  }
}

variable "database" {
  description = "Supavisor session pooler (Supabase → Connect → Session pooler). Port 5432 = session mode; the services need it."
  type = object({
    host    = string
    port    = optional(number, 5432)
    name    = optional(string, "postgres")
    owner   = optional(string, "postgres")
    sslmode = optional(string, "require")
  })

  validation {
    condition     = endswith(var.database.host, ".pooler.supabase.com")
    error_message = "database.host must be the pooler host (aws-<n>-<region>.pooler.supabase.com); the direct db.<ref>.supabase.co host is IPv6-only."
  }
  validation {
    condition     = var.database.port == 5432
    error_message = "Use the session pooler on port 5432: migrations take advisory locks and pgx uses prepared statements (transaction mode, 6543, breaks both)."
  }
}

variable "demo_email" {
  description = "Email of the App Review demo account (scripts/seed_demo.sh); its password is generated into operator/demo"
  type        = string
  default     = "review@ryftapp.de"

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[a-z]{2,}$", var.demo_email))
    error_message = "demo_email must be an email address."
  }
}

variable "database_owner_password" {
  description = "Password of the schema owner (the Supabase database password). Only the migrate namespace receives it."
  type        = string
  sensitive   = true

  validation {
    condition     = length(var.database_owner_password) >= 12 && !strcontains(var.database_owner_password, " ")
    error_message = "database_owner_password must be the project's database password (at least 12 characters, no spaces)."
  }
}

# Further credentials shared by several services. Written per service into that
# service's own KV, filtered by `secret_keys`, so no namespace can read another's set.
variable "shared_secrets" {
  description = "Shared credentials by key (optional; the database and NATS credentials are generated)"
  type        = map(string)
  sensitive   = true
  default     = {}
}

# Credentials that belong to exactly one service, e.g. {api = {SUPABASE_SERVICE_ROLE_KEY = "sb_secret_…"}}.
variable "service_secrets" {
  description = "Per-service credentials, merged over everything else"
  type        = map(map(string))
  sensitive   = true
  default     = {}

  # Placeholders from terraform.tfvars.example would reach the pods and fail only at
  # startup: the api refuses to start with a malformed Apple key.
  validation {
    condition = alltrue(flatten([for svc, kv in var.service_secrets : [for k, v in kv :
    !strcontains(v, "…") && !can(regex("^<[^>]*>$", trimspace(v)))]]))
    error_message = "service_secrets still holds a placeholder (… or <…>) from terraform.tfvars.example."
  }
  validation {
    condition = contains([0, 4], length(setintersection(keys(lookup(var.service_secrets, "api", {})),
    ["APPLE_TEAM_ID", "APPLE_KEY_ID", "APPLE_CLIENT_ID", "APPLE_KEY_P8"])))
    error_message = "service_secrets.api needs all four APPLE_* keys or none of them: the api refuses to start with a partial set."
  }
  validation {
    condition = !contains(keys(lookup(var.service_secrets, "api", {})), "APPLE_KEY_ID") || can(regex(
    "^[A-Z0-9]{10}$", lookup(var.service_secrets, "api", {})["APPLE_KEY_ID"]))
    error_message = "service_secrets.api.APPLE_KEY_ID is the 10-character Key ID of the Sign in with Apple key (Apple Developer → Keys)."
  }
  validation {
    condition = !contains(keys(lookup(var.service_secrets, "api", {})), "APPLE_KEY_P8") || length(regexall(
      "-----BEGIN PRIVATE KEY-----\\s*[A-Za-z0-9+/=\\s]{100,}-----END PRIVATE KEY-----",
      try(base64decode(lookup(var.service_secrets, "api", {})["APPLE_KEY_P8"]), lookup(var.service_secrets, "api", {})["APPLE_KEY_P8"])
    )) > 0
    error_message = "service_secrets.api.APPLE_KEY_P8 must be the whole AuthKey_<key id>.p8 file, as PEM or base64-encoded."
  }
  validation {
    condition = contains([0, 3], length(setintersection(keys(lookup(var.service_secrets, "notify", {})),
    ["APNS_KEY_ID", "APNS_TEAM_ID", "APNS_KEY_P8"])))
    error_message = "service_secrets.notify needs APNS_KEY_ID, APNS_TEAM_ID and APNS_KEY_P8 together or none of them: notify refuses to start with a partial set."
  }
  validation {
    condition = !contains(keys(lookup(var.service_secrets, "notify", {})), "APNS_KEY_ID") || can(regex(
    "^[A-Z0-9]{10}$", lookup(var.service_secrets, "notify", {})["APNS_KEY_ID"]))
    error_message = "service_secrets.notify.APNS_KEY_ID is the 10-character Key ID of the APNs key (Apple Developer → Keys)."
  }
  validation {
    condition = !contains(keys(lookup(var.service_secrets, "notify", {})), "APNS_KEY_P8") || length(regexall(
      "-----BEGIN PRIVATE KEY-----\\s*[A-Za-z0-9+/=\\s]{100,}-----END PRIVATE KEY-----",
      try(base64decode(lookup(var.service_secrets, "notify", {})["APNS_KEY_P8"]), lookup(var.service_secrets, "notify", {})["APNS_KEY_P8"])
    )) > 0
    error_message = "service_secrets.notify.APNS_KEY_P8 must be the whole AuthKey_<key id>.p8 file of the APNs key, as PEM or base64-encoded."
  }
}

variable "token_ttl" {
  description = "TTL of Vault tokens issued to the services (seconds)"
  type        = number
  default     = 3600
}

variable "vso_output_dir" {
  description = "Where the generated Vault Secrets Operator manifests are written (one kustomize dir per service)"
  type        = string
  default     = "../../deploy/k8s/overlays/vault-namespaces"
}
