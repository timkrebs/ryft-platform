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
       extract.py transform.py categorizer.py import_pg.py merge_catalog.sql)
for i in "${!files[@]}"; do files[i]="$directory/${files[i]}"; done
cp "${files[@]}" "$destination"
cp "$directory/../../backend/certs/supabase-ca.crt" "$destination/supabase-ca.crt"
