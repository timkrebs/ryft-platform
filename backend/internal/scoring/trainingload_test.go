package scoring

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func TestRatedWorkoutTRIMP(t *testing.T) {
	for _, tt := range []struct {
		name    string
		minutes float64
		rpe     float64
		want    float64
	}{
		// i = max(0, rpe/10 - 0.25) / 0.75; TRIMP = minutes * i * 0.64 * exp(1.92*i).
		{"eight", 60, 8, 115.1},
		{"three", 60, 3, 2.9},
		{"five", 60, 5, 24.3},
		{"ten", 60, 10, 261.9},
		{"at the floor", 60, 2.5, 0},
		{"below the floor", 60, 2, 0},
		{"zero rating", 60, 0, 0},
		{"above ten", 60, 11, 0},
		{"NaN rating", 60, math.NaN(), 0},
		{"zero duration", 0, 8, 0},
		{"negative duration", -1, 8, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := RatedWorkoutTRIMP(tt.minutes, tt.rpe)
			if math.Round(got*10)/10 != tt.want || (tt.want == 0 && got != 0) {
				t.Errorf("TRIMP = %.4f, want %.1f", got, tt.want)
			}
		})
	}
}

func TestTrainingLoadOn(t *testing.T) {
	// Calendar dates only (UTC arithmetic): the 28 days back from Nov 12 span
	// Berlin's DST end, which must not shift a window (the api test covers the
	// local bucketing).
	date := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	day := func(offset int) string { return date.AddDate(0, 0, offset).Format(time.DateOnly) }
	hrDays := func(count int, acute, older float64) map[string]TrainingLoadDay {
		inputs := make(map[string]TrainingLoadDay, count)
		for i := 0; i < count; i++ {
			inputs[day(-i)] = TrainingLoadDay{HasHR: true}
		}
		inputs[day(0)] = TrainingLoadDay{CardioTRIMP: acute, HasHR: true}
		inputs[day(-7)] = TrainingLoadDay{CardioTRIMP: older, HasHR: true}
		return inputs
	}
	part := func(acute, chronic float64, days int) model.LoadPart {
		return model.LoadPart{Acute7D: acute, Chronic28DWeekly: chronic, DaysWithData: days, WindowDays: 28}
	}
	withState := func(p model.LoadPart, low, high float64, state string, ratio *float64) model.LoadPart {
		p.OptimalLow, p.OptimalHigh, p.State, p.Ratio = &low, &high, &state, ratio
		return p
	}
	ratio := func(n float64) *float64 { return &n }

	for _, tt := range []struct {
		name         string
		days         map[string]TrainingLoadDay
		firstSession string
		cardio       model.LoadPart
		muscular     model.LoadPart
	}{
		{
			name: "20 days keeps the band and ratio hidden", days: hrDays(20, 8.061, 32.315),
			cardio: part(8.1, 10.1, 20), muscular: part(0, 0, 0),
		},
		{
			name: "21 days and equality at the rounded low edge", days: hrDays(21, 8.061, 32.315),
			cardio: withState(part(8.1, 10.1, 21), 8.1, 13.1, model.LoadStateOptimal, ratio(0.80)), muscular: part(0, 0, 0),
		},
		{
			name: "equal to the rounded high edge", days: hrDays(21, 13.144, 27.232),
			cardio: withState(part(13.1, 10.1, 21), 8.1, 13.1, model.LoadStateOptimal, ratio(1.30)), muscular: part(0, 0, 0),
		},
		{
			name: "below the band", days: hrDays(21, 8.04, 32.336),
			cardio: withState(part(8.0, 10.1, 21), 8.1, 13.1, model.LoadStateBelow, ratio(0.79)), muscular: part(0, 0, 0),
		},
		{
			name: "above the band", days: hrDays(21, 13.16, 27.216),
			cardio: withState(part(13.2, 10.1, 21), 8.1, 13.1, model.LoadStateAbove, ratio(1.31)), muscular: part(0, 0, 0),
		},
		{
			name: "ratio divides the rounded figures", days: hrDays(21, 2.146, 6.094),
			cardio: withState(part(2.1, 2.1, 21), 1.7, 2.7, model.LoadStateOptimal, ratio(1)), muscular: part(0, 0, 0),
		},
		{
			name: "zero chronic and acute", days: hrDays(21, 0, 0), firstSession: day(-35),
			cardio:   withState(part(0, 0, 21), 0, 0, model.LoadStateOptimal, nil),
			muscular: withState(part(0, 0, 28), 0, 0, model.LoadStateOptimal, nil),
		},
		{
			name: "zero rounded chronic and positive rounded acute", days: hrDays(21, 0.06, 0),
			cardio: withState(part(0.1, 0, 21), 0, 0, model.LoadStateAbove, nil), muscular: part(0, 0, 0),
		},
		{
			name: "window edges and sparse days", firstSession: day(-9),
			days: map[string]TrainingLoadDay{
				day(1):   {CardioTRIMP: 100, HasHR: true, MuscularTRIMP: 200},
				day(0):   {CardioTRIMP: 2, HasHR: true, MuscularTRIMP: 4},
				day(-6):  {CardioTRIMP: 6, HasHR: true, MuscularTRIMP: 12},
				day(-7):  {CardioTRIMP: 7, HasHR: true, MuscularTRIMP: 14},
				day(-27): {CardioTRIMP: 27, HasHR: true, MuscularTRIMP: 54},
				day(-28): {CardioTRIMP: 28, HasHR: true, MuscularTRIMP: 56},
			},
			cardio: part(8, 10.5, 4), muscular: part(16, 21, 10),
		},
		{
			name: "rated load and data, including a zero-load rating", firstSession: day(-10),
			days: map[string]TrainingLoadDay{
				day(0):  {CardioTRIMP: 12.03, HasHR: true, HasRating: true},
				day(-1): {RatedTRIMP: 2.91, HasRating: true},
				day(-2): {HasRating: true},
			},
			cardio: part(14.9, 3.7, 3), muscular: part(0, 0, 11),
		},
		{
			name: "first session is after the selected day", firstSession: day(1),
			cardio: part(0, 0, 0), muscular: part(0, 0, 0),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := TrainingLoadOn(day(0), tt.days, tt.firstSession)
			want := model.TrainingLoad{Cardio: tt.cardio, Muscular: tt.muscular}
			if !reflect.DeepEqual(got, want) {
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(want)
				t.Errorf("load = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

func TestTrainingLoadNewAccountExample(t *testing.T) {
	date := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	days := make(map[string]TrainingLoadDay)
	for i := 0; i < 6; i++ {
		days[date.AddDate(0, 0, -i).Format(time.DateOnly)] = TrainingLoadDay{CardioTRIMP: 30, HasHR: true}
	}
	load, err := json.Marshal(TrainingLoadOn(date.Format(time.DateOnly), days, ""))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"cardio":{"acute_7d":180,"chronic_28d_weekly":45,"ratio":null,"optimal_low":null,"optimal_high":null,"state":null,"days_with_data":6,"window_days":28},"muscular":{"acute_7d":0,"chronic_28d_weekly":0,"ratio":null,"optimal_low":null,"optimal_high":null,"state":null,"days_with_data":0,"window_days":28}}`
	if string(load) != want {
		t.Errorf("new account load = %s, want %s", load, want)
	}
}
