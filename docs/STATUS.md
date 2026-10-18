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
| `make k8s-validate` | base + Vault overlay render and validate with kubeconform (VSO CRD schemas included) |
| Backup script + restore procedure | run against the compose Postgres: dump as uid 70 with read-only root FS, `pg_restore --clean --if-exists` into empty and populated databases, identical row counts |
| Dockerfile cross-build | `linux/amd64` image built on arm64 and started |
| `actionlint .github/workflows/ci.yml` | pass |
| Independent review of the full diff | nothing that crashes, loses data or breaks a deploy; four wrong-result bugs found and fixed (`DECISIONS.md` D22), one with a new end-to-end check |

## Not done / not verified

- **CI has not run on GitHub.** This repo has no remote yet; the workflow is linted, not
  executed. Action major versions (`checkout@v5`, `setup-go@v6`, `golangci-lint-action@v8`)
  should be confirmed on the first run.
- **Nothing was applied to a cluster.** Manifests are validated offline only. The
  NetworkPolicy needs a CNI that enforces it (k3s does) and assumes Traefik in
  `kube-system` or ingress-nginx, and Prometheus in namespace `monitoring`.
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
