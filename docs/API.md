# RYFT HTTP API — v1

<!-- Tests read this file: every json code block needs `<!-- type: model.X -->` on the line before the fence (or an allow-list entry), see backend/internal/model/apidoc_examples_test.go; known doc/code drift is listed in docDrift there. The Errors tables are checked against the code by backend/internal/model/apidoc_errors_test.go: the `code` values and their statuses, the statuses the services answer with (each needs a row), and that every row has a Do line (non-empty only: the wording is reviewed by people). The messages Errors quotes verbatim are checked by backend/internal/httpx/apidoc_messages_test.go and backend/internal/gate/apidoc_messages_test.go: change a quoted message in code and in this file together. `make check-api-doc` runs these after an edit of this file (go test does not notice it, the file is outside the module). -->

This file is the contract between the platform and the RYFT iOS app. The app's
wire types (`../app/Ryft/Networking/Models.swift`) mirror the shapes below and
`backend/internal/model/*.go` 1:1.

**Compatibility rule.** v1 only changes additively: new endpoints, new optional
query parameters, new response fields. A field is never renamed, removed or
retyped without moving to `/v2`. Clients must ignore response fields they do not
know. Response enums are **closed** in v1 — `state`, `quality_label`, sleep `stage`,
`severity`, `calibrating_reason` will not gain values without `/v2`, so they can be decoded as Swift enums;
so are the operator routes' `actor.kind` (`key` or `user`, Admin routes → The actor) and `BetaInvite.status`
(`open`, `used`, `expired`, `revoked`, Admin routes → Invites for a person).
`Insight.id`, `Insight.metric` and `Workout.activity` are **open** strings.

## Screens → endpoints

| Screen | Numbers | Endpoint |
| --- | --- | --- |
| Home — the sync button (D297, D300) | syncing, up to date, stale, error; a long press: each source's last sync (Apple Health, Garmin, Suunto) | `GET /v1/me/sync` → `last_sync_at`, `sources`, `freshness`, `scores_pending_since` (**The sync button** there), `POST /v1/sync` → `scoring` |
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
| Profile — Data sources; tracker setup | which apps and devices send what, and how much of it in the last 7 days (a source without HRV shows `hrv_sdnn: 0`), and whether it arrives through Apple Health or a direct connection | `User.sources` (`kinds`, `samples_7d`, `origin`) |
| Devices & sync — Connect (a direct connection; D257, not served yet) | which providers can be connected and whether there is room, the data card, the provider's sign-in sheet, connected / reconnect / removed at the provider, when data last arrived, Disconnect (and what it deletes) | `GET /v1/me/connections`, `POST /v1/me/connections/{provider}/start`, the redirect `ryft://connect/{provider}?status=…` from `GET /v1/connect/{provider}/callback`, `POST /v1/me/connections/{provider}/confirm`, `DELETE /v1/me/connections/{provider}` (**Connections** below) |
| Onboarding — tracker setup: the first setup card for a tracker with a direct connection | offer the connection first when the provider is in `available`, else the Apple Health setup as today | `GET /v1/me/connections` → `available`, then the Connect flow above; `GET /v1/config` → `flags["connect.<provider>"]` |
| Profile — Report a problem | message, diagnostics, request IDs | `POST /v1/feedback` |
| (any score) | which model computed it, and what changed | `model_version`, `GET /v1/model` |
| Profile — Notifications | per-category toggles, quiet hours | `User.notifications`, `PATCH /v1/me/notifications` |
| (launch, then every 6 h) | feature flags | `GET /v1/config` |
| (after the first sync, on every token change; sign-out) | push registration | `POST` / `DELETE /v1/devices/{id}/push` |
| Notifications | morning score, scored session, record, no new data; silent refresh | **Push notifications** below |
| (after Sign in with Apple) | let deletion revoke the Apple grant | `POST /v1/me/apple-authorization` |
| (after sign-in, then about hourly; D245, D247: `501` until the server has its token key) | App Attest: one key per account and install, an app token for every request | `POST /v1/app-attest/challenge`, `POST /v1/app-attest/keys`, `POST /v1/app-attest/token`; the headers `X-RYFT-App-Token` and `X-RYFT-App-Build`; `GET /v1/config` → `app_attest` (Conventions → **App Attest**) |
| Strain — Train | start a workout, today's plan, live session, summary with load and PRs | `GET /v1/plans/today`, `PUT /v1/sessions/{id}`, `POST /v1/sessions/{id}/complete` |
| Strain — Train: plans | list, detail, plan builder, enrol / pause | `GET /v1/plans`, `GET`/`PUT`/`DELETE /v1/plans/{id}`, `POST /v1/plans/{id}/enroll`, `DELETE /v1/plans/enrollment` |
| Journal (Today card, morning push) | yesterday's behaviors as toggles, a count or a time; edit 14 days back | `GET`/`PUT /v1/journal/{day}` |
| Insights — Behaviors | "Alcohol · −14 recovery (n = 12/31)" with its interval; locked ones with the days still needed | `GET /v1/behaviors/impact` |
| Profile — Journal | choose behaviors, add or remove custom ones | `GET`/`PATCH`/`POST /v1/behaviors`, `DELETE /v1/behaviors/{id}` |
| Today — morning check-in (no watch at night; D291, not served yet) | whether to ask, the form pre-filled with bed and wake times, the recovery estimate with its source and confidence (it fills the Today ring, marked as estimated, when there is no wearable night); history; delete | `GET /v1/recovery/status` (or `CoachingBriefing.recovery`), `PUT`/`GET`/`DELETE /v1/recovery/checkins/{date}`, `GET /v1/recovery/checkins` |
| Strain — workout without heart rate: "How hard was it?" | session RPE 1–10 and its load | `PUT`/`DELETE /v1/workouts/{id}/rpe`, `Workout.session_rpe`, `session_rpe_load` |
| Strain — Train: library, history | exercise search, custom exercises, e1RM chart and PRs, past sessions | `GET`/`POST /v1/exercises`, `PATCH`/`DELETE /v1/exercises/{id}`, `GET /v1/exercises/{id}/history`, `GET /v1/sessions` |
| Strain — Train: import | plans and workout history from another training app's export; label imported sessions; start the imported plan where the user is | `POST /v1/train/import`, `Session.origin` / `import_source`, `POST /v1/plans/{id}/enroll` (optional position) |
| (background) | HealthKit upload, and the deletions HealthKit reports | `POST /v1/sync` (`deleted`, `Sample.id`) |
| Operator web page `https://ryftlab.de/admin` (not an app screen; Supabase sign-in with TOTP) | invites for a person (create, copy and share the link, list, revoke, who redeemed), users (beta access), feedback, flags (read-only), audit (read-only, last 7 days) | **Admin routes** below: `POST`/`GET /v1/admin/invites`, `DELETE /v1/admin/invites/{code}`, `GET /v1/admin/users`, `POST`/`DELETE /v1/admin/users/{id}/access`, `GET /v1/admin/feedback`, `POST /v1/admin/feedback/{id}/resolve`, `GET /v1/admin/flags`, `GET /v1/admin/audit` |

## Conventions

| Topic | Rule |
| --- | --- |
| Base URLs | Two services on two hosts. **ingest** serves only `POST /v1/sync`; **api** serves everything else. Production: **`https://api.ryftlab.de`** (api) and **`https://ingest.ryftlab.de`** (ingest), HTTPS only, no path (every path below starts with `/v1`). One host per service because a Cloud Run domain mapping points one host at one service and cannot route by path (D217). Configure the app with **both** URLs; the ingest URL is required in production: left empty, the sync goes to the api host, which answers `404` to `POST /v1/sync` (unknown route), and the ingest host answers `404` to every other `/v1/*` route. Neither host is served until the GCP runbook has run (D217). Local development with `docker-compose.yml`: ingest on `http://localhost:8081`, api on `http://localhost:8082` (from a device, the development machine's address with the same ports), so the app needs both URLs there too. |
| Auth | Every `/v1/*` request carries `Authorization: Bearer <access token>`: the Supabase Auth access token of the signed-in user (a JWT; Sign in with Apple through Supabase). See **Authentication** below. `/health/live`, `/health/ready`, `/healthz`, `/readyz`, `/metrics` are unauthenticated. The one `/v1/` route without a bearer is `GET /v1/connect/{provider}/callback` (Connections, not served yet): a provider's sign-in page sends the browser there, never the app. |
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
| App token | Send `X-RYFT-App-Token: <token>` with every request to both hosts once the app has one (Conventions → **App Attest**, D245, D247). What the server does with it follows its mode (`GET /v1/config` → `app_attest`): under `off` the header is not read, under `warn` a request without a valid token is served and logged, under `on` it is refused with `403 app_attest_required`. The exempt routes (Conventions → App Attest → Exempt) ignore it in every mode. |
| App build | Send `X-RYFT-App-Build: <CFBundleVersion>` (e.g. `X-RYFT-App-Build: 20261119.091500`) with every request to both hosts (AA1, D247). Optional. The value is digits and dots, `^[0-9]+(\.[0-9]+){0,3}$`, at most 24 characters; a missing or malformed value is ignored and never refused. The server writes it only into its App Attest warn line (a request without a valid token, under `warn` and `on`), never into a metric label, and reads it nowhere else. |
| Response headers | Every response sends `Cache-Control: no-store` (health data is never cached), `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'`, `Referrer-Policy: no-referrer` and `Strict-Transport-Security`. |
| Rate limits | Per client address, before any token is checked: bursts of 50 requests, then 20 per second, on every route but the health paths (Errors → `429`: a plain-text answer). Per user: bursts of 60 requests, then 10 per second (`POST /v1/sync` and `PUT /v1/sessions/{id}`: 2 per second each, their own budgets). Normal app use never gets near it. Over the limit: `429` with `Retry-After` (whole seconds). `POST /v1/train/import` has its own budget, bursts of 5, then one every 5 seconds (one token however many items a request carries), and **one import per user runs at a time**: a second one while the first is running is `429` with `code` `import_in_progress` and `Retry-After: 10`. `POST /v1/me/connections/{provider}/start` has its own budget per account too (Connections, not served yet): bursts of 5, then one a minute. |
| Import limits | `POST /v1/train/import`: body ≤ 1 MiB (`413`), ≤ 5 plans and ≤ 50 sessions per request (`422`), ≤ 2 500 sets per request (`422`); each session ≤ 50 exercises, ≤ 50 sets per exercise and ≤ 300 sets in all (else that session is `rejected` `invalid`), each plan as `PUT /v1/plans/{id}`. |
| Timeouts | The server gives an API request 15 s, a sync and an import 50 s of database work; past that it answers `503 {"error":"request timed out"}` with `Retry-After`. Sync is idempotent, so retrying it is safe. |
| Units | Durations in whole minutes (`*_min`), heart rate in bpm, HRV in ms (SDNN; RMSSD on a day whose `coverage.hrv_metric` is `rmssd`, Connections), strain on the 0–21 scale, scores 0–100. Percentages (`pct`, `efficiency`, `quality`, scores) are whole numbers 0–100, **never** 0–1 fractions: `1` means 1 %. |

### Errors

Every error response has the same shape:

```json
{ "error": "human-readable message", "request_id": "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c" }
{ "error": "consent to the privacy policy 2026-10-26 is required before uploading health data", "code": "consent_required", "request_id": "01a1…" }
```

`code` (string?) is present when the client should do something specific; branch on it,
not on the message. Closed enum:

| `code` | Status | Do |
| --- | --- | --- |
| `invite_required` | `403` | The beta is invite-only and this account has no access yet: show the invite screen, `POST /v1/me/invite`. Every `/v1/*` route answers it except `GET /v1/me`, `GET /v1/me/sync`, `GET /v1/config`, `GET /v1/model`, `POST /v1/me/invite`, `POST`/`DELETE /v1/me/consent`, `POST /v1/feedback`, `DELETE /v1/me`, `DELETE /v1/me/connections/{provider}` (Connections, not served yet), `POST /v1/app-attest/challenge`, `POST /v1/app-attest/keys` and `POST /v1/app-attest/token`. |
| `consent_required` | `403` | `POST /v1/sync`, `POST /v1/train/import`, `POST /v1/me/connections/{provider}/start` or `/confirm`, and (not served yet) `PUT /v1/recovery/checkins/{date}` or `PUT /v1/workouts/{id}/rpe` without a consent to the current privacy policy (`GET /v1/config` → `privacy_policy_version`): show the consent screen, `POST /v1/me/consent`, then send again. Reads keep working. |
| `import_in_progress` | `429` | `POST /v1/train/import` while another import of this account is still running (this device's earlier request, or another device): wait `Retry-After` (10) seconds and send the same request again. It is safe: nothing of this request was processed. |
| `import_not_enabled` | `403` | `POST /v1/train/import` for an account the feature flag `train.import` is off for (`GET /v1/config` → `flags`): hide the import; it is not an error to retry. Nothing of the request was read. |
| `mfa_required` | `403` | Only on `/v1/admin/*` (the operator's web page and scripts); the app never calls these routes and never receives it. The admin token has no second factor, or its TOTP is older than 12 hours: run a TOTP challenge (Supabase MFA) and send the request again with the new token (Admin routes → Who may call). |
| `app_attest_required` | `403` | Only under `APP_ATTEST=on` (`GET /v1/config` → `app_attest`), on every route the check covers (Conventions → App Attest → Exempt lists the others): the request had no valid app token. Get a new token (Conventions → App Attest, step 3; from step 1 when this account has no key on this install) and send the request again, once; refused again: stop and show `request_id`. **Not a gate and never "drop the edit":** a refused Train write or sync page stays queued, and no HealthKit anchor is committed for a refused page. |
| `app_attest_challenge_invalid` | `422` | `POST /v1/app-attest/keys` and `POST /v1/app-attest/token` only: the challenge is unknown, used up, expired or another account's. Get a new challenge (`POST /v1/app-attest/challenge`), then the same step again, once. |
| `app_attest_invalid` | `422` | `POST /v1/app-attest/keys` (the attestation fails a check; a development attestation included) and `POST /v1/app-attest/token` (the assertion fails a check, its counter included): drop the `key_id` and attest a new key, once; refused again: stop until the next launch. |
| `app_attest_key_unknown` | `404` | `POST /v1/app-attest/token` only: `key_id` is not one of the account's keys (never attested here, replaced, unused for 90 days, or deleted with the account's data). Drop the `key_id`, attest a new key (step 1), then get a token. |
| `unknown_provider` | `404` | `POST /v1/me/connections/{provider}/start` and `/confirm` only (Connections): the provider is not served here, or its flag `connect.<provider>` is off for the account (it does not say which). Hide that provider's Connect entry and refresh `GET /v1/me/connections`; don't retry. On `/confirm` the grant waiting for it is dropped. |
| `connection_limit` | `409` | `POST /v1/me/connections/{provider}/start` and `/confirm` only (Connections): the provider's user cap is full (other accounts only; the caller's own open states, grant and connection never count). Say the provider is full for now; no automatic retry; `available[].accepting` is `false` until there is room. |
| `connect_ticket_invalid` | `409` | `POST /v1/me/connections/{provider}/confirm` only (Connections): the ticket is unknown, another account's or another provider's (also an expired ticket already cleaned up), or a repeat of a confirm that worked once that connection has ended (moved to another RYFT account, replaced, removed at the provider or disconnected). Nothing changed. Don't resend it; read `GET /v1/me/connections`; not connected: start again. |
| `connect_expired` | `410` | `POST /v1/me/connections/{provider}/confirm` only (Connections): more than 10 minutes after the callback, a repeat of a confirm that worked included; the grant waiting for it is dropped. Read `GET /v1/me/connections`; not connected: start again. |
| `merge_has_connections` | `409` | Only on `/v1/admin/*` (`POST /v1/admin/users/{id}/merge-into/{to}`, also with `force=1`); the app never receives it. The account `{id}` still holds a connection: ask the user to disconnect first, then merge again. |

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

**App Attest (D245, D247).** Four `code`s come with App Attest (Conventions → **App Attest**):
`app_attest_required` (`403`, only under `APP_ATTEST=on`, on every route the check covers: get a new
app token and send the request again once), and on `/v1/app-attest/*` only
`app_attest_challenge_invalid` (`422`), `app_attest_invalid` (`422`) and `app_attest_key_unknown`
(`404`). The three routes answer only where the server has its token key; until then (production, in
mode `off` until the `warn` PR) they answer `501`, which the app treats like a `404` without a `code`,
so none of the four reaches the app. Under `off` and `warn` no route answers `app_attest_required`.
**`app_attest_required` never drops a queued write and never commits a HealthKit anchor:** the
`403` row's "drop the edit" is for a `403` without a `code` only. The app holds a refused Train write
or sync page as it does for a gate, renews the token once, and sends it again. A build that does not
know `app_attest_required` decodes it as no `code`, which reads as the `403` of a built-in: it never
signs out on it, but **today's builds drop a queued Train write** (`PUT /v1/sessions/{id}`,
`POST /v1/sessions/{id}/complete`) on such a `403`, so under `on` an old build loses its unsent
sessions (Conventions → App Attest → Old builds). `app_attest_required` is not a gate in the sense
above: it never appears in `X-RYFT-Gate` or `GET /v1/me/sync` → `gate`.

**Connections (D257).** Five `code`s come with direct connections (**Connections** below; served from the
platform PR `connect/foundation`), and the status `410`: `unknown_provider` (`404`: the provider is not served here or not enabled for
the account; hide its Connect entry), `connection_limit` (`409`: the provider's user cap is full; say so, no
automatic retry), `connect_ticket_invalid` (`409`: the confirm's ticket is unknown or another account's;
start again), `connect_expired` (`410`: the confirm came more than 10 minutes after the callback; start
again), and on `/v1/admin/*` only `merge_has_connections` (`409`; the app never receives it).
`consent_required` (`403`) is answered by `POST /v1/me/connections/{provider}/start` and `/confirm` too, and
on both `warn` acts as `on` (as for the import). They are in both tables above and below, and in the mirror's
`APIErrorCode`. A build that does not know them reads them as no `code`; it only meets them on the
Connections routes, which it does not call.

