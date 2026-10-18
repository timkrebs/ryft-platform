# RYFT platform — status

State of the v1 platform at the end of this engagement (2026-10-18). Contract:
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
| EKS cluster | VPC + EKS `ryft-prod` (Terraform modules), applied by the user on 2026-10-18 | [`infra/aws`](../infra/aws/README.md) |
| Domain `ryftapp.de` | Route 53 zone (own root, long-lived) with CAA for Let's Encrypt; GoDaddy stays the registrar | `infra/aws/dns` |
| Edge | AWS Load Balancer Controller, Traefik behind an NLB (HTTPS only), cert-manager with Let's Encrypt DNS-01 issuers, ExternalDNS, encrypted gp3 default StorageClass; all Terraform + Helm with Pod Identity | `infra/aws/kubernetes` |
| EKS overlay | `api.ryftapp.de` Ingress (Traefik + cert-manager), encrypted storage class on every volume, image tags to pin | `deploy/k8s/overlays/eks` |

Also: Podman support in the Makefile, JSON errors on unknown routes, `422` for inverted or
oversized ranges, UTC timestamps everywhere, `DailyScore.baseline_days`, end-to-end
contract checks in `make smoke`, Swift-side contract checks (`make check-swift`).

## How it was verified

| Check | Result |
| --- | --- |
| `make test` and `go test -race ./...` | pass (scoring, insights, httpx, api cache) |
| `golangci-lint run` (v2.15, `.golangci.yml`) | 0 issues |
| `make down && make up && make smoke` on Podman, fresh volumes (all six migrations) | pass, incl. `scripts/check_api.py`: every endpoint, documented invariants, error paths, a workout across midnight |
| `make check-swift` | the Swift mirror in `API.md` decodes every endpoint; the app's current models (`../app`) decode every endpoint they call |
| `make k8s-validate` | base, Vault and EKS overlays render and validate with kubeconform (77 resources; VSO CRD schemas included) |
| Terraform roots `infra/aws`, `dns`, `kubernetes` | `fmt`, `validate`, `terraform test` (mocked: 8 + 2 + 6 runs), tflint clean, trivy: no findings in `dns`/`kubernetes` |
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
- **Not on EKS yet:** Prometheus (`monitoring` namespace, needs a third node), Vault
  Secrets Operator, content on the apex `ryftapp.de` / `www`, DNSSEC.
- **Vault overlay untested against a live Vault / Vault Secrets Operator.**
- **Failure-path metrics** (`ryft_events_handled_total{result="retry|failed"}`,
  `ryft_nats_redeliveries_total`) are wired but were not exercised end to end.
- **HealthKit source de-duplication** (pre-existing): overlapping sources (iPhone + Garmin)
  are summed. Calories and steps are ingested but not aggregated for the Strain screen.
- Insight texts are English only; there are no Go handler tests against a database
  (handlers are covered by the smoke contract checks).
- **For the iOS side:** `SleepMath.percent` in the app treats values ≤ 1 as fractions, so
  a 1 % awake share renders as 100 %. The API sends whole percentages (`API.md`,
  Conventions). The app also reads `details` keys; they are frozen, but the typed fields
  (`baseline_days`, `DayDetail.sleep`, `DayDetail.strain`) are the contract.

## Deploy

Local:

```bash
make up && make smoke                 # Docker or Podman; prints the scored table + contract checks
make check-swift                      # optional, macOS
```

Images (CI does this on push once the repo is on GitHub; by hand):

```bash
make push REGISTRY=ghcr.io/timkrebs/ryft            # builds, tags :<short-sha> and :latest, pushes
```

Kubernetes, plain Secret:

```bash
cp deploy/k8s/secrets.example.yaml deploy/k8s/secrets.yaml   # set POSTGRES_PASSWORD (also in DATABASE_URL) and API_KEY
# set the image tag in deploy/k8s/base/kustomization.yaml and the host in deploy/k8s/base/ingress.yaml
make k8s-validate
kubectl apply -k deploy/k8s
kubectl -n ryft rollout status deploy/api deploy/ingest deploy/scoring
```

Kubernetes, Vault (after the Vault-side setup in [`OPERATIONS.md`](OPERATIONS.md#deploy-with-vault-vault-secrets-operator)):

```bash
kubectl apply -k deploy/k8s/vault
kubectl -n ryft get vaultstaticsecret ryft-secrets
```

Then, once per install or upgrade from the scaffold, rescore history so the new
columns are filled, and run the smoke test through port-forwards:

```bash
kubectl -n ryft port-forward svc/api 8082:80 & kubectl -n ryft port-forward svc/ingest 8081:80 &
curl -fsS -X POST localhost:8082/v1/recompute -H "Authorization: Bearer $API_KEY" \
  -H 'Content-Type: application/json' -d "{\"from\":\"$(date -v-90d +%F)\",\"to\":\"$(date +%F)\"}"
API_KEY=$API_KEY make smoke           # writes 10 synthetic days for a device named "synth"
```

`make smoke` against a real install adds synthetic data to the real user; skip it
there, or run it before the phone's first sync.

### EKS / api.ryftapp.de

Commands in order, each run by you (Terraform state never leaves your machine):

```bash
(cd infra/aws/dns && terraform init && terraform plan -out=tfplan && terraform apply tfplan && terraform output godaddy_instructions)
# GoDaddy: switch the nameservers to the four printed; check: dig NS ryftapp.de @a.nic.de +short
(cd infra/aws && terraform plan -out=tfplan && terraform apply tfplan)         # EBS add-on update
(cd infra/aws/kubernetes && cp terraform.tfvars.example terraform.tfvars)      # set acme_email
(cd infra/aws/kubernetes && terraform init && terraform plan -out=tfplan && terraform apply tfplan)
# after M1 + first CI run: overlays/eks secrets.yaml + image tag, then make k8s-apply-eks
```

Full runbook: `docs/OPERATIONS.md` → EKS.
