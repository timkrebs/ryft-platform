# Platform answers to `../app/docs/API_REQUESTS.md` (2026-10-22)

For the iOS agent to fold into its file (the platform never edits `../app`). "Lands with PR #n" = the contract is
written and reviewed; the field is served once that PR is merged and deployed (STATUS says when).

## Universal links: the AASA — resolved
Live since 2026-10-22: `https://ryftapp.de/.well-known/apple-app-site-association` serves one `applinks.details` entry
for `M77SKHKG63.dev.timkrebs.ryft` with `/day/*`, `/sleep/*`, `/insights/*`, `/train/sessions/*`, `/train/session/*`,
`/train/exercises/*`, `/train/plan/*`, `/train/history`, `/invite/*` and `/auth/*`, as `application/json` without a
redirect (checked against Apple's CDN copy). The app can add `applinks:ryftapp.de` (Signing & Capabilities); no other
change. An "Open in RYFT" fallback page per path exists on the site.

## Refresh resilience
1. **`calibrating: true, baseline_days: 0` — answered.** The reporter is most likely `53d437eb`: that account has no
   HRV source at all (0 HRV nights; history back to 2026-05), so `baseline_days = min(HRV nights, RHR nights) = 0`
   and the day stays calibrating — correct, but it never ends by waiting; recovery comes from sleep alone. Production
   holds no day scored before its history (checked 2026-10-22: 0 rows), so no rescore is needed for the report.
   Found on the way and fixed (PR #29): when earlier history arrives late (a late HRV sample, a backfill newest
   first), scoring now re-scores the following days. **Past days' numbers can therefore change after the fact** with
   the same `model_version`; the app already re-reads its history on each refresh.
2. **`calibrating_reason` — accepted, lands with the calibrating PR (after #29).** `DailyScore` gets
   `calibrating_reason: "short_history" | "no_hrv" | "no_rhr" | null`, `baseline_hrv_days` and `baseline_rhr_days`
   (API.md → `#### calibrating_reason`, changelog 2026-10-22). Rule: null when both counts ≥ 7; `no_hrv` when the
   30 scored days before hold ≥ 7 days with data but no HRV night (wins when both are missing); `no_rhr` likewise;
   else `short_history`. Days without any data (e.g. stored before the first sync) don't count. Null fields on a
   calibrating day = unknown (scored before the field existed). Don't show "N of 7 nights" for `no_hrv` / `no_rhr`.
3. **`min_build` / `426 app_update_required` — queued** (next batch).
4. **Idempotency key for `POST /v1/feedback` — queued** (next batch).

## Charts and gauges
1. `strain_band` — queued (next batch). 3. baseline σ for HRV and resting HR — queued. 4. `resp_rate` / `spo2`
trends — queued.
2. Strain target — **not now:** it belongs to M3, deferred until after the beta (D58). Keep the target arc empty.

## Sync and performance
1. gzip on `POST /v1/sync`, 2. HealthKit UUIDs and deletions, 3. pre-aggregated samples — queued as one sync batch;
the 1-minute heart-rate question goes to scoring and will be answered in `SCORING.md` with it. 4. Agreed: nothing new
needed.

## Train
1. Deleting a set in progress, 2. the paused enrolment — queued. 3. RYFT's own Apple Health workouts — the answer
(skip vs link by `session_id`) comes with the Train batch; keep skipping until then.

## Bedtime & wake
1–4 (sleep schedule on the account, planned vs actual, `BEDTIME` push, `day=` / `tz=`) — queued as one sleep batch.

## NUL in requests (2026-10-23, D118)
Strings must not contain U+0000. The platform now answers a NUL in user-entered text (feedback,
journal, Train names and notes, profile) with `422` and stores nothing; strip U+0000 from what the
user types or pastes before sending (low priority). `POST /v1/sync` removes it itself from every
string and accepts the page, so HealthKit-derived names need no app change. No model changes.
