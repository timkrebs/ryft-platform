# RYFT HTTP API — v1

This file is the contract between the platform and the RYFT iOS app. The app's
wire types (`../app/Ryft/Networking/Models.swift`) mirror the shapes below and
`backend/internal/model/model.go` 1:1.

**Compatibility rule.** v1 only changes additively: new endpoints, new optional
query parameters, new response fields. A field is never renamed, removed or
retyped, and an enum never loses a value, without moving to `/v2`. Clients must
ignore response fields they do not know.

## Conventions

| Topic | Rule |
|---|---|
| Base URLs | Two services. **ingest** serves `POST /v1/sync`; **api** serves everything else. Behind the Ingress both share one host (`/v1/sync` → ingest, other `/v1/*` → api). |
| Auth | Every `/v1/*` request carries `Authorization: Bearer <API_KEY>`. Missing or wrong key → `401` with `WWW-Authenticate: Bearer realm="ryft"`. `/healthz`, `/readyz`, `/metrics` are unauthenticated. |
| Content type | Requests and responses are `application/json`, UTF-8. Request bodies reject unknown fields (`400`). Max request body 32 MiB. |
| Keys | `snake_case`. The app decodes with `.convertFromSnakeCase`. |
| Timestamps | RFC 3339 strings, e.g. `"2026-10-18T05:12:00Z"`; fractional seconds may appear. |
| Days | `YYYY-MM-DD`, a calendar day in the user's timezone (`users.tz`, see `GET /v1/me`). The night that *ends* on day D belongs to D. |
| Nullability | A field typed `T?` below is `null` when unknown; a few `Workout` fields are *omitted* instead (noted per field), so decode every `T?` as optional. Fields not marked `?` are always present and non-null. |
| Units | Durations in whole minutes (`*_min`), heart rate in bpm, HRV in ms (SDNN), strain on the 0–21 scale, scores 0–100. |

### Errors

Every error response has the same shape:

```json
{ "error": "human-readable message" }
```

| Status | When |
|---|---|
| `400` | Malformed JSON, unknown body field, malformed query parameter or path value |
| `401` | Missing or wrong bearer token |
| `404` | Resource or route does not exist |
| `422` | Well-formed but invalid (out-of-range value, unknown enum, `from > to`) |
| `500` | Unexpected server error |
| `503` | A dependency is down (queue unavailable on `POST /v1/recompute`; `/readyz`) |

## Endpoints

| Method | Path | Service | Returns |
|---|---|---|---|
| `POST` | `/v1/sync` | ingest | `SyncResponse` |
| `GET` | `/v1/me` | api | `User` |
| `PATCH` | `/v1/me` | api | `User` |
| `GET` | `/v1/days?from&to` | api | `[DailyScore]` |
| `GET` | `/v1/days/{day}` | api | `DayDetail` |
| `GET` | `/v1/workouts?from&to` | api | `[Workout]` |
| `POST` | `/v1/recompute` | api | `202 {status:"queued"}` |
| `GET` | `/healthz`, `/readyz` | all | health |

### `POST /v1/sync` — upload HealthKit data

Request `SyncRequest`:

| Field | Type | Notes |
|---|---|---|
| `device_id` | uuid | Required. Stable per app install. |
| `device_name` | string? | Optional, shown nowhere yet. |
| `samples` | `[Sample]` | May be empty. |
| `sleep` | `[SleepSegment]` | May be empty. |
| `workouts` | `[Workout]` | May be empty. `strain` is ignored on input. |

`Sample`: `type` (enum below), `start`, `end` (timestamps, `end ≥ start`), `value`
(number), `unit` (string, informational), `source` (string, HealthKit source name).

| `type` | Unit of `value` |
|---|---|
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
segments on `(start, end, stage, source)`, workouts on `id`. Re-sending a page is harmless.

Response `200 SyncResponse`:

```json
{ "samples": 1440, "sleep": 14, "workouts": 1, "days_touched": ["2026-10-17", "2026-10-18"] }
```

`samples`/`sleep`/`workouts` count rows written (sleep counts only new segments).
`days_touched` are the local days queued for rescoring. Errors: `400` bad JSON,
`422` validation (`device_id` missing, unknown `type`/`stage`, `end` before `start`,
workout without `id`).

