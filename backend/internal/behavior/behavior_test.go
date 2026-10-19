package behavior

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/model"
)

func TestBuiltinIDIsPinned(t *testing.T) {
	// The app and API.md use these ids; they must never change.
	for slug, want := range map[string]string{"alcohol": "bad67883-7fb1-5f98-9761-feba0a813536",
		"last_caffeine": "89a9dd86-4767-5eed-9718-24370f52078b", "sauna": "ce48b6cf-43e3-5c6b-baf9-0a13e5d767d9"} {
		if got := BuiltinID(slug).String(); got != want {
			t.Errorf("%s: %s, want %s", slug, got, want)
		}
	}
	if Slug("Late screen Meeting!") != "late_screen_meeting" {
		t.Fatalf("slug %q", Slug("Late screen Meeting!"))
	}
}

func TestEditable(t *testing.T) {
	berlin, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 10, 19, 7, 0, 0, 0, berlin)
	for day, want := range map[string]bool{"2026-10-19": true, "2026-10-18": true, "2026-10-05": true,
		"2026-10-04": false, "2026-10-20": false, "yesterday": false} {
		if got := Editable(day, now); got != want {
			t.Errorf("%s: %v", day, got)
		}
	}
}

func ip(v int) *int       { return &v }
func sp(v string) *string { return &v }

func TestCheckEntry(t *testing.T) {
	for _, c := range []struct {
		kind string
		e    model.JournalEntry
		ok   bool
	}{
		{model.BehaviorYesNo, model.JournalEntry{ValueBool: true}, true},
		{model.BehaviorYesNo, model.JournalEntry{ValueBool: true, ValueNum: ip(1)}, false},
		{model.BehaviorCount, model.JournalEntry{ValueBool: true, ValueNum: ip(2)}, true},
		{model.BehaviorCount, model.JournalEntry{ValueBool: true}, false},
		{model.BehaviorCount, model.JournalEntry{ValueBool: true, ValueNum: ip(0)}, false},
		{model.BehaviorCount, model.JournalEntry{ValueBool: false, ValueNum: ip(0)}, true},
		{model.BehaviorCount, model.JournalEntry{ValueBool: false}, true},
		{model.BehaviorCount, model.JournalEntry{ValueBool: false, ValueNum: ip(3)}, false},
		{model.BehaviorTime, model.JournalEntry{ValueBool: true, ValueTime: sp("16:30")}, true},
		{model.BehaviorTime, model.JournalEntry{ValueBool: true, ValueTime: sp("00:00")}, true},
		{model.BehaviorTime, model.JournalEntry{ValueBool: true, ValueTime: sp("4:30")}, false},
		{model.BehaviorTime, model.JournalEntry{ValueBool: true}, false},
		{model.BehaviorTime, model.JournalEntry{ValueBool: false, ValueTime: sp("16:30")}, false},
		{model.BehaviorTime, model.JournalEntry{ValueBool: false}, true},
		{"mood", model.JournalEntry{ValueBool: true}, false},
	} {
		if err := CheckEntry(c.kind, c.e); (err == nil) != c.ok {
			t.Errorf("%s %+v: %v", c.kind, c.e, err)
		}
	}
}

func TestEffect(t *testing.T) {
	yes := []float64{48, 52, 50, 55, 45, 51, 49, 50}    // mean 50
	no := []float64{68, 72, 70, 75, 65, 71, 69, 70, 70} // mean 70
	e := Effect(42, yes, no, false)
	if e == nil || e.Delta != -20 || !e.Significant || e.CIHi >= 0 || e.CILo > -20 || e.CIHi < -20 || e.NYes != 8 || e.NNo != 9 {
		t.Fatalf("a clear effect: %+v", e)
	}
	if again := Effect(42, yes, no, false); *again != *e {
		t.Fatal("the same seed gives the same interval")
	}
	if rel := Effect(42, yes, no, true); rel.Delta != -28.6 || !rel.Significant {
		t.Fatalf("relative: (50 − 70) / 70 = −28.6 %%: %+v", rel)
	}
	same := []float64{60, 64, 58, 62, 61, 59, 63, 60}
	if e := Effect(7, same, same, false); e.Delta != 0 || e.Significant || e.CILo > 0 || e.CIHi < 0 {
		t.Fatalf("no effect: %+v", e)
	}
	if Effect(1, yes[:4], no, false) != nil || Effect(1, yes, no[:4], false) != nil {
		t.Fatal("fewer than 5 in a group: no effect")
	}
}

