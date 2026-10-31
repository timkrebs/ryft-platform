package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
)

// Tests of POST /v1/sync's deletions and HealthKit ids (docs/API.md → Deletions,
// HealthKit ids; design D-a, D-b). They speak the wire format (JSON in, JSON out) so
// they pin the contract, not the Go types. Postgres-backed ones skip without
// RYFT_TEST_POSTGRES_URL, like the rest of this package.

// ---- helpers --------------------------------------------------------------------

type delCounts struct {
	Samples  int `json:"samples"`
	Sleep    int `json:"sleep"`
	Workouts int `json:"workouts"`
}

type delIgnored struct {
	Duplicate     int `json:"duplicate"`
	OutsideWindow int `json:"outside_window"`
	UnknownType   int `json:"unknown_type"`
	UnknownID     int `json:"unknown_id"`
}

type delOut struct {
	Accepted delCounts `json:"accepted"`
	Deleted  delCounts `json:"deleted"`
	Ignored  struct {
		Samples  delIgnored `json:"samples"`
		Sleep    delIgnored `json:"sleep"`
		Workouts delIgnored `json:"workouts"`
	} `json:"ignored"`
	Days    []string `json:"days_touched"`
	Scoring struct {
		QueuedDays []string `json:"queued_days"`
	} `json:"scoring"`
}

// unknownIDs is the unknown_id counts as a delCounts.
func (o delOut) unknownIDs() delCounts {
	return delCounts{o.Ignored.Samples.UnknownID, o.Ignored.Sleep.UnknownID, o.Ignored.Workouts.UnknownID}
}

// delEnv is a database, a user (tz UTC, so a day is the UTC date) and the handler.
type delEnv struct {
	t    *testing.T
	pool *pgxpool.Pool
	st   *store.Store
	user model.User
	srv  http.Handler
	buf  fmt.Stringer
}

func newDelEnv(t *testing.T) *delEnv {
	t.Helper()
	pool := testPool(t)
	st := store.New(pool)
	e := &delEnv{t: t, pool: pool, st: st}
	e.user = e.newUser()
	srv, buf := syncServer(t, st, pool, e.user)
	e.srv, e.buf = srv, buf
	return e
}

func (e *delEnv) newUser() model.User {
	e.t.Helper()
	u, err := e.st.EnsureUser(context.Background(), store.Identity{ID: uuid.New()})
	if err != nil {
		e.t.Fatal(err)
	}
	u.TZ = "UTC"
	return u
}

// serverFor is a handler that serves user (another account).
func (e *delEnv) serverFor(u model.User) http.Handler {
	srv, _ := syncServer(e.t, e.st, e.pool, u)
	return srv
}

// recSample, recSleep, recWorkout and del build the request's parts.
func recSample(typ string, at time.Time, value float64, source string, id *uuid.UUID) map[string]any {
	m := map[string]any{"type": typ, "start": at, "end": at, "value": value, "unit": "x", "source": source}
	if id != nil {
		m["id"] = id.String()
	}
	return m
}

func recSleep(start, end time.Time, stage, source string, id *uuid.UUID) map[string]any {
	m := map[string]any{"start": start, "end": end, "stage": stage, "source": source}
	if id != nil {
		m["id"] = id.String()
	}
	return m
}

func recWorkout(id uuid.UUID, start time.Time, source string) map[string]any {
	return map[string]any{"id": id.String(), "activity": "running", "start": start, "end": start.Add(time.Hour), "source": source}
}

func del(kind string, id uuid.UUID) map[string]any {
	return map[string]any{"kind": kind, "id": id.String()}
}

func ptr(id uuid.UUID) *uuid.UUID { return &id }

type part struct {
	samples, sleep, workouts, deleted []map[string]any
}

func (p part) body() string {
	nz := func(m []map[string]any) []map[string]any {
		if m == nil {
			return []map[string]any{}
		}
		return m
	}
	b := map[string]any{"device_id": uuid.NewString(), "samples": nz(p.samples), "sleep": nz(p.sleep), "workouts": nz(p.workouts)}
	if p.deleted != nil {
		b["deleted"] = p.deleted
	}
	raw, _ := json.Marshal(b)
	return string(raw)
}

// send posts body to srv without t.Fatal, so goroutines may use it.
func send(srv http.Handler, body string) (*httptest.ResponseRecorder, delOut) {
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sync", strings.NewReader(body)))
	var out delOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

// ok posts p as the env's user and wants a 200.
func (e *delEnv) ok(p part) delOut {
	e.t.Helper()
	rec, out := send(e.srv, p.body())
	if rec.Code != http.StatusOK {
		e.t.Fatalf("%d %s, want 200", rec.Code, rec.Body)
	}
	return out
}

func (e *delEnv) count(q string, args ...any) (n int) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *delEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%.60s: %v", q, err)
	}
}

