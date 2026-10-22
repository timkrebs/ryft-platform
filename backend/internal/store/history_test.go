package store

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
)

// ScoreHistory's HasData is what dataColumns say, in the database: a stored
// row with none of them set has no data; setting any one of them, alone, gives
// it data; the columns left out (SpO2, naps, time in bed, a session's minutes)
// alone do not. Needs RYFT_TEST_POSTGRES_URL.
func TestScoreHistoryHasData(t *testing.T) {
	st, pool := testStore(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := st.EnsureUser(ctx, Identity{ID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO daily_scores (user_id, day) VALUES ($1, '2026-10-01')`, id); err != nil {
		t.Fatal(err)
	}
	dataIn := func(set string) bool {
		t.Helper()
		if set != "" {
			if _, err := pool.Exec(ctx, `UPDATE daily_scores SET `+set+` = 1 WHERE user_id = $1`, id); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := pool.Exec(ctx, `UPDATE daily_scores SET `+set+` = NULL WHERE user_id = $1`, id); err != nil {
					t.Fatal(err)
				}
			}()
		}
		h, err := st.ScoreHistory(ctx, id, "2026-10-02", 30)
		if err != nil || len(h) != 1 {
			t.Fatalf("history %v %v", h, err)
		}
		return h[0].HasData
	}
	if dataIn("") {
		t.Fatal("a stored row without data has data")
	}
	for _, c := range dataColumns {
		if !dataIn(c) {
			t.Errorf("a row with only %s set has no data", c)
		}
	}
	for _, c := range []string{"spo2", "nap_min", "in_bed_min", "strength_min"} {
		if dataIn(c) {
			t.Errorf("a row with only %s set has data; it is not in dataColumns", c)
		}
	}
}

// Every History field is a history input (UpsertDailyScore compares its
// column, so a change re-scores the days after it) or left out for a stated
// reason. A new field fails here until it is put in one of the two.
func TestHistoryInputsCoverHistory(t *testing.T) {
	inputs := map[string][]string{
		"HRV": {"hrv_ms"}, "RHR": {"rhr"}, "RespRate": {"resp_rate"}, "Strain": {"strain"},
		"SleepNeedMin": {"sleep_need_min"}, "SleepActualMin": {"sleep_actual_min"},
		"SleepStart": {"sleep_start"}, "SleepEnd": {"sleep_end"},
		"NapMin": {"nap_min"}, // the next day's need subtracts this day's naps
		// calibrating_reason's window: whether one of dataColumns is set
		// (ScoreHistory's SQL is built from them). Each must be compared, by value
		// or by presence ("col IS NULL": the steps a day gains with every sync
		// change no later day), or a day gaining only that would move W without a
		// re-score of the days after it.
		"HasData": nil,
	}
	for _, c := range dataColumns {
		switch {
		case slices.Contains(historyInputs, c):
			inputs["HasData"] = append(inputs["HasData"], c)
		case slices.Contains(historyInputs, c+" IS NULL"):
			inputs["HasData"] = append(inputs["HasData"], c+" IS NULL")
		default:
			t.Errorf("History.HasData reads %s, but historyInputs compares it neither by value nor by presence", c)
		}
	}
	// That ScoreHistory's SQL uses dataColumns: TestScoreHistoryHasData.
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
