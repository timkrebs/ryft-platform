# RYFT platform — status

State of the platform after go-live (M1, M1b and the zero-trust layout; 2026-10-18). Contract:
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

**Go-live (M1b), all applied by the user on 2026-10-18:**

| Item | State |
| --- | --- |
| CI | `github.com/timkrebs/ryft-platform`, every job green (test with Postgres, lint, manifests, smoke with the NATS permission check, images). Public multi-arch images `ghcr.io/timkrebs/ryft/{ingest,scoring,api,migrate}:<short-sha>`. |
| Terraform | `infra/aws`, `infra/aws/dns`, `infra/aws/kubernetes` (Traefik, cert-manager with public DNS-01 checks, ExternalDNS, the operator) and `infra/vault` applied; state local on the user's machine. |
| DNS + TLS | `ryftapp.de` delegated from GoDaddy to Route 53. `api.ryftapp.de` → the NLB (ExternalDNS). Let's Encrypt certificates in `ryft-api` and `ryft-ingest`, valid until 2027-01-16. Port 80 closed. |
| Workloads | `make k8s-apply-eks` at `a602a20`: migrate ran nine migrations against Supabase and set the role passwords; api ×2, ingest ×2, scoring, nats ready with 0 restarts; every Secret synced from Vault with only its own keys. |
| Public check | `GET https://api.ryftapp.de/v1/me` and `/v1/sync` → `401` with `WWW-Authenticate`, HSTS, `Cache-Control: no-store`, `X-Request-ID`. |

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
| Vault | HCP namespace paths fixed. Credentials are generated and written only where used. A migrate namespace, the api's Supabase key, and a mocked `terraform test` with golden operator manifests. | `infra/vault`, D45 |
| Operator | Vault Secrets Operator 1.6.0 as a pinned add-on, with no default identity. | `infra/aws/kubernetes`, D46 |

**After go-live** (account flows the app needs, operations gaps):

| Item | What | Where |
| --- | --- | --- |
| Apple token revocation | `POST /v1/me/apple-authorization` exchanges the Sign in with Apple code and keeps the refresh token encrypted in `apple_tokens` (api only). `DELETE /v1/me` revokes it before deleting the Supabase user; the reconciler retries. | `internal/apple`, `internal/sealed`, migration 0010, D48 |
| Production account checks | `make smoke-supabase` against the public host. With `SUPABASE_SECRET_KEY`, two throwaway users: row creation, isolation, deletion down to the Supabase sign-in. | `scripts/check_accounts.py` |
| Rescore tool | `scripts/rescore_all.sh` runs `scoring rescore` in the scoring pod with its own role. | D50 |
| Edge rate limit | 20 r/s, burst 50 per client IP (Traefik), client IPs preserved by the NLB | D53 |
| Logs | Fluent Bit → CloudWatch `/ryft/ryft-prod-euc1/containers`, KMS, 14 days, RYFT namespaces and Traefik only | `infra/aws/kubernetes/logs.tf`, D52 |
| Metrics | Grafana Cloud `k8s-monitoring` in namespace `monitoring`, token from Vault, alert rules; `/metrics` on port 9090 | `deploy/monitoring`, D51 |
| Terraform state | S3 bucket with KMS and lockfiles for all four roots | `infra/aws/state`, D54 |
| Budget | AWS Budgets monthly alarm, 80 % actual and 100 % forecast | `infra/aws/budget.tf`, D55 |
| Apex | `ryftapp.de` page, `www` redirect, `apple-app-site-association` (webcredentials, applinks) | `deploy/k8s/base/web`, D55 |
| Supabase | Checklist for email accounts, redirects, templates, rate limits, Apple key; the move to Frankfurt (decided) | `SUPABASE.md`, D49 |

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

Built and tested, applied by nobody yet: everything in **After go-live**. Your steps,
in this order:

1. **Remote state first.** The four roots now declare the S3 backend, so `terraform init`
   fails without `infra/backend.hcl`. Run `infra/aws/state`, then
   `make tf-migrate-state` (`OPERATIONS.md` → Terraform state).
