# Ticket B: `infra, deploy: GCP and the deploy workflow` (2026-11-14)

The plan for §10 row B of `design.md`, one PR from `infra/gcp`. It applies nothing and deploys nothing. Ticket A (the backend) is
built after B in the same checkout, and A builds to the **interfaces** below. Where B found the design wrong while building, the
fix is listed under **Design fixes**. The fixes go into `DECISIONS.md` with PR A, folded into D216–D227. B adds no D entry of its
own, so the numbers stay A's.

## Tickets

| # | Owner | Files (one writer each) | Acceptance |
|---|---|---|---|
| B1 | infra-engineer | `infra/gcp/**` (Terraform, `tests/`, lock file, `.tflint.hcl`, README, `pubsub.auto.tfvars.json`), `scripts/dns_compare.sh`, `scripts/tests/dns_compare_test.sh`, `docs/OPERATIONS.md` → `## GCP` (bootstrap, steps, €20 stop line) | `terraform fmt -check`, `init -backend=false`, `validate` pass locally. CI's `terraform` job (matrix entry `infra/gcp`) passes `tflint` and `terraform test`. The tests pin: the topic IAM matrix, secret-level accessors only, the gates (`serve`, `public_invokers`), the CAA set, every Route 53 record, the WIF condition by IDs, `prevent_destroy` + `version_destroy_ttl = 30d`, no SA key resource |
| B2 | gitops-engineer | `deploy/run/**`, `.github/workflows/deploy.yml`, `.github/workflows/ci.yml`, `.github/actionlint.yaml`, `.github/pull_request_template.md`, `scripts/` (except `dns_compare*`), `Makefile`, `deploy/grafana/**`, `docs/OBSERVABILITY.md`, `web/scripts/check-policy-version.mjs` (only the path it reads) | `actionlint`, `check_workflows.sh`, `make test-scripts`, `make alerts-check`, `make synthetics-check` and the new `deploy/run` check pass in CI. `deploy.yml` does nothing unless `vars.GCP_DEPLOY == 'on'` |
| B3 | gitops-engineer, after B1 | `docs/OPERATIONS.md` → `## GCP` → `### Deploy, roll back, rotate` | step 8, the rollback, the hold and the password rotation as commands |
| B4 | security-privacy-reviewer, sre | read-only | findings in `review-b.md`. Blockers are fixed by the owner, then re-verified |

Order: B1 ∥ B2 → B3 → B4 → fixes → PR → CI green.

## Interfaces A must match

**Names.** Project `ryftlab-prod`, region `europe-west1`.
- Services `api`, `ingest`, `scoring`, `notify`, and the job `migrate`.
- Runtime SAs: `ryft-<svc>@ryftlab-prod.iam.gserviceaccount.com` for api, ingest, scoring, notify and migrate.
- Push and tick tokens: `ryft-push@…`. CI: `ryft-deployer@…`. Grafana: `grafana-reader@…`.
- The `run.app` URL is the deterministic one, `https://<svc>-<project number>.europe-west1.run.app`.
- Images go to `europe-west1-docker.pkg.dev/ryftlab-prod/ryft/<api|ingest|scoring|notify|migrate>`, `linux/amd64`, and are
  deployed by digest.
- The GitHub WIF provider is `projects/<project number>/locations/global/workloadIdentityPools/github/providers/github-actions`.

**HTTP, on all four services.**
- One port, `$PORT` (Cloud Run sets it; `PORT` is reserved there and is never set in `deploy/run`). It serves h2c with prior
  knowledge and HTTP/1.1 on the same port. The container port is named `h2c`.
- `GET /health/live` is the liveness probe. `GET /health/ready` is the startup probe and the synthetics' target: a generic 503
  with the cause only in the log, cached 5 s.
- `POST /internal/tick` runs the service's ticks. The Scheduler job `ryft-tick` runs `*/15 * * * *` (Etc/UTC) and publishes
  `data = {}` with the attribute `ryft-key=tick` to the topic `ryft-tick`. Its subscriptions `tick-<svc>` push it with an OIDC
  token of `ryft-push`.
- `POST /push/<key>` on scoring: `/push/ingested`, `/push/session.completed`. On notify: `/push/scored`,
  `/push/session.scored`, `/push/refresh`, `/push/wait`.
- Every push and tick carries an OIDC token of `ryft-push`, whose `aud` is the service's `run.app` URL (no path). Any 2xx is an
  ack; the handlers answer 204.

**Plain env** (values in git: `deploy/run/<svc>.yaml` and the shared `deploy/run/config.env`):

| Name | Services | Value |
|---|---|---|
| `DATABASE_URL` | api, ingest, scoring, notify | `postgresql://ryft_<svc>.<ref>@<pooler host>:6543/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt&default_query_exec_mode=exec`, no password |
| `DATABASE_URL` | migrate | `postgresql://postgres.<ref>@<pooler host>:5432/postgres?sslmode=verify-full&sslrootcert=/etc/ryft/supabase-ca.crt`, no password |
| `PRIVACY_POLICY_VERSION` | api, ingest | from `deploy/run/config.env` (its new home) |
| `SUPABASE_PROJECT_REF` | api, ingest | from `config.env` |
| `GCP_PROJECT` | api, ingest, scoring, notify | `ryftlab-prod` |
| `PUBSUB_ENDPOINT` | api, ingest, scoring, notify | `https://europe-west1-pubsub.googleapis.com` (the regional endpoint, for ordering) |
| `PUSH_AUDIENCE` | api, ingest, scoring, notify | the service's own `run.app` URL |
| `PUSH_SA_EMAIL` | api, ingest, scoring, notify | `ryft-push@ryftlab-prod.iam.gserviceaccount.com` |
| `PUSH_SA_ID` | api, ingest, scoring, notify | `ryft-push`'s unique ID (the `sub`), Terraform output `push_sa_unique_id` |
| `OTLP_ENDPOINT`, `OTLP_USER` | api, ingest, scoring, notify | Grafana Cloud's OTLP gateway and instance ID. **Empty means no push**, so the services start without them |
| `LOG_LEVEL`, `AUTH_MODE=supabase` (api, ingest), `PRIVACY_POLICY_URL`, `ADMIN_WEB_ORIGINS` (api), `APPLE_TEAM_ID`, `APPLE_KEY_ID`, `APPLE_CLIENT_ID` (api), `APNS_TOPIC`, `APNS_KEY_ID`, `APNS_TEAM_ID` (notify) | as today | as today's `deploy/k8s/base/*/config.env` and Vault keys |

