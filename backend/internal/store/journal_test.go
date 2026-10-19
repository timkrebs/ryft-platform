package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/behavior"
	"github.com/timkrebs/ryft/internal/model"
)

func TestBehaviorCatalogAndChoice(t *testing.T) {
	st, _, a, b := trainStore(t) // seeds the built-ins
	ctx := context.Background()
	list, err := st.Behaviors(ctx, a)
	if err != nil || len(list) != 25 || list[0].Slug != "alcohol" || list[0].Category != "substances" {
		t.Fatalf("catalog: %d %v", len(list), err)
	}
	on := 0
	for _, x := range list {
		if x.Enabled {
			on++
		}
	}
	if on != 8 {
		t.Fatalf("default on: %d", on)
	}
	alcohol, sauna := behavior.BuiltinID("alcohol"), behavior.BuiltinID("sauna")
	if err := st.SetBehaviorsEnabled(ctx, a, map[uuid.UUID]bool{alcohol: false, sauna: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBehaviorsEnabled(ctx, a, map[uuid.UUID]bool{sauna: false, uuid.New(): true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown id: %v", err)
	}
	enabled := func(u uuid.UUID) map[string]bool {
		l, _ := st.Behaviors(ctx, u)
		m := map[string]bool{}
		for _, x := range l {
			m[x.Slug] = x.Enabled
		}
		return m
	}
	if e := enabled(a); e["alcohol"] || !e["sauna"] {
		t.Fatalf("a's choice: %v (and the failed call changed nothing)", e)
	}
	if e := enabled(b); !e["alcohol"] || e["sauna"] {
		t.Fatal("b keeps the defaults")
	}

	// Custom behaviors: create, update by id, someone else's id, limit, archive.
	mine := model.Behavior{ID: uuid.New(), NameEN: "Late screen meeting", NameDE: "Spätes Meeting", Kind: model.BehaviorYesNo, Category: "custom"}
	got, created, err := st.SaveCustomBehavior(ctx, a, mine)
	if err != nil || !created || got.Slug != "late_screen_meeting" || !got.Enabled || !got.IsCustom {
		t.Fatalf("create: %+v %v %v", got, created, err)
	}
	mine.NameEN = "Late meeting"
	if got, created, _ = st.SaveCustomBehavior(ctx, a, mine); created || got.NameEN != "Late meeting" {
		t.Fatalf("the same id updates: %+v", got)
	}
	if _, _, err := st.SaveCustomBehavior(ctx, b, mine); !errors.Is(err, ErrTaken) {
		t.Fatalf("a's id for b: %v", err)
	}
	if _, _, err := st.SaveCustomBehavior(ctx, b, model.Behavior{ID: alcohol, NameEN: "x", NameDE: "x", Kind: "yes_no", Category: "custom"}); !errors.Is(err, ErrTaken) {
		t.Fatalf("a built-in's id: %v", err)
	}
	if !enabled(a)["late_screen_meeting"] || enabled(b)["late_screen_meeting"] {
		t.Fatal("custom behaviors are the owner's")
	}
	if err := st.ArchiveBehavior(ctx, a, alcohol); !errors.Is(err, ErrForbidden) {
		t.Fatalf("archiving a built-in: %v", err)
	}
	if err := st.ArchiveBehavior(ctx, a, mine.ID); err != nil || st.ArchiveBehavior(ctx, a, mine.ID) != nil {
		t.Fatal("archive, twice")
	}
	if _, ok := enabled(a)["late_screen_meeting"]; ok {
		t.Fatal("archived: gone from the catalog")
	}
	for k := 0; k < behavior.MaxCustom; k++ {
		if _, _, err := st.SaveCustomBehavior(ctx, b, model.Behavior{ID: uuid.New(), NameEN: "Thing", NameDE: "Ding", Kind: "yes_no", Category: "custom"}); err != nil {
			t.Fatalf("custom %d: %v", k, err)
		}
	}
	if _, _, err := st.SaveCustomBehavior(ctx, b, model.Behavior{ID: uuid.New(), NameEN: "One more", NameDE: "Noch eins", Kind: "yes_no", Category: "custom"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("past the limit: %v", err)
	}
	l, _ := st.Behaviors(ctx, b)
	slugs := map[string]bool{}
	for _, x := range l {
		slugs[x.Slug] = true
	}
	if len(l) != 25+behavior.MaxCustom || len(slugs) != len(l) || !slugs["thing"] || !slugs["thing_2"] || !slugs["thing_50"] {
		t.Fatalf("50 custom behaviors named Thing get unique slugs: %d rows, %d slugs", len(l), len(slugs))
	}
}

func TestJournalDayRoundTrip(t *testing.T) {
	st, pool, a, _ := trainStore(t)
	ctx := context.Background()
	alcohol, drinks, caffeine, sauna := behavior.BuiltinID("alcohol"), behavior.BuiltinID("alcohol_drinks"),
		behavior.BuiltinID("last_caffeine"), behavior.BuiltinID("sauna")
	kinds := map[uuid.UUID]string{alcohol: model.BehaviorYesNo, drinks: model.BehaviorCount, caffeine: model.BehaviorTime, sauna: model.BehaviorYesNo}
	if at, entries, err := st.JournalDay(ctx, a, "2026-10-18"); err != nil || at != nil || len(entries) != 0 {
		t.Fatalf("never saved: %v %v %v", at, entries, err)
	}
	day := []model.JournalEntry{{BehaviorID: alcohol, ValueBool: true}, {BehaviorID: drinks, ValueBool: true, ValueNum: ip(2)},
		{BehaviorID: caffeine, ValueBool: true, ValueTime: sp("16:30")}, {BehaviorID: sauna, ValueBool: false}}
	if err := st.PutJournalDay(ctx, a, "2026-10-18", day, kinds); err != nil {
		t.Fatal(err)
	}
	at, entries, err := st.JournalDay(ctx, a, "2026-10-18")
	if err != nil || at == nil || len(entries) != 4 {
		t.Fatalf("saved: %v %v %v", at, entries, err)
	}
	byID := map[uuid.UUID]model.JournalEntry{}
	for _, e := range entries {
		byID[e.BehaviorID] = e
	}
	if *byID[drinks].ValueNum != 2 || *byID[caffeine].ValueTime != "16:30" || byID[sauna].ValueBool || byID[alcohol].ValueNum != nil {
		t.Fatalf("values: %+v", byID)
	}
	// Saving again replaces the day.
	if err := st.PutJournalDay(ctx, a, "2026-10-18", day[:1], kinds); err != nil {
		t.Fatal(err)
	}
	if _, entries, _ = st.JournalDay(ctx, a, "2026-10-18"); len(entries) != 1 {
		t.Fatalf("replaced: %d entries", len(entries))
	}
	if err := st.PutJournalDay(ctx, a, "2026-10-17", []model.JournalEntry{{BehaviorID: alcohol, ValueBool: false}}, kinds); err != nil {
		t.Fatal(err)
	}
	answers, err := st.JournalAnswers(ctx, a, "2026-10-01", "2026-10-31")
	if err != nil || len(answers[alcohol]) != 2 || answers[alcohol][0] != (behavior.Answer{Day: "2026-10-17", Yes: false}) ||
		!answers[alcohol][1].Yes || len(answers[sauna]) != 0 {
		t.Fatalf("answers %+v %v", answers, err)
	}
	// Account deletion erases the journal, custom behaviors included.
	if _, _, err := st.SaveCustomBehavior(ctx, a, model.Behavior{ID: uuid.New(), NameEN: "x", NameDE: "x", Kind: "yes_no", Category: "custom"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(ctx, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM journal_entries WHERE user_id=$1) + (SELECT count(*) FROM journal_days WHERE user_id=$1)
		+ (SELECT count(*) FROM behaviors WHERE user_id=$1) + (SELECT count(*) FROM user_behaviors WHERE user_id=$1)`, a).Scan(&n); err != nil || n != 0 {
		t.Fatalf("journal rows left: %d %v", n, err)
	}
}

func sp(v string) *string { return &v }
func ip(v int) *int       { return &v }