// hasSample, hasSleep, hasWorkout count the user's rows with that id.
func (e *delEnv) hasSample(u model.User, id uuid.UUID) int {
	return e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND hk_id = $2`, u.ID, id)
}
func (e *delEnv) hasSleep(u model.User, id uuid.UUID) int {
	return e.count(`SELECT count(*) FROM sleep_segments WHERE user_id = $1 AND hk_id = $2`, u.ID, id)
}
func (e *delEnv) hasWorkout(u model.User, id uuid.UUID) int {
	return e.count(`SELECT count(*) FROM workouts WHERE user_id = $1 AND id = $2`, u.ID, id)
}

// base is a fixed-hour instant two days back (inside the window, away from midnight).
func base() time.Time {
	d := time.Now().UTC().AddDate(0, 0, -2)
	return time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.UTC)
}

func day(t time.Time) string { return t.UTC().Format("2006-01-02") }

func wantCounts(t *testing.T, what string, got, want delCounts) {
	t.Helper()
	if got != want {
		t.Errorf("%s: %+v, want %+v", what, got, want)
	}
}

// ---- deletions ------------------------------------------------------------------

// Deleting by each kind removes the user's row only. An id of another account and an
// unknown id are unknown_id under their kind, indistinguishable, and never an error.
// The other rows of the user stay, and the log holds no id.
func TestDeletionByKindRemovesOnlyThisUsersRow(t *testing.T) {
	e := newDelEnv(t)
	other := e.newUser()
	otherSrv := e.serverFor(other)
	at := base()
	aS, aS2, aSl, aSl2, aW, aW2 := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	bS, bSl, bW := uuid.New(), uuid.New(), uuid.New()
	e.ok(part{
		samples:  []map[string]any{recSample("heart_rate", at, 60, "W", ptr(aS)), recSample("heart_rate", at.Add(time.Minute), 61, "W", ptr(aS2))},
		sleep:    []map[string]any{recSleep(at, at.Add(time.Hour), "core", "W", ptr(aSl)), recSleep(at.Add(time.Hour), at.Add(2*time.Hour), "deep", "W", ptr(aSl2))},
		workouts: []map[string]any{recWorkout(aW, at, "W"), recWorkout(aW2, at.Add(3*time.Hour), "W")},
	})
	// The other account holds the same natural keys under its own ids.
	if rec, _ := send(otherSrv, part{
		samples:  []map[string]any{recSample("heart_rate", at, 60, "W", ptr(bS))},
		sleep:    []map[string]any{recSleep(at, at.Add(time.Hour), "core", "W", ptr(bSl))},
		workouts: []map[string]any{recWorkout(bW, at, "W")},
	}.body()); rec.Code != 200 {
		t.Fatalf("other account: %d %s", rec.Code, rec.Body)
	}

	out := e.ok(part{deleted: []map[string]any{
		del("sample", aS), del("sleep", aSl), del("workout", aW), // this user's
		del("sample", bS), del("sleep", bSl), del("workout", bW), // the other's
		del("sample", uuid.New()), // never existed
	}})
	wantCounts(t, "deleted", out.Deleted, delCounts{1, 1, 1})
	wantCounts(t, "unknown_id", out.unknownIDs(), delCounts{2, 1, 1})
	wantCounts(t, "accepted (a deletion is not a record)", out.Accepted, delCounts{})
	if out.Ignored.Samples.Duplicate+out.Ignored.Sleep.Duplicate+out.Ignored.Workouts.Duplicate != 0 {
		t.Errorf("a deletion is never a duplicate: %+v", out.Ignored)
	}
	for name, n := range map[string]int{"sample": e.hasSample(e.user, aS), "sleep": e.hasSleep(e.user, aSl), "workout": e.hasWorkout(e.user, aW)} {
		if n != 0 {
			t.Errorf("%s deleted but still stored", name)
		}
	}
	for name, n := range map[string]int{"sample": e.hasSample(e.user, aS2), "sleep": e.hasSleep(e.user, aSl2), "workout": e.hasWorkout(e.user, aW2),
		"other sample": e.hasSample(other, bS), "other sleep": e.hasSleep(other, bSl), "other workout": e.hasWorkout(other, bW)} {
		if n != 1 {
			t.Errorf("%s must stay: %d", name, n)
		}
	}
	for _, id := range []uuid.UUID{aS, aSl, aW, bS, bSl, bW} {
		if strings.Contains(e.buf.String(), id.String()) {
			t.Fatalf("the log holds an id (%s):\n%s", id, e.buf)
		}
	}
}

// A deletion sent again answers 200 with every entry unknown_id; a repeated pair in one
// request counts once (deleted + unknown_id = distinct ids).
func TestDeletionResentIsUnknownID(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	s, sl, w := uuid.New(), uuid.New(), uuid.New()
	e.ok(part{samples: []map[string]any{recSample("steps", at, 5, "W", ptr(s))},
		sleep:    []map[string]any{recSleep(at, at.Add(time.Hour), "core", "W", ptr(sl))},
		workouts: []map[string]any{recWorkout(w, at, "W")}})
	request := part{deleted: []map[string]any{del("sample", s), del("sample", s), del("sleep", sl), del("workout", w), del("workout", w)}}

	first := e.ok(request)
	wantCounts(t, "first: deleted", first.Deleted, delCounts{1, 1, 1})
	wantCounts(t, "first: unknown_id", first.unknownIDs(), delCounts{})
	again := e.ok(request)
	wantCounts(t, "re-sent: deleted", again.Deleted, delCounts{})
	wantCounts(t, "re-sent: unknown_id", again.unknownIDs(), delCounts{1, 1, 1})
}

// An unknown kind is skipped and not counted; a nil UUID under a known kind is
// unknown_id; a response always carries deleted and unknown_id, a request without
// deleted behaves as before; a string that is no UUID is a 400.
func TestDeletionTolerance(t *testing.T) {
	e := newDelEnv(t)
	out := e.ok(part{deleted: []map[string]any{
		{"kind": "heart_rate_zone", "id": uuid.NewString()}, {"kind": "", "id": uuid.NewString()},
		del("sample", uuid.Nil), del("sleep", uuid.Nil), del("workout", uuid.Nil)}})
	wantCounts(t, "deleted", out.Deleted, delCounts{})
	wantCounts(t, "nil uuid: unknown_id; unknown kinds: not counted", out.unknownIDs(), delCounts{1, 1, 1})

	// Absent `deleted`: today's behaviour, and the new fields are there, zero.
	rec, _ := send(e.srv, part{samples: []map[string]any{recSample("steps", base(), 1, "W", nil)}}.body())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"deleted":{"samples":0,"sleep":0,"workouts":0}`) ||
		strings.Count(rec.Body.String(), `"unknown_id":0`) != 3 {
		t.Errorf("a response always carries deleted and unknown_id: %d %s", rec.Code, rec.Body)
	}

	for name, raw := range map[string]string{
		"id not a uuid":         `{"device_id":"` + uuid.NewString() + `","deleted":[{"kind":"sample","id":"not-a-uuid"}]}`,
		"deleted is not a list": `{"device_id":"` + uuid.NewString() + `","deleted":{"kind":"sample"}}`,
	} {
		if rec, _ := send(e.srv, raw); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
}

