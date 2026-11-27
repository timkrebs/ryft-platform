package main

import (
	"context"
	"time"

	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
)

func restingInputs(u model.User) scoring.RestingInputs {
	// Profile weight and sex will be wired when PR 1 (#266) merges.
	return scoring.RestingInputs{BirthYear: u.BirthYear}
}

// energyDayProgress uses calendar midnights in now's location for DST days. A
// stored day after local today (the user travelled west) has not started here.
func energyDayProgress(day string, now time.Time) (partial bool, elapsed float64) {
	today := now.Format(dayLayout)
	if day > today {
		return true, 0
	}
	if day != today {
		return false, 1
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	nextMidnight := midnight.AddDate(0, 0, 1)
	return true, float64(now.Sub(midnight)) / float64(nextMidnight.Sub(midnight))
}

func (h *handler) withDayEnergy(ctx context.Context, user model.User, from, to string, now time.Time, rows []model.DailyScore) error {
	if len(rows) == 0 {
		return nil
	}
	all, err := h.st.Flags(ctx)
	if err != nil {
		return err
	}
	diaryOn := flags.Enabled(all, model.FlagNutrition, user.ID) || flags.Enabled(all, model.FlagNutritionDiary, user.ID)
	var byDay map[string]model.NutritionDaily
	if diaryOn {
		days, err := h.st.NutritionEnergyTotals(ctx, user.ID, from, to)
		if err != nil {
			// The day routes must not fail on the diary: intake reads as unknown.
			// A fixed line; the error could carry private database values.
			h.log.WarnContext(ctx, "net energy: diary totals unavailable")
			diaryOn = false
		}
		byDay = make(map[string]model.NutritionDaily, len(days))
		for _, d := range days {
			byDay[d.LocalDate] = d
		}
	}
	inputs := restingInputs(user)
	for i := range rows {
		row := &rows[i]
		partial, elapsed := energyDayProgress(row.Day, now)
		diary := byDay[row.Day]
		total, ok := diary.Nutrients["energy_kcal"]
		in := scoring.EnergyInputs{
			IntakeKcal: total.KnownSubtotal, Meals: diary.Meals, DiaryOn: diaryOn, IntakeComplete: (ok && total.Complete) || diary.Items == 0,
			ActiveKcal: row.ActiveKcal, MeasuredResting: row.RestingKcal, PartialDay: partial,
		}
		if in.MeasuredResting == nil {
			day, err := time.Parse(dayLayout, row.Day)
			if err != nil {
				return err
			}
			in.EstimatedResting = scoring.RestingEstimateKcal(inputs, day, elapsed)
		}
		row.Energy = scoring.DayEnergyOf(in)
	}
	return nil
}
