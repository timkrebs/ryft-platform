#!/usr/bin/env bash
# scripts/check_pure_move.sh in a throwaway repository: a split of one file into two passes (a reordered const block
# included), and a changed struct tag, a changed comment, a dropped declaration, an unknown ref and an unknown
# package are refused.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
fail() { echo "✗ check_pure_move_test: $*" >&2; exit 1; }

repo="$tmp/repo"
mkdir -p "$repo/scripts" "$repo/backend/internal/p"
cp "$HERE/scripts/check_pure_move.sh" "$HERE/scripts/pure_move.go" "$repo/scripts/"
cd "$repo"
printf 'module example.com/m\n\ngo 1.24\n' > backend/go.mod
cat > backend/internal/p/p.go <<'EOF'
// Package p is a fixture.
package p

import "time"

// Kinds of a thing.
const (
	KindB = "b" // second
	KindA = "a"
)

// Thing is a thing.
type Thing struct {
	ID   int       `json:"id"`
	At   time.Time `json:"at"` // when
	Kind string    `json:"kind"`
}

// Name says what it is.
func (t Thing) Name() string { return t.Kind }

// Other is another thing.
type Other struct {
	N int `json:"n,omitempty"`
}
EOF
git init -q -b main; git config user.email t@example.com; git config user.name test
git add -A; git commit -q -m base

# split: Other and the kinds move to q.go, in the other order
cat > backend/internal/p/p.go <<'EOF'
// Package p is a fixture.
package p

import "time"

// Thing is a thing.
type Thing struct {
	ID   int       `json:"id"`
	At   time.Time `json:"at"` // when
	Kind string    `json:"kind"`
}

// Name says what it is.
func (t Thing) Name() string { return t.Kind }
EOF
cat > backend/internal/p/q.go <<'EOF'
package p

// Other is another thing.
type Other struct {
	N int `json:"n,omitempty"`
}

// Kinds of a thing.
const (
	KindB = "b" // second
	KindA = "a"
)
EOF
out=$(bash scripts/check_pure_move.sh HEAD internal/p 2>&1) || fail "a pure split was refused: $out"
case $out in *"pure move: yes"*) ;; *) fail "no verdict: $out" ;; esac

refuse() { # <what> <sed expression on q.go>
  cp backend/internal/p/q.go "$tmp/q.go"
  sed -i.bak "$2" backend/internal/p/q.go; rm -f backend/internal/p/q.go.bak
  if bash scripts/check_pure_move.sh HEAD internal/p >"$tmp/out" 2>&1; then fail "$1 was accepted"; fi
  grep -q 'pure move: NO' "$tmp/out" || fail "$1: no verdict: $(cat "$tmp/out")"
  cp "$tmp/q.go" backend/internal/p/q.go
}
refuse "a changed tag" 's/n,omitempty/n/'
refuse "a changed comment" 's|// second|// 2nd|'
refuse "a changed value" 's/KindA = "a"/KindA = "A"/'
refuse "a dropped declaration" '/^type Other/,/^}/d'

bash scripts/check_pure_move.sh nosuchref internal/p >/dev/null 2>&1 && fail "an unknown ref was accepted"
bash scripts/check_pure_move.sh HEAD internal/nosuch >/dev/null 2>&1 && fail "an unknown package was accepted"
echo "✓ check_pure_move_test"
