-- D285: validate the constraints 0053 added NOT VALID (SHARE UPDATE EXCLUSIVE: reads and writes continue).
ALTER TABLE nutrition.meal_items VALIDATE CONSTRAINT meal_items_portion_label_length;
ALTER TABLE nutrition.meal_items VALIDATE CONSTRAINT meal_items_portion_short_label_length;
