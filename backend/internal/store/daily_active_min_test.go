package store

import (
	"context"
	"testing"

	"github.com/timkrebs/ryft/internal/model"
)

func TestDailyActiveMinRoundTrip(t *testing.T) {
	st, _ := testStore(t)
	ctx := context.Background()
	user := newBareAccount(t, st)
	const day = "2026-11-27"
	row := ScoreRow{DailyScore: model.DailyScore{Day: day, State: model.StateCalibrating}}
	positive, zero := 42, 0
	for i, want := range []*int{nil, &positive, &zero, nil} {
		row.ActiveMin = want
		write, err := st.UpsertDailyScore(ctx, user, row)
		if err != nil {
			t.Fatal(err)
		}
		if write.InputsChanged != (i == 0) {
			t.Errorf("write %d: InputsChanged = %v; only the first write should change history", i, write.InputsChanged)
		}
		rows, err := st.DailyScores(ctx, user, day, day)
		if err != nil || len(rows) != 1 {
			t.Fatalf("read daily score: %d rows, %v", len(rows), err)
		}
		got := rows[0].ActiveMin
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("write %d: active_min did not round-trip", i)
		}
	}
}
