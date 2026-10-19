package scoring

import (
	"fmt"
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

// Sleep quality bands (SleepQuality); the brand board shows 82 % as "Good".
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
	quality, restorative := NightQuality(total, inBed, val(n.DeepMin), val(n.REMMin), n.HasStages, val(n.Performance))
	if n.Quality != nil { // as scored; the same formula unless its constants moved since
		quality = *n.Quality
	}
	return model.SleepSummary{
		Start:        n.Start.UTC(),
		End:          n.End.UTC(),
		TotalMin:     total,
		InBedMin:     inBed,
		Efficiency:   SleepEfficiency(total, inBed),
		Stages:       SleepStageBreakdown(val(n.DeepMin), val(n.LightMin), val(n.REMMin), val(n.AwakeMin), inBed),
		Quality:      quality,
		QualityLabel: SleepQualityLabel(quality),
		Restorative:  restorative,
		HasStages:    n.HasStages,
	}, true
}

// SleepRangeSummary assembles the Week / Month tab: every night with data plus
// averages over those nights. Stage averages only count nights with stages.
func SleepRangeSummary(rangeName, from, to string, nights []store.SleepNight) model.SleepRange {
	out := model.SleepRange{Range: rangeName, From: from, To: to,
		Days: []model.SleepDay{}, AvgStages: []model.SleepStage{}, Bedtimes: []model.SleepBedtime{}}
	var total, inBed, quality, staged, stagedInBed, consistency, consistent int
	var stageMin [4]int
	for _, n := range nights {
		s, ok := NightSummary(n)
		if !ok {
			continue
		}
		out.Days = append(out.Days, model.SleepDay{Day: n.Day, SleepSummary: s, Consistency: n.Consistency, NapMin: n.NapMin})
		out.Bedtimes = append(out.Bedtimes, model.SleepBedtime{Day: n.Day, Bed: s.Start, Wake: s.End})
		total, inBed, quality = total+s.TotalMin, inBed+s.InBedMin, quality+s.Quality
		if n.Consistency != nil {
			consistency, consistent = consistency+*n.Consistency, consistent+1
		}
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
	if consistent > 0 {
		c := avg(consistency, consistent)
		out.AvgConsistency = &c
	}
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

// ---- M4: naps, need breakdown, learned baseline, consistency, quality, planner ----

const (
	napFromHour     = 10 // naps end between 10:00 and 20:00 local
	napToHour       = 20
	napMinMinutes   = 20 // a nap counts from 20 minutes asleep
	napCapMinutes   = 90 // the day's credit is at most 90 minutes
	napGap          = 30 * time.Minute
	nightBlockGap   = 60 * time.Minute // a pause this long ends a sleep block
	learnMinNights  = 14
	learnMinQualify = 7
	learnMinBase    = 360
	learnMaxBase    = 570
	learnRecovery   = 67
	learnMaxStrain  = 10.0
	consistencyDays = 7
	consistencyMin  = 5
	planEffDefault  = 90
	planEffFloor    = 85
)

// NapWindow is where a day's naps end: 10:00 to 20:00 local on day d.
func NapWindow(d time.Time) (from, to time.Time) {
	return d.Add(napFromHour * time.Hour), d.Add(napToHour * time.Hour)
}

// sleepBlocks groups segments (start order) into blocks separated by gaps of
// at least gap between one block's end and the next segment's start.
func sleepBlocks(segs []model.SleepSegment, gap time.Duration) [][]model.SleepSegment {
	sorted := append([]model.SleepSegment(nil), segs...)
	sort.SliceStable(sorted, func(a, b int) bool { return sorted[a].Start.Before(sorted[b].Start) })
	var out [][]model.SleepSegment
	var end time.Time
	for _, s := range sorted {
		if len(out) == 0 || s.Start.Sub(end) >= gap {
			out = append(out, nil)
			end = s.End
		}
		out[len(out)-1] = append(out[len(out)-1], s)
		if s.End.After(end) {
			end = s.End
		}
	}
	return out
}

// SplitNight separates a night window's segments into the night and the naps
// it overlaps (docs/SCORING.md → Sleep, D65): the night is the block with
// the most sleep (blocks end at a pause of an hour). Another block that ends at
// or after napFrom (10:00, inside the night window, which closes at 13:00) is a
// morning nap; every other segment stays with the night, as before.
func SplitNight(segs []model.SleepSegment, napFrom time.Time) (night, naps []model.SleepSegment) {
	blocks := sleepBlocks(segs, nightBlockGap)
	main, best := -1, -1
	for k, b := range blocks {
		if m := SummarizeSleep(b).AsleepMin; m > best {
			main, best = k, m
		}
	}
	for k, b := range blocks {
		end := b[0].End
		for _, s := range b {
			if s.End.After(end) {
				end = s.End
			}
		}
		if k != main && !end.Before(napFrom) {
			naps = append(naps, b...)
			continue
		}
		night = append(night, b...)
	}
	return night, naps
}

// DaySleepWindow is where SplitDay needs segments from: the start of day d's
// night window (d−1 20:00) to the end of its nap window (d 20:00).
func DaySleepWindow(d time.Time) (from, to time.Time) {
	from, _ = NightWindow(d)
	_, to = NapWindow(d)
	return from, to
}

// SplitDay takes segments ending in DaySleepWindow(d) (more are ignored) and
// returns day d's night, from one source, without its morning naps, and the
// day's nap credit: those naps plus the sleep ending 13:00–20:00, also from
// one source.
func SplitDay(segs []model.SleepSegment, d time.Time) (night []model.SleepSegment, napMin int) {
	nightFrom, nightTo := NightWindow(d)
	napFrom, napTo := NapWindow(d)
	var inNight, afternoon []model.SleepSegment
	for _, s := range segs {
		switch {
		case s.End.Before(nightFrom) || !s.End.Before(napTo):
		case s.End.Before(nightTo):
			inNight = append(inNight, s)
		default:
			afternoon = append(afternoon, s)
		}
	}
	night, naps := SplitNight(DedupSleepSources(inNight), napFrom)
	return night, NapMinutes(append(naps, DedupSleepSources(afternoon)...))
}

// NapMinutes is a day's nap credit: the sleep of each nap block (pauses under
// 30 minutes join one), blocks of at least 20 minutes, summed and capped at 90.
func NapMinutes(segs []model.SleepSegment) int {
	total := 0
	for _, b := range sleepBlocks(segs, napGap) {
		if m := SummarizeSleep(b).AsleepMin; m >= napMinMinutes {
			total += m
		}
	}
	return min(total, napCapMinutes)
}

// NeedParts is a night's sleep need, term by term.
type NeedParts struct {
	Base, Strain, Debt, Nap int // Nap is the credit subtracted
}

// Total is base + strain + debt − nap credit.
func (p NeedParts) Total() int { return p.Base + p.Strain + p.Debt - p.Nap }

// SleepNeedParts is SleepNeed with each term and the nap credit (the previous
// day's nap minutes, at most 90).
func SleepNeedParts(baseMin int, strain float64, debtMin, napMin int) NeedParts {
	return NeedParts{
		Base:   baseMin,
		Strain: int(math.Round(math.Min(60, strain*3))),
		Debt:   int(math.Round(math.Min(60, float64(debtMin)*0.3))),
		Nap:    min(max(napMin, 0), napCapMinutes),
	}
}

// LearnedSleepBase estimates the baseline need from scored days (newest first,
// as store.ScoreHistory returns them): at least 14 nights of sleep, of which at
// least 7 qualify — that morning's recovery ≥ 67 after a day with strain < 10
// (a day without strain counts as below). The median minutes asleep of the
// qualifying nights, clamped to 360–570.
func LearnedSleepBase(hist []store.History) (int, bool) {
	strainOn := map[string]*float64{}
	for _, h := range hist {
		strainOn[h.Day] = h.Strain
	}
	nights := 0
	var mins []int
	for _, h := range hist {
		if h.SleepActualMin == nil || *h.SleepActualMin <= 0 {
			continue
		}
		nights++
		d, err := time.Parse("2006-01-02", h.Day)
		if err != nil || h.Recovery == nil || *h.Recovery < learnRecovery {
			continue
		}
		if prev := strainOn[d.AddDate(0, 0, -1).Format("2006-01-02")]; prev != nil && *prev >= learnMaxStrain {
			continue
		}
		mins = append(mins, *h.SleepActualMin)
	}
	if nights < learnMinNights || len(mins) < learnMinQualify {
		return 0, false
	}
	sort.Ints(mins)
	m := mins[len(mins)/2]
	if len(mins)%2 == 0 {
		m = int(math.Round(float64(mins[len(mins)/2-1]+mins[len(mins)/2]) / 2))
	}
	return min(max(m, learnMinBase), learnMaxBase), true
}

// minutesOnNoonAxis places a clock time on a noon-to-noon axis (D13), so 23:30
// and 00:30 are an hour apart.
func minutesOnNoonAxis(t time.Time, loc *time.Location) float64 {
	l := t.In(loc)
	m := float64(l.Hour()*60+l.Minute()) - 12*60
	if m < 0 {
		m += 24 * 60
	}
	return m
}

func stddev(xs []float64) float64 {
	var mean float64
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - mean) * (x - mean)
	}
	return math.Sqrt(v / float64(len(xs)))
}

