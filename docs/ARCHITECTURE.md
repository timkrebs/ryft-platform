# RYFT — architecture

A self-hosted Whoop: a Garmin watch feeds Apple Health, an iOS app ships that data to
Go microservices on Kubernetes, and the services turn it into daily **Readiness**,
**Recovery**, **Strain** and **Sleep** scores the app renders. The HTTP contract with
the app is [`API.md`](API.md).

```
 Garmin watch ──BLE──▶ Garmin Connect (iOS) ──▶ Apple Health (HealthKit)
                                                        │
                                           HKAnchoredObjectQuery (incremental)
                                                        ▼
                                              Ryft iOS app (SwiftUI)
                                     POST /v1/sync │            ▲ GET /v1/days, /v1/days/{d}, /v1/me
                                                   ▼            │
   ┌──────────── Kubernetes namespace `ryft` ────────────────────────────────────┐
   │   ingest ──── Postgres ◀──── scoring ──── NATS JetStream ◀──── api            │
   │   (writes raw samples,   (computes daily_scores,     (reads scores, settings, │
   │    publishes event)       workout strain)             publishes recompute)    │
   └───────────────────────────────────────────────────────────────────────────────┘
```

## Why HealthKit and not the Garmin API

Garmin's Connect Developer Program (Health/Activity APIs) is approval-only, aimed at
businesses, and stopped accepting new applications in 2026. Garmin Connect already
writes heart rate, resting HR, HRV, respiratory rate, SpO2, sleep (with stages on
current firmware), workouts, steps and active energy to Apple Health, so reading
HealthKit gets the same signals with a stable, documented API and no credentials on
the server. It also makes the platform watch-agnostic.

Garmin-only metrics (Body Battery, Training Readiness, HRV *status*) do not reach
HealthKit. If you ever want them, add a `garmin-sync` service that uses the
unofficial Connect client (`garth` / `python-garminconnect` style) and writes into
the same `samples` table with new `type` values — nothing else changes.

## Services

| Service   | Port | Responsibility | Talks to |
|-----------|------|----------------|----------|
| `ingest`  | 8080 | `POST /v1/sync`: validate, idempotent upsert of samples / sleep / workouts, publish `ryft.ingested` with the local days touched | Postgres, NATS |
| `scoring` | 8080 (health only) | Consume `ryft.ingested` (durable pull consumer); recompute touched days oldest-first; every 15 min recompute yesterday + today | Postgres, NATS |
| `api`     | 8080 | `GET /v1/me`, `PATCH /v1/me`, `GET /v1/days`, `GET /v1/days/{day}`, `GET /v1/workouts`, `POST /v1/recompute` | Postgres, NATS |

All three are one Go module (`backend/`), one Dockerfile (`--build-arg SERVICE=…`),
share `internal/` packages, and run DB migrations on boot behind an advisory lock.

**Auth.** Single-user: the app sends `Authorization: Bearer <API_KEY>`; services
compare in constant time. `users.id` is a fixed seeded UUID. Multi-user later = a
`devices.api_key_hash` column and a lookup in `httpx.BearerAuth`; the schema already
keys everything by `user_id`.

**Events.** Stream `RYFT`, subject `ryft.ingested`, work-queue retention, file
storage, 7-day max age. Payload `{user_id, days[], reason}`. `reason: "recompute"`
with two days means an inclusive range. Failures NAK with a 30 s delay, max 10
deliveries. If NATS is down, ingest still stores the data and scoring's 15-minute
timer catches up.

## Data model (Postgres)

- `users` — `tz` (the local day boundary), `max_hr` (for heart-rate reserve), `sleep_base_min`.
- `devices` — one row per app install, `last_sync_at`.
- `samples(user_id, type, start_at, end_at, value, unit, source)` — the raw time series. `type` is a closed vocabulary: `heart_rate, hrv_sdnn, resting_hr, respiratory_rate, spo2, vo2max, active_energy, steps`. Unique on `(user, type, start, end, source)` → re-uploads are no-ops.
- `sleep_segments` — HealthKit sleep analysis as-is: `inbed, asleep, core, deep, rem, awake`.
- `workouts` — one row per HKWorkout (`id` = HK UUID), with `strain` filled by scoring.
- `daily_scores(user_id, day)` — the product: readiness + state, recovery, strain, sleep performance / need / actual / debt, overnight HRV, RHR, respiratory rate, SpO2, `calibrating`, and a `details` JSONB with the breakdown the UI can show.

A "day" is a local calendar day in `users.tz`. The night that *ends* on day D belongs to
D (sleep ending between D-1 20:00 and D 13:00), so the morning's recovery and the
day's strain line up like Whoop.

## Scoring (`internal/scoring/algorithms.go`, unit-tested)

