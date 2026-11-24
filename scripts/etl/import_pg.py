"""COPY catalog batches into transaction-local PostgreSQL staging tables."""

from __future__ import annotations

import os
import re
import uuid
from pathlib import Path

import psycopg
from psycopg.types.json import Jsonb

from categorizer import normalize
from transform import Food

# Matches Go's importedFoodNamespace and nutritionImportLock.
IMPORT_NAMESPACE = uuid.UUID("a7e40c6e-bfb4-47eb-ad64-aea61136db20")
IMPORT_LOCK = 7419020
MERGE_SQL = Path(__file__).with_name("merge_catalog.sql")
STAGING_SQL = """
CREATE TEMP TABLE etl_batch (
    ordinal integer NOT NULL, provider text NOT NULL, external_id text NOT NULL, stable_id uuid NOT NULL,
    version text NOT NULL, source_url text NOT NULL, license text NOT NULL, checksum bytea NOT NULL,
    source_updated_at timestamptz, name text NOT NULL, brand text NOT NULL, food_type text NOT NULL,
    quality text NOT NULL, normalized_brand text NOT NULL, completeness smallint NOT NULL, markets text[] NOT NULL,
    gtin text, names jsonb NOT NULL, nutrients jsonb NOT NULL, category text NOT NULL,
    confidence numeric NOT NULL, score_profile jsonb NOT NULL
) ON COMMIT DROP
"""


def connect() -> psycopg.Connection:
    """Use a standard libpq DSN or a native Cloud Run Cloud SQL socket."""
    instance = os.environ.get("INSTANCE_CONNECTION_NAME")
    dsn = os.environ.get("DATABASE_URL")
    options = {"connect_timeout": 10, "application_name": "nutrition-etl", "client_encoding": "UTF8"}
    if instance:
        if dsn or not re.fullmatch(r"[a-z0-9.-]+:[a-z0-9-]+:[a-z0-9-]+", instance):
            raise ValueError("use a valid INSTANCE_CONNECTION_NAME without DATABASE_URL")
        user = os.environ.get("PGUSER")
        database = os.environ.get("PGDATABASE")
        if not user or not database:
            raise ValueError("PGUSER and PGDATABASE are required with the Cloud SQL socket")
        return psycopg.connect(host=f"/cloudsql/{instance}", hostaddr="", port=5432, user=user, dbname=database,
                               autocommit=True, **options)
    if not dsn:
        raise ValueError("set DATABASE_URL or configure the Cloud SQL socket")
    return psycopg.connect(dsn, autocommit=True, **options)


def staging_row(food: Food, ordinal: int) -> tuple:
    off = food.provider == "off"
    if off:
        url, license_id, food_type = (
            f"https://world.openfoodfacts.org/product/{food.external_id}", "ODbL-1.0", "branded"
        )
    else:
        url, license_id, food_type = (
            f"https://fdc.nal.usda.gov/food-details/{food.external_id}/nutrients", "CC0-1.0", "generic"
        )
    names = [{"lang": lang, "label": label, "normalized": normalize(label)} for lang, label in food.names]
    return (
        ordinal, food.provider, food.external_id, uuid.uuid5(IMPORT_NAMESPACE, f"{food.provider}:{food.external_id}"),
        food.version, url, license_id, food.checksum, food.source_updated_at, food.name, food.brand, food_type,
        "warnings" if food.warnings else "source_reported", normalize(food.brand), food.completeness, food.markets, food.gtin, Jsonb(names),
        Jsonb([{"code": n.code, "amount": n.amount, "method": n.method, "basis": food.reference_unit} for n in food.nutrients]), food.category.name,
        food.category.confidence, Jsonb(food.category.profile),
    )


def load_batch(conn: psycopg.Connection, foods: list[Food]) -> dict[str, int]:
    if not foods:
        return {}
    with conn.transaction(), conn.cursor() as cursor:
        cursor.execute("SELECT pg_advisory_xact_lock(%s::bigint)", (IMPORT_LOCK,))
        cursor.execute(STAGING_SQL)
        with cursor.copy("COPY etl_batch FROM STDIN") as copy:
            for i, food in enumerate(foods):
                copy.write_row(staging_row(food, i))
        cursor.execute(MERGE_SQL.read_text(), prepare=False)
        while cursor.nextset():
            pass
        return dict(cursor.fetchall())
