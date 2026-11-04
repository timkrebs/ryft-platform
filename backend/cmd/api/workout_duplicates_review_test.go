package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/store"
)

// Review round of PR 2 for the api: the visible-only chain, include_hidden and the ranking
// of `other`. Reads run as ryft_api, scoring as ryft_scoring (newDupBench).

func (b *dupBench) tz(user uuid.UUID, zone string) *time.Location {
	b.t.Helper()
	b.exec(`UPDATE users SET tz = $2 WHERE id = $1`, user, zone)
	loc, err := time.LoadLocation(zone)
	if err != nil {
		b.t.Fatal(err)
	}
	return loc
}

func (b *dupBench) mark(user, dup, primary uuid.UUID) {
	b.t.Helper()
	b.exec(`UPDATE workouts SET duplicate_of = $3 WHERE user_id = $1 AND id = $2`, user, dup, primary)
}

func (b *dupBench) rangeFor(token, from, to string) []map[string]any {
	b.t.Helper()
	var v []map[string]any
	b.get(token, "/v1/workouts?from="+from+"&to="+to, 200, &v)
	return v
}

// ---- 2. a duplicate is hidden only while its primary exists and is itself visible ----

// A run across local midnight (a Watch at 23:58, a Garmin at 00:01 the next day) for a
// Europe/Berlin user. Stale passes left A -> B (scored on day D) and B -> A (scored on D+1):
// the run is never invisible in the range read or in the day reads. Real passes in both
// orders keep at least one of the two listed, and the one listed names the other.
func TestVisibleChainAcrossMidnightIsNeverInvisible(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	loc := b.tz(user, "Europe/Berlin")
	d := scoredDay()
	d0 := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	next := d0.AddDate(0, 0, 1)
	dayD, dayN := d0.Format(time.DateOnly), next.Format(time.DateOnly)
	aStart := time.Date(d0.Year(), d0.Month(), d0.Day(), 23, 58, 0, 0, loc)
	bStart := time.Date(next.Year(), next.Month(), next.Day(), 0, 1, 0, 0, loc)
	a := b.workout(user, uuid.Nil, "running", watchSource, aStart, aStart.Add(time.Hour))
	c := b.workout(user, uuid.Nil, "running", garminSource, bStart, bStart.Add(time.Hour))

	b.mark(user, a, c)
	b.mark(user, c, a)
	rng := b.rangeFor(token, dayD, dayN)
	if len(rng) == 0 {
		t.Fatal("stale marks A->B and B->A hide the whole run from GET /v1/workouts")
	}
	if got := idsOf(rng); !slices.Equal(got, idsOfIDs(a, c)) {
		t.Errorf("a duplicate of a duplicate is hidden only while its primary is visible: listed %v, want both", got)
	}
	if len(b.dayView(token, d0).Workouts) == 0 || len(b.dayView(token, next).Workouts) == 0 {
		t.Error("each day lists the workout that starts on it while the marks cycle")
	}

	// A longer chain: A -> B -> C. B is a duplicate of a visible C and is hidden; A's primary
	// B is hidden, so A is listed.
	third := b.workout(user, uuid.Nil, "running", "Third Tracker", aStart.Add(time.Minute), aStart.Add(time.Hour))
	b.mark(user, a, c)
	b.mark(user, c, third)
	b.exec(`UPDATE workouts SET duplicate_of = NULL WHERE id = $1`, third)
	if got := idsOf(b.rangeFor(token, dayD, dayN)); !slices.Equal(got, idsOfIDs(a, third)) {
		t.Errorf("chain A->B->C: listed %v, want A and C", got)
	}

	// Real passes, both orders, repeated.
	b.exec(`UPDATE workouts SET duplicate_of = NULL WHERE user_id = $1`, user)
	for k, order := range [][]time.Time{{d0, next, d0}, {next, d0, next}} {
		for _, day := range order {
			b.compute(user, day)
			rng := b.rangeFor(token, dayD, dayN)
			if len(rng) == 0 {
				t.Fatalf("order %d, after scoring %s: the run is not listed at all", k, day.Format(time.DateOnly))
			}
			if len(rng) == 1 {
				if also, _ := alsoOf(rng[0]); len(also) == 0 {
					t.Errorf("order %d: the single listed workout names the other recording: %v", k, rng[0])
				}
			}
		}
	}
}

