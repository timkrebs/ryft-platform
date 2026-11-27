package scoring

import (
	"maps"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestMuscleFreshnessAt(t *testing.T) {
	at := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	const day = 24 * time.Hour
	setsAt := func(count int, age time.Duration, primary, secondary []string) []store.MuscleSet {
		return slices.Repeat([]store.MuscleSet{{CompletedAt: at.Add(-age), Primary: primary, Secondary: secondary}}, count)
	}
	wantAt := func(freshness int, state string, age time.Duration, count int) model.MuscleFreshness {
		last := at.Add(-age)
		return model.MuscleFreshness{Freshness: freshness, State: state, LastTrainedAt: &last, Sets7d: count}
	}
	chest, quads := []string{"chest"}, []string{"quads"}
	for _, tc := range []struct {
		name string
		sets []store.MuscleSet
		last map[string]time.Time
		want map[string]model.MuscleFreshness
	}{
		{name: "no history"},
		{
			name: "ten primary chest sets",
			sets: setsAt(10, time.Second, chest, nil),
			want: map[string]model.MuscleFreshness{"chest": wantAt(40, "fatigued", time.Second, 10)},
		},
		// For ten primary quad sets at age h hours, F = 10*exp(-h/48).
		// With K = 10/ln(2.5), 100*exp(-F/K) is 57.36, 71.38, 81.51,
		// and 95.54 at 24, 48, 72, and 144 hours, respectively.
		{
			name: "quads after 24 hours",
			sets: setsAt(10, day, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(57, "recovering", day, 10)},
		},
		{
			name: "quads after 48 hours",
			sets: setsAt(10, 2*day, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(71, "recovering", 2*day, 10)},
		},
		{
			name: "quads after 72 hours",
			sets: setsAt(10, 3*day, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(82, "fresh", 3*day, 10)},
		},
		{
			name: "quads after 6 days",
			sets: setsAt(10, 6*day, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(96, "fresh", 6*day, 10)},
		},
		{
			name: "smaller muscles decay faster",
			sets: setsAt(10, day, []string{"chest", "forearms"}, nil),
			want: map[string]model.MuscleFreshness{
				"chest":    wantAt(60, "recovering", day, 10), // tau = 40.8 h
				"forearms": wantAt(67, "recovering", day, 10), // tau = 28.8 h
			},
		},
		{
			name: "ten secondary triceps sets",
			sets: setsAt(10, time.Second, nil, []string{"triceps"}),
			want: map[string]model.MuscleFreshness{"triceps": wantAt(63, "recovering", time.Second, 10)},
		},
		{
			// F = 10 × 1 × 0.3 = 3, 100 × exp(−3/K) = 75.97: a set, but a lighter one.
			name: "ten cardio calf intervals",
			sets: slices.Repeat([]store.MuscleSet{{CompletedAt: at.Add(-time.Second), Kind: model.KindCardio, Primary: []string{"calves"}}}, 10),
			want: map[string]model.MuscleFreshness{"calves": wantAt(76, "recovering", time.Second, 10)},
		},
		{
			name: "exactly 7 days old adds fatigue only",
			sets: setsAt(10, 7*day, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(97, "fresh", 7*day, 0)},
		},
		{
			name: "8 days old adds fatigue only",
			sets: setsAt(4, 8*day, quads, nil), // One set's load alone rounds to 100.
			want: map[string]model.MuscleFreshness{"quads": wantAt(99, "fresh", 8*day, 0)},
		},
		{
			name: "just inside 14 days adds fatigue",
			sets: setsAt(100, 14*day-time.Second, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(99, "fresh", 14*day-time.Second, 0)},
		},
		{
			name: "exactly 14 days old adds no fatigue",
			sets: setsAt(100, 14*day, quads, nil),
			want: map[string]model.MuscleFreshness{"quads": wantAt(100, "fresh", 14*day, 0)},
		},
		{
			name: "15 days old contributes only to all-time history",
			sets: setsAt(100, 15*day, quads, nil),
			last: map[string]time.Time{"chest": at.Add(-15 * day)},
			want: map[string]model.MuscleFreshness{
				"chest": wantAt(100, "fresh", 15*day, 0),
				"quads": wantAt(100, "fresh", 15*day, 0),
			},
		},
		{
			name: "sets at or after evaluation are ignored",
			sets: append(setsAt(1, 0, chest, nil), setsAt(1, -time.Second, quads, nil)...),
			last: map[string]time.Time{"chest": at, "quads": at.Add(time.Second)},
		},
		{
			name: "duplicate and unknown muscles",
			sets: setsAt(10, time.Second, []string{"chest", "chest", "unknown"}, []string{"chest", "unknown"}),
			want: map[string]model.MuscleFreshness{"chest": wantAt(40, "fatigued", time.Second, 10)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MuscleFreshnessAt(tc.sets, tc.last, at)
			if len(got) != 18 {
				t.Fatalf("got %d muscles, want 18", len(got))
			}
			for i, muscle := range model.Muscles {
				want, ok := tc.want[muscle]
				if !ok {
					want = model.MuscleFreshness{Freshness: 100, State: "fresh"}
				}
				want.Muscle = muscle
				if !reflect.DeepEqual(got[i], want) {
					t.Errorf("entry %d = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestFreshnessStateBoundaries(t *testing.T) {
	for _, tc := range []struct {
		score int
		state string
	}{{80, "fresh"}, {79, "recovering"}, {50, "recovering"}, {49, "fatigued"}} {
		if got := freshnessState(tc.score); got != tc.state {
			t.Errorf("freshnessState(%d) = %q, want %q", tc.score, got, tc.state)
		}
	}
}

func TestMuscleMassCoversVocabulary(t *testing.T) {
	got := slices.Sorted(maps.Keys(muscleMass))
	want := slices.Sorted(slices.Values(model.Muscles))
	if !slices.Equal(got, want) {
		t.Errorf("muscleMass keys = %v, want %v", got, want)
	}
}
