# RYFT secrets on Vault (Terraform)

One Vault namespace and one Kubernetes namespace per workload, each pair joined by a
dedicated Kubernetes auth mount, plus the workload's own KV v2 and policy. Credentials
are generated here and written only where they are used. Adding a service is one entry
in `services`.

```text
HCP Vault Dedicated (admin/)                    EKS cluster ryft-prod-euc1
admin/ryft/
├── ingest/   auth/kubernetes ◀─TokenReview─▶ ns ryft-ingest   SA ingest  (+ SA vault-auth)
│             kv/config {DATABASE_URL (role ryft_ingest), NATS_USER/PASSWORD, SUPABASE_PROJECT_REF}
├── scoring/  …                               ns ryft-scoring  SA scoring
│             kv/config {DATABASE_URL (role ryft_scoring), NATS_USER/PASSWORD}
├── api/      …                               ns ryft-api      SA api
│             kv/config {DATABASE_URL (role ryft_api), NATS_USER/PASSWORD, SUPABASE_PROJECT_REF,
│                        SUPABASE_SERVICE_ROLE_KEY, ADMIN_API_KEY (generated), DATA_ENCRYPTION_KEY,
│                        APPLE_* once set}
├── notify/   …                               ns ryft-notify   SA notify
│             kv/config {DATABASE_URL (role ryft_notify), NATS_USER/PASSWORD, APNS_* once set}
├── nats/     …                               ns ryft-nats     SA nats
│             kv/config {NATS_PASSWORD_INGEST/SCORING/API/NOTIFY: bcrypt hashes only}
└── migrate/  …                               ns ryft-migrate  SA migrate
              kv/config {DATABASE_URL (the owner), DB_PASSWORD_INGEST/SCORING/API/NOTIFY}
```

| Vault object (per service, inside `admin/ryft/<svc>`) | Kubernetes object (inside `ryft-<svc>`) |
| --- | --- |
| `auth/kubernetes` mount, config = EKS endpoint + CA + reviewer JWT | ServiceAccount `vault-auth` + token Secret, ClusterRoleBinding to `system:auth-delegator` |
| role `<svc>`: bound to SA `<svc>` in ns `ryft-<svc>`, audience `vault`, policy `<svc>` | ServiceAccount `<svc>` (no automounted token; the workloads use it) |
| `kv/` (v2) with secret `config` = only the keys that service uses | VSO `VaultConnection`, `VaultAuth`, `VaultStaticSecret` → Secret `ryft-secrets` (generated into `deploy/k8s/overlays/vault-namespaces/<svc>/`) |
| policy `<svc>`: read `kv/data/config` only | Namespace with Pod Security `restricted` |

A token for `admin/ryft/api` cannot see `admin/ryft/ingest` (different namespace) and
cannot read anything but `kv/config` (policy). Only the operator, requesting a token
for SA `api` in namespace `ryft-api` with audience `vault`, can get one (role binding);
the pods themselves have no service-account token at all.

**What each credential can do:**

- **Postgres:** each service connects as its own least-privilege role (migration 0009,
  `docs/ARCHITECTURE.md` → Database roles). Only `migrate` holds the owner password.
- **NATS:** each service is its own user with the narrowest subjects
  (`deploy/k8s/base/nats/nats.conf`). The NATS namespace holds bcrypt hashes, so
  nothing in it can log in as a service.
- **Generated passwords:** the role and NATS passwords are `random_password`s. Nobody
  types them, and they never appear in `terraform.tfvars`. You provide only the
  Supabase owner password and the Supabase secret key.

## Prerequisites

- Vault Enterprise or HCP Vault Dedicated (namespaces are an Enterprise feature). The
  token in `VAULT_TOKEN` must be able to create namespaces, auth mounts, secret mounts
  and policies under the parent (`vault_parent_namespace_root`, `admin` on HCP).
- The EKS cluster from `../aws` applied, and `aws` and `kubectl` authenticated to it as
  the identity that ran `../aws` (cluster admin):
  `aws eks update-kubeconfig --region eu-central-1 --name ryft-prod-euc1 --alias ryft-prod-euc1`.
- **The EKS API must be reachable from Vault.** Kubernetes auth calls TokenReview on every
  login. `endpoint_public_access_cidrs` in `../aws` (today `0.0.0.0/0`) must admit HCP
  Vault's egress addresses; narrow it only together with them.
- The Vault Secrets Operator on the cluster: the `infra/aws/kubernetes` root installs it
  (`helm_release.vault_secrets_operator`).
- The Supabase project (`docs/SUPABASE.md`): its project ref, session-pooler host,
  database password and secret key.

## Usage

```bash
cd infra/vault
cp terraform.tfvars.example terraform.tfvars      # git-ignored; fill in the Supabase values
export VAULT_TOKEN=…        # HCP portal → Vault cluster → New admin token; the address comes from tfvars
terraform login                                   # once: HCP Terraform (state)
terraform init && terraform test                  # mocked, no credentials needed
terraform plan -out=tfplan && terraform apply tfplan
terraform output                                   # namespaces, roles, key names (never values)
```

The state holds every credential this root writes into Vault in plaintext. It lives in
the HCP Terraform workspace `ryft-prod-euc1-vault` (execution mode Local): access to that
workspace is access to every RYFT secret, so keep its team access to yourself.

Then `make k8s-apply-eks` from the repo root applies the generated operator manifests,
waits for every `ryft-secrets`, and deploys (`docs/OPERATIONS.md` → EKS).

**Rotate:**

- **A generated password:**
  `terraform apply -replace='random_password.db["api"]'` (or `random_password.nats["api"]`).
  The operator updates the Secrets within `refreshAfter` (1 h) and restarts the
  workloads. For a database password, run the migrate job again so Postgres has the new
  one: `make k8s-apply-eks`.
- **The owner password or the Supabase key:** change it in `terraform.tfvars`, then
  `terraform apply`.

**The admin key** (`/v1/admin/flags`, `docs/OPERATIONS.md` → Feature flags):
`terraform output -raw admin_api_key`; rotate with
`terraform apply -replace='random_password.admin_key["api"]'`.

**The APNs key** (notify, `docs/OPERATIONS.md` → Push): `service_secrets.notify` with
`APNS_KEY_ID`, `APNS_TEAM_ID` and `APNS_KEY_P8`, all three or none (validated). Without
them the plan warns (`check.notify_has_apns_key`) and notify sends nothing.

**Add a service:** add an entry to `services`. With `database = "role"` it also needs a
migration that creates `ryft_<name>` with its grants, and an entry in
`backend/internal/db.ServiceRoles`, so that migrate sets its password.

## Verify a login by hand

```bash
terraform output -json login_check | jq -r .api | sh   # needs the vault CLI and kubectl
kubectl -n ryft-api get vaultauth,vaultstaticsecret,secret ryft-secrets
```
