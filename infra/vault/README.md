# RYFT secrets on Vault (Terraform)

One Kubernetes namespace per workload and, on the Vault in the cluster, one role, one
policy and one KV v2 mount per workload. Credentials are generated here and written only
where they are used. Adding a service is one entry in `services`.

**One Vault (D207).** The only Vault is Vault Community Edition in the cluster (namespace
`vault`, three Raft pods, `infra/aws/kubernetes/vault.tf`; Shamir seal by default, D208).
HCP Vault Dedicated is gone (2026-11-10), and with it the Vault namespaces, the dual write
and the cutover switches: every generated overlay reads the in-cluster Vault.

```text
Vault CE, namespace vault (Raft, 3 pods)          EKS cluster ryft-prod-euc1
auth/kubernetes   (Vault reviews tokens as its own SA; no reviewer JWT, no CA, nothing long-lived)
├── role api      ◀── SA api in ns ryft-api only, audience vault ──▶ VSO in ryft-api
│   policy api:   read kv-api/data/config
├── role rabbitmq     policy rabbitmq: kv-rabbitmq/data/config, kv-rabbitmq/data/amqp-user-*
├── …             one role, one policy and one kv-<svc>/ per entry in services
└── role vault-snapshot ◀── SA vault-snapshot in ns vault only, audience vault, 10-minute service tokens, no default policy
    policy snapshot: read sys/storage/raft/snapshot, nothing else (restore is break-glass, admin)
kv-<svc>/config   only the keys that service uses (below)
kv-rabbitmq/amqp-user-<client>  {username, password} per RabbitMQ client, for the Topology Operator
kv-operator/demo  the App Review demo password (no role's policy names kv-operator)
```

| Service | `kv-<svc>/config` |
| --- | --- |
| ingest | `DATABASE_URL` (role `ryft_ingest`), `AMQP_USER`/`AMQP_PASSWORD`, `SUPABASE_PROJECT_REF` |
| scoring | `DATABASE_URL` (role `ryft_scoring`), `AMQP_USER`/`AMQP_PASSWORD` |
| api | `DATABASE_URL` (role `ryft_api`), `AMQP_*`, `SUPABASE_PROJECT_REF`, `SUPABASE_SERVICE_ROLE_KEY`, `ADMIN_API_KEY` and `DATA_ENCRYPTION_KEY` (generated), `APPLE_*` once set |
| notify | `DATABASE_URL` (role `ryft_notify`), `AMQP_*`, `APNS_*` once set |
| rabbitmq | none: `amqp-user-<client>` only, so no `ryft-secrets` |
| migrate | `DATABASE_URL` (the owner), `DB_PASSWORD_INGEST/SCORING/API/NOTIFY` |
| smoke | `SUPABASE_PROJECT_REF`, `SUPABASE_ANON_KEY`, `SMOKE_EMAIL`/`SMOKE_PASSWORD` |
| monitoring | `GC_TOKEN`, `PROM_USERNAME` |
| the import service | its client id, secret and refresh token |

