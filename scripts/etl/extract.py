"""DuckDB source projection, pushdowns, and bounded row batches."""

from __future__ import annotations

import math
import os
from collections.abc import Iterator
from pathlib import Path

import duckdb

from transform import OFF_MARKETS, STAPLES, USDA_NUTRIENTS

OFF_REQUIRED = ("code", "countries_tags", "categories_tags", "product_name", "nutriments", "nutrition_data_per")
OFF_OPTIONAL = ("lang", "brands", "no_nutrition_data", "product_quantity_unit", "serving_size", "last_modified_t")
CSV = "read_csv(?, header=true, delim=',', all_varchar=true, max_line_size=131072, buffer_size=2097152)"


def connection(directory: Path, memory_mb: int) -> duckdb.DuckDBPyConnection:
    config = {
        "memory_limit": f"{memory_mb}MB",
        "threads": "2",
        "temp_directory": str(directory / "duckdb-spill"),
        "max_temp_directory_size": "256MB",
    }
    if extension_dir := os.environ.get("RYFT_ETL_DUCKDB_EXTENSIONS"):
        config["extension_directory"] = extension_dir
    return duckdb.connect(config=config)


def batches(cursor: duckdb.DuckDBPyConnection, size: int) -> Iterator[list[dict]]:
    keys = [value[0] for value in cursor.description]
    while rows := cursor.fetchmany(size):
        yield [dict(zip(keys, row, strict=True)) for row in rows]


def off_rows(db: duckdb.DuckDBPyConnection, location: str, limit: int | None, size: int) -> Iterator[list[dict]]:
    if location.startswith("https://"):
        db.execute("INSTALL httpfs")
        db.execute("LOAD httpfs")
        db.execute("SET force_download = false")
        db.execute("SET auto_fallback_to_full_download = false")
    schema = {description[0]: description[1] for description in db.execute(
        "DESCRIBE SELECT * FROM read_parquet(?)", [location],
    ).fetchall()}
    missing = sorted(set(OFF_REQUIRED) - schema.keys())
    if missing:
        raise ValueError(f"OFF Parquet projection missing fields: {', '.join(missing)}")
    for field, prefix in {"code": "VARCHAR", "countries_tags": "VARCHAR[]", "categories_tags": "VARCHAR[]",
                          "product_name": "STRUCT(", "nutriments": "STRUCT("}.items():
        if not schema[field].startswith(prefix):
            raise ValueError(f"unexpected OFF Parquet type for {field}")
    projection = list(OFF_REQUIRED) + [
        field if field in schema else f"NULL AS {field}" for field in OFF_OPTIONAL
    ]
    sql = f"""
        SELECT {", ".join(projection)} FROM read_parquet(?)
        WHERE code IS NOT NULL AND length(code) BETWEEN 8 AND 14
          AND regexp_full_match(code, '[0-9]{{8,14}}')
          AND list_has_any(countries_tags, ?::VARCHAR[])
    """
    if "obsolete" in schema:
        sql += " AND NOT coalesce(TRY_CAST(obsolete AS BOOLEAN), false)"
    params = [location, list(OFF_MARKETS)]
    if limit is not None:
        sql += " LIMIT ?"
        params.append(limit)
    yield from batches(db.execute(sql, params), size)


def csv_columns(db: duckdb.DuckDBPyConnection, path: Path) -> set[str]:
    return {row[0] for row in db.execute(f"DESCRIBE SELECT * FROM {CSV}", [str(path)]).fetchall()}


