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
| `GET` | `/v1/sleep?range&to` | api | `SleepRange` |
| `GET` | `/v1/workouts?from&to` | api | `[Workout]` |
| `GET` | `/v1/workouts/{id}` | api | `WorkoutDetail` |
| `GET` | `/v1/trends?metric&range&to` | api | `Trend` |
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
| `hrv_baseline_ms` | number? | The 30-day HRV baseline `hrv_ms` was scored against: geometric mean of up to 30 scored days before `day`. |
| `rhr_baseline` | number? | The 30-day resting-HR baseline `rhr` was scored against. |
| `calibrating` | bool | Fewer than 7 days of baseline history. |
| `details` | object | Free-form breakdown for debugging; **not part of the contract** — do not build UI on it. |
| `computed_at` | timestamp | When the row was last scored. |

### `GET /v1/days/{day}` — one day

```jsonc
{ "score": DailyScore, "sleep": SleepDetail?, "strain": DayStrain?, "workouts": [Workout] }
```

A day that has not been scored yet returns `200` with a `score` whose numbers are
all `null`, `state: "calibrating"`, `sleep: null`, `strain: null` and `workouts: []`. `400` on a
malformed day. `sleep` is `null` when no sleep was recorded for the night ending on
`day` (also for days scored before the platform upgrade until they are recomputed).

`SleepDetail` — last night as the Sleep screen's *Day* tab shows it:

```json
{
  "start": "2026-10-17T21:00:00Z", "end": "2026-10-18T05:12:00Z",
  "total_min": 468, "in_bed_min": 492, "efficiency": 95,
  "stages": [ {"stage":"deep","min":112,"pct":24}, {"stage":"light","min":261,"pct":56},
              {"stage":"rem","min":95,"pct":20},  {"stage":"awake","min":10,"pct":2} ],
  "quality": 82, "quality_label": "good", "has_stages": true,
  "segments": [ {"stage":"light","start":"2026-10-17T21:12:00Z","end":"2026-10-17T21:40:00Z"}, … ]
}
```

| Field | Type | Notes |
|---|---|---|
| `start`, `end` | timestamp | First and last sleep segment of the night (including `inbed`). |
| `total_min` | int | Minutes asleep (= `score.sleep_actual_min`). |
| `in_bed_min` | int | Recorded time in bed; without an `inbed` record, the span `start`→`end`; never less than asleep + awake. |
| `efficiency` | int | `round(100 · total_min / in_bed_min)`, 0–100. |
| `stages` | `[SleepStage]` | Always four entries in this order: `deep`, `light`, `rem`, `awake`. HealthKit `core` is reported as `light`. |
| `quality` | int | 0–100. Currently equal to `score.sleep_performance`. |
| `quality_label` | string | `good` (quality ≥ 80), `fair` (60–79), `poor` (< 60). |
| `has_stages` | bool | `false` when the source recorded no stages (older firmware): `deep`/`light`/`rem` are then 0 min and 0 %. |
| `segments` | `[SleepInterval]` | The hypnogram, oldest first: `{stage, start, end}` with `stage` one of `deep`, `light`, `rem`, `awake`, `asleep` (stage unknown). Touching segments of one stage are merged; `inbed` is not a stage and is left out. Only on this endpoint. |

`DayStrain` — the heart-rate load behind `score.strain` (Strain screen); `null` when
the day had no heart-rate data:

```json
{ "trimp": 99, "hr_min": 912, "zone_min": [12, 24, 37, 7, 0] }
```

