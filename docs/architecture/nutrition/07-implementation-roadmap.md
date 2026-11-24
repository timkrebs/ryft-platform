# Nutrition implementation and rollout

## Current status

| State | Work |
| --- | --- |
| Completed locally | Architecture discovery and design, migration 0043, 40 canonical nutrient codes, normalized catalog, transactional account scoping, two provider adapters, import checkpoints and reports, basic multilingual search, GTIN candidates, private foods and submissions, meal lifecycle, daily totals and goals, Nutrition Score and scoped CGM batch upload |
| Partially completed | German discovery works on localized labels and synonyms that exist in the catalog. The seed and fixture imports exercise it, while USDA generic releases supply English names. Scale evidence uses synthetic data. The submission API records referrals, while validation, moderation and promotion need an operator workflow. |
| Planned | Photo Storage and cleanup, recognition provider integrations, external barcode fallback, opt-in HealthKit export and journal effects, biometric insights and CGM provider sync, full portability export, approved catalog automation and managed dashboards |
| Blocked for rollout | Validate hosted Supabase PostgreSQL and Supavisor on approved staging, confirm privacy-policy/consent and OFF reuse obligations, and approve acquisition of production-scale datasets. Features stay dark at 0% rollout. |

## Validation

- The [standalone Python ETL](../../../scripts/etl/README.md) adds OFF Parquet streaming, USDA release discovery, catalog-only COPY and AHEI tagging. Hosted execution and a production-volume benchmark are pending.
- The [GCP rollout](../../../infra/gcp/README.md#nutrition-on-cloud-run) prepares a bounded initial import with the Terraform-managed scheduler paused. The API and calibration use the existing release and tick.
- Passed `go build ./...`, `go vet ./...`, `make lint`, `make check-api-doc`, and `make cloudrun-check`. The Cloud Run check ran locally without the pull-request base configuration.
- Local PostgreSQL migrations and Supabase-role simulation, including a preinstalled `pg_trgm` extension in a separate schema, and runtime-role authorization in pgx exec mode.
- Deterministic fixtures for USDA CSV and Open Food Facts JSONL. Writes test idempotency, partial batch rollback, resume, source changes and separate equivalent-barcode candidates. The CLI fixture runs inserted two USDA and three OFF sources. A second OFF write reported three unchanged sources and zero inserts.
- HTTP tests cover bearer identity, the rollout gate, write consent, strict JSON limits, scope, and payload-free logs. Store tests cover replay, catalog edits, partial values, UTC/local-day boundary changes, cross-account reads, writes and delete.
- The full database-backed Go suite passed outside the working checkout’s `internal/scoring` package. `TestTheSameEventTwiceChangesNothing` reported zero morning announcements where it expected one, including on a temporary copy with migration 0043 omitted. Storage isolation tests require the planned private asset workflow. The hosted smoke needs approved staging access. Local Compose smoke was skipped because the Podman VM connection failed its SSH handshake. The Nutrition HTTP tests ran through the API mux on isolated PostgreSQL.

## Capacity and performance

The reproducible opt-in probe is `RYFT_NUTRITION_PERF=1 RYFT_TEST_POSTGRES_URL=… go test ./internal/store -run '^TestNutritionCatalogPerformance$' -count=1 -v`. It uses 10,000 synthetic branded foods with one source and barcode per food, approximately 1.5 labels, one portion per four foods and 24 numeric nutrients per food. It runs against a local PostgreSQL 14.25 instance using pgx exec mode and warm reads. The measured 79 MiB includes approximately 49 MiB for nutrient rows and keys, 20 MiB for search rows and indexes, 4.5 MiB for provenance, 2.9 MiB for foods, 2.1 MiB for barcodes and 0.6 MiB for portions.

| Shared foods | Catalog extrapolation | Comfortable DB allocation with 50% catalog headroom | Optional float32 768-dimensional embeddings before index |
| --- | ---: | ---: | ---: |
| 10,000 | 79 MiB | 119 MiB | 29 MiB |
| 100,000 | 0.77 GiB | 1.16 GiB | 0.29 GiB |
| 500,000 | 3.85 GiB | 5.79 GiB | 1.43 GiB |
| 1,000,000 | 7.7 GiB | 11.6 GiB | 2.86 GiB |

These are linear planning estimates. Real data can have more aliases, longer source metadata and different nutrient coverage, provider mix and index bloat. WAL, replicas, backup storage, build overhead, photos and meals need separate allocations. An optional vector index would require its own measurement and model/version plan.

As a user-data planning assumption, 4 meals/day and 3 items/meal at roughly 32 KiB/day combined take about 11 GiB/year per 1,000 active accounts before operational headroom. Two compressed 400 KiB photos per day over a 30-day window would use about 23 GiB per 1,000 active accounts. The photo estimate excludes replicas and temporary OCR input.

Targets are barcode SQL p95 below 100 ms and text SQL p95 below 200 ms for the initial catalog. The barcode plan uses the GTIN B-tree. The selective inner search plan uses lexical indexes. Across 40 measured executions, SQL p95 was 0.018 ms for barcode, 0.951 ms for selective text and 50.174 ms for broad text. The selective inner plan took 0.381 ms at p95. Measurements exclude connection acquisition and HTTP time. Before wider rollout, repeat with approved market extracts, both hosts' normal load, production-shaped hardware, the real pooler, cold and warm data, and representative unselective terms.

## App sequence

1. **Search Food.** The App has a typed catalog client, wire fixtures and the default-off flag. The next UI step is a search screen that reads the flag, uses the current catalog language preference, and fetches detail after selection.
2. **Barcode Scan.** The client can look up a string barcode. Add native capture with textual GTIN preservation. Show explicit no-match and conflicting candidate states. Reuse private food creation as a fallback.
3. **Meal Review and Logging.** Confirm time, zone, quantity and measure. Persist client request IDs for offline retries and resolve revision conflicts. Show known subtotal and logged-item coverage separately.
4. **Import Food.** Start with reviewed manual sources, then add recipe text, label OCR and photo-library selection through the same draft.
5. **Capture Food.** Add private Storage, asset ownership, signed access, cleanup and provider-agnostic image extraction before accepting uploads.
6. **Nutrition Score.** The API returns versioned quality, glucose impact and final scores. Integrate display states for missing coverage, the ten components, and meal-level CGM results.
7. **HealthKit and Journal.** Add separately consented, one-way dietary exports with stable sync IDs, and source-attributed, reversible Journal events.
8. **Recovery and Sleep insights.** Evaluate within-person lagged associations, minimum sample sizes and uncertainty. Add optional glucose integration only with separate permission and validation.

## Run the App catalog tests

From the RYFT workspace, use the App's Unit test plan. The tests use fixture responses and a stub transport.

```sh
cd app
make config
nutrition_destination=$(CI_SIM_NAME="RYFT Nutrition $(uuidgen)" sh scripts/ci-simulator.sh)
xcodebuild -scheme Ryft -testPlan Unit -destination "$nutrition_destination" \
  -derivedDataPath build/DerivedData -disableAutomaticPackageResolution \
  -parallel-testing-enabled NO -only-testing:RyftTests/NutritionCatalogTests \
  -only-testing:RyftTests/APIClientStatusTests test
```

## Run the local catalog smoke

From the workspace, start the existing local Compose stack. If you use Podman on macOS and its existing VM is stopped, run `podman machine start` first. If you use Docker for the stack, replace `podman compose` with `docker compose`.

```sh
cd plattform
podman compose up --build -d
curl --fail --silent --show-error --retry 10 --retry-connrefused --retry-max-time 25 \
  http://localhost:8082/readyz
podman compose exec -T postgres psql -U ryft -d ryft -v ON_ERROR_STOP=1 --single-transaction \
  -f - < backend/internal/nutrition/testdata/seed.sql
```

Run the seed once on a fresh local database. On a subsequent smoke, keep the existing seed rows and start at the test account step. These commands use the Compose default development API key and a new synthetic account. If you changed the development key, set `API_KEY` locally to match the running stack.

```sh
nutrition_test_user=$(uuidgen | tr '[:upper:]' '[:lower:]')
podman compose exec -T postgres psql -U ryft -d ryft -v ON_ERROR_STOP=1 \
  -v tester="$nutrition_test_user" <<'SQL'
UPDATE feature_flags
SET allow_user_ids = array_append(allow_user_ids, :'tester'::uuid), updated_at = now()
WHERE key = 'nutrition.enabled' AND enabled AND rollout_pct = 0;
SQL

nutrition_bearer="${API_KEY:-dev-api-key-change-me}:$nutrition_test_user"
curl --fail-with-body --silent --show-error \
  -H "Authorization: Bearer $nutrition_bearer" -H 'Content-Type: application/json' \
  --data '{"query":"Hähnchenbrust","language":"de","market":"DE"}' \
  http://localhost:8082/v1/nutrition/foods/search
```

The response includes a food named `Hähnchenbrust`. The feature flag stays at 0% rollout and admits that synthetic account on your local database. Use an approved staging project and tester account for hosted verification.
