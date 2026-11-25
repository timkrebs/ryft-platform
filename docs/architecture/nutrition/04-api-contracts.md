# Nutrition HTTP API

Base path is `/v1/nutrition` on the API host. Routes use the existing bearer, App Attest, beta admission, address/account limits, request IDs and 15-second budget. `nutrition.diary_enabled` enables owner-private diary, catalog and goal routes. Existing `nutrition.enabled` permits those routes plus submissions, glucose writes, N2R and adaptive coaching. The daily and range responses retain nutrient totals and return an empty score breakdown for the diary-only cohort. Writes require current health-data consent when enforcement is on or warn. Authenticated owner erasure of meals, glucose and N2R is available independently of Nutrition feature flags and consent. JSON accepts one document up to 64 KiB and rejects unknown fields, including client-supplied account IDs. Every personal lookup derives account scope from the bearer.

| Verb and relative path | Request | Success |
| --- | --- | --- |
| POST `/foods/search` | `FoodSearch` with `query`, optional `language`, `market`, `limit`, `offset` | 200 `FoodSearchPage` |
| POST `/foods/barcode` | `FoodBarcodeQuery` with string `barcode` and optional `market` | 200 `FoodBarcodeResult` |
| GET `/foods/{id}` | Food UUID | 200 `FoodDetail` |
| GET `/foods/{id}/portions` | Food UUID | 200 `FoodPortion[]` |
| POST `/foods/custom` | `FoodCustomInput` | 200 `FoodDetail` |
| POST `/foods/submissions` | `{"food_id":"…","status":"draft"}` or status `submitted` | 200 `FoodSubmission` |
| POST `/meals` | `NutritionMealInput` | 201 `NutritionMeal`, or 200 replay |
| GET `/meals?from=…&to=…` | Inclusive local dates, optional `limit` and `offset` | 200 `NutritionMealPage` |
| GET `/meals/{id}` | Meal UUID | 200 `NutritionMeal` |
| PATCH `/meals/{id}` | `NutritionMealPatch` | 200 `NutritionMeal` |
| DELETE `/meals/{id}` | Meal UUID | 204, including repeats |
| GET `/daily?date=…` | Local date, optional `chrononutrition=true` | 200 `NutritionDaily` |
| GET `/summary?from=…&to=…` | Inclusive local dates, optional `chrononutrition=true` | 200 `NutritionDaily[]`, oldest first |
| GET, PUT, DELETE `/n2r/profile` | Account opt-in and settings | 200 `N2RProfile` or null, 204 for DELETE |
| GET `/guidance/current` | Guidance from the caller's current local-day Nutrition and recovery context | 200 `N2RGuidance` |
| PUT `/glucose` | One to 500 CGM samples with unique timestamps | 204 |
| DELETE `/glucose` | — | 204 after erasing the caller's CGM history |
| GET `/goals` | — | 200 `NutritionGoals` |
| PUT `/goals` | `NutritionGoalInput` | 200 `NutritionGoals` |