| Field | Type | Notes |
|---|---|---|
| `trimp` | int | Banister TRIMP for the whole day. |
| `hr_min` | int | Minutes of usable heart-rate data (watch worn). |
| `zone_min` | `[int]` | Five entries: minutes at 50–60, 60–70, 70–80, 80–90 and ≥ 90 % of heart-rate reserve. |

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
              "efficiency": 93, "stages": [ … ], "quality": 93, "quality_label": "good", "has_stages": true }, … ],
  "avg_total_min": 455, "avg_in_bed_min": 486, "avg_efficiency": 94, "avg_quality": 90,
  "avg_stages": [ {"stage":"deep","min":85,"pct":19}, … ]
}
```

| Field | Type | Notes |
|---|---|---|
| `range`, `from`, `to` | string | The window actually served, inclusive. |
| `days` | `[SleepDay]` | Only nights with data, oldest first. A `SleepDay` is `day` plus every `SleepDetail` field except `segments`. Missing nights are absent — fill the gaps from `from`/`to`. |
| `nights` | int | `len(days)`. |
| `avg_total_min`, `avg_in_bed_min`, `avg_quality` | int? | Means over `days`; `null` when there are none. |
| `avg_efficiency` | int? | `avg_total_min / avg_in_bed_min`. |
| `avg_stages` | `[SleepStage]` | Mean minutes per stage over nights with `has_stages`, same order and `pct` rules as `stages`; `[]` when no night had stages. |

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
|---|---|---|
| `avg_hr`, `max_hr` | number? | As sent by the app; when the app sent none, derived from the heart-rate samples in the workout window. Still omitted when there are no samples either. |
| `hr` | `[HRSample]` | The heart-rate curve between `start` and `end` (inclusive), oldest first, at most 600 points. Longer series are down-sampled with Largest-Triangle-Three-Buckets, so peaks survive and every point is a real sample. `[]` when there are no samples. |
| `HRSample.t` | timestamp | |
| `HRSample.bpm` | int | Rounded bpm. |
| `zone_min` | `[int]?` | Five entries, minutes in the heart-rate-reserve zones (as `DayStrain`). `null` until scored. |
| `trimp` | int? | Banister TRIMP of the workout. `null` until scored. |
| `strain` | number? | 0–21. Omitted until scored. |

A workout logged without heart-rate samples is scored from `avg_hr` and duration:
the whole duration counts at that intensity and in that zone.

### `GET /v1/trends?metric=…&range=7d|30d|90d&to=YYYY-MM-DD` — Trends charts

| Parameter | Values | Default |
|---|---|---|
| `metric` | `readiness`, `recovery`, `strain`, `sleep` (sleep performance %), `sleep_min` (minutes asleep), `hrv` (ms), `rhr` (bpm) | required |
| `range` | `7d`, `30d`, `90d` — days ending at `to`, inclusive | `7d` |
| `to` | `YYYY-MM-DD` | today |

`422` on a missing/unknown `metric` or `range`, `400` on a malformed `to`.

```json
{ "metric": "hrv", "range": "7d", "from": "2026-10-12", "to": "2026-10-18",
  "points": [ {"day":"2026-10-12","value":47.4}, {"day":"2026-10-13","value":50.8}, … ],
  "avg": 51.7, "min": 47.1, "max": 57.6, "baseline": 52.3 }
```

| Field | Type | Notes |
|---|---|---|
| `metric`, `range`, `from`, `to` | string | The window actually served. |
| `points` | `[TrendPoint]` | `{day, value}` for each scored day with a value, oldest first. Days without a value are absent (a gap in the chart), so there can be fewer points than days. Same numbers as the matching `DailyScore` field. |
| `avg` | number? | Mean of `points`, one decimal. `null` without points. |
| `min`, `max` | number? | `null` without points. |
| `baseline` | number? | `hrv` and `rhr` only: the 30-day baseline stored on the most recent day in the window that has one (= that day's `hrv_baseline_ms` / `rhr_baseline`). `null` for other metrics. |

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
| 2026-10-18 | `GET /v1/trends` and `DailyScore.hrv_baseline_ms` / `rhr_baseline` added. |
| 2026-10-18 | `GET /v1/workouts/{id}` (heart-rate curve, zone minutes, TRIMP) and `DayDetail.strain` added. |
| 2026-10-18 | `DayDetail.sleep` (typed night with stages and hypnogram) and `GET /v1/sleep` added. |
| 2026-10-18 | `DailyScore.readiness` and `DailyScore.state` added. |
| 2026-10-18 | Initial v1: sync, me, days, day detail, workouts, recompute. |
