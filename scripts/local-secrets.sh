#!/bin/sh
# Writes the per-namespace secrets for deploy/k8s/overlays/local-postgres
# (git-ignored, mode 600): random passwords, and each namespace gets only what
# its workload uses, as Vault does on EKS. Refuses to overwrite existing files,
# because Postgres takes POSTGRES_PASSWORD only on its first start; a directory from
# before RabbitMQ (D102) only gets the broker's users added, in place of NATS's.
#
#   scripts/local-secrets.sh [dir]
set -eu
dir=${1:-deploy/k8s/overlays/local-postgres/secrets}
umask 077
r() { openssl rand -hex 24; }
amqp_users() {
  for s in ingest scoring api notify; do
    pw=$(r)
    printf 'username=%s\npassword=%s\n' "$s" "$pw" > "$dir/amqp-user-$s.env"
    printf 'AMQP_USER=%s\nAMQP_PASSWORD=%s\n' "$s" "$pw" >> "$dir/$s.env"
  done
}
if [ -e "$dir" ]; then
  if [ ! -e "$dir/amqp-user-ingest.env" ] && [ -e "$dir/ingest.env" ]; then
    for s in ingest scoring api notify; do
      grep -v '^NATS_' "$dir/$s.env" > "$dir/$s.env.new" && mv "$dir/$s.env.new" "$dir/$s.env"
    done
    rm -f "$dir/nats.env"
    amqp_users
    echo "local-secrets: added the RabbitMQ users to $dir (NATS's removed)"
    exit 0
  fi
  echo "local-secrets: $dir exists; delete it to start over (the database keeps its old owner password)" >&2
  exit 1
fi
mkdir -p "$dir"
pg=$(r); db_ingest=$(r); db_scoring=$(r); db_api=$(r); db_notify=$(r)
api_key=$(r); admin_key=$(r)
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
cat > "$dir/ingest.env" <<ENV
DATABASE_URL=postgres://ryft_ingest:$db_ingest@$host
API_KEY=$api_key
ENV
cat > "$dir/scoring.env" <<ENV
DATABASE_URL=postgres://ryft_scoring:$db_scoring@$host
ENV
cat > "$dir/api.env" <<ENV
DATABASE_URL=postgres://ryft_api:$db_api@$host
API_KEY=$api_key
ADMIN_API_KEY=$admin_key
ENV
cat > "$dir/notify.env" <<ENV
DATABASE_URL=postgres://ryft_notify:$db_notify@$host
ENV
amqp_users
echo "local-secrets: wrote $dir (bearer for the API: API_KEY in $dir/api.env)"
