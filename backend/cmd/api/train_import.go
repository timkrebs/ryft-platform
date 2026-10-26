package main

// POST /v1/train/import (docs/API.md → Train, D124-D133): plans and finished
// sessions from another training app's export, one item at a time. The rules are
// internal/train's (what is valid, what is the same), the storage
// internal/store/train_import.go's (one transaction per item); this file is the
// order of the steps, the finish and the answer.

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/timkrebs/ryft/internal/auth"
	"github.com/timkrebs/ryft/internal/config"
	"github.com/timkrebs/ryft/internal/events"
	"github.com/timkrebs/ryft/internal/flags"
	"github.com/timkrebs/ryft/internal/gate"
	"github.com/timkrebs/ryft/internal/httpx"
	"github.com/timkrebs/ryft/internal/metrics"
	"github.com/timkrebs/ryft/internal/model"
	"github.com/timkrebs/ryft/internal/scoring"
	"github.com/timkrebs/ryft/internal/store"
	"github.com/timkrebs/ryft/internal/train"
)

const (
	// importTimeout is the import's budget instead of readTimeout (D129), inside the
	// server's 60 s write timeout so that the answer is a JSON 503, not a cut connection.
	importTimeout = 50 * time.Second
	// importFinishTimeout is the finishing steps' own time (records, the queue, the event).
	// The items stop that much before the budget ends, so the finish fits inside it.
	importFinishTimeout = 10 * time.Second
	// importBusyRetry is the Retry-After of 429 import_in_progress (the contract's 10 s).
	importBusyRetry = 10
	// importRetry is the Retry-After of a 503: the database failed, or the time ran out.
	importRetry = 5
)

// importRules are internal/train's comparisons, which the store needs for the import
// (D131); main wires them in.
func importRules() store.ImportRules {
	return store.ImportRules{SameSession: train.SameSession, Conflict: train.ImportConflict, SamePlan: train.SamePlan,
		PlanConflict: train.PlanConflict}
}

// importRoute is POST /v1/train/import's chain: its own mux, so that its budget replaces
// readTimeout, behind the same authentication and invite gate as every other route; the
// rate limit takes one token from the import's own class (ratelimit, S2).
func importRoute(o auth.Options, mode config.Mode, fn http.HandlerFunc) http.Handler {
	imp := http.NewServeMux()
	imp.HandleFunc("POST /v1/train/import", fn)
	return auth.Middleware(o, betaGate(mode, httpx.Timeout(importTimeout, httpx.WithRoutes(imp))))
}

// importRun is one request's items as they are processed, by their index in the request.
type importRun struct {
	user     uuid.UUID
	source   string
	loc      *time.Location
	window   store.DayWindow
	plans    []store.ImportOutcome // Status "" until processed
	sessions []store.ImportOutcome
	bodies   []model.Session // the sessions as stored (normalised), for their starts
}

func (h *handler) trainImport(w http.ResponseWriter, r *http.Request) {
	result := "invalid"
	defer func() { metrics.TrainImports.WithLabelValues(result).Inc() }()
	u := userOf(r)
	// Before the body: a refused upload costs no parse (D129). On this route warn is on: no
	// app build that cannot ask for consent calls it, so nothing is served without (S3).
	consent := h.beta
	if consent.Consent == config.ModeWarn {
		consent.Consent = config.ModeOn
	}
	if !gate.Consent(w, r, consent, u, "api") {
		return
	}
	// The app's switch, enforced here too (S4): an account the flag is off for imports nothing.
	all, err := h.st.Flags(r.Context())
	if err != nil {
		result = importResult(r.Context(), err)
		importUnavailable(w, r, err, result)
		return
	}
	if !flags.Enabled(all, model.FlagTrainImport, u.ID) {
		result = "disabled"
		httpx.WriteCodedError(w, r, http.StatusForbidden, model.CodeImportNotEnabled, "the training import is not enabled for this account")
		return
	}
	var req model.TrainImportRequest
	if err := httpx.ReadJSON(w, r, &req, httpx.MaxBodyImport); err != nil {
		httpx.WriteDecodeError(w, err)
		return
	}
	if err := train.CheckImportRequest(req); err != nil {
		failTrain(w, r, err, "import")
		return
	}
	ctx := r.Context()
	if len(req.Plans) == 0 && len(req.Sessions) == 0 {
		result = "ok"
		base, _ := h.st.ExpectedScoreSec(ctx) // its default when the estimate fails
		httpx.WriteJSON(w, http.StatusOK, model.TrainImportResponse{Results: []model.ImportResult{}, DaysTouched: []string{},
			Scoring: model.SyncScoring{QueuedDays: []string{}, ExpectedWithinSec: base}})
		return
	}
	loc, err := h.location(r)
	if err != nil {
		result = "error"
		httpx.Fail(w, r, err)
		return
	}
	release, ok, err := h.st.AcquireImportLease(ctx, u.ID, store.ImportLeaseTTL)
	if err != nil {
		result = importResult(ctx, err)
		importUnavailable(w, r, err, result)
		return
	}
	if !ok {
		result = "busy"
		w.Header().Set("Retry-After", strconv.Itoa(importBusyRetry))
		httpx.WriteCodedError(w, r, http.StatusTooManyRequests, model.CodeImportInProgress, "an import is already running")
		return
	}
	// Given back before the answer is written (a retry right after it finds it free),
	// and by the defer on any other way out.
	var once sync.Once
	free := func() { once.Do(func() { release(ctx) }) }
	defer free()
	start := time.Now()

	from, to := train.ImportWindow(start, loc)
	run := &importRun{user: u.ID, source: req.Source, loc: loc, window: store.DayWindow{From: from, To: to},
		plans: make([]store.ImportOutcome, len(req.Plans)), sessions: make([]store.ImportOutcome, len(req.Sessions)),
		bodies: make([]model.Session, len(req.Sessions))}
	// The items stop before the budget ends, so that the finish has its time inside it.
	finish := cmp.Or(h.importFinish, importFinishTimeout)
	itemsCtx := ctx
	if dl, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		itemsCtx, cancel = context.WithDeadline(ctx, dl.Add(-finish))
		defer cancel()
	}
	stopped := h.importItems(itemsCtx, run, req)
	// Once, also after a failure or when the client has gone: what is stored gets its days
	// queued with its one event, then its records (D128).
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finish)
	defer cancel()
	days, finished := h.finishImport(fctx, r, run)
	base, _ := h.st.ExpectedScoreSec(fctx)
	free()
	metrics.TrainImportSeconds.Observe(time.Since(start).Seconds())
	err = errors.Join(stopped, finished)
	result = importResult(itemsCtx, stopped)
	if result == "ok" {
		result = importResult(fctx, finished)
	}
	h.logImport(r, run, days, start, result)
	if err != nil {
		importUnavailable(w, r, err, result)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, model.TrainImportResponse{Results: run.results(req), DaysTouched: days,
		Scoring: model.SyncScoring{QueuedDays: days, ExpectedWithinSec: train.ExpectedSeconds(base, len(days))}})
}

