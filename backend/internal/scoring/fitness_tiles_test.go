package scoring

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func fitnessTileTime(t *testing.T, loc *time.Location, wall string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02 15:04", wall, loc)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestVO2MaxOn(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	reading := func(wall string, value float64) model.Sample {
		return model.Sample{Start: fitnessTileTime(t, loc, wall).UTC(), Value: value}
	}
	sourced := func(s model.Sample, source string) model.Sample {
		s.Source = source
		return s
	}
	for _, tc := range []struct {
		name       string
		readings   []model.Sample
		vo2, delta *float64
	}{
		{"newest wins, larger value breaks ties", []model.Sample{
			reading("2026-11-26 17:00", 48.04), reading("2026-11-25 17:00", 51), reading("2026-11-26 17:00", 47),
		}, ptrTo(48.0), nil},
		{"only a ten-day-old reading", []model.Sample{reading("2026-11-17 12:00", 45.46)}, ptrTo(45.5), nil},
		{"day minus six is recent, delta uses unrounded readings", []model.Sample{
			reading("2026-11-18 17:00", 45.46), reading("2026-11-21 00:00", 48.04), reading("2026-11-20 23:59", 46.46),
		}, ptrTo(48.0), ptrTo(1.6)},
		{"day minus seven is outside the recent window", []model.Sample{
			reading("2026-11-18 12:00", 45), reading("2026-11-20 23:59", 47),
		}, ptrTo(47.0), nil},
		{"day minus 29 is the current lower bound", []model.Sample{reading("2026-10-29 00:00", 45)}, ptrTo(45.0), nil},
		{"day minus 30 is outside the current window", []model.Sample{reading("2026-10-28 23:59", 45)}, nil, nil},
		{"only a 31-day-old reading", []model.Sample{reading("2026-10-27 12:00", 45)}, nil, nil},
		{"day minus 36 is the comparison lower bound", []model.Sample{
			reading("2026-10-22 00:00", 49), reading("2026-11-27 23:30", 46),
		}, ptrTo(46.0), ptrTo(-3.0)},
		{"a reading before the comparison lower bound is too old", []model.Sample{
			reading("2026-10-21 23:59", 49), reading("2026-11-27 23:30", 46),
		}, ptrTo(46.0), nil},
		{"local midnight excludes the next day", []model.Sample{
			reading("2026-11-27 23:30", 47), reading("2026-11-28 00:00", 55),
		}, ptrTo(47.0), nil},
		{"no readings", nil, nil, nil},
		{"implausible readings are ignored, the bounds are inclusive", []model.Sample{
			reading("2026-11-10 12:00", 10), reading("2026-11-26 12:00", 100), reading("2026-11-27 08:00", 0),
			reading("2026-11-27 09:00", -3), reading("2026-11-27 10:00", 9.9), reading("2026-11-27 11:00", 100.1),
			reading("2026-11-27 12:00", math.NaN()),
		}, ptrTo(100.0), ptrTo(90.0)},
		{"only implausible readings", []model.Sample{reading("2026-11-27 12:00", 0)}, nil, nil},
		{"the change compares the latest reading's own source", []model.Sample{
			sourced(reading("2026-11-15 12:00", 45), "Watch"), sourced(reading("2026-11-18 12:00", 52), "Ring"),
			sourced(reading("2026-11-27 12:00", 46), "Watch"),
		}, ptrTo(46.0), ptrTo(1.0)},
		{"no earlier reading from the latest's source", []model.Sample{
			sourced(reading("2026-11-18 12:00", 52), "Ring"), sourced(reading("2026-11-27 12:00", 46), "Watch"),
		}, ptrTo(46.0), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vo2, delta := VO2MaxOn("2026-11-27", loc, tc.readings)
			checkFitnessTile(t, "vo2max", vo2, tc.vo2)
			checkFitnessTile(t, "vo2max_delta_7d", delta, tc.delta)
		})
	}
}

