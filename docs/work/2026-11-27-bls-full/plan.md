# G4 · Full BLS 4.0 import (D296), plan

Branch `feat/nutrition-bls-full` (worktree `.worktrees/bls-full`, from origin/main cd94cf1). Headless run, 2026-11-27.

## Facts found
- **Licence:** CC BY 4.0 ("Creative Commons Namensnennung 4.0 International"), with the Max Rubner-Institut named as
  publisher ([blsdb.de/download](https://blsdb.de/download), [OpenAgrar record](https://www.openagrar.de/receive/openagrar_mods_00112643),
  both *(search)*). The FAQ names app and software development as a use ([blsdb.de/faq](https://blsdb.de/faq) *(search)*).
  Nothing in it blocks commercial use. Citation: "Max Rubner-Institut (2025): Bundeslebensmittelschlüssel (BLS), Version 4.0 —
  Deutsche Nährstoffdatenbank. Karlsruhe." DOI 10.25826/Data20251217-134202-0.
- **Files** *(search)*: `BLS_4_0_Daten_2025_DE.xlsx` has 7,140 foods and 138 components in 418 columns. Columns 1–3 are the BLS code,
  the German name and the English name. Each component then takes three columns: value per 100 g edible portion,
  data origin and reference. Codes are EuroFIR-style (ENERCJ, ENERCC, WATER, PROT625, FIBLMW, …). There is also
  `BLS_4_0_Components_DE_EN.xlsx`. No density or volume basis was found.
- **The session can't download it:** curl and WebFetch both need approval. So it's unknown whether the download needs a form or a
  click-through. No local copy exists. **The data file is a user step** (see "Needs you").

## Design (D296)
1. **The data is a reviewed, committed extract.** `scripts/etl/bls_convert.py` (stdlib only, a streaming xlsx reader) turns the
   official xlsx into `scripts/etl/data/bls_4_0.csv` (code, de, en, our registry nutrients in our units) plus
   `bls_4_0.manifest.json` (source file sha256, header→code→unit mapping, counts). The job reads the CSV, with no runtime download,
   no DuckDB and no new dependency. Diffs can be reviewed. `data/README.md` holds the CC BY notice and the list of changes.
2. **Without the CSV** (this PR as merged, before the data commit), `--source bls` imports the three curated staples exactly as
   today, with the same version string, so the result is `unchanged`. The dry run says `"bls_full": false`.
3. **Identity:** provider `bls`, external_id is the BLS code, and the food UUID is uuid5(provider:code), as today. Version
   `py-etl4:bls_4_0:staples2:<sha256 of CSV + curation file>`, so an input change gives `updated` and never `conflict`. A rerun gives `unchanged`.
4. **Curated staples** (M111300, M111200, M713100) keep their curated names, aliases, portions, group, form and density. Their nutrients come from the CSV
   (a superset). The dry run reports `staple_nutrient_drift` where a curated value and the CSV differ.
5. **Twins of USDA staples** (the same food as a curated staple, e.g. BLS "Möhre, roh" vs the Karotte staple): a curated `twins`
   map in `bls_curation.v1.json` (BLS code → staple + expected German name as a guard). An applied twin is **not imported**
   (`skipped_twin`). On a name mismatch it imports normally and shows under `twin_mismatch`. The list starts empty because nobody has seen the file.
   The dry run lists `twin_candidates` for review in the data commit.
6. **Aliases:** curated DACH synonym groups in `bls_curation.v1.json`, applied case-sensitively and only to whole words of the BLS
   German name (Möhre→Karotte, Speisequark→Quark/Topfen, Brötchen→Semmel, …). This is substitution, not translation. At most 6 per food.
7. **Categories:** `classify(name_de, [BLS main group name from the code's first letter], provider="bls")`. Discovery only:
   there is no `ryft_nutrition` scoring profile, as for OFF. `food_form` comes from German state words (roh→raw, gekocht→boiled, …), else NULL.
   There is no `canonical_group_key`, so the forms never move a row in the ranking.
8. **Portions:** none for non-staples, only `reference_portion` 100 g and the app's g/ml input. There is no link to USDA portion sets
   (no reviewed crosswalk, and the sets are US sizes). Twins are skipped, so the staple's portions serve that food.
9. **Ranking (0061):** de: BLS German names reach match tiers 7/6/4. SR Legacy rows (English only) reach a German query only through the
   D283 alternate (tier 2), so BLS ranks first. Staples keep tiers 10/9 through promoted groups. One gap: a BLS exact name and a staple
   exact alias tie at tier 7, and `preferred_market` (BLS `DE`, USDA none) puts BLS first. 0061 adds `c.is_canonical_staple DESC`
   right after `c.match DESC`, and the function keeps its signature (rollback-safe). en: BLS English names compete with SR as equals.
   In DE/AT/CH… only `markets=['DE']` breaks a tie, which favours BLS for DE users.

## Tickets
| # | Owner | Files | Acceptance |
|---|---|---|---|
| T1 | backend-engineer | `scripts/etl/{bls_convert.py,bls_curation.v1.json,transform.py,run_etl.py,Dockerfile,.dockerignore,.gcloudignore,stage_build_context.sh,data/README.md,README.md}`, `scripts/etl/tests/*` | fixture xlsx → CSV + manifest; mapping and units fail closed; transform/aliases/twins/forms; no CSV → today's behaviour; PG: full fixture import, then rerun `unchanged`; converter peak RSS on a full-size synthetic sheet < 512 MiB |
| T2 | database-engineer | `backend/internal/db/migrations/0061_*.sql`, `backend/internal/store/nutrition_ranking_test.go`, migrate/perf tests if they pin | de: Gurke→staple, Linsen→BLS before SR, Apfel→staple (BLS present), Karotte→staple, Möhre→staple over a BLS exact "Möhre"; en: lentils with market DE → BLS first |
| T3 | platform-lead | `docs/DECISIONS.md` D296, `docs/STATUS.md`, plan/review | — |
| R  | code-reviewer | read-only | review.md |

## Risks
- The xlsx layout differs from the documented one → the converter fails closed and lists the headers. Nothing is written.
- Ranking: 7k more generic rows. The search scans `food_names`, which roughly doubles. 0058 indexes. CI's perf test.
- Size: ~7,140 foods × (1 food, 1 source, ~2.1 names, ~30 nutrients, 1 classification) ≈ 250k rows, **~30–45 MB** with indexes.

## Rollback
Re-pin the previous ETL digest. Imported BLS rows stay (data); `status` can retire them if needed. 0061 → re-apply 0059's body.

## Needs you (in order)
1. Download `BLS_4_0_Daten_2025_DE.xlsx` from https://blsdb.de/download (and say whether it asked for registration or acceptance).
2. Run the converter, commit the CSV, review `twin_candidates`, or allow `curl https://blsdb.de/*` for a follow-up run.
3. After merge: publish the image, pin the digest in local tfvars, plan/apply, dry run, write (STATUS).
