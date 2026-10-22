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
	inputs := map[string][]string{
		"HRV": {"hrv_ms"}, "RHR": {"rhr"}, "RespRate": {"resp_rate"}, "Strain": {"strain"},
		"SleepNeedMin": {"sleep_need_min"}, "SleepActualMin": {"sleep_actual_min"},
		"SleepStart": {"sleep_start"}, "SleepEnd": {"sleep_end"},
		"NapMin": {"nap_min"}, // the next day's need subtracts this day's naps
		// calibrating_reason's window: whether the day holds any data. The overnight
		// signals, strain and sleep are compared by value already; steps and active
		// energy by presence only (the steps a day gains with every sync change no
		// later day). ScoreHistory computes it from exactly these columns.
		"HasData": {"hrv_ms", "rhr", "resp_rate", "strain", "sleep_actual_min", "steps IS NULL", "active_kcal IS NULL"},
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
	read := map[string]bool{}
	for field, columns := range inputs {
		for _, column := range columns {
			read[column] = true
			if !slices.Contains(historyInputs, column) {
				t.Errorf("History.%s is an input, but historyInputs has no %s", field, column)
			}
		}
	}
	if len(historyInputs) != len(read) {
		t.Errorf("historyInputs %v has columns no History field reads", historyInputs)
	}
	// The old row's CTE selects each column once, presence entries included.
	if got := inputColumns(historyInputs); !slices.Equal(got, []string{"hrv_ms", "rhr", "resp_rate", "strain",
		"sleep_need_min", "sleep_actual_min", "sleep_start", "sleep_end", "nap_min", "steps", "active_kcal"}) {
		t.Errorf("inputColumns: %v", got)
	}
}
