#!/usr/bin/env bash
# shellcheck disable=SC2016  # the backticks are Go struct tags inside single quotes, meant literally
# scripts/check_pure_move.sh in a throwaway repository. A split passes (a reordered const group, several blank and
# init declarations, a test moved to another test file, a package without tests). Each of these is refused, for
# the reason given: a changed tag, value, comment or field order, a const taken out of its iota group, a dropped
# declaration, a declaration moved to another package, an import swapped under the same name, a weakened test
# assertion, two body comments swapped, a dropped blank declaration, a //go:build line, two initialisers that run
# code swapping places, a changed testdata file, a test that does not compile. And the exit codes: 1 for "not a
# pure move", 2 for "cannot tell" (unknown ref or package), also from the comparison program.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_pure_move_test: $*" >&2; exit 1; }

repo="$tmp/repo"
mkdir -p "$repo/scripts" "$repo/backend/internal/p/testdata" "$repo/backend/internal/np"
cp "$HERE/scripts/check_pure_move.sh" "$HERE/scripts/pure_move.go" "$repo/scripts/"
cd "$repo"
printf 'module example.com/m\n\ngo 1.24\n' > backend/go.mod
printf 'fixture\n' > backend/internal/p/testdata/a.txt
printf 'package np\n\n// X is one.\nconst X = 1\n\n// Y is two.\nconst Y = 2\n' > backend/internal/np/np.go
cat > backend/internal/p/p.go <<'EOF'
// Package p is a fixture.
package p

import (
	"fmt"
	"math/rand"
	"time"
)

// Kinds of a thing.
const (
	KindA = iota // first
	KindB        // second
	KindC
)

// Thing is a thing.
type Thing struct {
	ID   int       `json:"id"`
	At   time.Time `json:"at"` // when
	Kind string    `json:"kind"`
}

// Name says what it is.
func (t Thing) Name() string {
	// first note
	k := t.Kind
	// second note
	return k
}

// String makes a Thing a Stringer.
func (t Thing) String() string { return t.Kind }

// Other is another thing.
type Other struct {
	N int `json:"n,omitempty"`
}

// String makes an Other a Stringer.
func (o Other) String() string { return fmt.Sprint(o.N) }

var _ fmt.Stringer = Thing{}
var _ fmt.Stringer = Other{}

// Registered counts what ran at start.
var Registered int

func init() { Registered++ }

func init() { Registered += 2 }

func bump(n int) int { Registered += n; return Registered }

// A and B run code at start, in this order.
var A = bump(10)
var B = bump(100)

// Pick returns a random byte.
func Pick() int {
	buf := make([]byte, 1)
	_, _ = rand.Read(buf)
	return int(buf[0])
}
EOF
cat > backend/internal/p/p_test.go <<'EOF'
package p

import "testing"

func TestName(t *testing.T) {
	if (Thing{Kind: "x"}).Name() != "x" {
		t.Fatalf("name")
	}
}

func TestString(t *testing.T) {
	if (Other{N: 3}).String() != "3" {
		t.Fatalf("string")
	}
}
EOF
git init -q -b main; git config user.email t@example.com; git config user.name test
git add -A; git commit -q -m base

# --- the split: Other, its method, the kinds and one blank declaration go to q.go (the kinds after Other),
#     TestString goes to q_test.go; the initialisers stay in their order; the package np splits in two
python3 -I - <<'EOF'
import re
p = open("backend/internal/p/p.go").read()
def cut(s, start, end):
    i = s.index(start); j = s.index(end, i) + len(end)
    return s[:i] + s[j:], s[i:j]
p, kinds = cut(p, "// Kinds of a thing.", "KindC\n)\n\n")
p, other = cut(p, "// Other is another thing.", "return fmt.Sprint(o.N) }\n\n")
p, blank = cut(p, "var _ fmt.Stringer = Other{}\n", "Other{}\n")
open("backend/internal/p/p.go", "w").write(p)
open("backend/internal/p/q.go", "w").write("package p\n\nimport \"fmt\"\n\n" + other + kinds.rstrip("\n") + "\n\n" + blank)
t = open("backend/internal/p/p_test.go").read()
t, ts = cut(t, "func TestString", "\n}\n")
open("backend/internal/p/p_test.go", "w").write(t.rstrip("\n") + "\n")
open("backend/internal/p/q_test.go", "w").write("package p\n\nimport \"testing\"\n\n" + ts.lstrip("\n"))
EOF
[ -z "$(gofmt -l backend)" ] || fail "the fixture is not gofmt-clean"
printf 'package np\n\n// X is one.\nconst X = 1\n' > backend/internal/np/np.go
printf 'package np\n\n// Y is two.\nconst Y = 2\n' > backend/internal/np/y.go
(cd backend && go vet ./... >/dev/null) || fail "the split fixture does not build"

out=$(bash scripts/check_pure_move.sh HEAD internal/p 2>&1) || fail "a pure split was refused: $out"
case $out in *"pure move: yes"*) ;; *) fail "no verdict: $out" ;; esac
out=$(bash scripts/check_pure_move.sh HEAD internal/np 2>&1) || fail "a package without tests was refused: $out"
case $out in *"the same 0 tests"*) ;; *) fail "a package without tests: $out" ;; esac

snap() { rm -rf "$tmp/snap"; cp -R backend "$tmp/snap"; }
unsnap() { rm -rf backend; cp -R "$tmp/snap" backend; }
sub() { python3 -I -c 'import sys; p, o, n = sys.argv[1:]; s = open(p).read(); assert o in s, o; open(p, "w").write(s.replace(o, n, 1))' "$@"; }

