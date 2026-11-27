"""COPY catalog batches into transaction-local PostgreSQL staging tables."""

from __future__ import annotations

import json
import os
import re
import uuid
from dataclasses import asdict
from decimal import Decimal
from pathlib import Path

import psycopg
from psycopg.conninfo import conninfo_to_dict
from psycopg.types.json import Jsonb

from categorizer import normalize
from transform import Food, Portion

# Matches Go's importedFoodNamespace and nutritionImportLock.
IMPORT_NAMESPACE = uuid.UUID("a7e40c6e-bfb4-47eb-ad64-aea61136db20")
IMPORT_LOCK = 7419020
MERGE_SQL = Path(__file__).with_name("merge_catalog.sql")
SUPABASE_CA = "/etc/ryft/supabase-ca.crt"
POSTGRES_PORT = 5432
SUPABASE_CONNECTION_KEYS = {"host", "port", "dbname", "user", "password", "sslmode", "sslrootcert"}
SUPABASE_POOLER = re.compile(r"aws-[0-9]+-[a-z0-9-]+\.pooler\.supabase\.com")
SUPABASE_PROJECT = re.compile(r"[a-z0-9]{20}")
STAGING_SQL = """
CREATE TEMP TABLE etl_batch (
    ordinal integer NOT NULL, provider text NOT NULL, external_id text NOT NULL, stable_id uuid NOT NULL,
    version text NOT NULL, source_url text NOT NULL, license text NOT NULL, checksum bytea NOT NULL,
    source_updated_at timestamptz, name text NOT NULL, brand text NOT NULL, food_type text NOT NULL,
    quality text NOT NULL, normalized_brand text NOT NULL, completeness smallint NOT NULL, markets text[] NOT NULL,
    gtin text, names jsonb NOT NULL, nutrients jsonb NOT NULL, category text NOT NULL,
    confidence numeric NOT NULL, score_profile jsonb NOT NULL, portions jsonb NOT NULL,
    is_canonical_staple boolean NOT NULL, canonical_group_key text, food_form text, subtitles jsonb NOT NULL,
    attribution text, density_g_ml numeric
) ON COMMIT DROP
"""


def validate_supabase_dsn(dsn: str) -> dict[str, str]:
    """Validate hosted TCP options before libpq attempts authentication."""
    host = os.environ.get("SUPABASE_POOLER_HOST", "")
    project = os.environ.get("SUPABASE_PROJECT_REF", "")
    if not SUPABASE_POOLER.fullmatch(host) or not SUPABASE_PROJECT.fullmatch(project):
        raise ValueError("configure the approved Supabase pooler host and project for Cloud Run")
    try:
        params = conninfo_to_dict(dsn)
    except psycopg.ProgrammingError:
        raise ValueError("Cloud Run DATABASE_URL is not a valid PostgreSQL connection string") from None
    if params.keys() - SUPABASE_CONNECTION_KEYS or os.environ.get("PGSERVICE"):
        raise ValueError("Cloud Run DATABASE_URL has unsupported PostgreSQL connection settings")
    if params.get("host") != host or params.get("port") != str(POSTGRES_PORT):
        raise ValueError("Cloud Run DATABASE_URL must use the approved Supabase session pooler")
    if params.get("dbname") != "postgres" or not re.fullmatch(rf"[a-zA-Z0-9_]+\.{project}", params.get("user", "")):
        raise ValueError("Cloud Run DATABASE_URL must name the approved Supabase database and project login")
    if params.get("sslmode") != "verify-full" or params.get("sslrootcert", SUPABASE_CA) != SUPABASE_CA:
        raise ValueError("Cloud Run DATABASE_URL requires sslmode=verify-full and the bundled Supabase CA")
    return {**params, "sslrootcert": SUPABASE_CA, "hostaddr": "", "gssencmode": "disable"}


