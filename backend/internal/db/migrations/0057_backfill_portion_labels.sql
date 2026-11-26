-- D284: Recover legacy portion labels for saved diary items. Needs the snapshot columns
-- from 0053. Only recover a label when the original ID, food, unit and measured
-- quantity still agree. Never recompute a saved item's nutrient snapshot. The
-- statement is idempotent: it only fills rows whose label is still empty.
UPDATE nutrition.meal_items i SET portion_label_snapshot=p.label
FROM nutrition.portions p
WHERE i.portion_label_snapshot IS NULL
    AND i.portion_id=p.id AND i.food_id=p.food_id AND i.unit=p.unit
    AND (p.weight_g IS NOT NULL OR p.volume_ml IS NOT NULL)
    AND (p.weight_g IS NULL OR i.weight_g=round(p.weight_g*i.quantity/p.quantity,6))
    AND (p.volume_ml IS NULL OR i.volume_ml=round(p.volume_ml*i.quantity/p.quantity,6));
