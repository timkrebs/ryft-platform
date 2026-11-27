"""The ETL follow-ups of D313: the BLS energy rule, the curated preset portions, the new portion heads.

Standard library only (no DuckDB, httpx or PostgreSQL), so these run anywhere `transform` imports. The release
selection tests of D313 need httpx and live in test_etl.py.
"""

import csv
import hashlib
import json
import unittest
from collections import Counter

import transform
from transform import (BLS_CSV_PATH, BLS_TRANSFORM_VERSION, STAPLES, ImportNotes, Nutrient, Reject, Skip, bls_amounts,
                       bls_row_food, clean_portion, head_word, load_curation, usda_food, usda_measures, validate)

CURATION = load_curation()
FDC_NUTRIENTS = [{"id": key, "amount": amount} for key, amount in (("1008", 48), ("1003", 0), ("1004", 0), ("1005", 12))]


def amounts(**values):
    return {code: Nutrient(code, value, "test") for code, value in values.items()}


def bls_row(code="G999000", name="Testgemüse", **values):
    base = {"energy_kcal": "100", "protein": "5", "fat": "2", "carbohydrates": "15"}
    return {"bls_code": code, "name_de": name, "name_en": "", **base, **{k: str(v) for k, v in values.items()}}


def sr_row(position, modifier, grams, amount="1", source_id=None):
    return {"source_id": source_id, "description": None, "amount": amount, "measure": "undetermined",
            "modifier": modifier, "grams": grams, "priority": position}


