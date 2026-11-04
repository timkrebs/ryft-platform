#!/usr/bin/env bash
# scripts/golden_guard.sh: a golden user file changes only with a ModelVersion bump, its input
# digest or the field digest; hostile manifest values and paths are refused, never printed
# (make test-scripts).
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ golden_guard_test: $*" >&2; exit 1; }
d=backend/internal/scoring/testdata/golden
r=$tmp/repo  # the report goes to $tmp/out, outside the repository the checkouts switch
mkdir -p "$r/scripts" "$r/$d"; cp "$HERE/scripts/golden_guard.sh" "$r/scripts/"
cd "$r"; git init -q -b main; git config user.email t@example.com; git config user.name test
# manifest <version> <digest-a> <digest-b> [fields]
manifest() { printf '{"fields":"%s","inputs":{"user-a":{"digest":"%s"},"user-b":{"digest":"%s"}},"model_version":"%s"}\n' "${4:-10}" "$2" "$3" "$1" > $d/manifest.json; }
edit() { jq -c "$1" $d/manifest.json > $d/m && mv $d/m $d/manifest.json; }  # edit <jq filter>
guard() { st=0; ./scripts/golden_guard.sh "${1:-origin/main}" >"$tmp/out" 2>&1 || st=$?; }
ok()  { guard "${2:-}"; [ "$st" -eq 0 ] || fail "should pass: $1 ($(cat "$tmp/out"))"; }
bad() { guard "${2:-}"; [ "$st" -eq 1 ] || fail "should fail (exit 1): $1 (exit $st: $(cat "$tmp/out"))"; }
cannot() {
  guard "$2"
  if [ "$st" -ne 2 ] || ! grep -q "^::error title=golden guard::$3" "$tmp/out"; then fail "should not check (exit 2): $1 (exit $st: $(cat "$tmp/out"))"; fi
}
not_printed() { ! grep -q -- "$1" "$tmp/out" || fail "printed raw: $1 ($(cat "$tmp/out"))"; }
commit() { git add -A; git commit -qm "$1"; }
base() { commit "$1"; git update-ref refs/remotes/origin/main HEAD; }  # the state on main
reset() { git checkout -q -B pr origin/main; }                        # the PR branch from it

echo x > README; base "before the golden files"
# The files' first introduction: the base has no manifest, every user is new.
reset; manifest 2026.10.3 1 2; echo '{"recovery": 64}' > $d/user-a.json; echo '{"recovery": 50}' > $d/user-b.json
commit "golden files"; ok "the first introduction"
git checkout -q main; git merge -q --ff-only pr; git update-ref refs/remotes/origin/main HEAD

