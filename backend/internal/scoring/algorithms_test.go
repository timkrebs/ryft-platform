package scoring

import (
	"math"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func lnBase(vals ...float64) Baseline {
	out := make([]float64, len(vals))
	for k, v := range vals {
		out[k] = math.Log(v)
	}
	return NewBaseline(out, fallbackSDlnHRV)
}

func TestRecoveryAtBaselineIsMid(t *testing.T) {
	hist := []float64{48, 50, 52, 49, 51, 50, 50, 49, 51, 50}
	r := Recovery(RecoveryInputs{
		HRVms: f(50), LnHRVBase: lnBase(hist...),
		RHR: f(50), RHRBase: NewBaseline([]float64{49, 50, 51, 50, 50, 49, 51, 50, 50, 50}, fallbackSDRHR),
		SleepPerformance: i(50),
	})
	if r.Score < 45 || r.Score > 55 {
		t.Fatalf("at-baseline recovery should be ~50, got %d (%v)", r.Score, r.Components)
	}
	if r.Calibrating {
		t.Fatal("10 days of history should not be calibrating")
	}
}

func TestRecoveryRespondsToHRV(t *testing.T) {
	hist := []float64{48, 50, 52, 49, 51, 50, 50, 49, 51, 50}
	base := RecoveryInputs{LnHRVBase: lnBase(hist...), RHR: f(50),
		RHRBase: NewBaseline([]float64{49, 50, 51, 50, 50, 49, 51, 50, 50, 50}, fallbackSDRHR), SleepPerformance: i(85)}

	high := base
	high.HRVms = f(65)
	low := base
	low.HRVms = f(35)
	if Recovery(high).Score <= Recovery(low).Score {
		t.Fatalf("higher HRV must yield higher recovery: high=%d low=%d", Recovery(high).Score, Recovery(low).Score)
	}
	if Recovery(high).Score < 70 {
		t.Fatalf("HRV 30%% above baseline + good sleep should be green, got %d", Recovery(high).Score)
	}
	if Recovery(low).Score > 40 {
		t.Fatalf("HRV 30%% below baseline should be red, got %d", Recovery(low).Score)
	}
}

func TestRecoveryCalibratingWithShortHistory(t *testing.T) {
	r := Recovery(RecoveryInputs{HRVms: f(50), LnHRVBase: lnBase(50, 51), RHR: f(50),
		RHRBase: NewBaseline([]float64{50, 51}, fallbackSDRHR)})
	if !r.Calibrating {
		t.Fatal("2 days of history must be calibrating")
	}
}

func TestRecoveryNoData(t *testing.T) {
	if s := Recovery(RecoveryInputs{}).Score; s != 0 {
		t.Fatalf("no inputs should give 0, got %d", s)
	}
}

func hrSeries(start time.Time, mins int, bpm float64) []HRPoint {
	pts := make([]HRPoint, 0, mins)
	for m := 0; m <= mins; m++ {
		pts = append(pts, HRPoint{T: start.Add(time.Duration(m) * time.Minute), BPM: bpm})
	}
	return pts
}

func TestStrainScale(t *testing.T) {
	start := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	const rhr, maxHR = 50.0, 190.0

	rest := Strain(hrSeries(start, 600, 60), rhr, maxHR)  // 10h at ~7% HRR
	tempo := Strain(hrSeries(start, 60, 155), rhr, maxHR) // 60 min at 75% HRR
	race := Strain(hrSeries(start, 120, 170), rhr, maxHR) // 2h at 86% HRR

	if rest.Score > 0.5 {
		t.Fatalf("sitting all day should be ~0 strain, got %.1f", rest.Score)
	}
	walk := Strain(hrSeries(start, 60, 100), rhr, maxHR) // 60-min brisk walk at ~36% HRR
	if walk.Score < 1 || walk.Score > 4 {
		t.Fatalf("an hour's walk should register as light strain, got %.1f", walk.Score)
	}
	if tempo.Score < 12 || tempo.Score > 16 {
		t.Fatalf("60-min tempo run should land around 14, got %.1f (TRIMP %.0f)", tempo.Score, tempo.TRIMP)
	}
	if race.Score < 18 || race.Score > 21 {
		t.Fatalf("2h race effort should be 18+, got %.1f", race.Score)
	}
	if race.ZoneMinutes[3] < 110 {
		t.Fatalf("race minutes should fall in zone 4 (80-90%%), got %v", race.ZoneMinutes)
	}
}

func TestStrainIgnoresGaps(t *testing.T) {
	start := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	pts := []HRPoint{{start, 160}, {start.Add(3 * time.Hour), 160}} // watch off for 3h
	r := Strain(pts, 50, 190)
	if r.Minutes > 5 {
		t.Fatalf("a 3h gap must credit at most 5 min, credited %.1f", r.Minutes)
	}
}

func TestStrainFromAverageMatchesIntegration(t *testing.T) {
	start := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)
	integrated := Strain(hrSeries(start, 60, 155), 50, 190).Score
	avg := StrainFromAverage(155, 50, 190, 60*time.Minute)
	if math.Abs(integrated-avg) > 0.5 {
		t.Fatalf("average-based estimate %.1f should match integrated %.1f", avg, integrated)
	}
}

