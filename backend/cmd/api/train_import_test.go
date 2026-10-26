package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// ---- without a database ----------------------------------------------------------------

// ImportSession is Session without origin and import_source (D125): the same JSON names
// otherwise, so a field added to Session cannot be missing from the import, or slip in
// as one the endpoint decides.
func TestImportSessionMirrorsSession(t *testing.T) {
	names := func(v any) []string {
		var out []string
		rt := reflect.TypeOf(v)
		for k := 0; k < rt.NumField(); k++ {
			name, _, _ := strings.Cut(rt.Field(k).Tag.Get("json"), ",")
			if name != "-" { // server-only (imported_at): in neither JSON shape
				out = append(out, name)
			}
		}
		slices.Sort(out)
		return out
	}
	want := slices.DeleteFunc(names(model.Session{}), func(n string) bool { return n == "origin" || n == "import_source" })
	if got := names(model.ImportSession{}); !slices.Equal(got, want) {
		t.Fatalf("ImportSession fields %v, want Session's without origin and import_source %v", got, want)
	}
	if len(want) != len(names(model.Session{}))-2 {
		t.Fatal("Session has origin and import_source")
	}
	// ToSession copies every field the request determines.
	end := time.Now()
	in := model.ImportSession{ID: uuid.New(), Title: "t", StartedAt: end.Add(-time.Hour), EndedAt: &end, Status: "completed",
		PlanDayID: ptrTo(uuid.New()), Notes: "n", BodyweightKG: ptrTo(80.0), DeviceID: ptrTo(uuid.New()),
		Exercises: []model.SessionExercise{{ID: uuid.New()}}, WorkoutID: ptrTo(uuid.New()), MuscularTRIMP: ptrTo(1.0)}
	s := in.ToSession()
	if s.ID != in.ID || s.Title != in.Title || !s.StartedAt.Equal(in.StartedAt) || s.EndedAt != in.EndedAt || s.Status != in.Status ||
		s.PlanDayID != in.PlanDayID || s.Notes != in.Notes || s.BodyweightKG != in.BodyweightKG || s.DeviceID != in.DeviceID ||
		len(s.Exercises) != 1 || s.WorkoutID != nil || s.MuscularTRIMP != nil || s.Origin != "" || s.ImportSource != nil {
		t.Fatalf("ToSession: %+v", s)
	}
}

// The window the import queues is scoring's range (train cannot import scoring).
func TestImportWindowIsScoringsRange(t *testing.T) {
	if train.ImportWindowDays != scoring.MaxRangeDays {
		t.Fatalf("train.ImportWindowDays %d, scoring.MaxRangeDays %d", train.ImportWindowDays, scoring.MaxRangeDays)
	}
}

// The route as main mounts it: authenticated, rate-limited once, behind the invite gate,
// and with its own 50 s instead of readTimeout (D129).
func TestImportRouteBudgetAndGates(t *testing.T) {
	var deadline time.Time
	var tokens int
	probe := func(w http.ResponseWriter, r *http.Request) {
		deadline, _ = r.Context().Deadline()
		w.WriteHeader(http.StatusNoContent)
	}
	user := uuid.New()
	o := auth.Options{Log: quietLog(), Verifier: auth.NewAPIKey(benchKey, user),
		Limit: func(*http.Request, auth.Principal) (string, bool, time.Duration) { tokens++; return "default", true, 0 },
		OnAuth: auth.LoadUser(func(context.Context, auth.Principal) (model.User, error) {
			return model.User{ID: user, TZ: "UTC"}, nil
		})}
	call := func(mode config.Mode, token string) int {
		req, _ := http.NewRequest(http.MethodPost, "/v1/train/import", strings.NewReader("{}"))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		importRoute(o, mode, probe).ServeHTTP(rec, req)
		return rec.Code
	}
	start := time.Now()
	if code := call(config.ModeOff, benchKey); code != http.StatusNoContent {
		t.Fatalf("served: %d", code)
	}
	if left := deadline.Sub(start); left < importTimeout-time.Second || left > importTimeout+time.Second {
		t.Fatalf("the import's budget is %v, got %v", importTimeout, left)
	}
	// The finish runs inside the budget (the items stop importFinishTimeout before it); the
	// lease's release has its own 5 s after it: all within the server's 60 s write timeout.
	if importTimeout <= readTimeout || importTimeout+5*time.Second >= 60*time.Second || importFinishTimeout >= importTimeout {
		t.Fatal("the budget must exceed readTimeout, and it and the release fit the server's 60 s")
	}
	if store.ImportLeaseTTL <= importTimeout+5*time.Second {
		t.Fatalf("the lease (%v) must outlast the budget and the release", store.ImportLeaseTTL)
	}
	if tokens != 1 {
		t.Fatalf("one rate-limit token per request, took %d", tokens)
	}
	if code := call(config.ModeOff, ""); code != http.StatusUnauthorized {
		t.Fatalf("without a token: %d", code)
	}
	if code := call(config.ModeOn, benchKey); code != http.StatusForbidden {
		t.Fatalf("the invite gate: %d", code)
	}
}