// A workout RYFT made for a Train session is not deletable here.
func TestDeletionRyftSessionWorkoutIsNotDeletable(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	mine, hk := uuid.New(), uuid.New()
	if _, err := e.st.UpsertWorkouts(context.Background(), e.user.ID, []model.Workout{
		{ID: mine, Activity: "strength", Start: at, End: at.Add(time.Hour), Source: "ryft-session"},
		{ID: hk, Activity: "running", Start: at.Add(5 * time.Hour), End: at.Add(6 * time.Hour), Source: "Watch"}}); err != nil {
		t.Fatal(err)
	}
	out := e.ok(part{deleted: []map[string]any{del("workout", mine), del("workout", hk)}})
	wantCounts(t, "deleted", out.Deleted, delCounts{0, 0, 1})
	wantCounts(t, "unknown_id", out.unknownIDs(), delCounts{0, 0, 1})
	if e.hasWorkout(e.user, mine) != 1 || e.hasWorkout(e.user, hk) != 0 {
		t.Fatal("the ryft-session workout stays, the HealthKit one goes")
	}
}

// The days of every deleted row are queued (score_queue, pending) and announced by one
// outbox event, from a request that carries nothing else. The touched-days rules apply:
// an HRV sample also touches the next day, a sleep segment the day it ends, a workout the
// next day.
func TestDeletionQueuesDaysAndWritesTheEvent(t *testing.T) {
	e := newDelEnv(t)
	d0 := time.Date(base().Year(), base().Month(), base().Day(), 22, 0, 0, 0, time.UTC) // 22:00 on D
	hrv, sl, w, steps := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e.ok(part{samples: []map[string]any{recSample("hrv_sdnn", d0, 50, "W", ptr(hrv)), recSample("steps", d0.AddDate(0, 0, -5), 9, "W", ptr(steps))},
		sleep:    []map[string]any{recSleep(d0.Add(-2*time.Hour), d0.Add(5*time.Hour), "core", "W", ptr(sl))}, // ends on D+1
		workouts: []map[string]any{recWorkout(w, d0.AddDate(0, 0, -10), "W")}})
	e.exec(`DELETE FROM score_queue WHERE user_id = $1`, e.user.ID)
	e.exec(`DELETE FROM outbox WHERE user_id = $1`, e.user.ID)

	cases := []struct {
		name string
		d    map[string]any
		days []string
	}{
		{"hrv sample: its day and the next", del("sample", hrv), []string{day(d0), day(d0.AddDate(0, 0, 1))}},
		{"sleep segment: the day it ends", del("sleep", sl), []string{day(d0.AddDate(0, 0, 1))}},
		{"steps sample: its day", del("sample", steps), []string{day(d0.AddDate(0, 0, -5))}},
		{"workout: its day and the next", del("workout", w), []string{day(d0.AddDate(0, 0, -10)), day(d0.AddDate(0, 0, -9))}},
	}
	for _, c := range cases {
		e.exec(`DELETE FROM score_queue WHERE user_id = $1`, e.user.ID)
		e.exec(`DELETE FROM outbox WHERE user_id = $1`, e.user.ID)
		out := e.ok(part{deleted: []map[string]any{c.d}})
		if strings.Join(out.Days, ",") != strings.Join(c.days, ",") || strings.Join(out.Scoring.QueuedDays, ",") != strings.Join(c.days, ",") {
			t.Errorf("%s: days_touched %v, queued_days %v, want %v", c.name, out.Days, out.Scoring.QueuedDays, c.days)
		}
		var queued []string
		rows, err := e.pool.Query(context.Background(), `SELECT day::text FROM score_queue WHERE user_id = $1 AND scored_at IS NULL ORDER BY day`, e.user.ID)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var d string
			_ = rows.Scan(&d)
			queued = append(queued, d)
		}
		rows.Close()
		if strings.Join(queued, ",") != strings.Join(c.days, ",") {
			t.Errorf("%s: score_queue %v, want %v pending", c.name, queued, c.days)
		}
		var payload string
		if n := e.count(`SELECT count(*) FROM outbox WHERE user_id = $1 AND publisher = 'ingest' AND routing_key = 'ingested' AND published_at IS NULL`, e.user.ID); n != 1 {
			t.Fatalf("%s: %d outbox events, want 1", c.name, n)
		}
		if err := e.pool.QueryRow(context.Background(), `SELECT payload::text FROM outbox WHERE user_id = $1`, e.user.ID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		for _, d := range c.days {
			if !strings.Contains(payload, d) {
				t.Errorf("%s: the event %s lacks day %s", c.name, payload, d)
			}
		}
	}
	// Nothing deleted, nothing queued: an unknown id touches no day.
	e.exec(`DELETE FROM score_queue WHERE user_id = $1`, e.user.ID)
	e.exec(`DELETE FROM outbox WHERE user_id = $1`, e.user.ID)
	out := e.ok(part{deleted: []map[string]any{del("sample", uuid.New())}})
	if len(out.Days) != 0 || e.count(`SELECT count(*) FROM score_queue WHERE user_id = $1`, e.user.ID) != 0 {
		t.Errorf("an unknown id touches no day: %v", out.Days)
	}
}

