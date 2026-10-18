# RYFT Platform — Claude Code prompt

You are building the backend platform for **RYFT** ("Your whole health."), a self-hosted
Whoop-style health platform. A Garmin watch syncs into Apple Health; the RYFT iOS app
(built separately in `../app`) reads HealthKit and pushes the data to this platform;
the platform turns it into daily **Readiness, Recovery, Strain and Sleep** scores and
serves them back to the app.

Work only inside this folder (`plattform/`). The iOS app is a separate agent's job —
your contract with it is the HTTP API described below and documented in `docs/API.md`,
which you own and must keep accurate.

## 0. What is already here — read it before writing anything

This folder contains a working scaffold. Read it fully first (`docs/ARCHITECTURE.md`,
then `backend/`), then run it:

```bash
cd backend && go mod tidy && go vet ./... && go test ./...
cd .. && make up && make smoke        # docker compose stack + synthetic end-to-end test
```

`make smoke` must print a table of scored days before you change anything. Fix the
environment first if it does not.

Layout:

```
backend/            Go 1.24+ module github.com/timkrebs/ryft
  cmd/ingest        POST /v1/sync → validates, upserts samples/sleep/workouts, publishes NATS event
  cmd/scoring       NATS JetStream consumer + 15-min timer → writes daily_scores, workout strain
  cmd/api           read API for the app (+ PATCH /v1/me, POST /v1/recompute)
  internal/scoring  pure algorithms (algorithms.go, unit-tested) + compute.go (DB orchestration)
  internal/store    all SQL (pgx v5)            internal/db   pool + embedded migrations
  internal/events   NATS JetStream bus          internal/httpx json, bearer auth, health, serve
  internal/model    wire types (mirrored 1:1 by the iOS app)
deploy/k8s/         kustomize: Postgres 17, NATS 2.11 JetStream, 3 deployments, Ingress
scripts/            synth.py (fake HealthKit data), smoke.sh (e2e), print_days.py
docker-compose.yml, Makefile, docs/ARCHITECTURE.md
```

Conventions to keep: single Go module, one Dockerfile with `--build-arg SERVICE=`,
env-only config (`DATABASE_URL`, `NATS_URL`, `API_KEY`, `PORT`), stdlib `net/http`
with Go 1.22 method routing, `slog` JSON logs, migrations as numbered SQL files in
`internal/db/migrations/` (never edit an applied migration — add a new one), pure
scoring functions with tests pinning the calibration, `make smoke` as the acceptance test.

## 1. Goal of this engagement

Turn the scaffold into the complete v1 platform the RYFT app needs. The app's screens
(Today, Sleep, Strain, Insights, Profile) are the spec; every number on them must come
from this API.

## 2. Work items, in order

Commit after each item with a clear message. Keep `make smoke` green throughout.

### 2.1 Readiness score
Add `readiness smallint` to `daily_scores` (migration `0002_readiness.sql`) and compute it
in `internal/scoring`: `readiness = round(0.6·recovery + 0.4·sleep_performance)`, then
subtract up to 10 points when yesterday's strain exceeded what the recovery supported
(strain > 14 with recovery < 50). Pure function `Readiness(recovery, sleepPerf int,
yesterdayStrain float64) int` with tests; expose it in `DailyScore` as `readiness`.
Also add a `state` string the app shows under the greeting: `"balanced"` (≥67),
`"strained"` (34–66), `"depleted"` (<34), `"calibrating"` when baselines are missing.

### 2.2 Sleep detail
`GET /v1/days/{day}` currently returns stage minutes buried in `details`. Promote a typed
`sleep` object (see API below): total, time in bed, per-stage minutes and percentages,
efficiency, `quality` (0–100, = sleep performance for now) and a label
`"good" | "fair" | "poor"`. Map HealthKit `core` → `"light"` in the API — the app shows
*Light*. Add `GET /v1/sleep?range=week|month` returning per-day totals + stage minutes
and the averages the Week/Month tabs need.

### 2.3 Workout detail
`GET /v1/workouts/{id}`: the workout row plus its heart-rate series (from `samples` in
the window, down-sampled to ≤ 600 points), zone minutes (5 HRR zones, already computed
in `scoring.Strain`), average/max HR, TRIMP and strain. Store zone minutes on the
workout row at scoring time (`zone_minutes jsonb`) so the read is cheap.

### 2.4 Trends
`GET /v1/trends?metric=readiness|recovery|strain|sleep|hrv|rhr&range=7d|30d|90d`
returning `[{day, value}]` plus `avg`, `min`, `max`, and the 30-day baseline for hrv/rhr.
Serve from `daily_scores`; no new tables.