// ---- with a database ---------------------------------------------------------------------

// The contract's request (docs/API.md → POST /v1/train/import), read from API.md and moved
// to a recent day: the plan, then the session that names its day, created; the day
// queued with one outbox event (reason import, no session); the answer's shape.
func TestTrainImportContractExample(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("Europe/Berlin", true)
	example := contractRequest(t)
	d := day(daysAgo(20))
	example = strings.ReplaceAll(example, "2026-06-08T", d+"T")
	// The app creates its custom exercises first: the example's second one is an assisted
	// bodyweight exercise.
	b.want(b.call(token, http.MethodPost, "/v1/exercises", map[string]any{"id": "9b0e4c52-6d1a-4f0e-8a77-2c5d9e13b8f4",
		"name": "Assisted pull-up", "kind": "machine", "primary_muscles": []string{"lats"}, "load_type": "bodyweight"}), http.StatusCreated, nil)

	rec := b.call(token, http.MethodPost, "/v1/train/import", example)
	var raw map[string]any
	b.want(rec, http.StatusOK, &raw)
	if len(raw) != 3 || raw["results"] == nil || raw["days_touched"] == nil || raw["scoring"] == nil {
		t.Fatalf("TrainImportResponse: %s", rec.Body)
	}
	var res model.TrainImportResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if got := statuses(res); !slices.Equal(got, []string{"plan created", "session created"}) ||
		res.Results[0].ID.String() != "6a1f3c7e-1b52-5d0a-9c34-7e2b8d4f1a09" || res.Results[1].ID.String() != "3d8c5a21-7e4f-5b96-a0d3-c18f2e6b9047" {
		t.Fatalf("results: %v %+v", got, res.Results)
	}
	if strings.Contains(rec.Body.String(), `"code"`) || strings.Contains(rec.Body.String(), `"message"`) {
		t.Fatalf("code and message only with rejected: %s", rec.Body)
	}
	if !slices.Equal(res.DaysTouched, []string{d}) || !slices.Equal(res.Scoring.QueuedDays, res.DaysTouched) ||
		res.Scoring.ExpectedWithinSec < store.DefaultExpectedSec || res.Scoring.ExpectedWithinSec > 300 {
		t.Fatalf("days and scoring: %+v %+v", res.DaysTouched, res.Scoring)
	}
	if evs := b.outbox(user); len(evs) != 1 || evs[0].Reason != model.ReasonImport || !slices.Equal(evs[0].Days, []string{d}) ||
		evs[0].UserID != user || evs[0].RequestID == "" || evs[0].SessionID != nil {
		t.Fatalf("one ingested event, reason import: %+v", evs)
	}
	if p := b.pending(user); !slices.Equal(p, []string{d}) {
		t.Fatalf("pending: %v", p)
	}
	var s model.Session
	b.want(b.call(token, http.MethodGet, "/v1/sessions/3d8c5a21-7e4f-5b96-a0d3-c18f2e6b9047", nil), http.StatusOK, &s)
	if s.Origin != model.OriginImport || s.ImportSource == nil || *s.ImportSource != "alpha_progression" || s.Status != model.SessionCompleted ||
		s.MuscularTRIMP != nil || s.WorkoutID != nil || len(s.Exercises) != 2 || s.Exercises[1].Sets[0].AssistKG == nil ||
		*s.Exercises[1].Sets[0].AssistKG != 20 || s.PlanDayID == nil || s.PlanDayID.String() != "0c5e9b2a-3d71-5f48-a6b0-91c4e7d2f385" {
		t.Fatalf("stored: %+v", s)
	}
	var list []model.SessionSummary
	b.want(b.call(token, http.MethodGet, "/v1/sessions?from="+d+"&to="+d, nil), http.StatusOK, &list)
	if len(list) != 1 || list[0].Origin != model.OriginImport || *list[0].ImportSource != "alpha_progression" {
		t.Fatalf("summary: %+v", list)
	}
	if bus := b.bus; len(bus.ingested)+len(bus.completed) != 0 {
		t.Fatalf("nothing published around the outbox: %+v", bus)
	}

	// The same again while the day is still pending: unchanged, the day in the answer and
	// the event again (an earlier attempt's day that scoring has not finished).
	again := b.imports(token, example)
	if got := statuses(again); !slices.Equal(got, []string{"plan unchanged", "session unchanged"}) || !slices.Equal(again.DaysTouched, []string{d}) {
		t.Fatalf("resend while pending: %v %v", got, again.DaysTouched)
	}
	// Scored: the same again is nothing at all.
	b.scored(user, d)
	events := len(b.outbox(user))
	again = b.imports(token, example)
	if got := statuses(again); !slices.Equal(got, []string{"plan unchanged", "session unchanged"}) || len(again.DaysTouched) != 0 ||
		again.DaysTouched == nil || len(again.Scoring.QueuedDays) != 0 || len(b.outbox(user)) != events || len(b.pending(user)) != 0 {
		t.Fatalf("an idempotent resend writes nothing: %v %+v", got, again)
	}
}

