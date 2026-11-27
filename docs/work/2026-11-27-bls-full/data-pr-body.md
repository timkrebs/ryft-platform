The BLS 4.0 extract from the user's file, its twins, and the converter fixes the real file needed (D296). It follows #257.

## What's in it
- `scripts/etl/data/bls_4_0.csv` (1.58 MB, 7,140 foods) and `bls_4_0.manifest.json`, converted from `BLS_4_0_Daten_2025_DE.xlsx` (blsdb.de, no registration; 14,093,078 bytes, sha256 `524bbefe25b691f5cb3de7a9f3e27fa2967aebfeabf217d99414ba7806e78c60`). There are 33 components and 0 unparsed cells. Not in BLS 4.0: selenium, trans fat, caffeine. The CSV passes the `bytes` character policy, and the repo has no size cap.
- **20 twins** (`bls_curation.v1.json`): BLS rows that are a USDA staple in the same state. They are not imported, so a German query shows the staple once: Apfel, Apfelsaft, Avocado, Banane, Tomate, Gurke, Karotte/Möhre, Speisezwiebel, Gemüsepaprika rot (all raw), Kartoffel geschält gekocht, Reis poliert roh/gekocht, Hafer Flocken, Hühnerei roh/gekocht, Hähnchen Brust(filet) roh ×2, Weizenvollkornbrot, Olivenöl, Rapsöl. Left as separate foods (unsure, listed in D296): `B101000` Vollkornbrot (usually rye–wheat), `X820162` Reis gekocht (a salted dish), `V416162` oven-roasted chicken breast, the plain yoghurts, and all other states and dishes.
- **Twin candidates** now ignore state words and spaces (`twin_keys`; review finding 1). The first rule only compared the first comma segment and missed "Banane roh".
- **`--no-bls-csv`** refuses `--write` when the catalog holds a full BLS import (`refuse_staples_over_full_bls`; review finding 2). It can no longer swap the staples' CSV nutrients for the curated subset.
- **Attribution**: `food_sources.attribution` is now the publisher's exact citation: "Max Rubner-Institut (2025): Bundeslebensmittelschlüssel (BLS), Version 4.0 — Deutsche Nährstoffdatenbank. Karlsruhe. DOI: 10.25826/Data20251217-134202-0". The short sentence for the site (#252) and the About screen stays in D296. It is part of each food's checksum, so it ships with the CSV: the CSV moves the version, and the three staples become `updated`, not `conflict`.

## Fixes the real file needed (converter version 2, D296)
1. **Markers.** `TR`, `<LOD`, `<LOQ` and `<LOD or <LOQ` are stored as **0**: their data-origin cells say "Spuren" (traces) or an analysis below the limit. `-` (origin `-`, no data) stays unknown. Any other text in a required component still fails the run. The manifest counts the markers per component.
2. **Codes.** 772 foods have a letter in position 3 or 4 (`M5B1600` Gorgonzola, `P2A3000` Rotwein trocken, `M1A5200` H-Milch laktosefrei). The pattern is now `[A-Z][0-9][0-9A-Z]{2}[0-9]{3}` in the converter, in `transform.BLS_ID` and in the DB constraint `nutrition_bls_attribution`. **Migration 0062** swaps the constraint in `NOT VALID` in one ALTER, and **0063** validates it under SHARE UPDATE EXCLUSIVE (one transaction per file, as 0055/0056 do).
3. **EPA/DHA** headers are `F20:5CN3`/`F22:6CN3`, so a header code may contain a colon.

## The 91 Atwater rejects (not changed here)
BLS energy follows EU 1169/2011: fibre at 2 kcal/g, organic acids at 3, polyols at 2.4. The ETL's shared 4/4/9 check rejects kale (9 forms), raspberries, currants, celeriac, artichoke, shallots, oyster mushrooms, rhubarb, plain coffee and tea, plus acids, polyols and sweeteners. D296 proposes a BLS-only check as a follow-up: 4P + 4C + 9F + 2·fibre + 7·alcohol, ±30 %, with a 5 kcal floor. Rejected rows are never written, so the follow-up adds them as `inserted`.

## Verification
- Python, local (stdlib Python 3.15 without psycopg/duckdb; pip needed approval): `ConverterTests`, `ConverterMemoryTests` and `TransformTests` pass, 38 tests. code-reviewer found no blockers; two should-fix findings and three nits are fixed (`docs/work/2026-11-27-bls-full/data-review.md`). The CLI and Postgres tests run in CI.
- Go: `go vet ./...` and `go test ./...` are green locally. The Postgres migration tests (0062/0063 accept `M5B1600` and refuse `MA51600`, `HINWEIS`, `m5b1600`, …) run first in CI.
- Local dry run of the BLS branch of `run_etl.py` (`--full`, no database): processed 7,140, accepted 7,026, skipped 20 (`skipped_twin`), rejected 94 (91 `atwater_deviation`, 3 `missing_or_invalid_macro`: glutamate, E 450, E 501), `aliases_added` 1,554, empty `staple_nutrient_drift`, `staples_missing` and `twin_mismatch`. Version `py-etl4:bls_4_0:staples2:437c3c027953b3918557e50df4bd6fa0a4f3e01fbbdb54c043b293ee26379229`.

## On merge
`deploy.yml` push run: the migrate job applies **0062** and **0063**, the services roll with unchanged code, then smoke-prod runs. The site job skips. The ETL image is **not** published on push.

## Needs you, after the merge, in order
1. Publish the image: `gh workflow run deploy.yml --ref main -f publish_nutrition_etl=true`. Then find the run with `gh run list --workflow deploy.yml --event workflow_dispatch --limit 1` and read the digest from its **Nutrition ETL image** summary.
2. In your local, git-ignored `plattform/infra/gcp/terraform.tfvars`, replace the old `nutrition_etl.image_digest` with the new `sha256:…`. Change nothing else.
3. Plan from a clean checkout of `main`:
   ```bash
   git -C plattform fetch origin main
   git -C plattform worktree add --detach .worktrees/bls-plan origin/main
   cp plattform/infra/gcp/terraform.tfvars plattform/.worktrees/bls-plan/infra/gcp/terraform.tfvars
   terraform -chdir=plattform/.worktrees/bls-plan/infra/gcp init
   terraform -chdir=plattform/.worktrees/bls-plan/infra/gcp plan -out=bls-full.tfplan
   ```
   Expect **0 to add, 1 to change, 0 to destroy**: `google_cloud_run_v2_job.nutrition_etl[0]`, the image only. Anything else: stop.
4. `terraform -chdir=plattform/.worktrees/bls-plan/infra/gcp apply bls-full.tfplan`
5. Dry run: `gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --args=--source,bls,--full --wait`. Expect `"mode": "dry-run"`, `bls_full: true`, processed 7,140, accepted 7,026, skipped 20, rejected 94 (`atwater_deviation` 91, `missing_or_invalid_macro` 3), `aliases_added` 1,554, empty `twin_mismatch`, `staple_nutrient_drift` and `staples_missing`, and `stale_twins` listing the 20 twins with `imported: null`. The version is the one above.
6. Write: `gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --args=--source,bls,--full,--write --wait`. Expect **7,023 inserted, 3 updated** (the BLS staples M111300/M111200/M713100), 0 unchanged, 0 conflict, and `stale_twins` all with `imported: false`. A rerun gives 7,026 `unchanged`.
7. Roll back: re-pin the previous digest. Imported rows stay; retire them by `status` if they must go. 0062 is a superset of the old check and needs no rollback.
