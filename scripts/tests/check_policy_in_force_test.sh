#!/usr/bin/env bash
# shellcheck disable=SC2016
# scripts/check_policy_in_force.sh in a throwaway repository (make test-scripts): deploy/run/config.env's
# version (shape, calendar, listed, its English page) and, against a base ref, the draft guards.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_policy_in_force_test: $*" >&2; exit 1; }
# The repository is $tmp/repo; the scratch files ($tmp/out, list.bak, l.json) stay outside it.
mkdir -p "$tmp/repo/scripts"; cp "$HERE/scripts/check_policy_in_force.sh" "$tmp/repo/scripts/"
cd "$tmp/repo"; git init -q -b main; git config user.email t@example.com; git config user.name test
L=web/content/legal
RC=deploy/run/config.env
page() { # <lang> <version> [status]
  mkdir -p "$L/$1/privacy"; printf -- '---\ntitle: "Privacy policy"\nversion: %s\nstatus: %s\n---\n\ntext\n' "'$2'" "${3:-published}" > "$L/$1/privacy/$2.mdx"
}
run_cfg() { mkdir -p deploy/run; printf '# fixture\nLOG_LEVEL=info\nPRIVACY_POLICY_VERSION=%s\n' "$1" > $RC; }
ok()  { ./scripts/check_policy_in_force.sh "${BASE:-}" >"$tmp/out" 2>&1 || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { if ./scripts/check_policy_in_force.sh "${BASE:-}" >"$tmp/out" 2>&1; then fail "should fail: $1"; fi; grep -q -- "$2" "$tmp/out" || fail "$1: message lacks '$2': $(cat "$tmp/out")"; }

mkdir -p $L; echo '["2026-10-20","2026-11-02","2027-01-01","2027-02-01","2026-11-31"]' > $L/privacy-versions.json
page en 2026-10-20 draft; page en 2026-11-02; page en 2027-01-01; page en 2027-02-01 draft; page en 2026-11-31
run_cfg 2026-11-02
git add -A; git commit -qm first
BASE=

# The version itself: one bare, real, listed date with its English page.
ok "a listed version with its published page"
grep -q "policy in force: deploy/run/config.env 2026-11-02" "$tmp/out" || fail "the summary names the version: $(cat "$tmp/out")"
printf 'PRIVACY_POLICY_VERSION="2026-11-02"\n' > $RC; bad "a quoted value" "deploy/run/config.env: PRIVACY_POLICY_VERSION '\"2026-11-02\"' is not a bare"
printf 'PRIVACY_POLICY_VERSION=2026-11-02 # x\n' > $RC; bad "an inline comment" "is not a bare"
printf 'PRIVACY_POLICY_VERSION=2026-11-02\r\n' > $RC; bad "a CRLF line" "is not a bare"
printf 'PRIVACY_POLICY_VERSION=2026-11-02\nPRIVACY_POLICY_VERSION=2026-11-02\n' > $RC; bad "the key twice" "exactly once"
printf 'LOG_LEVEL=info\n' > $RC; bad "no key" "exactly once"
rm -f $RC; bad "no deploy/run/config.env" "deploy/run/config.env is missing"
run_cfg 2026-11-31; bad "a date that does not exist" "real calendar date"
run_cfg 2029-01-01; bad "an unlisted version" "2029-01-01 is not in"
cp $L/privacy-versions.json "$tmp/list.bak"; jq '. + ["2029-01-01"]' "$tmp/list.bak" > $L/privacy-versions.json
bad "a listed version without its page" "has no en page"
rm -f $L/privacy-versions.json; run_cfg 2026-11-02; bad "no list" "privacy-versions.json is missing"
cp "$tmp/list.bak" $L/privacy-versions.json
ok "the version again"
# Without a base, a draft in force passes: the guard below is about a change.
run_cfg 2027-02-01; ok "a draft without a base"
run_cfg 2026-11-02

# The draft guard on a pull request: only a CHANGE to a draft page is refused. The tree is the first
# commit's again, so that commit is the base.
[ -z "$(git status --porcelain)" ] || fail "the tree should equal the first commit here: $(git status --porcelain)"
git branch -q -f base HEAD; BASE=base
ok "unchanged against base"
grep -q "against base" "$tmp/out" || fail "the summary names the base: $(cat "$tmp/out")"
run_cfg 2027-02-01; bad "a change to a version whose page is a draft" "changes PRIVACY_POLICY_VERSION from 2026-11-02 (on base) to 2027-02-01"
run_cfg 2027-01-01; ok "a change to a published version"
run_cfg 2026-10-20; ok "back to a version listed before base's (a draft)"
run_cfg 2026-11-02; page en 2026-11-02 draft; bad "a published text set back to a draft in place" "cannot go back to a draft"
page en 2026-11-02; ok "published again"
# English only: a German page is not read, a draft there or none at all
page de 2026-11-02 draft; ok "a German draft beside a published English page is ignored"
rm -rf $L/de
# An unchanged draft stays allowed.
run_cfg 2026-10-20; git add -A; git commit -qm "base at a draft"; git branch -q -f base HEAD
ok "an unchanged version whose page is a draft"
run_cfg 2027-02-01; bad "from one draft to another, newer one" "is not status: published"
run_cfg 2026-11-02; git add -A; git commit -qm "base published"; git branch -q -f base HEAD

# The frontmatter is read the way the site reads it: only exactly "published" (after normalising) passes.
n=0
variant() { # <name> <ok|bad> <printf format of the English page>
  n=$((n + 1)); local v; v=$(printf '2028-01-%02d' "$n")
  jq --arg v "$v" '. + [$v]' $L/privacy-versions.json > "$tmp/l.json"; mv "$tmp/l.json" $L/privacy-versions.json
  mkdir -p "$L/en/privacy"
  # shellcheck disable=SC2059
  printf -- "$3" > "$L/en/privacy/$v.mdx"
  run_cfg "$v"
  if [ "$2" = ok ]; then ok "$1"; else bad "$1" "not status: published"; fi
}
variant "status published" ok '---\nstatus: published\n---\ntext\n'
variant "quoted published with a comment" ok '---\r\ntitle: "x"\r\nstatus: "published"  # live\r\n---\r\ntext\r\n'
variant "a BOM" ok '\357\273\277---\nstatus: published\n---\n'
variant "CRLF draft" bad '---\r\nstatus: draft\r\n---\r\ntext\r\n'
variant "a BOM before a draft" bad '\357\273\277---\nstatus: draft\n---\n'
variant "dashes with a trailing space" bad '--- \nstatus: draft\n---\n'
variant "a quoted key" bad '---\n"status": draft\n---\n'
variant "a tagged value" bad '---\nstatus: !!str draft\n---\n'
variant "flow frontmatter" bad '---\n{status: draft}\n---\n'
variant "no status at all" bad '---\ntitle: x\n---\n'
variant "no frontmatter" bad 'status: published\n'
variant "an unknown status" bad '---\nstatus: review\n---\n'
# the published forms of the same variants pass; the ones that fail closed even when published are deliberate
variant "published, dashes with a trailing space" ok '--- \nstatus: published\n---\n'
variant "published, quoted key" ok '---\n"status": published\n---\n'
variant "published, single-quoted key and value" ok "---\n'status': 'published'\n---\n"
variant "published, CRLF and a BOM" ok '\357\273\277---\r\nstatus: published\r\n---\r\n'
variant "published, a tag (deliberately refused)" bad '---\nstatus: !!str published\n---\n'
variant "published, flow frontmatter (deliberately refused)" bad '---\n{status: published}\n---\n'
variant "published, a block scalar (deliberately refused)" bad '---\nstatus: |\n  published\n---\n'
variant "published, a continuation line (deliberately refused)" bad '---\nstatus: published\n  and more\n---\n'
variant "published, a continuation line after a blank line (deliberately refused)" bad '---\nstatus: published\n\n  draft\n---\n'
variant "published, a continuation line after blank lines and a comment (deliberately refused)" bad '---\nstatus: published\n\n# c\n\n  draft\n---\n'
variant "published, a blank line, then another key" ok '---\nstatus: published\n\ntitle: x\n---\n'
variant "published twice (deliberately refused)" bad '---\nstatus: published\nstatus: published\n---\n'
variant "a quote inside the value" bad '---\nstatus: pub"lished"\n---\n'
variant "a blank inside the value" bad '---\nstatus: "pub lished"\n---\n'
run_cfg 2026-11-02; ok "back at base's version"

# The base ref must resolve and hold the file with the key: the guard cannot compare with nothing.
BASE=origin/nope; bad "a base ref that does not resolve" "does not resolve"
BASE=base
rm -f $RC; git add -A; git commit -qm "no config.env"; git branch -q -f base HEAD; run_cfg 2026-11-02
bad "a base without deploy/run/config.env" "does not exist on base"
printf 'LOG_LEVEL=info\n' > $RC; git add -A; git commit -qm "config.env without the key"; git branch -q -f base HEAD; run_cfg 2026-11-02
bad "a base whose config.env sets no version" "sets no PRIVACY_POLICY_VERSION"
BASE=
echo "✓ check_policy_in_force_test: deploy/run/config.env's shape, calendar, list and page; against a base: no draft put into force (frontmatter read as the site reads it), going back allowed, no in-place un-publish, a base that cannot be compared refused"