2. **Supabase in Frankfurt:** new project and the full checklist (`SUPABASE.md` → Moving
   the project to Frankfurt), including the Apple key for revocation
   (`service_secrets.api.APPLE_*`). Done on 2026-10-19.
3. **`infra/vault`:** new Supabase values, the Apple values, and the rotated Grafana
   token (`service_secrets.monitoring.GC_TOKEN`); then plan and apply (monitoring
   namespace and Apple key included). Retire the hand-made Grafana secret first
   (`OPERATIONS.md` → Monitoring).
4. **`infra/aws/kubernetes`:** plan and apply: Traefik with client IPs preserved,
   Fluent Bit with its log group and key, the operator without `_raw`, cert-manager's
   public DNS-01 checks.
5. **`infra/aws`:** `budget_alert_emails` in tfvars, plan and apply.
6. **Images and deploy:** CI for the current commit, pin its short SHA (quoted), then
   `make k8s-apply-eks` (migrations 0009 and 0010, the edge limit, the metrics port,
   the apex site).
7. **`make monitoring-apply`**, then the Grafana Cloud steps: exclude `ryft-.*` pod logs,
   load the alerts.
8. **`make smoke-supabase`** against `https://api.ryftapp.de` with `SUPABASE_SECRET_KEY`.
   Record the run here.

Known gaps, accepted for the alpha:
- **Failure-path metrics:** `ryft_events_handled_total{result="retry|failed"}` and
  `ryft_nats_redeliveries_total` are wired but were not exercised end to end.
- **Single points:** one NATS replica and one scoring replica.
- **Kubernetes auth needs HCP Vault to reach the EKS API** (TokenReview).
- **Grafana excludes RYFT logs by configuration, not by a boundary;** Beyla sees RYFT's
  HTTP traffic (D51).
- **Insight texts are English only.** Handlers are covered by the smoke contract checks,
  not by Go tests against a database.
- **Cost:** about €120–160 a month (`infra/aws/README.md`). The budget alarm watches
  it.

- **For the iOS side** (nothing in `../app` was changed):
  - Decode `User.email`/`provider`, `DailyScore.steps`/`active_kcal`,
    `DayStrain.steps`/`active_kcal`, and `APIErrorBody.requestId`. All are optional, and
    the current models still decode.
  - Status handling: treat `503` as "retry after `Retry-After`", never as sign-out. Honour
    `429` + `Retry-After`. `413` means a smaller sync page. Only a second `401` after a
    refresh signs out.
  - Delete account: call `DELETE /v1/me`, then sign out and clear caches.
  - After every successful Sign in with Apple, send
    `ASAuthorizationAppleIDCredential.authorizationCode` once to
    `POST /v1/me/apple-authorization` (`204`; ignore `422`/`501`, retry `503` once).
    Without it, deletion cannot revoke the Apple grant.
  - A `429` may come from the edge with a plain-text body: honour `Retry-After` without
    expecting JSON.
  - Entitlements: `webcredentials:ryftapp.de` for password AutoFill with email accounts
    (`apple-app-site-association` is live with the deploy); `applinks:ryftapp.de` once
    the app handles `/auth/*` universal links.
  - New users start in `Europe/Berlin`: send the device's zone with `PATCH /v1/me` on
    first launch.
  - Show steps and kcal on the Strain screen.
  - `SleepMath.percent` treats values ≤ 1 as fractions, so a 1 % awake share renders as
    100 %. The API sends whole percentages (`API.md`, Conventions).
  - The app also reads `details` keys. They are frozen, but the typed fields
    (`baseline_days`, `DayDetail.sleep`, `DayDetail.strain`) are the contract.

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
# After `make tf-migrate-state`, every `terraform init` takes -backend-config=$(git rev-parse --show-toplevel)/infra/backend.hcl
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
make monitoring-apply                                  # Grafana Cloud collector, after infra/vault has the token
```

Run `make smoke-supabase` only with the dedicated test user.

Self-hosted (k3s etc.): `make k8s-apply-local` (`OPERATIONS.md` → Self-hosted).
