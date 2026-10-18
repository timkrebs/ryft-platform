package scoring

import (
	"reflect"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

// series returns n samples of one type and source, one per step from start.
func series(typ, source string, start time.Time, n int, step time.Duration, value float64) []model.Sample {
	out := make([]model.Sample, n)
	for i := range out {
		at := start.Add(time.Duration(i) * step)
		out[i] = model.Sample{Type: typ, Start: at, End: at, Value: value, Source: source}
	}
	return out
}

func sources(xs []model.Sample) map[string]int {
	m := map[string]int{}
	for _, x := range xs {
		m[x.Source]++
	}
	return m
}

func TestDedupSourcesSingleSourceUnchanged(t *testing.T) {
	in := series(model.SampleHeartRate, "Watch", t0, 120, time.Minute, 60)
	if out := DedupSources(in); !reflect.DeepEqual(out, in) {
		t.Fatal("one source must pass through untouched")
	}
	if DedupSources(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

func TestDedupSourcesKeepsTheDensestSourcePerHour(t *testing.T) {
	var in []model.Sample
	// 09:00 both devices: the watch has 60 samples, the phone 6 → watch only.
	in = append(in, series(model.SampleHeartRate, "Watch", t0, 60, time.Minute, 70)...)
	in = append(in, series(model.SampleHeartRate, "iPhone", t0.Add(5*time.Minute), 6, 10*time.Minute, 90)...)
	// 10:00 the watch is charging: the phone's samples are all there is → kept.
	in = append(in, series(model.SampleHeartRate, "iPhone", t0.Add(time.Hour), 6, 10*time.Minute, 90)...)
	out := DedupSources(in)
	first, second := sources(nil), sources(nil)
	for _, x := range out {
		if x.Start.Before(t0.Add(time.Hour)) {
			first[x.Source]++
		} else {
			second[x.Source]++
		}
	}
	if !reflect.DeepEqual(first, map[string]int{"Watch": 60}) || !reflect.DeepEqual(second, map[string]int{"iPhone": 6}) {
		t.Fatalf("09:00 %v, 10:00 %v", first, second)
	}
}

func TestDedupSourcesTypesAreIndependentAndTiesByName(t *testing.T) {
	var in []model.Sample
	in = append(in, series(model.SampleSteps, "iPhone", t0, 10, 6*time.Minute, 100)...)
	in = append(in, series(model.SampleSteps, "Watch", t0, 4, 15*time.Minute, 300)...)
	in = append(in, series(model.SampleHeartRate, "Watch", t0, 30, 2*time.Minute, 65)...)
	in = append(in, series(model.SampleHeartRate, "iPhone", t0, 3, 20*time.Minute, 80)...)
	// A tie: both sources have 5 active-energy samples in the hour.
	in = append(in, series(model.SampleActiveEnergy, "Watch", t0, 5, 12*time.Minute, 20)...)
	in = append(in, series(model.SampleActiveEnergy, "Garmin", t0, 5, 12*time.Minute, 25)...)
	got := map[string]map[string]int{}
	for _, x := range DedupSources(in) {
		if got[x.Type] == nil {
			got[x.Type] = map[string]int{}
		}
		got[x.Type][x.Source]++
	}
	want := map[string]map[string]int{
		model.SampleSteps:        {"iPhone": 10},
		model.SampleHeartRate:    {"Watch": 30},
		model.SampleActiveEnergy: {"Garmin": 5},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDedupSourcesPreservesOrder(t *testing.T) {
	in := append(series(model.SampleHeartRate, "Watch", t0, 10, time.Minute, 60),
		series(model.SampleHeartRate, "iPhone", t0, 2, time.Minute, 60)...)
	in = append(in, series(model.SampleHeartRate, "Watch", t0.Add(10*time.Minute), 10, time.Minute, 60)...)
	out := DedupSources(in)
	for i := 1; i < len(out); i++ {
		if out[i].Start.Before(out[i-1].Start) {
			t.Fatalf("order changed at %d", i)
		}
	}
	if len(out) != 20 {
		t.Fatalf("%d samples, want the watch's 20", len(out))
	}
}

// Two heart-rate sources interleaved: a watch at 150 bpm and a second device
// that reads 60 bpm half a minute later. Mixed, every other interval would be
// scored at 60 bpm; de-duplicated, the strain is the watch's alone.
func TestDedupSourcesStopsInterleavedSourcesDistortingStrain(t *testing.T) {
	watch := series(model.SampleHeartRate, "Watch", t0, 60, time.Minute, 150)
	other := series(model.SampleHeartRate, "Band", t0.Add(30*time.Second), 20, 3*time.Minute, 60)
	alone := Strain(hrPoints(watch), 50, 190)
	mixed := Strain(hrPoints(append(append([]model.Sample{}, watch...), other...)), 50, 190)
	dedup := Strain(hrPoints(DedupSources(append(append([]model.Sample{}, watch...), other...))), 50, 190)
	if mixed.TRIMP >= alone.TRIMP {
		t.Fatalf("the mix should understate the load: mixed %.1f, alone %.1f", mixed.TRIMP, alone.TRIMP)
	}
	if dedup != alone {
		t.Fatalf("de-duplicated %+v, want the watch's %+v", dedup, alone)
	}
}

func TestSampleWindowAndWorkoutSamples(t *testing.T) {
	from, to := SampleWindow(t0.Add(20*time.Minute), t0.Add(70*time.Minute))
	if !from.Equal(t0) || !to.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("window %v – %v", from, to)
	}
	if _, to := SampleWindow(t0, t0.Add(time.Hour)); !to.Equal(t0.Add(time.Hour)) {
		t.Fatalf("an aligned end stays: %v", to)
	}
	// A 09:40–09:55 workout recorded by the phone only, in an hour the watch
	// dominates: the hour belongs to the watch, so the workout gets the watch's
	// samples, the same ones the day's strain uses.
	around := append(series(model.SampleHeartRate, "Watch", t0, 60, time.Minute, 120),
		series(model.SampleHeartRate, "iPhone", t0.Add(40*time.Minute), 16, time.Minute, 140)...)
	w := model.Workout{Start: t0.Add(40 * time.Minute), End: t0.Add(55 * time.Minute)}
	got := WorkoutSamples(w, around)
	if len(got) != 16 || got[0].Source != "Watch" || !got[0].Start.Equal(w.Start) || !got[15].Start.Equal(w.End) {
		t.Fatalf("%d samples, first %+v", len(got), got[0])
	}
}

func seg(source, stage string, from time.Time, mins int) model.SleepSegment {
	return model.SleepSegment{Start: from, End: from.Add(time.Duration(mins) * time.Minute), Stage: stage, Source: source}
}

func TestDedupSleepSourcesCountsANightOnce(t *testing.T) {
	bed := time.Date(2026, 10, 17, 22, 30, 0, 0, time.UTC)
	watch := []model.SleepSegment{
		seg("Watch", model.SleepCore, bed, 120), seg("Watch", model.SleepDeep, bed.Add(2*time.Hour), 90),
		seg("Watch", model.SleepAwake, bed.Add(210*time.Minute), 10), seg("Watch", model.SleepREM, bed.Add(220*time.Minute), 200),
	}
	garmin := []model.SleepSegment{
		seg("Garmin", model.SleepCore, bed.Add(10*time.Minute), 200), seg("Garmin", model.SleepDeep, bed.Add(210*time.Minute), 150),
	}
	phone := []model.SleepSegment{seg("iPhone", model.SleepInBed, bed.Add(-15*time.Minute), 460)}

	night := append(append(append([]model.SleepSegment{}, watch...), garmin...), phone...)
	summed := SummarizeSleep(night)
	got := SummarizeSleep(DedupSleepSources(night))
	if want := SummarizeSleep(watch); got != want {
		t.Fatalf("got %+v, want the watch's night %+v", got, want)
	}
	if summed.AsleepMin <= got.AsleepMin+300 {
		t.Fatalf("summing should have doubled the night: %d vs %d", summed.AsleepMin, got.AsleepMin)
	}

	// The phone's time in bed alone is kept when it is all there is.
	if out := DedupSleepSources(phone); !reflect.DeepEqual(out, phone) {
		t.Fatal("a single source passes through")
	}
	// Only time in bed from two sources: the longer one, then the name.
	a := []model.SleepSegment{seg("A", model.SleepInBed, bed, 400), seg("B", model.SleepInBed, bed, 420)}
	if out := DedupSleepSources(a); len(out) != 1 || out[0].Source != "B" {
		t.Fatalf("longer time in bed should win: %+v", out)
	}
	tie := []model.SleepSegment{seg("B", model.SleepInBed, bed, 400), seg("A", model.SleepInBed, bed, 400)}
	if out := DedupSleepSources(tie); len(out) != 1 || out[0].Source != "A" {
		t.Fatalf("a tie goes to the first name: %+v", out)
	}
}
