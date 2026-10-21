# RYFT platform

Your own Whoop: Garmin watch → Apple Health → iOS app → Go microservices on
Kubernetes → daily Recovery / Strain / Sleep scores.

Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) first — it explains the data
flow, the schema and the scoring model. The HTTP contract with the app is
[`docs/API.md`](docs/API.md); operations (Vault, network policy, backups) are in
[`docs/OPERATIONS.md`](docs/OPERATIONS.md), metrics in
[`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md), what is done and how to deploy in
[`docs/STATUS.md`](docs/STATUS.md), and judgement calls in
[`docs/DECISIONS.md`](docs/DECISIONS.md).

```
backend/      Go module: cmd/{ingest,scoring,api}, internal/{scoring,insights,store,db,events,httpx,metrics,model,config}
infra/aws/    Terraform: VPC + EKS cluster (modules/network, modules/eks), dns/ (ryftapp.de zone),
              kubernetes/ (Traefik, cert-manager, ExternalDNS, LB controller, encrypted storage)
deploy/k8s/   kustomize: overlays/eks (api.ryftapp.de), base/ (the services, Ingress, policies, backups), overlays local-postgres; deploy/rabbitmq (the broker); deploy/argocd
../app/          SwiftUI app (HealthKit sync, Today / Trends / Settings)
scripts/      synth.py (fake Health data), smoke.sh (end-to-end test), print_days.py
docker-compose.yml   local stack
```

## 1. Backend locally

```bash
cd backend && go mod tidy && go test ./...     # tidy once: go.sum is generated on your machine
cd .. && make up                               # Postgres + RabbitMQ + ingest(:8081) scoring api(:8082) notify
make smoke                                     # synthetic 10 days through the whole pipeline + API contract checks
```

`make smoke` prints a table like:

```
day          recovery  strain  sleep%  need  actual    hrv   rhr  resp  calibrating
2026-10-14         73       0      83   570     471   57.6  49.7  15.1  False
2026-10-15         33    15.0     84   550     463   47.1  51.2  14.6  False
```

Config is env-only: `DATABASE_URL` (the service's own role; for `migrate` the schema owner,
plus `DB_PASSWORD_INGEST|SCORING|API`), `AMQP_URL` with `AMQP_USER` + `AMQP_PASSWORD` and the client certificate (`AMQP_CA_FILE`, `AMQP_CERT_FILE`, `AMQP_KEY_FILE`), `PORT`, `DB_MAX_CONNS`, `LOG_LEVEL`, and for ingest/api
`AUTH_MODE` with `SUPABASE_PROJECT_REF` (production, Supabase tokens) or `API_KEY` +
`ALLOW_INSECURE_APIKEY_AUTH=true` (compose; bearer `API_KEY` or `API_KEY:<uuid>`).
Supabase setup: [`docs/SUPABASE.md`](docs/SUPABASE.md).

## 2. Kubernetes

One namespace per workload (`ryft-ingest`, `ryft-scoring`, `ryft-api`, `ryft-notify`,
`ryft-rabbitmq`, `ryft-migrate`, `ryft-smoke`), each with only its own credentials:

```bash
# EKS + Supabase + Vault (infra/aws, infra/aws/kubernetes and infra/vault applied; images on ghcr)
make k8s-validate && make k8s-apply-eks
kubectl -n ryft-api port-forward svc/api 8082:80 &  kubectl -n ryft-ingest port-forward svc/ingest 8081:80 &
make smoke-supabase              # with SUPABASE_URL/ANON_KEY + a dedicated test user; writes synthetic days

# Self-hosted (in-cluster Postgres, API key, generated per-namespace secrets)
make k8s-apply-local
```

Runbook, namespaces, network policy, scaling, backups and restore:
[`docs/OPERATIONS.md`](docs/OPERATIONS.md). Vault: [`infra/vault/README.md`](infra/vault/README.md).

## CI

`.github/workflows/ci.yml` runs on every push and pull request: `go vet`,
`go test -race`, `golangci-lint` (config: `.golangci.yml`), `make k8s-validate`
(kustomize + kubeconform) and `make up && make smoke` on Docker. On pushes that pass,
it builds the three images (linux/amd64 + linux/arm64) from `backend/Dockerfile` and
pushes `ghcr.io/timkrebs/ryft/<service>:<short-sha>`, plus `:latest` on `main`.
Locally: `make test`, `cd backend && golangci-lint run`, `make k8s-validate`.

## 3. iOS app

1. On the iPhone: Garmin Connect → More → Settings → **Apple Health** → allow everything.
2. `cd ../app && xcodegen generate && open Ryft.xcodeproj` (or create an empty iOS App
   named *RYFT* in Xcode and drag the `../app/Ryft` folder in). Set your team, make
   sure the target has the **HealthKit** capability with *Background Delivery* and
   **Background Modes → Background fetch**. `Info.plist` and `Ryft.entitlements`
   are included.
3. Run on a real device (HealthKit does nothing in the simulator). Accept the Health
   prompt, open Settings in the app, enter the API URL and API key, tap *Save & test*,
   then *Sync now*. The first sync uploads 90 days; after that only deltas.
4. Scores need ~7 nights before `calibrating` turns off and the baselines are yours.

## Adding a sample type or a metric

1. `backend/internal/model/model.go` → add the `Sample*` constant.
2. `../app/Ryft/Health/HealthKitManager.swift` → add a `QuantitySpec`.
3. Use it in `internal/scoring/compute.go`; pin the behaviour in `algorithms_test.go`.