// contractRequest is the TrainImportRequest example of docs/API.md.
func contractRequest(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../../../docs/API.md")
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(doc), "Request `TrainImportRequest`:")
	if !ok {
		t.Fatal("API.md has no TrainImportRequest example")
	}
	_, rest, _ = strings.Cut(rest, "```json\n")
	example, _, ok := strings.Cut(rest, "```")
	if !ok || !strings.Contains(example, `"source": "alpha_progression"`) {
		t.Fatalf("the example: %q", example)
	}
	return example
}

// One bad item does not fail the others: rejections by validation (invalid), by the
// store (unknown_exercise, unknown_plan_reference for a plan rejected in the same
// request), and the good items stored; results in request order; messages without values.
func TestTrainImportPartialSuccess(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("Europe/Berlin", true)
	good, bad := twoDayPlan(), twoDayPlan()
	bad.Goal = "fun"
	newest, unknown, refBad := done(daysAgo(3), squatID, 100, 105), done(daysAgo(10), uuid.New(), 50), done(daysAgo(9), squatID, 90)
	refBad.PlanDayID = &bad.Days[0].ID
	notDone, assisted := done(daysAgo(8), squatID, 95), done(daysAgo(7), squatID, 95)
	notDone.Exercises[0].Sets[0].CompletedAt = nil
	assisted.Exercises[0].Sets[0].AssistKG = ptrTo(17.5)
	planned := done(daysAgo(5), squatID, 100)
	planned.PlanDayID = &good.Days[1].ID
	planned.Exercises[0].PlanExerciseID = &good.Days[1].Exercises[0].ID
	for _, s := range []*model.ImportSession{&newest, &unknown, &refBad, &notDone, &assisted, &planned} {
		s.Title, s.Notes = "Leg day with Anna", "knee felt off"
	}

	res := b.imports(token, request([]model.Plan{good, bad}, newest, unknown, refBad, notDone, assisted, planned))
	want := []string{"plan created", "plan rejected invalid", "session created", "session rejected unknown_exercise",
		"session rejected unknown_plan_reference", "session rejected invalid", "session rejected invalid", "session created"}
	if got := statuses(res); !slices.Equal(got, want) {
		t.Fatalf("results %v, want %v", got, want)
	}
	for k, x := range res.Results {
		ids := []uuid.UUID{good.ID, bad.ID, newest.ID, unknown.ID, refBad.ID, notDone.ID, assisted.ID, planned.ID}
		if x.ID != ids[k] {
			t.Fatalf("result %d is %s, want %s", k, x.ID, ids[k])
		}
		if x.Status == store.ImportRejected && (x.Message == "" || strings.Contains(x.Message, "Anna") ||
			strings.Contains(x.Message, "knee") || strings.Contains(x.Message, "17.5") || strings.Contains(x.Message, "fun")) {
			t.Errorf("message of %s: %q", x.ID, x.Message)
		}
	}
	if !strings.Contains(res.Results[3].Message, unknown.Exercises[0].ExerciseID.String()) ||
		!strings.Contains(res.Results[5].Message, "completed_at") || !strings.Contains(res.Results[6].Message, "assist_kg") ||
		!strings.Contains(res.Results[1].Message, "goal") {
		t.Fatalf("messages name the id or the field: %+v", res.Results)
	}
	wantDays := []string{day(daysAgo(5)), day(daysAgo(3))}
	if !slices.Equal(res.DaysTouched, wantDays) || !slices.Equal(b.pending(user), wantDays) {
		t.Fatalf("days %v, pending %v, want %v", res.DaysTouched, b.pending(user), wantDays)
	}
	if n := b.count(`SELECT count(*) FROM sessions WHERE user_id=$1`, user); n != 2 {
		t.Fatalf("two sessions stored, %d", n)
	}
	if n := b.count(`SELECT count(*) FROM plans WHERE user_id=$1`, user); n != 1 {
		t.Fatalf("one plan stored, %d", n)
	}
	// Every item rejected is still a 200, with nothing queued and no event.
	events := len(b.outbox(user))
	res = b.imports(token, request([]model.Plan{bad}, unknown))
	if got := statuses(res); !slices.Equal(got, []string{"plan rejected invalid", "session rejected unknown_exercise"}) ||
		len(res.DaysTouched) != 0 || len(b.outbox(user)) != events {
		t.Fatalf("all rejected: %v %+v", got, res)
	}
}

