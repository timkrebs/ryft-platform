import io
import json
import os
import subprocess
import sys
import unittest
import uuid
import zipfile
from dataclasses import replace
from decimal import Decimal
from pathlib import Path
from tempfile import TemporaryDirectory

import duckdb
import httpx
import psycopg

from categorizer import classify, normalize
from download import MissingUSDATables, Release, extract_usda, resolve_off, resolve_usda, stage_usda
from extract import connection, off_rows, usda_rows
from generate_embeddings import DIMENSIONS, pending_batch, vectors
from import_pg import IMPORT_NAMESPACE, load_batch, source_portion_uuid, staging_row
from transform import (QUALIFIERS, SIZES, STAPLES, UNITS, USDA_NUTRIENTS, ImportNotes, Nutrient, Portion, Reject,
                       bls_food, clean_portion, format_amount, off_food, usda_food, usda_measures, validate)

FDC_NUTRIENTS = [{"id": key, "amount": amount} for key, amount in (("1008", 48), ("1003", 0), ("1004", 0), ("1005", 12))]


def sr_row(position, modifier, grams, amount="1", source_id=None):
    """A food_portion row shaped like SR Legacy: measure `undetermined`, the size or unit in the modifier."""
    return {"source_id": source_id, "description": None, "amount": amount, "measure": "undetermined",
            "modifier": modifier, "grams": grams, "priority": position}


def pre_g1(rows):
    """The portions the importer wrote before G1: the raw text as label, nothing localized."""
    return tuple(Portion(" ".join(part for part in (row["amount"], row["modifier"]) if part), "portion",
                         weight_g=row["grams"], source_portion_id=row["source_id"], sort_order=100 + row["priority"],
                         legacy_label=" ".join(part for part in (row["amount"], row["measure"], row["modifier"]) if part))
                 for row in rows)


PLUM_ROWS = [
    sr_row(1, 'medium (3" dia)', 182.0, source_id="880001"),
    sr_row(2, "cup, chopped", 125.0, source_id="880002"),
    sr_row(3, "slice (1 oz)", 28.0),
    sr_row(4, "bag", 50.0),
]

# modifier, amount, grams, en, de, short_en, short_de, sort_order (None: 100 + the row's position).
# Every row has its own grams and no two rows share a label, so the collision rule stays out of this table.
SR_TABLE = (
    ('small (2-3/4" dia)', "1", 149.0, "1 small", "1 kleines Stück", "Small", "Klein", 10),
    ('medium (3" dia)', "1", 182.0, "1 medium", "1 mittelgroßes Stück", "Medium", "Mittel", 20),
    ('large (3-1/4" dia)', "1", 223.0, "1 large", "1 großes Stück", "Large", "Groß", 30),
    ('extra small (2-1/4" dia)', "1", 101.0, "1 extra small", "1 sehr kleines Stück", "Extra small", "Sehr klein", 5),
    ('extra large (8" to 8-7/8" long)', "1", 250.0, "1 extra large", "1 sehr großes Stück", "Extra large", "Sehr groß", 35),
    ('jumbo (9" or longer)', "1", 300.0, "1 jumbo", "1 Stück (Jumbo)", "Jumbo", "Jumbo", 38),
    ('medium (3" dia)', "2", 364.0, "2 medium", "2 Stück, mittel", "2 × Medium", "2 × Mittel", 20),
    ("cup, chopped", "1", 125.0, "1 US cup, chopped", "1 US-Tasse, gehackt", "1 US cup, chopped", "1 US-Tasse, gehackt", 80),
    ("cup (8 fl oz)", "1", 240.0, "1 US cup", "1 US-Tasse", "1 US cup", "1 US-Tasse", 80),
    ("cup, sliced", "1", 110.0, "1 US cup, sliced", "1 US-Tasse, in Scheiben", "1 US cup, sliced", "1 US-Tasse, in Scheiben", 80),
    ("cup, mashed", "0.5", 105.0, "½ US cup, mashed", "½ US-Tasse, zerdrückt", "½ US cup, mashed", "½ US-Tasse, zerdrückt", 80),
    ("cup, diced", "1.5", 150.0, "1½ US cups, diced", "1½ US-Tassen, gewürfelt", "1½ US cups, diced", "1½ US-Tassen, gewürfelt", 80),
    ("cup, grated", "0.25", 60.0, "¼ US cup, grated", "¼ US-Tasse, gerieben", "¼ US cup, grated", "¼ US-Tasse, gerieben", 80),
    ("cup, unpacked", "1", 95.0, "1 US cup, unpacked", None, "1 US cup, unpacked", None, 80),
    ("slice (1 oz)", "1", 28.0, "1 slice", "1 Scheibe", "1 slice", "1 Scheibe", 42),
    ("slice, thin", "2", 40.0, "2 slices, thin", "2 Scheiben, dünn", "2 slices, thin", "2 Scheiben, dünn", 42),
    ("slice, medium", "1", 32.0, "1 slice, medium", "1 Scheibe, mittel", "1 slice, medium", "1 Scheibe, mittel", 42),
    ("tbsp", "1", 15.0, "1 tbsp", "1 EL", "1 tbsp", "1 EL", 70),
    ("tsp", "1", 5.0, "1 tsp", "1 TL", "1 tsp", "1 TL", 72),
    ("tablespoons", "2", 30.0, "2 tbsp", "2 EL", "2 tbsp", "2 EL", 70),
    ("fillet", "1", 140.0, "1 fillet", "1 Filet", "1 fillet", "1 Filet", 44),
    ("breast, bone and skin removed", "0.5", 86.0, "½ breast, bone and skin removed",
     "½ Bruststück, ohne Haut und Knochen", "½ breast, bone and skin removed", "½ Bruststück, ohne Haut und Knochen", 46),
    ('breast (approx 2-3/4" long, 2-1/2" dia)', "1", 120.0, "1 breast", "1 Bruststück", "1 breast", "1 Bruststück", 46),
    ("link", "1", 50.0, "1 link", "1 Würstchen", "1 link", "1 Würstchen", 48),
    ("patty", "2", 100.0, "2 patties", "2 Pattys", "2 patties", "2 Pattys", 50),
    ("scoop", "1", 70.0, "1 scoop", "1 Kugel", "1 scoop", "1 Kugel", 54),
    ("bar (1.5 oz)", "1", 43.0, "1 bar", "1 Riegel", "1 bar", "1 Riegel", 52),
    ("can (12 fl oz)", "1", 355.0, "1 can", "1 Dose", "1 can", "1 Dose", 56),
    ("bottle (20 fl oz)", "1", 590.0, "1 bottle", "1 Flasche", "1 bottle", "1 Flasche", 58),
    ("container (6 oz)", "1", 170.0, "1 container", "1 Becher", "1 container", "1 Becher", 60),
    ("packet", "1", 6.0, "1 packet", "1 Päckchen", "1 packet", "1 Päckchen", 62),
    ("serving", "1", 160.0, "1 serving", "1 Portion", "1 serving", "1 Portion", 64),
    ("NLEA serving", "1", 85.0, "US label serving (85 g)", "US-Referenzportion (85 g)", "85 g", "85 g", 88),
    ("oz", "1", 28.35, "28.35 g", "28.35 g", "28.35 g", "28.35 g", 90),
    ("lb", "1", 454.0, "454 g", "454 g", "454 g", "454 g", 92),
    ("fl oz", "1", 31.0, "30 ml", "30 ml", "30 ml", "30 ml", 85),
    ("fl oz", "2", 61.0, "59 ml", "59 ml", "59 ml", "59 ml", 85),
    ("piece", "1", 25.0, "1 piece", "1 Stück", "1 piece", "1 Stück", 41),
    ("wedge", "1", 20.0, "1 wedge", "1 Spalte", "1 wedge", "1 Spalte", 45),
    ('stalk, medium (7-1/2" to 8" long)', "1", 41.0, "1 stalk, medium", "1 Stange, mittel", "1 stalk, medium",
     "1 Stange, mittel", 47),
    ("loaf (1 lb)", "1", 450.0, "1 loaf", "1 Laib", "1 loaf", "1 Laib", 86),
    # Heads no table knows (D313: still counted by the dry run, still English only).
    ("dollop", "1", 17.5, "1 dollop", None, None, None, None),
    ("handful", "1", 33.5, "1 handful", None, None, None, None),
    ("squirt", "1", 7.5, "1 squirt", None, None, None, None),
    ("undetermined", "1", 77.0, "77 g", "77 g", "77 g", "77 g", None),
)


def sr_table_rows():
    return [sr_row(position, modifier, grams, amount)
            for position, (modifier, amount, grams, *_) in enumerate(SR_TABLE, 1)]


def write_fixture(directory: Path) -> tuple[Path, Path]:
    parquet, archive = directory / "off.parquet", directory / "fdc.zip"
    with duckdb.connect() as db:
        db.execute("""
            CREATE TABLE products AS SELECT code, [country] AS countries_tags,
                ['en:non-alcoholic-foods'] AS categories_tags,
                [{'lang':'en', 'text':'Oats'}, {'lang':'de', 'text':'Haferflocken'}] AS product_name,
                [{'name':'energy-kcal', '100g':170.0}, {'name':'proteins','100g':10.0},
                 {'name':'carbohydrates','100g':10.0}, {'name':'fat','100g':10.0},
                 {'name':'salt','100g':1.0}, {'name':'omega-3-fat','100g':0.25},
                 {'name':'fiber','100g':0.0}] AS nutriments, '100g' AS nutrition_data_per, obsolete
            FROM (VALUES ('012345678905','en:germany',false), ('012345678904','en:austria',false),
                         ('4006381333931','en:european-union',false), ('4006381333931','en:france',false),
                         ('4006381333931','en:switzerland',true), ('123','en:germany',false)) t(code,country,obsolete)
        """)
        db.execute("COPY products TO ? (FORMAT PARQUET)", [str(parquet)])
    with zipfile.ZipFile(archive, "w") as out:
        out.writestr("release/food.csv", "fdc_id,description,food_category_id,publication_date\n12345,\"Apples, raw\",9,2026-01-01\n")
        out.writestr("release/food_category.csv", "id,description\n9,Fruits and Fruit Juices\n")
        out.writestr("release/food_nutrient.csv", "fdc_id,nutrient_id,amount\n12345,1008,48\n12345,1003,0\n12345,1004,0\n12345,1005,12\n12345,1093,3\n")
        out.writestr("release/food_portion.csv", "fdc_id,seq_num,amount,measure_unit_id,portion_description,gram_weight\n12345,1,1,1,,110\n12345,2,1,2,1 medium,182\n12345,3,1,1,Invalid,0\n")
        out.writestr("release/measure_unit.csv", "id,name\n1,cup\n")
        out.writestr("release/nutrient.csv", "id,unit_name\n1008,KCAL\n1003,G\n1004,G\n1005,G\n1093,MG\n")
    return parquet, archive


