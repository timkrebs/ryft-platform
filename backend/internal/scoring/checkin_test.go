package scoring

import (
	"math"
	"testing"
	"time"
)

// Calibration of the check-in estimate (D292; docs/SCORING.md → Check-in
// recovery). Every expectation is worked out by hand in the comment next to
// it; the first four are the contract's pinned cases (docs/API.md, the
// implementation outline's Tests table).

// night is a check-in at UTC+1 (the contract's examples) ending at 06:00 UTC
// on 2026-11-26 after the given minutes in bed.
func night(minutes, quality, freshness int) CheckinInput {
	wake := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	return CheckinInput{Bedtime: wake.Add(-time.Duration(minutes) * time.Minute), WakeTime: wake, TZOffsetSec: 3600,
		Quality: quality, Freshness: freshness}
}

func priorRatings(n, quality, freshness int) []CheckinRating {
	out := make([]CheckinRating, n)
	for k := range out {
		out[k] = CheckinRating{quality, freshness}
	}
	return out
}

func TestCheckinPinnedCases(t *testing.T) {
	// 5 h, 3 and 3, no history: absolute (0.65·3 + 0.35·3 − 1)/9·100 = 22.2,
	// duration 40 (300 min), score (0.5·22.2 + 0.3·40)/0.8 = 28.9 → 29; both
	// ratings ≤ 4 → a drop by the low-ratings rule (no z without history).
	r := CheckinScore(night(300, 3, 3))
	if r.SleepDurationMin != 300 || r.Components.Duration != 40 || r.Components.WellnessAbsolute != 22 ||
		r.Components.Wellness != 22 || r.RecoveryScore != 29 || !r.AcuteDrop || r.DropReason != CheckinDropLowQualityFreshness ||
		r.Z != nil || r.Components.WellnessPersonal != nil || r.Components.Regularity != nil || r.Components.N2R != nil {
		t.Errorf("5 h, 3/3: %+v %+v", r, r.Components)
	}

	// 8 h, 9 and 9: absolute 8/9·100 = 88.9, duration 100 (480 in 450..570),
	// score (44.4 + 30)/0.8 = 93.1 → 93; no drop.
	r = CheckinScore(night(480, 9, 9))
	if r.Components.Duration != 100 || r.Components.WellnessAbsolute != 89 || r.RecoveryScore != 93 || r.AcuteDrop || r.DropReason != "" {
		t.Errorf("8 h, 9/9: %+v %+v", r, r.Components)
	}

	// 14 check-ins at 6/6 (absolute 5/9·100 = 55.6, SD 0 → floored to 6),
	// then 5/5 (4/9·100 = 44.4): z = −11.1/6 = −1.85, personal 50 − 46.3 = 3.7
	// → 4, wellness 0.7·44.4 + 0.3·3.7 = 32.2 → 32. 5 and 5 are above the
	// low-ratings rule, so the drop is by z alone. At 8 h the score is
	// (0.5·32.2 + 30)/0.8 = 57.6 → 58.
	in := night(480, 5, 5)
	in.Prior = priorRatings(14, 6, 6)
	r = CheckinScore(in)
	if r.Z == nil || math.Abs(*r.Z-(-1.851852)) > 1e-5 {
		t.Fatalf("z: %v", r.Z)
	}
	if r.Components.WellnessPersonal == nil || *r.Components.WellnessPersonal != 4 || r.Components.Wellness != 32 ||
		r.Components.WellnessAbsolute != 44 || !r.AcuteDrop || r.DropReason != CheckinDropBelowUsual || r.RecoveryScore != 58 {
		t.Errorf("14 × 6/6 then 5/5: %+v %+v", r, r.Components)
	}

	// 4 check-ins in the 14 days before: no personal part, wellness = absolute.
	in.Prior = priorRatings(4, 6, 6)
	r = CheckinScore(in)
	if r.Z != nil || r.Components.WellnessPersonal != nil || r.Components.Wellness != r.Components.WellnessAbsolute || r.AcuteDrop {
		t.Errorf("4 prior: %+v %+v", r, r.Components)
	}
	// The fifth makes it count.
	in.Prior = priorRatings(5, 6, 6)
	if r = CheckinScore(in); r.Z == nil || r.Components.WellnessPersonal == nil {
		t.Errorf("5 prior: %+v", r)
	}
}

func TestCheckinContractExamples(t *testing.T) {
	// docs/API.md's RecoveryCheckin: 22:10 → 05:40 (450 min), 7 and 6:
	// absolute (4.55 + 2.1 − 1)/9·100 = 62.8 → 63, duration 100, score
	// (31.4 + 30)/0.8 = 76.7 → 77.
	bed := time.Date(2026, 11, 25, 22, 10, 0, 0, time.UTC)
	r := CheckinScore(CheckinInput{Bedtime: bed, WakeTime: bed.Add(450 * time.Minute), TZOffsetSec: 3600, Quality: 7, Freshness: 6})
	if r.SleepDurationMin != 450 || r.RecoveryScore != 77 || r.Components.Wellness != 63 || r.Components.Duration != 100 || r.AcuteDrop {
		t.Errorf("API example 7/6: %+v %+v", r, r.Components)
	}
	// The briefing's: 00:40 → 05:40 (300 min), 3 and 4: absolute 2.35/9·100 =
	// 26.1 → 26, duration 40, score (13.1 + 12)/0.8 = 31.3 → 31, a drop by the
	// low ratings.
	r = CheckinScore(night(300, 3, 4))
	if r.RecoveryScore != 31 || r.Components.Wellness != 26 || r.Components.Duration != 40 || r.DropReason != CheckinDropLowQualityFreshness {
		t.Errorf("briefing example 3/4: %+v %+v", r, r.Components)
	}
}

