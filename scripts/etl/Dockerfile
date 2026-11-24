FROM python:3.11-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HOME=/tmp \
    RYFT_ETL_DUCKDB_EXTENSIONS=/opt/duckdb-extensions
WORKDIR /opt/ryft-etl

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    && mkdir -p "$RYFT_ETL_DUCKDB_EXTENSIONS" \
    && python -c 'import duckdb, os; duckdb.connect(config={"extension_directory": os.environ["RYFT_ETL_DUCKDB_EXTENSIONS"]}).install_extension("httpfs")'

COPY run_etl.py download.py extract.py transform.py categorizer.py import_pg.py merge_catalog.sql .
USER 65532:65532
ENTRYPOINT ["python", "run_etl.py"]
CMD ["--source", "all", "--limit", "10000"]
