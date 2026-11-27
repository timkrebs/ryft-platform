# BLS data extract

This directory is copied into the nutrition ETL image. The import reads `bls_4_0.csv` from here when the file exists.
Without it, `--source bls` imports only the three curated staples (`staples.v1.json`).

| File | What it is |
|---|---|
| `bls_4_0.csv` | The reviewed extract: one row per food (`bls_code`, `name_de`, `name_en`, then RYFT's nutrient codes per 100 g in RYFT's units). An empty field is an unknown value. |
| `bls_4_0.manifest.json` | How the extract was made: the SHA-256 of the CSV (`csv_sha256`, which the import checks), source file name and SHA-256, sheet, header row, per nutrient the BLS component code, header text, unit, conversion factor, the number of values and of cells that were not numbers (with samples), row counts and the converter version. |

Only these three files go into the image. The workbook (`*.xlsx`) is never committed.

## Regenerate

The extract is built offline from the official workbook and committed, so changes show up in review. The job never
downloads anything.

```bash
cd scripts/etl
python3 bls_convert.py BLS_4_0_Daten_2025_DE.xlsx
```

The converter uses only the standard library. It stops with a message and writes nothing when the layout differs from
the documented one (a required component is missing, a unit is not in the header, a code appears twice). A header
without a unit can be fixed with `--unit CODE=unit`, for example `--unit SE=ug`. Running it twice on the same workbook
gives byte-identical files.

## Notice

Contains data from the Bundeslebensmittelschlüssel (BLS), Version 4.0, Max Rubner-Institut (2025), Karlsruhe, DOI 10.25826/Data20251217-134202-0, licensed under CC BY 4.0 (https://creativecommons.org/licenses/by/4.0/). Changes by RYFT: a subset of components, converted to RYFT's units, synonyms added as search aliases, foods that duplicate a curated staple left out.