class PipelineTests(unittest.TestCase):
    def setUp(self):
        workspace = self.enterContext(TemporaryDirectory(dir="/tmp"))
        self.directory = Path(workspace)
        self.parquet, self.archive = write_fixture(self.directory)

    def test_off_projection_market_barcode_and_units(self):
        with connection(self.directory, 128) as db:
            rows = [row for batch in off_rows(db, str(self.parquet), None, 1) for row in batch]
        self.assertEqual({row["code"] for row in rows}, {"012345678905", "012345678904", "4006381333931"})
        row = next(row for row in rows if row["code"] == "012345678905")
        food = off_food(row, "v1")
        self.assertEqual((food.name, food.gtin, food.omega3_mg), ("Haferflocken", "00012345678905", 250))
        values = {n.code: n.amount for n in food.nutrients}
        self.assertEqual((values["sodium"], values["fiber"], values["omega_3"]), (400, 0, 0.25))
        self.assertNotIn("trans_fat", values)
        self.assertEqual(off_food({**row, "serving_size": "1 pack (30,5 g)"}, "v1").portions,
                         (Portion("Serving", "serving", weight_g=30.5),))
        for ambiguous in ("1 TL", "1 pack", "30 ml", "30–40 g", "0 g"):
            with self.subTest(serving=ambiguous):
                self.assertEqual(off_food({**row, "serving_size": ambiguous, "product_quantity_unit": "g"}, "v1").portions, ())
        with self.assertRaisesRegex(Reject, "unverified_100g_basis"):
            off_food({**row, "product_quantity_unit": "ml"}, "v1")
        with self.assertRaisesRegex(Reject, "invalid_barcode_checksum"):
            off_food(next(r for r in rows if r["code"] == "012345678904"), "v1")

    def test_usda_join_and_reference_units(self):
        files = extract_usda(self.archive, self.directory)
        with connection(self.directory, 128) as db:
            row = next(usda_rows(db, files, 10, 1, data_type="foundation_food"))[0]
        self.assertEqual(row["category"], "Fruits and Fruit Juices")
        food = usda_food(row, "v1")
        self.assertEqual(food.category.name, "whole_fruit")
        self.assertEqual(food.category.profile["whole_fruit_fraction"], 1)
        values = {n.code: n.amount for n in food.nutrients}
        self.assertEqual((values["carbohydrates_total"], values["sodium"]), (12, 3))
        # G1 orders a size before a household unit (20 before 80) and calls the cup a US cup.
        self.assertEqual([(p.label, p.weight_g) for p in food.portions], [("1 medium", 182), ("1 US cup", 110)])
        self.assertEqual(food.portions[1].short_label_de, "1 US-Tasse")
        self.assertEqual(food.portions[1].labels, {"en": "1 US cup", "de": "1 US-Tasse"})
        self.assertEqual(food.portions[1].legacy_label, "1 cup")

    def test_staple_apple_keeps_source_weights_and_prioritizes_bounded_import(self):
        files = extract_usda(self.archive, self.directory)
        files["food.csv"].write_text(
            'fdc_id,data_type,description,food_category_id,publication_date\n'
            '12345,sr_legacy_food,"Apples, raw",9,2026-01-01\n'
            '171688,sr_legacy_food,"Apples, raw, with skin",9,2019-04-01\n',
            encoding="utf-8",
        )
        nutrients = files["food_nutrient.csv"].read_text(encoding="utf-8")
        files["food_nutrient.csv"].write_text(nutrients + "\n".join(
            line.replace("12345", "171688") for line in nutrients.splitlines()[1:]
        ) + "\n", encoding="utf-8")
        files["food_portion.csv"].write_text(
            'id,fdc_id,seq_num,amount,measure_unit_id,modifier,gram_weight\n'
            '89192,171688,1,1,9999,large (3-1/4" dia),223\n'
            '89193,171688,2,1,9999,medium (3" dia),182\n'
            '89194,171688,3,1,9999,small (2-3/4" dia),149\n',
            encoding="utf-8",
        )
        files["measure_unit.csv"].write_text("id,name\n9999,undetermined\n", encoding="utf-8")
        with connection(self.directory, 128) as db:
            [row] = [food for batch in usda_rows(db, files, 1, 1, data_type="sr_legacy_food") for food in batch]
        self.assertEqual(row["fdc_id"], "171688")
        food = usda_food(row, "fixture-v1")
        self.assertEqual(food.names, [("de", "Apfel"), ("en", "Apple")])
        self.assertIn(("de", "Äpfel"), food.aliases)
        self.assertEqual((food.canonical_group_key, food.food_form, food.is_canonical_staple), ("apple", "whole_raw", True))
        self.assertEqual([(p.source_portion_id, p.portion_key, p.weight_g, p.is_default) for p in food.portions],
                         [("89194", "small", 149, False), ("89193", "medium", 182, True), ("89192", "large", 223, False)])
        self.assertEqual(food.portions[1].labels["de"], "1 mittelgroßer Apfel")
        self.assertNotIn("undetermined", food.portions[1].label)
        self.assertIn("undetermined", food.portions[1].legacy_label)
        # Klein 149 / Mittel 182 (default) / Groß 223, clean in every field and every language.
        self.assertEqual([(p.short_label_de, p.weight_g, p.is_default) for p in food.portions],
                         [("Klein", 149, False), ("Mittel", 182, True), ("Groß", 223, False)])
        self.assertEqual([p.label for p in food.portions], ["1 small apple", "1 medium apple", "1 large apple"])
        for portion in food.portions:
            with self.subTest(portion=portion.label):
                for text in (portion.label, *portion.labels.values(), portion.short_label_en, portion.short_label_de):
                    self.assertNotIn("undetermined", text)
                    self.assertNotIn('"', text)
                self.assertEqual(portion.legacy_label.count("undetermined"), 1)  # The stored portion ID still hashes it.
        [revised] = usda_measures([{**row["portions"][1], "grams": 183}], STAPLES["usda"]["171688"]["portions"])
        self.assertEqual((revised.weight_g, revised.portion_key, revised.is_default), (183, None, False))

    def test_bls_keeps_available_carbs_and_distinct_measured_reference_units(self):
        for source_id, kcal, glass_mass in (("M111300", 62, 206.0), ("M111200", 44, 206.8)):
            with self.subTest(source_id=source_id):
                food = bls_food({"external_id": source_id}, "bls_4_0")
                values = {nutrient.code: nutrient.amount for nutrient in food.nutrients}
                self.assertEqual((food.provider, food.reference_unit, values["energy_kcal"]), ("bls", "g", kcal))
                self.assertIn("Max Rubner-Institut", food.attribution)
                self.assertIn("carbohydrates", values)
                self.assertNotIn("added_sugar", values)
                [glass] = [portion for portion in food.portions if portion.is_default]
                self.assertEqual((glass.volume_ml, glass.weight_g), (200, glass_mass))
        quark = bls_food({"external_id": "M713100"}, "bls_4_0")
        self.assertEqual([portion.weight_g for portion in quark.portions], [25, 150, 250])
        self.assertTrue(all(portion.volume_ml is None for portion in quark.portions))
        self.assertIn(("de", "Magertopfen"), quark.aliases)

    def test_plain_apple_juice_is_admitted_without_whole_fruit_status(self):
        food = usda_food({
            "fdc_id": "173933",
            "description": "Apple juice, canned or bottled, unsweetened, without added ascorbic acid",
            "category": "Fruits and Fruit Juices",
            "nutrients": [{"id": key, "amount": amount} for key, amount in (
                ("1008", 46), ("1003", 0.1), ("1004", 0.13), ("1005", 11.3),
            )],
            "portions": [{"source_id": "93494", "modifier": "cup", "amount": "1",
                          "measure": "undetermined", "grams": 248}],
        }, "fixture-v1")
        self.assertEqual(food.names, [("de", "Apfelsaft"), ("en", "Apple juice")])
        self.assertEqual((food.is_canonical_staple, food.canonical_group_key, food.food_form),
                         (False, "apple", "juice"))
        self.assertEqual(food.category.profile["whole_fruit_fraction"], 0)
        [cup] = food.portions
        self.assertEqual((cup.source_portion_id, cup.weight_g, cup.is_default), ("93494", 248, True))

    def test_sr_legacy_sized_release_fits_the_smallest_memory_limit(self):
        """SR Legacy has ~7.8k foods with ~30 tracked nutrients each. An ordered list() aggregate keeps a
        non-spillable buffer per group past 16 rows, which ran out of the 768 MB limit at --limit 10000."""
        files = extract_usda(self.archive, self.directory)
        foods, ids = 8_000, list(USDA_NUTRIENTS)[:30]
        files["food.csv"].write_text("fdc_id,data_type,description,food_category_id,publication_date\n" + "".join(
            f"{100_000 + i},sr_legacy_food,Food {i},9,2019-04-01\n" for i in range(foods)), encoding="utf-8")
        files["nutrient.csv"].write_text("id,unit_name\n" + "".join(
            f"{nutrient},{USDA_NUTRIENTS[nutrient][1].upper()}\n" for nutrient in ids), encoding="utf-8")
        files["food_nutrient.csv"].write_text("fdc_id,nutrient_id,amount\n" + "".join(
            f"{100_000 + i},{nutrient},{(i + j) % 7}\n" for i in range(foods) for j, nutrient in enumerate(ids))
            + "100001,1003,\n100001,1003,9\n", encoding="utf-8")
        files["food_portion.csv"].write_text("id,fdc_id,seq_num,amount,measure_unit_id,portion_description,gram_weight\n"
            + "".join(f"{1 + i * 3 + k},{100_000 + i},{k + 1},1,1,{k + 1} piece,{10 * (k + 1)}\n"
                      for i in range(foods) for k in range(3))
            + "".join(f"{900_000 + k},100002,{20 - k},1,1,size {k},{k + 1}\n" for k in range(20)), encoding="utf-8")
        with connection(self.directory, 128) as db:
            rows = {row["fdc_id"]: row for batch in usda_rows(db, files, 10_000, 2_000, data_type="sr_legacy_food")
                    for row in batch}
        self.assertEqual(len(rows), foods)
        self.assertTrue(all(len(row["nutrients"]) == 30 for fdc_id, row in rows.items() if fdc_id != "100001"))
        duplicate = [entry["amount"] for entry in rows["100001"]["nutrients"] if entry["id"] == "1003"]
        self.assertEqual(duplicate, ["9", str((1 + ids.index("1003")) % 7), None])
        self.assertEqual([entry["id"] for entry in rows["100001"]["nutrients"]], sorted(ids + ["1003", "1003"]))
        self.assertEqual([portion["priority"] for portion in rows["100002"]["portions"]], list(range(1, 24)))

    def test_usda_filters_supporting_foods_before_limit(self):
        files = extract_usda(self.archive, self.directory)
        files["food.csv"].write_text(
            'fdc_id,data_type,description,food_category_id,publication_date\n'
            '10001,sub_sample_food,Apple laboratory sample,9,2026-01-01\n'
            '12345,foundation_food,"Apples, raw",9,2026-01-01\n',
            encoding="utf-8",
        )
        with connection(self.directory, 128) as db:
            row = next(usda_rows(db, files, 1, 1, data_type="foundation_food"))[0]
        self.assertEqual(row["fdc_id"], "12345")

    def test_usda_stale_archive_is_retried_on_the_newest_release(self):
        """D313: a stale first response is retried with a fresh URL, and the newest release is what comes back."""
        good = self.archive.read_bytes()
        for complete_from in (1, 2, 3):
            with self.subTest(complete_from=complete_from):
                requests = []
                stale = io.BytesIO()
                with zipfile.ZipFile(stale, "w") as archive:
                    archive.writestr("release/foundation_food.csv", "fdc_id\n12345\n")

                def handler(request):
                    requests.append(request)
                    return httpx.Response(200, content=good if len(requests) >= complete_from else stale.getvalue())

                latest = Release("https://fdc.nal.usda.gov/current.zip", "2026-10-29")
                previous = Release("https://fdc.nal.usda.gov/previous.zip", "2026-04-30")
                with httpx.Client(transport=httpx.MockTransport(handler)) as client:
                    archive, files, selected = stage_usda(client, [latest, previous], self.directory)
                self.assertEqual(archive.read_bytes(), good)
                self.assertIn(b"Apples, raw", files["food.csv"].read_bytes())
                self.assertEqual(selected, latest)
                self.assertEqual([request.url.path for request in requests], ["/current.zip"] * complete_from)
                # Every attempt asks for a URL of its own.
                tokens = [request.url.params["ryft_retry"] for request in requests]
                self.assertEqual(len(set(tokens)), complete_from)

    def test_usda_stale_archive_three_times_fails_without_the_older_release(self):
        requested = []
        stale = io.BytesIO()
        with zipfile.ZipFile(stale, "w") as archive:
            archive.writestr("release/foundation_food.csv", "fdc_id\n12345\n")

        def handler(request):
            requested.append(request)
            return httpx.Response(200, content=stale.getvalue())

        latest = Release("https://fdc.nal.usda.gov/current.zip", "2026-10-29")
        previous = Release("https://fdc.nal.usda.gov/previous.zip", "2026-04-30")
        with httpx.Client(transport=httpx.MockTransport(handler)) as client:
            with self.assertRaisesRegex(MissingUSDATables,
                                        "2026-10-29.*food.csv, food_nutrient.csv.*after 3 attempts"):
                stage_usda(client, [latest, previous], self.directory)
        self.assertEqual([request.url.path for request in requested], ["/current.zip"] * 3)
        self.assertNotIn("/previous.zip", [request.url.path for request in requested])

    def test_usda_archive_requests_are_not_cacheable(self):
        requested = []

        def handler(request):
            requested.append(request)
            return httpx.Response(200, content=self.archive.read_bytes())

        release = Release("https://fdc.nal.usda.gov/current.zip", "2026-10-29")
        with httpx.Client(transport=httpx.MockTransport(handler)) as client:
            stage_usda(client, [release], self.directory)
        [request] = requested
        self.assertEqual((request.headers["Cache-Control"], request.headers["Pragma"]), ("no-cache", "no-cache"))

    def test_off_recovery_nutrients_keep_units_and_basis(self):
        with connection(self.directory, 128) as db:
            row = next(off_rows(db, str(self.parquet), 1, 1))[0]
        row["categories_tags"] = ["en:beers"]
        row["product_quantity_unit"] = "ml"
        row["nutriments"] += [
            {"name": "alcohol", "100g": 5}, {"name": "caffeine", "100g": .012},
            {"name": "added-sugars", "100g": 2}, {"name": "eicosapentaenoic-acid", "100g": .03},
            {"name": "docosahexaenoic-acid", "100g": .04},
        ]
        food = off_food(row, "n2r-fixture")
        values = {n.code: n.amount for n in food.nutrients}
        self.assertEqual(food.reference_unit, "ml")
        self.assertEqual((values["alcohol"], values["caffeine"], values["epa"], values["dha"], values["added_sugar"]),
                         (3.945, 12, .03, .04, 2))
        mass_food = off_food({**row, "product_quantity_unit": "g"}, "n2r-fixture")
        self.assertNotIn("alcohol", {n.code for n in mass_food.nutrients})

    def test_offline_cli(self):
        result = subprocess.run(
            [sys.executable, "run_etl.py", "--source", "all", "--limit", "10000", "--memory-mb", "128",
             "--off-parquet", str(self.parquet), "--foundation-zip", str(self.archive), "--sr-legacy-zip", str(self.archive),
             "--no-bls-csv"],  # the committed BLS extract, once there, has its own tests (test_bls.py)
            cwd=Path(__file__).resolve().parents[1], check=True, capture_output=True, text=True,
        )
        off, foundation, legacy, bls = map(json.loads, result.stdout.splitlines())
        self.assertEqual((off["mode"], off["counts"]["accepted"], off["counts"]["rejected"]), ("dry-run", 2, 1))
        self.assertEqual(foundation["categories"], legacy["categories"])
        self.assertEqual(foundation["categories"], {"whole_fruit": 1})
        self.assertEqual((bls["release"], bls["mode"], bls["counts"]["accepted"]), ("bls", "dry-run", 3))
        # The USDA summary counts the portions: "1 cup" becomes "1 US cup", "1 medium" is already clean.
        for usda in (foundation, legacy):
            self.assertEqual(usda["portions"], {"total": 2, "relabelled": 1, "localized_de": 2,
                                                "unmapped_head": 0, "raw_marker": 0})
            self.assertEqual(usda["staples"], {"applied": 0, "description_mismatch": [], "unmatched": {}})
        self.assertNotIn("portions", off)
        self.assertNotIn("staples", bls)
        self.assertFalse(bls["bls_full"])