func idsOfIDs(ids ...uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	slices.Sort(out)
	return out
}

// The local-day clear of SetWorkoutDuplicates (run by scoring a day) covers the user's local
// day, not 24 hours from local midnight: on the 25-hour day (2026-10-25, Berlin) a stale
// mark on a workout at 23:30 local is cleared; on the 23-hour day (2026-03-29) a stale mark
// on a workout of the next local day (00:30 on the 30th, 22:30Z) is left alone.
func TestSetWorkoutDuplicatesClearsTheLocalDayAcrossDST(t *testing.T) {
	b := newDupBench(t)
	user, _ := b.user()
	loc := b.tz(user, "Europe/Berlin")
	gone := uuid.New() // a primary that does not exist: the mark is stale

	late := b.workout(user, uuid.Nil, "running", garminSource,
		time.Date(2026, 10, 25, 23, 30, 0, 0, loc), time.Date(2026, 10, 25, 23, 50, 0, 0, loc))
	early := b.workout(user, uuid.Nil, "running", garminSource,
		time.Date(2026, 10, 25, 0, 10, 0, 0, loc), time.Date(2026, 10, 25, 0, 40, 0, 0, loc))
	nextDay := b.workout(user, uuid.Nil, "running", garminSource,
		time.Date(2026, 10, 26, 0, 10, 0, 0, loc), time.Date(2026, 10, 26, 0, 40, 0, 0, loc))
	for _, id := range []uuid.UUID{late, early, nextDay} {
		b.mark(user, id, gone)
	}
	b.compute(user, time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	for name, id := range map[string]uuid.UUID{"00:10 on the 25-hour day": early, "23:30 on the 25-hour day": late} {
		if d := b.duplicateOf(user, id); d != nil {
			t.Errorf("%s: a stale mark survives the scoring of its own day", name)
		}
	}
	if d := b.duplicateOf(user, nextDay); d == nil {
		t.Error("00:10 on the 26th belongs to the next day: scoring the 25th leaves it alone")
	}

	spring := b.workout(user, uuid.Nil, "running", garminSource,
		time.Date(2026, 3, 29, 23, 30, 0, 0, loc), time.Date(2026, 3, 29, 23, 50, 0, 0, loc))
	after := b.workout(user, uuid.Nil, "running", garminSource,
		time.Date(2026, 3, 30, 0, 30, 0, 0, loc), time.Date(2026, 3, 30, 0, 50, 0, 0, loc)) // 22:30Z on the 29th
	b.mark(user, spring, gone)
	b.mark(user, after, gone)
	b.compute(user, time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC))
	if d := b.duplicateOf(user, spring); d != nil {
		t.Error("23:30 on the 23-hour day is cleared by scoring that day")
	}
	if d := b.duplicateOf(user, after); d == nil {
		t.Error("00:30 on the 30th (22:30Z on the 29th) is the next local day: the 23-hour day's pass leaves it alone")
	}
}

// ---- 3. include_hidden --------------------------------------------------------------

