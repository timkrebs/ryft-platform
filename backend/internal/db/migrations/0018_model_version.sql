-- The scoring model that computed a score (docs/DECISIONS.md D80): set by scoring on
-- every daily_scores row and every workout it scores; NULL for rows from before,
-- until they are rescored (scripts/rescore_all.sh).
ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS model_version text;
ALTER TABLE workouts     ADD COLUMN IF NOT EXISTS model_version text;

-- scoring writes it next to the load it already writes (0009, 0011).
GRANT UPDATE (model_version) ON workouts TO ryft_scoring;
GRANT INSERT (model_version) ON workouts TO ryft_scoring;
