package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/db"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/logx"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/testsupport"
	"github.com/timkrebs/ryft/internal/train"
)

// Workouts that two sources recorded (docs/API.md -> Duplicate workouts, "Different sources";
// design D-d), end to end: scoring marks the duplicate (workouts.duplicate_of), the api's
// day and range reads list the primary once with also_recorded_by. The writes run through
// a pool opened AS ryft_scoring (the engine) and the reads AS ryft_api, the deletion as
// ryft_ingest; the superuser pool only seeds rows and looks at them.

const (
	watchSource  = "Tim's Apple Watch"
	garminSource = "Garmin Connect"
)

type dupBench struct {
	t      *testing.T
	admin  *pgxpool.Pool
	engine *scoring.Engine
	ingest *store.Store
	srv    http.Handler
}

func dupRolePool(t *testing.T, admin *pgxpool.Pool, role string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pw := testsupport.RolePassword(role) // one password per role in every package: tests run at once on one server
	if err := testsupport.WithRoleLock(ctx, func() error { return db.SetRolePassword(ctx, admin, role, pw) }); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, pw)
	p, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func newDupBench(t *testing.T) *dupBench {
	t.Helper()
	admin := testPool(t)
	log := logx.NewWriter(io.Discard, "api", slog.LevelDebug)
	apiSt := store.New(dupRolePool(t, admin, "ryft_api"))
	eng := scoring.NewEngine(store.New(dupRolePool(t, admin, "ryft_scoring")), log)
	eng.SetConsentMode(config.ModeOff)
	b := &dupBench{t: t, admin: admin, engine: eng,
		ingest: store.New(dupRolePool(t, admin, "ryft_ingest"))}
	h := &handler{st: apiSt, bus: &benchBus{}, log: log, cache: newInsightCache(), engine: scoring.NewEngine(apiSt, log),
		beta: config.Beta{Gate: config.ModeOff, Consent: config.ModeOff}}
	api := http.NewServeMux()
	api.HandleFunc("GET /v1/days", h.days)
	api.HandleFunc("GET /v1/days/{day}", h.day)
	api.HandleFunc("GET /v1/workouts", h.workouts)
	api.HandleFunc("GET /v1/workouts/{id}", h.workout)
	api.HandleFunc("/v1/", httpx.NotFound)
	o := auth.Options{Log: log, Verifier: auth.NewAPIKey(benchKey, uuid.New()),
		Limit: func(*http.Request, auth.Principal) (string, bool, time.Duration) { return "default", true, 0 }}
	o.OnAuth = auth.LoadUser(ensureUser(apiSt))
	mux := http.NewServeMux()
	mux.Handle("/v1/", auth.Middleware(o, httpx.WithRoutes(api)))
	b.srv = httpx.Instrument(log, httpx.Recover(mux))
	return b
}

func (b *dupBench) exec(q string, args ...any) {
	b.t.Helper()
	if _, err := b.admin.Exec(context.Background(), q, args...); err != nil {
		b.t.Fatalf("%.70s: %v", q, err)
	}
}

// user is a new account (tz UTC) and the token that speaks for it.
func (b *dupBench) user() (uuid.UUID, string) {
	b.t.Helper()
	id := uuid.New()
	if _, err := store.New(b.admin).EnsureUser(context.Background(), store.Identity{ID: id}); err != nil {
		b.t.Fatal(err)
	}
	b.exec(`UPDATE users SET tz = 'UTC' WHERE id = $1`, id)
	return id, benchKey + ":" + id.String()
}

// at is hh:mm on day, minutes may be fractional.
func at(day time.Time, minutes float64) time.Time {
	return day.Add(time.Duration(minutes * float64(time.Minute)))
}

// scoredDay is the UTC midnight three days back: scored, not today.
func scoredDay() time.Time {
	return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -3)
}

// workout stores a workout; id may be uuid.Nil for a new one.
func (b *dupBench) workout(user uuid.UUID, id uuid.UUID, activity, source string, start, end time.Time) uuid.UUID {
	b.t.Helper()
	if id == uuid.Nil {
		id = uuid.New()
	}
	b.exec(`INSERT INTO workouts (id, user_id, activity, start_at, end_at, avg_hr, source) VALUES ($1, $2, $3, $4, $5, 150, $6)`,
		id, user, activity, start, end, source)
	return id
}

