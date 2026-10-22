package main

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestCatchUpTakesTurns(t *testing.T) {
	a := uuid.MustParse("10000000-0000-0000-0000-000000000000")
	b := uuid.MustParse("20000000-0000-0000-0000-000000000000")
	c := uuid.MustParse("30000000-0000-0000-0000-000000000000")
	for _, tc := range []struct {
		name string
		from uuid.UUID
		want []uuid.UUID
	}{
		{"from the start", uuid.Nil, []uuid.UUID{a, b, c}},
		{"from b", b, []uuid.UUID{b, c, a}},
		{"from an id that left: the next one", uuid.MustParse("25000000-0000-0000-0000-000000000000"), []uuid.UUID{c, a, b}},
		{"past the last: wraps", uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"), []uuid.UUID{a, b, c}},
	} {
		if got := startAt([]uuid.UUID{c, a, b}, tc.from); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := startAt(nil, b); len(got) != 0 {
		t.Errorf("no users: %v", got)
	}

	cu := catchUp{next: b, failed: map[uuid.UUID]string{}}
	if got := cu.order([]uuid.UUID{a, b, c}); got[0] != b || cu.next != uuid.Nil {
		t.Fatalf("a pass starts where the last ran out, the next from the start unless this one runs out: %v, %v", got, cu.next)
	}
}