// importResult is the result label of an import that ended with err in ctx: ok, timeout
// (its time ran out, or the client went away), error (the database failed).
func importResult(ctx context.Context, err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil:
		return "timeout"
	}
	return "error"
}

// importUnavailable answers an import that could not finish: 503 with Retry-After, "request
// timed out" when its time ran out (also the items' share of it), else the database failed.
// What was stored stays stored, and the app sends the same request again (API.md → Retries).
// A client that went away gets nothing (logged as 499).
func importUnavailable(w http.ResponseWriter, r *http.Request, err error, result string) {
	if errors.Is(r.Context().Err(), context.Canceled) {
		httpx.Fail(w, r, err)
		return
	}
	msg := "the database is unavailable; send the same request again"
	if result == "timeout" {
		msg = "request timed out"
	}
	slog.Default().ErrorContext(r.Context(), "request failed", "status", http.StatusServiceUnavailable, "err", err)
	w.Header().Set("Retry-After", strconv.Itoa(importRetry))
	httpx.WriteError(w, http.StatusServiceUnavailable, msg)
}

// importItems stores the plans in request order, then the sessions oldest first (T5),
// each on its own (D131). A rejection is the item's; any other error stops the loop and is
// returned: the items before it stay stored.
func (h *handler) importItems(ctx context.Context, run *importRun, req model.TrainImportRequest) error {
	for i, p := range req.Plans {
		if p.ID == uuid.Nil {
			run.plans[i] = invalid(train.Problem{Msg: "id is required"})
			continue
		}
		if err := train.CheckPlan(p); err != nil {
			run.plans[i] = invalid(err)
			continue
		}
		out, err := h.st.ImportPlan(ctx, run.user, p)
		if err != nil {
			return err
		}
		run.plans[i] = out
	}
	var exIDs []uuid.UUID
	for i, s := range req.Sessions {
		run.bodies[i] = s.ToSession()
		for _, e := range s.Exercises {
			exIDs = append(exIDs, e.ExerciseID)
		}
	}
	if len(req.Sessions) == 0 {
		return nil
	}
	// For CheckAssist: the load type of every exercise the sessions name (one query).
	exercises, err := h.st.VisibleExercises(ctx, run.user, exIDs)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, i := range train.ImportOrder(run.bodies) {
		s := &run.bodies[i]
		if err := train.CheckImportSession(s, now, exercises); err != nil {
			run.sessions[i] = invalid(err)
			continue
		}
		out, err := h.st.ImportSession(ctx, run.user, run.source, *s, run.loc, run.window)
		if err != nil {
			return err
		}
		run.sessions[i] = out
	}
	return nil
}

// invalid is the outcome of an item that fails validation: its message names the field.
func invalid(err error) store.ImportOutcome {
	msg := "a value is invalid"
	var p train.Problem
	if errors.As(err, &p) {
		msg = p.Msg
	}
	return store.ImportOutcome{Status: store.ImportRejected, Code: store.ImportInvalid, Message: msg}
}