def connect() -> psycopg.Connection:
    """Use verified hosted TCP, a native Cloud SQL socket, or a local libpq DSN."""
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
        return psycopg.connect(host=f"/cloudsql/{instance}", hostaddr="", port=POSTGRES_PORT, user=user, dbname=database,
                               autocommit=True, **options)
    if not dsn:
        raise ValueError("set DATABASE_URL or configure the Cloud SQL socket")
    if os.environ.get("CLOUD_RUN_JOB") or os.environ.get("K_SERVICE"):
        return psycopg.connect(autocommit=True, **options, **validate_supabase_dsn(dsn))
    return psycopg.connect(dsn, autocommit=True, **options)


def measure_text(value: float | None) -> str | None:
    """One spelling per number, so 110, 110.0 and 1.1e2 give the same portion ID."""
    return None if value is None else format(Decimal(str(value)).normalize(), "f")


def source_portion_uuid(namespace: uuid.UUID, key: str, portion: Portion) -> uuid.UUID:
    """A keyed portion keeps its ID while its measure is unchanged.

    The measure is part of the ID: a source that changes a portion's grams or millilitres
    gets a new ID and the old one is retired. A selection saved against the old ID is
    then rejected instead of silently logging the new amount.
    """
    measure = {"unit": portion.unit, "weight_g": measure_text(portion.weight_g), "volume_ml": measure_text(portion.volume_ml)}
    return uuid.uuid5(namespace, f"source-portion:{key}|{json.dumps(measure, sort_keys=True)}")


def staging_row(food: Food, ordinal: int) -> tuple:
    off = food.provider == "off"
    if off:
        url, license_id, food_type = (
            f"https://world.openfoodfacts.org/product/{food.external_id}", "ODbL-1.0", "branded"
        )
    elif food.provider == "usda":
        url, license_id, food_type = (
            f"https://fdc.nal.usda.gov/food-details/{food.external_id}/nutrients", "CC0-1.0", "generic"
        )
    elif food.provider == "bls":
        url, license_id, food_type = "https://doi.org/10.25826/Data20251217-134202-0", "CC-BY-4.0", "generic"
    else:
        raise ValueError("unsupported catalog provider")
    names = [
        {"lang": lang, "label": label, "normalized": normalize(label), "alias": alias}
        for pairs, alias in ((food.names, False), (food.aliases, True))
        for lang, label in pairs
    ]
    namespace = uuid.uuid5(IMPORT_NAMESPACE, f"{food.provider}:{food.external_id}")
    portions = []
    for portion in food.portions:
        data = asdict(portion)
        # The legacy text stays in the payload: the merge compares it with the Go importer's manual rows.
        legacy_label = data["legacy_label"] or portion.label
        legacy = {"label": legacy_label, "unit": portion.unit, "weight_g": portion.weight_g, "volume_ml": portion.volume_ml}
        legacy_id = uuid.uuid5(namespace, "portion:" + json.dumps(legacy, sort_keys=True))
        key = portion.source_portion_id or portion.portion_key
        portion_id = source_portion_uuid(namespace, key, portion) if key else legacy_id
        if off:
            data["labels"] = {"de": "Portion", "en": "Serving"}
        portions.append({**data, "id": str(portion_id), "legacy_id": str(legacy_id)})
    return (
        ordinal, food.provider, food.external_id, namespace,
        food.version, url, license_id, food.checksum, food.source_updated_at, food.name, food.brand, food_type,
        "warnings" if food.warnings else "source_reported", normalize(food.brand), food.completeness, food.markets, food.gtin, Jsonb(names),
        Jsonb([{"code": n.code, "amount": n.amount, "method": n.method, "basis": food.reference_unit} for n in food.nutrients]), food.category.name,
        food.category.confidence, Jsonb(food.category.profile), Jsonb(portions), food.is_canonical_staple,
        food.canonical_group_key, food.food_form, Jsonb(food.subtitles), food.attribution, food.density_g_ml,
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
