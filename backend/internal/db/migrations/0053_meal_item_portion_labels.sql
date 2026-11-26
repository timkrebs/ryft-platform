-- D285: portion label snapshots on meal items, added empty. The constraints are NOT VALID here and validated by 0054, so this file holds its lock only for the catalog change.
ALTER TABLE nutrition.meal_items
    ADD COLUMN portion_label_snapshot text,
    ADD COLUMN portion_short_label_snapshot text,
    ADD CONSTRAINT meal_items_portion_label_length
        CHECK (length(portion_label_snapshot) BETWEEN 1 AND 200) NOT VALID,
    ADD CONSTRAINT meal_items_portion_short_label_length
        CHECK (length(portion_short_label_snapshot) BETWEEN 1 AND 200) NOT VALID;
