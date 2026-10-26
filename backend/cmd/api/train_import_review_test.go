package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/train"
)

// The training import's review round (PR #59): the app's edits win over a resend (B1), ids
// per account (B2), the flag on the server (S4), the finish's order (SF1), the failure paths
// (SF3), tenancy (N3) and their log lines (N5).

// ---- B1: a resend never undoes what the app changed --------------------------------------

// The reviewer's probe: import, read, edit in the app (title, body weight, a set added, a set
// deleted), send the import again: newer_on_server, and nothing of the edit is reverted. A
// title alone counts the same (D126's former known limit).
func TestTrainImportKeepsTheAppsSessionEdits(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	s := done(daysAgo(6), squatID, 100, 105, 110)
	b.scored(user, b.imports(token, request(nil, s)).DaysTouched...)

	var stored map[string]any
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+s.ID.String(), nil), http.StatusOK, &stored)
	ex := stored["exercises"].([]any)[0].(map[string]any)
	sets := ex["sets"].([]any)
	deleted := sets[0].(map[string]any)["id"].(string)
	at := s.StartedAt.Add(40 * time.Minute)
	added := map[string]any{"id": uuid.NewString(), "set_index": 3, "reps": 3, "weight_kg": 115, "completed_at": at, "updated_at": at}
	ex["sets"] = append(slices.Clone(sets[1:]), added)
	stored["title"], stored["bodyweight_kg"] = "Lower (edited)", 83
	var edited model.Session
	b.want(b.call(token, http.MethodPut, "/v1/sessions/"+s.ID.String(), stored), http.StatusOK, &edited)

	res := b.imports(token, request(nil, s))
	if got := statuses(res); !slices.Equal(got, []string{"session rejected newer_on_server"}) || len(res.DaysTouched) != 0 {
		t.Fatalf("resend after the edit: %v %v", got, res.DaysTouched)
	}
	var after model.Session
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+s.ID.String(), nil), http.StatusOK, &after)
	var ids []string
	for _, st := range after.Exercises[0].Sets {
		ids = append(ids, st.ID.String())
	}
	if after.Title != "Lower (edited)" || after.BodyweightKG == nil || *after.BodyweightKG != 83 || len(ids) != 3 ||
		slices.Contains(ids, deleted) || !slices.Contains(ids, added["id"].(string)) || after.Origin != model.OriginImport {
		t.Fatalf("the edit was reverted: title %q bw %v sets %v", after.Title, after.BodyweightKG, ids)
	}

	// Only the title, nothing else: the same answer.
	other := done(daysAgo(4), squatID, 100)
	b.imports(token, request(nil, other))
	var body map[string]any
	b.want(b.call(token, http.MethodGet, "/v1/sessions/"+other.ID.String(), nil), http.StatusOK, &body)
	body["title"] = "Renamed"
	b.want(b.call(token, http.MethodPut, "/v1/sessions/"+other.ID.String(), body), http.StatusOK, nil)
	if got := statuses(b.imports(token, request(nil, other))); !slices.Equal(got, []string{"session rejected newer_on_server"}) {
		t.Fatalf("a renamed session: %v", got)
	}
	if b.want(b.call(token, http.MethodGet, "/v1/sessions/"+other.ID.String(), nil), http.StatusOK, &after); after.Title != "Renamed" {
		t.Fatalf("title reverted: %q", after.Title)
	}
}

// The same for plans, decided: an imported plan edited or archived in the app is never
// overwritten; neither is a plan the app made itself under that id.
func TestTrainImportKeepsTheAppsPlanEdits(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, token := b.user("UTC", true)
	p := twoDayPlan()
	if got := statuses(b.imports(token, request([]model.Plan{p}))); !slices.Equal(got, []string{"plan created"}) {
		t.Fatalf("import: %v", got)
	}
	if got := statuses(b.imports(token, request([]model.Plan{p}))); !slices.Equal(got, []string{"plan unchanged"}) {
		t.Fatalf("resend, untouched: %v", got)
	}
	changed := p
	changed.Name = "Changed in the file"
	if got := statuses(b.imports(token, request([]model.Plan{changed}))); !slices.Equal(got, []string{"plan updated"}) {
		t.Fatalf("a changed file, the plan untouched in the app: %v", got)
	}
	var stored model.Plan
	b.want(b.call(token, http.MethodGet, "/v1/plans/"+p.ID.String(), nil), http.StatusOK, &stored)
	stored.Name, stored.Archived = "Mine now", true
	b.want(b.call(token, http.MethodPut, "/v1/plans/"+p.ID.String(), stored), http.StatusOK, nil)
	for _, resend := range []model.Plan{p, changed} {
		res := b.imports(token, request([]model.Plan{resend}))
		if got := statuses(res); !slices.Equal(got, []string{"plan rejected newer_on_server"}) || res.Results[0].Message == "" {
			t.Fatalf("resend after the edit: %v %+v", got, res.Results)
		}
	}
	var after model.Plan
	b.want(b.call(token, http.MethodGet, "/v1/plans/"+p.ID.String(), nil), http.StatusOK, &after)
	if after.Name != "Mine now" || !after.Archived {
		t.Fatalf("the edit was reverted: %q archived %v", after.Name, after.Archived)
	}
	// Made in the app (PUT), then the same id imported: left alone.
	own := twoDayPlan()
	b.want(b.call(token, http.MethodPut, "/v1/plans/"+own.ID.String(), own), http.StatusCreated, nil)
	if got := statuses(b.imports(token, request([]model.Plan{own}))); !slices.Equal(got, []string{"plan rejected newer_on_server"}) {
		t.Fatalf("a plan made in the app: %v", got)
	}
}

