# RYFT — operations

How to deploy, where the secrets come from, how to watch the services and how to back up and
restore. RYFT runs on GCP only (**GCP** below); the EKS cluster, Argo CD, Vault, RabbitMQ and
every AWS resource are gone (D3, D246).

## GCP

RYFT runs on GCP serverless in the project `ryftlab-prod`, region `europe-west1`, with no
organisation (D216–D227; the design is `docs/work/2026-11-14-gcp/design.md`). `infra/gcp`
owns the platform. Its README lists what it owns and what it doesn't. Its state and runs live in
HCP Terraform: org `ryft-lab`, workspace `ryft-gcp`, CLI-driven, remote execution. CI's
`deploy.yml` owns the revisions (`deploy/run`). You apply Terraform. Nothing else does.

**The shell.** Every step runs in a shell where `TF_TOKEN_app_terraform_io` is unset, so
Terraform and the API calls use your `ryft-lab` login (`terraform login`). The steps marked
**old org** ran against `tim-krebs-org`, which is deleted now (steps 0 and 12–14). A leading
space keeps a command out of the history. In zsh, once per shell (or in `~/.zshrc`):

```zsh
setopt HIST_IGNORE_SPACE INTERACTIVE_COMMENTS
```

In bash: `HISTCONTROL=ignorespace`. `INTERACTIVE_COMMENTS` matters for pasting. Without it,
zsh treats a pasted `X=1   # note` as the command `#` with `X` in its environment, and `X` is
never set.

**When each step can run.** B is `infra, deploy: GCP and the deploy workflow`. A is the backend
on Pub/Sub and Cloud Run.

