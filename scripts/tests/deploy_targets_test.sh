#!/usr/bin/env bash
# scripts/deploy_targets.sh in throwaway repositories (make test-scripts): which image
# group is built and pinned on a push to main, against the RUNNING pin (D107).
set -euo pipefail
. "$(dirname "$0")/lib.sh"
fail() { echo "✗ deploy_targets_test: $*" >&2; exit 1; }
# got <key>: the value of a key in the last run's output.
run() { out=$(FORCE="${FORCE:-}" ./scripts/deploy_targets.sh 2>"$tmp/err"); }
got() { printf '%s\n' "$out" | sed -n "s/^$1=//p"; }
expect() { # <services> <web> <what>
  run
  [ "$(got services)" = "$1" ] || fail "$3: services=$(got services), want $1 ($(got services_why))"
  [ "$(got web)" = "$2" ] || fail "$3: web=$(got web), want $2 ($(got web_why))"
}

new_repo nothing
expect false false "everything pinned to this commit"
[ "$(got held)" = false ] || fail "held=false without a hold: $(got held)"
[ "$(got services_pin)" = "$first" ] || fail "services_pin: $(got services_pin)"
[ ! -s "$tmp/err" ] || fail "a repository without a hold must not warn: $(cat "$tmp/err")"

new_repo web-only; commit_file web/index.txt v2 "web: a change"
expect false true "a web change"
case "$(got web_why)" in *changed*) ;; *) fail "why: $(got web_why)" ;; esac

new_repo backend-only; commit_file backend/main.go v2 "scoring: a change"
expect true false "a backend change"

new_repo both; commit_file backend/main.go v2 "scoring: a change"; commit_file web/index.txt v2 "web: a change"
expect true true "both"

new_repo docs; commit_file README.md notes "docs: notes"; commit_file deploy/k8s/x.yaml a "deploy: a manifest"
expect false false "docs and manifests only"

# The merged pin commit changes neither directory: nothing to build.
new_repo pin-commit; commit_file backend/main.go v2 "scoring: a change"; commit_file web/index.txt v2 "web: a change"
pin services web "$(short)"
expect false false "the merged pin commit"

# A pin that is no commit here (ryft-web's f437747): web is different, whatever web/ holds.
new_repo unknown-web; ./scripts/pin_images.sh f437747 web "$OVERLAY" >/dev/null; git commit -qam "deploy: web f437747"
expect false true "a running web pin that is not in this history"
[ "$(got web_why)" = "pin not in this history" ] || fail "why: $(got web_why)"
[ "$(got web_pin)" = f437747 ] || fail "web_pin: $(got web_pin)"

# Service pins that disagree: services differ although backend/ did not change.
new_repo disagree; hand_pin ingest 1234567
expect true false "disagreeing service pins"
case "$(got services_why)" in *disagree*) ;; *) fail "why: $(got services_why)" ;; esac

# The hold: not targeted, a warning, while the rest of the group logic still runs.
new_repo hold; commit_file web/index.txt v2 "web: a change"; hold web "broken AASA, fix in #57"
expect false false "a held web group"
[ "$(got held)" = true ] || fail "held=true with a hold: $(got held)"
grep -q '::warning.*web held since 2026-10-23 (broken AASA, fix in #57), 1 commits under web/' "$tmp/err" || fail "no hold warning: $(cat "$tmp/err")"
new_repo hold-unknown; ./scripts/pin_images.sh f437747 web "$OVERLAY" >/dev/null; git commit -qam "deploy: web f437747"; hold web "rollback"
expect false false "a held web group whose pin is not in this history"
new_repo hold-services; commit_file backend/main.go v2 "scoring: a change"; hold services "bad migration"
expect false false "held services"
unhold; expect true false "the hold is released"
# A hold for one group does not stop the other, and a group name is matched whole.
new_repo hold-other; commit_file backend/main.go v2 "scoring: a change"; hold web "x"
expect true false "a web hold leaves services alone"
# Holds fail closed: anything that starts like a hold and does not parse stops the script (exit 2).
for bad in "# hold: webby 2026-10-23 x" "# hold: web x" "# hold: web 2026-13-01 x" "# hold: web 2026-10-23" "#hold: web 2026-10-23 x" "# Hold: web 2026-10-23 x" "# holding web 2026-10-23 x" "# HOLD: web 2026-10-23 x"; do
  new_repo bad-hold; commit_file web/index.txt v2 "web: a change"
  awk -v l="  $bad" '/ryft\/web$/ { print l } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: odd hold"
  rc=0; FORCE= ./scripts/deploy_targets.sh >"$tmp/out" 2>"$tmp/err" || rc=$?
  [ "$rc" = 2 ] || fail "'$bad' must stop with exit 2 (got $rc)"
  grep -q '::error' "$tmp/err" || fail "no annotation for '$bad'"
  [ ! -s "$tmp/out" ] || fail "no outputs when a hold does not parse"