// ---- B2: ids are per account -----------------------------------------------------------

// accountID is the app's derivation (API.md → POST /v1/train/import): UUIDv5 of the item's
// content under a namespace that is UUIDv5(the app's import namespace, the account's id), so
// the same file gives other ids in another account.
func accountID(user uuid.UUID, parts ...string) uuid.UUID {
	ns := uuid.NewSHA1(uuid.MustParse("8e1f2c7a-4b3d-5e60-9f81-2a3b4c5d6e7f"), []byte(user.String()))
	return uuid.NewSHA1(ns, []byte(strings.Join(parts, "\x1f")))
}

func accountPlan(user uuid.UUID) model.Plan {
	p := twoDayPlan()
	p.ID = accountID(user, "plan", p.Name)
	for k := range p.Days {
		p.Days[k].ID = accountID(user, "plan", p.Name, "day", p.Days[k].Name, string(rune('0'+k)))
		p.Days[k].Exercises[0].ID = accountID(user, "plan", p.Name, "day", string(rune('0'+k)), "exercise", "0")
	}
	return p
}

// Two accounts import the same file: different ids, both created. A real clash (another
// account's id sent by hand) is id_taken with words that say nothing about that account.
func TestTrainImportPlanIDsPerAccount(t *testing.T) {
	b := newBench(t, defaultBeta())
	a, tokenA := b.user("UTC", true)
	c, tokenC := b.user("UTC", true)
	pa, pc := accountPlan(a), accountPlan(c)
	if pa.ID == pc.ID || pa.Days[0].ID == pc.Days[0].ID {
		t.Fatal("the derivation must differ per account")
	}
	for _, x := range []struct {
		token string
		p     model.Plan
	}{{tokenA, pa}, {tokenC, pc}} {
		if got := statuses(b.imports(x.token, request([]model.Plan{x.p}))); !slices.Equal(got, []string{"plan created"}) {
			t.Fatalf("the same file in each account: %v", got)
		}
	}
	builtin := pa
	builtin.ID = train.BuiltinPlanID("full-body-3x")
	clash := b.imports(tokenC, request([]model.Plan{pa, builtin}))
	if got := statuses(clash); !slices.Equal(got, []string{"plan rejected id_taken", "plan rejected id_taken"}) {
		t.Fatalf("another account's id, a built-in's: %v", got)
	}
	if m := clash.Results[0].Message; m != clash.Results[1].Message || strings.Contains(m, "account") {
		t.Fatalf("id_taken must not tell another account's plan from a built-in: %q vs %q", m, clash.Results[1].Message)
	}
}

// ---- S4: the flag on the server; N5: the refusals' log lines ------------------------------

func TestTrainImportFlag(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	allowed, tokenAllowed := b.user("UTC", true)
	_, unconsented := b.user("UTC", false)
	b.flag(true, 0, allowed) // as shipped: dark, an allow-list
	body := request(nil, done(daysAgo(3), squatID, 101.25))
	disabled := testutil.ToFloat64(metrics.TrainImports.WithLabelValues("disabled"))
	for _, tc := range []struct {
		name, token string
		status      int
		code        string
	}{
		{"not on the allow-list", token, http.StatusForbidden, model.CodeImportNotEnabled},
		{"consent first", unconsented, http.StatusForbidden, model.CodeConsentRequired},
		{"on the allow-list", tokenAllowed, http.StatusOK, ""},
	} {
		rec := b.call(tc.token, http.MethodPost, "/v1/train/import", body)
		var e map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		if rec.Code != tc.status || (tc.code != "" && e["code"] != tc.code) {
			t.Fatalf("%s: %d %s", tc.name, rec.Code, rec.Body)
		}
	}
	b.flag(false, 100, allowed, user) // the kill switch: off for everyone, the allow-list too
	if rec := b.call(tokenAllowed, http.MethodPost, "/v1/train/import", body); rec.Code != http.StatusForbidden {
		t.Fatalf("flag off: %d", rec.Code)
	}
	if n := testutil.ToFloat64(metrics.TrainImports.WithLabelValues("disabled")) - disabled; n != 2 {
		t.Fatalf("counted %v as disabled, want 2", n)
	}
	if n := b.count(`SELECT count(*) FROM sessions WHERE user_id=$1`, user) +
		b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatal("a refused import stores nothing and takes no lease")
	}
	checkImportLog(t, b.logs.String(), []string{"Lower"}, []float64{101.25})
}