// Sessions are stored oldest first whatever their order in the array (T5): each item
// commits in turn, so the transaction ids show the order.
func TestTrainImportStoresOldestFirst(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	a, c, mid := done(daysAgo(30), squatID, 100), done(daysAgo(10), squatID, 110), done(daysAgo(20), squatID, 105)
	res := b.imports(token, request(nil, c, a, mid))
	if got := statuses(res); !slices.Equal(got, []string{"session created", "session created", "session created"}) ||
		res.Results[0].ID != c.ID || res.Results[1].ID != a.ID {
		t.Fatalf("results keep the request's order: %v", res.Results)
	}
	rows, err := b.pool.Query(context.Background(), `SELECT id FROM sessions WHERE user_id=$1 ORDER BY xmin::text::bigint`, user)
	if err != nil {
		t.Fatal(err)
	}
	order, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil || !slices.Equal(order, []uuid.UUID{a.ID, mid.ID, c.ID}) {
		t.Fatalf("stored in the order %v, want oldest first %v %v", order, []uuid.UUID{a.ID, mid.ID, c.ID}, err)
	}
	// The records are those of all the history, the heaviest set's.
	prs, err := b.st.ExercisePRs(context.Background(), user, squatID)
	if err != nil || len(prs) == 0 {
		t.Fatalf("records: %v %v", prs, err)
	}
	for _, p := range prs {
		if p.Kind == model.PRMaxWeight && (p.Value != 110 || p.SessionID != c.ID) {
			t.Fatalf("max weight from the newest, heaviest session: %+v", p)
		}
	}
}

// Changed content is updated and replaced whole (sets the request leaves out are gone);
// the day it moved from is rescored too; unchanged after that.
func TestTrainImportUpdates(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	s := done(daysAgo(12), squatID, 100, 105, 110)
	first := b.imports(token, request(nil, s))
	b.scored(user, first.DaysTouched...)

	edited := s
	edited.Exercises = []model.SessionExercise{s.Exercises[0]}
	edited.Exercises[0].Sets = slices.Clone(s.Exercises[0].Sets[:2])
	reps := 6
	edited.Exercises[0].Sets[1].Reps = &reps
	res := b.imports(token, request(nil, edited))
	if got := statuses(res); !slices.Equal(got, []string{"session updated"}) || !slices.Equal(res.DaysTouched, []string{day(daysAgo(12))}) {
		t.Fatalf("updated: %v %v", got, res.DaysTouched)
	}
	var stored model.Session
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+s.ID.String(), nil), http.StatusOK, &stored)
	if len(stored.Exercises[0].Sets) != 2 || *stored.Exercises[0].Sets[1].Reps != 6 || stored.Origin != model.OriginImport {
		t.Fatalf("replaced whole: %+v", stored.Exercises[0].Sets)
	}
	b.scored(user, res.DaysTouched...)
	// Moved two days later: both days are rescored.
	moved := edited
	moved.StartedAt = moved.StartedAt.AddDate(0, 0, 2)
	end := moved.StartedAt.Add(time.Hour)
	moved.EndedAt = &end
	moved.Exercises = []model.SessionExercise{edited.Exercises[0]}
	moved.Exercises[0].Sets = slices.Clone(edited.Exercises[0].Sets)
	for k := range moved.Exercises[0].Sets { // the sets move with it (they stay within an hour of it)
		at := moved.Exercises[0].Sets[k].CompletedAt.AddDate(0, 0, 2)
		moved.Exercises[0].Sets[k].CompletedAt, moved.Exercises[0].Sets[k].UpdatedAt = &at, at
	}
	res = b.imports(token, request(nil, moved))
	if got := statuses(res); !slices.Equal(got, []string{"session updated"}) ||
		!slices.Equal(res.DaysTouched, []string{day(daysAgo(12)), day(daysAgo(10))}) {
		t.Fatalf("moved: %v %v", got, res.DaysTouched)
	}
	b.scored(user, res.DaysTouched...)
	if res = b.imports(token, request(nil, moved)); !slices.Equal(statuses(res), []string{"session unchanged"}) || len(res.DaysTouched) != 0 {
		t.Fatalf("then unchanged: %+v", res)
	}
}

// The days of the user's other sessions after the oldest imported one, with one of its
// exercises, are rescored too (their load depends on the history); others are not.
func TestTrainImportQueuesDependentDays(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	later, unrelated, before := done(daysAgo(5), squatID, 120), done(daysAgo(4), benchID, 80), done(daysAgo(40), squatID, 90)
	b.scored(user, b.imports(token, request(nil, later, unrelated, before)).DaysTouched...)

	res := b.imports(token, request(nil, done(daysAgo(20), squatID, 100)))
	if want := []string{day(daysAgo(20)), day(daysAgo(5))}; !slices.Equal(res.DaysTouched, want) {
		t.Fatalf("days %v, want the import's and the later squat session's %v", res.DaysTouched, want)
	}
	if evs := b.outbox(user); !slices.Equal(evs[len(evs)-1].Days, res.DaysTouched) {
		t.Fatalf("the event names the same days: %+v", evs[len(evs)-1])
	}
}