class BlsEnergyRuleTests(unittest.TestCase):
    """D313: BLS `CHO` is the available carbohydrate and `ENERCC` counts fibre at 2 kcal/g (EU 1169/2011 Annex XIV)."""

    # kale-like: 1 g protein, 5 g carbohydrate, 0.2 g fat, 8 g fibre. Atwater 25.8 kcal, with fibre 41.8, label 42.
    KALE = dict(energy_kcal=42, protein=1, carbohydrates=5, fat=0.2, fiber=8)

    def test_version_is_bumped(self):
        self.assertEqual(BLS_TRANSFORM_VERSION, "bls2")

    def test_fibre_counts_at_two_kcal_per_gram_for_bls_only(self):
        nutrients = amounts(**self.KALE)
        with self.assertRaisesRegex(Reject, "atwater_deviation"):
            validate("Grünkohl", nutrients, False)  # the rule everything else uses: 15.4 kcal off, more than 30 %
        self.assertEqual(validate("Grünkohl", nutrients, False, bls=True), ())
        # Through the row transform, the same numbers.
        food = bls_row_food(bls_row(**self.KALE), "v", CURATION)
        self.assertEqual(food.warnings, ())

    def test_a_usda_row_with_the_same_numbers_is_unaffected(self):
        """USDA `carbohydrates_total` already holds the fibre, so adding it again would count it twice."""
        usda = {"fdc_id": "999001", "description": "Kale, raw", "portions": None, "category": None, "nutrients": [
            {"id": "1008", "amount": 42}, {"id": "1003", "amount": 1}, {"id": "1004", "amount": 0.2},
            {"id": "1005", "amount": 5}, {"id": "1079", "amount": 8}]}
        with self.assertRaisesRegex(Reject, "atwater_deviation"):
            usda_food(usda, "v")
        # The same grams as a BLS row pass.
        bls_row_food(bls_row(**self.KALE), "v", CURATION)

    def test_a_gap_of_two_kcal_passes_whatever_its_share(self):
        # 0.4 kcal of Atwater against a label of 2: the gap is 1.6 kcal, 80 % of the label.
        near_zero = amounts(energy_kcal=2, protein=0.1, carbohydrates=0, fat=0)
        with self.assertRaises(Reject):
            validate("Kaffee", near_zero, False)
        self.assertEqual(validate("Kaffee", near_zero, False, bls=True), ())
        # Exactly 2 kcal passes, a hair more does not.
        self.assertEqual(validate("Tee", amounts(energy_kcal=5, protein=0, carbohydrates=0.75, fat=0), False, bls=True), ())
        with self.assertRaises(Reject):
            validate("Tee", amounts(energy_kcal=5, protein=0, carbohydrates=0.74, fat=0), False, bls=True)
        # The floor is for BLS only.
        with self.assertRaises(Reject):
            validate("Tee", amounts(energy_kcal=5, protein=0, carbohydrates=0.75, fat=0), False)

    def test_what_the_rule_cannot_explain_stays_rejected(self):
        for name, row, reason in (
            # Organic acids give 3 kcal/g and the extract has no column for them: carbohydrate 0, label 25.
            ("Zitronensäure", bls_row(energy_kcal=25, protein=0, carbohydrates=0, fat=0), "atwater_deviation"),
            # Polyols are in CHO but give 2.4 kcal/g: 4 kcal/g in the sum, 240 kcal on the label.
            ("Sorbit", bls_row(energy_kcal=240, protein=0, carbohydrates=100, fat=0), "atwater_deviation"),
            # Fibre is no excuse for a gap it does not explain.
            ("Gemüse", bls_row(energy_kcal=200, protein=1, carbohydrates=5, fat=0, fiber=8), "atwater_deviation"),
            # No fat value is no fat of zero.
            ("Glutamat", {**bls_row(energy_kcal=0, protein=0, carbohydrates=0), "fat": ""}, "missing_or_invalid_macro"),
        ):
            with self.subTest(name=name), self.assertRaises(Reject) as error:
                bls_row_food({**row, "name_de": name}, "v", CURATION)
            self.assertEqual(str(error.exception), reason)

    def test_the_existing_exceptions_are_decided_as_before(self):
        wine = amounts(energy_kcal=85, protein=0.1, carbohydrates=2.6, fat=0, alcohol=9.5)
        self.assertEqual(validate("Wein", wine, True, bls=True), ("atwater_alcohol_exception",))
        self.assertEqual(validate("Wein", wine, True), ("atwater_alcohol_exception",))
        bran = amounts(energy_kcal=200, protein=10, carbohydrates=10, fat=0, fiber=20)
        self.assertEqual(validate("Kleie", bran, False, bls=True), ("atwater_high_fiber_exception",))

    def test_the_committed_extract_after_bls2(self):
        counts: Counter = Counter()
        reasons: Counter = Counter()
        left: dict[str, str] = {}
        both: Counter = Counter()  # old rule result vs the new one, over every non-twin row
        with BLS_CSV_PATH.open(encoding="utf-8", newline="") as stream:
            rows = list(csv.DictReader(stream))
        for row in rows:
            code = row["bls_code"]
            try:
                bls_row_food(row, "v", CURATION)
            except Skip:
                counts["skipped"] += 1
                continue
            except Reject as error:
                counts["rejected"] += 1
                reasons[str(error)] += 1
                left[code] = row["name_de"]
            else:
                counts["accepted"] += 1
            nutrients = bls_amounts(row)
            alcohol = nutrients.get("alcohol")
            args = (row["name_de"], nutrients, alcohol is not None and alcohol.amount > 0)
            try:
                before = validate(*args)
            except Reject:
                before = None
            try:
                after = validate(*args, bls=True)
            except Reject:
                after = None
            both[("accepted" if before is not None else "rejected", "accepted" if after is not None else "rejected")] += 1
            if before is not None:
                self.assertEqual(after, before, f"{code}: a row that passed before is decided differently now")
        self.assertEqual(len(rows), 7140)
        self.assertEqual(dict(counts), {"accepted": 7105, "rejected": 15, "skipped": 20})
        self.assertEqual(dict(reasons), {"atwater_deviation": 12, "missing_or_invalid_macro": 3})
        # Before bls2: 7,026 accepted and 94 rejected (of the 7,120 that are no twin). No accepted row is lost.
        self.assertEqual(both[("accepted", "rejected")], 0)
        self.assertEqual(both[("rejected", "accepted")], 79)
        self.assertEqual(both[("accepted", "accepted")], 7026)
        self.assertEqual(both[("rejected", "rejected")], 15)
        # What stays rejected, by category.
        self.assertEqual(sorted(left), sorted([
            "F081600", "F601300", "F602300", "R551000", "R552000", "R554000",  # organic acids (3 kcal/g)
            "S162000", "S163000", "S164000", "S170000", "S341000", "S342000",  # polyols and sweeteners
            "R172000", "R411000", "R421300"]))  # no fat value

    def test_recovery_by_cause(self):
        """The recovered rows by how the rule decides them. The rule adds the fibre term first and then accepts a gap
        of at most 30 % of the energy or at most 2 kcal, so the floor is checked with the fibre term too. A row is
        either inside the 30 % band (fibre-30 % alone) or needs the floor, never neither."""
        band_only = needs_floor = both = 0
        with BLS_CSV_PATH.open(encoding="utf-8", newline="") as stream:
            for row in csv.DictReader(stream):
                try:
                    bls_row_food(row, "v", CURATION)
                except (Skip, Reject):
                    continue
                nutrients = bls_amounts(row)
                alcohol = nutrients.get("alcohol")
                args = (row["name_de"], nutrients, alcohol is not None and alcohol.amount > 0)
                try:
                    validate(*args)
                    continue
                except Reject:
                    pass
                energy = nutrients["energy_kcal"].amount
                atwater = sum(nutrients[c].amount * f for c, f in (("protein", 4), ("carbohydrates", 4), ("fat", 9)))
                fibre = nutrients["fiber"].amount if "fiber" in nutrients else 0
                gap = abs(atwater + transform.BLS_FIBER_KCAL_PER_G * fibre - energy)  # the gap the rule judges
                in_band = gap <= energy * transform.ATWATER_FRACTION
                in_floor = gap <= transform.BLS_KCAL_ROUNDING
                self.assertTrue(in_band or in_floor, row["bls_code"])
                self.assertTrue(transform.bls_energy_matches(nutrients, atwater, energy), row["bls_code"])
                band_only += in_band and not in_floor
                needs_floor += in_floor and not in_band
                both += in_band and in_floor
        # 79 rows: 72 are inside the 30 % band once the fibre term is added (20 of them only there, 52 also within
        # 2 kcal) and 7 pass only by the 2 kcal floor, with the fibre term too (coffee, tea, near-zero drinks).
        self.assertEqual((band_only, needs_floor, both), (20, 7, 52))