// GET /v1/workouts?include_hidden=true also returns the hidden duplicates, each with
// duplicate_of, and the primaries with also_recorded_by; the default and =false hide them;
// any other value is a 422; the day read never lists them whatever the parameter.
func TestWorkoutsIncludeHidden(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	d := day.Format(time.DateOnly)
	b.hr(user, watchSource, at(day, 601), at(day, 659), 150)
	watch := b.workout(user, uuid.Nil, "running", watchSource, at(day, 600), at(day, 660))
	garmin := b.workout(user, uuid.Nil, "running", garminSource, at(day, 602), at(day, 657))
	b.compute(user, day)
	base := "/v1/workouts?from=" + d + "&to=" + d

	for _, q := range []string{"", "&include_hidden=false"} {
		var v []map[string]any
		b.get(token, base+q, 200, &v)
		if got := idsOf(v); !slices.Equal(got, idsOfIDs(watch)) {
			t.Errorf("%q lists %v, want the primary only", q, got)
		}
		if _, present := v[0]["duplicate_of"]; present {
			t.Errorf("%q: the primary has no duplicate_of: %v", q, v[0])
		}
	}
	var all []map[string]any
	b.get(token, base+"&include_hidden=true", 200, &all)
	if got := idsOf(all); !slices.Equal(got, idsOfIDs(watch, garmin)) {
		t.Fatalf("include_hidden=true lists %v, want both", got)
	}
	for _, w := range all {
		switch idOf(w) {
		case watch.String():
			if also, _ := alsoOf(w); !slices.Equal(also, []string{garminSource}) {
				t.Errorf("the primary names the other source: %v", w)
			}
			if _, present := w["duplicate_of"]; present {
				t.Errorf("the primary has no duplicate_of: %v", w)
			}
		case garmin.String():
			if got, _ := w["duplicate_of"].(string); got != watch.String() {
				t.Errorf("the hidden duplicate carries duplicate_of = %v, want %s", w["duplicate_of"], watch)
			}
		}
	}
	for _, bad := range []string{"1", "yes", "True", "TRUE", "2", "null"} {
		b.get(token, base+"&include_hidden="+bad, http.StatusUnprocessableEntity, nil)
	}
	for _, q := range []string{"", "?include_hidden=true"} {
		var v dayView
		b.get(token, "/v1/days/"+d+q, 200, &v)
		if got := idsOf(v.Workouts); !slices.Equal(got, idsOfIDs(watch)) {
			t.Errorf("GET /v1/days/{day}%s lists %v, never the hidden one", q, got)
		}
	}
}

// ---- 6. `other` ranks below a concrete activity ----------------------------------------

// A Watch `strength` workout with the Train session next to a longer Garmin `other` with
// heart rate: the strength workout stays listed and carries the session and its load; the
// Garmin one is its duplicate. A Watch `running` against a Garmin `other` with heart rate:
// the running workout stays listed.
func TestOtherActivityNeverOutranksAConcreteOne(t *testing.T) {
	b := newDupBench(t)

	user, token := b.user()
	day := scoredDay()
	session := uuid.New()
	b.exec(`INSERT INTO sessions (user_id, id, started_at, ended_at, status) VALUES ($1, $2, $3, $4, 'completed')`,
		user, session, at(day, 600), at(day, 660))
	strength := b.workout(user, uuid.Nil, "strength", watchSource, at(day, 600), at(day, 660))
	garmin := b.workout(user, uuid.Nil, "other", garminSource, at(day, 598), at(day, 662))
	b.hr(user, garminSource, at(day, 599), at(day, 661), 120)
	b.compute(user, day)
	b.compute(user, day)
	b.wantListed("strength + other", token, day, strength)
	w := b.dayView(token, day).Workouts[0]
	if got, _ := w["session_id"].(string); got != session.String() || w["muscular_load"] == nil {
		t.Errorf("the strength workout carries the session and its load: %v", w)
	}
	if also, _ := alsoOf(w); !slices.Equal(also, []string{garminSource}) {
		t.Errorf("also_recorded_by = %v, want [%s]", also, garminSource)
	}
	if d := b.duplicateOf(user, garmin); d == nil || *d != strength {
		t.Errorf("the Garmin `other` is the duplicate: %v", d)
	}

	user2, token2 := b.user()
	run := b.workout(user2, uuid.Nil, "running", watchSource, at(day, 600), at(day, 650))
	other := b.workout(user2, uuid.Nil, "other", garminSource, at(day, 598), at(day, 662))
	b.hr(user2, garminSource, at(day, 599), at(day, 661), 150)
	b.compute(user2, day)
	b.wantListed("running + other", token2, day, run)
	if d := b.duplicateOf(user2, other); d == nil || *d != run {
		t.Errorf("the Garmin `other` is the duplicate of the running workout: %v", d)
	}
}

