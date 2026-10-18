-- Readiness is the Today screen's headline (0.6·recovery + 0.4·sleep performance,
-- minus an overreach penalty); state is the word under the greeting. Rows scored
-- before this migration read as readiness NULL / 'calibrating' until recomputed.
ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS readiness smallint;
ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT 'calibrating';
