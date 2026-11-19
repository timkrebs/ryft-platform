# RYFT — observability

RYFT on Cloud Run has one observability: **Cloud Monitoring** (D236, which supersedes D224's
Grafana half). Nothing scrapes the services and nothing is pushed: what Google measures itself
(Cloud Run, Pub/Sub), log-based metrics counted from lines the services write, uptime
checks from outside, the dashboard "RYFT · Overview" and six alert policies (D244), all in `infra/gcp`
(**Cloud Monitoring** below). Logs are JSON (`slog`) on stdout, in Cloud Logging's bucket
`ryft-logs` for 14 days (**Logs** below). Without Cloud Run (compose) each service serves
`GET /metrics` on its own port, **9090** (`METRICS_PORT`, no auth), apart from the API and
probe port 8080. The probes are `/health/live` and `/health/ready` (**Health paths** below).
The sections on Grafana Cloud, the collector and Vault are **EKS leftovers until D3**: the
cluster's, not Cloud Run's.

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
| Log-based (D244) | `logging.googleapis.com/user/ryft_notices` (a classic filter, not PromQL) | `kind` (ours) | policy 6 |
| Load balancer (from `edge = "built"`, D242) | `loadbalancing_googleapis_com:https_request_count` (`https_lb_rule`) | `backend_target_name`, `response_code_class` (a number: `500`), `response_code` | the dashboard |
| Cloud Armor (the same) | `networksecurity_googleapis_com:https_request_count` (`network_security_policy`) | `backend_target_name`, `blocked` | the dashboard |
| Log-based (the same) | `logging_googleapis_com:user_ryft_edge_denials`, `…_preview_matches` | `priority` (ours) | the dashboard |

### The log-based metrics

Three counters (`DELTA`, `INT64`) of lines the services write, nothing pushed. D236's two needed no
Go change; D244's counts one new api line too. All three read only the services' own stream on Cloud
Run (`resource.type="cloud_run_revision"`, the `run.googleapis.com/stdout` log, and for errors
`stderr` too), never Cloud Run's request log. **No label may carry an IP, a user id, an email or a
message id**: `ryft_ticks` has one label of its own, `result`, and `ryft_notices` one, `kind`, each
a closed set (`tests/monitoring.tftest.hcl` pins both); the service is the resource's
`service_name`.

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
  key's own use (scripts, smoke) and `admin second factor required` (in the audit). The filter
  names api only. All three lines are INFO, so a `LOG_LEVEL` above `info` silences them and policy
  6. No panel (it would change the dashboard; a free follow-up).

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
| 6 | RYFT · Feedback and admin access (D244) | `user/ryft_notices`, `ALIGN_SUM` over 300 s, `REDUCE_SUM` by `metric.label.kind`, `> 0`: a feedback, an admin session begun or a refused admin bearer in the last 5 minutes, one series per kind. A classic threshold, not PromQL: one feedback is one point of a sparse `DELTA` counter, and `increase()` over one was never verified (D236). `evaluation_missing_data` `INACTIVE`: once 5 minutes pass without a line the incident closes, so the next line is a new incident and a new notification; only the opening is notified (`notification_prompts` `OPENED`) | 60 s | warning |

The subscriptions are named from the topology (`pubsub.auto.tfvars.json`), so a new consumer is in
4 and 5 without an edit. Incidents close when the query returns nothing, or 30 minutes after their
data stops; policy 6's when its data stops. Policy 6's subject and text name the kind
(`${metric.label.kind}`, a label its `group_by` keeps) and never a field of the line (no `${log.…}`
variable). A disabled channel is `checks.tf`'s `alert_channels_enabled` warning.

### What D236 dropped, and what covers it

The app rules (`deploy/grafana/alerts/ryft.yaml`, never loaded on Cloud Run) and Grafana's five
Cloud Monitoring rules went with Grafana Cloud:

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
| Policy 6 (D244; Google's pricing page through search snippets, 2026-11-19, *(search)*, not verified: alerting charged no sooner than 2027-09-01, then $0.35 a month per metric reference in a policy and $0.50 per million points returned) | 1 metric reference, at most 3 series (one per kind), usually none | **$0** until then, about **$0.35–0.45** after |
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

## Collection: Grafana Cloud

*EKS leftover until D3.* This section is the EKS cluster's collector, until D3 removes the
cluster; the services on Cloud Run are observed by Cloud Monitoring instead (above, D236), and the
scrape annotations below do not exist there.

Metrics go to Grafana Cloud (free tier; D51). The collector is Grafana's
`k8s-monitoring` chart (`deploy/monitoring/k8s-monitoring.yaml`, `make
monitoring-apply KUBE_CONTEXT=<context>`) in namespace `monitoring`: two Alloy collectors named as the
onboarding names them, `alloy-daemonset` (one per node, host network) and
`alloy-deployment` (one replica, cluster-wide work and the OTLP receiver), plus
node-exporter, kube-state-metrics, Beyla, Kepler (energy), OpenCost (cost) and
k8s-manifest-tail. Their pipelines are configured remotely in
Grafana Cloud's Fleet Management, which assigns them by collector name; the daemonset
discovers the RYFT pods through their annotations:

```yaml
prometheus.io/scrape: "true"
prometheus.io/port: "9090"
prometheus.io/path: /metrics
```

The collector runs on the nodes' network, so its scrapes come from node addresses. Each
service admits port 9090 from namespace `monitoring` (`base/<service>/networkpolicy.yaml`)
and, on EKS, from the VPC range (`overlays/eks/metrics-from-nodes.yaml`). Pods share
that range, so any pod may read `/metrics`: counters and route labels, no user data.
Port 8080 stays reachable from Traefik only.

**RYFT's pod logs stay out of Grafana Cloud.** They tie user ids to the days and records
they asked for (no health values: OPERATIONS → Data protection (logs)) and go to
CloudWatch instead (D52). The collector can read every container's log file
(`filesystem-log-reader`), so this is a Fleet Management setting, not a boundary:
exclude namespaces matching `ryft-.*` from pod-log collection. Check in Explore that
`{namespace=~"ryft-.*"}` returns nothing.

Two scrapes are the chart's own, not Fleet's (T12-e, D120; **Admission** below): the API
servers' admission metrics (job `ryft/kube-apiserver-admission`) and kube-state-metrics'
series for the admission bindings (job `ryft/admission-bindings`). They write through a
destination of their own with the `PROM_USERNAME` and `GC_TOKEN` of `ryft-secrets`, the
token the Fleet pipelines write with.

Two more are the in-cluster Vault's, RYFT's only Vault since the rebuild (D194, D207; **Vault**
under Alerts). The collector's own `GC_TOKEN` comes from that Vault too (mount `kv-monitoring`,
synced into `monitoring/ryft-secrets` by VSO), so on an empty cluster the collector is installed
after the Vault init and infra/vault's apply, with the Vault pods already running
(**From an empty cluster** below): `/v1/sys/metrics?format=prometheus`
on each server pod over TLS with the ryft-vault CA (job `ryft/vault`; no token,
`unauthenticated_metrics_access` on the listener; admitted by vault-extras' NetworkPolicy from the
`alloy-deployment` pods only, and only once `vault_collector_peer` is set after the operator's own
init: the peer reaches all of 8200, `PUT /v1/sys/init` included. The flag closes the pod-network
path only: the host-network `alloy-daemonset` connects from its node's address, not as a pod the
peer selects, and node-originated traffic very likely still reaches 8200 before the init (to
verify on the cluster); OPERATIONS → Vault covers that case: G3 and the bootstrap's first check,
"`Initialized true` before your own init: stop"), and kube-state-metrics' series
for namespace `vault` (job `ryft/vault-state`: the snapshot CronJob, the StatefulSet, the server
pods' readiness and their service-registration labels). Both run on
`alloy-deployment` and write through the same destination. Vault's own pod logs go to Grafana
Cloud through a pipeline of the chart's, off until the Loki details are in Vault (**Logs → Vault's
logs** below).

Alert rules: RYFT on Cloud Run alerts from Cloud Monitoring (D236, above). The EKS-coupled rules
left are in `eks-admission.yaml`, checked with `make alerts-check` and not loaded; see **Alerts**
below.

**The SDK injector stays out of `ryft-*`.** `telemetryServices.sdkInjector` deploys a
mutating webhook (`mpod-v1.beyla.grafana.com`, pods on CREATE, `failurePolicy: Ignore`)
that can add an SDK init container or volume to pods of every namespace it does not
exclude. The admission policies pin the images and commands of RYFT's pods, so an injected
container would be refused, and the services emit their own metrics. The chart excludes
by exact namespace name, not by prefix, so `deploy/monitoring/k8s-monitoring.yaml` lists
every `ryft-*` namespace next to the chart's own defaults (a list value replaces the
default list). A new `ryft-*` namespace is added there too. `vault` and
`vault-secrets-operator-system` are listed as well (D186): what the injector injects is steered
from Grafana Fleet, and a mutated Vault pod would run foreign code next to the unsealed keyring
(and, under `vault_seal = "awskms"`, the Pod Identity credentials for the unseal key). This
exclusion is the chart's, not Fleet's: it holds from the first install, and the webhook only
acts on pod CREATE, so Vault pods that started before the collector existed (a rebuild from
empty) were never seen by it. Scraping and the Fleet
Management log setting above are unaffected. Apply with `make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1`, then
check read-only:

```bash
kubectl get mutatingwebhookconfigurations \
  -o custom-columns='NAME:.metadata.name,NAMESPACES:.webhooks[*].namespaceSelector,OBJECTS:.webhooks[*].objectSelector'
```

The selector of the injector's webhook must list every `ryft-*` namespace, `vault` and
`vault-secrets-operator-system` under `NotIn`.

A self-hosted Prometheus works the same way: a job that keeps pods with
`prometheus.io/scrape=true` and uses the annotated port, running in namespace
`monitoring`.

### From an empty cluster