func TestCheckinDurationCurve(t *testing.T) {
	// L = 450: 40 up to 300, linear to 100 at 450 (375 is half way: 70), the
	// plateau to 570, then −10 an hour (630: 90; 720: 75) and the floor 75.
	for minutes, want := range map[int]int{0: 40, 240: 40, 300: 40, 375: 70, 449: 100, 450: 100, 570: 100, 600: 95, 630: 90, 720: 75, 900: 75} {
		if got := roundInt(CheckinDuration(minutes, 0, "default")); got != want {
			t.Errorf("%d min: %d, want %d", minutes, got, want)
		}
	}
	// A manual sleep base of 480 moves L to 480: 450 is now on the slope
	// (40 + 150/180·60 = 90), the plateau runs to 600.
	if l := CheckinDurationL(480, SleepBaseSourceManual); l != 480 {
		t.Errorf("manual 480: L %d", l)
	}
	if got := CheckinDuration(450, 480, SleepBaseSourceManual); got != 90 {
		t.Errorf("450 with L 480: %v", got)
	}
	if got := CheckinDuration(600, 480, SleepBaseSourceManual); got != 100 {
		t.Errorf("600 with L 480: %v", got)
	}
	// Learned is the same; the default source keeps 450 whatever the number;
	// the base is clamped to 360..600.
	for _, c := range []struct {
		base   int
		source string
		want   int
	}{{480, SleepBaseSourceLearned, 480}, {480, "default", 450}, {300, SleepBaseSourceLearned, 360}, {700, SleepBaseSourceManual, 600}, {0, SleepBaseSourceManual, 450}} {
		if l := CheckinDurationL(c.base, c.source); l != c.want {
			t.Errorf("base %d %s: L %d, want %d", c.base, c.source, l, c.want)
		}
	}
}

func TestCheckinRegularity(t *testing.T) {
	const three = 3 * 60
	// Two prior nights: no part.
	if _, ok := CheckinRegularity(three, []int{three, three}); ok {
		t.Error("2 prior nights must give no regularity")
	}
	// 03:00 ×3, today 03:25 (25 min off) → 100; 05:00 (120 min) → 50;
	// 04:15 (75 min, half way between 30 and 120) → 75.
	for today, want := range map[int]float64{three + 25: 100, 5 * 60: 50, 4*60 + 15: 75, 9 * 60: 50} {
		if got, ok := CheckinRegularity(today, []int{three, three, three}); !ok || math.Abs(got-want) > 1e-9 {
			t.Errorf("today %d: %v %v, want %v", today, got, ok, want)
		}
	}
	// Across midnight: 23:50 ×3 against 00:10 is 20 minutes on the clock, not
	// 1420 → 100 (circular).
	if got, ok := CheckinRegularity(10, []int{23*60 + 50, 23*60 + 50, 23*60 + 50}); !ok || got != 100 {
		t.Errorf("across midnight: %v %v", got, ok)
	}
	// A circular mean of 23:50 and 00:10 is midnight, not noon.
	if got, ok := CheckinRegularity(0, []int{23*60 + 50, 10, 23*60 + 50, 10}); !ok || got != 100 {
		t.Errorf("mean across midnight: %v %v", got, ok)
	}
	// Only the first (newest) 7 count: three far-off old nights do not move it.
	if got, _ := CheckinRegularity(three, []int{three, three, three, three, three, three, three, 15 * 60, 15 * 60, 15 * 60}); got != 100 {
		t.Errorf("first 7 only: %v", got)
	}
	// Midpoints that cancel out on the clock have no mean: no part.
	if _, ok := CheckinRegularity(three, []int{0, 720, 360, 1080}); ok {
		t.Error("no mean direction must give no regularity")
	}
}

func TestSleepMidpointMin(t *testing.T) {
	// 22:10Z → 05:40Z at UTC+1: midpoint 01:55Z, 02:55 local = 175.
	bed := time.Date(2026, 11, 25, 22, 10, 0, 0, time.UTC)
	if got := SleepMidpointMin(bed, bed.Add(450*time.Minute), 3600); got != 175 {
		t.Errorf("UTC+1: %d", got)
	}
	// 03:00Z → 11:00Z at UTC−8: midpoint 07:00Z, 23:00 local the day before = 1380.
	bed = time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)
	if got := SleepMidpointMin(bed, bed.Add(8*time.Hour), -8*3600); got != 1380 {
		t.Errorf("UTC−8: %d", got)
	}
}