// hr stores one heart-rate sample a minute from start to end, of source.
func (b *dupBench) hr(user uuid.UUID, source string, start, end time.Time, bpm float64) {
	b.t.Helper()
	b.exec(`INSERT INTO samples (user_id, type, start_at, end_at, value, unit, source)
		SELECT $1, 'heart_rate', t, t, $5, 'bpm', $4 FROM generate_series($2::timestamptz, $3::timestamptz, interval '1 minute') t`,
		user, start, end, source, bpm)
}

func (b *dupBench) compute(user uuid.UUID, day time.Time) {
	b.t.Helper()
	if err := b.engine.ComputeDay(context.Background(), user, day.Format(time.DateOnly)); err != nil {
		b.t.Fatalf("score %s as ryft_scoring: %v", day.Format(time.DateOnly), err)
	}
}

func (b *dupBench) get(token, path string, status int, out any) {
	b.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	b.srv.ServeHTTP(rec, req)
	if rec.Code != status {
		b.t.Fatalf("GET %s: %d %s, want %d", path, rec.Code, rec.Body, status)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			b.t.Fatalf("GET %s: %v: %s", path, err, rec.Body)
		}
	}
}

// dayView is the part of GET /v1/days/{day} these tests read; workouts stay generic so a
// missing also_recorded_by is told from an empty one.
type dayView struct {
	Workouts []map[string]any `json:"workouts"`
	Score    struct {
		Strain  *float64       `json:"strain"`
		Details map[string]any `json:"details"`
	} `json:"score"`
}

func (b *dupBench) dayView(token string, day time.Time) dayView {
	b.t.Helper()
	var v dayView
	b.get(token, "/v1/days/"+day.Format(time.DateOnly), 200, &v)
	return v
}

func (b *dupBench) rangeView(token string, day time.Time) []map[string]any {
	b.t.Helper()
	var v []map[string]any
	d := day.Format(time.DateOnly)
	b.get(token, "/v1/workouts?from="+d+"&to="+d, 200, &v)
	return v
}

func idOf(w map[string]any) string { s, _ := w["id"].(string); return s }

func idsOf(ws []map[string]any) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, idOf(w))
	}
	slices.Sort(out)
	return out
}

// alsoOf is also_recorded_by of w; present tells a missing key from an empty list.
func alsoOf(w map[string]any) (names []string, present bool) {
	raw, present := w["also_recorded_by"]
	if !present {
		return nil, false
	}
	list, _ := raw.([]any)
	for _, x := range list {
		names = append(names, fmt.Sprint(x))
	}
	return names, true
}

func (b *dupBench) duplicateOf(user, id uuid.UUID) *uuid.UUID {
	b.t.Helper()
	var d *uuid.UUID
	if err := b.admin.QueryRow(context.Background(), `SELECT duplicate_of FROM workouts WHERE user_id = $1 AND id = $2`, user, id).Scan(&d); err != nil {
		b.t.Fatal(err)
	}
	return d
}

// wantListedOnce asserts the day view and the range view both list exactly want, in any order.
func (b *dupBench) wantListed(what, token string, day time.Time, want ...uuid.UUID) {
	b.t.Helper()
	ws := make([]string, 0, len(want))
	for _, id := range want {
		ws = append(ws, id.String())
	}
	slices.Sort(ws)
	if got := idsOf(b.dayView(token, day).Workouts); !slices.Equal(got, ws) {
		b.t.Errorf("%s: GET /v1/days/{day} lists %v, want %v", what, got, ws)
	}
	if got := idsOf(b.rangeView(token, day)); !slices.Equal(got, ws) {
		b.t.Errorf("%s: GET /v1/workouts lists %v, want %v", what, got, ws)
	}
}

// ---- the decision table -----------------------------------------------------------

