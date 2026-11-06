# RYFT secrets on Vault (Terraform)

One Vault namespace and one Kubernetes namespace per workload, each pair joined by a
dedicated Kubernetes auth mount, plus the workload's own KV v2 and policy. Credentials
are generated here and written only where they are used. Adding a service is one entry
in `services`.

**Two targets during the migration (D187, D191).** This root writes HCP Vault Dedicated
(below) and, once `targets.tf` says so, the same values into the Vault in the cluster
(Community Edition, `infra/aws/kubernetes/vault.tf`; section
[The Vault in the cluster](#the-vault-in-the-cluster-community-edition)). Each generated
overlay points its namespace at exactly one of them. Until the decommission both are
written on every apply. HCP is not frozen.

```text
HCP Vault Dedicated (admin/)                    EKS cluster ryft-prod-euc1
admin/ryft/
├── ingest/   auth/kubernetes ◀─TokenReview─▶ ns ryft-ingest   SA ingest  (+ SA vault-auth)
│             kv/config {DATABASE_URL (role ryft_ingest), AMQP_USER/PASSWORD,
│                        SUPABASE_PROJECT_REF}
├── scoring/  …                               ns ryft-scoring  SA scoring
│             kv/config {DATABASE_URL (role ryft_scoring), AMQP_USER/PASSWORD}
├── api/      …                               ns ryft-api      SA api
│             kv/config {DATABASE_URL (role ryft_api), AMQP_USER/PASSWORD,
│                        SUPABASE_PROJECT_REF, SUPABASE_SERVICE_ROLE_KEY, ADMIN_API_KEY (generated),
│                        DATA_ENCRYPTION_KEY, APPLE_* once set}
├── notify/   …                               ns ryft-notify   SA notify
│             kv/config {DATABASE_URL (role ryft_notify), AMQP_USER/PASSWORD,
│                        APNS_* once set}
├── rabbitmq/ …                               ns ryft-rabbitmq SA rabbitmq
│             kv/amqp-user-<svc> {username, password} per client → Secret amqp-user-<svc>
│                        (labelled for the Topology Operator; no kv/config, no ryft-secrets)
├── smoke/    …                               ns ryft-smoke    SA smoke
│             kv/config {SUPABASE_PROJECT_REF, SUPABASE_ANON_KEY, SMOKE_EMAIL/PASSWORD}
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
- **RabbitMQ:** each service is its own user without configure rights, writing only its
  routing keys and reading only its queue (`deploy/rabbitmq/topology`, D99). The
  broker's namespace holds the users' passwords for the Topology Operator; it already
  holds the broker's administrator, so a hash there would protect nothing more.
- **Generated passwords:** the role and RabbitMQ passwords are `random_password`s. Nobody
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

Then `make k8s-apply-eks KUBE_CONTEXT=<context>` from the repo root (break-glass; Argo CD
normally does it) applies the generated operator manifests,
waits for every `ryft-secrets`, and deploys (`docs/OPERATIONS.md` → EKS).

**Rotate:**

- **A generated password:**
  `terraform apply -replace='random_password.db["api"]'` (or `random_password.amqp["api"]`).
  The operator updates the Secrets within `refreshAfter` (1 h) and restarts the
  workloads. For a database password, run the migrate job again so Postgres has the new
  one: `make k8s-apply-eks KUBE_CONTEXT=<context>`.
- **The owner password or the Supabase key:** change it in `terraform.tfvars`, then
  `terraform apply`.

**The admin key** (`/v1/admin/flags`, `docs/OPERATIONS.md` → Feature flags):
`terraform output -raw admin_api_key`; rotate with
`terraform apply -replace='random_password.admin_key["api"]'`.

**The App Review demo password** (`docs/OPERATIONS.md` → App Review demo account):
generated into the operator-only mount `operator/demo` of the project namespace (no
workload policy reaches it); `terraform output -raw demo_password`; rotate with
`terraform apply -replace=random_password.demo`, then `scripts/seed_demo.sh` and App
Store Connect. `demo_email` defaults to `review@ryftapp.de`.

**The APNs key** (notify, `docs/OPERATIONS.md` → Push): `service_secrets.notify` with
`APNS_KEY_ID`, `APNS_TEAM_ID` and `APNS_KEY_P8`, all three or none (validated). Without
them the plan warns (`check.notify_has_apns_key`) and notify sends nothing.

**Add a service:** add an entry to `services`. With `database = "role"` it also needs a
migration that creates `ryft_<name>` with its grants, and an entry in
`backend/internal/db.ServiceRoles`, so that migrate sets its password.

## Verify a login by hand

```bash
terraform output -json login_check | jq -r .api | sh   # needs the vault CLI and kubectl
kubectl --context ryft-prod-euc1 -n ryft-api get vaultauth,vaultstaticsecret,secret ryft-secrets
```

## The Vault in the cluster (Community Edition)

CE has no namespaces. The isolation is one Kubernetes auth mount with a role per service,
a policy per service and a KV mount per service (`k8s.tf`, D187):

```text
Vault CE, namespace vault (raft, 3 pods)          EKS cluster ryft-prod-euc1
auth/kubernetes   (Vault reviews tokens as its own SA; no reviewer JWT)
├── role api      ◀── SA api in ns ryft-api only, audience vault ──▶ VSO in ryft-api
│   policy api:   read kv-api/data/config
├── role rabbitmq     policy rabbitmq: kv-rabbitmq/data/config, kv-rabbitmq/data/amqp-user-*
├── …             one role, one policy and one kv-<svc>/ per entry in services
└── role vault-snapshot  ◀── SA vault-snapshot in ns vault (PR 4's CronJob), policy vault-snapshot
kv-<svc>/config   the same values as kv/config in admin/ryft/<svc> (same locals, same random_*)
kv-operator/demo  the App Review demo password (no workload policy names kv-operator)
```

| HCP (`admin/ryft/<svc>`) | CE (the Vault in the cluster) |
| --- | --- |
| namespace `admin/ryft/<svc>` | none: the mount, the role and the policy are the boundary |
| `auth/kubernetes` per namespace, EKS endpoint + CA + reviewer JWT | one `auth/kubernetes`, `https://kubernetes.default.svc`, Vault's own pod token |
| role `<svc>` in its namespace | role `<svc>`: SA `<svc>` × ns `ryft-<svc>`, audience `vault`, the same TTLs |
| `kv/config` | `kv-<svc>/config` |
| `operator/demo` in `admin/ryft` | `kv-operator/demo` |
| VaultConnection to the HCP address | VaultConnection `https://vault.vault.svc:8200`, `caCertSecretRef: vault-ca`, sync waves |

**The switches** are two locals in `targets.tf`, changed only by a reviewed commit (never
by a variable, D191):

- `committed_write_k8s`: when `true`, also write every object above into the Vault in the
  cluster. This needs the bootstrap first (`docs/OPERATIONS.md` → Vault → Bootstrap) and
  the port-forward on every plan and apply after that. Default `false`: the plan writes
  nothing there and needs no port-forward (`tests/k8s_offline.tftest.hcl`).
- `committed_vso_on_k8s`: the services whose overlay points at the Vault in the cluster.
  It needs `write_k8s` (a precondition). One namespace per PR.

`test_override_targets` exists for `terraform test` only. A check warns if it is ever set.

Every overlay asks cert-manager for the Secret `vault-ca` (a `Certificate`, ClusterIssuer
`ryft-vault`, P13), on either target. The literal `vault_ca_bundle_b64` Secret is retired:
it would fight the Certificate for the same Secret.

**Prerequisites for `write_k8s`:**

- The Vault add-on applied and bootstrapped (`docs/OPERATIONS.md` → Vault: gates G1–G4
  first). The bootstrap writes the policies in `bootstrap/` by hand, not Terraform.
- The CA file: `kubectl --context ryft-prod-euc1 -n vault get secret vault-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > ~/.config/ryft/vault-ca.pem`.
- A port-forward in a second terminal:
  `kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200:8200`.
- A 1-hour token of the policy `terraform`, minted from your OIDC login (the runbook):
  `export TF_VAR_vault_k8s_token=…`. The variable is ephemeral, so it is in neither plan
  nor state.

**Shell rules.** Two Vaults, two tokens, one shell:

- `VAULT_TOKEN` is the HCP token, and only the default provider reads it. The provider
  `vault.k8s` reads only `TF_VAR_vault_k8s_token`.
- Never export `VAULT_CACERT`, `VAULT_NAMESPACE` or `VAULT_SKIP_VERIFY` while running
  Terraform. The HCP provider would read them. For the vault CLI against the cluster, pass
  `-ca-cert=` and `-address=` per command, or set them in a subshell.
- Unset `TF_VAR_vault_k8s_token` when you are done (`unset TF_VAR_vault_k8s_token`). It
  expires after an hour anyway.

**Rotation with both targets:** `-replace` on a `random_password` changes the value on
both Vaults in one apply, because both read the same resource. VSO picks it up wherever
the namespace points.

**`bootstrap/`:** the files the bootstrap writes by hand: `admin.hcl` and `terraform.hcl`
(policies), and `oidc-config.json`, `oidc-role-admin.json` and `oidc-role-enroll.json`
(the Google Workspace login). Terraform never writes them. Otherwise its token could widen
itself or reach the operator's login, and the OIDC client secret would sit in state.
`bootstrap.tf` reads them, though, so every plan checks them (no catch-all path; no
audit, seal, Raft, OIDC or token paths; `deny` on the policies admin, terraform and
default; `user_claim = "sub"` and no claim mappings). `tests/vault.tftest.hcl` pins
their shape. A change to one is a reviewed commit, and then the same
`vault policy write` / `vault write` by hand.
