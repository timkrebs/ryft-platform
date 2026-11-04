package workoutdup

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

// The reporter's evening run, 2026-10-20 18:00 local (UTC here).
var t0 = time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)

// id makes readable, ordered ids: id(1) < id(2) byte-wise, as in Postgres.
func id(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", n)) }

func at(min float64) time.Time { return t0.Add(time.Duration(min * float64(time.Minute))) }

const garmin, watch, phone = "Garmin Connect", "Tim's Apple Watch", "Strava"

// copyOf is a 45-min Garmin run stored at the given upload time.
func copyOf(n int, dStart, dEnd time.Duration, uploaded time.Time) Copy {
	return Copy{ID: id(n), Source: garmin, Activity: "running", Start: t0.Add(dStart), End: t0.Add(45*time.Minute + dEnd),
		CreatedAt: uploaded}
}

// The same-source rule at its boundary, 120 s vs 121 s, on both ends and in
// both directions. Garmin re-writes keep the times to the second, so every
// real copy sits far inside; the boundary is pinned so it cannot drift.
func TestSameWorkoutBoundary(t *testing.T) {
	up := t0.Add(2 * time.Hour)
	base := copyOf(1, 0, 0, up)
	cases := []struct {
		name       string
		dStart     time.Duration
		dEnd       time.Duration
		mutate     func(*Copy)
		sameAsBase bool
	}{
		{"identical re-write", 0, 0, nil, true},
		{"start +120 s, end +120 s", 120 * time.Second, 120 * time.Second, nil, true},
		{"start -120 s, end -120 s", -120 * time.Second, -120 * time.Second, nil, true},
		{"start +121 s", 121 * time.Second, 0, nil, false},
		{"start -121 s", -121 * time.Second, 0, nil, false},
		{"end +121 s", 0, 121 * time.Second, nil, false},
		{"end -121 s", 0, -121 * time.Second, nil, false},
		{"start +120 s, end +121 s", 120 * time.Second, 121 * time.Second, nil, false},
		{"start +120.000001 s (µs, as Postgres stores)", 120*time.Second + time.Microsecond, 0, nil, false},
		{"other activity", 0, 0, func(c *Copy) { c.Activity = "walking" }, false},
		{"other source", 0, 0, func(c *Copy) { c.Source = watch }, false},
		{"scoring's session workout", 0, 0, func(c *Copy) { c.Source = model.SessionSource }, false},
		{"the same row", 0, 0, func(c *Copy) { c.ID = base.ID }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			other := copyOf(2, c.dStart, c.dEnd, up)
			b := base
			if c.mutate != nil {
				c.mutate(&other)
				if other.Source == model.SessionSource {
					b.Source = model.SessionSource // both scoring's: still never folded
				}
			}
			if got := SameWorkout(b, other); got != c.sameAsBase {
				t.Fatalf("SameWorkout = %v, want %v", got, c.sameAsBase)
			}
			if SameWorkout(other, b) != SameWorkout(b, other) {
				t.Fatal("SameWorkout is symmetric")
			}
		})
	}
}

// The overlap condition (D146 amendment): both ends within 120 s are not
// enough when the two do not overlap by at least half the shorter one. 200-s
// copies 100 s apart overlap by exactly 50 % (one); 102 s apart by 49 % (two),
// although both ends are within 120 s.
func TestSameWorkoutOverlap(t *testing.T) {
	up := t0.Add(time.Hour)
	short := func(n int, from time.Duration) Copy {
		return Copy{ID: id(n), Source: garmin, Activity: "running", Start: t0.Add(from), End: t0.Add(from + 200*time.Second), CreatedAt: up}
	}
	cases := []struct {
		name string
		b    Copy
		want bool
	}{
		{"50 % of the shorter (100 of 200 s)", short(2, 100*time.Second), true},
		{"49 % of the shorter (98 of 200 s)", short(2, 102*time.Second), false},
		{"one right after the other, 60 s gap", short(2, 260*time.Second), false},
		{"no duration at the first one's start (its end is 200 s off)", Copy{ID: id(2), Source: garmin, Activity: "running", Start: t0, End: t0, CreatedAt: up}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SameWorkout(short(1, 0), c.b); got != c.want {
				t.Fatalf("SameWorkout = %v, want %v", got, c.want)
			}
		})
	}
	zero := func(n int) Copy {
		return Copy{ID: id(n), Source: garmin, Activity: "running", Start: t0, End: t0, CreatedAt: up}
	}
	if !SameWorkout(zero(1), zero(2)) {
		t.Fatal("two copies without duration at the same instant are one")
	}
}

