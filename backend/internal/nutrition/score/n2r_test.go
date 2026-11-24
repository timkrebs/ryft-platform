package score

import (
	"math"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

var n2rDay = time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC)

func atHour(h float64) time.Time { return n2rDay.Add(time.Duration(h * float64(time.Hour))) }

func knownMeal(at time.Time, kcal, carb, sugar, sodium float64) model.NutritionMeal {
	no := false
	i := model.NutritionMealItem{WeightG: numberPtr(1000), QualityProfile: &model.NutritionQualityProfile{
		NonStarchyVegetableFraction: numberPtr(.3), WholeFruitFraction: numberPtr(.2), NutsLegumesFraction: numberPtr(.03),
		RedProcessedMeatFraction: numberPtr(0), SugaryDrink: &no,
	}, Nutrients: map[string]model.NutritionSnapValue{}}
	for _, v := range []struct {
		code, unit string
		amount     float64
	}{
		{"energy_kcal", "kcal", kcal}, {"carbohydrates", "g", carb}, {"added_sugar", "g", sugar}, {"sodium", "mg", sodium},
		{"fiber", "g", 30}, {"epa", "g", .5}, {"dha", "g", .5}, {"monounsaturated_fat", "g", kcal / 90},
		{"polyunsaturated_fat", "g", 0}, {"trans_fat", "g", 0}, {"protein", "g", 0}, {"fat", "g", 0},
		{"saturated_fat", "g", 0}, {"alcohol", "g", 0}, {"caffeine", "mg", 0},
	} {
		i.Nutrients[v.code] = model.NutritionSnapValue{Amount: v.amount, Unit: v.unit}
	}
	return model.NutritionMeal{ConsumedAt: at, ID: uuid.New(), Items: []model.NutritionMealItem{i}}
}

func expectNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Errorf("%s = %.10f, want %.10f", name, got, want)
	}
}

func TestN2RStrainAndExemptions(t *testing.T) {
	meal := knownMeal(atHour(10), 1600, 400, 100, 2500)
	for _, tc := range []struct {
		name, strain                                      string
		minutes                                           int
		wantCarb, wantNa, wantSugar, wantFuel, wantEnergy float64
	}{
		{"rest", "low", 0, 245, 0, 0, 1, 2000},
		{"moderate", "moderate", 60, 350, 720, 0, 1, 2400},
		{"endurance", "high", 90, 560, 1080, 70, .95, 2400},
		{"ultra", "ultra", 151, 700, 1812, 100, .892857142857, 2400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := N2RInput{Profile: model.N2RProfile{BodyMassKG: 70}, BaseKcal: 2000, Start: n2rDay, End: atHour(24), Meals: []model.NutritionMeal{meal}}
			if tc.minutes > 0 {
				in.Workouts = []model.Workout{{Activity: "running", Start: atHour(8), End: atHour(8).Add(time.Duration(tc.minutes) * time.Minute), EnergyKcal: numberPtr(400)}}
			}
			got := N2R(in)
			if got.Budget.Strain != tc.strain {
				t.Errorf("strain = %s", got.Budget.Strain)
			}
			expectNear(t, "carb target", got.Budget.CarbTarget, tc.wantCarb)
			expectNear(t, "sodium exemption", got.Budget.SodiumExemption, tc.wantNa)
			expectNear(t, "covered sugar", got.CoveredSugar, tc.wantSugar)
			expectNear(t, "net sugar", *got.NetSugar, 100-tc.wantSugar)
			expectNear(t, "fuel multiplier", got.FuelMultiplier, tc.wantFuel)
			expectNear(t, "energy target", got.Budget.EnergyTarget, tc.wantEnergy)
		})
	}
}

