package scoring

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Integration tests (RYFT_TEST_POSTGRES_URL) for the re-score after a change
// in the history (ComputeDays) and the 15-minute pass's safety net
// (RescoreBehind). seedNights writes 2026-09-01 .. 2026-10-10.

// rowsJSON is every stored row in [from, to] as JSON, computed_at left out:
// two calls that agree here wrote the same scores.
func rowsJSON(t *testing.T, st *store.Store, user uuid.UUID, from, to string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for day, r := range scoresByDay(t, st, user, from, to) {
		r.ComputedAt = time.Time{}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out[day] = string(b)
	}
	return out
}

// sameRows fails for every day whose row differs between got and want.
func sameRows(t *testing.T, what string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d rows, want %d", what, len(got), len(want))
	}
	for day, w := range want {
		if got[day] != w {
			t.Errorf("%s: %s differs from scoring in order:\n%s\n%s", what, day, got[day], w)
		}
	}
}

// inOrder re-scores [from, to] oldest first and returns the rows: what every
// re-score must end up with.
func inOrder(t *testing.T, st *store.Store, engine *Engine, user uuid.UUID, from, to string) map[string]string {
	t.Helper()
	if err := engine.ComputeRange(context.Background(), user, from, to); err != nil {
		t.Fatal(err)
	}
	return rowsJSON(t, st, user, from, to)
}

// lateHRV is a second overnight HRV sample for day (YYYY-MM-DD, UTC), as from
// a delayed sync: the night's mean moves.
func lateHRV(t *testing.T, st *store.Store, user uuid.UUID, day string) {
	t.Helper()
	d, _ := time.Parse(dayLayout, day)
	at := d.Add(4 * time.Hour)
	if _, err := st.UpsertSamples(context.Background(), user, []model.Sample{{Type: model.SampleHRVSDNN,
		Start: at, End: at, Value: 95, Unit: "ms", Source: "watch"}}); err != nil {
		t.Fatal(err)
	}
}

func TestAddDays(t *testing.T) {
	for _, c := range []struct {
		day  string
		n    int
		want string
	}{
		{"2026-10-09", 1, "2026-10-10"},
		{"2026-10-31", 1, "2026-11-01"},
		{"2026-03-29", -1, "2026-03-28"}, // a DST change in Europe: dates, not hours
		{"2026-12-31", 1, "2027-01-01"},
		{"2026-10-12", -(MaxRangeDays - 1), "2025-09-08"},
		{"2026-10-1", 1, ""},
		{"", 1, ""},
	} {
		if got := addDays(c.day, c.n); got != c.want {
			t.Errorf("addDays(%q, %d) = %q, want %q", c.day, c.n, got, c.want)
		}
	}
}

func rescored(reason string) float64 {
	return testutil.ToFloat64(metrics.DaysRescored.WithLabelValues(reason))
}

func scoredOK() float64 { return testutil.ToFloat64(metrics.DaysScored.WithLabelValues("ok")) }

// A re-score that changes nothing goes no further than the day asked for; a
// changed HRV re-scores exactly the 30 stored days whose baselines hold it,
// oldest first, and leaves the rows as scoring the range in order would.
func TestEngineRescoresAfterAChangeOnly(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	before := scoresByDay(t, st, user, "2026-09-01", "2026-10-10")

	// Unchanged: the duplicate of a sync, the 15-minute pass.
	cascade, scored := rescored("history"), scoredOK()
	if err := engine.ComputeDay(ctx, user, "2026-09-03"); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 0 {
		t.Fatalf("an unchanged day re-scored %v days after it", n)
	}
	if n := scoredOK() - scored; n != 1 {
		t.Fatalf("an unchanged day: %v days scored, want 1", n)
	}
	after := scoresByDay(t, st, user, "2026-09-04", "2026-09-04")["2026-09-04"]
	if !after.ComputedAt.Equal(before["2026-09-04"].ComputedAt) {
		t.Fatal("the day after an unchanged day was scored again")
	}

	// A late HRV sample for 09-03: 09-04 .. 10-03 hold it in their 30 days.
	lateHRV(t, st, user, "2026-09-03")
	cascade = rescored("history")
	if err := engine.ComputeDay(ctx, user, "2026-09-03"); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 30 {
		t.Fatalf("a changed HRV re-scored %v days after it, want 30", n)
	}
	got := scoresByDay(t, st, user, "2026-09-01", "2026-10-10")
	if got["2026-10-03"].HRVBaselineMS == nil || *got["2026-10-03"].HRVBaselineMS == *before["2026-10-03"].HRVBaselineMS {
		t.Fatal("10-03, the 30th day after, kept its old HRV baseline")
	}
	if !got["2026-10-04"].ComputedAt.Equal(before["2026-10-04"].ComputedAt) {
		t.Fatal("10-04 does not hold 09-03 in its 30 days and was scored again")
	}
	cascaded := rowsJSON(t, st, user, "2026-09-01", "2026-10-10")
	sameRows(t, "after the cascade", cascaded, inOrder(t, st, engine, user, "2026-09-01", "2026-10-10"))
}