def labels_of(portion):
    return [portion.label, *portion.labels.values(), portion.short_label_en, portion.short_label_de]


class PortionLabelTests(unittest.TestCase):
    def test_maps_are_pinned(self):
        self.assertEqual([(size.key, size.words, size.en, size.en_short, size.de_one, size.de_adj, size.de_short, size.order)
                          for size in SIZES], [
            ("extra_small", ("extra small", "extra-small"), "extra small", "Extra small", "sehr kleines Stück", "sehr klein", "Sehr klein", 5),
            ("small", ("small",), "small", "Small", "kleines Stück", "klein", "Klein", 10),
            ("medium", ("medium",), "medium", "Medium", "mittelgroßes Stück", "mittel", "Mittel", 20),
            ("large", ("large",), "large", "Large", "großes Stück", "groß", "Groß", 30),
            ("extra_large", ("extra large", "extra-large"), "extra large", "Extra large", "sehr großes Stück", "sehr groß", "Sehr groß", 35),
            ("jumbo", ("jumbo",), "jumbo", "Jumbo", "Stück (Jumbo)", "Jumbo", "Jumbo", 38),
        ])
        self.assertEqual([(unit.key, unit.en, unit.de, unit.order, unit.kind) for unit in UNITS], [
            ("whole", ("whole", "whole"), ("Stück", "Stück"), 40, "count"),
            ("piece", ("piece", "pieces"), ("Stück", "Stück"), 41, "count"),
            ("slice", ("slice", "slices"), ("Scheibe", "Scheiben"), 42, "count"),
            ("fillet", ("fillet", "fillets"), ("Filet", "Filets"), 44, "count"),
            ("breast", ("breast", "breasts"), ("Bruststück", "Bruststücke"), 46, "count"),
            ("link", ("link", "links"), ("Würstchen", "Würstchen"), 48, "count"),
            ("patty", ("patty", "patties"), ("Patty", "Pattys"), 50, "count"),
            ("bar", ("bar", "bars"), ("Riegel", "Riegel"), 52, "count"),
            ("scoop", ("scoop", "scoops"), ("Kugel", "Kugeln"), 54, "count"),
            ("can", ("can", "cans"), ("Dose", "Dosen"), 56, "count"),
            ("bottle", ("bottle", "bottles"), ("Flasche", "Flaschen"), 58, "count"),
            ("container", ("container", "containers"), ("Becher", "Becher"), 60, "count"),
            ("packet", ("packet", "packets"), ("Päckchen", "Päckchen"), 62, "count"),
            ("serving", ("serving", "servings"), ("Portion", "Portionen"), 64, "count"),
            ("tbsp", ("tbsp", "tbsp"), ("EL", "EL"), 70, "count"),
            ("tsp", ("tsp", "tsp"), ("TL", "TL"), 72, "count"),
            ("cup", ("US cup", "US cups"), ("US-Tasse", "US-Tassen"), 80, "count"),
            ("fl_oz", ("fl oz", "fl oz"), ("fl oz", "fl oz"), 85, "volume"),
            ("nlea_serving", ("serving", "servings"), ("Portion", "Portionen"), 88, "nlea"),
            ("oz", ("oz", "oz"), ("oz", "oz"), 90, "mass"),
            ("lb", ("lb", "lb"), ("lb", "lb"), 92, "mass"),
            # D313
            ("item", ("item", "items"), ("Stück", "Stück"), 41, "count"),
            ("fruit", ("fruit", "fruits"), ("Frucht", "Früchte"), 43, "count"),
            ("wedge", ("wedge", "wedges"), ("Spalte", "Spalten"), 45, "count"),
            ("stalk", ("stalk", "stalks"), ("Stange", "Stangen"), 47, "count"),
            ("spear", ("spear", "spears"), ("Stange", "Stangen"), 49, "count"),
            ("stick", ("stick", "sticks"), ("Stange", "Stangen"), 51, "count"),
            ("leaf", ("leaf", "leaves"), ("Blatt", "Blätter"), 53, "count"),
            ("clove", ("clove", "cloves"), ("Zehe", "Zehen"), 55, "count"),
            ("head", ("head", "heads"), ("Kopf", "Köpfe"), 57, "count"),
            ("bunch", ("bunch", "bunches"), ("Bund", "Bund"), 59, "count"),
            ("sprig", ("sprig", "sprigs"), ("Zweig", "Zweige"), 61, "count"),
            ("pod", ("pod", "pods"), ("Schote", "Schoten"), 63, "count"),
            ("strip", ("strip", "strips"), ("Streifen", "Streifen"), 65, "count"),
            ("ring", ("ring", "rings"), ("Ring", "Ringe"), 66, "count"),
            ("pat", ("pat", "pats"), ("Stück", "Stück"), 67, "count"),
            ("steak", ("steak", "steaks"), ("Steak", "Steaks"), 68, "count"),
            ("chop", ("chop", "chops"), ("Kotelett", "Koteletts"), 69, "count"),
            ("pinch", ("pinch", "pinches"), ("Prise", "Prisen"), 71, "count"),
            ("drumstick", ("drumstick", "drumsticks"), ("Unterkeule", "Unterkeulen"), 73, "count"),
            ("thigh", ("thigh", "thighs"), ("Oberkeule", "Oberkeulen"), 74, "count"),
            ("wing", ("wing", "wings"), ("Flügel", "Flügel"), 75, "count"),
            ("leg", ("leg", "legs"), ("Keule", "Keulen"), 76, "count"),
            ("roll", ("roll", "rolls"), ("Brötchen", "Brötchen"), 77, "count"),
            ("bun", ("bun", "buns"), ("Brötchen", "Brötchen"), 78, "count"),
            ("bagel", ("bagel", "bagels"), ("Bagel", "Bagels"), 79, "count"),
            ("muffin", ("muffin", "muffins"), ("Muffin", "Muffins"), 81, "count"),
            ("cookie", ("cookie", "cookies"), ("Keks", "Kekse"), 82, "count"),
            ("cracker", ("cracker", "crackers"), ("Cracker", "Cracker"), 83, "count"),
            ("tortilla", ("tortilla", "tortillas"), ("Tortilla", "Tortillas"), 84, "count"),
            ("loaf", ("loaf", "loaves"), ("Laib", "Laibe"), 86, "count"),
            ("sandwich", ("sandwich", "sandwiches"), ("Sandwich", "Sandwiches"), 87, "count"),
            ("package", ("package", "packages"), ("Packung", "Packungen"), 89, "count"),
            ("cubic_inch", ("cubic inch", "cubic inches"), ("Kubikzoll", "Kubikzoll"), 90, "mass"),
            ("bag", ("bag", "bags"), ("Beutel", "Beutel"), 91, "count"),
            ("jar", ("jar", "jars"), ("Glas", "Gläser"), 93, "count"),
            ("glass", ("glass", "glasses"), ("Glas", "Gläser"), 94, "count"),
            ("envelope", ("envelope", "envelopes"), ("Päckchen", "Päckchen"), 95, "count"),
            ("tablet", ("tablet", "tablets"), ("Tablette", "Tabletten"), 96, "count"),
            ("frank", ("frank", "franks"), ("Würstchen", "Würstchen"), 97, "count"),
            ("frankfurter", ("frankfurter", "frankfurters"), ("Würstchen", "Würstchen"), 97, "count"),
            ("sausage", ("sausage", "sausages"), ("Wurst", "Würste"), 98, "count"),
            ("order", ("order", "orders"), ("Portion", "Portionen"), 99, "count"),
        ])
        self.assertEqual(QUALIFIERS, {
            "chopped": "gehackt", "diced": "gewürfelt", "cubes": "gewürfelt", "cubed": "gewürfelt",
            "sliced": "in Scheiben", "slices": "in Scheiben", "mashed": "zerdrückt", "pureed": "püriert",
            "shredded": "geraspelt", "grated": "gerieben", "halves": "halbiert", "pieces": "in Stücken",
            "strips": "in Streifen", "wedges": "Spalten", "packed": "fest gedrückt", "thin": "dünn", "thick": "dick",
            "boneless": "ohne Knochen", "bone removed": "ohne Knochen", "bone and skin removed": "ohne Haut und Knochen",
            "skin removed": "ohne Haut", "cooked": "gegart", "raw": "roh", "drained": "abgetropft",
            "crumbled": "zerbröselt", "melted": "geschmolzen", "ground": "gemahlen", "sifted": "gesiebt",
            "florets": "Röschen", "small": "klein", "medium": "mittel", "large": "groß", "whole": "ganz",
            "peeled": "geschält", "unpeeled": "ungeschält", "pitted": "entsteint", "crushed": "zerstoßen",
            "fresh": "frisch", "frozen": "tiefgekühlt", "dried": "getrocknet", "canned": "aus der Dose",
            "toasted": "getoastet", "with bone": "mit Knochen", "with skin": "mit Haut",
        })

    def test_amounts(self):
        for value, en, de in ((1, "1", "1"), (2, "2", "2"), (3, "3", "3"), (0.5, "½", "½"), (1.5, "1½", "1½"),
                              (0.25, "¼", "¼"), (0.75, "¾", "¾"), (1.75, "1¾", "1¾"), (0.333, "⅓", "⅓"),
                              (0.667, "⅔", "⅔"), (0.125, "⅛", "⅛"), (2.5, "2.5", "2,5"), (0.1, "0.1", "0,1")):
            with self.subTest(value=value):
                self.assertEqual((format_amount(value, "en"), format_amount(value, "de")), (en, de))
        for text, amount, expected in (("1/2 cup", None, "½ US cup"), ("1 1/2 cup", None, "1½ US cups"),
                                       ("cup", "2", "2 US cups"), ("cup", None, "1 US cup"),
                                       ("cup", "not a number", "1 US cup"), ("0 cup", "3", "3 US cups")):
            with self.subTest(text=text, amount=amount):
                self.assertEqual(clean_portion(text, amount, 50.0).en, expected)

    def test_sr_legacy_modifiers(self):
        rows = sr_table_rows()
        tally = {}
        portions = usda_measures(rows, None, tally)
        self.assertEqual(len(portions), len(SR_TABLE))
        self.assertEqual(tally, {"unmatched": [], "unmapped_head": 3, "unmapped_heads": ["dollop", "handful", "squirt"]})
        self.assertEqual([portion.sort_order for portion in portions], sorted(portion.sort_order for portion in portions))
        by_grams = {portion.weight_g: portion for portion in portions}
        for position, (modifier, amount, grams, en, de, short_en, short_de, order) in enumerate(SR_TABLE, 1):
            with self.subTest(modifier=modifier, amount=amount):
                portion = by_grams[grams]
                self.assertEqual((portion.label, portion.labels.get("en"), portion.labels.get("de"),
                                  portion.short_label_en, portion.short_label_de, portion.sort_order),
                                 (en, en, de, short_en, short_de, order or 100 + position))
                self.assertIsNone(portion.portion_key)
                self.assertFalse(portion.is_default)
                self.assertIn("undetermined", portion.legacy_label)  # What the stored portion ID hashes.
        for portion in portions:
            for text in filter(None, labels_of(portion)):
                with self.subTest(text=text):
                    for marker in ("undetermined", '"', " oz)", "unspecified"):
                        self.assertNotIn(marker, text)
                    self.assertNotRegex(text, r"(?i)\bdia\b")

    def test_unmapped_heads_have_no_german_labels(self):
        [dollop, handful] = usda_measures([sr_row(1, "dollop", 20.0), sr_row(2, 'handful, medium (7-1/2" to 8" long)', 41.0)])
        self.assertEqual((dollop.label, dollop.labels, dollop.short_label_en, dollop.short_label_de, dollop.sort_order),
                         ("1 dollop", {"en": "1 dollop"}, None, None, 101))
        self.assertEqual((handful.label, handful.labels, handful.short_label_de, handful.sort_order),
                         ("1 handful, medium", {"en": "1 handful, medium"}, None, 102))
        # A mapped head with one unmapped qualifier part is not translated half way.
        [cup] = usda_measures([sr_row(1, "cup, chopped, unpacked", 90.0)])
        self.assertEqual((cup.label, cup.labels, cup.short_label_en, cup.short_label_de),
                         ("1 US cup, chopped, unpacked", {"en": "1 US cup, chopped, unpacked"},
                          "1 US cup, chopped, unpacked", None))

    def test_description_rows_take_the_amount_from_the_row(self):
        [medium] = usda_measures([{"source_id": None, "description": 'medium (3" dia)', "amount": "2", "measure": None,
                                   "modifier": None, "grams": 364.0, "priority": 1}])
        self.assertEqual((medium.label, medium.labels["de"], medium.legacy_label), ("2 medium", "2 Stück, mittel", 'medium (3" dia)'))

    def test_inch_marks_outside_parentheses_and_label_limits(self):
        [pieces] = usda_measures([sr_row(1, 'cup 1/2" pieces', 120.0)])
        self.assertEqual((pieces.label, pieces.labels.get("de")), ("1 US cup, pieces", "1 US-Tasse, in Stücken"))
        [ring] = usda_measures([sr_row(1, 'ring, 3" dia', 10.0)])
        self.assertEqual(ring.labels, {"en": "1 ring", "de": "1 Ring"})
        # A label the mapping would push past 200 characters falls back instead of breaking the CHECK.
        long = clean_portion("1 cup, " + "packed, " * 24 + "packed", None, 100.0)
        self.assertEqual(long.en, "100 g")
        self.assertIsNone(long.de)
        self.assertIsNone(long.short_en)
        for text in filter(None, (pieces.label, *pieces.labels.values(), ring.label)):
            self.assertNotRegex(text, r'(?i)"|\bdia\b')

    def test_labels_that_collide_with_other_grams_name_the_grams(self):
        slices = usda_measures([sr_row(1, "slice (1 oz)", 28.0), sr_row(2, "slice (3/4 oz)", 21.0), sr_row(3, "cup", 240.0)])
        self.assertEqual([(p.label, p.labels["de"], p.short_label_en, p.short_label_de) for p in slices], [
            ("1 slice (28 g)", "1 Scheibe (28 g)", "1 slice (28 g)", "1 Scheibe (28 g)"),
            ("1 slice (21 g)", "1 Scheibe (21 g)", "1 slice (21 g)", "1 Scheibe (21 g)"),
            ("1 US cup", "1 US-Tasse", "1 US cup", "1 US-Tasse"),
        ])
        sizes = usda_measures([sr_row(1, 'medium (3" dia)', 182.0), sr_row(2, "medium whole", 150.0)])
        self.assertEqual([(p.label, p.labels["de"], p.short_label_en, p.short_label_de, p.weight_g) for p in sizes], [
            ("1 medium (182 g)", "1 mittelgroßes Stück (182 g)", "Medium (182 g)", "Mittel (182 g)", 182),
            ("1 medium (150 g)", "1 mittelgroßes Stück (150 g)", "Medium (150 g)", "Mittel (150 g)", 150),
        ])
        # Only the German label collides here: the English labels stay as they are.
        stueck = usda_measures([sr_row(1, "whole", 200.0), sr_row(2, "piece", 25.0)])
        self.assertEqual([(p.label, p.labels["de"], p.short_label_en, p.short_label_de) for p in stueck], [
            ("1 whole", "1 Stück (200 g)", "1 whole", "1 Stück (200 g)"),
            ("1 piece", "1 Stück (25 g)", "1 piece", "1 Stück (25 g)"),
        ])
        # The same label with the same grams is no collision.
        twins = usda_measures([sr_row(1, "slice (1 oz)", 28.0), sr_row(2, "slice (1.0 oz)", 28.0)])
        self.assertEqual([(p.label, p.labels["de"]) for p in twins], [("1 slice", "1 Scheibe")] * 2)

    def test_summary_counts_portions_without_changing_them(self):
        notes = ImportNotes()
        row = {"fdc_id": "999001", "description": "Cheddar cheese", "nutrients": FDC_NUTRIENTS, "portions": sr_table_rows()}
        food = usda_food(row, "fixture-v1", notes)
        self.assertEqual(food.portions, usda_food(row, "fixture-v1").portions)
        self.assertEqual(notes.summary(), {
            "portions": {"total": len(SR_TABLE), "relabelled": len(SR_TABLE),
                         "localized_de": sum(1 for entry in SR_TABLE if entry[4]), "unmapped_head": 3, "raw_marker": 0},
            "unmapped_heads": [{"head": "dollop", "count": 1}, {"head": "handful", "count": 1},
                               {"head": "squirt", "count": 1}],
            "staples": {"applied": 0, "description_mismatch": [], "unmatched": {}},
            "staple_portions": {},
        })


