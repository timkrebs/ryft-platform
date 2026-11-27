-- D296: BLS 4.0 codes are not all seven digits. 772 food codes carry a capital letter in
-- position 3 or 4 (M5B1600, P2A3000, M1A5200, U57B100) next to the all-digit ones
-- (M111300). nutrition_bls_attribution (0055) pinned external_id to '^[A-Z][0-9]{6}$',
-- so the ETL's --write would fail on them. The shape is now: a letter, a digit, two
-- digits or capitals, three digits. It is a strict superset of the old one, so every
-- existing bls row (all digit codes) still passes. Only the external_id pattern
-- changes; the license and attribution conditions are as in 0055.
--
-- Lock reasoning. The migrate runner applies each file in one transaction, so a
-- VALIDATE in this file would keep the ACCESS EXCLUSIVE lock taken by DROP/ADD
-- CONSTRAINT for the whole table scan. Same split as 0055/0056: this file only swaps
-- the constraint as NOT VALID (a catalog update; new and updated rows are checked from
-- commit on), 0063 validates it under SHARE UPDATE EXCLUSIVE, which does not block
-- reads or writes. DROP and ADD are one ALTER, so there is no moment without the check.
-- DROP IF EXISTS keeps a rerun safe. Rolling back across this release is safe: the
-- looser check is ignored by older binaries, which never write letter codes.
ALTER TABLE nutrition.food_sources
    DROP CONSTRAINT IF EXISTS nutrition_bls_attribution,
    ADD CONSTRAINT nutrition_bls_attribution CHECK (provider <> 'bls' OR
        (license_identifier = 'CC-BY-4.0' AND attribution IS NOT NULL
            AND external_id ~ '^[A-Z][0-9][0-9A-Z]{2}[0-9]{3}$')) NOT VALID;