// ---- SF1: the finish queues first, then the records --------------------------------------

// A finish that fails after the items (the records step is held past the finish's time):
// 503 with Retry-After, and the days are already queued with their event, so nothing is
// stranded; the resend comes back unchanged with the pending days, and repairs the records.
func TestTrainImportFinishFailsAfterTheItems(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	b.h.importFinish = time.Second
	x, y := done(daysAgo(8), squatID, 100), done(daysAgo(5), squatID, 105)
	ctx := context.Background()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE personal_records IN SHARE MODE`); err != nil { // readers pass, writers wait
		t.Fatal(err)
	}
	rec := b.call(token, http.MethodPost, "/v1/train/import", request(nil, x, y))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("records held: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	days := []string{day(daysAgo(8)), day(daysAgo(5))}
	if evs := b.outbox(user); len(evs) != 1 || !slices.Equal(evs[0].Days, days) || !slices.Equal(b.pending(user), days) {
		t.Fatalf("the days are queued with their event before the records: %+v, pending %v", evs, b.pending(user))
	}
	if n := b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatal("the lease is given back")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	res := b.imports(token, request(nil, x, y))
	if got := statuses(res); !slices.Equal(got, []string{"session unchanged", "session unchanged"}) || !slices.Equal(res.DaysTouched, days) {
		t.Fatalf("resend: %v %v", got, res.DaysTouched)
	}
	if prs, err := b.st.ExercisePRs(ctx, user, squatID); err != nil || len(prs) == 0 {
		t.Fatalf("the resend repairs the records: %v %v", prs, err)
	}
}

// ---- SF3: the database failing, the client leaving ---------------------------------------

// A database error that is not the item's (here a trigger's internal error on one session)
// stops the request: 503 "the database is unavailable" with Retry-After, the items before it
// stored and finished, the lease given back, and one train import line with result error.
func TestTrainImportDatabaseFailure(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE FUNCTION ryft_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN IF NEW.title = 'boom' THEN RAISE EXCEPTION 'injected failure' USING ERRCODE = 'XX000'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER ryft_test_fail BEFORE INSERT OR UPDATE ON sessions FOR EACH ROW EXECUTE FUNCTION ryft_test_fail()`,
	} {
		if _, err := b.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	good, boom, never := done(daysAgo(9), squatID, 101.25), done(daysAgo(4), squatID, 103.75), done(daysAgo(2), squatID, 106.25)
	boom.Title = "boom"
	errors0 := testutil.ToFloat64(metrics.TrainImports.WithLabelValues("error"))
	rec := b.call(token, http.MethodPost, "/v1/train/import", request(nil, never, boom, good))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" ||
		!strings.Contains(rec.Body.String(), `"error":"the database is unavailable; send the same request again"`) ||
		strings.Contains(rec.Body.String(), "results") {
		t.Fatalf("database failure: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if testutil.ToFloat64(metrics.TrainImports.WithLabelValues("error")) != errors0+1 {
		t.Fatal("error counted")
	}
	if b.count(`SELECT count(*) FROM sessions WHERE id=$1`, good.ID) != 1 ||
		b.count(`SELECT count(*) FROM sessions WHERE id = ANY($1)`, []uuid.UUID{boom.ID, never.ID}) != 0 {
		t.Fatal("the items before the failure stay, none after it")
	}
	if evs := b.outbox(user); len(evs) != 1 || !slices.Equal(evs[0].Days, []string{day(daysAgo(9))}) {
		t.Fatalf("the finish ran for what is stored: %+v", evs)
	}
	if n := b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatal("the lease is given back")
	}
	lines := checkImportLog(t, b.logs.String(), []string{"boom", "Lower"}, []float64{101.25, 103.75, 106.25})
	if imp := lines["train import"]; len(imp) != 1 || imp[0]["result"] != "error" || imp[0]["created"] != 1.0 {
		t.Fatalf("train import: %v", imp)
	}
	if f := lines["request failed"]; len(f) != 1 || f[0]["status"] != 503.0 {
		t.Fatalf("request failed: %v", f)
	}
}

