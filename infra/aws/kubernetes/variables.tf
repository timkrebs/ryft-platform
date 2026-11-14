variable "acme_email" {
  description = "Email for the Let's Encrypt account (expiry and policy notices)"
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$", var.acme_email))
    error_message = "acme_email must be an email address."
  }
}

variable "cluster_name" {
  description = "Name of the EKS cluster created by infra/aws"
  type        = string
  default     = "ryft-prod-euc1"
}

variable "domain" {
  description = "Route 53 zone created by infra/aws/dns; Ingress hosts live under it"
  type        = string
  default     = "ryftapp.de"
}

variable "region" {
  description = "AWS region of the cluster"
  type        = string
  default     = "eu-central-1"
}

variable "vpc_cidr" {
  description = "The VPC's CIDR (infra/aws vpc_cidr): node addresses, from which the host-network metrics collectors scrape"
  type        = string
  default     = "10.0.0.0/16"

  validation {
    condition     = can(cidrhost(var.vpc_cidr, 0))
    error_message = "vpc_cidr must be a CIDR block."
  }
}

variable "tags" {
  description = "Extra tags for the IAM roles"
  type        = map(string)
  default     = {}
}

variable "admission_break_glass" {
  description = "Break-glass for the admission policies (D120): every binding gets Warn+Audit whatever admission-values.yaml says. Planned use: apply -target=helm_release.admission_policy with -var='admission_break_glass=true'; leave it false otherwise."
  type        = bool
  default     = false
}

variable "admission_actions" {
  description = "Overrides the action of single bindings over admission-values.yaml for one plan (binding => audit | deny). A step of the rollout is a reviewed change of the file instead."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for v in values(var.admission_actions) : contains(["audit", "deny"], v)])
    error_message = "Each admission action must be audit (Warn+Audit) or deny (Deny+Audit)."
  }

  validation {
    condition     = alltrue([for k in keys(var.admission_actions) : contains(keys(yamldecode(file("${path.module}/charts/admission-policy/values.yaml")).actions), k)])
    error_message = "admission_actions names a binding the admission-policy chart does not have."
  }
}

variable "admission_scoped_rbac_file" {
  description = "The file whose presence lets operators-cluster go to deny: T12-g's deploy/rabbitmq/operators/cluster-operator/scoped-rbac.yaml. null uses that path; a test points it elsewhere."
  type        = string
  default     = null
}

variable "admission_vault_namespaces_dir" {
  description = "The directory of the generated */vault.yaml whose VaultConnection addresses check.admission_vault_address compares with vaultAddresses: deploy/k8s/overlays/vault-namespaces. null uses that path; a test points it elsewhere."
  type        = string
  default     = null
}

variable "vault_seal" {
  description = "How Vault's root key is sealed (D208). shamir (default): no seal stanza, so Vault's own Shamir seal; the operator's init splits the unseal key into shares, and every Vault pod that starts stays sealed until it is unsealed by hand; no AWS identity and no egress out of the cluster for the server pods. awskms: auto-unseal with infra/aws's key alias/<cluster>-vault-unseal through the Pod Identity role of vault/vault (D189), and the server pods' egress to KMS. Changing it on an initialised Vault is a seal migration (docs/OPERATIONS.md -> Vault): apply, then restart and `vault operator unseal -migrate` pod by pod."
  type        = string
  default     = "shamir"

  validation {
    condition     = contains(["shamir", "awskms"], var.vault_seal)
    error_message = "vault_seal must be shamir or awskms."
  }
}

variable "vault_collector_peer" {
  description = "Admits the monitoring collector (alloy-deployment) to Vault's port 8200 for the metrics scrape (charts/vault-extras peers.collector, D194). Leave false until the operator's own init is done, the root token is revoked and the hard gates G1-G4 hold; set it false again before a restore into a fresh cluster (OPERATIONS -> Vault -> Disaster recovery)."
  type        = bool
  default     = false
}

variable "admission_enabled" {
  description = "Overrides `enabled` of admission-values.yaml for one plan (tests, a dry look at the release). null uses the file; the real switch is the reviewed file, so do not run an apply with this set."
  type        = bool
  default     = null
}