func TestN2RQuickTurnaroundAndGICoverage(t *testing.T) {
	first := model.Workout{Activity: "running", Start: atHour(8), End: atHour(10)}
	second := model.Workout{Activity: "strength", Start: atHour(19), End: atHour(20), Strain: numberPtr(13)}
	meal := knownMeal(atHour(13), 650, 200, 100, 500)
	meal.Items[0].Nutrients["glycemic_index"] = model.NutritionSnapValue{Amount: 75, Unit: "index"}
	in := N2RInput{Profile: model.N2RProfile{BodyMassKG: 40}, BaseKcal: 2000, Start: n2rDay, End: atHour(24),
		Workouts: []model.Workout{second, first}, Meals: []model.NutritionMeal{meal}}
	got := N2R(in)
	if got.Budget.Strain != "ultra" || !slices.Contains(got.Tags, "post_workout_refeed_hit") {
		t.Fatalf("unexpected score %+v", got)
	}
	expectNear(t, "GI eligible carbs", got.CoveredCarb, 192)
	expectNear(t, "sugar is part of eligible carbs", got.CoveredSugar, 96)
	upcoming := in
	upcoming.Workouts = []model.Workout{first}
	upcoming.Profile.NextWorkoutAt = &second.Start
	if got := N2R(upcoming); got.Budget.Strain != "ultra" || got.CoveredCarb != 192 {
		t.Fatalf("scheduled training did not extend the window: %+v", got)
	}
	second.Start, second.End = atHour(20), atHour(21) // exactly ten hours
	in.Workouts[0] = second
	if got := N2R(in); got.CoveredCarb != 0 {
		t.Fatalf("standard window extended: %+v", got)
	}
	prior := knownMeal(atHour(23.5), 500, 120, 55, 500)
	today := knownMeal(atHour(24.5), 650, 120, 30, 500)
	in = N2RInput{Profile: model.N2RProfile{BodyMassKG: 40}, BaseKcal: 2000, Start: atHour(24), End: atHour(48),
		Workouts: []model.Workout{{Activity: "running", Start: atHour(21), End: atHour(23)}}, Meals: []model.NutritionMeal{today}, PriorMeals: []model.NutritionMeal{prior}}
	expectNear(t, "midnight budget remainder", N2R(in).CoveredSugar, 5)
}

func TestN2RAlcoholAndCaffeine(t *testing.T) {
	for _, tc := range []struct {
		dose, lead, want float64
		sex              string
	}{
		{0, 3, 0, "male"}, {.2, 2, 4.022857142857, "male"}, {.5, 0, 14.428571428571, "male"},
		{.5, 0, 16.592857142857, "female"}, {2, 0, 25, "female"}, {.5, 20, 7.935714285714, "male"},
	} {
		expectNear(t, "alcohol", alcoholPenalty(tc.dose, tc.lead, tc.sex), tc.want)
	}
	for _, tc := range []struct{ dose, hours, half float64 }{{107, 9, 5}, {107, 9, 7.5}, {217.5, 8, 5}, {217.5, 8, 7.5}} {
		bed := atHour(23)
		m := knownMeal(atHour(23-tc.hours), 700, 70, 0, 100)
		m.Items[0].Nutrients["caffeine"] = model.NutritionSnapValue{Amount: tc.dose, Unit: "mg"}
		afterBed := knownMeal(atHour(23.5), 30, 0, 0, 0)
		afterBed.Items[0].Nutrients["caffeine"] = model.NutritionSnapValue{Amount: 500, Unit: "mg"}
		got := N2R(N2RInput{Profile: model.N2RProfile{BodyMassKG: 70, CaffeineHalfH: numberPtr(tc.half)}, BaseKcal: 2000,
			Start: n2rDay, End: atHour(24), Bedtime: &bed, Meals: []model.NutritionMeal{m, afterBed}})
		want := tc.dose * math.Pow(.5, tc.hours/tc.half)
		expectNear(t, "residual", *got.CaffeineBedMg, want)
		expectNear(t, "caffeine penalty", got.CaffeinePenalty, min(15, (want-25)*.15))
	}
}

