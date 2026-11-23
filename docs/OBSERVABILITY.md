# RYFT — observability

RYFT on Cloud Run has one observability: **Cloud Monitoring** (D236, which supersedes D224's
Grafana half). Nothing scrapes the services and nothing is pushed: what Google measures itself
(Cloud Run, Pub/Sub), log-based metrics counted from lines the services write, uptime
checks from outside, the dashboards "RYFT · Overview" and "RYFT · Connections" (D257) and six alert
policies (D244), all in `infra/gcp`
(**Cloud Monitoring** below). Logs are JSON (`slog`) on stdout, in Cloud Logging's bucket
`ryft-logs` for 14 days (**Logs** below). Without Cloud Run (compose) each service serves
`GET /metrics` on its own port, **9090** (`METRICS_PORT`, no auth), apart from the API and
probe port 8080. The probes are `/health/live` and `/health/ready` (**Health paths** below).
The EKS cluster, its collector, Grafana Cloud, Vault and RabbitMQ are gone (D3, D246), and with
them their sections here.

## Cloud Monitoring (Cloud Run, D236)

Everything here is `infra/gcp`: `monitoring.tf` (the log-based metrics, the uptime checks, the
dashboard; free at RYFT's volume, so always there) and `alerts.tf` (the six policies; only once
the workspace variable `alert_app_channel` is set, the app channel's resource name, runbook GCP step
11.1, D237). The user applies it (OPERATIONS → GCP → 11).

**What is measured, and by whom.**