// answers for days 2026-07-21 … ending 2026-10-18; mornings on the day after.
func scenario(n int, yes func(k int) bool, morning func(k int, yes bool) Morning) ([]Answer, map[string]Morning) {
	start := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(n - 1))
	var answers []Answer
	mornings := map[string]Morning{}
	for k := 0; k < n; k++ {
		d := start.AddDate(0, 0, k)
		y := yes(k)
		answers = append(answers, Answer{Day: d.Format("2006-01-02"), Yes: y})
		mornings[d.AddDate(0, 0, 1).Format("2006-01-02")] = morning(k, y)
	}
	return answers, mornings
}

func TestImpactsPairTheMorningAfter(t *testing.T) {
	user := uuid.New()
	alcohol := model.Behavior{ID: BuiltinID("alcohol"), Slug: "alcohol", Name: "Alcohol", Kind: model.BehaviorYesNo}
	answers, mornings := scenario(40, func(k int) bool { return k%3 == 0 }, func(k int, yes bool) Morning {
		r, perf, hrv := 72+k%5, 90, 55.0
		if yes {
			r, perf, hrv = 55+k%5, 84, 44.0
		}
		return Morning{Recovery: &r, SleepPerf: &perf, HRV: &hrv}
	})
	got := Impacts(user, []model.Behavior{alcohol}, map[uuid.UUID][]Answer{alcohol.ID: answers}, mornings)[0]
	if got.Status != model.ImpactUnlocked || got.NYes != 14 || got.NNo != 26 || got.Recovery.Delta > -15 || !got.Recovery.Significant ||
		got.HRVPct.Delta != -20 || got.SleepPerformance.Delta != -6 {
		t.Fatalf("impact %+v rec %+v hrv %+v sleep %+v", got, got.Recovery, got.HRVPct, got.SleepPerformance)
	}
	// Paired with the entry's own day instead, a "yes" day's morning would be
	// the one after a "no" day (high) and the day after a "yes" a "no" (low):
	// the delta would come out positive. Negative proves the morning after.
}

func TestImpactsLockedAndOrder(t *testing.T) {
	user := uuid.New()
	mk := func(slug string) model.Behavior {
		return model.Behavior{ID: BuiltinID(slug), Slug: slug, Name: slug, Kind: model.BehaviorYesNo}
	}
	alcohol, sauna, late, rare := mk("alcohol"), mk("sauna"), mk("late_meal"), mk("ice_bath")
	strong, mornings := scenario(30, func(k int) bool { return k%2 == 0 }, func(k int, yes bool) Morning {
		r := 70 + k%3
		if yes {
			r = 50 + k%3
		}
		return Morning{Recovery: &r}
	})
	weak := make([]Answer, len(strong))
	for k, a := range strong {
		weak[k] = Answer{Day: a.Day, Yes: k%3 == 0} // unrelated to recovery: small effect
	}
	few := []Answer{strong[0], strong[1], strong[2]}
	got := Impacts(user, []model.Behavior{rare, sauna, late, alcohol}, map[uuid.UUID][]Answer{
		alcohol.ID: strong, late.ID: weak, rare.ID: few}, mornings)
	order := ""
	for _, g := range got {
		order += g.Slug + "/" + g.Status + " "
	}
	if order != "alcohol/unlocked late_meal/unlocked ice_bath/locked sauna/locked " {
		t.Fatalf("order: %s", order)
	}
	if got[2].Needed != (model.ImpactNeeded{Yes: 3, No: 4}) || got[3].Needed != (model.ImpactNeeded{Yes: 5, No: 5}) || got[3].Recovery != nil {
		t.Fatalf("locked: %+v %+v", got[2], got[3])
	}
	if got[1].HRVPct != nil {
		t.Fatal("no HRV data: no HRV effect")
	}
}

// One request at the upper end: 75 behaviors (25 built-ins, 50 custom) over 90
// days, three metrics each. The api computes this on request (D76).
func BenchmarkImpacts75x90(b *testing.B) {
	user := uuid.New()
	var behaviors []model.Behavior
	answers := map[uuid.UUID][]Answer{}
	_, mornings := scenario(90, func(int) bool { return false }, func(k int, _ bool) Morning {
		r, perf, hrv := 50+(k*7)%40, 70+(k*3)%30, 40+float64((k*5)%25)
		return Morning{Recovery: &r, SleepPerf: &perf, HRV: &hrv}
	})
	for i := 0; i < 75; i++ {
		bh := model.Behavior{ID: BuiltinID(fmt.Sprint("b", i)), Slug: fmt.Sprint("b", i), Kind: model.BehaviorYesNo}
		behaviors = append(behaviors, bh)
		a, _ := scenario(90, func(k int) bool { return (k+i)%3 == 0 }, func(int, bool) Morning { return Morning{} })
		answers[bh.ID] = a
	}
	b.ResetTimer()
	for range b.N {
		Impacts(user, behaviors, answers, mornings)
	}
}