// If the apply transaction fails, the request answers 503 with Retry-After and leaves
// nothing half-done: no row deleted, no day queued, no event. The records of the request
// are stored (idempotent). Re-sending after the cause is gone deletes, then reports
// unknown_id.
func TestFailingApplyIs503AndLeavesNothingHalfDone(t *testing.T) {
	for _, c := range []struct{ name, on string }{
		{"the delete", "BEFORE DELETE ON samples"},
		{"the queue", "BEFORE INSERT OR UPDATE ON score_queue"},
		{"the outbox", "BEFORE INSERT ON outbox"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newDelEnv(t)
			at := base()
			old, fresh := uuid.New(), uuid.New()
			e.ok(part{samples: []map[string]any{recSample("steps", at, 5, "W", ptr(old))}})
			e.exec(`DELETE FROM score_queue WHERE user_id = $1`, e.user.ID)
			e.exec(`DELETE FROM outbox WHERE user_id = $1`, e.user.ID)

			e.exec(`CREATE FUNCTION test_apply_fails() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'apply fails'; END $$`)
			e.exec(`CREATE TRIGGER test_apply_fails ` + c.on + ` FOR EACH ROW EXECUTE FUNCTION test_apply_fails()`)
			request := part{samples: []map[string]any{recSample("steps", at.AddDate(0, 0, -3), 7, "W", ptr(fresh))},
				deleted: []map[string]any{del("sample", old)}}
			rec, _ := send(e.srv, request.body())
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
				t.Fatalf("%d (Retry-After %q) %s, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"), rec.Body)
			}
			if e.hasSample(e.user, old) != 1 {
				t.Error("the deletion was applied half-way")
			}
			if n := e.count(`SELECT count(*) FROM score_queue WHERE user_id = $1`, e.user.ID); n != 0 {
				t.Errorf("%d days queued by a failed apply", n)
			}
			if n := e.count(`SELECT count(*) FROM outbox WHERE user_id = $1`, e.user.ID); n != 0 {
				t.Errorf("%d events written by a failed apply", n)
			}
			if e.hasSample(e.user, fresh) != 1 {
				t.Error("the request's records are stored, as the contract says")
			}

			e.exec(`DROP TRIGGER test_apply_fails ON ` + strings.Fields(c.on)[len(strings.Fields(c.on))-1])
			resent := e.ok(request)
			wantCounts(t, "re-sent: deleted", resent.Deleted, delCounts{Samples: 1})
			wantCounts(t, "re-sent: unknown_id", resent.unknownIDs(), delCounts{})
			if resent.Ignored.Samples.Duplicate != 1 || resent.Accepted.Samples != 0 {
				t.Errorf("the stored record is a duplicate on the re-send: %+v", resent)
			}
			if e.hasSample(e.user, old) != 0 || e.count(`SELECT count(*) FROM score_queue WHERE user_id = $1 AND scored_at IS NULL`, e.user.ID) == 0 ||
				e.count(`SELECT count(*) FROM outbox WHERE user_id = $1`, e.user.ID) != 1 {
				t.Error("the re-send deletes, queues and writes one event")
			}
			again := e.ok(request)
			wantCounts(t, "third send: unknown_id", again.unknownIDs(), delCounts{Samples: 1})
		})
	}
}

// Deleting the HealthKit workout of a Train session keeps the session, and queues the day
// before the workout too (the session matcher looks +-12 h); a workout without a session
// queues its own day and the next only.
func TestDeletingAWorkoutWithASessionKeepsTheSession(t *testing.T) {
	e := newDelEnv(t)
	at := time.Date(base().Year(), base().Month(), base().Day(), 9, 0, 0, 0, time.UTC)
	withSession, plain, session := uuid.New(), uuid.New(), uuid.New()
	e.ok(part{workouts: []map[string]any{recWorkout(withSession, at, "Watch"), recWorkout(plain, at.AddDate(0, 0, -7), "Watch")}})
	e.exec(`INSERT INTO sessions (user_id, id, started_at, status, workout_id) VALUES ($1, $2, $3, 'completed', $4)`,
		e.user.ID, session, at, withSession)
	e.exec(`UPDATE workouts SET session_id = $1, muscular_load = 40 WHERE id = $2`, session, withSession)

	out := e.ok(part{deleted: []map[string]any{del("workout", withSession)}})
	wantCounts(t, "deleted", out.Deleted, delCounts{Workouts: 1})
	want := []string{day(at.AddDate(0, 0, -1)), day(at), day(at.AddDate(0, 0, 1))}
	if strings.Join(out.Days, ",") != strings.Join(want, ",") {
		t.Errorf("days_touched %v, want %v", out.Days, want)
	}
	if e.count(`SELECT count(*) FROM sessions WHERE user_id = $1 AND id = $2`, e.user.ID, session) != 1 {
		t.Fatal("the session is kept")
	}
	if e.hasWorkout(e.user, withSession) != 0 {
		t.Fatal("the workout is deleted")
	}

	out = e.ok(part{deleted: []map[string]any{del("workout", plain)}})
	want = []string{day(at.AddDate(0, 0, -7)), day(at.AddDate(0, 0, -6))}
	if strings.Join(out.Days, ",") != strings.Join(want, ",") {
		t.Errorf("a workout without a session: days_touched %v, want %v", out.Days, want)
	}
}