func TestSleepSummaryAndNeed(t *testing.T) {
	bed := time.Date(2026, 10, 17, 22, 30, 0, 0, time.UTC)
	segs := []model.SleepSegment{
		{Start: bed, End: bed.Add(8 * time.Hour), Stage: model.SleepInBed},
		{Start: bed.Add(10 * time.Minute), End: bed.Add(100 * time.Minute), Stage: model.SleepCore},
		{Start: bed.Add(100 * time.Minute), End: bed.Add(160 * time.Minute), Stage: model.SleepDeep},
		{Start: bed.Add(160 * time.Minute), End: bed.Add(170 * time.Minute), Stage: model.SleepAwake},
		{Start: bed.Add(170 * time.Minute), End: bed.Add(260 * time.Minute), Stage: model.SleepREM},
		{Start: bed.Add(260 * time.Minute), End: bed.Add(460 * time.Minute), Stage: model.SleepCore},
	}
	s := SummarizeSleep(segs)
	if s.AsleepMin != 440 || s.DeepMin != 60 || s.REMMin != 90 || s.AwakeMin != 10 || !s.HasStages {
		t.Fatalf("unexpected totals: %+v", s)
	}

	need := SleepNeed(460, 14, 40) // base 7h40, hard day yesterday, 40 min debt
	if need != 460+42+12 {
		t.Fatalf("need = %d", need)
	}
	if p := SleepPerformance(s.AsleepMin, need); p != 86 {
		t.Fatalf("performance = %d", p)
	}
	if d := SleepDebt([]int{480, 480, 480}, []int{420, 480, 400}); d != 60+0+39 {
		t.Fatalf("debt = %d", d)
	}
}

func TestSleepFallbackWithoutStages(t *testing.T) {
	bed := time.Date(2026, 10, 17, 22, 30, 0, 0, time.UTC)
	s := SummarizeSleep([]model.SleepSegment{{Start: bed, End: bed.Add(8 * time.Hour), Stage: model.SleepInBed}})
	if s.AsleepMin != 432 || s.HasStages {
		t.Fatalf("inbed-only should estimate 90%% asleep: %+v", s)
	}
}

func TestReadiness(t *testing.T) {
	// Brand board: recovery 82 % after a 87 % night and a moderate day → 84.
	if r := Readiness(82, 87, 12.4); r != 84 {
		t.Fatalf("brand-board readiness = %d, want 84", r)
	}
	cases := []struct {
		name       string
		rec, sleep int
		strain     float64
		want       int
	}{
		{"no penalty at strain 14", 40, 80, 14, 56},
		{"no penalty when recovery supports it", 60, 80, 21, 68},
		{"half penalty at strain 17.5", 40, 80, 17.5, 51},
		{"full penalty at strain 21", 40, 80, 21, 46},
		{"clamped at 1", 1, 0, 21, 1},
		{"clamped at 100", 100, 100, 0, 100},
	}
	for _, c := range cases {
		if got := Readiness(c.rec, c.sleep, c.strain); got != c.want {
			t.Errorf("%s: Readiness(%d, %d, %.1f) = %d, want %d", c.name, c.rec, c.sleep, c.strain, got, c.want)
		}
	}
}

func TestReadinessState(t *testing.T) {
	cases := []struct {
		readiness   *int
		calibrating bool
		want        string
	}{
		{i(67), false, model.StateBalanced},
		{i(66), false, model.StateStrained},
		{i(34), false, model.StateStrained},
		{i(33), false, model.StateDepleted},
		{i(90), true, model.StateCalibrating},
		{nil, false, model.StateCalibrating},
	}
	for _, c := range cases {
		if got := ReadinessState(c.readiness, c.calibrating); got != c.want {
			t.Errorf("ReadinessState(%v, %v) = %q, want %q", c.readiness, c.calibrating, got, c.want)
		}
	}
}
