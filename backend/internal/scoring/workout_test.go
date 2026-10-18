package scoring

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

func TestHRRZoneBoundaries(t *testing.T) {
	for hrr, want := range map[float64]int{0.49: -1, 0.5: 0, 0.6: 1, 0.69: 1, 0.7: 2, 0.8: 3, 0.9: 4, 1: 4} {
		if got := hrrZone(hrr); got != want {
			t.Errorf("hrrZone(%.2f) = %d, want %d", hrr, got, want)
		}
	}
}

func TestLoadFromAverageCreditsOneZone(t *testing.T) {
	r := LoadFromAverage(155, 50, 190, 60*time.Minute) // 75 % HRR → zone 3 (70-80)
	if r.ZoneMinutes != [5]float64{0, 0, 60, 0, 0} || r.Minutes != 60 {
		t.Fatalf("zones %v minutes %.0f", r.ZoneMinutes, r.Minutes)
	}
	if r.Score != StrainFromAverage(155, 50, 190, 60*time.Minute) {
		t.Fatal("LoadFromAverage and StrainFromAverage must agree")
	}
}

func TestDownsampleHRKeepsShapeAndBounds(t *testing.T) {
	start := time.Date(2026, 10, 18, 17, 30, 0, 0, time.UTC)
	pts := make([]HRPoint, 3600) // one hour at 1 Hz
	for k := range pts {
		pts[k] = HRPoint{T: start.Add(time.Duration(k) * time.Second), BPM: 140}
	}
	pts[1800].BPM = 188 // a single sprint peak

	got := DownsampleHR(pts, MaxHRPoints)
	if len(got) > MaxHRPoints || len(got) < MaxHRPoints-1 {
		t.Fatalf("got %d points, want ~%d", len(got), MaxHRPoints)
	}
	if got[0] != pts[0] || got[len(got)-1] != pts[len(pts)-1] {
		t.Fatal("first and last samples must be kept")
	}
	peak := false
	for k, p := range got {
		peak = peak || p.BPM == 188
		if k > 0 && !p.T.After(got[k-1].T) {
			t.Fatalf("output not strictly increasing in time at %d", k)
		}
	}
	if !peak {
		t.Fatal("LTTB must keep the isolated peak")
	}
	short := pts[:100]
	if out := DownsampleHR(short, MaxHRPoints); len(out) != 100 {
		t.Fatalf("short series must pass through untouched, got %d", len(out))
	}
}

func TestWorkoutDetail(t *testing.T) {
	start := time.Date(2026, 10, 18, 17, 30, 0, 0, time.UTC)
	var hr []model.Sample
	for m := 0; m <= 40; m++ {
		hr = append(hr, model.Sample{Type: model.SampleHeartRate, Start: start.Add(time.Duration(m) * time.Minute), Value: 150 + float64(m%3)})
	}
	trimp := 76.6
	row := store.WorkoutRow{
		Workout:     model.Workout{ID: uuid.New(), Activity: "running", Start: start, End: start.Add(40 * time.Minute)},
		ZoneMinutes: []float64{0, 4.6, 20.2, 15.1, 0}, TRIMP: &trimp,
	}
	d := WorkoutDetail(row, hr)
	if len(d.HR) != 41 || d.HR[0].BPM != 150 {
		t.Fatalf("hr series: %d points, first %+v", len(d.HR), d.HR[0])
	}
	if d.AvgHR == nil || *d.AvgHR != 151 || d.MaxHR == nil || *d.MaxHR != 152 {
		t.Fatalf("avg/max must be derived from samples when missing: %v %v", d.AvgHR, d.MaxHR)
	}
	if d.TRIMP == nil || *d.TRIMP != 77 || len(d.ZoneMin) != 5 || d.ZoneMin[2] != 20 {
		t.Fatalf("load: trimp %v zones %v", d.TRIMP, d.ZoneMin)
	}

	sent := 160.0
	row.AvgHR = &sent
	row.ZoneMinutes, row.TRIMP = nil, nil
	d = WorkoutDetail(row, nil)
	if *d.AvgHR != 160 || d.MaxHR != nil || d.HR == nil || len(d.HR) != 0 {
		t.Fatalf("app-sent avg must win; empty series must be [] not null: %+v", d)
	}
	if d.ZoneMin != nil || d.TRIMP != nil {
		t.Fatal("an unscored workout has null zones and trimp")
	}
}

func TestDayStrainFrom(t *testing.T) {
	if DayStrainFrom(store.DayLoad{}) != nil {
		t.Fatal("no HR data → nil")
	}
	trimp, mins := 41.4, 912
	got := DayStrainFrom(store.DayLoad{TRIMP: &trimp, HRMin: &mins, ZoneMinutes: []float64{3.4, 10.5, 0, 0, 0}})
	if got == nil || got.TRIMP != 41 || got.HRMin != 912 || got.ZoneMin[1] != 11 {
		t.Fatalf("got %+v", got)
	}
	zero, none := 0.0, 0 // under 30 s of HR: strain is 0.0, not null, so neither is this
	if DayStrainFrom(store.DayLoad{TRIMP: &zero, HRMin: &none, ZoneMinutes: make([]float64, 5)}) == nil {
		t.Fatal("a scored day with < 1 HR minute must still have a strain object")
	}
}