// A request that stopped after an item (its own transaction queued its day, the finish
// never ran), then the full request again: the stored item is unchanged, its pending day
// and the days that depend on it are in the answer and in the one event.
func TestTrainImportResumesAnInterruptedRequest(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	later := done(daysAgo(3), squatID, 130)
	b.scored(user, b.imports(token, request(nil, later)).DaysTouched...)
	events := len(b.outbox(user))

	first, second := done(daysAgo(30), squatID, 100), done(daysAgo(25), squatID, 105)
	ctx := context.Background()
	from, to := train.ImportWindow(time.Now(), time.UTC)
	out, err := b.st.ImportSession(ctx, user, model.ImportSourceAlphaProgression, first.ToSession(), time.UTC, store.DayWindow{From: from, To: to})
	if err != nil || out.Status != store.ImportCreated {
		t.Fatalf("the interrupted attempt's one item: %+v %v", out, err)
	}
	if len(b.outbox(user)) != events {
		t.Fatal("an item alone writes no event")
	}
	res := b.imports(token, request(nil, first, second))
	if got := statuses(res); !slices.Equal(got, []string{"session unchanged", "session created"}) {
		t.Fatalf("resent: %v", got)
	}
	want := []string{day(daysAgo(30)), day(daysAgo(25)), day(daysAgo(3))}
	if !slices.Equal(res.DaysTouched, want) {
		t.Fatalf("days %v, want the pending one, the new one and the dependent one %v", res.DaysTouched, want)
	}
	if evs := b.outbox(user); len(evs) != events+1 || !slices.Equal(evs[len(evs)-1].Days, want) {
		t.Fatalf("one event for the request: %+v", evs)
	}
}

// Whole-request refusals: nothing is processed, the lease is not taken, nothing stored.
func TestTrainImportRefusals(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	s := done(daysAgo(2), squatID, 100)
	raw, _ := json.Marshal(request(nil, s))
	withField := func(field string) string {
		return strings.Replace(string(raw), `"title":`, `"`+field+`":"import","title":`, 1)
	}
	many := func(n int) []model.ImportSession {
		out := make([]model.ImportSession, n)
		for k := range out {
			out[k] = done(daysAgo(2+k), squatID, 100)
		}
		return out
	}
	// heavy is n sessions of k sets each (N1: at most 300 a session, 2500 a request).
	heavy := func(n, k int) []model.ImportSession {
		out := make([]model.ImportSession, n)
		for i := range out {
			kgs := make([]float64, k)
			for j := range kgs {
				kgs[j] = 60
			}
			out[i] = done(daysAgo(30+i), squatID, kgs...)
		}
		return out
	}
	plans := make([]model.Plan, 6)
	for k := range plans {
		plans[k] = twoDayPlan()
	}
	dup := twoDayPlan()
	over := `{"source":"alpha_progression","plans":[],"sessions":[],"pad":"` + strings.Repeat("x", 1<<20) + `"}`
	for _, tc := range []struct {
		name   string
		body   any
		status int
		msg    string
	}{
		{"origin on a session", withField("origin"), http.StatusBadRequest, "unknown field"},
		{"import_source on a session", withField("import_source"), http.StatusBadRequest, "unknown field"},
		{"unknown field", `{"source":"alpha_progression","extra":1}`, http.StatusBadRequest, "unknown field"},
		{"malformed", `{"source":`, http.StatusBadRequest, "bad json"},
		{"over 1 MiB", over, http.StatusRequestEntityTooLarge, "request body over"},
		{"unknown source", strings.Replace(string(raw), "alpha_progression", "other_app", 1), http.StatusUnprocessableEntity, "source must be one of"},
		{"no source", `{"plans":[],"sessions":[]}`, http.StatusUnprocessableEntity, "source must be one of"},
		{"6 plans", request(plans), http.StatusUnprocessableEntity, "at most 5 plans"},
		{"51 sessions", request(nil, many(51)...), http.StatusUnprocessableEntity, "at most 50 sessions"},
		{"a plan twice", request([]model.Plan{dup, dup}), http.StatusUnprocessableEntity, "in the request twice"},
		{"a session twice", request(nil, s, s), http.StatusUnprocessableEntity, "in the request twice"},
		{"a NUL", strings.Replace(string(raw), `"title":"Lower"`, `"title":"Lo\u0000wer"`, 1), http.StatusUnprocessableEntity, "NUL"},
		{"over 2500 sets", request(nil, heavy(9, 280)...), http.StatusUnprocessableEntity, "at most 2500 sets per request"},
	} {
		rec := b.call(token, http.MethodPost, "/v1/train/import", tc.body)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.msg) || !strings.Contains(rec.Body.String(), `"request_id"`) {
			t.Errorf("%s: %d %s, want %d %q", tc.name, rec.Code, rec.Body, tc.status, tc.msg)
		}
	}
	if n := b.count(`SELECT count(*) FROM sessions WHERE user_id=$1`, user) + b.count(`SELECT count(*) FROM plans WHERE user_id=$1`, user) +
		b.count(`SELECT count(*) FROM outbox WHERE user_id=$1`, user) + b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatalf("a refused request stores nothing and takes no lease: %d rows", n)
	}
	// The limits themselves are accepted; an empty request is a 200 with nothing.
	if res := b.imports(token, request(nil, many(50)...)); len(res.Results) != 50 {
		t.Fatalf("50 sessions: %d results", len(res.Results))
	}
	empty := b.call(token, http.MethodPost, "/v1/train/import", `{"source":"alpha_progression"}`)
	var res model.TrainImportResponse
	b.want(empty, http.StatusOK, &res)
	if !strings.HasPrefix(empty.Body.String(), `{"results":[],"days_touched":[],"scoring":{"queued_days":[],"expected_within_sec":`) {
		t.Fatalf("empty: %s", empty.Body)
	}
}