// 2000 entries are accepted (any mix of kinds); 2001 is a 422 that applies nothing, not
// even the request's records.
func TestDeletionsLimit(t *testing.T) {
	e := newDelEnv(t)
	entries := func(n int) []map[string]any {
		out := make([]map[string]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, del([]string{"sample", "sleep", "workout"}[i%3], uuid.New()))
		}
		return out
	}
	out := e.ok(part{deleted: entries(2000)})
	if got := out.unknownIDs(); got.Samples+got.Sleep+got.Workouts != 2000 {
		t.Errorf("2000 entries all counted: %+v", got)
	}
	// A skipped unknown kind still counts toward the limit: the cap is on the list.
	over := append(entries(2000), map[string]any{"kind": "future", "id": uuid.NewString()})
	known := uuid.New()
	over2 := part{samples: []map[string]any{recSample("steps", base(), 3, "W", ptr(known))}, deleted: over}
	rec, _ := send(e.srv, over2.body())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("2001 entries: %d %s, want 422", rec.Code, rec.Body)
	}
	if e.hasSample(e.user, known) != 0 {
		t.Error("a 422 stores nothing")
	}
}

// ---- ids on records (the adopt rule) -------------------------------------------------

func (e *delEnv) sampleRow(at time.Time) (hk *uuid.UUID, value float64, n int) {
	e.t.Helper()
	n = e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND type = 'resting_hr' AND start_at = $2`, e.user.ID, at)
	if n == 0 {
		return nil, 0, 0
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT hk_id, value FROM samples WHERE user_id = $1 AND type = 'resting_hr' AND start_at = $2 ORDER BY id LIMIT 1`,
		e.user.ID, at).Scan(&hk, &value); err != nil {
		e.t.Fatal(err)
	}
	return hk, value, n
}

// A sample with an id adopts the row with the same natural key: the id and the value
// follow the latest upload; it counts accepted when either changed, else duplicate; the
// row never doubles. This also gives an id to a row uploaded before ids existed.
func TestSampleWithIDAdoptsTheRowOfItsNaturalKey(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	id1, id2 := uuid.New(), uuid.New()
	step := func(name string, id *uuid.UUID, value float64, wantID *uuid.UUID, wantAccepted, wantDup int) {
		t.Helper()
		out := e.ok(part{samples: []map[string]any{recSample("resting_hr", at, value, "Garmin", id)}})
		hk, v, n := e.sampleRow(at)
		if n != 1 || v != value || (wantID == nil) != (hk == nil) || (wantID != nil && *hk != *wantID) {
			t.Errorf("%s: %d rows, id %v, value %v; want 1 row, id %v, value %v", name, n, hk, v, wantID, value)
		}
		if out.Accepted.Samples != wantAccepted || out.Ignored.Samples.Duplicate != wantDup {
			t.Errorf("%s: accepted %d, duplicate %d; want %d, %d", name, out.Accepted.Samples, out.Ignored.Samples.Duplicate, wantAccepted, wantDup)
		}
	}
	step("uploaded before ids existed", nil, 50, nil, 1, 0)
	step("no id, identical: as before", nil, 50, nil, 0, 1)
	step("the id is backfilled", ptr(id1), 50, ptr(id1), 1, 0)
	step("same id, same value", ptr(id1), 50, ptr(id1), 0, 1)
	step("same id, new value", ptr(id1), 52, ptr(id1), 1, 0)
	step("Garmin: new id, same value", ptr(id2), 52, ptr(id2), 1, 0)
	id3 := uuid.New()
	step("Garmin: new id and new value", ptr(id3), 54, ptr(id3), 1, 0)
	step("no id: keeps the id, takes the new value (as before)", nil, 55, ptr(id3), 1, 0)
}

// A record without an id
// never clears or changes a stored id, and a re-send with ids after one without doubles nothing.
func TestRecordWithoutIDKeepsTheStoredID(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	id := uuid.New()
	e.ok(part{samples: []map[string]any{recSample("resting_hr", at, 50, "W", ptr(id))}})
	out := e.ok(part{samples: []map[string]any{recSample("resting_hr", at, 50, "W", nil)}})
	hk, _, n := e.sampleRow(at)
	if n != 1 || hk == nil || *hk != id || out.Ignored.Samples.Duplicate != 1 {
		t.Errorf("a record without id: %d rows, id %v, duplicate %d; want 1, %v, 1", n, hk, out.Ignored.Samples.Duplicate, id)
	}
	// 40 records without ids, then the same with ids: 40 rows, 40 adopted, none doubled.
	var plain, withID []map[string]any
	for i := 0; i < 40; i++ {
		s := recSample("heart_rate", at.Add(time.Duration(i)*time.Minute), float64(60+i), "W", nil)
		plain = append(plain, s)
		withID = append(withID, recSample("heart_rate", at.Add(time.Duration(i)*time.Minute), float64(60+i), "W", ptr(uuid.New())))
	}
	e.ok(part{samples: plain})
	out = e.ok(part{samples: withID})
	if out.Accepted.Samples != 40 || e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND type = 'heart_rate'`, e.user.ID) != 40 ||
		e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND type = 'heart_rate' AND hk_id IS NOT NULL`, e.user.ID) != 40 {
		t.Errorf("40 adopted, none doubled: accepted %d", out.Accepted.Samples)
	}
}

