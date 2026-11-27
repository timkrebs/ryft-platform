package energy

import (
	"math"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/wearables"
)

// reconciledLoad is the coaching load the store integrates from a set of recordings: Reconcile, then each selected
// recording with its rating (store.loadCoaching, D293).
func reconciledLoad(in []wearables.Workout, p Physiology) (float64, int) {
	got := wearables.Reconcile(in)
	load := 0.
	for _, w := range got {
		load += RatedWorkout(w, p, w.RPE).Load()
	}
	return load, len(got)
}

// TestReconcileCarriesTheRating pins that a rating on a dropped copy of a session reaches the copy Reconcile keeps:
// a partner recording (priority 2) beats the public copy the user rated (priority 4), and before the fix the rating
// vanished with it (load 0).
func TestReconcileCarriesTheRating(t *testing.T) {
	start := time.Date(2026, 11, 25, 17, 0, 0, 0, time.UTC)
	p := Physiology{MassKG: 70, RMR: 1680, RestHR: 55, MaxHR: 190}
	partner := wearables.Workout{Provider: wearables.GarminJSON, ExternalID: "g-1", Sport: "running", Start: start, End: start.Add(time.Hour)}
	public := wearables.Workout{Provider: wearables.HealthKit, ExternalID: "w-1", Source: "RYFT", Sport: "running",
		Start: start.Add(time.Minute), End: start.Add(61 * time.Minute), RPE: 8}

	// Unrated partner copy without heart rate or power wins: the rating counts once, 60 min at 8 → 151 (sex
	// unspecified, the Session RPE table in SCORING.md).
	load, n := reconciledLoad([]wearables.Workout{public, partner}, p)
	if n != 1 || math.Round(load) != 151 {
		t.Errorf("rated public copy under an unrated partner copy: %d recordings, load %v, want 1 and 151", n, load)
	}

	// A second rated copy of the same session (a manual entry, priority 5, rated 6) does not add a second load: the
	// first rating in priority order (the public copy's 8) is the one carried.
	manual := wearables.Workout{Provider: wearables.Manual, ExternalID: "w-2", Source: "manual", Sport: "running",
		Start: start, End: start.Add(time.Hour), RPE: 6}
	if load, n := reconciledLoad([]wearables.Workout{manual, public, partner}, p); n != 1 || math.Round(load) != 151 {
		t.Errorf("two rated copies: %d recordings, load %v, want 1 and 151", n, load)
	}

	// A measured partner copy still wins over the rating: its heart-rate TRIMP, reserve (122.5 − 55)/135 = 0.5 →
	// 60 · 0.5 · 0.75 · e^0.8975 ≈ 55.2, not the rating's 151.
	measured := partner
	measured.AvgHR = 122.5
	want := 60 * .5 * .75 * math.Exp(.8975)
	if load, n := reconciledLoad([]wearables.Workout{public, measured}, p); n != 1 || math.Abs(load-want) > 1e-8 {
		t.Errorf("measured partner copy: %d recordings, load %v, want 1 and %v", n, load, want)
	}

	// A partner copy split by a FIT recording of its middle 20 min (heart rate measured) carries the rating on both
	// remaining pieces: the FIT's TRIMP plus the uncovered 40 min at 8 once (2 × 20 min, linear in minutes ≈ 100.7).
	fit := wearables.Workout{Provider: wearables.GarminFIT, ExternalID: "f-1", Sport: "running",
		Start: start.Add(20 * time.Minute), End: start.Add(40 * time.Minute), AvgHR: 122.5}
	load, n = reconciledLoad([]wearables.Workout{public, partner, fit}, p)
	fitLoad := RatedWorkout(fit, p, 0).Load()
	rated := SessionRPETRIMP(40, 8, p.Sex)
	if n != 3 || math.Abs(load-(fitLoad+rated)) > 1e-8 {
		t.Errorf("split partner copy: %d recordings, load %v, want 3 and %v", n, load, fitLoad+rated)
	}
}