Never in `deploy/run`, as a CI check enforces: `AUTH_MODE=apikey`, `ALLOW_INSECURE_APIKEY_AUTH`, `API_KEY`, `USER_ID`,
`ALLOW_INSECURE_PUSH`, `PORT`, and any secret version `latest`.

**Secrets** (Secret Manager ID → env name → the only SAs that can access it). Each is pinned by version number in `deploy/run`.

| Secret | Env | Accessors |
|---|---|---|
| `db-password-api`, `-ingest`, `-scoring`, `-notify` | `PGPASSWORD` in that service; `DB_PASSWORD_<SVC>` in migrate | `ryft-<svc>`, `ryft-migrate` |
| `db-password-owner` | `PGPASSWORD` in migrate | `ryft-migrate` |
| `supabase-secret-key` | `SUPABASE_SERVICE_ROLE_KEY` | `ryft-api` |
| `admin-api-key` | `ADMIN_API_KEY` | `ryft-api` |
| `apple-key-p8` | `APPLE_KEY_P8` | `ryft-api` |
| `apns-key-p8` | `APNS_KEY_P8` | `ryft-notify` |
| `data-key` | `DATA_ENCRYPTION_KEY` | `ryft-api` |
| `otlp-token` | `OTLP_TOKEN` | `ryft-api`, `-ingest`, `-scoring`, `-notify` |

`/etc/ryft/supabase-ca.crt` is in the image (A's Dockerfile).

**Metrics (OTLP).**
- Resource attributes: `service.namespace=ryft`, `service.name=<svc>`, `service.instance.id` unique per instance. In Grafana
  Cloud they become `job="ryft/<svc>"` and `instance`.
- Metric names arrive in Mimir exactly as the Prometheus registry names them: no unit suffix, one `_total`.
- New: `ryft_ticks_total` (counter, one per handled `/internal/tick`). `ryft_db_up` is set by the tick.
- The alert rules select `job`, not `namespace`.

**The topology tfvars**, `infra/gcp/pubsub.auto.tfvars.json`. B commits it by hand. A's `cmd/pubsub-topology` must write it
byte for byte, and A adds the drift check.
- Format: Go `encoding/json`, `MarshalIndent(v, "", "  ")`, then one `\n`.
- The top level and the three named collections are maps, so their keys come out sorted.
- Entries are structs, with fields in this order:
  - topic: `publishers` (sorted, `[]` when empty, never `null`);
  - push subscription: `topic`, `service`, `path`, `ordered`, `ack_deadline_seconds`, `message_retention_seconds`,
    `dead_letter_topic` (`""` for none);
  - hold subscription: `topic`, `message_retention_seconds`.
- Terraform adds the service agents' bindings itself: Scheduler on `ryft-tick`, and Pub/Sub on the DLQ topics and the source
  subscriptions. They are not in the file.
- The retry policy (10–600 s) and `max_delivery_attempts = 20` are Terraform constants (D218).

## Design fixes found while building
(Each one is also listed in the PR body.)
- **The deployer's `run.developer` is project-level.** A resource-level binding can't exist before the service does, and CI
  creates the services and jobs. It still holds no IAM rights. `actAs` stays on the five runtime SAs only.
- **The WIF condition admits `event_name` `push` or `workflow_dispatch` on `refs/heads/main`.** §11 steps 8 and 10 run
  `deploy.yml` by hand, and `push` alone would refuse those runs.
- **Step 8's first run skips the smoke** (a dispatch input), because nothing is public before apply 2.
- **Ticks have no DLQ and keep messages 10 min.** A failed tick is dropped, and the next one comes 15 min later.
- **Every subscription has `expiration_policy` with an empty TTL.** The default deletes a subscription after 31 idle days.
- **Artifact Registry has vulnerability scanning off.** Scanning is billed per image and isn't on the keep list.
- **Config known only after the bootstrap goes into git** (`deploy/run/config.env`): the project number, `PUSH_SA_ID`, the
  Supabase ref and pooler host, and the Apple/APNs key IDs. The user merges a config PR before step 8, and `deploy.yml`
  refuses to run while a placeholder is left.

## Risks
- CI is the only Terraform test, since `terraform test` doesn't run locally. A red `terraform` job means another round, not a
  merge.
- The pinned action SHAs have to be real tag commits (`git ls-remote`). Dependabot keeps them current.
- The EKS `images`/`promote` jobs in `ci.yml` keep running on main until D3. B leaves them alone.

## Rollback
Nothing is applied, so reverting the PR is the rollback. After apply 1 or 2, the runbook's gate variables undo apply 2. Apply 1
is undone only through D3's removal path, because the zone and the secrets have `prevent_destroy`.

## What the user runs
The runbook is `docs/OPERATIONS.md` → GCP.
- Once B is merged: steps 0–6, and steps 7 and 11 in part.
- Waiting for A: step 7's `smoke-supabase` on 6543, step 8 onwards, and the OTLP half of step 11.
