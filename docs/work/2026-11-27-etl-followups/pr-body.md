Follow-ups to the full BLS import (`nutrition-etl-job-4r7vd`, image `sha256:d56ac763…`) and portions v2. D313; findings and review in `docs/work/2026-11-27-etl-followups/`.

## What changes
- **BLS rejects (`bls2`).** BLS energy (`ENERCC`) counts fibre at 2 kcal/g (EU 1169/2011, Annex XIV), and BLS `CHO` doesn't include fibre. The data is right; the rule was wrong. For BLS rows only, the Atwater check now adds fibre × 2 and accepts within 30 % or within 2 kcal (BLS rounds kcal). It runs after the existing paths, so no previously accepted row changes.

  | | before | after |
  |---|---|---|
  | accepted | 7,026 | 7,105 |
  | rejected | 94 | 15 |
  | skipped | 20 | 20 |

  - 72 of the 79 recovered rows are fibre-rich vegetables, fruit and mushrooms (Grünkohl, Artischocke, Himbeere, Morchel, …) that fall inside 30 % once fibre counts.
  - The other 7 are near-zero drinks (coffee, tea), which pass on the 2 kcal floor.
  - Still rejected, on purpose: 6 organic acids and 6 polyols/sweeteners (the extract has no `OA`/polyol columns), and 3 additives with no fat value.
  - The 20 skipped are the curated USDA twins (D296), by design.
- **Portions.** Salatgurke (168409) gets ½ Gurke = 200 g and Naturjoghurt (171284) gets 1 Becher = 150 g, both via a new curated kind `preset_grams`. Both are the default and sort first. SR Legacy has no fitting measure; the values are documented typical German sizes. Staples version 3.
- **Foundation release.** On a stale ZIP, `stage_usda` used to fall back to the previous release (#240), so the write read 2026-04-30 (469 rows) after the dry run read 2026-10-29 (498). Now it makes three attempts on the newest release, each with a fresh query parameter and `no-cache` headers, and then fails naming the release. It never imports an older one. Under `--source all` that failure ends the run.
- **German labels.** About 40 more SR Legacy head words and 11 qualifiers are mapped. The USDA summary gains `unmapped_heads` (the top 25 with counts). There is no local before/after count: no USDA sample is committed and there was no network. The dry run below gives the count.

## Tests
- `scripts/etl/tests/test_followups.py` passed locally (23 tests, stdlib only). It pins the real-CSV counts, the floor and fibre cases, that USDA is unaffected, the presets and their validation, the heads and `unmapped_heads`.
- `test_etl.py` and `test_bls.py` need httpx, duckdb and psycopg, so CI is their first run.
- One code-reviewer pass found 1 blocker (a stale English-only label expectation) and several nits. All are fixed or documented.

## On merge (`deploy.yml`)
- The usual push run: the five service images are built and scanned, and the migrate job runs (no new migration).
- Services roll in the order scoring, notify, ingest, api, with unchanged backend code, then `smoke-prod`.
- The site is not redeployed (`web/` is unchanged).
- **The ETL image is not published by the merge.**

## For you, in order (a new image and a tfvars digest are needed)
1. Publish the ETL image from main, then take the digest from the run's **Nutrition ETL image** summary:
   ```bash
   gh workflow run deploy.yml --ref main -f publish_nutrition_etl=true
   gh run list --workflow deploy.yml --event workflow_dispatch --limit 1
   ```
2. In the local `infra/gcp/terraform.tfvars`, set `nutrition_etl.image_digest = "sha256:<new digest>"` and keep the other fields. Then plan and apply. The plan should change only the image of `google_cloud_run_v2_job.nutrition_etl`.
   ```bash
   terraform -chdir=infra/gcp plan -out=nutrition-etl-followups.tfplan
   terraform -chdir=infra/gcp apply nutrition-etl-followups.tfplan
   ```
3. Dry runs:
   ```bash
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait --args=--source,bls,--full
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait --args=--source,foundation,--limit,10000
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait --args=--source,sr-legacy,--limit,10000
   ```
   - **BLS:** processed 7,140, accepted 7,105, rejected 15 (12 `atwater_deviation`, 3 `missing_or_invalid_macro`), skipped 20. The version is `py-etl4:bls_4_0:staples3:<digest>`.
   - **Foundation:** `source_release` 2026-10-29, about 498 accepted. If it fails with `still incomplete after 3 attempts, not falling back to an older release`, USDA is serving a stale ZIP. Rerun later, and don't write.
   - **SR Legacy:**
     - The version starts with `py-etl4:staples3:sr-legacy:`, and about 7,793 are accepted.
     - `staples.description_mismatch` and `staples.unmatched` are empty, and `portions.raw_marker` is 0.
     - `portions.unmapped_head` is well below the earlier 3,375. Please send back that line with `unmapped_heads` for the next pass.
4. Writes, only if the dry runs match:
   ```bash
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait --args=--source,bls,--full,--write
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait --args=--source,foundation,--write,--limit,10000
   gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --wait --args=--source,sr-legacy,--write,--limit,10000
   ```
   The row checksum excludes the version, so only changed content is `updated`. What to expect:
   - **BLS:** 79 `inserted`, 7,026 `unchanged`, 0 `conflict`.
   - **Foundation** (moving from the 2026-04-30 rows): at least 29 `inserted`, the rest `updated` or `unchanged`, 0 `conflict`. Foods whose FDC IDs USDA replaced stay in the catalog next to their successors; retiring them is a separate step.
   - **SR Legacy:** the two preset foods and the relabelled foods are `updated`, the rest `unchanged`, 0 `conflict`.
   - A rerun of any write gives `unchanged`.

Rollback: revert, and pin the previous digest `sha256:d56ac763…` in `terraform.tfvars`. Imported rows stay.
