package scoring

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

type RestingInputs struct {
	WeightKG  *float64
	Sex       *string
	BirthYear *int
}

// RestingEstimateKcal uses Henry's weight-only adult equations in MJ/day.
func RestingEstimateKcal(in RestingInputs, day time.Time, elapsed float64) *int {
	if in.WeightKG == nil || in.BirthYear == nil || *in.WeightKG <= 0 {
		return nil
	}
	weight := *in.WeightKG
	var male, female float64
	switch age := day.Year() - *in.BirthYear; {
	case age < 18:
		return nil
	case age < 30:
		male, female = 0.0669*weight+2.28, 0.0546*weight+2.33
	case age < 60:
		male, female = 0.0592*weight+2.48, 0.0407*weight+2.90
	default:
		male, female = 0.0563*weight+2.15, 0.0424*weight+2.38
	}
	mj := (male + female) / 2
	if in.Sex != nil {
		switch *in.Sex {
		case "male":
			mj = male
		case "female":
			mj = female
		}
	}
	const kcalPerMJ = 239.006
	kcal := int(math.Round(mj * kcalPerMJ * min(max(elapsed, 0), 1)))
	return &kcal
}

type EnergyInputs struct {
	IntakeKcal       float64
	Meals            int
	DiaryOn          bool
	IntakeComplete   bool
	ActiveKcal       *int
	MeasuredResting  *int
	EstimatedResting *int
	PartialDay       bool
}

// DayEnergyOf combines the known totals and grades confidence when all are present.
func DayEnergyOf(in EnergyInputs) *model.DayEnergy {
	out := &model.DayEnergy{ActiveKcal: in.ActiveKcal, Missing: []string{}, PartialDay: in.PartialDay}
	if in.DiaryOn && in.Meals > 0 {
		intake := int(math.Round(in.IntakeKcal))
		out.IntakeKcal = &intake
	}
	switch {
	case in.MeasuredResting != nil:
		source := model.RestingSourceHealthKit
		out.RestingKcal, out.RestingSource = in.MeasuredResting, &source
	case in.EstimatedResting != nil:
		source := model.RestingSourceEstimated
		out.RestingKcal, out.RestingSource = in.EstimatedResting, &source
	}
	if out.ActiveKcal == nil {
		out.Missing = append(out.Missing, model.EnergyPartActive)
	}
	if out.IntakeKcal == nil {
		out.Missing = append(out.Missing, model.EnergyPartIntake)
	}
	if out.RestingKcal == nil {
		out.Missing = append(out.Missing, model.EnergyPartResting)
	}
	if len(out.Missing) > 0 {
		return out
	}
	net := *out.IntakeKcal - (*out.ActiveKcal + *out.RestingKcal)
	out.NetKcal = &net
	unmet := 0
	if in.MeasuredResting == nil {
		unmet++
	}
	if in.Meals < 2 {
		unmet++
	}
	if in.PartialDay {
		unmet++
	}
	if !in.IntakeComplete {
		unmet++
	}
	confidence := [...]string{model.EnergyConfidenceHigh, model.EnergyConfidenceMedium, model.EnergyConfidenceLow}[min(unmet, 2)]
	out.Confidence = &confidence
	return out
}
