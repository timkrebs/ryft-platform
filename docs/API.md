# RYFT HTTP API — v1

This file is the contract between the platform and the RYFT iOS app. The app's
wire types (`../app/Ryft/Networking/Models.swift`) mirror the shapes below and
`backend/internal/model/model.go` 1:1.

**Compatibility rule.** v1 only changes additively: new endpoints, new optional
query parameters, new response fields. A field is never renamed, removed or
retyped without moving to `/v2`. Clients must ignore response fields they do not
know. Response enums are **closed** in v1 — `state`, `quality_label`, sleep `stage`,
`severity`, `calibrating_reason` will not gain values without `/v2`, so they can be decoded as Swift enums.
`Insight.id`, `Insight.metric` and `Workout.activity` are **open** strings.

## Screens → endpoints

| Screen | Numbers | Endpoint |
| --- | --- | --- |
| Today | readiness ring + state, Sleep / Recovery / Strain tiles, HRV, RHR, sleep need and debt, why a day is calibrating (`calibrating_reason`, `baseline_hrv_days`, `baseline_rhr_days`) | `GET /v1/days/{today}` → `score` |
| Today — provisional night, "based on …", which source won | `Provisional` line while the night is incomplete, hours of heart rate and HRV readings behind the numbers, the sleep and heart-rate sources | `GET /v1/days/{today}` → `score.coverage` |
| Today — Trends card | 7-day readiness line | `GET /v1/trends?metric=readiness&range=7d` |
| Sleep — Day | total, stage bar (hypnogram), stage minutes and %, quality | `GET /v1/days/{day}` → `sleep` |
| Sleep — Week / Month | nightly totals and stages, consistency, bedtime / wake strip, averages | `GET /v1/sleep?range=week` / `month` |
| Sleep — Planner | three bedtimes for a wake time, the need behind them | `GET /v1/sleep/plan?wake=07:00` |
| Strain | day strain, zones, TRIMP, steps, active kcal, activities (a workout two sources recorded is listed once, `also_recorded_by`) | `GET /v1/days/{day}` → `score.strain`, `strain`, `workouts` |
| Strain — workout | HR curve, zones, avg/max HR, TRIMP, strain | `GET /v1/workouts/{id}` |
| Insights | cards; charts behind them | `GET /v1/insights`, `GET /v1/trends?metric=…` |
| Profile | timezone, max HR, sleep baseline (learned or set by hand); apply & recompute | `GET`/`PATCH /v1/me`, `POST /v1/recompute` |
| Profile — Delete account | erase all data and the sign-in | `DELETE /v1/me` |
| Onboarding — invite | redeem the beta invite code | `POST /v1/me/invite`, `User.beta_access` |
| Onboarding — consent; Profile → Privacy | agree to (or withdraw) health-data processing for the current policy | `GET /v1/config` → `privacy_policy_*`, `POST`/`DELETE /v1/me/consent`, `User.consent` |
| Onboarding — about you | birth year, max HR | `PATCH /v1/me` (`birth_year`, `max_hr`, `max_hr_manual`) |
| Profile — Data sources; tracker setup | which apps and devices send what, and how much of it in the last 7 days (a source without HRV shows `hrv_sdnn: 0`) | `User.sources` (`kinds`, `samples_7d`) |
| Profile — Report a problem | message, diagnostics, request IDs | `POST /v1/feedback` |
| (any score) | which model computed it, and what changed | `model_version`, `GET /v1/model` |
| Profile — Notifications | per-category toggles, quiet hours | `User.notifications`, `PATCH /v1/me/notifications` |
| (launch, then every 6 h) | feature flags | `GET /v1/config` |
| (after the first sync, on every token change; sign-out) | push registration | `POST` / `DELETE /v1/devices/{id}/push` |
| Notifications | morning score, scored session, record, no new data; silent refresh | **Push notifications** below |
| (after Sign in with Apple) | let deletion revoke the Apple grant | `POST /v1/me/apple-authorization` |
| Strain — Train | start a workout, today's plan, live session, summary with load and PRs | `GET /v1/plans/today`, `PUT /v1/sessions/{id}`, `POST /v1/sessions/{id}/complete` |
| Strain — Train: plans | list, detail, plan builder, enrol / pause | `GET /v1/plans`, `GET`/`PUT`/`DELETE /v1/plans/{id}`, `POST /v1/plans/{id}/enroll`, `DELETE /v1/plans/enrollment` |
| Journal (Today card, morning push) | yesterday's behaviors as toggles, a count or a time; edit 14 days back | `GET`/`PUT /v1/journal/{day}` |
| Insights — Behaviors | "Alcohol · −14 recovery (n = 12/31)" with its interval; locked ones with the days still needed | `GET /v1/behaviors/impact` |
| Profile — Journal | choose behaviors, add or remove custom ones | `GET`/`PATCH`/`POST /v1/behaviors`, `DELETE /v1/behaviors/{id}` |
| Strain — Train: library, history | exercise search, custom exercises, e1RM chart and PRs, past sessions | `GET`/`POST /v1/exercises`, `PATCH`/`DELETE /v1/exercises/{id}`, `GET /v1/exercises/{id}/history`, `GET /v1/sessions` |
| Strain — Train: import | plans and workout history from another training app's export; label imported sessions; start the imported plan where the user is | `POST /v1/train/import`, `Session.origin` / `import_source`, `POST /v1/plans/{id}/enroll` (optional position) |
| (background) | HealthKit upload, and the deletions HealthKit reports | `POST /v1/sync` (`deleted`, `Sample.id`) |

## Conventions

