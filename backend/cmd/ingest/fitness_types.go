package main

import (
	"slices"

	"github.com/timkrebs/ryft/internal/model"
)

func gatedType(typ string) bool {
	switch typ {
	case model.SampleBasalEnergy, model.SampleHeartRateRecovery, model.SampleBodyMass:
		return true
	default:
		return false
	}
}

// Per-sample plausibility bounds of the Fitness types (D312). basal_energy: one sample never holds more than a
// day's resting energy (5 000 kcal is above any adult's; a kJ value read as kcal on a day-long sample lands
// above it). heart_rate_recovery: a one-minute drop of more than 120 bpm is a sensor or unit error.
const (
	maxBasalEnergyKcal = 5000
	maxHeartRateRecBPM = 120
)

// implausible says whether a sample's value is outside its type's bound. Only the Fitness types have one here;
// body_mass keeps validate's 422 (the pilot's rule). An implausible sample is dropped and counted, not refused:
// the app does not retry a 422, so one bad slice would stall the whole HealthKit stream.
func implausible(s model.Sample) bool {
	switch s.Type {
	case model.SampleBasalEnergy:
		return s.Value < 0 || s.Value > maxBasalEnergyKcal
	case model.SampleHeartRateRecovery:
		return s.Value < 0 || s.Value > maxHeartRateRecBPM
	default:
		return false
	}
}

func keepGated(typ string, pilot, fitness bool) bool {
	return fitness || !gatedType(typ) || (pilot && typ == model.SampleBodyMass)
}

func hasSampleStream(req model.SyncRequest, matches func(string) bool) bool {
	return (req.Cursor != nil && matches(req.Cursor.Stream)) ||
		slices.ContainsFunc(req.Samples, func(s model.Sample) bool { return matches(s.Type) })
}

func dropGated(req *model.SyncRequest, pilot, fitness bool) int {
	before := len(req.Samples)
	req.Samples = slices.DeleteFunc(req.Samples, func(s model.Sample) bool {
		return !keepGated(s.Type, pilot, fitness)
	})
	if req.Cursor != nil && !keepGated(req.Cursor.Stream, pilot, fitness) {
		req.Cursor = nil
	}
	return before - len(req.Samples)
}
