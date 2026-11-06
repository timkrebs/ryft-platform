# Production hardening (2026-11) — plan

Brief: `CLAUDE_FOLLOWUP_PRODUCTION_HARDENING.md`. Six phases, about twenty PRs, behaviour unchanged.
Written 2026-11-04 by platform-lead. Status at writing: **planned, nothing started**; blocked on the gate below
(the body below is that original plan, unchanged).

> **Status as of 2026-11-05** (added at the close-out; PR numbers checked with `gh pr list`; the current record is
> `docs/STATUS.md` → "Production hardening (2026-11)" and `docs/DECISIONS.md` D163–D183). The brief
> `CLAUDE_FOLLOWUP_PRODUCTION_HARDENING.md` is not in the repository.
>
> - **Gate:** #82 merged (2026-11-04 06:32 UTC); #71, #53, #55, #57 and #102 merged on 2026-11-04.
> - **Phase 0 done:** P0-1 golden fixtures #90 (CI guard #93, D149); P0-2 coverage baseline and gate #89 (D148);
>   P0-3 load baseline #97; P0-4 schema snapshots and API.md examples #87. P0-0 left no paragraph in this directory;
>   the `lint` target exists since #99.
> - **Phase 1 done:** package docs #96, #94, #95 (revive `exported` on); decision references #102; package map and
>   dependency rules #99; error actions in API.md #100.
> - **Phase 2 done:** A1 `internal/app` #105; A2 scoring and notify on `app.Run` #117 (pinned since #130); B1 `model` split #110;
>   C1 `computeDay` in steps #111 + #112; C2 `ApplySync` in steps #122 + #123; C4 `trainImport` in steps #119 + #120.
> - **Phase 2 not done:** A3 (api, ingest on `app.Run`), A4 (amqpbus finishes the delivery in hand), A5 (readiness and
>   ordered shutdown for api and ingest), A6 (config in one place), A7 (5 s drain delay), B2 (error taxonomy), B3 (store
>   split), B4, C3 (ingest sync in steps), L1 (funlen lint).
> - **Phases 3, 4, 5:** no PR of this run covers them.
> - **Where the run differs from this plan:** `scoring rescore -dry-run` does not exist (#112: the golden comparison
>   stands in); the brief's `pickSources` step was dropped (#112); the decision-reference check runs in the `bytes` job,
>   not the `lint` job (#102); the shutdown budget numbers of `phase2-design.md` §1.4 were replaced by #105's (D169).

## Gate before Phase 0 (needs the user)

PR #82 (`train/fold-duplicate-workouts`) is open, bumps `ModelVersion` 2026.10.2 → **2026.10.3** and moves stored
numbers (`details.workouts`, `duplicate_of`). Its only red check is GitGuardian. Golden fixtures pin the model
version and the scored rows, so building them on today's `main` would be regenerated within days.
**Decision: merge #82 (and let its deploy + `scripts/rescore_all.sh` run) first; Phase 0 branches from the
resulting `main`.** Also open and touching our areas: #71 (amqpbus permissions race, Phase 3/4), #53 (DLQ
replay redelivery, Phase 3 "redelivery marker"), #55/#57 (admission signals, alerts). Merge or close these
before the phases that overlap, or the PRs conflict. Untracked `web/CLAUDE_DESIGN_PROMPT.md` is not ours;
it stays out of every commit.

## Rules for every ticket

- Branch from `main`, own worktree (`isolation: worktree`), ≤ ~600 changed lines, green on
  `make test lint alerts-check smoke` (the Makefile has no `lint` target by that name today; P0-0 settles it).
- Proof of "behaviour unchanged": golden fixtures + `make smoke` + `scoring rescore -dry-run` = 0 changed days;
  `ModelVersion` untouched. A behaviour change says so in the title and gets its own `D<n>`.
- Reviews per PR: code-reviewer always; security-privacy-reviewer at the end of each phase (and on
  auth/logging/RLS PRs); sre on anything touching runtime, metrics, alerts, rollouts. Findings in `review.md`.