class StapleTests(unittest.TestCase):
    @staticmethod
    def food(fdc_id, description, portions, notes=None):
        rows = [sr_row(position, modifier, grams, source_id=str(880000 + position))
                for position, (modifier, grams) in enumerate(portions, 1)]
        return usda_food({"fdc_id": fdc_id, "description": description, "nutrients": FDC_NUTRIENTS, "portions": rows},
                         "fixture-v1", notes)

    @staticmethod
    def german(food):
        return {normalize(label) for lang, label in (*food.names, *food.aliases) if lang == "de"}

    def test_potatoes_resolve_and_keep_the_source_weights(self):
        notes = ImportNotes()
        food = self.food("170440", "Potatoes, boiled, cooked without skin, flesh, without salt", (
            ('small (1-3/4" to 2-1/4" dia)', 138.0), ('medium (2-1/4" to 3-1/4" dia)', 173.0),
            ('large (3" to 4-1/4" dia)', 299.0), ("cup, mashed", 210.0), ("cup, sliced", 156.0)), notes)
        self.assertIn(normalize("Kartoffel"), self.german(food))
        self.assertIn(normalize("Salzkartoffeln"), self.german(food))
        self.assertEqual((food.canonical_group_key, food.food_form, food.is_canonical_staple), ("potato", "boiled", True))
        self.assertEqual([(p.portion_key, p.weight_g, p.is_default, p.source_portion_id) for p in food.portions], [
            ("small", 138, False, "880001"), ("medium", 173, True, "880002"), ("large", 299, False, "880003"),
            (None, 210, False, "880004"), (None, 156, False, "880005")])
        medium = food.portions[1]
        self.assertEqual((medium.label, medium.labels, medium.short_label_de, medium.short_label_en),
                         ("1 medium potato", {"de": "1 mittelgroße Kartoffel", "en": "1 medium potato"}, "Mittel", "Medium"))
        self.assertIn("undetermined", medium.legacy_label)
        self.assertEqual(food.portions[3].labels, {"en": "1 US cup, mashed", "de": "1 US-Tasse, zerdrückt"})
        # The curated portion has the normal keyed ID of the source portion.
        namespace = uuid.uuid5(IMPORT_NAMESPACE, "usda:170440")
        ids = {p["weight_g"]: p["id"] for p in staging_row(food, 0)[22].obj}
        self.assertEqual(ids[173.0], str(source_portion_uuid(namespace, "880002", medium)))
        self.assertEqual(notes.summary()["staples"], {"applied": 1, "description_mismatch": [], "unmatched": {}})
        self.assertEqual(notes.summary()["staple_portions"], {"170440": {"small": 138, "medium": 173, "large": 299}})

    def test_avocado_half_is_the_curated_default(self):
        notes = ImportNotes()
        food = self.food("171705", "Avocados, raw, all commercial varieties", (
            ("cup, pureed", 230.0), ("cup, sliced", 146.0), ("avocado, NS as to Florida or California", 201.0),
            ("cup, cubes", 150.0)), notes)
        self.assertIn(normalize("Avocado"), self.german(food))
        self.assertEqual([(p.portion_key, p.weight_g, p.is_default, p.source_portion_id) for p in food.portions], [
            ("whole", 201, False, "880003"), ("half", 100.5, True, None),
            (None, 230, False, "880001"), (None, 146, False, "880002"), (None, 150, False, "880004")])
        whole, half = food.portions[:2]
        self.assertEqual(half.weight_g, whole.weight_g / 2)
        self.assertEqual((whole.label, whole.short_label_de, whole.short_label_en), ("1 avocado", "Ganz", "Whole"))
        self.assertEqual((half.labels, half.short_label_de, half.short_label_en),
                         ({"de": "½ Avocado", "en": "½ avocado"}, "½", "½"))
        self.assertEqual(notes.summary()["staples"], {"applied": 1, "description_mismatch": [], "unmatched": {}})

    def test_tomato_and_carrot(self):
        for fdc_id, description, word, extra, rows, grams in (
            ("170457", "Tomatoes, red, ripe, raw, year round average", "Tomate", None,
             (('small (2-2/5" dia)', 91.0), ('medium (2-3/5" dia)', 123.0), ('large (3" dia)', 182.0),
              ("cup, chopped or sliced", 180.0)), (91, 123, 182)),
            ("170393", "Carrots, raw", "Karotte", "Möhre",
             (('small (5-1/2" long)', 50.0), ('medium (7-1/4" to 8-1/2" long)', 61.0),
              ('large (7-1/4" to 8-1/2" long)', 72.0), ("cup, chopped", 128.0)), (50, 61, 72)),
        ):
            with self.subTest(fdc_id=fdc_id):
                notes = ImportNotes()
                food = self.food(fdc_id, description, rows, notes)
                self.assertIn(normalize(word), self.german(food))
                if extra:
                    self.assertIn(normalize(extra), self.german(food))
                curated = [p for p in food.portions if p.portion_key]
                self.assertEqual([(p.portion_key, p.weight_g) for p in curated], list(zip(("small", "medium", "large"), grams)))
                self.assertEqual([p.portion_key for p in food.portions if p.is_default], ["medium"])
                [default] = [p for p in food.portions if p.is_default]
                self.assertEqual(default.weight_g, grams[1])
                self.assertEqual(notes.summary()["staples"]["unmatched"], {})

    def test_extras_import_with_their_names_and_their_preset_default(self):
        # D313: the preset portion is the default and needs no source row; the source's own rows stay plain.
        for fdc_id, description, word, key, grams in (("168409", "Cucumber, with peel, raw", "Gurke", "half_200_g", 200),
                                                      ("171284", "Yogurt, plain, whole milk", "Joghurt", "pot_150_g", 150)):
            with self.subTest(fdc_id=fdc_id):
                food = self.food(fdc_id, description, (("cup", 245.0),))
                self.assertIn(normalize(word), self.german(food))
                self.assertTrue(food.is_canonical_staple)
                self.assertEqual([(p.portion_key, p.weight_g) for p in food.portions if p.is_default], [(key, grams)])
                self.assertEqual([(p.portion_key, p.weight_g) for p in food.portions], [(key, grams), (None, 245)])
        for fdc_id, description, word in (("170108", "Peppers, sweet, red, raw", "Paprika"), ("170000", "Onions, raw", "Zwiebel")):
            with self.subTest(fdc_id=fdc_id):
                food = self.food(fdc_id, description, (("small", 70.0), ("medium (2-1/2\" dia)", 110.0), ("large", 150.0)))
                self.assertIn(normalize(word), self.german(food))
                self.assertEqual([(p.portion_key, p.weight_g) for p in food.portions if p.is_default], [("medium", 110)])

    def test_a_wrong_description_imports_a_plain_food(self):
        notes = ImportNotes()
        description = "Potatoes, baked, flesh and skin, without salt"
        food = self.food("170440", description, (('medium (2-1/4" to 3-1/4" dia)', 173.0),), notes)
        self.assertEqual(food.names, [("en", description)])
        self.assertEqual((food.is_canonical_staple, food.canonical_group_key, food.aliases), (False, None, ()))
        self.assertEqual([(p.portion_key, p.is_default, p.label) for p in food.portions], [(None, False, "1 medium")])
        self.assertEqual(notes.summary()["staples"], {"applied": 0, "description_mismatch": ["170440"], "unmatched": {}})
        # The comparison is on the normalized text.
        self.assertTrue(self.food("170393", "carrots,  RAW", ()).is_canonical_staple)

    def test_a_portion_without_exactly_one_source_row_is_reported(self):
        notes = ImportNotes()
        food = self.food("170440", "Potatoes, boiled, cooked without skin, flesh, without salt", (
            ('small (1-3/4" dia)', 138.0), ('small (2" dia)', 140.0), ('medium (2-1/4" dia)', 173.0)), notes)
        # Two small rows and no large row: nothing is guessed, both small rows stay plain, the medium one applies.
        self.assertEqual([(p.portion_key, p.weight_g, p.label) for p in food.portions], [
            (None, 138, "1 small (138 g)"), (None, 140, "1 small (140 g)"), ("medium", 173, "1 medium potato")])
        self.assertEqual(notes.summary()["staples"],
                         {"applied": 1, "description_mismatch": [], "unmatched": {"170440": ["small", "large"]}})
        # An avocado needs its base row before the half exists.
        avocado = self.food("171705", "Avocados, raw, all commercial varieties", (("cup, sliced", 146.0),), notes)
        self.assertEqual([p.portion_key for p in avocado.portions], [None])
        self.assertEqual(notes.summary()["staples"]["unmatched"]["171705"], ["whole", "half"])


