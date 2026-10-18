# RYFT platform — status

State of the v1 platform after M1 (Supabase) and the zero-trust layout (2026-10-18). Contract:
[`API.md`](API.md). Design: [`ARCHITECTURE.md`](ARCHITECTURE.md). Judgement calls and
deviations from the brief: [`DECISIONS.md`](DECISIONS.md).

## Done

| Item | What | Where |
| --- | --- | --- |
| 2.1 Readiness | `readiness` (0.6·recovery + 0.4·sleep, overreach penalty ≤ 10) and `state` on `DailyScore`, stored (migration 0002) | `scoring.Readiness`, `ReadinessState` |
| 2.2 Sleep detail | Typed `DayDetail.sleep` with stages (core → light), %, efficiency, quality + label, hypnogram; `GET /v1/sleep?range=week\|month` | `scoring/sleep.go`, migration 0003 |
| 2.3 Workout detail | `GET /v1/workouts/{id}`: HR curve (LTTB ≤ 600 points), zone minutes, TRIMP, strain, avg/max HR; zones + TRIMP stored at scoring time; `DayDetail.strain` | `scoring/workout.go`, migration 0004 |
| 2.4 Trends | `GET /v1/trends?metric&range` with avg/min/max and stored 30-day HRV/RHR baselines | `store.Trend`, migration 0005 |
| 2.5 Insights | Pure rule engine (7 rules, 30-day history), `GET /v1/insights`, 5-minute cache | `internal/insights` |
| 2.6 Observability | Prometheus `/metrics` on all services, scrape annotations, dashboard panel list and alerts | `internal/metrics`, [`OBSERVABILITY.md`](OBSERVABILITY.md) |
| 2.7 Hardening | `base/` + default and Vault overlays (VaultStaticSecret), default-deny NetworkPolicy, PDBs, CPU HPAs, nightly `pg_dump` CronJob with documented restore | `deploy/k8s`, [`OPERATIONS.md`](OPERATIONS.md) |
| 2.8 CI | vet, race tests, golangci-lint, manifest validation, smoke on Docker; multi-arch images to `ghcr.io/timkrebs/ryft/<service>` (short SHA, `latest` on main) | `.github/workflows/ci.yml`, `.golangci.yml` |
| 2.9 API docs | Full v1 contract with screen map, error semantics, Swift mirror, changelog | [`API.md`](API.md) |

**Infrastructure (after the brief):**

| Item | What | Where |
| --- | --- | --- |
| EKS cluster | VPC + EKS `ryft-prod-euc1` (Terraform modules; renamed from `ryft-prod`), applied by the user | [`infra/aws`](../infra/aws/README.md) |
| Domain `ryftapp.de` | Route 53 zone (own root, long-lived) with CAA for Let's Encrypt; GoDaddy stays the registrar | `infra/aws/dns` |
| Edge | AWS Load Balancer Controller, Traefik behind an NLB (HTTPS only), cert-manager with Let's Encrypt DNS-01 issuers, ExternalDNS, encrypted gp3 default StorageClass; all Terraform + Helm with Pod Identity | `infra/aws/kubernetes` |
| EKS overlay | `api.ryftapp.de` Ingress (Traefik + cert-manager), encrypted storage class on every volume, image tags to pin | `deploy/k8s/overlays/eks` |

**M1 — Supabase, multi-user, platform hardening** (`CLAUDE_FOLLOWUP_SUPABASE.md` plus the
agreed extras):

