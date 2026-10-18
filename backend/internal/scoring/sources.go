package scoring

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// DedupSources keeps, for each sample type and clock hour, only the samples of
// the source with the most samples in that hour; ties go to the source whose
// name sorts first. HealthKit hands over every source's samples: an iPhone and
// a watch both count steps, and a Garmin and an Apple Watch both record heart
// rate, so summing would double the steps and mixing would interleave two
// different curves. Deciding per hour rather than per day keeps the phone's
// samples for the hours the watch was charging. Order is preserved.
func DedupSources(xs []model.Sample) []model.Sample {
	type bucket struct {
		typ  string
		hour int64
	}
	key := func(x model.Sample) bucket { return bucket{x.Type, x.Start.Truncate(time.Hour).Unix()} }
	counts := map[bucket]map[string]int{}
	multi := false
	for _, x := range xs {
		b := key(x)
		m := counts[b]
		if m == nil {
			m = map[string]int{}
			counts[b] = m
		}
		m[x.Source]++
		multi = multi || len(m) > 1
	}
	if !multi {
		return xs
	}
	winner := make(map[bucket]string, len(counts))
	for b, m := range counts {
		best, n := "", -1
		for src, c := range m {
			if c > n || (c == n && src < best) {
				best, n = src, c
			}
		}
		winner[b] = best
	}
	out := make([]model.Sample, 0, len(xs))
	for _, x := range xs {
		if winner[key(x)] == x.Source {
			out = append(out, x)
		}
	}
	return out
}

// SampleWindow widens [from, to) to whole hours. DedupSources decides per hour,
// so a workout's samples must be chosen with the whole hour in view, exactly as
// the day's strain sees them; WorkoutSamples then cuts them to the workout.
func SampleWindow(from, to time.Time) (time.Time, time.Time) {
	end := to.Truncate(time.Hour)
	if end.Before(to) {
		end = end.Add(time.Hour)
	}
	return from.Truncate(time.Hour), end
}

// WorkoutSamples de-duplicates the samples of a workout's SampleWindow and
// keeps those inside the workout window. Workout load, the HR curve and the
// day's strain therefore use the same source for every hour.
func WorkoutSamples(w model.Workout, around []model.Sample) []model.Sample {
	end := WorkoutWindowEnd(w)
	var out []model.Sample
	for _, s := range DedupSources(around) {
		if !s.Start.Before(w.Start) && s.Start.Before(end) {
			out = append(out, s)
		}
	}
	return out
}

// DedupSleepSources keeps one source's segments for a night. HealthKit only
// promises that one source's stages do not overlap, so a watch and a second
// tracker together would count the night twice. The winner has the most
// asleep minutes (stages or "asleep"), then the most minutes overall, then the
// name that sorts first. An iPhone that only logs time in bed loses to any
// tracker that measured sleep.
func DedupSleepSources(segs []model.SleepSegment) []model.SleepSegment {
	type score struct{ asleep, total time.Duration }
	by := map[string]*score{}
	for _, s := range segs {
		sc := by[s.Source]
		if sc == nil {
			sc = &score{}
			by[s.Source] = sc
		}
		d := s.End.Sub(s.Start)
		if d <= 0 {
			continue
		}
		sc.total += d
		switch s.Stage {
		case model.SleepDeep, model.SleepREM, model.SleepCore, model.SleepAsleep:
			sc.asleep += d
		}
	}
	if len(by) < 2 {
		return segs
	}
	best, bs := "", (*score)(nil)
	for src, sc := range by {
		if bs == nil || sc.asleep > bs.asleep || (sc.asleep == bs.asleep && sc.total > bs.total) ||
			(sc.asleep == bs.asleep && sc.total == bs.total && src < best) {
			best, bs = src, sc
		}
	}
	out := make([]model.SleepSegment, 0, len(segs))
	for _, s := range segs {
		if s.Source == best {
			out = append(out, s)
		}
	}
	return out
}

// DayTotal sums a cumulative sample type (steps, active energy) after
// DedupSources, so overlapping devices are counted once, rounded to a whole
// number. A sample counts on the day it starts.
func DayTotal(xs []model.Sample) int {
	var sum float64
	for _, x := range DedupSources(xs) {
		sum += x.Value
	}
	return int(math.Round(sum))
}
