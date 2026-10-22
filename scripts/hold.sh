#!/usr/bin/env bash
# shellcheck disable=SC2016
# Holds an image group, or releases it (docs/OPERATIONS.md → Deploy and roll back; D107):
#   scripts/hold.sh add <services|web> "<reason>"
#   scripts/hold.sh rm  <services|web>
# `add` writes exactly one line, `  # hold: <group> <YYYY-MM-DD UTC> <reason>`, right above the
# group's first image in deploy/k8s/overlays/eks/kustomization.yaml (ingest for the services,
# web for the site), the format scripts/deploy_targets.sh accepts. `rm` deletes it. Both then
# run `deploy_targets.sh --check` and put the file back as it was if that fails. The file is
# changed, not committed: a rollback is the old newTag(s) AND the hold in one commit.
# Idempotent: adding what is there changes nothing (the original date stays, so the deploy
# watch's clock is not reset), removing what is not there says so. A different reason for a
# group that is already held is refused: `rm` it first, deliberately.
#   TODAY  tests only: the date to write (YYYY-MM-DD)
set -euo pipefail
cd "$(dirname "$0")/.."
overlay=deploy/k8s/overlays/eks/kustomization.yaml
usage='usage: scripts/hold.sh add <services|web> "<reason>"  |  scripts/hold.sh rm <services|web>'
cmd=${1:-}; group=${2:-}
case "$group" in services|web) ;; *) echo "hold: group must be services or web. $usage" >&2; exit 2 ;; esac
case "$cmd" in
  add) [ "$#" = 3 ] || { echo "hold: add takes a group and one reason. $usage" >&2; exit 2; } ;;
  rm)  [ "$#" = 2 ] || { echo "hold: rm takes a group only. $usage" >&2; exit 2; } ;;
  *)   echo "hold: $usage" >&2; exit 2 ;;
esac
[ -f "$overlay" ] || { echo "hold: $overlay not found" >&2; exit 2; }

# The same line shape as deploy_targets.sh; a group is matched whole.
line_re="^[[:space:]]*#[[:space:]]hold:[[:space:]]${group}[[:space:]]([0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01]))[[:space:]]+([^[:space:]].*[^[:space:]]|[^[:space:]])[[:space:]]*\$"
existing=
while IFS= read -r l; do
  if [[ $l =~ $line_re ]]; then existing=${BASH_REMATCH[1]}$'\t'${BASH_REMATCH[4]}; break; fi
done < "$overlay"

backup=$(mktemp); trap 'rm -f "$backup"' EXIT
cat "$overlay" > "$backup"
# Rewrites the overlay in place (cat >, so the file keeps its mode), through awk with the
# reason in the environment: -v would interpret backslashes.
rewrite() { # <awk program>
  local out; out=$(mktemp)
  HOLD_LINE=${line:-} HOLD_GROUP=$group awk "$1" "$backup" > "$out"
  cat "$out" > "$overlay"; rm -f "$out"
}

if [ "$cmd" = add ]; then
  reason=$3
  # Trim; one line; no control characters (a hold is a comment in a YAML file).
  reason=${reason#"${reason%%[![:space:]]*}"}; reason=${reason%"${reason##*[![:space:]]}"}
  [ -n "$reason" ] || { echo "hold: a reason is required (what is held and where the fix is tracked)" >&2; exit 2; }
  # Printable ASCII only: U+2028, U+2029, U+0085 and CR are line breaks to YAML (they would end
  # the comment and let the rest of the "reason" be an `images:` entry) but not to the checks.
  if printf '%s' "$reason" | LC_ALL=C grep -aq '[^ -~]' || [ "$(printf '%s' "$reason" | wc -l | tr -d ' ')" != 0 ]; then
    echo "hold: the reason must be one line of printable ASCII (no accents, no line breaks)" >&2; exit 2
  fi
  # Reasons are public: they land in the overlay, in deploy PR bodies and in run summaries.
  case "$reason" in *@*) echo "hold: no @ in a reason (it would ping or link an account); name an issue number, nothing personal" >&2; exit 2 ;; esac
  if printf '%s' "$reason" | grep -Eiq '(^|[^[:alnum:]])(close[sd]?|fix(es|ed)?|resolve[sd]?)[^[:alnum:]]+([[:alnum:]_.-]+/[[:alnum:]_.-]+)?#[0-9]+'; then
    echo "hold: a reason must not say 'fixes #N' (or close/resolve): GitHub would close the issue when the deploy PR merges. Write 'see #N'." >&2; exit 2
  fi
  if [ -n "$existing" ]; then
    if [ "${existing#*$'\t'}" = "$reason" ]; then
      echo "hold: $group is already held since ${existing%%$'\t'*} with this reason: nothing changed"
      exit 0
    fi
    echo "hold: $group is already held since ${existing%%$'\t'*}: \"${existing#*$'\t'}\". Release it first (scripts/hold.sh rm $group) if the reason really changed." >&2
    exit 1
  fi
  first=ingest; [ "$group" = web ] && first=web
  if [ "$group" = services ]; then
    pins=$(./scripts/deploy_targets.sh --pins | grep -E '^(ingest|scoring|api|notify|migrate)=' | cut -d= -f2 | sort -u)
    if [ "$(printf '%s\n' "$pins" | wc -l | tr -d ' ')" != 1 ] || [ -z "$pins" ]; then
      echo "hold: the five services images are not pinned to one commit ($(printf '%s' "$pins" | paste -sd, -)): a hold would freeze a mixed state. Set them all with scripts/pin_images.sh <sha> services first." >&2; exit 1
    fi
  fi
  date=${TODAY:-$(date -u +%Y-%m-%d)}
  line="  # hold: $group $date $reason"
  grep -q "^  - name: ghcr.io/timkrebs/ryft/$first\$" "$overlay" || { echo "hold: no '- name: ghcr.io/timkrebs/ryft/$first' entry in $overlay" >&2; exit 1; }
  HOLD_FIRST="  - name: ghcr.io/timkrebs/ryft/$first" rewrite '
    $0 == ENVIRON["HOLD_FIRST"] && !done { print ENVIRON["HOLD_LINE"]; done = 1 }
    { print }'
  verb="held"
else
  if [ -z "$existing" ]; then echo "hold: $group is not held: nothing changed"; exit 0; fi
  line=
  rewrite '
    /^[ \t]*#[ \t]hold:[ \t]/ { s = $0; sub(/^[ \t]*#[ \t]hold:[ \t]+/, "", s); split(s, w, /[ \t]+/); if (w[1] == ENVIRON["HOLD_GROUP"]) next }
    { print }'
  verb="released"
fi

if ! ./scripts/deploy_targets.sh --check; then
  cat "$backup" > "$overlay"
  echo "hold: deploy_targets.sh --check rejects the result: $overlay is back as it was" >&2
  exit 1
fi
echo "hold: $group $verb in $overlay$([ "$cmd" = add ] && printf ':\n%s' "$line")"
echo "hold: commit it with the pin change (rollback) or on its own (release); see docs/OPERATIONS.md → Deploy and roll back"
