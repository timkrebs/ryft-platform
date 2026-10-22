#!/usr/bin/env bash
# The daily deploy watch (.github/workflows/deploy-watch.yml; docs/OPERATIONS.md → Deploy
# and roll back → The deploy watch). Read-only: it looks at the overlay, at git and at the
# open pull requests, and exits 1 with findings in $GITHUB_STEP_SUMMARY when
#   a. a `# hold:` line in deploy/k8s/overlays/eks/kustomization.yaml is older than
#      HOLD_MAX_DAYS (a hold is a temporary rollback; a forgotten one stops a group for good);
#   b. a pull request of the deploy App from deploy/<7 hex> has been open longer than
#      DEPLOY_PR_MAX_HOURS (it merges itself once its checks pass: it is stuck);
#   c. scripts/deploy_targets.sh targets a group (its directory differs from its running pin,
#      not held), main's tip is older than DEPLOY_PR_MAX_HOURS and no deploy PR is open:
#      promote failed, was skipped (no App variable, an environment without its secret) or
#      the PR was closed on purpose.
# The diff is deploy_targets.sh's own: run in a checkout of an up-to-date main with the whole
# history (a shallow clone exits 2). A hold dated in the future is a finding too (a typo).
# c. reads the committer date of main's tip, not the last CI run: no Actions permission needed,
# and the same answer in a test repository.
# Exit 0: nothing to report. Exit 1: findings. Exit 2: the watch itself could not run.
#   HOLD_MAX_DAYS         default 7
#   DEPLOY_PR_MAX_HOURS   default 2
#   DEPLOY_APP_SLUG       default ryft-deploy (the App's PRs show as author app/<slug>)
#   GH_TOKEN, GH_REPO     for `gh pr list` (pull-requests: read is enough)
#   NOW, TODAY            tests only: epoch seconds, YYYY-MM-DD (UTC)
# bash 3.2-safe (macOS).
set -euo pipefail
cd "$(dirname "$0")/.."
overlay=deploy/k8s/overlays/eks/kustomization.yaml
hold_max_days=${HOLD_MAX_DAYS:-7}
pr_max_hours=${DEPLOY_PR_MAX_HOURS:-2}
slug=${DEPLOY_APP_SLUG:-ryft-deploy}
now=${NOW:-$(date -u +%s)}
today=${TODAY:-$(date -u +%Y-%m-%d)}
for v in "HOLD_MAX_DAYS=$hold_max_days" "DEPLOY_PR_MAX_HOURS=$pr_max_hours" "NOW=$now"; do
  case "${v#*=}" in ""|*[!0-9]*) echo "deploy_watch: ${v%%=*} must be a whole number, not '${v#*=}'" >&2; exit 2 ;; esac