class RulesTests(unittest.TestCase):
    def test_relabelling_keeps_every_portion_id(self):
        g1 = usda_food({"fdc_id": "880000", "description": "Plums, raw", "nutrients": FDC_NUTRIENTS,
                        "portions": PLUM_ROWS}, "py-etl4:staples2:fixture")
        before = replace(g1, version="py-etl4:staples1:fixture", portions=pre_g1(PLUM_ROWS))

        def ids(food):
            return {p["weight_g"]: (p["id"], p["legacy_id"]) for p in staging_row(food, 0)[22].obj}

        self.assertEqual(len(ids(g1)), 4)
        self.assertEqual(ids(g1), ids(before))
        old, new = ({p["weight_g"]: p for p in staging_row(food, 0)[22].obj} for food in (before, g1))
        self.assertEqual({w: p["label"] for w, p in old.items()},
                         {182.0: '1 medium (3" dia)', 125.0: "1 cup, chopped", 28.0: "1 slice (1 oz)", 50.0: "1 bag"})
        self.assertEqual({w: p["label"] for w, p in new.items()},
                         {182.0: "1 medium", 125.0: "1 US cup, chopped", 28.0: "1 slice", 50.0: "1 bag"})
        # The legacy text travels with the portion: the merge compares it with the Go importer's rows.
        self.assertEqual({w: p["legacy_label"] for w, p in new.items()}, {w: p["legacy_label"] for w, p in old.items()})
        self.assertEqual(new[28.0]["legacy_label"], "1 undetermined slice (1 oz)")
        self.assertEqual(new[28.0]["id"], new[28.0]["legacy_id"])  # Unkeyed: the legacy ID is the ID.
        self.assertNotEqual(new[182.0]["id"], new[182.0]["legacy_id"])  # Keyed: the source portion ID is.
        self.assertNotEqual(g1.checksum, before.checksum)

    def test_keyed_portion_id_follows_the_measure(self):
        namespace = uuid.uuid5(IMPORT_NAMESPACE, "usda:171688")
        medium = Portion("Medium", "portion", weight_g=182.0, source_portion_id="89193")
        # The formula is pinned: production IDs must not drift between releases.
        self.assertEqual(source_portion_uuid(namespace, "89193", medium), uuid.uuid5(
            namespace, 'source-portion:89193|{"unit": "portion", "volume_ml": null, "weight_g": "182"}'))
        # Spelling of the same number does not change the ID; a different number or unit does.
        for same in (replace(medium, weight_g=182), replace(medium, weight_g=182.000)):
            self.assertEqual(source_portion_uuid(namespace, "89193", same), source_portion_uuid(namespace, "89193", medium))
        for other in (replace(medium, weight_g=183.0), replace(medium, weight_g=None, volume_ml=182.0),
                      replace(medium, unit="serving")):
            self.assertNotEqual(source_portion_uuid(namespace, "89193", other), source_portion_uuid(namespace, "89193", medium))
        self.assertNotEqual(source_portion_uuid(namespace, "89194", medium), source_portion_uuid(namespace, "89193", medium))
        self.assertEqual(source_portion_uuid(namespace, "glass", Portion("Glass", "portion", volume_ml=200.5)), uuid.uuid5(
            namespace, 'source-portion:glass|{"unit": "portion", "volume_ml": "200.5", "weight_g": null}'))

    def test_atwater_limits_and_exceptions(self):
        def amounts(energy=100, protein=10, fat=2, carbs=10, fiber=0):
            return {key: Nutrient(key, value, "fixture") for key, value in {
                "energy_kcal": energy, "protein": protein, "fat": fat, "carbohydrates": carbs, "fiber": fiber,
            }.items()}

        self.assertEqual(validate("Food", amounts(), False), ())
        self.assertEqual(validate("Food", amounts(energy=100, protein=0, fat=0, carbs=17.5), False), ())
        self.assertEqual(validate("Food", amounts(energy=200), True), ("atwater_alcohol_exception",))
        self.assertEqual(validate("Food", amounts(energy=200, fiber=15), False), ("atwater_high_fiber_exception",))
        for values, reason in ((amounts(energy=200), "atwater_deviation"),
                               (amounts(protein=100), "macro_mass_exceeds_105_g"),
                               (amounts(energy=951), "energy_exceeds_950_kcal")):
            with self.subTest(reason=reason), self.assertRaisesRegex(Reject, reason):
                validate("Food", values, False)
        del values["fat"]
        with self.assertRaisesRegex(Reject, "missing_or_invalid_macro"):
            validate("Food", values, False)

    def test_categories_and_unknown_fractions(self):
        cases = (
            ("Blumenkohl", "non_starchy_veg"), ("Süßkartoffeln", "starchy_veg"), ("Mais", "starchy_veg"),
            ("Äpfel", "whole_fruit"), ("Getrocknete Äpfel", "other"), ("Vollkornbrot", "whole_grain"),
            ("Weißbrot", "refined_grain"), ("Kichererbsen", "nuts_legumes"), ("Lachs", "fish_seafood"),
            ("Hähnchenbrust", "poultry_eggs"), ("Schinken", "red_processed_meat"), ("Käse", "dairy"),
            ("Rapsöl", "healthy_fat"), ("Apfelsaft", "added_sugar"), ("Sirup", "added_sugar"), ("Produkt", "other"),
        )
        for name, expected in cases:
            with self.subTest(name=name):
                self.assertEqual(classify(name, [], provider="off").name, expected)
        self.assertEqual(classify("Linsen mit Reis", [], provider="usda").profile, {})
        self.assertEqual(classify("Produkt", ["en:orange-juices"], provider="off").profile["sugary_drink"], 1)

    def test_official_release_resolution(self):
        def handler(request):
            if request.url.host == "huggingface.co":
                return httpx.Response(200, json={"sha": "a" * 40})
            return httpx.Response(200, text='''
                <a href="/fdc-datasets/FoodData_Central_foundation_food_csv_2025-04-24.zip">old</a>
                <a href="/fdc-datasets/FoodData_Central_foundation_food_csv_2026-10-29.zip">new</a>
                <a href="/fdc-datasets/FoodData_Central_foundation_food_csv_2026-10-29.zip">duplicate</a>
                <a href="https://example.com/fdc-datasets/FoodData_Central_foundation_food_csv_2029-02-01.zip">bad</a>
            ''')
        with httpx.Client(transport=httpx.MockTransport(handler)) as client:
            self.assertIn("a" * 40, resolve_off(client).location)
            releases = resolve_usda(client)["foundation_food"]
            self.assertEqual([release.version for release in releases], ["2026-10-29", "2025-04-24"])

    def test_embedding_batch_order_and_dimensions(self):
        def handler(request):
            self.assertEqual(json.loads(request.content)["input"], ["a", "b"])
            return httpx.Response(200, json={"data": [
                {"index": 1, "embedding": [1] * DIMENSIONS}, {"index": 0, "embedding": [0] * DIMENSIONS},
            ]})
        with httpx.Client(transport=httpx.MockTransport(handler)) as client:
            first, second = map(json.loads, vectors(client, ["a", "b"]))
        self.assertEqual((first, second), ([0] * DIMENSIONS, [1] * DIMENSIONS))