// A sync whose days leave gaps: the stored days between them are scored in
// between, in order, and the cascade runs on after the last change.
func TestEngineRescoreFillsTheGapsBetweenSyncDays(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	lateHRV(t, st, user, "2026-09-03")
	lateHRV(t, st, user, "2026-09-20")
	cascade, scored := rescored("history"), scoredOK()
	if err := engine.ComputeDays(ctx, user, []string{"2026-09-20", "2026-09-03"}); err != nil {
		t.Fatal(err)
	}
	// 09-04 .. 09-19 between them, 09-21 .. 10-10 after (fewer than 30 stored).
	if n := rescored("history") - cascade; n != 16+20 {
		t.Fatalf("re-scored %v days, want 36", n)
	}
	if n := scoredOK() - scored; n != 2+36 {
		t.Fatalf("scored %v days, want each once (38)", n)
	}
	sameRows(t, "with gaps", rowsJSON(t, st, user, "2026-09-01", "2026-10-10"),
		inOrder(t, st, engine, user, "2026-09-01", "2026-10-10"))
}

// A backfill that arrives newest first: the older 100 days come after the
// newest 20 were stored. Each of the 20 is scored again once, not once per
// older day, and the rows end up as scoring all 120 in order would.
func TestEngineNewestFirstBackfillIsBounded(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	first := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var segs []model.SleepSegment
	var samples []model.Sample
	for k := 0; k < 120; k++ {
		d := first.AddDate(0, 0, k)
		segs = append(segs, model.SleepSegment{Start: d.Add(-time.Hour), End: d.Add(7 * time.Hour),
			Stage: model.SleepCore, Source: "watch"})
		samples = append(samples,
			model.Sample{Type: model.SampleRestingHR, Start: d.Add(9 * time.Hour), End: d.Add(9 * time.Hour),
				Value: 55 + float64(k%3), Unit: "count/min", Source: "watch"},
			model.Sample{Type: model.SampleHRVSDNN, Start: d.Add(3 * time.Hour), End: d.Add(3 * time.Hour),
				Value: 60 + float64(k%5), Unit: "ms", Source: "watch"})
	}
	if _, err := st.UpsertSleep(ctx, user, segs); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSamples(ctx, user, samples); err != nil {
		t.Fatal(err)
	}
	day := func(k int) string { return first.AddDate(0, 0, k).Format(dayLayout) }
	if err := engine.ComputeRange(ctx, user, day(100), day(119)); err != nil {
		t.Fatal(err)
	}
	if g := scoresByDay(t, st, user, day(119), day(119))[day(119)]; g.BaselineDays != 19 {
		t.Fatalf("the newest chunk alone: baseline_days=%d", g.BaselineDays)
	}
	var older []string
	for k := 99; k >= 0; k-- { // as a sync lists them: any order
		older = append(older, day(k))
	}
	cascade, scored := rescored("history"), scoredOK()
	if err := engine.ComputeDays(ctx, user, older); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 20 {
		t.Fatalf("re-scored %v stored days after the backfill, want 20", n)
	}
	if n := scoredOK() - scored; n != 120 {
		t.Fatalf("scored %v days for a 100-day backfill, want 120 (each once)", n)
	}
	got := rowsJSON(t, st, user, day(0), day(119))
	sameRows(t, "newest first", got, inOrder(t, st, engine, user, day(0), day(119)))
}

