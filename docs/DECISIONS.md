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
