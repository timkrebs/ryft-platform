#!/usr/bin/env bash
# Go coverage per package, the internal/ total and the gate against main's (CI's `test` job).
#   coverage.sh report <profile> <outdir>   per-package table -> $GITHUB_STEP_SUMMARY (or stdout),
#                                           <outdir>/packages.txt, <outdir>/total.txt (internal/, "71.25")
#   coverage.sh baseline <outdir>           fetch total.txt of the newest `coverage` artifact that a run
#                                           on main uploaded into <outdir>/main-total.txt; none -> no file, exit 0
#   coverage.sh gate <total.txt> <main-total.txt>
#                                           fail when internal/ is more than MAX_DROP (default 1.0) percentage
#                                           points below main's; a missing baseline passes and says so
# Statement-weighted from the `go test -coverprofile` file (a block counts as covered when any
# count is > 0). No absolute threshold: the gate only stops a slide. baseline needs GH_TOKEN with
# `actions: read` and GITHUB_REPOSITORY; the artifact is trusted only if main of this repository ran it.
set -euo pipefail

summary() { if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat >> "$GITHUB_STEP_SUMMARY"; else cat; fi; }

report() {
  local profile=${1:?profile} out=${2:?outdir}
  [ -s "$profile" ] || { echo "coverage: $profile is missing or empty" >&2; exit 2; }
  mkdir -p "$out"
  # One line per block: "<file>:<range> <statements> <count>". A block counts once, as covered
  # when any count is > 0; the package is the file's directory below the module path.
  awk 'NR > 1 && NF == 3 {
      key = $1; n[key] = $2; if ($3 > 0) hit[key] = 1
    }
    END {
      for (k in n) {
        p = k; sub(/:[^:]*$/, "", p); sub(/\/[^\/]*$/, "", p); sub(/^[^\/]*\/[^\/]*\/[^\/]*\//, "", p)
        tot[p] += n[k]; if (hit[k]) cov[p] += n[k]
      }
      for (p in tot) printf "%s %d %d\n", p, cov[p], tot[p]
    }' "$profile" | LC_ALL=C sort > "$out/raw.txt"
  awk '{ printf "%s\t%.1f\t%d\n", $1, ($3 ? 100 * $2 / $3 : 0), $3 }' "$out/raw.txt" > "$out/packages.txt"
  awk '$1 ~ /^internal(\/|$)/ { c += $2; t += $3 } END { printf "%.2f\n", (t ? 100 * c / t : 0) }' "$out/raw.txt" > "$out/total.txt"
  rm -f "$out/raw.txt"
  {
    echo "### Go coverage"
    echo
    echo "| package | coverage | statements |"
    echo "|---|---:|---:|"
    awk -F'\t' '{ printf "| %s | %s%% | %s |\n", $1, $2, $3 }' "$out/packages.txt"
    echo
    echo "**internal/ total: $(cat "$out/total.txt")%** (statement-weighted; tests that need Postgres or RabbitMQ count only where those run)"
  } | summary
}

baseline() {
  local out=${1:?outdir} repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY} id
  mkdir -p "$out"; rm -f "$out/main-total.txt"
  id=$(gh api "repos/$repo/actions/artifacts?name=coverage&per_page=100" --jq \
    '[.artifacts[] | select(.expired == false and .workflow_run.head_branch == "main"
       and .workflow_run.head_repository_id == .workflow_run.repository_id)]
     | sort_by(.created_at) | reverse | .[0].id // empty') || { echo "coverage: could not list artifacts; no baseline" >&2; return 0; }
  [ -n "$id" ] || { echo "coverage: no coverage artifact from main yet"; return 0; }
  gh api "repos/$repo/actions/artifacts/$id/zip" > "$out/main.zip" || { echo "coverage: download of artifact $id failed; no baseline" >&2; return 0; }
  unzip -p "$out/main.zip" total.txt > "$out/main-total.txt" 2>/dev/null || { rm -f "$out/main-total.txt"; echo "coverage: artifact $id has no total.txt; no baseline" >&2; return 0; }
  rm -f "$out/main.zip"
  echo "coverage: baseline from artifact $id: $(cat "$out/main-total.txt")%"
}

gate() {
  local cur=${1:?total.txt} base=${2:?main-total.txt} max=${MAX_DROP:-1.0} c b msg
  c=$(tr -d '[:space:]' < "$cur")
  if [ ! -s "$base" ]; then
    echo "coverage-gate: no baseline from main yet; internal/ is at ${c}%. Passing."
    echo "coverage-gate: no baseline from main yet; internal/ is at ${c}%. Passing." | summary
    return 0
  fi
  b=$(tr -d '[:space:]' < "$base")
  case "$c$b" in *[!0-9.]*|'') echo "coverage-gate: unreadable numbers (current '$c', main '$b')" >&2; exit 2 ;; esac
  if awk -v c="$c" -v b="$b" -v m="$max" 'BEGIN { exit !(b - c > m + 1e-9) }'; then
    echo "::error title=coverage-gate::internal/ coverage fell from ${b}% on main to ${c}% (allowed drop: ${max} points)" >&2
    echo "**coverage-gate failed:** internal/ ${c}% vs main ${b}% (allowed drop ${max} points)" | summary
    return 1
  fi
  msg="coverage-gate: internal/ ${c}% vs main ${b}% (allowed drop ${max} points). OK."
  echo "$msg"
  echo "$msg" | summary
}

cmd=${1:-}; shift || true
case "$cmd" in
  report) report "$@" ;;
  baseline) baseline "$@" ;;
  gate) gate "$@" ;;
  *) echo "usage: coverage.sh report <profile> <outdir> | baseline <outdir> | gate <total.txt> <main-total.txt>" >&2; exit 2 ;;
esac
