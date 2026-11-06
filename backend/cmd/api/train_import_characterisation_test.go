package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

// The training import pinned byte for byte (phase 2, C4): for each case the status, the
// headers and the raw body of every answer, then what the request left behind (the items in
// the order they were stored, the outbox, the queue, the records, the lease), the metrics it
// moved and the lines it logged. The goldens under testdata/train_import were recorded on the
// code before trainImport was split into steps; a refactor passes when they are unchanged.
//
// Only what varies between runs is normalised: request ids, dates (as D-<days back from
// today>, whatever today is: TestTrainImportGoldenDaysIgnoreTheCalendar) and the user id.
// Item ids are derived from the case name, so they are stable.
// RYFT_IMPORT_GOLDEN=update rewrites the files (review the diff: it must be empty).

const importGoldenDir = "testdata/train_import"

var (
	importGoldenNS = uuid.MustParse("5b2f6a52-0d4c-4b71-9e0e-2f1d6c7a8b90")
	dateRE         = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	requestIDRE    = regexp.MustCompile(`"request_id":"[^"]*"`)
)

// goldenCase is one case's transcript.
type goldenCase struct {
	t       *testing.T
	b       *bench
	name    string
	out     strings.Builder
	today   time.Time
	logMark int
	m0      map[string]float64
	users   map[uuid.UUID]string
	labels  map[uuid.UUID]string
	rids    []string // the request id of each answer recorded, in order
}

// importGoldenNames is every case with a golden file: a case not listed fails, and a file not
// listed (a case renamed or dropped) is an orphan that TestTrainImportGoldenFiles reports.
var importGoldenNames = []string{"consent_gate", "counts", "database_failure", "deadline", "empty_and_limit", "finish_fails",
	"flag_off", "id_taken", "idempotent_reimport", "invalid_items", "lease_taken", "newer_on_server", "oldest_first",
	"panic_in_finish", "plan_database_failure", "records_recomputed", "request_refusals", "resumed_all_unchanged"}

// awayFromMidnight waits out the last seconds before UTC midnight: a case builds its requests from
// today's date and the server reads it again, so a case spanning midnight would shift every day.
func awayFromMidnight(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	if left := now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now); left < 30*time.Second {
		time.Sleep(left + time.Second)
	}
}