Go wire definitions live in [internal/model/nutrition.go](../../../backend/internal/model/nutrition.go). UUIDs are lowercase in responses. Dates are `YYYY-MM-DD`. Timestamps are RFC 3339. Error shapes follow the [v1 contract](../../API.md#errors). A private or inactive food and another account's meal return 404, like an unknown ID. An unavailable feature also returns 404. Malformed JSON returns 400, 64 KiB violations 413, and invalid fields or incompatible serving conversions 422. A reused create key with different data, a deleted meal's create key and stale revisions return 409. Retry a stale revision after fetching the saved version and resolving the edit.

## Catalog examples

```http
POST /v1/nutrition/foods/search
Authorization: Bearer <access token>
Content-Type: application/json

{"query":"Hähnchenbrust","language":"de","market":"DE","limit":20}
```

```json
{"foods":[{"id":"a7e40c6e-bfb4-47eb-ad64-aea61136db03","name":"Hähnchenbrust","brand":"","type":"generic","quality":"source_reported"}],"next_offset":null}
```

Search bodies carry food names and barcode digits to keep them out of access URLs. Search defaults to 20, supports 1–50, and caps offsets at 1,000. `language` prefers matching translations and supports German/English stemming for regional codes such as `de-DE`. `market` is a two-letter country preference. Exact labels precede prefixes, brand prefixes and full-text matches. Common German and English generic food terms can match the alternate term before typo matches. Results retain the catalog ID and the matched name supplied by the catalog. Match quality precedes market and language preferences. Text is accent- and punctuation-folded. An empty results array is a successful search.

```http
POST /v1/nutrition/foods/barcode
Content-Type: application/json

{"barcode":"012345678905","market":"DE"}
```

```json
{"gtin":"00012345678905","matches":[],"conflicted":false,"truncated":false}
```

A valid, unknown barcode has an empty `matches` array. `conflicted` signals two or more catalog IDs. Show candidates for review, and honor the 20-match cap indicated by `truncated`. Food detail returns a canonical name, optional verified density, names, source IDs, local labels for portions and nutrient references. An absent nutrient code is unknown. Portions are reported as measured weight, volume or servings with explicit IDs. The public catalog imports USDA household measures with source gram weights and Open Food Facts servings with a declared mass or volume compatible with the item's reference basis. Use grams or milliliters where no measured portion is available. Mass-to-volume conversion requires a verified density.

The development seed can return the following food detail. This example shows one name and one nutrient. `GET /foods/{id}/portions` returns the `portions` array on its own.

```json
{
  "id":"a7e40c6e-bfb4-47eb-ad64-aea61136db01","name":"Apple","brand":"","type":"generic","quality":"source_reported",
  "names":[{"language":"de","label":"Apfel","alias":false}],
  "sources":[{"id":"a7e40c6e-bfb4-47eb-ad64-aea61136db01","provider":"ryft","external_id":"a7e40c6e-bfb4-47eb-ad64-aea61136db01","dataset_version":"seed-1","source_url":"","license_identifier":"RYFT"}],
  "portions":[{"label":"Medium apple","labels":{"de":"Mittelgroßer Apfel"},"quantity":1,"unit":"portion","weight_g":150,"estimated":false,"id":"a7e40c6e-bfb4-47eb-ad64-aea61136db04","source_id":"a7e40c6e-bfb4-47eb-ad64-aea61136db01"}],
  "nutrients":[{"code":"fat","amount":0,"unit":"g","reference_amount":100,"reference_unit":"g","source_id":"a7e40c6e-bfb4-47eb-ad64-aea61136db01","method":"synthetic","quality":"source_reported"}]
}
```


```json
{"client_request_id":"71482956-284a-4ad2-a254-65f423576b02","name":"Homemade yogurt","names":[{"language":"de","label":"Selbstgemachter Joghurt","alias":false}],"nutrients":[{"code":"protein","amount":5,"unit":"g","reference_amount":100,"reference_unit":"g"}],"portions":[{"label":"Small bowl","unit":"portion","quantity":1,"weight_g":120,"estimated":true}]}
```

Custom food creation accepts up to 20 names, 25 portions and 120 nutrient references. A reference needs an explicit amount including an explicitly known zero. Accept grams, milligrams and micrograms when convertible to the registered unit. Serving references require one saved serving portion for that source. A matching client request ID and body return the same food. A submission is a separate owner-private row. Its draft can become submitted. Moderation and publishing operate independently of private logging.

## Meals and goals

```http
POST /v1/nutrition/meals
Content-Type: application/json

{"client_request_id":"ea0ac98d-9c71-410d-ac6a-61ce6f97f092","consumed_at":"2026-10-24T22:30:00Z","timezone":"Europe/Berlin","meal_type":"snack","input_method":"manual_search","items":[{"food_id":"a7e40c6e-bfb4-47eb-ad64-aea61136db01","quantity":150,"unit":"g"}]}
```

The response stores `local_date: "2026-10-25"`, revision 1, UTC consumption and creation timestamps, and an item with the food snapshot, resolved weight and nutrient provenance. Meal types are `breakfast`, `lunch`, `dinner`, `snack` and `other`. Input methods are `manual_search`, `barcode`, `photo_capture`, `photo_import`, `natural_language`, `nutrition_label`, `external_import` and `manual_entry`. They are provenance values for already reviewed food selections. Meals contain 1–50 items. Supply `g` or `ml` directly, or `portion` or `serving` with the corresponding `portion_id`. The backend recomputes values for new items. A matching POST replay returns the current saved meal, including any later edits. Deleted meals retain only a request ID marker until account deletion.

```json
{"revision":1,"notes":"Prepped at home","items":[{"item_id":"63cf1226-48cf-43c7-957e-c994ec3bbe61"}]}
```

In PATCH, omit `items` to retain all snapshots. Include only `item_id` for each retained snapshot in a reordered or partially changed list. A fresh quantity or explicit recalculation uses a new food/quantity/unit tuple. Include a new `timezone` or `consumed_at` to move the local day. Use an IANA timezone and include the time offset in the timestamp. Lists default to 20 meals, cap the page at 30 and offset at 1,000, and bound date ranges to 31 days. Sort is newest day and time first with a UUID tie-break.

```json
{"local_date":"2026-10-25","meal_count":1,"item_count":2,"nutrients":{"energy_kcal":{"known_subtotal":78,"unit":"kcal","known_items":1,"complete_for_logged_items":false}}}
```

A day with zero logs has zero logged meals/items and an empty nutrient map. Present totals describe the known subtotal. Completeness refers to logged items for each nutrient and says nothing about unlogged consumption.

```http
PUT /v1/nutrition/goals
Content-Type: application/json

{"revision":0,"targets":{"energy_kcal":2200,"protein":100,"fiber":25,"water":2000}}
```

`targets` replaces the previous revision using canonical units. A matching retry returns the saved revision. Revision 0 with empty targets denotes no goals saved. Water uses grams. Entries must be positive and user-configured. The server supplies no prescribed targets.

Future recognition contracts are in [Recognition](08-ai-recognition-design.md).

## Nutrition Score

`/daily` and `/summary` add `score_version`, nullable `nutrition_score`, `quality_score`, and `glucose_score`, the ten nullable 0–10 point values in `breakdown`, `meal_glucose_scores`, and `chrononutrition_penalty` in points. All three top-level scores use the 0–100 range. An empty meal window list is `[]`. See the [calibration and rules](09-nutrition-intelligence.md#score-architecture).

With a missing, outdated or withdrawn grant, the endpoints return the same version, counts and nutrient totals. The three scores and ten components are `null`, `meal_glucose_scores` is `[]`, `chrononutrition_penalty` is zero and `n2r` is omitted. Current consent resumes scoring from retained data. Owner access and erasure follow the usual feature gate.

```json
{
  "local_date": "2026-11-24",
  "meal_count": 1,
  "item_count": 1,
  "nutrients": {"energy_kcal": {"known_subtotal": 900, "unit": "kcal", "known_items": 1, "complete_for_logged_items": true}},
  "score_version": "1.0.0",
  "nutrition_score": 100,
  "quality_score": 100,
  "glucose_score": 100,
  "breakdown": {
    "non_starchy_veg_g": 10, "whole_fruit_g": 10, "fiber_g": 10, "nuts_legumes_g": 10,
    "omega3_epa_dha_mg": 10, "unsaturated_fat_kcal_pct": 10, "added_sugar_g": 10,
    "red_processed_meat_g": 10, "sodium_mg": 10, "trans_fat_kcal_pct": 10
  },
  "meal_glucose_scores": [
    {
      "meal_id": "ea0ac98d-9c71-410d-ac6a-61ce6f97f092",
      "window_end": "2026-11-24T23:30:00Z",
      "sample_count": 3,
      "baseline_mg_dl": 100, "peak_delta_mg_dl": 0, "iauc_mg_dl_min": 0, "end_delta_mg_dl": 0,
      "peak_score": 100, "auc_score": 100, "recovery_score": 100, "glucose_score": 100
    }
  ],
  "chrononutrition_penalty": 0
}
```

### Classifying a private food

`POST /foods/custom` accepts an optional `quality_profile`. The same field is returned in food details and captured on new meal items. Fractions use edible mass, lie between 0 and 1 and sum to at most 1. An explicit zero means absent. Omitted fields remain unknown. Juice and soft drinks set `sugary_drink: true` and `whole_fruit_fraction: 0`. The meat fraction is a single combined fraction for red or processed meat.

```json
{
  "client_request_id": "ad3045db-6b65-428f-a2f4-b231baa5d656",
  "name": "Mixed bowl",
  "quality_profile": {
    "non_starchy_vegetable_fraction": 0.45, "whole_fruit_fraction": 0,
    "nuts_legumes_fraction": 0.2, "red_processed_meat_fraction": 0, "sugary_drink": false
  }
}
```

### CGM upload

```http
PUT /v1/nutrition/glucose
Authorization: Bearer <access token>
Content-Type: application/json

{"samples":[
  {"timestamp":"2026-11-24T21:30:00Z","glucose_mg_dl":100},
  {"timestamp":"2026-11-24T22:00:00Z","glucose_mg_dl":100},
  {"timestamp":"2026-11-24T22:30:00Z","glucose_mg_dl":100}
]}
```

CGM data uses mg/dL and RFC 3339 instants. A repeated timestamp in a later request replaces that reading for the authenticated account. Duplicate instants within one batch or invalid values return 422. The upload follows the existing health consent gate. `DELETE /glucose` deletes all of the caller's readings and returns 204 on retries. The existing account deletion cascades to these rows.