| Status | `code` | When | Do |
| --- | --- | --- | --- |
| `400` | — | Malformed JSON (a raw 0x00 byte in the body included), unknown body field (in `POST /v1/train/import` also `origin` and `import_source` on a session), malformed date / UUID in query or path, a path or query holding `%00` or bytes that are not UTF-8 | Fix the request: sent again unchanged it fails the same way. A client bug, not for the user to fix; log `request_id`. Do not retry. |
| `401` | — | The token is missing, malformed, expired, not signed by the project, or not a signed-in user's token, or the account was deleted (checked before routing, so any `/v1/*` path). `/v1/admin/*` has its own rules (Admin routes → Who may call): there a `401` also means a valid token that is not an operator's. | Refresh the session and send the request again once; a second `401` means sign in again (Authentication). A `503` is never a reason to sign out. |
| `403` | `invite_required`, `consent_required`, `import_not_enabled`, `app_attest_required`, `mfa_required`, or none | Changing a built-in exercise, plan or behavior; and the `code`s of the table above: `invite_required` (no beta access), `consent_required` (sync, import, a connection's start or confirm, a recovery check-in or a session RPE without consent to the current policy), `import_not_enabled` (the import is off for the account), `app_attest_required` (under `APP_ATTEST=on`, no valid app token); on `/v1/admin/*` only: `mfa_required` (no recent second factor), or no `code` for a route that needs the admin key | Without a `code` the target is a built-in: do not retry, drop the edit. With a `code`, act on it as in the `code` table: `invite_required` shows the invite screen, `consent_required` the consent screen and then sends again, `import_not_enabled` hides the import, `app_attest_required` gets a new app token and sends again once, keeping a refused write queued (never "drop the edit"). `mfa_required` is never sent to the app: the operator's page runs the TOTP challenge and sends again. |
| `404` | `app_attest_key_unknown`, `unknown_provider`, or none | Resource does not exist or is not the user's (`/v1/workouts/{id}`, exercises, plans, sessions), or unknown route or method; `POST /v1/app-attest/token` only: `app_attest_key_unknown` (the `key_id` is not one of the account's keys); `POST /v1/me/connections/{provider}/start` and `/confirm` only: `unknown_provider` (the provider is not served or not enabled for the account) | Branch on the `code` first. `app_attest_key_unknown`: drop the `key_id`, attest a new key, then get a token. `unknown_provider`: hide that provider's Connect entry. Without a `code` the resource is gone or not the user's: do not retry, drop the local reference and refresh the list. An unknown route or method is a `404` too: the app is newer than the server (on `POST /v1/app-attest/challenge`: App Attest is not live, send no app token). **Exception, the recovery writes and status** (`PUT /v1/recovery/checkins/{date}`, `GET /v1/recovery/status`, `PUT /v1/workouts/{id}/rpe`; not served yet): a `404` without a `code` there means the feature is off or not served: hide it, and never drop the workout or other local data. |
| `409` | `connection_limit`, `connect_ticket_invalid`, `merge_has_connections`, or none | Connections only: `connection_limit` (the provider's user cap is full) and `connect_ticket_invalid` (a confirm's ticket is unknown or another account's); `/v1/admin/*` only: `merge_has_connections` (the merge's source account holds a connection). Without a `code`: the id is taken by another user's or a built-in exercise, plan or behavior (`POST /v1/exercises`, `POST /v1/behaviors`, `PUT /v1/plans/{id}`; a `PUT /v1/plans/{id}` can also mean that a day's or a plan exercise's id is taken); completing a discarded session. **`PUT /v1/sessions/{id}`**: the server has newer sets — the body is then the server's `Session`, not the error shape. | Branch on the `code` first. `connection_limit`: say the provider is full, no automatic retry. `connect_ticket_invalid`: read `GET /v1/me/connections`, else start again. `merge_has_connections` is never sent to the app. Without a `code`: do not resend unchanged. An id that is taken: create the item again under new UUIDs, for the document and for its children. `PUT /v1/sessions/{id}`: the body is the server's `Session`; merge it with the local copy by set id (the later `updated_at` wins per set) and `PUT` the result. Completing a discarded session: treat it as discarded. |
| `410` | `connect_expired` | `POST /v1/me/connections/{provider}/confirm` only (Connections): more than 10 minutes after the callback (a repeat of a confirm that worked included); the grant waiting for it is dropped. | Do not resend it. Read `GET /v1/me/connections`: connected, done; not connected, start again. |
| `413` | — | Request body over the limit (32 MiB for `POST /v1/sync`, 1 MiB for `POST /v1/train/import`, 256 KiB for `PUT /v1/sessions/{id}` and `PUT /v1/plans/{id}`, 64 KiB otherwise). | Do not resend unchanged. `POST /v1/sync`: send smaller pages. Anywhere else the app broke a limit it knows: split or shorten the request. |
| `415` | — | A coaching relay fixture sends a missing, repeated or unsupported `Content-Type`. | Send one `Content-Type` matching the body. Use JSON for Garmin or JSON or form encoding for Suunto. |
| `422` | `app_attest_challenge_invalid`, `app_attest_invalid`, or none | Well-formed but invalid: out-of-range value, unknown or missing enum parameter (`range`, `metric`; an enum parameter holding `%00` is `400`: the URL is refused before the parameter is read), `from > to`, recompute range over 400 days, unknown timezone, a string in the body containing NUL (`\u0000`); `POST /v1/train/import`: an unknown `source`, over 5 plans, 50 sessions or 2 500 sets, the same id twice in the request; `PUT /v1/recovery/checkins/{date}` and `PUT /v1/workouts/{id}/rpe` (not served yet): a value out of range, a wake day that does not match, older than 14 days or in the future, a hidden duplicate rated; `POST /v1/app-attest/keys` and `/token` only: `app_attest_challenge_invalid` (the challenge is unknown, used up, expired or another account's), `app_attest_invalid` (the attestation or the assertion fails a check) | Without a `code`: do not resend unchanged: show the problem to the user (a field error) or correct the value. A string with `\u0000`: strip it and send again (Errors → NUL). Not transient. `app_attest_challenge_invalid`: a new challenge, then the same step again, once. `app_attest_invalid`: drop the `key_id` and attest a new key, once. |
| `429` | `import_in_progress`, or none | The per-user rate limit (see **Conventions**), a second `POST /v1/recompute` within a minute, the limits of `POST /v1/feedback` (10 a day) and `POST /v1/me/invite` (5 an hour), or a second import while one runs (`code` `import_in_progress`); `/v1/admin/*` is limited per client address instead of per user (Admin routes). api and ingest also limit each client address (20/s, bursts of 50; counted per instance, an IPv6 address by its /64) before any token is checked, on every route but the health paths; that `429` has the plain-text body `Too Many Requests` (`text/plain; charset=utf-8`, no `request_id` in the body, `X-Request-ID` in the headers as always), so do not require JSON on a `429`. | Wait `Retry-After` seconds, then send the same request again. The per-address `429` sends `Retry-After` too (the delay rounded up, at least 1, usually 1) and `X-Retry-In` (the delay as a Go duration, e.g. `37.5ms`; informative only); a `429` without `Retry-After`: wait 1 s. If the wait is longer than the app will hold (`POST /v1/feedback` after 10 in a day: up to 24 h; `POST /v1/me/invite`: up to about 12 minutes), stop and tell the user. `import_in_progress`: wait `Retry-After` (10 s) and resend unchanged. |
| `500` | — | Unexpected server error. The message is always `"internal error"`; the details are in the server log under `request_id`. A database call that fails in the middle of a request is a `500`, except where the `503` row says otherwise. | Not the user's fault, maybe transient: retry later with jittered backoff, a few times at most (the first retry may be immediate), and show `request_id` if it persists. |
| `503` | — | The server cannot answer now, always with `Retry-After` except for `GET /readyz`: the token or the account could not be loaded at the start of the request (Supabase keys or the database unreachable — **not** a sign-in problem; `Retry-After: 5`); the request ran out of time (`"request timed out"`; 5); `POST /v1/sync` could not apply its page (the database, or a concurrent sync of the same account; 5) or `POST /v1/train/import` could not finish (5); the queue is down (`POST /v1/recompute`, or the completion or edit of a session, which is already stored; 30); Apple is unreachable (`POST /v1/me/apple-authorization`; 5); the account lookup for a code bound to an email is unavailable (`POST /v1/me/invite`; 5); on `/v1/admin/*` the signing keys or the database for an admin token (5); the service is still starting (a cold start on Cloud Run, `"temporarily unavailable, send the request again"`; 5). `GET /readyz` and `GET /health/ready` answer `503` without `Retry-After` when a dependency is down. | Wait `Retry-After` seconds and send the same request again (a sync and an import are idempotent; after a `POST /v1/recompute` `503` the retry can get `429` with `Retry-After: 60`, since the minute is already used); keep the session. The `503` of the ready paths is for probes, not the app. |

Endpoint-specific statuses (`501` on `POST /v1/me/apple-authorization` and on the three `POST /v1/app-attest/*` routes, for one) are in the endpoint's own table; `499` is logged by the server and never reaches a client. A status this table does not list: `408` and a `5xx` other than `501` are transient like `503`; any other `4xx` (`405`, `431`, from the HTTP server before a handler runs) is like `400`: do not resend unchanged. Do not branch on the text.

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

### App Attest

D245 (the user's decision of 2026-11-19) is the design; D247 the server, which ships in mode `off`.
**What is served:** the three routes below answer only where the server has its token key
(`APP_ATTEST_TOKEN_KEYS`); without it they answer `501`, which the app treats like the `404` without
a `code` of a server before D247: App Attest is not live, send no `X-RYFT-App-Token`. Production runs in
mode `off` without the token key until the `warn` PR sets it, so it answers `501` until then; local
compose answers `501` too. The mode (`GET /v1/config` → `app_attest`) decides what happens to a request
without a valid token (**When the server checks**, below): under `off` the header is not read, `warn`
logs it, `on` refuses it. App Attest (Apple's DeviceCheck framework, `DCAppAttestService`) lets the server
tell the RYFT app on a genuine Apple device from any other client with a valid Supabase session: the
app attests a key once per account and install, then trades a signed assertion for a short-lived
**app token**, which it sends with every request.

**The flow.**

1. After sign-in, when `DCAppAttestService.shared.isSupported` is true and this account has no key on
   this install: `POST /v1/app-attest/challenge`. A `404` without a `code` or a `501` means App Attest
   is not live: send no `X-RYFT-App-Token` and try again at the next launch. No `generateKey()` and no
   `attestKey` before a `200` from this route. `GET /v1/config` → `app_attest` may let the app skip this
   probe (below), but a `501` or `404` from this route stays authoritative.
2. `generateKey()` gives the `key_id`. `attestKey(key_id, clientDataHash:)` with the challenge (rules
   below), then `POST /v1/app-attest/keys`. Keep the `key_id` with the account it was made for.
3. Right after step 2, when the token has less than `min(300, ttl_sec / 2)` seconds left, and after a
   `403 app_attest_required`: `POST /v1/app-attest/challenge` again, `generateAssertion(key_id,
   clientDataHash:)` with that challenge, then `POST /v1/app-attest/token`.
4. Every request to api and ingest carries `X-RYFT-App-Token: <token>` (and `X-RYFT-App-Build`,
   Conventions → App build).

**The mode in the config (AA2).** `GET /v1/config` → `app_attest` is the server's mode, `"off"`,
`"warn"` or `"on"`, always present (a server before D247 omits it). The app may use it to skip the
challenge probe of step 1 under `"off"`; a `501` or a `404` without a `code` from
`POST /v1/app-attest/challenge` stays authoritative in every mode (`"warn"` with a `501` means: not live).
Under `"on"` the config read itself needs a token (`GET /v1/config` is not exempt), so an app without
one learns `on` from the `403 app_attest_required`, not from the config. The config is cached for its
`ttl_sec` (6 h), so a mode change reaches the app within that.

**Byte-exact rules** (the app and the server hash the same bytes):

| Value | Rule |
| --- | --- |
| `challenge` | An opaque ASCII string from `POST /v1/app-attest/challenge` (today 43 characters of base64url without padding for 32 random bytes; don't rely on the length or the alphabet). Send it back exactly as received; never decode it. |
| `clientDataHash` | SHA-256 of the UTF-8 bytes of the `challenge` string as received: no quotes, no newline, no decoding. The same rule for `attestKey` and for `generateAssertion`: `Data(SHA256.hash(data: Data(challenge.utf8)))`. |
| `key_id` | The string `generateKey()` returned, unchanged: **standard** base64 (`+`, `/`, `=` padding), 44 characters, of the SHA-256 of the key's public key. Not base64url. The server decodes it to 32 bytes and checks it against the credential certificate's public key and the attested credential id. |
| `attestation`, `assertion` | The `Data` `attestKey` and `generateAssertion` returned (CBOR), as `data.base64EncodedString()` with no options: standard base64, `=` padding, no line breaks. |

**The token.** Send it as `X-RYFT-App-Token: <token>`, exactly as received, on every request to both
hosts (`https://api.ryftlab.de`, `https://ingest.ryftlab.de`); the exempt routes below ignore it. It is
opaque, at most 512 ASCII characters: never parse, log or show it. Keep it in memory or in the Keychain
(`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`), with the account it belongs to. It is bound to the
account (the bearer's `sub`), and names the key it was issued for (checked on every request is the account
only): after a change of account, use that account's own key and token. A new token does not revoke an earlier one; each runs until its `expires_at`. The server checks a
token without a database read, so a key deleted on the server keeps its tokens working until they expire
(at most an hour).

**When the server checks** (`APP_ATTEST` in `deploy/run/config.env`, D245):

| Mode | A request without a valid token |
| --- | --- |
| `off` (as shipped, D247) | served; the header is not read, nothing is logged |
| `warn` | served as before; the server logs it (one warn line with the reason, the route group and `X-RYFT-App-Build`; no account, no token) and counts it, and nothing in the answer says so |
| `on` | `403` with `code` `app_attest_required` |

The modes go `off` → `warn` → `on`, never straight from `off` to `on`. A token is valid when this
platform issued it, it has not expired and it is for the bearer's account. The check runs after the
bearer (a missing or bad bearer is still `401`) and the per-user limit, and before the beta and consent
gates, the routing and the body.

**Exempt** (they never answer `app_attest_required`):

- the health paths `GET /health/live`, `/health/ready`, `/healthz`, `/readyz` (every service, no
  bearer) and `GET /metrics` (port 9090, local compose only);
- `POST /internal/tick` (Pub/Sub's push to api and ingest; its own token);
- every `/v1/admin/*` route and method, with either bearer, preflights included: the site's
  `https://ryftlab.de/admin` page calls them from a browser, and the operator's scripts with the key;
- `POST /v1/app-attest/challenge`, `POST /v1/app-attest/keys`, `POST /v1/app-attest/token`;
- `DELETE /v1/me` and `POST /v1/me/apple-authorization` (erasure, and its Apple half),
  `DELETE /v1/me/consent` (withdrawal), `DELETE /v1/me/connections/{provider}` (ending a connection and
  deleting its data; Connections, not served yet) and `DELETE /v1/devices/{id}/push` (sign-out's unregistering,
  so a device without a token stops getting pushes): they only remove, and work from any signed-in
  client;
- `POST /v1/feedback`, so a tester can still report that the app cannot verify itself;
- `GET /v1/connect/{provider}/callback` (Connections, not served yet): the provider's sign-in page sends the
  browser there, with no bearer and no app token;
- the accounts listed in `APP_ATTEST_EXEMPT_USERS` (the smoke user of `make smoke-prod` and
  `deploy.yml`), on every route.

Everything else on api and ingest needs the token under `on`: `POST /v1/sync`, `GET /v1/config` and
`GET /v1/me` included, and an unknown `/v1/*` path too (with a token it is the usual `404`). Paths outside
`/v1/` keep their JSON `404`. The site's other pages call neither host (their `connect-src` is `'none'`).

**The app's rules.**

- **`403 app_attest_required`:** get a new token (step 3, from step 1 when this account has no key on
  this install) and send the request again, once. Refused again: stop, and show the problem with its
  `request_id`; no loop. When `isSupported` is false, don't try: say that this device cannot verify the
  app.
- **Never lose a write on it.** A queued Train write (`PUT /v1/sessions/{id}`,
  `POST /v1/sessions/{id}/complete`, plans, journal) or a sync page refused with `app_attest_required`
  stays queued, as for `invite_required` or `consent_required`: no "drop the edit", and no HealthKit
  anchor is committed for a refused page. It is sent again once a token is had, and kept for later if
  the renewal fails.
- **One renewal per account and key at a time.** While a token is being renewed (or a key attested),
  every request refused with `app_attest_required` waits for that renewal and is then sent again with
  its token; none starts a renewal of its own. The server accepts an assertion only when its counter is
  above the last one it accepted for the key, so two renewals in flight can refuse one of them
  (`app_attest_invalid`), and a re-attestation each would use up `POST /v1/app-attest/keys`' budget.
- **One key per account on each install.** Keep the `key_id` across launches (the Keychain is fine). After
  a sign-in as another account, make that account its own key.
- **`DCError` from the framework** *(Apple's guidance as known, unverified, D245)*: `invalidKey` from
  `attestKey` or `generateAssertion` (the key is gone: the app was reinstalled, or restored onto another
  device): drop the `key_id` and start again at step 1. `serverUnavailable` from `attestKey`: try again
  later with the same key. Any other `attestKey` error: drop the key, and make a new one at the next try.
  `generateAssertion` needs no network.
- **No App Attest** (the Simulator, `isSupported` false): no key, no header. Under `warn` the server only
  logs it; under `on` every route the check covers answers `403 app_attest_required`.
- **Environment** *(Apple's documentation through search snippets, D245)*. TestFlight, App Store and
  Enterprise builds always attest in Apple's **production** environment, whatever the entitlement says; a
  build run from Xcode attests in **development** unless its entitlement
  `com.apple.developer.devicecheck.appattest-environment` is `production`, which makes it use production
  too. Every server, local compose included, accepts production attestations only (D247; a development
  attestation is `422 app_attest_invalid`), so every build sets the entitlement to `production`. A key from one environment does not
  work in the other: after a change of the entitlement, make a new key.
- **Old builds** send no header. Under `off` and `warn` nothing changes for them. Under `on` every route
  the check covers answers them `403` with a `code` they do not know, which they read as no `code` (the
  "built-in" `403`) and never as a sign-out. They then **drop every queued Train write** they send
  (`PUT /v1/sessions/{id}`, `POST /v1/sessions/{id}/complete`): those sessions are lost, not deferred.
  So `on` comes only once the `warn` lines show no request without a token from an app build for days
  (D245's stop rule), not merely once every tester could have updated.

#### `POST /v1/app-attest/challenge` — a one-time challenge

Bearer as on every `/v1/*` route; no body (an empty body or `{}`). The exchange (the body follows):

```http
POST /v1/app-attest/challenge HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Build: 20261119.091500

HTTP/2 200
content-type: application/json
x-request-id: 01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c
```

Response `200 AppAttestChallenge`:

<!-- type: model.AppAttestChallenge -->
```json
{ "challenge": "q3Jx0mZ9cT1vY8kL2pW5nB7dF4hS6aE0uR3iO9yG1tM", "expires_at": "2026-11-19T10:05:00Z" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `challenge` | string | Opaque, ASCII, single use, for this account only. A call of `POST /v1/app-attest/keys` or `POST /v1/app-attest/token` that gets past the `429` and the JSON decoding (a body that isn't JSON or has an unknown field is a `400` that leaves it unused) uses it up, whatever it answers then, the `400` of a malformed `key_id` or blob included: fetch a new one for every attempt. |
| `expires_at` | timestamp | 5 minutes after it was made. Use the challenge at once; don't compare `expires_at` with the device's clock. |

At most 5 open challenges per account: a sixth cancels the oldest.

#### `POST /v1/app-attest/keys` — attest a key

The exchange (the bodies follow):

```http
POST /v1/app-attest/keys HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Build: 20261119.091500
content-type: application/json

HTTP/2 201
content-type: application/json
x-request-id: 01a14ffb-0a52-7c3d-8e47-5b0d1e8c7a10
```

Request `AppAttestKeyPost` (the `attestation` shortened here):

<!-- type: model.AppAttestKeyPost -->
```json
{ "key_id": "pKqW2rZ8yH3nT6v+1mX9cF4sD7gJ0eL5uA2oI8w/3kE=",
  "attestation": "o2NmbXRvYXBwbGUtYXBwYXR0ZXN0Z2F0dFN0bXSiY3g1Y4JZA…",
  "challenge": "q3Jx0mZ9cT1vY8kL2pW5nB7dF4hS6aE0uR3iO9yG1tM" }
```

Response `201 AppAttestKey`:

<!-- type: model.AppAttestKey -->
```json
{ "key_id": "pKqW2rZ8yH3nT6v+1mX9cF4sD7gJ0eL5uA2oI8w/3kE=" }
```

All three fields are required strings (the rules above); `attestation` is at most 32 KiB of text (a real
one is a few KiB). `201` echoes `key_id`. What the server checks, after the challenge (Apple's "Validating
apps that connect to your server"; any failure is `422 app_attest_invalid`):

1. `attestation` is a CBOR map with `fmt` `"apple-appattest"`, `attStmt` (`x5c`, `receipt`) and `authData`;
2. `x5c` (the credential certificate, then the intermediate) chains to Apple's App Attestation Root CA,
   which the server holds: it calls nobody;
3. the credential certificate's extension `1.2.840.113635.100.8.2` is a DER `SEQUENCE { [1] { OCTET STRING
   nonce } }`, and that octet string equals `nonce` = SHA-256(`authData` ‖ `clientDataHash`);
4. SHA-256 of that certificate's public key (the uncompressed P-256 point) is `key_id` decoded, and so is
   the credential id in `authData`;
5. the RP ID hash in `authData` is SHA-256 of the App ID `M77SKHKG63.dev.timkrebs.ryft`;
6. the counter in `authData` is 0;
7. the AAGUID in `authData` is `appattest` followed by seven `0x00` bytes (production). Every server
   accepts production attestations only (D247): a development attestation (AAGUID `appattestdevelop`)
   is `422 app_attest_invalid`, on local compose too.

The `receipt` is not used: it is neither stored nor sent to Apple (no fraud metric). The server stores the
public key with the account and `key_id`, the environment (always `production`) and a counter of 0. Attesting the same `key_id`
again for the account replaces it. At most 10 keys per account: an eleventh deletes the one used longest
ago, and a key unused for 90 days is deleted. Either way the app learns it from
`404 app_attest_key_unknown` at its next token.

#### `POST /v1/app-attest/token` — an app token

The exchange (the bodies follow):

```http
POST /v1/app-attest/token HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Build: 20261119.091500
content-type: application/json

HTTP/2 200
content-type: application/json
x-request-id: 01a14ffc-2b13-7e44-9f58-6c1e2f9d8b21
```

Request `AppAttestTokenPost` (the `assertion` shortened here):

<!-- type: model.AppAttestTokenPost -->
```json
{ "key_id": "pKqW2rZ8yH3nT6v+1mX9cF4sD7gJ0eL5uA2oI8w/3kE=",
  "challenge": "Zt0bH6wQ1cJ8mV3xR5kP9nY2sL7aD4fG0uE6iT1oW3M",
  "assertion": "omlzaWduYXR1cmVYRzBFAiEA…" }
```

Response `200 AppAttestToken`:

<!-- type: model.AppAttestToken -->
```json
{ "token": "<opaque, at most 512 ASCII characters>",
  "expires_at": "2026-11-19T11:06:12Z", "ttl_sec": 3600 }
```

Every later request then carries the token and the build:

```http
GET /v1/days?from=2026-11-13&to=2026-11-19 HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Token: <the token, exactly as received>
X-RYFT-App-Build: 20261119.091500
X-RYFT-Device-ID: 6f1c2e3a-8b4d-4f5e-9a7b-1c2d3e4f5a6b
```

All three fields are required strings; `assertion` is at most 1 KiB of text. The server checks, in this
order (any failure after step 1 is `422 app_attest_invalid`):

1. `key_id` is one of the account's keys (else `404 app_attest_key_unknown`), and the challenge is valid
   (else `422 app_attest_challenge_invalid`);
2. `assertion` is a CBOR map with `signature` and `authenticatorData`;
3. `signature` (ECDSA P-256, ASN.1 DER) verifies with the key's stored public key as a signature of the
   message `nonce` = SHA-256(`authenticatorData` ‖ `clientDataHash`) under ECDSA with SHA-256: the digest
   checked is SHA-256(`nonce`), in Go `ecdsa.VerifyASN1(pub, sha256(nonce), signature)`;
4. the RP ID hash in `authenticatorData` is SHA-256 of the App ID;
5. the counter in `authenticatorData` is above the last one accepted for the key (the first assertion's:
   above 0); the server keeps the new one.

| Field | Type | Notes |
| --- | --- | --- |
| `token` | string | Opaque, at most 512 ASCII characters. Send it as `X-RYFT-App-Token`. |
| `expires_at` | timestamp | `ttl_sec` after it was issued (one hour today); the server refuses it from then on. |
| `ttl_sec` | int | Seconds the token is valid: **3600 today, never below 600** (AA3; a server test pins the floor). Get the next token when less than `min(300, ttl_sec / 2)` seconds are left, counted on the app's own clock from when this answer arrived, not by comparing `expires_at` with the device's clock. |

#### Answers of the three routes

They are api routes behind the bearer, outside the beta gate (they never answer `invite_required`) and
outside the consent gate. The Conventions apply (64 KiB bodies, unknown fields `400`, `X-Request-ID`, the
per-address limit and the per-user limit of every `/v1/*` route); on top of those, each has its own
budget per account (`429` row). Where the server has its token key they are served in every mode, `off`
included; the mode only decides what happens to the other routes.

**No echoed values (AA4).** The `error` text of every answer of the three routes is a fixed sentence per
refusal: it never contains the `challenge`, the `key_id`, the `assertion`, the `attestation`, a token, or
any other value of the request, and neither do the server's logs of these routes. The app may show the
text; it still branches on the status and the `code`, never on the text.

| Status | `code` | When | App should |
| --- | --- | --- | --- |
| `200`, `201` | — | as above | — |
| `400` | — | a field missing or unknown, a `key_id` that is not 44 characters of standard base64 for 32 bytes, an `attestation` or `assertion` that is not standard base64 or is over its limit | a client bug: log `request_id`, don't resend unchanged |
| `401` | — | the bearer, as on every `/v1/*` route | as on every route (Authentication) |
| `404` | `app_attest_key_unknown` | `POST /v1/app-attest/token` only: `key_id` is not one of the account's keys (never attested here, replaced, unused for 90 days, or deleted with the account's data) | drop the `key_id`, attest a new key (step 1), then get a token |
| `404` | — | no `code`: a server before D247, which has no App Attest (an unknown route) | send no header; try again at the next launch. Branch on the `code` first: only a `404` without it means "not live" |
| `422` | `app_attest_challenge_invalid` | `POST /v1/app-attest/keys`, `POST /v1/app-attest/token`: the challenge is unknown, used up, expired or another account's | a new challenge, then the same step again, once |
| `422` | `app_attest_invalid` | `POST /v1/app-attest/keys`: the attestation fails a check (a development attestation where only production is accepted included); `POST /v1/app-attest/token`: the assertion fails a check (the counter included) | drop the `key_id` and attest a new key, once; refused again: stop until the next launch |
| `429` | — | `POST /v1/app-attest/challenge` and `POST /v1/app-attest/token`: per account, bursts of 10, then one a minute (two budgets); `POST /v1/app-attest/keys`: 10 per account in 24 hours | wait `Retry-After`; on `POST /v1/app-attest/keys` it can be hours: carry on without a token until then |
| `501` | — | the server has no token key (`APP_ATTEST_TOKEN_KEYS` unset): local compose, and production in mode `off` until the `warn` PR sets it (after the privacy text names App Attest), whatever `GET /v1/config` → `app_attest` says | as the `404` without a `code`: App Attest is not live, send no header; try again at the next launch |
| `503` | — | as on every route | as on every route |

**What the server keeps** (D245): per account and app install, the key id, the public key, its counter, its
environment and when it was attested and last used, until the account is deleted, the key is replaced, or
90 days after the key's last use; open challenges for 5 minutes; tokens nowhere. `DELETE /v1/me` deletes
the keys and challenges. No receipt, no device model, no address.

## Nutrition

The `/v1/nutrition` contract covers food search, barcode lookup, portions, owner-private custom foods and submissions, meal logging, daily summaries and user-configured goals. Meals, food lookup, custom foods and daily totals require either `nutrition.diary_enabled` or the existing protected pilot's `nutrition.enabled`. Submissions, CGM upload, contextual scoring and coaching require the protected pilot. Both flags default dark for new accounts. Nutrition data writes use current consent under `CONSENT_ENFORCE=on` or `warn`. Authenticated owner erasure routes continue to work when these flags are off. [Nutrition endpoints and wire examples](architecture/nutrition/04-api-contracts.md)

Daily and range summaries continue to return meal counts and nutrient totals to the owner after consent withdrawal. Fresh scoring requires current consent and the protected pilot flag. For the diary cohort or when consent is unavailable, the three scores and all ten quality components are `null`, the per-meal glucose list is `[]`, the timing penalty is zero and `n2r` is omitted. The response still contains `score_version`.

`GET /v1/me/nutrition-export` returns `{"meals": [...], "next_after": "uuid"}` with the owner's saved meal and item snapshots across all dates. Set `limit` from 1 to 25 (default 20) and pass `next_after` unchanged as `after` until it is `null`. Each page is a consistent read and later pages reflect subsequent edits and erasures. Responses use `Cache-Control: no-store`. The caller needs the normal account bearer and applicable App Attest token even when beta admission, Nutrition flags or health-data processing consent are inactive. The endpoint exports meal diary snapshots and, on the first page only, the caller's food favourites as `favorites` (D284). The first page (no `after`) always carries `favorites`, as `[]` when there are none. Later pages (`after` set) omit the field, so decode it as optional. Each entry has `food_id`, `food_name` (the catalog's canonical name, `""` when the food is no longer readable; the ID is kept) and `favorited_at` (UTC), ordered by `favorited_at` and then `food_id`. Favourites are read in the same consistent snapshot as the first page. Private foods, goals, CGM and other personal data use the existing account rights process.

<!-- type: model.NutritionMealExportPage -->
```json
{"meals": [], "next_after": null}
```

A first page with one favourite:

<!-- type: model.NutritionMealExportPage -->
```json
{
  "meals": [],
  "next_after": null,
  "favorites": [
    {"food_id": "a7e40c6e-bfb4-47eb-ad64-aea61136db01", "food_name": "Apple", "favorited_at": "2026-11-26T08:15:00Z"}
  ]
}
```

### Nutrition food selection

Added 2026-11-25 (D284). Additive: no field is renamed, removed or retyped, and a build that ignores every new field keeps working. Curated staples (USDA SR Legacy and BLS 4.0) carry measured household portions, one explicit default per food, and localized labels. Favourites and the last selection belong to the signed-in account. The server computes portion weights and macros. The app never sends grams for a portion.

**Search.** `POST /v1/nutrition/foods/search` takes the existing `model.FoodSearch` body. Each `foods[]` entry may also carry the following optional fields. Decode every one of them as optional.

| Field | Meaning |
| --- | --- |
| `subtitle` | A localized qualifier (`language`, then its base language, then English). Absent when the catalog has none. |
| `default_portion` | The catalog's measured default as a `FoodPortionSelection` with a real `portion_id`. Absent when the food has no measured default. |
| `last_used_portion` | The caller's most recent selection in the last 90 days of their remaining diary items. If the portion was changed or retired, the saved grams or millilitres are used instead (`unit` `g`/`ml`, no `portion_id`). Absent when there is no usable selection. |
| `reference_portion` | 100 g, or 100 ml when the food's nutrients are given per volume. Absent when no compatible nutrients exist. |
| `favorited` | Always present on search and detail (`true`/`false`). Absent on barcode matches and on `POST /foods/custom`. |

Basic-food queries rank a matching staple and whole foods above derivatives. A query is broad when it is the name of a staple group, or a single word of 4 or more letters that starts a staple's name. A broad query ranks, in this order: (1) the group's staples, (2) its whole forms, (3) exact name matches, (4) cultivars, and (5) the group's other forms (for example apple juice), which still come ahead of brand and prefix matches. For example `Apfel` ranks the raw apple above apple juice. A query that names an exact variant, such as `Milch 1,5 Prozent`, is not broad: its exact match ranks first, even when another variant is a favourite. Within a rank, the caller's favourites, item count in the last 90 days and most recent use order the results. Where two foods tie on that and on how well their name matches, a curated staple ranks before any other food, so an exact German BLS name never outranks the staple whose alias it equals (D296). After that the caller's market, the caller's language, source quality, completeness and text similarity decide. The food ID breaks the remaining ties.

A `FoodPortionSelection` has `quantity` and `unit` (`g`, `ml`, `portion` or `serving`) and optional `portion_id`, `label`, `short_label`, `gram_weight`, `volume_ml`, `kcal`, `protein_g`, `carbs_g` and `fat_g`. A macro the catalog doesn't know is absent; it is never `0`. The values come from the same calculator that saves meals, so the preview matches the saved item.

<!-- type: model.FoodSearch -->
```json
{"query": "apfel", "language": "de-DE", "market": "DE", "limit": 20}
```

<!-- type: model.FoodSearchPage -->
```json
{
  "foods": [
    {
      "id": "0f3c9a52-6a1e-4d0b-9c61-6d3f0a1b2c01",
      "name": "Apfel, roh, mit Schale",
      "brand": "",
      "type": "generic",
      "quality": "verified",
      "subtitle": "Roh, mit Schale",
      "default_portion": {"portion_id": "a7e40c6e-bfb4-47eb-ad64-aea61136db04", "quantity": 1, "unit": "portion", "label": "1 mittelgroßer Apfel", "short_label": "Mittel", "gram_weight": 182, "kcal": 94.64, "protein_g": 0.4732, "carbs_g": 25.1342, "fat_g": 0.3094},
      "last_used_portion": {"portion_id": "5d0b8c1e-2f47-4a6b-8e13-91c2d4e5f602", "quantity": 1, "unit": "portion", "label": "1 kleiner Apfel", "short_label": "Klein", "gram_weight": 149, "kcal": 77.48, "protein_g": 0.3874, "carbs_g": 20.5769, "fat_g": 0.2533},
      "reference_portion": {"quantity": 100, "unit": "g", "gram_weight": 100, "kcal": 52, "protein_g": 0.26, "carbs_g": 13.81, "fat_g": 0.17},
      "favorited": true
    },
    {
      "id": "6b2e7d14-3c58-4f9a-a0b2-7e8f9a0b1c03",
      "name": "Apfelsaft",
      "brand": "",
      "type": "generic",
      "quality": "verified",
      "reference_portion": {"quantity": 100, "unit": "ml", "volume_ml": 100, "kcal": 46},
      "favorited": false
    }
  ],
  "next_offset": null
}
```

**Detail.** `GET /v1/nutrition/foods/{id}?language=de-DE` (`language` is optional; an invalid tag returns `422`). It returns the existing `FoodDetail`, and `name` uses the matching non-alias name. `portions` are ordered by `sort_order`, then label, then ID. Each portion always has `is_default` and `sort_order`, and may have `portion_key`, `short_label_en` and `short_label_de`. The catalog marks at most one portion per food `is_default: true` (if several ever appear, `default_portion` is the first in that order), and a default is always measured (never `estimated`). Detail also has the search fields above, plus `last_used_selection`, which has the same value as `last_used_portion`. BLS sources carry the BLS code in `external_id`, the release in `dataset_version`, and a named `attribution` (CC BY 4.0) that the app shows with the source.

<!-- type: model.FoodDetail -->
```json
{
  "id": "0f3c9a52-6a1e-4d0b-9c61-6d3f0a1b2c01",
  "name": "Apfel, roh, mit Schale",
  "brand": "",
  "type": "generic",
  "quality": "verified",
  "subtitle": "Roh, mit Schale",
  "default_portion": {"portion_id": "a7e40c6e-bfb4-47eb-ad64-aea61136db04", "quantity": 1, "unit": "portion", "label": "1 mittelgroßer Apfel", "short_label": "Mittel", "gram_weight": 182, "kcal": 94.64, "protein_g": 0.4732, "carbs_g": 25.1342, "fat_g": 0.3094},
  "reference_portion": {"quantity": 100, "unit": "g", "gram_weight": 100, "kcal": 52, "protein_g": 0.26, "carbs_g": 13.81, "fat_g": 0.17},
  "favorited": true,
  "names": [{"language": "de", "label": "Apfel, roh, mit Schale", "alias": false}, {"language": "en", "label": "Apples, raw, with skin", "alias": false}],
  "sources": [{"id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "provider": "usda", "external_id": "171688", "dataset_version": "SR Legacy 2018-04", "source_url": "https://fdc.nal.usda.gov/food-details/171688/nutrients", "license_identifier": "CC0-1.0"}],
  "portions": [
    {"id": "5d0b8c1e-2f47-4a6b-8e13-91c2d4e5f602", "source_id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "label": "1 small apple", "labels": {"de": "1 kleiner Apfel", "en": "1 small apple"}, "quantity": 1, "unit": "portion", "weight_g": 149, "estimated": false, "portion_key": "small", "short_label_en": "Small", "short_label_de": "Klein", "is_default": false, "sort_order": 10},
    {"id": "a7e40c6e-bfb4-47eb-ad64-aea61136db04", "source_id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "label": "1 medium apple", "labels": {"de": "1 mittelgroßer Apfel", "en": "1 medium apple"}, "quantity": 1, "unit": "portion", "weight_g": 182, "estimated": false, "portion_key": "medium", "short_label_en": "Medium", "short_label_de": "Mittel", "is_default": true, "sort_order": 20},
    {"id": "e1f2a3b4-c5d6-4e7f-8091-a2b3c4d5e604", "source_id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "label": "1 large apple", "labels": {"de": "1 großer Apfel", "en": "1 large apple"}, "quantity": 1, "unit": "portion", "weight_g": 223, "estimated": false, "portion_key": "large", "short_label_en": "Large", "short_label_de": "Groß", "is_default": false, "sort_order": 30}
  ],
  "nutrients": [{"code": "energy_kcal", "amount": 52, "unit": "kcal", "reference_amount": 100, "reference_unit": "g", "source_id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "method": "fdc:1008", "quality": "source_reported"}]
}
```

**Saving a meal.** `POST /v1/nutrition/meals` and `PATCH /v1/nutrition/meals/{id}` items take an optional `language`. For a new item with `unit` `portion` or `serving` and its `portion_id`, the server computes `weight_g`/`volume_ml` and the nutrients from the catalog. It also stores `food_name` in that language, plus `portion_label` and `portion_short_label`. An invalid `language` returns `422`. `language` is not allowed on an `item_id` entry and returns `422` there.

<!-- type: model.NutritionMealInput -->
```json
{
  "client_request_id": "9b1d4c3e-7a2f-4e8b-b6c5-d4e3f2a1b0c9",
  "consumed_at": "2026-11-25T07:30:00Z",
  "timezone": "Europe/Berlin",
  "meal_type": "breakfast",
  "input_method": "manual_search",
  "items": [{"food_id": "0f3c9a52-6a1e-4d0b-9c61-6d3f0a1b2c01", "quantity": 1, "unit": "portion", "portion_id": "a7e40c6e-bfb4-47eb-ad64-aea61136db04", "language": "de-DE"}]
}
```

**Saved items.** `POST /meals`, `PATCH /meals/{id}`, `GET /meals`, `GET /meals/{id}` and `GET /v1/me/nutrition-export` return each item with optional `portion_label` and `portion_short_label`. Both are a snapshot taken when the item was saved, and a later catalog rename or retirement doesn't change them. A PATCH that keeps an item by `item_id` (for example a change to the meal's name, notes or time) keeps the item's whole snapshot, labels included. Items saved before D284 have no short label. They gain `portion_label` only when their food, portion ID, unit and measured amount still match the catalog portion. Otherwise both fields are absent: show `quantity` and `unit`.

<!-- type: model.NutritionMealItem -->
```json
{
  "id": "63cf1226-48cf-43c7-957e-c994ec3bbe61",
  "food_id": "0f3c9a52-6a1e-4d0b-9c61-6d3f0a1b2c01",
  "food_name": "Apfel, roh, mit Schale",
  "brand": "",
  "quantity": 1,
  "unit": "portion",
  "portion_id": "a7e40c6e-bfb4-47eb-ad64-aea61136db04",
  "portion_label": "1 mittelgroßer Apfel",
  "portion_short_label": "Mittel",
  "weight_g": 182,
  "volume_ml": null,
  "nutrients": {"energy_kcal": {"amount": 94.64, "unit": "kcal", "source_id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "reference_amount": 100, "reference_unit": "g", "method": "fdc:1008", "quality": "source_reported"}},
  "sources": [{"id": "c4d5e6f7-0818-4a2b-9c3d-4e5f60718293", "provider": "usda", "external_id": "171688", "dataset_version": "SR Legacy 2018-04", "source_url": "https://fdc.nal.usda.gov/food-details/171688/nutrients", "license_identifier": "CC0-1.0"}],
  "calculation_version": 1,
  "created_at": "2026-11-25T07:30:01Z"
}
```

<!-- type: model.NutritionMealPatch -->
```json
{"revision": 1, "notes": "Before the run", "items": [{"item_id": "63cf1226-48cf-43c7-957e-c994ec3bbe61"}]}
```

**Favourites.** `PUT /v1/nutrition/foods/{id}/favorite` and `DELETE /v1/nutrition/foods/{id}/favorite` have no body and answer `204`. Both are idempotent, so a repeat also answers `204`. PUT needs the diary gate (`nutrition.diary_enabled` or `nutrition.enabled`) and a food the caller can see (a public food, or the caller's own custom food); otherwise it returns `404`. DELETE works for any valid UUID even with the Nutrition flags off, like the other erasure routes. A malformed ID returns `404`. Favourites belong to the account, are never shown to anyone else, and are deleted with the account (`DELETE /v1/me`). `GET /v1/me/nutrition-export` lists them as `favorites` on its first page; see [Nutrition](#nutrition).

```http
PUT /v1/nutrition/foods/0f3c9a52-6a1e-4d0b-9c61-6d3f0a1b2c01/favorite
Authorization: Bearer <access token>

HTTP/1.1 204 No Content
```

**Old builds** send no `language` and get the same items as before, plus the optional labels, which they ignore. Search and detail still return every field they returned before.

### Nutrition-to-Recovery

`PUT /v1/nutrition/n2r/profile` opts into contextual scoring and returns the same settings shape. `GET /v1/nutrition/n2r/profile` returns the caller's settings or JSON `null`. `DELETE /v1/nutrition/n2r/profile` removes the settings and their derived night records and returns 204. The calorie target is configured through the existing Nutrition goals endpoint.

<!-- type: model.N2RProfile -->
```json
{
  "body_mass_kg": 70,
  "sex": "unspecified",
  "sweat_rate_l_per_h": null,
  "sweat_na_mg_per_l": null,
  "caffeine_half_life_h": null,
  "expected_bedtime": "23:00",
  "next_workout_start_at": null
}
```

When enrolled with current consent, `GET /v1/nutrition/daily` and each summary row include `n2r`, a versioned `model.N2RScore` object. Its `n2r_score` is nullable until logging reaches the calorie threshold and all quality components can be scored. `quality_score`, `glucose_score` and individual components can be nullable depending on coverage.

`GET /v1/nutrition/guidance/current` includes the current waking day's N2R breakdown, morning biometrics when available and prioritized cards. Live guidance uses the account timezone and expected bedtime, including the previous waking day before an early-morning bedtime. Food suggestions may be empty when the catalog lacks a match. An unenrolled account receives empty cards and a null score.

<!-- type: model.N2RGuidance shape -->
```json
{
  "local_date": "2026-11-24",
  "score": {"version": "1.0.0", "n2r_score": null, "quality_score": null, "budget": {"energy_target_kcal": 2400, "strain": "high", "carb_target_g": 560}, "fuel_multiplier": 0.85, "tags": ["underfueled_high_strain"]},
  "morning_biometrics": {"wake_date": "2026-11-24", "ln_rmssd_7d_mean": 3.9, "ln_rmssd_7d_sd": 0.16, "ln_rmssd_7d_cv": 4.10, "swc": 0.032, "ln_rmssd_delta": -0.17, "rhr_delta": 3.0, "change": "moderate"},
  "cards": [{"code": "POST_WORKOUT_ENDURANCE_REFEED", "actions": ["Refuel with fast-digesting carbohydrates."], "targets": {"carb_min_g": 56, "carb_max_g": 84}, "foods": []}]
}
```


### Training-to-Nutrition coaching

The signed-in, allow-listed Nutrition account uses the same v1 auth, App Attest and consent gates. Set a birth year first with `PATCH /v1/me`, then enroll through the N2R profile. Coaching requires a birth year that establishes adulthood, standing height in centimeters, and the N2R body mass. A later weigh-in updates the weight trend. Dates and session classification use the account timezone. A user may opt out with `DELETE /v1/users/goal-profile` (204) to remove the goal, partner links and coaching history.

| Route | Request and response |
| --- | --- |
| `GET /v1/users/goal-profile` | 200 `GoalProfile` or JSON null |
| `PUT /v1/users/goal-profile` | 200 `GoalProfile`; replaces prior settings. Invalid data or insufficient age evidence returns 422 |
| `PUT /v1/coaching/feedback/{date}` | 200 `CoachingFeedback`; replaces feedback and the planned session for this local date |
| `GET /v1/coaching/daily-briefing` | 200 `CoachingBriefing` with periodized targets, readiness, training load, N2R and guidance. With no coaching plan the `plan` field is null. Gains `recovery` (`RecoveryStatus`), a check-in-based `readiness` without overnight HRV, the neutral display name `readiness_display`, and `N2RScore` gains `bedtime_source` (D291–D294, not served yet: [Recovery without a watch](#recovery-without-a-watch--check-ins-and-session-rpe-d291d295-not-served-yet)) |
| `POST /v1/wearables/healthkit/sync` | Alias on the ingest host for `POST /v1/sync` with the same request, auth and deletions |

`PUT /v1/users/goal-profile`

<!-- type: model.GoalProfile -->
```json
{"goal":"MARATHON","height_cm":178,"body_fat_pct":17,"race_date":"2027-04-25","hr_max":191,"hr_rest":53,"gut_trained":false,"recreational":true,"train_low":false}
```

`PUT /v1/coaching/feedback/2026-11-24`

<!-- type: model.CoachingFeedback -->
```json
{"intake_complete":true,"is_fasting_day":false,"planned_session":{"start_at":"2026-11-24T18:00:00+01:00","sport_type":"running","duration_min":75,"class":"MODERATE"},"intra_workout_carbs_g":45,"gi_distress_0_10":1,"rpe_0_10":6}
```

`GET /v1/coaching/daily-briefing` on an account without enough information for coaching.

<!-- type: model.CoachingBriefing -->
```json
{"local_date":"2026-11-24","plan":null,"readiness":"UNKNOWN","readiness_reasons":[],"training_load":{"acute_7d":0,"chronic_28d_weekly":0,"acwr":null,"tsb":0},"n2r":null,"yesterday_n2r":null,"morning_biometrics":null,"cards":[]}
```

The plan includes `energy_target_kcal`, `protein_target_g`, `carb_target_g`, `fat_target_g`, estimated `net_exercise_kcal`, `session_class`, `deficit_suspended`, `train_low`, availability evidence and optional fueling. Its energy and carb targets also feed `n2r.budget` in existing Nutrition routes. Availability can be `UNKNOWN`, `GREEN`, `YELLOW` or `RED`. The colors are product guardrails for calorie deficits. The probability used in the evaluation is a shadow score. Athlete-facing medical diagnosis and injury predictions are outside this contract.

HealthKit accepts optional workout `tz_offset_sec`, `was_user_entered` and source bundle ID. Additional quantities use `body_mass` in kilograms. The app can send an optional `cursor` object with `stream`, `sequence` and opaque `value`. Cursor progress is monotonic per account, device and stream. HealthKit HRV keeps its SDNN units and metric identity. The app may continue using its existing sync endpoint and local anchors.

The coaching relay routes `POST /v1/webhooks/garmin` and `POST /v1/webhooks/suunto` return 404 by default. Cloud Run rejects any relay signing or fetch configuration pending approval and verification of a provider connector. The local fixture contract requires a single `X-HMAC-SHA256-Signature` header over the original body and a single `Content-Type`. Garmin fixtures use `application/json`. Suunto fixtures use JSON or `application/x-www-form-urlencoded` with `username` and `workoutid`. Each provider uses its own signing key.

A synthetic relay receives 200 after durable acceptance or a no-op for a duplicate or ineligible account. It receives 401 for invalid credentials, 413 for an oversized body, 415 for unsupported media, 422 for invalid notifications and 503 when enqueue fails. The worker leases deliveries for providers enabled in its revision. It verifies the active account link and current consent again and completes the exact lease in the same transaction as its domain writes. Completion and consent withdrawal scrub raw bodies. Replay markers follow inbox retention, which continues when providers are disabled. The worker fetches file references only through configured HTTPS bridge destinations. See [coaching inbox runbook](OPERATIONS.md).

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
| `POST` | `/v1/app-attest/challenge` | api | `AppAttestChallenge` (`501` until the token key is set; D245, D247; Conventions → App Attest) |
| `POST` | `/v1/app-attest/keys` | api | `201 AppAttestKey` (body `AppAttestKeyPost`; `501` until the token key is set) |
| `POST` | `/v1/app-attest/token` | api | `AppAttestToken` (body `AppAttestTokenPost`; `501` until the token key is set) |
| `GET` | `/v1/recovery/status` | api | `RecoveryStatus` (D291; not served yet) |
| `GET` | `/v1/recovery/checkins?from&to` | api | `[RecoveryCheckin]` |
| `GET`, `PUT`, `DELETE` | `/v1/recovery/checkins/{date}` | api | `RecoveryCheckin`, `RecoveryCheckin` (body `RecoveryCheckinPut`), `204` |
| `PUT`, `DELETE` | `/v1/workouts/{id}/rpe` | api | `Workout` (body `WorkoutRPEPut`), `204` (D293; not served yet) |
| `GET` | `/v1/me/connections` | api | `ConnectionsResponse` (Connections, D257; not served yet) |
| `POST` | `/v1/me/connections/{provider}/start` | api | `ConnectStart` (no body) |
| `GET` | `/v1/connect/{provider}/callback` | api | `302` to `ryft://connect/{provider}?status=…` (no bearer; the provider's sign-in page calls it, not the app) |
| `POST` | `/v1/me/connections/{provider}/confirm` | api | `Connection` (body `ConnectConfirmPost`) |
| `DELETE` | `/v1/me/connections/{provider}` | api | `ConnectionDeleted` |
| `GET` | `/health/live`, `/health/ready` | all | health on Cloud Run: the probes and the synthetics (no auth; Endpoints → Health) |
| `GET` | `/healthz`, `/readyz`, `/metrics` | all | health and Prometheus for local compose (no auth; `/metrics` on port 9090, not on Cloud Run) |

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
| `workouts` | `[Workout]` | May be empty, at most **5 000** entries (`422` above, nothing of the request is stored, and the app never retries a `422`: split a larger backfill into more requests). `strain` and `also_recorded_by` are ignored on input. |
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
| `hrv_rmssd` | ms (RMSSD; Connections, from the platform PR `connect/foundation`) |
| `resting_hr` | bpm (one per day) |
| `respiratory_rate` | breaths/min |
| `spo2` | percent 0–100 |
| `vo2max` | ml/kg/min |
| `active_energy` | kcal |
| `steps` | count |

**`hrv_rmssd`** is the HRV a direct connection delivers (Connections). It is never the same number as
`hrv_sdnn`: the two are different measures of the same thing, and scoring keeps them apart (a baseline per
metric, `coverage.hrv_metric`). `POST /v1/sync` accepts it too, additively, from the platform PR
`connect/foundation` on; a server before it counts such a sample as `ignored.samples.unknown_type` (never
an error). Apple Health has no RMSSD type, so the app has nothing to send as `hrv_rmssd` today: never
convert or relabel an SDNN as RMSSD.

**Reserved source names.** A `source` that starts with `ryft-direct-` is the server's own name for the
records of a direct connection. From the platform PR `connect/foundation` on, a sample or sleep segment of a
sync whose `source` starts with it is not stored and is counted as `ignored.samples.unknown_type` or
`ignored.sleep.unknown_type`,
so no Apple Health source can be mistaken for a connection or overwrite its records. HealthKit source names
never look like that; the app does nothing.

`SleepSegment`: `start`, `end` (`end > start`), `stage` ∈ `inbed | asleep | core | deep | rem | awake`
(HealthKit's vocabulary; `asleep` = unspecified stage), `source`.

Uploads are idempotent: samples are unique on `(type, start, end, source)`, sleep
segments on `(start, end, stage, source)`, workouts on `id`; a sample or sleep segment
that carries an `id` is also unique on `(user, id)`. Re-sending a page is harmless.

When consent enforcement is on, both the raw record transaction and the later apply transaction require the active grant that admitted the request. The server returns `403 consent_required` if that grant is withdrawn between phases. The server keeps records that already committed with active consent and stops the later deletions, cursor update and scoring queue. Send a fresh request after consent is granted again.

Optional `body_mass` samples require the protected pilot's `nutrition.enabled` flag. Outside that cohort, sync reports them as `ignored.samples.unknown_type` and skips an explicit body-mass cursor. Other streams use the normal HealthKit consent rules.

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

<!-- type: model.SyncResponse -->
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
| `unknown_type` | has a sample `type` or sleep `stage` the server does not know, or (from the platform PR `connect/foundation`) a reserved `source` starting with `ryft-direct-` (above). Ignored instead of failing the whole sync, so an app that reads a new HealthKit type keeps syncing. |
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
  `unknown_id`. Delete the session instead (`DELETE /v1/sessions/{id}`). Likewise, re-uploading a workout with a `ryft-session` workout's id changes nothing: it is a no-op, counted as `duplicate`.
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
  `503` with `Retry-After: 5` and the records of the same request are stored (they are idempotent). The same
  `503` covers a conflict between two concurrent syncs of one user (a deadlock or serialization failure); nothing is half-applied and a re-send is harmless. Send the same
  request again; deletions that already happened come back as `unknown_id`. Keep a deletion until
  a `200` carried it.
- **Logs** hold counts, never ids.

Example request (a rewritten workout and a deleted resting heart rate):

<!-- type: model.SyncRequest -->
```json
{ "device_id": "0E6B2E54-9C6D-4F7B-8A55-4E3F3B7C1D20", "samples": [], "sleep": [], "workouts": [],
  "deleted": [ { "kind": "workout", "id": "5C3F0B9E-52B1-4E55-AE0A-7B1C2D3E4F50" },
               { "kind": "sample",  "id": "A41D7E02-6F1B-4C58-9B3A-0D5E8F7A6B21" },
               { "kind": "workout", "id": "5C3F0B9E-52B1-4E55-AE0A-7B1C2D3E4F50" } ] }
```

answers (the two distinct ids; the workout was held, the sample was already gone):

<!-- type: model.SyncResponse -->
```json
{ "samples": 0, "sleep": 0, "workouts": 0, "days_touched": ["2026-10-20", "2026-10-21"],
  "accepted": { "samples": 0, "sleep": 0, "workouts": 0 },
  "ignored": { "samples":  { "duplicate": 0, "outside_window": 0, "unknown_type": 0, "unknown_id": 1 },
               "sleep":    { "duplicate": 0, "outside_window": 0, "unknown_type": 0, "unknown_id": 0 },
               "workouts": { "duplicate": 0, "outside_window": 0, "unknown_type": 0, "unknown_id": 0 } },
  "deleted": { "samples": 0, "sleep": 0, "workouts": 1 },
  "scoring": { "queued_days": ["2026-10-20", "2026-10-21"], "expected_within_sec": 20 } }
```

A sample or sleep segment with an `id`, as the app sends it from 2026-10-31:

<!-- type: model.Sample -->
```json
{ "id": "A41D7E02-6F1B-4C58-9B3A-0D5E8F7A6B21", "type": "resting_hr", "start": "2026-10-20T00:00:00Z", "end": "2026-10-20T00:00:00Z",
  "value": 49, "unit": "bpm", "source": "Garmin Connect", "source_bundle_id": "com.garmin.connect.mobile" }
```

`days_touched` (and `scoring.queued_days`) are the local days queued for rescoring;
`scoring.expected_within_sec` is how long scoring has recently taken from a sync to the
score (the 95th percentile over every account and the last 6 hours; 30 until it knows).
`GET /v1/me/sync` says when they are done. A `\u0000` in any string is removed, not
refused (Errors → NUL). Errors: `400` bad JSON, `422` validation
(`device_id` missing, `end` before `start`, workout without `id`, more than 2 000 entries in `deleted`, more than 5 000 in `workouts`), `403` `invite_required`
/ `consent_required` (Errors above): keep the HealthKit anchors and send the same pages
again once that is resolved.

### `GET /v1/me` — user settings

<!-- type: model.User -->
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
| `sources` | `[UserSource]` | Apps and devices that sent data in the last 30 days, newest first: `name` (HealthKit source name), `bundle_id` (string?, when the app sent it), `kinds` (closed enum, sorted: `energy`, `hr`, `hrv`, `resp`, `sleep`, `spo2`, `steps`, `workout`; `hr` covers heart rate, resting heart rate and VO₂max), `last_seen` (the latest record's time), `samples_7d` (see below), `origin` (see below). `[]` when nothing arrived. |
| `notifications` | `NotificationPrefs` | Push preferences: every category (`MORNING_SCORE`, `SESSION_SCORED`, `PR`, `SYNC_STALE`) with whether it is on (default on), and `quiet_hours` (`{from, to}`, local `HH:MM`, or `null`). Change with `PATCH /v1/me/notifications`. |
| `account_hint` | `AccountHint?` | Set when this account has no data yet and the device in `X-RYFT-Device-ID` synced **another** account in the last 30 days: probably the same person signed in another way (Apple vs. email). `{"kind": "other_account_on_device", "other_provider": "email", "other_last_sync": "2026-10-20T06:58:00Z"}`: `kind` closed enum (one value so far), `other_provider` string? (`apple`, `email`), `other_last_sync` timestamp. Never an id or an email. Offer "sign in with email instead" and the support contact; the operator can merge accounts. `null` otherwise. |

**`UserSource.samples_7d`** (`{string: int}`) counts the **samples** (not sleep segments or workouts, which `kinds` covers) stored for that source with `start` in the last 7 × 24 hours, per sample `type`: the keys are the eight `Sample.type` values (`heart_rate`, `hrv_sdnn`, `resting_hr`, `respiratory_rate`, `spo2`, `vo2max`, `active_energy`, `steps`), **all present on every source, `0` when none arrived**. That is how the app tells a tracker that writes heart rate but no HRV (`hrv_sdnn: 0` next to `heart_rate` in the thousands) from one that writes both. The source is matched by `name`; the counts are after the sync's duplicate handling (each stored row once). The type keys are an open set (a later version may add one): ignore keys you do not know and treat a missing key as `0`. The counts are exact as of the response or taken from the last scoring pass (at most about 15 minutes old); do not compare them to a sync's `accepted`. The field is absent only from a platform that predates 2026-10-31; decode it as optional. There is no way to choose which source feeds which metric yet.

**`UserSource.origin`** (string, Connections; from the platform PR `connect/foundation`) says how the source's
data reached RYFT: `healthkit` (through Apple Health, uploaded by the app) or `direct` (a direct connection,
`GET /v1/me/connections`). A **closed** enum. Absent from a server before it: read absent as `healthkit`. A
`direct` source has `name` = the provider's display name (`"Garmin"`), `bundle_id: null`, its `kinds` and its
`samples_7d`, and appears once the connection has delivered (a connection without data yet is only in
`GET /v1/me/connections`). The same vendor can appear twice, once per origin (the example below): the
`healthkit` entry is what its app wrote to Apple Health, the `direct` one what the connection delivered.
`samples_7d` gains the key `hrv_rmssd` with the same release (present on every source, `0` when none), as the
open-keys rule above allows. When the connection ends, the `direct` entry is deleted with its data. The direct
entry next to the Apple Health one, in `GET /v1/me` → `sources` (an excerpt; a type-checked `json` block once the
server has the field):

```http
HTTP/2 200
content-type: application/json

{ …, "sources": [
    { "name": "Garmin", "bundle_id": null, "kinds": ["hrv", "sleep"], "last_seen": "2026-11-24T05:02:11Z", "origin": "direct",
      "samples_7d": { "heart_rate": 0, "hrv_sdnn": 0, "hrv_rmssd": 7, "resting_hr": 0, "respiratory_rate": 0, "spo2": 0, "vo2max": 0, "active_energy": 0, "steps": 0 } },
    { "name": "Garmin Connect", "bundle_id": "com.garmin.connect.mobile", "kinds": ["energy", "hr", "sleep", "steps", "workout"], "last_seen": "2026-11-24T03:02:00Z", "origin": "healthkit",
      "samples_7d": { "heart_rate": 9112, "hrv_sdnn": 0, "hrv_rmssd": 0, "resting_hr": 7, "respiratory_rate": 0, "spo2": 0, "vo2max": 0, "active_energy": 640, "steps": 1180 } } ] }
```

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
workouts, scores, devices, direct connections (Connections: every connection and what it
delivered; the provider's tokens are kept, sealed, only to end the grant at the provider)
and the profile. Then it revokes the app's Sign in with Apple
grant (if `POST /v1/me/apple-authorization` stored one), asks each connected provider once to
end its grant, and deletes the user's
Supabase Auth account with its identities and sessions. Responds `200`:

```json
{ "status": "deleted", "auth_user_deleted": true, "apple_token_revoked": true, "connections_deregistered": null }
```

| Field | Type | Notes |
| --- | --- | --- |
| `status` | string | Always `"deleted"`: the data is gone. |
| `auth_user_deleted` | bool | `false` if Supabase could not be reached. The platform retries in the background (every 15 minutes), so the app has nothing to do. |
| `apple_token_revoked` | bool? | `null` when no Apple token was stored (email accounts, or the app never sent a code). `false` if Apple could not be reached; retried in the background like `auth_user_deleted`. |
| `connections_deregistered` | bool? | Connections (from the platform PR `connect/foundation`; absent before it, read absent as `null`). `null` when the account had nothing to end (no connection and no grant waiting for its confirm, or only `revoked` connections). A grant waiting for its confirm counts as something to end. `true` when every provider ended its grant in the one attempt (at most 5 seconds in all). `false` when one was not ended: a provider could not be reached (the platform retries in the background for up to 30 days, then gives up), or another RYFT account holds that provider account live (ending the grant would end that account's connection, so only RYFT's copy is deleted; not retried). The app has nothing to do either way. The data the connections delivered is deleted either way, with the rest of the account. Same semantics as `apple_token_revoked`. |

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
`DELETE /v1/me` only. The exchanged Apple subject must match an Apple identity that
Supabase Auth links to the signed-in account.

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
| `501` | the platform is not configured for Apple revocation or Supabase linked identity checks (local development) | ignore |
| `503` | Apple could not be reached or Supabase could not verify the linked identity (`Retry-After`) | retry once within the code's five minutes |

Never log the code; it is a credential until used.

### Connections — direct sources (D257; not served yet)

A **connection** links the account to a provider's own service, so that the provider delivers the user's
data to RYFT directly instead of through Apple Health: data Apple Health does not carry (overnight HRV as
RMSSD) and the provider's own sleep record. The provider is a path parameter, `{provider}`: an id
`^[a-z][a-z0-9]{1,31}$`, an **open** set; `unknown` is reserved and never a provider id (the redirect uses it). The first is `garmin`. `fake` exists only in local compose
(`CONNECT_FAKE=on`), for development and the smoke test; production never lists it. Apple Health stays the
default source and the only one with history: a connection delivers data from the moment it is connected
on, nothing from before (`history: "none"`).

**When it is served.** The five routes, `DELETE /v1/me` → `connections_deregistered`, `User.sources[].origin`,
the sample type `hrv_rmssd` and the flag `connect.garmin` arrive with the platform PR `connect/foundation`;
`coverage` itself, with `sleep_origin` and `hrv_metric`, and the HRV metric rules arrive with the platform PR
`connect/scoring`. Until the first, every route here answers `404` without a `code` (an unknown route): hide
every Connect entry. After it, production still serves no provider until phase 2 (the provider's programme
approval, D257): `available` is `[]` and `start` answers `404 unknown_provider`. A provider is then turned on
for allow-listed testers first (flag `connect.<provider>`, `GET /v1/config`).

The provider delivers the data to ingest, on a route outside `/v1/` that is not part of this contract; the app
never calls it. A delivery is processed within seconds and scored like a sync (the silent refresh push follows,
Push notifications).

**The flow.**

1. `GET /v1/me/connections`. Offer Connect for a provider in `available` that this build has copy and artwork
   for (ignore any other id). `accepting: false`: the provider is full right now; say so and do not call
   `start`.
2. Show the data card (what RYFT receives and keeps, and that disconnecting deletes it: below), then
   `POST /v1/me/connections/{provider}/start` → `authorize_url`.
3. Open `authorize_url` in `ASWebAuthenticationSession` with the callback scheme `ryft`. The user signs in at
   the provider and grants access; the provider sends the browser to `GET /v1/connect/{provider}/callback` on
   api, which answers `302` to `ryft://connect/{provider}?status=…`, and the session hands that URL to the app.
   A session the user cancels ends with no URL: nothing to do (the flow expires on its own).
4. `status=ok`: at once `POST /v1/me/connections/{provider}/confirm` with its `ticket`, from the same account.
   Only the confirm connects. The other statuses: the callback's table below.
5. Data then arrives on the provider's schedule. `last_data_at` says when RYFT last stored a record.

**Consent.** `start` and `confirm` need consent to the policy in force (`403 consent_required`; on both
`CONSENT_ENFORCE=warn` acts as `on`, as for the import). A delivery that arrives while the account's consent
is not to the policy in force (a new `privacy_policy_version` not yet agreed to, or a withdrawn consent) is
**dropped, not stored**, and because there is no history it cannot be fetched later. So when
`privacy_policy_version` changes, ask for consent at the next launch, before anything else. A withdrawn
consent does not end the connection, and stored data stays readable as today.

**Statuses** (`Connection.status`, a **closed** enum of three):

| `status` | Means | App |
| --- | --- | --- |
| `connected` | Linked; the provider delivers. | Show it connected, with `last_data_at` and Disconnect. |
| `needs_reauth` | The provider's grant needs renewing: it could not be refreshed, or a permission RYFT needs was withdrawn at the provider. The stored data stays. | Show Reconnect: the same flow from `start`; the confirm replaces the grant. |
| `revoked` | Ended outside RYFT: the user removed RYFT at the provider, or the same provider account was connected to another RYFT account. RYFT has deleted what it received through it (below). `provider_user_id` is `null` and `permissions` `[]`. Listed for 90 days after it ended, then gone. | Say it was removed at the provider; Connect starts over. |

A flow that was started but not confirmed is not a connection and is never listed.

**What happens to stored data when a connection ends** (D257). **RYFT deletes everything it received
through that connection, and rescores the days it touched.**

- **When:** the user disconnects in RYFT (`DELETE /v1/me/connections/{provider}`); the user removes RYFT at
  the provider (`revoked`); the same provider account is connected to another RYFT account (`revoked` on the
  first); a reconnect confirms a **different** provider account (the old connection is replaced, `confirm`
  below); the RYFT account is deleted (`DELETE /v1/me`).
- **Deleted:** every sample and sleep record received through the connection; the records still waiting to
  be processed; its entry in `User.sources` (`origin: "direct"`); the provider's tokens, once the provider has
  confirmed the deregistration (retried in the background for up to 30 days, then given up).
- **Rescored:** every day that held that data, so no stored score or baseline keeps a value from it.
  `GET /v1/me/sync` → `pending_days` lists them until scoring is done.
- **Kept:** the same vendor's data that arrived through Apple Health (it is HealthKit data, under the Health
  permission, not this connection); every other source's data; scores, journal and Train data from them; a
  `revoked` connection's entry (provider, status and times; no token, no provider user id, no permissions)
  for 90 days.
- **Not at once:** the platform's database backups, until they expire.
- `needs_reauth` and a withdrawn consent end nothing.

**Answers common to the four bearer routes.** They are api routes under the Conventions: the bearer, the
per-address and per-user limits, the App Attest check (`403 app_attest_required` under `on`), the beta gate
(`403 invite_required`), 64 KiB bodies with unknown fields `400`, `X-Request-ID`, `401`, `503` with
`Retry-After`. **Except** `DELETE /v1/me/connections/{provider}`, which is exempt from the App Attest check
and the beta gate, like `DELETE /v1/me/consent`: ending a connection and deleting its data works from any
signed-in client, with or without beta access. Their `error` texts never contain a ticket, a state, a URL or a provider value.

#### `GET /v1/me/connections` — connections and what can be connected

The response also contains `providers`, a catalog of known direct providers with their availability for the caller. `partner_onboarding` means RYFT has not configured the provider, `unavailable` means the caller cannot start a link, `full` means new links are at capacity, and `available` permits a link. Consult `connections` for an existing account link and `available` for the capabilities and new-link capacity of an offered provider. The Apple Health connection remains managed on the device.

<!-- type: model.ConnectionsResponse -->
```json
{"connections":[],"available":[],"providers":[{"provider":"garmin","availability":"partner_onboarding"},{"provider":"suunto","availability":"partner_onboarding"}]}
```


No consent needed (a read). The exchange (the body follows):

```http
GET /v1/me/connections HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Token: <the token>

HTTP/2 200
content-type: application/json
x-request-id: 01a1502c-6d41-7a3e-8c52-3f4e5a6b7c8d

{ "connections": [
    { "provider": "garmin", "status": "connected", "connected_at": "2026-11-23T07:10:00Z",
      "last_data_at": "2026-11-24T05:02:11Z", "provides": ["hrv", "sleep"], "history": "none",
      "provider_user_id": "a1b2c3d4e5f6", "permissions": ["read_sleep", "read_hrv"] } ],
  "available": [
    { "provider": "garmin", "provides": ["hrv", "sleep"], "history": "none", "accepting": true } ] }
```

A connection removed at the provider, while the provider is full (a `revoked` connection is not live, so
reconnecting meets the cap like a new one):

```http
HTTP/2 200
content-type: application/json

{ "connections": [
    { "provider": "garmin", "status": "revoked", "connected_at": "2026-11-23T07:10:00Z",
      "last_data_at": "2026-11-30T05:01:40Z", "provides": ["hrv", "sleep"], "history": "none",
      "provider_user_id": null, "permissions": [] } ],
  "available": [
    { "provider": "garmin", "provides": ["hrv", "sleep"], "history": "none", "accepting": false } ] }
```

Production until phase 2, and every account the flag is off for: `{ "connections": [], "available": [] }`.

`ConnectionsResponse`:

| Field | Type | Notes |
| --- | --- | --- |
| `connections` | `[Connection]` | The account's connections, one per provider at most, in `connected`, `needs_reauth` or `revoked`. `[]` when none. Listed whatever `available` says (a provider turned off since keeps its entry). |
| `available` | `[AvailableConnection]` | The providers this account may connect now, connected or not: served by this server, its flag `connect.<provider>` on for the account, and configured. `[]` when none. **Authoritative**: the flag alone does not mean a provider is served. |

`Connection` (also the answer of `confirm`):

| Field | Type | Notes |
| --- | --- | --- |
| `provider` | string | The provider id (open; `garmin`). |
| `status` | string | `connected`, `needs_reauth`, `revoked` (closed; the Statuses table above). |
| `connected_at` | timestamp | When the link was confirmed; set again by a reconnect's confirm. |
| `last_data_at` | timestamp? | When RYFT last stored a record from this connection. `null` until the first one. |
| `provides` | `[string]` | What it delivers, in the `User.sources[].kinds` vocabulary (closed: `hrv`, `sleep` today), sorted. |
| `history` | string | `none`: only data from the connection on. An **open** set for later tiers: read a value you do not know as `none`. |
| `provider_user_id` | string? | The provider's id of the user's account there, as the provider gave it (1–128 characters), kept to tell provider accounts apart (and part of the account's data, GDPR Art. 15). `null` on a `revoked` connection. Show it if the screen wants to; do not interpret it, never log it. |
| `permissions` | `[string]` | What the user granted at the provider, in the provider's own words (an **open** set, at most 32). `[]` on a `revoked` connection. Show them, don't interpret them: a missing permission RYFT needs shows as `needs_reauth`, not here. The values in the examples are illustrative. |

`AvailableConnection`:

| Field | Type | Notes |
| --- | --- | --- |
| `provider`, `provides`, `history` | | As in `Connection`. |
| `accepting` | bool | `false` while the provider's user cap is full: `start` would answer `409 connection_limit`. The cap never counts this account's own open states, grant waiting for its confirm or connection, so its own flow never makes it `false`. Always `true` for a provider this account has a `connected` or `needs_reauth` connection to (a reconnect never meets the cap). |

| Status | `code` | When | App should |
| --- | --- | --- | --- |
| `200` | — | as above | — |
| `404` | — | a server before `connect/foundation` (an unknown route) | hide every Connect entry; ask again at the next launch |
| others | | the common answers above | as on every route |

#### `POST /v1/me/connections/{provider}/start` — begin a connection

No body (an empty body or `{}`). After the common checks, in this order: consent to the policy in force
(`403 consent_required`, `warn` acts as `on`), the provider (`404 unknown_provider`), the user cap
(`409 connection_limit`). Its own budget per account: bursts of 5, then one a minute (`429`). The exchange
(the body follows):

```http
POST /v1/me/connections/garmin/start HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Token: <the token>

HTTP/2 200
content-type: application/json
x-request-id: 01a1502d-1f72-7b05-9d63-4a5b6c7d8e9f

{ "authorize_url": "https://provider.example/oauth/authorize?client_id=…&state=…&code_challenge=…&code_challenge_method=S256&redirect_uri=https%3A%2F%2Fapi.ryftlab.de%2Fv1%2Fconnect%2Fgarmin%2Fcallback",
  "expires_at": "2026-11-23T07:20:00Z" }
```

`ConnectStart`:

| Field | Type | Notes |
| --- | --- | --- |
| `authorize_url` | string | `https://`, the provider's sign-in and consent page with RYFT's one-time `state` and PKCE challenge (the example's host and parameters are placeholders). **Opaque:** open it as it is, in `ASWebAuthenticationSession` (callback scheme `ryft`); never parse, show, log or store it. Single use. |
| `expires_at` | timestamp | 10 minutes after the start. A callback after it is `status=expired`. Start again rather than reuse an old URL; don't compare it with the device's clock. |

- Each start makes a new flow and reserves a place under the provider's cap for its 10 minutes. At most 3 open
  states per account and provider: a fourth start cancels the oldest, whose callback then answers `expired`.
- **The cap never counts the caller's own** open states, grant waiting for its confirm, or live connection: a
  second start, the callback and the confirm of the account's own flow never meet the cap because of the
  account's own reservation, and neither does `accepting`. Only other accounts fill it.
- **A reconnect** (a start while `connected` or `needs_reauth`) is allowed and never meets the cap. Until its
  confirm, the live connection, its grant and its data stay exactly as they are, and `GET /v1/me/connections`
  shows no change. The confirm replaces it (below).

| Status | `code` | When | App should |
| --- | --- | --- | --- |
| `200` | — | as above | open `authorize_url` |
| `403` | `consent_required` | no consent to the policy in force (`warn` acts as `on`) | the consent screen, `POST /v1/me/consent`, then start again |
| `404` | `unknown_provider` | the provider is not served here, or not enabled for this account (it does not say which) | hide its Connect entry; refresh `GET /v1/me/connections` |
| `404` | — | a server before `connect/foundation` | hide every Connect entry |
| `409` | `connection_limit` | the provider's user cap is full (other accounts only: the caller's own open states and grants never count) | say the provider is full for now; no automatic retry; `available[].accepting` is `false` until there is room |
| `429` | — | more than 5 starts in a burst, then one a minute per account (or the common limits) | wait `Retry-After` |
| others | | the common answers above | as on every route |

#### `GET /v1/connect/{provider}/callback` — the provider's redirect (not called by the app)

The provider's page sends the browser here; the redirect URI registered with the provider is
`https://api.ryftlab.de/v1/connect/{provider}/callback`. **No bearer, no App Attest token, no beta or consent
gate:** the only `/v1/` route without them, so it never answers `401` or `403`, and the app never calls it.
Query: `state`, and `code` or `error` (the provider's). The per-address limit applies (its plain-text `429`
would show in the sign-in sheet; normal use never meets it).

It always answers `302`, with no body, also for an unknown provider and a missing, used or old `state`:

```http
GET /v1/connect/garmin/callback?state=…&code=… HTTP/2
Host: api.ryftlab.de

HTTP/2 302
location: ryft://connect/garmin?status=ok&ticket=9f2c4e1a7b3d5f6081a2c3d4e5f60718
cache-control: no-store
referrer-policy: no-referrer
```

The redirect:

| Part | Rule |
| --- | --- |
| `ryft://connect/{provider}` | The path's provider id when it is a well-formed id (served or not); anything else is replaced by `unknown`, which is reserved and never a provider id. A provider the app did not start a flow for: treat it as `error`. |
| `status` | `ok`, `denied`, `expired` or `error` (**closed**). |
| `ticket` | With `ok` only: 32 lowercase hex characters, single use, for `confirm` within 10 minutes. Worthless without the bearer of the account that started the flow; still, never log or store it beyond the confirm. |
| anything else | Ignore query parameters you do not know. |

The URL never carries a token, the provider's code, the provider's user id or a health value.

| `status` | Means | App should |
| --- | --- | --- |
| `ok` | The provider granted access; RYFT holds the grant, but the account is not connected yet. | `POST /v1/me/connections/{provider}/confirm` with the `ticket`, at once, from the account that started. |
| `denied` | The user declined at the provider, or did not grant a permission RYFT needs (that grant is handed back). | Not connected. Say RYFT needs that access; Connect may be tried again. |
| `expired` | The flow is unknown, used, cancelled by a later start, or older than 10 minutes (indistinguishable on purpose). | Start again. |
| `error` | Anything else: the provider could not be reached or refused the exchange (not retried), the cap filled meanwhile, the account was deleted or its consent withdrawn meanwhile, an unknown provider. | Not connected. Offer to try again; refresh `GET /v1/me/connections`. |

#### `POST /v1/me/connections/{provider}/confirm` — finish a connection

After the common checks: consent to the policy in force (`warn` acts as `on`), the provider as for `start`
(served, and its flag `connect.<provider>` on for the account), then the ticket. Request `ConnectConfirmPost`, response `200 Connection` (`status` `connected`). The exchange:

```http
POST /v1/me/connections/garmin/confirm HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Token: <the token>
content-type: application/json

{ "ticket": "9f2c4e1a7b3d5f6081a2c3d4e5f60718" }

HTTP/2 200
content-type: application/json
x-request-id: 01a1502e-3a83-7c16-8e74-5b6c7d8e9fa0

{ "provider": "garmin", "status": "connected", "connected_at": "2026-11-23T07:11:02Z",
  "last_data_at": null, "provides": ["hrv", "sleep"], "history": "none",
  "provider_user_id": "a1b2c3d4e5f6", "permissions": ["read_sleep", "read_hrv"] }
```

- `ticket` (string, required) is the redirect's, unchanged. It must come from a flow this account started for
  this provider, and the confirm must come within 10 minutes of the callback.
- **Repeats.** The same ticket again answers `200` with the connection, until 10 minutes after the callback,
  as long as that connection is still this account's: once it has ended (moved to another RYFT account,
  replaced, removed at the provider or disconnected: `revoked` or gone) a repeat answers
  `409 connect_ticket_invalid`. After the 10 minutes a repeat answers `410 connect_expired`, even though the
  account may be connected. So after a network error, send the
  confirm again once; on a `409` or `410` after an earlier attempt, `GET /v1/me/connections` tells whether it
  is connected.
- **A refusal for consent** (`403 consent_required`) leaves the ticket usable: after the consent, confirm again
  with it within the 10 minutes, else start again.
- **What the confirm replaces.** The provider account the user signed in to decides:
  - the account's live connection is with the **same** provider account: it gets the new grant and
    permissions, `connected` again; its data stays;
  - the account's live connection is with a **different** provider account: that connection is disconnected
    first, **its data deleted** and its days rescored (as `DELETE` below), then the new one is connected;
  - the provider account is connected to **another RYFT account**: that connection ends there (`revoked`, its
    data deleted, its days rescored), and it is connected to this account. A provider account belongs to one
    RYFT account at a time.

| Status | `code` | When | App should |
| --- | --- | --- | --- |
| `200` | — | connected, or a repeat within 10 minutes of the callback while that connection is still this account's | show it connected |
| `400` | — | `ticket` missing, or an unknown body field | a client bug: log `request_id`, don't resend unchanged |
| `403` | `consent_required` | no consent to the policy in force (`warn` acts as `on`) | the consent screen, then confirm again with the same ticket within the 10 minutes, else start again |
| `404` | `unknown_provider` | the provider is not served, or its flag is off for the account (turned off since `start`). The grant waiting for this confirm is then handled as an expired one: dropped, and ended at the provider unless another RYFT account holds that provider account live. Nothing of a live connection of this account changes. | not connected; hide its Connect entry |
| `409` | `connect_ticket_invalid` | the ticket is unknown, another account's, or for another provider (also an expired ticket the server has already cleaned up); or a repeat within the 10 minutes of a confirm that worked, once that connection has ended (moved to another RYFT account, replaced, removed at the provider or disconnected). Nothing changes. | don't resend it; `GET /v1/me/connections`; not connected: start again |
| `409` | `connection_limit` | other accounts filled the cap between `start` and `confirm` (rare; the caller's own flow never counts) | as for `start` |
| `410` | `connect_expired` | more than 10 minutes after the callback; the grant waiting for this confirm is dropped | `GET /v1/me/connections` (a repeat of a confirm that worked lands here too); not connected: start again |
| others | | the common answers above | as on every route |

#### `DELETE /v1/me/connections/{provider}` — disconnect

No body; no consent, no app token and no beta access needed (it only removes: App Attest → Exempt, and the
`invite_required` row's exceptions). It works for any well-formed provider id, whether or not the provider is
served or its flag is on now, so a connection can always be ended. In one transaction it deletes the
connection and everything received through it (**What happens to stored data when a connection ends**,
above), cancels a flow of this account for the provider that waits for its confirm, and queues the touched
days for rescoring; then it asks the provider, once (at most 5 seconds), to end the grant. The exchange:

```http
DELETE /v1/me/connections/garmin HTTP/2
Host: api.ryftlab.de
Authorization: Bearer eyJhbGciOiJFUzI1NiIsImtpZCI6…
X-RYFT-App-Token: <the token>

HTTP/2 200
content-type: application/json
x-request-id: 01a1502f-4b94-7d27-8f85-6c7d8e9fa0b1

{ "status": "disconnected", "deregistered": true, "data_deleted": true }
```

The provider could not be reached (retried in the background):

```http
HTTP/2 200
content-type: application/json

{ "status": "disconnected", "deregistered": false, "data_deleted": true }
```

A second call (nothing left to end):

```http
HTTP/2 200
content-type: application/json

{ "status": "disconnected", "deregistered": null, "data_deleted": true }
```

`ConnectionDeleted`:

| Field | Type | Notes |
| --- | --- | --- |
| `status` | string | Always `"disconnected"`. |
| `deregistered` | bool? | `true`: the provider ended the grant (or no longer knew it). `false`: the grant was not ended at the provider, for one of two reasons: the provider could not be reached (RYFT retries in the background for up to 30 days), or another RYFT account holds the same provider account live (ending the grant would end that account's connection, so RYFT only deletes its own copy; not retried). Either way the app has nothing to do, and nothing more is stored for this account (a delivery for an ended connection is dropped). `null`: there was nothing to end (no connection and no grant waiting for its confirm, or `revoked`). A grant waiting for its confirm (a flow at `status=ok`, not yet confirmed) counts as something to end, alone or next to a connection: `true` or `false` as above. |
| `data_deleted` | bool | Always `true`: the data received through the connection is gone when this answers. |

Idempotent: a second call answers the same shape with `deregistered: null`. Afterwards refresh the touched
days (`GET /v1/me/sync` → `pending_days`); the night and HRV from the connection are gone from them.

| Status | `code` | When | App should |
| --- | --- | --- | --- |
| `200` | — | as above | show it disconnected |
| `404` | — | the provider id is not well-formed, or a server before `connect/foundation` | drop the local entry |
| `503` | — | nothing was deleted (`Retry-After`) | send it again |
| others | | the common answers above | as on every route |

### `GET /v1/config` — remote config

Feature flags, evaluated for the caller. Load on launch and again after `ttl_sec`; keep
the last answer (and compiled-in defaults) when offline.

<!-- type: model.RemoteConfig -->
```json
{ "flags": { "train.enabled": true, "sleep.planner": true, "today.redesign": false,
             "coach.enabled": false, "push.enabled": false, "physiology.card": false,
             "train.import": false, "analytics.enabled": true, "connect.garmin": false },
  "evaluated_at": "2026-11-20T06:12:00Z", "ttl_sec": 21600,
  "privacy_policy_version": "2026-10-26", "privacy_policy_url": "https://ryftlab.de/privacy",
  "analytics": { "host": "https://eu.i.posthog.com", "project_key": "phc_…" },
  "app_attest": "off" }
```

Without a project key on the server, `analytics` is `null` (the field is always present); this
server runs App Attest in `warn`:

<!-- type: model.RemoteConfig -->
```json
{ "flags": { "analytics.enabled": false }, "evaluated_at": "2026-11-20T06:12:00Z", "ttl_sec": 21600,
  "privacy_policy_version": "2026-10-26", "privacy_policy_url": "https://ryftlab.de/privacy",
  "analytics": null, "app_attest": "warn" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `flags` | `{string: bool}` | Every flag the platform has, on or off for this user. Keys are **open**: ignore unknown ones, and use the compiled default for one that is missing. |
| `evaluated_at` | timestamp | When this answer was computed. |
| `ttl_sec` | int | How long to use it: 21600 (6 h). |
| `privacy_policy_version` | string | The privacy policy in force. Consent (`POST /v1/me/consent`) is given to this version; when it changes, every user consents again before the next sync. |
| `privacy_policy_url` | string | Where it is published, in English (the site is English only since 2026-11-16; `/de/privacy` answers 301 to `/privacy`). |
| `analytics` | `AnalyticsConfig?` | Where the app sends its product analytics (PostHog EU Cloud, D231): `{host, project_key}`, or `null` while the server has no project key. Independent of the flag `analytics.enabled`: the app combines the two with the user's opt-in (below). |
| `analytics.host` | string | The ingestion origin, `https://eu.i.posthog.com` by default; always `https://`, no path. |
| `analytics.project_key` | string | PostHog's project API key (`phc_…`), a client key. It is served here, not compiled into the app, because the app's repository is public. Don't log it, and don't persist it beyond the cached config. |
| `app_attest` | string | The server's App Attest mode (AA2, D247), always present: `"off"`, `"warn"` or `"on"`, a **closed** enum (Conventions → App Attest → When the server checks); the same for every account. The app may use `"off"` to skip the challenge probe; a `501` or a `404` without a `code` from `POST /v1/app-attest/challenge` stays authoritative in every mode. Under `"on"` this read itself needs a token, so an app without one learns `on` from `403 app_attest_required`. A server before D247 omits the field, and a value the app doesn't know is treated as missing: probe as before. |

**Analytics: when the app sends anything.** Only while all three hold: `flags["analytics.enabled"]`
is `true`, `analytics` is not `null`, and the user opted in (off until they do). Send to
`analytics.host` with `analytics.project_key` from the newest answer. When any of the three stops
holding (the flag off, `analytics` null, the opt-in withdrawn), stop sending and drop what is queued.
The event list, the pseudonymous identifier and the opt-in screen are the app's (the analytics
plan, decisions 4–6); no IP is stored (D231).

A flag is on for a user when it is enabled and the user is on its allow-list or in its
first `rollout_pct` of 100 buckets: bucket = the first four bytes of SHA-256 of
`"<key>:<user id>"` (the lowercase UUID), big-endian, modulo 100. The bucket is stable per
user and flag, so raising a rollout only adds users. The operator sets flags
(`OPERATIONS.md` → Feature flags), not the app. Shipped with:

| Key | Default |
| --- | --- |
| `train.enabled`, `sleep.planner` | on (100 %) |
| `today.redesign`, `coach.enabled`, `push.enabled`, `physiology.card`, `train.import`, `analytics.enabled`, `connect.garmin` | off (enabled, 0 %: on only for allow-listed testers) |
| `nutrition.enabled` | off (enabled at 0% with the existing protected pilot allow-list) |
| `nutrition.diary_enabled` | off (disabled at 0% until the diary cohort is authorized) |
| `recovery.checkin` | off (enabled, 0 %: on only for allow-listed testers, and only after a published privacy version names the check-in; no percentage rollout without a new decision; from the implementation PR, D295, decided 2026-11-27) |

`train.import` is the switch for the training import (`POST /v1/train/import`): the app shows the import only while it is on, and the endpoint checks it too, so an account it is off for gets `403 import_not_enabled` whatever the app does. It shipped dark (enabled, 0 %: testers by the allow-list); since 2026-11-22 it is on for every account (100 %, D250), so `403 import_not_enabled` means the operator has turned it off again.

`analytics.enabled` is the switch for the app's product analytics (`analytics` above). It ships dark the same way: testers by the allow-list, the kill switch by `enabled: false`. No endpoint checks it; the app does.

`recovery.checkin` (D295; from the no-watch recovery implementation PR, not served yet) gates the morning check-in and the session RPE ([Recovery without a watch](#recovery-without-a-watch--check-ins-and-session-rpe-d291d295-not-served-yet)): while it is off, `PUT /v1/recovery/checkins/{date}`, `GET /v1/recovery/status` and `PUT /v1/workouts/{id}/rpe` answer `404` without a `code`, and `CoachingBriefing.recovery` is `null`; the reads of stored check-ins and both `DELETE`s keep working. Turning it off stores nothing new and deletes nothing. The endpoints check it too, so the app shows the check-in and the RPE prompt only while it is on.

`connect.garmin` (Connections, D257; from the platform PR `connect/foundation`) gates the direct connection to the provider `garmin`: `GET /v1/me/connections` → `available` lists the provider and `POST /v1/me/connections/{provider}/start` and `/confirm` accept it only while it is on for the account (`404 unknown_provider` otherwise). A confirm while it is off answers `404 unknown_provider`, and the grant waiting for that confirm is handled as an expired one: dropped, and ended at the provider unless another RYFT account holds that provider account live. It ships dark (enabled, 0 %): testers by the allow-list first. One flag per provider, `connect.<provider>`; `fake` has none (compose only). **The flag on does not mean the provider is served:** in production no provider is served until phase 2, so decide what to show by `available`, not by the flag; the app may use the flag only to skip the `GET /v1/me/connections` call while it is off. Turning it off hides the provider from `available` and refuses new starts; existing connections stay, and `DELETE /v1/me/connections/{provider}` still ends them.

### `PATCH /v1/me/notifications` — push preferences

Body `NotificationPatch`, both fields optional: `categories` (`{category: bool}`, merged
into the stored ones; the four categories above), `quiet_hours` (`{"from": "22:30", "to":
"07:00"}`, local times; `from` later than `to` spans midnight, equal times mean none;
`null` clears them, leaving the field out keeps them). Returns `NotificationPrefs`:

<!-- type: model.NotificationPrefs -->
```json
{ "categories": { "MORNING_SCORE": true, "SESSION_SCORED": true, "PR": false, "SYNC_STALE": true },
  "quiet_hours": { "from": "22:30", "to": "07:00" } }
```

`422` for an unknown category or a time that is not `HH:MM`; `400` for unknown fields.

### `POST /v1/devices/{id}/push` — register for push

`{id}` is the install's device ID, the same UUID the app sends as `device_id` with
`POST /v1/sync`. Body `PushRegistration`:

<!-- type: model.PushRegistration -->
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
| `MORNING_SCORE` | Today's day is **final** (`score.coverage.status`) with a recovery and a night's sleep for the first time: not on a provisional scoring (a 03:00 sync of half a night sends nothing), but when the night is complete or at 10:00 local, whichever comes first; if sleep only arrives after the 10:00 ceiling, when it does. Once a day; during quiet hours it waits until they end, the score as it is then. | `MORNING_SCORE_TITLE` (recovery `82%`) · `MORNING_SCORE_BODY_BALANCED`, `_STRAINED`, `_DEPLETED` or `_CALIBRATING` by `state` (readiness) | `https://ryftlab.de/day/<day>` |
| `SESSION_SCORED` | A completed strength session has its load (scoring has run), within 12 h of its end. The first one each day; not during quiet hours. | `SESSION_SCORED_TITLE` (session title) or `SESSION_SCORED_TITLE_UNTITLED` · `SESSION_SCORED_BODY` (strain), `_PR` (strain) or `_PRS` (strain, number of records) | `https://ryftlab.de/train/session/<id>` |
| `PR` | Instead of `SESSION_SCORED` when that category is off and the session set a record. | `PR_TITLE` (exercise name) · `PR_BODY_E1RM`, `_MAX_WEIGHT`, `_MAX_REPS` or `_MAX_VOLUME` (value) | `https://ryftlab.de/train/session/<id>` |
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
  "ryft": { "category": "MORNING_SCORE", "day": "2026-10-19", "url": "https://ryftlab.de/day/2026-10-19" } }
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

<!-- type: model.ConsentPost -->
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
used-up code (one message for all three), and, since 2026-11-05, for a **revoked** code and for a code
**bound to an email address** that is not the account's confirmed email, and for a code this account has **already
redeemed** (an account whose access the operator removed cannot redeem the same multi-use code again): one message
and one status for every refusal, so the answer reveals nothing about a code. At most 5 attempts per user and hour (`429`).
An account that already has access gets `204` without using the code. Only for a bound code, `503` with
`Retry-After: 5` (and no `code`) when the account's email cannot be checked right now: send the same request
again. The request and the app's handling do not change (Admin routes → Invites for a person has the rules).

### `GET /v1/me/sync` — what the server has, and what it is doing with it

<!-- type: model.SyncStatus -->
```json
{ "gate": null, "last_sync_at": "2026-10-21T06:58:12Z", "last_sync_device": "iPhone",
  "latest": { "hr": "2026-10-21T06:55:00Z", "hrv": "2026-10-21T04:10:00Z", "sleep": "2026-10-21T06:41:00Z",
              "steps": "2026-10-21T06:50:00Z", "workout": "2026-10-20T18:45:00Z" },
  "today": { "day": "2026-10-21", "state": "pending", "scored_at": null, "model_version": "2026.10.1" },
  "pending_days": ["2026-10-21"], "expected_within_sec": 20, "resync_requested_at": null,
  "freshness": "scores_pending", "scores_pending_since": "2026-10-21T06:58:12Z",
  "sources": [ { "source": "healthkit", "last_sync_at": "2026-10-21T06:58:12Z", "status": "ok", "reason": null } ] }
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
| `sources`, `freshness`, `scores_pending_since` | | D297, D300: **The sync button** below. |

Cheap to call (cached 10 s per user). After a sync, poll it every `expected_within_sec`
until `pending_days` is empty, or wait for the silent refresh push (Push).

#### The sync button: `sources`, `freshness`, `scores_pending_since` (D297, D300)

The home screen's sync button (syncing, up to date, stale, error; a long press lists each source's last
sync) reads this route. It gains three fields, additively. The server serves all three, always. An older
server (a build from before 2026-11-27) leaves them **absent**: decode all three as optional, and read an
absent `freshness` as `scores_pending` when `pending_days` holds a day of the last 30, else `current` (an
older server's approximation of the rule below).

| Field | Type | Notes |
| --- | --- | --- |
| `sources` | `[SyncSource]` | One entry per way data reaches RYFT, `healthkit` first, then the direct connections by `source`. Never `null`. |
| `freshness` | string | `current` or `scores_pending`, a **closed** enum. `scores_pending`: the server holds data that today's scores and readiness do not include yet. That is: `pending_days` holds today or yesterday (local), or a day of the last 30 that was queued in the last 10 minutes (scoring a past day rescores the days after it, today included); **or** a `sources` entry is `pending`. `current` otherwise. An older pending day (a long recompute, a stuck day) stays in `pending_days` but does not hold the button. |
| `scores_pending_since` | timestamp? | While `scores_pending`: when the earliest of that waiting work was queued or received. A day queued again (another sync of it) restarts its time. `null` while `current`. |

How long the work takes: a sync's or a connection's days are scored in seconds (`expected_within_sec`).
**Not tracked:** for accounts with Nutrition-to-Recovery, the morning N2R baseline, and the coaching briefing
built on it, follow the scores within one 15-minute pass. The coaching pilot's partner deliveries
(Training-to-Nutrition coaching) are processed by the same pass. `freshness` does not wait for these. In
the pilot, the briefing can lag the green button by up to 15 minutes. Reload it at the next foreground or
silent push, as today.

`SyncSource`:

| Field | Type | Notes |
| --- | --- | --- |
| `source` | string | `healthkit`, or a provider id of Connections (`garmin`, later `suunto`): an **open** set, ignore an id this build has no copy for. |
| `last_sync_at` | timestamp? | `healthkit`: the newest accepted `POST /v1/sync` of any of the account's devices, the same time as `last_sync_at` above. A provider: when RYFT last stored a record from it (`Connection.last_data_at`). `null`: nothing yet. |
| `status` | string | `ok`, `pending`, `error`, a **closed** enum (below). |
| `reason` | string? | Why, with `error`: an **open** set, `null` otherwise. Today `needs_reauth` (a provider), `consent_required` and `invite_required` (`healthkit`). Show a generic error for a reason you don't know. |

| `status` | `healthkit` | A provider |
| --- | --- | --- |
| `ok` | The server accepts this account's syncs. | `connected`, and nothing it delivered is still waiting to be stored. |
| `pending` | Never sent: an upload in flight is the app's own state. | `connected` or `needs_reauth`, and a delivery has arrived that ingest has not stored yet (usually seconds). |
| `error` | A gate refuses this account's syncs (`gate` is set **and** enforced; in `warn` mode syncs pass and the status stays `ok`). `reason` is the gate's `code`. | `needs_reauth`: the provider's grant needs renewing (Connections → Statuses). `reason: "needs_reauth"`. |

**Which sources are listed.** `healthkit` always, also before the first sync (`last_sync_at: null`,
`status: "ok"`). A provider when all three hold: the account has a connection to it that is `connected` or
`needs_reauth`, this server serves the provider, and its flag `connect.<provider>` is on for the account. So
until Connections is served and its flag is on for the account, Garmin and Suunto are **absent**, never an
`error`. A `revoked` connection is absent too: the Connect screen says it was removed. The coaching pilot's
partner links (Training-to-Nutrition coaching) are not listed as sources.

**What the app shows** (the server reports times and work in progress, never a colour; the 30-minute rule
is the app's):

1. **Syncing** while the app's own `POST /v1/sync` is in flight.
2. **Error** when the app's last upload failed and has not succeeded since, or any entry of `sources` is
   `error` (the long press names it, with Reconnect for `needs_reauth`).
3. **Syncing** while `freshness` is `scores_pending` and `scores_pending_since` is less than 3 minutes old.
4. **Up to date** when `freshness` is `current`, no sync of the app is still waiting for its scores (After
   a sync, below), and the newer of the app's last successful upload and `last_sync_at` is less than
   30 minutes old.
5. **Stale** otherwise: 30 minutes or more since the last sync, no sync yet, or scores still pending after
   3 minutes (long press: "Scores are still being calculated"; the data itself is stored).

The button's age is Apple Health's: that is what a tap syncs. A provider delivers on its own schedule, so
its `last_sync_at` appears in the long press and counts for the button only through `pending` and `error`.

**After a sync** (a `POST /v1/sync` with a non-empty `scoring.queued_days`): treat the scores as pending
at once, from the response, without waiting for this route. Its first call comes no sooner than
`max(expected_within_sec, 10)` seconds after the sync's `200` (its answer is cached for 10 s and could
otherwise predate the sync). Then call it again at the same interval while `freshness` is `scores_pending`,
for at most 3 minutes after the sync. Then stop polling. Call it once more at the next foreground or the
next silent refresh push (Push): on the push, reload today, then call this route once, at least 10 s after
the push. The push comes at most once per 5 minutes per account, so it cannot replace the polling. When
`freshness` is `current` and the call came at least 10 s after the sync's `200`, the sync's wait is over:
reload today (`GET /v1/days/{today}`) and, in the coaching pilot, `GET /v1/coaching/daily-briefing`, and only
then turn the button green. `POST /v1/sync` does not wait for scoring and does not change (D297): scoring
runs after it, in another service.

`freshness` can say `current` too early only in a rare race: a scoring run that started while a connection's
delivery was being stored marks the day done, and the run the delivery starts updates the score seconds
later. The silent refresh push follows that run.

Fresh: synced a minute ago, everything scored (a Garmin connection last delivered at 05:02):

<!-- type: model.SyncStatus -->
```json
{ "gate": null, "last_sync_at": "2026-11-27T06:58:12Z", "last_sync_device": "iPhone",
  "latest": { "hr": "2026-11-27T06:55:00Z", "hrv": "2026-11-27T05:02:00Z", "sleep": "2026-11-27T06:41:00Z" },
  "today": { "day": "2026-11-27", "state": "scored", "scored_at": "2026-11-27T06:58:31Z", "model_version": "2026.10.3" },
  "pending_days": [], "expected_within_sec": 20, "resync_requested_at": null,
  "freshness": "current", "scores_pending_since": null,
  "sources": [ { "source": "healthkit", "last_sync_at": "2026-11-27T06:58:12Z", "status": "ok", "reason": null },
               { "source": "garmin", "last_sync_at": "2026-11-27T05:02:11Z", "status": "ok", "reason": null } ] }
```

Scores pending: the sync's day is queued, and a Garmin delivery is still being stored:

<!-- type: model.SyncStatus -->
```json
{ "gate": null, "last_sync_at": "2026-11-27T06:58:12Z", "last_sync_device": "iPhone",
  "latest": { "hr": "2026-11-27T06:55:00Z", "hrv": "2026-11-26T05:01:40Z", "sleep": "2026-11-27T06:41:00Z" },
  "today": { "day": "2026-11-27", "state": "pending", "scored_at": null, "model_version": "2026.10.3" },
  "pending_days": ["2026-11-27"], "expected_within_sec": 20, "resync_requested_at": null,
  "freshness": "scores_pending", "scores_pending_since": "2026-11-27T06:58:12Z",
  "sources": [ { "source": "healthkit", "last_sync_at": "2026-11-27T06:58:12Z", "status": "ok", "reason": null },
               { "source": "garmin", "last_sync_at": "2026-11-26T05:03:02Z", "status": "pending", "reason": null } ] }
```

Stale (at 15:20Z, the last sync at 09:10Z; scored, nothing pending; no connection, so only `healthkit`):
the app shows stale from the age alone.

<!-- type: model.SyncStatus -->
```json
{ "gate": null, "last_sync_at": "2026-11-27T09:10:44Z", "last_sync_device": "iPhone",
  "latest": { "hr": "2026-11-27T09:08:00Z", "sleep": "2026-11-27T06:41:00Z", "steps": "2026-11-27T09:05:00Z" },
  "today": { "day": "2026-11-27", "state": "scored", "scored_at": "2026-11-27T09:11:02Z", "model_version": "2026.10.3" },
  "pending_days": [], "expected_within_sec": 20, "resync_requested_at": null,
  "freshness": "current", "scores_pending_since": null,
  "sources": [ { "source": "healthkit", "last_sync_at": "2026-11-27T09:10:44Z", "status": "ok", "reason": null } ] }
```

A provider error: Garmin's grant needs renewing (Apple Health is fine, the scores are current):

<!-- type: model.SyncStatus -->
```json
{ "gate": null, "last_sync_at": "2026-11-27T06:58:12Z", "last_sync_device": "iPhone",
  "latest": { "hr": "2026-11-27T06:55:00Z", "hrv": "2026-11-24T05:02:00Z", "sleep": "2026-11-27T06:41:00Z" },
  "today": { "day": "2026-11-27", "state": "scored", "scored_at": "2026-11-27T06:58:31Z", "model_version": "2026.10.3" },
  "pending_days": [], "expected_within_sec": 20, "resync_requested_at": null,
  "freshness": "current", "scores_pending_since": null,
  "sources": [ { "source": "healthkit", "last_sync_at": "2026-11-27T06:58:12Z", "status": "ok", "reason": null },
               { "source": "garmin", "last_sync_at": "2026-11-24T05:02:11Z", "status": "error", "reason": "needs_reauth" } ] }
```

### `GET /v1/model` — the scoring model and what changed

<!-- type: model.ModelInfo -->
```json
{ "current": "2026.10.1",
  "changelog": [ { "version": "2026.10.1", "date": "2026-10-20",
                   "summary": "The model at the start of the beta: …",
                   "summary_en": "The model at the start of the beta: …",
                   "summary_de": "Das Modell zum Start der Beta: …",
                   "affects": ["readiness", "recovery", "strain", "sleep", "sleep_quality"] } ] }
```

Newest first. `summary` follows `Accept-Language`. `affects` names trend metrics
(`GET /v1/trends`); it is always an array, never `null` or absent, and **may be empty** (a change that moves no score metric, e.g. 2026.10.3, which changes workout handling only), so do not assume a non-empty list. Scores with an older `model_version` move when they are rescored;
the app can say "recalculated with model 2026.11.1" and show the entry.

### `POST /v1/feedback` — report a problem

<!-- type: model.FeedbackPost -->
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

<!-- type: model.DailyScore shape -->
```json
{ "day": "2026-10-18", "readiness": 84, "state": "balanced", "recovery": 82, "strain": 12.4,
  "steps": 9412, "active_kcal": 612, "strength_min": 59, "volume_kg": 7420, "sleep_performance": 87, "sleep_need_min": 538, "sleep_actual_min": 468, "sleep_debt_min": 137,
  "sleep_need_base_min": 460, "sleep_need_strain_min": 37, "sleep_need_debt_min": 41, "sleep_need_nap_min": 0, "nap_min": 25,
  "sleep_quality": 91, "sleep_consistency": 81,
  "hrv_ms": 58.2, "rhr": 48.9, "resp_rate": 14.6, "spo2": 96.4,
  "hrv_baseline_ms": 53.1, "rhr_baseline": 50.2, "baseline_days": 30, "baseline_hrv_days": 30, "baseline_rhr_days": 30,
  "calibrating": false, "calibrating_reason": null,
  "coverage": { "sleep_hr_minutes": 400, "hrv_readings": 5, "sleep_source": "Garmin Connect",
                "hr_source_hours": { "Garmin Connect": 14, "Apple Watch": 9 }, "night_complete": true, "status": "final",
                "sleep_origin": "healthkit", "hrv_metric": "sdnn" },
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
| `hrv_ms` | number? | Overnight mean SDNN, ms; RMSSD instead on a day whose `coverage.hrv_metric` is `rmssd` (Connections, from the platform PR `connect/scoring`). Never compare values of different metrics. |
| `rhr` | number? | Resting HR, bpm. |
| `resp_rate` | number? | Overnight breaths/min. |
| `spo2` | number? | Overnight mean, percent. |
| `hrv_baseline_ms` | number? | The 30-day HRV baseline `hrv_ms` was scored against: geometric mean of up to 30 scored days before `day`. In the same metric as `hrv_ms`, from days of that metric only (`coverage.hrv_metric`). |
| `rhr_baseline` | number? | The 30-day resting-HR baseline `rhr` was scored against. |
| `baseline_days` | int | Nights of history behind the baselines (the smaller of HRV and resting-HR days among the 30 scored days before `day`), 0–30. "Calibrating — N nights to go" is `7 − baseline_days` and holds only when `calibrating_reason` is `short_history`; with `no_hrv` / `no_rhr` waiting does not end it. |
| `calibrating` | bool | `baseline_days < 7` on a scored day; `false` on a day that was never scored (the placeholder of `GET /v1/days/{day}`). |
| `baseline_hrv_days` | int? | N of the HRV baseline: scored days among the 30 before `day` that have an overnight HRV (from the platform PR `connect/scoring`: an HRV of the day's metric, `coverage.hrv_metric`), 0-30. `baseline_days = min(baseline_hrv_days, baseline_rhr_days)`. `null` (read it as unknown) on a day that was never scored, on rows scored before this field existed (until rescored), and on a row last written by an older scoring image after a rollback that disagrees with `baseline_days` / `calibrating` (the server serves such a row's fields as unknown). |
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

<!-- type: model.DailyScore*8 shape -->
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
  "hr_source_hours": { "Garmin Connect": 3 }, "night_complete": false, "status": "provisional",
  "sleep_origin": "healthkit", "hrv_metric": null }
```

| Field | Type | Notes |
| --- | --- | --- |
| `sleep_hr_minutes` | int | Minutes of heart-rate data (de-duplicated, from whichever source won each hour, as `details.hr_minutes`) inside the winning sleep source's window, from its first segment's start to its last segment's end. `0` without a night. |
| `hrv_readings` | int | Number of overnight HRV readings (`hrv_sdnn` samples in the night; from the platform PR `connect/scoring`, samples of the night's metric, `hrv_metric`) behind `hrv_ms`. `0` when `hrv_ms` is `null` for lack of them. |
| `sleep_source` | string? | The `source` (HealthKit name, as `UserSource.name`) whose sleep won the night (the longest, `SCORING.md`). `null` without a night. A direct connection's night: the provider's display name, as its `UserSource.name` (`"Garmin"`). |
| `hr_source_hours` | `{string: int}` | Whole hours of the local day `day` (up to 24, fewer for today so far) for which each source was the one heart-rate source used, keyed by source name. `{}` without heart rate. The keys are source names, so treat them as data (they may contain underscores or spaces), not as field names. |
| `night_complete` | bool | The night is over: **wake evidence** (a sample of any type that starts at least 30 minutes after the winning sleep source's last segment ended, from that same source; for a source that wrote nothing but sleep in the last 7 days, a `steps` sample from any source counts) **or 10:00 local time on `day` has passed** (the ceiling; every earlier day is complete). It reflects the latest computation. |
| `status` | string | `provisional` or `final` (closed enum). `final` once `night_complete` has held, or at the 10:00 ceiling. **Sticky:** once a day is `final` it stays `final`, even if a later sleep segment makes `night_complete` `false` again; its numbers still follow later scorings. So `final` with `night_complete: false` is possible, `provisional` with `night_complete: true` is not. |
| `sleep_origin` | string? | Connections (D257). Where the winning night came from: `healthkit` or `direct` (closed, as `UserSource.origin`). `null` without a night. |
| `hrv_metric` | string? | Connections (D257). The metric of `hrv_ms`, `hrv_baseline_ms` and `baseline_hrv_days`: `sdnn` or `rmssd`, a **closed** enum. It names the metric, not the path: `rmssd` comes from a direct connection today, but a `hrv_rmssd` sample from `POST /v1/sync` counts too, so read the night's path from `sleep_origin` and the sources, not from this. `null` without an HRV. Absent (a row scored before it, or a server before it) with an `hrv_ms`: read it as `sdnn`. |

**`sleep_origin` and `hrv_metric` arrive with `coverage` itself:** the platform PR `connect/scoring` builds
`coverage` (until then the server does not send it, and `null` reads as final, nothing known, as above), with
these two keys in it. Rows scored before stay without them until rescored. The rules behind them (D257, `SCORING.md`):

- **Two HRV metrics, never mixed.** A night's HRV is the overnight mean of one metric. With both, the one with
  more days in the 30-day window wins (a tie: `rmssd`). The baseline is built only from days of the night's
  metric, so `hrv_ms` and `hrv_baseline_ms` are always the same metric, and so are Trends' `hrv` points (Trends).
- **A change of metric recalibrates.** After a switch (SDNN, then RMSSD, or back; today SDNN comes from Apple Health and RMSSD
  from a connection), `baseline_hrv_days` counts the new metric's nights only, so the day is `calibrating` with
  `calibrating_reason` `short_history` until 7 nights of it exist: waiting ends it, as that reason says. No new
  reason.
- **One night, one record.** A direct connection's night beats the same vendor's night from Apple Health
  (`sleep_origin: "direct"`); across vendors the longest night wins, as before. A provider that sends a night
  again (a revised record) replaces it, and `night_complete` follows the provider's own validation of the night
  (there is no wake evidence from a direct source: complete once the provider calls it final, or at the 10:00
  ceiling).

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

<!-- type: model.DayDetail shape -->
```jsonc
{ "score": DailyScore, "sleep": SleepDetail?, "strain": DayStrain?, "workouts": [Workout] }
```

A day that has not been scored yet returns `200` with a `score` whose numbers are
all `null`, `state: "calibrating"`, `calibrating: false`, `computed_at:
"0001-01-01T00:00:00Z"`, `coverage: null`, and `sleep: null`, `strain: null`, `workouts: []`. `400` on a
malformed day. `sleep` is `null` when no sleep was recorded for the night ending on
`day` (also for days scored before the platform upgrade until they are recomputed).

`SleepDetail` — last night as the Sleep screen's *Day* tab shows it:

<!-- type: model.SleepDetail shape -->
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

<!-- type: model.DayStrain shape -->
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

<!-- type: model.SleepRange shape -->
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

<!-- type: model.SleepPlan -->
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
oldest first. Cross-source duplicates are left out (see **Duplicate workouts** below);
`include_hidden=true` (optional; `true` or `false`, absent = `false`; anything else is `422`) returns
them too, each with `duplicate_of` set to the id of the primary it is hidden behind. A visible primary
still carries `also_recorded_by`. **The app's data export passes `include_hidden=true`**, so the file
holds every workout the account has (the export promises completeness); every other screen leaves it off.
`GET /v1/days/{day}` has no such parameter and never lists hidden duplicates. Same-source copies that
were folded are deleted, not hidden, and are not returned.

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
| `duplicate_of` | uuid? | Only with `include_hidden=true`, on a hidden duplicate: the `id` of the primary it is hidden behind. Absent on visible workouts, and always absent without `include_hidden`. |
| `also_recorded_by` | `[string]?` | The `source` names of other recordings of this same workout that the server hid (a Watch and a Garmin that both recorded one run): "also recorded by Garmin Connect". Distinct, sorted. Omitted when there are none (a client treats missing and `[]` alike). Ignored on input. |
| `session_rpe`, `session_rpe_load` | number?, int? | Not served yet (D293): the user's session RPE (1–10) and Foster's load (minutes × RPE). Omitted without a rating. Read-only; set with `PUT /v1/workouts/{id}/rpe` ([Recovery without a watch](#recovery-without-a-watch--check-ins-and-session-rpe-d291d295-not-served-yet)). |

**Duplicate workouts.** One activity must appear once. Two rules, both server-side:

- *Same source.* Copies of one `source` with the same `activity` whose `start`s and whose `end`s each lie within
  120 seconds of each other are one workout (Garmin Connect re-writes a workout in Apple Health under a new UUID and deletes the old one).
  When a sync stores such a copy, the server **removes the older copies** and keeps the most recently uploaded id (the one Apple Health still has); a
  one-off run does the same for copies stored earlier. The removed ids answer `404` on `GET /v1/workouts/{id}`, a Train session that was attached to one stays and is
  re-attached to the kept workout when the day is rescored (`session_id`, `muscular_load`). These removals are not counted in `SyncResponse.deleted`, which counts the requested deletions only; their days are queued. If a request is too large for the fold on sync, the fold is skipped for that request: it still succeeds (`200`) and the one-off run folds the copies later.
- *Different sources.* Workouts of **different** `source`s with the same `activity` (either one `other` counts as the same) that overlap by at least **80 %** of the shorter one's duration are one workout. Nothing is deleted. The server picks a **primary** (a workout with a specific `activity` ranks before one whose activity is `other`; then the one with at least 2 heart-rate samples from its own source inside its window; then the longer one; then the lower `id`). So an `other` workout still matches as a duplicate but can only be hidden behind a specific one: a Watch `strength` workout stays visible against a longer Garmin `other`, which is listed in its `also_recorded_by`, lists only the primary in `GET /v1/days/{day}` → `workouts` and `GET /v1/workouts`, and puts the other sources in the primary's `also_recorded_by`. The primary's heart-rate window, zones and strain are the day's de-duplicated heart rate, so the day's strain does not change. `DayDetail.workouts` and `details.workouts` count the visible workouts.
- A workout with `source` `ryft-session` is never hidden.
- Hiding is decided **when the day is scored** (not at upload), so a copy can show until `scoring_state` leaves `pending`. If a primary is deleted, its duplicate is visible again **at once**, before the re-score, which later picks a new primary.
- `GET /v1/workouts/{id}` on a primary carries `also_recorded_by` too (distinct, sorted sources of its duplicates; omitted when none). On a hidden duplicate's id it still answers `200` with that workout as stored and **no** `also_recorded_by`, so a link the app holds keeps working.

`GET /v1/workouts?include_hidden=true`, a primary and the duplicate hidden behind it:

<!-- type: []model.Workout -->
```json
[ { "id": "7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12", "activity": "running", "start": "2026-10-20T16:02:00Z", "end": "2026-10-20T16:58:30Z",
    "avg_hr": 156.2, "max_hr": 181, "distance_m": 10240, "source": "Garmin Connect", "strain": 12.8, "also_recorded_by": ["Tim's Apple Watch"] },
  { "id": "2b9f4a60-8d3e-4c71-a5b2-6e0d1c9f3a77", "activity": "running", "start": "2026-10-20T16:02:10Z", "end": "2026-10-20T16:58:20Z",
    "avg_hr": 155.0, "max_hr": 179, "source": "Tim's Apple Watch", "duplicate_of": "7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12" } ]
```

A single workout (`GET /v1/days/{day}`, `GET /v1/workouts`):

<!-- type: model.Workout -->
```json
{ "id": "7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12", "activity": "running", "start": "2026-10-20T16:02:00Z", "end": "2026-10-20T16:58:30Z",
  "avg_hr": 156.2, "max_hr": 181, "distance_m": 10240, "energy_kcal": 702, "source": "Garmin Connect",
  "strain": 12.8, "model_version": "2026.10.3", "also_recorded_by": ["Tim's Apple Watch"] }
```

### `GET /v1/workouts/{id}` — one workout

`id` is the workout UUID. `400` when it is not a UUID, `404` when no such workout.
Returns every `Workout` field plus:

<!-- type: model.WorkoutDetail shape -->
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

<!-- type: model.Trend shape -->
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

**`metric=hrv` with two HRV metrics** (Connections, from the platform PR `connect/scoring`): `points` holds only
the days whose HRV metric (`coverage.hrv_metric`, absent = `sdnn`) is the one of the newest day in the window that
has an HRV; days of the other metric are gaps. `avg`, `min`, `max` and `baseline` are in that metric. The response
does not name the metric: read it from that day's `coverage.hrv_metric`.

### `GET /v1/insights` — Insights screen

Short rule-based cards about today, most urgent first (`warn`, then `notice`, then
`info`). `[]` when nothing stands out. Computed on read from the last 30 days and cached
for 5 minutes, so a sync can take up to 5 minutes to change the cards.

<!-- type: []model.Insight -->
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

<!-- type: []model.Exercise -->
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

<!-- type: model.ExerciseInput shape -->
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

<!-- type: model.ExerciseHistory shape -->
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

<!-- type: model.PlansOverview shape -->
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

<!-- type: model.Plan shape -->
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

<!-- type: model.Enrollment -->
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

<!-- type: model.PlanToday shape -->
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

<!-- type: model.Session shape -->
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

<!-- type: []model.SessionSummary shape -->
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

<!-- type: model.TrainImportRequest -->
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

<!-- type: model.TrainImportResponse -->
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

<!-- type: []model.Behavior -->
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

<!-- type: model.BehaviorInput shape -->
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

<!-- type: model.JournalDay -->
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

<!-- type: model.BehaviorImpacts -->
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

### Recovery without a watch — check-ins and session RPE (D291–D295; not served yet)

A user who does not wear a watch at night still gets a recovery estimate: a **morning check-in** (when they
went to bed and woke up, how well they think they slept, how fresh they feel, whether they are sore) and,
for a workout without heart rate, a **session RPE** (how hard it felt, 1–10). The server computes the sleep
duration, a recovery estimate 0–100 and how much data it rests on. Wearable data always wins: a check-in
is the fallback, never a second opinion (D291).

**When it is served.** Docs only so far (the contract PR): every route here answers `404` without a `code`
(an unknown route) until the implementation PR. After it, while the flag `recovery.checkin` (`GET /v1/config`)
is off for the account, the two writes and `GET /v1/recovery/status` answer that `404`; the reads of stored
check-ins and both `DELETE`s keep working. **On that `404`, hide the check-in and the RPE prompt; never drop
the workout, the day or any other local data** (it is not the `404` of a resource that is gone). The examples
are `http` blocks until the server has the model types (`model.RecoveryCheckin`, `RecoveryCheckinPut`,
`RecoveryStatus`, `WorkoutRPEPut`; D295), as for Connections.

**The wearable night** (D291). One rule decides everything below: the night ending on the day is a
**wearable night** when it has asleep stages (`asleep`, `core`, `deep`, `rem`; an `in_bed`-only night, such
as the iPhone's, is not one) or HRV readings in the sleep window, and `DailyScore.recovery` is not `null`. A resting heart rate alone does not make one: a watch worn only by day
writes resting heart rate every day, and its user is the `medium` tier.

**Tiers** (`RecoveryStatus.confidence`, a **closed** enum):

| Tier | When | `source` | `confidence` |
| --- | --- | --- | --- |
| Wearable night | The day has a wearable night. | `wearable` | `high` |
| Day watch + check-in | No wearable night; a check-in for the day; the day before has wearable heart rate in at least 8 distinct hours. | `checkin` | `medium` |
| Check-in only | No wearable night; a check-in for the day; less heart rate than that the day before. | `checkin` | `low` |
| Nothing | Neither. | `none` | `null` |

A sync that later brings a wearable night for a day that has a check-in **supersedes** the check-in: it stays
stored and readable (`superseded: true`), and the day's status, the briefing and the periodization use the
wearable. Deleting that wearable data makes the check-in current again. While the flag is off, stored
check-ins and ratings count nowhere (status, readiness, load, the N2R bedtime): the flag is a kill switch,
and they stay stored and readable.

**The Today ring** (decided 2026-11-27, D291). On a day without a wearable night, the Today ring shows the
check-in's estimate: `RecoveryStatus.recovery_score` while `source` is `checkin`, **visibly marked as
estimated**, with `confidence` (`medium`, `low`) saying how much it rests on. The app takes the label from
`source` and `confidence`, never from the number. `DailyScore.recovery` and `readiness` keep their meaning: they
stay wearable-only and `null` on such a day, so trends, baselines, insights and `GET /v1/trends` never contain a
check-in. With `source` `wearable` the ring shows `DailyScore.recovery` as today; with `none` it stays empty.

**Who is asked** (decided 2026-11-27, D291). The one rule above, with no special case: a night with asleep stages
from a sleep-only tracker but no recovery (no heart rate, no HRV) is not a wearable night, so its user is asked
too, and the check-in is scored on the reported times; the measured sleep is not blended in. A night typed into
Apple Health by hand counts as measured (the sync does not mark it); no sync field is added for it now. Apple's
workout effort score is not read: `session_rpe` comes only from the user's rating in RYFT.

**Answers common to every route here.** They are api routes under the Conventions: the bearer, the rate
limits, App Attest, the beta gate (`403 invite_required`), 64 KiB bodies with unknown fields `400`,
`X-Request-ID`, `401`, `503` with `Retry-After`. The writes (`PUT /v1/recovery/checkins/{date}`,
`PUT /v1/workouts/{id}/rpe`) store health data and need consent to the policy in force
(`403 consent_required`; `CONSENT_ENFORCE=warn` acts as `on`, as for Nutrition writes and the import). The
reads and the two `DELETE`s need no consent and work while the flag is off (the owner's access and erasure,
as for Nutrition). No new `code`.

#### `PUT /v1/recovery/checkins/{date}` — the morning check-in

`{date}` is the **wake day** (`YYYY-MM-DD`): the night that ends on `{date}` belongs to it, as everywhere
in this API. One check-in per account and wake day; `PUT` **replaces** it (an idempotent upsert: the same
body twice gives the same check-in, `created_at` unchanged). Body `RecoveryCheckinPut`, every field required:

```http
PUT /v1/recovery/checkins/2026-11-26
Authorization: Bearer <access token>
Content-Type: application/json

{ "bedtime": "2026-11-25T22:10:00Z", "wake_time": "2026-11-26T05:40:00Z", "tz_offset_sec": 3600,
  "perceived_sleep_quality": 7, "perceived_freshness": 6, "soreness": false }
```

| Field | Type | Rule |
| --- | --- | --- |
| `bedtime` | timestamp | When the user went to bed (as they report it; UTC). |
| `wake_time` | timestamp | When they woke up (UTC). After `bedtime`, by 60 to 960 minutes (1–16 h). Not more than 5 minutes in the future. |
| `tz_offset_sec` | int | The UTC offset of the clock the user saw at `wake_time` (as on a HealthKit workout), −50400 to 50400, a multiple of 60 (`TimeZone.current.secondsFromGMT(for: wakeTime)`). `{date}` must be the calendar date of `wake_time + tz_offset_sec`. |
| `perceived_sleep_quality` | int | 1–10: how well they think they slept (1 very poorly, 10 very well). |
| `perceived_freshness` | int | 1–10: how fresh they feel now (1 exhausted, 10 fully fresh). |
| `soreness` | bool | They feel sore. Captured with **zero weight** (decided 2026-11-27, D292): stored, shown, exported and kept for a later calibration; it moves neither the score nor `acute_drop` nor the readiness state, and only adds the reason `soreness_reported`. |

`{date}` must be within the last 14 days (today included, the account's timezone), like the journal. A
number with a fraction in an `int` field is `400` (it does not decode). `422` for everything else above: a
value out of range, `wake_time` not after `bedtime`, a duration outside 60–960 minutes, `{date}` that is not
the wake day, older than 14 days or in the future, a `wake_time` more than 5 minutes in the future. `404`
while the flag is off.
`200` with the `RecoveryCheckin`:

```http
HTTP/2 200
content-type: application/json

{ "date": "2026-11-26", "bedtime": "2026-11-25T22:10:00Z", "wake_time": "2026-11-26T05:40:00Z", "tz_offset_sec": 3600,
  "perceived_sleep_quality": 7, "perceived_freshness": 6, "soreness": false,
  "sleep_duration_min": 450, "recovery_score": 77,
  "components": { "wellness": 63, "wellness_absolute": 63, "wellness_personal": null, "duration": 100, "regularity": null, "n2r": null },
  "acute_drop": false, "confounders": [], "confidence": "low", "superseded": false, "n2r_bedtime_applied": false,
  "model_version": "2026.11.1", "created_at": "2026-11-26T05:52:10Z", "updated_at": "2026-11-26T05:52:10Z" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `date`, `bedtime`, `wake_time`, `tz_offset_sec`, `perceived_sleep_quality`, `perceived_freshness`, `soreness` | | As sent. |
| `sleep_duration_min` | int | `wake_time − bedtime` in whole minutes. Time in bed as the user reports it: nothing is taken off for falling asleep or waking at night. |
| `recovery_score` | int | 0–100, the hybrid estimate (below). Always present on a stored check-in. |
| `components` | `RecoveryComponents` | The parts behind it, each 0–100: `wellness` (what enters the score), `wellness_absolute`, `wellness_personal` (int?, `null` until there are 5 check-ins in the 14 days before), `duration`, `regularity` (int?, `null` with fewer than 3 nights to compare), `n2r` (int?, yesterday's Nutrition-to-Recovery score; `null` without one). A `null` part is left out and the weights of the others are scaled up. |
| `acute_drop` | bool | The check-in is well below the user's usual: the personal `z` (below; it needs 5 check-ins in the 14 days before, its SD floored at 6) is −1.5 or lower, or quality and freshness are both ≤ 4. |
| `confounders` | `[string]` | With `acute_drop`, what the evening before may explain it (D294): Nutrition-to-Recovery tags `alcohol_above_035gkg`, `caffeine_bed_high`, `late_satfat_meal`; journal behaviors of the day before `journal_alcohol` (`alcohol` or `alcohol_drinks`), `journal_late_caffeine` (`caffeine_after_2pm`, or `last_caffeine` within 8 h of `bedtime`), `journal_late_meal` (`late_meal`), and the journal states of the day before `journal_sick` (`sick`), `journal_travel` (`travel`), `journal_work_stress` (`work_stress`; decided 2026-11-27). An **open** set: ignore a value you don't know. `[]` without `acute_drop` or without one found. |
| `confidence` | string | `medium` or `low` (the tiers above). |
| `superseded` | bool | `date` has a wearable night: the day uses it, not this check-in. |
| `n2r_bedtime_applied` | bool | Yesterday's Nutrition-to-Recovery uses this `bedtime` (the account is enrolled, the flag is on and the night has no measured sleep start; D293). |
| `model_version` | string | The scoring model that computed it (`GET /v1/model`). A newer model rescores stored check-ins. |
| `created_at`, `updated_at` | timestamp | First save; last change of the content. |

**The score** (D292; every constant is a versioned RYFT heuristic unless cited, `docs/SCORING.md`):

- `wellness_absolute` = `(0.65 · quality + 0.35 · freshness − 1) / 9 · 100` (1 → 0, 10 → 100). Hooper-style
  self-report (Hooper & Mackinnon 1995); the weights are RYFT's.
- `wellness_personal`, from 5 check-ins in the 14 days before `{date}`: `z = (absolute − mean) / max(SD, 6)`,
  `personal = clamp(50 + 25 · z, 0, 100)`; then `wellness = 0.7 · absolute + 0.3 · personal`. Without it,
  `wellness = absolute`.
- `duration`: 100 from the lower edge `L` to `L + 120` minutes (7.5–9.5 h by default); below `L` linear down
  to 40 at 5 h, 40 below that; above `L + 120` minus 10 per hour, never under 75. `L` is 450 minutes, or
  `User.sleep_base_min` (clamped to 360–600) while `sleep_base_source` is `learned` or `manual`.
- `regularity`: the sleep midpoint (local clock, `tz_offset_sec`) against the circular mean of the up to 7
  nights before (a wearable night or a check-in, the wearable first; at least 3): 100 within 30 minutes, 50
  from 120 minutes, linear between.
- `recovery_score` = `0.50 · wellness + 0.30 · duration + 0.10 · regularity + 0.10 · n2r` over the parts
  present, rounded and clamped to 0–100.

So 5 h with quality and freshness 3 and no history is **29**; 8 h with 9 and 9 is **93**.

A check-in's score depends on the check-ins before it: changing or deleting one rescores the later ones
within 14 days. `GET /v1/me/sync` → `pending_days` does not list them; the answer of the `PUT` is final for
its own day.

#### `GET /v1/recovery/checkins?from=&to=` · `GET /v1/recovery/checkins/{date}` · `DELETE /v1/recovery/checkins/{date}`

The range takes the `from`/`to` of `GET /v1/days` (both optional, default the 30 days ending today; `400` on a
malformed date, `422` when `from > to` or the range is over 400 days) and returns `[RecoveryCheckin]`, oldest
first, superseded ones included (`[]` when none). It is also what the app's data export reads. One day:
`200 RecoveryCheckin`, `404` when there is none. `DELETE`: `204`, also when there was none; the day falls
back to whatever else it has, and yesterday's Nutrition-to-Recovery returns to the expected bedtime. Both
work while the flag is off.

```http
GET /v1/recovery/checkins?from=2026-11-25&to=2026-11-26

HTTP/2 200
content-type: application/json

[ { "date": "2026-11-26", "bedtime": "2026-11-25T22:10:00Z", "wake_time": "2026-11-26T05:40:00Z", "tz_offset_sec": 3600,
    "perceived_sleep_quality": 7, "perceived_freshness": 6, "soreness": false,
    "sleep_duration_min": 450, "recovery_score": 77,
    "components": { "wellness": 63, "wellness_absolute": 63, "wellness_personal": null, "duration": 100, "regularity": null, "n2r": null },
    "acute_drop": false, "confounders": [], "confidence": "low", "superseded": false, "n2r_bedtime_applied": false,
    "model_version": "2026.11.1", "created_at": "2026-11-26T05:52:10Z", "updated_at": "2026-11-26T05:52:10Z" } ]
```

#### `GET /v1/recovery/status` — today's recovery: which source, and whether to ask

`RecoveryStatus` for today (the account's timezone). The same object is `CoachingBriefing.recovery` (below);
this route serves accounts without the coaching pilot, which have no briefing (D291). `404` while the flag
is off.

```http
GET /v1/recovery/status

HTTP/2 200
content-type: application/json

{ "date": "2026-11-26", "source": "none", "confidence": null, "recovery_score": null, "needs_checkin": true,
  "checkin": null, "suggested_bedtime": "22:45", "suggested_wake_time": "06:45", "suggestion_basis": "expected_bedtime" }
```

| Field | Type | Notes |
| --- | --- | --- |
| `date` | string | Today, `YYYY-MM-DD`, the wake day it describes. |
| `source` | string | `wearable`, `checkin` or `none` (the tiers above). **Closed.** |
| `confidence` | string? | `high`, `medium`, `low`; `null` with `none`. **Closed.** |
| `recovery_score` | int? | `wearable`: `DailyScore.recovery`; `checkin`: `RecoveryCheckin.recovery_score`; `null` with `none`. |
| `needs_checkin` | bool | Ask for the check-in now: it is 04:00 or later locally, today has **no wearable night** (above), and there is no check-in for today. The sync does not say which sleep was typed into Apple Health by hand, so a typed night with asleep stages on a day with a recovery counts as a wearable night (decided 2026-11-27: kept so, no sync field for now; D291). |
| `checkin` | `RecoveryCheckin?` | Today's check-in; `null` without one. |
| `suggested_bedtime`, `suggested_wake_time` | string? | Local `HH:MM` to pre-fill the check-in: the median of the check-ins of the last 7 days (at least 3), else the Nutrition-to-Recovery `expected_bedtime` and 8 hours after it, else `null`. Never a recommendation to the user; the Sleep Planner (`GET /v1/sleep/plan`) stays that. |
| `suggestion_basis` | string? | `checkins_7d`, `expected_bedtime`, or `null` with no suggestion. **Closed.** |

#### `PUT /v1/workouts/{id}/rpe` · `DELETE /v1/workouts/{id}/rpe` — session RPE

How hard a workout felt, the session RPE of Foster et al. (2001), 1–10. Meant for a workout without heart rate
(entered by hand in Apple Health, `was_user_entered`, or a Train session without a watch: rate
`Session.workout_id`). Any of the user's workouts can be rated; the rating counts for load only where heart
rate or power cannot (D293). The rating is the user's own input in RYFT: the app does not forward Apple's
workout effort score as `session_rpe` (decided 2026-11-27).

```http
PUT /v1/workouts/7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12/rpe
Authorization: Bearer <access token>
Content-Type: application/json

{ "session_rpe": 8 }

HTTP/2 200
content-type: application/json

{ "id": "7d1b6c3e-2c1a-4b8e-9d3a-5a0e4f6b8c12", "activity": "strength", "start": "2026-11-25T17:00:00Z", "end": "2026-11-25T18:00:00Z",
  "source": "Tim's iPhone", "session_rpe": 8, "session_rpe_load": 480 }
```

Body `WorkoutRPEPut`: `session_rpe` (number, 1–10 in steps of 0.5; else `422`). Answers the `Workout` with
the rating. `404` when the workout is not the user's or the flag is off; `422` on a hidden duplicate
(`duplicate_of` set): rate the primary. `PUT` replaces the rating; `DELETE` removes it (`204` for any of the
user's workouts, rated or not; `404` for a workout that is not the user's or no longer exists, which the app
treats as done; it works while the flag is off). The rating belongs to the workout: a sync of the same
HealthKit workout keeps it, the same-source fold (Duplicate workouts) and a session re-attached to another
workout move it along, and a HealthKit deletion of the workout deletes it.

`Workout` gains two read-only fields, on every route that returns workouts (`GET /v1/workouts`,
`GET /v1/workouts/{id}`, `GET /v1/days/{day}` → `workouts`); `POST /v1/sync` ignores them on upload:

| Field | Type | Notes |
| --- | --- | --- |
| `session_rpe` | number? | The rating. Omitted when there is none. |
| `session_rpe_load` | int? | Foster's session load in arbitrary units: the duration in whole minutes (rounded) × `session_rpe`, rounded (60 minutes at 8 is 480). Omitted when there is no rating. |

**Load.** The coaching training load (`CoachingBriefing.training_load`: `acute_7d`, `chronic_28d_weekly`, `acwr`,
`tsb`) counts a rated workout that has no usable heart rate and no power on its TRIMP scale: Banister's TRIMP
with `session_rpe / 10` in place of the heart-rate-reserve fraction, `minutes × r × a × e^(b·r)` with the
constants the heart-rate path uses for the user's sex. 60 minutes at 8 (480 AU) is **151** with `sex`
unspecified, 143 male, 157 female. Heart rate or power, when present, wins and the rating only shows. **Day
strain does not use the rating** (decided 2026-11-27, D293): `DailyScore.strain`, its sleep-need top-up and
`WorkoutDetail.trimp` stay based on heart rate and the muscular load of logged sessions; the rating feeds only acute and chronic load,
ACWR and TSB.

#### `CoachingBriefing.recovery` and `N2RScore.bedtime_source`

`GET /v1/coaching/daily-briefing` gains `recovery`: the `RecoveryStatus` above for today, also when the
briefing's `local_date` is still yesterday (before the expected bedtime); `null` while `recovery.checkin` is off.
`readiness` and `training_load` come with a coaching `plan`, as today (without a goal profile, `plan` is `null`,
`readiness` `UNKNOWN`). Where the HRV path yields `UNKNOWN` (no overnight HRV, or not enough of it for its
baseline) and the day has a current check-in, `readiness` uses the check-in (D294):

| Check-in | `readiness` | `readiness_reasons` |
| --- | --- | --- |
| No `acute_drop` | `OPTIMAL` | `[]` |
| `acute_drop` with `confounders` | `ACUTE_LIFESTYLE_SUPPRESSION` (as today) | the `confounders`, then `checkin_below_usual` or `checkin_low_quality_and_freshness` |
| `acute_drop` without | **`LOW_RECOVERY_UNEXPLAINED`** (new) | `checkin_below_usual` or `checkin_low_quality_and_freshness` |

`soreness` adds `soreness_reported` to the reasons and changes nothing else. `journal_sick`, `journal_travel` and
`journal_work_stress` explain a drop like the other confounders (decided 2026-11-27), so a drop with one of them
is `ACUTE_LIFESTYLE_SUPPRESSION`, not `LOW_RECOVERY_UNEXPLAINED`; with `journal_sick` the plan stays protective
(no deficit, the carbohydrate floor), as for an unexplained drop. These three apply to the check-in path only;
the HRV path's confounders do not change. `LEA_GUARDRAIL_ACTIVE` and the HRV path come first, as
today. `LOW_RECOVERY_UNEXPLAINED` gets the protective plan of the HRV path's unexplained drop (no deficit, the
carbohydrate floor). `readiness` gains this one value; **treat a value you don't know as `UNKNOWN`**. Copy for
it and for `ACUTE_LIFESTYLE_SUPPRESSION` from a check-in says what the user reported ("you rated your sleep and
freshness lower than usual", "you noted feeling sick"), never a cause in the body ("your nervous system is
fatigued" is out).

**`readiness_display`, the neutral name** (decided 2026-11-27, D294). `CoachingBriefing` gains `readiness_display`
(string, always present from the implementation PR on, independent of the flag): the name to show and to key
copy on. It equals `readiness`, except that **`AUTONOMIC_FATIGUE` is shown as `LOW_RECOVERY_UNEXPLAINED`**: what
was observed (recovery below the user's usual, no lifestyle explanation), not a cause. `readiness` itself does not
change: the HRV path still emits `AUTONOMIC_FATIGUE` there, so existing clients keep decoding and branching on it,
and the plan logic (deficit, carbohydrate floor) still follows `readiness`. The app shows copy from
`readiness_display` when present and falls back to `readiness` when it is absent (an older server); an unknown
value reads as `UNKNOWN`. Which path produced it is in `readiness_reasons` and `recovery.source`. No copy, on
either path, claims a physiological cause.

A fat-loss account with one rated workout (60 minutes at RPE 8, no heart rate) in its history, after a
short night it rated 3 and 4; yesterday's logging was below the N2R threshold, so `n2r_score` is `null` and
the check-in has no `n2r` part (an excerpt: `…` stands for fields that do not change):

```http
HTTP/2 200
content-type: application/json

{ "local_date": "2026-11-26",
  "plan": { "energy_target_kcal": 2480, "session_class": "REST", "deficit_suspended": true, … },
  "readiness": "LOW_RECOVERY_UNEXPLAINED", "readiness_display": "LOW_RECOVERY_UNEXPLAINED",
  "readiness_reasons": ["checkin_low_quality_and_freshness"],
  "training_load": { "acute_7d": 151.3, "chronic_28d_weekly": 37.8, "acwr": 4.0, "tsb": -113.5 },
  "n2r": { "version": "1.0.0", "n2r_score": null, …, "bedtime": "2026-11-26T22:00:00Z", "bedtime_source": "expected", "tags": [] },
  "yesterday_n2r": { "version": "1.0.0", "n2r_score": null, …, "bedtime": "2026-11-26T00:40:00Z", "bedtime_source": "checkin", "tags": [] },
  "morning_biometrics": null, "cards": [ … ],
  "recovery": { "date": "2026-11-26", "source": "checkin", "confidence": "low", "recovery_score": 31, "needs_checkin": false,
                "checkin": { "date": "2026-11-26", "bedtime": "2026-11-26T00:40:00Z", "wake_time": "2026-11-26T05:40:00Z", "tz_offset_sec": 3600,
                             "perceived_sleep_quality": 3, "perceived_freshness": 4, "soreness": false,
                             "sleep_duration_min": 300, "recovery_score": 31,
                             "components": { "wellness": 26, "wellness_absolute": 26, "wellness_personal": null, "duration": 40, "regularity": null, "n2r": null },
                             "acute_drop": true, "confounders": [], "confidence": "low", "superseded": false, "n2r_bedtime_applied": true,
                             "model_version": "2026.11.1", "created_at": "2026-11-26T05:52:10Z", "updated_at": "2026-11-26T05:52:10Z" },
                "suggested_bedtime": "23:00", "suggested_wake_time": "07:00", "suggestion_basis": "expected_bedtime" } }
```

`N2RScore` (`n2r`, `yesterday_n2r`, the Nutrition daily summaries) gains `bedtime_source`: `expected` (the
profile's `expected_bedtime`), `wearable` (the night's measured sleep start) or `checkin` (the check-in of the
next morning; D293), a **closed** enum. It is absent when `bedtime` is `null` (no expected bedtime and nothing
measured or reported), and absent before the implementation: read absent as unknown. **Retroactive N2R:** these
N2R values are computed when they are read, so a check-in saved or deleted changes the day before's N2R at once,
with the reported bedtime in its alcohol, caffeine and late-meal penalties. `bedtime_source` and the check-in's
`n2r_bedtime_applied` say which bedtime was used. A wearable night's measured sleep start beats the check-in.

### Health

Every service answers the same paths, unauthenticated. **On Cloud Run use `/health/live` and
`/health/ready`** (Cloud Run reserves paths ending in `z`); `/healthz` and `/readyz` have the same
handlers and stay for local compose.

| Path | Answer |
|---|---|
| `GET /health/live`, `GET /healthz` | `200 {"status":"ok"}` while the process runs, from the moment it listens (before it reaches the database). Cloud Run's startup and liveness probe. |
| `GET /health/ready`, `GET /readyz` on Cloud Run | `200 {"status":"ready"}`, or `503 {"error":"not ready"}` without `Retry-After` while starting or when a check fails (Postgres; api's signing keys; scoring and notify also while draining). The cause is only in the service's log, and the answer is reused for 5 s. The synthetics' target. |
| `GET /health/ready`, `GET /readyz` locally, api and ingest | `200 {"status":"ready"}`; `503` with an `error` that names the cause when a check fails, as before A2. `503 {"error":"starting"}` before the start is done. |
| `GET /health/ready`, `GET /readyz` locally, scoring and notify | `200 {"status":"ready","checks":{"db":"ok"}}`; `503` with `"status":"unready"` and an `error` that names the failed check and its cause when Postgres is unreachable, `"status":"draining"` during the shutdown, and `503 {"error":"starting"}` before the start is done. |
| `GET /metrics` (port 9090, local only) | Prometheus text format (`OBSERVABILITY.md`). On Cloud Run the metrics are pushed instead. |

```http
GET /health/ready HTTP/2
Host: api.ryftlab.de

HTTP/2 503
content-type: application/json

{"error":"not ready"}
```

**While a service starts** (a cold start on Cloud Run: it listens before it has opened the database), a
request to any other path waits up to 5 s for the start, then gets
`503 {"error":"temporarily unavailable, send the request again"}` with `Retry-After: 5`. The app's 503
handling (Conventions → Errors) covers it: wait and send the same request again.

## Admin routes (operator only)

`/v1/admin/*` is the operator's surface: scripts (`scripts/*.sh`, `make smoke-prod`) and, since
2026-11-05, a Supabase sign-in for the operator's web page at `https://ryftlab.de/admin` (merged in
timkrebs/ryft-platform#128, pinned in #138, confirmed working by the owner on 2026-11-05). **The app never calls these
routes** and does not mirror their types; the page builds its own types from the shapes below. The
routes exist only when the api has an `ADMIN_API_KEY` (otherwise every `/v1/admin/*` path is `404`).
The Conventions apply (bodies up to 64 KiB, `X-Request-ID`, the response headers), except the per-user
rate limit, which these routes replace by the per-IP limit below.

### Who may call: two bearers

`Authorization: Bearer <token>` (the scheme is case-insensitive). The api tries the key first.

| Bearer | Accepted when | Reaches |
| --- | --- | --- |
| **The admin key** (`ADMIN_API_KEY`, unchanged) | the value matches; compared in constant time on every request; it does not depend on Supabase | every `/v1/admin/*` route |
| **A Supabase access token** (the page) | all of: it verifies like every `/v1/*` token (ES256 or RS256 signature from the project's keys, `iss`, `aud = authenticated`, `exp`); `app_metadata.role` is the string `"admin"` (never the top-level `role`, never `user_metadata`); its user has a row in the allow-list `admin_accounts` (written only by the operator, in SQL) and the account is not deleted; `aal` is `aal2` **and** the newest `totp` entry of `amr` is at most **12 hours** old (and not more than 30 s in the future) | the **Token** column of the route table below |

Granting access needs the claim and the row, so deleting the row ends a session on the very next
request; removing the claim ends it when the token expires (at most 1 h). The 12 h rule bounds what a
stolen refresh token is worth without the second factor. A Supabase user who is not allow-listed gets the
same answer as no session at all.

**Answers.** Every refusal of a bearer is the same `401`: status, headers and body are identical
whatever was wrong (no bearer, garbage, an expired or foreign token, an app user's token, a token without
the role, a user without a row), so the answer is no oracle for the key or for who is an admin. **One
exception, said plainly:** a token signed with a key id the api does not know, while it fetched the signing
keys less than a minute ago, answers `503` with `Retry-After: 5` (the refresh limit: it cannot tell a key
rotation from a forged id yet), not `401`. Anyone can send such a token, so a `503` on an admin route does not
by itself mean an outage; send the request again. In general the `503` says that the api could not complete
a key fetch or the database check for this token (the signing keys unreachable, a fetch that did not finish
within its 1 s, or the refresh limit), not that an outage is proven.

| Situation | Status | Body and headers |
| --- | --- | --- |
| No bearer, not the key, not a valid admin token (any reason) | `401` | `WWW-Authenticate: Bearer realm="ryft-admin"`; the error shape with the message `admin key or admin sign-in required`; no `code` |
| Valid token, `role` admin, allow-listed, but `aal` is not `aal2` or the TOTP is older than 12 h | `403` | `code` **`mfa_required`**. Only a real, allow-listed admin ever sees it |
| Valid admin token on a **key-only** route (route table below) | `403` | no `code`; message `this route needs the admin key` |
| The signing keys or the database cannot be reached while a **token** is checked, or the token names a key id the api does not know and the keys were fetched in the last minute | `503` | `Retry-After: 5`; message `authentication temporarily unavailable`. Never a reason to sign out. The key path is not affected |
| More than 10 requests a second from one client address (bursts of 20; counted per api pod, before any bearer is checked) | `429` | `Retry-After` (whole seconds); message `too many requests`. A preflight is not counted here (the per-address limit of every route, Errors → `429`, counts it) |

The message texts are for people and not pinned by tests (the `…` in the examples stands for the message; for `mfa_required` it reads `a second factor confirmed in the last 12 hours is required`); branch on the status and the `code`. What the page does: `401` →
refresh the Supabase session once and send again, a second `401` means sign in again; `403`
`mfa_required` → run the TOTP challenge (Supabase MFA) and send again with the new token; if it is still
`mfa_required`, sign in again. With `AUTH_MODE=apikey` (local development) a token is never admitted: only
the key works.

```http
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Bearer realm="ryft-admin"
X-Request-ID: 01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c

{"error": "…", "request_id": "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"}
```

```http
HTTP/1.1 403 Forbidden
X-Request-ID: 01a14ffb-0a52-7c3d-8e47-5b0d1e8c7a10

{"error": "…", "code": "mfa_required", "request_id": "01a14ffb-0a52-7c3d-8e47-5b0d1e8c7a10"}
```

### Routes and who may call them

The key reaches every route. A token reaches only the routes the page needs; the others are **key-only** and
answer a token with `403` (no `code`); the attempt is audited.

| Route | Key | Token | Used by the page |
| --- | --- | --- | --- |
| `GET /v1/admin/evaluation/metrics?since=YYYY-MM-DD` | yes | no | Anonymized coaching cohorts with at least ten accounts, up to 90 days |
| `GET /v1/admin/flags` | yes | yes | Flags, read-only: the page shows key, on or off, rollout and only the **count** of allow-listed accounts. The answer itself has no projection for a token: it carries `allow_user_ids` (user ids) to either bearer |
| `PUT`, `DELETE /v1/admin/flags/{key}` | yes | **403** | no |
| `POST /v1/admin/invites`, `GET /v1/admin/invites[?emails=1]`, `DELETE /v1/admin/invites/{code}` | yes | yes | Invites (`?emails=1` only while the page's "Show email addresses" switch is on; off by default) |
| `GET /v1/admin/feedback[?since=]`, `POST /v1/admin/feedback/{id}/resolve` | yes | yes | Feedback (`?since=` 30 days back) |
| `GET /v1/admin/users[?emails=1]` | yes | yes | Users (`?emails=1` as for invites) |
| `POST`, `DELETE /v1/admin/users/{id}/access` | yes | yes | Users: grant, revoke |
| `GET /v1/admin/users/{id}` | yes | **403** | no (sync details; `scripts/user.sh`) |
| `POST /v1/admin/users/{id}/merge-into/{to}` | yes | **403** | no (refused with `409 merge_has_connections` while `{id}` holds a connection, below) |
| `POST /v1/admin/users/{id}/resync-hint` | yes | **403** | no |
| `GET /v1/admin/audit[?since=]` | yes | yes | Audit, read-only (no `since`, so the default 7 days) |

`{id}` is a user id or its first 8 hex digits (`404` when none matches, `409` when the short id is
ambiguous). An unknown path under `/v1/admin/` is `404` after the bearer check.

### CORS (the page's origin only)

Only `/v1/admin/*` sends CORS headers; no other route does (`GET /v1/config` with an `Origin` has no
`Access-Control-*` header). The allowed origins come from the api's `ADMIN_WEB_ORIGINS` (a comma-separated
list of exact origins, production `https://ryftlab.de`). **Empty or unset means no CORS at all**: a browser
page then cannot read any answer; scripts are not affected. Matching is exact string equality: no
wildcards, no `*`, no `null`.

| Request | Answer |
| --- | --- |
| `OPTIONS` (preflight) with an allowed `Origin`, an `Access-Control-Request-Method` of `GET`, `POST` or `DELETE`, and every `Access-Control-Request-Headers` entry one of `authorization`, `content-type` (case-insensitive) | `204`, no body, no bearer needed: `Access-Control-Allow-Origin: <that origin>`, `Access-Control-Allow-Methods: GET, POST, DELETE`, `Access-Control-Allow-Headers: Authorization, Content-Type`, `Access-Control-Max-Age: 600`, `Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers` |
| `OPTIONS` from another origin, or asking for another method or header (`PUT` included) | `204` **without** any `Access-Control-*` header (the browser refuses it) |
| Any other request with an allowed `Origin` | served as usual (`401`, `403` and `429` too); the answer adds `Access-Control-Allow-Origin: <origin>`, `Access-Control-Expose-Headers: X-Request-ID, Retry-After` and `Vary: Origin` |
| Any other request without an `Origin` or with another one | served as usual with `Vary: Origin` and no `Access-Control-*`; it still needs a bearer |

There is no `Access-Control-Allow-Credentials` (no cookies exist; the bearer is the only credential), so the
page sends `credentials: 'omit'`. CORS is hygiene, not access control: the bearer is. Answers that never reach
the CORS layer carry no CORS headers: the per-address limit's plain-text `429` (Errors → `429`; it runs before
everything else, preflights included) and the errors of Google's front end itself (a `502`). A browser reports
them as a network error without a status: show "no answer" and no request id.

```http
OPTIONS /v1/admin/invites HTTP/1.1
Origin: https://ryftlab.de
Access-Control-Request-Method: POST
Access-Control-Request-Headers: authorization,content-type

HTTP/1.1 204 No Content
Access-Control-Allow-Origin: https://ryftlab.de
Access-Control-Allow-Methods: GET, POST, DELETE
Access-Control-Allow-Headers: Authorization, Content-Type
Access-Control-Max-Age: 600
Vary: Origin, Access-Control-Request-Method, Access-Control-Request-Headers
```

### The actor: who did it

Every authorized admin call writes an audit row (`GET /v1/admin/audit`) that names its **actor** (type `Actor`):

| `actor.kind` | `actor.id` | Meaning |
| --- | --- | --- |
| `key` | the key's fingerprint (8 hex digits, the same value as `key_fingerprint`) | a call with `ADMIN_API_KEY` |
| `user` | the admin's user id (UUID, lower case) | a call with an admin token |

`kind` is a **closed** enum. `id` is `string?`: it is `null` only in the `created_by` of an invite created
before 2026-11-05 by the key (the fingerprint was not kept: `{"kind": "key", "id": null}`). An attempt on a
key-only route is audited too, with status `403`. `GET /v1/admin/audit?since=` takes RFC 3339 or `YYYY-MM-DD`
(default 7 days), newest first, at most 1 000 rows; another format is `422`.

`AuditRecord.actor` is new. `key_fingerprint` stays (a string; never removed) and is `""` on rows whose actor is a
user. `details` is an open object (the route adds a few values, never an email): a create or revoke of an invite holds
its `code`, a list of users or invites holds `emails` (whether the addresses were asked for).

<!-- type: []model.AuditRecord -->
```json
[
  { "id": 9142, "at": "2026-11-05T18:02:44Z", "key_fingerprint": "",
    "actor": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "action": "POST /v1/admin/invites", "target_user": null, "status": 201,
    "request_id": "01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c", "details": { "code": "RYFT-7K3M-QX9P" } },
  { "id": 9141, "at": "2026-11-05T17:58:03Z", "key_fingerprint": "",
    "actor": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "action": "PUT /v1/admin/flags/{key}", "target_user": null, "status": 403,
    "request_id": "01a14ff9-91c0-7a2e-b6d3-0f4e5a6b7c8d", "details": {} },
  { "id": 9140, "at": "2026-11-05T09:12:31Z", "key_fingerprint": "3fa1c2d9",
    "actor": { "kind": "key", "id": "3fa1c2d9" },
    "action": "GET /v1/admin/users", "target_user": null, "status": 200,
    "request_id": "01a14ff1-0b7a-7d21-a3c4-5e6f708192a3", "details": { "emails": false, "n": 12 } }
]
```

### Invites for a person

An invite code (`RYFT-7K3M-QX9P`; case, spaces and dashes never matter where a code is read) is either
**unbound** (anyone with the code, today's behaviour) or **bound to an email address**. The tester opens the
universal link `https://ryftlab.de/invite/RYFT-7K3M-QX9P` (the `link` field) or types the code in the app
(`POST /v1/me/invite`).

#### `POST /v1/admin/invites` — create a code

| Field | Type | Rule |
| --- | --- | --- |
| `note` | string | optional, at most 200 characters (`422 "note has at most 200 characters"`); may name the person; never shown to the tester |
| `max_uses` | int? | 1 to 1000, default 1 (`422 "max_uses must be 1-1000"`) |
| `expires_at` | RFC 3339? | must be in the future (`422 "expires_at must be in the future"`); **omitted means it never expires** (the page's default is 14 days; its "Never", offered only for a code without `email`, omits the field), **except for a code with an `email`**: that always expires, so an omitted `expires_at` becomes 14 days from now, and one more than 90 days away is `422 "expires_at is at most 90 days away for a code for one email"` |
| `email` | string? (**new**) | the address this code is for. Trimmed and lower-cased; ASCII printable, at most 254 characters, one `@`, a dot in the domain, else `422 "email is not a valid address"`. A bound code has `max_uses` 1: a larger value is `422 "a code for one email has max_uses 1"`. `null` or omitted: an unbound code. The api never looks the address up. A code for an email is only made while bound codes can be redeemed safely: the Supabase project must confirm emails (*Confirm email* on) and the api must be able to ask Supabase for an account. Otherwise `422` with `"email-bound invites are not available: the Supabase project confirms no emails (Confirm email is off)"`, `"email-bound invites are not available right now: the Supabase sign-up settings could not be read"` (the api re-reads the setting every hour) or `"email-bound invites are not available: the api has no Supabase admin key"`. A code without `email` is never affected |

<!-- type: model.BetaInviteInput -->
```json
{ "note": "Anna", "email": "Anna@Example.com", "max_uses": 1, "expires_at": "2026-11-19T10:00:00Z" }
```

`201` with the new `BetaInvite`. In this answer `email` is the address **as stored** (lower case, not masked:
the caller just sent it), `uses_by` is `[]` and `created_by` is the caller's actor.

<!-- type: model.BetaInvite -->
```json
{ "code": "RYFT-7K3M-QX9P", "note": "Anna", "max_uses": 1, "uses": 0,
  "expires_at": "2026-11-19T10:00:00Z", "created_at": "2026-11-05T10:00:00Z",
  "email": "anna@example.com", "created_by": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
  "revoked_at": null, "status": "open", "link": "https://ryftlab.de/invite/RYFT-7K3M-QX9P",
  "uses_by": [] }
```

#### `GET /v1/admin/invites[?emails=1]` — the list

Still a JSON **array** of `BetaInvite`, newest first, without pagination (beta size). Every code is listed,
revoked and used-up ones too. The new fields are all additive:

| Field | Type | Meaning |
| --- | --- | --- |
| `code`, `max_uses`, `uses`, `expires_at`, `created_at` | as before | unchanged |
| `note` | string | as before; the api sets it to `""` 30 days after the code could no longer be redeemed (revoked, expired or used up), bound or not (D161, D249). An `open` code keeps its note |
| `email` | string? | the bound address; `null` for an unbound code. **Masked** (`a***@e***.com`) unless `?emails=1`. The api clears it, and the code's `note`, 30 days after the code could no longer be redeemed (revoked, expired or used up), so `null` on a code that is not `open` does **not** mean "anyone with the code". A bound code always has an `expires_at` |
| `created_by` | `Actor` | who created it; `{"kind": "key", "id": null}` for codes from before 2026-11-05 |
| `revoked_at` | RFC 3339? | set by `DELETE`; `null` otherwise |
| `status` | string | **closed** enum, the first match wins: `revoked` (`revoked_at` set), `used` (`uses` ≥ `max_uses`), `expired` (`expires_at` not in the future), `open` |
| `link` | string | `<PUBLIC_WEB_URL>/invite/<code with dashes>`, i.e. `https://ryftlab.de/invite/RYFT-7K3M-QX9P`. Always present |
| `uses_by` | `[InviteUse]` | the accounts that redeemed the code, oldest first |

`InviteUse`: `user_id` (uuid), `short_id` (its first 8 hex digits), `email` (string?, the account's email, masked unless
`?emails=1`; `null` when the account has none), `private_relay` (bool, an Apple "Hide My Email" address) and `at`
(RFC 3339). `uses_by` lists only accounts that still exist: `uses` stays the count, so `uses > len(uses_by)` means
"redeemed by an account deleted since". Codes redeemed before 2026-11-05 are listed with `at` = the account's
`beta_access_at`. The call is audited with `emails` true or false. The page passes `?emails=1`:

<!-- type: []model.BetaInvite -->
```json
[
  { "code": "RYFT-7K3M-QX9P", "note": "Anna", "max_uses": 1, "uses": 1,
    "expires_at": "2026-11-19T10:00:00Z", "created_at": "2026-11-05T10:00:00Z",
    "email": "anna@example.com", "created_by": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "revoked_at": null, "status": "used", "link": "https://ryftlab.de/invite/RYFT-7K3M-QX9P",
    "uses_by": [ { "user_id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "short_id": "9f8e7d6c", "email": "anna@example.com",
                   "private_relay": false, "at": "2026-11-05T18:02:11Z" } ] },
  { "code": "RYFT-H2NP-4WCX", "note": "Ben", "max_uses": 1, "uses": 0,
    "expires_at": "2026-11-05T11:30:00Z", "created_at": "2026-11-05T09:00:00Z",
    "email": "ben@example.com", "created_by": { "kind": "key", "id": "3fa1c2d9" },
    "revoked_at": "2026-11-05T11:30:00Z", "status": "revoked", "link": "https://ryftlab.de/invite/RYFT-H2NP-4WCX",
    "uses_by": [] },
  { "code": "RYFT-A9KT-3DMF", "note": "TestFlight wave 2", "max_uses": 20, "uses": 3,
    "expires_at": null, "created_at": "2026-10-20T08:00:00Z",
    "email": null, "created_by": { "kind": "key", "id": "3fa1c2d9" },
    "revoked_at": null, "status": "open", "link": "https://ryftlab.de/invite/RYFT-A9KT-3DMF",
    "uses_by": [ { "user_id": "0a1b2c3d-4e5f-4061-8283-949a5b6c7d8e", "short_id": "0a1b2c3d", "email": "mia@icloud.com",
                   "private_relay": false, "at": "2026-10-22T07:41:00Z" },
                 { "user_id": "7c6b5a49-3827-4160-8f0e-1d2c3b4a5968", "short_id": "7c6b5a49", "email": "x7k2m9q4@privaterelay.appleid.com",
                   "private_relay": true, "at": "2026-10-23T19:05:30Z" } ] }
]
```

Without `?emails=1` the same first entry reads:

<!-- type: []model.BetaInvite -->
```json
[
  { "code": "RYFT-7K3M-QX9P", "note": "Anna", "max_uses": 1, "uses": 1,
    "expires_at": "2026-11-19T10:00:00Z", "created_at": "2026-11-05T10:00:00Z",
    "email": "a***@e***.com", "created_by": { "kind": "user", "id": "1b2c3d4e-5f60-4718-9a2b-3c4d5e6f7081" },
    "revoked_at": null, "status": "used", "link": "https://ryftlab.de/invite/RYFT-7K3M-QX9P",
    "uses_by": [ { "user_id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "short_id": "9f8e7d6c", "email": "a***@e***.com",
                   "private_relay": false, "at": "2026-11-05T18:02:11Z" } ] }
]
```

#### `DELETE /v1/admin/invites/{code}` — revoke

**The meaning of this route changes.** `DELETE` no longer removes the row: it **revokes** the code. The row stays
in the list with `status: "revoked"`; a hard delete is not offered. A revoked code redeems nothing, and accounts that
already redeemed it keep their access. `204` with no body, also when the code was already revoked (idempotent; the
first revoke time is kept); `404 "no such code"` for a code that does not exist. `{code}` may be written with or
without dashes. A revoke also sets `expires_at` to the revoke time (an earlier value is kept), so the listed
`expires_at` of a revoked code is not the one it was created with: read `status`.

#### Redeeming: `POST /v1/me/invite`

The request and the answers are unchanged: `{"code": "…"}` → `204`, and **one** `422 "invalid or used code"` for
every refusal (unknown, expired, used up, and now also **revoked**, **already redeemed by this account**, **bound to another email**, and bound while the
account's email is not confirmed). The binding compares the email address Supabase holds for the account, and only
when Supabase has confirmed it (not the token's `email` claim, not the app's stored copy), trimmed and lower-cased,
with no plus-address or dot folding. A Sign in with Apple "Hide My Email" address matches only a code made for exactly
that relay address: when the tester used it and the operator does not have it, the operator leaves `email` empty.
While the Supabase project does not confirm emails (*Confirm email* off) or the api has no Supabase admin key, a bound code redeems for nobody: the same `422`; while the project's setting cannot be read, the same `503` as for an account lookup that fails. A bound code has one use, so an account cannot redeem it twice (a second attempt is the `422`; an account that already
has access still gets `204` without using the code). New for **bound** codes only: when Supabase's account lookup is
unavailable the answer is `503` with `Retry-After: 5` and no `code` (`"the invite could not be checked right now"`);
send the same request again. Unbound codes never depend on that lookup. The 5 attempts an hour apply as before. The
app's `InvitePost` and its handling of this route do not change.

### Users, feedback, flags (existing shapes)

These shapes are unchanged **for the admin key**; they are listed because the page uses them. `AdminUser.email` is
present only with `?emails=1`. **An operator's token gets a smaller answer for the users list and the feedback**
(the next two subsections): the server leaves out the health-derived data-quality fields, the device fields, the
consent version, the request ids and the diagnostics, so none of them reaches a browser. The key keeps the whole
answers (only fields are omitted for a token; no field of the key's answer changes).

| Route | Answer |
| --- | --- |
| `GET /v1/admin/users[?emails=1]` | `200` `[AdminUser]` for the key, `[AdminUserBrief]` for a token |
| `POST /v1/admin/users/{id}/access` | `204`: beta access without a code |
| `DELETE /v1/admin/users/{id}/access` | `204`: takes it away |
| `GET /v1/admin/feedback[?since=]` | `200` `[Feedback]` for the key, `[FeedbackBrief]` for a token, newest first, at most 500; `since` is RFC 3339 or `YYYY-MM-DD` (default 7 days), else `422` |
| `POST /v1/admin/feedback/{id}/resolve` | `204`; `400` when `id` is not a number, `404 "no such feedback"` |
| `GET /v1/admin/flags` | `200` `[FeatureFlag]` ordered by `key` |

<!-- type: []model.AdminUser -->
```json
[
  { "id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "short_id": "9f8e7d6c", "created_at": "2026-11-05T18:00:02Z",
    "provider": "email", "email": "anna@example.com", "beta_access": true, "consent_version": "2026-10-26",
    "app_version": "0.9.0 (41)", "os_version": "26.1", "last_sync_at": "2026-11-05T18:20:00Z",
    "sources": [ { "name": "Apple Watch", "kinds": ["hr", "hrv", "sleep"] } ],
    "days_scored_14d": 1, "nights_with_stages_14d": 1, "days_with_hrv_14d": 1, "calibrating": true,
    "max_hr_source": "tanaka", "open_feedback": 0, "dup": 0 }
]
```

<!-- type: []model.Feedback -->
```json
[
  { "id": 42, "user_id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "category": "bug",
    "message": "Sleep stages are empty for last night.", "app_version": "0.9.0", "build": "41",
    "device_model": "iPhone17,1", "os_version": "26.1", "request_ids": ["01a14ffa-eb7c-73e3-9cf9-262c70d5ed9c"],
    "diagnostics": null, "created_at": "2026-11-05T07:30:00Z", "resolved_at": null }
]
```

#### What an operator's token gets of the users list and the feedback

`AdminUserBrief` (the same rows, same order, same `?emails=1` rule): `id`, `short_id`, `created_at`, `provider`,
`email` (only with `?emails=1`), `beta_access`, `last_sync_at`. **Left out:** `consent_version`, `app_version`,
`os_version`, `sources`, `days_scored_14d`, `nights_with_stages_14d`, `days_with_hrv_14d`, `calibrating`,
`max_hr_source`, `open_feedback`, `dup`.

<!-- type: []model.AdminUserBrief -->
```json
[
  { "id": "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a", "short_id": "9f8e7d6c", "created_at": "2026-11-05T18:00:02Z",
    "provider": "email", "email": "anna@example.com", "beta_access": true, "last_sync_at": "2026-11-05T18:20:00Z" }
]
```

`FeedbackBrief`: `id`, `user_short_id` (the account's first 8 hex digits, not its id), `category`, `message`,
`created_at`, `resolved_at`. **Left out:** `user_id`, `app_version`, `build`, `device_model`, `os_version`,
`request_ids`, `diagnostics`.

<!-- type: []model.FeedbackBrief -->
```json
[
  { "id": 42, "user_short_id": "9f8e7d6c", "category": "bug",
    "message": "Sleep stages are empty for last night.", "created_at": "2026-11-05T07:30:00Z", "resolved_at": null }
]
```

<!-- type: []model.FeatureFlag -->
```json
[
  { "key": "train.import", "enabled": true, "rollout_pct": 0,
    "allow_user_ids": ["9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"], "updated_at": "2026-10-26T12:00:00Z" }
]
```

The key-only routes (`GET /v1/admin/users/{id}`, merge, resync-hint, flag writes) are operator tooling for
`scripts/user.sh` and the flag scripts; their shapes are not part of this contract. One behaviour is (Connections,
D257; from the platform PR `connect/foundation`): `POST /v1/admin/users/{id}/merge-into/{to}` refuses, also with
`force=1`, with `409` and `code` `merge_has_connections` while the account `{id}` holds any connection; the dry run
reports the count. Ask the user to disconnect first.

## Swift mirror

Codable types for the iOS app, matching the shapes above with the app's decoder
(`.convertFromSnakeCase`, RFC 3339 dates with or without fractional seconds). They
extend `Models.swift`: `DailyScore`, `DayDetail`, `DayStrain`, `Workout`, `User`,
`UserPatch` and the sleep types replace the existing structs (new fields only), the rest
are new. The sync request types are not mirrored here; on the app's `Sample` and `SleepSegment` add `id: UUID?`
(the HealthKit object UUID) and on `SyncRequest` `deleted: [DeletedRecord]?` (below). The response types `IgnoredCounts` and `SyncResponse` gain the fields below. The sample type `hrv_rmssd` (Connections) needs no case on the app's `SampleType` unless it ever sends one (Apple Health has none); `samples_7d` keys stay strings. A logged set is `SessionSet` in Swift (`Set` is taken). `scripts/check_swift.sh` compiles this block and decodes every
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
    let sleepOrigin: SourceOrigin?     // Connections: null without a night; absent before connect/scoring
    let hrvMetric: HRVMetric?          // Connections: null without HRV; absent with an hrvMs = .sdnn
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
    let duplicateOf: UUID?            // only with GET /v1/workouts?include_hidden=true, on a hidden duplicate
    var sessionRpe: Double? = nil     // D293, not served yet: omitted without a rating
    var sessionRpeLoad: Int? = nil    // minutes × sessionRpe (Foster); omitted without a rating
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
    let origin: SourceOrigin?         // Connections: absent (servers before connect/foundation) = .healthkit
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
    var sources: [SyncSource]? = nil           // D297, not served yet: absent until then
    var freshness: SyncFreshness? = nil        // absent: .scoresPending if pendingDays holds a day of the last 30
    var scoresPendingSince: Date? = nil
}

enum SyncFreshness: String, Codable { case current; case scoresPending = "scores_pending" }   // closed
enum SyncSourceStatus: String, Codable { case ok, pending, error }                          // closed

struct SyncSource: Codable {    // GET /v1/me/sync → sources (D297)
    let source: String          // "healthkit" or a provider id (open set: ignore unknown ids)
    let lastSyncAt: Date?
    let status: SyncSourceStatus
    let reason: String?         // open set: "needs_reauth", "consent_required", "invite_required"
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
    let analytics: AnalyticsConfig?   // null without a project key on the server; send only with flags["analytics.enabled"] and the opt-in
    let appAttest: String?  // "off" | "warn" | "on" (closed; D247); absent from a server before D247 (a rollback), so optional; a 501/404 from POST /v1/app-attest/challenge stays authoritative
}

struct AnalyticsConfig: Codable {   // RemoteConfig.analytics (PostHog EU Cloud)
    let host: String         // https origin
    let projectKey: String   // phc_…; never logged
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
    let connectionsDeregistered: Bool?   // Connections: null (none to end) or absent; false = retried in the background
}

struct AppleAuthorization: Codable {   // POST /v1/me/apple-authorization
    let code: String
}

// App Attest (D245, D247): 501 until the server has its token key (404 on a server before D247).
// Every request also carries X-RYFT-App-Token and X-RYFT-App-Build (CFBundleVersion; optional).
struct AppAttestChallenge: Codable {   // POST /v1/app-attest/challenge (no body)
    let challenge: String   // opaque; clientDataHash = SHA-256 of its UTF-8 bytes; send it back as is, never decode it
    let expiresAt: Date     // 5 minutes; use it at once
}

struct AppAttestKeyPost: Encodable {   // POST /v1/app-attest/keys
    let keyId: String         // as DCAppAttestService.generateKey returned it: standard base64, 44 characters
    let attestation: String   // attestKey's Data, .base64EncodedString()
    let challenge: String
}

struct AppAttestKey: Codable {   // 201 of POST /v1/app-attest/keys
    let keyId: String
}

struct AppAttestTokenPost: Encodable {   // POST /v1/app-attest/token
    let keyId: String
    let challenge: String     // a new one for every call
    let assertion: String     // generateAssertion's Data, .base64EncodedString()
}

struct AppAttestToken: Codable {   // 200 of POST /v1/app-attest/token
    let token: String         // opaque; the X-RYFT-App-Token header; never parse or log it
    let expiresAt: Date
    let ttlSec: Int           // 3600 today, never below 600; the next token when under min(300, ttlSec / 2) s are left, on the app's own clock
}

// ---- Connections (D257; not served yet: 404 without a code before connect/foundation) ----

enum SourceOrigin: String, Codable { case healthkit, direct }   // UserSource.origin, ScoreCoverage.sleepOrigin; closed
enum HRVMetric: String, Codable { case sdnn, rmssd }            // ScoreCoverage.hrvMetric; closed
enum ConnectionStatus: String, Codable {                        // closed; a flow waiting for its confirm is never listed
    case connected, needsReauth = "needs_reauth", revoked
}

struct Connection: Codable, Identifiable {   // GET /v1/me/connections → connections[]; 200 of POST …/confirm
    let provider: String          // open id ^[a-z][a-z0-9]{1,31}$: "garmin"; ignore an id this build has no copy for
    let status: ConnectionStatus
    let connectedAt: Date
    let lastDataAt: Date?         // null until the first record
    let provides: [SourceKind]    // closed vocabulary of UserSource.kinds
    let history: String           // open: "none" today; read an unknown value as "none"
    let providerUserId: String?   // null when revoked; show, don't interpret, never log
    let permissions: [String]     // the provider's own words, open; [] when revoked
    var id: String { provider }
}

struct AvailableConnection: Codable, Identifiable {   // GET /v1/me/connections → available[]
    let provider: String
    let provides: [SourceKind]
    let history: String
    let accepting: Bool           // false: the user cap is full (start answers 409 connection_limit); true for a reconnect
    var id: String { provider }
}

struct ConnectionsResponse: Codable {   // GET /v1/me/connections
    let connections: [Connection]
    let available: [AvailableConnection]   // authoritative for what to offer, not the flag
}

struct ConnectStart: Codable {   // 200 of POST /v1/me/connections/{provider}/start (no body)
    let authorizeUrl: String     // https; open as is in ASWebAuthenticationSession (callback scheme "ryft"); never parse, log or store it
    let expiresAt: Date          // 10 minutes
}

struct ConnectConfirmPost: Encodable {   // POST /v1/me/connections/{provider}/confirm → Connection
    let ticket: String           // from the redirect, unchanged: 32 lowercase hex characters, single use
}

struct ConnectionDeleted: Codable {   // DELETE /v1/me/connections/{provider}
    let status: String            // "disconnected"
    let deregistered: Bool?       // null: nothing to end; false: retried in the background for up to 30 days
    let dataDeleted: Bool         // always true
}

enum ConnectCallbackStatus: String { case ok, denied, expired, error }   // closed

struct ConnectCallback {   // the redirect ryft://connect/{provider}?status=…[&ticket=…] from GET /v1/connect/{provider}/callback
    let provider: String
    let status: ConnectCallbackStatus
    let ticket: String?           // with .ok only

    init?(url: URL) {
        guard url.scheme == "ryft", url.host == "connect",
              let items = URLComponents(url: url, resolvingAgainstBaseURL: false)?.queryItems else { return nil }
        // exactly one path component, a well-formed provider id ("unknown" included: reserved, read as an error)
        let parts = url.path.split(separator: "/", omittingEmptySubsequences: false)
        guard parts.count == 2, parts[0].isEmpty,
              String(parts[1]).range(of: "^[a-z][a-z0-9]{1,31}$", options: .regularExpression) != nil else { return nil }
        self.provider = String(parts[1])
        // an unknown status is read as .error; other query items are ignored
        let s = items.first(where: { $0.name == "status" })?.value.flatMap(ConnectCallbackStatus.init(rawValue:)) ?? .error
        status = provider == "unknown" ? .error : s
        ticket = status == .ok ? items.first(where: { $0.name == "ticket" })?.value : nil
    }
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

// Recovery without a watch (D291–D295; not served yet: 404 without a code until then, and while
// flags["recovery.checkin"] is off).
enum RecoverySource: String, Codable { case wearable, checkin, none }        // closed
enum RecoveryConfidence: String, Codable { case high, medium, low }          // closed
enum SuggestionBasis: String, Codable { case checkins7d = "checkins_7d", expectedBedtime = "expected_bedtime" }   // closed

struct RecoveryCheckinPut: Codable {   // PUT /v1/recovery/checkins/{date}; every field required
    let bedtime: Date
    let wakeTime: Date
    let tzOffsetSec: Int                // TimeZone.current.secondsFromGMT(for: wakeTime)
    let perceivedSleepQuality: Int      // 1–10
    let perceivedFreshness: Int         // 1–10
    let soreness: Bool
}

struct RecoveryComponents: Codable {   // each 0–100; a nil part is left out of the score
    let wellness: Int
    let wellnessAbsolute: Int
    let wellnessPersonal: Int?
    let duration: Int
    let regularity: Int?
    let n2r: Int?
}

struct RecoveryCheckin: Codable {
    let date: String                    // the wake day, YYYY-MM-DD
    let bedtime: Date
    let wakeTime: Date
    let tzOffsetSec: Int
    let perceivedSleepQuality: Int
    let perceivedFreshness: Int
    let soreness: Bool
    let sleepDurationMin: Int
    let recoveryScore: Int
    let components: RecoveryComponents
    let acuteDrop: Bool
    let confounders: [String]           // open: ignore values this build doesn't know
    let confidence: RecoveryConfidence  // .medium or .low
    let superseded: Bool                // a wearable recovery exists for the day
    let n2rBedtimeApplied: Bool
    let modelVersion: String
    let createdAt: Date
    let updatedAt: Date
}

struct RecoveryStatus: Codable {   // GET /v1/recovery/status; CoachingBriefing.recovery
    let date: String
    let source: RecoverySource
    let confidence: RecoveryConfidence?
    let recoveryScore: Int?
    let needsCheckin: Bool
    let checkin: RecoveryCheckin?
    let suggestedBedtime: String?       // local HH:MM, a pre-fill, not advice
    let suggestedWakeTime: String?
    let suggestionBasis: SuggestionBasis?
}

struct WorkoutRPEPut: Codable {   // PUT /v1/workouts/{id}/rpe
    let sessionRpe: Double              // 1–10, steps of 0.5
}

enum APIErrorCode: String, Codable {
    case inviteRequired = "invite_required", consentRequired = "consent_required"
    case importInProgress = "import_in_progress"   // 429 on POST /v1/train/import; wait Retry-After
    case importNotEnabled = "import_not_enabled"   // 403 on POST /v1/train/import: the flag is off for the account
    case mfaRequired = "mfa_required"              // 403 on /v1/admin/* only; the app never receives it
    case appAttestRequired = "app_attest_required"                     // 403 under APP_ATTEST=on: a new token, send again once; keep the write queued
    case appAttestChallengeInvalid = "app_attest_challenge_invalid"   // 422 on /v1/app-attest/keys and /token: a new challenge, the same step once
    case appAttestInvalid = "app_attest_invalid"                       // 422 on /v1/app-attest/keys and /token: drop the key_id, attest a new key once
    case appAttestKeyUnknown = "app_attest_key_unknown"                // 404 on POST /v1/app-attest/token: drop the key_id, attest a new key
    case unknownProvider = "unknown_provider"                // Connections: 404 on start/confirm: hide the provider's Connect entry
    case connectionLimit = "connection_limit"                // Connections: 409 on start/confirm: the provider is full; no automatic retry
    case connectTicketInvalid = "connect_ticket_invalid"     // Connections: 409 on confirm: GET /v1/me/connections; not connected: start again
    case connectExpired = "connect_expired"                  // Connections: 410 on confirm: GET /v1/me/connections; not connected: start again
    case mergeHasConnections = "merge_has_connections"       // 409 on /v1/admin/* only; the app never receives it
}
// App Attest (D245, D247): the four app_attest_* cases reach the app only once the server has its token key
// (until then the three routes answer 501). A build without them reads them as no code (Errors → App Attest).
// Connections (D257): the five connection codes are sent from the platform PR connect/foundation on (Errors → Connections).

struct APIErrorBody: Codable {
    let error: String
    var code: APIErrorCode? = nil  // closed enum; branch on it
    let requestId: String?   // same as the X-Request-ID header
}
```

## Changelog

| Date | Change |
| --- | --- |
| 2026-11-27 | **[The sync button](#the-sync-button-sources-freshness-scores_pending_since-d297-d300) is served: `GET /v1/me/sync` answers `sources`, `freshness`, `scores_pending_since`** (D297, D300; the implementation PR, branch `feat/sync-status`). No shape change from the contract of #258 (`docs: the sync status contract`): no field renamed, removed or retyped, no new route; the three fields are present in every answer, `sources` is never `null` and always holds `healthkit`. **Behaviour:** as the contract says. `healthkit`'s `last_sync_at` is the route's `last_sync_at`; its `status` is `error` with `invite_required` or `consent_required` only while that gate is `on` (in `warn` mode the status stays `ok` and `gate` is set, as before). A provider is listed only while its connection is `connected` or `needs_reauth`, the server serves it and `connect.<provider>` is on for the account; `needs_reauth` is `error`, a delivery waiting to be stored is `pending`. `freshness` is `scores_pending` while `pending_days` holds today or yesterday, a day from today − 30 through today (not after it) queued in the last 10 minutes, or a source is `pending`. **No consent recorded (D300):** scoring now closes the queued days of an account with no consent recorded (while the consent gate is `on`), so they leave `pending_days` and no longer hold `freshness`; they are not scored. An account that consented to an older policy is still scored: its `healthkit` source shows `consent_required` meanwhile, and nothing is closed. The cache stays 10 s. **Old apps:** ignore the fields. **The app:** nothing new; keep the fallback for an older server. The four examples under the section are typed now. |
| 2026-11-27 | **[The sync button](#the-sync-button-sources-freshness-scores_pending_since-d297-d300): `GET /v1/me/sync` gains `sources`, `freshness`, `scores_pending_since`** (D297; platform PR `docs: the sync status contract`, branch `docs/sync-status-contract`; the head-architect's brief of 2026-11-27). Additive; docs only, so the app can build it before the server has it: no v1 field renamed, removed or retyped, no new route. **Fields:** `sources` (`[SyncSource]`: `source`, an open set, `healthkit` or a provider id; `last_sync_at`; `status` `ok`/`pending`/`error`, closed; `reason`, an open set, `needs_reauth`/`consent_required`/`invite_required`); `freshness` (`current`/`scores_pending`, closed: `scores_pending` when today's scores and readiness do not include stored data yet, i.e. `pending_days` holds today or yesterday, or a day of the last 30 queued in the last 10 minutes, or a source is `pending`); `scores_pending_since` (timestamp?; a re-queued day restarts it). **Not tracked:** the N2R morning baseline, the coaching briefing built on it, and the pilot's partner deliveries follow within one 15-minute pass. The brief's names map onto the contract's conventions: `synced_at` is the existing `last_sync_at`, `last_sync_utc` is `last_sync_at` (every time here is UTC), `OK`/`PENDING`/`ERROR` are lower case, and `data_freshness` is `freshness` with only what the server knows (`UP_TO_DATE` and `STALE` are the app's 30-minute rule, `SYNCING_CLOUD` is `scores_pending`). **Behaviour:** none yet. A provider is listed only while its connection is `connected` or `needs_reauth`, the server serves it and its flag `connect.<provider>` is on for the account, so Garmin and Suunto stay absent until Connections is served. The coaching pilot's partner links are not sources. `POST /v1/sync` does not change and does not wait for scoring. **Old builds:** ignore the new fields. **The app:** the five-step rule under The sync button: after a sync, poll this route no sooner than `max(expected_within_sec, 10)` s, at that interval, for at most 3 minutes, then once at the next foreground or silent push; reload today and the briefing when `freshness` turns `current`, and only then turn the button green. Read absent fields as an older server (`freshness` from `pending_days` holding a day of the last 30). **Swift:** `SyncFreshness`, `SyncSourceStatus`, `SyncSource`; `SyncStatus.sources`, `.freshness`, `.scoresPendingSince`. The examples are `http` blocks until the server has the model types. |
| 2026-11-27 | **[Nutrition food selection](#nutrition-food-selection): BLS 4.0 foods and a staple tie-break** (D296; platform PR `nutrition-etl: the full BLS 4.0 catalog with German names`, branch `feat/nutrition-bls-full`). No shape change: no field is added, renamed, removed or retyped. **Behaviour:** once the operator imports it, the catalog gains about 7,140 generic foods from the Bundeslebensmittelschlüssel 4.0, each with a German name, an English name and curated search synonyms as aliases (`Karotte` finds `Möhre, roh`). They have no household portions, only `reference_portion` (100 g). `sources[].attribution` names the Max Rubner-Institut (CC BY 4.0), as for the three BLS staples. Ranking: when two foods tie on match quality, a curated staple now ranks first (migration 0061). Before, the caller's market decided, so an exact German BLS name could outrank the staple whose alias it equals. **Old builds** see more German results and nothing else. **The app:** show the source attribution as for the BLS staples, and name the BLS on the About or licences screen (wording in D296). **Swift:** none. |
| 2026-11-26 | **[Recovery without a watch](#recovery-without-a-watch--check-ins-and-session-rpe-d291d295-not-served-yet)** (D291–D295; platform PR `docs: the no-watch recovery contract`, branch `docs/no-watch-recovery-contract`; the head-architect's brief of 2026-11-26). Additive; docs only, so the app can build it before the server has it: no v1 field renamed, removed or retyped. **New routes:** `PUT /v1/recovery/checkins/{date}` (an idempotent upsert per wake day, body `RecoveryCheckinPut`: `bedtime`, `wake_time`, `tz_offset_sec`, `perceived_sleep_quality` and `perceived_freshness` 1–10, `soreness`; 14 days back; `200 RecoveryCheckin`), `GET /v1/recovery/checkins?from&to` (`/v1/days` semantics, ≤ 400 days; the export), `GET`/`DELETE /v1/recovery/checkins/{date}` (`DELETE` `204`, idempotent), `GET /v1/recovery/status` (`RecoveryStatus`: `source` `wearable`/`checkin`/`none`, `confidence` `high`/`medium`/`low`/`null`, `needs_checkin`, the pre-fill `suggested_bedtime`/`suggested_wake_time` with `suggestion_basis`; all three enums closed), `PUT`/`DELETE /v1/workouts/{id}/rpe` (body `WorkoutRPEPut` `{session_rpe}`, 1–10 in halves; `200 Workout`). **Fields:** `Workout.session_rpe` (number?) and `session_rpe_load` (int?, minutes × RPE), omitted without a rating, read-only; `CoachingBriefing.recovery` (`RecoveryStatus?`, `null` while the flag is off); `N2RScore.bedtime_source` (`expected`/`wearable`/`checkin`, closed; absent before). **Values:** `CoachingBriefing.readiness` gains `LOW_RECOVERY_UNEXPLAINED` (a check-in well below the user's usual with no lifestyle explanation; treat unknown values as `UNKNOWN`), and `readiness_reasons` gains `checkin_below_usual`, `checkin_low_quality_and_freshness`, `soreness_reported` and the `journal_*` confounders (including `journal_sick`, `journal_travel`, `journal_work_stress`); `CoachingBriefing.readiness_display` (string, the neutral name to show: `readiness`, with `AUTONOMIC_FATIGUE` shown as `LOW_RECOVERY_UNEXPLAINED`; `readiness` unchanged). **Decided 2026-11-27** (the user, D291–D294): the Today ring shows a check-in's `recovery_score`, marked as estimated from `source`/`confidence`, while `DailyScore.recovery` stays wearable-only; RPE feeds no day strain; soreness has zero weight; sick, travel and work stress explain a drop; no special case for sleep-only trackers; hand-typed sleep stays counted as measured; Apple's effort score is not read; testers by the allow-list only. **Flag:** `recovery.checkin`, enabled at 0 %; off: the writes and the status `404` without a `code`, reads and deletes keep working. **Codes:** none new; `consent_required` on both writes (`warn` acts as `on`). **Behaviour:** none yet; the implementation PR (migration 0061+) serves it, bumps the scoring model (a changelog entry in `GET /v1/model`) and counts a rated workout without heart rate or power in the coaching training load as Banister's TRIMP with `session_rpe / 10` as the reserve fraction (60 min at 8: 151); day strain does not change. One rule, the **wearable night** (asleep stages or overnight HRV, and a recovery), decides the tiers, `needs_checkin`, `superseded` and the readiness path. The flag is a kill switch: while off, stored check-ins and ratings count nowhere. A `404` without a `code` from these routes means "hide the feature", never "drop local data". **Old builds:** ignore the new fields and never call the routes. **The app:** show the check-in when `needs_checkin`; pre-fill it from the suggestions; send `tz_offset_sec` for the wake time; show `superseded` check-ins as history only; with `source` `checkin`, fill the Today ring from `RecoveryStatus.recovery_score` and mark it as estimated; ask for the RPE on a workout without heart rate (a Train session: `Session.workout_id`); key readiness copy on `readiness_display` (fallback `readiness`); copy never names a physiological cause. **Swift:** `RecoverySource`, `RecoveryConfidence`, `SuggestionBasis`, `RecoveryCheckinPut`, `RecoveryComponents`, `RecoveryCheckin`, `RecoveryStatus`, `WorkoutRPEPut`; `Workout.sessionRpe`, `.sessionRpeLoad`. The examples are `http` blocks until the server has the model types. |
| 2026-11-23 | **Connections** (D257; platform PR `docs: the Connections contract`, branch `connect/contract`; head-architect's brief of 2026-11-23, no `API_REQUESTS.md` entry). Additive; docs only, so the app can build it before the server has it: no v1 field renamed, removed or retyped. **New:** [Connections](#connections--direct-sources-d257-not-served-yet), a direct link to a provider's own service (`{provider}` a path parameter, an open id `^[a-z][a-z0-9]{1,31}$`; the first is `garmin`, `fake` is compose only): `GET /v1/me/connections` (`{connections: [Connection], available: [AvailableConnection]}`; `Connection` = `provider`, `status` `connected` / `needs_reauth` / `revoked` (closed), `connected_at`, `last_data_at?`, `provides` (the `kinds` vocabulary), `history` (`none`, open: unknown reads as `none`), `provider_user_id?` and `permissions` (open, the provider's words; `null` and `[]` when `revoked`); `available[].accepting` `false` at the provider's user cap, always `true` for a reconnect; `available` is authoritative, not the flag); `POST /v1/me/connections/{provider}/start` (no body → `{authorize_url, expires_at}`, 10 minutes, opaque, opened in `ASWebAuthenticationSession` with the scheme `ryft`; consent with `warn` acting as `on`, then `404 unknown_provider`, then `409 connection_limit`; its own budget, bursts of 5 then one a minute; a reconnect changes nothing until its confirm); `GET /v1/connect/{provider}/callback` (the provider's redirect, no bearer, no App Attest, no gates; always `302` to `ryft://connect/{provider}?status=ok|denied|expired|error` (closed), `&ticket=<32 lowercase hex>` with `ok` only; no token, code, provider user id or health value in the URL); `POST /v1/me/connections/{provider}/confirm` (`{ticket}` → `200 Connection`; only the confirm connects; a repeat answers `200` until 10 minutes after the callback, then `410 connect_expired`; `409 connect_ticket_invalid` (also a repeat once that connection has ended: moved, replaced, removed at the provider or disconnected); a consent refusal leaves the ticket usable; a different provider account replaces the live connection and deletes its data; a provider account connected to another RYFT account moves here and ends there as `revoked`); `DELETE /v1/me/connections/{provider}` (`{status: "disconnected", deregistered: bool?, data_deleted: true}`; idempotent; works whether or not the provider is served or its flag is on; exempt from App Attest and the beta gate, like `DELETE /v1/me/consent`; `deregistered`/`connections_deregistered` `false` also when another RYFT account holds that provider account live, a grant waiting for its confirm counts as something to end). **Cap:** never counts the caller's own open states (at most 3 per account and provider), pending grant or connection. **Flag off at confirm:** `404 unknown_provider`, the pending grant handled as an expired one; `unknown` is a reserved provider id. **Data when a connection ends** (disconnect, removed at the provider, moved to another RYFT account, replaced by another provider account, account deletion): everything received through it is deleted (samples, sleep, waiting records, the `direct` source entry; the tokens after the provider's deregistration, retried 30 days) and the touched days are rescored; the same vendor's Apple Health data and every other source stay; a `revoked` entry (no provider user id, no permissions) is listed 90 days. Deliveries while the consent is not to the policy in force are dropped, not stored. **Fields:** `DELETE /v1/me` → `connections_deregistered` (`bool?`: `null` none to end, `false` retried in the background); `User.sources[].origin` (`healthkit` | `direct`, closed; absent = `healthkit`; a direct source is named by the provider's display name, `bundle_id: null`); `Sample.type` `hrv_rmssd` (ms), also a new `samples_7d` key, accepted from `POST /v1/sync` too (an older server counts it `unknown_type`; Apple Health has no RMSSD, so the app sends none); `coverage.sleep_origin` (`healthkit` | `direct` | `null`) and `coverage.hrv_metric` (`sdnn` | `rmssd` | `null`; absent with an HRV = `sdnn`), both closed. **Flag:** `connect.garmin`, shipped enabled at 0 % (testers by the allow-list), gating `available`, start and confirm. **Codes** (Errors' `code` table, the status table's `403`, `404`, `409` rows and a new `410` row): `unknown_provider` (`404`), `connection_limit` (`409`), `connect_ticket_invalid` (`409`), `connect_expired` (`410`), and `merge_has_connections` (`409`, admin merge only); `consent_required` on start and confirm. **Behaviour:** none yet. With the platform PR `connect/foundation` the routes, `origin`, `hrv_rmssd`, `connections_deregistered`, the flag and the codes are served, and sync records whose `source` starts with the reserved `ryft-direct-` are counted `unknown_type`; production serves no provider until phase 2 (`available: []`, start `404 unknown_provider`). With the platform PR `connect/scoring` (a model version, a rescore) `coverage` is built for the first time with `sleep_origin` and `hrv_metric`; `hrv_ms`, `hrv_baseline_ms`, `baseline_hrv_days` and `hrv_readings` are in the day's metric, a baseline per metric (a change of metric is `short_history` for 7 nights), a direct night beats the same vendor's Apple Health night, and `GET /v1/trends?metric=hrv` serves only the newest day's metric. Conventions → Auth, Rate limits, Units and App Attest → Exempt name the new routes. **Old builds:** they ignore the new fields and never call the routes; a `hrv_ms` in RMSSD on a connected account reads as an HRV like any other (only allow-listed testers have one). **The app:** offer Connect from `available` only (an id this build knows; `404` without a `code`: hide every entry); data card, start, the sheet, confirm at once on `ok`; after a `409`/`410` on a repeated confirm read `GET /v1/me/connections`; show `needs_reauth` as Reconnect and `revoked` as removed at the provider; say on Disconnect that it deletes; ask for consent at once after a policy change (deliveries without it are lost); decode `origin`, `sleep_origin`, `hrv_metric`, `connections_deregistered` as optional and never compare HRV across metrics. **Swift:** `SourceOrigin`, `HRVMetric`, `ConnectionStatus`, `Connection`, `AvailableConnection`, `ConnectionsResponse`, `ConnectStart`, `ConnectConfirmPost`, `ConnectionDeleted`, `ConnectCallbackStatus`, `ConnectCallback` (parses the redirect); `UserSource.origin`, `ScoreCoverage.sleepOrigin` and `.hrvMetric`, `AccountDeleted.connectionsDeregistered`; `APIErrorCode` gains `unknownProvider`, `connectionLimit`, `connectTicketInvalid`, `connectExpired`, `mergeHasConnections`. The examples are `http` blocks until the server has the model types (`model.ConnectionsResponse`, `Connection`, `AvailableConnection`, `ConnectStart`, `ConnectConfirmPost`, `ConnectionDeleted`). |
| 2026-11-26 | **[Nutrition export favourites](#nutrition)** (D284). Additive: no v1 field renamed, removed or retyped. `GET /v1/me/nutrition-export` gains an optional `favorites` array (`NutritionFavorite`: `food_id`, `food_name`, `favorited_at`). It is present on the first page (no `after`), as `[]` when there are none, and omitted on later pages. `food_name` is `""` when the food is no longer readable; the ID stays. Entries are ordered by `favorited_at`, then `food_id`. Private foods and goals still go through the account rights process. [Nutrition food selection](#nutrition-food-selection) now describes the broad-query ranking order (staples, whole forms, exact matches, cultivars, other forms) and points its Favourites paragraph at the export. **Old builds** ignore `favorites` and keep working. **Swift:** `NutritionFavorite`; optional `favorites` on `NutritionMealExportPage`. |
| 2026-11-25 | **[Nutrition diary and portability](#nutrition)** (D278–D280). Adds a default-off diary flag, bilingual generic food search, source-backed portions and an authenticated owner-only meal export. Protected Nutrition score and coaching access continues to use `nutrition.enabled`. Under enforcement, HealthKit checks current consent before both write phases. |
| 2026-11-25 | **[Bilingual food suggestions](#nutrition)** (D283). Common German and English generic food suggestions support unambiguous prefixes of at least four normalized characters. |
| 2026-11-25 | **[Nutrition food selection](#nutrition-food-selection)** (D284; platform PR `nutrition: everyday staples, portions and favourites`, branch `feat/nutrition-everyday-catalog`, stacked on #247). Additive: no v1 field renamed, removed or retyped. **Search** (`POST /v1/nutrition/foods/search`): `foods[]` gains optional `subtitle`, `default_portion`, `last_used_portion`, `reference_portion` (each a new `FoodPortionSelection`) and `favorited`; staples rank above derivatives, then favourites and recent use. **Detail** (`GET /v1/nutrition/foods/{id}`): optional `?language=`; the same fields plus `last_used_selection`; `portions[]` ordered by `sort_order` and gaining `is_default`, `sort_order`, optional `portion_key`, `short_label_en`, `short_label_de`; at most one measured default; `sources[]` gain optional `attribution` (BLS, CC BY 4.0). **Meals** (`POST /meals`, `PATCH /meals/{id}`): items take optional `language`; the server computes portion weight and macros. Saved items (`POST`/`PATCH`/`GET /meals`, `GET /meals/{id}`, `GET /v1/me/nutrition-export`) gain optional `portion_label` and `portion_short_label`, which are snapshots; an `item_id`-only PATCH keeps the snapshot. **Favourites**: `PUT`/`DELETE /v1/nutrition/foods/{id}/favorite`, `204`, idempotent, account-bound; DELETE works with the flags off. **Old builds** ignore every new field and keep working. **Swift:** `FoodPortionSelection`; optional properties on `FoodSummary`, `FoodDetail`, `FoodPortion`, `FoodSource`, `NutritionMealItemInput` and `NutritionMealItem`. |
| 2026-11-25 | **Nutrition score consent** (D277). Daily and range summaries require current consent for freshly derived scores. Owner totals, retained meal records and deletions remain accessible under the Nutrition flag. |
| 2026-11-24 | **Training-to-Nutrition coaching** (D271). Signed-in goal profiles, planned sessions and feedback, daily briefing, additive HealthKit metadata and consent-scoped partner ingestion. Operator-only aggregate evaluation. |
| 2026-11-24 | **[Nutrition-to-Recovery](#nutrition-to-recovery)** (D268). Adds account opt-in, contextual scoring in daily summaries and current guidance. |
| 2026-11-24 | **Nutrition Score** (D266). Nutrition daily and range summaries report versioned quality and CGM subscores, ten components, per-meal impact and the optional timing penalty. Additive CGM upload/deletion and private food quality profiles. Examples in the [Nutrition contract](architecture/nutrition/04-api-contracts.md#nutrition-score). |
| 2026-11-24 | **[Nutrition API](#nutrition)** (D265). Gated catalog, search, barcode, private foods, submissions, meals with historical snapshots, daily summaries and goals. Additive endpoints and the `nutrition.enabled` flag. |
| 2026-11-20 | **Admin only: an unbound invite code's `note` expires; a deleted account leaves the flags' allow-lists** (D249; platform PR `store: deletion removes allow-list entries; invite notes expire`). `GET /v1/admin/invites`: the `note` of an unbound code is now `""` 30 days after the code could no longer be redeemed (revoked, expired or used up), as a bound code's already was; its `status` and `revoked_at` don't change. `GET /v1/admin/flags`: `allow_user_ids` no longer holds the id of a deleted account (`DELETE /v1/me` and the admin deletion remove it; migration 0038 removed those of earlier deletions). **Fields:** none added, renamed or removed. **The app:** nothing; it never calls `/v1/admin/*`, and `GET /v1/config` answers as before. **Swift:** none. |
| 2026-11-20 | **App Attest: the server (mode off)** (D247; platform PR `api: App Attest, the server (mode off)`; answers the app's `API_REQUESTS.md` → App Attest AA1–AA4). Additive; no v1 field renamed, removed or retyped. **Served:** `POST /v1/app-attest/challenge`, `POST /v1/app-attest/keys` and `POST /v1/app-attest/token` exist now, but answer only where the server has its token key; without it they answer `501`, which the app treats like the `404` without a `code` (not live, no header). Production ships in mode `off` without the token key, so it answers `501` until the `warn` PR sets it (after the privacy text names App Attest); local compose answers `501`. **Modes:** `off` doesn't read `X-RYFT-App-Token` and logs nothing; `warn` serves and logs a request without a valid token; `on` refuses it with `403 app_attest_required`; never `off` → `on`. **Codes:** `app_attest_required` (`403`), `app_attest_challenge_invalid` (`422`), `app_attest_invalid` (`422`), `app_attest_key_unknown` (`404`) join Errors' `code` table and the status table's `403`, `404` and `422` rows (they were a paragraph); the `invite_required` row's list of routes gains the three App Attest routes (outside the beta gate). **AA1:** Conventions → App build: the optional request header `X-RYFT-App-Build: <CFBundleVersion>`, `^[0-9]+(\.[0-9]+){0,3}$`, at most 24 characters; a missing or malformed value is ignored and never refused; written only into the App Attest warn line, never a metric label. **AA2:** `GET /v1/config` → `app_attest` (`"off"`, `"warn"` or `"on"`, closed, always present): the app may skip the challenge probe under `"off"`; a `501` or `404` from the challenge route stays authoritative; under `"on"` the config read itself needs a token. **AA3:** `ttl_sec` is 3600 today and never below 600; the app renews with under `min(300, ttl_sec / 2)` seconds left (it said 5 minutes). **AA4:** the three routes' `error` texts are fixed sentences and never echo `challenge`, `key_id`, `assertion`, `attestation` or a token. **Development attestations:** production only on every server (D247): `APP_ATTEST_ENVIRONMENTS` is gone from step 7, and a development attestation is `422 app_attest_invalid`. **Examples:** the three routes' bodies are type-checked `json` blocks now (`AppAttestChallenge`, `AppAttestKeyPost`, `AppAttestKey`, `AppAttestTokenPost`, `AppAttestToken`), the `http` exchanges keep the headers; a request with both headers is shown. **Behaviour:** none for the app in production until the token key is set; `GET /v1/config` gains a field. **Old builds:** they ignore `app_attest` and see `501` where they saw `404`, which D245 already told them to treat alike. **The app:** send `X-RYFT-App-Build` on every request; decode `app_attest` as optional (missing or unknown: probe as before); renew by `min(300, ttl_sec / 2)`. **Swift:** `RemoteConfig.appAttest: String`; `APIErrorCode` gains `appAttestRequired`, `appAttestChallengeInvalid`, `appAttestInvalid`, `appAttestKeyUnknown` (the comment after the enum is replaced); the `AppAttestToken.ttlSec` comment states the floor. |
| 2026-11-19 | **Docs only: no Kubernetes stack any more** (D246; platform PR `remove: Kubernetes, Argo CD, Vault, admission, AWS (D3)`). Conventions → Base URLs loses its last sentence: the Kubernetes manifests, whose Ingress served both services on one host, are deleted. Production stays `https://api.ryftlab.de` and `https://ingest.ryftlab.de`, local compose `:8082` and `:8081`. **Behaviour:** none; no route, field, status or `code` changes. **The app:** nothing. **Swift:** none. |
| 2026-11-19 | **App Attest: the contract, not served yet** (D245; platform PR `docs: the App Attest contract`; the user's decision of 2026-11-19, no `API_REQUESTS.md` entry). Additive; docs only, so the app can build it before the server has it. **New:** Conventions → [App Attest](#app-attest): `POST /v1/app-attest/challenge` (`200 {challenge, expires_at}`: opaque, single use, 5 minutes, for the account), `POST /v1/app-attest/keys` (`{key_id, attestation, challenge}` → `201 {key_id}`; Apple's attestation checks, production attestations only on the production servers), `POST /v1/app-attest/token` (`{key_id, challenge, assertion}` → `200 {token, expires_at, ttl_sec}`, one hour; the assertion's counter must rise); the request header `X-RYFT-App-Token` on every request to both hosts; the byte-exact rules (`clientDataHash` = SHA-256 of the challenge string's UTF-8 bytes for both steps; `key_id` and the CBOR blobs in standard base64). **Codes** (Errors → App Attest; not in the `code` table until the server sends them): `app_attest_required` (`403` on every route the check covers: get a new token, send again once), `app_attest_challenge_invalid` (`422`), `app_attest_invalid` (`422`), `app_attest_key_unknown` (`404`, `POST /v1/app-attest/token` only). **Exempt:** the health paths, `POST /internal/tick`, all of `/v1/admin/*`, the three App Attest routes, `DELETE /v1/me`, `POST /v1/me/apple-authorization`, `DELETE /v1/me/consent`, `DELETE /v1/devices/{id}/push`, `POST /v1/feedback`, and the accounts in `APP_ATTEST_EXEMPT_USERS` (the smoke user). **Behaviour:** none yet. Today `POST /v1/app-attest/challenge` is `404` and the header is ignored; the server's PR ships in mode `off` (served, header not read), then `warn` (served, logged), then `on` (`403 app_attest_required`), each a one-line PR; until the privacy text names App Attest the routes answer `501`. **Old builds:** nothing changes under `off` and `warn`; under `on` they get a `403` with a `code` they don't know (read as no `code`, never a sign-out) and drop their queued Train writes (`PUT /v1/sessions/{id}`, `POST /v1/sessions/{id}/complete`), so `on` waits until the `warn` lines show no request without a token from an app build for 7 days. **The app:** attest once per account and install after sign-in (only after `POST /v1/app-attest/challenge` answered `200`; `404` or `501`: no header, try at the next launch), get a token, send it on every request, renew it with 5 minutes left by `ttl_sec`, on `app_attest_required` get a new one once and keep the refused write queued (never drop it, never commit its HealthKit anchor), on `app_attest_key_unknown` attest again, one renewal per account and key at a time (refused requests wait for it), no header without App Attest; set the App Attest entitlement to `production`. **Swift:** `AppAttestChallenge`, `AppAttestKeyPost`, `AppAttestKey`, `AppAttestTokenPost`, `AppAttestToken`; the four `APIErrorCode` cases join the mirror with the server's PR (the app may add them now). The examples are `http` blocks until the server has the model types (D245). |
| 2026-11-18 | **The per-address limit is enforced by api and ingest on GCP** (D53, D240; platform PR `backend: the per-IP limit in api and ingest`). Documentation of behaviour that held on EKS and is true again on Cloud Run; the values and the answer are unchanged. Each client address (an IPv6 address by its /64) gets 20 requests a second, bursts of 50, counted per instance, before any token is checked, on every route of `https://api.ryftlab.de` and `https://ingest.ryftlab.de` except the health paths (`/health/live`, `/health/ready`). Over it: `429` with the plain-text body `Too Many Requests` (`text/plain; charset=utf-8`; no JSON and no `request_id` in the body, `X-Request-ID` and the response headers as on every answer, no CORS headers), `Retry-After` (the delay rounded up, at least 1: usually 1) and `X-Retry-In` (the delay as a Go duration, e.g. `37.5ms`; informative). The limit ran in Traefik at the edge and now runs in the services, so Conventions → Rate limits names it, and Errors → `429` and Admin routes → CORS no longer say "the edge"; a preflight counts like any request. **Fields:** none added, renamed or removed. **The app:** nothing; it already handles a non-JSON `429` with `Retry-After`. **Swift:** none. |
| 2026-11-16 | **Product analytics: `GET /v1/config` → `analytics` and the flag `analytics.enabled`** (D231; platform PR `config: analytics`). Additive. **Fields:** `RemoteConfig.analytics`, always present: `{"host": "https://eu.i.posthog.com", "project_key": "phc_…"}` (PostHog EU Cloud), or `null` while the server has no project key (until the operator stores it, and in local compose). **Flag:** `analytics.enabled`, shipped enabled at 0 %: on only for allow-listed testers, like `train.import`. The two are independent; nothing else changes. **The app:** decode `analytics` as optional; send events only while `flags["analytics.enabled"]` is `true`, `analytics` is not `null` and the user opted in (off by default); take the host and the key from the newest config, never from the build, and never log the key; when any of the three stops holding, stop sending and drop the queue. The event list, the pseudonymous identifier and the opt-in are the app's analytics plan (decisions 4–6). **Swift:** `AnalyticsConfig`, `RemoteConfig.analytics`. |
| 2026-11-16 | **Docs only: the privacy policy is published in English only** (web D18; platform PR `web: english only`). `GET /v1/config` → `privacy_policy_url` is unchanged (`https://ryftlab.de/privacy`); its note said "in German and English" and now says English. The site's German pages are gone; `/de`, `/de/privacy`, `/de/terms` and `/de/impressum`, which older app builds link, answer `301` to the English page. **Fields:** none added, renamed or removed; `privacy_policy_version` is unchanged (`2026-10-26`). **The app:** nothing required; a build that links `/de/…` lands on the English page. **Swift:** none. |
| 2026-11-15 | **Health paths for Cloud Run: `GET /health/live` and `GET /health/ready`** (D220; platform PR A2). Every service also answers `/health/live` (`200 {"status":"ok"}`, from the moment it listens) and `/health/ready` (`200 {"status":"ready"}`, or `503`), with the handlers of `/healthz` and `/readyz`, which stay for local compose. On Cloud Run the ready paths answer `503 {"error":"not ready"}` without `Retry-After` and without the cause, reused for 5 s; locally the body keeps its cause (api's and ingest's unchanged; scoring's and notify's with `status` and `checks`; Endpoints → Health). `/metrics` isn't served on Cloud Run. One new `503` cause for every route: during a cold start a request waits up to 5 s for the service to reach its database, then gets `503 {"error":"temporarily unavailable, send the request again"}` with `Retry-After: 5`, the shape and handling of every other `503`. HTTP/2 (h2c) end to end on Cloud Run keeps the 32 MiB sync unchanged. **Fields:** none added, renamed or removed in `/v1`. **The app:** nothing; its existing `503` retry covers the cold start. **Swift:** none. |
| 2026-11-14 | **Two hosts in production: the sync moves to `https://ingest.ryftlab.de`** (D217; platform PR A1, the move to Cloud Run). `POST /v1/sync` is served at **`https://ingest.ryftlab.de`**, every other route stays at **`https://api.ryftlab.de`** (Conventions → Base URLs); this replaces "for both services" in the D213 entry below. A Cloud Run domain mapping points one host at one service and cannot route `/v1/sync` to ingest by path. The api host answers `404` to `POST /v1/sync` (unknown route), the ingest host `404` to every other `/v1/*` route. Neither host is served until the GCP runbook has run (D217). Local compose is unchanged: ingest on `:8081`, api on `:8082`. The health paths for Cloud Run (`/health/live`, `/health/ready`) come in a later entry (PR A2). Nothing else in the contract changes: paths, bodies, statuses, `code`s, limits (32 MiB for a sync), timeouts and headers are as before. **Fields:** none added, renamed or removed. **The app:** a build configured with both URLs, `https://api.ryftlab.de` and `https://ingest.ryftlab.de`, that sends `POST /v1/sync` to the ingest URL and every other request to the api URL; the ingest URL is no longer optional in production (a build that falls back to the api URL for the sync gets `404` on every sync). **Swift:** none (no type changes; the URLs are configuration). |
| 2026-11-14 | **The domain is `ryftlab.de`, a hard cut** (D213; platform PR #161, the rebuild). The base URL is now **`https://api.ryftlab.de`** for both services (ingest's `POST /v1/sync` and everything else); `api.ryftapp.de` is not served any more: no alias, no redirect, no AASA on the old domain. Every URL the server emits moves with it: `privacy_policy_url` in `GET /v1/config` is `https://ryftlab.de/privacy`; the push payloads' `ryft.url` is `https://ryftlab.de/day/<day>` or `https://ryftlab.de/train/session/<id>`; an invite's `link` is `https://ryftlab.de/invite/<code>`; the operator's page and the CORS origin are `https://ryftlab.de` (Admin routes → CORS). The examples are updated. **Fields:** none added, renamed or removed; `privacy_policy_version` is unchanged (`2026-10-26`). **The app:** a build whose base URL is `https://api.ryftlab.de` and that accepts only `ryftlab.de` hosts in `ryft.url`, invite links and help links; its associated domains name `ryftlab.de`. Until the site is deployed again (D215), `https://ryftlab.de/...` has no page and no AASA, so these URLs are no universal links and `privacy_policy_url` does not load in a browser; the payload shapes are unchanged. **Swift:** none (no type changes; the base URL is configuration). |
| 2026-11-05 | **Docs only: the operator's page as built** (timkrebs/ryft-platform#128 merged, pinned in #138, confirmed working by the owner on 2026-11-05; no `API_REQUESTS.md` entry, the app is not involved). Admin routes → Routes and who may call them: the "Used by the page" column now matches the page: `GET /v1/admin/audit` is used (an Audit tab, read-only, the default 7 days; it said "not in v1"), flags are read-only and the page shows only the count of allow-listed accounts (the token's answer still carries `allow_user_ids`; flags have no projection), `?emails=1` is sent only while the page's email switch is on, feedback asks 30 days back. Screens → endpoints: the operator page's row lists the audit. `POST /v1/admin/invites`: the page's expiry default (14 days) and its "Never" (no `expires_at`, only without `email`) are said precisely. **Behaviour:** none; no route, field, status or `code` changes. **Swift:** none. |
| 2026-11-05 | **Invites for a person** (the operator's page; the app is not involved): `POST /v1/admin/invites` takes an optional `email` (a bound code has `max_uses` 1 and always expires: 14 days by default, at most 90; `422` otherwise, and `422` while bound invites are unavailable because the Supabase project does not confirm emails or cannot be asked); `BetaInvite` gains `email` (masked unless `?emails=1`), `created_by` (`Actor`), `revoked_at`, `status` (`open` \| `used` \| `expired` \| `revoked`, closed), `link` and `uses_by`; `GET /v1/admin/invites` is still an array; `DELETE /v1/admin/invites/{code}` now **revokes** (`204`, idempotent; the row stays listed) instead of deleting; `POST /v1/me/invite` refuses a revoked code, a bound code whose email is not the account's confirmed one and a code this account has already redeemed (an account whose access the operator removed cannot redeem the same multi-use code again), with the same single `422 "invalid or used code"`, and answers `503` with `Retry-After: 5` for a bound code when the account's email cannot be checked. **Behaviour:** nothing changes for the app, except that `DELETE /v1/admin/invites/{code}` no longer removes the row; codes created before this entry are unbound and list as created by `{"kind": "key", "id": null}`. Old clients ignore every new field. **Swift:** none (`InvitePost` is unchanged and the admin types are not mirrored). |
| 2026-11-05 | **Admin sign-in** (the api side of the operator's page at `https://ryftapp.de/admin`; the app is not involved, so no `API_REQUESTS.md` entry answers to this): a new section [Admin routes (operator only)](#admin-routes-operator-only) documents `/v1/admin/*`, which API.md did not cover before. **Auth:** `/v1/admin/*` accepts the admin key (unchanged) **or** a Supabase access token with `app_metadata.role` `admin`, an allow-list row and a TOTP (`aal2`, `amr` `totp`) at most 12 h old; every other bearer gets one identical `401`; a valid, allow-listed admin without a fresh second factor gets `403` with the new `code` **`mfa_required`** (Errors → `code` table and the `403` row); merge, flag writes, user detail and resync-hint stay key-only (`403` for a token, no `code`); a token whose signing keys or database check is unavailable gets `503` with `Retry-After: 5`; `/v1/admin/*` has a per-address limit (10/s, bursts of 20, `429` with `Retry-After`). **CORS:** only `/v1/admin/*`, only for the origins in `ADMIN_WEB_ORIGINS` (empty = none), `GET, POST, DELETE`, `Authorization`, `Content-Type`, preflight answered without auth, `Max-Age` 600, no credentials, `X-Request-ID` and `Retry-After` exposed. **Audit:** `AuditRecord.actor` (`{kind: key | user, id}`, `Actor`, closed `kind`); `key_fingerprint` stays and is `""` for a user actor. **Token answers:** the users list and the feedback are projections for an operator's token (`AdminUserBrief`, `FeedbackBrief`: no health-derived, device or diagnostic fields); the key's answers are unchanged. **Behaviour:** nothing changes for the app or for key-based scripts, except that a key caller can now get `429` above 10 requests a second per address, the `401` message is `admin key or admin sign-in required`, and the scheme `bearer` in lower case is accepted. Old clients ignore every new field. **Swift:** the closed `code` enum gains `mfa_required` in so many words, but only on `/v1/admin/*`, routes the app never calls, so no app request can receive it; the mirror gains `APIErrorCode.mfaRequired` for completeness; the app needs no change, it never calls `/v1/admin/*` (`APIErrorBody` is unchanged, and the admin types are not mirrored). |
| 2026-11-04 | **Errors: what the client does** (documentation only, behaviour unchanged): Errors → the status table gains a `code` column (the `code`s a status can carry) and a **Do** column with one line per status (`400`, `401`, `403`, `404`, `409`, `413`, `422`, `429`, `500`, `503`), consistent with the app's retry rules (`408`, `429` and `5xx` but `501` are transient; a `4xx` is not retried unchanged). The `When` cells state conditions only; the instructions moved to `Do`. `409` on `PUT /v1/sessions/{id}`: merge the returned `Session` with the local copy by set id (the later `updated_at` wins) and `PUT` the result. `503` When names its causes; a database failure mid-request is a `500`. `429` notes that the edge sends `Retry-After` too and names the feedback and invite limits. No status, `code` or field changed; the `code` enum is still the four values. The sample consent error body shows the real message (`consent to the privacy policy … is required before uploading health data`; it read `consent required`), and the `GET /v1/config` example shows the policy version in force (`2026-10-26`); the messages Errors quotes verbatim are fixed text, every other message stays free text: branch on the status and `code`. Tests check the `code` table, the status table and the quoted messages against the code (the Do wording only for being present). Swift: none (`APIErrorCode`, `APIErrorBody` unchanged). |
| 2026-10-31 | **Coverage and provisional scores** (answers the app's data-quality D2 and D4, `API_REQUESTS.md` → Sync and performance): `DailyScore.coverage` (`ScoreCoverage?`: `sleep_hr_minutes`, `hrv_readings`, `sleep_source`, `hr_source_hours`, `night_complete`, `status` `provisional` \| `final`, closed); `User.sources[].samples_7d` (sample counts per `Sample.type` over the last 7 days, all eight keys on every source). `details.hr_minutes` stays. **Behaviour:** a night is complete on wake evidence or at 10:00 local; a day is `provisional` until then and `final` after, sticky; scoring gains two floors (sleep heart rate < 120 min drops the resting-HR input of Recovery, which is renormalised, `rhr` itself stays; < 60 min of sleep from the winning source makes `sleep_performance` `null`), so stored numbers change for thin nights and the model version is bumped (`GET /v1/model`; rescore history to fill `coverage` on old rows); `MORNING_SCORE` now waits for the first `final` scoring of the day with recovery and sleep, the silent refresh still follows every scoring. Old clients ignore the new fields. Swift: `ScoreStatus`, `ScoreCoverage`, `DailyScore.coverage`, `UserSource.samples7D` (wire key `samples_7d`; the app's `.convertFromSnakeCase` maps it to `samples7D`, capital D). |
| 2026-10-31 | **Workout duplicates** (extended the same day with `include_hidden`): `GET /v1/workouts?include_hidden=true` (optional, `true`/`false`, else `422`) also returns the hidden cross-source duplicates, each with `Workout.duplicate_of` (uuid?, the primary's id); the app's export passes it so the file is complete; without it, and on `GET /v1/days/{day}`, nothing changes. `Workout.also_recorded_by` (`[string]?`, omitted when none). **Behaviour:** workouts of one source with the same activity and start and end within 120 s of each other are folded to the most recently uploaded id (the older ids are removed, `404` afterwards, a Train session stays and is re-attached when the day is rescored); workouts of different sources with the same activity (or `other`) that overlap by at least 80 % of the shorter are listed once, as the primary with the other sources in `also_recorded_by`, in `GET /v1/days/{day}` and `GET /v1/workouts` (`GET /v1/workouts/{id}` still answers for the hidden one). Distances, counts and `muscular_load` attachment on affected days change when they are rescored. Swift: `Workout.alsoRecordedBy`, `Workout.duplicateOf`. |
| 2026-10-31 | **Deletions and HealthKit ids** (answers the app's request, `API_REQUESTS.md` → Sync and performance §2): `Sample.id` and `SleepSegment.id` (uuid?, the HealthKit object UUID); `SyncRequest.deleted` (`[{kind: sample \| sleep \| workout, id}]`, ≤ 2 000, `422` above); `SyncResponse.deleted` (`samples`, `sleep`, `workouts`); `ignored.<kind>.unknown_id` (an id the server cannot delete for this user; a re-sent deletion). **Behaviour:** deletions apply after the same request's records; a record with an `id` adopts a stored row with the same natural key (and a sample takes the new value), so a source re-writing a value under a new UUID does not double it; a record whose `id` is already stored is `duplicate`; deleted days are queued for rescoring (in `days_touched`); `ryft-session` workouts are not deletable (`unknown_id`); deleting a workout that has a Train session keeps the session; a failed deletion is a `503` the app re-sends; more than 5 000 entries in `workouts` is a `422` (nothing stored; the limit lives in this entry's request changes). Request bodies reject unknown fields (`400`), so a platform from before this entry would refuse `deleted` and `id`: ship the app change only after this entry is deployed (there is no flag). Swift: `DeletionKind`, `DeletedRecord`, `SyncDeleted`, `SyncResponse.deleted`, `IgnoredCounts.unknownId`, `Sample.id` / `SleepSegment.id` / `SyncRequest.deleted` on the app's own request types. |
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