func TestHRRMean(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	reading := func(wall string, value float64) model.Sample {
		return model.Sample{Start: fitnessTileTime(t, loc, wall).UTC(), Value: value}
	}
	readings := []model.Sample{
		reading("2026-11-26 23:59", 80), reading("2026-11-27 00:00", 30), reading("2026-11-27 23:59", 37.16),
		reading("2026-11-28 00:00", 42), reading("2026-11-27 13:00", -25), reading("2026-11-27 14:00", 0),
		reading("2026-11-27 15:00", math.NaN()), reading("2026-11-27 16:00", math.Inf(1)),
		reading("2026-11-27 17:00", math.Inf(-1)), reading("2026-11-29 18:00", 0),
		// The bounds are inclusive; 0.9 and 121 are dropped (D311).
		reading("2026-12-01 12:00", 1), reading("2026-12-01 13:00", 120), reading("2026-12-01 14:00", 0.9),
		reading("2026-12-01 15:00", 121), reading("2026-12-02 12:00", 900), reading("2026-12-02 13:00", math.MaxFloat64),
	}
	for _, tc := range []struct {
		day  string
		want *float64
	}{
		{"2026-11-26", ptrTo(80.0)}, {"2026-11-27", ptrTo(33.6)}, {"2026-11-28", ptrTo(42.0)},
		{"2026-11-29", nil}, {"2026-11-30", nil}, {"2026-12-01", ptrTo(60.5)}, {"2026-12-02", nil},
	} {
		t.Run(tc.day, func(t *testing.T) {
			checkFitnessTile(t, "hrr_bpm", HRRMean(tc.day, loc, readings), tc.want)
		})
	}
}

func TestActiveMinutes(t *testing.T) {
	day := time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)
	at := func(minute int) time.Time { return day.Add(time.Duration(minute) * time.Minute) }
	point := func(minute int, bpm float64) HRPoint { return HRPoint{T: at(minute), BPM: bpm} }
	workout := func(from, to int) [2]time.Time { return [2]time.Time{at(from), at(to)} }
	for _, tc := range []struct {
		name     string
		points   []HRPoint
		maxHR    float64
		workouts [][2]time.Time
		want     *int
	}{
		{"below zone 1", []HRPoint{point(60, 119), point(63, 130)}, 180, nil, ptrTo(0)},
		{"zone 1 starts at half reserve, unsorted points", []HRPoint{point(63, 100), point(60, 120)}, 180, nil, ptrTo(3)},
		{"long sample gap credits five minutes", []HRPoint{point(60, 135), point(75, 100)}, 180, nil, ptrTo(5)},
		{"repeated timestamps credit no minutes", []HRPoint{point(60, 135), point(60, 135)}, 180, nil, ptrTo(0)},
		{"invalid reserve", []HRPoint{point(60, 135), point(63, 135)}, 60, nil, ptrTo(0)},
		{"workout only", nil, 180, [][2]time.Time{workout(60, 90)}, ptrTo(30)},
		{"heart rate inside workout counts once", []HRPoint{point(65, 160), point(69, 100)}, 180, [][2]time.Time{workout(60, 90)}, ptrTo(30)},
		{"overlapping workouts and separate heart rate", []HRPoint{point(50, 160), point(54, 100)}, 180,
			[][2]time.Time{workout(70, 95), workout(60, 90)}, ptrTo(39)},
		{"workouts clip at both midnights", nil, 180, [][2]time.Time{workout(-30, 15), workout(1430, 1460)}, ptrTo(25)},
		{"heart rate clips at both midnights", []HRPoint{point(-2, 160), point(2, 100), point(1438, 160), point(1442, 100)},
			180, nil, ptrTo(4)},
		{"round the union once", nil, 180, [][2]time.Time{{at(60), at(60).Add(35 * time.Second)}, {at(80), at(80).Add(35 * time.Second)}}, ptrTo(1)},
		{"one point", []HRPoint{point(60, 160)}, 180, nil, nil},
		{"nothing", nil, 180, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.points)
			checkFitnessTile(t, "active_min", ActiveMinutes(tc.points, 60, tc.maxHR, tc.workouts, day, at(1440)), tc.want)
			if !slices.Equal(tc.points, before) {
				t.Error("ActiveMinutes reordered the caller's points")
			}
		})
	}
}

func checkFitnessTile[T comparable](t *testing.T, field string, got, want *T) {
	t.Helper()
	switch {
	case got == nil && want != nil:
		t.Errorf("%s = nil, want %v", field, *want)
	case got != nil && want == nil:
		t.Errorf("%s = %v, want nil", field, *got)
	case got != nil && *got != *want:
		t.Errorf("%s = %v, want %v", field, *got, *want)
	}
}