// Bedtime is one night's start and end.
type Bedtime struct{ Bed, Wake time.Time }

// SleepConsistency scores the last nights' regularity: 100 − 0.5·(σ bedtime + σ
// wake time) in minutes on the noon-to-noon axis, clamped to 0–100; nights
// should be the last 7, and at least 5 must have data.
func SleepConsistency(nights []Bedtime, loc *time.Location) (int, bool) {
	if len(nights) < consistencyMin {
		return 0, false
	}
	beds := make([]float64, len(nights))
	wakes := make([]float64, len(nights))
	for k, n := range nights {
		beds[k], wakes[k] = minutesOnNoonAxis(n.Bed, loc), minutesOnNoonAxis(n.Wake, loc)
	}
	c := 100 - 0.5*(stddev(beds)+stddev(wakes))
	return int(math.Round(clamp(c, 0, 100))), true
}

// Restorative is deep + REM against 40 % of time asleep, capped at 100; without
// stages it is the performance (the night's only measure then).
func Restorative(deep, rem, asleep int, hasStages bool, performance int) int {
	if !hasStages || asleep <= 0 {
		return performance
	}
	return int(math.Round(math.Min(100, float64(deep+rem)/float64(asleep)/0.40*100)))
}

// NightQuality is a night's quality and restorative score from its totals
// (in bed is at least the time asleep, as on the Sleep screen).
func NightQuality(asleep, inBed, deep, rem int, hasStages bool, performance int) (quality, restorative int) {
	restorative = Restorative(deep, rem, asleep, hasStages, performance)
	return SleepQuality(performance, SleepEfficiency(asleep, max(inBed, asleep)), restorative), restorative
}