done
# The reason is public text: a hand-edited hold line is held to the rules hold.sh applies, in every mode.
for bad in "# hold: web 2026-10-23 ask @someone" "# hold: web 2026-10-23 fixes #12" "# hold: services 2026-10-23 Closes org/repo#9" \
  "# hold: web 2026-10-23 fixes https://github.com/o/r/issues/12" "# hold: web 2026-10-23 resolved: GH-3"; do
  new_repo bad-reason; commit_file web/index.txt v2 "web: a change"
  awk -v l="  $bad" '/ryft\/web$/ { print l } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: hand-written hold"
  for m in "" --check --pins; do
    rc=0; FORCE= ./scripts/deploy_targets.sh $m >"$tmp/out" 2>"$tmp/err" || rc=$?
    [ "$rc" = 2 ] || fail "'$bad' ($m) must stop with exit 2 (got $rc)"
    grep -q '::error.*hold reason' "$tmp/err" || fail "no annotation for the reason of '$bad': $(cat "$tmp/err")"
  done
done
for ok in "# hold: web 2026-10-23 broken AASA, fix in #57" "# hold: web 2026-10-23 see https://github.com/o/r/issues/12" "# hold: web 2026-10-23 prefixes #12"; do
  new_repo ok-reason; awk -v l="  $ok" '/ryft\/web$/ { print l } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: hand-written hold"
  FORCE= ./scripts/deploy_targets.sh --check >/dev/null 2>"$tmp/err" || fail "'$ok' must pass: $(cat "$tmp/err")"
done
# --reason on its own (hold.sh calls it)
./scripts/deploy_targets.sh --reason "broken AASA, see #57" || fail "--reason: a fine reason"
rc=0; ./scripts/deploy_targets.sh --reason "fixes https://github.com/o/r/issues/12" 2>/dev/null || rc=$?; [ "$rc" = 2 ] || fail "--reason: an issue URL with a closing keyword (rc=$rc)"
rc=0; ./scripts/deploy_targets.sh --reason 2>/dev/null || rc=$?; [ "$rc" = 2 ] || fail "--reason without a text (rc=$rc)"
# The overlay as committed carries the convention in a comment but no hold of its own.
new_repo real-overlay
grep -q "main.s head for the group" "$OVERLAY" || fail "the overlay no longer documents the hold convention"

# The rollback of D107: revert a bad web pin. Without the hold the code on main is deployed again.
new_repo rollback; w1=$(commit_file web/index.txt v2 "web: good" && short); pin web "$w1"; d1=$(short)
commit_file web/index.txt v3 "web: bad"; w2=$(short); pin web "$w2"; d2=$(short)
git revert --no-edit "$d2" >/dev/null
[ "$(pins "$OVERLAY" | sed -n 's/^web=//p')" = "$w1" ] || fail "the revert did not bring back $w1"
expect false true "a revert WITHOUT a hold pins the rolled-back code again"
hold web "rolled back from $w2"
expect false false "the revert with a hold"
unhold
expect false true "the hold removed: web is pinned to main's head"

# FORCE (workflow_dispatch): rebuild a group whose source did not change; a hold still wins.
new_repo force
FORCE=web expect false true "FORCE=web"; FORCE=services expect true false "FORCE=services"
FORCE=all expect true true "FORCE=all"; FORCE=none expect false false "FORCE=none"
hold web "x"; FORCE=all expect true false "FORCE=all with a web hold"
FORCE=nonsense ./scripts/deploy_targets.sh >/dev/null 2>&1 && fail "FORCE=nonsense must be refused"

# No pin at all counts as different.
new_repo no-pin; drop_pin web
expect false true "no web pin"

