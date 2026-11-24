#!/usr/bin/env python3
"""Generate resumable embeddings for active, shared catalog records."""

from __future__ import annotations

import argparse
import math
import os
import sys
import time
from uuid import UUID

import httpx
import psycopg
from psycopg import sql

from import_pg import connect

MODEL = "text-embedding-3-small"
DIMENSIONS = 768
ENDPOINT = "https://api.openai.com/v1/embeddings"
WORKER_LOCK = 7419031
MAX_REQUEST_BYTES = 200_000
FOOD_TEXT = "concat_ws(E'\\n', f.canonical_name, nullif(f.brand, ''))"
FOOD_HASH = f"sha256(convert_to({FOOD_TEXT}, 'UTF8'))"
PENDING = f"""
    FROM nutrition.foods f LEFT JOIN nutrition.food_embeddings e ON e.food_id=f.id
    WHERE f.status='active' AND f.owner_user_id IS NULL
      AND (e.food_id IS NULL OR e.model <> %s OR e.input_hash <> {FOOD_HASH})
"""


def should_retry(status: int) -> bool:
    return status == 429 or status in (500, 502, 503, 504)


def vectors(client: httpx.Client, texts: list[str]) -> list[str]:
    for attempt in range(3):
        response = client.post(ENDPOINT, json={"model": MODEL, "dimensions": DIMENSIONS, "input": texts})
        if not should_retry(response.status_code) or attempt == 2:
            break
        delay = response.headers.get("Retry-After", "")
        time.sleep(min(int(delay), 30) if delay.isdigit() and len(delay) < 5 else 2 ** attempt)
    response.raise_for_status()
    data = response.json().get("data")
    if not isinstance(data, list) or len(data) != len(texts):
        raise ValueError("embedding response had the wrong record count")
    result: dict[int, str] = {}
    for record in data:
        index, values = record["index"], record["embedding"]
        if type(index) is not int or index not in range(len(texts)) or index in result:
            raise ValueError("embedding response had invalid indices")
        if not isinstance(values, list) or len(values) != DIMENSIONS or not all(
            type(v) in (int, float) and math.isfinite(v) for v in values
        ):
            raise ValueError("embedding response had invalid coordinates")
        result[index] = "[" + ",".join(map(str, values)) + "]"
    return [result[i] for i in range(len(texts))]


def pending_batch(conn: psycopg.Connection, client: httpx.Client, limit: int, after: UUID) -> tuple[int, int, UUID]:
    with conn.transaction(), conn.cursor() as cursor:
        cursor.execute("SELECT pg_try_advisory_xact_lock(%s::bigint)", (WORKER_LOCK,))
        if not cursor.fetchone()[0]:
            raise ValueError("another embedding batch is already running")
        cursor.execute(f"SELECT f.id, {FOOD_TEXT}, {FOOD_HASH} {PENDING} AND f.id > %s ORDER BY f.id LIMIT %s",
                       (MODEL, after, limit))
        picked = []
        size = 0
        for row in cursor.fetchall():
            size += len(row[1].encode())
            if size > MAX_REQUEST_BYTES:
                break
            picked.append(row)
        if not picked:
            return 0, 0, after
        extension = cursor.execute("""
            SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
            WHERE e.extname='vector'
        """).fetchone()
        if extension is None:
            raise ValueError("pgvector is required")
        answers = vectors(client, [row[1] for row in picked])
        params = [(MODEL, digest, vector, food_id, digest) for (food_id, _, digest), vector in zip(picked, answers, strict=True)]
        cursor.executemany(
            sql.SQL(f"""INSERT INTO nutrition.food_embeddings(food_id,model,input_hash,embedding)
                SELECT f.id,%s,%s,CAST(%s AS {{}}.vector) FROM nutrition.foods f
                WHERE f.id=%s AND f.owner_user_id IS NULL AND f.status='active' AND {FOOD_HASH}=%s
                ON CONFLICT(food_id) DO UPDATE SET model=excluded.model,input_hash=excluded.input_hash,
                    embedding=excluded.embedding,updated_at=now()""").format(sql.Identifier(extension[0])),
            params,
        )
        return len(picked), cursor.rowcount, picked[-1][0]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="make explicitly authorized paid embedding requests")
    size = parser.add_mutually_exclusive_group()
    size.add_argument("--limit", type=int, default=1_000)
    size.add_argument("--all", action="store_true", help="work until there are no pending public catalog foods")
    parser.add_argument("--batch-size", type=int, default=128)
    args = parser.parse_args()
    if not 1 <= args.batch_size <= 500 or args.limit < 1:
        parser.error("batch size must be 1–500 and limit positive")
    key = os.environ.get("OPENAI_API_KEY")
    if args.write and not key:
        parser.error("OPENAI_API_KEY is required with --write")
    sent = saved = 0
    try:
        with connect() as conn:
            if not args.write:
                count = conn.execute(f"SELECT count(*) {PENDING}", (MODEL,)).fetchone()[0]
                print(f"pending={count}")
                return 0
            with httpx.Client(headers={"Authorization": f"Bearer {key}"}, timeout=45) as client:
                after = UUID(int=0)
                while args.all or sent < args.limit:
                    take = args.batch_size if args.all else min(args.batch_size, args.limit - sent)
                    count, changed, after = pending_batch(conn, client, take, after)
                    if not count:
                        break
                    sent += count
                    saved += changed
                    print(f"sent={sent} saved={saved}", flush=True)
    except (httpx.HTTPError, psycopg.Error, ValueError, TypeError, KeyError):
        print(f"Embedding batch failed. Committed vectors remain resumable. sent={sent} saved={saved}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