| # | Step | Can run |
| --- | --- | --- |
| 0 | Old org: the data key into 1Password | done |
| 1–6 | The project, bootstrap, budget rule, apply 1, secret values, the DNS move | B merged |
| 7 | Supabase, with `make pooler-check` on 6543 | A merged (the check and the CA are in A2) |
| 7a | The config PR | done in A2: the values are in git |
| 8 | First deploy | A merged, steps 5 and 7 done |
| 9 | Apply 2: invokers and mappings, then `make smoke-supabase` | step 8 |
| 10 | The site and the app | waits for C1 |
| 11 | Observability on Cloud Monitoring (D236) | the PR "observability on Cloud Monitoring" merged; step 10 done (the site's uptime checks). 11.6 waits for 11.4, 11.8 (once) for the PR "infra: the app alert channel by id" and 11.6's first apply |
| 12–14 | The retirement: Route 53, AWS and the old workspaces | done by hand on 2026-11-19 (D246); 14.3, the GitHub secrets, after D3 |
| 15 | Testers | step 10 done, C2 in force |
| 16 | The edge (D242): built, live, the deploy PR, locked | step 9 done, the PR "infra: the edge" merged, 16.1's roles; each phase after the one before it (`locked` after 7 clean days) |
| 17 | App Attest (D245, D247): the ring's container, its first version, `warn`, then `on`; the edge's path skip | the server PR merged; 17.2 once C2 names App Attest; `on` after 7 clean days of `warn`; 17.6 before 1000 or 1001 is enforced |

Each step ends with its stop rule. A stop means: do nothing further, find the cause, then start
that step again.

### 0. Old org: the data key into 1Password

**Done** (design §5.3 steps 1–2). The data key was read from the old org's state, once, into
`~/.config/ryft/data-key-api` (its fingerprint noted for step 5) and into the 1Password item
"RYFT data key". The old org and its states are deleted since (D246), so this step can't run
again: the 1Password item and Secret Manager's `data-key` (step 5) are the key's two copies.

### 1. The project, as it is

```bash
gcloud projects describe ryftlab-prod --format='value(projectNumber,parent.id)'   # a number, no parent
gcloud billing projects describe ryftlab-prod --format='value(billingEnabled)'     # True
gcloud projects get-iam-policy ryftlab-prod --format='table(bindings.role,bindings.members)'   # only you
```

2-step verification is on for your Google account. In Search Console, add the domain property
`ryftlab.de` and copy its TXT value (`google-site-verification=…`, not secret) for step 2e.
Don't verify yet. The record answers only after step 6.

**Stop if** a parent (an organisation) appears, or another principal.

### 2. Bootstrap

Run this as the project owner, in bash or zsh, from the repository root on main. Leave
`TF_TOKEN_app_terraform_io` unset. You need to be logged in to `ryft-lab` (`terraform login`
once; `tfc_auth` reads that token) and to GitHub (`gh auth status`).

```bash
P=ryftlab-prod
PN=$(gcloud projects describe "$P" --format='value(projectNumber)')
TFC_ORG_ID=org-aus6gvWumndxZixQ                     # ryft-lab
POOL=hcp-terraform; PROVIDER=app-terraform-io
PLAN_SA=hcp-terraform-plan@$P.iam.gserviceaccount.com
APPLY_SA=hcp-terraform-apply@$P.iam.gserviceaccount.com

# 2a. APIs: the keep list on, everything else off.
KEEP=(artifactregistry.googleapis.com billingbudgets.googleapis.com cloudbilling.googleapis.com
  cloudresourcemanager.googleapis.com cloudscheduler.googleapis.com cloudtrace.googleapis.com dns.googleapis.com
  iam.googleapis.com iamcredentials.googleapis.com logging.googleapis.com monitoring.googleapis.com
  pubsub.googleapis.com run.googleapis.com secretmanager.googleapis.com serviceusage.googleapis.com sts.googleapis.com)
# Once the edge is built (step 16, edge = built or later), two more stay on: without them the loop below would
# try to switch off the load balancer's APIs. The line reads the committed gate (run from the repository root).
grep -qx 'edge = "off"' infra/gcp/gates.auto.tfvars || KEEP+=(certificatemanager.googleapis.com compute.googleapis.com)
gcloud services enable "${KEEP[@]}" --project="$P"
for s in $(gcloud services list --enabled --project="$P" --format='value(config.name)'); do
  case " ${KEEP[*]} " in *" $s "*) continue ;; esac
  gcloud services disable "$s" --project="$P" || echo "still on: $s"
done
gcloud services list --enabled --project="$P" --format='value(config.name)'

# 2b. The workspace in ryft-lab (CLI-driven, remote execution, the root's Terraform version).
#     The token goes through stdin, never argv.
tfc_auth() { jq -r '"Authorization: Bearer " + .credentials["app.terraform.io"].token' ~/.terraform.d/credentials.tfrc.json; }
API=https://app.terraform.io/api/v2; CT='Content-Type: application/vnd.api+json'
tfc_auth | curl -sS -H @- -H "$CT" -X POST "$API/organizations/ryft-lab/workspaces" \
  --data '{"data":{"type":"workspaces","attributes":{"name":"ryft-gcp","execution-mode":"remote","auto-apply":false,"terraform-version":"1.16.7"}}}' \
  | jq -r '.data.id // .errors'
WS_ID=$(tfc_auth | curl -sS -H @- "$API/organizations/ryft-lab/workspaces/ryft-gcp" | jq -r .data.id)
echo "$WS_ID"                                       # ws-…

# 2c. The pool for app.terraform.io, pinned to the org and workspace IDs.
gcloud iam workload-identity-pools create "$POOL" --project="$P" --location=global \
  --display-name="HCP Terraform ryft-lab"
gcloud iam workload-identity-pools providers create-oidc "$PROVIDER" --project="$P" --location=global \
  --workload-identity-pool="$POOL" --issuer-uri=https://app.terraform.io \
  --attribute-mapping='google.subject=assertion.sub,attribute.terraform_organization_id=assertion.terraform_organization_id,attribute.terraform_workspace_id=assertion.terraform_workspace_id,attribute.terraform_run_phase=assertion.terraform_run_phase' \
  --attribute-condition="assertion.terraform_organization_id == '$TFC_ORG_ID' && assertion.terraform_workspace_id == '$WS_ID'"
gcloud iam workload-identity-pools providers describe "$PROVIDER" --project="$P" --location=global \
  --workload-identity-pool="$POOL" --format='value(attributeCondition)'   # both IDs, no name

# 2d. Two SAs: plan reads, apply writes. Each is usable only by its run phase.
gcloud iam service-accounts create hcp-terraform-plan --project="$P" --display-name="HCP Terraform plan (ryft-gcp)"
gcloud iam service-accounts create hcp-terraform-apply --project="$P" --display-name="HCP Terraform apply (ryft-gcp)"
for r in roles/viewer roles/iam.securityReviewer; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$PLAN_SA" --role="$r" --condition=None
done
for r in roles/run.admin roles/pubsub.admin roles/cloudscheduler.admin roles/secretmanager.admin \
         roles/artifactregistry.admin roles/dns.admin roles/logging.configWriter roles/iam.serviceAccountAdmin \
         roles/iam.serviceAccountUser roles/iam.workloadIdentityPoolAdmin roles/resourcemanager.projectIamAdmin \
         roles/serviceusage.serviceUsageAdmin; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$APPLY_SA" --role="$r" --condition=None
done
# Cloud Monitoring's (D236; on ryftlab-prod granted at step 11.2): the uptime checks, the dashboard, the
# alert policies, and reading the two notification channels.
for r in roles/monitoring.uptimeCheckConfigEditor roles/monitoring.dashboardEditor \
         roles/monitoring.alertPolicyEditor roles/monitoring.notificationChannelViewer; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$APPLY_SA" --role="$r" --condition=None
done
# The edge's (D242; on ryftlab-prod granted at step 16.1): the load balancer, Cloud Armor's policy, the certificates.
for r in roles/compute.loadBalancerAdmin roles/compute.securityAdmin roles/certificatemanager.editor; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$APPLY_SA" --role="$r" --condition=None
done
PS=principalSet://iam.googleapis.com/projects/$PN/locations/global/workloadIdentityPools/$POOL
gcloud iam service-accounts add-iam-policy-binding "$PLAN_SA" --project="$P" \
  --role=roles/iam.workloadIdentityUser --member="$PS/attribute.terraform_run_phase/plan"
gcloud iam service-accounts add-iam-policy-binding "$APPLY_SA" --project="$P" \
  --role=roles/iam.workloadIdentityUser --member="$PS/attribute.terraform_run_phase/apply"
gcloud projects get-iam-policy "$P" --flatten='bindings[].members' --filter="bindings.members:$APPLY_SA" \
  --format='value(bindings.role)'                   # the nineteen roles above, never roles/owner

# 2e. The workspace's variables (none sensitive; each prints 201, a 422 means the key exists: change it in the UI).
tfc_var() {  # tfc_var KEY VALUE [env|terraform]
  tfc_auth | curl -sS -o /dev/null -w "%{http_code} $1\n" -H @- -H "$CT" -X POST "$API/workspaces/$WS_ID/vars" \
    --data "$(jq -nc --arg k "$1" --arg v "$2" --arg c "${3:-env}" \
      '{data:{type:"vars",attributes:{key:$k,value:$v,category:$c,sensitive:false}}}')"
}
tfc_var TFC_GCP_PROVIDER_AUTH true
tfc_var TFC_GCP_WORKLOAD_PROVIDER_NAME "projects/$PN/locations/global/workloadIdentityPools/$POOL/providers/$PROVIDER"
tfc_var TFC_GCP_PLAN_SERVICE_ACCOUNT_EMAIL "$PLAN_SA"
tfc_var TFC_GCP_APPLY_SERVICE_ACCOUNT_EMAIL "$APPLY_SA"
tfc_var google_site_verification 'google-site-verification=<value from step 1>' terraform
# The GitHub WIF condition matches the repository and its owner by ID, not by name.
read -r REPO_ID OWNER_ID < <(gh api repos/timkrebs/ryft-platform --jq '"\(.id) \(.owner.id)"')
echo "$REPO_ID $OWNER_ID"                           # two numbers
tfc_var github_repository_id "$REPO_ID" terraform
tfc_var github_repository_owner_id "$OWNER_ID" terraform
# From step 11, not now: alert_app_channel, the Google Cloud app channel's resource name, which a policy
# shows only once you attached the device in the console: the workspace UI (step 11.1).

# 2f. Connect and plan once (speculative: it proves the plan SA's sign-in).
terraform -chdir=infra/gcp init                     # connects to ryft-gcp, which 2b made
terraform -chdir=infra/gcp plan                     # 125 to add (step 4)
```

- **APIs.** Without `--force`, `gcloud services disable` refuses a service that was used in
  the last 30 days, was enabled in the last 3 days, or that an enabled service depends on.
  `--force` would disable the dependants as well, so it is never used here. The project is new,
  so most defaults will be "still on" at first. Run 2a's loop again from day 4. What stays on
  after that is a dependency or in use. Terraform owns the same keep list
  (`google_project_service`, `disable_on_destroy = false`). Once the edge is built (step 16),
  `compute.googleapis.com` and `certificatemanager.googleapis.com` join it (18): the `grep … ||
  KEEP+=` line above adds them while the committed gate isn't `off`. Back at `edge = "off"` they
  leave Terraform's list but stay on.
- **Audience.** The provider has no `--allowed-audiences`. That gives GCP's default audience,
  the provider's full name. HCP Terraform sends it when only `TFC_GCP_WORKLOAD_PROVIDER_NAME`
  is set.
- **The condition** checks both IDs, not names. Workspace names can change. Module test runs
  carry no workspace claims, so they can't match.
- **The apply SA is owner-equivalent in effect**, because `projectIamAdmin` can grant any role.
  That comes with Terraform owning IAM. The boundary is the condition (one org, one workspace,
  apply phase only) plus `auto-apply` off. The stop rule still refuses `roles/owner` itself.
- **The Terraform version.** 2b pins the workspace to 1.16.7, the root's `required_version`.
  A remote run on another version fails at `init`.
- **Never** set `serve` or `public_invokers` as workspace variables. They live in
  `infra/gcp/gates.auto.tfvars`, so each flip is a reviewed PR.

**Stop if** a plan fails authentication; `attributeCondition` lacks either ID; or the apply SA
would need `roles/owner`.

### 3. The budget's 10 % rule

The budget "RYFT 50 EUR" exists (made by hand, design §7). Add the early warning at €5:

```bash
BILLING_ACCOUNT=$(gcloud billing projects describe ryftlab-prod --format='value(billingAccountName)')
BILLING_ACCOUNT=${BILLING_ACCOUNT#billingAccounts/}
BUDGET=$(gcloud billing budgets list --billing-account="$BILLING_ACCOUNT" \
  --filter='displayName="RYFT 50 EUR"' --format='value(name)')
echo "$BUDGET"   # one line: billingAccounts/<id>/budgets/<budget-id>
gcloud billing budgets update "$BUDGET" --add-threshold-rule=percent=0.10
gcloud billing budgets describe "$BUDGET" --format='yaml(amount,thresholdRules,budgetFilter)'
```

`describe` must show `0.1, 0.5, 0.9, 1.0`. If `budgetFilter` names no project, the budget counts
the whole billing account. That is fine while `ryftlab-prod` is its only project with a bill.

**Stop if** `describe` doesn't list `0.1`.

### 4. `infra/gcp` apply 1

`gates.auto.tfvars` on main says `serve = false`. Apply 1 creates everything except the
services' invoker bindings, the deployer's bindings on the services and the job, and the domain
mappings. The deployer's `run.developer` is project-wide until apply 2, because CI creates the
services at step 8.

```bash
terraform -chdir=infra/gcp plan -out=apply-1.tfplan
terraform -chdir=infra/gcp apply apply-1.tfplan
rm infra/gcp/apply-1.tfplan
```

```text
# Apply 1: 125 to add, 0 to change, 0 to destroy (on a rebuild today; the first apply 1 was 118, with
# grafana-reader and its monitoring.viewer and without D236's 6, D244's 1 and D247's 2).
#   16 APIs + 3 audit configs (Secret Manager, STS, IAM) + 2 service agents                  = 21
#    7 accounts + 1 token creator + 1 run.developer + 5 actAs                                = 14  (35)
#    1 GitHub pool + 1 provider + 1 workloadIdentityUser                                     =  3  (38)
#   13 secrets + 13 accessor bindings (app-attest-token-key's among them, D247)              = 26  (64)
#    9 topics + 9 publisher bindings + 10 push subs + 6 dead-letter source bindings + 2 hold = 36 (100)
#    1 Scheduler job + 1 Artifact Registry + 1 registry writer                               =  3 (103)
#    1 zone + 12 record sets (CAA, MX, apex TXT, DMARC, 6 CNAME, 2 SRV)                      = 13 (116)
#    1 log bucket + 1 sink                                                                   =  2 (118)
#    2 log-based metrics + 3 uptime checks (the site's) + 1 dashboard (D236)                 =  6 (124)
#    1 log-based metric (ryft_notices, D244)                                                 =  1 (125)
```

The site's uptime checks fail until step 10 serves it. Nothing pages: no alert policy exists before
step 11.1's variable.

Every line is a `+`. Expect one warning: the check `default_log_sink_disabled`, because the
`_Default` sink is still on. No change to the bootstrap: the pool `hcp-terraform`, the two
`hcp-terraform-*` SAs and their roles, your owner binding, the budget. Terraform adds IAM
members and audit configs per service, and never writes the project's whole policy, so none
of them appears. In the plan, check three things by eye:
- the provider `github-actions`' `attribute_condition` ends with
  `assertion.runner_environment == 'github-hosted'`;
- every topic's `message_storage_policy` names `europe-west1` only;
- the registry `ryft` has three `cleanup_policies`: `keep-5-most-recent`,
  `delete-untagged-after-30-days` and `delete-any-after-30-days`.

Then, once:

```bash
gcloud logging sinks update _Default --disabled --project=ryftlab-prod
gcloud logging sinks describe _Default --project=ryftlab-prod --format='value(disabled)'   # True
terraform -chdir=infra/gcp plan                     # No changes, and no warning
terraform -chdir=infra/gcp output name_servers      # the four ns-cloud-* servers (step 6)
```

Pub/Sub's service agent can take a minute to exist. If the apply fails on
`service-<number>@gcp-sa-pubsub.iam.gserviceaccount.com`, wait, then plan and apply again. The
new plan holds only what is left.

**Stop if** the plan has any change or destroy, or the add count isn't 125.

### 5. Secret values

The 13 containers exist since step 4. Each value goes in through stdin or a file, never on argv
and never on screen. Each `add` prints `Created version [N] of the secret [<id>]`. Every `N` must
be `1`, because `deploy/run` pins version 1. If one isn't, a PR moves that secret's pin in
`deploy/run` before step 8. For a `db-password-*` secret, that means `migrate.yaml` and the
service's YAML in one commit.

```bash
# The four role passwords: random, nobody needs to know them (step 8's migrate sets them in Postgres).
openssl rand -base64 32 | tr -d '\n' | gcloud secrets versions add db-password-api --project=ryftlab-prod --data-file=-
openssl rand -base64 32 | tr -d '\n' | gcloud secrets versions add db-password-ingest --project=ryftlab-prod --data-file=-
openssl rand -base64 32 | tr -d '\n' | gcloud secrets versions add db-password-scoring --project=ryftlab-prod --data-file=-
openssl rand -base64 32 | tr -d '\n' | gcloud secrets versions add db-password-notify --project=ryftlab-prod --data-file=-
# A hidden prompt for a pasted value: nothing on screen, in the history or on argv. (pbpaste stored
# whatever was on the clipboard, the command text itself included.) zsh's read syntax:
put() { read -rs "?Value for $1: " V; echo; printf '%s' "$V" | gcloud secrets versions add "$1" --project=ryftlab-prod --data-file=-; unset V; }
# The owner: Supabase's database password from 1Password, pasted at the prompt:
put db-password-owner
# Supabase -> Project Settings -> API Keys -> the secret key:
put supabase-secret-key
# ADMIN_API_KEY: a new value from 1Password's generator (item "RYFT ADMIN_API_KEY"):
put admin-api-key
# The two .p8 keys, saved from 1Password (their names carry the key IDs that step 7a needs):
gcloud secrets versions add apple-key-p8 --project=ryftlab-prod --data-file="$HOME/Downloads/AuthKey_<APPLE_KEY_ID>.p8"
gcloud secrets versions add apns-key-p8 --project=ryftlab-prod --data-file="$HOME/Downloads/AuthKey_<APNS_KEY_ID>.p8"
rm "$HOME/Downloads/AuthKey_<APPLE_KEY_ID>.p8" "$HOME/Downloads/AuthKey_<APNS_KEY_ID>.p8"
# PostHog's project API key (phc_…, never a personal phx_ key), PostHog -> Project settings. Needed
# before step 8 once deploy/run/api.yaml pins it (D231); while it doesn't, the api serves analytics null:
put posthog-project-key
pbcopy < /dev/null                                   # clear the clipboard you pasted from
```

A value stored wrongly (the version already exists) is fixed by a new version and a pin
move, not by `destroy`: add the right value with `put <secret>`, note the new number, and move
the pin in `deploy/run` (step 5's first paragraph). Then disable the wrong version:
`gcloud secrets versions disable <N> --secret=<secret> --project=ryftlab-prod`.

- `db-password-owner`: if 1Password lacks the value, reset it in Supabase → Database →
  Settings. Nothing else uses it.
- `otlp-token` gets no value any more: no revision pins it since D236 (it held one on
  ryftlab-prod, which every revision before D236 reads; step 11.6 removes it).
- `app-attest-token-key` (D247) gets no value here: its first version is step 17.2, only once C2's
  privacy text names App Attest. Until then no revision pins it.

The data key, design §5.3 steps 3–5. Use the version `add` prints. If it isn't `1`, the pin
in `deploy/run/api.yaml` moves too.

```bash
gcloud secrets versions add data-key --project=ryftlab-prod --data-file="$HOME/.config/ryft/data-key-api"
gcloud secrets versions access 1 --secret=data-key --project=ryftlab-prod | shasum -a 256 | cut -c1-16   # step 0's fingerprint
gcloud secrets versions access 1 --secret=data-key --project=ryftlab-prod | base64 -d | wc -c             # 32
rm "$HOME/.config/ryft/data-key-api"
```

If step 0's file is gone, paste the 1Password item at `put data-key` instead, then clear the
clipboard. The two checks stay the same. `rm` overwrites nothing on
APFS: FileVault protects the freed blocks (D211).

```bash
for s in $(gcloud secrets list --project=ryftlab-prod --format='value(name.basename())'); do
  echo "$s $(gcloud secrets versions list "$s" --project=ryftlab-prod --format='value(name.basename(),state)' | tr '\n' ' ')"
done                                                # 13 lines, each one version: 1 ENABLED (on a rebuild since D236, otlp-token's line has none; app-attest-token-key's has none until step 17.2)
```

**Stop if** the data key's fingerprint differs or its length isn't 32. Then use D211's fallback:
a new key, with the one-off statement before the api starts.

### 6. The DNS move

**Done:** the domain moved from Route 53 to Cloud DNS's zone, and Route 53 is deleted since
(D246). What follows is the move for a zone `infra/gcp` creates anew (new name servers, so
another change at GoDaddy).

```bash
dig +short NS ryftlab.de @1.1.1.1 | tee ~/ryft-ns-before.txt      # the servers in use: keep the file for a rollback
dig +norecurse DS ryftlab.de @a.nic.de                               # no DS record
terraform -chdir=infra/gcp output -raw godaddy_instructions
```

Before the switch, check every record against the new zone's own server (**Mail for ryftlab.de →
Check every record**): MX, the apex TXT (with the `google-site-verification=` string), DMARC, the
DKIM and other mail CNAMEs, both SRVs, CAA.

Then switch: GoDaddy → My Products → ryftlab.de → DNS → Nameservers → Change → "I'll use my own
nameservers". Enter the four `ns-cloud-*` from the output, without the trailing dot.

```bash
dig +norecurse NS ryftlab.de @a.nic.de     # again until the authority section lists the four ns-cloud-* servers
dig +short MX ryftlab.de @1.1.1.1          # the Microsoft 365 MX, unchanged
```

Send a mail from an outside address to your `ryftlab.de` address, and reply to it. **The old zone
stays for 48 h.** Resolvers that cached its NS records still ask it.

Then verify the domain: `dig +short TXT ryftlab.de @1.1.1.1` must show the
`google-site-verification=` string, then Search Console → ryftlab.de → Verify. Then make
`hcp-terraform-apply@ryftlab-prod.iam.gserviceaccount.com` a **verified owner**: Search Console →
Settings → Users and permissions → Manage property owners → Add an owner. Verification belongs
to the user who did it. The apply SA creates the domain mappings in step 9, and it can't as a
plain user.

**Stop if** a record differs from `infra/gcp/dns.tf`, or a DS exists. If mail fails after the
switch, set the old name servers back at GoDaddy at once (`~/ryft-ns-before.txt`, while their zone
still exists).

### 7. Supabase

In the dashboard:
- Connect → Transaction pooler: the host (`aws-<n>-eu-central-1.pooler.supabase.com`) and port
  6543 for the services. Session pooler: 5432 for migrate. The host and the project ref go into
  step 7a.
- Database → Settings → Connection pooling: the pool size. The default 15 is enough for at most
  26 clients (design §4).
- Project Settings → Data API: still off.
- Authentication → URL Configuration: the Site URL `https://ryftlab.de`, and redirect URLs only
  on `ryftlab.de`.
- Database → Settings → SSL Configuration: the CA is the Supabase Root 2021 CA, which PR A2
  committed as `backend/certs/supabase-ca.crt` (sha256 `700723581420dd1a…2c3b7`). The commands
  below use that file; a downloaded `prod-ca-2021.crt` is the same certificate.

Run these from the repository root on an up-to-date main (`git pull`): the ref and the pooler
host come from `deploy/run/config.env` (A2 put them in git), so nothing is typed or pasted. One
`verify-full` login as the owner on each port. It proves the CA and the host name on both ports,
and step 5's owner value. `-w` never prompts for a password, and `connect_timeout=10` ends a
hung connect:

```bash
REF=$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env)
POOLER=$(sed -n 's/^SUPABASE_POOLER_HOST=//p' deploy/run/config.env)
echo "$REF $POOLER"                                  # ydgbemufggwnegbnjhkm aws-1-eu-central-1.pooler.supabase.com
PGPASSWORD=$(gcloud secrets versions access 1 --secret=db-password-owner --project=ryftlab-prod) && export PGPASSWORD
for port in 5432 6543; do
  psql -w "host=$POOLER port=$port dbname=postgres user=postgres.$REF sslmode=verify-full sslrootcert=backend/certs/supabase-ca.crt connect_timeout=10" \
    -tAc 'select current_user' || break               # postgres, twice; after a failure, no second try
done
```

Then, only if both printed `postgres`, pgx's unnamed statements through Supavisor's transaction
mode (design §4, D221). The services can't run against Supabase before step 8's migrate (the
schema lacks 0034 and 0035), so this is `TestPooler`'s read-only queries instead of a smoke: one
connection as the owner on 6543 with `default_query_exec_mode=exec`, everything in a
`READ ONLY` transaction that is rolled back, one attempt, 30 s at most. It needs Go
(`backend/go.mod`'s version):

```bash
make pooler-check                                    # "--- PASS: TestPooler", then "ok"
unset PGPASSWORD
```

It passes when it prints `--- PASS: TestPooler` and then `ok`. On the way it proves DateStyle,
IntervalStyle and bytea output, `SET LOCAL`, `set_config(…, true)` and the advisory lock in one
transaction, the store's parameter types there and back, a batch, and that no named statement is
left. It also proves the owner holds CREATEROLE and ADMIN OPTION on each `ryft_*` role, which
migration 0035 needs at step 8. Its log names `extra_float_digits 0 (configuration file)`. That is
Supabase's server config, and the owner has no role setting, so `TestPooler` reports it and checks
the float8 at the 15 digits Postgres prints then, not exactly (D228). The services' roles get 1
from migration 0035 at step 8, and step 9's role login checks it.

The login as `ryft_api.<ref>` on 6543 is at the start of step 9: Postgres learns that role's
password from step 8's migrate. A failed login trips Supavisor's breaker for the whole role
(`SUPABASE.md`). `make smoke-supabase` runs at step 9, against the deployed hosts.

**Stop if** a login fails, `ECIRCUITBREAKER` appears (wait it out; don't retry in a loop), or
`pooler-check` fails (a `--- FAIL` line, or no `ok`). `extra_float_digits 0` in its log is not a
stop. A failure in `TestPooler` after a good login (an error naming a prepared statement or a
parameter type) means `exec` mode doesn't work through Supavisor: the fallback is
`default_query_exec_mode=simple_protocol` (design §4), a PR in `deploy/run` and `internal/db`,
before step 8. An error that the owner `lacks CREATEROLE or ADMIN OPTION` means step 8's
migrate would fail with 42501, on 0035 and on any role password that changed. Dropping 0035
wouldn't help. First get the grant: only Supabase can give it (`supabase_admin` runs `GRANT
<role> TO postgres WITH ADMIN OPTION` for each role named; ask Supabase support). Then run step 7
again, and only then step 8.

### 7a. The config PR (before step 8)

**Done in PR A2 (2026-11-15):** the values are in git, in `deploy/run/config.env`, as you
confirmed them. None of them is secret, and no `FILL_ME_IN` is left:

| Key | Value | Source |
|---|---|---|
| `GCP_PROJECT_NUMBER` | `49741851924` | `terraform -chdir=infra/gcp output -raw project_number` |
| `PUSH_SA_ID` | `100999984647275261823` | `terraform -chdir=infra/gcp output -raw push_sa_unique_id` |
| `SUPABASE_PROJECT_REF` | `ydgbemufggwnegbnjhkm` | step 7 |
| `SUPABASE_POOLER_HOST` | `aws-1-eu-central-1.pooler.supabase.com` | step 7 |
| `APPLE_KEY_ID`, `APNS_KEY_ID` | `3GY48KHYHL` (one key with both services) | Apple Developer → Keys |

To check them against the sources, or after a change (a new key, another pooler host), edit
the file in a PR, run `make cloudrun-check`, and merge it before the next deploy. `deploy.yml`
refuses to run while a placeholder is left.

**Stop if** an output differs from the table, or `make cloudrun-check` fails.

### 8. First deploy

Set `GCP_DEPLOY=on`, then run `deploy.yml` by hand with the `smoke` input off. The commands are
in **Deploy, roll back, rotate** below. **Stop if** `migrate`'s log lacks a `migration applied`
line for each new file (`0034_outbox_available_at.sql`, `0035_float_digits.sql`), or doesn't end
with `schema current`; or a revision isn't ready. This run
proves the revisions start. It doesn't prove they reach the database (the startup probe is
`/health/live`): step 9's role login and smoke do.

### 9. Apply 2: invokers and mappings

First, the role login moved here from step 7. Do it once. Step 8's migrate set the password and,
with 0035, the role's `extra_float_digits` (D228). From the repository root, as in step 7:

```bash
REF=$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env)
POOLER=$(sed -n 's/^SUPABASE_POOLER_HOST=//p' deploy/run/config.env)
PGPASSWORD=$(gcloud secrets versions access 1 --secret=db-password-api --project=ryftlab-prod) && export PGPASSWORD
psql -w "host=$POOLER port=6543 dbname=postgres user=ryft_api.$REF sslmode=verify-full sslrootcert=backend/certs/supabase-ca.crt connect_timeout=10" \
  -tAc "select current_user, current_setting('extra_float_digits')::int >= 1, '0.30000000000000004'::float8"
                                                    # ryft_api|t|0.30000000000000004
unset PGPASSWORD
```

It passes on exactly `ryft_api|t|0.30000000000000004`. psql reads results as text, as the services
do in `exec` mode, so the third field is the float8 a service reads back. Every `ryft_api` server
connection opens after step 8's migrate (`deploy.yml` runs it before the services), so none still
has the old value. **Stop if** the line is anything else: `f` or `0.3` means the role's setting
doesn't hold through Supavisor's transaction mode. Then the fallback is
`default_query_exec_mode=cache_describe` (binary results, D228): a PR in `deploy/run` and
`internal/db`, deployed as in step 8, and then step 9 from the start.

Then the hold, before the `serve = true` PR is merged. That merge is a push to main, so it starts
`deploy.yml`, and its smoke would fail while nothing is public:

```bash
R=timkrebs/ryft-platform
gh variable set DEPLOY_HOLD_SERVICES --body '#<issue>' -R "$R"   # the reason: an issue number (The hold, below)
gh variable list -R "$R"                                          # DEPLOY_HOLD_SERVICES #<issue>
```

Then open a PR that sets `serve = true` in `infra/gcp/gates.auto.tfvars`. It is one line, and
CI's `terraform` job must be green. Merge it: its `deploy.yml` run ends green as held, with
nothing built or deployed. Then:

```bash
git pull
terraform -chdir=infra/gcp plan -out=apply-2.tfplan
terraform -chdir=infra/gcp apply apply-2.tfplan
rm infra/gcp/apply-2.tfplan
```

```text
# Apply 2: 15 to add, 1 to change, 1 to destroy (13 to add before D236).
#   4 run.invoker bindings, one per service (api, ingest, scoring, notify)                   =  4
#   + 4 run.developer for ryft-deployer on the services + 1 on the job migrate               =  5  (9)
#   + 2 domain mappings (api., ingest.ryftlab.de) + 2 CNAME record sets (from the mappings)  =  4 (13)
#   + 2 uptime checks (ryft-api-ready, ryft-ingest-ready; D236)                              =  2 (15)  to add
#   ryft-tick: paused true -> false                                                          =  1       to change
#   ryft-deployer's project-wide run.developer (google_project_iam_member...[0])             =  1       to destroy
```

The bindings' `members`: `allUsers` and `ryft-push` on api and ingest, `ryft-push` alone on
scoring and notify. They are authoritative, so an invoker added by hand is gone after any later
apply. The destroy is the deployer's project-wide role: from now on it holds `run.developer` on
the four services and the job only, and can update them but create nothing (`infra/gcp/README.md`
→ The deployer). No change to the zone's other records, the secrets, the topics and
subscriptions, the accounts, the audit configs or the WIF pool. After the apply, the check
`domain_mappings_ready` warns until the certificates are issued (15 minutes to 24 hours). If a
CNAME fails its precondition because the mapping has no record yet, plan and apply again after a
minute.

```bash
for h in api ingest; do
  gcloud beta run domain-mappings describe --domain="$h.ryftlab.de" --region=europe-west1 \
    --project=ryftlab-prod --format='value(status.conditions)'   # Ready True once the certificate is issued
  dig +short CNAME "$h.ryftlab.de" @1.1.1.1                       # ghs.googlehosted.com.
done
```

Once both mappings are Ready, release the hold and deploy main with the smoke. This run also
proves the deployer's narrowed rights: `jobs replace` and `execute` on `migrate` and `services
replace` on the four, each through its binding on the resource.

```bash
gh variable delete DEPLOY_HOLD_SERVICES -R "$R"
gh workflow run deploy.yml --ref main -f smoke=true -R "$R"
gh run list --workflow=deploy.yml -R "$R" --limit=3              # the new run on top, workflow_dispatch
gh run watch <run id> -R "$R" --exit-status
gh run view <run id> -R "$R" --log-failed                        # only after a red run
```

It worked when the run is green, `make smoke-prod` passed in it, and step 8's services loop shows
the revisions its summary lists.

A permission error on `run.*` in that run (`PERMISSION_DENIED` naming a `run.jobs.…`,
`run.executions.…`, `run.services.…` or `run.operations.…` permission) is a stop. The fallback
is this, and nothing else:
1. Set the hold again (as above).
2. A PR that restores the deployer's project-wide binding unconditionally: in
   `infra/gcp/iam.tf`, `count = 1` for `google_project_iam_member.deployer_run_developer`; in
   `infra/gcp/tests/gates.tftest.hcl`, its pin in `apply_2_serve_true_public_true` to `== 1`.
   Merge it once CI is green.
3. Its apply: exactly 1 to add (`google_project_iam_member.deployer_run_developer[0]`), 0 to
   change, 0 to destroy.
4. Delete the hold and dispatch `deploy.yml` with the smoke again, as above. Never a re-run of
   the red run. Write the missing permission into the hold's issue: narrowing the deployer again
   is a later PR.

Then the three tests (design §3, §5.1, §4):

```bash
# 1. 33 MiB through ingest.ryftlab.de must get OUR JSON 413, which proves HTTP/2 end to end.
#    Google's front end would answer an HTML 413. The smoke user's token never reaches argv.
read -r SMOKE_EMAIL; read -rs SMOKE_PASSWORD        # the smoke user (1Password); the password not echoed
SUPABASE_URL=https://$REF.supabase.co; SUPABASE_ANON_KEY='<the publishable key>'
TOKEN=$(jq -nc --arg e "$SMOKE_EMAIL" --arg p "$SMOKE_PASSWORD" '{email: $e, password: $p}' \
  | curl -fsS -X POST "$SUPABASE_URL/auth/v1/token?grant_type=password" -H "apikey: $SUPABASE_ANON_KEY" \
      -H 'Content-Type: application/json' --data @- | jq -r .access_token)
unset SMOKE_PASSWORD
f=$(mktemp); { printf '{"pad":"'; head -c $((33 << 20)) /dev/zero | tr '\0' a; printf '"}'; } > "$f"
printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS -H @- -H 'Content-Type: application/json' \
  --data-binary @"$f" -w '\n%{http_code} %{content_type}\n' https://ingest.ryftlab.de/v1/sync
rm -f "$f"; unset TOKEN                             # "request body over 33554432 bytes", then 413 application/json

# 2. Three forged X-Forwarded-For entries; once a minute, three times (the line below is
#    sampled once a minute per instance, and a tick may have taken the minute).
for i in 1 2 3; do
  for h in api ingest; do
    curl -sS -o /dev/null -w "%{http_code} $h\n" \
      -H 'X-Forwarded-For: 203.0.113.7, 203.0.113.8, 203.0.113.9' "https://$h.ryftlab.de/health/ready"
  done
  [ "$i" = 3 ] || sleep 61
done
```

Then which entry `httpx.ClientKeys` took (at `XFF_TRUSTED_HOPS`, 1 here; D240). api and ingest log
a `client address` line for a request with `X-Forwarded-For`, at most once a minute per instance.
It holds no address (D222: no IP address in any log line), only `picked` (`xff` or `fallback`, the
shared key when no entry would do), `xff_index` (the picked entry's position from the left, from 0)
and `xff_entries`. That `ClientKeys` takes the right entry in each
shape Google could send (the address appended, a hop of its own after it, nothing appended) and
that `xff_index` points at that entry is proven by `TestStepNineUnderGoogleFrontEnd` in
`backend/internal/httpx`. This test checks which shape Google really sends.

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND jsonPayload.message="client address" AND jsonPayload.xff_entries>=3' \
  --project=ryftlab-prod --freshness=10m --limit=10 \
  --format='value(resource.labels.service_name,jsonPayload.picked,jsonPayload.xff_index,jsonPayload.xff_entries)'
```

Your requests are the lines with `xff_entries` 3 or more (other callers send none of their
own). On both services:
- **Pass:** `picked` is `xff` and `xff_index` is `3`: the entry Google appended after your three
  is the key. `xff_entries` is 4, or more if Google adds a hop from its known front-end ranges,
  which `ClientKeys` skips.
- **Stop for testers** (not for the deploy): `xff_index` is 4 or more. Google appends a hop that
  `ClientKeys` doesn't skip, so every client would share one bucket. A PR adds the range from
  Google's published front-end ranges to `googleFrontEnds`, with a test case.
- **Stop for testers:** `xff_entries` is 3 and `xff_index` is 2. Google appended nothing, so
  the forged entry wins: the per-address limits can be dodged. Report it before the beta opens.
- **Stop for testers:** `picked` is `fallback`. Fewer entries arrived than `XFF_TRUSTED_HOPS`
  expects, so every client shares the fallback bucket: the value in `deploy/run/config.env` is wrong.
- No line with 3 or more entries on a host: send that host's loop again.

3. `make smoke-supabase` against the two hosts, as the smoke user (step 9's `REF` is still set):
   a sign-in, `/v1/me`, a sync of synthetic days into that user, and the score read back,
   through the services on the transaction pooler. Leave `SUPABASE_SECRET_KEY` unset (that would
   add the throwaway-account tests).

```bash
read -r SMOKE_EMAIL; read -rs SMOKE_PASSWORD; echo; export SMOKE_EMAIL SMOKE_PASSWORD
SUPABASE_URL=https://$REF.supabase.co SUPABASE_ANON_KEY='<the publishable key>' \
  API_URL=https://api.ryftlab.de INGEST_URL=https://ingest.ryftlab.de make smoke-supabase
unset SMOKE_PASSWORD
```

Undo apply 2 with a PR that sets `serve = false`, the hold set before its merge (as above). Its
apply: 1 to add (the deployer's project-wide `run.developer` again), 1 to change (the tick
paused), 15 to destroy (step 9's 15; 16 with alerting on, the tick policy too), nothing stateful.

**Stop if** the plan isn't exactly the 15 adds, the tick and the one destroy above; a
certificate is still pending after 24 hours (then design §1's fallback, which you decide); the
dispatched run fails (a permission error on `run.*`: the fallback above, nothing else; any other
failure: **A normal deploy** below); or the 413 isn't ours (then a contract change, a 31 MiB cap:
design §3, api-contract-owner).

### 10. The site and the app *(C1, D233)*

Run from the repository root on an up-to-date main, in bash or zsh, as you, logged in to gcloud
and to Firebase (`npx --yes firebase-tools@$(cat web/.firebase-tools-version) login` once). In zsh,
run `setopt interactivecomments` first. Stock zsh doesn't treat `#` as a comment at the prompt, so a
line like `S=ryftlab-site   # taken? …` fails ("no matches found") and leaves `S` unset. No
billing account anywhere. Until 10f, `SITE_DEPLOY` is off, and every run of `deploy.yml` shows
"Site off" and stays green.

**10a. The environment `site`, the WIF split, then the site project** (C1 is merged; 10a.1
before the split's merge, `infra/wif-split`, D233; 10a.2 and 10a.3 after it). `deploy.yml`'s `site` job runs in its own GitHub environment
`site`. The pool's condition admits `deploy` and `site`, and each is its own principal set:
`ryft-deployer` binds `…/attribute.environment/deploy` alone, `site-deployer` `…/attribute.environment/site`
alone. So nothing the site job runs (firebase-tools and the dependencies npx resolves) can become
the services' deployer. In this order; the services keep deploying throughout.

1. **The environment `site`**, main only, with no secrets and no admin bypass:

   ```bash
   gh api -X PUT repos/timkrebs/ryft-platform/environments/site --input - <<'JSON'
   {"can_admins_bypass": false, "deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}
   JSON
   gh api -X POST repos/timkrebs/ryft-platform/environments/site/deployment-branch-policies -f name=main -f type=branch
   gh api repos/timkrebs/ryft-platform/environments/site --jq '{p: .deployment_branch_policy, bypass: .can_admins_bypass}'
   #   p: {"custom_branch_policies":true,"protected_branches":false}; bypass may print null (see below)
   gh api repos/timkrebs/ryft-platform/environments/site/deployment-branch-policies --jq '.branch_policies[] | [.name,.type]'   # ["main","branch"], nothing else
   gh api repos/timkrebs/ryft-platform/environments/site/secrets --jq .total_count           # 0
   gh api repos/timkrebs/ryft-platform/environments/site/variables --jq .total_count         # 0
   ```

   **Then confirm "admin bypass off" in the browser**, as for `deploy` (**Deploy, roll back,
   rotate → The first deploy**): Settings → Environments → `site` → "Allow administrators to
   bypass configured protection rules" must be **unchecked** (untick it and Save if not).
   `can_admins_bypass` is not in GitHub's REST schema, so the checkbox is the proof. Create the
   environment **before the merge**: a job naming an environment that doesn't exist creates it
   without a branch policy.

   **Stop if** the branch policy isn't `main` alone, the checkbox is still ticked, or the
   environment holds a secret or a variable (an environment variable overrides the repository's,
   `DEPLOY_HOLD_SERVICES` for one).

2. **The split's apply.** First check that `SITE_DEPLOY` is off, that no `deploy.yml` run is going,
   and that ci.yml's `ci-ok` is green on main's tip. Otherwise the dispatch below stops at "wait for
   ci-ok" and proves nothing about the sign-in:

   ```bash
   gh run list --workflow ci.yml --branch main --event push -L 1 -R timkrebs/ryft-platform --json headSha,conclusion --jq '.[] | [.headSha[0:7], .conclusion] | @tsv'   # main's tip, success
   gh variable get SITE_DEPLOY -R timkrebs/ryft-platform                          # off (or "variable SITE_DEPLOY was not found")
   gh run list --workflow deploy.yml -R timkrebs/ryft-platform -L 3 --json status --jq '.[].status'   # completed, each
   terraform -chdir=infra/gcp plan -out=wif-split.tfplan
   # Plan: 1 to add, 1 to change, 1 to destroy:
   #   ~ google_iam_workload_identity_pool_provider.github    attribute_condition: environment == 'deploy'
   #       -> environment in ['deploy', 'site'], and the description
   #   +/- google_service_account_iam_member.deployer_workload_identity    (create replacement, then destroy)
   #       member: …/attribute.repository_id/<id> -> …/attribute.environment/deploy
   #   Outputs: - github_principal_set, + deployer_principal_set, + site_principal_set
   terraform -chdir=infra/gcp apply wif-split.tfplan
   rm infra/gcp/wif-split.tfplan
   gcloud iam service-accounts get-iam-policy ryft-deployer@ryftlab-prod.iam.gserviceaccount.com --project=ryftlab-prod \
     --format=json | jq -r '.bindings[] | .role + " " + (.members | join(" "))'
   #   roles/iam.workloadIdentityUser principalSet://iam.googleapis.com/projects/<n>/locations/global/workloadIdentityPools/github/attribute.environment/deploy
   ```

   Then prove the services still sign in. Wait two minutes for IAM, then run deploy.yml by hand.
   It redeploys main's services with the smoke and says "Site off":

   ```bash
   gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform
   gh run list --workflow deploy.yml -L 1 -R timkrebs/ryft-platform    # the run's ID
   gh run watch <run id> --exit-status -R timkrebs/ryft-platform       # green: deploy, smoke, "Site off"
   ```

   **Stop if:**
   - `SITE_DEPLOY` is on: set it off first. Nothing orders the condition's update before the old
     member's removal, and IAM takes up to seven minutes to apply either, so in that window a
     `site` token could still match `ryft-deployer`'s repository-wide member.
   - A run is going, or main's newest `ci-ok` isn't `success`.
   - The plan holds anything else, or shows the binding as `-/+` (destroy, then create) instead of
     `+/-`.
   - `ryft-deployer`'s policy names anything but the `deploy` set.
   - The run's `deploy` job fails at "Sign in to Google Cloud". Dispatch once more after ten minutes
     (IAM propagates in up to seven). If it fails again, restore the old member by hand, and report:
     ```bash
     gcloud iam service-accounts add-iam-policy-binding ryft-deployer@ryftlab-prod.iam.gserviceaccount.com --project=ryftlab-prod \
       --role=roles/iam.workloadIdentityUser \
       --member="principalSet://iam.googleapis.com/projects/$(terraform -chdir=infra/gcp output -raw project_number)/locations/global/workloadIdentityPools/github/attribute.repository_id/$(gh api repos/timkrebs/ryft-platform --jq .id)"
     ```
     That member isn't in Terraform: the next apply leaves it. It also undoes the split, because the
     condition now admits `site` and that member matches every admitted job. So stop here:
     `SITE_DEPLOY` stays off, and 10a.3 and 10f wait until the cause is fixed and the member is
     gone again (the same command with `remove-iam-policy-binding`; the policy check above prints
     the `deploy` set alone).

3. **The site project**, its deploy account bound to the `site` set and nothing else.

   `ft` runs the firebase-tools of `web/.firebase-tools-version`, which main has only since C1.
   Without it, `firebase-tools@` would be whatever is newest.

```bash
# A function, not a variable: zsh doesn't word-split an unquoted $FT, so "$FT projects:…" fails there.
FTV=$(cat web/.firebase-tools-version); ft() { npx --yes "firebase-tools@$FTV" "$@"; }
S=ryftlab-site                                       # taken? stop: deploy.yml's SITE_PROJECT and SITE_DEPLOYER need a PR first
gcloud projects create "$S" --name="RYFT site"       # no --organization; never link a billing account
gcloud billing projects describe "$S" --format='value(billingEnabled)'   # False
gcloud services enable firebase.googleapis.com firebasehosting.googleapis.com iam.googleapis.com \
  iamcredentials.googleapis.com --project="$S"
ft projects:addfirebase "$S"                         # Spark, because there is no billing account
ft hosting:sites:list --project "$S"                 # one site, ryftlab-site; a 403 right after addfirebase is IAM
                                                     # propagation: wait a few minutes, then run it again

# addfirebase also creates the Admin SDK account, firebase-adminsdk-fbsvc, with roles/firebase.sdkAdminServiceAgent
# and a project-level roles/iam.serviceAccountTokenCreator. The project policy check below lists it too, and that is
# expected. Nothing of ours uses it: check it has no keys, then disable it.
ADMIN_SDK_SA=firebase-adminsdk-fbsvc@$S.iam.gserviceaccount.com
gcloud iam service-accounts keys list --iam-account="$ADMIN_SDK_SA" --project="$S" --managed-by=user   # empty
gcloud iam service-accounts disable "$ADMIN_SDK_SA" --project="$S"
gcloud iam service-accounts describe "$ADMIN_SDK_SA" --project="$S" --format='value(disabled)'        # True

# The deploy SA: Hosting, and reading whether its API is on; usable only through the GitHub pool in ryftlab-prod.
gcloud iam service-accounts create site-deployer --project="$S" --display-name="CI: Firebase Hosting deploys"
SITE_SA=site-deployer@$S.iam.gserviceaccount.com
gcloud projects add-iam-policy-binding "$S" --member="serviceAccount:$SITE_SA" \
  --role=roles/firebasehosting.admin --condition=None
# firebase deploy reads whether the Hosting API is on (serviceusage services.get); without this a 403 (10f).
gcloud projects add-iam-policy-binding "$S" --member="serviceAccount:$SITE_SA" \
  --role=roles/serviceusage.serviceUsageConsumer --condition=None
gcloud iam service-accounts add-iam-policy-binding "$SITE_SA" --project="$S" \
  --role=roles/iam.workloadIdentityUser --member="$(terraform -chdir=infra/gcp output -raw site_principal_set)"
gcloud projects get-iam-policy "$S" --format='table(bindings.role,bindings.members)'  # you as owner, the SA's two roles, fbsvc's two roles, nothing else
gcloud iam service-accounts get-iam-policy "$SITE_SA" --project="$S" --format=json \
  | jq -r '.bindings[] | .role + " " + (.members | join(" "))'
#   roles/iam.workloadIdentityUser principalSet://iam.googleapis.com/projects/<n>/locations/global/workloadIdentityPools/github/attribute.environment/site
gcloud iam service-accounts keys list --iam-account="$SITE_SA" --project="$S" --managed-by=user   # empty
```

**If you bound `site-deployer` before the split** (C1's 10a, to the repository's set
`…/attribute.repository_id/<id>`), swap it. Add the `site` set first, then remove the old one, and
print what is left:

```bash
S=ryftlab-site; SITE_SA=site-deployer@$S.iam.gserviceaccount.com
gcloud iam service-accounts add-iam-policy-binding "$SITE_SA" --project="$S" \
  --role=roles/iam.workloadIdentityUser --member="$(terraform -chdir=infra/gcp output -raw site_principal_set)"
gcloud iam service-accounts remove-iam-policy-binding "$SITE_SA" --project="$S" --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/$(terraform -chdir=infra/gcp output -raw project_number)/locations/global/workloadIdentityPools/github/attribute.repository_id/$(gh api repos/timkrebs/ryft-platform --jq .id)"
gcloud iam service-accounts get-iam-policy "$SITE_SA" --project="$S" --format=json \
  | jq -r '.bindings[] | .role + " " + (.members | join(" "))'
#   roles/iam.workloadIdentityUser principalSet://iam.googleapis.com/projects/<n>/locations/global/workloadIdentityPools/github/attribute.environment/site
```

**Stop if** any of these:
- `billingEnabled` isn't `False`;
- `addfirebase` asks for a plan or a billing account;
- the project policy names anyone but you, `site-deployer` (with `roles/firebasehosting.admin` and
  `roles/serviceusage.serviceUsageConsumer` and nothing else) and `firebase-adminsdk-fbsvc` (with
  `roles/firebase.sdkAdminServiceAgent` and `roles/iam.serviceAccountTokenCreator` and nothing else);
- `firebase-adminsdk-fbsvc` has a user-managed key (you didn't make one, so find out who did before
  you delete it), or `describe` doesn't say it is disabled;
- the SA's policy has any line but the one `workloadIdentityUser` line ending in
  `attribute.environment/site`, especially `attribute.repository_id/…` or `attribute.environment/deploy`;
- the key list isn't empty.

The pool's condition pins the repository and owner by ID, `deploy.yml`, main, push or dispatch, and a
GitHub-hosted runner. So the `site` set is exactly that workflow's jobs in the environment `site`.

**10b. Two console settings** (Firebase console → `ryftlab-site`):
- Hosting → the site's menu → **release storage settings**: keep the 10 newest releases. On Spark
  storage is capped at 10 GB, every deploy keeps a version, and over the cap no deploy goes through
  *(the setting's wording is unchecked)*.
- Project settings → Integrations → **Cloud Logging**: leave it unlinked. Linked, Hosting would log
  every visitor's IP address and user agent for 30 days (design §8).

**10c. The custom domains** (C1 merged). Hosting → Add custom domain → `ryftlab.de` (quick setup),
then `www.ryftlab.de` with "Redirect to an existing website" → `ryftlab.de`. Copy every record the
wizard shows for each, exactly: for the apex, its A record(s) and the TXT `hosting-site=ryftlab-site`;
for www, either A record(s) and a TXT, or one CNAME (`ryftlab-site.web.app`).

**Stop if** the console asks for Blaze to connect a domain (Spark doesn't allow it: stop, you
decide), or the wizard asks for any other record (an AAAA, an `_acme-challenge` TXT): `site.tf`
doesn't model it.

**10d. The records, then apply 3.** The values go into `infra/gcp/site.auto.tfvars` (the
variables are in `infra/gcp/site.tf`), in a PR:

```bash
git switch main && git pull --ff-only
git switch -c infra/site-dns
$EDITOR infra/gcp/site.auto.tfvars     # site_apex_a, site_apex_txt; site_www_a + site_www_txt, or site_www_cname
git commit -am "infra: the site's DNS records (GCP apply 3)"
git push -u origin infra/site-dns
gh pr create --fill
gh pr checks --watch                   # ci-ok green (the terraform job runs tests/site.tftest.hcl)
gh pr merge --squash --delete-branch
git switch main && git pull --ff-only
terraform -chdir=infra/gcp plan -out=apply-3.tfplan
terraform -chdir=infra/gcp apply apply-3.tfplan
rm infra/gcp/apply-3.tfplan
```

Its merge runs `deploy.yml` (the services, as on every merge; "Site off").

**Stop if** the plan has anything but `google_dns_record_set.site_*` to add and
`google_dns_record_set.apex_txt` to change (it gains the `hosting-site=` string): with A records
for www **3 to add, 1 to change, 0 to destroy**; with a CNAME for www **2 to add, 1 to change, 0 to
destroy**.

**10e. The certificates.** Wait until the console shows both domains as connected. That takes 15
minutes to 24 hours: the records must resolve and the CAA set already allows `pki.goog` and
`letsencrypt.org`.

```bash
dig +short A ryftlab.de @"$(terraform -chdir=infra/gcp output -json name_servers | jq -r '.[0]')"   # the wizard's address(es)
curl -sI https://ryftlab.de/ | head -n 1                    # an HTTP status line, no TLS error (a 404 before the first deploy)
```

**Stop if** a certificate is still pending after 24 hours.

**10f. The switch, and the first site deploy.** Only after all three parts of 10a:
- the environment `site` exists;
- the split was applied at least ten minutes ago (IAM's propagation), and the dispatch after it
  was green;
- `ryft-deployer`'s one `workloadIdentityUser` member ends in `attribute.environment/deploy`
  (no repository-wide member left from 10a.2's fallback);
- `site-deployer`'s one member ends in `attribute.environment/site`.

Check the last two again:

```bash
gcloud iam service-accounts get-iam-policy ryft-deployer@ryftlab-prod.iam.gserviceaccount.com --project=ryftlab-prod \
  --format=json | jq -r '.bindings[] | select(.role == "roles/iam.workloadIdentityUser") | .members[]'   # …/attribute.environment/deploy alone
terraform -chdir=infra/gcp output -raw site_principal_set; echo         # …/attribute.environment/site
gcloud iam service-accounts get-iam-policy site-deployer@ryftlab-site.iam.gserviceaccount.com --project=ryftlab-site \
  --format=json | jq -r '.bindings[].members[]'                          # that one line, nothing else
gh variable set SITE_DEPLOY --body on -R timkrebs/ryft-platform
gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform
gh run list --workflow deploy.yml -L 1 -R timkrebs/ryft-platform    # the run's ID
gh run watch <run id> --exit-status -R timkrebs/ryft-platform
```

A run by hand deploys the services from main's tip too, with the smoke (as on every merge).

**Stop if** either account's members aren't its one line. **Stop if** `site` is red:
- Sign-in, or the live-release step's token, fails with a permission error on
  `iamcredentials…generateAccessToken`. Then `site-deployer` isn't bound to the `site` set (10a.3),
  or the job's environment isn't `site`. Set `SITE_DEPLOY` off. Never bind it to the `deploy` set
  or the repository's set.
- `firebase deploy` names a missing permission: add only that role to `site-deployer` on
  `ryftlab-site`, then dispatch again.
  - Found on the first deploy (2026-11-17, run 44101115872): a 403 on
    `serviceusage…/services/firebasehosting.googleapis.com` (`services.get`) is
    `roles/serviceusage.serviceUsageConsumer`. 10a.3 grants it now, so a new project doesn't hit it.
- It refuses the credentials themselves (the `external_account` file of WIF; they worked on the
  first deploy, 2026-11-17, run 44101115872). Set `SITE_DEPLOY` off and report it; the fix is a
  change in `deploy.yml`.
- The live check fails: the new release is live. Read its FAIL lines; if the site is broken, roll
  back (GCP → Deploy, roll back, rotate → The site).

**10g. The checks by hand**, the same assertions as the job's, plus Apple's CDN:

```bash
node web/scripts/check-hosting.mjs --base https://ryftlab.de --www https://www.ryftlab.de \
  --policy-version "$(sed -n 's/^PRIVACY_POLICY_VERSION=//p' deploy/run/config.env)" --apple-cdn
for p in /privacy /impressum /terms /support; do
  curl -s -o /dev/null -w "%{http_code} $p\n" "https://ryftlab.de$p"            # 200 each
done
curl -s -o /dev/null -w "%{http_code} %{redirect_url}\n" https://ryftlab.de/de/privacy   # 301 …/privacy (web D18)
curl -sI https://ryftlab.de/.well-known/apple-app-site-association           # 200, content-type: application/json, no location
curl -sI 'https://ryftlab.de//evil.example/' | grep -i '^location' || echo 'no location'   # Google's front end: 307 /evil.example/ (web D21); never //evil.example, /\… or another host
curl -sI https://www.ryftlab.de/privacy | grep -i '^location'                # https://ryftlab.de/privacy
curl -sI https://ryftlab.de/admin | grep -ci '^content-security-policy'      # 1
```

**Stop if** any line of `check-hosting.mjs` says FAIL, or any check above differs. Apple's CDN may
lag its first fetch: if only `--apple-cdn` fails, run it again after an hour, and stop if it still
fails after 24 hours.

**10h. The app.** The app build uses `https://api.ryftlab.de` and `https://ingest.ryftlab.de`. App
Store Connect's privacy, support and marketing URLs move to `https://ryftlab.de/…`. Check the
AASA's team ID. The Sign in with Apple return URL stays Supabase's. Until here, only
you sign in. Testers wait for C2 in force (step 15).

### 11. Observability on Cloud Monitoring *(D236)*

Cloud Monitoring is RYFT's only observability (D236, which supersedes D224's Grafana half): what
Google measures itself (Cloud Run, Pub/Sub), log-based metrics counted from lines the services
write, five uptime checks, the dashboard "RYFT · Overview" and six alert policies, all in
`infra/gcp` (`monitoring.tf`, `alerts.tf`). Step 11 built five, with two metrics; the sixth, feedback
and admin access on a third metric, is your decision of 2026-11-19 (D244) and its own apply, step
14a.9. Six is the cap, a test (`six_conditions_at_most`). The policies notify two channels you made
by hand in the console: the email `ryft-timkrebs` and the Google Cloud app's. What each policy means and its
first look: **When an alert fires**, below; the whole picture: OBSERVABILITY → Cloud Monitoring.
The Grafana steps that stood here (the reader key, the data source, the OTLP push, the synthetics,
the app rules) never ran, and are gone with D236.

The parts run in this order, 11.1 to 11.5 in one sitting, once the PR "infra: observability on
Cloud Monitoring; Grafana Cloud out" is merged and step 10 serves the site. 11.6 is a PR of its own
after 11.4. 11.7 deletes what is left in Grafana Cloud. 11.8 runs once, in this project only: its
11.1 to 11.3 ran before D237.

**11.1 The app channel's id, a workspace variable** *(D237)*. The Google Cloud app's device
("Mobile Devices" in the console) is a notification channel the API doesn't list, and only the
console can pick it. So you attach it to one policy by hand, read its resource name there, and give
Terraform that name. Its display name (your phone's) never reaches Terraform.

1. Console → **Monitoring** → **Alerting** → the policy **RYFT · Dead letters held** → **Edit** →
   **Notifications**: the Google Cloud app's device (under Mobile Devices), next to `ryft-timkrebs`
   → **Save**. On a new project, where no policy exists before 11.3, make a throwaway one instead
   (**Create policy**, any metric, the device as its only channel), and delete it after 2.
2. The resource name (the projection keeps the policies' edit records, which name who edited them,
   off the screen):
   ```bash
   gcloud monitoring policies list --project=ryftlab-prod --format='json(displayName,notificationChannels)'
   ```
   The policy you edited lists `projects/…/notificationChannels/<number>` entries. The email
   channel's is in every other policy too. The new one, the entry that isn't the email channel's,
   is the app's: written here as `<the app channel's id>`, the whole `projects/…/notificationChannels/<number>`.
3. app.terraform.io → organization `ryft-lab` → Workspaces → `ryft-gcp` → **Variables** →
   Workspace variables → **+ Add variable**. Variable category **Terraform variable**. Key
   `alert_app_channel`. Value `<the app channel's id>`. **HCL** off. **Sensitive** off: an opaque id
   that grants nothing, and Terraform doesn't mark it sensitive either, so the plan shows it in each
   policy (D237). Description: `The Google Cloud app's notification channel, by resource name (D237)`.
   **Add variable**.

While Email in the same console page lists `ryft-timkrebs`, the email channel needs nothing here:
`alerts.tf` names it. The id is per device: never in a committed file (`tests/monitoring.tftest.hcl`
fails on one). A new phone is a new device: 11.1 again, and the variable's new value.

**Stop if** the edited policy lists no entry besides the email channel's (the device wasn't saved:
Edit again; still none after a second Save: stop and report it, don't guess an id), or more than one (two devices: keep the one you want, Save, read again); the console
doesn't offer the device (open the Google Cloud app on the phone, signed in to the account that owns
`ryftlab-prod`, with its notifications allowed, then Edit again); or the variable's value isn't the
whole `projects/…/notificationChannels/<number>` (the plan refuses anything else).

**11.2 The plan.** First the apply account's four Monitoring roles, once (step 2d grants them on a
new project; `infra/gcp/README.md` → Variables):

```bash
P=ryftlab-prod; APPLY_SA=hcp-terraform-apply@$P.iam.gserviceaccount.com
for r in roles/monitoring.uptimeCheckConfigEditor roles/monitoring.dashboardEditor \
         roles/monitoring.alertPolicyEditor roles/monitoring.notificationChannelViewer; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$APPLY_SA" --role="$r" --condition=None
done
gcloud projects get-iam-policy "$P" --flatten='bindings[].members' --filter="bindings.members:$APPLY_SA" \
  --format='value(bindings.role)'                   # sixteen of step 2d's (nineteen once 16.1 ran), never roles/owner
for sa in $(gcloud iam service-accounts list --project="$P" --format='value(email)'); do
  echo "$sa $(gcloud iam service-accounts keys list --iam-account="$sa" --managed-by=user --format='value(name)' | wc -l)"
done                                                # every account 0, grafana-reader included
```

Then, from main:

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=monitoring.tfplan
```

```text
# Step 11's plan (serve = true, public_invokers = true, the variable set): 13 to add, 0 to change, 2 to destroy.
#   read: data.google_monitoring_notification_channel.email[0] (ryft-timkrebs); the app's is the variable
#   2 log-based metrics (ryft_ticks, ryft_errors)                                             =  2
#   + 5 uptime checks (ryft-site-home, ryft-site-privacy, ryft-site-aasa, ryft-api-ready,
#     ryft-ingest-ready)                                                                      =  5  (7)
#   + 1 dashboard (RYFT · Overview)                                                           =  1  (8)
#   + 1 uptime policy + 4 PromQL policies (5xx, tick missing, dead letters, event backlog)    =  5 (13)  to add
#   google_service_account.this["grafana-reader"]
#   + google_project_iam_member.grafana_reader (roles/monitoring.viewer)                      =  2       to destroy
# The variable unset (the gate off): 8 to add, 0 to change, 2 to destroy; no channel read, no policy.
# Under the stop line (public_invokers = false): 10 to add with the gate on, 6 with it off (no
# ryft-api-ready, no ryft-ingest-ready, no tick policy); the same 2 to destroy.
```

Each policy's `notification_channels` is two entries, the email channel's name, then `<the app
channel's id>` in clear (D237). Outputs: `grafana_reader_email` goes,
`monitoring` comes. No change to the secrets (`otlp-token` and its accessor stay, 11.6), the
invoker bindings, the topics and subscriptions, the log bucket and sink, the zone, the WIF pool or
the deployer. The two destroys hold no data: `grafana-reader` never had a key (the loop above), and
nothing reads as it.

**Stop if** the counts are not your gate's line above; anything changes, or anything else is
destroyed; the email channel's data source fails (no channel, or more than one, named
`ryft-timkrebs`); the plan fails with `alert_app_channel is the email channel ryft-timkrebs` (11.1's
2 took the email's entry: the other one is the app's) or with the variable's validation (not the
whole resource name); the two entries of a policy's `notification_channels` are the same; a 403
on `monitoring.*` (the four roles above); or `grafana-reader` has a key (delete it with
`gcloud iam service-accounts keys delete`, then find out who made it).

**11.3 The apply.**

```bash
terraform -chdir=infra/gcp apply monitoring.tfplan
rm infra/gcp/monitoring.tfplan
terraform -chdir=infra/gcp output monitoring        # alerting true, the 5 policies, the 5 checks, the 2 metrics (6 and 3 since 14a.9)
terraform -chdir=infra/gcp plan                     # No changes
```

Monitoring → Uptime checks lists the five; each is green within 10 minutes (two runs from each of
the three locations). Monitoring → Dashboards → **RYFT · Overview** shows requests, instances and the
backlog at once; the ticks and the ERROR lines fill in from now on.

**"RYFT · Tick missing" may open once, by design.** A log-based metric counts from its creation,
not back, so until the first tick after the apply has been counted (up to 15 minutes, two ticks if
the query wants two points, plus Monitoring's lag) no service has an ok tick: one incident, all four
services, on both channels. It closes within the hour (the first counted ticks, then 5 minutes
held), which also proves the metric counts the tick lines that go to `ryft-logs`.

**Stop if** the second plan isn't "No changes" (a dashboard that differs at every plan is a bug of
this PR's JSON: report it); a check isn't green after 15 minutes (the check's page names the
location and the answer); or "Tick missing" is still open 90 minutes after the apply. Then Logs
Explorer: `jsonPayload.message="tick"` must show a line per service every 15 minutes. Lines there
but an empty ticks panel on the dashboard: the metric doesn't see them; stop, and report it.

**11.4 A test alert on both channels.** One message to a dead-letter topic: the hold subscription
holds it, and "RYFT · Dead letters held" fires within 10 to 15 minutes (evaluated every 5 minutes,
held 5; Pub/Sub's metrics lag 2 to 4 minutes):

```bash
CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB=https://europe-west1-pubsub.googleapis.com/ \
  gcloud pubsub topics publish ryft-dlq-notify --project=ryftlab-prod --message='runbook step 11.4: a test, not an event'
```

Both the email to `ryft-timkrebs` and a push on the Google Cloud app name the policy and
`dlq-notify-hold`. Then clear it:

```bash
scripts/dlq.sh --project ryftlab-prod list           # dlq-notify-hold: the one test letter
scripts/dlq.sh --project ryftlab-prod purge notify   # type PURGE
```

Monitoring → Alerting → Incidents: the incident closes within 15 minutes, and both channels get the
closing notice.

**Stop if** a channel gets nothing within 20 minutes (Monitoring → Alerting → the incident: opened
but not delivered is the channel; Edit notification channels → its row → **Send test
notification**; only the email arrives: the policy's second channel isn't the device, so check
`alert_app_channel` against 11.1's 2); `list` shows another letter than yours (then it is a real one: **Dead letters
and the outbox**, before the purge); or the incident is still open 20 minutes after the purge.

**11.5 Error Reporting's notifications** (free). Console → **Error Reporting**. It needs the
Error Reporting API, which is not on step 2a's keep list (`project.tf`, pinned by
`tests/guards.tftest.hcl`). Where the page shows error groups, **Configure notifications** (top of
the page) → tick the `ryft-timkrebs` email and the app channel → **Save**: you then hear of each new
error group and of each resolved one that comes back.

**Stop if** the page asks to enable the API. Enabling it is your decision, not a click on the way.
Three things speak against it today (D236):
- it is an API outside the keep list, so it needs a PR to `project.tf`;
- Error Reporting keeps its error events on its own terms, which the privacy text's 14 days in the
  EU may not cover, so C2 would have to say so;
- it groups only errors whose stack trace is in a field it knows (`message`, `stack_trace`,
  `exception`), and the services' `panic` line has it in `stack`, so panics wouldn't group.

If you want it anyway, say so and it gets its own PR: the API in `project.tf`, the privacy check,
and the panic line's field. Until then, the `ryft_errors` panel and the dashboard's logs panel show
every ERROR line.

**11.6 `otlp-token`, in a PR of its own** *(after 11.4, and after a deploy of this PR's revisions)*.
No revision of this PR reads the secret, but every older one does, and a revision whose secret is
gone doesn't start: a rollback to one (**Deploy, roll back, rotate**) needs it. So first, no
revision that can still serve may read it:

```bash
for s in api ingest scoring notify; do
  gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod \
    --format='value(status.traffic[].revisionName)'                     # the revisions serving
done
for s in api ingest scoring notify; do
  for r in $(gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod --format='value(status.traffic[].revisionName)'); do
    echo "$s $r $(gcloud run revisions describe "$r" --region=europe-west1 --project=ryftlab-prod --format=json | grep -c otlp-token)"
  done
done                                                                    # every line ends in 0
```

Then the PR. `prevent_destroy` stops a plain removal, so it takes two applies:
1. **PR 1:** `otlp-token` leaves `local.secret_accessors` (`secrets.tf`); a `moved` block puts its
   container into a new `google_secret_manager_secret.retired["otlp-token"]` without
   `prevent_destroy`, and its accessor into `…_iam_binding.retired["otlp-token"]`;
   `tests/iam.tftest.hcl` and `guards.tftest.hcl` follow (12 → 11, two secret blocks for one).
   Plan and apply: 0 to add, 0 to change, 0 to destroy, 2 moved. After its merge, from main:
   ```bash
   git switch main && git pull
   terraform -chdir=infra/gcp plan -out=otlp-retire.tfplan   # Plan: 0 to add, 0 to change, 0 to destroy; the 2 "has moved to" lines
   terraform -chdir=infra/gcp apply otlp-retire.tfplan
   rm infra/gcp/otlp-retire.tfplan
   terraform -chdir=infra/gcp plan                           # No changes
   ```
   The two moves: `google_secret_manager_secret.this["otlp-token"]` → `.retired["otlp-token"]` and
   `google_secret_manager_secret_iam_binding.accessor["otlp-token"]` → `.retired["otlp-token"]`.
2. **PR 2:** both `retired` resources and the `moved` blocks go; `scripts/check_cloudrun.py`'s
   `ACCESSORS` loses `otlp-token`. Plan and apply: 0 to add, 0 to change, 2 to destroy (the
   container, every version with it, and its binding).

**Stop if** any serving revision reads `otlp-token`; PR 1's plan adds, changes or destroys anything, or
moves anything but those two; PR 2's
plan destroys anything but those two. Rolling back past this PR after PR 2 needs the secret back
(a new container and a version: step 5's `put`).

**11.7 Grafana Cloud, the rest.** What RYFT on GCP had there: nothing live (11a–j never ran). So:
1. HCP Terraform → `ryft-gcp` → Variables: delete `grafana_url`, `grafana_datasource_uid` and
   `grafana_auth` where listed. Until then each plan warns of an undeclared variable.
2. Grafana Cloud → the stack → Administration → Users and access → Service accounts and Cloud
   access policies: delete every token made for RYFT on GCP (`terraform-ryft-gcp`, an OTLP
   `metrics:write` token, an SM access token). Testing & synthetics → Synthetics → Checks: delete
   any `ryft-*` check. Alerting → Alert rules: the six groups last loaded from EKS (`ryft/ryft`,
   `ryft/ryft-pipeline`, `ryft/ryft-ops`, `ryft/ryft-admission`, `ryft/ryft-vault` and
   `ryft-admission/ryft-admission-intended`) see no series and nothing loads them any more: delete
   them by hand, or with the stack (3).
3. The stack itself, at grafana.com → My Account → the stack → **Delete**. Nothing sends to it since
   the EKS cluster and its collector went (D3). Deleting it takes its alert rules along.

**Stop if** the stack's usage (grafana.com → the stack → Usage) still shows metrics or logs coming
in: something still sends. Find it before you delete the stack.

**11.8 Once: the app channel by id** *(D237; after 11.6's first apply. 1 to 3 **before** the PR
"infra: the app alert channel by id" is merged, 4 to 7 after it)*. 11.1 to 11.3 ran here on
2026-11-18 with the app channel looked up by display name, and the lookup found the email channel:
each of the five policies lists `ryft-timkrebs` twice and no app.

The order matters. With the PR merged and `alert_app_channel` unset, the gate is off, and any plan
from main destroys the five policies. So the new variable goes in first: the code before the PR
doesn't declare it, and a plan there only warns of it and changes nothing. Merge the PR only once 3
is done. In this order:

1. **The device on one policy**: 11.1's 1, on **RYFT · Dead letters held**.
   **Stop if** the console doesn't offer the device (11.1's stop rule).
2. **Its id**: 11.1's 2. Dead letters held now lists the email channel's entry and one more, the
   app's; the other four still list the email's twice.
   **Stop if** there is no entry besides the email channel's, or more than one.
3. **The new variable**: 11.1's 3, key `alert_app_channel`, Sensitive off.
   **Stop if** the value isn't the whole `projects/…/notificationChannels/<number>`.

   **Now merge the PR** (its checks green). Nothing deploys; nothing in GCP changes until 6.
4. **The old variable goes**: `ryft-gcp` → **Variables** → Workspace variables → the row
   `alert_app_channel_name` → **⋯** → **Delete**. Nothing reads it any more; while it is listed,
   each plan warns of a value for an undeclared variable.
   **Stop if** the list doesn't show `alert_app_channel` afterwards (the wrong row went: add it again, 3).
5. **The plan**, from main:
   ```bash
   git switch main && git pull
   terraform -chdir=infra/gcp plan -out=app-channel.tfplan
   ```
   ```text
   # Step 11.8's plan: 0 to add, 5 to change, 0 to destroy.
   #   read: data.google_monitoring_notification_channel.email[0] (ryft-timkrebs)
   #   ~ google_monitoring_alert_policy.uptime[0]                RYFT · Uptime check failing
   #   ~ google_monitoring_alert_policy.promql["http-5xx"]       RYFT · 5xx over 2 % (api, ingest)
   #   ~ google_monitoring_alert_policy.promql["tick-missing"]   RYFT · Tick missing
   #   ~ google_monitoring_alert_policy.promql["push-backlog"]   RYFT · Event backlog over 15 minutes
   #   ~ google_monitoring_alert_policy.promql["dead-letters"]   RYFT · Dead letters held             = 5 to change
   #     each: notification_channels [the email's, the email's] -> [the email's, <the app channel's id>]
   #     (a "-" line with the email channel's id, a "+" line with the app's)
   # 4 to change if the console saved Dead letters held's list as exactly [the email's, the app's], in
   # that order: it then matches the code and isn't in the plan. Under the stop line (public_invokers =
   # false) there is no Tick missing: 4, or 3.
   ```
   The channels are an ordered list, the email's first (`alerts.tf`), so a policy whose two entries
   are the same, or in the other order, is a change. Dead letters held may show more lines: what the
   console wrote when you saved goes back to the code's values. No change to the uptime checks, the
   dashboard, the log-based metrics, the secrets (as 11.6 left them), the invokers, Pub/Sub, the log
   bucket and sink, the zone, WIF or the deployer; no change to the outputs.
   **Stop if** anything is added or destroyed (a policy replaced, `-/+`, gets a new id and loses its
   incidents); any other resource changes; one of the other four policies changes anything but
   `notification_channels`; the plan shows "has moved to" lines (11.6's first apply hasn't run: do
   it first, then plan again); it fails with `alert_app_channel is the email channel ryft-timkrebs`
   (2 took the email's entry: the other one is the app's) or with the variable's validation (3); it
   warns that `alert_app_channel_name` is undeclared (4 didn't save; the `grafana_*` warnings are
   expected until 11.7); or a 403 on `monitoring.*` (11.2's four roles).
6. **The apply**:
   ```bash
   terraform -chdir=infra/gcp apply app-channel.tfplan
   rm infra/gcp/app-channel.tfplan
   terraform -chdir=infra/gcp plan                     # No changes
   gcloud monitoring policies list --project=ryftlab-prod --format='json(displayName,notificationChannels)'
   ```
   Each of the five lists two entries: the email channel's, then the app's.
   **Stop if** the second plan isn't "No changes", or a policy lists anything else.
7. **11.4's test alert again, on both channels this time**: the email to `ryft-timkrebs` and a push
   on the Google Cloud app, then the purge and both closing notices.
   **Stop if** only the email arrives (the id isn't the device's: read it again, 2), or 11.4's stop
   rule.

A wrong id is fixed the same way: the variable's right value in HCP, then 5 and 6 (again 0 to add,
5 to change, 0 to destroy, each policy's second entry; 6 to change once 14a.9 is applied). Unsetting
the variable is no way back: it turns the gate off and destroys the five policies (six after
14a.9). Neither is reverting the PR after 4: the old
code's gate reads `alert_app_channel_name`, so add that variable back first (11.1's old value), or
its plan destroys the five as well.

### 12–14. The retirement, done by hand on 2026-11-19 (D246)

The Terraform route that stood here (D227: Route 53, the Vault keys, the bucket and the budget
destroyed from the old org's states, then the four `ryft-prod-euc1-*` workspaces safe-deleted after a
last check of the data key) can't run any more: the old HCP org `tim-krebs-org` is deleted, with
every state in it. D1 (#222) is merged; D2 (#223) is closed as obsolete. What was left of RYFT in AWS
account `173003892479` you deleted by hand on 2026-11-19, by ID, after a read-only inventory of
`eu-central-1` and the global services:

| What | ID | Now |
| --- | --- | --- |
| The Route 53 zone `ryftlab.de` | `Z0847310CSDZ20OKMOUQ`, 12 record sets | deleted (Cloud DNS serves the domain since step 6) |
| The Vault snapshot bucket | `ryft-prod-euc1-vault-snapshots-173003892479` | deleted (it was empty) |
| The AWS budget | `ryft-prod-euc1-monthly` | deleted |
| The KMS key vault-unseal | `2960364e…` | its policy's `NoDeletionOrDisable` removed, its alias deleted; PendingDeletion until 2026-11-26 |
| The KMS key vault-snapshots | `90b5cce6…` | the same; PendingDeletion until 2026-11-26 |
| The EKS cluster's KMS key | `fb345db3…` | PendingDeletion already, until 2026-12-14 |

A re-check found no RYFT resource left: no EKS cluster, VPC, NAT gateway, Elastic IP, instance, log
group, IAM role or policy, OIDC provider, SES identity or ECR repository. The tag index still lists two
Pod Identity associations; their clusters don't exist.

**The data key.** Its copy in the old state went with the org, so the last comparison against that
state (the old 14.1) can't run. Two copies remain: Secret Manager's `data-key` at the version
`deploy/run/api.yaml` pins (step 5 checked it against step 0's fingerprint), and the 1Password item
"RYFT data key" from step 0. Keep both.

### 14.3. GitHub: the AWS and GHCR secrets, the old promote App's key

The one step left, after D3 is merged. The workflows on main read the variables `GCP_DEPLOY`,
`DEPLOY_HOLD_SERVICES`, `SITE_DEPLOY`, `TESTFLIGHT_URL` and `SUPPORT_EMAIL`, and the secrets
`SUPABASE_ANON_KEY`, `SMOKE_EMAIL` and `SMOKE_PASSWORD` (in `deploy`). These names go:
- the secret `RYFT_DEPLOY_APP_KEY`, the old promote App's key (no job has read it since C1, #194);
- the variable `RYFT_DEPLOY_APP_CLIENT_ID`, that App's client ID;
- every secret named `AWS_*` or `GHCR_*`. No workflow on main has ever read one by name.

The paste lists everything first, in the repository and in `deploy`. It then deletes only these
names, each one only if no workflow on main names it, and lists again. It ends by checking that
`deploy` still holds the smoke user's three secrets. It runs from the repository's root, on a clean,
current main with D3 merged.

```bash
# 14.3, GitHub: the listed secrets and variables, in the repository and in the environment deploy.
R=timkrebs/ryft-platform
if [ -z "$(git status --porcelain --untracked-files=no)" ] && [ "$(git rev-parse --abbrev-ref HEAD)" = main ] \
  && git pull -q --ff-only && [ -z "$(git ls-files infra/aws infra/vault deploy/k8s)" ] \
  && gh auth status >/dev/null 2>&1; then
  unused() { ! git grep -q -w -e "$1" -- .github/workflows; }
  echo "== before"; gh secret list -R "$R"; gh secret list --env deploy -R "$R"
  gh variable list -R "$R"; gh variable list --env deploy -R "$R"
  for n in $(gh secret list -R "$R" --json name --jq '.[].name' | grep -E '^(AWS_|GHCR_)|^RYFT_DEPLOY_APP_KEY$'); do
    if unused "$n"; then gh secret delete "$n" -R "$R" && echo "deleted secret $n"; else echo "kept secret $n: a workflow reads it"; fi
  done
  for n in $(gh secret list --env deploy -R "$R" --json name --jq '.[].name' | grep -E '^(AWS_|GHCR_)|^RYFT_DEPLOY_APP_KEY$'); do
    if unused "$n"; then gh secret delete "$n" --env deploy -R "$R" && echo "deleted secret $n (deploy)"; else echo "kept secret $n (deploy): a workflow reads it"; fi
  done
  for n in $(gh variable list -R "$R" --json name --jq '.[].name' | grep -x RYFT_DEPLOY_APP_CLIENT_ID); do
    if unused "$n"; then gh variable delete "$n" -R "$R" && echo "deleted variable $n"; else echo "kept variable $n: a workflow reads it"; fi
  done
  for n in $(gh variable list --env deploy -R "$R" --json name --jq '.[].name' | grep -x RYFT_DEPLOY_APP_CLIENT_ID); do
    if unused "$n"; then gh variable delete "$n" --env deploy -R "$R" && echo "deleted variable $n (deploy)"; else echo "kept variable $n (deploy): a workflow reads it"; fi
  done
  echo "== after"; gh secret list -R "$R"; gh secret list --env deploy -R "$R"
  gh variable list -R "$R"; gh variable list --env deploy -R "$R"
  left=$(gh secret list --env deploy -R "$R" --json name --jq '.[].name')
  for k in SUPABASE_ANON_KEY SMOKE_EMAIL SMOKE_PASSWORD; do
    printf '%s\n' "$left" | grep -qx "$k" || echo "STOP: deploy no longer holds $k; deploy.yml's smoke needs it"
  done
else
  echo "STOP: not a clean, current main with D3 merged, or gh isn't signed in."
fi
```

**Worked:** the "after" lists show no `AWS_*`, `GHCR_*`, `RYFT_DEPLOY_APP_KEY` or `RYFT_DEPLOY_APP_CLIENT_ID`,
and no STOP line printed. A "kept … a workflow reads it" line means a workflow on main still names it: send
the output. Then delete the old promote App itself: GitHub → Settings → Developer settings → GitHub Apps →
the App → Advanced → Delete GitHub App. Nothing reads its key or its client ID any more.

### 14a. Before testers: auth mail, the restore drill, feedback

Three things to do before step 15 (D239). 14a.1 to 14a.8 change no Terraform, no service and no site.
They can run now, in any order. 14a.9, the sixth alert condition for feedback and admin access (D244),
is a plan and an apply after its PR's merge and deploy. Testers who sign up by email need 14a.1 to 14a.4
first. *Confirm email* is on (SUPABASE.md), and until then nobody outside the Supabase team gets the
confirmation mail. Sign in with Apple sends no mail.

The external sources were read through search results only, on 2026-11-18. WebFetch was refused in that
session, so each claim below names its page and is marked *(search)*.

**The risk.** The app uses email sign-up and password reset. Supabase's custom SMTP takes a host, a
port, a user name and a password, and documents no OAuth (XOAUTH2) option
([Supabase: Send emails with custom SMTP](https://supabase.com/docs/guides/auth/auth-smtp), as of
2026-11-18, *(search)*). Exchange Online is retiring exactly that sign-in, a password for SMTP AUTH client
submission to `smtp.office365.com:587`.

- **Certain:**
  - The Exchange Team blog post
    ["Updated Exchange Online SMTP AUTH Basic Authentication Deprecation Timeline"](https://techcommunity.microsoft.com/blog/exchange/updated-exchange-online-smtp-auth-basic-authentication-deprecation-timeline/4489835)
    is dated 2026-01-27 (version 3.0, 2026-01-29) *(search)*. It says Basic auth for SMTP AUTH is unchanged
    until December 2026. At the end of December 2026 it is disabled by default for existing tenants, and
    administrators can still enable it. Tenants created after December 2026 don't get it. Microsoft
    announces the final removal date in the second half of 2027.
  - The plan before it is on hold: a share of submissions rejected from 2026-03-01, all of them by
    2026-04-30. That was Message Center MC786329's earlier version; its update of 2026-01-27 carries
    the timeline above instead (mirror:
    [mc.merill.net/message/MC786329](https://mc.merill.net/message/MC786329), *(search)*). The first
    announcement was
    [Exchange Online to retire Basic auth for Client Submission (SMTP AUTH)](https://techcommunity.microsoft.com/blog/exchange/exchange-online-to-retire-basic-auth-for-client-submission-smtp-auth/4114750)
    in 2024-04 *(search)*.
  - Only Basic auth goes, meaning a user name and a password for client submission. OAuth (XOAUTH2)
    submission stays: the blog names OAuth as the supported method.
  - Two switches decide today. One is the tenant's `SmtpClientAuthenticationDisabled`
    (`Get-TransportConfig`). The other is the mailbox's (`Set-CASMailbox`), and the mailbox's wins. Security
    defaults block SMTP AUTH whatever both say. So does an authentication policy that blocks Basic auth
    ([Learn: Enable or disable SMTP AUTH](https://learn.microsoft.com/en-us/exchange/clients-and-mobile-in-exchange-online/authenticated-client-smtp-submission),
    as of 2026-11-18, *(search)*).
  - High Volume Email doesn't help. It delivers to recipients inside the tenant only
    ([Learn: Manage High Volume Email](https://learn.microsoft.com/en-us/exchange/mail-flow-best-practices/high-volume-mails-m365),
    as of 2026-11-18, *(search)*). Its external sending ended in June 2025
    ([Exchange Team blog on HVE](https://techcommunity.microsoft.com/blog/exchange/high-volume-email-continued-support-for-basic-authentication--other-important-up/4411197),
    post date not read, *(search)*). Testers are external.
  - The tenant is an existing one (live since 2026-11-14, D214), so the rule for new tenants doesn't apply.
- **Not certain:**
  - Whether the default at the end of December 2026 also turns off a mailbox you enabled explicitly, and
    which switch turns it back on. The search excerpts don't say.
  - The final removal date. It isn't announced. After it this route stops for good, because Supabase's
    SMTP has no OAuth.
  - Whether a GoDaddy tenant lets you use the Microsoft 365 admin center or Exchange Online PowerShell
    for this. GoDaddy documents its own switch.
- **Verdict:** the route works for the start of the beta, and it has an end date. Test again in the
  first week of January 2027 (14a.5). Have the fallback (14a.6) ready before the second half of 2027.

**14a.1 The sender mailbox.** Use a dedicated user, `no-reply@ryftlab.de`, with its own licence: GoDaddy →
Email & Office Dashboard → add a user. GoDaddy's plan and price are not checked here. Mail for the domain
already runs there (**Mail for ryftlab.de** below; the zone's records are `infra/gcp/dns.tf`).
- **Not a shared mailbox.** SMTP AUTH signs in as the mailbox's account, with its password
  ([Learn: set up a device or application to send email](https://learn.microsoft.com/en-us/exchange/mail-flow-best-practices/how-to-set-up-a-multifunction-device-or-application-to-send-email-using-microsoft-365-or-office-365),
  as of 2026-11-18, *(search)*). A shared mailbox has no sign-in of its own: people reach it through
  their own accounts (no Microsoft page on this was read here). Community answers say that a password and
  a sign-in for it then need a licence anyway (they aren't Microsoft's docs).
- **Not `support@ryftlab.de`.** Its password would sit in Supabase's settings, and that mailbox holds every
  tester's mail.
- **The account:** no admin role, no forwarding, no other use. Its password comes from 1Password's
  generator (item "RYFT no-reply SMTP") and goes nowhere but into Supabase (14a.3).
- **MFA.** Basic SMTP AUTH can't answer a second factor, and security defaults block it for the whole
  tenant. When you enable SMTP authentication, GoDaddy's switch (14a.2) turns security defaults off for
  the whole organisation
  ([GoDaddy: Enable SMTP Authentication](https://www.godaddy.com/help/enable-smtp-authentication-40981),
  as of 2026-11-18, *(search)*). After that, `support@` and your admin account keep a second factor
  only through per-user MFA. Turn it on for each of them before 14a.2. Your tenant may offer it in
  GoDaddy's dashboard or in Entra → Per-user MFA; which one wasn't checked.

**Stop if** you can't keep a second factor on every other account of the tenant once security defaults
are off. Then use the fallback (14a.6): an EU sender needs nothing from the tenant.

**14a.2 Authenticated SMTP, for that mailbox only.** Use one of these three, whichever your tenant gives
you:
1. GoDaddy, the tenant's own path ([GoDaddy](https://www.godaddy.com/help/enable-smtp-authentication-40981),
   *(search)*): Email & Office Dashboard → **Manage** next to `no-reply@ryftlab.de` → Account information →
   **Advanced Settings** → **SMTP Authentication** on → **Continue**.
2. The Microsoft 365 admin center ([Learn](https://learn.microsoft.com/en-us/exchange/clients-and-mobile-in-exchange-online/authenticated-client-smtp-submission),
   *(search)*): Users → Active users → `no-reply@ryftlab.de` → **Mail** → **Manage email apps** →
   **Authenticated SMTP** ticked → **Save changes**.
3. Exchange Online PowerShell (the same page):

   ```powershell
   Connect-ExchangeOnline
   Get-TransportConfig | Format-List SmtpClientAuthenticationDisabled                          # the tenant's default: leave it
   Set-CASMailbox -Identity no-reply@ryftlab.de -SmtpClientAuthenticationDisabled $false
   Get-CASMailbox -Identity no-reply@ryftlab.de | Format-List SmtpClientAuthenticationDisabled   # False
   ```

The mailbox's setting wins over the tenant's. The tenant can stay at `True`, and then only this mailbox
submits by SMTP. Leave `support@` alone.

Paths 2 and 3 leave security defaults as they are, and while they are on, SMTP AUTH fails whatever
the mailbox says (`535 5.7.139`). After 14a.1's per-user MFA on every other account, turn them off:
Microsoft Entra admin center → Identity → Overview → **Properties** → **Manage security defaults** →
**Disabled** (Learn, the same page, *(search)*). Path 1 does this for you.

**Stop if** the switch is missing or greyed out (the tenant blocks it), or 14a.1's stop applies.

**14a.3 Supabase's SMTP settings.** In the production project's dashboard:
1. Authentication → URL Configuration. The Site URL is `https://ryftlab.de`. The Redirect URLs are
   `ryft://auth/callback` and nothing on another domain (step 7, SUPABASE.md, D213). Leave them as they are.
2. Authentication → Emails → **SMTP Settings** → enable custom SMTP:

   | Field | Value |
   | --- | --- |
   | Sender email | `no-reply@ryftlab.de`. The account that signs in appears as the sender (the Learn page in the port's row); another address fails without Send As |
   | Sender name | `RYFT` (SUPABASE.md) |
   | Host | `smtp.office365.com` |
   | Port | `587`, STARTTLS (Microsoft's recommended port, [Learn](https://learn.microsoft.com/en-us/exchange/mail-flow-best-practices/how-to-set-up-a-multifunction-device-or-application-to-send-email-using-microsoft-365-or-office-365), *(search)*) |
   | Username | `no-reply@ryftlab.de` |
   | Password | pasted from the 1Password item; never into a file, a chat or a command |

   Leave the minimum interval as Supabase sets it. **Save**.
3. Authentication → Rate Limits → the rate limit for emails: **30 an hour**. That is Supabase's default once
   custom SMTP is on ([auth-smtp](https://supabase.com/docs/guides/auth/auth-smtp),
   [Rate limits](https://supabase.com/docs/guides/auth/rate-limits), as of 2026-11-18, *(search)*). Keep it.
   5 to 10 testers need a few mails a day, and 30 caps abuse. Exchange's own limits per mailbox are 30
   messages a minute and 10,000 recipients a day
   ([Learn: Exchange Online limits](https://learn.microsoft.com/en-us/office365/servicedescriptions/exchange-online-service-description/exchange-online-limits),
   as of 2026-11-18, *(search)*).
4. Authentication → Emails → the templates (*Confirm signup*, *Reset password*, *Change email address*,
   *Magic link*) stay as SUPABASE.md words them. The SMTP settings only change how the mail leaves. The
   templates' links, the Site URL and the redirect URLs are separate settings, and this step doesn't touch
   them. A link still goes to `https://<ref>.supabase.co/auth/v1/verify…` and comes back into the app
   through `ryft://auth/callback`.

**Stop if** Supabase refuses to save.

**14a.4 The test: a password reset to an address outside the team.** Supabase's built-in mailer delivers
only to the organisation's team (Supabase → Organization → Team;
[auth-smtp](https://supabase.com/docs/guides/auth/auth-smtp), *(search)*). So the test address must not be
on that team. Use a second address of yours that isn't, written here as `<the test address>`.
1. In the app (the TestFlight build), sign up with `<the test address>`. The confirmation mail arrives.
   Open its link on the iPhone. That proves *Confirm signup* too.
2. In the app: Sign in → Forgot password → `<the test address>`.
3. Within five minutes the mail is in that inbox, not in spam, from `RYFT <no-reply@ryftlab.de>`.
   Don't long-press the link: iOS's preview opens it and spends the one-time token, and the tap then
   fails as "link expired". Read the URL in the mail's source instead (4's original view), and never
   copy it: it is a live token. It must start with `https://<ref>.supabase.co/auth/v1/verify?` and carry
   `redirect_to=ryft://auth/callback`. Then tap it once: the app opens on Set new password. Set one.
4. Open the mail's original (its headers). They show `SPF: PASS`, `DKIM: PASS` with `d=ryftlab.de`, and
   `DMARC: PASS`. If DKIM fails and names `selector1`, the `selector1` line of **Mail for ryftlab.de** is
   due. DMARC is `p=quarantine`, so such mail lands in spam.
5. Sign in to Outlook on the web as `no-reply@` and open **Sent Items**. It holds a copy: Exchange keeps
   every SMTP AUTH submission there
   ([Learn: SMTP Authenticated Submission](https://learn.microsoft.com/en-us/troubleshoot/exchange/send-emails/smtp-submission-improvements),
   as of 2026-11-18, *(search)*). Each copy holds a tester's address and a one-time link. Delete it for
   good now: Shift+Delete, or empty Deleted Items and then **Recover items deleted from this folder** →
   purge. A plain delete keeps it in Recoverable Items, 14 days by default (the mailbox's
   `RetainDeletedItemsFor`, `Get-Mailbox no-reply@ryftlab.de | Format-List RetainDeletedItemsFor`). Then
   set a retention of 7 days on Sent Items, or empty it every week the same way. C2 must name this
   copy, and both windows.
6. Delete the test account in the app (Settings → delete the account), unless you keep it for tests.

**Stop if** the mail hasn't come after 15 minutes, or DMARC fails in 4. Supabase's Auth logs should show
the SMTP server's reply (not checked). `535 5.7.139` means SMTP AUTH is off for the tenant or the mailbox, or security defaults
are on. `550 5.7.30 Basic authentication is not supported for Client Submission` means Basic auth is off.
Both codes come from search results (*(search)*), not from a page read here. Don't retry in a loop: fix
the setting the reply names, then test once more. If it still fails with Basic auth off, use the fallback
(14a.6).

**14a.5 January 2027: the test again.** Microsoft's default changes at the end of December 2026 (above).
In the first week of January 2027, run 14a.4's steps 2 to 4 again.

**Stop if** it fails with `5.7.30` or `5.7.139`. Enable the mailbox again with 14a.2's switch, where
Microsoft still lets admins do that, and test once. If Microsoft no longer offers it, use the fallback.

**14a.6 The fallback: an EU sender** (nothing is built here). These three EU providers have a free tier
that covers a beta. Each sends through its own SMTP host and credentials, which its console shows (not
checked here). In Supabase only 14a.3's host, port, user name and password change. The sender, the rate
limit, the templates and the URLs stay.

| Provider | Free tier | DNS records it asks for |
| --- | --- | --- |
| Brevo (France) | 300 emails a day ([brevo.com/facts](https://www.brevo.com/facts/), as of 2026-11-18, *(search)*; the pricing page itself not read) | an apex TXT `brevo-code:…`; two DKIM CNAMEs, `brevo1._domainkey` and `brevo2._domainkey`, to targets under `dkim.brevo.com` (the account shows them); a DMARC record (ours is one: don't add a second). Guides disagree on whether Brevo needs an SPF include for this ([Brevo help](https://help.brevo.com/hc/en-us/articles/12163873383186), *(search)*): DMARC passes through the aligned DKIM either way |
| Mailjet (France, part of Sinch) | 6,000 emails a month, at most 200 a day ([mailjet.com/pricing](https://www.mailjet.com/pricing/), as of 2026-11-18, *(search)*) | `include:spf.mailjet.com` in the SPF; a DKIM TXT at `mailjet._domainkey` (Mailjet's pages also show it as a CNAME: use what the account shows); DMARC through DKIM, because SPF aligns only with a custom return path ([Mailjet: SPF and DKIM](https://documentation.mailjet.com/hc/en-us/articles/360049641733-Authenticating-Domains-with-SPF-and-DKIM-A-Complete-Guide), *(search)*) |
| Scaleway Transactional Email (France) | 300 emails a month per organisation, counted per recipient, then €0.25 per 1,000 ([scaleway.com pricing](https://www.scaleway.com/en/pricing/managed-services/), as of 2026-11-18, *(search)*) | an SPF include: the guide shows `include:_spf.tem.scaleway.com`, another page `_spf.scw-tem.cloud`, so use what the console shows; a DKIM TXT under `_domainkey` with the console's name; an MX only for a domain without one (ours is Microsoft's: keep it); DMARC: ours ([Scaleway: authenticate your domain](https://www.scaleway.com/en/docs/transactional-email/how-to/authenticate-domain/), *(search)*) |

Each provider's DPA, subprocessors and log retention are not read here, and neither is whether its free
tier brands the mail.

The zone's records are code in `infra/gcp/dns.tf`. The apex TXT set is `local.apex_txt`: the SPF is one
string of `local.m365_apex_txt` in it, and a provider's verification string (`brevo-code:…`) joins
`local.apex_txt`, not the Microsoft list. The SPF: `v=spf1 include:secureserver.net -all`, plus the provider's include, never a second
SPF. DKIM CNAMEs go into `local.mail_cnames`. A DKIM TXT needs a new `google_dns_record_set` there. DMARC is
`local.dmarc`. `infra/gcp/tests/dns.tftest.hcl` pins those values, so the PR changes it too. SPF allows at
most 10 DNS lookups, and how many `secureserver.net` uses wasn't checked: check it before that PR. The PR is
code, and the apply is yours (`terraform -chdir=infra/gcp plan`, then `apply`).

**The privacy text must name the one chosen.** The text in force (2026-10-26) names Supabase "(database and
sign-in)". For email it names Google Workspace, for `support@`. It names no Microsoft, and no one who sends
the sign-in mails. Whichever route you take, C2 must add three things:
- who sends the sign-up, reset and email-change mails, and what passes: the address, the link and the
  time;
- on this route, Microsoft (Exchange Online, the tenant through GoDaddy) and the Sent Items copy with its
  retention; on the fallback, the provider, where it is, its contract and its log retention;
- Microsoft instead of Google Workspace for `support@` (D214 says so already).

**Stop if** the provider's console asks for a record not in the table above. Then read its page first; the
PR names every record it adds.

**14a.7 The restore drill: Supabase Pro's daily backups, without touching production.** What Supabase
offers today ([Database Backups](https://supabase.com/docs/guides/platform/backups),
[Restore to a new project](https://supabase.com/docs/guides/platform/clone-project),
[Restore Dashboard backup](https://supabase.com/docs/guides/platform/migrating-within-supabase/dashboard-restore),
[Database backups](https://supabase.com/features/database-backups), all as of 2026-11-18, *(search)*):
- Pro keeps the last 7 daily backups. Logical backups are for databases up to 15 GB, physical ones for
  bigger databases and for PITR. Every project on Postgres 15.8.1.079 or newer uses physical backups.
- **Restore to a new project** (Database → Backups → the tab **Restore to a New Project**) needs a paid
  plan with physical backups. The copy gets the source's compute size, disk, SSL enforcement and network
  restrictions. It stays in the source's region. It is a database-only copy: Auth's settings, the keys
  and SMTP don't come along. A cost overview shows before you start.
- **A download** shows in the dashboard only for older projects on logical backups. Physical backups
  can't be downloaded. The CLI route (`supabase db dump`) reads production itself.

**The route: restore to a new project.** The copy stays in Frankfurt with the same processor, Supabase, so
no new place holds Art. 9 data. A download would put the testers' heart rate and sleep on the laptop,
outside Supabase's controls. For a physical backup it isn't offered at all.

**The cost.** Compute is billed by the hour, and a started hour counts as a full one. Micro is $0.01344
an hour (about $10 a month); Small is $0.0206 an hour
([Manage Compute usage](https://supabase.com/docs/guides/platform/manage-your-usage/compute), as of
2026-11-18, *(search)*). Pro includes $10 of compute credits a month, which cover one Micro project.
Production uses them, so you pay for the copy's hours. A two-hour drill on Micro costs about $0.03. Left
running for a month, the copy costs about $10. Disk: a database this small stays inside a project's
included disk (8 GB per project,
[Manage Disk size usage](https://supabase.com/docs/guides/platform/manage-your-usage/disk-size),
*(search)*; not checked for a copy). If production's disk was grown, the copy mirrors it, and the overview
shows the cost.

1. **Before.** In production, open Database → Extensions: `pg_cron` and `pg_net` are off. A physical copy
   starts their jobs at once (Restore to a new project, above). No migration uses either. Then Database →
   Backups: note the newest backup's time, and whether a Download button shows (logical) or not (physical).
   And Project Settings → **Compute and Disk**: note production's compute size, which the copy gets. The
   repository doesn't record it; the cost above assumes Micro, and Small is about half as much again.

   **Stop if** `pg_cron` or `pg_net` is on: the copy would run their jobs too. First find out who turned it
   on.
2. **The restore.** Database → Backups → **Restore to a New Project** → the newest backup. Read the
   overview: the region is Central EU (Frankfurt), and the compute size is production's. Then **Restore**.
   Note the time, wait until the new project is healthy, and note how long it took.

   **Stop if** the tab is missing (no physical backups), the region isn't Frankfurt, or the size is bigger
   than production's. A download instead is your decision (D239), not a step on the way.
3. **Lock the copy.** In the copy, turn the Data API off (Project Settings → Data API, as SUPABASE.md step 4
   says). Migration 0007's revokes hold anyway. Set up no SMTP, keys, users or service. Connect nothing to it:
   no app, no compose stack, no `psql` from the laptop. So this drill replaces **Backups → Restore
   drill, monthly** for RYFT on GCP, its smoke against the copy included.

   **Stop if** the Data API won't turn off. Delete the copy (5) and ask Supabase why.
4. **The check, counts only.** Run the same query in the copy's SQL editor, then in production's. It only
   reads. It returns one row of numbers: no row content, no id. Copy nothing out of either page: no CSV,
   no export, no screenshot into a chat.

   ```sql
   SELECT
     (SELECT max(version) FROM schema_migrations)               AS schema_version,
     (SELECT count(*) FROM schema_migrations)                   AS migrations,
     (SELECT count(*) FROM pg_roles
       WHERE rolname IN ('ryft_api', 'ryft_ingest', 'ryft_scoring', 'ryft_notify')) AS service_roles,
     (SELECT count(*) FROM auth.users)                          AS auth_users,
     (SELECT count(*) FROM users)                               AS users,
     (SELECT count(*) FROM deleted_users)                       AS tombstones,
     (SELECT count(*) FROM consents WHERE revoked_at IS NULL)   AS consents,
     (SELECT count(*) FROM devices)                             AS devices,
     (SELECT count(*) FROM samples)                             AS samples,
     (SELECT count(*) FROM sleep_segments)                      AS sleep_segments,
     (SELECT count(*) FROM workouts)                            AS workouts,
     (SELECT count(*) FROM daily_scores)                        AS daily_scores,
     (SELECT count(*) FROM journal_entries)                     AS journal_entries,
     (SELECT count(*) FROM feedback)                            AS feedback,
     (SELECT count(*) FROM outbox WHERE published_at IS NULL)   AS outbox_pending;
   ```

   It passes when, comparing the copy with production:
   - `schema_version` is 36 on both. 36 is `0036_analytics_flag.sql`, main's newest migration file on
     2026-11-18; later, `ls backend/internal/db/migrations | tail -n 1` names it. `migrations` equals
     `schema_version`. If production is higher, a migration landed after the backup: the copy must then
     equal the newest file of the backup's day.
   - `service_roles` is 4 on both.
   - `tombstones` in the copy is at most production's. Tombstones are never deleted.
   - `users` in the copy is at most production's `users` plus production's `tombstones` minus the copy's
     `tombstones`; the same for `auth_users`. Sign-ups after the backup make production bigger; deletions
     make it smaller by exactly the new tombstones.
   - `samples`, `sleep_segments`, `workouts` and `daily_scores` are not 0 in the copy where production's
     aren't. Each is within about a day's growth of production's. An account deletion or a HealthKit
     deletion can make production's smaller.
   - `outbox_pending` is small on both: the outbox drains within minutes (**Dead letters and the outbox**).

   **Stop if** the query fails in the copy (a missing table: the backup lacks it), the copy's
   `schema_version` is below the backup day's, a data table is 0 in the copy but not in production, or a
   rule above fails. Keep the copy only as long as finding the cause takes, then step 5.
5. **Tear it down.** In the copy: Project Settings → General → **Delete project**, then type its name. The
   organisation's project list shows production only. The next day, Billing → Usage shows the copy's
   hours (cents). Note in STATUS.md the date, the backup's time, how long the restore took, and pass or
   stop. Don't note the counts: they are the beta's size.

   **Stop if** the delete fails, or the copy is still listed an hour later. Contact Supabase support the
   same day: the copy holds production's health data and bills by the hour.

Run 14a.7 again every month. For RYFT on GCP it replaces **Backups → Restore drill, monthly**, whose
compose stack and `make smoke-supabase` against the copy step 3 forbids.

**14a.8 Where `POST /v1/feedback` lands, and how you hear of it.**
- The report goes into the table `feedback` (migration 0021, `store.AddFeedback`, at most 10 a day per
  account). The api logs one line, `feedback received`. It carries `feedback_id`, `category` (bug, idea,
  data, other) and `request_ids` (a count), plus what every line has (`time`, `severity`, `message`,
  `service`) and what logx adds from the request (`request_id`, `client_request_id` when the app sent
  one, `user_id`, `route`, and the trace, `logging.googleapis.com/trace`). It never
  carries the message, the diagnostics or the device fields (**Data protection (logs)**; `writeLogKeys` in
  `backend/cmd/api/logs_test.go` pins it). The line stays in `ryft-logs` for 14 days.
- **How you hear of it: the sixth alert condition** (built, D244, your decision of 2026-11-19; applied
  and tested in 14a.9). On EKS, Grafana's NewFeedback alert on `ryft_feedback_received_total` told you
  (D83); D236 dropped it, because nothing reads the app's metrics on Cloud Run.
  - A third log-based counter, `ryft_notices` (`infra/gcp/monitoring.tf`), counts this line on api's
    stdout, and two admin lines with it (14a.9). Its one label is `kind`, the line's message, a closed set
    of three. No id becomes a label, and no category either.
  - The policy **RYFT · Feedback and admin access** (`alerts.tf`, warning, both channels) fires on any
    such line in the last 5 minutes, one incident per kind, and notifies the opening only. It closes by
    itself about 5 minutes after the last line, so the next feedback opens a new incident and notifies
    again. Its mail and push name the policy and the kind (`feedback received`), never the report, its
    category or the account.
  - The price (D244 → The price; Google's pricing page through search snippets, *(search)*): $0 until
    2027-09-01, then about $0.35–0.45 a month. D239's proposal's $1.50 was D236's reading A, which the
    page no longer shows.
  - No dashboard panel: one would change the dashboard (a free follow-up). Its logs panel shows ERROR
    lines only, and this line is INFO.
  - Rejected (D244): a log-match condition (its notification may carry the line's fields, the user id
    among them); a counter per line; PromQL over the sparse counter.
- **Where to look**, when it fires or at any time (the alert says only that one came):
  1. `/admin` → Feedback: the last 30 days, each with Resolve (**Beta → Admin sign-in**).
  2. The script, with the admin key from 1Password (item "RYFT ADMIN_API_KEY"), pasted at a hidden prompt:

     ```bash
     read -rs "?ADMIN_API_KEY: " ADMIN_API_KEY; echo; export ADMIN_API_KEY   # zsh: nothing on screen, in the history or on argv
     scripts/beta_report.sh feedback          # the last 7 days: id, when, account (8 characters), category, app, open, message
     scripts/beta_report.sh                   # one line per tester, "open fb" among the columns
     unset ADMIN_API_KEY
     ```
  3. Logs Explorer, `jsonPayload.message="feedback received"`: the count and the categories, no text.

**Stop if** a `feedback received` line in Logs Explorer shows a key that isn't listed above, such as a
message or an email. That is a bug in the api: report it. The counter reads only the line's message, but
the line itself is in `ryft-logs`.

**14a.9 The sixth condition: the plan, the apply, a test** *(D244)*. After the PR with `ryft_notices`
and the api's line `admin signed in` is merged, and `deploy.yml`'s run for that merge has shipped the api
with the line (without it the kind `admin signed in` never comes). What it counts, all on api's stdout:
`feedback received` (14a.8), `admin signed in` (once per operator session per instance: a token whose
TOTP is at most 10 minutes old, so a sign-in on `/admin` or its re-challenge after 12 hours), and `admin
auth rejected` but for `reason` `missing` (no bearer: crawlers) and `expired` (your own page with an old
token; it refreshes once). The key's own use and `admin second factor required` are not counted
(**Beta → Admin sign-in** → Alerts).

1. **The plan**, from main:

   ```bash
   git switch main && git pull
   gh run list --workflow=deploy.yml --branch=main --limit=1      # the merge's run: completed, success
   terraform -chdir=infra/gcp plan -out=notices.tfplan
   ```

   ```text
   # Step 14a.9's plan (alert_app_channel set): 2 to add, 0 to change, 0 to destroy.
   #   read: data.google_monitoring_notification_channel.email[0] (ryft-timkrebs)
   #   + google_logging_metric.notices               ryft_notices, the label kind                 = 1
   #   + google_monitoring_alert_policy.notices[0]   RYFT · Feedback and admin access (WARNING)   = 1 (2) to add
   #   Changes to Outputs: monitoring (policies + "RYFT · Feedback and admin access", log_metrics + "ryft_notices")
   # With alert_app_channel unset (the gate off): 1 to add, the metric alone. Under the stop line: the same.
   ```

   Read in the plan: the metric's `filter` names `resource.labels.service_name="api"`, the stdout log,
   the three messages and `NOT jsonPayload.reason="missing" AND NOT jsonPayload.reason="expired"`; its
   `label_extractors` is `kind = "EXTRACT(jsonPayload.message)"`. The policy's `notification_channels`
   is the email channel's name, then `<the app channel's id>` (D237); its `condition_threshold` is
   `ALIGN_SUM` over `300s`, `REDUCE_SUM` by `metric.label.kind`, `COMPARISON_GT` 0, `duration` `60s`,
   `evaluation_missing_data` `EVALUATION_MISSING_DATA_INACTIVE`; `alert_strategy` is `auto_close`
   `1800s` and `notification_prompts` `["OPENED"]`. No change to the five policies, the uptime checks,
   the dashboard, the other log-based metrics, the log bucket and sink, the edge, DNS, the invokers, the
   secrets, Pub/Sub, WIF or the deployer.

   **Stop if** the counts aren't your gate's line above; anything changes or is destroyed; any other
   policy is in the plan; the plan fails on the email channel (11.2's stop rule); or a 403 on
   `logging.*` or `monitoring.*` (11.2's roles; `logging.configWriter` from step 2d).
2. **The apply**:

   ```bash
   terraform -chdir=infra/gcp apply notices.tfplan
   rm infra/gcp/notices.tfplan
   terraform -chdir=infra/gcp output monitoring        # 6 policies, the new one among them; ryft_notices among log_metrics
   terraform -chdir=infra/gcp plan                     # No changes
   ```

   If the metric is created but the policy fails with `Cannot find metric(s) that match type =
   "logging.googleapis.com/user/ryft_notices"` (Monitoring hadn't seen the new metric yet; likely, since
   the pinned provider doesn't retry that error, and not yet seen here): wait two minutes, plan again with `-out=notices.tfplan` (1 to add, the policy alone), and
   apply that.
   **Stop if** the second plan isn't "No changes" (a field the API rewrites: report the diff, it is this
   PR's bug), or the apply fails otherwise.
3. **The test, each kind once.** In a new zsh, from the repository's root. Note the time. The password
   goes through a hidden prompt and `printf` (a builtin) into `jq`'s stdin, the token through `curl -H
   @-`: neither reaches the screen, the history or argv.

   ```bash
   # a. A feedback from the smoke user (as step 9 signs it in; the publishable key: Supabase ->
   #    Project Settings -> API Keys).
   REF=$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env); KEY='<the publishable key>'
   read -r "?smoke user's email: " SMOKE_EMAIL; export SMOKE_EMAIL; read -rs "?its password: " SMOKE_PASSWORD; echo
   TOKEN=$(printf '%s' "$SMOKE_PASSWORD" | jq -Rs '{email: env.SMOKE_EMAIL, password: .}' |
     curl -fsS "https://$REF.supabase.co/auth/v1/token?grant_type=password" -H "apikey: $KEY" \
       -H 'Content-Type: application/json' --data-binary @- | jq -r .access_token)
   unset SMOKE_PASSWORD SMOKE_EMAIL
   # Sends the feedback only after a sign-in; else it prints the failure and sends nothing.
   [ "${#TOKEN}" -gt 100 ] && printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS -o /dev/null -w '%{http_code}\n' -H @- \
     -H 'Content-Type: application/json' -d '{"message": "runbook 14a.9: a test of the alert, not a report", "category": "other"}' \
     https://api.ryftlab.de/v1/feedback || echo 'sign-in failed: stop here'   # 204
   unset TOKEN

   # b. An admin sign-in: https://ryftlab.de/admin in a new tab, the password, then the code. Keep the tab.

   # c. One wrong admin key (no secret: a made-up value):
   printf 'Authorization: Bearer %s\n' runbook-14a9-not-the-key | curl -sS -o /dev/null -w '%{http_code}\n' -H @- \
     https://api.ryftlab.de/v1/admin/flags              # 401, the line's reason not_a_token

   # The three lines (the time, the message, the reason; no id is printed):
   gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="api" AND (jsonPayload.message="feedback received" OR jsonPayload.message="admin signed in" OR jsonPayload.message="admin auth rejected")' \
     --project=ryftlab-prod --freshness=30m --limit=20 --format='value(timestamp,jsonPayload.message,jsonPayload.reason)'
   ```

   One line of each, the reject's reason `not_a_token`. Lines with reason `missing` or `expired` may be
   there too (crawlers, your page): they aren't counted.
4. **Expected:** per kind, within about 5 minutes of its line (the log-based metric's lag of a minute or
   two, the 60 s hold, the evaluation), one mail to `ryft-timkrebs` and one push in the Google Cloud app,
   each naming **RYFT · Feedback and admin access** and the kind, the subject `RYFT · <kind>`: three of
   each. Monitoring → Alerting → Incidents lists three, one per kind. Each closes by itself about 5
   minutes after its line, with no closing notice (only the opening is notified). No mail or push names
   an account, the report's text or a reason. Whether Google renders `${metric.label.kind}` in the
   subject isn't verified (D244): if a subject shows the variable as written, or nothing, the incident's
   page names the kind; note it in STATUS.md, it is no stop.
5. **Clean up:** in the admin tab, Feedback → the test report ("runbook 14a.9: …") → **Resolve**, then
   **Sign out**. Note in STATUS.md the date and, per kind, the minutes from the line to the mail and to
   the push.

**Stop if** a kind's mail or push hasn't come 15 minutes after its line. Look in this order:
- **Its line** (3's `gcloud logging read`). No `admin signed in`: the serving api lacks it (the merge's
  `deploy.yml` run), or the code was entered more than 10 minutes before the page's first call (sign in
  again, in a new tab). No line of any kind: a `LOG_LEVEL` above `info` in `deploy/run`.
- **Its point:** Monitoring → Metrics explorer → `logging/user/ryft_notices`, by `kind`. A line but no
  point: the metric's filter doesn't match the line. Report it, with the line's keys (not their values).
- **Its incident:** Monitoring → Alerting → **RYFT · Feedback and admin access**, the condition's chart.
  A point but no incident: report it.
- **Its delivery:** an incident but no mail or push: 11.4's stop rule (the channel).

### 15. Testers

Testers come after step 10's pages load and C2 is in force. C2 is a draft today, and its consent
round runs first. `/privacy` must show C2's version: `scripts/check_policy_in_force.sh` (C1)
reads the live page.

**Stop if** `/privacy` doesn't show C2's version.

### 16. The edge *(D242)*

A global external Application Load Balancer with Cloud Armor in front of api and ingest
(`infra/gcp/edge.tf`). The gate `edge` in `infra/gcp/gates.auto.tfvars` moves one phase per PR of
one line, and back the same way. Never set it as a workspace variable.

| Phase | The PR | What the apply does | One step back (16.8) |
| --- | --- | --- | --- |
| `off` | the D242 PR itself | nothing (16.0) | — |
| `built` | `edge = "built"` | the load balancer, Cloud Armor, the certificates, the sink's exclusions, the metrics and tiles; nothing points at it | `off` |
| `live` | `edge = "live"` | `api.` and `ingest.` become A records at its address | `built` |
| (deploy PR) | ingress and `XFF_TRUSTED_HOPS=2` in `deploy/run`, within the hour after `live` | no Terraform | revert it |
| `locked` | `edge = "locked"`, after 7 clean days | the two domain mappings destroyed | `live` (costly) |

`make cloudrun-check` (CI's `manifests` job, which a change of `gates.auto.tfvars` selects)
enforces the order (D242): the load balancer's ingress only while `edge` is `live` or `locked` and
only with `XFF_TRUSTED_HOPS` above 1, and `locked` only with that ingress on both. A step out of
order is a red PR, not a broken production. But two PRs green on their own can be wrong together
(a `live` → `built` PR opened before the deploy PR merged, say), and no apply runs the check: before
every plan below, right after `git pull`, run `make cloudrun-check`. **Stop if** it is red.
From `built` on the stop line is **€45**, the budget's 90 % mail (the user's decision, D242;
**The €20 stop line** → With the edge).
The address is never written into any file; below it is `<ip>`, and in a shell `$IP`.

Run from the repository root on an up-to-date main, in bash or zsh (zsh: `setopt
interactivecomments` first), logged in to gcloud as the project owner and to `ryft-lab`.

**16.0 Once, after the D242 PR's merge.** The two record sets of `api.` and `ingest.` are now
`google_dns_record_set.host` (moved from `.mapping`):

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=edge-off.tfplan   # Plan: 0 to add, 0 to change, 0 to destroy; the 2 "has moved to" lines
terraform -chdir=infra/gcp apply edge-off.tfplan
rm infra/gcp/edge-off.tfplan
terraform -chdir=infra/gcp plan                        # No changes
```

The two moves: `google_dns_record_set.mapping["api"]` → `.host["api"]`, the same for `ingest`.
**Stop if** anything is added, changed or destroyed, or anything else moves.

**16.1 The roles, once** (before the `built` PR's plan). The apply account gets three predefined
roles, the narrowest that cover the load balancer, the policy and the certificates; the plan
account's `roles/viewer` should cover every read:

```bash
P=ryftlab-prod; APPLY_SA=hcp-terraform-apply@$P.iam.gserviceaccount.com
EDGE_ROLES=(roles/compute.loadBalancerAdmin roles/compute.securityAdmin roles/certificatemanager.editor)
for r in "${EDGE_ROLES[@]}"; do
  gcloud projects add-iam-policy-binding "$P" --member="serviceAccount:$APPLY_SA" --role="$r" --condition=None
done
gcloud projects get-iam-policy "$P" --flatten='bindings[].members' --filter="bindings.members:$APPLY_SA" \
  --format='value(bindings.role)'                   # the nineteen roles of step 2d, never roles/owner

# What the apply calls, as far as known here: every line "ok".
NEED=(compute.globalAddresses.create compute.globalAddresses.use
  compute.regionNetworkEndpointGroups.create compute.regionNetworkEndpointGroups.use
  compute.backendServices.create compute.backendServices.use compute.backendServices.setSecurityPolicy
  compute.securityPolicies.create compute.securityPolicies.use compute.urlMaps.create compute.urlMaps.use
  compute.sslPolicies.create compute.sslPolicies.use compute.targetHttpsProxies.create compute.targetHttpsProxies.use
  compute.targetHttpProxies.create compute.targetHttpProxies.use compute.globalForwardingRules.create
  compute.globalOperations.get compute.regionOperations.get
  certificatemanager.dnsauthorizations.create certificatemanager.dnsauthorizations.use
  certificatemanager.certs.create certificatemanager.certs.use certificatemanager.certmaps.create
  certificatemanager.certmaps.use certificatemanager.certmapentries.create certificatemanager.operations.get)
HAVE=$(for r in "${EDGE_ROLES[@]}"; do gcloud iam roles describe "$r" --format='value(includedPermissions)' | tr ';' '\n'; done)
for p in "${NEED[@]}"; do grep -qx "$p" <<<"$HAVE" && echo "ok $p" || echo "MISSING $p"; done

# The plan account's reads: every line "ok".
VIEW=$(gcloud iam roles describe roles/viewer --format='value(includedPermissions)' | tr ';' '\n')
for p in compute.globalAddresses.get compute.regionNetworkEndpointGroups.get compute.backendServices.get \
         compute.securityPolicies.get compute.urlMaps.get compute.sslPolicies.get compute.targetHttpsProxies.get \
         compute.targetHttpProxies.get compute.globalForwardingRules.get certificatemanager.dnsauthorizations.get \
         certificatemanager.certs.get certificatemanager.certmaps.get certificatemanager.certmapentries.get; do
  grep -qx "$p" <<<"$VIEW" && echo "ok $p" || echo "MISSING $p"
done
unset HAVE VIEW
```

Not verified here: that these permission names are exactly Google's, and that the provider needs no
other (`roles/certificatemanager.editor` holding `certmaps.use` and `dnsauthorizations.create` is
from search results). The apply's first 403 would name what is missing.
**Stop if** a line says `MISSING`: look the name up in the three roles (`gcloud iam roles describe
<role>`); if none holds it, don't add a broader role or `roles/editor`, report it.

**16.2 `built`.** A PR with `edge = "built"` in `infra/gcp/gates.auto.tfvars`, one line, CI green
(`make cloudrun-check` passes: the ingress is still `all`). Merging it runs `deploy.yml`, which
redeploys from unchanged code. Then:

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=edge-built.tfplan
```

```text
# Step 16.2's plan (edge = built): 26 to add, 2 to change, 0 to destroy.
#   2 APIs on the keep list (certificatemanager, compute)                                    =  2
#   + 1 global address (IPv4) + 2 serverless NEGs + 2 backend services                        =  5  (7)
#   + 1 Cloud Armor policy (8 rules) + 1 SSL policy + 1 URL map + 1 HTTPS proxy               =  4 (11)
#   + 2 DNS authorizations + their 2 CNAME record sets + 2 certificates + 1 map + 2 entries   =  9 (20)
#   + 1 forwarding rule (443) + 1 URL map (http) + 1 HTTP proxy + 1 forwarding rule (80)      =  4 (24)
#   + 2 log-based metrics (ryft_edge_denials, ryft_edge_preview_matches)                      =  2 (26)  to add
#   ~ the sink ryft-logs: + 2 exclusions (lb-requests-unflagged, run-requests)                =  1
#   ~ the dashboard RYFT · Overview: + 4 tiles                                                 =  1  (2)  to change
```

In the plan, by eye: the policy's rate rule (`rate_based_ban`, priority 2000, `preview = false`,
`enforce_on_key = "IP"`, 300 per 10 s, `deny(429)`, ban 6000 per 300 s for 600 s); six WAF rules,
`preview = true`; `log_level = "NORMAL"`. No change to `api.` and `ingest.`'s records, the mappings,
the invokers, the uptime checks, the alert policies, the secrets, Pub/Sub, WIF, the deployer or the
bucket. Outputs: `edge_ip` comes (known after apply).
**Stop if** the counts differ; anything is destroyed; anything else changes; or a 403 (16.1).

```bash
terraform -chdir=infra/gcp apply edge-built.tfplan
rm infra/gcp/edge-built.tfplan
terraform -chdir=infra/gcp plan                     # No changes
```

**Stop if** the second plan isn't "No changes" (a resource that differs at every plan is a bug of
the D242 PR: report it, change nothing by hand).

**16.3 Compute Engine's side effects, right after the apply.** Enabling the Compute Engine API on a
project without an organisation makes its default service account with **Editor**, and usually the
network `default` with four firewall rules (D242). Nothing uses either:

```bash
P=ryftlab-prod; PN=$(gcloud projects describe "$P" --format='value(projectNumber)')
CSA="$PN-compute@developer.gserviceaccount.com"
gcloud iam service-accounts list --project="$P" --filter="email=$CSA" --format='value(email,disabled)'     # it, False
gcloud projects get-iam-policy "$P" --flatten='bindings[].members' --filter="bindings.members:serviceAccount:$CSA" \
  --format='value(bindings.role)'                                                                            # roles/editor
gcloud projects remove-iam-policy-binding "$P" --member="serviceAccount:$CSA" --role=roles/editor --condition=None
gcloud iam service-accounts disable "$CSA" --project="$P"
gcloud compute firewall-rules list --project="$P" --filter='network~/networks/default$' --format='value(name)'
for f in $(gcloud compute firewall-rules list --project="$P" --filter='network~/networks/default$' --format='value(name)'); do
  gcloud compute firewall-rules delete "$f" --project="$P" --quiet
done
gcloud compute networks delete default --project="$P" --quiet   # "not found" is fine: there was none
gcloud compute networks list --project="$P"                     # Listed 0 items.
gcloud iam service-accounts list --project="$P" --filter="email=$CSA" --format='value(email,disabled)'     # it, True
gcloud projects get-iam-policy "$P" --format='table(bindings.role,bindings.members)'   # step 1's view
```

Step 1's view now names the default account in no binding. New service agents of the two APIs
(`service-<number>@compute-system.iam.gserviceaccount.com` with `roles/compute.serviceAgent`, and
possibly one of Certificate Manager's) are Google's: leave them.
**Stop if** the Editor binding can't be removed; any other new member appears; or `networks list`
still lists a network (find what made it before deleting anything else).

**16.4 `built`'s checks** (nothing points at the load balancer yet, so `--resolve` sends each
request to it by hand). First the certificates:

```bash
for h in api ingest; do
  gcloud certificate-manager certificates describe "ryft-edge-$h" --project=ryftlab-prod --format='value(managed.state)'
done                                                # ACTIVE, ACTIVE (PROVISIONING until then)
```

**Stop if** either isn't `ACTIVE` 24 hours after the apply (`--format=yaml(managed)` names the
issue; the authorization's CNAME `_acme-challenge.<host>` is in the zone, `dig +short CNAME
_acme-challenge.api.ryftlab.de @1.1.1.1`). Then, once both are ACTIVE:

```bash
IP=$(terraform -chdir=infra/gcp output -raw edge_ip)
for h in api ingest; do
  curl -sS -o /dev/null -w "%{http_code} $h\n" --resolve "$h.ryftlab.de:443:$IP" "https://$h.ryftlab.de/health/ready"   # 200
  curl -sS -o /dev/null -w "%{http_code} %{redirect_url}\n" --resolve "$h.ryftlab.de:80:$IP" "http://$h.ryftlab.de/health/ready"
                                                    # 301 https://<host>:443/health/ready (Google writes the port: the same URL)
  curl -sS -o /dev/null --tlsv1.2 --tls-max 1.2 -w "%{http_code} TLS 1.2 $h\n" --resolve "$h.ryftlab.de:443:$IP" "https://$h.ryftlab.de/health/ready"   # 200
  curl -sS -o /dev/null --tlsv1.0 --tls-max 1.1 --resolve "$h.ryftlab.de:443:$IP" "https://$h.ryftlab.de/health/ready" \
    && echo "TLS 1.1 ACCEPTED $h" || echo "TLS 1.1 refused $h"                                                       # refused
done
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' --resolve "api.ryftlab.de:443:$IP" \
  -H 'Host: unknown.example' "https://api.ryftlab.de/anything"          # 301 https://ryftlab.de/ (no backend)
```

The last one sends api's name for TLS and another host to the URL map. Which certificate a
handshake without a known name gets isn't verified, and doesn't matter: no backend answers it.

Then two of step 9's tests through the load balancer:

```bash
# 1. The body limit: 33 MiB must get OUR JSON 413; 31 MiB must not get Google's (HTML) 413. Step 9's TOKEN
#    (the smoke user's, read the same way; it never reaches argv).
for mib in 33 31; do
  f=$(mktemp); { printf '{"pad":"'; head -c $((mib << 20)) /dev/zero | tr '\0' a; printf '"}'; } > "$f"
  printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS -H @- -H 'Content-Type: application/json' \
    --resolve "ingest.ryftlab.de:443:$IP" --data-binary @"$f" -w "\n%{http_code} %{content_type} ${mib} MiB\n" \
    https://ingest.ryftlab.de/v1/sync | tail -c 300
  rm -f "$f"
done                                                # 33: "request body over 33554432 bytes", 413 application/json
                                                    # 31: our JSON answer (not a 413, not text/html)
unset TOKEN

# 2. Three forged X-Forwarded-For entries, through the load balancer, hops still 1 (step 9's loop and query).
for i in 1 2 3; do
  for h in api ingest; do
    curl -sS -o /dev/null -w "%{http_code} $h\n" --resolve "$h.ryftlab.de:443:$IP" \
      -H 'X-Forwarded-For: 203.0.113.7, 203.0.113.8, 203.0.113.9' "https://$h.ryftlab.de/health/ready"
  done
  [ "$i" = 3 ] || sleep 61
done
gcloud logging read 'resource.type="cloud_run_revision" AND jsonPayload.message="client address" AND jsonPayload.xff_entries>=3' \
  --project=ryftlab-prod --freshness=10m --limit=10 \
  --format='value(resource.labels.service_name,jsonPayload.picked,jsonPayload.xff_index,jsonPayload.xff_entries)'
```

- **1. Pass:** 33 MiB is ours (JSON, the message above); 31 MiB is anything of ours (a JSON error is
  fine), never Google's HTML 413. A Google 413 means the hop to Cloud Run is HTTP/1 with its 32 MiB
  cap: **stop** before `live` (a contract change, as step 9's stop rule).
- **2. Pass:** on both services `picked` `xff`, `xff_index` `4`, `xff_entries` `5`: the load balancer
  appends exactly `<client>,<lb-ip>`, and with hops 1 the key is its own address. That is the shape
  `XFF_TRUSTED_HOPS=2` (the deploy PR) expects. If a service's line is missing, run the query again
  after a minute: log entries arrive with a delay. **Stop** on any other shape (report the three
  numbers; no address is in the line).

Then Cloud Armor and the logs (each `gcloud logging read` prints the priority and outcome only,
never `httpRequest`):

```bash
curl -sS -o /dev/null -w '%{http_code}\n' --resolve "api.ryftlab.de:443:$IP" \
  "https://api.ryftlab.de/health/ready?q=1%27%20OR%20%271%27=%271"      # 200: the WAF is in preview
curl -sS -o /dev/null -w '%{http_code}\n' --resolve "api.ryftlab.de:443:$IP" "https://api.ryftlab.de/health/ready"   # 200
sleep 180
L=(--project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=10m)
gcloud logging read 'resource.type="http_load_balancer" AND jsonPayload.previewSecurityPolicy.outcome="DENY"' "${L[@]}" --limit=10 \
  --format='value(timestamp,jsonPayload.previewSecurityPolicy.priority,jsonPayload.previewSecurityPolicy.outcome)'
                                                    # one line: 1000 (sqli) DENY
gcloud logging read 'resource.type="http_load_balancer" AND NOT jsonPayload.previewSecurityPolicy.outcome="DENY" AND NOT jsonPayload.enforcedSecurityPolicy.outcome="DENY"' \
  "${L[@]}" --limit=100 --format='value(timestamp)' | wc -l             # 0: the plain request is not kept
gcloud logging read 'logName="projects/ryftlab-prod/logs/run.googleapis.com%2Frequests" AND resource.labels.service_name=("api" OR "ingest") AND httpRequest.status<500' \
  "${L[@]}" --limit=100 --format='value(timestamp)' | wc -l             # 0: api's and ingest's request log lands only for a 5xx

# The one enforced rule: 400 requests in a few seconds, past its 300 per 10 s (far below the ban's 6,000 per 300 s).
seq 400 | xargs -P 40 -I{} curl -s -o /dev/null -w '%{http_code}\n' --resolve "api.ryftlab.de:443:$IP" \
  "https://api.ryftlab.de/v1/me" | sort | uniq -c                       # 401s, then 429s (the service's and Cloud Armor's)
sleep 180
gcloud logging read 'resource.type="http_load_balancer" AND jsonPayload.enforcedSecurityPolicy.priority=2000 AND jsonPayload.enforcedSecurityPolicy.outcome="DENY"' \
  "${L[@]}" --limit=1000 --format='value(timestamp)' | wc -l             # more than 0: Cloud Armor's own 429s
unset L
```

The kept entries (the preview match, Cloud Armor's 429s) hold your address, the URL and your user
agent for 14 days: that is what D242 keeps, and only for flagged or denied requests (and, in Cloud
Run's request log, for a 5xx of api or ingest). Cloud Armor
counts approximately, so its 429s are roughly the requests past 300, not exactly 100.
**Stop if** the probe isn't 200 (a WAF rule isn't in preview: check the plan's `preview`); no
preview line appears after 10 minutes; either `wc -l` of the plain request or of Cloud Run's request
log isn't 0 (an exclusion isn't working: every request's IP is being kept; go back to `off`, 16.8,
and report it); or the burst's count is 0 (the rate rule doesn't act).

**16.5 `live`.** Only after every 16.4 check passed. A PR with `edge = "live"`, one line, CI green.
Have the deploy PR (16.6) open before you merge this one: it follows within the hour, but not
before 10 minutes after the apply (twice the TTL), so no resolver still hands out the CNAME to the
mappings, which the new ingress refuses. Its CI goes
green only once `live` is on main (merge main into it then): `make cloudrun-check` refuses the load
balancer's ingress while `edge` is `built`.

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=edge-live.tfplan
```

```text
# Step 16.5's plan (edge = live): 0 to add, 2 to change, 0 to destroy.
#   ~ google_dns_record_set.host["api"]     type "CNAME" -> "A", rrdatas ["ghs.googlehosted.com."] -> ["<ip>"]
#   ~ google_dns_record_set.host["ingest"]  the same                                                  = 2 to change
```

`type` isn't ForceNew in the provider: it writes the change as one Cloud DNS change that deletes the
CNAME and adds the A, with no moment in between. No change to the mappings or anything else.
**Stop if** the plan shows the two as `-/+` (2 to add, 2 to destroy): a delete, then a create, leaves
a gap that resolvers cache as "no such name" for up to the zone's negative TTL. Report it; don't
apply. **Stop** too if anything else changes, or the A's address isn't `terraform -chdir=infra/gcp
output -raw edge_ip`.

```bash
terraform -chdir=infra/gcp apply edge-live.tfplan
rm infra/gcp/edge-live.tfplan
IP=$(terraform -chdir=infra/gcp output -raw edge_ip)
for h in api ingest; do
  [ "$(dig +short A "$h.ryftlab.de" @1.1.1.1)" = "$IP" ] && echo "$h: the edge" || echo "$h: NOT YET"
done                                                                    # both "the edge" within 5 minutes (TTL 300)
dig +short CNAME api.ryftlab.de @1.1.1.1                                # nothing
```

Then the uptime checks `ryft-api-ready` and `ryft-ingest-ready` green (Monitoring → Uptime checks,
within 10 minutes), `make smoke-prod`, and the app (sign in, a sync). From now on, until the deploy
PR, every request through the load balancer shares one per-address bucket per instance (D242):
**merge the deploy PR** as soon as these pass and 10 minutes have gone by since the apply.
**Stop if** a check is red after 15 minutes or the smoke fails: go back to `built` (16.8), which
points DNS at the mappings again with their certificates still valid.

**16.6 The deploy PR, 10 minutes to an hour after `live`'s apply.** In `deploy/run/api.yaml` and `ingest.yaml`
`run.googleapis.com/ingress: internal-and-cloud-load-balancing`, and in `deploy/run/config.env`
`XFF_TRUSTED_HOPS=2`, in one PR (`make cloudrun-check` allows them only while `edge` is `live` or
`locked`). Its merge deploys them. Then:

```bash
PN=$(gcloud projects describe ryftlab-prod --format='value(projectNumber)')
for s in api ingest; do
  curl -sS -o /dev/null -w "%{http_code} $s run.app\n" "https://$s-$PN.europe-west1.run.app/health/ready"   # not 200: Google refuses it
  curl -sS -o /dev/null -w "%{http_code} $s.ryftlab.de\n" "https://$s.ryftlab.de/health/ready"            # 200, through the edge
done
gcloud logging read 'resource.type="cloud_run_revision" AND jsonPayload.message="tick" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest")' \
  --project=ryftlab-prod --freshness=20m --limit=10 --format='value(timestamp,resource.labels.service_name,jsonPayload.result)'
                                                    # the next tick (every 15 minutes): api ok, ingest ok
```

Then step 9's forged-entry loop and query again, without `--resolve` (DNS points at the edge now):
`picked` `xff`, `xff_index` `3`, `xff_entries` `5` on both, the client's own address is the key
(a service's line missing: the query again after a minute, as in 16.4).
The run's smoke must have passed (`gh run view <run id> -R timkrebs/ryft-platform`). The tick is a
Pub/Sub push to `run.app`; Pub/Sub in the same project counts as internal for this ingress
*(search: docs.cloud.google.com/run/docs/securing/ingress)*, which the tick line proves.
**Stop if** `run.app` still answers 200 (the ingress didn't deploy); no `tick` line with `result`
ok from api or ingest within 20 minutes (Pub/Sub's push is refused: revert the deploy PR at once,
then report); `xff_index` isn't 3; or the smoke failed. The way back is reverting this PR, which
sets both lines back together (16.8).

**16.7 `locked`, after 7 clean days.** Clean: no stop above, no rollback, the uptime checks green,
no 5xx alert, the dashboard's Cloud Armor tiles without denials of real users (429s at priority
2000 for the app's own traffic), no tester locked out. A PR with `edge = "locked"`, one line
(`make cloudrun-check` refuses it unless api and ingest both have the load balancer's ingress).
On the same day, read the WAF rules' preview matches on real traffic, per rule (**Enforcing a WAF
rule**, below). A match there doesn't hold up `locked`. It keeps that rule in preview.

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=edge-locked.tfplan
```

```text
# Step 16.7's plan (edge = locked): 0 to add, 0 to change, 2 to destroy.
#   - google_cloud_run_domain_mapping.host["api"], ["ingest"]                                 = 2 to destroy
```

**Stop if** anything else is destroyed or changes (the A records stay as they are).

```bash
terraform -chdir=infra/gcp apply edge-locked.tfplan
rm infra/gcp/edge-locked.tfplan
terraform -chdir=infra/gcp plan                     # No changes, and no domain_mappings_ready warning
```

Then the uptime checks green and `make smoke-prod`.

**16.8 The way back, one step at a time** (each a PR of one line, its plan, the apply):
- **`locked` → `live`:** 2 to add (the mappings), 0 to change, 0 to destroy. Their certificates
  can't issue while DNS points at the load balancer, so `domain_mappings_ready` warns. Going on to
  `built` points DNS at ghs again, and only then are the certificates issued (15 minutes to 24
  hours): `api.` and `ingest.` fail TLS for that long. This is the one costly step, which is why
  `locked` waits 7 clean days. Take it only if the edge itself is the problem.
- **The deploy PR, reverted** (before `live` → `built`, which `make cloudrun-check` refuses while
  the ingress is the load balancer's; and only after `locked` → `live`, since at `locked` the revert is
  red): ingress `all` and `XFF_TRUSTED_HOPS=1` again. No Terraform. `locked` → `live` costs nothing
  until you go on to `built`.
- **`live` → `built`:** 0 to add, 2 to change (A back to CNAME, `ghs.googlehosted.com.`), 0 to
  destroy (a `-/+` is a stop, as in 16.5). Clients follow within 5 minutes. Clean only while the
  mappings' certificates still hold: in `live` they can't renew.
- **`built` → `off`:** 0 to add, 2 to change (the sink loses its exclusions, the dashboard its
  tiles), 26 to destroy (16.2's 26; the two APIs only leave the state and stay on). Nothing stateful:
  the address is released, and a later `built` gets a new one. Cloud Run's request log is kept again
  in full. 2a's `grep … || KEEP+=` line follows the committed gate by itself.

**Enforcing a WAF rule** (one set at a time, from `live` on, in priority order: 1000 first). After 7
days of real traffic with no preview match at its priority **on a request the backend answered
below 400**. Scanners probe a public host every day, and their probes get 401 (no token) or 404, so
"no match at all" would never come. The app's real requests carry a token and succeed: a match
among those is a false positive that enforcing would turn into a `403` for a tester.

The read covers the last 7 days and all six rules at once. It prints a count and a priority per rule,
never an entry or `httpRequest`. Run it no earlier than 7 days after `live`'s apply: until then the
window still holds requests from before `live`, among them 16.4's probe (a match at 1000 that got a `200`).

```bash
L=(--project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=7d --limit=100000)
# 1. Real traffic: preview matches on requests the backend answered below 400, per priority.
gcloud logging read 'resource.type="http_load_balancer" AND jsonPayload.previewSecurityPolicy.outcome="DENY" AND httpRequest.status<400' \
  "${L[@]}" --format='value(jsonPayload.previewSecurityPolicy.priority)' | sort | uniq -c
                                                    # "<count> <priority>" per rule with a match; a priority not listed: 0
# 2. For context only: every preview match, scanners' 401s and 404s included, per priority.
gcloud logging read 'resource.type="http_load_balancer" AND jsonPayload.previewSecurityPolicy.outcome="DENY"' \
  "${L[@]}" --format='value(jsonPayload.previewSecurityPolicy.priority)' | sort | uniq -c
unset L
```

The priorities: 1000 `sqli`, 1001 `xss`, 1002 `lfi`, 1003 `rce`, 1004 `scannerdetection`, 1005
`protocolattack`. The rule next in order may be enforced once read 1 doesn't list its priority. The
dashboard's "Cloud Armor: denials and preview matches" tile counts all of them, scanners included, so
it is no substitute for read 1. If read 1 lists the rule, it stays in preview for another 7 days.

A PR sets that set's line in `local.edge_waf_preview` (`infra/gcp/edge.tf`) to `false`, and its pin in
`tests/edge.tftest.hcl` (`the_policy`) with it. Its plan: 0 to add, 1 to change (the policy, in
place: that rule's `preview` true → false), 0 to destroy. The way back is the same line back to
`true`. From then on a match is a `403` and a `ryft_edge_denials` point at that priority.

**Before 1000 (`sqli`) or 1001 (`xss`) is enforced:** App Attest's two body routes skip both sets by
path first, in a PR of its own (17.6). Their base64 bodies can read as SQL or as an event handler to
libinjection, and a `403` there means no app token.

### 17. App Attest *(D245, D247)*

The server checks an app token (`X-RYFT-App-Token`) on api and ingest. The modes are `APP_ATTEST=off`, then
`warn`, then `on` in `deploy/run/config.env`, one PR each. The token ring is the Secret Manager secret
`app-attest-token-key`, rendered as `APP_ATTEST_TOKEN_KEYS` into both services at a pinned version. It holds
1 to 3 lines `<kid>:<base64 of 32 bytes>`: the first line signs (api), and every line verifies (api and
ingest). The server PR ships `off` with no pin. So production answers `501` on the three routes, and the
app sends no header.

| Step | Can run | Changes |
| --- | --- | --- |
| 17.1 | the server PR merged | the ring's container and accessor (Terraform) |
| 17.2 | 17.1, and C2's privacy text naming App Attest in force | the ring's first version (gcloud), pinned nowhere yet |
| 17.3 | 17.2, the attesting build on TestFlight, Apple's root checked | `warn` and the ring's pin (a deploy PR) |
| 17.4 | 7 days after 17.3's deploy | nothing (the reads) |
| 17.5 | 17.4's stop rule held, and 17.6 applied if 1000 or 1001 is enforced | `on` (a deploy PR of one line) |
| 17.6 | before the PR that enforces 1000 or 1001 | the edge: two WAF sets skip the two body routes (Terraform) |

Run from the repository root on an up-to-date main, in bash or zsh (zsh: `setopt
interactivecomments` first), logged in to gcloud as the project owner and to `ryft-lab`. No value of
the ring is ever printed, pasted or written to a file.

**17.1 The container and its accessor** (after the server PR's merge). Before the merge, a plan on main
must say **No changes**. Anything it shows belongs to another step (11.6's second PR, 11.8, 16.7): apply
that first.

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=app-attest.tfplan
```

```text
# Step 17.1's plan: 2 to add, 0 to change, 0 to destroy.
#   + google_secret_manager_secret.this["app-attest-token-key"]                 (europe-west1, 30-day destroy delay)
#   + google_secret_manager_secret_iam_binding.accessor["app-attest-token-key"] (members: ryft-api, ryft-ingest)
```

**Stop if** anything else is added, changed or destroyed, a "has moved to" line appears (#206's moves
were recorded by the applies since: 16.0, `built`, `live`), or the binding names any account but
`ryft-api` and `ryft-ingest`. No change to the other eleven secrets or their bindings, to `otlp-token`
(retired), to the edge, to the services' invokers or to the zone.

```bash
terraform -chdir=infra/gcp apply app-attest.tfplan
rm infra/gcp/app-attest.tfplan
terraform -chdir=infra/gcp plan                     # No changes
gcloud secrets get-iam-policy app-attest-token-key --project=ryftlab-prod --format='value(bindings.members)'
                                                    # serviceAccount:ryft-api@…;serviceAccount:ryft-ingest@…
```

Cost: one active version, one replica, about $0.06 a month, and a few access operations at each instance's
start (Secret Manager's list prices, *unverified*). With 2 or 3 versions enabled during a rotation, up to
$0.18. Disable the old version once the rotation is done.

**17.2 The ring's first version** (only once C2's privacy text names App Attest and is in force: D245 →
Privacy). The value is made and piped in one go: `openssl rand -base64 32` writes the key straight into
the pipe, so it is never a shell word, never in argv, on the screen or in the history; only the `kid` (no
secret) is expanded. Run it with tracing off (`set +x`; zsh: `unsetopt xtrace`). The guard adds it only if
the secret exists and has no version yet:

```bash
set +x
if gcloud secrets describe app-attest-token-key --project ryftlab-prod >/dev/null 2>&1 && [ -z "$(gcloud secrets versions list app-attest-token-key --project ryftlab-prod --format='value(name)' --limit=1)" ]; then { printf '%s:' "$(openssl rand -hex 4)"; openssl rand -base64 32; } | gcloud secrets versions add app-attest-token-key --project ryftlab-prod --data-file=- && echo "OK: version added"; else echo "STOP: the secret is missing or already has a version"; fi
                                                    # Created version [1] of the secret [app-attest-token-key].
                                                    # OK: version added
```

The line's shape is `<8 hex>:<44 base64>` plus a newline. `openssl rand -hex 4` prints 8 lower-case hex
digits (the `kid`). `openssl rand -base64 32` prints 44 characters, 32 bytes with one `=`, on one line.
The services accept one trailing newline. Read back the version number and the shape, never the value:

```bash
gcloud secrets versions list app-attest-token-key --project=ryftlab-prod --format='value(name.basename(),state)'
                                                    # 1 ENABLED   (the <N> for 17.3)
gcloud secrets versions access 1 --secret=app-attest-token-key --project=ryftlab-prod \
  | grep -cE '^[0-9a-f]{8}:[A-Za-z0-9+/]{43}=$'     # 1  (one line of the right shape)
gcloud secrets versions access 1 --secret=app-attest-token-key --project=ryftlab-prod \
  | cut -d: -f2 | base64 -d | wc -c                 # 32
```

Each `access` is a Data Access audit entry (Secret Manager's `DATA_READ`, `project.tf`), which is expected.
**Stop if** the guard says STOP (look with `gcloud secrets versions list`, never `access`), the count isn't
`1`, or the length isn't `32`. A wrong version is fixed by a new one: `gcloud secrets versions disable 1
--secret=app-attest-token-key --project=ryftlab-prod`, then add the new version by hand with the same
pipe (the guard refuses because a version exists), and use its number. Nothing pins the ring yet, so
nothing reads it, and the routes still answer `501`.

**17.3 The `warn` flip: a deploy PR.** It changes exactly three files (`make cloudrun-check` refuses
`warn` unless both services pin the ring, at the same version):
- `deploy/run/config.env`: `APP_ATTEST=off` becomes `APP_ATTEST=warn`, and `APP_ATTEST_EXEMPT_USERS=` gets
  the smoke user's id (the beta gate's step 3 shell: `as_smoke "$API/v1/me" | jq -r .id`; a test
  account's id names nobody and is no secret, D245);
- `deploy/run/api.yaml` and `deploy/run/ingest.yaml`: the row
  `- { name: APP_ATTEST_TOKEN_KEYS, valueFrom: { secretKeyRef: { name: app-attest-token-key, key: "<N>" } } }`
  at 17.2's version, by number (the comment in `api.yaml` shows where it goes).

Preconditions (D245's stop rules for `off` → `warn`). Flip only when all of these hold:
1. **C2's privacy text names App Attest and is in force.** In the beta gate's step 3 shell (`as_smoke`,
   `$API`), `as_smoke "$API/v1/config" | jq -r .privacy_policy_version` names the version that went live
   with it (`PRIVACY_POLICY_VERSION`, set in the same PR as the text).
2. **The ring is set:** 17.2's three checks.
3. **The attesting build is on TestFlight**, and it is the build your own device runs.
4. **Apple's root is the one the server pins.** The download's fingerprint equals both the embedded PEM
   and the pinned constant:

   ```bash
   curl -s https://www.apple.com/certificateauthority/Apple_App_Attestation_Root_CA.pem | openssl x509 -noout -fingerprint -sha256
                                                    # sha256 Fingerprint=1C:B9:82:3B:A2:8B:A6:AD:2D:33:A0:06:94:1D:E2:AE:4F:51:3E:F1:D4:E8:31:B9:F7:E0:FA:7B:62:42:C9:32
   openssl x509 -in backend/internal/appattest/apple_root.pem -noout -fingerprint -sha256   # the same line
   grep -o 'appleRootSHA256 = "[0-9a-f]*"' backend/internal/appattest/root_test.go         # the same, lower case, no colons
   ```

5. **Your own device shows accepted tokens.** The pin comes with this PR (D247's decision 1), so the
   device can attest only from this deploy on. That makes this the first check after the merge (below).
   `warn` refuses nothing, so checking afterwards is safe. If you want it before the flip, pin the ring
   under `off` first in a PR of its own (D247 allows it) and check the same.

**Stop if** any of 1 to 4 fails.

```bash
git fetch origin && git switch -c deploy/app-attest-warn origin/main
# config.env: APP_ATTEST=warn and APP_ATTEST_EXEMPT_USERS=<smoke user id>; api.yaml, ingest.yaml: the ring's row at "<N>"
make cloudrun-check                                  # refuses warn without the ring pinned in both
bash scripts/tests/check_cloudrun_test.sh
git diff --stat                                      # 3 files: config.env, api.yaml, ingest.yaml
git commit -am "deploy: APP_ATTEST warn, the ring at version <N>"
git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
```

Its merge is a normal deploy (**Deploy, roll back, rotate**). api and ingest roll with `warn` and the
ring, and from the first request a new revision serves, the three routes answer and tokens flow.
A revision that can't read the ring doesn't start. Cloud Run then keeps the previous one, and
`deploy.yml` goes red at the rollout. It worked when (in the beta gate's step 3 shell):

```bash
for s in api ingest; do
  echo "$s $(gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod --format=json \
    | jq -r '[.spec.template.spec.containers[0].env[] | select(.name | startswith("APP_ATTEST")) | "\(.name)=\(.value // .valueFrom.secretKeyRef.key)"] | join(" ")')"
done                                                 # api APP_ATTEST=warn APP_ATTEST_EXEMPT_USERS=<id> APP_ATTEST_TOKEN_KEYS=<N>, ingest the same
as_smoke "$API/v1/config" | jq -r .app_attest      # warn
as_smoke -X POST "$API/v1/app-attest/challenge" -o /dev/null -w '%{http_code}\n'   # 200 (was 501); the challenge expires unused
```

Then open the attesting build on your device, use it for a minute, and read the last hour (counts only,
no ids). `route` is the pattern with its method, as the beta gate's access line shows it:

```bash
L=(--project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1h --limit=100000)
S='resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout"'
gcloud logging read "$S AND jsonPayload.message=\"http\" AND jsonPayload.route:\"/v1/app-attest/\"" "${L[@]}" \
  --format='value(jsonPayload.route,jsonPayload.status)' | sort | uniq -c
                                                     # POST /v1/app-attest/keys 201 (once per install), …/token 200
gcloud logging read "$S AND jsonPayload.message=\"app attest\" AND jsonPayload.code=\"app_attest_required\"" "${L[@]}" \
  --format='value(jsonPayload.app_build,jsonPayload.reason)' | sort | uniq -c
                                                     # no line with your build's number
unset L S
```

**Stop if** the challenge isn't `200`, your device's `keys` or `token` isn't `201`/`200`, or your build's
number has warn lines. Then go back with the line to `off` (a PR; `warn` refused nothing, so nobody noticed)
and find out why (`422 app_attest_invalid` on `keys` with a real device is the case D245 left unverified).

**17.4 The 7-day check** (no earlier than 7 days after 17.3's deploy). The warn line (`message` `app
attest`, one per request without a valid token) has `code`, `reason` (`missing`, `invalid`, `expired`,
`other_account`), `mode`, `service`, `route_group`, `method`, `app_build` (the `X-RYFT-App-Build` header,
or empty), `app_device` (whether `X-RYFT-Device-ID` was sent: every app build sends it, scripts don't) and
`request_id`. It carries no account, key id or token. In Cloud Logging, the services' slog `msg` is
`jsonPayload.message` (`internal/logx`).

```bash
L=(--project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=7d --limit=100000)
Q='resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout" AND jsonPayload.message="app attest"'
# 1. The stop rule: requests from app builds without a token, by build ("<count> <build>"; an empty build: before AA1).
gcloud logging read "$Q AND jsonPayload.reason=\"missing\" AND jsonPayload.app_device=true" "${L[@]}" \
  --format='value(jsonPayload.app_build)' | sort | uniq -c
# 2. Everything else, by reason, device header and route group.
gcloud logging read "$Q" "${L[@]}" --format='value(jsonPayload.reason,jsonPayload.app_device,jsonPayload.route_group)' | sort | uniq -c
# 3. The edge, for 17.6: Cloud Armor's preview matches on the three routes, per priority.
gcloud logging read 'resource.type="http_load_balancer" AND jsonPayload.previewSecurityPolicy.outcome="DENY" AND httpRequest.requestUrl:"/v1/app-attest/"' \
  "${L[@]}" --format='value(jsonPayload.previewSecurityPolicy.priority)' | sort | uniq -c
unset L Q
```

The stop rule before `on`. Flip only when all of these hold:
1. **Read 1 shows no line from a build at or after `<the first App Attest build>`** for 7 days in a row.
   A line there is a request the app sent without a token, which is an app bug.
2. **Read 1 shows no other line either.** A line from an older build, or with an empty build, is a tester
   on a build before App Attest. Under `on`, that build loses its queued Train writes (D245, API.md →
   Old builds). Get them onto the attesting build (TestFlight's list), then 7 more days.
3. **Read 2 shows only known non-app clients:** `missing false` (the Simulator, your own scripts).
   `invalid`, `expired` or `other_account` with `true` is a bug to find first.
4. **Every tester is on the attesting build** (TestFlight → the build → its testers).
5. **`make smoke-prod` is green** (the beta gate's step 3 shell). The smoke user is exempt, so it passes under `on` too.

**Stop if** any of these fails: stay on `warn`, which refuses nothing. Read 3 is input for 17.6, not
for the flip.

**17.5 The `on` flip: a PR of one line.**

```bash
git fetch origin && git switch -c deploy/app-attest-on origin/main
# deploy/run/config.env: APP_ATTEST=warn becomes APP_ATTEST=on (that line only)
make cloudrun-check                                  # refuses off -> on against the base; warn -> on passes
git diff --stat                                      # deploy/run/config.env | 2 +-
git commit -am "deploy: APP_ATTEST on"
git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
```

Before its merge: if 1000 (`sqli`) or 1001 (`xss`) is already enforced, or will be before this merge, 17.6
must be applied first. An edge `403` on `POST /v1/app-attest/token` under `on` means no token, and then
every request is refused.

Its merge is a normal deploy. From the first request a new revision serves, a request without a valid
token gets `403 app_attest_required` outside the exempt routes and users. Ingest rolls before api. It
worked when (in the beta gate's step 3 shell):

```bash
# the env loop of 17.3: api APP_ATTEST=on …, ingest APP_ATTEST=on …
as_smoke "$API/v1/config" | jq -r .app_attest      # on
make smoke-prod                                      # green: the smoke user is exempt
gcloud logging read 'resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout" AND jsonPayload.message="app attest" AND jsonPayload.mode="on"' \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1h --limit=100000 \
  --format='value(jsonPayload.reason,jsonPayload.app_device,jsonPayload.route_group)' | sort | uniq -c
                                                     # nothing with true, or as few as read 2 showed
```

Then the app on your device works as before, and the Simulator gets `403 app_attest_required` outside the
exempt routes.

**Rollback:** a PR of one line, `APP_ATTEST=warn` (or `git revert` of the flip). Its merge serves every
request again from the first one the new revisions take. Faster, while that PR waits: **A rollback → By
traffic** for api and ingest (the hold first; their previous revisions run `warn`), then merge the PR and
release the hold. Leave the ring pinned: `warn` needs it.

**Stop if** after the flip `make smoke-prod` fails, any tester on the attesting build is refused, or the
1-hour read shows `true` lines that read 2 didn't: roll back, then find out why.

**17.6 The edge: App Attest's bodies skip `sqli` and `xss`** (a PR of its own, before the PR that
enforces 1000 or 1001; `docs/work/2026-11-20-app-attest-server/edge.md` has the analysis and the exact
diff). In `infra/gcp/edge.tf`, a map `local.edge_waf_skip_paths` gives `sqli-v33-stable` and
`xss-v33-stable` the path `^/v1/app-attest/(keys|token)$`, and their rules' expressions gain
`&& !request.path.matches('…')`. The four other sets are unchanged. `tests/edge.tftest.hcl`
(`the_policy`) pins both expressions. Ideally the PR comes after a few days of `warn`, with 17.4's read 3
in its description. If sqli's 7 days are clean before `warn` has shipped, it comes first, without that
read.

```bash
git switch main && git pull
terraform -chdir=infra/gcp plan -out=edge-app-attest.tfplan
```

```text
# Step 17.6's plan: 0 to add, 1 to change, 0 to destroy.
#   ~ google_compute_security_policy.edge[0]   the rules at 1000 and 1001: match.expr.expression gains the path clause
#                                              (rule is a set: each shows as one removed and one added inside the ~)
```

**Stop if** anything else changes, or any rule but 1000 and 1001 shows a diff (priority, action and
`preview` stay unchanged).

```bash
terraform -chdir=infra/gcp apply edge-app-attest.tfplan
rm infra/gcp/edge-app-attest.tfplan
terraform -chdir=infra/gcp plan                      # No changes
```

The way back is the PR reverted (0/1/0). The rules no longer evaluate the two paths, so their preview
matches there stop being logged. Any other spelling of the path is inspected as before.

### Deploy, roll back, rotate

`deploy.yml` owns the revisions. It runs on every push to main and by hand (`workflow_dispatch`,
main only), and only while the repository variable `GCP_DEPLOY` is `on`. One run, in order:

1. `wait for ci-ok` waits for `ci.yml`'s own run on main for this commit and its `ci-ok`. A
   check of that name from any other workflow doesn't count. It polls every 30 s, for up to 95
   minutes. Red, or still missing then: the run fails and nothing is deployed. Once it is green,
   the run checks that its commit is still main's head. If it isn't, the run ends green as
   "superseded", and the newer run deploys.
2. `config.env` lints and every template renders. A `FILL_ME_IN` left stops the run here.
3. The five images, each one `linux/amd64` manifest (no provenance, no SBOM), built with the
   runner's own Docker and scanned by Trivy from a tarball (critical, with a fix; no `docker.sock`).
   Nothing has signed in yet.
4. Only when all five pass: the sign-in as `ryft-deployer` (WIF admits only this workflow, on main,
   in the environment `deploy`, on a GitHub-hosted runner), then the push of the same five images,
   tagged with the short SHA.
5. `deploy/run` rendered with their digests.
6. Main's head again, just before `migrate`: a commit overtaken during the build ends green as
   "superseded" here, its images pushed and nothing rolled. Then `migrate`: `gcloud run jobs
   replace`, then `execute --wait`. If it fails, the run stops and no service rolls.
7. `gcloud run services replace` for scoring, notify, ingest, api, in that order. Each waits until
   its new revision is ready. The startup probe is `/health/live` (`/health/ready` stays the
   uptime checks' target), so ready means the process serves, not that it reaches the database.
8. `make smoke-prod`. It is what catches a revision with a broken database config. Only a manual
   run with `smoke` off skips it.

Every run builds and deploys all five, whatever the merge changed, and runs `migrate` (a no-op
when no file is new). The run summary lists the tag, the digests and each service's new revision.
The site's jobs run next to these (**The site**, below). `ci.yml` only checks: since C1 it builds no
image to deploy and opens no pull request.
One deploy job runs at a time, and a newer run never cancels it. GitHub keeps one run waiting, the
newest. A run in between shows as cancelled. Nobody re-runs an old deploy run: a manual
`gh workflow run deploy.yml --ref main` deploys main's head, and that is the retry everywhere
below.

Every command here is yours. Values go through stdin, never argv.

**The first deploy (step 8).** First the four prerequisites:

```bash
R=timkrebs/ryft-platform
git fetch origin
# 1. The config PR (step 7a) is merged: no placeholder on main.
git show origin/main:deploy/run/config.env | grep -c FILL_ME_IN          # 0
# 2. Every version deploy/run pins exists and is enabled (step 5's 1, or a pin a PR moved).
git grep -ho 'name: [a-z0-9-]*, key: "[0-9]*"' origin/main -- deploy/run/ | tr -d ',"' | sort -u |
  while read -r k s k v; do
    echo "$s $v $(gcloud secrets versions describe "$v" --secret="$s" --project=ryftlab-prod --format='value(state)')"
  done                                                                    # 11 lines, each ENABLED
# 3. The environment deploy admits main only.
gh api "repos/$R/environments/deploy" --jq '.deployment_branch_policy'   # protected_branches false, custom_branch_policies true
gh api "repos/$R/environments/deploy/deployment-branch-policies" --jq '.branch_policies[] | [.name,.type]'   # ["main","branch"], nothing else
# 4. The smoke user's three secrets in that environment. Copy each value, then run its line:
pbpaste | gh secret set SUPABASE_ANON_KEY --env deploy -R "$R"   # Supabase -> Project Settings -> API Keys -> the publishable key
pbpaste | gh secret set SMOKE_EMAIL --env deploy -R "$R"         # the smoke user's address (1Password)
pbpaste | gh secret set SMOKE_PASSWORD --env deploy -R "$R"      # its password (1Password)
pbcopy < /dev/null
gh secret list --env deploy -R "$R"                              # the three names
```

"Allow administrators to bypass" stays unchecked (Settings → Environments → `deploy`). If the
environment is missing, create it as step 10a.1 creates `site`, with `deploy` in its place.

Then switch the workflow on and run it by hand, without the smoke. Nothing is public before step 9.

```bash
gh variable set GCP_DEPLOY --body on -R "$R"
gh variable list -R "$R"                                         # GCP_DEPLOY on, no DEPLOY_HOLD_SERVICES
gh workflow run deploy.yml --ref main -f smoke=false -R "$R"
gh run list --workflow=deploy.yml -R "$R" --limit=3              # the new run on top, workflow_dispatch
gh run watch <run id> -R "$R" --exit-status
```

It worked when the run is green, with `make smoke-prod` skipped, and `migrate` and the four
services look like this:

```bash
EXEC=$(gcloud run jobs executions list --job=migrate --region=europe-west1 --project=ryftlab-prod \
  --sort-by=~metadata.creationTimestamp --limit=1 --format='value(metadata.name)')
gcloud run jobs executions describe "$EXEC" --region=europe-west1 --project=ryftlab-prod \
  --format='yaml(status.succeededCount,status.failedCount,status.completionTime)'   # succeededCount: 1, no failedCount
gcloud logging read "resource.type=\"cloud_run_job\" AND resource.labels.job_name=\"migrate\" AND labels.\"run.googleapis.com/execution_name\"=\"$EXEC\"" \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --order=asc --limit=200 \
  --format='value(timestamp,severity,jsonPayload.message,jsonPayload.file,jsonPayload.role,jsonPayload.err)'
for s in scoring notify ingest api; do
  gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod \
    --format='value(metadata.name,status.latestCreatedRevisionName,status.latestReadyRevisionName)'
done                                         # each line: the same revision twice, the one the run summary lists
```

`migrate`'s log, in order (`jsonPayload.message` is the message field of A's logger, design §6;
the strings are `backend/cmd/migrate` and `internal/db`'s):
- `migration applied` with its `file`, once per migration Supabase hasn't seen yet (A's among them).
- `built-ins seeded`, with the counts, on every run.
- `role password set` for `ryft_api`, `ryft_ingest`, `ryft_notify` and `ryft_scoring`, in that
  order: step 5's values. A later deploy logs `role password unchanged` for each role whose pin
  didn't move. A warning `no password given; role left unchanged` means a `DB_PASSWORD_<SVC>` is
  missing: stop.
- `schema current`, last: `migrate` is done. With no new file there is no `migration applied`
  line, so `schema current` is the only schema line ("nothing to do").

A revision that isn't ready says why here:

```bash
REV=<latestCreatedRevisionName from above>
gcloud run revisions describe "$REV" --region=europe-west1 --project=ryftlab-prod --format='yaml(status.conditions)'
gcloud logging read "resource.type=\"cloud_run_revision\" AND resource.labels.revision_name=\"$REV\" AND severity>=WARNING" \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --order=asc --limit=50 \
  --format='value(timestamp,severity,jsonPayload.message,jsonPayload.err,textPayload)'
```

**Stop if** the run fails before the push (a placeholder or a wrong shape: a config PR; the
sign-in after the scan: the WIF condition's IDs, the ref or the environment); Trivy finds a critical (nothing was
pushed: fix forward, never skip the scan); `migrate`'s execution failed or its log lacks `schema
current`; or a revision isn't ready. A failed `migrate` rolled nothing, and Cloud Run didn't
retry it (`maxRetries: 0`: a retry is a second failed owner login, which feeds Supavisor's circuit
breaker). Read its log and fix the cause. Then run
`gh workflow run deploy.yml --ref main -f smoke=false -R "$R"`, once (never a re-run of the red
run). If `ECIRCUITBREAKER` appears anywhere, wait it out (rotation step 4 below).

Until step 9's apply and certificates are done, a push to main deploys and then fails at the
smoke. Step 9's `serve = true` merge is such a push. Set the hold (below) before you merge it.
Delete the hold once apply 2's mappings are Ready, then run `deploy.yml` by hand with the smoke on
(step 9 has the commands). Merge nothing else in between.

**A normal deploy** (from step 9 on). A merge to main is the deploy, in the order above. Watch it:

```bash
R=timkrebs/ryft-platform
gh run list --workflow=deploy.yml -R "$R" --limit=5
gh run watch <run id> -R "$R" --exit-status
gh run view <run id> -R "$R" --log-failed                # after a red run: the failed step
git fetch origin && git rev-parse --short=7 origin/main  # the newest deploying run's "Images (<tag>)"
```

Then run step 8's services loop (four ready revisions, the summary's), and watch "RYFT · 5xx over
2 %", "RYFT · Uptime check failing" and "RYFT · Event backlog over 15 minutes" (Monitoring →
Alerting; the dashboard RYFT · Overview) for 30 minutes. What each outcome leaves:
- Green as "superseded": nothing rolled (its images may be pushed). No failure: the newer run,
  for main's head, deploys.
- `ci-ok` red or late: nothing deployed. Red is fixed by a new commit, which deploys itself.
  Late: once ci is green on main's head, `gh workflow run deploy.yml --ref main -R "$R"`.
- `migrate` failed: nothing rolled. Read its log as in step 8.
- A revision isn't ready: Cloud Run keeps routing that service to its last ready revision
  (`latestRevision` means the latest *ready* one). The services before it run the new code, the
  ones after it the old. Fix forward or revert.
- The smoke is red: all four rolled, and every new revision was ready. With the startup probe on
  `/health/live`, a revision whose database config is broken is ready and serving: the smoke is
  what catches it. Run `make smoke-prod` once yourself (step 9's environment). Red again: roll
  back.

Merges close together are safe. A run deploys only while its commit is main's head (checked
when `ci-ok` is found and again before `migrate`), so an older commit never deploys over a newer
one. The runs in between end green as superseded, and the newest ships every merge at once.

**Stop if** the smoke fails twice (roll back); once every run has ended, the newest deploying
run's tag isn't main's tip (then `gh workflow run deploy.yml --ref main -R "$R"`); or a run failed
and you don't know why. Don't re-run to find out.

**A rollback.** There are two ways. Moving traffic takes seconds and builds nothing. A revert
ships the old code through a normal deploy. Migrations are additive and stay either way: the older
code runs on the newer schema (**Lockouts and rolling back across releases**, no down-migrations).

By traffic:
1. Set the hold first (below), and let a running deploy end. Every `deploy/run/<svc>.yaml` sends
   all traffic to the latest revision, so without the hold the next push to main (any merge) or
   run by hand deploys and undoes the rollback.
2. Find the previous ready revision. The summary of the run that deployed before the bad one
   names it. Or:

   ```bash
   S=api                                                 # the service to roll back
   gcloud run services describe "$S" --region=europe-west1 --project=ryftlab-prod \
     --format='value(status.latestReadyRevisionName)'    # the bad one
   gcloud run revisions list --service="$S" --region=europe-west1 --project=ryftlab-prod \
     --sort-by=~metadata.creationTimestamp --limit=5     # newest first; a check mark is ready, ACTIVE yes has traffic
   REV=<the ready revision before the bad one>
   gcloud run revisions describe "$REV" --region=europe-west1 --project=ryftlab-prod \
     --format='yaml(status.conditions,spec.containers[0].env)'   # Ready True; the secret versions it reads
   ```

3. Move the traffic, and check it:

   ```bash
   gcloud run services update-traffic "$S" --to-revisions="$REV=100" --region=europe-west1 --project=ryftlab-prod
   gcloud run services describe "$S" --region=europe-west1 --project=ryftlab-prod --format='yaml(status.traffic)'   # percent: 100 on $REV
   ```

   For more than one service, go api, ingest, notify, scoring: the deploy's order reversed.
4. Run `make smoke-prod` from your shell (step 9's environment).
5. Merge the fix or the revert. With the hold set, nothing deploys. Delete the hold and run
   `deploy.yml` by hand. That deploy ends the traffic pin.

By revert:

```bash
git fetch origin && git switch -c fix/revert-<sha> origin/main
git revert --no-edit <sha>                               # main's commits are squash merges: one parent
git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
```

Its merge deploys the old code to all four services and ends any traffic pin. With a hold set,
delete it, then run `gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform`. A reverted
migration stays applied (`schema_migrations` keeps its number). Re-land it unchanged, or fix it in
a new migration.

**Stop if** `$REV` isn't Ready; a version it pins is disabled (enable it, or revert instead);
`$REV`'s `PGPASSWORD` version differs from the latest revision's (a rotation lies between: revert
instead, because the role has the new password, `$REV` logs in with the old one, and the breaker
trips for the whole role); or the smoke still fails on `$REV` (the cause isn't the code: the
database, Supabase, Pub/Sub).

**The hold.** It replaces D175's hold on EKS:

```bash
gh variable set DEPLOY_HOLD_SERVICES --body '#<issue>' -R timkrebs/ryft-platform   # the reason: an issue number, nothing else
gh variable list -R timkrebs/ryft-platform                                         # DEPLOY_HOLD_SERVICES #<issue>
```

While it is set, no run builds or deploys a service, by push or by hand. `wait for ci-ok` runs as
always, and a red `ci-ok` still fails the run. `held` is green, with the notice
"DEPLOY_HOLD_SERVICES is set (#<issue>): no service was built or deployed (the site has its own
switch, SITE_DEPLOY)" and "Deploy held" in the summary. `deploy` is skipped. The site isn't held: its jobs run as always
(**The site**, below), with one exception while the hold is set. `site` refuses a deploy that moves
`PRIVACY_POLICY_VERSION`, and any deploy while the live release's commit is unknown; a run by hand
is refused too. To stop the site too, set `SITE_DEPLOY` off. The reason
shows in every run, so keep it to the issue number. Merges pile up on main. Nothing tracks the
hold's age (the old deploy watch is gone, C1), so write the release into the issue. To release it:

```bash
gh variable delete DEPLOY_HOLD_SERVICES -R timkrebs/ryft-platform
gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform     # smoke on: main's tip, every held merge at once
```

Deleting the variable deploys nothing by itself.

**Stop if** `gh variable list` doesn't show the variable as you set it. A misspelt or empty one
holds nothing.

**A database password rotation** (design §4). `migrate` sets every role's password from its
`DB_PASSWORD_<SVC>` on every run, so both pins move in one commit.

1. Add version N+1. Nothing reads it yet.

   ```bash
   S=api                                                 # api, ingest, scoring or notify
   gcloud secrets versions list "db-password-$S" --project=ryftlab-prod --format='value(name.basename(),state)'   # N ENABLED on top
   openssl rand -base64 32 | tr -d '\n' | gcloud secrets versions add "db-password-$S" --project=ryftlab-prod --data-file=-
   #                                                       Created version [N+1] of the secret [db-password-<svc>].
   ```

2. One PR moves both pins:

   ```bash
   git fetch origin && git switch -c "chore/rotate-db-password-$S" origin/main
   scripts/rotate_db_password.sh "$S" <N+1>
   gcloud secrets versions describe <N+1> --secret="db-password-$S" --project=ryftlab-prod --format='value(state)'   # ENABLED
   make cloudrun-check
   git diff --stat                                       # deploy/run/$S.yaml and deploy/run/migrate.yaml, one line each
   git commit -am "deploy: db-password-$S at version <N+1>"
   git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
   ```

   CI's `manifests` job runs `make cloudrun-check`, which fails when the two pins differ.
3. Merge outside the testers' hours. The deploy's `migrate` sets `ryft_<svc>` to N+1, then the
   service rolls onto it. In between, its old instances keep their open connections but can't open
   new ones. scoring rolls first and api last, so api's gap spans three rollouts. Check:

   ```bash
   gcloud logging read 'resource.type="cloud_run_job" AND resource.labels.job_name="migrate" AND jsonPayload.message:"role password"' \
     --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1h \
     --format='value(timestamp,jsonPayload.message,jsonPayload.role)'   # set for ryft_<svc>, unchanged for the other three
   gcloud run services describe "$S" --region=europe-west1 --project=ryftlab-prod --format=json \
     | jq -r '.spec.template.spec.containers[0].env[] | select(.name == "PGPASSWORD") | .valueFrom.secretKeyRef.key'   # N+1
   ```

4. If `ECIRCUITBREAKER` appears, wait at least two minutes. Supavisor's breaker trips for the
   whole role. Never re-run the deploy, `migrate` or a login in a loop.

   ```bash
   gcloud logging read "resource.type=\"cloud_run_revision\" AND resource.labels.service_name=\"$S\" AND \"ECIRCUITBREAKER\"" \
     --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=30m --limit=5 \
     --format='value(timestamp,resource.labels.revision_name)'
   ```

   Then find the login that fails (a revision still pinned at N gets traffic: `yaml(status.traffic)`),
   and check Supabase → Database → Settings → Network Bans.
5. After a day, with all traffic on the latest revision, disable N:

   ```bash
   gcloud run services describe "$S" --region=europe-west1 --project=ryftlab-prod --format='yaml(status.traffic)'   # latestRevision: true, percent: 100
   gcloud secrets versions disable <N> --secret="db-password-$S" --project=ryftlab-prod
   gcloud secrets versions list "db-password-$S" --project=ryftlab-prod --format='value(name.basename(),state)'   # N+1 ENABLED, N DISABLED
   ```

To undo a rotation, run `gcloud secrets versions enable <N> --secret="db-password-$S"
--project=ryftlab-prod`, then `scripts/rotate_db_password.sh "$S" <N>`, and open the same kind of
PR. Never move traffic back across a rotation.

**Stop if** `make cloudrun-check` fails; the diff holds more than the two lines; `migrate` doesn't
log `role password set` for `ryft_<svc>`; or `ECIRCUITBREAKER` appears (step 4).

**Another secret's pin.** The same move, without `migrate`. For example, `admin-api-key`:

```bash
# A new value from 1Password's generator (item "RYFT ADMIN_API_KEY"), copied:
pbpaste | gcloud secrets versions add admin-api-key --project=ryftlab-prod --data-file=-   # Created version [N+1]
pbcopy < /dev/null
git fetch origin && git switch -c chore/rotate-admin-api-key origin/main
git grep -n 'name: admin-api-key,' -- deploy/run/        # every pin: api.yaml only; set its key "N" to "N+1" by hand
make cloudrun-check
git commit -am "deploy: admin-api-key at version <N+1>"
git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
```

The merge's deploy rolls api onto N+1. `migrate` doesn't read it. The old key works until api's
new revision serves. After a day, disable N as in step 5.
- `supabase-secret-key` moves the same way (api only). `otlp-token` is pinned nowhere since D236
  (step 11.6 removes it): never rotate it.
- A new `.p8` comes with a new key ID. Change `APPLE_KEY_ID` or `APNS_KEY_ID` in `config.env` in
  the same commit.
- `db-password-owner` is `migrate`'s own login, pinned by hand in `migrate.yaml`. Set the hold,
  reset the password in Supabase (Database → Settings), add the version, merge the pin, then delete
  the hold and run `deploy.yml` by hand. After a day, disable the old version as in step 5.
- Never rotate `data-key`. A new value leaves every sealed Apple token undecryptable (D211).

**Stop if** `git grep` shows a pin you didn't move, or `make cloudrun-check` fails.

**PostHog's project key (D231), once.** `GET /v1/config` serves the app's analytics target from
the api's `POSTHOG_PROJECT_KEY`; without it, `analytics` is `null` and the app sends nothing. The
secret `posthog-project-key` (api only) comes with `infra/gcp`. Its pin in `api.yaml` follows its
first version, never the other way round: a revision that reads a missing version doesn't start, and
the deploy goes red at api. In this order:

1. The container and its accessor:

   ```bash
   terraform -chdir=infra/gcp plan -out=posthog.tfplan
   # Plan: 2 to add, 0 to change, 0 to destroy:
   #   google_secret_manager_secret.this["posthog-project-key"]
   #   google_secret_manager_secret_iam_binding.accessor["posthog-project-key"]   (members: ryft-api only)
   terraform -chdir=infra/gcp apply posthog.tfplan
   rm infra/gcp/posthog.tfplan
   ```

2. The value: PostHog (EU Cloud) → Project settings → **Project API key** (`phc_…`), copied. Never a
   personal API key (`phx_…`): the api refuses to start with one. In the same settings, check that
   **Discard client IP data** is on (no IP, D231).

   ```bash
   put() { read -rs "?Value for $1: " V; echo; printf '%s' "$V" | gcloud secrets versions add "$1" --project=ryftlab-prod --data-file=-; unset V; }
   put posthog-project-key                                   # Created version [1] of the secret [posthog-project-key]
   pbcopy < /dev/null                                        # clear the clipboard you pasted from
   gcloud secrets versions list posthog-project-key --project=ryftlab-prod --format='value(name.basename(),state)'   # 1 ENABLED
   ```

3. The pin, a PR of one line. If `put` printed another number than 1, pin that one.

   ```bash
   git fetch origin && git switch -c deploy/posthog-pin origin/main
   # deploy/run/api.yaml: right after the PUSH_SA_ID line, with its indentation, add
   #   - { name: POSTHOG_PROJECT_KEY, valueFrom: { secretKeyRef: { name: posthog-project-key, key: "1" } } }
   make cloudrun-check
   bash scripts/tests/check_cloudrun_test.sh                 # CI's smoke job runs it on a deploy/run change
   git commit -am "deploy: posthog-project-key at version 1"
   git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
   ```

   Its merge's deploy is the first that reads the key. Then:

   ```bash
   gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="api" AND jsonPayload.message="analytics"' \
     --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1h --limit=1 \
     --format='value(timestamp,jsonPayload.configured)'      # True
   # The service's template pins the version, so every new revision gets it (names only, never the
   # value; after a rollback the serving revision can be an older one, which /v1/config below shows):
   gcloud run services describe api --region=europe-west1 --project=ryftlab-prod --format=json \
     | jq -c '.spec.template.spec.containers[0].env[] | select(.name == "POSTHOG_PROJECT_KEY") | .valueFrom.secretKeyRef'   # {"key":"1","name":"posthog-project-key"}
   # GET /v1/config as the smoke user. It prints the shape of `analytics`: the host, the key's
   # prefix and its length, never the key. The password and the token go through stdin.
   read -r "?Smoke user's email: " SMOKE_EMAIL; read -rs "?Its password: " SMOKE_PASSWORD; echo
   read -r "?Supabase anon key (Project settings -> API): " SUPABASE_ANON_KEY
   TOKEN=$(E="$SMOKE_EMAIL" P="$SMOKE_PASSWORD" jq -n '{email: env.E, password: env.P}' \
     | curl -fsS -X POST "https://$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env).supabase.co/auth/v1/token?grant_type=password" \
         -H "apikey: $SUPABASE_ANON_KEY" -H 'Content-Type: application/json' --data-binary @- | jq -r .access_token)
   printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -fsS -H @- https://api.ryftlab.de/v1/config \
     | jq -c '.analytics | if . == null then "null" else {host, key: (.project_key | .[0:4] + "… " + (length | tostring) + " chars")} end'
   #   {"host":"https://eu.i.posthog.com","key":"phc_… <n> chars"}; "null" means the revision has no key
   unset SMOKE_EMAIL SMOKE_PASSWORD SUPABASE_ANON_KEY TOKEN
   ```

A new key moves as **Another secret's pin** above (api only). To turn analytics off at once, use the
flag (Feature flags): the app stops at its next config, within six hours, at its next launch sooner.
Removing the pin turns `analytics` to `null` with the next deploy.

**Stop if** the plan holds anything else; the key you copied starts with `phx_` or `phs_`; PostHog's
**Discard client IP data** is off (the only thing that keeps the IP out, D231); or the api's new
revision doesn't become ready (the old one keeps serving; `gcloud run revisions list --service=api
--region=europe-west1 --project=ryftlab-prod`).

**Deploys off.**

```bash
gh variable set GCP_DEPLOY --body off -R timkrebs/ryft-platform    # anything but on
gh variable list -R timkrebs/ryft-platform
```

Every job's `if` is then false. A push run shows its jobs skipped and builds nothing. Cloud Run
keeps serving the last revisions: this is no stop (for that, see **The €20 stop line**). A run that
had already started may still deploy. Cancel it only while it is in `wait for ci-ok`
(`gh run cancel <run id> -R timkrebs/ryft-platform`). Once its `deploy` job runs, let it end:
`migrate` and the rollouts go on in Cloud Run either way. Switched back `on`, nothing deploys until
a merge or a run by hand. For a pause, use the hold instead: its runs say why.

**Stop if** a push run after the switch has a `deploy` job that ran.

**The site** (design §8, C1). `deploy.yml` also ships `web/` to Firebase Hosting in the project
`ryftlab-site`. The repository variable `SITE_DEPLOY` switches it, separately from the services'
hold; runbook step 10 sets it to `on`. Its jobs, in every run:
- `site-off`: when `SITE_DEPLOY` isn't `on`. A notice ("SITE_DEPLOY is not on") and "Site off" in
  the summary. Nothing is built or deployed, and the run stays green.
- `site-build`: `npm ci`, `npm run build` (the export in `web/out`, `web/firebase.json`, the brand
  check), then `SUPPORT_EMAIL` on `/beta`. It has no token and no environment. It hands `web/out`
  and `web/firebase.json` on as the artifact `site`, kept for a day. Without `SUPPORT_EMAIL` it
  stops: `gh variable set SUPPORT_EMAIL`, then run `deploy.yml` by hand.
- `site`: environment `site` (main only, no secrets). It checks main's tip first, as `deploy` does:
  superseded, it ends green and deploys nothing. Then it signs in as `site-deployer@ryftlab-site`
  (WIF, the same pool as `ryft-deployer` but the `site` principal set, which only `site-deployer`
  binds; D233). It deploys when `web/` differs from the commit of the live release (the
  release's message is `web <sha>`), on every run by hand, and when the live release can't be read
  or names no commit of main's history. Otherwise "Site unchanged: the live site is `<sha>`".
  - **The hold:** while `DEPLOY_HOLD_SERVICES` is set, `site` refuses ("site held", red) a deploy
    that moves `PRIVACY_POLICY_VERSION` (`deploy/run/config.env` at the live release's commit
    against main's). It also refuses every deploy while that commit is unknown, a run by hand
    included. Otherwise `/privacy` could show a version the held api doesn't ask for. Lift the hold,
    then `gh workflow run deploy.yml --ref main`: both ship.
  - **The deploy:** `firebase deploy --only hosting --message "web <sha>"` with the firebase-tools
    of `web/.firebase-tools-version`. It runs from `site-build`'s artifact, unpacked beside the
    checkout. The artifact must hold `firebase.json` and `out/` and nothing else, with no links.
  - **The live check:** `web/scripts/check-hosting.mjs` against `https://ryftlab.de` and
    `https://www.ryftlab.de` with `PRIVACY_POLICY_VERSION` from `deploy/run/config.env`. It runs
    `web-e2e`'s assertions with production's expectations (`web-e2e` passes `--emulator`; the two
    differ on the admin redirects and on `//`, web D21), on every run from main's tip, deployed or
    unchanged.

A run by hand always deploys the site, except while the services are held (**The hold**, above). Use
one after `SUPPORT_EMAIL` or `TESTFLIGHT_URL` changed,
because those aren't in `web/`. It also builds and deploys the five services from main's tip.
`ci.yml` doesn't check the site again for a variable, so start a manual ci run before it:
`gh workflow run ci.yml --ref main -R timkrebs/ryft-platform`. That run executes every check.
Start `deploy.yml` only once that run is green. A red one doesn't stop later pushes from skipping the web
jobs.

**What CI checks on main (D243).** A push to main runs every check of `ci.yml`, with one
exception: `web-checks` and `web-e2e` run only when the web paths changed since main's last green
commit. That's the newest commit whose `ci` run, from a push or by hand, is green. The web paths
are `ci.yml`'s `web` filter: `web/**`, `docs/SCORING.md`, `ci.yml`, `.githooks/**`,
`scripts/check_bytes.sh` and `scripts/lib/charpolicy.py`. `manifests` checks main's commit message
against the brand denylist on every push.
- Not since the parent. A red web commit followed by a non-web commit would otherwise ship the red
  `web/` through `site`.
- When the web jobs are skipped, the `changes` job's step "The web paths since main's last green
  commit" shows the notice **web checks skipped**, with the base commit and its run. Both web jobs
  show as skipped, and `ci-ok` is green.
- They run anyway when the lookup or the compare fails, when no run is green, when the base isn't
  an ancestor, or when 300 or more files changed. The step's log says why.
- When `web-e2e` fails on a web commit (a Lighthouse flake), re-run the failed jobs:
  `gh run rerun <run id> --failed -R timkrebs/ryft-platform`. Until a run is green, every later
  push to main runs the web jobs again, because the web paths still differ from the last green
  commit.

```bash
gh variable set SITE_DEPLOY --body on -R timkrebs/ryft-platform     # runbook step 10
gh variable set SITE_DEPLOY --body off -R timkrebs/ryft-platform    # anything but on: only site-off runs
# The live release (you own ryftlab-site; the token goes through stdin):
{ printf 'Authorization: Bearer '; gcloud auth print-access-token; } | curl -fsS -H @- \
  https://firebasehosting.googleapis.com/v1beta1/sites/ryftlab-site/channels/live | jq -r '.release.message, .release.releaseTime'   # web <sha>, when
```

What each outcome leaves:
- `firebase deploy` failed: the run is red, and the live release is still the old one. Hosting
  releases a version only once all of it is uploaded. Read the step's log. A missing permission
  gets only that role (runbook step 10). Then `gh workflow run deploy.yml --ref main`.
- The live check is red: the **new** release is live. Look at the step's findings. If the site is
  broken, roll back.

**A site rollback.** The console's release history takes seconds and builds nothing:
1. `gh variable set SITE_DEPLOY --body off`. With the switch on, the next run compares main's
   `web/` with whatever the rolled-back release's message names, or finds none, and deploys main's
   `web/` over the rollback *(the message a console rollback carries is unchecked)*.
2. Firebase console → `ryftlab-site` → Hosting → Release history → the release before the bad one
   → Roll back.
3. Check the pages (runbook step 10's checks).
4. Open the revert (as for the services, above) and merge it **with `SITE_DEPLOY` still off**. With
   it on, a merge landing before the revert would deploy the bad `web/` again. Its run deploys the
   services (unchanged) and says "Site off".
5. `gh variable set SITE_DEPLOY --body on -R timkrebs/ryft-platform`, then
   `gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform`. A run by hand always deploys,
   so this one deploys the reverted `web/` (and the services again) and runs the live check.

**Stop if** the live check fails twice on the same release on a header, a page, a redirect, the
AASA or the policy version: roll back. A timeout, a TLS or DNS error, or www alone failing is not
the release: the check runs on every push and doesn't retry, so dispatch again, and look at the
domain in the console if it persists. Also stop if `firebase deploy` fails for a reason other than
a missing permission, or if `site` deployed in a run whose summary says superseded.

**The app pin** (D234). The site's drift checks read the app: the tokens, the Swift licences, the
AASA's paths, the admin config, the mark and the icon. CI's `web-checks` checks out `timkrebs/ryft-app` at
the commit in `web/.app-ref`: one line, a full SHA, nothing else. App merges don't reach platform main
until a PR moves the pin, and that PR carries what the app's change implies. Nothing moves the pin
automatically. Move it when the app changed something the site follows (a colour, the type, the
mark, the wordmark, the app icon, a Swift package, the AASA's routes), from an up-to-date main (in zsh,
`setopt interactivecomments` first: the block carries comments):

```bash
git switch main && git pull --ff-only
git switch -c web/app-<short sha>
# The app at that commit, read-only. Not ../app: its branch is whatever the app session is on.
git clone --quiet https://github.com/timkrebs/ryft-app ~/ryft-app-pin   # once; later: git -C ~/ryft-app-pin fetch --quiet origin
git -C ~/ryft-app-pin switch --detach <sha>                             # a commit on the app's main
APP=$HOME/ryft-app-pin
git -C "$APP" rev-parse HEAD > web/.app-ref
RYFT_APP_DIR=$APP make web-tokens                       # src/styles/tokens.{css,json}, swatches.css
# When the app icon or a colour moved (web D22; the icon test fails until both ran; the OG card needs the fonts).
# They write the touch and manifest icons and the OG card; the tab icon (favicon.svg, favicon-32.png) is the site's own (web D23):
(cd web && npm run fonts -- --if-missing && RYFT_APP_DIR=$APP npm run icons && RYFT_APP_DIR=$APP npm run og)
RYFT_APP_DIR=$APP make web-test web-build web-e2e       # the build's prebuild also rewrites the Swift licence snapshot
git status --short                                      # web/.app-ref and the files the app's change implies
git add web && git commit -m "web: the app at <short sha>"
git push -u origin HEAD
gh pr create --fill
gh pr checks --watch                                    # web-checks green: tokens:check, licenses:check and the icon test against the new pin
```

**Stop if** `make web-tokens` throws (the app changed a source file's shape: `web/scripts/tokens.mjs`
needs a change in the same PR), if `npm run icons` or `npm run og` exits 1 (the icon's tile isn't one
colour or isn't the tokens' background, its word reaches the edge, or the OG card's tile meets the
copy: the icon's design moved, which is the user's call; web D22), or the diff asks for a design change
beyond the tokens (that's the user's call, in its own PR). To roll back, revert the bump PR: the pin and its outputs go back together.

### The €20 stop line

The budget "RYFT 50 EUR" emails, but it caps nothing (design §7). Max-instances is the hard cap.
At €20 actual spend in a month (Billing → Reports, project `ryftlab-prod`), and **from
`edge = "built"` on at €45**, the budget's own 90 % mail (the user's decision, D242; **With the
edge**, below):

```bash
for svc in api ingest; do
  gcloud run services remove-iam-policy-binding "$svc" --region=europe-west1 --project=ryftlab-prod \
    --member=allUsers --role=roles/run.invoker
done
gcloud scheduler jobs pause ryft-tick --location=europe-west1 --project=ryftlab-prod
```

api and ingest then answer 403 to everyone: the app fails, and "RYFT · Uptime check failing" fires
for `ryft-api-ready` and `ryft-ingest-ready` until the apply below removes both checks. scoring and notify
still drain what Pub/Sub holds. The tick stops, and with it everything that only a tick runs:
each service's outbox sweep (rows an inline flush missed wait, and published rows are no longer
deleted after 7 days), scoring's pass, notify's stale check and its deferred pushes, and api's
two retention jobs. Those are the purge of a code's bound email 30 days after the code stopped
being redeemable (D161), and the retries of account deletions whose Apple revoke or Supabase
deletion failed. That Supabase user, email included, stays until a retry succeeds. So while the
tick is paused you run it by hand (**While the tick is paused**, below).

Then open a PR that sets `public_invokers = false` in `infra/gcp/gates.auto.tfvars`. Otherwise
the next apply would add `allUsers` back: the invoker bindings are authoritative. Set the hold
first (**Deploy, roll back, rotate** → The hold): the merge is a push to main, and while api and
ingest refuse everyone, every deploy's smoke fails. Once it is merged:

```bash
terraform -chdir=infra/gcp plan -out=stop.tfplan
terraform -chdir=infra/gcp apply stop.tfplan
rm infra/gcp/stop.tfplan
```

Expected: 0 to add, 0 to change, 2 to destroy (3 with alerting on): the uptime checks
`ryft-api-ready` and `ryft-ingest-ready`, and with alerting on the policy "RYFT · Tick missing"
(D236: nothing to check on hosts nobody may call, no tick to miss). Terraform reports api's and
ingest's invoker bindings (without `allUsers`) and the tick's pause as changes outside Terraform.
If the gcloud commands haven't taken effect:

```text
# The stop line: 0 to add, 3 to change, 2 to destroy (3 with alerting on).
#   2 run.invoker bindings (api, ingest): members lose allUsers, keep ryft-push               =  2
#   + ryft-tick: paused false -> true                                                         =  1 (3) to change
#   2 uptime checks (ryft-api-ready, ryft-ingest-ready) [+ the policy RYFT · Tick missing]    =  2 (3) to destroy
```

Any other destroy: stop. None of these destroys is stateful. scoring's and notify's bindings, the
mappings, the deployer's bindings and the site's checks don't change.

Find the cause in the billing report (Billing → Reports, group by SKU) before undoing either.
The undo is a PR with `public_invokers = true`. Its apply puts `allUsers` back into api's and
ingest's bindings and resumes the tick: 2 to add (the two checks; 3 with alerting on, the tick
policy), 3 to change (2 bindings + the tick), 0 to destroy. Then delete the hold and run
`deploy.yml` by hand with the smoke on (step 9's commands).

**While the tick is paused**, run one by hand at least once a day, so no email outlives its 30
days and no failed deletion waits more than a day. Publish one tick message. Each service's tick
subscription pushes it with `ryft-push`'s token, which keeps `run.invoker` on api and ingest under
the stop line:

```bash
CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB=https://europe-west1-pubsub.googleapis.com/ \
  gcloud pubsub topics publish ryft-tick --project=ryftlab-prod --message='{}' --attribute=ryft-key=tick
```

That is one tick of all four services, scoring's pass included (at most 240 s). api's log names
a failed step: `invite email purge failed` or `reconcile deletions`. Don't resume the Scheduler
job on its own: the pause follows `public_invokers`, so the next apply pauses it again. The tick
resumes with the undo above.

The €20 line has no email of its own: you read it off Billing → Reports. The budget's rules stay
step 3's `0.1, 0.5, 0.9, 1.0`. If a `0.4` rule was added earlier (an older version of this page
offered one), remove it: Billing → Budgets & alerts → "RYFT 50 EUR" → Edit → Actions, delete the
40 % row, Save. Then step 3's `describe` must show `0.1, 0.5, 0.9, 1.0` again.

**With the edge** (step 16, from `edge = "built"` on). The load balancer and Cloud Armor cost about
€28 a month on their own, whatever the traffic (D242 → Cost, accepted by the user on 2026-11-19), so
the edge alone would cross €20. **From `built` on the line is €45** (the user's decision, D242): that
is the budget's existing 90 % mail, so no new rule. Back at `off` the line is €20 again. The
commands above still close api and ingest: the load balancer calls Cloud Run without a token, as
`allUsers`, so through it too everyone gets 403. But the load balancer keeps billing under the stop
line. Cutting its cost is the edge's way back, one PR a step: `locked` → `live` (only if it is
`locked`; 16.8's costly step), the deploy PR reverted, `live` → `built`, `built` → `off` (16.8).

### Dead letters and the outbox

**Dead letters** (D218, design §2.2). An event that fails its 20 delivery attempts is forwarded by
Pub/Sub from its push subscription to the consumer's dead-letter topic, `ryft-dlq-scoring` or
`ryft-dlq-notify`, with the `CloudPubSubDeadLetterSource*` attributes added. An event the consumer can
never handle (its data doesn't decode, or its `ryft-key` isn't the push path's) is filed there by the
consumer itself, with `ryft-error`, and acked. The hold subscriptions `dlq-scoring-hold` and
`dlq-notify-hold` keep each letter **14 days**; then Pub/Sub drops it. "RYFT · Dead letters held"
fires while one holds a letter. The cause is in the consumer's log (`handle event failed`, its `err`), around the
letter's time.

`scripts/dlq.sh` runs `gcloud` as you, in the project you name: `--project` has no default, and no call
uses gcloud's configured project. Its calls go through the regional endpoint.

```bash
scripts/dlq.sh --project ryftlab-prod list               # per hold subscription, one line per letter: key, by, source, attempts, error, verdict
scripts/dlq.sh --project ryftlab-prod show scoring 5     # in full, the data decoded (n: 1 to 1000, or all)
scripts/dlq.sh --project ryftlab-prod replay scoring 1   # after fixing the cause: back to its topic as a redelivery, then acked
scripts/dlq.sh --project ryftlab-prod purge notify       # notify's: a push hours late is wrong (replay refuses without --force)
```

`list` and `show` pull without acking and hand the letters back at once, so nothing moves. One pass
returns at most 1000 letters, and may return fewer than the subscription holds. The count is the hold
subscription's `num_undelivered_messages` in Monitoring (the dead-letter policy's value; the
dashboard's "Dead letters held"). Against compose's
emulator: `PUBSUB_EMULATOR_HOST=localhost:8085 scripts/dlq.sh --project ryft-local list`.

**Before a replay**, all of these:

1. The cause is fixed and deployed: the `deploy.yml` run of the fix is green, and no hold is set.
2. "RYFT · Event backlog over 15 minutes" is quiet for the consumer, so the replay doesn't queue
   behind a backlog.
3. `list` shows the letters you expect (count, keys, one cause in the log). `show` a few.
4. Start with `replay scoring 1`, watch it (below), then the rest.

**What a replay does.** For each letter it decides the key and the topic (below). It publishes the
letter's data there with the letter's attributes, minus Pub/Sub's `CloudPubSubDeadLetter*` and
`ryft-error`, plus `ryft-key` and `ryft-replayed=true`. On `ryft-ingested` and `ryft-session-completed`
it also sets the ordering key, the event's `user_id`. Only then does it ack the letter. The consumer
handles it as a redelivery (`events.Redelivered`, logged as `handling a replayed dead letter`): a
scoring letter re-scores the days after its days and finishes what its failed attempts left half-done
(D112). It gets its 20 attempts again. If it fails them, it is back in the hold subscription and `list`
shows `"replayed_before": true`. That takes about 10 minutes under push backoff, longer under the retry
policy (design §2.2). Call it done when `list` is empty 30 minutes later and the consumer's 5xx
(the dashboard's "5xx per second") are flat.

**Which letters are replayed** (D150). Only the keys the consumer handles: scoring `ingested`,
`session.completed`; notify `scored`, `session.scored`, `refresh`. The script reads them from
`infra/gcp/pubsub.auto.tfvars.json`. The key is read where a publisher can't write it:

- **Pub/Sub forwarded it** (`CloudPubSubDeadLetterSourceSubscription` is set; `list` says
  `"by": "pubsub"`). The key is its source subscription's (`scoring-ingested` → `ingested`), and its
  `ryft-key` must be absent or equal to that key. From `notify-wait` the key is its `ryft-key`, which must
  be one of notify's. It goes back to the source subscription's topic (`notify-wait`'s to
  `ryft-notify-wait`). The source must be one of this consumer's subscriptions, in this project.
- **The consumer filed it** (`ryft-error`, no source; `"by": "consumer"`). The key is its `ryft-key`,
  which the consumer set from the push path, and it goes to that key's topic.

Only the consumer and Pub/Sub's service agent may publish to a dead-letter topic, and the consumer
copies no `CloudPubSub*` attribute, so a publisher can't forge either reading. Anything else is refused,
named on stderr and left in the hold subscription, and the script exits 1. That covers a source of
another consumer or project, a key the consumer doesn't handle, a `ryft-key` that contradicts its
source, no key, data that isn't text, an ordered topic's event without a `user_id`, and a letter that is
neither Pub/Sub's nor the consumer's.

**A refused letter that must go through** (`show` it first, and be sure of its key): publish it by hand
with the original data and the key's attributes. The original stays in the hold subscription until its
14 days end (`purge` removes every letter of the consumer). `--ordering-key` only on `ryft-ingested` and
`ryft-session-completed`:

```bash
CLOUDSDK_API_ENDPOINT_OVERRIDES_PUBSUB=https://europe-west1-pubsub.googleapis.com/ \
  gcloud pubsub topics publish ryft-ingested --project=ryftlab-prod --ordering-key='<user_id>' \
  --message='<the data as show prints it, compact>' \
  --attribute='ryft-key=ingested,ryft-replayed=true,ryft-message-id=<its ryft-message-id>'
```

**Nothing is lost.** A letter is acked only after its publish succeeded. A failed publish or ack stops
the run, and what it didn't replay is handed back at once (Pub/Sub would redeliver it after its lease,
at most 10 minutes, anyway). An ack that fails after its publish leaves the letter in place, so the next
replay sends it again; handlers are idempotent (D100). Every argument is checked before `gcloud` runs. A
letter names a user and a day or days (and a session id, the reason, the request id). No scores or
other health values: `scored` carries only `user_id`, `day` and `request_id`. Its `ryft-message-id`
names the user and day too. So the script hands `gcloud` the data, the attributes and the ordering key
in a flags file (mode 600, in its own temporary directory, deleted after each call). `ps` shows only
the topic, and `gcloud`'s file log is off.

notify's letters are listed and purged, not replayed (D84). `--force` replays them anyway. scoring's
letters only name a user and days, so `POST /v1/recompute` repairs the same if a letter is gone.

**The outbox** (D101, D219): events not yet published are rows of `outbox` in Supabase (SQL editor, as
the owner). Every row is due when it is written, except notify's deferrals. Those wait for their
`available_at` (the end of quiet hours) and are not a backlog:

```sql
SELECT publisher, count(*), min(created_at) FROM outbox WHERE published_at IS NULL AND available_at <= now() GROUP BY 1;   -- the backlog
SELECT count(*), min(available_at) FROM outbox WHERE publisher = 'notify' AND published_at IS NULL AND available_at > now();  -- deferrals, not due yet
SELECT id, publisher, routing_key, attempts, last_error FROM outbox WHERE published_at IS NULL AND attempts >= 50;
UPDATE outbox SET attempts = 0 WHERE id = 1234;   -- a stuck row, once its cause is fixed: the next tick's sweep sends it again
UPDATE outbox SET attempts = 0 WHERE published_at IS NULL AND attempts >= 50 AND last_error LIKE 'rejected, set aside:%';   -- every row Pub/Sub refused (a missing topic or binding after a deploy), once the cause is fixed
```

A row is published inline after the commit that wrote it. What that misses (a Pub/Sub error, an
instance gone) goes with the next tick's sweep, every 15 minutes. A sweep that fails makes its tick
`result` error, so a lasting backlog is "RYFT · Tick missing" (D236; the app metrics' OutboxBacklog
and OutboxStuck went with Grafana Cloud). A row set aside (refused by Pub/Sub at once, or after 50
failed tick sweeps; `last_error` says which) raises no alert: the queries above find it. It is
usually a key without its topic: a topology change that isn't applied yet (`infra/gcp`).

An account deletion deletes the user's outbox rows, but `outbox` has no foreign key to `users`, so
a row written just after the deletion stays. One is a deferral of notify's (an event it held for
quiet hours while the account went), another a row of scoring's. Its body is the user's id and a
day (and a session id). When it is due it is published, and its consumer drops it because the user
is gone. The tick's clean deletes it 7 days after it was published.

### When an alert fires

Cloud Monitoring's six policies (`infra/gcp/alerts.tf`, D236, D244) reach the email `ryft-timkrebs`
and the Google Cloud app. Each notification links its incident (Monitoring → Alerting →
Incidents), which names the series that crossed: the check, the service, the subscription or the
kind. The dashboard **RYFT · Overview** has the context of the first five. An incident closes by
itself once its query returns nothing, or 30 minutes after its data stops; the sixth's when its
data stops, without a closing notice.

| Policy | Fires when | First look |
| --- | --- | --- |
| RYFT · Uptime check failing (critical) | a check fails from 2 of its 3 locations for 10 minutes | the check's page (which location, which status or TLS error). `ryft-site-*`: Firebase Hosting and the `site` job of `deploy.yml` (Deploy, roll back, rotate → The site). `ryft-api-ready`, `ryft-ingest-ready`: the service's revisions and its log; `/health/ready` is the database behind it, so Supabase's status next |
| RYFT · 5xx over 2 % (api, ingest) (critical) | 5xx above 2 % of a service's requests over 10 minutes, at least 3, held 10 minutes | Logs Explorer: `resource.type="cloud_run_revision" resource.labels.service_name="<service>" httpRequest.status>=500` (Cloud Run's request log; from `edge = "built"` on it keeps api's and ingest's 5xx only, which is all this needs, D242), then the service's ERROR lines (the dashboard's logs panel). A deploy just before: roll back (Deploy, roll back, rotate) |
| RYFT · Tick missing (critical; only while the tick runs) | a service without a tick of `result` ok for 45 minutes (three ticks), held 5 | Logs Explorer `jsonPayload.message="tick"` for the service: `result` error and its `err` name the step (the outbox sweep, the purges, the reconcile, a database that is down). No line at all: the Scheduler job `ryft-tick` (`gcloud scheduler jobs describe ryft-tick --location=europe-west1 --project=ryftlab-prod`), then the subscription `tick-<service>`. A `LOG_LEVEL` above `info` silences the line, and this policy with it |
| RYFT · Dead letters held (warning) | a hold subscription (`dlq-*-hold`) holds a letter, held 5 minutes | `scripts/dlq.sh --project ryftlab-prod list`, the cause in the consumer's log by `messageId` (**Dead letters and the outbox**) |
| RYFT · Event backlog over 15 minutes (warning) | an event subscription's oldest unacked event is over 15 minutes old, held 5 | the consumer's request log (429, 5xx) and its max instances; it dead-letters after 20 attempts, and then the policy above fires |
| RYFT · Feedback and admin access (warning; D244) | a line of one kind on api in the last 5 minutes, held 1: `feedback received`, `admin signed in` (an operator session begun, per instance), or `admin auth rejected` but for `reason` `missing` and `expired`. One incident per kind; it stays one while the lines keep coming | by the kind in the subject. **feedback received**: `/admin` → Feedback, or `scripts/beta_report.sh feedback` (GCP 14a.8); Resolve it once read. **admin signed in**: if it wasn't you, **Beta → Admin sign-in → Lost phone, or any doubt about the admin account**, at once, then `/admin` → Audit. **admin auth rejected**: Logs Explorer `resource.labels.service_name="api" jsonPayload.message="admin auth rejected"`, its `reason`: `not_a_token` a scanner or a wrong key; `signature`, `unknown_key` a forged or foreign token; `role`, `not_admin`, `not_allowed` a valid token that isn't an operator's; any other reason (`malformed`, `issuer`, `audience`, `key_mismatch`, …, `internal/auth/supabase.go`) a token that isn't one of the project's. Many, or ongoing: the admin routes' per-address limit answers them with `429` (**Beta → Admin sign-in**) |

What no policy covers since D236 (OBSERVABILITY → Cloud Monitoring → What D236 dropped): a slow
sync (ingest's p95 panel), 429s (their panel), APNs, the admin page's other events (`admin second
factor required`, `admin auth unavailable`: **Beta → Admin sign-in** → Alerts), the www redirect and
the certificate's days left (its panel; Google renews it, and a failed renewal fails TLS, which the
uptime checks see). Feedback and the admin sign-ins have the sixth policy since D244.

### A deploy: what the old pod logs

On Cloud Run an old instance gets SIGTERM when its revision stops serving, and SIGKILL 10 s later.
Every service (`internal/app`) logs its shutdown, in this order: `shutting down` (`reason`
`signal`), one `shutdown step` per step (`drain` when a drain delay is set, `http`, `push` when a
metrics push is configured, `pool`; `dur_ms` each), and `shut down` with `exit` 0. An instance that
logs `shutting down` but never `shut down` was killed at 10 s, or ended by the watchdog at 9 s (a
`shutdown overdue` line, then exit 1, or 2 after a panic): the last `shutdown step`, or the `step`
of `shutdown overdue`, names the step it was in. Exit codes: 0 clean; 1 a start failed, a task
failed or a step overran its bound; 2 a task panicked (recovered, then a normal shutdown). A SIGTERM
while the service still waits for the database is a clean stop: `start interrupted by the signal`
(info, `step` `db`), then `shutting down` and `shut down`, exit 0. A push or a tick in flight when
the `http` step ends is cut; Pub/Sub delivers the push again (D100), and the next tick comes in 15
minutes.

```bash
L=(--project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs)
# the shutdown timeline of the last hour, per instance
gcloud logging read 'resource.type="cloud_run_revision" AND jsonPayload.message=("shutting down" OR "shutdown step" OR "shut down" OR "shutdown overdue")' \
  "${L[@]}" --freshness=1h --order=asc --limit=500 \
  --format='value(timestamp,resource.labels.service_name,labels.instanceId,jsonPayload.message,jsonPayload.reason,jsonPayload.step,jsonPayload.dur_ms,jsonPayload.exit,jsonPayload.err)'
# panics of the last 7 days: a goroutine's (task), a ready check's (check), a tick step's (step) or a handler's (none of the three)
gcloud logging read 'resource.type="cloud_run_revision" AND jsonPayload.message="panic"' "${L[@]}" --freshness=7d --limit=20 \
  --format='value(timestamp,resource.labels.service_name,jsonPayload.task,jsonPayload.check,jsonPayload.step,jsonPayload.panic)'
```

The stack is in the expanded entry (Logs Explorer), the one place a stack is logged (**Data
protection (logs)**): read it there, copy nothing out. No policy watches exits or panics since D236:
a handler's panic answered `500` (the 5xx policy may see it), an event handler's is retried and at
its 20th attempt dead-lettered (**Dead letters and the outbox**).

### Lockouts and rolling back across releases

**The database refuses every connection** (`ECIRCUITBREAKER: too many authentication failures`
in every service's log, `/health/ready` answers 503): Supabase's pooler blocks a role for 120 s
after 10 failed logins in 150 s, and repeated failures get the address banned on the database host.
**Do not re-run the deploy, `migrate` or a login in a loop.** Wait two minutes, find the failing login
(**Deploy, roll back, rotate → A database password rotation**, step 4, has the query; a revision
pinned at an older password version that still gets traffic is the usual cause), and look at Supabase
→ Database → Settings → Network Bans (or `supabase network-bans get --project-ref <ref>`) and remove
the bans. The instances reconnect on their own (incident of 2026-10-20 20:43, `STATUS.md`).

**No down-migrations.** Migrations only add (tables, nullable or defaulted columns, grants,
backfills; never a drop or a rename), so the previous release runs on the newer schema: it ignores
what it does not know, and its `migrate` skips every file it has and does not mind newer rows in
`schema_migrations`. A rollback is therefore never a schema change.

**A migration that hits the lock timeout.** The migrator sets `lock_timeout` to 5 s in every
migration's transaction (`internal/db`, `lockTimeout`; a migration may set its own `SET LOCAL`,
which wins; 0027 does, also 5 s). A migration that cannot get its lock in that time fails with
`apply 00NN_name.sql: hit the lock timeout ...` (SQLSTATE 55P03). Its transaction rolls back:
`schema_migrations` and the schema stay as they were, the earlier migrations of the run stay
applied. The `migrate` job fails (no retry: `maxRetries` 0 in `deploy/run/migrate.yaml`),
`deploy.yml` stops before any service rolls, and the running revisions keep serving. The wait for
the migration lock itself (a second migrator, e.g. a run by hand next to a deploy) is not cut by
this timeout; it ends with the job's 600 s. The services never migrate, they only wait for the
schema (`WaitMigrated`). `statement_timeout` is not set: a migration that runs long (a backfill) is
not killed, but it holds its locks while it runs. `Seed` (the built-in exercises and plans, after
the migrations) gets the same 5 s timeout; its error is the raw `55P03` ("canceling statement due to
lock timeout") without the `apply ...` wrapper, on the line `seed`. The job's log: the `migrate`
query of **Deploy, roll back, rotate → The first deploy**, with the failed execution's name.

Find the blocker (as the database owner, in the Supabase SQL editor or `psql`):

```sql
-- sessions holding or waiting for locks on the table the migration names
SELECT a.pid, a.state, a.xact_start, now() - a.xact_start AS open_for, a.wait_event_type,
       l.mode, l.granted, left(a.query, 120) AS query
FROM pg_locks l JOIN pg_stat_activity a USING (pid)
WHERE l.relation = 'public.daily_scores'::regclass       -- the migration's table
ORDER BY a.xact_start;
-- long-open transactions (the migrator has given up by now, so nobody is blocked any more)
SELECT pid, backend_type, state, wait_event, xact_start, now() - xact_start AS open_for,
       left(query, 100) AS query
FROM pg_stat_activity
WHERE xact_start < now() - interval '5 seconds' AND pid <> pg_backend_pid()
ORDER BY xact_start;
```

Usually the blocker is a long transaction (`state = 'idle in transaction'`, or a long read). Let
it finish, or end it if it is safe. `idle in transaction`: only `SELECT pg_terminate_backend(pid)`
helps; `pg_cancel_backend(pid)` returns true but the session keeps its lock. `pg_cancel_backend`
ends a running query (`state = 'active'`) only. Never kill an autovacuum worker whose query says
"to prevent wraparound"; it can be the blocker, and the migration waits for it or goes first.

Each attempt queues every query on the target table for up to 5 s, so rerun once the blocker is
gone, not blindly: `gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform` (it runs
`migrate` again, then the services), at a quiet time. Nothing needs undoing.

### The beta gate: warn, then on (D238)

> **`BETA_GATE` is `off`** (D241, the user's decision on 2026-11-18): the beta has no invite codes.
> Every signed-in account is served and nothing is flagged; consent stays `on`. Access on the server is
> to come with App Attest, which is still to be decided. The steps below are kept for later. A gate
> starts again with a one-line PR to `warn`, never straight to `on` (it would refuse every account
> without a code), and then follows steps 1–6. The two
> unused codes (`beta 1 operator`, `beta 1 smoke`) are revoked. Their rows stay listed as `revoked`, and
> nobody can redeem them. With the gate off, `make smoke-prod` passes without the admin key; for a smoke
> user without access it prints "! the smoke user has no beta access: fine while BETA_GATE is off".

`BETA_GATE` (D79, D87) is a line of `deploy/run/config.env`, rendered into api and ingest only
(scoring reads only the consent mode). D238 set it to `warn`; step 6's one-line PR sets `on`;
`config.env` holds the mode in force. `CONSENT_ENFORCE` isn't in
`config.env`, so the services' default `on` holds in every step here: a sync without consent to the
current policy is `403 consent_required` throughout. This is how **Beta → The gates and their
modes** are set; the rest of **Beta** (codes, bound codes, the users list, the audit) uses the
admin key from step 1 below.

| `BETA_GATE` | An account without beta access (no redeemed code, no operator grant) |
| --- | --- |
| `off` | served, nothing flagged |
| `warn` | served; the answer carries `X-RYFT-Gate: invite_required`, the access line `gate`, and `GET /v1/me/sync` → `gate` says `invite_required` |
| `on` | `403 invite_required` on every `/v1/*` route but `GET /v1/me`, `GET /v1/me/sync`, `GET /v1/config`, `GET /v1/model`, `POST /v1/me/invite`, `POST`/`DELETE /v1/me/consent`, `POST /v1/feedback` and `DELETE /v1/me` (API.md → Errors) |

The app (API.md → Errors): under `warn` its Today screen shows "Action needed: enter your invite
code to use RYFT." with **Enter code**, and everything keeps working. Under `on` a `403
invite_required` stops every call but onboarding's until a code is redeemed.

**What D238's merge did.** A normal deploy (**Deploy, roll back, rotate**): api and ingest roll with
`BETA_GATE=warn`; scoring and notify roll with the new images, their env unchanged. Nothing is refused
that was served before. The run's `make smoke-prod` has no admin key, so for a smoke user without
access it says "! the smoke user has no beta access: fine while BETA_GATE is off" (off or warn), its
requests are served with the header, and it ends `gates: invite unknown (no ADMIN_API_KEY), consent on`.
The mode in force, from the repository root:

```bash
for s in api ingest; do
  echo "$s $(gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod --format=json \
    | jq -r '.spec.template.spec.containers[0].env[] | select(.name == "BETA_GATE") | .value')"
done                                                    # api warn, ingest warn
gcloud logging read 'resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND jsonPayload.message="beta"' \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1d --limit=4 \
  --format='value(timestamp,resource.labels.service_name,resource.labels.revision_name,jsonPayload.gate,jsonPayload.consent)'
                                                        # the newest revisions: gate warn, consent on
```

The `beta` line is each instance's start (min instances 0: one at the deploy, then one per cold start).

**1. The admin key, never on screen.** The api reads `admin-api-key` at the version `api.yaml` pins.
In zsh, from the repository root on an up-to-date main:

```bash
V=$(sed -n 's/.*name: admin-api-key, key: "\([0-9]*\)".*/\1/p' deploy/run/api.yaml)   # the version the api reads
ADMIN_API_KEY=$(gcloud secrets versions access "$V" --secret=admin-api-key --project=ryftlab-prod) && export ADMIN_API_KEY
echo "admin key: ${#ADMIN_API_KEY} characters"          # a length, never the key
API=https://api.ryftlab.de
adm() { printf 'Authorization: Bearer %s\n' "$ADMIN_API_KEY" | curl -sS --fail-with-body -H @- -H 'Content-Type: application/json' "$@"; }
```

`adm` keeps the key off argv (`printf` is a builtin; curl reads the header from stdin). Without
`gcloud`, the 1Password item "RYFT ADMIN_API_KEY" at a hidden prompt does the same:
`read -rs "ADMIN_API_KEY?ADMIN_API_KEY: "; echo; export ADMIN_API_KEY` (zsh's `name?prompt`). `make smoke-prod`,
`scripts/beta_report.sh` and `scripts/user.sh` take it from the environment (`scripts/lib/admin.sh`).
`unset ADMIN_API_KEY` when you're done.

**2. Codes** (`POST /v1/admin/invites`, API.md → Invites for a person). Each one unbound, single
use, with an expiry, and a note that names nobody: an unbound code's note is never cleared and has
no hard delete. No `email` (a bound code) until the privacy text names the stored address (U3,
**Beta → Before the first bound code**). One code per account: yours, the smoke user's, one per
tester. The loop makes yours and the smoke user's. Run it again with `tester-1 tester-2 …` when the
testers' TestFlight invites go out (step 15), so their 14 days start then (without the `rm` line).
The links go into a file only you can read, never to the screen. The whole loop runs under `umask
077`, so the file is `0600` whichever run creates it:

```bash
EXP=$(date -u -v+14d +%Y-%m-%dT%H:%M:%SZ)               # 14 days from now (macOS date)
rm -f ~/ryft-invites.txt                                # the first run only: no old line left
(umask 077
for n in operator smoke; do                             # later: tester-1 tester-2 …, one per tester; never a name
  adm -X POST "$API/v1/admin/invites" -d "{\"note\": \"beta 1 $n\", \"max_uses\": 1, \"expires_at\": \"$EXP\"}" \
    | jq -er --arg n "$n" 'select(.link != null) | "\($n) \(.link)"' >> ~/ryft-invites.txt \
    || echo "no code made for $n: stop (curl's error is the line above)" >&2
done)
ls -l ~/ryft-invites.txt                                # -rw------- (0600)
wc -l < ~/ryft-invites.txt                              # one line per code
adm "$API/v1/admin/invites" | jq -c '.[] | select(.note | startswith("beta 1 ")) | {note, status, uses, expires_at}'
                                                        # status open, uses 0; no code, no email, no account
```

A tester gets their line's link (`https://ryftlab.de/invite/RYFT-…`) with the TestFlight invite.
On the iPhone it opens the app's invite screen, or they type the code at **Enter code**. `rm
~/ryft-invites.txt` once every link is sent. The operator's page (`https://ryftlab.de/admin` →
Invites) makes the same codes, if your operator account is set up on this project (**Admin
sign-in**). A code that isn't needed is revoked:
`adm -X DELETE "$API/v1/admin/invites/<code>" -w '%{http_code}\n'` (`204`).

**3. Redeem yours and the smoke user's.** Yours in the app, which also proves that the TestFlight
build takes a code: on the Today screen, **Enter code** under "Action needed", and paste the code
(on the Mac, `sed -n 's#^operator .*/invite/##p' ~/ryft-invites.txt | pbcopy`, Universal Clipboard to
the iPhone, then `pbcopy < /dev/null`), or send yourself the link and open it on the iPhone. No
"Action needed" line means your account has access already (it existed at migration 0017, or has an
operator grant): revoke that code instead.

The smoke user's, with its token (the email, the password, the token and the code never reach argv
or the screen; zsh, in step 1's shell):

```bash
read -rs "SMOKE_EMAIL?Smoke user's email: "; echo; read -rs "SMOKE_PASSWORD?Its password: "; echo
read -r "SUPABASE_ANON_KEY?Supabase anon key (Project settings -> API): "
REF=$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env)
TOKEN=$(E="$SMOKE_EMAIL" P="$SMOKE_PASSWORD" jq -n '{email: env.E, password: env.P}' \
  | curl -fsS -X POST "https://$REF.supabase.co/auth/v1/token?grant_type=password" \
      -H "apikey: $SUPABASE_ANON_KEY" -H 'Content-Type: application/json' --data-binary @- | jq -r .access_token)
as_smoke() { printf 'Authorization: Bearer %s\n' "$TOKEN" | curl -sS --fail-with-body -H @- -H 'Content-Type: application/json' "$@"; }
as_smoke "$API/v1/me" | jq .beta_access                 # false: redeem below; true: revoke its code instead
C=$(sed -n 's#^smoke .*/invite/##p' ~/ryft-invites.txt)
as_smoke -X POST "$API/v1/me/invite" --data-binary @<(C="$C" jq -nc '{code: env.C}') -w '%{http_code}\n'   # 204
as_smoke "$API/v1/me" | jq .beta_access                 # true
unset C TOKEN
```

`make smoke-prod` with the key in the environment does the smoke user's half by itself: for a
smoke user without access it makes a one-use code (note `smoke-prod`, no expiry) and redeems it. Run
it once now, with the key, in the shell above, and not while a `deploy.yml` run is going (`gh run
list -w deploy.yml -L 1`): its probe takes the smoke user's access away for one request, and under
`on` a deploy's smoke in that second goes red:

```bash
export SMOKE_EMAIL SMOKE_PASSWORD SUPABASE_ANON_KEY
SUPABASE_URL=https://$REF.supabase.co make smoke-prod  # … gates: invite warn, consent on
unset SMOKE_PASSWORD
```

With the key, its gate probe takes the smoke user's access away for one `GET /v1/days`, then gives it
back by operator grant (`POST /v1/admin/users/{id}/access`, audited). So each such run writes one
warn line on purpose: `api GET /v1/days invite_required`. The smoke in `deploy.yml` has no key and
never probes. If the probe fails half way ("it is left without"), run `make smoke-prod` with the key
again: it redeems a new one-use code.

**4. The warn lines.** The access line (`message` `http`, `internal/httpx.Instrument`) of a request
the gate let through carries `gate`. Its fields: `time`, `severity`, `message`, `service`, `method`,
`path`, `status`, `bytes`, `dur_ms`, `code` (a refused request's), `gate`, `request_id`,
`client_request_id` (when the client sent one), `user_id`, `route` and `logging.googleapis.com/trace`.
It holds no IP (D222). Cloud Run's own request log (`run.googleapis.com/requests`) does, in
`httpRequest.remoteIp` (from `edge = "built"` on, for api and ingest only a 5xx is kept, D242), so both queries read only the services' stdout. The print leaves out
`user_id`, `request_id`, `client_request_id`, `path` (dates and resource ids), the trace, `status`,
`bytes` and `dur_ms`. `route` is the pattern with its method (`GET /v1/days/{day}`).

Logs Explorer on `ryft-logs` (project `ryftlab-prod`, scope: the bucket's `_AllLogs` view):

```text
resource.type="cloud_run_revision"
(resource.labels.service_name="api" OR resource.labels.service_name="ingest")
logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout"
jsonPayload.message="http"
jsonPayload.gate:*
```

An expanded entry shows the whole payload, `user_id` included. Add `jsonPayload.route` and
`jsonPayload.gate` as summary fields, read the histogram, and copy nothing out of an expanded entry.
The same lines in a shell, printed without an id:

```bash
Q='resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout" AND jsonPayload.message="http" AND jsonPayload.gate:*'
gcloud logging read "$Q" --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs \
  --freshness=7d --limit=500 --format='value(timestamp,resource.labels.service_name,jsonPayload.route,jsonPayload.gate)'
                                                        # the newest 500 of 7 days, newest first
# How many lines and how many accounts over the same 7 days, as counts: the ids go into sort and wc,
# never to the screen.
gcloud logging read "$Q" --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs \
  --freshness=7d --limit=100000 --format='value(jsonPayload.user_id)' | wc -l
gcloud logging read "$Q" --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs \
  --freshness=7d --limit=100000 --format='value(jsonPayload.user_id)' | sort -u | wc -l
```

`ryft_gate_refusals_total` is counted in each instance, but nothing reads it on Cloud Run (no scrape,
no push, D236; OBSERVABILITY → The OTLP push (dormant)). The access line's `gate` (warn) and `code`
(on) are the only signal on GCP.

**5. The stop rule before `on`.** Flip only when all of these hold:
1. The 7-day print in step 4 (`--freshness=7d`, inside `ryft-logs`' 14 days) shows no line but the
   probe's (`api GET /v1/days invite_required` at the minutes of your own `make smoke-prod` runs with
   the key), its line count is under 500 (else the print shows only the newest 500: read them all
   before you trust it), and its account count is 0, or 1 when you ran the probe (the smoke user):
   for a whole week your account and the smoke user's were served without a flag, and so was every
   tester who is in.
2. Every code you handed out is used, yours and the smoke user's included:
   `adm "$API/v1/admin/invites" | jq -c '.[] | select(.note | startswith("beta 1 ")) | {note, status}'`
   shows `used` for each one you sent (revoke the rest).
3. No account that should keep access lacks it:
   `adm "$API/v1/admin/users" | jq '{accounts: length, without_access: ([.[] | select(.beta_access | not)] | length)}'`.
   Every account counted in `without_access` is refused at the flip. That's right for one you don't
   know, and wrong for anyone you invited: they redeem first.
4. The testers' TestFlight build is the one whose **Enter code** took your code in step 3, or a newer
   one. The app sends a `403 invite_required` to that same screen (API.md → Errors); the throwaway
   check after the flip proves it on production.
5. `make smoke-prod` is green: the newest `deploy.yml` run, and once from your shell with the key
   (step 3), ending `gates: invite warn, consent on`.

**Stop if** any of these fails: stay on `warn`, which refuses nothing.

**6. The flip: a PR of one line.**

```bash
git fetch origin && git switch -c deploy/beta-gate-on origin/main
# deploy/run/config.env: BETA_GATE=warn becomes BETA_GATE=on (that line only, the comment stays)
make cloudrun-check
bash scripts/tests/check_cloudrun_test.sh
git diff --stat                                          # deploy/run/config.env | 2 +-
git commit -am "deploy: BETA_GATE on"
git push -u origin HEAD && gh pr create --fill -R timkrebs/ryft-platform
```

Its merge is a normal deploy. api and ingest roll with `BETA_GATE=on`, and scoring and notify with the
new images. From the first request a new revision serves, an account without access gets `403
invite_required` outside the open routes, and its app shows the invite screen. Ingest rolls before
api, so for a minute or two an account without access can be refused by one and served by the other.
`deploy.yml`'s smoke passes only if the smoke user has access (step 3). Without it, it fails with
"BETA_GATE is on and the smoke user has no access".

It worked when (in a new zsh: step 1, then step 3's three `read` lines, its `REF=` line and its
`export` line, first; not while another `deploy.yml` run is going, step 3):

```bash
for s in api ingest; do
  echo "$s $(gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod --format=json \
    | jq -r '.spec.template.spec.containers[0].env[] | select(.name == "BETA_GATE") | .value')"
done                                                     # api on, ingest on
# the beta line as above, --freshness=1h: gate on, consent on
SMOKE_EXPECT_GATES=on SUPABASE_URL=https://$REF.supabase.co make smoke-prod   # … gates: invite on, consent on
# the refusals since (api's route is /v1/, its gate runs before the inner routes):
gcloud logging read 'resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout" AND jsonPayload.message="http" AND jsonPayload.code="invite_required"' \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1d --order=asc --limit=200 \
  --format='value(timestamp,resource.labels.service_name,jsonPayload.method,jsonPayload.route)'
```

`SMOKE_EXPECT_GATES=on` fails unless both gates refused in the probe; it needs the key (without it the
invite gate reads `unknown`). Then the throwaway check: sign out in the app, sign up with an email
address you use for nothing else, and the app shows the invite screen and nothing loads. Delete that
account in the app (`DELETE /v1/me` is open under `on`), then sign in to yours again.

**Rollback:** a PR of one line, `BETA_GATE=warn` (or `git revert` of the flip, **Deploy, roll back,
rotate → By revert**). Its merge serves every account again from the first request the new revisions
take. Faster, while that PR waits: **A rollback → By traffic** for api and ingest (the hold first;
their previous revisions run `warn`), then merge the PR and release the hold. One tester refused by
mistake needs no rollback: a new code, or `scripts/user.sh <id> --grant` (**Beta**).

**Stop if** after the flip `deploy.yml`'s smoke fails at "the smoke user has no access" (step 3 for
the smoke user, then `gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform`), a tester who
redeemed is refused (roll back, then find out why), or the `beta` line says anything but `gate on`.

### The per-IP limit (D53, D240)

api and ingest limit each client address to 20 requests a second, bursts of 50, per instance, before
any token is checked (`internal/app/iplimit.go`; Traefik did it on EKS). It covers every route except
`/health/live`, `/health/ready` (and compose's `/healthz`, `/readyz`) and `POST /internal/tick`. On
api, a `/v1/admin/*` request that bears `ADMIN_API_KEY` passes it too. Over the limit, the answer is
`429`, the plain-text body `Too Many Requests`, `Retry-After: 1` and `X-Retry-In: <the wait, e.g.
37.5ms>`. The refusal is logged only on the access line (`code` `ip_limit`) and counted in
`ryft_rate_limited_total{class="ip"}`; the address appears nowhere (D222). The client is the
`XFF_TRUSTED_HOPS`-th `X-Forwarded-For` entry from the right, not counting Google's front-end hops.
`deploy/run/config.env` sets it, and it is 1 while Cloud Run's front end is all that is in front.

**After the deploy that brings it** (or any later change to `XFF_TRUSTED_HOPS`). Run this from your
Mac, on your own network: it needs `curl` 7.84 or newer for `%header{}` (`curl --version`), `jq` and
`gcloud`. The burst goes over IPv4 (`-4`), so the address the logs must not hold is one you know. In
zsh, run `setopt interactivecomments` first: stock zsh doesn't treat `#` as a comment at the prompt,
and the blocks below end lines with one.

```bash
# 1. The value in force, on both services:
for s in api ingest; do
  gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod --format=json \
    | jq -r '.spec.template.spec.containers[0].env[] | select(.name == "XFF_TRUSTED_HOPS") | .value'
done                                                          # 1, then 1

# 2. The burst: 300 requests without a token, 20 at a time, one host after the other.
for H in api.ryftlab.de ingest.ryftlab.de; do
  curl -4 -s -o /dev/null -w "%{http_code} warm-up $H\n" "https://$H/v1/me"   # wakes an instance (scale to zero)
  sleep 3                                                     # the bucket is full again after 2.5 s
  t0=$(date +%s)
  seq 300 | xargs -P 20 -I{} curl -4 -s -o /dev/null \
    -w '%{http_code} retry-after=%header{retry-after} x-retry-in=%header{x-retry-in} bytes=%{size_download} %{content_type}\n' \
    "https://$H/v1/me" > "burst-$H.txt"
  t=$(( $(date +%s) - t0 )); [ "$t" -gt 0 ] || t=1
  echo "$H: 300 in about $t s ($(( 300 / t )) a second): about $(( 50 + 20 * t )) 401s for each instance that served it"
  cut -d' ' -f1,2 "burst-$H.txt" | sort | uniq -c             # the summary
  grep -m 2 '^429' "burst-$H.txt"                             # two refusals in full
  sleep 3; curl -4 -s -o /dev/null -w "%{http_code} after 3 s $H\n" "https://$H/v1/me"
done
rm -f burst-api.ryftlab.de.txt burst-ingest.ryftlab.de.txt
```

What it should show, per host (the counts vary from run to run):

```text
401 warm-up api.ryftlab.de
api.ryftlab.de: 300 in about 9 s (33 a second): about 230 401s for each instance that served it
 224 401 retry-after=
  76 429 retry-after=1
429 retry-after=1 x-retry-in=41.3ms bytes=17 text/plain; charset=utf-8
429 retry-after=1 x-retry-in=12.9ms bytes=17 text/plain; charset=utf-8
401 after 3 s api.ryftlab.de
```

- The `401`s are the first 50 (the burst) plus about 20 for every second the burst took, for each
  instance that served it: the printed `50 + 20 × t`. So the split depends on how fast your
  connection sends the 300, not on a fixed range. The counts above are from the run of 2026-11-19 at
  about 33 a second (api 224 and 76, ingest 176 and 124). With api's concurrency of 40 one instance
  usually takes all of it, so api's `401`s are about the printed number or fewer. Ingest's
  concurrency of 4 can start its second instance, so up to twice that there (never more than 300).
  Every other line is a `429`.
- Every `429` has `retry-after=1`, an `x-retry-in` under `50ms`, `bytes=17` (`Too Many Requests`) and
  `text/plain`. The `401`s are the ordinary answer to a request without a token (JSON).
- `after 3 s` is `401` again: the limit has let go.

```bash
# 2b. Forged entries don't buy a bucket. The same burst, on api only, with two X-Forwarded-For lines of
#     your own: a fixed one and a different IPv6 /64 per request. Google appends your real address; if a
#     forged entry were the key, every request would have a fresh bucket and nothing would be refused.
H=api.ryftlab.de; sleep 3; t0=$(date +%s)
seq 300 | xargs -P 20 -I{} curl -4 -s -o /dev/null \
  -H 'X-Forwarded-For: 192.0.2.1' -H 'X-Forwarded-For: 2001:db8:{}::1' \
  -w '%{http_code} retry-after=%header{retry-after}\n' "https://$H/v1/me" | sort | uniq -c
t=$(( $(date +%s) - t0 )); [ "$t" -gt 0 ] || t=1; echo "about $t s: about $(( 50 + 20 * t )) 401s"
```

About the same split as step 2's api line at the same pace (`401`s about `50 + 20 × t` for the
seconds this burst took, the rest `429 retry-after=1`).

```bash
# 3. No line of either service holds your address (D222). Cloud Run's own request log
#    (run.googleapis.com/requests) does hold it, in httpRequest.remoteIp: that is Google's log, and
#    the filter leaves it out. Only the services' stdout and stderr are ours.
MYIP=$(dig +short -4 myip.opendns.com @resolver1.opendns.com); echo "$MYIP"   # your IPv4 address
F='resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND (logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout" OR logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstderr")'
L=(--project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1h --limit=20000)
gcloud logging read "$F AND jsonPayload.code=\"ip_limit\"" "${L[@]}" --format='value(resource.labels.service_name)' | sort | uniq -c
#   about as many as step 2's 429s, on api and on ingest: the refusals are on the access lines
gcloud logging read "$F" "${L[@]}" --format=json | grep -c -F "$MYIP"           # 0
gcloud logging read "$F" "${L[@]}" --format=json | grep -c -F "${MYIP%.*}."     # 0 (the /24)
unset MYIP F L
```

The same in Logs Explorer, on `ryft-logs` (project `ryftlab-prod`, the bucket's `_AllLogs` view),
over the last hour, with your address in place of `203.0.113.7`. The quoted string on its own
searches every field:

```text
resource.type="cloud_run_revision"
(resource.labels.service_name="api" OR resource.labels.service_name="ingest")
(logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstdout" OR logName="projects/ryftlab-prod/logs/run.googleapis.com%2Fstderr")
"203.0.113.7"
```

Expected: no entries. Drop the last line and add `jsonPayload.code="ip_limit"`: you get the
refusals, each with `method`, `path`, `status` 429, `code`, `request_id` and no address.

**Stop if:**
- **No `429` at all.** The limit isn't running: check step 1 and that the revision is the merge's.
  Or the burst was too slow: when the printed `50 + 20 × t` is 300 or more (about 24 a second or
  less, for each instance), nothing is refused. Run it again from a wired connection.
- **2b refuses nothing, or far less than step 2.** A forged entry became the key: anyone can dodge
  this limit and `/v1/admin`'s (which read the header the same way before) by sending a new address
  each time. Report it with the two splits (no address needed): a PR that reads the header the way
  Google writes it follows. A revert doesn't help here.
- **`429`s from the start, far fewer than 50 `401`s, or `after 3 s` still `429`.** Every client shares
  one key. Run step 9's forged-entry loop and its `client address` query. `picked` `fallback` means
  the hop count is above what Google appends. `xff_index` 4 or more means a Google hop `ClientKeys`
  doesn't skip. Either way testers share 20 a second, so revert the PR (**Deploy, roll back, rotate
  → By revert**) and report it.
- **Any `grep -c` above 0.** A line holds your address. Find it (the same read, `--format=json`,
  `grep -F`), don't copy it anywhere, and revert. That breaks the privacy text in force.

**Behind the external Application Load Balancer** (the Cloud Armor PR, not built). That PR sets
`XFF_TRUSTED_HOPS=2` in `deploy/run/config.env` **and** ingress `internal-and-cloud-load-balancing`
on api and ingest, in the same PR. The LB appends `<client>,<lb-ip>`. With ingress `all`, a caller
that reaches `run.app` or a domain mapping directly would get its own forged entry taken as the key,
and `make cloudrun-check` refuses that combination. After its deploy:
- step 9's forged-entry loop through the LB's host: `picked` `xff`, `xff_index` 3, `xff_entries` 5;
- this burst test through it;
- a direct request to the service's `run.app` URL is refused by Google (ingress).

Undo: revert that PR, which sets both back together.

## Rescoring

**After a scoring rule or constant changed** (`SCORING.md`), history is rescored per account:
`POST /v1/recompute` with that account's own access token (API.md; up to 400 days, one request a
minute), as in **Upgrading** below. Cloud Run has no bulk runner: `scripts/rescore_all.sh` runs
`scoring rescore` through kubectl only, and no Cloud Run job runs the subcommand (D246). Locally:
`RESCORE_LOCAL=1 scripts/rescore_all.sh 30` (each user's last 30 days, oldest first; the log shows
`user rescored … n of N`, then `rescore done`).

Days scored before their history (a backfill newest first, a late sample) need no rescore by hand:
scoring re-scores the days after a changed day itself, a retried event included (log `rescored days
after a history change`), and its tick's scoring pass picks up about 50 days a pass whose
`baseline_days` (or one of its two counts) is behind their stored history or whose baseline is
missing (log `rescored days behind their history`). A user whose re-score there failed is left until
their next local day (the error is logged). Only users who synced in the last 48 hours get the pass;
the others get it with their next sync, or now with `POST /v1/recompute`. A scoring letter replayed
with `scripts/dlq.sh replay` (**GCP → Dead letters and the outbox**) arrives as a redelivery: like a
retry, it re-scores the days after its days and finishes what its failed attempts began, so no
recompute is needed after a replay.

**Learned sleep baseline** (`SCORING.md` → Sleep): scoring's tick estimates it every Sunday from
04:00 local on its own. Running it for every user at once (`scoring learn-baselines`) has no Cloud
Run runner either (D246). Locally: `podman compose exec -T scoring /app learn-baselines`. Its log
ends with `learn-baselines done` and the counts `applied` (the baseline moved; that user's last
7 days were rescored), `kept`, `insufficient` and `failed`. Users who set their baseline by hand
keep it.

## Beta

Friends on TestFlight (D78–D83). Everything here uses the api's `ADMIN_API_KEY` and the `adm`
helper of **GCP → The beta gate: warn, then on → 1** (the key from Secret Manager, never on screen
or argv); `API=https://api.ryftlab.de` comes with it. Locally: `API=http://localhost:8082` and
`ADMIN_API_KEY=dev-admin-key-change-me-0123456789abcdef`.

**Consent.** `POST /v1/sync` answers `403 consent_required` until the account consented
to the current `PRIVACY_POLICY_VERSION` (`deploy/run/config.env`, which api and ingest read;
`2026-10-26` today), and scoring computes nothing for an account without consent. Nothing was
backfilled, so the operator's own account consents once too: with the app build that has the
consent screen, or by hand with its access token:

```bash
curl -s -X POST -H "Authorization: Bearer $TOKEN" $API/v1/me/consent -d '{"policy_version": "2026-10-26"}'
```

**A new policy text** (only on the user's decision: everyone consents again) is **one
PR**: `web/content/legal/en/privacy/<version>.mdx` (the old versions stay; the site is English
only, D230), `<version>` appended to `web/content/legal/privacy-versions.json`, and
`PRIVACY_POLICY_VERSION` in `deploy/run/config.env` and `docker-compose.yml`. The merge puts it into
force: `deploy.yml` rolls api and ingest with the new value, and its `site` job ships the page and
checks that the live `/privacy` shows it (**GCP → Deploy, roll back, rotate → The site**). CI's
`manifests` runs `node web/scripts/check-policy-version.mjs --strict` (published version, pages,
compose) and `scripts/check_policy_in_force.sh` (the version in `deploy/run/config.env` is listed,
has an English page, and on a pull request that page is `status: published`).

What to watch after the merge's deploy (read-only, from the repository root):

```bash
for s in api ingest; do
  echo "$s $(gcloud run services describe "$s" --region=europe-west1 --project=ryftlab-prod --format=json \
    | jq -r '.spec.template.spec.containers[0].env[] | select(.name == "PRIVACY_POLICY_VERSION") | .value')"
done                                                    # the new version on both
gcloud logging read 'resource.type="cloud_run_revision" AND (resource.labels.service_name="api" OR resource.labels.service_name="ingest") AND jsonPayload.message="beta"' \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1h --limit=4 \
  --format='value(timestamp,resource.labels.service_name,resource.labels.revision_name,jsonPayload.gate,jsonPayload.consent)'
make smoke-prod
```

While api and ingest roll, an old instance may still serve the old version for a minute:
`consent_required` refusals bump briefly and recover. The deploy's smoke consents through api and
syncs through ingest, so it proves the two agree.

Withdrawing a version. A policy PR cannot be reverted as a whole (the versions list is
append-only, and the site's `/privacy` serves the newest page). A withdrawal is a NEW version (the
corrected text, appended), never a revert. On a pull request `check_policy_in_force.sh` refuses a
version that becomes the one in force unless its (English) page says exactly `status: published`
(read as the site reads it; a tag, flow frontmatter or a block scalar refuse even when published).
Anything else is a draft for this check. Going back to a version listed before the one in force now
is allowed. So a policy PR stays red until `status: published` is set in that same PR. A text that
is published cannot be un-published in place: the check also refuses a PR that sets a published
page back to a draft. Publish a new version instead.

Every tester consents again before the next sync; the access lines' `code="consent_required"`
(Logs Explorer, as in **GCP → The beta gate: warn, then on**) show who has not yet.

**The gates and their modes** (D87). Two checks, each `off`, `warn` or `on`: `BETA_GATE` in
`deploy/run/config.env` (a PR, **GCP → The beta gate: warn, then on**); `CONSENT_ENFORCE` is not set
there, so the services' default, `on`, holds:

| Variable | Checks | Default | `on` | `warn` |
| --- | --- | --- | --- | --- |
| `BETA_GATE` | a redeemed code or an operator grant (api, ingest) | `off` | `403 invite_required` everywhere but onboarding | served, `X-RYFT-Gate: invite_required` |
| `CONSENT_ENFORCE` | a consent to the current policy (ingest, scoring) | `on` | `403 consent_required` on sync, nothing scored | synced and scored, `X-RYFT-Gate: consent_required` |

`warn` is for before the beta opens, while the app cannot pass a gate yet (it has no
invite or consent screen): the operator's own account keeps working, and every request
that would have been refused is flagged on its access line (`gate`; `ryft_gate_refusals_total`
is counted but nothing reads it on Cloud Run, D236). **Before the first external invite both are
`on`, and `make smoke-prod` proves it:** with `ADMIN_API_KEY` it takes the smoke user's consent
and access away for one request each, reports what each gate did, and gives them back;
`SMOKE_EXPECT_GATES=on make smoke-prod` fails unless both refused. *D238 amended this (the
invite gate `warn` while the first testers redeem, then `on`), and D241 amends it again: the invite
gate is `off`, with no codes for the beta, and consent stays `on`. A mode changes by a PR of
`deploy/run/config.env` and its deploy, never straight to `on`: GCP → **The beta gate: warn,
then on**.*

Accounts that existed at migration 0017 keep access. One account without a code gets it
from the operator (audited, D88):

```bash
adm -X POST   "$API/v1/admin/users/<id or first 8 digits>/access"   # 204
adm -X DELETE "$API/v1/admin/users/<id>/access"                     # takes it away
adm "$API/v1/admin/audit?since=2026-10-21" | jq                     # who did what, when
```

```bash
# A code for one person (or max_uses for a group), optionally expiring:
adm -X POST "$API/v1/admin/invites" \
  -d '{"note": "TestFlight wave 2", "max_uses": 20, "expires_at": "2026-12-31T23:59:59Z"}' | jq -r .code    # RYFT-7K3M-QX9P
adm "$API/v1/admin/invites" | jq                                     # status and uses so far, emails masked
adm "$API/v1/admin/invites?emails=1" | jq '.[] | {code, status, email, uses_by}'   # who, with addresses
# REVOKE (since 2026-11-05 DELETE no longer removes the row): the code stays listed as "revoked", nobody redeems it
# any more, accounts that used it keep their access. 204 again if it was revoked already; 404 for an unknown code.
adm -X DELETE "$API/v1/admin/invites/RYFT-7K3M-QX9P"
```

**A code for one person (bound to an email).**

```bash
adm -X POST "$API/v1/admin/invites" \
  -d '{"note": "Anna", "email": "anna@example.com", "expires_at": "2026-11-19T10:00:00Z"}' | jq '{code, link, expires_at}'
```

It is single use, **expires** (14 days when `expires_at` is left out, at most 90 days away) and only the account whose
**Supabase-confirmed** email is that address can redeem it (D159). Use the address the tester signs in with; a Sign
in with Apple "Hide My Email" address matches only a code made for exactly that relay address, so leave `email` out
when you do not have it. Everyone else gets the same `422 "invalid or used code"` as for a wrong code, and a tester
whose account lookup fails at Supabase gets `503` and sends again (a log line `invite email lookup failed`; unbound
codes never depend on it). The address and the note are cleared 30 days after the code stopped being redeemable (D161;
the api's tick does it, and a failure shows as its `result` error, **GCP → When an alert fires** → Tick missing); a person's name does not belong in the note of an
unbound code, which is never cleared and has no hard delete (SQL: `UPDATE beta_invite_codes SET note = '' WHERE
code = 'RYFT…'`).

**Before the first bound code (security-critical, two checks).**

1. **Supabase → Authentication → Sign In / Providers → Email: *Confirm email* ON and *Secure email change* ON**
   (SUPABASE.md → checklist 3). With *Confirm email* off, Supabase marks a sign-up's address confirmed at once, and
   anyone who has a leaked link could sign up with the tester's address and redeem the code. Record the state you saw
   and the date in STATUS.md.
2. **The api agrees:** the newest `invite binding` line says `state: ok` (read-only):

   ```bash
   gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="api" AND jsonPayload.message="invite binding"' \
     --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1d --limit=3 \
     --format='value(timestamp,resource.labels.revision_name,jsonPayload.state)'
   ```

   The api reads the project's public setting `mailer_autoconfirm` at start and on its tick, and while it is true,
   unreadable or the api has no Supabase admin key, `POST /v1/admin/invites` with an `email` answers `422` with the
   reason and a bound code cannot be redeemed (the same single `422`; `503` while the setting is unreadable). That is a
   safety net, not the check: do check 1 yourself.
3. **The privacy text names the stored address** (PRIVACY_POLICY_VERSION, a separate PR, decision U3): until it is
   published, no bound codes in production.

**Whose data is broken.** `scripts/beta_report.sh` prints one line per tester from
`GET /v1/admin/users`: short id, created, provider, beta access, consent version, app
version, last sync, sources, days scored / nights with stages / days with HRV in the
last 14 (UTC) days, calibrating, max-HR source, open feedback. Signs: `sources` without
`hr` (strain and recovery stay empty; the app shows `no-heart-rate-source`), `staged/14`
at 0 with `sleep` in the sources (a tracker writing only "asleep"), `scored/14` well under
the days since the first sync (syncs failing: `consent_required`, or the app's background
refresh). Emails only with `--emails` (`?emails=1`); every call is logged with the admin
key's fingerprint (`admin_key`).

```bash
scripts/beta_report.sh
scripts/beta_report.sh feedback 2026-10-20     # reports since a day
adm -X POST "$API/v1/admin/feedback/42/resolve"    # no longer open
```

**The scripts take the admin key from the environment only** (`scripts/lib/admin.sh`):
`ADMIN_API_KEY` exported as in **GCP → The beta gate: warn, then on → 1**; without it they say how
to read it from Secret Manager and stop. `API_URL` defaults to production.

### Admin sign-in

`/v1/admin/*` takes two bearers (docs/API.md → Admin routes): the key above (scripts, smoke;
compared first, in constant time, independent of Supabase), or the Supabase access token of
an operator, for the operator's web page. A token is accepted only when **all** of these
hold: `app_metadata.role` is `"admin"` (never the top-level `role`, never `user_metadata`,
which the user can write), the user has a row in `admin_accounts` and no tombstone, and the
token is `aal2` with a TOTP at most 12 hours old. Everything else is one identical `401`
(the reason is only in the log: `admin auth rejected`, `reason` and the access line's `code`).
One exception, plainly: a token whose key id the api does not know, while the signing keys were
fetched less than a minute ago, answers `503` (`jwks_refresh_limited`, `Retry-After: 5`), not `401`.
Anyone can send such a token, so a `503` on an admin route can be provoked and is not by itself an
outage: it means the api could not complete a key fetch (or the allow-list check) for that token,
which includes a fetch that ran past its 1 s. It is kept on purpose (D184: a `401` there was
considered and rejected). A client that hangs up no longer counts as a failed fetch (D184), so it
cannot turn later answers into `jwks_unavailable`. A real operator without a fresh second factor gets `403 mfa_required` (and an audit row);
an operator's token on merge, flag writes, user detail or resync-hint gets `403` (key-only routes,
audited). The token's answers for the users list and the feedback are a projection without the
health-derived, device and diagnostic fields (API.md); the key gets the whole answers.

**What changes on deploy, and for whom.** The token path admits nobody until an `admin_accounts`
row exists *and* the account has the role, and CORS is off while `ADMIN_WEB_ORIGINS` is empty.
For key callers (scripts, `make smoke-prod`) three small things change: more than 10 requests a
second from one address now get `429`, the `401` text is `admin key or admin sign-in required`,
and the scheme `bearer` in lower case is accepted. Granting access is two statements in the
SQL editor, as the owner, by id; they are one trust domain (the Supabase project owner), and the
row exists so that revoking takes effect at once, not so that two parties must agree. **An alert
notices each admin session begun** (D244: the sixth condition, **RYFT · Feedback and admin access**,
kind `admin signed in`). The api writes `admin signed in` once per operator session per instance,
when a token passes with a TOTP at most 10 minutes old: a sign-in on the page, or its re-challenge
after 12 hours. One that wasn't you is the lost-phone case (below). It doesn't see a token already in
a session (a stolen access token whose TOTP is older) or the key's use. So still, after any session
and weekly while a row exists, read the audit (`GET /v1/admin/audit`, `actor.kind` `user`) or Logs
Explorer on `ryft-logs`:

```text
resource.type="cloud_run_revision" resource.labels.service_name="api" jsonPayload.admin_actor:"user:"
```

Every line there that isn't yours is the lost-phone case (below).

```sql
update auth.users set raw_app_meta_data = coalesce(raw_app_meta_data, '{}'::jsonb) || '{"role":"admin"}'::jsonb where id = '<admin uuid>';
insert into public.admin_accounts (user_id, note) values ('<admin uuid>', 'operator') on conflict do nothing;
```

A role set this way appears only in tokens issued after the update: sign in again on the page.
The api only reads `admin_accounts`; it is operator configuration, not user data (account
deletion does not touch it; a deleted account's tombstone refuses the row). Which pages may
read the answers: `ADMIN_WEB_ORIGINS` in `deploy/run/api.yaml` (a comma-separated
list of exact origins, `https://ryftlab.de` today; empty means no browser page can, scripts are
unaffected; a malformed value stops the api at start-up, before it waits for the database, and
`make cloudrun-check` holds the committed value to `scripts/check_cloudrun.py`'s). Every `/v1/admin/*` request but one bearing `ADMIN_API_KEY` is
limited per client address (10 a second, bursts of 20, per instance; the address is
`httpx.ClientKeys`'s, the `XFF_TRUSTED_HOPS`-th `X-Forwarded-For` entry from the right that isn't
Google's front end, kept as an HMAC; never logged; the map holds 4,096 addresses, newer ones share
one bucket, D222, D240). It sits behind the per-address limit of every route (GCP → The per-IP
limit), which the key passes too. The key passes before
the limiter, so a flood of new addresses can't lock the break-glass key out. Every admin log line
carries `admin_actor` (`key:3fa1c2d9` or `user:1b2c3d4e`,
the first 8 hex digits). The audit (`GET /v1/admin/audit`) names the actor: `actor.kind` `user` or `key`.

**Setting up the operator account (once, in this order).** Before step 1 the Supabase project must be set as
SUPABASE.md → checklist says for the admin page (Data API off, TOTP on, maximum enrolled factors 1, *Confirm
email* and *Secure email change* on). Read the paragraph above about noticing a use first: since D244 the
sixth condition notices each session begun, and not every use.

1. **A dedicated admin account:** Supabase → Authentication → Users → *Add user*, an address used for nothing
   else and **not** your Apple ID address (Supabase links identities of the same verified email, so an app sign-in
   with Apple could land in it), a long random password from the password manager, *Auto confirm*. Note its UUID.
   Grant nothing yet.
2. **Enrol the TOTP on the live page:** open `https://ryftlab.de/admin`, sign in, scan the QR code (or open the
   `otpauth://` link; the page has no copy button for the secret on purpose) and enter the code. The page then
   says "This account is not an operator account." and ends this session only (`scope=local`): expected, the
   account has no role yet.
3. **V-MFA-1** (blocking): with an **aal1** session of this account, which now has a verified factor, enrolling
   a second factor must be refused. **Which refusal you get depends on the factor limit**, because GoTrue checks
   the count first (`validateFactors` in its `internal/api/mfa.go`, as read in the review of #139; not run against
   the project here):
   - *Maximum enrolled factors* is **1** (the setting this page needs): **pass** = `422`, `error_code`
     `too_many_enrolled_mfa_factors`. That shows the limit holds; it does not reach the aal check.
   - The limit is **2 or more**: **pass** = `403`, `error_code` `insufficient_aal` (the aal check itself).
   - `200`: **fail**. Stop, delete the factor the answer names (below), and do not grant the role.
   - Anything else (a `401` from a failed sign-in, a `429`, a `5xx`, another `error_code`): **inconclusive**; fix
     the cause and run it again.

   Optional, to test the aal check itself: set the limit to 2, run the probe (expect `403 insufficient_aal`), set
   it back to 1 and run it again (expect `422 too_many_enrolled_mfa_factors`). Either way below is enough:
   - Devtools on `https://ryftlab.de/admin`: sign in with the password and stop at "Enter your code" (the tab now
     holds the aal1 session), then in the console (the page's CSP allows exactly this origin and path; put in the
     two values of `web/config/admin.json`):
     ```js
     (async () => {
       const s = JSON.parse(sessionStorage.getItem('ryft.admin.session') ?? 'null');
       if (!s?.access_token) return console.log('no session in this tab: sign in and stop at "Enter your code"');
       const r = await fetch('<supabaseUrl>/auth/v1/factors', {
         method: 'POST',
         headers: {
           apikey: '<supabaseKey>',
           Authorization: `Bearer ${s.access_token}`,
           'Content-Type': 'application/json',
         },
         body: JSON.stringify({ factor_type: 'totp', friendly_name: 'v-mfa-1-probe' }),
       });
       console.log(r.status, await r.text());
     })();
     ```
     After a `200`, delete it from the same console with `fetch('<supabaseUrl>/auth/v1/factors/<id>', { method:
     'DELETE', headers: { apikey: '<supabaseKey>', Authorization: `Bearer ${s.access_token}` } })` (read `s` again
     as above).
   - `curl` from the repository root. The password is read without echo, stays out of the shell history and off
     every command line (`printf` is a shell builtin; `jq` reads it from stdin):
     ```bash
     KEY=$(jq -r .supabaseKey web/config/admin.json)
     AUTH=$(jq -r .supabaseUrl web/config/admin.json)/auth/v1
     read -r EMAIL; read -rs PASSWORD
     TOKEN=$(printf '%s' "$PASSWORD" | jq -Rs --arg e "$EMAIL" '{email: $e, password: .}' |
       curl -s "$AUTH/token?grant_type=password" -H "apikey: $KEY" -H 'Content-Type: application/json' --data-binary @- |
       jq -r .access_token)
     unset PASSWORD
     if [ -z "$TOKEN" ] || [ "$TOKEN" = null ]; then
       echo 'sign-in failed: no token, so the probe would prove nothing'
     else
       curl -s -w '\n%{http_code}\n' "$AUTH/factors" -H "apikey: $KEY" -H "Authorization: Bearer $TOKEN" \
         -H 'Content-Type: application/json' -d '{"factor_type":"totp","friendly_name":"v-mfa-1-probe"}'
     fi
     # only after a 200, before the unset: the id is the answer's "id"
     # curl -s -X DELETE "$AUTH/factors/<id>" -H "apikey: $KEY" -H "Authorization: Bearer $TOKEN"
     unset TOKEN
     ```
   Record the answer (status and `error_code`), the factor limit it ran under and the date in STATUS.md.
4. **Grant the role and the row** with the two SQL statements above (as owner, by id).
5. **Sign in again** on the page (the role is only in tokens issued after the update): password, then the code;
   the Invites list loads.

**Using the page.** The session lives in that tab's `sessionStorage` and is cleared when the tab leaves `/admin`
(web D16): closing the tab, going to another page, typing a URL **and a browser reload or pull to refresh** sign you
out (password and code again, about 20 seconds); switching sections (`#users`, `#audit`) does not. The page asks for
the code again when the api answers `mfa_required` (the TOTP is older than 12 hours; that path is tested against
the e2e stub only, **not verified** on the live project). **Sign out ends every session of that account
(`scope=global`)**, not only this tab (access tokens already issued live out their hour, so revoking at once is the
`admin_accounts` row); so does *Use another account* on the code screens. That other devices are signed out is
GoTrue's documented behaviour, **not verified** here. "Not an operator account" and a second `401` end only the page's
own session (`scope=local`). The Audit tab shows the admin calls of the last 7 days (read-only); flag changes, merge,
user detail and resync-hint stay with the key and the scripts. A lost phone: the runbook below (**not verified**: no run of
it is recorded).

**Evidence that the page works in production:** merged (#128), pinned (#138, web `bb74830`), confirmed by the owner on
2026-11-05 (pasted output, on EKS then): `/admin` answered `200` with the admin CSP and `Cache-Control:
no-store`, `/` kept `connect-src 'none'`, the api ran with `ADMIN_WEB_ORIGINS` (`"web_origins":1`, the `OPTIONS`
preflight `204` with `access-control-allow-origin: https://ryftlab.de`), and the operator's sign-in on the page worked.
On Firebase Hosting the site job's live check covers the headers (**GCP → 10g**); the sign-in is not re-recorded.

**Alerts.** On GCP one since D244: **RYFT · Feedback and admin access** (GCP → When an alert
fires), whose kinds `admin signed in` and `admin auth rejected` stand in for Grafana's two rules
that D236 dropped. `AdminWebUsed` was the page being used; now each session begun is one notice
(`admin signed in`, no key of its own on the line). If it was not you, act (below).
`AdminUnauthorized` was more than 20 requests with a bad bearer in 10 minutes, a scanner or a stale
or foreign token; now any one `admin auth rejected` notifies, but for `reason` `missing` (no bearer:
crawlers) and `expired` (your own page with an old token; it refreshes once), and stays one
incident while they keep coming. The api never sees a guessed password, that is Supabase Auth: read
Supabase → Logs → Auth for sign-in attacks. A real operator refused for want of a fresh second
factor (`mfa_required`, the line `admin second factor required`) is deliberately not counted; it is
in the audit (`actor.kind` `user`, status `403`) and in the logs. A `503` on an admin route with a token
means the api could not complete the check: a signing-key fetch that failed or took longer than a
second, the allow-list lookup in the database, or an unknown key id inside the refresh limit
(above). It is not proof of an outage (`Retry-After: 5`, D184; the 5xx policy counts it); the key
keeps working. Logs Explorer on `ryft-logs`, each with
`resource.type="cloud_run_revision" resource.labels.service_name="api"`:

```text
# refused bearers (jsonPayload.reason: missing, not_a_token, expired, signature, unknown_key, not_admin, not_allowed, …)
jsonPayload.message="admin auth rejected"
# who passed with a token (the page was used): every admin line names its actor
jsonPayload.admin_actor:"user:"
# a real operator without a fresh second factor (a stolen password with no second factor looks like this)
jsonPayload.message="admin second factor required"
# signing keys or the allow-list check unreachable
jsonPayload.message="admin auth unavailable"
# the audit row could not be written (the call was answered; GET /v1/admin/audit lacks it)
jsonPayload.message="admin audit write failed"
# the api refused to start over its admin settings (a malformed ADMIN_WEB_ORIGINS)
jsonPayload.message="admin config"
```

Counts by reason in a shell, without ids on screen:

```bash
gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="api" AND jsonPayload.message="admin auth rejected"' \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1d --limit=10000 \
  --format='value(jsonPayload.reason)' | sort | uniq -c
```

**The api fails to start after an `ADMIN_WEB_ORIGINS` change** (the `admin config` line above,
`severity` ERROR): the new revision never gets ready, and the deploy stops with the previous one
serving. Revert the line in a PR (**GCP → Deploy, roll back, rotate → A rollback**, by revert).
**A flood of `429`s on `/v1/admin/*`:** the access lines with `jsonPayload.status=429` on those
routes (`code` `ip_limit` is the per-address limit of every route, **GCP → The per-IP limit**; none
is the admin limit's); the limit already caps what one address costs (no token check, no database
call). If it persists, a deny is a Cloud Armor rule in `infra/gcp/edge.tf` (a PR
and the user's apply, **GCP → 16**); nothing does it automatically.

**Lost phone, or any doubt about the admin account** (immediate, in this order):

1. SQL editor, as owner: `delete from admin_accounts where user_id = '<admin uuid>';` — takes
   effect on the next request, whatever the token's lifetime.
2. Supabase → Authentication → Users → the admin account: *Ban user*, so no new token is issued
   (verify in the dashboard that the ban is shown; tokens already issued live out their hour, which
   is why step 1 comes first). Ending its sessions as the owner is `delete from auth.sessions
   where user_id = '<admin uuid>';` — check the table and the column in the dashboard's schema view
   first. Then remove the MFA factor and change the password (also in the dashboard).
3. What happened since: `adm "$API/v1/admin/audit?since=<day of the loss>" | jq '.[] | select(.actor.kind=="user")'`
   (the rows with status `403` and a user actor are refusals for want of a second factor).
4. To restore: lift the ban, enrol again from a trusted device, sign in again (a new `app_metadata.role` only
   appears in tokens issued after the update), then insert the row again.

**Tracing a request** a tester reports by its id (14 days back, the bucket's retention):

```bash
RID=01a15a69-7142-7a8b-aba2-c099a5fcd1e4      # a lost hyphen (01a15a69-71427a8b-…, as the app once showed it): put it back first
gcloud logging read "resource.type=\"cloud_run_revision\" AND jsonPayload.request_id=\"$RID\"" \
  --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=14d --order=asc \
  --format='value(timestamp,resource.labels.service_name,jsonPayload.message,jsonPayload.route,jsonPayload.status,jsonPayload.code,jsonPayload.gate,jsonPayload.err)'
```

Every line of api, ingest, scoring and notify that carries the id, oldest first (for scoring's
`scored day` add `jsonPayload.day,jsonPayload.model_version,jsonPayload.inputs_changed`). The line's
`user_id` names the account: read it in the expanded entry, then `scripts/user.sh <id>` (below).
This query replaces `scripts/trace.sh`, which read a cluster's pod logs and went with D3 (D246).

**One account:** `scripts/user.sh <id | first 8 digits | email> [--emails]` shows the
detail below as a page; `--grant` / `--revoke` set beta access without a code;
`--resync-hint` asks the app for a full re-sync (it sees `resync_requested_at` in
`GET /v1/me/sync` and re-reads its HealthKit window without anchors), for an account
whose phone has data the server lacks; `--merge-into` below; `--json` the raw answer.

**One account in detail** (`GET /v1/admin/users/{id or first 8 digits}[?emails=1]`, D89):
how it signs in (every identity from Supabase: Apple, email; whether the email is
Apple's "Hide My Email" relay; the last sign-in), beta access, the consent with the app
version it came from, `max_hr` and its source; every device with app and iOS version,
first and last sync, what the last sync carried, the push environment and its last
delivered push; the data of the last 14 days, the newest record per kind, open feedback;
and **possible duplicates**: other accounts that synced from one of its devices in the
last 30 days (`dup` in the list). Emails are masked (`t***@g***.com`) unless
`?emails=1`; every read is audited.

```bash
adm "$API/v1/admin/users/53d437eb" | jq
```

**Feedback** (`POST /v1/feedback`, 10 per tester a day) carries `request_ids`: trace each one
(above) to see what the tester saw. The sixth condition tells you of each one (D244); read them
with `scripts/beta_report.sh feedback <day>` (**GCP → 14a.8**).

## Two accounts for one person

**How it happens:** someone signs in with email and later with Apple (or the other way
round). Supabase links the two only when Apple hands over the same verified email; with
"Hide My Email" it is a relay address, and a second account appears. The app syncs into
whichever is signed in, so the data splits.

**How to see it:** `scripts/beta_report.sh` → `dup` (accounts that synced from the same
device id in the last 30 days); `scripts/user.sh <id>` names them under "possible
duplicate", with provider, last sync and days scored. The app is told too
(`GET /v1/me` → `account_hint`, API.md).

**How to merge** (D90): the smaller account into the one with the history. The plan
first, then the merge, which moves everything in one transaction, rescores the kept
account over the moved days, and deletes the other one like `DELETE /v1/me` (Supabase
sign-in and Apple grant included):

```bash
scripts/user.sh <from> --merge-into <to> --dry-run      # per table: moved, dropped (to's row kept), and why
scripts/user.sh <from> --merge-into <to>                # shows the plan again, asks for MERGE
```

Where both accounts have the same row (a sample, a device id, a session id, a journal
day, a custom exercise's slug), the kept account's wins and the other's is counted as
dropped. A consent and beta access are copied only when the kept account has none.
Merging the account with more scored days into the smaller one is refused (`409`) unless
`--force`. A second run answers `404` (from is gone). Both accounts may have held copies of one
workout under different ids; new syncs fold them on their own, but there is no Cloud Run runner for
`ingest fold-workouts`, the one-off for what is already stored (D246). Afterwards, the person signs in the
way the kept account uses; if both identities should open it, they link Apple to the
email account in the app (Supabase **manual linking**, `SUPABASE.md`).

**Imported training history and a merge.** The app derives the training import's ids from the
account's id (API.md → `POST /v1/train/import` → For the app). The merge moves imported
sessions and plans with the ids they have; an import of the same file into the kept account
would derive other ids and store that history a second time. So after a merge the person must
not import the same file again: tell them when you merge. A server-held import namespace that
the merge carries over is an open point (D141).

## The coverage gate went red

`coverage-gate` (the `test` job of a pull request, D148) says "internal/ coverage fell from X% on main to Y%".
The job summary lists every package; compare it with the `coverage` artifact of the latest push run on main to see
which package lost. Usual causes: code added without a test (add the test), a test that needs Postgres
that skipped (look for a service container that did not start, then re-run the job), or main moved: the baseline
is main's tip, so rebase first. A drop that is really intended (code deleted together with its well-tested
siblings) is accepted by raising `MAX_DROP` in the PR's own `ci.yml` for that PR, with the reason in the
description; there is no other override. A warning "main's coverage is unknown" is not a failure: the gate passed
without a baseline (artifact outage or expired); re-run once the artifact is back. Locally: `make coverage`.

## The vulns job went red

`vulns` (D229, required by `ci-ok`) has two steps, and both run even when the first is red.
- **govulncheck** names each vulnerability the code reaches, with a trace from our code ("Example traces found") and
  "Fixed in". For a module, bump that module alone (`go -C backend get <module>@<fixed version>`, then
  `go -C backend mod tidy`). The PR names the GO- (and CVE-) IDs. For the standard library, the fix is the Go of
  `backend/Dockerfile`'s build stage: a new digest of the same tag (Dependabot's docker PR), or a new Go line once
  the old one gets no more fixes (Go fixes the two newest; 1.25 → 1.27 in D229). For a new line, put the tag
  alone in the Dockerfile and push. The job pulls it, prints its `Digest:`, and fails. Pin `tag@digest`, and set
  go.mod's `toolchain` line to the Go version the next run's govulncheck names ("the goX.Y.Z standard library").
  govulncheck's version must build with that Go (`ci.yml`; v1.9.0 needs 1.26).
- **Trivy** is the deploy gate's own rule (critical, with a fix) on `backend/go.mod`: a module that would stop
  `deploy.yml` after the merge stops the PR now. Fix it the same way, whether govulncheck reaches it or not.
- **"go.mod requires go >= …"**: a module bump (Dependabot's gomod group, say) raised go.mod's floor above the
  image's Go. The fix is a newer Go line in the image, as above, never `GOTOOLCHAIN` in the job.

The job also runs on every push to main. A vulnerability published after a merge turns the next push's `ci-ok`
red, so `deploy.yml` deploys nothing until the fix is merged. Never skip the job or loosen either step to get a deploy
through. Locally, as CI runs it (the image from the Dockerfile's build stage):
`docker run --rm -v "$PWD/backend:/src:ro" -w /src <golang:…@sha256:…> go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...`
(Podman: the same with `podman run`). `go -C backend run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...` judges
your own Go's standard library instead.

## When a tester's scores do not appear

What the app sees is `GET /v1/me/sync` (D91): which gate it is behind, the last sync and
device, the newest record per kind, today's state (`pending`: synced, scoring has not
finished; `waiting_for_sleep`: no night yet; `stale`: nothing for 20 hours; `scored`),
the days still queued and how long scoring takes now (`expected_within_sec`, the p95 of
the last 6 hours). The operator sees the same and more:

```bash
scripts/user.sh <id>                 # devices, what the last sync carried and ignored, days, duplicates
```

and the sync's request id through **Beta → Admin sign-in → Tracing a request** (the sync's status,
code and gate, then scoring's lines for the same id).

| Sign | Means | Do |
| --- | --- | --- |
| sync `403`, `code` on the access line | a gate refused (`consent_required`, `invite_required`) | the gate's mode (Beta), the account's consent and access in `user.sh` |
| last sync carried 0 accepted, many `ignored.*.duplicate` | the phone re-sends what the server has | nothing; new data comes with the next sync |
| many `ignored.*.unknown_type` / `outside_window` | an app build sending a type the server does not know, or a clock far off | the app's version in `user.sh`; the API's sample types |
| days stay `pending` for minutes | scoring is behind or failing | the policies Event backlog and Dead letters held (**GCP → When an alert fires**), **GCP → Dead letters and the outbox**; scoring's lines for the id |
| no sync for days, the phone has data | the app's background sync or its anchors | `user.sh <id> --resync-hint`: the app re-reads its window without anchors |
| scored, but the app shows yesterday | the silent refresh did not arrive (iOS budgets them) | notify's lines in Logs Explorer (`resource.labels.service_name="notify"`) around the scoring time; the app polls `GET /v1/me/sync` too |

## Training import

`POST /v1/train/import` (D124-D133, API.md) stores plans and finished sessions that the
app parsed from another training app's export, each item in its own transaction, then
queues the days with one `ingested` event (`reason` `import`) for scoring, then
recomputes the records. Imported sessions send no `SESSION_SCORED` or `PR` push and move no
enrolment. The switch is the flag `train.import` (dark by default: testers by the allow-list,
Feature flags); the app hides the import while it is off, and the endpoint checks it too
(`403 import_not_enabled`), so the kill switch does not wait for the app's cached config.
A resend never overwrites what the user changed in the app since (`newer_on_server`, D141).

**Beta → Admin sign-in → Tracing a request** shows the access line and the import's one line
(`jsonPayload.message="train import"`: `result`, the items sent and their statuses, the days queued;
never a title, a value, an id list or the source app), then scoring's lines for the same id.

| Sign | Means | Do |
| --- | --- | --- |
| `429`, `code=import_in_progress` | another import of the account holds the lease (`users.import_lease_until`, 70 s, D132) | nothing: the app waits `Retry-After` (10 s) and resends the same request. An instance that died mid-import blocks that account for up to 70 s; the lease expires on its own, do not clear it by hand |
| `503`, `result=timeout` or `error` | the 50 s ran out (the items stop at 40 s), or the database failed; what was stored stays stored and was finished | nothing: the app resends, the stored items come back `unchanged`. Many in a row: the database (the api's `tick` line, step `db ping`; the uptime check `ryft-api-ready`; Supabase's status) and the access lines' `dur_ms` |
| `403`, `code=consent_required` | no consent to the current privacy policy (the import is an upload of health data, D129); on this route `CONSENT_ENFORCE=warn` refuses too (D141) | as for a sync (Beta) |
| `403`, `code=import_not_enabled` | the flag `train.import` is off for the account (`ryft_train_imports_total{result="disabled"}`) | intended for anyone not allow-listed; a tester: add the id to the allow-list (Feature flags) |
| `429` without a `code` | the import's own rate budget: bursts of 5, then one every 5 s (`ryft_rate_limited_total{class="import"}`) | nothing: the app waits `Retry-After` |
| `200` with `rejected` items | items the app must change; the `code` and `message` (ids and field names) are in the response only | the app's report; `ryft_train_import_items_total{status="rejected"}` |
| `days_touched` stay pending | scoring is behind | as for a sync (above) |
| sessions older than 400 days without strain | by design: stored for history and records, never scored (D128) | nothing |

## Feature flags

`GET /v1/config` serves the flags in table `feature_flags` (D69). Change them with the
admin routes and the api's `ADMIN_API_KEY`, with `adm` and `API` from **GCP → The beta gate: warn,
then on → 1** (locally: `ADMIN_API_KEY=dev-admin-key-change-me-0123456789abcdef`):

```bash
adm "$API/v1/admin/flags" | jq            # every flag
# On for two testers only (their user IDs: GET /v1/me → id):
adm -X PUT "$API/v1/admin/flags/today.redesign" \
  -d '{"enabled": true, "rollout_pct": 0, "allow_user_ids": ["<uuid>", "<uuid>"]}'
# 10 % of everyone, then everyone:
adm -X PUT "$API/v1/admin/flags/today.redesign" -d '{"enabled": true, "rollout_pct": 10}'
adm -X PUT "$API/v1/admin/flags/today.redesign" -d '{"enabled": true, "rollout_pct": 100}'
# Kill switch (off for everyone, testers included):
adm -X PUT "$API/v1/admin/flags/today.redesign" -d '{"enabled": false, "rollout_pct": 100}'
adm -X DELETE "$API/v1/admin/flags/some.old_flag"      # 204
```

`PUT` replaces the whole flag (`enabled` and `rollout_pct` required, a missing
allow-list is empty) and answers `201` for a new key, `200` otherwise; the api logs
every change (`msg="feature flag set"`). The app caches its answer for up to six hours,
so a change reaches everyone within that. Rotate the key: **GCP → Deploy, roll back, rotate →
Another secret's pin** (its example is `admin-api-key`).

`analytics.enabled` (D231) ships dark like `train.import`: the app sends product analytics only while
it is on, `GET /v1/config` → `analytics` is set (GCP → **PostHog's project key**) and the user opted
in. Testers first, by the allow-list; `{"enabled": false, "rollout_pct": 100}` is the kill switch.
Nothing on the server checks the flag, so it takes effect at the app's next config.

**Stop if**, before the first account goes on its allow-list, the privacy policy in force (the
version `GET /v1/config` → `privacy_policy_version` names, published at `https://ryftlab.de/privacy`)
doesn't name PostHog (EU Cloud) as a processor, with the purpose and the opt-in, or PostHog's project
doesn't have **Discard client IP data** on. Testers are real people: their events would reach a
processor the published text doesn't name. A quick look: `curl -s https://ryftlab.de/privacy | grep -c PostHog`
must not print `0`.

## Push (APNs)

notify sends with token-based APNs (D73). Until the key exists it runs and counts
`ryft_push_notifications_total{result="unconfigured"}`; tokens and preferences are
stored meanwhile. Once the Apple Developer Program membership is active:

1. Apple Developer → Certificates, IDs & Profiles → Keys → **+**, enable **Apple Push
   Notifications service (APNs)**, download `AuthKey_<key id>.p8` (once). The same key
   serves sandbox and production. The App ID `dev.timkrebs.ryft` needs the Push
   Notifications capability (the app side adds it in Xcode).
2. The .p8 into the secret `apns-key-p8` (**GCP → 5**) and the key's id as `APNS_KEY_ID` in
   `deploy/run/config.env` (**GCP → 7a**); `APNS_TEAM_ID` and `APNS_TOPIC` are in
   `deploy/run/notify.yaml`. A new key's version is a pin move (**GCP → Deploy, roll back, rotate →
   Another secret's pin**). After the deploy notify's log says `apns configured`.
3. Check: register a debug build (`env: sandbox`), then notify's lines after the next morning sync:

   ```bash
   gcloud logging read 'resource.type="cloud_run_revision" AND resource.labels.service_name="notify" AND jsonPayload.message:"apns"' \
     --project=ryftlab-prod --bucket=ryft-logs --location=europe-west1 --view=_AllLogs --freshness=1d --limit=20 \
     --format='value(timestamp,severity,jsonPayload.message,jsonPayload.err)'
   ```

Revoking the key in Apple Developer makes APNs answer `403` (no alert since D236; `APNsRejectsOurKey`
was a Grafana rule: notify's error lines show it).
A device token APNs reports gone is deleted (`ryft_push_tokens_pruned_total`). Raise
`push.enabled` (above) once sending works, so the app starts asking for permission.

## Apex site and apple-app-site-association

`https://ryftlab.de` is the static export of **`web/`** in this repository on Firebase Hosting
(project `ryftlab-site`, C1, D233). The pages, the legal texts (`web/content/legal/`), the Hosting
config and the AASA live there; its rules are `web/CLAUDE.md`. `www.ryftlab.de` redirects to the
apex. `deploy.yml`'s `site` job ships it and checks it live (**GCP → Deploy, roll back, rotate →
The site**, with its rollback); Cloud Monitoring's uptime checks watch it (D236).

- **`/.well-known/apple-app-site-association`:** `webcredentials` for password AutoFill
  with email accounts, and `applinks` for the paths the app's `AppRoute` parses
  (`../app/docs/API_REQUESTS.md`): `/day/*`, `/sleep/*`, `/insights/*`,
  `/train/sessions/*`, `/train/session/*`, `/train/exercises/*`, `/train/plan/*`,
  `/train/history`, `/invite/*` (since 2026-10-21), `/auth/*`. The app's entitlements
  list `webcredentials:ryftlab.de` and `applinks:ryftlab.de`.
- **Those paths in a browser** (a phone without the app) get an "Open in RYFT" page.
- **The legal pages**, English only (web D18), at `/privacy`, `/terms`, `/support`,
  `/impressum`, each version of the privacy policy also at `/privacy/<version>`. The German
  pages are gone; `/de/…` (still linked by older app builds) answers 301 to the English path.
  `/privacy` is `PRIVACY_POLICY_URL`, its version `PRIVACY_POLICY_VERSION` (OPERATIONS →
  Beta).
**Verify a web release:** the `site` job's live check (`web/scripts/check-hosting.mjs`) runs on
every `deploy.yml` run; by hand, **GCP → 10g**, the same assertions plus Apple's CDN. When an
uptime check of the site fails: **GCP → When an alert fires**.

## Mail for ryftlab.de

**`support@ryftlab.de` runs on Microsoft 365, bought through GoDaddy** (D214, live since
2026-11-14). The mailbox, its users and any forwarding are configured at GoDaddy and Microsoft.
`infra/gcp` (Terraform, the user applies) publishes the records the tenant needs on Cloud DNS, as
code in `infra/gcp/dns.tf` (public values, no tfvars), next to the CAA records:

| Record | Name | Value |
| --- | --- | --- |
| MX | `ryftlab.de` | `0 ryftlab-de.mail.protection.outlook.com.` |
| TXT | `ryftlab.de` | `NETORGFT21311797.onmicrosoft.com` (the tenant's verification) and `v=spf1 include:secureserver.net -all`, next to Search Console's `google-site-verification=` and, once set (C1), Firebase Hosting's `hosting-site=` string |
| TXT | `_dmarc.ryftlab.de` | `v=DMARC1; p=quarantine; adkim=r; aspf=r; rua=mailto:dmarc_rua@onsecureserver.net;` (GoDaddy collects the reports) |
| CNAME | `selector2._domainkey.ryftlab.de` | `selector2-ryftlab-de._domainkey.netorgft21311797.d-v1.dkim.mail.microsoft.` (DKIM) |
| CNAME | `autodiscover.ryftlab.de` | `autodiscover.outlook.com.` |
| CNAME | `email.ryftlab.de` | `email.secureserver.net.` (GoDaddy's webmail) |
| CNAME | `lyncdiscover.ryftlab.de`, `sip.ryftlab.de` | `webdir.online.lync.com.`, `sipdir.online.lync.com.` |
| CNAME | `msoid.ryftlab.de` | `clientconfig.microsoftonline-p.net.` |
| SRV | `_sip._tls.ryftlab.de` | `100 1 443 sipdir.online.lync.com.` |
| SRV | `_sipfederationtls._tcp.ryftlab.de` | `100 1 5061 sipfed.online.lync.com.` |

Not published: GoDaddy's parking A records on the apex (the apex and `www` point at Firebase Hosting,
`infra/gcp/site.tf`, C1), and `selector1._domainkey`, which did not resolve on 2026-11-14. Once GoDaddy
shows a `selector1` value that resolves, add its line to `mail_cnames` in `dns.tf` (with GoDaddy's
exact value), plan (`1 to add`), apply. Microsoft signs with one selector at a time and rotates between
the two, so DKIM fails on the day it signs with a selector that is not published.

**Check every record** against one of the zone's own nameservers (works before the delegation),
then once more against a public resolver after it:
```bash
ns=$(terraform -chdir=infra/gcp output -json name_servers | jq -r '.[0]')
d() { printf '%-5s %-38s ' "$1" "$2"; dig +short +norecurse "$1" "$2" @"$ns" | tr '\n' ' '; echo; }
d MX ryftlab.de; d TXT ryftlab.de; d TXT _dmarc.ryftlab.de; d CAA ryftlab.de; d CAA nothing-here.ryftlab.de
for n in selector2._domainkey autodiscover email lyncdiscover sip msoid; do d CNAME "$n.ryftlab.de"; done
d SRV _sip._tls.ryftlab.de; d SRV _sipfederationtls._tcp.ryftlab.de
```
Every line prints the table's value (CAA at the apex: `0 issue "letsencrypt.org"`, `0 issue "pki.goog"`
and `0 issuewild "letsencrypt.org"`; `nothing-here.ryftlab.de` prints nothing, so a CA climbs to the apex's set.
There is no `*.` CAA set on purpose: it would answer for every name that does not exist, RFC 8659).
For the public check, set `ns=1.1.1.1` and drop `+norecurse`. Then send a mail from an outside account to
`support@ryftlab.de` and one back, and check "Show original" for `SPF: PASS`, `DKIM: PASS`,
`DMARC: PASS`; a DKIM failure that names `selector1` means the line above is due.

**Who processes the mail:** Microsoft (with GoDaddy as the reseller) receives and stores what is sent
to `support@ryftlab.de`, and GoDaddy receives the DMARC aggregate reports. The privacy policy in force
(2026-10-26) still names Google Workspace for email: correcting that is a new policy text and version,
the owner's call (D214).

**Supabase Auth's mail** as the domain is its own setup at Microsoft: **GCP → 14a** (14a.1 to 14a.4).

**A change at the provider** (GoDaddy shows new values, a DKIM key rotates to a new target, DMARC
moves to `p=reject`): edit the value in `infra/gcp/dns.tf` in a PR, then from main
```bash
terraform -chdir=infra/gcp plan -out=mail.tfplan   # only the records you changed
terraform -chdir=infra/gcp apply mail.tfplan
rm infra/gcp/mail.tfplan
```
and run the check above. Each TXT value is its own string in the list, never two values inside one
pair of quotes (the first hand-made record's fault in 2026-10: its SPF did not count).

**A forwarding target is never in the repository.** Any private address mail is forwarded to
is configured at the provider only; it never appears in a DNS record, this repository or a
Terraform variable.

## App Review demo account

App Review signs in with an email account: `review@ryftlab.de`, filled by
`scripts/seed_demo.sh` with 60 days of synthetic Apple Watch and iPhone data (three
workouts a week), two logged strength sessions (the second yesterday, so a records
card shows), a week of journal, consent and beta access. The script checks that Today,
Sleep, Strain, Train and Insights all have data. Run it before every submission: it
deletes the old demo account (`DELETE /v1/me`, sign-in included) and creates it again,
so the data is always recent.

In zsh, from the repository root, with `ADMIN_API_KEY` exported as in **GCP → The beta gate: warn, then on → 1**
(Secret Manager, never on screen):

```bash
REF=$(sed -n 's/^SUPABASE_PROJECT_REF=//p' deploy/run/config.env)
V=$(sed -n 's/.*name: supabase-secret-key, key: "\([0-9]*\)".*/\1/p' deploy/run/api.yaml)   # the version the api reads
SUPABASE_SECRET_KEY=$(gcloud secrets versions access "$V" --secret=supabase-secret-key --project=ryftlab-prod) && export SUPABASE_SECRET_KEY
read -r "SUPABASE_ANON_KEY?Supabase publishable key (Project settings -> API Keys): "; export SUPABASE_ANON_KEY
read -rs "DEMO_PASSWORD?The demo account's password: "; echo; export DEMO_PASSWORD
SUPABASE_URL=https://$REF.supabase.co scripts/seed_demo.sh
unset SUPABASE_SECRET_KEY DEMO_PASSWORD
```

`DEMO_PASSWORD` is the password App Store Connect → App Review Information holds; it never goes
into the repo. Its old home, the in-cluster Vault's `kv-operator/demo`, is gone (D3). For a new
one, take it from 1Password's generator, run the script with it, and update App Store Connect.
Against the compose stack: `DEMO_TOKEN=dev-api-key-change-me:$(uuidgen) API_URL=http://localhost:8082
INGEST_URL=http://localhost:8081 ADMIN_API_KEY=dev-admin-key-change-me-0123456789abcdef
scripts/seed_demo.py`.

## Data protection (logs)

- **What is logged:** one access line per request (method, path, status, bytes,
  duration, route, request id, user id) and what the services decide: a sync's counts
  and device id, a training import's counts (`train import`: source, result, items per
  status, days), which day was scored by which model (`scored day`: `day`,
  `model_version`, `inputs_changed`), a push's category and day, errors with their
  cause. The api's `admin signed in` (D244), once per operator session per instance,
  has no key of its own: only what every line has and what logx adds from the request,
  as on every line. Cloud Monitoring's `ryft_notices` counts it, `feedback received`
  and `admin auth rejected` by their message alone, and its alert names the policy and
  that message, no key of the line (GCP → 14a.9). Paths carry dates and resource ids (`/v1/days/2026-10-19`,
  `/v1/workouts/<uuid>`), so the lines still show when someone used the app and which
  days they opened: personal data, read and kept as below.
- **Never logged:** health values (no score, sample, sleep or workout value, baseline,
  learned sleep need or readiness state, neither as a field nor in an error's text: a
  body that does not decode is named by its field and type, not by its value), request
  or response bodies, query strings, tokens (APNs device tokens too: a failed push's
  error does not quote its URL), the `Authorization` header, email addresses (the smoke
  user's too), device names, feedback messages and diagnostics, invite notes. Supabase's
  admin API errors are logged as the status and GoTrue's `error_code`, never the body.
- **Exceptions**, both from bugs and rare: a panic's stack trace (`debug.Stack()` in
  `httpx.Recover`) shows the arguments of the calls on it as hex words, and pgx's error
  for an argument it cannot encode (an int64 too large for an `int4` column) quotes that
  argument. Fixing the bug needs both; read such lines as health data.
- **Tests pin it:** `TestEngineLogsNoHealthValues` (scoring at debug level, every
  line's keys allowlisted), `TestSyncLogsCountsOnly` and
  `TestSyncErrorPathsLogNoHealthData` (ingest), `TestWritesLogNoBodies`,
  `TestErrorPathsLogNoBodies` and `TestTrainImportLogsCountsOnly` (api), `TestSyncStripsNULBeforeTheDatabase` and
  `TestWritesRefuseNULBeforeTheDatabase` (a NUL removed, or refused with `422`/`400`,
  logs no text), `TestDecodeErrorsQuoteNoValues` (events), `TestJSONErrorDropsTheValue`
  (logx), `TestTransportErrorsHideTheDeviceToken` (apns), `TestAdminErrorsHoldNoBody`
  (auth). A new line on the paths they run fails them until its keys are listed there.
  - **Value kinds:** each allowed key also has a kind of value, pinned by the test-only
    package `internal/logx/logcheck` (a depguard rule keeps production code from importing
    it): UUIDs for user, request and device ids, `YYYY-MM-DD` for days, closed enums for
    category, code, gate, method, level, service and model version, booleans for flags,
    non-negative integers for counts (bounded where a bound is known: `request_ids` at most
    20, rescored days at most `MaxRangeDays`), an integer 100 to 599 for a status, RFC 3339
    for the time. A float, a score or a string under a count key fails; so does a key that
    appears twice on a line (`encoding/json` keeps the last, and logx appends `user_id` and
    the request's keys after the call site's, so a duplicate would hide one). The known
    values of each test (recovery, readiness, device tokens, the seeded samples) are
    scanned for in the raw line. `TestSyncLogKindsRejectMutations`,
    `TestWriteLogKindsRejectMutations`, `TestImportLogKindsRejectMutations`, `TestEngineLogKindsRejectMutations` and
    `TestNotifyLogKindsRejectMutations` feed the checker synthetic lines (a float under
    `applied`, a score under a renamed key, a non-UUID `user_id`, a duplicate key, …) and
    require a problem that names the mutated key; `TestNotifyLogsNoHealthValuesOrTokens`
    pins the notify service's lines.
  - **Third-party errors:** the admin (GoTrue), Sign in with Apple and APNs errors that are
    logged or shown hold the response's status code with the standard phrase
    (`logx.StatusLine`, never `resp.Status`, whose text is the server's), and from the body
    only a bare code: `error_code` / `error` as `[a-z_0-9]{1,64}`, an APNs `reason` as
    letters only (else "no error code" / "unrecognised"); an undecodable GoTrue body is a
    fixed "undecodable response".
  - **Accepted gaps:** an integer health value under an integer count key (`samples`,
    `days`, `bytes`) passes by type; only a known bound or the test's known values catch
    it. `err` is free text up to 1 KiB (including pgx's "unable to encode" quote of an
    argument, see above), covered only by the scans for the values a test knows.
    `client_request_id` accepts what httpx accepts (`[A-Za-z0-9._:-]{1,128}`, the
    contract of `X-Request-ID`), digits and dots included. The lines of the events panic
    paths, of auth, httpx and the account, admin and train handlers, and of
    `cmd/notify`'s stale-check loop have no key test yet.
- **Older lines:** before this change `scored day` carried the day's scores and inputs
  (readiness, state, recovery, strain, sleep, nap, sleep need, HRV, resting HR), a
  failed push's error the device token (`refresh push`, `sync stale` and the broker's
  retry lines), and a `bad event` line the number it could not decode, which the dead
  letter's `Ryft-Error` header kept as well. The logs and the dead-letter queues of that time
  kept 14 days, so all of it is gone 14 days after the deploy of this change
  (`docs/STATUS.md` has the date).
- **Where and how long:** Cloud Logging's bucket `ryft-logs` in `europe-west1`, **14 days**
  (`infra/gcp/logging.tf`); `_Default` is off, so what no sink takes is stored nowhere. From
  `edge = "built"` on the sink excludes most of Google's request logs (OBSERVABILITY → Logs →
  Google's request logs on GCP, D242). `infra/gcp` defines no other sink.
- **Who can read them:** the principals with a Logging read role on `ryftlab-prod`
  (`gcloud projects get-iam-policy ryftlab-prod`, GCP step 1). Treat any new grant as access to
  the users' personal data.

## Backups

**Supabase.** The database is Supabase's, and so are the backups: Database →
Backups. **The free plan keeps none and pauses a project after a week without
activity**, so the project moves to **Pro** before the first tester's data arrives: daily
backups kept 7 days; point-in-time recovery is an add-on (worth it once losing a day of
syncs matters more than its price; the app re-uploads only what HealthKit still holds).
There is no dump job of RYFT's own. Before restoring a backup:

1. In the SQL editor, run `SELECT * FROM deleted_users` and download the result as CSV.
   Accounts deleted after the backup was taken would otherwise come back.
2. Restore in the dashboard.
3. Insert the tombstones back: SQL editor → import the CSV into a temporary table,
   then `INSERT INTO deleted_users SELECT * FROM <tmp> ON CONFLICT (id) DO NOTHING`.
4. `gh workflow run deploy.yml --ref main -R timkrebs/ryft-platform`: every run runs `migrate`,
   which sets the role passwords in case the backup predates them (**GCP → Deploy, roll back,
   rotate**). The api's tick (its `account reconcile` step) erases the restored accounts.

**Restore drill, monthly** (Pro): **GCP → 14a.7** (D239). It replaces the drill that stood here:
its copy is connected to nothing, so no service and no smoke runs against it.

## Upgrading

Migrations run in the migrate job before the services (`deploy.yml` runs it first, and no service
rolls when it fails; advisory lock, so two migrate runs at once are safe).
New columns on `daily_scores` (readiness, sleep detail, strain detail, baselines) are
filled when a day is scored (`steps` and `active_kcal` too), and source
de-duplication (`SCORING.md`) changes strain and sleep for anyone with two devices. Rescore history once after upgrading to this
version:

```bash
curl -fsS -X POST https://<host>/v1/recompute -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d "{\"from\":\"$(date -v-90d +%F 2>/dev/null || date -d '-90 days' +%F)\",\"to\":\"$(date +%F)\"}"
```