// SleepQuality is 0.6·performance + 0.2·efficiency + 0.2·restorative.
func SleepQuality(performance, efficiency, restorative int) int {
	return int(math.Round(0.6*float64(performance) + 0.2*float64(efficiency) + 0.2*float64(restorative)))
}

// PlanEfficiency is the efficiency the planner assumes: the mean of the
// nights given (the last 7), 90 without any, never below 85.
func PlanEfficiency(efficiencies []int) int {
	if len(efficiencies) == 0 {
		return planEffDefault
	}
	sum := 0
	for _, e := range efficiencies {
		sum += e
	}
	return max(int(math.Round(float64(sum)/float64(len(efficiencies)))), planEffFloor)
}

// SleepPlan turns tonight's need and a wake time into bedtimes for 100, 85 and
// 70 % of the need: time in bed = need ÷ efficiency.
func SleepPlan(need NeedParts, wake time.Time, efficiency int) model.SleepPlan {
	inBed := float64(need.Total()) / (float64(max(efficiency, planEffFloor)) / 100)
	at := func(share float64) time.Time {
		return wake.Add(-time.Duration(math.Round(share*inBed)) * time.Minute).UTC()
	}
	return model.SleepPlan{Wake: wake.UTC(), NeedMin: need.Total(), InBedMin: int(math.Round(inBed)), Efficiency: efficiency,
		Bedtime100: at(1), Bedtime85: at(0.85), Bedtime70: at(0.70),
		Basis: model.SleepPlanBasis{Base: need.Base, StrainTopup: need.Strain, DebtTopup: need.Debt, NapCredit: need.Nap, Efficiency: efficiency}}
}

// NeedOf is a scored day's need term by term (DayDetail.sleep.need). Days
// scored before the terms were stored report the whole need as base.
func NeedOf(d model.DailyScore) model.SleepNeed {
	total := val(d.SleepNeedMin)
	if d.SleepNeedBaseMin == nil {
		return model.SleepNeed{Base: total, Total: total}
	}
	return model.SleepNeed{Base: *d.SleepNeedBaseMin, Strain: val(d.SleepNeedStrainMin), Debt: val(d.SleepNeedDebtMin),
		NapCredit: val(d.SleepNeedNapMin), Total: total}
}

// SleepDebtFrom is the debt the next night carries: SleepDebt over the 7 most
// recent of hist (newest first, as store.ScoreHistory returns it) that have a
// need and a night.
func SleepDebtFrom(hist []store.History) int {
	var needs, actuals []int
	for idx, h := range hist {
		if idx < debtDays && h.SleepNeedMin != nil && h.SleepActualMin != nil {
			needs = append(needs, *h.SleepNeedMin)
			actuals = append(actuals, *h.SleepActualMin)
		}
	}
	return SleepDebt(needs, actuals)
}

// TonightPlan is GET /v1/sleep/plan: tonight's need from the baseline, today's
// strain so far and naps, and the debt including last night; hist is
// store.ScoreHistory up to and including today, newest first.
func TonightPlan(base int, today string, hist []store.History, wake time.Time) model.SleepPlan {
	var strain float64
	var nap int
	var effs []int
	for idx, h := range hist {
		if idx == 0 && h.Day == today {
			strain, nap = deref(h.Strain), val(h.NapMin)
		}
		if idx < debtDays && h.Efficiency != nil {
			effs = append(effs, *h.Efficiency)
		}
	}
	return SleepPlan(SleepNeedParts(base, strain, SleepDebtFrom(hist), nap), wake, PlanEfficiency(effs))
}

// NextWake resolves a local "HH:MM" to the next time it occurs after now
// (today or tomorrow, in now's location).
func NextWake(hhmm string, now time.Time) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil || len(hhmm) != 5 {
		return time.Time{}, fmt.Errorf("wake must be HH:MM")
	}
	w := time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location())
	if !w.After(now) {
		w = time.Date(now.Year(), now.Month(), now.Day()+1, t.Hour(), t.Minute(), 0, 0, now.Location())
	}
	return w, nil
}