- Implementers never approve their own work. No terraform apply, no kubectl mutation, no tfstate.

## Phase 0 · Safety net (test-engineer + scoring-scientist; alone, first)

| # | PR | Owner | Acceptance |
|---|----|-------|-----------|
| P0-0 | Inventory: Makefile targets, synthetic generator API, CI job layout | test-engineer | one paragraph in this dir; decides the `lint` target name |
| P0-1 | `test: golden scoring fixtures` | scoring-scientist (+ test-engineer) | 5 users × 30 days in `internal/scoring/testdata/golden/`, deterministic; `go test -run Golden` fails on a one-constant change; `make golden-update` refuses to run without a `ModelVersion` change |
| P0-2 | `ci: coverage baseline and gate` | test-engineer + gitops-engineer | per-package profile artifact, table in job summary, gate fails at > 1 pt drop vs `main`; number in STATUS |
| P0-3 | `test: load baseline` | test-engineer + sre | `scripts/load.sh`, k6 pinned by digest; p50/p95 per route + `ryft_sync_to_score_seconds` p95 in `OBSERVABILITY.md → Baselines` |
| P0-4 | `test: response schema snapshots, API.md examples decode` | api-contract-owner + test-engineer | snapshot per response type; every `API.md` example decodes with unknown fields disallowed |

P0-1 depends on the gate. P0-2/3/4 are disjoint from P0-1 and run in parallel after P0-0.
Risk: golden JSON churns on benign map ordering → canonical marshal; fixtures must not be secret-looking
(GitGuardian, see memory). Rollback: revert; no runtime effect.

## Phase 1 · Docs in code (backend-engineer, platform-architect for the map)

1. `docs: package docs and exported comments` ×3 (28 `doc.go`; `revive exported` on, zero `nolint`).
2. `docs: decision references in code` (+ `scripts/decision_refs.sh` as a lint warning).
3. `arch: package map and dependency rules` (`scripts/pkgmap.sh`, `internal/arch_test.go`).
4. Error-message table in `API.md` + a test that greps doc and code (api-contract-owner).
Each PR comment-only except 3/4. Review: code-reviewer.

## Phase 2 · Structure (platform-architect designs first → `design.md` + D-entries)

Order matters: `errs` and `config` before the splits that touch the same call sites.
1. `app: shared service bootstrap` (design first: shutdown order, `/readyz` shape, drain flag; D).
2. `config: all settings in one place` (36 `os.Getenv`; all-errors-at-once; `Redacted()`).
3. `errs: one error taxonomy` (89 `%w` fixes; NotFound-per-store-method test). May need 2 PRs.
4. `model: split by domain`, `store: split by table group` (pure moves; run after 2–3 to avoid conflicts).
5. `scoring: computeDay in steps`, `ingest: sync handler in steps`, `ApplySync`, `trainImport`.
Risk: 5 is where numbers could move; golden + dry-run rescore are the gate. Single writer per file set.

## Phase 3 · Reliability (sre, database-engineer, backend-engineer)

Request deadlines + `statement_timeout`; goroutine lifecycle (`errgroup` is already in the module graph
as `x/sync`, indirect; promoting it needs a D line, goleak is new → D); broker audit (decide
`maxSurge: 0` vs advisory lock for scoring, D); JWKS stale-while-revalidate + breaker;
idempotency tests per consumer; `Add(0)` for the eight counters + promtool; ingest back-pressure
(contract change in `API.md` → Conventions first, api-contract-owner). Back-pressure and deadlines are the
only PRs with runtime behaviour change: titled as such, own D, sre review, staged via the dark-flag pattern
where possible. Re-run the load baseline at the end.

## Phase 4 · Tests and CI · Phase 5 · Operability and data

As in the brief. User-only items: Supabase settings (`pg_stat_statements`, retention), the restore drill
(dashboard), the samples-retention decision (database-engineer proposes with numbers, user decides).

## Needs you (so far)

1. Decide the gate: merge #82 first (recommended), or have Phase 0 start on current `main` and regenerate.
2. Say whether #71, #53, #55 may merge/close before Phase 3.
