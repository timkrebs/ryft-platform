# infra/gcp

RYFT on GCP serverless (D216–D227, `docs/work/2026-11-14-gcp/design.md`): the project `ryftlab-prod`
in `europe-west1`. State and runs live in HCP Terraform, org `ryft-lab`, workspace `ryft-gcp`
(CLI-driven, remote execution, GCP dynamic credentials). The user applies it; nothing else does. The
runbook is `docs/OPERATIONS.md` → **GCP**.

## What this root owns

| File | What |
| --- | --- |
| `project.tf` | the API keep list (`google_project_service`, `disable_on_destroy = false`), Data Access audit logs (`DATA_READ`) for Secret Manager, STS and IAM credentials (every secret read and token mint), the Pub/Sub and Cloud Scheduler service agents |
| `iam.tf` | the accounts `ryft-<api,ingest,scoring,notify,migrate>`, `ryft-push`, `ryft-deployer` (no key, ever; `grafana-reader` went with Grafana Cloud, D236); the deployer's rights (**The deployer**, below); Pub/Sub's token creator on `ryft-push` |
| `wif.tf` | the GitHub pool `github` and provider `github-actions` (condition by repository and owner ID, `deploy.yml` on `main`, environment `deploy` or `site`, a GitHub-hosted runner); one principal set per environment, `ryft-deployer` bound to `deploy`'s alone (D233), `site`'s for `site-deployer` in ryftlab-site (by hand, step 10a) |
| `secrets.tf` | the 11 secret containers in use (ticket B's 10 and `posthog-project-key`, D231), `prevent_destroy`, 30-day delayed destruction, accessors on each secret; `retired`: `otlp-token` and its accessor without `prevent_destroy`, on their way out (OPERATIONS → GCP 11.6). **No values**: they never pass through state |
| `pubsub.tf`, `pubsub.auto.tfvars.json` | topics (messages stored in `europe-west1` only; `enforce_in_transit` stays off, so the Scheduler's publish and an operator's `gcloud pubsub` through the global endpoint still work), push and hold subscriptions, publisher bindings per topic, the agents' bindings. The JSON is written by `go run ./cmd/pubsub-topology` (PR A); do not edit it by hand once that exists |
| `scheduler.tf` | the job `ryft-tick` (every 15 minutes) |
| `registry.tf` | Artifact Registry `ryft`: the 5 newest versions of each image kept, every other version deleted after 30 days (tagged or not); scanning off |
| `run.tf` | who invokes the four services (one authoritative `run.invoker` binding each), and the domain mappings `api.` and `ingest.ryftlab.de` (only with `serve = true`) |
| `dns.tf` | the zone `ryftlab.de` (`prevent_destroy`, DNSSEC off) with every record of `infra/aws/dns`, CAA with `pki.goog`, Search Console's TXT, and the mappings' CNAMEs |
| `site.tf`, `site.auto.tfvars` | the site's records on Firebase Hosting (C1, D233): the apex A set and its `hosting-site=` TXT (joins the apex TXT set), and www's A set and TXT or its CNAME, from the values the Firebase wizard shows. Empty adds nothing |
| `logging.tf` | the bucket `ryft-logs` (europe-west1, 14 days) and the sink into it |
| `monitoring.tf` | Cloud Monitoring, RYFT's only observability (D236): the log-based metrics `ryft_ticks` (by `result`) and `ryft_errors`, the uptime checks (the site's `/`, `/privacy` and AASA always; api's and ingest's `/health/ready` while they're public; every 5 minutes from 3 locations), the dashboard "RYFT · Overview". All free at RYFT's volume, so ungated |
| `alerts.tf` | the five alert policies, one condition each (uptime failing, 5xx over 2 %, tick missing, dead letters held, event backlog over 15 minutes), to both notification channels, the email first, then the app's; only once `alert_app_channel` is set (step 11), the tick's only while the tick runs; the plan fails if that is the email channel (D237) |
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

## The gates

`gates.auto.tfvars` is committed, so each flip is a reviewed one-line PR. HCP Terraform loads
`*.auto.tfvars` in remote runs. Never set `serve` or `public_invokers` as workspace variables.
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

The arithmetic of each count is in the runbook (steps 4, 9, 11 and the stop line).

1. **Apply 1** (`serve = false`, runbook step 4): everything except the invokers, the deployer's
   bindings on the services and the mappings. On a rebuild today 122 to add, 0 to change, 0 to
   destroy: the 118 applied (116 before `posthog-project-key`, D231), less `grafana-reader` and its
   binding, plus D236's 6 (2 log-based metrics, the site's 3 uptime checks, the dashboard).
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
`roles/monitoring.notificationChannelViewer`. The log-based metrics need `logging.configWriter`, which
it already holds for the bucket and the sink.

## Checks

CI's `terraform` job: `fmt -check`, `init -backend=false`, `validate`, `tflint` (the bundled
`terraform` plugin only) and `terraform test` (`tests/`, plan-only, every provider mocked). Never a
plain `terraform init` outside the runbook: with the `cloud` block it creates the workspace.
