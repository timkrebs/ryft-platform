package scoring

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

func TestRestingEstimateKcal(t *testing.T) {
	day := time.Date(2026, time.November, 27, 0, 0, 0, 0, time.UTC)
	weight := ptrTo(70.0)
	for _, tc := range []struct {
		name    string
		in      RestingInputs
		elapsed float64
		want    *int
	}{
		// At 70 kg, plug weight into each equation, multiply by 239.006, then round.
		{"male 18", RestingInputs{weight, ptrTo("male"), ptrTo(2008)}, 1, ip(1664)},            // 6.963 MJ × 239.006 = 1664.199
		{"male 30", RestingInputs{weight, ptrTo("male"), ptrTo(1996)}, 1, ip(1583)},            // 6.624 MJ × 239.006 = 1583.176
		{"male 60", RestingInputs{weight, ptrTo("male"), ptrTo(1966)}, 1, ip(1456)},            // 6.091 MJ × 239.006 = 1455.786
		{"female 29", RestingInputs{weight, ptrTo("female"), ptrTo(1997)}, 1, ip(1470)},        // 6.152 MJ × 239.006 = 1470.365
		{"female 59", RestingInputs{weight, ptrTo("female"), ptrTo(1967)}, 1, ip(1374)},        // 5.749 MJ × 239.006 = 1374.045
		{"female 60", RestingInputs{weight, ptrTo("female"), ptrTo(1966)}, 1, ip(1278)},        // 5.348 MJ × 239.006 = 1278.204
		{"unspecified", RestingInputs{weight, ptrTo("unspecified"), ptrTo(2008)}, 1, ip(1567)}, // mean 6.5575 MJ = 1567.282 kcal
		{"nil sex", RestingInputs{weight, nil, ptrTo(1996)}, 1, ip(1479)},                      // mean 6.1865 MJ = 1478.611 kcal
		{"unknown sex", RestingInputs{weight, ptrTo("unknown"), ptrTo(1966)}, 1, ip(1367)},     // mean 5.7195 MJ = 1366.995 kcal
		{"half day", RestingInputs{weight, ptrTo("male"), ptrTo(2008)}, 0.5, ip(832)},          // 1664.199 × 0.5 = 832.099
		{"clamped below", RestingInputs{weight, ptrTo("male"), ptrTo(2008)}, -0.5, ip(0)},
		{"clamped above", RestingInputs{weight, ptrTo("male"), ptrTo(2008)}, 1.5, ip(1664)},
		{"age 17", RestingInputs{weight, ptrTo("male"), ptrTo(2009)}, 1, nil},
		{"missing weight", RestingInputs{nil, ptrTo("male"), ptrTo(2008)}, 1, nil},
		{"missing birth year", RestingInputs{weight, ptrTo("male"), nil}, 1, nil},
		{"zero weight", RestingInputs{ptrTo(0.0), nil, ptrTo(2008)}, 1, nil},
		{"negative weight", RestingInputs{ptrTo(-1.0), nil, ptrTo(2008)}, 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := RestingEstimateKcal(tc.in, day, tc.elapsed)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatal("unexpected resting estimate")
			}
		})
	}
}

