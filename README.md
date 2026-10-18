# RYFT platform

Your own Whoop: Garmin watch → Apple Health → iOS app → Go microservices on
Kubernetes → daily Recovery / Strain / Sleep scores.

Read [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) first — it explains the data
flow, the schema and the scoring model.

```
backend/      Go module: cmd/{ingest,scoring,api}, internal/{scoring,store,db,events,httpx,model,config}
deploy/k8s/   kustomize manifests (Postgres, NATS JetStream, 3 services, Ingress)
../app/          SwiftUI app (HealthKit sync, Today / Trends / Settings)
scripts/      synth.py (fake Health data), smoke.sh (end-to-end test), print_days.py
docker-compose.yml   local stack
```

## 1. Backend locally

```bash
cd backend && go mod tidy && go test ./...     # tidy once: go.sum is generated on your machine
cd .. && make up                               # Postgres + NATS + ingest(:8081) scoring api(:8082)
make smoke                                     # synthetic 10 days through the whole pipeline
```

`make smoke` prints a table like:

```
day          recovery  strain  sleep%  need  actual    hrv   rhr  resp  calibrating
2026-10-14         73       0      83   570     471   57.6  49.7  15.1  False
2026-10-15         33    15.0     84   550     463   47.1  51.2  14.6  False
```

Config is env-only: `DATABASE_URL`, `NATS_URL`, `API_KEY`, `PORT`, optional `USER_ID`.

## 2. Kubernetes

```bash
make images REGISTRY=ghcr.io/<you>/ryft && make push      # or your own registry
cp deploy/k8s/secrets.example.yaml deploy/k8s/secrets.yaml  # set API_KEY (openssl rand -hex 32) + DB password
# edit deploy/k8s/kustomization.yaml images + ingress.yaml host
kubectl apply -k deploy/k8s
kubectl -n ryft get pods
kubectl -n ryft port-forward svc/api 8082:80 &  kubectl -n ryft port-forward svc/ingest 8081:80 &
API_KEY=<your key> make smoke
```

Then put the Ingress (or the two services) behind your tunnel with TLS.

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