func newGoldenCase(t *testing.T, b *bench, name string) *goldenCase {
	t.Helper()
	if !slices.Contains(importGoldenNames, name) {
		t.Fatalf("case %q is not in importGoldenNames", name)
	}
	awayFromMidnight(t)
	// expected_within_sec is the sync estimate plus 2 s a day; the estimate is DefaultExpectedSec
	// while fewer than 5 rows were scored in the last 6 hours (store.ExpectedScoreSec). Each case
	// starts without scored rows and post() checks the count, so the goldens never read a measured p95.
	if _, err := b.pool.Exec(context.Background(), `DELETE FROM score_queue WHERE scored_at IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	c := &goldenCase{t: t, b: b, name: name, today: utcDay(time.Now()), logMark: len(b.logs.String()),
		m0: importMetrics(), users: map[uuid.UUID]string{}, labels: map[uuid.UUID]string{}}
	fmt.Fprintf(&c.out, "case %s\n", name)
	return c
}

// user is a new account; the transcript calls it name.
func (c *goldenCase) user(name string, consent bool) (uuid.UUID, string) {
	id, token := c.b.user("UTC", consent)
	c.users[id] = name
	return id, token
}

func (c *goldenCase) id(parts ...string) uuid.UUID {
	return uuid.NewSHA1(importGoldenNS, []byte(c.name+"/"+strings.Join(parts, "/")))
}

// sess is an imported session n days back with deterministic ids, labelled name.
func (c *goldenCase) sess(name string, back int, ex uuid.UUID, kgs ...float64) model.ImportSession {
	s := done(daysAgo(back), ex, kgs...)
	s.ID = c.id("session", name)
	c.labels[s.ID] = "session:" + name
	for i := range s.Exercises {
		s.Exercises[i].ID = c.id("session", name, "exercise", fmt.Sprint(i))
		for j := range s.Exercises[i].Sets {
			s.Exercises[i].Sets[j].ID = c.id("session", name, "exercise", fmt.Sprint(i), "set", fmt.Sprint(j))
		}
	}
	return s
}

// plan is a two-day plan with deterministic ids, labelled name.
func (c *goldenCase) plan(name string) model.Plan {
	p := twoDayPlan()
	p.ID = c.id("plan", name)
	c.labels[p.ID] = "plan:" + name
	for k := range p.Days {
		p.Days[k].ID = c.id("plan", name, "day", fmt.Sprint(k))
		p.Days[k].Exercises[0].ID = c.id("plan", name, "day", fmt.Sprint(k), "exercise")
	}
	return p
}

// post sends the import and records the answer under step.
func (c *goldenCase) post(step, token string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	if n := c.b.count(`SELECT count(*) FROM score_queue WHERE scored_at > now() - interval '6 hours'`); n >= 5 {
		c.t.Fatalf("%d scored rows: the estimate would be measured, not the default", n)
	}
	rec := c.b.call(token, http.MethodPost, "/v1/train/import", body)
	c.record(step, rec)
	return rec
}

// days is the days_touched of an answer.
func (c *goldenCase) days(rec *httptest.ResponseRecorder) []string {
	var res model.TrainImportResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK {
		c.t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return res.DaysTouched
}

func (c *goldenCase) record(step string, rec *httptest.ResponseRecorder) {
	fmt.Fprintf(&c.out, "\n== %s\nstatus: %d\n", step, rec.Code)
	rid := rec.Header().Get("X-Request-Id")
	c.rids = append(c.rids, rid)
	keys := make([]string, 0, len(rec.Header()))
	for k := range rec.Header() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range rec.Header().Values(k) {
			if rid != "" {
				v = strings.ReplaceAll(v, rid, "<rid>")
			}
			fmt.Fprintf(&c.out, "header %s: %s\n", k, v)
		}
	}
	body := requestIDRE.ReplaceAllString(rec.Body.String(), `"request_id":"<rid>"`)
	fmt.Fprintf(&c.out, "body: %s<eof>\n", c.norm(body))
}

// norm names the dates relative to today.
func (c *goldenCase) norm(s string) string { return normDays(s, c.today) }

// policyText is where the policy version stands in an answer (the consent refusal). It is a
// date that does not move, so it stays as it is; every other date is a day and becomes
// D-<days before today>.
var policyText = "privacy policy " + benchPolicy

// policyMark holds policyText's place while the days are renamed.
const policyMark = "\x00policy-version\x00"

// normDays names every date in s by its distance from today (00:00 UTC), except the policy
// version, which is kept by where it stands, never by its value: a session day falls on the
// policy's date once the clock is that many days past it, and which session that is moves by
// one each calendar day (until 2026-11-06 the golden kept session m33's day as 2026-10-01).
func normDays(s string, today time.Time) string {
	s = strings.ReplaceAll(s, policyText, policyMark)
	s = dateRE.ReplaceAllStringFunc(s, func(d string) string {
		t, err := time.Parse(time.DateOnly, d)
		if err != nil {
			return d
		}
		return fmt.Sprintf("D-%d", int(today.Sub(t).Hours()/24))
	})
	return strings.ReplaceAll(s, policyMark, policyText)
}

func (c *goldenCase) label(id uuid.UUID) string {
	if l, ok := c.labels[id]; ok {
		return l
	}
	if l, ok := c.users[id]; ok {
		return "user:" + l
	}
	if id == squatID {
		return "exercise:squat"
	}
	return id.String()
}

func importMetrics() map[string]float64 {
	m := map[string]float64{"seconds/observations": importSeconds()}
	for _, r := range []string{"ok", "invalid", "disabled", "busy", "timeout", "error"} {
		m["imports/"+r] = testutil.ToFloat64(metrics.TrainImports.WithLabelValues(r))
	}
	for _, k := range []string{model.ImportKindPlan, model.ImportKindSession} {
		for _, s := range []string{store.ImportCreated, store.ImportUnchanged, store.ImportUpdated, store.ImportRejected} {
			m["items/"+k+"/"+s] = testutil.ToFloat64(metrics.TrainImportItems.WithLabelValues(k, s))
		}
	}
	return m
}

// importSeconds is the number of observations of ryft_train_import_seconds.
func importSeconds() float64 {
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(metrics.TrainImportSeconds)
	fams, err := reg.Gather()
	if err != nil || len(fams) != 1 || len(fams[0].GetMetric()) != 1 {
		return -1
	}
	return float64(fams[0].GetMetric()[0].GetHistogram().GetSampleCount())
}

// state appends what the case left behind for each user, the metrics and the log lines.
func (c *goldenCase) state(users ...uuid.UUID) {
	c.t.Helper()
	ctx := context.Background()
	for _, u := range users {
		fmt.Fprintf(&c.out, "\n== state of %s\n", c.label(u))
		rows, err := c.b.pool.Query(ctx, `SELECT id, origin, to_char(started_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
			(SELECT count(*) FROM session_sets ss JOIN session_exercises se ON se.id = ss.session_exercise_id WHERE se.session_id = s.id)
			FROM sessions s WHERE user_id = $1 ORDER BY xmin::text::bigint, id`, u)
		if err != nil {
			c.t.Fatal(err)
		}
		for rows.Next() {
			var id uuid.UUID
			var origin, started string
			var sets int
			if err := rows.Scan(&id, &origin, &started, &sets); err != nil {
				c.t.Fatal(err)
			}
			fmt.Fprintf(&c.out, "stored session %s origin=%s day=%s sets=%d\n", c.label(id), origin, c.norm(started), sets)
		}
		rows.Close()
		rows, err = c.b.pool.Query(ctx, `SELECT id FROM plans WHERE user_id = $1 ORDER BY created_at, id`, u)
		if err != nil {
			c.t.Fatal(err)
		}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				c.t.Fatal(err)
			}
			fmt.Fprintf(&c.out, "stored plan %s\n", c.label(id))
		}
		rows.Close()
		rows, err = c.b.pool.Query(ctx, `SELECT routing_key, reason, payload::text FROM outbox WHERE user_id = $1 ORDER BY id`, u)
		if err != nil {
			c.t.Fatal(err)
		}
		for rows.Next() {
			var key, reason, payload string
			if err := rows.Scan(&key, &reason, &payload); err != nil {
				c.t.Fatal(err)
			}
			var ev model.IngestedEvent
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				c.t.Fatal(err)
			}
			from := "unrecorded" // the event carries the request id of the answer that queued it
			if k := slices.Index(c.rids, ev.RequestID); k >= 0 && ev.RequestID != "" {
				from = fmt.Sprintf("#%d", k+1)
			}
			fmt.Fprintf(&c.out, "outbox %s reason=%s days=%s user=%s request=%s session_id_set=%t\n", key, reason,
				c.norm(strings.Join(ev.Days, ",")), c.label(ev.UserID), from, ev.SessionID != nil)
		}
		rows.Close()
		fmt.Fprintf(&c.out, "pending %s\n", c.norm(strings.Join(c.b.pending(u), ",")))
		fmt.Fprintf(&c.out, "lease held: %t\n", c.b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, u) == 1)
		rows, err = c.b.pool.Query(ctx, `SELECT exercise_id, kind, value, session_id FROM personal_records WHERE user_id = $1 ORDER BY exercise_id, kind`, u)
		if err != nil {
			c.t.Fatal(err)
		}
		for rows.Next() {
			var ex, sid uuid.UUID
			var kind string
			var v float64
			if err := rows.Scan(&ex, &kind, &v, &sid); err != nil {
				c.t.Fatal(err)
			}
			fmt.Fprintf(&c.out, "record %s %s=%v from %s\n", c.label(ex), kind, v, c.label(sid))
		}
		rows.Close()
	}
	c.metricsAndLogs()
}

func (c *goldenCase) metricsAndLogs() {
	fmt.Fprint(&c.out, "\n== metrics moved\n")
	now := importMetrics()
	keys := make([]string, 0, len(now))
	for k := range now {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if d := now[k] - c.m0[k]; d != 0 {
			fmt.Fprintf(&c.out, "%s +%v\n", k, d)
		}
	}
	fmt.Fprint(&c.out, "\n== log lines\n")
	for _, line := range strings.Split(strings.TrimSpace(c.b.logs.String()[c.logMark:]), "\n") {
		var l map[string]any
		if json.Unmarshal([]byte(line), &l) != nil {
			continue
		}
		switch msg, _ := l["msg"].(string); msg {
		case "train import":
			fmt.Fprintf(&c.out, "%s result=%v plans=%v sessions=%v created=%v unchanged=%v updated=%v rejected=%v days=%v level=%v\n", msg,
				l["result"], l["plans"], l["sessions"], l["created"], l["unchanged"], l["updated"], l["rejected"], l["days"], l["level"])
		case "request failed", "client closed the request", "panic":
			fmt.Fprintf(&c.out, "%s status=%v level=%v\n", msg, l["status"], l["level"])
		case "http":
			if strings.Contains(fmt.Sprint(l["route"]), "train/import") {
				fmt.Fprintf(&c.out, "http status=%v code=%v route=%v\n", l["status"], l["code"], l["route"])
			}
		}
	}
}

// check compares the transcript with its golden file, or writes it.
func (c *goldenCase) check() {
	c.t.Helper()
	path := filepath.Join(importGoldenDir, c.name+".golden")
	got := c.out.String()
	if os.Getenv("RYFT_IMPORT_GOLDEN") == "update" {
		if os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" {
			c.t.Fatal("RYFT_IMPORT_GOLDEN=update would make every case pass: not in CI")
		}
		if err := os.MkdirAll(importGoldenDir, 0o755); err != nil {
			c.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			c.t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		c.t.Fatalf("%v (RYFT_IMPORT_GOLDEN=update records it)", err)
	}
	if string(want) != got {
		c.t.Errorf("%s differs from its golden:\n--- want\n%s\n--- got\n%s", c.name, want, got)
	}
}

func goldenCases(t *testing.T, cases map[string]func(c *goldenCase, b *bench)) {
	b := newBench(t, defaultBeta())
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			c := newGoldenCase(t, b, n)
			cases[n](c, b)
			c.check()
		})
	}
}

// ---- the gates and the refusals: nothing is processed -------------------------------------

func TestTrainImportCharacteriseRefusals(t *testing.T) {
	goldenCases(t, map[string]func(*goldenCase, *bench){
		// The flag is the server's too (S4): 403 import_not_enabled, no lease, nothing stored.
		"flag_off": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			b.flag(false, 100)
			defer b.flag(true, 100)
			c.post("flag off", token, request(nil, c.sess("s", 3, squatID, 100)))
			b.flag(true, 0) // dark and not on the allow-list
			c.post("dark, not on the allow-list", token, request(nil, c.sess("s", 3, squatID, 100)))
			c.state(u)
		},
		// The consent gate comes before the flag and the body; warn acts as on here (S3).
		"consent_gate": func(c *goldenCase, b *bench) {
			u, token := c.user("a", false)
			c.post("no consent", token, request(nil, c.sess("s", 3, squatID, 100)))
			c.post("no consent, malformed body", token, `{"source":`)
			b.flag(false, 100)
			c.post("no consent, flag off", token, request(nil, c.sess("s", 3, squatID, 100)))
			b.flag(true, 100)
			c.state(u)
		},
		// A lease held by another request: 429 import_in_progress, Retry-After 10, no item
		// processed; after it is given back the same request goes through.
		"lease_taken": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			req := request(nil, c.sess("s", 3, squatID, 100))
			release, ok, err := b.st.AcquireImportLease(context.Background(), u, store.ImportLeaseTTL)
			if err != nil || !ok {
				c.t.Fatalf("lease: %v %v", ok, err)
			}
			c.post("lease taken", token, req)
			c.state(u)
			release(context.Background())
			c.post("lease free", token, req)
			c.state(u)
		},
		// Whole-request refusals: sources, limits, shapes. Nothing stored, no lease.
		"request_refusals": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			s := c.sess("s", 3, squatID, 100)
			raw, _ := json.Marshal(request(nil, s))
			many := func(n int) []model.ImportSession {
				out := make([]model.ImportSession, n)
				for k := range out {
					out[k] = c.sess(fmt.Sprint("m", k), 2+k, squatID, 100)
				}
				return out
			}
			plans := make([]model.Plan, 6)
			for k := range plans {
				plans[k] = c.plan(fmt.Sprint("p", k))
			}
			heavy := make([]model.ImportSession, 9)
			for i := range heavy {
				kgs := make([]float64, 280)
				for j := range kgs {
					kgs[j] = 60
				}
				heavy[i] = c.sess(fmt.Sprint("h", i), 30+i, squatID, kgs...)
			}
			dup := c.plan("dup")
			for _, x := range []struct {
				step string
				body any
			}{
				{"unknown source", strings.Replace(string(raw), "alpha_progression", "other_app", 1)},
				{"no source", `{"plans":[],"sessions":[]}`},
				{"malformed json", `{"source":`},
				{"unknown field", `{"source":"alpha_progression","extra":1}`},
				{"origin on a session", strings.Replace(string(raw), `"title":`, `"origin":"import","title":`, 1)},
				{"over 1 MiB", `{"source":"alpha_progression","plans":[],"sessions":[],"pad":"` + strings.Repeat("x", 1<<20) + `"}`},
				{"6 plans", request(plans)},
				{"51 sessions", request(nil, many(51)...)},
				{"a plan twice", request([]model.Plan{dup, dup})},
				{"a session twice", request(nil, s, s)},
				{"a NUL", strings.Replace(string(raw), `"title":"Lower"`, `"title":"Lo\u0000wer"`, 1)},
				{"over 2500 sets", request(nil, heavy...)},
			} {
				c.post(x.step, token, x.body)
			}
			c.state(u)
		},
		// An empty request is a 200 before the lease is taken; the limit itself is accepted.
		"empty_and_limit": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			c.post("source only", token, `{"source":"alpha_progression"}`)
			c.post("empty arrays", token, request(nil))
			many := make([]model.ImportSession, 50)
			for k := range many {
				many[k] = c.sess(fmt.Sprint("m", k), 2+k, squatID, 100)
			}
			c.post("50 sessions", token, request(nil, many...))
			c.state(u)
		},
	})
}

// ---- the items ----------------------------------------------------------------------------

func TestTrainImportCharacteriseItems(t *testing.T) {
	goldenCases(t, map[string]func(*goldenCase, *bench){
		// Rejections are the item's, in a 200, in request order; the good ones are stored.
		"invalid_items": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			good, bad := c.plan("good"), c.plan("bad")
			bad.Goal = "fun"
			newest, unknown := c.sess("newest", 3, squatID, 100, 105), c.sess("unknown", 10, uuid.New(), 50)
			unknown.Exercises[0].ExerciseID = c.id("no-such-exercise")
			refBad, notDone, assisted := c.sess("refbad", 9, squatID, 90), c.sess("notdone", 8, squatID, 95), c.sess("assisted", 7, squatID, 95)
			refBad.PlanDayID = &bad.Days[0].ID
			notDone.Exercises[0].Sets[0].CompletedAt = nil
			assisted.Exercises[0].Sets[0].AssistKG = ptrTo(17.5)
			planned := c.sess("planned", 5, squatID, 100)
			planned.PlanDayID = &good.Days[1].ID
			planned.Exercises[0].PlanExerciseID = &good.Days[1].Exercises[0].ID
			noID := c.sess("noid", 6, squatID, 100)
			noID.ID = uuid.Nil
			noIDPlan := c.plan("noid")
			noIDPlan.ID = uuid.Nil
			c.post("mixed", token, request([]model.Plan{good, bad, noIDPlan}, newest, unknown, refBad, notDone, assisted, planned, noID))
			c.post("every item rejected", token, request([]model.Plan{bad}, unknown))
			c.state(u)
		},
		// id_taken per item inside a 200: another account's plan, a built-in's, a session id
		// that is the app's, an exercise id that belongs to another of the user's sessions.
		"id_taken": func(c *goldenCase, b *bench) {
			a, tokenA := c.user("a", true)
			x, tokenX := c.user("x", true)
			p, s := c.plan("shared"), c.sess("shared", 4, squatID, 100)
			c.post("a imports", tokenA, request([]model.Plan{p}, s))
			builtin := c.plan("builtin")
			builtin.ID = train.BuiltinPlanID("full-body-3x")
			reuse := c.sess("reuse", 6, squatID, 90)
			reuse.Exercises[0].ID = s.Exercises[0].ID
			appMade := c.sess("appmade", 7, squatID, 80)
			live := appMade.ToSession() // the app's own session: started in the app, still running
			live.Status, live.EndedAt = model.SessionInProgress, nil
			b.want(b.call(tokenX, http.MethodPut, "/v1/sessions/"+appMade.ID.String(), live), http.StatusCreated, nil)
			own := c.sess("own", 5, squatID, 70)
			c.post("x imports what is a's, a built-in's, the app's", tokenX, request([]model.Plan{p, builtin}, s, appMade, own))
			ownReuse := c.sess("ownreuse", 8, squatID, 60)
			ownReuse.Exercises[0].Sets[0].ID = own.Exercises[0].Sets[0].ID
			c.post("x reuses its own set id", tokenX, request(nil, ownReuse))
			c.state(a, x)
		},
		// created, unchanged, updated and rejected side by side, for plans and sessions.
		"counts": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			keep, change := c.plan("keep"), c.plan("change")
			s1, s2, s3 := c.sess("one", 12, squatID, 100, 105, 110), c.sess("two", 10, squatID, 90), c.sess("three", 8, squatID, 80)
			b.scored(u, c.days(c.post("first (the baseline)", token, request([]model.Plan{keep, change}, s1, s2, s3)))...)
			change.Name = "Upper / Lower v2"
			edited := s2
			edited.Exercises = []model.SessionExercise{s2.Exercises[0]}
			edited.Exercises[0].Sets = slices.Clone(s2.Exercises[0].Sets)
			reps := 7
			edited.Exercises[0].Sets[0].Reps = &reps
			fresh := c.sess("fresh", 4, squatID, 120)
			invalid := c.sess("invalid", 3, squatID, 50)
			invalid.Exercises[0].Sets[0].CompletedAt = nil
			c.post("one of each", token, request([]model.Plan{keep, change}, s1, edited, fresh, invalid))
			c.state(u)
		},
		// Sessions are stored oldest first whatever their order in the request (T5); the
		// answer keeps the request's order; the records are those of all the history.
		"oldest_first": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			a, newest, mid := c.sess("a", 30, squatID, 100), c.sess("newest", 10, squatID, 110), c.sess("mid", 20, squatID, 105)
			c.post("newest, oldest, middle", token, request(nil, newest, a, mid))
			c.state(u)
		},
		// A resend: unchanged with its still-pending days (the event again), then, once
		// scored, nothing at all (no days, no new event).
		"idempotent_reimport": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			p, s := c.plan("p"), c.sess("s", 9, squatID, 100, 105)
			req := request([]model.Plan{p}, s)
			c.post("first", token, req)
			c.post("resend while pending", token, req)
			c.state(u)
			b.scored(u, day(daysAgo(9)))
			c.post("resend after scoring", token, req)
			c.state(u)
		},
		// A resumed request whose items are all unchanged (D140): the attempt before it died after
		// its item, so the item is stored and its day pending; the user's later session of the same
		// exercise, scored long ago, is queued with it because the pending item counts as changed.
		"resumed_all_unchanged": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			later, x := c.sess("later", 3, squatID, 130), c.sess("x", 20, squatID, 100)
			b.scored(u, b.imports(token, request(nil, later)).DaysTouched...)
			from, to := train.ImportWindow(time.Now(), time.UTC)
			out, err := b.st.ImportSession(context.Background(), u, model.ImportSourceAlphaProgression, x.ToSession(), time.UTC,
				store.DayWindow{From: from, To: to})
			if err != nil || out.Status != store.ImportCreated {
				c.t.Fatalf("the interrupted attempt's one item: %+v %v", out, err)
			}
			c.post("resend, every item unchanged", token, request(nil, x))
			c.state(u)
		},
		// The records follow the stored sessions: a heavier set raises them, an update that
		// drops it lowers them again.
		"records_recomputed": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			s := c.sess("s", 12, squatID, 100, 105, 110)
			c.post("created", token, request(nil, s))
			c.state(u)
			b.scored(u, day(daysAgo(12)))
			edited := s
			edited.Exercises = []model.SessionExercise{s.Exercises[0]}
			edited.Exercises[0].Sets = slices.Clone(s.Exercises[0].Sets[:2])
			c.post("heaviest set dropped", token, request(nil, edited))
			c.state(u)
		},
		// The app's edit wins over a resend: newer_on_server for the item, nothing queued.
		"newer_on_server": func(c *goldenCase, b *bench) {
			u, token := c.user("a", true)
			s := c.sess("s", 6, squatID, 100, 105)
			b.scored(u, b.imports(token, request(nil, s)).DaysTouched...)
			var stored map[string]any
			b.want(b.call(token, http.MethodGet, "/v1/sessions/"+s.ID.String(), nil), http.StatusOK, &stored)
			stored["title"] = "Renamed"
			b.want(b.call(token, http.MethodPut, "/v1/sessions/"+s.ID.String(), stored), http.StatusOK, nil)
			c.post("resend after the app's edit", token, request(nil, s))
			c.state(u)
		},
	})
}

// ---- the failure paths: 503, what stays stored, the lease ---------------------------------

func TestTrainImportCharacteriseFailures(t *testing.T) {
	b := newBench(t, defaultBeta())
	ctx := context.Background()
	for _, q := range []string{
		`CREATE FUNCTION ryft_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN IF NEW.title = 'boom' THEN RAISE EXCEPTION 'injected failure' USING ERRCODE = 'XX000'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER ryft_test_fail BEFORE INSERT OR UPDATE ON sessions FOR EACH ROW EXECUTE FUNCTION ryft_test_fail()`,
		`CREATE FUNCTION ryft_test_fail_plan() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN IF NEW.name = 'boom' THEN RAISE EXCEPTION 'injected failure' USING ERRCODE = 'XX000'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER ryft_test_fail_plan BEFORE INSERT OR UPDATE ON plans FOR EACH ROW EXECUTE FUNCTION ryft_test_fail_plan()`,
	} {
		if _, err := b.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	b.h.importFinish = time.Second
	t.Cleanup(func() { b.h.importFinish = 0 })
	cases := map[string]func(*goldenCase){
		// A database error that is not the item's stops the loop: 503 "the database is
		// unavailable", Retry-After 5, the items before it stored and finished.
		"database_failure": func(c *goldenCase) {
			u, token := c.user("a", true)
			good, boom, never := c.sess("good", 9, squatID, 101.25), c.sess("boom", 4, squatID, 103.75), c.sess("never", 2, squatID, 106.25)
			boom.Title = "boom"
			c.post("failure on the second", token, request([]model.Plan{c.plan("p")}, never, boom, good))
			c.state(u)
		},
		// A plan's database error stops the loop the same way: 503, the first plan stored, the
		// sessions never reached.
		"plan_database_failure": func(c *goldenCase) {
			u, token := c.user("a", true)
			good, boom := c.plan("good"), c.plan("boom")
			boom.Name = "boom"
			c.post("failure on the second plan", token, request([]model.Plan{good, boom}, c.sess("never", 4, squatID, 100)))
			c.state(u)
		},
		// A panic in the finish (here the insight cache is gone), after the days are queued: the
		// recovered 500 leaves the lease free, because the deferred release runs (D132).
		"panic_in_finish": func(c *goldenCase) {
			u, token := c.user("a", true)
			cache := b.h.cache
			b.h.cache = nil
			defer func() { b.h.cache = cache }()
			c.post("panic after the queue", token, request(nil, c.sess("s", 5, squatID, 100)))
			c.state(u)
		},
		// The items' time runs out: 503 "request timed out", Retry-After 5; the items before
		// the stop stay stored and are finished (queued, one event, records).
		"deadline": func(c *goldenCase) {
			u, token := c.user("a", true)
			stuck, early := c.sess("stuck", 4, squatID, 100), c.sess("early", 9, squatID, 95)
			tx, err := b.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7419003, hashtext($1::text || ':' || $2::text))`, u, stuck.ID); err != nil {
				t.Fatal(err)
			}
			srv := b.srv
			b.srv = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 4 s: the items get 3 s, the finish 1 s
				ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
				defer cancel()
				srv.ServeHTTP(w, r.WithContext(ctx))
			})
			defer func() { b.srv = srv }()
			t0 := time.Now()
			c.post("items out of time", token, request([]model.Plan{c.plan("p")}, stuck, early))
			// The items stop the finish's share (1 s) before the 4 s budget ends (D140), not at its end.
			if el := time.Since(t0); el < 2800*time.Millisecond || el > 3700*time.Millisecond {
				c.t.Fatalf("answered after %v: the items get the budget less the finish's second (about 3 s)", el)
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			c.state(u)
		},
		// The records step fails after the days were queued with their event (SF1): 503, the
		// resend comes back unchanged with the pending days and repairs the records.
		"finish_fails": func(c *goldenCase) {
			u, token := c.user("a", true)
			x, y := c.sess("x", 8, squatID, 100), c.sess("y", 5, squatID, 105)
			tx, err := b.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `LOCK TABLE personal_records IN SHARE MODE`); err != nil {
				t.Fatal(err)
			}
			c.post("records held", token, request(nil, x, y))
			c.state(u)
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			c.post("resend", token, request(nil, x, y))
			c.state(u)
		},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			c := newGoldenCase(t, b, n)
			cases[n](c)
			c.check()
		})
	}
	// The lease is given back after the finish, not before it (D132, D140): while the records
	// step waits, a second import of the same user is still 429.
	t.Run("lease_held_through_the_finish", func(t *testing.T) {
		awayFromMidnight(t)
		u, token := b.user("UTC", true)
		tx, err := b.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `LOCK TABLE personal_records IN SHARE MODE`); err != nil {
			t.Fatal(err)
		}
		var first *httptest.ResponseRecorder
		served := make(chan struct{})
		go func() {
			first = b.call(token, http.MethodPost, "/v1/train/import", request(nil, done(daysAgo(6), squatID, 100)))
			close(served)
		}()
		for k := 0; len(b.outbox(u)) == 0; k++ { // the days are queued with their event: the finish is in its records step
			if k > 600 {
				t.Fatal("the finish never queued the days")
			}
			time.Sleep(5 * time.Millisecond)
		}
		second := b.call(token, http.MethodPost, "/v1/train/import", request(nil, done(daysAgo(5), squatID, 100)))
		if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "10" ||
			b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, u) != 1 {
			t.Fatalf("second import during the finish: %d %v %s", second.Code, second.Header(), second.Body)
		}
		<-served
		if first.Code != http.StatusServiceUnavailable {
			t.Fatalf("the first import (records held): %d %s", first.Code, first.Body)
		}
		if b.count(`SELECT count(*) FROM users WHERE id=$1 AND import_lease_until IS NOT NULL`, u) != 0 {
			t.Fatal("the lease is given back before the answer")
		}
	})
}