The collector's token is in Vault, so on a cluster built from nothing (the 2026-11 rebuild,
D207) the collector comes **after** Vault: the Vault pods have run, and logged, before the first
collector starts. The chart stays a Helm release of `make monitoring-apply`, outside Terraform
(D201 and D202 are decided, not built). Before `make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1`:

1. **Vault and infra/vault.** Vault is initialised and all three pods are unsealed (`1/1`), and
   infra/vault is applied. That apply creates namespace `monitoring` (Pod Security
   `privileged`), its service account and the Kubernetes auth role `monitoring`, the mount
   `kv-monitoring` with `config` = `GC_TOKEN`, `PROM_USERNAME` (from
   `service_secrets.monitoring`), and the generated overlay
   `deploy/k8s/overlays/vault-namespaces/monitoring` (Certificate `vault-ca`, a VaultConnection to
   `https://vault.vault.svc:8200`, VaultAuth, VaultStaticSecret `ryft-secrets`). No Argo CD
   application applies that overlay: `make monitoring-apply` applies it by hand first and waits
   for `monitoring/ryft-secrets`. The committed copy must be the in-cluster shape:
   `grep -c hashicorp.cloud deploy/k8s/overlays/vault-namespaces/monitoring/vault.yaml` prints
   `0`.
2. **The add-ons.** cert-manager with the ClusterIssuer `ryft-vault`, and VSO, both from
   infra/aws/kubernetes. Best also `vault_collector_peer = true` (Vault metrics (once) in
   OPERATIONS: only after your own init), or the `ryft/vault` scrape fails until it is set.
3. **The token** (below) is valid, and **Fleet's two exclusions** (below) are in place.

**The token.** `GC_TOKEN` is one Grafana Cloud access policy token (the onboarding's Kubernetes
Monitoring policy, realm: the stack), and everything in the namespace uses it:

- the remote configuration from Fleet Management: `fleet-management:read`;
- every Fleet pipeline's remote_write and the chart's own destination (admission, Vault):
  `metrics:write`;
- OpenCost reading the stack's Prometheus: `metrics:read`;
- Fleet's log and event pipelines, and `vault-logs` once it is on: `logs:write`;
- Beyla's and the OTLP receiver's traces: `traces:write` (and `profiles:write` if a Fleet
  pipeline profiles).

The policy page is authoritative (**to verify** the scope names there). Do not widen this token
for anything else: the alert rules token is separate (the Synthetic Monitoring token went with
D236). It
survived the decommission only if nobody revoked it. So before infra/vault's first apply, check
in Grafana Cloud → the stack → Access policies → the Kubernetes Monitoring policy that the token
in `service_secrets.monitoring.GC_TOKEN` is still listed (by its name; never paste the value). If
it is gone, or you cannot tell which one it is: add a token to the same policy (or recreate the
policy with the scopes above), put it into `infra/vault/terraform.tfvars` as
`service_secrets.monitoring.GC_TOKEN` (`PROM_USERNAME` stays the stack's Prometheus user), then
plan and apply infra/vault (only `kv-monitoring/config` changes). VSO rewrites `ryft-secrets` at
its next refresh (hourly) and restarts both collectors (`rolloutRestartTargets`). After an
install with a revoked token, the collectors log `401`/`403` and Fleet Management → Collectors
shows none of them.

**The three exclusions of `vault`** (D186, D197):

| Exclusion | Lives in | In force | Checked by |
| --- | --- | --- | --- |
| SDK injector webhook | this repo: `telemetryServices.sdkInjector.webhook.excludedNamespaces` | from the chart's first install. The webhook acts on pod CREATE only, so Vault pods that started before it were never seen, and every later one (each Shamir restart) is excluded | the `mutatingwebhookconfigurations` check above |
| Pod logs (`alloy-daemonset` reads the nodes' log files) | **Fleet Management**: the pod-log pipeline's namespace rule `ryft-.*\|vault` | at the collectors' first poll | Loki: `{namespace="vault", container=~".+"}` is empty |
| Beyla (eBPF; sees the request paths Vault serves) | **Fleet Management**: the Beyla pipeline's discovery exclusion (**to verify**: if this stack's Beyla takes its discovery from the chart instead, the exclusion belongs in this file; stop and raise it) | at the collectors' first poll | Prometheus: `count by (__name__) ({k8s_namespace_name="vault"})` and Tempo: `{resource.k8s.namespace.name="vault"}` are empty |

The two Fleet settings survived with the stack. Nobody has looked at them since #145 described
them (#145 was never applied). Check them in the Fleet UI (OPERATIONS → Monitoring → Fleet
exclusions of vault) before the Vault pods start, and at the latest before `make monitoring-apply`.
Nothing reads them while no collector runs. Once a collector starts, it can read the Vault pods'
log files from their first line, and Beyla attaches to processes that are already running.

**Worked when** (within 15 minutes of the install; Explore and read-only `kubectl`):
`monitoring/ryft-secrets` holds the keys `GC_TOKEN` and `PROM_USERNAME`; every pod in
`monitoring` is Running, with one `alloy-deployment` pod; Fleet Management → Collectors shows the
new collectors with their pipelines (remove the destroyed cluster's collectors there);
`up{job="ryft/vault"}` is 1 for three pods; `up{job="ryft/vault-state"}` and
`up{job=~"ryft/kube-apiserver-admission|ryft/admission-bindings"}` are 1;
`kube_pod_labels{job="ryft/vault-state", label_vault_initialized="true"}` has three series;
`kubelet_volume_stats_capacity_bytes{namespace="vault"}` has six; the injector's selector lists
`vault` under `NotIn`; and the Loki, Prometheus and Tempo queries in the table and
`{namespace=~"ryft-.*"}` return nothing. A Vault line in Loki, or a Beyla series or trace of
`vault`, is a stop: uninstall the release, fix the pipeline, and send a Loki delete request
before going on.

### Synthetic Monitoring (gone, D236)

Grafana Cloud's Synthetic Monitoring (`deploy/grafana/synthetics/ryftlab.json`,
`scripts/synthetics.sh`, `make synthetics-check|apply`) and its `Site*`, `ApiDown` and
`SyntheticsMissing` rules went with D236. The site and api's and ingest's readiness are Cloud
Monitoring's uptime checks now (**Cloud Monitoring → The uptime checks**, above). Its definitions
and their budget are in git before D236.

## Metrics

The services' own Prometheus registry. On Cloud Run nothing reads it since D236 (no push, no
scrape; **The OTLP push (dormant)** above): compose serves it on `:9090`, and the EKS cluster's
collector scraped it. Alert names below are the app rules' and EKS's, gone with D236 where
**Alerts** says so.