| Source | Series (PromQL name) | Labels used | Read by |
| --- | --- | --- | --- |
| Cloud Run | `run_googleapis_com:request_count` | `service_name`, `response_code_class`, `response_code` | policy 2, the dashboard |
| Cloud Run | `run_googleapis_com:request_latencies_bucket`, `run_googleapis_com:container_instance_count` | `service_name`, `le`, `state` | the dashboard |
| Pub/Sub | `pubsub_googleapis_com:subscription_num_undelivered_messages`, `…_oldest_unacked_message_age` | `subscription_id` | policies 4 and 5, the dashboard |
| Uptime checks | `monitoring.googleapis.com/uptime_check/check_passed`, `…/time_until_ssl_cert_expires` | `check_id`, `checker_location` | policy 1, the dashboard |
| Log-based | `logging_googleapis_com:user_ryft_ticks` | `result` (ours), `service_name` (the resource's) | policy 3, the dashboard |
| Log-based | `logging_googleapis_com:user_ryft_errors` | `service_name` (the resource's) | the dashboard |
| Log-based (D244, D257) | `logging.googleapis.com/user/ryft_notices` (a classic filter, not PromQL) | `kind` (ours) | policy 6 |
| Log-based (D257) | `logging_googleapis_com:user_ryft_connect_pushes` | `provider`, `kind`, `outcome` (ours), `service_name` (the resource's) | the dashboard RYFT · Connections |
| Load balancer (from `edge = "built"`, D242) | `loadbalancing_googleapis_com:https_request_count` (`https_lb_rule`) | `backend_target_name`, `response_code_class` (a number: `500`), `response_code` | the dashboard |
| Cloud Armor (the same) | `networksecurity_googleapis_com:https_request_count` (`network_security_policy`) | `backend_target_name`, `blocked` | the dashboard |
| Log-based (the same) | `logging_googleapis_com:user_ryft_edge_denials`, `…_preview_matches` | `priority` (ours) | the dashboard |

### The log-based metrics

Four counters (`DELTA`, `INT64`) of lines the services write, nothing pushed. D236's two needed no
Go change; D244's counts one new api line too, and D257 adds the connections' lines. All four read
only the services' own stream on Cloud Run (`resource.type="cloud_run_revision"`, the
`run.googleapis.com/stdout` log, and for errors `stderr` too), never Cloud Run's request log. **No
label may carry an IP, a user id, an email or a message id**: `ryft_ticks` has one label of its own,
`result`, `ryft_notices` one, `kind`, and `ryft_connect_pushes` three, `provider`, `kind` and `outcome`, each a
closed set (`tests/monitoring.tftest.hcl` pins them, and the run `the_connect_lines_are_written` pins
the D257 messages, and the hook's seven outcomes, to the Go that writes them); the service is the resource's `service_name`.

- **`ryft_ticks`**: one per `POST /internal/tick` a service handled, from the line `tick`
  (`internal/events/pubsub/tick.go`: `result` `ok` or `error`, with `message_id` and `took`, which
  stay in the log). It is logged at `info`, so a `LOG_LEVEL` above `info` silences it and policy 3
  fires for every service.
- **`ryft_errors`**: every line at `severity>=ERROR`, panics included (the line `panic` with its
  `stack`). A panel, no policy (the cap).
- **`ryft_notices`** (D244): api's lines the operator wants to hear of at once, by `kind`, the
  line's message: `feedback received` (`backend/cmd/api/feedback.go`, once per report stored),
  `admin signed in` (once per operator session per instance: a token whose TOTP is at most 10
  minutes old, so a sign-in on `/admin` or its re-challenge after 12 hours; no key of its own) and
  `admin auth rejected` (`adminauth.go`'s `reject`), but not for `reason` `missing` (no bearer:
  crawlers) or `expired` (the operator's own page with an old token, which refreshes once). The
  reasons are left out by a negative list on purpose: a reason added later counts. Not counted: the
  key's own use (scripts, smoke) and `admin second factor required` (in the audit). All three lines
  are INFO, so a `LOG_LEVEL` above `info` silences them and policy 6. **Since D257 four connection
  notices** (design §10), each matched on the service that writes it:
  - `connect capacity` (api): a provider's counted links (`connected` and `needs_reauth`, plus the
    accounts holding an open start or an unconfirmed grant) are at 80 % of `CONNECT_<PROVIDER>_USER_CAP`
    or more;
  - `connect no data` (api): at least one connection `connected` over 36 hours ago has stored nothing
    for 36 hours (`ryft_connect_stale_connections` above 0);
  - `connect deregistration stuck` (api): a row of the deregistration queue is older than 24 hours;
  - `connect inbox failed` (ingest): at least one `provider_inbox` row is `failed`.

  The four come from the tick steps `connect gauges` (api) and `connect inbox` (ingest), **at most once a day**:
  only the tick that starts 06:00–06:14 UTC writes them, and only while the condition holds, so
  policy 6 notifies once a day per kind and never every 15 minutes. They must be at `info` or above.
  The line's other fields (counts, providers) stay in the log. No panel (it would change the
  overview; a free follow-up).
- **`ryft_connect_pushes`** (D257): ingest's line `connect push`, one per request to the hook
  `POST /connect/{provider}/push` of a served provider (an unserved one is a `404` before the line), by
  `provider` (the registry's name) and `outcome`, the request's: `accepted` (verified and split; every
  item answered `200`, stored or not), `rejected` (the provider's verification refused it: `401`; or
  could not run now: `503`), `too_large` (`413`), `bad_body` (`400`), `error` (the database: `503`, the
  provider retries), `busy` (the instance's 2 hook slots were taken: `503` with `Retry-After`, before
  the body is read) and `timeout` (the body did not arrive within 10 s: `408`, nothing stored). A
  sustained `busy` is OPERATIONS → Direct connections → The hook is busy or slow. The
  line's per-item counts stay in the log (by kind they are `ryft_connect_push_items_total`, compose
  only). No user id, record id, address or body is on the line. A tile on **RYFT · Connections**, no
  policy (the cap): a provider secret that no longer verifies shows there at once as `rejected`, and as
  the notice `connect no data` within 36 hours. Flat until phase 2: `CONNECT_PROVIDERS` is empty on
  Cloud Run and the fake is compose only.

A counter writes no point for a minute without a matching line, so "no tick" is an absent series:
policy 3 names every service and asks for each. A log-based metric counts from its creation on,
not back, so the tick policy may open once at the first apply and closes within the hour (runbook
GCP 11.3).

### The uptime checks

Every 5 minutes from exactly three locations (`EUROPE`, Belgium; `USA_IOWA`; `USA_VIRGINIA`: the
fewest Google takes, and `USA` alone would be three more), a 10-second timeout, `GET` over validated
TLS, exactly `200`, no redirect followed.

| Check | URL | Passes when | Exists |
| --- | --- | --- | --- |
| `ryft-site-home` | `https://ryftlab.de/` | 200 | always |
| `ryft-site-privacy` | `https://ryftlab.de/privacy` | 200 | always |
| `ryft-site-aasa` | `https://ryftlab.de/.well-known/apple-app-site-association` | 200, the body holds `M77SKHKG63.dev.timkrebs.ryft` | always |
| `ryft-api-ready` | `https://api.ryftlab.de/health/ready` | 200 (the database behind it) | while api and ingest are public (`serve` and no stop line) |
| `ryft-ingest-ready` | `https://ingest.ryftlab.de/health/ready` | 200 | the same |

5 checks × 3 locations × 12 runs an hour × 730 hours = **131,400 executions a month**, under the
free million (the brief's figure, not checked here). The privacy page has no body marker: its text
changes with C2. `www.ryftlab.de` has no check (its redirect is Hosting's own).

### The dashboard "RYFT · Overview"

`google_monitoring_dashboard.overview`, two tiles a row: requests per second, 5xx per second, p95
latency and instances, each by service; 429s by service (what Saturated paged on); ticks per hour
by service and result; the oldest unacked event and the undelivered events per event subscription;
the dead letters held per hold subscription; ERROR lines per hour by service; uptime (the share of
locations passing) and the certificate's days left, by check; and a logs panel of the services'
`severity>=ERROR` lines from `ryft-logs`. ingest's p95 is what SyncSlow paged on (10 s).

### The dashboard "RYFT · Connections" (D257)

`google_monitoring_dashboard.connections`, apart from the overview (the feature ships dark, and the
overview's tiles stay as `tests/edge.tftest.hcl` pins them): the hook's requests per hour by provider
and outcome (`ryft_connect_pushes`); ingest's 429s per second (the hook class and the `ip` class
together: Cloud Run's metric has no path); the inbox's oldest unacked event per subscription (policy 5
fires above 900 s) and the dead letters its hold keeps (policy 4), both named from the topology
(ingest's push subscriptions with a dead-letter topic, `ingest-inbox-received` and `dlq-ingest-hold`
today; the overview shows them among the others); and a logs panel of ingest's `connect push` lines
and the access lines of its `hook_limit` refusals (`msg="http"`, `code` `hook_limit`: no address, no
query), from `ryft-logs`. The edge's rule 1900 (the hook's throttle, D257) is a priority on the
overview's Cloud Armor tile once the edge is built.

### The edge (D242)

From `edge = "built"` on (`infra/gcp/edge.tf`, runbook GCP 16) api and ingest can sit behind a
global external Application Load Balancer with Cloud Armor; from `live` on they do. What it adds
here, and nothing at `off`:

- **Four tiles** after the PromQL tiles above: the load balancer's requests per second by backend
  (`ryft-edge-api`, `ryft-edge-ingest`) and code class; its 429s per second by backend (Cloud
  Armor's and the services' own, as the client got them); Cloud Armor's requests per second,
  blocked or not, by backend; and Cloud Armor's denials and preview matches per hour by rule
  priority (series by `priority` and `kind`, `denied` or `preview`). The rest of the dashboard and
  the logs panel are unchanged.
- **Two log-based counters** (`DELTA`, `INT64`), from the load balancer's request log
  (`resource.type="http_load_balancer"`): `ryft_edge_denials` (`enforcedSecurityPolicy.outcome`
  DENY: an enforced WAF rule's 403, the rate rule's 429 and ban) and `ryft_edge_preview_matches`
  (`previewSecurityPolicy.outcome` DENY: what a preview rule would have refused). One label,
  `priority`, a closed set: 1000 to 1005 the WAF sets (sqli, xss, lfi, rce, scannerdetection,
  protocolattack), 2000 the rate rule. Never the IP (`rateLimitAction.key` holds it), a URL or a
  user agent.
- **No alert policy** (the cap, D236, D244). A WAF rule's preview matches decide when it is
  enforced (runbook GCP 16 → Enforcing a WAF rule); the uptime checks keep their URLs and reach the
  services through the edge once `live`.

### The alert policies

Six conditions, one policy each, to both channels: the email `ryft-timkrebs` and the Google Cloud
app's (made by hand; the app channel by its resource name, the workspace variable
`alert_app_channel`, not sensitive and in no file, D237). Six is the cap
(`tests/monitoring.tftest.hcl`): each condition has a price, and the sixth was the user's decision
(2026-11-19, D244); a seventh is a decision too. Each policy's documentation is its first look; the
table with the commands is OPERATIONS → GCP → When an alert fires.

| # | Policy | Query and threshold | Evaluated, held | Severity |
| --- | --- | --- | --- | --- |
| 1 | RYFT · Uptime check failing | `uptime_check/check_passed`, `ALIGN_NEXT_OLDER` over 1200 s, `REDUCE_COUNT_FALSE` by `check_id` and host, `> 1` (2 of 3 locations failing): a classic threshold, as Google's console writes it, because `check_passed` is a BOOL | 600 s | critical |
| 2 | RYFT · 5xx over 2 % (api, ingest) | `sum by (service_name)` of `rate(request_count{5xx}[10m]) / rate(request_count[10m]) > 0.02`, and at least 3 5xx in the 10 minutes (one failed request among a night's uptime checks is no outage) | every 60 s, 600 s | critical |
| 3 | RYFT · Tick missing | each of api, ingest, scoring, notify (`label_replace(vector(1), …)`) `unless` it has `increase(user_ryft_ticks{result="ok"}[45m]) > 0`: no ok tick in three ticks, an absent series included. Only while the tick runs (not under `serve = false` or the stop line) | every 300 s, 300 s | critical |
| 4 | RYFT · Dead letters held | `max by (subscription_id) (num_undelivered_messages{the hold subscriptions}) > 0` | every 300 s, 300 s | warning |
| 5 | RYFT · Event backlog over 15 minutes | `max by (subscription_id) (oldest_unacked_message_age{the event subscriptions}) > 900`: a consumer that is down or fails every push, before its 20 attempts run out and policy 4 fires | every 300 s, 300 s | warning |
| 6 | RYFT · Notices: feedback, admin access, connections (D244; renamed and widened by D257) | `user/ryft_notices`, `ALIGN_SUM` over 300 s, `REDUCE_SUM` by `metric.label.kind`, `> 0`: a feedback, an admin session begun, a refused admin bearer or one of the four connection notices (at most once a day each) in the last 5 minutes, one series per kind, seven kinds. A classic threshold, not PromQL: one feedback is one point of a sparse `DELTA` counter, and `increase()` over one was never verified (D236). `evaluation_missing_data` `INACTIVE`: once 5 minutes pass without a line the incident closes, so the next line is a new incident and a new notification; only the opening is notified (`notification_prompts` `OPENED`) | 60 s | warning |

The subscriptions are named from the topology (`pubsub.auto.tfvars.json`), so a new consumer is in
4 and 5 without an edit. Incidents close when the query returns nothing, or 30 minutes after their
data stops; policy 6's when its data stops. Policy 6's subject and text name the kind
(`${metric.label.kind}`, a label its `group_by` keeps) and never a field of the line (no `${log.…}`
variable). A disabled channel is `checks.tf`'s `alert_channels_enabled` warning.

### What D236 dropped, and what covers it

The app rules (the Grafana ruler's, never loaded on Cloud Run; their file went with D236) and
Grafana's five Cloud Monitoring rules went with Grafana Cloud:

| Was | Now |
| --- | --- |
| ApiDown, SiteDown | policy 1 |
| HTTP5xxRatio, AuthUnavailable on `/v1` (its `503`) | policy 2; on a push or tick, Pub/Sub retries the `503` (policies 3 and 5) |
| TickMissing, DatabaseDown (a tick's db step fails: `result` error), OutboxBacklog and PublishFailing (the tick's sweep fails), InviteEmailPurgeFailing, AccountDeletionIncomplete (the reconcile is a tick step) | policy 3 |
| DeadLetters, DeadLetterPublishFailing (the push fails, Pub/Sub dead-letters it) | policy 4, before it policy 5 |
| PushBacklog, SyncToScoreSlow, ScoringErrors (scoring's 5xx keep events unacked) | policy 5 |
| Panics | the `ryft_errors` panel and the logs panel; a panic that ends the process is a 5xx (policy 2) or a backlog (policy 5) |
| SyncSlow, Saturated, RateLimited | panels (p95, 429s) |
| SiteCertificateExpiring | the certificate panel; a certificate that lapses fails TLS, which every check validates (policy 1) |
| NewFeedback, AdminWebUsed (the lost-phone detection) | policy 6 since D244, from the lines `feedback received` and `admin signed in` instead of the app metrics: every report and every operator session begun (per instance) |
| AdminUnauthorized (over 20 bad bearers in 10 minutes) | policy 6 since D244, stricter: any `admin auth rejected` but for `reason` `missing` and `expired`, one incident while they keep coming |
| **Lost**: TextRefusedByDatabase, NoSync, APNsFailing, PushFailing, APNsRejectsOurKey, SiteProbeFlapping, SiteWwwRedirectBroken, SyntheticsMissing (nothing to miss), OutboxStuck (the outbox queries in OPERATIONS) | nothing; each was an app metric that nothing reads on Cloud Run now |

### Error Reporting

Free, but not on by this root. Its API (`clouderrorreporting.googleapis.com`) is not on the keep
list (`project.tf`); it groups only errors whose stack trace is in a field it knows (`message`,
`stack_trace`, `exception`), and the services' `panic` line has it in `stack`; and where and how
long it keeps error events is not checked against the privacy text's 14 days in the EU. Runbook GCP
11.5 turns its notifications on only where the API is already on; anything else is a PR (the keep
list, the line's key) and a privacy check.

### Cost (a month)

| Item | Price | RYFT |
| --- | --- | --- |
| Alert conditions, reading A (the brief's: $1.50 a condition; no longer on Google's page, D244) | $1.50 × 5 | **$7.50** (about €6.50–7) |
| Policy 6 (D244; Google's pricing page through search snippets, 2026-11-19, *(search)*, not verified: alerting charged no sooner than 2027-09-01, then $0.35 a month per metric reference in a policy and $0.50 per million points returned) | 1 metric reference, at most 7 series (one per kind, D257 added four), usually none | **$0** until then, about **$0.35–0.45** after |
| Alert conditions, reading B (possibly from May 2026: $0.10 a condition, $0.35 per million time series returned; not verified here) | $0.10 × 5 + series | $0.50 + about $0.21 = **about $0.70** |
| Time series returned (reading B) | at most: 5xx 43,800 evaluations × 6 series, ticks 8,760 × 4, holds 8,760 × 2, backlog 8,760 × 6, uptime 43,800 × 5 | about 0.6 million, at most |
| Uptime check executions | free up to 1 million (not verified here) | 131,400: $0 |
| Log-based metrics (chargeable ingestion, 150 MiB free) | 8 bytes a point | at most about 4 MB (every series, every minute; in practice far less): $0 |
| Dashboard | free | $0 |
| The edge (D242, from `built`; not observability, listed for the budget) | forwarding rules, Cloud Armor's policy and rules, requests (D242 → Cost) | about $32 (≈ €28) |

Before D236 the same was free in Grafana Cloud's tier. The delta is $7.50 (reading A) or about
$0.70 (reading B) a month; the user accepted about €6 (2026-11-17). Policy 6 adds $0 until
2027-09-01 and about $0.35–0.45 a month after (D244 → The price; the date, both rates, how often a
threshold condition is evaluated and whether a free allotment applies are unverified).

### The OTLP push (dormant)

The services can still push their Prometheus registry by OTLP at the end of each tick and on
SIGTERM (`internal/metrics`, `config.LoadOTLP`, D224), but nothing sets `OTLP_ENDPOINT` and
`OTLP_USER` any more (`deploy/run` dropped them with D236; `cloudrun_render.py` refuses them), so
no service pushes. Pushing to Google instead (telemetry.googleapis.com) would need a token client,
resource attributes, an IAM role and a format nobody could verify here; writing `timeSeries.create`
would be the most code. The log-based metrics give the two signals that matter for nothing. The
secret `otlp-token` stays until no serving revision reads it (runbook GCP 11.6). The `ryft_*`
metrics below are what a push would carry, and what compose serves on `:9090`.

## Health paths

| Path | Same handler as | Answers | Used by |
| --- | --- | --- | --- |
| `GET /health/live` | `/healthz` | `200 {"status":"ok"}` while the process runs, from the moment it listens | Cloud Run's **startup probe** (every 1 s, 20 tries) and liveness probe (`deploy/run/<svc>.yaml`) |
| `GET /health/ready` | `/readyz` | `200 {"status":"ready"}` when the service's checks pass (the database, and where it has one the signing keys); else `503` | the uptime checks (`ryft-api-ready`, `ryft-ingest-ready`, D236), the smoke, the runbook |

Cloud Run cannot route some paths that end in `z` (Google answers its own `404` for `/healthz`,
design §3), hence the pair; `/healthz` and `/readyz` stay for compose. A service listens before
it opens its database pool, so `/health/live` answers `200` at once and the ready paths `503`
until the start is done: an instance starts during a database outage, and its tick logs `result`
error (policy 3, Tick missing) instead of no instance and no signal.

On Cloud Run (`K_SERVICE` set) both ready paths answer a **generic** `503 {"error":"not ready"}`
(no `Retry-After`; the path is public on api and ingest, so no dependency's error is shown), log
the cause once at warn (the line `not ready`, with `err`), and reuse their result, ok or not, for 5 s:
however often a probe asks, the checks run (and the warning is logged) at most once in 5 s.
Without `K_SERVICE` the ready paths keep today's body, the failing check named (API.md → Health).

## Metrics

The services' own Prometheus registry. On Cloud Run nothing reads it since D236 (no push, no
scrape; **The OTLP push (dormant)** above): compose serves it on `:9090`. Alert names below are the
app rules', gone with D236 (**Cloud Monitoring → What D236 dropped, and what covers it**).

| Metric | Type | Labels | Service | Meaning |
| --- | --- | --- | --- | --- |
| `ryft_http_requests_total` | counter | `route`, `code` | ingest, api | Requests by ServeMux pattern (`GET /v1/days/{day}`, never the raw path) and status. Unauthenticated requests carry the outer pattern `/v1/`; unknown routes `unmatched`. |
| `ryft_http_request_duration_seconds` | histogram | `route` | ingest, api | Request latency. |
| `ryft_samples_ingested_total` | counter | `kind`, `type` | ingest | Records accepted by `POST /v1/sync`: `kind="sample"` with the sample type, `kind="sleep"` with the stage, `kind="workout"`. |
| `ryft_last_sync_timestamp_seconds` | gauge | — | ingest | Unix time of the last successful sync of any user. Resets on restart. (Had a `device` label before multi-user; per-user or per-device series would be unbounded.) |
| `ryft_rate_limited_total` | counter | `class` | ingest, api | Requests answered `429` by the per-user rate limit: `class="sync"` (`POST /v1/sync`), `session` (`PUT /v1/sessions/{id}`), `invite` (`POST /v1/me/invite`), `import` (`POST /v1/train/import`: bursts of 5, then one every 5 s; the app's batches one after another stay under it) or `default`; or `admin_ip` (`/v1/admin/*`: 10 a second per client address, bursts of 20, per instance, 4,096 addresses, newer ones share a bucket; a request bearing `ADMIN_API_KEY` is not limited; the address is never logged or labelled); or `ip` (D240: every route of api and ingest but the health paths and the tick, before any token: 20 a second per client address, bursts of 50, per instance, 65,536 addresses, newer ones share a bucket; on api a request to `/v1/admin/*` bearing `ADMIN_API_KEY` passes; its access line has `code` `ip_limit`); or `hook` (D257, ingest: `POST /connect/` instead of `ip`, 50 a second per client address, bursts of 200, 4,096 addresses, idle 1 minute; its access line has `code` `hook_limit`; `/push/` is exempt). The recompute gate (one per minute) is not counted here. |
| `ryft_http_panics_total` | counter | `route` | all | Handler panics turned into a `500` by the recover middleware. Each also logs `msg="panic"` with the stack. A tick step's panic counts under `route="POST /internal/tick"`; the tick goes on with its next step and answers `204` (D219). |
| `ryft_goroutine_panics_total` | counter | none | all (once a service starts through `internal/app`) | Panics recovered in a service's own goroutines: its servers (HTTP, and `/metrics` where it listens); there are no consumers, timers or relay goroutines any more (D219). The service shuts down in order and exits 2, as an unrecovered panic does; the `msg="panic"` line names the `task` and holds the stack. A panic in a ready check is counted too (the line names the `check`, logged at most once a minute) and the service keeps running. Any is a bug. The process exits 2 right after it, before any push would carry the increase: Cloud Logging's `msg="panic"` line and the instance's exit are the traces. |
| `ryft_http_text_refused_total` | counter | `route`, `at` | ingest, api | Requests refused for text the database cannot store (a NUL, or bytes that are not UTF-8; API.md → Errors → NUL): `at="body"` (`\u0000` in a JSON string, `422`), `url` (path or query, `400`), `database` (Postgres refused it, SQLSTATE 22021/22P05, `422`). No log line beyond the access line, and never the text. `database` above 0 means a path the checks and the raw-JSON normalisation missed, or a server-side bug producing bad text; the route says where (alert TextRefusedByDatabase). |
| `ryft_http_nul_stripped_total` | counter | `route` | ingest | Syncs accepted after the NULs were removed from their strings (`POST /v1/sync` does not refuse them: the app would never resend the page). One per request, however many NULs; never the text. A steady rate points at a source or device name the app should clean. |
| `ryft_auth_failures_total` | counter | `reason` | all | Rejected `/v1` requests (ingest, api), and push and tick tokens refused on `/push/*` and `/internal/*` (all four services, Google OIDC, D218) with the same reasons and no new ones: there `subject` is a token of another account than the push service account (its `sub`, `email` or `email_verified`), and `jwks_unavailable` means Google's signing keys are not loaded and the fetch the token tried failed (while none are loaded, a token fetches them first, at most one every 10 s per instance; the push or tick gets `503`, so `AuthUnavailable` covers it). For Google's tokens an unknown key id after the minute's refresh is `401 unknown_key` (anyone can send one; Pub/Sub retries a 401 too), and `jwks_refresh_limited` (`503`) only while the last fetch failed. 401 reasons: `missing_token`, `malformed`, `expired`, `signature`, `issuer`, `audience`, `unknown_key`, `key_mismatch`, `role`, `anonymous`, `subject`, `api_key`, `deleted` (the account was deleted), `invalid`. 503 reasons: `jwks_unavailable`, `jwks_refresh_limited`, `internal` (user lookup failed, or on `/v1/admin/*` the allow-list check). |
| `ryft_account_deletions_total` | counter | `step`, `result` | api | `DELETE /v1/me` and the reconciler. `step="data"` (our rows), `apple` (revoking the Sign in with Apple token) or `auth` (the Supabase auth user); `result` `ok`, `error` or `skipped` (not configured: no `APPLE_*` / `SUPABASE_SERVICE_ROLE_KEY`, or apikey mode). |
| `ryft_sync_deleted_total` | counter | `kind` | ingest | Rows removed by the deletions of `POST /v1/sync`, by `kind` (`sample`, `sleep`, `workout`). The same-source workout fold is not counted here. |
| `ryft_sync_ignored_total` | counter | `kind`, `reason` | ingest | Records and deletions of `POST /v1/sync` not applied: `kind` `sample`, `sleep`, `workout`; `reason` `duplicate`, `outside_window`, `unknown_type`, `unknown_id`. |
| `ryft_workouts_folded_total` | counter | `reason` | ingest, scoring | Duplicate workouts handled: `same_source` (a copy removed by a sync's fold, ingest) or `cross_source` (a workout newly marked `duplicate_of` another source's, scoring). `fold-workouts` runs as a one-off process without `/metrics`, so its removals are not counted here. |
| `ryft_workouts_fold_skipped_total` | counter | `reason` | ingest | Syncs whose same-source fold did nothing: `too_many` (more than 5 000 workouts of the request's sources in its range). Log line `workout fold skipped`. |
| `ryft_events_published_total` | counter | `reason`, `result` | ingest, api, scoring, notify | Publishes to Pub/Sub (by the outbox relay, D101, D218), `ok`/`error`. `error` is a refusal, an unreachable endpoint, an ordered event without a UUID `user_id`, or a key without a topic (no `unroutable` any more). Reasons: `ingested` with `sync` (ingest), `recompute` and `session` (an edited or deleted completed session; api), `session.completed` as `session_completed` (api), scoring's `scored`, `session_scored`, `refresh`, and notify's deferrals into `ryft-notify-wait` under the deferred event's reason. An `error` here is retried from the outbox row; an event is lost only if its outbox row could not be written (also `error`, and the sync or completion answers `503`). |
| `ryft_events_handled_total` | counter | `reason`, `result` | scoring, notify | Pushed events, by `result`: `ok` (`204`), `retry` (`500`: Pub/Sub delivers it again; also a deferral that could not be stored), `failed` (`500` on delivery attempt 20 or later: Pub/Sub dead-letters it), `bad` (did not decode, or a `ryft-key` that is not the push path's: the consumer dead-lettered it itself, `204`; always `reason="unknown"`), `dropped` (`events.Permanent`, e.g. the user was deleted, `204`), `deferred` (notify: quiet hours, stored as an outbox row due later, `204`). scoring's reasons are the `ingested` event's (`sync`, `recompute`, `merge`, `session`, `import`, `session_completed`), notify's `scored`, `session_scored` and `refresh`; `unknown` before the data decoded, and for any reason outside this closed list (`events.Reasons`): an ingested event's reason is in its body, so a forged one cannot add series. |
| `ryft_nats_redeliveries_total` | counter | `consumer` | scoring, notify | Messages received again, per consumer: a push with `deliveryAttempt` above 1, or a dead letter replayed with `scripts/dlq.sh` (`ryft-replayed=true`). Each delivery counts once. The name is from NATS, kept for the dashboards and the history (D102). |
| `ryft_days_scored_total` | counter | `result` | scoring | Days scored (`ok`/`error`; `no_consent`: skipped, consent withdrawn, D78), from events and the 15-minute tick. |
| `ryft_scoring_duration_seconds` | histogram | `result` | scoring | Time to score one day: load, compute, write (including its strength sessions). |
| `ryft_days_rescored_total` | counter | `reason` | scoring | Stored days scored again beyond the days an event or the timer asked for, because their history changed (they are in `ryft_days_scored_total` too). `history`: scoring a day wrote a new row or changed one of the inputs the next 30 scored days are computed from (HRV, resting HR, respiratory rate, strain, sleep need and actual, sleep times, naps, whether steps and active energy are there), or a retried event re-scored its days, so those days were scored again in the same pass, in order, never past today: a backfill that arrived newest first, a late sample, a recompute, a retry. At most 400 per event; hitting that logs `rescore after history change capped`. `behind`: the 15-minute pass found days (within the last 400) whose `baseline_days` (or its HRV or resting-HR count) is below what their stored history now holds, or whose HRV or resting-HR baseline is missing although the history has such nights, and scored them again (about 50 days a pass, the days their re-score reaches included, which count as `history`): what the in-pass re-score could not see (two consumers at once, the cap, rows from before it existed). It compares counts and missing baselines, not values. After a deploy `behind` works off old rows, then stays near 0; a steady rate means the re-score misses something. |
| `ryft_sleep_base_learned_total` | counter | `outcome` | scoring | Weekly sleep-baseline estimates (Sundays from 04:00 local, and `scoring learn-baselines`): `applied` (the baseline in use moved, the last 7 days were rescored), `kept` (same value, or set by hand), `insufficient` (fewer than 14 nights or 7 qualifying), `error`. A run of `error`s means the users' days or the baseline write failed; `applied` should be rare after the first weeks. |
| `ryft_journal_saves_total` | counter | — | api | Journal days saved (`PUT /v1/journal/{day}`). |
| `ryft_behavior_impact_seconds` | histogram | `caller` | api | CPU time of the behavior-impact computation: `caller="impact"` (`GET /v1/behaviors/impact`) or `insight` (yesterday's behaviors for the card). Its p95 passing ~0.1 s is the signal to move it into a worker (D76). |
| `ryft_flag_evaluations_total` | counter | `key`, `result` | api | Feature flags evaluated for `GET /v1/config`, `on` or `off` per flag key. |
| `ryft_admin_requests_total` | counter | `result` | api | `/v1/admin` requests: `ok`, `invalid`, `error` (a failed handler, and the `503` of a token that cannot be checked now: the api could not complete a key fetch or the allow-list check, which is not by itself an outage, since anyone can send an unknown key id inside the refresh limit; the log and the access line's `code` say `jwks_unavailable`, `jwks_refresh_limited` or `account_check`, and the last counts as `internal` in `ryft_auth_failures_total`), `unauthorized` (no bearer, or one that is neither the key nor an allow-listed operator's token: one `401` for every reason, the reason is only in the log), `mfa_required` (a real operator without a second factor of the last 12 hours: `403`), `forbidden` (an operator's token on a key-only route: `403`). The series exist at 0 from start-up. |
| `ryft_admin_authorized_total` | counter | `actor` | api | `/v1/admin` requests that passed the bearer check, by `actor`: `key` (scripts, smoke) or `user` (the operator's page with a Supabase token). No label holds an id: who acted is in `GET /v1/admin/audit` (`actor`), and `admin_actor` (`key:3fa1c2d9`, `user:1b2c3d4e`, the first 8 hex digits) is on the admin log lines. |
| `ryft_gate_refusals_total` | counter | `code`, `mode`, `service` | api, ingest, scoring | Requests a gate would refuse: `invite_required` (the beta gate, D79) or `consent_required` (a sync or, `service="api"`, a training import, or in scoring a day, without consent, D78, D129). `mode="on"`: refused with `403`; `mode="warn"`: served with `X-RYFT-Gate` (D87). After a policy bump, `consent_required` rises until the testers open the app. The training import's consent refusals count as `mode="on"` also under `CONSENT_ENFORCE=warn`: on that route warn acts as on (D141). (Was `ryft_beta_denied_total` before `61db1c4`, 2026-10-20.) |
| `ryft_app_attest_refusals_total` | counter | `reason`, `service` | api, ingest | Requests without a valid app token (`X-RYFT-App-Token`, D245, D247) under `APP_ATTEST=warn` (served unchanged) or `on` (`403 app_attest_required`), one per warn line `app attest` (**Logs → App Attest** below): `reason` `missing` (no header), `invalid` (not a token of this platform's ring: a bad MAC, an unknown kid, over 512 characters, issued more than 60 s ahead), `expired`, `other_account` (another account's token); `service` `api` or `ingest`. Nothing under `off`, and nothing for the exempt routes and accounts. No label holds a route, a build or an account: the warn line has the route group and the build. On Cloud Run nothing reads this registry (D236): the warn lines are the record of the `warn` week. |
| `ryft_consents_total` | counter | `action` | api | `granted`, `revoked`, `stale` (a POST for an outdated policy version: the app shows an old text). |
| `ryft_invite_redemptions_total` | counter | `result` | api | `POST /v1/me/invite`: `ok` (a use spent), `already` (the account had access), `invalid` (every refusal, a revoked code and a bound code for another email included: one label on purpose, D159). Many `invalid` from one place is someone guessing (5 an hour per user). The `503` for a bound code when Supabase cannot be asked is not counted here: it is the warn line `invite email lookup failed`. |
| `ryft_invite_binding_available` | gauge | none | api | 1 while email-bound invites work: the Supabase project confirms emails (`mailer_autoconfirm` false in its public auth settings) and the api holds the Supabase admin key. 0 while *Confirm email* is off (an address then counts as confirmed without a mailbox), the setting cannot be read, or there is no admin key: bound codes are neither created nor redeemed (D159). The log line `invite binding` says which state on every change |
| `ryft_invite_settings_checks_total` | counter | `result` | api | Reads of that setting, a step of every api tick plus one lazy read on the first bound invite (none at start, D219; until a read succeeds, bound codes are refused): `ok`, `autoconfirm` (Confirm email is off: turn it on, OPERATIONS → Beta), `error` (unreadable, treated like off) |
| `ryft_invite_email_purge_total` | counter | `result` | api | Passes of the job that clears a bound invite email and its note, and an unbound code's note, 30 days after the code stopped being redeemable (D161, D249; the log line `invite emails purged`'s `n` counts both kinds of code): `ok` (also a pass that cleared nothing), `error`. Created at 0 at start. `InviteEmailPurgeFailing` watches `error` |
| `ryft_events_dlq_total` | counter | `subject`, `result` | scoring, notify | Bad events a consumer dead-lettered itself (data that does not decode, or a `ryft-key` that is not the push path's) into its own topic, `ryft-dlq-scoring` or `ryft-dlq-notify`, with `ryft-error` (D218). `subject` is the push path's key (on `/push/wait` the attribute's key when it is one of notify's, else `unknown`); the attribute a publisher sets never becomes a label, so the values are the topology's keys and `unknown`. `result`: `ok`, or `error` (the publish failed; the push was nacked, and Pub/Sub's dead-letter policy forwards it after 20 attempts; `DeadLetterPublishFailing`). Events that fail all their attempts are dead-lettered by Pub/Sub and not counted here: they are `ryft_events_handled_total{result="failed"}`. |
| `ryft_sync_to_score_seconds` | histogram | — | scoring | From ingest accepting a sync to scoring finishing one of the days it queued (`score_queue`, D91; an import's days too: they share the queue, so a large import shows as a short rise, and `expected_within_sec`, the queue's p95 of the last 6 hours, rises with it, D141). The p95 is what testers wait for their scores; `SyncResponse.scoring.expected_within_sec` tells the app the same from the table. |
| `ryft_event_handler_panics_total` | counter | `subject` | scoring, notify | Event handlers that panicked: the message is retried and, at its last delivery, dead-lettered, instead of the service crashing (D92). Any is a bug. |
| `ryft_outbox_pending` | gauge | `publisher` | ingest, api, scoring, notify | Due rows only: outbox rows whose `available_at` has passed and that Pub/Sub has not accepted yet. A deferral waiting for its time (notify's quiet hours, D219) does not count. Set by each tick's outbox sweep. Normally 0: the request flushes its rows before it answers. |
| `ryft_outbox_oldest_seconds` | gauge | `publisher` | ingest, api, scoring, notify | Due rows only: how long the oldest due row has waited, counted from when it became due (the later of `created_at` and `available_at`), so a deferral that just came due after a night of quiet hours is no backlog; 0 when none waits. Set by each tick's sweep. Above a tick or two: the flush after the request and the sweeps fail, Pub/Sub refuses or cannot be reached (`OutboxBacklog`). |
| `ryft_outbox_stuck` | gauge | `publisher` | ingest, api, scoring, notify | Rows set aside, due or not (OPERATIONS → The outbox): after 50 failed sweeps (only the tick's sweep counts an attempt, so about 12 hours of an outage; a flush records the error only), or at once when the transport rejected the row for good (`events.Rejected`: a key no topic takes, an ordered event without a user, Pub/Sub's 400, 403, 404 or 413; `last_error` begins `rejected, set aside:`). |
| `ryft_outbox_relayed_total` | counter | `publisher`, `result` | ingest, api, scoring, notify | Rows the relay sent, by an inline flush or the tick's sweep: `ok` (Pub/Sub accepted it; the row is marked) or `error` (left for the next flush or sweep, or set aside when rejected for good). |
| `ryft_db_up` | gauge | `service` | all | 1 when the service's last database ping succeeded, 0 when it failed. Set by each tick (its first step, every 15 minutes), not by a timer: there is no `db.Watch` any more (D219). An instance that never ran a tick has no value. |
| `ryft_db_ping_failures_total` | counter | `service` | all | Failed pings of the tick's db step: Supabase or its pooler unreachable, or the role refused. |
| `ryft_ticks_total` | counter | `service`, `result` | all | `POST /internal/tick` handled after authentication (D219), one per tick: `ok` (every step succeeded), `error` (a step failed or panicked, or the budget ran out before a step began; the tick still answers `204`), `overlap` (a tick was still running in this instance: answered `204` at once). Counted before the tick's metrics push, so the push carries it. A token refused on the tick counts in `ryft_auth_failures_total`, not here. `TickMissing` reads it. |
| `ryft_users_by_last_sync` | gauge | `age` | scoring | Accounts by the age of their newest device sync (every 15 min): `under_6h`, `6h_to_24h`, `1d_to_3d`, `over_3d`, `never`. Counts only, no user ids. |
| `ryft_feedback_received_total` | counter | `category` | api | Reports stored by `POST /v1/feedback` (`bug`, `idea`, `data`, `other`). Until D236 the alert on any increase was how the operator heard about them (D83); nothing reads this counter now. Since D244 the sixth condition counts the api's `feedback received` line instead (`ryft_notices`, OPERATIONS → GCP → 14a.8). Read them with `scripts/beta_report.sh feedback` or `/admin`. |
| `ryft_push_notifications_total` | counter | `category`, `result` | notify | One per notification decided: `category` `MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE` or `REFRESH` (the silent push); `result` `sent`, `failed` (no device reached), `muted` (category off), `quiet` (dropped in quiet hours), `deferred` (morning push held until they end), `duplicate` (already sent that day), `throttled` (a silent refresh within 5 minutes of the last), `stale` (old day or session, deleted user), `import` (`SESSION_SCORED` only: an imported session, never announced, D125; scoring sends none for an import, so this counts only a re-complete of an imported session), `no_token` (no registered device), `unconfigured` (no APNs key: counted, not sent). An import's days end in one `REFRESH` once scored, like a sync's. |
| `ryft_apns_responses_total` | counter | `push_type`, `status` | notify | APNs answers per device request: `alert` or `background`, HTTP status (`200`, `400`, `403`, `410`, `429`, `5xx`) or `error` (no answer). |
| `ryft_push_tokens_pruned_total` | counter | — | notify | Device tokens dropped because APNs said they are gone. |
| `ryft_train_sessions_completed_total` | counter | — | api | Strength sessions completed (the first `POST /v1/sessions/{id}/complete` of each). |
| `ryft_train_sets_logged_total` | counter | — | api | Completed sets `PUT /v1/sessions/{id}` stored for the first time. |
| `ryft_train_prs_total` | counter | `kind` | api | Personal records a session took over (`e1rm`, `max_weight`, `max_reps`, `max_volume`). |
| `ryft_train_imports_total` | counter | `result` | api | `POST /v1/train/import` requests past authentication (D124): `ok` (`200`, also when every item was rejected), `invalid` (`400`/`413`/`422` or `403 consent_required`: nothing processed), `disabled` (`403 import_not_enabled`: the flag `train.import` is off for the account; nothing read), `busy` (`429 import_in_progress`: another import of the account holds the lease, D132), `timeout` (`503 "request timed out"`: the items ran into the 50 s budget, or the client went away), `error` (`503`: the database failed). After `timeout` and `error` the stored items stay stored and are finished (queue and event, then records); the app resends and gets them back `unchanged`. A steady `busy` for one account means a lease left by a pod that died (it expires after 70 s). |
| `ryft_train_import_items_total` | counter | `kind`, `status` | api | Items of the import: `kind` `plan` or `session`, `status` `created`, `unchanged` (identical, nothing written), `updated` (replaced whole), `rejected` (the `code` is in the response, not here). Mostly `unchanged` means the app resends files it already sent. |
| `ryft_train_import_seconds` | histogram | — | api | Duration of an import that took the lease: the items, the records and the queueing. The budget is 50 s (the items stop 10 s before it so the finish fits); p95 above ~20 s means the per-item reads (D131's note) need batching. |
| `ryft_connect_pushes_total` | counter | `provider`, `kind`, `outcome` | ingest | Requests to the hook `POST /connect/{provider}/push` of a served provider (D257, design §7.2): `accepted`, `rejected` (the provider's verification refused it, `401`, or could not run, `503`), `too_large` (`413`), `bad_body` (`400`), `error` (the database, `503`), `busy` (`503` with `Retry-After`: the instance's 2 hook slots taken, nothing read), `timeout` (`408`: no whole body within 10 s, nothing stored). On Cloud Run the log-based `ryft_connect_pushes` counts the same line (`connect push`). |
| `ryft_connect_push_items_total` | counter | `provider`, `kind`, `outcome` | ingest | The hook's items (`kind` `sleep`, `hrv`, `deregistration`, `permissions`): `stored` (new inbox row), `updated` (changed payload, version not lower: back to pending), `duplicate` (same digest), `stale` (lower version), `unknown_user` (no live connection, a pending grant included), `no_consent` (consent withdrawn, or older than ingest's cutoff `CONNECT_MIN_POLICY_VERSION`, empty = the policy in force, D259; OPERATIONS → Direct connections → Moving the consent cutoff), `id_collision` (the record id is another account's row: nothing written, an adapter bug, also an ERROR line), `over_cap` (the account already has 500 pending rows), `bad_item`. Only `stored` and `updated` write a row. Every outcome answers `200`. |
| `ryft_connect_inbox_processed_total` | counter | `provider`, `kind`, `outcome` | ingest | The consumer of `inbox.received` (`/push/inbox.received`): `processed` (applied), `no_consent` (as the hook's, D259), `no_connection`, `stale` (dropped, payload deleted), `failed` (a record that does not parse, or 20 attempts), `retry` (a transient error: `500`, Pub/Sub retries). One event can store its row and up to 24 more pending sleep and HRV rows of the account (25 records, 1,500 rows, one transaction, one `ingested` event for all their days): each row a batch took counts `processed` once, and its own later event changes nothing. |
| `ryft_connect_inbox_oldest_seconds` | gauge | `state` | ingest | Age of the oldest inbox row in `pending` and in `failed`, set by the tick step `connect inbox`. `failed` above 0 is the notice `connect inbox failed` (once a day). |
| `ryft_connect_inbox_purged_total` | counter | `state` | ingest | Inbox rows the tick deleted: `processed` and `dropped` 30 days, `failed` 7 days after they left `pending`. |
| `ryft_connect_connections` | gauge | `provider`, `status` | api | Connections by `status` (a closed set, design §10), set by the tick step `connect gauges`. `needs_reauth` is a user the app asks to reconnect. |
| `ryft_connect_capacity_ratio` | gauge | `provider` | api | Counted links over `CONNECT_<PROVIDER>_USER_CAP` (design §6.1). 0.8 or more is the notice `connect capacity`. |
| `ryft_connect_stale_connections` | gauge | `provider` | api | `connected` connections, connected over 36 hours ago, that stored nothing for 36 hours. Above 0 is the notice `connect no data`. |
| `ryft_connect_starts_total` | counter | `provider`, `outcome` | api | `POST /v1/me/connections/{provider}/start`: `ok`, `consent_required`, `connection_limit` (the cap), `unknown_provider`. |
| `ryft_connect_callbacks_total` | counter | `provider`, `status` | api | `GET /v1/connect/{provider}/callback`, by the status it redirects with: `ok`, `denied`, `expired`, `error` (the line `connect callback` has the closed `reason`). |
| `ryft_connect_confirms_total` | counter | `provider`, `outcome` | api | `POST …/confirm`: `ok`, `repeat`, `invalid` (another account's or an unknown ticket: many is someone's authorize URL sent around), `expired`, `replaced` (another provider account replaced the connection), `relinked` (the provider account moved from another RYFT account). |
| `ryft_connect_token_refreshes_total` | counter | `provider`, `outcome` | api | The tick step `connect refresh`: `ok`, `needs_reauth` (the grant is gone: the app asks to reconnect), `error` (transient, backed off; also a refreshed grant that could not be saved within its own 5 s, detached from the step's deadline, with the ERROR line `connect refresh: the refreshed grant was not saved`). |
| `ryft_connect_deregistrations_total` | counter | `provider`, `reason`, `outcome` | api | Deregistrations at the provider, inline and from the queue: `ok`, `gone` (the provider no longer knew the grant), `retry`, `given_up` (30 days; the step `connect deregister` gives up on those first, before it calls the provider), `skipped_live` (a live connection holds that provider account: nothing called, the tokens deleted, design §6.3). `reason` is the queue's (`disconnect`, `account_deleted`, `confirm_expired`, `cap`, `missing_permission`, `replaced`). |
| `go_*`, `process_*` | | | all | Go runtime and process collectors; part of what a push would carry (about 50 series per instance). |

## Alerts

**RYFT on Cloud Run** alerts from Cloud Monitoring only: the six policies of `infra/gcp/alerts.tf` (the sixth since D244)
(**Cloud Monitoring → The alert policies**, above; first looks in OPERATIONS → GCP → When an alert
fires). The app rules that stood here (Grafana Cloud's ruler, on the OTLP push) were never loaded
on Cloud Run and went with D236, and so did Grafana's five rules on Cloud Monitoring
(`grafana-reader`); what each paged on and what covers it now: **Cloud Monitoring → What D236
dropped**. The EKS cluster's rule groups (admission, Vault, the broker, Argo CD) went with the
cluster (D3, D246), their files and checks with them; the groups last loaded into the stack's ruler
go with the stack (OPERATIONS → GCP → 11.7). Their expressions are in git before D236 and D3.

## Baselines

Recorded numbers a later change is compared with. **A laptop is not production**: these come from the
compose stack on one machine, Postgres in a container on a local disk, no network between the services, no
Supabase pooler, no TLS at the edge, one replica of everything. Use them for "did this change get slower
than it was", never as a capacity statement or an SLO.

### Local load (`make load`, `scripts/load.sh`)

What it does: k6 (`grafana/k6:2.3.0`, pinned by digest in the script, run by `$CONTAINER` on the compose network)
drives 50 synthetic users. Each consents, syncs **one full UTC day** (about 1 250 records: heart rate every minute from
waking to midnight, steps and active energy in 10-minute buckets with the phone's 30-minute steps beside them, a night
of sleep with stages, overnight HRV, respiration, SpO2, resting HR, one run; the shape of `scripts/synth.py`, generated
from a seeded PRNG so the bytes are the same every run) and then reads `GET /v1/days?from=…&to=…` over 30 days 20 times
with 250 ms think time. The users start 100 ms apart. One warm-up run, then 3 measured runs, each with fresh accounts;
the script waits until scoring has finished (the `ryft_sync_to_score_seconds` count stops rising) before it scrapes
`/metrics` of ingest, api and scoring, which are not published to the host, over the compose network.
`make up && make load`; it prints the table, writes `scripts/load/out/<UTC timestamp>/` (ignored by version control;
`baseline.json` has every run and the median run; `out/latest` names the newest; nothing is deleted, and `LOAD_OUT` is
refused if it is `/`, `$HOME` or a non-empty directory without the `.ryft-load-out` marker) and exits 1 when any
route's error rate is above 1 % (`LOAD_MAX_ERROR_RATE`) or an optional k6 threshold (`LOAD_P95_SYNC_MS`,
`LOAD_P95_DAYS_MS`) failed. In local mode `INGEST_URL` and `API_URL` are only the readiness probes from the host; k6
talks to the services over the compose network. A plain URL on localhost is required: `LOAD_ALLOW_REMOTE=1` allows
other plain hosts (with `LOAD_REMOTE_API_KEY`, never the ambient `API_KEY`), but never `*.ryftlab.de` or
`*.supabase.co`; never aim it at production. Each invocation leaves 200 accounts and about 250 000 samples in the dev
database (`make down` clears it).

How 50 users exist locally: `AUTH_MODE=apikey` accepts the bearer `<API_KEY>:<uuid>` as that user
(`internal/auth/apikey.go`), so one key is enough and no auth code changes.

Rate limits (`internal/ratelimit`, per user): `POST /v1/sync` 2/s with bursts of 60, everything else 10/s with bursts
of 60. The test does one sync per user and 4 reads/s per user, so it stays under both: client-side 429s 0 and
`ryft_rate_limited_total` 0 in every run. No override was needed or set.

Metric: `ryft_sync_to_score_seconds` (histogram, scoring; buckets 1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800 s) is
taken as the difference of the buckets before and after each run and read like `histogram_quantile`. The buckets
are coarse: a value under 1 s is interpolated inside the first bucket (read it as "under a second"), and the 2.4, 4.0,
4.4 and 4.6 s values below are linear interpolation inside the (2, 5] s bucket: read them as "in the 2 to 5 s bucket".
Each run observes 102 days (about two queued days per user).

| | |
| --- | --- |
| Date | 2026-11-04 |
| Commit | `fb7b971` (main; the load tooling is scripts only) |
| Machine | MacBook, Apple M4 Pro, 12 cores, 48 GB, macOS 27.2; podman 6.1.4, libkrun VM of 4 vCPU and 7.9 GiB; Postgres 17-alpine and RabbitMQ 4.3.6 in that VM, volumes on the VM disk |
| Other load | another project's throwaway Postgres ran in the same VM (about 13 % of a vCPU); the machine was otherwise in ordinary use |
| Stack | `make up` with the default compose file; single replica of ingest, api, scoring, notify |

Three measured runs (invocation 1; ms unless noted; sync→score in s from the buckets). Warm-up run before them:
sync p50 222 / p95 465 ms.

| Run | `POST /v1/sync` p50 / p95 / p99 | `GET /v1/days` p50 / p95 / p99 | errors | sync→score p50 / p95 |
| --- | --- | --- | --- | --- |
| 1 | 98.9 / 1 252 / 1 413 | 18.4 / 167 / 254 | 0 % | 0.5 / 0.9 s |
| 2 | 1 573 / 4 564 / 4 607 | 16.8 / 422 / 986 | 0 % | 0.8 / 4.0 s |
| 3 (median) | 520 / 4 275 / 4 486 | 16.5 / 221 / 652 | 0 % | 0.8 / 2.4 s |

**Recorded baseline = the median run (run 3, by sync p95)**: sync p50 520 ms, p95 4.3 s, p99 4.5 s; days p50 16.5 ms,
p95 221 ms, p99 652 ms; error rate 0 %; `ryft_sync_to_score_seconds` p95 2.4 s.

The spread is wide and honest: the sync p95 over the three runs is 1.3 to 4.6 s, and a second invocation straight
after (a fresh set of accounts, same stack) gave sync p95 of 4.4 / 0.23 / 2.5 s, days p95 of 97 / 26 / 66 ms and
sync→score p95 of 4.4 / 0.9 / 4.6 s. The runs are bimodal: either every upload takes about 100 to 250 ms (p95 under
0.5 s) or the burst of 50 queues behind Postgres (WAL and fsync on the VM disk, the shared VM) and takes 2 to 4 s. The
days p50 alone moved between 7 and 18 ms. **Nothing here is stable enough to gate on except the error rate.** The later
nightly job therefore gates only on the error rate (one failed sync of 50 is 2 %, above the 1 % limit); latency is
reported, not gated, until a quieter runner exists. With n = 50 syncs per run the p95 sits between the 3rd and 2nd
slowest upload and the p99 is about the maximum, so they are single events, not distributions. `GET /v1/days` reads
one scored day each (the users have a single synced day), lighter than a tester with months of history.

A check of the revised script on a quieter machine (no other database in the VM) gave sync p95 of 83 and 73 ms and days
p95 of 8 and 6 ms over two measured runs, an order of magnitude below the table: the same code, so the table mostly
measures the machine's state that day.

The table was measured with the first version of the tooling (tree of `fb7b971` plus the first commit of the load
scripts); the verdict, threshold, output-directory and drain logic changed afterwards (review fixes).

## Logs

One JSON object per line on stdout, written by `internal/logx`. `LOG_LEVEL`
(`debug`, `info` — default —, `warn`, `error`) sets the level; on Cloud Run it is set in `deploy/run/config.env`. On Cloud Run (`K_SERVICE` or `CLOUD_RUN_JOB` set)
the level and the message are written as Cloud Logging's `severity` (`DEBUG`, `INFO`, `WARNING`, `ERROR`) and
`message`, and a request's lines carry `logging.googleapis.com/trace`, which joins them to Cloud Run's request
log; every other key is as below, and the messages and keys in this section are the same in both formats.

| Field | On | Meaning |
| --- | --- | --- |
| `time`, `level`, `msg`, `service` | every line (`severity` and `message` for `level` and `msg` on Cloud Run) | `service` is `ingest`, `scoring`, `api` or `notify`. |
| `request_id` | every line logged while serving a request; scoring lines for an event | The ID the server gave the request (UUIDv7, also the `X-Request-ID` response header and `request_id` in error bodies). Scoring logs the ID of the sync or recompute request that caused the event, so one ID follows a sync from ingest through scoring. |
| `client_request_id` | same | A client-sent `X-Request-ID` (if it looks like an ID). Logged only, never used as ours. |
| `user_id` | once the token is verified; scoring per event and per user in the 15-minute pass | The Supabase user ID (`sub`). |
| `route` | request lines once routed | ServeMux pattern, e.g. `GET /v1/days/{day}`. |
| `method`, `path`, `status`, `bytes`, `dur_ms` | the access line (`msg="http"`, one per request) | `status` `499`: the client gave up before the answer (logged at info, not an error). The `path` of `DELETE /v1/admin/invites/{code}` is that pattern, not the code (D160). |
| `code` | the access line of a refused request | The error code (`invite_required`, `consent_required`, `import_in_progress`, `app_attest_required`, …) or, for a `401`/auth `503`, the reason (`expired`, `signature`, `deleted`, …). |
| `result`, `plans`, `sessions`, `created`, `unchanged`, `updated`, `rejected`, `days`, `duration_ms` | the api's `train import`, one per import that took the lease | Its result as in `ryft_train_imports_total`, the items sent and what happened to them (counts over plans and sessions), the days queued. Never a title, a note, a value or an id list (D114, D117; `cmd/api/train_import_logs_test.go`). |
| `gate` | the access line of a request a gate in `warn` mode let through | The code(s) it would have been refused with, as in `X-RYFT-Gate`. |
| `picked`, `xff_index`, `xff_entries` | api's and ingest's `client address`, at most one a minute per instance, for a request with `X-Forwarded-For` | Which entry `httpx.ClientKeys` keyed the per-address limits by (the edge's and `/v1/admin`'s, D240): `picked` `xff` (with `xff_index`, its position from the left, from 0) or `fallback` (no entry would do: the shared key), and how many entries the header had. No address, whole or in part, no request ID, user or route (D222; runbook step 9 reads it). |
| `err` | errors and warnings | The internal error. Clients only ever see `"internal error"` and the request ID. |
| `day`, `model_version`, `inputs_changed` | scoring's `scored day`, one per day scored | The day, the model that scored it, and whether its history inputs changed. The days after it are scored again when they did or when the event was a retry (`inputs_changed` is then often `false`: the first attempt wrote the change): `rescored days after a history change`. No score, input or state: those are in `daily_scores` and the admin view. |

**Lifecycle lines.** A service started through `internal/app` (scoring and notify; api and
ingest follow) logs its start and stop with these lines and keys (the closed sets are
pinned by `internal/app/logs_test.go`, D114, D117). OPERATIONS → GCP → A deploy: what the old
pod logs has the `gcloud logging read` queries.

| Message | Keys | When |
| --- | --- | --- |
| `listening` | `addr` | the HTTP port is open (the service listens before it opens the pool: Health paths above) |
| `metrics listening` | `addr` | the `/metrics` port is open; only where there is one (compose), not on Cloud Run |
| `events` | `transport` (`pubsub`), `project`, `endpoint`, `push_auth` (`google_oidc`, `insecure`) | the transport is set up: where it publishes, how it checks push and tick tokens |
| `push and tick tokens are NOT checked (ALLOW_INSECURE_PUSH=true): local development only` | none | compose only; refused on Cloud Run |
| `config`, `beta config`, `app attest config`, `apns config`, `apns`, `db`, `events`, `build`, `serve` | `err` | the start failed in that area; the process exits 1. `serve`: the HTTP port would not bind (it used to be logged while the service ran on without probes). `db` on Cloud Run with a refused login (SQLSTATE 28, `ECIRCUITBREAKER`): the instance stays up and holds instead (D221), and the line comes again for each refused retry |
| `db: the login passed on a retry` | none | a tick's login retry passed while the start held: the gate opens and that tick is served (D221) |
| `start interrupted by the signal` | `step` (`config`, `db`, `events`, `build`, `serve`) | a SIGTERM before the start was done |
| `serve` | `task` (`http`), `err` | the HTTP server returned an error before the shutdown: it stops the service (exit 1). The consumer and timer tiers (`consume`, `job`, `background`) are gone: events are pushes and timers are the tick (D218, D219) |
| `metrics server` | `err` | the `/metrics` port could not be bound or its server failed (compose); the service runs on without it |
| `panic` | `task` (a server), `check` (a ready check: `db`, `auth`) or `step` (a tick step), `panic`, `stack` | a server's panic: counted in `ryft_goroutine_panics_total`, the service shuts down in order and exits 2. A check's: counted there too, the service runs on. A tick step's: counted in `ryft_http_panics_total` (`route="POST /internal/tick"`), the tick goes on with its next step |
| `tick` | `result` (`ok`, `error`), `message_id`, `took` | one per tick handled (`ryft_ticks_total`). On Cloud Run the log-based metric `ryft_ticks` counts these lines by `result` alone (D236; policy 3, Tick missing): renaming the message or the key, or changing its values, is a change to `infra/gcp/monitoring.tf` too |
| `tick: step failed` | `step`, `err`, `took` | a step returned an error or panicked; the tick goes on. `step` is a closed set: `db ping`, `outbox sweep`, `scoring pass`, `stale check`, `account reconcile`, `invite settings`, `invite email purge`, `app attest purge`, `finally`; api's `connect purge`, `connect gauges` (8 s each), `connect refresh`, `connect deregister` (12 s each; in that order, last in the tick: a step past its own deadline fails with `the step ran out of its <d>, the next tick goes on` and the next one starts) and ingest's `connect inbox` |
| `tick: the budget is spent, step skipped` | `step`, `budget` | the tick's budget (45 s; scoring 240 s) ended before that step began |
| `tick: the final step failed` | `err` | the metrics push at the end of the tick failed: `otlp push: HTTP <status>: …` or the transport's error (only with the push configured, which nothing committed does: The OTLP push (dormant) above) |
| `tick: the previous one still runs, skipping this one` | `message_id` | a tick arrived while one runs in this instance (`result="overlap"`) |
| `tick: not a push envelope`, `tick: not a tick` | `status` (`400`, `413`, `415`); `message_id`, `key` (cut) | a request to `/internal/tick` with a valid token that is not Pub/Sub's tick message: not an envelope, or a `ryft-key` other than `tick` (`400`) |
| `metrics push: points rejected` | `rejected`, `err` | warn: the gateway accepted a push but refused some of its points (out of order, too old, a limit; only with the push configured) |
| `shutting down` | `reason` (`signal`, `task`, `start`), `task`, `err` | the shutdown began; `err` is one line, cut |
| `shutdown step` | `step` (`drain`, `http`, `pool`, `metrics`), `dur_ms`, `err` | one per step, at info; at warn with `err` when it failed or overran its bound. The `consumers`, `jobs`, `relay` and `broker` steps are gone with their tiers (D219) |
| `shut down` | `dur_ms`, `exit` (0, 1, 2) | the shutdown is over; `exit` is the process's exit code |
| `shutdown overdue` | `step` (also `begin`), `elapsed_ms` | the watchdog: the shutdown passed `Grace` less 4 s, the process exits at once (1, or 2 after a panic) |
| `task ended with error during shutdown` | `task`, `err` | a task returned an error after the shutdown began (warn, not a failure) |

**App Attest** (D245, D247; api and ingest; pinned by `internal/gate/appattest_test.go` and
`cmd/api/appattest_test.go`). On Cloud Run nothing reads `ryft_app_attest_refusals_total`
(D236): the warn line `app attest` is the record of the `warn` week, read with `gcloud logging read`.

| Message | Keys | When |
| --- | --- | --- |
| `app attest` (info, at the start) | `mode` (`off`, `warn`, `on`), `ring` (bool: `APP_ATTEST_TOKEN_KEYS` is set), `exempt` (how many `APP_ATTEST_EXEMPT_USERS`) | api and ingest, once the configuration is read. Never a key, a key id or an account |
| `app attest` (warn) | `code` (`app_attest_required`), `reason` (`missing`, `invalid`, `expired`, `other_account`), `mode` (`warn`, `on`), `route_group` (the first path segment after `/v1/`: `days`, `sync`, `train`, …), `method`, `app_build` (`X-RYFT-App-Build` when it is digits and dots, at most 24 characters, else empty), `app_device` (bool: `X-RYFT-Device-ID` was sent), `request_id` | one per request the token check refuses, beside one count of the metric; `mode="warn"` served it, `mode="on"` answered `403`. The line is written outside the request's log context: it has **no `user_id`**, no `route` and no `client_request_id`, and never the token, a key id or the device id's value. Its `request_id` joins it to the access line, which has the account |
| `app attest challenge refused` | none (the line's `route` says which route) | api: `POST /v1/app-attest/keys` or `…/token` with a challenge that is unknown, used, expired or another account's (`422 app_attest_challenge_invalid`) |
| `app attest attestation refused`, `app attest assertion refused` | `step` (`cbor`, `fmt`, `x5c`, `chain`, `nonce`, `key_id`, `credential_id`, `rp_id`, `counter`, `aaguid`, `signature`; for an assertion also `counter` when the stored counter moved past it first) | api: the verifier refused the attestation or the assertion (`422 app_attest_invalid`). Never a byte of the input (AA4) |
| `app attest key unknown` | none | api: `POST /v1/app-attest/token` for a key id this account never attested (`404 app_attest_key_unknown`) |
| `app attest key attested` | none | api: a key was attested and stored (`201`) |
| `app attest purged` | `challenges`, `keys` | the tick's step `app attest purge` removed expired challenges or keys unused for 90 days (D245); only when it removed any |
| `app attest purge failed` | `err` | warn: that step's query failed; the step reports it as `tick: step failed` too |

**Direct connections** (D257; api and ingest). The lines the signals above count, and the ones the
runbooks read (OPERATIONS → Direct connections). Never a token, a state, a code, a ticket, a provider
user id, a record id, a payload or an address; the callback's access line has no query (a `logcheck`
test).

| Message | Keys | When |
| --- | --- | --- |
| `connect push` | `provider`, `kind`, `outcome` (`accepted`, `rejected`, `too_large`, `bad_body`, `error`, `busy`, `timeout`), the item counts per outcome | ingest, one per hook request to a served provider: the log-based `ryft_connect_pushes` |
| `connect record id collision` (ERROR) | `provider`, `kind` | ingest: an item's record id is another account's row (`id_collision`): an adapter bug; `ryft_errors` counts it |
| `connect callback` | `provider`, `status`, `reason` (`ok`, `access_denied`, `missing_permission`, `state_unknown`, `state_expired`, `exchange_failed`, `no_account`, `no_consent`, `cap`, `provider_unknown`), `user_id` when the state resolved one | api, one per callback |
| `connect capacity`, `connect no data`, `connect deregistration stuck` | counts and providers only, never an id | api's tick step `connect gauges`, at most once a day (06:00–06:14 UTC) while it holds: `ryft_notices`, policy 6 |
| `connect inbox failed` | counts only, never an id | ingest's tick step `connect inbox`, the same rule |
| `connect inbox` | `provider`, `kind`, `outcome` (`processed`, `no_consent`, `no_connection`, `stale`, `failed`), `days`, `code` | ingest's consumer, one per event it settles |
| `connect inbox batch` | `provider`, `records` | ingest's consumer stored the event's row with other pending rows of the account in one transaction (at most 25 records, 1,500 rows) and one `ingested` event |
| `connect inbox batch failed, applying the row alone` (WARN) | `provider`, `kind`, `records` | the batch's transaction failed; the event's row is applied alone, the others stay `pending` for their own events |

Never logged: health values (scores, samples, sleep, workouts, baselines, the learned
sleep need, the readiness state), tokens (APNs device tokens included), the
`Authorization` header, email addresses, IP addresses (whole or as a network, D222),
device names, request bodies, feedback text.

**Google's request logs on GCP** (not ours, D242). Until the edge is built, Cloud Run's request
log (`run.googleapis.com/requests`) lands in `ryft-logs` with every caller's IP, URL and user
agent, 14 days. From `edge = "built"` on, the sink `ryft-logs` excludes api's and ingest's entries
below 5xx (their 5xx stay: the only record of a failure Cloud Run makes itself, and what the 5xx
alert's text reads; scoring's and notify's stay whole, their callers are Pub/Sub's pushes). It also
excludes the load balancer's request log, except the entries of requests Cloud Armor denied or a
preview rule flagged: those keep the client's IP, the URL and the user agent for 14 days in the EU.
What no sink takes is stored nowhere (`_Default` is off). The services' own lines (`stdout`,
`stderr`) and the log-based metrics are unchanged; the access line `http` has each request's
status. Cloud Armor logs at `NORMAL`, never `VERBOSE`
(matched body snippets). The exclusion of api's and ingest's request log below 5xx is the user's
decision (2026-11-19). The privacy text (C2) must name what stays (D242 → Privacy has the list).
OPERATIONS → Data protection (logs) has the two exceptions (a panic's stack, a pgx
encode error), the tests that pin the rule and what older lines still hold. The lines
tie `user_id` to paths with dates, so limit who can read them and keep them no longer
than you need.

**Finding a request** a user reports (the app shows or logs `request_id`): every line of it, from
all four services, is in the bucket `ryft-logs` for 14 days (its retention). Scoring's lines for
the event a sync caused carry the sync's ID too.

```sh
RID=01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c
gcloud logging read "resource.type=\"cloud_run_revision\" AND jsonPayload.request_id=\"$RID\"" \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=14d --order=asc \
  --format='value(timestamp,resource.labels.service_name,jsonPayload.message,jsonPayload.route,jsonPayload.status,jsonPayload.code,jsonPayload.err)'
```

Reading it takes a Logging read role on `ryftlab-prod`; treat every such grant as access to the
users' personal data (OPERATIONS → Data protection (logs)). The runbook is OPERATIONS → Beta →
Admin sign-in → Tracing a request.
