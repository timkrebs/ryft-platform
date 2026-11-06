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

## D87 — Gate modes: off, warn, on

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
  key of a delivery from `ryft.events` is its routing key; `dlq.sh` replays only bound keys.
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
  **Amended by D206:** P12 `ryft-argo-scope`, where Argo CD's identities may write (never `vault` or `cert-manager`); 13
  policies, 18 bindings (#121, chart 0.6.0, not enabled).
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

## D180 — Argo CD's ClusterRoles are an allowlist, and everything else that decides what it may do is guarded (T12-f; amends D120; #47, merged 2026-11-05, **applied by the owner 2026-11-06**)

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
- **What is true now that this is applied (since 2026-11-06 the narrowed roles are the live ones, not the chart's `*`):** a stolen controller token can no
  longer grant itself write on the admission policies through RBAC. "Argo CD cannot switch off the admission layer" is **not** true until P6 and P12 are both in Deny and this
  change is applied (P12 is #121, open; P12 judges names only, so the RBAC route needs P6 and P12 together). With
  authentication mode `API` (#118, open) the `aws-auth` route closes too.
- **Rollback** is `git revert <squash-sha>` (this repository squash-merges), which also removes the guard; a partial hand
  rollback is refused by it on purpose. A new operator Role needs Server-Side Apply; the UI no longer shows the live
  manifest of a webhook configuration. The plan's human view hides the values but the saved plan holds them in clear (the
  admin password's bcrypt hash): never commit or paste `argocd-rbac.tfplan`, delete it after the apply.
- **State:** merged, and **applied by the owner on 2026-11-06** (Step 0, OPERATIONS → Deploy and roll back → "What Argo CD
  may no longer do"). The owner's checks: the controller's ClusterRole holds no `*`, the 11 Applications are Synced, the
  controller cannot patch or delete `validatingadmissionpolicybindings` and cannot impersonate, and it can list
  Applications (D206). **Kind runs:** the last full-mode `admission-e2e` run on #47 was
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
- **Amended by D206:** P12 `ryft-argo-scope` joins the layer (chart 0.6.0, 13 policies, 18 bindings); `vault`, `cert-manager`
  and `clusterResourceNamespace` are never in Argo CD's scope, so "Argo CD writing a well-shaped Certificate in `vault`" above is
  refused by P12.

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
  Beyla exclusion of `vault` (OPERATIONS → Before the Vault add-on is applied), then an Explore check that `{namespace="vault"}`,
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
- **Amended by D206:** P12 does **not** already admit it. `argoScope.namespaces` is an explicit list that leaves out the import
  service's namespace on purpose, so V7a needs that namespace added to `argoScope.namespaces` (a reviewed chart change) first;
  `check "admission_argo_scope"` fails the plan while an Application's destination is missing from the list.

## D201 — monitoring's operator objects are an Argo CD application in a project of their own (amends D51, D95; the owner; **decided, not built**)

- **Decision:** `vault-monitoring`, wave 4, project `ryft-monitoring`: destination `monitoring`, and only VaultConnection,
  VaultAuth, VaultStaticSecret and Certificate; no cluster-scoped kind. P7 gets a fixed entry, P12 a carve-out for those four
  kinds in `monitoring`, and renderpolicy binds the project to that path (ticket V7b). The monitoring cutover is then a merge,
  like every other group. **Rejected:** deploying the collector chart on merge through Argo CD or a CI identity.
- **State:** not built; the project is applied by hand (D95), and the admission values change with an apply of
  `infra/aws/kubernetes`.
- **Amended by D206:** P12 has no carve-out by kind today; it admits every namespaced object in the namespaces it lists, and
  `monitoring` is not one of them. V7b needs a P12 change first (an entry that admits only those four kinds in `monitoring`,
  with harness cases and a chart bump); adding `monitoring` to `argoScope.namespaces` instead would let a stolen controller
  token write anything next to the collector, and is not the decision.

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

## D206 — P12: Argo CD's identities write only where its apps render (amends D120, D195, D200, D201; #121)

- **Why (2026-11-04):** the #47 re-review found that the controller's narrowed ClusterRole still
  writes ConfigMaps, Services, ServiceAccounts, Deployments and Jobs in every namespace, and the
  argo-cd chart's namespaced Roles let argocd-server write every Secret and ConfigMap in `argocd`.
  No policy matched those writes outside `ryft-*`, so a stolen token reached cluster-admin
  (`kube-system/aws-auth`; a Pod as a controller-manager or cert-manager service account) or the
  public UI's admin (`argocd-rbac-cm`, `argocd-secret`). D120's "Argo CD cannot switch off the
  admission layer" was not true.
- **Decision:** a thirteenth ValidatingAdmissionPolicy, `ryft-argo-scope` (P12, one binding for every
  namespace), matched by the username prefix `system:serviceaccount:argocd:` (impersonation-safe; groups,
  `extra` and field managers are not used). The controller writes namespaced objects only in the explicit
  list `argoScope.namespaces` (the apps' namespaces, not a `ryft-` prefix), in `argocd` only Applications and
  events (events: create and update, never delete), cluster-scoped only Namespaces `ryft-web`/`rabbitmq-system`
  (`argoScope.namespaceObjects`, which P1 reads too), `*.rabbitmq.com` CRDs, the operators' prefixed
  ClusterRoles and bindings and four webhook configurations (access reviews are exempt from policies by
  kube-apiserver's exclusion list, claim o); no Namespace or CRD delete. argocd-server writes only
  Applications and events in `argocd`, plus the first `server.secretkey` of `argocd-secret`. The chart's
  redis-secret-init may create Secret `argocd-redis`. Every other `argocd` service account writes nothing. No
  subresource, no CONNECT, no AppProject write, no finalizer carve-out. Content stays P1-P11's and P13's.
  failurePolicy Fail like the others: the bindings are exempt from admission and the break-glass is a patch,
  so Ignore would only turn an evaluation error into a silent hole; every optional request field is read
  through has().
- **Never in scope (the rebase onto #147/#153, 2026-11-06):** `vault` (the Terraform add-on: P2 and P3 judge
  images and peers there, not who writes), `cert-manager` and `vault.clusterResourceNamespace` (the
  `ryft-vault` root CA), `argocd` beyond the row above and `kube-system`. The chart fails a render whose
  `argoScope.namespaces` or `namespaceObjects` holds one; `admission.tf` fails the plan (a precondition) and
  warns while disabled (`check "admission_argo_scope_never"`). Every Namespace object Argo CD may create is a
  `ryft-*` or `rabbitmq-system` one (P1 holds those to restricted) and one P12 lets it write in. The three
  harness cases that waited for P12 (the StatefulSet, `vault-tls` and a NetworkPolicy in `vault` as the
  controller) now expect P12's refusal; the kind run has a ConfigMap in `vault` and the `ryft-vault-ca` Secret
  in `cert-manager` as the controller.
- **P13's Argo CD Issuer rule stays P13's:** P12 judges where, not what, and admits an Issuer in the namespaces
  it lists, so an `acme` or `vault` Issuer in `ryft-api` (which would make cert-manager send a Secret of that
  namespace to the server it names) is P13's to refuse. For a ClusterIssuer and anything in `vault` both
  refuse: two messages for one write, harmless (no ordering between policies, either one refuses in Deny).
- **Amends D200 and D201:** neither app is admitted by P12 as it stands. D200's app for the import service needs
  `argoScope.namespaces` to list the import service's namespace first (left out on purpose: its own Vault credentials, no
  Argo CD app today). D201's app in `monitoring` needs a P12 change first (an entry for exactly those four kinds in
  `monitoring`; P12 has no per-kind carve-out, and listing `monitoring` would admit everything there). Either is a chart
  change with harness cases, merged before the app.
- **Deny gate:** `argo-scope: deny` requires the **general** `operators-cluster`, `namespaces` and
  `workloads-templates` bindings in deny, not their `-vault` twins (P12 judges names, not what a ClusterRole or
  binding grants: the RBAC route is closed when P6 and P12 are both in Deny), and a narrowed controller
  ClusterRole rendered by `argocd.tf` (`controller.clusterRoleRules.enabled`, no `*`, no sync impersonation).
  The chart fails the render, `admission.tf` fails the plan. The gate's expression is the argocd values guard's
  output `controller_rbac_narrowed` (it judges the values `helm_release.argocd` takes; tests run it with a wildcard
  verb, resource and group, the chart's default role and impersonation). On the tree since #47 the precondition
  reads the rendered role as narrowed (a test without the stand-in variable pins it); the owner applied #47 on 2026-11-06
  and checked that the controller cannot patch or delete `validatingadmissionpolicybindings` or impersonate,
  and can list Applications. The full kind mode must pass on a tree with #47, `argo-rbac` and `claim-i` not
  skipped. Sync impersonation (`application.sync.impersonation.enabled`) must stay off: it would make P12
  match nothing.
- **Owner and rollout:** the admission chart (**0.6.0**: 13 policies, 18 bindings; merged after #147's 0.5.0
  and #153's 0.5.1), Terraform, the user's apply; Warn+Audit with Phase 1 (two checklist items: a restart of
  the Argo CD components, an apply of the argocd release), Deny at Step B after the general P6, P1 and P2.
  `admission_policy` depends on the argocd release. **Ships disabled**: `admission-values.yaml` keeps
  `enabled: false`, so the merge changes nothing in the cluster.
- **Testing:** the CEL harness (every render as the controller, the projects and root as admin, the canaries,
  the first-key cases, the `vault` cases), the kind job (every render dry-run as the controller: 0 refused in
  Audit and Deny; P12-only fixtures, among them a `cert-manager` Deployment and a ConfigMap in `vault` as the
  controller), the full run (no P12 finding through sync, hooks, restart and chart upgrade).
- **Accepted:** reads (both Argo CD Roles read Secrets in `argocd`); writes inside the scope beyond the content
  rules, among them in-scope writes with cluster-wide effect (an Ingress for `api.ryftapp.de` with a Traefik
  router priority, a `redirectRegex` Middleware, a Service with `externalIPs`; follow-up T12-l6); the
  controller's choice of revision; Application content written by a stolen server token between Step B and
  Step D; the admission objects stay RBAC's. A refused Argo CD chart upgrade is unblocked by the binding patch
  alone (the policy release depends on the argocd release).
- **Consequences:** a new Argo-made namespace, a new cluster-scoped kind or name, or an Argo CD version that
  writes something new needs the chart first; the kind e2e on every argo-cd chart upgrade covers it. A new
  service needs its namespace in `argoScope.namespaces` (a check compares it with `deploy/argocd/apps`).
  Removing P12 is a forward change (0.6.1), never a downgrade. `-target=helm_release.admission_policy` now also
  covers the argocd release when it has pending changes. The rule group `ryft-admission-intended` holds 18 of
  its 20 rules (D179): two more bindings fill it.