reset; echo y > README; commit "no golden change"; ok "nothing golden changed"
reset; echo '{"recovery": 61}' > $d/user-a.json; commit "a moved value"; bad "a value moved, nothing else did"
grep -q 'user-a.json changed' "$tmp/out" || fail "the error names the file: $(cat "$tmp/out")"
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.model_version = "2026.10.4"'; commit "bump"; ok "ModelVersion bumped"
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.inputs["user-a"].digest = "3"'; commit "generator"; ok "user-a's inputs changed"
reset; echo '{"recovery": 49}' > $d/user-b.json; edit '.inputs["user-a"].digest = "3"'; commit "generator, wrong user"; bad "user-a's inputs changed, user-b's file moved"
reset; echo '{"recovery": 64, "new": 1}' > $d/user-a.json; edit '.fields = "11"'; commit "additive"; ok "the field digest changed (a new column)"
reset; echo '{"recovery": 1}' > $d/user-c.json; commit "a file without a manifest entry"; bad "a new user the manifest does not list"
reset; echo '{"recovery": 1}' > $d/user-c.json; edit '.inputs["user-c"] = {"digest": "7"}'; commit "a new user"; ok "a new user with its digest"
reset; git rm -q $d/user-b.json; edit 'del(.inputs["user-b"])'; commit "user-b gone"; bad "a user and its entry deleted, no bump"
grep -q 'user-b.json was deleted' "$tmp/out" || fail "the error names the deleted file: $(cat "$tmp/out")"
reset; git rm -q $d/user-b.json; edit 'del(.inputs["user-b"]) | .model_version = "2026.10.4"'; commit "user-b gone, bump"; ok "a user deleted with a version bump"
reset; git rm -q $d/user-b.json; edit 'del(.inputs["user-b"]) | .users = ["user-a"]'; commit "user-b retired"; ok "a user deleted with the users list changed"
reset; git mv $d/user-a.json $d/user-g.json; edit '.inputs["user-g"] = .inputs["user-a"] | del(.inputs["user-a"])'; commit "rename"; bad "a rename, manifest key renamed too (a deletion plus an addition)"
grep -q 'user-a.json was deleted' "$tmp/out" || fail "the rename is seen as a deletion: $(cat "$tmp/out")"
reset; echo '{"recovery": 61}' > $d/user-a.json; edit 'del(.inputs["user-a"])'; commit "digest dropped"; bad "a digest dropped while the file is kept"
reset; echo '{"recovery": 61}' > $d/user-a.json; echo 'not json' > $d/manifest.json; commit "broken manifest"; bad "an unreadable manifest"
reset; echo '{"recovery": 61}' > $d/user-a.json; git rm -q $d/manifest.json; commit "manifest gone"; bad "a manifest removed"
# The version is the manifest's: version.go is not read (reformatting it changes nothing).
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.model_version = ""'; commit "empty version"; bad "an empty model_version"
reset; echo '{"recovery": 61}' > $d/user-a.json; edit 'del(.model_version)'; commit "no version"; bad "no model_version"
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.model_version = "v2026.10.4"'; commit "odd version"; bad "a model_version not YEAR.MONTH.N"
# Hostile manifest values: refused, never printed (a "::" line is a workflow command).
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.inputs["user-a"].digest = "::add-mask::x\n::error::pwned"'
commit "hostile digest"; bad "a digest that is not a number"; not_printed pwned; not_printed add-mask
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.fields = "::warning::x"'; commit "hostile field digest"; bad "a hostile field digest"; not_printed '::warning::x'
reset; echo '{"recovery": 61}' > $d/user-a.json; edit '.model_version = "2026.10.4\n::error::v"'; commit "hostile version"; bad "a hostile model_version"; not_printed '::error::v'
# Hostile paths: a name with a line break is one name (read NUL-separated) and not a user name.
reset; printf '{}\n' > "$d/user-a.json
::error::x.json"; commit "a name with a line break"; bad "a file name with a line break"; not_printed '^::error::x'
reset; mkdir -p "$d/user-x
::error::y"; printf '{}\n' > "$d/user-x
::error::y/user-a.json"; commit "a user file below a folder with a line break"; bad "a user file below the golden folder"; not_printed '^::error::y'
# The merge base, not origin/main's tip: a bump merged to main after the branch was cut does not count.
reset; echo '{"recovery": 61}' > $d/user-a.json; commit "moved on a branch"
git checkout -q main; edit '.model_version = "2026.10.5"'; commit "a bump on main"; git update-ref refs/remotes/origin/main HEAD; git checkout -q pr
bad "the bump is on main, not in the change"
# Cannot check: a missing base, a base read as an option, no merge base.
cannot "a missing base" origin/no-such-branch "the base"
cannot "a base that looks like an option" "--output=$tmp/written" "the base"
[ ! -e "$tmp/written" ] || fail "a base starting with -- was read as an option"
git checkout -q --orphan unrelated; git rm -q -rf .; echo z > other; commit "unrelated history"; git checkout -q pr
cannot "no merge base" unrelated "no merge base"
echo "✓ golden_guard_test: a golden file moves only with a bump, its digest or new fields; hostile input is refused"
