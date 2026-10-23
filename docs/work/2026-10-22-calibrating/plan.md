# `calibrating: true, baseline_days: 0` on a scored day with history — plan (2026-10-22)

Brief: the user, "start on the calibrating bug next", from the app's `docs/API_REQUESTS.md` → Refresh resilience 1
(TestFlight 1.0.0 `20261021.141128`): an account with ~90 days of history got, for 2026-10-21, `calibrating: true`,
`baseline_days: 0`, `recovery: 75`, `readiness: 80`; the app showed "Baselines · 0 of 7 nights" beside a scored day.
The reporter's id is not given ("to fill in from `scripts/user.sh`"). Lead: platform-lead.

## Hypotheses (from the app; to be proven or refuted)
1. Ordering: today was scored before the days it depends on (backfill order, an account merge's rescore, two scoring
   consumers during a rollout surge, recompute ranges out of order), so its 30-day window had no baselines yet.
2. Data: the account has no HRV or resting-HR nights (phone-only, a watch that doesn't write HRV): then `calibrating`
   is right, but "0 of 7" never ends by waiting (→ `calibrating_reason`, a later contract item).
3. Default: rows still at migration 0006's default `baseline_days = 0` were never rescored.

## Tickets
- **A — root cause (debugger, read-only on production):** find the affected accounts from the data (no reporter id),
  size it, read the reporter's rows (only the named fields), prove the cause with a failing test.
- **B — fix (owner per cause: scoring-scientist for `internal/scoring`, backend-engineer for the engine/consumer
  ordering, database-engineer for a migration):** contract unchanged; a test pins it.
- **C — data repair (lead prepares, user approves):** rescore the affected accounts oldest-first
  (`scripts/rescore_all.sh` or a targeted rescore); verify the rows.
- **D — tell the app:** what was wrong, what changed, whether `calibrating_reason` is still needed (it is, for case 2).

## Findings of A (debugger, 2026-10-22, read-only admin GETs, no SQL)
- Likely reporter **53d437eb** (one of two beta accounts; the other, 671779e8, is the smoke account): `scored/14 = 14`,
  `hrv/14 = 0`, calibrating; no `hrv` record kind at all, older history back to 2026-05. **Case 2**: no HRV source, so
  `baseline_days = min(N_lnHRV, N_rhr) = 0` and calibrating forever; recovery comes from sleep (`Recovery()` averages
  the inputs that exist). Correct, but "0 of 7 nights" never ends → `calibrating_reason` (the app's request 2).
- **Engine gap (case 1, separate):** baselines are read from stored prior rows; nothing re-scores later days when an
  earlier day changes (the 15-minute pass redoes yesterday and today only). A day scored before its history keeps
  `baseline_days = 0` for good. Proven by `TestEngineLateHistoryRescoresTheDaysAfterIt` (branch
  `fix/calibrating-order`, `ad68a48`, failing); controls pass. No production evidence yet (needs SQL).
- Case 3 (0006 default) unlikely: the account was created 2026-10-19.

## Tickets (updated)
- **B1 — `calibrating_reason`** (api-contract-owner first: `docs/API.md` + Swift mirror; then scoring-scientist):
  `short_history | no_hrv | no_rhr | null` (+ `baseline_hrv_days`, `baseline_rhr_days`), as the app proposed.
- **B2 — re-score after a change in the history** (backend-engineer; scoring-scientist and sre review): when a stored
  day's baseline inputs change, re-score the following days (≤ 30, oldest first, up to today); a safety net in the
  periodic pass for `calibrating AND baseline_days < 7` rows whose history now has ≥ 7 nights.
- **C — data repair:** B2's safety net reaches only active users' rows whose `baseline_days` is behind, and is blind to
  accounts without HRV (the reporter's type), so after #29 is deployed: one user-approved `scripts/rescore_all.sh`
  (cheap with two beta accounts; `model_version` stays 2026.10.1), verified with query 1 before and after (expect 0
  rows) and `ryft_days_rescored_total{reason="behind"}` at 0.
- **E — health values in a log line (found by B2's engineer, pre-existing):** `computeDay`'s `scored day` line logs
  hrv, rhr, recovery …, against the logging rule (OPERATIONS → Data protection (logs)) → security-privacy-reviewer,
  then a fix (backend).

Status: B2 → PR #29 (draft, `7f2ba6d`), reviews by code and scoring running; B1 contract `d3e9612`
(`api/calibrating-reason`), implementation on top of #29 running.

**Result (2026-10-22, the user ran query 1): no rows.** No day in production was scored before its history (the
engine gap left no damage), so ticket C is not needed as a repair. A rescore after #29 + B1 deploy only fills the new
`calibrating_reason` fields for past days (optional; today and yesterday get them within one 15-minute pass).

## Read-only queries for the user (Supabase SQL editor; short ids only)
```sql
-- 1. stale rows, windowed like the code (the 30 stored rows before each day; scoring review of #29)
SELECT left(user_id::text,8) u, count(*) rows, min(day), max(day) FROM (
  SELECT user_id, day, baseline_days, hrv_baseline_ms, rhr_baseline,
    count(*) FILTER (WHERE hrv_ms > 0) OVER w n_hrv, count(*) FILTER (WHERE rhr > 0) OVER w n_rhr
  FROM daily_scores WINDOW w AS (PARTITION BY user_id ORDER BY day ROWS BETWEEN 30 PRECEDING AND 1 PRECEDING)) h
WHERE baseline_days < least(n_hrv, n_rhr) OR (rhr_baseline IS NULL AND n_rhr > 0) OR (hrv_baseline_ms IS NULL AND n_hrv > 0)
GROUP BY 1 ORDER BY 1;
-- 2. the reporter's named fields
SELECT day, calibrating, baseline_days, computed_at, hrv_ms, rhr, hrv_baseline_ms, rhr_baseline
FROM daily_scores WHERE user_id::text LIKE '53d437eb%' AND day BETWEEN '2026-07-24' AND '2026-10-21' ORDER BY day;
-- 3. case 2 vs case 3 across all users
SELECT left(user_id::text,8) u, count(*) rows, count(hrv_ms) n_hrv, count(rhr) n_rhr, min(computed_at)
FROM daily_scores GROUP BY 1;
```

## Verify
code-reviewer on B; scoring-scientist if a number can move; security-privacy-reviewer if production data is read
beyond aggregates.

## Needs the user
Approve any production rescore; the reporter's id if the data search finds several candidates.
