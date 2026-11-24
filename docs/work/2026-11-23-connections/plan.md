# Direct connections, Garmin first: phase 1 (2026-11-23)

Brief: head-architect's "direct connections, Garmin first" (2026-11-23); root plan
`../docs/work/2026-11-23-connect-and-onboarding/plan.md` §3–§4. Phase 2 (the Garmin adapter) is **not started**: no
programme documentation or credentials are within reach (nothing in `plattform/`, nothing in the root plan's folder).

Taken by open PRs: D248 (#228), D251–D256 (#231, #232), migrations 0039–0041. This work: **D257…**, **migration 0042**.

## Tickets

| # | Owner | What | Acceptance | Branch / PR |
|---|---|---|---|---|
| T1 | platform-architect | D257 + design note `docs/work/2026-11-23-connections/design.md`: tables, roles, the hook, events, inbox lifecycle, rate-limit class, data on disconnect (with security-privacy-reviewer), what PR 2 vs PR 3 owns, file ownership | Every open question of the brief answered; file sets disjoint per implementer | PR 1 `connect/contract` |
| T2 | api-contract-owner | `docs/API.md` "Connections" (changelog 2026-11-23), JSON examples, Swift mirror | `make check-swift` typechecks; additive only | PR 1 |
| T3 | database-engineer | `0042_connections.sql`: `connections`, OAuth state, provider inbox, `origin` columns, grants, RLS; deletion and export coverage | Migration applies on a fresh DB in CI; grants per role as in D257 | PR 2 `connect/foundation` (on PR 1) |
| T4 | backend-engineer | `internal/connect` (Provider, fake), api routes, ingest hook (verify, bound, inbox + outbox in one tx, 200), consumer (idempotent, consent, apply path), inbox retention tick + metric, topology + outputs, per-IP class, flag `connect.garmin` (migration row via T3), `DELETE /v1/me` retry | Handler + idempotency tests; `go vet`/`go test` green | PR 2 |
| T5 | infra-engineer | `infra/gcp`: client-secret secret(s) and accessor for api, edge class for the hook (D242), plan command | `terraform validate`/`test` green; nothing applied | PR 2 |
| T6 | sre | alerts (pushes by outcome, inbox age, 80 % cap, connected-no-data 36 h), `OBSERVABILITY.md`, `OPERATIONS.md` runbooks | `terraform test` green; runbooks name exact commands | PR 2 |
| T7 | test-engineer | smoke: connect fake, push an RMSSD night, expect a scored day with `direct` + `rmssd`; integration tests | CI smoke green | PR 2/3 |
| T8 | scoring-scientist | two HRV metrics, one night one record, light→core, re-validated nights; model version, `SCORING.md`, goldens | Calibration tests pin both metrics; goldens regenerated | PR 3 `connect/scoring` (on PR 2) |
| R | code-reviewer, security-privacy-reviewer, sre | per PR, read-only, findings → `review.md` | no open blocker | — |

## Order and risks
T1 → T2 → PR 1 opened. T3 → T4 ∥ T5 ∥ T6 (disjoint files per T1) → T7 → PR 2. T8 → PR 3.
Risks: the API.md changelog and `DECISIONS.md` conflict with #231/#232 (merge main in before review); the stacked
PRs need re-basing after a squash merge (`-s ours` after an equal-tree check); smoke and Postgres only run in CI.
Rollback: everything ships dark (`connect.garmin` off, fake provider only in compose); 0042 is additive.

## Needs the user
Terraform apply (secrets, edge rule); creating the client secret once Garmin approves; phase 2 inputs (docs, credentials).

## Added 2026-11-23: D259 (the user's answers to design §17)
| # | Owner | What | Acceptance |
|---|---|---|---|
| T9 | backend-engineer | `CONNECT_MIN_POLICY_VERSION` in ingest config; the hook (`store.StoreInbox`) and the consumer accept consent not withdrawn and ≥ the cutoff; empty = the policy in force | config tests (date, not after `PRIVACY_POLICY_VERSION`); store + consumer tests: older-but-≥-cutoff kept, below cutoff and withdrawn dropped |
| T10 | gitops-engineer | `CONNECT_MIN_POLICY_VERSION=` in `config.env`, ingest.yaml env, `check_cloudrun.py` (date, ≤ policy version) | `make cloudrun-check` + script test |
| T11 | sre | OPERATIONS runbook: when to move the cutoff (a policy change touching health-data processing) | runbook names the exact edit |
