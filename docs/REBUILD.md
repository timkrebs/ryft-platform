# Rebuilding production (2026-11)

On 2026-11-10 RYFT was shut down (D206) and every RYFT AWS resource was destroyed. This is the way back to
production **as it was**: the same hostnames (the TestFlight build needs no change), the same images, the testers'
data in Supabase. One thing is different: Vault runs in the cluster and is the only Vault (D207). HCP Vault and Google
Workspace are gone.

Every step is yours. Nothing here is run by an agent. Each step has the commands, the plan it must show, a
**worked when** and a **stop** rule. A plan that differs from the count named is a stop: find out why before you
apply (the #146 apply of 2026-11-06 went wrong exactly that way). Every cluster command names
`--context ryft-prod-euc1`. Commands that touch secrets run in a standalone terminal with history off (OPERATIONS →
Vault → "Where these commands run"), never in an agent's shell or the VS Code terminal.

The decisions this order rests on, and what is yours to confirm, are D207-D212 (`docs/DECISIONS.md`).

## Before you start

- **Your decisions** (D208-D212): the seal (Shamir, built by default), the login (a generate-root session, built), G2,
  the mail provider (none until you pick one). The defaults let you go ahead; change one before the step that uses it.
- **This PR is merged** and you work from a clean `main` (`git status` empty, `git pull --ff-only`). The repository is
  not archived: `gh repo view timkrebs/ryft-platform --json isArchived` says `false` (CI and the promote PRs need it).
- **Tools:** Terraform exactly 1.16.6 (`terraform version`), `terraform login` done, `kubectl`, `helm`, the `vault` CLI
  2.2.x, `aws` (the RYFT admin profile), `jq`, `dig`.
- **For steps 5-7** (shares and tokens on screen): Handoff off, every clipboard manager quit, Secure Keyboard Entry on
  in the terminal (OPERATIONS → Vault → **The clipboard**).
- **The HCP Terraform workspaces** (step 14): check them now, before step 3.
- **Supabase** (step 7's checklist) can be read now; its network restriction waits for the NAT addresses of step 3.

## 1. DNS: the zone's records, then the delegation back to Route 53

The zone `Z08455293REDHBC9QW5MZ` still exists with Google Workspace's mail records, and the domain points at
GoDaddy's nameservers. First the records (D212: no mail until you choose a provider), then the delegation.

```bash
terraform -chdir=infra/aws/dns init
terraform -chdir=infra/aws/dns plan -out=dns.tfplan
```
**Plan: 0 to add, 3 to change, 1 to destroy:** `mx` (`0 .`), `apex_txt` (`v=spf1 -all`), `dmarc` (`p=reject`) in
place, and `dkim[0]` destroyed (the `moved` block maps today's `dkim` to it). **Stop** if the zone or the CAA records
change, or if `dkim[0]` is not destroyed (a `dkim_record` is still set somewhere).
```bash
terraform -chdir=infra/aws/dns apply dns.tfplan
rm infra/aws/dns/dns.tfplan
dig DS ryftapp.de @a.nic.de +short          # must print nothing (no DNSSEC at the registry)
terraform -chdir=infra/aws/dns output name_servers
```
**Stop** if `dig DS` prints a record: DNSSEC was switched on at GoDaddy. Turn it off there, wait out the DS record's
TTL, and check again. Otherwise every resolver that validates fails the domain once Route 53 answers.

Then at GoDaddy (My Products → ryftapp.de → DNS → Nameservers → Change → "I'll use my own nameservers"), enter the four
nameservers of the output, as they are, without the trailing dot.

**Worked when:** `dig +norecurse NS ryftapp.de @a.nic.de` lists the four `awsdns` servers, and `dig MX ryftapp.de
@1.1.1.1 +short` prints `0 .`. DENIC publishes the change within minutes. Resolvers keep GoDaddy's answer for up to
the `.de` delegation TTL of **86400 s**, so allow 24 hours (48 for stragglers). Steps 2-7 don't need the delegation, so
do them meanwhile. Step 4's certificates and step 8's hostnames need it.

**Mail:** from now on no `@ryftapp.de` address receives mail. The Impressum and the privacy policy name
`support@ryftapp.de`, so a provider must work again **before, or soon after, the testers are back**. OPERATIONS → "Mail
for ryftapp.de" says how. Until then, give the budget alerts (step 2) and `acme_email` (step 4) an address outside the
domain.

## 2. `deletion_protection = true`

In `infra/aws/terraform.tfvars` (an editor; never print the file), set `deletion_protection = true`. It guards only the
EKS cluster against a delete (not the node group, the VPC, the keys, the bucket or the volumes). While you're there,
make sure `budget_alert_emails` names an address that receives mail (step 1).

**Worked when:** `grep -c '^deletion_protection *= *true' infra/aws/terraform.tfvars` prints `1`.

## 3. `infra/aws` from empty

First check that the destroy left nothing behind that the apply would create again (each leftover fails the apply
with `AlreadyExists`). Keys pending deletion don't matter: the apply creates new ones.
```bash
aws kms list-aliases --region eu-central-1 --query "Aliases[?starts_with(AliasName, 'alias/ryft-prod-euc1') || starts_with(AliasName, 'alias/eks/ryft-prod-euc1')].AliasName"   # []
aws s3api head-bucket --bucket "ryft-prod-euc1-vault-snapshots-$(aws sts get-caller-identity --query Account --output text)"   # 404 Not Found
aws logs describe-log-groups --region eu-central-1 --log-group-name-prefix /aws/eks/ryft-prod-euc1 --query 'logGroups[].logGroupName'   # []
aws logs describe-log-groups --region eu-central-1 --log-group-name-prefix /ryft/ryft-prod-euc1 --query 'logGroups[].logGroupName'    # []
aws budgets describe-budgets --account-id "$(aws sts get-caller-identity --query Account --output text)" --query 'Budgets[].BudgetName'   # no RYFT budget
```
Anything left: delete it in the console (by name, never by a tag another cluster could share), then go on.
```bash
terraform -chdir=infra/aws init
terraform -chdir=infra/aws plan -out=aws.tfplan
```
**Plan: about 80 to add, 0 to change, 0 to destroy** (counted from the code, not planned): the VPC module (about 28),
EKS with its node group and add-ons (about 41), Vault's two KMS keys, their aliases and the snapshot bucket with its
settings (11), the budget (1). The unseal key is created under Shamir too (D208: it keeps `awskms` one variable away,
about $1 a month). **Stop** on any change or destroy (the state must be empty), or on a resource outside those groups.
```bash
terraform -chdir=infra/aws apply aws.tfplan
rm infra/aws/aws.tfplan
aws eks update-kubeconfig --name ryft-prod-euc1 --region eu-central-1 --alias ryft-prod-euc1
kubectl --context ryft-prod-euc1 get nodes -L topology.kubernetes.io/zone,eks.amazonaws.com/capacityType
aws ec2 describe-nat-gateways --region eu-central-1 --filter Name=state,Values=available --query 'NatGateways[].NatGatewayAddresses[].PublicIp'
```
**Worked when:** three nodes `Ready`, in three zones, `ON_DEMAND`. Write down the NAT addresses for Supabase (step 7).
**Stop** on fewer than three zones: Vault's third pod would stay Pending (D196).

## 4. `infra/aws/kubernetes`: the add-ons and Vault

Traefik, cert-manager with the issuers (Let's Encrypt and the `ryft-vault` CA), ExternalDNS, the ALB controller, VSO,
Argo CD, Fluent Bit and Vault (D188), with `vault_seal` at its default `shamir` (D208). `acme_email` in its tfvars
should be an address that receives mail (step 1).
```bash
terraform -chdir=infra/aws/kubernetes init
terraform -chdir=infra/aws/kubernetes plan -out=addons.tfplan
```
**Plan: about 37 to add, 0 to change, 0 to destroy** (counted from the code): the Helm releases, their Pod Identity
roles and associations, the namespaces, the storage class, the logs key and group, Argo CD's admin password. **Stop**
if it shows `module.vault_unseal_pod_identity` or reads `alias/ryft-prod-euc1-vault-unseal` (then `vault_seal` is set
to `awskms` somewhere), if `helm_release.admission_policy` appears (the chart stays off, D210), or on any change or
destroy.
```bash
terraform -chdir=infra/aws/kubernetes apply addons.tfplan
rm infra/aws/kubernetes/addons.tfplan      # it holds Argo CD's password hash
kubectl --context ryft-prod-euc1 get pods -A | grep -v -E 'Running|Completed'
kubectl --context ryft-prod-euc1 -n vault get pods -o wide -L vault-initialized,vault-sealed,vault-version
```
**Worked when:** every add-on pod is `Running`; `vault-0`, `vault-1` and `vault-2` are `Running`, `0/1` ready, on three
nodes, `vault-initialized=false`, `vault-sealed=true`, `vault-version=2.2.1` (uninitialised means not ready, by design,
D196). A Vault pod can wait in `ContainerCreating` until cert-manager has written `vault-tls`; it recovers by itself.
Once the delegation of step 1 is live: `kubectl --context ryft-prod-euc1 get certificate -A` all `READY True`.
**Stop** if a Vault pod logs `unable to set initial state` or carries an `AWS_*` variable, or if the NetworkPolicy
`vault-server` already admits a peer: `kubectl --context ryft-prod-euc1 -n vault get networkpolicy vault-server -o json |
jq -r '[.spec.ingress[].from[].namespaceSelector.matchLabels["kubernetes.io/metadata.name"] // empty] | join(" ")'`
must print nothing (`vault_vso_peer` and `vault_collector_peer` stay `false` until step 6). Until your init, anything
that reaches 8200 can init Vault without a token (the node's own address, a port-forward), so go straight to step 5:
**if `vault status` shows `Initialized true` before your own init, or the init answers `already initialized`, stop**,
treat it as a compromise and wipe the claims and volumes (G3, OPERATIONS → Vault → Bootstrap steps 1-2).

## 5. Vault: init and unseal, the shares into 1Password

OPERATIONS → Vault → **Bootstrap**, steps 1-3 and 5, in the standalone terminal. Every share and token goes only to a
verified pod: `vault_forward <n> <port>` checks that `vault-<n>` is the StatefulSet's and runs the pinned image, then
port-forwards to that pod, never to `svc/vault-active` (OPERATIONS → Vault → **A verified pod**).
- Step 1: all three pods `Initialized false` (G3).
- Step 2: `vault operator init -key-shares=5 -key-threshold=3` on vault-0. The five shares and the initial root token go
  into 1Password, each share its own item (OPERATIONS → Vault → **Unseal keys**: with Shamir an unseal needs three
  shares every time, so 1Password holds a threshold. The paper copies are the backup against losing 1Password, and
  D203's 2-2-1 split no longer fits).
- Step 3: unseal vault-0 with three shares; vault-1 and vault-2 join by `retry_join` and each is unsealed with three
  shares through its own verified port-forward, after checking that it took vault-0's seal configuration and has no
  init of its own, and that afterwards it shows vault-0's `Cluster ID` (G3).
- Step 5: the restart drill (delete a pod, unseal it by hand).

**Worked when:** `vault operator raft list-peers` shows three voters, `vault operator raft autopilot state` says
`Healthy true` with failure tolerance 1, and the pods are `1/1` with one `vault-active=true`. **Stop** if a pod joins
without being unsealed, if a follower's `Cluster ID` differs from vault-0's, if `vault_forward` prints no `verified`
line, or if a share is refused (wrong item: check 1Password before a fourth try).

**What Shamir means from here on** (D208): every Vault pod that restarts (a node rotated or lost, an eviction, an OOM)
comes back sealed and waits for you (`VaultSealed` pages you: OPERATIONS → Vault → **A pod is sealed**). One sealed pod
costs nothing visible. Two sealed pods lose the Raft quorum, and then nothing is read or written. The services keep
their synced `ryft-secrets` and keep running, but nothing syncs or rotates, and a new namespace gets nothing. The PDB
stops a second drain until the first pod is unsealed, so node maintenance needs you at the keyboard. The alternative is
built: `vault_seal = "awskms"`, one `infra/aws/kubernetes` apply, and `vault operator unseal -migrate` pod by pod
(OPERATIONS → Vault → **The seal: Shamir or awskms**). The shares then become recovery keys.

## 6. Vault: the bootstrap

OPERATIONS → Vault → **Bootstrap**, steps 4 and 6-11: the two audit devices (`log_raw=false`) before anything is
written, the policies and token roles from `infra/vault/bootstrap/`, the first admin token, the terraform token's check,
the root token revoked, the drill of every share, and then the two peers opened. From then on every admin session is a
**generate-root session** (D209, OPERATIONS → Vault → **Login**): three shares make a root token, the root token makes a
one-hour admin token, and root is revoked at once. The admin policy creates no token but the terraform token and
writes no policy, token role or auth method; those changes need the root token of a session.

Step 11, once root is revoked: `vault_vso_peer = true` and `vault_collector_peer = true` in
`infra/aws/kubernetes/terraform.tfvars` (one editor change), then
```bash
terraform -chdir=infra/aws/kubernetes plan -out=peers.tfplan    # exactly 1 to change: helm_release.vault_extras
terraform -chdir=infra/aws/kubernetes apply peers.tfplan
rm infra/aws/kubernetes/peers.tfplan
```

**Worked when:** `vault audit list -detailed` shows `file/` and `stdout/` with `log_raw false`; `vault policy list`
holds `admin` and `terraform`; the token-capability checks of steps 8 and 9 match; after step 9 no token with policy
`root` is left; and step 11's `jq` line prints `vault-secrets-operator-system monitoring`. **Stop** on any capability
the checks do not expect, or if step 11's plan changes anything but the NetworkPolicy `vault-server` in
`helm_release.vault_extras`.

## 7. `infra/vault`

OPERATIONS → Vault → **The first apply** has every command. In short:
1. Delete `vault_parent_namespace_root` and `vault_address_for_cluster` from `infra/vault/terraform.tfvars` (an
   editor). The plan fails while they are set (D207).
2. **The data key and the demo password** (D211). Recover both from the last state version before the destroy:
   ```bash
   scripts/recover_data_key.sh          # writes ~/.config/ryft/data-key-api (0600); prints version, date, serial, a fingerprint, length 32
   scripts/recover_data_key.sh --demo   # writes ~/.config/ryft/demo-password
   ```
   Then import both before the first plan (the session shell, `TF_VAR_vault_k8s_token` set; the output goes to
   `/dev/null`, so the value never reaches the screen) and compare fingerprints, never values (step 3 of **The first
   apply**). **If the workspace or its versions are gone** (the script says so): a 404 can also mean that your token
   cannot see the workspace, so confirm on the workspace's page in the HCP Terraform UI first, because the fallback
   cannot be undone. Only then skip the data key's import (the plan then shows 91 to add) and run the fallback statement
   at step 8, before the api's first sync. Then delete both files: the state holds the values now. The script excludes
   `~/.config/ryft` from Time Machine before it writes, but a local snapshot taken while a file exists keeps it for about
   a day.
3. **Supabase, before the first sync** (in the dashboard; nothing prints personal data):
   - the project is active (not paused) and still on its plan;
   - **Network Restrictions** admit the new NAT addresses from step 3, and the two released ones are gone. The Network
     Bans list is empty;
   - Enforce SSL is fine with `sslmode=require`, and the session pooler's host and port are unchanged;
   - **SMTP:** if it pointed at Google Workspace, Supabase Auth mails (confirmations, resets) fail now. With D212's
     `-all`/`p=reject`, no `@ryftapp.de` sender is delivered until a provider is set up;
   - the JWT signing key (`kid`) and the Apple provider (`dev.timkrebs.ryft`) are unchanged;
   - the database password is the one in `database_owner_password` (never reset it in the dashboard: the migrate Job's
     retries would trip the pooler's ban). Test it with `psql` through the pooler, which prompts for it;
   - the secret and publishable keys and the Apple key in the tfvars are not revoked.

   Then the counts (SQL editor, as `postgres`). Write them down; step 8 compares:
   ```sql
   SELECT (SELECT max(version) FROM schema_migrations) AS schema_version,
     (SELECT count(*) FROM users) AS users,
     (SELECT count(*) FROM deleted_users) AS tombstones,
     (SELECT count(*) FROM daily_scores) AS scores,
     (SELECT count(*) FROM apple_tokens) AS apple_tokens,
     (SELECT count(*) FROM deleted_users WHERE apple_token_enc IS NOT NULL AND apple_revoked_at IS NULL) AS revocations_pending,
     (SELECT count(*) FROM auth.users WHERE email = 'review@ryftapp.de') AS demo,
     (SELECT count(*) FROM pg_roles WHERE rolname LIKE 'ryft\_%' AND rolcanlogin AND NOT rolbypassrls) AS roles_ok;
   ```
   Also check that the smoke user exists: `SELECT count(*) FROM auth.users WHERE email = '<SMOKE_EMAIL>';` prints `1`.
4. `terraform -chdir=infra/vault init` once (before the imports of item 2). Plan and apply in the session shell, with
   the verified port-forward to the active pod (`vault_forward "$(vault_active)" 8200 active`), `VAULT_CACERT` from the
   Secret `vault/vault-ca`, and `TF_VAR_vault_k8s_token` from the admin token's `-role=terraform`.

**Plan: 90 to add, 0 to change, 0 to destroy** after both imports (92 without them, which means import first). That is
9 namespaces and 9 service accounts, 9 `random_password`, the Kubernetes auth mount and its config, 10 KV mounts, 10
policies and 10 roles, 13 KV secrets and 18 generated files. The 90 assumes that the tfvars' `service_secrets` sets the
import service's keys and monitoring's, and nothing for `rabbitmq`; one `vault_kv_secret_v2` fewer per service without
keys, one more for a `service_secrets.rabbitmq`. **Stop** on any change or destroy, on a warning of
`check.bootstrap_terraform_policy_matches_services` (a 403 halfway), or if a `ryft-*` or `monitoring` namespace already
exists.

**Worked when:** a second plan says `No changes`; `terraform -chdir=infra/vault output -json login_check | jq -r .api |
sh` prints `[api default]` (Vault sorts the policies).

**The follow-up PR.** The apply rewrites `deploy/k8s/overlays/vault-namespaces/*/` in the in-cluster shape (nine
directories). Commit them on a branch `vault/in-cluster-overlays` with the other changes listed in
`docs/work/2026-11-13-rebuild/followup-overlays.md`: it adds back the test that holds the committed files to the
generator, and it drops HCP's address from the admission values. Open the PR `vault: the in-cluster overlays`, wait
for CI to go green, and merge it. **Stop** before step 8 until it is merged:
`grep -L 'https://vault.vault.svc:8200' deploy/k8s/overlays/vault-namespaces/*/vault.yaml` on `main` prints nothing.

## 8. Argo CD's first sync

`argocd.tf` installs Argo CD only; the projects and the app of apps are applied by hand (D95). The repository is
public, and so are the GHCR images (no pull secret).

Preconditions (stop if one fails): step 7's follow-up PR is merged (the `grep -L` above prints nothing); all three
Vault pods are unsealed; `./scripts/check_registry.sh` passes; Supabase admits the NAT addresses. **If the data key
was not recovered**, run this now, before the api starts (SQL editor, as `postgres`; a second run prints `0, 0`):
```sql
WITH live AS (
  DELETE FROM apple_tokens
  WHERE created_at < timestamptz '2026-11-11 00:00:00+00'
  RETURNING 1
), tomb AS (
  UPDATE deleted_users SET apple_token_enc = NULL
  WHERE apple_token_enc IS NOT NULL
    AND deleted_at < timestamptz '2026-11-11 00:00:00+00'
  RETURNING 1
)
SELECT (SELECT count(*) FROM live) AS live_tokens_cleared,
       (SELECT count(*) FROM tomb) AS tombstones_cleared;
```
(D211: without it, every account deletion would fail to decrypt, and the reconcile loop would retry forever. With it, a
deleted account's grant is not revoked at Apple (testers can remove it in iOS Settings → Sign in with Apple), and a
tester's token comes back at their next interactive Sign in with Apple.)
```bash
kubectl --context ryft-prod-euc1 apply -f deploy/argocd/project.yaml -f deploy/argocd/root.yaml
kubectl --context ryft-prod-euc1 apply -k deploy/k8s/overlays/vault-namespaces/monitoring
kubectl --context ryft-prod-euc1 apply -k "$(ls -d deploy/k8s/overlays/vault-namespaces/*-import)"
kubectl --context ryft-prod-euc1 -n argocd get applications -w
```
The waves wait for each other: **0** the `vault-<svc>` apps (done when every VaultStaticSecret shows `SecretSynced`),
**1** the RabbitMQ operators, **2** the broker, **3** `ryft-platform` (the migrate PreSync hook sets the new role
passwords and applies no migration (D93); then the services, then the PostSync smoke).

**Worked when:** all 11 applications are `Synced Healthy`; the migrate log has a `role password set` per role and no
`migration applied` unless `main` carries a migration newer than step 7's `schema_version`; `kubectl --context ryft-prod-euc1 -n ryft-smoke logs job/smoke` passes; the Supabase counts of
step 7 are unchanged (`apple_tokens` and `revocations_pending` are 0 if the fallback ran). **Stop:**
- wave 0 still Progressing after 10 minutes: `kubectl --context ryft-prod-euc1 -n ryft-api describe
  vaultstaticsecrets.secrets.hashicorp.com ryft-secrets`. Never create a Secret by hand;
- migrate fails: nothing has rolled. `kubectl --context ryft-prod-euc1 -n ryft-migrate logs job/migrate`;
- any count dropped: stop everything and look before a tester signs in.

After a fix, sync again through the UI (`kubectl --context ryft-prod-euc1 -n argocd port-forward svc/argocd-server
8080:80`, password: `terraform -chdir=infra/aws/kubernetes output -raw argocd_admin_password`). Never use `make
k8s-apply-eks`. The next merge that changes `backend/` or `web/` opens a promote PR as before; it needs only GitHub (the
`deploy` environment with `RYFT_DEPLOY_APP_KEY`, the App installed, auto-merge on).

## 9. The Grafana Cloud collector

A Helm release from `make monitoring-apply`, outside Terraform (D202 is not built). The token is
`service_secrets.monitoring.GC_TOKEN` in `infra/vault`'s tfvars, synced to `monitoring/ryft-secrets` by the overlay of
step 8. If it was revoked at the shutdown, create a new access policy token with the scopes in OBSERVABILITY → **From
an empty cluster**, put it into the tfvars, re-apply `infra/vault` (1 to change), and wait for the sync.

First, the Fleet Management checks of that section: the pod-log exclusion `ryft-.*|vault` and Beyla's exclusion of
`vault` (D197), **before** the collector starts. The collector's peer was opened in step 6 (Bootstrap step 11, D194).
Then:
```bash
grep -c hashicorp.cloud deploy/k8s/overlays/vault-namespaces/monitoring/vault.yaml      # 0
make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1
```
**Worked when:** the OBSERVABILITY list holds (`up{job="ryft/vault"}`, `up{job="ryft/vault-state"}`, three
`kube_pod_labels{namespace="vault",label_vault_initialized="true"}` series, `vault_core_active`). **Stop** if
`kubectl --context ryft-prod-euc1 -n monitoring logs deploy/grafana-cloud-alloy-deployment --since=15m | grep -E '
40[13]'` finds anything, or if any `vault` log line, Beyla series or trace arrives in Grafana:
`helm --kube-context ryft-prod-euc1 -n monitoring uninstall grafana-cloud`, then fix Fleet first.

## 10. The Grafana alert rules

The rules from before the shutdown are still loaded and have probably been firing since 2026-11-10 (NoSync,
ArgoMetricsMissing, AdmissionSignalsMissing, SiteDown, VaultScrapeDown). Look at the silences and contact points first.
The first Vault snapshot is taken by hand **before** the `ryft-vault` group (D194), as in OPERATIONS → Vault →
**Snapshots** → "The first snapshot":
```bash
kubectl --context ryft-prod-euc1 -n vault create job --from=cronjob/vault-snapshot vault-snapshot-first
kubectl --context ryft-prod-euc1 -n vault wait --for=condition=complete job/vault-snapshot-first --timeout=10m
kubectl --context ryft-prod-euc1 -n vault get cronjob vault-snapshot -o jsonpath='{.status.lastSuccessfulTime}'   # not empty
kubectl --context ryft-prod-euc1 -n vault delete job vault-snapshot-first
( read -rs GRAFANA_RULES_TOKEN; export GRAFANA_RULES_TOKEN; make alerts-apply )   # GRAFANA_PROM_URL, GRAFANA_PROM_USER as in OPERATIONS → Monitoring
```
Use a new token with `rules:read` and `rules:write`, and delete it afterwards. **Worked when:** every group loads and
nothing in `ryft-vault` fires after 15 minutes. **Stop** on a group-limit error or a firing Vault alert.

## 11. `make smoke-prod`

```bash
( read -rs SMOKE_PASSWORD; export SMOKE_PASSWORD; make smoke-prod )   # SUPABASE_URL, SUPABASE_ANON_KEY, SMOKE_EMAIL set as before
```
**Worked when:** the `✓ smoke-prod` line prints. **Stop** on any `✗`; no tester is invited back before this passes.

## 12. A tester's check

On your own phone with the TestFlight build: sign in, sync, and today's score appears. Meanwhile watch
`scripts/beta_report.sh`, `scripts/user.sh <id>`, `KUBE_CONTEXT=ryft-prod-euc1 scripts/trace.sh <request-id>` and
`KUBE_CONTEXT=ryft-prod-euc1 scripts/dlq.sh list` (empty). In Grafana Explore, `ryft_outbox_pending` is 0 and the
sync-to-score p95 is under 120 s. **Worked when:** the score shows within two minutes. **Stop** if a sync gets a 403, or
days stay pending for more than two minutes.

## 13. The rescore, if needed

Unsent outbox rows go out by themselves. Events that sat in the destroyed queues are lost and nothing re-sends them, so
their days stay pending in `score_queue` (a lost `session.completed` leaves no row at all). Once `ryft_outbox_pending`
is 0, run in the SQL editor:
```sql
SELECT count(*), min(day) FROM score_queue WHERE scored_at IS NULL AND queued_at < '2026-11-11';
SELECT min(ended_at)::date FROM sessions WHERE status = 'completed' AND updated_at >= '2026-11-09';
```
Both empty: no rescore. Otherwise N = (today − the oldest day) + 2, then
`KUBE_CONTEXT=ryft-prod-euc1 scripts/rescore_all.sh <N>`. **Worked when:** the log ends with `rescore done … failed 0`
and the first query returns 0. Expect one `SyncToScoreSlow`: it measures from the old queue times. **Stop** on failed
users.

## 14. The HCP Terraform workspaces

**Reuse them** (recommended; the `cloud` blocks name them, and nothing in the code changes): `ryft-prod-euc1-platform`,
`-dns`, `-kubernetes` and `-vault` in `tim-krebs-org`, execution mode **Local**, Terraform 1.16.6. Check before step 3
that all four exist and that the three emptied ones hold no resources (`terraform -chdir=<root> init`, then
`terraform -chdir=<root> state list` prints nothing, except for `infra/aws/dns`). New workspaces would also work, but
then the data key's old versions stay in the old `-vault` workspace (step 7 reads them from there). Afterwards: the
`-vault` workspace's earlier versions hold the old (now rotated) credentials and the data key. Restrict team access to
it as before (OPERATIONS → Terraform state).

## If a step fails

Nothing is in use until step 8. A failed `infra/aws` apply is fixed and applied again, or destroyed and started over
(turn `deletion_protection` off first). A broken Vault init before step 7 is a wipe of the three pods' claims and
volumes and a new init. After step 8 the rollbacks are those of OPERATIONS → Deploy and roll back.