**Baselines.** Each overnight signal is compared to your own previous 30 scored days.
HRV is compared in log space (it is log-normally distributed). With fewer than 7
days the row is flagged `calibrating` and a population SD is used instead of yours.

**Recovery (1–100).** Sub-scores map a z-score through `100 / (1 + e^(-1.2 z))`
(z=0 → 50, +1 → 77, −1 → 23). Weights: HRV 0.45, resting HR 0.25 (lower is better),
sleep performance 0.25, respiratory rate 0.05 (higher is worse — the illness tell).
Missing signals drop out and the weights renormalise. Bands: ≥67 green, 34–66 yellow,
<34 red.

**Readiness (1–100) and state.** The Today screen's headline:
`round(0.6 · recovery + 0.4 · sleep performance)`, minus an overreach penalty when
yesterday's strain was above 14 on a recovery below 50 — 0 points at strain 14
ramping linearly to 10 at strain 21. Without a night of sleep, readiness is
recovery alone (the weights renormalise); without recovery it is `null`. `state` is
the word under the greeting: `balanced` ≥ 67, `strained` 34–66, `depleted` < 34,
`calibrating` while fewer than 7 nights of baseline exist or there is no readiness.
Both are stored on `daily_scores` (migration 0002); the penalty is in
`details.readiness_penalty`.

**Strain (0–21).** Heart-rate reserve `HRR = (HR − RHR) / (maxHR − RHR)` per sample,
integrated over time (gaps > 5 min credit nothing — the watch was off). Intensity
`i = max(0, HRR − 0.25) / 0.75` so sitting earns nothing; TRIMP per minute is
Banister's `i · 0.64 · e^(1.92 i)`; score `21 · (1 − e^(−TRIMP/80))`. Calibration:
sitting all day ≈ 0, an hour's brisk walk ≈ 2, a 60-min run at 75 % HRR ≈ 14, a
two-hour race effort ≈ 20. Each workout gets its own strain over its window; if a
workout has no HR samples the average HR × duration is used.

**Sleep.** `need = base + min(60, 3 · yesterday's strain) + min(60, 0.3 · debt)`, where
debt is the shortfall over the last 7 nights with 0.7/day decay. `performance =
actual / need`, capped at 100. Stage minutes (deep / REM / core / awake) are kept in
`details`. If a source records only `inbed` (old firmware), 90 % of it counts as asleep.

Everything above is a starting point with sensible constants; the tests in
`algorithms_test.go` pin the calibration so you can change a formula and see what moved.

## Sync protocol (iOS)

1. First launch: HealthKit authorisation for the read types; `enableBackgroundDelivery`
   (hourly) and observer queries on sleep, workouts and HRV.
2. `SyncService.syncNow()` walks every type with an `HKAnchoredObjectQuery`, pages of
   20 000 samples. The first page per type is bounded to the last 90 days. Each page is
   uploaded, **then** its anchor is persisted — a crash mid-sync just re-sends one page,
   and the backend's unique constraints make that harmless.
3. Triggers: app becomes active, pull-to-refresh, HealthKit background delivery,
   `BGAppRefreshTask` (every ≥2 h when iOS allows), Settings → Sync now.
4. "Full re-sync" wipes anchors and re-uploads the window; "Apply & recompute" PATCHes
   `max_hr` / sleep base and queues a 90-day recompute.

## Deployment

`deploy/k8s` is a kustomization: namespace, Postgres 17 (StatefulSet, 10 Gi PVC),
NATS 2.11 with JetStream (2 Gi PVC), the three deployments (distroless, non-root,
read-only FS, probes on `/healthz` + `/readyz`), ClusterIP services, an Ingress that
routes `/v1/sync` → ingest and `/v1` → api. Copy `secrets.example.yaml` → `secrets.yaml`,
set `API_KEY` (`openssl rand -hex 32`) and the Postgres password, `kubectl apply -k`.

Expose it the way you already expose things: a Cloudflare tunnel pointing at the two
ClusterIP services (or at the Ingress) is enough; the API key is the only auth, so
keep TLS on.

## What to build next (in rough order of payoff)

1. **Vault Secrets Operator** for `ryft-secrets` instead of a committed Secret.
2. **Prometheus metrics** (`promhttp` on each service; samples ingested, days scored,
   sync latency) → Grafana dashboard next to your existing stack.
3. **Per-workout detail** screen: HR curve, zone minutes (already in `details`).
4. **Weekly / monthly views** and a strain-vs-recovery scatter (the Whoop "sweet spot").
5. **Multi-user**: per-device API keys mapped to `user_id`, so others can join.
6. **Sleep consistency and journal** (the behaviour-impact feature): a `journal`
   table + correlation against next-day recovery.
7. **Garmin-only metrics** via an optional `garmin-sync` service.