def usda_rows(db: duckdb.DuckDBPyConnection, files: dict[str, Path],
              limit: int | None, size: int, *, data_type: str) -> Iterator[list[dict]]:
    foods, values = files["food.csv"], files["food_nutrient.csv"]
    food_columns = csv_columns(db, foods)
    date_expr = "publication_date" if "publication_date" in food_columns else "NULL AS publication_date"
    has_categories = "food_category.csv" in files and "food_category_id" in food_columns
    category_id = ", food_category_id" if has_categories else ""
    params: list = [str(foods)]
    data_type_filter = ""
    if "data_type" in food_columns:
        data_type_filter = "WHERE data_type = ?"
        params.append(data_type)
    row_limit = ""
    if limit is not None:
        row_limit = "ORDER BY list_contains(?::VARCHAR[], fdc_id) DESC, fdc_id LIMIT ?"
        params.extend([list(STAPLES["usda"]), limit])
    unit_join = ""
    unit_expr = "NULL"
    params.append(str(values))
    if "nutrient.csv" in files:
        unit_join = f"LEFT JOIN {CSV} u ON u.id = n.nutrient_id"
        unit_expr = "u.unit_name"
        params.append(str(files["nutrient.csv"]))
    params.append(list(USDA_NUTRIENTS))

    portion_cte, portion_join, portion_expr = "", "", "NULL AS portions"
    if "food_portion.csv" in files:
        portion_file = files["food_portion.csv"]
        columns = csv_columns(db, portion_file)
        if {"fdc_id", "gram_weight"} <= columns:
            desc = "nullif(trim(p.portion_description),'')" if "portion_description" in columns else "NULL"
            amount = "nullif(trim(p.amount),'')" if "amount" in columns else "NULL"
            modifier = "nullif(trim(p.modifier),'')" if "modifier" in columns else "NULL"
            source_id = "nullif(trim(p.id),'')" if "id" in columns else "NULL"
            seq = "try_cast(p.seq_num AS INTEGER)" if "seq_num" in columns else "NULL"
            label_join, label = "", "NULL"
            params.append(str(portion_file))
            if "measure_unit_id" in columns and "measure_unit.csv" in files:
                measure = files["measure_unit.csv"]
                if {"id", "name"} <= csv_columns(db, measure):
                    label_join = f"LEFT JOIN {CSV} m ON m.id=p.measure_unit_id"
                    label = "nullif(trim(m.name),'')"
                    params.append(str(measure))
            portion_cte = f""", measured AS (
                SELECT p.fdc_id, {source_id} AS source_id, {desc} AS description, {amount} AS amount, {modifier} AS modifier,
                       {label} AS measure, try_cast(p.gram_weight AS DOUBLE) AS grams,
                       row_number() OVER (PARTITION BY p.fdc_id ORDER BY {seq} NULLS LAST,
                           {desc}, {amount}, {modifier}, {label}, p.gram_weight, {source_id}) AS priority
                FROM {CSV} p JOIN selected f USING(fdc_id) {label_join}
                WHERE try_cast(p.gram_weight AS DOUBLE) > 0 AND try_cast(p.gram_weight AS DOUBLE) <= 100000
            ), measures AS (
                SELECT fdc_id, list(struct_pack(source_id := source_id, description := description, amount := amount,
                       modifier := modifier, measure := measure, grams := grams, priority := priority)) AS portions
                FROM measured WHERE priority <= 50 GROUP BY fdc_id
            )"""
            portion_join = "LEFT JOIN measures pr USING(fdc_id)"
            portion_expr = "pr.portions"
    cat_join, cat_expr = "", "NULL AS category"
    if has_categories:
        cat_join = f"LEFT JOIN {CSV} c ON c.id = f.food_category_id"
        cat_expr = "c.description AS category"
        params.append(str(files["food_category.csv"]))
    sql = f"""
        WITH selected AS MATERIALIZED (
            SELECT fdc_id, description, {date_expr}{category_id}
            FROM {CSV} {data_type_filter} {row_limit}
        ), labels AS (
            SELECT n.fdc_id,
                   list(struct_pack(id := n.nutrient_id, amount := n.amount, unit := {unit_expr})) AS nutrients
            FROM {CSV} n JOIN selected f USING(fdc_id) {unit_join}
            WHERE n.nutrient_id IN (SELECT unnest(?::VARCHAR[]))
            GROUP BY n.fdc_id
        ){portion_cte}
        SELECT f.fdc_id, f.description, f.publication_date, {cat_expr}, l.nutrients, {portion_expr}
        FROM selected f LEFT JOIN labels l USING(fdc_id) {portion_join} {cat_join}
    """
    for batch in batches(db.execute(sql, params), size):
        for row in batch:
            if row["nutrients"]:
                row["nutrients"].sort(key=nutrient_order)
            if row["portions"]:
                row["portions"].sort(key=lambda portion: portion["priority"])
        yield batch


def nutrient_order(entry: dict) -> tuple:
    """The order of the former ORDER BY n.nutrient_id, try_cast(n.amount AS DOUBLE) DESC NULLS LAST.

    usda_food keeps the first entry per nutrient code, so a duplicate nutrient row keeps resolving to the
    larger measured amount. DuckDB sorts NaN above every number, so it comes first in descending order.
    Underscores are treated as unparseable on the assumption that try_cast rejects them, unlike float().
    Values where the two parsers differ (non-finite, Unicode whitespace) fail transform.number() either way.
    """
    raw = entry["amount"]
    try:
        value = float(raw) if raw is not None and "_" not in raw else None
    except ValueError:
        value = None
    if value is None:
        return (entry["id"], 2, 0.0)
    if math.isnan(value):
        return (entry["id"], 0, 0.0)
    return (entry["id"], 1, -value)
