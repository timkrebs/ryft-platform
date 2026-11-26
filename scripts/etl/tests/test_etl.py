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

from categorizer import classify
from download import MissingUSDATables, Release, extract_usda, resolve_off, resolve_usda, stage_usda
from extract import connection, off_rows, usda_rows
from generate_embeddings import DIMENSIONS, pending_batch, vectors
from import_pg import IMPORT_NAMESPACE, load_batch, source_portion_uuid
from transform import STAPLES, Nutrient, Portion, Reject, bls_food, off_food, usda_food, usda_measures, validate


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
        self.assertEqual([(p.label, p.weight_g) for p in food.portions], [("1 cup", 110), ("1 medium", 182)])
        self.assertEqual(food.portions[0].short_label_de, "1 Tasse")

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

    def test_usda_stale_archive_retry_and_previous_release(self):
        good = self.archive.read_bytes()
        with io.BytesIO() as buffer:
            with zipfile.ZipFile(buffer, "w") as archive:
                archive.writestr("release/foundation_food.csv", "fdc_id\n12345\n")
            stale = buffer.getvalue()

        latest = Release("https://fdc.nal.usda.gov/current.zip", "2026-10-29")
        previous = Release("https://fdc.nal.usda.gov/previous.zip", "2026-04-30")
        for retry_is_complete in (True, False):
            with self.subTest(retry_is_complete=retry_is_complete):
                requested = []

                def handler(request):
                    url = request.url
                    requested.append(url)
                    complete = url.path == "/previous.zip" or (url.query and retry_is_complete)
                    return httpx.Response(200, content=good if complete else stale)

                with httpx.Client(transport=httpx.MockTransport(handler)) as client:
                    archive, files, selected = stage_usda(client, [latest, previous], self.directory)
                self.assertEqual(archive.read_bytes(), good)
                self.assertIn(b"Apples, raw", files["food.csv"].read_bytes())
                self.assertEqual(requested[0].query, b"")
                self.assertEqual(requested[1].path, requested[0].path)
                self.assertIn("ryft_retry", requested[1].params)
                self.assertEqual([url.path for url in requested[2:]], [] if retry_is_complete else ["/previous.zip"])
                self.assertEqual(selected.version, latest.version if retry_is_complete else previous.version)

        with httpx.Client(transport=httpx.MockTransport(lambda _: httpx.Response(200, content=stale))) as client:
            with self.assertRaisesRegex(MissingUSDATables, "2026-10-29.*food.csv, food_nutrient.csv"):
                stage_usda(client, [latest], self.directory)

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
             "--off-parquet", str(self.parquet), "--foundation-zip", str(self.archive), "--sr-legacy-zip", str(self.archive)],
            cwd=Path(__file__).resolve().parents[1], check=True, capture_output=True, text=True,
        )
        off, foundation, legacy, bls = map(json.loads, result.stdout.splitlines())
        self.assertEqual((off["mode"], off["counts"]["accepted"], off["counts"]["rejected"]), ("dry-run", 2, 1))
        self.assertEqual(foundation["categories"], legacy["categories"])
        self.assertEqual(foundation["categories"], {"whole_fruit": 1})
        self.assertEqual((bls["release"], bls["mode"], bls["counts"]["accepted"]), ("bls", "dry-run", 3))


class RulesTests(unittest.TestCase):
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
