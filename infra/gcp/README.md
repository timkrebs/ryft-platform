# infra/gcp

RYFT on GCP serverless (D216–D227, `docs/work/2026-11-14-gcp/design.md`): the project `ryftlab-prod`
in `europe-west1`. State and runs live in HCP Terraform, org `ryft-lab`, workspace `ryft-gcp`
(CLI-driven, remote execution, GCP dynamic credentials). The user applies it; nothing else does. The
runbook is `docs/OPERATIONS.md` → **GCP**.

## What this root owns

| File | What |
| --- | --- |
| `project.tf` | the API keep list (`google_project_service`, `disable_on_destroy = false`; 16, and Compute Engine and Certificate Manager from `edge = "built"` on), Data Access audit logs (`DATA_READ`) for Secret Manager, STS and IAM credentials (every secret read and token mint), the Pub/Sub and Cloud Scheduler service agents |
| `iam.tf` | the accounts `ryft-<api,ingest,scoring,notify,migrate>`, `ryft-push`, `ryft-deployer` (no key, ever; `grafana-reader` went with Grafana Cloud, D236); the deployer's rights (**The deployer**, below); Pub/Sub's token creator on `ryft-push` |
| `wif.tf` | the GitHub pool `github` and provider `github-actions` (condition by repository and owner ID, `deploy.yml` on `main`, environment `deploy` or `site`, a GitHub-hosted runner); one principal set per environment, `ryft-deployer` bound to `deploy`'s alone (D233), `site`'s for `site-deployer` in ryftlab-site (by hand, step 10a) |
| `secrets.tf` | the 13 secret containers in use (ticket B's 10, `posthog-project-key`, D231, App Attest's token ring `app-attest-token-key`, api and ingest, D247, and Garmin's OAuth client secret `connect-garmin-client-secret`, api alone, D257: empty until phase 2), `prevent_destroy`, 30-day delayed destruction, accessors on each secret; `retired`: `otlp-token` and its accessor without `prevent_destroy`, on their way out (OPERATIONS → GCP 11.6). **No values**: they never pass through state |
| `pubsub.tf`, `pubsub.auto.tfvars.json` | topics (messages stored in `europe-west1` only; `enforce_in_transit` stays off, so the Scheduler's publish and an operator's `gcloud pubsub` through the global endpoint still work), push and hold subscriptions, publisher bindings per topic, the agents' bindings. The JSON is written by `go run ./cmd/pubsub-topology` (PR A); do not edit it by hand once that exists |
| `scheduler.tf` | the job `ryft-tick` (every 15 minutes) |
| `nutrition.tf` | optional monthly `nutrition-etl-job`, its dedicated runtime and scheduler identities, and a pinned database secret reference (D270) |
| `registry.tf` | Artifact Registry `ryft`: the 5 newest versions of each image kept, every other version deleted after 30 days (tagged or not); scanning off |
| `run.tf` | who invokes the four services (one authoritative `run.invoker` binding each), and the domain mappings `api.` and `ingest.ryftlab.de` (only with `serve = true`, and gone at `edge = "locked"`) |
| `dns.tf` | the zone `ryftlab.de` (`prevent_destroy`, DNSSEC off) with every record the Route 53 zone held (deleted 2026-11-19, D246), CAA with `pki.goog`, Search Console's TXT, and `api.`'s and `ingest.`'s record sets (`google_dns_record_set.host`): CNAMEs to the mappings, A records at the edge's address from `edge = "live"` on |
| `edge.tf` | the edge (D242, from `edge = "built"` on): a global external Application Load Balancer on one IPv4 address (no IPv6), a serverless NEG and a backend service per public service, a URL map by host (anything else 301 to the site), an SSL policy (MODERN, TLS 1.2+), Certificate Manager certificates by DNS authorization in one map, HTTPS on 443 and a redirect on 80; one Cloud Armor policy: six WAF rules in preview (`local.edge_waf_preview`), none of them on the direct-connection callback (`/v1/connect/<provider>/callback`) or ingest's hook (only `ingest.` + `POST` + `/connect/<provider>/push`, `local.edge_waf_skip`, D257, review P2-5); the hook's throttle at 1900 (`ingest.` + `/connect/`, 6,000 per 60 s per address, then 429, never banned, `local.edge_hook`); an enforced rate rule per address at 2000, allow by default. Output `edge_ip` |
| `site.tf`, `site.auto.tfvars` | the site's records on Firebase Hosting (C1, D233): the apex A set and its `hosting-site=` TXT (joins the apex TXT set), and www's A set and TXT or its CNAME, from the values the Firebase wizard shows. Empty adds nothing |
| `logging.tf` | the bucket `ryft-logs` (europe-west1, 14 days) and the sink into it; always the exclusion `connect-callback` (every entry whose absolute `httpRequest.requestUrl` is `http(s)://<host>/v1/connect/<provider>/callback`, with a query or none, anchored: the callback's OAuth state and code, D257, review P2-1); from `edge = "built"` on two more: the load balancer's requests but Cloud Armor's denials and preview matches, and api's and ingest's Cloud Run request log below 5xx (D242) |
| `monitoring.tf` | Cloud Monitoring, RYFT's only observability (D236): the log-based metrics `ryft_ticks` (by `result`), `ryft_errors` and `ryft_notices` (D244: api's `feedback received`, `admin signed in` and `admin auth rejected` but for reasons `missing` and `expired`, by `kind`), the uptime checks (the site's `/`, `/privacy` and AASA always; api's and ingest's `/health/ready` while they're public; every 5 minutes from 3 locations), the dashboard "RYFT · Overview". All free at RYFT's volume, so ungated. From `edge = "built"` on: `ryft_edge_denials` and `ryft_edge_preview_matches` (by `priority`) and four edge tiles |
| `alerts.tf` | the six alert policies, one condition each. Uptime, 5xx, missing tick, dead letters, and backlog use email then the Google Cloud app. Notices uses the existing email only (D281). The `alert_app_channel` gate still controls the policies and tick alerts require a running tick. The plan fails if the two channels resolve to the same channel (D237). Six is the cap, a test (`six_conditions_at_most`) |
| `checks.tf` | warnings for what fails silently: the `_Default` log sink still on, a mapping not Ready, a disabled email channel (the app's has no data source, D237: step 11.4 tests it) |

## What it does not own (design §9)

- **The Cloud Run services and the job `migrate`**: CI owns them (`deploy/run`, `deploy.yml`). This
  root names them, it never defines them; that is why the invokers and mappings wait for `serve`.
- **The budget** "RYFT 50 EUR": made by hand in the console, with its rules (runbook step 3).
- **Its own credentials**: the HCP Terraform pool and the accounts `hcp-terraform-plan` and
  `hcp-terraform-apply` (the bootstrap, runbook step 2). Terraform cannot create what it signs in with.
- **The two notification channels**: the email `ryft-timkrebs` and the Google Cloud app's device,
  made and picked by hand in the console. `alerts.tf` reads the email's by display name; the app's
  comes in by its resource name, a workspace variable in no committed file (step 11.1, D237).
- **Error Reporting's notifications**: a console toggle (step 11.5).
- **The site project `ryftlab-site`**, its deploy account and its custom domains (by hand, step 10).
  Only the site's DNS records come here, with C1.
- **The `_Default` log sink**: switched off once by hand after apply 1 (`checks.tf` warns until then).
- **The secret values**: `gcloud secrets versions add` (step 5).

## Recover the `ryft_notices` plan

The failed plan requested replacement of `google_logging_metric.notices` because the provider treats a change to the label descriptor set as a replacement. D269 pins the live `kind` descriptor and protects the metric from deletion. Terraform can then update the filter and the alert policy in place. Start a new plan after a partial apply so Terraform refreshes the Cloud Armor change that already succeeded. [Google Cloud Logging metrics](https://docs.cloud.google.com/logging/docs/logs-based-metrics/counter-metrics)

From the `plattform` repository root, with the corrected code and `nutrition_etl` unset for this recovery step:

```bash
terraform -chdir=infra/gcp plan -out=notices-recovery.tfplan
terraform -chdir=infra/gcp show -no-color notices-recovery.tfplan
```

Expect `google_logging_metric.notices` and its alert policy to update in place or show no change. Check for any deletion or replacement before applying. This guard reads only the action metadata from the plan.

```bash
set -o pipefail
terraform -chdir=infra/gcp show -json notices-recovery.tfplan | jq -e '
  any(.resource_changes[]; .address == "google_logging_metric.notices" and
      (.change.actions == ["update"] or .change.actions == ["no-op"])) and
  all(.resource_changes[]; (.change.actions | index("delete")) == null)
'
```

Review the rest of the plan before applying the fresh file. When recovering a plan from the Connections stack, generate its replacement with that stack’s resource definitions and the D269 fix so the full plan reflects the resources already applied. Keep the existing metric and dependent alert policies in place throughout the recovery.

For the D281 email-only update on an aligned workspace, the reviewed plan should update `google_monitoring_alert_policy.notices[0]` in place and attach just the existing email channel. Check the notification channel list, the metric and the other five policies before applying the saved plan.

## Nutrition on Cloud Run

The existing API serves the scores and guidance and its 15-minute tick runs the morning calibration. The separate Cloud Run Job imports the public food catalog once a month. It runs one task with 2 vCPU, 4 GiB, one retry and an 1800-second task timeout. The scheduler uses OAuth with a job-scoped invoker. The job uses the existing PostgreSQL database and registry. DuckDB reads OFF with HTTP ranges and uses ephemeral storage. The runtime starts only for executions. [Cloud Run scheduled jobs](https://docs.cloud.google.com/run/docs/execute/jobs-on-schedule) · [Cloud Run pricing](https://cloud.google.com/run/pricing)

Merge the Nutrition backend and infrastructure through the normal review and CI flow. Run the following from `plattform` on green `main` after hosted validation and sign-off on the catalog sources and health-data consent.

1. In HCP workspace `ryft-gcp`, set the Terraform variable `nutrition_etl` with **HCL** enabled and value `{}`. Plan the dedicated service accounts, the `nutrition-etl-db` secret container and its one accessor.

   ```bash
   terraform -chdir=infra/gcp plan -out=nutrition-bootstrap.tfplan
   terraform -chdir=infra/gcp show -no-color nutrition-bootstrap.tfplan
   ```

   Apply the reviewed bootstrap plan. The image and secret version pins are needed in the next phase.

2. Publish the ETL image and deploy the API through the existing workflow. The `migrate` job applies migrations through `0046` before services roll. Read the full image reference from the workflow's **Nutrition ETL image** summary.

   ```bash
   gh workflow run deploy.yml --ref main -f publish_nutrition_etl=true
   gh run list --workflow deploy.yml --commit "$(git rev-parse HEAD)" --event workflow_dispatch --limit 1
   ```

   The normal deploy gates and production smoke also apply to this dispatch. Image publication reuses the same registry writer and vulnerability scan. Terraform owns the ETL Job itself.

3. Once the catalog migration is installed, create a dedicated PostgreSQL login. Run the following in `psql` as the existing database operator. `\password` prompts for the ETL password.

   ```sql
   CREATE ROLE nutrition_etl_login LOGIN;
   \password nutrition_etl_login
   GRANT ryft_nutrition_etl TO nutrition_etl_login;
   ```

   For Supabase use the session pooler host and project from `deploy/run/config.env`, port `5432`, database `postgres` and the dedicated login with its `.<project-ref>` suffix. Include `sslmode=verify-full`. The image uses the same Supabase CA as the Platform API. Omit `sslrootcert` or set it to `/etc/ryft/supabase-ca.crt` inside the image. The role also needs database `TEMP` access for COPY staging. If the current secret version passes preflight, reuse it. Otherwise add a new version. The command prints only the version identifier. [Supabase SSL enforcement](https://supabase.com/docs/guides/platform/ssl-enforcement) · [PostgreSQL TLS verification](https://www.postgresql.org/docs/18/libpq-ssl.html)

   ```bash
   printf 'Nutrition ETL PostgreSQL DSN: '
   IFS= read -r -s nutrition_dsn
   printf '\n'
   printf '%s' "$nutrition_dsn" | gcloud secrets versions add nutrition-etl-db \
     --project=ryftlab-prod --data-file=- --format='value(name)'
   unset nutrition_dsn
   ```

   Before updating the scheduled job, have the authorized database operator check the selected secret and the candidate image on a trusted workstation with access to the pooler. Set `RYFT_ETL_IMAGE` to the full digest-pinned image reference from GitHub Actions and `RYFT_ETL_DB_VERSION` to the numeric version you intend to deploy. Run this with shell tracing disabled. Only pass/fail is printed. The candidate opens a verified TLS connection and checks privileges without changing database rows.

   ```bash
   (
     set +x
     set -euo pipefail
     : "${RYFT_ETL_IMAGE:?Set the candidate image by digest}" "${RYFT_ETL_DB_VERSION:?Set the numeric secret version}"
     [[ $RYFT_ETL_IMAGE =~ ^europe-west1-docker\.pkg\.dev/ryftlab-prod/ryft/nutrition-etl-job@sha256:[a-f0-9]{64}$ ]]
     [[ $RYFT_ETL_DB_VERSION =~ ^[1-9][0-9]*$ ]]
     gcloud secrets versions access "$RYFT_ETL_DB_VERSION" --secret=nutrition-etl-db --project=ryftlab-prod |
       docker run --rm -i --entrypoint python --env CLOUD_RUN_JOB=nutrition-etl-job "$RYFT_ETL_IMAGE" -c '
   import os
   import sys
   import psycopg
   from import_pg import connect

   os.environ["DATABASE_URL"] = sys.stdin.read()
   try:
       with connect() as conn:
           ok = conn.execute("""
               SELECT pg_has_role(current_user, %s::name, %s)
                 AND has_database_privilege(current_user, current_database(), %s)
                 AND has_table_privilege(current_user, %s, %s)
                 AND NOT has_table_privilege(current_user, %s, %s)
           """, ("ryft_nutrition_etl", "MEMBER", "TEMP", "nutrition.foods", "INSERT", "nutrition.meals", "SELECT")).fetchone()[0]
   except (ValueError, psycopg.Error):
       sys.exit("Nutrition ETL database preflight failed.")
   if not ok:
       sys.exit("Nutrition ETL database role preflight failed.")
   print("Nutrition ETL database preflight passed.")
   '
   )
   ```

   Keep the current job pin if this check fails. Correct the dedicated login DSN or confirm the reviewed CA against the Supabase Dashboard and run preflight again. Follow the bounded rollout below using the same image digest and secret version that passed.

4. Set `nutrition_etl` in HCP with the actual image digest and numeric secret version. Start with a bounded import and paused schedule.

   ```hcl
   {
     image_digest            = "sha256:<digest shown in the GitHub Actions summary>"
     database_secret_version = 1
     row_limit               = 1000
     schedule_enabled        = false
   }
   ```

   ```bash
   terraform -chdir=infra/gcp plan -out=nutrition-job.tfplan
   terraform -chdir=infra/gcp show -no-color nutrition-job.tfplan
   ```

   After reviewing and applying the job plan, run one bounded import and check its completion and aggregates.

   ```bash
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait
   gcloud run jobs executions list --job=nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --limit=3
   gcloud logging read 'resource.type="cloud_run_job" AND resource.labels.job_name="nutrition-etl-job"' \
     --project=ryftlab-prod --freshness=1h --limit=20
   ```

5. Set `schedule_enabled = true` in the reviewed configuration after the bounded validation. The schedule is 04:00 UTC on the first day of each month. Keep `row_limit` until database load, runtime and source volume have been measured for the intended scope. Use `full_import = true` when ready for unbounded provider scans. `schedule_enabled = false` pauses future launches, and the global stop line also pauses them.

6. Enable `nutrition.enabled` for a tester at 0% rollout with the existing admin workflow. Read the existing flag and include its allow-list in the replacement request. The tester can then configure a profile and call the guidance endpoint with their own current consent. See the [feature flag commands](../../docs/OPERATIONS.md#feature-flags) and the [Nutrition API](../../docs/API.md#nutrition-to-recovery).

### Existing Cloud SQL and optional vectors

To use Cloud SQL, point both the API and ETL at the intended existing catalog database. Set `cloud_sql = { instance_connection_name = "ryftlab-prod:europe-west1:existing-db", database = "ryft", user = "nutrition_etl_login" }` in `nutrition_etl`. Pin a secret version containing the password for this mode. Terraform enables the SQL Admin API, grants the ETL identity Cloud SQL Client and mounts `/cloudsql`. This socket configuration uses an instance reachable via the public-IP Auth Proxy path. A private-IP-only instance needs a separate network path. [Cloud SQL connections from Cloud Run](https://docs.cloud.google.com/sql/docs/postgres/connect-instance-cloud-run)

The API can use pgvector after applying the optional extension, index and embeddings schema to the same database. Embedding generation is run separately and can incur API charges. See the [ETL runbook](../../scripts/etl/README.md#optionale-embeddings).

## The gates

`gates.auto.tfvars` is committed, so each flip is a reviewed one-line PR. HCP Terraform loads
`*.auto.tfvars` in remote runs. Never set `serve`, `public_invokers` or `edge` as workspace variables.
`site.auto.tfvars` (the site's DNS values) works the same way: committed, reviewed, never a
workspace variable, and `tests/site.tftest.hcl` holds it to its five keys.

| `serve` | `public_invokers` | Invokers (authoritative, per service) | Deployer's `run.developer` | Mappings and their DNS | Tick |
| --- | --- | --- | --- | --- | --- |
| `false` | any | no binding (the services don't exist yet) | project-wide | none | paused |
| `true` | `true` | `ryft-push` on all four, plus `allUsers` on api and ingest | on the four services and the job | `api.`, `ingest.` | runs |
| `true` | `false` (the 20 EUR stop line) | `ryft-push` alone on all four | on the four services and the job | `api.`, `ingest.` | paused |

The invoker bindings are authoritative for the role: an invoker added by hand (`allUsers` on
scoring, say) is removed at the next apply. The stop line's gcloud commands (OPERATIONS → GCP →
The €20 stop line) remove `allUsers` by hand at once; `public_invokers = false` leaves exactly
that, so the next apply neither undoes them nor adds it back.

`edge` (D242, runbook step 16) is `off` unless `serve` is true, and moves one phase a PR, back the
same way. `scripts/check_cloudrun.py` (`make cloudrun-check`, CI's `manifests` job, which a change
of `gates.auto.tfvars` selects) holds `deploy/run`'s ingress to it: api's and ingest's
`internal-and-cloud-load-balancing` only at `live` or `locked` and with `XFF_TRUSTED_HOPS` above 1,
and `locked` only with it on both (D242).

| `edge` | The load balancer and Cloud Armor | `api.`, `ingest.` in DNS | Domain mappings | The sink |
| --- | --- | --- | --- | --- |
| `off` | none | CNAMEs to the mappings | yes | everything but `connect-callback` |
| `built` | built, nothing points at it | CNAMEs to the mappings | yes | everything but the three exclusions |
| `live` | serving | A records at `edge_ip` | yes (the way back) | the same |
| `locked` | serving | A records at `edge_ip` | none | the same |

## The deployer

`ryft-deployer` (CI's `deploy.yml`, through WIF) holds `run.developer`, `iam.serviceAccountUser` on
the five runtime accounts, and `artifactregistry.writer` on `ryft`. Never an IAM or admin role:
`run.developer` has no `setIamPolicy`. Where `run.developer` is bound follows `serve`:

- **Before apply 2** it is project-wide, because CI creates the four services and the job at step 8,
  and a binding on a service can't exist before the service does.
- **From apply 2 on** it is bound on each service and on the job `migrate` only, and the
  project-wide binding goes. The deployer then updates what exists and can create nothing.

**A new service or job needs the project-wide binding back for its first deploy.** That is two PRs:
1. Its account (the name in `local.runtime`, `locals.tf`, with what else it needs), and `count` of
   `google_project_iam_member.deployer_run_developer` (`iam.tf`) set to `1`, with its pin in
   `tests/gates.tftest.hcl` (`apply_2_serve_true_public_true`). Apply: the project-wide binding
   added. Then its first deploy creates it.
2. The name in `local.services` (or `local.jobs`), and `count` and the pin put back. Apply: its
   bindings on the resource added, the project-wide one destroyed.

Runbook step 9's fallback (a permission error on `run.*` after apply 2) is PR 1 alone, and nothing
else.

## The applies

The arithmetic of each count is in the runbook (steps 4, 9, 11, 14a.9 and the stop line).

1. **Apply 1** (`serve = false`, runbook step 4): everything except the invokers, the deployer's
   bindings on the services and the mappings. On a rebuild today 134 to add, 0 to change, 0 to
   destroy: the 118 applied (116 before `posthog-project-key`, D231), less `grafana-reader` and its
   binding, plus D236's 6 (2 log-based metrics, the site's 3 uptime checks, the dashboard),
   D244's `ryft_notices`, D247's `app-attest-token-key` with its accessor (2), and D257's
   `connect-garmin-client-secret` with its accessor (2) and inbox topology (7); `monitoring.tf`'s
   D257 metric `ryft_connect_pushes` comes on top.
2. **Apply 2** (`serve = true`, step 9, after CI's first deploy at step 8): 15 to add (4 invoker
   bindings, 4 + 1 deployer bindings on the services and the job, 2 mappings, 2 CNAMEs, api's and
   ingest's uptime checks), 1 to change (the tick resumes), 1 to destroy (the deployer's project-wide
   `run.developer`). 13 to add before D236.
3. **The stop line** (`public_invokers = false`): after its gcloud commands 0 to add, 0 to change, 2
   to destroy (api's and ingest's uptime checks), 3 with alerting on (and the tick policy); without
   them 3 to change more (api's and ingest's invoker bindings, the tick).
4. **Step 11** (D236, the PR that removes Grafana): 8 to add (2 log-based metrics, 5 uptime checks,
   the dashboard), 0 to change, 2 to destroy (`grafana-reader` and its `monitoring.viewer`) with the
   gate off; 13 to add with the app channel set (and the 5 policies). The runbook's 11.2
   has the stop line's counts. **Step 11.8** (D237, once, on the policies applied with 11.3): 0 to
   add, 5 to change (each policy's second channel, the email's twice so far, becomes the app's), 0
   to destroy; 4 to change if the policy you edited in the console already reads exactly [email, app].
5. **Apply 3** (C1, step 10, after the PR that fills in `site.auto.tfvars`): the site's record sets
   only. With A records for both names: 3 to add (the apex A, www's A and www's TXT), 1 to change
   (the apex TXT set gains `hosting-site=`), 0 to destroy. With a CNAME for www: 2 to add (the apex
   A, www's CNAME), 1 to change, 0 to destroy.
6. **The WIF split** (D233, step 10a.2, before the site's first deploy): 1 to add, 1 to change, 1
   to destroy. The provider's condition admits `deploy` and `site` (in place). `ryft-deployer`'s
   `workloadIdentityUser` moves from the repository's set to `…/attribute.environment/deploy`,
   create before destroy (`+/-`), so the policy is never without a binding. IAM takes up to seven
   minutes to apply each change, so `SITE_DEPLOY` stays off through it and 10f waits ten minutes.
   Outputs: `github_principal_set` goes; `deployer_principal_set` and `site_principal_set` come.
   The `deploy` set spans the pool, so `tests/iam.tftest.hcl` holds the root to one pool and one
   provider.
7. **The edge** (D242, step 16). The D242 PR's merge (`edge = "off"`): 0 to add, 0 to change, 0 to
   destroy, and two moves (`google_dns_record_set.mapping` → `.host`). `built`: 26 to add (2 APIs,
   the address, 2 NEGs, 2 backend services, the policy, the SSL policy, 2 URL maps, 2 proxies, 2
   forwarding rules, 2 DNS authorizations and their 2 CNAMEs, 2 certificates, the map and 2 entries,
   2 log-based metrics), 2 to change (the sink, the dashboard), 0 to destroy. `live`: 0 to add, 2 to
   change (the two record sets, CNAME → A), 0 to destroy. `locked`: 0 to add, 0 to change, 2 to
   destroy (the mappings). Each way back is the reverse; `built` → `off` destroys the 26 (the APIs
   stay on).
8. **The sixth condition** (D244, step 14a.9, after its PR's merge and deploy): 2 to add
   (`google_logging_metric.notices`, `google_monitoring_alert_policy.notices[0]`), 0 to change, 0 to
   destroy; 1 to add (the metric) with `alert_app_channel` unset. The output `monitoring` lists the
   policy and the metric. The stop line and `serve = false` keep both (no line comes, so it is quiet):
   their counts above are unchanged.
9. **Direct connections, phase 1** (D257, `connect/foundation`, at `edge = "live"`): this root's
   part is 9 to add, 5 to change, 0 to destroy. Added: `connect-garmin-client-secret` and its
   accessor (api), and the inbox topology from `pubsub.auto.tfvars.json`: the topics
   `ryft-inbox-received` and `ryft-dlq-ingest` with their publisher bindings (4), the push
   subscription `ingest-inbox-received`, its dead-letter source binding, the hold `dlq-ingest-hold`.
   Changed in place: the Cloud Armor policy (the six WAF expressions gain the two path conditions, and
   rule 1900 comes; the provider holds rules as a set, so the plan shows six removed and seven added
   inside the one `~`), the sink (`connect-callback`), and through the topology policies 4 and 5 and
   the dashboard (their subscription lists). `monitoring.tf`'s D257 part (sre) adds to this. **Stop
   if** a secret, a subscription other than `ingest-inbox-received`, a record set, a backend service or
   a mapping changes, or anything is destroyed. The secret's value is phase 2's (`gcloud secrets
   versions add connect-garmin-client-secret --data-file=…`), and `deploy/run/api.yaml` pins it only
   then. Cost: Cloud Armor bills the rule, $1 a month (D242 → Cost); the rest is free at RYFT's volume.

```bash
cd infra/gcp
terraform init                        # once per checkout, logged in to ryft-lab (terraform login)
terraform plan -out=gcp.tfplan        # a saved run in HCP Terraform; read every line
terraform apply gcp.tfplan
rm gcp.tfplan
```

## Variables

Set as workspace variables (runbook step 2e), never in a local file: `terraform.tfvars.example` lists
them with placeholders. `github_repository_id`, `github_repository_owner_id` and
`google_site_verification` have no default. `alert_app_channel` (the Google Cloud app channel's
resource name, `projects/<project>/notificationChannels/<number>`) is set in the workspace UI only,
never in a committed file: it is per device. Not sensitive, an opaque id (D237), so Sensitive off in
HCP too. Null (the default, unset) keeps every alert policy out; an empty string is refused (step 11.1).

The apply account `hcp-terraform-apply` needs four Monitoring roles for `alerts.tf` and
`monitoring.tf` beyond the bootstrap's (step 11.2): `roles/monitoring.uptimeCheckConfigEditor`,
`roles/monitoring.dashboardEditor`, `roles/monitoring.alertPolicyEditor` and
`roles/monitoring.notificationChannelViewer` (`alertPolicyEditor` covers the sixth policy, D244).
The log-based metrics need `logging.configWriter`, which it already holds for the bucket and the
sink (and so for `ryft_notices`, D244). The edge (D242, step 16.1) needs three more:
`roles/compute.loadBalancerAdmin`, `roles/compute.securityAdmin` (the Cloud Armor policy and its
attachment) and `roles/certificatemanager.editor`; the plan account's `roles/viewer` covers the reads.

## Checks

CI's `terraform` job: `fmt -check`, `init -backend=false`, `validate`, `tflint` (the bundled
`terraform` plugin only) and `terraform test` (`tests/`, plan-only, every provider mocked). Never a
plain `terraform init` outside the runbook: with the `cloud` block it creates the workspace.
