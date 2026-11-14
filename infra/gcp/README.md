# infra/gcp

RYFT on GCP serverless (D216–D227, `docs/work/2026-11-14-gcp/design.md`): the project `ryftlab-prod`
in `europe-west1`. State and runs live in HCP Terraform, org `ryft-lab`, workspace `ryft-gcp`
(CLI-driven, remote execution, GCP dynamic credentials). The user applies it; nothing else does. The
runbook is `docs/OPERATIONS.md` → **GCP**.

## What this root owns

| File | What |
| --- | --- |
| `project.tf` | the API keep list (`google_project_service`, `disable_on_destroy = false`), Data Access audit logs for Secret Manager, the Pub/Sub and Cloud Scheduler service agents |
| `iam.tf` | the accounts `ryft-<api,ingest,scoring,notify,migrate>`, `ryft-push`, `ryft-deployer`, `grafana-reader` (no key, ever); the deployer's rights; Pub/Sub's token creator on `ryft-push` |
| `wif.tf` | the GitHub pool `github` and provider `github-actions` (condition by repository and owner ID, `deploy.yml` on `main`, environment `deploy`) |
| `secrets.tf` | the 11 secret containers, `prevent_destroy`, 30-day delayed destruction, accessors on each secret. **No values**: they never pass through state |
| `pubsub.tf`, `pubsub.auto.tfvars.json` | topics, push and hold subscriptions, publisher bindings per topic, the agents' bindings. The JSON is written by `go run ./cmd/pubsub-topology` (PR A); do not edit it by hand once that exists |
| `scheduler.tf` | the job `ryft-tick` (every 15 minutes) |
| `registry.tf` | Artifact Registry `ryft` with its cleanup policy, scanning off |
| `run.tf` | who invokes the four services, and the domain mappings `api.` and `ingest.ryftlab.de` (only with `serve = true`) |
| `dns.tf` | the zone `ryftlab.de` (`prevent_destroy`, DNSSEC off) with every record of `infra/aws/dns`, CAA with `pki.goog`, Search Console's TXT, and the mappings' CNAMEs |
| `logging.tf` | the bucket `ryft-logs` (europe-west1, 14 days) and the sink into it |
| `alerts.tf` | the Grafana-managed rules on Cloud Monitoring: HTTP5xxRatio, SyncSlow, DeadLetters, PushBacklog, Saturated (only once `grafana_datasource_uid` is set) |
| `checks.tf` | warnings for what fails silently: the `_Default` log sink still on, a mapping not Ready |

## What it does not own (design §9)

- **The Cloud Run services and the job `migrate`**: CI owns them (`deploy/run`, `deploy.yml`). This
  root names them, it never defines them; that is why the invokers and mappings wait for `serve`.
- **The budget** "RYFT 50 EUR": made by hand in the console, with its rules (runbook step 3).
- **Its own credentials**: the HCP Terraform pool and the accounts `hcp-terraform-plan` and
  `hcp-terraform-apply` (the bootstrap, runbook step 2). Terraform cannot create what it signs in with.
- **`grafana-reader`'s JSON key**: made by hand at step 11, kept only in Grafana Cloud. A key resource
  would put the private key into the state.
- **The site project `ryftlab-site`**, its deploy account and its custom domains (by hand, step 10).
  Only the site's DNS records come here, with C1.
- **The `_Default` log sink**: switched off once by hand after apply 1 (`checks.tf` warns until then).
- **The secret values**: `gcloud secrets versions add` (step 5).

## The gates

`gates.auto.tfvars` is committed, so each flip is a reviewed one-line PR. HCP Terraform loads
`*.auto.tfvars` in remote runs. Never set `serve` or `public_invokers` as workspace variables.

| `serve` | `public_invokers` | Invokers | Mappings and their DNS | Tick |
| --- | --- | --- | --- | --- |
| `false` | any | none | none | paused |
| `true` | `true` | `ryft-push` on all four, `allUsers` on api and ingest | `api.`, `ingest.` | runs |
| `true` | `false` (the 20 EUR stop line) | `ryft-push` on all four | `api.`, `ingest.` | paused |

The stop line's gcloud commands (OPERATIONS → GCP → The €20 stop line) do the same by hand at
once; `public_invokers = false` keeps the next apply from undoing them.

## The applies

1. **Apply 1** (`serve = false`, runbook step 4): everything except the invokers and the mappings.
   114 to add, 0 to change, 0 to destroy. The arithmetic is in the runbook.
2. **Apply 2** (`serve = true`, step 9, after CI's first deploy at step 8): 10 to add (4 + 2
   invokers, 2 mappings, 2 CNAMEs), 1 to change (the tick resumes), 0 to destroy.
3. **Step 11** (the Grafana variables set): 2 to add (the folder and the rule group).
4. **Apply 3** (C1, step 10): the site's record sets only.

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
`google_site_verification` have no default. The Grafana token `grafana_auth` is sensitive and
ephemeral, set in the workspace UI only, and needed from step 11.

## Checks

CI's `terraform` job: `fmt -check`, `init -backend=false`, `validate`, `tflint` (the bundled
`terraform` plugin only) and `terraform test` (`tests/`, plan-only, every provider mocked). Never a
plain `terraform init` outside the runbook: with the `cloud` block it creates the workspace.
