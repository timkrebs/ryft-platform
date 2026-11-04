// Package workoutdup holds the two rules that make one activity one workout
// (docs/SCORING.md → Several sources, one reading → workouts). They are pure
// functions of the rows, with no store and no clock, in a package of their own
// so that ingest and the store (which run the same-source fold, D146) can use
// them without importing the scoring engine, which imports the store.
//
//   - Same source (SameWorkout, FoldClusters): copies of one recording that a
//     source re-wrote under a new HealthKit UUID. Ingest deletes all but one.
//   - Different sources (CrossSourceDuplicates): a watch and a second tracker
//     that both recorded one run. Nothing is deleted; scoring marks every copy
//     but the primary with duplicate_of (D147).
package workoutdup

import (
	"bytes"
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// FoldTolerance: two copies of one source are one workout when their starts
// and their ends each lie within this of each other. A source that re-writes a
// workout (Garmin Connect on every sync) keeps its times to the second; 120 s
// is the brief's figure and leaves room for a re-write that rounds or trims a
// pause, while two real workouts of one activity are never this close at
// both ends.
const FoldTolerance = 120 * time.Second

// FoldOverlapNum/FoldOverlapDen: and the two copies must overlap by at least
// half of the shorter one. A re-write overlaps its original by ~100 %; without
// this, short workouts of one source and activity that follow each other within
// 120 s (intervals logged as six 45-s workouts 60 s apart) would chain into one
// component and five would be deleted, which cannot be undone. 50 % rather
// than the cross-source 80 %: within one source a copy is already pinned by
// both ends, the overlap only has to rule out "one after the other".
const FoldOverlapNum, FoldOverlapDen = 1, 2

// Copy is what the same-source fold reads of a stored workout. CreatedAt is
// the row's insert time, which the upsert never updates: the most recently
// uploaded copy is the one Health still has.
type Copy struct {
	ID        uuid.UUID
	Source    string
	Activity  string
	Start     time.Time
	End       time.Time
	CreatedAt time.Time
}

// SameWorkout says whether a and b are copies of one recording: the same
// source, the same activity, |Δstart| ≤ FoldTolerance and |Δend| ≤
// FoldTolerance (inclusive), and an overlap of at least half the shorter one
// (FoldOverlapNum/FoldOverlapDen, inclusive; integers, so 50 % exactly holds).
// Two copies without duration at the same instant are one. The user is the
// caller's scope. A workout scoring created for a session
// (model.SessionSource) is never a copy of anything.
func SameWorkout(a, b Copy) bool {
	if a.ID == b.ID || a.Source != b.Source || a.Activity != b.Activity || a.Source == model.SessionSource ||
		abs(a.Start.Sub(b.Start)) > FoldTolerance || abs(a.End.Sub(b.End)) > FoldTolerance {
		return false
	}
	shorter := min(a.End.Sub(a.Start), b.End.Sub(b.Start))
	overlap := minTime(a.End, b.End).Sub(maxTime(a.Start, b.Start))
	return FoldOverlapDen*int64(overlap) >= FoldOverlapNum*int64(shorter)
}

// Fold is one set of copies: Keep stays, Drop goes. Drop is never empty and is
// ordered by start, then id.
type Fold struct {
	Keep Copy
	Drop []Copy
}

// FoldClusters groups the copies by SameWorkout into connected components (A
// and C are one workout when A~B and B~C, even if A and C are 200 s apart) and
// keeps, per component, the highest CreatedAt, ties to the higher id. Only
// components of two or more are returned, ordered by their earliest start,
// then by the kept id. Duplicate ids in the input count once.
func FoldClusters(ws []Copy) []Fold {
	out, _ := FoldClustersCtx(context.Background(), ws)
	return out
}

// ctxEvery: FoldClustersCtx looks at ctx once per this many rows of the
// pairing loop (the only part that is not O(n log n)).
const ctxEvery = 256

// FoldClustersCtx is FoldClusters that stops when ctx is done and returns
// ctx.Err() with no folds: the whole-account fold (ingest fold-workouts) can
// meet thousands of rows, and many starts within 120 s of each other make the
// pairing quadratic.
func FoldClustersCtx(ctx context.Context, ws []Copy) ([]Fold, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	xs := slices.Clone(ws)
	slices.SortFunc(xs, func(a, b Copy) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	xs = slices.CompactFunc(xs, func(a, b Copy) bool { return a.ID == b.ID })

	parent := make([]int, len(xs))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range xs {
		if i%ctxEvery == ctxEvery-1 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		// Sorted by start: nothing after j can be within FoldTolerance of i's
		// start once j is not.
		for j := i + 1; j < len(xs) && xs[j].Start.Sub(xs[i].Start) <= FoldTolerance; j++ {
			if SameWorkout(xs[i], xs[j]) {
				if a, b := find(i), find(j); a != b {
					parent[max(a, b)] = min(a, b) // the root is the earliest member
				}
			}
		}
	}

	members := map[int][]Copy{}
	var roots []int
	for i := range xs {
		r := find(i)
		if _, ok := members[r]; !ok {
			roots = append(roots, r) // in start order, as i is
		}
		members[r] = append(members[r], xs[i])
	}
	var out []Fold
	for _, r := range roots {
		m := members[r]
		if len(m) < 2 {
			continue
		}
		keep := 0
		for k := 1; k < len(m); k++ {
			if newer(m[k], m[keep]) {
				keep = k
			}
		}
		f := Fold{Keep: m[keep]}
		for k, c := range m {
			if k != keep {
				f.Drop = append(f.Drop, c)
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// newer: the higher CreatedAt, ties to the higher id.
func newer(a, b Copy) bool {
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c > 0
	}
	return bytes.Compare(a.ID[:], b.ID[:]) > 0
}

// Cross-source rule (U3, D147).
const (
	// OverlapNum/OverlapDen: two recordings of different sources are one
	// workout when they overlap by at least 4/5 (80 %) of the shorter one. A
	// watch and a chest-strap app started a minute apart and stopped a minute
	// apart overlap by > 95 % on a 30-min run; an evening walk that only
	// touches the end of a run does not reach 80 %. Integers, so the boundary
	// is exact (no float rounding at 80 %).
	OverlapNum, OverlapDen = 4, 5
	// MinOwnHR: a recording "has heart rate" when its own source wrote at least
	// two heart-rate samples inside its window: one sample is a spot reading,
	// two make a curve (Strain needs two points as well).
	MinOwnHR = 2
	// ActivityOther matches every activity: a source that does not know the
	// sport writes "other" (API.md → Workout.activity).
	ActivityOther = "other"
)

// CrossSourceMatch says whether a and b are one workout recorded by two
// sources: different sources, the same activity (or either is "other"), and
// an overlap of at least 80 % of the shorter one's duration. Workouts scoring
// created for a session and workouts without a positive duration never match.
func CrossSourceMatch(a, b model.Workout) bool {
	if a.ID == b.ID || a.Source == b.Source || a.Source == model.SessionSource || b.Source == model.SessionSource {
		return false
	}
	if a.Activity != b.Activity && a.Activity != ActivityOther && b.Activity != ActivityOther {
		return false
	}
	shorter := min(a.End.Sub(a.Start), b.End.Sub(b.Start))
	if shorter <= 0 {
		return false
	}
	overlap := minTime(a.End, b.End).Sub(maxTime(a.Start, b.Start))
	// overlap ≥ 4/5 · shorter, in nanoseconds: 5 · 10 years still fits an int64.
	return OverlapDen*int64(overlap) >= OverlapNum*int64(shorter)
}

// OwnSourceHR counts the heart-rate samples of w's own source that start
// inside w's window [Start, End] (End inclusive, as the workout's load window).
// hr is raw: before DedupSources, which would hand an hour to the denser
// source and so decide nothing between two overlapping recordings.
func OwnSourceHR(w model.Workout, hr []model.Sample) int {
	n := 0
	for _, s := range hr {
		if s.Source == w.Source && !s.Start.Before(w.Start) && !s.Start.After(w.End) {
			n++
		}
	}
	return n
}

// HasCrossSourceMatch says whether w matches any other workout of ws, so the
// caller loads heart rate only for the workouts the decision needs it for.
func HasCrossSourceMatch(w model.Workout, ws []model.Workout) bool {
	for _, x := range ws {
		if CrossSourceMatch(w, x) {
			return true
		}
	}
	return false
}

// CrossSourceDuplicates decides which recordings are hidden behind which
// primary. It returns duplicate id → primary id; primaries and workouts
// without a match are absent. ownHR is OwnSourceHR per workout id (missing
// counts as 0).
//
// The rank of a recording (U3b): a specific activity before "other" (it
// says what was done: a Watch "strength" workout and a tracker's "other" of the
// same hour keep the strength one, and a session can attach to it), then at
// least MinOwnHR samples of its own heart rate (the one that was worn), then
// the longer one, then the lower id. "other" still matches anything, but it can
// only be hidden behind a specific activity, never hide one; two "other"s rank
// by the rest. The
// best-ranked recording not yet decided becomes a primary and every undecided
// recording that matches it directly points at it; repeat. So every duplicate
// points at a primary (no chains), a primary is never a duplicate, and a
// recording is hidden only behind one it overlaps itself: a watch that split a
// run in two halves behind one long tracker recording hides both halves when
// the tracker ranks first, but when one half ranks first only the tracker is
// hidden behind it and the other half stays.
func CrossSourceDuplicates(ws []model.Workout, ownHR map[uuid.UUID]int) map[uuid.UUID]uuid.UUID {
	xs := slices.Clone(ws)
	xs = slices.DeleteFunc(xs, func(w model.Workout) bool { return !HasCrossSourceMatch(w, ws) })
	slices.SortFunc(xs, func(a, b model.Workout) int {
		if sa, sb := a.Activity != ActivityOther, b.Activity != ActivityOther; sa != sb {
			if sa {
				return -1
			}
			return 1
		}
		ha, hb := ownHR[a.ID] >= MinOwnHR, ownHR[b.ID] >= MinOwnHR
		if ha != hb {
			if ha {
				return -1
			}
			return 1
		}
		if da, db := a.End.Sub(a.Start), b.End.Sub(b.Start); da != db {
			return cmp.Compare(db, da) // longer first
		}
		return bytes.Compare(a.ID[:], b.ID[:])
	})
	xs = slices.CompactFunc(xs, func(a, b model.Workout) bool { return a.ID == b.ID })

	out := map[uuid.UUID]uuid.UUID{}
	decided := map[uuid.UUID]bool{}
	for i, p := range xs {
		if decided[p.ID] {
			continue
		}
		decided[p.ID] = true // p is a primary (or alone by now)
		for _, d := range xs[i+1:] {
			if !decided[d.ID] && CrossSourceMatch(p, d) {
				out[d.ID] = p.ID
				decided[d.ID] = true
			}
		}
	}
	return out
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
