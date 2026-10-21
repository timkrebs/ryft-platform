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
grep -q '::warning.*web held (broken AASA, fix in #57), 1 commits under web/' "$tmp/err" || fail "no hold warning: $(cat "$tmp/err")"
new_repo hold-unknown; ./scripts/pin_images.sh f437747 web "$OVERLAY" >/dev/null; git commit -qam "deploy: web f437747"; hold web "rollback"
expect false false "a held web group whose pin is not in this history"
new_repo hold-services; commit_file backend/main.go v2 "scoring: a change"; hold services "bad migration"
expect false false "held services"
unhold; expect true false "the hold is released"
# A hold for one group does not stop the other, and a group name is matched whole.
new_repo hold-other; commit_file backend/main.go v2 "scoring: a change"; hold web "x"
expect true false "a web hold leaves services alone"
new_repo hold-prefix; commit_file web/index.txt v2 "web: a change"; hold webby "x"
expect false true "a hold for another group name"
# The overlay as committed carries the convention in a comment but no hold of its own.
new_repo real-overlay
grep -q '# hold: <group> <reason>' "$OVERLAY" || fail "the overlay no longer documents the hold convention"

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

echo "✓ deploy_targets_test: web/services/both/none, docs, pin commit, unknown pin, disagreeing pins, hold (and its revert drill), FORCE"
