#!/usr/bin/env bash
# Proof that a split of one Go package into several files (J8, a pure move) changed nothing but where the code
# lives. Compares the package at a git ref with the working tree:
#   1. `go doc -all -u` is byte-identical once the entries of its CONSTANTS and VARIABLES sections are sorted
#      (go doc lists them in file order, so a split reorders them; types, functions and methods come sorted);
#   2. `go test -list .` names the same tests;
#   3. scripts/pure_move.go: the same top-level declarations (printed AST, field order, tags, attached comments)
#      and the same non-blank source lines outside package clauses and imports (nothing added without a removed
#      twin, nothing removed without an added twin).
#   scripts/check_pure_move.sh <base-ref> <package dir below backend/>     e.g.  origin/main internal/model
# Exit 0: a pure move. 1: not one (the difference is printed). 2: it cannot work (bad usage, no go/git/tar, the
# ref has no such package).
set -euo pipefail

die() { echo "check_pure_move: $*" >&2; exit 2; }
[ $# -eq 2 ] || die "usage: scripts/check_pure_move.sh <base-ref> <package dir below backend/>"
ref=$1
pkg=${2#backend/}
for t in go git tar; do command -v "$t" >/dev/null 2>&1 || die "$t is not on PATH"; done

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
git -C "$root" rev-parse --verify --quiet "$ref^{commit}" >/dev/null || die "unknown ref $ref"
git -C "$root" cat-file -e "$ref:backend/$pkg" 2>/dev/null || die "$ref has no backend/$pkg"
[ -d "$root/backend/$pkg" ] || die "backend/$pkg does not exist in the working tree"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/before"
git -C "$root" archive "$ref" backend | tar -x -C "$tmp/before"

status=0
# go doc lists the package-level const and var blocks in source order, which a split changes: sort the entries of
# those two sections (an entry is a block from its const/var line to the next one, doc text included).
norm_doc() {
  python3 -I -c '
import re, sys
out, sect, entries = [], None, []
def flush():
    global entries
    out.extend(sorted(entries))
    entries = []
for line in sys.stdin.read().split("\n"):
    if re.fullmatch(r"[A-Z]+", line):
        flush()
        sect = line
        out.append(line)
        continue
    if sect in ("CONSTANTS", "VARIABLES"):
        if re.match(r"(const|var) ", line):
            entries.append(line)
        elif entries:
            entries[-1] += "\n" + line
        else:
            out.append(line)
    else:
        out.append(line)
flush()
sys.stdout.write("\n".join(x.rstrip("\n") for x in out) + "\n")
'
}
doc_of() { (cd "$1/backend" && go doc -all -u "./$pkg") | norm_doc; }
list_of() { (cd "$1/backend" && go test -list . "./$pkg" | grep -v '^ok'); }

doc_of "$tmp/before" >"$tmp/doc.before"
doc_of "$root" >"$tmp/doc.after"
if cmp -s "$tmp/doc.before" "$tmp/doc.after"; then
  echo "go doc -all -u: identical ($(wc -l <"$tmp/doc.after" | tr -d ' ') lines, $(shasum -a 256 <"$tmp/doc.after" | cut -c1-16))"
else
  echo "go doc -all -u: DIFFERENT"; diff "$tmp/doc.before" "$tmp/doc.after" | head -40 || true; status=1
fi

list_of "$tmp/before" >"$tmp/list.before"
list_of "$root" >"$tmp/list.after"
if cmp -s "$tmp/list.before" "$tmp/list.after"; then
  echo "go test -list .: the same $(wc -l <"$tmp/list.after" | tr -d ' ') tests"
else
  echo "go test -list .: DIFFERENT"; diff "$tmp/list.before" "$tmp/list.after" | head -40 || true; status=1
fi

go run "$root/scripts/pure_move.go" "$tmp/before/backend/$pkg" "$root/backend/$pkg" || status=1
[ "$status" -eq 0 ] && echo "pure move: yes" || echo "pure move: NO"
exit "$status"