// The probe that found the gap: intervals logged as six 45-s workouts, 60 s
// apart (start to start), one source, one activity. Neighbours are within
// 120 s at both ends, but they never overlap, so nothing is folded. Before the
// overlap condition they chained into one component and five were deleted.
func TestFoldClustersKeepsBackToBackIntervals(t *testing.T) {
	var in []Copy
	for k := range 6 {
		s := t0.Add(time.Duration(k) * time.Minute)
		in = append(in, Copy{ID: id(k + 1), Source: watch, Activity: "running", Start: s, End: s.Add(45 * time.Second),
			CreatedAt: t0.Add(time.Hour)})
	}
	if abs(in[0].Start.Sub(in[1].Start)) > FoldTolerance || abs(in[0].End.Sub(in[1].End)) > FoldTolerance {
		t.Fatal("precondition: neighbours are within 120 s at both ends")
	}
	if got := FoldClusters(in); len(got) != 0 {
		t.Fatalf("intervals folded: %+v", got)
	}
	// The same six, each also re-written once by the source: six folds of one.
	var twice []Copy
	for k, c := range in {
		re := c
		re.ID, re.CreatedAt = id(100+k+1), c.CreatedAt.Add(time.Hour)
		twice = append(twice, c, re)
	}
	got := FoldClusters(twice)
	if len(got) != 6 {
		t.Fatalf("got %d folds, want 6: %+v", len(got), got)
	}
	for k, f := range got {
		if f.Keep.ID != id(100+k+1) || len(f.Drop) != 1 || f.Drop[0].ID != id(k+1) {
			t.Fatalf("fold %d: %+v", k, f)
		}
	}
}

func TestFoldClustersCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	in := []Copy{copyOf(1, 0, 0, t0), copyOf(2, 0, 0, t0)}
	if got, err := FoldClustersCtx(ctx, in); err == nil || got != nil {
		t.Fatalf("a done context: %v %v", got, err)
	}
	// Many starts within 120 s (the quadratic case): cancelled midway.
	many := make([]Copy, 5000)
	for k := range many {
		many[k] = Copy{ID: uuid.New(), Source: garmin, Activity: "running", Start: t0.Add(time.Duration(k) * 10 * time.Millisecond),
			End: t0.Add(time.Hour), CreatedAt: t0}
	}
	if got, err := FoldClustersCtx(context.Background(), many); err != nil || len(got) != 1 || len(got[0].Drop) != 4999 {
		t.Fatalf("5000 copies of one: %d folds, %v", len(got), err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Microsecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if _, err := FoldClustersCtx(ctx, many); err == nil {
		t.Fatal("a cancelled whole-account fold returns ctx.Err()")
	}
}

// countCtx is alive for its first Err call (the entry check) and cancelled from the second
// on: the cancellation can only be seen by the check inside the pairing loop.
type countCtx struct {
	context.Context
	calls int
}

func (c *countCtx) Err() error {
	c.calls++
	if c.calls > 1 {
		return context.Canceled
	}
	return nil
}

func TestFoldClustersCtxCancelledInsideThePairingLoop(t *testing.T) {
	many := make([]Copy, 5000)
	for k := range many {
		many[k] = Copy{ID: uuid.New(), Source: garmin, Activity: "running", Start: t0.Add(time.Duration(k) * 10 * time.Millisecond),
			End: t0.Add(time.Hour), CreatedAt: t0}
	}
	ctx := &countCtx{Context: context.Background()}
	got, err := FoldClustersCtx(ctx, many)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("cancelled after the entry check: %d folds, %v, want context.Canceled and no result", len(got), err)
	}
	if ctx.calls < 2 {
		t.Fatalf("the context was asked %d times: the loop never checks it", ctx.calls)
	}
}

