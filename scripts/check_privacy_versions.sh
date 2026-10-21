#!/usr/bin/env bash
# CI's manifests job on pull requests: the list of privacy policy versions
# (web/content/legal/privacy-versions.json) is append-only. The base branch's list must be a
# prefix of the PR's: a version that consent was recorded against never leaves it.
#   scripts/check_privacy_versions.sh <base-ref>     e.g. origin/main
set -euo pipefail
cd "$(dirname "$0")/.."
base=${1:?usage: check_privacy_versions.sh <base-ref>}
f=web/content/legal/privacy-versions.json
if ! git cat-file -e "$base:$f" 2>/dev/null; then echo "privacy versions: $f is not on $base yet: nothing to compare"; exit 0; fi
old=$(mktemp); trap 'rm -f "$old"' EXIT
git show "$base:$f" > "$old"
if jq -e -n --slurpfile a "$old" --slurpfile b "$f" '($a[0]) as $x | ($b[0]) as $y | ($y | length) >= ($x | length) and ($y[0:($x | length)]) == $x' >/dev/null; then
  echo "privacy versions: $(jq -c . "$f") extends $(jq -c . "$old")"
else
  echo "::error file=$f,title=privacy versions::the list of privacy policy versions is append-only: $(jq -c . "$old") on $base must stay the start of it, now $(jq -c . "$f" 2>/dev/null || echo unreadable)." >&2
  exit 1
fi