| Metric | Type | Labels | Service | Meaning |
| --- | --- | --- | --- | --- |
| `ryft_http_requests_total` | counter | `route`, `code` | ingest, api | Requests by ServeMux pattern (`GET /v1/days/{day}`, never the raw path) and status. Unauthenticated requests carry the outer pattern `/v1/`; unknown routes `unmatched`. |
| `ryft_http_request_duration_seconds` | histogram | `route` | ingest, api | Request latency. |
| `ryft_samples_ingested_total` | counter | `kind`, `type` | ingest | Records accepted by `POST /v1/sync`: `kind="sample"` with the sample type, `kind="sleep"` with the stage, `kind="workout"`. |
| `ryft_last_sync_timestamp_seconds` | gauge | — | ingest | Unix time of the last successful sync of any user. Resets on restart. (Had a `device` label before multi-user; per-user or per-device series would be unbounded.) |
| `ryft_rate_limited_total` | counter | `class` | ingest, api | Requests answered `429` by the per-user rate limit: `class="sync"` (`POST /v1/sync`), `session` (`PUT /v1/sessions/{id}`), `invite` (`POST /v1/me/invite`), `import` (`POST /v1/train/import`: bursts of 5, then one every 5 s; the app's batches one after another stay under it) or `default`; or `admin_ip` (`/v1/admin/*`: 10 a second per client address, bursts of 20, per instance, 4,096 addresses, newer ones share a bucket; a request bearing `ADMIN_API_KEY` is not limited; the address is never logged or labelled); or `ip` (D240: every route of api and ingest but the health paths and the tick, before any token: 20 a second per client address, bursts of 50, per instance, 65,536 addresses, newer ones share a bucket; on api a request to `/v1/admin/*` bearing `ADMIN_API_KEY` passes; its access line has `code` `ip_limit`). The recompute gate (one per minute) is not counted here. |
| `ryft_http_panics_total` | counter | `route` | all | Handler panics turned into a `500` by the recover middleware. Each also logs `msg="panic"` with the stack. A tick step's panic counts under `route="POST /internal/tick"`; the tick goes on with its next step and answers `204` (D219). |
| `ryft_goroutine_panics_total` | counter | none | all (once a service starts through `internal/app`) | Panics recovered in a service's own goroutines: its servers (HTTP, and `/metrics` where it listens); there are no consumers, timers or relay goroutines any more (D219). The service shuts down in order and exits 2, as an unrecovered panic does; the `msg="panic"` line names the `task` and holds the stack. A panic in a ready check is counted too (the line names the `check`, logged at most once a minute) and the service keeps running. Any is a bug. In the `Panics` alert, but the process exits 2 right after it, before any push, so the push rarely carries the increase: Cloud Logging's `msg="panic"` line and the instance's exit are the other traces. |
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
| `ryft_consents_total` | counter | `action` | api | `granted`, `revoked`, `stale` (a POST for an outdated policy version: the app shows an old text). |
| `ryft_invite_redemptions_total` | counter | `result` | api | `POST /v1/me/invite`: `ok` (a use spent), `already` (the account had access), `invalid` (every refusal, a revoked code and a bound code for another email included: one label on purpose, D159). Many `invalid` from one place is someone guessing (5 an hour per user). The `503` for a bound code when Supabase cannot be asked is not counted here: it is the warn line `invite email lookup failed`. |
| `ryft_invite_binding_available` | gauge | none | api | 1 while email-bound invites work: the Supabase project confirms emails (`mailer_autoconfirm` false in its public auth settings) and the api holds the Supabase admin key. 0 while *Confirm email* is off (an address then counts as confirmed without a mailbox), the setting cannot be read, or there is no admin key: bound codes are neither created nor redeemed (D159). The log line `invite binding` says which state on every change |
| `ryft_invite_settings_checks_total` | counter | `result` | api | Reads of that setting, a step of every api tick plus one lazy read on the first bound invite (none at start, D219; until a read succeeds, bound codes are refused): `ok`, `autoconfirm` (Confirm email is off: turn it on, OPERATIONS → Beta), `error` (unreadable, treated like off) |
| `ryft_invite_email_purge_total` | counter | `result` | api | Passes of the job that clears a bound invite email and its note 30 days after the code stopped being redeemable (D161): `ok` (also a pass that cleared nothing), `error`. Created at 0 at start. `InviteEmailPurgeFailing` watches `error` |
| `ryft_events_dlq_total` | counter | `subject`, `result` | scoring, notify | Bad events a consumer dead-lettered itself (data that does not decode, or a `ryft-key` that is not the push path's) into its own topic, `ryft-dlq-scoring` or `ryft-dlq-notify`, with `ryft-error` (D218). `subject` is the push path's key (on `/push/wait` the attribute's key when it is one of notify's, else `unknown`); the attribute a publisher sets never becomes a label, so the values are the topology's keys and `unknown`. `result`: `ok`, or `error` (the publish failed; the push was nacked, and Pub/Sub's dead-letter policy forwards it after 20 attempts; `DeadLetterPublishFailing`). Events that fail all their attempts are dead-lettered by Pub/Sub and not counted here: they are `ryft_events_handled_total{result="failed"}`. Before the cutover to RabbitMQ, `subject` was the NATS subject (`ryft.ingested`). |
| `ryft_sync_to_score_seconds` | histogram | — | scoring | From ingest accepting a sync to scoring finishing one of the days it queued (`score_queue`, D91; an import's days too: they share the queue, so a large import shows as a short rise, and `expected_within_sec`, the queue's p95 of the last 6 hours, rises with it, D141). The p95 is what testers wait for their scores; `SyncResponse.scoring.expected_within_sec` tells the app the same from the table. |
| `ryft_event_handler_panics_total` | counter | `subject` | scoring, notify | Event handlers that panicked: the message is retried and, at its last delivery, dead-lettered, instead of the service crashing (D92). Any is a bug. |
| `ryft_outbox_pending` | gauge | `publisher` | ingest, api, scoring, notify | Due rows only: outbox rows whose `available_at` has passed and that Pub/Sub has not accepted yet. A deferral waiting for its time (notify's quiet hours, D219) does not count. Set by each tick's outbox sweep. Normally 0: the request flushes its rows before it answers. |
| `ryft_outbox_oldest_seconds` | gauge | `publisher` | ingest, api, scoring, notify | Due rows only: how long the oldest due row has waited, counted from when it became due (the later of `created_at` and `available_at`), so a deferral that just came due after a night of quiet hours is no backlog; 0 when none waits. Set by each tick's sweep. Above a tick or two: the flush after the request and the sweeps fail, Pub/Sub refuses or cannot be reached (`OutboxBacklog`). |
| `ryft_outbox_stuck` | gauge | `publisher` | ingest, api, scoring, notify | Rows set aside, due or not (OPERATIONS → The outbox): after 50 failed sweeps (only the tick's sweep counts an attempt, so about 12 hours of an outage; a flush records the error only), or at once when the transport rejected the row for good (`events.Rejected`: a key no topic takes, an ordered event without a user, Pub/Sub's 400, 403, 404 or 413; `last_error` begins `rejected, set aside:`). |
| `ryft_outbox_relayed_total` | counter | `publisher`, `result` | ingest, api, scoring, notify | Rows the relay sent, by an inline flush or the tick's sweep: `ok` (Pub/Sub accepted it; the row is marked) or `error` (left for the next flush or sweep, or set aside when rejected for good). |
| `rabbitmq_queue_messages_ready`, `rabbitmq_queue_messages_unacked`, `rabbitmq_queue_messages` | gauge | `vhost`, `queue` | RabbitMQ (:15692) | Per queue (`return_per_object_metrics`): waiting, delivered and not settled, all. `scoring`, `notify`, the retry and wait tiers, `dlq.*`. |
| `rabbitmq_alarms_memory_used_watermark`, `rabbitmq_alarms_free_disk_space_watermark` | gauge | `instance` | RabbitMQ | 1 while that node's alarm blocks publishers. |
| `rabbitmq_identity_info` | gauge | `rabbitmq_node`, `rabbitmq_cluster` | RabbitMQ | One series per node that reports; three when healthy. Also `rabbitmq_process_resident_memory_bytes`, `rabbitmq_disk_space_available_bytes`, `rabbitmq_connections`, `rabbitmq_channel_messages_published_total`. |
| `ryft_db_up` | gauge | `service` | all | 1 when the service's last database ping succeeded, 0 when it failed. Set by each tick (its first step, every 15 minutes), not by a timer: there is no `db.Watch` any more (D219). An instance that never ran a tick has no value. |
| `ryft_db_ping_failures_total` | counter | `service` | all | Failed pings of the tick's db step: Supabase or its pooler unreachable, or the role refused. |
| `ryft_ticks_total` | counter | `service`, `result` | all | `POST /internal/tick` handled after authentication (D219), one per tick: `ok` (every step succeeded), `error` (a step failed or panicked, or the budget ran out before a step began; the tick still answers `204`), `overlap` (a tick was still running in this instance: answered `204` at once). Counted before the tick's metrics push, so the push carries it. A token refused on the tick counts in `ryft_auth_failures_total`, not here. `TickMissing` reads it. |
| `ryft_users_by_last_sync` | gauge | `age` | scoring | Accounts by the age of their newest device sync (every 15 min): `under_6h`, `6h_to_24h`, `1d_to_3d`, `over_3d`, `never`. Counts only, no user ids. |
| `ryft_feedback_received_total` | counter | `category` | api | Reports stored by `POST /v1/feedback` (`bug`, `idea`, `data`, `other`). On EKS the alert on any increase was how the operator heard about them (D83); on Cloud Run nothing reads this counter (D236), so nothing tells him: OPERATIONS → GCP → 14a.8. Read them with `scripts/beta_report.sh feedback` or `/admin`. |
| `ryft_push_notifications_total` | counter | `category`, `result` | notify | One per notification decided: `category` `MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE` or `REFRESH` (the silent push); `result` `sent`, `failed` (no device reached), `muted` (category off), `quiet` (dropped in quiet hours), `deferred` (morning push held until they end), `duplicate` (already sent that day), `throttled` (a silent refresh within 5 minutes of the last), `stale` (old day or session, deleted user), `import` (`SESSION_SCORED` only: an imported session, never announced, D125; scoring sends none for an import, so this counts only a re-complete of an imported session), `no_token` (no registered device), `unconfigured` (no APNs key: counted, not sent). An import's days end in one `REFRESH` once scored, like a sync's. |
| `ryft_apns_responses_total` | counter | `push_type`, `status` | notify | APNs answers per device request: `alert` or `background`, HTTP status (`200`, `400`, `403`, `410`, `429`, `5xx`) or `error` (no answer). |
| `ryft_push_tokens_pruned_total` | counter | — | notify | Device tokens dropped because APNs said they are gone. |
| `ryft_train_sessions_completed_total` | counter | — | api | Strength sessions completed (the first `POST /v1/sessions/{id}/complete` of each). |
| `ryft_train_sets_logged_total` | counter | — | api | Completed sets `PUT /v1/sessions/{id}` stored for the first time. |
| `ryft_train_prs_total` | counter | `kind` | api | Personal records a session took over (`e1rm`, `max_weight`, `max_reps`, `max_volume`). |
| `ryft_train_imports_total` | counter | `result` | api | `POST /v1/train/import` requests past authentication (D124): `ok` (`200`, also when every item was rejected), `invalid` (`400`/`413`/`422` or `403 consent_required`: nothing processed), `disabled` (`403 import_not_enabled`: the flag `train.import` is off for the account; nothing read), `busy` (`429 import_in_progress`: another import of the account holds the lease, D132), `timeout` (`503 "request timed out"`: the items ran into the 50 s budget, or the client went away), `error` (`503`: the database failed). After `timeout` and `error` the stored items stay stored and are finished (queue and event, then records); the app resends and gets them back `unchanged`. A steady `busy` for one account means a lease left by a pod that died (it expires after 70 s). |
| `ryft_train_import_items_total` | counter | `kind`, `status` | api | Items of the import: `kind` `plan` or `session`, `status` `created`, `unchanged` (identical, nothing written), `updated` (replaced whole), `rejected` (the `code` is in the response, not here). Mostly `unchanged` means the app resends files it already sent. |
| `ryft_train_import_seconds` | histogram | — | api | Duration of an import that took the lease: the items, the records and the queueing. The budget is 50 s (the items stop 10 s before it so the finish fits); p95 above ~20 s means the per-item reads (D131's note) need batching. |
| `argocd_app_info` | gauge | `name`, `sync_status`, `health_status`, `autosync_enabled`, … | Argo CD (controller, :8082) | One series per application, value 1; the alerts below read `sync_status` (`Synced`, `OutOfSync`, `Unknown` when manifests do not render) and `health_status` (`Healthy`, `Progressing`, `Degraded`, `Missing`). |
| `argocd_app_sync_total` | counter | `name`, `phase` | Argo CD | Sync operations by outcome: `Succeeded`, `Failed`, `Error` (a PreSync migration, a refused apply, the PostSync smoke), `Running`, `Terminating`. |
| `apiserver_validating_admission_policy_check_total` | counter | `policy`, `policy_binding`, `enforcement_action`, `error_type` | EKS API servers (one scrape per endpoint, job `ryft/kube-apiserver-admission`) | Policy checks that found something, by binding: a violation counts once per action of the binding (`warn` and `audit` in Warn+Audit, `deny` and `audit` in Deny+Audit); `error_type` is `no_error` unless the expression could not be evaluated (`compile_error`, `invalid_error`, `out_of_budget`). **Clean passes are not counted** (design claim e was wrong): a policy that is never violated has no series, so the series is absent, not zero, and never a liveness signal. A child is created at 1 by its first event, which `increase()` cannot see: the rules add series that are new (see Admission). Only `policy=~"ryft-.*"` is used by the alerts. |
| `apiserver_validating_admission_policy_check_duration_seconds_sum`, `_count` | counter | as above | EKS API servers | Check latency as a mean only: the buckets are dropped to save series. Nothing alerts on it (kept for the dashboard; it can go if the budget tightens). |
| `pod_security_evaluations_total` | counter | `decision`, `mode`, `resource`, `policy_level`, `request_operation`, … | EKS API servers | Pod Security Admission decisions. Only `decision="deny"` is kept (the relabel drops `allow` and `exempt`). `mode` is `enforce` (refused), `warn` or `audit`. Enforce is live today, so `AdmissionPodDenied` watches it without any binding. |
| `kube_validatingadmissionpolicybinding_info` | gauge | `validatingadmissionpolicybinding`, `policy_name` (+ `param_*`, empty) | kube-state-metrics, native collector `validatingadmissionpolicybindings` (job `ryft/admission-bindings`) | One series per binding, value 1. 12 `ryft-*` bindings once the chart is enabled, none before. kube-state-metrics v2.20 does not export a binding's labels, so the committed action is not here (see `ryft:admission_binding_intended`). Custom-resource-state cannot be used: it resolves kinds from CRDs only and skips built-in ones (design claim m was wrong). |
| `kube_validatingadmissionpolicybinding_validation_action` | gauge | `validatingadmissionpolicybinding`, `action` | kube-state-metrics | One series, value 1, for each of the binding's live `validationActions` (`Deny`, `Warn`, `Audit`); an action it lacks has no series. |
| `ryft:admission_binding_intended` | recording rule | `validatingadmissionpolicybinding`, `intended_action` | Grafana ruler (`deploy/grafana/alerts/admission-intended.yaml`) | 1 per binding, `audit` or `deny`, generated from `actions:` in `infra/aws/kubernetes/admission-values.yaml` by `scripts/admission_intended.sh`. `make alerts-check` fails when the file and the values differ. It exists whether or not the chart is enabled. |
| `vault_core_active` | gauge | `pod`, `vault_cluster` | Vault, every unsealed node (job `ryft/vault`) | 1 on the active node, 0 on a standby. A sealed node reports none (and Vault's gauges expire 2 minutes after their last update, `prometheus_retention_time`), so absence is not "standby". Vault's own `cluster` label (its Raft cluster name) is renamed `vault_cluster` by the scrape, so the external `cluster` label stays the cluster's. `VaultNoActiveNode`. |
| `vault_core_unsealed` | gauge | `pod`, `vault_cluster` | Vault, every node | 1 unsealed, 0 sealed, but it expires with the retention, so the alerts read sealing from the pods' readiness instead (`kube_pod_container_status_ready`). Dashboard only. |
| `vault_autopilot_healthy`, `vault_autopilot_failure_tolerance`, `vault_autopilot_node_healthy` | gauge | `node_id` (the last) | Vault, the active node only | Raft autopilot: the cluster's health, how many voters it may lose, each voter's health. `VaultRaftPeersBelow3` counts `node_healthy == 1` (`vault_raft_peers` expires and lags, not used). |
| `vault_audit_log_request_failure`, `vault_audit_log_response_failure` | counter | `pod` | Vault, the active node | Audit entries that could not be written. Cumulative: Vault adds 0 or 1 on every request, so the counter lives while requests come and expires 2 minutes after the last (retention). `VaultAuditFailing` (critical) reads `increase()` over 10 minutes, plus a counter that appears above 0 (no request for 2 minutes, then a failing one). With every audit device failing, Vault refuses the request. |
| `vault_audit_log_request_count`, `vault_audit_log_response_count`, `vault_core_handle_request_count`, `vault_core_handle_login_request_count`, `vault_core_in_flight_requests` | counter, gauge | `pod` | Vault, the active node | Request and login volume (VSO's hourly refreshes and logins for every service namespace, the snapshot job's four logins a day, the operator's sessions); dashboard only. |
| `vault_token_count`, `vault_token_count_by_policy`, `vault_token_count_by_ttl` | gauge | `policy`, `creation_ttl` | Vault, the active node | Live tokens. Vault emits these usage gauges every 10 minutes and they expire after 2, so the series has gaps: read it with `max_over_time(…[15m])`. Not seen in the local 2.2.1 check (a fresh dev cluster); confirm on the first scrape. A steady rise means a client that logs in more often than its tokens expire (the snapshot job's token, policy `snapshot`, cannot revoke itself and lives its 10 minutes: four a day, never more than one at a time). |
| `vault_raft_leader_lastContact_sum`, `_count` | summary | `pod` | Vault, standbys | Heartbeat delay from the leader as a mean; a rise before `VaultRaftPeersBelow3` points at a CPU-starved node. |
| `kube_cronjob_status_last_successful_time`, `kube_cronjob_status_last_schedule_time` | gauge | `cronjob` | kube-state-metrics (job `ryft/vault-state`, namespace `vault` only) | When the snapshot job last succeeded and was last scheduled (Unix seconds). The success series exists only after the first success. `VaultSnapshotStale` (pages at 13 h), `VaultSnapshotFailing`. |
| `kube_statefulset_replicas`, `kube_statefulset_status_replicas`, `kube_statefulset_status_replicas_ready`, `kube_statefulset_status_replicas_updated` | gauge | `statefulset` | kube-state-metrics (`ryft/vault-state`) | The server StatefulSet: desired, existing, ready (unsealed and serving), at the current template (OnDelete: `VaultUpdatePending`). `replicas_ready` at 0 for 10 minutes is `VaultUnavailable`. |
| `kube_pod_container_status_running`, `kube_pod_container_status_ready` | gauge | `pod`, `container` | kube-state-metrics (`ryft/vault-state`) | Per server container: running but not ready is `VaultSealed` (one pod) or `VaultSealedQuorum` (two or more). Fleet's own kube-state-metrics pipeline does not keep readiness, hence the scrape of its own. |
| `kube_pod_labels` | gauge | `pod`, `label_vault_initialized`, `label_vault_sealed`, `label_vault_active` | kube-state-metrics (`ryft/vault-state`; `--metric-labels-allowlist=pods=[vault-initialized,vault-sealed,vault-active]`) | The labels Vault's service registration writes on its own pods. At every process start it resets them to `vault-initialized=false`, `vault-sealed=true` and sets `vault-initialized=true` only when the node unseals, so the label means "unsealed at least once since this process started", not "initialised": a pod restarted and still sealed shows `"false"` (source: the state Vault's server command hands its Kubernetes service registration at start (`command/server.go`: initialized false, sealed true) and the registration's `NotifyInitializedStateChange(true)` on the unseal path (`vault/`), as read in the #153 re-read; not checked against 2.2.1's source here, **to verify in the kind drill**). A server pod with `label_vault_initialized="true"` in the last hour, or a snapshot that has succeeded once, is the alerts' "Vault has been initialised" gate. |
| `go_*`, `process_*` | | | all | Go runtime and process collectors; on Cloud Run pushed with the rest (about 50 series per instance). |

## Grafana dashboard — panel list

*EKS leftover until D3:* the cluster's Grafana dashboard. Cloud Run's is "RYFT · Overview" in
Cloud Monitoring (**Cloud Monitoring → The dashboard**, above).

Variables: `$namespace`, a regex (default `ryft-.*`, the per-service namespaces). All rates over `[5m]` unless noted.

**Row: Health at a glance**

| Panel | Type | Query |
| --- | --- | --- |
| Time since last sync | stat (thresholds 6 h / 24 h) | `time() - max(ryft_last_sync_timestamp_seconds)` |
| Syncs (24 h) | stat | `sum(increase(ryft_http_requests_total{app="ingest",route="POST /v1/sync",code="200"}[24h]))` |
| Days scored (24 h) | stat | `sum(increase(ryft_days_scored_total{result="ok"}[24h]))` |
| API error ratio | stat (%) | `sum(rate(ryft_http_requests_total{app="api",code=~"5.."}[5m])) / sum(rate(ryft_http_requests_total{app="api"}[5m]))` |
| Pods up | stat | `sum by (app) (up{namespace=~"$namespace"})` |

**Row: Ingest**

| Panel | Type | Query |
| --- | --- | --- |
| Sync requests by status | time series | `sum by (code) (rate(ryft_http_requests_total{route="POST /v1/sync"}[5m]))` |
| Sync latency p50 / p95 / p99 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_http_request_duration_seconds_bucket{route="POST /v1/sync"}[5m])))` (repeat for 0.5, 0.99) |
| Samples ingested by type | stacked bars, 1 h buckets | `sum by (type) (increase(ryft_samples_ingested_total{kind="sample"}[1h]))` |
| Sleep segments / workouts | time series | `sum by (kind) (increase(ryft_samples_ingested_total{kind!="sample"}[1h]))` |
| Auth failures by reason | time series | `sum by (reason) (rate(ryft_auth_failures_total[5m]))` |
| Rate limited / panics | time series | `sum by (app, class) (rate(ryft_rate_limited_total[5m]))`, `sum by (app, route) (increase(ryft_http_panics_total[5m]))` |

**Row: Scoring**

| Panel | Type | Query |
| --- | --- | --- |
| Days scored by result | time series | `sum by (result) (rate(ryft_days_scored_total[5m]))` |
| Scoring duration p50 / p95 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_scoring_duration_seconds_bucket[5m])))` |
| Days re-scored after a history change | stacked bars, 1 h buckets | `sum by (reason) (increase(ryft_days_rescored_total[1h]))` |
| Sleep baselines learned | stacked bars, 1 d buckets | `sum by (outcome) (increase(ryft_sleep_base_learned_total[1d]))` |
| Events handled by result | time series | `sum by (reason, result) (rate(ryft_events_handled_total[5m]))` |
| Redeliveries | time series | `sum by (consumer) (rate(ryft_nats_redeliveries_total[5m]))` |
| Publish errors | time series | `sum by (app, reason) (rate(ryft_events_published_total{result="error"}[5m]))` |

**Row: Journal**

| Panel | Type | Query |
| --- | --- | --- |
| Journal days saved (24 h) | stat | `sum(increase(ryft_journal_saves_total[24h]))` |
| Impact computation p95 | time series | `histogram_quantile(0.95, sum by (caller, le) (rate(ryft_behavior_impact_seconds_bucket[1h])))` |

**Row: Push**

| Panel | Type | Query |
| --- | --- | --- |
| Notifications by result | stacked bars, 1 h buckets | `sum by (category, result) (increase(ryft_push_notifications_total[1h]))` |
| APNs answers | time series | `sum by (push_type, status) (rate(ryft_apns_responses_total[5m]))` |
| Tokens pruned (7 d) | stat | `sum(increase(ryft_push_tokens_pruned_total[7d]))` |
| Flags served on / off | time series | `sum by (key, result) (rate(ryft_flag_evaluations_total[1h]))` |

**Row: API**

| Panel | Type | Query |
| --- | --- | --- |
| Requests by route | time series | `sum by (route) (rate(ryft_http_requests_total{app="api",route=~".*/v1/.*"}[5m]))` |
| Latency p95 by route | time series | `histogram_quantile(0.95, sum by (route, le) (rate(ryft_http_request_duration_seconds_bucket{app="api"}[5m])))` |
| 4xx / 5xx by route | time series | `sum by (route, code) (rate(ryft_http_requests_total{app="api",code=~"[45].."}[5m]))` |

**Row: Train**

| Panel | Type | Query |
| --- | --- | --- |
| Sessions completed (24 h) | stat | `sum(increase(ryft_train_sessions_completed_total[24h]))` |
| Sets logged | time series, 1 h buckets | `sum(increase(ryft_train_sets_logged_total[1h]))` |
| Records by kind | stacked bars, 1 d buckets | `sum by (kind) (increase(ryft_train_prs_total[1d]))` |
| Session saves: 409 / 422 / 429 | time series | `sum by (code) (rate(ryft_http_requests_total{route="PUT /v1/sessions/{id}",code=~"409\|422\|429"}[5m]))` |
| Imports by result | stacked bars, 1 h buckets | `sum by (result) (increase(ryft_train_imports_total[1h]))` |
| Imported items by status | stacked bars, 1 d buckets | `sum by (kind, status) (increase(ryft_train_import_items_total[1d]))` |
| Import duration p95 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_train_import_seconds_bucket[1h])))` |

**Row: Runtime** (repeat by `app`)

| Panel | Type | Query |
| --- | --- | --- |
| Memory (RSS) | time series | `process_resident_memory_bytes{namespace=~"$namespace"}` |
| Goroutines | time series | `go_goroutines{namespace=~"$namespace"}` |
| CPU | time series | `rate(process_cpu_seconds_total{namespace=~"$namespace"}[5m])` |

**Row: Events and the database**

| Panel | Type | Query |
| --- | --- | --- |
| Sync to score p50 / p95 | time series | `histogram_quantile(0.95, sum by (le) (rate(ryft_sync_to_score_seconds_bucket[15m])))` (repeat for 0.5) |
| Dead letters | stacked bars, 1 h buckets | `sum by (subject, result) (increase(ryft_events_dlq_total[1h]))` |
| Queue depths (RabbitMQ) | time series | `max by (queue) (rabbitmq_queue_messages_ready{vhost="ryft"})`, `max by (queue) (rabbitmq_queue_messages_unacked{vhost="ryft"})` |
| Dead-letter queues | stat | `max by (queue) (rabbitmq_queue_messages{vhost="ryft", queue=~"dlq\\..*"})` (above 0: `KUBE_CONTEXT=ryft-prod-euc1 scripts/dlq.sh list`) |
| Broker nodes and alarms | state timeline | `count(rabbitmq_identity_info{rabbitmq_cluster="rabbitmq"})`, `max by (instance) (rabbitmq_alarms_memory_used_watermark + rabbitmq_alarms_free_disk_space_watermark)` |
| Broker memory per node | time series | `max by (instance) (rabbitmq_process_resident_memory_bytes)` (the alarm at about 520 MB) |
| Connected to RabbitMQ | state timeline | `min by (namespace) (ryft_amqp_connected)`, and `sum by (namespace) (increase(ryft_amqp_reconnects_total[1h]))` |
| Outbox | time series | `max by (publisher) (ryft_outbox_pending)`, `max by (publisher) (ryft_outbox_oldest_seconds)`, `max by (publisher) (ryft_outbox_stuck)` |
| Publishes by result | time series | `sum by (reason, result) (rate(ryft_events_published_total[5m]))` |
| Database up | state timeline | `min by (service) (ryft_db_up)` |

**Row: Beta**

| Panel | Type | Query |
| --- | --- | --- |
| Testers by last sync | bar gauge | `max by (age) (ryft_users_by_last_sync)` (`under_6h`, `6h_to_24h`, `1d_to_3d`, `over_3d`, `never`; who: `scripts/beta_report.sh`) |
| Refused or flagged: invite / consent | time series | `sum by (code, mode) (increase(ryft_gate_refusals_total[1h]))` |
| Consents | stacked bars, 1 d buckets | `sum by (action) (increase(ryft_consents_total[1d]))` |
| Feedback (7 d) | stat | `sum by (category) (increase(ryft_feedback_received_total[7d]))` |
| Deploys | table | `max by (name, sync_status, health_status) (argocd_app_info)`, and `sum by (name, phase) (increase(argocd_app_sync_total[24h]))` |

**Row: Vault** (namespace `vault`; not filtered by `$namespace`)

| Panel | Type | Query |
| --- | --- | --- |
| Active node / ready pods | state timeline | `max by (pod) (vault_core_active{job="ryft/vault"})`, `max(kube_statefulset_status_replicas_ready{job="ryft/vault-state", statefulset="vault"})` (3 when healthy; 0 before the init) |
| Raft health | stat | `max(vault_autopilot_healthy{job="ryft/vault"})`, `min(vault_autopilot_failure_tolerance{job="ryft/vault"})` (1 with three voters), `count(max by (node_id) (vault_autopilot_node_healthy{job="ryft/vault"}) == 1)` |
| Last snapshot | stat (thresholds 7 h / 13 h) | `time() - max(kube_cronjob_status_last_successful_time{job="ryft/vault-state", cronjob="vault-snapshot"})` |
| Requests and logins | time series | `sum(rate(vault_core_handle_request_count{job="ryft/vault"}[15m]))`, `sum(rate(vault_core_handle_login_request_count{job="ryft/vault"}[15m]))` |
| Audit failures | time series | `max by (pod) (vault_audit_log_request_failure{job="ryft/vault"})`, `max by (pod) (vault_audit_log_response_failure{job="ryft/vault"})` (empty when healthy) |
| Volumes | bar gauge (%) | `max by (persistentvolumeclaim) (kubelet_volume_stats_used_bytes{namespace="vault"}) / max by (persistentvolumeclaim) (kubelet_volume_stats_capacity_bytes{namespace="vault"})` |
| Live tokens | time series | `max by (policy) (max_over_time(vault_token_count_by_policy{job="ryft/vault"}[15m]))` |
| Leader contact | time series | `max by (pod) (rate(vault_raft_leader_lastContact_sum{job="ryft/vault"}[5m]) / rate(vault_raft_leader_lastContact_count{job="ryft/vault"}[5m]))` (ms) |
| Pending pod restarts | stat | `max(kube_statefulset_replicas{job="ryft/vault-state", statefulset="vault"}) - max(kube_statefulset_status_replicas_updated{job="ryft/vault-state", statefulset="vault"})` |

RabbitMQ's nodes carry the same scrape annotations (port 15692, plain HTTP; the
broker's network policy admits it from the VPC range like the services' 9090). Its
management UI shows the same per queue (OPERATIONS → RabbitMQ). Until the cutover the
NATS consumers reported their backlog themselves (`ryft_nats_consumer_pending`,
`ryft_nats_stream_messages`); those series end with R4. For the
in-cluster Postgres of `local-postgres`, `postgres_exporter` with a read-only role.

## Alerts

**RYFT on Cloud Run** alerts from Cloud Monitoring only: the six policies of `infra/gcp/alerts.tf` (the sixth since D244)
(**Cloud Monitoring → The alert policies**, above; first looks in OPERATIONS → GCP → When an alert
fires). The app rules that stood here (`deploy/grafana/alerts/ryft.yaml`, its tests
`ryft.test.yaml`, `make alerts-apply`) were never loaded on Cloud Run and went with D236, and so did
Grafana's five rules on Cloud Monitoring (`grafana-reader`); what each paged on and what covers it
now: **Cloud Monitoring → What D236 dropped**. Their expressions are in git before D236.

**The EKS cluster's rules** are what is left in `deploy/grafana/alerts`, EKS leftovers until D3:
`eks-admission.yaml` (with `eks-admission.test.yaml`) and `admission-intended.yaml`. `make
alerts-check` runs `scripts/check_rule_groups.sh`, `promtool check rules`, `promtool test rules`
and `mimirtool rules check` (in containers) over them; nothing loads them. Grafana Cloud's ruler
takes at most **20 rules per group**, which `check_rule_groups.sh` holds them to.

### EKS leftovers

The move to Cloud Run (ticket B2) took every rule that reads the cluster out of `ryft.yaml`, and
D236 removed `ryft.yaml` itself:

- **Moved, not loaded.** Group `ryft-admission` (the `Admission*` alerts and their two recording
  rules) is in `deploy/grafana/alerts/eks-admission.yaml` (namespace `ryft`), its tests in
  `eks-admission.test.yaml`. `make alerts-check` still checks and tests it, because
  `scripts/admission_intended.sh` reads it and generates `admission-intended.yaml` (group
  `ryft-admission-intended`, namespace `ryft-admission`) from the bindings' committed actions;
  nothing loads either (`make alerts-apply` went with D236). Both go with the cluster (D3).
  **Admission** below describes them.
- **Removed.** Group `ryft-vault` (RYFT on GCP keeps its secrets in Secret Manager; **Vault**
  below describes the copy still live), and from the other groups PodRestarting, ServiceExited,
  DatabaseFlapping, CertificateExpiring, RabbitMQBacklog, RabbitMQDeadLetters, RabbitMQAlarm,
  RabbitMQNodeDown, AMQPDisconnected, EventsUnroutable and the four `Argo*` alerts (no pods,
  broker or Argo CD on Cloud Run), and the app-metric HTTP5xxRatio, SyncSlow and DeadLetters.
  With D236 the rest of `ryft.yaml` (groups `ryft`, `ryft-pipeline`, `ryft-ops`: the app-metric
  rules on the OTLP push) and its tests went too; **Cloud Monitoring** above says what replaced
  them and what nothing replaces. Their expressions are in git before D236.
- **Live copies.** On EKS, `make alerts-apply` loaded `ryft.yaml` (namespace `ryft`, groups
  `ryft`, `ryft-pipeline`, `ryft-ops`, `ryft-admission` and `ryft-vault`) and
  `admission-intended.yaml` (namespace `ryft-admission`, group `ryft-admission-intended`). A
  load never deleted a group it did not name, and since D236 nothing loads at all, so six groups
  stay live in the stack's ruler as last loaded: `ryft/ryft`, `ryft/ryft-pipeline` and
  `ryft/ryft-ops` (the app rules: with no push since the move they see no series; nothing
  replaces those groups any more), and `ryft/ryft-admission`, `ryft/ryft-vault` and
  `ryft-admission/ryft-admission-intended` (firing on the cluster as long as it runs). Once
  nothing serves from EKS (after the site moved at GCP step 10; at the latest with D3),
  `make alerts-retire-eks` deletes the last three (`EKS_RULE_GROUPS` in the Makefile,
  `mimirtool rules delete`; `scripts/tests/alerts_retire_test.sh` holds the list to the files).
  It tries every group: one that is already gone is reported, the others are still deleted, and
  the exit is 1, so check Alerting → Alert rules then. The first three go by hand in Alerting →
  Alert rules, or with the stack itself (GCP step 11.7).

### Admission

The `Admission*` alerts (group `ryft-admission`, now in `eks-admission.yaml`: checked, not
loaded since the move to Cloud Run, live in the ruler until `make alerts-retire-eks`; EKS
leftovers above) read the cluster's admission metrics (Collection above). Every selector is pinned
by `job`.

| Alert | Severity | Fires when | First look |
| --- | --- | --- | --- |
| AdmissionPolicyViolation | info | a Warn+Audit binding (or Pod Security's warn/audit mode) flagged an admitted request in the last 10 min; not for bindings that deny. Info, not warning: findings that repeat on every Argo CD refresh would page all day | OPERATIONS → Admission policy signals: the day's Logs Insights query by object |
| AdmissionPolicyDenied | warning | a Deny+Audit binding refused a request in the last 10 min (not `ryft-workloads-pods`, `ryft-workloads-pods-vault`, `ryft-operator-reach`, `ryft-auth-env`) | `argocd app get`, the audit annotation; fix the object in Git |
| AdmissionPodDenied | critical | a Pod-capable binding (`ryft-workloads-pods`, `ryft-workloads-pods-vault`, `ryft-operator-reach`, `ryft-auth-env`: step C, vault's pods and P5, the design's critical class) or Pod Security's enforce mode refused a request in the last 10 min. The counter has no resource label, so a P5 or P8 refusal of another object pages too | `FailedCreate` events; break-glass the binding first if workloads cannot be re-created |
| AdmissionPolicyError | critical / warning | a policy's CEL could not be evaluated in the last 10 min (`error_type` not `no_error`): critical when the binding refuses (the request fails under `Fail`), warning under Warn+Audit | the expression meets an unexpected object shape; break-glass, then fix the chart |
| AdmissionBindingMissing | critical | fewer than 17 `ryft-*` bindings for 10 min, once some existed in the last day | `kubectl get validatingadmissionpolicybindings`, kube-state-metrics, the `[0]` replace apply (OPERATIONS) |
| AdmissionBindingNotDenying | warning | a binding committed as deny has no `Deny` while other bindings do, 10 min | a fast-path patch left in place; the `[0]` replace apply |
| AdmissionBindingDrift | warning | the other mismatches with the committed action (deny without Audit; audit with Deny or without Warn or Audit), 2 h | the same |
| AdmissionBreakGlass | warning | no `ryft-*` binding has `Deny` while at least one is committed as deny, 4 h | end it with `admission_break_glass=false` |
| AdmissionSignalsMissing | warning | no `up` for the API-server scrape or the bindings scrape for 15 min (also before the monitoring values are applied) | `kubectl -n monitoring get pods`, the collector log |
| AdmissionScrapeDown | warning | `up == 0` for an admission target, 15 min | the same; remote_write errors in the collector log |

Properties that are deliberate:

- **Counter alerts need no gate on the bindings.** They match `policy=~"ryft-.*"`, which only
  exists once the chart is enabled, and Pod Security's enforce mode is live today, so its
  refusals page now. The binding alerts (Missing, NotDenying, Drift, BreakGlass) are quiet in
  Phase 0 because they join on bindings that do not exist. `AdmissionBindingMissing` looks back
  a day, so a chart switched off on purpose raises it for up to a day (silence it), and one that
  never ran does not.
- **The first-event gap.** A counter child is created at 1 by its first event, so
  `increase()` is empty for the first violation, denial or error of a policy and binding.
  Two recording rules (`ryft:apiserver_admission_policy_check:fresh10m`,
  `ryft:pod_security_evaluations:fresh10m`) add the series that have no sample in the hour
  before the last 10 minutes (`unless last_over_time(... [1h] offset 10m)`), but only on an
  endpoint whose scrape was up 12 minutes ago (`up offset 12m == 1`), so turning the scrape
  on, or recovering it after an outage, does not make every old counter look new. A single
  event fires for 10 minutes and resolves. A new API-server endpoint (EKS replaces
  control-plane instances) is blind for its first 12 minutes.
- **The committed action is a recording rule.** kube-state-metrics cannot export a binding's
  labels, so `deploy/grafana/alerts/admission-intended.yaml` (namespace `ryft-admission` in the
  ruler) holds `ryft:admission_binding_intended` per binding, generated from
  `infra/aws/kubernetes/admission-values.yaml`. When a step changes `actions:`, regenerate it
  (`scripts/admission_intended.sh`) in the same PR; `make alerts-check` fails otherwise. Load
  the rules after the Terraform apply (the other order raises `NotDenying`; this one a
  Drift that waits two hours and clears when the rules load).
- **Break-glass is seen from the live actions.** A binding committed as deny without `Deny`
  while others deny is `AdmissionBindingNotDenying` (10 min); when no binding denies it is
  `AdmissionBreakGlass` (4 h) instead. `AdmissionBindingDrift` covers the rest, after 2 h.
- **Liveness is not gated.** `AdmissionSignalsMissing` and `AdmissionScrapeDown` fire when a
  scrape is gone or failing; the clean-pass counter is not a liveness signal (it does not
  exist).

Series budget (free tier, 10k active series): the three API-server families are about 250
series at most (17 bindings x 3 or 4 action and error children x 2 API servers, the latency
as `_sum` and `_count`, Pod Security refusals only), kube-state-metrics 17 (`info`) + at most
34 (`validation_action`: Deny and Audit or Warn and Audit per binding) + 2, the 17 recorded
intended series, and `up` plus `scrape_samples_scraped` per target; about 350 in total. Deny
and error children appear only after their first event.

To confirm in the kind e2e before Phase 1: the `enforcement_action` and `error_type` label
values on EKS's Kubernetes minor (which of them an evaluation error carries), that
`policy_binding` is the binding's name, and that Pod refusals carry `resource="pod"`.

### Vault

**Removed from the repository with the move to Cloud Run** (ticket B2): the group `ryft-vault`
stays live in the ruler as last loaded until `make alerts-retire-eks` (EKS leftovers above), and
this section describes that copy until D3 removes it with the cluster. Its expressions are in git
before B2.

| Alert | Severity | Fires when | First look |
| --- | --- | --- | --- |
| VaultSealed | warning | one Vault server pod runs but is not ready for 5 min (sealed, or a standby that cannot reach the active node), once Vault has been initialised. Under shamir (the default, D208) this follows every pod restart, a node drain included, and does not clear by itself | shamir: unseal by hand (OPERATIONS → Vault → A pod is sealed); awskms: the KMS key, its role, the egress (OPERATIONS → Vault alerts (VaultSealed)) |
| VaultSealedQuorum | critical | two or more server pods run but are not ready for 5 min: no quorum with 3 replicas (same gate) | shamir: unseal each by hand (OPERATIONS → Vault → A pod is sealed; the second brings the leader back); awskms: the unseal key, its role (OPERATIONS → Vault alerts (VaultSealedQuorum) → Vault → Quorum loss) |
| VaultUnavailable | critical | no Vault server is ready for 10 min, whatever the reason (same gate) | OPERATIONS → Vault alerts (VaultUnavailable): sealed (shamir: unseal by hand), Pending or crash-looping |
| VaultNoActiveNode | critical | the Vault scrape works and no node reports itself active, 2 min (same gate) | OPERATIONS → Vault alerts (VaultNoActiveNode) → Vault → The leader is gone |
| VaultRaftPeersBelow3 | warning | autopilot counts fewer than 3 healthy voters, 10 min (only with an active node) | OPERATIONS → Vault alerts (VaultRaftPeersBelow3): which pod, its zone, `raft list-peers` |
| VaultSnapshotStale | critical | no successful snapshot for 13 h, or none in 13 h of a Vault that was ready throughout, while a server is ready (RPO 6 h; expiry would delete the last snapshot after 37 days) | OPERATIONS → Vault → Snapshots |
| VaultSnapshotFailing | warning | the latest scheduled snapshot run started over 30 min ago and has not succeeded, or none has ever succeeded while a server is ready | OPERATIONS → Vault → Snapshots: the failed Job's logs |
| VaultAuditFailing | critical | a node failed to write an audit entry in the last 10 min (`increase()`, or a counter that appears above 0) | OPERATIONS → Vault alerts (VaultAuditFailing): `vault audit list -detailed`, the audit volume |
| VaultVolumeFilling | warning | a Vault data or audit volume is over 80 % full, 15 min | OPERATIONS → Vault alerts (VaultVolumeFilling): patch the PVC only |
| VaultUpdatePending | info | Vault pods run the previous template for 24 h (updateStrategy OnDelete: the next node drain would roll them, D204) | OPERATIONS → Vault alerts (VaultUpdatePending): the pod-by-pod restart |
| VaultScrapeDown | warning | kube-state-metrics for vault (`ryft/vault-state`) is not scraped (not gated: rules loaded before the monitoring config warn until that scrape runs); or, once Vault has been initialised and while the StatefulSet exists, a server's scrape fails or is missing, or Fleet sends no kubelet volume stats for `vault`; 15 min: the Vault alerts are blind | OPERATIONS → Vault alerts (VaultScrapeDown): `vault_collector_peer`, the collector's `vault-ca` Secret, the NetworkPolicy peer |

The `Vault*` alerts read the two Vault scrapes (jobs `ryft/vault` and `ryft/vault-state`;
Collection above) of the in-cluster Vault (D207). Properties that are deliberate:

- **One rule set for both seals.** `vault_seal` (infra/aws/kubernetes, D208) is `shamir` by
  default: 5 shares, threshold 3, in the operator's 1Password, and every Vault pod that starts
  (a restart, an eviction, a node drain or replacement) stays sealed until the operator unseals
  it by hand. A sealed pod is then routine work that needs a person, not a fault, and it never
  clears by itself. Under `awskms` a pod unseals itself within seconds, so a sealed pod is a
  fault (the KMS key, its role, the egress). Neither the metrics nor the pod labels carry the
  seal type, so the summaries of `VaultSealed` and `VaultSealedQuorum` name both readings:
  "shamir: unseal by hand (OPERATIONS → Vault → A pod is sealed); awskms: the KMS key or its
  role". The severities stay: one sealed pod costs the failure tolerance (warning; under shamir it
  stays at 0 until the operator acts, and the PDB keeps a drain from taking a second ready pod),
  two cost the quorum (critical).
- **Sealed is read from readiness.** A sealed node still answers the metrics scrape, but its
  `vault_core_unsealed` expires two minutes later and it never reports `vault_core_active`
  (checked on 2.2.1). The readiness probe is strict (`/v1/sys/health?standbyok=true`: only an
  unsealed active or standby node is ready), so "running and not ready" is the signal.
- **The "initialised" gate.** Before the first init every pod runs and none is ready, by design
  (D196), and the collector may not reach the servers (`vault_collector_peer` is off until the
  operator's own init). `VaultSealed`, `VaultSealedQuorum`, `VaultUnavailable`,
  `VaultNoActiveNode` and the server and kubelet branches of `VaultScrapeDown` fire only once
  Vault has been initialised: a server pod labelled `vault-initialized=true` in the last hour
  (`kube_pod_labels`), or a snapshot that has succeeded once (the CronJob's `lastSuccessfulTime`
  stays in its status). The label half is short-lived: Vault's service registration resets the
  labels at every process start (`vault-initialized=false`, `vault-sealed=true`) and sets
  `vault-initialized=true` again only on unseal, so the label means "unsealed at least once since
  this process started" (the state Vault's server command hands its Kubernetes service registration at start (`command/server.go`: initialized false, sealed true) and the registration's `NotifyInitializedStateChange(true)` on the unseal path (`vault/`), as read in the #153 re-read; not checked against 2.2.1's source here, **to verify in the kind drill**). When every pod restarts and stays sealed (under shamir any
  restart of all three, a node group replaced, until the operator has unsealed them; under
  awskms the KMS key unreachable), the label half lapses an hour after the last unsealed sample,
  and new pods that never start carry no labels at all. Only the snapshot half holds through
  that, so a Vault down for a week keeps paging only if a snapshot has succeeded: the first
  snapshot, taken by hand right after the init and infra/vault's apply (OPERATIONS → Vault
  metrics (once)), is a precondition of these alerts, not an extra, and came before the rules
  were loaded (`make alerts-apply`, gone with D236). If Vault in fact writes `vault-initialized=true` at start for an
  initialised storage (the kind drill decides), the label half holds through a sealed restart
  too; the rules are right either way. While one pod is sealed the other two keep the label, so
  `VaultSealed` keeps firing however long the unseal takes (a test keeps one pod sealed for eight
  hours, from a restart to the unseal by hand, and checks that it resolves then).
- **From empty.** The rebuild starts with neither half: the Vault add-on never ran before
  2026-11-10, so no old `kube_pod_labels` or `lastSuccessfulTime` series exist, and the windows
  (1 h, 6 h) would have dropped them anyway. As read, a shamir init alone sets no label (the
  node is initialised but sealed) and the first unseal does (**to verify** with the label
  source above). The cluster is quiet until then. More to the point, no collector runs during
  the bootstrap (its token comes from this Vault, Collection → From an empty cluster), and the
  alerts are loaded only after the bootstrap, infra/vault's apply, the collector and the first
  snapshot. So the minutes between the first unseal and the third (two pods running unready)
  cannot raise `VaultSealedQuorum`, whichever way the label behaves. A
  `ryft-vault` group that is already live in the ruler from an earlier `make alerts-apply` sees
  no Vault series at all until the collector runs and warns only through `VaultScrapeDown`'s
  ungated half.
- **Sealed: one warns, two page.** One sealed pod costs the failure tolerance, not the service
  (`VaultSealed`, warning); two or more of three leave no quorum (`VaultSealedQuorum`,
  critical, one alert instead of one per pod). `VaultUnavailable` covers the rest of "no server
  ready" (Pending, crash loops).
- **A lost audit write pages.** `VaultAuditFailing` is critical (the lead's call): the audit
  trail is the one record of who read which secret, a failure means requests go unrecorded (or,
  with both devices failing, refused), and its causes (a full audit volume, a broken device)
  only get worse.
- **The snapshot pages.** `VaultSnapshotStale` is critical at 13 hours (two missed runs): the
  bucket's lifecycle deletes snapshots 30 days after they were written and the versions 7 days
  later, so a job broken for 37 days leaves no snapshot to restore. It counts only while a pod
  is ready (a Vault that is down pages as `VaultUnavailable`, and no snapshot can be taken);
  a never-successful job pages once Vault has been ready for 13 hours, and
  `VaultSnapshotFailing` warns 30 minutes after the first failed run.
- **Blind is a warning, not silence.** `VaultScrapeDown` fires when kube-state-metrics' vault
  scrape is gone (also before the monitoring values are applied), when a server's scrape fails
  or is missing, and when Fleet sends no `kubelet_volume_stats_*` for `vault` (VaultVolumeFilling
  would be blind). The last two count only once Vault has been initialised and while the
  StatefulSet exists, so a cluster without Vault, or one before its init, is quiet.
  `VaultNoActiveNode` stays quiet while every server scrape fails (blind is not leaderless).
- **Not here:** restarts (`PodRestarting` covers `vault`), the certificate
  (`CertificateExpiring` has no namespace filter), and the log-based rules (an audit device
  that stopped, a root-token login): Loki rules need `lokitool`, which `make alerts-check` does
  not run yet; they are a follow-up (D199, group `ryft-vault-logs`).

Series budget: about 25 from `ryft/vault` (three pods; the keep list in
`deploy/monitoring/k8s-monitoring.yaml`, the autopilot and audit series from the active node
only, token gauges per policy), about 18 from `ryft/vault-state` (one CronJob, one StatefulSet,
three containers, three `kube_pod_labels`), and `up` plus `scrape_samples_scraped` per target:
under 65 in total. The pod-label allowlist adds a `kube_pod_labels` series per pod cluster-wide
in kube-state-metrics itself, but the scrape keeps namespace `vault` only.

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
(`debug`, `info` — default —, `warn`, `error`) sets the level; on Cloud Run it is set in `deploy/run/config.env`
(on EKS, until D3, in `deploy/k8s/base/<service>/config.env`). On Cloud Run (`K_SERVICE` or `CLOUD_RUN_JOB` set)
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
| `code` | the access line of a refused request | The error code (`invite_required`, `consent_required`, `import_in_progress`) or, for a `401`/auth `503`, the reason (`expired`, `signature`, `deleted`, …). |
| `result`, `plans`, `sessions`, `created`, `unchanged`, `updated`, `rejected`, `days`, `duration_ms` | the api's `train import`, one per import that took the lease | Its result as in `ryft_train_imports_total`, the items sent and what happened to them (counts over plans and sessions), the days queued. Never a title, a note, a value or an id list (D114, D117; `cmd/api/train_import_logs_test.go`). |
| `gate` | the access line of a request a gate in `warn` mode let through | The code(s) it would have been refused with, as in `X-RYFT-Gate`. |
| `picked`, `xff_index`, `xff_entries` | api's and ingest's `client address`, at most one a minute per instance, for a request with `X-Forwarded-For` | Which entry `httpx.ClientKeys` keyed the per-address limits by (the edge's and `/v1/admin`'s, D240): `picked` `xff` (with `xff_index`, its position from the left, from 0) or `fallback` (no entry would do: the shared key), and how many entries the header had. No address, whole or in part, no request ID, user or route (D222; runbook step 9 reads it). |
| `err` | errors and warnings | The internal error. Clients only ever see `"internal error"` and the request ID. |
| `day`, `model_version`, `inputs_changed` | scoring's `scored day`, one per day scored | The day, the model that scored it, and whether its history inputs changed. The days after it are scored again when they did or when the event was a retry (`inputs_changed` is then often `false`: the first attempt wrote the change): `rescored days after a history change`. No score, input or state: those are in `daily_scores` and the admin view. |

**Lifecycle lines.** A service started through `internal/app` (scoring and notify; api and
ingest follow) logs its start and stop with these lines and keys (the closed sets are
pinned by `internal/app/logs_test.go`, D114, D117). OPERATIONS → A deploy: what the old pod
logs has the Insights queries.

| Message | Keys | When |
| --- | --- | --- |
| `listening` | `addr` | the HTTP port is open (the service listens before it opens the pool: Health paths above) |
| `metrics listening` | `addr` | the `/metrics` port is open; only where there is one (compose), not on Cloud Run |
| `events` | `transport` (`pubsub`), `project`, `endpoint`, `push_auth` (`google_oidc`, `insecure`) | the transport is set up: where it publishes, how it checks push and tick tokens |
| `push and tick tokens are NOT checked (ALLOW_INSECURE_PUSH=true): local development only` | none | compose only; refused on Cloud Run |
| `config`, `beta config`, `apns config`, `apns`, `db`, `events`, `build`, `serve` | `err` | the start failed in that area; the process exits 1. `serve`: the HTTP port would not bind (it used to be logged while the service ran on without probes). `db` on Cloud Run with a refused login (SQLSTATE 28, `ECIRCUITBREAKER`): the instance stays up and holds instead (D221), and the line comes again for each refused retry |
| `db: the login passed on a retry` | none | a tick's login retry passed while the start held: the gate opens and that tick is served (D221) |
| `start interrupted by the signal` | `step` (`config`, `db`, `events`, `build`, `serve`) | a SIGTERM before the start was done |
| `serve` | `task` (`http`), `err` | the HTTP server returned an error before the shutdown: it stops the service (exit 1). The consumer and timer tiers (`consume`, `job`, `background`) are gone: events are pushes and timers are the tick (D218, D219) |
| `metrics server` | `err` | the `/metrics` port could not be bound or its server failed (compose); the service runs on without it |
| `panic` | `task` (a server), `check` (a ready check: `db`, `auth`) or `step` (a tick step), `panic`, `stack` | a server's panic: counted in `ryft_goroutine_panics_total`, the service shuts down in order and exits 2. A check's: counted there too, the service runs on. A tick step's: counted in `ryft_http_panics_total` (`route="POST /internal/tick"`), the tick goes on with its next step |
| `tick` | `result` (`ok`, `error`), `message_id`, `took` | one per tick handled (`ryft_ticks_total`). On Cloud Run the log-based metric `ryft_ticks` counts these lines by `result` alone (D236; policy 3, Tick missing): renaming the message or the key, or changing its values, is a change to `infra/gcp/monitoring.tf` too |
| `tick: step failed` | `step`, `err`, `took` | a step returned an error or panicked; the tick goes on. `step` is a closed set: `db ping`, `outbox sweep`, `scoring pass`, `stale check`, `account reconcile`, `invite settings`, `invite email purge`, `finally` |
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

**Finding a request** a user reports (the app shows or logs `request_id`):

```sh
for s in ingest api scoring notify; do
  kubectl -n ryft-$s logs -l app=$s --tail=-1 --prefix --since=48h --max-log-requests=10; done \
  | grep 01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c
