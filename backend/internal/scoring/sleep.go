package scoring

import (
	"math"
	"sort"
	"time"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Sleep screen: pure functions that turn stored night totals and raw HealthKit
// segments into the typed objects the API serves. Scoring stores the totals
// (compute.go); the api service assembles the views on read.

// NightWindow is the span in which a sleep segment must end to count for local
// day d (midnight in the user's zone): d-1 20:00 to d 13:00.
func NightWindow(d time.Time) (from, to time.Time) {
	return d.Add(nightStartOffset), d.Add(nightEndOffset)
}

// Sleep quality bands. Quality is sleep performance for now; the brand board
// shows 82 % as "Good".
const (
	sleepQualityGood = 80
	sleepQualityFair = 60
)

func SleepQualityLabel(quality int) string {
	switch {
	case quality >= sleepQualityGood:
		return model.QualityGood
	case quality >= sleepQualityFair:
		return model.QualityFair
	default:
		return model.QualityPoor
	}
}

// InBedMinutes is the time in bed efficiency is measured against: the inbed
// segments when the source records them, otherwise the span from the first to
// the last segment — and never less than asleep + awake.
func InBedMinutes(t SleepTotals) int {
	inBed := t.InBedMin
	if inBed == 0 && !t.Start.IsZero() {
		inBed = int(math.Round(t.End.Sub(t.Start).Minutes()))
	}
	return max(inBed, t.AsleepMin+t.AwakeMin)
}

// SleepEfficiency is asleep / in bed as a whole percentage, 0 without a bed time.
func SleepEfficiency(totalMin, inBedMin int) int {
	if inBedMin <= 0 {
		return 0
	}
	return int(math.Round(clamp(100*float64(totalMin)/float64(inBedMin), 0, 100)))
}

// SleepStageBreakdown returns deep, light, rem and awake, always in that order.
// Deep/light/rem percentages are shares of the staged sleep and sum to exactly
// 100 (all 0 when nothing was staged); awake is its share of time in bed.
func SleepStageBreakdown(deep, light, rem, awake, inBed int) []model.SleepStage {
	pct := sharesOf100([]int{deep, light, rem})
	awakePct := 0
	if inBed > 0 {
		awakePct = int(math.Round(100 * float64(awake) / float64(inBed)))
	}
	return []model.SleepStage{
		{Stage: model.StageDeep, Min: deep, Pct: pct[0]},
		{Stage: model.StageLight, Min: light, Pct: pct[1]},
		{Stage: model.StageREM, Min: rem, Pct: pct[2]},
		{Stage: model.StageAwake, Min: awake, Pct: awakePct},
	}
}

// sharesOf100 turns parts into whole percentages that sum to exactly 100 using
// the largest-remainder method; all zeros when the parts sum to zero.
func sharesOf100(parts []int) []int {
	out := make([]int, len(parts))
	sum := 0
	for _, p := range parts {
		sum += p
	}
	if sum <= 0 {
		return out
	}
	type remainder struct {
		idx  int
		frac float64
	}
	rems := make([]remainder, len(parts))
	left := 100
	for k, p := range parts {
		exact := 100 * float64(p) / float64(sum)
		out[k] = int(exact)
		left -= out[k]
		rems[k] = remainder{k, exact - float64(out[k])}
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for k := 0; k < left && k < len(rems); k++ {
		out[rems[k].idx]++
	}
	return out
}

// NightSummary builds the Sleep screen's view of a stored night. ok is false
// when the row has no typed sleep columns (no sleep, or scored before they existed).
func NightSummary(n store.SleepNight) (s model.SleepSummary, ok bool) {
	if n.TotalMin == nil || n.Start == nil || n.End == nil {
		return s, false
	}
	total := *n.TotalMin
	inBed := max(val(n.InBedMin), total)
	quality := val(n.Performance)
	return model.SleepSummary{
		Start:        n.Start.UTC(),
		End:          n.End.UTC(),
		TotalMin:     total,
		InBedMin:     inBed,
		Efficiency:   SleepEfficiency(total, inBed),
		Stages:       SleepStageBreakdown(val(n.DeepMin), val(n.LightMin), val(n.REMMin), val(n.AwakeMin), inBed),
		Quality:      quality,
		QualityLabel: SleepQualityLabel(quality),
		HasStages:    n.HasStages,
	}, true
}

// SleepRangeSummary assembles the Week / Month tab: every night with data plus
// averages over those nights. Stage averages only count nights with stages.
func SleepRangeSummary(rangeName, from, to string, nights []store.SleepNight) model.SleepRange {
	out := model.SleepRange{Range: rangeName, From: from, To: to,
		Days: []model.SleepDay{}, AvgStages: []model.SleepStage{}}
	var total, inBed, quality, staged, stagedInBed int
	var stageMin [4]int
	for _, n := range nights {
		s, ok := NightSummary(n)
		if !ok {
			continue
		}
		out.Days = append(out.Days, model.SleepDay{Day: n.Day, SleepSummary: s})
		total, inBed, quality = total+s.TotalMin, inBed+s.InBedMin, quality+s.Quality
		if s.HasStages {
			staged++
			stagedInBed += s.InBedMin
			for k := range stageMin {
				stageMin[k] += s.Stages[k].Min
			}
		}
	}
	out.Nights = len(out.Days)
	if out.Nights == 0 {
		return out
	}
	avg := func(sum, n int) int { return int(math.Round(float64(sum) / float64(n))) }
	t, b, q := avg(total, out.Nights), avg(inBed, out.Nights), avg(quality, out.Nights)
	e := SleepEfficiency(t, b)
	out.AvgTotalMin, out.AvgInBedMin, out.AvgQuality, out.AvgEfficiency = &t, &b, &q, &e
	if staged > 0 {
		out.AvgStages = SleepStageBreakdown(avg(stageMin[0], staged), avg(stageMin[1], staged),
			avg(stageMin[2], staged), avg(stageMin[3], staged), avg(stagedInBed, staged))
	}
	return out
}

var hypnogramStage = map[string]string{
	model.SleepDeep:   model.StageDeep,
	model.SleepCore:   model.StageLight,
	model.SleepREM:    model.StageREM,
	model.SleepAwake:  model.StageAwake,
	model.SleepAsleep: model.StageAsleep,
}

// Hypnogram turns raw HealthKit segments into the stage timeline: inbed is the
// frame, not a stage, so it is dropped; core becomes light; touching or
// overlapping segments of the same stage merge into one bar.
func Hypnogram(segs []model.SleepSegment) []model.SleepInterval {
	sorted := append([]model.SleepSegment(nil), segs...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Start.Before(sorted[b].Start) })
	out := []model.SleepInterval{}
	for _, s := range sorted {
		stage, ok := hypnogramStage[s.Stage]
		if !ok || !s.End.After(s.Start) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Stage == stage && !s.Start.After(out[n-1].End) {
			if s.End.After(out[n-1].End) {
				out[n-1].End = s.End.UTC()
			}
			continue
		}
		out = append(out, model.SleepInterval{Stage: stage, Start: s.Start.UTC(), End: s.End.UTC()})
	}
	return out
}

func val(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
