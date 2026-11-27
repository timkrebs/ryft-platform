#!/usr/bin/env python3
"""Offline validation or explicit bulk import of OFF and FDC releases."""

from __future__ import annotations

import argparse
import json
import sys
import zipfile
from collections import Counter
from contextlib import ExitStack
from functools import partial
from pathlib import Path
from tempfile import TemporaryDirectory

import duckdb
import httpx
import psycopg
from tqdm import tqdm

from download import CLIENT_HEADERS, extract_usda, file_sha256, resolve_off, resolve_usda, stage_usda
from extract import connection, off_rows, usda_rows
from import_pg import connect, load_batch
from transform import (BLS_CSV_PATH, STAPLES, BlsNotes, ImportNotes, Reject, Skip, bls_csv_rows, bls_food,
                       bls_input_digest, bls_row_food, load_curation, off_food, usda_food, verify_bls_extract)

PIPELINE_VERSION = "py-etl4"
USDA_KINDS = ("foundation_food", "sr_legacy_food")


def bounded_int(low: int, high: int):
    def parse(value: str) -> int:
        result = int(value)
        if not low <= result <= high:
            raise argparse.ArgumentTypeError(f"expected an integer from {low} to {high}")
        return result
    return parse


def arguments() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", choices=("all", "off", "usda", "foundation", "sr-legacy", "bls"), default="all")
    size = parser.add_mutually_exclusive_group()
    size.add_argument("--limit", type=bounded_int(1, 5_000_000), default=10_000,
                      help="raw rows per release after source pushdowns (default 10000)")
    size.add_argument("--full", action="store_true", help="scan all matched rows in the selected releases")
    parser.add_argument("--batch-size", type=bounded_int(1, 5_000), default=2_000)
    parser.add_argument("--memory-mb", type=bounded_int(128, 2_048), default=768)
    parser.add_argument("--off-parquet", type=Path, help="local parquet artifact")
    parser.add_argument("--foundation-zip", type=Path, help="local Foundation Foods CSV archive")
    parser.add_argument("--sr-legacy-zip", type=Path, help="local SR Legacy CSV archive")
    bls = parser.add_mutually_exclusive_group()
    bls.add_argument("--bls-csv", type=Path, help="BLS extract to import (default: data/bls_4_0.csv when present)")
    bls.add_argument("--no-bls-csv", action="store_true", help="import only the curated BLS staples, even if the extract exists")
    parser.add_argument("--write", action="store_true", help="merge results into PostgreSQL")
    return parser.parse_args()


def refuse_staples_over_full_bls(conn) -> None:
    """The staples-only BLS path must not write over a full BLS import: it would replace the three staples' CSV
    nutrients with the curated subset (a new version, so `updated`). Only a full import writes BLS foods other than
    the curated staples, so any such source marks one, whatever the version. Read-only; without a connection (a dry
    run) there is nothing to protect."""
    if conn is None:
        return
    if conn.execute("SELECT 1 FROM nutrition.food_sources WHERE provider='bls' AND NOT (external_id = ANY(%s)) LIMIT 1",
                    (sorted(STAPLES["bls"]),)).fetchone():
        raise ValueError("the catalog holds the full BLS import; the staples-only path (--no-bls-csv, or an image "
                         "without data/bls_4_0.csv) would overwrite its staples. Use the image with the extract.")


def process(rows, normalize_row, version: str, conn, label: str, summary=None) -> dict:
    """`summary`, when given, returns extra counters for the report. It reads what normalize_row noted."""
    outcomes: Counter = Counter()
    categories: Counter = Counter()
    rejects: Counter = Counter()
    warnings: Counter = Counter()
    with tqdm(desc=label, unit="rows", disable=not sys.stderr.isatty(), file=sys.stderr) as progress:
        for batch in rows:
            foods = []
            for row in batch:
                outcomes["processed"] += 1
                try:
                    food = normalize_row(row, version)
                except Skip:
                    outcomes["skipped"] += 1  # a deliberate omission the summary explains, no quality reject
                    continue
                except Reject as invalid:
                    rejects[str(invalid)] += 1
                    outcomes["rejected"] += 1
                    continue
                foods.append(food)
                outcomes["accepted"] += 1
                categories[food.category.name] += 1
                warnings.update(food.warnings)
            if conn is not None:
                outcomes.update(load_batch(conn, foods))
            progress.update(len(batch))
    for name in ("processed", "accepted", "rejected", "inserted", "updated", "unchanged", "conflict", "duplicates"):
        outcomes.setdefault(name, 0)
    return {"release": label, "version": version, "counts": dict(outcomes),
            "categories": dict(categories), "reject_reasons": dict(rejects), "warnings": dict(warnings),
            **(summary() if summary else {})}