// A record whose id is already stored on a different row (the source was renamed) is a
// duplicate: the old row stays, the new one is not stored, nothing is counted twice.
func TestRecordWithAStoredIDOnAnotherRowIsADuplicate(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	id, sleepID := uuid.New(), uuid.New()
	e.ok(part{samples: []map[string]any{recSample("resting_hr", at, 50, "Garmin", ptr(id))},
		sleep: []map[string]any{recSleep(at, at.Add(time.Hour), "core", "Garmin", ptr(sleepID))}})
	out := e.ok(part{samples: []map[string]any{recSample("resting_hr", at, 51, "Garmin Connect", ptr(id))},
		sleep: []map[string]any{recSleep(at, at.Add(time.Hour), "core", "Garmin Connect", ptr(sleepID))}})
	if out.Accepted.Samples != 0 || out.Ignored.Samples.Duplicate != 1 || out.Accepted.Sleep != 0 || out.Ignored.Sleep.Duplicate != 1 {
		t.Errorf("duplicates, not accepted, no 500: %+v", out)
	}
	if e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND source = 'Garmin' AND hk_id = $2 AND value = 50`, e.user.ID, id) != 1 ||
		e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND source = 'Garmin Connect'`, e.user.ID) != 0 ||
		e.count(`SELECT count(*) FROM sleep_segments WHERE user_id = $1 AND source = 'Garmin Connect'`, e.user.ID) != 0 {
		t.Error("the old row stays untouched and the renamed one is not stored")
	}
}

// Another account may use the same id: ids are unique per user.
func TestTheSameIDForTwoUsersIsFine(t *testing.T) {
	e := newDelEnv(t)
	other := e.newUser()
	id := uuid.New()
	e.ok(part{samples: []map[string]any{recSample("steps", base(), 1, "W", ptr(id))}})
	rec, out := send(e.serverFor(other), part{samples: []map[string]any{recSample("steps", base(), 1, "W", ptr(id))}}.body())
	if rec.Code != 200 || out.Accepted.Samples != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	// Deleting as the first user leaves the second's.
	e.ok(part{deleted: []map[string]any{del("sample", id)}})
	if e.hasSample(e.user, id) != 0 || e.hasSample(other, id) != 1 {
		t.Error("deletion is per user")
	}
}

// A sleep segment adopts the same way (no value to follow).
func TestSleepWithIDAdoptsTheRowOfItsNaturalKey(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	id1, id2 := uuid.New(), uuid.New()
	send1 := func(id *uuid.UUID) delOut {
		return e.ok(part{sleep: []map[string]any{recSleep(at, at.Add(time.Hour), "core", "W", id)}})
	}
	stored := func() (hk *uuid.UUID, n int) {
		n = e.count(`SELECT count(*) FROM sleep_segments WHERE user_id = $1`, e.user.ID)
		_ = e.pool.QueryRow(context.Background(), `SELECT hk_id FROM sleep_segments WHERE user_id = $1 LIMIT 1`, e.user.ID).Scan(&hk)
		return
	}
	for _, c := range []struct {
		name     string
		id       *uuid.UUID
		wantID   *uuid.UUID
		accepted int
		dup      int
	}{
		{"before ids", nil, nil, 1, 0},
		{"backfilled", ptr(id1), ptr(id1), 1, 0},
		{"same id again", ptr(id1), ptr(id1), 0, 1},
		{"new id (Garmin)", ptr(id2), ptr(id2), 1, 0},
		{"no id keeps the id", nil, ptr(id2), 0, 1},
	} {
		out := send1(c.id)
		hk, n := stored()
		if n != 1 || (c.wantID == nil) != (hk == nil) || (c.wantID != nil && *hk != *c.wantID) || out.Accepted.Sleep != c.accepted || out.Ignored.Sleep.Duplicate != c.dup {
			t.Errorf("%s: rows %d id %v accepted %d duplicate %d; want id %v, %d, %d", c.name, n, hk, out.Accepted.Sleep, out.Ignored.Sleep.Duplicate, c.wantID, c.accepted, c.dup)
		}
	}
}

// The Garmin pattern: the same times under a new UUID, then the old UUID deleted. The record
// survives, in one request or in two; a record uploaded and deleted in one request is gone.
func TestGarminRewriteThenDeleteKeepsTheRecord(t *testing.T) {
	for _, together := range []bool{false, true} {
		t.Run(fmt.Sprintf("one request: %v", together), func(t *testing.T) {
			e := newDelEnv(t)
			at := base()
			oldID, newID := uuid.New(), uuid.New()
			sl := time.Hour
			e.ok(part{samples: []map[string]any{recSample("resting_hr", at, 50, "Garmin", ptr(oldID))},
				sleep: []map[string]any{recSleep(at, at.Add(sl), "core", "Garmin", ptr(oldID))}})
			rewrite := part{samples: []map[string]any{recSample("resting_hr", at, 49, "Garmin", ptr(newID))},
				sleep: []map[string]any{recSleep(at, at.Add(sl), "core", "Garmin", ptr(newID))}}
			deletion := []map[string]any{del("sample", oldID), del("sleep", oldID)}
			var out delOut
			if together {
				rewrite.deleted = deletion
				out = e.ok(rewrite)
			} else {
				e.ok(rewrite)
				out = e.ok(part{deleted: deletion})
			}
			wantCounts(t, "deleted: the old id is already gone", out.Deleted, delCounts{})
			wantCounts(t, "unknown_id", out.unknownIDs(), delCounts{1, 1, 0})
			hk, v, n := e.sampleRow(at)
			if n != 1 || hk == nil || *hk != newID || v != 49 {
				t.Errorf("sample: %d rows, id %v, value %v; want 1, %v, 49", n, hk, v, newID)
			}
			if e.hasSleep(e.user, newID) != 1 || e.count(`SELECT count(*) FROM sleep_segments WHERE user_id = $1`, e.user.ID) != 1 {
				t.Error("the sleep segment survives under the new id")
			}
		})
	}

	t.Run("uploaded and deleted in one request", func(t *testing.T) {
		e := newDelEnv(t)
		id, sid := uuid.New(), uuid.New()
		out := e.ok(part{samples: []map[string]any{recSample("steps", base(), 3, "W", ptr(id))},
			sleep:   []map[string]any{recSleep(base(), base().Add(time.Hour), "core", "W", ptr(sid))},
			deleted: []map[string]any{del("sample", id), del("sleep", sid)}})
		wantCounts(t, "deleted", out.Deleted, delCounts{1, 1, 0})
		if out.Accepted.Samples != 1 || e.hasSample(e.user, id) != 0 || e.hasSleep(e.user, sid) != 0 {
			t.Errorf("stored, then deleted: %+v", out)
		}
	})
}

