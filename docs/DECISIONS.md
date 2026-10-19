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
brand board) shows quality 82 as "good" — so Whoop's 85/70 split cannot be used. The
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
- **Logs:** RYFT's pod logs carry users' daily scores and go to CloudWatch (D52), not
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
  without a node.

