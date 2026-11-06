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
└── role vault-snapshot ◀── SA vault-snapshot in ns vault only, audience vault, 10-minute service tokens, no default policy
    policy snapshot: read sys/storage/raft/snapshot, nothing else (restore is break-glass, admin)
kv-<svc>/config   the same values as kv/config in admin/ryft/<svc> (same locals, same random_*)
kv-operator/demo  the App Review demo password (no workload policy names kv-operator)
```

The snapshot CronJob (`charts/vault-extras`, #153) logs in as `vault-snapshot`. Its token
holds `snapshot` alone, without `default`, so it can neither look itself up nor revoke itself:
the job lets it expire (10 minutes at most). Both names
are reserved: no service may be called `vault-snapshot` or `snapshot`. The job can succeed only
after PR 3b's first write of this Vault and the apply of #153, in that order.

| HCP (`admin/ryft/<svc>`) | CE (the Vault in the cluster) |
| --- | --- |
| namespace `admin/ryft/<svc>` | none: the mount, the role and the policy are the boundary |
| `auth/kubernetes` per namespace, EKS endpoint + CA + reviewer JWT | one `auth/kubernetes`, `https://kubernetes.default.svc`, Vault's own pod token |
| role `<svc>` in its namespace | role `<svc>`: SA `<svc>` × ns `ryft-<svc>`, audience `vault`, the same TTLs |
| `kv/config` | `kv-<svc>/config` |
| `operator/demo` in `admin/ryft` | `kv-operator/demo` |
| VaultConnection to the HCP address | VaultConnection `https://vault.vault.svc:8200`, `caCertSecretRef: vault-ca`, sync waves |

**The switches** are two locals in `targets.tf`, changed only by a reviewed commit (never
by a variable, D191), and **one switch per PR**: never `write_k8s` and `vso_on_k8s` in the
same one.

- `committed_write_k8s`: when `true`, also write every object above into the Vault in the
  cluster. This needs the bootstrap first (`docs/OPERATIONS.md` → Vault → Bootstrap) and
  the port-forward on every plan and apply after that. Default `false`: the plan makes no
  request to that Vault and needs no port-forward (`tests/k8s_offline.tftest.hcl`), and
  plans nothing there (`tests/vault.tftest.hcl`).
- `committed_vso_on_k8s`: the services whose overlay points at the Vault in the cluster.
  It needs `write_k8s` (a precondition). One namespace per PR.

`test_override_targets` exists for `terraform test` only. A check warns if it is ever set.
(It cannot fail the plan instead: a configuration cannot tell that `terraform test` runs it.)

Every overlay asks cert-manager for the Secret `vault-ca` (a `Certificate`, ClusterIssuer
`ryft-vault`, P13), on either target. The literal `vault_ca_bundle_b64` Secret is retired:
it would fight the Certificate for the same Secret.

**Prerequisites for `write_k8s`:**

- The Vault add-on applied and bootstrapped (`docs/OPERATIONS.md` → Vault: gates G1–G4
  first). The bootstrap writes the policies and the token role in `bootstrap/` by hand, not
  Terraform.
- The CA file: `kubectl --context ryft-prod-euc1 -n vault get secret vault-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > ~/.config/ryft/vault-ca.pem`.
- A port-forward in a second terminal:
  `kubectl --context ryft-prod-euc1 -n vault port-forward svc/vault-active 8200:8200`.
- A token of the token role `terraform` (1 hour, not renewable, usable from `127.0.0.1`
  only), minted from your OIDC login (the runbook): `export TF_VAR_vault_k8s_token=…`. The
  variable is ephemeral, so it is in neither plan nor state.

**What that token is.** Admin-equivalent for its hour, not a sandbox: it reads every
credential (the provider reads each secret back), and it rewrites the policy of any role
this root manages (the services' and the snapshot job's), which a Kubernetes token for that
role's service account turns into those rights. The policy (`bootstrap/terraform.hcl`) takes
away the cheap paths around that: exact paths only (no `ADMIN`, `Terraform` or `default `
spellings of the protected policies), a role carries only its own policy, namespace and
service account, the auth mount
stays `kubernetes` and in-cluster, no tune may switch audit HMAC off, and nothing on audit,
seal, Raft, generate-root, OIDC, identities or tokens. After every apply that wrote it,
`docs/OPERATIONS.md` → Vault → Terraform through the port-forward has a baseline check.

**Shell rules.** Two shells, never mixed (`docs/OPERATIONS.md` → Vault):

- The **Terraform shell**: `VAULT_TOKEN` is the HCP token, and only the default provider
  reads it. The provider `vault.k8s` takes its token from `TF_VAR_vault_k8s_token`, and its
  address, CA file and TLS verification from its configuration. It still reads
  `VAULT_NAMESPACE`, `VAULT_TLS_SERVER_NAME`, `VAULT_CAPATH`, `VAULT_CACERT_BYTES`, the
  client-certificate variables and `VAULT_MAX_RETRIES` from the environment, and the HCP
  provider reads `VAULT_CACERT` and the rest: none of them may be exported here. No `TF_LOG`
  at `DEBUG` or above while the token is set.
- The **vault shell**: exports `VAULT_ADDR`, `VAULT_CACERT` and `VAULT_TOKEN` for the vault
  CLI against the cluster. Never run Terraform in it.
- Unset `TF_VAR_vault_k8s_token` when you are done (`unset TF_VAR_vault_k8s_token`). It
  expires after an hour anyway.

**Rotation with both targets:** `-replace` on a `random_password` changes the value on
both Vaults in one apply, because both read the same resource. VSO picks it up wherever
the namespace points.

**`bootstrap/`:** the files the bootstrap writes into Vault by hand: `admin.hcl` and
`terraform.hcl` (policies), `token-role-terraform.json` (the token role), and
`oidc-config.json`, `oidc-role-admin.json` and `oidc-role-enroll.json` (the Google
Workspace login). Terraform never writes them: otherwise its token could rewrite its own
policy or reach the operator's login, and the OIDC client secret would sit in state.
`terraform.hcl` is generated from `terraform.hcl.tftpl` for the services (exact paths per
service). A changed service list turns the test red, and the check warns, until the file
matches.

**Render it without an apply.** `output` has the new value only after an apply, so read it from
a plan:
```bash
terraform -chdir=infra/vault plan -out=tfplan
terraform -chdir=infra/vault show -json tfplan | jq -r '.planned_values.outputs.bootstrap_terraform_policy.value' > infra/vault/bootstrap/terraform.hcl
rm infra/vault/tfplan   # the plan holds every credential; never keep or share the JSON
```

**The order** matters, because an apply the token's policy does not cover stops halfway on a
403 (a partial apply):
- **Adding a service:**
  1. commit the regenerated file in the same PR, and merge it;
  2. `vault policy write terraform infra/vault/bootstrap/terraform.hcl` with the admin login;
  3. then the apply.
- **Removing a service:**
  1. the apply first, while the policy still covers the old paths;
  2. then write the narrower policy.

`bootstrap.tf` reads all of them, and
`tests/vault.tftest.hcl` holds them to an exact allowlist: every path, and each block's whole
body byte for byte (capabilities and parameter limits). Nothing may sit outside those blocks
but `#` comment lines: no `/* */` or `//` comment, no JSON, no block the regexes cannot read; `user_claim = "sub"`, no claim mappings, a fresh sign-in and
loopback-bound tokens for the login; the token role's one policy, hour and no renewal. A
change to one is a reviewed commit, and then the same `vault policy write` / `vault write`
by hand.
