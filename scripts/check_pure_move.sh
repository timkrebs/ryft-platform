#!/usr/bin/env bash
# Proof that a split of one Go package into several files (J8, a pure move) changed nothing but where the code
# lives. Compares the package at a git ref with the working tree:
#   1. `go doc -all -u` is the same once the entries of its CONSTANTS and VARIABLES sections are sorted (go doc
#      lists them in file order, so a split reorders them; types, functions and methods come sorted);
#   2. `go test -list .` names the same tests (a package without tests names none);
#   3. every file of the package directory that is not a .go file (testdata, embedded files) is unchanged;
#   4. scripts/pure_move.go, for the non-test files and, as a separate set, the _test.go files: the same
#      declarations (exact text, comments, import qualifiers), imports, build contexts, order of the initialisers
#      that run code, and source lines outside package clauses and imports. See its header for what it proves
#      and what it does not.
#   scripts/check_pure_move.sh <base-ref> <package dir below backend/>     e.g.  origin/main internal/model
# Exit 0: a pure move. 1: not one (the difference is printed, the last line says `pure move: NO`). 2: it cannot
# tell (bad usage, a missing tool, a ref or package that is not there, the base does not build, the comparison
# program fails). A broken check must not read as "pure".
set -euo pipefail

die() { echo "check_pure_move: $*" >&2; exit 2; }
[ $# -eq 2 ] || die "usage: scripts/check_pure_move.sh <base-ref> <package dir below backend/>"
ref=$1
pkg=${2#backend/}
for t in go git tar python3 diff cmp; do command -v "$t" >/dev/null 2>&1 || die "$t is not on PATH"; done

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
git -C "$root" rev-parse --verify --quiet "$ref^{commit}" >/dev/null || die "unknown ref $ref"
git -C "$root" cat-file -e "$ref:backend/$pkg" 2>/dev/null || die "$ref has no backend/$pkg"
[ -d "$root/backend/$pkg" ] || die "backend/$pkg does not exist in the working tree"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/before"
git -C "$root" archive "$ref" backend | tar -x -C "$tmp/before"
go build -o "$tmp/pure_move" "$root/scripts/pure_move.go" || die "scripts/pure_move.go does not build"

status=0
verdict() { # <exit status>
  case $1 in
    0) echo "pure move: yes" ;;
    1) echo "pure move: NO" ;;
    *) echo "pure move: cannot tell" ;;
  esac
}

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

# go doc and go test -list of one tree into $2.* files; the exit status is that of the go commands.
doc_of() { (cd "$1/backend" && go doc -all -u "./$pkg") >"$2.raw" 2>"$2.err" && norm_doc <"$2.raw" >"$2"; }
list_of() {
  (cd "$1/backend" && go test -list . "./$pkg") >"$2.raw" 2>"$2.err" || return 1
  # test names only: not `ok pkg 0.1s`, not `? pkg [no test files]`
  { grep -E '^(Test|Benchmark|Example|Fuzz)' "$2.raw" || true; } >"$2"
}

# The base must work, or there is nothing to compare with.
doc_of "$tmp/before" "$tmp/doc.before" || { cat "$tmp/doc.before.err" >&2; echo "go doc fails on $ref"; verdict 2; exit 2; }
list_of "$tmp/before" "$tmp/list.before" || { cat "$tmp/list.before.err" >&2; echo "go test -list fails on $ref"; verdict 2; exit 2; }

if doc_of "$root" "$tmp/doc.after"; then
  if cmp -s "$tmp/doc.before" "$tmp/doc.after"; then
    echo "go doc -all -u: identical ($(wc -l <"$tmp/doc.after.raw" | tr -d ' ') lines; const and var entries sorted first, sha $(shasum -a 256 <"$tmp/doc.after" | cut -c1-16))"
  else
    echo "go doc -all -u: DIFFERENT"; { diff "$tmp/doc.before" "$tmp/doc.after" | head -40; } || true; status=1
  fi
else
  echo "go doc -all -u: fails on the working tree"; head -20 "$tmp/doc.after.err"; status=1
fi

if list_of "$root" "$tmp/list.after"; then
  if cmp -s "$tmp/list.before" "$tmp/list.after"; then
    echo "go test -list .: the same $(wc -l <"$tmp/list.after" | tr -d ' ') tests"
  else
    echo "go test -list .: DIFFERENT"; { diff "$tmp/list.before" "$tmp/list.after" | head -40; } || true; status=1
  fi
else
  echo "go test -list .: fails on the working tree (it does not compile or a test does not link)"; head -20 "$tmp/list.after.err"; status=1
fi

if other=$(diff -rq --exclude='*.go' "$tmp/before/backend/$pkg" "$root/backend/$pkg" 2>&1); then
  echo "non-Go files (testdata, embeds) in the package directory: unchanged"
else
  echo "non-Go files in the package directory CHANGED:"; echo "$other"; status=1
fi

rc=0
"$tmp/pure_move" "$tmp/before/backend/$pkg" "$root/backend/$pkg" || rc=$?
case $rc in
  0) ;;
  1) status=1 ;;
  *) verdict 2; exit 2 ;;
esac
verdict "$status"
exit "$status"
