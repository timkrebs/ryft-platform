# Decisions and discrepancies

Where the engagement prompt (`CLAUDE_PROMPT.md`) and the code disagreed, the prompt
won and the difference is recorded here. Judgement calls the prompt left open are
recorded too, so the iOS side and future changes can see why a number is what it is.

## D1 — Container runtime: Docker or Podman

The Makefile called `docker compose` / `docker build` unconditionally. The dev machine
this was built on has Podman only. `make` now uses `docker` when it is on `PATH`,
otherwise `podman` (`podman compose` runs the same `docker-compose.yml`). Override with
`make up CONTAINER=docker` or `COMPOSE="docker compose"`.

## D2 — Stale paths to the iOS app

`model.go` and the README pointed at `ios/Ryft/...` and `../app/RYFT/...`; the app
lives in `../app/Ryft/`. Comments and README fixed, no behaviour change.

## D3 — Readiness penalty shape and missing inputs

The prompt fixes the formula (`round(0.6·recovery + 0.4·sleep)`, "subtract up to 10
points when strain > 14 with recovery < 50") but not how the penalty scales. It ramps
linearly with yesterday's strain: 0 at 14, 10 at 21 (`ReadinessPenalty`), and is only
applied while today's recovery is below 50. "Recovery" is today's recovery — the only
one the prescribed signature `Readiness(recovery, sleepPerf, yesterdayStrain)` receives.

With no sleep recorded last night, readiness uses recovery for both terms (the same
renormalisation `Recovery` does for missing signals). With no recovery it is `null`.

## D4 — `state` is stored, and "calibrating" covers "no score"

`state` is computed at scoring time by `scoring.ReadinessState` and stored next to
`readiness` (migration 0002), so every reader gets the same value without importing
the thresholds. It is `calibrating` whenever the existing `calibrating` flag is set
(fewer than 7 nights of baseline) **or** readiness is `null` — the enum has no
separate "no data" value, and the app should show the calibrating copy in both cases.

## D5 — Sleep quality labels: good ≥ 80, fair ≥ 60

The prompt asks for `good | fair | poor` without thresholds, and its example (and the
brand board) shows quality 82 as "good" — so a stricter 85/70 split (good ≥ 85) cannot be used. The
bands are good ≥ 80, fair 60–79, poor < 60 (`scoring.SleepQualityLabel`).

## D6 — Stage percentages: staged sleep for deep/light/rem, time in bed for awake

The example (`deep 112 / light 261 / rem 95 / awake 10`, total 468, in bed 492 →
24 / 56 / 20 / 2 %) only works if deep/light/rem are shares of sleep and awake is a
share of time in bed. Deep/light/rem use the largest-remainder method so they always
sum to exactly 100. `stages` always has the four entries in fixed order; when the
source has no stages they are 0 min / 0 % and `has_stages` is false.

## D7 — Sleep additions beyond the prompt's shapes (additive)

- `DayDetail.sleep.segments`: the stage timeline. The brand board's Sleep screen
  draws a hypnogram bar, which the app cannot render from totals.
- `SleepRange` carries `from`, `to`, `nights`, `avg_in_bed_min`, `avg_efficiency` and
  `avg_stages` (what the Week/Month tabs show next to the total), and each day carries
  the full night summary, not only totals and stages.
- `GET /v1/sleep` accepts `to=YYYY-MM-DD` to page back, default today; `range`
  defaults to `week`.
- `days` lists only nights with data; the app fills gaps from `from`/`to`. This avoids
  inventing a "no data" encoding for every numeric field.

## D8 — Typed sleep columns instead of reading `details`

Night totals moved into columns on `daily_scores` (migration 0003) rather than being
parsed out of the free-form `details` JSON, which stays as it was for compatibility.
There is no SQL backfill — old rows get the columns on their next recompute, and the
upgrade procedure in `docs/STATUS.md` recomputes 90 days anyway. All timestamps now
scan from Postgres as UTC (`db.Connect`), so the API emits UTC as the contract says.

## D9 — Workout detail: integers on the wire, app values win

- `hr[].bpm`, `zone_min[]` and `trimp` are rounded integers, as in the prompt's example
  (`"bpm":152`, `"zone_min":[0,5,20,30,2]`, `"trimp":77`); storage keeps one decimal.
- `avg_hr` / `max_hr` stay what the app sent (HealthKit's workout statistics). Only
  when they are missing does the detail derive them from the samples in the window.
  The list endpoint is unchanged.
- Down-sampling uses Largest-Triangle-Three-Buckets rather than bucket means: a
  sprint peak is what the user looks for on the curve, and means flatten it.
- A workout scored from `avg_hr` alone (no samples) credits its whole duration to the
  average's zone, so `zone_min` is never misleadingly empty for it.

## D10 — `DayDetail.strain` (additive, not in the prompt)

The Strain screen needs the day's load, not only per-workout load. The day's TRIMP,
minutes of heart-rate data and zone minutes were already computed but only reachable
through the untyped `details`. They are now stored typed (migration 0004, together
with the workout columns) and served as `DayDetail.strain`.

## D11 — Trend baselines are stored, not recomputed on read

"The 30-day baseline for hrv/rhr" is taken to mean the baseline recovery scores
against: up to 30 scored days *before* the day, HRV as a geometric mean because the
model compares HRV in log space. Scoring already computes it, so it is stored per day
(migration 0005) — new columns, no new tables, as the prompt requires. The insight
rules need the same per-night baseline, and storing it keeps all three consumers
(recovery, trends, insights) on one definition. The two baselines are also exposed on
`DailyScore` (`hrv_baseline_ms`, `rhr_baseline`) so the Today tiles can show "vs. your
baseline" (additive).

## D12 — Trend metric `sleep` is sleep performance; `sleep_min` added

The prompt lists `sleep` without a unit. Since the other trend metrics are scores, and
the Today ring shows sleep as a percentage, `sleep` is `sleep_performance` (%).
`sleep_min` (minutes asleep) is added for duration charts. Points are sparse (days
without a value are left out, not sent as `null`), and `range` defaults to `7d` with an
optional `to` like `/v1/sleep`.

## D13 — Insight rule interpretations

- **"Bedtime variance > 90 min"** is read as *spread* (latest − earliest bedtime over the
  last 7 nights), not statistical variance (minutes², meaningless as a threshold) or
  standard deviation (a 90-min SD essentially never happens to non-shift-workers, so
  the card would never fire). Bedtimes are measured on a noon-to-noon axis so 23:30 and
  00:30 are an hour apart. At least 5 of the 7 nights must have data. Switching to SD
  is a change inside `bedtimeConsistency`.
- **Freshness.** "Today's" value is today's, or yesterday's when today has not synced
  yet; older values never produce a card, so a streak that ended days ago stays silent.
- **Consecutive** means consecutive calendar nights; a night without data ends a streak.
- **Calibrating.** Baseline-relative rules (HRV low, RHR up, best HRV) are silent while
  the day is `calibrating` — a 3-night baseline makes "below baseline" noise.
- **Three hard days without a green recovery** counts strain ≥ 14 days in the last 14,
  resetting on any recovery ≥ 67. A day's recovery is its morning, so a green morning
  followed by a hard day starts a new run that includes that day.
- **Sleep debt** uses `DailyScore.sleep_debt_min` (debt carried into last night) so the
  card and the Today screen show the same number.
- **Readiness trend up** = least-squares slope ≥ 1 point/day over the last 7 days on ≥ 5
  days, so one good morning after a bad one does not count as a trend.
- **History window** is 30 days, not the prompt's 14: "best HRV in 30 days" needs 30. The
  other rules only look at the last 14 or fewer days.
- Cards carry the extra `day` field the prompt's `Insight` example shows; `metric` is a
  `/v1/trends` metric so the app can deep-link.

## D14 — Metrics: route patterns, one package, pinned client_golang

- HTTP metrics label by the ServeMux pattern (`r.Pattern`, set by the nested mux behind
  `BearerAuth`), never the path, so day and UUID paths cannot explode cardinality. The
  prompt's "sync requests and latency" are these metrics with `route="POST /v1/sync"`
  rather than a second, sync-only pair.
- All metrics live in `internal/metrics`; service-specific ones are vectors, so a
  service only exports what it records.
- Added beyond the prompt: `ryft_last_sync_timestamp_seconds` (the "is my phone still
  syncing?" alert) and `ryft_events_published_total` (ingest swallows publish errors by
  design — this makes them visible).
- `prometheus/client_golang` is pinned to v1.23.2: v1.25 requires `go 1.26`, which would
  break the `golang:1.25` build image and the module's Go 1.24 floor.

## D15 — Manifests split into `base/` plus two overlays

The prompt wants `deploy/k8s/vault/` "as the alternative to `secrets.yaml`", but a
kustomize overlay cannot remove a resource from its base, and the old flat
`deploy/k8s/kustomization.yaml` listed `secrets.yaml` directly. Everything except the
Secret moved to `deploy/k8s/base/` (history kept with `git mv`); `deploy/k8s` is now the
default overlay (base + `secrets.yaml`), so `kubectl apply -k deploy/k8s` works exactly
as before, and `deploy/k8s/vault` is base + VaultStaticSecret. Image tags are now set in
`base/kustomization.yaml`.

## D16 — NetworkPolicy exceptions beyond "only the Ingress / only the services"

- Prometheus (namespace `monitoring`) may reach :8080 on the three services; otherwise
  the 2.6 metrics could not be scraped.
- The backup CronJob (and the documented restore pod), labelled `app: pg-backup`, may
  reach Postgres.
- Both Traefik (k3s default) and ingress-nginx are allowed out of the box, since the
  Ingress comment names both; a Cloudflare tunnel peer is provided commented out.
- *(Superseded in part by D185: Traefik and the smoke hook only; ingress-nginx removed.)*

## D17 — HPA owns replicas; scoring stays single

- **Amended by D218:** on Pub/Sub, "one consumer" becomes "scoring max-instances 1, one message in flight per user".
  The ordering key `user_id` on scoring's two topics holds a user's later messages until the current one is settled,
  so scoring can run with concurrency 4. The 15-minute timer is a tick step (D219).

api and ingest have no `replicas` in their Deployments (the HPA, min 2 / max 4, owns
it); otherwise every `kubectl apply` would reset them to 1. `minAvailable: 1` PDBs plus a
minimum of 2 replicas means drains never block. scoring is deliberately left at one
replica without HPA/PDB: it runs the 15-minute timer and recompute ranges must be
scored oldest-first by one consumer. Drains proceed without manual steps only on
multi-node clusters; on a single node the PDB blocks the second pod (see
`OPERATIONS.md`).

## D18 — CI does more than the prompt lists

- Also runs on `pull_request` (tests and checks only; images are pushed from `push`
  events, where the token can write packages).
- Two extra gates before images are pushed: `make k8s-validate` (the manifests are
  applied by hand, so CI is the only check they get) and `make up && make smoke` on
  Docker — the prompt calls smoke "the acceptance test", so images should not ship
  without it. `smoke.sh` now waits for `/readyz` so `make up && make smoke` is not racy.
- Images are multi-arch (amd64 + arm64): the Dockerfile's build stage now
  cross-compiles on `$BUILDPLATFORM` (`GOOS/GOARCH` from `TARGETOS/TARGETARCH`), so this
  costs no emulation. Plain `docker build` / `podman build` behave as before.
- Every push gets a short-SHA tag; `latest` only on the default branch, as specified.
  *(Since D107: only pushes to main build images, and none is tagged `latest`.)*
- `.golangci.yml` (v2 format) sits at the repo root; golangci-lint finds it from
  `backend/`. Default `standard` set plus bodyclose, copyloopvar, errorlint, gosec,
  misspell, nilerr, noctx, nolintlint, sqlclosecheck, unconvert, usestdlibvars, and
  gofmt/goimports. The handful of findings it raised were fixed rather than excluded.

## D19 — Error shape everywhere; 404 for wrong methods; stricter ranges

ServeMux's own 404/405 responses are plain text, which broke the `{"error": …}`
contract. A JSON catch-all now answers unknown routes on every service; a wrong method
on a known path is therefore `404`, not `405` (the prompt's status list has no 405).
Three inputs that used to "succeed" now fail fast with `422`: `from > to` on
`/v1/days` and `/v1/workouts` (was `200 []`), and `POST /v1/recompute` over 400 days
(was `202`, after which scoring rejected the event ten times and dropped it).

## D20 — `baseline_days` typed; `details` keys frozen

The iOS app (built in parallel from its own copy of the contract) reads several
`details` keys — `baseline_days` for "N nights to go", `zone_minutes`, `trimp`,
`sleep_start`/`sleep_end`. `details` stays free-form, but those keys are now documented
as frozen, and the calibration progress is a typed field: `baseline_days` =
min(HRV days, resting-HR days) in the baseline window, so `calibrating ⇔
baseline_days < 7` exactly (the `details` value counts HRV days only).

## D21 — The contract is checked from Swift too

`API.md` carries a Swift mirror of every response type. `scripts/check_swift.sh`
(`make check-swift`, macOS) compiles that block straight out of the doc and decodes every
endpoint of the running stack with the app's decoder settings, then compiles the app's
*current* models from `../app` and checks they decode every endpoint the app calls
without silently dropping data (the app's decoders use `try?`). Not in CI: it needs a
Swift toolchain and the app checkout.

Found while doing this: the app's `SleepMath.percent` treats any value ≤ 1 as a 0–1
fraction, so a stage share of 1 % (common for awake time) would render as 100 %. The
API sends whole percentages only; `API.md` now says so explicitly. The fix belongs in
the app.

## D22 — Fixes from an independent review of the change set

A second-pass review of `e4ca28c..HEAD` found nothing that crashes, loses data or breaks
a deploy, and these wrong-result bugs, all fixed:

- Workouts crossing local midnight were scored only on the samples before midnight,
  because scoring filtered the day's samples. Each workout now loads its own window
  (the same one `GET /v1/workouts/{id}` draws); `check_api.py` syncs a 23:20–00:40 run
  and fails without the fix (39 of 80 zone minutes).
- The api counted the 400-day recompute limit in UTC hours, scoring in local hours, so
  a range spanning a DST change could pass the api and fail ten times in scoring. Both
  now use `scoring.RangeDays` (calendar days).
- `DayDetail.strain` was `null` for a day with under 30 s of HR data although
  `score.strain` was `0.0`.
- `PATCH /v1/me` reported database failures as `422`; the timezone is now validated up
  front and store errors are `500`.
- The PDB documentation claimed drains never block; that only holds on multi-node clusters.

## D23 — `ryftapp.de` lives in Route 53, in its own long-lived root

GoDaddy stays the registrar; the whole zone is delegated to Route 53 (the GoDaddy zone
served only its parking page — apex A and `www`, no MX/TXT, no DNSSEC — so nothing had to
be migrated). Every future subdomain is then a Terraform or ExternalDNS change, not a
manual GoDaddy edit. The zone is its own root (`infra/aws/dns`, `prevent_destroy`) so
destroying the cluster to save money never recreates it — a new zone means new
nameservers and another registrar change. CAA allows only Let's Encrypt.

## D24 — Traefik + cert-manager with DNS-01, HTTPS only

As `CLAUDE_FOLLOWUP_EKS_GOLIVE.md` planned (and the user chose over an ALB + ACM edge):
Traefik behind an NLB, certificates from Let's Encrypt via cert-manager. Two deviations
from that note:
- **DNS-01 instead of HTTP-01.** The zone is in Route 53, so no solver pods, no port 80
  and no extra NetworkPolicy rule; certificates can be issued before the API is public.
- **Port 80 is not exposed.** Without a plain-HTTP listener a misconfigured client can
  never send its bearer token in cleartext before a redirect. The app uses `https://`.
Traefik runs in `kube-system`, which the existing `ryft-allow-ingress-controller` policy
already admits, so `deploy/k8s/base` did not change. The NLB comes from the AWS Load
Balancer Controller (`aws-load-balancer-type: external`, IP targets).

## D25 — The edge add-ons are a Terraform root, not a Helm runbook

The go-live note listed `helm install` commands for `docs/OPERATIONS.md`; the user asked
for Terraform. They live in `infra/aws/kubernetes`, a separate root that reads the cluster
by name — the Helm/Kubernetes providers must not be configured from a cluster created in
the same apply. Pod Identity roles are created there too, next to the releases that use
them. ClusterIssuers come from a small local chart because `kubernetes_manifest` needs the
cert-manager CRDs at plan time. Chart versions are pinned in one `locals` block.
Prometheus (go-live step 3.3) is not part of this root yet. Per `CLAUDE.md`, all three
roots are applied by the user; nothing here was applied by the agent.

## D26 — Encrypted default StorageClass; the add-on's default is removed

EBS encryption-by-default is off in the AWS account, and an account-wide setting is not
the platform's to change. The EBS CSI add-on's own default class
(`ebs-csi-default-sc`) would create unencrypted volumes for health data, so it is turned
off and `infra/aws/kubernetes` defines `gp3-encrypted` (default, `Retain`). The EKS
overlay names it explicitly on every `volumeClaimTemplate` and PVC because those fields
are immutable. `Retain` means deleted PVCs leave volumes behind; the teardown in
`infra/aws/README.md` lists them.

## D27 — Overlay layout and secrets for EKS

`deploy/k8s/overlays/eks` (the path `CLAUDE.md` names) keeps everything EKS-specific;
`base/` is unchanged apart from a comment. Its `secrets.yaml` sits next to it because
kustomize refuses files outside the overlay root, hence `.gitignore` now covers
`deploy/k8s/**/secrets.yaml`. Image tags are a deliberate placeholder
(`replace-with-ci-sha`) until CI has pushed SHA-tagged images: applying earlier fails
loudly instead of running `latest`. `make k8s-validate` borrows `secrets.example.yaml` to
render the overlay when no real secret exists.

## D28 — Supabase for Postgres and Auth; the services stay the only data API

As `CLAUDE_FOLLOWUP_SUPABASE.md` decided: the Supabase project is the datastore, Supabase
Auth (Sign in with Apple) replaces the shared `API_KEY`, and the app never talks to
Supabase's Data API. Migration 0007 enables RLS with own-row policies and revokes all
table, sequence and function privileges from `anon`, `authenticated` and `service_role`
(also for future tables), guarded so plain Postgres still migrates. The services connect
as `postgres`, the table owner, which bypasses RLS. `AUTH_MODE=apikey` survives for
compose and `overlays/local-postgres` only and refuses to start without
`ALLOW_INSECURE_APIKEY_AUTH=true`.

## D29 — JWKS refreshed every 10 minutes; 401 only for bad tokens

The spec said "refreshed hourly". Supabase's docs say the JWKS is edge-cached for 10
minutes and clients should not cache longer (revoked keys must stop working), so the
refresh interval is 10 minutes, plus an on-demand refresh at most once a minute for an
unknown key ID. The app refreshes its session once on a `401` and signs the user out if
the retry fails, so every failure that is not the token's fault — no keys loaded, the
JWKS unreachable during an unknown-key lookup, the refresh rate limit, a database error
in the user lookup — answers `503` with `Retry-After` instead. Tokens must also carry
`role = authenticated` and not be anonymous (the anon key is itself a JWT).

## D30 — keyfunc v3.7.0, not the latest

keyfunc v3.8.x and the latest `golang.org/x/time` require Go ≥ 1.25; v3.7.0 (jwkset
v0.11.0, x/time v0.9.0) keeps the module's Go 1.24 floor (as D14 did for
client_golang). `golang-jwt/jwt` v5.3.1 needs Go 1.21.

## D31 — Multi-user schema and scoping details

- `devices` and `workouts` are keyed by `(user_id, id)`: both IDs come from the phone,
  and one iPhone can be signed into by two accounts (`check_api.py` reuses a device and
  a workout UUID across users).
- `EnsureUser` reads the user row on every request (replacing the old per-request
  `GetUser`) and writes only when it is missing, older than five minutes or the token's
  profile changed — no write per request.
- The scoring timer finds active users through `devices.last_sync_at` (indexed), not a
  `DISTINCT` over `samples` as the spec suggested; cheaper and equivalent.
- `POST /v1/recompute` is limited to one per user per minute through a conditional update
  (works across replicas) and published with a JetStream dedup ID.
- The insights cache is keyed by user and local day and bounded.
- `ryft_last_sync_timestamp_seconds` lost its `device` label (unbounded with many users,
  and it published device IDs).

## D32 — apikey mode can act as any user

In apikey mode the bearer `<API_KEY>:<uuid>` acts as that user. That is what lets
`check_api.py` prove isolation with fresh users on every run against compose. The mode is
local-only by construction (D28).

## D33 — Deploy changes for Supabase

`postgres.yaml` moved from `base/` to `overlays/local-postgres` (which also switches to
apikey mode). The project ref is set once in `base/configmap.yaml`; the services refuse
to start while it is the placeholder. The Supabase secret key lives in its own Secret
(`ryft-supabase-admin`, Vault path `secret/ryft-admin`) referenced only by api, so ingest
and scoring never hold it. The backup job dumps `--schema=public` of `DATABASE_URL`
(Supabase or local) instead of the in-cluster Postgres. A new `ryft-egress` policy
allows the services DNS, NATS and TCP 443/5432 out; NetworkPolicy cannot match the
Supabase hostnames.

## D34 — Small pools and transaction-level migration locks

Supavisor session mode holds one server connection per client connection, so each pod
keeps `DB_MAX_CONNS` (default 4, scoring 2) connections, recycled every 30 minutes. Not a
`pool_max_conns` URL parameter: the backup job's libpq `pg_dump` would reject it.
Migrations now take `pg_advisory_xact_lock` inside each migration's transaction and
re-check `schema_migrations` there, so a pod dying mid-migration cannot leave a session
lock behind on a pooled connection.

## D35 — Request IDs are always the server's

Every request gets a UUIDv7 from the outermost middleware. It is returned in
`X-Request-ID` and `request_id` in error bodies, logged on every line of the request,
and carried in the `ryft.ingested` event so scoring's lines for a sync share its ID. A
client-sent `X-Request-ID` is logged as `client_request_id` only if it matches
`[A-Za-z0-9._:-]{1,128}`, and is never adopted: a client-chosen ID could collide with, or
be planted into, another user's log trail. The request ID lives in a mutable
`logx.Fields` struct in the context, which inner layers fill in (route, user) and which
survives `r.WithContext` copies. That is also how the route-pattern metric labels (D14)
reach the outer middleware.

## D36 — Per-user, per-pod token buckets

The rate limit is in memory in each pod, with no Redis. The effective limit is therefore
up to replicas × limit (≤ 4 api, ≤ 4 ingest), which is acceptable for a guard rail. It
is keyed by the verified user ID, not the client IP: mobile clients share carrier NAT
addresses, and behind the load balancer the source IP is not reliable. It runs after
token verification and before `EnsureUser`, so a throttled request costs no database
query. Classes:

- `POST /v1/sync`: 2/s, burst 60. A 90-day first backfill is a few dozen pages.
- Everything else: 10/s, burst 60. App launch is about 10 requests.

`POST /v1/recompute` keeps its own database gate (one per user per minute, D31), which
works across replicas. Unauthenticated floods are not limited by the services; each
costs one signature check at most, and unknown-key JWKS refreshes are themselves
limited (D29). A per-IP limit belongs at the edge (a Traefik `RateLimit` middleware on
the Ingress) if it is ever needed.

## D37 — Generic 5xx, 413 and hardening headers

Before, a database error's text (SQL, table names) went to the client in the `500`
body. Now every unexpected error answers `{"error":"internal error","request_id":…}`,
and the error itself is logged under that request ID (`httpx.Fail`). Request bodies have
explicit limits: 64 KiB in general, 32 MiB for `POST /v1/sync`. Oversized bodies get
`413` instead of a `400` "bad json", so the app can tell them apart. That is a status
change for an existing error, recorded in the API changelog; the app only branches on
401/429/503. Every response sends `Cache-Control: no-store` (health data must not sit in
a shared or on-device HTTP cache), `nosniff`, `X-Frame-Options: DENY`, a `default-src
'none'` CSP, `Referrer-Policy: no-referrer` and HSTS. The server sets
`MaxHeaderBytes` 64 KiB and `IdleTimeout` 120 s.

## D38 — Request deadlines as context timeouts, not `http.TimeoutHandler`

api requests get 15 s and syncs 50 s, as a context deadline inside the server's 60 s
write timeout. `http.TimeoutHandler` would buffer every response and write a plain-text
503 while the handler may still be running. A context deadline instead makes the pgx
calls return `context.DeadlineExceeded`, which `httpx.Fail` answers with `503 "request
timed out"` and `Retry-After`, so the app retries rather than reporting a server error.
Retrying a sync is safe because it is idempotent.


## D39 — Account deletion: hard delete, tombstone, locks, then Supabase

`DELETE /v1/me` hard-deletes every row in one transaction rather than soft-deleting:
nothing in the product needs a deleted account's health data, and an erased row cannot
leak. What remains is a tombstone (`deleted_users`: ID, timestamps). A deleted user's
access token stays valid for up to an hour, and without the tombstone `EnsureUser` would
recreate the account on the next request. Concurrency:

- `EnsureUser`'s write path and `DeleteUser` take the same per-user transaction-level
  advisory lock. A recreate therefore either commits before the deletion (and is then
  deleted with it) or sees the tombstone.
- `DeleteUser` locks the user row `FOR UPDATE` before deleting anything. Syncs in flight
  hold a key-share lock through their foreign keys, so they commit first and their rows
  are deleted too. Later syncs fail the foreign-key check.

The integration tests prove both with a held transaction, and they fail with either lock
removed. The Supabase auth user is deleted after our transaction commits. If that call
fails, the account is still erased on our side, the response says
`auth_user_deleted: false`, and each api pod's reconciler retries it. That reconciler
also re-deletes accounts that have a user row despite a tombstone, which happens after a
restore. `DELETE /v1/me` gets its own auth chain without the user lookup, so a retry
after a deletion still reaches the handler. It runs detached from the client
(`context.WithoutCancel`) with a 2-minute deadline. Apple token revocation is not part
of this (see `SUPABASE.md`).

## D40 — Source de-duplication per hour, and for sleep per night

The spec said to prefer the source with the most samples *that day*. A day-level
winner drops the phone's samples for the hours the watch was charging (and the reverse
for a phone left on a desk), so the winner is chosen per sample type and per clock hour.
Within an hour, "most samples" picks the device that was worn. Ties go to the name that
sorts first, so the result does not depend on row order. A workout's samples are chosen
from the whole hours around it (`SampleWindow`) and then cut to its window, so workout
load, workout curve and day strain always agree on the source.

The spec named `heart_rate`, `steps` and `active_energy`. Sleep has the same flaw and a
larger effect: two trackers' stages were summed, which doubles a night. So sleep is
de-duplicated as well, one source per night, the one with the most asleep minutes. HRV,
resting HR, respiratory rate and SpO₂ are averaged rather than summed and are left as
they are. Choosing the source with the largest total for steps was considered and
rejected: it rewards whichever device over-counts.

## D41 — Steps and active kcal as daily totals

Both are stored as whole numbers on `daily_scores` (migration 0008), summed from the
local day's samples after source de-duplication (D40). A sample counts on the day it
starts, the rule the ingest's dirty-day calculation already uses, and it is not split
across midnight. They are nullable: no samples means `null`, not `0`, so "not tracked"
and "did not move" stay apart. `DayDetail.strain` repeats them for the Strain screen.
It stays `null` without heart-rate data (the invariant fixed in D22), so a phone-only
user sees steps on `score` only. They are informational and do not change the strain
score.

## D42 — Least-privilege database roles and a migrate job

Every namespace used to hold the same owner credential, which can drop tables and read
every user's data. Each service now connects as its own role (`ryft_ingest`,
`ryft_scoring`, `ryft_api`, migration 0009) with only the table privileges its queries
use, derived from the store calls of that service. scoring may update only the load
columns of `workouts`, so the phone's data cannot be rewritten by the scorer. None of the
roles can run DDL, so migrations moved out of the services into `cmd/migrate`. It runs
once per deploy as the owner (Kubernetes Job in `ryft-migrate`, a one-shot compose
service). The services wait for the schema instead of applying it (`db.WaitMigrated`,
up to 10 minutes, then exit so Kubernetes retries).

- **Passwords:** migrations never contain them. The roles are created `NOLOGIN`, and the
  migrate job enables `LOGIN` and sets each password from `DB_PASSWORD_<SERVICE>`.
  Terraform (`infra/vault`) generates the passwords and writes each one only into the
  owning service's Vault namespace, plus the migrate namespace. The job sends a SCRAM
  verifier computed in Go (Go 1.24 `crypto/pbkdf2`, pinned against an independent
  implementation), so the plaintext cannot appear in Postgres's statement log.
- **RLS:** on Supabase, RLS is on and now applies to the services. A permissive
  `<table>_services` policy admits the three roles; the grants are what limit them. This
  is simpler than `BYPASSRLS`, which a non-superuser `postgres` may not be able to grant
  on Supabase.
- **Compose:** compose runs the same roles, so a missing grant fails `make smoke`
  locally, not in production.

## D43 — One NATS user per service, scoring owns the stream

NetworkPolicy only decides who can reach port 4222, and NATS used to accept anyone who
got there. Now every service authenticates as its own user, with the narrowest
permissions that still work:

- **ingest, api:** publish `ryft.ingested` only.
- **scoring:** the JetStream API subjects of stream `RYFT` and consumer `scoring`, plus
  `$JS.ACK.RYFT.scoring.>`. It cannot publish events or delete the stream.

Each client uses a custom inbox prefix, `_INBOX_<service>`, and may subscribe only
there. With the default `_INBOX.>`, any client could read every other client's replies.

Stream creation moved from every service to scoring, the consumer. A publisher that
starts before scoring has ever run gets a publish error. The sync is still stored, the
error is counted in `ryft_events_published_total{result="error"}`, and scoring's
15-minute pass picks the data up.

The server config is one file for compose and Kubernetes, and the passwords come from
the environment (NATS resolves `$VAR` from it). On Kubernetes they are bcrypt hashes
generated by Terraform (`random_password.bcrypt_hash`), so the NATS namespace never
holds a usable password; each service's plaintext exists only in its own namespace.
The bcrypt path was checked against `nats:2.11` with `$2a$` hashes, and
`permissions_test.go` fails against a permissive config.

## D44 — One namespace per workload, default deny both ways

Following `CLAUDE_FOLLOWUP_EKS_GOLIVE.md` step 2b, every workload has its own namespace:
`ryft-ingest`, `ryft-scoring`, `ryft-api`, `ryft-nats` and `ryft-migrate`.

- **Layout:** `base/<service>` holds namespace-neutral manifests, `services/<service>`
  sets the namespace, and `overlays/eks` composes all five.
- **Network policy:** every namespace denies ingress and egress by default and allows
  DNS (`base/common`). Each workload then allows exactly its flows. The old single
  namespace denied only ingress; egress was a port allow-list to anywhere, cluster
  included. Internet egress (443/5432) is now limited to public addresses (RFC 1918,
  CGNAT and link-local excluded), so a compromised pod cannot reach the VPC, the node
  metadata service or other pods on those ports.
- **Namespace selection:** policies match namespaces by `kubernetes.io/metadata.name`
  rather than the spec's `ryft.dev/service` label. The API server sets that label and
  nobody can relabel it; a Terraform-set label can be edited by anyone who may patch
  namespaces.
- **Pods:** they run without an automounted service-account token (no workload talks
  to the Kubernetes API), and Pod Security `restricted` is enforced on every
  namespace. NATS now runs as uid 1000 on a read-only root filesystem (checked with
  `nats:2.11-alpine`).
- **Ingress:** api and ingest each have an Ingress in their own namespace for the same
  host, with their own certificate, since a TLS Secret cannot cross namespaces. Only the
  api Ingress feeds ExternalDNS (`ingress-hostname-source: annotation-only` on ingest).
- **Removed:** scoring lost its Service (nothing calls it). The old root kustomization,
  `deploy/k8s/vault` and `secrets.example.yaml` are gone: EKS gets its Secrets from
  Vault, and `local-postgres` gets them from `scripts/local-secrets.sh`, one env file
  per namespace.
- **Backups:** as the spec says, the in-cluster `pg_dump` job left the EKS path with the
  in-cluster Postgres. On EKS the backups are Supabase's (plan-dependent); the job stays
  for `local-postgres`.

## D45 — infra/vault: corrected paths, generated credentials, one more namespace

- **Amended by D186, D187:** on the CE side the per-namespace HCP layout is replaced by one `auth/kubernetes` mount and a
  mount, policy and role per service; the long-lived `vault-auth` reviewer tokens go at the decommission (#145, #146, #147,
  #151, #152, #153; not applied).

The user's design stays: Kubernetes auth with a TokenReview reviewer per namespace,
the Vault Secrets Operator per namespace, and Terraform owning the namespaces and
service accounts. The user chose this over JWT auth against the EKS OIDC issuer, which
would have needed no reviewer tokens and no network path from HCP to the EKS API. The
changes:

- **Fix:** the generated `VaultAuth.spec.namespace` and the `login_check` output used
  the path relative to the provider (`ryft/api`). On HCP that path is `admin/ryft/api`,
  so every operator login would have failed. The template now uses a plan-time local
  with the parent prefix, and the golden test fails if it regresses.
- **Generated credentials:** the database and NATS passwords are now generated
  (`random_password`, D42 and D43). They are written only where used: a role URL per
  service, the owner and role passwords only to `migrate`, and bcrypt hashes only to
  `nats`. `tfvars` takes the pooler host, the owner password, the project ref and the
  Supabase secret key, which was missing for account deletion (a `check` block warns).
  The project ref stays in Vault, as designed, and left the ConfigMaps so there is one
  source.
- **New namespace:** `migrate`, the only holder of the owner credential.
- **Operator restarts:** `nats` restarts a StatefulSet; the migrate Job restarts
  nothing.
- **Hardening:** Pod Security `restricted` labels on the namespaces, and no automounted
  token for the workload and reviewer service accounts. The reviewer comment no longer
  claims a per-namespace blast radius: `system:auth-delegator` is cluster-wide, and a
  leaked reviewer token can review tokens, not read secrets.
- **Tests:** mocked `terraform test` checks the keys per namespace, that the owner
  password never leaves `migrate`, bcrypt-only NATS, role bindings, policies and
  Pod Security. The golden manifests are also checked against the operator's CRD
  schemas by `make k8s-validate`.
- **Defaults:** `eks_cluster_name` now defaults to `ryft-prod-euc1`, the applied name.

## D46 — The Vault Secrets Operator is a Terraform add-on

- **Amended by D186:** VSO's connection target changes per namespace to the in-cluster Vault, one cutover PR at a time
  (D191); the add-on itself stays.

The go-live spec had it as a manual `helm install`. Like the other add-ons (D25) it is
now a pinned `helm_release` in `infra/aws/kubernetes` (chart 1.6.0, CRDs still
`secrets.hashicorp.com/v1beta1`). Its default VaultConnection and default auth method
are off, so a namespace without its own VaultAuth gets no secrets at all. The `_raw`
key, the whole KV secret as JSON, is excluded globally and on each generated
VaultStaticSecret. Through `envFrom` it would have become one more environment
variable holding every credential, as seen on the first real sync. The chart was
rendered with these values and validated (33 resources).

## D47 — Start-up order and backoff instead of crash loops

On the first EKS deploy, the services started next to the migrate job, tried their
database roles before migrate had set the passwords, and exited, as designed. Kubernetes
restarted them quickly and in lockstep, and Supavisor's circuit breaker blocked the roles
(`ECIRCUITBREAKER … too many authentication failures`) well after the passwords existed.
Five restarts later they came up.

Two changes, either of which would have prevented it:

- **Backoff:** the services retry the database (`db.Open`) and NATS (`events.Open`) with
  backoff, 5 s doubling to 60 s with ±20 % jitter (`internal/retry`), instead of exiting.
  The database step is bounded by `MigrateWait` (10 min) and NATS by 5 min. Reproduced
  locally: with `ryft_api` set `NOLOGIN`, the api logged two retries and connected as
  soon as the role could log in, without a restart.
- **Staged apply:** `make k8s-apply-eks` applies NATS and the migrate Job first
  (`ryft.dev/stage=first`, a label only on those two namespaces' objects), waits for
  them, and only then applies the services.

The same race exists when rotating a database password: the services must not get the
new one before Postgres has it. `OPERATIONS.md` gives the forced order (migrate's Secret
first, the migrate job, then the service's Secret). A restart-free rotation with two
alternating roles was considered and not built; rotation is rare and manual.

## D48 — Apple token revocation: code once, encrypted, in its own table

Apple requires that deleting an account also revokes the app's Sign in with Apple
grant. Supabase's native sign-in keeps no Apple token, and the `.p8` key may not ship
in the app, so the api does it. The app sends the one-time authorization code to
`POST /v1/me/apple-authorization`; the api exchanges it for a refresh token and revokes
that token on `DELETE /v1/me`, before deleting the Supabase user. Deviations from the
spec:

- **Its own table, not `users.apple_refresh_token`.** `ryft_ingest` and `ryft_scoring`
  can read `users`; only `ryft_api` can read `apple_tokens`. While a deletion waits on
  Apple, the encrypted token sits in the tombstone, and ingest's tombstone read narrowed
  to the `id` column. Migration 0010; the grant matrix test pins both.
- **`APPLE_CLIENT_ID` is the bundle ID, not a Services ID.** Native sign-in issues
  codes for the app's bundle ID (`dev.timkrebs.ryft`), and the exchange's `client_id`
  must match. A Services ID only applies to web sign-in.
- **Encryption:** AES-256-GCM with `DATA_ENCRYPTION_KEY`, 32 random bytes generated by
  `infra/vault` into the api namespace only. The user ID is the associated data, so a
  ciphertext copied to another row does not open, and a version byte allows a later
  key rotation. The key must not be replaced casually; tokens sealed with it would no
  longer open, and their accounts would not be revoked at Apple.
- **Identity check:** if the access token carries the Apple identity
  (`user_metadata.provider_id` with Apple's issuer), the exchanged code's `sub` must
  match it (`422` otherwise). That catches a code from another Apple ID or an app bug.
  The `id_token` from the exchange is read without signature verification, since it
  comes straight from Apple over TLS in reply to our own authenticated request.
- **Status codes:** `501` when not configured (local, apikey), `422` for a used or
  expired code (the app does not retry), `503` + `Retry-After` when Apple is
  unreachable. Revocation failures never block the deletion: the data is erased either
  way, and the reconciler retries as it does for Supabase (`step="apple"` in
  `ryft_account_deletions_total`).

## D49 — Recreate the Supabase project in Frankfurt

The first production project landed in eu-west-1 (Ireland), the cluster in
eu-central-1. That is legal (both EU) and works, but every query crosses regions
(~20–25 ms; an API call is 2–5 queries), and a project cannot move. The user decided to
recreate it in Frankfurt before inviting anyone, while the database holds only test
data; `SUPABASE.md` → *Moving the project to Frankfurt* is the runbook. The platform
needs no code change: the region is only in the pooler host and the project ref
(`infra/vault` tfvars), and migrate builds the schema in the new project.

## D50 — Rescoring everyone runs inside scoring, not through the API

The spec suggested one `POST /v1/recompute` per user using the admin key. But that
endpoint acts as the user whose access token it carries, and the Supabase secret key
cannot mint user tokens. Working around that would mean a new admin path into the api.
Instead, the scoring binary has a `rescore` subcommand, run in the scoring pod with
`kubectl exec` (`scripts/rescore_all.sh`). It uses scoring's own least-privilege role,
which may read raw data and write scores and nothing else. It needs no new credential,
no NATS round trip and no per-minute gate, and rescores each user's days oldest first.
Concurrent events for the same days are harmless, since scoring a day is an idempotent
upsert.

No rescore is needed today. The production database was created on 2026-10-18 by the
current code, so no day was scored with older rules (and the Frankfurt project will
start empty as well).

## D51 — Grafana Cloud collector: the onboarding preset, isolated, token in Vault

- **Amended by D201, D202 (decided, not built):** monitoring's operator objects become an Argo CD application in a project of
  their own, and the collector a Terraform add-on that also owns the namespace.

The user chose Grafana Cloud's Kubernetes Monitoring with the onboarding's full preset:
a root, host-network Alloy DaemonSet with node log files, cgroups and storage mounted,
Beyla eBPF, an SDK-injection webhook, and pipelines from Fleet Management. That cannot
run under Pod Security `restricted`. It is kept as chosen, with four changes:

- **Namespace:** `monitoring`, created by `infra/vault` with Pod Security `privileged`,
  instead of the shared `ryft` namespace from the generated command.
- **Token:** the Fleet Management token is a Terraform-managed secret in Vault
  `admin/ryft/monitoring`, synced by the operator. The chart reads it from that Secret
  (`remoteConfig.secret.create: false`), never from a `--set` flag. The token shown
  during onboarding must be rotated.
- **Logs:** RYFT's pod logs carried users' daily scores until D114 and go to CloudWatch (D52), not
  Grafana Cloud. Because the collector can read every log file on the node, this is a
  Fleet Management setting (exclude `ryft-.*`), checked by an empty Logs query, not an
  enforced boundary. Making it one would mean dropping `filesystem-log-reader` from the
  preset.
- **Metrics port:** a host-network collector scrapes from node addresses, and on EKS
  nodes and pods share the VPC range. Opening the API port 8080 to that range would let
  every pod reach the API, so `/metrics` moved to port 9090. Only 9090 is opened to the
  VPC; it carries counters and route labels, no user data.

Beyla still sees RYFT's HTTP traffic (routes, status, durations; trace URLs contain
workout UUIDs). Excluding the `ryft-*` namespaces from Beyla's discovery is the next
step if that matters.


**2026-10-19, as deployed:** the onboarding now names two collectors, `alloy-daemonset`
and `alloy-deployment`, and Fleet Management assigns its Kubernetes pipelines by those
names; a single collector called `alloy` received only self-monitoring and sent no
cluster metrics. The values follow the onboarding (two collectors, node-exporter on),
without Kepler, OpenCost and k8s-manifest-tail (pod slots, and OpenCost would need a
second inline credential). Chart 4.5.3 needs `auth.type: basic` and the user as a
literal `usernameFrom` when the token comes from an existing secret, and the daemonset
advertises its cluster address on `ens5` (Amazon Linux 2023), or the three collectors
never form one cluster.
## D52 — Logs in CloudWatch, not Loki

The spec left the choice open; the cheaper one wins. At alpha volume CloudWatch Logs
costs about €1 a month plus €1 for the KMS key, needs no extra pods on two t3.medium
nodes, and keeps health data in the account and region that already hold the cluster.
Loki would need object storage, pods and upgrades, or Grafana Cloud's hosted Loki, which
means a further processor of health data. Fluent Bit ships only the `ryft-*` namespaces
and Traefik. The group keeps 14 days, encrypted with a key that CloudWatch Logs may use
for `/ryft/*` groups only, and is readable by the account's administrators only
(`OBSERVABILITY.md`).

## D53 — Per-IP limit at the edge, with client IPs preserved

Traefik's `RateLimit` middleware limits each client to 20 requests a second, bursts of
50, before a token is checked; the per-user limit (D36) takes over after it. With
`nlb-target-type: ip` the NLB had hidden client addresses, which would have put every
client into a handful of buckets, one per NLB address. The target-group attribute
`preserve_client_ip.enabled=true` fixes that; the load balancer controller manages the
security-group rules. The middleware is an EKS-overlay object (Traefik-specific), one per
namespace, since Traefik references middlewares within a namespace.

## D54 — Remote state: one bucket, partial backend configuration

*Superseded by D56.* The migration emptied the four local states without writing them
to S3.

`infra/aws/state` creates one S3 bucket for all four roots, each with its own key and S3
lockfiles. The bucket is versioned (90 days of old versions), private, TLS-only and
encrypted with its own KMS key. The backend blocks hold only key, region and locking.
Bucket and key ARN come from a git-ignored `infra/backend.hcl` that the bootstrap root
prints, which keeps the account ID out of the repository. The bootstrap root's own state
stays local: it holds no secrets and can be re-imported.

Retired on 2026-10-19: the bucket was destroyed and `infra/aws/state` and
`infra/backend.hcl` were removed from the repository.

## D55 — Budget in USD; the apex site in the cluster

AWS Budgets alerts in USD: USD 165 ≈ €150, at 80 % actual and 100 % forecast, created
only once a recipient is set, so a plan without one does not fail. The apex site runs as
an unprivileged nginx in `ryft-web`. It reuses Traefik, the DNS-01 certificate and
ExternalDNS, which S3 + CloudFront would duplicate with an ACM certificate in us-east-1.
Its `apple-app-site-association` lists `webcredentials` already, so password AutoFill
works for email accounts, and `applinks` under `/auth/*` for later universal links.
Apple fetches it without following redirects, so `www` redirects to the apex and the app
names `ryftapp.de` itself.

## D56 — State in HCP Terraform, one workspace per root, runs stay local

The S3 attempt (D54) failed in the worst way. `terraform init -migrate-state` emptied
all four local states without writing them to the bucket, and an apply against the
empty Vault state tried to recreate existing namespaces. The last full states survived
only in `terraform.tfstate.backup`. The user moved state to HCP Terraform (organization
`tim-krebs-org`).

- **One workspace per root.** `ryft-prod-euc1-platform` (`infra/aws`),
  `ryft-prod-euc1-dns`, `ryft-prod-euc1-kubernetes` and `ryft-prod-euc1-vault`.
  Merging the three AWS roots into one would have meant cross-root state moves right
  after the incident. It would also have made the Kubernetes and Helm providers depend
  on a cluster being created in the same run, so the separation stays.
- **Local execution.** HCP stores, locks and versions the state and records each run.
  Plans and applies stay on the operator's machine with the operator's credentials, as
  before, so no cloud credentials sit in HCP. `infra/vault` keeps writing the operator
  manifests into the repository, and the providers keep using `aws eks get-token`.
  Remote execution would need dynamic credentials (AWS OIDC, Vault JWT), Kubernetes
  access without the aws CLI, and the manifests as outputs; it can come later.
- **Pinned version.** Terraform is pinned to exactly `1.16.6`, as the user asked, in the
  code, in CI and in the workspaces.
- **Migration with checks.** `make tf-migrate-hcp` refuses unless the workspace exists
  and runs locally, keeps a copy of each state outside the repository, moves the S3
  backend cache aside before `init` so that no partial S3 state can travel (Terraform
  refuses `-reconfigure` with a `cloud` block), pushes only into an empty workspace, and
  compares resource counts.
- **CI** validates, lints and tests every root offline (`init -backend=false`): no HCP
  token and no credentials in GitHub, and CI never writes state. Speculative plans on
  pull requests would need remote execution.

## D57 — Three nodes, one per zone; prefix delegation next

On 2026-10-19 the deploy stopped with the migrate Job Pending: "0/2 nodes are available:
Too many pods". A t3.medium takes 17 pods, one per secondary IP address. Two nodes held
34, and the add-ons, the new Fluent Bit DaemonSet and rollouts stuck on a database
without roles used all of them. The Grafana collector adds about ten more (alloy and
beyla on every node, kube-state-metrics, beyla's cache, the SDK injector).

- **Three nodes** (`min_size = desired_size = 3`). They give 51 slots, the capacity
  `infra/aws/README.md` already planned for metrics. One node per zone also means the
  single-replica NATS, whose EBS volume cannot leave its zone, always has a node to run
  on. The cost is about $35 a month.
- **Not yet prefix delegation.** It raises the limit to 110 pods per t3.medium for
  free. It needs a further node rotation, though, and the kubelet then reserves about
  1 GiB more memory per node, so memory becomes the limit. It is the next step when 51
  slots get tight; the HPA maximums do not fit today.
- **Not bigger instances.** A t3.large takes 35 pods. Two of them (70 slots) would add
  about $70 a month against $35 for the third t3.medium, and they would leave one zone
  without a node. (Superseded by D64: three t3.large.)

## D58 — Train without M3: muscular load only from logged sessions

M3 (strain target: a daily target with bands, and a duration-based muscular-load estimate) is
deferred until the account flows are verified on a device; M3b (Train) was built
first, as the user decided. M3b only needs from M3 a place for muscular load and its
addition to the day's TRIMP, so that is all it takes: `workouts.muscular_load`,
`daily_scores.muscular_trimp`, added before the compression. Only logged sessions fill
it. When M3 comes, its `k[activity] · duration_min` estimate fills `muscular_load`
only for strength-type workouts *without* an attached session, and its target and bands
build on the same strain.

## D59 — The strength load model and its calibration

The spec's formula, `reps × (weight / e1RM)² × muscleFactor`, cannot hit all of its own
reference points. Against the e1RM of its own high-rep sets, a bodyweight set runs at
intensity ≈ 0.7, so a 20-minute circuit scored near strain 15 instead of 5. Twelve
Olympic singles scored near 6 instead of 8.

- **muscleFactor = muscle mass × a load factor per exercise.** The mass comes from the
  muscles (primary in full, secondary half; weights in `SCORING.md`). The load factor
  defaults by kind and is set per built-in where the kind misleads: Olympic lifts
  1.15, weighted pull-ups and dips 0.6. It is a column (`exercises.load_factor`), not
  part of the API.
- **Anchors: strain, not TRIMP.** The spec wrote "TRIMP ≈ 85 → strain ≈ 11". On the
  existing compression (scale 80) TRIMP 85 is strain 13.7, and changing that scale would
  move every heart-rate strain. The strain values are what users see, so they are
  pinned: 11.0, 8.0 and 5.1 (TRIMP 59.3, 38.1, 22.3) with `strengthScale = 0.6`.
- **e1RM as of the session, own sets included.** Rescoring old sessions gives the same
  answer, and intensity cannot exceed 1. A first session is scored against itself, a
  little high (≈ 0.86 for fives) until history exists.
- **Body weight**: the session's, else the last one logged, else 75 kg. HealthKit body
  mass is not synced (yet).
- **Unilateral reps are per side**, so the load counts both sides; warm-ups a quarter;
  time and distance sets the spec's per-minute / per-100 m rates times muscleFactor.

## D60 — Which workout carries a session's load

A HealthKit workout of activity `strength`, `functional_strength` or `crossfit` that
overlaps at least half of the session (largest overlap, then the earlier start; one
session per workout), else a workout scoring creates with a fixed id
(`train.SessionWorkoutID`). The rule runs on every pass for the session's day, because
the watch's workout often syncs after the app completed the session. Then the load moves
to it, the created workout is deleted and `sessions.workout_id` changes; the API says
so. A day's muscular TRIMP comes from its sessions, not from the workouts, so it cannot
be counted twice. Workouts whose session is gone or no longer completed are released
on any pass of that user.

## D61 — Saving sessions: merge while in progress, a 409 that carries the server copy

The app saves the whole session after every set and replays an offline queue, so saves
arrive late and out of order.

- **In progress: merge by id.** Exercises and sets in the body are written; ones only the
  server has are kept. An old queued save therefore never deletes newer sets. The cost:
  deleting a set while in progress is not possible by omission; the app unchecks it
  (`completed_at: null`), and unchecked sets count nowhere.
- **Conflict: per set, by the app's `updated_at`.** If a set exists on both sides and the
  stored `updated_at` is later, nothing is written and the answer is `409` with the
  server's whole `Session` as the body. The app merges by set id (later `updated_at`
  wins) and saves again. Equal timestamps are a replay, not a conflict. The app's clock
  is trusted for ordering its own edits; the server never invents one.
- **Completed: replaced whole**, so edits after the workout can remove sets; `ended_at`
  stays when the body has none. Status moves only forward: `completed` only via
  `…/complete`, never back to `in_progress`; `discarded` takes a session out of every
  total. An edit or delete of a completed session recomputes its records and rescores
  its days (old and new).
- Values are clamped (reps ≤ 100, kg ≤ 500, RPE 1–10, …) rather than rejected; structure
  (positions, set indexes, unknown exercises) is rejected with `422`.
- Session saves get their own rate-limit bucket with the sync budget (2/s, bursts of 60),
  and Train documents a 256 KiB body limit.

## D62 — Built-in content: seeds with fixed ids, loaded by the migrate job

173 exercises (English and German names, muscles, equipment, load type, body-weight and
load factors) and four plans (Full Body 3× 5×5 with linear progression, Upper/Lower 4×
by RPE, Weightlifting 4× as a four-week %-of-e1RM wave, 5/3/1 4× with Boring But Big)
live as JSON in `internal/db/seed` and are embedded in the migrate binary, which loads
them after the migrations.

- **Fixed ids** (UUIDv5 of the slug, and of the plan, week, day and position for plan
  days and exercises). The app caches built-ins, and sessions refer to them, so an id
  never changes; a test pins two of them.
- **Idempotent**: upsert by slug and id, rows touched only when they changed. A
  built-in that leaves the seeds is archived, never deleted (old sessions name it).
  A removed plan day or exercise is deleted; sessions keep their sets and their reference
  becomes `NULL`.
- **Language**: `name` follows `Accept-Language` (German or English); both names are
  always in the response, so a cached library does not depend on the device language.
- Built-ins have `user_id NULL`: readable by everyone, changed only by the seeds (the api
  answers `403`; on Supabase, RLS admits reads only).
- **Numbers as double precision.** 0011 made them `real`, and they read back with float4
  noise (75.1 as 75.0999…). 0012 converts them. It is a new migration because 0011 was
  already applied to local databases.

## D63 — Train ownership: api writes what the user enters, scoring what it derives

The api writes exercises, plans, enrolments, sessions and records (`ryft_api` owns
those tables). Scoring writes what it derives: a session's load columns (column grants
only) and the workout that carries the load. It may create and delete only its own
workouts (`source = 'ryft-session'`; restrictive RLS policies on Supabase) and set only
`muscular_load` and `session_id` on the phone's. The api's `…/complete` computes the
same load with the same code (`Engine.SessionLoadNow`), so the response is final in
practice; scoring's later pass is authoritative and re-runs on every rescore.

## D64 — t3.large nodes for the whole Grafana Cloud stack

Grafana Cloud's onboarding deploys more than the collectors: node-exporter, Kepler,
OpenCost and k8s-manifest-tail, and it now names two collectors (D51). On three
t3.medium (17 pods each, D57) RYFT, the add-ons and the core of that stack left two
slots; the rest and the HPA maximums did not fit. The user chose bigger nodes.

- **3 × t3.large**, still one per zone: 35 pods and 8 GiB each, 105 slots, about 55 in
  use. The HPA maximums and a rotation's surge fit, and memory stops being tight (the
  "large" collector preset requests 1 GiB per collector). This replaces D57's "not
  bigger instances" and its prefix-delegation plan.
- **Cost**: about $105 a month more, roughly $350–370 in all. The budget alarm's
  default ($165) is far below; raise `monthly_budget_usd`.
- **Changing the type replaces the node group** (the instance type cannot change in
  place): the module creates the new one first, then drains and deletes the old one.
  PDBs keep one api and one ingest pod; NATS and scoring restart once.
- **OpenCost's credentials** come from `ryft-secrets` like the token: `GC_TOKEN` (the
  same Grafana token as in the onboarding) and `PROM_USERNAME` (the stack's
  Prometheus user, a number), both from Vault, never in the values file.

## D65 — Sleep quality: performance, efficiency and restorative sleep

Quality was a copy of sleep performance, so a long but restless night looked as good as
a deep one. M4 gives it its own score: `0.6 · performance + 0.2 · efficiency + 0.2 ·
restorative`.

- **Performance keeps the most weight** because it is the part tied to the person's
  need; efficiency and restorative sleep describe how the time was spent.
- **Restorative** is deep + REM against **40 %** of time asleep, capped at 100. Adults
  typically spend roughly 15–20 % in deep and 20–25 % in REM, so 40 % is a good night,
  not an exceptional one. Without stages it equals performance (the formula then
  weighs performance 0.8), rather than punishing older sources.
- **The labels stay** (good ≥ 80, fair ≥ 60). Nights now land a few points higher than
  their performance when they are efficient and deep, lower when not. The brand board's
  82 % "good" corresponds to performance 72.
- **Old rows**: the Sleep screen computes quality from the stored totals until a night is
  rescored, so the app sees one formula at once; `DailyScore.sleep_quality` stays `null`
  for them until the rescore.

## D66 — Naps: the 10:00–20:00 window and splitting the night

The spec's nap window is 10:00–20:00 local, but the night window (D−1 20:00 to D 13:00)
already covers the morning. Choices:

- **A nap is decided by its end**, like the night: sleep ending 10:00–20:00.
- **The night is the block with the most sleep** in its window, blocks ending at a
  60-min pause (a waking hour, not a trip to the bathroom). Only *other* blocks that end
  from 10:00 become naps. Blocks before stay with the night, so a short doze before
  bed changes nothing, and a late sleeper's one long block until 11:30 stays the night.
- **The afternoon** (13:00–20:00), which no night window covers, is all nap. Morning and
  afternoon naps are joined before counting, so a nap across 13:00 is one nap.
- **Counting**: pauses under 30 min join one nap, a nap counts from 20 min asleep, the
  day's credit is capped at 90 min. Shorter dozes are noise or a phone left on the bed.
- **The credit goes to the next night** (the nap's day D credits D+1's need), computed
  from D's raw segments when D+1 is scored, not from D's stored `nap_min`, so the order
  in which days are rescored does not matter.
- **One source per window**, as for nights (D40).

## D67 — The learned baseline: which nights qualify, and rescoring in-process

The baseline (460 min by default) should be the person's own. Estimated from what sleep
the body takes when it is not repaying anything:

- **Qualifying night**: recovery ≥ 67 that morning (green) after a day with strain < 10.
  A green morning after a hard day is often the body catching up; an easy day before
  keeps the strain top-up out of it.
- **Median, not mean**, of those nights, so one 11-hour lie-in does not move it. At least
  14 nights with sleep and 7 qualifying in the last 30 days; clamped to 360–570
  (6–9.5 h) so a broken tracker cannot set an absurd value.
- **Weekly**, Sunday from 04:00 local: often enough to follow a change, rarely enough
  that the need does not wobble. It runs in scoring's 15-minute pass (once per user and
  Sunday per pod, also when there is too little data) and as `scoring learn-baselines`.
- **Manual wins.** `PATCH /v1/me` with `sleep_base_min` sets the source to `manual`;
  the estimate is still stored (`sleep_base_learned_min`) so the app can offer it, and
  `sleep_base_manual: false` switches back. Users who had already changed 460 before
  M4 are marked manual by migration 0013, because they chose that value.
- **Rescore the last 7 days in the same process** when the baseline in use moves,
  rather than publishing a recompute event: scoring is the only writer of scores, the
  week is a few hundred milliseconds, and the debt of the coming nights then chains
  through the new value at once. Older days keep the baseline they were scored with
  until a full rescore.
- **The grant**: scoring may update only the four baseline columns of `users`.

## D68 — The planner's efficiency: the last 7 nights, never below 85 %

`GET /v1/sleep/plan` turns tonight's need into time in bed by dividing by sleep
efficiency. The last 7 nights' mean is the person's own; **90 %** when there are none
(a typical adult value). **Never below 85 %**: a week of poor nights would otherwise ask
for 10+ hours in bed, and lying in bed longer is not how efficiency recovers (sleep
restriction works the other way). The plan uses **today's strain so far**, so the
evening's workout raises the need it shows only after it syncs; the night's own score
uses the day's final strain.

## D69 — Remote config: kill switch, allow-list, stable buckets; an admin key of its own

`GET /v1/config` serves the app's feature flags (M7 core). Choices:

- **A flag is on** for a user when `enabled` and (the user is on `allow_user_ids` or
  `bucket < rollout_pct`). `enabled = false` turns it off for everyone, testers
  included: the kill switch the spec's `enabled` column suggests. Shipping dark is
  `enabled, 0 %` plus an allow-list.
- **Bucket**: SHA-256 of `"<key>:<user id>"`, first four bytes, mod 100. Per flag, so
  two rollouts at 10 % do not hit the same users; stable, so raising a rollout only
  adds users. Written down in `API.md` and pinned in `flags_test.go`, so operations can
  predict who has a flag.
- **Seeds** for the app's six keys (`train.enabled` and `sleep.planner` on, the rest
  dark), inserted once and never overwritten by a later migration.
- **The admin key.** The spec says "behind the existing admin key", but the api has no
  inbound admin credential: `SUPABASE_SERVICE_ROLE_KEY` is what the api presents *to*
  Supabase. Accepting it as a bearer would make the most powerful key also an API
  password and tie its rotation to Supabase's. So `/v1/admin/*` takes its own
  `ADMIN_API_KEY` (48 random characters, generated by `infra/vault` for the api only,
  constant-time compare); without it the routes do not exist.
- **Migration 0014**, not the spec's 0013: that number is the sleep upgrades'.

## D70 — Which pushes, when

- **MORNING_SCORE** when the user's local today first gets a recovery after a night
  with sleep (scoring publishes `ryft.scored` from the day pass). Only for today, so a
  backfill or rescore never notifies, and the text is built from the score at send
  time, not the event's copy. One a day.
- **SESSION_SCORED** once scoring has scored a completed session (so the strain in the
  text is final), within 12 h of its end: an offline queue replayed days later is
  scored, not announced. The body says how many records it set. **PR** is sent instead
  only when the user turned session summaries off but wants records: one
  notification per session, not two.
- **SYNC_STALE** after 20 h without a heart-rate or step sample (the two types every
  watch writes all day), for users with data in the last 7 days (someone who stopped
  using RYFT is left alone), once a day between 09:00 and 21:00 local.
- **A silent refresh** (`content-available`) with each morning event, also when the
  morning alert is muted or held back by quiet hours: the app's data is fresh when it
  is opened. At most one a day (Apple throttles background pushes).
- **Quiet hours** hold the morning alert until they end, if that is still the same
  day, and drop the others (a session summary or a stale reminder is old news by then).
  `from = to` means none; `from > to` spans midnight.
- **Idempotent per user, day and category** (`notifications_sent`), as the spec says:
  a second session the same day gets no second summary.

## D71 — notify reads its own subjects: ryft.scored and ryft.session.scored

The spec has notify consume `ryft.session.completed`. Stream `RYFT` is a work queue
(work-queue retention, `ARCHITECTURE.md` → Events): a message is removed once acked, and JetStream refuses a second consumer whose
subjects overlap another's. scoring consumes `ryft.session.completed`, so notify cannot.
Instead **scoring publishes `ryft.session.scored`** after it has scored the session's day.
That is also the better moment: the load in the notification is final. notify has one
durable consumer filtered to `ryft.scored` and `ryft.session.scored` on scoring's
stream. Its NATS user may create only that consumer, pull and ack; scoring may publish
the two subjects and still not its own inputs. A failed publish loses that one
notification (the day is not "first" again); the score is stored either way. Deferred
messages count as deliveries, so notify's consumer allows 15.

## D72 — Delivery: claim first, release on failure, tokens move with the install

- notify **claims** `(user, day, category)` before it sends. A redelivered event or a
  second pod finds the claim and sends nothing. If no device was reached because APNs
  could not be reached, the claim is released and the event retried in 30 s; if APNs
  answered (even with refusals) the claim stays.
- **Tokens APNs reports gone** (410, `BadDeviceToken`, `DeviceTokenNotForTopic`) are
  deleted at once, also from the event's remaining sends.
- **A token belongs to an app install, not a user**: registering it moves it from
  whichever account or device had it, so a shared phone never shows the previous
  account's scores. Account deletion removes the tokens and the send log.
- **Language**: payloads carry localisation keys the app resolves (`loc-key`), so the
  wording ships with the app. The registered `locale` (`en`/`de`) formats the arguments:
  `82%` / `82 %`, `11.2` / `11,2`, the exercise's English or German name.
  `internal/notify.Catalog` pins each key's English and German text and its arguments;
  `API.md` lists them.

## D73 — An APNs client of our own; notify runs without a key

Token-based APNs is one HTTP/2 POST per device with an ES256 JWT (team ID, key ID, the
.p8). `net/http` speaks HTTP/2 over TLS and the module already has `golang-jwt/jwt/v5`
(Sign in with Apple), so `internal/apns` is about 150 lines and testable against an
`httptest` HTTP/2 server. `sideshow/apns2`, which the spec names, would add
`golang-jwt/jwt/v4` and `golang.org/x/net` for features RYFT does not use
(certificates, its own HTTP/2 client), against `CLAUDE.md`'s dependency rule. The
provider token is reused for 40 minutes (Apple: no older than an hour, renewed at most
every 20) and renewed once on `ExpiredProviderToken`.

The Apple Developer Program membership is pending, so there is no APNs key yet. notify
starts without one, consumes and decides as usual, and counts
`ryft_push_notifications_total{result="unconfigured"}` instead of sending; nothing is
claimed, so nothing is lost for later. The three `APNS_*` values are all-or-none
(validated by `infra/vault` and at start-up). The topic (the bundle ID
`dev.timkrebs.ryft`) is not secret and sits in the ConfigMap.

## D74 — The journal: the morning after, and "unknown" is not "no"

- **An entry is about the day it happened** (the morning sheet asks about yesterday),
  and its effect is the **morning after**: recovery and HRV of day D+1, the sleep
  performance of that night. The roadmap's `mean(recovery | yes)` does not say which
  morning; pairing an entry with its own day would measure the night before the
  behavior, and alcohol at night would seem to improve the previous morning.
- **A saved day is the unit.** `PUT /v1/journal/{day}` replaces the day with what the
  sheet showed, the toggles left off as `value_bool: false`. A behavior not sent that
  day, and a day never saved, is unknown. Without that, every day the user skipped the
  sheet would count as "no alcohol", and every behavior enabled later as "no" for all
  the past days. `journal_days` (beyond the roadmap's tables) records the save; it also
  answers the Today card's "answered yet?".
- **Editable 14 days back** (the app's Sleep → Day), never ahead.
- `value_bool` is the yes for every kind; `value_num` (count) and `value_time`
  (time, stored as minutes after midnight) are details the impact does not use yet.

## D75 — Effects: points, a seeded bootstrap, unlocked at 5 + 5

- **Units.** Recovery and sleep performance are already percentages, so their effect
  is a difference in **points** ("−14 recovery"); a relative change of a percentage
  would read as a second percentage. HRV differs a lot between people, so its effect is
  **relative** to the "no" mornings.
- **90 % percentile bootstrap, 1 000 resamples**, as the roadmap says. The generator is
  seeded with user, behavior and metric: a reload must not move the bar the app shows.
  It is xorshift64* with a multiply-shift bound instead of `math/rand`, whose bounded
  draws made up most of the time; the bias for groups of up to 90 is below 1e-7.
- **Unlocked at 5 yes and 5 no days in 90**, as the roadmap says; HRV and sleep
  performance need 5 + 5 of their own. `significant` (the interval excludes 0) gates
  the insight, not the list: the app shows unlocked but uncertain effects with their
  wide bar.
- Not modelled: confounding (alcohol on weekends, when people also sleep in) and
  autocorrelation of consecutive days. The card says "typically", and the interval is
  honest about the noise, not about causes.

## D76 — Impact on request, not a table, an event and a nightly job

The roadmap stores `behavior_impacts`, recomputes them nightly in scoring and after
every journal write through `ryft.journal.changed`. Computing them when they are
asked for is cheaper than keeping them fresh: ~44 ms of CPU for 75 unlocked behaviors
(25 built-ins and 50 custom, all with enough data, the upper end), under 15 ms for
25, plus two indexed queries. The api has no CPU limit and idles at 1 m. So:

- no table, no subject, no change to scoring's consumer filter (updating it once
  needed a retry, `internal/events`), no nightly pass, no staleness after a journal
  write;
- the insight computes only yesterday's behaviors; the M7 weekly report can call the
  same function;
- **no worker service** (asked for if needed): it would add a namespace, a role, a
  NATS user and a network hop for work shorter than the queries before it.
  `ryft_behavior_impact_seconds{caller}` watches it; if its p95 passes ~100 ms, the
  computation moves into a worker (or scoring) with a stored result.

## D77 — The behavior catalog

- **Built-ins** like the exercises (D62): 25 behaviors in `internal/db/seed/
  behaviors.json`, fixed ids (UUIDv5 of `behavior:<slug>`), English and German names,
  loaded by the migrate job; a built-in that leaves the seeds is archived, so old
  entries keep their behavior. Eight are on by default (alcohol, caffeine after 2 pm,
  late meal, screens in bed, stretching, meditation, feeling sick, work stress): a
  sheet that fits one screen.
- **The user's choice** is its own table (`user_behaviors`), so a changed default
  reaches everyone who never touched it.
- **Custom behaviors** take their id from the app (offline-safe, a repeated POST
  updates), at most 50, slug from the name made unique per user. Deleting one archives
  it: its past entries stay, it leaves the catalog and the impact list.
- The roadmap's ~25 included "melatonin", "magnesium" and "creatine"; sexual activity
  and medication beyond them were left out of the built-ins (custom behaviors cover
  them for whoever wants them).


## D78 — Consent: recorded per policy version, enforced where data comes in and is processed

Heart rate, HRV and sleep are health data (GDPR Art. 9); for a consumer app the basis is
the user's explicit consent (Art. 9(2)(a)), and it has to be provable and withdrawable.

- **A row per grant**, `revoked_at` on withdrawal, nothing ever updated otherwise
  (`consents`, migration 0017): the history answers "what did this user agree to, when,
  in which app version". Repeating a grant for the same version keeps the first row.
- **The policy version** lives in config (`PRIVACY_POLICY_VERSION`, served in
  `GET /v1/config`), not in a table: a new text ships with a deploy, and a POST for any
  other version is a `422` (the app showed an outdated text).
- **Enforced at the two places health data is processed**: ingest refuses a sync with
  `403 consent_required` before it reads the body (no current consent, or one to an older
  version), and scoring computes nothing for a user without an unrevoked consent
  (`ErrNoConsent`; any version, since new data cannot arrive under an old one anyway).
  Reads, export and `DELETE /v1/me` stay open: withdrawing must not lock the user out of
  their own data or out of deleting it. Withdrawal deletes nothing; erasure is its own
  right and its own button.
- **Read with the user row** (scalar subqueries in `userColumns`), never cached: a
  withdrawal stops the next sync and the next scoring pass, not 60 s later.
- **Existing accounts get no consent backfilled**: consent cannot be presumed. Until an
  app build with the consent screen ships, the user's own account consents once with
  `POST /v1/me/consent` and its token; until then its syncs answer `403`.

## D79 — The invite gate: server-side, codes with ~40 bits, one message for every failure

TestFlight's public link travels, and Sign in with Apple lets anyone with the app make an
account, so the gate is the server's: with `BETA_GATE=on` every `/v1` route answers
`403 invite_required` for an account without access, except what onboarding needs
(`GET /v1/me`, `GET /v1/config`, `GET /v1/model`, consent, `POST /v1/me/invite`,
`POST /v1/feedback`, `DELETE /v1/me`). Off by default (local, smoke); production turns it
on through Vault (`BETA_GATE` in the api and ingest secrets).

- **Codes**: `RYFT` + 8 characters from 31 (digits 2–9 and letters without I, L, O, so
  nothing is misread), drawn with rejection sampling from `crypto/rand`: ~40 bits, shown
  `RYFT-7K3M-QX9P`, stored upper case without dashes. Case, spaces and dashes do not matter
  when redeeming. `max_uses` (1–1000), optional `expires_at`, a note for whom it was.
- **No oracle**: unknown, expired and used-up codes get the same `422`; 5 attempts per user
  and hour (per pod), against ~10¹² codes.
- **Redeeming locks the user row**, so two concurrent redemptions use one code once; an
  account that already has access gets `204` without using one.
- **Every account that existed at the migration keeps access** (the user, smoke and test
  accounts), so turning the gate on breaks none of them.
- The operator manages codes with `ADMIN_API_KEY` (`/v1/admin/invites`); admin calls log a
  fingerprint of the key (first 4 bytes of its SHA-256), never the key.

## D80 — A model version per stored row, the changelog in the binary

Beta testers will compare numbers across weeks; when a rule changes, "why did my recovery
move" needs an answer.

- **`ModelVersion` in code** (`internal/scoring/version.go`), `year.month.n`: it names a
  state of the rules, it promises no compatibility, so no semver. Bumped by any change that
  moves a stored number (docs/SCORING.md → Model version).
- **Written on every row scoring writes** (`daily_scores`, the workout load), not per user:
  after a partial rescore, each number still says which model made it. Rows from before
  have `null`; nothing is backfilled, because nobody knows which rules made them.
- **The changelog is embedded JSON**, versioned with the code it describes: no table, no
  admin route, it cannot disagree with the deployed binary. A test pins newest-first, the
  newest entry equal to `ModelVersion`, and `affects` naming real trend metrics. Both
  languages are served; `summary` follows `Accept-Language`.
- Insights and behavior impact are computed on request from the stored scores, so they
  carry no version of their own.

## D81 — Max HR: Tanaka from the birth year, manual wins, a card instead of learning

190 for everyone overstates strain for most people over 30 and understates it for some
younger ones. The beta asks for a birth year in onboarding.

- **Tanaka (208 − 0.7 · age)** over 220 − age: it was fitted on far more people and
  is less biased past 40; both are ± ~10 bpm for an individual, which is why the card
  below exists. Age from the year alone: ± 1 year moves it < 1 bpm, and a birth date
  is more personal data than the formula can use.
- **Three sources like the sleep baseline (D67)**: `default`, `estimated`, `manual`.
  A birth year never overrides a hand-set value; `max_hr_manual: false` goes back. The
  estimate is computed on the PATCH, not re-derived each year: stored numbers move only
  when someone acts (and the model version says why, D80). Accounts with a changed
  `max_hr` before migration 0019 count as manual.
- **Observed, not learned**: two different days with a workout peak ≥ `max_hr` + 3 bpm
  produce the `max-hr-observed` card with the lower of the two highest days; the user
  decides. Raising it automatically would let one bad optical sensor rewrite every
  strain; one day can be a glitch, two days 3 bpm over is a pattern. Peaks come from
  the heart-rate samples inside workouts, not the app's per-workout `max_hr`, so every
  source is treated the same.
- The rule is pure (`internal/scoring/maxhr.go`, integer tenths so 183.5 rounds up);
  the store gets the estimate from the api because it cannot import scoring.

## D82 — Sources: a small table kept by ingest, fixtures for four set-ups

Beta testers will bring an Apple Watch, a ring, a Garmin, or only a phone. "Why is my
strain empty" and "which device does RYFT use" need an answer on the screen and in
support.

- **`user_sources`** (user, HealthKit source name, kind): the bundle id and the latest
  record's end, upserted by ingest from each sync (`store.SourcesOf`). `last_seen` only
  moves forward, a bundle id once sent is kept. The alternative, a `DISTINCT` over
  samples on every `GET /v1/me`, scans a month of heart rate per request. Migration
  0020 backfills the last 30 days (without bundle ids, which were never sent). A failed
  upsert is logged and does not fail the sync: the data itself is stored.
- **Kinds, not sample types**: eight kinds a person recognises (heart rate, HRV,
  breathing, SpO₂, energy, steps, sleep, workouts). VO₂max counts as heart rate (Apple
  estimates it from heart rate); resting heart rate too.
- **Bundle ids are not stored per sample**: only the sources table keeps them. Nothing in
  scoring needs them; source names stay the de-duplication key (D40).
- **`User.sources` is loaded by `GET`/`PATCH /v1/me` only**, not with the per-request
  user row.
- **`app_version` / `os_version`** on the device row, nil keeping the stored value, for
  support (the admin view) and to know which builds are still out there.
- **The `no-heart-rate-source` card**: data in the last 7 days but no `hr` kind. Fixed
  copy; nothing is computed from what is missing.
- **Golden fixtures** (`internal/scoring/testdata/sources`): synthetic HealthKit data for
  Watch + iPhone, Oura + iPhone, iPhone only (eight days) and Garmin one day, Watch the
  next, as declarative series (constant values per stretch), so every expectation, the
  strain included, is worked out by hand in the file instead of snapshotted. They run
  twice: through the pure functions `computeDay` uses (always, in CI), and stored the way
  ingest stores a sync, then scored by the engine (opt-in Postgres, like the other engine
  tests). The handler itself is not in the loop: its part is `validate` and the store
  calls, both tested on their own. No model version bump: they test the rules, they
  change none.

## D83 — Feedback in the database, the admin view as a query

- **A table, not an email or a third-party form**: the report stays next to the data it is
  about, is deleted with the account, and needs no new processor in the privacy policy.
  `request_ids` link it to the logs; `diagnostics` is any JSON object up to 32 KiB, so the
  app can add fields without a contract change. The message is never logged.
- **10 a day, counted in the database** (rolling 24 hours, under the user lock): the
  per-pod token buckets would allow replicas × 10, and a limit that sends a frustrated
  tester away should be exact. `429` with `Retry-After` until the oldest of the ten stops
  counting.
- **Open or resolved**: `resolved_at`, set with `POST /v1/admin/feedback/{id}/resolve`, so
  the report's "open feedback" means something without a ticket system.
- **The admin view is one query per call** (`GET /v1/admin/users`), not a table kept up
  to date: a handful of testers, read by one operator. The 14 days are UTC days; the app
  version is the device that synced last. Emails only with `?emails=1`, every call logged
  with the key's fingerprint and whether emails were shown: the view is for finding
  broken data, not for knowing who the testers are.
- **`scripts/beta_report.sh`** formats it with `jq` and `column`, no web UI.

## D84 — Dead letters in a stream of their own, written by the consumer

A message that fails its last delivery, or cannot be parsed, used to be dropped (logged
and counted). A bad payload from a new device type would vanish.

- **The consumer republishes it** to `ryft.dlq.<subject without "ryft.">` and terminates
  it in `RYFT`, with the original subject, consumer, delivery count and error (≤ 512 bytes)
  in headers. Listening to JetStream's max-deliveries advisories instead would need a
  further subscription, the system account's subjects, and a lookup of the message by
  sequence after the fact. If even the dead-letter publish fails, the message is NAKed
  and stays in `RYFT` (until its 7 days), and an alert says so.
- **`RYFT_DLQ`**: limits retention, 30 days, 64 MiB, oldest first out. Since stream
  subjects may not overlap, `RYFT` lists its four subjects instead of `ryft.>`; a new event
  subject is added to that list (`events.streamSubjects`). scoring updates `RYFT` first,
  then creates the DLQ. A rollback to a release before this one needs `scoring dlq drop`
  first, because that release sets `ryft.>` again.
- **Re-driving is handling again, not republishing**: `scoring dlq redrive` runs scoring's
  handler on its own letters with its own role, and deletes them when that works. No
  service gains the right to publish another's input subject. notify's letters are only
  listed and deleted: a morning push hours late is wrong, and the scores are stored.
- Permanent failures (the user was deleted) are still terminated without a letter:
  nothing to look at.
- **Backlog metrics come from the consumers** (`ryft_nats_consumer_pending`, polled every
  30 s with the consumer-info right each already has), not from a NATS exporter, which
  would need the monitoring port opened to the collector and another pod.

## D85 — Alerts as tested code; the operational checks

- **Prometheus rule format** under `deploy/grafana/alerts`, loaded into Grafana Cloud's
  ruler with mimirtool, rather than Grafana-managed rules exported as JSON: the format
  promtool can unit-test, and the rules are readable in review. `make alerts-check` runs
  promtool and mimirtool from their images, so neither needs installing; the tests make
  each new alert fire on synthetic series and stay quiet under its threshold. Routing
  (email, the Grafana mobile app) is the operator's, in Grafana Cloud.
- **The database alert has its own signal**: every service pings the database every 30 s
  (`ryft_db_up`, `ryft_db_ping_failures_total`). 5xx ratios need traffic; at beta volume
  an outage at night would go unseen until the first morning sync failed.
- **5xx as a ratio** (> 2 %) instead of a rate, and 410 Unregistered is not an APNs
  failure (the app was deleted; the token is dropped). The APNs alert needs ≥ 5 requests in
  its 30 minutes, so one failed push out of two is no page.
- **Sync staleness per tester is a panel, not an alert**, and the metric is aggregate:
  `ryft_users_by_last_sync{age}` counts accounts per bucket, so no user id leaves the
  cluster for Grafana Cloud; who is behind is `scripts/beta_report.sh`.
- **`make smoke-prod` is read-mostly**: one step from a dedicated user, no account
  creation or deletion (that is `smoke-supabase`'s, against throwaway accounts), so it
  can run after every deploy. `SMOKE_TOKEN` lets it run against the compose stack.
- **Push environments** are tested end to end with two fake APNs hosts behind the real
  client: a sandbox token reaches only the sandbox host, production tokens (TestFlight
  builds) only the production one, and 410 drops the token.
- Invite notes are no longer logged: a note may name the person the code is for.

## D86 — Legal drafts from what the system does; a demo account rebuilt per submission

- **The drafts describe the system, not a template**: every data type the app uploads,
  every table that holds personal data, every processor with the region it actually
  runs in (Supabase and AWS in Frankfurt, Grafana Cloud in London, Apple for sign-in and
  push), the retention that is configured (logs 14 days, backups 7 on Supabase Pro, dead
  letters 30). Two facts are easy to miss and are named: push texts carry scores through
  APNs, and the services' logs currently reach Grafana Cloud. What only the operator can
  decide or know is a visible `TODO(user)`, never a guess: controller, contact, DPAs,
  legal bases' review, the authority, minimum age (the API accepts 13, Art. 8 GDPR says
  16 in Germany), liability, governing law. No certification is claimed. Every page is
  marked `DRAFT` and `noindex` until the operator publishes it.
- **English at `/privacy`, German at `/de/privacy`**: `PRIVACY_POLICY_URL` stays the
  stable English URL the app opens; the Impressum is German (§ 5 DDG), with an English
  pointer page.
- **The app's universal-link paths** are the ones its `AppRoute` parses (the app's own
  list, which adds `/train/plan/*` and `/train/history` to the spec's); in a browser they
  get an "Open in RYFT" page instead of a 404.
- **The demo account is recreated, not topped up**: `DELETE /v1/me` and a new account each
  time, so the data always ends today, and the deletion path the reviewer may try is the
  one the script uses. The password is generated in `infra/vault` into an operator-only
  KV mount (no workload policy), so it never needs typing into a file. The account gets an
  invite code whenever the admin key is given, so it keeps working once the gate is on.
  The data is `synth.py --demo` (named sources with bundle ids, workouts three days a
  week); its default output is unchanged, so the smoke's expectations stay.

## D87 — Gate modes: off, warn, on (amended by D238)

The app cannot pass the gates yet (no invite or consent screen), and the operator's own
account was locked out of syncing by the consent check (STATUS → Incidents, 2026-10-20).
Both checks get three modes, `BETA_GATE` and `CONSENT_ENFORCE`:

- **`warn` serves the request** as if the account had access or consent, adds
  `X-RYFT-Gate` with the code(s) it would have refused with, counts it
  (`ryft_gate_refusals_total{mode="warn"}`) and flags the access line (`gate`). An app
  that knows the gates can prompt; one that does not keeps working.
  `GET /v1/me/sync` → `gate` carries it too.
- **Scoring follows `CONSENT_ENFORCE`**: a sync that `warn` lets through is scored as
  well, otherwise `warn` would accept data and show nothing. This processes health data
  without a recorded consent, which is only acceptable while nobody but the operator is
  in: the runbook makes "both `on`" the step before the first external invite, and
  `make smoke-prod` proves it (it takes the smoke user's consent and access away for one
  request each).
- **Defaults stay what production runs:** `BETA_GATE` off (unset today), `CONSENT_ENFORCE`
  on. A new default must not lock anyone out or open a gate on deploy.
- `ryft_beta_denied_total` becomes `ryft_gate_refusals_total{code,mode,service}`;
  refusals in `on` carry `mode="on"`.
- The access line now carries the refusal's `code` (or a 401's reason), and a client that
  hangs up is logged as `499` at info instead of a `500` error (two such "errors" were
  noise in the incident).

## D88 — Every admin call is audited, in the database

`admin_audit` (migration 0022) gets a row for every authorized `/v1/admin` call after it
is answered: the route pattern, the status, the request id, the admin key's fingerprint
(never the key), the user acted on and a few details (counts, the emails flag, a merge's
plan). A middleware writes it, so no handler can forget; handlers only add the target and
details. Calls without the key are not audited (there is no key to name; the metric counts
them). The table is append-only for the api (`SELECT`, `INSERT`) and has no foreign key on
the user, so it outlives deleted and merged accounts; it holds the pseudonymous id only.
Admin routes take a user's full id or its first 8 hex digits (`409` when those are not
unique), the form the logs and `beta_report.sh` show. An operator can grant beta access
without a code (`POST /v1/admin/users/{id}/access`), for an account that exists before
the app can redeem codes.

## D89 — The admin user view: one read answers "why can't this person sync"

`GET /v1/admin/users/{id}` puts together what the incident of 2026-10-20 needed from
four places: how the account signs in, what the gates know of it, what its devices
sent, and whether a second account shares its phone.

- **Identities come from Supabase** (`/auth/v1/admin/users/{id}`), asked on each read
  with the api's existing admin key, not copied into our tables: the source of truth for
  "Apple or email, and which address", and nothing to keep in sync. Without the key
  (apikey mode) the view says so.
- **Emails are masked** to `t***@g***.com` unless `?emails=1`, like the list (D83); the
  view is for finding broken data. Apple relay addresses are flagged, since they are the
  usual reason one person has two accounts.
- **Duplicates by device id**: an install's `device_id` is per install, so two accounts
  on one id within 30 days are one phone signed in two ways (or shared). Counted in the
  list (`dup`), named in the detail. "Data volume" is days scored and the newest record,
  not a row count of a year of heart rate.
- **What a sync carried** is kept on the device row (`last_sync_summary`), and a push
  token's last delivery on the token (`last_success_at`, the only column notify may
  update): two small columns instead of a sync log.

## D90 — Merging accounts: one transaction in the api, then the deletion path

- **In the api, as an admin route**, not a script with database credentials: the
  operator already holds the admin key; the merge is audited like every admin call, and
  the deletion of the merged account goes through the same code as `DELETE /v1/me`
  (Supabase auth user, Apple grant, tombstone). The api's role gains exactly the owner
  column of the raw tables (`UPDATE (user_id)` on samples, sleep, workouts, devices) and
  writes on `user_sources`; it can already read and delete those rows, so moving them adds
  integrity risk, not confidentiality risk.
- **Kept account wins on every conflict**, counted per table: a merge never overwrites
  what the person sees in the account they keep. Rows tied to a parent by `(user_id, id)`
  (sessions with their exercises and sets, journal days with their entries) are copied
  for exactly the parents that move, and the originals go with the deleted account.
  Custom exercises and behaviors of the same slug collapse into the kept one, with the
  moved sessions and entries pointing to it. Scores are not moved but rescored, and
  personal records recomputed.
- **The dry run runs the same transaction and rolls it back**, so its counts are the real
  ones. The direction check (more scored days into fewer needs `force=1`) and the second
  call's `404` keep a typo from merging the wrong way twice.

## D91 — Sync state on the server, so the app can stop guessing

- **`score_queue`** (migration 0025): ingest records the days a sync queued, scoring marks
  each done when a computation that *started after* the queueing finishes (a sync during a
  computation stays pending), and prunes after a day. `GET /v1/me/sync` → `pending_days`
  and today's state come from it; the same rows give the sync-to-score latency without a
  metrics backend in the request path. A small table, not NATS introspection: the queue
  says what is outstanding per message, not per user and day.
- **Today's state, in order:** `pending` (a queued day, or fresh data without a score),
  `stale` (nothing new for 20 h: the watch is off, or the app is not syncing),
  `waiting_for_sleep` (scored, no night yet), `scored`. Past days are `pending` or
  `scored`. `expected_within_sec` is the recent p95 over every account (6 hours, 30 s
  until there are 5 entries), so the app polls at the pace scoring actually keeps.
- **`gate` is reported in `warn` mode too**: that is how an app that knows the gates
  learns to prompt before they are switched on.
- **`account_hint` only for an account without data**, from the device id the app sends
  in `X-RYFT-Device-ID`, and only "another account, its provider, its last sync": no id,
  no email, nothing that tells a stranger on a shared phone more than that someone else
  uses RYFT here. An account that has data is not second-guessed.
- **The sync answer counts what it ignored:** an identical re-send is a `duplicate` (and
  is no longer rewritten: `ON CONFLICT … WHERE … IS DISTINCT FROM`), a record more than a
  day in the future or older than 400 days is `outside_window`, and an unknown sample type
  or sleep stage is `unknown_type` instead of a `422` for the whole page. A new HealthKit
  type in the app must not stop every sync of that user.
- **`resync_requested_at`** is a timestamp, not a flag the server clears: the app compares
  it with its own last full re-sync, so nobody has to tell the server it happened.

## D92 — The sync-to-score time is measured, and the app is told when scores are there

- **`ryft_sync_to_score_seconds`** is observed when scoring marks a queued day done
  (D91): from ingest accepting the sync to the score written, per day. That is the wait
  a tester sees, unlike the scoring duration alone (which leaves out the queue). Alert at
  a p95 over 2 minutes for 15 minutes; the dashboard shows p50 and p95.
- **A silent refresh after every sync's scored days**, not only the morning score: on a
  new subject `ryft.refresh` (scoring → notify, its own consumer filter and permissions),
  the newest day of the sync, collapsed per day. One per account every 5 minutes,
  shared with the morning refresh, in notify's memory (one replica): iOS rations
  background pushes, and a burst of syncs during a backfill needs one, not twenty. Not
  claimed per day and not retried: a refresh minutes late is no use, and the app polls
  `GET /v1/me/sync` meanwhile.
- **A handler panic no longer takes a consumer down.** The first refresh event crashed
  notify in the local smoke (a field lookup assumed only two event kinds); event handlers
  now run guarded, so a panic is an error, retried, and dead-lettered at the last
  delivery (`ryft_event_handler_panics_total`). The NATS permission test had also
  narrowed the live notify consumer to its old subjects; the test and the code now share
  the filter list.

## D93 — migrate writes a role's password only when it changed

- **Why:** the deploy of `4dd5aa1` took the database away from every service for 17
  minutes (STATUS → Incidents). migrate set the four role passwords on every run, each
  time with a new SCRAM salt; Supabase's pooler caches the verifiers, fails each
  connection checked against a stale one once, and blocks a project and client address
  for 120 s after 10 failures in 150 s. All pods share the cluster's NAT addresses, and
  a deploy restarts them right after migrate.
- **How:** before `ALTER ROLE`, migrate logs in as the role, at the owner's host with the
  pooler's user name for it (`ryft_api.<ref>`). That works: nothing is written. The role
  has no LOGIN, or the server refuses the password (`28xxx`): it is set. The pooler
  refuses with "recently changed": one more try after a second. Anything else (network,
  the circuit breaker) fails migrate rather than rewriting credentials blindly.
- **Not chosen:** a salt derived from the password (the same verifier on every run, but
  the salt is handed to anyone who starts a login, and it would be a fast hash of the
  password); a table of password fingerprints (a fast hash at rest, and its first run
  would still rewrite every verifier). The login costs one connection per role per
  deploy; a really rotated password costs one failed login per role, four at most.
- **Still true:** rotating a password (`terraform apply -replace='random_password.db[…]'`)
  fails the running pods' new connections until they restart with it; rotate one role at
  a time.

## D94 — Argo CD: a Terraform add-on that deploys the workloads, private, non-HA

- **Amended by D188:** the Vault server is a Terraform add-on too, not an Argo CD application (#145). **Amended by D202:** the Grafana Cloud collector becomes a Terraform add-on (decided, not built).
- **Why:** deploys were a human pinning `newTag` and running `make k8s-apply-eks`; two
  incidents in two days came from that path (a mistyped SHA, repeated applies during the
  database outage). With Argo CD, git is the only way to `ryft-prod-euc1`, a merged pin
  deploys itself, and drift is corrected.
- **Installed like every add-on:** the `argo-cd` chart 10.10.5 (Argo CD v3.5.6; the chart
  ships v3.5.5) as a Helm release in `infra/aws/kubernetes`. **Argo CD manages workloads
  only**; the add-ons, Argo CD included, stay in Terraform, so no object has two owners
  (also in `CLAUDE.md`).
- **Non-HA:** one replica per component and Redis without a volume (a cache). The HA
  option (redis-ha, two server and repo-server replicas, a sharded controller) is not
  worth it for ten applications on three nodes. Requests are set explicitly (the chart
  sets none) and small: the nodes are CPU-bound (1930m allocatable each; one already has
  1700m requested).
- **No Ingress:** the UI and API through `kubectl -n argocd port-forward svc/argocd-server
  8080:443`. An Ingress on `argocd.ryftapp.de` behind a Traefik IP allow-list with SSO
  (Dex, GitHub) is the later option, once someone besides the operator needs the UI.
- **The admin account stays, with a password Terraform generates** (`random_password`,
  its bcrypt hash in the chart's `argocd-secret`, read with `terraform output -raw
  argocd_admin_password`). The spec offered the initial password moved into Vault; with
  the hash set, Argo CD never writes an `argocd-initial-admin-secret` at all, there is
  nothing to move or delete, and the password lives where every other generated
  credential does (HCP Terraform state). No local operator account: without SSO it would
  be a second password for the same person.
- **The repository is public** (the user keeps it so), so Argo CD reads it over HTTPS
  without a deploy key and nothing goes into Vault for it. Making it private later means
  a read-only deploy key from `infra/vault` and a repository Secret.
- **Pod Security `restricted`** on namespace `argocd` (every chart container complies),
  the chart's NetworkPolicies, and one more that admits the metrics ports from the VPC
  (the collectors scrape from node addresses), like `metrics-from-nodes.yaml`.
- **Health:** `argocd-cm` restores the health of Application objects (removed in Argo CD
  1.8) and adds one for `VaultStaticSecret` (healthy once the operator reports
  `SecretSynced`), so the app-of-apps waits wave by wave (D95). Dex and Argo CD
  notifications are off: alerts come from Grafana Cloud (`argocd_app_info`,
  `argocd_app_sync_total`).
- **Tried on a kind cluster** with the chart and the Terraform values: restricted pods,
  no initial secret, the waves, a failed PreSync blocking a sync, a dry-run sync of the
  whole platform through the project's rules.

## D95 — The app of apps: waves, migrate before, the smoke after, rollback by revert

- **Amended by D200, D201 (decided, not built):** two more applications in wave 4 for the Vault operator objects of the import
  service and of monitoring.

- **Layout:** `deploy/argocd/root.yaml` (applied once by hand) syncs
  `deploy/argocd/apps`, one Application per unit. Wave 0: `vault-<svc>`, the generated
  Vault Secrets Operator objects of each namespace (Terraform writes them, Argo CD applies
  them; healthy when `ryft-secrets` exists). Wave 1: the broker (`broker-nats`, the NATS
  StatefulSet now in its own overlay `eks-nats`). Wave 2: `ryft-platform`
  (`overlays/eks`: services, Ingresses, NetworkPolicies, the web site, image pins).
- **Projects:** `ryft-root` may only create Applications in `argocd`; `ryft` only deploys
  this repository into `ryft-*` namespaces, with `Namespace` the one cluster-scoped kind
  (ryft-web's). The built-in `default` project is emptied, so a typo cannot deploy
  through it. Projects are applied by hand, never synced, so a commit to an application
  cannot widen them. Namespaces stay `infra/vault`'s (`CreateNamespace=false`).
- **Sync:** `automated` with prune and self-heal, server-side apply, `PruneLast`, and
  **server-side diff** on every application: in the kind test the NATS StatefulSet stayed
  OutOfSync forever under client-side diff (the API server adds `status` and `volumeMode`
  to `volumeClaimTemplates`). `ignoreDifferences`: the HPAs' `spec.replicas` and the
  operator's `restartedAt` annotation. No finalizers: deleting an Application leaves its
  objects running.
- **migrate is a PreSync hook** of ryft-platform: every sync migrates first, and a failed
  migration fails the sync before anything rolls (tried in kind: no object was created).
  It runs on *every* sync, also self-heal ones; that is harmless only because migrate no
  longer rewrites unchanged role passwords (D93). Its ConfigMap reference is optional: the
  hook runs before the sync creates the ConfigMap, and in kind a required one left the Job
  waiting until its deadline.
- **The smoke is a PostSync hook** (`/app smoke` in the migrate image, namespace
  `ryft-smoke` from infra/vault with only the smoke user's sign-in): every service's
  `/readyz`, a sign-in, one tiny sync, the day rescored with the served model within 90 s.
  A failure fails the sync, and `ArgoSyncFailed` fires. scoring got a Service and notify an
  `http` port for it; the four services admit `ryft-smoke` on 8080. It reuses the migrate
  image instead of a tools image: one image less to build, pin and promote.
- **Rollback is `git revert` of the pin commit**, which Argo CD deploys like any other.
  *(Since D107 the revert needs a `# hold: <group>` line, or the next push re-pins the
  reverted code.)*
  Argo CD does not roll back by itself, and Argo Rollouts (canaries, automated analysis) is
  not worth it yet: two replicas of api, one of scoring, a few testers; a revert takes
  minutes and the smoke tells whether it helped.
- **Adoption:** ryft-platform starts without `automated`; the first manual sync must show
  no diff against what runs (fixes go into git, not the cluster); the next commit turns
  automation on.
- **A wave that never gets healthy holds every later change**: the root's sync waits on
  it, so a fix to any application is not delivered until that operation is terminated
  (`argocd app terminate-op ryft-root`, OPERATIONS → Deploy). Seen in kind with a NATS that
  could not start.

## D96 — Images reach the cluster by a PR CI opens; Image Updater second

- **CI promotion:** after `images` on a push to main, `promote` (`scripts/promote.sh`)
  pins the five images in `overlays/eks` to that commit on `deploy/<sha>`, opens "deploy:
  images <sha>" with the commits since the running pin, and turns on auto-merge
  (squash). The PR's required checks (`manifests` now also asks ghcr.io for every pinned
  image) gate it; the merge is the deploy, Argo CD syncs it (D95). Git history is the
  deploy log; closing the PR blocks a deploy; reverting the merge rolls back.
- **No deploy without a backend change** *(per image group since D107: services for
  `backend/`, web for `web/`)*: promote skips when nothing under `backend/`
  changed since the running pin, so docs commits do not restart pods, and the merged pin
  commit (whose own images CI builds) does not promote itself. An older open deploy PR is
  closed as superseded: two would edit the same lines and the second would conflict.
- **A GitHub App** (the user's choice) makes the token: PRs opened with `GITHUB_TOKEN` do
  not trigger the workflows auto-merge waits for, and a fine-grained token is tied to a
  person and expires. Client ID as repository variable `RYFT_DEPLOY_APP_CLIENT_ID`, key as
  secret `RYFT_DEPLOY_APP_KEY`; permissions contents and pull requests (write), on this
  repository only. Branch protection on main requires the checks for PRs; the user, as
  admin, may still push directly (enforce_admins off), and such a push deploys through
  its own promote PR.
- **Not Argo CD Image Updater** (v1.3, CRD-based, still writes kustomizations back): our
  tags are short SHAs, so it would need `newest-build`, which orders by the image's build
  date from registry metadata (one manifest pull per tag, and wrong with reproducible
  builds); a bad build would be committed by a controller with no PR to stop, and the
  commit list a reviewer sees would be gone.
- **No `:latest`** from CI or `make push`: nothing should deploy it, and a tag that moves
  hides which code runs. `make images` still tags a local `:latest` for the self-hosted
  overlay, which you build yourself.
- **`make k8s-apply-eks` is break-glass** (Argo CD down): it warns that self-heal reverts
  what git does not hold, applies `eks-nats` and the platform without the PostSync smoke,
  and still checks the registry first.

## D97 — RabbitMQ replaces NATS JetStream (supersedes the messaging review)

- *(Superseded by D218: Pub/Sub replaces RabbitMQ, with a topic per key, push with OIDC and a DLQ per consumer.
  `amqp091-go`, `amqpbus` and `cmd/rabbitmq-topology` are deleted. `deploy/rabbitmq` goes with D3.)*
- **What it reverses:** `docs/MESSAGING_DESIGN.md` (2026-10-19) recommended keeping NATS
  and asked to record that as D48. It never was (D48 is Apple's token revocation); this
  entry supersedes the review itself.
- **Why, in the user's words:** familiarity with RabbitMQ and AMQP; the management UI to
  look at queues, messages and dead letters by hand; and dead-letter exchanges, delivery
  limits and TTL queues that the broker does, instead of code (JetStream needed our own
  dead-letter stream, D84).
- **The costs, as they are:**
  - **Memory:** three Erlang nodes at 1 GiB requested each (1.5 GiB limit), against one
    NATS pod using about 64 MB, plus two operators (128 MiB each requested): about
    3.3 GiB more requested, a sixth of the cluster's memory. CPU: 600m for the nodes
    (not the 1.5 CPU the spec's 500m each would be; below), 40m for the operators.
  - **Storage:** 3 × 10 GiB gp3 instead of 2 GiB, about $2.40 a month.
  - **No publish dedup:** JetStream dropped a repeat within two minutes (`Nats-Msg-Id`);
    RabbitMQ delivers it. Every handler must be idempotent, and the tests must show it
    (D100).
  - **Least privilege rebuilt** from vhost and topic permissions (D99), with a new test.
  - **A cutover** in four releases with both brokers side by side (D102).
- **Kept:** events are triggers, not records; Postgres is the truth; `POST
  /v1/recompute` replays; at-least-once delivery with idempotent handlers.
- **The pieces:** the official RabbitMQ Cluster Operator 2.23.1 and Messaging Topology
  Operator 1.20.4, their release manifests vendored byte for byte
  (`deploy/rabbitmq/operators/SHA256SUMS`, checked by `make k8s-validate`), changed only
  by kustomize patches. Not Bitnami: its images and charts stopped on 2025-08-28
  (`bitnamilegacy` is frozen, there is no `bitnamisecure/rabbitmq`, the chart stops at
  16.0.14). RabbitMQ 4.3.6, pinned by digest, the operator's tested series (its default
  is 4.3.4); 4.4.0 came out the day before and waits for a patch release.
- **The cluster:** named `rabbitmq` (the spec said `ryft`, but its own `AMQP_URL` names
  `rabbitmq.ryft-rabbitmq.svc`); three replicas with required anti-affinity, one per
  node; its own PDB (`maxUnavailable: 1`, the operator makes none); a termination grace
  of 10 minutes instead of the operator's week, which could hold a node drain that long;
  requests 200m / 1 GiB instead of the spec's 500m: the nodes are CPU-bound (1930m
  allocatable each, one had 1700m requested), and RabbitMQ idles far below that at
  under one message a second. The operators' own 200m and 300m are patched to 20m.
- **Two deviations in the client's interface:** `ConsumeIngested`/`ConsumeNotify` stay
  typed instead of one `Consume(queue, handler)`, as the call sites use them; and a
  `Permanent` error is dropped and counted (`dropped`), not dead-lettered: an event for a
  deleted account cannot succeed and need not, and in the DLQ it would page
  (`DeadLetters`) for nothing, as on NATS (D84).

## D98 — TLS inside the cluster from an internal CA in cert-manager

- **Amended by D190:** Vault has a CA of its own, `ryft-vault`, beside `ryft-internal` (#145, #151).
  *(Superseded: the AMQP mutual TLS and the per-user passwords by D218, where Google API TLS, per-SA tokens and push
  with OIDC replace them. The cluster and its CA go with D216.)*
- **The CA:** ClusterIssuer `ryft-internal` in `infra/aws/kubernetes`'s `cluster-issuers`
  chart (cert-manager is Terraform's): a self-signed bootstrap issuer signs one ten-year
  ECDSA root in the `cert-manager` namespace; the CA issuer signs from it. Nothing
  outside the cluster trusts it.
- **One Certificate per namespace that speaks AMQPS:** the broker's server certificate
  (the Service's and each node's names); in each client namespace the service's own
  (Secret `amqp-client`); in `rabbitmq-system` one for the Topology Operator, which talks
  to the management API over HTTPS and trusts the system pool, so its `ca.crt` is
  mounted into `/etc/ssl/certs`.
- **Mutual TLS, found in the kind test:** once a CA is configured the Cluster Operator
  turns on peer verification (`ssl_options.verify = verify_peer`), and a client without a
  certificate is refused in the handshake. So every service presents its namespace's
  certificate as well as its password: two factors, neither enough alone (tried: no
  certificate → refused in the handshake, a wrong password → 403). The client reads the
  key pair at every handshake, so cert-manager's renewal is used on the next connection
  without a restart. The management API verifies but does not require client
  certificates (the Topology Operator connects without one). The certificates could
  later replace the passwords (the EXTERNAL mechanism).
- **Not Vault, not trust-manager:** Vault would need a PKI engine or a CA made by
  Terraform, its private key in Vault and state, and a second synced Secret per
  namespace; trust-manager is one more add-on and, for Secret targets, reads every
  Secret in the cluster. A Certificate per namespace gives the same `ca.crt` with nothing
  new, renewed by cert-manager.
- **Listeners:** AMQP only on 5671 (TLS), the management UI only on 15671 (port-forward);
  plain AMQP is off (`disableNonTLSListeners`). Metrics stay plain on 15692 (counters and
  queue names, no message contents), set back in `additionalConfig`, because the
  collectors scrape by annotation over HTTP and the operator's TLS mode drops 15692.
  Erlang distribution between the nodes is plain TCP inside the namespace's
  NetworkPolicy; inter-node TLS needs the cert-manager CSI driver, not worth it here.
- **Renewal, not yet seen:** the server certificate lives a year and is renewed 30 days
  early. Whether RabbitMQ serves the new one without a restart (Erlang caches PEM
  files) is untested; `CertificateExpiring` fires 14 days before expiry, and a rolling
  restart of the cluster is the fallback (OPERATIONS → RabbitMQ).

## D99 — The broker's topology, retries and rights, defined once in Go

- **Amended by D150:** a replayed dead letter is marked `x-ryft-replayed` and handled as a redelivery; the
  key of a delivery from `ryft.events` is its routing key; `dlq.sh` replays only bound keys. **Amended by D218:**
  on Pub/Sub the rights are IAM per topic (one topic per key, `roles/pubsub.publisher` on its own topics only). The
  topology is in `internal/events/pubsub/topology.go`, and `cmd/pubsub-topology` writes `infra/gcp`'s tfvars and the
  emulator's `init.sh`. The retries are Pub/Sub's retry policy (10–600 s) and 20 attempts.
- **One source:** `internal/events/amqpbus/topology.go`. `go run ./cmd/rabbitmq-topology`
  writes the Topology Operator's objects (`deploy/rabbitmq/topology/topology.yaml`, 63
  objects in sync waves after the cluster) and the definitions compose and the tests
  load (`deploy/rabbitmq/dev/definitions.json`, dev passwords); `make k8s-validate`
  fails when either differs. The client takes its names and tiers from the same code,
  and the tests import the same topology at a hundredth of its TTLs.
- **Exchanges:** `ryft.events` (topic) for the five events, routing keys = today's
  subjects without `ryft.`; `ryft.retry` (topic) into the delay queues; `ryft.requeue`
  (direct), written only by the broker, back from them; `ryft.dlx` (topic) to the dead
  letters. **Queues:** `scoring`, `notify`, their delay queues and `dlq.scoring`,
  `dlq.notify`, all quorum (three replicas), without arguments: delivery limits, TTLs,
  dead-lettering and length limits are **policies**, since the Topology Operator cannot
  change a queue's arguments once it exists. Every queue matches exactly one policy
  (checked).
- **Retries without the delayed-message plugin:** a failed message is acknowledged and
  a copy with `x-ryft-attempt` goes to `<queue>.retry.5s`, `.30s`, then `.5m`; each holds
  it for its TTL and dead-letters it through `ryft.requeue` back to its own queue (not
  through `ryft.events`, so a retry never reaches another consumer). After six attempts
  (about 16 minutes; NATS gave up after ten in 5) it is dead-lettered with the same
  `Ryft-*` headers as on NATS. notify's deferrals (quiet hours) use
  `notify.wait.{1m,15m,1h}`, the longest tier that fits, again until it stops deferring;
  they are not attempts. Not the delayed-message plugin: it is a community plugin, not
  in the official image, and keeps delayed messages outside the replicated queues. Not
  (yet) RabbitMQ 4.3's new delayed retry for quorum queues, which delays rejected
  messages by broker settings: a few months old, and our attempt count and tiers would
  become broker configuration we cannot see per message. The TTL queues are plain,
  visible in the management UI, and tested.
- **The broker's own limit stays as the safety net:** `delivery-limit` 10 on the work
  queues counts what the client does not settle (a consumer that dies holding the
  message, a reject when even the retry publish fails); then the broker dead-letters it
  itself (at-least-once, `overflow: reject-publish`).
- **Publishing:** confirms on, `mandatory` (an event without a bound queue is an error
  and `ryft_events_published_total{result="unroutable"}`, not a silent drop), persistent,
  `message_id` = the old dedup id, `type` = the event key (decoding goes by type, since
  a retry or requeue arrives with another routing key).
- **Least privilege, D43's granularity restored:** no service may configure anything.
  Writes are limited by exchange and, with **topic permissions**, by routing key: ingest
  may publish only `ingested`; api `ingested` and `session.completed`; scoring `scored`,
  `session.scored`, `refresh`, plus its own retry keys (`scoring.*`) and dead-letter key;
  notify only its retry and dead-letter keys. Each reads only its own queue; nobody may
  write `ryft.requeue`. `permissions_test.go` proves the matrix against a broker (and
  fails when one rule is loosened). Users come from the operator's `User` objects, their
  passwords from Vault.
- **The DLQ** keeps letters 14 days (a policy); `scripts/dlq.sh` lists, shows, replays and
  purges them through the management API (OPERATIONS → RabbitMQ).

## D100 — Idempotent handlers instead of publish dedup

- *(Kept by D218: it still holds on Pub/Sub. Push delivers at least once, also after an ack, and has no exactly-once.)*
- JetStream dropped a repeat of the same message id within two minutes; RabbitMQ
  delivers every copy (a publisher retry after a lost confirm, a redelivery after a
  consumer died). The handlers are idempotent, and now tests say so:
  - **scoring:** the same event handled three times leaves every day's row as it was
    (apart from `computed_at`) and announces the morning once
    (`TestTheSameEventTwiceChangesNothing`); `scored` is only published for today's first
    recovery, so duplicates do not cascade into notify;
  - **notify:** a repeated `scored` or `session.scored` sends one push (the claim per
    user, day and category), a repeated `refresh` is throttled (five minutes).
- So no `processed_events` table: it would cost a write per event to protect handlers
  that need no protection. `message_id` stays stable for the same trigger, so one can be
  added if a handler ever stops being idempotent.

## D101 — A transactional outbox in front of the broker

- **Amended by D219:** the relay is inline (`Flush` after the commit, 2 s) plus the tick's `Sweep`; there is no poll.
  The drains are serialised per publisher with a transaction-level advisory lock, so one user's rows keep their
  order. Rows have a not-before time (`available_at`, 0034), and only due rows are drained and counted. notify is a
  publisher too (its deferrals). The broker is Pub/Sub (D218).
- **The gap it closes, there with NATS too:** a sync was stored, then its event
  published; a failed publish (the broker down, a pod killed in between) left days
  stored but never announced, until scoring's 15-minute pass (only yesterday and today)
  or a recompute. Now every event is a row in `outbox` (migration 0026), and each
  publishing service runs a relay that sends its rows with the broker's confirm and
  marks them published.
- **Same transaction where it matters:** ingest writes the sync's queued days and their
  event together (`QueueDaysWith`); the api writes a session's completion and its
  `session.completed` together (`CompleteSessionWith`). The other events (recompute,
  merge, session edits, scoring's `scored`/`session.scored`/`refresh`) are a row of their
  own right after the write: no atomicity with it, but durable across a broker outage,
  which is what loses them today. Moving scoring's writes into one transaction with its
  events would reach deep into the engine for little: a lost morning push is the worst
  case, and the outbox already removes the common cause.
- **The relay:** per publisher, oldest first, 50 rows a pass in one transaction holding
  their locks (`FOR UPDATE SKIP LOCKED`, so replicas never send a row twice), stopping
  at the first failure to keep the order, backing off 1 s → 30 s; a commit kicks it, so
  no poll stands between a sync and scoring (the poll, every 2 s, is for a crashed
  replica's rows and retries). A crash between a send and its mark sends it again;
  the handlers are idempotent (D100). A row that failed 50 times is set aside (an
  event no queue takes) and alerted (`OutboxStuck`) instead of holding up the rest.
  Published rows stay a week, then the relay deletes them; an account's rows go with
  the account (`DELETE /v1/me`, merge).
- **Rows, not a log table elsewhere:** the outbox is the services' own database, written
  with their own least-privilege roles (each may insert and mark, `UPDATE` only of
  `published_at`, `attempts`, `last_error`), so it needs no new credential. It works for
  either broker, which is why it ships before the cutover (D102): the same relay drains
  into NATS today and RabbitMQ tomorrow.
- **Metrics:** `ryft_outbox_pending`, `ryft_outbox_oldest_seconds`, `ryft_outbox_stuck`,
  `ryft_outbox_relayed_total`; alerts `OutboxBacklog` (an event waiting over 5 minutes)
  and `OutboxStuck`.
- **ingest's readiness leaves the broker out** (it failed while NATS was away): with the
  outbox, a sync needs only the database, so Traefik keeps sending syncs during a broker
  outage, as it does to the api, whose readiness never included the broker. scoring and
  notify still report unready without it (they have nothing to do). At start-up every
  service still waits for its brokers (up to five minutes, then exits).

## D102 — The cutover: four releases, configuration only

- *(Superseded by D218: the services speak Pub/Sub only, and `EVENTS_*` and `AMQP_*` are gone. Nothing is cut over in
  place: the platform is rebuilt on GCP (D216).)*
- **Both brokers in one build:** the services carry both clients until the last
  release; which one they publish on (`EVENTS_BACKEND`) and which they consume
  (`EVENTS_CONSUME`, a list) is configuration (`internal/eventbus.PlanFor`). Each
  release is a commit on branch `rabbitmq-cutover`, merged when the one before has been
  verified; none needs a new image, so a rollback is a revert of one merge, and Argo CD
  applies either.
  1. **R1, the broker:** `infra/vault` (namespace `ryft-rabbitmq`, the users' passwords,
     the services' `AMQP_PASSWORD`), `infra/aws/kubernetes` (the internal CA, Argo CD's
     health checks for the RabbitMQ kinds), `project.yaml`; then the merge adds the
     applications `vault-rabbitmq` (wave 0), `rabbitmq-operators` (1), `broker-rabbitmq`
     (2), and `ryft-platform` moves to wave 3. Nothing uses the broker yet.
  2. **R2, dual consume:** scoring and notify consume NATS and RabbitMQ; everything still
     publishes on NATS. Proves the consumers' connections, certificates and rights with
     nothing at stake (`ryft_amqp_connected`).
  3. **R3, publish on RabbitMQ:** ingest, api and scoring publish to RabbitMQ through the
     outbox. The consumers still drain what is left on NATS. A day of normal traffic
     before R4: the sync-to-score latency, queue depths, dead letters, the outbox.
  4. **R4, NATS goes:** once stream `RYFT` has nothing pending and `RYFT_DLQ` was looked
     at (re-driven or deleted, `scoring dlq`): the NATS client, its configuration, the
     `eks-nats` overlay, the applications `broker-nats` and `vault-nats`, the JetStream
     alerts, compose's NATS, the CI step; RabbitMQ becomes the default. Then
     `infra/vault` without the `nats` service (it deletes namespace `ryft-nats` with
     its volume), and `scripts/rescore_all.sh 3` for whatever was in flight.
- **Why not one release:** the order makes every step reversible. Consumers before
  publishers, so no event is published where nobody reads; publishers before removal, so
  NATS drains before it goes. Not a dual publish (both brokers at once): every event
  would be handled twice on purpose, and an outage of either would fail publishes.
- **No finalizer on the applications** (D95): removing `broker-nats` from `apps/` leaves
  NATS running, orphaned, until `infra/vault` deletes its namespace; the StatefulSet's
  volume follows the namespace (its claim is not deleted with the StatefulSet).
- **Rollback after R4** is restoring NATS (revert R4, `infra/vault` with NATS); events
  in RabbitMQ at that moment are lost to the consumers, which `rescore_all.sh` repairs.
- **R4 as built:**
  - `overlays/eks/events.yaml` keeps `EVENTS_BACKEND=amqp` (and `EVENTS_CONSUME=amqp`)
    for every service: Argo CD applies R4's manifests minutes before the promote PR
    pins R4's images, and an older image with the setting unset would fall back to
    NATS. The R4 images refuse anything but `amqp` there, so a stale `nats` fails at
    start-up instead of being ignored.
  - Secret `amqp-client` is no longer optional in the Deployments: without NATS a pod
    without its certificate cannot work, so it waits for cert-manager instead of
    starting and failing its TLS handshakes.
  - `ryft_nats_redeliveries_total` keeps its name: the dashboards and the history read
    it, and a rename buys nothing.
  - `overlays/local-postgres` runs one RabbitMQ node (no anti-affinity, the default
    storage class, 512 MiB) from the same `deploy/rabbitmq`, and needs cert-manager;
    `make k8s-apply-local` adds the internal CA with `helm template` of the EKS chart
    and the operators first. `scripts/local-secrets.sh` upgrades a directory from the
    NATS days in place (Postgres keeps its owner password).
  - `make k8s-rabbitmq` (break-glass and local) applies server-side with
    `--force-conflicts`: the operators' CRDs are too large for client-side apply, and
    break-glass means taking over what Argo CD owned.

## D103 — Argo CD and the RabbitMQ management UI on public hosts (supersedes D94's "No Ingress")

- **The user's call:** log in from anywhere, without a port-forward, at
  `https://argocd.ryftapp.de` and `https://rabbitmq.ryftapp.de`. Not behind an IP
  allow-list (no fixed address) and without SSO for now; D94's "allow-list plus SSO"
  stays the stronger option.
- **The path, as for `api.ryftapp.de`:** Traefik's HTTPS entrypoint only, a Let's
  Encrypt certificate from cert-manager (DNS-01), the record from ExternalDNS. Two
  Traefik middlewares in front of each: a per-IP rate limit (Argo CD 50/s, bursts of
  200, since its UI polls and streams; RabbitMQ 20/s, bursts of 100) and HSTS.
- **Argo CD** (Terraform, `infra/aws/kubernetes/argocd.tf`): the chart's own Ingress;
  `server.insecure`, so argocd-server speaks HTTP behind Traefik like the RYFT services
  (TLS inside the cluster is not covered there either, ARCHITECTURE → Namespaces and
  trust); `global.domain` is the host, which also sets the URL in argocd-cm. The login
  is the local `admin` with its 32-character generated password; Argo CD slows a user
  down after five failures in five minutes. The CLI goes through Traefik with
  `--grpc-web`.
- **RabbitMQ** (git, `deploy/rabbitmq/cluster/management.yaml`, so it comes with R1):
  a Service of its own for the management port (the operator's Service stays as it
  is), Traefik speaking TLS to 15671 and **verifying** the node's certificate against
  the internal CA under the name it holds (a ServersTransport; tested in kind: a wrong
  name fails with an x509 error), the broker's network policy admitting Traefik on
  15671. The login is the operator's default user (Secret `rabbitmq-default-user`, the
  broker's administrator, generated by the Cluster Operator, not Terraform).
- **What it costs:** both logins are exposed to password guessing and to any
  pre-authentication flaw in the two UIs; an Argo CD admin can change projects, so it
  is as powerful as the cluster. The passwords are long and random, and the rate limits
  cap guessing, but a flaw would not care. Restrict later with a Traefik `ipAllowList`
  middleware next to the rate limit (or SSO for Argo CD); both are one change.

## D104 — RabbitMQ: memory request equal to the limit

- *(Superseded by D218: no RabbitMQ.)*
- After R1 (2026-10-21) the Cluster Operator reported `NoWarnings=False`
  (`MemoryRequestAndLimitDifferent`): the broker derives its memory alarm from the limit
  (80 % of it is RabbitMQ's, the alarm at 60 % of that), so with a 1 GiB request and a
  1.5 GiB limit a node could plan with memory the scheduler never reserved for it.
- The limit drops to the request, 1 GiB: the requested memory and the cost stay as in
  D97, the alarm moves from about 0.77 GB to 0.52 GB, four times a node's idle use
  (130 MiB) at under one message a second. Raising both to 1.5 GiB would cost 1.5 GiB more
  requested for nothing measured.
- Done before R2, while nothing uses the broker: the operator replaces the nodes one at a
  time (the PDB), and quorum queues keep two of three members throughout.

## D105 — Argo CD: an autoscaler is healthy unless it cannot scale

- The sync of R4's images (2026-10-21 13:22 UTC) failed once: right after the rollout
  the api's HPA reported `ScalingActive=False` (`FailedGetResourceMetric`, "no metrics
  returned from resource metrics API"), because the metrics server has no samples for
  new pods for about a minute. Argo CD's built-in HPA check calls that Degraded, and a
  Degraded object fails the sync; the retry two minutes later passed. Every rollout of
  api or ingest can hit it.
- A custom check in `argocd-cm` (`infra/aws/kubernetes/argocd.tf`): Degraded only when
  the HPA cannot scale (`AbleToScale=False`) or its scaling is broken for another
  reason (an invalid selector); missing metrics, failed metric computation and disabled
  scaling are Healthy with the reason as message. Tested with luajit (Lua 5.1, as
  Argo CD's gopher-lua) against the conditions seen on the cluster.
- Not a Progressing state for missing metrics: a broken metrics server would then hold
  every sync, although the deploy is fine; the autoscaler's own trouble belongs to the
  metrics (kube-state-metrics), not to the deploy gate.

## D106 — The apex site is the ryft-web image, public

- **What changes:** `ryftapp.de` is served by `ghcr.io/timkrebs/ryft/web:<short-sha>`,
  built in `timkrebs/ryft-web`, instead of ConfigMaps in `deploy/k8s/base/web` (D86). The
  pages, the legal texts, nginx's config and the AASA live there; here only the
  Deployment, Service, NetworkPolicy, Ingress, the `www` redirect, the rate limit and the
  pin. A site change is a PR there plus a one-line pin PR here.
- **Public, by the user's choice (2026-10-21):** pulled anonymously like the service
  images, so no pull secret, nothing in `infra/vault`, Argo CD's project, the CI or
  `check_registry.sh`. ryft-web's D14 records the trade-off: the image holds the Satoshi
  font files, whose licence allows self-hosting but not offering them on public servers,
  and anyone can pull a public image; accepted for now. If it goes private, the variant
  with a Vault-synced `dockerconfigjson` Secret is
  `../web/docs/handoff/platform-web-image.patch`.
- *(Superseded by D107: the site lives here as `web/`, and this repository's promote pins it.)*
- **Who pins web:** ryft-web's own promote job (its PR #11) opens `deploy: web <sha>` on
  branches `deploy/web-<sha>` with the `ryft-deploy` App's token, after its image passed
  Trivy; this repository's `promote.sh` runs only for `backend/` changes and would wait
  for an unrelated one, and "the newest tag" in the registry would be a guess. So
  `promote.sh` closes older `deploy/*` PRs as superseded except `deploy/web-*` (they move
  a different line and never conflict); its test runs the real `--jq` filter.
- **Before the switch:** the anonymous manifest (linux/amd64 and arm64), and the image run
  locally as in the pod (uid 101, read-only root, `/tmp` and `/var/cache/nginx` only):
  every URL of the brief 200, the AASA `application/json` without a redirect, a CSP with
  the script hashes; its AASA is the ConfigMap site's plus `/invite/*`.

## D107 — The site lives here as `web/`; one CI, one promote per image group (supersedes D106's "Who pins web")

- **Why (the user's call, 2026-10-21):** the site's legal texts and AASA belong to the
  platform's releases: a privacy policy version is a ConfigMap here and a page there, the
  AASA follows the app's routes. Two repositories meant two CIs, a cross-repository promote
  and a version check that could only warn. Here a change to both sides is one PR, checked
  by one CI and deployed by one promote. ryft-web is archived.
- **History kept:** a fresh clone of ryft-web's `main` (`d0503ea`) rewritten with `git
  filter-repo --to-subdirectory-filter web --replace-message` (`(#n)` → `timkrebs/ryft-web#n`,
  so GitHub does not link old PR numbers to this repository's), merged with
  `--allow-unrelated-histories --no-ff` and landed as a merge commit (#7; a squash would have
  dropped it). `git log --follow -- web/<file>` reaches into ryft-web. The rewrite gives
  every imported commit a new SHA: ryft-web's SHAs and image tags (`abfbe26`, `f437747`)
  are not in this history.
- **One CI, path filters on pull requests only:** a `changes` job picks the checks a PR
  needs (Go, smoke, manifests, Terraform, web); the others are skipped. The required check
  of `ci.yml` is `ci-ok`, which needs every check, `changes` included, and fails on any
  failure or cancellation, so a skip never stands in for a failure and the Terraform matrix
  needs no name of its own. A push to main runs every check; only main gets pushes and
  images; only pull requests cancel an older run. The title rule (`area: what changed`, any
  lower-case area or path; the denylisted brand neither in the title nor in the PR's commit
  messages, `scripts/check_pr.sh`) is its own workflow, `pr-title`, because it must re-run
  when a title is edited. gitleaks checks the history of every PR and push, with a
  reviewed allowlist (`.gitleaks.toml`: one rule, one file, the exact value per entry); on a
  PR the base branch's config and ignore file count, not the PR's.
- **Two image groups, pinned against what runs:** services (ingest, scoring, api, notify,
  migrate, from `backend/`) and web (from `web/`). On a push to main a group is built when
  its directory differs from its pin in `overlays/eks`; a pin that is no commit of this
  history, or service pins that disagree, count as different (`scripts/deploy_targets.sh`,
  for the build jobs and for `promote.sh`). `promote` pins every built group to that commit
  in one "deploy: images <sha>" PR, closes every older `deploy/*` PR, and runs only when
  every group it needs was built. Comparing with the running pin, not the previous commit,
  keeps a change whose run was cancelled or whose deploy PR was superseded. A
  `workflow_dispatch` on main with `force` rebuilds and pins a group whose files did not
  change (a new `SUPPORT_EMAIL`). promote acts only for main's tip (an older run's re-run
  exits with a notice, so it cannot close a newer deploy PR or miss a newer hold), finds its
  PRs by author (the App), same repository and `deploy/<7 hex>`, reuses its own branch or
  open PR, and never force-pushes.
- **Rollback per group, with a hold:** revert the pin, or set one group's `newTag` back, and
  add `# hold: <services|web> <YYYY-MM-DD> <reason>` to `overlays/eks` in the same commit.
  Without it the next push builds and pins the rolled-back code again (true for the
  services since D96, unnoticed until this design). A line that looks like a hold but does
  not parse fails `manifests`, and so does a deploy PR (the App's `deploy/<sha>`) that moves a
  held group's `newTag`, so an older one cannot undo a rollback; a second rollback by hand
  still can. Every image entry in `overlays/eks` must be exactly the RYFT name and a quoted
  short SHA (no `newName`, no `digest`), so no line can change what runs unseen. A PR that removes the hold deploys main's head for
  that group; while a held group differs, every promote run says so.
- **The web image:** `ghcr.io/timkrebs/ryft/web:<the first seven characters of this
  repository's SHA>`, the same public package, linux/amd64 and arm64, provenance and an
  SBOM, never `latest`. One job builds amd64 into the runner, scans it with Trivy (critical,
  fixed) and only then pushes both architectures from the same builder; Trivy scans the
  pushed digest again before promote may pin it. The build stage runs on `$BUILDPLATFORM`
  (the export is architecture-independent). The build stops when `SUPPORT_EMAIL` is unset:
  it is baked into the HTML, and empty would publish a TODO marker on `/support`. The
  package grants this repository Write ("Manage Actions access"), ryft-web none.
- **The privacy policy check fails instead of warning:** `web/scripts/check-policy-version.mjs
  --strict` runs in `manifests` whenever a privacy page, the check or the api/ingest
  ConfigMaps change: the ConfigMaps' version must be the newest entry of
  `web/content/legal/privacy-versions.json`, and every listed version must exist in English
  and German, so a consented text never disappears. The version and its page go into the
  same PR. Known gaps (STATUS): F1, a ConfigMap-only change rolls no pod; and the page goes
  live with the web deploy about 15 minutes after the ConfigMap.
- **The brand rule's scope:** `web/**`, the built site, and every commit message and PR
  title in this repository. Root `.githooks/` (installed by `make hooks`, no npm `prepare`)
  check staged `web/` files and every message and fail closed without Node, because a
  message cannot be taken back without a force-push; CI checks the tree, the build, the
  PR's title and every commit message, with the base branch's denylist. The internal
  mentions outside `web/` were removed by the user's choice (#6).
- **ryft-web is archived,** frozen at the import (its workflow disabled, so its last README
  commit pushes no image). Its images stay: `f437747` is the rollback point for the first
  site built here; never delete package versions.
- **Superseded or amended:** D106's "Who pins web" (no `deploy/web-*` branches, no exception
  in `promote.sh`); D96's "No deploy without a backend change" holds per group; D95's
  rollback by revert needs the hold; D18's "Every push gets a short-SHA tag" now means every
  push to main.

## D108 — The site's toolchain: stable lines only, majors by hand, ignores in the Dependabot config

- **Why (2026-10-22):** Dependabot's first run on `web/` opened five majors at once (#12–#16); one was red, two were
  mainline or pre-LTS lines, and merging any of them alone would have split what moves together.
- **nginx follows its stable (even) branch.** Patches arrive by Dependabot; a new stable branch (1.32 …) or a new
  Alpine base is a hand PR. Docker tags compare only within the same suffix, so patches stop silently when the base
  image moves to a new `-alpine` suffix: watch for it.
- **Node moves as one unit and only onto an LTS line:** the Docker build stage (tag and digest), `web/.nvmrc`,
  `engines` and `@types/node`, in one hand PR. Node 26 is LTS from 2026-10-28.
- **A major goes in when it needs no code change and the rest of the toolchain declares or demonstrably supports it.**
  ESLint 10 went in (#19): its support for eslint-plugin-react and -import relies on eslint-config-next's
  `fixupPluginRules` shim; those plugins don't declare ESLint 10 yet. TypeScript 7 waits for typescript-eslint (peer
  `<6.1.0`).
- **The skipped updates are `ignore` rules in `.github/dependabot.yml`** (`update-types`, not `versions: N.x`, which
  the docker ecosystem does not understand), and every ecosystem has a 7-day `cooldown` (version updates only;
  security updates are not delayed).

## D109 — Synthetic Monitoring checks ryftapp.de from outside

- **Why (2026-10-22, the user's choice):** nothing outside the cluster checked the site: a page that is wrong but
  healthy, a broken AASA (universal links, password AutoFill) or the certificate Traefik serves raised no alert.
- **Grafana Cloud Synthetic Monitoring, free tier:** four HTTP checks (home; `/privacy` with a marker only the policy
  has; the AASA, `application/json`, no redirect, the app ID; `www` → apex), every 5 minutes from two EU probes,
  ≈69,000 of 100,000 executions a month. TLS required, no redirect followed; certificate expiry from the same runs.
- **As code, applied by hand:** `deploy/grafana/synthetics/ryftapp.json`, `scripts/synthetics.sh` and
  `make synthetics-check` / `synthetics-apply` (an upsert by job name; unchanged checks are skipped), like
  `alerts-apply`. Not Terraform: a provider and a state for four checks, and the token in tfvars. The SM token is
  tenant-wide: one per session, deleted afterwards.
- **Guards:** `synthetics.sh check` (CI job `monitoring`) validates the regexps with RE2 (Go), refuses anything but
  ryftapp.de targets and allow-listed keys, and asserts the body markers against the repository's AASA and newest
  privacy page, so a rewording fails its PR instead of paging after the deploy.
- **Alerts** (`deploy/grafana/alerts/ryft.yaml`): SiteDown (critical, every probe failing 10 minutes), a single probe
  failing now and then (warning, quiet after a full outage), the `www` redirect, the certificate (< 14 days), and a
  check that stopped reporting (per job). No new processor: the probes fetch public pages.

## D110 — The deploy key lives in a `deploy` environment; a daily watch; holds are written by a script

- **Why (2026-10-22):** the App key that opens auto-merging deploy PRs was a repository secret any workflow run could
  read; a promote that failed or never ran left no mark; nothing noticed a forgotten hold.
- **Environment `deploy`:** only `main`, no admin bypass, no reviewers (the promote stays unattended); only `promote`
  names it (`scripts/check_workflows.sh` in `lint`, which also refuses `pull_request_target` and `workflow_run`). Set
  up by rotating the key: a new key in the environment, the repository copy deleted right after the merge, the old key
  revoked in the App.
- **`deploy-watch.yml`, daily, read-only token:** fails (a GitHub mail to whoever last edited the cron line) on a hold
  older than 7 days or dated in the future, an App deploy PR open longer than 2 hours, or a group that differs from
  its pin on a main tip older than 2 hours with no deploy PR. It reuses `deploy_targets.sh`.
- **`scripts/hold.sh add|rm`** writes the hold line; reasons are public, one line of printable ASCII, no `@` and no
  closing keywords (they reach the deploy PR's body).
- **The EKS overlay is printable ASCII only** (`deploy_targets.sh` fails closed on any other byte): a review showed
  that U+2028, U+2029, NEL or CR inside a comment are line breaks to the YAML parser and could add an `images:`
  entry. `check_registry.sh` accepts only `ghcr.io/timkrebs/ryft/` and compares the rendered tags with the pins.
- **Runbook** (OPERATIONS → Deploy and roll back) rewritten from the drill preparation: services rollback with
  `pin_images.sh`, merge by hand (no `--auto`) when someone must approve, release as two merges, verification, rollback
  limits, the undo path; a selective `argocd app sync --resource` skips the hooks (Argo CD v3.5.6) and is user-only.

## D111 — A service's configuration rolls its pods; the privacy policy goes into force with its page (amends D78, D107)

- **Why (F1, 2026-10-22):** api, ingest, scoring and notify read the plain ConfigMap `ryft-config` through `envFrom`,
  so an edit rolled no pod; after a new `PRIVACY_POLICY_VERSION` api and ingest disagreed until restarted by hand.
  Consent is recorded against that version (D78). Rolling alone would have switched the version ~3 minutes after the
  merge and the page 25–40 minutes later.
- **Hashed ConfigMaps:** `ryft-config-<hash>` from `deploy/k8s/base/<service>/config.env` for the four; migrate's
  stays plain (its PreSync hook runs before a new name exists). Argo prunes the old one only after a healthy sync
  (`PruneLast`); roll back in git, never with `kubectl rollout undo`.
- **The version in force on EKS** is a pin next to web's image (two literals, api and ingest). Promote sets it to the
  base version of the commit it pins web to, so page and version go live in one sync (the user's choice, 2026-10-22);
  a hold or a failed web build keeps the old pair. A web rollback moves the version back with it (`pin_images.sh
  <old> web` reads the version at `<old>`, the newest page for commits before this change).
- **`scripts/check_policy_in_force.sh` (`manifests`, also on the App's deploy PRs):** api and ingest agree; a real
  date; listed; the in-force version is the page `/privacy` serves (the newest, en and de) in the pinned web commit;
  nothing else (a container `env`, another ConfigMap) can set it; a web pin is exactly one commit here or the known
  foreign `f437747`. **Drafts:** the version may change only to a page marked `published` in both languages (a policy
  PR with a draft is red), or back to one that was in force before. A withdrawal is durable only as a new version or
  with web held.
- **No default in Go:** api and ingest refuse to start without a valid date, in every mode, before they connect; the
  web check accepts only `PRIVACY_POLICY_VERSION=YYYY-MM-DD` with a real calendar date.
- **`/privacy` and `/de/privacy` are `no-cache`** (the version pages stay cacheable), so the text in force is the one
  shown.
- **web gets a PodDisruptionBudget** (`maxUnavailable: 1`, unhealthy pods always evictable).
- **Not chosen:** `replacements` into an annotation (dates became timestamps; overlay changes unseen), the version in
  each Deployment's env, kustomize's internal hash annotation, Reloader.
- **Known gap (the user's Terraform):** a `PRIVACY_POLICY_VERSION` key in Vault's `service_secrets` would override the
  ConfigMap (the secret comes later in `envFrom`); a validation in `infra/vault` should refuse it.

## D112 — A change in a day's history re-scores the days after it, in the handler

- **Why (2026-10-22, found while investigating the app's calibrating report):** baselines,
  sleep debt and need, consistency and yesterday's strain come from the 30 stored rows before
  a day (`ScoreHistory`). Nothing re-scored later days when an earlier one changed, so a day
  scored before its history (a backfill arriving newest first, a late HRV sample, a replayed
  range, two consumers in a rollout surge) kept `baseline_days = 0` or stale baselines for
  good. Production had no such row yet (checked 2026-10-22).
- **Detected on the write:** `UpsertDailyScore` reports whether one of `store.historyInputs`
  changed (the columns later days read, compared in SQL on the stored values; presence of
  steps and active energy after D113). Results such as `recovery` and `baseline_days` are left
  out, so a re-score settles. A guard test fails when `store.History` gains a field that is
  neither compared nor left out with a reason.
- **In the handler, not through the outbox:** `Engine.ComputeDays` scores the asked days, then
  the next 30 stored days after the last change, oldest first, never past the user's today,
  each day once, at most 400 extra. The outbox (D101) tells other services; a range sent back
  to scoring itself could interleave with the user's next events. A duplicate changes nothing
  and stops (D100); a retry or broker redelivery (`events.Redelivered`) re-scores the 30 days
  after its days, so a cascade interrupted half-way is finished. A DLQ replay arrives as a first
  delivery: follow it with `POST /v1/recompute` (until D150: `dlq.sh` marks a replay since then).
- **Safety net:** the 15-minute pass re-scores about 50 days a pass, round robin, whose stored
  counts are below the nights their window holds or whose baselines are missing. It compares
  counts, not values.
- **No push for a past day** (the morning push is for the user's today and the first recovery
  only, D70), and **no `ModelVersion` bump**: the rules did not change; past numbers can change
  after the fact with the same version (SCORING.md → Model version).

## D113 — `calibrating_reason` says why a day is calibrating

- **Why (the app's request, 2026-10-21):** the reporter's account has no HRV source at all;
  `baseline_days = min(HRV nights, RHR nights)` stays 0 and "0 of 7 nights" never ends by
  waiting. The app could not tell that from a short history.
- **Contract (additive, API.md → `#### calibrating_reason`):** `baseline_hrv_days`,
  `baseline_rhr_days` and `calibrating_reason` (`short_history | no_hrv | no_rhr`, a closed
  enum). The rule, in order: null when both counts are ≥ 7; `short_history` when the day brings
  a signal its window never had (a new watch writes HRV at night and resting HR later in the
  morning); `no_hrv` when the window has ≥ 7 days with data, no HRV night, and the day itself
  has none (it wins when both are missing); `no_rhr` likewise; else `short_history`.
- **W counts days with data,** not stored rows: a rescore stores empty rows before an
  account's first data, and counting them would say `no_hrv` on a watch user's first night.
  The data columns are one list (`store.dataColumns`), each compared as a history input.
- **Storage:** migration 0027, three nullable columns without a default, a CHECK on the reason,
  no cross-column constraint (an older image writing after a rollback must not fail),
  `lock_timeout` 5 s. Rows the server can't vouch for (before 0027, or rewritten by an older
  image so that the fields disagree with `baseline_days`/`calibrating`) are served as null
  ("unknown"). No stored number moved; `ModelVersion` stays.

## D114 — The logs hold no health values (amends D51's "Logs")

- **Why (2026-10-23):** the `scored day` line logged every day's recovery, readiness, strain,
  sleep, HRV and resting HR since the scaffold, against OPERATIONS → Data protection (logs);
  D112's cascades and every rescore multiplied it.
- **Decision:** `scored day` keeps only `day`, `model_version`, `inputs_changed` (plus
  `request_id`, `user_id`; the line `trace.sh` follows, so Info). No learned sleep minutes; no
  APNs device token in transport errors (it reached the retry/dead-letter lines and the DLQ
  header); decode errors name field and type, not the value; GoTrue admin errors keep status
  and `error_code`; no email in the smoke's error. Tests allowlist the keys of each log line
  and seed distinctive values. The full `user_id` stays (pseudonymous, as the privacy text
  says).
- **Known exceptions, documented:** a panic's stack trace can show arguments as hex; a pgx
  encode error can quote an argument. A hand-run rescore prints to the operator's terminal.
- **Old lines** leave CloudWatch 14 days after the deploy (2026-11-06). The privacy text still
  says scoring logs the daily scores (now more than happens); its correction goes into the next
  policy version after that date — a new version asks every user to consent again.

## D115 — Checks read what the parsers read

- **Why (2026-10-22/23):** kustomize, Argo CD and GitHub treat U+0085, U+2028, U+2029 and a lone
  CR as line breaks while grep and awk do not, and kustomize reads field names case-insensitively,
  decodes `!!binary`, follows symlinks and reads files without a `.yaml` extension. A comment line
  or a renamed key could carry content no reviewer sees (a sidecar with `envFrom: ryft-secrets`, a
  remote base, an allow-all NetworkPolicy) — each reproduced by the security review.
- **Decision: allowlists instead of denylists**, at three levels.
  - **Bytes:** `scripts/check_bytes.sh` (one character policy, `scripts/lib/charpolicy.py`) reads
    every tracked file by content — invalid UTF-8, controls, format and default-ignorable
    characters (reserved ranges included), separators, BOM; a variation selector only after a
    symbol; no symlinks or submodules; binary extensions skipped except under `deploy/` and
    `.github/`; no path exemptions (escapes in source instead). It runs in a `bytes` job with no
    path filter that `ci-ok`, `images` and `web-image` need, and in pre-commit.
  - **Structure:** kustomizations and Argo CD applications are parsed by yq against an exact,
    case-sensitive key schema whose path-typed fields drive the reference walk, and a YAML tag
    allowlist; every decoded key and string under `deploy/` passes the character policy; inline
    patches are literal blocks; references are plain paths below `deploy/` with no dotted
    segment (no remote base, even behind a decoy directory); env lines need `=`; only `root.yaml`
    uses the `ryft-root` project; the EKS overlay has its own allowlist. `check_workflows.sh`
    parses workflows (no external reusable workflows, `secrets: inherit`, dynamic secrets, merge
    or binary tags, or the scripts' test hooks).
  - **Render:** one Argo CD application per path, each project bound to its paths (`ryft-root` to
    `root.yaml`, `ryft-operators` to the operators); every app is rendered with
    `GIT_ALLOW_PROTOCOL=file` (no remote fetch; job-wide in `manifests` and `smoke`) and checked
    for a small set of production invariants (`scripts/lib/renderpolicy.py`): only the pinned ryft
    images (and the broker's digest-pinned RabbitMQ image); `AUTH_MODE=supabase` in the effective
    container environment (no `envFrom` prefix, no `valueFrom` for the auth variables, no API key,
    insecure flag or `USER_ID`, Secrets included); default-deny intact in every `ryft-*` namespace
    and no empty allow-all rule; no `argoproj.io` object outside `apps/`. `check_registry.sh`
    checks the pins exist; empty renders fail.
  - **Not a policy engine:** pod security (privileged, host*, hostPath, PSA labels), images per
    namespace and command overrides, NetworkPolicy rule shapes, the VSO objects and the operators
    project's cluster power are not covered by these CI checks — they belong in an admission
    policy in the cluster (open, STATUS).
- **Tools:** yq is a sha256-checked release binary from one install script for every job.
- **Script tests build their fixtures** (holds stripped, a fixed policy version, no live
  `deploy/` file), so a hold or a manifest cleanup on main can't turn `smoke` red (the drill).
- **Consequence:** a new kustomization key, tag, reference shape, label or invariant exception is a
  schema change in the same PR (OPERATIONS → "What a kustomization or an Argo CD app may contain").
  `.gitattributes` and `.gitmodules` may not exist. Argo CD's own repo-server isn't constrained by
  these checks; they gate what reaches `main`.

## D116 — The migrator bounds every lock wait (5 s `lock_timeout`)

- **Why (2026-10-23):** a migration that needs a strong lock on a busy table waited without
  limit, and every later query on that table queued behind its lock request (the shape of the
  2026-10-20 outage). Only 0027 set its own `lock_timeout` (D113).
- **Decision:** `locked()` sets a transaction-local `lock_timeout` of 5 s (`set_config(…,
  true)`) right **after** `pg_advisory_xact_lock`, so a second migrator still waits for the first
  (bounded by `cmd/migrate`'s context and the Job's deadline). Every migration and Seed run
  under it; a migration's own `SET LOCAL` runs later and wins. A constant with a test hook, no
  env setting. No `statement_timeout`: backfills legitimately run long.
- **Failure:** the timed-out migration rolls back (earlier ones of the run stay applied); the
  error names the file, the lock timeout and where to look; the PreSync hook fails, the sync stops
  before anything rolls, Argo CD retries, the running pods keep serving. Each attempt can stall
  queries on that table for up to 5 s (up to about 15 times over 10 minutes; no alert): find and
  end the blocker first (OPERATIONS → Deploy and roll back), then sync.
- **Consequence:** an `ALTER` on a table with long writers (`samples` under sync load) fails fast
  instead of waiting: run it off-peak, or give that migration its own `SET LOCAL lock_timeout`
  with the reason in its header. The repo targets Postgres 17; the hosted version is the user's
  to confirm (SUPABASE.md).

## D117 — Log tests pin each value's kind, not only its key (amends D114)

- **Why (2026-10-23):** D114's tests allowlisted the keys of each log line, so a health value
  under an allowed key (a recovery score as `applied`, `days` or a duplicate `user_id`) passed.
- **Decision:** `internal/logx/logcheck` (test-only; a depguard rule keeps it out of the
  services) gives every allowed key a kind: UUID, day, RFC 3339 time, bool, enum, a count with a
  bound where one is known, a pattern, or text with a length limit. A line with a duplicate key
  fails (slog writes both, `encoding/json` keeps the last); the known-values scans read the raw
  line. Each test pins the exact counts it can know (rescored days, the sync's days, the
  feedback id, the statuses in order) and each package has mutation tests that must fail on the
  key they break. notify's lines are covered the same way (scores, readiness, device tokens).
- **Third-party errors:** GoTrue, Sign in with Apple and APNs refusals are logged as the
  response's status code, the standard phrase (`logx.StatusLine`) and a bare code that matches
  a fixed pattern — never the status line's text or the body; a GoTrue body that doesn't decode
  is a fixed error. The AMQP URL is logged without its password.
- **Accepted gaps (OPERATIONS → Data protection (logs)):** an integer health value under a count
  key without a known bound passes by type (the known-values scans still look); `err` is free
  text up to 1 KiB (pgx's encode error can quote an argument, D114); `client_request_id` accepts
  what httpx accepts (the contract); the broker and panic lines, auth, httpx and several handler
  lines have no allowlist test yet (STATUS).

## D118 — Text the database can't store is refused at the edge; the sync strips NUL instead

- **Why (2026-10-23):** Postgres can't store U+0000 in text (22021) or jsonb (22P05), nor bytes that
  aren't UTF-8; such a request was a 500 (feedback, Train sessions and plans, exercises, behaviors,
  consent, the exercise search, the sync).
- **Decision:** `httpx` checks every request: `ReadJSON` scans the body lexically, so `\u0000` in any
  string, value or key, is `422` with the error shape and no `code` (`"\\u0000"` is six ordinary
  characters); `WithRoutes` refuses `%00` or non-UTF-8 in the path or query with `400`, after auth;
  raw JSON fields (`json.RawMessage`, feedback `diagnostics`) are decoded and encoded again, so bad
  UTF-8 and lone surrogates become U+FFFD and an out-of-range number is a `422`; `Fail` answers
  SQLSTATE 22021/22P05 with `422` as a backstop, counted in `ryft_http_text_refused_total{at="database"}`
  and alerted (`TextRefusedByDatabase`), never logged (D114, D117).
- **The sync strips instead (`POST /v1/sync`):** its strings come from HealthKit and the device (source
  and device names, bundle ids), which the user can't change, and the app advances a stream's anchor
  only after an accepted page and doesn't retry a `422`; one NUL would stall that stream for good. So
  the sync removes U+0000 from every string and accepts the page (`ryft_http_nul_stripped_total`).
  User-authored text elsewhere keeps the `422`: a silent edit of what someone wrote is worse.
- **Not chosen:** mapping the errors in the store (about 100 query functions, and its errors also feed
  scoring and notify); adding 22P02/22003 to the backstop (they also mean server bugs).
- **Consequences:** the app should strip U+0000 from text the user enters (low priority; the sync needs
  no change). A `database` count above 0 means a path the checks missed or a server-side bug producing
  bad text. Numbers in `diagnostics` are stored as double precision.

## D120 — Admission: Pod Security labels plus ValidatingAdmissionPolicy, owned by Terraform (amends D95)

- **Amended by D180:** Argo CD's ClusterRoles are an allowlist and the values that decide what Argo CD may do are guarded
  (merged in #47, not yet applied). **Amended by D182:** how the operators are namespace-scoped (T12-g, #51).
  **Amended by D195:** the layer covers namespace `vault` and the chain of the `ryft-vault` issuer (P13); 12 policies, 17 bindings
  (#147, #153, not enabled).
- **Why (2026-10-23):** D115's CI checks hold a small set of render invariants. Pod security, images
  per namespace, commands, NetworkPolicy and Traefik shapes, the VSO objects, the operators' cluster
  power and the Argo CD objects are not checked, and whatever is written in the cluster (Argo CD's
  public UI, the operators, VSO, a break-glass apply, the hand-applied projects) never passes CI.
- **Decision:** Pod Security Admission stays the pod-security mechanism (`restricted` on every `ryft-*`
  namespace, `rabbitmq-system` and `argocd`; `monitoring` privileged, D51). Eleven
  ValidatingAdmissionPolicies (GA since 1.30; EKS 1.36) cover the rest. No Kyverno or Gatekeeper: pods
  on CPU-bound nodes and a webhook that blocks or fails open, for features not needed yet.
  Design and rule set: `docs/work/2026-10-23-admission/design.md`.
- **Owner:** the chart `infra/aws/kubernetes/charts/admission-policy` with a committed values file, in a
  `helm_release` the user applies. **The AppProjects and `root.yaml` are rendered by Terraform** from
  `deploy/argocd/project.yaml` and `root.yaml` (the user's choice), the same decoded value P7 compares
  against: one file, one owner, no hand apply (amends D95's "applied by hand"; an apply during a pause
  resumes the root). Bindings are exempt from admission, so RBAC guards them: Argo CD's ClusterRoles
  are narrowed first, in an apply of their own.
- **Rollout:** Warn+Audit until an exercise checklist and 7 days pass, then Deny+Audit in four steps by
  blast radius (small config apps; what Argo applies; runtime Pods and the operators; Argo CD's own
  objects last). P7 skips the controller's status writes; P10 judges only requests that set or change
  an operation, so terminate-op, pause and refresh always work. Signals: the audit log in CloudWatch,
  two API server metric families, and KSM series of each binding's actions.
- **Testing:** CI job `admission`, **required in `ci-ok`**: kind at EKS's minor with Argo CD's CRDs, the
  chart, every render as a server-side dry run under Argo CD's identity, one refused fixture per rule;
  a kind e2e with Argo CD, the operators and the broker before production and on every upgrade.
- **Break-glass:** the user only: patch or delete a binding (always possible), restored by Terraform;
  drift alerts and a `check` block catch one left behind. No separate IAM role.
- **Operators (D97, the user's consent):** both RabbitMQ operators are namespace-scoped (T12-g), so
  neither reads Secrets outside its namespaces.
- **Accepted:** operator images by tag; no image signatures; cross-object invariants in CI only; the
  operators' cluster-wide Secret reads until T12-g lands; and, until it is verified what argocd-server
  writes as a sync's revision, `argocd app sync --revision <sha>` after a pause (rollback and `--local`
  are refused).
- **Consequences:** a change to `project.yaml` reaches the cluster with the user's apply. A new
  Argo-made namespace, a new mutating webhook on RYFT pods (the SDK injector excludes `ryft-*`), a
  Vault address change, or an operator upgrade that renames an object or adds an image needs the chart
  first. A new service doesn't (images and app paths are patterns).
- **Signals (amended 2026-10-26, T12-e):** two design claims were false — kube-state-metrics'
  custom-resource-state reads CRD kinds only (claim m), and clean passes aren't counted (claim e).
  So: KSM's native `validatingadmissionpolicybindings` collector (existence and live actions, no
  labels); the committed action per binding is a recording rule generated from
  `admission-values.yaml` (`scripts/admission_intended.sh`, an equality check in CI, keys equal to
  the chart's); break-glass is read from the live actions; liveness is the scrape's `up`. Pod
  Security enforce refusals alert today (ungated, critical); the Pod-capable bindings (workloads
  pods, operator reach, auth env) page critical, violations are info (Argo's refreshes repeat
  them); an evaluation error is critical only where it refuses. `GC_TOKEN` (the Fleet token) also
  writes the admission series; the check-duration family keeps `_sum`/`_count` only. A step change
  applies Terraform first, then the rules.

> **Note (D151, 2026-10-26):** the kind run (T12-d) answered claim k, so the "Accepted" risk above that
> `argocd app sync --revision <sha>` passes until k is verified is closed: argocd-server writes the full
> SHA of the synced revision, and P10 refuses any other revision from it. The design's claims table and
> §2 P10 say so; the rest of D120 stands.

## D121 — Argo CD's custom health checks run without the string library; a harness proves it

- **Why (2026-10-26):** the kind e2e showed that the RabbitMQ health checks (D97) fail in a real Argo
  CD as soon as a wanted condition is False with a reason (AllReplicasReady=False during a node
  roll): `attempt to index a non-table object(nil) with key 'find'`, and the application goes to
  ComparisonError. The script called `string.find(c.reason, "Fail")`. Argo CD v3.5.6 builds its Lua
  state with `SkipOpenLibs` and opens only `package`, `base` and `table` (`util/lua/lua.go`,
  `runLua`); its safe `os` registers into `table`, so `os` is reachable only through
  `require("os")`. `string` (and with it the string metatable, so `s:find` fails too, and `math`)
  exists only with `resource.customizations.useOpenLibs.<group_kind>: true`. Neither
  `terraform validate`, `terraform test` nor reading the script could see it.
- **Decision:** no pattern matching and no `useOpenLibs` (it would open the whole standard library for
  each kind to save an equality test). Healthy: every wanted condition True (RabbitmqCluster:
  AllReplicasReady and ReconcileSuccess; topology kinds: Ready). **Degraded: every
  `ReconcileSuccess=False` of a RabbitmqCluster and every `Ready=False` of a topology object, whatever
  the reason**, as Argo CD's own built-in RabbitmqCluster check does. `AllReplicasReady=False` never
  degrades (NotAllPodsReady during a node roll, ScaledToZero): Progressing, with the condition's
  message. While `status.observedGeneration` is behind `metadata.generation` the object is
  Progressing too: after a spec change the old conditions stay True until the operator has
  reconciled it, and both operators set observedGeneration on success only.
- **Why not a list of failure reasons:** the Cluster Operator v2.23.1 writes `ReconcileSuccess=False`
  only for a failed reconcile, and what reaches the generic `Error` (a `Build()` failure, or an
  update that survived `RetryOnConflict`: additionalConfig parse errors, API validation and admission
  rejections, immutable StatefulSet fields) is mostly permanent. Progressing would hold wave 1
  forever with no alert (`controller.sync.timeout.seconds` is 0 and no alert fires on
  Synced+Progressing). The Messaging Topology Operator v1.20.4 writes `FailedCreateOrUpdate` for
  every `Ready=False`, **transient ones included** (a DeclareFunc error, a cluster that is scaled to
  zero); those degrade the object too. The sync's `retry: {limit: 2}` and the `ArgoAppDegraded`
  alert's 10-minute `for` absorb them. Source locations are in the comment in `argocd.tf`.
- **Test:** `infra/aws/kubernetes/argocd-luatest` (`make argocd-luatest`, CI job `argocd-luatest`)
  parses `argocd.tf`, evaluates every `resource.customizations.health.*` script in gopher-lua v1.1.2
  (Argo CD v3.5.6's version) with exactly Argo CD's libraries (`oslib_safe.go` copied unchanged, and
  probes that `string`, `s:find`, `math` and a global `os` fail while `require("os")` works) and checks
  table-driven objects. It fails on the old code with the production error, and on a health key it
  cannot evaluate statically. A `terraform test` assertion rejects `string.` and `useOpenLibs` in the
  health keys and in `resource.customizations`.
- **Consequences:** an Argo CD upgrade that changes the library set or gopher-lua is a harness change
  (bump the module and `oslib_safe.go`). A new health check is covered only once it has cases: the
  harness fails on a script without them.

## D124 — The training import is one batch endpoint with per-item results (TI-1)

- **Why (2026-10-26):** the app imports another app's CSV export: up to years of history, 5 plans,
  sessions in batches. One call per session (`PUT` + `…/complete`) would complete each session with its
  own event, push and rescore, and a lost answer would have no safe retry.
- **Decision:** `POST /v1/train/import` takes `{source, plans ≤ 5, sessions ≤ 50}` (1 MiB) and answers
  `200` with one result per item (`created` / `unchanged` / `updated` / `rejected` + `code`), so a bad
  item never fails the batch. Whole-request failures are only structural (`400` unknown field, `413`,
  `422` unknown `source` / over the counts / duplicate ids, the gates, `429`, `503`). Every item is its
  own unit of work (a transaction per item; savepoints inside one outer transaction are the same
  semantics, the database engineer's call), plans first, sessions oldest first. A database failure stops
  the request with `503`: the app resends everything, and stored items come back `unchanged`.
  `ImportResult.kind` also lists `exercise`, reserved for a later `exercises` array; the app creates
  custom exercises with `POST /v1/exercises` first.
- **Not chosen:** all-or-nothing (one odd session in a 50-session file would block the user's whole
  history); a result per set (the unit the app can act on is a session); `207` (clients treat any 2xx as
  "read the body").
- **Consequences:** `results` is the app's source of truth, always one entry per item in request order.
  The server never decides to drop a reference: an unknown `plan_day_id` rejects the session (as the
  `422` of `PUT` does) and the app resends it without.

## D125 — Origin belongs to the endpoint; imports are silent (T1, T6)

- **Decision:** `sessions` gain `origin` (`app` | `import`, default `app`) and `import_source`
  (nullable, set exactly when `origin` is `import`). The import endpoint writes both from `source`;
  its request type has neither field, so a request that carries one is `400` (unknown field);
  `PUT /v1/sessions/{id}` accepts and ignores them. An import never publishes `session.completed`:
  it writes one `ingested` event with `reason: "import"` and no `session_id`. Scoring publishes
  `session.scored` only for an event with a session id, so no `SESSION_SCORED` and no `PR` push can
  follow; notify additionally refuses a session whose origin is `import` (a `POST …/complete` on an
  imported session would carry an id), counted as result `import`.
  The silent refresh push, sent after a `sync` event's days, is also sent after `import`.
- **Records** of imported history are recomputed with the existing `ReplacePRs` and stay silent:
  `PRsSince` (the insight cards) skips records of imported sessions. Whether the newest may surface once
  as an info card is open for the scoring-scientist.
- **Enrolment:** `AdvanceEnrollment` runs only in `POST …/complete`; the import does not call it.
- **Not chosen:** a per-session `origin` in the request (a client could label an import as live);
  `session.completed` with a flag (one more field every consumer must honour, and scoring would rescore
  per session).

## D126 — Idempotency: compare what the request determines; the app's later edits win (T3, T10)

- **Decision:** a stored id is compared with the request after the same clamping and defaults, over every
  field the request sets and nothing the server computes. Set `updated_at` and `completed_at` are content
  (the app derives them from the file, so a resend is identical); a set without `updated_at` takes its
  `completed_at`, not `now()`. `weight_kg` and `assist_kg` `null` equal `0`. `import_source` is content.
  Equal → `unchanged`, nothing written, nothing rescored. Different → `updated`, the item replaced whole
  (as a completed session or a plan is by `PUT`). Only a session with `origin: import` can be replaced; a
  session logged in the app is `id_taken`. A stored imported session whose set has a later `updated_at`
  than the request's, or that was discarded, is `rejected` / `newer_on_server` (the rule of `train.Merge`),
  so a resend after a lost answer never undoes the user's edit.
- **T3, differing from the user's proposal:** the user proposed normalising `null` → `0` on write. The
  code already treats them alike (`EffectiveWeight`, `PersonalRecords`, the e1RM chart skips both), and a
  rewrite would make a read differ from the live path (live sets send `null`) for nothing the scoring or
  the comparison needs. So the equivalence lives in the comparison and in a test.
- **Known limit, closed in the review of PR #59 (D141):** an edit to a title or a note (no set changed)
  used to go unseen, so a resend reverted it. Now every imported session and plan carries `imported_at`,
  and any change in the app after it (`updated_at` later) is `newer_on_server`, for plans too.

## D127 — `assist_kg` instead of a negative load; scoring decides later (T4)

- **Decision:** `Set.assist_kg` (number?, clamped 0–200, bodyweight exercises only, not together with a
  `weight_kg` above 0; `422` / `invalid` otherwise) is stored in `session_sets.assist_kg` and returned.
  `weight_kg` keeps its 0–500 meaning. **It changes no number yet:** scoring, volume, records and the e1RM
  chart ignore it until `SCORING.md` defines the effective load (proposal: `max(0, bodyweight_kg ×
  bodyweight_factor − assist_kg)`). That is a scoring change (model version, rescore), the scoring-scientist's.
- **Why not a negative `weight_kg`:** it would widen a range every consumer clamps and compares at 0, and
  "added load" would mean two opposite things.
- **Consequences:** until then an assisted set counts like an unassisted bodyweight set (overstated), as
  a reps-only import does today.

## D128 — One rescore per request, over explicit days; imports older than 400 days are stored, not scored (T7)

- **Decision:** after the last item the request (a) recomputes records once per affected exercise,
  (b) queues the days in `score_queue` (so `GET /v1/me/sync` → `pending_days` shows them) and (c) writes
  **one** outbox `ingested` event, `reason: "import"`, `days` = the explicit sorted list (not the
  recompute range form: a file spanning two years has 50 sessions, not 400 days to score). Each item's
  own transaction already queues its day, so a request that dies after some items leaves those days
  pending, and the retry (its items `unchanged`) puts the pending ones into the answer and the event.
  `days_touched` also holds the days of the user's other completed sessions after the oldest imported one
  that share an exercise (their load is measured against the e1RM history as of their end, which the
  import changed). Only days in the 400 local days ending today are queued, as a sync's window;
  older sessions keep `muscular_trimp` / `strain` / `volume_kg` / `workout_id` `null`.
  The api computes no load for an imported session; scoring fills it when it scores the day.
  `expected_within_sec` = the sync estimate + 2 s per queued day, at most 300.
- **Consequences:** `ryft_api` needs INSERT and UPDATE on `score_queue`. Imported days show up on the
  Strain screen like any logged session (a `ryft-session` workout). A day with a session and no sleep or
  HRV is what a day with a logged session already is; what such a history does to baselines and the
  strain average is for the scoring-scientist to confirm.

## D129 — The import's gates, limits and concurrency (T9)

- **Decision:** the invite gate is the api's (every route). **Consent is new on this route:** today only
  `POST /v1/sync` checks it and the Train writes do not (scoring simply refuses an unconsented user), but
  an import is a bulk upload of health data, so it answers `403 consent_required` under the same rule as
  the sync; **amended (D141):** on this route `warn` acts as `on`, and the flag `train.import` is checked
  after the gates (`403 import_not_enabled`). Body 1 MiB, 5 plans, 50 sessions, 50 s. One token however
  many items, **amended (D141):** from the import's own rate class (bursts of 5, then one every 5 s). At most **one import per user at a time across all pods**: a
  second one is `429`, `code: import_in_progress`, `Retry-After: 10`, nothing processed. `429` (not
  `409`): the app already treats `429` as "wait and send the same thing", and the request is not in
  conflict with a state, it is early. The per-pod token bucket cannot do this; a lease row in the
  database does, and works through a transaction pooler (a session advisory lock would not). New closed
  `code` `import_in_progress`.
- **Privacy:** no bodies, titles, notes or values in logs, metrics or `message`s (D114). The import is
  health data the user already holds, stored under the existing consent and processors; no new
  processor. A line in the privacy text that training data from other apps can be imported is the user's
  call (a text change moves `PRIVACY_POLICY_VERSION` in the same PR).

## D130 — Small answers that change behaviour (T8, T11, T12)

- **T8:** `POST /v1/plans/{id}/enroll` takes an optional `{started_at?, current_week?, current_day_index?}`.
  With a field set, an active or paused enrolment of that plan is moved in place (`200`); a new one starts
  at the position (`201`). The target day must exist (`422` otherwise); no body is today's behaviour.
- **T11:** a `bodyweight` plan target's `target_value` is `null` or an added load of 0–500 kg. The server
  stores it as sent and never uses it (`target_kg` stays `null`); the doc said `null` only. The 0–500
  range is checked now (it was not), as for `kg`.
- **T12:** `POST /v1/exercises` with an archived custom exercise's id un-archives it. That is what the
  code does (the update writes `archived = false`); documented and pinned with a test.

## D131 — One transaction per import item, not savepoints in one (TI-2)

- **Decision:** `ImportPlan` and `ImportSession` each run in their own transaction (`pgx.BeginFunc`), and
  a deadlock or serialization failure (`40001`, `40P01`) is retried once in a fresh one. A rejection is
  an error that rolls the item back (`importReject`), so a rejected item leaves no row, and the next item
  is unaffected. The queue rows of an item's day are written in the item's transaction (D128).
- **Why not savepoints in one request transaction** (same semantics, as D124 allowed): (1) the item's
  locks (`pg_advisory_xact_lock` on the session, `FOR UPDATE` on its row and its plan) would be held until
  the end of a request that may run 50 s, so a live save of any session in the file would wait that long;
  (2) one open transaction pins the connection and the snapshot (vacuum cannot clean up behind it) for
  the whole request, on a pooler that gives a small number of server connections to all pods; (3) a
  connection that dies at item 40 would lose items 1-39, which D124 says stay stored and the retry
  would have to redo; (4) a failed statement inside a savepoint leaves its dead tuples either way, so
  there is no storage argument for one transaction. The cost is one commit per item (at most 50 plus
  5 plans); measured locally a 50-session, 2 500-set request takes 1.9 s (create), 1.3 s (resend).
  On Supabase expect about a dozen round trips per item, so a few seconds, far inside the 50 s.
- **Idempotent by id:** `(user_id, id)` is the key, `lockSession` serialises with a live `PUT` of the same
  id, `unchanged` writes nothing (checked with `xmin` in the tests).
- **Rejections the store decides:** `id_taken` (another origin, or an exercise or set id of another
  session), `unknown_exercise`, `unknown_plan_reference`, `newer_on_server`. A value Postgres refuses
  (class 22, not-null, check) is `invalid` for that item (it fails the same way next time); anything else
  (connection, timeout, a foreign-key violation from a concurrent delete, a deadlock after the retry) is
  returned as an error, which the handler turns into the `503` that stops the request.
- **The comparisons are not in the store:** `SameSession`, `SamePlan` and `ImportConflict` are
  `internal/train`'s pure rules (design §3); the store takes them as `ImportRules`
  (`Store.WithImportRules`) and refuses to run without them (`ErrImportRules`). Its tests use
  stand-ins.

## D132 — The import lease is a column on `users`, fenced by its own value (TI-2)

- **Decision:** `AcquireImportLease` is one statement: `UPDATE users SET import_lease_until = now() +
  ttl WHERE id = $1 AND (import_lease_until IS NULL OR import_lease_until < now()) RETURNING
  import_lease_until`. No row: busy (or `ErrNotFound` when the account does not exist). TTL 70 s
  (`ImportLeaseTTL`; 120 s before the review of PR #59, D141). The database's clock decides, so the pods need no clock agreement; no session state
  is involved, so it works through Supavisor's transaction pooler where a session advisory lock does not.
- **Release is fenced:** `UPDATE users SET import_lease_until = NULL WHERE id = $1 AND import_lease_until =
  <the value we set>`. An import that overran its lease and finishes after another took it cannot free the
  other's lease. The returned `release` runs on `context.WithoutCancel` with its own 5 s, because the
  request's context is usually the thing that ended. A failed release is ignored: the lease expires.
- **Why the column and not a `train_import_leases` table:** no new table means no new deletion, export,
  retention and RLS surface (the column dies with the `users` row); the update is HOT (the column is
  not indexed) so it creates no index churn; the two writes per import are negligible next to the import.
  The lease row lock contends with other updates of the same `users` row only for the statement's
  duration.
- **Limit:** a pod that dies mid-import (a rollout) blocks that user's next import for up to 70 s (the
  client gets `429 import_in_progress`, `Retry-After: 10`, and retries). The lease is not renewed: the
  request is bounded by 50 s, the finish included, and its release has 5 s more, so 70 s cannot run out
  while it works.

## D133 — Migration 0028 is additive, NOT VALID, and leaves one constraint unvalidated (TI-2)

- **Decision:** `0028_train_import.sql` adds `sessions.origin` (`NOT NULL DEFAULT 'app'`, constant default:
  no rewrite on Postgres 11+) and `import_source`, `session_sets.assist_kg`, `users.import_lease_until`,
  the `train.import` flag row (enabled, 0 %), and two grants: `INSERT, UPDATE` on `score_queue` to
  `ryft_api` and `SELECT (origin)` on `sessions` to `ryft_notify`. Nothing is dropped or renamed, so the
  images before the migration run against the new schema; rollback is re-pinning them.
- **Locks:** the migrator runs a file in one transaction under the 5 s `lock_timeout` (D116, repeated
  in the file as 0027 does), so the `ACCESS EXCLUSIVE` of `ADD COLUMN` is held to the commit and a
  `VALIDATE` in the same file scans under it. Therefore every `CHECK` is added `NOT VALID` (catalog only;
  still enforced for new and updated rows), the small `sessions` table is validated in the file (all rows
  are `'app'`), and `session_sets_assist_kg_check` is **left `NOT VALID`**: `session_sets` is the largest
  train table and every existing row is `NULL`, which passes. `CHECK` inline on `ADD COLUMN` would have
  scanned it. A test proves 0028 gives up on a held `sessions` table after 5 s and leaves nothing behind.
- **Optional, not required:** `ALTER TABLE session_sets VALIDATE CONSTRAINT session_sets_assist_kg_check;`
  takes only `SHARE UPDATE EXCLUSIVE` (writes continue) and may be run from the SQL editor in a quiet
  minute; it only flips `convalidated`. The planner does not use the constraint.
- **No index:** the lookups are by `(user_id, id)`, `sessions_user_started` and
  `session_exercises_exercise`. `EXPLAIN (ANALYZE, BUFFERS)` of the dependent-days query on a user with
  1 500 sessions (6 years) among 37 500 sessions and 750 000 sets: 6 ms for the 400-day window (273
  sessions, bitmap scans on `sessions_user_started` and `session_exercises_exercise`, hash semi join),
  1.4 ms with no window cut. Nothing lists by `origin`; no index on it.
- **Deletion, export, retention:** no new table. `DELETE /v1/me` and the merge cover the columns because
  they live on rows both already handle (the merge copies columns generically); `delete_test` and
  `merge_test` now hold an imported session, an assisted set, a record, an imported plan and a held lease.
  Export is built from `GET /v1/sessions*`, which carry the new fields. No retention changes.

## D134 — Assisted sets: the support comes off the moved weight; they hold no record (TI-4, open point 1)

- **Why (2026-10-26):** D127 stored `assist_kg` and left its meaning to scoring; until now an
  assisted pull-up counted as an unassisted one, overstating its volume and, worse, giving its
  reps the `max_reps` record. The import makes these sets common (assistance machines and bands
  are a standard entry in training apps).
- **Decision:** a `bodyweight` set's weight is `max(0, body weight × bodyweight_factor + added −
  assist_kg)`: the support acts on the moving body, so it comes off kilogram for kilogram. Volume
  and muscular load follow with no special case (`reps × (weight / e1RM)²` is relative to the
  athlete). An assisted set counts towards the e1RM reference with that weight (a user who only
  does assisted pull-ups needs one; an unassisted set outweighs it), gives no e1RM chart point
  (as every unloaded bodyweight set) and **holds no record**. `assist_kg` null or 0 is exactly
  the old model; a weight exercise's `assist_kg` (refused by validation) is ignored. Calibration
  in `SCORING.md` → Assisted sets (3 × 8 with 30 kg: TRIMP 3.9 alone, 1.8 a week after 5
  unassisted reps; volume 1200 instead of 1920 kg).
- **Not chosen:** `(body weight − assist) × factor` (the support does not act on the part of the
  body that stays put); a `min_assist` record (a new kind in a closed enum, for one exercise
  family); leaving assisted sets out of the e1RM reference (an assisted-only exercise would have
  no load at all).
- **Consequences:** model **2026.10.2** with D135 and D138. Nothing stored before migration 0028
  has an `assist_kg`, so D134 alone moves no stored number; the bump is D135's. The built-in
  `assisted-dip` is a `weight` machine exercise whose number the machine shows as support, so a
  higher number there is *less* work: the app maps an imported assisted dip or pull-up to `dip` /
  `pull-up` with `assist_kg`. Re-modelling `assisted-dip` itself (load type, a data migration of
  its logged sets) is a separate change with the app.

## D135 — The e1RM reference is the last 90 days; an import moves the days that depend on it (TI-4, open point 4)

- **Why (2026-10-26):** D59 measured every set against the best e1RM ever logged. With RYFT's own
  few weeks of history that was the recent best. An import brings years: a lifter whose squat
  peaked two years ago would see every later squat session's load drop by about half (5 × 5 at
  100 kg: strain 6.9 → 3.9) until the old peak is beaten. Strength fades within weeks to months
  without training (Mujika & Padilla 2000; Bosquet et al. 2013), so an old best is not today's
  capacity. Open point 4 asked whether queueing later sessions' days (D128) is right and enough.
- **Decision:**
  - The reference is the best e1RM over the completed working sets in the **90 days** before the
    session's history ends, its own sets always included (`E1RMWindow`, `E1RMsAsOf`). The history
    ends at `ended_at` or at the session's last completed set, whichever is later
    (`HistoryUntil`; D138). Records stay all-time. `pct_1rm` targets use the same 90-day e1RM (none
    without a set in the window), so a plan never prescribes 80 % of a max from long ago.
  - Queueing the dependent days is right: a stored load must equal the rule applied to the stored
    history, or it moves later at an unexplained time (a rescore, an edit). "Stored load is final"
    would make the number depend on the order data arrived in. It was **not enough**: a later
    session without its own `bodyweight_kg` takes the last one logged before it, which an imported
    session can now be. The scope is `scoring.DependentScope` (`ScopeOf` the created and updated
    sessions plus the versions they replaced; `Covers`): later sessions sharing an exercise that
    start before the newest changed session's end + 90 days, or later sessions without a body
    weight that hold a bodyweight exercise, after the oldest changed session with one. The store's
    query may return a superset.
- **Not chosen:** a decay (a weighted best: opaque, one more constant to fit, no published curve to
  take it from); 180 or 365 days (an old peak still halves loads for that long); a window on the
  records (they are records); "stored load is final once scored".
- **Consequences:** model **2026.10.2**; `scripts/rescore_all.sh` after the deploy. In the beta it
  moves only sessions with sets more than 90 days before them (none expected: the beta began on
  2026-10-20) and sessions with a set after `ended_at`. The dependent days of an import end 90
  days after its newest session instead of reaching today. Both follow-ups ship in the same
  release (TI-6): `store.DependentDays` takes the scope's fields (the 90-day bound and the
  body-weight clause; a test compares it with `Covers`), and the api's `pct_1rm` target calls
  `E1RMsAsOf(…, now, uuid.Nil)` (`null` without a set in the window).

## D136 — Imported records stay silent, also the newest one (TI-4, open point 2)

- **Decision:** no `pr` card and no push for a record held by an imported session, however recent;
  no "newest imported record" card. `store.PRsSince` leaves them out (TI-2), so the rule needs no
  column and no state. They remain records everywhere else.
- **Why:** an imported record is history the user already saw in the other app, not news; and an
  import arrives in batches (≤ 50 sessions), so a record shown after one batch can move to an
  older session in the next, which is a card that contradicts itself within a minute. A "once"
  card needs a stored "shown" mark, for a summary the app's import screen can give from the file
  it parsed.
- **Consequences:** an import can take a record (and its card) away from a session logged in
  RYFT; that is correct. Pushes are already impossible (no `session.scored` for an import, D125).

## D137 — Days with only imported sessions are logged-session days; nothing more feeds on them (TI-4, open point 3)

- **Decision:** such a day is scored like a day with only a logged session: strain from the muscular
  load alone (a floor), `strength_min`, `volume_kg`, a `ryft-session` workout, no recovery, no
  readiness, state `calibrating`. Imported strain feeds what a logged session's feeds: the next
  day's sleep need and readiness penalty, `strength_week`, the learned sleep baseline's qualifier,
  and `calibrating_reason`'s `W`. There is no strain baseline or rolling strain load in the model
  to feed; HRV, resting-HR and breathing baselines cannot be crowded (one row per day; imported
  days are days without a night). Sessions older than the 400-day window stay stored, not scored.
- **Why:** they are the user's training, and the inputs that read strain are about training, not
  about the sensor. Treating them differently would make a day's numbers depend on which app the
  set was typed into.
- **Not chosen:** keeping import-only days out of `W` (it needs a per-day origin in `daily_scores`
  for a reason that is then true of the account anyway: the first synced day after a long import
  can say `no_hrv`, and a first night with HRV is still `short_history`); de-duplicating a
  session logged both in RYFT and in the file (both count today, strain 11 twice is 16): the app's
  import skips a file workout that overlaps a RYFT session and says so; a scoring rule follows
  only if the beta shows such pairs.
- **Consequences:** the walk after an import crosses every stored row from the oldest imported day
  on (≤ 400 days and the dependent days, seconds to a minute per request). A future rolling strain
  load (acute/chronic) must decide how days without heart rate count before it uses them.

## D138 — An assumed duration needs no contract flag; the load no longer reads `ended_at` (TI-4, open point 5)

- **Decision:** no flag now. Strain, muscular load, volume, e1RM and records do not depend on a
  session's duration: a set's load comes from its reps and weight, and the history a session is
  measured against ends at its last set when that is after `ended_at` (`HistoryUntil`, part of
  model 2026.10.2; before, a top set stamped after an assumed end was cut off its own session's
  e1RM: 30.2 instead of 28.1 in the calibration). The assumed 60 minutes show only as minutes
  (`strength_min` and its trend, `strength_week.minutes`, `duration_min`) and in the session's
  workout window (matching a HealthKit workout by half an overlap; the created workout's span).
- **Why:** a flag would buy a `null` instead of 60 in a display total and its trend; that is worth
  a contract field only if many files lack durations, which only the app can measure.
- **Consequences:** the app derives `ended_at` from the last set's time when the file has set times,
  keeps sets within `[started_at, ended_at]`, and reports how often it assumes. If it is most
  imports, an optional `duration_assumed` on the import's session lets scoring leave those minutes
  out.

## D140 — The import handler's own calls: order, budget, a resumed request's dependent days (TI-3)

(D139 is unused: D134–D139 were set aside for the scoring branch, which needed D134–D138.)

- **Consent before the body.** API.md says both gates are checked "before anything in the body is
  read"; design §4 listed the decode first. The contract wins: `gate.Consent` runs first (a refused
  upload costs no 1 MiB parse, as in ingest), so a malformed body without consent is a `403`, not a `400`.
- **One consent rule, `internal/gate`.** Ingest's `admitted`, the api's invite gate, `GET /v1/me/sync`'s
  `gate` and the import share `gate.Consented` / `gate.Apply` (design §4 offered `internal/beta` or
  `httpx`; a small package keeps `httpx` free of `config` and `model`). Behaviour of the existing routes
  is unchanged, `ryft_gate_refusals_total` included.
- **The finish fits inside the 50 s.** The items run on a context that ends 10 s before the request's
  deadline; the finish (records, queue, event) then has its own 10 s on `context.WithoutCancel`. So the
  answer, `503 "request timed out"` included, goes out by 50 s, inside the server's 60 s write timeout,
  instead of a finish that could overrun it and cut the connection.
- **A resumed request finds the dependent days too.** Design §7 takes `DependentDays` from the created and
  updated sessions only. After an interrupted attempt the retry's items are all `unchanged`, so the days
  of the user's later sessions (whose load depends on the imported e1RM history) would never be queued.
  An `unchanged` session whose day is still pending (`PendingAmong`) counts like a changed one: it
  joins the sessions `scoring.ScopeOf` gets (D135: the created and updated ones and the versions
  they replaced, `ImportOutcome.Replaced`). Cost: a few more days rescored when a sync's pending day
  happens to be an imported session's; harmless.
- **Smaller calls.** A plan or a session without an id is that item's `invalid`, not a whole-request
  `422`; the duplicate check skips the nil id. `train.Problem` gets no `Code`: the store's rejections
  carry their codes (`importReject`, D131) and every `Problem` from `internal/train` is `invalid`.
  `ryft_train_imports_total` adds `result="invalid"` (whole-request refusals after authentication) to
  the design's `ok`, `busy`, `timeout`, `error`; a client that went away counts as `timeout`. A database
  failure is `503 "the database is unavailable; send the same request again"` with `Retry-After: 5`,
  logged as httpx's `request failed`. The lease is given back before the answer is written, so the app's
  next request does not meet its own lease. `assist_kg` on a set of an exercise that is not `bodyweight`
  is `422` / `invalid` for any value, `0` included (API.md's wording); `null` passes.
- **Enrolment (T8).** With only `started_at` the stored or default position is kept and not checked
  against the plan's days; the day lookup (`CurrentPlanDay`) runs when `current_week` or
  `current_day_index` is given, on the resulting position, inside `Enroll`'s transaction, so a refused
  position also leaves the other plan's enrolment active.

## D141 — The import's review round: the app's edits win, ids per account, the server's own checks (PR #59)

- **Edits win, for sessions and plans (B1).** `sessions.imported_at` and `plans.imported_at` (migration
  0028, not yet applied anywhere, so edited in place) are set with `updated_at` by the import's write, in the
  same statement. `updated_at` **different from** `imported_at` (a `PUT` of any field, a completion,
  archiving a plan) or no `imported_at` (a plan the app made under that id) is `newer_on_server`: a
  resend never overwrites it. Not "later": a `PUT` whose transaction began just before the import's
  commits an earlier `now()` after it, which "later" would read as untouched (the final review's
  probe pins that ordering). Any save counts, even one back to the imported content
  (`train.ImportConflict`, `train.PlanConflict`, pure). Scoring's load write does not touch
  `updated_at`, so being scored is no edit. Decided for plans as well: an imported plan edited or archived in
  the app is the user's.
- **Ids per account (B2).** Plan ids are global (session ids are per account), so the app derives every id
  under `UUIDv5(import namespace, user id)`; the same file in two accounts gives two plans. `id_taken` keeps
  one message whoever holds the id (a built-in, another plan, another account).
- **The server checks what the app does (S1-S4).** Time bounds per session (≤ 24 h long, ending ≤ 24 h
  ahead, sets within an hour of it, `updated_at` ≤ 24 h ahead) as the item's `invalid`. Its own rate class,
  bursts of 5, then one every 5 s. Consent `warn` acts as `on` on this route: every app build that calls it
  has the consent screen. The flag `train.import` is evaluated after the gates (`403 import_not_enabled`,
  a new additive code; `ryft_train_imports_total{result="disabled"}`), so a kill switch does not depend on
  the app's cached config.
- **Set caps (N1), my call:** at most 300 sets in a session (that item's `invalid`, like the other
  per-session limits) and 2 500 in a request (a whole-request `422`, like the counts: it is a property of
  the batch, and the app splits it).
- **Finish order (SF1):** the days and their event are queued first, the records after, so a slow records
  step cannot strand stored days without a queue row; its `503` is repaired by the resend.
- **Smaller:** lease 70 s (above the 50 s budget plus the 5 s release, so a rollout leaves no long `429`);
  `score_queue` grants for `ryft_api` per column (`INSERT (user_id, day, queued_at, scored_at)`, `UPDATE
  (queued_at, scored_at)`); 0028 takes `users` first, the services' order; queued days are upserted sorted;
  no `source` in the log line (it says which other app the user trains with); `ImportKind` decodes unknown
  kinds as `.unknown` in the Swift mirror.
- **Not changed, documented:** `expected_within_sec` is the p95 over every `score_queue` row of the last 6
  hours, imports included, so a large import raises the estimate for a while for everyone (one queue, one
  scoring consumer: that is what a sync then waits for too). Keeping import rows out needs a reason column;
  not worth a schema change now.
- **Open points** (follow-ups, not in PR #59):
  - *A merge and a re-import duplicate history.* The app derives import ids from the account id, so after
    an operator's merge (D90) an import of the same file into the kept account stores it again under
    other ids. Documented for the app (API.md) and the operator (OPERATIONS → Two accounts). The fix is a
    server-held `import_namespace` on `GET /v1/me` that the app derives from and a merge carries over to
    the kept account.
  - A sweeper for `score_queue` rows left pending by an item whose request died before its finish (no
    event names those days until the app resends; `GET /v1/me/sync` shows them pending meanwhile); the
    api's shutdown drain (10 s) against an import's 50 s budget, which a rollout can cut (the lease then
    expires after 70 s); a per-account secret namespace instead of the account id.

## D142 — Mail for ryftapp.de is Google Workspace; its records live in Terraform

- **Chosen by the user: Google Workspace.** AWS WorkMail was ruled out: closed to new customers since
  2026-04-30, end of life 2027-03-31, and the platform role has no `workmail:*`. The data-processing and
  transfer lines of the privacy texts (Google Workspace, Cloud Data Processing Addendum, DPF + SCCs) are the
  user's own wording.
- **Records in `infra/aws/dns`**, next to the CAA records: MX `1 smtp.google.com`; the apex TXT as **two
  values** (the `google-site-verification=…` string and `v=spf1 include:_spf.google.com ~all`; the hand-made
  record had both inside one quoted string, which resolvers read as one broken value); DKIM TXT at
  `google._domainkey`, split into strings of at most 255 characters (Route 53's limit) by the record, joined as `chunk1" "chunk2` with **no outer quotes** (the AWS provider adds them; wrapping the value too failed the first apply with `InvalidCharacterString`);
  `_dmarc` TXT. The records were created by hand first, so they are **imported**, not recreated.
- **The DKIM value is a variable without a default** (`dkim_record`): the selector's key is the user's
  to rotate, and a default I could not read from the live record would plan a change to a working key.
- **SPF `~all` (soft fail)** as set up; tighten to `-all` with DMARC. **DMARC `adkim=s; aspf=s`** (strict):
  the only sender is Workspace on `ryftapp.de` itself, so strict costs nothing and a spoof from a subdomain
  fails; relax to `r` if a sub-domain sender is ever added.
- **DMARC ramp:** `p=none` with `rua=mailto:support@ryftapp.de` now; after 2–4 weeks of clean aggregate
  reports `p=quarantine` (the `dmarc` variable), then, if still clean, `p=reject`.
- **TTL 3600** on all of them (as the CAA records); lower to 300 for the day of a DKIM rotation.

## D143 — The minimum age is 16 (amends D81's range)

- **Chosen by the user** for the public beta: `scoring.MinAge = 16` (was 13). `PATCH /v1/me` accepts
  `birth_year` from 1920 to the current year − 16 and answers `422` otherwise. The range is one pure function,
  `scoring.ValidBirthYear`, with a boundary test (year − 16 accepted, year − 15 rejected).
- **Stored birth years are not touched.** No migration, no clearing: a value saved while the limit was 13 stays,
  and so does the `max_hr` derived from it. Only a new `PATCH` is checked. The few TestFlight accounts with
  such a value are friends and testers; rewriting a health-adjacent field silently would be the worse surprise.
- The privacy texts (2026-10-26 on) say 16 in the same place; the API contract has a changelog entry.

## D144 — HealthKit ids are deletion handles; the natural key stays the identity

- **`hk_id` (nullable uuid) on `samples` and `sleep_segments`, unique per user where set** (partial index, its
  own migration 0030; 0029 adds the columns). A record that arrives with an `id` first **adopts** the stored row with
  its natural key (this backfills rows sent before the app had ids, and follows a source that re-writes a record
  under a new UUID), then inserts target-less with `ON CONFLICT DO NOTHING`: no unique index can ever fail a sync.
  An id already held by another row (a renamed source) is a `duplicate`.
- **Why not the id as the identity:** Garmin re-writes a sample under a new UUID and deletes the old one. If the
  first UUID won, that deletion would remove a record Health still has. Latest upload owns the id.
- **Accepted risks:** two live HealthKit objects with one natural key leave one row whose id is the last uploaded,
  so deleting that object removes the row while the other copy exists until the next upload restores it; a second
  device uploading an old object after the rewrite flips the row back. Both heal on the next sync of the record.
- Workouts keep the HealthKit UUID as their `id`. Merge (D90) moves a row unless the target holds its `hk_id`.
- Merge runs as `ryft_api`, which may update `user_id` of the raw tables and nothing else (0024), so the merge
  does not copy an `hk_id` onto the kept row. Where both accounts hold the same natural key, to's row is kept as
  it is and the dropped row's id is lost; a later deletion of that id finds nothing (`unknown_id`) and the record
  stays until the next sync re-uploads it under its id. The guard on the moves (the id is not already held by
  to) is what prevents the unique violation. No grant was widened for this.

## D145 — A sync's deletions, queued days and event commit in one transaction, under a per-user lock

- Upserts first, then one apply transaction under `pg_advisory_xact_lock(7419005, hashtext(user))` (a class of its
  own; 7419004 is the plan-enrolment lock): deletes, the touched days into `score_queue`, the outbox event.
  Everything is scoped to the token's user; a foreign id and a missing id both count as `unknown_id`, so ids
  cannot be probed. A failed apply is a `503` with `Retry-After` and a re-send is harmless (before, a queue
  failure was swallowed).
- **`ryft_ingest` gets `DELETE`** on `samples`, `sleep_segments` and `workouts` (0029). `ryft-session` workouts
  are refused in the SQL and, on Supabase, by a restrictive RLS policy; the workouts upsert no longer overwrites
  such a row. Deleting a Train session's workout is the detach: scoring re-creates the row under a new id.
- **Accepted, security review:** a compromised ingest pod can now erase raw rows, not only overwrite them; the
  feature needs the grant and Supabase's backups cover it. No privacy-text change (a pseudonymous technical id
  for the existing purpose; deletions shorten retention). The device id in the `sync` log line predates this and
  is not named in the text; for the next privacy revision.
- **One-way door:** after an app build sends `deleted` or `id`, ingest cannot be rolled back past this change
  (OPERATIONS → Deploy and roll back).

## D146 — The same-source fold lives in ingest, not scoring

- **Rule:** two workouts of one user with the same `source` (the name; `workouts` stores no bundle id, so the
  brief's bundle-id key is not available), the same `activity`, `|Δstart| ≤ 120 s` and `|Δend| ≤ 120 s`, **and an
  overlap of at least 50 % of the shorter one** are one workout (connected components, so a chain of copies is
  one). The overlap clause is the code review's: without it, six 45-second interval workouts a minute apart, which
  never overlap, folded to one and five were deleted for good. Real copies overlap about 100 %. Keep the highest `created_at` (the most recent
  upload, which the upsert never changes), ties to the higher id; delete the rest; `ryft-session` rows are never
  folded. `session_id` is not moved: the fold deletes and queues the days, and the re-score re-attaches the session
  to the kept workout, the same path a deletion takes. The rule is a pure function (`scoring/workoutdup`, which
  imports only `model`, so the store and ingest use it without a cycle).
- **Where:** in the sync's apply transaction (D145's lock, so two syncs that each insert a copy converge on one),
  and as `ingest fold-workouts [-user <id> | -all] [-dry-run]`. Not in scoring: its RLS allows deleting only `ryft-session`
  workouts (0011), so a scoring subcommand would have deleted nothing in production while its dry-run listed the
  folds. Ingest is the one writer of raw rows and has `DELETE` since 0029.
- Folded ids answer `404`, are not counted in `SyncResponse.deleted`, and a later deletion of one is `unknown_id`.
  A fold cannot be undone by the platform. The brief's plan to save the rows to a CSV was dropped on the security
  review: it puts health data on an operator's laptop outside the 7-day retention the privacy text promises, and
  the dry-run's ids can differ from what a later run deletes. The real run prints what it deleted (ids and days, no
  health values); recovery is Supabase's own 7-day backup. A folded copy overlapped the kept one and started and
  ended within two minutes of it, so what is lost is a near-identical record.
- **The subcommand is guarded:** unexpected positional arguments exit 2 (a mistyped `-dry-run` once meant a real
  run over every user), the destructive run over all users needs `-all`, and users without consent are skipped, as
  `rescore` does. It is a one-off process and serves no metrics, so only syncs' folds show on
  `ryft_workouts_folded_total{reason="same_source"}`.
- **Cost caps:** at most `MaxWorkouts` workouts per request (`422`), the fold is skipped above a row count in scope
  (`ryft_workouts_fold_skipped_total`), and the clustering checks its context.
- **Accepted risk, ties:** copies that arrive new in one request share a `created_at` (one transaction), so the
  higher id is kept. If Health later deletes exactly that object while still holding the other, the next sync's
  deletion removes the last row and the other copy returns with the next upload of it. This needs both copies
  alive in Health at the same anchor, which a rewriting source does not do.
- **Known, not changed:** a client can still upload a workout with `source: "ryft-session"`; it is the user's own
  data, such a row is never folded, hidden or deletable through sync.

## D147 — `duplicate_of` is a scoring output without a foreign key

- **Rule (the user's decision):** different `source`, the same `activity` (or either is `other`), and an overlap
  of at least 80 % of the shorter workout. The primary is the one with at least 2 heart-rate samples **from its own
  source** inside its window (the window itself is by time over every source, so "has heart rate in its window"
  decides nothing for overlapping workouts), then the longer, then the lower id. **Decided by the user on the
  code review (U3b): a workout with a specific activity ranks above one recorded as `other`, before heart rate and
  length.** `other` still matches as a duplicate but can only hide behind a specific one. Otherwise a longer Garmin
  `other` hid the Watch's real strength workout, the one that carries the Train session.
- **Greedy primary, not connected components (scoring-scientist's refinement, accepted):** the best-ranked workout
  becomes the primary and hides only the workouts that overlap it directly; then the next best, and so on. There
  are no chains. With plain components a Watch run split in two halves would hide the second half behind a Garmin
  recording it does not overlap whenever the Garmin has no own-source heart rate.
- **No hard deletion:** the duplicate is hidden, not deleted (`GET /v1/workouts/{id}` still answers 200 for it).
  It is recomputed on every scoring of the day, which heals late heart rate and a deleted primary; until then a
  primary deleted through sync shows its duplicate at once, because a duplicate is hidden only while its primary
  exists **and is itself visible** (two days' passes can disagree around midnight, and a chain must never hide a
  run completely). `GET /v1/workouts?include_hidden=true` returns the hidden ones with `duplicate_of`, so the
  in-app export stays complete (privacy text §7). `ryft-session` workouts are never hidden. Day strain is unchanged: it uses the de-duplicated heart rate.
- **No FK:** merge moves workouts by `UPDATE user_id`, and a self-reference `(user_id, duplicate_of)` would fail
  when a primary moves and its duplicate stays. Scoring alone writes the column (`UPDATE (duplicate_of)` for
  `ryft_scoring`, 0031). `ryft_ingest` holds table-level `UPDATE` on `workouts` (0009) and so technically could;
  that cannot be narrowed additively, so a test fails if ingest code ever names the column.
- **Cost and reach:** above 200 workouts near a day, scoring skips the cross-source decision for that day (it logs
  the count only and clears the day's `duplicate_of`); one heart-rate query serves every matched workout. Scoring
  reads from 36 h before the window, so a workout up to 48 h long that started earlier is seen whole.
- **Model 2026.10.3 ships with this PR** (not with the coverage PR): CI promotes every merge, so the stored numbers
  this can move (`details.workouts`, which workout carries a session's link, muscular load and strain, and the new
  `duplicate_of`) must not change under the old version. The changelog entry has an empty `affects` (no score
  metric moves; `GET /v1/model` documents that it may be empty). The coverage PR bumps again; one
  `scripts/rescore_all.sh` after the last of the two deploys covers both.

## D148 — Go coverage: a relative gate on internal/, no floor

The `test` job writes a coverage profile (still `-race`), prints a per-package table in the job summary and uploads
it as the `coverage` artifact. On a pull request `coverage-gate` fails when the `internal/` total is more than
1.0 percentage point below main's (`scripts/coverage.sh`, tests in `scripts/tests/coverage_test.sh`).

- **No absolute threshold.** A floor would pin today's gaps (store, amqpbus and db only count where Postgres and
  RabbitMQ run) and invite padding tests. The gate only stops a drop. **Honestly:** a drop of up to 1 point per PR
  passes, so many PRs in a row can still slide coverage down; the run summary on main shows the number, and
  `docs/STATUS.md` records it. If that happens, lower `MAX_DROP` or add a floor then.
- **Baseline = the newest successful `push` run of `ci.yml` on main** (run by this repository), then that run's own
  `coverage` artifact. Not the merge base of the PR: that would need the artifact of an arbitrary old commit, which
  is often expired or never built. The cost: a PR is compared with main's tip, so a PR behind main can look worse
  or better than it is; rebase before judging a borderline result.
- **Only `internal/` counts** (statement-weighted). `cmd/` is thin wiring and is in the table, not in the number.
  The module path is read from `backend/go.mod`; a profile without any `internal/` statement fails the report
  (exit 2) instead of reading as 0 %.
- **Infrastructure errors pass with a warning.** No successful run or no artifact yet: the gate passes and says so.
  An API error, a failed download or an unreadable `total.txt`: it passes with `::warning`, and the reason is in
  the log and the summary. A gate that fails on an artifact outage would block unrelated merges; the upload step is
  `continue-on-error` for the same reason (it must not hold back `images` on main). Malformed numbers in the
  current total or `MAX_DROP` fail (exit 2); a malformed baseline is dropped with the warning.
- **No override** besides editing `MAX_DROP` in the PR's own `ci.yml` (visible in the diff and reviewed). The
  `test` job has `actions: read` at job level only, to list and download main's artifact; no secret.

## D149 — Scoring golden files: what they pin, how they move, what they leave out

`internal/scoring/testdata/golden` holds six synthetic users × 30 days (2026-10-05 .. 2026-11-03, both autumn DST
changes) as everything scoring stores: every `daily_scores` column, the `workouts` and `sessions` rows, and what each
sync's `ApplySync` returned (the days it queued, the copies the fold removed; SCORING.md → Model version).
`TestGoldenScores` regenerates them from a fixed seed through the store calls ingest and the api make and the real
Engine, and fails with the changed fields grouped by field.

- **Numbers rounded to 1e-6, printed shortest.** Postgres renders floats with `extra_float_digits = 1` (shortest
  exact), but a last-bit difference between arm64 and amd64 (fused multiply-add, `math` on different CPUs) would
  otherwise fail CI for a refactor that moved nothing. No stored score is shown with more than one decimal, so 1e-6
  hides nothing a user could see; a real change moves a number by far more. Ids are replaced by readable labels
  (`workout Garmin Connect 2026-10-18T00:01`), so the files never look like secrets to the scanner and a diff says
  which workout moved.
- **The update guard.** `make golden-update` (mode `1`) refuses while `ModelVersion` is the manifest's, unless the
  generator's inputs changed; then only for those users, and every other user may only gain fields or rows. The
  inputs' digest is a SHA-256 over canonical `field=value` lines of what the generator sets (not the JSON of the
  model structs), so a new model field the generator leaves zero does not reopen every user. `GOLDEN_MODE=additive`
  takes new fields and rows only; `GOLDEN_MODE=init` makes the first set and nothing else. Every update starts from
  the committed manifest (on disk = `HEAD`'s), so two updates in a row cannot stack a model change behind a
  generator change. An update refuses a database host that is not loopback or a socket unless
  `GOLDEN_ALLOW_REMOTE_DB=1`: the run creates a database and the service roles on that server.
- **The digests are checked, not trusted.** `TestGoldenManifest` (no database, always runs) recomputes every user's
  input digest from the generator as it is and requires exactly one manifest entry per user, and recomputes the field
  digest from the files. Before that, a regeneration with a generator edit that was then reverted (or a digest edited
  by hand) left a moved digest that let any value change pass without a `ModelVersion` bump; that gap is closed.
- **`scripts/golden_guard.sh <base>`** re-checks the rule on a pull request against the merge base: a changed
  `user-*.json` needs a `ModelVersion` bump (the manifest's `model_version` at both refs, which `TestGoldenManifest`
  ties to `version.go`), a moved input digest for that user, or a moved field digest (additive); a file kept without
  its digest fails. It exits 2 when it cannot check (no `jq`, the base is not a commit here, no merge base in a
  shallow checkout). Manifest values and paths are printed only in their expected form (digits, `YEAR.MONTH.N`,
  `user-<name>`), never raw: a `::` in a printed string would be a workflow command. CI runs it as the `golden-guard` job
  (pull requests only, whole history, path-filtered, in `ci-ok`'s needs). **Honestly:** the field digest is one value for all files, so a PR that adds a column lets
  every user file change in the script's eyes; the Go guard's additive mode is what stops a moved value there, and
  review does the rest. A deleted user file (a rename counts as a deletion plus an addition) fails unless
  `ModelVersion` or the manifest's `users` list changed. The job runs the PR's own copy of the script, so it is a
  tripwire for accidental regenerations, not a security boundary: a PR that touches `scripts/golden_guard.sh`, the
  `golden` filter, the `golden-guard` job or `ci-ok.needs` says so in its description and gets that diff reviewed
  explicitly.
- **CI fails instead of skipping.** Locally the test skips without `RYFT_TEST_POSTGRES_URL`, as every engine test.
  With `GITHUB_ACTIONS=true` or an update mode it fails: a CI job that lost its Postgres must not read as green.
- **Superuser pool.** The run uses the test's admin pool (migrate, seed, then a `Store` on a superuser, or at least
  the tables' owner: RLS is not `FORCE`d, so either bypasses it), not the `ryft_scoring` role. The golden files pin
  numbers, not grants: the roles' privileges and RLS are `internal/db/roles_test.go`'s (`TestServiceRoleGrants`,
  `TestServiceRolesLogInAndPassRLS`), not this test's. A scoring query that needs a grant the role lacks would pass
  here.
- **Not covered.** HealthKit deletions (`Apply.Deleted`, and the days they queue) are not part of the generator; the
  ingest handler's `validate`, `screen` (its 400-day window reads the wall clock) and `touchedDays` run before the
  point the harness starts, so the apply gets no days and the pinned `syncs` hold the fold's days only.
  `train.ImportOrder` only decides the order an import stores its sessions in, which only `personal_records` depend
  on, and those are not pinned: a mutation that sorts the import newest first passes. Newest-first scoring is
  checked on the last ten days only (the whole month costs ~465 day passes a user). The generator is the coverage:
  a scenario it does not have is not pinned.
- **Not marked as generated.** `linguist-generated` would collapse the files in GitHub's diff view, but it needs a
  `.gitattributes`, which `scripts/check_bytes.sh` refuses by name (D115: a `working-tree-encoding` can turn text
  into a hidden line break). The files stay expanded; the failing test's grouped report, not the raw JSON diff, is
  what a reviewer reads first.

## D150 — A replayed dead letter is a redelivery; operator scripts name their cluster (amends D99, D112)

- **Amended by D218:** on Pub/Sub the marker is the attribute `ryft-replayed=true`, exactly `true`; anything else is
  ignored and logged. `scripts/dlq.sh` pulls from the hold subscription with `gcloud` and republishes to the original
  topic. The replay rule stays: only the keys the consumer handles, and only a letter whose key is its topic's. The
  `x-death` and `Ryft-*` handling below is RabbitMQ's and goes with it.
- **Why (2026-10-23):** `dlq.sh replay` stripped every header that said a letter had been handled
  before, so scoring took it as a first delivery and a re-score left half-done by the failed attempts
  stayed so (D112's open item). And while building the fix, an agent ran `dlq.sh` to read its usage:
  the script set up its cluster access before parsing the command, and kubectl's current context was
  production — two Secrets read, a port-forward opened, nothing written (reported to the user).
- **The marker:** `x-ryft-replayed`, boolean `true`, set by `dlq.sh` on every replayed letter;
  `amqpbus` marks the ctx with `events.WithRedelivery`. Not a faked `x-ryft-attempt` (the retry count:
  a replay would start in a later tier or dead-letter on its first failure). Strict: only the exact
  name with a boolean `true` counts; anything else is ignored and logged with its type, never its
  value. A replay gets all six attempts again and counts in the redeliveries metric.
- **What may be replayed:** per dead-letter queue only the keys its consumer is bound for (scoring:
  `ingested|session.completed`; notify: `scored|session.scored|refresh`), and only a letter whose
  type equals its key; anything else goes back unchanged and is named. A delivery from `ryft.events`
  is handled and filed by its routing key (topic permissions constrain it); a type that is neither
  empty nor that key is a bad event. So a forged type can't be laundered past the permissions by
  the operator's replay, nor reach another handler.
- **Letters the broker dead-lettered** (its delivery limit, or the consumer's own copy into the DLQ
  failed) carry no `Ryft-*` the consumer wrote, so a replay ignores any such header on a letter with an
  `x-death` entry for the consumer's own queue: the key is that entry's routing key, taken only when the
  entry's exchange is `ryft.events`, and the type must be empty or equal it. A letter that was retried and
  then dead-lettered by the broker has an entry from `ryft.requeue` with routing keys `[consumer]` (a
  retry's copy carries the queue's name); its key is its type, if that is a key the consumer is bound for
  (the consumer wrote it, checked against the routing key on the first delivery; only consumers may publish
  into the retry tiers). Anything else is "dead-lettered by the broker: check by hand", with what is wrong
  named; OPERATIONS has the manual publish. amqpbus drops `x-death`, its `x-first-death-*`/`x-last-death-*`,
  `Ryft-*` and `x-ryft-attempt` from a first delivery from `ryft.events`, because a publisher sets any
  header: a forged `x-death` would pick the key, and a forged attempt count of -1 crashed the consumer
  (`RetryTiers[-1]`) or, large, skipped the retries. `attemptOf` and `tierFor` clamp as a second line.
- **Residual:** a letter dead-lettered by an image from before this has `Ryft-Subject` = the type its
  publisher set, not the routing key it arrived with. That image also copied every header the publisher
  sent, so such a letter can carry a forged `x-death` entry for the consumer's queue too; the script then
  reads the key from the entry and requires the type to equal it, so the outcome is the same: a letter
  whose type, `Ryft-Subject` or entry say one bound key and nothing contradicts it is replayed under that
  key. ingest, which may publish only `ingested`, could have sent a `session.completed` under `ingested`
  with type `session.completed`, which the old image filed as `session.completed`. OPERATIONS asks for
  `show` or `purge` before the first replay after the rollout; the DLQ's 14-day TTL ends the window.
- **No letter lost:** each taken letter is written to its own files, put back on any failure or
  signal (an uninterruptible way out), or kept with its path printed (`dlq.sh put-back <dir>`);
  credentials sit in a mode-600 curl config, never in argv; bodies go from files.
- **Operator scripts name their cluster:** `dlq.sh`, `rescore_all.sh`, `trace.sh` and every Makefile
  target that reaches a cluster require `KUBE_CONTEXT` (checked against the kubeconfig, announced,
  passed as `--context` on every call; `scripts/lib/kube.sh`, `kube.mk` with `override`s and the
  guard in each call's own shell line, so `make -e`/`-i` can't skip it). `k8s-apply-local`,
  `k8s-delete-local` and a non-production `k8s-rabbitmq` refuse a context that looks like EKS (name,
  cluster ARN, server, token exec: a heuristic, not a proof; a context that reaches EKS by none of these
  signs is taken for a local one). `k8s-rabbitmq` takes exactly `deploy/rabbitmq` or the local overlay.
  A directory a failed replay kept records its source (context or broker URL) and `put-back` refuses
  another. Arguments are checked before any cluster access.
- **Consequences:** no `POST /v1/recompute` after a replay once scoring runs an image with this
  (OPERATIONS says so for the window before). A replay can cost up to six full catch-up re-scores per
  letter that keeps failing; OPERATIONS has the checklist (one letter first, 16 minutes, the alerts).
  Plain `kubectl` lines in the runbooks still use the
  current context: the user's kubeconfig default should not be production (STATUS).

## D151 — The kind run of the admission policies: what it found and what was decided (T12-d; amends D120 by a note)

- **Amended by D183:** the "Claim m disproved" bullet's consequence ("T12-e needs another binding-state signal") no
  longer holds: T12-e shipped with kube-state-metrics' native collector (#55).
- **Why (2026-10-26):** the policies were only ever evaluated offline (D120's CEL harness). The kind run
  (`scripts/admission_e2e.sh`, CI job `admission`, workflow `admission-e2e`) put them in front of a real
  API server, a real Argo CD and the real operators, and found things the harness could not.
- **P10 changed intent (chart 0.2.2).** The controller's automated sync carries a copy of the
  Application's `spec.source`, so "no `operation.sync.source`" refused Argo CD's own auto-sync. P10 now
  admits a source equal to the Application's own `spec.source`/`spec.sources`. Consequences: `argocd app
  rollback` is refused **only for argocd-server, through the revision rule** (a history entry's
  revision is not the synced one); "no foreign source" rests on **P7**, which pins the whole `spec`.
  **P7 must not go to break-glass on the assumption that P10 blocks `--repo`, `--path` or a changed
  source.** The header comment of the template and design §2 say so; D120 is amended by the note above,
  not rewritten.
- **P11 is an allowlist (chart 0.2.3).** The Cluster Operator's defaulting webhook writes
  `secretBackend.externalSecret.name: ""` into every RabbitmqCluster; 0.2.2 exempted the empty name by
  matching on `vault` and a non-empty name. Now `secretBackend` may hold only `externalSecret`, and that
  only with an empty (or absent) name and no other field, so a Vault block next to the default, a named
  secret and any key a later CRD version adds are refused. The harness pins each shape.
- **Claim k, answered.** argocd-server writes `operation.sync.revision` = the full commit SHA of the
  revision the Application was compared with (`status.sync.revision`), not `""` and not `main`. P10's
  third alternative is what admits a manual sync, so D120's `--revision <sha>` risk is closed.
- **Claim i needs one more rule.** With `resource.respectRBAC: strict` the controller must also get,
  list and watch `applications.argoproj.io` cluster-wide, or the app-of-apps waits for the health of its
  children for ever. #47 (`infra/argocd-rbac`) lacks it; add `{ apiGroups = ["argoproj.io"], resources =
  ["applications"], verbs = ["get", "list", "watch"] }` to `argocd_controller_rules` before Step 0.
- **Claim m disproved.** kube-state-metrics' custom-resource-state exports CustomResourceDefinitions
  only ("no matching CRD found for configured GVK"), so it cannot export the bindings. T12-e needs
  another binding-state signal (the manifest tail with the bindings in its object list and a log alert,
  or an exporter of its own); the `claim-m` line of the kind run stays INFO until a version can.
- **`admission` is skipped on a promote merge to main too** (the design said "every push to main"): the
  diff is the pins the PR carried, which `scripts/promote_only.sh` (the base branch's copy; a base
  without one means "no") recognises line by line; any doubt runs the job. The generated fixtures no
  longer contain the overlay's pins (`0000000`), otherwise every release made them stale.
- **Not done (and said so in the docs):** a node drain, three workers (the broker runs one replica on the
  default StorageClass), claims f (SSD comparison error), j (`take_ownership`) and l (the VPC). The
  large-Pod CEL-cost fixture is harness-only.
- **Stubbed in the full mode:** Vault and VSO (the CRDs are real, a loop writes dummy Secrets and marks
  them synced), the service images (busybox, tagged inside the node under the exact names, none left in
  the local image store), the git remote (a smart-HTTP server in the cluster, the Applications rewritten
  to it), Ingress status.

> **Amendment (2026-11-04, security review of #47; replaces the wording "waits for ever" in the bullet
> "Claim i needs one more rule" above).** The first kind run of #47's tree passed claim i without the
> rule, so "waits for ever" was wrong. What happens: under `resource.respectRBAC: strict` the controller's
> cache lists each kind in all namespaces; when that is forbidden it asks a self-subject access review
> for namespace `*`, is denied, and **drops the kind without a log line**. The chart's Role in `argocd`
> (full rights on Applications there) does not count, and `GetManagedLiveObjs` falls back to a GET by
> name. Without the cluster-wide `applications.argoproj.io` get, list and watch: a child's change does
> not refresh the root until its periodic reconcile (about 3 minutes; the run showed the root
> Progressing for about 6 while every child was Healthy), a child whose file was deleted from git is
> **not pruned**, and a hand edit of a child is seen only at the root's reconcile. The general pattern:
> under strict mode any kind without a cluster-wide `list` is silently dropped (no tree, no prune, no
> log). The rule is now in #47; the check is `kubectl auth can-i list <kind> -A
> --as=system:serviceaccount:argocd:argocd-application-controller`, which the kind run does for the
> kinds of the apps (`claim-i-list`). A missing `get` on a rendered kind is a `ComparisonError` (sync
> status Unknown, `ArgoAppOutOfSync` after 30 minutes, the old release keeps running); a missing write
> is a failed sync (`ArgoSyncFailed`, critical, after the retries).

## D152 — The tree under test is code (T12-d)

- **Amended by D181:** the Terraform scan is a tripwire (a lexer with a longer refusal list, not "refuses a module with a
  provisioner, ..."), and a tree that is not this checkout needs its provenance (#114, #116).
- **Decision:** the kind run reads a tree (`--repo`, the workflow's `ref`) that kustomize renders,
  Terraform evaluates and Argo CD syncs, so the scripts treat it as code: only files git tracks are
  copied (no tfplan, tfvars, `.terraform` or git-ignored `secrets.yaml`; a tracked symbolic link is
  refused); Terraform runs under `env -i` with no cloud, HCP Terraform or Kubernetes credential and
  refuses a module with a provisioner, `data "external"`, `data "http"` or a `*.tf.json`, or a lock file
  naming other providers; every name, path, image reference and label taken from the tree is validated
  before it reaches a command line; `kc`, `kca` and `hc` refuse arguments that would choose another
  cluster or user; the guard accepts only `https://127.0.0.1:<port>` (or `[::1]`) with one `kind-*`
  context, no proxy, no plugin user, not in `~/.kube` (links resolved), and with `--kubeconfig` the file
  must equal `kind get kubeconfig`.
- **Not a sandbox.** An untrusted tree runs in the `admission-e2e` workflow or on a machine without
  production credentials, never on the laptop whose kubectl points at production. The workflow with a
  `ref` must be dispatched from the branch under test (not the default branch), refuses `refs/pull/*` and
  commits on no branch of the repository, and has no cache.
- **Pinned:** kind, kubectl (the EKS minor), helm and yq by version and sha256 in `scripts/install_*.sh`;
  their versions are printed in the summary. The chart's `version:` must go up when its templates or values
  change (`scripts/check_chart_bump.sh`, a pull request).

## D153 — Admin sign-in by Supabase token: claim, allow-list and fresh MFA (admin web, PR1)

- **Decision:** `/v1/admin/*` accepts `ADMIN_API_KEY` (compared first, SHA-256 then constant time) or a Supabase
  access token that verifies like every `/v1/*` token **and** carries `app_metadata.role == "admin"` (never the
  top-level `role`, never `user_metadata`, which the user can write) **and** whose user has a row in
  `admin_accounts` and no tombstone in `deleted_users` **and** has `aal == "aal2"` with a `totp` `amr` entry at
  most 12 h old (30 s of skew). Every other bearer gets one identical `401`; a real admin without a fresh second
  factor gets `403 mfa_required` (and an audit row); a token that cannot be checked now gets `503` with
  `Retry-After: 5`. The new claims are parsed leniently (`Principal.AppRole`, `AAL`, `MFAAt`): a claim of an odd
  shape reads as empty and never rejects an app user's token.
- **Why:** a JWT cannot be revoked before `exp` (1 h), and a Supabase session never expires by itself. Granting is
  two statements in one trust domain (the Supabase project owner), not two parties that must agree; the
  owner-written, api-readable row exists so that revoking takes effect on the next request, whatever the token's
  lifetime. The 12 h rule bounds what a stolen refresh token is worth without the second factor.
- **`admin_accounts` is operator configuration, not user data:** no foreign key, deliberately not part of account
  deletion (the tombstone check covers a deleted admin); the api role can only `SELECT` it (migration 0032).
- **Least privilege for the browser:** a token reaches only the routes the page needs and gets a server-side
  projection of the users list and the feedback (`AdminUserBrief`, `FeedbackBrief`: no health-derived, device,
  consent, request-id or diagnostic fields). Merge, flag writes, user detail and resync-hint stay key-only (`403`,
  audited). The routes are classified in one table (`admin.routes()`, zero value key-only), pinned by tests.
  `FeedbackBrief.message` is free text a tester wrote and may fill with health statements; it stays in the
  token's answer by design (the operator reads it to answer the tester), as `Feedback.message` is for the key.
- **Known and accepted:** a token with an unknown key id inside the one-a-minute JWKS refresh limit answers `503`
  (`jwks_refresh_limited`) instead of `401`, because `auth.classify` cannot tell a rotation from a forged id; it is
  kept out of `AuthUnavailable` and documented (API.md), and fixing the classification is a follow-up.
- **Would change with:** a second admin (then flag writes for tokens), or Supabase session introspection.
- Design: `docs/work/2026-11-05-admin-web/design.md` §2.1, §3, §4.2–§4.3.

## D154 — CORS on the admin routes only, from `ADMIN_WEB_ORIGINS`

- **Decision:** `httpx.CORS` wraps `/v1/admin/` and nothing else: exact origins (no `*`, no `null`, no wildcards),
  methods `GET, POST, DELETE`, headers `Authorization, Content-Type`, exposes `X-Request-ID, Retry-After`,
  `Max-Age 600`, `Vary: Origin`, no credentials. A preflight is answered before any auth, limiter, audit or metric;
  a refused one is `204` without any `Access-Control-*`.
- **Config:** `ADMIN_WEB_ORIGINS` is a comma-separated list of at most five bare origins in
  `deploy/k8s/base/api/config.env` (public, reviewable, the hashed ConfigMap of D111), not Vault. Empty means no
  CORS; a malformed value stops the api at start-up (before it waits for any dependency), and a test parses the
  committed line so it cannot merge. `PUBLIC_WEB_URL` has its default in code.
- CORS is not the access control; the bearer is. There are no cookies, so no CSRF.

## D155 — A per-address limiter on `/v1/admin/*`, keyed by the address Traefik appends

*Amended by D222 (Cloud Run) and D240 (`httpx.ClientKeys`, `ratelimit.Capped`, shared with the edge limit).*

- **Decision:** 10 requests a second, bursts of 20, per api pod, before the bearer check (so a flood of bad
  credentials costs no token verification and no database call); over the limit `429` with `Retry-After`. The key
  is `httpx.ClientIP`: the rightmost `X-Forwarded-For` entry, IPv6 masked to /64, else the peer.
- **Why the rightmost entry is the client:** the NLB keeps the client address (`preserve_client_ip`), Traefik
  trusts no forwarded header (no `trustedIPs`) so it drops what a client sent and appends the peer it saw, and the
  api's NetworkPolicy admits only Traefik and the smoke pod. Never the leftmost entry or `X-Real-Ip`.
- **The address lives in memory only** (a map key, evicted after 10 idle minutes): never logged, never a metric
  label. The map has no cap, which is fine for IPv4 and needs one before IPv6 clients arrive.
- It is the documented exception to D36's per-user keys: before the bearer check there is no user.

## D156 — `mfa_required` is an operator-only `403` code

- **Decision:** `model.CodeMFARequired` is in the Errors `code` table of `docs/API.md` and in its Swift mirror
  (`APIErrorCode.mfaRequired`), which the doc test ties to the code. The app never calls `/v1/admin/*` and never
  receives it, so the closed `code` enum grows only on routes the app does not use and the app needs no change.
- **Rejected:** exempting operator codes from the doc test (more test machinery for no gain).

## D157 — The admin actor: who made an admin call

- **Decision:** `admin_audit.actor_user` (migration 0032) next to `key_fingerprint`; a key call stores its
  fingerprint, a token call `''` and the user id, and one CHECK (`admin_audit_one_actor`) pins exactly one actor.
  `GET /v1/admin/audit` gains `actor {kind: key | user, id}` (closed `kind`); `key_fingerprint` stays. Log lines
  carry `admin_actor` (`key:3fa1c2d9`, `user:1b2c3d4e`, the first 8 hex digits), `admin_key` stays on the key's
  lines. A real operator's `mfa_required` refusal is audited too (user actor, `403`, route pattern, no details),
  bounded to a burst of 3 and then one a minute per operator and pod.
- **`key_fingerprint` was not made nullable:** an image from before 0032 scans it into a Go `string`, and a NULL
  would break `GET /v1/admin/audit` after a rollback. The migration is additive; the old image's inserts satisfy
  the CHECK.

## D158 — Admin alerts without personal data

- **Decision:** `AdminUnauthorized` (warning: more than 20 requests with a bad bearer in 10 minutes) and
  `AdminWebUsed` (info: any token-authorized request in 10 minutes) replace a per-user digest. No metric or label
  holds a user id or an address; who acted is in `GET /v1/admin/audit`, not in Grafana Cloud. The api never sees a
  guessed password (that is Supabase Auth, read in Supabase → Logs → Auth), and `mfa_required` is deliberately in
  neither alert (audit rows and CloudWatch).
- **`AuthUnavailable` no longer matches `jwks_refresh_limited`** (a forged key id anyone can send); the failed admin
  allow-list check counts as `internal`, which it watches. The `ryft_admin_*` series are created at 0 at start-up,
  because `increase()` of a counter born at 1 would miss the first use of a pod's life.
- **The rules reach Grafana only through `make alerts-apply`** (the user's manual step), which therefore comes
  before the first `admin_accounts` row: `AdminWebUsed` is the lost-phone detection.

## D159 — Invites for a person: bound to a confirmed email, revoked not deleted, with who redeemed (admin web, PR2; the design's D-F)

- **Decision:** migration 0033 adds to `beta_invite_codes` `email` (lower-case, printable ASCII; a code for one email
  has `max_uses` 1 and an `expires_at`), `created_by` (`key`, `key:<fingerprint>` or `user:<uuid>`; `DEFAULT 'key'`)
  and `revoked_at`, and the table `beta_invite_uses (code, user_id, at)` with primary key `(code, user_id)`,
  backfilled from `users.beta_invite_code` (`at` = `beta_access_at`). Redeeming is one transaction under the
  account's row lock and one statement that spends the use and records it; **every** refusal (unknown, expired, used
  up, revoked, another email, an unconfirmed email, redeemed by this account before) is the same
  `422 "invalid or used code"`. A bound code is compared with the email **Supabase** holds for the account (admin API
  `GET /auth/v1/admin/users/{id}`), and only when `email_confirmed_at` is set; never the token's `email` claim, never
  `users.email` (a copy of that claim), never `user_metadata`. The comparison is byte for byte after lower-casing the
  ASCII letters: no trimming (an address with a space or a newline around it is refused as it is), no plus-address or
  dot folding, no Unicode case folding (a Kelvin sign would otherwise match an ASCII `k`), and an address outside
  printable ASCII never matches. Supabase is asked only for a code that exists, is open and is bound, outside any
  transaction; if it cannot answer, `503` with `Retry-After: 5` and no `code`, for that case only. An account that
  already has access gets `204` first and nothing is looked up. `DELETE /v1/admin/invites/{code}` **revokes**
  (idempotent `204`, `404` for an unknown code) and caps `expires_at` at the revoke time. A hard delete is not offered.
- **Bound codes need *Confirm email* ON (security-critical).** With *Confirm email* off, Supabase marks a new
  sign-up's address confirmed at once (`email_confirmed_at` set), so anyone holding a leaked link could sign up with
  the victim's address and redeem the code; the design's sentence that the check holds "whatever the project's
  Confirm email setting is" was wrong and is corrected. So: (a) the user records *Confirm email* **on** and *Secure
  email change* **on** before the first bound code (OPERATIONS → Beta, SUPABASE.md); (b) the api enforces it: it reads
  the project's public auth setting `mailer_autoconfirm` (`GET {auth}/settings`, with the admin key as the apikey) at
  start and every hour, and **while it is true, unknown (not read yet, unreadable, older than 3 hours) or the api has
  no Supabase admin client**, a bound code is neither created (`422` with the reason) nor redeemed (`422`, the same
  single refusal, when Confirm email is off or there is no admin client; `503` + `Retry-After: 5` when the setting is
  unknown). A failed read is unknown at once; the last good answer is not kept. Metric `ryft_invite_binding_available`
  and log line `invite binding` (state `ok`, `autoconfirm`, `unknown`, `no_admin_client`). Unbound codes never
  depend on it. The field name `mailer_autoconfirm` is GoTrue's; it is read from the live project by the first
  `invite binding` line after the deploy (state `ok` on a project with Confirm email on).
- **Why:** a code in a chat can be forwarded; a binding to an address the tester has proven makes a forwarded code
  useless to anyone else, without a new service. Every refusal is one status and one body, so a guesser cannot tell
  *why* a code fails or tell an unknown code from a revoked, used or bound one. **There is no oracle before a correct
  guess of a live bound code**: the Supabase lookup (50-150 ms, and a possible `503`) happens only for a code that
  exists, is open and is bound, so a timing or status difference appears only after such a guess. The guess budget is
  31^8 = 8.5e11 codes (about 39.6 bits), 5 attempts an hour per account and pod, and fewer than about 100 open codes:
  about 1e-10 per guess, and every guessing account costs a confirmed email behind Supabase's sign-up limits. Revoke
  keeps the row so that `uses_by` and the audit still say what happened.
- **A bound code ends:** `expires_at` is required for a code with an `email` (the database CHECK
  `beta_invite_codes_bound_expires`): 14 days when the caller names none, at most 90 days away (`422` beyond).
  Otherwise the address of a non-user would be kept for ever by a code nobody redeems (the purge, D161, only clears
  codes that can no longer be redeemed).
- **Rollback stays safe, with one step before it:** `created_by` has a default, so an older image's `INSERT` works; a
  revoke sets `expires_at`, so an older image refuses a revoked code too; `ON DELETE CASCADE` on `code` keeps an older
  image's hard `DELETE` working. **Deviation from the design text:** `user_id` cascades too. The design relied on
  `DeleteUser` removing the rows first, but an older image's `DeleteUser` does not know the table, and the backfill
  gives every account that redeemed a code a row, so after a rollback their erasure (`DELETE /v1/me`) would fail on
  the foreign key. The current `DeleteUser` still deletes the rows explicitly. **But an older image's redeem
  statement ignores `email`**, so every open bound code would turn into a code anyone can use. **Before re-pinning an
  older image run** `UPDATE beta_invite_codes SET revoked_at = now(), expires_at = LEAST(COALESCE(expires_at, now()),
  now()) WHERE email IS NOT NULL AND revoked_at IS NULL;` (OPERATIONS → Beta → Rollback of 0033) and make new codes
  after the roll forward. The same holds during an ordinary rolling deploy: while pods of the older image still serve,
  they spend a bound code for any account. **Create the first bound code only after the rollout of the image with
  this migration is complete** (all api pods on it); a bound code made earlier is revoked with the SQL above.
- **Erasure:** `DeleteUser` removes the account's `beta_invite_uses` rows and, for a bound code it redeemed, clears
  the code's `email` and `note` (the code identifies the person). `MergeUsers` moves the rows; the merged-away
  account's deletion then clears as above. `beta_invite_uses` appears in no `/v1/me` read and is not in an export:
  it holds a code, an account id and a time, nothing the account entered; the rows are deleted with the account. If
  an export is added it gets this table (a code and a time).
- **Known and accepted:** an account whose access the operator took away cannot redeem the same code again (it
  already used it); the operator grants access by hand or makes a new code. `uses_by` lists accounts that still
  exist, `uses` stays the count. A Sign in with Apple relay address matches only a code made for exactly that
  address; the page says so and recommends an unbound code when unsure.
- **Needs the user (U3):** a bound email is data about someone who may never become a user (and the `note` may name
  them). The privacy text needs a sentence and a new `PRIVACY_POLICY_VERSION` (a separate PR, the user's decision on
  timing): the new category (an email address, possibly a name in a note, of a person who may never become a user;
  Art. 14 information if someone else asked for the invite), the purpose (sending that person an invitation, and
  redeeming it only for that address), the legal basis, the retention (until redeemed, at most 90 days to
  expiry, then 30 days; address and note), and what is compared at redemption (the address Supabase holds for the
  account, confirmed). **Bound codes are not to be created in production before it is published.** The api does not
  enforce that: a flag would be one more moving part for a single operator, and the operator decides who gets a bound
  code.
- Design: `docs/work/2026-11-05-admin-web/design.md` §6.1–§6.3; contract `docs/API.md` → Invites for a person.
  The design's D-G (the page) and D-H (sending the invite by email) take the numbers of the PRs that implement them.

## D160 — Emails in the invite answers: masked unless `?emails=1`, whichever bearer asks

- **Decision:** `GET /v1/admin/invites` shows the bound `email` and every `uses_by[].email` masked
  (`a***@e***.com`, the `maskEmail` of `/v1/admin/users`) unless the request says `?emails=1` (exactly `1`), for the
  admin key **and** for an operator's token. `uses_by` keeps `user_id`, `short_id`, `private_relay` and `at` either
  way. The call is audited with `emails`. Unlike users and feedback there is no second view for the token: the
  invite answer holds no health-derived, device or diagnostic field, so the only sensitive field is the address, and
  the one switch covers it (the token's projection rule: nothing a token does not need, no address unless asked for).
  The create answer shows the address in its **stored form** (trimmed, ASCII lower-cased), not masked, because the
  caller just sent it; nothing else about an address is returned by `POST`. `note` is free text the operator wrote and is returned as written; it may name the person
  (D161).
- **Why:** the token path is the browser, and an address that is not on the screen cannot be shoulder-surfed or
  left in a screenshot; the page asks for `?emails=1` because the operator needs the address to match a tester. A
  test pins that the default answer of both bearers has no `@` outside a mask.
- **Audit and logs:** the audit `details` of a create or revoke hold the `code` (the database only, for the
  operator), never an address; the log lines say `bound: true|false` and carry no code, note or address. The access
  line of `DELETE /v1/admin/invites/{code}` logs the pattern `/v1/admin/invites/{code}` as its `path` (the code is
  the last path segment; `RequestInfo.LogPath`), and the Supabase client's errors carry no URL, which holds the
  user id.

## D161 — A bound email and its note are kept 30 days after the code stopped being redeemable

- **Decision:** an hourly, idempotent job in each api pod (`PurgeInviteEmails`) sets `email` to NULL and `note` to ''
  on every bound code that has not been redeemable for 30 days (revoked, expired, or used up; a used-up code whose
  account is gone uses the creation time), and sets `revoked_at` on such a code that is not used up, so it stays dead
  even if someone edits `expires_at` in SQL later (a used-up code keeps its status; an expired one then reads
  `revoked`). Clearing is safe: a code that cannot be redeemed cannot be widened by losing its binding. An open code
  keeps its address; a bound code always expires (D159), so none is kept for ever. The note of an **unbound** code
  is the operator's label for a group and is not touched; there is no hard delete of a code, so a note that names a
  person on an unbound code stays until the operator edits it in SQL (`UPDATE beta_invite_codes SET note = ''
  WHERE code = '…'`) - do not put names in the note of an unbound code.
- **Amended by D249 (2026-11-20):** the note of an unbound code is cleared too, 30 days after that code stopped being
  redeemable; an open unbound code keeps its note.
- **Observable:** `ryft_invite_email_purge_total{result}` (`ok`, `error`; created at 0) and the warning
  `InviteEmailPurgeFailing` (three failed passes in three hours); the log line `invite emails purged` when a pass
  cleared something.
- **Why:** the privacy text (once U3 is decided) can state a retention that is enforced, instead of "until the
  operator remembers", and a failing job is seen, not silent.

## D162 — The /admin page on ryftapp.de and its CSP exception (admin web, PR3)

- **Decision:** the operator's page is `https://ryftapp.de/admin`, part of the static site (`web/`, D107), not
  a service of its own: one esbuild-bundled inline script in its own document, Supabase Auth by REST with TOTP,
  then only the `/v1/admin/*` routes of D153 with the operator's token. It is the one exception to the site's
  "no script beyond the theme, `connect-src 'none'`, no forms, `localStorage` for the theme only": its own nginx
  location and CSP (`connect-src` path-scoped to `<supabase>/auth/v1/` and `https://api.ryftapp.de/v1/admin/`,
  `trusted-types 'none'`), the session in `sessionStorage`, cleared when the tab leaves `/admin` (a reload signs
  out), English-only strings. Every other page keeps its policy unchanged. The details and the reasons are web
  D15 (document, script, CSP), web D16 (storage) and web D17 (forms, origins, language) in
  `web/docs/DECISIONS.md`; the PR is timkrebs/ryft-platform#128.
- **Why on the site and not a separate origin:** no new image, Deployment or certificate for a page used by one
  person; the marketing pages run no dynamic code, which keeps the shared origin acceptable. A subdomain is the
  v2 hardening if the site ever gets more dynamic pages.
- **Preconditions to use it:** D154's `ADMIN_WEB_ORIGINS=https://ryftapp.de` live on the api, the invite fields
  of D159-D160 live, the Data API off (`docs/SUPABASE.md` step 4), at most one TOTP factor per account
  (`max_enrolled_factors = 1`), and a check that a second factor cannot be enrolled at aal1 (the design's V-MFA-1).

<!-- D163-D183 were written at the close-out of the 2026-11 production hardening run (docs/work/2026-11-04-production-hardening/),
from the "Decision text" sections and descriptions of the pull requests named in each entry. The decision was taken in
the cited PR; the number was given here. D162 is reserved for the /admin page (#128, open). -->

## D163 — Import rules are a test over `go list`; one owner per rule (R1–R13) (hardening Phase 1; #99, #105)

- **Decision:** `backend/internal/arch_test.go` (stdlib only, `go list`) checks the non-test import graph, and a
  failure names the rule, the offending edge and why the rule exists. R1: `model` imports nothing internal. R2: `store`
  imports no `scoring/...`, `notify`, `httpx`, `auth`, `gate`, `eventbus`, `amqpbus`, `apns`, `apple`, with
  `scoring/workoutdup` the one exception (D146). R3: `cmd/*` importing each other is not encoded (the compiler refuses
  it). R4: the pure rule packages do no I/O and import no internal package but `model` and `train`. R5: only
  `eventbus` and `cmd/rabbitmq-topology` import `amqpbus`; only `amqpbus` imports amqp091. R6: `events` and `outbox`
  know no transport, store or db. R7: `pgxpool` only in `db`, `store`, `outbox` and (since #105) `internal/app`, `pgx`
  only there plus `testsupport` and the named entries `cmd/api` and `cmd/ingest` (`pgx.Tx`, `pgconn`), each with a
  reason. R8: `auth`, `httpx`, `gate` hold no storage. R9: the eight leaf packages import nothing internal. R10:
  `sealed` is imported by `cmd/api` only. R11 (`testsupport` is tests-only) is a depguard rule next to
  `logcheck-tests-only` (D114). R12: the documentation shape (D164). R13 (#105): only the four service commands import
  `internal/app`.
- **To allow an edge, add an entry with a reason; do not loosen a pattern.** A rule whose pattern matches no package
  of the module and nothing it imports fails (a rename, a pgx v6 or amqp091-go v2 bump cannot leave a vacuous rule); a
  self-test feeds the checkers a graph that breaks each rule.
- **`scripts/pkgmap.sh`** generates the package table (package, owns, depends on, tested by) between markers in
  `docs/ARCHITECTURE.md`; `--check` exits 1 with a diff when it is stale and 2 when it cannot work. "Depends on" lists
  **direct** imports (`go list -deps` would change on every edit). `make lint` runs golangci-lint, then the check, and
  warns instead of failing on a golangci-lint other than the CI pin; the CI check is a step of the `lint` job, and a
  `pkgmap` path filter selects only that job for a doc-only edit.
- **Rejected:** a new linter (go-arch-lint) or one depguard rule per edge: a new dependency, or one YAML list per edge
  without a reason column.
- **Not proven by it:** that the edge is a good one. Every rule held against the graph before it was encoded (#99).

## D164 — Package docs live in `doc.go`; its first sentence is the package map (hardening Phase 1; #94, #95, #96, #99)

- **Decision:** every package with Go files has exactly one `doc.go` holding the only package comment and no
  declaration. Its first paragraph is one sentence ending in a period, at most 160 characters; `scripts/pkgmap.sh`
  puts it into the "owns" cell (D163, R12). revive `exported` and `package-comments` are on in `.golangci.yml` with
  the stutter check **on**; one rule of `exclusions.rules` (a text match on revive's stutter message) exempts exactly
  `model.ModelInfo` and `model.ModelChangeEntry`, which keep the names API.md and the Swift mirror use. Nothing else may
  stutter. No `//nolint:revive`, `//nolint:all` or bare `//nolint` anywhere (the arch test reads the comments; nolintlint
  `require-specific` is on too); other linters have named `//nolint` comments.
- **Why:** the old package comments were stale in places the review found by checking each claim against the code
  (`scoring` said "pure functions" while only the formulas are; `workoutdup` said ingest uses it while only `store`
  and `scoring` import it, D146; `store` claimed RLS as a second line for the service roles, D42). Comments only: the
  diff of #94 stripped of comments, blank lines and package clauses is one trailing comment.
- **Cost:** 43, 39 and 23 exported identifiers got a why-comment in #96, #94 and #95.

## D165 — Every decision is findable from the code it governs; the check warns (hardening Phase 1; #102)

- **Decision:** `scripts/decision_refs.sh` lists the `D<n>` of this file that no tracked source or config file
  mentions. Markdown, `docs/`, test data, lock files, generated outputs, the vendored operator releases and `web/` (the
  site numbers its own decisions in `web/docs/DECISIONS.md`) do not count; a range counts for each member. It is
  **warning-only** (exit 0; `--strict` exits 1 for a later promotion; exit 2 only when the script itself cannot work, so
  a broken check never reads as "all linked"). `scripts/decision_refs.skip` holds the decisions without a code
  consequence, one reason each; a skipped decision that later gets mentioned is reported as stale.
- **Where it runs:** `make lint`, and in CI as a step of the unfiltered `bytes` job (not the path-filtered `lint` job),
  so a PR that only edits this file or the skip list runs it and a renumbered `D` shows up on the PR that renumbered it.
  One `::warning` summary annotation, not one per decision.
- **Result at #102:** 40 unreferenced before, 0 after (33 linked or already cited, 7 skipped with a reason); 34
  decisions got a one-line link. Comments that cited the wrong decision were corrected. A first scan without the `web/`
  exclusion said 37: D1, D2 and D4 only looked linked because the site's decisions and test labels matched.
- **Consequence:** a new entry in this file is reported until code or config cites it or the skip list gives a
  reason. D163–D183 were added without a code change (documentation only) and are reported by the script until then.

## D166 — The load baseline is local, recorded, and gated on the error rate only (hardening Phase 0; #97)

- **Decision:** `scripts/load.sh` / `make load` runs k6 (`grafana/k6:2.3.0`, pinned by tag and index digest) in a
  container on the compose network against the local stack: 50 synthetic users, each syncs one full day (about 1 250
  records, seeded) and reads `GET /v1/days` over 30 days 20 times; one warm-up run, 3 measured. It refuses any target
  that is not localhost unless `LOAD_ALLOW_REMOTE=1`, and never `*.ryftapp.de` or `*.supabase.co`. It uses
  `AUTH_MODE=apikey`'s `<API_KEY>:<uuid>` bearer, so no auth code changed. It exits 1 above 1 % errors.
- **The numbers are not a capacity statement.** `docs/OBSERVABILITY.md` → Baselines records the median run and says why
  a laptop is not production; the runs are bimodal (sync p95 1.3 to 4.6 s over three runs of one invocation, and 83 and
  73 ms on a quieter machine), so **only the error rate is a gate**; latency is reported until a quieter runner exists.
  The nightly job is not built (`verdict()` is where a later `--compare` goes).
- **Rate limits were checked:** the test stays under them (0 client `429`, 0 `ryft_rate_limited_total`), no override.

## D167 — The contract is tested against the code: schema snapshots, API.md examples, error tables (hardening Phase 0 and 1; #87, #100)

- **Decision:** schema snapshots of the `model` types (104 in #87, 108 on main now; reflection, `go test ./internal/model -run TestSchemaSnapshots
  -update`); `TestAPIDocExamples` decodes the json blocks of `docs/API.md` (45 blocks at #87: 39 tied by
  `<!-- type: model.X -->`, 23 of them decoded strictly and 16 field-checked, 6 allow-listed with reasons; a failure
  names the API.md line). #100 adds tests that tie the Errors tables of API.md (status, `code`, the **Do** column) to
  the `model.Code*` constants, the Swift `APIErrorCode` cases and the `WriteError`/`WriteCodedError`/`gate.Apply` calls,
  and every message API.md quotes verbatim to what the code answers; `make check-api-doc` runs them uncached (the test
  cache does not see edits of `docs/API.md`).
- **Known drift is recorded, not hidden:** `docDrift` in the test lists what API.md documents and the Go types lack
  (`User.sources[].samples_7d`, `DailyScore.coverage` / `ScoreCoverage`), as TODOs for the api-contract-owner. A symlink
  to API.md is refused by `check_bytes.sh`, so there is no `testdata/API.md`. The Do wording itself is only checked for
  being present.

## D168 — Services start and stop through `internal/app` (hardening Phase 2, A1; #105)

- **Decision:** `app.Run(ctx, Spec)` / `app.Main(Spec)` owns a long-running service's lifecycle: the settings
  (`Spec.Load`, before any connection), the pool and its watch, the broker, `/metrics`, what the service builds on them
  (`Build` returns routes, checks, consumers and timers), the outbox relay, the timers, the consumers and the HTTP
  server last, so the start-up probe still covers the database wait. `/metrics` stays after the broker as today
  (moving it first is A5's behaviour change). `app` may import `pgxpool` (R7, no other driver package) and `eventbus`;
  only the four service commands import it (R13, D163). Subcommands and `migrate` stay outside.
- **A signal while the start waits** (database, broker, `Build`) is a clean stop (reason `signal`, exit 0); a start
  failure without a signal stays exit 1. A `Build` that ignores its context ends at the kubelet's SIGKILL.
  `ConfigError{Msg, Err}` keeps today's messages. The contexts `Build` and every task get end with the shutdown's own
  steps, not at the signal, **except that until A4 a consumer's context still ends when its own step begins**.
- **Rejected:** static `Spec` fields for routes and consumers (they need the opened pool); the mains opening the
  dependencies and handing them in (each main stays over 40 lines).
- **Status:** scoring and notify run through it since #117 (D174). api and ingest (A3) do not yet.

## D169 — Shutdown order and a budget that a test pins (hardening Phase 2, A1; #105)

- **Decision:** on the first signal or a failing part: `/readyz` reports `draining`; timers stop; an optional drain
  delay (`Spec.DrainDelay`, default none; A7 would make it 5 s for api and ingest); consumers (`Deps.ConsumerDrain`,
  15 s, the step waits it plus 1 s); HTTP `Shutdown` (10 s for an Edge service, 1 s for health-only), then `Close`
  and carry on; timers waited; relay and db watch; broker; pool; `/metrics`. Every step is bounded, all bounds live in
  one struct, and a step's bound includes the waits after it. One `shutdown step` line per step with `dur_ms`.
- **The watchdog** logs `shutdown overdue` (`step`, `elapsed_ms`) and exits at `Spec.Grace` (default 30 s) minus 4 s:
  exit 1, or 2 if a panic was recorded. **Worst case** of the bounds: api and ingest 18 s (23 s with A7), scoring and
  notify 25 s, against 26 s; `TestShutdownStaysInBudget` fails unless each is strictly below the watchdog's time. The
  design's earlier numbers (27, 32 and 29 s) did not count the waits after `Close` or the metrics stop.
- **A request still running after its HTTP budget is cut.** That is safe because syncs, imports and deletions are
  idempotent or transactional (D39, D126, D131, D144, D145), not because it is waited for.
- **Rejected:** a grace period long enough for D129's 50 s (about 75 s on every rollout, drain and scale-down, to save
  one retry); a `preStop` sleep (an exec hook in four manifests and the admission policy, where a constant in the
  process does the same).

## D170 — `/readyz` names its dependencies (hardening Phase 2, A1; #105)

- **Decision:** the body gains `checks` (one string per dependency named `db`, `auth`, `broker`: `"ok"`, `"stale 3m"` or
  `"down: <short reason>"`; a stale check counts as ok) and `status` (`ready`, `unready`, `draining`); `error` keeps its
  meaning. Status codes are exactly today's, 503 while draining, `/healthz` stays 200 until the listener closes. A check is
  required or reported: the broker is **reported, not required**, in api and ingest (D101). Checks that are not required
  wait at most 500 ms. A panic in a check is counted and logged (stack at most once a minute per check) and the service
  keeps running. Probes, the PostSync smoke and the scripts read the status code only.
- **Rejected:** a separate `/livez` or `/drainz` (a second endpoint for the same state); an object per check (no reader
  needs more than the reason).
- **Where it is live:** scoring and notify since #117 (they serve health only). api and ingest keep today's body until A3.

## D171 — `errgroup` and `goleak` (hardening Phase 2, A1; #105)

- **Decision:** `golang.org/x/sync` becomes a direct requirement for `errgroup` (one group per shutdown tier); it is
  already compiled into every binary through pgxpool's puddle. `go.uber.org/goleak` becomes a test-only requirement
  (depguard `goleak-tests-only` enforces it); it was already in `go.sum` through amqp091-go's and client_golang's tests.
  No new module in the graph.
- **Rejected:** a home-made WaitGroup supervisor (an idiom reviewers know beats 20 lines they must read); goroutine
  counts through `runtime.NumGoroutine` (flaky).

## D172 — Shutdown is measured in the log, not in a metric (hardening Phase 2, A1; #105)

- **Decision:** the `shutdown step`, `shutdown overdue` and `shut down` lines (CloudWatch, D52; the last carries `exit`)
  are the record. A histogram observed by a pod that exits seconds later is almost never scraped, and the exit code of
  a rolled pod is not a Grafana signal either: kube-state-metrics drops deleted pods. The queries are in OPERATIONS →
  "A deploy: what the old pod logs".
- **Rejected:** `ryft_shutdown_seconds` (from the brief).

## D173 — A recovered task panic exits 2; the log line and the restart are the signal (hardening Phase 2, A1; #105)

- **Decision:** a panic in a service's own goroutine (a `Task`) is recovered so the shutdown runs in order, logged as
  `panic` with `task`, `panic` and `stack` (the documented exception in OPERATIONS → Data protection (logs)), counted in
  `ryft_goroutine_panics_total` (no label), and the process exits 2, as an unrecovered panic does; the watchdog exits 2
  too after a recorded panic. The counter is best effort (the process is about to exit), so an alert rests on the log
  line and on a restart (`ServiceExited`, D174). A Job or Consumer must log and swallow a transient error and return
  only a fatal one, because a returned error stops the whole service.
- **Rejected:** exit 1 for a panic (it would hide a bug among ordinary failures).

## D174 — scoring and notify start through `app.Run`: four behaviour changes accepted, two alerts (hardening Phase 2, A2; #117)

- **Decision:** `cmd/scoring` and `cmd/notify` hand `app.Main` a `Spec` (`Name`, `BrokerRequired`, `Outbox` for scoring,
  `Load`, `Build`) and keep no lifecycle code (`main()` 132 to 24 lines and 81 to 16); the engine, handler, timer and
  consumer live in `service.go`. Subcommands stay outside. Four behaviour changes, titled in the PR and accepted:
  (1) an HTTP port that cannot be bound ends the service with exit 1 (it was logged, and the service ran without probes
  until the start-up probe killed the pod); (2) one ordered signal handler replaces two, and the event in hand is **not**
  yet protected from the signal (A4), so `handle event failed, will retry` and `amqp: could not retry, rejecting for
  redelivery` on a deploy are still expected; (3) the shutdown lines and `ryft_goroutine_panics_total` exist here, and
  a recovered panic exits 2; (4) `/readyz` answers 503 `draining` from the start of the shutdown.
- **Loops check the context:** scoring's `refreshActive` logged one `periodic: user` error per remaining user after a
  SIGTERM; it now stops between two users (`eachUser`) and skips the queue pruning and the gauges once the context has
  ended. notify's `CheckStale` does the same and returns the context's error.
- **Alerts (the gate):** `Panics` now covers `ryft_http_panics_total`, `ryft_event_handler_panics_total{subject}` and
  `ryft_goroutine_panics_total` joined with `or` (a `+` empties the sum when one series is absent); the two labelled
  counters get TextRefusedByDatabase's `unless last_over_time(... offset)` term for their first panic. New
  `ServiceExited` (critical): any restart of a container in `ryft-(api|ingest|scoring|notify)` over 15 minutes. It does
  not look at the exit code: the services exit 0 on SIGTERM, a probe kill can end with exit 0, and the exit-code series
  may not be in Fleet's allow-list. Known holes until the counters are created at 0 (Phase 3): a first panic during a
  collector outage of about 75 minutes, and a second within about 75 minutes of a pod restart.
- **Rejected:** a label on the goroutine counter (the log line names the task); an exit-code condition on `ServiceExited`.
- **Rules reach Grafana only through `make alerts-apply`** (the user's step). See D175 for the order and for what
  happened on 2026-11-05.

## D175 — A change to shutdown or alerts is staged by holding its image group, not by leaving one promote PR open (hardening Phase 2, A2; #117; amended by #124, #130, #131)

- **The order #117 asked for:** merge the PR alone and **do not** merge the promote PR; load the alert rules
  (`make alerts-apply`) and check them; read the restarts (`kubectl --context ryft-prod-euc1 get pods -A`); merge the
  promote PR; watch 30 minutes after Healthy (restarts, `ryft_amqp_connected`, `ryft_db_up`, queue depth under 100,
  `increase(ryft_goroutine_panics_total[30m]) == 0`, sync-to-score p95 under 120 s, PostSync smoke); look at the
  shutdown lines in CloudWatch; soak at least 1 hour before A3. Rollback: `./scripts/pin_images.sh 10bc4d0 services` and
  `./scripts/hold.sh add services "A2 rollback, see #117"` in one commit (10bc4d0 is after the D144/D145 one-way door).
- **What happened (timestamps from GitHub):** the promote PR #124 `deploy: images ab87021` (services `10bc4d0` to
  `ab87021`, whose only backend commit was A2, #117) had its auto-merge switched off at 06:53:06 UTC by the owner, which is
  why it sat open and unmerged. At 10:19:28 UTC on 2026-11-05 promote closed it
  with the comment "Superseded by deploy: images dc1fcd4" and, 4 seconds later, opened #130, which pinned the same A2 commit
  and nothing else from `backend/`; #130 merged itself at 10:22:25 UTC when its checks passed. That is what the
  deploy flow does (OPERATIONS → Deploy and roll back: promote "closes every older open `deploy/*` PR as superseded", and
  the newest merges itself): **one unmerged promote PR is not a hold.** At 10:47 UTC #131 recorded that `make alerts-apply`
  had been refused by Grafana Cloud's 20-rule group limit (D179), so `ServiceExited` and the extended `Panics` were **not
  live** while the A2 images were already pinned. Whether the rules were applied afterwards, and what the 30-minute watch
  showed, is not recorded in any PR and is **not verified** here.
- **Rule (decided in the close-out PR #137, after that supersede; it replaces #117's step "do not merge the promote PR".
  #124, #130 and #131 are the evidence, not the decision: #124 and #130 are promote PRs opened by the bot, and #131 split
  the alert group):** to stage a backend change whose promote must wait, hold the
  group **before** the merge that would build it: `./scripts/hold.sh add services "<reason>"` in a PR that reaches main
  first. While the hold stands CI builds nothing for the group and no promote PR opens; the change merges; the user does
  the steps that must come first; then the hold is released (`hold.sh rm`, two merges, OPERATIONS → Release a hold). A
  newer promote PR supersedes an older one and merges itself; the staging must not depend on someone not merging it.
  Switching auto-merge off on one promote PR (what was done to #124) is not a hold; OPERATIONS → "Staging a backend
  change" says so in that case.
- **One group at a time:** the five service images roll together (D107). Stage A3 (api and ingest through `app.Run`)
  and A4 (the in-hand delivery) as separate rollouts with the soak between them.
- **Consequences:** the checks above are a precondition of the pin moving, not a watch after it, for any change that
  adds an alert the change needs. The alert-rule load can fail for reasons unrelated to the change (D179).

## D176 — A pure move is exempt from the ~600-line limit when a tool proves it (hardening Phase 2, B1; #110)

- **Decision:** a PR that only moves declarations between files of one package is exempt from the ~600-line size rule
  (the design's J8) when `scripts/check_pure_move.sh <base> <package>` exits 0 and its output is in the PR. The tool
  (`scripts/pure_move.go`, tested by `scripts/tests/check_pure_move_test.sh` in `make test-scripts`) proves that every
  top-level declaration has the same exact source text including its doc comment, the comments attached to each
  declaration and before the package clause, the imports and what each selector resolves to, the declarations in four
  build contexts (linux/amd64, darwin/arm64, windows/amd64, linux/amd64 with cgo), the order of initialisers that run
  code, the multiset of lines outside package clauses and imports, the same for the `_test.go` files as a second set,
  the non-Go files of the directory, `go test -list .` and `go doc -all -u` (const and var entries sorted, because a
  split necessarily changes their source order). Exit 1 is not a pure move, 2 is cannot tell.
- **What it does not prove:** that the code type-checks (the compiler and the tests do), the meaning of a name through a
  package whose name differs from the last element of its path (the import set still covers the path), and whether a
  reordering of initialisers matters (it flags one). It does not replace review.
- **First use:** `model/model.go` (1 453 lines) became 14 files of 13 to 250 lines (`sync`, `train`, `plans`, `admin`,
  `sleep`, `journal`, `days`, `account`, `push`, `events`, `trends`, `beta`, `config`, `common`); a const or var block that
  mixes two domains moved whole with its first use. Rejected: fourteen 300-line move PRs. Follow-ups a pure move cannot
  do are listed in the PR (grouping of `ModelInfo`, `Base*`, `HRSource*`, `InvitePost`).

## D177 — A refactor of numbers or stored state is two PRs: characterisation tests on the old code, then the steps (hardening Phase 2, C1, C2, C4; #111, #112, #119, #120, #122, #123)

- **Decision:** when a function split and its tests together exceed the size rule, the tests come first as their own PR,
  recorded on the **unsplit** code, and the refactor is stacked on it with the recorded files unchanged
  (`git diff` of `testdata` empty). The proof is: the characterisation and the golden fixtures (D149) pass unchanged,
  and a table of hand-made mutants each killed by a named test. A surviving mutant is stated.
  - **C1 `computeDay`** (#111, #112): 13 cases pinned on what the engine stores; `computeDay` 278 lines to a 28-line
    runbook over named steps in `compute_steps.go` (I/O in `Engine` methods, the rest pure), store reads and writes in the
    same order (the strength pass still follows the duplicate pass, D147/D60). Golden unchanged three times;
    `ModelVersion` stays 2026.10.3; no rescore. The brief's `pickSources` step was **dropped**: source picking happens
    inside the steps that read the data. **`scoring rescore -dry-run` does not exist** (`rescore` takes `-days` only), so
    the golden comparison of every stored column stands in. Observed, unchanged and pre-existing: `mean` and `last` read a
    store error as a missing value in `loadNight` (HRV, respiratory rate, SpO2, resting HR); changing that is its own PR.
  - **C4 `trainImport`** (#119, #120): 15 golden cases plus a lease test (status, headers, raw body, stored state,
    outbox rows, metrics, log lines), `trainImport` 101 to 45 lines and `finishImport` 69 to 21; the goldens of #119 pass
    unchanged. `sync.Once` was restored after the first version used `sync.OnceFunc` (it re-panics).
  - **C2 `ApplySync`** (#122, #123): tests plus a statement trace of 16 scenarios (`testdata/applysync_trace.golden`,
    `RYFT_UPDATE_TRACE=1` regenerates); `ApplySync` 136 to 14 lines, longest step 27. The trace and the lock-first
    checks pass against the golden recorded on the old code. One mutant survived and is equivalent (duplicate ids not
    skipped in `dedupeDeletions`: `= ANY($2)` deletes the row once).
- **Why:** golden fixtures catch a moved number, not a moved statement order or a moved lock; the characterisation catches
  those.
- **Remaining long functions** are not decided here (L1, see STATUS).

## D178 — The clock-change days are characterised as they are, not fixed (hardening Phase 2, C2; #122)

- **Finding (pre-existing, found while characterising `ApplySync`):** the next day and the day before of a row are
  computed as the start plus or minus 24 absolute hours, in `ApplySync`'s deletions (`store/apply.go`), `planFold`
  (`store/fold.go`) and the ingest handler's `touchedDays` (`cmd/ingest/main.go`). On a day with a clock change that is
  not the neighbouring local day, so a neighbouring day is not queued. Four shapes in Europe/Berlin 2026: autumn next day
  (00:30 CEST 10-25, +24 h = 23:30 CET 10-25, so 10-26 is not queued); autumn day before (23:30 CET 10-25, -24 h = 00:30
  CEST 10-25, so 10-24); spring next day (23:30 CET 03-28, +24 h = 00:30 CEST 03-30, so 03-29 is skipped); spring day
  before (00:30 CEST 03-30, -24 h = 23:30 CET 03-28, so 03-29). The day before applies to a workout with a session; the
  autumn next-day shape is also pinned for a night HRV sample. Effect: a score is not recalculated after a deletion or an
  upload on the neighbouring day until something else queues it.
- **Decision:** #122 and #123 are refactors and pin the behaviour **as it is** in `TestApplySyncDeletedRowsDays`; the pins
  do not endorse it. The code at `origin/main` cd7970c still has `Add(24 * time.Hour)` in the three places (checked; the same at d924dd7).
- **Follow-up, not built:** one helper that computes the neighbouring local date from the date (`AddDate(0, 0, ±1)` on
  the local date), used in `touchedDays`, `deleteSamples`/`deleteWorkouts` and `planFold` together, with its own `D`
  entry; the pinned autumn case then flips to two days and the others to the full set. The `touchedDays` table of C3 pins
  the same four shapes so the three places cannot drift apart.

## D179 — A Grafana alert rule group holds at most 20 rules; a script guards it (#131)

- **Why (2026-11-05):** `make alerts-apply` was refused by Grafana Cloud's ruler: `per-user rules per rule group limit
  (limit: 20 actual: 39)` for group `ryft`. mimirtool rejected the load, so nothing was written and the live group
  stayed the older, smaller set (D175).
- **Decision:** the 39-rule group `ryft` became three groups along the file's own section comments: `ryft` (11 rules),
  `ryft-pipeline` (14), `ryft-ops` (14 in #131; 17 on main now, with the admin and invite alerts of #126 and #127). With
  `ryft-admission` (13) and `ryft-admission-intended` (12, in its own file `admission-intended.yaml`), both unchanged,
  `ryft.yaml` holds four groups; counts from `scripts/check_rule_groups.sh` on main: `ryft-ops` has room for one more
  under the 18 guidance. Only
  `- name:` / `rules:` header lines were inserted; no rule moved or changed, and the first group keeps the name `ryft`,
  so the load replaces the live group with a disjoint subset and adds two. `scripts/check_rule_groups.sh` counts `alert:`
  and `record:` items per group (block and flow style, comments ignored) and fails above 20 (`RULE_GROUP_LIMIT`) and on a
  group named twice in a file; `make alerts-check` runs it on `ryft.yaml` and `admission-intended.yaml` before promtool,
  so the CI `monitoring` job fails too. `docs/OBSERVABILITY.md` → Alerts: keep a group at 18 or fewer.
- **Consequences:** a renamed or dropped group stays live in Grafana until it is deleted there. Nothing is deployed to the
  cluster by the change; the rules reach Grafana only through the user's `make alerts-apply`.

## D180 — Argo CD's ClusterRoles are an allowlist, and everything else that decides what it may do is guarded (T12-f; amends D120; #47, merged 2026-11-05, **not applied**)

- **Decision:** the application controller's and argocd-server's ClusterRoles in `infra/aws/kubernetes/argocd.tf` are
  explicit rules derived from every synced render (25 controller rules, 13 server rules at merge; server read-only
  cluster-wide), not the chart's `*`. No `escalate` on any ClusterRole, `bind` on one name only
  (`messaging-topology-metrics-auth-role`), `escalate`/`bind` on the four operator Roles by name, nothing on VAP/VAPB,
  `secrets`, `pods/exec`, `impersonate` or `deletecollection`; `resource.respectRBAC: strict`, `exec.enabled: false`,
  the ApplicationSet controller at 0 replicas.
- **Amends D120** (the rules-only allowlist is not the whole surface): `extraObjects`, `controller.roleRules`,
  `createClusterRoles`, Argo CD's own RBAC and login settings, any `{{`, non-ASCII or control character in a value (the
  chart renders values with `tpl`, and a line break, U+2028, U+2029 or U+0085 starts another YAML document), a `set` or a
  second values document, and role bindings written in Terraform all decide what Argo CD may do. They are guarded by
  `modules/argocd-values-guard` as a **precondition** of `helm_release.argocd` (an error of plan and apply; a `check` block
  only warns); the release takes the guard's output as its only values, and a postcondition requires that (a plan-time check
  only while its inputs are known: no computed value in the release's values or `set*`). The kind run adds `argo-bindings`,
  `argo-extra-objects`, `argo-inventory` (the whole render counted by kind and namespace) and `argo-tf-bindings` (a
  tripwire, not a guard). The key allowlist is two levels deep plus a few listed maps; the dangerous variants are canned
  test cases, so a variant nobody wrote down is not tested for, and the guard edited together with its tests is caught only
  by review (`.github/CODEOWNERS`).
- **Update/patch/delete on the four RBAC kinds stays without `resourceNames`** (a fresh cluster needs `create`): limiting
  them narrows but does not close the route of a written ClusterRole bound to `system:serviceaccounts` or
  `system:authenticated` (or an aggregation-labelled ClusterRole). Not applying it is a scoping choice, not a proof of
  unsafety; it is documented as open (OPERATIONS → "What stays open", item 6).
- **What is true once this is applied (today the chart's `*` role is the live one):** a stolen controller token can no
  longer grant itself write on the admission policies through RBAC. "Argo CD cannot switch off the admission layer" is **not** true until P6 and P12 are both in Deny and this
  change is applied (P12 is #121, open; P12 judges names only, so the RBAC route needs P6 and P12 together). With
  authentication mode `API` (#118, open) the `aws-auth` route closes too.
- **Rollback** is `git revert <squash-sha>` (this repository squash-merges), which also removes the guard; a partial hand
  rollback is refused by it on purpose. A new operator Role needs Server-Side Apply; the UI no longer shows the live
  manifest of a webhook configuration. The plan's human view hides the values but the saved plan holds them in clear (the
  admin password's bcrypt hash): never commit or paste `argocd-rbac.tfplan`, delete it after the apply.
- **State:** merged; `terraform apply` of `infra/aws/kubernetes` is the user's Step 0 (OPERATIONS → Deploy and roll back →
  "What Argo CD may no longer do", and the PR body). **Kind runs:** the last full-mode `admission-e2e` run on #47 was
  42004329007 on `c5af15b` (success). The final head `0f1402c` changed the comments of `argocd.tf` only (plus 23 lines of
  tests and docs); there was **no full-mode run on it**. The ci-mode `admission` job passed on `0f1402c`. The PR was marked
  ready at 10:11:33 UTC and merged at 10:12:20 UTC on 2026-11-05.

## D181 — The Terraform scan of the kind run is a tripwire, and a foreign tree needs its provenance (amends D152; #114, #116)

- **Amends D152's wording** "refuses a module with a provisioner, `data "external"`, `data "http"` or a `*.tf.json`":
  `scripts/lib/tf_scan.py` is a small HCL lexer (comments removed, strings and heredocs blanked) that stops at
  `provisioner`, `alias`, `exec`, `config_path(s)`, `shared_config_files`, `shared_credentials_files`,
  `credential_process`, `postrender`, `web_identity_token_file`, `local-exec`/`remote-exec`, `data "external"`,
  `data "http"`, a `\u` escape, any `*.tf.json`, and (#116) the file-reading functions (`file`, `templatefile`,
  `filebase64`, `filemd5`, `filesha*`, `fileset`, `fileexists`, `pathexpand`, `abspath`), `path.cwd`, `path.root`, any `::`
  function and the data sources `local_file`, `local_sensitive_file`, `terraform_remote_state`, also in downloaded
  modules. `*.tftest.hcl`, `*.tftest.json`, `*.tfmock.*` and tfvars of the tree are deleted. A file is exempt from the
  function check only when byte-identical to this checkout's; in GitHub Actions the file functions are allowed with a
  logged notice (the runner holds none of the user's credentials) and the downloaded modules' `examples/`, `tests/` and
  `test/` directories are not scanned (Terraform does not load them).
- **This is a tripwire, not a refusal to rely on:** an aliased provider is not mocked by the test header, `env -i` does not
  stop an absolute-path read of `~/.aws` or `~/.kube`, and HCL is a language. What contains the risk is a runner or a
  machine without credentials (D152's "Not a sandbox").
- **Provenance:** a `--repo` that is not the running checkout is refused unless its HEAD commit is on an `origin/*` branch
  of this checkout (`origin/pr/*` and `origin/pull/*` never count; a `remote.origin.fetch` other than the default refspec
  is refused) and its tracked files are unmodified; the copy is made with `git archive` from that commit. `--force` /
  `FORCE=1` overrides with a loud warning. `TF_VALUES_CONTAINER=<terraform image by digest>` optionally runs Terraform in a
  container that mounts only the work directory (`--cap-drop ALL`, `no-new-privileges`, `--network none` for the test); that
  path is tested with a fake engine only and has **not** been run against a real container (#114).
- **`scripts/promote_only.sh`** (the predicate that can skip the required `admission` job) reads the diff in two states
  (the four exact header lines of a plain change of the one overlay file, then content lines checked against fixed shapes),
  balances each hunk's pins and policy literals, and answers no on more than one merge base, an invalid calendar date, more
  than one hold per group, or `$(` or a backtick in a reason.

## D182 — The RabbitMQ operators are namespace-scoped by kustomize patches over the vendored release (T12-g; implements D120; #51)

- **Decision:** `OPERATOR_SCOPE_NAMESPACE=ryft-rabbitmq` on both Deployments (Cluster Operator 2.23.1, Messaging Topology
  Operator 1.20.4) as strategic-merge patches keyed by container name; their cluster-wide ClusterRole and
  ClusterRoleBinding (`rabbitmq-cluster-operator-role`, `messaging-topology-manager-role`) are deleted by kustomize patches
  and replaced by Roles and RoleBindings with the same rules in `ryft-rabbitmq` (`scoped-rbac.yaml`). The vendored files
  and `SHA256SUMS` are untouched (D97: an upgrade stays a file swap). Neither operator can then read Secrets
  (`DATA_ENCRYPTION_KEY`, database URLs, `ADMIN_API_KEY`) or update or exec pods outside `ryft-rabbitmq`; admission cannot
  see reads, so this is the only control for them.
- **A check, because kustomize does not fail on an unmatched patch target:** `scripts/check_operators_scope.sh` in
  `make k8s-validate` fails if a ClusterRole grants `secrets`, `pods`, `pods/exec` or `*`, if any ClusterRoleBinding other
  than the metrics token-review one remains, if either Role or RoleBinding is missing, or if a manager container lacks the
  scope. `ryft-rabbitmq` became a destination of the `ryft-operators` project. Rollback: revert and let Argo sync it
  first; leave the project's extra destination in place.
- **Needed live:** each Role carrying every verb the operators need is visible only in a running cluster (symptom:
  `forbidden` in an operator log); OPERATIONS → RabbitMQ has the post-sync checks.

## D183 — T12-e's binding-state signal is kube-state-metrics' native collector; claims m and o (amends D151; #55, #121)

- **Amends D151's "Claim m disproved" bullet,** whose consequence "T12-e needs another binding-state signal" no longer
  holds: T12-e shipped in #55 (merged 2026-11-04) with kube-state-metrics' **native** `validatingadmissionpolicybindings`
  collector (existence and live actions; the chart adds the RBAC), which is what `deploy/monitoring/k8s-monitoring.yaml`
  enables and what `AdmissionBindingMissing`, `AdmissionBindingNotDenying`, `AdmissionBindingDrift` and
  `AdmissionBreakGlass` read (D120's "Signals" bullet already says so). What D151 recorded stays true: custom-resource-state
  exports CustomResourceDefinitions only.
- **Observed on the open #121** (draft; kind run 42004326289, workflow `admission-e2e`, mode full, head 4b9f739: 83 pass,
  0 fail, 2 skipped): `claim-m-native` passes (for each of the 13 `ryft-*` bindings the info series exists and the
  `validation_action` series are exactly one per entry of `spec.validationActions`, value 1). **Claim o is disproved
  for the policy as it was tested:** with P12 in Deny on that kind cluster (kube-apiserver 1.36.4), neither the controller's
  nor argocd-server's SelfSubjectAccessReview was refused. That is all the run measured. The reason #121 gives is that the
  review kinds (`selfsubjectaccessreviews`, `subjectaccessreviews`, `localsubjectaccessreviews`, `selfsubjectrulesreviews`,
  `tokenreviews`, `selfsubjectreviews`) are on kube-apiserver's hard-coded exclusion list, which the policy plugin consults,
  since Kubernetes 1.30; that is **read from the sources** of release-1.30 and v1.36.0, not measured. What P12 does with
  it is decided in #121, not here.
- **Alerts:** the series budget is about 300 of the free tier's 10k; the admission alerts stay quiet until the chart is
  enabled, except Pod Security refusals and the liveness alerts.

## D184 — A client that hangs up no longer fails the JWKS fetch; the admin `503` for an unknown key id stays (amends D153)

- **Considered and rejected: the admin routes answering an unknown key id inside the refresh limit with the generic
  `401`** (D153's "Known and accepted" bullet; built in the first version of #141 and withdrawn after its security
  review). Two reasons:
  - **Its premise was false.** It assumed that with the mapping a `503` on `/v1/admin/*` proves an outage. It does not:
    the key lookup ran on the request's context, jwkset fetches an unknown key id on `context.WithTimeout(requestCtx,
    1 s)`, and the transport recorded the cancelled fetch of a client that hung up as a failed fetch. Anyone could so make
    the last fetch "failed", get the `503` back, and count `jwks_unavailable`, which `AuthUnavailable` pages on (true on
    every route on main before this entry).
  - **It is worse in the case it is for.** During a key rotation the operator was not told about, a flood of forged key
    ids holds the global refresh limiter, so the operator's own token with the new key id would get `401`, refresh,
    `401` again, and the page signs out (a local revoke), where the `503` asks for a retry.
- **The `503` stays** on the admin routes as on the user routes. It means "the api could not complete a key fetch (or the
  database check) for this token", not that an outage is proven; API.md, OPERATIONS and OBSERVABILITY say so.
- **Decision instead, the root cause in `internal/auth` (api and ingest):**
  - `Supabase.Verify` calls the keyfunc with `context.WithoutCancel(ctx)`: a fetch an unknown key id starts no longer
    ends with the client. It stays bounded by jwkset's 1 s (`RateLimitWaitMax`, which also bounds the fetch) and the
    HTTP client's 10 s; the context's values stay. A request whose client left may finish its key lookup (at most 1 s).
  - The transport records only a `200`. A failure is recorded by jwkset's refresh error handler
    (`RefreshErrorHandlerFunc`, keyfunc's log line kept word for word), which sees the whole refresh: no answer, another
    status, or a `200` whose body is not a key set (that was counted as a working fetch). A refresh cut short by its
    own context being cancelled (the service stopping) is not recorded. One that ran out of its time is: after the
    detach that can only be the fetch's own bound, a JWKS slower than 1 s, and treating it as healthy would answer a
    real rotation with `401 unknown_key` on the user routes (a sign-out).
  - Behaviour change: a client that disconnects mid-refresh no longer makes later unknown key ids `jwks_unavailable`,
    and a `200` with an undecodable key set now makes an unknown key id a `503` instead of `401 unknown_key`. Nothing
    changes for a valid token, an expired one, an unknown key id inside the limit (`503 jwks_refresh_limited`) or an
    outage (`503 jwks_unavailable`). `AuthUnavailable` and its test are unchanged.
- **Not done:** a refresh limiter per caller (the limiter stays global, so a flood of forged key ids still delays a
  rotated key by up to a minute, answered with retryable `503`s); JWKS stale-while-revalidate (STATUS, Phase 3).

## D185 — Only Traefik and the smoke hook reach the services on 8080; the ingress-nginx peer is gone (supersedes D16's third bullet; #140)

- **Decision:** port 8080 of the services is admitted from exactly two peers. **Traefik** in `kube-system` (pods
  `app.kubernetes.io/name: traefik`) for api, ingest and web, and **the PostSync smoke pod** (namespace `ryft-smoke`,
  `app: smoke`, `deploy/k8s/base/smoke`) for api, ingest, scoring and notify (`deploy/k8s/base/<service>/networkpolicy.yaml`
  on `origin/main` at `da869d0`). No other peer reaches 8080.
- **What changed (#140, merged 2026-11-05 17:27 UTC):** api, ingest and web also admitted 8080 from namespace
  `ingress-nginx` / pods `app.kubernetes.io/name: ingress-nginx` ("if you run that instead"). Nothing deploys
  ingress-nginx: the only Ingress controller is Traefik in `kube-system` (the `helm_release.traefik` in
  `infra/aws/kubernetes/addons.tf`; k3s's default locally, D24). The peer was an unused way around Traefik: a pod in a
  namespace named `ingress-nginx` with that label could call the services directly and set the rightmost
  `X-Forwarded-For` entry, which `httpx.ClientIP` and the admin per-address limiter (D155) take as the one Traefik
  appended. #140 removed that one peer from the three policies and nothing else; `grep ingress-nginx` over `deploy/` and
  `infra/` now finds nothing.
- **D16 is out of date elsewhere too:** its first bullet says Prometheus reaches `:8080`; the metrics collector scrapes
  `/metrics` on **9090**, from namespace `monitoring` (base policies) and, on EKS, from the VPC range
  (`deploy/k8s/overlays/eks/metrics-from-nodes.yaml`, the host-network collector, D51), not on 8080. Its Cloudflare tunnel
  peer no longer exists in `deploy/`. Its backup bullet is not re-checked here.
- **Would change:** moving to another Ingress controller changes the policies and this decision in the same PR.
- **Verification after the sync** (#140's body, the owner's read-only checks: `grep -c ingress-nginx` on the three
  namespaces' NetworkPolicies `0`, `argocd app get ryft-platform` Synced and Healthy, `GET /v1/me` `401` and `/` `200`
  through Traefik, `make smoke-prod`): given to the owner; the result is **not verified**.

## D186 — Vault Community Edition in the cluster replaces HCP Vault Dedicated (amends D45, D46; #145, #146, #147, #151, #152, #153; the owner, 2026-11-05/06)

- **Decision:** RYFT's secrets move from HCP Vault Dedicated (Enterprise namespaces `admin/ryft/<svc>`, public endpoint) to
  Vault Community Edition **2.2.1** in namespace `vault` on ryft-prod-euc1: three replicas with integrated storage (Raft),
  auto-unseal by AWS KMS (D189), Raft snapshots to S3 (D192). Reasons: the HCP cost line goes, the public Vault endpoint and
  HCP's TokenReview calls into the EKS API go, and the whole secret path lives in git and Terraform. Workloads keep reading
  the Kubernetes Secrets that VSO syncs, so Vault is not on the request path: an outage stops syncs and rotations, not
  running pods. HCP Terraform stays the state store (D56) and is the second copy of every value.
- **The owner decided (plan.md, 2026-11-05/06):** the server is a Terraform add-on, not an Argo CD app (D188); **dual-write**
  to both Vaults until decommission (D191), not a freeze of HCP; **BUSL-1.1 accepted** (internal use) and CE's "latest minor
  only" support, so a minor upgrade is planned every few months; **Vault 2.2.1** by digest with chart 0.34.1, which is
  tested up to Vault 2.0.4 and so runs outside the chart's matrix; the operator logs in with **OIDC and Google Workspace**,
  not the design's userpass with TOTP (D193); recovery keys **2-2-1** over three places (D203); Vault's logs go to Grafana
  Cloud (D197); the import service's and monitoring's operator objects move to automatic apply (D200-D202); the EKS public
  endpoint is narrowed after HCP is gone (D205).
- **Order and gates.** `infra/aws` keys and bucket (#146) before the add-on (#145); then the bootstrap, then `write_k8s`
  (PR 3b), then one cutover PR per group (`vso_on_k8s`, one name per PR), a 14-day soak, then the decommission of HCP (PR 6, 6b),
  **only on the owner's explicit go**, after a fresh `vault_diff.sh` with zero differences and a restore drill. Before the
  bootstrap and before PR 3b four gates must hold (OPERATIONS → Vault → Hard gates):
  - **G1** D180 Step 0 (#47) applied, and P12 (#121), P13 and the `vault` branches of P1, P2 and P3 in **Deny**. Merged
    means disabled, then Audit; neither stops an Argo CD controller token from running a pod in `vault` as the service
    account `vault`. P12 cannot go to Deny alone: #121's chart render and `admission.tf` precondition refuse it unless P6
    (`operators-cluster`), P1 (`namespaces`) and P2 (`workloads-templates`) are in Deny too, the **general** `ryft-*` bindings.
    #121 is a stale draft (`4b9f739`; a trial merge with `main` conflicts in 19 files). Order: #47's Step 0 applied; #121 rebased
    (it merges second: above chart 0.5.1, 18 bindings, 13 policies, P1's in-scope list reconciled); a fresh full-mode
    `admission-e2e` run on a tree containing #47; #121 marked ready, reviewed, merged, its decision given a number (D206 is
    next); the chart enabled; P6, P1, P2 to Deny, then P12. The `vault` bindings and P13 can go to Deny at any time.
  - **G2** the monitoring stack sits inside Vault's trust boundary (D194). Accept that or reduce it: the owner's call, **no
    decision is recorded here**.
  - **G3** never `vault login` through `kubectl exec`; `Initialized true` before the owner's own init means stop and wipe.
  - **G4** `prevent_destroy` on the namespace and both Helm releases (done in #145).
- **Amends D45** (one Vault namespace, one `auth/kubernetes` mount and one long-lived `vault-auth` reviewer token per
  service: replaced by D187 on the CE side; the nine tokens and their cluster role bindings go at the decommission) **and
  D46** (VSO's connection target changes per namespace; the add-on itself stays).
- **State (2026-11-06):** #146 applied by the owner; #145, #151, #153 merged, **not applied**; nothing reads the in-cluster
  Vault. STATUS → Vault CE migration (2026-11) has the ordered steps.

## D187 — Isolation by mount, role and policy instead of namespaces (amends D45; #151; reserved names #147, #153)

- **Decision:** CE has no namespaces. Each service gets a KV v2 mount `kv-<svc>`, a policy `<svc>` that reads only
  `kv-<svc>/data/config` (rabbitmq also `kv-rabbitmq/data/amqp-user-*`; no `metadata`), and a role `<svc>` in the **one**
  `auth/kubernetes` mount, bound to exactly its namespace and service account with audience `vault` and today's TTLs (3600 s,
  maximum four times that). One auth mount, because per-service mounts would share the same reviewer and cluster and add no
  isolation. The demo password moves to `kv-operator`, which no role's policy names.
- **Kubernetes auth is in-cluster:** `kubernetes_host = https://kubernetes.default.svc`, Vault reviews tokens as its own
  service account (`system:auth-delegator`), no reviewer JWT, no CA. The same `random_*` values feed both Vaults (D191).
- **The snapshot job's identity** (differs from the design, which named the policy `vault-snapshot`): role `vault-snapshot`,
  bound to `vault/vault-snapshot`, audience `vault`, revocable **service** tokens of 10 minutes (TTL and maximum 600), policy
  **`snapshot`** only and **no default policy**: `read` on `sys/storage/raft/snapshot` and nothing else (restore stays
  break-glass). Created in `infra/vault` (`k8s.tf`, written from PR 3b on), not by the snapshot PR. Such a token cannot revoke
  itself, so the job does not try (D192).
- **Names:** service names must be canonical (`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`) and `default`, `root`, `admin`, `terraform`,
  `vault-snapshot`, `snapshot` and `operator` are refused (a validation; `vso.reservedServices` of the admission chart refuses
  the matching `kv-` mounts in the namespace that would map to them: #147, and `snapshot` in #153). The mount formula maps
  `ryft-monitoring` and `monitoring` to the same `kv-monitoring`; admitted, because the role binds the namespace.
- **State:** merged, nothing written: `committed_write_k8s = false`.

## D188 — The Vault server is a Terraform add-on, not an Argo CD application (amends D94's list of add-ons; #145)

- **Decision:** `helm_release.vault` (`hashicorp/vault` 0.34.1, tarball sha256 pinned in the tests, image
  `docker.io/hashicorp/vault:2.2.1@sha256:962a…`) and `helm_release.vault_extras` (the local chart `charts/vault-extras`: the
  Certificates, NetworkPolicies, PDB, service-registration Role and, from #153, the snapshot CronJob) in
  `infra/aws/kubernetes/vault.tf`, beside VSO and cert-manager. Argo CD would have needed write on StatefulSets and CronJobs,
  a binding to `system:auth-delegator`, a project for a namespace that is not `ryft-*` and an image outside renderpolicy:
  each widens what a stolen controller token can do, and the server holding every credential is the worst place to widen it.
- **How it is held:** 3 replicas, `updateStrategy: OnDelete`, `wait = false` (an uninitialised Vault is not ready by design,
  D196, and waiting would fail every first apply), `prevent_destroy` on the namespace and both releases (removal is two
  reviewed steps), Pod Security `restricted`. Five **preconditions** (plan and apply fail): the alias resolves to a key of this
  account and region; none of the words the chart's start script `sed`-replaces is in the config; a tripwire against AWS
  credential paths in the values (a tripwire, not a proof); one certificate name per replica; no LoadBalancer or NodePort.
- **Before the apply (#145, #153):** the SDK injector webhook's `excludedNamespaces` gain `vault` and
  `vault-secrets-operator-system` (`deploy/monitoring/k8s-monitoring.yaml`, rolled out by the monitoring apply), and the
  Fleet Management pod-log pipeline and Beyla must exclude `vault` (D197); Beyla's probes read process memory.
- **Consequences:** a chart or image change needs the owner's apply, and OnDelete updates are manual anyway (D204). The
  association, namespace, extras and chart order themselves in one apply. Disaster-recovery order: Argo CD's wave 0 waits for
  VSO. Chart 0.34.1 with Vault 2.2.1 is **not verified at runtime**.
- **State:** merged, **not applied**. Plan expectation **from `main`: 11 to add, 1 to change, 0 to destroy**: #145's seven plus
  #153's four (`module.vault_snapshot_pod_identity`: role, policy, attachment, association); `vault_extras` is created at 0.2.0 with
  the snapshot CronJob, which fails until the role `vault-snapshot` is written (D187); `cluster_issuers` 0.2.0 → 0.3.0 is the change.

## D189 — KMS auto-unseal through EKS Pod Identity; the keys and the bucket outlive the cluster (#146, #145, #153)

- **Decision:** the seal is `awskms` with the key **ARN** (from the alias, never the alias; a precondition requires this
  account and region) and the region in the stanza. SDK v2's default chain finds the Pod Identity credentials: no static keys,
  no IRSA annotation. Role `<cluster>-vault-unseal` may `kms:Encrypt`, `kms:Decrypt`, `kms:DescribeKey` on that key only,
  through the association `vault/vault`; its trust policy also requires the session tags `kubernetes-namespace=vault`,
  `kubernetes-service-account=vault` and `eks-cluster-arn` = this cluster.
- **In `infra/aws` with `prevent_destroy` (#146):** the unseal key, a **separate snapshot key** (so the snapshot job never holds
  the key that unwraps Vault's root key) and the bucket `ryft-prod-euc1-vault-snapshots-<account>`: private, versioned,
  SSE-KMS with the bucket key, `BucketOwnerEnforced`, current objects kept 30 days and old versions 7 more. The bucket denies
  non-TLS requests and any put that does not name the snapshot key's ARN; #153 adds a third deny, `DenyPutWithoutIfNoneMatch`
  (a put without `If-None-Match` is refused: create-only).
- **Key policy:** the account administers (IAM decides who uses a key, so roles can be rebuilt without touching the keys), and
  `NoDeletionOrDisable` denies `kms:ScheduleKeyDeletion` and `kms:DisableKey` to every principal, root included, because
  `prevent_destroy` disappears with its block. An **optional** allowlist (`vault_unseal_allowed_principal_arns`, default
  empty) limits the unseal key's cryptographic use to named principals, with a `check` that warns when Vault's role is
  missing; it stays off until Vault has unsealed with its role, because a wrong list keeps Vault sealed.
- **Consequences:** losing the unseal key makes Vault's data and snapshots unreadable; recovery is a fresh Vault plus
  `infra/vault` from state (hours, no credential lost). A teardown of `infra/aws` targets the cluster and the network and
  leaves the keys and the bucket. Rolling back #146 after the apply is wrong: deleting the blocks destroys the aliases and the
  bucket settings (infra/aws README → Destroying). Cost about +$2 a month. A `check` warns on a non-`ON_DEMAND` node group (D204).
- **State:** **applied by the owner on 2026-11-06: 11 added, 7 changed, 0 destroyed.** The 7 were drift of the node group and the
  add-ons (ENI/CNI, CoreDNS, kube-proxy, EBS CSI, pod identity agent, metrics-server, node group release 1.36.4-20261012 →
  1.36.5-20261031), applied by mistake against #146's stop rule; the cluster stayed healthy and `make smoke-prod` was green (the
  owner's report, not verified here; the seven addresses are in the HCP Terraform run log). The pod-identity role and the association
  come with #145 (not applied). **Not verified:** the credentials reaching the seal at runtime; that EKS sets the three
  request tags for this association; `UploadPart` is probably denied by the key-header rule (the job uses one `put-object`).

## D190 — Vault's TLS comes from a CA of its own; every namespace requests its own `vault-ca` (amends D98; #145, #151, #147)

- **Decision:** a second cert-manager CA, `ryft-vault` (a self-signed bootstrap issuer, the root `ryft-vault-ca` in
  `cert-manager`, ECDSA P-256, ten years; `charts/cluster-issuers` 0.3.0, `ryft-internal` untouched), signs
  `vault/vault-tls` (the 18 service and pod names plus `127.0.0.1`; one year, renewed 30 days before) and one `vault-ca`
  Certificate per RYFT namespace. That Secret's `ca.crt` is the VaultConnection's `caCertSecretRef`; Argo CD cannot write
  Secrets, and a literal CA in git would need it. The leaf is inert (`commonName: vso.<ns>`, no SAN, and `ryft-vault` is no
  CA the broker trusts). A separate CA, because leaves of `ryft-internal` are a TLS factor at the broker (D98).
- **Amended in #151:** the generator writes the `vault-ca` Certificate into every overlay on **both** targets, before any
  namespace moves, and the literal CA Secret (`vault_ca_bundle_b64`) is retired at once (a validation refuses it: it would
  fight cert-manager). The Argo CD project `ryft` ignores the Secret `vault-ca` as an orphan, which also hides a hand-made one.
- **Reload:** the Secret is mounted as a directory and `tls_reload_interval = "5m"` (new in 2.2.0) picks up renewals, no
  sidecar. **Not verified:** that renewal is picked up. P13 (D195) limits what `ryft-vault` may issue; **until P13 is in Deny
  any namespace can issue from it.**
- **Amends D98:** its internal CA is no longer the only one; the broker's rule is unchanged.
- **State:** the Certificates exist after #145 is applied and the overlays are applied and committed (STATUS).

## D191 — One `infra/vault` root writes both Vaults during the migration; git says which namespace reads which (#151)

- **Decision:** a second configuration of the `vault` provider (alias `k8s`) reaches the in-cluster Vault only through the
  owner's `kubectl port-forward`: a loopback address (validated), a CA file of its own, `skip_tls_verify = false`, and an
  **ephemeral** 1-hour token in `TF_VAR_vault_k8s_token` (in neither plan nor state). Every CE object is a new address that
  reads the same locals and `random_*`, so the values reach both Vaults and the migration is a copy, not a rotation. Every
  apply writes both Vaults until the decommission, so a rollback never hands out a stale credential.
- **Two committed locals** switch the CE writes on (`committed_write_k8s`) and move namespaces (`committed_vso_on_k8s`, a list
  of service names). **One switch per PR.** Preconditions: a name must be a service and needs `write_k8s`; `write_k8s` needs
  the token. `test_override_targets` exists for `terraform test` only and warns if set.
- **While `write_k8s` is false the plan makes no request to the CE Vault**, pinned by a test with the real provider against a
  closed port. The two shells (vault and Terraform) are never mixed.
- **Rejected:** a second root, which needs either a cross-state move of the secret values (D56's failure) or a second state
  holding every credential.
- **State:** merged; both switches off. Expected plan with the committed defaults: 9 to add, 9 to destroy, all generated files.

## D192 — Raft snapshots every 6 hours to S3 by two pinned upstream images; the upload is create-only (#153, #146, #151, #155)

- **Decision:** a CronJob `vault/vault-snapshot` in `vault-extras` (`17 */6 * * *`, `concurrencyPolicy: Forbid`, Job deadline
  600 s, Pod Security `restricted`). RPO is 6 hours. An init container (the server's own image) logs in with Kubernetes auth as
  role `vault-snapshot` (D187), saves from `vault-active` (a save through a standby fails), runs `raft snapshot inspect`, and
  **does not revoke** its token (no default policy; it expires in 10 minutes). The snapshot lives in a memory-backed
  `emptyDir` and is removed on every exit, SIGTERM included. An upload container (AWS CLI 2.39.5, multi-arch index digest) does
  one `aws s3api put-object` with `--if-none-match '*'`, `--expected-bucket-owner`, `--server-side-encryption aws:kms
  --ssekms-key-id <full ARN>`, `--bucket-key-enabled` and `--checksum-sha256`. Not a custom image or Go code.
- **The checksum (#155, vault-extras 0.2.1):** the init container hashes the snapshot (SHA-256, hex, a file beside it in the
  `emptyDir`) between the save and the inspect. The upload checks that hash (64 lowercase hex characters), converts it to
  base64 of the raw digest with POSIX `printf` octal escapes (no `xxd` in the CLI image), checks the result is 44
  characters, and sends it as `--checksum-sha256`; the hex value stays in the metadata `sha256`. S3 stores the object only if
  the bytes it receives have that hash, so the stored object is what was hashed and then inspected. Replaces
  `--checksum-algorithm SHA256`, with which the CLI hashed the stream itself at upload time (an unsigned trailer) and so
  checked the transfer only. With a precomputed checksum the CLI signs the payload, so `XAmzContentSHA256Mismatch` is
  possible again beside `BadDigest` (OPERATIONS → Vault → Snapshots).
- **The job's AWS role** (`vault_snapshot_pod_identity`, `infra/aws/kubernetes`): `s3:PutObject` on `<bucket>/snapshots/*`
  (with `s3:ResourceAccount` and the key-ID condition) and `kms:GenerateDataKey` on the snapshot key (`kms:ViaService` = S3,
  encryption context = the bucket). No get, list, delete or decrypt. The Pod Identity token is mounted in **both**
  containers, so the init container could use the role too; the role can put snapshots and nothing else.
- **Guards:** P2 admits the AWS CLI image **only in a pod whose service account is `vault-snapshot`** (D195); the bucket
  refuses a put without `If-None-Match` (D189), so a stolen job identity cannot overwrite a snapshot; the template refuses to
  render without digests, with an alias, or with a key of another account or region.
- **Alerts:** `VaultSnapshotStale` (critical, 13 h) and `VaultSnapshotFailing` (D194). The lifecycle deletes the last good
  snapshot after 37 days without a new one, so this alert is the only thing between a broken job and an empty bucket.
- **Restore** needs the same KMS key. A restore drill in kind is a ticket, not built.
- **State:** merged, **not applied**. **Not verified:** S3 conditional writes and the SHA-256 checksum against this bucket
  policy; whether S3 asks for `kms:Decrypt` when it reuses a cached bucket key; Pod Identity credentials in a CronJob pod;
  that `kubectl create job --from=cronjob` sets `lastSuccessfulTime`.

## D193 — Vault administration: port-forward, OIDC with Google Workspace, no root token, an hour-long Terraform token that is admin-equivalent (#151; amended by D197, D203)

- **Decision:** no Ingress and no LoadBalancer; the API is reachable only by `kubectl port-forward`, from a standalone terminal,
  never an agent's shell. Init gives 5 recovery keys with threshold 3 (D203). The **root token is revoked** after the bootstrap.
- **Departure from the design (userpass with TOTP) by the owner's decision:** OIDC against Google Workspace, an internal OAuth
  client with the CLI's loopback redirect only. The role `admin` binds `hd = ryftapp.de`, the operator's numeric `sub`
  (`bound_subject`, checked **fail-closed** after every write), and `max_age` 10 minutes. Tokens last 1 hour (2 at most) and are
  bound to `127.0.0.1`. The second factor is 2-Step Verification with security keys. An `enroll` role (5-minute tokens, deleted
  after use) reads the `sub`. No policy is attached to identity entities or groups. A login must not depend on Google alone
  during an incident, so **break-glass is `vault operator generate-root`** with the recovery keys; a use is to page
  (`VaultRootTokenUsed`, D199, **not built**: until then nothing pages and the drill finds the two root logins in the audit log),
  and a foreign attempt in progress is cancelled and treated as an incident.
- **Which Google account** (a dedicated Workspace user such as `vault-admin@ryftapp.de`, recommended, or the existing account
  hardened) is **an open question to the owner**, not decided.
- **The bootstrap files** (OIDC config, policies `admin` and `terraform`, the token role `terraform`) are committed in
  `infra/vault/bootstrap/`, held to an exact allowlist by tests, and written into Vault by hand.
- **The Terraform token** comes from the token role: policies `default` and `terraform` (allowed: `terraform`; disallowed: `admin`, `root`), 1 hour, **not renewable**, `token_bound_cidrs
  127.0.0.1/32`. The policy is generated and uses **exact paths only**, because Vault canonicalises policy names on write but
  matches ACL paths as written, so the design's globs were bypassable (`ADMIN`, `Terraform`, `admin%20`). It may write only
  the services' policies and `snapshot`, and only the services' roles and `vault-snapshot`, each role with its own policy,
  namespace and service account only; the auth mount stays `kubernetes` and in-cluster; no tune may switch audit HMAC off
  (`audit_non_hmac_*`, `passthrough_request_headers`, `allowed_response_headers`, `listing_visibility`, and `token_type` on the
  auth mount). It has nothing on audit, seal, Raft, generate-root, OIDC or identities, and on tokens only what `default` gives (look up, renew
  and revoke itself; the role's token is not renewable).
- **The token is still admin-equivalent for its hour:** it reads every credential, and a rewritten policy plus that role's
  Kubernetes token is any right. Residual: `vault secrets enable -audit-non-hmac-request-keys=…` succeeds under the token,
  because the keys travel inside `config` at creation. Covered by a `k8s.tf` test, the post-apply baseline and (not built) the
  Loki rules of D199.
- **State:** the files are merged; the bootstrap is **not run**. **Not verified:** that 2.2.1 still enables audit devices over
  the API with these options; Google's `iss`, `hd` without the `email` scope and `auth_time` for `max_age`; that the Admin
  console user ID equals the OIDC `sub`; the loopback binding (the connection's peer as `127.0.0.1` through containerd's
  port-forward: if it is `::1`, all three bound files fail at the first login, which does not lock the bootstrap out);
  generate-root under auto-unseal.

## D194 — Vault metrics without a token, admitted by NetworkPolicy to named peers; the alerts are gated on "initialised" (#145, #153, #155)

- **Decision:** `unauthenticated_metrics_access` on the listener lets every node answer `/v1/sys/metrics` (standbys would
  redirect, and a scrape token would live where Fleet's remote config can read it, D51). The `alloy-deployment` collector
  scrapes it over TLS with `vault-ca` from `monitoring`, behind a keep list (`ryft/vault`), and a second scrape of
  kube-state-metrics for namespace `vault` (`ryft/vault-state`) adds `kube_pod_labels` with the pod labels `vault-initialized`,
  `vault-sealed`, `vault-active`. Sealed is read from readiness (a sealed pod exports nothing).
- **The NetworkPolicy names pod-network peers:** the snapshot job's pods and the Raft peers (8200/8201); VSO and the collector
  each behind a value, **off by default**. The collector peer is the Terraform variable `vault_collector_peer` (default
  `false`), set to `true` only after the owner's own init, the root token revoked and G1-G4 holding, and false again before a
  restore into a fresh cluster. `peers.vso` stays off until the first cutover. No ipBlock ingress.
- **Correction to the design:** the policy governs pod-network peers only, not `kubectl port-forward` or traffic from a node's
  own address (kubelet, very likely host-network pods). And even initialised, port 8200 answers more than the metrics without
  a token: `sys/health`, `sys/seal-status`, `sys/leader`, starting or cancelling a generate-root or rekey attempt (completing
  one needs recovery shares) and the login endpoints. Before the init the collector peer would reach the unauthenticated
  `PUT /v1/sys/init`, hence the variable.
- **Trust boundary (G2, D186):** alloy-daemonset is privileged with hostPID and can read the memory of every pod; alloy-operator
  and VSO read Secrets cluster-wide. Vault is therefore as safe as the monitoring stack and VSO. The Art. 32 description must
  not say Vault is isolated from the monitoring vendor. Accepting or reducing this is the owner's decision (open).
- **Alerts:** group `ryft-vault`, 11 rules (D179's limit is 20), each with a firing and a quiet promtool test: `VaultSealed`
  (warning), `VaultSealedQuorum`, `VaultUnavailable`, `VaultNoActiveNode` (critical), `VaultRaftPeersBelow3`,
  `VaultSnapshotStale` (critical), `VaultSnapshotFailing`, `VaultAuditFailing` (**critical**), `VaultVolumeFilling`,
  `VaultUpdatePending` (info, D204) and `VaultScrapeDown`. The 7-day readiness gate is replaced by "this Vault has been
  initialised" (a pod labelled `vault-initialized=true` in the last hour, or a CronJob success, which outlasts an outage of
  every pod), so an uninitialised Vault (D196) pages nothing.
- **Amended (#155): the label half is not durable.** Vault's Kubernetes service registration starts every process as
  initialized=false, sealed=true and sets `vault-initialized=true` again only on unseal (as read from Vault's source in the
  #153 re-read; to verify in the kind drill). The label therefore means "unsealed at least once since this process
  started": after every pod restarts and stays sealed it lapses an hour after the last unsealed sample, and only the
  snapshot success holds the gate. So the **first snapshot, by hand, is a precondition of the alerts**: right after the
  init and PR 3b's apply, before `make alerts-apply`, and again after every restore or re-bootstrap (a rebuilt CronJob
  has no `lastSuccessfulTime`). The collector peer closes the pod-network path only; the host-network `alloy-daemonset`
  very likely still reaches 8200 (to verify), so G3's "`Initialized true` before your own init: stop" covers the rest.
  Known gap: with the StatefulSet gone, `VaultUnavailable` and the server and kubelet branches of `VaultScrapeDown` are
  silent by design. `PodRestarting` covers `ryft-.*|vault`; `CertificateExpiring`
  covers `vault/vault-tls`. Series budget under 65.
- **State:** merged (#145, #153), **not applied**. **Not verified in the cluster:** the scrape, the `alloy-deployment` and
  VSO pod labels, KSM's endpoint labels, `kube_pod_labels` with Vault's labels, `kubelet_volume_stats_*` from Fleet (if absent,
  drop `VaultVolumeFilling` and the kubelet branch of `VaultScrapeDown`), `vault_token_count*`.

## D195 — The admission layer covers namespace `vault` and the chain of the `ryft-vault` issuer (amends D120; #147, #153)

- **Decision:** the chart `admission-policy` goes 0.2.3 → 0.5.0 (#147) → 0.5.1 (#153): **12 policies, 17 bindings**. P1 holds
  `vault` to Pod Security `restricted`. P1, P2 and P3 get **bindings of their own for `vault`** (`<name>-vault`; the general
  ones are `NotIn [vault]`), in step A on `audit`, so that the `vault` bindings can go to Deny independently of the `ryft-*`
  rollout and gate G1 can be met on its own. `AdmissionPodDenied` covers `ryft-workloads-pods-vault`; `admission_intended.sh
  --check` now fails when `AdmissionBindingMissing`'s threshold differs from the binding count.
- **P2 in `vault` judges images only:** every container, init, ephemeral container and image volume must match `images.vault`
  (2.2.1 by exact digest) or `images.awsCli`, each one literal reference with a full digest (no alternation, group or
  repetition in the chart guard). #153 pins `images.awsCli` and admits it **only in a pod whose `serviceAccountName` is
  `vault.snapshotServiceAccount`** (`vault-snapshot`). Commands and service accounts in `vault` are bounded by RBAC (only
  Terraform writes there) and P12; the Vault image is refused in `ryft-*`.
- **P3 in `vault`:** no ingress ipBlock; egress ipBlocks only the public shape on 443 (no 5432), the VPC CIDR on 443 (API
  server) and the Pod Identity agent `169.254.170.23/32` on 80: tightenings of the design text.
- **P4** admits the union of the HCP and CE fields during the cutover, judged field by field (address list, `caCertSecretRef`
  absent or `vault-ca`, Vault namespace absent or under `admin/ryft/`, mount `kv` or `kv-<service>`); **the pairing is not
  enforced** (a mix fails closed at TLS or login). It refuses the reserved mounts (D187). The decommission empties
  `vso.kvMount` and `vso.vaultNamespacePrefix`, and then only the CE fields pass.
- **P13 `ryft-vault-trust`** (step A, `audit`) guards the chain: Certificates with `secretName` `vault-ca`, `vault-tls` or
  `ryft-vault-ca`, every Issuer and ClusterIssuer, and Kubernetes CSRs for a `ryft-vault*` signer. Only `vault/vault-tls` and
  `<ns>/vault-ca` (no SAN-bearing key) may be issued from the issuer; the chain's Secrets may be written only by those
  Certificates; the ClusterIssuers `ryft-vault` and `ryft-vault-bootstrap` are pinned; **Argo CD's identities may write only
  Issuers that are exactly `selfSigned: {}`** (an Issuer of type `vault` or `acme` would make cert-manager send a Secret's
  value to a server); CertificateRequests for the issuer and their status only as the cert-manager service account. **Not held
  by P13:** a Secret written directly (RBAC), and Argo CD writing a well-shaped Certificate in `vault` (P12).
- **Amends D120:** its eleven policies become twelve; `vault` and the `ryft-vault` chain are inside the layer.
- **State:** merged with `enabled: false` in `admission-values.yaml`: **nothing is live**, and the first apply of the chart
  starts all 17 bindings on `audit`. `make admission-celtest`: 960 cases, 0 failures. **Merge order with #121 (P12, chart
  0.3.0, an open stale draft):** #147 and #153 merged first, so #121 bumps above 0.5.1 and sets 18 bindings and 13 policies
  (`--check` fails if missed); D186's G1 has the order for enabling and for Deny. The VSO and alloy pod labels in the NetworkPolicy fixtures are placeholders.

## D196 — Vault's probes, budget and placement: alive when sealed, ready only when serving (#145)

- **Decision:** liveness is `/v1/sys/health?standbyok=true&sealedcode=204&uninitcode=204&haunhealthycode=204&removedcode=204`
  after 60 s, 6 × 5 s (so a sealed or uninitialised pod, a standby that cannot reach the leader (474) and a removed node (530)
  are alive, not killed); readiness is `/v1/sys/health?standbyok=true` (initialised, unsealed, active or standby). The
  user's draft had the two paths the other way round. The Services publish ready pods only; `vault-internal` publishes all.
  Standbys advertise `https://<pod>.vault-internal.vault.svc:8200` (`$(VAULT_K8S_POD_NAME)`, not `$(HOSTNAME)`, which the
  chart defines after `VAULT_API_ADDR`).
- **Own PDB** in `vault-extras` (the chart's PDB is off): `minAvailable` 2 (quorum) with `unhealthyPodEvictionPolicy:
  AlwaysAllow`. The trade-off: a pod that is not Ready can always be evicted, which keeps an uninitialised or sealed Vault
  from blocking every drain, but an unsealed voter that flaps NotReady can be evicted while another pod is down. **Amended by D204.**
- **Placement:** one pod per node (the chart's anti-affinity) and per zone (`topologySpreadConstraints`, `minDomains` = the
  replicas, `DoNotSchedule`: with nodes in fewer zones than pods a pod stays Pending). `terminationGracePeriodSeconds: 30`.
  The service-registration Role is `get, update, patch` on `vault-0..2` only (the chart's had every pod in the namespace).
- **Consequences:** an uninitialised Vault is three pods Running and `0/1` ready, by design, and nothing alerts (D194).
  **Not verified:** the PDB and zone spread under a real drain; service registration at runtime (a failure is silent: only
  the pod labels and `auth can-i` show it).

## D197 — Vault's logs go to Grafana Cloud through the collector's own pipeline, shipped off until the DPA is accepted (amends D193; D52 and D51 unchanged for `ryft-*`; #153, #145)

- **Decision (the owner accepted the recommendation):** Vault's server log (JSON, `info`) and its `stdout` audit device are
  shipped by `alloy-daemonset` from the node's container log files to the stack's hosted Logs (14 days). The pipeline
  (`podLogsViaLoki`) is in the collector's values in git, limited to namespace `vault`, with the labels `cluster`,
  `namespace`, `pod`, `container`, `job="ryft/vault"` and `stream`, no structured metadata. **No CloudWatch sink.** The PVC
  audit file stays the complete local record. The audit log holds no app user's data; the operator is a pseudonymous OIDC
  `sub` (D198).
- **Shipped OFF.** It is switched on by a later PR (the Loki URL, `podLogsViaLoki.enabled: true`, the key `LOKI_USERNAME` in
  `infra/vault`) only after the owner has accepted Grafana Labs' DPA and added the **record of processing** entry. The record
  must name the transfer mechanisms: the UK is a third country with an adequacy decision and the DPA lists US entities and
  sub-processors (**to verify**). Vault also holds the smoke user's and the App Review demo account's e-mail addresses as KV
  values (HMAC'd in the audit log).
- **Mandatory before the add-on is applied (D188):** Fleet Management's pod-log exclusion `ryft-.*` → `ryft-.*|vault` and a
  Beyla exclusion of `vault` (OPERATIONS → Fleet exclusions of vault; the heading was "Before the Vault add-on is applied"), then an Explore check that `{namespace="vault"}`,
  Beyla series and Tempo return nothing.
- **Limits:** the Loki copy is not tamper-proof (anyone with `logs:write` can push `stream="audit"` lines); operator sessions
  show `127.0.0.1`. **Not verified:** the Fleet UI paths and Beyla's exclusion syntax, the pipeline end to end.
- **State:** merged (#153), switched off.

## D198 — Vault's audit: two file devices, raw logging off, no request headers, nothing over the network (#151, #153)

- **Decision:** the bootstrap enables `file` at `/vault/audit/audit.log` and `file` with `file_path=stdout`, both with
  `log_raw=false hmac_accessor=true format=json` set explicitly; Vault serves while one device records. No `socket` or `syslog`
  device and no sidecar (an undrained socket stalls Vault). The three request headers a fresh Vault audits (`correlation-id`,
  `x-correlation-id`, `user-agent`) are deleted, so no request header is audited. No mount sets `audit_non_hmac_*` (D193).
- **The operator's identity:** the OIDC roles use `user_claim = "sub"` and `bound_subject`, map no email, name or groups, request
  no extra scopes and set `verbose_oidc_logging = false`, so every audit line names the operator by a pseudonymous number.
  `log_raw` on would hand every credential to a third party; `vault audit list -detailed` is the check.
- **State:** the options are written by the bootstrap, **not run**. `VaultAuditFailing` is critical (D194).

## D199 — Log-based Vault alerts in a Loki rule group of their own (#153; **decided, not built**)

- **Decision:** the log rules that metrics cannot give (a sealed pod exports nothing, a hung audit device counts no failure)
  are a group `ryft-vault-logs` in the stack's Loki ruler, counted by `check_rule_groups.sh` (D179). Proposed content, not
  final: `VaultAuditConfigChanged` and `VaultUnsealFailing` (critical); `VaultRootTokenUsed`, `VaultPermissionDeniedBurst`,
  `VaultRaftTrouble` and `VaultAuditLogsMissing` (warning); from #151: any write naming `audit_non_hmac` (critical), and writes
  to `sys/policies/acl/*`, `auth/kubernetes/(role|config)`, `sys/auth/*`, `sys/mounts/*` outside an apply window or by a token
  other than display name `token-terraform` (warning).
- **Not built:** mimirtool cannot check Loki rules; the follow-up needs `lokitool` and a test over lines captured in the kind
  run. Until it exists and the logs are switched on (D197), nothing watches `sys/audit*`, and a use of the root token or of
  generate-root pages nothing today.

## D200 — The import service's operator objects are an Argo CD application (amends D95's waves; the owner; **decided, not built**)

- **Decision:** `deploy/argocd/apps/vault-<key>.yaml`, project `ryft`, wave 4 (ticket V7a). Nothing waits on it. P7's `vault-<x>`
  pattern, the project's `ryft-*` destinations, the controller's ClusterRole and P12 already admit it, so it gains no power.
  The owner's `kubectl apply -k` for it ends; its workload is not in git yet.
- **State:** not built. Acceptance: the app Synced and Healthy with no diff at adoption, before cutover 5i.

## D201 — monitoring's operator objects are an Argo CD application in a project of their own (amends D51, D95; the owner; **decided, not built**)

- **Decision:** `vault-monitoring`, wave 4, project `ryft-monitoring`: destination `monitoring`, and only VaultConnection,
  VaultAuth, VaultStaticSecret and Certificate; no cluster-scoped kind. P7 gets a fixed entry, P12 a carve-out for those four
  kinds in `monitoring`, and renderpolicy binds the project to that path (ticket V7b). The monitoring cutover is then a merge,
  like every other group. **Rejected:** deploying the collector chart on merge through Argo CD or a CI identity.
- **State:** not built; the project is applied by hand (D95), and the admission values change with an apply of
  `infra/aws/kubernetes`.

## D202 — The Grafana Cloud collector is a Terraform add-on in `infra/aws/kubernetes`, which also owns namespace `monitoring` (amends D51, D94's add-on list; the owner; **decided, not built**)

- **Decision:** `helm_release.grafana-cloud` (k8s-monitoring, pinned; values in `infra/aws/kubernetes/values/`), adopted by an
  `import` block, `wait = false`. The namespace (Pod Security `privileged`) moves from `infra/vault` (the owner's `state rm`)
  to `infra/aws/kubernetes` (`import`). The monitoring apply target is removed (ticket V7c). Chart and values changes take the
  owner's apply, like every add-on. DR order: namespace and chart, then `infra/vault`, then Argo CD.
- **Why not Argo CD:** the chart needs privileged host DaemonSets, cluster roles that list Secrets and a mutating webhook; a
  stolen controller token would be node root. A CI deploy identity was rejected: cluster admin, reachable from a public
  repository, and it would keep the API endpoint open to GitHub's address ranges.
- **State:** not built. Until it is, the monitoring apply target stays the way the collector, the injector exclusion (D188) and
  the Vault scrape (D194) reach the cluster.

## D203 — Vault's recovery keys: one custodian, three places, no place holds a threshold (amends D193; #151, the owner)

- **Decision:** five shares, threshold 3, in three places: one password-manager vault and two on paper, one of them off site. The
  split is **2-2-1**; no place holds more than two, so losing any one place leaves three and stealing any one yields at most
  two (with all five in one password manager the threshold protects only against a corrupted item). **The map of which share is
  where is in the password manager, not in the repository.** Paper shares are written from the screen, never through the
  clipboard. PGP-encrypted shares only with two hardware keys.
- **Break-glass** is `vault operator generate-root` through a port-forward (D193). A yearly drill with two sets of three shares
  covering all five proves every share (the page, `VaultRootTokenUsed`, is D199 and not built: until then the drill finds the two
  root logins in the audit log). A lost or exposed place means a rekey the same day.
- **Note:** `docs/work/2026-11-05-vault-ce/addendum-1.md` §3 still names the places; the public docs are generic.

## D204 — Vault runs on on-demand nodes; node maintenance only with a rolled StatefulSet (amends D196; #146, #145, #153, the owner)

- **Decision:** the node group stays `ON_DEMAND` (checked with `kubectl --context ryft-prod-euc1 get nodes -L eks.amazonaws.com/capacityType`); a `check`
  in `infra/aws` (`stateful_quorum_needs_on_demand`, #146) warns on Spot, because two interruptions at once lose the Raft
  (and RabbitMQ quorum-queue) quorum. Updates are `OnDelete`, so a drain recreates a pod from the current template: every chart
  or image change is rolled by hand (standbys first, step-down, the old active last) **before** node maintenance, and
  `VaultUpdatePending` (info) fires after 24 h (#153). Managed node group updates are never forced. The soak drill replaces a
  standby's node, then the leader's, then runs one node group version update.
- **Open, not decided:** the design proposed a PDB of `maxUnavailable: 1` with the default eviction policy. What shipped in
  #145 (D196) is `minAvailable` 2 with `AlwaysAllow`, and #151 asks whether to keep it or move to `IfHealthyBudget`.
  This entry records the shipped one.
- **Consequences:** a manual drain leaves a Vault pod Pending when no other node is in its zone (the zonal volume pins it); a
  NotReady node keeps its pod Terminating (force-delete only once the node is confirmed down); a lost PVC needs `raft
  remove-peer` first.

## D205 — After HCP, the EKS public endpoint admits the operator's addresses only (the owner; **decided, not built**; ticket PR 6c)

- **Decision:** once HCP's TokenReview calls are gone (PR 6), the only outside clients are the operator's tools and local
  Terraform runs. `endpoint_public_access_cidrs` becomes the operator's IPv4 `/32`s plus the NAT gateway addresses, and a
  `check` warns while `0.0.0.0/0` is listed. The precondition is private endpoint access, without which the nodes lose the API.
  `infra/aws` uses only the AWS provider, so a wrong list is fixed from anywhere with AWS credentials (Terraform or `aws eks
  update-cluster-config`). **Rejected:** a fully private endpoint (VPN, bastion) as new moving parts; CI gets no cluster
  credentials.
- **State:** after the decommission (PR 6), as PR 6c, in one reviewed tfvars edit, a plan with one in-place update, the
  owner's apply and a 24-hour watch. Not started.

*D206 is the shutdown of 2026-11-10 (`docs/DECOMMISSION.md`, branch `infra/decommission`; not merged into this
history). The rebuild of 2026-11-13 follows (`docs/REBUILD.md`, `docs/work/2026-11-13-rebuild/`).*

## D207 — The rebuild: the in-cluster Vault is the only Vault (supersedes D191 and the cutovers of D186; amends D187, D190; the rebuild PR)

- **Context:** HCP Vault Dedicated was deleted with the shutdown, and every Terraform state but the zone's is empty. The
  migration machinery of D186 and D191 (dual-write, `write_k8s`, `vso_on_k8s`, cutovers 5a-5i, `vault_diff.sh`'s HCP mode,
  PR 6/6b) has nothing left to migrate from.
- **Decision:** `infra/vault` writes the in-cluster Vault only, from an empty state. There is one provider (the
  port-forward's loopback address, the CA file, `TF_VAR_vault_k8s_token`, pinned against the environment fallbacks), one
  `auth/kubernetes` mount reviewed by Vault's own service account, and one `kv-<svc>` mount, policy and role per service
  (D187). No Vault namespaces, no reviewer JWTs or their ClusterRoleBindings, and no `ryft.dev/vault-namespace` label.
  Every generated VaultConnection names `https://vault.vault.svc:8200`. VSO's peer on 8200 is the variable
  `vault_vso_peer` (the twin of D194's `vault_collector_peer`, default `false`): both stay closed until the operator's
  own init is done and the root token revoked, then open together in one tfvars edit (OPERATIONS → Vault → Bootstrap
  step 11, plan 1 to change), and both go back to `false` before a restore or a lost volume's rebuild. So during the
  init window no other namespace's pod reaches the unauthenticated `sys/init`. The HCP variables are retired with a validation that fails the plan and names the line to delete. Every
  `random_*` keeps its address, so the data key and the demo password can be imported (D211).
- **Order:** the committed overlays are generator output (the repo guard admits no hand edit), so this PR keeps the
  old ones. The user's first `infra/vault` apply (REBUILD step 7) writes the new ones, and a follow-up PR commits them,
  adds back the test that holds them to the generator, and drops HCP's address from `vaultAddresses`
  (`docs/work/2026-11-13-rebuild/followup-overlays.md`). Argo CD's root app is applied only after that merge (step 8).
- **Kept from main:** the add-on (D188), the CA (D190), the audit devices (D198), metrics and alerts (D194), snapshots
  (D192), probes and placement (D196, D204). **Moot:** D199 is still not built; D200-D202 and D205 stand as decided, not
  built.

## D208 — Vault's seal: Shamir by default, `awskms` one variable away (amends D189, D203; **the user confirms**)

- **The user's words:** "I keep the unseal keys local in 1Password." Built as the default: `vault_seal = "shamir"` in
  `infra/aws/kubernetes` renders no seal stanza, no unseal Pod Identity role or association, and no lookup of the KMS
  alias. The server pods have no AWS identity and no egress out of the cluster (vault-extras 0.3.0). The init is
  `-key-shares=5 -key-threshold=3`, and the shares and the initial root token go to 1Password.
- **Consequence:** every Vault pod that starts (node rotation or loss, eviction, OOM) stays sealed until it is unsealed
  by hand with three shares. A sealed pod does not vote, and two sealed pods lose the Raft quorum. The workloads keep
  their synced Secrets (VSO only syncs), so the users notice nothing, but nothing syncs or rotates until the quorum is
  back. The PDB (`minAvailable` 2 of ready pods) stops a second drain, so node maintenance needs the operator.
  `VaultSealed` pages for every restart.
- **Custody (amends D203):** an unseal needs three shares each time, so 1Password holds a threshold (one item per share).
  The paper copies are the backup against losing 1Password. Whoever has 1Password (three shares) and any path to 8200
  (a port-forward, a pod or a node that reaches it) has Vault. D203's 2-2-1 split stays right for `awskms`, where the
  shares are recovery keys and rarely used.
- **What Shamir does not protect:** a running, unsealed Vault holds its keys in the server's memory, so node root and
  privileged hostPID pods (alloy-daemonset, G2) can read them under either seal. Shamir protects a stopped or sealed
  Vault and its volumes and snapshots: nothing outside the shares opens them.
- **The alternative, built (D189):** `vault_seal = "awskms"`, one `infra/aws/kubernetes` apply (the role, the association,
  the stanza and the KMS egress), then `vault operator unseal -migrate` pod by pod. After that the shares are recovery
  keys. The unseal key stays in `infra/aws` under Shamir (about $1 a month), so the switch needs no `infra/aws` apply.
- **Why Shamir by default:** it is what the user asked for. It also takes the parts of D189 that were never verified
  (Pod Identity reaching the seal on 2.2.1, the session-tag trust) off the rebuild's critical path, and no AWS
  administrator can unseal Vault. **Recommendation:** Shamir for the rebuild. Move to `awskms` if unseals by hand
  become a burden (more than one a week, or nights).

## D209 — The operator's login without Google Workspace: a generate-root session, no OIDC (amends D193; **the user confirms**)

- **Decision:** there is no standing login. A session is `vault operator generate-root` with three shares (an OTP; the
  token decoded locally), then a one-hour orphan admin token bound to `127.0.0.1` (token role `admin`,
  `bootstrap/token-role-admin.json`), and the root token is revoked at once. The terraform token is the admin token's
  child (token role `terraform`). In the audit stream every session is a root token followed by one admin token, and
  any other pattern is an incident.
- **The admin token cannot outlive its session** (`bootstrap/admin.hcl`, pinned by an exact-allowlist test): it denies
  `auth/token/create`, `auth/token/create-orphan` and `auth/token/create/*` except `auth/token/create/terraform` (a
  deny of the glob, because ACL paths match case-sensitively and a deny of `create/admin` would let `create/ADMIN`
  through), every `auth/token/roles/*`, `sys/auth/*` and `sys/remount` (a moved auth mount would escape the blocks
  below), and it may only read and list policies (with `path "*"` a policy write would let it lift its own denies),
  every auth method's roles and config (`auth/+/role/*`, `auth/+/config`: a Kubernetes role with `token_policies =
  ["admin"]`, or a re-pointed `kubernetes_host`, is a login that outlives the session), and `identity/*` (an entity or
  group with policies). It saves a snapshot but restores none (`snapshot-force` denied, the plain restore read-only:
  either brings back or hands over who logs in). A tune of an auth mount stays: it changes the lifetime of a login that
  exists, capped by each role's `token_max_ttl`, and adds none. Writing any of the others is a root token's, in a
  generate-root session before it mints the admin token: the bootstrap, adding a service (the policy `terraform`), the
  403 repair and the disaster recovery (its throwaway root token) say so. Found by the security re-review of the PR.
- **Trade-off:** about two minutes of ceremony per session, and no credential at rest besides the shares. It needs a
  port-forward, so cluster access as well.
- **Alternatives, each a reviewed change before use:** (1) the initial root token kept in 1Password: simpler, but a
  standing credential that never expires and works without any share; (2) OIDC with a personal Google account later
  (the `oidc-*.json` files stay as that option, now without the `hd` binding and needing a Google Cloud project of the
  user's own); it adds Vault's egress to Google. **Recommendation:** the session, as built.

## D210 — The hard gates for a rebuild "as it was" (amends the gates of the Vault CE plan; **G2 is the user's**)

- **Blocking:** G3 (never `vault login` through `kubectl exec`; `Initialized true` before the user's own init, or an
  `already initialized` init, means stop and wipe, and a follower that does not show vault-0's cluster identity gets no
  share) and G4 (`prevent_destroy` on the namespace and both releases, in the code; and on `random_bytes.data_key`,
  D211).
- **Blocking procedure in G1's place: a verified pod.** The security review found the gap G1 leaves: without admission in
  Deny, a pod created in `vault` with Vault's labels joins `svc/vault-active`'s endpoints, and a share or a token sent
  there goes to it. So no share and no token goes anywhere but a port-forward to `pod/vault-N`, checked first to be
  controlled by the StatefulSet `vault` (by uid), to run only the pinned image digest and, for a login or Terraform, to
  carry `vault-active=true`, and checked again after the forward starts (OPERATIONS → Vault → **A verified pod**, the
  session shell's `vault_forward`). The two peers stay closed until the init is done (D207).
- **Not blocking, follow-up:** G1, the admission layer in Deny for `vault`. In production the chart was disabled before
  the shutdown (`enabled: false`), and under Shamir no pod can reach a seal key or decrypt the Raft data. An Argo CD
  token could already create a pod in any `ryft-*` namespace and mount its `ryft-secrets`. Follow-up: the chart on in
  Audit, then the `vault` bindings and P13 in Deny (they can go alone), then #121's P12. **Recommendation:** do that
  follow-up soon after the testers are back, and before `vault_seal = "awskms"` (then G1 blocks again: a pod in `vault`
  as the service account `vault` reaches KMS). Putting the bindings in Deny before step 7 would be safer still, but the
  chart was disabled in production before the shutdown, so switching it on and into Deny would add a second unknown to
  the rebuild's critical path.
- **The user's call:** G2, the monitoring stack inside Vault's trust boundary (alloy-daemonset privileged with hostPID,
  alloy-operator and VSO reading Secrets cluster-wide). **Recommendation:** accept it for the rebuild, with D197's Fleet
  exclusions of `vault` (pod logs, Beyla) checked before the collector starts (REBUILD step 9). The Art. 32 description
  must not claim Vault is isolated from the monitoring vendor.

## D211 — The data key and the demo password come back from the old state (**the user confirms**)

- **Decision:** `scripts/recover_data_key.sh` reads the newest state version of `ryft-prod-euc1-vault` that still holds
  `random_bytes.data_key["api"]` (and with `--demo`, `random_password.demo`). It goes through the HCP Terraform API
  (the token and every request URL, the signed state download URL included, in curl's stdin config, never in argv) and
  writes a 0600 file, after `tmutil addexclusion` on its directory on macOS (a local snapshot taken while the file exists
  still keeps it for about a day). It prints a version, a date, a fingerprint and the key's length, never the value. A
  404 says that the workspace is gone **or** hidden from this token, and the user confirms in the HCP Terraform UI
  before treating the key as lost. `random_bytes.data_key` has `prevent_destroy` (the state is its only copy after the
  import); the demo password does not, so it can still be rotated with `-replace`. The user imports both values before the first plan, with the
  output sent to `/dev/null`, and compares fingerprints (OPERATIONS → Vault → The first apply). The values then live in
  their resources' state, with nothing to remember on later applies. The stored Apple tokens keep decrypting, and App
  Store Connect's demo login stays valid.
- **If the versions are gone:** the plan creates a new key. A one-off statement in the SQL editor (REBUILD step 8, before
  the api starts) deletes the tokens sealed before 2026-11-11 and clears them from tombstones, so the reconcile loop no
  longer retries revocations it cannot decrypt (an ERROR per tombstone every 15 minutes, and `AccountDeletionIncomplete`
  without end). A tester's token comes back at their next interactive Sign in with Apple. Accounts deleted before then
  are not revoked at Apple, and the tester can remove the grant in iOS Settings. **No migration:** a migration would run
  in every database, including those whose key is fine.
- **Recommendation:** recover. The fallback is the cost of losing it.

## D212 — No mail for ryftapp.de until a provider is chosen (amends D142; **the user picks the provider**)

- **Decision:** the mail records in `infra/aws/dns` are variables whose default says "no mail": a null MX `0 .` (RFC
  7505), SPF `v=spf1 -all`, DMARC `v=DMARC1; p=reject; adkim=s; aspf=s` (no `rua`, since no mailbox exists), and no
  DKIM and no site verification. Google Workspace's values are the example in `terraform.tfvars.example`. From the
  current state the plan is 0 to add, 3 to change, 1 to destroy (the DKIM record). Nobody can send mail as the domain,
  and every mail to it bounces at once, instead of reaching a deleted mailbox.
- **Consequence:** the Impressum (§5 DDG) and the privacy policy name `support@ryftapp.de`, so mail must work again
  before, or soon after, the testers are back. Supabase Auth SMTP, the budget alerts and `acme_email` need an address
  that receives mail. A new provider is one tfvars block and an apply, with DMARC starting at `p=none` and a real `rua`.
- **Update 2026-11-14 (D213, D214): the mail decision is made.** `support@ryftlab.de` runs on Microsoft 365 through
  GoDaddy, live since 2026-11-14, on the new domain. Its records are code in `infra/aws/dns/mail.tf`; the no-mail
  default and its variables are gone, and so is the old zone.

## D213 — The domain is `ryftlab.de`, a hard cut (**the user's decision**, 2026-11-14)

- **Decision:** `ryftlab.de` replaces `ryftapp.de` everywhere, before any apply of the rebuild (PR #161): the hosts
  `api.ryftlab.de`, `argocd.ryftlab.de` and `rabbitmq.ryftlab.de`; the contact `support@ryftlab.de`;
  `PRIVACY_POLICY_URL` `https://ryftlab.de/privacy` (base `config.env` and the code default); the push payloads' links,
  the invite link base and the operator page's origin (`ADMIN_WEB_ORIGINS`, CORS). **A hard cut:** no alias, no
  redirect, no AASA on the old domain. Both domains stay registered at GoDaddy. `ryftlab.de` is delegated to a new
  Route 53 zone. `ryftapp.de` keeps GoDaddy's nameservers and leaves Terraform: the old zone `Z08455293REDHBC9QW5MZ` and
  its six records are destroyed by the next DNS plan (the block `aws_route53_zone.this` is removed, and
  `prevent_destroy` guards only a configured block). The new zone is `aws_route53_zone.ryftlab_de`, again with
  `prevent_destroy`. ExternalDNS's and cert-manager's IAM stays scoped to one zone, the one `var.domain` names in
  `infra/aws/kubernetes` (now `ryftlab.de`, looked up by name).
- **Legal texts:** only the domain and the contact change, in the text in force (2026-10-26), the Impressum and the
  support pages. No new `PRIVACY_POLICY_VERSION`; `status` and `effective` stay. The archived 2026-10-20 text keeps its
  words, as the record of what was consented to. D214 names what this leaves wrong.
- **Kept on purpose:** the scoring golden generator's UUID namespace seed (`golden_gen_test.go`) is a constant string,
  not a host; changing it would move every golden ID. Dated history (this file, changelogs, `docs/work/`, `web/docs/`)
  keeps the old name.
- **Consequences, outside this repository:** the app needs a build whose base URL is `https://api.ryftlab.de`, that
  accepts only `ryftlab.de` hosts in links and names `ryftlab.de` in its associated domains; the TestFlight build from
  before does not reach the rebuilt production. The demo account is `review@ryftlab.de` (`seed_demo.sh` creates it;
  App Store Connect's review login changes). `SMOKE_EMAIL` may stay as it is (a Supabase sign-in needs no mailbox).
  Supabase's Site URL, the repository variable `SUPPORT_EMAIL` and App Store Connect's URLs move to the new domain.
- **Order** (REBUILD step 1): the DNS apply, every record dug against the new nameservers, the nameservers set at
  GoDaddy, `dig NS ryftlab.de @a.nic.de` shows Route 53, then the rest of the runbook.

## D214 — Mail on Microsoft 365 through GoDaddy, its records as code (amends D212; **the user's decision**, 2026-11-14)

- **Decision:** `support@ryftlab.de` runs on Microsoft 365, bought through GoDaddy, and is live. `infra/aws/dns/mail.tf`
  publishes its records exactly as GoDaddy's DNS shows them: MX `0 ryftlab-de.mail.protection.outlook.com.`, one apex
  TXT set (`NETORGFT21311797.onmicrosoft.com` and `v=spf1 include:secureserver.net -all`), DMARC `p=quarantine` with
  relaxed alignment and reports to `dmarc_rua@onsecureserver.net` (GoDaddy), the DKIM CNAME `selector2._domainkey`,
  `autodiscover`, `email`, `lyncdiscover`, `sip`, `msoid` and the SRVs `_sip._tls` and `_sipfederationtls._tcp`. They are
  public, so they are code, not tfvars; the domain is a local too. CAA stays Let's Encrypt only, now as one set at the
  apex with `issue` and `issuewild` (security review of the domain diff). The old zone's `*.` CAA set is not carried
  over: under RFC 8659 it answered for every name that does not exist, and since it held only `issuewild`, which a CA
  ignores for a normal certificate, those names were unrestricted.
- **Left out:** GoDaddy's parking A records on the apex, `www` (no web, D215), and `selector1._domainkey`, which did not
  resolve on 2026-11-14. The user checks it at GoDaddy; adding it is one commented line in `mail.tf`. Until then a day on
  which Microsoft signs with selector1 fails DKIM (DMARC can still pass on SPF's alignment).
- **Plan:** `infra/aws/dns` 13 to add (the zone, CAA, MX, apex TXT, DMARC, six CNAMEs, two SRVs), 0 to change, 7 to
  destroy (the old zone and its six records). Counted from the code and the old state, not planned.
- **Open, the owner's legal call:** the privacy policy in force (2026-10-26, §5 "Email") says that what is sent to the
  support address is received and stored by Google Workspace. Since this decision it is Microsoft (through GoDaddy),
  and GoDaddy receives the DMARC reports. Correcting that is a change of the text's meaning, so a new version and
  everyone's consent again (`web/CLAUDE.md`); this change does not make it ("only the domain and the contact"). For
  that text the review names what to settle first: GoDaddy is the contracting processor (its DPA, with Microsoft as
  sub-processor); where the tenant stores mail, and on what transfer basis, is unverified, and support mail can carry
  health data (Art. 9). Mail sent to the old support address is no longer received; deletion and export in the app
  still cover Art. 17 and 20.
- **The old domain** (security review): it leaves Terraform, so its safety rests on GoDaddy. Before the destroy, its
  delegation must be GoDaddy's, not the old zone's awsdns servers, or the delegation dangles and can be claimed.
  No-mail records and the transfer lock at GoDaddy are the user's (REBUILD step 1).

## D215 — No web frontend in the rebuild, only the services (**the user's decision**, 2026-11-14)

- **Decision:** `deploy/k8s/overlays/eks` no longer lists `../../services/web` and `web.yaml`. Not deployed: the
  namespace `ryft-web`, the site's pods, the apex and `www` Ingress, their certificate, ExternalDNS's records for them
  and the AASA. The manifests and `web/` stay, on `ryftlab.de`; the image is still built and promoted, and its pin stays
  in the overlay. Adding the two lines back deploys the site (REBUILD → **Optional: the web pod for the legal pages**).
  The user has not chosen a host for the legal pages.
- **Checks kept with the smallest changes:** `check_policy_in_force.sh` reads the overlay's web pin when no web
  Deployment renders and still requires that commit to serve the version in force; `check_registry.sh` allows web to be
  pinned without being rendered and still asks the registry for the pin; the CEL harness appends the `ryft-web`
  Namespace file for P1's cases (as the kind harness already did); `make k8s-validate` validates `services/web` and
  `web.yaml` on their own. P1 still lets Argo CD write the `ryft-web` Namespace.
- **Consequences:** `https://ryftlab.de` has no address. `privacy_policy_url` and the Impressum do not load until a
  host serves them, so the app's links to them fail; no universal links and no password AutoFill association (no AASA);
  the invite links do not open the app. The site's Synthetic Monitoring checks stay unapplied, and the `ryft-site`
  rules (with `SyntheticsMissing`) are silenced until the site is back (REBUILD step 10). Consent to a text nobody can
  open, and an Impressum that is not available (§5 DDG), are not acceptable for anyone but the owner, so REBUILD step 12
  stops before any tester or App Review signs in until `/privacy`, `/de/privacy` and `/impressum` load somewhere.

## D216 — RYFT runs on GCP serverless, its Terraform in HCP org `ryft-lab` (**the user's decision**, 2026-11-14)

- **Decision:** RYFT moves from EKS to GCP serverless. That means Cloud Run, Pub/Sub, Secret Manager, Cloud Scheduler,
  Artifact Registry and Cloud DNS, in project `ryftlab-prod` (no organisation, billing on). The site goes on Firebase
  Hosting in a project of its own, `ryftlab-site` (D225). Nothing stays in the HashiCorp AWS account.
- **Terraform:** it runs in HCP Terraform org `ryft-lab` (`org-aus6gvWumndxZixQ`, the user's own account), with
  CLI-driven workspaces, remote execution and GCP dynamic credentials. The old org `tim-krebs-org` is used only for the
  retirement, one prefixed command at a time.
- **The design:** `docs/work/2026-11-14-gcp/design.md`, accepted by the user the same day (§13).
- **Why:**
  - Since 2026-11-14 an SCP of the HashiCorp org denies `eks:CreateCluster` in account 173003892479.
  - EKS cost about $400–450 a month, and that cost caused the shutdown on 11-10 (D206).
  - In the user's words: "für eine pre-alpha absolut ausreichend". The target is a few euros a month for about ten
    testers.
- **Unchanged:** Supabase (Postgres and Auth, Frankfurt), Grafana Cloud, the Apple keys, the domain `ryftlab.de`
  (D213, D214), the contract's shapes (`docs/API.md`), the outbox (D101) and idempotent handlers (D100).
- **Who decided what (§13):** the platform, the project and the org are the user's. Putting the site in a separate
  project without billing is head-architect's default, and the user can still choose `ryftlab-prod`.
- **Consequences:**
  - PR A1 replaces RabbitMQ with Pub/Sub (D218) and every timer with one tick (D219).
  - A2 brings the Cloud Run runtime and writes D220–D227.
  - B prepares `infra/gcp` and `deploy.yml`, applied by the user only.
  - D3 removes Kubernetes, Argo CD, Vault and `deploy/rabbitmq` once production on GCP passes the smoke (design §10).
  - The decisions of design §10's list are marked superseded or amended by the PR that makes them false.

## D217 — europe-west1 with Cloud Run domain mappings, two API hosts, `min-instances 0` (**the user's decision**, 2026-11-14)

- **Decision:**
  - **Region:** everything regional runs in europe-west1: Cloud Run, Pub/Sub's message storage, the secrets,
    Artifact Registry, Scheduler and the log bucket.
  - **Domains:** the custom domains are Cloud Run domain mappings, which are Preview.
  - **Two API hosts:** `https://api.ryftlab.de` (api) and `https://ingest.ryftlab.de` (ingest, `POST /v1/sync`). The
    app's ingest URL becomes required (`API.md` → Base URLs; a documentation change, no shape change).
  - **Scaling:** `min-instances 0` on every service.
  - **Accepted gaps:** design §5.2's gaps are accepted for the pre-alpha: no Cloud Armor, TLS 1.0 and 1.1 on the
    mapped hosts, no organisation, the one Grafana SA key, request logs at Google, unrestricted egress, and rate
    limits per instance.
- **Why:**
  - The user first said Frankfurt, then chose west1 once he saw the cost. europe-west3 has no domain mappings, and
    its alternative, a global load balancer (~$18 a month *(unchecked)*), would cost several times the rest of the
    bill. west1 is also Tier 1 pricing.
  - A mapping points a host only at `/`, so Traefik's `/v1/sync → ingest` on one host has no equivalent. Hence two
    hosts.
  - The price: a sync about 0.15 s slower and scoring a few days about a second slower *(estimate, unmeasured)*.
    Both stay inside the sync-to-score target (p95 2 min).
- **Who decided what (§13):** the region, the mappings and the two hosts are the user's answers, and so is the
  Grafana SA key. `min-instances 0` and the other gaps of §5.2 are head-architect's defaults, which the user can
  still object to.
- **Revisit if** (design §1):
  - a mapping fails or is withdrawn, or its latency hurts. Then move to europe-west3, or stay in west1, either way
    behind a load balancer;
  - Supabase moves;
  - scoring latency starts to matter (a rescore of many days).
- **Consequences:**
  - Cold starts take roughly 0.5–1.5 s after idle *(unmeasured)*. The app already retries 503s.
  - With request-based CPU nothing runs between requests, so every timer becomes a tick (D219).
  - The 32 MiB sync needs HTTP/2 end to end (D220, A2). Runbook step 9 tests it.

## D218 — Events on Pub/Sub: a topic per key, push with OIDC, a dead-letter topic per consumer (supersedes D97, D102, D104 and D98's AMQP TLS; amends D17, D99, D150; PR A1)

- **Decision:**
  - **Topology:**
    - One topic per event key: `ryft-ingested`, `ryft-session-completed`, `ryft-scored`, `ryft-session-scored`,
      `ryft-refresh`.
    - Each publisher service holds `roles/pubsub.publisher` on its own topics only (D99's matrix).
    - Two more topics: `ryft-notify-wait` and `ryft-tick` (D219).
    - A dead-letter topic per consumer, `ryft-dlq-scoring` and `ryft-dlq-notify`, each with a pull hold subscription
      that keeps letters 14 days.
    - The topology is defined once, in Go (`internal/events/pubsub/topology.go`). `go run ./cmd/pubsub-topology` writes
      `infra/gcp/pubsub.auto.tfvars.json` and `deploy/pubsub-emulator/init.sh`, and `-check` fails on drift.
  - **Delivery:**
    - Push subscriptions to each service's `run.app` URL, with an OIDC token of `ryft-push` and `oidc_token.audience`
      set explicitly to that URL.
    - Retry policy 10–600 s. For push, Pub/Sub may use its push backoff instead.
    - 20 delivery attempts, then the consumer's DLQ.
    - Per-user ordering (`ordering_key = user_id`) on scoring's two topics, published through the regional endpoint
      `https://europe-west1-pubsub.googleapis.com`.
  - **Transport:** `internal/events/pubsub`, stdlib only. It publishes over REST with a token from the metadata
    server, and uses no Google client library. `amqp091-go`, `internal/events/amqpbus` and `cmd/rabbitmq-topology` are
    deleted. `deploy/rabbitmq` stays until D3.
  - **Interface:**
    - `events.Handler` is `func(ctx, key, data) error`, called by the push handler.
    - `Bus` has no `Ready` and no `Close`: a REST publish holds no connection.
    - The status mapping:
      - `nil` → 204.
      - `Permanent` → 204, dropped.
      - Any other error or a panic → 500.
      - The new `events.Bad` → the consumer's own DLQ topic with `ryft-error`, then 204, or 500 when that publish
        fails. A decode failure is `Bad`, and so is a `ryft-key` that isn't the push path's key (filed under the
        path's key).
      - `Defer` → an outbox row with a not-before time (D219), then 204.
  - **The token check (the messaging part of D222):**
    - It runs in the app on every `/push/*` and `/internal/*` of all four services: RS256 only, `exp` required.
    - `iss` must be `https://accounts.google.com` or `accounts.google.com`, which Google's own verifiers also accept.
    - `aud` must equal `PUSH_AUDIENCE` exactly, never the `Host` header.
    - `email` must equal `PUSH_SA_EMAIL`, with `email_verified` true, and `sub` must equal `PUSH_SA_ID`.
    - The keys are Google's JWKS, through `internal/auth`'s keyfunc, not the Supabase parser.
    - A refusal is a 401, or a 503 while the token cannot be checked: no keys loaded, or an unknown key id while
      the last fetch failed. Refusals count in `ryft_auth_failures_total` with the existing reasons.
    - While no keys are loaded, a token fetches them first: at most once every 10 s per instance, within the
      request's deadline and at most 5 s. A cold instance (`min-instances 0`) whose first fetch failed would
      otherwise answer 503 until the hourly refresh, and Pub/Sub would dead-letter the pushes.
    - An unknown key id after the minute's refresh is a 401 `unknown_key` while the last fetch worked, not a 503
      `jwks_refresh_limited`: anyone can send one to the public api and ingest, and a 503 would feed the
      availability alerts. Pub/Sub retries a 401 as well, so a real rotation inside that minute is delivered on a
      later attempt. The Supabase verifier keeps its 503 (A2's runtime ticket).
    - On api and ingest it is the only gate. On scoring and notify it is the backstop behind ingress `internal` and
      Cloud Run IAM (`roles/run.invoker` for `ryft-push` only).
    - `config.Load` refuses `ALLOW_INSECURE_PUSH=true` and `PUBSUB_EMULATOR_HOST` when `K_SERVICE` or `CLOUD_RUN_JOB` is
      set, and a `PUBSUB_ENDPOINT` that is not `https://<name>.googleapis.com` (no port, path or userinfo): the
      access token goes with every publish.
  - **What a message holds and what is taken from it:**
    - No health value in a body. `scored` is `{user_id, day, request_id}`; notify reads the score when it sends.
      A message sits in Google's Pub/Sub up to 7 days, in a dead-letter hold 14 and in notify's waiting rows until
      quiet hours end. Older images' `scored` messages, with the values, still decode.
    - The envelope is the pusher's: a `messageId` that is not 1 to 32 decimal digits is a 400, the subscription
      and a `ryft-key` that is not a key are cut in log lines, and a deferral keeps at most 256 bytes of
      `ryft-message-id`.
    - The `reason` label of `ryft_events_handled_total` is the event's reason only when it is one of
      `events.Reasons`, else `unknown`: an ingested event's reason is in its body.
    - A send no retry can deliver is `events.Rejected` (no topic for the key, an ordered event without a user,
      Pub/Sub's 400, 403, 404 or 413); the outbox sets its row aside at once (D219).
- **Why:**
  - RabbitMQ needs a cluster: three nodes and two operators (D97, D104). The services no longer run on one (D216).
  - Pub/Sub grants publishing per topic, not per attribute, so one topic per key keeps D99's rule ("ingest cannot
    publish `session.completed`") without a broker.
  - Push needs no consumer loop, which request-based CPU wouldn't run (D219).
  - REST on the stdlib adds no dependency, which this repository's rule asks for.
- **Kept:**
  - Events are triggers, and the outbox stays (D101).
  - Idempotent handlers (D100). Pub/Sub delivers at least once and can redeliver after an ack, and exactly-once
    delivery isn't available for push. `ryft-message-id` stays stable per trigger, and nothing dedups on it.
  - `Permanent` is dropped, not dead-lettered (D97's deviation).
  - The key rule: the path's key, which the publisher can't change.
  - D150's replay rule. The marker is `ryft-replayed=true`, and `scripts/dlq.sh` runs on `gcloud`.
  - The name `ryft_nats_redeliveries_total`.
- **Alternatives:**
  - One topic per consumer with the key as an attribute: one binding would let ingest publish any key.
  - Pull subscriptions: they need CPU between requests, so instance-based billing (D217, D219).
  - Exactly-once delivery: not available for push.
- **Consequences:**
  - The retry spacing isn't ours to fix (push backoff), and the attempt count is best effort.
  - Pub/Sub's own dead letters carry no `ryft-error`. The last error is in the consumer's log, by `messageId`.
  - With a dead-letter topic, order "might not be" kept, because forwarding is best effort.
  - A 429 from a saturated scoring counts as an attempt. `PushBacklog` alerts first.
  - A captured token is valid for its hour, and D100 makes a replay harmless.
  - The emulator checks no IAM and no OIDC: Terraform tests pin the matrix, and Go tests with signed tokens pin the
    check.
  - `ryft_amqp_*` and the `unroutable` result are gone. A key without a topic is an `error`.
  - D17 becomes "scoring max-instances 1, one message in flight per user" (concurrency 4, set by the Cloud Run
    runtime, A2/B).
  - Details: `MESSAGING_DESIGN.md`, design §2.

## D219 — One Scheduler tick replaces every timer; the outbox relays inline and has a not-before time (amends D101; PR A1)

- **Decision:**
  - **The tick:**
    - Cloud Scheduler job `ryft-tick`, every 15 minutes, publishes `{}` with `ryft-key=tick` to topic `ryft-tick`.
    - A push subscription per service delivers it to `POST /internal/tick`, whose token is checked as on a push (D218).
    - The service's steps run in order within a budget: api, ingest and notify 45 s, scoring 240 s.
      - api: db ping, outbox sweep, account reconcile, invite settings refresh, invite email purge.
      - ingest: db ping, outbox sweep.
      - scoring: db ping, outbox sweep, the scoring pass and housekeeping.
      - notify: db ping, outbox sweep, the stale check.
    - The answer is 204 after auth, whatever a step did. A tick that arrives while one runs in the same process gets
      204 and counts as `overlap`. `ryft_ticks_total{service,result=ok|error|overlap}` is counted before the final hook
      (A2's metrics push).
    - A failed tick isn't retried: its subscription keeps a message 10 minutes and has no DLQ.
  - **No timers in the services:** no tickers, no `db.Watch`, no outbox poll. The tick's db ping sets `ryft_db_up` and
    `ryft_db_ping_failures_total`.
  - **No Supabase call at start:** the api's invite settings refresh is a tick step, plus one lazy refresh on the
    first bound invite. Bound invites are refused until a refresh succeeds (fail closed). A tick's read that the
    tick's budget cuts short records nothing (as the db ping), so it neither makes the state unknown nor spares the
    next request its lazy read.
  - **The outbox, inline:**
    - `Flush` runs after the commit and before the answer, with a 2 s budget, detached from the request's cancel. It
      replaces `Kick`. `Send` is `Insert` plus `Flush`.
    - `Sweep` runs in the tick.
    - `Run` is for tests and local tools only.
    - A failed send stops the drain and is recorded in `last_error`. Only a `Sweep` counts it as an attempt
      (`MaxAttempts` 50, about 12 hours of ticks): a `Flush` runs per request, and during a Pub/Sub outage fifty
      requests would set the head row aside in seconds. An `events.Rejected` send sets the row aside at once, from
      either, and the rows after it go in the same drain.
  - **Order:**
    - The drains of each publisher are serialised with `pg_advisory_xact_lock(7419006, hashtext(publisher))`, with
      `lock_timeout` taken from the drain's budget. Both are transaction-scoped, as the transaction pooler requires.
      So one user's rows go out in id order across goroutines and instances.
    - A `Flush` that loses the race waits for the lock within its 2 s budget, then takes what is due, its own row
      included.
    - `FOR UPDATE SKIP LOCKED` stays, so an older binary in a rollout never sends a row twice at once.
  - **Not before:**
    - Migration `0034_outbox_available_at.sql` adds `outbox.available_at timestamptz NOT NULL DEFAULT now()`.
    - Only due rows are drained, and only they count in `ryft_outbox_pending` and `ryft_outbox_oldest_seconds`.
    - The age is measured from the later of `created_at` and `available_at`.
    - notify's deferral is a row of publisher `notify`, due at now + d. notify's tick sweep relays it to
      `ryft-notify-wait` under its original key, and `/push/wait` hands it back to the handler.
    - 0034 also widens the publisher CHECK to `notify`, adds the index `outbox_due`, and grants `ryft_notify` the
      outbox with an RLS policy `publisher = 'notify'`.
  - **Local:** compose runs the same runtime as Cloud Run (the emulator, `ALLOW_INSECURE_PUSH=true`, a ticker every
    60 s), not a second mode.
- **Why:**
  - With request-based billing, "CPU is allocated when instances are processing requests". A timer goroutine, or a
    relay after the response, may never run (design §3).
  - Pub/Sub has no per-message delay, and backing off counts as attempts, so RabbitMQ's wait tiers have no
    equivalent. The outbox already has rows, a relay and a sweep.
  - With inline flushes, `SKIP LOCKED` alone let a second drain send a later row while the first still held an earlier
    one.
  - The due-only gauges: a deferral that came due after a night of quiet hours is no backlog of hours.
- **Alternatives:**
  - `min-instances 1` with instance-based billing for CPU outside requests: several euros a month *(unchecked)*.
  - A Scheduler job per timer: the free tier has three jobs per billing account (design §7). One job fanned out
    through a topic costs nothing.
  - Deferring by nack: every back-off is an attempt, and a long quiet night would dead-letter the push.
- **Consequences:**
  - Precision is the tick. A deferred push can be up to 15 minutes late; the knob is a second Scheduler job every 5
    minutes for `tick-notify`.
  - The invite email purge runs every 15 minutes instead of hourly. It is idempotent.
  - The old jobs' state in memory (`learner.tried`, `catchUp.failed`, `catchUp.next`) is per instance. A failing user
    is retried every tick instead of once a day, and a pass starts from the first user. That is harmless at ten users,
    and a `job_state` table would fix it.
  - A row whose `Flush` failed waits for a tick.
  - Absence is a signal: without ticks there is no sweep, no scoring pass and no `ryft_db_up`. `TickMissing` reads
    `ryft_ticks_total`.
  - Rollback: 0034 is additive, and older images ignore `available_at`. But no older image relays publisher `notify`,
    so notify's waiting rows stay unsent until the images come back.
  - `Grace` stays 30 s in A1. D169's 10 s comes with A2.
  - Details: `MESSAGING_DESIGN.md` → The tick, The outbox.

## D220 — The Cloud Run HTTP runtime: h2c, listen before the pool, `/health/*`, Grace 10 s (amends D169; PR A2)

- **Decision:**
  - **HTTP/2 end to end.** `httpx.Server` serves HTTP/1.1 and h2c with prior knowledge on one port
    (`http.Server.Protocols`, stdlib). Cloud Run runs `--use-http2` (the port is named `h2c` in `deploy/run`), so a
    32 MiB `POST /v1/sync` isn't cut by the front end's HTTP/1 limit. `httpx.MaxBodySync` is unchanged
    (`TestH2CCarriesAFullSync`). Runbook step 9 sends 33 MiB through the mapping and must get our JSON 413.
  - **All four services on `internal/app`.** api and ingest hand app their whole mux (`Parts.Handler`), so one start
    and one shutdown cover every service.
  - **The port opens before the pool.** Order: settings (OTLP's too), the transport (its one network call is the first
    fetch of Google's JWKS, at most 10 s, so the port may open that late, within the startup probe's 20 s), the
    listener, then the pool, the outbox and `Build`, and the start gate opens (`app/gate.go`). Until then:
    - `/health/live` and `/healthz` answer `200 {"status":"ok"}`: Cloud Run's startup (1 s × 20) and liveness probe.
    - The ready paths answer 503 (`{"error":"starting"}` locally, `{"error":"not ready"}` on Cloud Run).
    - Any other request waits up to 5 s (and its own context), then gets `503 {"error":"temporarily unavailable,
      send the request again"}` with `Retry-After: 5`.
    - A tick that waits in vain checks its token, sets `ryft_db_up{service}` 0, counts a failed tick, pushes the
      metrics and gets that 503, so `DatabaseDown` fires during a Supabase outage instead of no instance and no signal.
  - **Health paths.** `GET /health/live` and `GET /health/ready` have `/healthz`'s and `/readyz`'s handlers; the old
    paths stay for compose. With `K_SERVICE` set, both ready paths answer `503 {"error":"not ready"}` without
    `Retry-After`, log the cause at warn, and reuse their answer, ok or not, for 5 s.
  - **Grace 10 s.** Cloud Run's SIGTERM-to-SIGKILL. The watchdog fires at 9 s. The budget:

    | step | api, ingest | scoring, notify |
    |---|---|---|
    | http (Shutdown + wait) | 3 + 1 s | 2 + 1 s |
    | metrics push | 2 s | 2 s |
    | pool | 1 s | 1 s |
    | `/metrics` (local only) | 1 s | 1 s |
    | worst case | 8 s | 7 s |

    When Shutdown runs out, every handler's context is cancelled, so a push or tick in flight gives its pool
    connection back before the pool closes (review-a1 finding 6, `TestAnInFlightRequestGivesItsConnectionBack`).
    There is no drain delay: Cloud Run stops routing to an instance before SIGTERM.
- **Why:**
  - Cloud Run's quotas: 32 MiB for an HTTP/1 request, no limit over HTTP/2 (design §3).
  - Cloud Run's known issues: avoid paths ending in `z` (users report Google's own 404 on `/healthz`).
  - A startup probe on `/health/ready` would let no cold instance start during a Supabase outage, so no tick would ever
    report `ryft_db_up 0` (ticket B's review).
  - A public `/readyz` showed `err.Error()` to the internet, and every hit costs a pooler round trip.
- **Alternatives:** a 31 MiB cap (a contract change, the fallback if step 9's 413 isn't ours); a load balancer
  (paid); keeping the listener last (the startup probe would cover the database wait, see Why).
- **Consequences:** `docs/API.md` 2026-11-15: two health paths and the start's 503, additive. The early tick's 503
  is a nack; the tick subscription keeps a message 10 minutes, so a failed tick is dropped (D219).

## D221 — The transaction pooler, `exec` mode and `verify-full`; migrate on session mode (PR A2)

- **Decision:**
  - **Services on Supavisor's transaction pooler (6543)** with `default_query_exec_mode=exec`; `migrate` on session
    mode (5432). The URLs are `ticket-b.md`'s, rendered from `deploy/run/config.env`.
  - **TLS verified:** `sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt`. The Supabase Root 2021 CA is
    public and baked into every image (`backend/certs/supabase-ca.crt`, sha256 `70072358…2c3b7`, valid until
    2031-04-26). `internal/db`'s `ca_test.go` pins the hash and fails 90 days before the expiry.
  - **`DATABASE_URL` has no password**; it comes as `PGPASSWORD` from the role's secret (pgx reads it).
  - **`db.Open` refuses on Cloud Run** (`K_SERVICE` or `CLOUD_RUN_JOB`): a password in the URL, any `sslmode` but
    `verify-full`, no `sslrootcert`. A service (`K_SERVICE`) also needs port 6543, `exec` mode and no startup parameter
    except `application_name` (PgBouncer and Supavisor refuse unknown ones). The error names the setting, never the
    userinfo. A refused URL fails at once.
  - **A refused login or `ECIRCUITBREAKER` is not retried in a loop;** one retry for Supavisor's "recently changed".
    Failed logins trip the breaker for the whole role (design §4).
  - **On a refusal the instance stays up and holds** (`db.LoginRefused`, `app.holdLogin`). An exit would be a crash
    loop: each new instance logs in once more and keeps the breaker shut, and no tick would report the outage.
    While it holds, every request but the health paths gets the start's 503 at once, the ready paths' logged cause
    says the login was refused, and a tick reports `ryft_db_up 0`. A tick with a valid token gets one login retry at
    most every 5 minutes (in practice one per 15-minute tick), bounded to 20 s; a tick is when the instance has
    CPU. A retry that passes opens the gate and that tick is served.
  - **`exec` mode sends Go types as their own OIDs:** a `[]byte` goes as `bytea`, which a `jsonb` column refuses. The
    store's five jsonb parameters are strings now, and `[]uuid.UUID` is registered in `db`.
  - **Session state is checked:** `scripts/check_session_state.sh` (in `make lint`) fails on a session `SET`,
    `set_config(…, false)`, `LISTEN`, temp tables, session advisory locks, `PREPARE` and `WITH HOLD` cursors outside
    migrations, tests and `testsupport`. Today's code is clean; the outbox's locks are transaction-level (D219).
  - **CI's `pooler` job** runs the store's integration tests through PgBouncer (Ubuntu's package) in transaction mode
    with `exec`, plus `TestPooler`'s representative queries. It is in `ci-ok`, so it gates `images`.
  - **Step 7's check:** `make pooler-check` runs `TestPooler` once against Supavisor on 6543 as the owner, read-only,
    in a rolled-back transaction, one connection. The services can't run there before step 8's migrate. Amended by
    D228: `exec` mode reads floats as text, and Supabase's `extra_float_digits` is 0, so the service roles set 1
    (migration 0035), step 7 reports the owner's value, and step 9's role login checks the role's.
- **Why:** Cloud Run's churn makes many short-lived clients, and transaction mode holds a server connection only per
  transaction. Supabase's shared pooler doesn't support prepared statements in transaction mode. The link crosses
  the internet from Google to AWS with health data and the SCRAM login, so `require` (unverified) isn't enough.
- **Alternatives:** `simple_protocol` (client-side parameters; the fallback if `exec` fails on Supavisor); session
  mode for the services (a server connection per client: 26 clients at the maximum would fit, but scale-out churns);
  the IPv4 add-on for direct connections (paid).
- **Consequences:** a store change that passes a `[]byte` to `jsonb` fails CI's `pooler` job, not production. The CA
  file is renewed by a PR before 2031-01-26.

## D222 — Secret Manager, one SA per service, pins by number; P5 in the code; `ClientIP` behind Google (amends D155; PR A2)

- **Decision:**
  - **Secrets:** Secret Manager, one runtime SA per service with `secretAccessor` on each secret it reads, never at
    project level. Versions are pinned by number in `deploy/run`, never `latest`. Values are added by the user
    (`gcloud secrets versions add`) and never pass through Terraform state, a terminal or argv. The data key's path is
    design §5.3. A role password moves in one commit with `scripts/rotate_db_password.sh` (design §4).
  - **P5 in the code:** with `K_SERVICE` or `CLOUD_RUN_JOB` set, `config.LoadAuth` refuses `AUTH_MODE=apikey`,
    `ALLOW_INSECURE_APIKEY_AUTH`, `API_KEY` and `USER_ID` (`TestLoadAuthRefusesAPIKeyOnCloudRun`); `config.Load`
    refuses `ALLOW_INSECURE_PUSH` and the emulator (D218). `scripts/check_cloudrun.py` rejects the names in
    `deploy/run`.
  - **Push and tick tokens are checked in the app on all four services** (D218).
  - **Jobs:** with `CLOUD_RUN_JOB` (and no `K_SERVICE`) the push identity and `GCP_PROJECT` are optional, but checked
    when set. A `scoring rescore` job needs `DATABASE_URL`, `PGPASSWORD`, `CONSENT_ENFORCE`, `LOG_LEVEL`. There is no
    such job in `deploy/run` yet.
  - **`httpx.ClientIP`:** walk `X-Forwarded-For` from the right, skip only Google's front-end ranges (35.191.0.0/16,
    130.211.0.0/22 and their IPv6 /64s), and take the next entry as it is, else `RemoteAddr`; never the leftmost
    entry. A private, CGNAT, link-local or loopback entry is a client (one inside a VPC), not a hop: skipping it would
    let the forged entry to its left be the key (security review of A2). IPv6 is cut to /64.
    `TestStepNineUnderGoogleFrontEnd` proves the choice for each shape Google could send. *Amended by D240:* it is
    `httpx.ClientKeys` now, the `XFF_TRUSTED_HOPS`-th entry, with no `RemoteAddr` (a shared fallback key instead),
    `picked` is `xff`|`fallback`, and the limits key by an HMAC of the address.
  - **No IP address in any log line** (head-architect's rule, 2026-11-15): the privacy text in force promises logs
    without them, so no address is logged, whole or as a /24 or /48. The `client address` line (info, at most once
    a minute per instance, no request ID, user, route or header) holds `picked` (`xff`|`remote`), `xff_index` (the
    picked entry's position from the left) and `xff_entries`. Runbook step 9 sends three forged entries and checks
    `xff_index` is 3. The keys are in the log tests' allowlists (`clientAddressKeys`, `adminLogKeys`).
  - **The admin limiter is capped** at 4,096 addresses; new ones beyond that share one bucket (`adminlimit_test.go`).
    A request bearing `ADMIN_API_KEY` passes before the limiter (constant-time check), so a flood that fills the
    cap and the shared bucket can't lock the break-glass key out (`TestAdminKeyPassesTheLimit`).
- **Why:** design §5.1: Vault, admission and Traefik are gone, so their guarantees move to IAM, the code and CI.
  Domain mappings give IPv6 clients, and one /48 is about 65k /64 keys.
- **Alternatives:** Binary Authorization (price unchecked); a per-instance limit keyed by the leftmost entry
  (forgeable); logging the picked address's /24 or /48 for step 9 (refused: still an address under the rule above).
- **Consequences:** limits are per instance and reset at a cold start; the aggregate is bounded by max-instances.
  Under a flood of new addresses the admin route's newcomers share one bucket, so they may wait; the known ones keep
  theirs.

## D223 — CI owns the revisions and jobs; Terraform owns the rest (supersedes D94–D96; PR B, PR A2)

- **Decision:**
  - `deploy/run/<svc>.yaml` and `migrate.yaml` are rendered by `scripts/cloudrun_render.py` from
    `deploy/run/config.env` (values in git, no repository variables) and applied by `deploy.yml` on push to main after
    `ci-ok`: WIF auth, build and scan, push by digest, `migrate` (stop on failure), then scoring, notify, ingest, api,
    then `make smoke-prod`. Rollback: `gcloud run services update-traffic`.
  - Terraform (`infra/gcp`, workspace `ryft-gcp`, applied by the user) owns IAM, secrets' containers, the topology, the
    Scheduler job, Artifact Registry, the WIF pool, the mappings, DNS, the log bucket and the Grafana-managed rules.
    Not in Terraform: the budget (D226), HCP Terraform's own WIF pool and SAs, the Grafana SA's key.
  - HCP Terraform signs in with dynamic credentials: a pool pinned to `ryft-lab`'s org ID and `ryft-gcp`'s workspace
    ID, a plan SA and an apply SA, each usable only in its run phase.
  - B's design fixes (`ticket-b.md` → Design fixes) are part of this decision: the deployer's project-wide
    `run.developer` only while `serve = false`; a WIF condition on IDs that admits `push` and `workflow_dispatch` on
    main from GitHub-hosted runners; `migrate` with `maxRetries: 0`; ticks without a DLQ; subscriptions that never
    expire; Artifact Registry keeping 30 days and the 5 newest; `deploy.yml` deploying only main's head after
    `ci.yml`'s own run.
  - The bootstrap's values (project number, `PUSH_SA_ID`, the Supabase ref and pooler host, the Apple key IDs) are in
    `config.env` since A2 (2026-11-15). Apple's one key serves both Sign in with Apple and APNs, so `APPLE_KEY_ID` and
    `APNS_KEY_ID` are equal.
- **Why:** one owner per object (D94's rule kept); a reviewed commit is the only way to change a running revision.
- **Consequences:** `GCP_DEPLOY=on` turns the deploy on (runbook step 8). The EKS `images`/`promote` jobs keep running
  until C1 removes them.

## D224 — Logs stay in Google; Grafana reads Cloud Monitoring and gets the app metrics by OTLP (supersedes D52; PR A2)

- **Decision:**
  - **Logs:** stdout JSON to Cloud Logging, a regional bucket `ryft-logs` (europe-west1, 14 days), the `_Default`
    sink off. With `K_SERVICE` or `CLOUD_RUN_JOB` set, `level` is `severity` (DEBUG, INFO, WARNING, ERROR), `msg` is
    `message`, and request lines carry `logging.googleapis.com/trace` from `traceparent` or `X-Cloud-Trace-Context`.
    Locally the keys stay `level` and `msg`: `scripts/trace.sh`, `logcheck` and the runbooks read them.
  - **Platform metrics:** Grafana Cloud reads Cloud Monitoring through `grafana-reader` (`monitoring.viewer`, the one
    SA key, accepted by the user).
  - **App metrics:** OTLP/HTTP JSON from the Prometheus registry's `Gather()`, stdlib only, pushed at the end of each
    tick and once on SIGTERM. Resource `service.namespace=ryft`, `service.name`, `service.instance.id` (random per
    process), so Mimir's `job` is `ryft/<svc>`. Names as registered, no unit, no scope, so `ryft_ticks_total` arrives
    as such with or without Mimir's suffixes. One gzipped request, no retry, no redirect.
  - **Config:** `OTLP_ENDPOINT` and `OTLP_USER` both empty means no push; one without the other is refused. The
    endpoint must be exactly `https://otlp-gateway-<zone>.grafana.net/otlp`, because the token goes with every push and
    other `grafana.net` names belong to whoever creates a stack. `OTLP_TOKEN` (secret `otlp-token`) is required then.
  - On Cloud Run there is no `:9090` listener; locally `/metrics` stays.
- **Why:** with request-based CPU nothing can be scraped between requests; logs at Google keep the privacy text's
  "Grafana: metrics without personal data" true.
- **Unchecked** (runbook step 11 checks them after the first push): that the gateway takes JSON, whether it promotes
  resource attributes to labels, and whether it writes a zero at a series' start (then `increase()` would count a
  fresh instance's first sample).
- **Consequences:** `OBSERVABILITY.md` → The metrics push, Health paths.

_Grafana half superseded by D236._

## D226 — One budget, made by hand; the €20 stop line is a runbook action (**the user's decision**, 2026-11-14)

- **Decision:** one budget, "RYFT 50 EUR", on the billing account, made by hand and not in Terraform, with rules at
  10, 50, 90 and 100 % that mail the user. At €20 the user runs the stop line (invokers off, the tick paused;
  `OPERATIONS.md` → GCP → The €20 stop line). `max-instances` is the hard cap.
- **Why:** a budget needs billing-account rights that the Terraform SAs shouldn't hold, and an alert doesn't stop
  spending.
- **Consequences:** retention (the invite email purge, deletion retries) pauses with the tick (review-a1 finding 13).

## D227 — The removal order and what GCP supersedes (PR A2 writes it; D1, E, D2, D3 carry it out)

- **Decision:** the order is the user's: the data key first (into 1Password, then Secret Manager), then Cloud DNS and
  the NS switch, Route 53 (≥ 48 h later), the AWS keys, bucket and budget, and last the old workspaces in
  `tim-krebs-org` (design §10, §11).
- **Superseded** once GCP serves production: D16, D17 (amended by D218), D24, D27, D43, D45, D46, D51, D52 (by D224),
  D55 (by D226; the site on Hosting without billing), D57, D64, D94–D96 (by D223), D97, D98, D99 (amended: Pub/Sub
  IAM), D102–D105, D107 (amended: Firebase Hosting, D225), D120, D121, D151, D155 (amended by D222), D169 (amended by
  D220: 10 s), D175 (amended: the hold in `deploy.yml`), D180, D182, D183, D185, D186–D205, D207–D210, D211 (amended:
  design §5.3), D215 (superseded by D225).
- **Kept:** D100 and D101 (amended by D219), D213 (no `argocd.` and `rabbitmq.` hosts, plus `ingest.`), D214 (the
  records in Cloud DNS; CAA adds `pki.goog`).
- **Why:** nothing is removed before its replacement serves, and a step that can't be undone (the KMS keys) comes
  after the data key is safe.
- **Consequences:** D3 deletes the code and manifests of the superseded decisions; until then they stay as history.
- **Amended 2026-11-19 (PR D1):** step 13's first apply is **targeted** at the two keys and the bucket. `infra/aws`
  still declares the VPC and EKS, and no cluster is in its state (destroyed on 11-10, and the SCP refuses
  `eks:CreateCluster`), so the design's plain `apply` would plan to create one. The second apply (after D2) stays
  untargeted: an empty root destroys everything in the state, leftovers of the rebuild's apply included. Before
  step 14 deletes `ryft-prod-euc1-vault`, the data key in Secret Manager is compared with that state's
  (`recover_data_key.sh --dry-run`), not with step 0's note. The guarded commands are in OPERATIONS → GCP 12–14.

## D228 — Exact floats through the pooler: the service roles set `extra_float_digits = 1` (amends D221)

- **Context:** runbook step 7 failed `make pooler-check` on 2026-11-15. Supavisor on 6543 as `postgres` (PostgreSQL
  17.11) gave `extra_float_digits 0`, and `float8 0.3 (want 0.30000000000000004 exactly)`; everything else passed.
  `pg_settings` says `0 | configuration file` on 5432 and 6543, and `pg_db_role_setting` has no entry, so it's
  Supabase's server config, not Supavisor or a role. On EKS it never showed: pgx's default mode reads results in
  binary. D221's `exec` mode reads them as text, and with 0 Postgres prints a float8 with 15 significant digits.
- **Decision:**
  - **Migration 0035** (additive) runs `ALTER ROLE … SET extra_float_digits = 1` for `ryft_ingest`, `ryft_scoring`,
    `ryft_api` and `ryft_notify`. A role's setting outranks the configuration file, and Supavisor logs in as the
    role when it opens a server connection, so the setting applies there. 1 is Postgres's default since 12: the
    shortest text that reads back as the same float8. The owner needs CREATEROLE and ADMIN OPTION on each role
    (PG ≥ 16), the right that migrate's `ALTER ROLE … PASSWORD` uses whenever a password changes. Creating a role
    grants it only since 16, and roles made before an upgrade may lack it, so step 7's `TestPooler` checks it
    read-only. Roles are cluster-wide, and CI migrates many databases on one server at once, so 0035 skips a role
    that has the setting, catches a lost race (`unique_violation`, "tuple concurrently updated"), and fails if a
    role still lacks it.
  - **Not the owner** (`ALTER ROLE CURRENT_USER`): migrate logs in on the session pooler in pgx's default mode,
    which reads results in binary, and its own reads are versions, booleans and role names, never a float. No
    service SQL turns a float into text on the server (no `::text`, `to_json` or `format` of a float column). The
    owner's defaults are also Supabase's dashboard's.
  - **Step 7** (`TestPooler` as the owner, before 0035) reports `extra_float_digits` and its source. It checks the
    float8 exactly when the setting is ≥ 1, and otherwise at the 15 + n digits Postgres prints. A service role
    (`db.ServiceRoles`) needs ≥ 1. Everything else is checked as before, plus the owner's right to run 0035.
  - **Step 9's role login** as `ryft_api` on 6543 checks it: `select current_user,
    current_setting('extra_float_digits')::int >= 1, '0.30000000000000004'::float8` must print
    `ryft_api|t|0.30000000000000004`.
  - **CI:** `internal/db`'s `TestServiceRolesReadExactFloats` sets the test database to 0 (that ranks above the
    configuration file and below a role, so it stands in for Supabase's file; `ALTER SYSTEM` would change the
    server under other packages' tests). The owner must read 0.3 there, the proof that the stand-in holds. Each
    service role must read `0.30000000000000004` in `exec` mode, with the setting's source `user`. The `pooler` job
    runs `TestPooler` with `ALTER SYSTEM SET extra_float_digits = 0`, as on Supabase, checks that the 0 was in force,
    and resets it before the store's tests, which log in through the bouncer as the owner.
- **Why:** it's one additive migration, no change to the services, and `exec` mode stays (D221). The role's setting
  is the narrowest place with the right owner.
- **Alternatives:**
  - `default_query_exec_mode=cache_describe`: binary results and unnamed statements, one round trip once cached.
    A cached description goes stale when a migration changes a column's type, until the connection is recycled
    (30 minutes), and the first use of a statement costs a second round trip. It's the fallback if step 9 shows
    the setting doesn't hold through Supavisor. If step 7 shows the owner lacks ADMIN OPTION, it's no fallback,
    because migrate's password writes need the same right. Supabase must grant it first (OPERATIONS → step 7).
  - `ALTER DATABASE postgres SET`: it ranks below a role and would also change every Supabase role's sessions.
  - A startup parameter: `serviceFindings` refuses it, and PgBouncer and Supavisor refuse unknown ones.
  - `SET LOCAL` in every transaction: one more statement each time.
- **Consequences:** a new service role needs the setting in its migration (`TestServiceRolesReadExactFloats` ranges
  over `db.ServiceRoles`). A future migration that turns a float into text on the server runs as the owner with
  Supabase's 0, so it must not rely on more than 15 digits. A server connection Supavisor already has open keeps
  the old value until it closes. At the first deploy there are none, because migrate runs before the services.

## D229 — The Go dependencies are checked before the merge: govulncheck as shipped, plus the deploy gate's Trivy rule (pgx 5.9, Go 1.27)

- **Context:** runbook step 8 stopped on 2026-11-15. `deploy.yml` run 43756793439's Trivy gate (critical, with a fix)
  found CVE-2026-33815 and CVE-2026-33816 in the api image's pgx v5.7.5 (fixed in 5.9.0). Nothing was pushed. Three
  gaps let it get that far: Dependabot didn't watch Go, a pull request's CI never scanned the services (`images` and
  its Trivy run on main only), and two flakes on main had held the run before (tflint's plugin download, an h2c test).
- **Decision:**
  - **pgx v5.9.2**, the latest 5.9.x. It also fixes GO-2026-5004, a SQL injection through dollar-quoted strings in
    pgx's sanitizer (fixed in 5.9.2), which our code reaches. go.mod's floor moves to Go 1.25.0, pgx 5.9's minimum.
    Nothing in the changelog touches `exec` mode, text results or how a DSN and `PGPASSWORD` are read. New since
    5.8: `min/max_protocol_version` (default 3.0), and `channel_binding` (default `prefer`, as in libpq). So over TLS,
    SCRAM's first message now says the client supports channel binding (`y,,`, not 5.7.5's `n,,`). Supavisor must
    accept that. CI's pooler runs without TLS, so the proof is step 7's `make pooler-check` (pgx, `verify-full`,
    6543), run again after the merge and before step 8. `channel_binding=disable` in the URL restores the old message
    (`serviceFindings` allows it: it isn't a startup parameter).
  - **golang.org/x/text v0.41.0**: govulncheck reaches GO-2026-6629 and GO-2026-5970 through pgx's SCRAM login.
    golang.org/x/sys stays: its only finding (GO-2026-5024) is in `x/sys/windows`, not called.
  - **The build image moves from Go 1.25 to 1.27** (`golang:1.27-alpine`, digest read off this PR's `vulns` log).
    In go1.25.14, govulncheck reaches 11 standard-library vulnerabilities, in net/http (two HTTP/2 server crashes
    among them), crypto/x509, crypto/tls and net/textproto. Go 1.25 got no more fixes after 1.27's release, so they're
    fixed only in 1.26.9+ and 1.27. The images running now carry them. go.mod's floor stays 1.25.0, so the GODEBUG
    defaults stay 1.25's. A `toolchain` line names the image's Go, so CI's setup-go tests with it, not with 1.25.0.
  - **A `vulns` job in ci.yml, required by `ci-ok`**, when the Go changes and on every push to main:
    - **govulncheck v1.9.0**, in backend/Dockerfile's build image by its digest, so it judges the standard library we
      ship. Any reachable finding fails, whatever its severity. Its version must build with the image's Go (v1.9.0
      needs 1.26; v1.7.0 was the last for 1.25). An unpinned golang tag fails the job and prints the digest to pin.
    - **Trivy**, the deploy gate's image and flags, `fs` on backend/go.mod. A pull request now fails on what would stop
      `deploy.yml`.
  - **Dependabot `gomod` on /backend**, grouped minor and patch, like the npm block.
  - **tflint `--init` gets the job's token** (`contents: read`), for GitHub's API limit.
  - **The cut has a cause** (`errCut`, wrapping `context.Canceled`). The in-flight test waits for the request's
    context and checks its cause, so it no longer reads the context at a racing moment.
- **Why both scanners:** govulncheck puts the two CVEs in a package we import (pgproto3) but in no function we call.
  Alone, it would not have stopped what the gate stops. Trivy has no reachability, can't see the build image's
  standard library from a go.mod, and its critical-only rule let GO-2026-5004 through. Together, a pull request fails
  on what the gate would refuse, and on anything our code reaches.
- **Alternatives:**
  - govulncheck with setup-go reading go.mod: that's go.mod's Go, not the image's, and the two drift apart when
    Dependabot moves the image's digest. The image is what ships.
  - Raising go.mod's floor to 1.27.0 with the image: that changes the GODEBUG defaults of two releases at once. The floor
    moves when we choose it, not when a security fix needs a supported line.
  - The newest 1.26 instead of 1.27: it fixes the same 11, but its support ends half a year earlier.
  - govulncheck `-scan package`: closer to Trivy, but it also fails on any standard-library advisory in a package we
    import, reachable or not. Trivy's critical-only module rule is the narrower match for the gate.
  - Building the images on a pull request and scanning them as `deploy.yml` does: the exact gate, but five image
    builds per Go PR.
- **Consequences:** a vulnerability published after a merge turns the next push's `ci-ok` red, and `deploy.yml` then
  deploys nothing until the fix lands (OPERATIONS → The vulns job went red). Go 1.27's support ends with 1.29's
  release (about August 2027). After that, the next reachable standard-library finding means a new Go line in the
  image, and the `toolchain` line moves with it. When Dependabot moves the image's digest to a new patch, the
  `toolchain` line lags until a hand edit. CI then tests one patch behind what ships, and govulncheck still judges
  the image. Trivy on go.mod also sees test-only modules, which the images don't contain, so it is a little stricter
  than the gate.
- **Amended by D232:** go.mod's floor moves to 1.26.0 (client_golang v1.25.0 and x/sync v0.23.0 need it), so the GODEBUG
  defaults are 1.26's. The image and the `toolchain` line stay Go 1.27.

## D230 — The site is English only: the privacy guards read the English page, `/de/…` is a 301 (amends D78, D107)

- **Context:** the user's decision of 2026-11-14: the site is English only, like the app, and the restyle that #142 was
  for (light only, Inter) is dropped. The site's own record is web D18 (`web/docs/DECISIONS.md`). D78's and D107's
  guards and REBUILD step 12's check of `/de/privacy` assumed a page in each of two languages.
- **Decision:**
  - `scripts/check_policy_in_force.sh` (the EKS part and B2's Cloud Run part), `scripts/pin_images.sh` and
    `web/scripts/check-policy-version.mjs` read `web/content/legal/en/privacy/<version>.mdx` only. A German page in
    an older commit is ignored, a draft there or none at all. The language loops stay loops of one, for the day German
    comes back.
  - `/de` and `/de/<path>` answer 301 to `/` and `/<path>`, through the trailing-slash rule's safe capture (#156): a
    letter or digit first, then letters, digits and `/ . _ -`, else 404. So the Location can't leave the site. It is
    cached for an hour, not forever, so the paths stay free if German comes back. Older app builds link
    `/de/privacy`, `/de/terms` and `/de/impressum`.
  - nginx carries the rule for now. C1 (Firebase Hosting) must carry the same 301 in `firebase.json`.
    `docs/work/2026-11-14-gcp/design.md` §8 and runbook row 10 now say so.
  - `privacy-versions.json` keeps every version (2026-10-20, 2026-10-26), each with its English page. The version in
    force and its text don't change.
- **Alternatives:** keep the German privacy pages alone, for the consent screen of older builds. Rejected: two texts
  of one version must say the same thing, and C2 is written in English. A 410 for `/de/…`: older builds would show an
  error where the English text is one hop away.
- **Consequences:** a policy PR is one page, the list, `deploy/run/config.env` and its copies. `docs/API.md` says
  English for `privacy_policy_url` (no field changes). Bringing German back means the pages, the messages, a `[locale]`
  segment and a second loop entry, from this change's parent commit.

## D231 — The api serves the app's analytics target: `GET /v1/config` → `analytics` and the flag `analytics.enabled`

- **Context:** the app's product analytics go to PostHog EU Cloud: opt-in, pseudonymous, a fixed event list, no IP
  (the analytics plan of 2026-11-16, decisions 4–6 and "Order" step 1, approved by the user). Both repositories are
  public, so the project key must not be compiled into the app. PostHog's project key is a client key, sent with
  every event; what it must never be confused with is a personal API key (`phx_…`), which reads and changes the
  organisation.
- **Decision:**
  - **Contract (additive):** `RemoteConfig.analytics`, always present, `{host, project_key}` or `null` while the api
    has no key, and the flag `analytics.enabled`, seeded enabled at 0 % (migration 0036, never overwritten), so testers
    get it by the allow-list as with `train.import`. The two are independent; the app sends only when the flag is on,
    `analytics` is set and the user opted in, and drops its queue when any of them stops holding. No route checks the
    flag.
  - **Config:** `POSTHOG_PROJECT_KEY` (optional; trimmed; when set it must match `phc_[A-Za-z0-9_-]{20,128}`, so a
    personal key or a pasted wrong secret stops the start instead of reaching every app) and `POSTHOG_HOST` (default
    `https://eu.i.posthog.com`; exactly `https://<host name>`, lower case, no IP address, port, path, query or
    fragment; checked with or without a key). Both are read in the api's `Load`, before anything connects. Neither
    value appears in an error, a log line (the start logs `analytics configured=<bool>`) or a metric label. The
    code doesn't pin the host to PostHog's EU name, so a first-party proxy can front it later. On Cloud Run it is the
    default: `deploy/run` doesn't set `POSTHOG_HOST`, and `check_cloudrun.py` refuses it until a reviewed PR adds it
    to `api.yaml` and to the check's table.
  - **Secret:** `posthog-project-key` in `infra/gcp/secrets.tf`, accessor `ryft-api` only, like the other 11.
  - **Deploy safety: the pin follows the first version.** Cloud Run refuses a revision that reads a missing secret
    version, and `deploy.yml` replaces api last, so a pin merged before the user's `apply` and `put` would leave every
    deploy red until then. The deployer can't read Secret Manager (its roles are pinned), so the deploy has no check
    that could catch it, and a `FILL_ME_IN` in `config.env` would hold every deploy. So this PR adds no pin:
    `check_cloudrun.py` knows the secret and accepts `POSTHOG_PROJECT_KEY` in `api.yaml` as optional (pinned by
    number, from that secret, api only). The pin is a one-line PR after `apply` and `put` (OPERATIONS → GCP →
    PostHog's project key). Until it merges, `analytics` is `null`.
- **Alternatives:**
  - The pin in this PR, with the runbook order apply → put → merge: no follow-up PR, but a merge out of order turns
    the deploy red, also for every other PR merged after it.
  - Compiling the key into the app: public repository.
  - A check in `deploy.yml` that every pinned version exists: needs `secretmanager.versions.get` for the deployer.
  - Serving the key only when the flag is on for the caller: the app needs both anyway, and the plan keeps them
    independent.
- **Consequences:** the app reads `analytics` from the newest config; a key change is a new version and a pin move.
  The kill switch is the flag (the app's next config, within six hours). This changes no privacy text: naming PostHog
  there is a step of the analytics plan, not of this PR. Nothing is sent until the flag, the key and the opt-in are
  all there.

## D232 — pgx 5.12's text arrays are taken as they are; go.mod's floor moves to Go 1.26.0 (amends D229)

- **Context:** Dependabot #176 bumps the backend's minor and patch modules: pgx v5.9.2 → v5.12.0, client_golang
  v1.25.0, client_model v0.6.3, x/sync v0.23.0, x/time v0.15.0, jwkset v0.11.4, keyfunc/v3 v3.9.1. It failed one test,
  `internal/db` `TestExecModeEncoding/[]string,_quoted_where_needed`. In `exec` mode (D221, Supavisor in transaction
  mode) a `[]string` goes out as a text array literal, so its text is what the server parses.
- **Cause:** pgx 5.12's `quoteArrayElementIfNeeded` quotes an element that holds whitespace anywhere, or the
  type's delimiter. 5.9.2 quoted whitespace only at either end. So `b c` now goes out as `"b c"`. Postgres's
  `array_in` reads both as the same element, and the text quoting was already correct for `""`, `NULL`, `{`, `}`,
  `,`, `"` and `\`. The change only adds quotes Postgres accepts. It doesn't change which values reach a query.
- **Decision:** take all 7 updates.
  - `TestExecModeEncoding` pins 5.12's text: `{a,"b c","","NULL"," x","{,}"}`, plus a case for tab, quote and
    backslash.
  - `store`'s `TestPooler` (CI's pooler job, `make pooler-check`) reads every one of those forms back through the
    pooler, inner whitespace and the delimiter included (`pull-up bar` among them).
  - No Dependabot `ignore`.
  - The login doesn't change (`go doc` on 5.12's `pgconn.Config`): `MaxProtocolVersion` and `MinProtocolVersion`
    still default to 3.0, and `ChannelBinding` to `prefer` (D229). The new `SSLCertMode` defaults to `allow`, and we
    hold no client certificate. `RequireAuth` empty accepts every method. `TestPoolConfigHooks` still finds no
    startup parameter. CI's pooler runs without TLS, so the Supavisor login over TLS is first proved by the
    deploy's `make smoke-prod`. The startup probe is `/health/live`, so a revision that can't log in still takes
    the traffic and answers 503. If the smoke fails, roll back by traffic (OPERATIONS → GCP → Deploy, roll back,
    rotate), then revert.
- **The `[]string` parameters in production:**
  - `queueDays` (`unnest($2::text[])`, on every sync, import, fold and recompute) and `PendingAmong`: dates.
  - The journal's `array_position($n::text[], category)`: `model.BehaviorCategories`.
  - Exercises' `primary_muscles`, `secondary_muscles` and `equipment` (the store's INSERT/UPDATE, and migrate's
    upsert of the built-in catalogue).
  - Feedback's `request_ids`.
  - `ReplacePRs` (`kind = ANY`).
  - The fold's `source = ANY`, on every sync with workouts: HealthKit source names such as `Tim’s Apple Watch`.
  - migrate's seed slugs and role names.
  - Three of them carry inner spaces, so their text on the wire changes: the fold's sources (on the hot path),
    `equipment` (1 to 40 characters) and `request_ids` (1 to 64). The values Postgres reads don't change, and
    `TestPooler` reads a source-shaped and an equipment-shaped element back through the pooler.
- **go.mod's floor moves from 1.25.0 to 1.26.0.** client_golang v1.25.0, x/sync v0.23.0, x/sys, x/text and
  prometheus/common require Go 1.26. D229 said the floor moves when we choose it; this is the choice. The image stays
  `golang:1.27-alpine` and the `toolchain` line stays go1.27.4, so only the GODEBUG defaults move:
  - `cryptocustomrand`: crypto ignores a custom random reader. Every key and signature in the code and tests uses
    `crypto/rand.Reader`.
  - `tlssecpmlkem`: the SecP256r1/384r1 ML-KEM hybrids join the TLS key exchanges a client offers (Supabase, Pub/Sub,
    APNs, PostHog). The server still picks one it supports.
  - `urlstrictcolons`: `url.Parse` refuses a second colon in an unbracketed host. Every URL we parse has one host and
    at most one port (the DSN, OTLP, Pub/Sub, the config's https checks).
  - `httpservecontentmaxranges`: caps the ranges `http.ServeContent` serves. The services don't call it.
- **Alternatives:**
  - The other six, with pgx held at 5.9.2 by an `ignore` for `>=5.12`. Rejected: the encoding is correct, and holding
    pgx means missing its next security fix. pgx had two security releases in 2026 (5.9.0 and 5.9.2, D229).
  - Testing for semantics alone (round trip only, no pinned text). Rejected: the pin is what told us pgx changed what
    it sends through the pooler. Keeping it costs one line per pgx change.
  - Holding the floor at 1.25.0 by holding the five modules. Rejected: it holds the Prometheus client and x/sys
    back for a GODEBUG delta that touches nothing we do.
- **Consequences:** the next pgx release that changes a text encoding fails `TestExecModeEncoding` again, as it
  should. Check the new text against Postgres's input rules and `TestPooler`, then re-pin. A module that needs Go 1.27
  moves the floor again, with its own GODEBUG list. There's no date to look again: nothing is held back.

## D233 — The site on Firebase Hosting: deployed by `deploy.yml` behind `SITE_DEPLOY`; CI loses the EKS jobs (C1; amends D107, D223, D225)

- **Context:** since 2026-11-16 the services run on Cloud Run (runbook steps 1–9). No host serves ryftlab.de, so
  `/privacy`, `/terms`, `/support`, `/impressum`, the AASA and `/admin` are all down. Design §8 (D225) puts the whole
  export on Firebase Hosting in `ryftlab-site` (Spark, no billing). ci.yml still built GHCR images and opened
  `deploy: images` PRs that merged themselves, for a cluster that no longer exists.
- **Decision, the site** (the Hosting rules themselves are web D19):
  - `deploy.yml` has three jobs next to `deploy`, gated by the repository variable **`SITE_DEPLOY`**:
    - `site-off` runs when `SITE_DEPLOY` isn't `on`. It prints a notice and the run stays green. Until the user creates
      `ryftlab-site` (step 10), a merge deploys the services and never goes red because of the site.
    - `site-build` builds without a token: `npm ci`, the build, the brand check and the SUPPORT_EMAIL checks (moved
      from web-image). It passes `out/` and `firebase.json` on as an artifact. Every step of a job with
      `id-token: write` can mint a token, so no npm install script and no build runs next to one.
    - `site` runs in its own environment `site` (main only, no secrets; **the split** below), after the tip
      check, and signs in by WIF as `site-deployer@ryftlab-site` (the same pool and condition as `ryft-deployer`,
      its own principal set, no key). It runs
      `firebase deploy --only hosting --message "web <sha>"` with the one pinned firebase-tools
      (`web/.firebase-tools-version`, 15.35.0, install scripts off). Then `web/scripts/check-hosting.mjs` checks
      ryftlab.de and www live, and the policy version of `deploy/run/config.env` on `/privacy`. It runs that
      check on every run from main's tip, deployed or not, so drift shows.
    - The artifact is data, never code for `site`. It's unpacked into `$RUNNER_TEMP/site`, not over the
      checkout. Anything in it besides `firebase.json` and `out/`, and any link, fails the step. npx would read an
      `.npmrc` or a `node_modules/` there, and npm code in `site-build` could plant either (the review of #194).
  - The site deploys when `web/` differs from the commit named in the live release's message, and on every run by
    hand. Otherwise it's "unchanged". A superseded run can't lose a web change: the next run compares against what's
    live, not against the previous push. Deploying on every push would add a version per push to Spark's 10 GB.
  - `site` refuses a `firebase.json` with `predeploy`/`postdeploy` hooks, `hosting.source` or `frameworksBackend`.
    The file comes from npm code in `site-build`, and those keys would run commands next to the token.
  - `DEPLOY_HOLD_SERVICES` doesn't hold the site; `SITE_DEPLOY` off does. One exception: while the services are
    held, `site` refuses (red) a deploy that moves `PRIVACY_POLICY_VERSION` against the live release's commit.
    It also refuses every deploy while that commit is unknown (fail closed, from the re-review), a run by hand
    included. Otherwise `/privacy` could show a version the held api doesn't ask users to consent to.
    - Not covered: a run whose `deploy` fails at migrate while `site` publishes a new version. The two jobs run in
      parallel, so the window lasts until the services' fix ships.
  - `scripts/check_workflows.sh` pins the shape:
    - an environment only on `deploy` (in `deploy`) and `site` (in `site`), never the other's;
    - no token in `site-build`;
    - in `site`, only checkout, setup-node (no cache key, `package-manager-cache: false`) and download-artifact, by
      SHA;
    - `firebase deploy` and the live check only after `tip`.

    Its `firebase … deploy` rule reads one `run` line at a time. A deploy hidden behind an env indirection, a line
    continuation or curl to the REST API would pass it; review catches those.
  - **The emulator needs the native `re2`** (observed in #194's first CI run). Its static server falls back to
    JavaScript's `RegExp` without it, can't parse RE2's `(?P<name>…)`, and dies on the first request.
    `web/scripts/hosting.mjs` installs the pinned CLI into `web/node_modules/.cache/` with install scripts off,
    then runs `npm rebuild re2` alone, and stops unless `re2` loads (web D19). The deploy doesn't serve, so it
    keeps every install script off.
  - **The split** (amended 2026-11-16 after #194; head-architect's decision, branch `infra/wif-split`):
    - **The problem:** as C1 merged, `site-deployer` was to bind the same WIF principal set as `ryft-deployer`, the
      repository's (`attribute.repository_id/<id>`), that is, every principal of the repository that passed the
      condition. Code in `site` could mint its own token and impersonate `ryft-deployer`: Cloud Run and migrate in
      production. That code is firebase-tools and its dependency tree, which npx resolves at run time with no
      lockfile. Splitting `site-build` from `site` kept npm's install scripts and the build away from the token,
      but not this.
    - **The decision:** one GitHub environment, and so one principal set, per deploy account.
      - `site` runs in the environment `site` (main only, no secrets, no admin bypass).
      - The condition admits `assertion.environment in ['deploy', 'site']`. The mapping already carried
        `attribute.environment`.
      - `ryft-deployer` is bound to `principalSet://…/attribute.environment/deploy` alone, and `site-deployer` (by
        hand in ryftlab-site, runbook 10a.3) to `…/attribute.environment/site` alone.
      - `check_workflows.sh` pins each job to its environment. `tests/iam.tftest.hcl` pins:
        - the condition and the mapping;
        - the deployer's member;
        - the two outputs (`deployer_principal_set`, `site_principal_set`; `github_principal_set` is gone);
        - that no `.tf` file names the repository's set;
        - that `deployer_workload_identity` is replaced create-before-destroy;
        - one pool and one provider. `…/attribute.environment/deploy` spans the pool, so it is only as narrow as
          the one provider's condition (repository and owner by ID). The brief named this member form. The
          alternative, a mapped `repository_id:environment` attribute, is safe against a second provider too, but
          it would change the mapping and every member for a pool that has no second provider.
    - **Order, so the services always deploy:**
      1. The user creates the environment `site`, before the merge (a job naming a missing environment creates it
         without a branch policy).
      2. The merge: `SITE_DEPLOY` is off, so `site` doesn't run, and `deploy` signs in under the old condition and
         binding.
      3. The apply: 1 to add, 1 to change, 1 to destroy. The condition is changed in place and still admits
         `deploy`. The new binding is made before the old one goes (`+/-`), and both admit the deploy job's token.
      4. A dispatched run proves the services still sign in.
      5. `site-deployer` is bound to the `site` set, or swapped to it from C1's binding.
      6. 10f, at least ten minutes after the apply. Nothing orders the condition's update before the old binding's
         removal, and IAM takes up to seven minutes to apply each, so `SITE_DEPLOY` stays off until then. 10f
         re-checks both accounts' members, because 10a.2's fallback (the old member back by hand) undoes the
         split.
    - **Rejected:**
      - Keeping one environment and narrowing by `job_workflow_ref` or `sub`: the two jobs share both.
      - A second provider: it would carry the same claims, so it adds nothing an environment doesn't.
      - Two PRs (add the new binding, then remove the old): create-before-destroy gives the same no-gap order in
        one apply.
- **Decision, the DNS:** `infra/gcp/site.tf` holds five variables for the records the Firebase wizard shows:
  `site_apex_a`, `site_apex_txt` (`hosting-site=…`, which joins the apex TXT set), and `site_www_a` with
  `site_www_txt`, or `site_www_cname` instead (exclusive, validated). Their values live in the committed
  `site.auto.tfvars`, like the gates: a reviewed PR, then apply 3. Empty adds nothing, and `tests/site.tftest.hcl`
  holds the file to its five keys.
- **Decision, CI without EKS:**
  - The `images`, `web-image`, `promote` and `admission` jobs are gone, and so are the `changes` outputs that only
    they read, the `force` input and every GHCR push.
  - Also gone: `admission-e2e.yml` and `infra/aws/kubernetes/admission-celtest/` (nothing on GCP admits anything;
    Dependabot's security PRs #180/#181/#183/#186/#187 on its go.mod close), the Makefile's `admission-celtest` and
    `admission-e2e`, `.github/actionlint.yaml` (it only silenced promote's App action), and `deploy-watch.yml`.
    Without promote every group stays "different from its pin", so the watch would have gone red every day.
  - `web-e2e` builds the site and runs the pinned Hosting emulator (`npm run e2e:hosting`). There is no nginx
    container and no Trivy of an nginx image.
  - The scripts behind the removed jobs (`promote.sh`, `deploy_targets.sh`, `hold.sh`, `pin_images.sh`,
    `check_registry.sh`, `deploy_watch.sh`, `admission_e2e.sh`) and the nginx image (`web/Dockerfile`,
    `web/nginx/`) stay until D3, with their tests.
- **Not done:** the brief asked for `"terraform-version":"1.16.6"` in OPERATIONS 2b. The line already says `1.16.7`,
  which `infra/gcp/terraform.tf` pins exactly (`required_version = "1.16.7"`, and the 2b note says the workspace
  follows it). Applies 1 and 2 ran, so the workspace is on 1.16.7, and a 1.16.6 workspace would refuse to init. Left
  as it is, for the user to confirm.
- **Unchecked until the first run by hand (step 10f):**
  - whether firebase-tools takes WIF's `external_account` credentials;
  - whether `roles/firebasehosting.admin` is enough;
  - what message a console rollback carries. The runbook sets `SITE_DEPLOY` off first, so it doesn't matter.
  - Hosting's header merging, Content-Type on the AASA and bare `/de` were settled against the emulator, with RE2,
    in #194's `web-e2e` (run 43857046841; web D19). They're checked again live after every deploy:
    - one CSP per response, the admin one on `/admin` only;
    - the AASA served as `application/json`, byte-identical;
    - bare `/de` → `/` by its exact rule;
    - one Cache-Control, the last matching rule's;
    - query strings kept;
    - no Location off the site.

    The emulator also showed two things the docs don't say. The `redirects` rules' 301s carry no header rules
    (cleanUrls' 301s do). `/%61dmin` serves `admin.html` under the marketing CSP, where it is inert.
- **Consequences:**
  - Rollback: `SITE_DEPLOY` off, Firebase console → Release history → Roll back, the revert merged while it's
    off, then `SITE_DEPLOY` on and a run by hand.
  - A run by hand deploys the services and the site.
  - **C2 inherits one change:** `scripts/check_policy_in_force.sh`'s EKS half wants the `overlays/eks` web pin to
    serve the version in force. Nothing moves that pin any more, so C2's version bump must drop that half (D3 would
    have).
  - D3 removes the leftover scripts and the nginx image.
- **Amended 2026-11-17 (the first deploy, run 44101115872, step 10f):**
  - firebase-tools takes WIF's `external_account` credentials.
  - `roles/firebasehosting.admin` isn't enough. `firebase deploy` also reads whether the Hosting API is on
    (serviceusage `services.get`), so `site-deployer` also has `roles/serviceusage.serviceUsageConsumer`, and
    nothing else (OPERATIONS 10a.3).
  - Live Hosting differs from the emulator on the admin redirects and on `//`, so `check-hosting.mjs` expects
    production by default and the emulator under `--emulator` (web D21).

## D234 — The site's checks read the app at a pinned commit, `web/.app-ref` (head-architect's decision, 2026-11-16; amends D107, web D10)

- **Context:** `web-checks` checked out `timkrebs/ryft-app` at its main. App PR #112 (Nocturne PR 1, the app's ADR
  0016) changed `Typography.swift`'s shape, so `tokens:check` threw "Typography.swift → size not found". Main has
  been red since #194, and `deploy.yml` deploys nothing while `ci-ok` is red. A merge in another repository broke
  this one, with no PR here to review it.
- **Decision:**
  - `web/.app-ref` holds one full commit SHA of the app and nothing else. `web-checks` reads it, refuses anything but
    40 lowercase hex characters (a branch or a tag would move under the pin), and checks the app out at that commit.
    The value goes through a step output, never into a `run` line.
  - The first pin is `7358f0c99b7a6eb544521e87407b7d2c9bc09556`, app main at #114 (2026-11-17), the commit the
    Nocturne tokens were generated from.
  - Nothing moves the pin automatically. A token update is a PR that moves the pin, regenerates what follows from
    it and passes the drift checks against the new commit (OPERATIONS → GCP → Deploy, roll back, rotate → The app
    pin).
  - Local runs still read whatever `RYFT_APP_DIR` (or `../app`) has checked out. CI's checks against the pin catch
    outputs made from another commit, so the local tools don't check the pin themselves.
- **Rejected:**
  - Dependabot or a scheduled bump. The app's main moves while a design runs over several PRs, and the site
    follows a design once it's done.
  - A git submodule. It would pin the same commit, but it adds a checkout mode to every clone, the hooks and the
    agents' worktrees.
- **Consequences:** app merges no longer turn platform main red. The site can trail the app by design; that gap
  is visible as the pin. Every check that reads the app, in CI, sees the same commit: tokens, Swift licences, the
  AASA's paths, the admin config and the mark.

## D235 — The workflow checks' test reads the pins, and `manifests` runs it on every `.github/` change (2026-11-17)

- **Context:**
  - #198 (Dependabot: `actions/download-artifact` 7.0.0 → 8.0.2 in `deploy.yml`) was green on its PR. Main went red
    at `09b50b4` (run 44117122278): `scripts/tests/check_workflows_test.sh` named the old pin's line verbatim, and
    `job_swap` didn't find it ("the test is stale"). `deploy.yml` deploys nothing while `ci-ok` is red.
  - On the PR, `changes` didn't select `smoke`, the job that runs `make test-scripts`. Its filter has `ci.yml` and
    `scripts/**`, but not `deploy.yml`. Main was the first to run the test.
- **Decision:**
  - The test writes no pin of the repository's workflows. For each of `deploy.yml`'s `deploy`, `site-build` and
    `site` jobs, it reads every SHA-pinned step `uses:` from `deploy.yml` as it is. It swaps each one for the same
    action on its tag (the version in the pin's comment) and expects `check_workflows.sh`'s message for that step's
    `uses:`. A job with no pinned line fails as stale. The lint job's golangci-lint version is read from the
    Makefile's `GOLANGCI_LINT_VERSION` the same way.
  - `manifests` runs `bash scripts/tests/check_workflows_test.sh` right after `./scripts/check_workflows.sh`. Its
    filter has `.github/**` and `scripts/**`, so every change to a workflow, to the script or to its test runs the
    test on the PR. `manifests` already installs the pinned yq the test needs.
- **The selection rule:** a script test that reads the repository's real workflows runs in `manifests`, not only in
  `smoke`. Today that is `check_workflows_test.sh` alone; the others build fixtures of their own.
- **Rejected:**
  - Adding `.github/workflows/**` to `smoke`'s filter. That costs the compose stack, about 7½ minutes, for every
    action bump, to run one test that needs only yq.
  - Keeping the test only in `manifests`. `make test-scripts` stays the whole suite locally, so `smoke` runs it too:
    twice on main and on PRs that select both jobs.
- **Consequences:** a Dependabot bump of an action anywhere under `.github/` runs the test on its own PR, and the next
  bump of a pinned action doesn't touch the test.

## D236 — Cloud Monitoring is the only observability; Grafana Cloud out (**the user's decision**, 2026-11-17; supersedes D224's Grafana half; amended by D237, D244)

- **Context:** D224 had Grafana Cloud read Cloud Monitoring through `grafana-reader` (a service-account key) and take
  the app metrics by OTLP push. Runbook step 11, which would have made the key, the data source, the push, the
  synthetics and the app rules, never ran: `grafana-reader` exists without a key, `OTLP_ENDPOINT` and `OTLP_USER`
  were empty, nothing was ever pushed. A second vendor, a key and a stack for a one-person beta weren't worth it.
- **Decision (the user's):** Grafana Cloud goes for RYFT on GCP; Cloud Monitoring is the only observability. About
  €6 a month for alerting is fine. Two channels, both made by hand in the console: the email `ryft-timkrebs` and the
  Google Cloud app's. D224's logs half stays (`ryft-logs`, 14 days).
- **How (`infra/gcp/monitoring.tf`, `alerts.tf`):**
  - **Signals:** what Cloud Run and Pub/Sub measure themselves; two log-based counters of lines the services already
    write, `ryft_ticks` (the `tick` line, label `result`) and `ryft_errors` (`severity>=ERROR`, no label); five uptime
    checks (the home page, `/privacy`, the AASA always; api's and ingest's `/health/ready` only while they're
    public), every 300 s from three locations; the dashboard "RYFT · Overview". **No label carries an IP, a user id,
    an email or a message id** (`tests/monitoring.tftest.hcl` pins the label set).
  - **Five conditions, one policy each** (the cap is a test: a sixth is a decision and a price): (1) uptime
    `check_passed`, `REDUCE_COUNT_FALSE` by check and host over 1200 s `> 1`, held 600 s; (2) api's or ingest's 5xx
    share over 10 min `> 0.02` and at least 3 5xx, held 600 s; (3) a service with no `result="ok"` tick in 45 min,
    an absent series included, only while the tick runs; (4) a dead-letter hold subscription holds a message; (5) an
    event subscription's oldest unacked message is older than 900 s. The subscriptions come from the topology's
    tfvars, not from a pattern. **Why (5), the lead's pick of the free fifth slot:** scoring and notify have no
    public 5xx condition and no uptime check, and (4) fires only after 20 failed deliveries with backoff. A consumer
    that is down, failing or saturated shows first as an event that ages. SyncSlow and Saturated (`alerts.tf`
    before D236) became dashboard panels instead.
  - **Channels** are data sources, never resources. The app channel's display name is personal data: a sensitive
    variable `alert_app_channel_name` (default `""`), set only in the HCP workspace's UI. Not ephemeral: a data
    source's argument can't take an ephemeral value. `local.alerting = nonsensitive(var.alert_app_channel_name !=
    "")` is the gate (the boolean shows only whether it is set): off, no channel is read and no policy exists; the
    metrics, checks and dashboard are free and stay.
  - **App metrics from logs**, not by pushing the registry to Google (`telemetry.googleapis.com`: a token client,
    resource attributes, a role, unverifiable here) or writing `timeSeries.create` from Go (the most code, a write
    role, a series per instance). The log-based metrics need no Go change and no role and carry the two signals the
    policies need. The OTLP code stays, dormant.
- **Cost a month** (prices not verified here): reading A, $1.50 a condition: **$7.50**. Reading B, possibly from May
  2026, $0.10 a condition and $0.35 per million time series returned: $0.50 + at most about 0.6 M series ≈ **$0.70**.
  Uptime: 5 × 3 × 12 × 730 = 131,400 executions (free up to 1 M). The log-based metrics: at most about 4 MB of
  points (150 MiB free). The dashboard: free.
- **Lost** (no alert, nothing reads the app registry on Cloud Run): TextRefusedByDatabase, NoSync, the APNs and push
  alerts, NewFeedback, the admin alerts, OutboxStuck, the www redirect and probe-flapping checks. The rest map onto
  the five or onto panels (`OBSERVABILITY.md` → What D236 dropped). The admin alerts were the lost-phone detection
  (`AdminWebUsed`): an admin sign-in you didn't make is now noticed only by reading the audit or the `admin_actor`
  lines (OPERATIONS → the admin page).
- **Removed:** the `grafana` provider, its resources, variables and mocks; `grafana-reader` and its
  `monitoring.viewer` (two destroys, no data); `deploy/grafana/synthetics`, `scripts/synthetics.sh`,
  `synthetics_re2.go`, their test, `make synthetics-*` and CI's steps; `deploy/grafana/alerts/ryft.yaml`, its tests
  and `make alerts-apply`; `OTLP_*` in `deploy/run` and `config.env` (`cloudrun_render.py` now refuses the keys).
- **Kept:** the secret `otlp-token` and its accessor, until no serving revision reads it: a rollback to an older
  revision needs it. Its removal is a PR of its own in two applies, because of `prevent_destroy` (runbook GCP 11.6).
  Until D3: `eks-admission.yaml` and its test, `admission-intended.yaml`, `admission_intended.sh`,
  `check_rule_groups.sh`, `make alerts-check` and `alerts-retire-eks`, the `k8s-monitoring` collector, the stack.
- **The lead's design, changed:** the 5xx condition got a floor of 3 (one failed uptime request at night is no
  outage); the subscriptions are derived, not matched; the apply SA needs four Monitoring roles, granted by hand
  (11.2); Error Reporting isn't turned on here (its API isn't on the keep list, it doesn't read the `panic` line's
  `stack` key, its retention is unchecked against the privacy text), so 11.5 makes enabling it the user's decision,
  in a PR of its own; the
  stack can go only once EKS stops sending (D3), and the app rule groups still live in its ruler go by hand (11.7).
- **Unchecked:** both price readings and the free uptime quota; that `increase()` over the log-based DELTA counter
  answers after the first counted tick; the app channel's delivery (11.4 tests it).
- **Privacy:** no new processor (Monitoring is Google's, like Cloud Run and Logging). The metrics are counts by
  service and result; alerts name policies, services, subscriptions and checks. The dashboard's logs panel reads
  `ryft-logs` with the reader's own log permissions, and only the services' stdout and stderr: Cloud Run's request
  log has ERROR entries for every 5xx, with the caller's IP. For RYFT on GCP, C2 need not name Grafana.
- **Consequences:** `OBSERVABILITY.md` → Cloud Monitoring; `OPERATIONS.md` → GCP → 11 and When an alert fires;
  `infra/gcp/README.md`.

## D237 — The app alert channel by id: `alert_app_channel`, null as unset, and a plan that fails on the email channel (2026-11-18; amends D236)

- **Context (found on 2026-11-18, read-only, through the API):** every alert policy lists the same channel twice,
  the email channel `ryft-timkrebs` (type `email`). The project has no other channel in the API and no mobile type
  among its channel descriptors: the Google Cloud app's device ("Mobile Devices" in the console) is no channel the
  API lists or a data source can find, and only the console can pick it. D236's app data source, looked up by
  display name alone, resolved to the email channel. Nothing failed: not the plan, not the apply, not `checks.tf`'s
  enabled check.
- **Decision:**
  - **By id.** The app channel comes in by its full resource name, `alert_app_channel` (a string,
    `^projects/[a-z0-9-]+/notificationChannels/[0-9]+$`). The user attaches the device to one policy in the console
    and reads the name from that policy's `notificationChannels` (runbook GCP 11.1; once, 11.8). The app data source
    and D236's display-name variable go. 11.1 reads the policies with a projection (`displayName`,
    `notificationChannels`), so their edit records, which name who edited them, stay off the screen.
  - **Null is unset.** `default = null`; the validation is `var.alert_app_channel == null || can(regex(…))`. With no
    default at all, every plan, every `terraform test` run and CI would need a value: the gate (unset, no policy and
    no channel read) would be gone, and a value would have to sit in a file. "No default" meant no committed value:
    there is none, and a test fails on one. An empty string is refused, not taken for unset, so an HCP variable
    saved empty fails the plan instead of turning alerting off quietly.
  - **Not sensitive** (HCP's Sensitive off to match). The name is an opaque id, the project and a number: it grants
    nothing, and the device's display name, the personal part, stays in the console. Not sensitive, it drives
    `local.alerting` without `nonsensitive()`, and the plan shows it in every policy, which is where the user checks
    it against 11.1. It is per device, so it is a workspace variable and in no committed file.
  - **A postcondition on the email channel's data source, not a `check`.** A check only warns, and the finding passed
    every plan with its warnings. The postcondition fails the plan when the app channel is the email channel: one
    place, evaluated when the email channel is read at plan time, where a precondition would sit on both policy
    blocks and fail five instances with five identical errors.
  - **By the channel number**, the name's last segment. `projects/<id>/…` and `projects/<number>/…` are the same
    project, so a comparison of the whole names could miss the same channel. The whole number is compared, never its
    end (`11` is not `1`). Another project's channel with the same number fails too, which is harmless: a policy
    takes this project's channels.
  - **Order `[email, app]`** in every policy. `notification_channels` is an ordered list, so the policies applied
    under D236 (`[email, email]`) change in place once: 11.8's plan is 0 to add, 5 to change, 0 to destroy, or 4 to
    change if the console saved the policy the user edited as exactly `[email, app]` (then it already matches).
- **Rejected:** a lookup with a `type` (no mobile type among the descriptors to name); the channel as a resource (only
  the console makes a device channel); sensitive (it would hide the one value the user must check in the plan, and
  protect no secret); a `check` (see above).
- **Lost:** `checks.tf`'s enabled check covers the email channel only, since no data source reads the app's. 11.4's
  test alert is its check, and 11.8 runs it again.
- **Tests** (`tests/monitoring.tftest.hcl`, mocked, plan only): unset, no policy and no data source; set, every
  policy's channels exactly [the email's name, the variable], not sensitive; equal to the email channel's, also
  under the project number, the plan fails (`expect_failures` on the data source); `…/11` beside `…/1` plans; the
  validation refuses a display name, `""`, `…/alertPolicies/1` and a trailing slash; no committed tfvars sets it, no
  `.tf` or tfvars holds a channel id, and every test value is in the fake project `fake-for-tests`.
- **Consequences:** `OPERATIONS.md` → GCP → 11.1, 11.2, 11.4 and 11.8 (once); `infra/gcp/README.md`;
  `OBSERVABILITY.md`; STATUS's 11.1 and 11.2.
  
## D238 — The beta gate on GCP: `warn` first, `on` after a stop rule, in `deploy/run/config.env` (2026-11-18; amends D87; amended by D241)

- **Context:** on Cloud Run `BETA_GATE` was set nowhere, so the code's default (`off`, D87) held: any Supabase
  account could sync. The first testers (5–10, TestFlight, heart rate and sleep: GDPR Art. 9) come next. `on`
  straight away would refuse every account without a redeemed code at the first request after the deploy: the
  operator's own and the smoke user's, if they have none, and `deploy.yml`'s smoke would go red, since it has
  no admin key to make one.
- **Decision:**
  - `BETA_GATE` is a required key of `deploy/run/config.env` (shape `off|warn|on`, lower case), rendered into
    api and ingest only, the two services that run the gate. scoring calls `LoadBeta` but reads only the
    consent mode, so it gets no such line. The mode in force is always the one in git, never the code's
    default.
  - **`warn` first.** It serves every account as before and flags each one without access (`X-RYFT-Gate:
    invite_required`, the access line's `gate`, `GET /v1/me/sync` → `gate`), so the merge refuses nothing and the
    app, which knows the header, asks for a code. In that time the operator redeems a code for their own account
    (in the app, which also proves the TestFlight build takes one) and for the smoke user, and every invited
    tester redeems theirs.
  - **`on` needs** (OPERATIONS → GCP → The beta gate, step 5): 7 days of warn lines with nothing but the smoke
    probe's, every code handed out used, no account without access that should keep it, the testers' build the
    one that took the operator's code (or newer), and `make smoke-prod` green with the key (`gates: invite warn`).
    Then the flip is a PR of one line, and its rollback is the same line back to `warn`.
  - Codes are unbound, single use, expire in 14 days, and their notes name nobody (an unbound code's note was
    never cleared; since D249 it is, 30 days after the code stopped being redeemable). No bound code before the privacy text names the stored address (U3).
  - **`CONSENT_ENFORCE` is unchanged:** not in `config.env`, so the default `on` holds in every step. A sync
    without consent to the current policy is refused throughout, and nothing is scored without one.
- **Amends D87:** D87's runbook made "both `on`" the step before the first external invite, because `warn`
  in the consent gate processes health data without a recorded consent. With the consent gate `on` throughout,
  that reason doesn't apply to the beta gate. So invited testers may redeem while it is still `warn`: under warn
  an account without a code is served, never processed without consent. `on` still comes before the beta is
  opened to anyone not invited.
- **Rejected:**
  - `on` in this PR. It would lock out whichever of the operator's account and the smoke user has no code,
    with no way to tell in advance which: `ryft_gate_refusals_total` isn't read on Cloud Run (D236).
  - Leaving the key optional, with the code's default. A deploy's mode would then be what the code says, not
    what the reviewed PR says.
  - The admin key in `deploy.yml`'s smoke, so CI could redeem or probe by itself. That puts the operator's
    break-glass key into a CI environment for a one-time step.
- **Unchecked here:** that the TestFlight build shows the invite screen on a `403 invite_required` (the app's
  code reads so; the throwaway check after the flip proves it); that the operator's and the smoke user's accounts
  lack access today (the count in step 5 says).
- **Privacy:** no new processor and no new data. The warn lines carry `user_id` like every access line; the
  runbook's prints leave it out, and the count goes through `sort` and `wc`. A code's note names nobody.
- **Consequences:** `scripts/cloudrun_render.py` (the key and the placeholder), `scripts/check_cloudrun.py` (the
  env of api and ingest), their tests, `deploy/run/{config.env,api.yaml,ingest.yaml}`; `OPERATIONS.md` → GCP →
  The beta gate: warn, then on.

## D239 — Before testers: auth mail through Microsoft 365 as a bridge, the restore drill on a new project, feedback by a proposed sixth condition (2026-11-18)

- **Context:** the first testers (5 to 10 friends, TestFlight) come next, and their heart rate and sleep are Art. 9
  data. Three gaps: email sign-up needs mail outside the Supabase team (*Confirm email* is on, D159), Supabase Pro's
  backups were never restored, and since D236 no alert tells the operator about feedback. The research used search
  results only, because WebFetch was refused. Every external claim in `OPERATIONS.md` → GCP → 14a names its page and
  is marked *(search)*.
- **Decision 1, auth mail (Microsoft 365 is the user's choice; the how is the lead's):**
  - Supabase's custom SMTP sends through `smtp.office365.com:587` as a dedicated, licensed user,
    `no-reply@ryftlab.de`. SMTP AUTH is enabled for that mailbox only, and the tenant's default stays as it is.
  - **A bridge, not a home.** The Exchange Team blog (2026-01-27) disables Basic auth for SMTP AUTH by default for
    existing tenants at the end of December 2026, lets admins enable it again, and announces the final removal date
    in the second half of 2027. Supabase documents no OAuth for SMTP. So 14a.5 tests again in January 2027, and
    14a.6 is the way out.
  - Not a shared mailbox: SMTP AUTH signs in with the account's password, and a shared mailbox has no sign-in of its
    own. Not `support@`: its password would sit in Supabase's settings, and every tester's mail sits behind it.
  - **Security defaults:** GoDaddy's SMTP switch turns them off for the whole organisation. The other accounts keep a
    second factor through per-user MFA, turned on first. If they can't, the route stops and the fallback runs.
  - **Sent Items:** Exchange keeps every SMTP AUTH submission there, so each tester's address and a one-time link
    stay in the mailbox. They get a 7-day retention or a weekly emptying, and C2 names the copy.
  - **The fallback:** an EU transactional sender (Brevo, Mailjet or Scaleway Transactional Email), with records in
    `infra/gcp/dns.tf`, in a PR of its own with C2's text. There is no pick here: the providers' DPAs and
    subprocessors weren't read.
  - **Rejected:** High Volume Email (internal recipients only); sending as `support@`; building Supabase's Send
    Email hook now to send through Microsoft Graph with OAuth. That hook is the way to keep Microsoft past 2027, but
    it is a build, not the beta's start.
- **Decision 2, the restore drill:** the newest daily backup goes to a new project ("Restore to a new project": the
  same region, the same processor, compute billed by the hour). A counts-only query runs in both SQL editors, judged
  by plausibility rules, because production moves on after the backup. Then the copy is deleted. **Rejected:** a
  download into a local Postgres. It puts Art. 9 data on a laptop, physical backups can't be downloaded, and the CLI
  dump reads production. On GCP this replaces `OPERATIONS.md` → Backups → Restore drill, monthly, all of it: no
  service or compose stack connects to the copy, and the counts stay on Supabase's pages.
- **Decision 3, feedback:** today the operator hears of feedback only by looking (`/admin` → Feedback,
  `scripts/beta_report.sh feedback`). **Proposed, not built:** a log-based counter `ryft_feedback` of the line
  `feedback received`, with the label `category` only, and a sixth condition "RYFT · New feedback". The line holds
  `feedback_id`, `category`, `request_ids` (a count), the common keys and logx's request keys (`request_id`,
  `client_request_id`, `user_id`, `route`, the trace), and never the message
  (`writeLogKeys`). No id becomes a label. The price is $1.50 a month under D236's reading A; Google's pricing page
  now says alerting is charged from no sooner than 2027-09-01, at $0.35 a month per metric reference. The cap of five
  is a test, so the sixth condition is the user's decision. **Rejected:** a log-match condition, whose notification
  may carry the line's fields.
- **Placement:** a new step 14a. The step table at the top of GCP, step 11 and step 15 stay as they are, because
  other open PRs edit them.
- **Unchecked:** every external page was read through search excerpts only. Also unchecked: whether the December 2026
  default also turns off an explicitly enabled mailbox; whether a GoDaddy tenant offers the Microsoft 365 admin
  center, Exchange Online PowerShell or per-user MFA; the SMTP error codes; whether a copy on physical backups is
  offered for this project; `secureserver.net`'s SPF lookup count; and both alerting prices.
- **Privacy:** the Microsoft route adds a processor for the sign-in mails that the text in force (2026-10-26)
  doesn't name: it names Google Workspace for `support@` and nobody who sends sign-in mail. C2 must name the sender
  (Microsoft through the GoDaddy tenant, with the Sent Items copy, or the fallback provider) and Microsoft for
  `support@` (D214). The drill adds no processor and no place. The proposed counter carries no personal data.
- **Consequences:** `OPERATIONS.md` → GCP → 14a; `STATUS.md`.

## D240 — D53's per-IP limit on GCP: in api and ingest themselves, keyed by `XFF_TRUSTED_HOPS` (2026-11-18; amends D53, D155, D222)

- **Context:** D53's limit (20 requests a second per client address, bursts of 50, before any token) was Traefik's
  middleware on EKS. On Cloud Run nothing in front does it, so requests without a token reached api and ingest
  unthrottled; max instances (api 3, ingest 2) cap the bill, not availability. `API.md` still documented it. The
  user decided on 2026-11-18 to bring it back now; an external Application Load Balancer with Cloud Armor follows
  in a later PR, so the limit must keep working behind it.
- **Decision:**
  - **Where:** `internal/app`, Edge services only (api, ingest), between SecurityHeaders and the start gate:
    `Instrument → Recover → SecurityHeaders → limit → gate`. So the `429` carries `X-Request-ID`, the security
    headers and an access line, and a flood during a cold start is refused at once instead of waiting for it.
  - **Values:** D53's, 20/s, bursts of 50, a token bucket per client key, in memory per instance (stdlib).
  - **The answer:** Traefik's, as `API.md` documents it. Status `429`, body `Too Many Requests` (no newline,
    `text/plain; charset=utf-8`, no JSON) and no CORS headers. `Retry-After` is the wait rounded up, at least 1
    (with 20/s it is always 1). `X-Retry-In` is the wait as a Go duration (`37.5ms`).
  - **The client** (`httpx.ClientKeys`, one per process, shared by this limit, `/v1/admin`'s limit and the
    sampled `client address` line, so they agree):
    - walk `X-Forwarded-For` from the right over all its lines;
    - skip Google's front-end ranges without counting them (as D222);
    - the client is the `XFF_TRUSTED_HOPS`-th remaining entry, and nothing left of it is read;
    - IPv6 is cut to its /64.
  - **`XFF_TRUSTED_HOPS`** is 1–8, default 1. A required key of `deploy/run/config.env`, rendered into api and
    ingest only (D238's pattern), so the count in force is the one in git. It is 1 today: Cloud Run's front end
    appends the caller's address. Behind the external Application Load Balancer it is 2: the LB appends
    `<client>,<lb-ip>`, which that PR's step-9-style `xff_index` check confirms.
    - **2 only with ingress `internal-and-cloud-load-balancing`**, in the same PR. With ingress `all`, a caller
      that reaches `run.app` or the domain mapping directly arrives with one appended entry, and a count of 2
      would take the forged entry left of it. `scripts/check_cloudrun.py` refuses that combination.
  - **The fallback:** a missing header, fewer countable entries than the hop count, or an entry that doesn't parse
    before the client's is reached: one shared key (`RemoteAddr` is no longer read; it was the admin limit's
    fallback). It is safe because on Cloud Run every request through Google's front end carries the entry Google
    appended. Only a wrong hop count or a path that isn't Google's front end lands in the fallback. Those requests
    share one bucket, so the limit still holds (it fails closed) and nothing a client controls becomes a key of
    its own. `RemoteAddr` on Cloud Run is Google's front end anyway, a key every client would share.
  - **Memory bound:** `ratelimit.Capped`, the admin limit's `addrLimit` moved into `internal/ratelimit` with a
    configurable idle time and one map. The edge limit keeps at most 65,536 keys per instance, about 6.3 MiB at
    the cap (measured). A key idle for a minute is dropped: its bucket is full again after 2.5 s, so nothing is
    lost. The sweep runs every 1,024 calls, and at most once a second while the map is full.
    - **When the map is full:** a known key keeps its bucket. Every new key shares one overflow bucket (20/s
      together), so a flood of new addresses fails closed for the newcomers and leaves the known ones alone.
    - The admin limit keeps its values (10/s, bursts of 20, 4,096 keys, 10 minutes) on the same type.
  - **Keys are HMACs:** HMAC-SHA256 of the normalized address under a random 32-byte key made at start, cut to
    16 bytes. Neither limit's map, nor a heap dump of it, holds a plain address, and a key means nothing outside
    the process.
  - **Exempt** (they take no token):
    - the health paths (`/health/live`, `/health/ready`, and compose's `/healthz` and `/readyz`): Cloud Run's
      probes arrive without `X-Forwarded-For` and would share the fallback, and the uptime checks must not fail
      because someone floods. `/health/ready` reuses its answer for 5 s, so a flood there costs no database
      call;
    - `POST /internal/tick`: Pub/Sub's push of the tick, the only push api and ingest take. Its token is the
      tick's to check. `TestEdgeLimitExemptsEveryPushOfApiAndIngest` fails if the topology gains a push to
      either service on a path the limit counts;
    - on api, a request to `/v1/admin/*` bearing `ADMIN_API_KEY`, compared in constant time by the admin
      chain's own code (`Spec.LimitExempt`, `adminKey`). This keeps D222's guarantee that a flood can't lock
      the break-glass key out.
    - Preflights are counted like any request (Traefik counted them too).
- **Privacy (D222):** the limit logs nothing. A refusal shows only as `ryft_rate_limited_total{class="ip"}` and as
  the access line's `code` `ip_limit`. No address is in any log line, metric label or store, and the limit sets
  no trace attribute (`TestEdgeLimitLogsNoAddress`). The `client address` line's `picked` is `xff` or
  `fallback` (was `remote`).
- **Rejected:**
  - Counting hops without the Google-range skip. That changes what D222's step 9 proved for today's shape and
    gains nothing at hops 1.
  - Evicting the oldest key when the map is full (an LRU). A caller rotating /64s would evict real clients and
    get a fresh burst of 50 for each new key: the limit fails open.
  - `RemoteAddr` as the fallback, which keys nothing useful on Cloud Run.
  - Plain addresses as map keys.
  - A production switch to turn the limit off for tests.
  - Waiting for Cloud Armor, which is the next PR and per-LB, not per-instance.
- **Unchecked here:** the shape behind the LB (`<client>,<lb-ip>` with nothing more from Cloud Run's front end) is
  Google's documented header for the external Application Load Balancer, not observed. That PR's `xff_index`
  check decides between 2 and another value.
- **Consequences:**
  - The limits are per instance and reset at a cold start. One address gets at most max-instances × 20/s, and
    with scale to zero usually one instance's worth.
  - **The map can be held full from one IPv6 /48** (65,536 /64s, free from a tunnel broker). Touching each of
    them once a minute takes about 1,100 requests a second per instance, and a little more traffic from
    elsewhere drains the shared bucket. While that lasts, every client that is new or was idle for over a
    minute, testers included, gets `429` on api and ingest. The known keys keep their buckets. Keying IPv6 by
    /48, or counting a /48 once against the cap, would raise that bar. It is left for a follow-up if it is
    ever seen. Cloud Armor's rules in front (the next PR) are the answer.
  - **The tick is the one public path the limit doesn't guard.** A flood of fake ticks costs a token check (a
    parse, an RSA verification for a real Google key id) and two log lines each, bounded by max instances like
    any flood. Limiting it would let a flood that fills the map push Google's real tick into the shared
    bucket, and the brief exempts what Google pushes.
  - **Multi-line `X-Forwarded-For` is read last line first.** Whether Google's front end appends to the last
    line when a client sends several isn't documented. The runbook's burst 2b sends two forged lines and
    would show it.
  - Requests without `X-Forwarded-For` share the fallback bucket: local compose, `make smoke`, `make load`.
    The smoke is sequential and stays under it. `scripts/load/day.js` gives each k6 user an address of its
    own in 198.18.0.0/15, so the load test measures the services, not this limit.
  - Files: `internal/httpx` (`ClientKeys`; `Instrument` takes it), `internal/ratelimit` (`Capped`, `Idle`,
    `Clock`), `internal/app` (`iplimit.go`, `Spec.LimitExempt`, `Deps.Clients`), `internal/config`
    (`XFFTrustedHops`), `cmd/api` (`adminKey`, the admin limit on `Capped`), `scripts/cloudrun_render.py`,
    `scripts/check_cloudrun.py`, `deploy/run/{config.env,api.yaml,ingest.yaml}`, `API.md` (2026-11-18),
    `OBSERVABILITY.md`, `OPERATIONS.md` → GCP → The per-IP limit.

## D241 — `BETA_GATE` off: the beta has no invite codes (**the user's decision**, 2026-11-18; amends D238)

- **Context:** D238 set `BETA_GATE=warn` on api and ingest, with `on` after a stop rule and a code for every
  account that keeps access. On 2026-11-18 the user decided the beta gets no invite codes. The two unused
  codes (`beta 1 operator`, `beta 1 smoke`) were already revoked.
- **Decision:** `deploy/run/config.env` sets `BETA_GATE=off`. The key stays required (D238's reason holds: the
  mode in force is the one in git). Under `off` every signed-in account is served as under `warn`, and nothing
  is flagged as missing a code: no `X-RYFT-Gate: invite_required`, and `GET /v1/me/sync` → `gate` is never
  `invite_required` (it still says `consent_required` until the user consents). Access on
  the server is to come with App Attest, which is still to be decided. Until then nothing on the server tells
  the TestFlight app from any other client with a confirmed Supabase account.
- **Unchanged:** `CONSENT_ENFORCE` (not in `config.env`, so the default `on` holds): nothing is synced or
  scored without consent to the current policy. Also unchanged are the per-user limits, the code routes
  (`POST /v1/me/invite`, `/v1/admin/invites`) and the stored grants. An account that redeemed a code keeps
  its row. The per-IP limit on GCP (#211, D240) is merged and deployed (2026-11-19), with `XFF_TRUSTED_HOPS=1`.
- **Amends D238:** its "`on` before the beta is opened to anyone not invited" no longer holds. Who gets the
  app is TestFlight's list. The server admits any confirmed account. OPERATIONS → GCP → The beta gate keeps
  its steps for a later `warn` or `on`, behind a note that the gate is `off`.
- **Rejected:** staying on `warn`. It flags every tester (the app shows "Action needed: enter your invite
  code") for codes nobody will hand out.
- **The risk, which the user confirms with the PR:** anyone who signs up with Supabase (*Confirm email* is on,
  D159) and has the API's address can sync their own data after consenting. Each new account gets its own
  per-user budget, so the per-user limits don't cap many accounts; the per-IP limit (D240) and max instances
  cap the load and the bill. What is stored is theirs, under the same consent and deletion rules. App Attest is
  the answer that is still to be decided.
- **Privacy:** no new processor and no new data.
- **Consequences:** `deploy/run/config.env`; `OPERATIONS.md` → GCP → The beta gate (the note) and Beta → The
  gates and their modes (the GCP note); `scripts/smoke_prod.sh`'s header comment; `STATUS.md`. The merge's
  `deploy.yml` rolls api and ingest with `BETA_GATE=off`. The rollback is the same line back to `warn`.
- **Open, not here:** the site still tells testers the beta needs an invite code (`web/messages/en.json`,
  `web/content/legal/en/support.mdx`). Under `off` the app never asks for one. That text is a web PR of its
  own (the site is out of this one's scope).

## D242 — The edge: an external Application Load Balancer with Cloud Armor in front of api and ingest, in four phases (2026-11-18; amends D217, D222's logging rule, D224, D226; **the user's decisions, 2026-11-19:** the stop line €45 from `built` on, Cloud Run's request log of api and ingest excluded below 5xx, about €28 a month)

- **Context:** api and ingest are public on Cloud Run through domain mappings (D217), ingress `all`, and limit each
  address per instance (D53, D240). Nothing sits in front of them: no WAF, no limit across instances, and a flood
  spread over instances meets each instance's own bucket. The head-architect's brief (2026-11-18) asks for an edge
  with Cloud Armor, built so that nothing moves until it is proven, and merged with nothing planned. Prices and
  Google's limits below were read from search results (WebFetch was not available); each is marked *(search)* with
  its page.
- **The user's decisions (2026-11-19), no longer open:**
  1. the stop line is **€45 from `built` on**: the budget "RYFT 50 EUR"'s existing 90 % mail, so no new threshold
     rule (below, The stop line);
  2. **Cloud Run's request log of api and ingest is excluded below 5xx** (exclusion (2), `run-requests`, with
     `built`);
  3. **about €28 a month is accepted** (below, Cost).
- **Decision:**
  - **A gate, four phases** (`edge` in `infra/gcp/gates.auto.tfvars`, `off | built | live | locked`, default `off`,
    refused unless `serve` is true; each flip a reviewed one-line PR, never a workspace variable):

    | Phase | The apply | One step back |
    | --- | --- | --- |
    | `off` | nothing: the plan of the merge is today's (no edge resource, 16 APIs, the sink and dashboard as they are) | — |
    | `built` | the load balancer, Cloud Armor, the certificates, `compute` and `certificatemanager` on the keep list, the sink's two exclusions, two log-based metrics, four dashboard tiles. Nothing points at it | `off`: all destroyed; the two APIs stay on (`disable_on_destroy = false`) |
    | `live` | `api.` and `ingest.` become A records at its IPv4 address (TTL 300); the mappings stay as the way back | `built`: A back to CNAME |
    | (deploy PR) | ingress `internal-and-cloud-load-balancing` and `XFF_TRUSTED_HOPS=2` on api and ingest, within the hour after `live` | revert it |
    | `locked` | the two domain mappings destroyed, after 7 clean days | `live`: the mappings come back, but their certificates issue only once DNS points at ghs again |
  - **The load balancer** (`infra/gcp/edge.tf`): global external Application LB (`EXTERNAL_MANAGED`), one global IPv4
    address (Premium, as every global address is), a serverless NEG and a backend service per service (the policy,
    `log_config` 1.0, no health check, no `timeout_sec`, no CDN; protocol `HTTP`, the API's default, because Google's
    setup with a serverless NEG passes none and documents it as ignored *(search)*), a URL map by host (any other host
    is a 301 to `https://ryftlab.de/` and reaches no backend), an SSL policy `MODERN` with TLS 1.2 at least (Google's
    default admits 1.0), Certificate Manager certificates by DNS authorization in one certificate map (both go ACTIVE
    while DNS still names the mappings), 443 and a redirect on 80 on the same address.
  - **The policy** (Cloud Armor Standard, one policy, rules inline): six WAF rules, one per preconfigured set
    (`sqli`, `xss`, `lfi`, `rce`, `scannerdetection`, `protocolattack`, all `v33-stable`, sensitivity 1,
    `deny(403)`), **in preview**, at priorities 1000 to 1005, each enforced on its own later by one line of a local
    map; one **enforced** `rate_based_ban` at 2000 keyed on `IP`; the default rule allow (no geo blocking); JSON
    parsing `STANDARD`, log level `NORMAL` (VERBOSE would log matched body snippets: health data). The WAF rules come
    first because a throttle's conform action `allow` ends the evaluation.
  - **The rate values:** 300 requests per 10 s per address, then `deny(429)`; a ban at 6,000 per 300 s, for 600 s.
    300 is above the most D53's in-process bucket admits in 10 s (50 + 20 × 10 = 250), so the instance's limit acts
    first; above one user's per-user ceilings on both hosts together (API.md: api 60 + 10/s = 160 per 10 s, ingest's
    sync 60 + 2/s = 80: 240); below three api instances' worth (750), so a flood spread over instances ends at the
    edge. The ban counts only the requests over the rate limit (the API reference *(search)*): 6,000 of them in five
    minutes is about 50 a second sustained, which no app does. Within Google's limits *(search)*:
    `interval_sec` one of 10, 30, 60, 120, 180, 240, 300, 600, 900, 1200, 1800, 2700, 3600; a `rate_based_ban`'s counts
    1 to 10,000; `ban_duration_sec` one of 60, 120, 180, 240, 300, 600, …. The uptime checks (2 hosts × 3 locations
    every 5 minutes), the smoke (sequential) and the tick (Pub/Sub to `run.app`, never through the edge) are nowhere
    near it.
  - **IPv4 only:** no IPv6 address and no AAAA. Cloud Armor documents no prefix key for a rate limit *(search: ALL,
    IP, HTTP-HEADER, XFF-IP, HTTP-COOKIE, HTTP-PATH, SNI, REGION-CODE, TLS JA3/JA4, USER-IP, ASN)*, so an IPv6 client
    would get a key per address of its /64. IPv6-only phones come in through their carrier's NAT64. Once `locked`, no
    /48 can fill D240's map.
  - **Logs** (`logging.tf`): the load balancer logs every request (sample 1.0), and from `built` on the sink
    `ryft-logs` takes everything except two exclusions: (1) the load balancer's entries that are neither a Cloud Armor
    denial nor a preview match, `resource.type="http_load_balancer"` and `NOT (jsonPayload.enforcedSecurityPolicy.outcome="DENY"
    OR jsonPayload.previewSecurityPolicy.outcome="DENY")` (field names from Cloud Armor's request-logging page
    *(search)*: a throttle that exceeds logs outcome DENY with `rateLimitAction`; `previewSecurityPolicy` appears only
    when a preview rule matched); (2) **the user's decision (2026-11-19)**, Cloud Run's request log of api and ingest
    below 5xx (`logName=…/logs/run.googleapis.com%2Frequests`, `resource.labels.service_name=("api" OR "ingest")`,
    `httpRequest.status<500`). Today it keeps every caller's IP, URL and user agent for 14 days. Their 5xx stay: they
    are the only record of a failure Cloud Run makes itself (a request timeout, no instance), which the services'
    access line never sees, and the 5xx alert's text reads them (`alerts.tf`, unchanged). scoring's and notify's request
    logs stay whole: their callers are Pub/Sub's pushes from Google's addresses, and the event backlog alert reads their
    429s. Without (2), an IP would be kept for every request to api and ingest. What no sink takes is stored nowhere
    (`_Default` is off, `_Required` takes audit logs only).
  - **Enforcing a WAF rule** (runbook 16 → Enforcing a WAF rule): after 7 days with no preview match, at that rule's
    priority, on a request the backend answered below 400. The app's real requests carry a token and succeed;
    scanners probe a public host every day and get 401 or 404. "No match at all" would never come.
  - **Metrics and tiles** (`monitoring.tf`, from `built` on): `ryft_edge_denials` and `ryft_edge_preview_matches`,
    counted from the kept entries, one label `priority` (a closed set: 1000 to 1005, 2000), never the IP (the rate
    rule's `rateLimitAction.key` holds it), a URL or a user agent; tiles for the load balancer's requests by backend
    and code class, its 429s by backend, Cloud Armor's blocked or not by backend, and the denials and preview matches
    by priority. **No alert policy** (the cap of five stays). At `off` the dashboard is today's, byte for byte.
  - **DNS** (`dns.tf`): one record set per host whose type and data follow the gate (`google_dns_record_set.host`,
    moved from `.mapping`), because Cloud DNS can't hold a CNAME and an A at one name, and two resources could create
    the A first. `type` isn't ForceNew: the provider writes the change as one Cloud DNS change (delete the CNAME, add
    the A), so `live`'s plan is 0/2/0. A `-/+` would leave a gap that resolvers cache as NXDOMAIN: the runbook makes it
    a stop.
  - **The order, and why:** `built` → `live` → the deploy PR within the hour → `locked` after 7 clean days. In `live`
    with `XFF_TRUSTED_HOPS=1` every request through the load balancer keys the in-process limit on the load balancer's
    own address (the LB appends `<client>,<lb-ip>`; `clientip_test.go`'s "hops 1 behind the load balancer" pins
    it, `xff_index` 4 of 5): one bucket per
    instance for everyone, so the deploy PR follows at once. The mappings go last: until then reverting the deploy PR
    and `live` → `built` is the whole way back with valid certificates. In `live` the mappings' certificates can no
    longer renew (DNS doesn't point at ghs), so `live` is not a place to stay for months either.
  - **check_cloudrun's order rule** (`scripts/check_cloudrun.py`, added once #211 had merged; it reads `edge` from
    `infra/gcp/gates.auto.tfvars`, `off` when no line sets it, and refuses a value outside the four or a second
    line): ingress `internal-and-cloud-load-balancing` on api or ingest is refused unless `edge` is `live` or
    `locked` (with `off` or `built` DNS names the mappings, which that ingress refuses: everyone locked out), and
    with `XFF_TRUSTED_HOPS=1` (every client in the load balancer's one bucket); `edge = "locked"` is refused unless
    both have it. With D240's rule (a count above 1 only with that ingress) the deploy PR is exactly the pair. So
    the order built → live → deploy PR → locked holds in CI, and the reverse for a rollback (`live` → `built` is
    red until the deploy PR is reverted). CI's `smoke` (its test) and `manifests` (`make cloudrun-check`) jobs run
    when `gates.auto.tfvars` changes.
  - **The apply account's roles** (runbook GCP 16.1, as 2d): `roles/compute.loadBalancerAdmin`,
    `roles/compute.securityAdmin` (security policies; the attach needs `compute.backendServices.setSecurityPolicy` and
    `compute.securityPolicies.use`) and `roles/certificatemanager.editor` (includes `certmaps.use` and
    `dnsauthorizations.create` *(search)*), with a `gcloud iam roles describe` check. The plan account's
    `roles/viewer` covers the reads (16.1 checks it).
- **Rejected:**
  - Two WAF rules with three sets each: $4 a month cheaper, but a set could not be enforced on its own.
  - `XFF_IP` as the rate key: a client writes `X-Forwarded-For`.
  - IPv6 on the load balancer: no prefix key (above).
  - The throttle before the WAF rules (a lower priority number): its conform `allow` would end the evaluation, and the
    WAF rules would never run.
  - A custom role for the apply account: one more object to maintain by hand for an account that already holds
    `projectIamAdmin` (2d's note: owner-equivalent in effect); predefined roles, the narrowest that cover it.
- **The Compute Engine API's side effects** (a project without an organisation): enabling it creates the default
  service account `<number>-compute@developer.gserviceaccount.com` with **Editor** *(search,
  docs.cloud.google.com/compute/docs/access/service-accounts)* and usually the network `default` with four firewall
  rules. Nothing here uses either. Runbook 16.3, right after `built`'s apply, removes the binding, disables the
  account and deletes the rules and the network. `infra/gcp` manages no project-level binding authoritatively
  (`tests/iam.tftest.hcl` refuses a `google_project_iam_binding` or `_policy`), so nothing fights that.
- **Cost a month** (prices *(search)*):
  - forwarding rules: $0.025 an hour for the first 5 (both rules fit) = **$18.25** at 730 hours; $0.01 an hour for
    each beyond (cloud.google.com/load-balancing/pricing);
  - data processing: $0.008 per GiB in and out on that page's example; outbound $0.008–0.012 by region (Google's 2022
    announcement); europe-west1's rate unverified; under $0.10 at the beta's volume;
  - Cloud Armor Standard (cloud.google.com/armor/pricing): a policy $0.006849315 an hour (**$5.00**), a rule
    $0.001369863 an hour ($1.00), $0.75 per million requests. The page bills rules in preview the same. 8 rules (6 WAF,
    the rate rule, the default) = **$8**;
  - Certificate Manager: the first 100 certificates $0 (cloud.google.com/certificate-manager/pricing); 2 used: $0;
  - **total about $32 a month (≈ €28)**: $18.25 + $5 + $8 + under $1 of requests and data. $2 above the brief's
    $25–30, because every rule is billed. **Accepted by the user (2026-11-19).**
  - In a flood, every denial and ban is a kept entry (about 1.5 KB): a million is about 1.4 GiB of Cloud Logging, inside
    the free 50 GiB a month, and about $0.72 a million beyond it (on top of Cloud Armor's $0.75 a million).
  - Unverified: whether the default rule is billed as a rule (counted here); whether preconfigured WAF evaluation adds
    a per-request fee in Standard; the global address while a forwarding rule uses it (taken as $0); DNS
    authorizations and certificate maps (no price found); europe-west1's processing rate; egress (paid through Cloud
    Run today, the same bytes).
- **The stop line** (D226): the €20 line is below the edge's fixed ~€28, so once `built` the edge alone would cross
  it. **The user's decision (2026-11-19): from `built` on the line is €45**, which is the budget "RYFT 50 EUR"'s
  existing 90 % mail: no new threshold rule. An optional €20 mail (`percent=0.40`) is not part of the runbook; if
  one was added, it is removed (runbook → The stop line). Below `built` (and back at `off`) the line stays €20. The
  stop line's commands still close api and ingest (the load balancer calls Cloud Run as `allUsers`), but the load
  balancer keeps billing under it; cutting its cost means going back `live` → `built` → `off` (and from `locked`
  first to `live`).
- **Privacy:** the load balancer's entries hold the client's IP, the URL and the user agent. With the user's decision
  (2) the stored IPs are only those of blocked, flagged or failed requests, 14 days, in `ryft-logs` (EU). **What the
  privacy text (C2, not this PR) must name:**
  - the load balancer's and Cloud Armor's logs (Google Cloud, already a processor), only for requests Cloud Armor
    denied or a preview rule flagged;
  - Cloud Run's request log, only for requests that failed with a 5xx;
  - in each: the IP, the URL and the user agent;
  - kept 14 days.

  The purpose is security: blocking attacks and abuse, and finding server errors. This **amends the head-architect's
  "no IP in any log line"** (D222's context) for Google's logs only: our own lines still hold none. Design §8's
  planned C2 line ("the IP address and user agent of every API request, in Google's request logs, 14 days") is
  untrue from `built` on and needs this list instead. Until `built` the text in force is unchanged (no edge, and
  Cloud Run's request log is kept whole, as today).
- **Unverified here:** every external page (search excerpts only); the backend protocol's effect on the hop to Cloud
  Run (built's 33/31 MiB check decides); that the load balancer appends exactly `<client>,<lb-ip>` (built's XFF check
  decides); that the provider changes a record set's type in place (read in its source; the plan shows it, and a
  `-/+` is a stop); the
  Cloud Monitoring name of the log-based metrics' resource (the tile has no `monitored_resource` selector);
  `networksecurity…/https/request_count`'s labels; which certificate an unknown SNI gets; how long a
  domain-mapping certificate lasts once it can't renew; that Pub/Sub's push from this project counts as internal
  traffic under the new ingress *(search: docs.cloud.google.com/run/docs/securing/ingress)*, which the deploy PR's tick
  check decides.
- **Consequences:** `infra/gcp/{edge.tf,variables.tf,gates.auto.tfvars,locals.tf,project.tf,run.tf,dns.tf,logging.tf,
  monitoring.tf,outputs.tf}`, `tests/edge.tftest.hcl` and the other tests pinned to `edge = "off"`;
  `OPERATIONS.md` → GCP → 16 (and 2a, 2d, the stop line, When an alert fires); `OBSERVABILITY.md`;
  `infra/gcp/README.md`; `STATUS.md`; and, once #211 (D240) had merged, in the same PR: `scripts/check_cloudrun.py`
  and `scripts/tests/check_cloudrun_test.sh`, CI's `smoke` and `manifests` path filters, one `clientip_test.go`
  case.
- **Status:** `live` since 2026-11-19 (#216 applied, the deploy PR #217 merged); `locked` after 7 clean days.

## D243 — On a push to main, the web jobs run only when the web paths changed since main's last green commit (**the user's decision**, 2026-11-19; amends D107)

- **Context:** D107 runs every check of `ci.yml` on a push to main, `web-checks` and `web-e2e` included.
  `web-e2e`'s Lighthouse step flakes on Total Blocking Time (#214: a median of 154 ms against 100). That turned
  `ci-ok` red and stopped `deploy.yml` at "wait for ci-ok" on a commit that changed no web file. The edge's next
  steps (`live`, the deploy PR, `locked`) are non-web pushes, and the deploy PR is time-bound. The user decided
  that on main the web jobs run only when the web paths changed. The 100 ms budget stays for web changes.
- **Decision:**
  - On a push to main, the `changes` job's step `since-green` takes the newest green `ci` run on main. That is
    a push or a manual run, other than the current one.
  - The step compares that run's commit with this one through the compare API (`actions: read`,
    `github.token`, no new secret).
  - `web-checks` and `web-e2e` are skipped only when none of the web paths changed, a rename's old name
    included. The web paths are the PR filter's `web` list: `web/**`, `docs/SCORING.md` and `ci.yml`.
  - The list gains `.githooks/**`, `scripts/check_bytes.sh` and `scripts/lib/charpolicy.py`.
    `web/tests/hooks.test.ts` runs them, and until now only main's every-push run did when a PR changed
    them alone.
  - When they are skipped, a `::notice` "web checks skipped" names the base commit and its run.
  - Main's commit message keeps its brand check: a step in `manifests`, which runs on every push. Until now
    `web-checks`' `npm run brand` did that check; its file scan covers only `web/`.
- **Why since the last green commit, not the parent:** `deploy.yml`'s `site` job ships `web/` whenever it
  differs from the live release. A red web commit followed by a non-web one would ship the red `web/`
  unchecked. Counted from the last green commit, the induction holds: every green commit's web paths passed
  `web-e2e` on a commit with the same ones.
- **Fail-safe:** the web jobs run when:
  - the runs or the compare can't be read;
  - no run is green;
  - the base commit isn't an ancestor of this one (`ahead` or `identical`, behind by 0);
  - the compare lists no files, or 300 or more (its cap, so the list may be truncated);
  - the step breaks or hangs (`continue-on-error`, a 2-minute timeout; `skip` is written last).

  A manual run executes everything, as before. Every other job keeps running on every push to main.
- **Accepted:**
  - Only green runs count, so a newer red run on the same web paths doesn't stop a later skip. This covers
    a manual run that went red after a repository variable baked into the site changed (`TESTFLIGHT_URL`,
    `SUPPORT_EMAIL`, which are no path).
  - After changing such a variable, run `gh workflow run ci.yml --ref main`. Don't start `deploy.yml` by
    hand until that run is green.
- **Consequences:**
  - `.github/workflows/ci.yml`: the `changes` job, the `web` filter, the two web jobs' `if`, and `manifests`'
    message check.
  - `OPERATIONS.md` → GCP → Deploy, roll back, rotate → The site; `STATUS.md`.
  - The paths in `since-green` and in the PR filter's `web` list change together (a comment at both).

## D244 — The sixth condition: feedback and admin access, one log-based counter (**the user's decision**, 2026-11-19; amends D236, builds D239's decision 3)

- **Context:** since D236 no alert told the operator of a feedback (Grafana's NewFeedback) or of an admin
  sign-in (`AdminWebUsed`, the lost-phone detection). D236's cap of five conditions is a test. D239's
  decision 3 proposed a sixth for feedback alone and left it to the user. The user said yes on 2026-11-19,
  with the admin page in the same condition. PR A builds it: ticket A1 the api's new line
  (`backend/cmd/api`), ticket A2 the metric and the policy (`infra/gcp`).
- **The kinds, and the events behind each.** All three are INFO lines on api's stdout:
  - `feedback received` (`feedback.go`): once per report stored. Its keys (`feedback_id`, `category`,
    `request_ids`, logx's request keys) stay in the log.
  - `admin signed in` (A1): once per operator admin session per instance, when an operator's token passes
    with a TOTP at most 10 minutes old. A fresh TOTP marks a session's start. The page asks for the code
    at sign-in and again after 12 hours (`mfa_required`), and a token keeps the time of its TOTP (`amr`)
    through its refreshes, so every later request of the session carries an older one. The line has no
    key of its own (no `user_id`, no `admin_actor`).
  - `admin auth rejected` (`adminauth.go`'s `reject`), but not for `reason` `missing` (no bearer: crawlers
    on `/v1/admin/*`) or `expired` (the operator's own page with an old access token; it refreshes once and
    goes on). A negative list on purpose: a reason added later counts by default.
  - **Not counted:** the key's use (scripts, `make smoke-prod` with the key; each call is audited, and
    counting it would make every script run a notice), and `admin second factor required` (a real operator
    without a fresh second factor: an audit row, and the page handles every 12-hour re-challenge itself).
- **The metric** `ryft_notices` (`monitoring.tf`): `DELTA`, `INT64`, like `ryft_ticks`. Its filter is
  `resource.type="cloud_run_revision"`, `resource.labels.service_name="api"`, the stdout log, and the three
  messages with the two reasons left out. Every OR group has its parentheses, because in Logging's query
  language OR binds tighter than AND. One label, `kind` = `EXTRACT(jsonPayload.message)`: the three
  messages, a closed set by the filter. Not gated, free like the others.
- **The policy** "RYFT · Feedback and admin access" (`alerts.tf`, a third block, `count = local.alerting ?
  1 : 0`, both channels, severity WARNING). A classic threshold on
  `logging.googleapis.com/user/ryft_notices`: `ALIGN_SUM` over 300 s, `REDUCE_SUM` by `metric.label.kind`,
  `> 0`, held 60 s, trigger 1, `evaluation_missing_data` `EVALUATION_MISSING_DATA_INACTIVE`, `auto_close`
  1800 s, `notification_prompts` `["OPENED"]`. The documentation's subject is `RYFT · ${metric.label.kind}`.
  Its text gives the first look per kind: `/admin` → Feedback or `scripts/beta_report.sh feedback`; the
  lost-phone runbook (OPERATIONS → Beta → Admin sign-in → Lost phone, or any doubt about the admin account)
  and `/admin` → Audit; Logs Explorer for the reason. It holds "First look" and `alert_look` like the others.
  - **Why a classic threshold:** D236 left `increase()` over a sparse `DELTA` counter unverified, and one
    feedback is one point. A 5-minute sum `> 0` holds for that one point.
  - **Why missing data inactive:** without it, an incident opened by one feedback stays open until
    `auto_close` (30 minutes without data), and a second feedback in that time falls into the open incident
    without a notification. With `INACTIVE`, missing data counts as not firing. The incident closes once the
    5-minute window is empty, and the next line opens a new one and notifies again. Google says the setting
    needs a duration of at least 60 s (it is ignored at 0), hence 60 s. Only the opening is notified, so
    there is no closing mail for every feedback.
  - A flood (a scanner's bearers) stays one incident while the lines keep coming: the series stays above 0.
  - **Schema:** `terraform validate` checked every field against the pinned provider, 8.11.0
    (`.terraform.lock.hcl`). `notification_prompts`, `documentation.subject` and `evaluation_missing_data`
    are in it, and none was left out. `terraform providers schema -json` needed an approval this headless run
    didn't have, so validate was the check: it fails on any argument the schema lacks.
- **The price** *(search)*: Google's pricing page (https://cloud.google.com/products/observability/pricing)
  was read only through search snippets, on 2026-11-19. Monitoring charges for alerting no sooner than
  2027-09-01. Then it charges $0.35 a month per metric reference in a policy, and $0.50 per million points
  returned. This policy has one metric reference and at most three series (one per kind), usually none. So
  it costs $0 until then and about $0.35–0.45 a month after. At most 3 series × 43,800 one-minute
  evaluations ≈ 0.13 M points ≈ $0.07, if it is evaluated every minute. The brief's $1.50 is D236's
  reading A, which is no longer on the page. **Unverified:** the date and both rates from the page itself,
  how often a threshold condition is evaluated (the points), and whether the free allotment applies.
- **Rejected:**
  - A log-match condition: its notification may carry the line's fields, `user_id` among them (logx's
    request keys).
  - Three metrics, one per line: three metric references, three prices, the same signal.
  - PromQL: `increase()` over the sparse counter is unverified (D236).
  - A key added to each line to filter on: a change to two existing lines and their key tests, for nothing
    the message doesn't already say.
  - Counting the key: every script run would notify.
  - A dashboard tile: it would change the dashboard in this plan. It is a free follow-up PR.
- **Lost, or unchecked:**
  - Per-instance dedupe. Two instances that serve a session's first 10 minutes write two `admin signed in`
    lines. Within 5 minutes they are one incident; further apart, two notices. An instance that joins
    later sees an older TOTP and writes nothing.
  - A stolen access token whose TOTP is over 10 minutes old writes no `admin signed in`. The audit read
    stays (OPERATIONS → Beta → Admin sign-in).
  - A `LOG_LEVEL` above `info` silences all three lines, and the policy with them (as with condition 3).
  - The rendering of `${metric.label.kind}` in the subject and the text. Google's documentation-variables
    page says a label kept by `group_by` is available, and a value that doesn't start with a letter is left
    out (all three start with one). 14a.9 checks it.
  - The latency: a log-based metric's points arrive a minute or two after the line. With the 60 s hold
    and the evaluation, that is about 5 minutes.
  - One incident per kind: while one is open (its line in the last 5 minutes), another line of that kind
    notifies nothing of its own. A second sign-in within about 5 minutes of yours, someone else's, is no
    second notice: read the incident's chart (two points) and the audit after every sign-in alert.
  - The policy is created right after its metric, and a "Cannot find metric(s)" error is likely while
    Monitoring doesn't know the new descriptor yet: the pinned provider (8.11.0) retries only concurrent
    edits, not that error (the review read it in the provider). 14a.9 plans again then. Not seen here.
  - The price (above).
- **Privacy:** no new processor: Monitoring, Logging and the Google Cloud app are Google's, as since D236.
  `admin signed in` has no key of its own. The label holds the message only, a closed set, and no id
  becomes a label. The mail and the push carry the policy's name, the kind and the documentation's text,
  which names no account, no report and no reason. A test pins that: no `user_id`, `email`, `feedback_id`,
  `request_id` or `admin_actor`, and no `${log.…}` variable. The privacy text needn't change.
- **Plan counts:** with alerting on, 2 to add, 0 to change, 0 to destroy (`google_logging_metric.notices`,
  `google_monitoring_alert_policy.notices[0]`). With it off, 1 to add. The same under the stop line and
  with `serve = false`. No existing resource's address or attribute changed (comments only). The
  `monitoring` output gains the policy and the metric. A rebuild's apply 1 is 123 to add.
- **Tests** (`tests/monitoring.tftest.hcl`, `tests/edge.tftest.hcl`; `terraform test` 76 passed):
  - `six_conditions_at_most`: six with every gate open, three policy blocks.
  - Every loop over the policies includes the new one, and the output lists six.
  - Gate off: no notices policy, and the metric exists. The stop line keeps it (five policies), and so
    does `serve = false`.
  - The metric's filter exactly, its one label, and the forbidden-label regex.
  - The three messages and the two excluded reasons are the strings the api writes (`feedback.go`,
    `adminauth.go`, `internal/auth/supabase.go`), so a rename in Go fails the test instead of silencing
    the condition.
  - Go (`backend/cmd/api`): one `admin signed in` per operator and TOTP time, the window's edges and the
    30 s skew, and the line holds no `user_id` or `admin_actor`.
  - The condition's aggregation, comparison, threshold, duration, missing data, `group_by` and trigger.
  - The documentation's kinds, `/admin`, the script and the lost-phone runbook, with no identifier and
    no `${log.`.
- **Consequences:**
  - `infra/gcp/{monitoring,alerts,outputs}.tf` and `README.md`.
  - `OBSERVABILITY.md` → Cloud Monitoring: the third metric, six conditions, What D236 dropped, Cost.
  - `OPERATIONS.md` → GCP → 4 (123), 11, 14a.8 (built), 14a.9 (new: the plan, the apply, a test), and
    When an alert fires; Beta → Admin sign-in; Data protection (logs).
  - The user's apply is 14a.9.

## D245 — App Attest: the contract first, `403 app_attest_required`, an HMAC app token checked without a database read (**the user's decision**, 2026-11-19; amends D241; amended by D247)

- **Context:** since D241 the server admits any confirmed Supabase account, and nothing tells the TestFlight app
  from a script with a valid session. The user decided on 2026-11-19: App Attest, yes; the contract first and docs
  only, so the app can start; the server in a brief of its own, shipped in mode `off`. The head architect's draft
  is the base; every change to it is listed below with its reason. The contract's author read no external page:
  there was no web search tool, and `curl` needs approval. Apple's behaviour was written from Apple's documentation
  as known. The lead then confirmed part of it through search snippets on 2026-11-19 (*(search)*, listed below); the
  rest stays *unverified*, and the server PR checks it against a real device's attestation before `warn`.
- **Decision (the contract):** `API.md` → Conventions → App Attest, the Errors paragraph, the Swift mirror and the
  changelog of 2026-11-19:
  - `POST /v1/app-attest/challenge` (no body) → `200 {challenge, expires_at}`. Opaque, single use, for the account,
    5 minutes; at most 5 open per account. Any call that names it uses it up, whatever it answers.
  - `POST /v1/app-attest/keys` `{key_id, attestation, challenge}` → `201 {key_id}`. Apple's seven attestation checks:
    the chain to Apple's App Attestation Root CA, held by the server; the nonce in extension
    `1.2.840.113635.100.8.2`; the key id against the certificate's key and the credential id; the RP ID hash of
    `M77SKHKG63.dev.timkrebs.ryft` (the AASA's and `infra/gcp/monitoring.tf`'s `local.app_id`); counter 0; the AAGUID.
    The key is stored per account and key id. At most 10 keys per account (the eleventh deletes the least recently
    used), and a key unused for 90 days is deleted.
  - `POST /v1/app-attest/token` `{key_id, challenge, assertion}` → `200 {token, expires_at, ttl_sec}`, 3600 s. The
    key is looked up (`404 app_attest_key_unknown`), then the challenge, the signature over the nonce, the RP ID
    hash, and a counter above the stored one, which is then kept.
  - **Client data, byte-exact:** `clientDataHash` = SHA-256 of the UTF-8 bytes of the challenge string as received,
    for the attestation and the assertion alike. The challenge is never decoded. Today it is 32 random bytes in
    base64url without padding (43 characters), which the server PR keeps; the app may not rely on that. `key_id` is
    `generateKey()`'s string unchanged, standard base64 with padding (44 characters, 32 bytes). The attestation and
    the assertion are `base64EncodedString()` of the CBOR. One rule for both steps, so the app has one hash to get
    right.
  - **The header** `X-RYFT-App-Token` on every request to api and ingest, opaque to the app (at most 512 characters).
- **The token (the server's, opaque to the app):**
  - **Format:** `v1.<kid>.<body>.<mac>`. `kid` is 8 lower-case hex digits naming a ring key. `body` is base64url
    without padding of 48 bytes: the account id (16), the first 16 bytes of SHA-256 of the decoded key id, and the
    issue and expiry times (8 each, Unix seconds, big-endian). `mac` is HMAC-SHA256 under the ring key of
    `ryft-app-token/v1/` ‖ `kid` ‖ `.` ‖ `body`, in base64url (43 characters). About 120 characters in all.
  - **The check** (api and ingest, stdlib only, no database, no network): the version; a known `kid`; the MAC with
    `hmac.Equal`; not expired; issued at most 60 s in the future; the account id equals the bearer's `sub`. A
    deleted key's tokens run out within the hour: the price of no lookup.
  - **The ring:** the Secret Manager secret `app-attest-token-keys` in `ryftlab-prod`. The container is `infra/gcp`'s,
    the user's Terraform; the value is the operator's, never in git. It holds 1 to 3 lines `<kid>:<base64 of 32 random
    bytes>`. The first line signs (api); every line verifies (api and ingest). It is rendered as
    `APP_ATTEST_TOKEN_KEYS` into both services at a pinned version, as `posthog-project-key` is (D231).
  - **Rotation, planned:** three version pins in three PRs, so no token meets a service that cannot verify it:
    1. the new key as a second line (verify only);
    2. the new key first (api signs with it, ingest already verifies it);
    3. at least an hour after (2) is deployed, without the old key.
  - **Rotation in an emergency** (a leaked key): one version with a fresh key alone. Every token in use fails, and
    under `on` each install gets one `403 app_attest_required`, fetches a token and sends again: one challenge and
    one token per device. A leaked ring key alone opens nothing, since the bearer is still required: it is App
    Attest `off` for whoever also holds a session.
- **Modes and configuration** (required lines of `deploy/run/config.env`, D238's reason: the mode in force is the one
  in git):
  - `APP_ATTEST=off|warn|on`, api and ingest.
    - `off`: no check, the header is not read; the three routes serve once the ring is set.
    - `warn`: the token is checked and nothing is refused. The access line gets `app_attest` with the reason
      (`missing`, `invalid`, `expired`, `other_account`); a valid token adds nothing. The counter
      `ryft_app_attest_refusals_total{reason,mode,service}` counts it too.
    - `on`: `403 app_attest_required`, logged the same way.
  - `APP_ATTEST_EXEMPT_USERS`, api and ingest: comma-separated lower-case UUIDs, at most 5, may be empty. On GCP it
    holds the smoke user's id, so `deploy.yml`'s smoke and `make smoke-prod` pass under `on`. The id of a test
    account names nobody and is no secret.
  - `APP_ATTEST_APP_ID=M77SKHKG63.dev.timkrebs.ryft` and `APP_ATTEST_ENVIRONMENTS=production` (`production` or
    `production,development`), api.
  - The code's defaults (local compose): `off`, no exempt user, `production,development`. The routes answer `501`
    without a ring.
  - **No `warn` or `on` without the ring** (a requirement of the server PR). Without it every token would fail
    and the routes would answer `501`, so the app would send no header. Under `on` that refuses every app
    request, a lockout of everyone but the exempt routes and users. So:
    - api and ingest refuse to start when `APP_ATTEST` is not `off` and `APP_ATTEST_TOKEN_KEYS` is missing,
      empty or malformed (not 1 to 3 lines `<kid>:<32 bytes in base64>`, or a `kid` twice);
    - `scripts/check_cloudrun.py` refuses the same in CI: `APP_ATTEST` other than `off` while the rendered api or
      ingest has no `APP_ATTEST_TOKEN_KEYS` pinned to a version.
    - **What the app sees meanwhile:** nothing new. A revision that refuses to start never takes traffic; Cloud Run
      keeps serving the previous one, and `deploy.yml` goes red at the rollout. Under `off` without a ring, the
      routes answer `501` and the app sends no header, which `off` doesn't read.
  - **Order** `off` → `warn` → `on`. The rollback is the line back.
- **Exempt** (as written in `API.md`):
  - the health paths (`/health/live`, `/health/ready`, `/healthz`, `/readyz`) and `/metrics`: no bearer;
  - `POST /internal/tick`: Pub/Sub's push with its own token, the only push api and ingest take (D240);
  - **all** of `/v1/admin/*` with either bearer, preflights included: the site's `/admin` page calls it from a
    browser (`web/scripts/lib/csp.mjs`: `connect-src` is the Supabase auth path and `<api>/v1/admin/` only), which
    can never attest. The other pages' `connect-src` is `'none'`, so the site calls nothing else;
  - the three App Attest routes, which are how a token is had;
  - `DELETE /v1/me`, `POST /v1/me/apple-authorization` and `DELETE /v1/me/consent`, which covers the lead's
    "data-subject routes". Erasure, its Apple half (Apple requires the revocation at deletion, and the code
    expires in 5 minutes, which a failed attestation must not eat) and the withdrawal of consent (Art. 7(3)) may
    not depend on Apple's service, the device, or the server's token path. A bug in the token path under `on` must
    never trap someone who wants out. They only remove, and they still need a valid session. A stolen session can
    already change the account's email at Supabase, so the exemption adds little to it;
  - `DELETE /v1/devices/{id}/push` (the route in `cmd/api/main.go`; the lead's review): sign-out's
    unregistering only removes a push token. A device that has no token at sign-out would otherwise keep
    getting the account's pushes;
  - `POST /v1/feedback`: the report path has to work exactly when attestation fails. It is capped at 10 a day per
    account;
  - the accounts in `APP_ATTEST_EXEMPT_USERS`.
  - **Not exempt: the export.** The app has no export route; it builds the file from `GET /v1/me`, `/v1/days`,
    `/v1/workouts` and `/v1/sessions` (`DataExport.swift`), the reads App Attest is for. Portability from a device
    that cannot attest goes through support, as any request from outside the app does.
- **Changed from the draft:**
  1. `403`, not `401`, for `app_attest_required`. Today's builds refresh the session on a `401` and sign out on
     the second (`APIClient.swift`, API.md → Authentication). A `403` with a code they don't know reads to them as a
     built-in's `403`, never a sign-out. The same shape as `invite_required` and `mfa_required`.
  2. `app_attest_key_unknown` (`404`) is answered only on `POST /v1/app-attest/token`. Data routes check the
     token without a database read, so they cannot know it, and their own `404`s mean something else (a resource,
     an unknown route). The challenge and keys routes have no key to miss. The app branches on the code before its
     "404 = not live" rule.
  3. `422`, not `400`, for `app_attest_invalid`. API.md's `400` is a malformed request (a client bug), `422` a
     well-formed value that fails, as for the refused credentials of `POST /v1/me/apple-authorization` and
     `POST /v1/me/invite`. A malformed field stays `400` without a code.
  4. `challenge_expired` became `app_attest_challenge_invalid` (`422`). It follows the codes' feature prefix
     (`import_in_progress`, `import_not_enabled`), and one code covers unknown, used, expired and another
     account's, whose remedy is the same.
  5. The token answer gains `ttl_sec` (3600): the app counts the hour on its own clock, as with `GET /v1/config`'s
     `ttl_sec`, instead of comparing `expires_at` with a device clock that may be off.
  6. The exemptions above: all of `/v1/admin/*` and not only the key; the three data-subject routes;
     `DELETE /v1/devices/{id}/push`; `POST /v1/feedback`.
  7. The three routes are outside the beta gate. Under `BETA_GATE=on`, `POST /v1/me/invite` needs a token and a
     token needs the routes, so a gated route would be a deadlock. The `invite_required` row's route list gains
     them in the server PR.
  8. Refusing `off` → `on` is a new check, not "as D238 did". D238 states the order in the runbook and in
     `config.env`'s comment, and no check enforces it: `scripts/check_cloudrun.py` reads `BETA_GATE` only to render.
     The server PR adds a rule in `check_cloudrun.py` against the PR's base (`git show <base>:deploy/run/config.env`).
  9. `warn` logs a reason, not one code `app_attest`. The stop rule must tell "no header" (a simulator, an old
     build) from "a bad token" (a bug, or a forgery). The counter isn't read on Cloud Run (D236), so the access
     lines are the record.
  10. Keys are capped at 10 per account and deleted 90 days after their last use, not only at the account's
      deletion: data minimisation. The app's `app_attest_key_unknown` path covers it, and a TestFlight build expires
      in 90 days anyway.
  11. Development attestations are a config list, `APP_ATTEST_ENVIRONMENTS`, `production` on GCP. The app sets the
      entitlement to `production` in every build that talks to production, so a debug build from Xcode attests in
      production too *(search, the lead, 2026-11-19)*. TestFlight, App Store and Enterprise builds use production
      whatever the entitlement says.
  12. Until the privacy text names App Attest, the routes answer `501` (no ring). So nothing is stored before the
      text is in force, and the app, which calls the challenge route first, never attests at Apple before then.
- **Changed in the lead's review (2026-11-19):**
  - **Refused writes are kept.** `app_attest_required` never drops a queued write and never commits a HealthKit
    anchor: the app holds them as for a gate, renews once and sends again (`API.md` → Errors → App Attest, and the
    app's rules). Today's app drops a queued Train write on a `403` with a code it doesn't know
    (`../app/Ryft/Train/TrainSyncService.swift`, `isRetryable`). Under `on`, an old build loses its unsent
    `PUT /v1/sessions/{id}` and `POST /v1/sessions/{id}/complete`, so the stop rule for `on` counts `missing` lines
    from app builds (below).
  - **No `warn` or `on` without the ring:** a start refusal and a `check_cloudrun.py` rule (Modes above).
  - **One renewal per account and key at a time.** Requests refused meanwhile wait for it, so assertion counters
    don't collide and re-attestations don't use up `POST /v1/app-attest/keys`' 10 a day.
  - `DELETE /v1/devices/{id}/push` is exempt (above).
  - The signature check and the extension's structure are written out in `API.md` (the token and keys routes).
- **The checkers (this PR is docs only):**
  - `TestAPIDocExamples` (`backend/internal/model/apidoc_examples_test.go`) needs a `model` type for every fenced
    `json` block. Its mechanism for a documented type the code lacks (`docExampleAllowList` with `missingType`) is an
    edit of that Go test file. So the examples are fenced `http` exchanges, as in Admin routes, with their JSON
    bodies. The server PR adds the model types, turns them into annotated `json` blocks, and drops "not served yet".
  - `TestAPIDocErrorCodesMatchCode` pins Errors' `code` table and the mirror's `APIErrorCode` to the `model.Code*`
    constants, and the status table to the codes the services send. So the four codes are a paragraph in Errors, a
    table in App Attest and a comment after the enum, not rows or cases. The server PR adds them with the constants.
  - `scripts/decision_refs.sh` warns that no code mentions D245 until the server PR (a warning, exit 0).
- **Rejected:**
  - `401` (above).
  - A response header under `warn`, or `app_attest_required` in `X-RYFT-Gate`. The app renews by `ttl_sec`, so there
    is nothing for it to do; the app reads `X-RYFT-Gate`'s values as onboarding screens; and an additive header can
    come later.
  - A database read per request (a token table, or an assertion on every request): the brief wants none, and an HMAC
    costs microseconds.
  - A signed JWT (ES256): api would hold the private key and ingest the public one, which separates them a little
    better, at the cost of a JOSE library or hand-rolled JOSE. Both services are ours, and HMAC is the stdlib's.
  - Apple's receipt and fraud metric. At the beta's size there is nothing to act on, and it adds a call to Apple and
    stored data per device.
  - A purpose tag in the assertion's client data. The challenge is single use and per account, so a tag guards
    nothing, and one hashing rule for both steps is the fewer bug.
  - A token from `POST /v1/app-attest/keys`: one way to a token, so the counter's first step is always an assertion.
  - Exempting the export's reads (above).
- **Privacy, what the privacy text (C2) must say before the ring is set:**
  - The app uses Apple's App Attest to prove to RYFT's servers that a request comes from the genuine app on a genuine
    Apple device. The device talks to Apple for the attestation, under Apple's terms; RYFT's servers never contact
    Apple for it. C2 decides how to name Apple's role; there is no new processor on the server side.
  - RYFT stores, per account and app install: the key id (per device and install), the public key, a counter, the
    environment, and when the key was attested and last used. Challenges are kept 5 minutes. There is no receipt, no
    device model and no address; the app holds the token for an hour, and the server stores none.
  - Kept until the account is deleted, the key is replaced, or 90 days after the key's last use.
  - The purpose is security: refusing requests that do not come from the app (scripted access with a valid account,
    abuse).
  - **The account wipe:** `DELETE /v1/me` deletes the keys and the challenges; an account merge
    (`POST /v1/admin/users/{id}/merge-into/{to}`) deletes the merged account's keys. `delete_test.go`'s `owned` count
    covers both tables. The app's export doesn't include them (a device credential, like push tokens); an Art. 15
    request lists them.
  - The text and `PRIVACY_POLICY_VERSION` change in the same PR (`CLAUDE.md`), before `warn`.
- **Stop rules** (proposed; the server PR writes them into OPERATIONS → GCP):
  - `off` → `warn`: the text is in force, the ring is set, the attesting build is on TestFlight, and the operator's
    own device shows accepted tokens (its lines carry no `app_attest`).
  - `warn` → `on`:
    - **zero `missing` warn lines from app builds for 7 days in a row**. A request from an app build carries
      `X-RYFT-Device-ID`: every build sends it on every request (`APIClient.swift`), and scripts don't. So the warn
      line records whether that header was present, as a boolean, never its value (the server PR). A request without
      a token from an app build is a write that an old build would lose under `on`;
    - nothing else but known non-app clients (the simulator);
    - every tester on the attesting build (TestFlight's list);
    - `make smoke-prod` green.
- **Confirmed by search snippets (the lead, 2026-11-19, *(search)*: Apple's "Validating apps that connect to your
  server" and "Preparing to use the App Attest service", and reference implementations):**
  - the nonce = SHA-256(`authData` ‖ SHA-256(challenge)) in the OID `1.2.840.113635.100.8.2`;
  - counter 0 at attestation;
  - the AAGUIDs `appattestdevelop` and `appattest` + seven `0x00`;
  - an assertion's counter must exceed the stored one;
  - TestFlight, App Store and Enterprise builds always use production, whatever the entitlement says; the
    entitlement `production` makes a development build use production too;
  - development keys and receipts don't work in production, nor the reverse.
- **Unverified (Apple, as known; no page was read by the author):**
  - the rest of the attestation's seven steps (the chain, the key id against the certificate's key and the
    credential id, the RP ID hash), and the extension's structure `SEQUENCE { [1] { OCTET STRING } }`;
  - that the assertion's signature is ECDSA P-256 in DER over the message `nonce` with SHA-256, i.e.
    `ecdsa.VerifyASN1(pub, sha256(nonce), sig)` (the server PR pins it with a real assertion);
  - that "SHA-256 of the public key" means the 65-byte uncompressed point;
  - that `generateKey`'s id is standard base64 with padding;
  - `DCError.invalidKey` after a reinstall or a restore onto another device; `serverUnavailable` → the same key
    later; any other error → a new key; whether an attested key can be attested again;
  - that `generateAssertion` needs no network, and works in the background (HealthKit's background delivery);
  - Apple's advice of one key per user per device;
  - the receipt and its risk metric;
  - an attestation's size (a few KiB).
- **Consequences:**
  - This PR: `API.md` (Screens, Conventions → App token, Errors → App Attest, Conventions → App Attest, Endpoints,
    the Swift mirror, the changelog of 2026-11-19).
  - **The server PR:**
    - a pure `internal/appattest` (the checks, Apple's root pinned, tests from a real device's attestation and
      assertion) and the token functions with a calibration test of the format;
    - a migration of `app_attest_keys` and `app_attest_challenges` (only the challenge's SHA-256 stored), grants, RLS,
      the deletion and the merge;
    - the middleware in api and ingest, with tests for each exempt route and for the order (`401` before `403`).
      `POST /v1/train/import` sits outside the `/v1/` chain (`importRoute`, `cmd/api/main.go`), as `DELETE /v1/me`
      does, so the server PR wires the check into it itself, with a test that it answers `app_attest_required`;
    - the start refusal without a ring and its `check_cloudrun.py` rule;
    - the `model.Code*` constants, Errors' rows, the mirror's cases and the model types;
    - `deploy/run/config.env`, `scripts/cloudrun_render.py` and `scripts/check_cloudrun.py` (the keys, the pin, the
      order rule), and the secret's container in `infra/gcp` (the user's Terraform);
    - `OBSERVABILITY.md`, `OPERATIONS.md` and `STATUS.md`.
  - **D241's** "access on the server is to come with App Attest, which is still to be decided" is decided here.
    Until `on`, D241's risk stands.

## D246 — The AWS rest retired by hand; D3 removes the EKS era's code (**the user's retirement**, 2026-11-19; amends D227, closes the design's D2 as obsolete)

- **Context:** D227's order ended with Terraform: step 12 (E) destroys the Route 53 zone, step 13 (D1, D2) the
  Vault keys, the snapshot bucket and the budget, and step 14 the four old workspaces in `tim-krebs-org`. On
  2026-11-19 the user deleted that org, and every old state with it, so none of those plans can run. The user then
  deleted RYFT's AWS rest by hand, by ID, after a read-only inventory of account `173003892479` (eu-central-1 and
  the global services).
- **Decision (the retirement, done):**
  - deleted: the Route 53 zone `ryftlab.de` (`Z0847310CSDZ20OKMOUQ`, 12 record sets); the bucket
    `ryft-prod-euc1-vault-snapshots-173003892479` (it was empty); the budget `ryft-prod-euc1-monthly`; the aliases of
    the two Vault keys;
  - the KMS keys `2960364e…` (vault-unseal) and `90b5cce6…` (vault-snapshots): `NoDeletionOrDisable` removed from
    their policies, PendingDeletion until 2026-11-26; the EKS key `fb345db3…` was already PendingDeletion, until
    2026-12-14;
  - the user's re-check found no RYFT resource left: no EKS, VPC, NAT, EIP, instance, log group, IAM role or policy,
    OIDC provider, SES or ECR. The tag index still lists two pod-identity associations whose clusters don't exist;
  - the data key: its old-state copy went with the org. Secret Manager's pinned `data-key` version and the user's
    1Password item from step 0 remain; step 5 compared the two. Step 14.1's comparison with the state can't run
    any more, and nothing replaces it;
  - D2 (#223, `infra/aws: empty the root`) is obsolete and closed: there is no state left to empty.
  - OPERATIONS → GCP 12–14 is a done-note with these IDs. 14.3, the GitHub secrets and variables no workflow reads,
    is the one step left.
- **Decision (D3, this PR):** the code and manifests of the decisions D227 supersedes are deleted: `infra/aws` (with
  `dns`, `kubernetes`, the modules, the admission charts, `admission-e2e`, `argocd-luatest`), `infra/vault`,
  `deploy/k8s`, `deploy/argocd`, `deploy/rabbitmq`, `deploy/monitoring`, `deploy/grafana`, the kubectl, EKS, Vault,
  admission, chart, registry, promote and hold scripts with their tests (`trace.sh` too: it read only a cluster's pod
  logs; OPERATIONS has its `gcloud logging read` query), `recover_data_key.sh`, `dns_compare.sh`, CI's `monitoring`
  and `argocd-luatest` jobs and the four EKS-era terraform roots, `make k8s-*`, `tf-migrate-hcp`,
  `monitoring-apply`, `alerts-*`, `images`/`push` (GHCR), `web-image`/`web-serve`, the nginx web image (web D25),
  `docs/REBUILD.md`, the EKS-era runbooks and the EKS halves of ARCHITECTURE and OBSERVABILITY. Kept: `infra/gcp`,
  `deploy/run`, `deploy/pubsub-emulator`, `web/` (Firebase Hosting), `backend/`, compose (Postgres and the Pub/Sub
  emulator; it used no RabbitMQ since A1).
- **Kept on purpose, with their reason:**
  - `.gitleaks.toml`'s allowlists for removed paths: the `secrets` job scans the whole history (`--log-opts=HEAD`)
    with main's config, and old commits still hold those files;
  - generic `.gitignore` rules (`secrets.yaml`, `secrets/`, `terraform.tfvars`, `*.tfstate*`, `.terraform/`): a clone
    keeps its git-ignored files in the removed folders, and with their own `.gitignore` gone they must stay out of
    `git add -A`;
  - `scripts/rescore_all.sh` and `scripts/lib/kube.sh`: the compose mode (`RESCORE_LOCAL=1`) serves local work; its
    cluster mode has no cluster;
  - `infra/gcp/outputs.tf`'s `godaddy_instructions` still names `scripts/dns_compare.sh`: a new text shows as a change
    to outputs in the next plan, and the runbook's checks after each apply expect "No changes". It moves with the
    next `infra/gcp` change that is applied anyway;
  - the DECISIONS entries before this one and `docs/work/**`: history, as written.
- **Open (removed without a GCP successor):** a bulk rescore on Cloud Run (`scoring rescore`; until then
  `POST /v1/recompute` per account, OPERATIONS → Rescoring), `scoring learn-baselines` for everyone and
  `ingest fold-workouts` as Cloud Run jobs, an alert on a service that exits or panics, runbooks for Cloud Run scaling
  and for adding a service. `seed_demo.sh` takes the App Review account's password from `DEMO_PASSWORD` (it was in
  Vault).
- **Why:** nothing serves from the removed code, and no state is left that its Terraform could plan against. A
  repository that still holds the EKS rules invites an agent or a person to follow them.
- **Consequences:** D227's last step is the user's deletion of `tim-krebs-org`. `CLAUDE.md` loses the EKS, Argo CD
  and Vault rules. Nothing in AWS is RYFT's; the three keys finish their waiting periods by themselves.

## D247 — App Attest, the server: shipped in `off`, `501` until the ring is pinned, the challenges in Postgres, no new module (2026-11-20; amends D245; the user's AA1–AA4 decisions)

- **Context:** D245 is the contract; the head architect's brief of 2026-11-19 asked for the server in one PR, in mode
  `off`, with the app's four requests decided (AA1 yes, AA2 only if cheap, AA3 yes, AA4 yes). The app client is built
  (app repo `feat/app-attest`, ADR 0023): `clientDataHash` = SHA-256 of the challenge's UTF-8 bytes, production
  attestations in every build, one key per account and install, the token renewed with under `min(300, ttl_sec / 2)`
  s left, a probe at most every 6 h after a `404` without a code or a `501`. The plan, the interfaces and the reviews
  are in `docs/work/2026-11-20-app-attest-server/` (`plan.md`, `edge.md`).
- **Decision:**
  1. **`off`'s routes:** served when the token ring is configured, `501` without it. The `off` deploy pins no secret
     version, so production answers `501` and the app waits (D245's "nothing stored before the privacy text"). The
     `warn` PR pins the ring in api and ingest, so the routes start serving and tokens flow in the same deploy;
     `warn` refuses nothing, so the ramp costs nothing. An operator may pin the ring under `off` first (both
     services, one version) to see tokens before `warn`.
  2. **The ring** is the secret **`app-attest-token-key`** (the brief's name; D245 wrote `app-attest-token-keys`), read
     as `APP_ATTEST_TOKEN_KEYS`, D245's value format (1–3 lines `<kid>:<base64 of 32 bytes>`, the first signs, all
     verify) and rotation. Accessors api and ingest (`infra/gcp/secrets.tf`); no value in Terraform.
     `check_cloudrun.py` refuses `warn`/`on` without the pin in both, a pin in one service only, and two versions.
  3. **Production attestations only.** The AAGUID must be `appattest` + seven `0x00`; `APP_ATTEST_ENVIRONMENTS` is
     dropped from D245 (every build's entitlement is `production`). Development attestations are
     `422 app_attest_invalid`.
  4. **The App ID** is `APPLE_TEAM_ID` + `.` + `APPLE_CLIENT_ID`, the values Sign in with Apple already has on api
     (`M77SKHKG63.dev.timkrebs.ryft`); no `APP_ATTEST_APP_ID` key. api refuses to start with a ring and either empty.
  5. **The challenge store** is Postgres: `app_attest_challenges` keeps only SHA-256 of the challenge, single use by
     `DELETE … RETURNING`, 5 minutes, at most 5 open per account. Cloud Run runs up to three api instances, so memory
     was no option. A call gets past the `429` and the JSON decoding before it uses a challenge up (API.md says so).
  6. **The keys** are `app_attest_keys` (migration 0037; `ryft_api` only, RLS as 0033): key id, public key, counter,
     environment, attested and last used. The caps (5 challenges, 10 keys) hold across instances under an advisory
     lock (class 7419007); the counter is a compare-and-set; the api tick purges expired challenges and keys unused for
     90 days. **Deletion:** `DELETE /v1/me` deletes both tables in its transaction, and their foreign keys cascade from
     `users` as well; the merge deletes the merged account's rows (device credentials of the old account, not moved).
  7. **The token** is D245's `v1.<kid>.<body>.<mac>` HMAC-SHA256, checked in api and ingest without a database read.
     `Check` verifies the version, kid, MAC (`hmac.Equal`), expiry, issue time ≤ 60 s ahead and the account; the key
     hash in the body is carried, not checked (API.md says so). **`ttl_sec` 3600, pinned ≥ 600 by a test (AA3).**
  8. **Modes:** `APP_ATTEST=off|warn|on` and `APP_ATTEST_EXEMPT_USERS` are required keys of `deploy/run/config.env`
     (`off`, empty). api and ingest refuse to start in `warn`/`on` without a valid ring; `check_cloudrun.py` refuses
     `off` → `on` against the PR's base (CI passes the base's `config.env` on pull requests, `make cloudrun-check
     CLOUDRUN_BASE=…`), the rule D245's item 8 asked for. Order in the chain: bearer → per-user limit → user → App
     Attest → beta gate → routes.
  9. **The `warn` line** (under `warn` and `on`, one per request without a valid token): `message` `app attest`,
     `code`, `reason` (`missing|invalid|expired|other_account`), `mode`, `service`, `route_group` (7 fixed values),
     `method`, `app_build`, `app_device` (a bool) and `request_id`. It is written without the request's log fields, so
     it carries **no `user_id`**, email, address, key id, token or device id (a test). The `request_id` joins it to the
     request's access line, which carries `user_id` as every access line does, so C2 must not call these lines
     anonymous. Counter `ryft_app_attest_refusals_total{reason,service}` (the brief's labels; D245 had `mode` too).
  10. **AA1:** `X-RYFT-App-Build`, optional, `^[0-9]+(\.[0-9]+){0,3}$`, at most 24 characters, otherwise ignored, never
      refused; a field of the warn line, never a label. The stop rule's query is OPERATIONS → GCP → 17.4.
  11. **AA2:** `GET /v1/config` gains `app_attest` (`off|warn|on`). It is cheap: the handler has the mode. No new
      route. The mirror's field is optional, so a config from a server before D247 (a rollback) still decodes.
  12. **AA4:** every refusal of the three routes has a fixed `error` text; neither the answers nor the logs carry the
      challenge, key id, attestation, assertion or token (tests in `internal/appattest` and `cmd/api`).
  13. **No new Go module.** A strict minimal CBOR decoder (definite lengths, depth 8, 32 KiB, no tags or floats,
      preallocation capped at 16, fuzzed) is about 200 lines; `fxamacker/cbor` would be the obvious module and was not
      worth a dependency for two fixed shapes. X.509, ASN.1, ECDSA and HMAC are the stdlib's. `internal/config`
      imports `internal/appattest` (to parse the ring at start), an exception to its leaf rule in `arch_test.go`:
      `appattest` is pure and imports nothing of ours.
  14. **Apple's root** (`backend/internal/appattest/apple_root.pem`) was written from the lead's memory: WebFetch and
      `curl` were refused in the run. Its self-signature, subject and validity verify, so no byte is wrong, but a
      self-signed look-alike would pass the same test. The SHA-256 `1cb9823b…c932` is pinned; the user compares it
      with Apple's download before `warn` (OPERATIONS → GCP → 17.3, precondition 4).
- **The edge** (`edge.md`): of the six preview sets, `sqli` (942100, libinjection: base64's `+`, `/`, `=` and a
  base64url challenge's `--`) can match the two body routes, certainly for a challenge that starts with digits then
  `--`; `xss` (941100) plausibly, rarely; `lfi`, `rce`, `scannerdetection` and `protocolattack` no. Exclusion needed:
  yes, for `sqli` and `xss` on `^/v1/app-attest/(keys|token)$`, by scoping those rules' expressions, a prepared
  0/1/0 change applied before the `sqli` enforcement PR (OPERATIONS → GCP → 17.6). Not in this PR. The preview logs
  could not be read (the command needed approval). An attestation passes the API's 64 KiB and the edge (no body
  limit; Cloud Armor inspects the first 8 KB, *unverified*).
- **Rejected:** a challenge in memory (three instances); development attestations behind a key (no build needs them);
  a token from `POST /v1/app-attest/keys` (D245); `mode` as a metric label (the brief's bounded pair); a new route for
  AA2; a hex challenge to dodge libinjection (the exclusion covers the device's blobs too, which a server choice can't).
- **Unverified:** a real device's attestation and assertion (the tests use generated vectors from a test CA with real
  P-256 signatures); the root against Apple's file; Cloud Armor's behaviour on base64 JSON values.
- **Privacy (for C2):** per account and app install: the key id, the public key, a counter, the environment
  (`production`), when the key was attested and last used; open challenges as SHA-256 for 5 minutes; no receipt,
  device model, address or token. The purpose: refusing requests that don't come from the genuine app on a genuine
  Apple device. Apple's role: the device talks to Apple for the attestation under Apple's terms; RYFT's servers never
  call Apple, so no new processor on the server side. Deleted with the account (`DELETE /v1/me`), when the key is
  replaced or evicted (10 per account), or 90 days after its last use. The warn lines (above) are logs under the
  existing retention. The text and `PRIVACY_POLICY_VERSION` change together, before the ring's first version.
- **Consequences:** `internal/appattest` (+ `appattesttest`), migration 0037, `internal/store/appattest.go`,
  `internal/config/appattest.go`, `internal/gate/appattest.go`, `cmd/api/appattest.go`, the wiring in api and ingest,
  `deploy/run` (two keys, `off`), the scripts' rules, `infra/gcp/secrets.tf`, API.md (changelog 2026-11-20),
  OBSERVABILITY, OPERATIONS → GCP → 17, STATUS. The user's steps: 17.1 (apply 2/0/0) → C2 → 17.2 → 17.3 (`warn`) →
  17.4 (7 days) → 17.6 (the edge, before `sqli` is enforced) → 17.5 (`on`).

## D249 — Account deletion clears the flags' allow-lists; an unbound invite note expires 30 days after its code (2026-11-20; amends D69, D161)

_Numbered after D248, which PR #228 (C2, draft) holds._

- **Context:** the C2 draft (D248) found two gaps: `DeleteUser` left the account id in `feature_flags.allow_user_ids`,
  and the note of an invite code without an email was never deleted (D161 left it to the operator).
- **Decision 1, allow-lists:** `DeleteUser` removes the id from every flag's `allow_user_ids` in its transaction
  (`array_remove`, only on lists that hold it, so a repeated deletion updates nothing). Migration 0038 removes from
  every list the ids that have a tombstone in `deleted_users`, keeping the order of the rest; a second run updates no
  row. **Not** "every id without a `users` row": an operator may allow-list a tester's Supabase id before that user's
  first API call creates the row, and that entry must stay. An id that is in neither table is left alone.
- **Decision 2, invite notes:** the purge (`PurgeInviteEmails`, D161, a step of the api's tick) also sets `note = ''` on an unbound code
  that has not been redeemable for **30 days** (revoked, expired or used up that long ago; a used-up code whose
  accounts are gone uses its creation time): the same period and the same clock as a bound code. For an unbound code
  it leaves `revoked_at` alone, so an expired code still reads `expired`. An open unbound code keeps its note: the
  note lives as long as the code can be used, then 30 days. Same job, same metric
  (`ryft_invite_email_purge_total`), same alert.
- **Rejected:** a separate job or period for unbound notes (one rule is easier to state in the policy); a hard delete
  of dead codes (the admin list and the audit name them); removing ids without a `users` row (above).
- **Left open:** `PUT /v1/admin/flags/{key}` still accepts a deleted account's id (an operator pasting an old list
  puts it back, and a PUT racing a deletion can too), and nothing removes it again. An account merge moves beta
  access to the target but not the source's allow-list entries, so deleting the source drops them (as before this
  PR; the operator adds the target's id). Both small (testers, the operator's hand), not fixed here.
- **Order:** the allow-list update runs last but one in `DeleteUser` (before `DELETE FROM users`), so the shared flag
  rows are locked only briefly, not behind the large deletes, and 0038's 5 s `lock_timeout` doesn't meet a long
  deletion.
- **Privacy (for C2):** the two sentences the policy can say are in this PR's description; the C2 work folder is
  not touched here.
- **Consequences:** `internal/store/store.go` (`DeleteUser`), `internal/store/beta.go` (`PurgeInviteEmails`),
  migration 0038, tests in `internal/store` (`delete_test.go`, `invites_test.go`), `internal/db`
  (`migrate_flags_test.go`) and `cmd/api` (`invites_test.go`); API.md (changelog 2026-11-20, admin only),
  ARCHITECTURE, OBSERVABILITY, OPERATIONS → Beta, STATUS.

## D250 — The training import is on for everyone (**the user's decision**, 2026-11-22; ends the dark rollout of D133)

- **Context:** the flag `train.import` shipped dark with migration 0028 (D133: enabled, 0 %), testers by the
  allow-list, from 2026-10-26.
- **Decision:** every account can import: `{"enabled": true, "rollout_pct": 100}`, set by the user with
  `PUT /v1/admin/flags/train.import`. No code, migration or deploy; the flag stays as the kill switch
  (`{"enabled": false, "rollout_pct": 100}`), and the endpoint keeps checking it (D141).
- **What still applies to an ordinary account:** consent (`403 consent_required`, `warn` acts as `on`, D129/D141),
  the import's rate class and the per-account lease (D129, D132), the per-IP limit (D240), the invite gate and
  App Attest as on every route (both `off` today).
- **Consequences:** STATUS, API.md (the flag's paragraph), OPERATIONS (the import's table, `import_not_enabled`).

## D257 — Direct connections: OAuth in api, a push inbox in ingest, no token on the processing path, and a connection's data goes with it (2026-11-23; supersedes ARCHITECTURE "Why HealthKit and not the Garmin API" and "What to build next" item 6; amends D48, D69, D101, D218, D240, D242, D244)

- **Context:**
  - A Garmin reaches RYFT only through Apple Health, and that path carries no HRV, respiration or SpO₂. HRV is 45 %
    of Recovery.
  - Suunto writes no sleep, HRV or resting HR to Apple Health at all.
  - The head architect's brief of 2026-11-23 (the root plan §3–§4) asks for a provider-agnostic foundation, built and
    shipped dark against a fake provider, with Garmin's adapter in a phase 2 that waits for the programme's approval
    and documentation.
  - The starter tier is push only, has no history, and caps the users.
  - The design, the tables, the flows and the file split are `docs/work/2026-11-23-connections/design.md`.
- **Superseded:**
  - ARCHITECTURE's "Why HealthKit and not the Garmin API" said the programme was closed and suggested an unofficial
    Connect client for Garmin-only metrics. HealthKit stays the default source and the only one with history.
  - A direct connection through the official programme is now the way to a vendor's data that Apple Health does not
    carry.
  - **An unofficial client is ruled out:** it holds the user's password, breaks without notice, and no agreement
    covers it.
  - "What to build next" item 6 is removed.
- **Decision:**
  1. **Roles and the sealing invariant.**
     - api owns connections and tokens: start, callback, confirm, exchange, refresh, deregistration.
     - ingest owns the provider inbox and processes pushes.
     - scoring gets no new grant.
     - Tokens and PKCE verifiers are sealed with `internal/sealed`; only api holds `DATA_ENCRYPTION_KEY` (D48's
       invariant, unchanged).
     - **Processing a push needs no token.** A provider that ever needs one to deliver data (a pull callback) is a new
       architecture decision, not an adapter detail.
  2. **Tables** (migration 0042, additive):
     - `connections`: metadata. ingest may read the provider-user lookup and write only status and `last_data_at`.
     - `connection_tokens`, `connection_pending` (a grant waiting for its confirm, kept apart from the live
       connection), `connection_states` and `connection_deregistrations`: api only, like `apple_tokens` (D48).
     - One live link per provider account: the unique index on `connections` covers only `connected` and
       `needs_reauth`. A `revoked` row keeps no provider user id and no permissions.
     - `provider_inbox`: unique on (provider, kind, record id), ingest's.
     - On existing tables:
       - `origin` and `vendor` on `samples` and `sleep_segments`, plus `record_id`, and `record_state` on the sleep;
       - `origin` on `user_sources`;
       - `hrv_metric` on `daily_scores`, plus the data-quality design's never-built coverage columns (`coverage`,
         `score_status`, `final_at`, `first_recovery_at`), so PR 3 needs no migration.
     - Every new CHECK on an existing table is `NOT VALID`, and no index is built on `samples` or `sleep_segments`.
     - Direct rows use the reserved source `ryft-direct-<provider>`. The sync drops records with that prefix.
  3. **The contract:**
     - `GET /v1/me/connections`, with each connection's `provider_user_id` and `permissions` (Art. 15);
     - `POST /v1/me/connections/{provider}/start`: `409 connection_limit`, and `403 consent_required` with `warn`
       acting as `on` (D141);
     - `GET /v1/connect/{provider}/callback`: no bearer, no App Attest, no invite gate. It answers `302
       ryft://connect/{provider}?status=ok|denied|expired|error`;
     - `DELETE /v1/me/connections/{provider}`;
     - `connections_deregistered` on `DELETE /v1/me` and `deregistered` on a disconnect, over every grant ended,
       unconfirmed pending ones included: `null` none, `true` each deregistered or skipped as live elsewhere, `false`
       retried in the background;
     - `origin` on `User.sources[]` and `coverage.sleep_origin`;
     - `coverage.hrv_metric`;
     - the sample type `hrv_rmssd` (ms).
     - Statuses `connected`, `needs_reauth` and `revoked` are a closed set.
  4. **A confirm step (a deviation from the brief):**
     - `status=ok` carries a one-time `ticket`;
     - the link becomes `connected` only when the app sends `POST /v1/me/connections/{provider}/confirm` with the
       ticket and the bearer of the account that started it, within 10 minutes.
     - Without it, a bearer-less callback lets anyone send their own `authorize_url` to someone else and receive that
       person's health data.
     - The ticket is a binding nonce, worthless without that bearer. It is not a token and not health data, so "nothing
       sensitive in the URL" holds.
     - A repeat confirm with the same ticket answers 200 until 10 minutes after the callback.
     - **Until the confirm, the live connection and its tokens are untouched.** The swap happens in one transaction
       at confirm: the same provider account refreshes the live row; a different provider account disconnects the old
       connection first (its data deleted as on any disconnect); a provider account linked to another RYFT account
       moves (that link ends as `revoked` / `relinked`, its data deleted, its tokens deleted with no deregistration
       call, because that call would end the new grant too).
     - **No deregistration ever ends a live grant.** Before every call to the provider, for every reason (an expired
       or replaced pending grant, the cap, a missing permission, a disconnect, a deleted account), api checks whether
       any account holds that provider account as `connected` or `needs_reauth`. If one does, it deletes the tokens
       and calls nothing. Otherwise an attacker's own authorize URL, completed by a connected victim and left to
       expire, would end the victim's connection and, by the rule below, delete the victim's data. Pinned by
       `TestDeregisterSkipsALiveGrant`.
  5. **OAuth:**
     - state: 32 random bytes as hex. Only its SHA-256 is stored. Single use, 10 minutes, bound to the account, at
       most 3 open per account and provider;
     - PKCE S256;
     - redirect URI `CONNECT_CALLBACK_BASE` + `/v1/connect/<provider>/callback`;
     - no state, code or ticket in any log line (a test).
  6. **The user cap** is config, `CONNECT_<PROVIDER>_USER_CAP` (a required key of `config.env`, `0` for Garmin
     until phase 2).
     - It counts the provider's pending, connected and needs-reauth links plus the accounts holding an open state.
       Start reserves a slot.
     - It is checked at start, callback and confirm under the advisory lock 7419009 (7419008 is open PR
       `push/activity-scored`'s).
  7. **The hook:**
     - `POST /connect/{provider}/push` on ingest, outside `/v1/`;
     - order: served provider → per-IP class `hook` → the body bounded by the provider (at most 4 MiB) → the provider's
       own verification over the raw body → split into items → the users resolved through `connections`;
     - one transaction: an inbox row and its outbox event per new or changed item. The conflict update is guarded by
       `user_id`, so a record-id collision never writes one account's data into another's row (counted, logged as an
       error);
     - at most 500 `pending` inbox rows per account; items beyond are not stored;
     - flush, then 200;
     - nothing is processed inline. A repeat is the same row: an identical body is a no-op, a changed one (version not
       lower) goes back to pending with `revision`+1.
     - Items for nobody, for a pending link, or for an account without consent to the policy in force are not
       stored, and still answered 200.
  8. **The event** `inbox.received`:
     - topic `ryft-inbox-received`, unordered, published by ingest;
     - consumed by ingest at `/push/inbox.received`, a third consumer with `ryft-dlq-ingest` and `dlq-ingest-hold`
       (amends D218's two consumers);
     - its body holds ids only.
     - The consumer:
       - checks consent again, mode-independent, and checks the connection;
       - parses with the adapter's pure `ParseRecord`;
       - stores through `store.ApplyDirect`: the apply lock, the record's earlier rows replaced, the upserts, the
         queue, `ingested` (reason `connect`) and the inbox row marked processed, in one transaction.
     - Idempotent, with a test (D100).
  9. **The inbox's life:**
     - `pending` → `processed` or `dropped` (payload deleted at once, the row kept 30 days for dedupe);
     - or → `failed` (parse error, 20 attempts, or 7 days pending), kept 7 days, then deleted by ingest's tick
       (`ryft_connect_inbox_purged_total`).
     - Pending rows older than an hour are published again.
  10. **The hook's traffic class:**
      - in process (D240): class `hook`, 50/s, bursts of 200, apart from `ip`;
      - at the edge (D242): Cloud Armor rule 1900, a throttle on `ingest.` + `/connect/` at 6,000 per 60 s per
        address, ahead of the ban at 2000;
      - prepared in Terraform, applied by the user;
      - the six WAF rules are scoped away from the callback and `/connect/` in the same change, so no preview match
        logs the callback's URL with its `state` and `code`;
      - a third log exclusion, `connect-callback` (`logging.tf`), unconditional (not tied to the edge's phase),
        drops every load balancer and Cloud Run request-log entry whose URL contains `/v1/connect/`. The api's own access line, which has no query, still records it.
  11. **Flag** `connect.garmin`: shipped dark (`enabled`, 0 %, allow-list), listed in `GET /v1/config`. It gates
      `available`, start and confirm: a confirm while it is off answers 404, and the pending grant is handled like an
      expired one (deregistered unless a live connection elsewhere holds it). It never gates a disconnect, which also
      skips App Attest and the invite gate.
      - The cap never counts the caller's own connection, open states or pending grant.
      - `CONNECT_PROVIDERS` (in git, api and ingest equal) is the per-deploy switch, empty in phase 1.
      - The fake provider exists only under `CONNECT_FAKE=on`, which config refuses on Cloud Run.
  12. **Alerts within the cap of six (D244):**
      - inbox age and poison rows come from policies 5 and 4 through the topology;
      - the hook's outcomes are the log-based counter `ryft_connect_pushes`;
      - `ryft_notices` gains `connect capacity` (80 % of the cap), `connect no data` (a connected account without data
        for 36 h), `connect inbox failed` and `connect deregistration stuck`, each written at most once a day;
      - policy 6 is renamed "Notices: feedback, admin access, connections".
      - No seventh policy.
  13. **Scoring (PR 3):**
      - two HRV metrics, never mixed: a baseline per metric, the night's metric for Recovery, a device change
        calibrating as `short_history` from the new metric's own nights;
      - one night one record: a direct source beats Apple Health for the same vendor, and today's most-asleep rule
        decides across vendors;
      - the provider's "light" is `core`;
      - a re-sent night replaces the earlier one, and `night_complete` follows its validation state;
      - the model version moves once, together with the coverage work PR 3 builds.
- **What happens to stored data when a connection ends:** **RYFT deletes everything it received through that
  connection, and rescores the days it touched.**
  - **When:** the user disconnects in RYFT, the user removes RYFT at the provider, the provider account is relinked
    to another RYFT account, or the RYFT account is deleted.
  - **Deleted:**
    - the direct sleep and HRV rows;
    - the inbox rows;
    - the direct `user_sources` entry;
    - the tokens, which go to a deregistration queue first, retried by api's tick for 30 days, then given up and
      counted.
  - **Rescored:** the touched days, so no stored score or baseline keeps a value from that source.
  - **Kept:**
    - the same vendor's data that arrived through Apple Health (HealthKit data, under the Health permission and the
      account, not this connection);
    - every other source's data;
    - a `revoked` row (provider, status and times; no token, provider user id or permissions) for 90 days, so the
      app can say "removed at the provider";
    - Supabase's backups until they expire. The restore runbook deletes direct rows without a live connection.
  - **Why:**
    - the root plan's data card promises "disconnect deletes";
    - users read "disconnect" as stop and remove;
    - minimisation leaves no purpose for an ended source's raw records;
    - keeping the scores would keep the provider's HRV in `hrv_ms`.
  - **To be confirmed in phase 2 against Garmin's API agreement:** whether deletion on deregistration is required and
    within what time, and whether derived scores count as Garmin data. A lesser requirement leaves this rule as it
    is; a stricter one extends it.
  - `needs_reauth` and a withdrawn consent end nothing. Pushes are dropped while consent is not to the policy in
    force, and stored data stays readable as today (SCORING → Consent).
- **Rejected:**
  - **Hook and processing in api.** api would become a raw-data writer, and the tokens would sit on the data path.
  - **Processing in ingest's tick.** Up to 15 minutes late, a home-made retry, no backlog signal.
  - **One table for connections and tokens with column grants.** D48 chose an own table for the same reason.
  - **An index on `record_id`.** It is a scan of `samples` under the migration's locks; the existing indexes with
    time bounds suffice.
  - **An ordered topic.** A poison row would stall a user's later records; the apply lock already serialises.
  - **Keeping data on disconnect, or asking the user.** Covered above.
  - **A new `calibrating_reason` for a metric change.** A closed enum; `short_history` is true.
  - **A seventh alert policy.**
  - **The unofficial Connect client.** Covered above.
- **Phase 2 precondition:** a cryptographic signature over the push body, or an equivalent the programme documents.
  Without one, a forged deregistration notice would delete an account's data. If the programme offers only an
  unsigned push, a secret in the URL or an address list, that is a new decision before Garmin ships.
- **Unverified / phase 2, from the programme docs:**
  - Garmin's endpoints, scopes and token lifetimes;
  - its push verification, and whether ingest then needs a secret;
  - its record ids, versions and validation states;
  - its deregistration and permission notices, and its retry policy for a push answered 5xx;
  - whether it publishes source ranges;
  - its brand rules;
  - the cap's number;
  - the bundle id `com.garmin.connect.mobile` → `garmin` mapping (from our own API.md example; check it against
    `user_sources`).
- **Privacy (for C2, before the first real connection):**
  - **The new flow:** RYFT receives the user's sleep and HRV from the provider at the user's request (the role of
    Garmin to be confirmed in phase 2).
  - **Kept:**
    - the provider's user id, the granted permissions, the connection times;
    - the tokens, encrypted, api only;
    - each record in the inbox until processed (a failed one 7 days), its digest 30 days;
    - the records as health data.
  - **Deleted:** as above. The deregistration retry runs 30 days. No address is stored.
  - The text and `PRIVACY_POLICY_VERSION` change together.
- **Consequences:**
  - migration 0042;
  - `internal/connect` (+ `fake`);
  - `internal/store/{connections,inbox,direct}.go`;
  - api's routes and four tick steps, ingest's hook, consumer and tick step;
  - the topology (+ outputs);
  - `internal/app`'s `hook` class;
  - `internal/config/connect.go`;
  - `deploy/run` (`CONNECT_PROVIDERS`, `CONNECT_GARMIN_USER_CAP`, `CONNECT_CALLBACK_BASE`) and `check_cloudrun.py`;
  - compose's fake;
  - `infra/gcp` (`edge.tf` rule 1900 and the WAF scope exclusion, `logging.tf` `connect-callback`, `secrets.tf`
    `connect-garmin-client-secret`, monitoring, policy 6);
  - the smoke;
  - API.md (changelog 2026-11-23), ARCHITECTURE, SUPABASE, OBSERVABILITY, OPERATIONS, SCORING (PR 3), STATUS.
  - **The user's steps:** the Terraform apply (rule 1900, the WAF scope, the log exclusion, the secret container,
    monitoring); in phase 2 the secret
    value, the cap, the privacy text, then the allow-list.

## D258 — Direct connections get their own dashboard, and the hook's counter is by provider, kind and outcome (2026-11-23; amends D236, D244, D257)

- **Context:** D257 §12 keeps the connections inside the cap of six policies. Its tiles on the overview would move
  the overview's layout, which the edge tests pin, and the feature ships dark. The brief asks for pushes counted by
  provider, kind and outcome; the design's `connect push` line had no kind.
- **Decision:**
  - A dashboard of its own, "RYFT · Connections" (`infra/gcp/monitoring.tf`): hook requests by provider and
    outcome, the inbox's oldest unacked event, the dead letters held, and the hook's log lines. No new policy.
  - The `connect push` line carries `kind`: the push's record kind when its items share one, `mixed` when they
    differ, `none` when the request is refused before parsing. `ryft_connect_pushes` extracts it as a label. The
    kinds are the adapter's closed set, so the label's cardinality stays small.
- **Rejected:** tiles on the overview (the edge tests and the overview's purpose: the live system, not a dark
  feature); a label per item outcome (the per-item counts stay on the line and in the local Prometheus counter).

## D259 — Direct connections: pushes follow a consent cutoff, a revocation at the provider deletes, the notices stay in policy 6 (**the user's decisions**, 2026-11-23; answers D257 design §17 Q1–Q3; amends D78 for the hook only)

- **Context:** a provider keeps no history, so a push dropped is lost for good. D257 dropped every push of an
  account whose consent is to an older policy version than the one in force, which would cost every such account
  its nights after each policy change until the user opens the app again (the C2 text is the next one). The
  security review reads health-data consent as bound to its purpose, not to the text's version.
- **Decision:**
  1. **The consent cutoff.** `CONNECT_MIN_POLICY_VERSION` (ingest, `deploy/run/config.env`, a `YYYY-MM-DD` date not
     after `PRIVACY_POLICY_VERSION`; empty would mean the policy in force, the D257 behaviour). It ships pinned to
     `2026-10-26`, the version in force, so a policy PR that forgets the line keeps older consents instead of
     dropping them. The hook and the
     consumer accept an account's push while its health-processing consent is not withdrawn and is to that version
     or a later one. The operator moves the cutoff only when a policy change touches how health data is processed;
     a wording change leaves it. A withdrawn consent always drops. `start` and `confirm` keep requiring consent to
     the policy in force (the app asks for it there). The lawyer reviewing C2 (#228) confirms the reading before
     phase 2's first real connection.
  2. **A revocation at the provider is a disconnect** (D257 §8): the connection's data is deleted and the touched
     days rescored; the same vendor's data from Apple Health stays. The app shows the connection as ended with its
     data removed and offers to reconnect. Revisited against Garmin's agreement in phase 2; keeping the data until
     a RYFT disconnect would be a one-line change if the agreement allows it.
  3. **The connection notices stay in policy 6** (capacity, no data for 36 h, failed inbox rows, stuck
     deregistrations), each at most once a day, to the email and app channels. No seventh policy.
- **Rejected:** dropping on any version gap (permanent loss on every text change); holding pushes in the inbox
  until re-consent (stores data under a consent the design treated as lapsed, and adds a state); keeping data after
  a provider-side revocation (likely against the provider's terms; decided in phase 2); a seventh policy (one more
  channel set to keep, no gain at beta scale).

## D260 — Direct connections: the hook keeps room for sync, the consumer batches an account's pending rows, the api tick budgets each step (2026-11-23; amends D257 §7.2, §7.3, §6.3; review P2-2, P2-3, P2-4)

- **Context:** ingest serves `/v1/sync` and the hook on 4 slots an instance, 2 instances at most, and the hook reads
  a body before it can verify it. Every inbox row emitted its own `ingested` event on the per-user ordered topic
  that sync shares, so a push of N records put N rescore walks ahead of the user's next sync (up to ~15 min). The
  api's connection tick ran its steps under one shared deadline, so a slow provider starved the daily notices and
  could drop a grant the provider had already rotated.
- **Decision:**
  - The hook takes at most 2 requests in flight per instance (`maxHookInflight`); the excess answers `503` with
    `Retry-After` before reading (outcome `busy`). A body has 10 s to arrive (`hookReadTimeout`; `408`, outcome
    `timeout`, nothing stored). The 2 other slots stay for sync.
  - The consumer stores the account's other pending sleep and HRV rows in the same transaction (at most 25
    records, 1500 rows) and emits one `ingested` event for the union of their days; a failing batch falls back to
    the row alone. Idempotency (D100) and the outbox (D101) hold per batch.
  - The api tick runs purge → gauges (the daily notices) → refresh → deregister, each under its own deadline
    (8 s for a database step, 25 s for a provider step, within the tick's 45 s). A refreshed grant is saved on a context detached from the
    step's deadline (5 s of its own).
  - A provider call (refresh, deregister) runs on a context detached from its step, bounded by its own timeout, and
    the step starts no refresh it has no room to finish; a rotated grant is never cut off mid-call. The one
    exception is `DELETE`'s inline attempt, which keeps API.md's 5 s: a cut there leaves its queue row for the tick.
  - `SaveRefresh` is a compare-and-swap on the claimed token, so a relink or replace meanwhile is never
    overwritten; 0 rows queues the rotated tokens for deregistration. A relinking confirm takes both accounts'
    apply locks, then both rows, in user-id order (the order `StoreInbox` locks in).
- **Rejected:** raising ingest's concurrency for the hook (makes the slow-body case cheaper to mount); a separate
  topic for direct data (one more subscription and ordering scope for a dark feature; batching removes the
  backlog); one longer tick deadline (still lets a provider starve the notices).
- **Follow-up, not here:** notify's silent-refresh throttle is per user, so a connect refresh can swallow a sync's
  that follows within 5 min (review P2-23).

## D265 — Nutrition uses the API, PostgreSQL and an isolated catalog importer (2026-11-24)

- **Storage.** Structured food and meal data belongs in a dedicated schema in the current Supabase PostgreSQL database. Joins, constraints, transactions and indexes support its access patterns; reserve private Supabase Storage for binary assets when their workflow ships.
- **Nutrients.** Registry codes fix canonical units and distinguish materially different measurements. Sparse rows retain source and basis. Missing measurements remain unknown.
- **Meal snapshots.** Calculation versions and selected source evidence travel with each item. PostgreSQL commits each meal and its items atomically. Date-scoped aggregates read snapshots on demand. Edits preserve explicitly retained items.
- **Provenance.** Provider and external ID identify a source. Multiple sources and conflicting barcodes remain distinguishable. Promotion of future moderated submissions requires an explicit operator process with its own lineage.
- **Search.** Use PostgreSQL prefix, full-text and pg_trgm indexes, with Unicode-normalized names. Order exact matches before fuzzy candidates. Keep queries and barcode text in request bodies.
- **Authorization.** Reuse the App’s existing identity, admission and consent gates. Bind the verified account to the transaction, enforce repository predicates and RLS, and leave Food Data API roles without grants. Gate release with `nutrition.enabled`.
- **Imports.** Use a one-shot Go command with finite, checksum-pinned staging, set-based child writes and transactional checkpoints. Require explicit writes. Plan a dedicated writer role and production import schedule only after licensing and throughput are validated.
- **Recognition.** Keep provider extraction, candidate matching, review and deterministic nutrient computation as separate responsibilities. A provider cannot write a meal or supply its authoritative nutrients.

## D266 — Nutrition Score uses meal snapshots, current calorie goals and account-scoped CGM (2026-11-24)

- Compute on demand beside Nutrition totals. Save food classification with each new meal snapshot. Keep unknown contributions and measurements nullable. Use the current calorie goal and publish the calculation version.
- Apply the explicit positive formula at all unsaturated fat percentages. Use 9 kcal/g for both fat indicators and one combined class for red or processed meat.
- Store CGM in Nutrition because the existing ingestion contract has no glucose sample type. Use authenticated, consent-gated batch upserts and the Nutrition account isolation controls. Use inclusive measurement windows and the last available in-window measurement for recovery.
- Make the timing penalty an opt-in query option. Use the following wake day's known sleep onset when available and the saved meal timezone for the 21:00 fallback.

## D267 — Scheduled catalog imports use a standalone Python ETL (2026-11-24)

- Stream the official OFF Parquet release with DuckDB HTTP ranges and stage only the selected USDA tables under `/tmp`. Apply per-100-g validation before writes and report source-specific rejects.
- Keep the Go catalog schema, stable source identities and transaction lock. Use PostgreSQL COPY and merge source records in bounded transactions. Preserve manual classifications and snapshots. Grant the scheduled importer a catalog-only role.
- Deploy the import as an opt-in Cloud Run Job with separate scheduler identity. Install pgvector and run the public-catalog embedding worker separately. See the [ETL runbook](../scripts/etl/README.md).

## D268 — Nutrition-to-Recovery is an opt-in, versioned model (2026-11-24)

- Add N2R as a versioned object after account setup and current consent. The existing `nutrition.enabled` rollout covers N2R. Treat the entered calorie goal as a base energy target and add reported workout energy once. Keep unknown AHEI measurements nullable and expose the inputs used by each adjustment.
- Attribute meal intake to its captured day, workouts to the account timezone and next-morning outcomes to `wake_date`. Classify duration with the 30/75/150 minute boundaries. Use 12 on either recorded workout strain or muscular load as the high-intensity proxy. A recorded next session or explicit planned start can extend the window. Use expected bedtime for live advice and measured sleep onset when available for completed days.
- Preserve source units and nullable evidence. Store labeled OFF beverages per 100 mL when volume is supported. Derive ethanol grams from documented ABV on that basis. Keep GI dimensionless across portion sizes and outside summed daily totals. Reserve known glucose-context exceptions for recorded qualifying sessions.
- Use seven preceding calendar days of RMSSD for the sample SD, CV and SWC. The bounded morning step runs on the existing API tick and pairs the previous intake day with the wake day. Step the model's caffeine decay parameter only with repeated, newly observed associations. No separate Cloud Run service or per-request embedding calls are required.
- Retrieve suggestions from a bounded public catalog shortlist with nutrient constraints. Rank eligible foods by cosine similarity to an eligible catalog anchor when its current embedding exists. The catalog filters also support installations without pgvector. See the [N2R model and use cases](architecture/nutrition/10-nutrition-to-recovery.md).

## D269 — Update the notices metric in place (2026-11-24)

The failed production apply requested deletion of `ryft_notices` because Google provider 8.11 marked the `metric_descriptor[0].labels` change for replacement. The dependent alert policy kept the metric in use. Keep the existing label descriptor and add deletion protection so filter expansions can update the metric in place. Resume from a fresh plan after partial applies. [Recovery runbook](../infra/gcp/README.md#recover-the-ryft_notices-plan)

## D270 — Deploy Nutrition through the existing API and a Terraform-owned monthly job (2026-11-24)

- The API and tick serve N2R. Reuse their existing GitHub deploy workflow and database migration job. Publish the catalog image on explicit dispatch through the existing Artifact Registry writer, with a scan before sign-in. Terraform owns and pins the ETL job image separately.
- Run one Cloud Run task with bounded retries, temporary staging and project-local Scheduler OAuth. Start paused with a bounded import. The existing stop line pauses the schedule. Separate the invoker from the runtime and pin the catalog login's Secret Manager version.
- Use the same Postgres as the API. An existing Cloud SQL instance can supply a native socket. Embeddings are an optional offline step. [Nutrition rollout](../infra/gcp/README.md#nutrition-on-cloud-run)

## D271 — Training-to-Nutrition plans feed opt-in N2R (2026-11-24)

The platform derives an adult coaching plan from account-scoped meals, body mass and reconciled
workouts. Existing N2R accounts keep their current target path until they set a goal profile. HealthKit
uses the existing anchored ingest route and keeps SDNN distinct from genuine RMSSD. New native partner
records are normalized beside existing HealthKit workouts. The API verifies signatures and enqueues
work on PostgreSQL. The existing Cloud Run tick fetches FIT using the configured partner bridge and
applies records idempotently. No additional always-on worker is needed.

Calorie reductions require sufficient availability evidence. Estimates use net modelled expenditure,
with cycling mechanical work preferred where present. The guardrail thresholds are app decision rules.
The IOC REDs CAT2 clinical assessment combines more evidence. The evaluation uses persisted forecasts,
dated feedback and outcomes and bounds parameter changes. Athlete metrics stay account-scoped and
operator cohorts suppress small groups. [IOC REDs consensus](https://bjsm.bmj.com/content/57/17/1073)

## D272 — Coaching queue claims follow the API revision's configured providers (2026-11-25)

The tick passes the providers with configured webhook keys to a scoped form of
`nutrition.claim_wearable`. The query filters providers before it locks a delivery or advances a
retry attempt. Keep the prior signature for compatible rollbacks while both connectors are dark.
Before removing a key on a future enabled rollout, stop routing requests and ticks to every
revision that still has that key. An already running revision keeps its own configuration.

## D273 — Bind Apple revocation credentials to linked identities (2026-11-25)

The authorization-code exchange requires an Apple linked identity supplied by
Supabase Auth Admin. Compare its provider identifier or provider-owned identity
subject to the non-empty subject returned by Apple's exchange for this client.
A generic identity record ID
does not prove the provider subject. User-editable access-token metadata carries
display information only. If linked identity verification is unavailable, decline
to save the refresh token and let the app retry within the authorization-code
lifetime. [Supabase Auth identities](https://supabase.com/docs/guides/auth/identities)

## D274 — Keep coaching inbox retention running with callbacks disabled (2026-11-25)

The bounded inbox purge runs on the existing API tick before the provider eligibility
check. Disabling providers still stops new deliveries and leases, and the retention
period continues to apply to existing health payloads and replay records.

## D275 — Constrain coaching relays to local fixtures and complete active leases atomically (2026-11-25)

Cloud Run fails configuration if any existing coaching relay signing key or fetch
setting is present. RYFT-02/03 define the provider approval and adapter work before
any hosted activation. Local fixtures retain the signed relay envelope with explicit
media types and a separate key per provider.

Migration 0049 checks the current link and consent at enqueue. The worker rechecks
ownership and consent and finishes the exact lease with the account-scoped domain
writes in one transaction. Domain write failure rolls back completion. Coaching
opt-out serializes with application on the goal row and deletes the link and inbox
through the existing foreign keys. Consent withdrawal scrubs pending payloads
while retaining replay markers, so regrant cannot reopen an earlier delivery.
Keep the older queue functions for dark rollback.

## D277 — Check consent inside the Nutrition scoring transaction (2026-11-25)

Daily and range summaries return the owner's meal counts and nutrient totals.
Fresh scores require current health-processing consent. A shared lock on the
grant orders scoring and the nightly N2R refresh against withdrawal. The summary
keeps its consistent read snapshot and retries serialization conflicts. Without
current consent, it returns the existing empty score shape and skips CGM, sleep
and contextual score inputs. The caller can still erase records.

## D278 — Use source identifiers and measured household servings (2026-11-25)

Common German and English generic names add one alternate search term while keeping the food ID and catalog label. Exact names rank ahead of translated matches. When match quality ties, the requested market and language determine preference. The Python importer accepts measured USDA gram weights and Open Food Facts servings with a known mass or volume on the same reference basis. Updates keep existing equivalent portion IDs and change only Python-owned portion rows. Meal snapshots keep the quantities and nutrients in use when the meal was saved.

## D279 — Serialize HealthKit write phases with consent changes (2026-11-25)

Keep the cheap admission check before reading the request body. Before either write phase, acquire the existing account sync lock, then check that the grant that admitted the request is still active. Grant and withdrawal use the same lock. Store the optional cursor in the apply transaction with the queue and deletions. Replaying an old request across a same-policy regrant fails. Records already committed with valid consent retain the documented retry behavior when apply fails.

## D280 — Separate the beta diary from the protected Nutrition pilot (2026-11-25)

`nutrition.diary_enabled` defaults off and permits meals, catalog reads, private foods and diary totals independently of `nutrition.enabled`. The existing protected pilot flag still governs scoring, CGM writes, coaching, submissions and optional HealthKit weight collection. Enable the diary flag only for the approved pilot cohort. Keep authenticated owner erasure routes available independently of those flags. The existing beta gate governs API admission.

## D281 — Deliver notices by email (2026-11-25)

Use the existing `ryft-timkrebs` email channel alone for the `ryft_notices` policy. Uptime and the four PromQL policies use the email and the Google Cloud app channel. Keep the same alerting gate, policy identity and incident settings. Review the existing policy's in-place channel update in the saved Terraform plan, then confirm delivery with a synthetic notice.

## D282 — Verify production smoke authentication before rollout (2026-11-25)

When smoke is enabled, check the dedicated session after the tip check and before replacing or running the migrate job. Accept the existing password login where available or a short-lived Supabase access token for the configured dedicated fixture UUID. Keep the verified session in a private runner file and validate it against the rolled API before sync and consent writes. Report the HTTP status and allowlisted Auth error code on sign-in failure.

## D285 — Release Nutrition selection in two steps; a meal PATCH copies retained item rows (2026-11-26)

The API before D285 deletes and reinserts every meal item on PATCH from its typed snapshot, so a rollback or a mixed-version rollout across a release that adds item columns or source fields erases them. This step copies each retained item row as stored (`to_jsonb` and `jsonb_populate_record`: every column, the sources JSON unchanged), only moves its position, and writes no new snapshot fields. It adds the two portion label columns empty (0053, constraints `NOT VALID`, validated by 0054). The catalog selection release (D284) writes the labels and ships only after this build is live; its rollback target is this build, never an earlier one.

## D290 — Privacy policy 2026-11-26 and terms 2026-11-26: today's RYFT, the planned connections, no personal details (2026-11-26; supersedes the C2 draft of #228; published now under the user's rule of 2026-11-27: open points stay `TODO(user)`, the legal review comes later, RFT-155)

**Not legal advice:** the texts state facts with their sources (`docs/work/2026-11-26-legal-partners/facts.md`);
the bases they propose and every legal reading stay with the user and their reviewer.

- **Context:** the policy in force, 2026-10-26, describes the AWS era (AWS, Grafana Cloud, Google Workspace mail,
  Vault, the site's own access log). C2 (#228, `docs/work/2026-11-20-privacy-c2/`) rewrote it for GCP but was never
  published and still describes email and password sign-in. Since then: the user turned email sign-in off
  (2026-11-26; sign-in is Apple or Google), nutrition landed dark (D265-D285), the Garmin and Suunto connections'
  foundation landed dark (D257-D260), and the Garmin and Suunto partner programs need a public policy, terms and
  contact (`…/partners.md`).
- **Decision:**
  1. **A new version, `2026-11-26`**, in one PR: `web/content/legal/en/privacy/2026-11-26.mdx`, appended to
     `privacy-versions.json`, `PRIVACY_POLICY_VERSION=2026-11-26` in `deploy/run/config.env` and
     `docker-compose.yml`. It is `status: published`, `effective: '2026-11-26'`: `scripts/check_policy_in_force.sh`
     refuses a pull request that moves the version to a page that is not exactly published, `check-policy-version.mjs
     --strict` needs the newest page to equal the version and the list's last entry, and `web/tests/legal.test.ts`
     needs a date in `effective` for a published page. A draft would turn CI red; a published page with
     `TODO(user)` passes every guard (no guard reads markers in published pages; LEGAL_CHECKLIST P7). Since
     2026-11-27 the PR is not held back for the markers (see "Amended 2026-11-27"), and the user sets `effective`
     to the merge date if it is later.
  2. **Consequence: the merge puts it into force.** `deploy.yml` rolls api and ingest (the two services that read
     `PRIVACY_POLICY_VERSION`, `deploy/run/api.yaml:55`, `ingest.yaml:45`), the `site` job deploys the page and its
     live check expects `/privacy` to show 2026-11-26, and **every user is asked to consent again** before the next
     upload (D78).
  3. **The old versions stay as published:** 2026-10-20 and 2026-10-26 keep their pages and words. **Amended
     2026-11-27 (the user's rule: no personal details online):** privacy 2026-10-20 §1, privacy 2026-10-26 §1 and
     terms 2026-10-20 §1 lose the operator's name and home address, replaced by "[Provider details removed on
     2026-11-27; the provider is named in the current legal notice.]"; every other word stays. The original text
     stays in git history (the files at `origin/main` before this PR) as the record of what users consented to; no
     guard pins the archived pages' content, so none changes.
  4. **Against 2026-10-26** (and as C2 had it): Google Cloud in Belgium, Firebase Hosting, Microsoft 365 through
     GoDaddy instead of AWS, Grafana Cloud and Google Workspace; App Attest, PostHog, the full feedback
     diagnostics, Google's firewall logs; no site access log and no browser storage; the controller, the
     authority and the transfer bases as `TODO(user)`.
  5. **Against C2:** sign-in is Apple, with Google as a `TODO(user)` until the app offers it (the user turned
     email sign-in off on 2026-11-26; C2's sign-up and password-reset emails and `no-reply@` are gone; older email
     accounts keep their address and password hash at Supabase until deleted, with a marker for what happens to
     them; the Google details carry markers in privacy, terms and support, because neither the app at ea52758 nor
     the platform has a Google sign-in yet, and the unversioned support page must not state it as fact); the analytics events, identifier and menu path
     come from the app (two C2 markers resolved); the food diary (stored data, not live, catalog sources; CGM, N2R
     and coaching deferred to a later version); a new §4 on the Garmin and Suunto connections; the export's real
     contents; feedback's path; C2's "flag allow-lists keep the id" removed (D249); a sentence that unavailable
     parts process nothing until they arrive. Sections renumbered (§4 connections; §5-§12 follow).
  6. **The connection commitments** (privacy §4, §7, terms §7, support): only at the user's request, after the
     app shows what is read; sleep and overnight HRV only; tokens encrypted for the api; deliveries emptied once
     processed, failed ones ≤ 14 days (7 pending, then purged 7 days after failing, `store/inbox.go:355,366`),
     the arrival record (kind, record id, checksum, times) 30 days; disconnecting in the app, a revocation at the
     provider and deleting the account each delete what the connection delivered and end RYFT's access at the
     provider, retried up to 30 days keeping the sealed tokens, the account id and the provider's user id; an
     in-app disconnect deletes the row at once, a provider-side end keeps it listed 90 days; further data from a
     provider needs a new version first. **Gap, open for the backend before phase 2:** the consent gate sets a
     provider's deregistration notice aside like any other item while consent is withdrawn
     (`store/inbox.go:139-140`, `cmd/ingest/connect.go:305-308`), so the revocation sentence carries a
     `TODO(user)` until `KindDeregistration` is exempt. Everything is described as not yet available (no real adapter on main,
     `CONNECT_PROVIDERS=` empty).
  7. **No personal details:** the Impressum (edited in place, a single page), terms 2026-11-26 §1 and privacy
     2026-11-26 §1 carry `TODO(user)` markers that name what § 5 DDG, § 18(2) MStV, Art. 13(1)(a) GDPR and the
     partner applications ask for, and why it is open (LEGAL_CHECKLIST section 0). Contact everywhere:
     `support@ryftlab.de`.
  8. **Terms 2026-11-26** (new file; `/terms/2026-10-20` stays): the provider as `TODO(user)`, minimum age 16
     (the platform's `MinAge`, not 13), Apple or Google sign-in, the food diary and its catalog attribution, the
     planned connections and other services (new §7, §8); §9-§12 unchanged in words.
  9. **`CONNECT_MIN_POLICY_VERSION` moves to `2026-11-26`** (decided 2026-11-27 under the user's rule, LEGAL_CHECKLIST
     U14). The runbook moves it when a policy change touches health-data processing, which this one does (D259); no
     connection is live, so it changes nothing today. It equals `PRIVACY_POLICY_VERSION`, which `check_cloudrun.py`
     allows (not after it).
- **Rejected:** a draft page (CI refuses it, and the version would sit in force with a draft); editing 2026-10-26
  in place (it is the record of consents given); describing CGM, N2R and coaching now (not in the app, and their
  data model may still move); naming the BLS (excluded on main pending review,
  `docs/architecture/nutrition/06-data-source-licensing.md:10`; superseded by the import of D296, see the
  amendment); a trademark notice for Garmin (none mandated was
  found; the site names Garmin only as a source). Amended by the lead: BLS 4.0 is named as "once German staples
  are added", with draft #248's attribution (`scripts/etl/transform.py:95` there, CC BY 4.0), because the brief
  asks for it and #248 carries it.
- **Open:** the 24 `TODO(user)` in the four files (LEGAL_CHECKLIST U1-U15; 28 before the amendment below), Google
  sign-in in the app, the legal review (RFT-155), the partner requirements still missing (partners.md).

**Amended 2026-11-27** (the user's rule of that day: legal questions don't block work; the most conservative sound
text is published now, open points stay `TODO(user)` or a PR note, a lawyer reviews it later, Linear RFT-155). The
PR is no longer a draft held for the answers. The version stays `2026-11-26`: it was never in force anywhere, so the
id is re-used, and `PRIVACY_POLICY_VERSION`, `privacy-versions.json`, `config.env` and `docker-compose.yml` do not
change. Merging still bumps `PRIVACY_POLICY_VERSION` from 2026-10-26, so every user is asked to consent again. The
privacy text now matches today's RYFT (sources: `docs/work/2026-11-26-legal-partners/revision-2026-11-27.md`):
- **App Attest goes to `warn` (#263, after this merge).** §2 no longer lists the device check as not yet available.
  §3 says RYFT is introducing it: at first a request without a valid check is only recorded and still answered,
  later it is refused (`on`). That holds under `off` (until #263), `warn` and `on`; §10 says the check "helps
  protect" the servers "as it is introduced". The recorded line names the reason, the app's build number, the
  device header's presence, the method and route group, and the request id that links it to the request's log
  entry (`backend/internal/gate/appattest.go`; no account, key or token). The `TODO(user)` for the § 25 TDDDG basis
  stays in §5.
- **The no-watch morning check-in** (code live, flag off; D291-D295, D299): the text of
  `docs/work/2026-11-26-no-watch-recovery/implementation.md` → Privacy, verbatim, in §2, §3 (a new bullet, and what
  RYFT computes), §5 (the health-data basis names the check-in, Art. 9(2)(a)), §9 (morning check-ins by request)
  and §12 (an estimate is not a measurement); §3 also names the stored components, the drop flag and reason, the
  evening's journal confounders and the model version (migration 0064). Not in it: what waits for the version that
  describes N2R and coaching, so until then no tester on `recovery.checkin` who is in the N2R or coaching pilot.
- **BLS 4.0 (CC BY 4.0, imported, D296).** §6 names the Bundeslebensmittelschlüssel (BLS) 4.0 of the Max
  Rubner-Institut as an imported source (no more "once German staples are added", also in terms and support). The
  licences page has a new "Data sources" group with the citation, the licence link and the DOI link.
- **Usage analytics (PostHog), a section of its own.** New §7 "Usage analytics (only if you say yes)" after §6,
  from `app/docs/ANALYTICS.md`, with the sync event covering the Home screen's sync button too. Sections 7-12
  became 8-13 and every "Section N" reference was updated. The transfer basis is the EU Standard Contractual
  Clauses (Art. 46(2)(c) GDPR, module 2 of the 2021/914 clauses) in PostHog's DPA; PostHog Inc.'s certification
  under the EU-U.S. Data Privacy Framework could not be confirmed, so the DPF is not named. IP discarded and no
  location derived, no screen recordings, retention 12 months, the DPA accepted. §3 and §5 point to §7; §6 and §8
  carry the settled facts. Deleting an account does not reach the events at PostHog: their random id is not linked
  to the account and the app does not show it (Art. 11 GDPR; the app's text said "if you give us the ID", which a
  user cannot do). Error events name their type or error code.
- **Re-consent:** the version bump stays, the conservative path. The check-in adds Art. 9 data under the existing
  explicit consent (same purpose: computing scores), so no new consent purpose is needed, but the consent must
  name it, and the check-in writes require consent to the policy in force (D300). App Attest (Art. 6(1)(f)) and
  the analytics section (its own opt-in) are information duties and would not need it on their own.
- **`TODO(user)` closed: 4** (PostHog's GeoIP and session recording, its retention, its transfer basis, and what
  deleting an account does to its events). 24 remain in the four files (privacy 18, terms 2, Impressum 3, support
  1), plus 3 in `messages/en.json`. The markers that need the user's details (controller, Impressum, Microsoft and
  GoDaddy, Supabase, DPO, authority) are unchanged.

**Amended 2026-11-27, before the merge** (head architect, on the same rule):
- **U14 decided:** `CONNECT_MIN_POLICY_VERSION=2026-11-26` (item 9).
- **Effective 2026-11-27** on privacy and terms (the planned merge day); the version id stays `2026-11-26`, which
  consents reference. No guard ties the two: `legal.test.ts` only wants a date, `check-policy-version.mjs` and the
  live check read the version.
- **No `TODO(user)` renders on the site** (supersedes "open points stay `TODO(user)`" for page text). **The
  Impressum carries no personal details** (item 7 stands; a brief that kept the live page was corrected the same day):
  the service name RYFT, `support@ryftlab.de`, and "A postal address for service of documents is available on request
  by email." in place of the address; no name, address or phone. **The Impressum is incomplete under § 5 DDG until a
  business or service address exists (Linear RFT-156 A1).** Privacy §1 and terms §1 say "the provider named in the
  legal notice". The transfer sentences keep the facts the markers carried; Art. 77 names the member state where the
  user lives or works; every other marker is dropped without a replacement sentence (nothing invented). `/support`
  loses the response-time line, `/licenses` names the provider in the legal notice as rights holder, the styleguide's
  demo loses its literal marker. `beta.missingEmail` stays: `deploy.yml` refuses a build without `SUPPORT_EMAIL`, so it
  never renders in production. The open points stay in LEGAL_CHECKLIST U1-U15 and RFT-155.

## D286 — The production smoke signs in as a Google service account, mapped to the smoke account only (2026-11-26)

Supabase's email provider is off for good (the user's decision, 2026-11-26), so the smoke account cannot get a Supabase session and every deploy stopped in its auth preflight (run 45777989628). api and ingest now also accept a Google-signed ID token of the service account `ryft-smoke` (RS256 from Google's JWKS, `aud` exactly `ryft-smoke`, `email` and `sub` the account's, `email_verified`, `exp`) and treat it as `SMOKE_USER_ID` and nothing else: no email, provider, operator role or second factor. The route is chosen from the unverified `iss` (Google's or not), and each verifier checks the whole token, so a forged `iss` can only fail. All four settings or none. `/readyz` stays the Supabase verifier's. `ryft-smoke` holds no role; `roles/iam.serviceAccountOpenIdTokenCreator` on it goes to ryft-deployer and to the operators in `smoke_token_creators`. The smoke mints its token through `generateIdToken` and checks the claims before any call. There is no secret: Google rotates the keys, and revoking means removing a binding or disabling the account. A leaked token is good for at most an hour, and only as the smoke account. Its worst case is writing synthetic data into that account through the smoke's own routes (it can neither give nor withdraw consent); it cannot delete the account (see the allowlist below). The smoke principal never creates a users row: a missing or deleted account is a 401 (`smoke_unknown_user`).

After an independent security review:
- **Server-side binding.** The UUID in config.env is not trusted alone. The database must also name the account in `smoke_account` (migration 0060): one row at most, readable by api and ingest, writable by no service role. The operator writes it once with `scripts/sql/bind_smoke_account.sql`, which refuses an account without a users row, with a non-email identity, or tombstoned. Every smoke request checks it and fails closed with 401 `smoke_unbound`. A real user's UUID copied into config.env therefore yields only 401s. The script does not trust the pasted UUID either, since it comes from the same place as config.env. It resolves the account on its own, by its email `smoke@ryftapp.de` in `auth.users`, and refuses unless exactly one row has that email and its id is the pasted one (second review).
- **Route allowlist.** A smoke principal reaches only the routes `smoke_prod.sh` calls (api: me, invite, days, config, model, connections; ingest: `/v1/sync`). Everything else gets 403 `smoke_route`, including `DELETE /v1/me`, export, train import and nutrition. The admin auth refuses the smoke principal outright.
- **No consent by machine** (second review). POST and DELETE `/v1/me/consent` are off the allowlist. Consent is a person's act, and with a wrong binding a machine credential would fabricate one. The smoke no longer probes the consent gate (it reports `not probed`; `SMOKE_EXPECT_GATES=on` checks the invite gate only). The preflight requires the account to hold an unrevoked consent, so a missing one stops a run before migrate. A consent for another version than the commit's `PRIVACY_POLICY_VERSION` only warns. The sync's gate takes any unrevoked consent, and an exact match would let a consent recorded ahead of a bump, or for a reverted one, block every deploy (the code review). When the version moves, the operator records the smoke account's consent before the merge with `scripts/sql/consent_smoke_account.sql`, which writes only for the bound account and re-checks its email. Rejected: an admin consent route, because an operator key that can create consent for any user is worse; and exempting the smoke principal from the consent gate, because a gate bypass in production would then go unsmoked.
- **Startup.** The Google key set loads in the background, so a slow Google never delays a cold start or a Supabase request. Smoke tokens get 503 until it is in.
- **Where tokens go.** The helper mints and sends a Google smoke token only to `https://api.ryftlab.de` and `https://ingest.ryftlab.de`.
- **The deploy.**
  - The post-rollout smoke always runs, with a fresh token. When it fails, traffic returns to the revisions recorded before the rollout.
  - The replace, the smoke and the rollback run in one step, with one EXIT trap as the only rollback handler. Each service is recorded before its replace is issued. A failed replace, a failed smoke or any unexpected error (a failed `describe`, say) therefore sends every recorded service back. The lookup of a new revision is no longer best effort (third review): a failed one rolls back, and no later replace starts.
  - **Bounded** (third review). Every gcloud call of the record step, the rollout and the rollback runs under `timeout`. The forward calls (replace, lookup, smoke) never run past the job's 60 minutes minus the restores' reserve (4 × 90 s + 180 s). Each restore has its own 90 s and is tried whatever the others did. `smoke_prod.sh` bounds every curl and itself (600 s). Persisting the rollback state across a killed runner was ruled out of scope. Instead the step prints the hand-restore commands before anything moves.
  - **Proof from the image** (third review). Labels and env can drift from the code, so the record step reads which commits built the serving api image: the registry tags of its digest (`<sha7>`, plus a run-unique `<sha7>-<run>-<attempt>` that a rebuild of the same commit can't move), each resolved in the full history. #249 (`4bca49d`) is the pin: D285's change and main's last commit before #250. D285 = the label AND an image commit containing #249. The bootstrap = none of the four smoke settings AND an image commit at #249 or older. A newer image with missing, empty, partial or secret-backed settings is config drift, an error in every mode, never a bootstrap. Rejected: pinning api-00044-5lh's digest (one more value to copy by hand, and it proves nothing for the next rollback target); image labels read by a pull (the legacy image has none, so the tags would be needed anyway). Limit: the proof lives as long as the tags, which the registry's 30-day rule deletes with the image (OPERATIONS → The D285 floor).
  - `migrate_only` also skips site-build and site (third review): a migrations-only run publishes nothing.
  - **Replacing the smoke account** is a deadlock by design: the preflight compares the serving api with the commit's `SMOKE_USER_ID`, and only a rollout changes the api's. The way out (OPERATIONS → Replacing the smoke account; untested): the config PR, `migrate_only` for its migrations, the rebind, and the env of api and ingest set by hand. It is rare enough not to warrant a bypass in the workflow.
  - No input picks the smoke authentication (`auth_preflight` is gone; second review). The record step reads the serving api revision before migrate. With `SMOKE_OIDC_SA_ID` in its env the run uses the preflight. Without it, a dispatch runs the bootstrap check, which also requires the running api to reject the token, and a push stops before migrate ("the D286 bootstrap is pending"). So once D286 serves, the bootstrap cannot be chosen. Skipping the preflight against the old revision is safe because the new revisions are still smoked, and rolled back on failure.
  - The dispatch input `migrate_only` runs migrate and stops: no smoke authentication, no rollout, no smoke. Migrations are expand-only and run before every rollout anyway, so it opens nothing new.
  - **The D285 floor** (second review). An api from before D285 erases the meal portion labels on PATCH, and the serving api is the automatic rollback's target. Every api built from #250 on carries the revision label `ryft-meal-patch=d285` (a #249 build is D285 but unlabelled; none ever served, since its deploy failed) (`deploy/run/api.yaml`, required by `check_cloudrun.py`). The record step refuses before migrate, in every mode, when the serving api lacks the label and the commit writes `portion_label_snapshot`/`portion_short_label_snapshot`. That is a `git grep` of `backend/` without tests and without 0053/0054, and a failed grep counts as a writer. Today no file writes them, so a rollback to api-00044 stays allowed. #248 can ship only after a labelled api serves. A label rather than a revision number or commit, because a label states the property itself and survives a rebuild.

Rejected:
- A signing key imported into Supabase. Standby keys are published in the JWKS, so it would have worked against the running API at once, but it can mint any user, any `app_metadata` role and `role: service_role` for the Data API.
- The legacy HS256 secret. The API accepts no HS256, and it is the same kind of master key.
- Admin `generate_link` plus `verify`. It needs the service-role key in the deploy job, and nobody confirmed without a production test whether `verify` works with email off.
- Supabase's `id_token` grant with a service-account token. It needs a provider change and a second identity on the smoke account, and it depends on GoTrue accepting such a token.

Cost: api-00044 does not know the new token, and the binding table exists only once a migrate has run. The bootstrap is therefore four steps, and the only red run is the push run, which changes nothing:
1. The push run of the merge stops before migrate ("bootstrap pending"). Nothing changes.
2. `gh workflow run deploy.yml --ref main -f migrate_only=true` applies 0053, 0054 and 0060.
3. The operator binds the account.
4. `gh workflow run deploy.yml --ref main` runs the bootstrap check, the rollout and the smoke.
The next run uses the preflight by itself. The other way, running 0060 by hand in the SQL editor before the merge, would also work, since the migrate job runs as the same owner, `postgres`. It was not chosen: schema changes reach production only through the migrate job. The first version, two dispatches of which the first rolled, failed `smoke_unbound` and rolled back, was replaced after the second review.

Correction (2026-11-26, after step 2). #250 named the smoke account `smoke@ryftlab.de` (from an old `smoke_prod.sh` comment) and `SMOKE_USER_ID=5258f5d1-…`. That account was created on 2026-11-26 and never used, and step 3 refused it ("not in public.users"). The production smoke account is `smoke@ryftapp.de`, `671779e8-9634-4964-b3a6-4445c6ad1c5f`, in `public.users` since 2026-10-20. `config.env` and the `v_email` of both SQL scripts now name it. The bind's refusal worked as designed: nothing was bound, and no rollout had happened (the #250 push run stopped at "bootstrap pending", api-00044-5lh still serves), so this is still the bootstrap and not **Replacing the smoke account**. The email is one constant per language: `v_email` in each SQL file (each is pasted alone) and `smokeEmail` in the Go test, which pins both files to it. A test now also refuses an otherwise valid account that holds the retired address or a near-miss of the right one.

## D283 — Use one bilingual catalog term for Nutrition suggestions (2026-11-25)

After preserving exact dictionary matches, expand prefixes of at least four normalized characters when the shortest alternate is a prefix of every other alternate. This lets `Apfe` find English apple and apple juice entries without requiring German catalog labels. Competing alternates without a shared dictionary term fall back to primary search. The existing query ranks direct lexical matches ahead of the generic-only alternate and returns catalog labels and IDs.

## D284 — Localize catalog selections and preserve logged portion labels (2026-11-25)

Catalog metadata identifies supported staple forms and measured defaults. Prioritize whole foods on broad staple searches, preserve explicit variant intent, and rank eligible records with account-scoped favorites and 90 days of remaining diary history. Preview amounts through the meal calculator. Reuse a retired or changed portion by its stored compatible mass or volume. Snapshot item labels and only recover a linked legacy label when the existing unit and measurements still agree. Keep source IDs, BLS CC BY attribution and the old search functions through rollout.

Amended 2026-11-26 after review, before release:
- Only a staple group's name or a single word of at least four letters that starts a staple's name makes a query broad and promotes the group. An exact name match alone doesn't. An exact variant (`Milch 1,5 Prozent`) therefore ranks ahead of favourites and history. The group's other forms (apple juice) rank just below cultivars, ahead of brand and prefix matches.
- A keyed source portion's ID includes its unit and measure. A refresh that changes the grams issues a new ID and retires the old row, so a stale selection fails with 422 instead of logging the new amount.
- A food has at most one default portion (a partial unique index); the ETL clears its defaults before re-marking.
- Favourites block an account merge and are exported on the first page of `GET /v1/me/nutrition-export`.
- The schema change is split so that no file holds ACCESS EXCLUSIVE while it scans, backfills or builds an index: 0055 adds columns and `NOT VALID` checks, 0056 validates them, 0057 backfills labels, 0058 builds indexes, 0059 the ranking function. The migrator runs each file in one transaction, so indexes are built without `CONCURRENTLY`; the label columns come from D285.

## D287 — The USDA extract aggregates without ORDER BY and sorts in Python (2026-11-26)

The SR Legacy dry run `--source sr-legacy --limit 10000` on image `d53bb29` (execution `nutrition-etl-job-q6v66`) failed both attempts after ~15 s with DuckDB `OutOfMemoryException`. The cause is the ordered aggregate `list(… ORDER BY …)` in `extract.usda_rows`. DuckDB keeps each group's rows in a small linked list up to 16 rows. Past that it allocates a pair of 2048-row chunk buffers for the group (arguments and sort keys, ~150 KB with the struct's three VARCHARs). These buffers come from the buffer allocator, count against `memory_limit` and can't spill. An SR Legacy food has ~30 of the 39 tracked nutrients, so every group crosses the threshold. At 7,793 foods that's ~1.1 GB, over the ETL's explicit `--memory-mb 768` (the default; `temp_directory` on `/tmp` and `max_temp_directory_size` were already set, so DuckDB's 80 % default isn't involved). The 2026-11-24 run succeeded at `--limit 1000` (~150 MB). The step that matters is the tenfold limit. #248 added struct fields and the staples-first `ORDER BY … LIMIT` in `selected` (a top-N, bounded by the limit). The ordered `measures` aggregate already existed, and its groups are mostly under 16 rows.

`labels` and `measures` now use plain `list()`, which appends to arena-backed segments in proportion to the data. `usda_rows` sorts each row's lists in Python with the old keys: nutrients by `(nutrient_id, amount DESC NULLS LAST)` (NaN first, as DuckDB orders it), so `usda_food`'s first-entry-wins rule still picks the larger amount of a duplicate row; portions by their window `priority`. The container stays at 4 GiB and DuckDB at 768 MB: the data doesn't need more, and a larger `memory_limit` would only move the cliff. `test_sr_legacy_sized_release_fits_the_smallest_memory_limit` pins the bound with 8,000 foods × 30 nutrients at `--memory-mb 128`.

Not reproduced locally: the headless session that wrote this couldn't run Python. The buffer threshold comes from DuckDB's sorted-aggregate implementation, and the 1000-vs-10000 contrast fits it. The CI test runs the SR Legacy shape at a sixth of the production limit.

Staples: all 13 USDA staples are SR Legacy IDs, and bounded runs order them first (`ORDER BY list_contains(staples, fdc_id) DESC`). Any `--limit` ≥ 13 reaches them. SR Legacy has 7,793 foods, so `--limit 10000` already reads the whole release, and `--full` adds nothing for it.

## D288 — Clean USDA portion labels catalog-wide; staples match source portions, not guessed IDs (2026-11-26)

Outside the 16 curated staples, a USDA portion's `label` was the raw source text (`1 medium (3" dia)`) and `labels` was empty, so Go's `portionLabels` showed the raw text in both languages. The ETL now cleans every non-curated USDA portion (`transform.clean_portion`):
- **What it removes:** placeholder measures (`undetermined`, …), parentheticals with imperial measures, and inch marks outside them.
- **How it maps:** the amount, head and qualifier map through three pinned tables (`SIZES`, `UNITS`, `QUALIFIERS`) to an English label, a German label, short labels and a sort order. Sizes: Klein 10, Mittel 20, Groß 30, Sehr klein 5, Sehr groß 35, Jumbo 38. Household units from 40 up: Stück 40/41, Scheibe 42 … EL 70, TL 72, US-Tasse 80. Unknown heads keep `100 + priority`.
- **Fallback rules:** a cup stays a "US cup"/"US-Tasse", as in the staples. `oz` and `lb` become grams and `fl oz` becomes ml. If any qualifier word is unmapped, there's no German label at all, and Go falls back to the clean English one, so labels are never half translated. Two labels that collide with different grams get ` (<g> g)`.
- **Curated stays curated:** staples keep precedence. `is_default` stays curated-only, because `default_portion` promises a measured, explicit default and a heuristic ("Mittel if present") would move people's logged amounts.

IDs don't move. A keyed portion's ID hashes the USDA portion ID and the measure, not the label. An unkeyed portion's ID hashes `legacy_label`, which stays byte-identical. The raw dedupe key is unchanged, so the set of portions per food is unchanged. `legacy_label` now also goes into the staging row, so the merge's manual-twin check matches the Go importer's rows (label = raw text) as before.

The four new staples (Kartoffeln gekocht 170440, Avocado 171705, Tomate 170457, Karotte 170393) and four extras (Salatgurke 168409, rote Paprika 170108, Zwiebel 170000, Naturjoghurt 171284) match their portions **by G1 key, text prefix or grams, never by a USDA portion ID**. The session that wrote them couldn't fetch FDC, and guessing an ID risks a curated label on the wrong measure. They also carry a `source_description` guard: a wrong FDC ID leaves a plain USDA food, and the dry run reports it. Weights always come from the source row, so the user's reference numbers (Kartoffel 60/100/150 g, Avocado ½ 75 g / ganz 150 g, Tomate 90/120/180 g, Karotte 50/70/100 g) are shown for comparison, not imported. Avocado ½ is derived (`half_of`) from the measured whole fruit and is its default.

The staples version goes from 1 to 2. The merge treats a changed checksum under an unchanged `dataset_version` as a conflict, so without the bump the rerun would write nothing. Also in the merge: a curated `portion_key` that moves between surviving rows is released before the upsert, as defaults already are (unique `nutrition_portions_semantic_key`).

Not covered: USDA portions written by the Go importer (`origin='manual'`) keep their raw labels. RLS keeps the ETL role off them by design. The runbook has a read-only count, and cleaning them would be a separate, reviewed operator step.

## D296 — Import BLS 4.0 in full from a committed, reviewed extract; curated synonyms, no machine translation (2026-11-27)

The user decided on 2026-11-27 to import the Bundeslebensmittelschlüssel fully, with curated aliases and no machine translation. OFF at scale (G3) is deferred. Only about 130 production foods have a German name today.

**Licence.** BLS 4.0 is published under **CC BY 4.0** (Creative Commons Namensnennung 4.0 International). The Max Rubner-Institut must be named as publisher ([blsdb.de/download](https://blsdb.de/download), [OpenAgrar](https://www.openagrar.de/receive/openagrar_mods_00112643), both from search snippets: the session could open neither page). The FAQ names app and software development as a use ([blsdb.de/faq](https://blsdb.de/faq), search). CC BY 4.0 allows commercial use, storage, adaptation and redistribution, including serving the data through our API and showing it in the app. It needs credit, a licence link and a note that we changed the data (CC BY 4.0 §3(a)), "in any reasonable manner" for the medium. Nothing blocks our use. The user confirmed the terms on the download page on 2026-11-27 (the file needs no registration): "Die Daten des Bundeslebensmittelschlüssels (BLS) stehen als Open Data zur freien Verfügung. Die Nutzung ist unter der Lizenz CC BY 4.0 (Creative Commons Namensnennung 4.0 International) gestattet. Bei Verwendung ist das Max Rubner-Institut als Herausgeber zu nennen." Citation, verbatim from that page: **Max Rubner-Institut (2025): Bundeslebensmittelschlüssel (BLS), Version 4.0 — Deutsche Nährstoffdatenbank. Karlsruhe. DOI: 10.25826/Data20251217-134202-0**. Where it appears:
- **Per food:** `food_sources.attribution` carries the exact citation above (`transform.BLS_ATTRIBUTION`), with `license_identifier` `CC-BY-4.0` and `source_url` `https://doi.org/10.25826/Data20251217-134202-0`, served as `sources[].attribution`. The attribution is part of each food's checksum, so the change ships together with the CSV. Without the CSV the three staples keep the version `staples2`, and a new checksum on the same version would be a `conflict`.
- **The site's licences/legal page (#252) and the app's About/licences screen:** DE: "Enthält Daten aus dem Bundeslebensmittelschlüssel (BLS), Version 4.0, Max Rubner-Institut (2025), Karlsruhe, DOI 10.25826/Data20251217-134202-0, lizenziert unter CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/deed.de). RYFT hat eine Auswahl der Nährstoffe in eigene Einheiten umgerechnet, Suchbegriffe ergänzt und Lebensmittel ausgelassen, die ein kuratiertes Grundnahrungsmittel doppeln." EN: "Contains data from the German Nutrient Database Bundeslebensmittelschlüssel (BLS), Version 4.0, Max Rubner-Institut (2025), Karlsruhe, DOI 10.25826/Data20251217-134202-0, licensed under CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/). RYFT converted a selection of nutrients to its units, added search terms and left out foods that duplicate a curated staple."
- **The repository:** `scripts/etl/data/README.md`.

**Source of the data.** The job does not download BLS. `scripts/etl/bls_convert.py` (stdlib only, a streaming xlsx reader) turns the official `BLS_4_0_Daten_2025_DE.xlsx` into `scripts/etl/data/bls_4_0.csv`: code, German name, English name, and our registry nutrients in our units. It also writes `bls_4_0.manifest.json`: the source sha256, the header → component → unit mapping, unparsed cells and counts. Both are committed after review. Why: the download page's layout and links are unknown to us, and a committed extract gives a reviewable diff, deterministic input and stable IDs. The image needs no new dependency, no DuckDB and no network for BLS. The converter fails closed. It exits without output on unknown units, missing macros, name columns that don't look like code/German/English, or unparsable numbers in required components, and it lists what it found. The session that built this could download neither the file nor its documentation, so the layout comes from documentation snippets (columns 1–3: code, German and English name; three columns per component: value per 100 g, origin, reference; EuroFIR-style codes such as ENERCC, PROT625, FAT, CHO).

**Import.** Provider `bls`, external ID = the BLS code, food UUID uuid5(`bls:<code>`), so IDs are stable across reruns. `canonical_name` is the German name, with `de` and `en` names and `markets` `DE`. Nutrients are per 100 g edible portion. BLS gives no density or volume basis that we found, so no BLS food has a per-100-ml basis. The categories come from `classify` with the BLS main group (the code's first letter) as a hint. They are discovery only and carry no `ryft_nutrition` scoring profile, the same as OFF, because the BLS names give no single-ingredient evidence we have reviewed. `food_form` comes from a German state word in the name (`roh`, `gekocht`, `gegart`, `gebraten`, `gebacken`, `getrocknet`, `tiefgefroren`). BLS foods get no `canonical_group_key`, so the forms never move a row in the ranking. Version: `py-etl4:bls_4_0:staples<v>:<sha256 of CSV, curation, staples and BLS_TRANSFORM_VERSION>`. Changed inputs give `updated`, never `conflict`, and a rerun gives `unchanged`. Without the CSV (or with `--no-bls-csv`), `--source bls` imports the 3 staples under `py-etl4:bls_4_0:staples2`. Since the data commit's attribution change that gives 3 `conflict` over the old rows, and `--write` refuses when the catalog holds a full import before any source runs (`refuse_staples_over_full_bls`, a read-only check for any `bls` source outside the curated staples, so it holds under any later version), so it never replaces the staples' CSV nutrients with the curated subset.

**Staples and duplicates.** The curated BLS staples (M111300, M111200, M713100) keep their names, aliases, portions, group, form and density. Their nutrients now come from the CSV, and the dry run reports `staple_nutrient_drift`. A BLS food that is the same food as a curated USDA staple (a *twin*, e.g. "Möhre, roh" next to the staple Karotte) is listed in `scripts/etl/bls_curation.v1.json` → `twins`, with its expected German name as a guard. It is **not imported**, so a German query shows the staple once. On a name mismatch it imports as a normal food (`twin_mismatch`). The code PR shipped the list empty because nobody had seen the file yet; the data commit fills it (below). The dry run proposes `twin_candidates` (the first name segment equals a staple's German name or alias). They are reviewed with the data commit, before the first write. The merge never retires rows, so a twin added after a write stays visible (`stale_twins` in the dry run) until it is retired by hand.

**Aliases.** `bls_curation.v1.json` → `synonyms`: 40 curated DACH synonym groups (Möhre/Karotte/Mohrrübe/Rüebli, Speisequark/Quark/Topfen, Brötchen/Semmel/Schrippe/Weckle, Sahne/Rahm/Obers, Blumenkohl/Karfiol, Tomate/Paradeiser, Aprikose/Marille, Hackfleisch/Gehacktes/Faschiertes, Porree/Lauch, Rotkohl/Rotkraut/Blaukraut, Rote Bete/Rote Rübe, Feldsalat/Rapunzel/Nüsslisalat, Frikadelle/Bulette/Fleischpflanzerl, …). Each is applied case-sensitively to whole words of the BLS German name, with at most 6 aliases per food. Case sensitivity keeps German nouns apart from adjectives ("Paprika, orange" gets no "Apfelsine"), and whole words keep compounds such as "Lauchzwiebel" and "Sauerrahm" out. This is substitution, not translation. Pairs we weren't sure of were left out: Pflaume/Zwetschge, Pfannkuchen/Eierkuchen, Krapfen/Berliner, Paprika/Peperoni, Champignon/Egerling.

**Portions.** None for BLS foods: they get `reference_portion` (100 g) and the app's g/ml input. We don't link BLS foods to USDA portion sets: there is no reviewed crosswalk, and those sets are US sizes. Twins are skipped, so the staple's curated portions serve that food. The three BLS staples keep theirs.

**Ranking (migration 0061).** For `de`, BLS German names reach match 7 (exact), 6 (prefix) or 4 (text). SR Legacy rows have only English names and reach a German query only through the D283 alternate term (tier 2), so BLS ranks above SR Legacy ("Linsen" → "Linsen, getrocknet" before "Lentils, raw"). Staples keep tiers 10/9 through promoted groups ("Apfel", "Gurke", "Karotte"). One gap needed a change: a BLS row whose German name equals the query and a staple whose alias equals it both land in tier 7, and the tie-break read `preferred_market` first (BLS `DE`, USDA none), so BLS won ("Möhre"). 0061 replaces `search_foods_v2` with `c.is_canonical_staple DESC` right after `c.match DESC`. The signature is unchanged, so older API binaries keep working. For `en`, BLS English names and SR Legacy compete as equals. On a tie, a caller in market DE gets BLS first, and others get language, quality, completeness and similarity order. D283 and D284 are otherwise unchanged.

**Size.** About 7,140 foods × (1 food, 1 source, ~2.1 names, ~30 nutrient values, 1 classification) ≈ 250k rows. That is ~30–45 MB with indexes (the research note's per-row sizes), against the 8 GB of the Pro plan. The job reads a ~1.5–2.5 MB CSV and uses no DuckDB for BLS, so the 4 GiB job and the 768 MB DuckDB cap (D287) are untouched. The offline converter streams the sheet. A CI test parses a full-size synthetic sheet (7,140 × 418) and asserts peak RSS under 512 MiB, with `RLIMIT_AS` 1 GiB on Linux.

**Not done (code PR).** The extract itself: the session could not download the file. There is no English-name crosswalk to SR Legacy and no scoring profile for BLS foods. Both are follow-ups. Migrations 0061–0063 are taken (0062/0063 below), so the no-watch recovery implementation (D291–D295) starts at 0064.

**The data commit (2026-11-27).** The user downloaded `BLS_4_0_Daten_2025_DE.xlsx` from blsdb.de, with no registration (14,093,078 bytes, sha256 `524bbefe25b691f5cb3de7a9f3e27fa2967aebfeabf217d99414ba7806e78c60`; one sheet, header in row 1, columns `BLS Code`, `Lebensmittelbezeichnung`, `Food name`). The layout matched the documentation, but three of our guesses about it were wrong. The converter now follows the file, and each fix has a test:
- **Markers.** Value cells hold `TR`, `<LOD`, `<LOQ`, `<LOD or <LOQ` or `-` (23 protein and 26 fat cells among them, so the converter failed closed). Each marker's data-origin cell says what it means: `TR` comes with "Spuren", the limit markers with "Analyse"/"Literatur"/"Nährstoffdatenbank"/"Labelangabe", and `-` with origin `-`. So the trace and limit markers are stored as **0**, as a measured trace would be, and `-` is **unknown**. Afterwards a measured 0 and "below the limit" look the same; the manifest's per-component counts are the only record. Only the exact spellings count: any other text in a required component still stops the run. The manifest counts the markers per component, and `converter_version` is 2. Three additives have `-` as fat (R172000 glutamate, R411000, R421300), so the ETL rejects them as `missing_or_invalid_macro`.
- **Codes.** 772 foods have a capital letter in position 3 or 4 (`M5B1600` Gorgonzola, `P2A3000` Rotwein trocken, `M1A5200` lactose-free UHT milk, `U57B100`). The pattern `[A-Z][0-9]{6}` skipped them as non-food rows. The new pattern is `[A-Z][0-9][0-9A-Z]{2}[0-9]{3}`, in the converter, in `transform.BLS_ID` and in `nutrition_bls_attribution`. Migration **0062** swaps that constraint in `NOT VALID` in one ALTER, so the check never lapses, and **0063** validates it. They are two files because the runner wraps each file in one transaction, the same split as 0055/0056. The new pattern is a superset of the old one, so every existing row passes.
- **EPA/DHA.** The headers are `F20:5CN3` and `F22:6CN3` (g/100g). The header-code pattern now accepts a colon inside a code. There is no selenium, trans-fat or caffeine column in BLS 4.0.

Result: 7,140 foods, 0 skipped rows, 33 components, 0 unparsed cells, a 1.58 MB CSV that passes the `bytes` character policy. **Twins** (20), filled only where the BLS food and the staple are clearly the same food in the same state. Review found that the first candidate rule (the first comma segment) missed most of them, because BLS 4.0 writes the state after a space ("Banane roh"). `twin_keys` now also compares the whole name, the name without state words and that without spaces. The candidates were then checked against the CSV by hand, and five twins whose names differ from the staple (Karotte/Möhre, Speisezwiebel, Gemüsepaprika rot, Rapsöl/Rüböl, Kartoffel geschält) were found by search:
- `B111000` Weizenvollkornbrot → 172690; `C133000` Hafer Flocken → 173904 (rolled oats, dry)
- `C352000` Reis poliert, roh → 169756; `C352032` Reis poliert, gekocht → 169757 (9.8 mg sodium, so unsalted, as the staple is)
- `E111100` Hühnerei roh → 171287; `E111132` Hühnerei gekocht → 173424
- `F110100` Apfel roh → 171688; `F110600` Apfelsaft → 173933 (vitamin C 0, as the staple's "no added ascorbic acid" says); `F502100` Avocado roh → 171705; `F503100` Banane roh → 173944
- `G480100` Speisezwiebel roh → 170000; `G520100` Gurke roh → 168409; `G543100` Gemüsepaprika rot, roh → 170108; `G561100` Tomate roh → 170457; `G620100` Karotte/Möhre, roh → 170393
- `K110132` Kartoffel geschält, gekocht → 170440 (2.6 mg sodium; the staple is boiled without skin and salt)
- `Q120000` Olivenöl → 171413; `Q180000` Rapsöl/Rüböl → 172336
- `V416100` Hähnchen Brustfilet, roh and `V4A6100` Hähnchen Brust, ohne Haut, roh → 171077 (two BLS rows for the same raw breast meat)

Not twins, and they stay separate foods:
- `B101000` Vollkornbrot: in Germany usually a rye–wheat bread, while the staple is whole-wheat.
- `X820162` Reis gekocht: a dish, salted (101 mg sodium).
- `V416162` Hähnchen Brustfilet, gebraten ohne Fett (Ofen): close to the roasted staple 171477, but not clearly the same preparation.
- The plain yoghurts (`Joghurt mind. 3,5 % Fett` appears in several rows, and none is clearly the staple's plain whole-milk yoghurt).
- Every other state (dried, cooked, baked, frozen) and every dish (X/Y codes).

The dry run without a database gives processed 7,140, accepted 7,026, skipped 20, rejected 94, `aliases_added` 1,554, no staple drift and no staple missing. The X and Y codes (dishes, about 2,000) have no `BLS_MAIN_GROUPS` hint, so they are classified from the name alone. That was already so before this commit.

**The 91 `atwater_deviation` rejects** are kept for now. The rule (4/4/9 kcal per g, ±30 %, exceptions for alcohol and ≥ 15 g fibre) is the ETL's shared quality gate. BLS energy follows Regulation (EU) 1169/2011: fibre at 2 kcal/g, organic acids at 3 and polyols at 2.4. So the rule rejects kale (9 forms), raspberries, currants, celeriac, artichoke, shallots, oyster mushrooms, rhubarb, plain coffee and tea (1 kcal against 0), and also acids, polyols and sweeteners. Proposed follow-up, a BLS-only check: 4P + 4C + 9F + 2·fibre + 7·alcohol, ±30 % with a 5 kcal floor. By hand that accepts kale, raspberries and coffee, and keeps lemon concentrate and polyols out. It changes a quality rule, so it isn't part of the data commit. Rejected rows are never written, so the follow-up adds them as `inserted`.

## D291 — Recovery without a watch: three tiers, the wearable wins, a check-in is the fallback (2026-11-26; contract only)

The brief reserved D290–D294; the legal PR #252 already takes D290, so this work uses **D291–D295**.

- **Context:** a user who does not wear a watch at night has no `DailyScore.recovery` (no overnight HRV or
  resting heart rate) and today gets nothing in its place. The product spec proposes a morning check-in and
  session RPE. Contract: `docs/API.md` → Recovery without a watch. Implementation outline:
  `docs/work/2026-11-26-no-watch-recovery/implementation.md`.
- **Decision:**
  1. **One rule, the wearable night:** the night ending on the day has asleep stages (not `in_bed` only) or HRV
     readings in the sleep window, **and** `DailyScore.recovery` is not null. Recovery alone is not enough: it is
     computed from resting heart rate too (`scoring/compute_steps.go`), which a watch worn only by day writes
     every day (review #1). The tiers, `needs_checkin`, `superseded` and the readiness path all use this rule.
  2. **Tiers** (`RecoveryStatus.confidence`, closed): `high` = a wearable night; `medium` = a check-in and wearable
     heart rate in at least 8 distinct hours of the day before (the "day watch"); `low` = a check-in only;
     `null` = nothing. 8 hours is a RYFT heuristic.
  3. **Precedence:** wearable night > check-in > nothing, per wake day. A later sync that brings a wearable night
     **supersedes** the check-in: it stays stored (`superseded: true`, still in the history and the export) and
     every consumer (status, briefing, periodization) uses the wearable. Deleting the wearable data makes the
     check-in current again. Never averaged: a subjective score next to a measured one would read as a second
     measurement.
  4. **`needs_checkin`:** 04:00 local or later, no wearable night, no check-in yet, and the flag on. The sync
     carries no `was_user_entered` on sleep segments, so a night typed into Apple Health with asleep stages
     counts as measured (review #2; decided 2026-11-27: kept so, no sync field for now).
  5. **Routes, mapped to what exists** (the spec named `/api/v1/…` and other paths): `/v1/recovery/checkins/{date}`
     (`PUT` upsert, `GET`, `DELETE`) and `GET /v1/recovery/checkins?from&to` with the `/v1/days` range rules;
     `{date}` is the wake day, as every day in v1.
  6. **`GET /v1/recovery/status` next to `CoachingBriefing.recovery` (a deviation):** the briefing needs the
     Nutrition-to-Recovery profile (`store/n2r_guidance.go`: no profile, no briefing), so it reaches only the
     protected pilot. The no-watch user is mostly not in it. The same `RecoveryStatus` object therefore has its
     own route, and the briefing embeds it.
  7. **Suggested bed and wake times are a pre-fill** of the check-in form (median of the last 7 days' check-ins,
     at least 3; else the N2R `expected_bedtime` and 8 h later; else none), never advice; the Sleep Planner stays
     the recommendation.
- **Rejected:** writing a check-in score into `DailyScore.recovery`: it would change what an existing v1 field
  means and mix a subjective estimate into the measured trend and baselines.
- **Decided by the user, 2026-11-27** (the open questions in `docs/work/…/implementation.md`):
  - **The Today ring shows the check-in score**, visibly marked as estimated. The app fills the ring from
    `RecoveryStatus.recovery_score` while `source` is `checkin` and labels it from `source` and `confidence`;
    `DailyScore.recovery`/`readiness` stay wearable-only (so the rejection above stands), and trends, baselines
    and insights never contain a check-in.
  - **Sleep-only trackers, no special case** (conservative default): the one rule applies, so a night with asleep
    stages but no recovery (no heart rate, no HRV) is not a wearable night, its user is asked, and the check-in is
    scored on the reported times; measured sleep is not blended into a check-in. (The question was written before
    review #1 made recovery part of the rule; under the rule no measured-sleep user is left without an estimate.)
  - **Hand-typed sleep** (conservative default): counts as measured, as the sync cannot tell; no additive sync
    field in this work. Adding one is its own contract change with the app.
  - **Apple's workout effort score** (conservative default): not read; `session_rpe` is only the rating given in
    RYFT (Apple's score is its own construct, and a second source would need its own field and calibration).
  - **Rollout** (conservative default): allow-listed testers only, not before a published privacy version names
    the check-in; no percentage rollout, neither to the beta at large nor to the Nutrition pilot as a whole,
    without a new decision.

## D292 — The check-in score: a hybrid of wellness, duration, regularity and N2R, every constant versioned (2026-11-26; contract only)

- **Decision:** a pure function in `internal/scoring` with calibration tests, computed when a check-in is saved,
  carrying the scoring `ModelVersion`; the implementation bumps it (the next `2026.11.N`, a `changelog.json` entry,
  `affects: []` since no trend metric moves) and a newer model rescores stored check-ins. Constants:
  - `wellness_absolute = (0.65·quality + 0.35·freshness − 1) / 9 · 100` (1 → 0, 10 → 100).
  - Personal blend from **5 check-ins in the 14 days before**: `z = (absolute − mean) / max(SD, 6)`,
    `personal = clamp(50 + 25·z)`, `wellness = 0.7·absolute + 0.3·personal`.
  - `duration`: plateau 100 from `L` to `L + 120` min, linear to 40 at 300 min, 40 below; above the plateau
    −10 per hour, floor 75. `L = 450`, or `sleep_base_min` clamped to 360–600 when it is learned or manual.
  - `regularity`: midpoint vs the circular mean of up to 7 prior nights (at least 3; wearable night first),
    100 at ≤ 30 min, 50 at ≥ 120 min, linear.
  - `recovery_score = 0.50·wellness + 0.30·duration + 0.10·regularity + 0.10·n2r`, missing parts dropped and
    the weights renormalised, rounded, clamped 0–100.
  - `acute_drop`: `z ≤ −1.5`, or quality ≤ 4 and freshness ≤ 4.
- **Sources:** self-reported wellness as a training-response monitor: Hooper & Mackinnon (1995, Sports Med 20:321)
  and Saw, Main & Gastin (2016, Br J Sports Med 50:281), who find subjective measures track training stress at
  least as well as common objective ones. **Every number above is a RYFT heuristic:** the 65/35 split, the 70/30
  blend, the SD floor, the 50 + 25·z mapping, the duration curve, the regularity band and the 50/30/10/10 weights
  come from the product spec, not from a study, and are named as such in `docs/SCORING.md` when they ship.
- **Where it deviates from the spec, and why:** the spec's `0.65·quality + 0.35·freshness` "scaled to 0–100" is
  fixed as `(x − 1)/9` so 1 is 0 and 10 is 100; its duration band "100 in 7.5–9 h, decay above 9.5 h" left
  9–9.5 h undefined, now 100 (plateau `L` to `L + 120`); "gentle decay" is −10 per hour; the z mapping
  `50 + 25·z` is new (the spec gave none); a missing part is renormalised rather than counted as 0 (most
  no-watch users have no N2R and no regularity in their first week, and a 0 would punish that); `soreness` is
  stored and shown and adds a readiness reason but does not move the score (the spec gave it no weight; a
  weight without data to calibrate it would be invented). **Decided by the user, 2026-11-27:** soreness is
  recorded with zero weight, captured for a later calibration: it moves neither the score, `acute_drop` nor the
  readiness state, and only adds `soreness_reported`. `docs/SCORING.md` says so when it ships.
- **Pinned cases:** 5 h, quality and freshness 3, no history → 29; 8 h, 9 and 9 → 93; 14 days of check-ins
  at 6 and 6 (absolute 55.6, SD 0 → floored to 6), then 5 and 5 (44.4) → `z` −1.85, `personal` 3.7,
  `wellness` 32, `acute_drop` by `z` alone; 4 check-ins in 14 days → no personal part.

## D293 — Session RPE on the workout id, mapped onto the TRIMP scale; a check-in's bedtime retroactively feeds N2R (2026-11-26; contract only)

- **Session RPE (Foster et al. 2001, J Strength Cond Res 15:109):** `PUT /v1/workouts/{id}/rpe`, 1–10 in halves,
  stored per workout id; `Workout.session_rpe` and `session_rpe_load` (minutes × RPE, arbitrary units).
  - **Why the workout, not the Train or sync bodies (a deviation):** `PUT /v1/sessions/{id}` and
    `POST /v1/sessions/{id}/complete` decode strictly and are queued writes, so a new field sent to a server
    that doesn't know it is a `400` and today's builds drop such a write; `POST /v1/sync` is the HealthKit
    mirror, and the rating is the user's input in RYFT. The workout route covers both kinds of manual workout:
    a HealthKit workout entered by hand (`was_user_entered`) and a Train session (`Session.workout_id`, its
    `ryft-session` workout when no HealthKit one matched).
  - **Load:** the coaching load (`energy.Load`: acute 7 d, chronic 28 d, ACWR, TSB) counts a rated workout with
    neither usable heart rate (no TRIMP) nor power (no TSS) as Banister's TRIMP with `r = session_rpe / 10` in
    place of the heart-rate-reserve fraction: `minutes × r × a × e^(b·r)`, with `energy.Workout`'s constants
    for the user's sex (0.64/1.92 male, 0.86/1.67 female, 0.75/1.795 otherwise). Measured load wins. Reading
    RPE × 10 as the reserve percentage is a RYFT heuristic. The spec's "duration × RPE, mapped onto the
    internal scale" is kept as `session_rpe_load` (AU) for display. A flat factor was rejected (review #4):
    TRIMP per AU is `0.075·e^(0.1795·RPE)` with the default constants, 0.13 at RPE 3 and 0.32 at 8, so one
    factor overstates easy sessions up to 2.3×. So 60 min at RPE 8 = 480 AU → 151 (unspecified sex),
    143 (male), 157 (female).
  - **Day strain stays HR-based** (`DailyScore.strain`, `WorkoutDetail.trimp`): putting reported effort into the
    0–21 strain and its sleep-need top-up moves stored numbers for every user and needs its own calibration.
    **Decided by the user, 2026-11-27:** session RPE does not feed day strain; it feeds acute and chronic load,
    ACWR and TSB only, as drafted.
- **Retroactive N2R:** the N2R bedtime precedence becomes the measured sleep start (`daily_scores.sleep_start`
  of the next day, as `nutritionBedtimes` reads it) > the next morning's check-in `bedtime` > `expected_bedtime`.
  `N2RScore.bedtime_source` (`expected`/`wearable`/`checkin`; absent when `bedtime` is null) says which.
  **Explicit, and live rather than stored** (review #5): the N2R the app reads (`yesterday_n2r`, the Nutrition
  daily summaries via `addN2RDays`) is computed on read from `nutritionBedtimes`, so the check-in bedtime as a
  fallback there changes D − 1's alcohol, caffeine and late-meal penalties as soon as the check-in is saved or
  deleted. The stored night record (`daily_biometric_baselines.n2r_score_breakdown`) is written only by the tick
  and only for nights with overnight biometrics, i.e. wearable nights, where the measured bedtime wins anyway.
  So it never needs the check-in bedtime, and no recompute job or event is added. The check-in path's
  confounders are taken from the live D − 1 N2R, not from that record. What makes it explicit: `bedtime_source`
  on the N2R and `n2r_bedtime_applied` on the check-in. The N2R formula and `n2rVersion` do not change; only an
  input does.

## D294 — Readiness without overnight HRV: reported state names, no diagnosis (2026-11-26; contract only)

- **Decision:** `CoachingBriefing.readiness` uses a current check-in where the HRV path yields `UNKNOWN` (no
  overnight HRV, or not enough for its baseline, `availability.go`), after the availability guardrail, and only
  with a coaching plan, as today. No `acute_drop` → `OPTIMAL`. `acute_drop` with a confounder
  of the evening before → `ACUTE_LIFESTYLE_SUPPRESSION` (the existing value; reasons the confounders, then
  `checkin_below_usual` or `checkin_low_quality_and_freshness`). `acute_drop` without one → the new
  **`LOW_RECOVERY_UNEXPLAINED`**, which `periodization.Plan` treats like the HRV path's unexplained drop
  (deficit suspended, carbohydrate floor 4.5 g/kg). `soreness` adds `soreness_reported`.
- **Confounders:** the N2R tags the HRV path already uses (`alcohol_above_035gkg`, `caffeine_bed_high`,
  `late_satfat_meal`, from the live D − 1 N2R with the reported bedtime, D293) **and, a deviation, the journal of the day before**
  (`alcohol`/`alcohol_drinks`, `caffeine_after_2pm` or `last_caffeine` within 8 h of the reported bedtime,
  `late_meal` → `journal_alcohol`, `journal_late_caffeine`, `journal_late_meal`): most no-watch users are not in
  the N2R pilot but can keep the journal. **Decided by the user, 2026-11-27:** `sick`, `travel` and `work_stress`
  of the day before are explanations too (`journal_sick`, `journal_travel`, `journal_work_stress`), so a drop
  with one of them is `ACUTE_LIFESTYLE_SUPPRESSION`, not `LOW_RECOVERY_UNEXPLAINED`. A judgement call on top:
  with `journal_sick` the plan stays protective (no deficit, the 4.5 g/kg carbohydrate floor), because
  `ACUTE_LIFESTYLE_SUPPRESSION` alone keeps a fat-loss deficit (`periodization/plan.go` suspends it only for
  the unexplained state) and a deficit while ill is the riskier default. These three apply to the check-in path
  only; the HRV path's confounders do not change (that would move what `AUTONOMIC_FATIGUE` means today).
- **Names:** no diagnostic label. `AUTONOMIC_FATIGUE` claims a nervous-system cause that a questionnaire cannot
  show, so the check-in path never emits it; the new name says what was observed (low, unexplained) and nothing
  about why. App copy for both check-in states repeats what the user reported and names no physiological cause.
  `AUTONOMIC_FATIGUE` stays on the HRV path (v1 renames nothing).
- **The neutral name, decided by the user 2026-11-27; the form is ours:** a new field
  `CoachingBriefing.readiness_display`, always present from the implementation PR on (not behind the flag; it
  carries no check-in data). It equals `readiness`, except that `AUTONOMIC_FATIGUE` is displayed as
  `LOW_RECOVERY_UNEXPLAINED`, the same observation the check-in path names. `readiness` keeps its value set, so
  the pilot app keeps decoding and branching on `AUTONOMIC_FATIGUE`, and the plan logic keeps reading
  `readiness`. The app keys copy on `readiness_display` and falls back to `readiness` when it is absent.
  - **Rejected:** emitting a new state in place of `AUTONOMIC_FATIGUE` (behind the flag or a negotiation header):
    an existing build would read it as `UNKNOWN` and lose the protective message, a negotiation header is a new
    mechanism for one value, and the flag would tie an HRV-path name to the check-in feature. An alias inside
    `readiness_reasons`: reasons are evidence, not the state's name, and copy keyed on them would be fragile.
- **Compatibility:** `readiness` was never declared closed; the contract now says to read an unknown value as
  `UNKNOWN`. `CoachingBriefing` is not in the Swift mirror; the pilot app is the only reader.

## D295 — The check-in's consent, flag, deletion, export and privacy text (2026-11-26; contract only)

- **Consent:** the check-in and the session RPE are health data (Art. 9 GDPR: perceived sleep, freshness,
  soreness). Both writes need consent to the policy in force (`403 consent_required`, `warn` acting as `on`, as
  for Nutrition writes); the reads and deletes do not, so a user who withdrew can still see and erase.
- **Flag:** `recovery.checkin`, enabled at 0 % (testers by the allow-list; decided 2026-11-27 as the rollout:
  allow-listed testers only, after the privacy version, no percentage without a new decision; D291). Off: the two writes and the status
  route answer `404` without a `code` (what an older server answers too, so the app has one rule: hide the
  feature, never drop local data), and the briefing's `recovery` is `null`. Reads and deletes keep working.
  It is a **kill switch**: stored check-ins and ratings count nowhere while it is off (readiness, load, the N2R
  bedtime), and turning it off deletes nothing. No new error `code`.
- **Deletion and export:** `DELETE /v1/me` deletes check-ins and ratings with the account; a HealthKit deletion of
  a workout deletes its rating; the account merge moves both; `GET /v1/recovery/checkins` (≤ 400 days a call)
  feeds the app's export, and the workout export carries `session_rpe` already.
- **Examples as `http` blocks** until the model types exist (as for Connections, D257): the doc test ties every
  `json` block to a Go type, and this PR adds no Go. The implementation PR turns them into typed `json` blocks.
- **Privacy:** the policy in force (2026-10-26) and #252's draft 2026-11-26 do not name subjective sleep times,
  ratings, soreness or session RPE, and #252 defers N2R and coaching to a later version. The sentences #252
  needs, and what waits for the N2R version, are in `docs/work/2026-11-26-no-watch-recovery/implementation.md`
  → Privacy. The feature stays dark until a published version names it.

## D298 — The check-in score's open details: which drop reason wins, and how the numbers are computed (2026-11-27)

- **Context:** implementing D292–D294 (`internal/scoring/checkin.go`, `internal/energy`, `internal/periodization/checkin.go`)
  left details the contract does not fix. None changes a pinned case of `docs/API.md` or D292.
- **Decision:**
  1. **Both drop rules hold → `checkin_below_usual`.** A check-in can be both 1.5 SD below the user's usual and
     rated ≤ 4 twice. One reason is emitted (the contract's table names one), and the personal comparison wins:
     it is what `acute_drop` is about ("well below the user's usual"), and for a user whose ratings are always
     low it is the one that says something; the absolute rule is the fallback for the first weeks, when there is
     no `z`. Copy for both says what the user reported, so neither choice claims more than the other.
  2. **The score uses the unrounded parts**; the components are reported rounded. The pinned cases (29, 93, 32,
     77, 31) come out the same either way; unrounded avoids double rounding.
  3. **Personal SD is the sample SD (n − 1)** of the prior absolutes, recomputed from their quality and freshness
     (the stored component is rounded: 14 × 6/6 then 5/5 gives the pinned `z` −1.85 only unrounded), then floored
     at 6. The mean and SD exclude the day itself.
  4. **`sleep_duration_min` truncates** to whole minutes (`wake − bed`), as "in whole minutes" reads.
  5. **Regularity without a mean direction is `null`:** prior midpoints that cancel out on the clock (e.g. 00:00,
     06:00, 12:00, 18:00) have no circular mean; the part is left out rather than scored against noise. Only the
     newest 7 midpoints count (the caller passes them newest first).
  6. **Late caffeine:** `last_caffeine` counts when it is at most 8 h before the reported bedtime, the boundary
     included, and also when it is after the bedtime (a report that late is late caffeine).
  7. **`confounders` are `[]` without `acute_drop`** (`periodization.CheckinConfounders` takes the drop), so the
     stored check-in and the readiness reasons cannot disagree.
  8. **sRPE load takes the workout's moving time** (`Workout.Seconds()`, as the heart-rate TRIMP does) and a
     rating outside 1–10 counts as none; heart rate counts as usable only with a reserve (resting and max heart
     rate known), as on the heart-rate path, so a workout with `avg_hr` but no resting heart rate takes the
     rating.
- **Rejected:** emitting both drop reasons (the contract's table and the app's copy key on one).

## D299 — The no-watch recovery implementation: the details the contract leaves to the store and the routes (2026-11-27)

- **Context:** the implementation of D291–D295 and D298 (`internal/store/recovery.go`, `cmd/api/recovery.go`). None of this
  changes a sentence of `docs/API.md`.
- **Decision:**
  1. **The score is injected into the store.** `internal/scoring` imports `internal/store`, so the store cannot call
     `scoring.CheckinScore`. It gathers the facts (`CheckinFacts`: the report, the sleep base, the prior ratings and
     nights, yesterday's N2R and tags, the journal of the day before) and takes a `CheckinScorer`; the api passes
     `scoreCheckin`. One transaction holds the upsert and the rescoring of the later check-ins within 14 days
     (`lockCheckin`, advisory class 7419010). The store's tests use a stand-in scorer; the real numbers (29, 93, 77,
     the z blend, 62 with regularity) are pinned through the api.
  2. **`wearableNightSQL` is the contract's rule:** the day's `daily_scores.recovery` is not null, and the night has
     asleep stages or an overnight HRV. The stages are `sleep_segments` of stage `asleep`, `core`, `deep` or `rem`
     ending in scoring's night window of the day (local 20:00 before to 13:00, `scoring.NightWindow`); an `inbed`-only
     night has none. `daily_scores.sleep_has_stages` (core, deep or rem when it was scored) and `hrv_ms` (the stored
     sleep-window HRV) answer first, so the `sleep_segments` lookup (index `sleep_user_end`) runs only for a day that has
     a recovery and neither, such as the older Garmin firmware that writes `asleep` alone. The one rule is a SQL
     fragment used by the range read, the status, the readiness states and the regularity's nights.
  3. **Confounders are those of the save.** They are computed from the live D − 1 N2R (with the check-in's bedtime) and
     the journal of the day before when the check-in is saved or rescored, and stored; the briefing reads the stored
     ones. A meal logged afterwards moves D − 1's N2R at once but the check-in's confounders at the next rescore (a
     later check-in, a recompute, a model bump).
  4. **Suggestions** take the check-ins of the 7 days **before** today (`today − 7 … today − 1`, at least 3); medians
     are taken across midnight (the clock shifted by 12 h), over each check-in's own clock (`tz_offset_sec`).
  5. **The flag is a kill switch for counting, not for showing.** While it is off the status, the readiness path, the
     load, the N2R bedtime and `n2r_bedtime_applied` ignore check-ins and ratings; `Workout.session_rpe` and the check-in
     reads still show what is stored (the owner's access, as for the DELETEs). `GET /v1/workouts` and the day read
     `RatedWorkoutsBetween`; scoring reads `VisibleWorkoutsBetween`, which has no rating (its role cannot read the value).
     A check-in's bedtime goes into N2R only: not into the nutrition diary's meal-timing score, and not into a stored
     N2R night (`refreshN2RNight` reads its history with the flag off), which would outlive the kill switch.
  6. **Required fields:** `tz_offset_sec` and `soreness` are pointers in `RecoveryCheckinPut` and `session_rpe` in
     `WorkoutRPEPut`, so a missing one is a `422` and not a silent 0 or false. A malformed `{id}` is `400` as in
     `GET /v1/workouts/{id}`.
  7. **A model bump** rescores stored check-ins at the next `POST /v1/recompute` of the account
     (`RescoreStaleCheckins`; best effort, never fails the recompute), and any `PUT`/`DELETE` rescores the later ones.
  8. **Migration 0065 is corrected before it ships:** the foreign key to `workouts(id)` cannot exist (the key of
     `workouts` has been `(user_id, id)` since 0007), so it is `(user_id, workout_id) REFERENCES workouts (user_id, id)
     ON DELETE CASCADE ON UPDATE CASCADE`. That also keeps a rating its workout's owner's in the database, and the
     merge's `UPDATE workouts SET user_id` carries the rating with it. The grants end up as follows. `ryft_ingest`
     (the fold): `SELECT (user_id, workout_id, updated_at)` and `UPDATE (workout_id, updated_at)`, no `DELETE` (a dropped
     copy's rating goes with its workout through the foreign key, as the owner), never the value. `ryft_scoring`:
     `SELECT (user_id, workout_id)` and `UPDATE (workout_id, updated_at)` on `workout_ratings`, and
     `UPDATE (start_at, end_at)` on `workouts` (per column, not per row: the statement is limited to source
     `ryft-session`). `ryft_api` has the table.
  9. **A rating follows its session.** A Train session's own workout whose times are edited is **updated in place**
     (`EnsureSessionWorkout`; its load columns start over and the day's pass scores it again, as for a new row),
     where it used to be deleted and inserted under the same id, so the cascade no longer drops the rating. When
     scoring attaches a session to another workout, `ReleaseSessionWorkouts` moves the rating **from the workout the
     session had before this pass** (`sessions.workout_id` as read at its start, passed in as `previous`) and from its
     own workout, to the one chosen: own to HealthKit, HealthKit to HealthKit, HealthKit back to own. A target that has
     a rating keeps it, and of two ratings for one target the previous workout's wins. The move and the releases run
     in one transaction.
- **Rejected:** a `scoring` grant to insert ratings (a service that derives numbers writing a user's input); keeping
  the confounders live in the briefing (the N2R history read would run per check-in on every briefing).

## D300 — The no-watch recovery's second pass: consent at the write, a recovery-free plan, one strength transaction, no times grant (2026-11-27)

- **Context:** a static adversarial review of #260 against `main` found six holes. All six were checked in the code,
  confirmed, and fixed with a test (`docs/work/2026-11-26-no-watch-recovery/review-codex.md`). This entry amends D299.
- **Decision:**
  1. **Consent is checked again at the write.** `PutCheckin` and `PutWorkoutRating` take the admitting consent grant
     and revalidate it in their transaction under the consent lock `RevokeConsent` takes (`lockApply` before
     `lockCheckin`), as the HealthKit sync does. A withdrawal committed in between answers `consent_required` and stores
     nothing.
  2. **A saved plan carries a recovery-free twin.** When a check-in changed the day's plan, `daily_energy_states.plan`
     also holds the plan as it would be with recovery off (`recovery_off`; jsonb, no schema change). Readers with the
     flag off and the stored N2R night restore it. The capture computes projection, the evaluation's history and calibration
     with the flag off. The evaluation's adherence and availability are measured against the plan the user was
     shown, which is the `recovery_off` one once the flag is off. So a check-in leaves no trace in any number stored
     after the flag goes off.
  3. **Reconcile keeps a rating.** When `wearables.Reconcile` drops a rated copy of a session, the kept copy takes its
     session RPE if it has none. Measured HR or power load still wins over the rating, and the rating counts once.
  4. **The day's strength pass is one transaction** (`Store.ApplySessionPass`): attachments, session loads, rating
     moves and releases, planned from the mapping read before the pass. A failure rolls it all back, and the retry starts
     from the same mapping. Rating moves follow chains of reassignments. A swap of two rated workouts leaves both
     ratings where they are.
  5. **Scoring has no grant on workout times** (amends D299 item 8). 0065 is changed in place (unmerged, applied
     nowhere): the `UPDATE (start_at, end_at)` column grant and its trigger are gone (the file revokes and drops them, so
     a rerun stays clean). `public.scoring_follow_session_times(user, workout)` is SECURITY DEFINER with
     `search_path = pg_catalog` and EXECUTE for `ryft_scoring` only. It takes no times: it reads them from the owner's
     completed session of that workout. `EnsureSessionWorkout` calls it.
  6. **The outermost log redactor covers the recovery paths** (`redactPrivatePath`, formerly `redactNutritionPath`):
     `/v1/recovery/checkins/{date}` and `/v1/workouts/{id}/rpe` log their pattern even when auth, the rate limit, App
     Attest, the beta gate or the mux rejects the request.
- **Rejected:** rebuilding a flag-off plan on every read (the stored plan is what was shown; a second copy is cheaper
  than replaying the day); a trigger that checks ownership on the times (it still lets the caller pick the times).

## D297 — The sync button reads `GET /v1/me/sync`: per-source status and "scores pending", no new route (2026-11-27; contract only)

- **Context:** the home screen gets a sync button (syncing, up to date < 30 min, stale ≥ 30 min, error; a long
  press lists Apple Health, Garmin and Suunto). The user's spec asked for `synced_at`, `sources`
  (`last_sync_utc`, `status` OK/PENDING/ERROR) and `data_freshness` (`UP_TO_DATE`/`SYNCING_CLOUD`/`STALE`), and
  for the button not to turn green before the scores, readiness, N2R and the briefing include the new data.
- **Where:** `GET /v1/me/sync` (D91), which already says what the server has and what it is doing with it
  (`last_sync_at`, `pending_days`, `expected_within_sec`), is behind a 10 s cache and passes the beta gate. A new
  `GET /v1/sync/status` would be a second answer to the same question. `GET /v1/me` is heavier (`samples_7d`,
  `account_hint`), uncached, and the Profile screen's. `POST /v1/sync` cannot say when scoring ends (below).
  So the route gains three fields: `sources`, `freshness`, `scores_pending_since`.
- **Names follow the contract, not the spec:** `synced_at` is the existing `last_sync_at` (no second field for
  the same time); `last_sync_utc` is `last_sync_at` (every time in the API is UTC with an `_at` name); the status
  values are lower case like every closed enum here. `data_freshness` becomes `freshness` with only what the
  server knows: `current` or `scores_pending`. `UP_TO_DATE` and `STALE` are the app's 30-minute rule on
  `last_sync_at`; the server reports times and work in progress, never a colour.
- **"Scores pending", the server's rule:** either of:
  1. a `score_queue` row with `scored_at IS NULL` (in `pending_days`) for today or yesterday (local), or for a
     day of the last 30 queued in the last 10 minutes. Scoring a past day rescores up to 30 stored days after
     it in the same pass (`baselineDays`), today included, before the row is marked;
  2. a `provider_inbox` row of the user in state `pending` (a connection's delivery not stored yet).

  Readiness is part of the day's score. A sync stores its records, then its apply transaction queues their
  days (`ApplySync` → `queueAndEmit`), both before the `200`. The window between the two lies inside the
  uploading app's own request, which shows syncing anyway. A direct delivery is stored, its days queued and
  its inbox row processed in one transaction (`ApplyDirect`).
- **Why the day window:** a day queued long ago that scoring never clears (a dead letter, or an account that
  scoring skips for consent) would otherwise keep the button off green for good, and a 400-day recompute would
  spin it for minutes. Such days stay in `pending_days`; the home screen does not show them.
- **What it does not track, and why:** for accounts with Nutrition-to-Recovery, the morning N2R baseline
  (`daily_biometric_baselines`, written by the api tick's `RefreshN2RNights`) and the coaching briefing built
  on it; and the coaching pilot's partner deliveries (`nutrition.wearable_inbox`, drained by
  `processWearables` on the same tick). They follow within one 15-minute pass, and the contract says so. The
  first version tracked them (a baseline older than its day's score, an uncompleted job), but review showed
  the baseline term fires almost always. Scoring's 15-minute pass rewrites today and yesterday for every
  active user and moves `computed_at` even when nothing changed. The same scheduler job ticks scoring and the
  api, so `processed_at < computed_at` recurs every tick, and the button would never turn green. The job term
  would have needed `claim_wearable`'s conditions and a new grant for a pilot-only path. Tracking them exactly
  needs an event-driven N2R refresh after scoring, or `computed_at` moving only on a change: a question for
  the user, not decided here.
- **Not exact, and why it is acceptable:** `MarkScored` clears a day queued before its run started
  (`queued_at <= started`). `queued_at` is the queueing transaction's start time (`now()`), and `started` is
  the scoring instance's clock. A run that starts while `ApplyDirect`'s transaction is open, before its
  commit, does not see the records, but marks the day done. The delivery's own event then rescores it seconds
  later, and the silent refresh push follows. The contract says so. Making it exact (clear only when
  `queued_at` still equals the value the run read at its start) is a scoring change, follow-up F1 in the
  implementation outline. `scores_pending_since` restarts when a day is queued again (`queued_at = now()` on
  conflict), so it measures the current wave, not the first arrival.
- **Per source:** `healthkit` always, with `last_sync_at` from `devices.last_sync_at`, `ok`, or `error` with the
  code of the gate that refuses it, each gate checked against its own mode (beta `warn` with consent `on` and
  no consent is `consent_required`); `warn` lets syncs through and stays `ok`. The server never says `pending`
  for it: an upload in flight is the app's state. A provider is listed when its connection is `connected` or
  `needs_reauth`, the server serves the provider and `connect.<provider>` is on for the account. The rule is
  written out, not "what `GET /v1/me/connections` lists", because that route filters only `available` by
  flag. It has `last_data_at`, `error`/`needs_reauth`, `pending` while an inbox row waits, else `ok`. So Garmin
  and Suunto are absent, not `error`, until Connections is live. A `revoked` connection is absent; the Connect
  screen explains it. A `failed` inbox row (one record ingest could not parse) is no source error: the user can
  do nothing about it, and the operator's gauges show it. The coaching pilot's partner links (D271) are not
  sources: they are operator-provisioned, pilot-only, and a second Garmin entry next to a Connections one would
  confuse.
- **The sync does not wait for scoring:** scoring is another service behind Pub/Sub (D218), taking seconds to
  tens of seconds (`expected_within_sec`), more for a backfill; a held `POST /v1/sync` would hold a Cloud Run request slot and the app's background time,
  and a timeout retry would send the page again. A sync is many pages too. The app takes `scoring.queued_days`
  from the response as pending at once, polls `GET /v1/me/sync` no sooner than `max(expected_within_sec, 10)` s
  (the cache) at that interval for at most 3 minutes, and reloads today and the briefing when `freshness` turns
  `current`. The silent refresh push ends the wait early, but it comes at most once per 5 minutes. A long-poll
  (`?wait=`) was rejected for the same request-slot reason.
- **The bound:** pending work older than 3 minutes shows stale with "Scores are still being calculated", not
  syncing forever and not an error: the data is stored. `scores_pending_since` lets the app apply that after a
  relaunch too.
- **Enums:** `freshness` and `status` are closed (Swift enums); `source` and `reason` are open, so Suunto and new
  reasons need no `/v2`.
- **Examples as `http` blocks** until the model types exist (as D257, D291). The implementation is outlined in
  `docs/work/2026-11-27-sync-status/implementation.md`: one indexed query more behind the cache, a column grant
  on `provider_inbox.received_at` for the api and a partial index, no new table.

## D301 — The sync status is served: `closed_reason`, scoring closes the queue of an account without consent, five queries (2026-11-27)

- **Context:** D297 fixed the contract (#258); this is the implementation PR (`feat/sync-status`), and the
  calls it made that the outline left open. The user's answers of 2026-11-27: the N2R baseline, the briefing and
  the pilot's partner deliveries stay untracked, withdrawn consent makes scoring close the queue rows, offline is
  the app's concern.
- **`score_queue.closed_reason` (migration 0067):** `NULL` or `'no_consent'`, a closed set (a new reason is a new
  migration). It means something only together with `scored_at`: scoring sets both when it closes a row without
  scoring it (`Store.CloseQueue`), and `MarkScored` sets it back to `NULL` when a day is really scored. A row
  queued again (`scored_at = NULL`) may keep a stale value; every reader asks `scored_at IS NULL` first, so a
  stale value on a pending row is never read. That is why the queue upserts of ingest and the api (`queueDays`)
  do not change: no new grant for them and no change of the golden trace. `ExpectedScoreSec` counts only rows
  with `closed_reason IS NULL`: a closed row has `scored_at - queued_at` of however long the account had been
  without consent, which says nothing about how long scoring takes, and would pull the p95 that
  `expected_within_sec` shows to every account.
- **Consent closing lives in `cmd/scoring`, not in `internal/scoring`:** where scoring answers `ErrNoConsent`
  (`handleEvent`) it now calls `CloseQueue` first, and the timer pass does the same for an active user the
  engine does not admit (`refreshActive`). No scoring rule changes: `internal/scoring` is untouched, so no
  `ModelVersion`, no `SCORING.md`, no rescore. A failure of `CloseQueue` is logged and the event is still acked
  (D78: nothing to retry); the next event or the next 15-minute pass closes the rows. Closing twice closes
  nothing the second time (the `WHERE` is `scored_at IS NULL`), so a redelivery is idempotent (D100). If the
  account consents again, the next sync queues its days again and they are scored; closed days are not scored
  retroactively. A closed day leaves
  `pending_days` and stops holding `freshness`; the `healthkit` source shows `consent_required` while the consent
  gate is `on`. Scoring closes the rows of an account with **no consent recorded** (`Admits`: `Consent != nil`
  while the consent gate is `on`); an account that consented to an older policy is still scored while `healthkit`
  shows `consent_required`, and nothing of it is closed. `CloseQueue` takes a cutoff and closes only rows with
  `queued_at <=` it (as `MarkScored` does with `started`), taken before the account is read: a re-consent and a
  sync racing the close queue their days again after the cutoff, and those days stay pending and are scored.
- **"A day of the last 30" (D297's window) means today − 30 through today:** `syncFreshness` holds the button for
  a day in that range queued in the last 10 minutes, and for no day after today (a timezone change can leave
  one). The lower bound is today − 30 because scoring's rescore reaches 30 stored days after a changed day, so
  today − 30's rescore reaches today.
- **Why not a retention of the old rows instead:** the day window of D297 already stops such a row holding the
  button after two days. Closing it also removes it from `pending_days` (which the app's fallback for an older
  server reads), and it records why the row never got a score.
- **Migration number:** 0067. Main ends at 0063 (#259); #260 holds 0064 to 0066 and merges first.
- **Cost of the route, uncached:** 5 queries, up from 4: the user row with its last device and today's score;
  the kinds; the queue (`pendingQueue`, the same primary-key range as before, with `queued_at` as one more
  column, and `PendingDays` reads it too); the connections (`SyncConnections`: the `connections` primary key,
  and per listed connection one probe of `provider_inbox_user_pending`, at most two today); the global p95.
  On top, `served` reads the flag table (`Store.Flags`, one small query, not cached) once per listed
  connection, as `available` does; none when the account has no connection or `h.conn` is nil (Connections not
  wired), so 5 queries today and at most 7 with two providers. Reading the flags once per request would need a
  `served` variant that takes them; not done, the table has a dozen rows. The index
  holds only `state = 'pending'` rows, so it stays small while the processed rows stay as dedupe keys for 30
  days. The 10 s per-user cache stays: ingest cannot invalidate the api's cache, and the contract's polling rule
  (first call no sooner than `max(expected_within_sec, 10)` s after the sync) is built on it.
- **Evaluation of the gates:** `healthkit` is `error` with `invite_required` only when the beta gate is `on` and
  the account has no access, else `consent_required` only when the consent gate is `on` and the account has
  not consented; each against its own mode. `gateFor` (the `gate` field) is untouched: it answers the first
  failing gate whatever its mode, as D91 says.
- **A `needs_reauth` connection with a delivery waiting is `pending`, not `error`:** API.md's status table lists
  `pending` for "`connected` or `needs_reauth`" with a delivery not stored yet, and `freshness` follows a
  `pending` source. Ingest stores the delivery whatever the grant's state, so the data is on its way into the
  scores; the `error` shows again once it is stored (usually seconds). Error first would have let `freshness`
  say `current` for data no score has.
- **Where the window lives:** `syncFreshness` in `cmd/api/syncstatus.go`, a pure function of the queued days
  with their `queued_at`, the user's local today and `now`. The `score_queue` primary key lists only the
  account's unscored rows, so the filtering happens in Go, not in SQL.
- **Not changed:** `MarkScored`'s `queued_at <= started` (F1 stays a follow-up), `POST /v1/sync`, the events,
  the Cloud Run templates. Rollback: revert the api and scoring commits; the migration stays (a column, an
  index and two grants nothing old reads or writes).

## D304 — The contract for goal, training load, net energy and the Fitness tiles (2026-11-27)

- **Context:** the user approved a new app structure on 2026-11-27 (a Fitness tab in place of Strain, Nutrition
  as tab 4, a goal step in onboarding, Profile as a sheet). Head-architect's brief C1 asks for the contract
  first: `docs/API.md` only, additive, implementation in later PRs. The design and the inventory of what
  exists are in `docs/work/2026-11-27-fitness-contract/plan.md`. D302 is taken by `fix/merge-race` (#262), and
  D303 is left for the revision of #252.
- **Goal on `User`, separate from `GoalProfile.goal`:** `User.goal` is the app-wide goal (`health`,
  `endurance`, `strength`, `body_composition`, `performance`, or `null` for "decide later"). The pilot's
  `GoalProfile.goal` (`HYPERTROPHY`, `MARATHON`, `CYCLING_RACE`, `FAT_LOSS_RECOMP`) is a nutrition plan goal
  that needs height, race date and the rest. Merging them would force every user into the pilot's profile, or
  give the pilot's plan a goal it can't compute targets for. Neither one writes the other. A table in API.md
  maps one to the other, and the app pre-fills Nutrition's picker from it. `health` and `performance` have no
  Nutrition default, because no deficit or surplus follows from them. `endurance` pre-selects by the dominant
  activity of 28 days. The value is **open** in responses, so a sixth goal needs no `/v2`. `PATCH` accepts
  only the five values or `null`.
- **Sex and weight on `User`:** no RYFT Age contract exists, and `N2RProfile` is pilot-only, so they go on the
  profile. `sex` uses N2R's vocabulary (`female`, `male`, `unspecified`), plus `null` for never asked, and is
  closed. `weight_kg` is one weigh-in. **The newest weigh-in wins**, whether manual (`PATCH`, recorded at the
  request time) or HealthKit `body_mass` (at the sample's `start`). There is no manual pin like
  `max_hr_manual`, because a pinned weight goes stale. `weight_kg: null` clears the manual value and falls back
  to the newest HealthKit reading, and a deleted `body_mass` sample hands over to the next newest weigh-in.
  `sex` feeds RYFT Age and the resting estimate. `N2RProfile` keeps its own sex and body mass, and the pilot
  path does not change. Today's server refuses unknown body fields (`400`), so the app sends the new fields
  only once `GET /v1/me` carries the key `goal`. A `PATCH` with `sex` or `weight_kg` needs consent to the policy
  version that names them, else `403 consent_required`. `goal` is not gated.
- **Training load on `DailyScore`, computed on read:** the Fitness tab already reads `GET /v1/days/{day}` and
  `GET /v1/days?from&to`. A new route would add a request and its own pending state. Computed on read from
  stored day values (`strain.trimp`, `muscular_trimp`, session ratings), a late sync of an old day moves the
  next 28 days at once. Storing the value would need a cascade of rescores, and no stored number moves this
  way. **Cardio** is the heart-rate TRIMP plus the TRIMP of rated workouts without usable heart rate (fewer
  than 2 heart-rate samples in the window and no `avg_hr`, scoring's rule), so a user without a watch has a
  load. The rating counts on `strain.trimp`'s scale (its constants and its 0.25 reserve floor), not on
  `energy.RatedWorkout`'s scale (sex constants, no floor), because summing the two scales would mix units.
  The implementation PR pins the conversion in SCORING.md. Day strain still ignores the rating (D293 stands). **Muscular** is the
  existing session load (`muscular_trimp`). Per-set RPE is too sparse to build on. The windows are 7 days
  acute and 28 days chronic in weekly units, named `chronic_28d_weekly` like `energy.Load`. Today counts so
  far. The optimal band is 0.8–1.3 × chronic (Gabbett
  2016), a RYFT display heuristic. `state` shows from 21 of 28 days with data. It is not the coaching load:
  `CoachingBriefing.training_load` stays the pilot's workout-based number with power and TSS, and the doc says
  so next to both.
- **Net energy on `DailyScore`:** intake is the day's logged meals, behind the diary flag. Active energy is
  `active_kcal`. Resting energy is HealthKit `basal_energy` when synced, else a weight-only estimate (Henry
  2005: needs `weight_kg` and `birth_year`, no height, which `User` does not have). `net_kcal` is `null` when
  any part is missing. `missing` and `confidence` say why: confidence drops a step when a logged item has no
  known kcal (`complete_for_logged_items: false`). `partial_day` marks today. A missing part is never counted
  as 0.
- **Tiles:** VO₂ max is synced but read nowhere, so it gets day fields with a 7-day delta
  (`null` without a reading in those 7 days, so that it is never a false 0). Heart-rate recovery
  is a new HealthKit type and a day mean. Active time is derived (workouts ∪ minutes ≥ 50 % HRR), so it needs
  no new HealthKit type and no privacy change. The zones and active energy exist. Muscle freshness goes on
  `DayDetail` (all 18 muscles of the closed vocabulary), because it describes one moment. It is a decay model
  over completed sets, and the scoring PR pins it as a RYFT heuristic.
- **New HealthKit types are gated by the policy version:** the policy of 2026-10-26 lists neither basal
  energy, nor heart-rate recovery, nor body mass from Apple Health. From the implementation PR on, the server
  stores them only while the flag `sync.fitness_types` is on for the account **and** its consent is to a
  policy version that names them. Otherwise they count as `unknown_type`, and a cursor for such a stream is
  skipped. A `200` moves the app's HealthKit anchor, so samples ignored that way are lost. That is why the app
  sends the types only while `GET /v1/config` → `flags["sync.fitness_types"]` is `true`: the flag is the app's
  signal and the kill switch, so no ordering is left to an operator. A source that sends only `body_mass` is
  not listed in `User.sources`, because the `kinds` enum stays closed. The text,
  `PRIVACY_POLICY_VERSION`, the consent list and App Privacy change together in one bundled privacy update,
  with sex, weight and goal. That update is not part of this PR.
- **The four open questions, answered (head-architect, 2026-11-27, on the user's rule "take the best sound
  option, don't block"):** (1) The goal names stand as they are (`health`, `endurance`, `strength`,
  `body_composition`, `performance`, `null`). `health` and `performance` get no Nutrition default: the user
  picks the Nutrition training goal separately, as today. (2) The newest weigh-in wins, manual or HealthKit, as
  in Apple Health. A manual entry is a new weigh-in with `weight_source: manual`, with no separate override flag.
  (3) The band is 0.8–1.3 × the 28-day weekly average (the usual acute:chronic range), and `state` shows only
  with ≥ 21 of 28 days of data. It is a training guide, not a medical or injury-risk claim, and API.md says so.
  (4) The privacy carrier is **neither #228 nor #252**. #252 merges first, as it is. Resting energy, heart-rate
  recovery and body weight from Apple Health, and sex, weight and goal, go into the **next** privacy version, a
  bundled update made when PR 3 (the sync types) is ready. Until then `sync.fitness_types` stays off and the
  guard holds: the server stores none of the three sample types and refuses `sex` and `weight_kg` (`403
  consent_required`). `goal` is not consent-gated in this contract; the policy text names it in that update.
- **Old builds:** they ignore every new field. Every new field is optional to decode, because a server before
  the implementation PRs omits it. The examples are `http` blocks until the model types exist.
- **Implementation order:** (1) profile fields, (2) training load, (3) the sync types with the gate, (4) net
  energy, (5) VO₂ max, HRR and active time, (6) muscle freshness. About 9–10 days. 2 and 6 can run beside 1.
  Rollback: this PR is docs only.

## D302 — The account merge moves and erases `from` in one transaction (RFT-88, 2026-11-27)

- **Context:** the merge (D90) committed the move, and the handler then erased `from` in a second transaction.
  In the gap `from` was a live account: a sync, a journal save or a Train write that passed `EnsureUser` landed
  on it and was erased with it. Inside the merge, `copyRows` read sessions and journal days without row locks, and
  a save of an existing session or journal day locks no `users` row (an upsert that updates checks no foreign
  key), so `users … FOR UPDATE` did not wait for it: the merge copied the old version and the new entries went
  with `from`.
- **Decision:** `MergeUsers` (real run) runs `eraseUser` (the body of `DeleteUser`: tombstone, Apple token to the
  tombstone, connection tokens to the deregistration queue, every delete) on `from` in the same transaction as the
  move, then commits. `DeleteUser` is `lockUser` plus `eraseUser`, unchanged. Supabase auth user, Apple
  revocation and the provider grants run after the commit (`afterErase`, shared with `DELETE /v1/me`); the
  handler's "the data is moved, deleting failed (run the merge again)" 500 is gone, because the erasure is
  atomic with the move and the reconciler retries Apple and Supabase.
- **Lock order:** `lockUser` for both accounts, then `lockApply` for both, each pair in uuid-string order; then
  (real run only) `from`'s `users` row `FOR UPDATE`, then `from`'s rows `FOR UPDATE` in `sessions`,
  `session_exercises`, `session_sets`, `journal_days`, `journal_entries`, before `m.run` reads them. `lockApply`
  waits for a sync, HealthKit write or fold in flight; the `users` row for an insert that references `from`; the
  row locks for a save of an existing session or day. The other paths that take `lockApply` on two accounts (the
  connection confirm, which orders with `bytes.Compare`, and the inbox, `ORDER BY user_id`) use the same uuid
  order as the merge, so the advisory locks do not invert. **Deadlocks are still possible**: scoring's
  `ApplySessionPass` updates a `sessions` row and then inserts a workout (a key-share lock on `users`), the reverse
  of the merge's `users` then `sessions` order. Postgres detects it (`40P01`) and aborts one side, usually the
  merge; `MergeUsers` rolls back and runs the whole transaction once more (the dry run too), and a second
  deadlock is an error (`500`, run it again). The merge does not check that `to` or `from` has no nutrition data
  or connections inside the transaction (the handler checks before it); moving the checks in is a follow-up.
- **What a late writer sees** (it waits for the commit, then fails; no data is lost):
  (a) a request whose `EnsureUser` runs after the commit finds no row and the tombstone: `401` with the reason
  `deleted`, as after `DELETE /v1/me`; (b) a plain write in flight across the commit (a journal or Train save,
  `UpsertSamples`) fails its foreign key to `users` (`23503`): `500`, and the app's retry gets the `401`; (c) a
  HealthKit sync or a check-in with a grant waits on `lockApply` and then gets `consent_required`, because
  `requireGrant` finds the consents erased, so the app may show the consent screen briefly before the `401`.
  Before, these writes answered `2xx` and their data was erased. No new status or `code`: `API.md` is
  unchanged.
- **Cost:** the merge transaction now also deletes `from`; it is bounded by `deleteTimeout` and the route is
  admin-only. Rollback: revert; no migration.
