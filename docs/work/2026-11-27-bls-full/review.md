# Review: G4 full BLS import (code-reviewer, 2026-11-27, read-only, no Python run)

No blockers. Fixes by the backend-engineer (resumed), desk-checked. The lead read the run_etl/packaging diff. CI is the first run.

| # | Sev | Where | Finding | Fix | Owner | State |
|---|---|---|---|---|---|---|
| 1 | should | run_etl.py:143 | The version digest covers only the CSV and the curation file, so a change to the transform/categorizer/staples gives `conflict` | `bls_input_digest` adds staples.v1.json and `BLS_TRANSFORM_VERSION` | backend-engineer | fixed |
| 2 | should | transform.py twins | A twin added after a write stays active | `stale_twins` in the dry run (with a source-exists check under `--write`); the runbook says review twins before the first write and retire them by hand | backend-engineer, lead | fixed |
| 3 | should | bls_convert.py | Unparsable cells become blanks silently; "3.700" is read as 3.7 | `unparsed` and samples in the manifest; fail on the thousands pattern and on unparsed required cells | backend-engineer | fixed |
| 4 | should | bls_convert.py | Name columns are assumed | The headers of columns 1–3 are checked; `--name-columns` | backend-engineer | fixed |
| 5 | nit | packaging | `data/**` too broad; `*.xlsx` not ignored | Allowlist of 3 files; `scripts/etl/**/*.xlsx` ignored | backend-engineer | fixed |
| 6 | nit | bls_convert.py | Python 3.11+ | Checked at start | backend-engineer | fixed |
| 7 | nit | bls_convert.py | CSV and manifest pair not atomic | `csv_sha256` in the manifest; run_etl refuses a mismatch | backend-engineer | fixed |
| 8 | nit | transform.py | A malformed row mid-file leaves batches committed | Whole file validated before the first batch | backend-engineer | fixed |
| 9 | nit | docs/API.md | Changelog entry missing; D296 missing | Both added; `make lint` green | lead | fixed |
| 10 | nit | test_bls.py | The memory test adds ~30–60 s to CI | Accepted | — | accepted |
