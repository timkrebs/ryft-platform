#!/usr/bin/env bash
# shellcheck disable=SC2154,SC1091,SC2015,SC2016
# scripts/deploy_watch.sh in throwaway repositories with a fake gh (make test-scripts): a hold
# older than HOLD_MAX_DAYS, a deploy PR open too long and a targeted group on an old main with
# no deploy PR fail it; everything else passes.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ deploy_watch_test: $*" >&2; exit 1; }
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'G'
#!/bin/sh
[ -z "${GH_FAIL_LIST:-}" ] || { echo "HTTP 403: Resource not accessible" >&2; exit 1; }
[ "$1 $2" = "pr list" ] || exit 0
jq_expr=""; prev=""
for a in "$@"; do [ "$prev" = "--jq" ] && jq_expr=$a; prev=$a; done
[ -n "${GH_OPEN_PRS:-}" ] && printf '%s' "$GH_OPEN_PRS" | jq -r "$jq_expr"
exit 0
G
chmod +x "$tmp/bin/gh"
iso() { python3 -c 'import sys,time;print(time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime(int(sys.argv[1]))))' "$1"; }
epoch() { python3 -c 'import sys,calendar,time;print(calendar.timegm(time.strptime(sys.argv[1],"%Y-%m-%d %H:%M")))' "$1"; }
H=3600
# pr <number> <branch> <age-in-seconds> [fork] [author]
pr() { printf '{"number":%s,"headRefName":"%s","createdAt":"%s","isCrossRepository":%s,"author":{"login":"%s"}}' "$1" "$2" "$(iso $((NOW - $3)))" "${4:-false}" "${5:-app/ryft-deploy}"; }
prs() { local o="["; local sep=; for x in "$@"; do o="$o$sep$x"; sep=,; done; printf '%s]' "$o"; }
# watch: runs the script at NOW/TODAY; sets rc and the summary in $tmp/summary.
NOW=$(epoch "2026-10-22 12:00"); TODAY=2026-10-22
watch() {
  : > "$tmp/summary"; rc=0
  PATH="$tmp/bin:$PATH" NOW=$NOW TODAY=$TODAY GITHUB_STEP_SUMMARY="$tmp/summary" ./scripts/deploy_watch.sh >"$tmp/out" 2>"$tmp/err" || rc=$?
}
# repo <name>: a repository whose main tip is `age` seconds old (set before the call: TIP_AGE).
repo() {
  new_repo "$1"; cp "$HERE"/scripts/{deploy_watch,hold}.sh scripts/
  GIT_COMMITTER_DATE="$(iso $((NOW - ${TIP_AGE:-60})))" git commit -q --amend --no-edit --date "$(iso $((NOW - ${TIP_AGE:-60})))"
  : "${first:=}"
}
ok() { watch; [ "$rc" = 0 ] || fail "$1: want exit 0, got $rc: $(cat "$tmp/summary") $(cat "$tmp/err")"; }
bad() { watch; [ "$rc" = 1 ] || fail "$1: want exit 1, got $rc: $(cat "$tmp/out") $(cat "$tmp/err")"; grep -q "$2" "$tmp/summary" || fail "$1: summary lacks '$2': $(cat "$tmp/summary")"; }

# Quiet: no hold, no PR, nothing differs.
repo quiet
ok "a quiet repository"; grep -q '^OK' "$tmp/summary" || fail "no OK in the summary"

# a. holds: today passes, 7 days passes, 8 days fails (both groups), HOLD_MAX_DAYS is honoured.
repo hold-today; TODAY=2026-10-22 ./scripts/hold.sh add web "fresh" >/dev/null; git commit -qam hold
ok "a hold dated today"
repo hold-7; TODAY=2026-10-15 ./scripts/hold.sh add web "a week" >/dev/null; git commit -qam hold
ok "a hold dated 7 days back"
repo hold-8; TODAY=2026-10-14 ./scripts/hold.sh add web "too old" >/dev/null; git commit -qam hold
bad "a hold dated 8 days back" 'web has been held for 8 days'
grep -q '::error' "$tmp/err" || fail "no error annotation"
HOLD_MAX_DAYS=10 ok "HOLD_MAX_DAYS=10 allows 8 days"
repo hold-svc; TODAY=2026-09-01 ./scripts/hold.sh add services "ancient" >/dev/null; git commit -qam hold
bad "an old services hold" 'services has been held for 51 days'
# across a month and a year boundary
repo hold-year; TODAY=2025-12-30 ./scripts/hold.sh add web "x" >/dev/null; git commit -qam hold
TODAY=2026-01-06 NOW=$(epoch "2026-01-06 12:00") ok "7 days over New Year"
TODAY=2026-01-07 NOW=$(epoch "2026-01-07 12:00") bad "8 days over New Year" 'held for 8 days'
# A hold that does not parse stops deploy_targets.sh: the watch says so.
repo hold-typo; awk '/ryft\/web$/ { print "  # hold: web x" } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam typo
bad "a hold that does not parse" 'deploy_targets.sh failed'

