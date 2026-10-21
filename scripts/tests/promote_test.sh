#!/usr/bin/env bash
# promote.sh in throwaway repositories with a fake gh and a local remote (make test-scripts).
# Pins only the groups that differ from their running pin, in one PR; closes every older
# deploy/* PR; checks the registry before any branch exists; respects the hold; the merged
# pin commit and docs-only pushes do nothing (D107).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ promote_test: $*" >&2; exit 1; }
mkdir -p "$tmp/bin"
cat > "$tmp/bin/gh" <<'G'
#!/bin/sh
echo "gh $*" >> "$GH_LOG"
case "$1 $2" in
  "api /users/"*) echo 4242 ;;
  # The open PRs as GitHub lists them, filtered by the caller's own --jq.
  "pr list")
    jq_expr=""; prev=""
    for a in "$@"; do [ "$prev" = "--jq" ] && jq_expr=$a; prev=$a; done
    [ -n "${GH_OPEN_PRS:-}" ] && printf '%s' "$GH_OPEN_PRS" | jq -r "$jq_expr" ;;
  "pr create")
    prev=""; for a in "$@"; do [ "$prev" = "--body-file" ] && cp "$a" "$GH_BODY"; prev=$a; done ;;
esac
exit 0
G
# The registry check: records the refs, fails when CHECK_FAIL is set.
cat > "$tmp/check.sh" <<'G'
#!/bin/sh
printf '%s\n' "$@" >> "$CHECK_LOG"
[ -z "${CHECK_FAIL:-}" ]
G
chmod +x "$tmp/bin/gh" "$tmp/check.sh"
export GH_LOG="$tmp/gh.log" GH_BODY="$tmp/body.md" CHECK_LOG="$tmp/check.log"
reset_logs() { : > "$GH_LOG"; : > "$CHECK_LOG"; rm -f "$GH_BODY"; }
run() { PATH="$tmp/bin:$PATH" GH_TOKEN=t SLUG=ryft-deploy CHECK_REGISTRY="$tmp/check.sh" ./scripts/promote.sh; }
pr_file() { git show "origin/deploy/$1:$OVERLAY"; }
changed_pins() { # <branch-sha>: how many newTag lines the PR changes, against main
  git diff --numstat main "origin/deploy/$1" -- "$OVERLAY" | cut -f1
}
no_branch() { [ -z "$(git ls-remote --heads origin 'deploy/*')" ] && ! git rev-parse -q --verify "refs/heads/deploy/$1" >/dev/null; }

# 1. docs only: no group differs, no gh call, no registry call
new_repo docs; reset_logs
commit_file README.md notes "docs: notes"
out=$(run) && echo "$out" | grep -q 'no group differs' || fail "a docs-only change must not deploy: $out"
[ ! -s "$GH_LOG" ] && [ ! -s "$CHECK_LOG" ] || fail "no gh or registry call without a deploy: $(cat "$GH_LOG")"

# 2. web only: web moves, the service lines are byte-identical, one registry ref, the bot commits
new_repo web-only; reset_logs
commit_file web/index.txt v2 "web: a change"; sha=$(short)
GH_OPEN_PRS='[{"number":3,"headRefName":"feature/x"}]' run >/dev/null || fail "promote failed"
[ "$(changed_pins "$sha")" = 1 ] || fail "only web's newTag may change (changed: $(changed_pins "$sha"))"
pr_file "$sha" | grep -A1 'ryft/web$' | grep -q "newTag: \"$sha\"" || fail "web is not pinned to $sha"
[ "$(pr_file "$sha" | grep -c "$first")" = 5 ] || fail "the five service pins must stay at $first"
[ "$(cat "$CHECK_LOG")" = "ghcr.io/timkrebs/ryft/web:$sha" ] || fail "registry refs: $(cat "$CHECK_LOG")"
[ "$(git log -1 --format='%an <%ae> %s' "origin/deploy/$sha")" = "ryft-deploy[bot] <4242+ryft-deploy[bot]@users.noreply.github.com> deploy: images $sha" ] \
  || fail "the pin commit is not the bot's: $(git log -1 --format='%an <%ae> %s' "origin/deploy/$sha")"