func TestCheckinRenormalisation(t *testing.T) {
	// 7/6 for 450 min: wellness 62.78, duration 100.
	in := night(450, 7, 6)
	w := WellnessAbsolute(7, 6)
	// No regularity, no N2R: the 0.5/0.3 weights scaled by 1/0.8.
	if r := CheckinScore(in); r.RecoveryScore != roundInt((0.5*w+0.3*100)/0.8) || r.RecoveryScore != 77 {
		t.Errorf("wellness and duration only: %d", r.RecoveryScore)
	}
	// N2R 50 only: (31.39 + 30 + 5)/0.9 = 73.8 → 74.
	n := 50
	in.N2R = &n
	r := CheckinScore(in)
	if r.RecoveryScore != 74 || r.Components.N2R == nil || *r.Components.N2R != 50 || r.Components.Regularity != nil {
		t.Errorf("with N2R: %+v %+v", r, r.Components)
	}
	// All four parts (regularity 100: the same midpoint three nights running):
	// 31.39 + 30 + 10 + 5 = 76.4 → 76, the weights summing to 1.
	mid := SleepMidpointMin(in.Bedtime, in.WakeTime, in.TZOffsetSec)
	in.PriorMidpoints = []int{mid, mid, mid}
	r = CheckinScore(in)
	if r.RecoveryScore != 76 || r.Components.Regularity == nil || *r.Components.Regularity != 100 {
		t.Errorf("all parts: %+v %+v", r, r.Components)
	}
}

func TestCheckinDropReasonsAndSoreness(t *testing.T) {
	// Both rules hold (14 × 6/6, then 4/4: absolute 33.3, z = −22.2/6 = −3.7):
	// the personal comparison names it (D298).
	in := night(480, 4, 4)
	in.Prior = priorRatings(14, 6, 6)
	if r := CheckinScore(in); !r.AcuteDrop || r.DropReason != CheckinDropBelowUsual {
		t.Errorf("both: %+v", r)
	}
	// Low ratings, but usual for this user (14 × 3/3 = 22.2, then 3/4 = 26.1:
	// z = 3.9/6 = 0.65):
	// still a drop by the absolute rule.
	in = night(480, 3, 4)
	in.Prior = priorRatings(14, 3, 3)
	if r := CheckinScore(in); !r.AcuteDrop || r.DropReason != CheckinDropLowQualityFreshness || r.Z == nil || *r.Z <= 0 {
		t.Errorf("low but usual: %+v", r)
	}
	// Only one rating ≤ 4 and no history: no drop.
	if r := CheckinScore(night(480, 4, 5)); r.AcuteDrop || r.DropReason != "" {
		t.Errorf("4/5: %+v", r)
	}
	// Soreness has zero weight: the result is identical.
	in = night(300, 3, 3)
	in.Prior = priorRatings(6, 7, 7)
	sore := in
	sore.Soreness = true
	a, b := CheckinScore(in), CheckinScore(sore)
	if a.RecoveryScore != b.RecoveryScore || a.AcuteDrop != b.AcuteDrop || a.DropReason != b.DropReason || *a.Z != *b.Z ||
		a.Components.Wellness != b.Components.Wellness {
		t.Errorf("soreness moved the score: %+v vs %+v", a, b)
	}
}

func TestCheckinSampleSD(t *testing.T) {
	// Prior absolutes 0, 0, 0, 100, 100 (1/1 ×3, 10/10 ×2): mean 40, sample SD
	// √(12000/4) = 54.8 (the population SD would be 49.0); today 10/10: z =
	// 60/54.8 = 1.095, personal 50 + 27.4 = 77.4 → 77 (81 with the population SD).
	in := night(480, 10, 10)
	in.Prior = []CheckinRating{{1, 1}, {1, 1}, {1, 1}, {10, 10}, {10, 10}}
	r := CheckinScore(in)
	if r.Components.WellnessPersonal == nil || *r.Components.WellnessPersonal != 77 || math.Abs(*r.Z-60/math.Sqrt(3000)) > 1e-9 {
		t.Errorf("sample SD: %+v %v", r.Components, *r.Z)
	}
	// Personal is clamped to 0..100: 14 × 1/1 then 10/10 is z = 16.7.
	in.Prior = priorRatings(14, 1, 1)
	if r := CheckinScore(in); *r.Components.WellnessPersonal != 100 || r.Components.Wellness != 100 || r.RecoveryScore != 100 {
		t.Errorf("clamped: %+v %d", r.Components, r.RecoveryScore)
	}
}

func TestCheckinTotalOnBadInput(t *testing.T) {
	// The handler refuses these; the function stays total: ratings clamped,
	// a wake before the bed is 0 minutes.
	r := CheckinScore(CheckinInput{Bedtime: time.Unix(1000, 0), WakeTime: time.Unix(0, 0), Quality: 0, Freshness: 11})
	if r.SleepDurationMin != 0 || r.Components.Duration != 40 || r.RecoveryScore < 0 || r.RecoveryScore > 100 {
		t.Errorf("bad input: %+v", r)
	}
	if CheckinModelVersion != ModelVersion {
		t.Error("check-ins carry the scoring model's version")
	}
}