| Topic | Rule |
| --- | --- |
| Base URLs | Two services. **ingest** serves `POST /v1/sync`; **api** serves everything else. Behind the Ingress both share one host (`/v1/sync` → ingest, other `/v1/*` → api). Production: **`https://api.ryftapp.de`** for both (HTTPS only, port 80 closed; live once Supabase auth ships, M1). Configure the app with that one URL and leave the ingest URL empty. |
| Auth | Every `/v1/*` request carries `Authorization: Bearer <access token>`: the Supabase Auth access token of the signed-in user (a JWT; Sign in with Apple through Supabase). See **Authentication** below. `/healthz`, `/readyz`, `/metrics` are unauthenticated. |
| Users | Every response is scoped to the token's user. There is no sign-up call: the platform creates the user on the first authenticated request. |
| Content type | Requests and responses are `application/json`, UTF-8. Request bodies reject unknown fields (`400`). Max request body 32 MiB for `POST /v1/sync`, 1 MiB for `POST /v1/train/import`, 256 KiB for `PUT /v1/sessions/{id}` and `PUT /v1/plans/{id}`, 64 KiB for everything else (`413`). |
| Text | Strings never contain U+0000 (NUL), which the database cannot store: a `\u0000` in a string of a request body is `422`, `%00` in the path or query `400` (Errors → **NUL**). Strip it from every string the app sends (typed text and HealthKit-derived names); `POST /v1/sync` removes it itself and accepts the page. |
| Keys | `snake_case`. The app decodes with `.convertFromSnakeCase`. |
| Timestamps | RFC 3339 strings, e.g. `"2026-10-18T05:12:00Z"`; fractional seconds may appear. |
| Days | `YYYY-MM-DD`, a calendar day in the user's timezone (`users.tz`, see `GET /v1/me`). The night that *ends* on day D belongs to D. |
| Nullability | A field typed `T?` below is `null` when unknown; a few `Workout` fields are *omitted* instead (noted per field), so decode every `T?` as optional. Fields not marked `?` are always present and non-null. |
| UUIDs | Every id in the body of `POST /v1/train/import` (plan, day, plan exercise, exercise, session, set, `exercise_id`, `plan_day_id`, `plan_exercise_id`) is parsed as a UUID, so **upper or lower case is accepted** (Swift's `JSONEncoder` writes upper case) and means the same id: comparisons, `id_taken`, `newer_on_server` and the stored value do not depend on the case sent. Every id the server writes, `results[].id` of `POST /v1/train/import` included, is **lower case**; compare ids as UUIDs, not as strings. |
| Request IDs | Every response, success or error, carries `X-Request-ID` (generated by the server; a client-sent `X-Request-ID` is only logged next to it, as `client_request_id`). Error bodies repeat it as `request_id`. Show or log it when a request fails: it finds the request in the server logs. |
| Device | Send `X-RYFT-Device-ID: <device_id>` (the install's id, the one in `POST /v1/sync`) with every request. Optional; `GET /v1/me` uses it for `account_hint`. |
| Response headers | Every response sends `Cache-Control: no-store` (health data is never cached), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`, `Referrer-Policy: no-referrer` and `Strict-Transport-Security`. |
| Rate limits | Per user: bursts of 60 requests, then 10 per second (`POST /v1/sync` and `PUT /v1/sessions/{id}`: 2 per second each, their own budgets). Normal app use never gets near it. Over the limit: `429` with `Retry-After` (whole seconds). `POST /v1/train/import` has its own budget, bursts of 5, then one every 5 seconds (one token however many items a request carries), and **one import per user runs at a time**: a second one while the first is running is `429` with `code` `import_in_progress` and `Retry-After: 10`. |
| Import limits | `POST /v1/train/import`: body ≤ 1 MiB (`413`), ≤ 5 plans and ≤ 50 sessions per request (`422`), ≤ 2 500 sets per request (`422`); each session ≤ 50 exercises, ≤ 50 sets per exercise and ≤ 300 sets in all (else that session is `rejected` `invalid`), each plan as `PUT /v1/plans/{id}`. |
| Timeouts | The server gives an API request 15 s, a sync and an import 50 s of database work; past that it answers `503 {"error":"request timed out"}` with `Retry-After`. Sync is idempotent, so retrying it is safe. |
| Units | Durations in whole minutes (`*_min`), heart rate in bpm, HRV in ms (SDNN), strain on the 0–21 scale, scores 0–100. Percentages (`pct`, `efficiency`, `quality`, scores) are whole numbers 0–100, **never** 0–1 fractions: `1` means 1 %. |

### Errors

Every error response has the same shape:

```json
{ "error": "human-readable message", "request_id": "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c" }
{ "error": "consent required", "code": "consent_required", "request_id": "01a1…" }
```

`code` (string?) is present when the client should do something specific; branch on it,
not on the message. Closed enum:

| `code` | Status | Do |
| --- | --- | --- |
| `invite_required` | `403` | The beta is invite-only and this account has no access yet: show the invite screen, `POST /v1/me/invite`. Every `/v1/*` route answers it except `GET /v1/me`, `GET /v1/me/sync`, `GET /v1/config`, `GET /v1/model`, `POST /v1/me/invite`, `POST`/`DELETE /v1/me/consent`, `POST /v1/feedback` and `DELETE /v1/me`. |
| `consent_required` | `403` | `POST /v1/sync` or `POST /v1/train/import` without a consent to the current privacy policy (`GET /v1/config` → `privacy_policy_version`): show the consent screen, `POST /v1/me/consent`, then send again. Reads keep working. |
| `import_in_progress` | `429` | `POST /v1/train/import` while another import of this account is still running (this device's earlier request, or another device): wait `Retry-After` (10) seconds and send the same request again. It is safe: nothing of this request was processed. |
| `import_not_enabled` | `403` | `POST /v1/train/import` for an account the feature flag `train.import` is off for (`GET /v1/config` → `flags`): hide the import; it is not an error to retry. Nothing of the request was read. |

`request_id` equals the `X-Request-ID` response header. It is present on every error the
services produce; decode it as optional anyway (a proxy in front may answer on its own).
It is a UUID (8-4-4-4-12 hex digits): show and copy it exactly as received, since support
searches the logs for that string.

**Gates in `warn` mode.** Before the beta opens, the operator can run either gate in
`warn` (OPERATIONS.md → Beta): the request is served as if the account had access or
consent, and the response carries `X-RYFT-Gate` with the code(s) it would have been
refused with (`invite_required`, `consent_required`; several are comma-separated, or the
header repeats). An app that knows the gates prompts for what is missing; one that does
not keeps working. `GET /v1/me/sync` → `gate` says the same.

| Status | When |
| --- | --- |
| `400` | Malformed JSON (a raw 0x00 byte in the body included), unknown body field (in `POST /v1/train/import` also `origin` and `import_source` on a session), malformed date / UUID in query or path, a path or query holding `%00` or bytes that are not UTF-8 |
| `401` | The token is missing, malformed, expired, not signed by the project, or not a signed-in user's token, or the account was deleted (checked before routing, so any `/v1/*` path). Refresh the session once and retry; a second `401` means sign in again. |
| `403` | Changing a built-in exercise, plan or behavior; and the two `code`s above: `invite_required` (no beta access), `consent_required` (sync or import without consent to the current policy), `import_not_enabled` (the import is off for the account) |
| `404` | Resource does not exist or is not the user's (`/v1/workouts/{id}`, exercises, plans, sessions), or unknown route or method |
| `409` | The id is taken by another user's or a built-in exercise or plan; completing a discarded session. **`PUT /v1/sessions/{id}`**: the server has newer sets — the body is then the server's `Session`, not the error shape. |
| `413` | Request body over the limit (32 MiB for `POST /v1/sync`, 1 MiB for `POST /v1/train/import`, 256 KiB for `PUT /v1/sessions/{id}` and `PUT /v1/plans/{id}`, 64 KiB otherwise). For sync, send smaller pages. |
| `422` | Well-formed but invalid: out-of-range value, unknown or missing enum parameter (`range`, `metric`; an enum parameter holding `%00` is `400`: the URL is refused before the parameter is read), `from > to`, recompute range over 400 days, unknown timezone, a string in the body containing NUL (`\u0000`); `POST /v1/train/import`: an unknown `source`, over 5 plans, 50 sessions or 2 500 sets, the same id twice in the request |
| `429` | Too many requests; wait `Retry-After` seconds: the per-user rate limit (see **Conventions**), a second `POST /v1/recompute` within a minute, or a second import while one runs (`code` `import_in_progress`). The edge also limits each client IP (20/s, bursts of 50) before any token is checked; that `429` has a plain-text body, so do not require JSON on a `429`. |
| `500` | Unexpected server error. The message is always `"internal error"`; the details are in the server log under `request_id`. |
| `503` | Retry after `Retry-After`: the token could not be checked right now (Supabase keys unreachable — **not** a sign-in problem, keep the session), the database or the queue is down, or the request ran out of time (`"request timed out"`); `/readyz`. |

The message is for logs and debugging; branch on the status code, not the text.

**NUL.** The database cannot store U+0000, so no request may carry it as text. Both
services check every request, after authentication (a missing token is still `401`) and
before any of it is stored:

| Where | Status | Body |
| --- | --- | --- |
| A body's JSON has `\u0000` in a string: a value or an object key, at any depth, also in a field the server would otherwise ignore or normalise (`locale`); every route but `POST /v1/sync` | `422` | `{ "error": "strings must not contain the NUL character (\\u0000)", "request_id": "…" }` |
| `POST /v1/sync` with `\u0000` in a string, value or key | `200` | the page is stored without its NULs (a key then names its field; one that names none is still `400`) |
| A body holds a raw 0x00 byte (not JSON) | `400` | `{ "error": "bad json: invalid character '\\x00' in string literal", "request_id": "…" }` (the text after `bad json:` varies) |
| The path or the query holds `%00` or bytes that are not UTF-8 (`?q=%FF`) | `400` | `{ "error": "the path and query must be UTF-8 text without NUL characters", "request_id": "…" }` |
| A header holds a NUL byte | `400` | plain text, from the HTTP server before anything else, authentication included (do not require JSON) |
| Free-form JSON (`POST /v1/feedback` → `diagnostics`) holds bytes that are not UTF-8 or a lone surrogate (`"\ud800"`) | `204` | stored with U+FFFD in their place |
| Free-form JSON holds a number beyond double precision (`1e400`) | `422` | `{ "error": "diagnostics: a number is out of range", "request_id": "…" }` |

```json
{ "error": "strings must not contain the NUL character (\\u0000)", "request_id": "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c" }
```

The `422` refuses the whole request; there is no `code`, since only the app can fix it:
strip U+0000 and send again. A sync is the exception: its strings come from HealthKit and
the device (source names, bundle ids, the device name, versions), which nobody can
correct, and a refused page would stall its stream, so ingest removes every `\u0000` from
the page's strings and stores the rest. `"\\u0000"` (an escaped backslash, then `u0000`)
is six ordinary characters and is stored as written, on every route. Free-form JSON is
stored as the server decodes it, like every other field: text that is not valid Unicode
becomes U+FFFD, numbers are kept as double precision (about 16 significant digits), and
keys and values otherwise as sent (a NUL in them is the `422` above). Should the database
itself ever refuse a request's text, the answer is a `422` as well, with
`"the request holds text that cannot be stored (a NUL character or bytes that are not UTF-8)"`.

### Authentication

| Situation | Response | Client should |
| --- | --- | --- |
| Valid Supabase access token | normal response | — |
| No or malformed `Authorization` header, expired token, bad signature, wrong issuer/audience, `role` ≠ `authenticated`, anonymous user | `401`, `WWW-Authenticate: Bearer realm="ryft", error="invalid_token"` | refresh the session once, retry; on a second `401` sign the user out |
| The account was deleted (`DELETE /v1/me`), token not yet expired | `401` (except `DELETE /v1/me`: `200`) | sign out; the refresh fails as well, since the Supabase user is gone |
| Signing keys temporarily unreachable, database down | `503` with `Retry-After` | retry later; never sign out |

Tokens are verified against the project's published signing keys
(`https://<ref>.supabase.co/auth/v1/.well-known/jwks.json`, ES256/RS256 only) and must
carry `iss = https://<ref>.supabase.co/auth/v1`, `aud = authenticated`,
`role = authenticated`, a UUID `sub` (the user ID) and must not be anonymous. The
platform reads `email`, `app_metadata.provider` and `user_metadata.full_name` into the
user's profile.

**Local development** (`AUTH_MODE=apikey`, refused unless explicitly allowed): the bearer
is the shared `API_KEY` (acts as one configured user) or `<API_KEY>:<uuid>` (acts as that
user). Never exposed outside a developer machine.

## Endpoints

| Method | Path | Service | Returns |
| --- | --- | --- | --- |
| `POST` | `/v1/sync` | ingest | `SyncResponse` |
| `GET` | `/v1/config` | api | `RemoteConfig` |
| `GET` | `/v1/model` | api | `ModelInfo` |
| `POST`, `DELETE` | `/v1/me/consent` | api | `204`, `204` |
| `POST` | `/v1/me/invite` | api | `204` |
| `POST` | `/v1/feedback` | api | `204` |
| `GET` | `/v1/me` | api | `User` |
| `GET` | `/v1/me/sync` | api | `SyncStatus` |
| `PATCH` | `/v1/me` | api | `User` |
| `PATCH` | `/v1/me/notifications` | api | `NotificationPrefs` |
| `POST`, `DELETE` | `/v1/devices/{id}/push` | api | `204`, `204` |
| `GET` | `/v1/days?from&to` | api | `[DailyScore]` |
| `GET` | `/v1/days/{day}` | api | `DayDetail` |
| `GET` | `/v1/sleep?range&to` | api | `SleepRange` |
| `GET` | `/v1/sleep/plan?wake` | api | `SleepPlan` |
| `GET` | `/v1/workouts?from&to` | api | `[Workout]` |
| `GET` | `/v1/workouts/{id}` | api | `WorkoutDetail` |
| `GET` | `/v1/trends?metric&range&to` | api | `Trend` |
| `GET` | `/v1/insights` | api | `[Insight]` |
| `POST` | `/v1/recompute` | api | `202 {status:"queued"}` |
| `GET`, `POST` | `/v1/exercises` | api | `[Exercise]`, `Exercise` |
| `PATCH`, `DELETE` | `/v1/exercises/{id}` | api | `Exercise`, `204` |
| `GET` | `/v1/exercises/{id}/history?limit` | api | `ExerciseHistory` |
| `GET` | `/v1/plans` | api | `PlansOverview` |
| `GET`, `PUT`, `DELETE` | `/v1/plans/{id}` | api | `Plan`, `Plan`, `204` |
| `POST` | `/v1/plans/{id}/enroll` | api | `Enrollment` (optional body `EnrollPost`) |
| `DELETE` | `/v1/plans/enrollment` | api | `204` |
| `GET` | `/v1/plans/today` | api | `PlanToday` or `204` |
| `GET` | `/v1/sessions?from&to&status` | api | `[SessionSummary]` |
| `GET`, `PUT`, `DELETE` | `/v1/sessions/{id}` | api | `Session`, `Session` (`409`: the server's `Session`), `204` |
| `POST` | `/v1/sessions/{id}/complete` | api | `Session` |
| `POST` | `/v1/train/import` | api | `TrainImportResponse` |
| `GET`, `PATCH`, `POST` | `/v1/behaviors` | api | `[Behavior]`, `[Behavior]`, `Behavior` |
| `DELETE` | `/v1/behaviors/{id}` | api | `204` |
| `GET` | `/v1/behaviors/impact` | api | `BehaviorImpacts` |
| `GET`, `PUT` | `/v1/journal/{day}` | api | `JournalDay` |
| `GET` | `/healthz`, `/readyz`, `/metrics` | all | health, Prometheus (no auth, not routed by the Ingress) |

### `POST /v1/sync` — upload HealthKit data

Request `SyncRequest`:

| Field | Type | Notes |
| --- | --- | --- |
| `device_id` | uuid | Required. Stable per app install. |
| `device_name` | string? | Optional, shown nowhere yet. |
| `app_version` | string? | The app's version and build, e.g. `0.9.0 (41)`; kept on the device row for support (the admin view). |
| `os_version` | string? | e.g. `iOS 26.1`. |
| `samples` | `[Sample]` | May be empty. |
| `sleep` | `[SleepSegment]` | May be empty. |
| `workouts` | `[Workout]` | May be empty. `strain` and `also_recorded_by` are ignored on input. |
| `deleted` | `[DeletedRecord]?` | HealthKit objects that were deleted in Apple Health: `{"kind": "sample" \| "sleep" \| "workout", "id": uuid}`, at most **2 000** per request (`422` above; send more in the next request). Optional and may be empty; a request with only `deleted` is valid. See **Deletions** below. |

`Sample`: `type` (enum below), `start`, `end` (timestamps, `end ≥ start`), `value`
(number), `unit` (string, informational), `source` (string, HealthKit source name),
`source_bundle_id` (string?, the HealthKit source's bundle identifier, e.g.
`com.ouraring.oura`; `User.sources` shows it), `id` (uuid?, the HealthKit object's UUID,
`HKObject.uuid`; see **HealthKit ids** below). `SleepSegment` and `Workout` take
`source_bundle_id` too, `SleepSegment` takes `id` too.

| `type` | Unit of `value` |
| --- | --- |
| `heart_rate` | bpm |
| `hrv_sdnn` | ms |
| `resting_hr` | bpm (one per day) |
| `respiratory_rate` | breaths/min |
| `spo2` | percent 0–100 |
| `vo2max` | ml/kg/min |
| `active_energy` | kcal |
| `steps` | count |

`SleepSegment`: `start`, `end` (`end > start`), `stage` ∈ `inbed | asleep | core | deep | rem | awake`
(HealthKit's vocabulary; `asleep` = unspecified stage), `source`.

Uploads are idempotent: samples are unique on `(type, start, end, source)`, sleep
segments on `(start, end, stage, source)`, workouts on `id`; a sample or sleep segment
that carries an `id` is also unique on `(user, id)`. Re-sending a page is harmless.

**HealthKit ids.** `Sample.id` and `SleepSegment.id` are optional (uuid, upper or lower
case; stored lower case). Records without one behave exactly as before. Health objects
are immutable, and a source that changes a value (a watch app re-writing its resting heart
rate) writes the same `(type, start, end, source)` under a **new** UUID and deletes the old
one. So the id is a handle for deletions, and the latest upload owns it:

- A record with an `id` whose natural key (`(type, start, end, source)`, or
  `(start, end, stage, source)`) is already stored **adopts** that row: the row takes the new
  `id` and, for a sample, the new `value`. That counts as stored (`accepted`) when the id or
  the value changed, else as `duplicate`. This also gives the id to rows uploaded before the
  app sent ids.
- A record whose `id` is already stored on a different row of the user (the source was
  renamed) is `duplicate`; the old row stays.
- A `SyncRequest` can therefore be re-sent with ids after it was sent without, and no row
  doubles.

Response `200 SyncResponse`:

```json
{ "samples": 1380, "sleep": 14, "workouts": 1, "days_touched": ["2026-10-17", "2026-10-18"],
  "accepted": { "samples": 1380, "sleep": 14, "workouts": 1 },
  "ignored": { "samples":  { "duplicate": 58, "outside_window": 0, "unknown_type": 2, "unknown_id": 1 },
               "sleep":    { "duplicate": 0,  "outside_window": 0, "unknown_type": 0, "unknown_id": 0 },
               "workouts": { "duplicate": 0,  "outside_window": 0, "unknown_type": 0, "unknown_id": 0 } },
  "deleted": { "samples": 3, "sleep": 0, "workouts": 6 },
  "scoring": { "queued_days": ["2026-10-17", "2026-10-18"], "expected_within_sec": 20 } }
```

`samples`/`sleep`/`workouts` (and `accepted`) count records stored new or changed. Each
record that is not counted there is in `ignored`, per reason:

| Reason | The record |
| --- | --- |
| `duplicate` | is identical to one already stored (a page sent twice, a re-sync). Harmless. |
| `outside_window` | starts more than 24 h in the future (a wrong clock) or ended more than 400 days ago (no score reaches back that far). |
| `unknown_type` | has a sample `type` or sleep `stage` the server does not know. Ignored instead of failing the whole sync, so an app that reads a new HealthKit type keeps syncing. |
| `unknown_id` | is an entry of `deleted` (counted under its `kind`: `sample` → `ignored.samples`, `sleep` → `ignored.sleep`, `workout` → `ignored.workouts`) that names nothing the server can delete for this user. Not a record of `samples`/`sleep`/`workouts`. See **Deletions**. |

`unknown_id` is present in every `IgnoredCounts` (0 when none) from 2026-10-31; a client
decodes it as optional. `deleted` (`SyncDeleted`: `samples`, `sleep`, `workouts`) is present in
every response from the same date and counts the entries of `deleted` that removed a row.

#### Deletions

`deleted` tells the server which HealthKit objects no longer exist. For each distinct
`(kind, id)` of the request, for this user only:

| `kind` | Removes | Counted |
| --- | --- | --- |
| `sample` | the sample stored with that `Sample.id` | `deleted.samples`, else `ignored.samples.unknown_id` |
| `sleep` | the sleep segment stored with that `SleepSegment.id` | `deleted.sleep`, else `ignored.sleep.unknown_id` |
| `workout` | the workout with that `Workout.id` | `deleted.workouts`, else `ignored.workouts.unknown_id` |

For each kind, `deleted.<kind> + ignored.<kind>.unknown_id` equals the number of distinct ids of that
kind in the request (a repeated pair is counted once).

- **Order.** Deletions apply **after** the same request's `samples`, `sleep` and `workouts`
  are stored. The Garmin pattern (a new UUID uploaded, the old one deleted) in one request
  keeps the new record; a record uploaded and deleted in one request ends up deleted.
- **Idempotent.** A deletion sent twice, or for an object the server never had (it was
  deleted before the app uploaded it, or uploaded before ids existed), is `unknown_id` and `200`,
  never an error. An id of another account is `unknown_id` too, and nothing of that account is
  touched or revealed: the server cannot be asked whether an id exists.
- **Tolerant.** An entry with a `kind` this version does not know is skipped and not counted
  (a later app keeps syncing); a nil UUID (`00000000-0000-0000-0000-000000000000`) under a
  known `kind` is `unknown_id`. Only more than 2 000 entries is a `422`. A string that is not a UUID is `400` (bad JSON shape).
- **Workouts made by RYFT.** A workout with `source` `ryft-session` (created for a Train
  session that has no matching HealthKit workout) is not deletable here: its id is
  `unknown_id`. Delete the session instead (`DELETE /v1/sessions/{id}`).
- **A workout with a Train session.** Deleting the HealthKit workout keeps the session. The
  day is rescored: the session loses its HealthKit match, scoring creates a `ryft-session`
  workout for it (`Workout.session_id` set, `source` `ryft-session`) and its muscular load
  stays on the day. The workout's old id then answers `404` on `GET /v1/workouts/{id}`.
- **Heart rate stays.** Heart-rate samples that lie in a deleted workout's window are separate
  HealthKit objects and are only deleted by their own entries.
- **Rescoring.** The local days of every deleted row (and of every workout folded, see
  `Workout.also_recorded_by`) are queued: they are in `days_touched` and `scoring.queued_days`
  like the days of stored records. A deletion changes the day's numbers and moves `scoring_state`
  to `pending` until scoring is done.
- **Failure.** If the server cannot apply a request's deletions, the whole request answers
  `503` with `Retry-After` and the records of the same request are stored (they are idempotent). Send the same
  request again; deletions that already happened come back as `unknown_id`. Keep a deletion until
  a `200` carried it.
- **Logs** hold counts, never ids.

Example request (a rewritten workout and a deleted resting heart rate):

```json
{ "device_id": "0E6B2E54-9C6D-4F7B-8A55-4E3F3B7C1D20", "samples": [], "sleep": [], "workouts": [],
  "deleted": [ { "kind": "workout", "id": "5C3F0B9E-52B1-4E55-AE0A-7B1C2D3E4F50" },
               { "kind": "sample",  "id": "A41D7E02-6F1B-4C58-9B3A-0D5E8F7A6B21" },
               { "kind": "workout", "id": "5C3F0B9E-52B1-4E55-AE0A-7B1C2D3E4F50" } ] }
```

answers (the two distinct ids; the workout was held, the sample was already gone):

```json
{ "samples": 0, "sleep": 0, "workouts": 0, "days_touched": ["2026-10-20"],
  "accepted": { "samples": 0, "sleep": 0, "workouts": 0 },
  "ignored": { "samples":  { "duplicate": 0, "outside_window": 0, "unknown_type": 0, "unknown_id": 1 },
               "sleep":    { "duplicate": 0, "outside_window": 0, "unknown_type": 0, "unknown_id": 0 },
               "workouts": { "duplicate": 0, "outside_window": 0, "unknown_type": 0, "unknown_id": 0 } },
  "deleted": { "samples": 0, "sleep": 0, "workouts": 1 },
  "scoring": { "queued_days": ["2026-10-20"], "expected_within_sec": 20 } }
```

A sample or sleep segment with an `id`, as the app sends it from 2026-10-31:

```json
{ "id": "A41D7E02-6F1B-4C58-9B3A-0D5E8F7A6B21", "type": "resting_hr", "start": "2026-10-20T00:00:00Z", "end": "2026-10-20T00:00:00Z",
  "value": 49, "unit": "bpm", "source": "Garmin Connect", "source_bundle_id": "com.garmin.connect.mobile" }
```

`days_touched` (and `scoring.queued_days`) are the local days queued for rescoring;
`scoring.expected_within_sec` is how long scoring has recently taken from a sync to the
score (the 95th percentile over every account and the last 6 hours; 30 until it knows).
`GET /v1/me/sync` says when they are done. A `\u0000` in any string is removed, not
refused (Errors → NUL). Errors: `400` bad JSON, `422` validation
(`device_id` missing, `end` before `start`, workout without `id`, more than 2 000 entries in `deleted`), `403` `invite_required`
/ `consent_required` (Errors above): keep the HealthKit anchors and send the same pages
again once that is resolved.

### `GET /v1/me` — user settings

```json
{ "id": "5f0e8c1a-6c1b-4a4e-9d55-1f1d2f6a3b7c", "name": "Tim K", "email": "tim@example.com",
  "provider": "apple", "tz": "Europe/Berlin", "max_hr": 190, "sleep_base_min": 455,
  "sleep_base_source": "learned", "sleep_base_learned_min": 455, "sleep_base_learned_at": "2026-10-18T02:00:04Z",
  "notifications": { "categories": { "MORNING_SCORE": true, "SESSION_SCORED": true, "PR": false, "SYNC_STALE": true },
                     "quiet_hours": { "from": "22:30", "to": "07:00" } },
  "beta_access": true,
  "consent": { "policy_version": "2026-10-20", "granted_at": "2026-10-20T09:12:44Z" },
  "birth_year": 1990, "max_hr_source": "estimated", "account_hint": null,
  "sources": [ { "name": "Tim's Apple Watch", "bundle_id": "com.apple.health.81E2…", "kinds": ["energy", "hr", "hrv", "resp", "sleep", "spo2", "workout"], "last_seen": "2026-10-20T06:58:00Z",
                 "samples_7d": { "heart_rate": 9800, "hrv_sdnn": 41, "resting_hr": 7, "respiratory_rate": 212, "spo2": 160, "vo2max": 1, "active_energy": 1893, "steps": 0 } },
               { "name": "Tim's iPhone", "bundle_id": "com.apple.health.3F0A…", "kinds": ["steps"], "last_seen": "2026-10-20T08:40:00Z",
                 "samples_7d": { "heart_rate": 0, "hrv_sdnn": 0, "resting_hr": 0, "respiratory_rate": 0, "spo2": 0, "vo2max": 0, "active_energy": 0, "steps": 5230 } },
               { "name": "Garmin Connect", "bundle_id": "com.garmin.connect.mobile", "kinds": ["energy", "hr", "sleep", "steps", "workout"], "last_seen": "2026-10-20T03:02:00Z",
                 "samples_7d": { "heart_rate": 9112, "hrv_sdnn": 0, "resting_hr": 7, "respiratory_rate": 0, "spo2": 0, "vo2max": 0, "active_energy": 640, "steps": 1180 } } ] }
```

The user is created on the first authenticated request with `tz = Europe/Berlin`,
`max_hr = 190` (`max_hr_source: default`), `sleep_base_min = 460`; set real values with
`PATCH /v1/me`.

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | Supabase user ID (`sub` of the token). |
| `name` | string | From the sign-in (`user_metadata.full_name`, the Apple name); `""` until known. |
| `email` | string? | From the token; for Sign in with Apple possibly a private relay address. |
| `provider` | string? | Sign-in provider, e.g. `apple`. |
| `tz` | string | IANA zone; defines every day boundary in this API. |
| `max_hr` | int | bpm, used for heart-rate reserve in strain and zones. |
| `sleep_base_min` | int | Baseline sleep need in minutes before strain and debt top-ups and the nap credit: the one in use, see `sleep_base_source`. |
| `sleep_base_source` | string | `default` (460 min, nothing learned yet), `learned` (the weekly estimate below), `manual` (set with `PATCH`). Closed enum. |
| `sleep_base_learned_min` | int? | The latest learned estimate, also while `manual` (for "learned 7 h 35 min · override"); `null` until there is one. |
| `sleep_base_learned_at` | timestamp? | When that estimate was computed. |
| `beta_access` | bool | The account redeemed an invite code (or existed before the gate). Without it, and with the gate on, other routes answer `403 invite_required`. |
| `consent` | `Consent?` | The latest consent to health-data processing that was not withdrawn: `{policy_version, granted_at}`; `null` when there is none. Syncing needs `policy_version` to equal `GET /v1/config` → `privacy_policy_version`. |
| `birth_year` | int? | For the max-HR estimate; nothing finer is stored. `PATCH /v1/me` accepts 1920 to the current year − 16. |
| `max_hr_source` | string | `default` (190, nothing known), `estimated` (208 − 0.7 · age, Tanaka, from `birth_year`), `manual` (set with `PATCH`). Closed enum. |
| `sources` | `[UserSource]` | Apps and devices that sent data in the last 30 days, newest first: `name` (HealthKit source name), `bundle_id` (string?, when the app sent it), `kinds` (closed enum, sorted: `energy`, `hr`, `hrv`, `resp`, `sleep`, `spo2`, `steps`, `workout`; `hr` covers heart rate, resting heart rate and VO₂max), `last_seen` (the latest record's time), `samples_7d` (see below). `[]` when nothing arrived. |
| `notifications` | `NotificationPrefs` | Push preferences: every category (`MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE`) with whether it is on (default on), and `quiet_hours` (`{from, to}`, local `HH:MM`, or `null`). Change with `PATCH /v1/me/notifications`. |
| `account_hint` | `AccountHint?` | Set when this account has no data yet and the device in `X-RYFT-Device-ID` synced **another** account in the last 30 days: probably the same person signed in another way (Apple vs. email). `{"kind": "other_account_on_device", "other_provider": "email", "other_last_sync": "2026-10-20T06:58:00Z"}`: `kind` closed enum (one value so far), `other_provider` string? (`apple`, `email`), `other_last_sync` timestamp. Never an id or an email. Offer "sign in with email instead" and the support contact; the operator can merge accounts. `null` otherwise. |

**`UserSource.samples_7d`** (`{string: int}`) counts the **samples** (not sleep segments or workouts, which `kinds` covers) stored for that source with `start` in the last 7 × 24 hours, per sample `type`: the keys are the eight `Sample.type` values (`heart_rate`, `hrv_sdnn`, `resting_hr`, `respiratory_rate`, `spo2`, `vo2max`, `active_energy`, `steps`), **all present on every source, `0` when none arrived**. That is how the app tells a tracker that writes heart rate but no HRV (`hrv_sdnn: 0` next to `heart_rate` in the thousands) from one that writes both. The source is matched by `name`; the counts are after the sync's duplicate handling (each stored row once). The type keys are an open set (a later version may add one): ignore keys you do not know and treat a missing key as `0`. The counts are exact as of the response or taken from the last scoring pass (at most about 15 minutes old); do not compare them to a sync's `accepted`. The field is absent only from a platform that predates 2026-10-31; decode it as optional. There is no way to choose which source feeds which metric yet.

**Learned baseline.** Every Sunday from 04:00 local time, scoring estimates the
baseline from the last 30 scored days. It needs at least 14 nights of sleep, of which at
least 7 *qualify*: the morning's recovery was ≥ 67 after a day with strain < 10. The
estimate is the median minutes asleep of the qualifying nights, clamped to 360–570. Unless
the source is `manual`, `sleep_base_min` takes the estimate when it changes, and the last
seven days are rescored with it.

### `PATCH /v1/me` — change settings

Body `UserPatch`, every field optional: `tz` (IANA zone name), `max_hr` (int, 100–230;
sets `max_hr_source` to `manual`), `max_hr_manual` (bool; `false` goes back to the
estimate, or to 190 without a birth year), `birth_year` (int, 1920 to this year − 16, `422` otherwise;
a birth year stored earlier stays as it is, even when it is outside the range now; while the source is not `manual`, `max_hr` becomes `round(208 − 0.7 · (this year −
birth_year))` and the source `estimated`),
`sleep_base_min` (int, 240–720; sets `sleep_base_source` to `manual`, so learning no
longer changes it), `sleep_base_manual` (bool; `false` goes back to the learned estimate,
or to the default 460 when there is none; `true` keeps the current value as manual).
Returns the updated `User`. `422` on an unknown zone, an out-of-range value,
`sleep_base_min` together with `sleep_base_manual: false`, or `max_hr` together with
`max_hr_manual: false`; `500` if the update itself fails. Changing a setting does **not** rescore history; follow with
`POST /v1/recompute`.

### `DELETE /v1/me` — delete the account

No body. Erases everything stored for the user, in one transaction: samples, sleep,
workouts, scores, devices and the profile. Then it revokes the app's Sign in with Apple
grant (if `POST /v1/me/apple-authorization` stored one), and deletes the user's
Supabase Auth account with its identities and sessions. Responds `200`:

```json
{ "status": "deleted", "auth_user_deleted": true, "apple_token_revoked": true }
```

| Field | Type | Notes |
| --- | --- | --- |
| `status` | string | Always `"deleted"`: the data is gone. |
| `auth_user_deleted` | bool | `false` if Supabase could not be reached. The platform retries in the background (every 15 minutes), so the app has nothing to do. |
| `apple_token_revoked` | bool? | `null` when no Apple token was stored (email accounts, or the app never sent a code). `false` if Apple could not be reached; retried in the background like `auth_user_deleted`. |

From then on, every request with that user's token gets `401`, including tokens that
have not expired yet. The exception is `DELETE /v1/me` itself, which answers `200` again,
so retrying after a network error is safe. A large account can take several seconds. The
deletion completes even if the app is closed, and a retry waits for it. `503` (with
`Retry-After`) means nothing was deleted; retry.

**App:** after `200`, sign out locally and clear any cached data. For Sign in with Apple
accounts, the revocation Apple requires happens here, provided the app sent the
authorization code after sign-in (next section).

### `POST /v1/me/apple-authorization` — enable Apple token revocation

Apple requires apps that use Sign in with Apple to revoke the user's tokens when the
account is deleted. That takes a refresh token, which only the sign-in's one-time
authorization code yields. Send the code once, right after a successful Sign in with
Apple; the platform exchanges it with Apple and keeps the refresh token encrypted, for
`DELETE /v1/me` only.

```json
{ "code": "c1b2…" }
```

`code` is `ASAuthorizationAppleIDCredential.authorizationCode` as UTF-8. It is valid for
five minutes and only once. Responds `204` (no body). Sending a code again later (a new
sign-in) replaces the stored token.

| Status | When | App should |
| --- | --- | --- |
| `204` | stored | — |
| `422` | `code` missing, invalid, expired or already used; or the code is for a different Apple ID than the signed-in account | not retry with the same code; it is fine to carry on (deletion then cannot revoke at Apple) |
| `501` | the platform is not configured for Apple revocation (local development) | ignore |
| `503` | Apple could not be reached (`Retry-After`) | retry once within the code's five minutes |

Never log the code; it is a credential until used.

### `GET /v1/config` — remote config

Feature flags, evaluated for the caller. Load on launch and again after `ttl_sec`; keep
the last answer (and compiled-in defaults) when offline.

```json
{ "flags": { "train.enabled": true, "sleep.planner": true, "today.redesign": false,
             "coach.enabled": false, "push.enabled": false, "physiology.card": false },
  "evaluated_at": "2026-10-19T06:12:00Z", "ttl_sec": 21600,
  "privacy_policy_version": "2026-10-20", "privacy_policy_url": "https://ryftapp.de/privacy" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `flags` | `{string: bool}` | Every flag the platform has, on or off for this user. Keys are **open**: ignore unknown ones, and use the compiled default for one that is missing. |
| `evaluated_at` | timestamp | When this answer was computed. |
| `ttl_sec` | int | How long to use it: 21600 (6 h). |
| `privacy_policy_version` | string | The privacy policy in force. Consent (`POST /v1/me/consent`) is given to this version; when it changes, every user consents again before the next sync. |
| `privacy_policy_url` | string | Where it is published, in German and English. |

A flag is on for a user when it is enabled and the user is on its allow-list or in its
first `rollout_pct` of 100 buckets: bucket = the first four bytes of SHA-256 of
`"<key>:<user id>"` (the lowercase UUID), big-endian, modulo 100. The bucket is stable per
user and flag, so raising a rollout only adds users. The operator sets flags
(`OPERATIONS.md` → Feature flags), not the app. Shipped with:

| Key | Default |
| --- | --- |
| `train.enabled`, `sleep.planner` | on (100 %) |
| `today.redesign`, `coach.enabled`, `push.enabled`, `physiology.card`, `train.import` | off (enabled, 0 %: on only for allow-listed testers) |

`train.import` is the switch for the training import (`POST /v1/train/import`): the app shows the import only while it is on, and the endpoint checks it too, so an account it is off for gets `403 import_not_enabled` whatever the app does. It ships dark (enabled, 0 %): testers get it by the allow-list.

### `PATCH /v1/me/notifications` — push preferences

Body `NotificationPatch`, both fields optional: `categories` (`{category: bool}`, merged
into the stored ones; the four categories above), `quiet_hours` (`{"from": "22:30", "to":
"07:00"}`, local times; `from` later than `to` spans midnight, equal times mean none;
`null` clears them, leaving the field out keeps them). Returns `NotificationPrefs`:

```json
{ "categories": { "MORNING_SCORE": true, "SESSION_SCORED": true, "PR": false, "SYNC_STALE": true },
  "quiet_hours": { "from": "22:30", "to": "07:00" } }
```

`422` for an unknown category or a time that is not `HH:MM`; `400` for unknown fields.

### `POST /v1/devices/{id}/push` — register for push

`{id}` is the install's device ID, the same UUID the app sends as `device_id` with
`POST /v1/sync`. Body `PushRegistration`:

```json
{ "token": "8a5e…c41f", "env": "production", "locale": "de-DE" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `token` | string | The APNs device token as hex (64+ characters, any case). |
| `env` | string | `sandbox` or `production`: the build's `aps-environment` (Xcode debug builds `sandbox`, TestFlight and the App Store `production`). |
| `locale` | string | The app's language, e.g. `de-DE`; `de…` is kept as `de`, everything else as `en`. It picks the language of names and number formats in the notification's arguments. |

`204`. Send it again whenever the token or the language changes; repeating it is
harmless. A token another account registered (a shared iPhone) moves to the caller.
`422` for a malformed token or `env`, `400` for a malformed `{id}`.
`DELETE /v1/devices/{id}/push` → `204` (on sign-out; also when there is none). Deleting
the account removes every token.

### Push notifications

RYFT's notifications come from the platform's `notify` service over APNs, with the app's
bundle ID as topic. **Until the APNs key is configured** (Apple Developer Program,
`STATUS.md`) nothing is sent; registration and preferences already work.

| Category | Sent when | Title · body keys (arguments) | Opens (`ryft.url`) |
| --- | --- | --- | --- |
| `MORNING_SCORE` | Today's day is **final** (`score.coverage.status`) with a recovery and a night's sleep for the first time: not on a provisional scoring (a 03:00 sync of half a night sends nothing), but when the night is complete or at 10:00 local, whichever comes first; if sleep only arrives after the 10:00 ceiling, when it does. Once a day; during quiet hours it waits until they end, the score as it is then. | `MORNING_SCORE_TITLE` (recovery `82%`) · `MORNING_SCORE_BODY_BALANCED`, `_STRAINED`, `_DEPLETED` or `_CALIBRATING` by `state` (readiness) | `https://ryftapp.de/day/<day>` |
| `SESSION_SCORED` | A completed strength session has its load (scoring has run), within 12 h of its end. The first one each day; not during quiet hours. | `SESSION_SCORED_TITLE` (session title) or `SESSION_SCORED_TITLE_UNTITLED` · `SESSION_SCORED_BODY` (strain), `_PR` (strain) or `_PRS` (strain, number of records) | `https://ryftapp.de/train/session/<id>` |
| `PR` | Instead of `SESSION_SCORED` when that category is off and the session set a record. | `PR_TITLE` (exercise name) · `PR_BODY_E1RM`, `_MAX_WEIGHT`, `_MAX_REPS` or `_MAX_VOLUME` (value) | `https://ryftapp.de/train/session/<id>` |
| `SYNC_STALE` | No heart-rate or step sample for 20 hours (users with data in the last 7 days), once a day between 09:00 and 21:00 local, not during quiet hours. | `SYNC_STALE_TITLE` · `SYNC_STALE_BODY` (hours) | — |

With every `MORNING_SCORE` event, also when that category is off or quiet hours hold
it back, a **silent** push (`content-available`, background priority) tells the app to
refresh the day: `{"aps": {"content-available": 1}, "ryft": {"kind": "refresh", "day":
"2026-10-19"}}`. The same silent push follows **every sync once scoring has finished its
days** (`day`: the newest day the sync touched; collapsed per day), so the app shows the
new scores without polling. The same silent push follows an **import** once scoring has finished its days (`day`: the newest of `days_touched`); an import sends no `SESSION_SCORED` and no `PR` alert, whatever it contains (`POST /v1/train/import`). Together at most one every 5 minutes per account: iOS
budgets background pushes, so poll `GET /v1/me/sync` as the fallback.

The silent push is **not** held back for a provisional day: every scoring of a day, also one that
leaves it `provisional`, is followed by it, so the app can show the provisional score and then the
final one. Only the `MORNING_SCORE` alert waits for `final` (table above); the 5-minute cap above can fold the
final refresh into the provisional one, so refresh the day when the app opens as well.

An alert's payload:

```json
{ "aps": { "alert": { "title-loc-key": "MORNING_SCORE_TITLE", "title-loc-args": ["82%"],
                      "loc-key": "MORNING_SCORE_BODY_BALANCED", "loc-args": ["78"] },
           "sound": "default", "category": "MORNING_SCORE", "thread-id": "MORNING_SCORE",
           "mutable-content": 1 },
  "ryft": { "category": "MORNING_SCORE", "day": "2026-10-19", "url": "https://ryftapp.de/day/2026-10-19" } }
```

`category` is the `UNNotificationCategory` identifier, `thread-id` groups by category,
`mutable-content` lets a Notification Service Extension attach the ring. A notification
replaces an undelivered one of the same category and day (`apns-collapse-id`) and
expires at the end of the local day (`MORNING_SCORE`), after 12 h (`SESSION_SCORED`,
`PR`) or 6 h (`SYNC_STALE`). Arguments are formatted for the registered `locale`:
`82%` / `82 %`, `11.2` / `11,2`, exercise names in English or German.

The keys, as the app ships them in `Localizable.xcstrings` (`%@` and positional
`%1$@`, as iOS fills `loc-args`):

| Key | en | de |
| --- | --- | --- |
| `MORNING_SCORE_BODY_BALANCED` | Readiness %@. You're ready to perform: a good day to push. | Bereitschaft %@. Du bist bereit: ein guter Tag für hohe Belastung. |
| `MORNING_SCORE_BODY_CALIBRATING` | Readiness %@. RYFT is still learning your baselines. | Bereitschaft %@. RYFT lernt noch deine Ausgangswerte. |
| `MORNING_SCORE_BODY_DEPLETED` | Readiness %@. Take it easy today and protect tonight's sleep. | Bereitschaft %@. Geh es heute ruhig an und schütze deinen Schlaf. |
| `MORNING_SCORE_BODY_STRAINED` | Readiness %@. Keep the load moderate today. | Bereitschaft %@. Belaste dich heute moderat. |
| `MORNING_SCORE_TITLE` | Recovery %@ | Erholung %@ |
| `PR_BODY_E1RM` | Estimated one-rep max %@ kg. | Geschätztes 1RM %@ kg. |
| `PR_BODY_MAX_REPS` | %@ reps in one set, your most yet. | %@ Wiederholungen in einem Satz, so viele wie nie. |
| `PR_BODY_MAX_VOLUME` | %@ kg in one set (reps × kg), your best yet. | %@ kg in einem Satz (Wdh. × kg), dein Bestwert. |
| `PR_BODY_MAX_WEIGHT` | %@ kg, your heaviest set yet. | %@ kg, dein schwerster Satz bisher. |
| `PR_TITLE` | New record: %@ | Neuer Rekord: %@ |
| `SESSION_SCORED_BODY` | Strain %@. | Belastung %@. |
| `SESSION_SCORED_BODY_PR` | Strain %@, 1 new record. | Belastung %@, 1 neuer Rekord. |
| `SESSION_SCORED_BODY_PRS` | Strain %1$@, %2$@ new records. | Belastung %1$@, %2$@ neue Rekorde. |
| `SESSION_SCORED_TITLE` | %@ scored | %@ ausgewertet |
| `SESSION_SCORED_TITLE_UNTITLED` | Workout scored | Training ausgewertet |
| `SYNC_STALE_BODY` | Nothing new from your watch for %@ hours. Open RYFT to sync. | Seit %@ Stunden nichts Neues von deiner Uhr. Öffne RYFT zum Synchronisieren. |
| `SYNC_STALE_TITLE` | No new data | Keine neuen Daten |

### `POST /v1/me/consent` · `DELETE /v1/me/consent` — consent to health-data processing

Heart rate, sleep and the rest are health data (GDPR Art. 9); RYFT processes them on
the user's explicit consent to the current privacy policy. `POST` records it:

```json
{ "policy_version": "2026-10-20", "app_version": "0.9.0 (41)" }
```

`204`. `422` when `policy_version` is not `GET /v1/config` → `privacy_policy_version`
(the app showed an outdated text). Repeating it for the same version keeps the first
`granted_at`. `DELETE` withdraws it (`204`, also when there was none): from then on
`POST /v1/sync` answers `403 consent_required`, at once, and no day is scored any more
(stored scores stay readable). Withdrawing deletes nothing; the
app offers export and deletion next to it (`DELETE /v1/me`). Every grant and withdrawal
is kept with its time, policy version and app version.

### `POST /v1/me/invite` — redeem an invite code

`{"code": "RYFT-7K3M-QX9P"}` → `204`, and `User.beta_access` becomes `true`. Case,
spaces and dashes do not matter. `422 "invalid or used code"` for an unknown, expired or
used-up code (one message for all three). At most 5 attempts per user and hour (`429`).
An account that already has access gets `204` without using the code.

### `GET /v1/me/sync` — what the server has, and what it is doing with it

```json
{ "gate": null, "last_sync_at": "2026-10-21T06:58:12Z", "last_sync_device": "iPhone",
  "latest": { "hr": "2026-10-21T06:55:00Z", "hrv": "2026-10-21T04:10:00Z", "sleep": "2026-10-21T06:41:00Z",
              "steps": "2026-10-21T06:50:00Z", "workout": "2026-10-20T18:45:00Z" },
  "today": { "day": "2026-10-21", "state": "pending", "scored_at": null, "model_version": "2026.10.1" },
  "pending_days": ["2026-10-21"], "expected_within_sec": 20, "resync_requested_at": null }
```

| Field | Type | Notes |
| --- | --- | --- |
| `gate` | string? | `invite_required` or `consent_required` when a gate stands between this account and its scores, **also while that gate is in `warn` mode** (Errors): prompt for it. `null` when nothing does. The beta gate is checked first. |
| `last_sync_at` | timestamp? | The newest accepted sync of any of the account's devices; `null` before the first. |
| `last_sync_device` | string? | That device's name. |
| `latest` | object | The newest record per kind (the `UserSource` kinds that ever arrived: `energy`, `hr`, `hrv`, `resp`, `sleep`, `spo2`, `steps`, `workout`), as timestamps. Missing keys: never arrived. |
| `today.day` | day | Today in the user's timezone. |
| `today.state` | string | `scored`, `pending`, `waiting_for_sleep`, `stale`, as `DailyScore.scoring_state`. |
| `today.scored_at` | timestamp? | When today was last scored; `null` before. |
| `today.model_version` | string | The current scoring model (`GET /v1/model`). |
| `pending_days` | `[day]` | Days a sync or a training import queued that scoring has not finished yet, oldest first. |
| `expected_within_sec` | int | As in `SyncResponse.scoring`: when to look again (or wait for the silent refresh push). |
| `resync_requested_at` | timestamp? | Set by the operator when the server is missing data (`scripts/user.sh --resync-hint`): if it is newer than the app's last full re-sync, run one (re-read the HealthKit window without anchors). `null` otherwise. |

Cheap to call (cached 10 s per user). After a sync, poll it every `expected_within_sec`
until `pending_days` is empty, or wait for the silent refresh push (Push).

### `GET /v1/model` — the scoring model and what changed

```json
{ "current": "2026.10.1",
  "changelog": [ { "version": "2026.10.1", "date": "2026-10-20",
                   "summary": "The model at the start of the beta: …",
                   "summary_en": "The model at the start of the beta: …",
                   "summary_de": "Das Modell zum Start der Beta: …",
                   "affects": ["readiness", "recovery", "strain", "sleep", "sleep_quality"] } ] }
```

Newest first. `summary` follows `Accept-Language`. `affects` names trend metrics
(`GET /v1/trends`). Scores with an older `model_version` move when they are rescored;
the app can say "recalculated with model 2026.11.1" and show the entry.

### `POST /v1/feedback` — report a problem

```json
{ "message": "Recovery 41 after a great night?", "category": "data",
  "app_version": "0.9.0", "build": "41", "device_model": "iPhone17,1", "os_version": "iOS 26.1",
  "request_ids": ["01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"],
  "diagnostics": { "last_sync": "2026-10-20T06:58:00Z", "healthkit_types": 9 } }
```

`message` 1–4000 characters; `category` `bug`, `idea`, `data` or `other` (default);
`app_version`, `build`, `device_model`, `os_version` strings up to 64 characters;
`request_ids` up to 20 (each up to 64); `diagnostics` any JSON object up to 32 KiB,
stored as decoded (numbers as double precision; Errors → NUL). `204`. At most 10 per user and day (`429`). Deleted with the account. Do not put health
values into `message` or `diagnostics` that the user did not write themselves.

### `GET /v1/days?from=YYYY-MM-DD&to=YYYY-MM-DD` — daily scores

Both parameters optional; default is the 30 days ending today (local). Returns the
scored days in `[from, to]`, oldest first; days that were never scored are absent.
`400` on a malformed date, `422` when `from > to`.

```json
{ "day": "2026-10-18", "readiness": 84, "state": "balanced", "recovery": 82, "strain": 12.4,
  "steps": 9412, "active_kcal": 612, "strength_min": 59, "volume_kg": 7420, "sleep_performance": 87, "sleep_need_min": 538, "sleep_actual_min": 468, "sleep_debt_min": 137,
  "sleep_need_base_min": 460, "sleep_need_strain_min": 37, "sleep_need_debt_min": 41, "sleep_need_nap_min": 0, "nap_min": 25,
  "sleep_quality": 91, "sleep_consistency": 81,
  "hrv_ms": 58.2, "rhr": 48.9, "resp_rate": 14.6, "spo2": 96.4,
  "hrv_baseline_ms": 53.1, "rhr_baseline": 50.2, "baseline_days": 30, "baseline_hrv_days": 30, "baseline_rhr_days": 30,
  "calibrating": false, "calibrating_reason": null,
  "coverage": { "sleep_hr_minutes": 400, "hrv_readings": 5, "sleep_source": "Garmin Connect",
                "hr_source_hours": { "Garmin Connect": 14, "Apple Watch": 9 }, "night_complete": true, "status": "final" },
  "details": { … }, "computed_at": "2026-10-18T07:15:02.123456Z" }
```

`DailyScore`:

| Field | Type | Notes |
| --- | --- | --- |
| `day` | string | `YYYY-MM-DD` |
| `readiness` | int? | 1–100. Today's headline: `round(0.6·recovery + 0.4·sleep_performance)` minus up to 10 points when yesterday's strain was > 14 on a recovery < 50. `null` when there is no recovery. |
| `state` | string | `balanced` (readiness ≥ 67), `strained` (34–66), `depleted` (< 34), `calibrating` (fewer than 7 nights of baseline, or no readiness). |
| `recovery` | int? | 1–100. ≥ 67 green, 34–66 yellow, < 34 red. |
| `strain` | number? | 0–21, one decimal: heart-rate load plus the muscular load of completed strength sessions (`SCORING.md` → Strength). `null` when there was neither heart-rate data nor such a session that day. |
| `steps` | int? | Steps that day (local day), counting each hour from one device only (`SCORING.md`). `null` when no step samples arrived; days scored before this field existed stay `null` until rescored. |
| `active_kcal` | int? | Active energy that day in whole kcal, de-duplicated the same way. `null` without samples. |
| `strength_min` | int? | Minutes of the completed strength sessions started that day. `null` without one. |
| `volume_kg` | number? | Their volume, Σ reps × kg of working sets. `null` without a session. |
| `sleep_performance` | int? | 0–100, actual / need. |
| `sleep_need_min` | int? | Minutes: `sleep_need_base_min + sleep_need_strain_min + sleep_need_debt_min − sleep_need_nap_min`. |
| `sleep_need_base_min` | int? | The baseline the need started from (`User.sleep_base_min` when scored). |
| `sleep_need_strain_min` | int? | Top-up for yesterday's strain: 3 min per strain point, at most 60. |
| `sleep_need_debt_min` | int? | Top-up for sleep debt: 30 % of `sleep_debt_min`, at most 60. |
| `sleep_need_nap_min` | int? | Nap credit: yesterday's `nap_min`, which lowers last night's need. |
| `nap_min` | int? | Naps that day: sleep ending 10:00–20:00 local that is not part of the night, each nap ≥ 20 min, the sum capped at 90. It lowers the **following** night's need (the next day's `sleep_need_nap_min`). `0` when there was none; `null` for days scored before this field existed. |
| `sleep_quality` | int? | 0–100, the night's quality (`DayDetail.sleep.quality`, `SCORING.md` → Sleep): `0.6·performance + 0.2·efficiency + 0.2·restorative`. `null` without a night. |
| `sleep_consistency` | int? | 0–100: how regular bedtime and wake time were over the last 7 nights, `100 − 0.5·(σ bedtime + σ wake time)` in minutes. Needs 5 nights with data, else `null`. |
| `sleep_actual_min` | int? | Minutes asleep last night. |
| `sleep_debt_min` | int? | Decayed shortfall of the 7 nights before last night. |
| `hrv_ms` | number? | Overnight mean SDNN, ms. |
| `rhr` | number? | Resting HR, bpm. |
| `resp_rate` | number? | Overnight breaths/min. |
| `spo2` | number? | Overnight mean, percent. |
| `hrv_baseline_ms` | number? | The 30-day HRV baseline `hrv_ms` was scored against: geometric mean of up to 30 scored days before `day`. |
| `rhr_baseline` | number? | The 30-day resting-HR baseline `rhr` was scored against. |
| `baseline_days` | int | Nights of history behind the baselines (the smaller of HRV and resting-HR days among the 30 scored days before `day`), 0–30. "Calibrating — N nights to go" is `7 − baseline_days` and holds only when `calibrating_reason` is `short_history`; with `no_hrv` / `no_rhr` waiting does not end it. |
| `calibrating` | bool | `baseline_days < 7` on a scored day; `false` on a day that was never scored (the placeholder of `GET /v1/days/{day}`). |
| `baseline_hrv_days` | int? | N of the HRV baseline: scored days among the 30 before `day` that have an overnight HRV, 0-30. `baseline_days = min(baseline_hrv_days, baseline_rhr_days)`. `null` (read it as unknown) on a day that was never scored, on rows scored before this field existed (until rescored), and on a row last written by an older scoring image after a rollback that disagrees with `baseline_days` / `calibrating` (the server serves such a row's fields as unknown). |
| `baseline_rhr_days` | int? | The same for resting HR (scored days among the 30 before `day` with a resting HR), 0-30. Same `null` rule. |
| `calibrating_reason` | string? | Why `calibrating` is `true`: `short_history`, `no_hrv` or `no_rhr` (closed enum, rule below). `null` when `calibrating` is `false` and on a day that was never scored. Also `null` on a calibrating row scored before this field existed (until it is rescored) or written by an older scoring image after a rollback: treat that as "unknown", not as a fourth reason. |
| `scoring_state` | string | `scored`, `pending` (data newer than the score: scoring is queued or running), `waiting_for_sleep` (today: no night uploaded yet), `stale` (today: no new data for over 20 h). Past days are `scored` or `pending`. Closed enum, as `SyncStatus.today.state`. |
| `coverage` | `ScoreCoverage?` | What the day's night and heart rate are based on, and whether the night is complete: `status` `provisional` or `final`. `null` on a day that was never scored and on rows scored before 2026-10-31 (until rescored): read `null` as **final, nothing known**. See **Coverage and provisional scores** below. |
| `details` | object | Free-form scoring breakdown. Not part of the typed contract, but these keys stay as they are for clients that already read them: `baseline_days`, `trimp`, `hr_minutes`, `zone_minutes`, `sleep_start`, `sleep_end` (local RFC 3339), `sleep_stages`, `sleep_has_stages`, `recovery`. Prefer the typed fields: `baseline_days`, `DayDetail.strain`, `DayDetail.sleep`. |
| `computed_at` | timestamp | When the row was last scored. |
| `model_version` | string? | The scoring model that computed it (`GET /v1/model`), e.g. `2026.10.1`; `null` for rows scored before versions existed, until they are rescored. A row whose version differs from `GET /v1/model` → `current` will move when it is rescored. |

#### `calibrating_reason`

With `H = baseline_hrv_days`, `R = baseline_rhr_days` and `W` = the days with any data among the (up to) 30
scored days before `day` (the engine's window; a day stored without data, e.g. before the account's first sync, does
not count), the engine decides in this order:

1. `min(H, R) >= 7` (`calibrating == false`) gives `null`.
2. If the day brings a signal its window never had (`H == 0` and the day has an HRV, or `R == 0` and the day has a resting HR), `short_history`, for both signals: the source has just appeared.
3. `W >= 7 && H == 0` and the day itself has no HRV (`hrv_ms` is `null`) gives `no_hrv`: the history is long enough, yet not one night in the window, and not this one, has an HRV.
4. `W >= 7 && R == 0` and the day itself has no resting HR (`rhr` is `null`) gives `no_rhr`: likewise for resting HR.
5. Otherwise `short_history`.

So `calibrating_reason` is `null` exactly when `calibrating` is `false` (apart from the legacy and never-scored
`null`s in the table). Decisions: a day-one account has `H = R = 0` too but `W < 7`, so it is `short_history` and
waiting is correct; when both are 0, `no_hrv` wins because HRV is the heavier recovery signal and the app reads
`baseline_rhr_days == 0` for the second gap (no combined value). One to six HRV nights in the window is
`short_history` (sporadic data: more nights do end it), not `no_hrv`. So does the first morning a source arrives, for either signal: `H` and `R` count the days before `day` and are still 0, but the day's own `hrv_ms` (or `rhr`) is there, so it is `short_history`, not `no_hrv` / `no_rhr`. A new watch writes HRV at night and resting HR later in the morning, so without this step the morning would say `no_rhr` until the next sync.

What each reason means for the app (no copy here):

| Reason | Meaning | Does waiting end it? | Counts |
| --- | --- | --- | --- |
| `short_history` | Too little history to tell (fewer than 7 days with data), or a signal present on fewer than 7 days (including the first day one arrives, for either signal). | Yes: `7 - baseline_days` more nights with both. | "N of 7 nights" (`baseline_days`) is meaningful. |
| `no_hrv` | At least 7 days with data, but no overnight HRV in the 30-day window or on the day itself (phone only, or a watch that does not write HRV to Apple Health). | No, not until an HRV source appears. | Do not show "N of 7 nights"; `baseline_hrv_days` is 0. |
| `no_rhr` | At least 7 days with data, but no resting HR in the window or on the day itself. | No, until a resting-HR source appears. | Do not show "N of 7 nights"; `baseline_rhr_days` is 0. |

`recovery` and `readiness` may still be present while calibrating (recovery averages the inputs that exist, here
mostly sleep); `state` stays `calibrating`. Old clients ignore the new fields and keep showing `baseline_days`.
`calibrating_reason` is a closed enum in v1: it gains no values without `/v2`, so it decodes as a Swift enum.

Examples (the changed fields only):

```json
{ "baseline_days": 3,  "baseline_hrv_days": 3,  "baseline_rhr_days": 5,  "calibrating": true,  "calibrating_reason": "short_history" }
{ "baseline_days": 0,  "baseline_hrv_days": 0,  "baseline_rhr_days": 0,  "calibrating": true,  "calibrating_reason": "short_history" }
{ "baseline_days": 0,  "baseline_hrv_days": 0,  "baseline_rhr_days": 24, "calibrating": true,  "calibrating_reason": "no_hrv" }
{ "baseline_days": 0,  "baseline_hrv_days": 0,  "baseline_rhr_days": 24, "calibrating": true,  "calibrating_reason": "short_history" }
{ "baseline_days": 0,  "baseline_hrv_days": 28, "baseline_rhr_days": 0,  "calibrating": true,  "calibrating_reason": "short_history" }
{ "baseline_days": 0,  "baseline_hrv_days": 28, "baseline_rhr_days": 0,  "calibrating": true,  "calibrating_reason": "no_rhr" }
{ "baseline_days": 0,  "baseline_hrv_days": 0,  "baseline_rhr_days": 0,  "calibrating": true,  "calibrating_reason": "no_hrv" }
{ "baseline_days": 12, "baseline_hrv_days": 12, "baseline_rhr_days": 19, "calibrating": false, "calibrating_reason": null }
```

The second is day one (`W = 0`, whatever empty days are stored before it). The third has 24 resting-HR nights and no HRV in the window, and the day has no `hrv_ms`; the fourth is the first morning a watch's HRV arrives (`hrv_ms` present, `H` still 0), hence `short_history`. The fifth is the same for resting HR (`R` = 0, the day has `rhr`); the sixth has `R` = 0 and no `rhr` on the day, hence `no_rhr`. The seventh has 30 days with data, neither signal in the window and neither on the day (`no_hrv` by precedence).

#### Coverage and provisional scores

`DailyScore.coverage` (`ScoreCoverage`) says how much data the numbers rest on:

```json
{ "sleep_hr_minutes": 170, "hrv_readings": 0, "sleep_source": "Garmin Connect",
  "hr_source_hours": { "Garmin Connect": 3 }, "night_complete": false, "status": "provisional" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `sleep_hr_minutes` | int | Minutes of heart-rate data (de-duplicated, from whichever source won each hour, as `details.hr_minutes`) inside the winning sleep source's window, from its first segment's start to its last segment's end. `0` without a night. |
| `hrv_readings` | int | Number of overnight HRV readings (`hrv_sdnn` samples in the night) behind `hrv_ms`. `0` when `hrv_ms` is `null` for lack of them. |
| `sleep_source` | string? | The `source` (HealthKit name, as `UserSource.name`) whose sleep won the night (the longest, `SCORING.md`). `null` without a night. |
| `hr_source_hours` | `{string: int}` | Whole hours of the local day `day` (up to 24, fewer for today so far) for which each source was the one heart-rate source used, keyed by source name. `{}` without heart rate. The keys are source names, so treat them as data (they may contain underscores or spaces), not as field names. |
| `night_complete` | bool | The night is over: **wake evidence** (a sample of any type that starts at least 30 minutes after the winning sleep source's last segment ended, from that same source; for a source that wrote nothing but sleep in the last 7 days, a `steps` sample from any source counts) **or 10:00 local time on `day` has passed** (the ceiling; every earlier day is complete). It reflects the latest computation. |
| `status` | string | `provisional` or `final` (closed enum). `final` once `night_complete` has held, or at the 10:00 ceiling. **Sticky:** once a day is `final` it stays `final`, even if a later sleep segment makes `night_complete` `false` again; its numbers still follow later scorings. So `final` with `night_complete: false` is possible, `provisional` with `night_complete: true` is not. |

`coverage` is an object that may gain fields (and `hr_source_hours` has open keys): ignore
what you do not know. The score fields stay as they are; a `provisional` day carries a real
score computed from what has arrived, and a rescore of it (as more of the night syncs) changes
the numbers. After the 10:00 ceiling, or once the night is complete, a day is rescored into
`final` by the next scoring (the scoring pass runs every 15 minutes for active users, so by about 10:15 at the latest).

**Floors.** Below a floor, a component is *absent*, never a low score:

| Floor | Rule |
| --- | --- |
| Sleep heart rate | `sleep_hr_minutes` < **120**: Recovery drops the resting-heart-rate input and is renormalised over the inputs it has (as it already does without HRV). The stored `rhr` field, `rhr_baseline`, `baseline_rhr_days` and `calibrating_reason` are **not** changed by this. |
| Sleep | Less than **60** minutes of sleep from the winning source: `sleep_performance` is `null` (and what follows from a missing performance, as for a day without a night); `sleep_actual_min` and `SleepDetail` still report the minutes. |

Both are in `SCORING.md` with the model version that introduced them. A client recognises the
first floor by `coverage.sleep_hr_minutes < 120` (and the second by `sleep_performance` being `null`
with `sleep_actual_min` below 60).

**Morning push.** `MORNING_SCORE` is sent on the first scoring of the day with a `final`
status, a recovery and a night's sleep, not on the first scoring (Push notifications). The silent
refresh push still follows **every** scoring, provisional ones too, so the app can show the
provisional number and then the final one.

Examples of what a client sees for one morning: after a 03:00 Garmin sync, the `coverage` above
(`provisional`, 170 minutes of heart rate) and no push; after the watch syncs the rest of the
night, the same day with `"sleep_hr_minutes": 400, "hrv_readings": 5, "night_complete": true,
"status": "final"`, the push, and a silent refresh for both.

### `GET /v1/days/{day}` — one day

```jsonc
{ "score": DailyScore, "sleep": SleepDetail?, "strain": DayStrain?, "workouts": [Workout] }
```

A day that has not been scored yet returns `200` with a `score` whose numbers are
all `null`, `state: "calibrating"`, `calibrating: false`, `computed_at:
"0001-01-01T00:00:00Z"`, `coverage: null`, and `sleep: null`, `strain: null`, `workouts: []`. `400` on a
malformed day. `sleep` is `null` when no sleep was recorded for the night ending on
`day` (also for days scored before the platform upgrade until they are recomputed).

`SleepDetail` — last night as the Sleep screen's *Day* tab shows it:

```json
{
  "start": "2026-10-17T21:00:00Z", "end": "2026-10-18T05:12:00Z",
  "total_min": 468, "in_bed_min": 492, "efficiency": 95,
  "stages": [ {"stage":"deep","min":112,"pct":24}, {"stage":"light","min":261,"pct":56},
              {"stage":"rem","min":95,"pct":20},  {"stage":"awake","min":10,"pct":2} ],
  "quality": 91, "quality_label": "good", "restorative": 100, "has_stages": true,
  "need": { "base": 460, "strain": 37, "debt": 41, "nap_credit": 0, "total": 538 },
  "segments": [ {"stage":"light","start":"2026-10-17T21:12:00Z","end":"2026-10-17T21:40:00Z"}, … ]
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `start`, `end` | timestamp | First and last sleep segment of the night (including `inbed`). |
| `total_min` | int | Minutes asleep (= `score.sleep_actual_min`). |
| `in_bed_min` | int | Recorded time in bed; without an `inbed` record, the span `start`→`end`; never less than asleep + awake. |
| `efficiency` | int | `round(100 · total_min / in_bed_min)`, 0–100. |
| `stages` | `[SleepStage]` | Always four entries in this order: `deep`, `light`, `rem`, `awake`. HealthKit `core` is reported as `light`. |
| `quality` | int | 0–100: `round(0.6·performance + 0.2·efficiency + 0.2·restorative)`, with performance = `score.sleep_performance`. Was equal to performance before 2026-10-19. |
| `restorative` | int | 0–100: the share of deep + REM sleep against 40 % of time asleep, `min(100, (deep + rem) / asleep / 0.40 · 100)`; equal to performance when the source records no stages. |
| `quality_label` | string | `good` (quality ≥ 80), `fair` (60–79), `poor` (< 60). |
| `need` | `SleepNeed` | Why the night's need is what it is: `{base, strain, debt, nap_credit, total}` in minutes, the `sleep_need_*` fields of the score (`total = base + strain + debt − nap_credit` = `score.sleep_need_min`). Days scored before 2026-10-19 report the whole need as `base` until they are rescored. Only on this endpoint. |
| `has_stages` | bool | `false` when the source recorded no stages (older firmware): `deep`/`light`/`rem` are then 0 min and 0 %. |
| `segments` | `[SleepInterval]` | The hypnogram, oldest first: `{stage, start, end}` with `stage` one of `deep`, `light`, `rem`, `awake`, `asleep` (stage unknown). Touching segments of one stage are merged; `inbed` is not a stage and is left out. Only on this endpoint. |

`DayStrain` — the load behind `score.strain` (Strain screen); `null` exactly when
`score.strain` is `null` (no heart-rate data and no completed strength session that day):

```json
{ "trimp": 99, "hr_min": 912, "zone_min": [12, 24, 37, 7, 0], "steps": 9412, "active_kcal": 612,
  "muscular_trimp": 55, "sessions": [ SessionSummary, … ] }
```

| Field | Type | Notes |
| --- | --- | --- |
| `trimp` | int | Banister TRIMP for the whole day. |
| `hr_min` | int | Minutes of usable heart-rate data (watch worn). |
| `zone_min` | `[int]` | Five entries: minutes at 50–60, 60–70, 70–80, 80–90 and ≥ 90 % of heart-rate reserve. |
| `steps` | int? | Same as `score.steps`, for the Strain screen. |
| `active_kcal` | int? | Same as `score.active_kcal`. |
| `muscular_trimp` | int | Muscular load of the day's completed strength sessions (TRIMP-equivalent); `score.strain` is the strain of `trimp + muscular_trimp`. `0` without sessions; then `hr_min` and `zone_min` may be `0` too on a day with only a session. |
| `sessions` | `[SessionSummary]` | The day's completed sessions, oldest first (see **Train**); `[]` without. |

`SleepStage`: `{ "stage": string, "min": int, "pct": int }`. For `deep`, `light`, `rem`,
`pct` is the share of staged sleep and the three always sum to exactly 100 (or are all 0
when `has_stages` is false). For `awake`, `pct` is the share of `in_bed_min`.

### `GET /v1/sleep?range=week|month&to=YYYY-MM-DD` — Week / Month tabs

`range` is `week` (7 days) or `month` (30 days), default `week`; `to` is the last day,
default today. `422` on another `range`, `400` on a malformed `to`.

```json
{
  "range": "week", "from": "2026-10-12", "to": "2026-10-18", "nights": 7,
  "days": [ { "day": "2026-10-12", "start": "…", "end": "…", "total_min": 475, "in_bed_min": 511,
              "efficiency": 93, "stages": [ … ], "quality": 93, "quality_label": "good", "restorative": 96,
              "has_stages": true, "consistency": 84, "nap_min": 0 }, … ],
  "avg_total_min": 455, "avg_in_bed_min": 486, "avg_efficiency": 94, "avg_quality": 90, "avg_consistency": 83,
  "avg_stages": [ {"stage":"deep","min":85,"pct":19}, … ],
  "bedtimes": [ { "day": "2026-10-12", "bed": "2026-10-11T21:08:00Z", "wake": "2026-10-12T05:01:00Z" }, … ]
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `range`, `from`, `to` | string | The window actually served, inclusive. |
| `days` | `[SleepDay]` | Only nights with data, oldest first. A `SleepDay` is `day` plus every `SleepDetail` field except `segments` and `need`, plus `consistency` (int?, = that day's `score.sleep_consistency`) and `nap_min` (int?, = `score.nap_min`). Missing nights are absent — fill the gaps from `from`/`to`. |
| `nights` | int | `len(days)`. |
| `avg_total_min`, `avg_in_bed_min`, `avg_quality` | int? | Means over `days`; `null` when there are none. |
| `avg_efficiency` | int? | `avg_total_min / avg_in_bed_min`. |
| `avg_stages` | `[SleepStage]` | Mean minutes per stage over nights with `has_stages`, same order and `pct` rules as `stages`; `[]` when no night had stages. |
| `avg_consistency` | int? | Mean of `days[].consistency`; `null` when no day has one. |
| `bedtimes` | `[SleepBedtime]` | `{day, bed, wake}` for each night in `days` (the night's start and end) for the bedtime / wake-time strip. |

### `GET /v1/sleep/plan?wake=HH:MM` — Sleep Planner

`wake` is a local wake time (`07:00`): the next time it occurs, today or tomorrow. `422`
when it is missing or not `HH:MM`.

```json
{ "wake": "2026-10-20T05:00:00Z", "need_min": 512, "in_bed_min": 569, "efficiency": 90,
  "bedtime_100": "2026-10-19T19:31:00Z", "bedtime_85": "2026-10-19T20:56:00Z", "bedtime_70": "2026-10-19T22:22:00Z",
  "basis": { "base": 460, "strain_topup": 36, "debt_topup": 16, "nap_credit": 0, "efficiency": 90 } }
```

| Field | Type | Notes |
| --- | --- | --- |
| `wake` | timestamp | The wake time asked for, resolved. |
| `need_min` | int | Tonight's need: `base + strain_topup + debt_topup − nap_credit`. |
| `in_bed_min` | int | Time in bed for the full need: `need_min / efficiency`. |
| `efficiency` | int | The efficiency planned with: the mean of the last 7 nights, never below 85 (90 without nights). |
| `bedtime_100`, `bedtime_85`, `bedtime_70` | timestamp | When to go to bed to get 100 %, 85 % and 70 % of the need: `wake − share · in_bed_min`. Ascending. |
| `basis.base` | int | `User.sleep_base_min`. |
| `basis.strain_topup` | int | **Today's** strain so far × 3 min, at most 60 (the night's score will use the day's final strain). |
| `basis.debt_topup` | int | 30 % of the debt including last night, at most 60. |
| `basis.nap_credit` | int | Today's naps (`score.nap_min`), at most 90. |
| `basis.efficiency` | int | As `efficiency`. |

### `GET /v1/workouts?from&to` — workouts

Same `from`/`to` semantics as `/v1/days`. Workouts whose start falls in the range,
oldest first.

`Workout`:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | The HealthKit workout UUID. |
| `activity` | string | e.g. `running`; `other` when unknown. |
| `start`, `end` | timestamp | |
| `avg_hr`, `max_hr` | number? | bpm, as sent by the app. Omitted when absent. |
| `energy_kcal` | number? | Omitted when absent. |
| `distance_m` | number? | Omitted when absent. |
| `source` | string | |
| `strain` | number? | 0–21, filled by scoring, including an attached session's muscular load. Omitted until scored. |
| `muscular_load` | number? | Muscular load (TRIMP-equivalent) of the strength session attached to this workout. Omitted when none. |
| `session_id` | uuid? | That session. Omitted when none. A workout with `source` `ryft-session` was created for a session that has no matching HealthKit workout. |
| `model_version` | string? | The scoring model of its `strain`, as on `DailyScore`. Omitted until scored. |
| `also_recorded_by` | `[string]?` | The `source` names of other recordings of this same workout that the server hid (a Watch and a Garmin that both recorded one run): "also recorded by Garmin Connect". Distinct, sorted. Omitted when there are none (a client treats missing and `[]` alike). Ignored on input. |

**Duplicate workouts.** One activity must appear once. Two rules, both server-side:

- *Same source.* Copies of one `source` with the same `activity` whose `start`s and whose `end`s each lie within
  120 seconds of each other are one workout (Garmin Connect re-writes a workout in Apple Health under a new UUID and deletes the old one).
  When a sync stores such a copy, the server **removes the older copies** and keeps the most recently uploaded id (the one Apple Health still has); a
  one-off run does the same for copies stored earlier. The removed ids answer `404` on `GET /v1/workouts/{id}`, a Train session that was attached to one stays and is
  re-attached to the kept workout when the day is rescored (`session_id`, `muscular_load`). These removals are not counted in `SyncResponse.deleted`, which counts the requested deletions only; their days are queued.
- *Different sources.* Workouts of **different** `source`s with the same `activity` (either one `other` counts as the same) that overlap by at least **80 %** of the shorter one's duration are one workout. Nothing is deleted. The server picks a **primary** (the one with at least 2 heart-rate samples from its own source inside its window, then the longer one, then the lower `id`), lists only the primary in `GET /v1/days/{day}` → `workouts` and `GET /v1/workouts`, and puts the other sources in the primary's `also_recorded_by`. The primary's heart-rate window, zones and strain are the day's de-duplicated heart rate, so the day's strain does not change. `DayDetail.workouts` and `details.workouts` count the visible workouts.
- A workout with `source` `ryft-session` is never hidden. The hiding is decided when the day is scored, so a copy can show until `scoring_state` leaves `pending`; if the primary is deleted its duplicate shows again at once and a later scoring picks a new primary. `GET /v1/workouts/{id}` with a hidden duplicate's id still answers `200` with that workout as stored (without `also_recorded_by`), so a link the app holds keeps working.

```json
{ "id": "7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12", "activity": "running", "start": "2026-10-20T16:02:00Z", "end": "2026-10-20T16:58:30Z",
  "avg_hr": 156.2, "max_hr": 181, "distance_m": 10240, "energy_kcal": 702, "source": "Garmin Connect",
  "strain": 12.8, "model_version": "2026.10.3", "also_recorded_by": ["Tim's Apple Watch"] }
```

### `GET /v1/workouts/{id}` — one workout

`id` is the workout UUID. `400` when it is not a UUID, `404` when no such workout.
Returns every `Workout` field plus:

```json
{
  "id": "…", "activity": "running", "start": "2026-10-17T17:30:00Z", "end": "2026-10-17T18:40:00Z",
  "avg_hr": 151.6, "max_hr": 178, "source": "Garmin Connect", "strain": 14.9,
  "hr": [ {"t":"2026-10-17T17:30:00Z","bpm":96}, {"t":"2026-10-17T17:31:00Z","bpm":131}, … ],
  "zone_min": [2, 24, 37, 7, 0],
  "trimp": 99
}
```

| Field | Type | Notes |
| --- | --- | --- |
| `avg_hr`, `max_hr` | number? | As sent by the app; when the app sent none, derived from the heart-rate samples in the workout window. Still omitted when there are no samples either. |
| `hr` | `[HRSample]` | The heart-rate curve between `start` and `end` (inclusive, also past midnight), oldest first, at most 600 points. Longer series are down-sampled with Largest-Triangle-Three-Buckets, so peaks survive and every point is a real sample. `[]` when there are no samples. |
| `HRSample.t` | timestamp | |
| `HRSample.bpm` | int | Rounded bpm. |
| `zone_min` | `[int]?` | Five entries, minutes in the heart-rate-reserve zones (as `DayStrain`). `null` until scored. |
| `trimp` | int? | Banister TRIMP of the workout. `null` until scored. |
| `strain` | number? | 0–21. Omitted until scored. |

A workout logged without heart-rate samples is scored from `avg_hr` and duration:
the whole duration counts at that intensity and in that zone.

### `GET /v1/trends?metric=…&range=7d|30d|90d&to=YYYY-MM-DD` — Trends charts

| Parameter | Values | Default |
| --- | --- | --- |
| `metric` | `readiness`, `recovery`, `strain`, `sleep` (sleep performance %), `sleep_min` (minutes asleep), `hrv` (ms), `rhr` (bpm), `steps`, `active_kcal`, `strength_min`, `volume_kg`, `sleep_quality`, `sleep_consistency`, `nap_min` | required |
| `range` | `7d`, `30d`, `90d` — days ending at `to`, inclusive | `7d` |
| `to` | `YYYY-MM-DD` | today |

`422` on a missing/unknown `metric` or `range`, `400` on a malformed `to`.

```json
{ "metric": "hrv", "range": "7d", "from": "2026-10-12", "to": "2026-10-18",
  "points": [ {"day":"2026-10-12","value":47.4}, {"day":"2026-10-13","value":50.8}, … ],
  "avg": 51.7, "min": 47.1, "max": 57.6, "baseline": 52.3 }
```

| Field | Type | Notes |
| --- | --- | --- |
| `metric`, `range`, `from`, `to` | string | The window actually served. |
| `points` | `[TrendPoint]` | `{day, value}` for each scored day with a value, oldest first. Days without a value are absent (a gap in the chart), so there can be fewer points than days. Same numbers as the matching `DailyScore` field. |
| `avg` | number? | Mean of `points`, one decimal. `null` without points. |
| `min`, `max` | number? | `null` without points. |
| `baseline` | number? | `hrv` and `rhr` only: the 30-day baseline stored on the most recent day in the window that has one (= that day's `hrv_baseline_ms` / `rhr_baseline`). `null` for other metrics. |

### `GET /v1/insights` — Insights screen

Short rule-based cards about today, most urgent first (`warn`, then `notice`, then
`info`). `[]` when nothing stands out. Computed on read from the last 30 days and cached
for 5 minutes, so a sync can take up to 5 minutes to change the cards.

```json
[ { "id": "hrv-low-3d", "severity": "warn", "title": "HRV below your baseline for 3 nights",
    "body": "Your overnight HRV has been under your 30-day baseline of 55 ms for 3 nights in a row (47 ms last night). …",
    "metric": "hrv", "day": "2026-10-18" } ]
```

| Field | Type | Notes |
| --- | --- | --- |
| `id` | string | Stable per rule (table below); use it for icons and de-duplication. New ids may appear — show unknown ones generically. |
| `severity` | string | `warn`, `notice` or `info` (positive news is `info`). |
| `title`, `body` | string | Display text, English, numbers already formatted. |
| `metric` | string | A `GET /v1/trends` metric to deep-link to. |
| `day` | string | The day the card is about (today, or yesterday when today has not synced yet; for streaks, the latest day). |

| `id` | Severity | Fires when |
| --- | --- | --- |
| `hrv-low-3d` | warn | Overnight HRV below its 30-day baseline for ≥ 3 consecutive nights. |
| `rhr-elevated` | warn | Last night's resting HR ≥ 5 bpm above its baseline. |
| `strain-no-recovery` | warn | ≥ 3 days at strain ≥ 14 in the last 14 days with no recovery ≥ 67 since the first of them. |
| `sleep-debt` | notice | Sleep debt (`DailyScore.sleep_debt_min`) ≥ 90 min. |
| `bedtime-irregular` | notice | Bedtime spread (latest − earliest) > 90 min over the last 7 nights (≥ 5 nights with data). |
| `readiness-up` | info | Readiness rising ≥ 1 point/day over the last 7 days (least-squares fit, ≥ 5 days). |
| `hrv-best-30d` | info | Last night's HRV is the highest of the last 30 days (≥ 14 nights of data). |
| `pr` | info | A personal record in a session completed today or yesterday (the newest; an `e1rm` record before the others). Metric `volume_kg`. |
| `no-heart-rate-source` | info | Data arrived in the last 7 days (steps, sleep) but no heart rate: strain, recovery and readiness need a watch or ring that writes heart rate to Apple Health. Fixed copy. Metric `strain`. |
| `max-hr-observed` | info | In the last 90 days a workout went at least 3 bpm above `max_hr` on two different days; the card names the lower of the two highest days' peaks as a likely max HR (nothing changes by itself). Metric `strain`. |
| `behavior-impact` | notice | A behavior logged for yesterday whose effect on recovery is unlocked, significant and negative (the largest such): "Alcohol yesterday — your recovery is typically 14 points lower". Metric `recovery`. |
| `sleep-consistency-low` | notice | `sleep_consistency` below 60 (over the last 7 nights). Metric `sleep_consistency`. |
| `nap-credit` | info | A nap of ≥ 45 min today (or yesterday, before today syncs): tonight's need is that much lower. Metric `nap_min`. |
| `baseline-learned` | info | The learned sleep baseline became the one in use in the last 2 days (`sleep_base_source` turned `learned`). Metric `sleep_min`. |

Baseline-relative rules stay silent while `calibrating`.

### `POST /v1/recompute` — rescore a range

Body `{ "from": "YYYY-MM-DD", "to": "YYYY-MM-DD" }` (inclusive, `from ≤ to`, `to − from`
at most 400 days). At most one request per user per minute: a second one gets `429` with
`Retry-After`. Responds `202 {"status":"queued","from":"…","to":"…"}`; scoring
rebuilds the days oldest first; a 90-day range takes a few seconds. `422` on bad dates, an
inverted or too-large range; `503` if the queue is down. Use it after `PATCH /v1/me`
and after the first big backfill.

### Train — exercises, plans, sessions

Strength training lives inside the Strain screen: an exercise library (built-in and
the user's own), training plans with an enrolment, and logged sessions whose sets
become muscular load on the day's strain (`SCORING.md` → Strength). Every id in this
section is a UUID; the ids of sessions, their exercises and sets, custom exercises and
custom plans are generated by the app, so it can create them offline. Built-in
exercises and plans have fixed ids that never change.

**Language.** Built-in exercises have an English and a German name. `name` follows the
request's `Accept-Language` (`de…` → German, anything else → English); `name_en` and
`name_de` are always both present.

**Closed enums** (decode as Swift enums): `kind`, `load_type`, the muscle names, plan
`goal`, `target_kind`, `progression.type`, session `status`, session `origin`, enrolment `status`, PR
`kind`. `equipment` entries are open strings.

| Muscle names (`primary_muscles`, `secondary_muscles`, `?muscle=`) |
| --- |
| `chest`, `lats`, `upper_back`, `traps`, `front_delts`, `side_delts`, `rear_delts`, `biceps`, `triceps`, `forearms`, `abs`, `obliques`, `lower_back`, `glutes`, `quads`, `hamstrings`, `adductors`, `calves` |

#### `GET /v1/exercises?q=&muscle=&kind=&include_archived=` — the library

All built-in exercises plus the user's own, sorted by `name`. `q` matches a substring of
either name or the slug (case-insensitive); `muscle` matches a primary or secondary
muscle; `kind` one kind; archived exercises only with `include_archived=true`. `422` on
an unknown `muscle` or `kind`.

```json
[ { "id": "053b5627-084e-5417-a033-a6602d812f86", "slug": "back-squat", "name": "Back squat",
    "name_en": "Back squat", "name_de": "Kniebeuge", "kind": "barbell",
    "primary_muscles": ["quads", "glutes"], "secondary_muscles": ["hamstrings", "adductors", "lower_back"],
    "equipment": ["barbell", "rack"], "load_type": "weight", "bodyweight_factor": 0,
    "is_unilateral": false, "is_custom": false, "archived": false } ]
```

`Exercise`:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | |
| `slug` | string | Unique per owner, e.g. `back-squat`; derived from the name for custom exercises. |
| `name` | string | In the request's language (see above). |
| `name_en`, `name_de` | string | Both names; a custom exercise has the same name twice unless `name_de` was given. |
| `kind` | string | `barbell`, `dumbbell`, `kettlebell`, `bodyweight`, `machine`, `cable`, `cardio`. |
| `primary_muscles`, `secondary_muscles` | `[string]` | Muscle names (table above); at least one primary. |
| `equipment` | `[string]` | Open strings, e.g. `barbell`, `bench`, `rack`, `pullup_bar`. |
| `load_type` | string | What a set records: `weight` (reps × kg), `bodyweight` (reps; `weight_kg` is added load, e.g. a belt), `time` (`duration_sec`), `distance` (`distance_m`). |
| `bodyweight_factor` | number | Share of body weight moved in a `bodyweight` exercise (push-up 0.64, pull-up 1.0); 0 otherwise. |
| `is_unilateral` | bool | Sets log reps **per side**; the load counts both sides. |
| `is_custom` | bool | `true` for the user's own exercises. |
| `archived` | bool | Hidden from the library, still resolvable in old sessions and plans. |

#### `POST /v1/exercises` — create a custom exercise

```json
{ "id": "9b0e…", "name": "Landmine press", "name_de": "Landmine-Drücken", "kind": "barbell",
  "primary_muscles": ["front_delts", "chest"], "secondary_muscles": ["triceps"],
  "equipment": ["barbell", "landmine"], "load_type": "weight", "is_unilateral": true }
```

Required: `id`, `name` (1–80 characters), `kind`, `primary_muscles`, `load_type`.
Optional: `name_de` (default `name`), `secondary_muscles`, `equipment`,
`bodyweight_factor` (default 0.65 for `bodyweight`, else 0), `is_unilateral` (default
`false`). `201` with the `Exercise`; repeating the request with the same `id` updates it
and answers `200` (safe to retry). The repeat replaces every field the request can set,
`archived` included: it is `false` afterwards, so **posting an archived custom exercise's
id un-archives it** (no `PATCH` needed; [T12](#t12)). `409` when the `id` belongs to a built-in or another
user's exercise; `422` on invalid values.

#### `PATCH /v1/exercises/{id}` · `DELETE /v1/exercises/{id}`

`PATCH` takes any of the `POST` fields except `id`, plus `archived`; `200` with the
`Exercise`. `DELETE` archives (it never removes an exercise that sessions refer to):
`204`, idempotent. Both: `403` for a built-in exercise, `404` when the exercise is not
the user's.

#### `GET /v1/exercises/{id}/history?limit=50` — exercise detail

The user's completed sessions with this exercise, newest first (`limit` 1–200, default
50), its personal records and the best estimated 1RM per day. `404` when the exercise is
neither built-in nor the user's.

```json
{ "exercise_id": "053b5627…",
  "prs": [ { "exercise_id": "053b5627…", "kind": "e1rm", "value": 142.5, "set_id": "…", "session_id": "…",
             "achieved_at": "2026-10-17T17:41:00Z" } ],
  "sessions": [ { "session_id": "…", "date": "2026-10-17", "started_at": "2026-10-17T17:02:00Z",
                  "sets": [ Set, … ] } ],
  "e1rm": [ { "day": "2026-09-20", "value": 131.3 }, { "day": "2026-10-17", "value": 142.5 } ] }
```

`e1rm` covers the same sessions, oldest first, `weight` and loaded `bodyweight` sets only:
`weight × (1 + reps/30)` with reps capped at 12 (Epley), one decimal.

`PR` — a personal record, the best value of one `kind` for one exercise:

| Field | Type | Notes |
| --- | --- | --- |
| `exercise_id` | uuid | |
| `kind` | string | `e1rm` (kg, Epley as above), `max_weight` (kg in one set), `max_reps` (reps in one set), `max_volume` (reps × kg in one set). `bodyweight` exercises have `max_reps`, and `max_weight` for added load; `time` and `distance` exercises have none. |
| `value` | number | One decimal for kg, whole for reps. |
| `set_id`, `session_id` | uuid | The set that set the record (the first one, on a tie). |
| `achieved_at` | timestamp | That set's `completed_at` (or the session's end). |

#### `GET /v1/plans` — plans overview

```json
{ "builtin": [ PlanSummary, … ], "mine": [ PlanSummary, … ], "active": Enrollment? }
```

`PlanSummary`: `{ id, slug?, name, goal, weeks?, days_per_week, description, is_builtin,
archived, day_count, exercise_count }` (the `Plan` fields below without `days`, plus the
number of plan days and plan exercises). `mine` leaves archived plans out. `active` is
the active enrolment, or `null`.

`Enrollment`:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | |
| `plan_id` | uuid | |
| `started_at` | timestamp | |
| `current_week` | int | 1-based. |
| `current_day_index` | int | 0-based, the next plan day to train. |
| `status` | string | `active`, `paused`, `completed`. At most one enrolment is `active`. |

#### `GET /v1/plans/{id}` — one plan

Built-in or the user's own; `404` otherwise.

```json
{ "id": "…", "slug": "full-body-3x", "name": "Full Body 3×", "goal": "strength", "weeks": null,
  "days_per_week": 3, "description": "…", "is_builtin": true, "archived": false,
  "progression": { "type": "linear", "increment_kg": 2.5, "on_fail": "repeat", "deload_after": 3 },
  "days": [ { "id": "…", "week": null, "day_index": 0, "name": "A",
              "exercises": [ { "id": "…", "exercise_id": "053b5627…", "exercise": Exercise, "position": 0,
                               "sets": 5, "reps_min": 5, "reps_max": 5, "target_kind": "kg",
                               "target_value": 20, "rest_sec": 180, "superset_group": null, "notes": "" } ] } ] }
```

`Plan`:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | |
| `slug` | string? | Built-in plans only. |
| `name`, `description` | string | |
| `goal` | string | `strength`, `hypertrophy`, `weightlifting`, `general`. |
| `weeks` | int? | Length of the plan; `null` repeats forever. |
| `days_per_week` | int | 1–7. |
| `progression` | object? | `{"type":"linear","increment_kg":2.5,"on_fail":"repeat","deload_after":3}` (raise the target after every set was completed at the top of the rep range, repeat it after a miss, 10 % less after `deload_after` misses in a row), `{"type":"percent"}` (targets follow the current e1RM) or `{"type":"none"}`. `null` = `none`. |
| `is_builtin` | bool | Built-in plans are read-only. |
| `archived` | bool | |
| `days` | `[PlanDay]` | Ordered by `week`, then `day_index`. |
| `PlanDay.week` | int? | 1-based week this day belongs to; `null` = the same every week. |
| `PlanDay.day_index` | int | 0-based position in the week (0 … `days_per_week` − 1). |
| `PlanDay.name` | string | e.g. `Upper A`. |
| `PlanExercise.exercise_id` | uuid | |
| `PlanExercise.exercise` | `Exercise` | Resolved, read-only (ignored on `PUT`). |
| `PlanExercise.position` | int | 0-based order within the day. |
| `PlanExercise.sets`, `reps_min`, `reps_max` | int | 1–20 sets; 1 ≤ `reps_min` ≤ `reps_max` ≤ 100. |
| `PlanExercise.target_kind` | string | `pct_1rm` (`target_value` is percent of e1RM, e.g. 80), `rpe` (`target_value` is the RPE, 1–10), `kg`, `bodyweight` (`target_value` is `null`, or the added load in kg, 0–500; [T11](#t11)). |
| `PlanExercise.target_value` | number? | See `target_kind`. For `bodyweight` it is informational: the app shows it as the planned added load; the server never turns it into `target_kg`. |
| `PlanExercise.rest_sec` | int | 0–900. |
| `PlanExercise.superset_group` | int? | Exercises of one day with the same number form a superset. |
| `PlanExercise.notes` | string | |

#### `PUT /v1/plans/{id}` · `DELETE /v1/plans/{id}` — the user's own plans

`PUT` creates or replaces the whole plan document (the `Plan` shape; `id` must match the
path; `slug`, `is_builtin` and `PlanExercise.exercise` are ignored): `201` when created,
`200` when replaced, with the stored `Plan`. Day and plan-exercise ids come from the app;
ids left out of a replacement are deleted. `403` for a built-in plan, `409` when the id
belongs to another user's plan, `422` on invalid values or an exercise that is neither
built-in nor the user's. `DELETE` removes the plan and any enrolment in it: `204`; past
sessions keep their sets (their `plan_day_id` becomes `null`). Request bodies up to
256 KiB.

#### `POST /v1/plans/{id}/enroll` · `DELETE /v1/plans/enrollment`

`POST` makes the plan the active one: `201` with the `Enrollment` (week 1, day 0), or
`200` resuming a paused enrolment in the same plan where it stopped. Another active
enrolment is paused first. `404` for an unknown plan.

**Enrolling where the user is** ([T8](#t8)). The body is optional (none, empty or `{}`: the
behaviour above, unchanged). With `EnrollPost` the enrolment starts, or is moved, to a
position:

```json
{ "started_at": "2026-06-01T00:00:00Z", "current_week": 3, "current_day_index": 1 }
```

| Field | Type | Notes |
| --- | --- | --- |
| `started_at` | timestamp? | When the plan began. Default: now for a new enrolment, the stored value for an existing one. Not before 2000-01-01, at most 24 h in the future. |
| `current_week` | int? | 1-based. Default 1 (existing: unchanged). At most the plan's `weeks`; plans with `weeks` `null` accept 1–1000. |
| `current_day_index` | int? | 0-based, the next plan day to train. Default 0 (existing: unchanged). 0 … `days_per_week` − 1. |

A field that is absent or `null` takes its default. With at least one field set, an
enrolment of this plan that is already active, or paused, is **updated in place** (`200`,
status `active`) instead of being returned or resumed as it was; with none, a new one is
created (`201`). An unknown field is `400`; `422` when a value is out of range or the plan has
no day at that week and index (the same lookup as `GET /v1/plans/today`; a plan whose days
have `week` `null` has a day for every week). Importing sessions never moves an enrolment
(`POST /v1/train/import`); this is how an app places one where the imported history ends.

`DELETE` pauses the active enrolment: `204`, also when there is none. Completing a session of the current plan
day moves the enrolment to the next day (and week); after the last day of a plan with
`weeks` it becomes `completed`.

#### `GET /v1/plans/today` — what to train today

`204` (no body) without an active enrolment. Otherwise:

```json
{ "enrollment": Enrollment, "day": PlanDay,
  "targets": [ { "plan_exercise_id": "…", "exercise_id": "053b5627…", "sets": 5, "reps_min": 5, "reps_max": 5,
                 "target_kind": "kg", "target_kg": 102.5, "rest_sec": 180,
                 "last_time": [ Set, … ] } ] }
```

| Field | Type | Notes |
| --- | --- | --- |
| `day` | `PlanDay` | The enrolment's current day. |
| `targets` | `[PlanTarget]` | One per plan exercise of that day, in `position` order. |
| `PlanTarget.target_kg` | number? | The weight to use, rounded to 2.5 kg: `pct_1rm` from the current e1RM, the best of the exercise's completed working sets in the **last 90 days** (imported sessions included; model 2026.10.2), `kg` with the plan's progression applied to the history of this plan exercise. `null` for `rpe` and `bodyweight` targets, and for `pct_1rm` without a completed set of the exercise in the last 90 days (an older best is history, not a target). |
| `PlanTarget.last_time` | `[Set]` | The working sets of the most recent completed session with this exercise (imported sessions count, by `started_at`); `[]` when none. |

#### `PUT /v1/sessions/{id}` — save a session (after every set)

The whole session document as the app has it; idempotent, so the app can replay its
offline queue. Body: the `Session` shape below (`id` must match the path;
`muscular_trimp`, `strain`, `volume_kg`, `workout_id`, `prs`, `origin`, `import_source`
and `updated_at` of the session are ignored). Up to 256 KiB, 50 exercises, 50 sets per exercise.

```json
{ "id": "a7f2…", "title": "Upper A", "started_at": "2026-10-18T17:02:00Z", "ended_at": null,
  "status": "in_progress", "plan_day_id": "…", "notes": "", "bodyweight_kg": 80, "device_id": "…",
  "exercises": [ { "id": "…", "exercise_id": "053b5627…", "plan_exercise_id": "…", "position": 0, "notes": "",
                   "sets": [ { "id": "…", "set_index": 0, "reps": 5, "weight_kg": 100, "assist_kg": null, "rpe": 8,
                               "duration_sec": null, "distance_m": null, "is_warmup": false,
                               "completed_at": "2026-10-18T17:09:30Z", "updated_at": "2026-10-18T17:09:31Z" } ] } ] }
```

Rules:

- **Merge while `in_progress`.** Exercises and sets are matched by `id`. Ones in the body
  are inserted or updated; ones the server has but the body leaves out are **kept** (an
  older queued save never deletes newer sets).
- **Conflict.** When a set exists on both sides and the server's `updated_at` is later
  than the body's, nothing is written and the answer is **`409` with the server's
  `Session` as the body** (not the error shape). Merge by set id, keep the later
  `updated_at` per set, and `PUT` again.
- **Completed sessions** are replaced whole: sets and exercises left out are deleted, and
  the day is rescored. `status` `completed` is only accepted for a session that is
  already completed; use `…/complete` to finish one. `discarded` keeps the session out
  of every total.
- **Validation** (`422`): `started_at` required, `ended_at ≥ started_at`, exercise
  `position`s unique within the session and `set_index`es unique within an exercise
  (both ≥ 0), every `exercise_id` built-in or the user's (archived is fine).
  **Clamped**, not rejected: `reps` 0–100, `weight_kg` 0–500, `assist_kg` 0–200, `rpe` 1–10,
  `duration_sec` 0–21 600, `distance_m` 0–100 000, `bodyweight_kg` 30–300. `assist_kg` on a
  set of an exercise whose `load_type` is not `bodyweight`, or above 0 together with a
  `weight_kg` above 0, is `422` (a set has an added load or a support, not both).
- `origin` and `import_source` of the body are ignored (the server owns them, like
  `muscular_trimp`): a session saved here is `origin` `app`, and a session that was
  imported keeps `import` when it is saved again.
- `201` when the session was created, `200` otherwise, with the stored `Session`.
- Rate limit: its own budget of 2 requests per second (bursts of 60), like sync.

`Session`:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | |
| `title` | string | Up to 120 characters. |
| `started_at` | timestamp | The session's day is the local day of `started_at`. |
| `ended_at` | timestamp? | Set on completion at the latest. |
| `status` | string | `in_progress`, `completed`, `discarded`. |
| `plan_day_id` | uuid? | The plan day it was started from. |
| `workout_id` | uuid? | The workout the load is attached to (see `…/complete`); `null` until completed. |
| `notes` | string | Up to 2 000 characters. |
| `origin` | string | `app` (logged in RYFT, the default) or `import` (created by `POST /v1/train/import`). Closed enum. Read-only. A client decodes a missing value as `app` (servers before 2026-10-26). |
| `import_source` | string? | The import's `source` (e.g. `alpha_progression`) when `origin` is `import`, else `null`. Open string: show unknown values generically. Read-only. |
| `bodyweight_kg` | number? | For `bodyweight` exercises; without it the last known value (else 75 kg) is used. |
| `muscular_trimp` | number? | Muscular load (TRIMP-equivalent), one decimal; `null` until completed. |
| `strain` | number? | 0–21, the muscular load alone on the strain scale; `null` until completed. |
| `volume_kg` | number? | Σ reps × kg of working sets (bodyweight sets with body weight × factor + added load); `null` until completed. |
| `device_id` | uuid? | |
| `exercises` | `[SessionExercise]` | Ordered by `position`. |
| `prs` | `[PR]` | Records this session set; `[]` before completion. |
| `updated_at` | timestamp | Server time of the last change. |
| `SessionExercise.id`, `exercise_id` | uuid | |
| `SessionExercise.plan_exercise_id` | uuid? | The plan exercise it fulfils, for progression; optional. |
| `SessionExercise.position` | int | |
| `SessionExercise.notes` | string | |
| `SessionExercise.sets` | `[Set]` | Ordered by `set_index`. |

`Set`:

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | |
| `set_index` | int | 0-based. |
| `reps` | int? | |
| `weight_kg` | number? | For `bodyweight` exercises: added load (a belt, a vest); `null` and `0` mean the same there ([T3](#t3)). |
| `assist_kg` | number? | `bodyweight` exercises only: the support that makes the movement lighter (an assisted pull-up machine, a band), in kg, 0–200. `null` = none. Instead of a negative `weight_kg` ([T4](#t4)). The support comes off the moved weight: the set counts `max(0, body weight × bodyweight_factor + weight_kg − assist_kg)` for volume and muscular load, holds **no** personal record and gives no e1RM chart point (model 2026.10.2, `SCORING.md` → Assisted sets). `null` and `0` are a plain bodyweight set. |
| `rpe` | number? | 1–10, halves allowed. |
| `duration_sec` | int? | For `time` exercises. |
| `distance_m` | number? | For `distance` exercises. |
| `is_warmup` | bool | Warm-up sets count a quarter of their load and never set records. |
| `completed_at` | timestamp? | `null` = planned, not done; only completed sets count. A live session keeps this meaning; an import sends only sets that were done and refuses one with `null` ([T2](#t2)). |
| `updated_at` | timestamp | Set by the app on every change (its clock); decides conflicts. |

#### `POST /v1/sessions/{id}/complete` — finish a session

Body (optional fields): `{ "bodyweight_kg": 80, "ended_at": "2026-10-18T18:01:00Z" }`.
`ended_at` defaults to the stored `ended_at`, then the last set's `completed_at`, then
now — send it when completing from the offline queue. Answers `200` with the `Session`,
now with `muscular_trimp`, `strain`, `volume_kg`, `prs` and `workout_id`; repeating it
recomputes and returns the same. `404` for an unknown session, `409` for a discarded one.

The load is attached to one workout: a HealthKit workout of activity `strength`,
`functional_strength` or `crossfit` that overlaps at least half of the session, or else a
workout the platform creates (`source` `ryft-session`, activity `strength`, the session's
start and end) so the Strain screen lists it. If a matching HealthKit workout syncs
later, the load moves to it, the created workout disappears and `workout_id` changes.
The day's strain includes the load within seconds (it is rescored in the background).

#### `GET /v1/sessions?from=&to=&status=` · `GET /v1/sessions/{id}` · `DELETE /v1/sessions/{id}`

The list returns `SessionSummary` for sessions started in `[from, to]` (same semantics
as `/v1/days`), newest first, optionally only one `status` (`422` on another value):

```json
[ { "id": "a7f2…", "title": "Upper A", "started_at": "…", "ended_at": "…", "status": "completed",
    "plan_day_id": "…", "workout_id": "…", "duration_min": 59, "exercise_count": 5, "set_count": 19,
    "volume_kg": 7420, "muscular_trimp": 55.3, "strain": 10.6, "pr_count": 2,
    "origin": "app", "import_source": null },
  { "id": "3d8c…", "title": "Push", "started_at": "2026-06-08T17:02:00Z", "ended_at": "2026-06-08T18:01:00Z",
    "status": "completed", "plan_day_id": null, "workout_id": "9e41…", "duration_min": 59, "exercise_count": 2,
    "set_count": 5, "volume_kg": 2310, "muscular_trimp": 31.2, "strain": 6.4, "pr_count": 0,
    "origin": "import", "import_source": "alpha_progression" } ]
```

`SessionSummary`: the `Session` fields `id`, `title`, `started_at`, `ended_at`, `status`,
`plan_day_id`, `workout_id`, `volume_kg`, `muscular_trimp`, `strain`, plus
`duration_min` (int?, `null` while in progress), `exercise_count`, `set_count`
(completed working sets), `pr_count`, and `origin` / `import_source` as on `Session`.

`GET /v1/sessions/{id}` returns the `Session` (`404` if unknown). `DELETE` removes it with
its sets: `204`; a completed session's day is rescored.

#### `POST /v1/train/import` — import plans and history from another training app

The app parses an export of another training app (a CSV file) on the device and sends
structured plans and finished sessions here, in batches. The file itself never leaves the
phone. The ids of plans, sessions, their exercises and sets are chosen by the app and are
**derived from the file's content and the account** (UUIDv5 under a namespace that is itself
UUIDv5(the app's import namespace, the account's id as in `GET /v1/me` → `id`, lowercase)), so
sending the same workout again is the same id, and the same file imported into another account
gives other ids. Plan ids are global (session ids are per account): an id derived without the
account would clash with another account's import of the same file (`id_taken`). The call is
**idempotent by id**, and the answer says per item what happened. It is
**not all-or-nothing**: an item that is rejected does not undo the others.

Auth as every `/v1/*` route, plus: the **beta invite gate** (`403 invite_required`) and the
**consent gate** (`403 consent_required`), because the sessions are stored for scoring
(health data, GDPR Art. 9). The invite gate in `warn` mode serves the request with
`X-RYFT-Gate` (Errors); the consent gate does **not**: on this route `warn` acts as `on`,
because every app build that calls it has the consent screen, so an import without consent is
always `403 consent_required`. Then the feature flag `train.import` (`GET /v1/config`): off for
the account, `403 import_not_enabled`. All of it is checked before anything in the body is read.

Body up to **1 MiB** (`413`), `Content-Type: application/json`. Request `TrainImportRequest`:

```json
{ "source": "alpha_progression",
  "plans": [
    { "id": "6a1f3c7e-1b52-5d0a-9c34-7e2b8d4f1a09", "name": "Upper / Lower", "goal": "general", "weeks": null,
      "days_per_week": 2, "description": "", "archived": false, "progression": null,
      "days": [ { "id": "0c5e9b2a-3d71-5f48-a6b0-91c4e7d2f385", "week": null, "day_index": 0, "name": "Lower",
                  "exercises": [ { "id": "f27d4a18-6c3b-5e90-8b15-d0a3c9e47b62",
                                   "exercise_id": "053b5627-084e-5417-a033-a6602d812f86", "position": 0,
                                   "sets": 3, "reps_min": 5, "reps_max": 8, "target_kind": "kg", "target_value": 100,
                                   "rest_sec": 120, "superset_group": null, "notes": "" } ] } ] } ],
  "sessions": [
    { "id": "3d8c5a21-7e4f-5b96-a0d3-c18f2e6b9047", "title": "Lower", "started_at": "2026-06-08T17:02:00Z",
      "ended_at": "2026-06-08T18:01:00Z", "status": "completed",
      "plan_day_id": "0c5e9b2a-3d71-5f48-a6b0-91c4e7d2f385", "notes": "", "bodyweight_kg": 82, "device_id": null,
      "exercises": [
        { "id": "a4c81e07-93b2-5d6f-8e10-5b7c2f9d3a46", "exercise_id": "053b5627-084e-5417-a033-a6602d812f86",
          "plan_exercise_id": "f27d4a18-6c3b-5e90-8b15-d0a3c9e47b62", "position": 0, "notes": "",
          "sets": [
            { "id": "e90b3d52-1a7c-5f84-b6d9-02c4e8a17f35", "set_index": 0, "reps": 5, "weight_kg": 100, "assist_kg": null,
              "rpe": null, "duration_sec": null, "distance_m": null, "is_warmup": false,
              "completed_at": "2026-06-08T17:20:00Z", "updated_at": "2026-06-08T17:20:00Z" },
            { "id": "17f5a6c9-d3e2-5b08-9a41-c6e0b8d27f93", "set_index": 1, "reps": 5, "weight_kg": 100, "assist_kg": null,
              "rpe": null, "duration_sec": null, "distance_m": null, "is_warmup": false,
              "completed_at": "2026-06-08T17:28:00Z", "updated_at": "2026-06-08T17:28:00Z" } ] },
        { "id": "5e2d9c80-4b6a-5173-a8f2-d1b3e7c05a69", "exercise_id": "9b0e4c52-6d1a-4f0e-8a77-2c5d9e13b8f4",
          "plan_exercise_id": null, "position": 1, "notes": "",
          "sets": [
            { "id": "c3b7e194-5a08-5d2f-b1c6-8f4a0d9e2b57", "set_index": 0, "reps": 6, "weight_kg": null, "assist_kg": 20,
              "rpe": null, "duration_sec": null, "distance_m": null, "is_warmup": false,
              "completed_at": "2026-06-08T17:50:00Z", "updated_at": "2026-06-08T17:50:00Z" } ] } ] } ] }
```

| Field | Type | Notes |
| --- | --- | --- |
| `source` | string | Required. Which app the file came from. **Request enum, closed:** `alpha_progression`. Another value is `422`. New sources arrive additively (changelog), and an app only sends one it knows. The server stores it on every session as `import_source`. |
| `plans` | `[Plan]` | Optional (default `[]`), at most 5. The `Plan` shape of `PUT /v1/plans/{id}`, the same rules (1–364 days, `target_kind`s, an `exercise_id` that is built-in or the user's); `slug`, `is_builtin` and `PlanExercise.exercise` are ignored. App-chosen ids. Saved like `PUT /v1/plans/{id}`: the plan is replaced whole when the id exists. |
| `sessions` | `[Session]` | Optional (default `[]`), at most 50. The `Session` shape of `PUT /v1/sessions/{id}` with the rules below. App-chosen ids. |

Whole-request errors (nothing is processed): `400` malformed JSON or an unknown field —
**including `origin` and `import_source` on a session**: the endpoint decides the origin,
the request never carries one ([T1](#t1)); `403` a gate; `413` over 1 MiB; `422` an unknown
or missing `source`, more than 5 plans or 50 sessions, the same plan id or session id twice in
the request, a string with NUL (Errors → NUL: strip U+0000 from titles and notes taken from a file); `429` with `code` `import_in_progress` or the
rate limit; `503` with `Retry-After` when the database or the queue is down or the request ran
out of its 50 s (what was stored stays stored, see **Retries**). A request with no plans and no
sessions is `200` with `results: []`.

**Rules for a session.** An item that breaks one is `rejected` (below), the rest go on.

- `status` must be `completed`, `started_at` and `ended_at` are required with
  `ended_at ≥ started_at`, and `started_at` lies between 2000-01-01 and 24 h from now. The
  session's day is the local day of `started_at` in the user's timezone.
- A session lasts at most 24 h (`ended_at − started_at`), and `ended_at` is at most 24 h from
  now. Every set's `completed_at` lies between an hour before `started_at` and an hour after
  `ended_at`, and its `updated_at` is at most 24 h from now. At most 300 sets in a session.
- Every set of an imported session was done: `completed_at` is required on each, and a `null`
  is `invalid` ([T2](#t2)). Sets the user skipped are not sent. `set_index` is unique per
  exercise, positions unique per session; ids unique as for `PUT`.
- Limits and clamping as `PUT /v1/sessions/{id}`: ≤ 50 exercises, ≤ 50 sets per exercise,
  `reps` 0–100, `weight_kg` 0–500, `assist_kg` 0–200, `rpe` 1–10, `bodyweight_kg` 30–300 (clamped,
  not rejected). `assist_kg` only on a `bodyweight` exercise and not together with a
  `weight_kg` above 0 ([T4](#t4)).
- `bodyweight_kg` is the user's body weight, `weight_kg` of a `bodyweight` set the **added**
  load, and `null` and `0` mean the same there ([T3](#t3)).
- Every `exercise_id` is built-in or one of the user's own (archived is fine). The app creates
  custom exercises first, with `POST /v1/exercises` one at a time.
- `plan_day_id` and `plan_exercise_id` may point at a built-in plan, at a plan of the user, or
  at a plan of **this request** ([T5](#t5)).
- A set without `updated_at` takes its `completed_at` (not the time of the request), so a
  resend is identical.
- Ignored on input, as for `PUT`: `workout_id`, `muscular_trimp`, `strain`, `volume_kg`, `prs`
  and the session's `updated_at`. The set's `updated_at` is content (below).

Response `200 TrainImportResponse`, whenever the request was understood, even when every item is rejected:

```json
{ "results": [
    { "kind": "plan", "id": "6a1f3c7e-1b52-5d0a-9c34-7e2b8d4f1a09", "status": "created" },
    { "kind": "session", "id": "3d8c5a21-7e4f-5b96-a0d3-c18f2e6b9047", "status": "created" },
    { "kind": "session", "id": "b84e1d63-0a9c-5f27-9e51-3c7a6d2f8b10", "status": "unchanged" },
    { "kind": "session", "id": "71c9e3a5-2f8d-5604-b3a7-e95d1c4802fb", "status": "updated" },
    { "kind": "session", "id": "d2a60f94-8b17-5c3e-a145-6f9e0b7d3c28", "status": "rejected",
      "code": "unknown_exercise", "message": "exercise 4f6a1b30-2c9d-5e87-b0a3-7d15c8e92f64 is neither built-in nor yours" } ],
  "days_touched": ["2026-06-08", "2026-06-10"],
  "scoring": { "queued_days": ["2026-06-08", "2026-06-10"], "expected_within_sec": 40 } }
```

| Field | Type | Notes |
| --- | --- | --- |
| `results` | `[ImportResult]` | **One entry per submitted item**: the plans in request order, then the sessions in request order (not the order they were processed in). |
| `ImportResult.kind` | string | `plan` or `session`. `exercise` is reserved (a later `exercises` array); this version never sends it, and a client decodes unknown kinds leniently. |
| `ImportResult.id` | uuid | The item's id from the request. |
| `ImportResult.status` | string | `created` (new), `unchanged` (stored, identical content: nothing written), `updated` (stored, different content: replaced), `rejected` (not stored; the stored copy, if any, is as before). Closed enum. |
| `ImportResult.code` | string? | Only with `rejected`. Open string with the values below; treat an unknown one as a generic rejection. |
| `ImportResult.message` | string? | Only with `rejected`. English text for logs and support. Never holds a title, a note or another value from the file, only ids and field names. |
| `days_touched` | `[day]` | The local days queued for rescoring, oldest first, as in `SyncResponse` (see **Rescoring** below). `[]` when nothing changed. |
| `scoring` | `SyncScoring` | `queued_days` equals `days_touched`; `expected_within_sec` is when to look at `GET /v1/me/sync` again (or wait for the silent refresh push): the sync estimate plus 2 s per queued day, at most 300; never below the value `POST /v1/sync` would give. |

`rejected` `code`s:

| `code` | Item | Meaning |
| --- | --- | --- |
| `invalid` | plan, session | A value fails validation (the rules of `PUT`, and the import rules above). `message` names the field. Not retryable without a change. |
| `unknown_exercise` | plan, session | An `exercise_id` is neither built-in nor the user's. |
| `unknown_plan_reference` | session | `plan_day_id` or `plan_exercise_id` is not a built-in plan's, the user's, or one stored by this request. A session whose plan was `rejected` in the same request lands here; resend it without the references ([T5](#t5)). |
| `id_taken` | plan, session | Plan (the `message` is the same whoever holds the id): the id (or a day or exercise id in it) belongs to a built-in plan, another account's plan, or another of the user's plans. Session: the id, or one of its exercise or set ids, belongs to a session that is not this import's (logged in the app, or another of the user's sessions). |
| `newer_on_server` | plan, session | The stored copy was changed in the app after the import last wrote it, or made there: a session saved (`PUT`, any field: a title or a note too) or completed in the app, a set with a later `updated_at` than the request's, a discarded session; a plan saved with `PUT /v1/plans/{id}` (edited or archived), or one the app made under that id. Any save of an imported session or plan counts as an edit, even one back to the imported content. The import left it as it is, and a resend never overwrites it. |

**Idempotency and "identical"** ([T10](#t10)). An id that is not stored is `created`. For a stored
one the server compares the stored item with the request's **after the same clamping and
defaults**, over everything the request determines, and nothing the server computes:

- *Session:* `title`, `started_at`, `ended_at`, `notes`, `bodyweight_kg`, `device_id`, `plan_day_id`,
  `import_source`; its exercises by id (`exercise_id`, `plan_exercise_id`, `position`, `notes`);
  their sets by id (`set_index`, `reps`, `weight_kg`, `assist_kg`, `rpe`, `duration_sec`,
  `distance_m`, `is_warmup`, `completed_at`, `updated_at`). `weight_kg` and `assist_kg` `null`
  equal `0`. Timestamps are compared as instants to the microsecond, so `Z` and `+00:00` and
  fractional zeros do not matter, and so does the order of the arrays. Not compared:
  `muscular_trimp`, `strain`, `volume_kg`, `workout_id`, `prs`, the server's `updated_at`,
  `origin`.
- *Plan:* every field of the `Plan` document except `slug`, `is_builtin` and the resolved
  `exercise`; days and exercises by id; `progression: null` equals `{"type":"none"}`.

Equal is `unchanged`: nothing is written, nothing is rescored. Different is `updated`: the
stored item is **replaced whole**, as a completed session or a plan is by `PUT` (exercises, sets,
days that the request leaves out are deleted). Only an item **as the import left it** can be
replaced: the id of a session logged in the app is `id_taken`, and an imported session or plan
that the app changed after the import last wrote it, in any field, is left alone
(`newer_on_server`); so is a plan the app made itself under that id. The server keeps when
the import last wrote each item and compares it with the item's last change.

**What an import does**

- Sessions are stored as `completed` at their original times, `origin` `import`, `import_source`
  the request's `source`. They feed history (`GET /v1/sessions`, exercise history and the e1RM
  chart), personal records and `last_time` / `target_kg` in `GET /v1/plans/today`, by `started_at`.
- **Records** are recomputed once per affected exercise after the batch. They are recorded
  **silently**: no `PR` push and no `pr` insight card for a record an imported session holds,
  however recent; everywhere else (exercise history, `Session.prs`, `pr_count`) they are records
  like any other. An assisted set holds none ([T4](#t4)).
- **No `SESSION_SCORED` and no `PR` push**, for any imported session, however recent. The one
  notification an import may cause is the silent refresh push once its days are scored.
- **No enrolment moves**, even for a session with the active plan's `plan_day_id`
  ([T6](#t6)). Enrolling is `POST /v1/plans/{id}/enroll`, which can now take a position.
- **Rescoring** ([T7](#t7)): one event for the whole request, after the last item, not one per
  session. `muscular_trimp`, `strain`, `volume_kg` and `workout_id` of an imported session are
  `null` until scoring has run, as `pending_days` in `GET /v1/me/sync` shows; the scored days then
  carry the sessions' strain (the Strain screen lists each as a workout with source `ryft-session`).
  `days_touched` holds: the local day of every session that was `created` or `updated`; the days of
  `unchanged` sessions that an earlier, interrupted attempt queued and scoring has not finished;
  and the days of the user's other completed sessions whose load the import can move: those that
  start after the oldest imported session, share an exercise with an imported one and start
  **within 90 days after the newest imported session ends** (a set's load is measured against the
  best e1RM of the 90 days before its session), and those without their own `bodyweight_kg` that
  hold a `bodyweight` exercise and start after the oldest imported session with a body weight (they
  take the last body weight logged before them). An updated session counts with the version it
  replaced. Only days in the **400 days ending today** are queued (like a sync's window): an older
  session is stored and counts for history and records, but it is not scored, so its
  `muscular_trimp`, `strain`, `volume_kg` and `workout_id` stay `null`.
- Deleting works as for any session (`DELETE /v1/sessions/{id}`, and the day is rescored);
  deleting the account removes everything. Sending a deleted session again creates it again.

**Order within a request** ([T5](#t5)). Plans are stored first, in request order, each in its own
transaction; so a session of the same request can name one of them. Then the sessions, **oldest
`started_at` first** (ties by id) whatever their order in the array, so that a record or an e1RM
is measured against the history before it. The `results` keep the request's order.

**Transactions.** Every item is its own unit: an item is stored completely or not at all, and a
failed item never undoes another. The finishing steps (queueing the days with the rescoring
event, then the records) run once after the last item, also when the client has gone.

**Retries.** The request is safe to repeat as it is. After a network failure, `429`, `503` or
`408` send the same request again (after `Retry-After`): items already stored come back
`unchanged`, and the days of an interrupted attempt that scoring has not finished are queued in the
answer. After `413` halve the number of sessions. A `200` with `rejected` items is final for those
items until the app changes them. A resend never undoes what the user changed in the app since
the import: such items come back `newer_on_server`, as they are.

**Rate limit and concurrency.** The request costs one token of the import's own budget (bursts of
5, then one every 5 seconds) however many items it holds. At most
one import per user runs at a time, across all devices and servers: a second one gets
`429`, `code` `import_in_progress`, `Retry-After: 10`, and none of its items is processed. The
server gives an import 50 s; if it runs out, the answer is `503 "request timed out"` with
`Retry-After`, and the items that were done stay stored.

**Not logged:** bodies, titles, notes and values are never written to the server log or a
metric label; only counts per status and the request id are.

**For the app.** Derive every id with the account (above): `uuid5(uuid5(importNamespace,
userID.lowercased), content)`, never from the file alone. Leave `origin` and `import_source` out
of the request. Create custom exercises with `POST /v1/exercises` first. Strip U+0000 from titles
and notes. Keep sets within an hour of their session and sessions within a day. On `429`
`import_in_progress` wait `Retry-After` and resend unchanged; on `413`, and on the `422` "at most
2500 sets per request", split the batch (halve it) and send the halves; on `403`
`import_not_enabled` hide the import. Treat `rejected` + `newer_on_server` as "already there,
changed by the user since": nothing to do. **After two accounts are merged** (an operator's
merge, `GET /v1/me` → `account_hint`), do not import the same file again: its ids derive from
the account's id, so the kept account would get the merged-away account's imported history a
second time, under other ids.

**Export and deletion.** `origin`, `import_source` and `assist_kg` are part of `GET /v1/sessions`
and `GET /v1/sessions/{id}`, so an export built from them includes them; account deletion
(`DELETE /v1/me`) removes imported sessions, plans and records with the rest of the account.

<a id="training-import-answers"></a>
#### Training import: answers to T1–T13

(The anchor `training-import-answers` is unchanged; the heading was "answers to T1–T12" until T13 was added.)

The app's questions from `docs/API_REQUESTS.md` → "Training import (open, 2026-10-23)", answered
against the platform as it is. **Changed** marks an answer that differs from the app's proposal.

| # | Answer |
| --- | --- |
| <a id="t1"></a>**T1** | Yes. The endpoint sets `origin: import` and `import_source` = `source` on every session; a per-session `origin` or `import_source` in the request is `400` (unknown field). `Session` and `SessionSummary` gain `origin` (`app` \| `import`, closed, default `app`) and `import_source` (string?, open). `PUT /v1/sessions/{id}` ignores both on input, so an app that echoes a `Session` back is not refused there, and cannot turn an imported session into an app one. **The app must leave both out of its import request** (encode them as absent). |
| <a id="t2"></a>**T2** | Yes, no `skipped`. A set that is sent was done. In a live session `completed_at: null` still means "planned, not done" and such a set counts for nothing; in an import it is refused (`invalid`), so a skipped set is never stored by accident. |
| <a id="t3"></a>**T3** | Convention confirmed: `load_type: bodyweight` → `weight_kg` is the added load and `Session.bodyweight_kg` the body weight. `null` and `0` mean the same for a bodyweight set: the effective weight, the records and the e1RM chart already treat them alike. **Changed:** not normalised on write. The value is stored as sent (live sets send `null`, and a read must show what was written), and the T10 comparison treats `null` and `0` as equal for `weight_kg` and `assist_kg`. |
| <a id="t4"></a>**T4** | `assist_kg` (number? on `Set`, 0–200, bodyweight sets only, not with a `weight_kg` above 0) instead of a negative `weight_kg`, which stays clamped to 0–500. The moved weight is body weight × factor + added load − `assist_kg`, floored at 0 (model 2026.10.2, `SCORING.md` → Assisted sets): the support lowers the set's volume and load, and an assisted set holds no record and gives no e1RM chart point; `null` and `0` are a plain bodyweight set. The same field is accepted by `PUT /v1/sessions/{id}`. |
| <a id="t5"></a>**T5** | Plans first, then sessions (oldest first); `plan_day_id` / `plan_exercise_id` may point into the request. They are validated. **Changed:** an unknown reference is not `422` for the request but a `rejected` item with `code` `unknown_plan_reference`, because the other items are still good. A session whose plan was rejected needs resending without the references. **Ids per account:** plan ids are global, so the app derives every id from the content under a namespace of the account (UUIDv5(import namespace, user id)); the same file in two accounts gives two plans. |
| <a id="t6"></a>**T6** | Confirmed. Enrolment moves only when a session is completed with `POST /v1/sessions/{id}/complete`, which the import never uses. No `SESSION_SCORED` or `PR` push for `origin: import`: the rescoring event carries no session, and notify refuses imported sessions as well. |
| <a id="t7"></a>**T7** | Yes. `days_touched` and `scoring.expected_within_sec` as in `SyncResponse` (estimate grows 2 s per queued day, max 300). The days stay in `GET /v1/me/sync` → `pending_days` until scored, and the silent refresh push follows once (`day` = the newest of `days_touched`; at most one per 5 minutes per account, so keep polling as the fallback). `days_touched` may also hold days of the user's other sessions whose load depends on the imported history. |
| <a id="t8"></a>**T8** | Yes: `POST /v1/plans/{id}/enroll` takes an optional `EnrollPost` `{started_at?, current_week?, current_day_index?}` (defaults: now, 1, 0). **Changed:** with a field set, an enrolment of that plan that is already active or paused is moved in place (`200`) instead of returned or resumed as it was. Ranges and `422`s: under the endpoint. |
| <a id="t9"></a>**T9** | In **Conventions**: body 1 MiB (`413`), ≤ 5 plans and ≤ 50 sessions (`422`), 50 s, one token per request from the import's own budget (bursts of 5, then one every 5 s). A second import while one runs: `429` + `Retry-After: 10` + `code` `import_in_progress` (new in the closed `code` list); nothing of that request was processed. |
| <a id="t10"></a>**T10** | Server fields are left out (`updated_at` of the session, scoring, `workout_id`, `prs`); **everything else the request sets is compared**, including each set's `updated_at` and `completed_at`, which the app derives deterministically, and `import_source`. `null` = `0` for `weight_kg` and `assist_kg`. Equal → `unchanged`. A re-sent import never overwrites a later edit, of a session (any field) or a plan (edited or archived): `newer_on_server`; any save of an imported item in the app counts as an edit, even one back to the imported content. Details: under the endpoint. |
| <a id="t11"></a>**T11** | The documentation was wrong, not the builder: the server stores a `bodyweight` plan target's `target_value` as sent and has never used it. Now documented: `null`, or the added load in kg (0–500). **Changed:** 0–500 is validated (`422` / `invalid`; it was unchecked). `PlanTarget.target_kg` stays `null` for `bodyweight`. The import may send `null`. |
| <a id="t12"></a>**T12** | Yes, today and now documented: `POST /v1/exercises` with an existing custom exercise's id replaces all of its fields, `archived` included, so it is `false` afterwards (`200`). `PATCH` with `archived: false` does the same without resending the rest. |
| <a id="t13"></a>**T13** | Yes, both already hold; no field changed. `results` has **one entry per submitted item whatever its status** (`created`, `unchanged`, `updated` or `rejected`; plans in request order, then sessions in request order; see `results` under the endpoint), so the app can match every item to a result by position and `id`. `X-Request-ID` is on every response, `2xx` included (Conventions → Request IDs), so log the header. |

**Decided since the answers** (model 2026.10.2, `docs/SCORING.md`; no shape changed):

1. *Scoring of assisted sets*: the support comes off the moved weight, `max(0, body weight ×
   factor + added − assist_kg)`, for volume and muscular load; an assisted set holds no record and
   gives no e1RM chart point ([T4](#t4)). Stored numbers moved with the model version
   (`GET /v1/model`, rescored after the deploy).
2. *Imported records*: they stay silent, the newest one too: no `pr` insight card and no push for
   a record an imported session holds; they are records everywhere else.

### Journal — behaviors and their impact

The morning journal ("How was yesterday?"): yes/no toggles, and a count or a time for a
few, about what the user did **on a day**. Once a behavior has enough days with and
without it, RYFT shows how the **morning after** differs: recovery, HRV and sleep
performance (`SCORING.md` → Journal). Built-in behaviors have fixed ids; custom ones get
their id from the app, like custom exercises. `name` follows `Accept-Language` as in
Train; `name_en` and `name_de` are always both present.

**Closed enums:** behavior `kind` (`yes_no`, `count`, `time`), `category`
(`substances`, `sleep`, `recovery`, `supplements`, `health`, `lifestyle`, `custom`),
impact `status` (`locked`, `unlocked`).

#### `GET /v1/behaviors` — the catalog with the user's choice

Every built-in behavior and the user's own (not deleted), by `category`, then `sort`.

```json
[ { "id": "bad67883-7fb1-5f98-9761-feba0a813536", "slug": "alcohol", "name": "Alcohol",
    "name_en": "Alcohol", "name_de": "Alkohol", "category": "substances", "kind": "yes_no",
    "default_on": true, "enabled": true, "is_custom": false, "sort": 10 },
  { "id": "89a9dd86-4767-5eed-9718-24370f52078b", "slug": "last_caffeine", "name": "Last caffeine",
    "name_en": "Last caffeine", "name_de": "Letztes Koffein", "category": "substances", "kind": "time",
    "default_on": false, "enabled": false, "is_custom": false, "sort": 40 } ]
```

| Field | Type | Notes |
| --- | --- | --- |
| `id` | uuid | Fixed for built-ins. |
| `slug` | string | Unique per owner; derived from the name for custom behaviors. |
| `name`, `name_en`, `name_de` | string | See Language above. |
| `category` | string | Closed enum above; custom behaviors are `custom` unless given one. |
| `kind` | string | `yes_no`; `count` (how many, e.g. drinks); `time` (a local `HH:MM`, e.g. the last coffee). |
| `default_on` | bool | On for users who never changed it. |
| `enabled` | bool | On the user's journal sheet. |
| `is_custom` | bool | The user's own. |
| `sort` | int | Order within the category. |

#### `PATCH /v1/behaviors` — choose behaviors

`{"enabled": {"<behavior id>": true, "<behavior id>": false}}` → `200` with the whole
list as `GET` returns it. `422` for an id that is not a built-in or the user's own.

#### `POST /v1/behaviors` · `DELETE /v1/behaviors/{id}` — custom behaviors

```json
{ "id": "3f1c…", "name": "Late screen meeting", "name_de": "Spätes Online-Meeting", "kind": "yes_no" }
```

Required: `id` (from the app), `name` (1–60 characters), `kind`. Optional: `name_de`
(default `name`), `category` (default `custom`). `201` with the `Behavior`, enabled;
repeating it with the same `id` updates it and answers `200`. `409` when the `id`
belongs to a built-in or another user's behavior; `422` on invalid values or past 50
custom behaviors. `DELETE` removes a custom behavior from the catalog (its past entries
stay, without effect on the impact list): `204`, idempotent; `403` for a built-in.

#### `GET /v1/journal/{day}` · `PUT /v1/journal/{day}` — one day's journal

`{day}` is the day the behaviors happened (the morning sheet asks about yesterday).

```json
{ "day": "2026-10-18", "answered": true, "answered_at": "2026-10-19T06:41:12Z", "editable": true,
  "entries": [
    { "behavior_id": "bad67883-7fb1-5f98-9761-feba0a813536", "value_bool": true, "value_num": null, "value_time": null },
    { "behavior_id": "8cf20bff-9124-5fba-95e5-fd2083b90318", "value_bool": true, "value_num": 2, "value_time": null },
    { "behavior_id": "89a9dd86-4767-5eed-9718-24370f52078b", "value_bool": true, "value_num": null, "value_time": "16:30" },
    { "behavior_id": "ce48b6cf-43e3-5c6b-baf9-0a13e5d767d9", "value_bool": false, "value_num": null, "value_time": null } ] }
```

| Field | Type | Notes |
| --- | --- | --- |
| `day` | string | `YYYY-MM-DD`, local. |
| `answered` | bool | The day was saved at least once. A day never saved is unknown, not "no". The Today card asks until yesterday is answered. |
| `answered_at` | timestamp? | Last save; `null` when not answered. |
| `editable` | bool | Within the last 14 days and not in the future. |
| `entries` | `[JournalEntry]` | `[]` when not answered. |

`JournalEntry`: `behavior_id`; `value_bool` (bool, always: did it happen); `value_num`
(int?, `count` behaviors: how many, ≥ 1 when `value_bool`); `value_time` (string?,
`time` behaviors: local `HH:MM` when `value_bool`). Other kinds leave both `null`.

`PUT` takes `{"entries": [JournalEntry, …]}` and **replaces** the day: send every
behavior the sheet showed, the ones left off as `value_bool: false`. That is what makes
them a "no"; a behavior not sent that day counts as unknown for it. Returns the
`JournalDay`. `422` when the day is not `editable`, for a behavior that is not
the user's, a duplicate, or a value that does not fit the kind; `400` for unknown fields.

#### `GET /v1/behaviors/impact` — how behaviors change the morning after

Computed on request from the last 90 days: every entry day D with a scored morning on
D+1. A behavior is `unlocked` with at least 5 days with it and 5 without (`needed` says
how many more). Each effect is the difference **yes − no** of the mean, with a 90 %
bootstrap interval; `significant` when the interval excludes 0.

```json
{ "window_days": 90, "computed_at": "2026-10-19T06:45:03Z",
  "impacts": [
    { "behavior_id": "bad67883-7fb1-5f98-9761-feba0a813536", "slug": "alcohol", "name": "Alcohol", "kind": "yes_no",
      "status": "unlocked", "n_yes": 12, "n_no": 31, "needed": { "yes": 0, "no": 0 },
      "recovery":          { "delta": -14.2, "ci_lo": -19.8, "ci_hi": -8.1, "significant": true,  "n_yes": 12, "n_no": 31 },
      "hrv_pct":           { "delta": -11.5, "ci_lo": -17.0, "ci_hi": -5.9, "significant": true,  "n_yes": 12, "n_no": 30 },
      "sleep_performance": { "delta": -3.0,  "ci_lo": -8.2,  "ci_hi": 2.4,  "significant": false, "n_yes": 12, "n_no": 31 } },
    { "behavior_id": "ce48b6cf-43e3-5c6b-baf9-0a13e5d767d9", "slug": "sauna", "name": "Sauna", "kind": "yes_no",
      "status": "locked", "n_yes": 2, "n_no": 20, "needed": { "yes": 3, "no": 0 },
      "recovery": null, "hrv_pct": null, "sleep_performance": null } ] }
```

| Field | Type | Notes |
| --- | --- | --- |
| `impacts` | `[BehaviorImpact]` | Every enabled behavior and every one with entries in the window: `unlocked` first, by the size of the recovery effect; then `locked`, fewest days needed first. |
| `n_yes`, `n_no` | int | Entry days with and without it that have a scored morning after (recovery). |
| `needed` | `{yes, no}` | More days needed to unlock; `0, 0` when unlocked. |
| `recovery` | `BehaviorEffect?` | Recovery points (0–100) the morning after: yes − no. `null` while locked. |
| `hrv_pct` | `BehaviorEffect?` | HRV, in % of the mean on "no" mornings. `null` while locked or with fewer than 5 + 5 nights with HRV. |
| `sleep_performance` | `BehaviorEffect?` | Sleep performance points of the night after. `null` like `hrv_pct`. |

`BehaviorEffect`: `delta`, `ci_lo`, `ci_hi` (one decimal), `significant`, and the `n_yes` /
`n_no` it rests on. A yes is `value_bool` for every kind (a `count` of 0 or a `time`
behavior that did not happen is a no). Show `−14 recovery` as points, not a percentage
of recovery; the CI as a thin range bar.

### Health

`GET /healthz` → `200 {"status":"ok"}` while the process runs.
`GET /readyz` → `200 {"status":"ready"}`, or `503 {"error":"…"}` when Postgres (and,
for scoring/notify, the event broker) is unreachable. ingest stays ready without the
broker: a sync's events wait in its outbox. `GET /metrics` → Prometheus text format
(see `OBSERVABILITY.md`). All three are unauthenticated and not exposed by the Ingress.

## Swift mirror

Codable types for the iOS app, matching the shapes above with the app's decoder
(`.convertFromSnakeCase`, RFC 3339 dates with or without fractional seconds). They
extend `Models.swift`: `DailyScore`, `DayDetail`, `DayStrain`, `Workout`, `User`,
`UserPatch` and the sleep types replace the existing structs (new fields only), the rest
are new. The sync request types are not mirrored here; on the app's `Sample` and `SleepSegment` add `id: UUID?`
(the HealthKit object UUID) and on `SyncRequest` `deleted: [DeletedRecord]?` (below). The response types `IgnoredCounts` and `SyncResponse` gain the fields below. A logged set is `SessionSet` in Swift (`Set` is taken). `scripts/check_swift.sh` compiles this block and decodes every
endpoint of a running stack with it.

```swift
import Foundation

enum ReadinessState: String, Codable { case balanced, strained, depleted, calibrating }
enum CalibratingReason: String, Codable { case shortHistory = "short_history", noHrv = "no_hrv", noRhr = "no_rhr" }
enum SleepQualityLabel: String, Codable { case good, fair, poor }
enum SleepStageName: String, Codable { case deep, light, rem, awake, asleep }
enum InsightSeverity: String, Codable { case warn, notice, info }
enum ScoreStatus: String, Codable { case provisional, final }   // closed in v1

struct ScoreCoverage: Codable {   // DailyScore.coverage; may gain fields, ignore unknown ones
    let sleepHrMinutes: Int
    let hrvReadings: Int
    let sleepSource: String?
    let hrSourceHours: [String: Int]   // keys are source names, open
    let nightComplete: Bool
    let status: ScoreStatus
}

struct DailyScore: Codable, Identifiable {
    let day: String
    let readiness: Int?
    let state: ReadinessState
    let recovery: Int?
    let strain: Double?
    let steps: Int?
    let activeKcal: Int?
    let strengthMin: Int?
    let volumeKg: Double?
    let sleepPerformance: Int?
    let sleepNeedMin: Int?
    let sleepActualMin: Int?
    let sleepDebtMin: Int?
    let sleepNeedBaseMin: Int?
    let sleepNeedStrainMin: Int?
    let sleepNeedDebtMin: Int?
    let sleepNeedNapMin: Int?
    let napMin: Int?
    let sleepQuality: Int?
    let sleepConsistency: Int?
    let hrvMs: Double?
    let rhr: Double?
    let respRate: Double?
    let spo2: Double?
    let hrvBaselineMs: Double?
    let rhrBaseline: Double?
    let baselineDays: Int
    let baselineHrvDays: Int?
    let baselineRhrDays: Int?
    let calibrating: Bool
    let calibratingReason: CalibratingReason?
    let computedAt: Date
    let modelVersion: String?
    let scoringState: ScoringState
    let coverage: ScoreCoverage?      // null: never scored, or scored before 2026-10-31 (read as final)
    var id: String { day }
}

struct SleepStage: Codable {
    let stage: SleepStageName
    let min: Int
    let pct: Int
}

struct SleepInterval: Codable {
    let stage: SleepStageName
    let start: Date
    let end: Date
}

struct SleepNeed: Codable {
    let base: Int
    let strain: Int
    let debt: Int
    let napCredit: Int
    let total: Int
}

struct SleepDetail: Codable {
    let start: Date
    let end: Date
    let totalMin: Int
    let inBedMin: Int
    let efficiency: Int
    let stages: [SleepStage]
    let quality: Int
    let qualityLabel: SleepQualityLabel
    let restorative: Int
    let hasStages: Bool
    let need: SleepNeed
    let segments: [SleepInterval]
}

struct DayStrain: Codable {
    let trimp: Int
    let hrMin: Int
    let zoneMin: [Int]
    let steps: Int?
    let activeKcal: Int?
    let muscularTrimp: Int
    let sessions: [SessionSummary]
}

struct Workout: Codable, Identifiable {
    let id: UUID
    let activity: String
    let start: Date
    let end: Date
    let avgHr: Double?
    let maxHr: Double?
    let energyKcal: Double?
    let distanceM: Double?
    let source: String
    var strain: Double?
    let muscularLoad: Double?
    let sessionId: UUID?
    let modelVersion: String?
    let alsoRecordedBy: [String]?     // omitted when none
}

struct DayDetail: Codable {
    let score: DailyScore
    let sleep: SleepDetail?
    let strain: DayStrain?
    let workouts: [Workout]
}

struct SleepDay: Codable, Identifiable {
    let day: String
    let start: Date
    let end: Date
    let totalMin: Int
    let inBedMin: Int
    let efficiency: Int
    let stages: [SleepStage]
    let quality: Int
    let qualityLabel: SleepQualityLabel
    let restorative: Int
    let hasStages: Bool
    let consistency: Int?
    let napMin: Int?
    var id: String { day }
}

struct SleepBedtime: Codable {
    let day: String
    let bed: Date
    let wake: Date
}

struct SleepRange: Codable {
    let range: String
    let from: String
    let to: String
    let days: [SleepDay]
    let nights: Int
    let avgTotalMin: Int?
    let avgInBedMin: Int?
    let avgEfficiency: Int?
    let avgQuality: Int?
    let avgStages: [SleepStage]
    let avgConsistency: Int?
    let bedtimes: [SleepBedtime]
}

struct SleepPlanBasis: Codable {
    let base: Int
    let strainTopup: Int
    let debtTopup: Int
    let napCredit: Int
    let efficiency: Int
}

struct SleepPlan: Codable {   // GET /v1/sleep/plan?wake=HH:MM
    let wake: Date
    let needMin: Int
    let inBedMin: Int
    let efficiency: Int
    let bedtime100: Date
    let bedtime85: Date
    let bedtime70: Date
    let basis: SleepPlanBasis
}

struct HRSample: Codable {
    let t: Date
    let bpm: Int
}

struct WorkoutDetail: Codable, Identifiable {
    let id: UUID
    let activity: String
    let start: Date
    let end: Date
    let avgHr: Double?
    let maxHr: Double?
    let energyKcal: Double?
    let distanceM: Double?
    let source: String
    let strain: Double?
    let hr: [HRSample]
    let zoneMin: [Int]?
    let trimp: Int?
}

struct TrendPoint: Codable, Identifiable {
    let day: String
    let value: Double
    var id: String { day }
}

struct Trend: Codable {
    let metric: String
    let range: String
    let from: String
    let to: String
    let points: [TrendPoint]
    let avg: Double?
    let min: Double?
    let max: Double?
    let baseline: Double?
}

struct Insight: Codable, Identifiable {
    let id: String
    let severity: InsightSeverity
    let title: String
    let body: String
    let metric: String
    let day: String
}

enum SleepBaseSource: String, Codable { case `default`, learned, manual }

struct User: Codable {
    let id: UUID
    let name: String
    let email: String?
    let provider: String?
    let tz: String
    let maxHr: Int
    let sleepBaseMin: Int
    let sleepBaseSource: SleepBaseSource
    let sleepBaseLearnedMin: Int?
    let sleepBaseLearnedAt: Date?
    let notifications: NotificationPrefs
    let betaAccess: Bool
    let consent: Consent?
    let birthYear: Int?
    let maxHrSource: MaxHRSource
    let sources: [UserSource]
    let accountHint: AccountHint?
}

// ---- Beta: consent, invite, profile, sources, model, feedback ---------------------

enum MaxHRSource: String, Codable { case `default`, estimated, manual }
enum SourceKind: String, Codable { case energy, hr, hrv, resp, sleep, spo2, steps, workout }

struct Consent: Codable {
    let policyVersion: String
    let grantedAt: Date
}

struct UserSource: Codable {
    let name: String
    let bundleId: String?
    let kinds: [SourceKind]
    let lastSeen: Date
    let samples7D: [String: Int]?     // wire key samples_7d: .convertFromSnakeCase yields samples7D (capital D). Sample.type → count; dictionary keys are not converted
}

struct ConsentPost: Encodable {   // POST /v1/me/consent
    let policyVersion: String
    var appVersion: String? = nil
}

struct InvitePost: Encodable {   // POST /v1/me/invite
    let code: String
}

struct ModelChange: Codable {
    let version: String
    let date: String
    let summary: String
    let summaryEn: String
    let summaryDe: String
    let affects: [String]
}

// ---- Accounts and sync state -------------------------------------------------------

enum ScoringState: String, Codable { case scored, pending, waitingForSleep = "waiting_for_sleep", stale }

struct AccountHint: Codable {   // GET /v1/me → account_hint
    let kind: String            // "other_account_on_device"
    let otherProvider: String?
    let otherLastSync: Date
}

struct TodayState: Codable {
    let day: String
    let state: ScoringState
    let scoredAt: Date?
    let modelVersion: String
}

struct SyncStatus: Codable {    // GET /v1/me/sync
    let gate: APIErrorCode?
    let lastSyncAt: Date?
    let lastSyncDevice: String?
    let latest: [String: Date]  // keys: SourceKind raw values
    let today: TodayState
    let pendingDays: [String]
    let expectedWithinSec: Int
    let resyncRequestedAt: Date?
}

struct SyncCounts: Codable { let samples: Int; let sleep: Int; let workouts: Int }
struct IgnoredCounts: Codable { let duplicate: Int; let outsideWindow: Int; let unknownType: Int; let unknownId: Int? }
struct SyncIgnored: Codable { let samples: IgnoredCounts; let sleep: IgnoredCounts; let workouts: IgnoredCounts }
struct SyncScoring: Codable { let queuedDays: [String]; let expectedWithinSec: Int }
enum DeletionKind: String, Codable { case sample, sleep, workout }
struct DeletedRecord: Codable { let kind: DeletionKind; let id: UUID }   // SyncRequest.deleted, ≤ 2000
struct SyncDeleted: Codable { let samples: Int; let sleep: Int; let workouts: Int }   // SyncResponse.deleted

struct SyncResponse: Codable {  // POST /v1/sync
    let samples: Int
    let sleep: Int
    let workouts: Int
    let daysTouched: [String]
    let accepted: SyncCounts
    let ignored: SyncIgnored
    let deleted: SyncDeleted?         // always present from 2026-10-31
    let scoring: SyncScoring
}

struct ModelInfo: Codable {   // GET /v1/model
    let current: String
    let changelog: [ModelChange]
}

enum FeedbackCategory: String, Codable { case bug, idea, data, other }

struct FeedbackPost: Encodable {   // POST /v1/feedback
    let message: String
    var category: FeedbackCategory? = nil
    var appVersion: String? = nil
    var build: String? = nil
    var deviceModel: String? = nil
    var osVersion: String? = nil
    var requestIds: [String] = []
    var diagnostics: [String: String]? = nil   // the server takes any JSON object up to 32 KiB
}

// ---- Journal ----------------------------------------------------------------------

enum BehaviorKind: String, Codable { case yesNo = "yes_no", count, time }
enum BehaviorCategory: String, Codable { case substances, sleep, recovery, supplements, health, lifestyle, custom }
enum ImpactStatus: String, Codable { case locked, unlocked }

struct Behavior: Codable, Identifiable {   // GET /v1/behaviors
    let id: UUID
    let slug: String
    let name: String
    let nameEn: String
    let nameDe: String
    let category: BehaviorCategory
    let kind: BehaviorKind
    let defaultOn: Bool
    let enabled: Bool
    let isCustom: Bool
    let sort: Int
}

struct BehaviorsPatch: Encodable {   // PATCH /v1/behaviors
    let enabled: [String: Bool]       // behavior id (lowercase) → on
}

struct BehaviorInput: Encodable {   // POST /v1/behaviors
    let id: UUID
    let name: String
    var nameDe: String? = nil
    let kind: BehaviorKind
    var category: BehaviorCategory? = nil
}

struct JournalEntry: Codable {
    let behaviorId: UUID
    let valueBool: Bool
    var valueNum: Int? = nil         // count behaviors
    var valueTime: String? = nil     // time behaviors, local "HH:MM"
}

struct JournalDay: Codable {   // GET/PUT /v1/journal/{day}
    let day: String
    let answered: Bool
    let answeredAt: Date?
    let editable: Bool
    let entries: [JournalEntry]
}

struct JournalPut: Encodable {   // PUT /v1/journal/{day}
    let entries: [JournalEntry]
}

struct BehaviorEffect: Codable {   // BehaviorImpact.recovery, .hrvPct, .sleepPerformance
    let delta: Double
    let ciLo: Double
    let ciHi: Double
    let significant: Bool
    let nYes: Int
    let nNo: Int
}

struct ImpactNeeded: Codable {
    let yes: Int
    let no: Int
}

struct BehaviorImpact: Codable {
    let behaviorId: UUID
    let slug: String
    let name: String
    let kind: BehaviorKind
    let status: ImpactStatus
    let nYes: Int
    let nNo: Int
    let needed: ImpactNeeded
    let recovery: BehaviorEffect?
    let hrvPct: BehaviorEffect?
    let sleepPerformance: BehaviorEffect?
}

struct BehaviorImpacts: Codable {   // GET /v1/behaviors/impact
    let windowDays: Int
    let computedAt: Date
    let impacts: [BehaviorImpact]
}

// ---- Remote config and push ----------------------------------------------------

struct RemoteConfig: Codable {   // GET /v1/config
    let flags: [String: Bool]     // open keys
    let evaluatedAt: Date
    let ttlSec: Int
    let privacyPolicyVersion: String
    let privacyPolicyUrl: String
}

enum NotificationCategory: String, Codable, CaseIterable {
    case morningScore = "MORNING_SCORE", sessionScored = "SESSION_SCORED", pr = "PR", syncStale = "SYNC_STALE"
}

struct QuietHours: Codable, Equatable {
    var from: String   // local "HH:MM"
    var to: String
}

struct NotificationPrefs: Codable {   // User.notifications, PATCH /v1/me/notifications
    let categories: [String: Bool]    // keyed by NotificationCategory.rawValue
    let quietHours: QuietHours?
}

struct NotificationPatch: Encodable {   // PATCH /v1/me/notifications
    var categories: [String: Bool]? = nil
    /// nil: leave unchanged; .some(nil): clear; .some(q): set.
    var quietHours: QuietHours?? = nil

    enum CodingKeys: String, CodingKey { case categories, quietHours = "quiet_hours" }
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeIfPresent(categories, forKey: .categories)
        if let q = quietHours { try c.encode(q, forKey: .quietHours) }   // .some(nil) encodes null
    }
}

enum PushEnvironment: String, Codable { case sandbox, production }

struct PushRegistration: Encodable {   // POST /v1/devices/{id}/push
    let token: String                   // hex
    let env: PushEnvironment
    let locale: String
}

struct RyftPushInfo: Decodable {   // userInfo["ryft"] of a notification
    let category: String?          // absent on the silent refresh
    let kind: String?              // "refresh" on the silent refresh
    let day: String
    let url: String?
}

struct UserPatch: Codable {   // PATCH /v1/me, every field optional
    var tz: String?
    var maxHr: Int?
    var maxHrManual: Bool?
    var birthYear: Int?
    var sleepBaseMin: Int?
    var sleepBaseManual: Bool?
}

struct AccountDeleted: Codable {   // DELETE /v1/me
    let status: String
    let authUserDeleted: Bool
    let appleTokenRevoked: Bool?
}

struct AppleAuthorization: Codable {   // POST /v1/me/apple-authorization
    let code: String
}

// ---- Train ----------------------------------------------------------------------

enum ExerciseKind: String, Codable { case barbell, dumbbell, kettlebell, bodyweight, machine, cable, cardio }
enum LoadType: String, Codable { case weight, bodyweight, time, distance }
enum Muscle: String, Codable {
    case chest, lats, traps, biceps, triceps, forearms, abs, obliques, glutes, quads, hamstrings, adductors, calves
    case upperBack = "upper_back", frontDelts = "front_delts", sideDelts = "side_delts", rearDelts = "rear_delts"
    case lowerBack = "lower_back"
}
enum PlanGoal: String, Codable { case strength, hypertrophy, weightlifting, general }
enum TargetKind: String, Codable { case pct1rm = "pct_1rm", rpe, kg, bodyweight }
enum ProgressionType: String, Codable { case linear, percent, none }
enum SessionStatus: String, Codable { case inProgress = "in_progress", completed, discarded }
enum SessionOrigin: String, Codable { case app, `import` }   // closed; absent (servers before 2026-10-26) = app
enum EnrollmentStatus: String, Codable { case active, paused, completed }
enum PRKind: String, Codable { case e1rm, maxWeight = "max_weight", maxReps = "max_reps", maxVolume = "max_volume" }

struct Exercise: Codable, Identifiable {
    let id: UUID
    let slug: String
    let name: String
    let nameEn: String
    let nameDe: String
    let kind: ExerciseKind
    let primaryMuscles: [Muscle]
    let secondaryMuscles: [Muscle]
    let equipment: [String]
    let loadType: LoadType
    let bodyweightFactor: Double
    let isUnilateral: Bool
    let isCustom: Bool
    let archived: Bool
}

struct ExerciseInput: Codable {   // POST /v1/exercises (PATCH: the same fields, all optional, plus archived)
    let id: UUID
    let name: String
    let nameDe: String?
    let kind: ExerciseKind
    let primaryMuscles: [Muscle]
    let secondaryMuscles: [Muscle]?
    let equipment: [String]?
    let loadType: LoadType
    let bodyweightFactor: Double?
    let isUnilateral: Bool?
}

struct PersonalRecord: Codable {
    let exerciseId: UUID
    let kind: PRKind
    let value: Double
    let setId: UUID
    let sessionId: UUID
    let achievedAt: Date
}

struct SessionSet: Codable, Identifiable {
    let id: UUID
    let setIndex: Int
    let reps: Int?
    let weightKg: Double?
    let assistKg: Double?     // bodyweight sets only, 0...200; absent from servers before 2026-10-26
    let rpe: Double?
    let durationSec: Int?
    let distanceM: Double?
    let isWarmup: Bool
    let completedAt: Date?
    let updatedAt: Date
}

struct E1RMPoint: Codable {
    let day: String
    let value: Double
}

struct ExerciseHistorySession: Codable {
    let sessionId: UUID
    let date: String
    let startedAt: Date
    let sets: [SessionSet]
}

struct ExerciseHistory: Codable {
    let exerciseId: UUID
    let prs: [PersonalRecord]
    let sessions: [ExerciseHistorySession]
    let e1rm: [E1RMPoint]
}

struct Progression: Codable {
    let type: ProgressionType
    let incrementKg: Double?
    let onFail: String?
    let deloadAfter: Int?
}

struct PlanExercise: Codable, Identifiable {
    let id: UUID
    let exerciseId: UUID
    let exercise: Exercise   // read-only, ignored on PUT
    let position: Int
    let sets: Int
    let repsMin: Int
    let repsMax: Int
    let targetKind: TargetKind
    let targetValue: Double?
    let restSec: Int
    let supersetGroup: Int?
    let notes: String
}

struct PlanDay: Codable, Identifiable {
    let id: UUID
    let week: Int?
    let dayIndex: Int
    let name: String
    let exercises: [PlanExercise]
}

struct Plan: Codable, Identifiable {
    let id: UUID
    let slug: String?
    let name: String
    let goal: PlanGoal
    let weeks: Int?
    let daysPerWeek: Int
    let description: String
    let progression: Progression?
    let isBuiltin: Bool
    let archived: Bool
    let days: [PlanDay]
}

struct PlanSummary: Codable, Identifiable {
    let id: UUID
    let slug: String?
    let name: String
    let goal: PlanGoal
    let weeks: Int?
    let daysPerWeek: Int
    let description: String
    let isBuiltin: Bool
    let archived: Bool
    let dayCount: Int
    let exerciseCount: Int
}

struct Enrollment: Codable, Identifiable {
    let id: UUID
    let planId: UUID
    let startedAt: Date
    let currentWeek: Int
    let currentDayIndex: Int
    let status: EnrollmentStatus
}

struct PlansOverview: Codable {
    let builtin: [PlanSummary]
    let mine: [PlanSummary]
    let active: Enrollment?
}

struct PlanTarget: Codable {
    let planExerciseId: UUID
    let exerciseId: UUID
    let sets: Int
    let repsMin: Int
    let repsMax: Int
    let targetKind: TargetKind
    let targetKg: Double?
    let restSec: Int
    let lastTime: [SessionSet]
}

struct PlanToday: Codable {   // GET /v1/plans/today; 204 = nothing planned
    let enrollment: Enrollment
    let day: PlanDay
    let targets: [PlanTarget]
}

struct SessionExercise: Codable, Identifiable {
    let id: UUID
    let exerciseId: UUID
    let planExerciseId: UUID?
    let position: Int
    let notes: String
    let sets: [SessionSet]
}

struct Session: Codable, Identifiable {   // PUT body and response; 409 body
    let id: UUID
    let title: String
    let startedAt: Date
    let endedAt: Date?
    let status: SessionStatus
    let planDayId: UUID?
    let workoutId: UUID?
    let notes: String
    let origin: SessionOrigin?        // read-only; nil = app. Encode as nil in TrainImportRequest (the server answers 400 otherwise)
    let importSource: String?         // read-only; open string
    let bodyweightKg: Double?
    let muscularTrimp: Double?
    let strain: Double?
    let volumeKg: Double?
    let deviceId: UUID?
    let exercises: [SessionExercise]
    let prs: [PersonalRecord]
    let updatedAt: Date
}

struct SessionSummary: Codable, Identifiable {
    let id: UUID
    let title: String
    let startedAt: Date
    let endedAt: Date?
    let status: SessionStatus
    let planDayId: UUID?
    let workoutId: UUID?
    let durationMin: Int?
    let exerciseCount: Int
    let setCount: Int
    let volumeKg: Double?
    let muscularTrimp: Double?
    let strain: Double?
    let prCount: Int
    let origin: SessionOrigin?
    let importSource: String?
}

struct SessionComplete: Codable {   // POST /v1/sessions/{id}/complete
    let bodyweightKg: Double?
    let endedAt: Date?
}

struct EnrollPost: Codable {   // POST /v1/plans/{id}/enroll; the body is optional, nil fields take their defaults
    var startedAt: Date? = nil
    var currentWeek: Int? = nil
    var currentDayIndex: Int? = nil
}

enum TrainImportSource: String, Codable { case alphaProgression = "alpha_progression" }   // request enum; extended additively

struct TrainImportRequest: Codable {   // POST /v1/train/import, body <= 1 MiB, <= 5 plans, <= 50 sessions
    let source: TrainImportSource
    let plans: [Plan]
    let sessions: [Session]   // origin and importSource must be nil here
}

enum ImportKind: String, Codable {
    case plan, session, exercise   // exercise: reserved, not sent yet
    case unknown                   // a kind this build does not know: decoded leniently
    init(from decoder: Decoder) throws {
        self = ImportKind(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .unknown
    }
}
enum ImportStatus: String, Codable { case created, unchanged, updated, rejected }

struct ImportResult: Codable {
    let kind: ImportKind
    let id: UUID
    let status: ImportStatus
    let code: String?      // rejected only: invalid, unknown_exercise, unknown_plan_reference, id_taken, newer_on_server (open)
    let message: String?
}

struct TrainImportResponse: Codable {
    let results: [ImportResult]       // one per submitted item: plans, then sessions, in request order
    let daysTouched: [String]
    let scoring: SyncScoring
}

enum APIErrorCode: String, Codable {
    case inviteRequired = "invite_required", consentRequired = "consent_required"
    case importInProgress = "import_in_progress"   // 429 on POST /v1/train/import; wait Retry-After
    case importNotEnabled = "import_not_enabled"   // 403 on POST /v1/train/import: the flag is off for the account
}

struct APIErrorBody: Codable {
    let error: String
    var code: APIErrorCode? = nil  // closed enum; branch on it
    let requestId: String?   // same as the X-Request-ID header
}
```

## Changelog

| Date | Change |
| --- | --- |
| 2026-10-31 | **Coverage and provisional scores** (answers the app's data-quality D2 and D4, `API_REQUESTS.md` → Sync and performance): `DailyScore.coverage` (`ScoreCoverage?`: `sleep_hr_minutes`, `hrv_readings`, `sleep_source`, `hr_source_hours`, `night_complete`, `status` `provisional` \| `final`, closed); `User.sources[].samples_7d` (sample counts per `Sample.type` over the last 7 days, all eight keys on every source). `details.hr_minutes` stays. **Behaviour:** a night is complete on wake evidence or at 10:00 local; a day is `provisional` until then and `final` after, sticky; scoring gains two floors (sleep heart rate < 120 min drops the resting-HR input of Recovery, which is renormalised, `rhr` itself stays; < 60 min of sleep from the winning source makes `sleep_performance` `null`), so stored numbers change for thin nights and the model version is bumped (`GET /v1/model`; rescore history to fill `coverage` on old rows); `MORNING_SCORE` now waits for the first `final` scoring of the day with recovery and sleep, the silent refresh still follows every scoring. Old clients ignore the new fields. Swift: `ScoreStatus`, `ScoreCoverage`, `DailyScore.coverage`, `UserSource.samples7D` (wire key `samples_7d`; the app's `.convertFromSnakeCase` maps it to `samples7D`, capital D). |
| 2026-10-31 | **Workout duplicates**: `Workout.also_recorded_by` (`[string]?`, omitted when none). **Behaviour:** workouts of one source with the same activity and start and end within 120 s of each other are folded to the most recently uploaded id (the older ids are removed, `404` afterwards, a Train session stays and is re-attached when the day is rescored); workouts of different sources with the same activity (or `other`) that overlap by at least 80 % of the shorter are listed once, as the primary with the other sources in `also_recorded_by`, in `GET /v1/days/{day}` and `GET /v1/workouts` (`GET /v1/workouts/{id}` still answers for the hidden one). Distances, counts and `muscular_load` attachment on affected days change when they are rescored. Swift: `Workout.alsoRecordedBy`. |
| 2026-10-31 | **Deletions and HealthKit ids** (answers the app's request, `API_REQUESTS.md` → Sync and performance §2): `Sample.id` and `SleepSegment.id` (uuid?, the HealthKit object UUID); `SyncRequest.deleted` (`[{kind: sample \| sleep \| workout, id}]`, ≤ 2 000, `422` above); `SyncResponse.deleted` (`samples`, `sleep`, `workouts`); `ignored.<kind>.unknown_id` (an id the server cannot delete for this user; a re-sent deletion). **Behaviour:** deletions apply after the same request's records; a record with an `id` adopts a stored row with the same natural key (and a sample takes the new value), so a source re-writing a value under a new UUID does not double it; a record whose `id` is already stored is `duplicate`; deleted days are queued for rescoring (in `days_touched`); `ryft-session` workouts are not deletable (`unknown_id`); deleting a workout that has a Train session keeps the session; a failed deletion is a `503` the app re-sends. Request bodies reject unknown fields (`400`), so a platform from before this entry would refuse `deleted` and `id`: ship the app change only after this entry is deployed (there is no flag). Swift: `DeletionKind`, `DeletedRecord`, `SyncDeleted`, `SyncResponse.deleted`, `IgnoredCounts.unknownId`, `Sample.id` / `SleepSegment.id` / `SyncRequest.deleted` on the app's own request types. |
| 2026-10-29 | **Minimum age 16**: `PATCH /v1/me` `birth_year`: the accepted range is 1920 to the current year − 16 (it was − 13); `422` otherwise. No field renamed or removed, no shape change. **Behaviour:** birth years stored before stay as they are, also those now outside the range (nothing re-checks or clears them; `max_hr` is unchanged); a client that sends such a value again gets `422`. Swift: none. |
| 2026-10-27 | **UUID case** (documentation only, behaviour unchanged): Conventions → UUIDs. The server accepts ids in upper or lower case (Swift's `JSONEncoder` writes upper case) and always writes lower case, so `results[].id` of `POST /v1/train/import` is lower case whatever was sent. |
| 2026-10-27 | **Training import, T13** (documentation only, no field, status or header changed): [Training import: answers to T1–T13](#training-import-answers) gains **T13**: `results` of `POST /v1/train/import` has one entry per submitted item whatever its status, and `X-Request-ID` is on `2xx` responses too. The anchor is unchanged. |
| 2026-10-26 | **Training import** (answers the app's T1–T12, [Training import: answers to T1–T12](#training-import-answers)): `POST /v1/train/import` (`source` `alpha_progression`; ≤ 5 plans and ≤ 50 sessions in a 1 MiB body; partial success, idempotent by id: per item `created` / `unchanged` / `updated` / `rejected` with a `code`; `days_touched` and `scoring` as in `SyncResponse`). `Session` and `SessionSummary` gain `origin` (`app` \| `import`, closed) and `import_source` (string?); `Set` gains `assist_kg` (number?, 0–200, bodyweight sets); `POST /v1/plans/{id}/enroll` takes an optional `EnrollPost` (`started_at`, `current_week`, `current_day_index`); error `code`s `import_in_progress` (`429`, `Retry-After: 10`) and `import_not_enabled` (`403`, the flag off for the account); feature flag `train.import` (off by default), checked by the server too. **Behaviour:** imported sessions are completed sessions at their original times and feed history, e1RM, records and `last_time`; they send no `SESSION_SCORED` or `PR` push, move no enrolment, and are rescored once per request (silent refresh push afterwards); the import needs the consent gate (`403 consent_required`; on this route `warn` acts as `on`); it has its own rate budget (bursts of 5, then one every 5 s); ≤ 2 500 sets per request, ≤ 300 per session, sessions of at most 24 h with sets within an hour of them; ids are derived per account; a resend never overwrites an item the app changed since the import (`newer_on_server`, sessions and plans); sessions older than 400 days are stored but not scored; model 2026.10.2 (`GET /v1/model`): `assist_kg` comes off a bodyweight set's moved weight (`max(0, body weight × factor + added − assist_kg)`) and an assisted set holds no record and gives no e1RM chart point, a set's load and a `pct_1rm` target use the best e1RM of the last 90 days (a `pct_1rm` target is `null` without a set in them), imported records give no insight card or push, and an import's dependent days end 90 days after its newest session and include later sessions without a body weight that hold a bodyweight exercise; a `bodyweight` plan target's `target_value` is validated 0–500 (it was unchecked); `POST /v1/exercises` with an archived custom exercise's id un-archives it (documented, unchanged); `PUT /v1/sessions/{id}` ignores `origin` and `import_source`. Old clients ignore every new response field. Swift: `SessionOrigin`, `Session.origin` / `importSource`, `SessionSummary.origin` / `importSource`, `SessionSet.assistKg`, `EnrollPost`, `TrainImportSource`, `TrainImportRequest`, `ImportKind`, `ImportStatus`, `ImportResult`, `TrainImportResponse`, `APIErrorCode.importInProgress` / `importNotEnabled`. |
| 2026-10-23 | Errors → NUL: a NUL (U+0000, `\u0000` in JSON) in a string of a request body, value or object key, is `422` with the error shape and no `code`; nothing is stored. It was a `500` where the text reached the database: `POST /v1/feedback` (`message`, the version and device fields, `request_ids`, `diagnostics`), Train names and notes (`PUT /v1/sessions/{id}`, `PUT /v1/plans/{id}`, `POST /v1/exercises`, `PATCH /v1/exercises/{id}`), `POST /v1/behaviors` (`name`, `name_de`) and `POST /v1/me/consent` (`app_version`); fields checked before (`tz`, notification categories, the push `token`, invite codes, `policy_version`, journal times) were and stay `422`. **`POST /v1/sync`** (was `500`) removes the NULs from its strings and accepts the page instead. `%00` or bytes that are not UTF-8 in the path or query are `400` (`GET /v1/exercises?q=` was `500`; an enum parameter such as `kind=%00` was `422`). A raw 0x00 byte in a body stays `400` (not JSON), a NUL in a header the HTTP server's plain-text `400`. Feedback `diagnostics` with bytes that are not UTF-8 or a lone surrogate is stored with U+FFFD in their place, and with a number beyond double precision is `422` (all three were `500`). **Behaviour:** a NUL in push `locale`, which was normalised away (`204`), is now `422`; `diagnostics` numbers are stored as double precision. No shape change; Swift: none. |
| 2026-10-22 | `DailyScore.calibrating_reason` (`short_history` \| `no_hrv` \| `no_rhr` \| null, closed enum), `baseline_hrv_days` and `baseline_rhr_days` (int?, the two N's behind `baseline_days = min(...)`) added. **Behaviour:** none for existing fields; the new ones are `null` on rows scored before they existed until rescored, and `calibrating_reason` is `null` exactly when `calibrating` is `false` (rule: `DailyScore` > `calibrating_reason`). Old clients ignore them. Swift: `DailyScore.baselineHrvDays` / `baselineRhrDays` / `calibratingReason`, `CalibratingReason`. |
| 2026-10-21 | `GET /readyz` of ingest no longer answers `503` while the event broker is unreachable (a sync's events wait in the outbox, so syncs keep being accepted). No shape change. |
| 2026-10-20 | **Accounts and sync state**: gates in `warn` mode with the `X-RYFT-Gate` response header (Errors); request ids are to be shown exactly as received; the `X-RYFT-Device-ID` request header and `User.account_hint`; `GET /v1/me/sync` (`SyncStatus`: `gate`, last sync, newest record per kind, today's scoring state, pending days, `expected_within_sec`, `resync_requested_at`); `DailyScore.scoring_state`; `SyncResponse.accepted` / `ignored` / `scoring`; the silent refresh push after every sync's scored days (Push). **Behaviour:** an unknown sample `type` or sleep `stage` is ignored and counted (`ignored.*.unknown_type`) instead of failing the sync with `422`; records more than 24 h in the future or older than 400 days are ignored (`outside_window`); identical re-sends no longer count in `samples` / `workouts` (`ignored.*.duplicate`). Swift: `ScoringState`, `AccountHint`, `TodayState`, `SyncStatus`, `SyncResponse` and its parts, `DailyScore.scoringState`, `User.accountHint`. |
| 2026-10-20 | **Beta** (external TestFlight): error `code` (`invite_required`, `consent_required`; `APIErrorBody.code`); consent to health-data processing (`POST`/`DELETE /v1/me/consent`, `User.consent`, `GET /v1/config` → `privacy_policy_version` / `privacy_policy_url`; `POST /v1/sync` needs it); invite codes (`POST /v1/me/invite`, `User.beta_access`); `User.birth_year` / `max_hr_source` and `PATCH /v1/me` `birth_year` / `max_hr_manual` (Tanaka estimate); `User.sources`; `SyncRequest.app_version` / `os_version` and `source_bundle_id` on samples, sleep segments and workouts; `model_version` on `DailyScore` and `Workout`, `GET /v1/model`; `POST /v1/feedback`; insights `no-heart-rate-source`, `max-hr-observed`. **Behaviour:** sync answers `403 consent_required` until the user consents, and nothing is scored without consent; with the beta gate on, accounts without a redeemed code get `403 invite_required`. Swift: `APIErrorCode`, `Consent`, `UserSource`, `SourceKind`, `MaxHRSource`, `ConsentPost`, `InvitePost`, `ModelInfo`, `ModelChange`, `FeedbackCategory`, `FeedbackPost`, the new fields. |
| 2026-10-19 | **Journal and behavior impact** (M5): `GET`/`PATCH`/`POST /v1/behaviors`, `DELETE /v1/behaviors/{id}` (25 built-ins, custom behaviors with app ids), `GET`/`PUT /v1/journal/{day}` (a saved day; unsent behaviors are unknown, not "no"), `GET /v1/behaviors/impact` (recovery, HRV % and sleep performance the morning after, yes − no with a 90 % bootstrap interval, unlocked at 5 + 5 days), insight `behavior-impact`. Swift: `BehaviorKind`, `BehaviorCategory`, `ImpactStatus`, `Behavior`, `BehaviorsPatch`, `BehaviorInput`, `JournalEntry`, `JournalDay`, `JournalPut`, `BehaviorEffect`, `ImpactNeeded`, `BehaviorImpact`, `BehaviorImpacts`. |
| 2026-10-19 | **Remote config and push** (M7 core): `GET /v1/config` (feature flags evaluated per user, `ttl_sec` 21600); `PATCH /v1/me/notifications` and `User.notifications`; `POST` / `DELETE /v1/devices/{id}/push`; the push notifications `MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE` and the silent refresh, with their localisation keys. Nothing is sent until the APNs key is configured. Swift: `RemoteConfig`, `NotificationCategory`, `QuietHours`, `NotificationPrefs`, `NotificationPatch`, `PushEnvironment`, `PushRegistration`, `RyftPushInfo`, `User.notifications`. |
| 2026-10-19 | **Sleep upgrades** (M4): `DailyScore.nap_min`, `sleep_quality`, `sleep_consistency` and the need breakdown (`sleep_need_base_min`, `_strain_min`, `_debt_min`, `_nap_min`); `DayDetail.sleep.need` and `restorative`; `SleepRange.avg_consistency`, `bedtimes`, `days[].consistency` / `nap_min`; `User.sleep_base_source` / `sleep_base_learned_min` / `sleep_base_learned_at` and `UserPatch.sleep_base_manual`; `GET /v1/sleep/plan`; trend metrics `sleep_quality`, `sleep_consistency`, `nap_min`; insights `sleep-consistency-low`, `nap-credit`, `baseline-learned`. **Behaviour, no shape change:** `quality` is now its own score instead of a copy of sleep performance, so quality values and labels change; a nap lowers the following night's need; the baseline is learned weekly unless set by hand. Rescore history to fill past days. Swift: the new fields and types, `UserPatch` joins the mirror. |
| 2026-10-19 | **Train** (M3b): exercise library (`/v1/exercises`, history and records), plans with an enrolment (`/v1/plans`, `/v1/plans/today`), and logged sessions (`PUT /v1/sessions/{id}` with merge and a `409` that carries the server's copy, `…/complete`, list, detail, delete). `DailyScore.strength_min` / `volume_kg`, `Workout.muscular_load` / `session_id`, `DayStrain.muscular_trimp` / `sessions`, trend metrics `strength_min` and `volume_kg`, insight `pr`, status `403`. **Behaviour:** `strain` now includes the muscular load of completed sessions, so a day (or workout) with a logged session but no heart rate has a strain. Swift: the Train types, `Workout` joins the mirror. |
| 2026-10-18 | A per-IP limit at the edge (20 requests/s, bursts of 50) answers `429` with a plain-text body before the per-user limit applies. No shape change. |
| 2026-10-18 | `POST /v1/me/apple-authorization` added (the Sign in with Apple authorization code, so deletion can revoke the Apple grant). `DELETE /v1/me` responses add `apple_token_revoked` (nullable). Swift: `AccountDeleted.appleTokenRevoked`, `AppleAuthorization`. |
| 2026-10-18 | `DailyScore.steps` / `active_kcal` and `DayStrain.steps` / `active_kcal` added (nullable, daily totals after source de-duplication), with trend metrics `steps` and `active_kcal`. Rescore history to fill past days. |
| 2026-10-18 | Scoring: overlapping HealthKit sources are no longer mixed or summed (see `SCORING.md`). Heart rate is taken from one source per hour, sleep from one source per night. Values change for users with two devices: strain, sleep totals and stages, and the workout curve, which now shows only the winning source. No shape change. |
| 2026-10-18 | `DELETE /v1/me` added (account deletion: all data, then the Supabase auth user). Afterwards that user's tokens get `401` everywhere except a repeated `DELETE /v1/me`. Swift: `AccountDeleted`. |
| 2026-10-18 | **Request IDs and hardening**: every response carries `X-Request-ID`, and error bodies add `request_id` (`APIErrorBody.requestId`). `500` bodies no longer include internal error text. Request bodies other than `POST /v1/sync` are limited to 64 KiB, and an oversized body is `413` (was `400`). Per-user rate limits (`429` + `Retry-After`) and request timeouts (`503 "request timed out"` + `Retry-After`) were added, plus no-cache and security headers on every response. |
| 2026-10-18 | **Auth: Supabase access tokens** replace the shared API key; every response is scoped to the token's user, created on first request. `401` now strictly means "refresh / sign in again", `503` (with `Retry-After`) "retry". `User.email` and `User.provider` added. `POST /v1/recompute` answers `429` within a minute of the previous request. |
| 2026-10-18 | Production host `https://api.ryftapp.de` documented (both services, HTTPS only). No API change. |
| 2026-10-18 | Fixes: a workout running past midnight is scored on its whole window (was: only the part before midnight); `DayDetail.strain` is non-null whenever `score.strain` is; `PATCH /v1/me` returns `500`, not `422`, when the database fails; the 400-day recompute limit counts calendar days, so ranges across DST are judged the same by api and scoring. |
| 2026-10-18 | `DailyScore.baseline_days` added; `details` keys read by existing clients frozen. |
| 2026-10-18 | Errors: unknown routes and methods return a JSON `404` (was plain text); `GET /v1/days` and `/v1/workouts` with `from > to` return `422` (was `200 []`); `POST /v1/recompute` over 400 days returns `422` (was `202`, then failed in scoring). |
| 2026-10-18 | `GET /v1/insights` added. |
| 2026-10-18 | `GET /v1/trends` and `DailyScore.hrv_baseline_ms` / `rhr_baseline` added. |
| 2026-10-18 | `GET /v1/workouts/{id}` (heart-rate curve, zone minutes, TRIMP) and `DayDetail.strain` added. |
| 2026-10-18 | `DayDetail.sleep` (typed night with stages and hypnogram) and `GET /v1/sleep` added. |
| 2026-10-18 | `DailyScore.readiness` and `DailyScore.state` added. |
| 2026-10-18 | Initial v1: sync, me, days, day detail, workouts, recompute. |
