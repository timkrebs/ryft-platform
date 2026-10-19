#!/bin/sh
# Writes the per-namespace secrets for deploy/k8s/overlays/local-postgres
# (git-ignored, mode 600): random passwords, and each namespace gets only what
# its workload uses, as Vault does on EKS. Refuses to overwrite existing files,
# because Postgres takes POSTGRES_PASSWORD only on its first start.
#
#   scripts/local-secrets.sh [dir]
set -eu
dir=${1:-deploy/k8s/overlays/local-postgres/secrets}
if [ -e "$dir" ]; then
  echo "local-secrets: $dir exists; delete it to start over (the database keeps its old owner password)" >&2
  exit 1
fi
umask 077
mkdir -p "$dir"
r() { openssl rand -hex 24; }
pg=$(r); db_ingest=$(r); db_scoring=$(r); db_api=$(r); db_notify=$(r)
nats_ingest=$(r); nats_scoring=$(r); nats_api=$(r); nats_notify=$(r); api_key=$(r); admin_key=$(r)
host=postgres.ryft-postgres.svc.cluster.local:5432/ryft?sslmode=disable

cat > "$dir/postgres.env" <<ENV
POSTGRES_PASSWORD=$pg
DATABASE_URL=postgres://ryft:$pg@$host
ENV
cat > "$dir/migrate.env" <<ENV
DATABASE_URL=postgres://ryft:$pg@$host
DB_PASSWORD_INGEST=$db_ingest
DB_PASSWORD_SCORING=$db_scoring
DB_PASSWORD_API=$db_api
DB_PASSWORD_NOTIFY=$db_notify
ENV
cat > "$dir/nats.env" <<ENV
NATS_PASSWORD_INGEST=$nats_ingest
NATS_PASSWORD_SCORING=$nats_scoring
NATS_PASSWORD_API=$nats_api
NATS_PASSWORD_NOTIFY=$nats_notify
ENV
cat > "$dir/ingest.env" <<ENV
DATABASE_URL=postgres://ryft_ingest:$db_ingest@$host
NATS_USER=ingest
NATS_PASSWORD=$nats_ingest
API_KEY=$api_key
ENV
cat > "$dir/scoring.env" <<ENV
DATABASE_URL=postgres://ryft_scoring:$db_scoring@$host
NATS_USER=scoring
NATS_PASSWORD=$nats_scoring
ENV
cat > "$dir/api.env" <<ENV
DATABASE_URL=postgres://ryft_api:$db_api@$host
NATS_USER=api
NATS_PASSWORD=$nats_api
API_KEY=$api_key
ADMIN_API_KEY=$admin_key
ENV
cat > "$dir/notify.env" <<ENV
DATABASE_URL=postgres://ryft_notify:$db_notify@$host
NATS_USER=notify
NATS_PASSWORD=$nats_notify
ENV
echo "local-secrets: wrote $dir (bearer for the API: API_KEY in $dir/api.env)"