// GET /v1/workouts/{id} of a primary carries also_recorded_by (read as ryft_api, with the
// duplicates searched within two days of the primary's start): a cross-midnight duplicate
// and one that starts 1.9 days into a long activity are named; a hidden duplicate's own
// id answers without it.
func TestWorkoutDetailCarriesAlsoRecordedBy(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	long := b.workout(user, uuid.Nil, "hiking", watchSource, at(day, 600), at(day, 600+60*60)) // 60 h
	near := b.workout(user, uuid.Nil, "hiking", garminSource, at(day, 600+60), at(day, 600+120))
	far := b.workout(user, uuid.Nil, "hiking", "Another Tracker", at(day, 600+1.9*24*60), at(day, 600+1.9*24*60+60))
	b.mark(user, near, long)
	b.mark(user, far, long)
	// 2.5 days after the primary's start: outside the two-day bound of the detail read, so
	// not named (a read without the bound would name it).
	beyond := b.workout(user, uuid.Nil, "hiking", "Beyond Tracker", at(day, 600+2.5*24*60), at(day, 600+2.5*24*60+60))
	b.mark(user, beyond, long)

	var detail map[string]any
	b.get(token, "/v1/workouts/"+long.String(), 200, &detail)
	if also, _ := alsoOf(detail); !slices.Equal(also, []string{"Another Tracker", garminSource}) {
		t.Errorf("the primary's detail names both: %v", detail["also_recorded_by"])
	}
	var hidden map[string]any
	b.get(token, "/v1/workouts/"+far.String(), 200, &hidden)
	if _, present := alsoOf(hidden); present {
		t.Errorf("the hidden duplicate is served as stored: %v", hidden)
	}
	b.exec(`DELETE FROM workouts WHERE id = ANY($1)`, []uuid.UUID{near, far, beyond})
	var lone map[string]any
	b.get(token, "/v1/workouts/"+long.String(), 200, &lone)
	if _, present := alsoOf(lone); present {
		t.Errorf("no duplicates, no also_recorded_by: %v", lone)
	}
}

// storeList is ListWorkoutsBetween as ryft_api, as the JSON maps the api would write.
func (b *dupBench) storeList(user uuid.UUID, day time.Time, includeHidden bool) []map[string]any {
	b.t.Helper()
	st := store.New(dupRolePool(b.t, b.admin, "ryft_api"))
	ws, err := st.ListWorkoutsBetween(context.Background(), user, day, day.AddDate(0, 0, 1), includeHidden)
	if err != nil {
		b.t.Fatal(err)
	}
	raw, _ := json.Marshal(ws)
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		b.t.Fatal(err)
	}
	return out
}

func find(ws []map[string]any, id uuid.UUID) map[string]any {
	for _, w := range ws {
		if idOf(w) == id.String() {
			return w
		}
	}
	return nil
}