func TestN2RLateCompositionAndCGM(t *testing.T) {
	dinner := knownMeal(atHour(22), 800, 100, 35, 800)
	dinner.Items[0].Nutrients["saturated_fat"] = model.NutritionSnapValue{Amount: 23, Unit: "g"}
	dinner.Items[0].Nutrients["fat"] = model.NutritionSnapValue{Amount: 35, Unit: "g"}
	casein := knownMeal(atHour(21.5), 150, 0, 0, 100)
	casein.Items[0].Nutrients["protein"] = model.NutritionSnapValue{Amount: 35, Unit: "g"}
	penalty, high := lateMeal(dinner, 1, 2000, true)
	caseinP, _ := lateMeal(casein, 1.5, 2000, true)
	noStrength, _ := lateMeal(casein, 1.5, 2000, false)
	expectNear(t, "late dinner", penalty, 24*math.Pow(.75, 1.3)*.4*(1+.5*207/800+.4*140/800))
	expectNear(t, "late casein", caseinP, 24*math.Pow(.625, 1.3)*(52.0/2000))
	if !high || caseinP >= noStrength || noStrength >= penalty {
		t.Fatal("late meal composition or exemption wrong")
	}
	if p, _ := lateMeal(dinner, 4, 2000, true); p != 0 {
		t.Fatal("four-hour boundary")
	}
	bed := atHour(23)
	morning := knownMeal(atHour(10), 800, 100, 5, 300)
	data := map[uuid.UUID]model.NutritionMealGlucoseScore{morning.ID: {GlucoseScore: 60}, dinner.ID: {GlucoseScore: 80}}
	in := N2RInput{Profile: model.N2RProfile{BodyMassKG: 70}, BaseKcal: 2000, Start: n2rDay, End: atHour(24), Bedtime: &bed,
		Meals: []model.NutritionMeal{dinner, morning}, Workouts: []model.Workout{{Activity: "running", Start: atHour(8), End: atHour(9.5)}}, Glucose: data}
	got := N2R(in)
	expectNear(t, "weighted CGM", *got.Glucose, (.2*60+1.5*80)/1.7)
	expectNear(t, "with CGM", *got.Score, .70*(*got.Quality)*got.FuelMultiplier+.30*(*got.Glucose)-got.AutoPenalty)
	in.Glucose = nil
	got = N2R(in)
	expectNear(t, "without CGM", *got.Score, *got.Quality*got.FuelMultiplier-got.AutoPenalty)
}

func TestN2RUnlockDepotAndFloor(t *testing.T) {
	in := N2RInput{Profile: model.N2RProfile{BodyMassKG: 70}, BaseKcal: 2000, Start: n2rDay, End: atHour(24),
		Meals: []model.NutritionMeal{knownMeal(atHour(12), 599, 100, 0, 100)}}
	if got := N2R(in); got.Score != nil || got.Components.FiberG != nil {
		t.Fatalf("unlocked early: %+v", got)
	}
	in.Meals[0] = knownMeal(atHour(12), 600, 100, 0, 100)
	if got := N2R(in); got.Score == nil || *got.Score != 100 {
		t.Fatalf("perfect day: %+v", got)
	}
	expectNear(t, "EWMA ignores day 14", *omegaEWMA([]OmegaDay{{0, 250}, {1, 1000}, {14, 99999}}), (250+1000*13.0/15)/(1+13.0/15))
	for _, tc := range []struct{ mg, points float64 }{{0, 0}, {250, 7}, {625, 8.5}, {1000, 10}, {4000, 10}} {
		expectNear(t, "omega curve", *omegaPoints(&tc.mg), tc.points)
	}
	bed := atHour(23)
	in.Bedtime = &bed
	m := knownMeal(bed, 2000, 100, 200, 7000)
	m.Items[0].WeightG = numberPtr(0)
	for code, value := range map[string]model.NutritionSnapValue{"alcohol": {Amount: 90, Unit: "g"}, "caffeine": {Amount: 400, Unit: "mg"},
		"trans_fat": {Amount: 15, Unit: "g"}, "fiber": {Amount: 0, Unit: "g"}, "epa": {Amount: 0, Unit: "g"}, "dha": {Amount: 0, Unit: "g"}} {
		m.Items[0].Nutrients[code] = value
	}
	in.Meals = []model.NutritionMeal{m}
	got := N2R(in)
	if *got.Score != 0 || got.AutoPenalty != 45 {
		t.Fatalf("score and penalty caps: %+v", got)
	}
}

