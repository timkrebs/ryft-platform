package scoring

import (
	"math"
	"slices"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Muscle freshness (D309; docs/SCORING.md → Muscle freshness). Pure.
//
//	F_m(t)    = Σ w × exp(−(t − completed_at) / τ_m)   working sets in (t − 14 d, t)
//	w         = 1 primary, ½ secondary, × 0.3 for a cardio exercise
//	τ_m       = 24 h + 24 h × muscleMass[m]
//	freshness = round(100 × exp(−F_m / K)),  K = 10 / ln 2.5: ten primary sets just done leave 40
const (
	// FreshnessWindow bounds the age of sets that contribute to fatigue (D309).
	FreshnessWindow = 14 * 24 * time.Hour
	freshnessWeek   = 7 * 24 * time.Hour
)

var freshnessScale = 10 / math.Log(2.5)

// MuscleFreshnessAt scores working sets and all-time training history as of t.
// Callers check for a completed session before using the result.
func MuscleFreshnessAt(sets []store.MuscleSet, last map[string]time.Time, t time.Time) []model.MuscleFreshness {
	out := make([]model.MuscleFreshness, len(model.Muscles))
	windowStart, weekStart := t.Add(-FreshnessWindow), t.Add(-freshnessWeek)
	for i, muscle := range model.Muscles {
		entry := model.MuscleFreshness{Muscle: muscle}
		tauHours := 24 + 24*muscleMass[muscle]
		latest, trained := last[muscle]
		trained = trained && latest.Before(t)
		var fatigue float64
		for _, set := range sets {
			at := set.CompletedAt
			if !at.Before(t) {
				continue
			}
			var weight float64
			switch {
			case slices.Contains(set.Primary, muscle):
				weight = 1
			case slices.Contains(set.Secondary, muscle):
				weight = 0.5
			default:
				continue
			}
			if set.Kind == model.KindCardio {
				// A rowing or jump-rope interval tires the muscles less than a hard set: Strength's cardio load factor.
				weight *= DefaultLoadFactor(model.KindCardio)
			}
			if !trained || at.After(latest) {
				latest, trained = at, true
			}
			if at.After(windowStart) {
				fatigue += weight * math.Exp(-t.Sub(at).Hours()/tauHours)
			}
			if at.After(weekStart) {
				entry.Sets7d++
			}
		}
		if trained {
			entry.LastTrainedAt = &latest
		}
		entry.Freshness = min(100, max(0, int(math.Round(100*math.Exp(-fatigue/freshnessScale)))))
		entry.State = freshnessState(entry.Freshness)
		out[i] = entry
	}
	return out
}

func freshnessState(freshness int) string {
	switch {
	case freshness >= 80:
		return model.FreshnessFresh
	case freshness >= 50:
		return model.FreshnessRecovering
	default:
		return model.FreshnessFatigued
	}
}