| Vault object (in-cluster Vault) | Kubernetes object (in the service's namespace) |
| --- | --- |
| one `auth/kubernetes`, `https://kubernetes.default.svc`, Vault's own pod token | Namespace with Pod Security `restricted` (`monitoring`: `privileged`, D51) |
| role `<svc>`: SA `<svc>` × ns `ryft-<svc>`, audience `vault`, policy `<svc>`, 1 h (4 h max) | ServiceAccount `<svc>` (no automounted token; the workloads run as it) |
| `kv-<svc>/` (v2) with secret `config` | VSO `Certificate vault-ca`, `VaultConnection`, `VaultAuth`, `VaultStaticSecret` → Secret `ryft-secrets` (generated into `deploy/k8s/overlays/vault-namespaces/<svc>/`) |
| policy `<svc>`: read `kv-<svc>/data/config` only | |

CE has no namespaces, so the boundary is the role, the policy and the mount (D187): a token
for role `api` reads `kv-api/data/config` and nothing else. Only the operator, requesting a
token for SA `api` in namespace `ryft-api` with audience `vault`, can get one (the role's
binding); the pods themselves have no service-account token at all.

**What each credential can do:**

- **Postgres:** each service connects as its own least-privilege role (migration 0009,
  `docs/ARCHITECTURE.md` → Database roles). Only `migrate` holds the owner password.
- **RabbitMQ:** each service is its own user without configure rights, writing only its
  routing keys and reading only its queue (`deploy/rabbitmq/topology`, D99). The
  broker's mount holds the users' passwords for the Topology Operator; its namespace already
  holds the broker's administrator, so a hash there would protect nothing more.
- **Generated values:** the role and RabbitMQ passwords, the admin key, the data key and the
  demo password are `random_*`. Nobody types them, and they never appear in
  `terraform.tfvars`. You provide only the Supabase values and the per-service keys.

## Prerequisites

- The EKS cluster from `../aws` and its add-ons from `../aws/kubernetes` (the Vault release,
  the Vault Secrets Operator, cert-manager with the ClusterIssuer `ryft-vault`), and `aws`
  and `kubectl` authenticated as the identity that ran `../aws` (cluster admin):
  `aws eks update-kubeconfig --region eu-central-1 --name ryft-prod-euc1 --alias ryft-prod-euc1`.
- The Vault initialised, every pod unsealed and the bootstrap done (`docs/OPERATIONS.md` →
  Vault → Bootstrap): the audit devices, the policies `admin` and `terraform` and the token
  roles `admin` and `terraform` from `bootstrap/`, written by hand.
- The Supabase project (`docs/SUPABASE.md`): its project ref, session-pooler host,
  database password and secret key.

## Usage

Every plan and apply writes the in-cluster Vault, so every one needs the verified
port-forward to the active pod (`docs/OPERATIONS.md` → Vault → A verified pod; never
`svc/vault-active`), the CA file and the one-hour terraform token: run them in the session
shell of `docs/OPERATIONS.md` → Vault → Login, with the commands of → Terraform through the
port-forward. The very first apply after the rebuild imports two values first (→ The first
apply).

```bash
cd infra/vault
cp terraform.tfvars.example terraform.tfvars      # once; git-ignored; fill in the values
terraform login                                   # once: HCP Terraform (state)
terraform init && terraform test                  # mocked, no credentials needed
# in the session shell (port-forward, VAULT_CACERT file, TF_VAR_vault_k8s_token):
terraform plan -out=tfplan
terraform apply tfplan && rm tfplan               # the plan holds every credential
git status --short ../../deploy/k8s/overlays/vault-namespaces   # commit what the apply wrote
```

`terraform test` also reads a local `terraform.tfvars`: one that still sets a retired
variable (below) fails every run, as it fails the plan.

The state holds every credential this root writes into Vault in plaintext. It lives in
the HCP Terraform workspace `ryft-prod-euc1-vault` (execution mode Local): access to that
workspace is access to every RYFT secret, so keep its team access to yourself.

**Retired variables (D207).** `vault_parent_namespace_root`, `vault_address`,
`vault_address_for_cluster` and `vault_ca_bundle_b64` exist only for their validations: a
`terraform.tfvars` (or `TF_VAR_…`) that still sets one fails the plan with the line to
delete. The Vault provider (`providers.tf`, the only one) takes its address, CA file, TLS
verification and token from `vault_k8s_address`, `vault_k8s_ca_cert_file` and
`TF_VAR_vault_k8s_token`, never from `VAULT_ADDR`, `VAULT_CACERT`, `VAULT_SKIP_VERIFY` or
`VAULT_TOKEN`.

**The generated overlays.** `vso.tf` writes `deploy/k8s/overlays/vault-namespaces/<svc>/`
(`vault.yaml`, `kustomization.yaml`) on every apply, and Argo CD applies them (`vault-<svc>`,
wave 0). `tests/vault.tftest.hcl` pins three renders to `tests/golden/k8s/`. The committed
files are still HCP's render until the first apply after the rebuild rewrites them; the
follow-up PR that commits them adds back `the_committed_overlays_are_what_the_apply_writes`,
which pins them to exactly what an apply writes for the default services
(`docs/work/2026-11-13-rebuild/followup-overlays.md`). A change of the template, the
services or a service's key set: apply, then commit the rewritten files in the same PR.
Never edit them by hand.

**Rotate:**

- **A generated password:**
  `terraform apply -replace='random_password.db["api"]'` (or `random_password.amqp["api"]`).
  The operator updates the Secrets within `refreshAfter` (1 h) and restarts the
  workloads. For a database password, run the migrate job again so Postgres has the new
  one (Argo CD's next sync of the workloads, or `make k8s-apply-eks` as break-glass).
- **The owner password or the Supabase key:** change it in `terraform.tfvars`, then
  plan and apply.
- **Never** the data key (`random_bytes.data_key["api"]`): what it sealed no longer opens.

**The admin key** (`/v1/admin/flags`, `docs/OPERATIONS.md` → Feature flags):
`terraform output -raw admin_api_key`; rotate with
`terraform apply -replace='random_password.admin_key["api"]'`.

**The App Review demo password** (`docs/OPERATIONS.md` → App Review demo account):
generated into `kv-operator/demo` (no role's policy reaches it); `terraform output -raw
demo_password`; rotate with `terraform apply -replace=random_password.demo`, then
`scripts/seed_demo.sh` and App Store Connect. `demo_email` defaults to `review@ryftapp.de`.

**The APNs key** (notify, `docs/OPERATIONS.md` → Push): `service_secrets.notify` with
`APNS_KEY_ID`, `APNS_TEAM_ID` and `APNS_KEY_P8`, all three or none (validated). Without
them the plan warns (`check.notify_has_apns_key`) and notify sends nothing.

**Add a service:** add an entry to `services`, render `bootstrap/terraform.hcl` and write
it before the apply (the order below). With `database = "role"` it also needs a migration
that creates `ryft_<name>` with its grants, and an entry in `backend/internal/db.ServiceRoles`,
so that migrate sets its password. `default`, `root`, `admin`, `terraform`, `vault-snapshot`,
`snapshot` and `operator` are reserved names.

## Verify a login by hand

In the session shell (`VAULT_ADDR` and `VAULT_CACERT` of the port-forward):

```bash
terraform output -json login_check | jq -r .api | sh   # prints the token's policies, sorted: [api default]
kubectl --context ryft-prod-euc1 -n ryft-api get vaultauth,vaultstaticsecret,secret ryft-secrets
```

## The snapshot job

The snapshot CronJob (`charts/vault-extras`, #153) logs in as `vault-snapshot`. Its token
holds `snapshot` alone, without `default`, so it can neither look itself up nor revoke itself:
the job lets it expire (10 minutes at most). It can succeed only after the first apply of this
root.

## The terraform token

A token of the token role `terraform` (1 hour, not renewable, usable from `127.0.0.1` only,
a child of the session's admin token, so it dies with it), created in the session
(`docs/OPERATIONS.md` → Vault → Login): `export TF_VAR_vault_k8s_token=…`. The variable is
ephemeral, so it is in neither plan nor state, and an apply needs it again.

**What that token is.** Admin-equivalent for its hour, not a sandbox: it reads every
credential (the provider reads each secret back), and it rewrites the policy of any role
this root manages (the services' and the snapshot job's), which a Kubernetes token for that
role's service account turns into those rights. The policy (`bootstrap/terraform.hcl`) takes
away the cheap paths around that: exact paths only (no `ADMIN`, `Terraform` or `default `
spellings of the protected policies), a role carries only its own policy, namespace and
service account, the auth mount stays `kubernetes` and in-cluster, no tune may switch audit
HMAC off, and nothing on audit, seal, Raft, generate-root, OIDC, identities or tokens. After
every apply, `docs/OPERATIONS.md` → Vault → Terraform through the port-forward has a baseline
check.

**Shell rules** (`docs/OPERATIONS.md` → Vault): one session shell, a standalone terminal
(never an editor's or an agent's), history off. The provider still reads `VAULT_NAMESPACE`,
`VAULT_TLS_SERVER_NAME`, `VAULT_CAPATH`, `VAULT_CACERT_BYTES`, the client-certificate
variables and `VAULT_MAX_RETRIES` from the environment: none of them may be exported. No
`TF_LOG` at `DEBUG` or above while the token is set. End the session by revoking the admin
token (it takes the terraform token with it) and `unset TF_VAR_vault_k8s_token VAULT_TOKEN`.

## `bootstrap/`

The files the bootstrap writes into Vault by hand, never Terraform (otherwise its token could
rewrite its own policy or mint an admin token):

- `admin.hcl`, `terraform.hcl`: the policies `admin` and `terraform`. `admin` is everything
  except what would outlive its session: it denies `auth/token/create`,
  `auth/token/create-orphan`, every `auth/token/create/<role>` but `terraform`, the token
  roles and `sys/auth/*`, so an admin token mints the terraform token (its child) and no other
  token, role or auth method.
- `token-role-admin.json`: the token role `admin` (policy `admin`, never `root`; an orphan, so
  revoking the root token leaves it; 1 hour, not renewable, `127.0.0.1` only). The session's
  root token, from `vault operator generate-root`, creates one such token and is revoked
  (D209).
- `token-role-terraform.json`: the token role `terraform` (above).
- `oidc-config.json`, `oidc-role-admin.json`, `oidc-role-enroll.json`: **a documented later
  option, written nowhere.** They are the Google Workspace version (`hd = ryftapp.de`), and
  Google Workspace is gone. A login with a personal Google account has no `hd` claim, so it
  needs a reviewed change first (a binding to the account's `sub`, passed at the setup and
  never committed, and the tests' allowlist with it). Until then the login is the
  generate-root session.

`terraform.hcl` is generated from `terraform.hcl.tftpl` for the services (exact paths per
service). A changed service list turns the test red, and the check warns, until the file
matches.

**Render it without an apply.** `output` has the new value only after an apply, so read it from
a plan (in the session shell):
```bash
terraform -chdir=infra/vault plan -out=tfplan
terraform -chdir=infra/vault show -json tfplan | jq -r '.planned_values.outputs.bootstrap_terraform_policy.value' > infra/vault/bootstrap/terraform.hcl
rm infra/vault/tfplan   # the plan holds every credential; never keep or share the JSON
```

**The order** matters, because an apply the token's policy does not cover stops halfway on a
403 (a partial apply):
- **Adding a service:**
  1. commit the regenerated file in the same PR, and merge it;
  2. `vault policy write terraform infra/vault/bootstrap/terraform.hcl` with the root token of
     a generate-root session, before it mints the admin token (the admin policy writes no
     policy);
  3. then the apply.
- **Removing a service:**
  1. the apply first, while the policy still covers the old paths;
  2. then write the narrower policy.

`bootstrap.tf` reads all of them, and `tests/vault.tftest.hcl` holds them to an exact
allowlist: every path, and each block's whole body byte for byte (capabilities and parameter
limits). Nothing may sit outside those blocks but `#` comment lines: no `/* */` or `//`
comment, no JSON, no block the regexes cannot read. Both token roles are pinned key for key.
A change to one is a reviewed commit, and then the same `vault policy write` /
`vault write auth/token/roles/<name> @…` by hand, with the root token of a generate-root
session (the admin policy writes no policy and no token role).
