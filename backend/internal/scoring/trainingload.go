package scoring

import (
	"math"
	"time"

	"github.com/timkrebs/ryft/internal/model"
)

// The Fitness training load's constants (SCORING.md → Training load (Fitness), D306):
// the 28-day window (also the lookback the api reads), the 7 acute days, the
// divisor to weekly units, the band 0.8–1.3 × chronic, and the 21 data days a
// state needs.
const (
	TrainingLoadWindowDays     = 28
	trainingLoadAcuteDays      = 7
	trainingLoadChronicDivisor = 4
	trainingLoadLow            = 0.8
	trainingLoadHigh           = 1.3
	trainingLoadMinDays        = 21
)

// TrainingLoadDay holds the inputs for one scored local date.
type TrainingLoadDay struct {
	CardioTRIMP   float64
	RatedTRIMP    float64
	HasHR         bool
	HasRating     bool
	MuscularTRIMP float64
}

// ValidWorkoutRPE reports whether a rating counts as Fitness training-load data.
func ValidWorkoutRPE(rpe float64) bool {
	return rpe >= 1 && rpe <= 10
}

// RatedWorkoutTRIMP maps session RPE onto the same scale as day strain.
func RatedWorkoutTRIMP(minutes, rpe float64) float64 {
	if !ValidWorkoutRPE(rpe) || !(minutes > 0) {
		return 0
	}
	return minutes * trimpPerMinute(rpe/10)
}

// TrainingLoadOn computes load over the local calendar dates ending on day.
// days contains only scored dates, with absent dates contributing zero load.
// firstSessionDay is empty if the account has no completed session.
func TrainingLoadOn(day string, days map[string]TrainingLoadDay, firstSessionDay string) model.TrainingLoad {
	var cardioAcute, cardioTotal, muscularAcute, muscularTotal float64
	var cardioDays, muscularDays int
	date, err := time.Parse(time.DateOnly, day)
	if err == nil {
		for offset := 0; offset < TrainingLoadWindowDays; offset++ {
			key := date.AddDate(0, 0, -offset).Format(time.DateOnly)
			d := days[key]
			cardio := d.CardioTRIMP + d.RatedTRIMP
			cardioTotal += cardio
			muscularTotal += d.MuscularTRIMP
			if offset < trainingLoadAcuteDays {
				cardioAcute += cardio
				muscularAcute += d.MuscularTRIMP
			}
			if d.HasHR || d.HasRating {
				cardioDays++
			}
			if firstSessionDay != "" && key >= firstSessionDay {
				muscularDays++
			}
		}
	}
	return model.TrainingLoad{
		Cardio:   trainingLoadPart(cardioAcute, cardioTotal, cardioDays),
		Muscular: trainingLoadPart(muscularAcute, muscularTotal, muscularDays),
	}
}

func trainingLoadPart(acute, total float64, days int) model.LoadPart {
	acute = math.Round(acute*10) / 10
	chronic := math.Round(total/trainingLoadChronicDivisor*10) / 10
	part := model.LoadPart{
		Acute7D:          acute,
		Chronic28DWeekly: chronic,
		DaysWithData:     days,
		WindowDays:       TrainingLoadWindowDays,
	}
	if days < trainingLoadMinDays {
		return part
	}
	low := math.Round(chronic*trainingLoadLow*10) / 10
	high := math.Round(chronic*trainingLoadHigh*10) / 10
	state := model.LoadStateOptimal
	switch {
	case acute < low:
		state = model.LoadStateBelow
	case acute > high:
		state = model.LoadStateAbove
	}
	part.OptimalLow, part.OptimalHigh, part.State = &low, &high, &state
	if chronic != 0 {
		ratio := math.Round(acute/chronic*100) / 100
		part.Ratio = &ratio
	}
	return part
}