// The gates: invite (the api's, every route) and consent (D129, as a sync): 403 with the
// code under on, served with X-RYFT-Gate under warn; checked before the body is read.
func TestTrainImportGates(t *testing.T) {
	body := request(nil, done(daysAgo(2), squatID, 100))
	for _, tc := range []struct {
		name    string
		beta    config.Beta
		consent bool
		status  int
		code    string
		header  string
	}{
		{"consent on", defaultBeta(), false, http.StatusForbidden, model.CodeConsentRequired, ""},
		// On this route warn is on (S3): no app build without the consent screen calls it.
		{"consent warn acts as on", config.Beta{Gate: config.ModeOff, Consent: config.ModeWarn, PrivacyPolicyVersion: benchPolicy}, false,
			http.StatusForbidden, model.CodeConsentRequired, ""},
		{"consent off", config.Beta{Gate: config.ModeOff, Consent: config.ModeOff, PrivacyPolicyVersion: benchPolicy}, false, http.StatusOK, "", ""},
		{"invite on", config.Beta{Gate: config.ModeOn, Consent: config.ModeOn, PrivacyPolicyVersion: benchPolicy}, true,
			http.StatusForbidden, model.CodeInviteRequired, ""},
		{"invite warn", config.Beta{Gate: config.ModeWarn, Consent: config.ModeOn, PrivacyPolicyVersion: benchPolicy}, true,
			http.StatusOK, "", model.CodeInviteRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBench(t, tc.beta)
			user, token := b.user("UTC", tc.consent)
			mode := tc.beta.Consent
			if mode == config.ModeWarn {
				mode = config.ModeOn
			}
			refused := testutil.ToFloat64(metrics.GateRefusals.WithLabelValues(model.CodeConsentRequired, string(mode), "api"))
			rec := b.call(token, http.MethodPost, "/v1/train/import", body)
			if rec.Code != tc.status || rec.Header().Get("X-RYFT-Gate") != tc.header {
				t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("X-RYFT-Gate"), rec.Body)
			}
			if tc.code != "" {
				var e map[string]string
				_ = json.Unmarshal(rec.Body.Bytes(), &e)
				if e["code"] != tc.code || b.count(`SELECT count(*) FROM sessions WHERE user_id=$1`, user) != 0 {
					t.Fatalf("refused with %v, nothing stored", e)
				}
			}
			after := testutil.ToFloat64(metrics.GateRefusals.WithLabelValues(model.CodeConsentRequired, string(mode), "api"))
			if consentGate := !tc.consent && tc.beta.Consent != config.ModeOff; (after-refused == 1) != consentGate {
				t.Fatalf("consent refusals counted %v", after-refused)
			}
			checkImportLog(t, b.logs.String(), []string{"Lower"}, nil) // the 403s' lines too (N5)
		})
	}
	// A refused import never reads the body: a malformed one is still the 403.
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", false)
	if rec := b.call(token, http.MethodPost, "/v1/train/import", `{"source":`); rec.Code != http.StatusForbidden {
		t.Fatalf("consent before the body: %d", rec.Code)
	}
}