// ---- concurrency ------------------------------------------------------------------------

// Two syncs of one user at the same time, each with its own new records and deletions that
// overlap and are listed in opposite orders. This pins what holds with or without the
// per-user advisory lock: no deadlock (a 30 s guard), every request a 200, every id named is
// gone, every new and untouched row stays, and the row lock makes exactly one request the
// winner of each overlapped id (deleted in one answer, unknown_id in the other). It does NOT
// prove the advisory lock; that one serializes the fold of same-source workouts (D-c), which
// is not in this PR.
func TestConcurrentSyncsWithDeletionsDoNotDeadlockOrLoseRows(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	const rounds, perKind = 6, 12
	for round := 0; round < rounds; round++ {
		type ids struct{ s, sl, w []uuid.UUID }
		mk := func() ids {
			var x ids
			for i := 0; i < perKind; i++ {
				x.s, x.sl, x.w = append(x.s, uuid.New()), append(x.sl, uuid.New()), append(x.w, uuid.New())
			}
			return x
		}
		shared, a, b, keep := mk(), mk(), mk(), mk()
		seed := func(x ids, off time.Duration) part {
			var p part
			for i := range x.s {
				ts := at.Add(off + time.Duration(i)*time.Minute)
				p.samples = append(p.samples, recSample("heart_rate", ts, float64(i), "W", ptr(x.s[i])))
				p.sleep = append(p.sleep, recSleep(ts, ts.Add(30*time.Second), "core", "W", ptr(x.sl[i])))
				p.workouts = append(p.workouts, recWorkout(x.w[i], ts, "W"))
			}
			return p
		}
		off := time.Duration(round) * 3 * time.Hour
		var all part
		for k, x := range []ids{shared, a, b, keep} {
			p := seed(x, off+time.Duration(k)*30*time.Minute)
			all.samples, all.sleep, all.workouts = append(all.samples, p.samples...), append(all.sleep, p.sleep...), append(all.workouts, p.workouts...)
		}
		e.ok(all)

		deletions := func(own ids, reverse bool) []map[string]any {
			var d []map[string]any
			for i := range own.s {
				d = append(d, del("sample", own.s[i]), del("sleep", own.sl[i]), del("workout", own.w[i]))
			}
			for i := range shared.s {
				d = append(d, del("sample", shared.s[i]), del("sleep", shared.sl[i]), del("workout", shared.w[i]))
			}
			if reverse {
				for i, j := 0, len(d)-1; i < j; i, j = i+1, j-1 {
					d[i], d[j] = d[j], d[i]
				}
			}
			return d
		}
		fresh := func(who string) part {
			ts := at.AddDate(0, 0, -30).Add(time.Duration(round) * time.Hour)
			if who == "b" {
				ts = ts.Add(10 * time.Minute)
			}
			return part{samples: []map[string]any{recSample("steps", ts, 1, who, ptr(uuid.New()))}}
		}
		reqA, reqB := fresh("a"), fresh("b")
		reqA.deleted, reqB.deleted = deletions(a, false), deletions(b, true)

		var wg sync.WaitGroup
		start := make(chan struct{})
		recs := make([]*httptest.ResponseRecorder, 2)
		outs := make([]delOut, 2)
		for i, body := range []string{reqA.body(), reqB.body()} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				recs[i], outs[i] = send(e.srv, body)
			}()
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("two syncs of one user did not finish in 30 s: deadlock")
		}
		for i, rec := range recs {
			if rec.Code != http.StatusOK {
				t.Fatalf("round %d, request %d: %d %s", round, i, rec.Code, rec.Body)
			}
		}
		total := delCounts{outs[0].Deleted.Samples + outs[1].Deleted.Samples, outs[0].Deleted.Sleep + outs[1].Deleted.Sleep,
			outs[0].Deleted.Workouts + outs[1].Deleted.Workouts}
		if want := (delCounts{3 * perKind, 3 * perKind, 3 * perKind}); total != want {
			t.Errorf("round %d: every id deleted exactly once across the two answers: %+v, want %+v", round, total, want)
		}
		unknown := delCounts{outs[0].unknownIDs().Samples + outs[1].unknownIDs().Samples, outs[0].unknownIDs().Sleep + outs[1].unknownIDs().Sleep,
			outs[0].unknownIDs().Workouts + outs[1].unknownIDs().Workouts}
		if want := (delCounts{perKind, perKind, perKind}); unknown != want {
			t.Errorf("round %d: the overlap is unknown_id for the loser: %+v, want %+v", round, unknown, want)
		}
		for _, x := range []ids{shared, a, b} {
			for i := range x.s {
				if e.hasSample(e.user, x.s[i])+e.hasSleep(e.user, x.sl[i])+e.hasWorkout(e.user, x.w[i]) != 0 {
					t.Fatalf("round %d: a named id is still stored", round)
				}
			}
		}
		for i := range keep.s {
			if e.hasSample(e.user, keep.s[i]) != 1 || e.hasSleep(e.user, keep.sl[i]) != 1 || e.hasWorkout(e.user, keep.w[i]) != 1 {
				t.Fatalf("round %d: an untouched row was lost", round)
			}
		}
		if e.count(`SELECT count(*) FROM samples WHERE user_id = $1 AND type = 'steps' AND source IN ('a', 'b') AND start_at = $2`, e.user.ID,
			at.AddDate(0, 0, -30).Add(time.Duration(round)*time.Hour)) != 1 {
			t.Fatalf("round %d: a new record was lost", round)
		}
	}
}

