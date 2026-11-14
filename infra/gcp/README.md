# infra/gcp

RYFT on GCP serverless (D216–D227, `docs/work/2026-11-14-gcp/design.md`): the project `ryftlab-prod`
in `europe-west1`. State and runs live in HCP Terraform, org `ryft-lab`, workspace `ryft-gcp`
(CLI-driven, remote execution, GCP dynamic credentials). The user applies it; nothing else does. The
runbook is `docs/OPERATIONS.md` → **GCP**.

## What this root owns

| File | What |
| --- | --- |
| `project.tf` | the API keep list (`google_project_service`, `disable_on_destroy = false`), Data Access audit logs (`DATA_READ`) for Secret Manager, STS and IAM credentials (every secret read and token mint), the Pub/Sub and Cloud Scheduler service agents |
| `iam.tf` | the accounts `ryft-<api,ingest,scoring,notify,migrate>`, `ryft-push`, `ryft-deployer`, `grafana-reader` (no key, ever); the deployer's rights (**The deployer**, below); Pub/Sub's token creator on `ryft-push` |
| `wif.tf` | the GitHub pool `github` and provider `github-actions` (condition by repository and owner ID, `deploy.yml` on `main`, environment `deploy`, a GitHub-hosted runner) |
| `secrets.tf` | the 11 secret containers, `prevent_destroy`, 30-day delayed destruction, accessors on each secret. **No values**: they never pass through state |
| `pubsub.tf`, `pubsub.auto.tfvars.json` | topics (messages stored in `europe-west1` only; `enforce_in_transit` stays off, so the Scheduler's publish and an operator's `gcloud pubsub` through the global endpoint still work), push and hold subscriptions, publisher bindings per topic, the agents' bindings. The JSON is written by `go run ./cmd/pubsub-topology` (PR A); do not edit it by hand once that exists |
| `scheduler.tf` | the job `ryft-tick` (every 15 minutes) |
| `registry.tf` | Artifact Registry `ryft`: the 5 newest versions of each image kept, every other version deleted after 30 days (tagged or not); scanning off |
| `run.tf` | who invokes the four services (one authoritative `run.invoker` binding each), and the domain mappings `api.` and `ingest.ryftlab.de` (only with `serve = true`) |
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
   bindings on the services and the mappings. 116 to add, 0 to change, 0 to destroy.
2. **Apply 2** (`serve = true`, step 9, after CI's first deploy at step 8): 13 to add (4 invoker
   bindings, 4 + 1 deployer bindings on the services and the job, 2 mappings, 2 CNAMEs), 1 to
   change (the tick resumes), 1 to destroy (the deployer's project-wide `run.developer`).
3. **The stop line** (`public_invokers = false`): no changes after its gcloud commands; without
   them 0 to add, 3 to change (api's and ingest's invoker bindings, the tick), 0 to destroy.
4. **Step 11** (the Grafana variables set): 2 to add (the folder and the rule group).
5. **Apply 3** (C1, step 10): the site's record sets only.

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