class PresetPortionTests(unittest.TestCase):
    @staticmethod
    def food(fdc_id, description, rows=None):
        return usda_food({"fdc_id": fdc_id, "description": description, "nutrients": FDC_NUTRIENTS, "portions": rows}, "v")

    def test_the_staples_version_is_3(self):
        self.assertEqual(STAPLES["version"], 3)

    def test_half_a_cucumber_and_a_pot_of_yogurt_are_the_default(self):
        for fdc_id, description, expected in (
            ("168409", "Cucumber, with peel, raw",
             ("half_200_g", 200, "½ cucumber (200 g)", "½ Salatgurke (200 g)", "½ cucumber", "½ Gurke")),
            ("171284", "Yogurt, plain, whole milk",
             ("pot_150_g", 150, "1 pot (150 g)", "1 Becher (150 g)", "Pot", "Becher")),
        ):
            key, grams, en, de, short_en, short_de = expected
            for rows in (None, [sr_row(1, "cup", 245.0)]):  # with and without a source row
                with self.subTest(fdc_id=fdc_id, rows=bool(rows)):
                    food = self.food(fdc_id, description, rows)
                    [preset] = [p for p in food.portions if p.portion_key]
                    self.assertEqual((preset.portion_key, preset.weight_g, preset.is_default, preset.source_portion_id,
                                      preset.volume_ml, preset.sort_order), (key, grams, True, None, None, 1))
                    self.assertEqual((preset.label, preset.labels, preset.short_label_en, preset.short_label_de),
                                     (en, {"de": de, "en": en}, short_en, short_de))
                    self.assertEqual([p.is_default for p in food.portions].count(True), 1)
                    self.assertEqual(food.portions[0], preset)  # order 1 sorts before the source's rows
                    self.assertEqual(len(food.portions), 2 if rows else 1)

    def test_a_renamed_release_keeps_the_staple_off(self):
        food = self.food("168409", "Cucumber, peeled, raw")
        self.assertFalse(food.is_canonical_staple)
        self.assertEqual(food.portions, ())

    def test_the_default_sorts_before_every_size_row(self):
        """The USDA sizes sort at 5 (extra small) to 38 (jumbo), so the preset must come first, not tie with one."""
        rows = [sr_row(1, "extra small", 60.0), sr_row(2, "small", 80.0), sr_row(3, "medium", 120.0),
                sr_row(4, "cup", 245.0)]
        for fdc_id, description in (("168409", "Cucumber, with peel, raw"), ("171284", "Yogurt, plain, whole milk")):
            with self.subTest(fdc_id=fdc_id):
                portions = self.food(fdc_id, description, rows).portions
                self.assertEqual(len(portions), 5)
                self.assertTrue(portions[0].is_default)
                self.assertIsNotNone(portions[0].portion_key)
                self.assertLess(portions[0].sort_order, min(p.sort_order for p in portions[1:]))
                self.assertEqual([p.sort_order for p in portions], sorted(p.sort_order for p in portions))

    @staticmethod
    def preset(value, **extra):
        return {"preset_grams": value, "order": 10, "labels": {"en": "x", "de": "x"},
                "short_labels": {"en": "x", "de": "x"}, **extra}

    def test_a_preset_with_unusable_grams_is_reported_not_applied(self):
        for value in (0, -5, -0.5, "200", "", None, True, False, float("nan"), float("inf"), [200], {"g": 200}):
            with self.subTest(value=value):
                tally: dict = {}
                self.assertEqual(usda_measures(None, {"bad": self.preset(value)}, tally), ())
                self.assertEqual(tally["unmatched"], ["bad"])
        for value, grams in ((200, 200), (200.0, 200), (0.5, 0.5)):
            with self.subTest(value=value):
                tally = {}
                [portion] = usda_measures(None, {"ok": self.preset(value)}, tally)
                self.assertEqual((portion.weight_g, tally["unmatched"]), (grams, []))

    def test_an_entry_with_two_sources_is_invalid_and_applies_none(self):
        rows = [sr_row(1, "medium", 120.0, source_id="880001")]
        for extra in ({"half_of": "base"}, {"match": {"key": "medium"}}, {"half_of": "base", "match": {"key": "medium"}}):
            with self.subTest(extra=extra):
                tally: dict = {}
                overrides = {"base": {"match": {"key": "medium"}, "order": 20, "labels": {"en": "m", "de": "m"},
                                      "short_labels": {"en": "m", "de": "m"}},
                             "both": self.preset(200, **extra)}
                portions = usda_measures(rows, overrides, tally)
                self.assertIn("both", tally["unmatched"])
                self.assertEqual([p.portion_key for p in portions if p.portion_key], ["base"])
                self.assertNotIn(200, [p.weight_g for p in portions])

    def test_a_half_of_and_match_entry_is_invalid_even_without_a_preset(self):
        entry = {"half_of": "base", "match": {"key": "medium"}, "order": 20,
                 "labels": {"en": "x", "de": "x"}, "short_labels": {"en": "x", "de": "x"}}
        tally: dict = {}
        portions = usda_measures([sr_row(1, "medium", 120.0)], {"both": entry}, tally)
        self.assertEqual(tally["unmatched"], ["both"])
        self.assertEqual([p.portion_key for p in portions], [None])

    def test_no_other_curated_portion_changed(self):
        """The portions of every other staple are byte for byte what they were before D313 (their IDs follow)."""
        others = {"bls": {code: entry["portions"] for code, entry in STAPLES["bls"].items()},
                  "usda": {fdc: entry["portions"] for fdc, entry in STAPLES["usda"].items()
                           if fdc not in ("168409", "171284")}}
        digest = hashlib.sha256(json.dumps(others, sort_keys=True, ensure_ascii=False).encode()).hexdigest()
        self.assertEqual(digest, "8ff2264420a57c8d57f098d7c29bd7f848369ed7138641f4c230b43702beb17e")