// Which workout is the primary, and when two are one: different sources, same activity
// (either one `other` counts), overlap at least 80 % of the shorter; the primary is the one
// with 2+ heart-rate samples of its own source in its window, then the longer, then the lower
// id. The duplicate points at the primary, which lists it in also_recorded_by; nothing is
// deleted; the day lists the primary once in the day and the range reads.
func TestCrossSourcePrimaryAndOverlapTable(t *testing.T) {
	b := newDupBench(t)
	type run struct {
		from, to float64 // minutes after 10:00
		hr       bool    // 2+ heart-rate samples of its own source in the window
		source   string
		activity string
	}
	const (
		watch  = "watch"
		garmin = "garmin"
		both   = "both"
		one    = "one" // exactly one is listed, either
	)
	for _, c := range []struct {
		name    string
		w, g    run
		lowID   string // who gets the lower id: "", watch, garmin
		primary string // watch, garmin, one, both (both listed: no duplicate)
	}{
		{"own heart rate and longer", run{0, 60, true, watchSource, "running"}, run{2, 55, false, garminSource, "running"}, "", watch},
		{"own heart rate beats longer", run{0, 60, false, watchSource, "running"}, run{5, 55, true, garminSource, "running"}, "", garmin},
		{"both have heart rate: the longer", run{0, 60, true, watchSource, "running"}, run{2, 55, true, garminSource, "running"}, "", watch},
		{"neither: the longer", run{0, 50, false, watchSource, "running"}, run{0, 60, false, garminSource, "running"}, "", garmin},
		{"neither, equal: the lower id (garmin)", run{0, 60, false, watchSource, "running"}, run{0, 60, false, garminSource, "running"}, garmin, garmin},
		{"neither, equal: the lower id (watch)", run{0, 60, false, watchSource, "running"}, run{0, 60, false, garminSource, "running"}, watch, watch},
		{"overlap exactly 80 percent", run{0, 60, false, watchSource, "running"}, run{12, 72, false, garminSource, "running"}, "", one},
		{"overlap just under 80 percent", run{0, 60, false, watchSource, "running"}, run{12.5, 72.5, false, garminSource, "running"}, "", both},
		{"no overlap", run{0, 30, false, watchSource, "running"}, run{40, 70, false, garminSource, "running"}, "", both},
		{"other counts as the same activity", run{0, 60, true, watchSource, "running"}, run{0, 58, false, garminSource, "other"}, "", watch},
		{"different activities", run{0, 60, true, watchSource, "running"}, run{0, 58, false, garminSource, "cycling"}, "", both},
		{"same source is the fold's, not this rule's", run{0, 60, false, garminSource, "running"}, run{5, 65, false, garminSource, "running"}, "", both},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := &dupBench{t: t, admin: b.admin, engine: b.engine, ingest: b.ingest, srv: b.srv}
			user, token := b.user()
			day := scoredDay()
			lo, hi := uuid.New(), uuid.New()
			if lo.String() > hi.String() {
				lo, hi = hi, lo
			}
			wid, gid := lo, hi
			if c.lowID == garmin {
				wid, gid = hi, lo
			}
			b.workout(user, wid, c.w.activity, c.w.source, at(day, 600+c.w.from), at(day, 600+c.w.to))
			b.workout(user, gid, c.g.activity, c.g.source, at(day, 600+c.g.from), at(day, 600+c.g.to))
			for _, r := range []run{c.w, c.g} {
				if r.hr {
					b.hr(user, r.source, at(day, 600+r.from+1), at(day, 600+r.to-1), 140)
				}
			}
			b.compute(user, day)

			switch c.primary {
			case both:
				b.wantListed("no duplicate", token, day, wid, gid)
				if b.duplicateOf(user, wid) != nil || b.duplicateOf(user, gid) != nil {
					t.Error("duplicate_of must stay NULL")
				}
				for _, w := range b.dayView(token, day).Workouts {
					if _, present := alsoOf(w); present {
						t.Errorf("no also_recorded_by without a duplicate: %v", w)
					}
				}
				return
			case one:
				if n := len(b.dayView(token, day).Workouts); n != 1 {
					t.Fatalf("%d workouts listed, want 1", n)
				}
				if n := len(b.rangeView(token, day)); n != 1 {
					t.Fatalf("%d workouts in the range, want 1", n)
				}
				return
			}
			primary, dup, dupSource := wid, gid, c.g.source
			if c.primary == garmin {
				primary, dup, dupSource = gid, wid, c.w.source
			}
			b.wantListed("the primary only", token, day, primary)
			if d := b.duplicateOf(user, dup); d == nil || *d != primary {
				t.Errorf("duplicate_of of the hidden one = %v, want the primary %s", d, primary)
			}
			if d := b.duplicateOf(user, primary); d != nil {
				t.Errorf("the primary points at %s", d)
			}
			also, present := alsoOf(b.dayView(token, day).Workouts[0])
			if !present || !slices.Equal(also, []string{dupSource}) {
				t.Errorf("day view also_recorded_by = %v (present %v), want [%s]", also, present, dupSource)
			}
			also, present = alsoOf(b.rangeView(token, day)[0])
			if !present || !slices.Equal(also, []string{dupSource}) {
				t.Errorf("range view also_recorded_by = %v (present %v), want [%s]", also, present, dupSource)
			}
			if n := b.rowCount(user); n != 2 {
				t.Errorf("nothing is deleted: %d rows", n)
			}
		})
	}
}

