G4 (D296): the full Bundeslebensmittelschlüssel 4.0 as the German generic layer of the nutrition catalog, with curated DACH synonyms and no machine translation.

## What changes
- **`scripts/etl/bls_convert.py`** (new, offline, stdlib only): streams the official `BLS_4_0_Daten_2025_DE.xlsx` into `scripts/etl/data/bls_4_0.csv` plus `bls_4_0.manifest.json` (source sha256, component mapping, units, unparsed cells, counts). It fails closed on unknown units, missing macros, unexpected name columns or unparsable required values, and lists what it found.
- **The ETL** (`transform.py`, `run_etl.py`): with the CSV, `--source bls` imports every BLS food with its German name (canonical), its English name, the nutrients we track per 100 g, a category hint from the main group, a `food_form` from German state words, the BLS attribution, `markets=['DE']`, and no portions. The three curated BLS staples keep their curated names, aliases and portions, and their nutrients come from the CSV. Twins of USDA staples (`bls_curation.v1.json` → `twins`, empty until the data has been reviewed) are left out. **Without the CSV the behaviour is byte-identical to main** (3 staples, `py-etl4:bls_4_0:staples2`, `unchanged`).
- **Synonyms** (`bls_curation.v1.json`): 40 curated groups (Möhre/Karotte, Speisequark/Quark/Topfen, Brötchen/Semmel, …), applied case-sensitively to whole words only, at most 6 aliases per food.
- **Migration 0061**: `search_foods_v2` adds `is_canonical_staple DESC` right after `match DESC`, so a curated staple wins an exact-name tie against a BLS row (before, the market decided). Same signature, so it's rollback-safe.
- **Image**: `bls_curation.v1.json` and `data/` (README, plus the CSV and manifest once committed).
- **Docs**: D296 (licence, attribution text, design), STATUS, ETL README, API.md changelog (behaviour only, no shape change).

## Licence
CC BY 4.0 with the Max Rubner-Institut named as publisher (blsdb.de/download, OpenAgrar; from search snippets, because the session could not open the pages). App development is a named use. The attribution sentence for the site (#252) and the app's About screen is in D296.

## Tests
- Python (`tests/test_bls.py`): an xlsx fixture built at runtime goes through the converter to the CSV and manifest; a rerun is byte-identical; the fail-closed paths and unit conversion are covered. The transform tests cover names, category, form, synonyms (and their non-matches), twins and the staple overlay. Without the CSV, the staples-only path holds. The memory test parses a full-size synthetic sheet (7,140 × 418) and asserts peak RSS < 512 MiB, with RLIMIT_AS 1 GiB on Linux. On Postgres, a fixture import is followed by a rerun that returns `unchanged`.
- Go (`TestNutritionRankingBLSGerman`): the `de` queries Gurke, Apfel, Karotte and Möhre return the staple first, and Linsen returns the BLS row before SR Legacy. Möhre fails on 0059. For `en`, lentils in market DE returns BLS first, and apple returns the staple.
- Nothing in Python ran locally (refused in this headless session), so CI is the first run. `go vet` and the Go tests ran locally; the Postgres tests skip there and run in CI.

## On merge
`deploy.yml` push run: the migrate job applies **0061**, the services roll with unchanged code, then smoke-prod. The site job skips. The ETL image isn't published on push, so **no catalog data changes on merge.**

## The user, in order
1. Download `BLS_4_0_Daten_2025_DE.xlsx` from https://blsdb.de/download and report whether it needed registration or acceptance.
2. Run `python3 bls_convert.py BLS_4_0_Daten_2025_DE.xlsx` (from `scripts/etl`, Python 3.11+), then `python3 run_etl.py --source bls`. Review `twin_candidates`, `staple_nutrient_drift` and the manifest, fill `twins`, and commit the CSV, manifest and twins as a separate PR.
3. After that PR: `gh workflow run deploy.yml --ref main -f publish_nutrition_etl=true`, then set the digest in the local `infra/gcp/terraform.tfvars`, `terraform -chdir=infra/gcp plan -out=bls-full.tfplan` and apply it.
4. `gcloud run jobs execute nutrition-etl-job --project=ryftlab-prod --region=europe-west1 --args=--source,bls,--full --wait` (dry run), then the same with `--write`.
5. Add the attribution sentence to #252 and the app's About screen.
