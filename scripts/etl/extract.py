"""DuckDB source projection, pushdowns, and bounded row batches."""

from __future__ import annotations

import os
from collections.abc import Iterator
from pathlib import Path

import duckdb

from transform import OFF_MARKETS, USDA_NUTRIENTS

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
        row_limit = "LIMIT ?"
        params.append(limit)
    unit_join = ""
    unit_expr = "NULL"
    params.append(str(values))
    if "nutrient.csv" in files:
        unit_join = f"LEFT JOIN {CSV} u ON u.id = n.nutrient_id"
        unit_expr = "u.unit_name"
        params.append(str(files["nutrient.csv"]))
    params.append(list(USDA_NUTRIENTS))

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
                   list(struct_pack(id := n.nutrient_id, amount := n.amount, unit := {unit_expr})
                        ORDER BY n.nutrient_id, try_cast(n.amount AS DOUBLE) DESC NULLS LAST) AS nutrients
            FROM {CSV} n JOIN selected f USING(fdc_id) {unit_join}
            WHERE n.nutrient_id IN (SELECT unnest(?::VARCHAR[]))
            GROUP BY n.fdc_id
        )
        SELECT f.fdc_id, f.description, f.publication_date, {cat_expr}, l.nutrients
        FROM selected f LEFT JOIN labels l USING(fdc_id) {cat_join}
    """
    yield from batches(db.execute(sql, params), size)