### `GET /v1/me` — user settings

```json
{ "id": "00000000-0000-0000-0000-000000000001", "name": "owner", "tz": "Europe/Berlin",
  "max_hr": 190, "sleep_base_min": 460 }
```

### `PATCH /v1/me` — change settings

Body `UserPatch`, every field optional: `tz` (IANA zone name), `max_hr` (int, 100–230),
`sleep_base_min` (int, 240–720). Returns the updated `User`. `422` on an unknown zone or
out-of-range value. Changing a setting does **not** rescore history; follow with
`POST /v1/recompute`.

### `GET /v1/days?from=YYYY-MM-DD&to=YYYY-MM-DD` — daily scores

Both parameters optional; default is the 30 days ending today (local). Returns the
scored days in `[from, to]`, oldest first; days that were never scored are absent.
`400` on a malformed date.

`DailyScore`:

| Field | Type | Notes |
|---|---|---|
| `day` | string | `YYYY-MM-DD` |
| `readiness` | int? | 1–100. Today's headline: `round(0.6·recovery + 0.4·sleep_performance)` minus up to 10 points when yesterday's strain was > 14 on a recovery < 50. `null` when there is no recovery. |
| `state` | string | `balanced` (readiness ≥ 67), `strained` (34–66), `depleted` (< 34), `calibrating` (fewer than 7 nights of baseline, or no readiness). |
| `recovery` | int? | 1–100. ≥ 67 green, 34–66 yellow, < 34 red. |
| `strain` | number? | 0–21, one decimal. `null` when no heart-rate data that day. |
| `sleep_performance` | int? | 0–100, actual / need. |
| `sleep_need_min` | int? | Minutes. |
| `sleep_actual_min` | int? | Minutes asleep last night. |
| `sleep_debt_min` | int? | Decayed shortfall of the 7 nights before last night. |
| `hrv_ms` | number? | Overnight mean SDNN, ms. |
| `rhr` | number? | Resting HR, bpm. |
| `resp_rate` | number? | Overnight breaths/min. |
| `spo2` | number? | Overnight mean, percent. |
| `calibrating` | bool | Fewer than 7 days of baseline history. |
| `details` | object | Free-form breakdown for debugging; **not part of the contract** — do not build UI on it. |
| `computed_at` | timestamp | When the row was last scored. |

### `GET /v1/days/{day}` — one day

```json
{ "score": DailyScore, "workouts": [Workout] }
```

A day that has not been scored yet returns `200` with a `score` whose numbers are
all `null`, `state: "calibrating"` and `workouts: []`. `400` on a malformed day.

### `GET /v1/workouts?from&to` — workouts

Same `from`/`to` semantics as `/v1/days`. Workouts whose start falls in the range,
oldest first.

`Workout`:

| Field | Type | Notes |
|---|---|---|
| `id` | uuid | The HealthKit workout UUID. |
| `activity` | string | e.g. `running`; `other` when unknown. |
| `start`, `end` | timestamp | |
| `avg_hr`, `max_hr` | number? | bpm, as sent by the app. Omitted when absent. |
| `energy_kcal` | number? | Omitted when absent. |
| `distance_m` | number? | Omitted when absent. |
| `source` | string | |
| `strain` | number? | 0–21, filled by scoring. Omitted until scored. |

### `POST /v1/recompute` — rescore a range

Body `{ "from": "YYYY-MM-DD", "to": "YYYY-MM-DD" }` (inclusive, `from ≤ to`, at most
400 days). Responds `202 {"status":"queued","from":"…","to":"…"}`; scoring rebuilds
the days oldest first within seconds. `422` on bad dates, `503` if the queue is down.

### Health

`GET /healthz` → `200 {"status":"ok"}` while the process runs.
`GET /readyz` → `200 {"status":"ready"}`, or `503 {"error":"…"}` when Postgres (and,
for ingest/scoring, NATS) is unreachable.

## Changelog

| Date | Change |
|---|---|
| 2026-10-18 | `DailyScore.readiness` and `DailyScore.state` added. |
| 2026-10-18 | Initial v1: sync, me, days, day detail, workouts, recompute. |
