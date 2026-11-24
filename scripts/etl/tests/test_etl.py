import json
import os
import subprocess
import sys
import unittest
import uuid
import zipfile
from dataclasses import replace
from pathlib import Path
from tempfile import TemporaryDirectory

import duckdb
import httpx
import psycopg

from categorizer import classify
from download import extract_usda, resolve_off, resolve_usda
from extract import connection, off_rows, usda_rows
from generate_embeddings import DIMENSIONS, pending_batch, vectors
from import_pg import IMPORT_NAMESPACE, load_batch
from transform import Nutrient, Reject, off_food, usda_food, validate


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
        with self.assertRaisesRegex(Reject, "unverified_100g_basis"):
            off_food({**row, "product_quantity_unit": "ml"}, "v1")
        with self.assertRaisesRegex(Reject, "invalid_barcode_checksum"):
            off_food(next(r for r in rows if r["code"] == "012345678904"), "v1")

    def test_usda_join_and_reference_units(self):
        files = extract_usda(self.archive, self.directory)
        with connection(self.directory, 128) as db:
            row = next(usda_rows(db, files, 10, 1))[0]
        self.assertEqual(row["category"], "Fruits and Fruit Juices")
        food = usda_food(row, "v1")
        self.assertEqual(food.category.name, "whole_fruit")
        self.assertEqual(food.category.profile["whole_fruit_fraction"], 1)
        values = {n.code: n.amount for n in food.nutrients}
        self.assertEqual((values["carbohydrates_total"], values["sodium"]), (12, 3))

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
        off, foundation, legacy = map(json.loads, result.stdout.splitlines())
        self.assertEqual((off["mode"], off["counts"]["accepted"], off["counts"]["rejected"]), ("dry-run", 2, 1))
        self.assertEqual(foundation["categories"], legacy["categories"])
        self.assertEqual(foundation["categories"], {"whole_fruit": 1})


class RulesTests(unittest.TestCase):
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
                <a href="https://example.com/fdc-datasets/FoodData_Central_foundation_food_csv_2029-02-01.zip">bad</a>
            ''')
        with httpx.Client(transport=httpx.MockTransport(handler)) as client:
            self.assertIn("a" * 40, resolve_off(client).location)
            self.assertEqual(resolve_usda(client)["foundation_food"].version, "2026-10-29")

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
    def test_copy_idempotence_and_manual_classification(self):
        external_id = str(uuid.uuid4().int % 10**18 + 1)
        row = {"fdc_id": external_id, "description": "Apples, raw", "publication_date": "2026-01-01", "nutrients": [
            {"id": key, "amount": amount} for key, amount in (("1008", 48), ("1003", 0), ("1004", 0), ("1005", 12))
        ]}
        food = usda_food(row, "revision-1")
        food_id = uuid.uuid5(IMPORT_NAMESPACE, f"usda:{external_id}")
        user, request, meal, item = [uuid.uuid4() for _ in range(4)]
        with psycopg.connect(os.environ["RYFT_ETL_TEST_DATABASE_URL"], autocommit=True) as db:
            db.execute("SET ROLE ryft_nutrition_etl")
            self.assertEqual(load_batch(db, [food, food]), {"inserted": 1, "duplicates": 1})
            self.assertEqual(load_batch(db, [food]), {"unchanged": 1, "duplicates": 0})
            with self.assertRaises(psycopg.errors.InsufficientPrivilege):
                db.execute("SELECT count(*) FROM nutrition.meal_items")
            db.execute("RESET ROLE")
            db.execute("INSERT INTO public.users (id,name) VALUES (%s, 'ETL fixture')", (user,))
            db.execute("INSERT INTO nutrition.meal_requests (user_id,request_id) VALUES (%s,%s)", (user, request))
            db.execute("""INSERT INTO nutrition.meals (id,user_id,client_request_id,request_hash,consumed_at,
                timezone,local_date,meal_type,input_method,name,notes)
                VALUES (%s,%s,%s,sha256('fixture'::bytea),now(),'UTC',current_date,'snack','manual_search','','')""",
                (meal, user, request))
            db.execute("""INSERT INTO nutrition.meal_items (id,meal_id,user_id,position,food_id,food_name,brand,
                quantity,unit,nutrients,sources,calculation_version,created_at)
                VALUES (%s,%s,%s,0,%s,'Apples, raw','',100,'g','{"energy_kcal":48}','[]',1,now())""",
                (item, meal, user, food_id))
            db.execute("""UPDATE nutrition.classifications SET contribution_fraction=.5, origin='manual'
                WHERE food_id=%s AND code='whole_fruit_fraction'""", (food_id,))
            db.execute("SET ROLE ryft_nutrition_etl")
            update = replace(food, name="Apple, raw", names=[("en", "Apple, raw")], version="revision-2")
            self.assertEqual(load_batch(db, [update]), {"updated": 1, "duplicates": 0})
            self.assertEqual(load_batch(db, [replace(food, version="revision-0", source_updated_at=None)])["conflict"], 1)
            db.execute("RESET ROLE")
            self.assertEqual(db.execute("SELECT canonical_name FROM nutrition.foods WHERE id=%s", (food_id,)).fetchone(), ("Apple, raw",))
            self.assertEqual(db.execute("""SELECT origin, contribution_fraction FROM nutrition.classifications
                WHERE food_id=%s AND code='whole_fruit_fraction'""", (food_id,)).fetchone(), ("manual", 0.5))
            self.assertEqual(db.execute("SELECT food_id,food_name,nutrients FROM nutrition.meal_items WHERE id=%s", (item,)).fetchone(),
                             (food_id, "Apples, raw", {"energy_kcal": 48}))

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
