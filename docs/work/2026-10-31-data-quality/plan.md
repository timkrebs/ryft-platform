# Data quality: deletions, duplicate workouts, night coverage, complete-night push

Brief: `CLAUDE_FOLLOWUP_DATA_QUALITY.md`. App half: `../app/CLAUDE_FOLLOWUP_DATA_QUALITY.md`.
Start state: `main` at `7f0f8db`, model `2026.10.2`, last migration `0028`. Open PRs touching nothing here
(#71, #65, #62, #57, #55, #53, #51, #47). The tree has untracked `.claude/` and `web/CLAUDE_DESIGN_PROMPT.md`;
neither is ours, neither is touched.
`kubectl` current context is `admin@homelab` (not prod) and has no credentials: no live reads were possible.
Production state is assumed from `overlays/eks` pins (`e94b9b9` backends, `1407053` web).

## Order

0. **platform-architect**: design note `design.md` (below). Gate for everything else.
1. **api-contract-owner**: Q1 + Q2 + Q3 + Q5 contract in `docs/API.md` (three changelog entries, Swift mirror,
   `make check-swift`). Goes first so the app is unblocked; merged as its own docs-first commit on PR 1.
2. Build (disjoint file sets; PR 1 is the base, 2 and 3 branch from it only where they share `sync` code):
   | PR | Owner | Files | Depends on |
   | --- | --- | --- | --- |
   | 1 `ingest: HealthKit ids and deletions in sync` | backend-engineer + database-engineer (migration 0029 `hk_id`) | `cmd/ingest`, `internal/store`, migration | contract |
   | 2 `train/strain: fold duplicate workouts, one-off cleanup` | database-engineer (migration 0030 `duplicate_of`, fold SQL) + scoring-scientist (rule as pure fn) + backend-engineer (`scoring fold-workouts`, `also_recorded_by`) | `internal/scoring/workout*`, `cmd/scoring`, store | PR 1 merged (shares ingest path) |
   | 3 `scoring: night coverage, floors, provisional and final` | scoring-scientist + database-engineer (migration 0031 coverage columns) | `internal/scoring`, `cmd/scoring` | design |
   | 4 `notify: morning score on the final night` | backend-engineer | `internal/notify` | PR 3 |
   | 5 `observability: data-quality metrics and alerts` | sre | `deploy/grafana`, `docs/OBSERVABILITY.md`, counters in code after 1-4 land | PR 1-4 |
   | 6 `api: sources with 7-day counts; trackers.json sync cadence` | backend-engineer (`/v1/me`) + web-engineer (`trackers.json`, en+de) | `cmd/api`, `web/content/trackers` | contract |
   test-engineer writes the failing integration tests per PR from the acceptance criteria below, in parallel with the owner.
   Model version bump is **once**, in PR 3 (scoring-scientist decides; PR 2 changes shown counts and `muscular_load`
   attachment, so the single bump covers both and ships with one `rescore_all.sh`). If PR 2 deploys first, it carries
   no bump and the rescore waits for PR 3.
3. Verify (read-only, parallel per PR): code-reviewer always; security-privacy-reviewer on PR 1 (a delete path over
   user data, ownership check on every id), PR 2 (admin subcommand), PR 6; sre on PR 3-5.
4. Ship per PR; `gh pr checks --watch`; debugger on red.

## Design questions for platform-architect (answer in `design.md`, D-entries to follow)

- D-a: `hk_id` partial unique index `(user_id, hk_id) WHERE hk_id IS NOT NULL` on `samples` and `sleep_segments`;
  how the existing upsert conflict targets change, and how a record that arrives with an `hk_id` after the same
  row arrived without one is handled (no double rows).
- D-b: deletion transaction: delete + queue touched days into `score_queue` + outbox event in one tx; per-kind
  ownership scoping; `workouts.session_id` detach; a deleted workout's HR window samples are not deleted.
- D-c: fold on ingest vs. in the scoring consumer; concurrency with a concurrent sync of the same user (advisory
  lock or order); what "most recently uploaded" means (needs an `uploaded_at`/`created_at` check).
- D-d: `duplicate_of` as a self-FK, cascade on delete of the primary, how queries exclude duplicates
  (view or predicate), and the cross-source primary choice that needs HR samples in the window.
- D-e: `coverage` storage: JSONB column on `daily_scores` vs. typed columns; `status` re-score trigger and the
  10:00-local ceiling (needs the user's timezone: where it lives today); loop risk between "more night arrived",
  re-score, and the history-changed path (#29, D112).
- D-f: `MORNING_SCORE` gate state: how notify knows "first final of the day" idempotently (D100), including a day
  that never becomes `night_complete` (the 10:00 rule must still emit a final).

## Acceptance criteria (tickets, testable)

Q1: sync with `deleted` removes only this user's rows of the named kind; wrong-user or unknown id counts in
`ignored.deleted.unknown_id`, no error; re-send is all `unknown_id`; > 2 000 is a `422`; touched days appear in
`score_queue`; deleting a workout with `session_id` keeps the session and its muscular load returns to the day;
records with `hk_id` are unique per user, without keep today's rules; response carries `deleted: {samples,sleep,workouts}`.
Q2: same-source rule at 120 s vs 121 s on both ends; keeps newest upload, moves `session_id`; ingest and
`scoring fold-workouts [-user] [-dry-run]` agree; dry-run changes nothing; second run folds nothing; cross-source
marks shorter (or one without HR) `duplicate_of`, API omits it and adds `also_recorded_by`.
Q3: 119/120 min and 59/60 min floors; `night_complete` at 29 vs 31 min and 09:59 vs 10:00 local; provisional → final
by re-scoring; `coverage` shape as briefed; `details.hr_minutes` unchanged; notify sends `MORNING_SCORE` once, on the
first `final`, silent refresh on every scoring.
Q4: counters as briefed; `ManyUsersStale`, `ProvisionalAllMorning` with promtool tests; dashboard row.
Q5: `sources[]` has `bundle_id`, `name`, `last_seen`, `samples_7d`; `trackers.json` `sync` en+de, ETag unchanged mechanism.
`make smoke` gains a deletion and a fold step.

## Risks

- **Deletion is destructive and irreversible**: ownership scoping and idempotence get a dedicated security review;
  the fold is destructive too, so `-dry-run` first on the reporter's account (expect 6 folds on 2026-10-20).
- **Rescore storm**: deletions and folds queue days; bound by the existing queue; the full `rescore_all.sh` runs once.
- **Gate can silently delay the push**: the 10:00-local ceiling and `ryft_morning_push_delay_seconds` are the guard;
  `ProvisionalAllMorning` the alarm. Timezone missing for a user → treat as final (document).
- **Migrations 0029-0031** are additive/nullable; the migrate PreSync hook applies them; no backfill in-migration.
- **Floors change stored numbers** (rhr/sleep null below floor) → visible score changes for thin nights; flagged in changelog.

## Rollback

Each PR reverts by re-pinning the previous images (OPERATIONS → Deploy and roll back); migrations stay (additive).
The app ignores unknown fields; the platform ignores a missing `deleted`. A fold is not reversible: take a
`workouts` backup query of the dry-run list before the production run.

## Needs the user

- Merge order and the production run of `scoring fold-workouts` (reporter's account `-dry-run` first), then
  `scripts/rescore_all.sh` (runs in the *current* kubectl context: set `ryft-prod-euc1` deliberately, then reset).
- Grafana alert rules: confirm they load (rules are in git; provisioning path per OPERATIONS).
- No Terraform, no secrets, no GitHub settings in this work.

## Revision 1 (2026-10-31): design.md and the user's decisions

User decisions: U1 the RHR floor gates the Recovery input only (stored `rhr` stays); U1b `night_complete` = wake
evidence (sample from the winning sleep source ≥ 30 min after its last segment, phone steps for sleep-only apps) or
10:00 local; U2 the unknown count is `ignored.<kind>.unknown_id`; U3 cross-source = same activity (or `other`) and
≥ 80 % overlap of the shorter workout. U4 (size of `samples`) is open: the user runs the read-only count.

Plan changes from design.md (verified against the code where marked *):
- Fold subcommand is `ingest fold-workouts`, not `scoring` (ingest is the raw-row writer) and migration adds
  `DELETE` for `ryft_ingest` on samples, sleep_segments, workouts.* Match on `source` (workouts store no bundle id).
- Migrations 0029-0032, numbered in merge order; the `hk_id` index in its own file.
- The morning gate lives in scoring (event emitted when a day first becomes final with recovery and sleep)*, not
  notify; PR 4 shrinks to the delay metric in notify. Plan PR 3 therefore carries the gate.
- `ProvisionalAllMorning` is replaced by an alert on the share of nights final only at the ceiling;
  `ManyUsersStale` needs a gauge from `user_sources.last_seen`.
- Account merge needs an `hk_id` guard and `duplicate_of` has no FK.