// A conflict between two syncs (a deadlock 40P01, a unique violation 23505 on an id,
// a serialization failure 40001) raised by any of the three upserts is a retryable 503
// with Retry-After (h.fail); any other database error stays a 500.
func TestUpsertConflictsAre503(t *testing.T) {
	body := func() part {
		at := base()
		return part{samples: []map[string]any{recSample("steps", at, 1, "W", ptr(uuid.New()))},
			sleep:    []map[string]any{recSleep(at, at.Add(time.Hour), "core", "W", ptr(uuid.New()))},
			workouts: []map[string]any{recWorkout(uuid.New(), at, "W")}}
	}
	for _, table := range []string{"samples", "sleep_segments", "workouts"} {
		for _, c := range []struct {
			code string
			want int
		}{{"40P01", 503}, {"23505", 503}, {"40001", 503}, {"22023", 500}} {
			t.Run(table+"/"+c.code, func(t *testing.T) {
				e := newDelEnv(t)
				e.exec(`CREATE FUNCTION test_conflict() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'conflict' USING ERRCODE = '` + c.code + `'; END $$`)
				e.exec(`CREATE TRIGGER test_conflict BEFORE INSERT ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION test_conflict()`)
				rec, _ := send(e.srv, body().body())
				if rec.Code != c.want || (c.want == 503) != (rec.Header().Get("Retry-After") != "") {
					t.Fatalf("%d (Retry-After %q) %s, want %d", rec.Code, rec.Header().Get("Retry-After"), rec.Body, c.want)
				}
			})
		}
	}
}

// The queued days are local to the user's tz, not UTC: a sample at 23:30 UTC belongs to the
// next day in Berlin, in summer and in winter; and around both DST changes.
func TestDeletionQueuesLocalDaysInTheUsersTimezone(t *testing.T) {
	e := newDelEnv(t)
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skip("no tz database")
	}
	u := e.newUser()
	u.TZ = "Europe/Berlin"
	srv := e.serverFor(u)
	utc := func(m time.Month, d, h, min int) time.Time { return time.Date(2026, m, d, h, min, 0, 0, time.UTC) }
	for _, c := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"summer, 23:30 UTC is tomorrow", utc(time.July, 1, 23, 30), "2026-07-02"},
		{"winter, 23:30 UTC is tomorrow", utc(time.January, 15, 23, 30), "2026-01-16"},
		{"before the autumn change (CEST)", utc(time.October, 24, 23, 30), "2026-10-25"},
		{"after the autumn change (CET)", utc(time.October, 25, 23, 30), "2026-10-26"},
		{"before the spring change (CET)", utc(time.March, 28, 23, 30), "2026-03-29"},
		{"after the spring change (CEST)", utc(time.March, 29, 22, 30), "2026-03-30"},
		{"22:30 UTC in summer is still the UTC day's evening", utc(time.July, 1, 21, 59), "2026-07-01"},
	} {
		if got := c.at.In(berlin).Format("2006-01-02"); got != c.want {
			t.Fatalf("%s: the fixture is wrong, local day %s", c.name, got)
		}
		id := uuid.New()
		if rec, _ := send(srv, part{samples: []map[string]any{recSample("steps", c.at, 1, "W", ptr(id))}}.body()); rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
		e.exec(`DELETE FROM score_queue WHERE user_id = $1`, u.ID)
		rec, out := send(srv, part{deleted: []map[string]any{del("sample", id)}}.body())
		if rec.Code != 200 || len(out.Days) != 1 || out.Days[0] != c.want {
			t.Errorf("%s: %d days_touched %v, want [%s]", c.name, rec.Code, out.Days, c.want)
		}
		if e.count(`SELECT count(*) FROM score_queue WHERE user_id = $1 AND day = $2::date AND scored_at IS NULL`, u.ID, c.want) != 1 ||
			e.count(`SELECT count(*) FROM score_queue WHERE user_id = $1`, u.ID) != 1 {
			t.Errorf("%s: score_queue must hold exactly the local day %s", c.name, c.want)
		}
	}
}

// A request that carries a workout under the id of a ryft-session workout, and deletes
// that id, leaves the row as it was: neither overwritten by the phone's copy nor deleted.
func TestRyftSessionWorkoutSurvivesAnUploadAndDeletionOfItsID(t *testing.T) {
	e := newDelEnv(t)
	at := base()
	x := uuid.New()
	if _, err := e.st.UpsertWorkouts(context.Background(), e.user.ID, []model.Workout{
		{ID: x, Activity: "strength", Start: at, End: at.Add(time.Hour), Source: "ryft-session"}}); err != nil {
		t.Fatal(err)
	}
	rec, out := send(e.srv, part{workouts: []map[string]any{recWorkout(x, at.Add(time.Minute), "Phone")}, deleted: []map[string]any{del("workout", x)}}.body())
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if out.Deleted.Workouts != 0 || out.Ignored.Workouts.UnknownID != 1 {
		t.Errorf("not deletable: deleted %+v, unknown_id %d", out.Deleted, out.Ignored.Workouts.UnknownID)
	}
	if e.count(`SELECT count(*) FROM workouts WHERE user_id = $1 AND id = $2 AND source = 'ryft-session' AND activity = 'strength' AND start_at = $3`,
		e.user.ID, x, at) != 1 {
		t.Error("the ryft-session row must stay as it was (source, activity, start)")
	}
}