// finishImport runs once after the items (D128): the days queued with the request's one
// ingested event (reason import) in one transaction, then the records of every exercise a
// stored item has (unchanged ones too: a cheap repair after an interrupted attempt). In that
// order, so that a slow or failing records step never strands days that are stored but not
// queued: the 503 it causes leaves them pending, and the resend repairs the records. The days
// are those of the created and updated sessions (and the days an update moved them from),
// those of unchanged sessions that an earlier attempt queued and scoring has not finished,
// and the days of the user's other sessions whose load the changed ones can move
// (scoring.ScopeOf over the changed sessions and the versions they replaced, D135: a shared
// exercise within the e1RM window after the newest of them, or no body weight of their own
// after the oldest with one); all inside the window.
func (h *handler) finishImport(ctx context.Context, r *http.Request, run *importRun) ([]string, error) {
	var records []uuid.UUID
	var scope []model.Session
	changed := map[string]bool{}
	var unchanged []string
	for i, o := range run.sessions {
		switch o.Status {
		case store.ImportCreated, store.ImportUpdated:
			for _, d := range []string{o.Day, o.PreviousDay} {
				if d != "" && run.window.Contains(d) {
					changed[d] = true
				}
			}
			scope = append(scope, run.bodies[i])
			if o.Replaced != nil {
				scope = append(scope, *o.Replaced)
			}
		case store.ImportUnchanged:
			if run.window.Contains(o.Day) {
				unchanged = append(unchanged, o.Day)
			}
		}
		if o.Status != "" && o.Status != store.ImportRejected {
			records = append(records, o.Exercises...)
		}
	}
	pending, err := h.st.PendingAmong(ctx, run.user, unchanged)
	if err != nil {
		return nil, err
	}
	for i, o := range run.sessions {
		if o.Status == store.ImportUnchanged && slices.Contains(pending, o.Day) {
			changed[o.Day] = true
			scope = append(scope, run.bodies[i]) // its first attempt never got as far as the dependent days
		}
	}
	if d, ok := scoring.ScopeOf(scope); ok {
		deps, err := h.st.DependentDays(ctx, run.user, store.DependentScope{Since: d.Since, Exercises: d.Exercises,
			Through: d.Through, BodyweightSince: d.BodyweightSince}, run.window, run.loc)
		if err != nil {
			return nil, err
		}
		for _, day := range deps {
			changed[day] = true
		}
	}
	days := make([]string, 0, len(changed))
	for d := range changed {
		days = append(days, d)
	}
	slices.Sort(days)
	m, err := events.Ingested(model.IngestedEvent{UserID: run.user, Days: days, Reason: model.ReasonImport, RequestID: requestID(r)}, "")
	if err != nil {
		return nil, err
	}
	if err := h.st.FinishImport(ctx, run.user, days, m); err != nil {
		return nil, err
	}
	if len(days) > 0 && h.outbox != nil {
		h.outbox.Kick()
	}
	h.cache.forget(run.user)
	if len(records) > 0 {
		if err := h.recordsFor(ctx, run.user, uniqueIDs(records)); err != nil {
			return nil, err
		}
	}
	return days, nil
}

func uniqueIDs(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// results is one entry per submitted item: the plans, then the sessions, in request order.
func (run *importRun) results(req model.TrainImportRequest) []model.ImportResult {
	out := make([]model.ImportResult, 0, len(req.Plans)+len(req.Sessions))
	add := func(kind string, id uuid.UUID, o store.ImportOutcome) {
		x := model.ImportResult{Kind: kind, ID: id, Status: o.Status}
		if o.Status == store.ImportRejected {
			x.Code, x.Message = o.Code, o.Message
		}
		out = append(out, x)
	}
	for i, p := range req.Plans {
		add(model.ImportKindPlan, p.ID, run.plans[i])
	}
	for i, s := range req.Sessions {
		add(model.ImportKindSession, s.ID, run.sessions[i])
	}
	return out
}

// logImport counts the items and writes the request's one line: counts per status, the
// days, the duration; never a title, a note, a value or a list of ids (D114, D117), and not
// the source, which says which other app the user trains with (N6).
func (h *handler) logImport(r *http.Request, run *importRun, days []string, start time.Time, result string) {
	n := map[string]int{}
	for _, x := range []struct {
		kind string
		out  []store.ImportOutcome
	}{{model.ImportKindPlan, run.plans}, {model.ImportKindSession, run.sessions}} {
		for _, o := range x.out {
			if o.Status != "" {
				n[o.Status]++
				metrics.TrainImportItems.WithLabelValues(x.kind, o.Status).Inc()
			}
		}
	}
	h.log.InfoContext(r.Context(), "train import", "result", result,
		"plans", len(run.plans), "sessions", len(run.sessions), "created", n[store.ImportCreated],
		"unchanged", n[store.ImportUnchanged], "updated", n[store.ImportUpdated], "rejected", n[store.ImportRejected],
		"days", len(days), "duration_ms", time.Since(start).Milliseconds())
}
