# RYFT — architecture

RYFT, self-hosted: a watch or ring (a Garmin, in development) feeds Apple Health, an iOS
app ships that data to Go microservices on Kubernetes, and the services turn it into
daily **Readiness**, **Recovery**, **Strain** and **Sleep** scores the app renders. The
HTTP contract with the app is [`API.md`](API.md).

```
 Garmin watch ──BLE──▶ Garmin Connect (iOS) ──▶ Apple Health (HealthKit)
                                                        │
                                           HKAnchoredObjectQuery (incremental)
                                                        ▼
                                              Ryft iOS app (SwiftUI)
                                     POST /v1/sync │            ▲ GET /v1/days, /v1/days/{d}, /v1/me
                                                   ▼            │
   ┌──────────── Kubernetes namespace `ryft` ────────────────────────────────────┐
   │   ingest ──── Postgres ◀──── scoring ──── RabbitMQ ◀───────────── api            │
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
| --- | --- | --- | --- |
| `ingest`  | 8080 | `POST /v1/sync`: validate, idempotent upsert of samples / sleep / workouts, publish `ingested` with the local days touched (through its outbox) | Postgres, RabbitMQ |
| `scoring` | 8080 (health only) | Consume `ingested` and `session.completed` (queue `scoring`); recompute touched days oldest-first, including the day's strength sessions and the workouts carrying their load; every 15 min recompute yesterday + today; publish `scored` (today's first recovery), `session.scored` and `refresh` (a sync's days scored) | Postgres, RabbitMQ |
| `api`     | 8080 | `GET /v1/me`, `PATCH /v1/me`, `GET /v1/days`, `GET /v1/days/{day}`, `GET /v1/sleep`, `GET /v1/workouts[/{id}]`, `GET /v1/trends`, `GET /v1/insights`, `POST /v1/recompute`, Train (`/v1/exercises`, `/v1/plans`, `/v1/sessions`), the journal (`/v1/behaviors`, `/v1/journal/{day}`, `/v1/behaviors/impact`), `GET /v1/config`, `GET /v1/model`, consent and invite (`/v1/me/consent`, `/v1/me/invite`), `POST /v1/feedback`, push registration and preferences — see [`API.md`](API.md); the operator's `/v1/admin/` flags, invites, feedback and users | Postgres, RabbitMQ |
| `notify`  | 8080 (health only) | Consume `scored`, `session.scored` and `refresh` (queue `notify`); send APNs pushes; every 15 min the no-data check | Postgres, RabbitMQ, APNs |

All four are one Go module (`backend/`), one Dockerfile (`--build-arg SERVICE=…`), and
share `internal/` packages. A fourth binary, `migrate`, is the only thing that changes the
schema: it runs once per deploy as the schema owner (a Job, or a one-shot compose
service), applies each migration in a transaction under a transaction-level advisory
lock (safe behind Supabase's pooler), and sets the services' role passwords. The
services connect as least-privilege roles (below) and wait at start-up until the schema
has every migration they were built with.
Each serves `/healthz`, `/readyz` and Prometheus `/metrics` on 8080 — metric
catalogue and dashboard in [`OBSERVABILITY.md`](OBSERVABILITY.md).

**Auth and users.** Multi-user, with Supabase as identity provider and database
([`SUPABASE.md`](SUPABASE.md)). The app signs in with Apple through Supabase and sends
the Supabase access token as the bearer; `internal/auth` verifies it against the
project's JWKS (ES256/RS256, issuer, audience, role, expiry) and puts the principal in
the request context. `store.EnsureUser` then loads the user row — creating it on the
first request, refreshing email/name/provider at most every five minutes — and every
handler reads the user and timezone from the context; there is no global user. `401`
means "this token is not acceptable" (the app refreshes once, then signs out), `503`
means "could not check right now" (JWKS or database unreachable), so an outage never
signs anyone out. `AUTH_MODE=apikey` (shared key, `<key>:<uuid>` to act as any user) is
for compose and `overlays/local-postgres` only and refuses to start without
`ALLOW_INSECURE_APIKEY_AUTH=true`. The scoring service needs no auth; its events carry
the `user_id`, and its 15-minute pass covers every user with a device synced in the
last 48 hours, each in their own timezone.

**Account deletion.** `DELETE /v1/me` (api) erases the user's rows in one transaction
and leaves a tombstone (`deleted_users`), then deletes the Supabase auth user with the
project's secret key, which only api holds. A per-user advisory lock serialises the
deletion against `EnsureUser`'s writes, and the user row is locked first, so syncs in
flight finish before the delete and later ones fail. Tokens of a deleted account get
`401`. A reconciler in each api pod (at start-up, then every 15 minutes) retries failed
Supabase deletions and erases accounts that reappeared, e.g. after a restore.

**Remote config.** `feature_flags` holds one row per flag (`enabled`, `rollout_pct`,
`allow_user_ids`); `GET /v1/config` evaluates all of them for the caller with the pure
`internal/flags` (D69). The operator changes flags through `/v1/admin/flags` with
`ADMIN_API_KEY`, a bearer of its own, not a user token (`OPERATIONS.md` → Feature flags).

**Push (notify).** The app registers its APNs token per install
(`POST /v1/devices/{id}/push`, table `push_tokens`) and its preferences
(`PATCH /v1/me/notifications`, columns on `users`). scoring announces two moments:
`scored` when today first gets a recovery after a night with sleep, and
`session.scored` once a completed session's day is scored, plus `refresh` once a
sync's days are scored (a silent push, at most one per account every 5 minutes, D92). notify consumes all three,
loads what the message needs as `ryft_notify`, checks the category and quiet hours,
claims `(user, day, category)` in `notifications_sent` and sends one APNs request per
device (`internal/apns`, token-based, HTTP/2), with localisation keys the app resolves.
A 15-minute timer sends the no-data reminder. Without an APNs key it counts instead of
sending. Rules: D70–D73, payloads: `API.md` → Push notifications.

**Request path.** Every service wraps its mux in the same chain (`internal/httpx`),
outermost first: **Instrument** (a server-generated request ID in `X-Request-ID`, the log
context and error bodies; one access-log line; the HTTP metrics labelled by route
pattern) → **Recover** (a panic becomes a JSON `500`, counted and logged with its stack)
→ **SecurityHeaders** (`no-store`, `nosniff`, no framing, no referrer, HSTS) → mux.
`/v1/*` then goes through **auth** (verify the token → per-user token-bucket rate limit
→ `EnsureUser`) → **Timeout** (15 s for api, 50 s for sync, as a context deadline the
database calls honour; running out answers `503`) → the route. Bodies are read with a
size limit (64 KiB, 256 KiB for a Train session or plan, 32 MiB for sync; over → `413`), and unexpected errors answer a
generic `500` with the request ID while the details go to the log. Logs are JSON with
`request_id`, `user_id` and `route` on every line of a request (`internal/logx`).

**Events.** RabbitMQ (D97), which replaced NATS JetStream in four releases (D102).
`internal/events` is the transport-neutral interface (`events.Bus`), `amqpbus` the
transport, `internal/eventbus` opens it from the configuration. Five events (routing keys on topic exchange `ryft.events`
in vhost `ryft`): `ingested` (`{user_id, days[], reason, request_id}`; `request_id`: the
request that caused it, for log correlation) and `session.completed` (`{user_id,
session_id, day, request_id}`, handled like an ingest of that day) go to queue
`scoring`; `scored` (`{user_id, day, recovery, readiness, state, sleep_performance,
sleep_min, request_id}`), `session.scored` (`{user_id, session_id, day, request_id}`)
and `refresh` (`{user_id, day, request_id}`) to queue `notify` (D71). `reason:
"recompute"` with two days means an inclusive range. Every event is first a row of the
publisher's **outbox** in Postgres, in the transaction of the write it announces where
that matters (a sync's days, a session's completion), and the service's relay sends it
with the broker's confirm (D101): a broker outage delays events, it loses none.
Quorum queues on three nodes; a failure is retried after 5 s, 30 s and 5 minutes (TTL
queues), then dead-lettered to `dlq.scoring`/`dlq.notify` (14 days; OPERATIONS →
RabbitMQ); notify's quiet hours wait in `notify.wait.*`. Delivery is at least once and
every handler idempotent (D100). Every service is its own broker user without
configure rights; write rights per exchange and, by topic permission, per routing key
(ingest only `ingested`, api `ingested` and `session.completed`, scoring its three),
read rights only on its own queue; mutual TLS with the internal CA (D98, D99). The
topology is Go (`amqpbus/topology.go`), generated into the Topology Operator's objects
and compose's definitions; `amqpbus/permissions_test.go` proves the rights against a
broker. Design: [`MESSAGING_DESIGN.md`](MESSAGING_DESIGN.md). If the broker is down,
the services keep serving, the outbox keeps the events, and scoring's 15-minute timer
catches up anyway.

## Data model (Postgres)

- `users` — `id` = Supabase user ID; `name`, `email`, `provider` from the token; `tz` (the local day boundary), `max_hr` (for heart-rate reserve) with `max_hr_source` (`default`, `estimated` from `birth_year`, `manual`), `sleep_base_min` with `sleep_base_source` (`default`, `learned`, `manual`), `sleep_base_manual` and the weekly estimate `sleep_base_learned_min` / `_at` (scoring may write only these baseline columns); `last_seen_at`, `recompute_requested_at`.
- `deleted_users` — tombstones (UUID only) of deleted accounts.
- `devices` — one row per user and app install (`(user_id, id)`), `last_sync_at`, the `app_version` and `os_version` the app last sent.
- `consents` — every grant and withdrawal of consent to health-data processing, with the policy and app version (D78); `beta_invite_codes` — invite codes with their uses (D79); `users.beta_access_at` / `beta_invite_code`.
- `feedback` — reports from the app with versions, request ids and diagnostics; `resolved_at` is set by the operator (D83). Deleted with the account.
- `score_queue` — the days a sync queued for scoring, and when scoring finished each (`GET /v1/me/sync` → `pending_days`, the sync-to-score latency; D91). `users.resync_requested_at`: the operator's request for a full re-sync.
- `admin_audit` — every authorized admin call: route, status, request id, key fingerprint, target user, details (D88). Append-only, no foreign key on the user.
- `devices.last_sync_summary` (what the last sync carried) and `push_tokens.last_success_at` (the last delivered push) feed the admin user view (D89).
- `user_sources` — per user, HealthKit source name and kind (`hr`, `steps`, `sleep`, …): the bundle id and the latest record's time. Ingest keeps it on every sync; `User.sources` and the `no-heart-rate-source` card read it (D82).
- `samples(user_id, type, start_at, end_at, value, unit, source)` — the raw time series. `type` is a closed vocabulary: `heart_rate, hrv_sdnn, resting_hr, respiratory_rate, spo2, vo2max, active_energy, steps`. Unique on `(user, type, start, end, source)` → re-uploads are no-ops.
- `sleep_segments` — HealthKit sleep analysis as-is: `inbed, asleep, core, deep, rem, awake`.
- `workouts` — one row per user and HKWorkout (`(user_id, id)`, `id` = HK UUID), with `strain`, `trimp` and `zone_minutes` filled by scoring.
- `daily_scores(user_id, day)` — the product: readiness + state, recovery, strain, baselines and `baseline_days`, sleep performance / need / actual / debt, overnight HRV, RHR, respiratory rate, SpO2, daily `steps` and `active_kcal` (migration 0008), `muscular_trimp`, `strength_min`, `volume_kg` (0011), `calibrating`, and a `details` JSONB with the breakdown the UI can show.
- **Journal** (0016, D74–D77): `behaviors` (built-ins with `user_id NULL` and fixed ids from the seeds, custom rows per user), `user_behaviors(user_id, behavior_id)` (the user's choice over `default_on`), `journal_days(user_id, day)` (a saved day) and `journal_entries(user_id, day, behavior_id)` with `value_bool` and `value_num`. The impact on the morning after is computed on request (`internal/behavior`), not stored.
- `feature_flags(key)` — remote config (0014): `enabled`, `rollout_pct`, `allow_user_ids`.
- `push_tokens(user_id, device_id)` — the app's APNs token per install (unique), `env`, `locale` (0015); `notifications_sent(user_id, day, category)` — what notify sent, claimed before sending. `users.notify_categories` (off switches), `quiet_from` / `quiet_to`.
- **Train** (migrations 0011–0012, D58–D63): `exercises` and `plans` (built-ins with `user_id NULL` from the seeds, custom rows per user), `plan_days`, `plan_exercises`, `plan_enrollments` (one active per user), `sessions` → `session_exercises` → `session_sets` keyed by `(user_id, id)` with client-generated ids, `personal_records(user_id, exercise_id, kind)`. `workouts.muscular_load` / `session_id` say which workout carries a session's load.

**Database roles** (migration 0009; `internal/db/roles_test.go` pins the matrix):

| Role | May |
| --- | --- |
| `ryft_ingest` | read/insert/update `users`, `devices`, `samples`, `sleep_segments`, `workouts`; read `deleted_users.id` only |
| `ryft_scoring` | read the raw tables, exercises and sessions; insert/update `daily_scores`; update only `strain`, `trimp`, `zone_minutes`, `muscular_load`, `session_id` of `workouts`, and create and delete only its own (`ryft-session`); update only the load columns of `sessions` |
| `ryft_api` | read everything; insert/update/delete `users`; delete the user's rows (account deletion); insert/update `deleted_users`; all of `apple_tokens`, which no other role can read; all of the Train tables, the journal tables, `feature_flags` and `push_tokens`; read and delete `notifications_sent` |
| `ryft_notify` | read a user's `id`, `tz` and notification settings only; read and delete `push_tokens`; all of `notifications_sent`; read `devices.last_sync_at`, `samples` (user, type, start), the morning's score columns, a session's title, status and load, `personal_records` and exercise names (migration 0015) |

None of them may create, alter or drop anything, or truncate. Every new table must grant
the roles what they need in the same migration, and the grants test must be extended.

A "day" is a local calendar day in `users.tz`. The night that *ends* on day D belongs to
D (sleep ending between D-1 20:00 and D 13:00), so the morning's recovery and the
day's strain line up.

## Scoring (`internal/scoring/algorithms.go`, unit-tested)

**Sources.** Before anything is computed, overlapping HealthKit sources are reduced to
one: per hour for heart rate, steps and active energy, and per night for sleep. See
[`SCORING.md`](SCORING.md).

**Baselines.** Each overnight signal is compared to your own previous 30 scored days.
HRV is compared in log space (it is log-normally distributed). With fewer than 7
days the row is flagged `calibrating` and a population SD is used instead of yours.
The HRV (geometric mean) and resting-HR baselines each day was scored against are
stored on its row (`hrv_baseline_ms`, `rhr_baseline`; migration 0005), so trends and
insights compare against exactly what recovery used.

**Trends.** `GET /v1/trends` reads one `daily_scores` column over a 7/30/90-day window
(metric → column is a whitelist in `store.Trend`) plus, for HRV and resting HR, the
latest stored baseline. No extra tables or rollups: 90 rows per request.

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

**Strain detail.** Integration also yields minutes in five HRR zones (50–60, 60–70,
70–80, 80–90, ≥ 90 %). Scoring stores zones + TRIMP on each workout row and on the
day (`zone_minutes` jsonb, `trimp`, `hr_min`; migration 0004), so
`GET /v1/workouts/{id}` only has to load the heart-rate samples in the window and
down-sample them to ≤ 600 points (LTTB, `scoring.DownsampleHR`). A workout is scored on
the samples of its own window, so one that runs past midnight counts in full on the
day it started; the day's strain counts only that day's samples.

**Sleep.** `need = base + min(60, 3 · yesterday's strain) + min(60, 0.3 · debt) −
yesterday's naps (≤ 90)`, each term stored on `daily_scores`, where debt is the shortfall
over the last 7 nights with 0.7/day decay. `performance = actual / need`, capped at 100.
If a source records only `inbed` (old firmware), 90 % of it counts as asleep. The night
is the longest block of sleep in its window; other blocks ending from 10:00, and sleep
ending 13:00–20:00, are naps. `base` is learned weekly from good nights unless the user
set it. Details, quality and consistency: [`SCORING.md`](SCORING.md) → Sleep.

**Sleep screen.** Scoring stores the night's typed totals on `daily_scores`
(`sleep_start`, `sleep_end`, `in_bed_min`, `deep_min`, `light_min` = HealthKit core,
`rem_min`, `awake_min`, `sleep_has_stages`; migration 0003). The api assembles the
views on read with pure functions in `internal/scoring/sleep.go`: efficiency =
asleep / in bed, stage percentages (deep/light/rem sum to exactly 100 by largest
remainder, awake is a share of time in bed), quality = 0.6 · performance + 0.2 ·
efficiency + 0.2 · restorative (stored by scoring) with labels good ≥ 80 / fair ≥ 60 /
poor, and the hypnogram from the night's raw `sleep_segments` without its naps.
`GET /v1/sleep/plan` turns tonight's need into bedtimes. `GET /v1/sleep` averages the stored nights of a 7- or 30-day window.

**Insights.** `internal/insights` is a list of pure rules, each
`func(History) *model.Insight`, over a contiguous 30-day calendar ending today (days
without a score are empty, so "consecutive nights" means calendar nights). Shared
helpers handle the fiddly parts: `Latest` (today, or yesterday before today's sync),
`Streak` (consecutive days ending at the latest day with data). Thresholds sit together
at the top of `rules.go`. The api computes the cards on read and caches them in memory
for 5 minutes per local day; with several api replicas each keeps its own cache.

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

## Namespaces and trust

Every workload is its own security domain: a Kubernetes namespace, a service account, a
Vault namespace with its own Kubernetes auth mount, and credentials usable only for
what that workload does. No credential is shared, and none is broader than its user.

```text
              Internet ──TLS── Traefik (kube-system)
                                 │ /v1/sync          │ /v1
                        ┌────────▼───────┐  ┌────────▼───────┐
                        │ ryft-ingest    │  │ ryft-api       │      ryft-scoring
                        │ SA ingest      │  │ SA api         │      SA scoring
                        │ PG ryft_ingest │  │ PG ryft_api    │      PG ryft_scoring
                        └───┬────────┬───┘  └──┬──────────┬──┘      ┌───┬─────┐
              AMQP user ingest│      │443/5432 │AMQP user api│      │   │5432 │
                    mTLS 5671 ┌─▼──────┴─────────▼──┐          │      │   │     │
                            │ ryft-rabbitmq (3)   │◀─────────┼──────┘   │     │
                            └─────────────────────┘          │ AMQP user scoring
                                                             ▼          ▼
            ryft-migrate (Job, owner) ──5432──▶  Supabase: Supavisor pooler + Auth (public)

  ryft-notify (SA notify, PG ryft_notify, AMQP user notify) ──443──▶ APNs; 5432 ▶ pooler

  Vault (HCP): admin/ryft/{ingest,scoring,api,notify,rabbitmq,migrate,smoke}, each with auth/kubernetes
  bound to its namespace + service account, kv/config = that workload's keys only.
  The Vault Secrets Operator logs in per namespace and writes Secret ryft-secrets there.
```

- **Identity:** each pod runs as its own service account, without an automounted
  Kubernetes token (no workload calls the Kubernetes API). The operator requests
  short-lived, audience-`vault` tokens for that account and logs into that namespace's
  Vault mount. Vault confirms each token through the cluster's TokenReview. A role is
  bound to exactly one namespace and one service account.
- **Secrets:** `infra/vault` generates the database and broker passwords and writes each
  one only into the namespaces that need it (`infra/vault/README.md`); the broker's
  users get theirs in `ryft-rabbitmq` (`amqp-user-<service>`, for the Topology Operator).
- **Data:** each service connects as its own Postgres role (Database roles above). Only
  the migrate Job holds the owner.
- **Messaging:** each service is its own RabbitMQ user without configure rights, writing
  only its routing keys and reading only its queue, over mutual TLS with a certificate
  of its own from the internal CA (Events above, D98, D99).
- **Network:** default deny for ingress and egress in every namespace, then exactly
  the flows in the drawing (`OPERATIONS.md` → Network policy). Internet egress is
  limited to public addresses on 443/5432. Traefik limits each client IP before any
  token is checked. `/metrics` has its own port (9090), so the metrics collector can be
  let in without reaching the API port.
- **Outside the RYFT namespaces:**
  - `monitoring` runs Grafana Cloud's collector, privileged and steered from Grafana
    Cloud (D51). It gets metrics only; RYFT's logs go to CloudWatch (`amazon-cloudwatch`,
    Fluent Bit), KMS-encrypted and kept 14 days (D52).
  - `ryft-web` serves the apex site and `apple-app-site-association`, without secrets:
    the `ryft-web` image from `timkrebs/ryft-web` (pages, legal texts, nginx config,
    AASA), pinned in `overlays/eks` (D106, OPERATIONS → Apex site).
  - Two operator UIs are public through Traefik, each behind its own login, a per-IP
    rate limit and HSTS: Argo CD at `argocd.ryftapp.de` (`argocd`) and the RabbitMQ
    management UI at `rabbitmq.ryftapp.de` (Traefik → the nodes' 15671, verified
    against the internal CA). No IP allow-list or SSO yet (D103).
- **Pods:** Pod Security `restricted` on every namespace. Everything runs as non-root
  with a read-only root filesystem (the in-cluster Postgres of `local-postgres`
  excepted), no privilege escalation, all capabilities dropped and seccomp
  `RuntimeDefault`.

Not covered (yet): mTLS between Traefik and the services (inside the cluster that hop is
plain HTTP), image signing and admission policy, and per-user row-level security inside
the services' own roles. The services scope every query by user themselves.

## Deployment

`deploy/k8s/base/<service>` holds namespace-neutral manifests for each workload. The
four services are distroless, non-root, with a read-only FS, startup/readiness/liveness
probes and Prometheus annotations. RabbitMQ 4.3 runs as three nodes (the Cluster
Operator's `RabbitmqCluster`, 10 Gi each, `deploy/rabbitmq/cluster`), its exchanges,
queues, policies, users and rights as the Topology Operator's objects
(`deploy/rabbitmq/topology`, generated). migrate is a one-shot Job. api and ingest have their own Ingress
(`/v1` and `/v1/sync` on one host), HPA (2–4) and PDB; scoring and notify run one
replica each. `deploy/k8s/services/<service>`
puts each into its namespace. `overlays/eks` composes the services on EKS, with
Secrets from Vault through the operator; `deploy/rabbitmq` holds the broker (with
`deploy/rabbitmq/operators`).
`overlays/local-postgres` composes them with an in-cluster Postgres, a nightly
`pg_dump` CronJob and generated per-namespace Secrets.

**GitOps.** Argo CD (an add-on from `infra/aws/kubernetes`) syncs `deploy/argocd/apps`,
one application per unit in waves: the Vault objects of each namespace, the broker,
then `ryft-platform`, whose every sync runs migrate first (PreSync hook) and the
in-cluster smoke last (PostSync, namespace `ryft-smoke`). CI promotes images by a PR
that pins `overlays/eks`; its merge is the deploy, a revert the rollback (D94–D96).

```
push to main ─► CI: tests, images <sha> ─► promote PR "deploy: images <sha>" ─► merge
                                                                               │
Argo CD ◄──── git (main) ◄─────────────────────────────────────────────────────┘
  wave 0  vault-<svc>      VaultConnection/Auth/StaticSecret → Secret ryft-secrets
  wave 1  rabbitmq-operators  Cluster + Topology Operator (CRDs, rabbitmq-system)
  wave 2  broker-rabbitmq  RabbitmqCluster, topology, client certificates
  wave 3  ryft-platform    PreSync migrate → services, ingresses, policies → PostSync smoke
```

Step by step, including restore: [`OPERATIONS.md`](OPERATIONS.md).

## What to build next (in rough order of payoff)

1. **HealthKit source de-duplication.** Samples and sleep from several sources (iPhone +
   Garmin) are summed as-is; pick one source per type and night (HealthKit's own
   priority order) before scoring. Prerequisite for the next item.
2. **Calories and steps on the Strain screen** (`active_energy`, `steps` are ingested but
   not aggregated yet).
3. **Strain-vs-recovery "sweet spot"**: a recommended strain target for the day from
   today's recovery.
4. **Multi-user**: per-device API keys mapped to `user_id`, so others can join.
5. **Journal** (the behaviour-impact feature): a `journal` table + correlation against
   next-day recovery, surfaced as more insight rules.
6. **Garmin-only metrics** via an optional `garmin-sync` service.
