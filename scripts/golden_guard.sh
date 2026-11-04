#!/usr/bin/env bash
# The scoring golden files (backend/internal/scoring/testdata/golden, D149) change for three
# reasons only: a ModelVersion bump, a generator change (that user's input digest in
# manifest.json moved) or new fields (GOLDEN_MODE=additive: the manifest's field digest moved).
# A user-*.json that changed against the merge base without any of the three is a hand edit or a
# regeneration that hides a model change: this fails it. CI wires it on pull requests.
#   scripts/golden_guard.sh <base-ref>     e.g. origin/main
set -euo pipefail
cd "$(dirname "$0")/.."
base=${1:?usage: golden_guard.sh <base-ref>}
dir=backend/internal/scoring/testdata/golden
version_file=backend/internal/scoring/version.go
mb=$(git merge-base "$base" HEAD)

changed=$(git diff --name-only "$mb" HEAD -- "$dir/user-*.json")
if [ -z "$changed" ]; then echo "golden guard: no golden user file changed against $base"; exit 0; fi

version() { git show "$1:$version_file" 2>/dev/null | sed -nE 's/^const ModelVersion = "([^"]+)".*/\1/p'; }
old_v=$(version "$mb"); new_v=$(version HEAD)
if [ "$old_v" != "$new_v" ]; then
  echo "golden guard: ModelVersion $old_v -> $new_v: every golden file may change"
  exit 0
fi

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
manifest() { git show "$1:$dir/manifest.json" 2>/dev/null || echo '{}'; }
manifest "$mb" > "$tmp/old"; manifest HEAD > "$tmp/new"
for f in "$tmp/old" "$tmp/new"; do
  jq -e 'type == "object"' "$f" >/dev/null 2>&1 || { echo "::error file=$dir/manifest.json,title=golden guard::manifest.json is not readable JSON" >&2; exit 1; }
done
field() { jq -r "$2 // \"\"" "$1"; }
fields_moved=false
[ "$(field "$tmp/old" .fields)" = "$(field "$tmp/new" .fields)" ] || fields_moved=true

bad=0
for f in $changed; do
  u=$(basename "$f" .json)
  # shellcheck disable=SC2016 # $u is jq's variable
  was=$(jq -r --arg u "$u" '.inputs[$u].digest // ""' "$tmp/old")
  # shellcheck disable=SC2016
  now=$(jq -r --arg u "$u" '.inputs[$u].digest // ""' "$tmp/new")
  if [ "$was" != "$now" ]; then
    echo "golden guard: $u: the generator's inputs changed (${was:-none} -> ${now:-none})"
  elif $fields_moved; then
    echo "golden guard: $u: the files' fields changed (additive)"
  else
    echo "::error file=$f,title=golden guard::$u.json changed, but ModelVersion ($new_v), its input digest and the field digest did not: a stored number moved without a model bump, or the file was edited by hand. Bump ModelVersion (version.go, changelog.json, docs/SCORING.md) and run make golden-update." >&2
    bad=1
  fi
done
exit "$bad"
