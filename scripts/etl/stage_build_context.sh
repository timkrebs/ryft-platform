#!/usr/bin/env bash
set -euo pipefail

[[ $# == 1 ]] || { printf 'Usage: bash stage_build_context.sh EMPTY_DIRECTORY\n' >&2; exit 2; }
directory=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
destination=$1
mkdir -p "$destination"
if [[ -n $(find "$destination" -mindepth 1 -maxdepth 1 -print -quit) ]]; then
  printf 'Use an empty ETL build context directory.\n' >&2
  exit 2
fi
files=(Dockerfile .dockerignore .gcloudignore requirements.txt run_etl.py download.py
       extract.py transform.py categorizer.py import_pg.py merge_catalog.sql staples.v1.json bls_curation.v1.json)
for i in "${!files[@]}"; do files[i]="$directory/${files[i]}"; done
cp "${files[@]}" "$destination"
# data/README.md is always there, so the Dockerfile's COPY data/ never fails; the BLS extract and its manifest join it
# once committed. Only these names go into the context (never a stray workbook).
mkdir -p "$destination/data"
cp "$directory/data/README.md" "$destination/data/"
for name in bls_4_0.csv bls_4_0.manifest.json; do
  if [[ -f "$directory/data/$name" ]]; then cp "$directory/data/$name" "$destination/data/"; fi
done
cp "$directory/../../backend/certs/supabase-ca.crt" "$destination/supabase-ca.crt"
