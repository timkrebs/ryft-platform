# Calibrating bug — review (2026-10-22)

## A — root cause (debugger)
Read-only admin GETs only (`beta_report.sh`, `user.sh --json`); no SQL, no writes. Likely reporter `53d437eb`: no HRV
source (case 2) — calibrating is correct but never ends. Separate engine gap (case 1) proven by a failing test
(`ad68a48`). Case 3 unlikely. Production row counts need the user's SQL (plan → queries).

## B1 — contract (api-contract-owner)
`d3e9612` on `api/calibrating-reason`: `baseline_hrv_days`, `baseline_rhr_days`, `calibrating_reason`
(`short_history | no_hrv | no_rhr`); rule: null when min(H,R) ≥ 7; `no_hrv` when W ≥ 7 and H = 0 (wins over `no_rhr`);
`no_rhr` when W ≥ 7 and R = 0; else `short_history` (the W ≥ 7 guard keeps a new account at `short_history`). Legacy
rows: null fields even when calibrating (unknown). `make check-swift` could not run (no stack); the mirror was
decoded offline against every example.

## B2 — re-score after a history change (#29, `7f2ba6d`)
scoring-scientist: no blocker. A cascade cannot push for an old day (morning push only for the user's today and the
first recovery, which depends on the day's own data; notify drops other days, D70; event id dedupe); insights are
computed on read. Reach of 30 stored rows is exactly right (`ScoreHistory` is `LIMIT 30` rows). Past numbers move
(a day first scored without history: median 9, p90 21 recovery points); no ModelVersion bump (the rules didn't
change), but SCORING.md must say so. Should-fix: D-entry + SCORING.md note; the safety net isn't the data repair (blind
to no-HRV accounts, active users only) → one user-approved `rescore_all.sh` after deploy; `nap_min` not compared
(probe: next day's need off by 60 min); a cascade lost on retry; pin the push invariant in a test.

code-reviewer: no blocker; change detection NULL-safe and exact; column list matches what `computeDay` reads; window
and termination sound; safety-net query 3.3 ms/user at 2,000 users × 509 days. Should-fix: a half-failed cascade is
lost on retry and invisible to the count-only net (proven); the tests don't pin the column list (cutting 7 of 8
columns kept them green); no D-entry. Nits: today scored twice in the timer; the 50-per-pass budget excludes cascades;
a failing behind-day re-picked every pass; untested bounds (today, 400 cap); the `scored day` log line (pre-existing,
`e4ca28c`, documented in OPERATIONS) multiplies with cascades → ticket E.
→ all back to backend-engineer in one list.

Fixes `c01802c` (redelivery mark `events.Redelivered`), `bd47ee8` (retry finishes the cascade, `nap_min`, one
`historyInputs` list + guard test, safety net for missing baselines, budget/round robin/skip), `bef3ef6` (docs).
Re-verify (code): all done, no new blocker; redelivery marking proven against a real RabbitMQ (a crash-redelivered
message arrives `Redelivered=true`; a first delivery is never marked); a 43-row chain cut at row 35 finishes on the
marked retry. Remaining: the D-entry (lead, docs PR); nits (`catchUp.failed` never pruned; a guard-test message;
the walk stops at the first failing behind-day — pre-existing). Merged as `6eba3bb` (2026-10-22 19:34 UTC,
user-approved; GitHub CI green on bef3ef6). Rollout: main's run green, five service images, promote in environment
`deploy` (19:50:46), deploy PR #32 merged itself 19:54:09, Argo synced 19:57:18 — services on `6eba3bb`, PreSync
migrate and PostSync smoke Succeeded, `/v1/me` 401; scoring logged no safety-net re-scores (no stale rows, as query 1
said).

