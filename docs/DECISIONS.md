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
  delivery: follow it with `POST /v1/recompute` (open: a marker in `dlq.sh`).
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