// One import per user at a time (D129): a second one while the first runs is 429
// import_in_progress, Retry-After 10, and none of its items is processed; the first
// completes; another account is not affected.
func TestTrainImportOneAtATime(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	_, other := b.user("UTC", true)
	first := done(daysAgo(6), squatID, 100)
	// Hold the session's lock (a live PUT of it would) so that the first import waits in its item.
	ctx := context.Background()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`, user, first.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var firstRes model.TrainImportResponse
	var firstCode int
	wg.Add(1)
	go func() {
		defer wg.Done()
		rec := b.call(token, http.MethodPost, "/v1/train/import", request(nil, first))
		firstCode = rec.Code
		_ = json.Unmarshal(rec.Body.Bytes(), &firstRes)
	}()
	for k := 0; b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until > now()`, user) == 0; k++ {
		if k > 200 {
			t.Fatal("the first import never took the lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	busy := testutil.ToFloat64(metrics.TrainImports.WithLabelValues("busy"))
	second := done(daysAgo(5), squatID, 105)
	rec := b.call(token, http.MethodPost, "/v1/train/import", request(nil, second))
	var e map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "10" || e["code"] != model.CodeImportInProgress ||
		e["request_id"] == "" || e["error"] != "an import is already running" {
		t.Fatalf("second import: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if testutil.ToFloat64(metrics.TrainImports.WithLabelValues("busy")) != busy+1 {
		t.Fatal("busy counted")
	}
	if res := b.imports(other, request(nil, done(daysAgo(5), squatID, 105))); len(res.Results) != 1 || res.Results[0].Status != "created" {
		t.Fatalf("another account imports meanwhile: %+v", res)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if firstCode != http.StatusOK || !slices.Equal(statuses(firstRes), []string{"session created"}) {
		t.Fatalf("the first completes: %d %+v", firstCode, firstRes)
	}
	if n := b.count(`SELECT count(*) FROM sessions WHERE user_id=$1 AND id=$2`, user, second.ID); n != 0 {
		t.Fatal("the refused import stored its session")
	}
	if n := b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatal("the lease is given back")
	}
	// Free again: the second one goes through.
	if res := b.imports(token, request(nil, second)); !slices.Equal(statuses(res), []string{"session created"}) {
		t.Fatalf("after the first: %+v", res)
	}
	// The 429's access line carries its code; no line holds a title (N5).
	lines := checkImportLog(t, b.logs.String(), []string{"Lower"}, nil)
	if !slices.ContainsFunc(lines["http"], func(l map[string]any) bool {
		return l["status"] == 429.0 && l["code"] == model.CodeImportInProgress
	}) {
		t.Fatalf("no 429 line with its code: %v", lines["http"])
	}
}

// Out of time: the items stop before the budget ends, what was stored stays stored and is
// finished (queued, one event), and the answer is 503 "request timed out" with Retry-After.
func TestTrainImportOutOfTime(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	p, stuck := twoDayPlan(), done(daysAgo(4), squatID, 100)
	early := done(daysAgo(9), squatID, 95)
	ctx := context.Background()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`, user, stuck.ID); err != nil {
		t.Fatal(err)
	}
	// The route's chain with a budget of the finish's time plus 5 s for the items: enough for
	// the plan and the early session on a loaded machine; the stuck one waits past it anyway.
	srv := withBudget(b, importFinishTimeout+5*time.Second)
	timeouts := testutil.ToFloat64(metrics.TrainImports.WithLabelValues("timeout"))
	rec := callHandler(srv, token, request([]model.Plan{p}, stuck, early))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" ||
		!strings.Contains(rec.Body.String(), `"error":"request timed out"`) || strings.Contains(rec.Body.String(), "results") {
		t.Fatalf("out of time: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if testutil.ToFloat64(metrics.TrainImports.WithLabelValues("timeout")) != timeouts+1 {
		t.Fatal("timeout counted")
	}
	if b.count(`SELECT count(*) FROM plans WHERE id=$1`, p.ID) != 1 || b.count(`SELECT count(*) FROM sessions WHERE id=$1`, early.ID) != 1 ||
		b.count(`SELECT count(*) FROM sessions WHERE id=$1`, stuck.ID) != 0 {
		t.Fatal("the items before the stop stay stored")
	}
	if evs := b.outbox(user); len(evs) != 1 || !slices.Equal(evs[0].Days, []string{day(daysAgo(9))}) {
		t.Fatalf("the finish ran for what is stored: %+v", evs)
	}
	if n := b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatal("the lease is given back")
	}
	// The timeout's lines: request failed 503, train import result timeout, no title (N5).
	lines := checkImportLog(t, b.logs.String(), []string{"Lower", "Upper / Lower"}, nil)
	if imp := lines["train import"]; len(imp) != 1 || imp[0]["result"] != "timeout" || len(lines["request failed"]) != 1 {
		t.Fatalf("lines: %v", lines)
	}
}

// withBudget is importRoute's chain with another budget.
func withBudget(b *bench, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		b.srv.ServeHTTP(w, r.WithContext(ctx))
	})
}

func callHandler(h http.Handler, token string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, "/v1/train/import", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Sessions older than the 400 days ending today are stored for history and records, but
// not scored: created, no day, no queue row, no load.
func TestTrainImportOlderThanTheWindow(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	old, recent := done(daysAgo(450), squatID, 140), done(daysAgo(399), squatID, 100)
	res := b.imports(token, request(nil, old, recent))
	if got := statuses(res); !slices.Equal(got, []string{"session created", "session created"}) ||
		!slices.Equal(res.DaysTouched, []string{day(daysAgo(399))}) {
		t.Fatalf("results %v, days %v", got, res.DaysTouched)
	}
	if n := b.count(`SELECT count(*) FROM score_queue WHERE user_id=$1 AND day=$2::date`, user, day(daysAgo(450))); n != 0 {
		t.Fatal("an old day is not queued")
	}
	var s model.Session
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+old.ID.String(), nil), http.StatusOK, &s)
	if s.MuscularTRIMP != nil || s.WorkoutID != nil || s.Origin != model.OriginImport {
		t.Fatalf("stored, not scored: %+v", s)
	}
	prs, _ := b.st.ExercisePRs(context.Background(), user, squatID)
	if !slices.ContainsFunc(prs, func(p model.PR) bool { return p.Kind == model.PRMaxWeight && p.Value == 140 && p.SessionID == old.ID }) {
		t.Fatalf("its records count: %+v", prs)
	}
}

// Importing moves no enrolment (T6), even with the active plan's day; the history,
// last_time and the plan's targets see the imported sets.
func TestTrainImportFeedsHistoryNotTheEnrollment(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	p := twoDayPlan()
	b.want(b.call(token, http.MethodPut, "/v1/plans/"+p.ID.String(), p), http.StatusCreated, nil)
	var e model.Enrollment
	b.want(b.call(token, http.MethodPost, "/v1/plans/"+p.ID.String()+"/enroll", nil), http.StatusCreated, &e)
	s := done(daysAgo(2), squatID, 100, 107.5)
	s.PlanDayID = &p.Days[0].ID
	s.Exercises[0].PlanExerciseID = &p.Days[0].Exercises[0].ID
	b.imports(token, request(nil, s))

	var today model.PlanToday
	b.want(b.call(token, http.MethodGet, "/v1/plans/today", nil), http.StatusOK, &today)
	if today.Enrollment.CurrentWeek != e.CurrentWeek || today.Enrollment.CurrentDayIndex != e.CurrentDayIndex {
		t.Fatalf("the enrolment moved: %+v → %+v", e, today.Enrollment)
	}
	if len(today.Targets) != 1 || len(today.Targets[0].LastTime) != 2 || *today.Targets[0].LastTime[1].WeightKG != 107.5 {
		t.Fatalf("last_time is the imported session's: %+v", today.Targets)
	}
	var hist model.ExerciseHistory
	b.want(b.call(token, http.MethodGet, "/v1/exercises/"+squatID.String()+"/history", nil), http.StatusOK, &hist)
	if len(hist.Sessions) != 1 || hist.Sessions[0].SessionID != s.ID || len(hist.E1RM) != 1 || len(hist.PRs) == 0 {
		t.Fatalf("history: %+v", hist)
	}
}

// The log has one line per import with counts, never a title, a note, a value or an id
// list (D114, D117); the metrics count the items by kind and status.
func TestTrainImportLogsCountsOnly(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	p := twoDayPlan()
	p.Name, p.Description = "Anna's 5x5", "from the gym's whiteboard"
	s := done(daysAgo(3), squatID, 123.25)
	s.Title, s.Notes = "Leg day with Anna", "knee felt off"
	bad := done(daysAgo(4), uuid.New(), 61.75)
	created := testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues("session", "created"))
	rejected := testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues("session", "rejected"))
	plans := testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues("plan", "created"))
	oks := testutil.ToFloat64(metrics.TrainImports.WithLabelValues("ok"))
	b.imports(token, request([]model.Plan{p}, s, bad))
	b.want(b.call(token, http.MethodPost, "/v1/train/import", `{"source":"other_app"}`), http.StatusUnprocessableEntity, nil)

	if testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues("session", "created")) != created+1 ||
		testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues("session", "rejected")) != rejected+1 ||
		testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues("plan", "created")) != plans+1 ||
		testutil.ToFloat64(metrics.TrainImports.WithLabelValues("ok")) != oks+1 {
		t.Fatal("items and imports counted")
	}
	lines := checkImportLog(t, b.logs.String(), []string{"Anna", "whiteboard", "knee", "other_app", s.ID.String(), bad.ID.String()},
		[]float64{123.25, 61.75})
	imp := lines["train import"]
	if len(imp) != 1 || imp[0]["source"] != nil || imp[0]["result"] != "ok" || imp[0]["plans"] != 1.0 ||
		imp[0]["sessions"] != 2.0 || imp[0]["created"] != 2.0 || imp[0]["rejected"] != 1.0 || imp[0]["days"] != 1.0 {
		t.Fatalf("one line with the counts: %v", imp)
	}
}
