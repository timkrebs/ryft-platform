# RYFT platform — status

State of the platform after go-live (M1, M1b and the zero-trust layout; 2026-10-18),
Train (M3b, 2026-10-19; M3 itself is deferred, D58), the sleep upgrades (M4,
2026-10-19), the push and remote-config core of M7 (2026-10-19) and the journal with
behavior impact (M5, 2026-10-19), all deployed (`82506fa`, 2026-10-20) with the Sign in
with Apple and APNs keys active. The **Beta** for friends on TestFlight (2026-10-20) is
deployed (`21eb0fa`). The follow-up **operator tooling, account mix-ups and server-side
sync state** (2026-10-20, `61db1c4`…`cbfc8d0` and the docs commit) is committed and **not
deployed yet**: see **Accounts and sync: your steps** below, and **Incidents**.
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
| Web and demo | legal page drafts (en/de, DRAFT, `TODO(user)`), AASA `applinks` for the app's paths, "Open in RYFT" fallback; `scripts/seed_demo.sh` for `review@ryftapp.de`, password generated by `infra/vault` | `deploy/k8s/base/web`, `infra/vault/operator.tf`, D86 |

**Accounts and sync** (2026-10-20, not deployed):

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

**Accounts and sync: your steps, in this order:**

1. **Deploy** (`OPERATIONS.md` → Deploy and roll back): commit your `newTag: "21eb0fa"`
   edit (or let the new one replace it), push, let CI build, set every `newTag` to the
   new SHA, `make k8s-apply-eks`. migrate applies 0022–0025; NATS restarts with the new
   permissions (scoring may publish `ryft.refresh`, notify dead-letter
   `ryft.dlq.refresh`); scoring widens `RYFT` to five subjects and notify's consumer
   takes `ryft.refresh`. Order does not matter: a refresh scoring may not publish yet is
   logged, not retried.
2. **`CONSENT_ENFORCE=warn`** until the app's consent screen is on TestFlight (the
   incident), **only after step 1**: `21eb0fa` accepts `BETA_GATE` `on` or `off` only and
   does not start with `warn`. In `infra/vault/terraform.tfvars`, `shared_secrets = { CONSENT_ENFORCE =
   "warn" }` (and `BETA_GATE = "warn"` if you want the invite gate counted already; left
   out, it stays `off`). Then:

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
   by choice: RYFT's pod logs also reach Grafana Cloud (not excluded), and the alert
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