// also_recorded_by names a source only for a duplicate that is itself hidden (the same
// predicate as the visibility): a mark on a primary that is gone, or on a primary that is a
// duplicate itself, names nobody. Read three ways: GET /v1/workouts, the store list and
// the detail of the primary.
func TestAlsoRecordedByNamesOnlyHiddenDuplicates(t *testing.T) {
	b := newDupBench(t)
	day := scoredDay()
	api := store.New(dupRolePool(t, b.admin, "ryft_api"))

	// Case 1: B (Garmin) is hidden behind A; C (Oura) points at D, which does not exist.
	user, token := b.user()
	a := b.workout(user, uuid.Nil, "running", "Watch", at(day, 600), at(day, 660))
	bb := b.workout(user, uuid.Nil, "running", "Garmin", at(day, 601), at(day, 659))
	c := b.workout(user, uuid.Nil, "running", "Oura", at(day, 602), at(day, 658))
	b.mark(user, bb, a)
	b.mark(user, c, uuid.New())
	d := day.Format(time.DateOnly)
	lists := map[string][]map[string]any{
		"GET /v1/workouts": b.rangeFor(token, d, d),
		"store list":       b.storeList(user, day, false),
	}
	for name, ws := range lists {
		if got := idsOf(ws); !slices.Equal(got, idsOfIDs(a, c)) {
			t.Errorf("case 1, %s: listed %v, want A and C", name, got)
			continue
		}
		if also, _ := alsoOf(find(ws, a)); !slices.Equal(also, []string{"Garmin"}) {
			t.Errorf("case 1, %s: A names %v, want [Garmin] and not Oura", name, also)
		}
		if _, present := alsoOf(find(ws, c)); present {
			t.Errorf("case 1, %s: C (primary gone) names nobody: %v", name, find(ws, c))
		}
	}
	row, err := api.Workout(context.Background(), user, a)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmtAlso(row.AlsoRecordedBy); got != "Garmin" {
		t.Errorf("case 1, Workout(A): also_recorded_by %q, want Garmin", got)
	}

	// Case 2: B -> A, and A is itself a duplicate of the live E. Both are listed, A names
	// nobody (B is not hidden), and include_hidden changes nothing.
	user2, token2 := b.user()
	a2 := b.workout(user2, uuid.Nil, "running", "Watch", at(day, 600), at(day, 660))
	b2 := b.workout(user2, uuid.Nil, "running", "Garmin", at(day, 601), at(day, 659))
	e := b.workout(user2, uuid.Nil, "running", "Polar", at(day, 599), at(day, 661))
	b.mark(user2, b2, a2)
	b.mark(user2, a2, e)
	var withHidden []map[string]any
	b.get(token2, "/v1/workouts?from="+d+"&to="+d+"&include_hidden=true", 200, &withHidden)
	// A is hidden behind the live E (its primary is visible); B is listed because its primary
	// A is itself hidden; E names only the duplicate that is hidden (A, "Watch"), never B.
	for name, ws := range map[string][]map[string]any{
		"GET /v1/workouts": b.rangeFor(token2, d, d),
		"store list":       b.storeList(user2, day, false),
	} {
		if got := idsOf(ws); !slices.Equal(got, idsOfIDs(b2, e)) {
			t.Errorf("case 2, %s: listed %v, want B and E (A is hidden behind E)", name, got)
			continue
		}
		if also, _ := alsoOf(find(ws, e)); !slices.Equal(also, []string{"Watch"}) {
			t.Errorf("case 2, %s: E names %v, want [Watch] and not Garmin", name, also)
		}
		if _, present := alsoOf(find(ws, b2)); present {
			t.Errorf("case 2, %s: B (visible, no duplicates of its own) names nobody", name)
		}
	}
	for name, ws := range map[string][]map[string]any{
		"GET /v1/workouts?include_hidden=true": withHidden,
		"store list, include hidden":           b.storeList(user2, day, true),
	} {
		if got := idsOf(ws); !slices.Equal(got, idsOfIDs(a2, b2, e)) {
			t.Errorf("case 2, %s: listed %v, want A, B and E", name, got)
			continue
		}
		if x := find(ws, a2); x == nil || x["duplicate_of"] != e.String() {
			t.Errorf("case 2, %s: A carries duplicate_of = E: %v", name, x)
		}
		if also, _ := alsoOf(find(ws, a2)); slices.Contains(also, "Garmin") {
			t.Errorf("case 2, %s: A names Garmin though B is not hidden: %v", name, also)
		}
	}
	row, err = api.Workout(context.Background(), user2, a2)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmtAlso(row.AlsoRecordedBy); got != "" {
		t.Errorf("case 2, Workout(A): also_recorded_by %q, want none", got)
	}
}

// fmtAlso is a Workout's also_recorded_by as one comma-joined string, whatever its Go type.
func fmtAlso(v any) string {
	raw, _ := json.Marshal(v)
	var names []string
	_ = json.Unmarshal(raw, &names)
	return strings.Join(names, ",")
}