grep -q "gh pr create --base main --head deploy/$sha --title deploy: images $sha" "$GH_LOG" || fail "no PR: $(cat "$GH_LOG")"
grep -q "gh pr merge deploy/$sha --auto --squash" "$GH_LOG" || fail "no auto-merge"
! grep -q 'gh pr close' "$GH_LOG" || fail "a non-deploy PR must stay open"
# the body: per group, old → new, why, the commits under that group's directory
grep -q "^### web: \`$first\` → \`$sha\` (web/ changed since the pin)" "$GH_BODY" || fail "body, web: $(cat "$GH_BODY")"
grep -q "^- .* web: a change" "$GH_BODY" || fail "body lacks web's commit"
grep -q "^### services: unchanged at \`$first\`" "$GH_BODY" || fail "body, services: $(cat "$GH_BODY")"

# 3. both groups: all six move, services list their commits, web its own
new_repo both; reset_logs
commit_file backend/main.go v2 "scoring: a change"; commit_file web/index.txt v2 "web: a change"; sha=$(short)
run >/dev/null || fail "promote failed"
[ "$(changed_pins "$sha")" = 6 ] || fail "all six pins must change"
[ "$(sort "$CHECK_LOG" | wc -l | tr -d ' ')" = 6 ] || fail "six registry refs: $(cat "$CHECK_LOG")"
awk '/^### services/,/^### web/' "$GH_BODY" | grep -q 'scoring: a change' || fail "the services' commits"
awk '/^### services/,/^### web/' "$GH_BODY" | grep -q 'web: a change' && fail "web's commit listed under services"
awk '/^### web/,0' "$GH_BODY" | grep -q 'web: a change' || fail "the web commit"

# 4. web push A not merged, then backend push B: B pins services AND web, A's PR is closed
new_repo superseded; reset_logs
commit_file web/index.txt v2 "web: a change"; a=$(short)
run >/dev/null || fail "promote of A failed"; git switch -q main
commit_file backend/main.go v2 "scoring: a change"; b=$(short)
: > "$GH_LOG"
GH_OPEN_PRS='[{"number":5,"headRefName":"deploy/'"$a"'"},{"number":9,"headRefName":"deploy/web-f437747"},{"number":3,"headRefName":"feature/x"}]' run >/dev/null || fail "promote of B failed"
[ "$(changed_pins "$b")" = 6 ] || fail "B must pin services and web (A was never merged): $(pr_file "$b" | grep newTag)"
grep -q "gh pr close 5 --delete-branch" "$GH_LOG" || fail "A's PR is not closed"
grep -q "gh pr close 9 --delete-branch" "$GH_LOG" || fail "every deploy/* PR is closed, deploy/web-* too"
! grep -q "gh pr close 3 " "$GH_LOG" || fail "a PR that is no deploy PR must stay open"

# 5. the merged pin commit: pinned already, nothing to do (and no registry call)
new_repo merged; reset_logs
commit_file backend/main.go v2 "scoring: a change"; commit_file web/index.txt v2 "web: a change"; sha=$(short)
run >/dev/null; git switch -q main; git merge -q --squash "deploy/$sha" >/dev/null && git commit -qm "deploy: images $sha (#8)"
reset_logs
out=$(run) && echo "$out" | grep -q 'no group differs' || fail "the pin commit must not promote itself: $out"
[ ! -s "$GH_LOG" ] || fail "gh called for the pin commit"