func TestN2RMorningCalibrationAndGuidance(t *testing.T) {
	var prior []Overnight
	for i, value := range []float64{3, 3, 3.5, 4, 4.5, 5, 5} {
		prior = append(prior, Overnight{Date: n2rDay.AddDate(0, 0, i-7), RMSSDms: numberPtr(math.Exp(value)), RHR: numberPtr(50)})
	}
	morning := MorningBaseline(Overnight{Date: n2rDay, RMSSDms: numberPtr(math.Exp(3.4)), RHR: numberPtr(53)}, prior)
	expectNear(t, "RMSSD average", *morning.RMSSDMean, 4)
	expectNear(t, "RMSSD SD", *morning.RMSSDSD, math.Sqrt(.75))
	expectNear(t, "SWC", *morning.SWC, .2*math.Sqrt(.75))
	if morning.Change != "moderate" || *morning.RHRDelta != 3 {
		t.Fatalf("morning %+v", morning)
	}
	var pairs []CaffeinePair
	for i := 0; i < 19; i++ {
		dose := float64(i) * 5
		pairs = append(pairs, CaffeinePair{WakeDate: n2rDay.AddDate(0, 0, -i), CaffeineMg: dose, RMSSDDelta: numberPtr(-dose * .01), SWC: numberPtr(.1)})
	}
	expectNear(t, "too little evidence", CalibrateCaffeine(n2rDay, 5, pairs[:13]), 5)
	expectNear(t, "step", CalibrateCaffeine(n2rDay, 5, pairs), 5.5)
	expectNear(t, "ceiling", CalibrateCaffeine(n2rDay, 7.5, pairs), 7.5)
	bed := atHour(23)
	cards := Guidance(GuidanceInput{Now: atHour(20), Profile: model.N2RProfile{BodyMassKG: 70}, Morning: &morning, SleepLatencyMinutes: numberPtr(35),
		Score: model.N2RScore{Bedtime: &bed, AlcoholGKG: .25, Budget: model.N2RBudget{EnergyTarget: 2000}, LoggedKcal: 1000}, Workouts: []model.Workout{
			{Activity: "running", Start: atHour(18), End: atHour(19.5)}, {Activity: "strength", Start: atHour(14), End: atHour(15), Strain: numberPtr(12)},
		}})
	want := []string{GuideAlcohol, GuideEndurance, GuideRed, GuideStrength, GuideCaffeine, GuideLate}
	if len(cards) != len(want) {
		t.Fatalf("cards %+v", cards)
	}
	for i, code := range want {
		if cards[i].Code != code {
			t.Errorf("card %d = %s want %s", i, cards[i].Code, code)
		}
	}
	if cards[1].Targets["carb_min_g"] != 56 || cards[2].Targets["optional_glycine_g"] != 3 {
		t.Fatal(cards)
	}
	meal := knownMeal(atHour(10), 100, 0, 0, 0)
	meal.Items[0].Nutrients["caffeine"] = model.NutritionSnapValue{Amount: 107, Unit: "mg"}
	meal.Items = append(meal.Items, meal.Items[0])
	cards = Guidance(GuidanceInput{Now: atHour(12), Score: model.N2RScore{Bedtime: &bed, CaffeineHalfH: 5}, Meals: []model.NutritionMeal{meal}})
	if len(cards) != 1 || cards[0].Code != GuideCaffeine {
		t.Fatalf("compound caffeine dose: %+v", cards)
	}
}
