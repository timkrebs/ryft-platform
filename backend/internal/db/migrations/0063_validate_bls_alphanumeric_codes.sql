-- D296: Validate the constraint 0062 swapped in NOT VALID. VALIDATE CONSTRAINT scans
-- food_sources under SHARE UPDATE EXCLUSIVE, which does not block reads or writes; it
-- is a separate file because the runner wraps each file in one transaction (see 0062).
-- On an already valid constraint it is a no-op, so a rerun is safe.
ALTER TABLE nutrition.food_sources
    VALIDATE CONSTRAINT nutrition_bls_attribution;
