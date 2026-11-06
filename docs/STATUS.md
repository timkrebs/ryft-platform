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
The **training import** (2026-10-26, PR #59, D124–D141, `docs/work/2026-10-26-train-import/`):
`POST /v1/train/import` with scoring model 2026.10.2 and migration 0028, reviewed, **not yet
deployed**. Your steps, in order (cluster: `--context ryft-prod-euc1`, which is the current context on
this machine; check with `kubectl config current-context`):
1. **T11 check, read-only, before the merge.** As the database owner in the Supabase SQL editor:
   `SELECT count(*) FROM plan_exercises WHERE target_kind = 'bodyweight' AND (target_value < 0 OR target_value > 500);`
   Worked = `0`. Anything else: stop; those plans would fail the new validation on their next save.
2. **Merge #59** (`gh pr checks 59` green first). CI builds the images and opens a promote PR
   (`deploy/<7 hex>`) that pins `overlays/eks`; it merges itself once its checks pass
   (`gh pr list --state open --search 'head:deploy/'`; open over 2 h: the daily deploy watch reports it,
   merge it by hand).
3. **Argo CD syncs** (2–6 min after the pin merges, about 15 min for the release):
   `kubectl --context ryft-prod-euc1 -n argocd get applications` until all are Synced and Healthy.
   The migrate PreSync hook applies `0028_train_import.sql` (additive; a failed migration fails the sync
   and nothing rolls: `argocd app get ryft-platform` names the step; the PostSync smoke runs last).
   Worked: the migrate Job's log says `migration applied 0028_train_import.sql`, `GET /v1/model`
   (api.ryftapp.de) returns `2026.10.2`, `make smoke-prod` passes, and `GET /v1/admin/flags`
   (OPERATIONS → Feature flags) lists `train.import` with `enabled: true, rollout_pct: 0` (dark).
4. **Rescore:** `KUBE_CONTEXT=ryft-prod-euc1 scripts/rescore_all.sh`. It runs
   `kubectl --context ryft-prod-euc1 -n ryft-scoring exec deploy/scoring -- /app rescore -days 400` in the context
   you name (never the current one; the name from `kubectl config get-contexts -o name`, if your alias differs)
   and prints the model first. Worked: the last line names `2026.10.2` and
   no failed users. Needed because the model moved (assisted sets, the 90-day e1RM reference).
5. **The flag stays dark** (`rollout_pct: 0`, nobody on the allow-list). When the app ships the import:
   allow-list testers, then widen, with `PUT /v1/admin/flags/train.import` (commands: OPERATIONS →
   Feature flags). Kill switch: `{"enabled": false, "rollout_pct": 100}`.
6. **Privacy text with the app release, not before** (PR #61, draft): flip both pages to
   `status: published`, then merge. A draft version on `main` blocks every deploy PR. Every account
   consents again (`consent_required` rises; expected).
7. **The vault PR (`infra/vault-import-service`, an import service of the wearable data) does not depend
   on #59**: land it before or after. Its Terraform is already applied; the merge deploys nothing (its
   namespace is not an Argo app). Run `cd infra/vault && terraform plan` first: expect no change.

Rollback of 2–4: re-pin the previous images (OPERATIONS → Deploy and roll back); 0028 is additive and stays.

Open (D141, not built): a server-held import namespace for merged accounts (until then the app must not
import the same file again after a merge); a `score_queue` sweeper (days that stay queued if a request
dies between queueing and the rescore); the api's shutdown drain against the import's 50 s budget (a
rolling update can cut an import in flight; the app resends and the stored items come back `unchanged`).
Contract:
[`API.md`](API.md). Design: [`ARCHITECTURE.md`](ARCHITECTURE.md). Judgement calls and
deviations from the brief: [`DECISIONS.md`](DECISIONS.md).

## Production hardening (2026-11)

Run of 2026-11-04 and 2026-11-05: Phase 0 (safety net), Phase 1 (docs in code) and part of Phase 2 (structure) of
`docs/work/2026-11-04-production-hardening/plan.md`, plus the admission and admin-web work that ran beside it. Written at the
close-out against `origin/main` `7b439df`; every number below comes from a pull request named next to it or from a
command run for this note, and "not verified" means exactly that. Decisions: D148–D152 (earlier) and D163–D183 (this
close-out); D162 is the `/admin` page (#128), D185 the NetworkPolicy peers (#140, written in #139); D184 is proposed in
#141 (open). Updated after the close-out with the admin-web merges of 2026-11-05 (#135, #136, #128, #138, #140) and the
owner's checks of that evening. Behaviour is unchanged except where D174 says otherwise
(A2: four accepted changes), where D150 changed the DLQ replay (a redelivery) and the operator scripts (they name their
cluster), where D182 re-scoped the RabbitMQ operators (once synced), and where the admin work added routes and CORS
(D153–D161, #136).

**Done**

- **Golden scoring fixtures** (#90, D149; CI guard #93): six synthetic users × 30 days (2026-10-05 to 2026-11-03, across
  the Berlin and New York clock changes), every `daily_scores` column plus the `workouts` and `sessions` rows scoring
  writes; `make golden-update` refuses while `ModelVersion` is unchanged unless the generator's inputs changed (and then
  only for those users; `ModelVersion` is still `2026.10.3`). Every Phase 2 PR below
  passed them unchanged.
- **Coverage gate** (#89, D148): relative, fails on a drop of more than 1.0 point of the `internal/` total against main's
  newest push run. **Baseline 77.30 %** (the first push run after #89, run 41792584785); **81.35 %** on main at
  `d924dd7` (run 42034721046; both read from the runs' `coverage` artifacts for this note).
- **Load baseline** (#97, D166; numbers in `docs/OBSERVABILITY.md` → Baselines): the median of three runs on one laptop
  (Apple M4 Pro, a 4-vCPU podman VM, one replica of everything): `POST /v1/sync` p50 520 ms, p95 4.3 s, p99 4.5 s;
  `GET /v1/days` p50 16.5 ms, p95 221 ms, p99 652 ms; 0 % errors; `ryft_sync_to_score_seconds` p95 2.4 s. **Caveats:** the
  runs are bimodal (sync p95 1.3 to 4.6 s across the three runs; a quieter machine gave 83 and 73 ms), a laptop is not
  production, n = 50 syncs per run makes p95 and p99 single events. Only the error rate is a gate; no nightly job exists.
- **Contract tests** (#87, #100, D167): 108 schema snapshots (104 in #87; counted in `backend/internal/model/testdata/schemas` on main now), API.md examples decoded, the Errors tables tied to the
  `model.Code*` constants and the Swift mirror, `make check-api-doc`.
- **Docs in code** (Phase 1; #94, #95, #96, #99, #102; D163–D165): a `doc.go` per package, revive `exported` on with no
  `//nolint` for revive (other linters have some), the package map generated into `docs/ARCHITECTURE.md` (`scripts/pkgmap.sh --check`), import rules R1–R13 as
  a test, `make lint`, and a warning-only check that every decision is cited from code.
- **Structure** (Phase 2):
  - A1 `internal/app` (#105, D168–D173).
  - A2 scoring and notify on `app.Run` (#117, D174; `ServiceExited` and the extended `Panics`, rules in `deploy/grafana/alerts/`).
  - B1 `model.go` split into 14 files, proven by `scripts/check_pure_move.sh` (#110, D176).
  - C1 `computeDay` in steps (#111, #112), C2 `ApplySync` in steps (#122, #123), C4 `trainImport` in steps (#119, #120),
    each as characterisation tests on the old code followed by the refactor with unchanged goldens (D177).
- **Longest functions and files over 800 lines, before and after.** Measured for this note with a go/ast walk over
  non-test files under `backend/` (a function's first to last line; the method of `phase2-design.md`'s appendix, which
  reproduced that note's numbers for the base), base `69d1838` (before A1, B1, C1, C2, C4) and `d924dd7`:

  | | before (`69d1838`) | after (`d924dd7`) |
  | --- | --- | --- |
  | functions over 80 lines | 15 | 10 |
  | functions over 40 lines | 77 | 80 |
  | longest function | `scoring.computeDay` 278 | `cmd/api` `main` 184 (163 before; the growth was not analysed) |
  | non-test files over 800 lines | 3: `model/model.go` 1 453, `store/train.go` 1 378, `store/store.go` 1 148 | 2: `store/train.go` 1 378, `store/store.go` 1 152 |
  | non-test files, lines | 132, 24 249 | 158, 27 413 (new code since the base, such as `internal/app` and the admin work; not broken down) |

  Per refactor, from the PR bodies: `computeDay` 278 to 28 lines, `ApplySync` 136 to 14, `trainImport` 101 to 45 (and
  `finishImport` 69 to 21), scoring `main` 132 to 24, notify `main` 81 to 16. The 40 count rose because new code was added
  (A3 and L1 are not done); no "after" number exists from any PR other than these.
- **Admission and CI follow-ups merged this run:** #53 (a replayed dead letter is a redelivery; operator scripts name their
  cluster, D150), #57 (the `admission` job and a kind end-to-end run, D151/D152), #114 and #116 (the kind-run scripts as a
  tripwire, D181), #51 (both operators namespace-scoped, D182), #55 (admission signals scraped and alerted, D183), #131
  (alert groups under Grafana Cloud's 20-rule limit, D179), #107 (the licence snapshot in step with the app).
- **Admin web, backend half** (#126 admin sign-in by Supabase token with MFA, CORS, audit actor, D153–D158; #127 invites for
  a person, revoke, who redeemed, D159–D161; migrations 0032 and 0033).
- **Admin web, the page and its config** (2026-11-05): #135 merged 13:39 UTC (the secret-scan allowlist for the page's
  publishable key, U4); #136 merged 13:55 UTC (`ADMIN_WEB_ORIGINS=https://ryftapp.de`); #128 merged 15:48 UTC (the `/admin`
  page, D162, web D15–D17) and pinned by #138 (16:04 UTC, web `bb74830`); #140 merged 17:27 UTC (the unused ingress-nginx
  peer removed from the api, ingest and web NetworkPolicies, D185). The docs as built are #139 (API.md audit row and page
  column, `design.md` corrections, OPERATIONS → Admin sign-in setup order and V-MFA-1, SUPABASE.md checklist).

**Live in production as of 2026-11-05** (the pins of `deploy/k8s/overlays/eks`: services `dc344e1`, web `bb74830` (#138)
on `origin/main`; that Argo CD has the cluster Synced and Healthy on them is **not verified** from this session):

- Services at `dc344e1` (#134): A2 (reached the pin with #130, `dc1fcd4`, 10:22 UTC; see D175), the B1, C1, C2 and C4
  refactors, #126 and #127.
- **The admin page works** (`https://ryftapp.de/admin`): merged (#128), pinned (#138), **confirmed by the owner on
  2026-11-05** (pasted output): the web pods rolled; `/admin` answers `200` with the admin CSP and `Cache-Control:
  no-store`; `/` keeps `connect-src 'none'`; the api rolled with #136's `ADMIN_WEB_ORIGINS` (`admin routes enabled` with
  `"web_origins":1`, the `OPTIONS` preflight on `/v1/admin/invites` `204` with `access-control-allow-origin:
  https://ryftapp.de`); the operator's sign-in on the page worked, so the owner did the account setup (OPERATIONS → Beta →
  Admin sign-in). **V-MFA-1 result: not recorded.** Whether `make alerts-apply` ran before the first row (D158) is **not
  verified**. Also **not verified**: `mfa_required` after 12 h on the live project, the effect of *Sign out*
  (`scope=global`) on other devices, the lost-phone runbook.
- **NetworkPolicies without the ingress-nginx peer** (#140, D185): Argo CD updates the three policies in place, no pod
  restarts. #140's read-only post-sync checks were given to the owner; the result is **not verified**.
- **Invites with a bound email** exist since migration 0033. A bound code is created or redeemed only while the api's
  `invite binding` state is `ok` (Confirm email on at Supabase, read as `mailer_autoconfirm` from the project; D159); the
  log line `invite binding` after the deploy is the evidence, and **reading it from the live pods is not verified here**.
  D159 also says bound codes are not to be created in production before the privacy text (#129, the user's decision U3)
  is published, and not before the rollout of the image with 0033 has completed on all api pods.
- **Alert rules in Grafana Cloud:** #131's body (10:47 UTC) says `make alerts-apply` had been refused by the 20-rule limit
  and `ServiceExited` and the extended `Panics` were not live. Whether the user has re-run it since is **not verified**.

**Merged but NOT applied**

- **#47** (Argo CD's ClusterRoles narrowed, admission Step 0; D180), merged 2026-11-05 10:12 UTC: nothing reaches the
  cluster until the user plans and applies `infra/aws/kubernetes`. The commands and the checks that come first are in
  OPERATIONS → "Applying the narrowing (Step 0)"; in short, from a clean `main` (the stop rules are in that text):

  ```bash
  terraform -chdir=infra/aws/kubernetes init                           # the values guard is a new local module
  terraform -chdir=infra/aws/kubernetes plan -out=argocd-rbac.tfplan   # only helm_release.argocd may change
  terraform -chdir=infra/aws/kubernetes apply argocd-rbac.tfplan       # then delete argocd-rbac.tfplan: it holds a password hash
  ```

  **P12 (#121) must not go to Deny before this is applied** and the three `kubectl auth can-i` answers in that text are no.
  Until it is applied the chart's `*` ClusterRoles are the live ones.

- **#55** (admission signals, T12-e; D183), merged 2026-11-04: the scrape and the kube-state-metrics collector are in
  `deploy/monitoring/k8s-monitoring.yaml`, which reaches the cluster only through the owner's monitoring apply, and the
  admission alert rules only through `make alerts-apply`. **Needed only if not yet run (not verified):** until the monitoring
  apply has run, the native `validatingadmissionpolicybindings` collector and the API-server admission scrape do not exist
  in the cluster.

  ```bash
  make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1        # the owner's step; KSM restarts once, Alloy reloads its ConfigMap
  make alerts-apply                                         # GRAFANA_* variables as for any alert load
  # Grafana Explore, from #55's body: 1 per API server endpoint plus one for KSM
  up{job=~"ryft/kube-apiserver-admission|ryft/admission-bindings"}
  ```

- **#51** (both RabbitMQ operators namespace-scoped; D182), merged 2026-11-04: `deploy/argocd/project.yaml` is applied by hand
  and #51's body asks for that **before** the merge (`kubectl diff -f deploy/argocd/project.yaml` shows only the added
  `ryft-rabbitmq` destination, then `kubectl apply` it); Argo CD then syncs `rabbitmq-operators` and both operators restart
  once. Whether that was done, and the post-sync checks of OPERATIONS → RabbitMQ → "After an operator sync" (both operators
  Running, no new `forbidden`, the broker `ALLREPLICASREADY`, every topology object Ready, `kubectl auth can-i list secrets
  -A --as=system:serviceaccount:rabbitmq-system:rabbitmq-cluster-operator` answers `no`), is **not verified**. Needed only
  if not yet done.

**Open**

- **#118** (draft): EKS authentication mode `API`, inert until the user sets it in `terraform.tfvars`; one-way.
- **#121** (draft): admission policy P12 `ryft-argo-scope`, chart 0.3.0, ships disabled (`admission-values.yaml` still says
  `enabled: false`). Its Deny gate needs P6, P1 and P2 in Deny (`operators-cluster`, `namespaces`, `workloads-templates`)
  and a narrowed controller role rendered by `argocd.tf` (#47 applied); the chart fails the render and `admission.tf` the
  plan otherwise. Kind run 42004326289 on its head: 83 pass, 0 fail, 2 skipped (`argo-rbac`, `claim-i`: that tree has no
  #47). Follow-up T12-l6 (Ingress hosts and Traefik router annotations, `externalIPs`) is not built.
- **V-MFA-1 result: not recorded.** The owner did the operator setup; please record the probe's status and `error_code`, the
  factor limit it ran under and the date here (OPERATIONS → Beta → Admin sign-in, step 3: with *Maximum enrolled factors* 1
  the pass is `422 too_many_enrolled_mfa_factors`, with 2 or more `403 insufficient_aal`; `200` means stop). The Supabase
  states (Data API off, maximum enrolled factors 1, *Confirm email* and *Secure email change* on) are not recorded here
  either (SUPABASE.md → checklist).
- **#141** (open): the unknown-key-id follow-up below.
- **#129** (draft): the privacy text for an invite address, the user's decision U3. A new `PRIVACY_POLICY_VERSION` makes every
  account consent again; under `CONSENT_ENFORCE=warn` (this file records `warn` until the app ships its consent screen, D87; the
  live Vault value is not verified here) uploads still pass, and scoring keeps running (#129's body).
- **Proposed, not decided** (drafts; no D-entry until they merge): #118 proposes to switch to authentication mode `API`
  after read-only checks, a recovery identity outside Identity Center and a plan that shows one in-place update of
  `aws_eks_cluster.this[0]`, with admins pinned in `admin_principal_arns`; its text calls it irreversible and says a missing
  node access entry is an immediate user-facing outage. #121 proposes a twelfth policy that lets Argo CD's service accounts
  write only where its apps render (the controller in an explicit namespace list, `argocd-server` Applications and events
  plus the first `server.secretkey`), failurePolicy Fail, and states that access reviews are exempt from policies since
  Kubernetes 1.30 (read from the kube-apiserver sources; its kind run saw P12 refuse neither review, D183).

**Queued follow-ups** (nothing here has a PR yet unless named)

- Small fixes: a foreign-key violation (SQLSTATE 23503) should map to `ErrNotFound` in the scoring handler (no `23503`
  handling exists under `backend/`, checked with grep); `eventFailed` in `cmd/ingest/main.go`; the trends `422` message (these
  three come from the close-out hand-off and are not traced to a PR body); the P1, P6, P7 and P8 CEL templates read
  `request.userInfo.username` without `has()` (checked in the templates; P12 guards it); `httpx.TestInstrumentLabelsByInnerRouteAcrossRequestCopies`
  fails at `-count>1` and passes at `-count=1` (#105's body); `Add(0)` for the counters whose first event no alert sees
  (listed under Technical below, and the holes named in D174); `samples_7d` and `coverage` are documented in API.md and absent
  from the Go types (`docDrift`, #87); `loadNight`'s `mean`/`last` read a store error as a missing value (#112).
- Documentation: superseded notes for D15, D16 (its third bullet points to D185 since #139; the first, Prometheus on 8080, is also stale), D18, D27, D33, D44 and D84 (the hand-off's list; D15 is in
  `scripts/decision_refs.skip`); `../app/Ryft/Networking/Models.swift` line 2 names `model/model.go` (the app repo, #110);
  the "Not covered (yet)" sentence in `docs/ARCHITECTURE.md` still says admission policy is not covered, while the chart
  exists and is disabled; the grouping leftovers #110 lists. The `Models.swift` and `ARCHITECTURE.md` lines are not edited here.
- Citations still missing for `scripts/decision_refs.sh` (5 warnings: D168, D169, D170, D172, D178): cite D168 to D172 in
  `backend/internal/app/doc.go` and D178 at the clock-change sites, batched with the next backend change (a `backend/` edit
  rolls every service, so it is not done in a documentation PR). D175 and D177 are process rules, in
  `scripts/decision_refs.skip` with a reason; the other new decisions are cited from scripts, `.golangci.yml`, the
  `Makefile`, the alert rules and the monitoring values (comments only).
- Phase 2, not built: A3 api and ingest through `app.Run`, A4 the delivery in hand finishes on shutdown (amqpbus), A5 `/readyz`
  and the ordered shutdown for api and ingest, A6 settings in one place, A7 the 5 s drain delay, L1 `funlen` lint, B2 the
  error taxonomy, B3 the store split, B4, C3 the ingest sync in steps. Stage each as D175 says (hold the group first).
- The clock-change days (D178): the `AddDate` helper in `touchedDays`, `deleteSamples`, `deleteWorkouts` and `planFold`, with its own D-entry.
- Phases 3 to 5 are queued, none started: request deadlines and `statement_timeout`; goroutine lifecycle; broker audit and
  prefetch 1; JWKS stale-while-revalidate; idempotency tests per consumer; RLS tests, where **a decision is needed from the
  user** (the service roles' policies are `USING (true)`, D42, so "user A cannot read user B through a service role" fails
  by design: re-scope the test or add per-user RLS); govulncheck and dependabot for the Go modules; a nightly load job;
  build info and pprof; SLOs; a database audit and retention; the restore drill.
- Admin-web follow-ups: **an unknown key id on an admin route** still answers `503 jwks_refresh_limited` within the refresh
  limit (OPERATIONS → Admin sign-in). Mapping it to the generic `401` was considered and rejected (D184 in #141, open); the
  root cause, a client hang-up counted as a failed key fetch, is fixed in #141. Open until #141 merges. The privacy text should
  name `api.ryftapp.de` with the PR that ships PR2's wording. Settled: the ingress-nginx rule (#140, D185), the stale
  `docs/API.md` audit row and the `design.md` corrections (#139).

**Deploy commands for what is open** (the user's; every cluster command names its cluster, D150):

```bash
# #47 Step 0: above (terraform init, plan -out, apply of that file).
# #55, only if not yet run: the monitoring apply, then the alert rules (the GRAFANA_* variables of OPERATIONS → Monitoring set)
make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1
git pull --ff-only && make alerts-apply
# #51, only if not yet done: kubectl --context ryft-prod-euc1 diff -f deploy/argocd/project.yaml (only the added destination), then apply it,
# and the checks of OPERATIONS → RabbitMQ → "After an operator sync".
# The admin page: done by the owner on 2026-11-05 (sign-in works). Still to record: the V-MFA-1 result (OPERATIONS → Beta → Admin sign-in, step 3).
# #140, read-only after the sync (its body): the ingress-nginx peer is gone and traffic still passes Traefik
kubectl --context ryft-prod-euc1 get networkpolicy -n ryft-api -o yaml | grep -c ingress-nginx      # 0 (also -n ryft-ingest, -n ryft-web)
curl -s -o /dev/null -w '%{http_code}\n' https://api.ryftapp.de/v1/me   # 401
make smoke-prod
```

**Gotcha: a newer promote PR supersedes a held one.** On 2026-11-05 the promote PR #124 (A2) had its auto-merge switched
off at 06:53:06 UTC to stage the rollout, and sat open; promote closed it as "Superseded by deploy: images dc1fcd4" when the next promote PR (#130) opened, and #130
merged itself. To stage a backend change, hold the group (`./scripts/hold.sh add services "<reason>"`) before the merge that
builds it; see D175 and OPERATIONS → Deploy and roll back → "Staging a backend change".

## Vault CE migration (2026-11)

Moving every credential from HCP Vault Dedicated to Vault Community Edition 2.2.1 in namespace `vault` (plan, design and
addenda: `docs/work/2026-11-05-vault-ce/`). Decisions: **D186-D205** (D186-D198 and D204 are cited by the code; D203 is a procedure;
D199-D202 and D205 are decided, not built). Written against `origin/main` `6d1f9fa` on 2026-11-06. "Not applied" means exactly that:
**nothing reads the in-cluster Vault, and it does not exist in the cluster yet.** Every cluster command below names
`--context ryft-prod-euc1` (the current context is production; D150).

**Merged (2026-11-06)**

| PR | What | State |
| --- | --- | --- |
| #146 | `infra/aws`: the unseal key, a separate snapshot key and the snapshot bucket (D189), `NoDeletionOrDisable`, the Spot `check` (D204) | **Applied by the owner on 2026-11-06: 11 added, 7 changed, 0 destroyed.** The 7 were drift of the node group and the add-ons (ENI/CNI, CoreDNS, kube-proxy, EBS CSI, pod identity agent, metrics-server, node group release 1.36.4-20261012 → 1.36.5-20261031), applied by mistake against #146's stop rule ("if the plan shows any `~`, `-` or `-/+` line, stop"); the cluster stayed healthy and `make smoke-prod` was green (the owner's report, not verified here). The 7 addresses are in the HCP Terraform run log and are not recorded here |
| #145 | `infra/aws/kubernetes`: Vault as a Terraform add-on (`vault.tf`, `iam.tf`, `charts/vault-extras`, `charts/cluster-issuers` 0.3.0, the injector exclusion) (D188, D190, D194, D196) | Merged, **not applied**. Expected plan **from `main`: 11 to add, 1 to change, 0 to destroy**: #145's seven (the unseal role, its policy, attachment and association, the namespace, `vault_extras`, `vault`) plus #153's four (`module.vault_snapshot_pod_identity`: role, policy, attachment, association `snapshot`); the one change is `cluster_issuers` 0.2.0 → 0.3.0. `vault_extras` is **created** at 0.2.0, with the CronJob |
| #147 | Admission for `vault`: P1-P4 extended, P13 new, chart 0.5.0 (D195) | Merged, **disabled** (`enabled: false`); nothing live |
| #148 | Test only: the `trainImport` characterisation no longer depends on today's date (it failed in every PR's `test` job from 2026-11-06) | Merged. Unrelated to Vault. The promote PR that followed is #149 ("deploy: images edef166", merged 2026-11-06 07:42 UTC; the five services d372b8f → edef166, one image group, behaviour unchanged); Argo CD's sync of it is not verified here |
| #152 | `scripts/vault_diff.sh` (compares the two Vaults and a synced Secret; no value is ever printed; no `--save`; HCP's address pinned), `RYFT_VAULT=ce` in `lib/admin.sh` and `seed_demo.sh` | Merged; scripts on the operator's machine, nothing to apply |
| #151 | `infra/vault`: the Kubernetes target beside HCP, the bootstrap files, the generated `vault-ca` Certificates, OPERATIONS → Vault (D187, D191, D193, D198, D203) | Merged, **not applied**. Both switches off. Expected plan: 9 to add, 9 to destroy (the generated files only) |
| #153 | Snapshots (CronJob, role, bucket deny), metrics, 11 alerts in `ryft-vault`, switched-off log pipeline, admission chart 0.5.1 (D192, D194, D197) | Merged, **not applied**. Expected plans: `infra/aws` 0 to add, 1 to change (the bucket policy, `DenyPutWithoutIfNoneMatch`); its `infra/aws/kubernetes` part is already in #145's plan above (11 / 1 / 0) |

A follow-up PR from branch `ops/vault-followups` (OPERATIONS, OBSERVABILITY, alerts) was in preparation by another agent
when this was written: **no PR and no remote branch existed yet**, so it is not recorded here.

**Your steps, in order** (nothing below has been run; each plan must match the number named, anything else is a stop)

The order follows the lead's: gates first, then the add-on. Strictly, #145's and #153's bodies require the Fleet, Beyla
and injector exclusions before the add-on apply, and G1-G4 before the bootstrap.

1. **Two decisions that are yours** (no D-entry records them): G2, whether the monitoring stack inside Vault's trust boundary
   is accepted ("Vault is as safe as the monitoring stack and VSO", D194) or reduced first; and the Google account of the Vault
   login, a dedicated Workspace user (recommended, OPERATIONS → Vault → The operator's Google account) or the existing one
   hardened.
2. **G1, the admission layer.** P12 cannot go to Deny alone: #121's chart render and its `admission.tf` precondition refuse
   `argo-scope` in Deny unless P6 (`operators-cluster`), P1 (`namespaces`) and P2 (`workloads-templates`) are in Deny too, and those
   are the **general** `ryft-*` bindings, not the `vault` ones. #121 is a stale draft (head `4b9f739`; a trial merge with `main`
   conflicts in 19 files). In order:
   1. Apply #47's Step 0 (STATUS → Production hardening → deploy commands).
   2. Rebase #121. It merges second: it bumps the chart above 0.5.1 (#147's and #153's), sets 18 bindings and 13 policies
      (`admission_intended.sh --check` fails if missed) and reconciles P1's in-scope list with `argoScope.namespaceObjects`.
   3. A fresh full-mode `admission-e2e` kind run on a tree that contains #47.
   4. Mark #121 ready, review, merge, and give its decision a D-number (D206 is next).
   5. Enable the chart and apply it (`infra/aws/kubernetes`; every binding starts on `audit`).
   6. Move P6, P1 and P2 (the general bindings) to **Deny**, then P12, each in a reviewed PR. The `vault` bindings and P13 can
      go to Deny at any time, and alone.
   Verify with the G1 command in OPERATIONS → Vault → Hard gates. **If step 6's plan shows `helm_release.admission_policy`, this
   step's apply is not done: stop.**
3. **Fleet Management and Beyla** (D197): the pod-log exclusion `ryft-.*` → `ryft-.*|vault` and Beyla's exclusion of `vault`
   (OPERATIONS → Before the Vault add-on is applied).
4. **The injector exclusion** (#145): `make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1`, then
   `kubectl --context ryft-prod-euc1 get mutatingwebhookconfigurations -o custom-columns='NAME:.metadata.name,NAMESPACES:.webhooks[*].namespaceSelector,OBJECTS:.webhooks[*].objectSelector'`:
   the injector's `NotIn` list carries every `ryft-*` namespace, `vault` and `vault-secrets-operator-system`.
5. **Read-only checks:** `kubectl --context ryft-prod-euc1 get nodes -L topology.kubernetes.io/zone,eks.amazonaws.com/capacityType`
   (three zones, `ON_DEMAND`); `aws kms describe-key --key-id alias/ryft-prod-euc1-vault-unseal --region eu-central-1 --query 'KeyMetadata.[Arn,KeyState]' --output text`
   (the ARN and `Enabled`). Optional here, required before the first snapshot (step 10): apply #153's `infra/aws` change now
   (`terraform -chdir=infra/aws plan` is exactly **1 to change**, the bucket policy; see step 10 for the stop rule).
6. **Apply #145** from `main`: `terraform -chdir=infra/aws/kubernetes plan -out=vault-addon.tfplan` (**11 / 1 / 0**: #145's seven plus #153's four; `vault_extras` is created at 0.2.0 with the CronJob), `apply`, then
   `rm infra/aws/kubernetes/vault-addon.tfplan`. The CronJob fails until step 9 writes the role `vault-snapshot`: expected. Keep
   steps 8-10 in one sitting, or do not load the `ryft-vault` alert group before step 10. Verify read-only as the list in #145's body (step 6): three pods `Running`, `0/1`
   ready, in three nodes, `vault-initialized=false`, `vault-sealed=true`, `vault-version=2.2.1`, exactly five `AWS_*` variables
   in the pod, no `unable to set initial state`. Then the Explore check: `{namespace="vault"}` (Loki), Beyla series and Tempo
   return nothing, **before bootstrap step 3**. If `vault status` already shows `Initialized true` before your own init
   (G3): stop, treat it as a compromise, wipe the PVCs and EBS volumes.
7. **Apply #151's plan.** Precondition first: `kubectl --context ryft-prod-euc1 get clusterissuer ryft-vault` READY True. Then `terraform -chdir=infra/vault plan -out=tfplan` (9 to add, 9 to destroy; no port-forward, no CA file,
   no `TF_VAR_vault_k8s_token`; HCP token only), `apply`; commit the nine regenerated overlays in a PR of their own
   (`vault: the vault-ca Certificates`); then `kubectl --context ryft-prod-euc1 apply -k deploy/k8s/overlays/vault-namespaces/monitoring`
   and the same for the import service's key (both are outside Argo CD). Verify: `kubectl --context ryft-prod-euc1 get certificate -A --field-selector metadata.name=vault-ca`
   shows 10 rows READY True; `kubectl --context ryft-prod-euc1 -n monitoring get secret vault-ca` exists. Optional:
   `kubectl --context ryft-prod-euc1 apply -f deploy/argocd/project.yaml`.
8. **The bootstrap** (OPERATIONS → Vault → Bootstrap, ten steps) in a **standalone terminal**, never an agent's shell or the
   VS Code terminal: init (recovery keys 2-2-1, paper first), audit devices, policies and the token role, OIDC, the subject
   check, the Terraform token, **root revoked**, the share drill.
9. **PR 3b** (`committed_write_k8s = true` alone, one switch per PR): plan and apply through the port-forward with the
   ephemeral token; `scripts/vault_diff.sh --context ryft-prod-euc1 --all --k8s-cacert ...` all equal; the baseline lists
   `vault-snapshot` with `[snapshot]` and `token_max_ttl` 600.
10. **Apply the rest of #153:** `terraform -chdir=infra/aws plan` must be **exactly 1 to change** (the bucket policy), then apply (skip
    if done before step 6). Node-group or add-on changes in that plan mean the drift of #146's run has recurred: stop.
    `infra/aws/kubernetes` has nothing left of #153 (it was in step 6) except the collector peer: only if `vault status` showed
    `Initialized true` from your own init, the root token is
    revoked and G1-G4 hold, put `vault_collector_peer = true` into `infra/aws/kubernetes/terraform.tfvars` (a file entry, not
    `-var`), plan (1 to change), apply; `make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1` and the Explore checks of OPERATIONS
    → Vault metrics (`up{job="ryft/vault"}`, `up{job="ryft/vault-state"}`, `kube_pod_labels{namespace="vault",label_vault_initialized="true"}`
    three series, `vault_core_active`, six `kubelet_volume_stats_*` series each; if those are absent drop `VaultVolumeFilling`
    and the kubelet branch of `VaultScrapeDown`); `make alerts-apply`; the first snapshot by hand (`kubectl --context ryft-prod-euc1 -n vault create job --from=cronjob/vault-snapshot vault-snapshot-first`,
    wait, logs of `upload`, `.status.lastSuccessfulTime`, delete the Job) and the create-only checks (a put without
    `If-None-Match` is denied; the second put with `--if-none-match '*'` answers 412).
11. **Cutovers 5a-5i**, one PR per group, each with `vault_diff.sh --kv` and `--secret ... --vault hcp` equal before the switch and
    `--secret` equal after; before 5a flip `peers.vso.enabled` (it is off; a PR and an apply, plan 1 to change on `helm_release.vault_extras`); then 14 days of soak, a fresh `vault_diff.sh` with zero
    differences, a restore drill, and **your explicit go** for PR 6 and 6b (decommission HCP and drop its provider); PR 6c then
    narrows the EKS endpoint (D205).
12. **Logs, when you want them** (D197): accept Grafana's DPA, add the record-of-processing entry naming the transfer mechanisms,
    put `LOKI_USERNAME` into `infra/vault`, then a PR with the Loki URL and `podLogsViaLoki.enabled: true`.

**Open**

- **Tickets not built:** V7a (the import service's operator objects as an Argo CD app, D200), V7b (monitoring's, project
  `ryft-monitoring`, D201), V7c (the collector as a Terraform `helm_release`, namespace handover, `make monitoring-apply`
  removed, D202); PR 3b (`write_k8s`, step 9); cutovers 5a-5i; PR 6, 6b (decommission), 6c (EKS endpoint, D205). V7a and V7b come before 5i and 5h, V7c is
  recommended before the collector changes. Until V7a and V7b exist, every regeneration of the monitoring and import-service
  overlays needs the same hand `kubectl --context ryft-prod-euc1 apply -k deploy/k8s/overlays/vault-namespaces/<dir>`.
- **Tickets for test-engineer:** the kind rehearsal of #145 in full mode (render under admission, real pods under
  `enforce=restricted` against a transit-sealed dev Vault, init and 3 voters, a deleted pod coming back, the service-registration
  labels with the narrowed Role, `cmctl renew` within 6 minutes without a restart, NetworkPolicy enforcement, disruption in
  three zones); the kind restore drill in CI (three Raft pods, MinIO, the 412 on a second put, `restore -force`); the fake
  `ryft-vault-logs` lines for D199.
- **The Loki rules** (`ryft-vault-logs`, D199) need `lokitool`; until they exist nothing watches `sys/audit*`.
- **Decisions for the owner:** G2 and the Workspace user (steps 1); whether the PDB stays `minAvailable: 2` with `AlwaysAllow`
  (shipped) or moves to `IfHealthyBudget` (D204); whether a later PR flips `vault_collector_peer` to `true` by default.
- **Privacy:** Grafana Labs' DPA, the record of processing with the transfer mechanisms (UK adequacy, US entities; to verify), and
  the Art. 32 wording that must not claim Vault is isolated from the monitoring vendor.
- **Smaller follow-ups from the reviews:** pin the unseal key ID in the precondition; `aws:SourceIp` or a KMS VPC endpoint for
  the unseal calls; vendor the chart tarball; a wider values precondition; `aws:SourceAccount`/`aws:SourceArn` trust conditions
  once verified; a test tying `images.vault` to the release's pin; the `ryft_admin` bearer token in curl's argv (before cutover
  5e); the CEL harness's decommission fixture needs a literal HCP address when the api moves (5a); the stale comment on
  `reserved_service_names` in `infra/vault/targets.tf`; #121's follow-ups (flip three P12 cases, a guard that `vault` and
  `cert-manager` are never in `argoScope.namespaces`, a kind fixture of Argo writing in `vault`); `addendum-1.md` §3 still names
  the recovery-key places; #151's open question 4 (the public-443 egress also carries Google's OIDC discovery and JWKS; the
  comment names only KMS); the `terraform` policy's paths left unaudited until the kind rehearsal (`sys/mounts` read, `sys/auth`
  read, `auth/kubernetes/role` list, `sys/policies/acl` list, the tune `update` paths, and the probably-unneeded `sudo` on
  `sys/mounts/kv-*`); #146's open question 2 (`prevent_destroy` on the bucket's versioning, SSE and policy resources); `argocd`
  and the other add-on namespaces are not in the injector exclusion (#145's residual risk). Also open: list the seven drift
  addresses of #146's run from the HCP Terraform log, to decide whether the node group release and the add-ons are now what
  `infra/aws` should say.
- The EKS public endpoint stays `0.0.0.0/0` until PR 6c.

**Not verified** (each is marked "to verify" where the runbook uses it)

- Pod Identity credentials reaching the `awskms` seal at runtime on 2.2.1, and that EKS sets the three session tags the trust
  policy requires; Pod Identity in a CronJob pod.
- Chart 0.34.1 with Vault 2.2.1 (the chart is tested up to 2.0.4); Pod Security `restricted` admitting the pods; `$(VAULT_K8S_POD_NAME)`
  expanding; `tls_reload_interval` picking up a renewal; the service-registration Role at runtime; the PDB and zone spread under
  a real drain; the VPC CNI and egress to `169.254.170.23`; the plans against the real providers (mocks only).
- That 2.2.1 enables audit devices over the API with these options; Google's `iss`, `hd` and `auth_time`; the Admin console user ID
  equal to the OIDC `sub`; the loopback binding of the three bound files; generate-root under auto-unseal and the `peers.json`
  recovery.
- S3 conditional writes and the SHA-256 checksum against the bucket policy; `kms:Decrypt` for a cached bucket key; `create job
  --from=cronjob` setting `lastSuccessfulTime`; auto-unseal after `restore -force` (the kind drill).
- In the cluster: the scrape, the `alloy-deployment` and VSO pod labels, KSM's endpoint labels, `kube_pod_labels` with Vault's
  labels, `kubelet_volume_stats_*`, `vault_token_count*`; the Fleet UI paths and Beyla's exclusion syntax; the log pipeline end to end.
- #146's applied state beyond the owner's report (11 added, 7 changed, smoke green): not checked here; the seven changed addresses
  are not listed (HCP Terraform run log).

**GitGuardian** is not a required check. It flags a service name after the secret flag of `vault_diff.sh` as a "Generic CLI Secret": five incidents, all false
positives to mark in the dashboard: three test lines of #152's first commit (`7670547`), the occurrence in this PR's first commit
`ca5dbb5` (incident 39736629; the later commit `61ebd02` cannot clear it) and `docs/work/2026-11-05-vault-ce/addendum-1.md`
line 569 (the call of `vault_diff.sh` with the flag for the synced Secret, followed by the namespace name `monitoring`; already on
main from #150). No secret is involved.

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
| Training import | `POST /v1/train/import`: plans and sessions from another app's export, per-item results, idempotent by per-account ids, the app's edits win (`newer_on_server`), one rescore per request, no pushes, silent records; flag `train.import` (off), its own rate class and a lease per user; model 2026.10.2 (e1RM reference over 90 days, assisted sets) | `cmd/api/train_import.go`, `internal/train/import.go`, migration 0028 |

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
| Admission kind run (2026-10-26, T12-d, D151, D152) | Podman, kind v0.33.0, node `kindest/node:v1.36.4`, kubectl 1.36.5, helm 4.3.1. `--mode ci` on the branch: 29 checks passed, 1 skipped (`argo-rbac`: the branch has no T12-f); the API server held exactly the 11 policies and 12 bindings `helm template` renders, in Audit and in Deny, with `status.typeChecking` empty and a canary fixture answered within 2 s; 218 objects of the 10 apps, the root and the projects dry-run applied as Argo CD's controller (2 refused/warned by P6 for the vendored operator roles, explained by the missing T12-g); 48 generated and 30 manual fixtures; claims a, b, c, d and the negative test of h. `--mode full` on a git worktree of the branch merged with #47 (plus the `applications` rule), #51: 62 checks passed, 0 failed: cert-manager, Argo CD from `argocd.tf`'s values (narrowed ClusterRoles, `respectRBAC: strict`), 11 Applications Synced and Healthy in Audit and again after a manual sync of every app in Deny, PreSync migrate and PostSync smoke Succeeded, the broker Ready with 63 topology objects, no validation failure logged, the D98 restart, `terminate-op`, both operators' `can-i` answers, claims i, k (the full SHA) and m (disproved). `--mode ci --kubeconfig` on a kind cluster of its own (compared with `kind get kubeconfig`; 28 passed, claim d skipped for want of an audit log), an interruption (SIGTERM deletes the cluster and keeps the logs), and no `ghcr.io/timkrebs/ryft` tag left in the local image store. `make test-scripts` (kind guard with 17 hostile server URLs, links and a fake `kind`, tracked copy, tf_values refusals, chart bump, ref policy, promote_only, audit findings, tree values), `make admission-celtest` (690 cases on the branch), `make k8s-validate`, shellcheck, actionlint, `check_workflows.sh`. The `admission` job then ran on GitHub, on PR #57: 28 checks passed and 1 skipped (`argo-rbac`), the CEL harness 696 cases and 0 failed, the chart at 0.2.3 (0.2.1 when the branch was first run). Not verified: a node drain, three workers, claims f, j and l, the `admission-e2e` workflow, the install scripts on a runner (their sha256 were checked against the published files and the downloaded bytes). |
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

**Mail for ryftapp.de on Google Workspace: your steps, in this order** (2026-10-28, D142; PR
`infra/dns-google-mail`). Done: the records are in `infra/aws/dns` (MX, the apex TXT as two
values, DKIM split for Route 53, `_dmarc` at `p=none`), tested in `tests/dns.tftest.hcl`
(not run locally: no terraform allowed here; CI's terraform job runs it); OPERATIONS →
"Mail for ryftapp.de". Open, yours: (1) merge the PR; (2) import, plan, apply with the
commands in OPERATIONS (`terraform import` ×3, then `plan`: expect the apex TXT to change
to two values, `_dmarc` created, nothing else; then `apply`); (3) send a test mail both
ways and read SPF/DKIM/DMARC in the headers; (4) after 2–4 weeks of clean DMARC reports set
`dmarc` to `p=quarantine`. The privacy texts' Google line is in PR #66 (2026-10-20) and #61
(2026-10-26); the Impressum still has the phone and USt-IdNr/§ 36 VSBG `TODO(user)`s.

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

**Follow-ups of 2026-10-23: done**

- **The migrator** bounds every lock wait with a 5 s `lock_timeout`, set after its advisory
  lock (D116; OPERATIONS → Deploy and roll back: a migration that hit the lock timeout).
- **Log tests** pin each value's kind, find duplicate keys and cover notify; GoTrue, Apple and
  APNs refusals log a status code and a bare code only (D117).
- **`dlq.sh` replay** marks a replay as a redelivery (no recompute needed once scoring runs it),
  replays only bound keys, loses no letter; every operator script and cluster Makefile target
  needs `KUBE_CONTEXT`, the local targets refuse EKS (D150).
- **NUL and non-UTF-8 text** gets a `422`/`400` instead of a 500; the sync strips U+0000 and
  accepts the page; raw JSON fields are normalised; a database refusal is alerted (D118).

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
- **kubectl's default context is production** on your machine (it made the 2026-10-23 incident
  possible, D150): point the default at something harmless, or keep production in its own
  `KUBECONFIG` file. The RabbitMQ default user's rotation is not required by that incident (the
  security review); check the EKS audit log query in the PR if you want certainty.
- **Postgres version:** confirm the hosted major version once (`select version();` in the
  Supabase SQL editor); the repo targets 17 (SUPABASE.md → Postgres version).
- **Argo CD** reports `OrphanedResourceWarning: 1 orphaned resource` on ryft-platform:
  `argocd app resources ryft-platform --orphaned`.
- Optional: squash commits take the PR title; approval for outside contributors' runs; the
  `bytes` job as a required check (`ci-ok` already needs it). `PRIVACY_POLICY_URL`
  versioned (`/privacy/<version>`): your call.

Technical:
- **An admission policy in the cluster:** designed (`docs/work/2026-10-23-admission/design.md`,
  security-reviewed three rounds): Pod Security labels plus ValidatingAdmissionPolicy (CEL), owned by
  Terraform in `infra/aws/kubernetes`, Warn+Audit first, then Deny in four steps by blast radius.
  Decided (D120): Terraform owns the AppProjects, both operators namespace-scoped, `admission`
  required, the SDK injector excludes `ryft-*`, break-glass is your binding patch. Tickets T12-a…k:
  see "Production hardening (2026-11)" above for what merged (#47, #51, #55, #57, #114, #116) and what is open
  (#121, #118); nothing reaches the cluster before your applies (the note's §5 commands).
  **T12-d (the kind run) is done** on `ci/admission-kind` (D151, D152): CI's required job `admission`
  (`scripts/admission_e2e.sh --mode ci`, ~5 min; skipped for promote PRs and their merge) and the
  repeatable full run (`make admission-e2e REPO=<git checkout>`, ~25 min, or the manual workflow
  `admission-e2e`). It found and fixed P10 (Argo CD's own auto-sync carries a copy of the source) and P11
  (the Cluster Operator's defaulted `secretBackend.externalSecret.name: ""`), chart 0.2.3. **#47 is merged,
  not applied (D180; Step 0 is yours):** it had lacked `{ apiGroups = ["argoproj.io"], resources =
  ["applications"], verbs = ["get", "list", "watch"] }` in `argocd_controller_rules`; the rule is in it (claim
  i did not wait for ever: under `respectRBAC: strict` the kind is dropped from the cache silently, so the
  root lagged by minutes and a deleted child would not be pruned; D151 amended). #47 also dropped
  `escalate`/`bind` on ClusterRoles, tightened CRD update and the server's cluster-wide writes, and
  states what stays open until P6 and P12 are both in Deny (docs/OPERATIONS.md "What stays open"). T12-e is merged
  (#55) and live after the monitoring apply: its binding-state signal
  is kube-state-metrics' native `validatingadmissionpolicybindings` collector (claim m is disproved only for custom-resource-state,
  which exports CRD kinds only; D183 amends D151); P7 must not go to break-glass assuming P10 blocks
  `--repo` (P10 no longer does). Not tried by the kind run: a node drain, three workers, claims f, j and l.
- **Node 26 for web** on or after 2026-10-28: Docker tag and digest, `.nvmrc`, `engines`,
  `@types/node` in one PR (D108).
- **Supply chain** (#17's review): pin `tonistiigi/binfmt` and `moby/buildkit` by digest;
  Trivy before push for the service images; provenance `mode=max` carries the push event
  payload. Dependabot for the `docker run` images and the services' Go modules and base
  images; the services' `images` still use the GHA cache.
- **scoring** runs two consumers for 10–30 s during a rollout surge: confirm its timer is
  safe, or `maxSurge: 0` for scoring.
- **The rest of D150's guard:** plain `kubectl` lines in the OPERATIONS runbooks and `CLAUDE.md`'s
  `make k8s-apply-eks` example still use the current context; the guard covers the scripts and the
  Makefile targets.
- **Alerts miss a counter's first event:** `Panics`, `DeadLetters`, `DeadLetterPublishFailing`,
  `NewFeedback`, `PushFailing`, `APNsRejectsOurKey`, `AccountDeletionIncomplete` and
  `EventsUnroutable` use `increase()` on CounterVecs created at 1, so the first event per label
  set never fires (promtool-proven). Initialise the known label values with `Add(0)` at start-up
  (sre; D118's alert already handles it).
- **Log tests, the rest** (D117): an allowlist test for the broker's retry and dead-letter lines
  and the events panic line (needs RabbitMQ in the test), for auth, httpx and the account,
  admin and train lines; cut pgx's "unable to encode" error to the argument's index in one
  `logx` helper; an invite code can stay in the access line's `path` after a failed delete.
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
# Every cluster command names its cluster, per command (D150). ryft-prod-euc1 is the alias the
# `aws eks update-kubeconfig` above wrote (the name from `kubectl config get-contexts -o name`, if yours differs).
make k8s-validate && make k8s-apply-eks KUBE_CONTEXT=ryft-prod-euc1
make monitoring-apply KUBE_CONTEXT=ryft-prod-euc1      # Grafana Cloud collector, after infra/vault has the token
make smoke-prod                                        # after every deploy (OPERATIONS.md → Deploy and roll back)
make alerts-apply                                      # alert rules into Grafana Cloud (OBSERVABILITY.md → Alerts)
KUBE_CONTEXT=ryft-prod-euc1 scripts/rescore_all.sh     # after a deploy that changed scoring rules or the model version (Beta: yes)
kubectl --context ryft-prod-euc1 -n ryft-scoring exec deploy/scoring -- /app learn-baselines   # M4, once; then weekly on its own
```

Run `make smoke-supabase` only with the dedicated test user.

Self-hosted (k3s etc.): `make k8s-apply-local KUBE_CONTEXT=<your local context>` (`OPERATIONS.md` → Self-hosted); it refuses a
context that looks like EKS (a heuristic).