func TestDayEnergyConfidence(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		measured, partial, complete bool
		meals                       int
		confidence                  string
	}{
		{"high", true, false, true, 2, model.EnergyConfidenceHigh},
		{"estimated", false, false, true, 2, model.EnergyConfidenceMedium},
		{"one meal", true, false, true, 1, model.EnergyConfidenceMedium},
		{"partial day", true, true, true, 2, model.EnergyConfidenceMedium},
		{"two unmet", false, true, true, 2, model.EnergyConfidenceLow},
		{"three unmet", false, true, true, 1, model.EnergyConfidenceLow},
		{"incomplete drops high", true, false, false, 2, model.EnergyConfidenceMedium},
		{"incomplete drops medium", true, true, false, 2, model.EnergyConfidenceLow},
		{"incomplete stays low", false, true, false, 2, model.EnergyConfidenceLow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := EnergyInputs{IntakeKcal: 1234.5, Meals: tc.meals, DiaryOn: true, IntakeComplete: tc.complete,
				ActiveKcal: ip(250), EstimatedResting: ip(950), PartialDay: tc.partial}
			resting, source, net := 950, model.RestingSourceEstimated, 35
			if tc.measured {
				in.MeasuredResting = ip(1200)
				resting, source, net = 1200, model.RestingSourceHealthKit, -215
			}
			got := DayEnergyOf(in)
			if got.Confidence == nil || *got.Confidence != tc.confidence || got.PartialDay != tc.partial {
				t.Fatal("unexpected confidence or partial day")
			}
			if got.IntakeKcal == nil || *got.IntakeKcal != 1235 || got.ActiveKcal == nil || *got.ActiveKcal != 250 ||
				got.RestingKcal == nil || *got.RestingKcal != resting || got.RestingSource == nil || *got.RestingSource != source ||
				got.NetKcal == nil || *got.NetKcal != net || got.Missing == nil || len(got.Missing) != 0 {
				t.Fatal("unexpected energy balance")
			}
		})
	}
}

func TestDayEnergyMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   EnergyInputs
		want []string
	}{
		{"all missing", EnergyInputs{DiaryOn: true}, []string{"active", "intake", "resting"}},
		{"diary off", EnergyInputs{Meals: 2, IntakeKcal: 900, ActiveKcal: ip(250), MeasuredResting: ip(1400)}, []string{"intake"}},
		{"active missing", EnergyInputs{DiaryOn: true, Meals: 2, MeasuredResting: ip(1400)}, []string{"active"}},
		{"resting missing", EnergyInputs{DiaryOn: true, Meals: 2, ActiveKcal: ip(250)}, []string{"resting"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DayEnergyOf(tc.in)
			if !slices.Equal(got.Missing, tc.want) || got.Confidence != nil || got.NetKcal != nil ||
				(got.RestingKcal == nil) != (got.RestingSource == nil) {
				t.Fatal("unexpected missing parts or nullable fields")
			}
		})
	}
	got := DayEnergyOf(EnergyInputs{DiaryOn: true, Meals: 2, ActiveKcal: ip(0), MeasuredResting: ip(0)})
	if got.IntakeKcal == nil || *got.IntakeKcal != 0 || got.NetKcal == nil || *got.NetKcal != 0 ||
		got.Confidence == nil || *got.Confidence != model.EnergyConfidenceMedium || len(got.Missing) != 0 {
		t.Fatal("meals without item energy should give an incomplete zero subtotal")
	}
}

func TestEngineRestingKcalRescore(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	const day = "2026-10-24"
	base, err := time.Parse(dayLayout, day)
	if err != nil {
		t.Fatal(err)
	}
	sample := func(hour, minute int, value float64, source string) model.Sample {
		start := base.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
		return model.Sample{Type: model.SampleBasalEnergy, Start: start, End: start.Add(15 * time.Minute),
			Value: value, Unit: "kcal", Source: source}
	}
	if _, err := st.UpsertSamples(ctx, user, []model.Sample{
		sample(7, 0, 70.25, "watch"), sample(7, 15, 80.25, "watch"),
		sample(7, 0, 150, "phone"), sample(23, 55, 25, "phone"),
	}); err != nil {
		t.Fatal(err)
	}
	const next = "2026-10-25"
	if err := engine.ComputeRange(ctx, user, day, next); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		rows, err := st.DailyScores(ctx, user, day, next)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 2 || rows[0].RestingKcal == nil || *rows[0].RestingKcal != 176 ||
			rows[0].ActiveKcal != nil || rows[1].RestingKcal != nil {
			t.Fatal("stored resting total differs from the deduplicated sum on the start day")
		}
	}
	check()
	if changed, err := engine.computeDay(ctx, user, day); err != nil || changed {
		t.Fatalf("scoring the same samples again changed history inputs: %v, %v", changed, err)
	}
	check()
}