# 6. the running web pin is a ryft-web SHA (not in this history): web is pinned, services are not
new_repo unknown-web; ./scripts/pin_images.sh f437747 web "$OVERLAY" >/dev/null; git commit -qam "deploy: web f437747"; reset_logs
commit_file README.md notes "docs: notes"; sha=$(short)
run >/dev/null || fail "promote failed"
[ "$(changed_pins "$sha")" = 1 ] || fail "only web moves"
pr_file "$sha" | grep -A1 'ryft/web$' | grep -q "newTag: \"$sha\"" || fail "web is not pinned"
grep -q "^### web: \`f437747\` → \`$sha\` (pin not in this history)" "$GH_BODY" || fail "body: $(cat "$GH_BODY")"
grep -q 'not one commit of this history' "$GH_BODY" || fail "the body must say the old pin is unknown"

# 7. the hold: no PR, no registry call, the warning is printed
new_repo hold; commit_file web/index.txt v2 "web: a change"; hold web "broken AASA"; reset_logs
out=$(run 2>&1) || fail "a held group is no error"
echo "$out" | grep -q '::warning.*web held (broken AASA)' || fail "no warning: $out"
[ ! -s "$GH_LOG" ] && [ ! -s "$CHECK_LOG" ] && no_branch x || fail "a held group must not open a PR or push a branch: $(cat "$GH_LOG")"
# ...held web does not hold back the services
commit_file backend/main.go v2 "scoring: a change"; sha=$(short)
run >/dev/null 2>&1 || fail "services promote failed"
[ "$(changed_pins "$sha")" = 5 ] || fail "only the five services move while web is held"
git switch -q main
# ...the same revert WITHOUT the hold would pin web again; with the hold nothing; released: pinned
new_repo rollback; commit_file web/index.txt v2 "web: good"; w1=$(short); pin web "$w1"; d1=$(short)
commit_file web/index.txt v3 "web: bad"; pin web "$(short)"; d2=$(short)
git revert --no-edit "$d2" >/dev/null; reset_logs
sha=$(short); run >/dev/null 2>&1 || fail "promote after the revert failed"
pr_file "$sha" | grep -A1 'ryft/web$' | grep -q "newTag: \"$sha\"" || fail "without a hold the revert re-pins web (documented behaviour)"
git switch -q main; git push -q origin --delete "deploy/$sha"; git branch -q -D "deploy/$sha" 2>/dev/null || true
hold web "rolled back"; reset_logs
run >/dev/null 2>&1 || fail "held promote failed"
[ ! -s "$GH_LOG" ] || fail "the hold must stop the promote: $(cat "$GH_LOG")"
unhold; sha=$(short); reset_logs
run >/dev/null 2>&1 || fail "release promote failed"
pr_file "$sha" | grep -A1 'ryft/web$' | grep -q "newTag: \"$sha\"" || fail "removing the hold pins web to HEAD"

# 8. service pins that disagree: all five move although backend/ did not change
new_repo disagree; hand_pin ingest 1234567; reset_logs; sha=$(short)
run >/dev/null || fail "promote failed"
[ "$(pr_file "$sha" | grep -c "newTag: \"$sha\"")" = 5 ] || fail "all five services must move: $(pr_file "$sha" | grep newTag)"
pr_file "$sha" | grep -A1 'ryft/web$' | grep -q "newTag: \"$first\"" || fail "web must stay"
grep -q 'service pins disagree' "$GH_BODY" || fail "the body gives the reason"

# 9. registry failure for a targeted group: non-zero, and no branch, no PR, no gh call at all
new_repo registry; commit_file web/index.txt v2 "web: a change"; sha=$(short); reset_logs
CHECK_FAIL=1 run >/dev/null 2>&1 && fail "a missing image must fail the promote"
no_branch "$sha" || fail "a branch exists after a registry failure"
[ ! -s "$GH_LOG" ] || fail "gh was called before the registry check passed: $(cat "$GH_LOG")"

