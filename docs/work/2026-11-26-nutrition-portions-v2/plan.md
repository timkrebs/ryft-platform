# Nutrition portions v2 (plattform plan, 2026-11-26)

The head-architect's plan (`../docs/work/2026-11-26-nutrition-portions-v2/plan.md`) can't be read from this session, so
this plan follows the inline brief. Branch `feat/nutrition-portions-v2` from `origin/main` (4f95b6c, after #254).
No API contract change, no migration, no Terraform change.

## Findings that shape the work
- `nutrition.portions.label` for USDA rows is the raw `portion_description`, or `amount + modifier`. For SR Legacy
  that is `1 medium (3" dia)`. `labels` is empty outside the 16 staples, so Go's `portionLabels` falls back to the
  raw label in both languages.
- Portion IDs don't depend on the label. Keyed rows use `source_portion_uuid(namespace, source_portion_id,
  measure)`. Unkeyed rows use `legacy_id`, which hashes `legacy_label`, so `legacy_label` must stay byte-identical.
- `undetermined` in production most likely comes from the Go importer (`importer/usda.go` joins `amount measure
  modifier`). Its rows are `origin='manual'`, and RLS (0051/0055) keeps the ETL role off them. G1 can't relabel
  those. A read-only count for the operator goes in the runbook, and the cleanup is a follow-up.
- The merge treats a changed checksum under an unchanged `dataset_version` as a **conflict**. G1 changes every
  USDA checksum, so the staples version must go from 1 to 2. Otherwise the rerun writes nothing.
- WebFetch, curl and local Python are refused in this session. CI's `test` job runs `scripts/etl/tests` against
  PostgreSQL, and that is the proof. USDA portion IDs for the new staples can't be looked up. So new staple portions
  match source rows by G1 key, by text or by measured grams, never by a guessed ID. A `source_description` guard
  makes a wrong FDC ID harmless, and the dry run reports every unmatched or mismatched staple.

## Tickets
| # | Ticket | Owner | Acceptance |
|---|---|---|---|
| T1 | G1 clean USDA portion labels (`transform.py`, `import_pg.py`, `merge_catalog.sql`, `run_etl.py` dry-run stats) | backend-engineer | No label holds `undetermined` or an imperial parenthetical. The modifier map is pinned. Sort orders: Klein 10, Mittel 20, Groß 30, household units 40+. Staples keep precedence. `is_default` is curated only. IDs don't change on relabel (PG test). A rerun is unchanged. |
| T2 | G2 staples: Kartoffeln gekocht, Avocado, Tomate, Karotte, plus at most 6 extras (`staples.v1.json`, version 2) | backend-engineer | Each resolves from Kartoffel/Avocado/Tomate/Karotte/Möhre. The default is curated and uses the source's grams. The dry run lists matches. |
| T3 | Tests (in T1/T2) | backend-engineer | FDC 171688 gives Klein 149, Mittel 182 (default), Groß 223. No `undetermined` anywhere. A representative SR Legacy modifier fixture. |
| T4 | Research G3/G4, `research.md` | general-purpose (web search) | Key numbers with sources, plus a recommendation. |
| T5 | D288, STATUS, ETL runbook (rollout steps) | lead | The operator steps in order. |
| T6 | Review of the ETL diff | code-reviewer | Findings in `review.md`. Blockers fixed and re-verified. |

## Rollout (the user's, after merge)
Publish the ETL image (`publish_nutrition_etl=true`), set the digest in the local `infra/gcp/terraform.tfvars`, then
plan and apply. Dry run `--source sr-legacy --limit 10000` and check the version `py-etl4:staples2:`, the portion
stats and the staple matches. Then write `--source sr-legacy,--write,--limit,10000` and `--source foundation,--write`.
Expect one wave of `updated`, then `unchanged` on a rerun.

## Rollback
Re-pin the previous image digest. Labels revert on the next write with the old image only if its version string
differs, and it doesn't (`staples1`), so the rows would become conflicts. Labels are cosmetic and IDs are unchanged,
so the rollback is "leave the new labels". Saved meals keep their snapshots.