@unittest.skipUnless(os.getenv("RYFT_ETL_TEST_DATABASE_URL"), "requires a disposable migrated PostgreSQL database")
class PostgresTests(unittest.TestCase):
    def test_bls_source_attribution_density_and_repeatable_merge(self):
        version = "py-etl-test:bls_4_0:staples1"
        foods = [bls_food({"external_id": source_id}, version) for source_id in STAPLES["bls"]]
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, foods), {"inserted": 3, "duplicates": 0})
            self.assertEqual(load_batch(db, foods), {"unchanged": 3, "duplicates": 0})
            self.assertEqual(db.execute("""SELECT s.external_id,s.license_identifier,s.attribution,s.source_url,
                f.density_g_ml,p.weight_g,p.volume_ml FROM nutrition.food_sources s JOIN nutrition.foods f ON f.id=s.food_id
                JOIN nutrition.portions p ON p.food_id=f.id AND p.source_id=s.id AND p.is_default
                WHERE s.provider='bls' ORDER BY s.external_id""").fetchall(), [
                    ("M111200", "CC-BY-4.0", foods[1].attribution, "https://doi.org/10.25826/Data20251217-134202-0",
                    Decimal("1.034"), Decimal("206.8"), 200),
                    ("M111300", "CC-BY-4.0", foods[0].attribution, "https://doi.org/10.25826/Data20251217-134202-0",
                    Decimal("1.030"), 206, 200),
                    ("M713100", "CC-BY-4.0", foods[2].attribution, "https://doi.org/10.25826/Data20251217-134202-0",
                    None, 150, None),
            ])

    def test_copy_idempotence_and_manual_classification(self):
        external_id = str(uuid.uuid4().int % 10**18 + 1)
        row = {"fdc_id": external_id, "description": "Apples, raw", "publication_date": "2026-01-01", "nutrients": [
            {"id": key, "amount": amount} for key, amount in (("1008", 48), ("1003", 0), ("1004", 0), ("1005", 12))
        ]}
        food = replace(usda_food(row, "revision-1"), portions=(Portion("1 cup", "portion", weight_g=110.0),))
        food_id = uuid.uuid5(IMPORT_NAMESPACE, f"usda:{external_id}")
        user, request, meal, item = [uuid.uuid4() for _ in range(4)]
        legacy_portion = uuid.uuid4()
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [food, food]), {"inserted": 1, "duplicates": 1})
            portion = db.execute("SELECT id, weight_g, origin FROM nutrition.portions WHERE food_id=%s", (food_id,)).fetchone()
            self.assertEqual(portion[1:], (110, "python_etl"))
            self.assertEqual(load_batch(db, [food]), {"unchanged": 1, "duplicates": 0})
            self.assertEqual(db.execute("SELECT id FROM nutrition.portions WHERE food_id=%s", (food_id,)).fetchone(), portion[:1])
            with self.assertRaises(psycopg.errors.InsufficientPrivilege):
                db.execute("SELECT count(*) FROM nutrition.meal_items")
            db.execute("RESET ROLE")
            db.execute("""INSERT INTO nutrition.portions (id,food_id,source_id,label,unit,quantity,weight_g)
                VALUES (%s,%s,%s,'1 medium','portion',1,182)""", (legacy_portion, food_id, food_id))
            db.execute("INSERT INTO public.users (id,name) VALUES (%s, 'ETL fixture')", (user,))
            db.execute("INSERT INTO nutrition.meal_requests (user_id,request_id) VALUES (%s,%s)", (user, request))
            db.execute("""INSERT INTO nutrition.meals (id,user_id,client_request_id,request_hash,consumed_at,
                timezone,local_date,meal_type,input_method,name,notes)
                VALUES (%s,%s,%s,sha256('fixture'::bytea),now(),'UTC',current_date,'snack','manual_search','','')""",
                (meal, user, request))
            db.execute("""INSERT INTO nutrition.meal_items (id,meal_id,user_id,position,food_id,food_name,brand,
                portion_id,quantity,unit,weight_g,portion_label_snapshot,portion_short_label_snapshot,
                nutrients,sources,calculation_version,created_at)
                VALUES (%s,%s,%s,0,%s,'Apples, raw','',%s,1,'portion',110,'1 cup','Cup',
                    '{"energy_kcal":52.8}','[]',1,now())""",
                (item, meal, user, food_id, portion[0]))
            db.execute("""UPDATE nutrition.classifications SET contribution_fraction=.5, origin='manual'
                WHERE food_id=%s AND code='whole_fruit_fraction'""", (food_id,))
            db.execute("SET ROLE ryft_nutrition_etl")
            measured = replace(food.portions[0], source_portion_id="99999998", portion_key="cup",
                               labels={"en": "One cup", "de": "Eine Tasse"}, short_label_en="Cup",
                               short_label_de="Tasse", is_default=True, sort_order=10)
            enrichment = replace(food, version="revision-2", portions=(measured,))
            self.assertEqual(load_batch(db, [enrichment]), {"updated": 1, "duplicates": 0})
            self.assertEqual(db.execute("""SELECT id,source_portion_id,short_label_de,portion_key,is_default
                FROM nutrition.portions WHERE source_portion_id='99999998' AND food_id=%s""", (food_id,)).fetchone(),
                             (portion[0], "99999998", "Tasse", "cup", True))
            self.assertEqual(load_batch(db, [enrichment]), {"unchanged": 1, "duplicates": 0})
            update = replace(food, name="Apple, raw", names=[("en", "Apple, raw")], version="revision-3",
                             portions=(replace(measured, weight_g=112.0),
                                       Portion("1 medium", "portion", weight_g=182.0)))
            self.assertEqual(load_batch(db, [update]), {"updated": 1, "duplicates": 0})
            self.assertEqual(load_batch(db, [replace(food, version="revision-0", source_updated_at=None)])["conflict"], 1)
            db.execute("RESET ROLE")
            portions = db.execute("SELECT id,weight_g,origin FROM nutrition.portions WHERE food_id=%s ORDER BY weight_g", (food_id,)).fetchall()
            self.assertEqual([(weight, origin) for _, weight, origin in portions], [(112, "python_etl"), (182, "manual")])
            # A changed measure is a new portion: the old ID is retired, not edited in place.
            changed_id = source_portion_uuid(food_id, "99999998", replace(measured, weight_g=112.0))
            self.assertEqual(portions[0][0], changed_id)
            self.assertNotEqual(portions[0][0], portion[0])
            self.assertEqual(db.execute("SELECT count(*) FROM nutrition.portions WHERE id=%s", (portion[0],)).fetchone(), (0,))
            self.assertEqual(portions[1][0], legacy_portion)
            self.assertEqual(db.execute("SELECT canonical_name FROM nutrition.foods WHERE id=%s", (food_id,)).fetchone(), ("Apple, raw",))
            self.assertEqual(db.execute("""SELECT origin, contribution_fraction FROM nutrition.classifications
                WHERE food_id=%s AND code='whole_fruit_fraction'""", (food_id,)).fetchone(), ("manual", 0.5))
            self.assertEqual(db.execute("""SELECT food_id,food_name,portion_id,weight_g,portion_label_snapshot,
                portion_short_label_snapshot,nutrients FROM nutrition.meal_items WHERE id=%s""", (item,)).fetchone(),
                             (food_id, "Apples, raw", portion[0], 110, "1 cup", "Cup", {"energy_kcal": 52.8}))

    @staticmethod
    def keyed_food(version, name="Plums, raw", portions=()):
        external_id = str(uuid.uuid4().int % 10**18 + 1)
        row = {"fdc_id": external_id, "description": name, "publication_date": "2026-01-01", "nutrients": [
            {"id": key, "amount": amount} for key, amount in (("1008", 48), ("1003", 0), ("1004", 0), ("1005", 12))
        ]}
        return replace(usda_food(row, version), portions=tuple(portions)), uuid.uuid5(IMPORT_NAMESPACE, f"usda:{external_id}")

    @staticmethod
    def portion_state(db, food_id):
        return db.execute("""SELECT portion_key,id,is_default FROM nutrition.portions
            WHERE food_id=%s AND origin='python_etl' ORDER BY portion_key""", (food_id,)).fetchall()

    def test_default_moves_between_portions(self):
        small = Portion("Small plum", "portion", weight_g=60.0, portion_key="small", is_default=True, sort_order=10)
        large = Portion("Large plum", "portion", weight_g=90.0, portion_key="large", sort_order=20)
        food, food_id = self.keyed_food("revision-1", portions=(small, large))
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [food]), {"inserted": 1, "duplicates": 0})
            before = {key: (portion_id, default) for key, portion_id, default in self.portion_state(db, food_id)}
            self.assertEqual({key: default for key, (_, default) in before.items()}, {"small": True, "large": False})
            # The new default is listed first, so it is written while the old one still holds the flag.
            moved = replace(food, version="revision-2", portions=(replace(large, is_default=True), replace(small, is_default=False)))
            self.assertEqual(load_batch(db, [moved]), {"updated": 1, "duplicates": 0})
            after = {key: (portion_id, default) for key, portion_id, default in self.portion_state(db, food_id)}
            self.assertEqual(after["small"], (before["small"][0], False))
            self.assertEqual(after["large"], (before["large"][0], True))
            self.assertEqual(db.execute("SELECT count(*) FROM nutrition.portions WHERE food_id=%s AND is_default",
                                        (food_id,)).fetchone(), (1,))

    def test_curated_key_moves_between_surviving_portions(self):
        # nutrition_portions_semantic_key is checked per row: the key is released before the upsert.
        first = Portion("1 small", "portion", weight_g=60.0, source_portion_id="777201", portion_key="medium", sort_order=20)
        second = Portion("1 medium", "portion", weight_g=90.0, source_portion_id="777202", sort_order=120)
        food, food_id = self.keyed_food("revision-1", portions=(first, second))
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [food]), {"inserted": 1, "duplicates": 0})
            moved = replace(food, version="revision-2", portions=(
                replace(second, portion_key="medium", sort_order=20), replace(first, portion_key=None, sort_order=110)))
            self.assertEqual(load_batch(db, [moved]), {"updated": 1, "duplicates": 0})
            self.assertEqual(db.execute("""SELECT source_portion_id,portion_key FROM nutrition.portions
                WHERE food_id=%s ORDER BY source_portion_id""", (food_id,)).fetchall(),
                             [("777201", None), ("777202", "medium")])
            self.assertEqual(load_batch(db, [moved]), {"unchanged": 1, "duplicates": 0})

    def test_one_default_per_food_across_sources(self):
        # A second source an operator linked to the same food brings its own default: the batch
        # must not violate nutrition_portions_one_default, and the food keeps exactly one default.
        first, food_id = self.keyed_food("revision-1", portions=(
            Portion("Small plum", "portion", weight_g=60.0, portion_key="small", is_default=True, sort_order=10),))
        second, _ = self.keyed_food("revision-1", portions=(
            Portion("1 cup of plums", "portion", weight_g=165.0, portion_key="cup", is_default=True, sort_order=10),))
        linked_source = uuid.uuid4()
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [first]), {"inserted": 1, "duplicates": 0})
            db.execute("RESET ROLE")
            db.execute("""INSERT INTO nutrition.food_sources(id,food_id,provider,external_id,dataset_version,license_identifier)
                VALUES (%s,%s,'usda',%s,'revision-0','CC0-1.0')""", (linked_source, food_id, second.external_id))
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [second]), {"updated": 1, "duplicates": 0})
            defaults = db.execute("""SELECT source_id,portion_key FROM nutrition.portions
                WHERE food_id=%s AND is_default""", (food_id,)).fetchall()
            self.assertEqual(defaults, [(linked_source, "cup")])
            self.assertEqual(db.execute("""SELECT count(*) FROM nutrition.portions
                WHERE food_id=%s AND origin='python_etl'""", (food_id,)).fetchone(), (2,))
            db.execute("RESET ROLE")

    def test_identical_rerun_keeps_portion_ids(self):
        cup = Portion("1 cup", "portion", weight_g=165.0, source_portion_id="777001", portion_key="cup", is_default=True)
        pit = Portion("1 pitted plum", "portion", weight_g=66.0, source_portion_id="777002", portion_key="plum")
        food, food_id = self.keyed_food("revision-1", portions=(cup, pit))
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [food]), {"inserted": 1, "duplicates": 0})
            first = self.portion_state(db, food_id)
            namespace = food_id
            self.assertEqual({key: portion_id for key, portion_id, _ in first},
                             {"cup": source_portion_uuid(namespace, "777001", cup), "plum": source_portion_uuid(namespace, "777002", pit)})
            self.assertEqual(load_batch(db, [food]), {"unchanged": 1, "duplicates": 0})
            self.assertEqual(self.portion_state(db, food_id), first)
            # A refresh that changes the food but not the portions goes through the portion merge and keeps every ID.
            renamed = replace(food, version="revision-2", name="Plum, raw", names=[("en", "Plum, raw")])
            self.assertEqual(load_batch(db, [renamed]), {"updated": 1, "duplicates": 0})
            self.assertEqual(self.portion_state(db, food_id), first)

    @staticmethod
    def stored_portions(db, food_id):
        return {float(weight): (portion_id, label) for weight, portion_id, label in db.execute(
            "SELECT weight_g, id, label FROM nutrition.portions WHERE food_id=%s AND origin='python_etl'",
            (food_id,)).fetchall()}

    def test_g1_relabels_without_changing_portion_ids(self):
        external_id = str(uuid.uuid4().int % 10**18 + 1)
        row = {"fdc_id": external_id, "description": "Plums, raw", "publication_date": "2026-01-01",
               "nutrients": FDC_NUTRIENTS, "portions": PLUM_ROWS}
        g1 = usda_food(row, "py-etl-test:staples2:sr-legacy:fixture")
        before = replace(g1, version="py-etl-test:staples1:sr-legacy:fixture", portions=pre_g1(PLUM_ROWS))
        food_id = uuid.uuid5(IMPORT_NAMESPACE, f"usda:{external_id}")
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [before]), {"inserted": 1, "duplicates": 0})
            stored = self.stored_portions(db, food_id)
            self.assertEqual({weight: label for weight, (_, label) in stored.items()},
                             {182.0: '1 medium (3" dia)', 125.0: "1 cup, chopped", 28.0: "1 slice (1 oz)", 50.0: "1 bag"})
            # The release is the same, the pipeline and staples version differ: one wave of updates.
            self.assertEqual(load_batch(db, [g1]), {"updated": 1, "duplicates": 0})
            relabelled = self.stored_portions(db, food_id)
            self.assertEqual({weight: portion_id for weight, (portion_id, _) in relabelled.items()},
                             {weight: portion_id for weight, (portion_id, _) in stored.items()})
            self.assertEqual({weight: label for weight, (_, label) in relabelled.items()},
                             {182.0: "1 medium", 125.0: "1 US cup, chopped", 28.0: "1 slice", 50.0: "1 bag"})
            self.assertEqual(db.execute("""SELECT labels,short_label_en,short_label_de,sort_order,portion_key,is_default
                FROM nutrition.portions WHERE food_id=%s AND weight_g=182""", (food_id,)).fetchone(),
                             ({"en": "1 medium", "de": "1 mittelgroßes Stück"}, "Medium", "Mittel", 20, None, False))
            self.assertEqual(db.execute("""SELECT labels,short_label_de FROM nutrition.portions
                WHERE food_id=%s AND weight_g=50""", (food_id,)).fetchone(),
                             ({"en": "1 bag", "de": "1 Beutel"}, "1 Beutel"))
            # A second identical load changes nothing.
            self.assertEqual(load_batch(db, [g1]), {"unchanged": 1, "duplicates": 0})
            self.assertEqual(self.stored_portions(db, food_id), relabelled)

    def test_manual_twin_is_found_by_the_legacy_label(self):
        external_id = str(uuid.uuid4().int % 10**18 + 1)
        row = {"fdc_id": external_id, "description": "Plums, raw", "publication_date": "2026-01-01",
               "nutrients": FDC_NUTRIENTS,
               "portions": [sr_row(1, "slice (1 oz)", 28.0), sr_row(2, 'medium (3" dia)', 182.0, source_id="880001")]}
        g1 = usda_food(row, "py-etl-test:staples2:sr-legacy:twin")
        bare = replace(g1, version="py-etl-test:staples1:sr-legacy:twin", portions=())
        food_id = uuid.uuid5(IMPORT_NAMESPACE, f"usda:{external_id}")
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [bare]), {"inserted": 1, "duplicates": 0})
            db.execute("RESET ROLE")
            # The Go importer wrote the raw text (amount measure modifier) as the label.
            db.execute("""INSERT INTO nutrition.portions (id,food_id,source_id,label,unit,quantity,weight_g)
                VALUES (%s,%s,%s,'1 undetermined slice (1 oz)','portion',1,28)""", (uuid.uuid4(), food_id, food_id))
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [g1]), {"updated": 1, "duplicates": 0})
            db.execute("RESET ROLE")
            self.assertEqual(db.execute("""SELECT origin,label FROM nutrition.portions WHERE food_id=%s
                ORDER BY origin,label""", (food_id,)).fetchall(),
                             [("manual", "1 undetermined slice (1 oz)"), ("python_etl", "1 medium")])

    def test_embedding_store_and_resume(self):
        external_id = str(uuid.uuid4().int % 10**18 + 1)
        food = usda_food({"fdc_id": external_id, "description": "Quinoa, raw", "nutrients": [
            {"id": key, "amount": amount} for key, amount in (("1008", 48), ("1003", 0), ("1004", 0), ("1005", 12))
        ]}, "embedding-fixture")
        food_id = uuid.uuid5(IMPORT_NAMESPACE, f"usda:{external_id}")
        requests = []

        def handler(request):
            requests.extend(json.loads(request.content)["input"])
            return httpx.Response(200, json={"data": [{"index": 0, "embedding": [0.1] * DIMENSIONS}]})

        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            if db.execute("SELECT to_regclass('nutrition.food_embeddings')").fetchone()[0] is None:
                self.skipTest("optional pgvector schema is missing")
            db.execute("SET ROLE ryft_nutrition_etl")
            load_batch(db, [food])
            after = uuid.UUID(int=food_id.int - 1)
            with httpx.Client(transport=httpx.MockTransport(handler)) as api:
                self.assertEqual(pending_batch(db, api, 1, after), (1, 1, food_id))
                self.assertEqual(db.execute("SELECT count(*) FROM nutrition.food_embeddings WHERE food_id=%s", (food_id,)).fetchone(), (1,))
                updated = replace(food, name="Quinoa grains, raw", names=[("en", "Quinoa grains, raw")], version="fixture-v2")
                load_batch(db, [updated])
                db.execute("SET ROLE ryft_api")
                self.assertEqual(db.execute("SELECT count(*) FROM nutrition.food_embeddings WHERE food_id=%s", (food_id,)).fetchone(), (0,))
                db.execute("SET ROLE ryft_nutrition_etl")
                self.assertEqual(pending_batch(db, api, 1, after), (1, 1, food_id))
            self.assertEqual(requests, ["Quinoa, raw", "Quinoa grains, raw"])


if __name__ == "__main__":
    unittest.main()