// A client that goes away mid-items: the items stop, what is stored is finished, the lease
// is given back, and the request is logged as 499.
func TestTrainImportClientGoesAway(t *testing.T) {
	b := newBench(t, defaultBeta())
	user, token := b.user("UTC", true)
	early, stuck := done(daysAgo(9), squatID, 101.25), done(daysAgo(4), squatID, 103.75)
	ctx := context.Background()
	tx, err := b.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`, user, stuck.ID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request(nil, stuck, early))
	client, leave := context.WithCancel(ctx)
	req := httptest.NewRequestWithContext(client, http.MethodPost, "/v1/train/import", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	served := make(chan struct{})
	go func() { b.srv.ServeHTTP(rec, req); close(served) }()
	for k := 0; b.count(`SELECT count(*) FROM sessions WHERE id=$1`, early.ID) == 0; k++ {
		if k > 500 {
			t.Fatal("the early item was never stored")
		}
		time.Sleep(10 * time.Millisecond)
	}
	leave()
	select {
	case <-served:
	case <-time.After(15 * time.Second):
		t.Fatal("the request did not end after the client left")
	}
	if rec.Code != 499 {
		t.Fatalf("logged as 499: %d", rec.Code)
	}
	if evs := b.outbox(user); len(evs) != 1 || !slices.Equal(evs[0].Days, []string{day(daysAgo(9))}) {
		t.Fatalf("the finish ran without the client: %+v", evs)
	}
	if n := b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, user); n != 0 {
		t.Fatal("the lease is given back")
	}
	lines := checkImportLog(t, b.logs.String(), []string{"Lower"}, []float64{101.25, 103.75})
	if len(lines["client closed the request"]) != 1 || len(lines["train import"]) != 1 || lines["train import"][0]["result"] != "timeout" {
		t.Fatalf("lines: %v", lines)
	}
	var access []float64
	for _, l := range lines["http"] {
		access = append(access, l["status"].(float64))
	}
	if !slices.Contains(access, 499) {
		t.Fatalf("access lines: %v", access)
	}
}

// ---- N3: another account's references; S1 at the endpoint --------------------------------

// Another account's custom exercise, plan day or plan exercise is unknown, in the same words
// as an id that exists nowhere; a set far from its session is the item's invalid.
func TestTrainImportTenancy(t *testing.T) {
	b := newBench(t, defaultBeta())
	_, tokenA := b.user("UTC", true)
	_, tokenC := b.user("UTC", true)
	mine := uuid.New()
	b.want(b.call(tokenA, http.MethodPost, "/v1/exercises", map[string]any{"id": mine, "name": "Landmine press", "kind": "barbell",
		"primary_muscles": []string{"front_delts"}, "load_type": "weight"}), http.StatusCreated, nil)
	pa := twoDayPlan()
	b.imports(tokenA, request([]model.Plan{pa}))
	random := uuid.New()

	sessionWith := func(ex uuid.UUID) model.ImportSession { return done(daysAgo(3), ex, 50) }
	planWith := func(ex uuid.UUID) model.Plan {
		p := twoDayPlan()
		p.Days[0].Exercises[0].ExerciseID = ex
		return p
	}
	day0, pe0 := done(daysAgo(4), squatID, 100), done(daysAgo(5), squatID, 100)
	day0.PlanDayID = &pa.Days[0].ID
	pe0.Exercises[0].PlanExerciseID = &pa.Days[0].Exercises[0].ID
	dayR, peR := done(daysAgo(6), squatID, 100), done(daysAgo(7), squatID, 100)
	dayR.PlanDayID, peR.Exercises[0].PlanExerciseID = &random, &random
	far := done(daysAgo(8), squatID, 100)
	late := far.EndedAt.Add(2 * time.Hour)
	far.Exercises[0].Sets[0].CompletedAt = &late

	res := b.imports(tokenC, request([]model.Plan{planWith(mine), planWith(random)},
		sessionWith(mine), sessionWith(random), day0, dayR, pe0, peR, far))
	want := []string{"plan rejected unknown_exercise", "plan rejected unknown_exercise", "session rejected unknown_exercise",
		"session rejected unknown_exercise", "session rejected unknown_plan_reference", "session rejected unknown_plan_reference",
		"session rejected unknown_plan_reference", "session rejected unknown_plan_reference", "session rejected invalid"}
	if got := statuses(res); !slices.Equal(got, want) {
		t.Fatalf("results %v, want %v", got, want)
	}
	m := func(k int) string { return res.Results[k].Message }
	same := func(k, l int, id uuid.UUID) bool {
		return m(k) == strings.ReplaceAll(m(l), random.String(), id.String())
	}
	if !same(0, 1, mine) || !same(2, 3, mine) || m(4) != m(5) || m(6) != m(7) || !strings.Contains(m(8), "completed_at") {
		t.Fatalf("another account's ids read like ids that exist nowhere: %+v", res.Results)
	}
}
