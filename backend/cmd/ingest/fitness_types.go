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