func (b *dupBench) rowCount(user uuid.UUID) (n int) {
	b.t.Helper()
	if err := b.admin.QueryRow(context.Background(), `SELECT count(*) FROM workouts WHERE user_id = $1`, user).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

// ---- the reads ---------------------------------------------------------------------

// A Watch run and a Garmin run of the same hour: the day and the range list the Watch run
// (own heart rate) with also_recorded_by [Garmin Connect], the counted workouts of the day are
// the visible ones, and GET /v1/workouts/{id} of the hidden one still answers with the row as
// stored, without also_recorded_by, so a link the app holds keeps working. The listing is
// sorted and distinct when several sources recorded it. Another account's identical pair
// is its own cluster.
func TestCrossSourceReads(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	other, otherToken := b.user()
	day := scoredDay()
	for _, u := range []uuid.UUID{user, other} {
		b.hr(u, watchSource, at(day, 601), at(day, 659), 150)
	}
	watch := b.workout(user, uuid.Nil, "running", watchSource, at(day, 600), at(day, 660))
	garmin := b.workout(user, uuid.Nil, "running", garminSource, at(day, 602), at(day, 657))
	garmin2 := b.workout(user, uuid.Nil, "running", "Another Tracker", at(day, 601), at(day, 655))
	oWatch := b.workout(other, uuid.Nil, "running", watchSource, at(day, 600), at(day, 660))
	oGarmin := b.workout(other, uuid.Nil, "running", garminSource, at(day, 602), at(day, 657))
	b.compute(user, day)
	b.compute(other, day)

	b.wantListed("three recordings, one workout", token, day, watch)
	v := b.dayView(token, day)
	if also, _ := alsoOf(v.Workouts[0]); !slices.Equal(also, []string{"Another Tracker", garminSource}) {
		t.Errorf("also_recorded_by = %v, want the other sources sorted and distinct", also)
	}
	if n, _ := v.Score.Details["workouts"].(float64); n != 1 {
		t.Errorf("details.workouts counts the visible workouts: %v, want 1", v.Score.Details["workouts"])
	}
	for _, d := range []uuid.UUID{garmin, garmin2} {
		if p := b.duplicateOf(user, d); p == nil || *p != watch {
			t.Errorf("every duplicate points at the primary directly (no chains): %v", p)
		}
		var hidden map[string]any
		b.get(token, "/v1/workouts/"+d.String(), 200, &hidden)
		if idOf(hidden) != d.String() {
			t.Errorf("GET /v1/workouts/{id} of a hidden duplicate answers %v", hidden)
		}
		if _, present := alsoOf(hidden); present {
			t.Errorf("the hidden one is served as stored, without also_recorded_by: %v", hidden)
		}
	}

	b.wantListed("another account, its own pair", otherToken, day, oWatch)
	if p := b.duplicateOf(other, oGarmin); p == nil || *p != oWatch {
		t.Errorf("the other account's duplicate points into its own account: %v", p)
	}
	var n int
	if err := b.admin.QueryRow(context.Background(), `SELECT count(*) FROM workouts d JOIN workouts p ON p.id = d.duplicate_of WHERE d.user_id <> p.user_id`).Scan(&n); err != nil || n != 0 {
		t.Errorf("duplicate_of crossing accounts: %d, %v", n, err)
	}
	// An account never reads another's row, hidden or not.
	b.get(token, "/v1/workouts/"+oGarmin.String(), 404, nil)
}

// Deleting the primary through the deletion path (ryft_ingest, as a sync does) shows its
// duplicate at once, before any re-score; the next scoring leaves it listed on its own,
// without also_recorded_by and with duplicate_of cleared.
func TestCrossSourceDeletingThePrimaryShowsTheDuplicateAtOnce(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	b.hr(user, watchSource, at(day, 601), at(day, 659), 150)
	watch := b.workout(user, uuid.Nil, "running", watchSource, at(day, 600), at(day, 660))
	garmin := b.workout(user, uuid.Nil, "running", garminSource, at(day, 602), at(day, 657))
	b.compute(user, day)
	b.wantListed("before", token, day, watch)

	res, err := b.ingest.ApplySync(context.Background(), user, store.Apply{
		Deleted: []model.DeletedRecord{{Kind: model.DeletedWorkout, ID: watch}}, Loc: time.UTC,
		Emit: func(pgx.Tx, []string) error { return nil }})
	if err != nil {
		t.Fatalf("delete the primary as ryft_ingest: %v", err)
	}
	if res.Deleted.Workouts != 1 {
		t.Fatalf("deleted %+v", res.Deleted)
	}
	b.wantListed("primary deleted, not yet re-scored", token, day, garmin)
	if _, present := alsoOf(b.dayView(token, day).Workouts[0]); present {
		t.Error("the shown duplicate lists nobody as also_recorded_by")
	}

	b.compute(user, day)
	b.wantListed("re-scored", token, day, garmin)
	if d := b.duplicateOf(user, garmin); d != nil {
		t.Errorf("duplicate_of is cleared by the re-score: %s", d)
	}
	if _, present := alsoOf(b.dayView(token, day).Workouts[0]); present {
		t.Error("alone again: no also_recorded_by")
	}
}

// Scoring clears duplicate_of when the overlap disappears (a run moved or re-timed by its
// source) and sets it again when it returns.
func TestCrossSourceClearsWhenTheOverlapDisappears(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	b.hr(user, watchSource, at(day, 601), at(day, 659), 150)
	watch := b.workout(user, uuid.Nil, "running", watchSource, at(day, 600), at(day, 660))
	garmin := b.workout(user, uuid.Nil, "running", garminSource, at(day, 602), at(day, 657))
	b.compute(user, day)
	b.wantListed("overlapping", token, day, watch)

	b.exec(`UPDATE workouts SET start_at = $2, end_at = $3 WHERE id = $1`, garmin, at(day, 900), at(day, 950))
	b.compute(user, day)
	b.wantListed("apart", token, day, watch, garmin)
	if d := b.duplicateOf(user, garmin); d != nil {
		t.Errorf("duplicate_of is cleared: %s", d)
	}
	for _, w := range b.dayView(token, day).Workouts {
		if _, present := alsoOf(w); present {
			t.Errorf("no also_recorded_by once apart: %v", w)
		}
	}

	b.exec(`UPDATE workouts SET start_at = $2, end_at = $3 WHERE id = $1`, garmin, at(day, 605), at(day, 650))
	b.compute(user, day)
	b.wantListed("overlapping again", token, day, watch)
}

// The duplicate changes nothing in the numbers: the day's strain (from the de-duplicated
// heart rate) and the primary's own strain are the same with and without it.
func TestCrossSourceDoesNotChangeDayStrain(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	b.hr(user, watchSource, at(day, 601), at(day, 659), 150)
	watch := b.workout(user, uuid.Nil, "running", watchSource, at(day, 600), at(day, 660))
	b.compute(user, day)
	before := b.dayView(token, day)
	if before.Score.Strain == nil || before.Workouts[0]["strain"] == nil {
		t.Fatalf("the Watch run alone has a day strain and a workout strain: %+v", before)
	}

	b.workout(user, uuid.Nil, "running", garminSource, at(day, 602), at(day, 657))
	b.compute(user, day)
	after := b.dayView(token, day)
	if len(after.Workouts) != 1 || idOf(after.Workouts[0]) != watch.String() {
		t.Fatalf("only the Watch run is listed: %v", idsOf(after.Workouts))
	}
	if *after.Score.Strain != *before.Score.Strain {
		t.Errorf("day strain %v, was %v", *after.Score.Strain, *before.Score.Strain)
	}
	if after.Workouts[0]["strain"] != before.Workouts[0]["strain"] {
		t.Errorf("the primary's strain %v, was %v", after.Workouts[0]["strain"], before.Workouts[0]["strain"])
	}
}

// ---- Train sessions ------------------------------------------------------------------

// The workout RYFT makes for a Train session (source ryft-session) is never hidden, also
// when a HealthKit workout of the same activity lies inside it and would win by heart rate.
func TestCrossSourceNeverHidesARyftSessionWorkout(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	session := uuid.New()
	b.exec(`INSERT INTO sessions (user_id, id, started_at, ended_at, status) VALUES ($1, $2, $3, $4, 'completed')`,
		user, session, at(day, 600), at(day, 660))
	// 20 of the session's 60 minutes: below the share a session needs to adopt a workout.
	b.hr(user, watchSource, at(day, 621), at(day, 639), 140)
	b.workout(user, uuid.Nil, "strength", watchSource, at(day, 620), at(day, 640))
	b.compute(user, day)
	b.compute(user, day)

	own := train.SessionWorkoutID(user, session)
	if b.rowCountWhere(`user_id = $1 AND id = $2 AND source = 'ryft-session'`, user, own) != 1 {
		t.Fatal("scoring made the session's own workout")
	}
	if d := b.duplicateOf(user, own); d != nil {
		t.Errorf("the ryft-session workout points at %s", d)
	}
	listed := idsOf(b.dayView(token, day).Workouts)
	if !slices.Contains(listed, own.String()) {
		t.Errorf("day view %v lacks the ryft-session workout %s", listed, own)
	}
	if got := idsOf(b.rangeView(token, day)); !slices.Contains(got, own.String()) {
		t.Errorf("range view %v lacks the ryft-session workout", got)
	}
}

func (b *dupBench) rowCountWhere(where string, args ...any) (n int) {
	b.t.Helper()
	if err := b.admin.QueryRow(context.Background(), `SELECT count(*) FROM workouts WHERE `+where, args...).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

// A session's load lands on the workout that is listed. Two recordings of the session's
// hour, the Garmin one a minute wider (so it matches the session first); the Watch one has
// the heart rate and is the primary: after the day is scored the session is attached to the
// listed Watch workout, with its muscular load, and to no hidden one; the session row is kept.
func TestCrossSourceSessionLoadLandsOnTheListedWorkout(t *testing.T) {
	b := newDupBench(t)
	user, token := b.user()
	day := scoredDay()
	session := uuid.New()
	b.exec(`INSERT INTO sessions (user_id, id, started_at, ended_at, status) VALUES ($1, $2, $3, $4, 'completed')`,
		user, session, at(day, 600), at(day, 660))
	b.hr(user, watchSource, at(day, 601), at(day, 659), 130)
	watch := b.workout(user, uuid.Nil, "strength", watchSource, at(day, 600), at(day, 660))
	garmin := b.workout(user, uuid.Nil, "strength", garminSource, at(day, 599), at(day, 661))
	b.compute(user, day)
	b.compute(user, day) // a re-score settles what the first pass marked

	b.wantListed("one strength workout", token, day, watch)
	v := b.dayView(token, day)
	if got, _ := v.Workouts[0]["session_id"].(string); got != session.String() {
		t.Errorf("the listed workout carries the session: %v", v.Workouts[0])
	}
	if v.Workouts[0]["muscular_load"] == nil {
		t.Errorf("the listed workout carries the muscular load: %v", v.Workouts[0])
	}
	if n := b.rowCountWhere(`user_id = $1 AND session_id = $2 AND duplicate_of IS NULL`, user, session); n != 1 {
		t.Errorf("%d visible workouts carry the session, want 1", n)
	}
	if n := b.rowCountWhere(`user_id = $1 AND id = $2 AND session_id IS NOT NULL`, user, garmin); n != 0 {
		t.Error("the hidden recording carries no session")
	}
	if n := b.rowCountWhere(`user_id = $1 AND source = 'ryft-session'`, user); n != 0 {
		t.Errorf("a matching HealthKit workout exists: no ryft-session workout is created (%d)", n)
	}
	var kept int
	if err := b.admin.QueryRow(context.Background(), `SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2 AND workout_id = $3`, user, session, watch).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("sessions.workout_id points at the listed workout: %d, %v", kept, err)
	}
}