```

`--tail=-1` matters: with `-l`, `kubectl logs` shows only the last 10 lines of each pod
by default, so a grep finds nothing. `logs deploy/<name>` reads one pod of several.

Pod logs only reach back to the last restart. Everything older is in CloudWatch.

### In CloudWatch

Fluent Bit (`infra/aws/kubernetes`, `logs.tf`) ships the container logs of the `ryft-*`
namespaces and of Traefik to the log group `/ryft/ryft-prod-euc1/containers`. The group
is KMS-encrypted and keeps 14 days; there is one stream per container. Each JSON line is
parsed into fields under `data` (`data.request_id`, `data.user_id`, `data.msg`, …).
CloudWatch Logs Insights:

```text
fields @timestamp, data.service, data.msg, data.status, data.err
| filter data.request_id = "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"
| sort @timestamp asc
```

**Who can read them:** anyone whose IAM identity may call `logs:GetLogEvents` or
`logs:StartQuery` on the group *and* `kms:Decrypt` on the key `alias/ryft-prod-euc1-logs`.
Today that is the account's administrators only. Treat any grant as access to the users'
personal data, and until 14 days after the deploy that stopped logging health values
(OPERATIONS → Data protection (logs), older lines) to their scores: no broad
`CloudWatchLogsReadOnlyAccess` for others, and no export to buckets or third parties. Cost at alpha volume is around €1 a month, plus €1 for the key.

### Vault's logs

The one log stream that goes to Grafana Cloud on purpose (D197, D198): namespace `vault`, from
`alloy-daemonset` (the node's log files) to the stack's Loki, through the chart's own
`podLogsViaLoki` pipeline (`deploy/monitoring/k8s-monitoring.yaml`, **off** until the steps in
OPERATIONS → Vault logs are done). Fleet Management's pod-log pipeline excludes `ryft-.*|vault`
and Beyla skips `vault`, both set before the collector reads the nodes' log files: on the old
cluster before the Vault add-on, on a cluster rebuilt from empty before `make monitoring-apply`
(the Vault pods run by then; Collection → From an empty cluster, with the Explore check that
`{namespace="vault", container=~".+"}` is empty), so nothing of it arrives shaped by remote
configuration.

| Stream label | Values |
| --- | --- |
| `cluster`, `namespace`, `pod`, `container` | from the pod (`namespace` is always `vault`) |
| `job` | `ryft/vault` |
| `stream` | `server` (Vault's JSON log: a line with `@level` from container `vault`), `audit` (the stdout audit device: a line with `type` `request` or `response`), `other` (the start script, the snapshot job: its init container's `@level` lines too) |

Nothing else becomes a label or structured metadata, and no line is rewritten: a value inside
a line (a path, an accessor) never turns into a stream. Queries:

```text
{namespace="vault", stream="audit"} | json | request_path=~"sys/.*"     # administrative requests
{namespace="vault", stream="audit"} | json | auth_display_name="root"   # root-token use (each generate-root session's start, D209; anything else: ask why)
{namespace="vault", stream="server"} | json | __error__="" | _level=~"warn|error"
```

**What an audit entry holds.** One entry per request and per response: time, the operation and
the request path (`kv-api/data/config`, `sys/storage/raft/snapshot`), the mount, the client's
address (a pod IP; `127.0.0.1` through the port-forward), the auth's display name, entity and
policies (a service's Kubernetes role; for the operator, the session's tokens from
`vault operator generate-root`, D209: `root`, then the hour's admin token), token accessor and TTLs.
Secret values, tokens and request or response bodies' values are HMAC-SHA256'd with the audit
device's salt (Vault's default; no `hmac_accessor=false`, no unhashed keys), so a line names
*which* secret was read by *whom*, never its value. No tester data: the testers never talk to
Vault. Vault does hold a little personal data as KV values (the e-mail addresses of the smoke
user and of the App Review demo account); the audit lines carry those only as HMACs.

**Data protection.**

- **Art. 32:** the audit trail of who reads which credential is a security measure in its own
  right (integrity and confidentiality of the processing systems, and detecting their misuse).
  The copy in Grafana Cloud survives the cluster and the audit volume, which a compromised
  Vault host could alter; access to the stack is limited to its members (OPERATIONS →
  Monitoring: keep them to yourself). It is **not** tamper-proof: anyone with a token that has
  `logs:write` on the stack (`GC_TOKEN`, which the collectors hold) can push lines with
  `stream="audit"`. It is evidence only together with the file device on the audit volume.
- **Processor:** Grafana Labs, already the processor for the metrics (stack in London, D86;
  the UK is a third country with an adequacy decision, Art. 45 GDPR). The audit lines add
  personal data of one person, the operator (no OIDC login in the rebuild, D209: an operator
  session is a token from `vault operator generate-root`, so the lines show token display names
  such as `root` and `token`, not a personal subject; D198's OIDC subject applies only if OIDC is
  added later; the client address of an operator session is `127.0.0.1`, through the
  port-forward), and none of the testers. Retention is the stack's log retention (Grafana Cloud → the stack → Usage/limits).
- **Your steps, before turning the pipeline on:** accept Grafana Labs' data processing
  agreement (grafana.com → Legal → DPA, in the organisation's settings), and add an entry to
  the record of processing activities: purpose "security monitoring of access to credentials",
  categories "the operator's token display names (a pseudonymous subject once OIDC exists), client addresses (pod IPs; `127.0.0.1` for
  operator sessions), service account names, timestamps, HMACs of KV values", recipient
  Grafana Labs (processor), the retention above, and the transfers with their mechanisms: the
  UK (the stack's region; adequacy decision) and the US entities and sub-processors Grafana's
  DPA lists (the mechanism the DPA names for each, for example the EU-US Data Privacy Framework
  or Standard Contractual Clauses). **To verify** against the DPA and its sub-processor list
  before the entry is written; do not record "no transfer".
- **The privacy policy does not change:** it covers the app's users, whose data never reaches
  Vault or its audit log.