| Item | What | Where |
| --- | --- | --- |
| Auth | Supabase access tokens verified against the project JWKS (ES256/RS256, issuer, audience, role, not anonymous). `401` only for bad tokens, `503` + `Retry-After` when keys or the database are unreachable. apikey mode for local dev only, behind an explicit flag. | `internal/auth`, [`SUPABASE.md`](SUPABASE.md), D28–D30 |
| Multi-user | Users created on first request. Every query scoped to the token's user. Composite keys for devices and workouts. Per-user insight cache. Scoring timer over active users. Recompute gated per user. | migration 0007, `store.EnsureUser`, D31 |
| Supabase storage | Session pooler, small pools, transaction-level migration locks. RLS and revokes so the Data API exposes nothing. `local-postgres` overlay. Egress NetworkPolicy. | `internal/db`, `deploy/k8s`, D33–D34 |
| Logging | JSON logs with `request_id`, `user_id` and `route` on every line. The ID travels through the event into scoring. `LOG_LEVEL`. | `internal/logx`, [`OBSERVABILITY.md`](OBSERVABILITY.md#logs), D35 |
| Middleware | Request ID → access log + metrics → recover → security headers. On `/v1`: auth → per-user rate limit → user lookup → timeout. Generic `500` bodies, `413` body limits. | `internal/httpx`, `internal/ratelimit`, D36–D38 |
| Account deletion | `DELETE /v1/me`: all rows in one transaction plus a tombstone, then the Supabase auth user. Races with syncs and token reuse handled by locks. Reconciler for failed Supabase calls and restores. | `cmd/api/account.go`, `auth.Admin`, D39 |
| Source de-dup | One source per hour for heart rate, steps and active energy; one source per night for sleep. | `scoring/sources.go`, [`SCORING.md`](SCORING.md), D40 |
| Steps and kcal | Daily totals on `DailyScore` and `DayDetail.strain`, trend metrics. | migration 0008, D41 |
| CI | The test job runs the Postgres integration tests (migrations, deletion races) against a service container. | `.github/workflows/ci.yml` |

**Zero trust: one identity per workload** ([`ARCHITECTURE.md`](ARCHITECTURE.md#namespaces-and-trust)):

| Item | What | Where |
| --- | --- | --- |
| Database roles | `ryft_ingest` / `ryft_scoring` / `ryft_api` with only the privileges their queries use. Migrations run only in the migrate job, as the owner. Passwords are set as SCRAM verifiers. | migration 0009, `cmd/migrate`, D42 |
| NATS users | One user per service. Publishers may only publish `ryft.ingested`; scoring alone manages and consumes the stream. Each service has its own reply inbox. | `deploy/k8s/base/nats/nats.conf`, D43 |
| Namespaces | `ryft-{ingest,scoring,api,nats,migrate}`, each with default deny in and out, explicit flows, internet egress only to public addresses, Pod Security `restricted`, and no service-account tokens in pods. | `deploy/k8s`, D44 |
| Vault | HCP namespace paths fixed. Credentials are generated and written only where used. A migrate namespace, the api's Supabase key, and a mocked `terraform test` with golden operator manifests. | `infra/vault` (yours, uncommitted), D45 |
| Operator | Vault Secrets Operator 1.6.0 as a pinned add-on, with no default identity. | `infra/aws/kubernetes`, D46 |

Also: Podman support in the Makefile, JSON errors on unknown routes, `422` for inverted or
oversized ranges, UTC timestamps everywhere, `DailyScore.baseline_days`, end-to-end
contract checks in `make smoke`, Swift-side contract checks (`make check-swift`).

## How it was verified

| Check | Result |
| --- | --- |
| `make test` and `go test -race ./...` | pass (scoring, insights, auth incl. a fake JWKS, httpx middleware, rate limiter, logx, api cache) |
| Postgres integration tests (`RYFT_TEST_POSTGRES_URL`, throwaway `postgres:17`) | pass: migrations on plain Postgres and on a faked Supabase shape (RLS, revokes), account deletion including both races; each race test fails with its lock removed |
| `golangci-lint run` (v2.15, `.golangci.yml`) | 0 issues |
| `make down && make up && make smoke` on Podman, fresh volumes (six migrations at the time) | pass, incl. `scripts/check_api.py`: every endpoint, documented invariants, error paths, a workout across midnight |
| `make up && make smoke` on the existing volume after M1 (migrations 0007 and 0008 over real data) | pass, incl. two-user isolation on a shared device and workout UUID, request IDs and security headers, `413`s, account deletion (data gone, `401` afterwards on api and ingest, repeatable), overlapping heart-rate, sleep and step sources; no warnings or errors in the service logs |
| `make check-swift` | the Swift mirror in `API.md` decodes every endpoint; the app's current models (`../app`) decode every endpoint they call |
| `make k8s-validate` | base, Vault, EKS and local-postgres overlays render and validate with kubeconform (103 resources; VSO CRD schemas included) |
| Terraform roots `infra/aws`, `dns`, `kubernetes` | `fmt`, `validate`, `terraform test` (mocked: 8 + 2 + 7 runs), tflint clean, trivy: no findings in `dns`/`kubernetes` |
| `infra/vault` | `validate`, tflint clean, `terraform test` (mocked, 6 runs): keys per namespace, owner password only in migrate, bcrypt-only NATS, role bindings, policy, Pod Security, golden operator manifests (fails with the old namespace path) |
| Zero-trust layout locally | compose runs migrate, then the services as `ryft_*` roles and their NATS users. `make smoke` passes on the existing volume, with migration 0009 applied over the data. The grant matrix, SCRAM login and RLS on the Supabase shape are tested against Postgres 17. The NATS permission tests pass against compose and with bcrypt hashes, and fail against a permissive config. |
| `make k8s-validate` (per-namespace) | each `services/<svc>`, `overlays/eks`, `overlays/local-postgres` and the golden operator manifests (140 resources, VSO 1.6 CRD schemas). Every rendered pod checked for the restricted profile; each namespace's Secret holds only its own keys. NATS ran as uid 1000 on a read-only root, and Postgres as uid 70 with all capabilities dropped (Podman). |
| Helm charts of the edge | each pinned chart rendered with the exact values (`helm template`), schemas accepted, output validated with kubeconform incl. cert-manager CRD schemas; Traefik Service exposes only 443 |
| Backup script + restore procedure | run against the compose Postgres: dump as uid 70 with read-only root FS, `pg_restore --clean --if-exists` into empty and populated databases, identical row counts |
| Dockerfile cross-build | `linux/amd64` image built on arm64 and started |
| `actionlint .github/workflows/ci.yml` | pass |
| Independent review of the full diff | nothing that crashes, loses data or breaks a deploy; four wrong-result bugs found and fixed (`DECISIONS.md` D22), one with a new end-to-end check |

## Not done / not verified

- **CI has not run on GitHub.** This repo has no remote yet; the workflow is linted, not
  executed. Action major versions (`checkout@v5`, `setup-go@v6`, `golangci-lint-action@v8`)
  should be confirmed on the first run.
- **Only the cluster root is applied** (by the user). `infra/aws/dns` and
  `infra/aws/kubernetes` are verified offline, not applied; the nameserver switch at
  GoDaddy is a manual step; the RYFT overlay waits for M1 and the first CI images.
  The cluster root's next plan also carries the EBS add-on change (no default
  StorageClass from the add-on) and, without a `terraform.tfvars`, still leaves the
  Kubernetes API open to `0.0.0.0/0`.
- **Not on EKS yet:** Prometheus (`monitoring` namespace, needs a third node), content on
  the apex `ryftapp.de` / `www`, DNSSEC.
- **Untested against the real services:** `infra/vault` against HCP Vault, the operator
  against the cluster, and Supavisor accepting the custom roles (`ryft_api.<ref>`). The
  first `make k8s-apply-eks` proves all three; `infra/vault` is only checked with mocked
  providers.
- **Failure-path metrics** (`ryft_events_handled_total{result="retry|failed"}`,
  `ryft_nats_redeliveries_total`) are wired but were not exercised end to end.
- **No live Supabase project yet.** The Supabase paths are verified against a fake JWKS
  and a faked Supabase database shape. `make smoke-supabase` skips without
  `SUPABASE_*`. Before go-live: work through the `SUPABASE.md` checklist, then run
  `make smoke-supabase` against the stack and the RLS check queries in `SUPABASE.md`.
- **Apple token revocation on account deletion is not implemented.** Apple asks for it
  for Sign in with Apple. It needs the sign-in's authorization code and a server-side
  client secret (`SUPABASE.md` → Account deletion).
- **Logs are not shipped anywhere** (stdout only, lost on pod restart). They carry
  daily scores, so treat any log store as health data.
- **Rate limits are per pod** (effective limit up to replicas × limit). There is no
  per-IP limit for unauthenticated traffic; that belongs on the Traefik edge if needed
  (D36).
- **Rescore after deploying M1.** De-duplication changes strain and sleep for users with
  two devices, and `steps`/`active_kcal` are `null` on days scored before. Run
  `POST /v1/recompute` over the history (one per user per minute).
- Insight texts are English only; there are no Go handler tests against a database
  (handlers are covered by the smoke contract checks).
- **For the iOS side** (nothing in `../app` was changed):
  - Decode `User.email`/`provider`, `DailyScore.steps`/`active_kcal`,
    `DayStrain.steps`/`active_kcal`, and `APIErrorBody.requestId`. All are optional, and
    the current models still decode.
  - Status handling: treat `503` as "retry after `Retry-After`", never as sign-out. Honour
    `429` + `Retry-After`. `413` means a smaller sync page. Only a second `401` after a
    refresh signs out.
  - Delete account: call `DELETE /v1/me`, then sign out and clear caches (Apple
    revocation above).
  - New users start in `Europe/Berlin`: send the device's zone with `PATCH /v1/me` on
    first launch.
  - Show steps and kcal on the Strain screen.
  - `SleepMath.percent` treats values ≤ 1 as fractions, so a 1 % awake share renders as
    100 %. The API sends whole percentages (`API.md`, Conventions).
  - The app also reads `details` keys. They are frozen, but the typed fields
    (`baseline_days`, `DayDetail.sleep`, `DayDetail.strain`) are the contract.

## Alpha readiness

The code, manifests and Terraform are ready and verified offline and on the local
stack. Nothing runs on the cluster yet. A first alpha (TestFlight, a few people) needs
these steps, in order. All of them are yours, because each needs an account or
credentials:

| # | Step | Why it blocks |
| --- | --- | --- |
| 1 | **Supabase project** per [`SUPABASE.md`](SUPABASE.md) (Frankfurt, asymmetric JWT keys, Apple provider, Data API off). Use a paid plan for real users: free projects pause after a week of inactivity and keep no backups. | no database, no sign-in |
| 2 | **GitHub remote + first CI run.** Images `ghcr.io/timkrebs/ryft/{ingest,scoring,api,migrate}:<sha>`. Make the packages public, or add an `imagePullSecret` per namespace. | nothing to pull |
| 3 | **`infra/aws/dns`**, then the GoDaddy nameserver switch | no certificate, no DNS |
| 4 | **`infra/aws/kubernetes`** (Traefik, cert-manager, ExternalDNS, gp3-encrypted, now the Vault Secrets Operator) | no ingress, no secrets sync |
| 5 | **`infra/vault`**: `terraform.tfvars` has the old shape and placeholders. Copy the new `terraform.tfvars.example`: `supabase_project_ref`, `database.host`, `database_owner_password`, `service_secrets.api.SUPABASE_SERVICE_ROLE_KEY`. Then `terraform test`, plan, apply. | no credentials anywhere |
| 6 | **kubectl context**: the current one points at the old cluster ARN `…/ryft-prod`. Run `aws eks update-kubeconfig --region eu-central-1 --name ryft-prod-euc1 --alias ryft-prod-euc1`. | Terraform's kubernetes provider uses its own token, but `make k8s-apply-eks` uses kubectl |
| 7 | Pin `newTag` in `deploy/k8s/overlays/eks/kustomization.yaml`, then `make k8s-apply-eks` | — |
| 8 | **Verify:** `curl -sI https://api.ryftapp.de/v1/me` → `401`; `make smoke-supabase` through port-forwards with the test user; the RLS/role queries in `SUPABASE.md` | — |
| 9 | **App:** Release xcconfig → `https://api.ryftapp.de` and the Supabase URL and publishable key; the hand-off list above | the app talks to nothing |

Acceptable for an alpha, but known gaps:
- **Single points:** one NATS replica and one scoring replica.
- **No Prometheus or alerting.** At least add an external uptime check on
  `https://api.ryftapp.de/v1/me`, expecting 401.
- **Pod logs only, not shipped.**
- **Apple token revocation** is not implemented. App Store review requires it, an
  internal TestFlight alpha does not.
- **Kubernetes auth needs HCP Vault to reach the EKS API.** If you narrow
  `endpoint_public_access_cidrs`, include Vault's egress addresses, or logins (and
  secret refreshes) stop.
- **Cost:** about €120–160 a month (`infra/aws/README.md`).
- **Your own uncommitted files:** the `ryft-prod-euc1` rename in `infra/aws`
  (`locals.tf`, `variables.tf`) matches the applied state; commit it. `infra/vault` is
  untracked; commit it, together with its `.terraform.lock.hcl`, once you have reviewed
  the changes.

## Deploy

Local:

```bash
make up && make smoke                 # Docker or Podman: migrate, roles, NATS users, contract checks
make check-swift                      # optional, macOS
```

Images (CI does this on push once the repo is on GitHub; by hand):

```bash
make push REGISTRY=ghcr.io/timkrebs/ryft            # ingest, scoring, api, migrate: :<short-sha> and :latest
```

EKS, in order (each Terraform root applied by you; Terraform state never leaves your
machine; full runbook in [`OPERATIONS.md`](OPERATIONS.md#eks-apiryftappde)):

```bash
(cd infra/aws/dns && terraform init && terraform plan -out=tfplan && terraform apply tfplan && terraform output godaddy_instructions)
# GoDaddy: switch the nameservers to the four printed; check: dig NS ryftapp.de @a.nic.de +short
(cd infra/aws && terraform plan -out=tfplan && terraform apply tfplan)
aws eks update-kubeconfig --region eu-central-1 --name ryft-prod-euc1 --alias ryft-prod-euc1
(cd infra/aws/kubernetes && cp terraform.tfvars.example terraform.tfvars)      # set acme_email
(cd infra/aws/kubernetes && terraform init && terraform plan -out=tfplan && terraform apply tfplan)
(cd infra/vault && cp terraform.tfvars.example terraform.tfvars)               # Supabase values (SUPABASE.md)
(cd infra/vault && export VAULT_TOKEN=… && \
  terraform init && terraform test && terraform plan -out=tfplan && terraform apply tfplan)
# newTag in deploy/k8s/overlays/eks/kustomization.yaml = the CI commit
make k8s-validate && make k8s-apply-eks
```

Then, with each user's token, rescore history (`POST /v1/recompute`, `OPERATIONS.md` →
Upgrading). Run `make smoke-supabase` only with the dedicated test user.

Self-hosted (k3s etc.): `make k8s-apply-local` (`OPERATIONS.md` → Self-hosted).