done
# Whole numbers, leading zeros included ("08" is not octal).
hold_max_days=$((10#$hold_max_days)); pr_max_hours=$((10#$pr_max_hours)); now=$((10#$now))
case "$slug" in *[!a-z0-9-]*|"") echo "deploy_watch: odd DEPLOY_APP_SLUG '$slug'" >&2; exit 2 ;; esac
[[ $today =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || { echo "deploy_watch: TODAY must be YYYY-MM-DD, not '$today'" >&2; exit 2; }
if [ "$(git rev-parse --is-shallow-repository)" != false ]; then
  echo "deploy_watch: this is a shallow clone; deploy_targets.sh needs the whole history (checkout with fetch-depth: 0)" >&2; exit 2
fi

# Days since 1970-01-01 of a civil date (no date(1): GNU and BSD differ).
daynum() { # <year> <month> <day>
  local y=$((10#$1)) m=$((10#$2)) d=$((10#$3)) era yoe doy doe
  [ "$m" -gt 2 ] || y=$((y - 1))
  era=$((y / 400)); yoe=$((y - era * 400))
  doy=$(((153 * (m > 2 ? m - 3 : m + 9) + 2) / 5 + d - 1))
  doe=$((yoe * 365 + yoe / 4 - yoe / 100 + doy))
  echo $((era * 146097 + doe - 719468))
}
# Epoch seconds of GitHub's 2026-10-22T06:10:23Z (a fraction of a second is tolerated); returns
# 1 on anything else. Never `exit` in here: it runs in a command substitution.
iso_epoch() {
  local re='^([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})(\.[0-9]+)?Z$'
  [[ $1 =~ $re ]] || return 1
  echo $(($(daynum "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}") * 86400 + 10#${BASH_REMATCH[4]} * 3600 + 10#${BASH_REMATCH[5]} * 60 + 10#${BASH_REMATCH[6]}))
}

findings=; nfind=0
find_() { findings="$findings- $1"$'\n'; nfind=$((nfind + 1)); echo "::error title=deploy watch::$1" >&2; }

# --- deploy_targets.sh: validates the holds too (exit 2 on one that does not parse) ----------
errf=$(mktemp); trap 'rm -f "$errf"' EXIT
targets_ok=1; targets=
if ! targets=$(FORCE=none ./scripts/deploy_targets.sh 2>"$errf"); then
  targets_ok=
  find_ "scripts/deploy_targets.sh failed ($(head -c 300 "$errf" | tr '\n' ' ')): fix the overlay (a hold that does not parse, a byte that is not printable ASCII) before anything else."
fi
cat "$errf" >&2
val() { printf '%s\n' "$targets" | sed -n "s/^$1=//p" | head -1; }

# --- a. holds older than HOLD_MAX_DAYS -------------------------------------------------------
holds=0
if [ -n "$targets_ok" ]; then
  hold_re='^[[:space:]]*#[[:space:]]hold:[[:space:]](services|web)[[:space:]]([0-9]{4})-([0-9]{2})-([0-9]{2})[[:space:]]+([^[:space:]].*)$'
  t_today=$(daynum "${today:0:4}" "${today:5:2}" "${today:8:2}")
  while IFS= read -r line; do
    [[ $line =~ $hold_re ]] || continue
    holds=$((holds + 1))
    g=${BASH_REMATCH[1]}; reason=${BASH_REMATCH[5]}
    age=$((t_today - $(daynum "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}" "${BASH_REMATCH[4]}")))
    if [ "$age" -lt 0 ]; then
      find_ "$g is held since ${BASH_REMATCH[2]}-${BASH_REMATCH[3]}-${BASH_REMATCH[4]}, which is $((-age)) day(s) in the future (today is $today UTC): \"$reason\". A typo in the date keeps the age check from ever firing; fix the date."
    elif [ "$age" -gt "$hold_max_days" ]; then
      find_ "$g has been held for $age days (since ${BASH_REMATCH[2]}-${BASH_REMATCH[3]}-${BASH_REMATCH[4]}, limit $hold_max_days): \"$reason\". Ship the fix and release it (scripts/hold.sh rm $g in a PR), or renew it on purpose."
    fi
  done < "$overlay"
fi

# --- b. deploy PRs open for too long ---------------------------------------------------------
# "<number> <branch> <createdAt> <author>" of the same-repository PRs from deploy/<7 hex>
# (a fork's is none of ours). The App's own count; anyone else's with that branch name is
# a warning (the App renamed? a person using the reserved name?) and does not count as a deploy.
expr='.[] | select(.isCrossRepository == false and (.headRefName | test("^deploy/[0-9a-f]{7}$"))) | "\(.number) \(.headRefName) \(.createdAt) \(.author.login)"'
open_n=0; warned=
if list=$(gh pr list --state open --limit 200 --json number,headRefName,isCrossRepository,author,createdAt --jq "$expr" 2>"$errf"); then
  while read -r n b created who; do
    [ -n "${n:-}" ] || continue
    if [ "$who" != "app/$slug" ]; then
      warned="$warned#$n ($b by $who) "
      echo "::warning title=deploy watch::#$n is from $b but not by app/$slug (by $who): not counted as a deploy PR. Did the App get renamed (DEPLOY_APP_SLUG)?" >&2
      continue
    fi
    open_n=$((open_n + 1))
    if ! e=$(iso_epoch "$created"); then
      find_ "#$n ($b) has a creation time I cannot read ('$created'): the age check cannot run for it."
      continue
    fi
    age=$((now - e))
    if [ "$age" -gt $((pr_max_hours * 3600)) ]; then
      find_ "#$n ($b) has been open for $((age / 60)) min (limit $pr_max_hours h): it should have merged itself. Look at its checks, re-run or fix them, or close it and hold the group (scripts/hold.sh add <group> \"<reason>\") to stop the deploy."
    fi
  done <<EOT
$list
EOT
else
  find_ "could not list the open pull requests ($(head -c 200 "$errf" | tr '\n' ' ')): the token needs pull-requests: read."
  open_n=-1   # unknown: c. cannot say that no PR is open
fi

# --- c. a group is targeted, main is old, and no deploy PR is open ----------------------------
if [ -n "$targets_ok" ] && [ "$open_n" = 0 ]; then
  tip=$(git log -1 --format=%ct HEAD)
  tip_age=$((now - tip))
  if [ "$tip_age" -gt $((pr_max_hours * 3600)) ]; then
    for g in services web; do
      if [ "$(val "$g")" = true ]; then
        find_ "$g differs from its pin ($(val "${g}_pin"): $(val "${g}_why")), main's tip $(git rev-parse --short=7 HEAD) is $((tip_age / 3600)) h old and no deploy PR is open: promote failed, never ran or its PR was closed. Check the latest 'ci' run on main (re-run failed jobs), or hold the group on purpose (scripts/hold.sh add $g \"<reason>\")."
      fi
    done
  fi
fi

# --- report ------------------------------------------------------------------------------------
{
  echo "### Deploy watch"
  if [ "$nfind" = 0 ]; then
    echo "OK: $holds hold(s) within $hold_max_days days, $( [ "$open_n" -ge 0 ] && echo "$open_n" || echo "?" ) deploy PR(s) open (limit $pr_max_hours h)$( [ -n "$targets_ok" ] && echo ", services=$(val services) web=$(val web)" )."
  else
    echo "$nfind finding(s) (docs/OPERATIONS.md → Deploy and roll back → The deploy watch):"
    printf '%s' "$findings"
  fi
  [ -z "$warned" ] || echo "Warning: PRs from a deploy/<sha> branch by someone other than app/$slug (not counted): $warned"
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
[ "$nfind" = 0 ]