// D100: the same event twice. The second finds nothing changed, re-scores
// nothing beyond its days, and leaves every row as the first left it.
func TestEngineRescoreTwiceChangesNothing(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	if err := engine.ComputeDay(ctx, user, "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-09"); err != nil {
		t.Fatal(err)
	}
	once := rowsJSON(t, st, user, "2026-09-01", "2026-10-10")
	cascade := rescored("history")
	if err := engine.ComputeRange(ctx, user, "2026-09-01", "2026-10-09"); err != nil {
		t.Fatal(err)
	}
	if n := rescored("history") - cascade; n != 0 {
		t.Fatalf("the duplicate re-scored %v days", n)
	}
	sameRows(t, "twice", rowsJSON(t, st, user, "2026-09-01", "2026-10-10"), once)
}

// The safety net: days scored before their history whose re-score was lost
// (scored here without ComputeDays, as an attempt that failed half-way leaves
// them) are found, and only they; a source without HRV is not "behind"; a
// pass re-scores them, oldest first, and the next pass finds nothing.
func TestEngineRescoreBehind(t *testing.T) {
	st, engine, user := engineStore(t)
	ctx := context.Background()
	seedNights(t, st, user, true)
	order := []string{"2026-10-10", "2026-10-09"}
	for d := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); d.Before(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)); d = d.AddDate(0, 0, 1) {
		order = append(order, d.Format(dayLayout))
	}
	for _, d := range order {
		if _, err := engine.computeDay(ctx, user, d); err != nil {
			t.Fatal(err)
		}
	}
	behind, err := st.BehindHistoryDays(ctx, user, "2026-09-01", baselineDays, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(behind, []string{"2026-10-09", "2026-10-10"}) {
		t.Fatalf("behind their history: %v", behind)
	}
	if one, _ := st.BehindHistoryDays(ctx, user, "2026-09-01", baselineDays, 1); !slices.Equal(one, []string{"2026-10-09"}) {
		t.Fatalf("bounded, oldest first: %v", one)
	}
	if late, _ := st.BehindHistoryDays(ctx, user, "2026-10-10", baselineDays, 50); !slices.Equal(late, []string{"2026-10-10"}) {
		t.Fatalf("from 10-10 on: %v", late)
	}

	noHRV := uuid.New()
	if _, err := st.EnsureUser(ctx, store.Identity{ID: noHRV}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUser(ctx, noHRV, model.UserPatch{TZ: ptrTo("UTC")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.GrantConsent(ctx, noHRV, "test", nil); err != nil {
		t.Fatal(err)
	}
	seedNights(t, st, noHRV, false)
	if err := engine.ComputeRange(ctx, noHRV, "2026-09-01", "2026-10-10"); err != nil {
		t.Fatal(err)
	}
	if b, _ := st.BehindHistoryDays(ctx, noHRV, "2026-09-01", baselineDays, 50); len(b) != 0 {
		t.Fatalf("no HRV source is calibrating, not behind: %v", b)
	}

	// The pass looks back MaxRangeDays from the user's today, no further.
	if n, err := engine.RescoreBehind(ctx, user, "2027-12-31", 50); err != nil || n != 0 {
		t.Fatalf("400 days on: %d %v", n, err)
	}
	before := rescored("behind")
	n, err := engine.RescoreBehind(ctx, user, "2026-10-12", 50)
	if err != nil || n != 2 {
		t.Fatalf("rescore behind: %d %v", n, err)
	}
	if d := rescored("behind") - before; d != 2 {
		t.Fatalf("counted %v", d)
	}
	for _, d := range []string{"2026-10-09", "2026-10-10"} {
		if g := scoresByDay(t, st, user, d, d)[d]; g.BaselineDays != 30 || g.Calibrating {
			t.Fatalf("%s after the pass: baseline_days=%d calibrating=%v", d, g.BaselineDays, g.Calibrating)
		}
	}
	if n, err := engine.RescoreBehind(ctx, user, "2026-10-12", 50); err != nil || n != 0 {
		t.Fatalf("the next pass: %d %v", n, err)
	}
}
