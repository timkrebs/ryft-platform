FROM python:3.11-slim

ARG SUPABASE_POOLER_HOST
ARG SUPABASE_PROJECT_REF
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 HOME=/tmp \
    RYFT_ETL_DUCKDB_EXTENSIONS=/opt/duckdb-extensions \
    SUPABASE_POOLER_HOST=${SUPABASE_POOLER_HOST} SUPABASE_PROJECT_REF=${SUPABASE_PROJECT_REF}
WORKDIR /opt/ryft-etl

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt \
    && mkdir -p "$RYFT_ETL_DUCKDB_EXTENSIONS" \
    && python -c 'import duckdb, os; duckdb.connect(config={"extension_directory": os.environ["RYFT_ETL_DUCKDB_EXTENSIONS"]}).install_extension("httpfs")'

COPY run_etl.py download.py extract.py transform.py categorizer.py import_pg.py merge_catalog.sql staples.v1.json .
COPY supabase-ca.crt /etc/ryft/supabase-ca.crt
USER 65532:65532
ENTRYPOINT ["python", "run_etl.py"]
CMD ["--source", "all", "--limit", "10000"]