class PortionHeadTests(unittest.TestCase):
    # text, amount, English label, German label
    HEADS = (
        ("item", "1", "1 item", "1 Stück"), ("fruit", "1", "1 fruit", "1 Frucht"),
        ("wedge", "1", "1 wedge", "1 Spalte"), ("wedge", "2", "2 wedges", "2 Spalten"),
        ("stalk, medium", "1", "1 stalk, medium", "1 Stange, mittel"), ("spear", "4", "4 spears", "4 Stangen"),
        ("stick", "1", "1 stick", "1 Stange"), ("sticks", "2", "2 sticks", "2 Stangen"), ("leaf", "1", "1 leaf", "1 Blatt"), ("leaves", "3", "3 leaves", "3 Blätter"),
        ("clove", "1", "1 clove", "1 Zehe"), ("cloves", "2", "2 cloves", "2 Zehen"),
        ("head, large", "1", "1 head, large", "1 Kopf, groß"), ("bunch", "1", "1 bunch", "1 Bund"),
        ("bunches", "2", "2 bunches", "2 Bund"), ("sprig", "1", "1 sprig", "1 Zweig"), ("sprigs", "5", "5 sprigs", "5 Zweige"),
        ("pod", "1", "1 pod", "1 Schote"), ("strip", "2", "2 strips", "2 Streifen"), ("ring", "3", "3 rings", "3 Ringe"),
        ("pat", "1", "1 pat", "1 Stück"), ("steak", "1", "1 steak", "1 Steak"), ("chop", "2", "2 chops", "2 Koteletts"),
        ("pinch", "1", "1 pinch", "1 Prise"), ("drumstick", "1", "1 drumstick", "1 Unterkeule"),
        ("thigh, bone and skin removed", "1", "1 thigh, bone and skin removed",
         "1 Oberkeule, ohne Haut und Knochen"), ("thighs", "2", "2 thighs", "2 Oberkeulen"),
        ("wing", "2", "2 wings", "2 Flügel"), ("leg, with bone", "1", "1 leg, with bone", "1 Keule, mit Knochen"),
        ("roll", "1", "1 roll", "1 Brötchen"), ("rolls", "2", "2 rolls", "2 Brötchen"), ("bun", "1", "1 bun", "1 Brötchen"),
        ("bagel", "1", "1 bagel", "1 Bagel"), ("muffin", "1", "1 muffin", "1 Muffin"),
        ("cookie", "2", "2 cookies", "2 Kekse"), ("cracker", "3", "3 crackers", "3 Cracker"),
        ("tortilla", "1", "1 tortilla", "1 Tortilla"), ("loaf", "1", "1 loaf", "1 Laib"),
        ("sandwich", "1", "1 sandwich", "1 Sandwich"), ("package", "1", "1 package", "1 Packung"),
        ("bag", "1", "1 bag", "1 Beutel"), ("jar", "1", "1 jar", "1 Glas"), ("glass", "2", "2 glasses", "2 Gläser"),
        ("envelope", "1", "1 envelope", "1 Päckchen"), ("tablet", "2", "2 tablets", "2 Tabletten"),
        ("frank", "1", "1 frank", "1 Würstchen"), ("frankfurter", "2", "2 frankfurters", "2 Würstchen"),
        ("sausage", "2", "2 sausages", "2 Würste"), ("order", "1", "1 order", "1 Portion"),
        ("fruit, peeled", "1", "1 fruit, peeled", "1 Frucht, geschält"),
        ("cup, frozen", "1", "1 US cup, frozen", "1 US-Tasse, tiefgekühlt"),
    )

    def test_new_heads_get_german_labels(self):
        for text, amount, en, de in self.HEADS:
            with self.subTest(text=text, amount=amount):
                label = clean_portion(text, amount, 50.0)
                self.assertEqual((label.en, label.de, label.short_en, label.short_de, label.unmapped), (en, de, en, de, False))
                self.assertLess(label.order, 100)  # mapped heads sort before the unknown ones

    def test_a_cubic_inch_reads_in_grams_like_an_ounce(self):
        label = clean_portion("cubic inch", "1", 17.0)
        self.assertEqual((label.en, label.de, label.short_en, label.short_de), ("17 g",) * 4)

    def test_no_word_belongs_to_two_heads(self):
        words = [word for head in (*transform.SIZES, *transform.UNITS) for word in head.words]
        self.assertEqual([word for word, count in Counter(words).items() if count > 1], [])

    def test_unknown_heads_stay_english_only(self):
        label = clean_portion("1 dollop", None, 20.0)
        self.assertEqual((label.en, label.de, label.unmapped), ("1 dollop", None, True))

    def test_head_word(self):
        for rest, word in (("dollop", "dollop"), ("Dollop, large", "dollop"), ("half-moon", "half-moon"),
                           ("2% milkfat", "2%"), ("(see note)", "(see")):
            with self.subTest(rest=rest):
                self.assertEqual(head_word(rest), word)

    def test_the_summary_lists_the_top_25_remaining_heads(self):
        notes = ImportNotes()
        # 26 unknown heads ("a" .. "z"): b five times, z once, the rest twice.
        for index, head in enumerate("abcdefghijklmnopqrstuvwxyz"):
            count = 5 if head == "b" else 1 if head == "z" else 2
            tally: dict = {}
            portions = usda_measures([sr_row(i, head, 10.0 + i) for i in range(1, count + 1)], None, tally)
            notes.record(str(1000 + index), portions, tally, False)
        summary = notes.summary()
        top = summary["unmapped_heads"]
        self.assertEqual(len(top), 25)
        self.assertEqual(top[0], {"head": "b", "count": 5})
        self.assertEqual([entry["head"] for entry in top[1:]], [c for c in "acdefghijklmnopqrstuvwxy"])
        self.assertTrue(all(entry["count"] == 2 for entry in top[1:]))
        self.assertNotIn("z", [entry["head"] for entry in top])
        self.assertEqual(summary["portions"]["unmapped_head"], 24 * 2 + 5 + 1)
        self.assertEqual(json.loads(json.dumps(summary))["unmapped_heads"], top)

    def test_the_summary_through_a_usda_food(self):
        notes = ImportNotes()
        rows = [sr_row(1, "dollop", 20.0), sr_row(2, "wedge", 30.0), sr_row(3, "dollop, large", 40.0)]
        usda_food({"fdc_id": "999001", "description": "Cheddar cheese", "nutrients": FDC_NUTRIENTS, "portions": rows},
                  "v", notes)
        self.assertEqual(notes.summary()["unmapped_heads"], [{"head": "dollop", "count": 2}])
        self.assertEqual(ImportNotes().summary()["unmapped_heads"], [])


if __name__ == "__main__":
    unittest.main()
