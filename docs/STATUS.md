# RYFT platform — status

State of the platform after go-live (M1, M1b and the zero-trust layout; 2026-10-18),
Train (M3b, 2026-10-19; M3 itself is deferred, D58), the sleep upgrades (M4,
2026-10-19), the push and remote-config core of M7 (2026-10-19) and the journal with
behavior impact (M5, 2026-10-19), all deployed (`82506fa`, 2026-10-20) with the Sign in
with Apple and APNs keys active. The **Beta** for friends on TestFlight (2026-10-20) is
deployed (`21eb0fa`). The follow-up **operator tooling, account mix-ups and server-side
sync state** (2026-10-20, `61db1c4`…`4dd5aa1`) is **deployed** (`4dd5aa1`, 2026-10-20,
after a 17-minute database outage during the rollout), and so is the migrate fix that
prevents it (`1015bc7`, 2026-10-21 05:33 UTC, "role password unchanged" ×4). **GitOps
with Argo CD** is live (2026-10-21): Argo CD at `https://argocd.ryftapp.de` deploys
everything from git, CI promotes images by PR, ryft-platform syncs itself (D94–D96,
D103). The move from **NATS to RabbitMQ** is complete (2026-10-21, R1–R4, D97–D102):
RabbitMQ carries every event through the outbox, NATS and its namespace are gone; the
record of each release is under **RabbitMQ: your steps**. Open: the Argo CD health check
for autoscalers (D105, an `infra/aws/kubernetes` apply); the rollback drill ran on 2026-10-22
(`docs/work/2026-10-22-followups/drill.md`).
The **website lives here** since 2026-10-21 (D107): `timkrebs/ryft-web` imported with its
history as `web/`, one CI for Go and the site, one promote per image group with a hold for
rollbacks. Live since 2026-10-22 06:32 UTC; ryft-web is archived with a pointer here.
The **follow-ups** (2026-10-22/23, D108–D111, D115) are deployed: the site's toolchain
policy (#19), Synthetic Monitoring for ryftapp.de as code (#20; your one-time setup is
open), the deploy key rotated into a `deploy` environment with a daily deploy watch and
`hold.sh` (#21), configuration that rolls its pods and a privacy version that goes into
force with its page (F1, #22), the rollback drill, and byte checks and allowlists for
every file, kustomization and Argo CD app (#31, #38). The app's **calibrating report**
(2026-10-22/23, D112–D114, `docs/work/2026-10-22-calibrating/`): the reporter's account has
no HRV source; days are re-scored when their history changes (#29); `DailyScore` carries
`calibrating_reason` and per-signal counts (#30, migration 0027, last 30 days rescored);
the logs hold no health values any more (#35).
Contract:
[`API.md`](API.md). Design: [`ARCHITECTURE.md`](ARCHITECTURE.md). Judgement calls and
deviations from the brief: [`DECISIONS.md`](DECISIONS.md).

## Done

| Item | What | Where |
| --- | --- | --- |
| 2.1 Readiness | `readiness` (0.6·recovery + 0.4·sleep, overreach penalty ≤ 10) and `state` on `DailyScore`, stored (migration 0002) | `scoring.Readiness`, `ReadinessState` |
| 2.2 Sleep detail | Typed `DayDetail.sleep` with stages (core → light), %, efficiency, quality + label, hypnogram; `GET /v1/sleep?range=week\|month` | `scoring/sleep.go`, migration 0003 |
| 2.3 Workout detail | `GET /v1/workouts/{id}`: HR curve (LTTB ≤ 600 points), zone minutes, TRIMP, strain, avg/max HR; zones + TRIMP stored at scoring time; `DayDetail.strain` | `scoring/workout.go`, migration 0004 |
| 2.4 Trends | `GET /v1/trends?metric&range` with avg/min/max and stored 30-day HRV/RHR baselines | `store.Trend`, migration 0005 |
| 2.5 Insights | Pure rule engine (11 rules with Train's and M4's, 30-day history), `GET /v1/insights`, 5-minute cache | `internal/insights` |
| 2.6 Observability | Prometheus `/metrics` on all services, scrape annotations, dashboard panel list and alerts | `internal/metrics`, [`OBSERVABILITY.md`](OBSERVABILITY.md) |
| 2.7 Hardening | `base/` + default and Vault overlays (VaultStaticSecret), default-deny NetworkPolicy, PDBs, CPU HPAs, nightly `pg_dump` CronJob with documented restore | `deploy/k8s`, [`OPERATIONS.md`](OPERATIONS.md) |
| 2.8 CI | vet, race tests, golangci-lint, manifest validation, smoke on Docker; multi-arch images to `ghcr.io/timkrebs/ryft/<service>` (short SHA, `latest` on main) | `.github/workflows/ci.yml`, `.golangci.yml` |
| 2.9 API docs | Full v1 contract with screen map, error semantics, Swift mirror, changelog | [`API.md`](API.md) |

**Infrastructure (after the brief):**

| Item | What | Where |
| --- | --- | --- |
| EKS cluster | VPC + EKS `ryft-prod-euc1` (Terraform modules; renamed from `ryft-prod`), applied by the user | [`infra/aws`](../infra/aws/README.md) |
| Domain `ryftapp.de` | Route 53 zone (own root, long-lived) with CAA for Let's Encrypt; GoDaddy stays the registrar | `infra/aws/dns` |
| Edge | AWS Load Balancer Controller, Traefik behind an NLB (HTTPS only), cert-manager with Let's Encrypt DNS-01 issuers, ExternalDNS, encrypted gp3 default StorageClass; all Terraform + Helm with Pod Identity | `infra/aws/kubernetes` |
| EKS overlay | `api.ryftapp.de` Ingress (Traefik + cert-manager), encrypted storage class on every volume, image tags to pin | `deploy/k8s/overlays/eks` |

**Go-live (M1b), all applied by the user on 2026-10-18:**

| Item | State |
| --- | --- |
| CI | `github.com/timkrebs/ryft-platform`, every job green (test with Postgres, lint, manifests, smoke with the NATS permission check, images). Public multi-arch images `ghcr.io/timkrebs/ryft/{ingest,scoring,api,migrate}:<short-sha>`. |
| Terraform | `infra/aws`, `infra/aws/dns`, `infra/aws/kubernetes` (Traefik, cert-manager with public DNS-01 checks, ExternalDNS, the operator) and `infra/vault` applied; state local on the user's machine. |
| DNS + TLS | `ryftapp.de` delegated from GoDaddy to Route 53. `api.ryftapp.de` → the NLB (ExternalDNS). Let's Encrypt certificates in `ryft-api` and `ryft-ingest`, valid until 2027-01-16. Port 80 closed. |
| Workloads | `make k8s-apply-eks` at `a602a20`: migrate ran nine migrations against Supabase and set the role passwords; api ×2, ingest ×2, scoring, nats ready with 0 restarts; every Secret synced from Vault with only its own keys. |
| Public check | `GET https://api.ryftapp.de/v1/me` and `/v1/sync` → `401` with `WWW-Authenticate`, HSTS, `Cache-Control: no-store`, `X-Request-ID`. |

**M1 — Supabase, multi-user, platform hardening** (`CLAUDE_FOLLOWUP_SUPABASE.md` plus the
agreed extras):

| Item | What | Where |
| --- | --- | --- |
| Auth | Supabase access tokens verified against the project JWKS (ES256/RS256, issuer, audience, role, not anonymous). `401` only for bad tokens, `503` + `Retry-After` when keys or the database are unreachable. apikey mode for local dev only, behind an explicit flag. | `internal/auth`, [`SUPABASE.md`](SUPABASE.md), D28–D30 |
| Multi-user | Users created on first request. Every query scoped to the token's user. Composite keys for devices and workouts. Per-user insight cache. Scoring timer over active users. Recompute gated per user. | migration 0007, `store.EnsureUser`, D31 |
| Supabase storage | Session pooler, small pools, transaction-level migration locks. RLS and revokes so the Data API exposes nothing. `local-postgres` overlay. Egress NetworkPolicy. | `internal/db`, `deploy/k8s`, D33–D34 |
| Logging | JSON logs with `request_id`, `user_id` and `route` on every line. The ID travels through the event into scoring. `LOG_LEVEL`. | `internal/logx`, [`OBSERVABILITY.md`](OBSERVABILITY.md#logs), D35 |
| Middleware | Request ID → access log + metrics → recover → security headers. On `/v1`: auth → per-user rate limit → user lookup → timeout. Generic `500` bodies, `413` body limits. | `internal/httpx`, `internal/ratelimit`, D36–D38 |
| Account deletion | `DELETE /v1/me`: all rows in one transaction plus a tombstone, then the Supabase auth user. Races with syncs and token reuse handled by locks. Reconciler for failed Supabase calls and restores. | `cmd/api/account.go`, `auth.Admin`, D39 |
| Source de-dup | One source per hour for heart rate, steps and active energy; one source per night for sleep. | `scoring/sources.go`, [`SCORING.md`](SCORING.md), D40 |
| Steps and kcal | Daily totals on `DailyScore` and `DayDetail.strain`, trend metrics. | migration 0008, D41 |
| CI | The test job runs the Postgres integration tests (migrations, deletion races) against a service container. | `.github/workflows/ci.yml` |

**Zero trust: one identity per workload** ([`ARCHITECTURE.md`](ARCHITECTURE.md#namespaces-and-trust)):

| Item | What | Where |
| --- | --- | --- |
| Database roles | `ryft_ingest` / `ryft_scoring` / `ryft_api` with only the privileges their queries use. Migrations run only in the migrate job, as the owner. Passwords are set as SCRAM verifiers. | migration 0009, `cmd/migrate`, D42 |
| NATS users | One user per service. Publishers may only publish `ryft.ingested`; scoring alone manages and consumes the stream. Each service has its own reply inbox. | `deploy/k8s/base/nats/nats.conf`, D43 |
| Namespaces | `ryft-{ingest,scoring,api,nats,migrate}`, each with default deny in and out, explicit flows, internet egress only to public addresses, Pod Security `restricted`, and no service-account tokens in pods. | `deploy/k8s`, D44 |
| Vault | HCP namespace paths fixed. Credentials are generated and written only where used. A migrate namespace, the api's Supabase key, and a mocked `terraform test` with golden operator manifests. | `infra/vault`, D45 |
| Operator | Vault Secrets Operator 1.6.0 as a pinned add-on, with no default identity. | `infra/aws/kubernetes`, D46 |

**After go-live** (account flows the app needs, operations gaps):

| Item | What | Where |
| --- | --- | --- |
| Apple token revocation | `POST /v1/me/apple-authorization` exchanges the Sign in with Apple code and keeps the refresh token encrypted in `apple_tokens` (api only). `DELETE /v1/me` revokes it before deleting the Supabase user; the reconciler retries. | `internal/apple`, `internal/sealed`, migration 0010, D48 |
| Production account checks | `make smoke-supabase` against the public host. With `SUPABASE_SECRET_KEY`, two throwaway users: row creation, isolation, deletion down to the Supabase sign-in. | `scripts/check_accounts.py` |
| Rescore tool | `scripts/rescore_all.sh` runs `scoring rescore` in the scoring pod with its own role. | D50 |
| Edge rate limit | 20 r/s, burst 50 per client IP (Traefik), client IPs preserved by the NLB | D53 |
| Logs | Fluent Bit → CloudWatch `/ryft/ryft-prod-euc1/containers`, KMS, 14 days, RYFT namespaces and Traefik only | `infra/aws/kubernetes/logs.tf`, D52 |
| Metrics | Grafana Cloud `k8s-monitoring` in namespace `monitoring`, token from Vault, alert rules; `/metrics` on port 9090 | `deploy/monitoring`, D51 |
| Terraform state | HCP Terraform, one workspace per root, execution Local; `make tf-migrate-hcp`; CI checks every root offline | D56 |
| Budget | AWS Budgets monthly alarm, 80 % actual and 100 % forecast | `infra/aws/budget.tf`, D55 |
| Apex | `ryftapp.de` page, `www` redirect, `apple-app-site-association` (webcredentials, applinks) | `deploy/k8s/base/web`, D55 |
| Supabase | Checklist for email accounts, redirects, templates, rate limits, Apple key; the move to Frankfurt (decided) | `SUPABASE.md`, D49 |

**Train (M3b, 2026-10-19)** — deployed (`3a0e9a1`, now `82506fa`):

| Item | What | Where |
| --- | --- | --- |
| Data | Exercise library (173 built-ins with English and German names, custom exercises), 4 plan templates with fixed ids, enrolments, sessions keyed by the app's ids, personal records; seeds loaded by the migrate job | migrations 0011–0012, `internal/db/seed`, `internal/store/train.go`, D62–D63 |
| Session saves | `PUT /v1/sessions/{id}` merges by id while in progress, answers `409` with the server copy on a newer set, replaces completed sessions whole | `internal/train`, D61 |
| Strength load | Logged sets become muscular load that joins the day's TRIMP before the compression; pinned on the seed library: 5×5 at 80 % → strain 11.0, 12 Olympic singles at 90 % → 8.0, 20-min bodyweight circuit → 5.1 | `internal/scoring/strength.go`, [`SCORING.md`](SCORING.md) → Strength, D59 |
| Workouts | The load lands on a matching HealthKit strength workout (≥ 50 % overlap) or one scoring creates (`ryft-session`), re-decided on every pass | `scoring/strength_day.go`, D60 |
| Records, progression | e1RM / max weight / max reps / max volume; `GET /v1/plans/today` targets from %-of-e1RM or linear progression with repeat and deload | `PersonalRecords`, `NextTargetKG` |
| API | `/v1/exercises`, `/v1/plans`, `/v1/plans/today`, `/v1/sessions` (+ `…/complete`), `DailyScore.strength_min` / `volume_kg`, `Workout.muscular_load` / `session_id`, `DayStrain.muscular_trimp` / `sessions`, insight `pr`, `ryft.session.completed` | `cmd/api/train.go`, [`API.md`](API.md) → Train |

**Sleep upgrades (M4, 2026-10-19)** — deployed (`b7e4a4a`, now `82506fa`)
([`SCORING.md`](SCORING.md) → Sleep, D65–D68):

| Item | What | Where |
| --- | --- | --- |
| Naps | The night is the block with the most sleep; other blocks ending from 10:00 and sleep ending 13:00–20:00 are naps (≥ 20 min each, ≤ 90 a day), stored as `nap_min` and credited to the next night's need | `SplitDay`, `NapMinutes`, migration 0013, D66 |
| Need by term | `sleep_need_{base,strain,debt,nap}_min` on the score; `DayDetail.sleep.need` | `SleepNeedParts`, `NeedOf` |
| Quality | Its own score, `0.6·performance + 0.2·efficiency + 0.2·restorative`; `restorative` on every night | `NightQuality`, D65 |
| Consistency | `100 − 0.5·(σ bed + σ wake)` over 7 nights (5 needed); `SleepRange.avg_consistency`, `bedtimes` | `SleepConsistency` |
| Learned baseline | Weekly (Sunday 04:00 local) median of green nights after easy days, 14/7 nights, 360–570; manual wins; the last 7 days rescored in-process; `scoring learn-baselines`; metric `ryft_sleep_base_learned_total` | `scoring/learn.go`, `SetLearnedBase`, D67 |
| Planner | `GET /v1/sleep/plan?wake=HH:MM`: bedtimes for 100/85/70 % of tonight's need, efficiency of the last 7 nights (floor 85) | `TonightPlan`, D68 |
| Settings | `User.sleep_base_source` / `_learned_min` / `_learned_at`; `PATCH /v1/me` `sleep_base_manual` | `store.UpdateUser` |
| Insights, trends | `sleep-consistency-low`, `nap-credit`, `baseline-learned`; trend metrics `sleep_quality`, `sleep_consistency`, `nap_min` | `internal/insights/rules.go` |

**Push and remote config (M7 core, 2026-10-19)** — deployed (`b7e4a4a`, now `82506fa`), APNs key
active; the weekly report stays in M7 (D69–D73):

| Item | What | Where |
| --- | --- | --- |
| Remote config | `GET /v1/config`: flags per user (kill switch, allow-list, stable SHA-256 buckets), the app's six keys seeded; `/v1/admin/flags` with its own `ADMIN_API_KEY` | migration 0014, `internal/flags`, `cmd/api/config.go`, D69 |
| Token registry | `POST` / `DELETE /v1/devices/{id}/push`; a token moves to the account that registers it last; erased with the account | migration 0015, `store/push.go`, D72 |
| Preferences | `PATCH /v1/me/notifications` (categories, quiet hours), `User.notifications` | `cmd/api/push.go` |
| notify | New service and namespace `ryft-notify`, role `ryft_notify`, NATS user `notify`: `MORNING_SCORE` + silent refresh on `ryft.scored`, `SESSION_SCORED` / `PR` on `ryft.session.scored`, `SYNC_STALE` after 20 h; claim per user/day/category, gone tokens pruned, quiet hours honoured; counts instead of sending without an APNs key | `cmd/notify`, `internal/notify`, `internal/apns`, D70–D73 |
| Events | scoring publishes `ryft.scored` (today's first recovery after a night with sleep) and `ryft.session.scored` (instead of notify reading `ryft.session.completed`, which the work queue gives to scoring alone) | `internal/events`, `nats.conf`, D71 |
| Payloads | `loc-key` + `loc-args`, en/de texts pinned in `notify.Catalog` and listed in `API.md`; arguments formatted per install language | `internal/notify/messages.go` |
| Ops | Manifests (Deployment, Service, NetworkPolicy with 443 for APNs), local-postgres and EKS overlays, CI image, `infra/vault` service entry with all-or-none APNs keys, metrics and two alerts | `deploy/k8s/base/notify`, `infra/vault`, `OBSERVABILITY.md` |

**Journal and behavior impact (M5, 2026-10-19)** — deployed (`82506fa`, 2026-10-20)
(`SCORING.md` → Journal, D74–D77):

| Item | What | Where |
| --- | --- | --- |
| Catalog | 25 built-in behaviors (en/de, fixed ids, 8 on by default) from the seeds, the user's choice, up to 50 custom behaviors with app ids | migration 0016, `internal/db/seed/behaviors.json`, `store/journal.go`, D77 |
| Journal | `GET`/`PUT /v1/journal/{day}`: a saved day replaces its entries; unsent behaviors are unknown, not "no"; 14 days back | `cmd/api/journal.go`, D74 |
| Impact | `GET /v1/behaviors/impact`: recovery and sleep performance (points) and HRV (%) the morning after, yes − no with a seeded 90 % bootstrap, unlocked at 5 + 5 days in 90; computed on request, no worker needed | `internal/behavior`, D75–D76 |
| Insight | `behavior-impact`: yesterday's behavior with the largest significant negative recovery effect | `internal/insights/rules.go` |

**Beta: friends on TestFlight (2026-10-20)** — committed, not deployed (D78–D86):

| Item | What | Where |
| --- | --- | --- |
| Consent | `consents` (every grant and withdrawal, per policy version), `POST`/`DELETE /v1/me/consent`, `User.consent`, `GET /v1/config` → `privacy_policy_version`/`_url`; sync answers `403 consent_required`, scoring computes nothing without consent; error `code` | migration 0017, `cmd/api/beta.go`, `cmd/ingest`, D78 |
| Invite gate | `BETA_GATE` (off until set in `infra/vault`), codes `RYFT-XXXX-XXXX` (~40 bits, one `422` for every failure, 5 tries an hour), `POST /v1/me/invite`, `/v1/admin/invites`; existing accounts keep access | migration 0017, `store/beta.go`, D79 |
| Model version | `2026.10.1` on every score and workout load, `GET /v1/model` with the embedded changelog (en/de), the bump rule; `rescore_all.sh` names the version | migration 0018, `scoring/version.go`, `SCORING.md`, D80 |
| Profile | `birth_year`, `max_hr_source` (default/estimated/manual), Tanaka from the birth year, `max_hr_manual`; insight `max-hr-observed` | migration 0019, `scoring/maxhr.go`, D81 |
| Sources | `user_sources` kept by ingest, `User.sources`, `source_bundle_id`, `app_version`/`os_version` on the device; insight `no-heart-rate-source`; golden fixtures for Watch + iPhone, Oura + iPhone, iPhone only, Garmin then Watch, expectations by hand | migration 0020, `scoring/testdata/sources`, D82 |
| Feedback and admin view | `POST /v1/feedback` (10 a day, DB-counted), `/v1/admin/feedback`, resolve, `/v1/admin/users` (no emails unless asked, logged), `scripts/beta_report.sh` | migration 0021, `cmd/api/feedback.go`, D83 |
| Operations | alerts as tested code (`deploy/grafana/alerts`, `make alerts-check`/`alerts-apply`), dead letters (`RYFT_DLQ`, `scoring dlq`), consumer backlog and database-up metrics, push environments tested against fake APNs hosts, `make smoke-prod`, runbooks: Deploy and roll back, Dead letters, Data protection, Supabase Pro and the restore drill | `internal/events`, `internal/db`, `OPERATIONS.md`, D84–D85 |
| Web and demo | the apex site is the web image built from `web/` in this repository (pages, legal texts, nginx config, AASA incl. `/invite/*`), pinned in `overlays/eks` like the services (D106, D107); `scripts/seed_demo.sh` for `review@ryftapp.de`, password generated by `infra/vault` | `web/`, `deploy/k8s/base/web`, `infra/vault/operator.tf`, D86, D106, D107 |

**The site in this repository** (2026-10-21, D107;
[`docs/work/2026-10-21-web-monorepo`](work/2026-10-21-web-monorepo/plan.md)):

| Item | What | Where |
| --- | --- | --- |
| Import | ryft-web's `main` (`d0503ea`, after its #8 go-live docs and #10 legal answers) with its history, PR references rewritten to `timkrebs/ryft-web#n` (`git filter-repo`); merged as a merge commit; `git log --follow -- web/<file>` reaches ryft-web's first commit | #7 (`ff719a8`) |
| Brand outside `web/` | the six internal mentions and D58's milestone name, by the user's choice; 0 hard findings outside `web/`, no commit message ever | #6 |
| `web/` in place | paths (`../../app`, the repo root), `make web-*` and `make hooks`; root `.githooks/` (brand on staged `web/` files and on every commit message, fail closed without Node); the strict privacy check against the ConfigMaps, `docker-compose.yml` and `web/content/legal/privacy-versions.json` (every consented version stays, en and de); build stage on `$BUILDPLATFORM`; `@lhci/cli` pinned | #8 |
| One CI | `changes` filters PRs; `ci-ok` (required) needs every check; `pr-title` (required) with the brand on the title and every commit, base branch's checker; ryft-web's checks (`web-checks`, `web-e2e`), gitleaks with a reviewed allowlist, actionlint; push runs only on `main` | #9, `.github/workflows/{ci,pr}.yml` |
| Web image and promote | `ghcr.io/timkrebs/ryft/web:<sha>` (Trivy before push and on the digest, provenance, SBOM, no cache, no `latest`, stops without `SUPPORT_EMAIL`); promote per group against the running pin, dated holds that fail closed, only main's tip, only the App's own `deploy/<sha>` PRs, no force-push | #9, `scripts/{deploy_targets,promote,pin_images,check_pr,check_privacy_versions}.sh` |
| Docs | D107; OPERATIONS → Deploy and roll back (groups, hold, re-runs), Apex site (release = merge, verification), A new policy text; ARCHITECTURE; README | #10 |
| Cutover (2026-10-22) | #6 → its deploy #11 (services `a9685b5`, identical binaries, 05:25 UTC); #8; #9 (`bfc51b8`); required checks switched to `ci-ok` + `pr-title`; the first `web-image` failed at the push (`write_package`: the package's Actions access was Read), passed after the role was set to Write and the failed jobs re-run; promote opened #18 (web only, `f437747` → `bfc51b8`), merged by itself at 06:29:24, the new site served from 06:32:14 | #6, #11, #8, #9, #18 |

**GitOps with Argo CD** (2026-10-21, committed, not applied):

| Item | What | Where |
| --- | --- | --- |
| Argo CD | chart 10.10.5 / v3.5.6 as a Terraform add-on: non-HA, public at `https://argocd.ryftapp.de` behind Traefik (rate limit, HSTS; D103), admin password from Terraform (no initial secret), Pod Security restricted, Application and VaultStaticSecret health, metrics scraped | `infra/aws/kubernetes/argocd.tf`, D94 |
| App of apps | projects `ryft-root` and `ryft` (the `default` project emptied); waves: `vault-<svc>`, `broker-nats` (NATS in `overlays/eks-nats`), `ryft-platform`; server-side apply and diff, retries | `deploy/argocd`, D95 |
| Hooks | migrate as PreSync (a failed migration fails the sync), the in-cluster smoke as PostSync (`/app smoke`, namespace `ryft-smoke`) | `internal/smoke`, `deploy/k8s/base/smoke`, D95 |
| Promotion | CI's promote job opens "deploy: images <sha>" (GitHub App) and auto-merges it; skips without a backend change; supersedes older deploy PRs; `manifests` checks every pinned image exists; no `:latest` | `scripts/promote.sh`, `.github/workflows/ci.yml`, D96 |
| Alerts | ArgoSyncFailed, ArgoAppDegraded, ArgoAppOutOfSync, ArgoMetricsMissing (25 rules) | `deploy/grafana/alerts` |

**NATS → RabbitMQ** (2026-10-21, committed on main without changing the broker; the
releases on branch `rabbitmq-cutover`, not applied):

| Item | What | Where |
| --- | --- | --- |
| Broker | RabbitMQ 4.3.6 (by digest), three nodes with anti-affinity and a PDB, 10 Gi gp3 each, TLS only (5671, management 15671), metrics on 15692; Cluster Operator 2.23.1 and Topology Operator 1.20.4 vendored byte for byte (`SHA256SUMS`); two Argo CD applications | `deploy/rabbitmq`, D97 |
| TLS | internal CA `ryft-internal` (cert-manager ClusterIssuer); the broker's certificate, a client certificate per service, mutual TLS | `infra/aws/kubernetes/charts/cluster-issuers`, D98 |
| Topology and rights | vhost `ryft`, exchanges, quorum queues, retry tiers (5 s, 30 s, 5 min) and notify's wait tiers, DLQs (14 days), policies, users without configure, topic permissions per routing key; one Go source, generated for the Topology Operator and compose; a permission test against a broker | `internal/events/amqpbus/topology.go`, `cmd/rabbitmq-topology`, D99 |
| Client | `events.Bus` with `natsbus` and `amqpbus`: confirms, `mandatory`, persistent, decode by type, retry tiers then DLQ, deferral, reconnect with backoff, readiness | `internal/events`, `internal/eventbus` |
| Idempotency | scoring and notify proven idempotent by tests instead of publish dedup | D100 |
| Outbox | every event a row in `outbox` (with the sync's days and the session's completion in one transaction), a relay per publisher, set-aside after 50 failures, a week kept | migration 0026, `internal/events/outbox`, D101 |
| Users | a password per service in `infra/vault`, written to the service and, as `amqp-user-<svc>`, to `ryft-rabbitmq` for the Topology Operator | `infra/vault`, `vault-namespaces/rabbitmq` (generated on apply) |
| Operating | `scripts/dlq.sh` (list, show, replay, purge), OPERATIONS → RabbitMQ; 8 alerts (33 rules), dashboard panels | `scripts/dlq.sh`, `deploy/grafana/alerts` |
| Compose | RabbitMQ with the generated definitions and dev certificates; publishes on RabbitMQ, consumes both | `docker-compose.yml`, `scripts/dev-amqp-certs.sh` |
| The cutover | four releases, configuration only until R4 (D102) | branch `rabbitmq-cutover` |

**Cost delta** (after R4, against NATS): about 3.3 GiB more memory requested (three
nodes at 1 GiB, two operators, minus NATS's 64 MiB), 640m more CPU, 30 GiB gp3 instead
of 2 GiB (about $2.40 a month). The memory is a sixth of the three t3.large's: check
the nodes' requests before R1 (`kubectl describe nodes | grep -A8 'Allocated
resources'`); if a node lacks 1 GiB, a RabbitMQ pod stays Pending (anti-affinity, one
per node) and R1 waits for a bigger node group.

**Accounts and sync** (2026-10-20, deployed):

| Item | What | Where |
| --- | --- | --- |
| Gate modes | `BETA_GATE` and `CONSENT_ENFORCE` each `off`, `warn`, `on`; `warn` serves the request with `X-RYFT-Gate: <code>` and counts `ryft_gate_refusals_total{mode="warn"}`; scoring follows `CONSENT_ENFORCE`; the refusal's `code` and the `gate` on every access line; a canceled request is logged as `499` at info | `config.Mode`, `httpx.SetCode`, D87 |
| Access grant and audit | `POST` / `DELETE /v1/admin/users/{id}/access`; every authorized admin call written to `admin_audit` (key fingerprint, route, target, status), `GET /v1/admin/audit`; admin routes take a full id or its first 8 digits | migration 0022, `cmd/api/audit.go`, D88 |
| One account in detail | `GET /v1/admin/users/{id}`: Supabase identities, masked emails (`?emails=1`), devices with app/iOS version, the last sync's summary and the last delivered push, 14 days of data, possible duplicates (same device id, 30 days); `dup` in the list and in `beta_report.sh` | migration 0023, `cmd/api/admin_users.go`, D89 |
| Operator scripts | `scripts/lib/admin.sh` finds the admin key (env, `infra/vault` output, Vault); `scripts/trace.sh <request id>` reads every pod of the four services (`--tail=-1`, 48 h; fixes a lost hyphen), then the account; `scripts/user.sh` (`--grant`, `--revoke`, `--resync-hint`, `--merge-into` with `--dry-run`); `beta_report.sh` and `make smoke-prod` (production only) find the key the same way; tested with a fake kubectl (`make test-scripts`, in CI) | `scripts/` |
| Merge | `POST /v1/admin/users/{from}/merge-into/{to}` with `dry_run`: one transaction, the kept account's rows win, per-table moved/dropped, consent and access copied if missing, rescored over the moved days, then `from` deleted like `DELETE /v1/me`; `409` if from has more history (`force`) | migration 0024, `store/merge.go`, D90 |
| Sync state | `X-RYFT-Device-ID` and `User.account_hint` (another account on this device); `GET /v1/me/sync`; `DailyScore.scoring_state`; `SyncResponse.accepted` / `ignored` / `scoring` (queued days, `expected_within_sec`); unknown types and out-of-window records ignored and counted instead of `422`; identical re-sends not counted | migration 0025 (`score_queue`), `cmd/api/syncstatus.go`, D91 |
| Sync to score | `ryft_sync_to_score_seconds` per queued day, alert `SyncToScoreSlow` (p95 > 2 min, 15 min) and a dashboard panel; a silent refresh push (`ryft.refresh`) after every sync's scored days, one per account every 5 minutes, collapsed per day; event handlers recover from a panic (`ryft_event_handler_panics_total`) | `cmd/scoring`, `internal/notify`, D92 |

Also: Podman support in the Makefile, JSON errors on unknown routes, `422` for inverted or
oversized ranges, UTC timestamps everywhere, `DailyScore.baseline_days`, end-to-end
contract checks in `make smoke`, Swift-side contract checks (`make check-swift`).

## How it was verified

| Check | Result |
| --- | --- |
| `make test` and `go test -race ./...` | pass (scoring, insights, auth incl. a fake JWKS, httpx middleware, rate limiter, logx, api cache) |
| Postgres integration tests (`RYFT_TEST_POSTGRES_URL`, throwaway `postgres:17`) | pass: migrations on plain Postgres and on a faked Supabase shape (RLS, revokes), account deletion including both races; each race test fails with its lock removed |
| `golangci-lint run` (v2.15, `.golangci.yml`) | 0 issues |
| `make down && make up && make smoke` on Podman, fresh volumes (six migrations at the time) | pass, incl. `scripts/check_api.py`: every endpoint, documented invariants, error paths, a workout across midnight |
| `make up && make smoke` on the existing volume after M1 (migrations 0007 and 0008 over real data) | pass, incl. two-user isolation on a shared device and workout UUID, request IDs and security headers, `413`s, account deletion (data gone, `401` afterwards on api and ingest, repeatable), overlapping heart-rate, sleep and step sources; no warnings or errors in the service logs |
| `make check-swift` | the Swift mirror in `API.md` decodes every endpoint, the Train ones included; the app's current models (`../app`) decode every endpoint they call |
| Train (2026-10-19) | `go test ./...` with the strength calibration pinned; Postgres integration tests for the Train store (merge, conflict, replace, ownership, enrolment through a whole plan, account deletion), the seeds (idempotent, fixed ids), the grants and the Supabase shape, and the scoring engine (session-only day, hand-over to a later HealthKit workout, deletion); NATS permission tests (api may publish `ryft.session.completed`, ingest may not); `make up && make smoke` over the existing volume with 0011–0012 applied, incl. the Train block of `check_api.py`; `golangci-lint` 0 issues; `make k8s-validate` |
| Sleep upgrades (M4, 2026-10-19) | `go test -race ./...` with the pins (consistency 85/55 and the midnight wrap, quality 91 and 82, restorative, nap counting and the split, the planner's API example, learning 14/7/clamp); Postgres integration tests: migration 0013 (manual marking, scoring's column grants, RLS on the Supabase shape), the baseline rules in the store (manual wins, back to learned or 460), `SetLearnedBase` as `ryft_scoring`, the engine over 21 nights (nap credit, need terms, consistency, quality) and learning (applied with the week rescored; manual kept); `make up && make smoke` over the existing volume with 0013 applied and 21 synthetic days with a 40-min nap, including the new `check_api.py` block (nap credit, quality formula, consistency, bedtimes, planner, `PATCH` manual and back, `422`s) and with the scoring commit alone; `make check-swift` (the mirror decodes `SleepPlan`); `golangci-lint` 0 issues; `make k8s-validate`. `scoring learn-baselines` ran in compose (all users `insufficient` on synthetic data, whose green mornings follow hard days). |
| Push and remote config (M7 core, 2026-10-19) | `go test -race ./...`: flag buckets pinned (and checked with Python's hashlib), evaluation and key rules; the APNs client against an `httptest` HTTP/2 server (headers, priorities, the provider token reused and renewed, `ExpiredProviderToken` retried once, gone tokens); the notify service with a fake store and APNs (once per day, per-device language, deferral to the end of quiet hours, muted, stale day, pruning, release and retry when APNs is down, nothing claimed without a key, session vs record, the stale check by day and hour); the catalog (every key has en and de with the same arguments). Postgres integration tests: migrations 0014–0015 on plain Postgres and the Supabase shape (grants, RLS policies), notify's queries **as `ryft_notify`** (tokens, claims, stale candidates, the morning score, the session notice with records), tokens moving between accounts, preferences, account deletion of the new tables, the engine's morning hook (today only, not on a rescore), the admin and config handlers. NATS permission tests against compose (scoring may publish the two new subjects, notify may only create its consumer, pull and ack). `make up && make smoke` with notify running: config shape, admin allow-list/rollout/kill switch/422s, token registry, preferences, and **the morning path end to end** (a new user's sync → scoring → `ryft.scored` → notify, seen in its `/metrics`); `make check-swift` (mirror decodes `RemoteConfig` and `User.notifications`, `NotificationPatch` encodes `null`; the app's current models still decode); `infra/vault` `terraform test` 15 runs; `golangci-lint` 0 issues; `make k8s-validate` (197 resources). Not verified: a real APNs send (no key yet). |
| Journal and behavior impact (M5, 2026-10-19) | `go test -race ./...`: the fixed ids pinned, the 14-day window, entry checks per kind, the effect (a clear one significant, none not, the same seed the same interval, relative HRV), the pairing with the morning after, locked counts and the order; `BenchmarkImpacts75x90` ~44 ms. Postgres integration tests: migration 0016 and the grants (vanilla and Supabase shape), the seeds (25 behaviors, fixed ids, idempotent), the catalog and choice, custom behaviors (update by id, someone else's id, the limit, unique slugs, archive), a journal day's round trip with count and time, replacement, account deletion of the journal; the api handlers (422s, the impact of a known effect: −15 recovery, −16.4 % HRV, significant; the `behavior-impact` card). `make up && make smoke`: catalog, choice, custom behaviors, 403/409/422 and **the roadmap's done-when**: alcohol before the five weakest-HRV mornings of the last 14 days → recovery −11.6 (90 % −18 to −4.7), HRV −7 %; `make check-swift` (the mirror decodes `[Behavior]`, `JournalDay`, `BehaviorImpacts`; the app's models still decode); `golangci-lint` 0 issues; `make k8s-validate`. |
| Beta (2026-10-20) | `go test -race ./...` with Postgres and NATS: migrations 0017–0021 on plain Postgres and the Supabase shape (grants, RLS; existing accounts keep beta access, changed `max_hr` becomes manual, sources backfilled), consent and invites in the store, Tanaka pinned for seven ages, the max-HR and no-heart-rate cards, the four source fixtures through the pure functions **and** stored and scored by the engine (iPhone only: calibrating, no readiness on day 8), feedback limits and the admin view, push routing through the real APNs client to two fake hosts (410 drops the token), the dead-letter permissions and a bad event dead-lettered by the running scoring; `golangci-lint` 0 issues. `make up && make smoke`: consent 403/200/withdrawn, invite codes, `/v1/model` and `model_version`, profile and `max-hr-observed` end to end, sources and `no-heart-rate-source`, feedback 422/429, admin feedback/resolve/users. `make check-swift`: the mirror and the app's models decode everything. `make alerts-check`: 20 rules, the unit tests and mimirtool pass. `make smoke-prod` and `scripts/seed_demo.py` against the compose stack (every demo screen has data). The site in its nginx image: each legal page, the fallback and the AASA (`application/json`, no redirect). `infra/vault` `terraform test` 17 runs. `make k8s-validate` (201 resources). Not verified: anything against production (not deployed), the Supabase sign-in paths of `seed_demo.sh` and `smoke-prod`, Grafana loading the rules. |
| GitOps (2026-10-21) | A kind cluster (Kubernetes 1.37, podman) with the chart and the Terraform values, a git daemon serving this tree, VSO's and Traefik's CRDs: Argo CD under Pod Security restricted, no initial admin secret; the projects and the root; wave 0 Progressing until each VaultStaticSecret reported `SecretSynced`, then the broker, then the platform; the NATS StatefulSet perpetually OutOfSync under client-side diff and Synced with server-side diff; ryft-platform refused for an empty destination namespace (fixed); the migrate PreSync stuck on a ConfigMap the sync had not made yet (fixed: optional); a failing migrate failed the sync with not one object created; a dry-run sync of the whole platform through the project's rules. `terraform test`: infra/aws/kubernetes 11 runs (chart pins, private, single-replica, bcrypt admin, health, restricted), infra/vault 19 (the smoke namespace gets only its sign-in; the tests no longer read a local tfvars). `make alerts-check`: 25 rules, the Argo CD ones tested (an auto-sync-paused app does not alert). `make test-scripts`: pin_images (typos refused), promote (a fake gh, a local remote: backend change → PR by the bot, docs and pin commits skipped, an older PR closed). `make smoke-hook` and `go run ./cmd/migrate smoke` against compose. `make k8s-validate`: 225 resources with the Argo CD manifests and `check_argocd.sh`. actionlint. Not verified: anything on ryft-prod-euc1, the PostSync smoke on a real stack, the promote job on GitHub (needs the App), adoption of the running objects. |
| RabbitMQ (2026-10-21) | A kind cluster (three workers, cert-manager, both operators, Argo CD): the broker from `deploy/rabbitmq` with all 63 topology objects Ready, three quorum voters per queue, listeners 5671 (TLS) and 15671 only plus 15692, no alarms, per-queue metrics; Argo CD adopted `rabbitmq-operators` and `broker-rabbitmq` Synced and Healthy with the new health checks; `amqpbus` over mutual TLS with the Vault-style user secrets: ingest publishes, a `scored` publish by ingest refused by its topic permission, scoring consumes; no client certificate or a wrong password refused; `scripts/dlq.sh` list, show, replay and purge. Locally: every Go test with `-race` against Postgres, NATS and a RabbitMQ (retry tiers, DLQ after six attempts, deferral, unroutable, a broker restart mid-consume with reconnect, the permission matrix including a loosened rule failing it, the outbox relay with two replicas and a rollback, the duplicate tests); `make smoke` on compose publishing on RabbitMQ and again on NATS (`EVENTS_BACKEND=nats`); the outbox drained; `terraform test` infra/vault 20 runs (RabbitMQ golden file), infra/aws/kubernetes 11; `make alerts-check` 33 rules; `make k8s-validate`. R4 on its branch: compose without NATS (every service `broker: amqp`), `make smoke`, `make smoke-hook`, `make test-scripts`, every Go test with `-race`, golangci-lint, `make k8s-validate` (local-postgres with one broker node), `make alerts-check` (32 rules), `terraform test` infra/vault 20 runs, actionlint. ingest's `/readyz` 200 with the compose broker stopped (`295f91f`). Not verified: anything on ryft-prod-euc1 (R1–R4), `make k8s-apply-local` on a cluster, a node drain with the PDB, a certificate renewal, the Grafana Cloud scrape of 15692. |
| Public admin UIs (2026-10-21, D103) | In the kind cluster with the Traefik chart 42.0.0 (v3.7.14) and the broker: `rabbitmq.ryftapp.de` through Traefik → the management UI 200, the API 200 with the default user and 401 with a wrong password, HSTS; Traefik verifies the node's certificate (a wrong `serverName` → 500 with an x509 error); without the Traefik rule in the broker's network policy the request times out (504). Argo CD with the chart's Ingress and `server.insecure`: the UI 200, `POST /api/v1/session` 200 with the password and 401 without, HSTS. `terraform test` infra/aws/kubernetes 11 runs; `make k8s-validate` (the ServersTransport, Middlewares and Ingress against Traefik's schemas). Not verified: the Let's Encrypt certificates and DNS records on ryft-prod-euc1, the `argocd` CLI with `--grpc-web` (not installed here). |
| Apex site from ryft-web (2026-10-21, D106) | Before: `ghcr.io/timkrebs/ryft/web:f437747` pulled anonymously (linux/amd64 and arm64), run locally as in the pod (uid 101, read-only root, tmpfs `/tmp` and `/var/cache/nginx`): every URL 200, the AASA production's plus `/invite/*`. PR #3 merged 18:32:16 UTC (`2cc55ef`), the new image served from 18:36:43: `/`, `/privacy`, `/terms`, `/support`, `/impressum`, `/de/privacy`, `/de/impressum`, `/privacy/2026-10-20`, `/day/2026-10-19`, `/auth/callback`, `/invite/TEST` 200 `text/html`; the AASA 200 `application/json`, no redirect; the CSP with the script hashes on 10 of 10 requests (both pods); `www` → `https://ryftapp.de/`; `/privacy/` → 301 `/privacy`. `promote_test.sh` fails with the old filter and passes with the new. Not checked: the pods' image through kubectl (the session's AWS login had expired). |
| The site from this repository (2026-10-22, D107) | Before: PR #9's own run (every job, the workflow changed): web-checks, web-e2e (the nginx image, 150 Playwright/axe pages, links, Lighthouse), secrets, manifests, smoke, test, lint, terraform ×4, `ci-ok`, `pr-title` green; `ci-ok` red on the first run (actionlint, `GITHUB_BASE_REF` in a test), as designed. After: `web-image` built amd64, checked `/beta` for the address, Trivy, pushed both architectures (provenance, SBOM), Trivy on the digest; `ghcr.io/timkrebs/ryft/web:bfc51b8` exists. #18 changed only web's `newTag` (`f437747` → `bfc51b8`), authored by `app/ryft-deploy`, auto-merged after `changes`, `manifests`, `secrets`, `pr-title`, `ci-ok` (everything else skipped). Live from 06:32:14 UTC (2:50 after the merge): `/`, `/privacy`, `/terms`, `/support`, `/impressum`, `/de/privacy`, `/de/impressum`, `/privacy/2026-10-20`, `/day/2026-10-19`, `/auth/callback`, `/invite/TEST`, `/beta`, `/de/beta`, `/science` 200 `text/html`; the AASA 200 `application/json`, no redirect, appID `M77SKHKG63.dev.timkrebs.ryft`, the ten paths, `webcredentials`, and Apple's CDN copy identical; `/beta` links `mailto:support@ryftapp.de` with no TODO marker; the CSP's hashes match every inline script on `/`, `/privacy`, `/beta`, no external script; `www` → apex, `/privacy/` → 301 `/privacy`, HSTS; the 2026-10-21 legal texts (#10 of ryft-web) served; `api.ryftapp.de/v1/me` 401. Not checked: the pods' image and Argo CD's state through kubectl (the session's AWS login had expired) — the served text proves the new image; check `kubectl -n argocd get applications ryft-platform` once. |
| Web follow-ups (2026-10-22/23, D108–D111, D115) | Every PR reviewed by code and security (SRE where runtime changed); findings fixed and re-verified before merge (`docs/work/2026-10-22-followups/review.md`). Deploys watched read-only: web `332c252` (#19 → #23, the first build with #17's actions); F1 (#22: four ConfigMaps renamed, migrate and smoke Succeeded, then #25 — the first promote in environment `deploy` with the rotated key); the drill (#26 rollback with a hold, promote held, the watch OK, #27 release, #28 deploy; Argo pickup 2–6 min); #31 (web image `a4ad9f7`, #36). |
| The calibrating report (2026-10-22/23, D112–D114) | Root cause from admin aggregates (the reporter has 0 HRV nights) and your SQL (0 stale rows); #29 and #30 reviewed by code, scoring and database, #35 by security and code; migration 0027 applied (`migration applied 0027_calibrating_reason.sql`); `scripts/rescore_all.sh 30` (2 users, 0 failed, model 2026.10.1); a fresh production `scored day` line carries only `day, inputs_changed, model_version, request_id, user_id` and the standard fields (`docs/work/2026-10-22-calibrating/review.md`). |
| Accounts and sync (2026-10-20) | `go test -race ./...` with Postgres and NATS: migrations 0022–0025 on plain Postgres and the Supabase shape (grants, RLS), gate modes in api, ingest and scoring (the warn header and metric, scoring's consent mode), the audit middleware (written after the call, short ids, `409` when ambiguous), the admin detail with a fake Supabase admin API (identities, relay emails, masking), the merge over every table (dry run equal to the real run, conflicts kept, children of dropped parents not copied, a second run `404`, `from` deletable afterwards), the score queue (queued, marked, pruned, the p95), the sync screen (unknown types, window, duplicates), today's state, the refresh throttle shared with the morning push, a panicking handler retried, the NATS permissions for `ryft.refresh` and its dead letters; `golangci-lint` 0 issues. `make up && make smoke` over the existing volume with 0022–0025 applied: the admin detail and short ids, two accounts on one phone merged (plan first, `404` the second time), `account_hint` (and none without the device header or for the account itself), `GET /v1/me/sync` with nothing pending once scored, `scoring_state`, the `SyncResponse` counts (a re-send and an unknown type ignored), and the refresh path (sync → scoring → `ryft.refresh` → notify, counted without a key). `scripts/smoke_prod.sh` against compose with and without the admin key; its key lookup with a fake `terraform` (found for production, never for another URL). `make test-scripts` (trace with a fake kubectl). `make check-swift`: the mirror decodes `SyncStatus` and the new fields; the app's current models still decode everything. `make alerts-check`: 21 rules with `SyncToScoreSlow`'s unit test. `infra/vault` `terraform test` 18 runs. `make k8s-validate`. `scripts/trace.sh` against production found the incident's line. A rollback past the refresh subject on a throwaway NATS 2.11. Not verified: anything else against production (not deployed), a merge on real accounts, a refresh push reaching a device. |
| `make k8s-validate` | base, Vault, EKS and local-postgres overlays render and validate with kubeconform (103 resources; VSO CRD schemas included) |
| Terraform roots `infra/aws`, `dns`, `kubernetes` | `fmt`, `validate`, `terraform test` (mocked: 8 + 2 + 7 runs), tflint clean, trivy: no findings in `dns`/`kubernetes` |
| `infra/vault` | `validate`, tflint clean, `terraform test` (mocked, 6 runs): keys per namespace, owner password only in migrate, bcrypt-only NATS, role bindings, policy, Pod Security, golden operator manifests (fails with the old namespace path) |
| Zero-trust layout locally | compose runs migrate, then the services as `ryft_*` roles and their NATS users. `make smoke` passes on the existing volume, with migration 0009 applied over the data. The grant matrix, SCRAM login and RLS on the Supabase shape are tested against Postgres 17. The NATS permission tests pass against compose and with bcrypt hashes, and fail against a permissive config. |
| `make k8s-validate` (per-namespace) | each `services/<svc>`, `overlays/eks`, `overlays/local-postgres` and the golden operator manifests (140 resources, VSO 1.6 CRD schemas). Every rendered pod checked for the restricted profile; each namespace's Secret holds only its own keys. NATS ran as uid 1000 on a read-only root, and Postgres as uid 70 with all capabilities dropped (Podman). |
| Helm charts of the edge | each pinned chart rendered with the exact values (`helm template`), schemas accepted, output validated with kubeconform incl. cert-manager CRD schemas; Traefik Service exposes only 443 |
| Backup script + restore procedure | run against the compose Postgres: dump as uid 70 with read-only root FS, `pg_restore --clean --if-exists` into empty and populated databases, identical row counts |
| Dockerfile cross-build | `linux/amd64` image built on arm64 and started |
| `actionlint .github/workflows/ci.yml` | pass |
| Independent review of the full diff | nothing that crashes, loses data or breaks a deploy; four wrong-result bugs found and fixed (`DECISIONS.md` D22), one with a new end-to-end check |

## Incidents

**2026-10-20, 20:43–21:00 UTC: every service lost the database during the deploy of `4dd5aa1`.**

- **What:** from 20:43:49 every new connection through Supabase's pooler answered
  `ECIRCUITBREAKER: too many authentication failures, new connections are temporarily
  blocked`, from 20:48 also `Failed to connect to database: econnrefused` (the pooler
  could not reach Postgres). Every pod of api, ingest, scoring and notify, old and new,
  went unready; each migrate retry failed at its first ping; Supabase listed network
  bans. Migrations 0022–0025 had been applied by the first migrate run at 20:43.
- **Why:** migrate sets the four role passwords on every run, each time as a verifier
  with a new random salt, even when the password is the same. Supavisor caches each
  role's verifier; after a change, every connection it checks with the stale salt fails
  once (`28P01`, "password was recently changed"), and it blocks a project and client
  address for 120 s after 10 such failures in 150 s. All pods leave through the
  cluster's NAT addresses (`63.188.104.25`, `3.120.13.212`), and the deploy restarted
  all of them right after migrate. Each re-run of `make k8s-apply-eks` ran migrate
  again; the repeated failed logins got addresses banned on the database host. Every
  earlier deploy rewrote the verifiers the same way; why this one tipped over (more pods
  reconnecting at once, the repeated runs) is not known.
- **Recovery:** the migrate run at 20:59:56 completed (nothing pending), and the pods
  connected from about 21:00; no data was lost (a sync during the outage failed and is
  re-sent by the app).
- **Fix:** migrate logs in as each role first and writes its password only when that
  fails (D93). Runbook: on `ECIRCUITBREAKER`, do not re-run the deploy; wait two
  minutes, and check Supabase's network bans (`OPERATIONS.md` → Deploy and roll back).

**2026-10-20, 10:37–19:04 UTC: the operator's own account could not sync.**

- **What:** after the Beta deploy (`21eb0fa`), every `POST /v1/sync` of account `53d437eb`
  (Sign in with Apple, with data and beta access) answered `403 consent_required`, 41 times.
  The app has no consent screen yet and shows any `403` as "RYFT didn't accept your
  sign-in", on every screen. Reads (days, insights, plans) answered `200` throughout.
- **Why:** consent is enforced on every sync (D78) and nothing was backfilled, by design.
  The deploy step "consent first for your own account" could not be done: the app has no
  screen for it, and an Apple account has no password to get a token with by hand.
- **Not the cause:** the beta gate, which is off (the api logs `"gate":false`;
  `ryft-secrets` has no `BETA_GATE` key), and a second account. Since the deploy, the logs
  show no account but this one and the smoke user's. The email and Apple sign-ins reached
  the api as the same user.
- **Why it took long to see:**
  - The request id the app showed had lost a hyphen: `01a15a69-71427a8b-…` instead of
    `01a15a69-7142-7a8b-…`. It is ours, a UUIDv7 from 17:43:42 UTC.
  - `kubectl logs -l` reads 10 lines per pod unless `--tail=-1`, and `logs deploy/api`
    reads one pod of two.
  - The request was ingest's, not the api's.
  - The access line did not say which gate refused.
  - `beta_report.sh` needed the admin key typed in.
- **Fix:**
  - `CONSENT_ENFORCE=warn` until the app ships its consent screen (D87).
  - The refusal's `code` on the access line.
  - `scripts/trace.sh` and the admin-key lookup.
  - For the app: show request ids exactly as received, and tell `consent_required` and
    `invite_required` apart from sign-in failures.

## Not done / not verified

**Web in this repository: your steps, in this order** (D107; PR bodies have the details).
All done (2026-10-22): the docs PR (#10); ryft-web archived with the README pointer (its
ruleset was off for that one push, then back on unchanged) and the description "Moved to
timkrebs/ryft-platform (web/)"; its package access removed, `make hooks`, `make
web-install` and `../web.old` (yours); the `deploy` environment of step 10 is #21 (D110),
the two other settings stay optional.

1. **Freeze ryft-web** (any time; before its README commit): `gh workflow disable ci -R timkrebs/ryft-web`
2. **Merge #6** and let its "deploy: images" PR merge (the services, identical binaries).
3. **Merge #8** (`web: paths, hooks…`); #9 then targets `main`.
4. **Before merging #9** (the package's Actions access must be **Write**, not Read):
   ```bash
   gh variable set SUPPORT_EMAIL -R timkrebs/ryft-platform --body support@ryftapp.de
   # https://github.com/users/timkrebs/packages/container/ryft%2Fweb/settings
   #   → Manage Actions access → Add repository → timkrebs/ryft-platform → Write
   ```
5. **Required checks**, once #9 shows `ci-ok` and `pr-title` green, right before merging it:
   ```bash
   gh api -X PATCH repos/timkrebs/ryft-platform/branches/main/protection/required_status_checks --input - <<'JSON'
   {"strict": false, "checks": [{"context": "ci-ok", "app_id": 15368}, {"context": "pr-title", "app_id": 15368}]}
   JSON
   ```
6. **Merge #9.** web-image → "deploy: images <sha>" (web only) → Argo CD; 25–40 minutes. Then
   the checks in OPERATIONS → Apex site → Verify. Rollback: `newTag: "f437747"` + `# hold: web
   <date> <reason>`.
7. **Merge the docs PR.**
8. **Retire ryft-web** (after the live check):
   ```bash
   git clone https://github.com/timkrebs/ryft-web.git /tmp/ryft-web-last && cd /tmp/ryft-web-last
   printf '# Moved\n\nThe site now lives in [timkrebs/ryft-platform/web](https://github.com/timkrebs/ryft-platform/tree/main/web).\n' > README.md
   git commit -am "docs: moved to timkrebs/ryft-platform/web" && git push      # the workflow is disabled: no image
   gh repo archive timkrebs/ryft-web --yes
   # package settings: remove timkrebs/ryft-web from Manage Actions access; never delete package versions
   ```
9. **Locally:** `make hooks` and `make web-install` in `plattform/`; once all is verified,
   `mv ../web ../web.old` (delete later). The site lives at `plattform/web/` from now on.
10. **Optional hardening:** squash commits take the PR title (`gh api -X PATCH
    repos/timkrebs/ryft-platform -f squash_merge_commit_title=PR_TITLE -f
    squash_merge_commit_message=COMMIT_MESSAGES`); approval for outside contributors' workflow
    runs (`gh api -X PUT repos/timkrebs/ryft-platform/actions/permissions/fork-pr-contributor-approval
    -f approval_policy=all_external_contributors`); the App key in a `deploy` environment limited
    to `main` (follow-up below).

**Web follow-ups and the calibrating report: done** (every PR's reviews, fixes and deploy
record: `docs/work/2026-10-22-followups/review.md`, `docs/work/2026-10-22-calibrating/review.md`)

- **F1** → #22 (D111): hashed ConfigMaps roll their pods; the version in force on EKS is a
  pin promote moves with web's; `check_policy_in_force.sh`; api/ingest refuse to start
  without a valid version; web PDB.
- **External probe** → #20 (D109); **stale hold / stuck deploy PR / targeted without a PR**
  → the daily deploy watch, #21 (D110); **`hold.sh`**, **the deploy environment** → #21;
  **the rollback drill** → 2026-10-22, `drill.md` (it found that a hold turned `smoke` red;
  fixed in #31). A selective `argocd app sync --resource` skips the hooks (Argo CD v3.5.6).
- **Dependabot majors** → #19 (D108): ESLint 10 in, nginx stays on the stable branch, Node
  26 waits for its LTS (2026-10-28), TypeScript 7 for typescript-eslint; #17 (the actions
  group) merged.
- **Hidden line breaks and look-alike YAML** → #31, #38 (D115).
- **`../ROADMAP.md`**: the 10 lines with the denylisted brand reworded (milestone names
  kept, as you chose).
- **The calibrating report** → #29 (D112), #30 (D113), #35 (D114); production had no day
  scored before its history (your query, 0 rows).

**Open (2026-10-23)**

Yours:
- **Synthetic Monitoring, once** (OPERATIONS → Monitoring): initialise it in Grafana Cloud,
  then `make synthetics-apply` and `make alerts-apply`; until then the checks and alerts of
  #20 are not live.
- **The privacy text in force (2026-10-20)** is still `status: draft`, with `TODO(user)` for
  the controller's name and address and the supervisory authority. Its sentence that
  scoring logs the daily scores over-describes since #35: fold the correction into the next
  policy version after **2026-11-06** (the old log lines have left CloudWatch by then).
- **`infra/vault`**: a validation that refuses `config.env` keys (`PRIVACY_POLICY_VERSION`,
  `CONSENT_ENFORCE`, `BETA_GATE`, …) in `service_secrets` — the secret comes after the
  ConfigMap in `envFrom` and would override the version in force.
- **Argo CD** reports `OrphanedResourceWarning: 1 orphaned resource` on ryft-platform:
  `argocd app resources ryft-platform --orphaned`.
- Optional: squash commits take the PR title; approval for outside contributors' runs; the
  `bytes` job as a required check (`ci-ok` already needs it). `PRIVACY_POLICY_URL`
  versioned (`/privacy/<version>`): your call.

Technical:
- **Node 26 for web** on or after 2026-10-28: Docker tag and digest, `.nvmrc`, `engines`,
  `@types/node` in one PR (D108).
- **Supply chain** (#17's review): pin `tonistiigi/binfmt` and `moby/buildkit` by digest;
  Trivy before push for the service images; provenance `mode=max` carries the push event
  payload. Dependabot for the `docker run` images and the services' Go modules and base
  images; the services' `images` still use the GHA cache.
- **scoring** runs two consumers for 10–30 s during a rollout surge: confirm its timer is
  safe, or `maxSurge: 0` for scoring.
- **`dlq.sh` replay** keeps no redelivery marker, so a replayed letter doesn't finish a
  half-done re-score: until fixed, follow a replay with `POST /v1/recompute`.
- **The migrator** sets no `lock_timeout` (only 0027 does): one for every migration; name
  the Postgres major version in `SUPABASE.md`.
- **ingest/api**: a NUL byte in a text field gives 500 instead of 422.
- **Log tests**: pin the allowed keys' value types (a health value under `applied` would
  pass); GoTrue errors via `resp.StatusCode`.
- **The deploy watch** alerts by GitHub mail only (no dead-man signal): look at its runs
  weekly.
- The never-scored placeholder of `GET /v1/days/{day}` says `calibrating: false` with
  `state: "calibrating"` (pre-existing).

**For the app session:** `docs/work/2026-10-22-calibrating/app-answers.md` answers
`../app/docs/API_REQUESTS.md` (AASA done — add `applinks:ryftapp.de`; `calibrating_reason`
live; the reporter has no HRV source; past days can change after the fact with the same
`model_version`). The rest of that file is the next brief. A legal text or AASA change is a
PR here (`web/`); the app's hits of the denylisted brand are in `web/docs/BRAND_CHECK.md`.

**GitOps: your steps, in this order** (OPERATIONS.md → Deploy and roll back → Cutover):

1. **Push main.** CI builds images for the new HEAD, the first with the `smoke`
   subcommand; the promote job skips itself until step 2 exists.
2. **GitHub App** `ryft-deploy`: GitHub → Settings → Developer settings → GitHub Apps → New;
   no webhook; repository permissions Contents and Pull requests read & write (Metadata
   read); install it on `ryft-platform` only; generate a private key. Then:

   ```bash
   gh variable set RYFT_DEPLOY_APP_CLIENT_ID -R timkrebs/ryft-platform --body <the App's client ID>
   gh secret set RYFT_DEPLOY_APP_KEY -R timkrebs/ryft-platform < ~/Downloads/ryft-deploy.*.private-key.pem
   gh api -X PATCH repos/timkrebs/ryft-platform -F allow_auto_merge=true -F delete_branch_on_merge=true
   gh api -X PUT repos/timkrebs/ryft-platform/branches/main/protection --input - <<'JSON'
   {"required_status_checks": {"strict": false, "contexts": ["test", "lint", "manifests", "smoke"]},
    "enforce_admins": false, "required_pull_request_reviews": null, "restrictions": null}
   JSON
   ```

   Re-run CI on main (`gh run rerun <id>`, or push once more): the first "deploy: images"
   PR appears and merges itself; it pins the five images the smoke hook needs (step 6).
3. **`infra/vault`:** done (2026-10-21, with the smoke user and the RabbitMQ users); the
   generated `vault-namespaces/smoke` and `rabbitmq` are committed (`f8ccddd`): push
   them, the application `vault-smoke` (wave 0) needs the first in git.
4. **`infra/aws/kubernetes`:** done (2026-10-21: Argo CD, the internal CA, the RabbitMQ
   health checks). **Again for the public UI** (D103; the Ingress, two Traefik
   middlewares, argocd-server behind Traefik):
   `(cd infra/aws/kubernetes && terraform plan -out=tfplan && terraform apply tfplan)`.
   Then `kubectl -n argocd get ingress,certificate` (READY True after a minute or two),
   `dig +short argocd.ryftapp.de` (the NLB) and `https://argocd.ryftapp.de`, user
   `admin`, password from `terraform -chdir=infra/aws/kubernetes output -raw
   argocd_admin_password`.
5. **Projects and root:** done (2026-10-21). Re-apply `project.yaml` once more for the
   management certificate's orphan ignore: `kubectl apply -f deploy/argocd/project.yaml`.
6. **Adoption of ryft-platform:** done 2026-10-21. The first sync (10:59 UTC) ran
   `057e4ce` before the promote PR had pinned it, so its smoke ran the old migrate image
   (no `smoke` subcommand: "DATABASE_URL is required"); the sync of `9332aad` (11:10 UTC,
   images `057e4ce`) applied 0026 and rolled out all four; its smoke failed for want of
   `service_secrets.smoke` in `infra/vault` (added, applied); `smoke passed` at 11:44
   UTC. Auto-sync on since `d733f68` (pushed 11:53 UTC, applied by the root at 11:54).
7. **Prove the loop:** a merge that changes nothing: done (`d733f68`: ryft-platform
   stayed Synced, no sync, no hooks). A real promote PR: done (PR #2: opened 13:17:53,
   merged by itself 13:21:03 after CI, Argo CD synced 13:22, healthy with the smoke
   13:29 including one retry; about 4 minutes without the HPA failure). The rollback
   drill ran on 2026-10-22 with a hold (`docs/work/2026-10-22-followups/drill.md`); the
   D105 apply is still open.
8. **Argo CD's HPA health check** (D105): `(cd infra/aws/kubernetes && terraform plan
   -out=tfplan && terraform apply tfplan)`; the plan changes only `helm_release.argocd`
   (argocd-cm), no restart of the services.

**RabbitMQ: your steps, one release at a time** (D102; OPERATIONS → RabbitMQ → The
cutover). Before: the GitOps steps above, done. The releases are the four commits of
branch `rabbitmq-cutover` (R1 `b07e7be`, R2 `da67f71`, R3 `9fa1741`, R4 `1d67b9e`);
cherry-pick each onto main when the one before is verified.

0. **Main as it is:** deployed 2026-10-21 11:10 UTC (`057e4ce`, migration 0026, the
   outbox, still on NATS). Check `ryft_outbox_pending` stays 0 and `make smoke-prod`.
1. **R1, the broker:** done 2026-10-21 (`9afa16f`, pushed 12:11 UTC; three nodes ready
   12:17, `broker-rabbitmq` Healthy 12:23). All 63 topology objects Ready, 13 quorum
   queues with three members each, no alarms, listeners 5671 and 15671 (TLS) only,
   `https://rabbitmq.ryftapp.de` 200 with a valid certificate, 401 without a login, no
   orphan warnings. A node idles at 12m CPU and 130 MiB. The operator's
   `MemoryRequestAndLimitDifferent` warning: limit = request (`4e746af`, D104). The
   original steps:

   ```bash
   kubectl describe nodes | grep -A8 'Allocated resources'      # 1 GiB free on each of three nodes
   kubectl get clusterissuer ryft-internal                        # READY True (infra/aws/kubernetes, done)
   kubectl -n ryft-rabbitmq get secret                            # amqp-user-ingest/api/scoring/notify (infra/vault, done)
   git cherry-pick b07e7be && git push                            # the applications
   kubectl -n argocd get applications -w                          # vault-rabbitmq, rabbitmq-operators, broker-rabbitmq Healthy
   ```

   Then the checks in OPERATIONS → RabbitMQ → Healthy is, and the management UI at
   `https://rabbitmq.ryftapp.de` (comes with the broker, D103), user and password from
   `kubectl -n ryft-rabbitmq get secret rabbitmq-default-user -o go-template='{{.data.username | base64decode}} {{.data.password | base64decode}}{{"\n"}}'`.
2. **R2, dual consume:** done 2026-10-21 (`a2298ff`; synced 12:39 UTC, smoke passed
   12:40). The broker shows `scoring` and `notify` connected over TLS 1.3 with their own
   client certificates (CN=scoring, CN=notify), one consumer on each queue, all 13
   queues empty; the services log `publish: nats, consume: [nats, amqp]`. The memory
   change (`4e746af`) rolled the nodes one at a time before it, `NoWarnings=True`. The
   original steps: `git cherry-pick da67f71 && git push`; Argo CD syncs ryft-platform
   (scoring and notify restart). Check `kubectl -n ryft-scoring logs deploy/scoring |
   grep -i amqp`, `ryft_amqp_connected` 1 for both, `make smoke-prod`.
3. **R3, publish on RabbitMQ:** done 2026-10-21 (`7e667ed`; synced and smoke passed
   13:01:55 UTC, scored within a second over RabbitMQ). Connections `api` ×2, `ingest` ×2,
   `scoring`, `notify`, each TLS 1.3 with its own certificate; every send confirmed
   (`result="ok"`), the outboxes empty, nothing in `dlq.*`, no redeliveries; a tester's
   backfill at 13:07 scored through RabbitMQ. NATS: both consumers 0 pending, `RYFT_DLQ`
   empty (`/app dlq`: 0). **Next:** a day of normal syncs, then R4. The original steps: `git cherry-pick 9fa1741 && git push`. Check `make
   smoke-prod`, the queue depths and the outbox at 0; wait a day of normal syncs
   (`ryft_sync_to_score_seconds` p95 as before, `scripts/dlq.sh list` empty).
4. **R4, NATS goes:** done 2026-10-21 (`0fd4256`; its images pinned by PR #2,
   `3cdbedc`, merged 13:21 UTC; healthy and smoke passed 13:29). The first attempt failed
   on the api HPA's "no metrics yet" right after the rollout (Argo CD's own HPA check
   calls it Degraded), the retry passed: fixed by D105. `infra/vault` without NATS
   applied (namespace `ryft-nats` gone), `scripts/rescore_all.sh 3` and `make
   smoke-prod` passed. The original steps: first `kubectl -n ryft-scoring exec deploy/scoring -- /app dlq`
   (redrive or delete each letter) and `ryft_nats_consumer_pending` 0 for both. Then
   `git cherry-pick 1d67b9e && git push` (CI builds images without NATS; the promote PR
   deploys them). Then `(cd infra/vault && terraform plan -out=tfplan && terraform apply
   tfplan)` (deletes namespace `ryft-nats` with its volume), commit the removed
   `vault-namespaces/nats`, and `scripts/rescore_all.sh 3`.

Each release: note its SHA and time here. Rollback: `git revert` of that release's commit
(R2, R3: back to NATS at any time; after R4 see D102).

**Accounts and sync: your steps, in this order:**

0. **The migrate fix** (D93): done, `1015bc7` (2026-10-21 05:33 UTC), "role password
   unchanged" ×4. In Supabase → Database → Settings → **Network Bans**, remove any ban on
   `63.188.104.25` or `3.120.13.212` that is still listed.
1. **Deploy:** done (`4dd5aa1`, 2026-10-20; commit your `newTag` edit). migrate applied
   0022–0025; NATS restarted with the new permissions; scoring widened `RYFT` to five
   subjects and notify's consumer took `ryft.refresh`.
2. **The gates in `warn`** until the app's invite and consent screens are on TestFlight
   (the first incident): `infra/vault/terraform.tfvars` has `shared_secrets = {
   BETA_GATE = "warn", CONSENT_ENFORCE = "warn" }`, but the cluster's `ryft-secrets`
   have neither key yet (the api logs `gate off, consent on`). Apply it; safe at any
   time now, since the operator only restarts the services and only migrate rewrote
   passwords (never with `21eb0fa`, which does not start with `warn`):

   ```bash
   (cd infra/vault && terraform plan -out=tfplan && terraform apply tfplan)
   kubectl -n vault-secrets-operator-system rollout restart deploy vault-secrets-operator-controller-manager
   for s in api ingest scoring; do kubectl -n ryft-$s logs -l app=$s --tail=-1 --since=10m | grep '"msg":"beta"'; done
   ```

3. **Check:** `make smoke-prod` (finds the admin key itself; prints `gates: invite …,
   consent warn`), `scripts/beta_report.sh` (no key to type), `scripts/user.sh 53d437eb`:
   both sign-ins should be identities of this one account, and "possible duplicates"
   empty. Your next sync is accepted (`X-RYFT-Gate: consent_required` on it) and scored;
   `scripts/rescore_all.sh` once, for the days it missed.
4. **A duplicate**, only if `user.sh` or `dup` shows one: `scripts/user.sh <from>
   --merge-into <to> --dry-run`, read the plan, then without `--dry-run` (it asks for
   `MERGE`). Not done here: the logs since the deploy show no second account.
5. **Grafana:** `make alerts-apply` again (21 rules, `SyncToScoreSlow` new) and the
   "Sync to score p50 / p95" panel (`OBSERVABILITY.md` → Dashboard).
6. **Before the first external invite:** the app's invite and consent screens on
   TestFlight, then both gates `on` (`shared_secrets = { BETA_GATE = "on",
   CONSENT_ENFORCE = "on" }`, the same apply and restart) and `SMOKE_EXPECT_GATES=on make
   smoke-prod`, which fails unless both refuse.

**For the app (accounts and sync):** the contract is `API.md` → changelog "Accounts and
sync state", Swift mirror included.

- Send `X-RYFT-Device-ID` (the same id as in sync) on every request; show
  `User.account_hint` ("you used another account on this iPhone", with its provider and
  last sync) and offer to sign out.
- `GET /v1/me/sync` for a "Synced / scoring / waiting for sleep / no data since" line and
  the pull-to-refresh; poll it while `today.state` is `pending`, about
  `expected_within_sec`. On `resync_requested_at` newer than the last full re-read: read
  the HealthKit window again without anchors.
- `DailyScore.scoring_state` instead of guessing from missing fields.
- `SyncResponse.accepted` / `ignored` / `scoring.queued_days` in the sync log; counts no
  longer include identical re-sends.
- The silent push `{"ryft": {"kind": "refresh", "day": …}}`, now after every sync too:
  refetch that day.
- `X-RYFT-Gate` on a `200`: the request would be refused once the gate is `on`; show the
  consent or invite screen without blocking.
- `403` with `consent_required` or `invite_required` is not a sign-in problem: show the
  matching screen, never "RYFT didn't accept your sign-in". Show request ids exactly as
  received (the hyphen the incident lost).

**Beta: your steps, in this order:**

1. **Supabase Pro:** done (2026-10-20): daily backups, kept 7 days; PITR optional
   (`OPERATIONS.md` → Backups). The first restore drill is still open.
2. **DPAs:** accept Supabase's and AWS's data processing agreements in their consoles
   (and Grafana Labs', if the logs keep going there: decide in step 4).
3. **Deploy:** done (`21eb0fa`, 2026-10-20). What it said: migrate applies 0017–0021; NATS
   restarts with the new permissions; scoring narrows `RYFT` to its four subjects and
   creates `RYFT_DLQ`.
   - **Consent comes first for your own account:** from now on its syncs answer `403
     consent_required` and nothing is scored until it consents, with the app build that
     has the consent screen or by hand: `curl -X POST -H "Authorization: Bearer $TOKEN"
     https://api.ryftapp.de/v1/me/consent -d '{"policy_version": "2026-10-20"}'`.
   - Then `make smoke-prod` (the dedicated smoke user; it consents itself) and
     `scripts/rescore_all.sh`, so every row carries `model_version` `2026.10.1` (accounts
     without consent are skipped: consent first).
4. **Grafana Cloud:** the 20 rules are loaded (21 after the next `make alerts-apply`) (`make alerts-apply`, 2026-10-20: group
   `ryft` in namespace `ryft`, Prometheus instance `3085952`). Open: a contact point
   (email and the Grafana mobile app) and a notification policy for `critical`,
   `warning`, `info`. Until the Beta is deployed, the rules on its new metrics
   (`ryft_db_up`, the backlog, dead letters, feedback) have no series and stay quiet. Decide whether RYFT's pod logs
   keep going to Grafana Cloud (exclude `ryft-.*` in Fleet Management, or name Grafana
   Labs as a processor; the privacy draft says it does now). Delete the old onboarding
   token (still open, below).
5. **`infra/vault`:** `terraform plan` and apply: the operator-only mount `operator/demo`
   with the demo password; the gates as in **Accounts and sync** step 2 and 6 (then
   restart the operator: `kubectl -n vault-secrets-operator-system rollout restart deploy
   vault-secrets-operator-controller-manager`).
6. **Invite codes** for the first testers (`OPERATIONS.md` → Beta), and
   `scripts/beta_report.sh` to watch whose data arrives.
7. **Legal pages:** fill in every `TODO(user)` (controller, address, contact, DPAs, the
   authority, liability, law), have them reviewed, remove the DRAFT banner and the
   `noindex`. Decide the **minimum age**: the API accepts birth years up to 13 years ago,
   Art. 8 GDPR asks 16 in Germany for consent-based services.
8. **App Review:** `scripts/seed_demo.sh` after step 5, the password into App Store
   Connect; check the AASA with `curl` after the deploy (`OPERATIONS.md` → Apex site).

**For the app** (the contract is `API.md` → changelog "Beta", Swift mirror included):
the consent screen (`POST /v1/me/consent` with `GET /v1/config` →
`privacy_policy_version`; `403 consent_required` and `invite_required` with their
`code`), the invite screen (`POST /v1/me/invite`), the birth year in onboarding and
`max_hr_manual` in settings, "Data sources" from `User.sources`, the feedback form
(`POST /v1/feedback`, with `request_ids`), the model version and its changelog (`GET
/v1/model`), and `app_version`, `os_version`, `source_bundle_id` in every sync.

**Open, technical:**

- After a strength session is completed or edited for an earlier day, only that day is
  rescored; the next days' sleep need, debt and readiness catch up with the 15-minute
  pass (yesterday and today) or a recompute. Rescoring forward to today would fix it.
- Ingest queues tomorrow for today's workouts (sleep need), so a row for tomorrow can
  exist before the day starts; harmless, but untidy.
- The consent check is not cached (the brief said 60 s): withdrawal takes effect at once
  (D78).
- The refresh throttle (5 minutes per account) lives in notify's memory: one replica, and
  a restart forgets it, so a restart may send one extra silent push per account (D92).
- `expected_within_sec` is the p95 of the last 6 hours' queue; with fewer than 5 days
  scored in that time it says 30.
- A merge moves an account's rows but not its Supabase identity: the person signs in the
  kept account's way, or links the identities in the app (`SUPABASE.md` → Manual identity
  linking, off until switched on in the dashboard).

**Before the Beta**, applied on 2026-10-19 except where marked open:

1. **State in HCP Terraform:** done (`make tf-migrate-hcp`; the `monitoring` namespace
   was recreated by Terraform). The local `terraform.tfstate*`, `tfplan` files and
   `~/.ryft-tfstate-rescue/` are deleted, the S3 bucket of the first attempt is
   destroyed and `infra/aws/state` removed (2026-10-19).
2. **Supabase in Frankfurt:** done; the new database was migrated by the deploy.
3. **`infra/vault`:** done. Applied with the Supabase values, `GC_TOKEN`, the four
   `service_secrets.api.APPLE_*` (the Apple Developer Program membership was accepted on
   2026-10-19) and `service_secrets.notify.APNS_*`. Sign in with Apple revocation is on:
   the api starts without the "APPLE_* not set" warning. After every apply that adds
   keys, `kubectl -n vault-secrets-operator-system rollout restart deploy
   vault-secrets-operator-controller-manager` (the operator re-reads Vault only hourly).
4. **`infra/aws/kubernetes`:** done (no changes left).
5. **`infra/aws`:** done: budget, the API endpoint stays `0.0.0.0/0` (HCP Vault's
   TokenReview), the node tags (one full node rotation), three t3.large nodes (D57, D64). The budget's
   $165 is below the estimate; raise `monthly_budget_usd` or expect the alarm.
6. **Images and deploy:** done; `82506fa` runs for every service (ingest, scoring,
   api, notify, migrate, web).
7. **`make monitoring-apply`:** done; the Helm release is `deployed` (revision 8), the
   stale collectors are removed from Fleet Management (2026-10-19). Left as they are,
   RYFT's pod logs: excluded from Grafana Cloud (`ryft-.*` in Fleet Management, the user's
   decision of 2026-10-21; check in Explore that `{namespace=~"ryft-.*"}` is empty), and the alert
   rules (now `deploy/grafana/alerts/ryft.yaml`, Beta step 4) are not loaded. **Open risk:** the onboarding's
   Grafana token, which was on a command line and in a chat, is still valid; delete it
   under Cloud access policies.
8. **`make smoke-supabase`** against `https://api.ryftapp.de` with
   `SUPABASE_SECRET_KEY` (open). Record the run here. It now also runs the Train checks,
   which leave a custom exercise, an enrolment and sessions on the smoke user.
9. **Train (M3b):** done (`3a0e9a1`).
10. **Sleep upgrades (M4) and push + remote config (M7 core):** deployed 2026-10-19 at
    `b7e4a4a`. Checked afterwards: every pod ready without restarts; migrate applied
    0013–0015 and set four role passwords; JetStream has the consumers `scoring` and
    `notify` with their subjects; the admin route lists the six seeded flags; the
    public routes answer `401` without a token. The first `make k8s-apply-eks` stopped
    with `nats-0` crash-looping: its Secret lacked `NATS_PASSWORD_NOTIFY` because the
    operator had not re-read Vault yet; restarting the operator fixed it, and
    `make k8s-apply-eks` now checks that first (`k8s-check-nats-secret`). **Open: confirm
    the rescore and `learn-baselines` ran** (their output is the terminal's, not the
    pods'). How it was rolled out:
    **Vault first**, because notify is a new service: its namespace, service account,
    role and NATS passwords, operator manifests (`overlays/vault-namespaces/notify`) and
    the api's `ADMIN_API_KEY` come from `infra/vault`, and NATS will not start without
    `NATS_PASSWORD_NOTIFY`:

    ```bash
    cd infra/vault && terraform plan -out=tfplan      # + namespace ryft-notify, Vault ns, role, kv; api gains ADMIN_API_KEY
    terraform apply tfplan && cd ../..                # warns: no APNs key (expected until Apple accepts the membership)
    ```

    Then push; once CI is green, put its short SHA in
    `deploy/k8s/overlays/eks/kustomization.yaml` (all **five** `newTag`, notify included,
    quoted) and `make k8s-apply-eks`. The migrate job applies 0013–0015 and sets four
    role passwords; NATS rolls (its config gained notify); notify starts and logs that
    APNs is not configured. Then fill history with the M4 rules and learn the baselines
    once:

    ```bash
    scripts/rescore_all.sh                                   # 400 days, every user
    kubectl -n ryft-scoring exec deploy/scoring -- /app learn-baselines
    curl -s https://api.ryftapp.de/v1/config -H "Authorization: Bearer <token>"   # the six flags
    ```

    Until the rescore, older days report their whole need as `base` and have no
    `sleep_quality` / `sleep_consistency` / `nap_min`; the Sleep screen already shows
    the new quality for them. The weekly estimate then runs on its own every Sunday.
11. **APNs key:** done. `service_secrets.notify` holds the APNs key (`ZMJ74PJSGJ`,
    sandbox and production); notify logs `apns configured` for `dev.timkrebs.ryft`.
    Apple accepts the provider token in both environments (a request with a dummy
    device token answered `400 BadDeviceToken`, not `403`). **Open:** the first real
    push needs the app to register a token (Xcode: Push Notifications capability,
    `POST /v1/devices/{id}/push`). Then turn `push.enabled` on for yourself, and after
    the first `MORNING_SCORE` (`ryft_push_notifications_total{result="sent"}`) for
    everyone (`OPERATIONS.md` → Feature flags).
12. **Journal (M5):** deployed 2026-10-20 at `82506fa`. Checked afterwards: every pod
    ready without restarts; migrate reports the schema current and seeded the 25
    behaviors (0016 is applied); `/v1/behaviors`, `/v1/journal/{day}` and
    `/v1/behaviors/impact` answer `401` without a token. The first
    `make k8s-apply-eks` failed on expired AWS credentials ("the server has asked for
    the client to provide credentials"); it now checks the login first
    (`k8s-check-auth`) and stops at the first failing apply. Open: the app side
    (journal sheet, Behavior Insights, Profile → Journal).

Known gaps, accepted for the alpha:
- **Failure-path metrics:** `ryft_events_handled_total{result="retry|failed"}` and
  `ryft_nats_redeliveries_total` are wired but were not exercised end to end.
- **Single points:** one NATS replica and one scoring replica.
- **Kubernetes auth needs HCP Vault to reach the EKS API** (TokenReview).
- **Grafana excludes RYFT logs by configuration, not by a boundary;** Beyla sees RYFT's
  HTTP traffic (D51).
- **Insight texts are English only.** Handlers are covered by the smoke contract checks,
  not by Go tests against a database.
- **M3 is deferred** (D58): no strain target or bands, and strength workouts without a
  logged session get no muscular load (M3's duration estimate).
- **No push delivered to a device yet**: the key is accepted by Apple, but no app
  build registers a token so far. The morning push waits for quiet hours
  through a JetStream redelivery (up to a day), and a failed publish of `ryft.scored`
  loses that morning's push. One notify replica.
- **M4 calibration rests on the pins** in `SCORING.md` → Sleep: the 40 % restorative
  target, the quality weights and the learned-baseline qualifier are judgement calls
  (D65, D67), not fitted to data. The learned baseline's `applied` path is tested
  against Postgres, not yet seen on real nights. The nap split works on stored segments
  only: a nap the watch records as part of the night (no hour's pause) stays night.
- **Local smoke data**: repeated `make smoke` runs on one volume pile up overlapping
  synthetic nights of one source on the same dates (minutes asleep above 900 in the
  table). The checks hold; `make down && make up` starts clean.
- **Train calibration rests on three reference sessions** (D59). Body weight comes from
  the sessions, not from HealthKit; a first session of an exercise is scored against
  its own sets.
- **Cost:** about $350–370 a month with three t3.large (`infra/aws/README.md`, D64), far
  above the budget's default of $165: raise `monthly_budget_usd` or expect the alarm. The budget alarm watches
  it.

- **For the iOS side** (nothing in `../app` was changed):
  - Decode `User.email`/`provider`, `DailyScore.steps`/`active_kcal`,
    `DayStrain.steps`/`active_kcal`, and `APIErrorBody.requestId`. All are optional, and
    the current models still decode.
  - Status handling: treat `503` as "retry after `Retry-After`", never as sign-out. Honour
    `429` + `Retry-After`. `413` means a smaller sync page. Only a second `401` after a
    refresh signs out.
  - Delete account: call `DELETE /v1/me`, then sign out and clear caches.
  - After every successful Sign in with Apple, send
    `ASAuthorizationAppleIDCredential.authorizationCode` once to
    `POST /v1/me/apple-authorization` (`204`; ignore `422`/`501`, retry `503` once).
    Without it, deletion cannot revoke the Apple grant.
  - A `429` may come from the edge with a plain-text body: honour `Retry-After` without
    expecting JSON.
  - Entitlements: `webcredentials:ryftapp.de` for password AutoFill with email accounts
    (`apple-app-site-association` is live with the deploy); `applinks:ryftapp.de` once
    the app handles `/auth/*` universal links.
  - New users start in `Europe/Berlin`: send the device's zone with `PATCH /v1/me` on
    first launch.
  - Show steps and kcal on the Strain screen.
  - `SleepMath.percent` treats values ≤ 1 as fractions, so a 1 % awake share renders as
    100 %. The API sends whole percentages (`API.md`, Conventions).
  - The app also reads `details` keys. They are frozen, but the typed fields
    (`baseline_days`, `DayDetail.sleep`, `DayDetail.strain`) are the contract.
  - **Train** (`API.md` → Train, Swift mirror): a logged set is `SessionSet` in Swift.
    `PUT /v1/sessions/{id}` after every set with the whole document; a `409` body is the
    server's `Session` (merge by set id, later `updated_at` wins, save again). Omitting a
    set never deletes it while in progress: uncheck it (`completed_at: null`). Send
    `ended_at` with `…/complete` when completing from the offline queue. Send
    `Accept-Language` for German exercise names. `workout_id` can change once the
    watch's workout syncs; merge the activity row by `Workout.session_id`.
  - **Journal** (`API.md` → Journal, Swift mirror): the sheet sends every behavior it
    showed (`PUT /v1/journal/{day}`, off toggles as `value_bool: false`), because a
    behavior not sent counts as unknown; the Today card asks until
    `GET /v1/journal/{yesterday}` is `answered`; editable 14 days back (`editable`).
    Behavior Insights from `GET /v1/behaviors/impact`: show recovery and sleep effects
    as points ("−14 recovery"), HRV as %, the interval as a range bar, locked ones with
    `needed`. Profile → Journal: `PATCH /v1/behaviors`, custom behaviors with the app's
    id (`POST`, `DELETE`). Names follow `Accept-Language`.
  - **Remote config and push** (`API.md` → `GET /v1/config`, Push notifications;
    Swift mirror): load `/v1/config` on launch and after `ttl_sec`; register the APNs
    token with `POST /v1/devices/{id}/push` (the sync `device_id`, `env` from the
    build, the app language) on every change and `DELETE` it on sign-out; per-category
    toggles and quiet hours through `PATCH /v1/me/notifications` (`NotificationPatch`
    encodes `null` to clear). Ship the localisation keys from `API.md` in
    `Localizable.xcstrings` (en and de) and the categories `MORNING_SCORE`,
    `SESSION_SCORED`, `PR`, `SYNC_STALE`; open `ryft.url`; refresh `ryft.day` on the
    silent push. `push.enabled` stays off until the APNs key exists.
  - **Sleep upgrades** (`API.md` changelog 2026-10-19, Swift mirror): decode the new
    `DailyScore` fields, `SleepDetail.need` / `restorative`, `SleepRange.avg_consistency`
    / `bedtimes`, `User.sleep_base_source` / `_learned_min` / `_learned_at`. `quality` is
    no longer equal to `sleep_performance`: show it as the night's quality and keep
    performance for "% of need". Planner: `GET /v1/sleep/plan?wake=HH:MM` (local time;
    `422` when malformed). Settings: show the learned value and its source; setting a
    value sends `sleep_base_min` (manual), "use learned" sends `sleep_base_manual:
    false`. New insight ids and trend metrics are additive.

## Deploy

Local:

```bash
make up && make smoke                 # Docker or Podman: migrate, roles, NATS users, contract checks
make check-swift                      # optional, macOS
```

Images (CI does this on push once the repo is on GitHub; by hand):

```bash
make push REGISTRY=ghcr.io/timkrebs/ryft            # ingest, scoring, api, migrate: :<short-sha> and :latest
```

EKS, in order (each Terraform root applied by you from your machine; state in HCP
Terraform; full runbook in [`OPERATIONS.md`](OPERATIONS.md#eks-apiryftappde)):

```bash
# State: HCP Terraform workspaces (execution Local), after `terraform login` and, once, `make tf-migrate-hcp`
(cd infra/aws/dns && terraform init && terraform plan -out=tfplan && terraform apply tfplan && terraform output godaddy_instructions)
# GoDaddy: switch the nameservers to the four printed; check: dig NS ryftapp.de @a.nic.de +short
(cd infra/aws && terraform plan -out=tfplan && terraform apply tfplan)
aws eks update-kubeconfig --region eu-central-1 --name ryft-prod-euc1 --alias ryft-prod-euc1
(cd infra/aws/kubernetes && cp terraform.tfvars.example terraform.tfvars)      # set acme_email
(cd infra/aws/kubernetes && terraform init && terraform plan -out=tfplan && terraform apply tfplan)
(cd infra/vault && cp terraform.tfvars.example terraform.tfvars)               # Supabase values (SUPABASE.md)
(cd infra/vault && export VAULT_TOKEN=… && \
  terraform init && terraform test && terraform plan -out=tfplan && terraform apply tfplan)
# newTag in deploy/k8s/overlays/eks/kustomization.yaml = the CI commit
make k8s-validate && make k8s-apply-eks
make monitoring-apply                                  # Grafana Cloud collector, after infra/vault has the token
make smoke-prod                                        # after every deploy (OPERATIONS.md → Deploy and roll back)
make alerts-apply                                      # alert rules into Grafana Cloud (OBSERVABILITY.md → Alerts)
scripts/rescore_all.sh                                 # after a deploy that changed scoring rules or the model version (Beta: yes)
kubectl -n ryft-scoring exec deploy/scoring -- /app learn-baselines   # M4, once; then weekly on its own
```

Run `make smoke-supabase` only with the dedicated test user.

Self-hosted (k3s etc.): `make k8s-apply-local` (`OPERATIONS.md` → Self-hosted).