# 10. the tag is HEAD cut to 7, also where `rev-parse --short=7` would answer with 8 characters
real_git=$(command -v git)
cat > "$tmp/bin/git" <<G
#!/bin/sh
# ambiguity simulated: --short=7 grows to 8 characters
case "\$*" in *--short*) "$real_git" "\$@" | sed 's/\$/0/'; exit 0 ;; esac
exec "$real_git" "\$@"
G
chmod +x "$tmp/bin/git"
new_repo cut; commit_file web/index.txt v2 "web: a change"; sha=$(short); reset_logs
run >/dev/null || fail "promote failed with the ambiguity shim"
[ "${#sha}" = 7 ] && pr_file "$sha" | grep -q "newTag: \"$sha\"" || fail "the tag must be the first 7 characters of HEAD"
rm "$tmp/bin/git"

# 11. re-runs, never a force-push
# 11a. the PR of the attempt before is open: reported, auto-merge asserted, older deploy PRs closed, no push
new_repo rerun-open; commit_file web/index.txt v2 "web: a change"; sha=$(short); run >/dev/null; git switch -q main
tip=$(git rev-parse "origin/deploy/$sha"); git branch -q -D "deploy/$sha"; reset_logs
out=$(GH_OPEN_PRS='[{"number":5,"headRefName":"deploy/'"$sha"'"},{"number":4,"headRefName":"deploy/abc1234"},{"number":3,"headRefName":"feature/x"}]' run) || fail "re-run with an open PR failed"
echo "$out" | grep -q 'already open: #5' || fail "no 'already open': $out"
! grep -q 'gh pr create' "$GH_LOG" && ! grep -q 'gh pr close 5 ' "$GH_LOG" && ! grep -q 'gh pr close 3 ' "$GH_LOG" || fail "a re-run must keep its PR: $(cat "$GH_LOG")"
grep -q 'gh pr close 4 --delete-branch' "$GH_LOG" || fail "older deploy PRs are still closed"
grep -q "gh pr merge deploy/$sha --auto --squash" "$GH_LOG" || fail "auto-merge is asserted again"
[ "$(git ls-remote origin "refs/heads/deploy/$sha" | cut -f1)" = "$tip" ] || fail "the remote branch was touched"
# 11b. the branch exists, no open PR, same pins: the PR is opened from it, the branch is not pushed again
new_repo rerun-branch; commit_file web/index.txt v2 "web: a change"; sha=$(short); run >/dev/null; git switch -q main
tip=$(git rev-parse "origin/deploy/$sha"); git branch -q -D "deploy/$sha"; reset_logs
run >/dev/null || fail "re-run with an identical branch failed"
grep -q "gh pr create --base main --head deploy/$sha" "$GH_LOG" || fail "no PR from the existing branch: $(cat "$GH_LOG")"
grep -q "gh pr merge deploy/$sha --auto --squash" "$GH_LOG" || fail "no auto-merge"
[ "$(git ls-remote origin "refs/heads/deploy/$sha" | cut -f1)" = "$tip" ] || fail "the existing branch must stay as it is"
# 11c. the branch exists with other pins: exit 1, names the branch and the fix, nothing created
new_repo rerun-differs; commit_file web/index.txt v2 "web: a change"; sha=$(short); run >/dev/null; git switch -q main
git switch -q -c scratch "origin/deploy/$sha"; ./scripts/pin_images.sh 1234567 web "$OVERLAY" >/dev/null; git commit -qam "other pins"; git push -q origin "scratch:deploy/$sha"
tip=$(git rev-parse HEAD); git switch -q main; git branch -q -D "deploy/$sha" scratch; reset_logs
out=$(run 2>&1) && fail "a branch with other pins must fail the job"
echo "$out" | grep -q "git push origin --delete deploy/$sha" || fail "the message must name the fix: $out"
! grep -q 'gh pr create' "$GH_LOG" || fail "no PR from a branch with other pins"
[ "$(git ls-remote origin "refs/heads/deploy/$sha" | cut -f1)" = "$tip" ] || fail "the remote branch must not be overwritten"

echo "✓ promote_test: per-group pins in one PR, superseded PRs closed, docs/pin commits/holds skipped, registry first, 7-char tag"
