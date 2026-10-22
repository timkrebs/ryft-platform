package store

import (
	"reflect"
	"slices"
	"testing"
)

// Every History field is a history input (UpsertDailyScore compares its
// column, so a change re-scores the days after it) or left out for a stated
// reason. A new field fails here until it is put in one of the two.
func TestHistoryInputsCoverHistory(t *testing.T) {
	inputs := map[string]string{
		"HRV": "hrv_ms", "RHR": "rhr", "RespRate": "resp_rate", "Strain": "strain",
		"SleepNeedMin": "sleep_need_min", "SleepActualMin": "sleep_actual_min",
		"SleepStart": "sleep_start", "SleepEnd": "sleep_end",
		"NapMin": "nap_min", // the next day's need subtracts this day's naps
	}
	leftOut := map[string]string{
		"Day":        "the key",
		"Recovery":   "follows from the baselines; the weekly sleep-baseline estimate reads it when it runs",
		"Efficiency": "the sleep planner reads it per request; no stored row is computed from it",
	}
	h := reflect.TypeOf(History{})
	for i := range h.NumField() {
		name := h.Field(i).Name
		_, in := inputs[name]
		_, out := leftOut[name]
		if in == out {
			t.Errorf("History.%s: a history input (historyInputs) or left out with a reason, exactly one", name)
		}
	}
	for field, column := range inputs {
		if !slices.Contains(historyInputs, column) {
			t.Errorf("History.%s is an input, but historyInputs has no %s", field, column)
		}
	}
	if len(historyInputs) != len(inputs) {
		t.Errorf("historyInputs %v has columns no History field reads", historyInputs)
	}
}