# --check (CI's manifests job): the summary, annotations, and the rule that a PR may not move a held pin
chk() { rc=0; GITHUB_STEP_SUMMARY="$tmp/summary" ./scripts/deploy_targets.sh --check "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?; }
new_repo check; git update-ref refs/remotes/origin/base HEAD
chk origin/base; [ "$rc" = 0 ] || fail "check on an unchanged overlay: $(cat "$tmp/err")"
grep -q 'none' "$tmp/summary" || fail "summary without holds"
# a rollback PR moves the pin AND adds the hold, the base is not held yet: passes, announces the hold
commit_file web/index.txt v2 "web: a change"; pin web "$(short)"; git update-ref refs/remotes/origin/base HEAD
hand_pin web f437747; hold web "rolled back"
chk origin/base; [ "$rc" = 0 ] || fail "a rollback PR that holds must pass: $(cat "$tmp/err") $(cat "$tmp/out")"
grep -q '::notice.*holds web' "$tmp/out" || fail "no notice for the added hold: $(cat "$tmp/out")"
grep -q 'web\*\* held since 2026-10-23: rolled back' "$tmp/summary" || fail "summary lists the hold: $(cat "$tmp/summary")"
# now the base is held: a PR that changes the pin fails, one that only removes the hold passes
git update-ref refs/remotes/origin/base HEAD
unhold; chk origin/base; [ "$rc" = 0 ] || fail "a release PR that only removes the hold must pass: $(cat "$tmp/err")"
grep -q '::notice.*releases the hold on web' "$tmp/out" || fail "no notice for the release"
git reset -q --hard origin/base; hand_pin web 1234567
chk origin/base; [ "$rc" = 0 ] || fail "a person's PR (a second rollback) must not be blocked by the hold: $(cat "$tmp/err")"
HEAD_REF=some/branch chk origin/base; [ "$rc" = 0 ] || fail "only the App's deploy branches are blocked"
HEAD_REF=deploy/abc1234 chk origin/base; [ "$rc" = 1 ] || fail "a deploy PR moving the pin of a held group must fail (rc=$rc)"
grep -q '::error.*web is held' "$tmp/err" || fail "the error names the group: $(cat "$tmp/err")"
HEAD_REF=deploy/abc1234 chk origin/base; HEAD_REF=
# an older deploy PR for the services still passes: only the held group is protected
git reset -q --hard origin/base; ./scripts/pin_images.sh abc1234 services "$OVERLAY" >/dev/null; git commit -qam "deploy: services"
HEAD_REF=deploy/abc1234 chk origin/base; [ "$rc" = 0 ] || fail "the other group may move: $(cat "$tmp/err")"
chk; [ "$rc" = 0 ] || fail "check without a base (push)"
chk origin/nope; [ "$rc" = 1 ] || fail "a missing base must fail, not pass"

# A hold-like "#hold" anywhere on a line that is not a standalone valid hold: exit 2
for bad in 'newTag: "f437747" # hold: web 2026-10-23 x' 'newTag: "f437747"  #HOLD web' '- name: ghcr.io/timkrebs/ryft/web # hold: web 2026-10-23 x'; do
  new_repo trailing-hold
  awk -v l="$bad" '/ryft\/web$/ { print; getline; print "    " l; next } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: odd"
  rc=0; ./scripts/deploy_targets.sh >/dev/null 2>&1 || rc=$?; [ "$rc" = 2 ] || fail "'$bad' must exit 2 (got $rc)"
done

# --check: the image entries are exactly name + quoted 7-hex newTag
new_repo entries
chk; [ "$rc" = 0 ] || fail "the real entries pass: $(cat "$tmp/err")"
mutate() { # <awk program on $OVERLAY> <what>
  new_repo entries; awk "$1" "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: $2"
  chk; [ "$rc" = 1 ] || fail "$2 must fail the check (rc=$rc)"; grep -q '::error' "$tmp/err" || fail "$2: no annotation"
}
mutate '{ print } /ryft\/web$/ { getline; print; print "    digest: sha256:0000000000000000000000000000000000000000000000000000000000000000" }' "a digest line"
mutate '{ print } /ryft\/api$/ { getline; print; print "    newName: ghcr.io/evil/api" }' "a newName line"
mutate '/ryft\/web$/ { print; getline; print "    newTag: latest"; next } { print }' "a non-SHA tag"
mutate '/ryft\/web$/ { print; getline; print "    newTag: \"f437747\""; getline; print; print "  - name: ghcr.io/timkrebs/ryft/web"; print "    newName: ghcr.io/evil/web"; print "    newTag: \"f437747\""; next } { print }' "a second web entry with a newName"
mutate '/ryft\/api$/ { print "  - name: \"ghcr.io/timkrebs/ryft/api\""; next } { print }' "a quoted name"
mutate '/ryft\/web$/ { print; getline; print "    newTag: \"f4377470\""; next } { print }' "an 8-digit tag"

# --check reads the whole overlay as an allowlist: these all fail, a comment and a blank line do not.
new_repo allow; awk '{ print } /^kind:/ { print "# a harmless comment"; print "" }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: comment"
chk; [ "$rc" = 0 ] || fail "comments and blank lines pass: $(cat "$tmp/err")"
mutate '{ print } END { print "images:"; print "  - name: ghcr.io/timkrebs/ryft/web"; print "    newTag: \"f437747\"" }' "a second images key"
mutate '/^images:/ { print "\"images\":"; next } { print }' "a quoted images key"
mutate '/^images:/ { print "images :"; next } { print }' "a space before the colon of images"
mutate '/^images:/ { print "images: [{name: ghcr.io/timkrebs/ryft/web, newName: ghcr.io/evil/web, newTag: \"f437747\"}]"; skip = 1; next } skip && /^[^ #]/ { skip = 0 } skip && /^ / { next } { print }' "a flow-style images list"
mutate '{ print } /^kind:/ { print "namePrefix: evil-" }' "an unknown top-level key"
mutate '{ print } /^kind:/ { print "replacements:"; print "  - source: {kind: ConfigMap, name: x}" }' "replacements"
mutate '{ print } END { print "---"; print "images:"; print "  - name: ghcr.io/timkrebs/ryft/web"; print "    newName: ghcr.io/evil/web" }' "a second YAML document"
mutate '/^patches:/ { print; print "  - patch: |-"; print "      - op: add"; next } { print }' "an inline patch"
mutate '/^resources:/ { print; print "  - https://example.com/evil.yaml?x=1"; next } { print }' "a remote resource with a query"
mutate '/^configMapGenerator:/ { print; print "  - { name: ryft-config, namespace: ryft-api, behavior: merge, literals: [PRIVACY_POLICY_VERSION=2026-10-20, EXTRA=1] }"; next } { print }' "an extra literal"
mutate '/ryft\/notify$/ { print "  - name: docker.io/library/postgres"; print "    newTag: \"abcdef0\""; getline; next } { print }' "an image that is not ours instead of notify"
mutate '/ryft\/notify$/ { getline; next } { print }' "an entry missing"
mutate '/ryft\/notify$/ { print "  - name: ghcr.io/timkrebs/ryft/miner"; next } { print }' "an unknown ryft image"
mutate '/ryft\/api$/ { print; getline; print "    # a comment between name and tag is fine"; print; print "    newTag: \"abcdef0\""; next } { print }' "two newTag lines"

# Bytes that are line breaks to YAML but not to this script: LS, PS, NEL (UTF-8 and raw) and CR
# stop every mode with exit 2, so a hold reason cannot smuggle an `images:` entry (a newName).
for name in LS PS NEL raw-NEL CR; do
  case "$name" in
    LS) bad=$'\xe2\x80\xa8' ;; PS) bad=$'\xe2\x80\xa9' ;; NEL) bad=$'\xc2\x85' ;; raw-NEL) bad=$'\x85' ;; CR) bad=$'\r' ;;
  esac
  new_repo "bytes-$name"
  smuggle="  # hold: web 2026-10-23 reason${bad}  - name: ghcr.io/timkrebs/ryft/web${bad}    newName: ghcr.io/attacker/web"
  awk -v l="$smuggle" '/ryft\/web$/ { print l } { print }' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"; git commit -qam "deploy: smuggled hold"
  for m in "" --check --pins; do
    rc=0; FORCE='' ./scripts/deploy_targets.sh $m >"$tmp/out" 2>"$tmp/err" || rc=$?
    [ "$rc" = 2 ] || fail "$name: 'deploy_targets.sh $m' must exit 2 (got $rc)"
    grep -q 'not printable ASCII' "$tmp/err" || fail "$name: no annotation: $(cat "$tmp/err")"
    [ ! -s "$tmp/out" ] || fail "$name: no outputs"
  done
done
# A CRLF overlay is the same thing, by accident.
new_repo crlf; sed 's/$/\r/' "$OVERLAY" > "$OVERLAY.new"; mv "$OVERLAY.new" "$OVERLAY"
rc=0; ./scripts/deploy_targets.sh >/dev/null 2>"$tmp/err" || rc=$?
[ "$rc" = 2 ] || fail "a CRLF overlay must exit 2 (got $rc)"
# --pins: the six images and their tags.
new_repo pins
[ "$(./scripts/deploy_targets.sh --pins | wc -l | tr -d ' ')" = 6 ] || fail "--pins: $(./scripts/deploy_targets.sh --pins)"
./scripts/deploy_targets.sh --pins | grep -qx "web=$first" || fail "--pins lacks web=$first"

echo "✓ deploy_targets_test: web/services/both/none, docs, pin commit, unknown pin, disagreeing pins, hold (and its revert drill), FORCE"