# b. deploy PRs: 1 h is fine, 3 h is not; other authors, forks and other branches are ignored.
repo prs
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((1 * H)))") ok "a 1 h old deploy PR"
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((3 * H)))") bad "a 3 h old deploy PR" '#5 (deploy/abcdef0) has been open for 180 min'
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((3 * H)))") DEPLOY_PR_MAX_HOURS=4 ok "DEPLOY_PR_MAX_HOURS=4 allows 3 h"
GH_OPEN_PRS=$(prs "$(pr 6 deploy/abcdef0 $((9 * H)) true)" "$(pr 7 deploy/abcdef0 $((9 * H)) false alice)" "$(pr 8 deploy/rollback-web $((9 * H)))" "$(pr 9 feature/x $((9 * H)))" "$(pr 10 deploy/abcdef01 $((9 * H)))") ok "PRs that are not the App's deploy PRs"
# c. a targeted group on an old main with no deploy PR; fresh main, an open PR, or a hold excuse it.
TIP_AGE=$((3 * H)) repo stuck; commit_file web/index.txt v2 "web: a change"
GIT_COMMITTER_DATE="$(iso $((NOW - 3 * H)))" git commit -q --amend --no-edit --date "$(iso $((NOW - 3 * H)))"
bad "web targeted, main 3 h old, no PR" 'web differs from its pin'
grep -q "promote failed" "$tmp/summary" || fail "no hint what it means"
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((1 * H)))") ok "targeted, but a fresh deploy PR is open"
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((5 * H)))") bad "targeted, a stuck PR" 'has been open for 300 min'
if grep -q 'web differs from its pin' "$tmp/summary"; then fail "an open PR means c. stays quiet"; fi
GIT_COMMITTER_DATE="$(iso $((NOW - 1 * H)))" git commit -q --amend --no-edit --date "$(iso $((NOW - 1 * H)))"
ok "targeted, but main's tip is only 1 h old"
GIT_COMMITTER_DATE="$(iso $((NOW - 3 * H)))" git commit -q --amend --no-edit --date "$(iso $((NOW - 3 * H)))"
TODAY=2026-10-20 ./scripts/hold.sh add web "on purpose" >/dev/null; GIT_COMMITTER_DATE="$(iso $((NOW - 3 * H)))" git commit -qam hold --date "$(iso $((NOW - 3 * H)))"
ok "targeted but held (2 days)"
TIP_AGE=$((3 * H)) repo stuck-services; commit_file backend/main.go v2 "scoring: a change"
GIT_COMMITTER_DATE="$(iso $((NOW - 3 * H)))" git commit -q --amend --no-edit --date "$(iso $((NOW - 3 * H)))"
bad "services targeted, main old" 'services differs from its pin'
# The merged pin commit targets nothing, however old.
pin services "$(short)"; GIT_COMMITTER_DATE="$(iso $((NOW - 3 * H)))" git commit -q --amend --no-edit --date "$(iso $((NOW - 3 * H)))"
ok "the merged pin commit"

# A hold dated in the future would never age: a finding.
repo hold-future; TODAY=2026-10-25 ./scripts/hold.sh add web "typo in the year" >/dev/null; git commit -qam hold
bad "a hold dated 3 days ahead" 'in the future'
# Leading zeros are decimal; a fraction of a second in createdAt is read; garbage is a finding
# (a silent drop of the loop was a pass once).
repo numbers
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((3 * H)))") DEPLOY_PR_MAX_HOURS=08 ok "DEPLOY_PR_MAX_HOURS=08 is eight hours"
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((9 * H)))") DEPLOY_PR_MAX_HOURS=08 bad "9 h against 08" 'has been open for 540 min'
HOLD_MAX_DAYS=08 ok "HOLD_MAX_DAYS=08"
frac=$(pr 5 deploy/abcdef0 $((3 * H)) | sed 's/Z"/.123Z"/')
GH_OPEN_PRS=$(prs "$frac") bad "createdAt with a fraction of a second" 'has been open for 180 min'
junk=$(pr 5 deploy/abcdef0 $((3 * H)) | sed 's/"createdAt":"[^"]*"/"createdAt":"yesterday"/')
GH_OPEN_PRS=$(prs "$junk") bad "an unreadable createdAt" 'cannot read'
# A PR from deploy/<7 hex> by someone else is warned about, is not a deploy PR, and does not excuse c.
TIP_AGE=$((3 * H)) repo renamed; commit_file web/index.txt v2 "web: a change"
GIT_COMMITTER_DATE="$(iso $((NOW - 3 * H)))" git commit -q --amend --no-edit --date "$(iso $((NOW - 3 * H)))"
GH_OPEN_PRS=$(prs "$(pr 5 deploy/abcdef0 $((1 * H)) false alice)") bad "a person's deploy/<sha> PR with web targeted" 'web differs from its pin'
grep -q 'not by app/ryft-deploy' "$tmp/err" || fail "no warning for the other author: $(cat "$tmp/err")"
grep -q 'Warning: PRs from a deploy' "$tmp/summary" || fail "no warning in the summary"
# A shallow clone cannot answer.
git clone -q --depth 1 "file://$PWD" "$tmp/shallow"; mkdir -p "$tmp/shallow/scripts"
cp "$HERE"/scripts/{deploy_watch,deploy_targets}.sh "$tmp/shallow/scripts/"
rc=0; (cd "$tmp/shallow" && PATH="$tmp/bin:$PATH" ./scripts/deploy_watch.sh >/dev/null 2>"$tmp/err") || rc=$?
[ "$rc" = 2 ] && grep -q 'shallow' "$tmp/err" || fail "a shallow clone must exit 2 (got $rc): $(cat "$tmp/err")"

# A failing gh is a finding, not a pass.
repo gh-fails
GH_FAIL_LIST=1 bad "gh fails" 'could not list the open pull requests'
# The watch's own settings.
repo env; rc=0; HOLD_MAX_DAYS=x PATH="$tmp/bin:$PATH" ./scripts/deploy_watch.sh >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "a bad HOLD_MAX_DAYS must exit 2 (got $rc)"
echo "✓ deploy_watch_test"