func ids(cs []Copy) []uuid.UUID {
	out := make([]uuid.UUID, len(cs))
	for k, c := range cs {
		out[k] = c.ID
	}
	return out
}

// The reporter's account on 2026-10-20: Garmin Connect re-wrote its run four
// times, its walk three times and its strength workout twice across syncs, and
// the app kept every UUID. 9 rows, 3 workouts, so 6 folds (the brief's "expect
// 6 folds"). The Watch's own run of the same evening is another source and is
// not touched here (that is CrossSourceDuplicates' job).
func TestFoldClustersReportersDay(t *testing.T) {
	sync := func(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }
	walk := func(n int, up time.Time) Copy {
		return Copy{ID: id(n), Source: garmin, Activity: "walking", Start: at(-480), End: at(-450), CreatedAt: up}
	}
	lift := func(n int, up time.Time) Copy {
		return Copy{ID: id(n), Source: garmin, Activity: "strength", Start: at(-240), End: at(-180), CreatedAt: up}
	}
	in := []Copy{
		copyOf(11, 0, 0, sync(1)), copyOf(12, 0, 0, sync(3)), copyOf(13, 0, 0, sync(5)), copyOf(14, 0, 0, sync(2)),
		walk(21, sync(-6)), walk(22, sync(1)), walk(23, sync(3)),
		lift(31, sync(-2)), lift(32, sync(-3)),
		{ID: id(41), Source: watch, Activity: "running", Start: at(0), End: at(45), CreatedAt: sync(1)},
	}
	got := FoldClusters(in)
	want := []struct {
		keep uuid.UUID
		drop []uuid.UUID
	}{
		{id(23), []uuid.UUID{id(21), id(22)}},         // walk, earliest; newest upload is 23
		{id(31), []uuid.UUID{id(32)}},                 // strength; 31 uploaded after 32
		{id(13), []uuid.UUID{id(11), id(12), id(14)}}, // run; 13 is the newest (sync 5)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d folds, want %d: %+v", len(got), len(want), got)
	}
	folded := 0
	for k, w := range want {
		if got[k].Keep.ID != w.keep || !slices.Equal(ids(got[k].Drop), w.drop) {
			t.Fatalf("fold %d: keep %v drop %v, want keep %v drop %v", k, got[k].Keep.ID, ids(got[k].Drop), w.keep, w.drop)
		}
		folded += len(got[k].Drop)
	}
	if folded != 6 {
		t.Fatalf("folded %d, want 6", folded)
	}
	// A second run over the survivors folds nothing (idempotent).
	var kept []Copy
	for _, f := range got {
		kept = append(kept, f.Keep)
	}
	kept = append(kept, in[len(in)-1])
	if again := FoldClusters(kept); len(again) != 0 {
		t.Fatalf("second run folds %+v", again)
	}
}

