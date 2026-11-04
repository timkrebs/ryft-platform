#!/usr/bin/env bash
# The scoring golden files (backend/internal/scoring/testdata/golden, D149) change for three
# reasons only: a ModelVersion bump, a generator change (that user's input digest in
# manifest.json moved) or new fields (GOLDEN_MODE=additive: the manifest's field digest moved).
# A user-*.json that changed against the merge base without any of the three is a hand edit or a
# regeneration that hides a model change: this fails it. The version is the manifest's
# model_version at both refs (TestGoldenManifest ties it to version.go's ModelVersion, and checks
# that the digests are the generator's). Meant for pull requests; CI wiring is a follow-up PR.
#   scripts/golden_guard.sh <base-ref>     e.g. origin/main, or a commit sha
# Exit 1: a finding. Exit 2: cannot check (no jq, no such base, no merge base).
# Manifest values and paths are never printed raw: a digest must be all digits, a version
# YEAR.MONTH.N and a user file dir/user-<name>.json (a "::" in a printed string would be a
# workflow command in the Actions log).
set -euo pipefail
cd "$(dirname "$0")/.."
base=${1:?usage: golden_guard.sh <base-ref>}
dir=backend/internal/scoring/testdata/golden
err() { echo "::error title=golden guard::$*" >&2; }
command -v jq >/dev/null 2>&1 || { err "jq is not installed: cannot read manifest.json"; exit 2; }
shown=$(printf '%s' "$base" | tr -cd '[:alnum:]/._-' | cut -c1-80)
base_sha=$(git rev-parse --verify --quiet --end-of-options "$base^{commit}") \
  || { err "the base \"$shown\" is not a commit in this checkout (fetch it, e.g. fetch-depth 0)"; exit 2; }
mb=$(git merge-base "$base_sha" HEAD) \
  || { err "no merge base of \"$shown\" and HEAD (a shallow checkout? fetch-depth 0)"; exit 2; }

changed=()
while IFS= read -r -d '' f; do
  case "$f" in "$dir"/user-*.json) changed+=("$f") ;; esac
done < <(git diff -z --name-only "$mb" HEAD -- "$dir")
if [ "${#changed[@]}" -eq 0 ]; then echo "golden guard: no golden user file changed against $shown"; exit 0; fi

tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
# manifest <rev> <out>: the manifest at rev, or nothing (a missing file) when rev has none.
manifest() { git show "$1:$dir/manifest.json" > "$2" 2>/dev/null || rm -f "$2"; }
manifest "$mb" "$tmp/old"; manifest HEAD "$tmp/new"
[ -f "$tmp/new" ] || { err "golden user files changed, but $dir/manifest.json is not in HEAD"; exit 1; }
[ -f "$tmp/old" ] || echo '{}' > "$tmp/old"  # the files' first introduction: every user is new
for f in "$tmp/old" "$tmp/new"; do
  jq -e 'type == "object"' "$f" >/dev/null 2>&1 || { err "manifest.json is not readable JSON"; exit 1; }
done
# value <file> <jq path> <regex> <what>: the value if it matches regex, "" if absent; else a finding.
value() {
  local v
  v=$(jq -r "($2) // \"\" | tostring" "$1")
  if [ -n "$v" ] && ! [[ "$v" =~ $3 ]]; then err "a $4 in manifest.json has an unexpected form"; exit 1; fi
  printf '%s' "$v"
}
version_re='^[0-9]{4}\.[0-9]{2}\.[0-9]+$'
digest_re='^[0-9]{1,20}$'
old_v=$(value "$tmp/old" .model_version "$version_re" "model_version")
new_v=$(value "$tmp/new" .model_version "$version_re" "model_version")
[ -n "$new_v" ] || { err "manifest.json in HEAD has no model_version"; exit 1; }
if [ -z "$old_v" ] && jq -e 'has("model_version")' "$tmp/old" >/dev/null; then
  err "manifest.json at the merge base has an empty model_version"; exit 1
fi
if [ -n "$old_v" ] && [ "$old_v" != "$new_v" ]; then
  echo "golden guard: ModelVersion $old_v -> $new_v: every golden file may change"
  exit 0
fi
old_fields=$(value "$tmp/old" .fields "$digest_re" "field digest")
new_fields=$(value "$tmp/new" .fields "$digest_re" "field digest")

bad=0
for f in "${changed[@]}"; do
  u=$(basename "$f" .json)
  # The name comes from git: print it only as a plain user name directly in the folder.
  if ! [[ "$u" =~ ^user-[a-z0-9-]{1,32}$ ]] || [ "$f" != "$dir/$u.json" ]; then
    err "a golden user file has an unexpected name or place"; bad=1; continue
  fi
  was=$(value "$tmp/old" ".inputs[\"$u\"].digest" "$digest_re" "digest")
  now=$(value "$tmp/new" ".inputs[\"$u\"].digest" "$digest_re" "digest")
  if [ -z "$now" ] && git cat-file -e "HEAD:$f" 2>/dev/null; then
    echo "::error file=$f,title=golden guard::$u.json is in HEAD but manifest.json has no input digest for it" >&2
    bad=1
  elif [ "$was" != "$now" ]; then
    echo "golden guard: $u: the generator's inputs changed (${was:-none} -> ${now:-none})"
  elif [ "$old_fields" != "$new_fields" ]; then
    echo "golden guard: $u: the files' fields changed (additive)"
  else
    echo "::error file=$f,title=golden guard::$u.json changed, but ModelVersion ($new_v), its input digest and the field digest did not: a stored number moved without a model bump, or the file was edited by hand. Bump ModelVersion (version.go, changelog.json, docs/SCORING.md) and run make golden-update." >&2
    bad=1
  fi
done
exit "$bad"
