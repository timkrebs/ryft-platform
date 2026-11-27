# Review: fix/nutrition-etl-memory (D287)

code-reviewer, one pass, 2026-11-26, on the uncommitted diff over `d53bb29`.

No blockers. Code change correct, new test should pass.

## Should fix
1. `infra/gcp/README.md` step 1 still says `nutrition_etl` lives in HCP workspace `ryft-gcp`; step 4 and STATUS now
   say it's local `terraform.tfvars`. If an HCP variable exists it may shadow the local value and the plan
   would not move the image. → **Fixed**: step 1 reworded, with a check for a leftover workspace variable.
2. The test hasn't been shown to fail on the old SQL; D287 says the cause wasn't reproduced. → **Open, stated**:
   Python can't run in this headless session and CI is read once. The production dry run (step 3) is the proof.

## Nits
3. D287 said #248 added the second ordered aggregate; `ORDER BY priority` already existed in `24b3b24`. #248 added
   fields and the staples ordering in `selected`. → **Fixed** in D287.
4. `extract.py` `"_" not in raw` assumes DuckDB's `try_cast` rejects underscores. → **Fixed**: the comment says so.

## Checked sound
Sort equivalence (only duplicates of one nutrient ID matter; non-finite values fail `number()` either way), row
shape (`nutrients`/`portions` always present, list or None), test trace (`["9","5",None]`, 32 sorted IDs,
priorities 1..23), other memory consumers (window ~15k rows, top-N, small hash builds), `--args` syntax, tfvars
shape, version prefix, 13 SR Legacy staples.