func TestFoldClustersRules(t *testing.T) {
	up := t0.Add(time.Hour)
	t.Run("connected components: A~B and B~C fold A and C, 200 s apart", func(t *testing.T) {
		a, b, c := copyOf(1, 0, 0, up), copyOf(2, 100*time.Second, 100*time.Second, up), copyOf(3, 200*time.Second, 200*time.Second, up)
		if SameWorkout(a, c) {
			t.Fatal("precondition: A and C are not directly one")
		}
		got := FoldClusters([]Copy{c, a, b})
		if len(got) != 1 || got[0].Keep.ID != id(3) || !slices.Equal(ids(got[0].Drop), []uuid.UUID{id(1), id(2)}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("same upload time: the higher id stays", func(t *testing.T) {
		got := FoldClusters([]Copy{copyOf(7, 0, 0, up), copyOf(5, 0, 0, up), copyOf(6, 0, 0, up)})
		if len(got) != 1 || got[0].Keep.ID != id(7) || !slices.Equal(ids(got[0].Drop), []uuid.UUID{id(5), id(6)}) {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("121 s apart at one end: two workouts, nothing folded", func(t *testing.T) {
		if got := FoldClusters([]Copy{copyOf(1, 0, 0, up), copyOf(2, 0, 121*time.Second, up)}); len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("an id listed twice counts once", func(t *testing.T) {
		if got := FoldClusters([]Copy{copyOf(1, 0, 0, up), copyOf(1, 0, 0, up)}); len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := FoldClusters(nil); len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

// wk is a workout from minute a to minute b of t0.
func wk(n int, source, activity string, a, b float64) model.Workout {
	return model.Workout{ID: id(n), Source: source, Activity: activity, Start: at(a), End: at(b)}
}

// The 80 % boundary: the shorter one is 100 min, an overlap of 80 min is one
// workout and 79 min is two. Integer arithmetic, so 80 % exactly holds.
func TestCrossSourceMatchBoundary(t *testing.T) {
	cases := []struct {
		name string
		a, b model.Workout
		want bool
	}{
		{"80 % of the shorter (80 of 100 min)", wk(1, watch, "running", 0, 100), wk(2, garmin, "running", 20, 120), true},
		{"79 % of the shorter (79 of 100 min)", wk(1, watch, "running", 0, 100), wk(2, garmin, "running", 21, 121), false},
		{"80 % of the shorter, not of the longer (24 of 30 min, 24 of 90)", wk(1, watch, "running", 0, 90), wk(2, garmin, "running", 66, 96), true},
		{"79.97 % of the shorter (23.99 of 30 min)", wk(1, watch, "running", 0, 90), wk(2, garmin, "running", 66.01, 96.01), false},
		{"inside the other: 100 %", wk(1, watch, "running", 0, 60), wk(2, garmin, "running", 10, 40), true},
		{"Watch and Garmin, a minute apart at both ends", wk(1, watch, "running", 0, 45), wk(2, garmin, "running", 1, 46), true},
		{"other activity", wk(1, watch, "running", 0, 45), wk(2, garmin, "cycling", 0, 45), false},
		{"either is other (wildcard)", wk(1, watch, "running", 0, 45), wk(2, phone, "other", 0, 45), true},
		{"other is other", wk(1, watch, "other", 0, 45), wk(2, phone, "other", 0, 45), true},
		{"the same source never (that is the fold)", wk(1, garmin, "running", 0, 45), wk(2, garmin, "running", 0, 45), false},
		{"scoring's session workout never", wk(1, model.SessionSource, "strength", 0, 60), wk(2, watch, "strength", 0, 60), false},
		{"no overlap", wk(1, watch, "running", 0, 45), wk(2, garmin, "running", 50, 95), false},
		{"zero-length workout", wk(1, watch, "running", 10, 10), wk(2, garmin, "running", 0, 45), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := CrossSourceMatch(c.a, c.b); got != c.want {
				t.Fatalf("CrossSourceMatch = %v, want %v", got, c.want)
			}
			if CrossSourceMatch(c.b, c.a) != c.want {
				t.Fatal("CrossSourceMatch is symmetric")
			}
		})
	}
}

// Primary choice and the shape of the result: every duplicate points at a
// primary, no chains, primaries absent from the map.
func TestCrossSourceDuplicates(t *testing.T) {
	hr := func(kv ...int) map[uuid.UUID]int {
		m := map[uuid.UUID]int{}
		for k := 0; k+1 < len(kv); k += 2 {
			m[id(kv[k])] = kv[k+1]
		}
		return m
	}
	cases := []struct {
		name string
		ws   []model.Workout
		hr   map[uuid.UUID]int
		want map[uuid.UUID]uuid.UUID
	}{
		{
			// A 45-min evening run on both: the Watch wrote 45 own HR samples
			// (1/min while running), Garmin Connect shares no HR with Health.
			// The one that was worn wins although Garmin's is a minute longer.
			"Watch with HR, Garmin without: the Watch is primary although shorter",
			[]model.Workout{wk(1, garmin, "running", -1, 45), wk(2, watch, "running", 0, 45)},
			hr(2, 45), map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			"both with HR: the longer is primary",
			[]model.Workout{wk(1, watch, "running", 0, 45), wk(2, garmin, "running", -1, 45)},
			hr(1, 45, 2, 46), map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			"both with HR, same length: the lower id is primary",
			[]model.Workout{wk(2, watch, "running", 0, 45), wk(1, garmin, "running", 1, 46)},
			hr(1, 45, 2, 45), map[uuid.UUID]uuid.UUID{id(2): id(1)},
		},
		{
			"neither with HR: the longer is primary",
			[]model.Workout{wk(1, phone, "running", 0, 40), wk(2, garmin, "running", 0, 45)},
			nil, map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			// One sample is a spot reading, not a recording; MinOwnHR is 2.
			"1 own HR sample is no HR, 2 are",
			[]model.Workout{wk(1, watch, "running", 0, 40), wk(2, garmin, "running", 0, 45)},
			hr(1, 2, 2, 1), map[uuid.UUID]uuid.UUID{id(2): id(1)},
		},
		{
			"1 own HR sample on both: the longer",
			[]model.Workout{wk(1, watch, "running", 0, 40), wk(2, garmin, "running", 0, 45)},
			hr(1, 1, 2, 1), map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			// Watch, Garmin and a phone app all recorded one ride: both others
			// point at the Watch, none at the Garmin (no chains).
			"three sources: every duplicate points at the primary",
			[]model.Workout{wk(1, phone, "other", 0, 60), wk(2, garmin, "cycling", 2, 58), wk(3, watch, "cycling", 1, 59)},
			hr(3, 58, 2, 56), map[uuid.UUID]uuid.UUID{id(1): id(3), id(2): id(3)},
		},
		{
			"79 % overlap: two workouts, both shown",
			[]model.Workout{wk(1, watch, "running", 0, 100), wk(2, garmin, "running", 21, 121)},
			hr(1, 100), map[uuid.UUID]uuid.UUID{},
		},
		{
			"80 % overlap: one workout",
			[]model.Workout{wk(1, watch, "running", 0, 100), wk(2, garmin, "running", 20, 120)},
			hr(1, 100), map[uuid.UUID]uuid.UUID{id(2): id(1)},
		},
		{
			// A run on the Watch and a ride on Garmin at once: different
			// activities, neither is "other", so both stay.
			"different activities: both stay",
			[]model.Workout{wk(1, watch, "running", 0, 45), wk(2, garmin, "cycling", 0, 45)},
			hr(1, 45, 2, 45), map[uuid.UUID]uuid.UUID{},
		},
		{
			"scoring's session workout is never hidden and hides nothing",
			[]model.Workout{wk(1, model.SessionSource, "strength", 0, 60), wk(2, watch, "other", 0, 60)},
			hr(2, 60), map[uuid.UUID]uuid.UUID{},
		},
		{
			// The Watch split the run in two (a pause it ended), Garmin kept one.
			// Garmin has HR and is longer: it hides both halves.
			"split run, tracker ranks first: both halves hidden",
			[]model.Workout{wk(1, watch, "running", 0, 30), wk(2, watch, "running", 31, 60), wk(3, garmin, "running", 0, 60)},
			hr(1, 30, 2, 29, 3, 60), map[uuid.UUID]uuid.UUID{id(1): id(3), id(2): id(3)},
		},
		{
			// Garmin without HR: the first half ranks first and hides Garmin; the
			// second half does not overlap the first, so it stays (not a chain).
			"split run, a half ranks first: only the tracker is hidden",
			[]model.Workout{wk(1, watch, "running", 0, 30), wk(2, watch, "running", 31, 60), wk(3, garmin, "running", 0, 60)},
			hr(1, 30, 2, 29), map[uuid.UUID]uuid.UUID{id(3): id(1)},
		},
		{
			// U3b: the Watch logged "strength" (and the session attaches to
			// it), Garmin the same hour as "other", longer and with heart rate.
			// The specific activity ranks first: Garmin's is hidden.
			"specific activity before other, ahead of HR and length",
			[]model.Workout{wk(1, garmin, "other", -5, 65), wk(2, watch, "strength", -2, 61)},
			hr(1, 70), map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			"specific activity before other, both with HR",
			[]model.Workout{wk(1, garmin, "other", -5, 65), wk(2, watch, "strength", -2, 61)},
			hr(1, 70, 2, 63), map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			// Two "other"s rank as before: own HR, then the longer.
			"two others: own HR first",
			[]model.Workout{wk(1, garmin, "other", -5, 65), wk(2, phone, "other", -2, 61)},
			hr(2, 63), map[uuid.UUID]uuid.UUID{id(1): id(2)},
		},
		{
			"two others, neither with HR: the longer",
			[]model.Workout{wk(1, garmin, "other", -5, 65), wk(2, phone, "other", -2, 61)},
			nil, map[uuid.UUID]uuid.UUID{id(2): id(1)},
		},
		{
			// An "other" never hides a specific one, even when the two
			// specific ones do not match each other (run vs ride): the
			// "other" goes behind the first-ranked of them, the other stays.
			"other between two specific ones that differ",
			[]model.Workout{wk(1, phone, "other", 0, 60), wk(2, watch, "running", 0, 60), wk(3, garmin, "cycling", 0, 60)},
			hr(1, 60, 3, 60), map[uuid.UUID]uuid.UUID{id(1): id(3)},
		},
		{
			"nothing to decide",
			[]model.Workout{wk(1, watch, "running", 0, 45)},
			hr(1, 45), map[uuid.UUID]uuid.UUID{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CrossSourceDuplicates(c.ws, c.hr)
			if !maps.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for d, p := range got {
				if _, chained := got[p]; chained {
					t.Fatalf("%v points at %v, which is itself a duplicate", d, p)
				}
			}
			// The order rows arrive in decides nothing.
			rev := slices.Clone(c.ws)
			slices.Reverse(rev)
			if again := CrossSourceDuplicates(rev, c.hr); !maps.Equal(again, got) {
				t.Fatalf("input order changed the result: %v vs %v", again, got)
			}
		})
	}
}

// Late heart rate and deletions re-decide, because the caller clears and
// re-sets duplicate_of on every pass: a Garmin run alone is shown; the Watch's
// copy arrives without its HR (another sync) and is hidden behind the longer
// Garmin; its HR arrives and it becomes the primary; Garmin's is deleted and
// the Watch's stands alone.
func TestCrossSourceDuplicatesHealsOnRescore(t *testing.T) {
	g, w := wk(1, garmin, "running", -1, 45), wk(2, watch, "running", 0, 45)
	steps := []struct {
		ws   []model.Workout
		hr   map[uuid.UUID]int
		want map[uuid.UUID]uuid.UUID
	}{
		{[]model.Workout{g}, nil, map[uuid.UUID]uuid.UUID{}},
		{[]model.Workout{g, w}, nil, map[uuid.UUID]uuid.UUID{id(2): id(1)}},
		{[]model.Workout{g, w}, map[uuid.UUID]int{id(2): 45}, map[uuid.UUID]uuid.UUID{id(1): id(2)}},
		{[]model.Workout{w}, map[uuid.UUID]int{id(2): 45}, map[uuid.UUID]uuid.UUID{}},
	}
	for k, s := range steps {
		if got := CrossSourceDuplicates(s.ws, s.hr); !maps.Equal(got, s.want) {
			t.Fatalf("step %d: got %v, want %v", k, got, s.want)
		}
	}
}

func TestOwnSourceHR(t *testing.T) {
	w := wk(1, watch, "running", 0, 45)
	s := func(source string, min float64) model.Sample {
		return model.Sample{Type: model.SampleHeartRate, Start: at(min), End: at(min), Value: 140, Source: source}
	}
	hr := []model.Sample{
		s(watch, -0.5),  // before the start
		s(watch, 0),     // at the start: in
		s(watch, 20),    // in
		s(garmin, 20),   // another source's: never its own
		s(watch, 45),    // at the end: in (the load window's end is inclusive)
		s(watch, 45.01), // after the end
	}
	if got := OwnSourceHR(w, hr); got != 3 {
		t.Fatalf("OwnSourceHR = %d, want 3", got)
	}
	if got := OwnSourceHR(wk(2, garmin, "running", 0, 45), hr); got != 1 {
		t.Fatalf("Garmin's own = %d, want 1", got)
	}
}