## Rollout of #30 and the rescore (2026-10-23)
Last rounds before merge: DB fixes (`e6773c6` lock_timeout 5 s + give-up test, `16c2a2e` convergence test), code fixes
(`66a0f20` one `dataColumns` list, `f04bd41` the day's own signal), API.md (`16856b3`), the RHR twin of the
first-morning case (`c5088fb`: a day bringing a signal its window never had is `short_history`), a DB test that
ScoreHistory's HasData uses exactly `dataColumns` (`4ec143c`), API.md's new step (`0062625`; rule in code and doc
compared by the lead: identical). Merged as `51767f3` (2026-10-23 05:05 UTC, user-approved; CI green on 0062625).
Rollout: the merge itself changed no manifest (Argo marked it Synced, no operation); main's run built the images; the
deploy PR's sync at 05:18:02–05:18:40: PreSync migrate logged `migration applied 0027_calibrating_reason.sql` and
"schema current", PostSync smoke Succeeded, api/ingest/scoring/notify on `51767f3`. (The lead's kubectl login had
expired overnight; the user logged in again.)
Rescore (asked by the user): `scripts/rescore_all.sh 30` at 05:19:03 — 2 users, 0 skipped, 0 failed, model 2026.10.1,
5 s. The admin view doesn't carry per-day fields, so the result for 53d437eb is checked with the user's SQL (plan).
Noted again: the rescore's `scored day` log lines carry hrv/rhr/recovery (ticket E).

## E — no health values in logs (#34, `bb89c79`, `2bbc98f`)
security-privacy-reviewer: mergeable, no blocker; a sweep of every slog call and logged error found nothing personal
left on a reachable path; the access line, Postgres errors (Detail excluded), auth, Apple, admin lines and the AMQP
URL are clean; `user_id` (pseudonymous UUID) is consistent with OPERATIONS and the privacy text. Mutations: the old
line, renamed input keys, the APNs/decode/smoke/device changes are caught; **computed scores under new key names
(recovery as `score`, state as `band`) pass** → should-fix 1: an allowlist of keys per line. Should-fix 2: D-entry
replacing DECISIONS:695 ("pod logs carry users' daily scores") — lead, docs PR; 3: OPERATIONS should say a production
rescore's output goes to the operator's terminal (the 2026-10-23 run printed 12 `scored day` lines in the lead's
session, all of the smoke account with zero values; nothing saved); 4: name the two exceptions (panic stack traces;
Supabase admin error bodies — low risk, GoTrue answers `{code, error_code, msg}` for UUID requests). Nits: success-path
log tests (CI has Postgres), two decode cases prove nothing (no numeric field), `zone_minutes` decode errors, anchor
the 14-day window on the deploy, optional: log only status + error_code from GoTrue. DLQ bodies keep morning scores
for 14 days (storage, matches the privacy text). Privacy text: over-describes after deploy (safe); fold the correction
into the next policy change ≥ 14 days after the deploy (a new version asks everyone to consent again).

#34's CI was red (gitleaks: a 64-hex APNs test token literal; PR history is scanned, a fix commit can't clear it) →
recreated without the literal on `fix/logs-without-health-values` (#35; #34 closed), plus all review fixes (key
allowlists per log line, GoTrue status + error_code only, `logx.JSONError`, trace.sh fields, doc exceptions). Security
re-verify: ready. CI then failed on a role race on a fresh Postgres (tests in several packages create the server-wide
service roles at once; `main` had the same flake in an existing test) → `db.EnsureServiceRoles` in the six test setups
(`b4c3a9f`). Merged as `2349692` (07:20 UTC, user-approved). Rollout: services on `2349692` at 07:43:23 (deploy PR
#37), hooks Succeeded; a fresh production `scored day` line carries only `day, inputs_changed, level, model_version,
msg, request_id, service, time, user_id`. Older lines with scores leave CloudWatch by **2026-11-06**.
Follow-ups: pin the allowed keys' value types in the log tests; NUL byte → 422 in ingest/api.

## B1 — implementation (scoring-scientist, `scoring/calibrating-reason`: 1787c7c, 6211a12, on #29 + the contract)
Migration 0027: three nullable columns, a CHECK on the reason values, no default, no cross-column constraint (an older
image writing after a rollback must not fail); grants from 0009 cover them (per-column test). The rule is a pure
function with an exhaustive test (reason null exactly when Recovery isn't calibrating, for all 0 ≤ H,R ≤ W ≤ 30);
engine tests for four source setups; the store serves all three as null when they disagree with `baseline_days` /
`calibrating` (a rollback image rewrote the row). No stored number moved (248 synthetic days, old vs new). No
ModelVersion bump. `make smoke` and `make check-swift` green in a separate compose project.
**Judgement call (accepted by the lead):** W counts days WITH DATA, not every stored row — otherwise a rescore's empty
rows before the first data make a watch user's first night `no_hrv`. Consequence for #29: the presence of steps/active
energy joins `InputsChanged` (B1 and #29's fixes both edit it → merge with care). Contract wording → api-contract-owner.
Pre-existing, noted: the never-scored placeholder has `calibrating: false` with `state: "calibrating"`.

After #29's fixes: merged into B1 (e4ffcff, 8feec2a), conflict resolved into the one `historyInputs` list with
presence entries (`steps IS NULL`, `active_kcal IS NULL`); `c2274f5` adds `baseline_hrv_days < n_hrv` /
`baseline_rhr_days < n_rhr` to the safety net (pre-0027 NULL counts are not picked). All green incl. smoke and
check-swift. PR #30 (draft, stacked on #29).

database-engineer (#30, static): no blocker. The inline CHECK scans `daily_scores` once under ACCESS EXCLUSIVE (NULLs
pass; milliseconds at beta size) — fine, note the row count; the migrator sets no `lock_timeout` → `SET LOCAL
lock_timeout = '5s'` in 0027 (a migrator-wide policy is a follow-up); a convergence test (score, then
`BehindHistoryDays` is empty); grants/RLS correct (notify's column grant excludes the new columns); rollback = re-pin
(additive; `knownCalibration` serves stale columns as unknown); deletion covers the columns; no export endpoint exists
(an export built on GET scores includes them). Nit: check `docs/API.md` ~604 (`calibrating` text).

code-reviewer (#30): no code blocker. Merge procedure: #29 was squashed (`6eba3bb`, same tree as `bef3ef6`) → `git
merge -s ours origin/main` after a tree check. Should-fix: (1) `no_hrv` on the first morning a watch's HRV arrives (the
day's own signal isn't counted; reproduced) → `no_hrv`/`no_rhr` only when the day's own value is nil too — a contract
rule change; (2) HasData's SQL columns not tied to the guard (adding spo2 kept tests green) → one slice; (3) D-entry
(lead); (4) changelog row out of order; (5) `short_history` row wrong for new accounts; nits: closed-enum wording
contradicts itself (the mirror is strict: an unknown value fails the decode), NOT VALID option for the CHECK (kept
inline + lock_timeout), `knownCalibration` can serve stale agreeing values after a rollback, HasData misses some data
kinds (errs to `short_history`), a mirror comment. Verified: JSON shape (Swift mirror compiled and decoded every
example and the placeholder), counts H, R ≤ W and equal to the SQL's, no re-pick, injection-safe presence entries.
→ engine/store/tests: scoring-scientist; API.md wording: api-contract-owner after.