refuse() { # <what> <expected text in the output> <mutation: a function name>
  snap
  "$3"
  rc=0
  bash scripts/check_pure_move.sh HEAD internal/p >"$tmp/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ] || fail "$1: exit status $rc, want 1: $(cat "$tmp/out")"
  grep -q 'pure move: NO' "$tmp/out" || fail "$1: no verdict: $(cat "$tmp/out")"
  grep -qF "$2" "$tmp/out" || fail "$1: refused, but not for the reason ($2): $(cat "$tmp/out")"
  unsnap
}
m_tag() { sub backend/internal/p/q.go 'n,omitempty' 'n'; }
m_value() { sub backend/internal/p/q.go 'KindB        // second' 'KindB        // second
	KindZ'; }
m_comment_dropped() { sub backend/internal/p/p.go ' // when' ''; }
m_fields_swapped() { sub backend/internal/p/p.go '	ID   int       `json:"id"`
	At   time.Time `json:"at"` // when' '	At   time.Time `json:"at"` // when
	ID   int       `json:"id"`'; }
m_iota() { sub backend/internal/p/q.go '	KindC
)' ')

const KindC = 2'; }
m_dropped() { sub backend/internal/p/q.go '// Other is another thing.
type Other struct {
	N int `json:"n,omitempty"`
}' ''; }
m_other_pkg() {
  mkdir backend/internal/p2
  sub backend/internal/p/q.go '// Other is another thing.
type Other struct {
	N int `json:"n,omitempty"`
}' ''
  printf 'package p2\n\n// Other is another thing.\ntype Other struct {\n\tN int `json:"n,omitempty"`\n}\n' > backend/internal/p2/other.go
}
m_import_swap() { sub backend/internal/p/p.go '"math/rand"' 'rand "crypto/rand"'; }
m_weak_assertion() { sub backend/internal/p/p_test.go 't.Fatalf("name")' 't.Logf("name")'; }
m_comments_swapped() {
  sub backend/internal/p/p.go '// first note' '// TMP'
  sub backend/internal/p/p.go '// second note' '// first note'
  sub backend/internal/p/p.go '// TMP' '// second note'
}
m_blank_dropped() { sub backend/internal/p/q.go 'var _ fmt.Stringer = Other{}
' ''; }
m_buildtag() { sub backend/internal/p/q.go 'package p' '//go:build ignore

package p'; }
m_init_order() {
  sub backend/internal/p/p.go 'var A = bump(10)
' ''
  printf '\nvar A = bump(10)\n' >> backend/internal/p/q.go
}
m_testdata() { printf 'changed\n' > backend/internal/p/testdata/a.txt; }
m_testdata_new() { printf 'new\n' > backend/internal/p/testdata/b.txt; }
m_test_broken() { sub backend/internal/p/q_test.go 'func TestString' 'var broken = undefinedName

func TestString'; }
m_test_helper() { sub backend/internal/p/q_test.go 'func TestString' 'func helper() {}

func TestString'; }

refuse "a changed tag" "differs: p type Other" m_tag
refuse "a value added to a const group" "only before: p const KindA,KindB,KindC" m_value
refuse "a dropped comment" "differs: p type Thing" m_comment_dropped
refuse "two fields swapped" "differs: p type Thing" m_fields_swapped
refuse "a const out of its iota group" "only before: p const KindA,KindB,KindC" m_iota
refuse "a dropped declaration" "only before: p type Other" m_dropped
refuse "a declaration moved to another package" "only before: p type Other" m_other_pkg
refuse "an import swapped under the same name" "import added: p rand crypto/rand" m_import_swap
refuse "a weakened assertion" "[test files] declaration differs: p func TestName" m_weak_assertion
refuse "two body comments swapped" "differs: p func (Thing) Name" m_comments_swapped
refuse "a dropped blank declaration" "differs: p var _" m_blank_dropped
refuse "a //go:build line" "build context" m_buildtag
refuse "initialisers that swap places" "order of the package-level initialisers" m_init_order
refuse "a changed testdata file" "non-Go files in the package directory CHANGED" m_testdata
refuse "a new testdata file" "non-Go files in the package directory CHANGED" m_testdata_new
refuse "a test that does not compile" "pure move: NO" m_test_broken
refuse "a helper added to the tests" "only after: p func helper" m_test_helper

# exit codes: 2 is "cannot tell", also from the comparison program, and never 1
rc=0; bash scripts/check_pure_move.sh nosuchref internal/p >/dev/null 2>&1 || rc=$?; [ "$rc" -eq 2 ] || fail "an unknown ref: exit $rc, want 2"
rc=0; bash scripts/check_pure_move.sh HEAD internal/nosuch >/dev/null 2>&1 || rc=$?; [ "$rc" -eq 2 ] || fail "an unknown package: exit $rc, want 2"
go build -o "$tmp/pure_move" scripts/pure_move.go
rc=0; "$tmp/pure_move" "$tmp/nosuch1" "$tmp/nosuch2" >/dev/null 2>&1 || rc=$?; [ "$rc" -eq 2 ] || fail "the comparison program on a missing directory: exit $rc, want 2"
mkdir "$tmp/bad"; printf 'package p\nfunc (' > "$tmp/bad/x.go"
rc=0; "$tmp/pure_move" "$tmp/bad" "$tmp/bad" >/dev/null 2>&1 || rc=$?; [ "$rc" -eq 2 ] || fail "the comparison program on an unparsable file: exit $rc, want 2"
rc=0; "$tmp/pure_move" backend/internal/p "$tmp/snap/internal/p" >/dev/null 2>&1 || rc=$?; [ "$rc" -eq 0 ] || fail "the comparison program on equal directories: exit $rc, want 0"
echo "✓ check_pure_move_test"
