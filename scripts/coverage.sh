#!/usr/bin/env bash
# Go coverage per package, the internal/ total and the gate against main's (CI's `test` job, D-entry
# in docs/DECISIONS.md, what to do when it is red in docs/OPERATIONS.md).
#   coverage.sh report <profile> <outdir>   per-package table -> $GITHUB_STEP_SUMMARY (or stdout),
#                                           <outdir>/packages.txt, <outdir>/total.txt (internal/, "71.25").
#                                           The module path comes from backend/go.mod (GO_MOD overrides);
#                                           exit 2 when the profile holds no internal/ statement.
#   coverage.sh baseline <outdir>           the newest successful push run of ci.yml on main, its coverage
#                                           artifact, its total.txt -> <outdir>/main-total.txt. Always exit 0;
#                                           <outdir>/baseline-reason says why there is no file (an error also
#                                           emits ::warning).
#   coverage.sh gate <total.txt> <main-total.txt>
#                                           fail when internal/ is more than MAX_DROP (default 1.0) percentage
#                                           points below main's; no baseline passes and prints the reason.
# Statement-weighted from the `go test -coverprofile` file (a block counts as covered when any
# count is > 0). No absolute threshold. baseline needs GH_TOKEN with `actions: read` and
# GITHUB_REPOSITORY. Every number is one plain percentage or the script exits 2 (gate) / drops it (baseline).
set -euo pipefail

summary() { if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then cat >> "$GITHUB_STEP_SUMMARY"; else cat; fi; }

# number <file>: prints the file's content when it is one line holding a plain percentage.
number() {
  local v
  [ -f "$1" ] || return 1
  v=$(cat "$1") || return 1
  plain "$v" || return 1
  printf '%s' "$v"
}
plain() { [[ $1 =~ ^[0-9]{1,3}(\.[0-9]+)?$ ]]; }

report() {
  local profile=${1:?profile} out=${2:?outdir}
  local gomod=${GO_MOD:-"$(dirname "$0")/../backend/go.mod"} mod
  [ -s "$profile" ] || { echo "coverage: $profile is missing or empty" >&2; exit 2; }
  mod=$(awk '$1 == "module" { print $2; exit }' "$gomod" 2>/dev/null) || mod=
  [ -n "$mod" ] || { echo "coverage: no module line in $gomod" >&2; exit 2; }
  mkdir -p "$out"
  # One line per block: "<file>:<range> <statements> <count>". A block counts once, as covered
  # when any count is > 0; the package is the file's directory below the module path. A file
  # outside the module path is not counted (and the internal/ check below then fails on drift).
  awk -v mod="$mod/" 'NR > 1 && NF == 3 && index($1, mod) == 1 {
      key = $1; n[key] = $2; if ($3 > 0) hit[key] = 1
    }
    END {
      for (k in n) {
        p = substr(k, length(mod) + 1); sub(/:[^:]*$/, "", p); sub(/\/?[^\/]*$/, "", p)
        tot[p] += n[k]; if (hit[k]) cov[p] += n[k]
      }
      for (p in tot) printf "%s %d %d\n", p, cov[p], tot[p]
    }' "$profile" | LC_ALL=C sort > "$out/raw.txt"
  if ! awk '$1 ~ /^internal(\/|$)/ && $3 > 0 { found = 1 } END { exit !found }' "$out/raw.txt"; then
    rm -f "$out/raw.txt"
    echo "::error title=coverage::no internal/ statement in the profile under module $mod: module path drift or an empty profile" >&2
    exit 2
  fi
  awk '{ printf "%s\t%.1f\t%d\n", $1, ($3 ? 100 * $2 / $3 : 0), $3 }' "$out/raw.txt" > "$out/packages.txt"
  awk '$1 ~ /^internal(\/|$)/ { c += $2; t += $3 } END { printf "%.2f\n", 100 * c / t }' "$out/raw.txt" > "$out/total.txt"
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
  local out=${1:?outdir} repo=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY} run id v
  mkdir -p "$out"; rm -f "$out/main-total.txt" "$out/main.zip" "$out/baseline-reason"
  # none <reason>: genuinely nothing to compare with yet. fault <reason>: something broke; louder.
  none() { echo "$1" | tee "$out/baseline-reason"; }
  fault() { echo "$1" > "$out/baseline-reason"; echo "::warning title=coverage-gate::$1" >&2; }
  # The run first: the newest successful push run of ci.yml on main that this repository ran
  # (not a fork); then its own artifact. A PR, another workflow or a planted artifact is never a baseline.
  run=$(gh api "repos/$repo/actions/workflows/ci.yml/runs?branch=main&event=push&status=success&per_page=1" --jq \
    '[.workflow_runs[] | select(.head_branch == "main" and .event == "push" and .head_repository.id == .repository.id)]
     | .[0].id // empty') || { fault "the run list could not be read (API error), so main's coverage is unknown"; return 0; }
  if [ -z "$run" ]; then none "no coverage artifact from main yet (no successful push run of ci on main)"; return 0; fi
  [[ $run =~ ^[0-9]+$ ]] || { fault "the run list returned something unexpected"; return 0; }
  id=$(gh api "repos/$repo/actions/runs/$run/artifacts?name=coverage" --jq \
    '[.artifacts[] | select(.expired == false and .name == "coverage")] | .[0].id // empty') \
    || { fault "the artifacts of run $run could not be read (API error), so main's coverage is unknown"; return 0; }
  if [ -z "$id" ]; then none "no coverage artifact from main yet (run $run has no unexpired one)"; return 0; fi
  [[ $id =~ ^[0-9]+$ ]] || { fault "the artifact list returned something unexpected"; return 0; }
  gh api "repos/$repo/actions/artifacts/$id/zip" > "$out/main.zip" \
    || { rm -f "$out/main.zip"; fault "the download of artifact $id failed, so main's coverage is unknown"; return 0; }
  unzip -p "$out/main.zip" total.txt 2>/dev/null | head -c 64 > "$out/main-total.txt" || true
  rm -f "$out/main.zip"
  # Anything but one plain number is dropped; never echo the raw file (workflow commands start with ::).
  if v=$(number "$out/main-total.txt"); then
    echo "coverage: baseline from run $run, artifact $id: ${v}%"
  else
    rm -f "$out/main-total.txt"
    fault "total.txt of artifact $id is missing or unreadable, so main's coverage is unknown"
  fi
}

gate() {
  local cur=${1:?total.txt} base=${2:?main-total.txt} max=${MAX_DROP-1.0} c b msg reason=
  plain "$max" || { echo "coverage-gate: MAX_DROP is not a plain number" >&2; exit 2; }
  c=$(number "$cur") || { echo "coverage-gate: the current total is not a plain number" >&2; exit 2; }
  if [ ! -e "$base" ]; then
    [ ! -f "$(dirname "$base")/baseline-reason" ] || reason=$(head -c 300 "$(dirname "$base")/baseline-reason" | tr -cd '[:print:]')
    msg="coverage-gate: no baseline (${reason:-baseline was not fetched}); internal/ is at ${c}%. Passing."
    echo "$msg"
    echo "$msg" | summary
    return 0
  fi
  b=$(number "$base") || { echo "coverage-gate: the baseline is not a plain number" >&2; exit 2; }
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