// A golden file without a case (a case renamed or dropped) would pass for ever, and a case
// without a file cannot compare: the files and importGoldenNames are the same set. In update
// mode the orphans are removed.
func TestTrainImportGoldenFiles(t *testing.T) {
	update := os.Getenv("RYFT_IMPORT_GOLDEN") == "update"
	if update && (os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "") {
		t.Fatal("RYFT_IMPORT_GOLDEN=update would make every case pass: not in CI")
	}
	entries, err := os.ReadDir(importGoldenDir)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		name, golden := strings.CutSuffix(e.Name(), ".golden")
		have[name] = true
		if golden && slices.Contains(importGoldenNames, name) {
			continue
		}
		if update {
			if err := os.Remove(filepath.Join(importGoldenDir, e.Name())); err != nil {
				t.Fatal(err)
			}
			continue
		}
		t.Errorf("%s has no case: renamed or dropped? (RYFT_IMPORT_GOLDEN=update removes it)", e.Name())
	}
	for _, n := range importGoldenNames {
		if !have[n] && !update {
			t.Errorf("case %s has no golden file (RYFT_IMPORT_GOLDEN=update records it)", n)
		}
	}
}

// The goldens are recorded on one day and compared on every later one: what the normaliser
// makes of a case's days must not depend on which day that is. For reference days around the
// policy's date, the month, year, leap-day and DST changes, each day the import window holds,
// built as the cases build theirs (daysBefore, as daysAgo), is D-<n> and nothing else, also
// when it is the policy's date; the policy version in the consent refusal stays as it is.
func TestTrainImportGoldenDaysIgnoreTheCalendar(t *testing.T) {
	refusal := `{"code":"consent_required","error":"consent to the privacy policy ` + benchPolicy +
		` is required before uploading health data","request_id":"<rid>"}`
	policy, err := time.Parse(time.DateOnly, benchPolicy)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{
		"2026-10-01", // the policy's own date: D-0
		"2026-10-03", // the policy's date is the first session's (m0, two days back)
		"2026-10-25", // Europe/Berlin leaves summer time
		"2026-11-01", // a new month; America/New_York leaves summer time
		"2026-11-05", // the goldens were recorded: m33 (D-35) on the policy's date
		"2026-11-06", // the first day they failed: m34 (D-36) on it
		"2026-11-21", // the policy's date is the last session's (m49, D-51)
		"2026-12-31",
		"2027-01-01", // a new year
		"2027-03-28", // Europe/Berlin enters summer time
		"2027-11-04", // the policy's date is the import window's first day (D-399)
		"2028-02-29", // a leap day
		"2028-03-01",
	} {
		day0, err := time.Parse(time.DateOnly, ref)
		if err != nil {
			t.Fatal(err)
		}
		for _, now := range []time.Time{day0, day0.Add(23*time.Hour + 59*time.Minute + 59*time.Second)} {
			today := utcDay(now)
			for n := 0; n < train.ImportWindowDays; n++ {
				d := day(daysBefore(now, n))
				if got, want := normDays(d, today), fmt.Sprintf("D-%d", n); got != want {
					t.Fatalf("today %s: %s (%d days back) is %q, want %q", now.Format(time.RFC3339), d, n, got, want)
				}
			}
			if got := normDays(refusal, today); got != refusal {
				t.Fatalf("today %s: the consent refusal became %s", ref, got)
			}
			mixed := `"days_touched":["` + benchPolicy + `"] ` + refusal
			want := fmt.Sprintf(`"days_touched":["D-%d"] `, int(today.Sub(policy).Hours()/24)) + refusal
			if got := normDays(mixed, today); got != want {
				t.Fatalf("today %s: the policy's date as a day and as the version:\n got %s\nwant %s", ref, got, want)
			}
		}
	}
}