def run(args: argparse.Namespace) -> None:
    limit = None if args.full else args.limit
    with ExitStack() as stack:
        conn = stack.enter_context(connect()) if args.write else None
        bls_csv = args.bls_csv or (None if args.no_bls_csv or not BLS_CSV_PATH.is_file() else BLS_CSV_PATH)
        if args.source in ("all", "bls") and bls_csv is None:
            refuse_staples_over_full_bls(conn)  # before any source runs, so a refused run writes nothing
        client = stack.enter_context(httpx.Client(headers=CLIENT_HEADERS, timeout=45, follow_redirects=True, max_redirects=4))
        if args.source in ("all", "off"):
            release = resolve_off(client, args.off_parquet)
            with TemporaryDirectory(prefix="ryft-off-", dir="/tmp") as path, connection(Path(path), args.memory_mb) as db:
                result = process(off_rows(db, release.location, limit, args.batch_size), off_food,
                                 f"{PIPELINE_VERSION}:off:{release.version}", conn, "off")
                print(json.dumps({"mode": "write" if args.write else "dry-run", **result}, sort_keys=True), flush=True)
        kinds = {
            "all": USDA_KINDS, "usda": USDA_KINDS, "foundation": USDA_KINDS[:1], "sr-legacy": USDA_KINDS[1:],
        }.get(args.source, ())
        local = dict(zip(USDA_KINDS, (args.foundation_zip, args.sr_legacy_zip), strict=True))
        official = resolve_usda(client) if any(not local[kind] for kind in kinds) else {}
        for kind in kinds:
            with TemporaryDirectory(prefix=f"ryft-{kind}-", dir="/tmp") as path:
                directory = Path(path)
                archive = local[kind]
                if archive is None:
                    if kind not in official:
                        raise ValueError(f"USDA did not publish a matching CSV release for {kind}")
                    archive, files, release = stage_usda(client, official[kind], directory)
                    result_metadata = {"source_release": release.version}
                else:
                    files = extract_usda(archive, directory)
                    result_metadata = {}
                digest = file_sha256(archive)
                tag = "foundation" if kind == "foundation_food" else "sr-legacy"
                version = f"{PIPELINE_VERSION}:staples{STAPLES['version']}:{tag}:{digest}"
                with connection(directory, args.memory_mb) as db:
                    rows = usda_rows(db, files, limit, args.batch_size, data_type=kind)
                    notes = ImportNotes()
                    result = process(rows, partial(usda_food, notes=notes), version, conn, kind, notes.summary)
                print(json.dumps({"mode": "write" if args.write else "dry-run", "sha256": digest,
                                  "has_category_table": "food_category.csv" in files,
                                  **result_metadata, **result}, sort_keys=True), flush=True)
        if args.source in ("all", "bls"):
            csv_path = bls_csv
            if csv_path is None:
                # Without the extract (or with --no-bls-csv): the three curated staples under the staples-only version.
                # The version carries the staples version, so after a staples bump (3 since D313) these rows are a new
                # dataset version: `updated` where their content changed since the last write (the full citation as
                # attribution since D296's data commit), else `unchanged`, never `conflict`. Over a full import run()
                # refused it above.
                ids = list(STAPLES["bls"])[:limit]
                batches = ([{"external_id": external_id} for external_id in ids[start:start + args.batch_size]]
                           for start in range(0, len(ids), args.batch_size))
                version = f"{PIPELINE_VERSION}:bls_4_0:staples{STAPLES['version']}"
                result = process(batches, bls_food, version, conn, "bls", lambda: {"bls_full": False})
            else:
                # The extract must be the one its manifest describes, and the whole file is checked before the first batch.
                verify_bls_extract(csv_path)
                # The version carries the digest of the CSV, the curation, the staples and the transform version, so a
                # changed input is an update, never a conflict.
                version = f"{PIPELINE_VERSION}:bls_4_0:staples{STAPLES['version']}:{bls_input_digest(csv_path)}"
                notes = BlsNotes()
                batches = bls_csv_rows(csv_path, args.batch_size, limit)
                result = process(batches, partial(bls_row_food, curation=load_curation(), notes=notes), version, conn,
                                 "bls", partial(notes.summary, conn))
            print(json.dumps({"mode": "write" if args.write else "dry-run", **result}, sort_keys=True), flush=True)


def main() -> int:
    args = arguments()
    try:
        run(args)
    except httpx.HTTPStatusError as error:
        print(f"ETL failed: release request returned HTTP {error.response.status_code}", file=sys.stderr)
    except httpx.HTTPError:
        print("ETL failed: unable to read official release metadata or artifact", file=sys.stderr)
    except psycopg.Error as error:
        print(f"ETL failed: database error ({error.sqlstate or 'connection'}). Check migration and import role.", file=sys.stderr)
    except duckdb.Error as error:
        print(f"ETL failed: DuckDB {type(error).__name__}. Check the artifact schema, HTTP range access or temporary memory limits.", file=sys.stderr)
    except ValueError as error:
        print(f"ETL failed: {error}", file=sys.stderr)
    except (OSError, zipfile.BadZipFile):
        print("ETL failed: could not read or stage an artifact", file=sys.stderr)
    else:
        return 0
    return 1


if __name__ == "__main__":
    sys.exit(main())