### 2.5 Insights
A rule-based insight engine in `internal/insights` (pure functions + tests) that looks at
the last 14 scored days and emits short cards: id, severity (`info|notice|warn`), title,
body, metric. Start with these rules and make adding one a 10-line change:
- HRV below 30-day baseline for 3+ consecutive nights
- resting HR ≥ 5 bpm above baseline overnight
- sleep debt ≥ 90 min
- three high-strain days (≥14) without a recovery ≥ 67 in between
- sleep consistency: bedtime variance > 90 min over 7 nights
- positive ones too: 7-day readiness trend up, best HRV in 30 days
`GET /v1/insights` returns them for today, computed on read (cache in-memory for 5 min).

### 2.6 Observability
Add Prometheus metrics to all three services on `/metrics` (`prometheus/client_golang`):
samples ingested by type, sync requests and latency, days scored, scoring duration,
NATS redeliveries. Add a `ServiceMonitor`-free scrape annotation on the pods
(`prometheus.io/scrape: "true"`). Document a Grafana panel list in `docs/OBSERVABILITY.md`.

### 2.7 Secrets and deployment hardening
- Add `deploy/k8s/vault/` with a `VaultStaticSecret` (Vault Secrets Operator) that
  materialises `ryft-secrets` from `secret/ryft`, as the alternative to `secrets.yaml`.
- Add a `NetworkPolicy`: only the Ingress may reach ingest/api; only the services may
  reach Postgres and NATS.
- Add `PodDisruptionBudget`s and `HorizontalPodAutoscaler` for api/ingest (CPU 70 %).
- Add a CronJob that runs `pg_dump` nightly to a PVC (document restore).

### 2.8 CI
`.github/workflows/ci.yml`: on push → `go vet`, `go test -race`, `golangci-lint`, build
the three images with the Dockerfile, push to `ghcr.io/timkrebs/ryft/<service>` tagged
with the short SHA and `latest` on `main`. Add `.golangci.yml` with a sane default set.

### 2.9 API docs
Write `docs/API.md`: every endpoint, request/response JSON with field types and units,
error shape, auth, and a changelog. The iOS agent builds from this file — treat it as a
contract: additive changes only; never rename or remove a field without bumping to `/v2`.

## 3. API contract (v1)

Auth: `Authorization: Bearer <API_KEY>` on everything under `/v1`. Errors:
`{"error": "message"}` with 400/401/404/422/500/503. Dates are RFC3339 UTC; days are
`YYYY-MM-DD` in the user's timezone (`users.tz`).

```
POST  /v1/sync                      (ingest)  SyncRequest → SyncResponse
GET   /v1/me                        User           PATCH /v1/me  UserPatch → User
GET   /v1/days?from&to              [DailyScore]
GET   /v1/days/{day}                DayDetail {score, sleep, workouts}
GET   /v1/sleep?range=week|month    SleepRange
GET   /v1/workouts?from&to          [Workout]      GET /v1/workouts/{id}  WorkoutDetail
GET   /v1/trends?metric&range       Trend
GET   /v1/insights                  [Insight]
POST  /v1/recompute {from,to}       202 {status:"queued"}
GET   /healthz /readyz /metrics     (no auth)
```

Existing shapes are in `backend/internal/model/model.go` and must stay backwards
compatible. New shapes:

```jsonc
DailyScore  += "readiness": 84, "state": "balanced"
DayDetail.sleep = {
  "start": "...", "end": "...", "total_min": 468, "in_bed_min": 492, "efficiency": 95,
  "stages": [ {"stage":"deep","min":112,"pct":24}, {"stage":"light","min":261,"pct":56},
              {"stage":"rem","min":95,"pct":20}, {"stage":"awake","min":10,"pct":2} ],
  "quality": 82, "quality_label": "good", "has_stages": true
}
SleepRange   = { "range":"week", "days":[{day,total_min,stages:[...] ,quality}], "avg_total_min": 450, "avg_quality": 78 }
WorkoutDetail = Workout + { "hr": [{"t":"...","bpm":152}], "zone_min":[0,5,20,30,2], "trimp": 77 }
Trend        = { "metric":"readiness", "range":"7d", "points":[{"day":"2026-10-12","value":71}], "avg":74, "min":52, "max":88, "baseline": null }
Insight      = { "id":"hrv-low-3d", "severity":"warn", "title":"HRV below your baseline for 3 nights",
                 "body":"...", "metric":"hrv", "day":"2026-10-18" }
```

## 4. Rules

- Go only; no framework. Add dependencies only when they clearly pay for themselves
  (client_golang yes; an ORM no).
- Every scoring/insight rule is a pure function with a test. Keep `algorithms_test.go`
  calibration tests passing or explain in the commit why a constant moved.
- Update `docs/ARCHITECTURE.md` and `docs/API.md` in the same commit as the behaviour.
- Never commit secrets. `deploy/k8s/secrets.yaml` stays git-ignored.
- Manifests are applied by hand with `kubectl apply -k deploy/k8s`; keep them
  kustomize-only, no Helm.
- When something in this prompt conflicts with what you find in the code, prefer the
  prompt and note the discrepancy in `docs/DECISIONS.md`.
- Finish with: `make test`, `make smoke`, a short `docs/STATUS.md` listing what is done,
  what is not, and the exact commands to deploy.
